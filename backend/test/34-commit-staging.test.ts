/**
 * §21 #107 one Commit = one transaction incl. the bytes: stage → upload missing blobs → commit { stageId } (verify size +
 * sha256, promote BEFORE the commit is visible, stage deleted on every outcome), 422 V_BLOB_MISSING, 409 stale with
 * staged blobs, crash points (emulator-only x-opm-test-fault), owner checks, no blob / upload audit noise.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createHash, randomBytes } from 'node:crypto';
import type { ConfigInput } from '@opm/shared';
import { abucket, adb, BASE, depInput, helloDeployment, person, raw, rejects, tplInput, uniq } from './helpers.ts';

const shaOf = (b: Uint8Array) => 'sha256:' + createHash('sha256').update(b).digest('hex');
const hex = (s: string) => s.slice(7);
const bytes = (label: string) => new TextEncoder().encode(`${label} ${uniq('c')} ${randomBytes(8).toString('hex')}\n`);
async function stagedObjects(stageId: string) {
  const [files] = await abucket.getFiles({ prefix: `staging/${stageId}/` });
  return files.map((f) => f.name);
}
const inStore = async (sha: string) => (await abucket.file(`files/${hex(sha)}`).exists())[0];
const stageDoc = async (stageId: string) => (await adb.collection('stages').doc(stageId).get()).exists;
/** the first File of the hello config gets new content */
function withContent(c: ConfigInput, b: Uint8Array): ConfigInput {
  const f = c.files[0]!;
  f.content = { sha256: shaOf(b), sizeBytes: b.length };
  return c;
}
async function commitRaw(bearer: string, path: string, body: unknown, fault?: string) {
  return raw('POST', path, { headers: { authorization: bearer, 'content-type': 'application/json', ...(fault ? { 'x-opm-test-fault': fault } : {}) }, body: JSON.stringify(body) });
}

test('deployment commit with a new blob: stage reports only missing blobs, commit promotes, stage gone, blob readable; no blob audit noise', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const api = admin.real;
  const d0 = await api.getDeployment({ deploymentId: id });
  const base = d0.branch!.head!.commitId;
  const b = bytes('banner v2');
  const cfg = withContent(await depInput(api, id), b);
  const banner = d0.draft.files[0]!.content!;
  const t1 = new Date().toISOString();
  // the stage: the head's banner is present, the new one missing
  const st = await api.createStage({ body: { blobs: [{ sha256: shaOf(b), sizeBytes: b.length }, banner] } });
  assert.equal(st.present, 1);
  assert.deepEqual(st.missing, [{ sha256: shaOf(b), sizeBytes: b.length }]);
  assert.ok(await stageDoc(st.stageId));
  // sha mismatch → 422; present blob → staged false
  await rejects(api.putStageBlob({ stageId: st.stageId, sha256: shaOf(b), body: bytes('other') }), 'invalid', 422);
  const r = await api.putStageBlob({ stageId: st.stageId, sha256: shaOf(b), body: b });
  assert.equal(r.staged, true);
  assert.deepEqual(await stagedObjects(st.stageId), [`staging/${st.stageId}/${hex(shaOf(b))}`]);
  assert.equal(await inStore(shaOf(b)), false, 'nothing in the store before the commit');
  // the blob is not readable through the store API before the commit
  await rejects(api.getBlob({ sha256: shaOf(b) }), 'not-found', 404);
  const c = await api.commitDeployment({ deploymentId: id, body: { message: 'new banner', baseCommitId: base, config: cfg, stageId: st.stageId } });
  assert.equal(c.parentId, base);
  assert.equal(await inStore(shaOf(b)), true);
  assert.deepEqual(await stagedObjects(st.stageId), []);
  assert.equal(await stageDoc(st.stageId), false);
  const got = await api.getBlob({ sha256: shaOf(b) });
  assert.deepEqual(new Uint8Array(await got.arrayBuffer()), b);
  // the next stage for the same blob: present, nothing to upload
  const st2 = await api.createStage({ body: { blobs: [{ sha256: shaOf(b), sizeBytes: b.length }] } });
  assert.equal(st2.missing.length, 0);
  await api.deleteStage({ stageId: st2.stageId });
  assert.equal(await stageDoc(st2.stageId), false);
  // audit: no blob / upload / stage entries for this person; one commit entry
  const au = await api.listAuditLog({ query: { actor: admin.uid } });
  const after = au.items.filter((x) => x.ts >= t1);
  assert.deepEqual(after.map((x) => x.op), ['commitDeployment'], 'one entry: the commit');
  const commits = (await api.listAuditLog({ query: { target: `deployments/${id}`, action: 'deployment.committed' } })).items.filter((x) => x.data.commitId === c.commitId);
  assert.equal(commits.length, 1);
});

