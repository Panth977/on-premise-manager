import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { checkDevToolExpr, checkLocalEdit, containerEnvIsSecret, isSafeRelativePath, Issues, siteDevtoolExprContext, siteFileExprContext, normalizeConfig, normalizeSiteConfig, normalizeSiteDevTools, normalizeStoredDevToolPreset, validateConfig, validateSiteDevTools, VALIDATION_CODES, type ValidationContext } from './validate.js';
import { containerEnvExpr, containerEnvOfExpr, devToolContainerEnvRefs, newDevToolPreset, suggestDevToolEnv, type DevToolPreset } from './devtools.js';
import { canonicalize } from './canonical.js';
import { resolve, tryResolve } from './expr.js';
import { diffConfigs } from './diff.js';
import { applyTemplateSync, planTemplateSync } from './template-sync.js';
import type { Config, SiteConfig } from './types.js';

const here = dirname(fileURLToPath(import.meta.url));
const load = (f: string) => JSON.parse(readFileSync(join(here, '..', 'testdata', f), 'utf8'));
const HELLO = load('hello-config.json') as { deploymentId: string; config: Config };
const CAT = load('hello-catalog.json') as Omit<ValidationContext, 'mode' | 'deploymentId'>;
const V = load('devtools-validate-vectors.json') as { cases: { name: string; siteSlug: string; config: Config; catalog: { images: ValidationContext['images'] }; expectedCodes: string[]; expected: { path: string; code: string; severity: string }[] }[] };
const ctx = (mode: ValidationContext['mode'] = 'publish'): ValidationContext => ({ mode, deploymentId: HELLO.deploymentId, services: CAT.services, images: CAT.images, notFoundPageIds: CAT.notFoundPageIds });
const clone = <T>(v: T): T => structuredClone(v);
const site = (c: Config, slug: string) => c.sites.find((s) => s.siteSlug === slug)!.config;
const sha = (v: unknown) => createHash('sha256').update(canonicalize(v)).digest('hex');
const redisPreset = (slug = 'cache'): DevToolPreset => {
  const p = newDevToolPreset('redis');
  p.presetSlug = slug;
  p.name = 'Cache';
  p.target = { kind: 'container', containerSlug: 'redis', portName: 'tcp' };
  p.auth.password = { kind: 'expr', expr: '{{ secret.api-token }}' };
  return p;
};
const devCodes = (c: Config, mode: ValidationContext['mode'] = 'publish') =>
  validateConfig(c, ctx(mode))
    .list.filter((x) => x.path.includes('.devTools'))
    .map((x) => `${x.severity}:${x.code}`);

test('devtools validation vectors (Go mirror: agent/internal/validate)', () => {
  assert.ok(V.cases.length >= 25);
  for (const c of V.cases) {
    const is = new Issues('publish');
    validateSiteDevTools(c.config.sites.find((s) => s.siteSlug === c.siteSlug)!, c.config, c.catalog.images, is);
    const got = is.list.map(({ path, code, severity }) => ({ path, code, severity }));
    assert.deepEqual(got, c.expected, c.name);
    assert.deepEqual([...new Set(got.map((x) => x.code))].sort(), c.expectedCodes, c.name);
  }
  // every code of the vectors is documented
  for (const c of V.cases) for (const x of c.expected) if (x.code.startsWith('V_')) assert.ok(VALIDATION_CODES[x.code], x.code);
  for (const code of ['V_DEVTOOL_SLUG', 'V_DEVTOOL_DUP', 'V_DEVTOOL_MAX', 'V_DEVTOOL_KIND', 'V_DEVTOOL_TARGET', 'V_DEVTOOL_CONTAINER', 'V_DEVTOOL_PORT', 'V_DEVTOOL_VOLUME', 'V_DEVTOOL_LITERAL', 'V_DEVTOOL_SECRET', 'V_DEVTOOL_VAR', 'V_DEVTOOL_AUTH', 'V_DEVTOOL_OPTION']) assert.ok(VALIDATION_CODES[code], code);
  // every devtools code is covered by a vector (positive) and a valid case exists (negative)
  const covered = new Set(V.cases.flatMap((c) => c.expectedCodes));
  for (const code of ['V_DEVTOOL_SLUG', 'V_DEVTOOL_DUP', 'V_DEVTOOL_MAX', 'V_DEVTOOL_KIND', 'V_DEVTOOL_TARGET', 'V_DEVTOOL_CONTAINER', 'V_DEVTOOL_PORT', 'V_DEVTOOL_VOLUME', 'V_DEVTOOL_LITERAL', 'V_DEVTOOL_SECRET', 'V_DEVTOOL_VAR', 'V_DEVTOOL_AUTH', 'V_DEVTOOL_OPTION', 'E_SYNTAX', 'E_NOT_ALLOWED', 'E_UNKNOWN_PATH']) assert.ok(covered.has(code), code);
  assert.ok(V.cases.some((c) => c.expected.length === 0 && (site(c.config, c.siteSlug).devTools?.length ?? 0) >= 8));
});

