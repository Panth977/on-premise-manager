import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import {
  validateConfig,
  checkLocalEdit,
  siteOrder,
  containerOrder,
  resolveSiteHosts,
  containerExprContext,
  validateRegisterImage,
  validateVariable,
  Issues,
  normalizeSiteConfig,
  defaultContainer,
  normalizeImageEnv,
  parseImageReference,
  formatImageReference,
  matchRegistries,
  registryHost,
  nextVersion,
  normalizeContainer,
  normalizeConfig,
  duplicateContainer,
  suggestContainerSlug,
  checkFileContent,
  renderSiteFile,
  siteFileExprContext,
  locationPath,
  locationLabel,
  resolveContainerConfig,
  containerMounts,
  newSiteVolume,
  volumeUsers,
  undeclaredVolumes,
  suggestVolumeSlug,
  isLegacySiteConfig,
  seedPathError,
  seedLabel,
  type ValidationContext,
} from './validate.js';
import { autoHostPlaceholder } from './expr.js';
import { resolve } from './expr.js';
import { diffConfigs } from './diff.js';
import { ACTIVITY_TYPES } from './types.js';
import { RE, isResponseOnlyHeader, moveToResponseHeaders } from './validate.js';
import type { Config, Container, Variable } from './types.js';

const load = (f: string) => JSON.parse(readFileSync(fileURLToPath(new URL(`../testdata/${f}`, import.meta.url)), 'utf8'));
const HELLO = load('hello-config.json') as { deploymentId: string; config: Config };
const CAT = load('hello-catalog.json') as Omit<ValidationContext, 'mode' | 'deploymentId'>;
const ctx = (mode: ValidationContext['mode'] = 'publish'): ValidationContext => ({ mode, deploymentId: HELLO.deploymentId, services: CAT.services, images: CAT.images, notFoundPageIds: CAT.notFoundPageIds });
const clone = (): Config => structuredClone(HELLO.config);
const codes = (c: Config, mode: ValidationContext['mode'] = 'publish') => validateConfig(c, ctx(mode)).list.filter((x) => x.severity === 'error').map((x) => x.code);
const app = (c: Config) => c.sites.find((s) => s.siteSlug === 'app')!.config;
const db = (c: Config) => c.sites.find((s) => s.siteSlug === 'db')!.config;
const W = 'hello-web';
const A = 'hello-api';
const R = 'redis';

test('hello-world config is valid for publish', () => {
  const is = validateConfig(HELLO.config, ctx());
  assert.deepEqual(is.errors, []);
});

test('apply orders: db before app, hello-api before hello-web', () => {
  assert.deepEqual(siteOrder(HELLO.config), { order: ['db', 'app'] });
  assert.deepEqual(containerOrder(app(HELLO.config), CAT.services), { order: [A, W] });
});

test('expressions across sites resolve', () => {
  const hosts = resolveSiteHosts(HELLO.config, HELLO.deploymentId);
  assert.equal(hosts.db!.host, 'host.docker.internal');
  const c = app(HELLO.config).containers.find((x) => x.serviceSlug === A)!;
  const ec = containerExprContext(HELLO.config, ctx(), 'app', c);
  assert.equal(resolve(c.env[0]!.value!, ec, 'env').value, 'host.docker.internal:16379');
  const w = app(HELLO.config).containers.find((x) => x.serviceSlug === W)!;
  assert.equal(resolve(w.env[1]!.value!, containerExprContext(HELLO.config, ctx(), 'app', w), 'env').value, '/etc/hello/banner.txt');
});

test('site depends cycle', () => {
  const c = clone();
  db(c).depends = [{ siteSlug: 'app' }];
  assert.ok(codes(c).includes('V_CYCLE'));
});

test('container depends cycle + self', () => {
  const c = clone();
  app(c).containers.find((x) => x.serviceSlug === A)!.depends = [{ containerSlug: W }];
  assert.ok(codes(c).includes('V_CYCLE'));
});

test('edge ports, agent ports and host ports must not collide', () => {
  const c = clone();
  app(c).agentGuiAliasPort = 7080;
  db(c).containers[0]!.ports[0]!.hostPort = 8082;
  const cs = codes(c);
  assert.ok(cs.includes('V_PORT_CONFLICT'));
  assert.ok(cs.includes('V_PORT_EDGE'));
  const d = clone();
  db(d).containers[0]!.ports[0]!.hostPort = 80;
  assert.ok(codes(d).includes('V_PORT_EDGE'));
});

test('reserved site slug and duplicate site', () => {
  const c = clone();
  c.sites[0]!.siteSlug = 'updates';
  assert.ok(codes(c).includes('V_RESERVED'));
  const d = clone();
  d.sites[1]!.siteSlug = 'db';
  assert.ok(codes(d).includes('V_DUPLICATE'));
});

test('env needs exactly one of value / secretRef; unknown secret; secret env needs a secret', () => {
  const c = clone();
  const api = app(c).containers.find((x) => x.serviceSlug === A)!;
  api.env[1] = { name: 'API_TOKEN', value: 'plain', secretRef: null };
  api.env.push({ name: 'X', value: 'a', secretRef: 'api-token' });
  api.env.push({ name: 'Y', value: null, secretRef: 'nope' });
  const cs = codes(c);
  assert.ok(cs.includes('V_ENV_SECRET'));
  assert.ok(cs.includes('V_ENV_ONE_OF'));
  assert.ok(cs.includes('V_REF'));
});

test('publish-level issues are warnings in draft mode', () => {
  const c = clone();
  c.variables.find((v) => v.variableSlug === 'theme')!.value = null;
  c.secrets[0]!.value = null;
  assert.deepEqual(codes(c, 'draft'), []);
  const w = validateConfig(c, ctx('draft')).list.map((x) => x.code);
  assert.ok(w.includes('V_VAR_REQUIRED'));
  assert.ok(w.includes('V_SECRET_REQUIRED'));
  assert.ok(!w.includes('E_NULL'), 'round E: usages of a required variable without value are not repeated (set at deployment)');
  assert.ok(codes(c, 'publish').includes('V_VAR_REQUIRED'));
  assert.ok(!codes(c, 'publish').includes('E_NULL'));
});

test('expression errors: not allowed root in host, unknown path, no hostport', () => {
  const c = clone();
  app(c).host = '{{ secret.api-token }}';
  db(c).containers[0]!.ports[0]!.hostPort = null;
  const cs = codes(c);
  assert.ok(cs.includes('E_NOT_ALLOWED'));
  assert.ok(cs.includes('E_NO_HOSTPORT'), 'REDIS_ADDR uses site.db.port.redis.tcp, which now has no host port');
  const d = clone();
  app(d).containers[0]!.env[2]!.value = '{{ var.nope }}';
  assert.ok(codes(d).includes('E_UNKNOWN_PATH'));
});

