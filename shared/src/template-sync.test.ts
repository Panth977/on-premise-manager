import { test } from 'node:test';
import assert from 'node:assert/strict';
import { applyTemplateSync, classifySync, cleanTemplateChoices, countTemplateSync, nextSkipRecords, planTemplateSync, templateSyncHash, templateSyncMissing } from './template-sync.js';
import { defaultSiteConfig, newEdgeHost, newEdgeRoute, newProxyAction } from './validate.js';
import type { Config, Container, FileDef, SecretDef, Site, TemplateSyncChange, Variable } from './types.js';

const variable = (slug: string, value: Variable['value'] = 'x', more: Partial<Variable> = {}): Variable => ({ variableSlug: slug, label: slug, description: '', type: 'string', value, required: false, options: null, validation: null, ...more });
const secret = (slug: string, value: string | null = null, more: Partial<SecretDef> = {}): SecretDef => ({ secretSlug: slug, label: slug, description: '', required: true, generate: { charset: 'alnum', length: 24 }, value, rotatedAt: value ? '2026-10-01T00:00:00Z' : null, ...more });
const file = (slug: string, hex = 'a', more: Partial<FileDef> = {}): FileDef => ({ fileSlug: slug, label: slug, description: '', mime: 'text/plain', editor: 'text', maxBytes: 1048576, content: { sha256: `sha256:${hex.repeat(64)}`, sizeBytes: 10 }, render: 'expressions', location: { kind: 'auto' }, ...more });
const container = (slug: string, version = '1.0.0', more: Partial<Container> = {}): Container => ({
  containerSlug: slug,
  serviceSlug: 'web',
  version,
  enabled: true,
  env: [{ name: 'PORT', value: '8080', secretRef: null }],
  ports: [{ portName: 'http', hostPort: null, hostIp: '0.0.0.0', edge: true }],
  volumes: [],
  command: null,
  entrypoint: null,
  restart: 'always',
  resources: { cpus: null, memoryMb: null },
  healthcheck: null,
  depends: [],
  ...more,
});
const site = (slug: string, containers: Container[] = [container('web')]): Site => {
  const config = defaultSiteConfig();
  config.containers = containers;
  config.edge.hosts = [newEdgeHost('app.example.com')];
  config.edge.routes = [newEdgeRoute('web', '/', newProxyAction('web', 'http'))];
  return { siteSlug: slug, label: slug.toUpperCase(), config };
};
const cfg = (more: Partial<Config> = {}): Config => ({ variables: [variable('domain', 'example.com')], secrets: [secret('db-pass')], files: [file('nginx')], sites: [site('app')], ...more });
const clone = <T>(v: T): T => JSON.parse(JSON.stringify(v)) as T;
/** base / theirs / ours from one config: the draft has its own secret value (generated at create) */
function triple() {
  const base = cfg();
  const theirs = clone(base);
  const ours = clone(base);
  ours.secrets[0]!.value = 'generated-per-deployment';
  ours.secrets[0]!.rotatedAt = '2026-10-02T00:00:00Z';
  return { base, theirs, ours };
}
const byKey = (p: TemplateSyncChange[], key: string) => p.find((c) => c.key === key);
const kinds = (p: TemplateSyncChange[]) => Object.fromEntries(p.map((c) => [c.key, c.kind]));

test('classifySync: the 3-way rule', () => {
  const s = (v: unknown) => ({ has: true, v });
  const none = { has: false, v: null };
  assert.equal(classifySync(s(1), s(1), s(1)), null);
  assert.equal(classifySync(s(1), s(2), s(1)), 'theirs');
  assert.equal(classifySync(s(1), s(1), s(3)), 'ours');
  assert.equal(classifySync(s(1), s(2), s(2)), 'same');
  assert.equal(classifySync(s(1), s(2), s(3)), 'conflict');
  assert.equal(classifySync(none, s(1), none), 'theirs'); // template added
  assert.equal(classifySync(s(1), none, s(1)), 'theirs'); // template removed
  assert.equal(classifySync(s(1), none, s(2)), 'conflict'); // template removed, you changed
  assert.equal(classifySync(none, none, s(1)), 'ours'); // you added
  assert.equal(classifySync(none, s(1), s(1)), 'same'); // both added the same
  assert.equal(classifySync(s({ a: 1, b: 2 }), s({ b: 2, a: 1 }), s({ a: 1, b: 2 })), null); // canonical equality
});

