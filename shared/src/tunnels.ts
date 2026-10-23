/**
 * §21 #125 port tunnels — a port of a site reachable at `<cloud host>:<public port>` (VM cloud only, §21 #108).
 *
 * Different from the remote agent GUI (§21 #118, request by request, under the console's own origin): a tunnel carries
 * raw TCP, so anything that speaks TCP works through it (a website, an API, a database client). The cloud listens on a
 * public port taken from `TUNNEL_FREE_PORTS` (e.g. `2000-2100,2400-2500,3000`; the ports are published from the cloud
 * container to its VM). The cloud never calls a site: an agent long-polls `agentPortTunnelNext` for new connections, dials
 * the target and moves the bytes with `agentPortTunnelRecv` / `agentPortTunnelSend` (outbound HTTPS only, like everything else).
 *
 * Targets: a container port (by port name), the site's edge (http or https port) or any port on the site's server.
 * Tunnels are added / removed in the cloud console; each one is OFF on its site until someone switches it on in that
 * site's agent GUI (switching on is refused through the cloud: the site decides). The agent GUI tunnel (the remote GUI)
 * is the default one per site, on by default, switched off / on on the site as before (§21 #118).
 *
 * A site that cannot reach the cloud is served through a site that can: the connected agent dials the target site's
 * agent over the mesh (`/_opm/peer/tunnel/{tunnelId}`, an HTTP upgrade) and that site checks its own switch.
 */
import { ep } from './http.js';
import type { Slug, Ts } from './types.js';

export type PortTunnelTarget =
  /** a port of a container of the site (the port name of its image, as edge routes use) */
  | { kind: 'container'; containerSlug: Slug; portName: string }
  /** the site's edge: its http (default 80) or https (default 443) port */
  | { kind: 'edge'; scheme: 'http' | 'https' }
  /** a TCP port on the site's server (127.0.0.1) */
  | { kind: 'host'; port: number };

export interface PortTunnel {
  tunnelId: string;
  siteSlug: Slug;
  target: PortTunnelTarget;
  /** the port of the cloud host that carries the tunnel */
  publicPort: number;
  label: string | null;
  createdAt: Ts;
  createdBy: string; //  email
}

/** the state the console shows (in memory of the cloud process: empty right after a restart until the agents poll) */
export interface PortTunnelStatus extends PortTunnel {
  /** `<cloud host>:<publicPort>` */
  address: string;
  listening: boolean;
  listenError: string | null;
  /** switched on on its site; null = the site did not tell yet (not connected to the cloud, directly or through a peer) */
  enabledOnSite: boolean | null;
  /** the site polls the cloud itself (direct) or is served through a peer that reaches it (via) */
  route: { kind: 'direct' } | { kind: 'via'; siteSlug: Slug } | null;
  openConnections: number;
  lastConnectionAt: Ts | null;
  lastError: string | null;
}

/** one default agent GUI tunnel per site (the remote GUI): cannot be removed */
export interface AgentTunnelStatus {
  siteSlug: Slug;
  /** the site polls the cloud for tunnels */
  connected: boolean;
  /** the remote GUI switch of the site; null = unknown */
  enabledOnSite: boolean | null;
}

export interface PortTunnelsResponse {
  /** tunnels work on this cloud: the single-process VM cloud with TUNNEL_FREE_PORTS set */
  available: boolean;
  /** why not, when available is false */
  reason: string | null;
  /** the cloud host the tunnels listen on (from the public URL) */
  host: string;
  /** ports of TUNNEL_FREE_PORTS not used by any tunnel (of any deployment) */
  freePorts: number[];
  agents: AgentTunnelStatus[];
  items: PortTunnelStatus[];
}

export interface CreatePortTunnelRequest {
  siteSlug: Slug;
  target: PortTunnelTarget;
  publicPort: number;
  label?: string | null;
}

// ───────────────────────── agent ⇄ cloud

