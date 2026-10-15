/**
 * The edge request resolver ("Test a URL"). It answers how the agent edge (embedded Caddy + OPM handlers) treats
 * one request. Normative: SPEC-final.md §12.1 (round E: nginx location semantics). The Go edge (agent
 * internal/edgeres + the Caddy modules) must produce the same decisions; shared vectors: testdata/edge-vectors.json.
 *
 * The edge given here must already have its expressions resolved (hostnames, redirect.to, header values).
 */
import type { Edge, EdgeHost, EdgeRoute, EdgeProxyAction, EdgeResponseHeaders, EdgeMatchKind } from './types.js';

// ───────────────────────────────────────────────────────────── constants
export const EDGE_DEFAULTS = {
  connectTimeoutSec: 60,
  sendTimeoutSec: 60,
  readTimeoutSec: 60,
  /** internal rewrites per request (nginx: more than 10 cycles → 500) */
  maxRewrites: 10,
  /** requestBuffering true: bytes buffered in memory before the body is sent upstream (the rest streams) */
  requestBufferBytes: 8 * 1024 * 1024,
  /** largest maxBodyBytes (1 TiB) */
  maxBodyLimit: 1024 * 1024 * 1024 * 1024,
  maxTimeoutSec: 86400,
  maxConnectTimeoutSec: 3600,
} as const;
/** Statuses nginx add_header (without "always") applies to. */
export const ADD_HEADER_STATUSES = [200, 201, 204, 206, 301, 302, 303, 304, 307, 308] as const;
/** Request headers the edge sets on every proxied request (a route's setHeaders / removeHeaders may override them). */
export const EDGE_DEFAULT_REQUEST_HEADERS = ['Host', 'X-Real-IP', 'X-Forwarded-For', 'X-Forwarded-Proto', 'X-Forwarded-Host'] as const;
/** Hop-by-hop headers Caddy manages itself (websocket upgrade, keep-alive); setting them is ignored with a warning. */
export const HOP_BY_HOP_HEADERS = ['Connection', 'Upgrade', 'Keep-Alive', 'Transfer-Encoding', 'TE', 'Trailer', 'Proxy-Connection'] as const;

export function emptyResponseHeaders(): EdgeResponseHeaders {
  return { set: {}, add: {}, remove: [], always: [] };
}
export function isEmptyResponseHeaders(h: EdgeResponseHeaders | null | undefined): boolean {
  return !h || (Object.keys(h.set ?? {}).length === 0 && Object.keys(h.add ?? {}).length === 0 && (h.remove ?? []).length === 0);
}

// ───────────────────────────────────────────────────────────── regular expressions (RE2 ∩ JS)
/**
 * Compile a route / rewrite regex. The syntax is the subset RE2 (Go regexp, used by the agent) and JavaScript (used by
 * the GUIs) both understand; a leading "(?i)" makes it case-insensitive (nginx `~*`). Throws on a bad pattern.
 */
export function compileEdgeRegex(src: string): RegExp {
  const err = checkEdgeRegex(src);
  if (err) throw new Error(err);
  return compileRaw(src);
}
function compileRaw(src: string): RegExp {
  return src.startsWith('(?i)') ? new RegExp(src.slice(4), 'i') : new RegExp(src);
}
/** null when the pattern is valid in RE2 and JS with the same meaning; else a message for the user. */
export function checkEdgeRegex(src: string): string | null {
  if (typeof src !== 'string' || src === '') return 'the regex is empty';
  if (src.length > 1024) return 'the regex is longer than 1024 characters';
  const body = src.startsWith('(?i)') ? src.slice(4) : src;
  let inClass = false;
  for (let i = 0; i < body.length; i++) {
    const c = body[i]!;
    if (c === '\\') {
      const n = body[i + 1] ?? '';
      if (/[1-9]/.test(n)) return `backreferences (\\${n}) are not supported (RE2 has none)`;
      if (n === 'k') return 'named backreferences (\\k<…>) are not supported (RE2 has none)';
      if ('AzZQEGhHRXCKNVoe'.includes(n) && n !== '') return `\\${n} is not supported by both RE2 and JavaScript: write it another way (e.g. ^ $ instead of \\A \\z)`;
      if (n === 'p' || n === 'P') return '\\p{…} Unicode classes are not supported here (JavaScript needs Unicode mode)';
      if (n === 'x' && body[i + 2] === '{') return '\\x{…} is not supported by JavaScript; use \\xHH or the character itself';
      if (n === 'u') return '\\uHHHH is not supported by RE2; use the character itself';
      if (n === 'c') return '\\c control escapes are not supported by RE2';
      if (n === '0') return '\\0 is not supported by RE2; use \\x00';
      i++;
      continue;
    }
    if (inClass) {
      if (c === ']') inClass = false;
      continue;
    }
    if (c === '[') {
      if (body.startsWith('[[:', i) || body.startsWith('[^[:', i) || body.startsWith('[:', i + 1)) return 'POSIX classes like [[:alpha:]] are not supported by JavaScript; use [a-zA-Z]';
      inClass = true;
      if (body[i + 1] === '^') i++;
      if (body[i + 1] === ']') i++; // "[]…]" literal ]
      continue;
    }
    if (c === '(' && body[i + 1] === '?') {
      const rest = body.slice(i + 2);
      if (rest.startsWith('=') || rest.startsWith('!')) return 'lookahead (?= / (?! is not supported (RE2 has no lookaround)';
      if (rest.startsWith('<=') || rest.startsWith('<!')) return 'lookbehind (?<= / (?<! is not supported (RE2 has no lookaround)';
      if (rest.startsWith('>')) return 'atomic groups (?> are not supported (RE2)';
      if (rest.startsWith('P<')) return 'write named groups as (?<name>…) (JavaScript does not know (?P<name>…))';
      if (rest.startsWith('<') || rest.startsWith(':')) continue;
      return 'inline flags are not supported except a leading (?i) for the whole pattern';
    }
    if ((c === '+' || c === '*' || c === '?' || c === '}') && body[i + 1] === '+') return 'possessive quantifiers (++ *+ ?+) are not supported (RE2)';
  }
  try {
    compileRaw(src);
  } catch (e) {
    return `not a valid regex: ${(e as Error).message.replace(/^Invalid regular expression: /, '')}`;
  }
  return null;
}
function execRegex(src: string, s: string): string[] | null {
  let re: RegExp;
  try {
    re = compileRaw(src);
  } catch {
    return null;
  }
  const m = re.exec(s);
  if (!m) return null;
  return m.map((x) => x ?? '');
}

