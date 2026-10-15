/**
 * The agent local API client (@opm/shared createAgentClient). Same origin, cookie opm_session; endpoints with auth
 * "session+actor" get Authorization: Actor <token>. VITE_MOCK=1 swaps fetch for the in-browser mock agent.
 *
 * 401 handling (every call except login/info):
 *   actor-required          → drop the actor token, show the actor popup again
 *   session-expired / other → back to /_opm/login?next=<path>&reason=<code>
 */
import { goto } from '$app/navigation';
import { base } from '$app/paths';
import { createAgentClient, AGENT_API, type AgentClient } from '@opm/shared/agent-api';
import type { ValidationIssue } from '@opm/shared/types';
import { session } from './state/session.svelte';

type FetchFn = typeof fetch;
let fetchImpl: FetchFn = (i, init) => fetch(i, init);
export const isMock: boolean = __OPM_MOCK__;

export function setFetch(f: FetchFn) {
  fetchImpl = f;
}
/** fetch used for everything (API, tunnel requests to the cloud) — the mock in VITE_MOCK mode */
export const opmFetch: FetchFn = (i, init) => fetchImpl(i, init);

const client = createAgentClient('', () => session.actorToken, { fetch: opmFetch });

export interface ApiErrorLike {
  name: 'OpmApiError';
  status: number;
  code: string;
  message: string;
  details: Record<string, unknown>;
}
export function isApiError(e: unknown): e is ApiErrorLike {
  return typeof e === 'object' && e !== null && (e as { name?: string }).name === 'OpmApiError';
}
export function errCode(e: unknown): string | null {
  return isApiError(e) ? e.code : null;
}
export function errMessage(e: unknown): string {
  if (isApiError(e)) {
    if (e.code === 'invalid') {
      const n = issuesOf(e).filter((i) => i.severity === 'error').length;
      return n ? `${e.message} (${n} ${n === 1 ? 'issue' : 'issues'})` : e.message;
    }
    if (e.status === 0 || e.code === 'internal') return e.message || 'Internal error';
    return e.message || e.code;
  }
  if (e instanceof TypeError) return 'Network error: the agent did not answer.';
  if (e instanceof Error) return e.message;
  return String(e);
}
export function issuesOf(e: unknown): ValidationIssue[] {
  if (!isApiError(e)) return [];
  const list = (e.details as { errors?: unknown }).errors;
  return Array.isArray(list) ? (list as ValidationIssue[]) : [];
}

const PUBLIC_OPS = new Set(['login', 'info', 'healthz', 'localCa']);
let redirecting = false;
/** round I: while the restart overlay waits for the agent, it alone decides about a lost session (clear message) */
let holdAuthRedirect: () => boolean = () => false;
export function setAuthHold(fn: () => boolean) {
  holdAuthRedirect = fn;
}

export function onApiError(op: string, e: unknown) {
  if (!isApiError(e) || e.status !== 401 || PUBLIC_OPS.has(op)) return;
  if (holdAuthRedirect()) return;
  if (e.code === 'actor-required') {
    session.actor = null;
    session.askActor = true;
    return;
  }
  if (redirecting || location.pathname.startsWith(`${base}/login`)) return;
  redirecting = true;
  session.clear(e.code === 'session-expired' ? 'Your session ended (6 h). Log in again.' : 'You are not logged in on this site.');
  const here = location.pathname + location.search;
  const next = here.startsWith(`${base}/app`) ? here : `${base}/app`;
  goto(`${base}/login?next=${encodeURIComponent(next)}`, { replaceState: true }).finally(() => (redirecting = false));
}