/** CloudView.tunnels (every tunnel of the deployment; each agent switches its own on / off) */
export interface PortTunnelCloudItem {
  tunnelId: string;
  siteSlug: Slug;
  target: PortTunnelTarget;
  publicPort: number;
  label: string | null;
  createdAt: Ts;
  createdBy: string;
}
/** a new connection for the polling agent: its own site's tunnel, or one of a peer it reaches (siteSlug) */
export interface PortTunnelOpen {
  connId: string;
  tunnelId: string;
  siteSlug: Slug;
  target: PortTunnelTarget;
}
export interface PortTunnelNextResponse {
  opens: PortTunnelOpen[];
}
export interface PortTunnelRecvResponse {
  /** bytes from the client (base64) */
  dataB64: string;
  /** the client closed (after dataB64) */
  closed: boolean;
}
export interface PortTunnelSendRequest {
  /** bytes for the client (base64, ≤ PORT_TUNNEL_LIMITS.chunkMax decoded) */
  dataB64: string;
  /** the target closed: end the client connection after dataB64 */
  close?: boolean;
  /** why the connection could not be made / ended (shown in the console) */
  error?: string | null;
}

export const PORT_TUNNEL_LIMITS = {
  /** max bytes of one send / recv chunk */
  chunkMax: 1024 * 1024,
  /** bytes the cloud buffers from a client before it stops reading */
  bufferMax: 4 * 1024 * 1024,
  /** a new connection waits this long for an agent to take it */
  openTimeoutSec: 20,
  /** a connection whose agent stopped polling is closed after this */
  idleAgentSec: 75,
  labelMax: 100,
} as const;
/** an agent that polled within this many seconds counts as connected */
export const PORT_TUNNEL_CONNECTED_SEC = 45;
export const PORT_TUNNEL_MIN_AGENT = '0.21.0';

/**
 * TUNNEL_FREE_PORTS → sorted unique ports: comma separated, each a port or a range `a-b` (1024–65535).
 * Throws on a malformed entry.
 */
export function parseTunnelPorts(spec: string | null | undefined): number[] {
  const out = new Set<number>();
  for (const raw of (spec ?? '').split(',')) {
    const e = raw.trim();
    if (!e) continue;
    const m = /^(\d{1,5})(?:\s*-\s*(\d{1,5}))?$/.exec(e);
    if (!m) throw new Error(`TUNNEL_FREE_PORTS: "${e}" is not a port or a range a-b`);
    const a = Number(m[1]);
    const b = m[2] === undefined ? a : Number(m[2]);
    if (a < 1024 || b > 65535 || a > b) throw new Error(`TUNNEL_FREE_PORTS: "${e}" must be within 1024-65535 (low-high)`);
    if (b - a > 20000) throw new Error(`TUNNEL_FREE_PORTS: "${e}" is too large a range`);
    for (let p = a; p <= b; p++) out.add(p);
  }
  return [...out].sort((x, y) => x - y);
}

export function portTunnelTargetText(t: PortTunnelTarget): string {
  if (t.kind === 'container') return `${t.containerSlug} · ${t.portName}`;
  if (t.kind === 'edge') return `edge ${t.scheme}`;
  return `server port ${t.port}`;
}

/** the problem with a tunnel request, or null */
export function validatePortTunnel(r: CreatePortTunnelRequest): string | null {
  if (!r || typeof r !== 'object') return 'a body is required';
  if (typeof r.siteSlug !== 'string' || !/^[a-z0-9][a-z0-9-]{0,62}$/.test(r.siteSlug)) return 'siteSlug is not a site slug';
  if (!Number.isInteger(r.publicPort) || r.publicPort < 1024 || r.publicPort > 65535) return 'publicPort must be a port 1024–65535';
  if (r.label != null && (typeof r.label !== 'string' || r.label.length > PORT_TUNNEL_LIMITS.labelMax)) return `label is at most ${PORT_TUNNEL_LIMITS.labelMax} characters`;
  const t = r.target as PortTunnelTarget | undefined;
  if (!t || typeof t !== 'object') return 'target is required';
  switch (t.kind) {
    case 'container':
      if (typeof t.containerSlug !== 'string' || !/^[a-z0-9][a-z0-9-]{0,62}$/.test(t.containerSlug)) return 'target.containerSlug is not a container slug';
      if (typeof t.portName !== 'string' || !t.portName || t.portName.length > 64) return 'target.portName is required';
      return null;
    case 'edge':
      return t.scheme === 'http' || t.scheme === 'https' ? null : 'target.scheme must be http or https';
    case 'host':
      return Number.isInteger(t.port) && t.port >= 1 && t.port <= 65535 ? null : 'target.port must be a port 1–65535';
    default:
      return 'target.kind must be container, edge or host';
  }
}