test('validateConfig runs validateSiteDevTools for every site, in every mode', () => {
  const c = clone(HELLO.config);
  assert.deepEqual(devCodes(c), []);
  site(c, 'db').devTools = [redisPreset()];
  assert.deepEqual(devCodes(c), []);
  assert.ok(validateConfig(c, ctx()).ok);
  const bad = redisPreset('bad');
  bad.auth.password = { kind: 'literal', value: 'hunter2' };
  site(c, 'app').devTools = [bad];
  // save level: errors in every mode; §21 #103: a static value is a warning (allowed, plain text in the config)
  for (const mode of ['template', 'draft', 'publish', 'agent'] as const) assert.deepEqual(devCodes(c, mode), ['error:V_DEVTOOL_CONTAINER', 'warning:V_DEVTOOL_LITERAL'], mode);
  // a preset pointing at a disabled container: warning only, still publishable
  const d = clone(HELLO.config);
  site(d, 'db').devTools = [redisPreset()];
  site(d, 'db').containers[0]!.enabled = false;
  assert.deepEqual(devCodes(d), ['warning:V_DEVTOOL_CONTAINER']);
});

test('template mode: secret refs check the definitions (templates have no values)', () => {
  const c = clone(HELLO.config);
  c.secrets[0]!.value = null;
  site(c, 'db').devTools = [redisPreset()];
  assert.deepEqual(devCodes(c, 'template'), []);
  site(c, 'db').devTools![0]!.auth.password = { kind: 'expr', expr: '{{ secret.nope }}' };
  assert.deepEqual(devCodes(c, 'template'), ['error:V_DEVTOOL_SECRET']);
});

test('checkDevToolExpr + isSafeRelativePath', () => {
  assert.deepEqual(checkDevToolExpr('{{ secret.a }}:{{ var.b | upper }}@{{ site.db.host }}/{{ containers.redis.host }}/{{ deployment.id }}/{{ self.slug }}'), []);
  assert.equal(checkDevToolExpr('{{ container.slug }}')[0]!.code, 'E_NOT_ALLOWED');
  assert.equal(checkDevToolExpr('{{ file.x.path }}')[0]!.code, 'E_NOT_ALLOWED');
  assert.equal(checkDevToolExpr('{{ secret.a | nope }}')[0]!.code, 'E_UNKNOWN_FILTER');
  assert.equal(checkDevToolExpr('{{ secret.a.b }}')[0]!.code, 'E_UNKNOWN_PATH');
  assert.equal(checkDevToolExpr('{{ secret.a ')[0]!.code, 'E_SYNTAX');
  for (const ok of ['a.db', 'dir/a.db', 'a..b.db', '.hidden/x']) assert.ok(isSafeRelativePath(ok), ok);
  for (const bad of ['', '/a.db', '../a.db', 'x/../../a', 'x/..', 'a\\b', '{{ var.x }}', 'x'.repeat(1025), null]) assert.ok(!isSafeRelativePath(bad), String(bad));
});

