/**
 * §21 #91 the install command as the hard fallback for any up- / downgrade — backend side:
 * - the script's sh version compare / schema rule == shared compareAgentVersions / agentSchemaCompatible (same vectors)
 * - unusable tokens → 200 + a tiny script that says why (exit 1); installBinary / installDeps / installRelease keep 410
 * - installRelease (pin / ?version= / not uploaded), schema metadata (release.json → headers + response + script)
 * - installScript ?version=<v> embeds that version; upload-agent.ts stores <dir>/release.json
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { agentInstallAction, agentSchemaCompatible, compareAgentVersions, type DepsManifest } from '@opm/shared';
import { abucket, adb, enrollSite, helloDeployment, here, person, PROJECT, raw, sha256Hex } from './helpers.ts';
import { installScript } from '../src/routes/install.ts';

const SAMPLE = {
  apiBase: 'https://api.opm.example',
  deploymentId: 'Dep0000000000000000a',
  siteSlug: 'app',
  token: 'opi_' + 'A'.repeat(43),
  releasePublicKey: 'Af0P9kVPsXG0cMwfCGgLSqZjaHwLViQDd6cKv1npyi0=',
  agent: { version: '0.10.0-1', files: { amd64: { sha256: 'a'.repeat(64), sig: 'sigA' } }, schema: 3, minSchema: 1 },
  docker: { version: '28.5.2', minApi: '1.41', sha256: { amd64: 'c'.repeat(64) } },
};
/** the helper functions of the generated script (from have() to the questions block) */
function shFns(): string {
  const sh = installScript(SAMPLE);
  return sh.slice(sh.indexOf('have() {'), sh.indexOf('# ── questions'));
}
const VECTORS = [
  '0.7.2-10080232', '0.7.2', '0.10.0', '0.10.0-10090000', '0.10.0-9', '0.10.0-10', '0.9.0', '0.10.1', '1.0.0', 'v0.10.0', ' 0.10.0 ',
  '0.10.0-beta', '0.10.0-alpha', '0.10.0-beta2', '0.10.0-10', '0.10.0+10', '0.10.0-007', '0.10.0-7', '10.0.0', '2.0.0',
  '0.10.0-9007199254740991', '0.10.0-900719925474099', 'garbage', '', '0.10', '0.10.0-', '00.010.000', '0.0.0',
];

test('sh ver_cmp == compareAgentVersions; schema_ok == agentSchemaCompatible; install actions', () => {
  const fns = shFns();
  const pairs: [string, string][] = [];
  for (const a of VECTORS) for (const b of VECTORS) pairs.push([a, b]);
  // one sh run for every pair (tab-separated lines)
  // (builds beyond 2^53 differ: TS compares doubles, sh compares the digits exactly — not used by MMDDHHMM stamps)
  const input = pairs.map(([a, b]) => `${a}|${b}`).join('\n') + '\n';
  const r = spawnSync('sh', ['-c', `LOG=/dev/null\nts() { :; }\nlogf() { :; }\n${fns}\nwhile IFS='|' read -r a b; do ver_cmp "$a" "$b"; done`], { input, encoding: 'utf8' });
  assert.equal(r.status, 0, r.stderr);
  const got = r.stdout.trim().split('\n');
  assert.equal(got.length, pairs.length);
  const bad = pairs.map(([a, b], i) => ({ a, b, sh: Number(got[i]), ts: Math.sign(compareAgentVersions(a, b)) })).filter((x) => x.sh !== x.ts);
  assert.deepEqual(bad, [], 'sh and TS disagree');
  // dash too
  const d = spawnSync('dash', ['-c', `LOG=/dev/null\nts() { :; }\nlogf() { :; }\n${fns}\nwhile IFS='|' read -r a b; do ver_cmp "$a" "$b"; done`], { input, encoding: 'utf8' });
  if (!d.error) assert.deepEqual(d.stdout.trim().split('\n'), got);
  // schema rule
  const cases: [number | null, number | null, number | null][] = [];
  for (const db of [null, 0, 1, 2, 3, 5]) for (const mx of [null, 1, 2, 3]) for (const mn of [null, 0, 1, 2]) cases.push([db, mx, mn]);
  const s = (n: number | null) => (n === null ? '' : String(n));
  const sr = spawnSync('sh', ['-c', `${fns}\nwhile IFS=, read -r d x n; do if schema_ok "$d" "$x" "$n"; then echo 1; else echo 0; fi; done`], { input: cases.map((c) => c.map(s).join(',')).join('\n') + '\n', encoding: 'utf8' });
  const sg = sr.stdout.trim().split('\n');
  cases.forEach(([db, mx, mn], i) => assert.equal(sg[i] === '1', agentSchemaCompatible(db, { schema: mx, minSchema: mn }), `schema ${db} vs ${mx}/${mn}`));
  // the script's ACTION mapping (installed vs target) = agentInstallAction
  for (const [i, t] of [['0.7.2-10080232', '0.10.0-1'], ['0.10.0-2', '0.10.0-1'], ['0.10.0-1', '0.10.0-1']] as const) {
    const c = Number(spawnSync('sh', ['-c', `${fns}\nver_cmp "$1" "$2"`, 'sh', i, t], { encoding: 'utf8' }).stdout.trim());
    assert.equal(c < 0 ? 'upgrade' : c > 0 ? 'downgrade' : 'same', agentInstallAction(i, t));
  }
  assert.equal(agentInstallAction(null, '0.10.0'), 'fresh');
});