// ───────────────────────────────────────────────────────────── variables (SPEC §12.6)
export interface EdgeVars {
  /** current path (decoded; after internal rewrites) */
  uri: string;
  /** current query without "?" */
  args: string;
  /** original path + query as the client sent them (before rewrites, not decoded) */
  requestUri: string;
  /** request host without port */
  host: string;
  scheme: string;
  /** regex captures: [0] whole match, [1]… groups */
  captures: string[];
  remoteAddr?: string;
  serverPort?: string;
  /** request headers (lower-case names) for $http_<name> */
  headers?: Record<string, string>;
}
/** Names accepted after "$" (besides $1…$9 and $http_<name>). Unknown "$name" stays literal. */
export const EDGE_VARIABLES = ['uri', 'args', 'query_string', 'is_args', 'request_uri', 'host', 'scheme', 'remote_addr', 'server_port'] as const;
/**
 * Expand nginx-style variables and the OPM placeholders {path} {query} {host}:
 *   $uri / {path}  current path · $args $query_string · $is_args ("?" or "") · {query} ("?args" or "") · $request_uri ·
 *   $host / {host} · $scheme · $remote_addr · $server_port · $http_<name> · $1 … $9 (captures, missing = "") ·
 *   ${name} = $name. Anything else after "$" is kept as written.
 */
export function expandEdgeVars(t: string, v: EdgeVars): string {
  const named = (name: string): string | null => {
    switch (name) {
      case 'uri':
        return v.uri;
      case 'args':
      case 'query_string':
        return v.args;
      case 'is_args':
        return v.args ? '?' : '';
      case 'request_uri':
        return v.requestUri;
      case 'host':
        return v.host;
      case 'scheme':
        return v.scheme;
      case 'remote_addr':
        return v.remoteAddr ?? '{client-ip}';
      case 'server_port':
        return v.serverPort ?? '';
    }
    if (name.startsWith('http_') && name.length > 5) {
      const h = name.slice(5).replace(/_/g, '-').toLowerCase();
      return v.headers?.[h] ?? `{header ${h}}`;
    }
    return null;
  };
  return t.replace(/\$([1-9])|\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)|\{(path|query|host)\}/g, (all, d, br, nm, ph) => {
    if (d) return v.captures[Number(d)] ?? '';
    if (ph) return ph === 'path' ? v.uri : ph === 'query' ? (v.args ? '?' + v.args : '') : v.host;
    const r = named(br ?? nm);
    return r === null ? all : r;
  });
}

// ───────────────────────────────────────────────────────────── types
export type HostMatch = 'exact' | 'wildcard' | 'any';
export interface EdgeResolveOptions {
  method?: string; //                                   default GET
  /** containerSlug → { portName → container port } (from the image ports, expressions resolved) */
  ports?: Record<string, Record<string, number>>;
  /** unused (kept for compatibility): containers are keyed by containerSlug */
  slugs?: Record<string, string>;
  /** containerSlugs whose container is not "completed" (→ 503 not-found page) */
  notReady?: Record<string, boolean>;
  /** request body size in bytes (→ 413 when above the effective maxBodyBytes) */
  bodyBytes?: number;
  /** the request asks for a WebSocket upgrade (→ 400 when the route has websocket false) */
  upgrade?: boolean;
}
export type EdgeResolutionKind =
  | 'proxy'
  | 'redirect'
  | 'agent'
  | 'not-found'
  | 'not-ready'
  | 'tls-fail'
  | 'bad-url'
  | 'not-edge-port'
  | 'rewrite-loop'
  | 'too-large'
  | 'ws-refused';
