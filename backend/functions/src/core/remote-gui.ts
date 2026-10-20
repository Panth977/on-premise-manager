/**
 * §21 #118 remote agent GUI — the in-memory hub of ONE cloud process (the VM cloud, §21 #108): remote sessions (cookie
 * → person + site, sliding expiry), the queue of browser requests per site, the waiting long-polls of the agents and the
 * pending browser requests waiting for their result. Nothing is stored: a restart drops the sessions (the person opens
 * the GUI again from the console) and fails the requests in flight. The cloud never calls a site: agents long-poll.
 */
import { randomBytes } from 'node:crypto';
import { REMOTE_GUI_CONNECTED_SEC, REMOTE_GUI_LIMITS, REMOTE_GUI_REQUEST_TIMEOUT_SEC, REMOTE_GUI_SESSION_SEC, type RemoteGuiActor, type RemoteGuiCloudItem, type RemoteGuiRequest } from '@opm/shared';

export interface RemoteSession {
  sessionId: string;
  deploymentId: string;
  siteSlug: string;
  actor: RemoteGuiActor;
  createdAt: number;
  expiresAt: number;
}
export interface RemoteResult {
  status: number;
  headers: Record<string, string>;
  body: Buffer;
}
interface Pending {
  req: RemoteGuiRequest;
  deploymentId: string;
  siteSlug: string;
  handedOut: boolean;
  resolve: (r: RemoteResult) => void;
  timer: NodeJS.Timeout;
}
interface Waiter {
  resolve: (items: RemoteGuiRequest[]) => void;
  timer: NodeJS.Timeout;
}

const sessions = new Map<string, RemoteSession>();
const pending = new Map<string, Pending>(); // reqId → request
const queues = new Map<string, string[]>(); // dep/site → reqIds not handed out yet
const waiters = new Map<string, Waiter[]>(); // dep/site → agent long-polls
const lastPoll = new Map<string, number>(); // dep/site → ms
const key = (d: string, s: string) => `${d}/${s}`;
const id = () => randomBytes(18).toString('base64url');

/** a new remote session for a person (the cookie value is the session id) */
export function openSession(deploymentId: string, siteSlug: string, actor: RemoteGuiActor): RemoteSession {
  prune();
  const now = Date.now();
  const s: RemoteSession = { sessionId: id(), deploymentId, siteSlug, actor, createdAt: now, expiresAt: now + REMOTE_GUI_SESSION_SEC * 1000 };
  sessions.set(s.sessionId, s);
  return s;
}
/** the session of a cookie for this site (sliding expiry), else null */
export function touchSession(sessionId: string | undefined, deploymentId: string, siteSlug: string): RemoteSession | null {
  if (!sessionId) return null;
  const s = sessions.get(sessionId);
  if (!s || s.deploymentId !== deploymentId || s.siteSlug !== siteSlug) return null;
  if (s.expiresAt <= Date.now()) {
    sessions.delete(sessionId);
    return null;
  }
  s.expiresAt = Date.now() + REMOTE_GUI_SESSION_SEC * 1000;
  return s;
}
/** CloudView.remoteGui of a deployment: one item per site with an open session */
export function activeSites(deploymentId: string): RemoteGuiCloudItem[] {
  prune();
  const by = new Map<string, number>();
  for (const s of sessions.values()) if (s.deploymentId === deploymentId) by.set(s.siteSlug, Math.max(by.get(s.siteSlug) ?? 0, s.expiresAt));
  return [...by].sort(([a], [b]) => (a < b ? -1 : 1)).map(([siteSlug, until]) => ({ siteSlug, until: new Date(until).toISOString() }));
}
/** the site long-polled within REMOTE_GUI_CONNECTED_SEC */
export function connected(deploymentId: string, siteSlug: string): boolean {
  const t = lastPoll.get(key(deploymentId, siteSlug));
  return !!t && Date.now() - t < REMOTE_GUI_CONNECTED_SEC * 1000;
}

