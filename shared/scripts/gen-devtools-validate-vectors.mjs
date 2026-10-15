// Generates testdata/devtools-validate-vectors.json (§21 #97 b): dev-tool preset validation vectors for the Go mirror
// (agent/internal/validate/devtools.go). Base = hello-config (site db: container redis (image redis:v1, port "tcp"),
// volume redis-data (dir); site app: hello-api + hello-web (port "http"), volume hello-api-data; secret api-token;
// variables theme, greeting, redis-host-port, app-host, db-host). Each case puts presets on one site.
//
// §21 #99: containers.<c>.env.<NAME> (field devtool): V_DEVTOOL_CONTAINER / V_DEVTOOL_ENV errors, V_DEVTOOL_PLAIN_ENV warning
// (password / token from a plain env). Site app: hello-api env REDIS_ADDR, API_TOKEN (secretRef api-token), GREETING
// (+ image env FAIL_HEALTH, DATA_DIR, PORT).
// Vector: { name, siteSlug, config (full Config), catalog: { images: { "<service>:<version>": { ports, env } } },
//           expectedCodes: sorted unique codes, expected: [{ path, code, severity }] in validator order }
// = the issues of validateSiteDevTools(site, config, catalog.images, issues) ONLY (not the rest of validateConfig).
// `want` below is hand-written; the script asserts the implementation agrees before writing the file.
// Run: npm run build && node scripts/gen-devtools-validate-vectors.mjs
import { readFileSync, writeFileSync } from 'node:fs';
import assert from 'node:assert/strict';
import { Issues, validateSiteDevTools } from '../dist/validate.js';
import { newDevToolPreset } from '../dist/devtools.js';

const load = (f) => JSON.parse(readFileSync(new URL(`../testdata/${f}`, import.meta.url), 'utf8'));
const HELLO = load('hello-config.json');
const CAT = load('hello-catalog.json');
const images = Object.fromEntries(Object.entries(CAT.images).map(([k, v]) => [k, { ports: v.ports, env: v.env ?? [] }]));

/** a preset of a kind with overrides (deep-merged one level for target / auth / options) */
function P(kind, slug, o = {}) {
  const p = newDevToolPreset(kind);
  p.presetSlug = slug;
  p.name = o.name ?? `${kind} ${slug}`;
  if (o.target) p.target = o.target;
  if (o.auth) p.auth = { ...p.auth, ...o.auth };
  if (o.options) p.options = { ...p.options, ...o.options };
  if (o.readOnly !== undefined) p.readOnly = o.readOnly;
  if (o.kind) p.kind = o.kind;
  if (o.description !== undefined) p.description = o.description;
  return p;
}
const redis = (slug = 'redis', o = {}) => P('redis', slug, { target: { kind: 'container', containerSlug: 'redis', portName: 'tcp' }, auth: { password: { kind: 'expr', expr: '{{ secret.api-token }}' } }, ...o });
const host = (h = '10.0.0.5', port = 5432) => ({ kind: 'host', host: h, port });
const expr = (e) => ({ kind: 'expr', expr: e });

