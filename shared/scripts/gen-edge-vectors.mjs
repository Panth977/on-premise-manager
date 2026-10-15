// Generates testdata/edge-vectors.json from the edges + cases below, using the TS resolver (dist/). Every case carries
// hand-written key expectations (`want`) that are asserted here first, so a resolver bug cannot silently become a vector.
// Run: npm run build && node scripts/gen-edge-vectors.mjs
import { writeFileSync } from 'node:fs';
import assert from 'node:assert/strict';
import { resolveEdgeRequest, edgeVectorView } from '../dist/edge-resolve.js';

const rh = (o = {}) => ({ set: {}, add: {}, remove: [], always: [], ...o });
const proxy = (containerSlug, o = {}) => ({
  type: 'proxy',
  containerSlug,
  portName: 'http',
  stripPrefix: false,
  rewrite: null,
  addPrefix: null,
  setHeaders: {},
  removeHeaders: [],
  websocket: true,
  connectTimeoutSec: 60,
  sendTimeoutSec: 60,
  readTimeoutSec: 60,
  requestBuffering: false,
  responseBuffering: false,
  maxBodyBytes: null,
  ...o,
});
const route = (routeId, kind, path, action, order, o = {}) => ({
  routeId,
  hosts: [],
  match: { kind, path, stopRegex: false, methods: null },
  action,
  order,
  responseHeaders: null,
  ...o,
});

// ── the anonymized client nginx config (e2e/fixtures/client-nginx.conf) as the importer maps it
//    (nginx defaults: proxy_buffering on → responseBuffering true; http-level proxy_request_buffering on)
const NOCACHE = { 'Cache-Control': 'no-store, no-cache, must-revalidate, proxy-revalidate, max-age=0', Pragma: 'no-cache', Expires: '0' };
const S3 = rh({ set: { 'Cache-Control': 'public, max-age=3600, must-revalidate' }, remove: ['Pragma', 'Expires'] });
const nx = (slug, o = {}) => proxy(slug, { requestBuffering: true, responseBuffering: true, ...o });
const strip = (slug, o = {}) => nx(slug, { stripPrefix: true, ...o });
const rw = (slug, pre, o = {}) => nx(slug, { rewrite: { regex: `^${pre}(/.*)$`, replacement: '$1' }, ...o });
const sse = { requestBuffering: false, responseBuffering: false };
let n = 0;
const o = () => ++n;
const client = {
  httpPort: 80,
  httpsPort: 443,
  maxBodyBytes: 104857600,
  hosts: [{ hostname: 'env.client.example.gov.in', tls: { mode: 'files', certFileSlug: 'client-crt', keySecretSlug: 'client-key' }, redirectHttpToHttps: false, responseHeaders: rh({ add: NOCACHE }) }],
  routes: [
    route('x-ez-apis', 'prefix', '/x/ez-apis/', strip('ez-integration'), o()),
    route('x-ez-dc-worker', 'prefix', '/x/ez-dc-worker/', strip('ez-dc'), o()),
    route('x-ez-worker', 'prefix', '/x/ez-worker/', strip('ez-worker'), o()),
    route('x-ez-pg-listen', 'prefix', '/x/ez-pg-listen/', strip('ez-pg-listen'), o()),
    route('x-ez-terminal-real-time', 'prefix', '/x/ez-terminal/real-time/', rw('ez-terminal', '/x/ez-terminal', sse), o()),
    route('x-ez-terminal-s3', 'prefix', '/x/ez-terminal/s3/', rw('ez-terminal', '/x/ez-terminal'), o(), { responseHeaders: S3 }),
    route('x-ez-terminal', 'prefix', '/x/ez-terminal/', strip('ez-terminal'), o()),
    route('x-ez-migrate', 'prefix', '/x/ez-migrate/', strip('ez-migrate'), o()),
    route('x-ez-admin-real-time', 'prefix', '/x/ez-admin/real-time/', rw('ez-admin', '/x/ez-admin', sse), o()),
    route('x-ez-admin-s3', 'prefix', '/x/ez-admin/s3/', rw('ez-admin', '/x/ez-admin'), o(), { responseHeaders: S3 }),
    route('x-ez-admin', 'prefix', '/x/ez-admin/', strip('ez-admin'), o()),
    route('x-admin', 'prefix', '/x/admin/', strip('northwind-admin'), o()),
    route('x-ez-heatmap', 'prefix', '/x/ez-heatmap/', strip('ez-heatmap'), o()),
    route('x-pg-admin', 'prefix', '/x/pg-admin/', nx('pg-admin', { websocket: false, setHeaders: { 'X-Script-Name': '/x/pg-admin' } }), o()),
    route('x-nodered', 'prefix', '/x/nodered/', strip('nodered'), o()),
    route('swagger', 'prefix', '/swagger/', nx('ez-integration'), o()),
    route('apis', 'prefix', '/apis/', strip('ez-integration'), o()),
    route('regex-1', 'regex', '^/apis/(docs|_docs_/[^/]+)$', { type: 'redirect', to: '$uri/$is_args$args', status: 301, keepPath: false }, o()),
    route('root', 'prefix', '/', nx('northwind-web'), o()),
    route('api-v0002-device-types', 'prefix', '/api/v0002/device/types', { type: 'rewrite', regex: '^', replacement: '/x/nodered$request_uri' }, o()),
  ],
};
const clientPorts = Object.fromEntries(
  [
    ['ez-integration', 8080],
    ['ez-dc', 8080],
    ['ez-worker', 8080],
    ['ez-pg-listen', 8080],
    ['ez-terminal', 8080],
    ['ez-migrate', 8080],
    ['ez-admin', 8080],
    ['northwind-admin', 3000],
    ['ez-heatmap', 8080],
    ['pg-admin', 80],
    ['nodered', 1880],
    ['northwind-web', 80],
  ].map(([c, p]) => [c, { http: p }]),
);