test('identical configs: no changes; a secret VALUE in the draft is never a change', () => {
  const { base, theirs, ours } = triple();
  assert.deepEqual(planTemplateSync(base, theirs, ours), []);
});

test('variables: definition vs value (values are deployment-owned; template default only opt-in)', () => {
  const { base, theirs, ours } = triple();
  ours.variables[0]!.value = 'prod.example.com'; // the deployment's value
  theirs.variables[0]!.description = 'The public domain'; // template changed the definition
  theirs.variables[0]!.value = 'example.org'; //            and its default
  theirs.variables.push(variable('region', null, { required: true })); // template added a required variable
  const p = planTemplateSync(base, theirs, ours);
  assert.equal(byKey(p, 'variables/domain')!.kind, 'theirs');
  assert.equal(byKey(p, 'variables/domain/value')!.kind, 'default');
  assert.equal(byKey(p, 'variables/region')!.kind, 'theirs');
  assert.equal(byKey(p, 'variables/region')!.theirsOp, 'added');
  // taking the definition keeps the deployment value
  const r = applyTemplateSync(base, theirs, ours, [{ key: 'variables/domain', take: 'theirs' }, { key: 'variables/region', take: 'theirs' }], { values: { region: 'eu' } });
  assert.deepEqual(r.errors, []);
  assert.equal(r.config.variables[0]!.description, 'The public domain');
  assert.equal(r.config.variables[0]!.value, 'prod.example.com');
  assert.equal(r.config.variables[1]!.value, 'eu');
  // "use template default"
  const r2 = applyTemplateSync(base, theirs, ours, [{ key: 'variables/domain/value', take: 'theirs' }]);
  assert.equal(r2.config.variables[0]!.value, 'example.org');
  assert.equal(r2.config.variables[0]!.description, '', 'definition not taken');
  // the default unit is never recorded as skipped (the base moves to the template default)
  assert.ok(!r2.notTaken.some((c) => c.key === 'variables/domain/value'));
  // a template default that did not change since the base is not a change at all; a deployment value is not "your change"
  const t3 = clone(base);
  assert.deepEqual(planTemplateSync(base, t3, ours), []);
});

test('secrets: definitions + generate rules only; values never compared, shown or copied', () => {
  const { base, theirs, ours } = triple();
  theirs.secrets[0]!.generate = { charset: 'hex', length: 32 };
  theirs.secrets.push(secret('api-key'));
  const p = planTemplateSync(base, theirs, ours);
  assert.equal(byKey(p, 'secrets/db-pass')!.kind, 'theirs');
  for (const c of p) assert.ok(!JSON.stringify(c).includes('generated-per-deployment'), 'no secret value in the plan');
  const r = applyTemplateSync(base, theirs, ours, cleanTemplateChoices(p));
  const s = r.config.secrets.find((x) => x.secretSlug === 'db-pass')!;
  assert.equal(s.value, 'generated-per-deployment', 'the draft value is kept');
  assert.deepEqual(s.generate, { charset: 'hex', length: 32 });
  assert.equal(r.config.secrets.find((x) => x.secretSlug === 'api-key')!.value, null, 'a new secret starts without a value');
});

test('files: metadata + content ref; conflict when both changed the content differently', () => {
  const { base, theirs, ours } = triple();
  theirs.files[0] = file('nginx', 'b');
  ours.files[0] = file('nginx', 'c');
  const p = planTemplateSync(base, theirs, ours);
  assert.equal(byKey(p, 'files/nginx')!.kind, 'conflict');
  assert.equal(byKey(p, 'files/nginx')!.scalar, false);
  const r = applyTemplateSync(base, theirs, ours, []);
  assert.deepEqual(r.unresolved, ['files/nginx']);
  const keep = applyTemplateSync(base, theirs, ours, [{ key: 'files/nginx', take: 'ours' }]);
  assert.deepEqual(keep.unresolved, []);
  assert.equal(keep.config.files[0]!.content!.sha256, `sha256:${'c'.repeat(64)}`);
  const take = applyTemplateSync(base, theirs, ours, [{ key: 'files/nginx', take: 'theirs' }]);
  assert.equal(take.config.files[0]!.content!.sha256, `sha256:${'b'.repeat(64)}`);
  const bad = applyTemplateSync(base, theirs, ours, [{ key: 'files/nginx', take: 'custom', value: 'x' }]);
  assert.equal(bad.errors.length, 1);
  // both changed the same way = no-op
  ours.files[0] = file('nginx', 'b');
  assert.equal(byKey(planTemplateSync(base, theirs, ours), 'files/nginx')!.kind, 'same');
});

