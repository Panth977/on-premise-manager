/**
 * Agent operator toolbox (SPEC-final §7.4, §21 #89): logs, terminal (container exec + host shell), monitoring,
 * live container ops + inspect, volumes browser (read-only), network checks, Docker housekeeping.
 *
 * Every path is per site: /_opm/api/sites/{siteSlug}/tools/… . {siteSlug} = this site (local), another site (forwarded
 * over the mesh with the actor identity; shells and streams are bridged, both sites write the activity), or "*" for the
 * GETs marked "merged" (one item per reachable site).
 *
 * Auth: reads = session cookie; writes and sensitive reads (transcript content, volume archive ticket) = session + actor.
 * Streams: logs = text/event-stream (EventSource, cookie); terminal = WebSocket (same origin, single-use ticket from
 * createTerminal, 60 s). Downloads with a ticket are plain links (the browser streams them).
 */
import { createClient, ep, type Client } from './http.js';
import type { ListResponse } from './types.js';

const S = 'session' as const;
const SA = 'session+actor' as const;

// ───────────────────────────────────────────────────────────── capabilities

/** agent.json `tools` + `hostShell` (all optional; defaults shown). */
export interface AgentToolsConfig {
  tools?: { hostShell?: boolean /* true */; containerExec?: boolean /* true */ };
  hostShell?: { user?: string /* the agent's user (root in production) */; shell?: string /* the user's login shell, else /bin/bash, else /bin/sh */ };
}

export interface ToolSwitch {
  enabled: boolean;
  /** why it is off (agent.json `tools.hostShell: false`, not supported on this OS, …) */
  reason: string | null;
}

export interface ToolsInfo {
  siteSlug: string;
  os: string; //            linux | darwin
  arch: string;
  agentUser: string; //     user the agent runs as
  dockerVersion: string | null;
  hostShell: ToolSwitch & { user: string; shell: string };
  containerExec: ToolSwitch;
  limits: { maxSessions: number /* 4 */; idleTimeoutSec: number /* 1800 */; transcriptDays: number /* 7 */; viewMaxBytes: number /* 1 MiB */ };
  activeSessions: number;
  metrics: { intervalSec: number /* 15 */; rawSec: number /* 3600 */; minuteSec: number /* 86400 */; quarterSec: number /* 604800 */ };
  configPath: string | null; // agent.json path (for the "how to enable" hint)
}

// ───────────────────────────────────────────────────────────── containers (live)

export type ToolsContainerState = 'running' | 'restarting' | 'paused' | 'exited' | 'created' | 'dead' | 'removing' | 'missing';

export interface ToolsContainer {
  siteSlug: string;
  /** containerSlug for OPM containers; the docker name for a kept failed container (`<name>-failed-<unix>`) */
  ref: string;
  containerSlug: string;
  name: string; //          docker name
  id: string | null; //     docker id (12 chars) — null when missing
  serviceSlug: string;
  image: string;
  failedCopy: boolean; //   a container kept after a failed update (§21 #87); read-only tools only
  state: ToolsContainerState;
  status: string; //        docker "Up 3 hours (healthy)"
  health: 'healthy' | 'unhealthy' | 'starting' | 'none';
  startedAt: string | null;
  finishedAt: string | null;
  exitCode: number | null;
  restartCount: number;
  oomKilled: boolean;
  /** stopped from the toolbox; the agent does not treat it as a failure until a start or the next update (§21 #89 d) */
  manuallyStopped: { at: string; actor: string | null } | null;
  /** enabled in the head config (the update + health system expects it running) */
  expectedRunning: boolean;
  ports: { containerPort: number; hostPort: number | null; protocol: string }[];
  /** latest sample (null until the first 15 s sample) */
  cpuPct: number | null;
  memBytes: number | null;
  memLimitBytes: number | null;
}

export interface ContainerActionRequest {
  /** optional note stored in the activity */
  reason?: string;
}
export interface ContainerActionResponse {
  ok: true;
  container: ToolsContainer;
}

