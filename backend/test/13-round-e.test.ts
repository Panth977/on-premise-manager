/** Round E (BRIEF-E, SPEC §12.1 / §21 #68+): edge nginx parity — new + legacy edge shapes on write / read, validation codes, migrate-e. */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { gzipSync } from 'node:zlib';
import { newEdgeRoute, newProxyAction, SEED_ARCHIVE_MAX, type Config, type EdgeRoute, type Site, type ValidationIssue } from '@opm/shared';
import { abucket, adb, enrollSite, HELLO, helloDeployment, load, person, rejects, sha256Hex, publish } from './helpers.ts';
import { runDaily } from '../functions/src/jobs/daily.ts';
import { jcsHash } from '../functions/src/core/util.ts';
import { applyPlanE, planMigrationE } from '../scripts/migrate-e.ts';
import { verifySchema } from '../scripts/verify-schema.ts';

type Doc = Record<string, any>;
const appSite = (cfg: Config) => structuredClone(cfg.sites.find((s) => s.siteSlug === 'app')!) as Site;
const CC = 'public, max-age=3600, must-revalidate';

/** A pre-E edge (as stored before round E): {pathPrefix} matches, proxy timeoutSec, no responseHeaders / maxBodyBytes. */
function legacyEdge(edge: Doc): Doc {
  const { maxBodyBytes, ...e } = edge;
  void maxBodyBytes;
  return {
    ...e,
    hosts: edge.hosts.map(({ responseHeaders, ...h }: Doc) => (void responseHeaders, h)),
    routes: edge.routes.map((r: Doc) => {
      const { responseHeaders, match, action, ...rest } = r;
      void responseHeaders;
      let a = action;
      if (action.type === 'proxy') {
        const { connectTimeoutSec, sendTimeoutSec, readTimeoutSec, rewrite, requestBuffering, responseBuffering, maxBodyBytes: mb, ...pa } = action;
        void connectTimeoutSec, void sendTimeoutSec, void rewrite, void requestBuffering, void responseBuffering, void mb;
        a = { ...pa, timeoutSec: readTimeoutSec };
      }
      return { ...rest, match: { pathPrefix: match.path, methods: match.methods }, action: a };
    }),
  };
}
/** The hello app site with a response-only header on the api route (as the user's "Single" template has). */
function appWithCacheHeader(): Site {
  const s = appSite(HELLO().config);
  const api = s.config.edge.routes.find((r) => r.routeId === 'api')!;
  if (api.action.type === 'proxy') api.action.setHeaders = { ...api.action.setHeaders, 'Cache-Control': CC };
  return s;
}
/** pre-E site: legacy edge + site volumes without kind / seed */
const legacySite = (s: Site): Doc => ({
  ...s,
  config: { ...s.config, volumes: s.config.volumes.map(({ kind, seed, ...v }) => (void kind, void seed, v)), edge: legacyEdge(s.config.edge as unknown as Doc) },
});
const assertVolumesE = (sc: Doc) => {
  assert.ok(sc.volumes.length >= 1);
  for (const v of sc.volumes) assert.ok(v.kind === 'dir' && v.seed === null, JSON.stringify(v));
};
const legacyConfig = (cfg: Doc): Doc => ({ ...cfg, sites: cfg.sites.map((s: Site) => (s.siteSlug === 'app' ? legacySite(s) : s)) });

/** Asserts the hello app edge is in the round-E shape with the fixture's values (websocket kept false, readTimeoutSec 30). */
function assertE(edge: Doc, cache = false) {
  assert.equal(edge.maxBodyBytes, null);
  assert.deepEqual(edge.hosts[0].responseHeaders, { set: {}, add: {}, remove: [], always: [] });
  assert.deepEqual(
    edge.routes.map((r: Doc) => [r.routeId, r.order, r.match]),
    [
      ['api', 1, { kind: 'prefix', path: '/api', stopRegex: false, methods: null }],
      ['old', 2, { kind: 'prefix', path: '/old', stopRegex: false, methods: null }],
      ['web', 3, { kind: 'prefix', path: '/', stopRegex: false, methods: null }],
    ],
  );
  const api = edge.routes[0];
  assert.equal(api.responseHeaders, null);
  assert.deepEqual(
    { ...api.action },
    {
      type: 'proxy',
      containerSlug: 'hello-api',
      portName: 'http',
      stripPrefix: true,
      rewrite: null,
      addPrefix: null,
      setHeaders: cache ? { 'X-Opm-Site': '{{ self.slug }}', 'Cache-Control': CC } : { 'X-Opm-Site': '{{ self.slug }}' },
      removeHeaders: [],
      websocket: false,
      connectTimeoutSec: 60,
      sendTimeoutSec: 60,
      readTimeoutSec: 30,
      requestBuffering: false,
      responseBuffering: false,
      maxBodyBytes: null,
    },
  );
  for (const r of edge.routes) assert.ok(!('pathPrefix' in r.match) && !('timeoutSec' in r.action), JSON.stringify(r));
}