test('sites: whole site added / removed, settings field by field, scalar conflict with a merged value', () => {
  const { base, theirs, ours } = triple();
  theirs.sites.push(site('db', []));
  theirs.sites[0]!.config.agentPort = 7090; // template
  ours.sites[0]!.label = 'Production'; //      yours
  theirs.sites[0]!.config.host = 'tpl.local';
  ours.sites[0]!.config.host = 'mine.local';
  const p = planTemplateSync(base, theirs, ours);
  assert.equal(byKey(p, 'sites/db')!.kind, 'theirs');
  assert.equal(byKey(p, 'sites/db')!.theirsOp, 'added');
  assert.equal(byKey(p, 'sites/app/settings/agentPort')!.kind, 'theirs');
  assert.equal(byKey(p, 'sites/app/settings/label')!.kind, 'ours');
  const host = byKey(p, 'sites/app/settings/host')!;
  assert.equal(host.kind, 'conflict');
  assert.equal(host.scalar, true);
  const r = applyTemplateSync(base, theirs, ours, [...cleanTemplateChoices(p), { key: 'sites/app/settings/host', take: 'custom', value: 'merged.local' }]);
  assert.deepEqual(r.errors, []);
  assert.deepEqual(r.config.sites.map((s) => s.siteSlug), ['app', 'db']);
  assert.equal(r.config.sites[0]!.config.agentPort, 7090);
  assert.equal(r.config.sites[0]!.label, 'Production');
  assert.equal(r.config.sites[0]!.config.host, 'merged.local');
  // template removed a site you did not touch → clean removal; you changed it → conflict
  const t2 = clone(base);
  t2.sites = [];
  assert.equal(byKey(planTemplateSync(base, t2, triple().ours), 'sites/app')!.kind, 'theirs');
  assert.equal(byKey(planTemplateSync(base, t2, ours), 'sites/app')!.kind, 'conflict');
});

