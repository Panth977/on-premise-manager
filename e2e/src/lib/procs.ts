// Agent processes + docker helpers.
import { spawn, execFileSync, type ChildProcess } from 'node:child_process';
import { mkdirSync, openSync, writeFileSync, existsSync, readFileSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { createConnection } from 'node:net';
import { CFG, type SiteName } from './env.js';
import { log } from './report.js';

const children = new Map<SiteName, ChildProcess>();

export function siteDir(site: SiteName): string {
  return join(CFG.runDir, site);
}

export function docker(...args: string[]): string {
  return execFileSync('docker', args, { encoding: 'utf8', env: { ...process.env, DOCKER_HOST: CFG.dockerHost } }).trim();
}

export interface Ctr { id: string; name: string; container: string; service: string; slug: string; site: string; rev: string; started: string; state: string }
export function opmContainers(deploymentId?: string): Ctr[] {
  const filter = deploymentId ? `label=opm.deployment=${deploymentId}` : 'label=opm.deployment';
  const ids = docker('ps', '-aq', '--filter', filter).split('\n').filter(Boolean);
  if (!ids.length) return [];
  const out = JSON.parse(docker('inspect', ...ids)) as { Id: string; Name: string; Config: { Labels: Record<string, string> }; State: { StartedAt: string; Status: string } }[];
  return out.map((c) => ({
    id: c.Id,
    name: c.Name.replace(/^\//, ''),
    container: c.Config.Labels['opm.container'] ?? '', // round C: containerSlug (the identity on a site)
    service: c.Config.Labels['opm.service'] ?? '',
    slug: c.Config.Labels['opm.slug'] ?? '',
    site: c.Config.Labels['opm.site'] ?? '',
    rev: c.Config.Labels['opm.rev'] ?? '',
    started: c.State.StartedAt,
    state: c.State.Status,
  }));
}

export function portFree(port: number): Promise<boolean> {
  return new Promise((res) => {
    const s = createConnection({ host: '127.0.0.1', port });
    s.once('connect', () => { s.destroy(); res(false); });
    s.once('error', () => res(true));
  });
}

export function writeAgentConfig(site: SiteName, deploymentId: string): string {
  const dir = siteDir(site);
  mkdirSync(join(dir, 'data'), { recursive: true });
  const p = CFG.sites[site];
  const cfg = {
    apiBase: CFG.apiBase,
    deploymentId,
    siteSlug: site,
    dataDir: join(dir, 'data'),
    agentPort: p.agentPort,
    guiAliasPort: p.guiAliasPort,
    advertiseHost: '127.0.0.1',
    dockerHost: CFG.dockerHost,
    installTokenFile: join(dir, 'etc/install-token'),
    containerNames: 'prefixed', // several sites share one Docker host (SPEC §21 #88)
  };
  writeFileSync(join(dir, 'agent.json'), JSON.stringify(cfg, null, 2) + '\n');
  return join(dir, 'agent.json');
}

export function startAgent(site: SiteName, bin: string, extraEnv: Record<string, string> = {}): ChildProcess {
  const dir = siteDir(site);
  const out = openSync(join(dir, 'agent.log'), 'a');
  const env = { ...process.env, OPM_APPROVE_WAIT_SEC: String(CFG.approveWaitSec), DOCKER_HOST: CFG.dockerHost, OPM_LOG_LEVEL: 'debug', ...extraEnv };
  const child = spawn(bin, ['run', '--config', join(dir, 'agent.json')], { env, stdio: ['ignore', out, out], detached: true });
  writeFileSync(join(dir, 'agent.pid'), String(child.pid));
  children.set(site, child);
  child.on('exit', (code, sig) => log(`agent ${site} exited code=${code} signal=${sig}`));
  log(`started agent ${site} pid ${child.pid} (${bin})`);
  return child;
}

export function agentAlive(site: SiteName): boolean {
  const c = children.get(site);
  return !!c && c.exitCode === null && c.signalCode === null;
}

/** kill agents of a previous run (pid files) */
export function killPrevious(): void {
  for (const site of ['app', 'db'] as SiteName[]) {
    const pf = join(siteDir(site), 'agent.pid');
    if (!existsSync(pf)) continue;
    const pid = Number(readFileSync(pf, 'utf8'));
    try {
      process.kill(pid, 'SIGTERM');
      log(`killed previous agent ${site} pid ${pid}`);
    } catch {
      /* gone */
    }
    rmSync(pf, { force: true });
  }
}

export function stopAgents(): void {
  for (const [site, c] of children) {
    if (c.exitCode === null) {
      c.kill('SIGTERM');
      log(`stopped agent ${site}`);
    }
  }
}

export function tailLog(site: SiteName, n = 40): string {
  try {
    const t = readFileSync(join(siteDir(site), 'agent.log'), 'utf8').split('\n');
    return t.slice(-n).join('\n');
  } catch {
    return '';
  }
}