test('script text: prompts via /dev/tty, env flags, rerun hint, backups, units, health + rollback; lint clean', () => {
  const sh = installScript({ ...SAMPLE, upgradeOnly: true, requestedVersion: '0.10.0-1' });
  const dir = mkdtempSync(join(tmpdir(), 'opm-fb-'));
  const file = join(dir, 'install.sh');
  writeFileSync(file, sh);
  execFileSync('sh', ['-n', file]);
  if (!spawnSync('dash', ['-n', file]).error) assert.equal(spawnSync('dash', ['-n', file]).status, 0);
  const sc = spawnSync('shellcheck', ['-s', 'sh', file], { encoding: 'utf8' });
  if (!sc.error) assert.equal((sc.stdout + sc.stderr).trim(), '', 'shellcheck');
  for (const s of [
    '(: </dev/tty) 2>/dev/null',
    'read -r ANS </dev/tty',
    'ANS_YES="${OPM_YES:-}"; ANS_DOCKER="${OPM_INSTALL_DOCKER:-}"; ANS_DOWN="${OPM_ALLOW_DOWNGRADE:-}"; ANS_RESTORE="${OPM_RESTORE_DB_BACKUP:-}"',
    '[ -z "$ANS_DOCKER" ] && [ "$SKIP_DOCKER" = 1 ] && ANS_DOCKER=0',
    'WANT_VERSION="${OPM_AGENT_VERSION:-}"; SOCKET="${OPM_SOCKET_ACTIVATION:-1}"',
    'SCRIPT_QS="?version=0.10.0-1"',
    'ASCHEMA="3"; AMINSCHEMA="1"',
    'Docker Engine is not installed. Install the pinned Docker $DOCKER_VERSION now?',
    '"OPM_INSTALL_DOCKER=1"',
    'Installed $OLD_VER → available $TARGET: $WORD',
    '"OPM_ALLOW_DOWNGRADE=1 OPM_YES=1"',
    '/v1/install/release?os=linux&arch=$ARCH&version=$WANT_VERSION',
    'openssl pkeyutl -verify -pubin',
    'printf \'version=%s\\nschema=%s\\ncreatedAt=%s\\nreason=install-script %s to %s\\n\'',
    'tail -n +6',
    'cp -p "$BIN" "$BIN.prev"',
    '"$BIN" systemd-units --install "$SOCK_FLAG"',
    '/_opm/api/health',
    'rollback "no healthy answer on 127.0.0.1:$AGENT_PORT with version $TARGET"',
    'Hard fallback for any up- / downgrade: rerun this same command',
  ])
    assert.ok(sh.includes(s), `missing: ${s}`);
  // the token is never written to the log by the rerun hint
  assert.ok(sh.includes('logf "rerun with: curl ... | sudo $* sh"'));
  assert.ok(!/docker (stop|rm|kill|restart|rmi|compose)/.test(sh), 'the script never touches containers');
});

async function uploadVersion(v: string, meta: { schema: number; minSchema: number } | null) {
  const bin = Buffer.from('bin ' + v);
  const base = `agent/${v}/linux-amd64/opm-agent`;
  await abucket.file(base).save(bin);
  await abucket.file(`${base}.sha256`).save(sha256Hex(bin) + '\n');
  await abucket.file(`${base}.sig`).save('sig-' + v + '\n');
  if (meta) await abucket.file(`agent/${v}/release.json`).save(JSON.stringify({ version: v, ...meta, commit: 'abc', builtAt: null }));
  return bin;
}

