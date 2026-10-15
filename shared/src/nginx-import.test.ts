import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import {
  parseNginx,
  importNginx,
  applyNginxImport,
  parseNginxSize,
  parseNginxTime,
  routeIdFromLocation,
  suggestUpstream,
  type NginxImportContainer,
  type NginxImportResult,
  type NginxMapRow,
} from './nginx-import.js';
import { resolveEdgeRequest, edgeVectorView } from './edge-resolve.js';
import { defaultEdge, normalizeEdge, validateEdge, Issues, newEdgeHost } from './validate.js';
import type { Edge, EdgeProxyAction, EdgeRoute } from './types.js';

const read = (p: string) => readFileSync(fileURLToPath(new URL(p, import.meta.url)), 'utf8');
const FIXTURE = read('../testdata/client-nginx.conf');
const SNAP = JSON.parse(read('../testdata/nginx-import-snapshot.json')) as { containers: NginxImportContainer[]; result: NginxImportResult };
interface Vec {
  edges: Record<string, Edge>;
  ports: Record<string, Record<string, Record<string, number>>>;
  cases: { edge: string; url: string; method?: string; bodyBytes?: number; upgrade?: boolean; expect: unknown }[];
}
const V = JSON.parse(read('../testdata/edge-vectors.json')) as Vec;
const CERTS = { 'env.client.example.gov.in': { certFileSlug: 'client-crt', keySecretSlug: 'client-key' } };
const C = SNAP.containers;
const imp = (text: string, containers: NginxImportContainer[] = C) => importNginx(text, { containers });
const route = (r: NginxImportResult, id: string): EdgeRoute => {
  const x = r.edge.routes.find((y) => y.routeId === id);
  assert.ok(x, `route ${id} exists (have ${r.edge.routes.map((y) => y.routeId).join(', ')})`);
  return x;
};
const proxy = (r: NginxImportResult, id: string): EdgeProxyAction => {
  const a = route(r, id).action;
  assert.equal(a.type, 'proxy');
  return a as EdgeProxyAction;
};
const row = (r: NginxImportResult, line: number, directive?: string): NginxMapRow => {
  const x = r.rows.find((y) => y.line === line && (!directive || y.directive === directive));
  assert.ok(x, `row at line ${line}`);
  return x;
};
/** wrap location bodies in a minimal http/server */
const srv = (body: string, head = '') => `http { ${head} server { listen 80; server_name a.example; ${body} } }`;
const ok = (status: string) => ['ok', 'note', 'default'].includes(status);