export type EdgeOrderGroup = 'exact' | 'prefix-stop' | 'regex' | 'prefix';
export interface EdgeOrderEntry {
  routeId: string;
  group: EdgeOrderGroup;
  /** 1-based position in the effective order */
  rank: number;
  why: string;
}
export interface EdgeRewriteStep {
  routeId: string;
  from: string; // path + query before
  to: string; //   path + query after
}
export interface EdgeTrace {
  match: { kind: EdgeMatchKind; path: string; why: string };
  /** how the upstream path was made (proxy only) */
  upstreamPath: { how: 'none' | 'strip' | 'rewrite'; from: string; to: string } | null;
  /** request headers sent upstream (defaults, then the route's setHeaders) */
  requestHeaders: { name: string; value: string; source: 'default' | 'route' }[];
  removedRequestHeaders: string[];
  /** effective response headers (route's, else the host's) */
  responseHeaders: EdgeResponseHeaders;
  responseHeadersFrom: 'route' | 'host' | 'none';
  timeouts: { connectSec: number; sendSec: number; readSec: number } | null;
  buffering: { request: boolean; response: boolean } | null;
  /** effective request body limit; null = unlimited */
  maxBodyBytes: number | null;
  maxBodyFrom: 'route' | 'edge';
  websocket: boolean | null;
}
export interface EdgeResolution {
  /** HTTP status the client gets; 0 = no HTTP response (bad URL, wrong port, TLS handshake failure) */
  status: number;
  kind: EdgeResolutionKind;
  title: string;
  host: EdgeHost | null;
  hostMatch: HostMatch | null;
  route: EdgeRoute | null;
  /** redirect target (308 HTTP→HTTPS, a redirect route, or the nginx auto-redirect 301 "…/") */
  location: string | null;
  upstream: { containerSlug: string; portName: string; containerPort: number | null; path: string; url: string } | null;
  /** agent GUI page: "gui" (known path) or "agent-404" */
  agent: 'gui' | 'agent-404' | null;
  steps: string[];
  /** internal rewrites (rewrite routes) in order */
  rewrites: EdgeRewriteStep[];
  /** captures of the final route's regex ([] for prefix / exact) */
  captures: string[];
  /** nginx auto-redirect: "/x" → 301 "/x/" because proxy prefix route "/x/" exists */
  autoRedirect: boolean;
  trace: EdgeTrace | null;
  /** the effective evaluation order of this host's routes (for the method) */
  order: EdgeOrderEntry[];
}

// ───────────────────────────────────────────────────────────── matching
/** Plain string prefix (nginx): "/api" matches "/api", "/api/x" AND "/apix"; "/api/" does not match "/api". */
export function prefixHit(prefix: string, path: string): boolean {
  return path.startsWith(prefix);
}

/** Host selection: exact (case-insensitive) > wildcard "*.x.y" (exactly one extra label) > "*". */
export function matchHost(edge: Edge, hostname: string): { host: EdgeHost; how: HostMatch } | null {
  const hn = hostname.toLowerCase();
  const exact = edge.hosts.find((h) => h.hostname.toLowerCase() === hn);
  if (exact) return { host: exact, how: 'exact' };
  const wild = edge.hosts.find((h) => {
    const n = h.hostname.toLowerCase();
    if (!n.startsWith('*.')) return false;
    const suffix = n.slice(1); // ".x.y"
    return hn.endsWith(suffix) && hn.split('.').length === n.split('.').length && hn.length > suffix.length;
  });
  if (wild) return { host: wild, how: 'wildcard' };
  const any = edge.hosts.find((h) => h.hostname === '*');
  return any ? { host: any, how: 'any' } : null;
}

export function methodHit(r: EdgeRoute, method: string): boolean {
  const m = r.match.methods;
  return !m || m.length === 0 || m.map((x) => x.toUpperCase()).includes(method.toUpperCase());
}

const byOrder = (a: EdgeRoute, b: EdgeRoute) => a.order - b.order || (a.routeId < b.routeId ? -1 : a.routeId > b.routeId ? 1 : 0);

/** Routes that apply to a host (route.hosts [] = all), in effective order. */
export function routesForHost(edge: Edge, host: EdgeHost): EdgeRoute[] {
  const rs = edge.routes.filter((r) => r.hosts.length === 0 || r.hosts.some((x) => x.toLowerCase() === host.hostname.toLowerCase()));
  const ord = effectiveRouteOrder(rs);
  return ord.map((o) => rs.find((r) => r.routeId === o.routeId)!);
}

/**
 * Effective evaluation order (what the GUIs show, and the order of the Caddy routes): exact routes, then prefix routes
 * marked stopRegex (longest first), then regex routes (by order), then prefix routes (longest first). The decision itself
 * is selectRoute (nginx): exact → longest prefix → regexes unless that prefix is stopRegex → longest prefix.
 */
