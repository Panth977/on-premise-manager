/**
 * Installer end-to-end (round G): the generated script in a plain ubuntu:24.04 container (Docker on the dev machine, e.g.
 * Colima) with DRY_RUN=1 against a stub install server: preflight, the pinned Docker static bundle (real
 * download.docker.com bundle, sha256-verified), the pinned agent (agent/dist, release signature) → files in
 * /usr/local/bin, /etc/opm/agent.json + install-token, the log. Skipped when no Docker / no linux agent build is available
 * (OPM_E2E_INSTALL=0 skips it explicitly).
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { spawn, spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { createServer, type Server } from 'node:http';
import { homedir } from 'node:os';
import { join, resolve } from 'node:path';
import { here } from './helpers.ts';
import { installScript } from '../functions/src/routes/install.ts';
import { signHex } from '../scripts/upload-agent.ts';
import { DOCKER_VERSION, MIN_DOCKER_API } from '../scripts/upload-deps.ts';

const sh256 = (b: Buffer) => createHash('sha256').update(b).digest('hex');
function dockerArch(): 'amd64' | 'arm64' | null {
  if (process.env.OPM_E2E_INSTALL === '0') return null;
  const r = spawnSync('docker', ['info', '--format', '{{.Architecture}}'], { encoding: 'utf8', timeout: 20_000 });
  if (r.status !== 0) return null;
  const a = r.stdout.trim();
  return a === 'aarch64' || a === 'arm64' ? 'arm64' : a === 'x86_64' || a === 'amd64' ? 'amd64' : null;
}
const arch = dockerArch();
const agentBin = arch ? resolve(here, `../../agent/dist/opm-agent-linux-${arch}`) : '';
const keyFile = resolve(here, '../../.secrets/release-key.json');
const skip = !arch ? 'no Docker available' : !existsSync(agentBin) ? `no ${agentBin}` : !existsSync(keyFile) ? 'no release key' : false;

async function dockerBundle(a: 'amd64' | 'arm64'): Promise<Buffer> {
  const dir = join(homedir(), '.cache', 'opm-install-e2e');
  mkdirSync(dir, { recursive: true });
  const f = join(dir, `docker-${DOCKER_VERSION}-${a}.tgz`);
  if (!existsSync(f)) {
    const r = await fetch(`https://download.docker.com/linux/static/stable/${a === 'arm64' ? 'aarch64' : 'x86_64'}/docker-${DOCKER_VERSION}.tgz`);
    assert.ok(r.ok, `download docker bundle: ${r.status}`);
    writeFileSync(f, Buffer.from(await r.arrayBuffer()));
  }
  return readFileSync(f);
}

test('installer e2e: ubuntu:24.04 container, DRY_RUN=1 → pinned Docker bundle + agent land in /usr/local/bin', { skip, timeout: 900_000 }, async () => {
  const a = arch!;
  const key = JSON.parse(readFileSync(keyFile, 'utf8')) as { privateKeySeedB64: string; publicKeyB64: string };
  const docker = await dockerBundle(a);
  const agent = readFileSync(agentBin);
  const agentSha = sh256(agent);
  const token = 'opi_' + 'E'.repeat(43);
  let port = 0;
  const hits: string[] = [];
  const server: Server = createServer((req, res) => {
    hits.push(req.url ?? '');
    if (req.headers.authorization !== `Bearer ${token}`) return void res.writeHead(401).end();
    const u = new URL(req.url ?? '/', 'http://x');
    if (u.pathname === '/v1/install/script') {
      const body = installScript({
        apiBase: `http://host.docker.internal:${port}`,
        deploymentId: 'DepE2E00000000000001',
        siteSlug: 'app',
        token,
        releasePublicKey: key.publicKeyB64,
        agent: { version: '0.0.0-e2e', files: { [a]: { sha256: agentSha, sig: signHex(agentSha, key.privateKeySeedB64) } } },
        docker: { version: DOCKER_VERSION, minApi: MIN_DOCKER_API, sha256: { [a]: sh256(docker) } },
      });
      return void res.writeHead(200, { 'Content-Type': 'text/x-shellscript' }).end(body);
    }
    if (u.pathname === '/v1/install/deps/docker' && u.searchParams.get('arch') === a) return void res.writeHead(200, { 'X-OPM-Sha256': sh256(docker), 'X-OPM-Version': DOCKER_VERSION }).end(docker);
    if (u.pathname === '/v1/install/binary' && u.searchParams.get('arch') === a && u.searchParams.get('version') === '0.0.0-e2e') return void res.writeHead(200, { 'X-OPM-Sha256': agentSha }).end(agent);
    res.writeHead(404).end();
  });
  await new Promise<void>((r) => server.listen(0, '0.0.0.0', () => r()));
  port = (server.address() as { port: number }).port;
  try {
    const inner = [
      'set -e',
      'apt-get update -qq >/dev/null && apt-get install -y -qq curl ca-certificates >/dev/null',
      // an existing Docker is judged by its API range: fake docker CLIs reporting API 1.40 (refused) and 1.41 (accepted)
      'mk() { printf \'#!/bin/sh\\ncase "$1" in version) echo "%s 1.12 %s";; info) exit 0;; --version) echo "Docker version %s, build fake";; esac\\n\' "$1" "$2" "$2" > /usr/local/bin/docker; chmod +x /usr/local/bin/docker; }',
      'mk 1.40 19.03.15',
      `echo "== FAKE140"; curl -fsSL -H "Authorization: Bearer ${token}" http://host.docker.internal:${port}/v1/install/script | DRY_RUN=1 sh || echo "exit=$?"`,
      'mk 1.41 20.10.24',
      `echo "== FAKE141"; curl -fsSL -H "Authorization: Bearer ${token}" http://host.docker.internal:${port}/v1/install/script | DRY_RUN=1 sh; echo "== END141"`,
      'rm -rf /usr/local/bin/docker /usr/local/bin/opm-agent /etc/opm /var/log/opm-install.log',
      'echo "== REAL"',
      // §21 #91: Docker is installed only when the operator agrees (no terminal here → the flag)
      `curl -fsSL -H "Authorization: Bearer ${token}" http://host.docker.internal:${port}/v1/install/script | OPM_INSTALL_DOCKER=1 DRY_RUN=1 sh`,
      'echo "== BIN"; ls /usr/local/bin',
      'echo "== CFG"; cat /etc/opm/agent.json; test -s /etc/opm/install-token && echo token-ok',
      'echo "== VER"; /usr/local/bin/dockerd --version; /usr/local/bin/opm-agent version',
      'echo "== LOG"; grep -c . /var/log/opm-install.log',
      // idempotent rerun: everything detected, nothing downloaded again for the agent (§21 #91: the rerun asks first; the
      // real agent/dist binary reports its own version, not the 0.0.0-e2e label → a "downgrade" answered by the flags)
      `curl -fsSL -H "Authorization: Bearer ${token}" http://host.docker.internal:${port}/v1/install/script | OPM_YES=1 OPM_ALLOW_DOWNGRADE=1 DRY_RUN=1 sh | grep -E "already installed|OK|Docker found|Installed" | head -50`,
    ].join('\n');
    // async (the stub server lives in this process: spawnSync would block it)
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
    let out = r.out;
    assert.equal(r.status, 0, out.slice(-4000));
    const fake140 = out.slice(out.indexOf('== FAKE140'), out.indexOf('== FAKE141'));
    assert.match(fake140, /FAILED at step 2\/7 \(Docker Engine\): Docker Engine 19\.03\.15 \(API 1\.40\) is too old/, fake140);
    assert.match(fake140, /exit=1/);
    const fake141 = out.slice(out.indexOf('== FAKE141'), out.indexOf('== END141'));
    assert.match(fake141, /OK +Docker Engine 20\.10\.24 \(API 1\.41, min 1\.12; agent speaks 1\.41–1\.47\)/, fake141);
    assert.match(fake141, /\[7\/7\] GUI addresses and firewall/);
    assert.match(fake141, /DRY_RUN: the agent was not started, no GUI port listens yet/);
    assert.match(fake141, /Upgrade later: rerun this same command/);
    assert.ok(!/FAILED/.test(fake141), fake141);
    out = out.slice(out.indexOf('== REAL'));
    for (const f of ['containerd', 'containerd-shim-runc-v2', 'ctr', 'docker', 'docker-init', 'docker-proxy', 'dockerd', 'runc', 'opm-agent']) assert.match(out, new RegExp(`^${f}$`, 'm'), `missing /usr/local/bin/${f}\n${out.slice(-3000)}`);
    assert.match(out, /\[1\/7\] Checking this server/);
    assert.match(out, /OK +running as root/);
    assert.match(out, new RegExp(`downloaded docker-${DOCKER_VERSION.replace(/\./g, '\\.')}\\.tgz \\(sha256 ${sh256(docker)}\\)`));
    assert.match(out, /release signature verified/);
    assert.match(out, /"deploymentId":"DepE2E00000000000001"/);
    assert.match(out, /token-ok/);
    assert.match(out, new RegExp(`Docker version ${DOCKER_VERSION.replace(/\./g, '\\.')}`));
    assert.match(out, /opm-agent 0\.0\.0-e2e already installed \(sha256 matches\)/);
    assert.match(out, /Docker found: Docker version/);
    // fake-1.41 run + the real run download the agent once each; the rerun does not
    assert.equal(hits.filter((h) => h.startsWith('/v1/install/binary')).length, 2, 'the rerun must not download the agent again');
  } finally {
    server.close();
  }
});
