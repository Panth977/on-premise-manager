/**
 * Mock of the operator toolbox (round T): every TOOLS_API endpoint + the log EventSource + the terminal WebSocket.
 * mock/index.ts routes every /tools/ path here. Knobs (localStorage):
 *   opm.mock.noHostShell=1     agent.json tools.hostShell=false
 *   opm.mock.noExec=1          agent.json tools.containerExec=false
 */
import type { ErrorCode } from '@opm/shared/types';
import type {
  AccessLogLine,
  AgentLogLine,
  ContainerInspectResponse,
  ContainerSample,
  DockerEvent,
  DockerImageRow,
  DockerImagesResponse,
  HostSample,
  LogStreamLine,
  MetricAlert,
  MetricsSeriesResponse,
  MetricsSummary,
  NetcheckRequest,
  NetcheckResult,
  TerminalSession,
  ToolsContainer,
  ToolsInfo,
  ToolsVolume,
  TranscriptMeta,
  VolumeEntry,
} from '@opm/shared/agent-tools';
import { setStreamImpl } from '$lib/tools/api';
import { DENO_LINES, ESC_LOST_LINES } from './data'; // §21 #93

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type AnySession = any;
interface Ctx {
  /** §21 #96: an update holds the site lock (advisory since §21 #110: volume writes go ahead) */
  updateRunning?: () => boolean;
  requireSession: () => AnySession;
  requireActor: (h: Headers, s: AnySession) => string;
  fail: (code: ErrorCode, message: string, details?: Record<string, unknown>) => never;
  json: (status: number, body: unknown, extra?: Record<string, string>) => Response;
}

const SITES = ['app', 'db', 'northwind'];
const HOME: Record<string, string> = { app: '/var/lib/opm', db: '/opt/opm-db', northwind: '/srv/opm' };
const knob = (k: string) => (typeof localStorage !== 'undefined' ? localStorage.getItem(k) : null);
const now = () => Date.now();
const iso = (ms: number) => new Date(ms).toISOString();
const MiB = 1024 * 1024;
const GiB = 1024 * MiB;

/** deterministic noise */
function wave(seed: number, i: number, base: number, amp: number) {
  const v = base + amp * (Math.sin(i / 7 + seed) * 0.6 + Math.sin(i / 2.3 + seed * 3) * 0.25 + Math.sin(i * 1.7 + seed * 11) * 0.15);
  return Math.max(0, Math.round(v * 10) / 10);
}

// ───────────────────────────────────────────── containers

interface MC {
  ref: string;
  serviceSlug: string;
  image: string;
  cpu: number;
  memMiB: number;
  limitMiB: number | null;
  restarts: number;
  oom?: boolean;
  failedCopy?: boolean;
  state?: ToolsContainer['state'];
  health?: ToolsContainer['health'];
  ports: ToolsContainer['ports'];
  expected?: boolean;
}
const CONTAINERS: Record<string, MC[]> = {
  app: [
    { ref: 'hello-web', serviceSlug: 'hello-web', image: 'hello-web:v1', cpu: 2.4, memMiB: 38, limitMiB: 256, restarts: 0, health: 'healthy', ports: [{ containerPort: 80, hostPort: 32768, protocol: 'tcp' }] },
    { ref: 'hello-api', serviceSlug: 'hello-api', image: 'hello-api:v1', cpu: 91.5, memMiB: 412, limitMiB: 512, restarts: 3, health: 'healthy', ports: [{ containerPort: 3000, hostPort: 32769, protocol: 'tcp' }] },
    { ref: 'hello-api-2', serviceSlug: 'hello-api', image: 'hello-api:v1', cpu: 0, memMiB: 0, limitMiB: 512, restarts: 0, state: 'exited', health: 'none', ports: [{ containerPort: 3000, hostPort: 9081, protocol: 'tcp' }] },
    { ref: 'hello-api-failed-1759880000', serviceSlug: 'hello-api', image: 'hello-api:v2', cpu: 0, memMiB: 0, limitMiB: 512, restarts: 5, oom: true, failedCopy: true, state: 'exited', health: 'unhealthy', ports: [], expected: false },
  ],
  db: [{ ref: 'redis', serviceSlug: 'redis', image: 'redis:v1', cpu: 4.1, memMiB: 96, limitMiB: null, restarts: 0, health: 'healthy', ports: [{ containerPort: 6379, hostPort: 32770, protocol: 'tcp' }] }],
  northwind: [
    { ref: 'northwind-backend-admin', serviceSlug: 'northwind-backend', image: 'northwind-backend:v1', cpu: 12.2, memMiB: 640, limitMiB: 1024, restarts: 1, health: 'healthy', ports: [{ containerPort: 3000, hostPort: 32771, protocol: 'tcp' }] },
    { ref: 'northwind-backend-terminal', serviceSlug: 'northwind-backend', image: 'northwind-backend:v1', cpu: 6.8, memMiB: 380, limitMiB: 1024, restarts: 0, health: 'healthy', ports: [{ containerPort: 3000, hostPort: 32772, protocol: 'tcp' }] },
    { ref: 'node-red', serviceSlug: 'node-red', image: 'node-red:v3', cpu: 1.2, memMiB: 150, limitMiB: null, restarts: 0, health: 'none', ports: [{ containerPort: 1880, hostPort: 32773, protocol: 'tcp' }] },
  ],
};
/** site → ref → manual stop */
const manual: Record<string, Record<string, { at: string; actor: string | null }>> = { app: { 'hello-api-2': { at: iso(now() - 42 * 60_000), actor: 'ravi@acme.example' } } };
const stateOver: Record<string, Record<string, ToolsContainer['state']>> = {};

function stateOf(site: string, c: MC): ToolsContainer['state'] {
  return stateOver[site]?.[c.ref] ?? c.state ?? 'running';
}
function container(site: string, c: MC): ToolsContainer {
  const st = stateOf(site, c);
  const running = st === 'running';
  const started = now() - (c.ref.length * 3_600_000) / 3;
  const m = manual[site]?.[c.ref] ?? null;
  return {
    siteSlug: site,
    ref: c.ref,
    containerSlug: c.failedCopy ? c.serviceSlug : c.ref,
    name: c.failedCopy ? c.ref : `opm-${site}-${c.ref}`,
    id: `${c.ref.length.toString(16)}a91c0d3f${site.length}e2`.slice(0, 12),
    serviceSlug: c.serviceSlug,
    image: c.image,
    failedCopy: !!c.failedCopy,
    state: st,
    status: running ? `Up ${Math.round((now() - started) / 3_600_000)} hours${c.health && c.health !== 'none' ? ` (${c.health})` : ''}` : `Exited (${c.oom ? 137 : 0}) ${m ? '42 minutes' : '3 hours'} ago`,
    health: running ? (c.health ?? 'none') : c.failedCopy ? 'unhealthy' : 'none',
    startedAt: iso(started),
    finishedAt: running ? null : iso(now() - 42 * 60_000),
    exitCode: running ? null : c.oom ? 137 : 0,
    restartCount: c.restarts,
    oomKilled: !!c.oom,
    manuallyStopped: running ? null : m,
    expectedRunning: c.expected ?? !c.failedCopy,
    ports: c.ports,
    cpuPct: running ? c.cpu : null,
    memBytes: running ? c.memMiB * MiB : null,
    memLimitBytes: c.limitMiB ? c.limitMiB * MiB : running ? 7.6 * GiB : null,
  };
}
function findC(site: string, ref: string, ctx: Ctx): MC {
  const c = (CONTAINERS[site] ?? []).find((x) => x.ref === ref);
  if (!c) ctx.fail('not-found', `no container ${ref} on site ${site}`);
  return c!;
}

// ───────────────────────────────────────────── monitoring

