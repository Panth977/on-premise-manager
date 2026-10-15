import { test } from 'node:test';
import assert from 'node:assert/strict';
import { resolveEdgeRequest, prefixHit, matchHost, redirectLocation, upstreamPath, expandEdgeVars, checkEdgeRegex, selectRoute, effectiveRouteOrder } from './edge-resolve.js';
import { normalizeEdge } from './validate.js';
import type { Edge } from './types.js';

const LEGACY = {
  httpPort: 80,
  httpsPort: 443,
  hosts: [
    { hostname: 'plant.acme.com', tls: { mode: 'acme', email: 'it@acme.com', challenge: 'http-01', dnsProvider: null, dnsTokenSecretSlug: null }, redirectHttpToHttps: true },
    { hostname: '10.0.1.10', tls: { mode: 'internal' }, redirectHttpToHttps: false },
    { hostname: '*.dev.acme.com', tls: { mode: 'internal' }, redirectHttpToHttps: false },
    { hostname: '*', tls: { mode: 'none' }, redirectHttpToHttps: false },
  ],
  routes: [
    { routeId: 'web', hosts: [], match: { pathPrefix: '/', methods: null }, action: { type: 'proxy', containerSlug: 'web', portName: 'http', stripPrefix: false, addPrefix: null, setHeaders: {}, removeHeaders: [], websocket: true, timeoutSec: 60 }, order: 4 },
    { routeId: 'api', hosts: [], match: { pathPrefix: '/api/', methods: null }, action: { type: 'proxy', containerSlug: 'api', portName: 'http', stripPrefix: true, addPrefix: null, setHeaders: { 'X-Forwarded-Prefix': '/api' }, removeHeaders: [], websocket: false, timeoutSec: 60 }, order: 1 },
    { routeId: 'old', hosts: [], match: { pathPrefix: '/old', methods: null }, action: { type: 'redirect', to: '/new', status: 308, keepPath: true }, order: 2 },
    { routeId: 'docs', hosts: ['plant.acme.com'], match: { pathPrefix: '/docs', methods: null }, action: { type: 'redirect', to: 'https://docs.acme.com{path}', status: 301, keepPath: false }, order: 3 },
    { routeId: 'legacy', hosts: ['10.0.1.10'], match: { pathPrefix: '/legacy', methods: ['GET'] }, action: { type: 'proxy', containerSlug: 'api', portName: 'http', stripPrefix: true, addPrefix: '/v0', setHeaders: {}, removeHeaders: [], websocket: false, timeoutSec: 30 }, order: 3 },
  ],
};
/** legacy shape (pathPrefix, timeoutSec, no responseHeaders) normalized to round E */
const EDGE: Edge = normalizeEdge(LEGACY as never);
const OPTS = { ports: { web: { http: 80 }, api: { http: 3000 } } };

test('legacy normalization', () => {
  const api = EDGE.routes.find((r) => r.routeId === 'api')!;
  assert.deepEqual(api.match, { kind: 'prefix', path: '/api/', stopRegex: false, methods: null });
  assert.equal(api.action.type === 'proxy' && api.action.readTimeoutSec, 60);
  assert.equal(api.action.type === 'proxy' && api.action.connectTimeoutSec, 60);
  assert.equal(api.action.type === 'proxy' && api.action.websocket, false, 'websocket kept as stored');
  assert.equal('timeoutSec' in api.action, false);
  assert.equal(api.responseHeaders, null);
  assert.deepEqual(EDGE.hosts[0]!.responseHeaders, { set: {}, add: {}, remove: [], always: [] });
  assert.equal(EDGE.maxBodyBytes, null);
  assert.deepEqual(normalizeEdge(EDGE as never), EDGE, 'idempotent');
});

test('prefixHit (nginx plain prefix)', () => {
  assert.ok(prefixHit('/api', '/api'));
  assert.ok(prefixHit('/api', '/api/v1'));
  assert.ok(prefixHit('/api', '/apix'));
  assert.ok(prefixHit('/', '/anything'));
  assert.ok(prefixHit('/api/', '/api/x'));
  assert.ok(!prefixHit('/api/', '/api'));
});

