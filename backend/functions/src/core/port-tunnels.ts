/**
 * §21 #125 port tunnels — the in-memory hub of ONE cloud process (the VM cloud, §21 #108): a TCP listener per tunnel
 * on its public port, the agents' long-polls for new connections, and the byte queues of the open connections.
 *
 *   client ──TCP──▶ cloud :publicPort ──(queued open)──▶ agent long-poll (agentPortTunnelNext)
 *   agent dials the target (its own site, or a peer site over the mesh), then moves the bytes:
 *     client → target: the agent long-polls agentPortTunnelRecv; target → client: the agent posts agentPortTunnelSend.
 *
 * The tunnels themselves are stored (deployments/{d}/portTunnels/{id} + tunnelPorts/{port} for the global port
 * uniqueness); the live state is not: a restart drops the connections and the agents' reports come back within a poll.
 */
import net from 'node:net';
import { randomBytes } from 'node:crypto';
import { PORT_TUNNEL_CONNECTED_SEC, PORT_TUNNEL_LIMITS, parseTunnelPorts, type AgentTunnelStatus, type PortTunnel, type PortTunnelOpen, type PortTunnelStatus } from '@opm/shared';
import { logger } from '../ports/index.js';

export type StoredPortTunnel = PortTunnel & { deploymentId: string };

interface SiteState {
  lastPoll: number;
  enabled: Set<string>;
  remoteGui: boolean | null;
  reach: string[];
  queue: PortTunnelOpen[];
  waiters: ((opens: PortTunnelOpen[]) => void)[];
}
interface Conn {
  connId: string;
  deploymentId: string;
  tunnelId: string;
  /** the site whose agent carries it */
  agentSite: string;
  socket: net.Socket;
  inbuf: Buffer[];
  inLen: number;
  clientClosed: boolean;
  picked: boolean;
  recvWaiter: (() => void) | null;
  lastAgentAt: number;
  openTimer: NodeJS.Timeout;
}
interface Listener {
  tunnel: StoredPortTunnel;
  server: net.Server | null;
  listening: boolean;
  error: string | null;
  open: Set<string>;
  lastConnectionAt: string | null;
  lastError: string | null;
}

const sites = new Map<string, SiteState>(); //  dep/site
const conns = new Map<string, Conn>();
const listeners = new Map<number, Listener>(); // publicPort → listener (every tunnel of every deployment)
const key = (d: string, s: string) => `${d}/${s}`;
const id = () => randomBytes(18).toString('base64url');
let sweeper: NodeJS.Timeout | null = null;
let enabledHub = false;

/** TUNNEL_FREE_PORTS (or OPM_TUNNEL_FREE_PORTS); [] when unset or malformed (logged) */
export function configuredPorts(): number[] {
  const spec = process.env.TUNNEL_FREE_PORTS ?? process.env.OPM_TUNNEL_FREE_PORTS ?? '';
  try {
    return parseTunnelPorts(spec);
  } catch (e) {
    logger.error('TUNNEL_FREE_PORTS is not valid', { error: (e as Error).message });
    return [];
  }
}
/** the hub runs (the VM cloud started it) */
export const hubRunning = () => enabledHub;
/** free public ports: configured and not taken by any tunnel */
export const freePorts = () => configuredPorts().filter((p) => !listeners.has(p));
export const portTaken = (p: number) => listeners.has(p);

function state(d: string, s: string): SiteState {
  let st = sites.get(key(d, s));
  if (!st) sites.set(key(d, s), (st = { lastPoll: 0, enabled: new Set(), remoteGui: null, reach: [], queue: [], waiters: [] }));
  return st;
}
const isConnected = (st: SiteState | undefined) => !!st && Date.now() - st.lastPoll < PORT_TUNNEL_CONNECTED_SEC * 1000;

/** the route for a connection to a tunnel of site s: its own agent, else a connected agent that reaches it */
function routeOf(d: string, s: string): { kind: 'direct' } | { kind: 'via'; siteSlug: string } | null {
  if (isConnected(sites.get(key(d, s)))) return { kind: 'direct' };
  for (const [k, st] of sites) {
    if (!k.startsWith(`${d}/`) || !isConnected(st) || !st.reach.includes(s)) continue;
    return { kind: 'via', siteSlug: k.slice(d.length + 1) };
  }
  return null;
}

// ───────────────────────── listeners

/** start the hub with the stored tunnels (server start, VM cloud only) */
export function startPortTunnels(tunnels: StoredPortTunnel[]) {
  enabledHub = true;
  for (const t of tunnels) listen(t);
  sweeper ??= setInterval(sweep, 15_000);
  sweeper.unref?.();
}
export function stopPortTunnels() {
  for (const p of [...listeners.keys()]) unlisten(p);
  if (sweeper) clearInterval(sweeper);
  sweeper = null;
  enabledHub = false;
}

