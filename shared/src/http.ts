/**
 * Typed endpoint-table helpers shared by cloud-api.ts and agent-api.ts.
 */
import type { ApiErrorBody, ErrorCode } from './types.js';

export type HttpMethod = 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';
/**
 * human         Authorization: Bearer <ID token of /auth>
 * agent         signed request (X-OPM-Site / X-OPM-Ts / X-OPM-Nonce / X-OPM-Sig)
 * install       Authorization: Bearer opi_… (install token)
 * public        nothing
 * session       agent GUI: cookie opm_session
 * session+actor agent GUI: cookie opm_session + Authorization: Actor <actorToken>
 * peer          signed request from a site of the same deployment (agentPort only)
 */
export type AuthKind = 'human' | 'agent' | 'install' | 'public' | 'session' | 'session+actor' | 'peer';
/** json = application/json; raw = application/octet-stream (or any bytes); none = no body */
export type BodyKind = 'json' | 'raw' | 'none';
/** json = JSON body; raw = bytes (download, may arrive as 302 → follow); text = text/plain or script */
export type ResponseKind = 'json' | 'raw' | 'text';

export interface EndpointDef {
  method: HttpMethod;
  path: string; //            with {param} placeholders
  auth: AuthKind;
  /** permission "resource.action" (human), or a rule description; null = none beyond auth */
  perm: string | null;
  body: BodyKind;
  res: ResponseKind;
  /** HTTP status on success (default 200) */
  ok?: number;
  /** error codes this endpoint can return besides unauthenticated / forbidden / internal / rate-limited */
  errors: readonly ErrorCode[];
  /** activity / audit types written on success */
  activities?: readonly string[];
  summary: string;
}
export type Typed<Req, Res, Q> = { readonly __req?: Req; readonly __res?: Res; readonly __q?: Q };

/** Build a typed endpoint: ep<Req, Res, Query>()({ … }) */
export function ep<Req = void, Res = unknown, Q = void>() {
  return <const D extends EndpointDef>(d: D): D & Typed<Req, Res, Q> => d as D & Typed<Req, Res, Q>;
}

export type PathParams<P extends string> = P extends `${string}{${infer K}}${infer R}` ? { [k in K]: string } & PathParams<R> : unknown;
type ReqOf<E> = E extends Typed<infer R, unknown, unknown> ? R : never;
type ResOf<E> = E extends Typed<unknown, infer R, unknown> ? R : never;
type QOf<E> = E extends Typed<unknown, unknown, infer Q> ? Q : never;
type RawBody = Blob | ArrayBuffer | Uint8Array | string | ReadableStream<Uint8Array>;
export type CallArgs<E extends EndpointDef> = (E extends { path: infer P extends string } ? PathParams<P> : unknown) &
  ([ReqOf<E>] extends [void] ? (E['body'] extends 'raw' ? { body: RawBody; contentType?: string; headers?: Record<string, string> } : { headers?: Record<string, string> }) : { body: ReqOf<E>; headers?: Record<string, string> }) &
  ([QOf<E>] extends [void] ? unknown : { query?: QOf<E> });
export type CallResult<E extends EndpointDef> = E['res'] extends 'json' ? ResOf<E> : Response;
export type Client<T extends Record<string, EndpointDef>> = { [K in keyof T]: (args: CallArgs<T[K]>) => Promise<CallResult<T[K]>> };

export class OpmApiError extends Error {
  constructor(
    public readonly status: number,
    public readonly code: ErrorCode | string,
    message: string,
    public readonly details: Record<string, unknown> = {},
  ) {
    super(message);
    this.name = 'OpmApiError';
  }
}

export function fillPath(path: string, params: Record<string, unknown>): string {
  return path.replace(/\{([A-Za-z0-9_]+)\}/g, (_, k: string) => {
    const v = params[k];
    if (v === undefined || v === null || v === '') throw new Error(`missing path parameter ${k} for ${path}`);
    return encodeURIComponent(String(v));
  });
}

export function buildQuery(q: Record<string, unknown> | undefined): string {
  if (!q) return '';
  const parts: string[] = [];
  for (const [k, v] of Object.entries(q)) {
    if (v === undefined || v === null) continue;
    parts.push(`${encodeURIComponent(k)}=${encodeURIComponent(typeof v === 'boolean' ? (v ? '1' : '0') : String(v))}`);
  }
  return parts.length ? `?${parts.join('&')}` : '';
}

export interface RequestContext {
  method: HttpMethod;
  /** path + query exactly as sent, starting at "/v1/…" or "/_opm/…" */
  path: string;
  body: Uint8Array | null;
  endpoint: EndpointDef;
}
/** Return extra headers (or an Authorization value as a string), or null for none. */
export type AuthProvider = (ctx: RequestContext) => Promise<Record<string, string> | string | null> | Record<string, string> | string | null;

async function toBytes(b: unknown): Promise<Uint8Array | null> {
  if (b === undefined || b === null) return null;
  if (b instanceof Uint8Array) return b;
  if (b instanceof ArrayBuffer) return new Uint8Array(b);
  if (typeof b === 'string') return new TextEncoder().encode(b);
  if (typeof Blob !== 'undefined' && b instanceof Blob) return new Uint8Array(await b.arrayBuffer());
  return null; // streams are sent as is
}

export function createClient<T extends Record<string, EndpointDef>>(table: T, baseUrl: string, auth: AuthProvider | null, init?: { fetch?: typeof fetch; credentials?: RequestCredentials }): Client<T> {
  const f = init?.fetch ?? globalThis.fetch.bind(globalThis);
  const base = baseUrl.replace(/\/$/, '');
  const out: Record<string, unknown> = {};
  for (const [name, def] of Object.entries(table)) {
    out[name] = async (args: Record<string, unknown> = {}) => {
      const path = fillPath(def.path, args) + buildQuery(args.query as Record<string, unknown> | undefined);
      const headers: Record<string, string> = { ...((args.headers as Record<string, string>) ?? {}) };
      let body: BodyInit | undefined;
      let bytes: Uint8Array | null = null;
      if (def.body === 'json') {
        bytes = new TextEncoder().encode(JSON.stringify(args.body ?? {}));
        body = bytes as unknown as BodyInit;
        headers['Content-Type'] = 'application/json';
      } else if (def.body === 'raw') {
        const raw = args.body;
        bytes = await toBytes(raw);
        body = (bytes ?? raw) as BodyInit;
        headers['Content-Type'] = (args.contentType as string) ?? 'application/octet-stream';
      }
      if (auth) {
        const a = await auth({ method: def.method, path, body: bytes, endpoint: def });
        if (typeof a === 'string') headers['Authorization'] = a;
        else if (a) Object.assign(headers, a);
      }
      const res = await f(base + path, { method: def.method, headers, body, credentials: init?.credentials, redirect: 'follow' });
      if (!res.ok) {
        let parsed: ApiErrorBody | null = null;
        try {
          parsed = (await res.json()) as ApiErrorBody;
        } catch {
          /* not JSON */
        }
        throw new OpmApiError(res.status, parsed?.error?.code ?? 'internal', parsed?.error?.message ?? res.statusText, parsed?.error?.details ?? {});
      }
      if (def.res === 'json') return res.status === 204 ? undefined : res.json();
      return res;
    };
  }
  return out as Client<T>;
}
