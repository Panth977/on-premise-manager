/**
 * §21 #118 remote agent GUI: a site's agent GUI opened from the cloud console at
 *   <cloud>/app/deployments/{deploymentId}/remote/{siteSlug}/_opm/…
 * The cloud never calls a site. While a person has a remote session open, the cloud view lists the site
 * (`CloudView.remoteGui`); the agent then long-polls `agentRemoteGuiNext` for the browser's requests, runs each one
 * against its own GUI handler as that cloud person (an agent session + actor token made for them: no access key, no
 * email prompt) and posts the response with `agentRemoteGuiResult` — the same store-and-forward idea as the browser
 * internet tunnel (§9.10), the other way round. The cloud serves the answer with every `/_opm` path re-prefixed
 * (`rewriteRemoteText`), so the unchanged agent GUI build runs under the remote prefix.
 * WebSockets (terminal, MQTT client) are relayed frame by frame (§21 #118 g); event streams (log follow) are not — 501.
 */
import { ep } from './http.js';
import type { Slug, Ts } from './types.js';

export const REMOTE_GUI_MIN_AGENT = '0.19.0';
/** a remote session lives this long after its last request (cloud cookie + the agent's session for that person) */
export const REMOTE_GUI_SESSION_SEC = 8 * 3600;
/** how long the cloud keeps a browser request waiting for the site */
export const REMOTE_GUI_REQUEST_TIMEOUT_SEC = 60;
/** a site counts as connected while it long-polled within this window */
export const REMOTE_GUI_CONNECTED_SEC = 40;
/** limits of one forwarded request / response (bodies travel whole) */
export const REMOTE_GUI_LIMITS = { requestBodyMax: 16 * 1024 * 1024, responseBodyMax: 64 * 1024 * 1024, batchMax: 8 } as const;

/** the path under the cloud origin where a site's agent GUI is served */
export const remoteGuiPrefix = (deploymentId: string, siteSlug: string) => `/app/deployments/${deploymentId}/remote/${siteSlug}`;
const PREFIX_RE = /^\/app\/deployments\/([A-Za-z0-9_-]{1,64})\/remote\/([a-z][a-z0-9-]{0,39})(\/.*)?$/;
/** parse a cloud path into the remote target + the agent path (`/_opm/…`; the bare prefix maps to `/_opm/`) */
export function parseRemoteGuiPath(path: string): { deploymentId: string; siteSlug: string; agentPath: string } | null {
  const m = PREFIX_RE.exec(path);
  if (!m) return null;
  const rest = m[3] && m[3] !== '/' ? m[3] : '/_opm/';
  return { deploymentId: m[1]!, siteSlug: m[2]!, agentPath: rest };
}

/** CloudView.remoteGui: sites with an open remote session (each agent serves only its own siteSlug) */
export interface RemoteGuiCloudItem {
  siteSlug: Slug;
  until: Ts;
}
/** the person a request runs as on the site (the cloud account that opened the session) */
export interface RemoteGuiActor {
  uid: string;
  email: string;
  name: string | null;
}
/** one browser request handed to the site */
export interface RemoteGuiRequest {
  reqId: string;
  /** the cloud session (the agent keeps one agent session per cloud session) */
  sessionId: string;
  method: string;
  /** the agent path incl. the query, always under /_opm */
  path: string;
  headers: Record<string, string>;
  bodyB64: string | null;
  actor: RemoteGuiActor;
  /** §21 #118 g: a WebSocket upgrade — the agent opens it against its own GUI port, answers 101 (or the refusal) as
   * the result, then relays frames through agentRemoteGuiWsRecv / agentRemoteGuiWsSend until either side closes */
  ws?: { streamId: string } | null;
}
/** one WebSocket message relayed through the cloud (close: code + reason in dataB64 as UTF-8) */
export interface RemoteGuiWsFrame {
  op: 'text' | 'binary' | 'close';
  dataB64: string;
  code?: number;
}
export interface RemoteGuiWsRecvResponse {
  frames: RemoteGuiWsFrame[];
  /** the browser side is gone: close the local socket */
  closed: boolean;
}
export interface RemoteGuiWsSendRequest {
  frames: RemoteGuiWsFrame[];
}
export interface RemoteGuiNextResponse {
  items: RemoteGuiRequest[];
}
/** POST /v1/agent/remote-gui/{reqId}/result: raw body; status + headers in X-OPM-Remote-Status / X-OPM-Remote-Headers (base64 JSON) */
export const REMOTE_GUI_STATUS_HEADER = 'x-opm-remote-status';
export const REMOTE_GUI_HEADERS_HEADER = 'x-opm-remote-headers';
export interface OpenRemoteGuiResponse {
  url: string;
  expiresAt: Ts;
  /** the site long-polled recently (else the page explains why it does not answer) */
  connected: boolean;
}
/** agent: GET / PUT /_opm/api/agent/remote-gui */
export interface RemoteGuiSetting {
  enabled: boolean;
  /** open remote sessions of this site right now (cloud persons) */
  sessions?: { email: string; since: Ts }[];
}

