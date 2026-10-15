/** Round D (BRIEF-D, SPEC §21 #58+): declared site volumes — normalize on write + read, validation codes, diff, migrate-d. */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { newSiteVolume, type Config, type Site } from '@opm/shared';
import { abucket, adb, HELLO, helloDeployment, person, rejects, publish } from './helpers.ts';
import { jcsHash } from '../functions/src/core/util.ts';
import { applyPlanD, planMigrationD } from '../scripts/migrate-d.ts';
import { verifySchema } from '../scripts/verify-schema.ts';

type Doc = Record<string, any>;
const appSite = (cfg: Config) => structuredClone(cfg.sites.find((s) => s.siteSlug === 'app')!) as Site;

/**
 * A round-C (pre-D) app site: no `volumes` list; hello-api mounts its image volume through the old `data` source with
 * readOnly true (the round-C editor bug); `implicit` = hello-api has NO row for its image volume (implicit data volume).
 */
function preDSite(s: Site, implicit = false): Doc {
  const { volumes, ...config } = s.config as Doc;
  void volumes;
  return {
    ...s,
    config: {
      ...config,
      containers: s.config.containers.map((c) =>
        c.containerSlug !== 'hello-api' ? c : { ...c, volumes: implicit ? [] : [{ imageVolume: 'data', mountPath: null, source: { kind: 'data', name: 'hello-api-data' }, readOnly: true }] },
      ),
    },
  };
}
const preDConfig = (cfg: Doc, implicit = false): Doc => ({ ...cfg, sites: (cfg.sites as Site[]).map((s) => (s.siteSlug === 'app' ? preDSite(s, implicit) : s)) });

function assertD(sc: Doc, label = 'hello-api-data') {
  assert.deepEqual(sc.volumes, [{ volumeSlug: 'hello-api-data', label, description: '', backup: true, location: { kind: 'auto' }, kind: 'dir', seed: null }]);
  const api = sc.containers.find((c: Doc) => c.containerSlug === 'hello-api');
  assert.deepEqual(api.volumes, [{ imageVolume: 'data', mountPath: null, source: { kind: 'volume', volumeSlug: 'hello-api-data' }, readOnly: false }]);
  const web = sc.containers.find((c: Doc) => c.containerSlug === 'hello-web');
  assert.deepEqual(web.volumes, [{ imageVolume: null, mountPath: '/etc/hello/banner.txt', source: { kind: 'file', fileSlug: 'banner' }, readOnly: true }]);
}

test('normalize on write: pre-D site (data source, read-only, implicit row) → declared volume, volume source, readOnly false (draft PUT + template PATCH)', async () => {
  const admin = await person('admin');
  const { dep, templateId } = await helloDeployment(admin);
  const id = dep.deploymentId;
  // draft PUT site: explicit data row with readOnly true
  const w = await admin.api.putDraftSite({ deploymentId: id, siteSlug: 'app', body: { ifRevision: 0, site: preDSite(appSite(HELLO().config)) as unknown as Site } });
  assert.equal(w.validation.ok, true);
  assert.ok(!w.validation.issues.some((i) => i.severity === 'error'));
  const raw0 = JSON.parse((await adb.collection('deployments').doc(id).get()).data()!._draft);
  assertD(raw0.sites.find((s: Doc) => s.siteSlug === 'app').config);
  // draft PUT sites section: implicit image volume (no row) → explicit row + declared
  const all = (await admin.api.getDeployment({ deploymentId: id })).draft.sites.map((s) => (s.siteSlug === 'app' ? preDSite(s, true) : s));
  const w2 = await admin.api.putDraftSection({ deploymentId: id, section: 'sites', body: { ifRevision: 1, items: all as unknown as Site[] } });
  assert.equal(w2.validation.ok, true);
  assertD((await admin.api.getDeployment({ deploymentId: id })).draft.sites.find((s) => s.siteSlug === 'app')!.config as unknown as Doc);
  // template PATCH
  const tpl = await admin.api.getTemplate({ templateId });
  const tw = await admin.api.patchTemplate({ templateId, body: { ifVersion: tpl.version, sites: tpl.sites.map((s) => (s.siteSlug === 'app' ? (preDSite(s, true) as unknown as Site) : s)) } });
  assertD(tw.template.sites.find((s) => s.siteSlug === 'app')!.config as unknown as Doc);
  // the db site was already D (declared redis-data "Redis data") → unchanged, nothing auto-declared
  assert.deepEqual(tw.template.sites.find((s) => s.siteSlug === 'db')!.config.volumes, [{ volumeSlug: 'redis-data', label: 'Redis data', description: '', backup: true, location: { kind: 'auto' }, kind: 'dir', seed: null }]);
});