const cases = [
  { name: 'valid redis preset on a container (password from a secret)', presets: [redis()], want: [] },
  {
    name: 'valid presets of every kind',
    presets: [
      redis(),
      P('postgres', 'pg', { target: host('pg.internal', 5432), auth: { username: '{{ var.greeting | lower }}', password: expr('{{ secret.api-token }}'), database: 'app' }, options: { sslMode: 'require', timeoutSec: 60, maxRows: 500 } }),
      P('sqlite', 'local-db', { target: { kind: 'volume-file', volumeSlug: 'redis-data', path: 'db/app.sqlite' }, readOnly: true }),
      P('sqlite', 'container-db', { target: { kind: 'container-file', containerSlug: 'redis', path: '/data/app.db' } }),
      P('http', 'api', { target: { kind: 'url', url: 'https://{{ var.app-host }}:8443/api' }, auth: { httpScheme: 'bearer', token: expr('{{ secret.api-token }}') }, options: { basePath: '/v1', defaultHeaders: [{ name: 'X-Site', value: '{{ self.slug }}' }] } }),
      P('http', 'web', { target: { kind: 'host', host: '127.0.0.1', port: 8080 }, auth: { httpScheme: 'api-key', token: expr('{{ secret.api-token }}'), apiKeyHeader: 'X-API-Key' } }),
      P('mqtt', 'broker', { target: host('broker.local', 1883), auth: { username: 'opm', password: expr('{{ secret.api-token }}'), clientId: 'opm-{{ self.slug }}' }, options: { keepAliveSec: 0, cleanSession: false } }),
      P('cassandra', 'cql', { target: host('10.0.0.9', 9042), options: { consistency: 'QUORUM' } }),
    ],
    want: [],
  },
  { name: 'bad preset slug', presets: [redis('Bad_Slug')], want: ['V_DEVTOOL_SLUG'] },
  { name: 'duplicate preset slug', presets: [redis('cache'), redis('cache')], want: ['V_DEVTOOL_DUP'] },
  { name: 'more than 50 presets', presets: Array.from({ length: 51 }, (_, i) => redis(`r-${i}`)), want: ['V_DEVTOOL_MAX'] },
  { name: 'empty name', presets: [redis('r', { name: ' ' })], want: ['V_REQUIRED'] },
  { name: 'name longer than 80', presets: [redis('r', { name: 'x'.repeat(81) })], want: ['V_LENGTH'] },
  { name: 'unknown kind', presets: [redis('r', { kind: 'mongodb' })], want: ['V_DEVTOOL_KIND'] },
  { name: 'target kind not allowed for sqlite', presets: [P('sqlite', 's', { target: { kind: 'container', containerSlug: 'redis', portName: 'tcp' } })], want: ['V_DEVTOOL_TARGET'] },
  { name: 'host target: bad host + port 0', presets: [P('postgres', 'pg', { target: host('bad host!', 0) })], want: ['V_DEVTOOL_TARGET'] },
  { name: 'host target with an expression', presets: [P('postgres', 'pg', { target: host('{{ var.db-host }}', 5432) })], want: ['V_DEVTOOL_TARGET'] },
  { name: 'url target must be http(s)', presets: [P('http', 'h', { target: { kind: 'url', url: 'ftp://x/y' } })], want: ['V_DEVTOOL_TARGET'] },
  { name: 'container target: empty container + port', presets: [redis('r', { target: { kind: 'container', containerSlug: '', portName: '' } })], want: ['V_DEVTOOL_TARGET'] },
  { name: 'container does not exist on the site', presets: [redis('r', { target: { kind: 'container', containerSlug: 'hello-api', portName: 'http' } })], want: ['V_DEVTOOL_CONTAINER'] },
  { name: 'disabled container → warning', presets: [redis()], patch: (c) => (site(c, 'db').containers[0].enabled = false), want: ['V_DEVTOOL_CONTAINER'] },
  { name: 'port name not on the image', presets: [redis('r', { target: { kind: 'container', containerSlug: 'redis', portName: 'http' } })], want: ['V_DEVTOOL_PORT'] },
  { name: 'sqlite: undeclared volume', presets: [P('sqlite', 's', { target: { kind: 'volume-file', volumeSlug: 'nope', path: 'a.db' } })], want: ['V_DEVTOOL_VOLUME'] },
  { name: 'sqlite: path with ..', presets: [P('sqlite', 's', { target: { kind: 'volume-file', volumeSlug: 'redis-data', path: 'x/../../etc/passwd' } })], want: ['V_DEVTOOL_VOLUME'] },
  { name: 'sqlite: absolute path in a volume', presets: [P('sqlite', 's', { target: { kind: 'volume-file', volumeSlug: 'redis-data', path: '/app.db' } })], want: ['V_DEVTOOL_VOLUME'] },
  {
    name: 'sqlite: single-file volume',
    presets: [P('sqlite', 's', { target: { kind: 'volume-file', volumeSlug: 'one-file', path: 'app.db' } })],
    patch: (c) => site(c, 'db').volumes.push({ volumeSlug: 'one-file', label: 'one-file', description: '', backup: true, location: { kind: 'auto' }, kind: 'file', seed: null }),
    want: ['V_DEVTOOL_VOLUME'],
  },
  { name: 'sqlite: container path not absolute', presets: [P('sqlite', 's', { target: { kind: 'container-file', containerSlug: 'redis', path: 'data/app.db' } })], want: ['V_DEVTOOL_VOLUME'] },
  // §21 #103 (supersedes #97 b): a static value is allowed — V_DEVTOOL_LITERAL is a WARNING ("stored in plain text in the config")
  { name: 'literal password = static value (warning)', presets: [redis('r', { auth: { password: { kind: 'literal', value: 'hunter2' } } })], want: ['V_DEVTOOL_LITERAL'] },
  { name: 'literal password on cassandra satisfies "both or neither" (warning only)', presets: [P('cassandra', 'c', { target: host('c', 9042), auth: { username: 'cassandra', password: { kind: 'literal', value: 'cassandra' } } })], want: ['V_DEVTOOL_LITERAL'] },
  { name: 'literal bearer token (warning)', presets: [P('http', 'h', { target: { kind: 'url', url: 'http://x' }, auth: { httpScheme: 'bearer', token: { kind: 'literal', value: 'tok-123' } } })], want: ['V_DEVTOOL_LITERAL'] },
  { name: 'literal password empty → error', presets: [redis('r', { auth: { password: { kind: 'literal', value: '' } } })], want: ['V_DEVTOOL_AUTH'] },
  { name: 'expression without {{ }} is a static value (warning)', presets: [redis('r', { auth: { password: expr('hunter2') } })], want: ['V_DEVTOOL_LITERAL'] },
  { name: 'expression without a reference is a static value (warning)', presets: [P('http', 'h', { target: { kind: 'url', url: 'http://x' }, auth: { httpScheme: 'bearer', token: expr('{{ "hunter2" }}') } })], want: ['V_DEVTOOL_LITERAL'] },
  { name: 'password expression with a hash filter of a secret', presets: [redis('r', { auth: { password: expr('{{ secret.api-token | sha256 }}') } })], want: [] },
  { name: 'unknown secret + unknown variable', presets: [P('postgres', 'pg', { target: host(), auth: { username: '{{ var.nope }}', password: expr('{{ secret.missing }}') } })], want: ['V_DEVTOOL_SECRET', 'V_DEVTOOL_VAR'] },
  { name: 'expression errors: syntax, root not allowed, unknown path', presets: [P('postgres', 'pg', { target: host(), auth: { username: '{{ var.greeting ', database: '{{ file.banner.path }}' }, options: {} }), P('mqtt', 'm', { target: host('b', 1883), auth: { clientId: '{{ self.nope }}' } })], want: ['E_NOT_ALLOWED', 'E_SYNTAX', 'E_UNKNOWN_PATH'] },
  { name: 'postgres needs a username', presets: [P('postgres', 'pg', { target: host(), auth: { username: null } })], want: ['V_DEVTOOL_AUTH'] },
  { name: 'cassandra username without password', presets: [P('cassandra', 'c', { target: host('c', 9042), auth: { username: 'cassandra' } })], want: ['V_DEVTOOL_AUTH'] },
  { name: 'redis database 16', presets: [redis('r', { auth: { database: '16' } })], want: ['V_DEVTOOL_AUTH'] },
  { name: 'http basic without username, bearer without token', presets: [P('http', 'a', { target: { kind: 'url', url: 'http://x' }, auth: { httpScheme: 'basic' } }), P('http', 'b', { target: { kind: 'url', url: 'http://x' }, auth: { httpScheme: 'bearer' } })], want: ['V_DEVTOOL_AUTH'] },
  { name: 'mqtt clientId longer than 65535 bytes', presets: [P('mqtt', 'm', { target: host('b', 1883), auth: { clientId: 'é'.repeat(32768) } })], want: ['V_DEVTOOL_AUTH'] },
  { name: 'unused auth fields → warnings', presets: [redis('r', { auth: { clientId: 'x', httpScheme: 'bearer' } }), P('sqlite', 's', { target: { kind: 'volume-file', volumeSlug: 'redis-data', path: 'a.db' }, auth: { username: 'u' } })], want: ['V_DEVTOOL_AUTH'] },
  {
    name: 'option ranges',
    presets: [P('http', 'h', { target: { kind: 'url', url: 'http://x' }, options: { timeoutSec: 0, maxRows: 10001, keepAliveSec: -1, basePath: 'api', defaultHeaders: [{ name: 'bad header', value: 'v' }], sslMode: 'always' } })],
    want: ['V_DEVTOOL_OPTION'],
  },
  // §21 #99 containers.<c>.env.<NAME>
  {
    name: 'container env refs: secret env as password, plain env as username, image env as database (valid)',
    siteSlug: 'app',
    presets: [P('http', 'api', { target: { kind: 'container', containerSlug: 'hello-api', portName: 'http' }, auth: { httpScheme: 'basic', username: '{{ containers.hello-api.env.GREETING }}', password: expr('{{ containers.hello-api.env.API_TOKEN }}') }, options: { defaultHeaders: [{ name: 'X-Data', value: '{{ containers.hello-api.env.DATA_DIR | default("/data") }}' }] } })],
    want: [],
  },
  {
    name: 'container env refs: unknown container, unknown env name',
    siteSlug: 'app',
    presets: [P('postgres', 'pg', { target: host(), auth: { username: '{{ containers.nope.env.POSTGRES_USER }}', password: expr('{{ containers.hello-api.env.NOPE }}') } })],
    want: ['V_DEVTOOL_CONTAINER', 'V_DEVTOOL_ENV'],
  },
  {
    name: 'container env refs: plain env as a password → warning; secret via container.env chain → none',
    siteSlug: 'app',
    presets: [
      P('postgres', 'pg', { target: host(), auth: { username: 'u', password: expr('{{ containers.hello-api.env.GREETING }}') } }),
      P('mqtt', 'm', { target: host('b', 1883), auth: { username: 'u', password: expr('{{ containers.hello-api.env.TOKEN_COPY }}') } }),
    ],
    patch: (c) => site(c, 'app').containers[0].env.push({ name: 'TOKEN_COPY', value: 'Bearer {{ container.env.API_TOKEN }}', secretRef: null }),
    want: ['V_DEVTOOL_PLAIN_ENV'],
  },
  {
    name: 'container env refs: not allowed shapes (container.env, file) stay E_NOT_ALLOWED',
    siteSlug: 'app',
    presets: [P('postgres', 'pg', { target: host(), auth: { username: '{{ container.env.GREETING }}' } })],
    want: ['E_NOT_ALLOWED'],
  },
  { name: 'devTools is not a list', presets: { not: 'a list' }, want: ['V_TYPE'] },
  { name: 'absent devTools → nothing', presets: undefined, want: [] },
];