export interface ContainerInspectResponse {
  ref: string;
  /** the docker inspect JSON; Config.Env values of secret-named / secret-sourced variables replaced by "••••••" */
  inspect: Record<string, unknown>;
  maskedEnv: string[]; //   env names whose value was masked
  healthLog: { start: string; end: string; exitCode: number; output: string }[]; // docker HEALTHCHECK log (if any)
  checks: { at: string; ok: boolean; message: string }[]; //                         OPM health checks (driver history)
}

export interface ContainerTopResponse {
  ref: string;
  titles: string[];
  processes: string[][];
}

export interface DockerEvent {
  time: string; //          ISO
  type: string; //          container
  action: string; //        start, die, oom, health_status: healthy, …
  attributes: Record<string, string>;
}

// ───────────────────────────────────────────────────────────── logs

export type LogSource = 'containers' | 'agent' | 'access';

/** SSE `line` event data of the container log stream (compact keys keep big tails small). */
export interface LogStreamLine {
  c: string; //  container ref
  s: 'stdout' | 'stderr';
  t: string; //  RFC3339Nano timestamp from Docker
  x: string; //  text (no trailing newline)
}
/** SSE `line` event of the agent log stream (dlog JSONL). */
export interface AgentLogLine {
  t: string;
  level: 'debug' | 'info' | 'warn' | 'error';
  module: string;
  msg: string;
  fields: Record<string, unknown>;
}
/** SSE `line` event of the edge access log. */
export interface AccessLogLine {
  t: string;
  remote: string;
  host: string;
  method: string;
  uri: string;
  proto: string;
  status: number;
  size: number;
  durMs: number;
  ua: string;
  upstream: string | null;
}
/** SSE events: `line` (one of the line types), `meta` {containers, follow}, `end` {reason}, `error` {message}. */
export interface LogStreamQuery {
  /** comma separated container refs (containers source); "*" = every OPM container of the site */
  containers?: string;
  stdout?: boolean; //   default true
  stderr?: boolean; //   default true
  since?: string; //     ISO or unix seconds
  until?: string; //     ISO or unix seconds (no follow when set)
  tail?: number; //      lines per container, default 500, max 10000
  follow?: boolean;
  q?: string; //         server-side substring filter (agent/access logs)
  level?: string; //     agent log minimum level
}

// ───────────────────────────────────────────────────────────── terminal

export type TerminalKind = 'container' | 'host';
export interface CreateTerminalRequest {
  kind: TerminalKind;
  /** container ref (kind container) */
  containerSlug?: string;
  /** sh | bash | custom (container); host uses the configured shell */
  shell?: 'sh' | 'bash' | 'custom';
  /** custom command (argv), shell custom only */
  command?: string[];
  /** host shell: the site slug typed by the operator */
  confirm?: string;
  cols?: number;
  rows?: number;
}
export interface CreateTerminalResponse {
  sessionId: string;
  ticket: string; //     single-use, 60 s
  expiresAt: string;
  /** path of the WebSocket on this agent (same origin): /_opm/api/sites/{site}/tools/terminal/ws?ticket=… */
  wsPath: string;
}
export interface TerminalSession {
  sessionId: string;
  siteSlug: string;
  kind: TerminalKind;
  target: string; //     container ref, or "host (<user>)"
  command: string[];
  actor: string | null;
  startedAt: string;
  lastActivityAt: string;
  /** site the operator is working from when the session was forwarded over the mesh */
  via: string | null;
  bytesOut: number;
  /** false = reserved (ticket issued, WebSocket not attached yet) */
  open: boolean;
}
/**
 * WebSocket protocol (one terminal per socket):
 *   binary frames  = raw terminal bytes, both directions
 *   text frames    = JSON control:
 *     client → { type: 'resize', cols, rows } | { type: 'ping' }
 *     server → { type: 'ready', sessionId, target } | { type: 'exit', code: number | null, reason } |
 *              { type: 'error', message } | { type: 'idle-warning', secondsLeft } | { type: 'pong' }
 */
export type TerminalControl =
  | { type: 'resize'; cols: number; rows: number }
  | { type: 'ping' }
  | { type: 'ready'; sessionId: string; target: string }
  | { type: 'exit'; code: number | null; reason: string }
  | { type: 'error'; message: string }
  | { type: 'idle-warning'; secondsLeft: number }
  | { type: 'pong' };

