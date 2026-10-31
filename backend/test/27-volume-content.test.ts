/**
 * §21 #101 / #117 volume content + persist-to-cloud volumes: the removed cloud-copy writes (410), persist content
 * committed as the seed tree (rules), agent blob check / put for any site, old-agent snapshots readable, publish with
 * tree seeds + blobContents render errors, diff per-file, daily prune, migrate-h (dry run, apply, revisions untouched,
 * idempotent, verify-schema, audit).
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { ulid, volumeTreeHash, type Site, type ValidationIssue, type VolumeContentVersion, type VolumeTree } from '@opm/shared';
import { adb, enrollSite, helloDeployment, person, rejects, sha256Hex, uniq, type Person, publish } from './helpers.ts';
import { runDaily } from '../src/jobs/daily.ts';
import { applyPlanH, planMigrationH, printPlan } from '../scripts/migrate-h.ts';
import { verifySchema } from '../scripts/verify-schema.ts';

type Doc = Record<string, any>;
const MiB = 1024 * 1024;
const PERSIST = { exclude: ['node_modules/', '*.log'], maxBytes: 50 * MiB, auto: true };
const enc = (s: string) => new TextEncoder().encode(s);
const shaOf = (s: string) => `sha256:${sha256Hex(s)}`;
const entry = (path: string, text: string, render: 'none' | 'expressions' = 'none', mode = '0644') => ({ path, sha256: shaOf(text), size: enc(text).length, mode, render });
const treeHashOf = (t: VolumeTree) => volumeTreeHash(t, (s) => sha256Hex(s));
const codes = (e: { details: Record<string, unknown> }) => ((e.details.errors as ValidationIssue[]) ?? []).map((x) => x.code);

async function appSite(admin: Person, id: string): Promise<{ site: Site; dv: number }> {
  const d = await admin.api.getDeployment({ deploymentId: id });
  return { site: structuredClone(d.draft.sites.find((s) => s.siteSlug === 'app')!) as Site, dv: d.draftVersion };
}
/** publish the draft and make the app site report it as its head (agent reportHead) */
async function publishAndReport(admin: Person, id: string, app: Awaited<ReturnType<typeof enrollSite>>, summary: string) {
  const dv = (await admin.api.getDeployment({ deploymentId: id })).draftVersion;
  const p = await publish(admin.api, id, { ifRevision: dv, summary } );
  const r = (await app.api.getAgentRevision({ revId: p.revision.revId })).revision;
  await app.api.reportHead({ body: { revision: r } });
  return r;
}