type AnyCall = (a: unknown) => Promise<unknown>;
const raw = client as unknown as Record<string, AnyCall>;
export const api: AgentClient = new Proxy({} as AgentClient, {
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

/** Path of an endpoint with params + query (for links / XHR uploads). */
export function apiPath(op: keyof typeof AGENT_API, params: Record<string, string> = {}, query?: Record<string, unknown>): string {
  let p: string = AGENT_API[op].path;
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

async function errorFromResponse(res: Response): Promise<ApiErrorLike> {
  let body: { error?: { code?: string; message?: string; details?: Record<string, unknown> } } = {};
  try {
    body = await res.json();
  } catch {
    /* not json */
  }
  return { name: 'OpmApiError', status: res.status, code: body.error?.code ?? 'internal', message: body.error?.message ?? res.statusText, details: body.error?.details ?? {} };
}

/**
 * Raw upload with progress (XHR in the real agent: fetch has no upload progress). Returns the parsed JSON body.
 * withActor: add Authorization: Actor <token>.
 */
export async function uploadRaw<T>(op: keyof typeof AGENT_API, path: string, body: Blob, onProgress?: (f: number) => void): Promise<T> {
  return uploadRawTo<T>({ op, method: AGENT_API[op].method, auth: AGENT_API[op].auth }, path, body, onProgress);
}
/** uploadRaw for any endpoint definition (§21 #96: the tools volume upload) */
export async function uploadRawTo<T>(ep: { op: string; method: string; auth: string }, path: string, body: Blob, onProgress?: (f: number) => void): Promise<T> {
  const op = ep.op;
  const headers: Record<string, string> = { 'Content-Type': body.type || 'application/octet-stream' };
  if (ep.auth === 'session+actor' && session.actorToken) headers.Authorization = `Actor ${session.actorToken}`;
  if (isMock) {
    onProgress?.(0);
    const res = await opmFetch(path, { method: ep.method, headers, body });
    for (const f of [0.35, 0.7, 1]) {
      await new Promise((r) => setTimeout(r, 120));
      onProgress?.(f);
    }
    if (!res.ok) {
      const e = await errorFromResponse(res);
      onApiError(op, e);
      throw e;
    }
    return (await res.json()) as T;
  }
  return new Promise<T>((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open(ep.method, path);
    for (const [k, v] of Object.entries(headers)) xhr.setRequestHeader(k, v);
    xhr.upload.onprogress = (ev) => ev.lengthComputable && onProgress?.(ev.loaded / ev.total);
    xhr.onerror = () => reject(new TypeError('network error'));
    xhr.onload = async () => {
      const res = new Response(xhr.responseText, { status: xhr.status, headers: { 'Content-Type': 'application/json' } });
      if (xhr.status >= 200 && xhr.status < 300) {
        onProgress?.(1);
        try {
          resolve(JSON.parse(xhr.responseText) as T);
        } catch {
          resolve({} as T);
        }
      } else {
        const e = await errorFromResponse(res);
        onApiError(op, e);
        reject(e);
      }
    };
    xhr.send(body);
  });
}

function fileNameFrom(res: Response, fallback: string): string {
  const cd = res.headers.get('Content-Disposition') ?? '';
  const m = /filename\*=UTF-8''([^;]+)|filename="?([^";]+)"?/i.exec(cd);
  const n = m ? decodeURIComponent(m[1] ?? m[2] ?? '') : '';
  return n || fallback;
}
export function saveBlob(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 30_000);
}

/**
 * Download an endpoint. Session-only endpoints in the real agent: a plain link (the browser streams big files,
 * cookie included). Actor endpoints (or mock mode): fetch → Blob → save.
 */
export async function download(op: keyof typeof AGENT_API, path: string, fallbackName: string): Promise<void> {
  const def = AGENT_API[op];
  if (!isMock && def.auth !== 'session+actor') {
    const a = document.createElement('a');
    a.href = path;
    a.download = fallbackName;
    document.body.appendChild(a);
    a.click();
    a.remove();
    return;
  }
  const headers: Record<string, string> = {};
  if (def.auth === 'session+actor' && session.actorToken) headers.Authorization = `Actor ${session.actorToken}`;
  const res = await opmFetch(path, { method: 'GET', headers, credentials: 'same-origin' });
  if (!res.ok) {
    const e = await errorFromResponse(res);
    onApiError(op, e);
    throw e;
  }
  saveBlob(await res.blob(), fileNameFrom(res, fallbackName));
}

/** GET raw bytes of an endpoint as text (file editor). */
export async function fetchText(op: keyof typeof AGENT_API, path: string): Promise<string> {
  const res = await opmFetch(path, { method: 'GET', credentials: 'same-origin' });
  if (!res.ok) {
    const e = await errorFromResponse(res);
    onApiError(op, e);
    throw e;
  }
  return res.text();
}