export interface TranscriptMeta {
  id: string; //         = sessionId
  siteSlug: string;
  kind: TerminalKind;
  target: string;
  command: string[];
  actor: string | null;
  via: string | null;
  startedAt: string;
  endedAt: string | null; // null = still open
  durationSec: number | null;
  exitCode: number | null;
  sizeBytes: number;
  expiresAt: string; //  startedAt + 7 days
}

// ───────────────────────────────────────────────────────────── monitoring

export interface HostSample {
  t: string;
  cpuPct: number | null;
  cpuCount: number;
  memUsedBytes: number | null;
  memTotalBytes: number | null;
  swapUsedBytes: number | null;
  swapTotalBytes: number | null;
  load1: number | null;
  load5: number | null;
  load15: number | null;
  uptimeSec: number | null;
  disks: { mount: string; label: string; usedBytes: number; totalBytes: number }[];
}
export interface ContainerSample {
  ref: string;
  t: string;
  cpuPct: number | null;
  memBytes: number | null;
  memLimitBytes: number | null;
  netRxBps: number | null; //   bytes / s
  netTxBps: number | null;
  blkReadBps: number | null;
  blkWriteBps: number | null;
  restartCount: number;
  oomKilled: boolean;
}
export type ThresholdLevel = 'ok' | 'amber' | 'red';
export interface MetricAlert {
  subject: string; //   host | disk:<mount> | container:<ref>
  metric: 'cpu' | 'mem' | 'disk';
  level: Exclude<ThresholdLevel, 'ok'>;
  value: number; //     percent
  since: string | null;
  message: string;
}
export interface MetricsSummary {
  siteSlug: string;
  intervalSec: number;
  sampledAt: string | null;
  host: HostSample | null;
  containers: (ContainerSample & { spark: { cpu: (number | null)[]; mem: (number | null)[] } /* last 60 min, 1 point / min */ })[];
  alerts: MetricAlert[];
  thresholds: { cpuPct: number /* 85 */; memPct: number /* 90 */; diskPct: number /* 90 */; sustainSec: number /* 300 */ };
  dbBytes: number;
}
export type MetricsRange = '1h' | '24h' | '7d';
export type MetricName = 'cpuPct' | 'cpuPeak' | 'memBytes' | 'memLimitBytes' | 'netRxBps' | 'netTxBps' | 'blkReadBps' | 'blkWriteBps' | 'restartCount' | 'memUsedBytes' | 'memTotalBytes' | 'swapUsedBytes' | 'swapTotalBytes' | 'load1' | 'load5' | 'load15' | 'diskUsedBytes' | 'diskTotalBytes';
/** default metrics per subject: host → cpuPct, memUsedBytes, memTotalBytes, swapUsedBytes, swapTotalBytes, load1/5/15; disk:<mount> → diskUsedBytes, diskTotalBytes; container:<ref> → cpuPct, cpuPeak, memBytes, memLimitBytes, netRxBps, netTxBps, blkReadBps, blkWriteBps, restartCount */
export interface MetricsSeriesResponse {
  siteSlug: string;
  subject: string; //   host | container:<ref> | disk:<mount>
  range: MetricsRange;
  stepSec: number; //   15 | 60 | 900
  /** unix seconds of each point */
  t: number[];
  series: Partial<Record<MetricName, (number | null)[]>>;
}

// ───────────────────────────────────────────────────────────── volumes