test('new edge shape: regex / exact / rewrite / stop-regex routes, response headers, timeouts, body limits are saved as sent (draft PUT + template PATCH)', async () => {
  const admin = await person('admin');
  const { dep, templateId } = await helloDeployment(admin);
  const site = appSite(HELLO().config);
  const e = site.config.edge;
  e.maxBodyBytes = 10 * 1024 * 1024;
  e.hosts[0]!.responseHeaders = { set: { 'X-Frame-Options': 'DENY' }, add: { 'X-Served-By': '$host' }, remove: ['Server'], always: ['X-Frame-Options'] };
  const sse: EdgeRoute = {
    ...newEdgeRoute('events', '^/events/(\\d+)$', { ...newProxyAction('hello-api', 'http'), rewrite: { regex: '^/events/(\\d+)$', replacement: '/stream?id=$1' }, readTimeoutSec: 86400, responseBuffering: false, maxBodyBytes: 0 }, 4),
    match: { kind: 'regex', path: '(?i)^/events/(\\d+)$', stopRegex: false, methods: ['GET'] },
    responseHeaders: { set: { 'Cache-Control': 'no-cache' }, add: {}, remove: [], always: [] },
  };
  const s3: EdgeRoute = { ...newEdgeRoute('s3', '/s3/', { ...newProxyAction('hello-api', 'http'), stripPrefix: true, addPrefix: '/s3', requestBuffering: true, connectTimeoutSec: 5, sendTimeoutSec: 600 }, 5), match: { kind: 'prefix', path: '/s3/', stopRegex: true, methods: null } };
  const rw: EdgeRoute = { ...newEdgeRoute('legacy-app', '/app', { type: 'rewrite', regex: '^/app(/.*)?$', replacement: '/$1' }, 6), match: { kind: 'exact', path: '/app', stopRegex: false, methods: null } };
  const see: EdgeRoute = { ...newEdgeRoute('see', '/see', { type: 'redirect', to: '/other$is_args$args', status: 303, keepPath: false }, 7), match: { kind: 'exact', path: '/see', stopRegex: false, methods: null } };
  e.routes.push(sse, s3, rw, see);
  const w = await admin.api.putDraftSite({ deploymentId: dep.deploymentId, siteSlug: 'app', body: { ifRevision: 0, site } });
  assert.ok(!w.validation.issues.some((i) => i.severity === 'error'), JSON.stringify(w.validation.issues));
  const saved = (await admin.api.getDeployment({ deploymentId: dep.deploymentId })).draft.sites.find((s) => s.siteSlug === 'app')!.config.edge;
  assert.deepEqual(saved, e);
  const raw = JSON.parse((await adb.collection('deployments').doc(dep.deploymentId).get()).data()!._draft);
  assert.deepEqual(raw.sites.find((s: Doc) => s.siteSlug === 'app').config.edge, e, 'stored as sent');
  // template PATCH
  const tpl = await admin.api.getTemplate({ templateId });
  const tw = await admin.api.patchTemplate({ templateId, body: { ifVersion: tpl.version, sites: tpl.sites.map((s) => (s.siteSlug === 'app' ? site : s)) } });
  assert.deepEqual(tw.template.sites.find((s) => s.siteSlug === 'app')!.config.edge, e);
  // publishable (edge rules hold at publish level too)
  const p = await publish(admin.api, dep.deploymentId, { ifRevision: 1, summary: 'round E edge' } );
  const rev = await admin.api.getRevision({ deploymentId: dep.deploymentId, revId: p.revision.revId });
  assert.deepEqual(rev.config.sites.find((s) => s.siteSlug === 'app')!.config.edge, e);
});