function hostSample(site: string, i = 0): HostSample {
  const seed = site.length;
  return {
    t: iso(now() - i * 15_000),
    cpuPct: site === 'app' ? wave(seed, i, 58, 18) : wave(seed, i, 21, 8),
    cpuCount: site === 'app' ? 4 : 2,
    memUsedBytes: (site === 'app' ? 5.1 : 1.4) * GiB,
    memTotalBytes: (site === 'app' ? 7.6 : 3.8) * GiB,
    swapUsedBytes: (site === 'app' ? 0.3 : 0) * GiB,
    swapTotalBytes: 2 * GiB,
    load1: site === 'app' ? 2.31 : 0.42,
    load5: site === 'app' ? 1.98 : 0.38,
    load15: site === 'app' ? 1.54 : 0.31,
    uptimeSec: 86400 * 23 + 3600 * 5,
    disks: [
      { mount: '/', label: 'root', usedBytes: (site === 'app' ? 47.1 : 18) * GiB, totalBytes: 50 * GiB },
      { mount: HOME[site], label: 'agentHome', usedBytes: (site === 'app' ? 12.4 : 3.2) * GiB, totalBytes: 100 * GiB },
    ],
  };
}
function cSample(site: string, c: MC, i = 0): ContainerSample {
  const running = stateOf(site, c) === 'running';
  const seed = c.ref.length;
  return {
    ref: c.ref,
    t: iso(now() - i * 15_000),
    cpuPct: running ? wave(seed, i, c.cpu, Math.max(1, c.cpu * 0.12)) : null,
    memBytes: running ? Math.round(wave(seed, i, c.memMiB, c.memMiB * 0.04) * MiB) : null,
    memLimitBytes: c.limitMiB ? c.limitMiB * MiB : null,
    netRxBps: running ? wave(seed, i, 42_000, 30_000) : null,
    netTxBps: running ? wave(seed + 1, i, 18_000, 12_000) : null,
    blkReadBps: running ? wave(seed + 2, i, 2_000, 1_800) : null,
    blkWriteBps: running ? wave(seed + 3, i, 9_000, 8_000) : null,
    restartCount: c.restarts,
    oomKilled: !!c.oom,
  };
}
function summary(site: string): MetricsSummary {
  const host = hostSample(site);
  const alerts: MetricAlert[] = [];
  const cs = (CONTAINERS[site] ?? []).filter((c) => !c.failedCopy);
  for (const c of cs) {
    if (stateOf(site, c) === 'running' && c.cpu > 85) alerts.push({ subject: `container:${c.ref}`, metric: 'cpu', level: 'amber', value: c.cpu, since: iso(now() - 3 * 60_000), message: `CPU ${c.cpu.toFixed(0)} % (above 85 % for 3 min)` });
  }
  for (const d of host.disks) {
    const p = (100 * d.usedBytes) / d.totalBytes;
    if (p > 90) alerts.push({ subject: `disk:${d.mount}`, metric: 'disk', level: 'red', value: Math.round(p * 10) / 10, since: iso(now() - 3 * 3_600_000), message: `Disk ${d.mount} ${p.toFixed(0)} % full` });
  }
  return {
    siteSlug: site,
    intervalSec: 15,
    sampledAt: host.t,
    host,
    containers: cs.map((c) => {
      const run = stateOf(site, c) === 'running';
      return { ...cSample(site, c), spark: { cpu: Array.from({ length: 60 }, (_, i) => (run ? wave(c.ref.length, 60 - i, c.cpu, Math.max(1, c.cpu * 0.15)) : null)), mem: Array.from({ length: 60 }, (_, i) => (run ? Math.round(wave(c.ref.length, 60 - i, c.memMiB, c.memMiB * 0.05) * MiB) : null)) } };
    }),
    alerts,
    thresholds: { cpuPct: 85, memPct: 90, diskPct: 90, sustainSec: 300 },
    dbBytes: 3.4 * MiB,
  };
}
function series(site: string, subject: string, range: string): MetricsSeriesResponse {
  const step = range === '24h' ? 60 : range === '7d' ? 900 : 15;
  const n = range === '24h' ? 1440 : range === '7d' ? 672 : 240;
  const end = Math.floor(now() / 1000 / step) * step;
  const t = Array.from({ length: n }, (_, i) => end - (n - 1 - i) * step);
  const out: MetricsSeriesResponse = { siteSlug: site, subject, range: range as MetricsSeriesResponse['range'], stepSec: step, t, series: {} };
  if (subject === 'host') {
    const seed = site.length;
    out.series.cpuPct = t.map((_, i) => wave(seed, i, site === 'app' ? 58 : 21, site === 'app' ? 18 : 8));
    out.series.memUsedBytes = t.map((_, i) => Math.round(wave(seed, i, site === 'app' ? 5200 : 1400, 300) * MiB));
    out.series.memTotalBytes = t.map(() => (site === 'app' ? 7.6 : 3.8) * GiB);
    out.series.swapTotalBytes = t.map(() => 2 * GiB);
    out.series.load5 = t.map((_, i) => wave(seed + 1, i, site === 'app' ? 1.8 : 0.35, 0.5));
    out.series.load15 = t.map((_, i) => wave(seed + 2, i, site === 'app' ? 1.5 : 0.3, 0.3));
    out.series.load1 = t.map((_, i) => wave(seed, i, site === 'app' ? 2 : 0.4, 0.8));
    out.series.swapUsedBytes = t.map(() => 0.3 * GiB);
    return out;
  }
  const ref = subject.replace(/^container:/, '');
  const c = (CONTAINERS[site] ?? []).find((x) => x.ref === ref);
  if (!c) return out;
  const seed = ref.length;
  const off = (i: number) => stateOf(site, c) !== 'running' && i > n - 10;
  out.series.cpuPct = t.map((_, i) => (off(i) ? null : wave(seed, i, Math.max(c.cpu, 3), Math.max(1, c.cpu * 0.2))));
  out.series.cpuPeak = t.map((_, i) => (off(i) ? null : wave(seed, i, Math.max(c.cpu, 3) * 1.25, Math.max(1, c.cpu * 0.25))));
  out.series.memBytes = t.map((_, i) => (off(i) ? null : Math.round(wave(seed, i, Math.max(c.memMiB, 40), Math.max(c.memMiB, 40) * 0.08) * MiB)));
  out.series.memLimitBytes = t.map(() => (c.limitMiB ? c.limitMiB * MiB : null));
  out.series.netRxBps = t.map((_, i) => (off(i) ? null : wave(seed, i, 42_000, 30_000)));
  out.series.netTxBps = t.map((_, i) => (off(i) ? null : wave(seed + 1, i, 18_000, 12_000)));
  out.series.blkReadBps = t.map((_, i) => (off(i) ? null : wave(seed + 2, i, 2_000, 1_800)));
  out.series.blkWriteBps = t.map((_, i) => (off(i) ? null : wave(seed + 3, i, 9_000, 8_000)));
  return out;
}

// ───────────────────────────────────────────── volumes