export function effectiveRouteOrder(routes: readonly EdgeRoute[]): EdgeOrderEntry[] {
  const kind = (r: EdgeRoute) => r.match.kind ?? 'prefix';
  const byLen = (a: EdgeRoute, b: EdgeRoute) => b.match.path.length - a.match.path.length || byOrder(a, b);
  const exact = routes.filter((r) => kind(r) === 'exact').sort((a, b) => (a.match.path < b.match.path ? -1 : a.match.path > b.match.path ? 1 : byOrder(a, b)));
  const stop = routes.filter((r) => kind(r) === 'prefix' && r.match.stopRegex).sort(byLen);
  const regex = routes.filter((r) => kind(r) === 'regex').sort(byOrder);
  const prefix = routes.filter((r) => kind(r) === 'prefix' && !r.match.stopRegex).sort(byLen);
  const out: EdgeOrderEntry[] = [];
  const push = (r: EdgeRoute, group: EdgeOrderGroup, why: string) => out.push({ routeId: r.routeId, group, rank: out.length + 1, why });
  for (const r of exact) push(r, 'exact', `Exact match: wins whenever the path is exactly ${r.match.path} (checked first).`);
  for (const r of stop)
    push(r, 'prefix-stop', `Prefix ${r.match.path} with "stop regex" (nginx ^~): when it is the LONGEST matching prefix, regex routes are skipped and it wins.`);
  regex.forEach((r, i) =>
    push(r, 'regex', `Regex #${i + 1} (order ${r.order}): checked after the longest prefix is found; the first regex that matches wins (unless that prefix is "stop regex").`),
  );
  for (const r of prefix)
    push(r, 'prefix', `Prefix ${r.match.path}: the LONGEST matching prefix wins when no regex matches${r.match.path === '/' ? ' (catch-all)' : ''}.`);
  return out;
}

export interface RouteSelection {
  route: EdgeRoute;
  captures: string[];
  why: string;
  /** nginx auto-redirect to path + "/" (route = the proxy prefix route "path/") */
  autoRedirect: boolean;
}
/**
 * nginx location selection over the given routes (already filtered to the host). Routes whose methods do not contain the
 * method are invisible. 1) exact; 2) longest prefix (ties: order, routeId); nginx auto-redirect: when no prefix route equals
 * the path and a PROXY prefix route equals path + "/", answer 301 path + "/" (regex routes not checked); 3) if the longest
 * prefix is stopRegex → it; 4) regex routes by order, first match; 5) the longest prefix; else none.
 */
export function selectRoute(routes: readonly EdgeRoute[], path: string, method = 'GET'): RouteSelection | null {
  const cand = routes.filter((r) => methodHit(r, method));
  const kind = (r: EdgeRoute) => r.match.kind ?? 'prefix';
  const exact = cand.filter((r) => kind(r) === 'exact' && r.match.path === path).sort(byOrder)[0];
  if (exact) return { route: exact, captures: [], why: `exact match ${exact.match.path}`, autoRedirect: false };
  const prefixes = cand.filter((r) => kind(r) === 'prefix' && prefixHit(r.match.path, path)).sort((a, b) => b.match.path.length - a.match.path.length || byOrder(a, b));
  const longest = prefixes[0];
  if (!longest || longest.match.path.length < path.length) {
    const auto = cand
      .filter((r) => kind(r) === 'prefix' && r.action.type === 'proxy' && r.match.path.length > 1 && r.match.path === path + '/')
      .sort(byOrder)[0];
    if (auto) return { route: auto, captures: [], why: `nginx auto-redirect: ${path} → ${path}/ (proxy prefix ${auto.match.path})`, autoRedirect: true };
  }
  if (longest?.match.stopRegex) return { route: longest, captures: [], why: `longest prefix ${longest.match.path} has "stop regex" (^~): regex routes skipped`, autoRedirect: false };
  for (const r of cand.filter((x) => kind(x) === 'regex').sort(byOrder)) {
    const m = execRegex(r.match.path, path);
    if (m) return { route: r, captures: m, why: `regex ${r.match.path} matches${longest ? ` (wins over the longest prefix ${longest.match.path})` : ''}`, autoRedirect: false };
  }
  if (longest) return { route: longest, captures: [], why: `longest matching prefix ${longest.match.path}`, autoRedirect: false };
  return null;
}

// ───────────────────────────────────────────────────────────── paths
/** Path after the matched prefix ("" when the path equals the prefix). For prefix "/" it is the full path. */
export function restAfterPrefix(prefix: string, path: string): string {
  if (prefix === '/') return path;
  return path.slice(prefix.replace(/\/$/, '').length);
}

/** Split "path?query" (a rewrite result) into its parts; no "?" → query null (unchanged). */
export function splitPathQuery(s: string): { path: string; query: string | null } {
  const i = s.indexOf('?');
  const p = i < 0 ? s : s.slice(0, i);
  return { path: p.startsWith('/') ? p : '/' + p, query: i < 0 ? null : s.slice(i + 1) };
}