test('normalize on read: a pre-D document written directly to Firestore is served declared + explicit (image volumes known)', async () => {
  const admin = await person('admin');
  const { templateId, dep } = await helloDeployment(admin);
  const tRef = adb.collection('templates').doc(templateId);
  await tRef.update({ _body: JSON.stringify(preDConfig(JSON.parse((await tRef.get()).data()!._body), true)) });
  const t = await admin.api.getTemplate({ templateId });
  assertD(t.sites.find((s) => s.siteSlug === 'app')!.config as unknown as Doc);
  // a PATCH that does not touch sites writes the normalized (explicit + declared) sites back — no silent unmount
  await admin.api.patchTemplate({ templateId, body: { ifVersion: t.version, description: 'touched' } });
  assertD(JSON.parse((await tRef.get()).data()!._body).sites.find((s: Doc) => s.siteSlug === 'app').config);
  // deployment draft
  const dRef = adb.collection('deployments').doc(dep.deploymentId);
  await dRef.update({ _draft: JSON.stringify(preDConfig(JSON.parse((await dRef.get()).data()!._draft))) });
  assertD((await admin.api.getDeployment({ deploymentId: dep.deploymentId })).draft.sites.find((s) => s.siteSlug === 'app')!.config as unknown as Doc);
});

test('validation: undeclared volume → 422 V_VOLUME_UNDECLARED; unused → V_VOLUME_UNUSED warning; not mounted → warning; file readOnly forced; diff section volumes', async () => {
  const admin = await person('admin');
  const { dep, templateId } = await helloDeployment(admin);
  const id = dep.deploymentId;
  // a current (D) config is never auto-declared
  const undeclared = appSite(HELLO().config);
  undeclared.config.volumes = [];
  const e = await rejects(admin.api.putDraftSite({ deploymentId: id, siteSlug: 'app', body: { ifRevision: 0, site: undeclared } }), 'invalid', 422);
  assert.ok((e.details.errors as Doc[]).some((x) => x.code === 'V_VOLUME_UNDECLARED' && /hello-api-data/.test(x.message)), JSON.stringify(e.details));
  const tpl = await admin.api.getTemplate({ templateId });
  const e2 = await rejects(admin.api.patchTemplate({ templateId, body: { ifVersion: tpl.version, sites: tpl.sites.map((s) => (s.siteSlug === 'app' ? undeclared : s)) } }), 'invalid', 422);
  assert.ok((e2.details.errors as Doc[]).some((x) => x.code === 'V_VOLUME_UNDECLARED'));
  // bad volume slug → V_FORMAT
  const badSlug = appSite(HELLO().config);
  badSlug.config.volumes.push(newSiteVolume('Bad Name'));
  const e3 = await rejects(admin.api.putDraftSite({ deploymentId: id, siteSlug: 'app', body: { ifRevision: 0, site: badSlug } }), 'invalid', 422);
  assert.ok((e3.details.errors as Doc[]).some((x) => x.code === 'V_FORMAT'));
  // publish the fixture, then: a declared but unused volume (warning), an image volume without row (warning), a file mount sent read-write (forced read-only)
  await publish(admin.api, id, { ifRevision: 0, summary: 'r1' } );
  const site = appSite(HELLO().config);
  site.config.volumes.push(newSiteVolume('spare', 'Spare', 'not used yet', false));
  site.config.containers.find((c) => c.containerSlug === 'hello-api')!.volumes = [];
  site.config.containers.find((c) => c.containerSlug === 'hello-web')!.volumes[0]!.readOnly = false;
  const w = await admin.api.putDraftSite({ deploymentId: id, siteSlug: 'app', body: { ifRevision: 0, site } });
  assert.equal(w.validation.ok, true);
  const codes = w.validation.issues.map((i) => `${i.severity}:${i.code}`);
  assert.ok(codes.includes('warning:V_VOLUME_UNUSED'), codes.join());
  assert.ok(codes.includes('warning:V_VOLUME_NOT_MOUNTED'), codes.join());
  const saved = (await admin.api.getDeployment({ deploymentId: id })).draft.sites.find((s) => s.siteSlug === 'app')!.config;
  assert.equal(saved.containers.find((c) => c.containerSlug === 'hello-web')!.volumes[0]!.readOnly, true, 'file mounts are always read-only');
  const diff = await admin.api.getDiff({ deploymentId: id });
  const vol = diff.changes.filter((c) => c.section === 'volumes');
  assert.ok(vol.some((c) => c.siteSlug === 'app' && /spare/.test(JSON.stringify(c)) && c.kind === 'added'), JSON.stringify(vol));
});