// ── a synthetic edge covering every rule
const R = (id, kind, path, action, order, extra = {}) => route(id, kind, path, action, order, extra);
const rules = {
  httpPort: 8081,
  httpsPort: 8443,
  maxBodyBytes: 1024,
  hosts: [
    { hostname: 'a.example', tls: { mode: 'internal' }, redirectHttpToHttps: false, responseHeaders: rh({ set: { 'X-Frame-Options': 'DENY' }, always: ['X-Frame-Options'] }) },
    { hostname: '*', tls: { mode: 'none' }, redirectHttpToHttps: false, responseHeaders: rh({ add: { 'X-Site': 'any' } }) },
  ],
  routes: [
    R('r-exact', 'exact', '/login', proxy('app'), 5),
    R('r-pre-login', 'prefix', '/login', proxy('auth'), 1),
    R('r-static', 'prefix', '/static/', proxy('files'), 2, { match: { kind: 'prefix', path: '/static/', stopRegex: true, methods: null } }),
    R('r-img', 'regex', '\\.(png|jpg)$', proxy('img'), 3),
    R('r-img2', 'regex', '(?i)\\.PNG$', proxy('img2'), 4),
    R('r-api', 'prefix', '/api', proxy('api', { stripPrefix: true, addPrefix: '/v1', setHeaders: { 'X-Real-IP': '$remote_addr', 'X-Orig': '$request_uri' }, removeHeaders: ['X-Forwarded-Host'] }), 6),
    R('r-api-post', 'prefix', '/api/', proxy('apiw', { connectTimeoutSec: 5, sendTimeoutSec: 600, readTimeoutSec: 3600, requestBuffering: true }), 7, {
      match: { kind: 'prefix', path: '/api/', stopRegex: false, methods: ['POST'] },
    }),
    R('r-old', 'prefix', '/old/', { type: 'redirect', to: 'https://new.example/', status: 308, keepPath: true }, 8),
    R('r-user', 'regex', '^/u/([0-9]+)/(\\w+)$', { type: 'redirect', to: '/users/$1?tab=$2', status: 302, keepPath: false }, 9),
    R('r-loop', 'prefix', '/loop', { type: 'rewrite', regex: '^/loop(.*)$', replacement: '/loop$1' }, 10),
    R('r-norw', 'prefix', '/norw', { type: 'rewrite', regex: '^/zzz', replacement: '/x' }, 11),
    R('r-rw', 'prefix', '/rw/', { type: 'rewrite', regex: '^/rw/(.*)$', replacement: '/api/$1?from=rw' }, 12),
    R('r-q', 'prefix', '/q/', proxy('api', { rewrite: { regex: '^/q/(.*)$', replacement: '/search?term=$1' } }), 13),
    R('r-ws', 'prefix', '/ws/', proxy('api', { websocket: false }), 14),
    R('r-big', 'prefix', '/big/', proxy('api', { maxBodyBytes: 0 }), 15),
    R('r-small', 'exact', '/small', proxy('api', { maxBodyBytes: 10 }), 16, { responseHeaders: rh({ add: { 'X-Small': '1' } }) }),
    R('r-host', 'prefix', '/only-a/', { type: 'redirect', to: '/x', status: 303, keepPath: false }, 17, { hosts: ['a.example'] }),
    R('r-root', 'prefix', '/', proxy('app'), 18),
  ],
};
const rulesPorts = { app: { http: 80 }, auth: { http: 9000 }, files: { http: 8080 }, img: { http: 81 }, img2: { http: 82 }, api: { http: 3000 }, apiw: { http: 3001 } };