test('§21 #117 persist content is committed: removed Send / edit endpoints 410, commit rules, agent blob upload for any site, old-agent snapshots readable', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const one = { deploymentId: id, siteSlug: 'app', volumeSlug: 'hello-api-data' };

  // not persisted yet → 404; persist on in the draft → listed
  await rejects(admin.api.getVolumeContent(one), 'not-found', 404);
  const { site, dv } = await appSite(admin, id);
  site.config.volumes[0]!.persist = PERSIST;
  await admin.api.putDraftSite({ deploymentId: id, siteSlug: 'app', body: { ifRevision: dv, site } });
  const list = await admin.api.listVolumeContent({ deploymentId: id });
  assert.equal(list.items.length, 1);
  assert.equal(list.items[0]!.versionCount, 0);

  const A = `[{"id":"flow-${uniq()}"}]`;
  const B = `module.exports = ${Date.now()};`;
  for (const t of [A, B]) await admin.api.uploadBlob({ body: enc(t) });
  const t1: VolumeTree = { entries: [entry('flows.json', A)], dirs: ['lib'] };

  // the cloud copy is no editing surface any more: every write answers 410 gone
  await rejects(admin.api.createVolumeContentVersion({ ...one, body: { baseVersionId: null, tree: t1, note: '' } }), 'gone', 410);
  await rejects(admin.api.restoreVolumeContentVersion({ ...one, versionId: ulid(), body: { note: '' } }), 'gone', 410);
  await rejects(admin.api.requestVolumeSnapshot(one), 'gone', 410);
  await rejects(admin.api.sendVolumeContent({ ...one, body: { versionId: ulid(), restart: false, note: '' } }), 'gone', 410);
  await rejects(admin.api.cancelVolumeContentPush(one), 'gone', 410);

  // the content is the persist volume's seed tree, committed with the deployment: literal, nothing excluded
  const withSeed = async (tree: VolumeTree) => {
    const { site: s2, dv: dv2 } = await appSite(admin, id);
    s2.config.volumes[0]!.seed = { kind: 'tree', ...tree };
    await admin.api.putDraftSite({ deploymentId: id, siteSlug: 'app', body: { ifRevision: dv2, site: s2 } });
    return (await admin.api.getDeployment({ deploymentId: id })).draftVersion;
  };
  // (the test client commits a draft save: the commit refuses it)
  const rend = await rejects(withSeed({ entries: [entry('a.txt', A, 'expressions')], dirs: [] }), 'invalid', 422);
  assert.ok(codes(rend).includes('V_VOLUME_SEED'), JSON.stringify(rend.details));
  const exc = await rejects(withSeed({ entries: [entry('debug.log', A), entry('flows.json', B)], dirs: [] }), 'invalid', 422);
  assert.deepEqual(codes(exc), ['V_VOLUME_CONTENT_EXCLUDED']);
  const ok = await publish(admin.api, id, { ifRevision: await withSeed(t1), summary: 'persist content' });
  assert.ok(ok.revision.revId);
  const headSeed = (await admin.api.getDeployment({ deploymentId: id })).draft.sites.find((x) => x.siteSlug === 'app')!.config.volumes[0]!.seed;
  assert.deepEqual(headSeed, { kind: 'tree', ...t1 });

  // agents: any enrolled site may upload the bytes of its commits (content-addressed, checked)
  const app = await enrollSite(admin, id, 'app');
  const db = await enrollSite(admin, id, 'db');
  const C = `binary-ish ${uniq()}`;
  assert.deepEqual((await db.api.agentCheckBlobs({ body: { sha256s: [shaOf(A), shaOf(C)] } })).missing, [shaOf(C)]);
  await rejects(db.api.agentCheckBlobs({ body: { sha256s: ['nope'] } }), 'invalid', 422);
  await rejects(db.api.agentPutBlob({ sha256: shaOf(C), body: enc(C + 'x') }), 'invalid', 422);
  assert.deepEqual(await db.api.agentPutBlob({ sha256: shaOf(C), body: enc(C) }), { sha256: shaOf(C), sizeBytes: enc(C).length });
  assert.deepEqual((await app.api.agentCheckBlobs({ body: { sha256s: [shaOf(A), shaOf(C)] } })).missing, []);

  // an older agent still uploads snapshots: the history stays readable; the cloud view carries no pushes
  const head = (await app.api.getAgentRevision({ revId: ok.revision.revId })).revision;
  await app.api.reportHead({ body: { revision: head } });
  const snapTree: VolumeTree = { entries: [entry('data/c.bin', C), entry('flows.json', A)], dirs: [] };
  const snap = { snapshotId: ulid(), trigger: 'manual-agent' as const, takenAt: new Date().toISOString(), actor: 'ops@example.com', tree: snapTree, treeHash: await treeHashOf(snapTree), excludedCount: 0, skippedCount: 0, skipped: [], requestId: null, pushId: null, note: '' };
  const v1 = (await app.api.agentCreateVolumeSnapshot({ volumeSlug: 'hello-api-data', body: snap })).version;
  assert.deepEqual((await admin.api.listVolumeContentVersions(one)).items.map((v) => v.versionId), [v1.versionId]);
  assert.deepEqual((await admin.api.getVolumeContentVersion({ ...one, versionId: v1.versionId })).tree, snapTree);
  assert.deepEqual((await app.api.poll({ query: { wait: 0 } })).volumeContent, []);

  // permissions: reading needs deployment.config.read
  const nobody = await person([]);
  await rejects(nobody.api.listVolumeContent({ deploymentId: id }), 'not-found', 404);
});