export interface ToolsVolume {
  /** §21 #96: the site (the "*" merge lists every site's volumes) */
  siteSlug: string;
  volumeSlug: string;
  label: string;
  kind: 'dir' | 'file';
  hostPath: string;
  exists: boolean;
  sizeBytes: number | null; //  null while not computed (big tree, see sizeTruncated)
  fileCount: number | null;
  sizeTruncated: boolean;
  usedBy: string[]; //          container refs mounting it
  backup: boolean;
}
export interface VolumeEntry {
  name: string;
  path: string; //       relative to the volume root, "/" separated, leading "/"
  type: 'dir' | 'file' | 'symlink' | 'other';
  sizeBytes: number | null; // files: size; dirs: total size of the subtree (capped walk; null when the 2 s budget ran out)
  mtime: string;
  mode: string; //       -rw-r--r--
  linkTarget: string | null;
  textLike: boolean; //  viewable (≤ 1 MiB, no NUL bytes in the first 8 KiB)
}
export interface VolumeTreeResponse {
  volumeSlug: string;
  path: string;
  entries: VolumeEntry[];
  truncated: boolean; // > 2000 entries in the directory
}
export interface VolumeFileResponse {
  volumeSlug: string;
  path: string;
  sizeBytes: number;
  mtime: string;
  binary: boolean;
  /** UTF-8 text (≤ 1 MiB); null when binary or too large (download instead) */
  text: string | null;
}
// §21 #96 volume manager — file operations (session + actor; refused (locked) while an update or a volume action runs on
// the volume; 120 per session per minute; activities volume.file-*). New files / folders take the owner of their parent
// folder, so the container that owns the volume can read them.
export interface VolumeWriteFileRequest {
  path: string;
  /** UTF-8 text, ≤ 1 MiB */
  text: string;
  /** mtime the editor loaded (conflict when the file changed since); null = create, the file must not exist */
  baseMtime: string | null;
}
export interface VolumeMkdirRequest {
  path: string;
}
export interface VolumeMoveRequest {
  from: string;
  to: string;
  /** replace an existing file at `to` (never a folder) */
  overwrite?: boolean;
}
export interface VolumeDeleteRequest {
  paths: string[];
  /** "DELETE", typed */
  confirm: string;
}
export interface VolumeWriteResponse {
  entry: VolumeEntry;
}
export interface VolumeDeleteResponse {
  deleted: number; //  entries removed (a folder counts its whole subtree)
}
export const VOLUME_FILE_LIMITS = { textMax: 1 << 20, uploadMax: 2 * 1024 * 1024 * 1024, writesPerMinute: 120 } as const;

export interface DownloadTicketResponse {
  /** same-origin path with a single-use ticket (5 min); open as a plain link */
  url: string;
  expiresAt: string;
}

// ───────────────────────────────────────────────────────────── network

export type NetcheckKind = 'dns' | 'tcp' | 'http' | 'ping';
export interface NetcheckRequest {
  /** host (default) or a container ref */
  from: { kind: 'host' } | { kind: 'container'; containerSlug: string };
  check: NetcheckKind;
  /** dns: name · tcp: host:port · http: http(s)://… · ping: host */
  target: string;
  timeoutSec?: number; // 1–30, default 10
}
export interface NetcheckResult {
  ok: boolean;
  check: NetcheckKind;
  from: string; //       "host" | container ref
  target: string;
  at: string;
  durationMs: number;
  summary: string; //    one line ("200 OK in 84 ms", "connected in 3 ms", "3 addresses", "no route to host")
  addresses?: string[];
  status?: number;
  headers?: Record<string, string>;
  timing?: { dnsMs: number | null; connectMs: number | null; tlsMs: number | null; ttfbMs: number | null; totalMs: number };
  /** raw tool output (container checks run busybox / coreutils tools inside the container) */
  output?: string;
  /** the tool used inside the container (getent, nslookup, nc, wget, curl, ping, sh /dev/tcp) */
  tool?: string;
  error?: string;
}

// ───────────────────────────────────────────────────────────── docker housekeeping

export interface DockerImageRow {
  id: string; //         sha256:… (12 shown)
  tags: string[];
  sizeBytes: number;
  created: string;
  containers: { name: string; ref: string | null; state: string }[];
  inUse: boolean;
  opm: boolean; //       carries an OPM tag (opm/cfg:… or <service>:<version> of this deployment)
  protected: boolean; // used by the head revision of this site → never pruned
}
export interface DockerDiskUsage {
  images: { count: number; sizeBytes: number; reclaimableBytes: number };
  containers: { count: number; sizeBytes: number; reclaimableBytes: number };
  volumes: { count: number; sizeBytes: number; reclaimableBytes: number };
  buildCache: { count: number; sizeBytes: number; reclaimableBytes: number };
}
export interface DockerImagesResponse {
  siteSlug: string;
  dockerVersion: string | null;
  items: DockerImageRow[];
  df: DockerDiskUsage | null;
}
export interface PruneImagesRequest {
  /** must be "PRUNE" */
  typed: string;
}
export interface PruneImagesResponse {
  deleted: { id: string; tags: string[]; sizeBytes: number }[];
  skipped: { id: string; tags: string[]; reason: string }[];
  reclaimedBytes: number;
}

