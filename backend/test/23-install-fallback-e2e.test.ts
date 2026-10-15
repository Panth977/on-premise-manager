/**
 * §21 #91 the install command as the hard fallback for any up- / downgrade — ubuntu:24.04 container (Docker / Colima;
 * OPM_E2E_INSTALL=0 skips). One server walks through: Docker missing (no terminal → no; fake TTY "n"; OPM_INSTALL_DOCKER=1
 * DRY_RUN installs the pinned bundle), upgrade 0.7.2 → pin (TTY no / no-terminal rerun hint / TTY yes: DB backup +
 * meta.env, .prev, agent.json missing key added, units by `systemd-units --install --socket`, health), same-version
 * reinstall (cloud connected required), an unhealthy target rolled back (DB restored when the schema differs), downgrade
 * (second confirmation, schema refusal without a compatible backup, restore offer: TTY no / flags yes → DB restored,
 * legacy unit + socket removed), 5 newest backups kept, and the docker CLI only ever asked for version / info.
 * Fakes: systemctl (logs), docker (logs), curl wrapper (/_opm/api/health + /_opm/api/info answered by the INSTALLED fake
 * agent → a restart = a binary swap), fake agents (shell scripts with real Ed25519 release signatures; openssl verifies).
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createHash, createPrivateKey, createPublicKey, randomBytes } from 'node:crypto';
import { spawn, spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { createServer, type Server } from 'node:http';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
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
const OLD = '0.7.2-10080232';
const PIN = '0.10.0-10090000';
const BAD = '0.10.1-10090100';
const DOWN = '0.9.5-10080500';

interface Fake { version: string; json: boolean; schema?: number; minSchema?: number; healthy: boolean; units: boolean }
function fakeAgent(f: Fake): Buffer {
  const meta = `{"version":"${f.version}","schema":${f.schema ?? 1},"minSchema":${f.minSchema ?? 1},"commit":null,"builtAt":null}`;
  const sub = (name: string, on: boolean, act: string) =>
    `  ${name}) case "$2" in --help) ${on ? 'echo "usage: opm-agent ' + name + ' --install"; exit 0' : 'echo "unknown command: ' + name + '" >&2; exit 2'};; --install) ${on ? act : 'exit 2'};; esac;;`;
  return Buffer.from(`#!/bin/sh
# fake opm-agent ${f.version}
case "$1" in
  version) ${f.json ? `if [ "\${2:-}" = --json ]; then echo '${meta}'; else echo "opm-agent ${f.version} linux/x"; fi` : `echo "opm-agent ${f.version} linux/x"`};;
  verify-release) exit 0;;
  status) echo '{"enrolled": true}';;
${sub('systemd-units', f.units, `echo "systemd-units $*" >> /tmp/units.log; [ "\${3:-}" = --socket ] && touch /etc/systemd/system/opm-agent.socket; echo "[Service]" > /etc/systemd/system/opm-agent.service; exit 0`)}
${sub('upgrade-guard', f.units, 'echo "upgrade-guard $*" >> /tmp/units.log; exit 0')}
  fake-health) ${f.json && f.healthy ? `echo '{"ok":true,"version":"${f.version}","schema":${f.schema ?? 1},"pid":1,"startedAt":"x","cloud":{"connected":true,"route":"direct","lastContactAt":null},"docker":{"ok":true},"socketActivated":true,"checks":[]}'` : 'exit 22'};;
  fake-info) ${f.healthy ? `echo '{"deploymentId":"D","siteSlug":"app","agentVersion":"${f.version}","enrolled": true}'` : 'exit 7'};;
  *) echo "unknown command: $1" >&2; exit 2;;
esac
`);
}
const SYSTEMCTL = `#!/bin/sh
echo "systemctl $*" >> /tmp/systemctl.log
exit 0
`;
const DOCKER = `#!/bin/sh
echo "docker $*" >> /tmp/docker.log
case "$1" in version) echo "1.43 1.12 24.0.7";; info) exit 0;; --version) echo "Docker version 24.0.7, build fake";; *) exit 0;; esac
`;
const CURL = `#!/bin/sh
case "$*" in
  *127.0.0.1:7080/_opm/api/health*) exec /usr/local/bin/opm-agent fake-health;;
  *127.0.0.1:7080/_opm/api/info*) exec /usr/local/bin/opm-agent fake-info;;
esac
exec /usr/bin/curl "$@"
`;

test('install command = hard fallback: prompts, flags, upgrade / same / rollback / downgrade, backups, no container touched', { skip: arch ? false : 'no Docker available', timeout: 1_200_000 }, async () => {
  const a = arch!;
  const seed = randomBytes(32);
  const priv = createPrivateKey({ key: Buffer.concat([Buffer.from('302e020100300506032b657004220420', 'hex'), seed]), format: 'der', type: 'pkcs8' });
  const pub = Buffer.from(createPublicKey(priv).export({ format: 'jwk' }).x!, 'base64url').toString('base64');
  const seedB64 = seed.toString('base64');
  const bins: Record<string, { bin: Buffer; schema: number | null; minSchema: number | null }> = {
    [PIN]: { bin: fakeAgent({ version: PIN, json: true, schema: 2, minSchema: 1, healthy: true, units: true }), schema: 2, minSchema: 1 },
    [BAD]: { bin: fakeAgent({ version: BAD, json: true, schema: 3, minSchema: 1, healthy: false, units: true }), schema: 3, minSchema: 1 },
    [DOWN]: { bin: fakeAgent({ version: DOWN, json: false, healthy: true, units: false }), schema: null, minSchema: null },
  };
  // the Docker "bundle": docker/docker = the logging shim, docker/dockerd
  const bd = mkdtempSync(join(tmpdir(), 'opm-fb-docker-'));
  mkdirSync(join(bd, 'docker'));
  writeFileSync(join(bd, 'docker', 'docker'), DOCKER, { mode: 0o755 });
  writeFileSync(join(bd, 'docker', 'dockerd'), '#!/bin/sh\nexit 0\n', { mode: 0o755 });
  spawnSync('tar', ['-czf', join(bd, 'docker.tgz'), '-C', bd, 'docker']);
  const tgz = readFileSync(join(bd, 'docker.tgz'));
  const token = 'opi_' + 'F'.repeat(43);
  const dep = 'DepFallback000000001';
  let port = 0;
  const hits: string[] = [];
  const server: Server = createServer((req, res) => {
    hits.push(req.url ?? '');
    if (req.headers.authorization !== `Bearer ${token}`) return void res.writeHead(401).end();
    const u = new URL(req.url ?? '/', 'http://x');
    const v = u.searchParams.get('version');
    if (u.pathname === '/v1/install/script') {
      const pin = bins[PIN]!;
      const sha = sh256(pin.bin);
      const body = installScript({
        apiBase: `http://host.docker.internal:${port}`,
        deploymentId: dep,
        siteSlug: 'app',
        token,
        releasePublicKey: pub,
        agent: { version: PIN, files: { [a]: { sha256: sha, sig: signHex(sha, seedB64) } }, schema: pin.schema, minSchema: pin.minSchema },
        docker: { version: '28.5.2', minApi: '1.41', sha256: { [a]: sh256(tgz) } },
        upgradeOnly: true,
      });
      return void res.writeHead(200, { 'Content-Type': 'text/x-shellscript' }).end(body);
    }
    if (u.pathname === '/v1/install/release' && v && bins[v]) {
      const x = bins[v]!;
      const sha = sh256(x.bin);
      return void res.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify({ version: v, os: 'linux', arch: a, sha256: sha, signature: signHex(sha, seedB64), releasePublicKey: pub, binaryPath: `/v1/install/binary?os=linux&arch=${a}&version=${v}`, notes: null, sizeBytes: x.bin.length, schema: x.schema, minSchema: x.minSchema }));
    }
    if (u.pathname === '/v1/install/binary' && v && bins[v]) return void res.writeHead(200).end(bins[v]!.bin);
    if (u.pathname === '/v1/install/deps/docker') return void res.writeHead(200).end(tgz);
    res.writeHead(404).end();
  });
  await new Promise<void>((r) => server.listen(0, '0.0.0.0', () => r()));
  port = (server.address() as { port: number }).port;
  const url = `http://host.docker.internal:${port}/v1/install/script`;
  const b64 = (s: string | Buffer) => Buffer.from(s).toString('base64');
  const put = (path: string, body: string | Buffer) => `echo ${b64(body)} | base64 -d > ${path}; chmod +x ${path}`;
  const pipe = (env = '') => `curl -fsSL -H "Authorization: Bearer ${token}" ${url} | ${env} sh`;
  /** answers typed on a fake terminal (script(1) gives the installer a /dev/tty) */
  const tty = (answers: string, env = '') => `(printf '${answers}'; sleep 2) | script -q -e -c '${pipe(env)}' /dev/null`;
  const sc = (name: string, cmd: string) => [`echo "== ${name}"`, `${cmd}`, `echo "rc=$?"`, `echo "bin=$(sha256sum /usr/local/bin/opm-agent | cut -d' ' -f1)"`, `echo "== END ${name}"`];
  try {
    const inner = [
      'apt-get update -qq >/dev/null && apt-get install -y -qq curl ca-certificates openssl >/dev/null',
      'mkdir -p /etc/opm /var/lib/opm/backup /run/systemd/system /etc/systemd/system',
      put('/usr/local/sbin/systemctl', SYSTEMCTL),
      put('/usr/local/sbin/curl', CURL),
      put('/usr/local/bin/opm-agent', fakeAgent({ version: OLD, json: false, healthy: true, units: false })),
      // agent.json (pretty, WITHOUT releasePublicKey → the script adds it), enrolled data dir, the DB
      `printf '{\\n  "apiBase": "http://x",\\n  "deploymentId": "${dep}",\\n  "siteSlug": "app",\\n  "dataDir": "/var/lib/opm"\\n}\\n' > /etc/opm/agent.json`,
      'printf "DB-v1" > /var/lib/opm/opm.db; printf "WAL-v1" > /var/lib/opm/opm.db-wal',
      // 4 older backups written by earlier runs (the 5 newest are kept)
      'for i in 1 2 3 4; do d=/var/lib/opm/backup/0.6.$i-2026010${i}T000000Z; mkdir -p $d; printf "version=0.6.$i\\nschema=1\\n" > $d/meta.env; touch -d "2026-01-0$i" $d; done',
      // ── Docker missing
      ...sc('D-NOTTY', pipe()),
      ...sc('D-TTY-NO', tty('n\\n')),
      ...sc('D-YES-DRY', pipe('OPM_INSTALL_DOCKER=1 DRY_RUN=1')),
      'ls /usr/local/bin/dockerd >/dev/null && echo "dockerd-installed"',
      // ── upgrade 0.7.2 → pin
      ...sc('U-TTY-NO', tty('n\\n')),
      ...sc('U-NOTTY', pipe()),
      ...sc('U-TTY-YES', tty('y\\n')),
      'echo "== STATE-U"; cat /etc/opm/agent.json; ls /etc/opm; cat /tmp/units.log; sha256sum /usr/local/bin/opm-agent.prev; ls -1 /var/lib/opm/backup; cat /var/lib/opm/backup/0.7.2-*/meta.env; cat /var/lib/opm/backup/0.7.2-*/opm.db; echo; echo "== END STATE-U"',
      // ── same version, reinstall (the old agent reported cloud connected → required again)
      ...sc('SAME', pipe('OPM_YES=1')),
      // ── unhealthy target → rollback (schema 2 → 3 differs: DB restored)
      'printf "DB-v2" > /var/lib/opm/opm.db',
      ...sc('BAD', pipe(`OPM_YES=1 OPM_AGENT_VERSION=${BAD} OPM_HEALTH_TIMEOUT=6`)),
      'echo "== STATE-BAD"; cat /var/lib/opm/opm.db; echo; echo "== END STATE-BAD"',
      // ── downgrade pin (schema 2) → 0.9.5 (legacy schema 1): refused without a compatible backup
      'mkdir -p /tmp/bk && mv /var/lib/opm/backup/* /tmp/bk/',
      ...sc('DOWN-REFUSED', pipe(`OPM_YES=1 OPM_ALLOW_DOWNGRADE=1 OPM_AGENT_VERSION=${DOWN}`)),
      'mv /tmp/bk/* /var/lib/opm/backup/',
      ...sc('DOWN-NOTTY', pipe(`OPM_YES=1 OPM_AGENT_VERSION=${DOWN}`)),
      ...sc('DOWN-TTY-NO-RESTORE', tty('y\\ny\\nn\\n', `OPM_AGENT_VERSION=${DOWN}`)),
      ...sc('DOWN-YES', pipe(`OPM_YES=1 OPM_ALLOW_DOWNGRADE=1 OPM_RESTORE_DB_BACKUP=1 OPM_AGENT_VERSION=${DOWN}`)),
      'echo "== STATE-DOWN"; cat /var/lib/opm/opm.db; echo; ls /etc/systemd/system; grep ExecStart /etc/systemd/system/opm-agent.service; ls -1 /var/lib/opm/backup | wc -l; ls -1 /var/lib/opm/backup; echo "== END STATE-DOWN"',
      'echo "== DOCKERLOG"; sort -u /tmp/docker.log; echo "== END DOCKERLOG"',
      'echo "== SYSTEMCTL"; cat /tmp/systemctl.log; echo "== END SYSTEMCTL"',
    ].join('\n');
    const r = await new Promise<{ status: number | null; out: string }>((done) => {
      // the Docker step of the first runs needs "no docker": the shim is only installed by the bundle (D-YES-DRY)
      const p = spawn('docker', ['run', '--rm', '--add-host=host.docker.internal:host-gateway', 'ubuntu:24.04', 'sh', '-c', inner]);
      let out = '';
      p.stdout.on('data', (d) => (out += d));
      p.stderr.on('data', (d) => (out += d));
      const timer = setTimeout(() => p.kill('SIGKILL'), 1_100_000);
      p.on('close', (status) => {
        clearTimeout(timer);
        done({ status, out });
      });
    });
    const out = r.out;
    writeFileSync(join(tmpdir(), 'opm-install-fallback-e2e.log'), out);
    assert.equal(r.status, 0, out.slice(-6000));
    const sec = (n: string) => {
      const i = out.indexOf(`== ${n}\n`);
      const j = out.indexOf(`== END ${n}\n`);
      assert.ok(i >= 0 && j > i, `section ${n} missing\n${out.slice(-3000)}`);
      return out.slice(i, j);
    };
    const rc = (s: string) => Number(/rc=(\d+)/.exec(s)![1]);
    const binOf = (s: string) => /bin=([0-9a-f]{64})/.exec(s)![1];
    const shaPin = sh256(bins[PIN]!.bin);
    const shaDown = sh256(bins[DOWN]!.bin);

    let s = sec('D-NOTTY');
    assert.equal(rc(s), 1, s);
    assert.match(s, /Docker Engine is not installed\. Install the pinned Docker 28\.5\.2 now\? \[y\/N\] no \(no terminal to ask\)/);
    assert.ok(s.includes(`curl -fsSL -H "Authorization: Bearer ${token}" "http://host.docker.internal:${port}/v1/install/script" | sudo OPM_INSTALL_DOCKER=1 sh`), s);
    assert.match(s, /MANUAL: install Docker Engine 20\.10 or newer yourself/);
    s = sec('D-TTY-NO');
    assert.equal(rc(s), 1, s);
    assert.match(s, /Install the pinned Docker 28\.5\.2 now\? \[y\/N\]/);
    assert.match(s, /Docker Engine is required; nothing was changed/);
    assert.ok(!s.includes('no terminal to ask'), 'the fake TTY was used');
    s = sec('D-YES-DRY');
    assert.match(s, /\[y\/N\] yes \(OPM_INSTALL_DOCKER\)/);
    assert.match(s, /downloaded docker-28\.5\.2\.tgz/);
    assert.match(s, new RegExp(`Installed ${OLD} → available ${PIN}: UPGRADE`));
    assert.match(s, /Nothing changed \(opm-agent 0\.7\.2-10080232 keeps running\)/, 'OPM_INSTALL_DOCKER answers only the Docker question');
    assert.equal(rc(s), 0);
    assert.match(out, /dockerd-installed/);

    s = sec('U-TTY-NO');
    assert.equal(rc(s), 0, s);
    assert.match(s, new RegExp(`Installed ${OLD} → available ${PIN}: UPGRADE`));
    assert.match(s, /Nothing changed/);
    assert.notEqual(binOf(s), shaPin);
    s = sec('U-NOTTY');
    assert.equal(rc(s), 0);
    assert.ok(s.includes('| sudo OPM_YES=1 sh'), s);
    s = sec('U-TTY-YES');
    assert.equal(rc(s), 0, s);
    assert.equal(binOf(s), shaPin);
    for (const re of [
      /release signature verified by openssl/,
      /agent\.json: \+ "releasePublicKey"/,
      /already enrolled .*the install token is NOT used/,
      /opm-agent stopped for the swap \(app containers keep running\)/,
      /database backup: \/var\/lib\/opm\/backup\/0\.7\.2-10080232-\d{8}T\d{6}Z \(2 file\(s\), agent 0\.7\.2-10080232, schema 1\)/,
      /upgraded opm-agent 0\.7\.2-10080232 -> 0\.10\.0-10090000 \(previous binary kept as \/usr\/local\/bin\/opm-agent\.prev\)/,
      /systemd units written by opm-agent systemd-units --install --socket/,
      /opm-agent 0\.10\.0-10090000 is healthy/,
      /Upgrades from now on: cloud console Setup → Upgrade agent/,
    ])
      assert.match(s, re);
    assert.ok(!/FAILED/.test(s), s);
    let st = sec('STATE-U');
    assert.match(st, /"releasePublicKey": "/);
    assert.match(st, /"dataDir": "\/var\/lib\/opm"/, 'existing keys kept');
    assert.match(st, /agent\.json\.bak/);
    assert.ok(!/install-token/.test(st), 'the token is never written for an enrolled site');
    assert.match(st, /systemd-units --install --socket/);
    assert.match(st, /version=0\.7\.2-10080232\nschema=1\n/);
    assert.match(st, /DB-v1/);

    s = sec('SAME');
    assert.equal(rc(s), 0, s);
    assert.match(s, /available 0\.10\.0-10090000: SAME \(reinstall\)/);
    assert.match(s, /reinstalled opm-agent 0\.10\.0-10090000 -> 0\.10\.0-10090000/);
    assert.match(s, /opm-agent 0\.10\.0-10090000 is healthy and connected to the cloud/);

    s = sec('BAD');
    assert.equal(rc(s), 1, s);
    assert.match(s, /target agent 0\.10\.1-10090100 \(OPM_AGENT_VERSION/);
    assert.match(s, /is not healthy \(no healthy answer on 127\.0\.0\.1:7080 with version 0\.10\.1-10090100\) — rolling back to 0\.10\.0-10090000/);
    assert.match(s, /database restored from \/var\/lib\/opm\/backup\/0\.10\.0-10090000-/);
    assert.match(s, /opm-agent 0\.10\.0-10090000 runs again \(rolled back\)/);
    assert.match(s, /FAILED at step 6\/7 \(Verifying\): opm-agent 0\.10\.1-10090100 was not healthy within 6 s/);
    assert.equal(binOf(s), shaPin, 'the previous binary is back');
    assert.match(sec('STATE-BAD'), /DB-v2/);

    s = sec('DOWN-REFUSED');
    assert.equal(rc(s), 1, s);
    assert.match(s, /available 0\.9\.5-10080500: DOWNGRADE/);
    assert.match(s, /the database \(schema 2, written by 0\.10\.0-10090000\) is too new for 0\.9\.5-10080500 \(schema 1, min 0\)/);
    assert.match(s, /downgrade refused: no database backup/);
    assert.equal(binOf(s), shaPin);
    s = sec('DOWN-NOTTY');
    assert.equal(rc(s), 0, s);
    assert.match(s, /Really downgrade 0\.10\.0-10090000 → 0\.9\.5-10080500\? \[y\/N\] no \(no terminal to ask\)/);
    assert.ok(s.includes('| sudo OPM_ALLOW_DOWNGRADE=1 OPM_YES=1 sh'), s);
    s = sec('DOWN-TTY-NO-RESTORE');
    assert.equal(rc(s), 0, s);
    assert.match(s, /backup \/var\/lib\/opm\/backup\/0\.7\.2-10080232-[^:]+: agent 0\.7\.2-10080232, schema 1/);
    assert.match(s, /Restore this database backup for 0\.9\.5-10080500\?/);
    assert.match(s, /Nothing changed/);
    assert.equal(binOf(s), shaPin);
    s = sec('DOWN-YES');
    assert.equal(rc(s), 0, s);
    assert.equal(binOf(s), shaDown);
    assert.match(s, /restored the database backup \/var\/lib\/opm\/backup\/0\.7\.2-10080232-/);
    assert.match(s, /downgraded opm-agent 0\.10\.0-10090000 -> 0\.9\.5-10080500/);
    assert.match(s, /removed opm-agent\.socket/);
    assert.match(s, /wrote \/etc\/systemd\/system\/opm-agent\.service/);
    assert.match(s, /boot guard \(rollback of a failed self-upgrade\): not available/);
    assert.match(s, /opm-agent 0\.9\.5-10080500 is healthy/);
    st = sec('STATE-DOWN');
    assert.match(st, /DB-v1/, 'the compatible DB backup was restored');
    assert.ok(!/opm-agent\.socket/.test(st));
    assert.match(st, /ExecStart=\/usr\/local\/bin\/opm-agent run --config \/etc\/opm\/agent\.json/);
    assert.match(st, /\n\s*5\n/, `5 newest backups kept\n${st}`);
    const dl = sec('DOCKERLOG').split('\n').filter((l) => l.startsWith('docker '));
    assert.ok(dl.length > 0);
    for (const l of dl) assert.match(l, /^docker (version|info|--version)\b/, `the installer must never touch containers: ${l}`);
    const sys = sec('SYSTEMCTL');
    assert.ok(!/systemctl (stop|restart|disable)[^\n]*docker/.test(sys), 'docker is never stopped / restarted');
    assert.match(sys, /systemctl restart opm-agent/);
    assert.match(sys, /systemctl start opm-agent\.socket/);
  } finally {
    server.close();
  }
});