test('matchHost order', () => {
  assert.equal(matchHost(EDGE, 'PLANT.acme.com')!.how, 'exact');
  assert.equal(matchHost(EDGE, 'a.dev.acme.com')!.how, 'wildcard');
  assert.equal(matchHost(EDGE, 'a.b.dev.acme.com')!.how, 'any');
  assert.equal(matchHost(EDGE, 'other.example')!.how, 'any');
  assert.equal(matchHost({ ...EDGE, hosts: EDGE.hosts.slice(0, 2) }, 'x.example'), null);
});

test('proxy with strip prefix keeps the query', () => {
  const r = resolveEdgeRequest(EDGE, 'https://plant.acme.com/api/v1/x?id=7', OPTS);
  assert.equal(r.kind, 'proxy');
  assert.equal(r.status, 200);
  assert.equal(r.route!.routeId, 'api');
  assert.equal(r.upstream!.url, 'http://api:3000/v1/x?id=7');
});

test('strip prefix of the prefix itself gives "/"', () => {
  assert.equal(resolveEdgeRequest(EDGE, 'https://plant.acme.com/api/', OPTS).upstream!.path, '/');
});

test('http → https redirect for a host with redirectHttpToHttps', () => {
  const r = resolveEdgeRequest(EDGE, 'http://plant.acme.com/api/v1/x?q=1', OPTS);
  assert.equal(r.status, 308);
  assert.equal(r.location, 'https://plant.acme.com/api/v1/x?q=1');
});

test('redirect keepPath', () => {
  const r = resolveEdgeRequest(EDGE, 'https://plant.acme.com/old/a?b=1', OPTS);
  assert.equal(r.status, 308);
  assert.equal(r.location, '/new/a?b=1');
  assert.equal(resolveEdgeRequest(EDGE, 'https://plant.acme.com/old', OPTS).location, '/new');
});

test('redirect with placeholder, host-specific route', () => {
  const r = resolveEdgeRequest(EDGE, 'https://plant.acme.com/docs/setup', OPTS);
  assert.equal(r.status, 301);
  assert.equal(r.location, 'https://docs.acme.com/docs/setup');
  // /docs route is only for plant.acme.com → on 10.0.1.10 the "/" route (web) wins
  assert.equal(resolveEdgeRequest(EDGE, 'https://10.0.1.10/docs/setup', OPTS).route!.routeId, 'web');
});

test('add prefix after strip + method filter', () => {
  const r = resolveEdgeRequest(EDGE, 'https://10.0.1.10/legacy/users', OPTS);
  assert.equal(r.upstream!.url, 'http://api:3000/v0/users');
  const p = resolveEdgeRequest(EDGE, 'https://10.0.1.10/legacy/users', { ...OPTS, method: 'POST' });
  assert.equal(p.route!.routeId, 'web');
});

test('/apix is not /api/ (prefix with slash)', () => {
  assert.equal(resolveEdgeRequest(EDGE, 'http://10.0.1.10/apix', OPTS).route!.routeId, 'web');
});

test('nginx auto-redirect /api → /api/ (proxy prefix with slash)', () => {
  const r = resolveEdgeRequest(EDGE, 'http://10.0.1.10/api?a=1', OPTS);
  assert.equal(r.status, 301);
  assert.equal(r.location, '/api/?a=1');
  assert.equal(r.autoRedirect, true);
});

test('variables', () => {
  const v = { uri: '/a/b', args: 'x=1', requestUri: '/orig?x=1', host: 'h', scheme: 'https', captures: ['/a/b', 'a', 'b'] };
  assert.equal(expandEdgeVars('$uri/$is_args$args', v), '/a/b/?x=1');
  assert.equal(expandEdgeVars('${scheme}://$host$request_uri', v), 'https://h/orig?x=1');
  assert.equal(expandEdgeVars('/$2/$1$9{query}', v), '/b/a?x=1');
  assert.equal(expandEdgeVars('$unknown and $ alone', v), '$unknown and $ alone');
  assert.equal(expandEdgeVars('$http_x_token', { ...v, headers: { 'x-token': 't' } }), 't');
});