type Node = { type: 'dir'; children: Record<string, Node> } | { type: 'file'; size: number; text?: string; binary?: boolean } | { type: 'symlink'; target: string };
const dir = (children: Record<string, Node>): Node => ({ type: 'dir', children });
const txt = (text: string): Node => ({ type: 'file', size: new TextEncoder().encode(text).length, text });
const bin = (size: number): Node => ({ type: 'file', size, binary: true });
const VOLS: Record<string, { v: Omit<ToolsVolume, 'siteSlug' | 'sizeBytes' | 'fileCount' | 'exists' | 'sizeTruncated'>; root: Node }[]> = {
  app: [
    {
      v: { volumeSlug: 'api-env', label: 'API env config', kind: 'dir', hostPath: '/var/lib/opm/volumes/api-env', usedBy: ['hello-api', 'hello-api-2'], backup: true },
      root: dir({
        'worker.ts': txt("export default {\n  queue: 'jobs',\n  concurrency: 4,\n  retry: { attempts: 5, backoffMs: 2000 },\n};\n"),
        'production.ts': txt("export default {\n  db: { host: 'redis.db.opm', port: 6379 },\n  logLevel: 'info',\n};\n"),
        archive: dir({ 'old-config.json': txt('{\n  "legacy": true\n}\n'), 'dump.bin': bin(3 * MiB) }),
        current: { type: 'symlink', target: 'production.ts' },
      }),
    },
    {
      v: { volumeSlug: 'hello-api-data', label: 'API data', kind: 'dir', hostPath: '/var/lib/opm/volumes/hello-api-data', usedBy: ['hello-api'], backup: true },
      root: dir({
        'counter.json': txt('{ "count": 1284, "updatedAt": "2026-10-08T03:12:44Z" }\n'),
        logs: dir({ 'app.log': txt(Array.from({ length: 80 }, (_, i) => `2026-10-08T0${i % 10}:1${i % 6}:00Z INFO request ${i} served in ${12 + (i % 17)} ms`).join('\n') + '\n'), 'app.log.1.gz': bin(840_000) }),
        uploads: dir({ 'logo.png': bin(48_213), 'report-q3.pdf': bin(1_250_000) }),
      }),
    },
    { v: { volumeSlug: 'hello-api-2-data', label: 'API 2 scratch data', kind: 'dir', hostPath: '/var/lib/opm/cache/hello-api-2', usedBy: ['hello-api-2'], backup: false }, root: dir({}) },
  ],
  db: [{ v: { volumeSlug: 'redis-data', label: 'Redis data', kind: 'dir', hostPath: '/opt/opm-db/volumes/redis-data', usedBy: ['redis'], backup: true }, root: dir({ 'dump.rdb': bin(24 * MiB), 'appendonly.aof': bin(61 * MiB) }) }],
  northwind: [
    { v: { volumeSlug: 'nodered-data', label: 'Node-RED data', kind: 'dir', hostPath: '/srv/opm/volumes/nodered-data', usedBy: ['node-red'], backup: true }, root: dir({ 'flows.json': txt('[\n  { "id": "f1", "type": "tab", "label": "Device types" }\n]\n'), 'settings.js': txt('module.exports = {\n  uiPort: 1880,\n};\n'), node_modules: dir({ '.package-lock.json': txt('{}\n') }) }) },
    // §21 #101 persist-to-cloud folders (their live content is set by lib/mock/volsync.ts)
    { v: { volumeSlug: 'ez-integrations', label: 'Client integrations', kind: 'dir', hostPath: '/srv/opm/volumes/ez-integrations', usedBy: ['northwind-backend-integration'], backup: true }, root: dir({}) },
    { v: { volumeSlug: 'ez-scripts', label: 'Ops scripts', kind: 'dir', hostPath: '/srv/opm/volumes/ez-scripts', usedBy: ['northwind-backend-integration'], backup: true }, root: dir({}) },
  ],
};
function nodeSize(n: Node): { size: number; files: number } {
  if (n.type === 'file') return { size: n.size, files: 1 };
  if (n.type === 'symlink') return { size: 0, files: 0 };
  let size = 0;
  let files = 0;
  for (const c of Object.values(n.children)) {
    const s = nodeSize(c);
    size += s.size;
    files += s.files;
  }
  return { size, files };
}
function vol(site: string, slug: string, ctx: Ctx) {
  const v = (VOLS[site] ?? []).find((x) => x.v.volumeSlug === slug);
  if (!v) ctx.fail('not-found', `no volume ${slug} on site ${site}`);
  return v!;
}
function walk(root: Node, path: string, ctx: Ctx): Node {
  let n = root;
  for (const seg of path.split('/').filter(Boolean)) {
    if (seg === '..') ctx.fail('invalid', 'path must stay inside the volume');
    if (n.type !== 'dir' || !n.children[seg]) ctx.fail('not-found', `${path} does not exist`);
    n = (n as { children: Record<string, Node> }).children[seg];
  }
  return n;
}
const norm = (p: string | null) => '/' + (p ?? '').split('/').filter(Boolean).join('/');

// §21 #96 volume manager — shared with lib/mock/volops.ts (empty / copy / restore change these trees)
export type MockNode = Node;
/** `${site}/${volumeSlug}` → what holds the volume (a running volume action) */
export const volBusy = new Map<string, string>();
export function mockVolume(site: string, slug: string): { v: Omit<ToolsVolume, 'siteSlug' | 'sizeBytes' | 'fileCount' | 'exists' | 'sizeTruncated'>; root: Node } | null {
  return (VOLS[site] ?? []).find((x) => x.v.volumeSlug === slug) ?? null;
}
export function mockVolumes(site: string) {
  return VOLS[site] ?? [];
}
export const mockNodeSize = (n: Node) => nodeSize(n);
export const cloneNode = (n: Node): Node => JSON.parse(JSON.stringify(n)) as Node;
export const mockHome = (site: string) => HOME[site] ?? '/var/lib/opm';
function parentOf(root: Node, p: string, ctx: Ctx, create: boolean): { dirNode: { children: Record<string, Node> }; name: string } {
  const segs = p.split('/').filter(Boolean);
  if (!segs.length) ctx.fail('invalid', 'not the volume folder itself (use Empty / Restore for the whole volume)');
  let n = root;
  for (const seg of segs.slice(0, -1)) {
    if (n.type !== 'dir') ctx.fail('invalid', `${seg} is not a folder`);
    const d = n as { children: Record<string, Node> };
    if (!d.children[seg]) {
      if (!create) ctx.fail('not-found', `${p} does not exist`);
      d.children[seg] = dir({});
    }
    n = d.children[seg];
    if (n.type === 'symlink') ctx.fail('invalid', `path passes through the symlink /${seg}`);
  }
  if (n.type !== 'dir') ctx.fail('invalid', 'not a folder');
  return { dirNode: n as { children: Record<string, Node> }, name: segs[segs.length - 1] };
}
function entryOf(p: string, n: Node): VolumeEntry {
  return { name: p.split('/').pop() || '/', path: p, type: n.type, sizeBytes: nodeSize(n).size, mtime: iso(now()), mode: n.type === 'dir' ? 'drwxr-xr-x' : '-rw-r--r--', linkTarget: n.type === 'symlink' ? n.target : null, textLike: n.type === 'file' && !n.binary && n.size <= MiB };
}

// ───────────────────────────────────────────── terminal

const sessions: TerminalSession[] = [];
const tickets = new Map<string, { site: string; sessionId: string; target: string }>();
const transcripts: TranscriptMeta[] = [
  { id: 'TS01J9MOCKA', siteSlug: 'app', kind: 'container', target: 'hello-api', command: ['/bin/sh'], actor: 'ravi@acme.example', via: null, startedAt: iso(now() - 26 * 3_600_000), endedAt: iso(now() - 26 * 3_600_000 + 7 * 60_000), durationSec: 420, exitCode: 0, sizeBytes: 18_402, expiresAt: iso(now() - 26 * 3_600_000 + 7 * 86_400_000) },
  { id: 'TS01J9MOCKB', siteSlug: 'app', kind: 'host', target: 'host (root)', command: ['/bin/bash', '-l'], actor: 'meera@acme.example', via: 'db', startedAt: iso(now() - 3 * 86_400_000), endedAt: iso(now() - 3 * 86_400_000 + 31 * 60_000), durationSec: 1860, exitCode: null, sizeBytes: 96_110, expiresAt: iso(now() + 4 * 86_400_000) },
];
const TRANSCRIPT_TEXT = 'Script started on 2026-10-07 23:41:02+00:00 [hello-api, /bin/sh, ravi@acme.example]\n/app # ls\nnode_modules  package.json  dist  config.json\n/app # cat config.json\n{\n  "site": "app",\n  "token": "••••••"\n}\n/app # exit\n\nScript done (exit 0, 7 min).\n';

function info(site: string): ToolsInfo {
  const hs = knob('opm.mock.noHostShell') !== '1';
  const ex = knob('opm.mock.noExec') !== '1';
  return {
    siteSlug: site,
    os: 'linux',
    arch: 'amd64',
    agentUser: 'root',
    dockerVersion: '28.5.2',
    hostShell: { enabled: hs, reason: hs ? null : 'agent.json tools.hostShell is false', user: 'root', shell: '/bin/bash' },
    containerExec: { enabled: ex, reason: ex ? null : 'agent.json tools.containerExec is false' },
    limits: { maxSessions: 4, idleTimeoutSec: 1800, transcriptDays: 7, viewMaxBytes: MiB },
    activeSessions: sessions.filter((s) => s.siteSlug === site).length,
    metrics: { intervalSec: 15, rawSec: 3600, minuteSec: 86400, quarterSec: 604800 },
    configPath: '/etc/opm/agent.json',
  };
}