test('starter content: tree seeds publish with blobContents render errors, diff per file, agent blob access, legacy File seeds refused', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const badText = `listen {{ nope.thing }}\n`;
  const goodText = `deployment {{ deployment.id }} ${uniq()}\n`;
  const plain = `static ${uniq()}`;
  for (const t of [badText, goodText, plain]) await admin.api.uploadBlob({ body: enc(t) });
  const { site, dv } = await appSite(admin, id);
  site.config.volumes[0]!.seed = { kind: 'tree', entries: [entry('cfg/app.conf', badText, 'expressions'), entry('static.txt', plain)], dirs: ['empty'] };
  const w = await admin.api.putDraftSite({ deploymentId: id, siteSlug: 'app', body: { ifRevision: dv, site } });
  assert.ok(!w.validation.issues.some((i) => i.severity === 'error'), JSON.stringify(w.validation.issues));
  // §21 #105: the render error blocks the commit (the change stays in the browser change set)
  const at = 'sites[app].config.volumes[hello-api-data].seed.entries[cfg/app.conf]';
  const pub = await rejects(publish(admin.api, id, { ifRevision: w.draftVersion, summary: 'x' } ), 'invalid', 422);
  assert.ok(codes(pub).includes('V_FILE_RENDER'));
  assert.ok((pub.details.errors as { code: string; path: string }[]).some((b) => b.code === 'V_FILE_RENDER' && b.path === at), JSON.stringify(pub.details));
  // fix the entry → publishable; diff lists one change per file (blob before / after) + the empty folder
  site.config.volumes[0]!.seed = { kind: 'tree', entries: [entry('cfg/app.conf', goodText, 'expressions'), entry('static.txt', plain)], dirs: ['empty'] };
  const w2 = await admin.api.putDraftSite({ deploymentId: id, siteSlug: 'app', body: { ifRevision: w.draftVersion, site } });
  const d2 = await admin.api.getDiff({ deploymentId: id });
  assert.equal(d2.publishable, true, JSON.stringify(d2.blockers));
  const p = await publish(admin.api, id, { ifRevision: w2.draftVersion, summary: 'starter content' } );
  assert.equal(p.revision.seq, 1);
  // the site may fetch the content blobs of an open proposal / its head
  const app = await enrollSite(admin, id, 'app');
  assert.equal(Buffer.from(await (await app.api.getAgentBlob({ sha256: shaOf(plain) })).arrayBuffer()).toString(), plain);
  assert.equal(Buffer.from(await (await app.api.getAgentBlob({ sha256: shaOf(goodText) })).arrayBuffer()).toString(), goodText);
  await rejects(app.api.getAgentBlob({ sha256: shaOf(badText) }), 'forbidden', 403);
  // a second file change → the diff against the head shows exactly that file as changed
  const plain2 = `static v2 ${uniq()}`;
  await admin.api.uploadBlob({ body: enc(plain2) });
  const r1 = (await app.api.getAgentRevision({ revId: p.revision.revId })).revision;
  await app.api.reportHead({ body: { revision: r1 } });
  site.config.volumes[0]!.seed = { kind: 'tree', entries: [entry('cfg/app.conf', goodText, 'expressions'), entry('new.txt', plain), entry('static.txt', plain2)], dirs: [] };
  const dv3 = (await admin.api.getDeployment({ deploymentId: id })).draftVersion;
  await admin.api.putDraftSite({ deploymentId: id, siteSlug: 'app', body: { ifRevision: dv3, site } });
  const d3 = await admin.api.getDiff({ deploymentId: id });
  const c3 = d3.changes.filter((c) => c.path.startsWith('app/volumes/'));
  assert.deepEqual(c3.map((c) => [c.path, c.kind, c.blob?.before ?? null, c.blob?.after ?? null]).sort(), [
    ['app/volumes/hello-api-data/content/empty/', 'removed', null, null],
    ['app/volumes/hello-api-data/content/new.txt', 'added', null, shaOf(plain)],
    ['app/volumes/hello-api-data/content/static.txt', 'changed', shaOf(plain), shaOf(plain2)],
  ]);
  assert.ok(c3.every((c) => c.note && c.section === 'volumes'));
  // legacy File seeds are refused in the draft (Files are never volume content)
  const legacy = structuredClone(site);
  legacy.config.volumes[0]!.seed = { kind: 'files', entries: [{ path: 'banner.txt', fileSlug: 'banner' }] };
  const le = await rejects(admin.api.putDraftSite({ deploymentId: id, siteSlug: 'app', body: { ifRevision: dv3 + 1, site: legacy } }), 'invalid', 422);
  assert.ok(codes(le).includes('V_VOLUME_SEED_FILE_REF'), JSON.stringify(le.details));
});