test('edge rules', () => {
  const c = clone();
  const e = app(c).edge;
  e.routes.push({ routeId: 'api', hosts: ['nohost'], match: { kind: 'prefix', path: '/_opm/x', stopRegex: false, methods: ['FETCH'] }, action: { type: 'redirect', to: '', status: 304 as 308, keepPath: false }, order: 0, responseHeaders: null });
  e.hosts.push({ hostname: '10.0.1.10', tls: { mode: 'acme', email: 'x', challenge: 'http-01', dnsProvider: null, dnsTokenSecretSlug: null }, redirectHttpToHttps: true, responseHeaders: { set: {}, add: {}, remove: [], always: [] } });
  const cs = codes(c);
  for (const k of ['V_DUPLICATE', 'V_REF', 'V_RESERVED', 'V_ENUM', 'V_REQUIRED', 'V_RANGE', 'V_EDGE_TLS', 'V_FORMAT']) assert.ok(cs.includes(k), `expected ${k} in ${cs.join(',')}`);
  const d = clone();
  (app(d).edge.routes[0]!.action as { portName: string }).portName = "nope";
  assert.ok(codes(d).includes('V_REF'));
  const w = validateConfig(clone(), ctx()).list;
  assert.equal(w.filter((x) => x.severity === 'error').length, 0, JSON.stringify(w.filter((x) => x.severity === 'error')));
  // round E rules
  const f = clone();
  const fr = app(f).edge.routes;
  fr.push({ ...structuredClone(fr[0]!), routeId: 'dup' });
  fr.push({ routeId: 're', hosts: [], match: { kind: 'regex', path: '(?=x)', stopRegex: true, methods: null }, action: { type: 'rewrite', regex: '^/(', replacement: '' }, order: 9, responseHeaders: { set: {}, add: {}, remove: [], always: [] } });
  const pa = fr[0]!.action as import('./types.js').EdgeProxyAction;
  pa.readTimeoutSec = 0;
  pa.maxBodyBytes = -1;
  pa.rewrite = { regex: 'a', replacement: '/b' };
  pa.setHeaders = { Connection: 'Upgrade' };
  app(f).edge.hosts[0]!.responseHeaders = { set: { 'X-A': '1' }, add: { 'x-a': '2' }, remove: ['bad name'], always: ['X-B'] };
  const fl = validateConfig(f, ctx()).list;
  const fc = fl.map((x) => x.code);
  for (const k of ['V_DUPLICATE', 'V_EDGE_REGEX', 'V_EDGE_ROUTE', 'V_RANGE', 'V_EDGE_BODY', 'V_EDGE_HEADER', 'V_EDGE_HOP', 'V_REQUIRED', 'V_FORMAT']) assert.ok(fc.includes(k), `expected ${k} in ${fc.join(',')}`);
  assert.ok(fl.some((x) => x.code === 'V_EDGE_HEADER' && x.path.endsWith('routes[re].responseHeaders')), 'rewrite routes have no response headers');
});

test('W_RESPONSE_HEADER_AS_REQUEST + moveToResponseHeaders (user evidence: s3 Cache-Control set as a request header)', () => {
  const c = clone();
  const r = app(c).edge.routes[0]!;
  const pa = r.action as import('./types.js').EdgeProxyAction;
  pa.setHeaders = { 'Cache-Control': 'public, max-age=3600, must-revalidate', 'X-Script-Name': '/x' };
  const host = app(c).edge.hosts[0]!;
  host.responseHeaders = { set: {}, add: { Pragma: 'no-cache', 'Cache-Control': 'no-store' }, remove: [], always: [] };
  const l = validateConfig(c, ctx()).list.filter((x) => x.code === 'W_RESPONSE_HEADER_AS_REQUEST');
  assert.equal(l.length, 1);
  assert.equal(l[0]!.severity, 'warning');
  assert.ok(isResponseOnlyHeader('Access-Control-Allow-Origin'));
  assert.ok(!isResponseOnlyHeader('Cache-Control', 'no-transform'));
  assert.ok(!isResponseOnlyHeader('X-Script-Name', '/x'));
  const m = moveToResponseHeaders(r, 'cache-control', host);
  assert.deepEqual((m.action as import('./types.js').EdgeProxyAction).setHeaders, { 'X-Script-Name': '/x' });
  assert.deepEqual(m.responseHeaders, { set: { 'Cache-Control': 'public, max-age=3600, must-revalidate' }, add: { Pragma: 'no-cache' }, remove: [], always: [] });
  app(c).edge.routes[0] = m;
  assert.equal(validateConfig(c, ctx()).list.filter((x) => x.code === 'W_RESPONSE_HEADER_AS_REQUEST').length, 0);
});

test('image not ready + image missing + job restart', () => {
  const c = clone();
  const images = { ...CAT.images, [`${A}:v1`]: { ...CAT.images[`${A}:v1`]!, importStatus: 'copying' as const } };
  const is = validateConfig(c, { ...ctx(), images });
  assert.ok(is.errors.some((x) => x.code === 'V_IMAGE_NOT_READY'));
  app(c).containers[0]!.version = 'v9';
  assert.ok(codes(c).includes('V_REF'));
  const j = clone();
  const jobs = { ...CAT.images, [`${R}:v1`]: { ...CAT.images[`${R}:v1`]!, kind: 'job' as const } };
  assert.ok(validateConfig(j, { ...ctx(), images: jobs }).errors.some((x) => x.code === 'V_JOB_RESTART'));
});

test('variables', () => {
  const is = new Issues('publish');
  const v: Variable = { variableSlug: 'nn', label: 'N', description: '', type: 'number', value: 70000, required: true, options: null, validation: { min: 1, max: 65535, pattern: null } };
  validateVariable(v, 'v', is);
  validateVariable({ ...v, type: 'string', value: 'ABC', validation: { min: null, max: null, pattern: '[a-z]+' } }, 'v2', is);
  validateVariable({ ...v, type: 'enum', value: 'x', options: ['a'], validation: null }, 'v3', is);
  validateVariable({ ...v, type: 'string', value: 'x', validation: { min: null, max: null, pattern: '(?=a)' } }, 'v4', is);
  assert.deepEqual(is.list.map((x) => x.code), ['V_VALUE_RULE', 'V_VALUE_RULE', 'V_VALUE_TYPE', 'V_PATTERN', 'V_VALUE_RULE']);
});