test('containers: by containerSlug; a rename = remove + add; field-level merge; env by name; depends', () => {
  const { base, theirs, ours } = triple();
  // template: bumps the image + renames container web → frontend? no: adds a worker and bumps web
  theirs.sites[0]!.config.containers[0]!.version = '1.1.0';
  theirs.sites[0]!.config.containers[0]!.env.push({ name: 'LOG', value: 'info', secretRef: null });
  theirs.sites[0]!.config.containers.push(container('worker', '1.1.0', { depends: [{ containerSlug: 'web' }] }));
  // draft: changed an env value of web
  ours.sites[0]!.config.containers[0]!.env[0]!.value = '9090';
  const p = planTemplateSync(base, theirs, ours);
  assert.equal(byKey(p, 'sites/app/containers/web/image')!.kind, 'theirs');
  assert.equal(byKey(p, 'sites/app/containers/web/env/LOG')!.kind, 'theirs');
  assert.equal(byKey(p, 'sites/app/containers/web/env/PORT')!.kind, 'ours');
  assert.equal(byKey(p, 'sites/app/containers/worker')!.kind, 'theirs');
  // env values are scalars: a conflict can be merged inline; a secretRef row compares as { secretRef }
  assert.equal(byKey(p, 'sites/app/containers/web/env/PORT')!.scalar, true);
  assert.equal(byKey(p, 'sites/app/containers/web/env/PORT')!.ours, '9090');
  const t5 = clone(theirs);
  t5.sites[0]!.config.containers[0]!.env[0]!.value = '7070';
  const p5 = planTemplateSync(base, t5, ours);
  assert.equal(byKey(p5, 'sites/app/containers/web/env/PORT')!.kind, 'conflict');
  const r5 = applyTemplateSync(base, t5, ours, [{ key: 'sites/app/containers/web/env/PORT', take: 'custom', value: '7171' }]);
  assert.deepEqual(r5.config.sites[0]!.config.containers[0]!.env[0], { name: 'PORT', value: '7171', secretRef: null });
  const t6 = clone(base);
  t6.sites[0]!.config.containers[0]!.env[0] = { name: 'PORT', value: null, secretRef: 'db-pass' };
  const p6 = planTemplateSync(base, t6, triple().ours);
  assert.deepEqual(byKey(p6, 'sites/app/containers/web/env/PORT')!.theirs, { secretRef: 'db-pass' });
  const r6 = applyTemplateSync(base, t6, triple().ours, cleanTemplateChoices(p6));
  assert.deepEqual(r6.config.sites[0]!.config.containers[0]!.env[0], { name: 'PORT', value: null, secretRef: 'db-pass' });
  const r = applyTemplateSync(base, theirs, ours, cleanTemplateChoices(p));
  const web = r.config.sites[0]!.config.containers[0]!;
  assert.equal(web.version, '1.1.0');
  assert.deepEqual(web.env.map((e) => `${e.name}=${e.value}`), ['PORT=9090', 'LOG=info']);
  assert.deepEqual(r.config.sites[0]!.config.containers.map((c) => c.containerSlug), ['web', 'worker']);
  // rename in the template: web → frontend = remove web + add frontend (identity is the slug)
  const t2 = clone(base);
  t2.sites[0]!.config.containers[0]!.containerSlug = 'frontend';
  const p2 = planTemplateSync(base, t2, triple().ours);
  assert.equal(byKey(p2, 'sites/app/containers/web')!.theirsOp, 'removed');
  assert.equal(byKey(p2, 'sites/app/containers/frontend')!.theirsOp, 'added');
  // depends is a field of the container
  const t3 = clone(base);
  t3.sites[0]!.config.containers[0]!.depends = [{ containerSlug: 'db' }];
  assert.equal(byKey(planTemplateSync(base, t3, triple().ours), 'sites/app/containers/web/depends')!.kind, 'theirs');
  // you removed a container the template changed → conflict
  const o4 = triple().ours;
  o4.sites[0]!.config.containers = [];
  assert.equal(byKey(planTemplateSync(base, theirs, o4), 'sites/app/containers/web')!.kind, 'conflict');
});

test('edge hosts / routes by hostname / routeId, edge ports; volumes by volumeSlug; list order follows the template', () => {
  const { base, theirs, ours } = triple();
  theirs.sites[0]!.config.edge.hosts.unshift(newEdgeHost('www.example.com'));
  theirs.sites[0]!.config.edge.routes.unshift(newEdgeRoute('api', '/api', newProxyAction('web', 'http')));
  theirs.sites[0]!.config.edge.httpsPort = null;
  theirs.sites[0]!.config.volumes = [{ volumeSlug: 'data', label: '', description: '', backup: true, location: { kind: 'auto' }, kind: 'dir', seed: null }];
  ours.sites[0]!.config.edge.routes[0]!.match.path = '/app'; // yours
  const p = planTemplateSync(base, theirs, ours);
  assert.equal(byKey(p, 'sites/app/edge/hosts/www.example.com')!.kind, 'theirs');
  assert.equal(byKey(p, 'sites/app/edge/routes/api')!.kind, 'theirs');
  assert.equal(byKey(p, 'sites/app/edge/routes/web')!.kind, 'ours');
  assert.equal(byKey(p, 'sites/app/edge/httpsPort')!.kind, 'theirs');
  assert.equal(byKey(p, 'sites/app/volumes/data')!.kind, 'theirs');
  assert.equal(byKey(p, 'sites/app/volumes/data')!.section, 'volumes');
  const r = applyTemplateSync(base, theirs, ours, cleanTemplateChoices(p));
  const e = r.config.sites[0]!.config.edge;
  assert.deepEqual(e.hosts.map((h) => h.hostname), ['www.example.com', 'app.example.com']);
  assert.deepEqual(e.routes.map((x) => x.routeId), ['api', 'web']);
  assert.equal(e.routes[1]!.match.path, '/app');
  assert.equal(e.httpsPort, null);
  assert.equal(r.config.sites[0]!.config.volumes.length, 1);
});

