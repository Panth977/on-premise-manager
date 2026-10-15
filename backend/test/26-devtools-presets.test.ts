/**
 * SPEC-final §7.6 + §21 #97 — dev-tool presets in SiteConfig.devTools (templates + deployment drafts, ride in the
 * revision): validation on save (422 V_DEVTOOL_*), normalized stored shape, devTools OMITTED when empty (hash-compatible
 * with agents < 0.11), diff section devTools, publish, Template sync, agent devtools.* activities, verify-schema.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { activityHashInput, canonicalize, newDevToolPreset, type Activity, type Config, type DevToolPreset, type Site } from '@opm/shared';
import { adb, enrollSite, helloDeployment, person, rejects, sha256Hex, type Person, publish } from './helpers.ts';
import { verifySchema } from '../scripts/verify-schema.ts';
import { jcsHash } from '../functions/src/core/util.ts';

type Doc = Record<string, any>;
const clone = <T>(v: T): T => JSON.parse(JSON.stringify(v)) as T;
const siteOf = (c: Pick<Config, 'sites'>, s: string) => c.sites.find((x) => x.siteSlug === s)!;

function redis(slug = 'cache', name = 'Cache'): DevToolPreset {
  const p = newDevToolPreset('redis');
  p.presetSlug = slug;
  p.name = name;
  p.target = { kind: 'container', containerSlug: 'redis', portName: 'tcp' };
  p.auth.password = { kind: 'expr', expr: '{{ secret.api-token }}' };
  return p;
}
function mqtt(slug = 'broker'): DevToolPreset {
  const p = newDevToolPreset('mqtt');
  p.presetSlug = slug;
  p.target = { kind: 'host', host: 'broker.local', port: 1883 };
  return p;
}
async function putSite(admin: Person, id: string, slug: string, edit: (s: Site) => void) {
  const d = await admin.api.getDeployment({ deploymentId: id });
  const s = clone(siteOf(d.draft, slug)) as Site;
  edit(s);
  return admin.api.putDraftSite({ deploymentId: id, siteSlug: slug, body: { ifRevision: d.draftVersion, site: s } });
}
async function patchTpl(admin: Person, templateId: string, edit: (c: Config) => void) {
  const t = await admin.api.getTemplate({ templateId });
  const c: Config = clone({ variables: t.variables, secrets: t.secrets, files: t.files, sites: t.sites });
  edit(c);
  return (await admin.api.patchTemplate({ templateId, body: { ifVersion: t.version, ...c } })).template;
}
const rawDraft = async (id: string) => JSON.parse((await adb.collection('deployments').doc(id).get()).data()!._draft) as Config;
const errCodes = (e: { details: Doc }) => (e.details.errors as { code: string }[]).map((x) => x.code);

test('draft PUT: presets validated (422 V_DEVTOOL_*), normalized, [] omitted; diff section; publish carries them + hash', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  // the hello draft has no presets: no devTools key anywhere
  for (const s of (await rawDraft(id)).sites) assert.ok(!('devTools' in s.config), s.siteSlug);

  // invalid presets → 422 with the codes, nothing stored
  const v0 = (await admin.api.getDeployment({ deploymentId: id })).draftVersion;
  const bad = await rejects(
    putSite(admin, id, 'db', (s) => {
      const lit = redis('lit');
      lit.auth.password = { kind: 'literal', value: '' }; // §21 #103: a static value is allowed; an EMPTY one is not
      const port = redis('port');
      port.target = { kind: 'container', containerSlug: 'redis', portName: 'http' };
      const gone = redis('gone');
      gone.target = { kind: 'container', containerSlug: 'nope', portName: 'tcp' };
      const sec = redis('sec');
      sec.auth.password = { kind: 'expr', expr: '{{ secret.missing }}' };
      const opt = redis('Bad_Slug');
      opt.options.timeoutSec = 0;
      s.config.devTools = [lit, port, gone, sec, opt, redis('lit')];
    }),
    'invalid',
    422,
  );
  for (const code of ['V_DEVTOOL_AUTH', 'V_DEVTOOL_PORT', 'V_DEVTOOL_CONTAINER', 'V_DEVTOOL_SECRET', 'V_DEVTOOL_SLUG', 'V_DEVTOOL_OPTION', 'V_DEVTOOL_DUP']) assert.ok(errCodes(bad).includes(code), `${code}: ${JSON.stringify(bad.details)}`);
  assert.equal((await admin.api.getDeployment({ deploymentId: id })).draftVersion, v0, 'nothing stored');
  // the sites section PUT validates too
  const d0 = await admin.api.getDeployment({ deploymentId: id });
  const sitesBad = clone(d0.draft.sites) as Site[];
  const pg = newDevToolPreset('postgres');
  pg.target = { kind: 'host', host: 'pg', port: 5432 };
  pg.auth.username = null;
  siteOf({ sites: sitesBad }, 'app').config.devTools = [pg];
  const sb = await rejects(admin.api.putDraftSection({ deploymentId: id, section: 'sites', body: { ifRevision: d0.draftVersion, items: sitesBad } }), 'invalid', 422);
  assert.ok(errCodes(sb).includes('V_DEVTOOL_AUTH'), JSON.stringify(sb.details));

  // valid preset (missing optional keys + an unknown key) → stored in the normalized shape
  await putSite(admin, id, 'db', (s) => {
    const p = redis() as unknown as Doc;
    delete p.options;
    p.junk = 1;
    s.config.devTools = [p as unknown as DevToolPreset];
  });
  const stored = siteOf(await rawDraft(id), 'db').config.devTools!;
  assert.equal(stored.length, 1);
  assert.ok(!('junk' in stored[0]!));
  assert.deepEqual(stored[0]!.options, newDevToolPreset('redis').options);
  assert.deepEqual(stored[0]!.auth.password, { kind: 'expr', expr: '{{ secret.api-token }}' });
  const view = await admin.api.getDeployment({ deploymentId: id });
  assert.deepEqual(siteOf(view.draft, 'db').config.devTools, stored);
  // an empty list on another site is never written
  await putSite(admin, id, 'app', (s) => (s.config.devTools = []));
  assert.ok(!('devTools' in siteOf(await rawDraft(id), 'app').config));

  // diff: section devTools
  const diff = await admin.api.getDiff({ deploymentId: id });
  const dt = diff.changes.filter((c) => c.section === 'devTools');
  assert.deepEqual(dt.map((c) => `${c.kind}:${c.path}`), ['added:db/devtools/cache']);
  assert.equal(diff.publishable, true, JSON.stringify(diff.blockers));

  // publish → the revision carries the presets; hash = sha256(JCS(config)); app has no devTools key
  const p = await publish(admin.api, id, { ifRevision: (await admin.api.getDeployment({ deploymentId: id })).draftVersion, summary: 'presets' });
  const rv = await admin.api.getRevision({ deploymentId: id, revId: p.revision.revId });
  assert.deepEqual(siteOf(rv.config, 'db').config.devTools, stored);
  const rawRev = (await adb.collection('deployments').doc(id).collection('revisions').doc(p.revision.revId).get()).data()!;
  const revCfg = JSON.parse(rawRev._config) as Config;
  assert.equal(rawRev.hash, jcsHash(revCfg));
  assert.equal(p.revision.hash, jcsHash(revCfg));
  assert.ok(!('devTools' in siteOf(revCfg, 'app').config));
  // without the db presets the config hashes exactly like a config that never had the key (agents < 0.11)
  const noPresets = clone(revCfg);
  delete siteOf(noPresets, 'db').config.devTools;
  assert.notEqual(jcsHash(noPresets), rawRev.hash);
  assert.ok(!canonicalize(noPresets).includes('devTools'));

  // removing the last preset → key absent again; diff shows removed relative to head
  await adb.collection('deployments').doc(id).update({ head: { revId: p.revision.revId, seq: p.revision.seq, hash: p.revision.hash, reportedAt: new Date().toISOString(), reportedBy: 'db' } });
  await putSite(admin, id, 'db', (s) => delete s.config.devTools);
  assert.ok(!('devTools' in siteOf(await rawDraft(id), 'db').config));
  const diff2 = await admin.api.getDiff({ deploymentId: id });
  assert.deepEqual(diff2.changes.filter((c) => c.section === 'devTools').map((c) => `${c.kind}:${c.path}`), ['removed:db/devtools/cache']);
  // a deployment without presets publishes a revision without any devTools key
  const p2 = await publish(admin.api, id, { ifRevision: (await admin.api.getDeployment({ deploymentId: id })).draftVersion, summary: 'no presets' });
  const rev2 = JSON.parse((await adb.collection('deployments').doc(id).collection('revisions').doc(p2.revision.revId).get()).data()!._config);
  assert.ok(!JSON.stringify(rev2).includes('devTools'));

  // verify-schema: these documents are clean
  const mine = (l: string) => l.startsWith(`deployments/${id}`);
  assert.deepEqual((await verifySchema(adb)).filter(mine), []);
});

test('templates: presets saved + validated in template mode; [] omitted; Template sync plan / apply incl. a conflict', async () => {
  const admin = await person('admin');
  const { dep, templateId } = await helloDeployment(admin);
  const id = dep.deploymentId;
  // template save validates (template mode: still an error — presets are save level)
  const t0 = await admin.api.getTemplate({ templateId });
  const tc = clone({ variables: t0.variables, secrets: t0.secrets, files: t0.files, sites: t0.sites }) as Config;
  const lit = redis();
  lit.auth.password = { kind: 'expr', expr: '{{ secret.api-token | mosquitto_passwd(9) }}' }; // bad filter argument
  siteOf(tc, 'db').config.devTools = [lit];
  const e = await rejects(admin.api.patchTemplate({ templateId, body: { ifVersion: t0.version, ...tc } }), 'invalid', 422);
  assert.ok(errCodes(e).includes('E_SYNTAX'), JSON.stringify(e.details));
  // [] → omitted in the template body
  await patchTpl(admin, templateId, (c) => (siteOf(c, 'app').config.devTools = []));
  const body0 = JSON.parse((await adb.collection('templates').doc(templateId).get()).data()!._body) as Config;
  assert.ok(!('devTools' in siteOf(body0, 'app').config));

  // the template adds "cache" (Template cache) + "broker"; the draft adds its own "cache" → conflict, broker → theirs
  const tpl = await patchTpl(admin, templateId, (c) => (siteOf(c, 'db').config.devTools = [redis('cache', 'Template cache'), mqtt()]));
  assert.equal(siteOf(tpl, 'db').config.devTools!.length, 2);
  const draftBefore = await rawDraft(id);
  assert.ok(!('devTools' in siteOf(draftBefore, 'db').config), 'detached: the template edit does not reach the draft');
  await putSite(admin, id, 'db', (s) => (s.config.devTools = [redis('cache', 'My cache')]));
  // §21 #104: Template sync → commit both sides, compare template → deployment, cherry-pick
  const tplRef = { kind: 'template' as const, templateId };
  const depRef = { kind: 'deployment' as const, deploymentId: id };
  await admin.api.commitTemplate({ templateId, body: { message: 'presets', ifVersion: (await admin.api.getTemplate({ templateId })).version } });
  await admin.api.commitDeployment({ deploymentId: id, body: { message: 'my cache', ifRevision: (await admin.api.getDeployment({ deploymentId: id })).draftVersion } });
  const plan = await admin.api.compareBranches({ body: { source: { branch: tplRef, at: 'head' }, target: depRef } });
  const k = Object.fromEntries(plan.items.filter((c) => c.section === 'devTools').map((c) => [c.key, c.kind]));
  assert.deepEqual(k, { 'sites/db/devtools/cache': 'conflict', 'sites/db/devtools/broker': 'theirs' });
  const base = { source: { branch: tplRef, at: 'head' as const }, target: depRef, targetHead: plan.target.head!.commitId, sourceCommit: plan.source.commit!.commitId, message: 'presets from the template' };
  const un = await rejects(admin.api.merge({ body: { ...base, choices: [{ key: 'sites/db/devtools/broker', take: 'source' }] } }), 'invalid', 422);
  assert.match(JSON.stringify(un.details), /V_MERGE_UNRESOLVED/);
  // a preset cannot take a custom value (not a scalar)
  const cu = await rejects(admin.api.merge({ body: { ...base, choices: [{ key: 'sites/db/devtools/broker', take: 'source' }, { key: 'sites/db/devtools/cache', take: 'custom', value: 'x' }] } }), 'invalid', 422);
  assert.match(JSON.stringify(cu.details), /V_MERGE_CHOICE/);
  const choices = [{ key: 'sites/db/devtools/broker', take: 'source' as const }, { key: 'sites/db/devtools/cache', take: 'source' as const }];
  const pv = await admin.api.previewMerge({ body: { ...base, choices } });
  assert.deepEqual(pv.changes.filter((c) => c.section === 'devTools').map((c) => `${c.kind}:${c.path}`).sort(), ['added:db/devtools/broker', 'changed:db/devtools/cache']);
  await admin.api.merge({ body: { ...base, choices } });
  const after = siteOf(await rawDraft(id), 'db').config.devTools!;
  assert.deepEqual(after.map((p) => `${p.presetSlug}:${p.name}`), ['cache:Template cache', 'broker:MQTT']);
  // the template removes every preset; merging it leaves the key ABSENT (never devTools: [])
  await patchTpl(admin, templateId, (c) => delete siteOf(c, 'db').config.devTools);
  await admin.api.commitTemplate({ templateId, body: { message: 'no presets', ifVersion: (await admin.api.getTemplate({ templateId })).version } });
  const plan2 = await admin.api.compareBranches({ body: { source: { branch: tplRef, at: 'head' }, target: depRef } });
  const ch2 = plan2.items.filter((c) => c.section === 'devTools');
  assert.deepEqual(ch2.map((c) => `${c.key}:${c.kind}:${c.effect}`).sort(), ['sites/db/devtools/broker:theirs:remove', 'sites/db/devtools/cache:theirs:remove']);
  await admin.api.merge({ body: { source: { branch: tplRef, at: 'head' }, target: depRef, targetHead: plan2.target.head!.commitId, sourceCommit: plan2.source.commit!.commitId, message: 'drop presets', choices: ch2.map((c) => ({ key: c.key, take: 'source' as const })) } });
  assert.ok(!('devTools' in siteOf(await rawDraft(id), 'db').config));
  const mine = (l: string) => l.startsWith(`deployments/${id}`) || l.startsWith(`templates/${templateId}`);
  assert.deepEqual((await verifySchema(adb)).filter(mine), []);
  // verify-schema reports a stored devTools: [] and a preset that is not in the stored shape
  const tRef = adb.collection('templates').doc(templateId);
  const body = JSON.parse((await tRef.get()).data()!._body) as Doc;
  body.sites.find((s: Doc) => s.siteSlug === 'app').config.devTools = [];
  body.sites.find((s: Doc) => s.siteSlug === 'db').config.devTools = [{ ...redis(), junk: 1 }];
  await tRef.update({ _body: JSON.stringify(body) });
  const v = (await verifySchema(adb)).filter((l) => l.startsWith(`templates/${templateId}`));
  assert.ok(v.some((l) => /site app: devTools is \[\]/.test(l)), v.join('\n'));
  assert.ok(v.some((l) => /site db dev tool cache: not in the stored shape/.test(l)), v.join('\n'));
});

function makeActivity(deploymentId: string, siteSlug: string, seq: number, prevHash: string | null, type: string, data: Doc): Activity {
  const base = { activityId: `${siteSlug}:${seq}`, deploymentId, siteSlug, seq, ts: new Date(Date.now() + seq).toISOString(), actor: 'ops@example.com', origin: 'agent' as const, type, summary: `${type} ${seq}`, data, prevHash };
  const hash = 'sha256:' + sha256Hex(activityHashInput(base));
  return { ...base, hash, uploadedVia: null, uploadedAt: null };
}

test('agent activities: every devtools.* type is accepted by POST /v1/agent/activities (the pipe never breaks)', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const app = await enrollSite(admin, id, 'app');
  const types = ['devtools.query', 'devtools.query-cancelled', 'devtools.connection-created', 'devtools.connection-updated', 'devtools.connection-deleted', 'devtools.connection-tested', 'devtools.credential-revealed', 'devtools.mqtt-published', 'devtools.mqtt-subscribed', 'devtools.mqtt-unsubscribed', 'devtools.action-forwarded'];
  const query = { connectionId: 'p:cache', connectionName: 'Cache', kind: 'redis', source: 'preset', target: 'redis:tcp', op: 'redis', statement: 'SET k ••••••', statementTruncated: false, durationMs: 3, rows: null, rowsAffected: null, status: null, ok: true, error: null, readOnly: false, destructive: false, confirmed: false, queryId: 'q1' };
  const entries: Activity[] = [];
  let prev: string | null = null;
  types.forEach((t, i) => {
    const a = makeActivity(id, 'app', i + 1, prev, t, t === 'devtools.query' ? query : { connectionId: 'c_01J', n: i, nested: [[1], { a: null }] });
    entries.push(a);
    prev = a.hash;
  });
  const r = await app.api.uploadAgentActivities({ body: { siteSlug: 'app', entries } });
  assert.deepEqual([r.ackSeq, r.inserted, r.rejected], [types.length, types.length, 0]);
  const list = await admin.api.listActivities({ deploymentId: id, query: { site: 'app', type: 'devtools.' } });
  assert.equal(list.items.length, types.length);
  const q = (await admin.api.listActivities({ deploymentId: id, query: { type: 'devtools.query' } })).items;
  assert.ok(q.some((a) => a.type === 'devtools.query' && canonicalize(a.data) === canonicalize(query)), JSON.stringify(q));
});

test('§21 #99: presets may read another container env ({{ containers.<c>.env.<NAME> }}); unknown name → 422 V_DEVTOOL_ENV; plain env password → warning only', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  // app: hello-api env API_TOKEN (secretRef api-token), GREETING (plain)
  const http = (slug: string, user: string, pw: string): DevToolPreset => {
    const p = newDevToolPreset('http');
    p.presetSlug = slug;
    p.target = { kind: 'container', containerSlug: 'hello-api', portName: 'http' };
    p.auth.httpScheme = 'basic';
    p.auth.username = user;
    p.auth.password = { kind: 'expr', expr: pw };
    return p;
  };
  const bad = await rejects(putSite(admin, id, 'app', (s) => (s.config.devTools = [http('api', '{{ containers.hello-api.env.GREETING }}', '{{ containers.hello-api.env.NOPE }}')])), 'invalid', 422);
  assert.ok(errCodes(bad).includes('V_DEVTOOL_ENV'), JSON.stringify(bad.details));
  // a container env reference in the env of a CONTAINER is not allowed (field env) → E_NOT_ALLOWED
  const bad2 = await rejects(
    putSite(admin, id, 'app', (s) => {
      s.config.containers.find((c) => c.containerSlug === 'hello-web')!.env.push({ name: 'X', value: '{{ containers.hello-api.env.GREETING }}', secretRef: null });
    }),
    'invalid',
    422,
  );
  assert.ok(errCodes(bad2).includes('E_NOT_ALLOWED'), JSON.stringify(bad2.details));
  // valid: secret env as password; plain env as password is only a warning (stored)
  await putSite(admin, id, 'app', (s) => (s.config.devTools = [http('api', '{{ containers.hello-api.env.GREETING }}', '{{ containers.hello-api.env.API_TOKEN }}'), http('plain', 'u', '{{ containers.hello-api.env.GREETING }}')]));
  const stored = siteOf(await rawDraft(id), 'app').config.devTools!;
  assert.deepEqual(stored.map((p) => p.auth.password), [
    { kind: 'expr', expr: '{{ containers.hello-api.env.API_TOKEN }}' },
    { kind: 'expr', expr: '{{ containers.hello-api.env.GREETING }}' },
  ]);
});

test('§21 #103: a preset password may be a static Value (stored as a literal, warning only); the Diff never shows it; hash filters in files + presets', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const b = mqtt('broker');
  b.auth.username = 'northwind';
  b.auth.password = { kind: 'literal', value: 'Static-Value-103' };
  const h = mqtt('hashed');
  h.auth.password = { kind: 'expr', expr: '{{ secret.api-token | sha256 }}' };
  await putSite(admin, id, 'db', (s) => (s.config.devTools = [b, h]));
  const stored = siteOf(await rawDraft(id), 'db').config.devTools!;
  assert.deepEqual(stored[0]!.auth.password, { kind: 'literal', value: 'Static-Value-103' }, 'stored in the config as a literal');
  assert.deepEqual(stored[1]!.auth.password, { kind: 'expr', expr: '{{ secret.api-token | sha256 }}' });
  const diff = await admin.api.getDiff({ deploymentId: id });
  assert.ok(!JSON.stringify(diff).includes('Static-Value-103'), 'Diff never shows the static value');
  assert.ok(diff.changes.some((c) => c.path === 'db/devtools/broker'));
  assert.equal(diff.publishable, true, JSON.stringify(diff.blockers));
  // change only the value → one changed row with the hidden-value note
  await putSite(admin, id, 'db', (s) => {
    s.config.devTools![0]!.auth.password = { kind: 'literal', value: 'Other-Value-103' };
  });
  // the user's Mosquitto password file shape is accepted in a container env too (field env allows secrets)
  await putSite(admin, id, 'app', (s) => {
    s.config.containers.find((c) => c.containerSlug === 'hello-api')!.env.push({ name: 'MQTT_LINE', value: 'northwind:{{ secret.api-token | mosquitto_passwd }}', secretRef: null });
  });
  // …but not in an edge field (password-hash filters only where secrets are allowed)
  const bad = await rejects(
    putSite(admin, id, 'app', (s) => {
      s.config.containers.find((c) => c.containerSlug === 'hello-web')!.env.push({ name: 'BAD', value: '{{ var.greeting | mosquitto_passwd(5) }}', secretRef: null });
    }),
    'invalid',
    422,
  );
  assert.ok(errCodes(bad).includes('E_SYNTAX'), JSON.stringify(bad.details));
  const audit = (await adb.collection('auditLog').get()).docs.map((d) => JSON.stringify(d.data())).join('\n');
  assert.ok(!audit.includes('Static-Value-103') && !audit.includes('Other-Value-103'), 'audit log redacts the static value');
  const p = await publish(admin.api, id, { ifRevision: (await admin.api.getDeployment({ deploymentId: id })).draftVersion, summary: 'static value' });
  const rv = await admin.api.getRevision({ deploymentId: id, revId: p.revision.revId });
  assert.deepEqual(siteOf(rv.config, 'db').config.devTools![0]!.auth.password, { kind: 'literal', value: 'Other-Value-103' }, 'the revision carries it to the site');
  // verify-schema accepts a static value
  const mine = (await verifySchema(adb)).filter((l) => l.includes(id));
  assert.deepEqual(mine, []);
});