test('tokens: unusable → 200 tiny script (exit 1); installRelease / ?version= / schema headers', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  await uploadVersion('0.10.0-fb', { schema: 4, minSchema: 2 });
  await uploadVersion('0.9.0-fb', null);
  const manifest: DepsManifest = { docker: { version: '0.0.1-f', files: { amd64: { path: 'x', sha256: 'e'.repeat(64), size: 1 }, arm64: { path: 'y', sha256: 'f'.repeat(64), size: 1 } } }, minDockerApi: '1.41', agent: { version: '0.10.0-fb', notes: 'n', schema: 4, minSchema: 2 }, updatedAt: new Date().toISOString() };
  await abucket.file('deps/manifest.json').save(JSON.stringify(manifest));
  const app = await enrollSite(admin, id, 'app');
  const H = (t: string) => ({ Authorization: `Bearer ${t}` });
  const runBody = (body: string) => spawnSync('sh', ['-s'], { input: body, encoding: 'utf8' });

  // the enrolled site's used token: the real (upgrade-only) script, pin schema embedded
  let s = await raw('GET', '/v1/install/script', { headers: H(app.token) });
  assert.equal(s.status, 200);
  assert.ok(s.body.toString().includes('UPGRADE_ONLY="1"') && s.body.toString().includes('ASCHEMA="4"; AMINSCHEMA="2"'));
  // ?version= embeds that version (schema unknown → empty = legacy 1)
  s = await raw('GET', '/v1/install/script?version=0.9.0-fb', { headers: H(app.token) });
  assert.ok(s.body.toString().includes('AGENT_VERSION="0.9.0-fb"; ASCHEMA=""; AMINSCHEMA=""; SCRIPT_QS="?version=0.9.0-fb"'));
  s = await raw('GET', '/v1/install/script?version=7.7.7', { headers: H(app.token) });
  assert.equal(s.status, 200);
  let r = runBody(s.body.toString());
  assert.equal(r.status, 1);
  assert.match(r.stderr, /agent 7\.7\.7 is not uploaded/);
  // installRelease
  const rel = await raw('GET', '/v1/install/release?os=linux&arch=amd64', { headers: H(app.token) });
  assert.equal(rel.status, 200);
  assert.equal(rel.json.version, '0.10.0-fb');
  assert.equal(rel.json.schema, 4);
  assert.equal(rel.json.minSchema, 2);
  assert.equal(rel.json.notes, 'n');
  assert.equal(rel.json.binaryPath, '/v1/install/binary?os=linux&arch=amd64&version=0.10.0-fb');
  const rel9 = await raw('GET', '/v1/install/release?os=linux&arch=amd64&version=0.9.0-fb', { headers: H(app.token) });
  assert.equal(rel9.json.schema, null);
  assert.equal(rel9.json.notes, null);
  assert.equal(rel9.json.sha256, sha256Hex('bin 0.9.0-fb'));
  assert.equal((await raw('GET', '/v1/install/release?os=linux&arch=amd64&version=7.7.7', { headers: H(app.token) })).status, 404);
  assert.equal((await raw('GET', '/v1/install/release?os=linux&arch=s390x', { headers: H(app.token) })).status, 422);
  // installBinary schema headers + agentRelease schema
  const b = await raw('GET', '/v1/install/binary?os=linux&arch=amd64&version=0.10.0-fb', { headers: H(app.token) });
  assert.equal(b.headers.get('x-opm-agent-schema'), '4');
  assert.equal(b.headers.get('x-opm-agent-min-schema'), '2');
  const ar = await app.api.agentRelease({ query: { os: 'linux', arch: 'amd64' } });
  assert.equal(ar.schema, 4);
  assert.equal(ar.minSchema, 2);

  // expired / revoked / used-elsewhere / unknown → 200 tiny script, exit 1, nothing changed; binary etc. keep 410
  const toks = adb.collection('deployments').doc(id).collection('installTokens');
  const exp = await admin.api.createInstallToken({ deploymentId: id, body: { siteSlug: 'db' } });
  await toks.doc(exp.tokenHash).update({ expiresAt: new Date(Date.now() - 1000).toISOString() });
  s = await raw('GET', '/v1/install/script', { headers: H(exp.token) });
  assert.equal(s.status, 200);
  r = runBody(s.body.toString());
  assert.equal(r.status, 1);
  assert.match(r.stderr, /token expired \(site db\) — open the cloud Setup page → Install command → Regenerate \(or View\) and run the new command/);
  assert.equal((await raw('GET', '/v1/install/binary?os=linux&arch=amd64', { headers: H(exp.token) })).status, 410);
  assert.equal((await raw('GET', '/v1/install/release?os=linux&arch=amd64', { headers: H(exp.token) })).status, 410);
  assert.equal((await raw('GET', '/v1/install/deps/docker?arch=amd64', { headers: H(exp.token) })).status, 410);
  const rev = await admin.api.createInstallToken({ deploymentId: id, body: { siteSlug: 'db' } });
  await admin.api.revokeInstallToken({ deploymentId: id, tokenHash: rev.tokenHash });
  r = runBody((await raw('GET', '/v1/install/script', { headers: H(rev.token) })).body.toString());
  assert.equal(r.status, 1);
  assert.match(r.stderr, /token revoked \(site db\)/);
  // the app token after a revoke + re-enroll = used for an earlier enrollment
  await admin.api.revokeAgent({ deploymentId: id, siteSlug: 'app', body: { confirm: 'app' } });
  await enrollSite(admin, id, 'app');
  r = runBody((await raw('GET', '/v1/install/script', { headers: H(app.token) })).body.toString());
  assert.equal(r.status, 1);
  assert.match(r.stderr, /was used for an earlier enrollment \(site app\)/);
  r = runBody((await raw('GET', '/v1/install/script', { headers: H('opi_' + 'Z'.repeat(43)) })).body.toString());
  assert.match(r.stderr, /unknown install token/);
  assert.equal((await raw('GET', '/v1/install/script')).status, 401, 'no token at all stays 401');
  await abucket.file('deps/manifest.json').delete().catch(() => undefined);
});