test('migrate-d: pre-D template / draft / snapshot / revision → dry run lists declared volumes + read-only resets, apply, rerun 0, verify-schema 0', async () => {
  const admin = await person('admin');
  const { dep, templateId } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const p = await publish(admin.api, id, { ifRevision: 0, summary: 'r1' } );
  const tRef = adb.collection('templates').doc(templateId);
  const t0 = (await tRef.get()).data()!;
  await tRef.update({ _body: JSON.stringify(preDConfig(JSON.parse(t0._body))) });
  const t0b = (await tRef.get()).data()!;
  const dRef = adb.collection('deployments').doc(id);
  const d0 = (await dRef.get()).data()!;
  await dRef.update({ _draft: JSON.stringify(preDConfig(JSON.parse(d0._draft), true)), _srcSnapshot: JSON.stringify(preDConfig(JSON.parse(d0._srcSnapshot))) });
  const rRef = dRef.collection('revisions').doc(p.revision.revId);
  const r0 = (await rRef.get()).data()!;
  const legacyRev = preDConfig(JSON.parse(r0._config));
  await rRef.update({ _config: JSON.stringify(legacyRev), hash: jcsHash(legacyRev) });
  const mine = [`templates/${templateId}`, `deployments/${id}`];
  const isMine = (l: string) => mine.some((m) => l.startsWith(m));
  // verify-schema before: missing volumes lists + data sources
  const v0 = (await verifySchema(adb)).filter(isMine);
  assert.ok(v0.some((l) => /volumes list missing/.test(l)), v0.join('\n'));
  assert.ok(v0.some((l) => /legacy source kind data/.test(l)), v0.join('\n'));
  assert.ok(v0.some((l) => l.startsWith(`deployments/${id}/revisions/`) && /volumes list missing/.test(l)), 'revisions checked');
  // dry run
  const plan = await planMigrationD(adb, abucket);
  const paths = plan.ops.map((o) => o.path);
  for (const pth of [`templates/${templateId}`, `deployments/${id}`, `deployments/${id}/revisions/${p.revision.revId}`]) assert.ok(paths.includes(pth), pth);
  const tdoc = plan.docs.find((d) => d.path.startsWith(`templates/${templateId}`))!;
  assert.ok(tdoc.lines.includes('site app: volumes declared [hello-api-data (backup on)]'), tdoc.lines.join('\n'));
  assert.ok(tdoc.lines.includes('site db: volumes declared [redis-data (backup on)]') === false, 'the D site db is untouched');
  assert.ok(tdoc.lines.some((l) => /container hello-api: image volume data: READ-ONLY RESET true → false/.test(l)), tdoc.lines.join('\n'));
  assert.ok(tdoc.lines.some((l) => /data:hello-api-data → volume:hello-api-data/.test(l)));
  const ddoc = plan.docs.find((d) => d.path.startsWith(`deployments/${id} `))!;
  assert.ok(ddoc.lines.some((l) => /^draft: site app: container hello-api: image volume data: implicit → volume:hello-api-data/.test(l)), ddoc.lines.join('\n'));
  assert.ok(ddoc.lines.some((l) => /^srcTemplate snapshot: site app: volumes declared/.test(l)));
  assert.ok(plan.counts.readOnlyResets! >= 3, JSON.stringify(plan.counts));
  assert.equal((await tRef.get()).data()!._body, t0b._body, 'dry run wrote nothing');
  // apply
  await applyPlanD(adb, plan);
  const t1 = (await tRef.get()).data()!;
  assert.equal(t1.version, t0.version, 'template version unchanged');
  assert.equal(t1.updatedAt, t0.updatedAt, 'template updatedAt unchanged');
  assertD(JSON.parse(t1._body).sites.find((s: Doc) => s.siteSlug === 'app').config);
  const d1 = (await dRef.get()).data()!;
  assertD(JSON.parse(d1._draft).sites.find((s: Doc) => s.siteSlug === 'app').config);
  assertD(JSON.parse(d1._srcSnapshot).sites.find((s: Doc) => s.siteSlug === 'app').config);
  const r1 = (await rRef.get()).data()!;
  const rc = JSON.parse(r1._config);
  assertD(rc.sites.find((s: Doc) => s.siteSlug === 'app').config);
  assert.equal(r1.hash, jcsHash(rc));
  assert.notEqual(r1.hash, jcsHash(legacyRev), 'hash recomputed');
  assert.equal(r1.catalogHash, r0.catalogHash, 'catalog unchanged');
  const audit = (await adb.collection('auditLog').where('action', '==', 'system.migration').get()).docs.map((x) => JSON.parse(x.data()._data)).filter((x) => x.migration === 'd');
  assert.ok(audit.some((a) => a.docs.some((d: Doc) => d.path.startsWith(`templates/${templateId}`)) && a.counts.volumesDeclared >= 1));
  // idempotent + verify-schema clean
  const again = await planMigrationD(adb, abucket);
  assert.deepEqual(again.ops.filter((o) => isMine(o.path)).map((o) => o.path), []);
  const v1 = (await verifySchema(adb)).filter(isMine);
  assert.deepEqual(v1, []);
  // a revision whose config is already D keeps its hash (only changed configs are rewritten)
  assert.equal((await rRef.get()).data()!.hash, r1.hash);
});

