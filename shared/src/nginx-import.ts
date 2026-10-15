/**
 * nginx.conf → OPM edge importer (round E, SPEC-final §12.7). Pure TS, no I/O. Used by the cloud console and the agent
 * GUI ("Import from nginx.conf…"). Test: nginx-import.test.ts against e2e/fixtures/client-nginx.conf (copied to
 * testdata/client-nginx.conf) with the JSON snapshot testdata/nginx-import-snapshot.json.
 *
 * API CONTRACT (frozen; the implementation fills the bodies):
 *   parseNginx(text)                      → NginxNode[] (+ parse errors, never throws)
 *   importNginx(text, options)            → NginxImportResult (edge + mapping rows + certificate paths)
 *   applyNginxImport(current, result, mode, certs) → Edge (append | replace)
 *
 * nginx inheritance (http → server → location) is evaluated here, so the imported routes behave as in nginx:
 *   - scalar directives (client_max_body_size, proxy_*buffering, proxy_*_timeout, proxy_http_version): inner overrides outer
 *   - array directives (proxy_set_header, add_header, proxy_hide_header): a level that has ANY of one directive replaces
 *     the outer list of that directive entirely
 *   - nginx defaults are made explicit (client_max_body_size 1m, proxy_buffering on, proxy_request_buffering on, 60 s)
 */
import type { Edge, EdgeHost, EdgeRoute, EdgeMatch, EdgeProxyAction, EdgeRouteAction, EdgeResponseHeaders } from './types.js';
import { checkEdgeRegex, emptyResponseHeaders, isEmptyResponseHeaders as isEmptyHeaders, EDGE_DEFAULTS } from './edge-resolve.js';
import { newEdgeHost, newProxyAction, normalizeEdge } from './validate.js';

// ───────────────────────────────────────────────────────────── parser
export interface NginxNode {
  /** directive name, e.g. "location", "proxy_pass" */
  name: string;
  /** arguments with quotes removed (escapes resolved) */
  args: string[];
  /** child directives of a block ("server { … }"); null for a simple directive ("listen 80;") */
  block: NginxNode[] | null;
  /** 1-based line of the directive name */
  line: number;
}
export interface NginxParseResult {
  nodes: NginxNode[];
  errors: { line: number; message: string }[];
}

// ───────────────────────────────────────────────────────────── import
/** A container of the target site as the importer sees it (upstream `http://<containerSlug>:<port>` resolution). */
export interface NginxImportContainer {
  containerSlug: string;
  /** image ports: portName + container port number; edge false ports are listed but never chosen */
  ports: { portName: string; port: number; edge: boolean }[];
}
export interface NginxImportOptions {
  containers: NginxImportContainer[];
  /**
   * Upstream mapping chosen by the user (import page step "Upstreams"): "<name>:<port>" of proxy_pass → a container +
   * port of this site. Wins over the same-name match. Lets a config written for other container names (ez-terminal)
   * import onto this site's names (northwind-backend-terminal) in one step instead of one route at a time.
   */
  upstreams?: Record<string, { containerSlug: string; portName: string }>;
}
/** One distinct proxy_pass upstream of the config ("<name>:<port>") and how it resolves on this site. */
export interface NginxUpstreamRef {
  key: string; //            "<name>:<port>"
  host: string;
  port: number;
  /** route ids that proxy to it */
  routes: string[];
  /** the target used (null = unresolved → those routes have an empty target) */
  resolved: { containerSlug: string; portName: string } | null;
  /** name = a container named like the upstream; mapped = options.upstreams; null = unresolved */
  how: 'name' | 'mapped' | null;
  /** best guess for an unresolved upstream (prefill only, never applied by itself), see suggestUpstream */
  suggestion: { containerSlug: string; portName: string } | null;
}
/**
 * Guess the container for an upstream name: same name; else names equal after dropping "-" (nodered ~ node-red); else
 * the containers whose name ends with "-<rest>" where rest = the upstream name without a leading "ez-" / "northwind-"
 * (ez-terminal ~ northwind-backend-terminal). Several candidates: the one with an edge port numbered `port`, else none.
 * Port: the edge port with that number; a same-name container may fall back to its only edge port, a fuzzy match may not.
 */
export function suggestUpstream(name: string, port: number, containers: NginxImportContainer[]): { containerSlug: string; portName: string } | null {
  // fuzzy (suffix) matches need the same port number; only a same-name container may fall back to its only edge port
  const pick = (c: NginxImportContainer, fuzzy = false) => {
    const edge = c.ports.filter((x) => x.edge);
    const p = edge.find((x) => x.port === port) ?? (!fuzzy && edge.length === 1 ? edge[0] : undefined);
    return p ? { containerSlug: c.containerSlug, portName: p.portName } : null;
  };
  const flat = (x: string) => x.replace(/-/g, '');
  const exact = containers.find((c) => c.containerSlug === name) ?? containers.find((c) => flat(c.containerSlug) === flat(name));
  if (exact) return pick(exact);
  const rest = name.replace(/^(ez|northwind)-/, '');
  if (rest === name && !name.includes('-')) {
    const c = containers.filter((x) => x.containerSlug.endsWith('-' + name));
    if (c.length === 1) return pick(c[0]!, true);
  }
  const cands = containers.filter((c) => c.containerSlug === rest || c.containerSlug.endsWith('-' + rest));
  if (cands.length === 1) return pick(cands[0]!, true);
  const byPort = cands.filter((c) => c.ports.some((x) => x.edge && x.port === port));
  return byPort.length === 1 ? pick(byPort[0]!, true) : null;
}
/**
 * One row of the mapping table (one per directive / block the importer looked at, in file order).
 *   ok          — mapped 1:1
 *   note        — mapped, with a difference the user should read (`note`)
 *   default     — nothing to set: OPM does this by default (e.g. proxy_set_header Host $host)
 *   ignored     — has no meaning for OPM (worker_processes, events, resolver …), `note` says why
 *   unsupported — not mapped; the user must handle it by hand (`note` says how)
 *   error       — mapped but needs input (e.g. upstream container not found → route created with an empty target)
 */
export type NginxMapStatus = 'ok' | 'note' | 'default' | 'ignored' | 'unsupported' | 'error';
export interface NginxMapRow {
  line: number;
  /** where it is: "http", "server env.x.y", "server env.x.y › location /x/" */
  context: string;
  /** the directive name ("location", "proxy_pass", …) */
  directive: string;
  /** the directive as written (args joined; blocks: "location ~ ^/apis/(docs)$ { … }") */
  text: string;
  status: NginxMapStatus;
  /** the OPM target, e.g. "route x-ez-apis", "route x-ez-apis · stripPrefix", "host env.x.y · responseHeaders.add", "edge.maxBodyBytes" */
  target: string | null;
  note: string;
}
export interface NginxCertRef {
  hostname: string;
  certPath: string | null; //  ssl_certificate
  keyPath: string | null; //   ssl_certificate_key
}
export interface NginxImportResult {
  /** the whole imported edge (replace mode uses it as is, after certificates are bound) */
  edge: Edge;
  rows: NginxMapRow[];
  /** hosts in files TLS mode whose cert / key the user must bind to an OPM file / secret */
  certs: NginxCertRef[];
  /** every distinct proxy_pass upstream, in first-use order (the import page maps the unresolved ones) */
  upstreams: NginxUpstreamRef[];
  parseErrors: { line: number; message: string }[];
  /** counts for the summary line */
  summary: Record<NginxMapStatus, number> & { routes: number; hosts: number };
}
/** User choice per hostname: the OPM file (certificate PEM) and secret (key PEM). */
export type NginxCertBindings = Record<string, { certFileSlug: string; keySecretSlug: string }>;