// ───────────────────────── response rewriting (cloud side)

const TEXT_TYPE = /^(text\/html|text\/css|text\/javascript|application\/javascript|application\/x-javascript|application\/manifest\+json)\b/i;
export const isRewritableType = (contentType: string | undefined | null) => !!contentType && TEXT_TYPE.test(contentType);
/**
 * Re-prefix every absolute `/_opm` path of the agent GUI shell (HTML, the JS bundle, CSS): a `/_opm` right after a quote,
 * backtick, `(`, `=` or whitespace and followed by `/`, a quote, backtick, `?`, `#` or `)` becomes `<prefix>/_opm`.
 * Escaped forms (`\/_opm`, regex literals) and already prefixed paths are left alone.
 */
export function rewriteRemoteText(text: string, prefix: string): string {
  return text.replace(/(["'`(=\s])\/_opm(?=[/"'`?#)])/g, `$1${prefix}/_opm`);
}
/** a redirect Location under /_opm → under the prefix */
export function rewriteRemoteLocation(loc: string, prefix: string): string {
  return loc.startsWith('/_opm') ? prefix + loc : loc;
}

/** cloud console API (human) + the agent's side of the remote channel (site key) */
export const REMOTE_GUI_CLOUD_API = {
  openRemoteGui: ep<void, OpenRemoteGuiResponse>()({ method: 'POST', path: '/v1/deployments/{deploymentId}/remote-gui/{siteSlug}/session', auth: 'human', perm: 'deployment.config.publish', body: 'none', res: 'json', errors: ['not-found', 'forbidden', 'unavailable'], activities: ['remote-gui.opened'], summary: '§21 #118: open the site\'s agent GUI through the cloud: sets the remote session cookie (Path = the remote prefix, 8 h sliding) and lists the site in the cloud view so its agent starts long-polling. unavailable: this cloud cannot hold remote sessions (multi-instance).' }),
  agentRemoteGuiNext: ep<void, RemoteGuiNextResponse, { wait?: number }>()({ method: 'GET', path: '/v1/agent/remote-gui/next', auth: 'agent', perm: 'site key (requests of the signer site only)', body: 'none', res: 'json', errors: [], summary: '§21 #118: long-poll (wait ≤ 30 s) for browser requests of open remote sessions of the signer site (≤ 8 per answer).' }),
  agentRemoteGuiWsRecv: ep<void, RemoteGuiWsRecvResponse, { wait?: number }>()({ method: 'GET', path: '/v1/agent/remote-gui/ws/{streamId}/recv', auth: 'agent', perm: 'site key (the site of the stream)', body: 'none', res: 'json', errors: ['not-found'], summary: '§21 #118 g: long-poll (wait ≤ 30 s) for the browser\'s WebSocket messages of a relayed stream; closed = the browser left.' }),
  agentRemoteGuiWsSend: ep<RemoteGuiWsSendRequest, { accepted: boolean }>()({ method: 'POST', path: '/v1/agent/remote-gui/ws/{streamId}/send', auth: 'agent', perm: 'site key (the site of the stream)', body: 'json', res: 'json', errors: ['not-found', 'too-large'], summary: '§21 #118 g: WebSocket messages from the site to the browser (≤ 8 MiB per call); a close frame ends the stream.' }),
  agentRemoteGuiResult: ep<void, { accepted: boolean }>()({ method: 'POST', path: '/v1/agent/remote-gui/{reqId}/result', auth: 'agent', perm: 'site key (the site the request went to)', body: 'raw', res: 'json', errors: ['too-large'], summary: '§21 #118: the response to one forwarded request — raw body ≤ 64 MiB, status in X-OPM-Remote-Status, headers in X-OPM-Remote-Headers (base64 JSON). accepted=false: the browser gave up (timeout).' }),
} as const;

/** agent API additions (spread into AGENT_API) */
export const REMOTE_GUI_AGENT_API = {
  getRemoteGui: ep<void, RemoteGuiSetting>()({ method: 'GET', path: '/_opm/api/agent/remote-gui', auth: 'session', perm: null, body: 'none', res: 'json', errors: [], summary: '§21 #118: whether this site serves its GUI to the cloud (default on) + the open remote sessions.' }),
  putRemoteGui: ep<RemoteGuiSetting, RemoteGuiSetting>()({ method: 'PUT', path: '/_opm/api/agent/remote-gui', auth: 'session+actor', perm: null, body: 'json', res: 'json', errors: ['invalid', 'forbidden'], activities: ['agent.remote-gui-changed'], summary: '§21 #118: turn the remote GUI on / off (not from a remote session: switch it off on the site).' }),
  // §21 #121 the same per site (self in process, another site over the mesh): every site is managed from any site
  siteGetRemoteGui: ep<void, RemoteGuiSetting>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/agent/remote-gui', auth: 'session', perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: '§21 #121: getRemoteGui of any site (forwarded over the mesh).' }),
  sitePutRemoteGui: ep<RemoteGuiSetting, RemoteGuiSetting>()({ method: 'PUT', path: '/_opm/api/sites/{siteSlug}/agent/remote-gui', auth: 'session+actor', perm: null, body: 'json', res: 'json', errors: ['invalid', 'forbidden', 'not-found', 'unavailable'], activities: ['agent.remote-gui-changed'], summary: '§21 #121: putRemoteGui of any site; refused (403) when the person works through the cloud.' }),
} as const;

// ───────────────────────── §21 #119 storage + moving the agent home (agent GUI › Agent › Storage)

/** one filesystem OPM uses (several roles share one when they are on the same disk) */
export interface StorageFilesystem {
  path: string;
  totalBytes: number;
  freeBytes: number;
  usedBytes: number;
  /** the free space the agent never uses (min(5 % of the disk, 2 GiB)): downloads / snapshots below it are refused */
  floorBytes: number;
  roles: string[];
}
export interface MoveHomeState {
  moveId: string;
  status: 'prepared' | 'copying' | 'copied' | 'done' | 'failed';
  from: string;
  to: string;
  mode: 'rename' | 'copy';
  actor: string;
  startedAt: Ts;
  finishedAt: Ts | null;
  sizeBytes: number;
  files: number;
  stopped: string[];
  /** the old home kept after a copy (delete it to free that space) */
  oldCopy: string | null;
  error: string | null;
}
export interface StorageView {
  home: string;
  homeBytes: number | null;
  homeFiles: number;
  dockerRootDir: string | null;
  filesystems: StorageFilesystem[];
  move: MoveHomeState | null;
  canMove: boolean;
  cannotMove: string | null;
  measuredAt: Ts | null;
}
export interface MoveHomeRequest {
  to: string;
  typed?: string;
}
export interface MoveHomeCheck {
  ok: boolean;
  to: string;
  sameFilesystem: boolean;
  sizeBytes: number;
  targetFreeBytes: number;
  targetTotalBytes: number;
  errors: string[];
  warnings: string[];
  containers: string[];
}
export const STORAGE_AGENT_API = {
  agentStorage: ep<void, StorageView>()({ method: 'GET', path: '/_opm/api/agent/storage', auth: 'session', perm: null, body: 'none', res: 'json', errors: [], summary: '§21 #119: the disks OPM uses (agent home, Docker data, volumes with their own location): total / used / free, the home size, the current / last move.' }),
  checkMoveHome: ep<MoveHomeRequest, MoveHomeCheck>()({ method: 'POST', path: '/_opm/api/agent/move-home/check', auth: 'session', perm: null, body: 'json', res: 'json', errors: ['invalid'], summary: '§21 #119: can the agent home move there (absolute, empty, outside the home, enough free space, systemd, no update running)?' }),
  moveHome: ep<MoveHomeRequest, MoveHomeState>()({ method: 'POST', path: '/_opm/api/agent/move-home', auth: 'session+actor', perm: null, body: 'json', res: 'json', ok: 202, errors: ['invalid'], activities: ['agent.home-move-started'], summary: '§21 #119: typed MOVE — stops this site\'s containers and hands over to `opm-agent move-home` (the agent restarts from the new folder; containers are re-applied).' }),
  deleteOldHome: ep<MoveHomeRequest, MoveHomeState>()({ method: 'POST', path: '/_opm/api/agent/move-home/delete-old', auth: 'session+actor', perm: null, body: 'json', res: 'json', errors: ['invalid', 'not-found'], activities: ['agent.home-old-deleted'], summary: '§21 #119: typed DELETE — remove the old copy kept after a move.' }),
  // §21 #121 the same per site (self in process, another site over the mesh)
  siteAgentStorage: ep<void, StorageView>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/agent/storage', auth: 'session', perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: '§21 #121: agentStorage of any site.' }),
  siteCheckMoveHome: ep<MoveHomeRequest, MoveHomeCheck>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/agent/move-home/check', auth: 'session+actor', perm: null, body: 'json', res: 'json', errors: ['invalid', 'not-found', 'unavailable'], summary: '§21 #121: checkMoveHome on any site.' }),
  siteMoveHome: ep<MoveHomeRequest, MoveHomeState>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/agent/move-home', auth: 'session+actor', perm: null, body: 'json', res: 'json', ok: 202, errors: ['invalid', 'not-found', 'unavailable'], activities: ['agent.home-move-started'], summary: '§21 #121: moveHome on any site.' }),
  siteDeleteOldHome: ep<MoveHomeRequest, MoveHomeState>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/agent/move-home/delete-old', auth: 'session+actor', perm: null, body: 'json', res: 'json', errors: ['invalid', 'not-found', 'unavailable'], activities: ['agent.home-old-deleted'], summary: '§21 #121: deleteOldHome on any site.' }),
} as const;