test('upload-agent.ts stores <dir>/release.json as agent/<v>/release.json; upload-deps copies schema into the manifest', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'opm-rel-'));
  writeFileSync(join(dir, 'opm-agent-linux-amd64'), 'linux-amd64-rel');
  writeFileSync(join(dir, 'release.json'), JSON.stringify({ version: '0.10.0-rel', schema: 5, minSchema: 3, commit: 'c0ffee', builtAt: '2026-10-08T00:00:00Z' }));
  const env = { ...process.env, GCLOUD_PROJECT: PROJECT, OPM_RELEASE_PRIVATE_KEY: Buffer.alloc(32, 7).toString('base64') };
  const out = execFileSync('npx', ['tsx', resolve(here, '../scripts/upload-agent.ts'), '--dir', dir, '--version', '0.10.0-rel', '--project', PROJECT], { env, encoding: 'utf8' });
  assert.match(out, /release\.json \(schema 5, minSchema 3\)/);
  const m = JSON.parse((await abucket.file('agent/0.10.0-rel/release.json').download())[0].toString());
  assert.deepEqual(m, { version: '0.10.0-rel', schema: 5, minSchema: 3, commit: 'c0ffee', builtAt: '2026-10-08T00:00:00Z' });
  assert.throws(() => execFileSync('npx', ['tsx', resolve(here, '../scripts/upload-agent.ts'), '--dir', dir, '--version', '0.10.0-other', '--project', PROJECT], { env, encoding: 'utf8', stdio: 'pipe' }), /version 0\.10\.0-rel != --version 0\.10\.0-other/);
  for (const a of ['amd64', 'arm64']) writeFileSync(join(dir, `docker-0.0.8-${a}.tgz`), 'd');
  execFileSync('npx', ['tsx', resolve(here, '../scripts/upload-deps.ts'), '--project', PROJECT, '--from-dir', dir, '--docker-version', '0.0.8', '--agent-version', '0.10.0-rel'], { env, encoding: 'utf8' });
  const man = JSON.parse((await abucket.file('deps/manifest.json').download())[0].toString()) as DepsManifest;
  assert.deepEqual(man.agent, { version: '0.10.0-rel', notes: null, schema: 5, minSchema: 3 });
  await abucket.file('deps/manifest.json').delete();
});
