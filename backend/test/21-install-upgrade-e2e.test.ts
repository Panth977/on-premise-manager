/**
 * Round I (SPEC-final §21 #90 h): an ENROLLED site on an old agent (fake 0.7.2 binary: no `upgrade`, no `upgrade-guard`)
 * reruns the install command of its current enrollment (the used token → the upgrade-only script) in a plain ubuntu:24.04
 * container: the new pinned binary is installed (sha256 matches), the token is NOT written, the old → new version change is
 * printed, and the boot guard is installed with `opm-agent upgrade-guard --install …` (DRY_RUN prints the command; the
 * second run fakes systemd + the local GUI so the real call path runs). Needs Docker (Colima); OPM_E2E_INSTALL=0 skips.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { spawn, spawnSync } from 'node:child_process';
import { createServer, type Server } from 'node:http';
import { createPrivateKey, createPublicKey, randomBytes } from 'node:crypto';
import { installScript } from '../functions/src/routes/install.ts';
import { signHex } from '../scripts/upload-agent.ts';

const sh256 = (b: Buffer) => createHash('sha256').update(b).digest('hex');
function dockerArch(): 'amd64' | 'arm64' | null {
  if (process.env.OPM_E2E_INSTALL === '0') return null;
  const r = spawnSync('docker', ['info', '--format', '{{.Architecture}}'], { encoding: 'utf8', timeout: 20_000 });
  if (r.status !== 0) return null;
  const a = r.stdout.trim();
  return a === 'aarch64' || a === 'arm64' ? 'arm64' : a === 'x86_64' || a === 'amd64' ? 'amd64' : null;
}
const arch = dockerArch();

const NEW = '0.10.0-10090000';
const OLD = '0.7.2-10080232';
/** the "new" agent: knows verify-release, status, upgrade-guard (records its arguments) */
const NEW_BIN = Buffer.from(`#!/bin/sh
case "$1" in
  version) echo "opm-agent ${NEW} linux/${arch ?? 'amd64'}";;
  verify-release) exit 0;;
  status) if [ -f /var/lib/opm/identity.json ]; then echo '{"enrolled": true, "siteSlug": "app"}'; else echo '{"enrolled": false}'; fi;;
  upgrade-guard)
    case "$2" in
      --help) echo "usage: opm-agent upgrade-guard --install [--data-dir DIR] [--binary PATH] [--no-reload]"; exit 0;;
      --install) echo "$*" > /tmp/guard-called; exit 0;;
    esac; exit 2;;
  *) echo "unknown command: $1" >&2; exit 2;;
esac
`);
/** answers the local agent GUI check of step 6 (no real agent runs in the container); everything else = real curl */
const CURL_WRAP = `#!/bin/sh
case "$*" in
  *127.0.0.1:7080/_opm/api/info*) echo '{"enrolled": true}'; exit 0;;
  *127.0.0.1:7080/_opm/api/health*) /usr/local/bin/opm-agent version | grep -q ${NEW} || exit 22; echo '{"ok":true,"version":"${NEW}","schema":2,"cloud":{"connected":true}}'; exit 0;;
esac
exec /usr/bin/curl "$@"
`;
/** the old agent on the server (0.7.2: no upgrade, no upgrade-guard) */
const OLD_BIN = `#!/bin/sh
case "$1" in
  version) echo "opm-agent ${OLD} linux/${arch ?? 'amd64'}";;
  status) echo '{"enrolled": true}';;
  *) echo "unknown command: $1" >&2; exit 2;;
esac
`;