/**
 * Upstream path + query of a proxy route. stripPrefix: the rest after the matched path (prefix / exact), ensured to start
 * with "/". rewrite: regex against the path; on a match the replacement (captures + variables) becomes the path (a "?" in
 * it replaces the query); no match → unchanged (nginx). Then addPrefix (trailing "/" removed) is prepended.
 */
export function upstreamPathQuery(
  a: Pick<EdgeProxyAction, 'stripPrefix' | 'addPrefix'> & { rewrite?: EdgeProxyAction['rewrite'] },
  matched: string,
  path: string,
  vars: EdgeVars,
): { path: string; args: string; how: 'none' | 'strip' | 'rewrite' } {
  let up = path;
  let args = vars.args;
  let how: 'none' | 'strip' | 'rewrite' = 'none';
  if (a.rewrite) {
    const m = execRegex(a.rewrite.regex, path);
    if (m) {
      const sp = splitPathQuery(expandEdgeVars(a.rewrite.replacement, { ...vars, uri: path, captures: m }));
      up = sp.path;
      if (sp.query !== null) args = sp.query;
      how = 'rewrite';
    }
  } else if (a.stripPrefix) {
    const rest = restAfterPrefix(matched, path);
    up = rest.startsWith('/') ? rest : '/' + rest;
    how = 'strip';
  }
  if (a.addPrefix) up = a.addPrefix.replace(/\/$/, '') + up;
  return { path: up, args, how };
}
/** Legacy helper (prefix routes): upstream path without query after stripPrefix / addPrefix. */
export function upstreamPath(a: Pick<EdgeProxyAction, 'stripPrefix' | 'addPrefix'>, prefix: string, path: string): string {
  return upstreamPathQuery(a, prefix, path, { uri: path, args: '', requestUri: path, host: '', scheme: 'http', captures: [] }).path;
}

/**
 * Redirect target: variables + placeholders (expandEdgeVars); with keepPath (prefix routes), strip a trailing "/" from the
 * result and append the rest of the path after the prefix (unless it is "/") and "?query".
 */
export function redirectLocation(to: string, keepPath: boolean, prefix: string, path: string, query: string, host: string, extra: Partial<EdgeVars> = {}): string {
  const args = query.startsWith('?') ? query.slice(1) : query;
  let loc = expandEdgeVars(to, { uri: path, args, requestUri: path + (args ? '?' + args : ''), host, scheme: 'http', captures: [], ...extra });
  if (keepPath) {
    const rest = restAfterPrefix(prefix, path);
    loc = loc.replace(/\/$/, '') + (rest === '/' ? '' : rest) + (args ? '?' + args : '');
  }
  return loc;
}

/** Known agent GUI / API paths under /_opm on the edge (everything else → agent 404; /_opm/peer/ is never on the edge). */
export function isKnownAgentPath(path: string): boolean {
  if (path === '/_opm' || path === '/_opm/') return true;
  if (path.startsWith('/_opm/peer/') || path === '/_opm/peer') return false;
  return /^\/_opm\/(app|login|install|assets)(\/|$)/.test(path) || path.startsWith('/_opm/api/') || path === '/_opm/healthz';
}

function safeDecode(p: string): string {
  try {
    return decodeURIComponent(p);
  } catch {
    return p;
  }
}

/** Effective response headers of a route on a host (nginx: a route's own list REPLACES the host's). */
export function effectiveResponseHeaders(route: EdgeRoute | null, host: EdgeHost | null): { headers: EdgeResponseHeaders; from: 'route' | 'host' | 'none' } {
  if (route?.responseHeaders) return { headers: route.responseHeaders, from: 'route' };
  if (host?.responseHeaders && !isEmptyResponseHeaders(host.responseHeaders)) return { headers: host.responseHeaders, from: 'host' };
  return { headers: emptyResponseHeaders(), from: 'none' };
}
/** Effective body limit of a proxy action: route value (0 = unlimited) else the edge's; null = unlimited. */
export function effectiveMaxBody(edge: Pick<Edge, 'maxBodyBytes'>, a: Pick<EdgeProxyAction, 'maxBodyBytes'>): { bytes: number | null; from: 'route' | 'edge' } {
  if (a.maxBodyBytes !== null && a.maxBodyBytes !== undefined) return { bytes: a.maxBodyBytes === 0 ? null : a.maxBodyBytes, from: 'route' };
  const e = edge.maxBodyBytes ?? null;
  return { bytes: e === 0 ? null : e, from: 'edge' };
}

export function formatBytes(n: number | null): string {
  if (n === null) return 'unlimited';
  const u = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let i = 0;
  let x = n;
  while (x >= 1024 && i < u.length - 1 && x % 1024 === 0) {
    x /= 1024;
    i++;
  }
  return `${x} ${u[i]}`;
}