test('legacy edge shape from a client (pathPrefix / timeoutSec, no responseHeaders / maxBodyBytes) is normalized on write, not rejected; legacy stored data normalized on read', async () => {
  const admin = await person('admin');
  const { dep, templateId } = await helloDeployment(admin);
  const id = dep.deploymentId;
  // draft PUT site
  const w = await admin.api.putDraftSite({ deploymentId: id, siteSlug: 'app', body: { ifRevision: 0, site: legacySite(appWithCacheHeader()) as unknown as Site } });
  assert.equal(w.validation.ok, true, JSON.stringify(w.validation.issues));
  const raw0 = JSON.parse((await adb.collection('deployments').doc(id).get()).data()!._draft);
  assertE(raw0.sites.find((s: Doc) => s.siteSlug === 'app').config.edge, true);
  assertVolumesE(raw0.sites.find((s: Doc) => s.siteSlug === 'app').config);
  // draft PUT sites section
  const all = (await admin.api.getDeployment({ deploymentId: id })).draft.sites.map((s) => (s.siteSlug === 'app' ? legacySite(appSite(HELLO().config)) : s));
  await admin.api.putDraftSection({ deploymentId: id, section: 'sites', body: { ifRevision: 1, items: all as unknown as Site[] } });
  assertE((await admin.api.getDeployment({ deploymentId: id })).draft.sites.find((s) => s.siteSlug === 'app')!.config.edge as unknown as Doc);
  // template PATCH
  const tpl = await admin.api.getTemplate({ templateId });
  const tw = await admin.api.patchTemplate({ templateId, body: { ifVersion: tpl.version, sites: tpl.sites.map((s) => (s.siteSlug === 'app' ? (legacySite(s) as unknown as Site) : s)) } });
  assertE(tw.template.sites.find((s) => s.siteSlug === 'app')!.config.edge as unknown as Doc);
  assertE(JSON.parse((await adb.collection('templates').doc(templateId).get()).data()!._body).sites.find((s: Doc) => s.siteSlug === 'app').config.edge);
  // normalize on read: a legacy edge written directly to Firestore is served in the round-E shape
  const tRef = adb.collection('templates').doc(templateId);
  await tRef.update({ _body: JSON.stringify(legacyConfig(JSON.parse((await tRef.get()).data()!._body))) });
  const t = await admin.api.getTemplate({ templateId });
  assertE(t.sites.find((s) => s.siteSlug === 'app')!.config.edge as unknown as Doc);
  assertVolumesE(t.sites.find((s) => s.siteSlug === 'app')!.config as unknown as Doc);
  // a PATCH that does not touch sites writes the normalized edge back
  await admin.api.patchTemplate({ templateId, body: { ifVersion: t.version, description: 'touched' } });
  assertE(JSON.parse((await tRef.get()).data()!._body).sites.find((s: Doc) => s.siteSlug === 'app').config.edge);
});

test('validation: V_EDGE_REGEX → 422 invalid on draft + template save; W_RESPONSE_HEADER_AS_REQUEST is a warning (saved, publishable)', async () => {
  const admin = await person('admin');
  const { dep, templateId } = await helloDeployment(admin);
  const id = dep.deploymentId;
  // lookahead: not in RE2 ∩ JS
  const bad = appSite(HELLO().config);
  bad.config.edge.routes.push({ ...newEdgeRoute('look', '/x', newProxyAction('hello-api', 'http'), 4), match: { kind: 'regex', path: '^/(?=admin)', stopRegex: false, methods: null } });
  const e = await rejects(admin.api.putDraftSite({ deploymentId: id, siteSlug: 'app', body: { ifRevision: 0, site: bad } }), 'invalid', 422);
  assert.ok((e.details.errors as Doc[]).some((x) => x.code === 'V_EDGE_REGEX' && /look/.test(x.path)), JSON.stringify(e.details));
  // bad rewrite regex (backreference) in a proxy rewrite, on a template
  const bad2 = appSite(HELLO().config);
  const api = bad2.config.edge.routes[0]!;
  if (api.action.type === 'proxy') Object.assign(api.action, { stripPrefix: false, rewrite: { regex: '^/api/(a)\\1', replacement: '/$1' } });
  const tpl = await admin.api.getTemplate({ templateId });
  const e2 = await rejects(admin.api.patchTemplate({ templateId, body: { ifVersion: tpl.version, sites: tpl.sites.map((s) => (s.siteSlug === 'app' ? bad2 : s)) } }), 'invalid', 422);
  assert.ok((e2.details.errors as Doc[]).some((x) => x.code === 'V_EDGE_REGEX'), JSON.stringify(e2.details));
  // response-only header as a request header → warning only
  const w = await admin.api.putDraftSite({ deploymentId: id, siteSlug: 'app', body: { ifRevision: 0, site: appWithCacheHeader() } });
  assert.equal(w.validation.ok, true);
  const warn = w.validation.issues.filter((i) => i.code === 'W_RESPONSE_HEADER_AS_REQUEST');
  assert.equal(warn.length, 1, JSON.stringify(w.validation.issues));
  assert.equal(warn[0]!.severity, 'warning');
  assert.match(warn[0]!.path, /routes\[api\]\.action\.setHeaders\.Cache-Control/);
  const tw = await admin.api.patchTemplate({ templateId, body: { ifVersion: tpl.version, sites: tpl.sites.map((s) => (s.siteSlug === 'app' ? appWithCacheHeader() : s)) } });
  assert.ok(tw.validation.issues.some((i) => i.code === 'W_RESPONSE_HEADER_AS_REQUEST' && i.severity === 'warning'));
  await publish(admin.api, id, { ifRevision: 1, summary: 'warning only' } );
});