test('regex checks (RE2 ∩ JS)', () => {
  assert.equal(checkEdgeRegex('^/apis/(docs|_docs_/[^/]+)$'), null);
  assert.equal(checkEdgeRegex('(?i)\\.png$'), null);
  assert.equal(checkEdgeRegex('^/u/(?<id>[0-9]+)$'), null);
  for (const bad of ['(?=x)', 'a(?!b)', '(?<=a)b', '(a)\\1', '(?P<n>x)', '(?s).', '[[:alpha:]]', '\\Aabc', 'a++', '(', '']) assert.ok(checkEdgeRegex(bad), `expected an error for ${bad}`);
});

test('selection order: exact > longest prefix (stop) > regex > prefix', () => {
  const p = (routeId: string, kind: 'prefix' | 'exact' | 'regex', path: string, order = 1, stopRegex = false) => ({
    routeId,
    hosts: [],
    match: { kind, path, stopRegex, methods: null },
    action: { type: 'redirect' as const, to: '/', status: 302 as const, keepPath: false },
    order,
    responseHeaders: null,
  });
  const rs = [p('root', 'prefix', '/'), p('ex', 'exact', '/a'), p('a', 'prefix', '/a'), p('st', 'prefix', '/s/', 1, true), p('png', 'regex', '\\.png$', 2), p('any', 'regex', '.', 3)];
  assert.equal(selectRoute(rs, '/a')!.route.routeId, 'ex');
  assert.equal(selectRoute(rs, '/s/x.png')!.route.routeId, 'st');
  assert.equal(selectRoute(rs, '/a/x.png')!.route.routeId, 'png');
  assert.equal(selectRoute(rs, '/b')!.route.routeId, 'any');
  assert.deepEqual(effectiveRouteOrder(rs).map((o) => o.routeId), ['ex', 'st', 'png', 'any', 'a', 'root']);
});

test('reserved /_opm', () => {
  const a = resolveEdgeRequest(EDGE, 'https://10.0.1.10/_opm/app', OPTS);
  assert.equal(a.kind, 'agent');
  assert.equal(a.agent, 'gui');
  const b = resolveEdgeRequest(EDGE, 'http://10.0.1.10/_opm/nope', OPTS);
  assert.equal(b.status, 404);
  assert.equal(b.agent, 'agent-404');
  assert.equal(resolveEdgeRequest(EDGE, 'http://10.0.1.10/_opm/peer/ping', OPTS).agent, 'agent-404');
  assert.equal(resolveEdgeRequest(EDGE, 'http://10.0.1.10/_opm/api/info', OPTS).agent, 'gui');
});

test('tls none on https → handshake fails; no host → 404 / tls fail', () => {
  assert.equal(resolveEdgeRequest(EDGE, 'https://other.example/', OPTS).kind, 'tls-fail');
  const noStar: Edge = { ...EDGE, hosts: EDGE.hosts.filter((h) => h.hostname !== '*') };
  assert.equal(resolveEdgeRequest(noStar, 'http://other.example/', OPTS).status, 404);
  assert.equal(resolveEdgeRequest(noStar, 'https://other.example/', OPTS).status, 0);
});

test('not ready → 503; no route → 404; wrong port', () => {
  assert.equal(resolveEdgeRequest(EDGE, 'https://10.0.1.10/', { ...OPTS, notReady: { web: true } }).status, 503);
  const noRoutes: Edge = { ...EDGE, routes: [] };
  assert.equal(resolveEdgeRequest(noRoutes, 'http://10.0.1.10/x', OPTS).status, 404);
  assert.equal(resolveEdgeRequest(EDGE, 'http://10.0.1.10:8080/', OPTS).kind, 'not-edge-port');
  assert.equal(resolveEdgeRequest({ ...EDGE, httpPort: 8081 }, 'http://127.0.0.1:8081/api/count', OPTS).upstream!.url, 'http://api:3000/count');
  assert.equal(resolveEdgeRequest(EDGE, 'http://10.0.1.10/api/x', { ...OPTS, upgrade: true }).status, 400);
});

test('helpers', () => {
  assert.equal(redirectLocation('https://x{path}{query}', false, '/a', '/a/b', '?q=1', 'h'), 'https://x/a/b?q=1');
  assert.equal(redirectLocation('https://{host}/new/', true, '/old', '/old/a', '', 'h.example'), 'https://h.example/new/a');
  assert.equal(upstreamPath({ stripPrefix: true, addPrefix: '/v0/' }, '/', '/x'), '/v0/x');
});