export function listen(t: StoredPortTunnel) {
  unlisten(t.publicPort);
  const l: Listener = { tunnel: t, server: null, listening: false, error: null, open: new Set(), lastConnectionAt: null, lastError: null };
  listeners.set(t.publicPort, l);
  if (!configuredPorts().includes(t.publicPort)) {
    l.error = `port ${t.publicPort} is not in TUNNEL_FREE_PORTS any more`;
    return;
  }
  const srv = net.createServer({ pauseOnConnect: true }, (sock) => onClient(l, sock));
  srv.on('error', (e) => {
    l.listening = false;
    l.error = (e as Error).message;
    logger.error('port tunnel listen failed', { port: t.publicPort, tunnelId: t.tunnelId, error: l.error });
  });
  srv.listen(t.publicPort, process.env.OPM_TUNNEL_HOST || '0.0.0.0', () => {
    l.listening = true;
    l.error = null;
  });
  l.server = srv;
}

export function unlisten(port: number) {
  const l = listeners.get(port);
  if (!l) return;
  listeners.delete(port);
  l.server?.close();
  for (const cid of l.open) conns.get(cid)?.socket.destroy();
}

function onClient(l: Listener, sock: net.Socket) {
  const t = l.tunnel;
  l.lastConnectionAt = new Date().toISOString();
  const route = routeOf(t.deploymentId, t.siteSlug);
  const refuse = (why: string) => {
    l.lastError = why;
    sock.destroy();
  };
  if (!route) return refuse(`site ${t.siteSlug} is not connected to the cloud (nor through a site that reaches it)`);
  if (route.kind === 'direct' && !state(t.deploymentId, t.siteSlug).enabled.has(t.tunnelId)) return refuse(`the tunnel is switched off on site ${t.siteSlug} (switch it on in that site's agent GUI)`);
  const agentSite = route.kind === 'direct' ? t.siteSlug : route.siteSlug;
  const c: Conn = {
    connId: id(),
    deploymentId: t.deploymentId,
    tunnelId: t.tunnelId,
    agentSite,
    socket: sock,
    inbuf: [],
    inLen: 0,
    clientClosed: false,
    picked: false,
    recvWaiter: null,
    lastAgentAt: Date.now(),
    openTimer: setTimeout(() => {
      if (!c.picked) {
        l.lastError = `site ${agentSite} did not take the connection within ${PORT_TUNNEL_LIMITS.openTimeoutSec} s`;
        sock.destroy();
      }
    }, PORT_TUNNEL_LIMITS.openTimeoutSec * 1000),
  };
  c.openTimer.unref?.();
  conns.set(c.connId, c);
  l.open.add(c.connId);
  sock.setNoDelay(true);
  sock.on('data', (d: Buffer) => {
    c.inbuf.push(d);
    c.inLen += d.length;
    if (c.inLen >= PORT_TUNNEL_LIMITS.bufferMax) sock.pause();
    wakeRecv(c);
  });
  sock.on('end', () => {
    c.clientClosed = true;
    wakeRecv(c);
  });
  sock.on('error', () => undefined);
  sock.on('close', () => {
    c.clientClosed = true;
    clearTimeout(c.openTimer);
    l.open.delete(c.connId);
    wakeRecv(c);
    setTimeout(() => conns.delete(c.connId), 60_000).unref?.();
  });
  const st = state(t.deploymentId, agentSite);
  st.queue.push({ connId: c.connId, tunnelId: t.tunnelId, siteSlug: t.siteSlug, target: t.target });
  const w = st.waiters.shift();
  if (w) w(st.queue.splice(0));
}

function wakeRecv(c: Conn) {
  const w = c.recvWaiter;
  c.recvWaiter = null;
  w?.();
}

function sweep() {
  const now = Date.now();
  for (const c of conns.values()) if (c.picked && !c.socket.destroyed && now - c.lastAgentAt > PORT_TUNNEL_LIMITS.idleAgentSec * 1000) c.socket.destroy();
}

// ───────────────────────── agent side

/** an agent long-poll: report its switches + the peers it reaches; answer the queued connections or wait */
export function next(d: string, s: string, report: { enabled: string[]; remoteGui: boolean | null; reach: string[] }, waitMs: number, signal?: AbortSignal): Promise<PortTunnelOpen[]> {
  const st = state(d, s);
  st.lastPoll = Date.now();
  st.enabled = new Set(report.enabled);
  st.remoteGui = report.remoteGui;
  st.reach = report.reach.filter((x) => x !== s);
  if (st.queue.length || waitMs <= 0) return Promise.resolve(st.queue.splice(0));
  return new Promise((resolve) => {
    const done = (opens: PortTunnelOpen[]) => {
      clearTimeout(t);
      st.lastPoll = Date.now();
      resolve(opens);
    };
    const t = setTimeout(() => {
      st.waiters = st.waiters.filter((x) => x !== done);
      st.lastPoll = Date.now();
      resolve([]);
    }, waitMs);
    t.unref?.();
    st.waiters.push(done);
    signal?.addEventListener('abort', () => {
      st.waiters = st.waiters.filter((x) => x !== done);
      clearTimeout(t);
      resolve([]);
    });
  });
}