// ───────────────────────────────────────────── docker

function images(site: string): DockerImagesResponse {
  const cs = (CONTAINERS[site] ?? []).map((c) => container(site, c));
  const rows: DockerImageRow[] = [];
  const byImage = new Map<string, ToolsContainer[]>();
  for (const c of cs) byImage.set(c.image, [...(byImage.get(c.image) ?? []), c]);
  let k = 0;
  for (const [img, list] of byImage) {
    const usedBy = list.filter((c) => !c.failedCopy || true);
    rows.push({ id: `sha256:${(++k * 0x1f3a7b).toString(16).padStart(8, '0')}c4e1d2a9b8f7e6d5c4b3a291`, tags: [img, `opm/cfg:${(k * 977).toString(16)}e3f1a2b4`], sizeBytes: (80 + k * 37) * MiB, created: iso(now() - k * 86_400_000 * 3), containers: usedBy.map((c) => ({ name: c.name, ref: c.ref, state: c.state })), inUse: true, opm: true, protected: !img.endsWith(':v2') });
  }
  if (!dockerPruned[site]) {
    rows.push({ id: 'sha256:9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b', tags: [`${cs[0]?.serviceSlug ?? 'app'}:v0`], sizeBytes: 212 * MiB, created: iso(now() - 30 * 86_400_000), containers: [], inUse: false, opm: true, protected: false });
    rows.push({ id: 'sha256:0f1e2d3c4b5a69788796a5b4c3d2e1f0a9b8c7d6', tags: ['busybox:1.36'], sizeBytes: 4.3 * MiB, created: iso(now() - 60 * 86_400_000), containers: [], inUse: false, opm: false, protected: false });
    rows.push({ id: 'sha256:7766554433221100ffeeddccbbaa998877665544', tags: [], sizeBytes: 145 * MiB, created: iso(now() - 12 * 86_400_000), containers: [], inUse: false, opm: false, protected: false });
  }
  const size = rows.reduce((a, r) => a + r.sizeBytes, 0);
  const recl = rows.filter((r) => !r.inUse).reduce((a, r) => a + r.sizeBytes, 0);
  return {
    siteSlug: site,
    dockerVersion: '28.5.2',
    items: rows,
    df: { images: { count: rows.length, sizeBytes: size, reclaimableBytes: recl }, containers: { count: cs.length, sizeBytes: 18 * MiB, reclaimableBytes: 2 * MiB }, volumes: { count: 2, sizeBytes: 640 * MiB, reclaimableBytes: 0 }, buildCache: { count: 0, sizeBytes: 0, reclaimableBytes: 0 } },
  };
}
const dockerPruned: Record<string, boolean> = {};

// ───────────────────────────────────────────── netcheck

