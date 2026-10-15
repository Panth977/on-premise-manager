/** HTTP client helpers (§21 #97): URL ↔ query rows, curl, history → request, defaults, header suggestions. */
import type { DevToolConnection, DevToolsInfo, HttpKV, HttpRequestSpec } from '@opm/shared/devtools';

export const METHODS: HttpRequestSpec['method'][] = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS'];
export const METHOD_TONE: Record<string, string> = { GET: 'green', POST: 'amber', PUT: 'blue', PATCH: 'violet', DELETE: 'red', HEAD: 'gray', OPTIONS: 'gray' };
export const COMMON_HEADERS = ['Accept', 'Accept-Encoding', 'Accept-Language', 'Authorization', 'Cache-Control', 'Content-Type', 'Cookie', 'If-Match', 'If-None-Match', 'Origin', 'Range', 'Referer', 'User-Agent', 'X-Api-Key', 'X-Forwarded-For', 'X-Request-Id'];
export const CONTENT_TYPES = ['application/json', 'text/plain', 'application/xml', 'text/html', 'text/csv', 'application/x-www-form-urlencoded', 'application/octet-stream'];

export function blankRequest(): HttpRequestSpec {
  return { method: 'GET', url: '', query: [], headers: [], body: { kind: 'none' }, auth: null, followRedirects: null, tlsInsecure: null, timeoutSec: null };
}
/** "path?a=1&b" → rows (raw text kept: placeholders stay as typed) */
export function parseQuery(q: string): HttpKV[] {
  if (!q) return [];
  return q.split('&').map((kv) => {
    const i = kv.indexOf('=');
    const dec = (s: string) => {
      try {
        return decodeURIComponent(s.replace(/\+/g, ' '));
      } catch {
        return s;
      }
    };
    return { name: dec(i < 0 ? kv : kv.slice(0, i)), value: i < 0 ? '' : dec(kv.slice(i + 1)), enabled: true };
  });
}
const enc = (s: string) => (/\{\{/.test(s) ? s : encodeURIComponent(s).replace(/%2F/gi, '/'));
export function queryString(rows: HttpKV[]): string {
  return rows
    .filter((r) => r.enabled && r.name)
    .map((r) => `${enc(r.name)}${r.value !== '' ? `=${enc(r.value)}` : ''}`)
    .join('&');
}
export function displayUrl(spec: HttpRequestSpec): string {
  const q = queryString(spec.query);
  return q ? `${spec.url}${spec.url.includes('?') ? '&' : '?'}${q}` : spec.url;
}
/** a typed URL → base + query rows (disabled rows are kept at the end) */
export function applyUrl(spec: HttpRequestSpec, text: string): void {
  const i = text.indexOf('?');
  spec.url = i < 0 ? text : text.slice(0, i);
  const rows = i < 0 ? [] : parseQuery(text.slice(i + 1));
  spec.query = [...rows, ...spec.query.filter((r) => !r.enabled)];
}

/** the base URL of the connection, as text (placeholders kept) */
export function connBase(c: DevToolConnection, info: DevToolsInfo): string {
  const t = c.target;
  const sch = c.options.scheme ?? 'http';
  let b = '';
  if (t.kind === 'url') b = t.url;
  else if (t.kind === 'container') {
    const p = info.containers.find((x) => x.containerSlug === t.containerSlug)?.ports.find((x) => x.portName === t.portName);
    b = `${sch}://${t.containerSlug}:${p?.containerPort ?? '?'}`;
  } else if (t.kind === 'host') b = `${sch}://${t.host}:${t.port}`;
  return b.replace(/\/$/, '') + (c.options.basePath ?? '');
}
export function fullUrl(spec: HttpRequestSpec, base: string): string {
  const u = displayUrl(spec);
  return /^https?:\/\//i.test(u) ? u : base + (u.startsWith('/') || !u ? u : `/${u}`);
}
const sq = (s: string) => `'${s.replace(/'/g, `'\\''`)}'`;
/** curl command of the request as written (placeholders unresolved, connection auth as a placeholder) */
export function toCurl(spec: HttpRequestSpec, c: DevToolConnection, info: DevToolsInfo): string {
  const parts = ['curl'];
  if (spec.method !== 'GET') parts.push('-X', spec.method);
  if (spec.followRedirects ?? c.options.followRedirects ?? true) parts.push('-L');
  if (spec.tlsInsecure ?? c.options.tlsInsecure) parts.push('-k');
  if (spec.timeoutSec) parts.push('--max-time', String(spec.timeoutSec));
  parts.push(sq(fullUrl(spec, connBase(c, info))));
  for (const h of c.options.defaultHeaders) parts.push('-H', sq(`${h.name}: ${h.value}`));
  for (const h of spec.headers) if (h.enabled && h.name) parts.push('-H', sq(`${h.name}: ${h.value}`));
  const a = spec.auth;
  const credText = (k: 'password' | 'token') => {
    const cr = c.auth[k];
    return cr.kind === 'expr' ? cr.expr : cr.kind === 'literal' ? `{{ connection.${k} }}` : '';
  };
  if (a ? a.scheme === 'basic' : c.auth.httpScheme === 'basic') parts.push('-u', sq(`${a ? (a.username ?? '') : (c.auth.username ?? '')}:${a ? (a.password ?? '') : credText('password')}`));
  if (a ? a.scheme === 'bearer' : c.auth.httpScheme === 'bearer') parts.push('-H', sq(`Authorization: Bearer ${a ? (a.token ?? '') : credText('token')}`));
  if (a ? a.scheme === 'api-key' && a.apiKeyIn === 'header' : c.auth.httpScheme === 'api-key') parts.push('-H', sq(`${(a ? a.apiKeyHeader : c.auth.apiKeyHeader) ?? 'X-API-Key'}: ${a ? (a.token ?? '') : credText('token')}`));
  const b = spec.body;
  if (b.kind === 'json') {
    if (!spec.headers.some((h) => h.enabled && /^content-type$/i.test(h.name))) parts.push('-H', sq('Content-Type: application/json'));
    parts.push('--data-raw', sq(b.text));
  } else if (b.kind === 'text') {
    if (b.contentType) parts.push('-H', sq(`Content-Type: ${b.contentType}`));
    parts.push('--data-raw', sq(b.text));
  } else if (b.kind === 'form') for (const f of b.fields) f.enabled && parts.push('--data-urlencode', sq(`${f.name}=${f.value}`));
  else if (b.kind === 'multipart') for (const f of b.fields) f.enabled && parts.push('-F', sq(f.fileName ? `${f.name}=@${f.fileName}` : `${f.name}=${f.value}`));
  else if (b.kind === 'binary') parts.push('--data-binary', '@file.bin');
  return parts.join(' ').replace(/ (-[A-Za-z]|--[a-z-]+) /g, ' \\\n  $1 ');
}
/** a history row (redacted "METHOD url\nHeader: v…") → a request */
export function fromHistory(statement: string): HttpRequestSpec {
  const lines = statement.split('\n');
  const m = /^([A-Z]+)\s+(\S+)/.exec(lines[0] ?? '');
  const spec = blankRequest();
  if (m) {
    spec.method = (METHODS as string[]).includes(m[1]!) ? (m[1] as HttpRequestSpec['method']) : 'GET';
    applyUrl(spec, m[2]!);
  }
  for (const l of lines.slice(1)) {
    const i = l.indexOf(':');
    if (i > 0 && !l.startsWith('(body')) spec.headers.push({ name: l.slice(0, i).trim(), value: l.slice(i + 1).trim(), enabled: !/••••••/.test(l) });
  }
  return spec;
}
export function statusTone(s: number | null): string {
  if (s === null) return 'red';
  if (s < 300) return 'green';
  if (s < 400) return 'blue';
  if (s < 500) return 'amber';
  return 'red';
}
export function b64ToBytes(s: string): Uint8Array {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}
export function bytesToB64(b: Uint8Array): string {
  let s = '';
  for (let i = 0; i < b.length; i += 0x8000) s += String.fromCharCode(...b.subarray(i, i + 0x8000));
  return btoa(s);
}
