/**
 * Dev tools client (§7.6, §21 #97, @opm/shared/devtools). Same rules as lib/tools/api.ts: same origin, cookie session,
 * Authorization: Actor <token> on session+actor endpoints and the lib/api 401 handling. The MQTT stream is an
 * EventSource on the ticketed streamPath; in VITE_MOCK mode lib/mock/devtools.ts installs a fake EventSource class
 * with setDevStreamImpl().
 */
import { base } from '$app/paths';
import { createDevToolsClient, DEVTOOLS_API, type DevToolBlockedDetails, type DevToolConfirmDetails, type DevToolsClient, type MqttMessage, type MqttSubscription } from '@opm/shared/devtools';
import { ulid } from '@opm/shared/ulid';
import { isApiError, onApiError, opmFetch } from '$lib/api';
import { session } from '$lib/state/session.svelte';

const client = createDevToolsClient('', () => session.actorToken, { fetch: opmFetch });
type AnyCall = (a: unknown) => Promise<unknown>;
const raw = client as unknown as Record<string, AnyCall>;
export const dev: DevToolsClient = new Proxy({} as DevToolsClient, {
  get(_t, name: string) {
    return async (args: unknown = {}) => {
      try {
        return await raw[name](args ?? {});
      } catch (e) {
        onApiError(name, e);
        throw e;
      }
    };
  },
});

/** Path of a dev tools endpoint with params + query (download links, the MQTT stream). */
export function devPath(op: keyof typeof DEVTOOLS_API, params: Record<string, string>, query?: Record<string, unknown>): string {
  let p: string = DEVTOOLS_API[op].path;
  p = p.replace(/\{([A-Za-z0-9_]+)\}/g, (_, k: string) => encodeURIComponent(params[k] ?? ''));
  if (query) {
    const parts: string[] = [];
    for (const [k, v] of Object.entries(query)) {
      if (v === undefined || v === null || v === '') continue;
      parts.push(`${encodeURIComponent(k)}=${encodeURIComponent(String(v))}`);
    }
    if (parts.length) p += `?${parts.join('&')}`;
  }
  return p;
}

/** client-chosen execution id (cancel needs it) */
export const newQueryId = () => ulid();

/** 422 invalid with details.confirmRequired → the reasons (else null) */
export function confirmReasons(e: unknown): string[] | null {
  if (!isApiError(e) || e.code !== 'invalid') return null;
  const d = (e.details ?? {}) as Partial<DevToolConfirmDetails>;
  if (!d.confirmRequired) return null;
  return Array.isArray(d.reasons) ? d.reasons.map(String) : [];
}
/** 422 invalid with details.blocked (Redis command refused entirely) → the command name (else null) */
export function blockedCommand(e: unknown): string | null {
  if (!isApiError(e) || e.code !== 'invalid') return null;
  const d = (e.details ?? {}) as Partial<DevToolBlockedDetails>;
  return d.blocked ? (d.command ?? '') : null;
}

// ───────────────────────────── MQTT stream (EventSource)
interface ESLike {
  addEventListener(type: string, fn: (e: MessageEvent) => void): void;
  onerror: ((e: Event) => void) | null;
  close(): void;
  readyState: number;
}
type ESCtor = new (url: string, init?: EventSourceInit) => ESLike;
let ES: ESCtor = (typeof EventSource !== 'undefined' ? EventSource : null) as unknown as ESCtor;
export function setDevStreamImpl(es: ESCtor) {
  ES = es;
}
export interface MqttStreamHandlers {
  meta?: (m: { sessionId: string; subscriptions: MqttSubscription[] }) => void;
  message: (m: MqttMessage) => void;
  status?: (s: { connected: boolean; error: string | null }) => void;
  dropped?: (n: number) => void;
  /** the stream is over: reason = the server `end` reason, or "interrupted" (network) / an error message */
  end: (reason: string, unexpected: boolean) => void;
}
/** Open the MQTT session stream; messages are delivered in batches of one animation frame. */
export function openMqttStream(url: string, h: MqttStreamHandlers): { close(): void } {
  // the agent answers /_opm/…; through the cloud (§21 #118) the page's base carries the remote prefix (JSON is not
  // rewritten, so streamPath has none: without this the stream went to the cloud's own page and failed at once)
  const es = new ES(url.replace(/^\/_opm(?=\/)/, base), { withCredentials: true });
  let closed = false;
  const parse = (d: unknown) => {
    try {
      return JSON.parse(String(d));
    } catch {
      return null;
    }
  };
  es.addEventListener('meta', (e) => h.meta?.(parse(e.data)));
  es.addEventListener('message', (e) => {
    const m = parse(e.data) as MqttMessage | null;
    if (m) h.message(m);
  });
  es.addEventListener('status', (e) => h.status?.(parse(e.data)));
  es.addEventListener('dropped', (e) => h.dropped?.(Number(parse(e.data)?.count ?? 0)));
  es.addEventListener('end', (e) => {
    if (closed) return;
    closed = true;
    es.close();
    const reason = String(parse(e.data)?.reason ?? 'ended');
    h.end(reason, false);
  });
  es.addEventListener('error', (e) => {
    const d = (e as MessageEvent).data;
    if (typeof d === 'string' && d && !closed) {
      closed = true;
      es.close();
      h.end(String(parse(d)?.message ?? d), true);
    }
  });
  es.onerror = () => {
    if (closed) return;
    closed = true;
    es.close();
    h.end('interrupted', true);
  };
  return {
    close() {
      closed = true;
      es.close();
    },
  };
}