/** Tokenize + build the directive tree. Never throws; problems go to `errors` and parsing goes on. */
export function parseNginx(text: string): NginxParseResult {
  const src = typeof text === 'string' ? text : '';
  const errors: NginxParseResult['errors'] = [];
  const root: NginxNode[] = [];
  const stack: { list: NginxNode[]; line: number; name: string }[] = [{ list: root, line: 0, name: '' }];
  let words: { v: string; line: number }[] = [];
  let line = 1;
  let i = 0;
  const top = () => stack[stack.length - 1]!;
  const emit = (block: NginxNode[] | null) => {
    const [first, ...rest] = words;
    words = [];
    if (!first) return null;
    const node: NginxNode = { name: first.v, args: rest.map((w) => w.v), block, line: first.line };
    top().list.push(node);
    return node;
  };
  const escape = (n: string): string | null => {
    if (n === '"' || n === "'" || n === '\\') return n;
    if (n === 'n') return '\n';
    if (n === 't') return '\t';
    if (n === 'r') return '\r';
    return null;
  };
  while (i < src.length) {
    const c = src[i]!;
    if (c === '\n') {
      line++;
      i++;
      continue;
    }
    if (c === ' ' || c === '\t' || c === '\r' || c === '\f' || c === '\v') {
      i++;
      continue;
    }
    if (c === '#') {
      while (i < src.length && src[i] !== '\n') i++;
      continue;
    }
    if (c === ';') {
      if (words.length === 0) errors.push({ line, message: 'unexpected ";"' });
      else emit(null);
      i++;
      continue;
    }
    if (c === '{') {
      if (words.length === 0) errors.push({ line, message: 'unexpected "{" (a block needs a name)' });
      const at = words[0]?.line ?? line;
      const block: NginxNode[] = [];
      const node = emit(block);
      stack.push({ list: block, line: at, name: node?.name ?? '' });
      i++;
      continue;
    }
    if (c === '}') {
      if (words.length > 0) {
        errors.push({ line: words[0]!.line, message: `missing ";" after "${words.map((w) => w.v).join(' ')}"` });
        emit(null);
      }
      if (stack.length === 1) errors.push({ line, message: 'unexpected "}" (no block is open)' });
      else stack.pop();
      i++;
      continue;
    }
    const start = line;
    let v = '';
    if (c === '"' || c === "'") {
      let j = i + 1;
      let closed = false;
      while (j < src.length) {
        const d = src[j]!;
        if (d === c) {
          closed = true;
          j++;
          break;
        }
        if (d === '\\' && j + 1 < src.length) {
          const e = escape(src[j + 1]!);
          v += e ?? d + src[j + 1]!;
          if (src[j + 1] === '\n') line++;
          j += 2;
          continue;
        }
        if (d === '\n') line++;
        v += d;
        j++;
      }
      if (!closed) errors.push({ line: start, message: `unterminated ${c === '"' ? 'double' : 'single'} quote` });
      i = j;
    } else {
      let j = i;
      while (j < src.length) {
        const d = src[j]!;
        if (d === ' ' || d === '\t' || d === '\r' || d === '\n' || d === '\f' || d === '\v' || d === ';' || d === '}') break;
        if (d === '{') {
          // nginx keeps "${name}" inside a word; any other "{" opens a block
          if (v.endsWith('$')) {
            const k = src.indexOf('}', j);
            const end = k < 0 ? src.length : k + 1;
            v += src.slice(j, end);
            j = end;
            continue;
          }
          break;
        }
        if (d === '\\' && j + 1 < src.length && src[j + 1] !== '\n') {
          const e = escape(src[j + 1]!);
          v += e ?? d + src[j + 1]!;
          j += 2;
          continue;
        }
        v += d;
        j++;
      }
      i = j;
    }
    words.push({ v, line: start });
  }
  if (words.length > 0) {
    errors.push({ line: words[0]!.line, message: `missing ";" after "${words.map((w) => w.v).join(' ')}"` });
    emit(null);
  }
  while (stack.length > 1) {
    const s = stack.pop()!;
    errors.push({ line: s.line, message: `missing "}" for the "${s.name}" block opened on line ${s.line}` });
  }
  return { nodes: root, errors };
}

