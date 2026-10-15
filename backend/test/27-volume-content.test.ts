/**
 * §21 #101 volume content + persist-to-cloud volumes (round VC): cloud edit / save / 409 / 422s, restore, send /
 * supersede / cancel, snapshot request through the cloud view, agent blob check / put / snapshot (idempotent), push
 * report, getAgentBlob access, heartbeat volumeSync → state.live, publish with tree seeds + blobContents render errors,
 * diff per-file, daily prune, migrate-h (dry run, apply, revisions untouched, idempotent, verify-schema, audit).
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { ulid, volumeTreeHash, type Site, type ValidationIssue, type VolumeContentVersion, type VolumeTree } from '@opm/shared';
import { adb, enrollSite, helloDeployment, person, raw, rejects, sha256Hex, uniq, type Person, publish } from './helpers.ts';
import { runDaily } from '../functions/src/jobs/daily.ts';
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

async function cloudTypes(deploymentId: string): Promise<string[]> {
  const q = await adb.collection('deployments').doc(deploymentId).collection('activities').where('siteSlug', '==', '_cloud').get();
  return q.docs.map((d) => d.data().type as string);
}
async function epoch(deploymentId: string): Promise<number> {
  return (await adb.collection('deployments').doc(deploymentId).get()).data()!.agentEpoch as number;
}
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

test('volume content: edit / restore / send / cancel / snapshot request / agent snapshot + push report / blob access / heartbeat', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const one = { deploymentId: id, siteSlug: 'app', volumeSlug: 'hello-api-data' };

  // not persisted yet → 404; persist on in the draft → listed (never snapshotted)
  await rejects(admin.api.getVolumeContent(one), 'not-found', 404);
  const { site, dv } = await appSite(admin, id);
  site.config.volumes[0]!.persist = PERSIST;
  await admin.api.putDraftSite({ deploymentId: id, siteSlug: 'app', body: { ifRevision: dv, site } });
  const list = await admin.api.listVolumeContent({ deploymentId: id });
  assert.equal(list.items.length, 1);
  assert.deepEqual({ ...list.items[0], updatedAt: null }, { deploymentId: id, siteSlug: 'app', volumeSlug: 'hello-api-data', persist: PERSIST, persistInHead: false, latest: null, lastSnapshot: null, versionCount: 0, push: null, snapshotRequest: null, live: null, sync: 'never-snapshotted', updatedAt: null });
  await rejects(admin.api.getVolumeContent({ ...one, siteSlug: 'db', volumeSlug: 'redis-data' }), 'not-found', 404);

  // blobs (human upload)
  const A = `[{"id":"flow-${uniq()}"}]`;
  const B = `module.exports = ${Date.now()};`;
  for (const t of [A, B]) await admin.api.uploadBlob({ body: enc(t) });
  const t1: VolumeTree = { entries: [entry('flows.json', A)], dirs: ['lib'] };

  // 422s: missing blob (details.missing), excluded path, bad path, render expressions in a version
  const fake = `sha256:${'a'.repeat(64)}`;
  const miss = await rejects(admin.api.createVolumeContentVersion({ ...one, body: { baseVersionId: null, tree: { entries: [{ path: 'x.bin', sha256: fake, size: 3, mode: '0644', render: 'none' }], dirs: [] }, note: '' } }), 'invalid', 422);
  assert.deepEqual(codes(miss), ['V_BLOB_MISSING']);
  assert.deepEqual(miss.details.missing, [fake]);
  const exc = await rejects(admin.api.createVolumeContentVersion({ ...one, body: { baseVersionId: null, tree: { entries: [entry('debug.log', A), entry('node_modules/a/i.js', B)], dirs: [] }, note: '' } }), 'invalid', 422);
  assert.deepEqual(codes(exc), ['V_VOLUME_CONTENT_EXCLUDED', 'V_VOLUME_CONTENT_EXCLUDED']);
  const badp = await rejects(admin.api.createVolumeContentVersion({ ...one, body: { baseVersionId: null, tree: { entries: [entry('../x', A)], dirs: [] }, note: '' } }), 'invalid', 422);
  assert.ok(codes(badp).includes('V_VOLUME_CONTENT_PATH'), JSON.stringify(badp.details));
  const rend = await rejects(admin.api.createVolumeContentVersion({ ...one, body: { baseVersionId: null, tree: { entries: [entry('a.txt', A, 'expressions')], dirs: [] }, note: '' } }), 'invalid', 422);
  assert.ok(codes(rend).includes('V_VOLUME_CONTENT'), JSON.stringify(rend.details));

  // edit → version 1 (201), stale base → 409 { latest }, identical tree → 200 the same version
  const v1 = await admin.api.createVolumeContentVersion({ ...one, body: { baseVersionId: null, tree: t1, note: 'first' } });
  assert.equal(v1.version.seq, 1);
  assert.equal(v1.version.origin, 'edit');
  assert.equal(v1.version.treeHash, await treeHashOf(t1));
  assert.deepEqual(v1.tree, t1);
  assert.deepEqual(v1.version.changes, { added: 1, removed: 0, modified: 0 });
  const stale = await rejects(admin.api.createVolumeContentVersion({ ...one, body: { baseVersionId: null, tree: { entries: [entry('flows.json', B)], dirs: [] }, note: '' } }), 'conflict', 409);
  assert.equal((stale.details.latest as Doc).versionId, v1.version.versionId);
  const same = await raw('POST', `/v1/deployments/${id}/volume-content/app/hello-api-data/versions`, { headers: { Authorization: admin.bearer, 'Content-Type': 'application/json' }, body: JSON.stringify({ baseVersionId: v1.version.versionId, tree: t1, note: '' }) });
  assert.equal(same.status, 200);
  assert.equal(same.json.version.versionId, v1.version.versionId);
  // the tree JSON is the content-addressed blob files/<treeHash hex>
  assert.equal((await admin.api.getBlob({ sha256: v1.version.treeHash }).then((r) => r.text())).length > 0, true);

  const t2: VolumeTree = { entries: [entry('flows.json', A), entry('lib/x.js', B)], dirs: [] };
  const v2 = await admin.api.createVolumeContentVersion({ ...one, body: { baseVersionId: v1.version.versionId, tree: t2, note: '' } });
  assert.equal(v2.version.seq, 2);
  assert.deepEqual(v2.version.changes, { added: 1, removed: 0, modified: 0 });
  const page1 = await admin.api.listVolumeContentVersions({ ...one, query: { limit: 1 } });
  assert.deepEqual(page1.items.map((v) => v.seq), [2]);
  const page2 = await admin.api.listVolumeContentVersions({ ...one, query: { limit: 1, cursor: (page1 as Doc).cursor } });
  assert.deepEqual(page2.items.map((v) => v.seq), [1]);
  assert.deepEqual((await admin.api.getVolumeContentVersion({ ...one, versionId: v1.version.versionId })).tree, t1);
  await rejects(admin.api.getVolumeContentVersion({ ...one, versionId: ulid() }), 'not-found', 404);

  // restore v1 → version 3 (origin restore, base = v1)
  const v3 = await admin.api.restoreVolumeContentVersion({ ...one, versionId: v1.version.versionId, body: { note: '' } });
  assert.equal(v3.version.seq, 3);
  assert.equal(v3.version.origin, 'restore');
  // the base is the latest at restore time (v2); the copied version is restoredFromVersionId
  assert.equal(v3.version.baseVersionId, v2.version.versionId);
  assert.equal(v3.version.restoredFromVersionId, v1.version.versionId);
  assert.equal(v2.version.restoredFromVersionId, null);
  assert.equal(v3.version.treeHash, v1.version.treeHash);
  assert.deepEqual(v3.version.changes, { added: 0, removed: 1, modified: 0 });

  // snapshot request needs persist in the head the site runs
  const req0 = await rejects(admin.api.requestVolumeSnapshot(one), 'invalid', 422);
  assert.deepEqual(codes(req0), ['V_VOLUME_PERSIST']);
  const app = await enrollSite(admin, id, 'app');
  const db = await enrollSite(admin, id, 'db');
  // agent ops before the head has persist → 403
  await rejects(app.api.agentPutBlob({ sha256: shaOf(A), body: enc(A) }), 'forbidden', 403);
  const head = await publishAndReport(admin, id, app, 'persist on');
  assert.equal((await admin.api.getVolumeContent(one)).persistInHead, true);

  const e0 = await epoch(id);
  const st = await admin.api.requestVolumeSnapshot(one);
  assert.ok(st.snapshotRequest?.requestId);
  assert.equal(await epoch(id), e0 + 1);
  const view = await app.api.poll({ query: { wait: 0 } });
  assert.deepEqual(view.volumeContent, [{ siteSlug: 'app', volumeSlug: 'hello-api-data', push: null, snapshotRequest: st.snapshotRequest }]);

  // agent: check / put blobs
  const C = `binary-ish ${uniq()}`;
  assert.deepEqual((await app.api.agentCheckBlobs({ body: { sha256s: [shaOf(A), shaOf(C)] } })).missing, [shaOf(C)]);
  await rejects(app.api.agentCheckBlobs({ body: { sha256s: ['nope'] } }), 'invalid', 422);
  await rejects(app.api.agentPutBlob({ sha256: shaOf(C), body: enc(C + 'x') }), 'invalid', 422);
  const put = await app.api.agentPutBlob({ sha256: shaOf(C), body: enc(C) });
  assert.deepEqual(put, { sha256: shaOf(C), sizeBytes: enc(C).length });
  assert.deepEqual((await app.api.agentCheckBlobs({ body: { sha256s: [shaOf(A), shaOf(C)] } })).missing, []);

  // agent snapshot (answers the request): 201, idempotent retry → existing, request cleared
  const snapTree: VolumeTree = { entries: [entry('data/c.bin', C), entry('flows.json', A)], dirs: ['empty'] };
  const snap = { snapshotId: ulid(), trigger: 'manual-cloud' as const, takenAt: new Date().toISOString(), actor: 'ops@example.com', tree: snapTree, treeHash: await treeHashOf(snapTree), excludedCount: 2, skippedCount: 1, skipped: [{ path: 'link', reason: 'symlink' as const }], requestId: st.snapshotRequest!.requestId, pushId: null, note: '' };
  await rejects(app.api.agentCreateVolumeSnapshot({ volumeSlug: 'hello-api-data', body: { ...snap, treeHash: `sha256:${'b'.repeat(64)}` } }), 'invalid', 422);
  const bm = await rejects(app.api.agentCreateVolumeSnapshot({ volumeSlug: 'hello-api-data', body: { ...snap, tree: { entries: [{ path: 'q', sha256: fake, size: 1, mode: '0644', render: 'none' }], dirs: [] }, treeHash: await treeHashOf({ entries: [{ path: 'q', sha256: fake, size: 1, mode: '0644', render: 'none' }], dirs: [] }) } }), 'invalid', 422);
  assert.deepEqual(bm.details.missing, [fake]);
  await rejects(app.api.agentCreateVolumeSnapshot({ volumeSlug: 'nope-volume', body: snap }), 'forbidden', 403);
  await rejects(db.api.agentCreateVolumeSnapshot({ volumeSlug: 'hello-api-data', body: snap }), 'forbidden', 403);
  const s4 = await app.api.agentCreateVolumeSnapshot({ volumeSlug: 'hello-api-data', body: snap });
  assert.equal(s4.existing, false);
  const v4 = s4.version;
  assert.deepEqual([v4.seq, v4.origin, v4.trigger, v4.createdBy, v4.agentSnapshotId, v4.requestId, v4.excludedCount, v4.skippedCount], [4, 'snapshot', 'manual-cloud', 'ops@example.com', snap.snapshotId, snap.requestId, 2, 1]);
  assert.deepEqual(v4.changes, { added: 1, removed: 0, modified: 0 });
  assert.equal(v4.restoredFromVersionId, null);
  const again = await app.api.agentCreateVolumeSnapshot({ volumeSlug: 'hello-api-data', body: snap });
  assert.equal(again.existing, true);
  assert.equal(again.version.versionId, v4.versionId);
  let state = await admin.api.getVolumeContent(one);
  assert.equal(state.snapshotRequest, null);
  assert.equal(state.lastSnapshot?.versionId, v4.versionId);
  assert.equal(state.latest?.versionId, v4.versionId);
  assert.equal(state.versionCount, 4);
  assert.deepEqual((await app.api.poll({ query: { wait: 0 } })).volumeContent, []);

  // edit on top of the snapshot → v5; send v5 → base = v4 (newest snapshot ancestor); send again supersedes
  const t5: VolumeTree = { entries: [entry('data/c.bin', C), entry('flows.json', B)], dirs: ['empty'] };
  const v5 = (await admin.api.createVolumeContentVersion({ ...one, body: { baseVersionId: v4.versionId, tree: t5, note: 'cloud edit' } })).version;
  const e1 = await epoch(id);
  const p1 = await admin.api.sendVolumeContent({ ...one, body: { versionId: v5.versionId, restart: true, note: 'go' } });
  assert.deepEqual([p1.status, p1.versionId, p1.treeHash, p1.baseVersionId, p1.baseTreeHash, p1.restart], ['pending', v5.versionId, v5.treeHash, v4.versionId, v4.treeHash, true]);
  assert.equal(await epoch(id), e1 + 1);
  assert.equal(p1.versionSeq, v5.seq);
  const p2 = await admin.api.sendVolumeContent({ ...one, body: { versionId: v5.versionId, restart: false, note: '' } });
  state = await admin.api.getVolumeContent(one);
  assert.equal(state.push?.pushId, p2.pushId);
  assert.equal(state.sync, 'cloud-ahead'); // no live report yet: the last snapshot (v4) = the push base
  const pushView = (await app.api.poll({ query: { wait: 0 } })).volumeContent!;
  assert.equal(pushView.length, 1);
  assert.equal(pushView[0]!.push?.pushId, p2.pushId);
  // sending a snapshot version: it is its own base
  await rejects(admin.api.sendVolumeContent({ ...one, body: { versionId: ulid(), restart: false, note: '' } }), 'not-found', 404);

  // getAgentBlob: the pending push's trees + files (and its base) for that site only
  const blobOk = async (sha: string) => Buffer.from(await (await app.api.getAgentBlob({ sha256: sha })).arrayBuffer());
  assert.ok((await blobOk(v5.treeHash)).toString().includes('flows.json'));
  assert.equal((await blobOk(v4.treeHash)).length > 0, true);
  assert.equal((await blobOk(shaOf(B))).toString(), B);
  assert.equal((await blobOk(shaOf(C))).toString(), C);
  await rejects(db.api.getAgentBlob({ sha256: v5.treeHash }), 'forbidden', 403);
  const other = `unrelated ${uniq()}`;
  await admin.api.uploadBlob({ body: enc(other) });
  await rejects(app.api.getAgentBlob({ sha256: shaOf(other) }), 'forbidden', 403);

  // heartbeat volumeSync → state.live (+ reportedAt); bad shape → 422
  const hb = { siteSlug: 'app', agentVersion: '0.12.0', headRevId: head.revId, staging: null, peersSeen: [], metrics: { cpu: 0, memPct: 0, diskPct: 0 }, edge: null, keyVersion: 1, advertiseHost: '127.0.0.1', agentPort: 7080, ip: '127.0.0.1', hostname: 'host-app' };
  await rejects(app.api.heartbeat({ body: { ...hb, volumeSync: [{ volumeSlug: 'hello-api-data', state: 'weird' }] } as never }), 'invalid', 422);
  const report = { volumeSlug: 'hello-api-data', state: 'cloud-ahead' as const, liveTreeHash: v4.treeHash, snapshotTreeHash: v4.treeHash, liveChanged: false, queued: 0, scannedAt: new Date().toISOString(), lastError: null };
  await app.api.heartbeat({ body: { ...hb, volumeSync: [report] } });
  state = await admin.api.getVolumeContent(one);
  assert.deepEqual({ ...state.live, reportedAt: 'x' }, { ...report, reportedAt: 'x' });
  assert.ok(Date.parse(state.live!.reportedAt) > Date.now() - 60_000);
  assert.equal(state.sync, 'cloud-ahead');
  assert.equal((await admin.api.listVolumeContent({ deploymentId: id })).items[0]!.sync, 'cloud-ahead');
  // an unchanged report (only scannedAt differs) within 10 min is not written again; a changed one is
  const first = state.live!.reportedAt;
  await app.api.heartbeat({ body: { ...hb, volumeSync: [{ ...report, scannedAt: new Date(Date.now() + 1000).toISOString() }] } });
  assert.equal((await admin.api.getVolumeContent(one)).live!.reportedAt, first);
  assert.equal((await admin.api.getVolumeContent(one)).live!.scannedAt, report.scannedAt);
  await app.api.heartbeat({ body: { ...hb, volumeSync: [{ ...report, liveChanged: true }] } });
  state = await admin.api.getVolumeContent(one);
  assert.equal(state.live!.liveChanged, true);
  assert.notEqual(state.live!.reportedAt, first);
  await app.api.heartbeat({ body: { ...hb, volumeSync: [report] } });

  // cancel (+ 409 when none pending) → nothing in the cloud view
  const e2 = await epoch(id);
  const cancelled = await admin.api.cancelVolumeContentPush(one);
  assert.equal(cancelled.push?.status, 'cancelled');
  assert.equal(await epoch(id), e2 + 1);
  await rejects(admin.api.cancelVolumeContentPush(one), 'conflict', 409);
  assert.deepEqual((await app.api.poll({ query: { wait: 0 } })).volumeContent, []);
  await rejects(app.api.getAgentBlob({ sha256: shaOf(B) }), 'forbidden', 403);

  // send again → the site applies it → report (idempotent); superseded / cancelled keep their status
  const p3 = await admin.api.sendVolumeContent({ ...one, body: { versionId: v5.versionId, restart: true, note: '' } });
  const result = { resolution: 'fast-forward' as const, written: 1, deleted: 0, keptLive: 0, snapshotId: ulid(), restarted: ['hello-api'], error: null, afterVersionId: null };
  await rejects(app.api.agentReportVolumePush({ volumeSlug: 'hello-api-data', pushId: p3.pushId, body: { status: 'done' as never, resolvedBy: 'ops@example.com', resolvedAt: new Date().toISOString(), result } }), 'invalid', 422);
  await rejects(db.api.agentReportVolumePush({ volumeSlug: 'hello-api-data', pushId: p3.pushId, body: { status: 'applied', resolvedBy: 'ops@example.com', resolvedAt: new Date().toISOString(), result } }), 'not-found', 404);
  const e3 = await epoch(id);
  const done = await app.api.agentReportVolumePush({ volumeSlug: 'hello-api-data', pushId: p3.pushId, body: { status: 'applied', resolvedBy: 'ops@example.com', resolvedAt: new Date().toISOString(), result } });
  assert.equal(done.status, 'applied');
  assert.deepEqual(done.result, result);
  assert.equal(await epoch(id), e3 + 1);
  const twice = await app.api.agentReportVolumePush({ volumeSlug: 'hello-api-data', pushId: p3.pushId, body: { status: 'failed', resolvedBy: 'x@example.com', resolvedAt: new Date().toISOString(), result } });
  assert.equal(twice.status, 'applied');
  assert.equal((await app.api.agentReportVolumePush({ volumeSlug: 'hello-api-data', pushId: p1.pushId, body: { status: 'applied', resolvedBy: 'ops@example.com', resolvedAt: new Date().toISOString(), result } })).status, 'superseded');
  assert.equal((await admin.api.getVolumeContent(one)).push?.status, 'applied');

  // _cloud activities of the human ops + the snapshot upload
  const types = await cloudTypes(id);
  for (const t of ['volume-content.version-created', 'volume-content.snapshot-requested', 'volume-content.sent', 'volume-content.push-cancelled']) assert.ok(types.includes(t), `missing ${t}`);
  assert.ok(!types.includes('volume-content.snapshot-uploaded'), 'the agent logs snapshot-uploaded in its own chain');
  const sent = (await adb.collection('deployments').doc(id).collection('activities').where('type', '==', 'volume-content.sent').get()).docs.map((d) => JSON.parse(d.data()._data));
  assert.ok(sent.some((d) => d.supersededPushId === p1.pushId && d.pushId === p2.pushId));

  // permissions: read-only cannot write, write cannot send
  const rule = (actions: string[]) => [{ resource: 'deployment.config', actions, scope: { kind: 'all' } }] as never;
  const reader = await person(rule(['read']));
  assert.equal((await reader.api.getVolumeContent(one)).versionCount, 5);
  await rejects(reader.api.createVolumeContentVersion({ ...one, body: { baseVersionId: v5.versionId, tree: t1, note: '' } }), 'forbidden', 403);
  const writer = await person(rule(['read', 'write']));
  await rejects(writer.api.sendVolumeContent({ ...one, body: { versionId: v5.versionId, restart: false, note: '' } }), 'forbidden', 403);
  await rejects(writer.api.cancelVolumeContentPush(one), 'forbidden', 403);
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