// ───────────────────────────────────────── part 2: storage locations (one copy per site)
test('locations: missing → auto on write + read; custom "./" + absolute kept; malformed / reserved / nested → 422 V_LOCATION', async () => {
  const admin = await person('admin');
  const { templateId } = await helloDeployment(admin);
  const tpl = await admin.api.getTemplate({ templateId });
  const strip = (x: Doc) => (({ location, ...r }) => (void location, r))(x);
  // missing locations on write → auto
  const files0 = tpl.files.map((f) => strip(f as unknown as Doc)) as unknown as typeof tpl.files;
  const sites0 = tpl.sites.map((s) => ({ ...s, config: { ...s.config, volumes: s.config.volumes.map((v) => strip(v as unknown as Doc)) } })) as unknown as typeof tpl.sites;
  const w = await admin.api.patchTemplate({ templateId, body: { ifVersion: tpl.version, files: files0, sites: sites0 } });
  assert.deepEqual(w.template.files.map((f) => f.location), [{ kind: 'auto' }]);
  assert.ok(w.template.sites.every((s) => s.config.volumes.every((v) => v.location.kind === 'auto')));
  const stored = JSON.parse((await adb.collection('templates').doc(templateId).get()).data()!._body);
  assert.deepEqual(stored.files[0].location, { kind: 'auto' }, 'stored with location');
  // custom locations are kept
  const files1 = w.template.files.map((f) => ({ ...f, location: { kind: 'path' as const, path: './config/banner.txt' } }));
  const sites1 = w.template.sites.map((s) => (s.siteSlug !== 'app' ? s : { ...s, config: { ...s.config, volumes: s.config.volumes.map((v) => ({ ...v, location: { kind: 'path' as const, path: '/srv/hello/api-data' } })) } }));
  const w1 = await admin.api.patchTemplate({ templateId, body: { ifVersion: w.template.version, files: files1, sites: sites1 } });
  assert.deepEqual(w1.template.files[0]!.location, { kind: 'path', path: './config/banner.txt' });
  assert.deepEqual(w1.template.sites.find((s) => s.siteSlug === 'app')!.config.volumes[0]!.location, { kind: 'path', path: '/srv/hello/api-data' });
  const v = w1.template.version;
  const code = async (files: unknown, sites: unknown, expect: RegExp) => {
    const e = await rejects(admin.api.patchTemplate({ templateId, body: { ifVersion: v, ...(files ? { files: files as never } : {}), ...(sites ? { sites: sites as never } : {}) } }), 'invalid', 422);
    assert.ok((e.details.errors as Doc[]).some((x) => x.code === 'V_LOCATION' && expect.test(`${x.path} ${x.message}`)), JSON.stringify(e.details));
  };
  await code([{ ...files1[0], location: { kind: 'bogus' } }], null, /files\[banner\]\.location/);
  await code(null, sites1.map((s) => (s.siteSlug !== 'app' ? s : { ...s, config: { ...s.config, volumes: s.config.volumes.map((x) => ({ ...x, location: { kind: 'path' } })) } })), /volumes\[hello-api-data\]\.location/);
  await code([{ ...files1[0], location: { kind: 'path', path: './blobs/banner' } }], null, /belongs to the agent/);
  await code([{ ...files1[0], location: { kind: 'path', path: './a/../b' } }], null, /\.\./);
  await code([{ ...files1[0], location: { kind: 'path', path: '/srv/hello/api-data/banner.txt' } }], null, /same place or nested/);
  // read: a stored document without locations is served with auto
  const body = JSON.parse((await adb.collection('templates').doc(templateId).get()).data()!._body);
  body.files = body.files.map(strip);
  body.sites = body.sites.map((s: Doc) => ({ ...s, config: { ...s.config, volumes: s.config.volumes.map(strip) } }));
  await adb.collection('templates').doc(templateId).update({ _body: JSON.stringify(body) });
  const r = await admin.api.getTemplate({ templateId });
  assert.deepEqual(r.files[0]!.location, { kind: 'auto' });
  assert.ok(r.sites.every((s) => s.config.volumes.every((x) => x.location.kind === 'auto')));
});