test('template commit with staged blob; missing staged blob → 422 V_BLOB_MISSING (no commit, stage dropped); no stageId → 422', async () => {
  const admin = await person('admin');
  const { templateId } = await helloDeployment(admin);
  const api = admin.real;
  const t0 = await api.getTemplate({ templateId });
  const base = t0.branch!.head!.commitId;
  const b = bytes('tpl banner');
  const cfg = withContent(await tplInput(api, templateId), b);
  // no stage and not stored → 422
  const e0 = await rejects(api.commitTemplate({ templateId, body: { message: 'x', baseCommitId: base, config: cfg } }), 'invalid', 422);
  assert.ok((e0.details.errors as { code: string; path: string }[]).some((x) => x.code === 'V_BLOB_MISSING' && x.path.startsWith('files[')));
  assert.deepEqual(e0.details.missing, [{ sha256: shaOf(b), sizeBytes: b.length }]);
  // a stage without the blob uploaded → 422, the stage is dropped
  const st = await api.createStage({ body: { blobs: [{ sha256: shaOf(b), sizeBytes: b.length }] } });
  await rejects(api.commitTemplate({ templateId, body: { message: 'x', baseCommitId: base, config: cfg, stageId: st.stageId } }), 'invalid', 422);
  assert.equal(await stageDoc(st.stageId), false);
  assert.equal((await api.getTemplate({ templateId })).branch!.head!.commitId, base);
  // a wrong size in the config vs the staged bytes → 422
  const st1 = await api.createStage({ body: { blobs: [{ sha256: shaOf(b), sizeBytes: b.length }] } });
  await api.putStageBlob({ stageId: st1.stageId, sha256: shaOf(b), body: b });
  const bad = JSON.parse(JSON.stringify(cfg)) as ConfigInput;
  bad.files[0]!.content!.sizeBytes = b.length + 1;
  await rejects(api.commitTemplate({ templateId, body: { message: 'x', baseCommitId: base, config: bad, stageId: st1.stageId } }), 'invalid', 422);
  assert.deepEqual(await stagedObjects(st1.stageId), []);
  assert.equal(await inStore(shaOf(b)), false);
  // the real thing
  const st2 = await api.createStage({ body: { blobs: [{ sha256: shaOf(b), sizeBytes: b.length }] } });
  await api.putStageBlob({ stageId: st2.stageId, sha256: shaOf(b), body: b });
  const c = await api.commitTemplate({ templateId, body: { message: 'tpl banner', baseCommitId: base, config: cfg, stageId: st2.stageId } });
  assert.equal(c.seq, t0.branch!.head!.seq + 1);
  assert.equal(await inStore(shaOf(b)), true);
  assert.equal(await stageDoc(st2.stageId), false);
});

test('failure after staging (validation error, fault after-verify) deletes the staged blobs and writes no commit', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const api = admin.real;
  const base = (await api.getDeployment({ deploymentId: id })).branch!.head!.commitId;
  const b = bytes('fail');
  const need = { sha256: shaOf(b), sizeBytes: b.length };
  // validation error (an unknown variable reference / bad slug) after the stage was filled
  const st = await api.createStage({ body: { blobs: [need] } });
  await api.putStageBlob({ stageId: st.stageId, sha256: need.sha256, body: b });
  const bad = withContent(await depInput(api, id), b);
  bad.variables.push({ variableSlug: 'BAD SLUG', label: 'x', description: '', value: '', required: false } as never);
  await rejects(api.commitDeployment({ deploymentId: id, body: { message: 'bad', baseCommitId: base, config: bad, stageId: st.stageId } }), 'invalid', 422);
  assert.deepEqual(await stagedObjects(st.stageId), []);
  assert.equal(await stageDoc(st.stageId), false);
  assert.equal(await inStore(need.sha256), false);
  // fault after verify (a crash before promotion)
  const st2 = await api.createStage({ body: { blobs: [need] } });
  await api.putStageBlob({ stageId: st2.stageId, sha256: need.sha256, body: b });
  const res = await commitRaw(admin.bearer, `/v1/deployments/${id}/commits`, { message: 'crash', baseCommitId: base, config: withContent(await depInput(api, id), b), stageId: st2.stageId }, 'after-verify');
  assert.equal(res.status, 500);
  assert.deepEqual(await stagedObjects(st2.stageId), []);
  assert.equal(await inStore(need.sha256), false);
  assert.equal((await api.getDeployment({ deploymentId: id })).branch!.head!.commitId, base);
  void BASE;
});