test('template mode refuses secret values', () => {
  const c = clone();
  assert.ok(codes(c, 'template').includes('V_TEMPLATE_SECRET_VALUE'));
});

test('local edit rules', () => {
  const after = clone();
  after.variables[0]!.value = 'yellow';
  app(after).containers[0]!.env[2]!.value = 'changed';
  app(after).edge.routes.pop();
  assert.deepEqual(checkLocalEdit(HELLO.config, after), []);
  after.variables[0]!.options = ['x'];
  app(after).containers[0]!.version = 'v2';
  db(after).containers.pop();
  after.sites[0]!.label = 'renamed';
  after.files = [];
  // §21 #113: a site is standalone — definitions, the image, labels, adds and removes are all local edits
  assert.deepEqual(checkLocalEdit(HELLO.config, after), []);
  after.secrets = [];
  after.variables.push({ ...after.variables[0]!, variableSlug: 'extra' });
  assert.deepEqual(checkLocalEdit(HELLO.config, after), []);
  // the list of sites stays the deployment's
  after.sites.pop();
  assert.deepEqual(checkLocalEdit(HELLO.config, after).map((x) => x.code), ['V_LOCAL_SITES']);
});

test('register image body', () => {
  const ok = validateRegisterImage({ version: 'v1', reference: 'redis:7.4', mode: 'copy', kind: 'service', ports: [{ name: 'http', port: 8080, protocol: 'tcp', description: '' }], healthcheck: { type: 'http', portName: 'http', path: '/healthz', expectStatus: 200, intervalSec: 10, timeoutSec: 5, retries: 3, startPeriodSec: 30 } });
  assert.deepEqual(ok.list, []);
  const bad = validateRegisterImage({ version: '-latest', reference: 'Bad Repo', mode: 'copy', kind: 'daemon', ports: [{ name: 'http', port: 70000, protocol: 'tcp', description: '' }, { name: 'http', port: 8080, protocol: 'tcp', description: '' }], env: [{ name: 'Api-Key', required: true, secret: true, value: 'abc', description: '' }], healthcheck: { type: 'tcp', portName: 'admin', path: '/x', intervalSec: 10, timeoutSec: 5, retries: 3, startPeriodSec: 30 } });
  assert.deepEqual(bad.list.map((x) => x.code), ['V_FORMAT', 'V_REFERENCE', 'V_ENUM', 'V_DUPLICATE', 'V_RANGE', 'V_DUPLICATE', 'V_FORMAT', 'V_IMAGE_ENV', 'V_HEALTHCHECK', 'V_REF']);
});

test('defaults + diff', () => {
  const sc = normalizeSiteConfig({ host: 'x' });
  assert.equal(sc.agentPort, 7080);
  assert.equal(sc.agentGuiAliasPort, 8080);
  assert.equal(sc.edge.hosts[0]!.hostname, '*');
  const dc = defaultContainer(A, 'v1', CAT.images[`${A}:v1`]!);
  assert.deepEqual(dc.env, [{ name: 'API_TOKEN', value: null, secretRef: '' }]);
  assert.deepEqual(dc.volumes, [], 'new containers start with no volume rows (not mounted until picked)');
  assert.deepEqual(sc.volumes, []);
  assert.deepEqual(dc.ports, [{ portName: 'http', hostPort: null, hostIp: '0.0.0.0', edge: true }]);
  assert.equal(normalizeSiteConfig({ host: '' }).host, null, 'empty host = auto');
  assert.equal(normalizeSiteConfig({}).host, null);
  const after = clone();
  after.variables[0]!.value = 'yellow';
  after.secrets[0]!.value = 'new';
  const ch = diffConfigs(HELLO.config, after, (id) => id);
  assert.deepEqual(ch, [
    { section: 'variables', siteSlug: null, path: 'theme', kind: 'changed', before: 'green', after: 'yellow' },
    { section: 'secrets', siteSlug: null, path: 'api-token', kind: 'changed', before: 'set', after: 'changed' },
  ]);
  assert.equal(diffConfigs(null, HELLO.config).filter((x) => x.kind === 'added').length, 5 + 1 + 1 + 2 + 2 + 3); // vars, secrets, files, sites, declared volumes, containers
});

test('every catalog activity type matches the activityType pattern', () => {
  for (const t of ACTIVITY_TYPES) assert.match(t, RE.activityType, t);
  assert.match('access-key.received', RE.activityType);
  assert.doesNotMatch('Access.key', RE.activityType);
  assert.doesNotMatch('-a.b', RE.activityType);
});

test('image env options (choice) + legacy list normalisation', () => {
  const base = { version: 'v1', reference: 'docker.io/acme/api:1', mode: 'copy', kind: 'service' };
  const env = (e: Record<string, unknown>) => validateRegisterImage({ ...base, env: [{ name: 'NODE_ENV', required: false, secret: false, description: '', ...e }] }).list.map((x) => x.code);
  assert.deepEqual(env({ value: 'worker', options: ['integration', 'worker'] }), []);
  assert.deepEqual(env({ value: 'prod', options: ['integration', 'worker'] }), ['V_OPTION']);
  assert.deepEqual(env({ value: '{{ var.x }}', options: ['a'] }), ['V_OPTION']);
  assert.deepEqual(env({ options: ['a', 'a'] }), ['V_DUPLICATE']);
  assert.deepEqual(env({ options: [] }), ['V_LENGTH']);
  assert.deepEqual(env({ value: ['dc', 'worker'] }), []); // legacy list = options, first = default
  assert.deepEqual(normalizeImageEnv({ name: 'X', value: ['dc', 'worker'] as unknown as string, required: false, secret: false, description: '' }), { name: 'X', value: 'dc', options: ['dc', 'worker'], required: false, secret: false, description: '' });
  assert.deepEqual(env({ secret: true, options: ['a'] }), ['V_IMAGE_ENV']);
});

test('image volume mountPath expressions', () => {
  const base = { version: 'v1', reference: 'docker.io/acme/api:1', mode: 'copy', kind: 'service' };
  const vol = (mountPath: string) => validateRegisterImage({ ...base, volumes: [{ name: 'config', mountPath, description: '' }] }).list.map((x) => x.code);
  assert.deepEqual(vol('/app/helper/config/env/{{ container.env.NODE_ENV }}.ts'), []);
  assert.deepEqual(vol('helper/config/env/{{ container.env.NODE_ENV }}.ts'), ['V_PATH']);
  assert.deepEqual(vol('/x/{{ secret.a }}'), ['E_NOT_ALLOWED']);
  assert.deepEqual(vol('helper/config'), ['V_MOUNT']);
});