const H = 'http://env.client.example.gov.in';
const cases = [
  // client fixture — every location
  { edge: 'client', url: `${H}/x/ez-terminal/real-time/x`, want: { status: 200, routeId: 'x-ez-terminal-real-time', upstreamUrl: 'http://ez-terminal:8080/real-time/x' }, note: 'SSE route (buffering off), rewrite keeps /real-time' },
  { edge: 'client', url: `${H}/x/ez-terminal/s3/a`, want: { status: 200, routeId: 'x-ez-terminal-s3', upstreamUrl: 'http://ez-terminal:8080/s3/a' }, note: 's3: route response headers replace the host no-cache ones' },
  { edge: 'client', url: `${H}/x/ez-terminal/y`, want: { status: 200, routeId: 'x-ez-terminal', upstreamUrl: 'http://ez-terminal:8080/y' } },
  { edge: 'client', url: `${H}/apis/docs`, want: { status: 301, routeId: 'regex-1', location: '/apis/docs/' }, note: 'regex beats the longest prefix /apis/' },
  { edge: 'client', url: `${H}/apis/docs?a=1`, want: { status: 301, location: '/apis/docs/?a=1' } },
  { edge: 'client', url: `${H}/apis/_docs_/v2`, want: { status: 301, location: '/apis/_docs_/v2/' } },
  { edge: 'client', url: `${H}/apis/docs/`, want: { status: 200, routeId: 'apis', upstreamUrl: 'http://ez-integration:8080/docs/' } },
  {
    edge: 'client',
    url: `${H}/api/v0002/device/types?x=1`,
    want: { status: 200, routeId: 'x-nodered', upstreamUrl: 'http://nodered:1880/api/v0002/device/types?x=1', rewrites: 1 },
    note: 'internal rewrite (nginx last) then re-match',
  },
  { edge: 'client', url: `${H}/`, want: { status: 200, routeId: 'root', upstreamUrl: 'http://northwind-web:80/' } },
  { edge: 'client', url: `${H}/dashboard/42`, want: { status: 200, routeId: 'root', upstreamUrl: 'http://northwind-web:80/dashboard/42' } },
  { edge: 'client', url: `${H}/swagger/ui`, want: { status: 200, routeId: 'swagger', upstreamUrl: 'http://ez-integration:8080/swagger/ui' }, note: 'no strip' },
  { edge: 'client', url: `${H}/x/ez-apis/v1/devices?limit=5`, want: { status: 200, routeId: 'x-ez-apis', upstreamUrl: 'http://ez-integration:8080/v1/devices?limit=5' } },
  { edge: 'client', url: `${H}/x/ez-dc-worker/a`, want: { routeId: 'x-ez-dc-worker', upstreamUrl: 'http://ez-dc:8080/a' } },
  { edge: 'client', url: `${H}/x/ez-worker/a`, want: { routeId: 'x-ez-worker', upstreamUrl: 'http://ez-worker:8080/a' } },
  { edge: 'client', url: `${H}/x/ez-pg-listen/a`, want: { routeId: 'x-ez-pg-listen', upstreamUrl: 'http://ez-pg-listen:8080/a' } },
  { edge: 'client', url: `${H}/x/ez-migrate/a`, want: { routeId: 'x-ez-migrate', upstreamUrl: 'http://ez-migrate:8080/a' } },
  { edge: 'client', url: `${H}/x/ez-admin/real-time/s`, want: { routeId: 'x-ez-admin-real-time', upstreamUrl: 'http://ez-admin:8080/real-time/s' } },
  { edge: 'client', url: `${H}/x/ez-admin/s3/k/v.png`, want: { routeId: 'x-ez-admin-s3', upstreamUrl: 'http://ez-admin:8080/s3/k/v.png' } },
  { edge: 'client', url: `${H}/x/ez-admin/z`, want: { routeId: 'x-ez-admin', upstreamUrl: 'http://ez-admin:8080/z' } },
  { edge: 'client', url: `${H}/x/admin/`, want: { routeId: 'x-admin', upstreamUrl: 'http://northwind-admin:3000/' } },
  { edge: 'client', url: `${H}/x/ez-heatmap/t/1`, want: { routeId: 'x-ez-heatmap', upstreamUrl: 'http://ez-heatmap:8080/t/1' } },
  { edge: 'client', url: `${H}/x/pg-admin/browser/`, want: { routeId: 'x-pg-admin', upstreamUrl: 'http://pg-admin:80/x/pg-admin/browser/' }, note: 'X-Script-Name, Host kept, no strip' },
  { edge: 'client', url: `${H}/x/nodered/flows`, want: { routeId: 'x-nodered', upstreamUrl: 'http://nodered:1880/flows' } },
  { edge: 'client', url: `${H}/x/ez-terminal`, want: { status: 301, location: '/x/ez-terminal/', autoRedirect: true }, note: 'nginx auto-redirect' },
  { edge: 'client', url: `${H}/x/nodered?a=b`, want: { status: 301, location: '/x/nodered/?a=b' } },
  { edge: 'client', url: 'https://env.client.example.gov.in/x/admin/login', want: { status: 200, upstreamUrl: 'http://northwind-admin:3000/login' }, note: 'TLS files on 443, no redirect on 80' },
  { edge: 'client', url: `${H}/x/ez-terminal/s3/big`, method: 'PUT', bodyBytes: 104857601, want: { status: 413 } },
  { edge: 'client', url: `${H}/x/ez-terminal/s3/big`, method: 'PUT', bodyBytes: 104857600, want: { status: 200 } },
  { edge: 'client', url: `${H}/x/ez-terminal/real-time/ws`, upgrade: true, want: { status: 200 } },
  { edge: 'client', url: `${H}/x/pg-admin/ws`, upgrade: true, want: { status: 400 }, note: 'its own proxy_set_header list does not forward Upgrade' },
  { edge: 'client', url: `${H}/_opm/app`, want: { status: 200, kind: 'agent' } },
  // rules
  { edge: 'rules', url: 'http://x.example:8081/login', want: { routeId: 'r-exact' } },
  { edge: 'rules', url: 'http://x.example:8081/login/x', want: { routeId: 'r-pre-login' } },
  { edge: 'rules', url: 'http://x.example:8081/loginx', want: { routeId: 'r-pre-login' }, note: 'plain string prefix (nginx)' },
  { edge: 'rules', url: 'http://x.example:8081/static/a.png', want: { routeId: 'r-static' }, note: 'stopRegex' },
  { edge: 'rules', url: 'http://x.example:8081/static', want: { status: 301, location: '/static/' } },
  { edge: 'rules', url: 'http://x.example:8081/img/a.png', want: { routeId: 'r-img', captures: 1 } },
  { edge: 'rules', url: 'http://x.example:8081/img/a.PnG', want: { routeId: 'r-img2' }, note: '(?i)' },
  { edge: 'rules', url: 'http://x.example:8081/apix', want: { routeId: 'r-api', upstreamUrl: 'http://api:3000/v1/x' } },
  { edge: 'rules', url: 'http://x.example:8081/api/a?b=1', want: { routeId: 'r-api', upstreamUrl: 'http://api:3000/v1/a?b=1' } },
  { edge: 'rules', url: 'http://x.example:8081/api/a', method: 'POST', want: { routeId: 'r-api-post', upstreamUrl: 'http://apiw:3001/api/a' } },
  { edge: 'rules', url: 'http://x.example:8081/old/a/b?c=1', want: { status: 308, location: 'https://new.example/a/b?c=1' } },
  { edge: 'rules', url: 'http://x.example:8081/u/42/posts', want: { status: 302, location: '/users/42?tab=posts' } },
  { edge: 'rules', url: 'http://x.example:8081/loop', want: { status: 500, kind: 'rewrite-loop', rewrites: 10 } },
  { edge: 'rules', url: 'http://x.example:8081/norw', want: { status: 404, kind: 'not-found' } },
  { edge: 'rules', url: 'http://x.example:8081/rw/x', want: { routeId: 'r-api', upstreamUrl: 'http://api:3000/v1/x?from=rw', rewrites: 1 } },
  { edge: 'rules', url: 'http://x.example:8081/q/abc?old=1', want: { routeId: 'r-q', upstreamUrl: 'http://api:3000/search?term=abc' } },
  { edge: 'rules', url: 'http://x.example:8081/ws/a', upgrade: true, want: { status: 400, kind: 'ws-refused' } },
  { edge: 'rules', url: 'http://x.example:8081/big/u', method: 'POST', bodyBytes: 5000, want: { status: 200, routeId: 'r-big' } },
  { edge: 'rules', url: 'http://x.example:8081/x', method: 'POST', bodyBytes: 5000, want: { status: 413, routeId: 'r-root' } },
  { edge: 'rules', url: 'http://x.example:8081/small', method: 'POST', bodyBytes: 11, want: { status: 413, routeId: 'r-small' } },
  { edge: 'rules', url: 'http://x.example:8081/small', want: { status: 200, routeId: 'r-small' } },
  { edge: 'rules', url: 'http://a.example:8081/only-a/', want: { status: 303, location: '/x' } },
  { edge: 'rules', url: 'http://b.example:8081/only-a/', want: { routeId: 'r-root' } },
  { edge: 'rules', url: 'http://a.example:8081/home', want: { routeId: 'r-root' }, note: 'host response headers with always' },
  { edge: 'rules', url: 'https://a.example:8443/home', want: { routeId: 'r-root' } },
  { edge: 'rules', url: 'https://b.example:8443/home', want: { status: 0, kind: 'tls-fail' } },
  { edge: 'rules', url: 'http://x.example:8081/_opm/peer/ping', want: { status: 404, kind: 'agent' } },
  { edge: 'rules', url: 'http://x.example:8081/a%20b', want: { routeId: 'r-root', upstreamUrl: 'http://app:80/a b' }, note: 'matching uses the decoded path' },
];