test('migrate-e: legacy template / draft / snapshot / revision → dry run lines + notes, apply (headers untouched), rerun 0, verify-schema 0', async () => {
  const admin = await person('admin');
  const { dep, templateId } = await helloDeployment(admin);
  const id = dep.deploymentId;
  // a plain-prefix route without trailing "/" (note) and the Cache-Control request header (note); publish first
  await admin.api.putDraftSite({ deploymentId: id, siteSlug: 'app', body: { ifRevision: 0, site: appWithCacheHeader() } });
  const p = await publish(admin.api, id, { ifRevision: 1, summary: 'r1' } );
  const tRef = adb.collection('templates').doc(templateId);
  const t0 = (await tRef.get()).data()!;
  const body0 = JSON.parse(t0._body);
  body0.sites = body0.sites.map((s: Site) => (s.siteSlug === 'app' ? { ...s, config: { ...s.config, edge: appWithCacheHeader().config.edge } } : s));
  await tRef.update({ _body: JSON.stringify(legacyConfig(body0)) });
  const t0b = (await tRef.get()).data()!;
  const dRef = adb.collection('deployments').doc(id);
  const d0 = (await dRef.get()).data()!;
  await dRef.update({ _draft: JSON.stringify(legacyConfig(JSON.parse(d0._draft))), _srcSnapshot: JSON.stringify(legacyConfig(JSON.parse(d0._srcSnapshot))) });
  const rRef = dRef.collection('revisions').doc(p.revision.revId);
  const r0 = (await rRef.get()).data()!;
  const legacyRev = legacyConfig(JSON.parse(r0._config));
  await rRef.update({ _config: JSON.stringify(legacyRev), hash: jcsHash(legacyRev) });
  const mine = [`templates/${templateId}`, `deployments/${id}`];
  const isMine = (l: string) => mine.some((m) => l.startsWith(m));
  // verify-schema before: legacy edge = violation (each document kind)
  const v0 = (await verifySchema(adb)).filter(isMine);
  for (const pth of [`templates/${templateId}:`, `deployments/${id} draft:`, `deployments/${id} srcTemplate:`, `deployments/${id}/revisions/`])
    assert.ok(v0.some((l) => l.startsWith(pth) && /route api: legacy match \{pathPrefix\}/.test(l)), `${pth}\n${v0.join('\n')}`);
  assert.ok(v0.some((l) => /legacy proxy timeoutSec/.test(l)) && v0.some((l) => /edge maxBodyBytes missing/.test(l)) && v0.some((l) => /host \*: responseHeaders/.test(l)) && v0.some((l) => /route old: responseHeaders missing/.test(l)), v0.join('\n'));
  assert.ok(v0.some((l) => /volume hello-api-data: kind missing/.test(l)) && v0.some((l) => /volume hello-api-data: seed missing/.test(l)), v0.join('\n'));
  assert.ok(!v0.some((l) => /V_ENUM|V_RANGE|V_TYPE|V_VOLUME_KIND/.test(l)), `validator duplicates are not printed:\n${v0.join('\n')}`);
  // dry run
  const plan = await planMigrationE(adb);
  const paths = plan.ops.map((o) => o.path);
  for (const pth of [`templates/${templateId}`, `deployments/${id}`, `deployments/${id}/revisions/${p.revision.revId}`]) assert.ok(paths.includes(pth), pth);
  const tdoc = plan.docs.find((d) => d.path.startsWith(`templates/${templateId}`))!;
  assert.ok(tdoc.lines.includes('site app: 3 of 3 route(s) migrated'), tdoc.lines.join('\n'));
  assert.ok(tdoc.lines.includes('site app: volumes → kind dir, seed none (starts empty): hello-api-data'), tdoc.lines.join('\n'));
  assert.ok(plan.counts.volumes! >= 4, JSON.stringify(plan.counts));
  assert.ok(tdoc.lines.includes('site app: edge maxBodyBytes → null (unlimited)'));
  assert.ok(tdoc.lines.includes('site app: host *: responseHeaders → empty'));
  assert.ok(tdoc.lines.some((l) => l.startsWith('site app: route api: match {pathPrefix "/api"} → prefix "/api" (stopRegex off); timeoutSec 30 → readTimeoutSec 30 (connect 60, send 60)') && /websocket false \(kept\)/.test(l)), tdoc.lines.join('\n'));
  assert.ok(!tdoc.lines.some((l) => l.startsWith('site db:')), 'the db site has no routes and a round-E edge already');
  assert.ok(tdoc.notes.some((n) => /route api: prefix "\/api" has no trailing "\/"/.test(n)), tdoc.notes.join('\n'));
  assert.ok(tdoc.notes.some((n) => /route old: prefix "\/old" has no trailing/.test(n)));
  assert.ok(!tdoc.notes.some((n) => /route web: prefix/.test(n)), '"/" is not noted');
  assert.ok(tdoc.notes.some((n) => /route api: setHeaders Cache-Control: .* W_RESPONSE_HEADER_AS_REQUEST/.test(n)), tdoc.notes.join('\n'));
  const ddoc = plan.docs.find((d) => d.path.startsWith(`deployments/${id} `))!;
  assert.ok(ddoc.lines.includes('draft: site app: 3 of 3 route(s) migrated') && ddoc.lines.includes('srcTemplate snapshot: site app: 3 of 3 route(s) migrated'), ddoc.lines.join('\n'));
  assert.ok(plan.counts.routesMigrated! >= 12 && plan.counts.notesResponseHeader! >= 3 && plan.counts.notesPlainPrefix! >= 8, JSON.stringify(plan.counts));
  assert.equal((await tRef.get()).data()!._body, t0b._body, 'dry run wrote nothing');
  // apply
  await applyPlanE(adb, plan);
  const t1 = (await tRef.get()).data()!;
  assert.equal(t1.version, t0.version, 'template version unchanged');
  assert.equal(t1.updatedAt, t0.updatedAt, 'template updatedAt unchanged');
  const b1 = JSON.parse(t1._body);
  assertE(b1.sites.find((s: Doc) => s.siteSlug === 'app').config.edge, true);
  // nothing else changed: the body equals the legacy body with only the app edge replaced
  const expect = legacyConfig(body0);
  const appAfter = b1.sites.find((x: Doc) => x.siteSlug === 'app').config;
  assertVolumesE(appAfter);
  assert.deepEqual(
    appAfter.volumes.map(({ kind, seed, ...v }: Doc) => (void kind, void seed, v)),
    expect.sites.find((s: Doc) => s.siteSlug === 'app').config.volumes,
    'volumes: only kind / seed added',
  );
  expect.sites = expect.sites.map((s: Doc) => (s.siteSlug === 'app' ? { ...s, config: { ...s.config, volumes: appAfter.volumes, edge: appAfter.edge } } : s));
  assert.deepEqual(b1, expect);
  const d1 = (await dRef.get()).data()!;
  assertE(JSON.parse(d1._draft).sites.find((s: Doc) => s.siteSlug === 'app').config.edge, true);
  assertE(JSON.parse(d1._srcSnapshot).sites.find((s: Doc) => s.siteSlug === 'app').config.edge);
  const r1 = (await rRef.get()).data()!;
  const rc = JSON.parse(r1._config);
  assertE(rc.sites.find((s: Doc) => s.siteSlug === 'app').config.edge, true);
  assert.equal(r1.hash, jcsHash(rc));
  assert.notEqual(r1.hash, jcsHash(legacyRev), 'hash recomputed');
  assert.equal(r1.catalogHash, r0.catalogHash, 'catalog unchanged');
  const audit = (await adb.collection('auditLog').where('action', '==', 'system.migration').get()).docs.map((x) => JSON.parse(x.data()._data)).filter((x) => x.migration === 'e');
  assert.ok(audit.some((a) => a.docs.some((d: Doc) => d.path.startsWith(`templates/${templateId}`) && d.notes.length >= 3) && a.counts.routesMigrated >= 3), JSON.stringify(audit).slice(0, 500));
  // idempotent + verify-schema clean
  const again = await planMigrationE(adb);
  assert.deepEqual(again.ops.filter((o) => isMine(o.path)).map((o) => o.path), []);
  assert.deepEqual((await verifySchema(adb)).filter(isMine), []);
  assert.equal((await rRef.get()).data()!.hash, r1.hash);
});

