/**
 * Round G installer (SPEC-final §19.1): the generated script is POSIX sh (sh -n, shellcheck when available), pins every
 * version + sha256 at generation, installs the pinned Docker static bundle only when Docker is missing (unless
 * OPM_SKIP_DOCKER_INSTALL=1), and GET /v1/install/deps/{name} serves the pinned bundle with the install-token rules.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { DepsManifest } from '@opm/shared';
import { abucket, adb, helloDeployment, person, raw, sha256Hex } from './helpers.ts';
import { installScript } from '../functions/src/routes/install.ts';

const SAMPLE = {
  apiBase: 'https://api.opm.example',
  deploymentId: 'Dep0000000000000000a',
  siteSlug: 'app',
  token: 'opi_' + 'A'.repeat(43),
  releasePublicKey: 'Af0P9kVPsXG0cMwfCGgLSqZjaHwLViQDd6cKv1npyi0=',
  agent: { version: '0.7.0-test', files: { amd64: { sha256: 'a'.repeat(64), sig: 'sigA' }, arm64: { sha256: 'b'.repeat(64), sig: 'sigB' } } },
  docker: { version: '28.5.2', minApi: '1.41', sha256: { amd64: 'c'.repeat(64), arm64: 'd'.repeat(64) } },
  ports: { agent: 7080, alias: 8080, edge: 80 },
};

function shellcheck(file: string): { ran: boolean; out: string } {
  const sc = spawnSync('shellcheck', ['-s', 'sh', file], { encoding: 'utf8' });
  if (!sc.error) return { ran: true, out: sc.stdout + sc.stderr };
  return { ran: false, out: '' };
}

test('install script: sh -n, shellcheck, pinned versions, docker auto-install branch + OPM_SKIP_DOCKER_INSTALL guard', () => {
  const sh = installScript(SAMPLE);
  const dir = mkdtempSync(join(tmpdir(), 'opm-install-'));
  const file = join(dir, 'install.sh');
  writeFileSync(file, sh);
  execFileSync('sh', ['-n', file]); // throws on a syntax error
  const sc = shellcheck(file);
  if (sc.ran) assert.equal(sc.out.trim(), '', `shellcheck:\n${sc.out}`);
  // the token never appears on curl's command line (ps shows it to every local user): a 0600 header file in the temp dir
  assert.ok(sh.includes('-H @"$TMP/auth.header"') && sh.includes('(umask 077; printf \'Authorization: Bearer %s\\n\' "$TOKEN" > "$TMP/auth.header")'));
  assert.ok(!/curl [^\n]*-H "Authorization: Bearer \$TOKEN" "\$API\$1"/.test(sh), 'fetch() must not pass the token as an argument');
  // pinned (never "latest" at run time)
  assert.ok(!/latest/i.test(sh), 'the script must not resolve "latest"');
  assert.ok(sh.includes('AGENT_VERSION="0.7.0-test"'));
  assert.ok(sh.includes(`ASHA="${'a'.repeat(64)}"; ASIG="sigA"`) && sh.includes(`ASHA="${'b'.repeat(64)}"; ASIG="sigB"`));
  assert.ok(sh.includes('/v1/install/binary?os=linux&arch=$ARCH&version=$TARGET'));
  assert.ok(sh.includes('DOCKER_VERSION="28.5.2"; MIN_DOCKER_API="1.41"; MAX_DOCKER_API="1.47"'));
  assert.ok(sh.includes('! ver_ge "$MAX_DOCKER_API" "$MIN_V"'), 'existing Docker accepted when its min API <= the agent cap');
  assert.ok(sh.includes(`DSHA="${'c'.repeat(64)}"`) && sh.includes(`DSHA="${'d'.repeat(64)}"`));
  // Docker: detection, auto-install of the pinned static bundle, the skip guard, too-old / too-new existing Docker untouched
  assert.ok(sh.includes('Docker Engine not found — installing the pinned static bundle Docker $DOCKER_VERSION'));
  assert.ok(sh.includes('/v1/install/deps/docker?arch=$ARCH'));
  assert.ok(sh.includes('SKIP_DOCKER="${OPM_SKIP_DOCKER_INSTALL:-0}"'));
  assert.ok(sh.includes('[ -z "$ANS_DOCKER" ] && [ "$SKIP_DOCKER" = 1 ] && ANS_DOCKER=0'), 'OPM_SKIP_DOCKER_INSTALL=1 = OPM_INSTALL_DOCKER=0');
  assert.ok(sh.includes('https://docs.docker.com/engine/install/'));
  assert.ok(sh.includes('this script does not touch an existing Docker'));
  assert.ok(sh.includes('ExecStart=/usr/local/bin/dockerd -H fd:// --containerd=/run/containerd/containerd.sock'));
  assert.ok(sh.includes('"max-size": "10m", "max-file": "3"') && sh.includes('"live-restore": true'));
  assert.ok(sh.includes('/etc/opm/installed-deps.json'));
  assert.ok(sh.includes('systemctl enable --now docker'));
  // logging, failure handling, idempotency, dry run, verification
  assert.ok(sh.includes('LOG=/var/log/opm-install.log'));
  assert.ok(sh.includes('tail -n 20 "$LOG"'));
  assert.ok(sh.includes('already enrolled'));
  assert.ok(sh.includes('DRY="${DRY_RUN:-0}"'));
  assert.ok(sh.includes('/_opm/api/info'));
  assert.ok(sh.includes('hostname -I'));
  assert.ok(sh.includes('ALIAS_PORT="8080"') && sh.includes('EDGE_PORT="80"'));
  assert.ok(sh.includes('ExecStart=/usr/local/bin/opm-agent run --config /etc/opm/agent.json'));
  // §21 #86 final step: listening ports (port 80 first), firewall report (never changes rules), self-check, upgrade line
  assert.ok(sh.includes('[ "$UPGRADE_ONLY" = 1 ]') && sh.includes('UPGRADE_ONLY="0"'));
  assert.ok(sh.includes('"releasePublicKey":"%s"'), 'a new agent.json carries the release public key');
  assert.ok(sh.includes('ss -ltnH') && sh.includes('netstat -ltn') && sh.includes('/proc/net/tcp'));
  assert.ok(sh.includes('for p in "$HTTP_PORT" "$EDGE_PORT" 443 "$ALIAS_PORT" "$AGENT_PORT"; do'));
  assert.ok(sh.includes('sudo ufw allow $p/tcp') && sh.includes('sudo firewall-cmd --permanent --add-port=$p/tcp') && sh.includes('sudo firewall-cmd --reload'));
  assert.ok(sh.includes('no host firewall active (ufw / firewalld)'));
  assert.ok(!/ufw (allow|enable)[^\n]*\n\s*run /.test(sh) && !/run (sudo )?(ufw|firewall-cmd|iptables)/.test(sh), 'firewall rules are never changed');
  assert.ok(sh.includes("curl -s -o /dev/null -m 3 -w '%{http_code}'") && sh.includes('reachable from this host') && sh.includes('NOT reachable from this host'));
  assert.ok(sh.includes('A cloud firewall (e.g. DigitalOcean Cloud Firewall, AWS security group) or NAT in front of the server is invisible from here'));
  assert.ok(!/takeover/i.test(sh), 'no takeover wording: port 80 is the edge');
  assert.ok(sh.includes("port $HTTP_PORT is not listening — another program may hold it (ss -ltnp 'sport = :$HTTP_PORT'); the agent retries every 30 s; GUI also on$ALT_PORTS"));
  assert.ok(sh.includes('Upgrade later: rerun this same command (it stays valid for upgrades of this enrolled site), or run: sudo opm-agent upgrade (agent >= 0.8)'));
  // upgrade-only render: also sh -n / dash -n / shellcheck clean; refuses a server that is not the enrolled site
  const up = installScript({ ...SAMPLE, upgradeOnly: true, seenIp: '::ffff:139.59.2.56' });
  writeFileSync(file, up);
  execFileSync('sh', ['-n', file]);
  if (spawnSync('dash', ['-n', file]).error === undefined) assert.equal(spawnSync('dash', ['-n', file]).status, 0);
  const sc2 = shellcheck(file);
  if (sc2.ran) assert.equal(sc2.out.trim(), '', `shellcheck (upgrade-only):\n${sc2.out}`);
  assert.ok(up.includes('UPGRADE_ONLY="1"; SEEN_IP="139.59.2.56"'));
  assert.ok(up.includes('This install command already enrolled site $SITE; it can only upgrade that server'));
  assert.equal(installScript({ ...SAMPLE, seenIp: '1.2.3.4; rm -rf /' }).includes('SEEN_IP=""'), true, 'SEEN_IP is shell-safe');
  // §21 #90: boot guard step (agents >= 0.10.0 have `upgrade-guard`; older ones are skipped) + the GUI upgrade line
  assert.ok(up.includes('GUI_UPGRADE="0"') && up.includes('GH=$("$1" "$2" --help') && up.includes('"$BIN" upgrade-guard --install --data-dir "$DATA_DIR" --binary "$BIN"'));
  assert.ok(up.includes('DRY_RUN: would run: $BIN upgrade-guard --install --data-dir $DATA_DIR --binary $BIN'));
  assert.ok(up.includes('$PAST opm-agent $OLD_VER -> $(agent_ver)'));
  const gui = installScript({ ...SAMPLE, agent: { ...SAMPLE.agent, version: '0.10.0-10090000' }, upgradeOnly: true });
  writeFileSync(file, gui);
  execFileSync('sh', ['-n', file]);
  if (spawnSync('dash', ['-n', file]).error === undefined) assert.equal(spawnSync('dash', ['-n', file]).status, 0);
  const sc3 = shellcheck(file);
  if (sc3.ran) assert.equal(sc3.out.trim(), '', `shellcheck (0.10 upgrade-only):\n${sc3.out}`);
  assert.ok(gui.includes('GUI_UPGRADE="1"'));
  assert.ok(gui.includes('Upgrades from now on: cloud console Setup → Upgrade agent, or the agent GUI Agent page'));
  // no docker manifest → the script still parses and points to the manual install
  const noDeps = installScript({ ...SAMPLE, docker: null, agent: null, ports: undefined } as never);
  writeFileSync(file, noDeps);
  execFileSync('sh', ['-n', file]);
  assert.ok(noDeps.includes('DOCKER_VERSION=""') && noDeps.includes('the cloud has no pinned Docker bundle'));
});

test('install script docker API check: ver_ge accepts Docker 20.10 (1.41) and 29 (min 1.44 <= cap 1.47), rejects 1.40 / too new', () => {
  const sh = installScript(SAMPLE);
  const fn = sh.slice(sh.indexOf('ver_ge() {'), sh.indexOf('sha_of() {'));
  const check = (a: string, b: string) => spawnSync('sh', ['-c', `${fn}\nver_ge "${'$'}1" "${'$'}2"`, 'sh', a, b]).status === 0;
  assert.ok(check('1.51', '1.41'), 'Docker 28 max API 1.51 >= floor');
  assert.ok(check('1.41', '1.41'), 'Docker 20.10 (API 1.41) is accepted');
  assert.ok(!check('1.40', '1.41'), 'Docker 19.03 (API 1.40) is too old');
  assert.ok(check('1.47', '1.44'), 'Docker 29 min API 1.44 <= cap 1.47');
  assert.ok(!check('1.47', '1.50'), 'a daemon with min API 1.50 is above the cap');
});

test('install script runs: non-root fails at preflight with the manual hint (exit 1, log written)', () => {
  // runs the real script as the current (non-root) user with a private LOG path; it must stop at step 1
  const sh = installScript(SAMPLE).replace('LOG=/var/log/opm-install.log', `LOG=${join(mkdtempSync(join(tmpdir(), 'opm-log-')), 'install.log')}`);
  const r = spawnSync('sh', ['-s'], { input: sh, encoding: 'utf8' });
  if (process.getuid?.() === 0) return; // CI as root: covered by the container e2e
  assert.equal(r.status, 1, r.stdout + r.stderr);
  assert.match(r.stdout, /\[1\/7\] Checking this server/);
  assert.match(r.stdout, /FAILED at step 1\/7/);
  assert.match(r.stdout, /MANUAL: rerun with sudo/);
});

test('GET /v1/install/deps/docker: pinned bundle (sha + version headers), token rules; getSetup.installDeps; script embeds the pins', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const bundle = Buffer.from('fake docker bundle ' + id);
  const hex = sha256Hex(bundle);
  const path = 'deps/docker/0.0.1-test/arm64/docker-0.0.1-test.tgz';
  await abucket.file(path).save(bundle);
  const manifest: DepsManifest = {
    docker: { version: '0.0.1-test', files: { amd64: { path: path.replace('arm64', 'amd64'), sha256: 'e'.repeat(64), size: 1 }, arm64: { path, sha256: hex, size: bundle.length } } },
    minDockerApi: '1.41',
    agent: { version: '0.0.2-pinned' },
    updatedAt: new Date().toISOString(),
  };
  await abucket.file('deps/manifest.json').save(JSON.stringify(manifest));
  const tok = await admin.api.createInstallToken({ deploymentId: id, body: { siteSlug: 'app' } });
  const H = { Authorization: `Bearer ${tok.token}` };
  const r = await raw('GET', '/v1/install/deps/docker?arch=arm64', { headers: H });
  assert.equal(r.status, 200);
  assert.deepEqual(r.body, bundle);
  assert.equal(r.headers.get('x-opm-sha256'), hex);
  assert.equal(r.headers.get('x-opm-version'), '0.0.1-test');
  assert.equal((await raw('GET', '/v1/install/deps/docker?arch=s390x', { headers: H })).status, 422);
  assert.equal((await raw('GET', '/v1/install/deps/podman?arch=arm64', { headers: H })).status, 404);
  assert.equal((await raw('GET', '/v1/install/deps/docker?arch=arm64')).status, 401);
  assert.equal((await raw('GET', '/v1/install/deps/docker?arch=arm64', { headers: { Authorization: 'Bearer opi_unknown' } })).status, 401);
  // setup shows what the script installs
  const setup = await admin.api.getSetup({ deploymentId: id });
  assert.deepEqual(setup.installDeps, { dockerVersion: '0.0.1-test', minDockerApi: '1.41', agentVersion: '0.0.2-pinned' });
  // the script pins the manifest's versions + sha256 (agent version from the manifest, not agent/latest)
  const s = await raw('GET', '/v1/install/script', { headers: H });
  const sh = s.body.toString();
  assert.ok(sh.includes('DOCKER_VERSION="0.0.1-test"; MIN_DOCKER_API="1.41"; MAX_DOCKER_API="1.47"'));
  assert.ok(sh.includes(`DSHA="${hex}"`));
  assert.ok(sh.includes('AGENT_VERSION="0.0.2-pinned"'));
  // expired / revoked / used tokens → gone
  const tRef = adb.collection('deployments').doc(id).collection('installTokens');
  await tRef.doc(tok.tokenHash).update({ expiresAt: new Date(Date.now() - 1000).toISOString() });
  assert.equal((await raw('GET', '/v1/install/deps/docker?arch=arm64', { headers: H })).status, 410);
  const t2 = await admin.api.createInstallToken({ deploymentId: id, body: { siteSlug: 'app' } });
  await admin.api.revokeInstallToken({ deploymentId: id, tokenHash: t2.tokenHash });
  assert.equal((await raw('GET', '/v1/install/deps/docker?arch=arm64', { headers: { Authorization: `Bearer ${t2.token}` } })).status, 410);
  const t3 = await admin.api.createInstallToken({ deploymentId: id, body: { siteSlug: 'app' } });
  await tRef.doc(t3.tokenHash).update({ usedAt: new Date().toISOString() });
  assert.equal((await raw('GET', '/v1/install/deps/docker?arch=arm64', { headers: { Authorization: `Bearer ${t3.token}` } })).status, 410);
  // no manifest → setup null, deps 404
  await abucket.file('deps/manifest.json').delete();
  assert.equal((await admin.api.getSetup({ deploymentId: id })).installDeps, null);
  const t4 = await admin.api.createInstallToken({ deploymentId: id, body: { siteSlug: 'db' } });
  assert.equal((await raw('GET', '/v1/install/deps/docker?arch=arm64', { headers: { Authorization: `Bearer ${t4.token}` } })).status, 404);
});

test('install script final step: listen_ports (ss / netstat / /proc fallback) + gui_url (port 80 first, no :80 / :443)', () => {
  const sh = installScript(SAMPLE);
  const fns = sh.slice(sh.indexOf('have() {'), sh.indexOf('ver_ge() {')) + sh.slice(sh.indexOf('listen_ports() {'), sh.indexOf('CAND=""'));
  const dir = mkdtempSync(join(tmpdir(), 'opm-ss-'));
  // fake ss: LISTEN on 80 (v4 + v6), 7080, 8080
  writeFileSync(join(dir, 'ss'), '#!/bin/sh\nprintf "%s\\n" "LISTEN 0 4096 0.0.0.0:80 0.0.0.0:*" "LISTEN 0 4096 [::]:80 [::]:*" "LISTEN 0 4096 *:7080 *:*" "LISTEN 0 4096 *:8080 *:*"\n', { mode: 0o755 });
  const run = (body: string, path: string) => spawnSync('sh', ['-c', `LOG=/dev/null\nts() { :; }\nlogf() { :; }\n${fns}\n${body}`], { encoding: 'utf8', env: { ...process.env, PATH: path } });
  const r = run('echo "$LISTEN"; is_listening 80 && echo L80; is_listening 443 || echo N443; gui_url 1.2.3.4 80; gui_url 1.2.3.4 443; gui_url 1.2.3.4 8080; gui_url fe80::1 7080', `${dir}:/usr/bin:/bin`);
  assert.equal(r.status, 0, r.stderr);
  const lines = r.stdout.trim().split('\n');
  assert.equal(lines[0]!.trim(), '80 7080 8080');
  assert.deepEqual(lines.slice(1), ['L80', 'N443', 'http://1.2.3.4/_opm/', 'https://1.2.3.4/_opm/', 'http://1.2.3.4:8080/_opm/', 'http://[fe80::1]:7080/_opm/']);
});