function netcheck(site: string, req: NetcheckRequest, ctx: Ctx): NetcheckResult {
  const from = req.from.kind === 'host' ? 'host' : req.from.containerSlug;
  if (req.from.kind === 'container') findC(site, req.from.containerSlug, ctx);
  const target = String(req.target ?? '').trim();
  if (!target) ctx.fail('invalid', 'target is required', { errors: [{ path: 'target', code: 'V_REQUIRED', message: 'enter a target', severity: 'error' }] });
  const base = { check: req.check, from, target, at: iso(now()) };
  const bad = /fail|invalid|nope|\.invalid$/.test(target);
  const tool = req.from.kind === 'host' ? undefined : req.check === 'dns' ? 'getent' : req.check === 'tcp' ? 'nc' : req.check === 'http' ? 'wget' : 'ping';
  switch (req.check) {
    case 'dns':
      return bad
        ? { ...base, ok: false, durationMs: 31, summary: 'no such host', error: `lookup ${target}: no such host`, tool, output: tool ? `getent: ${target}: not found` : undefined }
        : { ...base, ok: true, durationMs: 4, summary: '2 addresses', addresses: ['10.0.1.11', 'fd00::11'], tool, output: tool ? `10.0.1.11       ${target}\nfd00::11        ${target}` : undefined };
    case 'tcp':
      if (!/:\d+$/.test(target)) ctx.fail('invalid', 'tcp target must be host:port', { errors: [{ path: 'target', code: 'V_FORMAT', message: 'use host:port', severity: 'error' }] });
      return bad ? { ...base, ok: false, durationMs: 5003, summary: 'timed out after 5 s', error: 'dial tcp: i/o timeout', tool } : { ...base, ok: true, durationMs: 3, summary: 'connected in 3 ms', addresses: ['10.0.1.11'], tool };
    case 'http':
      if (!/^https?:\/\//.test(target)) ctx.fail('invalid', 'http target must start with http:// or https://', { errors: [{ path: 'target', code: 'V_FORMAT', message: 'use http(s)://…', severity: 'error' }] });
      return bad
        ? { ...base, ok: false, durationMs: 12, status: 502, summary: '502 Bad Gateway in 12 ms', headers: { server: 'Caddy', 'content-length': '0' }, timing: { dnsMs: 1, connectMs: 2, tlsMs: null, ttfbMs: 9, totalMs: 12 }, tool }
        : { ...base, ok: true, durationMs: 84, status: 200, summary: '200 OK in 84 ms', headers: { 'content-type': 'application/json; charset=utf-8', 'content-length': '58', server: 'Caddy', date: new Date().toUTCString(), 'x-request-id': '01J9MOCK7Q' }, timing: { dnsMs: 4, connectMs: 6, tlsMs: target.startsWith('https') ? 21 : null, ttfbMs: 49, totalMs: 84 }, tool, output: tool ? `Connecting to ${target.replace(/^https?:\/\//, '').split('/')[0]}\n  HTTP/1.1 200 OK` : undefined };
    default:
      return bad
        ? { ...base, ok: false, durationMs: 6000, summary: '100 % packet loss', error: '3 packets transmitted, 0 received', output: `PING ${target}: 56 data bytes\n--- ${target} ping statistics ---\n3 packets transmitted, 0 packets received, 100% packet loss`, tool }
        : { ...base, ok: true, durationMs: 2031, summary: '3/3 replies, avg 0.6 ms', output: `PING ${target} (10.0.1.11): 56 data bytes\n64 bytes from 10.0.1.11: seq=0 ttl=64 time=0.71 ms\n64 bytes from 10.0.1.11: seq=1 ttl=64 time=0.52 ms\n64 bytes from 10.0.1.11: seq=2 ttl=64 time=0.58 ms\n--- ${target} ping statistics ---\n3 packets transmitted, 3 packets received, 0% packet loss\nround-trip min/avg/max = 0.52/0.60/0.71 ms`, tool };
  }
}

// ───────────────────────────────────────────── inspect / top / events

function inspect(site: string, c: MC): ContainerInspectResponse {
  const tc = container(site, c);
  return {
    ref: c.ref,
    maskedEnv: ['API_TOKEN', 'DB_PASSWORD'],
    inspect: {
      Id: `${tc.id}f00dcafe1234567890abcdef1234567890abcdef1234567890ab`,
      Created: iso(now() - 5 * 86_400_000),
      Path: 'docker-entrypoint.sh',
      Args: ['node', 'dist/server.js'],
      State: { Status: tc.state, Running: tc.state === 'running', Paused: false, Restarting: false, OOMKilled: tc.oomKilled, Dead: false, Pid: tc.state === 'running' ? 21877 : 0, ExitCode: tc.exitCode ?? 0, Error: '', StartedAt: tc.startedAt, FinishedAt: tc.finishedAt ?? '0001-01-01T00:00:00Z' },
      Image: 'sha256:1f3a7bc4e1d2a9b8f7e6d5c4b3a291',
      Name: `/${tc.name}`,
      RestartCount: tc.restartCount,
      Config: {
        Hostname: tc.ref,
        User: 'node',
        Env: ['NODE_ENV=production', 'PORT=3000', 'API_TOKEN=••••••', 'DB_PASSWORD=••••••', 'DB_HOST=redis.db.opm', 'PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin'],
        Cmd: ['node', 'dist/server.js'],
        Image: c.image,
        WorkingDir: '/app',
        Labels: { 'opm.deployment': 'DepHelloWorld0000001', 'opm.site': site, 'opm.container': tc.containerSlug, 'opm.service': c.serviceSlug, 'opm.rev_seq': '7', 'opm.spec_hash': 'a1b2c3d4e5f6' },
      },
      HostConfig: { RestartPolicy: { Name: 'unless-stopped', MaximumRetryCount: 0 }, Memory: tc.memLimitBytes ?? 0, NanoCpus: 1_000_000_000, NetworkMode: 'opm-net' },
      Mounts: [
        { Type: 'bind', Source: `${HOME[site]}/volumes/${c.ref}-data`, Destination: '/app/data', RW: true },
        { Type: 'bind', Source: `${HOME[site]}/files/api-config`, Destination: '/app/config.json', RW: false },
      ],
      NetworkSettings: { Ports: Object.fromEntries(c.ports.map((p) => [`${p.containerPort}/${p.protocol}`, p.hostPort ? [{ HostIp: '127.0.0.1', HostPort: String(p.hostPort) }] : null])), Networks: { 'opm-net': { IPAddress: '172.20.0.' + (c.ref.length + 2), Aliases: [c.ref] } } },
    },
    healthLog: tc.state === 'running' ? [] : [{ start: iso(now() - 50 * 60_000), end: iso(now() - 50 * 60_000 + 120), exitCode: 1, output: 'curl: (7) Failed to connect to localhost port 3000' }],
    checks: Array.from({ length: 8 }, (_, i) => ({ at: iso(now() - (8 - i) * 30_000), ok: !(c.failedCopy && i > 4), message: c.failedCopy && i > 4 ? 'GET /health → connection refused' : 'GET /health → 200' })),
  };
}
function events(site: string, c: MC): DockerEvent[] {
  const ev: [number, string][] = [
    [23 * 3600, 'create'],
    [23 * 3600 - 2, 'start'],
    [23 * 3600 - 30, 'health_status: healthy'],
    [5 * 3600, 'die'],
    [5 * 3600 - 1, 'restart'],
    [5 * 3600 - 20, 'health_status: healthy'],
  ];
  if (manual[site]?.[c.ref]) ev.push([42 * 60, 'kill'], [42 * 60 - 1, 'stop']);
  if (c.oom) ev.push([50 * 60, 'oom']);
  return ev
    .map(([ago, action]) => ({ time: iso(now() - ago * 1000), type: 'container', action, attributes: { name: `opm-${site}-${c.ref}`, image: c.image, ...(action === 'die' ? { exitCode: '137' } : {}) } }))
    .sort((a, b) => b.time.localeCompare(a.time));
}

// ───────────────────────────────────────────── logs

const LOG_TEXT = [
  'GET /api/counter 200 4ms',
  'POST /api/counter 200 9ms',
  'GET /health 200 1ms',
  'warn: slow query took 812ms (SELECT * FROM jobs WHERE state = $1)',
  'connected to redis.db.opm:6379',
  'GET /api/devices?limit=50 200 22ms',
  'error: upstream timeout after 30000ms (device-sync)',
  'job device-sync finished in 1.2s (48 devices)',
];
function logLine(site: string, refs: string[], i: number, ms: number): LogStreamLine {
  const ref = refs[i % refs.length];
  // §21 #93: a Deno backend with ANSI colors (ESC kept) and a stream whose pipeline lost the ESC bytes
  const t = new Date(ms).toISOString().replace('Z', '123456Z');
  if (ref === 'northwind-backend-admin') {
    const x = DENO_LINES[i % DENO_LINES.length];
    return { c: ref, s: i % DENO_LINES.length < 3 ? 'stderr' : 'stdout', t, x };
  }
  if (ref === 'node-red') return { c: ref, s: 'stdout', t, x: ESC_LOST_LINES[i % ESC_LOST_LINES.length] };
  const x = LOG_TEXT[(i * 7 + ref.length) % LOG_TEXT.length];
  return { c: ref, s: /^(error|warn)/.test(x) ? 'stderr' : 'stdout', t: new Date(ms).toISOString().replace('Z', '123456Z'), x: `${x}${site === 'app' ? '' : ` [${site}]`}` };
}
function agentLine(i: number, ms: number): AgentLogLine {
  const L: [AgentLogLine['level'], string, string, Record<string, unknown>][] = [
    ['info', 'mesh', 'ping ok', { site: 'db', rttMs: 1.8 }],
    ['debug', 'cloudlink', 'poll answered', { cursor: '41', route: 'direct' }],
    ['info', 'health', 'container ready', { container: 'hello-api' }],
    ['warn', 'edge', 'upstream slow', { route: 'api', ms: 812 }],
    ['error', 'pipe', 'cloud refused the activity chain (retry in 10 min)', { status: 503 }],
  ];
  const [level, module, msg, fields] = L[i % L.length];
  return { t: new Date(ms).toISOString(), level, module, msg, fields };
}
function accessLine(i: number, ms: number): AccessLogLine {
  const R = [
    ['GET', 'hello.acme.internal', '/', 200, 1820, 3.1],
    ['GET', 'hello.acme.internal', '/api/counter', 200, 58, 4.2],
    ['POST', 'hello.acme.internal', '/api/counter', 200, 58, 9.8],
    ['GET', '10.0.1.10', '/nope', 404, 412, 0.4],
    ['GET', 'hello.acme.internal', '/api/devices?limit=50', 502, 0, 30001],
    ['GET', 'hello.acme.internal', '/assets/app.3f9a.js', 304, 0, 0.9],
  ] as const;
  const [method, host, uri, status, size, dur] = R[i % R.length];
  return { t: new Date(ms).toISOString(), remote: `10.0.1.${50 + (i % 9)}`, host, method, uri, proto: 'HTTP/1.1', status, size, durMs: dur, ua: 'Mozilla/5.0 (X11; Linux x86_64) Chrome/129', upstream: uri.startsWith('/api') ? 'hello-api:3000' : status === 404 ? null : 'hello-web:80' };
}

/** fake EventSource for the three log streams */
class MockEventSource {
  readyState = 0;
  onerror: ((e: Event) => void) | null = null;
  #l = new Map<string, ((e: MessageEvent) => void)[]>();
  #timers: ReturnType<typeof setTimeout>[] = [];
  #iv: ReturnType<typeof setInterval> | null = null;
  constructor(url: string) {
    const u = new URL(url, location.origin);
    const m = /\/sites\/([^/]+)\/tools\/logs\/(containers|agent|access)$/.exec(u.pathname);
    const site = m?.[1] ?? 'app';
    const kind = m?.[2] ?? 'containers';
    const q = u.searchParams;
    const follow = q.get('follow') === '1' || q.get('follow') === 'true';
    const tail = Math.min(Number(q.get('tail') ?? 500) || 500, 10000);
    const stdout = q.get('stdout') !== '0';
    const stderr = q.get('stderr') !== '0';
    const all = (CONTAINERS[site] ?? []).filter((c) => !c.failedCopy).map((c) => c.ref);
    const refs = !q.get('containers') || q.get('containers') === '*' ? all : q.get('containers')!.split(',').filter(Boolean);
    const filt = (q.get('q') ?? '').toLowerCase();
    const emit = (type: string, data: unknown) => {
      const ev = new MessageEvent(type, { data: JSON.stringify(data) });
      for (const f of this.#l.get(type) ?? []) f(ev);
    };
    this.#timers.push(
      setTimeout(() => {
        this.readyState = 1;
        emit('open', {});
        emit('meta', { containers: refs, follow });
        const n = Math.min(tail, kind === 'containers' ? 160 : 60);
        const end = now() - 2000;
        for (let i = 0; i < n; i++) {
          const ms = end - (n - i) * 4000;
          if (kind === 'containers') {
            const l = logLine(site, refs, i, ms);
            if ((l.s === 'stdout' && stdout) || (l.s === 'stderr' && stderr)) emit('line', l);
          } else if (kind === 'agent') {
            const l = agentLine(i, ms);
            if (!filt || l.msg.toLowerCase().includes(filt)) emit('line', l);
          } else emit('line', accessLine(i, ms));
        }
        if (!follow) {
          emit('end', { reason: 'done' });
          return;
        }
        let i = n;
        this.#iv = setInterval(() => {
          const ms = now();
          if (kind === 'containers') {
            const l = logLine(site, refs, i, ms);
            if ((l.s === 'stdout' && stdout) || (l.s === 'stderr' && stderr)) emit('line', l);
          } else if (kind === 'agent') emit('line', agentLine(i, ms));
          else emit('line', accessLine(i, ms));
          i++;
        }, 500);
      }, 80),
    );
  }
  addEventListener(type: string, fn: (e: MessageEvent) => void) {
    this.#l.set(type, [...(this.#l.get(type) ?? []), fn]);
  }
  close() {
    this.readyState = 2;
    for (const t of this.#timers) clearTimeout(t);
    if (this.#iv) clearInterval(this.#iv);
  }
}

/** fake terminal WebSocket: a tiny shell (ls, echo, pwd, whoami, date, clear, exit) */
class MockWebSocket {
  binaryType = 'arraybuffer';
  readyState = 0;
  onopen: ((e: Event) => void) | null = null;
  onmessage: ((e: MessageEvent) => void) | null = null;
  onclose: ((e: CloseEvent) => void) | null = null;
  onerror: ((e: Event) => void) | null = null;
  #line = '';
  #prompt = 'root@app:/# ';
  #enc = new TextEncoder();
  #sessionId = '';
  constructor(url: string) {
    const u = new URL(url.replace(/^ws/, 'http'));
    const t = tickets.get(u.searchParams.get('ticket') ?? '');
    setTimeout(() => {
      if (!t) {
        this.readyState = 3;
        this.onclose?.(new CloseEvent('close', { code: 4401, reason: 'ticket unknown or used' }));
        return;
      }
      tickets.delete(u.searchParams.get('ticket') ?? '');
      this.#sessionId = t.sessionId;
      this.#prompt = t.target.startsWith('host') ? `root@${t.site}:~# ` : `root@${t.target}:/app# `;
      this.readyState = 1;
      this.onopen?.(new Event('open'));
      this.#ctl({ type: 'ready', sessionId: t.sessionId, target: t.target });
      this.#out(`\x1b[2m# OPM terminal · ${t.target} on ${t.site} · output recorded (7 days)\x1b[0m\r\n${this.#prompt}`);
    }, 60);
  }
  #ctl(v: unknown) {
    this.onmessage?.(new MessageEvent('message', { data: JSON.stringify(v) }));
  }
  #out(s: string) {
    const b = this.#enc.encode(s);
    this.onmessage?.(new MessageEvent('message', { data: b.buffer.slice(b.byteOffset, b.byteOffset + b.byteLength) }));
    const ss = sessions.find((x) => x.sessionId === this.#sessionId);
    if (ss) {
      ss.bytesOut += b.length;
      ss.lastActivityAt = iso(now());
    }
  }
  send(data: string | ArrayBuffer | Uint8Array) {
    if (this.readyState !== 1) return;
    if (typeof data === 'string') {
      const c = JSON.parse(data) as { type: string };
      if (c.type === 'ping') this.#ctl({ type: 'pong' });
      return;
    }
    const s = new TextDecoder().decode(data instanceof Uint8Array ? data : new Uint8Array(data));
    for (const ch of s) {
      if (ch === '\r') {
        const cmd = this.#line.trim();
        this.#line = '';
        this.#out('\r\n');
        if (cmd === 'exit') {
          this.#out('logout\r\n');
          this.#ctl({ type: 'exit', code: 0, reason: 'the shell exited' });
          this.close();
          return;
        }
        const out = cmd === '' ? '' : cmd === 'ls' ? 'bin  dist  node_modules  package.json  config.json\r\n' : cmd.startsWith('echo ') ? cmd.slice(5) + '\r\n' : cmd === 'pwd' ? '/app\r\n' : cmd === 'whoami' ? 'root\r\n' : cmd === 'date' ? new Date().toUTCString() + '\r\n' : cmd === 'clear' ? '\x1b[2J\x1b[H' : `sh: ${cmd.split(' ')[0]}: not found\r\n`;
        this.#out(out + this.#prompt);
      } else if (ch === '\x7f') {
        if (this.#line) {
          this.#line = this.#line.slice(0, -1);
          this.#out('\b \b');
        }
      } else if (ch === '\x03') {
        this.#line = '';
        this.#out('^C\r\n' + this.#prompt);
      } else if (ch >= ' ') {
        this.#line += ch;
        this.#out(ch);
      }
    }
  }
  close() {
    if (this.readyState === 3) return;
    this.readyState = 3;
    const i = sessions.findIndex((x) => x.sessionId === this.#sessionId);
    if (i >= 0) {
      const s = sessions.splice(i, 1)[0];
      transcripts.unshift({ id: s.sessionId, siteSlug: s.siteSlug, kind: s.kind, target: s.target, command: s.command, actor: s.actor, via: s.via, startedAt: s.startedAt, endedAt: iso(now()), durationSec: Math.round((now() - Date.parse(s.startedAt)) / 1000), exitCode: 0, sizeBytes: s.bytesOut + 120, expiresAt: iso(Date.parse(s.startedAt) + 7 * 86_400_000) });
    }
    setTimeout(() => this.onclose?.(new CloseEvent('close', { code: 1000, reason: 'closed' })), 10);
  }
}
setStreamImpl(MockEventSource as never, MockWebSocket as never);

// ───────────────────────────────────────────── router

let seq = 0;
const rid = () => `01J9MOCK${(++seq).toString(36).toUpperCase().padStart(4, '0')}`;

export async function toolsFetch(url: URL, init: RequestInit, ctx: Ctx): Promise<Response> {
  const m = /^\/_opm\/api\/sites\/([^/]+)\/tools\/(.+)$/.exec(url.pathname);
  if (!m) return ctx.json(404, { error: { code: 'not-found', message: `mock: no tools endpoint ${url.pathname}` } });
  const site = decodeURIComponent(m[1]);
  const rest = m[2];
  const method = (init.method ?? 'GET').toUpperCase();
  const q = url.searchParams;
  const headers = new Headers(init.headers);
  let body: Record<string, unknown> = {};
  if (typeof init.body === 'string') {
    try {
      body = JSON.parse(init.body);
    } catch {
      /* none */
    }
  } else if (init.body instanceof Uint8Array) {
    try {
      body = JSON.parse(new TextDecoder().decode(init.body));
    } catch {
      /* none */
    }
  }
  await new Promise((r) => setTimeout(r, 50 + Math.random() * 80));
  try {
    const s = ctx.requireSession();
    const actor = () => ctx.requireActor(headers, s);
    const one = (x: string) => {
      if (x === '*') ctx.fail('invalid', '"*" is not supported here: ask one site');
      if (!SITES.includes(x)) ctx.fail('not-found', `site ${x} does not exist`);
      return x;
    };
    const ok = (v: unknown, st = 200) => ctx.json(st, v);
    let r: RegExpExecArray | null;
    if (method === 'GET' && rest === 'info') return ok(info(one(site)));
    if (method === 'GET' && rest === 'containers') {
      const list = site === '*' ? SITES : [one(site)];
      return ok({ items: list.flatMap((x) => (CONTAINERS[x] ?? []).map((c) => container(x, c))) });
    }
    if (method === 'POST' && (r = /^containers\/([^/]+)\/(restart|stop|start)$/.exec(rest))) {
      const a = actor();
      const st = one(site);
      const c = findC(st, decodeURIComponent(r[1]), ctx);
      if (c.failedCopy) ctx.fail('invalid', 'a failed copy is kept for reading only');
      stateOver[st] = stateOver[st] ?? {};
      manual[st] = manual[st] ?? {};
      if (r[2] === 'stop') {
        stateOver[st][c.ref] = 'exited';
        manual[st][c.ref] = { at: iso(now()), actor: a };
      } else {
        stateOver[st][c.ref] = 'running';
        delete manual[st][c.ref];
      }
      await new Promise((x) => setTimeout(x, 300));
      return ok({ ok: true, container: container(st, c) });
    }
    if (method === 'GET' && (r = /^containers\/([^/]+)\/(inspect|top|events)$/.exec(rest))) {
      const st = one(site);
      const c = findC(st, decodeURIComponent(r[1]), ctx);
      if (r[2] === 'inspect') return ok(inspect(st, c));
      if (r[2] === 'events') return ok({ items: events(st, c) });
      if (stateOf(st, c) !== 'running') ctx.fail('conflict', `${c.ref} is not running`);
      return ok({
        ref: c.ref,
        titles: ['UID', 'PID', 'PPID', 'C', 'STIME', 'TTY', 'TIME', 'CMD'],
        processes: [
          ['node', '21877', '21851', '2', '03:11', '?', '00:01:42', 'node dist/server.js'],
          ['node', '21912', '21877', '0', '03:11', '?', '00:00:03', '/usr/local/bin/node dist/worker.js --queue jobs'],
          ['node', '22410', '21877', '0', '03:40', '?', '00:00:00', 'sh -c curl -fsS localhost:3000/health'],
        ],
      });
    }
    if (method === 'GET' && rest === 'logs/containers/download') {
      const st = one(site);
      const refs = (q.get('containers') ?? '').split(',').filter(Boolean);
      const list = refs.length ? refs : (CONTAINERS[st] ?? []).map((c) => c.ref);
      const lines = Array.from({ length: 50 }, (_, i) => logLine(st, list, i, now() - (50 - i) * 4000)).map((l) => `${l.t} ${l.c} ${l.s} ${l.x}`);
      return new Response(lines.join('\n') + '\n', { status: 200, headers: { 'Content-Type': 'text/plain; charset=utf-8', 'Content-Disposition': `attachment; filename="opm-${st}-logs-${new Date().toISOString().slice(0, 10)}.log"` } });
    }
    // terminal
    if (rest === 'terminal/sessions' && method === 'GET') return ok({ items: sessions.filter((x) => site === '*' || x.siteSlug === site) });
    if (rest === 'terminal/sessions' && method === 'POST') {
      const a = actor();
      const st = one(site);
      const inf = info(st);
      const kind = body.kind as string;
      if (kind === 'host') {
        if (!inf.hostShell.enabled) ctx.fail('forbidden', 'the host shell is disabled on this site (agent.json tools.hostShell: false)', { tool: 'hostShell' });
        if (String(body.confirm ?? '').trim() !== st) ctx.fail('invalid', `type the site name "${st}" to open a host shell`, { errors: [{ path: 'confirm', code: 'V_CONFIRM', message: `type ${st}`, severity: 'error' }] });
      } else {
        if (!inf.containerExec.enabled) ctx.fail('forbidden', 'container exec is disabled on this site (agent.json tools.containerExec: false)', { tool: 'containerExec' });
        const c = findC(st, String(body.containerSlug ?? ''), ctx);
        if (stateOf(st, c) !== 'running') ctx.fail('conflict', `${c.ref} is not running`);
      }
      if (sessions.filter((x) => x.siteSlug === st).length >= 4) ctx.fail('conflict', '4 terminal sessions are already open on this site', { max: 4 });
      const id = rid();
      const target = kind === 'host' ? 'host (root)' : String(body.containerSlug);
      const command = kind === 'host' ? ['/bin/bash', '-l'] : body.shell === 'custom' ? ((body.command as string[]) ?? ['/bin/sh']) : [body.shell === 'bash' ? '/bin/bash' : '/bin/sh'];
      sessions.push({ sessionId: id, siteSlug: st, kind: kind === 'host' ? 'host' : 'container', target, command, actor: a, startedAt: iso(now()), lastActivityAt: iso(now()), via: null, bytesOut: 0, open: true });
      const ticket = `tk_${id}`;
      tickets.set(ticket, { site: st, sessionId: id, target });
      return ok({ sessionId: id, ticket, expiresAt: iso(now() + 60_000), wsPath: `/_opm/api/sites/${st}/tools/terminal/ws?ticket=${ticket}` }, 201);
    }
    if (method === 'POST' && (r = /^terminal\/sessions\/([^/]+)\/close$/.exec(rest))) {
      actor();
      const i = sessions.findIndex((x) => x.sessionId === r![1]);
      if (i < 0) ctx.fail('not-found', 'no such session');
      sessions.splice(i, 1);
      return ok({ closed: true });
    }
    if (method === 'GET' && rest === 'terminal/transcripts') return ok({ items: transcripts.filter((x) => x.siteSlug === site || site === '*') });
    if (method === 'POST' && (r = /^terminal\/transcripts\/([^/]+)\/ticket$/.exec(rest))) {
      actor();
      if (!transcripts.some((x) => x.id === r![1])) ctx.fail('not-found', 'no such transcript');
      const fmt = q.get('format') === 'cast' ? 'cast' : 'text';
      return ok({ url: `/_opm/api/sites/${site}/tools/terminal/transcripts/${r[1]}/download?ticket=dl_${rid()}&format=${fmt}`, expiresAt: iso(now() + 300_000) });
    }
    if (method === 'GET' && (r = /^terminal\/transcripts\/([^/]+)\/download$/.exec(rest))) {
      const cast = q.get('format') === 'cast';
      const text = cast ? `{"version":2,"width":120,"height":32,"timestamp":${Math.floor(now() / 1000)}}\n[0.1,"o","/app # "]\n[1.2,"o","ls\\r\\n"]\n` : TRANSCRIPT_TEXT;
      return new Response(text, { status: 200, headers: { 'Content-Type': 'text/plain; charset=utf-8', 'Content-Disposition': `attachment; filename="opm-${site}-terminal-${r[1]}.${cast ? 'cast' : 'txt'}"` } });
    }
    // monitoring
    if (method === 'GET' && rest === 'metrics/summary') {
      if (site === '*') return ok({ items: SITES.map(summary) });
      return ok(summary(one(site)));
    }
    if (method === 'GET' && rest === 'metrics/series') {
      const range = q.get('range') ?? '1h';
      if (!['1h', '24h', '7d'].includes(range)) ctx.fail('invalid', 'range must be 1h, 24h or 7d');
      return ok(series(one(site), q.get('subject') ?? 'host', range));
    }
    // volumes
    if (method === 'GET' && rest === 'volumes') {
      const list = site === '*' ? SITES.filter((x) => !(knob('opm.mock.down') ?? '').split(',').includes(x)) : [one(site)];
      return ok({ items: list.flatMap((st) => (VOLS[st] ?? []).map(({ v, root }) => ({ ...v, siteSlug: st, exists: true, ...(() => { const z = nodeSize(root); return { sizeBytes: z.size, fileCount: z.files }; })(), sizeTruncated: false }))) });
    }
    if (method === 'GET' && (r = /^volumes\/([^/]+)\/tree$/.exec(rest))) {
      const st = one(site);
      const v = vol(st, decodeURIComponent(r[1]), ctx);
      const p = norm(q.get('path'));
      const n = walk(v.root, p, ctx);
      if (n.type !== 'dir') ctx.fail('invalid', `${p} is not a directory`);
      const entries: VolumeEntry[] = Object.entries((n as { children: Record<string, Node> }).children).map(([name, c]) => ({
        name,
        path: (p === '/' ? '' : p) + '/' + name,
        type: c.type,
        sizeBytes: nodeSize(c).size,
        mtime: iso(now() - name.length * 3_600_000 * 5),
        mode: c.type === 'dir' ? 'drwxr-xr-x' : c.type === 'symlink' ? 'lrwxrwxrwx' : '-rw-r--r--',
        linkTarget: c.type === 'symlink' ? c.target : null,
        textLike: c.type === 'file' && !c.binary && c.size <= MiB,
      }));
      entries.sort((a, b) => (a.type === 'dir' ? 0 : 1) - (b.type === 'dir' ? 0 : 1) || a.name.localeCompare(b.name));
      return ok({ volumeSlug: v.v.volumeSlug, path: p, entries, truncated: false });
    }
    if (method === 'GET' && (r = /^volumes\/([^/]+)\/file$/.exec(rest))) {
      const st = one(site);
      const v = vol(st, decodeURIComponent(r[1]), ctx);
      const p = norm(q.get('path'));
      const n = walk(v.root, p, ctx);
      if (n.type !== 'file') ctx.fail('invalid', `${p} is not a file`);
      const f = n as { size: number; text?: string; binary?: boolean };
      return ok({ volumeSlug: v.v.volumeSlug, path: p, sizeBytes: f.size, mtime: iso(now() - 3_600_000), binary: !!f.binary, text: f.binary || f.size > MiB ? null : (f.text ?? '') });
    }
    if (method === 'POST' && (r = /^volumes\/([^/]+)\/download$/.exec(rest))) {
      actor();
      const st = one(site);
      const v = vol(st, decodeURIComponent(r[1]), ctx);
      const p = norm(q.get('path'));
      walk(v.root, p, ctx);
      return ok({ url: `/_opm/api/sites/${st}/tools/volumes/${v.v.volumeSlug}/download?ticket=dl_${rid()}&path=${encodeURIComponent(p)}`, expiresAt: iso(now() + 300_000) });
    }
    if (method === 'GET' && (r = /^volumes\/([^/]+)\/download$/.exec(rest))) {
      const st = one(site);
      const v = vol(st, decodeURIComponent(r[1]), ctx);
      const p = norm(q.get('path'));
      const n = walk(v.root, p, ctx);
      const isFile = n.type === 'file';
      const name = isFile ? p.split('/').pop()! : `${v.v.volumeSlug}${p === '/' ? '' : p.replace(/\//g, '_')}.tar.gz`;
      return new Response(isFile ? ((n as { text?: string }).text ?? 'binary') : 'mock tar.gz', { status: 200, headers: { 'Content-Type': isFile ? 'application/octet-stream' : 'application/gzip', 'Content-Disposition': `attachment; filename="${name}"` } });
    }
    // §21 #96 file operations
    if (method === 'POST' && (r = /^volumes\/([^/]+)\/(file|upload|mkdir|move|delete)$/.exec(rest))) {
      actor();
      const st = one(site);
      const v = vol(st, decodeURIComponent(r[1]), ctx);
      const busy = volBusy.get(`${st}/${v.v.volumeSlug}`);
      if (busy) ctx.fail('locked', `volume ${v.v.volumeSlug} is busy: ${busy}`);
      const op = r[2];
      if (op === 'file') {
        const p = norm(String(body.path ?? ''));
        const text = String(body.text ?? '');
        if (new TextEncoder().encode(text).length > MiB) ctx.fail('too-large', 'text files up to 1 MiB can be edited here; upload bigger files');
        const { dirNode, name } = parentOf(v.root, p, ctx, body.baseMtime == null);
        const ex = dirNode.children[name];
        if (body.baseMtime == null && ex) ctx.fail('conflict', `${p} already exists`);
        if (body.baseMtime != null && !ex) ctx.fail('conflict', `${p} was deleted since you opened it`);
        if (ex && ex.type !== 'file') ctx.fail('conflict', `${p} exists and is not a regular file`);
        dirNode.children[name] = txt(text);
        return ok({ entry: entryOf(p, dirNode.children[name]) });
      }
      if (op === 'upload') {
        const p = norm(q.get('path'));
        const size = init.body instanceof Blob ? init.body.size : typeof init.body === 'string' ? init.body.length : 0;
        const { dirNode, name } = parentOf(v.root, p, ctx, true);
        if (dirNode.children[name] && q.get('overwrite') !== '1') ctx.fail('conflict', `${p} already exists`, { type: dirNode.children[name].type });
        dirNode.children[name] = { type: 'file', size, binary: !/\.(txt|json|ya?ml|conf|ini|env|md|js|ts|csv|log|xml|sql|sh)$/i.test(name) };
        return ok({ entry: entryOf(p, dirNode.children[name]) });
      }
      if (op === 'mkdir') {
        const p = norm(String(body.path ?? ''));
        const { dirNode, name } = parentOf(v.root, p, ctx, true);
        if (dirNode.children[name]) ctx.fail('conflict', `${p} already exists`);
        dirNode.children[name] = dir({});
        return ok({ entry: entryOf(p, dirNode.children[name]) });
      }
      if (op === 'move') {
        const from = norm(String(body.from ?? ''));
        const to = norm(String(body.to ?? ''));
        if (from === to) ctx.fail('invalid', 'the new path is the same');
        if ((to + '/').startsWith(from + '/')) ctx.fail('invalid', 'a folder cannot move into itself');
        const a = parentOf(v.root, from, ctx, false);
        const n = a.dirNode.children[a.name];
        if (!n) ctx.fail('not-found', `${from} does not exist`);
        const b = parentOf(v.root, to, ctx, true);
        if (b.dirNode.children[b.name] && (!body.overwrite || b.dirNode.children[b.name].type === 'dir')) ctx.fail('conflict', `${to} already exists`);
        delete a.dirNode.children[a.name];
        b.dirNode.children[b.name] = n;
        return ok({ entry: entryOf(to, n) });
      }
      // delete
      if (body.confirm !== 'DELETE') ctx.fail('invalid', 'type DELETE to confirm');
      const paths = (body.paths as string[]) ?? [];
      let count = 0;
      const cnt = (n: Node): number => 1 + (n.type === 'dir' ? Object.values(n.children).reduce((x, c) => x + cnt(c), 0) : 0);
      for (const raw of paths) {
        const p = norm(raw);
        const { dirNode, name } = parentOf(v.root, p, ctx, false);
        if (!dirNode.children[name]) ctx.fail('not-found', `${p} does not exist`);
      }
      for (const raw of paths) {
        const { dirNode, name } = parentOf(v.root, norm(raw), ctx, false);
        count += cnt(dirNode.children[name]);
        delete dirNode.children[name];
      }
      return ok({ deleted: count });
    }
    // network
    if (method === 'POST' && rest === 'netcheck') {
      actor();
      const st = one(site);
      await new Promise((x) => setTimeout(x, 250));
      return ok(netcheck(st, body as unknown as NetcheckRequest, ctx));
    }
    // docker
    if (method === 'GET' && rest === 'docker/images') {
      if (site === '*') return ok({ items: SITES.map(images) });
      return ok(images(one(site)));
    }
    if (method === 'POST' && rest === 'docker/prune-images') {
      actor();
      const st = one(site);
      if (body.typed !== 'PRUNE') ctx.fail('invalid', 'type PRUNE to confirm');
      const before = images(st).items.filter((x) => !x.inUse && !x.protected);
      dockerPruned[st] = true;
      return ok({ deleted: before.map((x) => ({ id: x.id, tags: x.tags, sizeBytes: x.sizeBytes })), skipped: [], reclaimedBytes: before.reduce((a, x) => a + x.sizeBytes, 0) });
    }
    return ctx.json(404, { error: { code: 'not-found', message: `mock: no tools endpoint ${method} ${url.pathname}` } });
  } catch (e) {
    const err = e as { code?: string; message?: string; details?: Record<string, unknown> };
    if (err && typeof err.code === 'string') {
      const st: Record<string, number> = { unauthenticated: 401, 'session-expired': 401, 'actor-required': 401, forbidden: 403, 'not-found': 404, conflict: 409, locked: 409, invalid: 422, 'rate-limited': 429, unavailable: 503 };
      return ctx.json(st[err.code] ?? 500, { error: { code: err.code, message: err.message, details: err.details ?? {} } });
    }
    throw e;
  }
}