const EDGES = { client, rules };
const PORTS = { client: clientPorts, rules: rulesPorts };
const out = [];
for (const c of cases) {
  const opts = { method: c.method, ports: PORTS[c.edge], bodyBytes: c.bodyBytes, upgrade: c.upgrade };
  const res = resolveEdgeRequest(EDGES[c.edge], c.url, opts);
  const v = edgeVectorView(res);
  const w = c.want;
  const ctx = `${c.edge} ${c.method ?? 'GET'} ${c.url}`;
  if ('status' in w) assert.equal(v.status, w.status, ctx);
  else assert.equal(v.status, 200, ctx);
  if ('kind' in w) assert.equal(v.kind, w.kind, ctx);
  if ('routeId' in w) assert.equal(v.routeId, w.routeId, ctx);
  if ('location' in w) assert.equal(v.location, w.location, ctx);
  if ('upstreamUrl' in w) assert.equal(v.upstreamUrl, w.upstreamUrl, ctx);
  if ('rewrites' in w) assert.equal(v.rewrites.length, w.rewrites, ctx);
  if ('captures' in w) assert.equal(v.captures.length - 1, w.captures, ctx);
  if ('autoRedirect' in w) assert.equal(v.autoRedirect, w.autoRedirect, ctx);
  const { want, ...input } = c;
  void want;
  out.push({ ...input, expect: v });
}
const doc = {
  _doc:
    'Shared edge resolver vectors (SPEC-final §12.1). TS: shared/src/edge-vectors.test.ts; Go: agent/internal/edgeres. For each case resolve(edges[edge], url, {method, ports: ports[edge], bodyBytes, upgrade}) and compare edgeVectorView(result) with expect. Generated by shared/scripts/gen-edge-vectors.mjs (hand-written key expectations asserted there). Request header values: "{client-ip}" stands for the client address.',
  edges: EDGES,
  ports: PORTS,
  cases: out,
};
writeFileSync(new URL('../testdata/edge-vectors.json', import.meta.url), JSON.stringify(doc, null, 2) + '\n');
console.log(`edge-vectors.json: ${out.length} cases`);