const H = 'human' as const;
export const PORT_TUNNELS_CLOUD_API = {
  listPortTunnels: ep<void, PortTunnelsResponse>()({ method: 'GET', path: '/v1/deployments/{deploymentId}/tunnels', auth: H, perm: 'deployment.config.read', body: 'none', res: 'json', errors: ['not-found'], summary: '§21 #125: the agent GUI tunnel of every site + the port tunnels of the deployment with their live state, and the free public ports.' }),
  createPortTunnel: ep<CreatePortTunnelRequest, PortTunnelStatus>()({ method: 'POST', path: '/v1/deployments/{deploymentId}/tunnels', auth: H, perm: 'deployment.config.publish', body: 'json', res: 'json', ok: 201, errors: ['not-found', 'invalid', 'conflict', 'unavailable'], activities: ['tunnel.created'], summary: '§21 #125: add a port tunnel (off on its site until switched on there). conflict: the public port is taken; unavailable: not a VM cloud / no TUNNEL_FREE_PORTS.' }),
  deletePortTunnel: ep<void, { deleted: true }>()({ method: 'DELETE', path: '/v1/deployments/{deploymentId}/tunnels/{tunnelId}', auth: H, perm: 'deployment.config.publish', body: 'none', res: 'json', errors: ['not-found'], activities: ['tunnel.deleted'], summary: '§21 #125: remove a port tunnel: stops listening and closes its connections.' }),
  agentPortTunnelNext: ep<void, PortTunnelNextResponse, { wait?: number; enabled?: string; remoteGui?: string; reach?: string }>()({ method: 'GET', path: '/v1/agent/tunnels/next', auth: 'agent', perm: 'site key', body: 'none', res: 'json', errors: [], summary: '§21 #125: long-poll (wait ≤ 30 s) for new tunnel connections. enabled = the tunnel ids switched on on this site (comma separated), remoteGui = 1|0, reach = peers this agent reaches (it may serve their tunnels; each peer reports its own switches through its listing).' }),
  agentPortTunnelRecv: ep<void, PortTunnelRecvResponse, { wait?: number }>()({ method: 'GET', path: '/v1/agent/tunnels/conn/{connId}/recv', auth: 'agent', perm: 'site key (the agent the connection was handed to)', body: 'none', res: 'json', errors: [], summary: '§21 #125: long-poll (wait ≤ 30 s) for bytes from the client; closed = the client left (or the connection is unknown).' }),
  agentPortTunnelSend: ep<PortTunnelSendRequest, { accepted: boolean }>()({ method: 'POST', path: '/v1/agent/tunnels/conn/{connId}/send', auth: 'agent', perm: 'site key (the agent the connection was handed to)', body: 'json', res: 'json', errors: ['too-large'], summary: '§21 #125: bytes from the target to the client (answers once written); close ends the client connection. accepted=false: the client is gone.' }),
} as const;

/** agent GUI: the tunnels of a site and their switches (any site: forwarded over the mesh) */
export interface SitePortTunnel extends PortTunnelCloudItem {
  enabled: boolean;
  openConnections: number;
  lastConnectionAt: Ts | null;
  lastError: string | null;
}
export interface SitePortTunnelsResponse {
  siteSlug: Slug;
  /** the cloud supports tunnels (its view lists them); false on a firebase cloud or before the first view */
  cloudTunnels: boolean;
  /** the agent GUI tunnel (the remote GUI switch) */
  remoteGui: boolean;
  items: SitePortTunnel[];
}
export interface SetPortTunnelRequest {
  enabled: boolean;
}
export const PORT_TUNNELS_AGENT_API = {
  sitePortTunnels: ep<void, SitePortTunnelsResponse>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/tunnels', auth: 'session', perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: '§21 #125: the tunnels of a site (from the cloud) with their switches.' }),
  siteSetPortTunnel: ep<SetPortTunnelRequest, SitePortTunnel>()({ method: 'PUT', path: '/_opm/api/sites/{siteSlug}/tunnels/{tunnelId}', auth: 'session+actor', perm: null, body: 'json', res: 'json', errors: ['not-found', 'invalid', 'forbidden', 'unavailable'], activities: ['tunnel.enabled', 'tunnel.disabled'], summary: '§21 #125: switch a tunnel on / off on its site. Switching ON through the cloud (remote GUI) is refused: the site decides.' }),
} as const;