test('container: choice env (V_OPTION), container.env in a mount path, E_CYCLE, V_PATH', () => {
  const images = structuredClone(CAT.images);
  const key = `${A}:v1`;
  images[key]!.env.push({ name: 'NODE_ENV', value: 'worker', options: ['integration', 'dc', 'worker'], required: false, secret: false, description: '' });
  images[key]!.volumes.push({ name: 'config', mountPath: '/app/helper/config/env/{{ container.env.NODE_ENV }}.ts', kind: 'dir', description: '' });
  const cx = (): ValidationContext => ({ ...ctx(), images });
  const errs = (c: Config) => validateConfig(c, cx()).errors.map((x) => `${x.code}@${x.path}`);
  assert.deepEqual(errs(clone()), []);
  const c1 = clone();
  const api = app(c1).containers.find((x) => x.serviceSlug === A)!;
  api.env.push({ name: 'NODE_ENV', value: 'prod', secretRef: null });
  assert.ok(errs(c1).some((e) => e.startsWith('V_OPTION@') && e.includes('env[NODE_ENV]')));
  api.env[api.env.length - 1]!.value = '{{ var.nope | default("dc") }}';
  assert.ok(errs(c1).some((e) => e.startsWith('E_UNKNOWN_PATH@')));
  api.env[api.env.length - 1]!.value = 'dc';
  assert.deepEqual(errs(c1), []);
  // cycle: NODE_ENV → A → NODE_ENV
  api.env[api.env.length - 1]!.value = '{{ container.env.CYC }}';
  api.env.push({ name: 'CYC', value: 'x{{ container.env.NODE_ENV }}', secretRef: null });
  const cyc = errs(c1).filter((e) => e.startsWith('E_CYCLE@'));
  assert.ok(cyc.length >= 2, cyc.join(' '));
  // a mount path resolving to a relative / root path
  const c2 = clone();
  const api2 = app(c2).containers.find((x) => x.serviceSlug === A)!;
  api2.env.push({ name: 'EMPTY_DIR', value: '', secretRef: null });
  app(c2).volumes.push(newSiteVolume('cfg'));
  api2.volumes.push({ imageVolume: 'config', mountPath: '/{{ container.env.EMPTY_DIR }}', source: { kind: 'volume', volumeSlug: 'cfg' }, readOnly: false });
  assert.ok(errs(c2).some((e) => e.startsWith('V_PATH@')), errs(c2).join(' '));
  // the draft keeps resolution problems as warnings
  assert.ok(validateConfig(c2, { ...cx(), mode: 'draft' }).list.some((x) => x.code === 'V_PATH' && x.severity === 'warning'));
});

test('docker references, registry matching, next version', () => {
  const p = (r: string) => {
    const x = parseImageReference(r);
    return x.ok ? formatImageReference(x.parts) : `ERR ${x.error}`;
  };
  assert.equal(p('docker.io/northwind/backend:3.0.9'), 'docker.io/northwind/backend:3.0.9');
  assert.equal(p('redis'), 'docker.io/library/redis:latest');
  assert.equal(p('registry-1.docker.io/redis:7.4'), 'docker.io/library/redis:7.4');
  assert.equal(p('index.docker.io/acme/api'), 'docker.io/acme/api:latest');
  assert.equal(p('ghcr.io/acme/api:1.2.0'), 'ghcr.io/acme/api:1.2.0');
  assert.equal(p('localhost:5000/x/y:t'), 'localhost:5000/x/y:t');
  assert.equal(p('asia-south1-docker.pkg.dev/p/r/hello-api:1.0.0'), 'asia-south1-docker.pkg.dev/p/r/hello-api:1.0.0');
  const dg = 'sha256:' + 'a'.repeat(64);
  assert.equal(p(`acme/api@${dg}`), `docker.io/acme/api@${dg}`);
  assert.equal(p(`acme/api:1@${dg}`), `docker.io/acme/api:1@${dg}`);
  assert.ok(p('Acme/API:1').startsWith('ERR'));
  assert.ok(p('acme/api:-bad').startsWith('ERR'));
  assert.ok(p('acme/api@sha256:xyz').startsWith('ERR'));
  assert.ok(p('').startsWith('ERR'));
  const regs = [
    { registryId: 'a', url: 'https://registry-1.docker.io', label: 'Northwind' },
    { registryId: 'b', url: 'https://asia-south1-docker.pkg.dev', label: 'AR' },
    { registryId: 'c', url: 'https://index.docker.io', label: 'Another hub' },
  ];
  assert.deepEqual(matchRegistries('docker.io', regs).map((r) => r.registryId), ['c', 'a']);
  assert.deepEqual(matchRegistries('ghcr.io', regs), []);
  assert.equal(registryHost('https://asia-south1-docker.pkg.dev'), 'asia-south1-docker.pkg.dev');
  assert.equal(nextVersion(null), 'v1');
  assert.equal(nextVersion('v3'), 'v4');
  assert.equal(nextVersion('3.1.0'), '3.1.1');
  assert.equal(nextVersion('v9', ['v10']), 'v11');
  assert.ok(RE.version.test('3.1.0') && RE.version.test('v1') && !RE.version.test('V1') && !RE.version.test('-x'));
});

// ───────────────────────────────────────────────────────────── round C: auto host, container instances, rendered files
const all = (c: Config, cx: ValidationContext = ctx()) => validateConfig(c, cx).list;

test('site host null = auto: no issue in any cloud mode; placeholder or detected address', () => {
  const c = clone();
  app(c).host = null;
  for (const m of ['template', 'draft', 'publish'] as const) assert.equal(all(c, ctx(m)).filter((x) => x.path.endsWith('.host')).length, 0, m);
  assert.equal(resolveSiteHosts(c, 'd').app!.host, autoHostPlaceholder('app'));
  assert.equal(resolveSiteHosts(c, 'd', { detected: { app: '10.0.0.7' } }).app!.host, '10.0.0.7');
  assert.equal(resolveSiteHosts(c, 'd', { placeholder: false }).app!.host, null);
  // an env referencing an auto host resolves to the placeholder in the cloud …
  db(c).containers[0]!.env.push({ name: 'PEER', value: '{{ site.app.host }}', secretRef: null });
  assert.deepEqual(codes(c), []);
  // … and fails with E_NULL on a site that does not know the address yet
  assert.ok(validateConfig(c, { ...ctx('agent') }).errors.some((x) => x.code === 'E_NULL'));
  assert.deepEqual(validateConfig(c, { ...ctx('agent'), detectedHosts: { app: '10.0.0.7' } }).errors, []);
});

