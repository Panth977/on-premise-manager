/**
 * Tiny router for /v1/*: registers EXACTLY the routes of CLOUD_API (shared/src/cloud-api.ts) — a handler map typed
 * as Record<keyof CloudApi, Handler> makes a missing or extra route a compile error.
 * Mount prefixes are stripped: matching and signing use the path starting at "/v1/" (§1.2).
 */
import { randomUUID } from 'node:crypto';
import { CLOUD_API, CREDENTIAL_OPS, REVEAL_RATE_PER_MINUTE, type Account, type EndpointDef } from '@opm/shared';
import { Timestamp } from '../ports/index.js';
import { ApiError, E } from './errors.js';
import { Reply } from './reply.js';
import { verifyAgent, verifyHuman, type AgentAuth } from './auth.js';
import { col, db } from './fire.js';
import { primeImageVolumes } from './catalog.js';
import { env } from './env.js';
import { logger } from '../ports/index.js';
import { auditBegin, auditFinish, isAudited, type AuditHint, type AuditPre } from './audit.js';

export type OpName = keyof typeof CLOUD_API;

export interface ReqLike {
  method: string;
  /** path from "/v1/" including "?query" exactly as sent */
  url: string;
  headers: Record<string, string | undefined>;
  body: Buffer;
  ip: string;
  signal?: AbortSignal;
  /** URL prefix before "/v1/" as received (emulator: /<project>/<region>/api) */
  mount?: string;
}

export interface Ctx {
  op: OpName;
  def: EndpointDef;
  method: string;
  /** path without query */
  path: string;
  /** path + query exactly as sent (what agents sign) */
  url: string;
  params: Record<string, string>;
  query: URLSearchParams;
  headers: Record<string, string | undefined>;
  body: Buffer;
  ip: string;
  requestId: string;
  account: Account | null;
  agent: AgentAuth | null;
  signal: AbortSignal | undefined;
  mount: string;
  /** audited requests only: handlers refine the root audit entry (action / target / targetType / data) */
  audit: AuditHint | null;
  json<T = Record<string, unknown>>(): T;
}
export type Handler = (c: Ctx) => Promise<unknown>;
export type Handlers = Record<OpName, Handler>;

interface Route {
  op: OpName;
  def: EndpointDef;
  re: RegExp;
  keys: string[];
  literalScore: number;
}
let routes: Route[] | null = null;
function compile(): Route[] {
  if (routes) return routes;
  routes = Object.entries(CLOUD_API).map(([op, def]) => {
    const keys: string[] = [];
    const src = def.path.replace(/[.*+?^$()|[\]\\]/g, '\\$&').replace(/\{([A-Za-z0-9_]+)\}/g, (_, k: string) => {
      keys.push(k);
      return '([^/]+)';
    });
    return { op: op as OpName, def: def as EndpointDef, re: new RegExp(`^${src}$`), keys, literalScore: -keys.length };
  });
  routes.sort((a, b) => b.literalScore - a.literalScore);
  return routes;
}

// ───────────────────────────────────────── rate limit (per uid / per site, per instance, best effort)
const buckets = new Map<string, { tokens: number; at: number }>();
function rateLimit(key: string) {
  const now = Date.now();
  const b = buckets.get(key) ?? { tokens: env.rate.burst, at: now };
  b.tokens = Math.min(env.rate.burst, b.tokens + ((now - b.at) / 1000) * env.rate.perSec);
  b.at = now;
  if (b.tokens < 1) {
    buckets.set(key, b);
    const retry = Math.max(1, Math.ceil((1 - b.tokens) / env.rate.perSec));
    throw new ApiError('rate-limited', 'too many requests', { retryAfterSec: retry }, { 'Retry-After': String(retry) });
  }
  b.tokens -= 1;
  buckets.set(key, b);
  if (buckets.size > 10000) for (const [k, v] of buckets) if (now - v.at > 60000) buckets.delete(k);
}