function site(c, slug) {
  return c.sites.find((s) => s.siteSlug === slug).config;
}

const out = [];
for (const c of cases) {
  const config = structuredClone(HELLO.config);
  const siteSlug = c.siteSlug ?? 'db';
  if (c.presets !== undefined) site(config, siteSlug).devTools = structuredClone(c.presets);
  c.patch?.(config);
  const is = new Issues('publish');
  validateSiteDevTools(config.sites.find((s) => s.siteSlug === siteSlug), config, images, is);
  const expected = is.list.map(({ path, code, severity }) => ({ path, code, severity }));
  const expectedCodes = [...new Set(expected.map((x) => x.code))].sort();
  assert.deepEqual(expectedCodes, [...c.want].sort(), `${c.name}: ${JSON.stringify(expected)}`);
  out.push({ name: c.name, siteSlug, config, catalog: { images }, expectedCodes, expected });
}
const file = new URL('../testdata/devtools-validate-vectors.json', import.meta.url);
const description =
  'Dev-tool preset validation vectors (§21 #97 b; static values = V_DEVTOOL_LITERAL warning since §21 #103). For each case: validateSiteDevTools(config.sites[siteSlug], config, catalog.images, Issues("publish")) must produce exactly `expected` (path, code, severity; in this order) — expectedCodes = sorted unique codes. Generated by scripts/gen-devtools-validate-vectors.mjs; Go mirror: agent/internal/validate (devtools.go).';
// one case per line (the configs are full hello configs)
writeFileSync(file, `{"description":${JSON.stringify(description)},"cases":[\n${out.map((c) => JSON.stringify(c)).join(',\n')}\n]}\n`);
console.log(`wrote ${out.length} cases`);
