import { createCloudClient, OpmApiError, type ValidationIssue, type CloudClient } from '@opm/shared';
import { MOCK, apiBase } from './env';
import { noteResponse } from './util/serverclock.svelte';

/** Supplies the Authorization header (Firebase / opm-cloud ID token or the mock token). Set by session.svelte.ts. */
let tokenProvider: () => Promise<string | null> = async () => null;
/** §21 #108 local auth: after a 401 'unauthenticated' → a fresh ID token (null = signed out); the request is retried once. */
let onUnauthorized: (() => Promise<string | null>) | null = null;
export function setTokenProvider(p: () => Promise<string | null>, unauthorized: (() => Promise<string | null>) | null = null) {
  tokenProvider = p;
  onUnauthorized = unauthorized;
}

function hasBearer(h: HeadersInit | undefined): boolean {
  if (!h) return false;
  return /^Bearer /.test(new Headers(h).get('Authorization') ?? '');
}

/** 401 with code 'unauthenticated' on a request that carried a token → refresh + retry once (bodies here are bytes / strings). */
async function retryUnauthorized(f: typeof fetch, input: RequestInfo | URL, init: RequestInit | undefined, res: Response): Promise<Response> {
  if (res.status !== 401 || !onUnauthorized || !hasBearer(init?.headers)) return res;
  if (typeof ReadableStream !== 'undefined' && init?.body instanceof ReadableStream) return res;
  let code: string | null = null;
  try {
    code = ((await res.clone().json()) as { error?: { code?: string } })?.error?.code ?? null;
  } catch {
    /* not JSON */
  }
  if (code !== 'unauthenticated') return res;
  const t = await onUnauthorized().catch(() => null);
  if (!t) return res;
  const headers = new Headers(init?.headers);
  headers.set('Authorization', `Bearer ${t}`);
  return f(input, { ...init, headers });
}

const mockFetch: typeof fetch = async (input, init) => {
  const m = await import('./mock/server');
  return m.mockFetch(input, init);
};

/** §21 #92 every API response tells the cloud's clock (X-OPM-Server-Time) → relative times use it, not the browser clock */
const clockFetch: typeof fetch = async (input, init) => {
  const f = MOCK ? mockFetch : fetch;
  const res = await retryUnauthorized(f, input, init, await f(input, init));
  noteResponse(res);
  return res;
};

const authHeader = async () => {
  const t = await tokenProvider();
  return t ? `Bearer ${t}` : null;
};
/** one client per API base (§21 #108: the base may come from opm-config.json, read at startup) */
let client: { base: string; c: CloudClient } | null = null;
function currentClient(): CloudClient {
  const b = apiBase();
  if (!client || client.base !== b) client = { base: b, c: createCloudClient(b, authHeader, { fetch: clockFetch }) };
  return client.c;
}

/** The typed cloud API client (every endpoint of CLOUD_API). */
export const api: CloudClient = new Proxy({} as CloudClient, {
  get: (_t, k) => (currentClient() as unknown as Record<string | symbol, unknown>)[k],
});

/** Raw authorised fetch for downloads (follows 302 to signed URLs). */
export async function authFetch(path: string, init: RequestInit = {}): Promise<Response> {
  const t = await tokenProvider();
  const headers = new Headers(init.headers);
  if (t) headers.set('Authorization', `Bearer ${t}`);
  const f = MOCK ? mockFetch : fetch;
  const url = apiBase() + path;
  const req: RequestInit = { ...init, headers, redirect: 'follow' };
  return retryUnauthorized(f, url, req, await f(url, req));
}

/** A blob / file download that failed; `message` is the human text (HTTP status + server message, or network / CORS). */
export class DownloadError extends Error {
  readonly status: number | null;
  constructor(message: string, status: number | null) {
    super(message);
    this.name = 'DownloadError';
    this.status = status;
  }
}

function hostOf(url: string): string {
  try {
    return new URL(url, typeof location !== 'undefined' ? location.href : 'http://localhost').host;
  } catch {
    return url;
  }
}

/**
 * Download a content-addressed blob (GET /v1/blobs/{sha256}, SPEC §21 #55): bodies ≤ 8 MiB (BLOB_STREAM_MAX) come back
 * with 200; larger ones answer 302 to a short-lived signed Storage URL, which fetch follows. Never throws the bare
 * "Failed to fetch": the error says the HTTP status + server message, or "network error / CORS" with the host.
 */
export async function fetchBlob(sha256: string): Promise<Blob> {
  const path = `/v1/blobs/${encodeURIComponent(sha256)}`;
  const url = apiBase() + path;
  let res: Response;
  try {
    res = await authFetch(path);
  } catch (e) {
    const why = e instanceof Error ? e.message : String(e);
    throw new DownloadError(`network error / CORS: could not download from ${hostOf(url)} (or the Cloud Storage URL it redirects to) — ${why}`, null);
  }
  if (!res.ok) {
    let detail = '';
    try {
      const text = await res.text();
      try {
        const j = JSON.parse(text) as { error?: { code?: string; message?: string } };
        detail = j.error?.message ? `${j.error.code ? j.error.code + ': ' : ''}${j.error.message}` : text;
      } catch {
        detail = text;
      }
    } catch {
      /* no body */
    }
    detail = detail.replace(/\s+/g, ' ').trim().slice(0, 300);
    const where = res.redirected ? ` (from ${hostOf(res.url)})` : '';
    throw new DownloadError(`download failed: ${res.status}${res.statusText ? ' ' + res.statusText : ''}${where}${detail ? ' — ' + detail : ''}`, res.status);
  }
  try {
    return await res.blob();
  } catch (e) {
    throw new DownloadError(`download failed while reading the body from ${hostOf(res.url || url)} — ${e instanceof Error ? e.message : String(e)}`, res.status);
  }
}

export { OpmApiError };

export function isApiError(e: unknown): e is OpmApiError {
  return e instanceof OpmApiError || (typeof e === 'object' && e !== null && (e as { name?: string }).name === 'OpmApiError');
}

/** Human message of any error (API message first). */
export function errMsg(e: unknown): string {
  if (isApiError(e)) {
    const issues = errIssues(e);
    if (e.code === 'invalid' && issues.length) {
      const first = issues.filter((i) => i.severity === 'error')[0] ?? issues[0]!;
      return `${e.message || 'Invalid'}${issues.length ? ` — ${first.path ? first.path + ': ' : ''}${first.message}${issues.length > 1 ? ` (+${issues.length - 1} more)` : ''}` : ''}`;
    }
    return e.message || e.code;
  }
  if (e instanceof DownloadError) return e.message;
  if (e instanceof Error) {
    if (e.message === 'Failed to fetch' || e.name === 'TypeError') return `Network error / CORS: cannot reach ${hostOf(apiBase() || '/')} (${e.message}). Check your connection and try again.`;
    return e.message;
  }
  return String(e);
}

export function errIssues(e: unknown): ValidationIssue[] {
  if (!isApiError(e)) return [];
  const list = (e.details as { errors?: unknown })?.errors;
  return Array.isArray(list) ? (list as ValidationIssue[]) : [];
}

export function errCode(e: unknown): string | null {
  return isApiError(e) ? e.code : null;
}

/** ifVersion / ifRevision conflict → the current version (details.current), else null. */
export function conflictVersion(e: unknown): number | null {
  if (!isApiError(e) || e.code !== 'conflict') return null;
  const c = (e.details as { current?: unknown })?.current;
  return typeof c === 'number' ? c : null;
}