test('installer upgrade-only e2e: enrolled 0.7.2 site → pinned agent, no token, upgrade-guard --install', { skip: arch ? false : 'no Docker available', timeout: 900_000 }, async () => {
  const a = arch!;
  const seed = randomBytes(32);
  const priv = createPrivateKey({ key: Buffer.concat([Buffer.from('302e020100300506032b657004220420', 'hex'), seed]), format: 'der', type: 'pkcs8' });
  const pub = Buffer.from(createPublicKey(priv).export({ format: 'jwk' }).x!, 'base64url').toString('base64');
  const token = 'opi_' + 'U'.repeat(43);
  const dep = 'DepUpg00000000000001';
  let port = 0;
  const hits: string[] = [];
  const server: Server = createServer((req, res) => {
    hits.push(req.url ?? '');
    if (req.headers.authorization !== `Bearer ${token}`) return void res.writeHead(401).end();
    const u = new URL(req.url ?? '/', 'http://x');
    if (u.pathname === '/v1/install/script') {
      const body = installScript({
        apiBase: `http://host.docker.internal:${port}`,
        deploymentId: dep,
        siteSlug: 'app',
        token,
        releasePublicKey: pub,
        agent: { version: NEW, files: { [a]: { sha256: sh256(NEW_BIN), sig: signHex(sh256(NEW_BIN), seed.toString('base64')) } } },
        docker: { version: '28.5.2', minApi: '1.41', sha256: { [a]: 'c'.repeat(64) } },
        upgradeOnly: true,
        seenIp: '139.59.2.56',
      });
      return void res.writeHead(200, { 'Content-Type': 'text/x-shellscript' }).end(body);
    }
    if (u.pathname === '/v1/install/binary' && u.searchParams.get('arch') === a && u.searchParams.get('version') === NEW) return void res.writeHead(200, { 'X-OPM-Sha256': sh256(NEW_BIN) }).end(NEW_BIN);
    res.writeHead(404).end();
  });
  await new Promise<void>((r) => server.listen(0, '0.0.0.0', () => r()));
  port = (server.address() as { port: number }).port;
  const url = `http://host.docker.internal:${port}/v1/install/script`;
  const b64 = (s: string) => Buffer.from(s).toString('base64');
  try {
    const inner = [
      'set -e',
      'apt-get update -qq >/dev/null && apt-get install -y -qq curl ca-certificates >/dev/null',
      // existing Docker (fake CLI, API 1.43) so step 2 passes without the bundle
      'printf \'#!/bin/sh\\ncase "$1" in version) echo "1.43 1.12 24.0.7";; info) exit 0;; --version) echo "Docker version 24.0.7, build fake";; esac\\n\' > /usr/local/bin/docker; chmod +x /usr/local/bin/docker',
      // the enrolled site: old agent 0.7.2, agent.json of this deployment/site, an enrolled data dir
      `old() { echo ${b64(OLD_BIN)} | base64 -d > /usr/local/bin/opm-agent; chmod +x /usr/local/bin/opm-agent; }`,
      'old',
      'mkdir -p /etc/opm /var/lib/opm',
      `printf '{"apiBase":"http://x","deploymentId":"${dep}","siteSlug":"app"}\\n' > /etc/opm/agent.json`,
      `echo '{"siteSlug":"app"}' > /var/lib/opm/identity.json`,
      'echo "== BEFORE"; /usr/local/bin/opm-agent version; /usr/local/bin/opm-agent upgrade 2>&1 || true',
      `echo "== DRY"; curl -fsSL -H "Authorization: Bearer ${token}" ${url} | OPM_YES=1 DRY_RUN=1 sh; echo "== DRYEND"`,
      'echo "== SHA"; sha256sum /usr/local/bin/opm-agent; /usr/local/bin/opm-agent version; test -e /etc/opm/install-token && echo TOKEN-WRITTEN || echo no-token',
      // real path: fake systemd (systemctl + /run/systemd/system) and a curl wrapper answering the local agent GUI
      'old',
      'mkdir -p /run/systemd/system /etc/systemd/system',
      'printf \'#!/bin/sh\\necho "systemctl $*" >> /tmp/systemctl.log\\nexit 0\\n\' > /usr/local/sbin/systemctl; chmod +x /usr/local/sbin/systemctl',
      `echo ${b64(CURL_WRAP)} | base64 -d > /usr/local/sbin/curl; chmod +x /usr/local/sbin/curl`,
      `echo "== REAL"; curl -fsSL -H "Authorization: Bearer ${token}" ${url} | OPM_YES=1 sh; echo "== REALEND"`,
      'echo "== GUARD"; cat /tmp/guard-called',
      'echo "== SYSTEMCTL"; cat /tmp/systemctl.log',
      'echo "== UNIT"; grep ExecStart /etc/systemd/system/opm-agent.service',
      'echo "== AFTER"; sha256sum /usr/local/bin/opm-agent; test -e /etc/opm/install-token && echo TOKEN-WRITTEN || echo no-token',
    ].join('\n');
    const r = await new Promise<{ status: number | null; out: string }>((done) => {
      const p = spawn('docker', ['run', '--rm', '--add-host=host.docker.internal:host-gateway', 'ubuntu:24.04', 'sh', '-c', inner]);
      let out = '';
      p.stdout.on('data', (d) => (out += d));
      p.stderr.on('data', (d) => (out += d));
      const timer = setTimeout(() => p.kill('SIGKILL'), 800_000);
      p.on('close', (status) => {
        clearTimeout(timer);
        done({ status, out });
      });
    });
    const out = r.out;
    assert.equal(r.status, 0, out.slice(-5000));
    const sec = (a1: string, b1: string) => out.slice(out.indexOf(a1), out.indexOf(b1));
    const before = sec('== BEFORE', '== DRY');
    assert.match(before, new RegExp(`opm-agent ${OLD}`));
    assert.match(before, /unknown command: upgrade/, 'the old agent cannot upgrade itself');
    const dry = sec('== DRY', '== DRYEND');
    assert.match(dry, /This install command already enrolled site app: it upgrades \/ downgrades that server only/);
    assert.match(dry, /OK +upgrade of the enrolled site app/);
    assert.match(dry, new RegExp(`installed now: opm-agent ${OLD}`));
    assert.match(dry, new RegExp(`OK +upgraded opm-agent ${OLD} -> ${NEW}`));
    assert.match(dry, /already enrolled \(identity in \/var\/lib\/opm\): the same setup is kept, the install token is NOT used/);
    assert.match(dry, /Installed 0\.7\.2-10080232 → available 0\.10\.0-10090000: UPGRADE/);
    assert.match(dry, /release signature verified by openssl/);
    assert.match(dry, /DRY_RUN: would run: \/usr\/local\/bin\/opm-agent upgrade-guard --install --data-dir \/var\/lib\/opm --binary \/usr\/local\/bin\/opm-agent/);
    assert.match(dry, /Upgrades from now on: cloud console Setup → Upgrade agent, or the agent GUI Agent page/);
    assert.ok(!/FAILED/.test(dry), dry);
    const shaSec = sec('== SHA', '== REAL');
    assert.ok(shaSec.includes(sh256(NEW_BIN)), shaSec);
    assert.match(shaSec, new RegExp(`opm-agent ${NEW}`));
    assert.match(shaSec, /no-token/);
    const real = sec('== REAL', '== REALEND');
    assert.ok(!/FAILED/.test(real), real);
    assert.match(real, new RegExp(`OK +upgraded opm-agent ${OLD} -> ${NEW}`));
    assert.match(real, /OK +boot guard \(rollback of a failed self-upgrade\): \/usr\/local\/lib\/opm\/upgrade-guard\.sh/);
    assert.match(real, /OK +opm-agent 0\.10\.0-10090000 is healthy/);
    assert.match(sec('== GUARD', '== SYSTEMCTL'), /^upgrade-guard --install --data-dir \/var\/lib\/opm --binary \/usr\/local\/bin\/opm-agent$/m);
    const sc = sec('== SYSTEMCTL', '== UNIT');
    assert.match(sc, /systemctl stop opm-agent/);
    assert.match(sc, /systemctl restart opm-agent/);
    // the guard runs before the restart (it installs the ExecStartPre drop-in)
    const after = out.slice(out.indexOf('== AFTER'));
    assert.ok(after.includes(sh256(NEW_BIN)));
    assert.match(after, /no-token/);
    assert.equal(hits.filter((h) => h.startsWith('/v1/install/binary')).length, 2, 'one download per run (the old binary was put back)');
  } finally {
    server.close();
  }
});