test('crash after promotion: no commit visible, head unchanged; retry finds the blob present and commits', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const api = admin.real;
  const d0 = await api.getDeployment({ deploymentId: id });
  const base = d0.branch!.head!.commitId;
  const count0 = d0.branch!.commitCount;
  const b = bytes('crash');
  const need = { sha256: shaOf(b), sizeBytes: b.length };
  const cfg = withContent(await depInput(api, id), b);
  const st = await api.createStage({ body: { blobs: [need] } });
  await api.putStageBlob({ stageId: st.stageId, sha256: need.sha256, body: b });
  const res = await commitRaw(admin.bearer, `/v1/deployments/${id}/commits`, { message: 'crash', baseCommitId: base, config: cfg, stageId: st.stageId }, 'after-promote');
  assert.equal(res.status, 500);
  const d1 = await api.getDeployment({ deploymentId: id });
  assert.equal(d1.branch!.head!.commitId, base, 'no commit visible');
  assert.equal(d1.branch!.commitCount, count0);
  assert.equal((await api.listDeploymentCommits({ deploymentId: id })).items[0]!.commitId, base);
  assert.equal(await stageDoc(st.stageId), false);
  assert.equal(await inStore(need.sha256), true, 'promoted (unreferenced) — harmless');
  // retry: present → nothing to upload; the commit works without re-sending bytes
  const st2 = await api.createStage({ body: { blobs: [need] } });
  assert.equal(st2.missing.length, 0);
  const c = await api.commitDeployment({ deploymentId: id, body: { message: 'retry', baseCommitId: base, config: cfg, stageId: st2.stageId } });
  assert.equal(c.parentId, base);
  assert.equal(await stageDoc(st2.stageId), false);
});

test('409 stale with staged blobs: stage deleted, retry with a new stage on the new head succeeds', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const api = admin.real;
  const base = (await api.getDeployment({ deploymentId: id })).branch!.head!.commitId;
  // someone else moves the head
  const other = await depInput(api, id);
  other.variables.find((v) => v.variableSlug === 'theme')!.value = 'red';
  const moved = await api.commitDeployment({ deploymentId: id, body: { message: 'other', baseCommitId: base, config: other } });
  const b = bytes('stale');
  const need = { sha256: shaOf(b), sizeBytes: b.length };
  const st = await api.createStage({ body: { blobs: [need] } });
  await api.putStageBlob({ stageId: st.stageId, sha256: need.sha256, body: b });
  const cfg = withContent(await depInput(api, id), b);
  const e = await rejects(api.commitDeployment({ deploymentId: id, body: { message: 'mine', baseCommitId: base, config: cfg, stageId: st.stageId } }), 'stale', 409);
  assert.equal((e.details.head as { commitId: string }).commitId, moved.commitId);
  assert.deepEqual(await stagedObjects(st.stageId), []);
  assert.equal(await stageDoc(st.stageId), false);
  assert.equal(await inStore(need.sha256), false);
  const st2 = await api.createStage({ body: { blobs: [need] } });
  assert.equal(st2.missing.length, 1);
  await api.putStageBlob({ stageId: st2.stageId, sha256: need.sha256, body: b });
  const c = await api.commitDeployment({ deploymentId: id, body: { message: 'mine again', baseCommitId: moved.commitId, config: cfg, stageId: st2.stageId } });
  assert.equal(c.parentId, moved.commitId);
});