// ───────────────────────────────────────────────────────────── endpoint table

const P = '/_opm/api/sites/{siteSlug}/tools';
type Merged<T> = ListResponse<T>;

export const TOOLS_API = {
  toolsInfo: ep<void, ToolsInfo>()({ method: 'GET', path: `${P}/info`, auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: 'Which tools are enabled on the site (agent.json tools.*), limits, agent user, Docker version.' }),
  // containers
  toolsContainers: ep<void, Merged<ToolsContainer>>()({ method: 'GET', path: `${P}/containers`, auth: S, perm: null, body: 'none', res: 'json', errors: ['unavailable'], summary: 'Live state of the OPM containers (+ kept failed copies) with the latest sample. "*" = merged.' }),
  restartContainer: ep<ContainerActionRequest, ContainerActionResponse>()({ method: 'POST', path: `${P}/containers/{ref}/restart`, auth: SA, perm: null, body: 'json', res: 'json', errors: ['not-found', 'invalid', 'locked', 'rate-limited', 'unavailable'], activities: ['container.restarted'], summary: 'docker restart (stop timeout of the image). Clears manuallyStopped. locked while an update runs.' }),
  stopContainer: ep<ContainerActionRequest, ContainerActionResponse>()({ method: 'POST', path: `${P}/containers/{ref}/stop`, auth: SA, perm: null, body: 'json', res: 'json', errors: ['not-found', 'invalid', 'locked', 'rate-limited', 'unavailable'], activities: ['container.stopped'], summary: 'docker stop; marks manuallyStopped until a start or the next update.' }),
  startContainer: ep<ContainerActionRequest, ContainerActionResponse>()({ method: 'POST', path: `${P}/containers/{ref}/start`, auth: SA, perm: null, body: 'json', res: 'json', errors: ['not-found', 'invalid', 'locked', 'rate-limited', 'unavailable'], activities: ['container.started'], summary: 'docker start; clears manuallyStopped.' }),
  inspectContainer: ep<void, ContainerInspectResponse>()({ method: 'GET', path: `${P}/containers/{ref}/inspect`, auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: 'docker inspect with secret env values masked + health history.' }),
  containerTop: ep<void, ContainerTopResponse>()({ method: 'GET', path: `${P}/containers/{ref}/top`, auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'conflict', 'unavailable'], summary: 'docker top (ps -ef style); conflict when the container is not running.' }),
  containerEvents: ep<void, ListResponse<DockerEvent>, { since?: string }>()({ method: 'GET', path: `${P}/containers/{ref}/events`, auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: 'Docker events of the container, newest first (default and max: last 24 h, 1000).' }),
  // logs
  logStream: ep<void, Response, LogStreamQuery>()({ method: 'GET', path: `${P}/logs/containers`, auth: S, perm: null, body: 'none', res: 'raw', errors: ['not-found', 'unavailable'], summary: 'text/event-stream of container log lines (merged by timestamp across containers); follow keeps it open.' }),
  logDownload: ep<void, Response, LogStreamQuery>()({ method: 'GET', path: `${P}/logs/containers/download`, auth: S, perm: null, body: 'none', res: 'raw', errors: ['not-found', 'unavailable'], summary: 'Same lines as a .log attachment ("<ts> <container> <stream> <text>").' }),
  agentLogStream: ep<void, Response, LogStreamQuery>()({ method: 'GET', path: `${P}/logs/agent`, auth: S, perm: null, body: 'none', res: 'raw', errors: [], summary: 'text/event-stream of the agent debug log (JSONL, redacted); follow tails the current day file.' }),
  accessLogStream: ep<void, Response, LogStreamQuery>()({ method: 'GET', path: `${P}/logs/access`, auth: S, perm: null, body: 'none', res: 'raw', errors: [], summary: 'text/event-stream of the edge access log (<agentHome>/logs/edge-access.log, rotated 20 MiB × 5).' }),
  // terminal
  createTerminal: ep<CreateTerminalRequest, CreateTerminalResponse>()({ method: 'POST', path: `${P}/terminal/sessions`, auth: SA, perm: null, body: 'json', res: 'json', ok: 201, errors: ['forbidden', 'invalid', 'not-found', 'conflict', 'rate-limited', 'unavailable'], activities: ['terminal.opened'], summary: 'Reserve a terminal (container exec or host shell; host needs confirm = site slug) → single-use WebSocket ticket. conflict = 4 sessions already open on the site.' }),
  terminalSessions: ep<void, ListResponse<TerminalSession>>()({ method: 'GET', path: `${P}/terminal/sessions`, auth: S, perm: null, body: 'none', res: 'json', errors: [], summary: 'Open terminal sessions of the site.' }),
  closeTerminal: ep<void, { closed: true }>()({ method: 'POST', path: `${P}/terminal/sessions/{sessionId}/close`, auth: SA, perm: null, body: 'none', res: 'json', errors: ['not-found'], activities: ['terminal.closed'], summary: 'Kill an open session (any operator may end any session; logged).' }),
  transcripts: ep<void, ListResponse<TranscriptMeta>>()({ method: 'GET', path: `${P}/terminal/transcripts`, auth: S, perm: null, body: 'none', res: 'json', errors: [], summary: 'Terminal transcripts kept on the site (7 days), newest first.' }),
  transcriptTicket: ep<void, DownloadTicketResponse, { format?: 'cast' | 'text' }>()({ method: 'POST', path: `${P}/terminal/transcripts/{id}/ticket`, auth: SA, perm: null, body: 'none', res: 'json', errors: ['not-found'], summary: 'Single-use link to the transcript (asciicast v2 .cast, or text with ANSI removed).' }),
  // monitoring
  metricsSummary: ep<void, MetricsSummary>()({ method: 'GET', path: `${P}/metrics/summary`, auth: S, perm: null, body: 'none', res: 'json', errors: ['unavailable'], summary: 'Latest host + container samples, 60-min sparklines, threshold alerts. "*" = { items: MetricsSummary[] }.' }),
  metricsSeries: ep<void, MetricsSeriesResponse, { subject: string; range?: MetricsRange; metrics?: string }>()({ method: 'GET', path: `${P}/metrics/series`, auth: S, perm: null, body: 'none', res: 'json', errors: ['invalid'], summary: 'One subject over 1h (15 s raw) / 24h (1 min) / 7d (15 min).' }),
  // volumes
  toolsVolumes: ep<void, ListResponse<ToolsVolume>>()({ method: 'GET', path: `${P}/volumes`, auth: S, perm: null, body: 'none', res: 'json', errors: [], summary: 'Declared site volumes with host path, size totals and users.' }),
  volumeTree: ep<void, VolumeTreeResponse, { path?: string }>()({ method: 'GET', path: `${P}/volumes/{volumeSlug}/tree`, auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'invalid'], summary: 'One directory level (read-only; never follows symlinks out of the volume).' }),
  volumeFile: ep<void, VolumeFileResponse, { path: string }>()({ method: 'GET', path: `${P}/volumes/{volumeSlug}/file`, auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'invalid'], summary: 'Text of a file ≤ 1 MiB (binary / bigger → text null).' }),
  volumeDownloadTicket: ep<void, DownloadTicketResponse, { path?: string }>()({ method: 'POST', path: `${P}/volumes/{volumeSlug}/download`, auth: SA, perm: null, body: 'none', res: 'json', errors: ['not-found', 'invalid', 'rate-limited'], activities: ['volume.downloaded'], summary: 'Single-use link: a file (path = file) or a streamed tar.gz (path = dir or empty = whole volume).' }),
  writeVolumeFile: ep<VolumeWriteFileRequest, VolumeWriteResponse>()({ method: 'POST', path: `${P}/volumes/{volumeSlug}/file`, auth: SA, perm: null, body: 'json', res: 'json', errors: ['not-found', 'invalid', 'conflict', 'locked', 'too-large', 'rate-limited'], activities: ['volume.file-written'], summary: '§21 #96: create / replace a text file ≤ 1 MiB (conflict when it changed since baseMtime).' }),
  uploadVolumeFile: ep<void, VolumeWriteResponse, { path: string; overwrite?: boolean }>()({ method: 'POST', path: `${P}/volumes/{volumeSlug}/upload`, auth: SA, perm: null, body: 'raw', res: 'json', errors: ['not-found', 'invalid', 'conflict', 'locked', 'too-large', 'rate-limited'], activities: ['volume.file-uploaded'], summary: '§21 #96: upload one file (≤ 2 GiB) to path (missing parent folders are created; conflict when it exists without overwrite).' }),
  mkdirVolume: ep<VolumeMkdirRequest, VolumeWriteResponse>()({ method: 'POST', path: `${P}/volumes/{volumeSlug}/mkdir`, auth: SA, perm: null, body: 'json', res: 'json', errors: ['not-found', 'invalid', 'conflict', 'locked', 'rate-limited'], activities: ['volume.file-folder-created'], summary: '§21 #96: create a folder (and its parents).' }),
  moveVolumeEntry: ep<VolumeMoveRequest, VolumeWriteResponse>()({ method: 'POST', path: `${P}/volumes/{volumeSlug}/move`, auth: SA, perm: null, body: 'json', res: 'json', errors: ['not-found', 'invalid', 'conflict', 'locked', 'rate-limited'], activities: ['volume.file-moved'], summary: '§21 #96: rename / move inside the volume.' }),
  deleteVolumeEntries: ep<VolumeDeleteRequest, VolumeDeleteResponse>()({ method: 'POST', path: `${P}/volumes/{volumeSlug}/delete`, auth: SA, perm: null, body: 'json', res: 'json', errors: ['not-found', 'invalid', 'locked', 'rate-limited'], activities: ['volume.file-deleted'], summary: '§21 #96: delete files / folders (confirm "DELETE"; the volume root itself: use Empty).' }),
  // network
  netcheck: ep<NetcheckRequest, NetcheckResult>()({ method: 'POST', path: `${P}/netcheck`, auth: SA, perm: null, body: 'json', res: 'json', errors: ['invalid', 'not-found', 'forbidden', 'rate-limited'], activities: ['tool.netcheck'], summary: 'DNS / TCP / HTTP GET / ping from the host or from inside a container.' }),
  // docker
  dockerImages: ep<void, DockerImagesResponse>()({ method: 'GET', path: `${P}/docker/images`, auth: S, perm: null, body: 'none', res: 'json', errors: ['unavailable'], summary: 'Images with users + docker system df. "*" = { items: DockerImagesResponse[] }.' }),
  pruneImages: ep<PruneImagesRequest, PruneImagesResponse>()({ method: 'POST', path: `${P}/docker/prune-images`, auth: SA, perm: null, body: 'json', res: 'json', errors: ['invalid', 'locked', 'rate-limited', 'unavailable'], activities: ['docker.images-pruned'], summary: 'Remove images no container uses, except the images of the head revision (typed "PRUNE").' }),
} as const;

export type ToolsClient = Client<typeof TOOLS_API>;

export function createToolsClient(baseUrl: string, getActorToken: () => string | null, init?: { fetch?: typeof fetch }): ToolsClient {
  return createClient(
    TOOLS_API,
    baseUrl,
    ({ endpoint }) => {
      if (endpoint.auth !== 'session+actor') return null;
      const t = getActorToken();
      return t ? `Actor ${t}` : null;
    },
    { fetch: init?.fetch, credentials: 'same-origin' },
  );
}

/** Thresholds (GUI badges only, §21 #89 c). */
export const METRIC_THRESHOLDS = { cpuPct: 85, memPct: 90, diskPct: 90, sustainSec: 300 } as const;
/** Terminal limits (§21 #89 b). */
export const TERMINAL_LIMITS = { maxSessions: 4, idleTimeoutSec: 1800, transcriptDays: 7, ticketSec: 60 } as const;
/** Host-shell confirmation: the operator types the site slug. */
export function hostShellConfirmOk(siteSlug: string, typed: string): boolean {
  return typed.trim() === siteSlug;
}