// ───────────────────────────────────────── credential ops (§21 #84): no-store + per-uid fixed-minute limit (Firestore)
const CREDENTIAL = new Set<string>(CREDENTIAL_OPS);
export const isCredentialOp = (op: string) => CREDENTIAL.has(op);
/** REVEAL_RATE_PER_MINUTE; OPM_REVEAL_RATE overrides it (tests). */
export const revealRate = () => {
  const n = Number(process.env.OPM_REVEAL_RATE);
  return Number.isInteger(n) && n > 0 ? n : REVEAL_RATE_PER_MINUTE;
};
/**
 * Count one value-returning request of `uid` in the current UTC minute (doc rateLimits/reveal_<uid>_<yyyymmddhhmm>,
 * expireAt = window end + 1 min → TTL policy + daily cleanup). Over the limit → 429 rate-limited {retryAfterSec}.
 */
export async function credentialRateLimit(uid: string, now = new Date()): Promise<void> {
  const win = now.toISOString().slice(0, 16).replace(/[^0-9]/g, ''); // yyyymmddhhmm
  const winStart = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate(), now.getUTCHours(), now.getUTCMinutes());
  const ref = db().collection('rateLimits').doc(`reveal_${uid.replace(/[/.]/g, '_')}_${win}`);
  const limit = revealRate();
  const count = await db().runTransaction(async (tx) => {
    const s = await tx.get(ref);
    const n = ((s.data()?.count as number | undefined) ?? 0) + 1;
    tx.set(ref, { uid, window: win, count: n, expireAt: Timestamp.fromMillis(winStart + 120_000) });
    return n;
  });
  if (count > limit) {
    const retry = Math.max(1, Math.ceil((winStart + 60_000 - now.getTime()) / 1000));
    throw new ApiError('rate-limited', `too many credential requests (${limit} per minute); try again in ${retry} s`, { retryAfterSec: retry }, { 'Retry-After': String(retry) });
  }
}

const JSON_LIMIT = 10 * 1024 * 1024;
export const jsonHeaders = { 'Content-Type': 'application/json; charset=utf-8' };

export function errorReply(e: unknown, requestId: string): Reply {
  if (e instanceof ApiError) {
    return new Reply(e.status, { ...jsonHeaders, ...e.headers }, JSON.stringify({ error: { code: e.code, message: e.message, details: e.details } }));
  }
  logger.error('internal error', { requestId, error: (e as Error)?.stack ?? String(e) });
  return new Reply(500, jsonHeaders, JSON.stringify({ error: { code: 'internal', message: 'internal error', details: { requestId } } }));
}

let handlers: Handlers | null = null;
export function setHandlers(h: Handlers) {
  handlers = h;
}

export interface DispatchOpts {
  /** relay: the inner request was verified by the relay handler */
  preAgent?: AgentAuth;
}

export async function dispatch(req: ReqLike, opts: DispatchOpts = {}): Promise<Reply> {
  const requestId = randomUUID();
  const meta: { op?: OpName } = {};
  const reply = await run(req, requestId, opts, meta).catch((e) => errorReply(e, requestId));
  // §21 #84: responses of credential ops are never cached (success AND errors)
  if (meta.op && isCredentialOp(meta.op)) {
    reply.headers['Cache-Control'] = 'no-store';
    reply.headers['Pragma'] = 'no-cache';
  }
  reply.headers['X-Request-Id'] = requestId;
  reply.headers['X-OPM-Server-Time'] = String(Math.floor(Date.now() / 1000));
  return reply;
}