test('site host expression without a value falls back to auto with a warning (never an error)', () => {
  const c = clone();
  c.variables.push({ variableSlug: 'server-ip', label: 'Server IP', description: '', type: 'string', value: null, required: false, options: null, validation: null });
  app(c).host = '{{ var.server-ip }}';
  for (const m of ['draft', 'publish'] as const) {
    const l = all(c, ctx(m)).filter((x) => x.path === 'sites[app].config.host');
    assert.equal(l.length, 1, m);
    assert.equal(l[0]!.code, 'V_HOST_AUTO');
    assert.equal(l[0]!.severity, 'warning');
    assert.match(l[0]!.message, /var server-ip has no value — leave host on auto or set the variable/);
  }
  assert.equal(resolveSiteHosts(c, 'd', { detected: { app: '192.168.1.5' } }).app!.host, '192.168.1.5');
  c.variables.at(-1)!.value = '10.1.1.1';
  assert.equal(resolveSiteHosts(c, 'd').app!.host, '10.1.1.1');
});

test('the same image twice on one site with different names, env and ports', () => {
  const c = clone();
  const api = app(c).containers.find((x) => x.containerSlug === A)!;
  const two = duplicateContainer(api, suggestContainerSlug(A, app(c).containers.map((x) => x.containerSlug)));
  assert.equal(two.containerSlug, 'hello-api-2');
  assert.deepEqual(two.volumes.find((v) => v.imageVolume === 'data')!.source, { kind: 'volume', volumeSlug: 'hello-api-2-data' });
  assert.deepEqual(undeclaredVolumes({ volumes: app(c).volumes, containers: [...app(c).containers, two] }), ['hello-api-2-data']);
  app(c).volumes.push(newSiteVolume('hello-api-2-data'));
  two.env.find((e) => e.name === 'GREETING')!.value = 'second';
  two.ports[0]!.hostPort = 18080;
  app(c).containers.push(two);
  assert.deepEqual(codes(c), []);
  // both are on the site map under their own name
  const ec = containerExprContext(c, ctx(), 'app', two);
  assert.deepEqual(Object.keys(ec.site!.app!.port).sort(), [A, 'hello-api-2', W].sort());
  assert.equal(ec.site!.app!.port['hello-api-2']!.http, 18080);
  assert.equal(ec.container!.slug, 'hello-api-2');
  // duplicate name
  app(c).containers.push({ ...two });
  assert.ok(all(c).some((x) => x.code === 'V_DUPLICATE' && x.path.endsWith('.containerSlug')));
  // a host port used twice
  const d = clone();
  const t2 = duplicateContainer(app(d).containers[0]!, 'api-b');
  app(d).containers[0]!.ports[0]!.hostPort = 18080;
  t2.ports[0]!.hostPort = 18080;
  app(d).containers.push(t2);
  assert.ok(codes(d).includes('V_PORT_CONFLICT'));
  // depends by containerSlug
  t2.ports[0]!.hostPort = null;
  t2.depends = [{ containerSlug: 'nope' }];
  assert.ok(codes(d).includes('V_REF'));
});

test('edge targets {containerSlug, portName}; a port closed to the edge is refused', () => {
  const c = clone();
  app(c).containers.find((x) => x.containerSlug === W)!.ports[0]!.edge = false;
  assert.ok(all(c).some((x) => x.code === 'V_EDGE_ROUTE' && x.path.includes('routes[web]')));
  const d = clone();
  (app(d).edge.routes[0]!.action as { containerSlug: string }).containerSlug = 'ghost';
  assert.ok(all(d).some((x) => x.code === 'V_REF' && x.path.includes('action.containerSlug')));
});

test('volume rows: sources, unknown image volume, extra mounts, host path, none', () => {
  const c = clone();
  const api = app(c).containers.find((x) => x.containerSlug === A)!;
  api.volumes.push({ imageVolume: 'nope', mountPath: null, source: { kind: 'volume', volumeSlug: 'hello-api-data' }, readOnly: false });
  api.volumes.push({ imageVolume: null, mountPath: '/host', source: { kind: 'host', path: 'relative' }, readOnly: false });
  api.volumes.push({ imageVolume: null, mountPath: null, source: { kind: 'none' }, readOnly: false });
  api.volumes.push({ imageVolume: null, mountPath: '/s', source: { kind: 'volume', volumeSlug: 'Bad Name' }, readOnly: false });
  api.volumes.push({ imageVolume: null, mountPath: '/u', source: { kind: 'volume', volumeSlug: 'undeclared' }, readOnly: false });
  api.volumes.push({ imageVolume: null, mountPath: '/f', source: { kind: 'file', fileSlug: 'nope' }, readOnly: false });
  api.volumes.push({ imageVolume: null, mountPath: '/old', source: { kind: 'data', name: 'x' } as never, readOnly: false });
  const cs = codes(c);
  for (const k of ['V_REF', 'V_VOLUME', 'V_REQUIRED', 'V_FORMAT', 'V_VOLUME_UNDECLARED', 'V_READONLY', 'V_ENUM']) assert.ok(cs.includes(k), `${k} in ${cs.join(',')}`);
  // none: the image volume is not mounted; host: bind; shared data volume between two containers is fine
  const d = clone();
  const a = app(d).containers.find((x) => x.containerSlug === A)!;
  a.volumes[0]!.source = { kind: 'none' };
  assert.equal(containerMounts(a, CAT.images[`${A}:v1`]!).length, 0);
  a.volumes[0]!.source = { kind: 'host', path: '/srv/hello' };
  const b = duplicateContainer(a, 'api-b');
  b.volumes[0]!.source = { kind: 'volume', volumeSlug: 'shared' };
  a.volumes[0]!.source = { kind: 'volume', volumeSlug: 'shared' };
  app(d).containers.push(b);
  const unused = all(d).filter((x) => x.severity === 'warning' && x.code === 'V_VOLUME_UNUSED').map((x) => x.path);
  assert.ok(unused.some((p) => p.endsWith('volumes[hello-api-data]')), unused.join(','));
  assert.ok(codes(d).includes('V_VOLUME_UNDECLARED'), 'shared is not declared yet');
  app(d).volumes.push(newSiteVolume('shared', 'Shared data'));
  assert.deepEqual(codes(d), []);
  assert.deepEqual(
    volumeUsers(app(d)).shared!.map((u) => u.containerSlug),
    [A, 'api-b'],
  );
  assert.deepEqual(undeclaredVolumes({ volumes: [], containers: app(d).containers }), ['shared']);
  // missing rows = not mounted (warning)
  a.volumes = [];
  assert.deepEqual(containerMounts(a, CAT.images[`${A}:v1`]!), []);
  assert.ok(all(d).some((x) => x.severity === 'warning' && x.code === 'V_VOLUME_NOT_MOUNTED' && x.path.endsWith(`containers[${A}].volumes[data]`)));
  // site volume fields
  app(d).volumes.push({ volumeSlug: 'shared', label: 'x'.repeat(81), description: '', backup: 'yes' as never, location: { kind: 'auto' }, kind: 'dir', seed: null });
  const vc = codes(d);
  for (const k of ['V_DUPLICATE', 'V_LENGTH', 'V_TYPE']) assert.ok(vc.includes(k), `${k} in ${vc.join(',')}`);
  assert.equal(suggestVolumeSlug('hello-api-data', ['hello-api-data']), 'hello-api-data-2');
});