// ───────────────────────────────────────────────────────────── helpers (exported for the GUIs / tests)
/** nginx size ("100M", "512k", "1g", "1024") → bytes; 0 = unlimited; null = not a size. */
export function parseNginxSize(s: string | undefined): number | null {
  const m = /^(\d+)([kKmMgG]?)$/.exec(s ?? '');
  if (!m) return null;
  const mult: Record<string, number> = { '': 1, k: 1024, m: 1024 ** 2, g: 1024 ** 3 };
  return Number(m[1]) * mult[m[2]!.toLowerCase()]!;
}
/** nginx time ("60s", "1m", "500ms", "1h30m", bare = seconds) → milliseconds; null = not a time. */
export function parseNginxTime(s: string | undefined): number | null {
  const str = s ?? '';
  if (str === '') return null;
  const unit: Record<string, number> = { ms: 1, s: 1000, m: 60_000, h: 3_600_000, d: 86_400_000, w: 604_800_000, M: 2_592_000_000, y: 31_536_000_000 };
  const re = /(\d+)(ms|s|m|h|d|w|M|y)?/y;
  let total = 0;
  let pos = 0;
  while (pos < str.length) {
    re.lastIndex = pos;
    const m = re.exec(str);
    if (!m) return null;
    total += Number(m[1]) * unit[m[2] ?? 's']!;
    pos = re.lastIndex;
  }
  return total;
}
/** Route id from a location path: lowercased, runs of non [a-z0-9] → "-", trimmed; "/" → "root"; ≤ 63 chars. */
export function routeIdFromLocation(path: string): string {
  const s = path
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 63)
    .replace(/-+$/, '');
  return s === '' ? 'root' : s;
}
/** `base`, or `base-2`, `base-3` … (≤ 63 chars) when taken. Adds the result to `taken`. */
export function uniqueRouteId(base: string, taken: Set<string>): string {
  let id = base;
  for (let n = 2; taken.has(id); n++) {
    const suffix = `-${n}`;
    id = base.slice(0, 63 - suffix.length).replace(/-+$/, '') + suffix;
  }
  taken.add(id);
  return id;
}
function escapeRegex(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}
function quoteArg(a: string): string {
  return a === '' || /[\s;{}#"'\\]/.test(a) ? JSON.stringify(a) : a;
}
/** The directive as written: name + args (quoted where needed); blocks end in " { … }". */
export function nginxDirectiveText(n: NginxNode): string {
  const a = n.args.map(quoteArg).join(' ');
  return `${n.name}${a ? ' ' + a : ''}${n.block ? ' { … }' : ''}`;
}

// ───────────────────────────────────────────────────────────── directive tables
const WORKER = 'nginx worker / process setting; the agent runs the edge (Caddy) itself';
const LOGS = 'the agent keeps the edge logs (site Logs)';
const SOCKET = 'socket tuning; Caddy picks its own';
const TLS_DEFAULTS = 'Caddy uses modern TLS defaults';
const BUFFERS = 'buffer / hash sizes; Caddy manages its own (bodies stream with bounded memory)';
const CLIENT_TIMEOUTS = 'client-side timeouts are managed by Caddy';
const IGNORED: Record<string, string> = {
  worker_processes: WORKER,
  worker_connections: WORKER,
  worker_rlimit_nofile: WORKER,
  worker_cpu_affinity: WORKER,
  worker_priority: WORKER,
  multi_accept: WORKER,
  use: WORKER,
  accept_mutex: WORKER,
  events: WORKER,
  pid: WORKER,
  user: WORKER,
  daemon: WORKER,
  master_process: WORKER,
  error_log: LOGS,
  access_log: LOGS,
  log_format: LOGS,
  log_not_found: LOGS,
  resolver: 'OPM resolves containers itself',
  resolver_timeout: 'OPM resolves containers itself',
  sendfile: SOCKET,
  tcp_nopush: SOCKET,
  tcp_nodelay: SOCKET,
  keepalive_timeout: 'client keep-alive is managed by Caddy',
  keepalive_requests: 'client keep-alive is managed by Caddy',
  types: 'MIME types come from the containers',
  default_type: 'MIME types come from the containers',
  server_tokens: 'nginx version banner; not applicable',
  charset: 'the containers set the charset',
  ssl_protocols: TLS_DEFAULTS,
  ssl_ciphers: TLS_DEFAULTS,
  ssl_prefer_server_ciphers: TLS_DEFAULTS,
  ssl_session_cache: TLS_DEFAULTS,
  ssl_session_timeout: TLS_DEFAULTS,
  ssl_session_tickets: TLS_DEFAULTS,
  ssl_dhparam: TLS_DEFAULTS,
  ssl_ecdh_curve: TLS_DEFAULTS,
  ssl_stapling: TLS_DEFAULTS,
  ssl_stapling_verify: TLS_DEFAULTS,
  ssl_buffer_size: TLS_DEFAULTS,
  ssl: 'the ssl flag is read from the listen lines / certificates',
  http2: 'Caddy serves HTTP/2 by default',
  client_body_buffer_size: BUFFERS,
  client_header_buffer_size: BUFFERS,
  large_client_header_buffers: BUFFERS,
  proxy_buffer_size: BUFFERS,
  proxy_buffers: BUFFERS,
  proxy_busy_buffers_size: BUFFERS,
  types_hash_max_size: BUFFERS,
  types_hash_bucket_size: BUFFERS,
  server_names_hash_bucket_size: BUFFERS,
  server_names_hash_max_size: BUFFERS,
  variables_hash_max_size: BUFFERS,
  variables_hash_bucket_size: BUFFERS,
  client_body_timeout: CLIENT_TIMEOUTS,
  client_header_timeout: CLIENT_TIMEOUTS,
  send_timeout: CLIENT_TIMEOUTS,
  reset_timedout_connection: CLIENT_TIMEOUTS,
};
const STATIC = 'the edge does not serve files: run a static file container (e.g. nginx or caddy) and proxy to it';
const UNSUPPORTED: Record<string, string> = {
  if: 'nginx "if" is not mapped: make one route per case (exact / regex locations) or handle it in the container',
  map: 'map variables are not supported: write fixed values into the routes',
  upstream: 'upstream groups are not supported: each route proxies to one container (proxy_pass http://<container>:<port>)',
  root: STATIC,
  alias: STATIC,
  try_files: STATIC,
  index: STATIC,
  autoindex: STATIC,
  error_page: 'custom error pages: OPM has one not-found page per site; other error pages come from the container',
  limit_except: 'use the route\'s "methods" filter instead',
  auth_request: 'sub-request auth is not supported by the edge; protect it in the container',
  allow: 'IP allow / deny lists are not supported by the edge; restrict it in the container or the firewall',
  deny: 'IP allow / deny lists are not supported by the edge; restrict it in the container or the firewall',
  stream: 'TCP / UDP proxying is not supported by the edge: publish the container port instead',
  mail: 'mail proxying is not supported by the edge',
  location: 'nested / named locations are not supported: make a separate route',
  server: 'a server block is only read inside http { }',
  expires: 'set Cache-Control / Expires as response headers (add_header) instead',
  return: 'not mapped',
  rewrite: 'not mapped',
  proxy_pass: 'proxy_pass is only read inside a location',
  listen: 'listen is only read inside a server',
  server_name: 'server_name is only read inside a server',
  proxy_set_header: 'only read inside http / server / location',
  add_header: 'only read inside http / server / location',
};
const UNSUPPORTED_PREFIX: [string, string][] = [
  ['proxy_cache', 'the edge does not cache: cache in the container'],
  ['proxy_store', 'the edge does not cache: cache in the container'],
  ['limit_req', 'rate limiting is not supported by the edge'],
  ['limit_conn', 'connection limits are not supported by the edge'],
  ['limit_rate', 'bandwidth limits are not supported by the edge'],
  ['auth_basic', 'basic auth is not supported by the edge: protect it in the container'],
  ['sub_filter', 'response body rewriting is not supported by the edge'],
  ['proxy_cookie_', 'cookie path / domain rewriting is not supported: fix it in the container'],
  ['proxy_ssl_', 'the edge talks plain HTTP to containers on the site network'],
  ['gzip', 'the edge does not compress; the container can'],
  ['brotli', 'the edge does not compress; the container can'],
  ['fastcgi_', 'FastCGI is not supported: run an HTTP server in the container'],
  ['uwsgi_', 'uwsgi is not supported: run an HTTP server in the container'],
  ['grpc_', 'gRPC proxying is not mapped: proxy it as HTTP/2 from the container'],
  ['ssl_client_', 'client certificates are not supported by the edge'],
  ['ssl_verify', 'client certificates are not supported by the edge'],
  ['ssl_', TLS_DEFAULTS],
];
const PROXY_ONLY = new Set([
  'client_max_body_size',
  'proxy_request_buffering',
  'proxy_buffering',
  'proxy_connect_timeout',
  'proxy_send_timeout',
  'proxy_read_timeout',
  'proxy_http_version',
  'proxy_set_header',
  'proxy_pass_request_headers',
  'proxy_max_temp_file_size',
  'proxy_redirect',
]);

interface RowInfo {
  status: NginxMapStatus;
  target: string | null;
  note: string;
}
const info = (status: NginxMapStatus, target: string | null = null, note = ''): RowInfo => ({ status, target, note });

/** Status of a directive the importer does not map by itself (ignored / unsupported / harmless defaults). */
function classifyMisc(n: NginxNode): RowInfo {
  const a0 = n.args[0] ?? '';
  if (n.name === 'include') return /(^|\/)mime\.types$/.test(a0) ? info('ignored', null, 'MIME types come from the containers') : info('unsupported', null, 'include is not followed: paste the included file into the import');
  if (n.name === 'proxy_max_temp_file_size') return info('default', null, 'OPM never writes temp files; bodies stream');
  if (n.name === 'proxy_pass_request_headers') return a0 === 'on' ? info('default', null, 'OPM always passes the request headers') : info('unsupported', null, 'not passing the client headers is not supported; use removeHeaders for single headers');
  if (n.name === 'proxy_pass_request_body') return a0 === 'on' ? info('default', null, 'OPM always passes the request body') : info('unsupported', null, 'dropping the request body is not supported');
  if (n.name === 'proxy_redirect') {
    if (a0 === 'off') return info('default', null, 'OPM never rewrites Location headers of the container');
    if (a0 === 'default') return info('note', null, 'OPM does not rewrite Location headers of the container (nginx default would when proxy_pass has a URI)');
    return info('unsupported', null, 'Location rewriting is not supported: make the container send the right URL');
  }
  if (n.name === 'proxy_ignore_client_abort' && a0 === 'off') return info('default', null, 'OPM stops the upstream request when the client goes away');
  const ig = IGNORED[n.name];
  if (ig !== undefined) return info('ignored', null, ig);
  const un = UNSUPPORTED[n.name];
  if (un !== undefined) return info('unsupported', null, un);
  for (const [p, note] of UNSUPPORTED_PREFIX) if (n.name.startsWith(p)) return info(p === 'ssl_' ? 'ignored' : 'unsupported', null, note);
  return info('unsupported', null, 'unknown directive (or not valid here): not mapped');
}

// ───────────────────────────────────────────────────────────── inheritance
interface Hdr {
  name: string;
  value: string;
  line: number;
}
interface AddHdr extends Hdr {
  always: boolean;
}
/** Effective settings at one level (undefined = not set at this level or above → nginx default). */
interface Eff {
  maxBody?: number | null; // bytes; null = unlimited (nginx 0)
  reqBuf?: boolean;
  respBuf?: boolean;
  connect?: number;
  send?: number;
  read?: number;
  httpVersion?: string;
  setHeaders?: Hdr[];
  addHeaders?: AddHdr[];
  hideHeaders?: Hdr[];
  sslCert?: string;
  sslKey?: string;
}
type Level = 'http' | 'server' | 'location';
interface Targets {
  /** proxy settings ("all routes", "routes of server x", "route id") */
  routes: string;
  /** body limit ("edge.maxBodyBytes", "routes of server x · maxBodyBytes", "route id · maxBodyBytes") */
  body: string;
  /** response headers / TLS ("all hosts", "host x", "route id") */
  headers: string;
}
type ReqKind = 'default' | 'websocket' | 'set' | 'remove' | 'hop';
function classifyRequestHeader(name: string, value: string): { kind: ReqKind; note: string } {
  const n = name.toLowerCase();
  if (n === 'host' && (value === '$host' || value === '$http_host')) return { kind: 'default', note: 'OPM keeps the client Host by default' };
  if (n === 'x-real-ip' && value === '$remote_addr') return { kind: 'default', note: 'OPM sets X-Real-IP to the client IP by default' };
  if (n === 'x-forwarded-for' && (value === '$proxy_add_x_forwarded_for' || value === '$remote_addr'))
    return { kind: 'default', note: 'OPM sets X-Forwarded-For by default; Caddy sets the client IP, it does not append to a client-sent list' };
  if (n === 'x-forwarded-proto' && value === '$scheme') return { kind: 'default', note: 'OPM sets X-Forwarded-Proto by default' };
  if (n === 'x-forwarded-host' && (value === '$host' || value === '$http_host')) return { kind: 'default', note: 'OPM sets X-Forwarded-Host by default' };
  if (n === 'upgrade' && value === '$http_upgrade') return { kind: 'websocket', note: 'Caddy manages Upgrade/Connection' };
  if (n === 'connection' && (/^upgrade$/i.test(value) || value === '$connection_upgrade')) return { kind: 'websocket', note: 'Caddy manages Upgrade/Connection' };
  if (n === 'connection' && value === '') return { kind: 'default', note: 'OPM uses keep-alive connections to the containers' };
  if (['connection', 'upgrade', 'keep-alive', 'te', 'transfer-encoding', 'trailer', 'proxy-connection'].includes(n)) return { kind: 'hop', note: `${name} is a hop-by-hop header managed by Caddy; not mapped` };
  if (value === '') return { kind: 'remove', note: 'an empty value means nginx does not pass the header → removeHeaders' };
  return { kind: 'set', note: '' };
}
function onOff(v: string | undefined): boolean | null {
  return v === 'on' ? true : v === 'off' ? false : null;
}

/**
 * Read the inheritable directives of one level: returns the effective settings (own over parent; array directives
 * replace) and the mapping rows of those directives.
 */
function readLevel(nodes: NginxNode[], parent: Eff, level: Level, t: Targets): { eff: Eff; own: Eff; rows: Map<NginxNode, RowInfo> } {
  const own: Eff = {};
  const rows = new Map<NginxNode, RowInfo>();
  const timeout = (n: NginxNode, k: 'connect' | 'send' | 'read', field: string, max: number) => {
    const ms = n.args.length === 1 ? parseNginxTime(n.args[0]) : null;
    if (ms === null) return rows.set(n, info('unsupported', null, 'cannot read the time'));
    const raw = Math.ceil(ms / 1000);
    const sec = Math.min(max, Math.max(1, raw));
    own[k] = sec;
    const notes: string[] = [];
    if (ms % 1000 !== 0) notes.push(`rounded up to ${raw} s`);
    if (sec !== raw) notes.push(`clamped to ${sec} s (OPM allows 1–${max})`);
    rows.set(n, info(notes.length ? 'note' : 'ok', `${t.routes} · ${field}`, notes.join('; ')));
  };
  for (const n of nodes) {
    if (n.block) continue;
    switch (n.name) {
      case 'client_max_body_size': {
        const b = n.args.length === 1 ? parseNginxSize(n.args[0]) : null;
        if (b === null) {
          rows.set(n, info('unsupported', null, 'cannot read the size'));
          break;
        }
        const clamped = Math.min(b, EDGE_DEFAULTS.maxBodyLimit);
        own.maxBody = b === 0 ? null : clamped;
        rows.set(n, info(clamped !== b ? 'note' : 'ok', t.body, b === 0 ? '0 = unlimited' : clamped !== b ? 'clamped to 1 TiB' : ''));
        break;
      }
      case 'proxy_request_buffering':
      case 'proxy_buffering': {
        const v = onOff(n.args[0]);
        if (v === null || n.args.length !== 1) {
          rows.set(n, info('unsupported', null, 'expected on / off'));
          break;
        }
        if (n.name === 'proxy_request_buffering') {
          own.reqBuf = v;
          rows.set(n, info(v ? 'note' : 'ok', `${t.routes} · requestBuffering`, v ? 'OPM buffers up to 8 MiB in memory, the rest streams (nginx spools the whole body to a temp file)' : ''));
        } else {
          own.respBuf = v;
          rows.set(n, info(v ? 'note' : 'ok', `${t.routes} · responseBuffering`, v ? 'text/event-stream (SSE) is always flushed immediately by OPM' : ''));
        }
        break;
      }
      case 'proxy_connect_timeout':
        timeout(n, 'connect', 'connectTimeoutSec', EDGE_DEFAULTS.maxConnectTimeoutSec);
        break;
      case 'proxy_send_timeout':
        timeout(n, 'send', 'sendTimeoutSec', EDGE_DEFAULTS.maxTimeoutSec);
        break;
      case 'proxy_read_timeout':
        timeout(n, 'read', 'readTimeoutSec', EDGE_DEFAULTS.maxTimeoutSec);
        break;
      case 'proxy_http_version':
        own.httpVersion = n.args[0] ?? '';
        rows.set(n, info(own.httpVersion === '1.1' ? 'default' : 'note', null, 'OPM always uses HTTP/1.1 with keep-alive'));
        break;
      case 'proxy_set_header':
        if (n.args.length !== 2) {
          rows.set(n, info('unsupported', null, 'expected: proxy_set_header <name> <value>'));
          break;
        }
        (own.setHeaders ??= []).push({ name: n.args[0]!, value: n.args[1]!, line: n.line });
        break;
      case 'add_header':
        if (n.args.length < 2 || n.args.length > 3 || (n.args.length === 3 && n.args[2] !== 'always')) {
          rows.set(n, info('unsupported', null, 'expected: add_header <name> <value> [always]'));
          break;
        }
        (own.addHeaders ??= []).push({ name: n.args[0]!, value: n.args[1]!, always: n.args[2] === 'always', line: n.line });
        break;
      case 'proxy_hide_header':
        if (n.args.length !== 1) {
          rows.set(n, info('unsupported', null, 'expected: proxy_hide_header <name>'));
          break;
        }
        (own.hideHeaders ??= []).push({ name: n.args[0]!, value: '', line: n.line });
        break;
      case 'ssl_certificate':
      case 'ssl_certificate_key':
        if (level === 'location') break;
        if (n.name === 'ssl_certificate') own.sslCert = n.args[0] ?? '';
        else own.sslKey = n.args[0] ?? '';
        rows.set(
          n,
          info('note', `${t.headers} · tls files`, n.name === 'ssl_certificate' ? `bind ${n.args[0] ?? ''} to an OPM file (certificate PEM)` : `bind ${n.args[0] ?? ''} to an OPM secret (key PEM)`),
        );
        break;
    }
  }
  const eff: Eff = { ...parent, ...own };
  // rows that depend on the effective lists of this level
  for (const n of nodes) {
    if (n.block || rows.has(n)) continue;
    if (n.name === 'proxy_set_header') {
      const c = classifyRequestHeader(n.args[0]!, n.args[1]!);
      const row =
        c.kind === 'default'
          ? info('default', null, c.note)
          : c.kind === 'websocket'
            ? info('ok', `${t.routes} · websocket`, c.note)
            : c.kind === 'hop'
              ? info('note', null, c.note)
              : c.kind === 'remove'
                ? info('ok', `${t.routes} · removeHeaders`, c.note)
                : info('ok', `${t.routes} · setHeaders.${n.args[0]}`, '');
      rows.set(n, row);
    } else if (n.name === 'add_header') {
      const name = n.args[0]!;
      const hidden = (eff.hideHeaders ?? []).some((h) => h.name.toLowerCase() === name.toLowerCase());
      const first = (own.addHeaders ?? []).find((h) => h.name.toLowerCase() === name.toLowerCase());
      const notes: string[] = [];
      if (hidden) notes.push(`with proxy_hide_header ${name} → set (replaces the container's value)`);
      if (n.args[2] === 'always') notes.push('always: also on error responses');
      if (first && first.line !== n.line) {
        rows.set(n, info('unsupported', null, `OPM keeps one value per header name: the first add_header ${name} (line ${first.line}) is used`));
        continue;
      }
      rows.set(n, info('ok', `${t.headers} · responseHeaders.${hidden ? 'set' : 'add'}`, notes.join('; ')));
    } else if (n.name === 'proxy_hide_header') {
      const name = n.args[0]!;
      const added = (eff.addHeaders ?? []).some((h) => h.name.toLowerCase() === name.toLowerCase());
      rows.set(n, info('ok', `${t.headers} · responseHeaders.${added ? 'set' : 'remove'}`, added ? `with add_header ${name} → set` : ''));
    }
  }
  return { eff, own, rows };
}

/** nginx add_header + proxy_hide_header lists → EdgeResponseHeaders (hide + add → set; add → add; hide → remove). */
function toResponseHeaders(add: AddHdr[] | undefined, hide: Hdr[] | undefined): EdgeResponseHeaders {
  const r = emptyResponseHeaders();
  const lower = (s: string) => s.toLowerCase();
  const hidden = new Set((hide ?? []).map((h) => lower(h.name)));
  const seen = new Set<string>();
  for (const a of add ?? []) {
    if (seen.has(lower(a.name))) continue;
    seen.add(lower(a.name));
    (hidden.has(lower(a.name)) ? r.set : r.add)[a.name] = a.value;
    if (a.always) r.always.push(a.name);
  }
  for (const h of hide ?? []) {
    if (seen.has(lower(h.name)) || r.remove.some((x) => lower(x) === lower(h.name))) continue;
    r.remove.push(h.name);
  }
  return r;
}
function sameHeaders(a: EdgeResponseHeaders | null, b: EdgeResponseHeaders | null): boolean {
  const canon = (h: EdgeResponseHeaders | null) =>
    h === null ? 'null' : JSON.stringify([Object.entries(h.set).sort(), Object.entries(h.add).sort(), [...h.remove].sort(), [...h.always].sort()]);
  return canon(a) === canon(b);
}

// ───────────────────────────────────────────────────────────── importer
const DEFAULT_MAX_BODY = 1024 * 1024; // nginx client_max_body_size 1m

interface ServerInfo {
  /** OPM hostnames of this server (deduplicated, lowercased) */
  hostnames: string[];
  routes: EdgeRoute[];
}
interface State {
  rows: NginxMapRow[];
  routes: EdgeRoute[];
  servers: ServerInfo[];
  hosts: Map<string, { host: EdgeHost; hasRoutes: boolean }>;
  certs: NginxCertRef[];
  ids: Set<string>;
  regexCount: number;
  httpPort: number | null;
  httpsPort: number | null;
  anySsl: boolean;
  /** a proxy location inherited no proxy_http_version (nginx default 1.0) */
  http10: boolean;
  containers: NginxImportContainer[];
  upstreamMap: Record<string, { containerSlug: string; portName: string }>;
  upstreams: Map<string, NginxUpstreamRef>;
  /** effective client_max_body_size of each imported proxy route (undefined = nginx default 1m) */
  bodyOf: Map<EdgeRoute, number | null | undefined>;
}
function pushRow(s: State, n: NginxNode, context: string, r: RowInfo) {
  s.rows.push({ line: n.line, context, directive: n.name, text: nginxDirectiveText(n), status: r.status, target: r.target, note: r.note });
}
function childContext(ctx: string, n: NginxNode): string {
  const label = n.args.length ? `${n.name} ${n.args.join(' ')}` : n.name;
  return ctx === 'main' ? label : `${ctx} › ${label}`;
}
/** Rows for a block that is not mapped and everything inside it. */
function unmappedBlock(s: State, n: NginxNode, ctx: string, r: RowInfo) {
  pushRow(s, n, ctx, r);
  if (!n.block || n.name === 'types') return;
  const inner = childContext(ctx, n);
  for (const c of n.block) {
    const ci = r.status === 'ignored' ? info('ignored', null, IGNORED[c.name] ?? `inside ${n.name} { }`) : info('unsupported', null, `inside ${n.name} { }: not mapped`);
    unmappedBlock(s, c, inner, ci);
  }
}
function misc(s: State, n: NginxNode, ctx: string) {
  const r = classifyMisc(n);
  if (n.block) unmappedBlock(s, n, ctx, r);
  else pushRow(s, n, ctx, r);
}

export function importNginx(text: string, options: NginxImportOptions): NginxImportResult {
  const parsed = parseNginx(text);
  const s: State = {
    rows: [],
    routes: [],
    servers: [],
    hosts: new Map(),
    certs: [],
    upstreamMap: options?.upstreams ?? {},
    upstreams: new Map(),
    ids: new Set(),
    regexCount: 0,
    httpPort: null,
    httpsPort: null,
    anySsl: false,
    http10: false,
    containers: Array.isArray(options?.containers) ? options.containers : [],
    bodyOf: new Map(),
  };
  let httpMaxBody: number | null | undefined;
  for (const n of parsed.nodes) {
    if (n.name === 'http' && n.block) {
      pushRow(s, n, 'main', info('ok', 'edge'));
      const lvl = readLevel(n.block, {}, 'http', { routes: 'all routes', body: 'edge.maxBodyBytes', headers: 'all hosts' });
      if (lvl.own.maxBody !== undefined) httpMaxBody = lvl.own.maxBody;
      for (const c of n.block) {
        if (c.name === 'server' && c.block) importServer(s, c, lvl.eff);
        else {
          const r = lvl.rows.get(c);
          if (r) pushRow(s, c, 'http', r);
          else misc(s, c, 'http');
        }
      }
    } else if (n.name === 'events' && n.block) unmappedBlock(s, n, 'main', info('ignored', null, IGNORED['events']!));
    else misc(s, n, 'main');
  }
  const synthetic: NginxMapRow[] = [];
  if (httpMaxBody === undefined)
    synthetic.push({
      line: 0,
      context: 'http',
      directive: 'client_max_body_size',
      text: '(default) client_max_body_size 1m',
      status: 'default',
      target: 'edge.maxBodyBytes',
      note: 'nginx default (not set in http { }): bodies over 1 MiB get 413',
    });
  if (s.http10)
    synthetic.push({
      line: 0,
      context: 'http',
      directive: 'proxy_http_version',
      text: '(default) proxy_http_version 1.0',
      status: 'note',
      target: null,
      note: 'OPM always uses HTTP/1.1 with keep-alive',
    });
  const edgeMax = httpMaxBody === undefined ? DEFAULT_MAX_BODY : httpMaxBody;
  // route body limits: the effective nginx value; equal to the edge's → null (inherit), unlimited → 0
  for (const r of s.routes) {
    if (r.action.type !== 'proxy') continue;
    const eff = s.bodyOf.get(r);
    const v = eff === undefined ? DEFAULT_MAX_BODY : eff;
    r.action.maxBodyBytes = v === edgeMax ? null : v === null ? 0 : v;
  }
  const hosts = [...s.hosts.values()].map((h) => h.host);
  const all = hosts.map((h) => h.hostname);
  for (const sv of s.servers) {
    const scoped = sv.hostnames.includes('*') || all.every((h) => sv.hostnames.includes(h)) ? [] : sv.hostnames;
    for (const r of sv.routes) r.hosts = [...scoped];
  }
  const edge: Edge = normalizeEdge({
    httpPort: s.httpPort ?? 80,
    httpsPort: s.anySsl ? (s.httpsPort ?? 443) : null,
    maxBodyBytes: edgeMax,
    hosts,
    routes: s.routes,
  });
  const rows = [...synthetic, ...s.rows];
  const summary: NginxImportResult['summary'] = { ok: 0, note: 0, default: 0, ignored: 0, unsupported: 0, error: 0, routes: edge.routes.length, hosts: edge.hosts.length };
  for (const r of rows) summary[r.status]++;
  return { edge, rows, certs: s.certs, upstreams: [...s.upstreams.values()], parseErrors: parsed.errors, summary };
}

/** server_name entry → OPM hostnames (empty list = not mappable). */
function mapServerName(raw: string): { names: string[]; note: string } {
  if (raw === '' || raw === '_') return { names: ['*'], note: `"${raw}" → host * (any)` };
  if (raw.startsWith('~')) return { names: [], note: `regex server names are not supported: add the hosts by hand` };
  const n = raw.toLowerCase();
  if (n.startsWith('.')) return { names: [n.slice(1), `*${n}`], note: `${raw} → ${n.slice(1)} and *${n}` };
  if (n.includes('*') && !n.startsWith('*.')) return { names: [], note: `wildcards at the end (${raw}) are not supported: add the hosts by hand` };
  return { names: [n], note: '' };
}
function parseListen(args: string[]): { port: number | null; ssl: boolean; note: string } {
  const a = args[0] ?? '';
  const ssl = args.includes('ssl');
  if (a.startsWith('unix:')) return { port: null, ssl, note: 'unix sockets are not supported' };
  const m = /^(?:(.*):)?(\d+)$/.exec(a);
  if (m) return { port: Number(m[2]), ssl, note: m[1] ? `the address ${m[1]} is ignored: the edge listens on every address` : '' };
  if (/^[\w.-]+$|^\[.*\]$/.test(a)) return { port: 80, ssl, note: `the address ${a} is ignored: the edge listens on every address` };
  return { port: null, ssl, note: 'cannot read the port' };
}
function isHttpsRedirect(args: string[]): boolean {
  return (args[0] === '301' || args[0] === '302' || args[0] === '307' || args[0] === '308') && /^https:\/\/\$(host|server_name|http_host)\$request_uri$/.test(args[1] ?? '');
}

function importServer(s: State, node: NginxNode, httpEff: Eff) {
  const kids = node.block ?? [];
  const nameNodes = kids.filter((k) => k.name === 'server_name' && !k.block);
  const rawNames = nameNodes.flatMap((k) => k.args);
  const ctx = `server${rawNames.length ? ' ' + rawNames.join(' ') : ''}`;
  const nameNotes = new Map<NginxNode, string[]>();
  const hostnames: string[] = [];
  for (const k of nameNodes)
    for (const a of k.args) {
      const m = mapServerName(a);
      if (m.note) (nameNotes.get(k) ?? nameNotes.set(k, []).get(k)!).push(m.note);
      for (const h of m.names) if (!hostnames.includes(h)) hostnames.push(h);
    }
  if (hostnames.length === 0) hostnames.push('*');
  const label = hostnames.join(', ');
  const lvl = readLevel(kids, httpEff, 'server', { routes: `routes of ${ctx}`, body: `routes of ${ctx} · maxBodyBytes`, headers: `host ${label}` });
  // listen
  const listens = kids.filter((k) => k.name === 'listen' && !k.block).map((k) => ({ node: k, ...parseListen(k.args) }));
  const sslOn = kids.some((k) => k.name === 'ssl' && k.args[0] === 'on');
  const serverSsl = sslOn || listens.some((l) => l.ssl);
  const hasCert = !!lvl.eff.sslCert;
  const tlsFiles = serverSsl && hasCert;
  if (serverSsl) s.anySsl = true;
  const listenRows = new Map<NginxNode, RowInfo>();
  for (const l of listens) {
    if (l.port === null) {
      listenRows.set(l.node, info('unsupported', null, l.note));
      continue;
    }
    const ssl = l.ssl || sslOn;
    const key = ssl ? 'httpsPort' : 'httpPort';
    const notes = l.note ? [l.note] : [];
    if (s[key] === null) s[key] = l.port;
    else if (s[key] !== l.port) notes.push(`the edge has one ${ssl ? 'HTTPS' : 'HTTP'} port: ${s[key]} is used`);
    if (ssl && !hasCert) notes.push('no ssl_certificate: pick the TLS mode of the host (internal / acme / files)');
    listenRows.set(l.node, info(notes.length ? 'note' : 'ok', `edge.${key}`, notes.join('; ')));
  }
  // ssl rows: only meaningful when the server listens with ssl
  if (!serverSsl)
    for (const k of kids) if ((k.name === 'ssl_certificate' || k.name === 'ssl_certificate_key') && lvl.rows.has(k)) lvl.rows.set(k, info('ignored', null, 'this server does not listen with ssl'));
  // server-level return (https redirect)
  let redirect = false;
  const returnRows = new Map<NginxNode, RowInfo>();
  for (const k of kids.filter((x) => x.name === 'return' && !x.block)) {
    if (!redirect && isHttpsRedirect(k.args)) {
      redirect = true;
      returnRows.set(k, info('note', `host ${label} · redirectHttpToHttps`, 'OPM answers HTTP with 308 → https for this host (nginx sent ' + k.args[0] + ')'));
    } else returnRows.set(k, info('unsupported', null, 'a server-level return is only mapped for "return 301 https://$host$request_uri"; make a redirect route instead'));
  }
  // hosts
  const hasLocations = kids.some((k) => k.name === 'location');
  const headers = toResponseHeaders(lvl.eff.addHeaders, lvl.eff.hideHeaders);
  for (const h of hostnames) {
    const prev = s.hosts.get(h);
    const tls: EdgeHost['tls'] = tlsFiles ? { mode: 'files', certFileSlug: '', keySecretSlug: '' } : { mode: 'none' };
    if (tlsFiles) s.certs.push({ hostname: h, certPath: lvl.eff.sslCert ?? null, keyPath: lvl.eff.sslKey ?? null });
    if (!prev) {
      s.hosts.set(h, { host: { ...newEdgeHost(h), tls, redirectHttpToHttps: redirect, responseHeaders: headers }, hasRoutes: hasLocations });
      continue;
    }
    if (tlsFiles && prev.host.tls.mode !== 'files') prev.host.tls = tls;
    if (redirect) prev.host.redirectHttpToHttps = true;
    if (hasLocations && !prev.hasRoutes) {
      prev.host.responseHeaders = headers;
      prev.hasRoutes = true;
    }
  }
  const server: ServerInfo = { hostnames, routes: [] };
  s.servers.push(server);
  const serverNote = hostnames.includes('*') && s.servers.length > 1 ? 'catch-all server: its routes apply to every host in OPM' : '';
  pushRow(s, node, 'http', info(serverNote ? 'note' : 'ok', `host ${label}`, serverNote));
  for (const k of kids) {
    if (k.name === 'location' && k.block) {
      importLocation(s, k, ctx, lvl.eff, server, headers);
      continue;
    }
    const r =
      listenRows.get(k) ??
      returnRows.get(k) ??
      lvl.rows.get(k) ??
      (k.name === 'server_name' && !k.block ? info(nameNotes.has(k) ? 'note' : 'ok', `host ${label}`, (nameNotes.get(k) ?? []).join('; ')) : undefined) ??
      (k.name === 'rewrite' ? info('unsupported', null, 'a server-level rewrite is not supported: put it in a location (a rewrite route)') : undefined);
    if (r) pushRow(s, k, ctx, r);
    else misc(s, k, ctx);
  }
}

interface LocMatch {
  kind: EdgeMatch['kind'];
  path: string;
  stopRegex: boolean;
  note: string;
}
function locationMatch(args: string[]): LocMatch | { unsupported: string } {
  let mod = '';
  let path = '';
  if (args.length === 2) [mod, path] = [args[0]!, args[1]!];
  else if (args.length === 1) {
    const a = args[0]!;
    const g = /^(=|\^~|~\*|~)(.+)$/.exec(a);
    if (g) [mod, path] = [g[1]!, g[2]!];
    else path = a;
  } else return { unsupported: 'expected: location [= | ^~ | ~ | ~*] <path>' };
  if (mod === '' && path.startsWith('@')) return { unsupported: 'named locations (@name) are not supported: they are only reached by error_page / try_files' };
  if (mod === '') return { kind: 'prefix', path, stopRegex: false, note: '' };
  if (mod === '=') return { kind: 'exact', path, stopRegex: false, note: '= → exact' };
  if (mod === '^~') return { kind: 'prefix', path, stopRegex: true, note: '^~ → prefix with stop regex' };
  if (mod === '~') return { kind: 'regex', path, stopRegex: false, note: '' };
  if (mod === '~*') return { kind: 'regex', path: `(?i)${path}`, stopRegex: false, note: '~* → regex with (?i)' };
  return { unsupported: `unknown location modifier "${mod}"` };
}
function parseProxyPass(u: string): { host: string; port: number; uri: string } | { error: string } {
  if (u.includes('$')) return { error: 'variables in proxy_pass are not supported: pick the target container' };
  if (/^https:\/\//i.test(u)) return { error: 'https to a container is not supported (the edge talks HTTP on the site network): pick the target container' };
  const m = /^http:\/\/([^/:?#]+)(?::(\d+))?(\/[^?#]*)?$/i.exec(u);
  if (!m) return { error: 'cannot read proxy_pass (expected http://<container>:<port>[/uri])' };
  return { host: m[1]!, port: m[2] ? Number(m[2]) : 80, uri: m[3] ?? '' };
}
/** nginx $proxy_host: the proxy_pass host, with ":port" unless it is the default 80. */
function proxyHost(host: string, port: number): string {
  return port === 80 ? host : `${host}:${port}`;
}

function importLocation(s: State, node: NginxNode, serverCtx: string, serverEff: Eff, server: ServerInfo, serverHeaders: EdgeResponseHeaders) {
  const kids = node.block ?? [];
  const ctx = `${serverCtx} › ${node.args.length ? `location ${node.args.join(' ')}` : 'location'}`;
  const lm = locationMatch(node.args);
  if ('unsupported' in lm) return unmappedBlock(s, node, serverCtx, info('unsupported', null, lm.unsupported));
  const id = uniqueRouteId(lm.kind === 'regex' ? `regex-${++s.regexCount}` : routeIdFromLocation(lm.path), s.ids);
  const R = `route ${id}`;
  const lvl = readLevel(kids, serverEff, 'location', { routes: R, body: `${R} · maxBodyBytes`, headers: R });
  const eff = lvl.eff;
  const rows = new Map<NginxNode, RowInfo>(lvl.rows);
  const locNotes: string[] = lm.note ? [lm.note] : [];
  let locStatus: NginxMapStatus = 'ok';
  if (lm.kind === 'regex') {
    const e = checkEdgeRegex(lm.path);
    if (e) {
      locStatus = 'error';
      locNotes.push(`regex not usable by the edge: ${e}`);
    }
  }
  // ── action: the first rewrite-module directive (return / rewrite), then proxy_pass
  const rw = kids.filter((k) => !k.block && (k.name === 'rewrite' || k.name === 'return'));
  const pps = kids.filter((k) => !k.block && k.name === 'proxy_pass');
  const pp = pps[0];
  for (const extra of pps.slice(1)) rows.set(extra, info('unsupported', null, 'only one proxy_pass per location'));
  let action: EdgeRouteAction | null = null;
  let breakRewrite: { regex: string; replacement: string; node: NginxNode } | null = null;
  const first = rw[0];
  for (const extra of rw.slice(1)) rows.set(extra, info('unsupported', null, 'only the first rewrite / return of a location is mapped'));
  if (first?.name === 'return') {
    const [a0 = '', a1] = first.args;
    const codes = ['301', '302', '303', '307', '308'];
    if (codes.includes(a0) && a1 !== undefined && first.args.length === 2) {
      action = { type: 'redirect', to: a1, status: Number(a0) as 301 | 302 | 303 | 307 | 308, keepPath: false };
      rows.set(first, info('ok', `${R} · redirect ${a0}`));
    } else if (first.args.length === 1 && /^(https?:\/\/|\$scheme)/.test(a0)) {
      action = { type: 'redirect', to: a0, status: 302, keepPath: false };
      rows.set(first, info('ok', `${R} · redirect 302`, 'return <url> = 302'));
    } else rows.set(first, info('unsupported', null, 'only return 301 | 302 | 303 | 307 | 308 <url> is mapped (a redirect route); other answers must come from a container'));
  } else if (first?.name === 'rewrite') {
    const [regex = '', replacement = '', flag = ''] = first.args;
    const regexErr = checkEdgeRegex(regex);
    if (first.args.length < 2 || first.args.length > 3) rows.set(first, info('unsupported', null, 'expected: rewrite <regex> <replacement> [flag]'));
    else if (flag === 'break') {
      if (pp) breakRewrite = { regex, replacement, node: first };
      else rows.set(first, info('unsupported', null, 'rewrite … break without proxy_pass serves files: the edge does not; add a proxy_pass'));
    } else if (flag === 'last' || flag === '') {
      if (pp) rows.set(first, info('unsupported', null, 'rewrite … last next to proxy_pass is not mapped: use "break" (proxy rewrite) or a separate location'));
      else {
        action = { type: 'rewrite', regex, replacement };
        rows.set(
          first,
          info(regexErr ? 'error' : 'note', `${R} · rewrite (internal, routes matched again)`, regexErr ?? 'a "?" in the replacement replaces the query (nginx would append the original args; with $request_uri they are already in it)'),
        );
      }
    } else if (flag === 'permanent' || flag === 'redirect') {
      if (regex === '^' || regex === '^(.*)$') {
        const status = flag === 'permanent' ? 301 : 302;
        action = { type: 'redirect', to: replacement.replace(/\$1(?![0-9])/g, '$uri'), status, keepPath: false };
        rows.set(first, info('note', `${R} · redirect ${status}`, 'nginx appends the original query unless the replacement ends with "?"; add $is_args$args if needed'));
      } else rows.set(first, info('unsupported', null, `rewrite … ${flag} with a pattern is not mapped: use a regex location with return ${flag === 'permanent' ? 301 : 302}`));
    } else rows.set(first, info('unsupported', null, `unknown rewrite flag "${flag}"`));
  }
  // ── proxy
  if (!action) {
    const a: EdgeProxyAction = newProxyAction();
    a.requestBuffering = eff.reqBuf ?? true;
    a.responseBuffering = eff.respBuf ?? true;
    a.connectTimeoutSec = eff.connect ?? EDGE_DEFAULTS.connectTimeoutSec;
    a.sendTimeoutSec = eff.send ?? EDGE_DEFAULTS.sendTimeoutSec;
    a.readTimeoutSec = eff.read ?? EDGE_DEFAULTS.readTimeoutSec;
    let upstreamHost = '';
    if (!pp) {
      locStatus = 'error';
      locNotes.push('no proxy_pass / return: route created without a target — pick the container that serves this path');
    } else {
      const p = parseProxyPass(pp.args[0] ?? '');
      if ('error' in p) rows.set(pp, info('unsupported', `${R} · target`, p.error));
      else {
        upstreamHost = proxyHost(p.host, p.port);
        const key = `${p.host}:${p.port}`;
        const mapped = s.upstreamMap[key];
        const mc = mapped ? s.containers.find((x) => x.containerSlug === mapped.containerSlug) : undefined;
        const mport = mc?.ports.find((x) => x.portName === mapped!.portName);
        const c = mport ? mc : s.containers.find((x) => x.containerSlug === p.host);
        const port = mport ?? c?.ports.find((x) => x.port === p.port && x.edge);
        const how: NginxUpstreamRef['how'] = mport ? 'mapped' : c && port ? 'name' : null;
        const ref = s.upstreams.get(key) ?? { key, host: p.host, port: p.port, routes: [], resolved: c && port ? { containerSlug: c.containerSlug, portName: port.portName } : null, how, suggestion: null };
        if (!s.upstreams.has(key)) {
          if (!ref.resolved) ref.suggestion = suggestUpstream(p.host, p.port, s.containers);
          s.upstreams.set(key, ref);
        }
        ref.routes.push(id);
        const notes: string[] = [];
        let target = `${R} → ${p.host}:${port?.portName ?? p.port}`;
        if (breakRewrite) {
          if (p.uri) notes.push(`the URI part "${p.uri}" is ignored by nginx after rewrite … break`);
        } else if (p.uri) {
          if (lm.kind === 'regex') notes.push('a URI part in proxy_pass inside a regex location is not valid nginx; ignored');
          else {
            a.stripPrefix = true;
            target += ' · stripPrefix';
            const pre = p.uri.replace(/\/$/, '');
            if (pre) {
              a.addPrefix = pre;
              target += ` · addPrefix ${pre}`;
              if (!p.uri.endsWith('/') && lm.path.endsWith('/')) notes.push(`nginx joins without "/" (${p.uri}rest); OPM gives ${pre}/rest`);
            }
          }
        }
        if (c && port) {
          a.containerSlug = c.containerSlug;
          a.portName = port.portName;
          rows.set(pp, info(notes.length ? 'note' : 'ok', target, notes.join('; ')));
        } else rows.set(pp, info('error', `${R} · target`, `no container ${p.host} with port ${p.port} on this site — map the upstream ${key} to a container (step "Upstreams") or pick the target on the route`));
        if (mport) rows.set(pp, info(notes.length ? 'note' : 'ok', `${R} → ${mc!.containerSlug}:${mport.portName}${target.includes('stripPrefix') ? target.slice(target.indexOf(' · stripPrefix')) : ''}`, [`upstream ${key} mapped to ${mc!.containerSlug}`, ...notes].join('; ')));
      }
    }
    // proxy rewrite (nginx rewrite … break)
    if (breakRewrite) {
      const { regex, replacement, node: rn } = breakRewrite;
      const base = lm.path.replace(/\/$/, '');
      const isStrip =
        lm.kind === 'prefix' && lm.path.endsWith('/') && replacement === '$1' && (regex === `^${escapeRegex(base)}(/.*)$` || regex === `^${base}(/.*)$`);
      if (isStrip) {
        a.stripPrefix = true;
        a.addPrefix = null;
        rows.set(rn, info('ok', `${R} · stripPrefix`, '→ strip prefix'));
      } else {
        a.stripPrefix = false;
        a.addPrefix = null;
        a.rewrite = { regex, replacement };
        const e = checkEdgeRegex(regex);
        rows.set(rn, e ? info('error', `${R} · rewrite`, e) : info(replacement.includes('?') ? 'note' : 'ok', `${R} · rewrite`, replacement.includes('?') ? 'proxy rewrite; a "?" in the replacement replaces the query (nginx would append the original args)' : 'proxy rewrite (nginx rewrite … break)'));
      }
    }
    // request headers (effective proxy_set_header list)
    const list = eff.setHeaders ?? [];
    let up = false;
    let conn = false;
    let hasHost = false;
    for (const h of list) {
      const c = classifyRequestHeader(h.name, h.value);
      if (h.name.toLowerCase() === 'host') hasHost = true;
      if (c.kind === 'websocket') {
        if (h.name.toLowerCase() === 'upgrade') up = true;
        else conn = true;
      } else if (c.kind === 'set') a.setHeaders[h.name] = h.value;
      else if (c.kind === 'remove' && !a.removeHeaders.includes(h.name)) a.removeHeaders.push(h.name);
    }
    a.websocket = up && conn;
    if (lvl.own.setHeaders && (serverEff.setHeaders ?? []).length > 0) locNotes.push("its own proxy_set_header list replaces the server's (nginx inheritance)");
    const missing = ['X-Real-IP', 'X-Forwarded-For', 'X-Forwarded-Proto'].filter((x) => !list.some((h) => h.name.toLowerCase() === x.toLowerCase()));
    if (pp && missing.length) locNotes.push(`OPM also sends ${missing.join(', ')} (nginx did not here)`);
    if (!up) locNotes.push('WebSocket off: nginx did not forward Upgrade here; turn WebSocket on if needed');
    else if (!conn) locNotes.push('WebSocket off: Upgrade is forwarded but Connection is not "upgrade"; turn WebSocket on if needed');
    if (!hasHost && upstreamHost) {
      a.setHeaders['Host'] = upstreamHost;
      locNotes.push(`no proxy_set_header Host: nginx sends Host ${upstreamHost} ($proxy_host) → setHeaders.Host`);
    }
    if (pp && eff.httpVersion === undefined) s.http10 = true;
    action = a;
  } else if (pp) rows.set(pp, info('ignored', null, `never reached: the location ${action.type === 'redirect' ? 'redirects' : 'rewrites'} first`));
  // directives that only matter for proxying
  if (action.type !== 'proxy')
    for (const k of kids)
      if (!k.block && PROXY_ONLY.has(k.name)) rows.set(k, info('ignored', null, `the route ${action.type === 'redirect' ? 'redirects' : 'rewrites'}; proxy settings do not apply`));
  // response headers
  let responseHeaders: EdgeResponseHeaders | null = null;
  if (action.type !== 'rewrite') {
    const mine = toResponseHeaders(eff.addHeaders, eff.hideHeaders);
    if (!sameHeaders(mine, serverHeaders)) {
      responseHeaders = mine;
      if ((lvl.own.addHeaders || lvl.own.hideHeaders) && !isEmptyHeaders(serverHeaders))
        locNotes.push("its own add_header / proxy_hide_header list replaces the server's (nginx inheritance)");
    }
  } else if (lvl.own.addHeaders || lvl.own.hideHeaders) {
    for (const k of kids)
      if (!k.block && (k.name === 'add_header' || k.name === 'proxy_hide_header'))
        rows.set(k, info('unsupported', null, 'a rewrite route has no response of its own: set the headers on the route it rewrites to'));
  }
  const route: EdgeRoute = {
    routeId: id,
    hosts: [],
    match: { kind: lm.kind, path: lm.path, stopRegex: lm.stopRegex, methods: null },
    action,
    order: s.routes.length + 1,
    responseHeaders,
  };
  if (action.type === 'proxy') s.bodyOf.set(route, eff.maxBody);
  s.routes.push(route);
  server.routes.push(route);
  if (locStatus === 'ok' && locNotes.length) locStatus = 'note';
  pushRow(s, node, serverCtx, info(locStatus, R, locNotes.join('; ')));
  for (const k of kids) {
    if (k.block) {
      unmappedBlock(s, k, ctx, info('unsupported', null, k.name === 'location' ? 'nested locations are not supported: make it a separate route' : classifyMisc(k).note));
      continue;
    }
    const r = rows.get(k);
    if (r) pushRow(s, k, ctx, r);
    else misc(s, k, ctx);
  }
}

// ───────────────────────────────────────────────────────────── merge
/**
 * Merge an import into a site's edge. replace: the imported edge (ports, maxBodyBytes, hosts, routes). append: keep the
 * current ports / maxBodyBytes / hosts (add imported hosts that are new), add the imported routes (route ids renamed
 * "-2", "-3" … on collision; orders after the current ones; route.hosts scoped to the imported server names unless "*");
 * an imported proxy route that inherited the imported edge's maxBodyBytes gets it explicitly when it differs from the
 * current edge's; an imported route that inherited its server's response headers gets them explicitly when the host
 * already existed with different ones. Certificate bindings set tls files { certFileSlug, keySecretSlug }.
 */
export function applyNginxImport(current: Edge, result: NginxImportResult, mode: 'append' | 'replace', certs: NginxCertBindings = {}): Edge {
  const imported = normalizeEdge(structuredClone(result.edge) as Edge & Record<string, unknown>);
  const bind = (h: EdgeHost): EdgeHost => {
    const c = certs[h.hostname] ?? certs[h.hostname.toLowerCase()];
    return c && h.tls.mode === 'files' ? { ...h, tls: { mode: 'files', certFileSlug: c.certFileSlug, keySecretSlug: c.keySecretSlug } } : h;
  };
  if (mode === 'replace') return normalizeEdge({ ...imported, hosts: imported.hosts.map(bind) } as Edge & Record<string, unknown>);
  const out = normalizeEdge(structuredClone(current) as Edge & Record<string, unknown>);
  const lower = (x: string) => x.toLowerCase();
  for (const h of imported.hosts) if (!out.hosts.some((x) => lower(x.hostname) === lower(h.hostname))) out.hosts.push(bind(h));
  const importedNames = imported.hosts.map((h) => h.hostname);
  const anyStar = importedNames.includes('*');
  const ids = new Set(out.routes.map((r) => r.routeId));
  const maxOrder = out.routes.reduce((m, r) => Math.max(m, r.order), 0);
  for (const r0 of imported.routes) {
    const r = structuredClone(r0);
    const src = r.hosts.length ? imported.hosts.find((h) => lower(h.hostname) === lower(r.hosts[0]!)) : (imported.hosts.find((h) => h.hostname === '*') ?? imported.hosts[0]);
    if (!r.hosts.length && !anyStar) r.hosts = [...importedNames];
    r.routeId = uniqueRouteId(r.routeId, ids);
    r.order = maxOrder + r.order;
    if (r.action.type === 'proxy' && r.action.maxBodyBytes === null && imported.maxBodyBytes !== out.maxBodyBytes) r.action.maxBodyBytes = imported.maxBodyBytes ?? 0;
    if (r.responseHeaders === null && r.action.type !== 'rewrite' && src) {
      const applies = r.hosts.length ? out.hosts.filter((h) => r.hosts.some((x) => lower(x) === lower(h.hostname))) : out.hosts;
      if (applies.some((h) => !sameHeaders(h.responseHeaders, src.responseHeaders))) r.responseHeaders = structuredClone(src.responseHeaders);
    }
    out.routes.push(r);
  }
  return normalizeEdge(out as Edge & Record<string, unknown>);
}
export type { Edge, EdgeHost, EdgeRoute };