test('daily job prunes AUTO snapshots beyond the newest 200 versions (pinned / referenced / manual kept)', async () => {
  const id = `prune${uniq()}`;
  const key = 'app:data';
  const st = adb.collection('deployments').doc(id).collection('volumeContent').doc(key);
  const now = new Date().toISOString();
  const ids: string[] = [];
  const b = adb.bulkWriter();
  for (let seq = 1; seq <= 205; seq++) {
    const versionId = ulid(Date.now() + seq);
    ids.push(versionId);
    const v: Partial<VolumeContentVersion> = { versionId, seq, origin: seq === 150 ? 'edit' : 'snapshot', trigger: seq === 150 ? null : seq === 3 ? 'manual-agent' : 'auto', treeHash: `sha256:${'c'.repeat(64)}`, createdAt: now, createdBy: 'agent:app', deploymentId: id, siteSlug: 'app', volumeSlug: 'data', baseVersionId: seq === 150 ? ids[1]! : null };
    b.set(st.collection('versions').doc(versionId), v);
  }
  await b.close();
  await st.set({ deploymentId: id, siteSlug: 'app', volumeSlug: 'data', versionCount: 205, lastSeq: 205, latest: null, lastSnapshot: null, push: null, _open: false });
  await st.collection('pushes').doc(ulid()).set({ pushId: 'p', versionId: ids[0], baseVersionId: null, status: 'applied' });
  const r = await runDaily();
  assert.ok(r.volumeVersionsPruned >= 2);
  const left = (await st.collection('versions').orderBy('seq').get()).docs.map((d) => d.data().seq as number);
  assert.equal(left.length, 203);
  assert.deepEqual(left.slice(0, 4), [1, 2, 3, 6]); // 1 = a push's version, 2 = base of an edit, 3 = manual
  assert.equal((await st.get()).data()!.versionCount, 203);
  const r2 = await runDaily();
  void r2;
  assert.equal((await st.collection('versions').get()).size, 203);
});