test('round E: volume kind + starter content (seed)', () => {
  const d = clone();
  const banner = d.files[0]!.fileSlug;
  const vs = app(d).volumes;
  // a folder volume seeded from files, a single-file volume seeded from a file, an archive seed
  vs.push(newSiteVolume('nodered-data', 'Node-RED', '', true, { kind: 'auto' }, 'dir', { kind: 'files', entries: [{ path: 'flows.json', fileSlug: banner }, { path: 'lib/settings.js', fileSlug: banner }] }));
  vs.push(newSiteVolume('one-file', 'One', '', true, { kind: 'auto' }, 'file', { kind: 'file', fileSlug: banner }));
  vs.push(newSiteVolume('arch', 'Arch', '', true, { kind: 'auto' }, 'dir', { kind: 'archive', blobSha256: 'sha256:' + 'a'.repeat(64), name: 'start.tar.gz', size: 1024 }));
  const api = app(d).containers.find((c) => c.containerSlug === A)!;
  api.volumes.push({ imageVolume: null, mountPath: '/nodered', source: { kind: 'volume', volumeSlug: 'nodered-data' }, readOnly: false });
  api.volumes.push({ imageVolume: null, mountPath: '/etc/one.json', source: { kind: 'volume', volumeSlug: 'one-file' }, readOnly: false });
  api.volumes.push({ imageVolume: null, mountPath: '/seed', source: { kind: 'volume', volumeSlug: 'arch' }, readOnly: false });
  // §21 #101: legacy File seeds are valid only in old revisions (agent mode); templates / drafts / publish reject them
  const ok = validateConfig(d, { ...ctx(), mode: 'agent' }).list.filter((x) => x.code.startsWith('V_VOLUME') || x.severity === 'error');
  assert.deepEqual(ok, []);
  assert.deepEqual(validateConfig(d, ctx()).list.filter((x) => x.severity === 'error').map((x) => x.code), ['V_VOLUME_SEED_FILE_REF', 'V_VOLUME_SEED_FILE_REF']);
  // errors
  const e = structuredClone(d);
  const ev = app(e).volumes;
  ev.find((v) => v.volumeSlug === 'nodered-data')!.seed = { kind: 'files', entries: [{ path: '../x', fileSlug: banner }, { path: 'a', fileSlug: 'nope' }, { path: 'a/b', fileSlug: banner }] };
  ev.find((v) => v.volumeSlug === 'one-file')!.seed = { kind: 'files', entries: [{ path: 'x', fileSlug: banner }] };
  ev.find((v) => v.volumeSlug === 'arch')!.seed = { kind: 'archive', blobSha256: 'x', name: 'start.zip', size: 300 * 1024 * 1024 };
  app(e).containers.find((c) => c.containerSlug === A)!.volumes.find((v) => v.mountPath === '/etc/one.json')!.mountPath = '/etc/one/';
  const el = validateConfig(e, { ...ctx(), mode: 'agent' }).list.filter((x) => x.severity === 'error');
  const has = (code: string, end: string) => assert.ok(el.some((x) => x.code === code && x.path.endsWith(end)), `${code} at …${end} in ${JSON.stringify(el.map((x) => [x.code, x.path]))}`);
  has('V_VOLUME_SEED', 'volumes[nodered-data].seed.entries[0].path');
  has('V_REF', 'volumes[nodered-data].seed.entries[1].fileSlug');
  has('V_VOLUME_SEED', 'volumes[nodered-data].seed.entries[1].path');
  has('V_VOLUME_SEED', 'volumes[one-file].seed');
  has('V_FORMAT', 'volumes[arch].seed.blobSha256');
  has('V_VOLUME_SEED', 'volumes[arch].seed.name');
  has('V_VOLUME_SEED', 'volumes[arch].seed.size');
  has('V_VOLUME_KIND', '.mountPath');
  assert.equal(seedPathError('lib/settings.js'), null);
  assert.ok(seedPathError('/abs') && seedPathError('a/./b') && seedPathError('a b'));
  assert.equal(seedLabel(null), 'Empty');
  assert.equal(seedLabel(vs[vs.length - 3]!.seed), '2 files');
  // legacy volumes normalize to dir / no seed
  const n = normalizeSiteConfig({ ...app(HELLO.config), volumes: [{ volumeSlug: 'x', label: 'x', description: '', backup: true, location: { kind: 'auto' } } as never] });
  assert.deepEqual([n.volumes[0]!.kind, n.volumes[0]!.seed], ['dir', null]);
});