// ───────────────────────────────────────── round E additions: required variables (shared/testdata/required-vars-vectors.json)
type Vec = { name: string; mode: string; variable: { required: boolean; value: string | null }; secretValue: string | null; expect: Pick<ValidationIssue, 'path' | 'code' | 'severity'>[] };
const VEC = load('required-vars-vectors.json') as { codes: string[]; cases: Vec[] };
const vec = (name: string) => VEC.cases.find((c) => c.name.startsWith(name))!;
const pick = (issues: ValidationIssue[]) =>
  issues
    .filter((i) => VEC.codes.includes(i.code))
    .map(({ path, code, severity }) => ({ path, code, severity }))
    .sort((a, b) => (a.path + a.code < b.path + b.code ? -1 : 1));
const sorted = (x: Vec['expect']) => [...x].sort((a, b) => (a.path + a.code < b.path + b.code ? -1 : 1));
/** The vector base config: hello + variable server-ip (host + env SERVER_URL) + required secret jwt-key (env JWT). */
function reqConfig(required: boolean, value: string | null): Config {
  const c = structuredClone(HELLO().config);
  c.variables.push({ variableSlug: 'server-ip', label: 'Server IP', description: '', type: 'string', value, required, options: null, validation: null });
  c.secrets.push({ secretSlug: 'jwt-key', label: 'JWT key', description: '', required: true, generate: null, value: null, rotatedAt: null });
  const app = c.sites.find((s) => s.siteSlug === 'app')!.config;
  app.host = '{{ var.server-ip }}';
  for (const k of app.containers) k.env.push({ name: 'SERVER_URL', value: 'http://{{ var.server-ip | upper }}:8080', secretRef: null });
  app.containers[0]!.env.push({ name: 'JWT', value: 'Bearer {{ secret.jwt-key }}', secretRef: null });
  return c;
}
async function reqTemplate(admin: Awaited<ReturnType<typeof person>>, required: boolean) {
  const { templateId } = await helloDeployment(admin);
  const c = reqConfig(required, null);
  const tpl = await admin.api.getTemplate({ templateId });
  const w = await admin.api.patchTemplate({ templateId, body: { ifVersion: tpl.version, variables: c.variables, secrets: c.secrets.map((s) => ({ ...s, value: null, rotatedAt: null })), sites: c.sites } });
  return { templateId, w };
}