test('normalizeSiteConfig: devTools omitted when absent / null / empty; hash unchanged (§21 #97 a)', () => {
  const plain = normalizeConfig(clone(HELLO.config));
  for (const s of plain.sites) assert.ok(!('devTools' in s.config), s.siteSlug);
  const before = sha(plain);
  // normalizing again, or with an explicit empty list / null, writes the same bytes
  assert.equal(sha(normalizeConfig(clone(plain))), before);
  const withEmpty = clone(plain);
  site(withEmpty, 'db').devTools = [];
  (site(withEmpty, 'app') as unknown as { devTools: unknown }).devTools = null;
  const n = normalizeConfig(withEmpty);
  for (const s of n.sites) assert.ok(!('devTools' in s.config), s.siteSlug);
  assert.equal(sha(n), before);
  assert.equal(canonicalize(n), canonicalize(plain));
  assert.deepEqual(normalizeSiteDevTools(undefined), {});
  assert.deepEqual(normalizeSiteDevTools([]), {});
  assert.deepEqual(normalizeSiteDevTools('x'), { devTools: 'x' }); // kept for the validator (V_TYPE)
});

test('normalizeStoredDevToolPreset: exact stored shape (known keys only, defaults filled)', () => {
  const raw = {
    presetSlug: 'pg',
    name: 'PG',
    kind: 'postgres',
    extra: 1,
    target: { kind: 'host', host: 'h', port: 5432, containerSlug: 'leftover' },
    auth: { password: { kind: 'expr', expr: '{{ secret.x }}', value: 'junk' }, junk: true },
    options: { timeoutSec: 5, defaultHeaders: [{ name: 'A', value: 'b', enabled: true }], junk: 1 },
  };
  const p = normalizeStoredDevToolPreset(raw);
  assert.deepEqual(p, {
    presetSlug: 'pg',
    name: 'PG',
    description: '',
    kind: 'postgres',
    target: { kind: 'host', host: 'h', port: 5432 },
    auth: { username: 'postgres', password: { kind: 'expr', expr: '{{ secret.x }}' }, database: null, clientId: null, httpScheme: 'none', token: { kind: 'none' }, apiKeyHeader: null },
    options: { tls: false, tlsInsecure: false, tlsServerName: null, sslMode: null, timeoutSec: 5, maxRows: null, consistency: null, scheme: null, basePath: null, followRedirects: null, defaultHeaders: [{ name: 'A', value: 'b' }], cleanSession: null, keepAliveSec: null },
    readOnly: false,
  });
  // idempotent; unknown kinds pass through for the validator
  assert.deepEqual(normalizeStoredDevToolPreset(p), p);
  assert.deepEqual(normalizeStoredDevToolPreset({ kind: 'mongo', x: 1 }), { kind: 'mongo', x: 1 });
  const sc = normalizeSiteConfig({ ...clone(HELLO.config.sites[0]!.config), devTools: [raw as unknown as DevToolPreset] });
  assert.deepEqual(sc.devTools, [p]);
  assert.deepEqual(Object.keys(sc).slice(-1), ['devTools']);
});

test('checkLocalEdit: presets change locally like any other config (§21 #113)', () => {
  const head = clone(HELLO.config);
  site(head, 'db').devTools = [redisPreset()];
  const same = clone(head);
  // key order does not matter (the agent re-encodes)
  const p = site(same, 'db').devTools![0]!;
  site(same, 'db').devTools = [Object.fromEntries(Object.entries(p).reverse()) as unknown as DevToolPreset];
  assert.deepEqual(checkLocalEdit(head, same).map((x) => x.code), []);
  const changed = clone(head);
  site(changed, 'db').devTools![0]!.readOnly = true;
  assert.deepEqual(checkLocalEdit(head, changed), []);
  const removed = clone(head);
  delete site(removed, 'db').devTools;
  assert.deepEqual(checkLocalEdit(head, removed), []);
  const added = clone(HELLO.config);
  site(added, 'app').devTools = [redisPreset()];
  assert.deepEqual(checkLocalEdit(HELLO.config, added), []);
  // absent vs [] is the same (both = no presets)
  const empty = clone(HELLO.config);
  site(empty, 'app').devTools = [];
  assert.deepEqual(checkLocalEdit(HELLO.config, empty), []);
});