test('legacy one-per-service containers + edge targets normalize to instances', () => {
  const legacy = {
    serviceSlug: 'redis',
    version: 'v1',
    enabled: true,
    env: [],
    volume: [
      { kind: 'data', name: 'data', mountPath: '/data' },
      { kind: 'file', fileSlug: 'banner', mountPath: '/etc/b.txt' },
    ],
    port: [{ portName: 'tcp', hostPort: 6379, protocol: 'tcp' }],
    command: null,
    restart: 'always',
    resources: { cpus: null, memoryMb: null },
    depends: [{ serviceSlug: 'x' }],
  } as unknown as Container;
  const n = normalizeContainer(legacy as never, CAT.images['redis:v1']);
  assert.deepEqual(n, {
    containerSlug: 'redis',
    serviceSlug: 'redis',
    version: 'v1',
    enabled: true,
    env: [],
    ports: [{ portName: 'tcp', hostPort: 6379, hostIp: '0.0.0.0', edge: true }],
    volumes: [
      { imageVolume: 'data', mountPath: null, source: { kind: 'volume', volumeSlug: 'redis-data' }, readOnly: false },
      { imageVolume: null, mountPath: '/etc/b.txt', source: { kind: 'file', fileSlug: 'banner' }, readOnly: true },
    ],
    command: null,
    entrypoint: null,
    restart: 'always',
    resources: { cpus: null, memoryMb: null },
    healthcheck: null,
    depends: [{ containerSlug: 'x' }],
  });
  const sc = normalizeSiteConfig({ host: '{{ var.x }}', containers: [legacy], edge: { httpPort: 80, httpsPort: null, maxBodyBytes: null, hosts: [], routes: [{ routeId: 'r', hosts: [], match: { pathPrefix: '/', methods: null } as never, order: 1, responseHeaders: null, action: { type: 'proxy', serviceSlug: 'redis', portName: 'tcp', stripPrefix: false, addPrefix: null, setHeaders: {}, removeHeaders: [], websocket: false, timeoutSec: 60 } as never }] } });
  assert.equal((sc.edge.routes[0]!.action as { containerSlug: string }).containerSlug, 'redis');
  assert.equal('serviceSlug' in sc.edge.routes[0]!.action, false);
  // idempotent
  assert.deepEqual(normalizeContainer(n as never, CAT.images['redis:v1']), n);
  const nc = normalizeConfig({ files: [{ fileSlug: 'f', label: 'F', description: '', mime: 'text/plain', editor: 'text', maxBytes: 10, content: null } as unknown as Config['files'][number]], sites: [] });
  assert.equal(nc.files[0]!.render, 'expressions');
});

test('files with expressions: rendered once per site, secrets allowed, no container.*, escape, line:col, V_FILE_RENDER, V_FILE_JSON', () => {
  const c = clone();
  const banner = 'Hi from {{ self.slug }} theme={{ var.theme }}\npw={{ secret.api-token }} lit={{ "{{" }}x}}\ndb={{ site.db.host }}';
  const cx = { ...ctx('agent'), detectedHosts: { app: '10.0.0.1', db: '10.0.0.2' }, fileContents: { banner } };
  assert.deepEqual(validateConfig(c, cx).errors, []);
  const ectx = siteFileExprContext(c, cx, 'app');
  const r = renderSiteFile(banner, ectx);
  assert.ok(r.ok, JSON.stringify(r));
  if (r.ok) {
    assert.equal(r.value, `Hi from app theme=green\npw=${c.secrets[0]!.value} lit={{x}}\ndb=${r.value.split('db=')[1]}`);
    assert.ok(r.masked.includes('pw=••••'));
    assert.equal(r.secret, true);
  }
  // container.* / file.* are not allowed: one copy is shared by every container of the site
  for (const src of ['x={{ container.env.THEME }}', 'p={{ file.banner.path }}', '{{ container.slug }}']) {
    const e = validateConfig(c, { ...cx, fileContents: { banner: src } }).errors.find((x) => x.code === 'V_FILE_RENDER');
    assert.ok(e, src);
    assert.match(e!.message, /E_NOT_ALLOWED .*files are shared by all containers on the site — use a variable instead/);
  }
  assert.equal(checkFileContent('a {{ container.env.X }}')[0]!.code, 'E_NOT_ALLOWED');
  // errors carry file:line:col
  const bad = 'line one\n  x={{ var.nope }}';
  const v = validateConfig(c, { ...cx, fileContents: { banner: bad } }).errors.find((x) => x.code === 'V_FILE_RENDER');
  assert.ok(v, 'V_FILE_RENDER');
  assert.match(v!.message, /^banner:2:8: E_UNKNOWN_PATH .*\(site app\)/);
  assert.equal(validateConfig(c, { ...cx, fileContents: { banner: bad } }).errors.filter((x) => x.code === 'V_FILE_RENDER').length, 1, 'rendered once per site');
  assert.deepEqual(checkFileContent('a\nb {{ nope.x }} {{ var.ok }}'), [{ code: 'E_UNKNOWN_PATH', message: 'unknown root "nope" (use var, secret, file, site, self, deployment, container, containers)', pos: 7, line: 2, col: 6 }]);
  assert.deepEqual(checkFileContent('{{ var.a | upper }}\n{{ "{{" }}'), []);
  assert.equal(checkFileContent('{{ var.a\n}}')[0]!.code, 'E_SYNTAX');
  // JSON files must stay JSON after rendering
  const j = clone();
  j.files[0]!.mime = 'application/json';
  assert.deepEqual(validateConfig(j, { ...cx, fileContents: { banner: '{"theme": "{{ var.theme }}"}' } }).errors, []);
  assert.ok(validateConfig(j, { ...cx, fileContents: { banner: '{"theme": {{ var.theme }}}' } }).errors.some((x) => x.code === 'V_FILE_JSON'));
  // render none: content is not checked; binary + expressions is refused
  j.files[0]!.render = 'none';
  assert.deepEqual(validateConfig(j, { ...cx, fileContents: { banner: '{{ nope' } }).errors, []);
  j.files[0]!.editor = 'binary';
  j.files[0]!.render = 'expressions';
  assert.ok(codes(j).includes('V_ENUM'));
});

test('local edits may rename / add / remove containers and change their image (§21 #113)', () => {
  const after = clone();
  app(after).containers[0]!.containerSlug = 'renamed';
  app(after).containers.push({ ...app(after).containers[0]!, containerSlug: 'added' });
  assert.deepEqual(checkLocalEdit(HELLO.config, after), []);
  const b = clone();
  app(b).containers[0]!.serviceSlug = 'redis';
  db(b).containers = [];
  assert.deepEqual(checkLocalEdit(HELLO.config, b), []);
});