test('required variables: template save (required null → nothing; optional null → INFO), draft V_VAR_REQUIRED warning, publish blocked, publish ok when set', async () => {
  const admin = await person('admin');
  // template + required null → no issue
  const r1 = await reqTemplate(admin, true);
  assert.equal(r1.w.validation.ok, true);
  assert.deepEqual(pick(r1.w.validation.issues), sorted(vec('template + required null').expect));
  // template + optional null → one INFO on the variable (never counted as warning / error)
  const r2 = await reqTemplate(admin, false);
  assert.equal(r2.w.validation.ok, true);
  assert.deepEqual(pick(r2.w.validation.issues), sorted(vec('template + optional null').expect));
  assert.ok(r2.w.validation.issues.some((i) => i.code === 'V_VAR_OPTIONAL_NULL' && i.severity === 'info'));
  // draft from the required template: one V_VAR_REQUIRED + V_SECRET_REQUIRED (warnings in the draft: saves work)
  const d = await admin.api.createDeployment({ body: { label: `req-${Date.now()}`, templateId: r1.templateId, tagSlugs: [], managerAccountUids: [] } });
  const id = d.deployment.deploymentId;
  const dep = await admin.api.getDeployment({ deploymentId: id });
  const wv = await admin.api.putDraftSection({ deploymentId: id, section: 'variables', body: { ifRevision: 0, items: dep.draft.variables } });
  assert.equal(wv.validation.ok, true);
  assert.deepEqual(pick(wv.validation.issues), sorted(vec('draft + required null').expect));
  // publish: blocked by the errors on the variable / secret only
  const diff = await admin.api.getDiff({ deploymentId: id });
  assert.deepEqual(pick(diff.blockers), sorted(vec('publish + required null').expect));
  assert.ok(!diff.warnings.some((i) => i.severity !== 'warning'), 'diff warnings hold warnings only');
  const e = await rejects(publish(admin.api, id, { ifRevision: wv.draftVersion, summary: 'x' } ), 'invalid', 422);
  assert.deepEqual(pick(e.details.errors as ValidationIssue[]), sorted(vec('publish + required null').expect));
  // set the variable + the secret → clean, publish works
  const set = vec('draft + required set');
  const w2 = await admin.api.putDraftSection({ deploymentId: id, section: 'variables', body: { ifRevision: wv.draftVersion, items: dep.draft.variables.map((v) => (v.variableSlug === 'server-ip' ? { ...v, value: set.variable.value } : v)) } });
  const cur = (await admin.api.getDeployment({ deploymentId: id })).draft.secrets;
  const w3 = await admin.api.putDraftSection({ deploymentId: id, section: 'secrets', body: { ifRevision: w2.draftVersion, items: cur.map((x) => (x.secretSlug === 'jwt-key' ? { ...x, plainValue: set.secretValue } : x)) as never } });
  assert.deepEqual(pick(w3.validation.issues), sorted(set.expect));
  const p = await publish(admin.api, id, { ifRevision: w3.draftVersion, summary: 'vars set' } );
  assert.equal(p.revision.seq, 1);
});