/** queue a browser request for the site; resolves with its result (504 on timeout) */
export function forward(deploymentId: string, siteSlug: string, req: Omit<RemoteGuiRequest, 'reqId'>, timeoutMs = REMOTE_GUI_REQUEST_TIMEOUT_SEC * 1000): Promise<RemoteResult> {
  const reqId = id();
  const full: RemoteGuiRequest = { reqId, ...req };
  return new Promise<RemoteResult>((resolve) => {
    const timer = setTimeout(() => {
      const p = pending.get(reqId);
      pending.delete(reqId);
      const q = queues.get(key(deploymentId, siteSlug));
      if (q) queues.set(key(deploymentId, siteSlug), q.filter((x) => x !== reqId));
      resolve(timeoutPage(siteSlug, !!p?.handedOut, connected(deploymentId, siteSlug)));
    }, timeoutMs);
    timer.unref?.();
    pending.set(reqId, { req: full, deploymentId, siteSlug, handedOut: false, resolve, timer });
    const k = key(deploymentId, siteSlug);
    queues.set(k, [...(queues.get(k) ?? []), reqId]);
    wake(k);
  });
}

function take(k: string): RemoteGuiRequest[] {
  const q = queues.get(k) ?? [];
  const out: RemoteGuiRequest[] = [];
  while (q.length && out.length < REMOTE_GUI_LIMITS.batchMax) {
    const p = pending.get(q.shift()!);
    if (!p) continue;
    p.handedOut = true;
    out.push(p.req);
  }
  queues.set(k, q);
  return out;
}
function wake(k: string) {
  const ws = waiters.get(k);
  if (!ws?.length) return;
  const w = ws.shift()!;
  clearTimeout(w.timer);
  w.resolve(take(k));
}

/** an agent long-poll: the queued requests at once, else wait ≤ waitMs for the next one */
export function next(deploymentId: string, siteSlug: string, waitMs: number, signal?: AbortSignal): Promise<RemoteGuiRequest[]> {
  const k = key(deploymentId, siteSlug);
  lastPoll.set(k, Date.now());
  const now = take(k);
  if (now.length || waitMs <= 0) return Promise.resolve(now);
  return new Promise((resolve) => {
    const w: Waiter = {
      resolve: (items) => {
        lastPoll.set(k, Date.now());
        resolve(items);
      },
      timer: setTimeout(() => {
        waiters.set(k, (waiters.get(k) ?? []).filter((x) => x !== w));
        lastPoll.set(k, Date.now());
        resolve([]);
      }, waitMs),
    };
    w.timer.unref?.();
    waiters.set(k, [...(waiters.get(k) ?? []), w]);
    signal?.addEventListener('abort', () => {
      clearTimeout(w.timer);
      waiters.set(k, (waiters.get(k) ?? []).filter((x) => x !== w));
      resolve([]);
    });
  });
}

/** the site's answer to a request it was handed (false: unknown / timed out / another site) */
export function deliver(deploymentId: string, siteSlug: string, reqId: string, r: RemoteResult): boolean {
  const p = pending.get(reqId);
  if (!p || p.deploymentId !== deploymentId || p.siteSlug !== siteSlug) return false;
  pending.delete(reqId);
  clearTimeout(p.timer);
  p.resolve(r);
  return true;
}

function prune() {
  const now = Date.now();
  for (const [k, s] of sessions) if (s.expiresAt <= now) sessions.delete(k);
}

const esc = (s: string) => s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
export function page(status: number, title: string, text: string): RemoteResult {
  const html = `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>${esc(title)}</title><style>body{font:15px/1.5 system-ui,sans-serif;max-width:640px;margin:12vh auto;padding:0 16px;color:#222}@media(prefers-color-scheme:dark){body{background:#111;color:#ddd}}h1{font-size:20px}</style><h1>${esc(title)}</h1><p>${esc(text)}</p>`;
  return { status, headers: { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' }, body: Buffer.from(html) };
}
function timeoutPage(siteSlug: string, handedOut: boolean, isConnected: boolean): RemoteResult {
  if (handedOut) return page(504, `Site ${siteSlug} did not answer in time`, `The site took the request but sent no answer within ${REMOTE_GUI_REQUEST_TIMEOUT_SEC} s. Reload to try again.`);
  if (isConnected) return page(504, `Site ${siteSlug} is busy`, `The site is connected but did not pick up the request within ${REMOTE_GUI_REQUEST_TIMEOUT_SEC} s. Reload to try again.`);
  return page(503, `Site ${siteSlug} is not connected`, `The site did not ask the cloud for requests. It needs agent 0.19.0 or newer, the remote GUI switched on (agent GUI › Agent), and a connection to the cloud. It starts within a few seconds of opening this page — reload in a moment.`);
}

/** test hook */
export function _resetRemoteGui() {
  for (const p of pending.values()) clearTimeout(p.timer);
  for (const ws of waiters.values()) for (const w of ws) clearTimeout(w.timer);
  sessions.clear();
  pending.clear();
  queues.clear();
  waiters.clear();
  lastPoll.clear();
}