test('skip semantics: not taken → recorded; shown as skipped until the template changes it again', () => {
  const { base, theirs, ours } = triple();
  theirs.sites[0]!.config.containers[0]!.version = '2.0.0';
  theirs.variables[0]!.label = 'Domain';
  const p = planTemplateSync(base, theirs, ours);
  const r = applyTemplateSync(base, theirs, ours, [{ key: 'variables/domain', take: 'theirs' }]);
  assert.deepEqual(r.notTaken.map((c) => c.key), ['sites/app/containers/web/image']);
  const skips = nextSkipRecords(r.notTaken, [], 2, '2026-10-08T00:00:00Z', 'u1');
  assert.equal(skips[0]!.theirsHash, byKey(p, 'sites/app/containers/web/image')!.theirsHash);
  // after the apply the base = the template now
  const base2 = clone(theirs);
  const p2 = planTemplateSync(base2, theirs, r.config, skips);
  assert.deepEqual(kinds(p2), { 'sites/app/containers/web/image': 'skipped' });
  assert.equal(p2[0]!.skippedAt, '2026-10-08T00:00:00Z');
  // a later sync with nothing new keeps the record; taking it later works
  const r2 = applyTemplateSync(base2, theirs, r.config, [], { skipped: skips });
  assert.deepEqual(nextSkipRecords(r2.notTaken, skips, 3, 'later', 'u2'), skips);
  const r3 = applyTemplateSync(base2, theirs, r.config, [{ key: 'sites/app/containers/web/image', take: 'theirs' }], { skipped: skips });
  assert.equal(r3.config.sites[0]!.config.containers[0]!.version, '2.0.0');
  assert.deepEqual(r3.notTaken, []);
  // the template changes it AGAIN → a new change (conflict with your kept value), no longer "skipped"
  const theirs3 = clone(theirs);
  theirs3.sites[0]!.config.containers[0]!.version = '3.0.0';
  assert.deepEqual(kinds(planTemplateSync(base2, theirs3, r.config, skips)), { 'sites/app/containers/web/image': 'conflict' });
});

test('link a template without a base: everything is an addition, equal items are no-ops, differences are conflicts', () => {
  const { theirs, ours } = triple();
  ours.sites[0]!.config.containers[0]!.version = '0.9.0';
  ours.variables = [];
  const p = planTemplateSync(null, theirs, ours);
  assert.equal(byKey(p, 'variables/domain')!.kind, 'theirs');
  assert.equal(byKey(p, 'files/nginx')!.kind, 'same');
  assert.equal(byKey(p, 'sites/app/containers/web/image')!.kind, 'conflict');
  const c = countTemplateSync(p);
  assert.equal(c.conflict, 1);
  assert.equal(c.templateChanges, c.theirs + c.conflict + c.same + c.default);
});

test('apply: unknown keys, duplicate choices, values for unknown variables are errors; same / ours need no choice', () => {
  const { base, theirs, ours } = triple();
  theirs.variables[0]!.label = 'X';
  const r = applyTemplateSync(base, theirs, ours, [{ key: 'variables/nope', take: 'theirs' }, { key: 'variables/domain', take: 'theirs' }, { key: 'variables/domain', take: 'theirs' }], { values: { ghost: 1 } });
  assert.deepEqual(r.errors.map((e) => e.key).sort(), ['variables/domain', 'variables/ghost/value', 'variables/nope'].sort());
});

test('missing values + hash', () => {
  const c = cfg({ variables: [variable('a', null, { required: true }), variable('b', '', { required: false })], secrets: [secret('s1', null, { generate: null }), secret('s2', null)] });
  const m = templateSyncMissing(c);
  assert.deepEqual(m.variables.map((v) => v.variableSlug), ['a']);
  assert.deepEqual(m.secrets.map((s) => s.secretSlug), ['s1']);
  assert.equal(templateSyncHash({ has: false, v: null }), '-');
  assert.equal(templateSyncHash({ a: 1, b: 2 }), templateSyncHash({ b: 2, a: 1 }));
  assert.notEqual(templateSyncHash({ a: 1 }), templateSyncHash({ a: 2 }));
  assert.match(templateSyncHash('x'), /^[0-9a-f]{16}$/);
});