test('migrate-d part 2: files + volumes get location auto; a file using container.* is reported (not rewritten); rerun 0; verify-schema 0', async () => {
  const admin = await person('admin');
  const { dep, templateId } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const p = await publish(admin.api, id, { ifRevision: 0, summary: 'r1' } );
  const perContainer = await admin.api.uploadBlob({ body: new TextEncoder().encode('env={{ container.env.NODE_ENV }}\nname={{ file.banner.path }}\n') });
  const strip = (x: Doc) => (({ location, ...r }) => (void location, r))(x);
  const noLoc = (cfg: Doc, extra = false): Doc => ({
    ...cfg,
    files: [...(cfg.files as Doc[]).map(strip), ...(extra ? [{ fileSlug: 'per-container', label: 'Per container', description: '', mime: 'text/plain', editor: 'text', maxBytes: 4096, content: perContainer, render: 'expressions' }] : [])],
    sites: (cfg.sites as Doc[]).map((s) => ({ ...s, config: { ...s.config, volumes: s.config.volumes.map(strip) } })),
  });
  const tRef = adb.collection('templates').doc(templateId);
  const t0 = (await tRef.get()).data()!;
  await tRef.update({ _body: JSON.stringify(noLoc(JSON.parse(t0._body), true)) });
  const dRef = adb.collection('deployments').doc(id);
  const d0 = (await dRef.get()).data()!;
  await dRef.update({ _draft: JSON.stringify(noLoc(JSON.parse(d0._draft))), _srcSnapshot: JSON.stringify(noLoc(JSON.parse(d0._srcSnapshot))) });
  const rRef = dRef.collection('revisions').doc(p.revision.revId);
  const r0 = (await rRef.get()).data()!;
  const oldRev = noLoc(JSON.parse(r0._config));
  await rRef.update({ _config: JSON.stringify(oldRev), hash: jcsHash(oldRev) });
  const mine = [`templates/${templateId}`, `deployments/${id}`];
  const isMine = (l: string) => mine.some((m) => l.startsWith(m));
  const v0 = (await verifySchema(adb)).filter(isMine);
  assert.ok(v0.some((l) => /file banner: location missing/.test(l)), v0.join('\n'));
  assert.ok(v0.some((l) => /volume hello-api-data: location missing/.test(l)), v0.join('\n'));
  assert.ok(v0.some((l) => l.startsWith(`deployments/${id}/revisions/`) && /location missing/.test(l)), 'revisions checked');
  const plan = await planMigrationD(adb, abucket);
  const tdoc = plan.docs.find((d) => d.path.startsWith(`templates/${templateId}`))!;
  assert.ok(tdoc.lines.includes('file banner: location auto (auto · <agentHome>/files/banner)'), tdoc.lines.join('\n'));
  assert.ok(tdoc.lines.includes('site app: volume hello-api-data: location auto (auto · <agentHome>/volumes/hello-api-data)'), tdoc.lines.join('\n'));
  assert.ok(tdoc.lines.some((l) => /^file per-container: content 1:\d+ E_NOT_ALLOWED .*container/.test(l)), tdoc.lines.join('\n'));
  assert.ok(tdoc.lines.some((l) => /^file per-container: content 2:\d+ E_NOT_ALLOWED .*file/.test(l)), tdoc.lines.join('\n'));
  assert.ok(plan.warnings.some((w) => w.includes(`templates/${templateId}`) && /per-container/.test(w)));
  await applyPlanD(adb, plan);
  const b1 = JSON.parse((await tRef.get()).data()!._body);
  assert.deepEqual(b1.files.map((f: Doc) => [f.fileSlug, f.location, f.render]), [['banner', { kind: 'auto' }, 'expressions'], ['per-container', { kind: 'auto' }, 'expressions']], 'content + render not rewritten');
  assert.equal(b1.files[1].content.sha256, perContainer.sha256);
  assert.equal((await tRef.get()).data()!.version, t0.version);
  const r1 = (await rRef.get()).data()!;
  assert.equal(r1.hash, jcsHash(JSON.parse(r1._config)));
  assert.deepEqual(JSON.parse(r1._config).files[0].location, { kind: 'auto' });
  const again = await planMigrationD(adb, abucket);
  assert.deepEqual(again.ops.filter((o) => isMine(o.path)).map((o) => o.path), []);
  assert.ok(again.warnings.some((w) => /per-container/.test(w)), 'the content report repeats on a rerun');
  assert.deepEqual((await verifySchema(adb)).filter(isMine), []);
});