async function run(req: ReqLike, requestId: string, opts: DispatchOpts, meta: { op?: OpName }): Promise<Reply> {
  const qi = req.url.indexOf('?');
  const path = qi >= 0 ? req.url.slice(0, qi) : req.url;
  const query = new URLSearchParams(qi >= 0 ? req.url.slice(qi + 1) : '');
  const method = req.method.toUpperCase();
  let found: Route | null = null;
  let m: RegExpExecArray | null = null;
  let pathMatched = false;
  for (const r of compile()) {
    const mm = r.re.exec(path);
    if (!mm) continue;
    pathMatched = true;
    if (r.def.method === method) {
      found = r;
      m = mm;
      break;
    }
  }
  if (!found || !m) {
    if (pathMatched) throw new ApiError('method-not-allowed', `${method} is not allowed on ${path}`);
    throw E.notFound(`no route ${method} ${path}`);
  }
  const params: Record<string, string> = {};
  found.keys.forEach((k, i) => {
    try {
      params[k] = decodeURIComponent(m![i + 1]!);
    } catch {
      throw E.bad(k, 'V_FORMAT', `bad path parameter ${k}`);
    }
  });
  const def = found.def;
  meta.op = found.op;
  // round D: normalize-on-read needs the image volumes (legacy implicit image volumes → explicit rows); cached, TTL 15 s
  await primeImageVolumes();
  if (def.body === 'json' && req.body.length > JSON_LIMIT) throw E.tooLarge('JSON body larger than 10 MiB');
  let parsed: unknown;
  const c: Ctx = {
    op: found.op,
    def,
    method,
    path,
    url: req.url,
    params,
    query,
    headers: req.headers,
    body: req.body,
    ip: req.ip,
    requestId,
    account: null,
    agent: null,
    signal: req.signal,
    mount: req.mount ?? '',
    audit: null,
    json<T>() {
      if (parsed === undefined) {
        if (!req.body.length) parsed = {};
        else
          try {
            parsed = JSON.parse(req.body.toString('utf8'));
          } catch {
            throw E.bad('', 'V_TYPE', 'body is not valid JSON');
          }
      }
      return parsed as T;
    },
  };
  if (!handlers) throw new Error('handlers not registered');
  if (isAudited(found.op, method, def.auth, path)) {
    // root audit hook (§6.2): one entry per request, success or failure; a failed audit write never fails the request
    let pre: AuditPre | null = null;
    let out: unknown;
    let err: unknown = undefined;
    try {
      c.account = await verifyHuman(req.headers['authorization']);
      rateLimit(`u:${c.account.uid}`);
      if (isCredentialOp(found.op)) await credentialRateLimit(c.account.uid);
      pre = await auditBegin(c);
      out = await handlers[found.op](c);
    } catch (e) {
      err = e ?? new Error('unknown error');
    }
    await auditFinish(c, pre, out, err, out instanceof Reply ? out.status : (def.ok ?? 200));
    if (err !== undefined) throw err;
    if (out instanceof Reply) return out;
    return new Reply(def.ok ?? 200, { ...jsonHeaders }, JSON.stringify(out ?? null));
  }
  if (def.auth === 'human') {
    c.account = await verifyHuman(req.headers['authorization']);
    rateLimit(`u:${c.account.uid}`);
    if (isCredentialOp(found.op)) await credentialRateLimit(c.account.uid);
  } else if (def.auth === 'agent') {
    if (opts.preAgent) c.agent = opts.preAgent;
    else {
      c.agent = await verifyAgent({ method, path: req.url, headers: req.headers, body: req.body }, { allowDeleted: found.op === 'poll' });
      if (c.agent.agent.relayVia && found.op !== 'relay' && found.op !== 'heartbeat') {
        await col.agents(c.agent.deploymentId).doc(c.agent.siteSlug).update({ relayVia: null }).catch(() => undefined);
      }
    }
    rateLimit(`s:${c.agent.deploymentId}/${c.agent.siteSlug}`);
  } else if (def.auth === 'install') {
    rateLimit(`i:${req.ip}`);
  }
  const out = await handlers[found.op](c);
  if (out instanceof Reply) return out;
  return new Reply(def.ok ?? 200, { ...jsonHeaders }, JSON.stringify(out ?? null));
}