test('migrate-h: legacy File seeds + a File inside a writable volume → own content; revisions untouched; idempotent; verify-schema; audit', async () => {
  const admin = await person('admin');
  const { dep, templateId } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const seedText = `seed-only ${uniq()}`;
  const settings = `module.exports = { uiPort: 1880 } // ${uniq()}`;
  const seedBlob = await admin.api.uploadBlob({ body: enc(seedText) });
  const setBlob = await admin.api.uploadBlob({ body: enc(settings) });
  const legacy = (cfg: Doc) => {
    cfg.files.push({ fileSlug: 'seed-only', label: 'Seed only', description: '', mime: 'text/plain', editor: 'text', maxBytes: 1048576, content: { sha256: seedBlob.sha256, sizeBytes: seedBlob.sizeBytes, uploadedAt: new Date().toISOString() }, render: 'none', location: { kind: 'auto' } });
    cfg.files.push({ fileSlug: 'settings', label: 'Settings', description: '', mime: 'text/javascript', editor: 'text', maxBytes: 1048576, content: { sha256: setBlob.sha256, sizeBytes: setBlob.sizeBytes, uploadedAt: new Date().toISOString() }, render: 'expressions', location: { kind: 'auto' } });
    const app = cfg.sites.find((s: Doc) => s.siteSlug === 'app');
    app.config.volumes[0].seed = { kind: 'files', entries: [{ path: 'banner.txt', fileSlug: 'banner' }, { path: 'data/seed.txt', fileSlug: 'seed-only' }] };
    const api = app.config.containers.find((c: Doc) => c.containerSlug === 'hello-api');
    api.volumes.push({ imageVolume: null, mountPath: '/data/settings.js', source: { kind: 'file', fileSlug: 'settings' }, readOnly: true });
    return cfg;
  };
  // a published revision first (immutable history), then legacy shapes written directly (the API refuses them now)
  await publish(admin.api, id, { ifRevision: 0, summary: 'before migrate-h' } );
  const tRef = adb.collection('templates').doc(templateId);
  const dRef = adb.collection('deployments').doc(id);
  const t0 = (await tRef.get()).data()!;
  await tRef.update({ _body: JSON.stringify(legacy(JSON.parse(t0._body))) });
  const d0 = (await dRef.get()).data()!;
  await dRef.update({ _draft: JSON.stringify(legacy(JSON.parse(d0._draft))), _srcSnapshot: JSON.stringify(legacy(JSON.parse(d0._srcSnapshot))) });
  const revs = await dRef.collection('revisions').get();
  const rv = revs.docs[0]!;
  await rv.ref.update({ _config: JSON.stringify(legacy(JSON.parse(rv.data()._config))) });
  const revBefore = (await rv.ref.get()).data()!;
  const tBefore = (await tRef.get()).data()!;
  const dvBefore = (await dRef.get()).data()!.draftVersion as number;
  // verify-schema reports the legacy seeds
  const vb = (await verifySchema(adb)).filter((l) => l.includes(id) || l.includes(templateId));
  assert.ok(vb.some((l) => l.includes('legacy starter content from Files')), vb.join('\n'));

  // dry run: per-doc before / after, files removed / kept, moved mount + placeholder note
  const plan = await planMigrationH(adb);
  const out: string[] = [];
  printPlan(plan, (l) => out.push(l));
  const text = out.join('\n');
  const tDoc = plan.docs.find((d) => d.path.startsWith(`templates/${templateId}`))!;
  const dDoc = plan.docs.find((d) => d.path.startsWith(`deployments/${id}`))!;
  assert.ok(tDoc && dDoc, text);
  const tl = tDoc.lines.join('\n');
  assert.match(tl, /volume hello-api-data before: files \[banner\.txt ← file banner/);
  assert.match(tl, /volume hello-api-data after: {2}tree 3 file\(s\)/);
  assert.ok(tl.includes(`data/seed.txt ${enc(seedText).length} B ${seedBlob.sha256.slice(0, 19)}`), tl);
  assert.match(tl, /file seed-only removed/);
  assert.match(tl, /file settings removed/);
  assert.match(tl, /file banner kept/);
  assert.match(tl, /hello-api mounts file settings at \/data\/settings\.js inside the writable volume hello-api-data/);
  assert.match(tl, /fills the 0-byte former mountpoint settings\.js once/);
  assert.ok(dDoc.lines.some((l) => l.startsWith('draft: ')) && dDoc.lines.some((l) => l.startsWith('srcTemplate snapshot: ')));
  assert.ok(dDoc.lines.includes(`draftVersion ${dvBefore} → ${dvBefore + 1} (open editors reload)`));
  assert.ok(!plan.ops.some((o) => o.path.includes('/revisions/')), 'revisions are never migrated');
  assert.equal(JSON.stringify((await tRef.get()).data()), JSON.stringify(tBefore), 'dry run writes nothing');

  await applyPlanH(adb, plan);
  const tb = JSON.parse((await tRef.get()).data()!._body);
  assert.equal((await tRef.get()).data()!.version, tBefore.version, 'templates keep their version');
  const vol = tb.sites.find((s: Doc) => s.siteSlug === 'app').config.volumes[0];
  assert.equal(vol.seed.kind, 'tree');
  assert.deepEqual(vol.seed.entries.map((e: Doc) => [e.path, e.sha256, e.render]), [
    ['banner.txt', HELLO_BANNER_SHA(tb), 'expressions'],
    ['data/seed.txt', seedBlob.sha256, 'none'],
    ['settings.js', setBlob.sha256, 'expressions'],
  ]);
  assert.deepEqual(tb.files.map((f: Doc) => f.fileSlug), ['banner']);
  assert.ok(!tb.sites.find((s: Doc) => s.siteSlug === 'app').config.containers.find((c: Doc) => c.containerSlug === 'hello-api').volumes.some((m: Doc) => m.source.kind === 'file'));
  const d1 = (await dRef.get()).data()!;
  assert.equal(d1.draftVersion, dvBefore + 1);
  assert.equal(d1.updatedBy, 'system:migrate-h');
  assert.equal(JSON.parse(d1._draft).sites.find((s: Doc) => s.siteSlug === 'app').config.volumes[0].seed.kind, 'tree');
  assert.equal(JSON.parse(d1._srcSnapshot).sites.find((s: Doc) => s.siteSlug === 'app').config.volumes[0].seed.kind, 'tree');
  assert.deepEqual((await rv.ref.get()).data(), revBefore, 'the revision is untouched');
  // the migrated draft is valid through the API (getDeployment + diff)
  const view = await admin.api.getDeployment({ deploymentId: id });
  assert.equal(view.draftVersion, dvBefore + 1);
  // idempotent rerun: nothing for these docs
  const plan2 = await planMigrationH(adb);
  assert.ok(!plan2.ops.some((o) => o.path === `templates/${templateId}` || o.path === `deployments/${id}`), JSON.stringify(plan2.ops.map((o) => o.path)));
  // verify-schema: nothing left for these docs (the revision keeps its legacy seed: history)
  const va = (await verifySchema(adb)).filter((l) => (l.includes(id) || l.includes(templateId)) && !l.includes('/revisions/'));
  assert.deepEqual(va, []);
  // one system.migration audit entry (op system:migrate-h)
  const audit = (await adb.collection('auditLog').where('op', '==', 'system:migrate-h').get()).docs.map((d) => d.data());
  assert.ok(audit.length >= 1);
  const datas = audit.map((a) => JSON.parse(a._data));
  assert.ok(datas.every((d) => d.migration === 'h'));
  assert.ok(datas.some((data) => data.docs.some((d: Doc) => d.path.startsWith(`deployments/${id}`))), 'an entry of this run names the deployment');
});

/** the hello banner file's content sha (the fixture's File) */
function HELLO_BANNER_SHA(cfg: Doc): string {
  return cfg.files.find((f: Doc) => f.fileSlug === 'banner').content.sha256;
}