function connOf(connId: string, d: string, s: string): Conn | null {
  const c = conns.get(connId);
  return c && c.deploymentId === d && c.agentSite === s ? c : null;
}

/** bytes from the client (≤ chunkMax); closed once the client left and everything was handed over */
export async function recv(connId: string, d: string, s: string, waitMs: number, signal?: AbortSignal): Promise<{ dataB64: string; closed: boolean }> {
  const c = connOf(connId, d, s);
  if (!c) return { dataB64: '', closed: true };
  c.picked = true;
  c.lastAgentAt = Date.now();
  clearTimeout(c.openTimer);
  if (c.socket.isPaused() && c.inLen < PORT_TUNNEL_LIMITS.bufferMax) c.socket.resume();
  if (!c.inLen && !c.clientClosed && waitMs > 0) {
    await new Promise<void>((resolve) => {
      const t = setTimeout(() => {
        if (c.recvWaiter === done) c.recvWaiter = null;
        resolve();
      }, waitMs);
      t.unref?.();
      const done = () => {
        clearTimeout(t);
        resolve();
      };
      c.recvWaiter = done;
      signal?.addEventListener('abort', () => {
        if (c.recvWaiter === done) c.recvWaiter = null;
        clearTimeout(t);
        resolve();
      });
    });
  }
  c.lastAgentAt = Date.now();
  const all = Buffer.concat(c.inbuf);
  const out = all.subarray(0, PORT_TUNNEL_LIMITS.chunkMax);
  const rest = all.subarray(out.length);
  c.inbuf = rest.length ? [Buffer.from(rest)] : [];
  c.inLen = rest.length;
  if (c.socket.isPaused() && c.inLen < PORT_TUNNEL_LIMITS.bufferMax && !c.socket.destroyed) c.socket.resume();
  return { dataB64: out.toString('base64'), closed: c.clientClosed && c.inLen === 0 };
}

/** bytes for the client (resolves once written); close ends the client connection */
export async function send(connId: string, d: string, s: string, data: Buffer, close: boolean, error: string | null): Promise<boolean> {
  const c = connOf(connId, d, s);
  if (!c) return false;
  c.picked = true;
  c.lastAgentAt = Date.now();
  clearTimeout(c.openTimer);
  if (error) {
    const l = listeners.get(listenerPortOf(c));
    if (l) l.lastError = error;
  }
  if (c.socket.destroyed) return false;
  if (data.length && !c.socket.write(data)) await new Promise<void>((r) => c.socket.once('drain', () => r()).once('close', () => r()));
  if (close) c.socket.end();
  return !c.socket.destroyed;
}
function listenerPortOf(c: Conn): number {
  for (const [p, l] of listeners) if (l.tunnel.tunnelId === c.tunnelId) return p;
  return -1;
}

// ───────────────────────── console view

export function tunnelStatus(t: StoredPortTunnel, host: string): PortTunnelStatus {
  const l = listeners.get(t.publicPort);
  const own = l && l.tunnel.tunnelId === t.tunnelId ? l : null;
  const st = sites.get(key(t.deploymentId, t.siteSlug));
  const { deploymentId: _d, ...rest } = t;
  return {
    ...rest,
    address: `${host}:${t.publicPort}`,
    listening: !!own?.listening,
    listenError: own ? own.error : 'not started',
    enabledOnSite: isConnected(st) ? st!.enabled.has(t.tunnelId) : null,
    route: routeOf(t.deploymentId, t.siteSlug),
    openConnections: own?.open.size ?? 0,
    lastConnectionAt: own?.lastConnectionAt ?? null,
    lastError: own?.lastError ?? null,
  };
}
export function agentTunnels(d: string, siteSlugs: string[]): AgentTunnelStatus[] {
  return siteSlugs.map((s) => {
    const st = sites.get(key(d, s));
    return { siteSlug: s, connected: isConnected(st), enabledOnSite: isConnected(st) ? st!.remoteGui : null };
  });
}

/** test hook */
export function _resetPortTunnels() {
  stopPortTunnels();
  for (const c of conns.values()) c.socket.destroy();
  conns.clear();
  sites.clear();
}