// ───────────────────────────────────────────────────────────── resolve
export function resolveEdgeRequest(edge: Edge, rawUrl: string, opts: EdgeResolveOptions = {}): EdgeResolution {
  const method = (opts.method ?? 'GET').toUpperCase();
  const base = { host: null, hostMatch: null, route: null, location: null, upstream: null, agent: null, rewrites: [], captures: [], autoRedirect: false, trace: null, order: [] } as const;
  const steps: string[] = [];
  let u: URL;
  try {
    u = new URL(/^[a-z][a-z0-9+.-]*:\/\//i.test(rawUrl) ? rawUrl : 'http://' + rawUrl);
  } catch {
    return { ...base, rewrites: [], captures: [], order: [], status: 0, kind: 'bad-url', title: 'Not a URL', steps: ['Enter for example https://plant.acme.com/api/v1/x'] };
  }
  const scheme = u.protocol.slice(0, -1);
  if (scheme !== 'http' && scheme !== 'https') return { ...base, rewrites: [], captures: [], order: [], status: 0, kind: 'bad-url', title: 'Only http and https', steps: [] };
  const hn = u.hostname.replace(/^\[|\]$/g, '').toLowerCase();
  const rawPath = u.pathname || '/';
  const rawQuery = u.search; // "?a=b" or ""
  const port = u.port ? Number(u.port) : scheme === 'https' ? 443 : 80;
  const want = scheme === 'https' ? edge.httpsPort : edge.httpPort;
  if (port !== want) {
    return {
      ...base,
      rewrites: [],
      captures: [],
      order: [],
      status: 0,
      kind: 'not-edge-port',
      title: `Port ${port} is not an edge port`,
      steps: [`The edge listens on http ${edge.httpPort} and https ${edge.httpsPort ?? 'off'}.`, 'agentPort and agentGuiAliasPort serve only /_opm/ and do not use these rules.'],
    };
  }
  const m = matchHost(edge, hn);
  if (!m) {
    return {
      ...base,
      rewrites: [],
      captures: [],
      order: [],
      status: scheme === 'https' ? 0 : 404,
      kind: scheme === 'https' ? 'tls-fail' : 'not-found',
      title: scheme === 'https' ? 'TLS handshake fails' : '404 · site not-found page',
      steps: [`No entry in edge.hosts matches ${hn} and there is no "*" host.`, scheme === 'https' ? 'There is no certificate for this name.' : 'The edge answers with the site not-found page.'],
    };
  }
  const { host, how } = m;
  const at = { ...base, rewrites: [] as EdgeRewriteStep[], captures: [] as string[], order: [] as EdgeOrderEntry[], host, hostMatch: how };
  steps.push(`Host ${hn} → ${host.hostname} (${how}) · TLS ${host.tls.mode}`);
  if (scheme === 'https' && host.tls.mode === 'none') {
    return { ...at, status: 0, kind: 'tls-fail', title: 'TLS handshake fails', steps: steps.concat(['TLS mode is none for this host, so the edge has no certificate for HTTPS.']) };
  }
  if (scheme === 'http' && host.redirectHttpToHttps && host.tls.mode !== 'none' && edge.httpsPort != null) {
    const loc = `https://${u.hostname}${edge.httpsPort === 443 ? '' : ':' + edge.httpsPort}${rawPath}${rawQuery}`;
    return { ...at, status: 308, kind: 'redirect', title: '308 → HTTPS', location: loc, steps: steps.concat(['redirectHttpToHttps is on for this host. Routes are not checked on HTTP.']) };
  }
  if (rawPath === '/_opm' || rawPath.startsWith('/_opm/')) {
    const known = isKnownAgentPath(rawPath);
    return {
      ...at,
      status: known ? 200 : 404,
      kind: 'agent',
      agent: known ? 'gui' : 'agent-404',
      title: known ? '200 · agent GUI / API' : '404 · agent 404 page',
      steps: steps.concat(['/_opm/ is reserved on every host. Routes are not checked.', known ? 'The agent answers (in process, not a container).' : 'Unknown /_opm/ path → the AGENT 404 page (never the site page). /_opm/peer/ is only on agentPort.']),
    };
  }
  const hostRoutes = edge.routes.filter((r) => r.hosts.length === 0 || r.hosts.some((x) => x.toLowerCase() === host.hostname.toLowerCase()));
  const order = effectiveRouteOrder(hostRoutes.filter((r) => methodHit(r, method)));
  at.order = order;
  steps.push(`Routes for this host (${method}), effective order: ${order.map((o) => o.routeId).join(' → ') || 'none'}`);

  const vars: EdgeVars = { uri: safeDecode(rawPath), args: rawQuery.replace(/^\?/, ''), requestUri: rawPath + rawQuery, host: hn, scheme, captures: [] };
  const rewrites: EdgeRewriteStep[] = [];
  const pq = () => vars.uri + (vars.args ? '?' + vars.args : '');
  let sel: RouteSelection | null = null;
  for (;;) {
    sel = selectRoute(hostRoutes, vars.uri, method);
    if (!sel) {
      return {
        ...at,
        rewrites,
        status: 404,
        kind: 'not-found',
        title: '404 · site not-found page',
        steps: steps.concat([`No route matches ${vars.uri}. The edge serves the site not-found page (notFoundPageId; built-in page if null).`]),
      };
    }
    const r = sel.route;
    if (sel.autoRedirect) {
      const loc = vars.uri + '/' + (vars.args ? '?' + vars.args : '');
      steps.push(`No route for ${vars.uri}, but proxy route ${r.routeId} has prefix ${r.match.path}: nginx answers 301 with a trailing "/".`);
      return { ...at, rewrites, route: r, status: 301, kind: 'redirect', title: '301 · add trailing /', location: loc, autoRedirect: true, steps, trace: null };
    }
    steps.push(`Match: route ${r.routeId} — ${sel.why}`);
    if (r.action.type !== 'rewrite') break;
    if (rewrites.length >= EDGE_DEFAULTS.maxRewrites) {
      return {
        ...at,
        rewrites,
        route: r,
        status: 500,
        kind: 'rewrite-loop',
        title: '500 · rewrite loop',
        steps: steps.concat([`More than ${EDGE_DEFAULTS.maxRewrites} internal rewrites: the edge answers 500 (nginx does the same).`]),
      };
    }
    const mm = execRegex(r.action.regex, vars.uri);
    if (!mm) {
      return {
        ...at,
        rewrites,
        route: r,
        status: 404,
        kind: 'not-found',
        title: '404 · site not-found page',
        steps: steps.concat([`The rewrite regex ${r.action.regex} does not match ${vars.uri}: not-found page.`]),
      };
    }
    const from = pq();
    const sp = splitPathQuery(expandEdgeVars(r.action.replacement, { ...vars, captures: mm }));
    vars.uri = sp.path;
    if (sp.query !== null) vars.args = sp.query;
    rewrites.push({ routeId: r.routeId, from, to: pq() });
    steps.push(`Rewrite (internal, like nginx "last"): ${from} → ${pq()}; the routes are matched again.`);
  }
  const r = sel.route;
  const a = r.action;
  const captures = sel.captures;
  const rh = effectiveResponseHeaders(r, host);
  const traceBase = {
    match: { kind: r.match.kind ?? 'prefix', path: r.match.path, why: sel.why },
    responseHeaders: rh.headers,
    responseHeadersFrom: rh.from,
  };
  if (a.type === 'redirect') {
    const loc = redirectLocation(a.to, a.keepPath, r.match.path, vars.uri, vars.args ? '?' + vars.args : '', hn, { captures, requestUri: vars.requestUri, scheme, args: vars.args });
    return {
      ...at,
      rewrites,
      captures,
      route: r,
      status: a.status,
      kind: 'redirect',
      title: `${a.status} redirect`,
      location: loc,
      steps: steps.concat([a.keepPath ? `keepPath: the rest of the path (${restAfterPrefix(r.match.path, vars.uri) || '/'}) and the query are added.` : `Location ${loc}`]),
      trace: {
        ...traceBase,
        upstreamPath: null,
        requestHeaders: [],
        removedRequestHeaders: [],
        timeouts: null,
        buffering: null,
        maxBodyBytes: null,
        maxBodyFrom: 'edge',
        websocket: null,
      },
    };
  }
  if (a.type !== 'proxy') throw new Error('unreachable');
  const mb = effectiveMaxBody(edge, a);
  const reqHeaders: EdgeTrace['requestHeaders'] = [
    { name: 'Host', value: u.host, source: 'default' },
    { name: 'X-Real-IP', value: '{client-ip}', source: 'default' },
    { name: 'X-Forwarded-For', value: '{client-ip}', source: 'default' },
    { name: 'X-Forwarded-Proto', value: scheme, source: 'default' },
    { name: 'X-Forwarded-Host', value: u.host, source: 'default' },
  ];
  for (const [k, v] of Object.entries(a.setHeaders || {})) {
    if ((HOP_BY_HOP_HEADERS as readonly string[]).some((x) => x.toLowerCase() === k.toLowerCase())) continue;
    const val = expandEdgeVars(v, { ...vars, captures: [] });
    const i = reqHeaders.findIndex((x) => x.name.toLowerCase() === k.toLowerCase());
    if (i >= 0) reqHeaders[i] = { name: k, value: val, source: 'route' };
    else reqHeaders.push({ name: k, value: val, source: 'route' });
  }
  const removed = (a.removeHeaders || []).slice();
  const reqHeadersOut = reqHeaders.filter((h) => !removed.some((x) => x.toLowerCase() === h.name.toLowerCase()));
  const trace: EdgeTrace = {
    ...traceBase,
    upstreamPath: null,
    requestHeaders: reqHeadersOut,
    removedRequestHeaders: removed,
    timeouts: { connectSec: a.connectTimeoutSec, sendSec: a.sendTimeoutSec, readSec: a.readTimeoutSec },
    buffering: { request: a.requestBuffering, response: a.responseBuffering },
    maxBodyBytes: mb.bytes,
    maxBodyFrom: mb.from,
    websocket: a.websocket,
  };
  if (opts.upgrade && !a.websocket) {
    return { ...at, rewrites, captures, route: r, status: 400, kind: 'ws-refused', title: '400 · WebSocket not allowed', trace, steps: steps.concat(['The route has websocket off: an Upgrade: websocket request gets 400.']) };
  }
  if (opts.bodyBytes !== undefined && mb.bytes !== null && opts.bodyBytes > mb.bytes) {
    return {
      ...at,
      rewrites,
      captures,
      route: r,
      status: 413,
      kind: 'too-large',
      title: '413 · body too large',
      trace,
      steps: steps.concat([`The body (${opts.bodyBytes} B) is above the limit ${formatBytes(mb.bytes)} (${mb.from}): 413 before the upstream is contacted.`]),
    };
  }
  if (opts.notReady?.[a.containerSlug]) {
    return {
      ...at,
      rewrites,
      captures,
      route: r,
      status: 503,
      kind: 'not-ready',
      title: '503 · not-found page',
      trace,
      steps: steps.concat([`Container ${a.containerSlug} is not ready. The edge serves the not-found page with status 503.`]),
    };
  }
  const up = upstreamPathQuery(a, r.match.path, vars.uri, { ...vars, captures: [] });
  trace.upstreamPath = { how: up.how, from: vars.uri, to: up.path };
  const upq = up.path + (up.args ? '?' + up.args : '');
  const cport = opts.ports?.[a.containerSlug]?.[a.portName] ?? null;
  const url = `http://${a.containerSlug}:${cport ?? '?'}${upq}`;
  const hdr = reqHeadersOut.filter((h) => h.source === 'route').map((h) => `${h.name}: ${h.value}`);
  const rhs = [
    ...Object.entries(rh.headers.set).map(([k, v]) => `set ${k}: ${v}`),
    ...Object.entries(rh.headers.add).map(([k, v]) => `add ${k}: ${v}`),
    ...rh.headers.remove.map((k) => `remove ${k}`),
  ];
  return {
    ...at,
    rewrites,
    captures,
    route: r,
    status: 200,
    kind: 'proxy',
    title: 'proxy → container',
    upstream: { containerSlug: a.containerSlug, portName: a.portName, containerPort: cport, path: upq, url },
    trace,
    steps: steps.concat([
      up.how === 'strip' ? `stripPrefix: ${vars.uri} → ${up.path}` : up.how === 'rewrite' ? `rewrite ${a.rewrite!.regex} → ${up.path}` : 'The path goes unchanged.',
      `Upstream (site network, DNS = container name): ${url}`,
      `Request headers: Host kept, X-Real-IP, X-Forwarded-For/Proto/Host set${hdr.length ? ` · ${hdr.join(' · ')}` : ''}${removed.length ? ` · removed ${removed.join(', ')}` : ''}`,
      rhs.length ? `Response headers (${rh.from}): ${rhs.join(' · ')}` : 'Response headers: none added.',
      `Timeouts: connect ${a.connectTimeoutSec} s · send ${a.sendTimeoutSec} s · read ${a.readTimeoutSec} s (idle between reads)`,
      `Body: ${a.requestBuffering ? 'buffered (≤ 8 MiB in memory, then streamed)' : 'streamed'} · limit ${formatBytes(mb.bytes)} (${mb.from}) · response ${a.responseBuffering ? 'buffered' : 'flushed at once'}${' (text/event-stream always flushes)'}`,
      a.websocket ? 'WebSocket upgrade allowed.' : 'WebSocket upgrade refused (400).',
    ]),
  };
}

/**
 * The part of a resolution that testdata/edge-vectors.json pins (TS and Go must agree on every field). `steps` and the
 * `why` texts are free text and not compared.
 */
export function edgeVectorView(r: EdgeResolution) {
  return {
    status: r.status,
    kind: r.kind,
    routeId: r.route?.routeId ?? null,
    location: r.location,
    upstreamUrl: r.upstream?.url ?? null,
    rewrites: r.rewrites,
    captures: r.captures,
    autoRedirect: r.autoRedirect,
    order: r.order.map((o) => `${o.group}:${o.routeId}`),
    trace: r.trace
      ? {
          matchKind: r.trace.match.kind,
          upstreamPath: r.trace.upstreamPath,
          requestHeaders: r.trace.requestHeaders,
          removedRequestHeaders: r.trace.removedRequestHeaders,
          responseHeaders: r.trace.responseHeaders,
          responseHeadersFrom: r.trace.responseHeadersFrom,
          timeouts: r.trace.timeouts,
          buffering: r.trace.buffering,
          maxBodyBytes: r.trace.maxBodyBytes,
          maxBodyFrom: r.trace.maxBodyFrom,
          websocket: r.trace.websocket,
        }
      : null,
  };
}