test('diffConfigs: section devTools, path <site>/devtools/<presetSlug>', () => {
  const a = clone(HELLO.config);
  site(a, 'db').devTools = [redisPreset('cache'), redisPreset('old')];
  const b = clone(a);
  site(b, 'db').devTools = [{ ...redisPreset('cache'), readOnly: true }, redisPreset('new')];
  const ch = diffConfigs(a, b).filter((x) => x.section === 'devTools');
  assert.deepEqual(
    ch.map((x) => `${x.kind}:${x.path}`),
    ['changed:db/devtools/cache', 'added:db/devtools/new', 'removed:db/devtools/old'],
  );
  assert.equal((ch[0]!.after as DevToolPreset).readOnly, true);
  assert.equal(ch[0]!.siteSlug, 'db');
  // absent = []: adding the first preset / removing the last
  assert.deepEqual(diffConfigs(HELLO.config, a).filter((x) => x.section === 'devTools').map((x) => `${x.kind}:${x.path}`), ['added:db/devtools/cache', 'added:db/devtools/old']);
  assert.deepEqual(diffConfigs(a, HELLO.config).filter((x) => x.section === 'devTools').map((x) => `${x.kind}:${x.path}`), ['removed:db/devtools/cache', 'removed:db/devtools/old']);
  // same presets in another key order: no change
  const r = clone(a);
  site(r, 'db').devTools = site(r, 'db').devTools!.map((p) => Object.fromEntries(Object.entries(p).reverse()) as unknown as DevToolPreset);
  assert.deepEqual(diffConfigs(a, r).filter((x) => x.section === 'devTools'), []);
  // added / removed sites carry their presets
  const c = clone(a);
  c.sites.push({ siteSlug: 'edge2', label: 'E2', config: { ...clone(site(a, 'db')), devTools: [redisPreset('x')] } });
  assert.deepEqual(diffConfigs(a, c).filter((x) => x.section === 'devTools').map((x) => `${x.kind}:${x.path}`), ['added:edge2/devtools/x']);
  assert.deepEqual(diffConfigs(c, a).filter((x) => x.section === 'devTools').map((x) => `${x.kind}:${x.path}`), ['removed:edge2/devtools/x']);
  assert.deepEqual(diffConfigs(null, a).filter((x) => x.section === 'devTools').length, 2);
});