// ── (1) snapshot
test('nginx import: client fixture matches the snapshot', () => {
  assert.deepEqual(JSON.parse(JSON.stringify(imp(FIXTURE))), SNAP.result);
});
test('nginx import: client fixture parses cleanly and maps every row', () => {
  const r = imp(FIXTURE);
  assert.deepEqual(r.parseErrors, []);
  assert.equal(r.summary.error, 0);
  assert.equal(r.summary.unsupported, 0);
  assert.equal(r.summary.routes, 20);
  assert.equal(r.summary.hosts, 1);
  assert.deepEqual(r.certs, [{ hostname: 'env.client.example.gov.in', certPath: '/etc/nginx/certs/client.crt', keyPath: '/etc/nginx/certs/client.key' }]);
  // one row per directive / block, in file order
  const lines = r.rows.map((x) => x.line);
  assert.deepEqual(lines, [...lines].sort((a, b) => a - b));
  const directives = FIXTURE.split('\n').filter((l) => /^\s*[a-z_]+[\s;{]/.test(l) && !/^\s*#/.test(l)).length;
  assert.equal(r.rows.length, directives);
});

// ── (2) replace import of the fixture = the client edge of the shared vectors
test('nginx import: replace into defaultEdge = edge-vectors client edge', () => {
  const e = applyNginxImport(defaultEdge(), imp(FIXTURE), 'replace', CERTS);
  assert.deepEqual(e, V.edges['client']);
  assert.deepEqual(normalizeEdge(e as never), e);
  const is = new Issues();
  validateEdge(e, { agentPort: 7080, agentGuiAliasPort: 8080 }, is);
  assert.deepEqual(is.list.filter((x) => x.severity === 'error'), []);
});

// ── (3) every client vector resolves the same through the imported edge
test('nginx import: client vectors resolve the same through the imported edge', () => {
  const e = applyNginxImport(defaultEdge(), imp(FIXTURE), 'replace', CERTS);
  const cases = V.cases.filter((c) => c.edge === 'client');
  assert.ok(cases.length > 20);
  for (const c of cases) {
    const r = resolveEdgeRequest(e, c.url, { method: c.method, ports: V.ports['client'], bodyBytes: c.bodyBytes, upgrade: c.upgrade });
    assert.deepEqual(edgeVectorView(r), c.expect, c.url);
  }
});

// ── (4) units
test('nginx import: fixture rows (resolver, worker_processes, add_header, s3 headers)', () => {
  const r = imp(FIXTURE);
  assert.equal(row(r, 1).status, 'ignored');
  assert.equal(row(r, 124).status, 'ignored');
  assert.match(row(r, 124).note, /resolves containers itself/);
  for (const l of [22, 23, 24]) assert.equal(row(r, l).target, 'host env.client.example.gov.in · responseHeaders.add');
  assert.deepEqual(r.edge.hosts[0]!.responseHeaders.add, { 'Cache-Control': 'no-store, no-cache, must-revalidate, proxy-revalidate, max-age=0', Pragma: 'no-cache', Expires: '0' });
  assert.equal(row(r, 66).target, 'route x-ez-terminal-s3 · responseHeaders.set');
  assert.equal(row(r, 67).target, 'route x-ez-terminal-s3 · responseHeaders.remove');
  assert.deepEqual(route(r, 'x-ez-terminal-s3').responseHeaders, { set: { 'Cache-Control': 'public, max-age=3600, must-revalidate' }, add: {}, remove: ['Pragma', 'Expires'], always: [] });
  // add_header never becomes a request header
  for (const x of r.edge.routes) if (x.action.type === 'proxy') for (const k of Object.keys(x.action.setHeaders)) assert.ok(!/cache-control|pragma|expires/i.test(k), k);
});
test('nginx import: http-level client_max_body_size present → no synthetic default row; absent → 1m', () => {
  assert.ok(!imp(FIXTURE).rows.some((x) => x.text.startsWith('(default)')));
  const r = imp(srv('location / { proxy_pass http://northwind-web; }'));
  assert.equal(r.edge.maxBodyBytes, 1024 * 1024);
  const d = r.rows.find((x) => x.text === '(default) client_max_body_size 1m')!;
  assert.equal(d.line, 0);
  assert.equal(d.status, 'default');
  assert.ok(r.rows.some((x) => x.text === '(default) proxy_http_version 1.0' && x.status === 'note'));
  assert.equal(proxy(r, 'root').requestBuffering, true);
  assert.equal(proxy(r, 'root').responseBuffering, true);
});
test('nginx import: location modifiers', () => {
  const r = imp(
    srv(`
      location = /login { proxy_pass http://northwind-web:80; }
      location ^~ /static/ { proxy_pass http://northwind-web:80; }
      location ~ \\.(png|jpg)$ { proxy_pass http://northwind-web:80; }
      location ~* \\.PNG$ { proxy_pass http://northwind-web:80; }
      location ~ (?<=x)y { proxy_pass http://northwind-web:80; }
      location @fallback { proxy_pass http://northwind-web:80; }
      location / { proxy_pass http://northwind-web:80; }
      location = / { proxy_pass http://northwind-web:80; }
    `),
  );
  assert.deepEqual(route(r, 'login').match, { kind: 'exact', path: '/login', stopRegex: false, methods: null });
  assert.deepEqual(route(r, 'static').match, { kind: 'prefix', path: '/static/', stopRegex: true, methods: null });
  assert.deepEqual(route(r, 'regex-1').match, { kind: 'regex', path: '\\.(png|jpg)$', stopRegex: false, methods: null });
  assert.deepEqual(route(r, 'regex-2').match, { kind: 'regex', path: '(?i)\\.PNG$', stopRegex: false, methods: null });
  assert.equal(route(r, 'regex-3').match.path, '(?<=x)y');
  assert.equal(r.rows.find((x) => x.target === 'route regex-3' && x.directive === 'location')!.status, 'error');
  assert.ok(r.rows.some((x) => x.text.startsWith('location @fallback') && x.status === 'unsupported'));
  assert.deepEqual(
    r.edge.routes.map((x) => [x.routeId, x.order]),
    [['login', 1], ['static', 2], ['regex-1', 3], ['regex-2', 4], ['regex-3', 5], ['root', 6], ['root-2', 7]],
  );
  const is = new Issues();
  validateEdge(r.edge, { agentPort: 7080, agentGuiAliasPort: 8080 }, is);
  assert.ok(is.list.some((x) => x.code === 'V_EDGE_REGEX'));
});
test('nginx import: sizes, times, route ids', () => {
  assert.equal(parseNginxSize('100M'), 104857600);
  assert.equal(parseNginxSize('512k'), 524288);
  assert.equal(parseNginxSize('1g'), 1073741824);
  assert.equal(parseNginxSize('1024'), 1024);
  assert.equal(parseNginxSize('0'), 0);
  assert.equal(parseNginxSize('1x'), null);
  assert.equal(parseNginxTime('60s'), 60_000);
  assert.equal(parseNginxTime('60'), 60_000);
  assert.equal(parseNginxTime('500ms'), 500);
  assert.equal(parseNginxTime('1h30m'), 5_400_000);
  assert.equal(parseNginxTime('2d'), 172_800_000);
  assert.equal(parseNginxTime('abc'), null);
  assert.equal(routeIdFromLocation('/'), 'root');
  assert.equal(routeIdFromLocation('/X/Ez_Apis/'), 'x-ez-apis');
  assert.equal(routeIdFromLocation('/' + 'a'.repeat(80)).length, 63);
  const r = imp(
    srv(`
      location /a/ { proxy_pass http://northwind-web; proxy_read_timeout 500ms; proxy_connect_timeout 2h; proxy_send_timeout 3d; client_max_body_size 0; }
      location /b/ { proxy_pass http://northwind-web; client_max_body_size 5m; }
      location /c/ { proxy_pass http://northwind-web; client_max_body_size 100M; }
    `, 'client_max_body_size 100M;'),
  );
  const a = proxy(r, 'a');
  assert.equal(a.readTimeoutSec, 1);
  assert.equal(a.connectTimeoutSec, 3600);
  assert.equal(a.sendTimeoutSec, 86400);
  assert.equal(a.maxBodyBytes, 0);
  assert.equal(proxy(r, 'b').maxBodyBytes, 5 * 1024 * 1024);
  assert.equal(proxy(r, 'c').maxBodyBytes, null, 'equal to the inherited value → null');
  assert.ok(r.rows.some((x) => x.directive === 'proxy_connect_timeout' && x.status === 'note' && /clamped/.test(x.note)));
});
test('nginx import: server-level body limit → explicit on its routes; http 0 → edge null', () => {
  const r = imp(`http { client_max_body_size 0; server { client_max_body_size 2m; location / { proxy_pass http://northwind-web; } } server { server_name b.example; location / { proxy_pass http://northwind-web; } } }`);
  assert.equal(r.edge.maxBodyBytes, null);
  assert.equal(proxy(r, 'root').maxBodyBytes, 2 * 1024 * 1024);
  assert.equal(proxy(r, 'root-2').maxBodyBytes, null);
});
test('nginx import: add_header / proxy_set_header inheritance replaces the outer list', () => {
  const r = imp(
    srv(
      `
      add_header X-Server 1;
      proxy_set_header X-A a;
      proxy_set_header Upgrade $http_upgrade;
      proxy_set_header Connection $connection_upgrade;
      location /inherit/ { proxy_pass http://northwind-web; }
      location /own/ { proxy_pass http://northwind-web; add_header X-Loc 2 always; proxy_set_header X-B b; }
      location /hide/ { proxy_pass http://northwind-web; proxy_hide_header X-Powered-By; }
    `,
      'add_header X-Http 0;',
    ),
  );
  const h = r.edge.hosts[0]!;
  assert.deepEqual(h.responseHeaders, { set: {}, add: { 'X-Server': '1' }, remove: [], always: [] }, 'server list replaces the http list');
  assert.equal(route(r, 'inherit').responseHeaders, null);
  assert.deepEqual(proxy(r, 'inherit').setHeaders, { 'X-A': 'a', Host: 'northwind-web' });
  assert.equal(proxy(r, 'inherit').websocket, true);
  assert.deepEqual(route(r, 'own').responseHeaders, { set: {}, add: { 'X-Loc': '2' }, remove: [], always: ['X-Loc'] });
  assert.deepEqual(proxy(r, 'own').setHeaders, { 'X-B': 'b', Host: 'northwind-web' }, 'own list replaces; no Host → $proxy_host');
  assert.equal(proxy(r, 'own').websocket, false);
  assert.match(r.rows.find((x) => x.target === 'route own' && x.directive === 'location')!.note, /replaces the server's/);
  assert.deepEqual(route(r, 'hide').responseHeaders, { set: {}, add: { 'X-Server': '1' }, remove: ['X-Powered-By'], always: [] }, 'add list inherited, hide list own');
});
test('nginx import: unresolved upstream → error row + V_REQUIRED', () => {
  const r = imp(srv('location / { proxy_pass http://nope:9000; }'));
  const p = r.rows.find((x) => x.directive === 'proxy_pass')!;
  assert.equal(p.status, 'error');
  assert.match(p.note, /no container nope with port 9000/);
  assert.equal(proxy(r, 'root').containerSlug, '');
  assert.equal(proxy(r, 'root').setHeaders['Host'], 'nope:9000');
  const is = new Issues();
  validateEdge(r.edge, { agentPort: 7080, agentGuiAliasPort: 8080 }, is);
  assert.ok(is.list.some((x) => x.code === 'V_REQUIRED' && x.path.includes('containerSlug')));
  // port that is not an edge port
  const r2 = imp(srv('location / { proxy_pass http://db:5432; }'), [{ containerSlug: 'db', ports: [{ portName: 'pg', port: 5432, edge: false }] }]);
  assert.equal(r2.rows.find((x) => x.directive === 'proxy_pass')!.status, 'error');
  // https / variables → unsupported, route with empty target
  const r3 = imp(srv('location /a/ { proxy_pass https://northwind-web; } location /b/ { proxy_pass http://$up; }'));
  assert.deepEqual(r3.rows.filter((x) => x.directive === 'proxy_pass').map((x) => x.status), ['unsupported', 'unsupported']);
  assert.equal(proxy(r3, 'a').containerSlug, '');
});
test('nginx import: proxy_pass with a URI', () => {
  const r = imp(
    srv(`
      location /a/ { proxy_pass http://northwind-web:80/; }
      location /b/ { proxy_pass http://northwind-web:80/v1/; }
      location /c/ { proxy_pass http://northwind-web:80; }
    `),
  );
  assert.deepEqual([proxy(r, 'a').stripPrefix, proxy(r, 'a').addPrefix], [true, null]);
  assert.deepEqual([proxy(r, 'b').stripPrefix, proxy(r, 'b').addPrefix], [true, '/v1']);
  assert.deepEqual([proxy(r, 'c').stripPrefix, proxy(r, 'c').addPrefix], [false, null]);
  const e = applyNginxImport(defaultEdge(), r, 'replace');
  const up = (u: string) => edgeVectorView(resolveEdgeRequest(e, u, { ports: { 'northwind-web': { http: 80 } } })).upstreamUrl;
  assert.equal(up('http://a.example/a/x?q=1'), 'http://northwind-web:80/x?q=1');
  assert.equal(up('http://a.example/b/x'), 'http://northwind-web:80/v1/x');
  assert.equal(up('http://a.example/c/x'), 'http://northwind-web:80/c/x');
});
test('nginx import: return codes and rewrite flags', () => {
  const r = imp(
    srv(`
      location /r1 { return 308 https://new.example$request_uri; }
      location /r2 { return 302 /x; }
      location /r3 { return 200 "ok"; }
      location /r4 { return https://x.example/; }
      location /p1 { rewrite ^ https://new.example$uri permanent; }
      location /p2 { rewrite ^(.*)$ /new$1 redirect; }
      location /p3 { rewrite ^/p3/(.*)$ /x/$1 permanent; }
      location /l1 { rewrite ^/l1/(.*)$ /x/$1 last; rewrite ^ /y last; }
      location /b1/ { rewrite ^/b1/(.*)$ /api/$1?v=1 break; proxy_pass http://northwind-web; }
    `),
  );
  assert.deepEqual(route(r, 'r1').action, { type: 'redirect', to: 'https://new.example$request_uri', status: 308, keepPath: false });
  assert.deepEqual(route(r, 'r2').action, { type: 'redirect', to: '/x', status: 302, keepPath: false });
  assert.equal(r.rows.find((x) => x.text === 'return 200 ok')!.status, 'unsupported');
  assert.equal(route(r, 'r3').action.type, 'proxy', 'unsupported return → empty proxy route');
  assert.deepEqual(route(r, 'r4').action, { type: 'redirect', to: 'https://x.example/', status: 302, keepPath: false });
  assert.deepEqual(route(r, 'p1').action, { type: 'redirect', to: 'https://new.example$uri', status: 301, keepPath: false });
  assert.deepEqual(route(r, 'p2').action, { type: 'redirect', to: '/new$uri', status: 302, keepPath: false });
  assert.equal(r.rows.find((x) => x.text.startsWith('rewrite ^/p3/'))!.status, 'unsupported');
  assert.deepEqual(route(r, 'l1').action, { type: 'rewrite', regex: '^/l1/(.*)$', replacement: '/x/$1' });
  assert.equal(route(r, 'l1').responseHeaders, null);
  assert.equal(r.rows.find((x) => x.text === 'rewrite ^ /y last')!.status, 'unsupported');
  assert.deepEqual(proxy(r, 'b1').rewrite, { regex: '^/b1/(.*)$', replacement: '/api/$1?v=1' });
  assert.equal(proxy(r, 'b1').stripPrefix, false);
});
test('nginx import: hosts, listen, https redirect server, several servers', () => {
  const r = imp(`
    http {
      server { listen 80; server_name a.example b.example; return 301 https://$host$request_uri; }
      server {
        listen 8443 ssl; server_name a.example b.example;
        ssl_certificate /c.pem; ssl_certificate_key /k.pem;
        location / { proxy_pass http://northwind-web; }
      }
      server { listen 8080; server_name _ ~^www\\.; location / { proxy_pass http://northwind-web; } }
    }`);
  assert.equal(r.edge.httpPort, 80);
  assert.equal(r.edge.httpsPort, 8443);
  assert.deepEqual(r.edge.hosts.map((h) => [h.hostname, h.tls.mode, h.redirectHttpToHttps]), [['a.example', 'files', true], ['b.example', 'files', true], ['*', 'none', false]]);
  assert.deepEqual(route(r, 'root').hosts, ['a.example', 'b.example']);
  assert.deepEqual(route(r, 'root-2').hosts, []);
  assert.equal(r.certs.length, 2);
  assert.ok(r.rows.some((x) => x.directive === 'listen' && x.text === 'listen 8080' && x.status === 'note'));
  assert.ok(r.rows.some((x) => x.directive === 'server_name' && /regex server names/.test(x.note)));
  // no ssl anywhere → httpsPort null
  assert.equal(imp(srv('location / { proxy_pass http://northwind-web; }')).edge.httpsPort, null);
});
test('nginx import: append mode (ids, orders, hosts, explicit body / headers)', () => {
  const current: Edge = {
    ...defaultEdge(),
    maxBodyBytes: null,
    hosts: [{ ...newEdgeHost('env.client.example.gov.in'), responseHeaders: { set: {}, add: { 'X-Old': '1' }, remove: [], always: [] } }, newEdgeHost('other.example')],
    routes: [{ routeId: 'root', hosts: [], match: { kind: 'prefix', path: '/old/', stopRegex: false, methods: null }, action: { type: 'redirect', to: '/', status: 302, keepPath: false }, order: 7, responseHeaders: null }],
  };
  const r = imp(FIXTURE);
  const e = applyNginxImport(current, r, 'append', CERTS);
  assert.deepEqual(normalizeEdge(e as never), e);
  assert.equal(e.httpPort, 80);
  assert.equal(e.maxBodyBytes, null, 'current edge limit kept');
  assert.deepEqual(e.hosts.map((h) => h.hostname), ['env.client.example.gov.in', 'other.example']);
  assert.equal(e.hosts[0]!.tls.mode, 'none', 'existing host kept as is');
  assert.equal(e.routes.length, 21);
  const root2 = e.routes.find((x) => x.routeId === 'root-2')!;
  assert.equal(root2.match.path, '/');
  assert.equal(root2.order, 7 + 19);
  assert.deepEqual(root2.hosts, ['env.client.example.gov.in']);
  assert.equal((root2.action as EdgeProxyAction).maxBodyBytes, 104857600, 'inherited imported limit made explicit');
  assert.deepEqual(root2.responseHeaders, r.edge.hosts[0]!.responseHeaders, 'host existed with other headers → explicit');
  const rw = e.routes.find((x) => x.routeId === 'api-v0002-device-types')!;
  assert.equal(rw.responseHeaders, null, 'rewrite routes keep null');
  // new host added with the bound certificate
  const e2 = applyNginxImport({ ...defaultEdge(), hosts: [] }, r, 'append', CERTS);
  assert.deepEqual(e2.hosts[0]!.tls, { mode: 'files', certFileSlug: 'client-crt', keySecretSlug: 'client-key' });
  assert.equal(e2.routes.find((x) => x.routeId === 'root')!.responseHeaders, null, 'new host has the same headers → inherit');
  assert.equal((e2.routes.find((x) => x.routeId === 'root')!.action as EdgeProxyAction).maxBodyBytes, 104857600, 'defaultEdge limit null ≠ 100M');
});
test('nginx parser: tokens, quotes, comments, escapes', () => {
  const p = parseNginx(`a "x y" 'z\\'q' b\\"c; # comment ; {\n  blk ~ ^/a{1}$ {\n inner "${'$'}{v}";\n}\nv \${x}y;`);
  assert.deepEqual(p.errors.map((x) => x.line), [2], 'unquoted "{" in a regex opens a block (as in nginx)');
  assert.deepEqual(p.nodes[0], { name: 'a', args: ['x y', "z'q", 'b"c'], block: null, line: 1 });
  const p2 = parseNginx('http {\n  server {\n    listen 80;\n  }\n}\nx "a;b" "multi\nline";');
  assert.deepEqual(p2.errors, []);
  assert.equal(p2.nodes[0]!.block![0]!.block![0]!.line, 3);
  assert.deepEqual(p2.nodes[1]!.args, ['a;b', 'multi\nline']);
  assert.deepEqual(parseNginx('v ${x}y;').nodes[0]!.args, ['${x}y']);
});
test('nginx parser: errors (unbalanced braces, missing ;, unterminated quote)', () => {
  const a = parseNginx('http {\n server {\n  listen 80;\n');
  assert.equal(a.errors.length, 2);
  assert.ok(a.errors.every((x) => /missing "}"/.test(x.message)));
  assert.equal(a.nodes[0]!.block![0]!.block!.length, 1);
  const b = parseNginx('a;\n}\nb');
  assert.deepEqual(b.errors.map((x) => [x.line, /unexpected "}"/.test(x.message) || /missing ";"/.test(x.message)]), [[2, true], [3, true]]);
  assert.deepEqual(b.nodes.map((x) => x.name), ['a', 'b']);
  const c = parseNginx('a "open;\n');
  assert.match(c.errors[0]!.message, /unterminated/);
  const d = parseNginx('server { listen 80 }');
  assert.match(d.errors[0]!.message, /missing ";"/);
  assert.equal(d.nodes[0]!.block![0]!.name, 'listen');
});
test('nginx import: never throws on garbage', () => {
  const junk = ['', '}}}{{{', '"', ';;;', 'http { server { location { } } }', 'http { server { location ~ ( { proxy_pass; } } }', '\u0000￿', 'location / { proxy_pass http://x; }', 'http{server{listen;server_name;return;rewrite;add_header;proxy_set_header;client_max_body_size;proxy_read_timeout x;}}'];
  let seed = 7;
  const rnd = () => (seed = (seed * 1103515245 + 12345) % 2147483648) / 2147483648;
  const alphabet = ' \n;{}"\'#\\$~=^*/abcxyz_0123456789';
  for (let i = 0; i < 200; i++) junk.push(Array.from({ length: 1 + Math.floor(rnd() * 80) }, () => alphabet[Math.floor(rnd() * alphabet.length)]).join(''));
  for (let i = 0; i < 100; i++) {
    const cut = Math.floor(rnd() * FIXTURE.length);
    junk.push(FIXTURE.slice(0, cut) + FIXTURE.slice(cut + Math.floor(rnd() * 40)));
  }
  for (const t of junk) {
    const r = importNginx(t, { containers: C });
    assert.ok(Array.isArray(r.rows));
    assert.deepEqual(normalizeEdge(r.edge as never), r.edge, t);
    applyNginxImport(defaultEdge(), r, 'append');
  }
  assert.doesNotThrow(() => importNginx(undefined as unknown as string, undefined as unknown as { containers: [] }));
});

test('upstream mapping step: other container names (prod "Single": northwind-backend-*) map per upstream, with suggestions', () => {
  const text = readFileSync(fileURLToPath(new URL('../testdata/client-nginx.conf', import.meta.url)), 'utf8');
  const names = ['node-red', 'northwind-frontend', 'northwind-backend-terminal', 'northwind-backend-integration', 'northwind-backend-admin', 'northwind-admin'];
  const cs = names.map((c) => ({ containerSlug: c, ports: [{ portName: 'http', port: c === 'northwind-admin' ? 3000 : c === 'node-red' ? 1880 : c === 'northwind-frontend' ? 80 : 8080, edge: true }] }));
  const r = importNginx(text, { containers: cs });
  const by = Object.fromEntries(r.upstreams.map((u) => [u.key, u]));
  assert.equal(by['northwind-admin:3000']!.how, 'name');
  assert.deepEqual(by['ez-terminal:8080']!.suggestion, { containerSlug: 'northwind-backend-terminal', portName: 'http' });
  assert.deepEqual(by['ez-admin:8080']!.suggestion, { containerSlug: 'northwind-backend-admin', portName: 'http' }, 'ez-admin: two "-admin" names, the 8080 one wins');
  assert.deepEqual(by['nodered:1880']!.suggestion, { containerSlug: 'node-red', portName: 'http' });
  assert.equal(by['northwind-web:80']!.suggestion, null);
  assert.equal(suggestUpstream('northwind-admin', 3000, [{ containerSlug: 'northwind-backend-admin', ports: [{ portName: 'http', port: 8080, edge: true }] }]), null, 'fuzzy match with another port number: no guess');
  assert.deepEqual(suggestUpstream('pg-admin', 80, [{ containerSlug: 'pg-admin', ports: [{ portName: 'web', port: 5050, edge: true }] }]), { containerSlug: 'pg-admin', portName: 'web' }, 'same name: its only edge port');
  assert.deepEqual(by['ez-terminal:8080']!.routes, ['x-ez-terminal-real-time', 'x-ez-terminal-s3', 'x-ez-terminal']);
  // apply the mapping: the routes get the target, rows say "mapped"
  const r2 = importNginx(text, { containers: cs, upstreams: { 'ez-terminal:8080': { containerSlug: 'northwind-backend-terminal', portName: 'http' } } });
  const t = r2.edge.routes.find((x) => x.routeId === 'x-ez-terminal-s3')!.action;
  assert.equal(t.type === 'proxy' && t.containerSlug, 'northwind-backend-terminal');
  assert.equal(r2.upstreams.find((u) => u.key === 'ez-terminal:8080')!.how, 'mapped');
  assert.ok(r2.rows.some((x) => x.note.includes('upstream ez-terminal:8080 mapped to northwind-backend-terminal')));
  assert.equal(r2.summary.error, r.summary.error - 3);
});