// ───────────────────────────────────────── round E additions: volume kind + starter content (seed)
async function uploadSeed(admin: Awaited<ReturnType<typeof person>>, data: Buffer) {
  const sha = `sha256:${sha256Hex(data)}`;
  const u = await admin.api.createUpload({ body: { purpose: 'seed-archive', sizeBytes: data.length, sha256: sha } });
  await admin.api.putUploadPart({ uploadId: u.uploadId, index: '0', body: data });
  const done = await admin.api.completeUpload({ uploadId: u.uploadId });
  return { sha, done, u };
}

test('seed archives: createUpload purpose seed-archive ≤ 200 MiB (gzip only), seed on a volume saved + published, agent may fetch it, daily keeps it', async () => {
  const admin = await person('admin');
  const sha0 = `sha256:${'e'.repeat(64)}`;
  // limits: blob 60 MiB → 413; seed-archive 60 MiB ok; > SEED_ARCHIVE_MAX → 413; unknown purpose → invalid
  await rejects(admin.api.createUpload({ body: { purpose: 'blob', sizeBytes: 60 * 1024 * 1024, sha256: sha0 } }), 'too-large', 413);
  const big = await admin.api.createUpload({ body: { purpose: 'seed-archive', sizeBytes: 60 * 1024 * 1024, sha256: sha0 } });
  assert.equal(big.partCount, Math.ceil((60 * 1024 * 1024) / big.partSize));
  await rejects(admin.api.createUpload({ body: { purpose: 'seed-archive', sizeBytes: SEED_ARCHIVE_MAX + 1, sha256: sha0 } }), 'too-large', 413);
  await rejects(admin.api.createUpload({ body: { purpose: 'nope' as never, sizeBytes: 1, sha256: sha0 } }), 'invalid');
  // not gzip → 422 V_FORMAT on the first part
  const notGz = Buffer.from('plain text, not an archive');
  const nu = await admin.api.createUpload({ body: { purpose: 'seed-archive', sizeBytes: notGz.length, sha256: `sha256:${sha256Hex(notGz)}` } });
  const ne = await rejects(admin.api.putUploadPart({ uploadId: nu.uploadId, index: '0', body: notGz }), 'invalid', 422);
  assert.ok((ne.details.errors as Doc[]).some((x) => x.code === 'V_FORMAT'), JSON.stringify(ne.details));
  // a real .tar.gz-like (gzip) archive → content-addressed blob
  const data = gzipSync(Buffer.from(`starter ${Date.now()}`));
  const { sha, done } = await uploadSeed(admin, data);
  assert.deepEqual(done.blob, { sha256: sha, sizeBytes: data.length });
  assert.equal(done.upload.purpose, 'blob');
  assert.ok(!('_seedArchive' in (done.upload as Doc)), 'internal flag not exposed');
  assert.equal(Buffer.from(await (await admin.api.getBlob({ sha256: sha })).arrayBuffer()).toString('hex'), data.toString('hex'));
  // seed on a declared volume: saved, read back, publishable
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const site = appSite(HELLO().config);
  site.config.volumes[0]!.seed = { kind: 'archive', blobSha256: sha, name: 'starter.tar.gz', size: data.length };
  const w = await admin.api.putDraftSite({ deploymentId: id, siteSlug: 'app', body: { ifRevision: 0, site } });
  assert.ok(!w.validation.issues.some((i) => i.severity === 'error'), JSON.stringify(w.validation.issues));
  const saved = (await admin.api.getDeployment({ deploymentId: id })).draft.sites.find((s) => s.siteSlug === 'app')!.config.volumes[0]!;
  assert.deepEqual(saved.seed, { kind: 'archive', blobSha256: sha, name: 'starter.tar.gz', size: data.length });
  assert.equal(saved.kind, 'dir');
  // a wrong seed (single-file seed on a folder volume) → 422 V_VOLUME_SEED; bad kind → 422 V_VOLUME_KIND
  const bad = structuredClone(site);
  // §21 #101: legacy File seeds are V_VOLUME_SEED_FILE_REF now; a single-file seed on a folder volume stays V_VOLUME_SEED
  bad.config.volumes[0]!.seed = { kind: 'single', sha256: sha, size: data.length, mode: '0644', render: 'none' };
  const be = await rejects(admin.api.putDraftSite({ deploymentId: id, siteSlug: 'app', body: { ifRevision: 1, site: bad } }), 'invalid', 422);
  assert.ok((be.details.errors as Doc[]).some((x) => x.code === 'V_VOLUME_SEED'), JSON.stringify(be.details));
  const bad2 = structuredClone(site);
  (bad2.config.volumes[0] as Doc).kind = 'folder';
  const be2 = await rejects(admin.api.putDraftSite({ deploymentId: id, siteSlug: 'app', body: { ifRevision: 1, site: bad2 } }), 'invalid', 422);
  assert.ok((be2.details.errors as Doc[]).some((x) => x.code === 'V_VOLUME_KIND'), JSON.stringify(be2.details));
  await publish(admin.api, id, { ifRevision: 1, summary: 'seeded volume' } );
  // the agent may fetch the archive (open proposal), not an unrelated blob
  const app = await enrollSite(admin, id, 'app');
  const ab = await app.api.getAgentBlob({ sha256: sha });
  assert.equal(Buffer.from(await ab.arrayBuffer()).toString('hex'), data.toString('hex'));
  const other = await uploadSeed(admin, gzipSync(Buffer.from(`other ${Date.now()}`)));
  await rejects(app.api.getAgentBlob({ sha256: other.sha }), 'forbidden');
  // the daily job never deletes content blobs referenced by volume seeds
  await runDaily();
  assert.equal((await abucket.file(`files/${sha.slice(7)}`).exists())[0], true);
});

test('volumes kind + seed: missing → dir / null on write and read', async () => {
  const admin = await person('admin');
  const { dep, templateId } = await helloDeployment(admin);
  const site = appSite(HELLO().config) as Doc;
  site.config.volumes = site.config.volumes.map(({ kind, seed, ...v }: Doc) => (void kind, void seed, v));
  await admin.api.putDraftSite({ deploymentId: dep.deploymentId, siteSlug: 'app', body: { ifRevision: 0, site: site as Site } });
  assertVolumesE(JSON.parse((await adb.collection('deployments').doc(dep.deploymentId).get()).data()!._draft).sites.find((s: Doc) => s.siteSlug === 'app').config);
  const tRef = adb.collection('templates').doc(templateId);
  const body = JSON.parse((await tRef.get()).data()!._body);
  body.sites = body.sites.map((s: Doc) => (s.siteSlug === 'app' ? site : s));
  await tRef.update({ _body: JSON.stringify(body) });
  assertVolumesE((await admin.api.getTemplate({ templateId })).sites.find((s) => s.siteSlug === 'app')!.config as unknown as Doc);
});