test('template sync: devTools keyed by presetSlug (theirs / ours / same / conflict / skipped), empty list omitted', () => {
  const base = clone(HELLO.config);
  site(base, 'db').devTools = [redisPreset('cache'), redisPreset('shared')];
  const theirs = clone(base);
  const ours = clone(base);
  site(theirs, 'db').devTools = [{ ...redisPreset('cache'), name: 'Cache (template)' }, { ...redisPreset('shared'), readOnly: true }, redisPreset('t-new')];
  site(ours, 'db').devTools = [redisPreset('cache'), { ...redisPreset('shared'), name: 'Mine' }, redisPreset('o-new')];
  const plan = planTemplateSync(base, theirs, ours);
  const k = Object.fromEntries(plan.filter((c) => c.section === 'devTools').map((c) => [c.key, c.kind]));
  assert.deepEqual(k, { 'sites/db/devtools/cache': 'theirs', 'sites/db/devtools/shared': 'conflict', 'sites/db/devtools/t-new': 'theirs', 'sites/db/devtools/o-new': 'ours' });
  assert.equal(plan.find((c) => c.key === 'sites/db/devtools/shared')!.scalar, false);
  const r = applyTemplateSync(base, theirs, ours, [
    { key: 'sites/db/devtools/cache', take: 'theirs' },
    { key: 'sites/db/devtools/shared', take: 'theirs' },
  ]);
  assert.deepEqual(r.errors, []);
  assert.deepEqual(r.unresolved, []);
  assert.deepEqual(
    site(r.config, 'db').devTools!.map((p) => `${p.presetSlug}:${p.name}:${p.readOnly}`),
    ['cache:Cache (template):false', 'shared:Cache:true', 'o-new:Cache:false'],
  );
  assert.deepEqual(r.notTaken.map((c) => c.key), ['sites/db/devtools/t-new']);
  // same: both added the same preset
  const t2 = clone(base);
  const o2 = clone(base);
  site(t2, 'db').devTools!.push(redisPreset('both'));
  site(o2, 'db').devTools!.push(redisPreset('both'));
  assert.equal(planTemplateSync(base, t2, o2).find((c) => c.key === 'sites/db/devtools/both')!.kind, 'same');
  // skipped: an earlier sync did not take the template's t-new (the base moved to the template; the draft lacks it)
  const tHash = plan.find((c) => c.key === 'sites/db/devtools/t-new')!.theirsHash;
  const sk = planTemplateSync(theirs, theirs, ours, [{ key: 'sites/db/devtools/t-new', theirsHash: tHash, templateVersion: 2, skippedAt: '2026-10-08T00:00:00Z', skippedBy: 'u' }]);
  assert.equal(sk.find((c) => c.key === 'sites/db/devtools/t-new')!.kind, 'skipped');
  // taking a removal of the LAST preset leaves the key absent (never devTools: [])
  const b3 = clone(HELLO.config);
  site(b3, 'app').devTools = [redisPreset('only')];
  const t3 = clone(HELLO.config); // the template removed it
  const o3 = clone(b3);
  const r3 = applyTemplateSync(b3, t3, o3, [{ key: 'sites/app/devtools/only', take: 'theirs' }]);
  assert.deepEqual(r3.errors, []);
  assert.ok(!('devTools' in site(r3.config, 'app')));
  // no base (link a template): the template's presets are additions
  const r4 = planTemplateSync(null, b3, HELLO.config);
  assert.equal(r4.find((c) => c.key === 'sites/app/devtools/only')!.kind, 'theirs');
});

// ───────────────────────────── §21 #99 containers.<c>.env.<NAME> (field devtool)
test('siteDevtoolExprContext: final env of every container (image default ← container env, resolved; secrets marked)', () => {
  const c = clone(HELLO.config);
  const api = site(c, 'app').containers.find((x) => x.containerSlug === 'hello-api')!;
  api.env.push({ name: 'TOKEN_COPY', value: 'Bearer {{ container.env.API_TOKEN }}', secretRef: null });
  api.env.push({ name: 'LOOP', value: '{{ container.env.LOOP }}', secretRef: null });
  c.secrets.find((s) => s.secretSlug === 'api-token')!.value = 'tok-123';
  const ectx = siteDevtoolExprContext(c, { ...ctx('agent') }, 'app');
  const env = ectx.containers!['hello-api']!.env!;
  assert.equal(env.GREETING!.secret, false);
  assert.equal(env.GREETING!.value, (c.variables.find((v) => v.variableSlug === 'greeting')!.value as string) ?? null);
  assert.deepEqual(env.API_TOKEN, { value: 'tok-123', secret: true });
  assert.deepEqual(env.TOKEN_COPY, { value: 'Bearer tok-123', secret: true });
  assert.equal(env.LOOP!.error!.code, 'E_CYCLE');
  assert.ok('PORT' in env && 'DATA_DIR' in env, 'image env defaults are included');
  // the context resolves with field devtool only
  const r = resolve('{{ containers.hello-api.env.API_TOKEN }}', ectx, 'devtool');
  assert.deepEqual([r.value, r.masked, r.secret], ['tok-123', '••••', true]);
  assert.equal(tryResolve('{{ containers.hello-api.env.API_TOKEN }}', ectx, 'file').ok, false);
  assert.equal(tryResolve('{{ containers.hello-api.env.LOOP }}', ectx, 'devtool').ok, false);
  // the plain file context has no env (files cannot use it)
  assert.equal(siteFileExprContext(c, ctx('agent'), 'app').containers!['hello-api']!.env, undefined);
  // cloud (secretsSet): secret values masked
  const cloud = siteDevtoolExprContext(c, { ...ctx('publish'), secretsSet: ['api-token'] }, 'app');
  assert.deepEqual(cloud.containers!['hello-api']!.env!.API_TOKEN, { value: '•••', secret: true });
  assert.equal(resolve('{{ containers.hello-api.env.API_TOKEN }}', cloud, 'devtool').masked, '••••');
  // checkDevToolExpr = checkExpr(field devtool)
  assert.deepEqual(checkDevToolExpr('{{ containers.pg.env.POSTGRES_USER }}'), []);
  assert.equal(checkDevToolExpr('{{ container.env.X }}')[0]!.code, 'E_NOT_ALLOWED');
  assert.equal(containerEnvIsSecret(api, 'TOKEN_COPY'), true);
  assert.equal(containerEnvIsSecret(api, 'GREETING'), false);
  assert.equal(containerEnvIsSecret(api, 'LOOP'), false);
});

