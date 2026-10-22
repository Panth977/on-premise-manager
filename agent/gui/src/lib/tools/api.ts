/**
 * Operator toolbox client (round T, @opm/shared/agent-tools). Same rules as lib/api.ts: same origin, cookie session,
 * Authorization: Actor <token> on session+actor endpoints, the same 401 handling (actor popup / login redirect).
 * Streams: logs = EventSource (text/event-stream, cookie), terminal = WebSocket (single-use ticket). In VITE_MOCK mode
 * lib/mock/tools.ts installs fake EventSource / WebSocket classes with setStreamImpl().
 */
import { createToolsClient, TOOLS_API, type ToolsClient } from '@opm/shared/agent-tools';
import { base } from '$app/paths';
import { isMock, onApiError, opmFetch, saveBlob } from '$lib/api';
import { session } from '$lib/state/session.svelte';

const client = createToolsClient('', () => session.actorToken, { fetch: opmFetch });

type AnyCall = (a: unknown) => Promise<unknown>;
const raw = client as unknown as Record<string, AnyCall>;
export const tools: ToolsClient = new Proxy({} as ToolsClient, {
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

/** Path of a tools endpoint with params + query (EventSource URLs, plain download links). */
export function toolsPath(op: keyof typeof TOOLS_API, params: Record<string, string>, query?: Record<string, unknown>): string {
  let p: string = TOOLS_API[op].path;
  p = p.replace(/\{([A-Za-z0-9_]+)\}/g, (_, k: string) => encodeURIComponent(params[k] ?? ''));
  if (query) {
    const parts: string[] = [];
    for (const [k, v] of Object.entries(query)) {
      if (v === undefined || v === null || v === '') continue;
      parts.push(`${encodeURIComponent(k)}=${encodeURIComponent(typeof v === 'boolean' ? (v ? '1' : '0') : String(v))}`);
    }
    if (parts.length) p += `?${parts.join('&')}`;
  }
  return p;
}

/** Open a same-origin link (ticket downloads, session-only downloads): the browser streams the file. */
export function openLink(url: string, name?: string) {
  const a = document.createElement('a');
  a.href = url;
  if (name !== undefined) a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
}

/**
 * Download a same-origin URL (ticket link or session-only GET). Real agent: a plain link (the browser streams it).
 * Mock mode: fetch → Blob (the static test server has no API).
 */
export async function downloadUrl(url: string, fallbackName: string): Promise<void> {
  if (!isMock) return openLink(url, fallbackName);
  const res = await opmFetch(url, { method: 'GET', credentials: 'same-origin' });
  if (!res.ok) throw new Error(`download failed (${res.status})`);
  const cd = res.headers.get('Content-Disposition') ?? '';
  const m = /filename="?([^";]+)"?/i.exec(cd);
  saveBlob(await res.blob(), m?.[1] ?? fallbackName);
}
/** GET a same-origin URL as text (transcript viewer). */
export async function fetchUrlText(url: string): Promise<string> {
  const res = await opmFetch(url, { method: 'GET', credentials: 'same-origin' });
  if (!res.ok) throw new Error(`request failed (${res.status})`);
  return res.text();
}

// ───────────────────────────────────────────── streams

interface ESLike {
  addEventListener(type: string, fn: (e: MessageEvent) => void): void;
  onerror: ((e: Event) => void) | null;
  close(): void;
  readyState: number;
}
type ESCtor = new (url: string, init?: EventSourceInit) => ESLike;
type WSCtor = new (url: string) => WebSocket;
let ES: ESCtor = (typeof EventSource !== 'undefined' ? EventSource : null) as unknown as ESCtor;
let WS: WSCtor = (typeof WebSocket !== 'undefined' ? WebSocket : null) as unknown as WSCtor;
export function setStreamImpl(es: ESCtor, ws: WSCtor) {
  ES = es;
  WS = ws;
}

export interface StreamHandlers<L> {
  line: (l: L) => void;
  meta?: (m: Record<string, unknown>) => void;
  end?: (reason: string) => void;
  error?: (message: string) => void;
  open?: () => void;
}
export interface StreamHandle {
  close(): void;
}

/**
 * EventSource over a tools stream. follow = reconnect on network errors (since = last line ts, at most every 2 s);
 * a server `end` event closes it for good.
 */
export function openLogStream<L extends { t: string }>(url: string, h: StreamHandlers<L>, opts: { follow: boolean } = { follow: false }): StreamHandle {
  let es: ESLike | null = null;
  let closed = false;
  let lastTs: string | null = null;
  let retry: ReturnType<typeof setTimeout> | null = null;
  const connect = (u: string) => {
    es = new ES(u, { withCredentials: true });
    es.addEventListener('open', () => h.open?.());
    es.addEventListener('meta', (e) => h.meta?.(safeJson(e.data) as Record<string, unknown>));
    es.addEventListener('line', (e) => {
      const l = safeJson(e.data) as L | null;
      if (!l) return;
      lastTs = l.t;
      h.line(l);
    });
    es.addEventListener('end', (e) => {
      closed = true;
      es?.close();
      h.end?.(String((safeJson(e.data) as { reason?: string } | null)?.reason ?? 'end'));
    });
    es.addEventListener('error', (e) => {
      const d = (e as MessageEvent).data;
      if (typeof d === 'string' && d) {
        closed = true;
        es?.close();
        h.error?.(String((safeJson(d) as { message?: string } | null)?.message ?? d));
      }
    });
    es.onerror = () => {
      if (closed) return;
      es?.close();
      if (!opts.follow) {
        closed = true;
        h.error?.('The log stream was interrupted.');
        return;
      }
      h.error?.('reconnecting…');
      retry = setTimeout(() => {
        if (closed) return;
        const next = lastTs ? withParam(withParam(url, 'since', lastTs), 'tail', '0') : url;
        connect(next);
      }, 2000);
    };
  };
  connect(url);
  return {
    close() {
      closed = true;
      if (retry) clearTimeout(retry);
      es?.close();
    },
  };
}

function withParam(url: string, k: string, v: string): string {
  const u = new URL(url, location.origin);
  u.searchParams.set(k, v);
  return u.pathname + u.search;
}
function safeJson(s: unknown): unknown {
  try {
    return JSON.parse(String(s));
  } catch {
    return null;
  }
}

/** WebSocket to a terminal ticket path (ws:// or wss:// like the page). */
export function openTerminalSocket(wsPath: string): WebSocket {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  // the agent answers /_opm/…; through the cloud (§21 #118 g) the page's base carries the remote prefix (JSON is not
  // rewritten; the regex form is left alone by the cloud's rewriting, base is not)
  const ws = new WS(`${proto}//${location.host}${wsPath.replace(/^\/_opm(?=\/)/, base)}`);
  ws.binaryType = 'arraybuffer';
  return ws;
}