test("another person's stage → 404; expired stage → 410; multi-part upload into a stage is not audited", async () => {
  const admin = await person('admin');
  const other = await person('admin');
  const b = bytes('owner');
  const need = { sha256: shaOf(b), sizeBytes: b.length };
  const st = await admin.real.createStage({ body: { blobs: [need] } });
  await rejects(other.real.putStageBlob({ stageId: st.stageId, sha256: need.sha256, body: b }), 'not-found', 404);
  await rejects(other.real.deleteStage({ stageId: st.stageId }), 'not-found', 404);
  assert.ok(await stageDoc(st.stageId));
  // a commit naming someone else's stage must not drop it
  const { dep } = await helloDeployment(other);
  const base = (await other.real.getDeployment({ deploymentId: dep.deploymentId })).branch!.head!.commitId;
  await rejects(other.real.commitDeployment({ deploymentId: dep.deploymentId, body: { message: 'x', baseCommitId: base, config: withContent(await depInput(other.real, dep.deploymentId), b), stageId: st.stageId } }), 'not-found', 404);
  assert.ok(await stageDoc(st.stageId), "the owner's stage survives");
  // multi-part (createUpload with stageId) → into the stage, no audit entry
  const t2 = new Date().toISOString();
  const up = await admin.real.createUpload({ body: { purpose: 'blob', sizeBytes: b.length, sha256: need.sha256, stageId: st.stageId } });
  await admin.real.putUploadPart({ uploadId: up.uploadId, index: '0', body: b });
  const done = await admin.real.completeUpload({ uploadId: up.uploadId });
  assert.equal(done.blob!.sha256, need.sha256);
  assert.deepEqual(await stagedObjects(st.stageId), [`staging/${st.stageId}/${hex(need.sha256)}`]);
  assert.equal(await inStore(need.sha256), false);
  const [m] = await abucket.file(`staging/${st.stageId}/${hex(need.sha256)}`).getMetadata();
  assert.equal(m.metadata?.opmSha256, hex(need.sha256));
  const au = await admin.real.listAuditLog({ query: { actor: admin.uid } });
  assert.deepEqual(au.items.filter((x) => x.ts >= t2).map((x) => x.op), [], 'staged uploads are not audited');
  // expiry → 410
  await adb.collection('stages').doc(st.stageId).update({ expiresAt: new Date(Date.now() - 1000).toISOString() });
  await rejects(admin.real.putStageBlob({ stageId: st.stageId, sha256: need.sha256, body: b }), 'gone', 410);
  await admin.real.deleteStage({ stageId: st.stageId });
  assert.deepEqual(await stagedObjects(st.stageId), []);
});

test('persist-to-cloud "Save as new version" with a stage: promoted before the version, stage dropped; daily job removes expired stages', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const api = admin.real;
  const base = (await api.getDeployment({ deploymentId: id })).branch!.head!.commitId;
  const cfg = await depInput(api, id);
  const vol = cfg.sites.find((s) => s.siteSlug === 'app')!.config.volumes[0]!;
  vol.persist = { exclude: ['node_modules/'], maxBytes: 50 * 1024 * 1024, auto: true };
  await api.commitDeployment({ deploymentId: id, body: { message: 'persist on', baseCommitId: base, config: cfg } });
  const one = { deploymentId: id, siteSlug: 'app', volumeSlug: vol.volumeSlug };
  const b = bytes('flows');
  const need = { sha256: shaOf(b), sizeBytes: b.length };
  const tree = { entries: [{ path: 'flows.json', sha256: need.sha256, size: b.length, mode: '0644', render: 'none' as const }], dirs: [] };
  const st = await api.createStage({ body: { blobs: [need] } });
  // without the bytes → 422, stage dropped
  await rejects(api.createVolumeContentVersion({ ...one, body: { baseVersionId: null, tree, note: '', stageId: st.stageId } }), 'invalid', 422);
  assert.equal(await stageDoc(st.stageId), false);
  const st2 = await api.createStage({ body: { blobs: [need] } });
  await api.putStageBlob({ stageId: st2.stageId, sha256: need.sha256, body: b });
  const v = await api.createVolumeContentVersion({ ...one, body: { baseVersionId: null, tree, note: 'edit', stageId: st2.stageId } });
  assert.equal(v.version.seq, 1);
  assert.equal(await inStore(need.sha256), true);
  assert.equal(await stageDoc(st2.stageId), false);
  // the daily job removes expired stages (doc + objects)
  const st3 = await api.createStage({ body: { blobs: [{ sha256: shaOf(bytes('x')), sizeBytes: 1 }] } });
  await abucket.file(`staging/${st3.stageId}/${'0'.repeat(64)}`).save(Buffer.from('x'));
  await adb.collection('stages').doc(st3.stageId).update({ expiresAt: new Date(Date.now() - 1000).toISOString() });
  const { runDaily } = await import('../functions/src/jobs/daily.ts');
  const res = await runDaily();
  assert.ok((res as { stagesExpired: number }).stagesExpired >= 1);
  assert.equal(await stageDoc(st3.stageId), false);
  assert.deepEqual(await stagedObjects(st3.stageId), []);
});