test('suggestDevToolEnv + container env expression helpers', () => {
  const pg = ['PGDATA', 'POSTGRES_PASSWORD', 'POSTGRES_DB', 'POSTGRES_USER', 'TZ'];
  assert.deepEqual(suggestDevToolEnv('postgres', 'username', pg), ['POSTGRES_USER']);
  assert.deepEqual(suggestDevToolEnv('postgres', 'password', pg), ['POSTGRES_PASSWORD']);
  assert.deepEqual(suggestDevToolEnv('postgres', 'database', pg), ['POSTGRES_DB']);
  assert.deepEqual(suggestDevToolEnv('postgres', 'username', ['PGUSER', 'APP_USER']), ['PGUSER', 'APP_USER']);
  assert.deepEqual(suggestDevToolEnv('redis', 'password', ['REDIS_PASSWORD', 'REDIS_PORT_NUMBER']), ['REDIS_PASSWORD']);
  assert.deepEqual(suggestDevToolEnv('cassandra', 'username', ['CASSANDRA_USER', 'CASSANDRA_PASSWORD']), ['CASSANDRA_USER']);
  assert.deepEqual(suggestDevToolEnv('mqtt', 'password', ['MQTT_PASSWORD']), ['MQTT_PASSWORD']);
  assert.deepEqual(suggestDevToolEnv('http', 'token', ['API_TOKEN', 'X']), ['API_TOKEN']);
  assert.deepEqual(suggestDevToolEnv('postgres', 'password', ['TZ']), []);
  assert.equal(containerEnvExpr('postgress', 'POSTGRES_USER'), '{{ containers.postgress.env.POSTGRES_USER }}');
  assert.deepEqual(containerEnvOfExpr(' {{containers.postgress.env.POSTGRES_USER}} '), { containerSlug: 'postgress', name: 'POSTGRES_USER' });
  assert.equal(containerEnvOfExpr('x{{ containers.a.env.B }}'), null);
  const p = newDevToolPreset('postgres');
  p.auth.username = '{{ containers.pg.env.POSTGRES_USER }}';
  p.auth.password = { kind: 'expr', expr: '{{ containers.pg.env.POSTGRES_PASSWORD | default("x") }}' };
  assert.deepEqual(devToolContainerEnvRefs(p), ['pg.POSTGRES_PASSWORD', 'pg.POSTGRES_USER']);
  assert.deepEqual(devToolContainerEnvRefs(newDevToolPreset('redis')), []);
});