test('round D: legacy data sources become declared site volumes (readOnly reset), volumes diff', () => {
  // round-C site config: no volumes list, a data source stored read-only by the old editor, a missing row
  const roundC = {
    host: null,
    containers: [
      { containerSlug: 'cassandra', serviceSlug: 'redis', version: 'v1', enabled: true, env: [], ports: [], volumes: [{ imageVolume: 'data', mountPath: null, source: { kind: 'data', name: 'cassandra-data' }, readOnly: true }], command: null, entrypoint: null, restart: 'always', resources: { cpus: null, memoryMb: null }, healthcheck: null, depends: [] },
      { containerSlug: 'cache', serviceSlug: 'redis', version: 'v1', enabled: true, env: [], ports: [], volumes: [], command: null, entrypoint: null, restart: 'always', resources: { cpus: null, memoryMb: null }, healthcheck: null, depends: [] },
    ],
  };
  assert.equal(isLegacySiteConfig(roundC), true);
  const n = normalizeSiteConfig(roundC as never, { 'redis:v1': CAT.images['redis:v1']! });
  assert.equal(isLegacySiteConfig(n), false);
  assert.deepEqual(n.containers[0]!.volumes, [{ imageVolume: 'data', mountPath: null, source: { kind: 'volume', volumeSlug: 'cassandra-data' }, readOnly: false }]);
  assert.deepEqual(n.containers[1]!.volumes, [{ imageVolume: 'data', mountPath: null, source: { kind: 'volume', volumeSlug: 'cache-data' }, readOnly: false }], 'legacy implicit row made explicit');
  assert.deepEqual(n.volumes, [newSiteVolume('cassandra-data'), newSiteVolume('cache-data')]);
  // idempotent; a current config is not auto-declared and keeps missing rows
  assert.deepEqual(normalizeSiteConfig(n, { 'redis:v1': CAT.images['redis:v1']! }), n);
  const cur = normalizeSiteConfig({ ...n, volumes: [], containers: [{ ...n.containers[1]!, volumes: [] }] }, { 'redis:v1': CAT.images['redis:v1']! });
  assert.deepEqual(cur.volumes, []);
  assert.deepEqual(cur.containers[0]!.volumes, []);
  // diff: volumes keyed by volumeSlug
  const before = clone();
  const after = clone();
  app(after).volumes[0]!.backup = false;
  app(after).volumes.push(newSiteVolume('extra'));
  const ch = diffConfigs(before, after).filter((x) => x.section === 'volumes');
  assert.deepEqual(ch.map((x) => [x.path, x.kind]), [['app/volumes/hello-api-data', 'changed'], ['app/volumes/extra', 'added']]);
  // local edits may add / change site volumes (no V_LOCAL_* issue)
  assert.deepEqual(checkLocalEdit(before, after), []);
});

test('round D: storage locations of files and site volumes (auto / ./ / absolute; V_LOCATION)', () => {
  const c = clone();
  assert.equal(locationPath({ kind: 'auto' }, 'files', 'banner'), './files/banner');
  assert.equal(locationPath({ kind: 'auto' }, 'volumes', 'v', '/var/lib/opm'), '/var/lib/opm/volumes/v');
  assert.equal(locationPath({ kind: 'path', path: './cfg/app.json' }, 'files', 'x', '/var/lib/opm/'), '/var/lib/opm/cfg/app.json');
  assert.equal(locationPath({ kind: 'path', path: '/srv/x' }, 'files', 'x', '/var/lib/opm'), '/srv/x');
  assert.equal(locationLabel({ kind: 'auto' }, 'files', 'banner', '/var/lib/opm'), 'auto · /var/lib/opm/files/banner');
  c.files[0]!.location = { kind: 'path', path: './cfg/banner.txt' };
  app(c).volumes[0]!.location = { kind: 'path', path: '/srv/hello-api' };
  assert.deepEqual(codes(c), []);
  const bad = (f: string, v?: string) => {
    const d = clone();
    d.files[0]!.location = { kind: 'path', path: f };
    if (v) app(d).volumes[0]!.location = { kind: 'path', path: v };
    return all(d).filter((x) => x.code === 'V_LOCATION').map((x) => x.path);
  };
  for (const p of ['./../etc/x', './a/../b', 'relative', '/', './blobs/x', './volumes/x', '{{ var.x }}', './a b']) assert.ok(bad(p).length, p);
  assert.deepEqual(bad('./files/custom-name'), []);
  // same place / nested on a site
  assert.ok(bad('./data/f', './data').length, 'file inside a volume');
  assert.ok(bad('/srv/a', '/srv/a').length, 'same absolute path');
  assert.ok(bad('./volumes/hello-api-data').length, 'reserved for volumes');
});

test('round E: diff notes for seed / kind changes', async () => {
  const { diffConfigs: dc, VOLUME_SEED_CHANGED_NOTE } = await import('./diff.js');
  const a = clone();
  app(a).volumes.push({ volumeSlug: 'nr', label: 'nr', description: '', backup: true, location: { kind: 'auto' }, kind: 'dir', seed: null });
  const b = structuredClone(a);
  app(b).volumes.find((v) => v.volumeSlug === 'nr')!.seed = { kind: 'files', entries: [{ path: 'flows.json', fileSlug: a.files[0]!.fileSlug }] };
  const ch = dc(a, b).filter((x) => x.section === 'volumes');
  assert.equal(ch.length, 1);
  assert.equal(ch[0]!.note, VOLUME_SEED_CHANGED_NOTE);
});

test('§21 #88 containers.<slug>.host / .port.<portName>: other containers of the site, V_CONTAINER_REF_DISABLED, unknown → error', () => {
  const c = clone();
  const web = app(c).containers.find((x) => x.containerSlug === W)!;
  web.env.push({ name: 'API_URL', value: 'http://{{ containers.hello-api.host }}:{{ containers.hello-api.port.http }}', secretRef: null });
  assert.deepEqual(codes(c), []);
  const ec = containerExprContext(c, ctx(), 'app', web);
  assert.equal(resolve(web.env.find((e) => e.name === 'API_URL')!.value!, ec, 'env').value, 'http://hello-api:8080');
  // a disabled target: still resolves, warning
  app(c).containers.find((x) => x.containerSlug === A)!.enabled = false;
  const w = validateConfig(c, ctx()).list.filter((x) => x.code === 'V_CONTAINER_REF_DISABLED');
  assert.equal(w.length, 1);
  assert.equal(w[0]!.severity, 'warning');
  assert.equal(w[0]!.path, `sites[app].config.containers[${W}].env[API_URL].value`);
  // unknown container / port; a container of ANOTHER site is unknown here
  const u = clone();
  const web2 = app(u).containers.find((x) => x.containerSlug === W)!;
  web2.env.push({ name: 'X', value: '{{ containers.redis.host }}', secretRef: null });
  web2.env.push({ name: 'Y', value: '{{ containers.hello-api.port.nope }}', secretRef: null });
  assert.deepEqual(codes(u), ['E_UNKNOWN_PATH', 'E_UNKNOWN_PATH']);
});
