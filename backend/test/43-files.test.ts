/**
 * §21 #123 Files — one shared bucket per deployment: console publish (upload → ready, cloud view), a site registers
 * (idempotent) + reports copies held, the cloud fetches a copy from a holder (fileFetches → the holder uploads →
 * stored, download with Range), a site asks the cloud for a copy, unpublish (console + agent → tombstone, 410),
 * permissions, validation, migrate-m (transfers → files).
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { randomBytes } from 'node:crypto';
import { ulid, type SharedFile } from '@opm/shared';
import { abucket, adb, enrollSite, helloDeployment, person, ports, raw, rejects, sha256Hex } from './helpers.ts';
import { applyMigrationM, planMigrationM } from '../scripts/migrate-m.ts';

const shaOf = (b: Uint8Array | string) => `sha256:${sha256Hex(b)}`;
const reg = (o: Partial<{ fileId: string; fileName: string; sizeBytes: number; sha256: string; note: string | null; via: 'browser' | 'path'; sourcePath: string | null }>, data: Buffer) => ({
  fileId: o.fileId ?? ulid(),
  fileName: o.fileName ?? 'site.bin',
  sizeBytes: o.sizeBytes ?? data.length,
  sha256: o.sha256 ?? shaOf(data),
  note: o.note ?? null,
  publishedAt: new Date().toISOString(),
  publishedBy: 'ops@example.com',
  via: o.via ?? 'browser',
  sourcePath: o.sourcePath ?? null,
});

test('files: console publish → upload → ready; cloud view; download (Range); unpublish → tombstone + 410', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const app = await enrollSite(admin, id, 'app');
  const data = randomBytes(300_000);
  const v0 = await app.api.poll({ query: { wait: 0 } });
  assert.deepEqual(v0.files, []);
  assert.deepEqual(v0.fileFetches, []);
  const p = await admin.real.publishFile({ deploymentId: id, body: { fileName: 'manual.pdf', sizeBytes: data.length, sha256: shaOf(data), note: 'read me' } });
  assert.equal(p.file.state, 'uploading');
  assert.deepEqual(p.file.origin, { kind: 'cloud' });
  assert.equal(p.file.publishedBy, admin.email);
  assert.equal(p.upload.purpose, 'file');
  assert.equal(p.upload.transferId, p.file.fileId);
  assert.ok(!('_storagePath' in p.file));
  const v1 = await app.api.poll({ query: { wait: 0 } });
  assert.equal(Number(v1.cursor), Number(v0.cursor) + 1);
  assert.equal(v1.files![0]!.state, 'uploading');
  await rejects(admin.real.downloadFile({ deploymentId: id, fileId: p.file.fileId }), 'conflict', 409);
  await rejects(admin.real.fetchFileToCloud({ deploymentId: id, fileId: p.file.fileId }), 'conflict', 409);
  await admin.real.putUploadPart({ uploadId: p.upload.uploadId, index: '0', body: data });
  const done = await admin.real.completeUpload({ uploadId: p.upload.uploadId });
  assert.equal(done.upload.status, 'complete');
  const v2 = await app.api.poll({ query: { wait: 0 } });
  assert.equal(Number(v2.cursor), Number(v1.cursor) + 1);
  const f = v2.files!.find((x) => x.fileId === p.file.fileId)!;
  assert.equal(f.state, 'ready');
  assert.deepEqual(f.cloud, { stored: true, fetch: null });
  assert.ok(!('_storagePath' in f) && !('_uploadId' in f));
  const [objs] = await abucket.getFiles({ prefix: `shared-files/${id}/${p.file.fileId}` });
  assert.equal(objs.length, 1);
  // downloads: console (activity) + Range; agent
  const dl = await admin.real.downloadFile({ deploymentId: id, fileId: p.file.fileId });
  assert.deepEqual(Buffer.from(await dl.arrayBuffer()), data);
  assert.match(dl.headers.get('content-disposition') ?? '', /manual\.pdf/);
  const r = await raw('GET', `/v1/deployments/${id}/files/${p.file.fileId}/download`, { headers: { Authorization: admin.bearer, Range: 'bytes=100-199' } });
  assert.equal(r.status, 206);
  assert.deepEqual(r.body, data.subarray(100, 200));
  const adl = await app.api.downloadAgentFile({ fileId: p.file.fileId, headers: { Range: 'bytes=-10' } });
  assert.deepEqual(Buffer.from(await adl.arrayBuffer()), data.subarray(data.length - 10));
  const acts = await admin.real.listActivities({ deploymentId: id, query: { type: 'file.' } });
  assert.deepEqual(acts.items.map((a) => a.type).sort(), ['file.downloaded', 'file.published']);
  assert.deepEqual(acts.items.find((a) => a.type === 'file.downloaded')!.data, { fileId: p.file.fileId, where: 'cloud' });
  // fetch on a stored file: unchanged
  assert.deepEqual((await admin.real.fetchFileToCloud({ deploymentId: id, fileId: p.file.fileId })).cloud, { stored: true, fetch: null });
  // list
  const l = await admin.real.listFiles({ deploymentId: id });
  assert.deepEqual(l.items.map((x) => x.fileId), [p.file.fileId]);
  // unpublish (idempotent) → tombstone in the cloud view, object gone, 410
  await admin.real.unpublishFile({ deploymentId: id, fileId: p.file.fileId });
  assert.deepEqual(await admin.real.unpublishFile({ deploymentId: id, fileId: p.file.fileId }), { deleted: true });
  const v3 = await app.api.poll({ query: { wait: 0 } });
  assert.equal(Number(v3.cursor), Number(v2.cursor) + 1, 'the second unpublish changes nothing');
  const t = v3.files!.find((x) => x.fileId === p.file.fileId)!;
  assert.equal(t.state, 'unpublished');
  assert.equal(t.unpublishedBy, admin.email);
  assert.ok(t.unpublishedAt);
  assert.deepEqual(t.cloud, { stored: false, fetch: null });
  const [objs2] = await abucket.getFiles({ prefix: `shared-files/${id}/${p.file.fileId}` });
  assert.equal(objs2.length, 0);
  await rejects(admin.real.downloadFile({ deploymentId: id, fileId: p.file.fileId }), 'gone', 410);
  await rejects(app.api.downloadAgentFile({ fileId: p.file.fileId }), 'gone', 410);
  await rejects(admin.real.fetchFileToCloud({ deploymentId: id, fileId: p.file.fileId }), 'gone', 410);
  assert.equal((await admin.real.listFiles({ deploymentId: id })).items.length, 0);
  assert.equal((await admin.real.listFiles({ deploymentId: id, query: { unpublished: true } })).items.length, 1, 'the client sends ?unpublished=1');
  assert.equal((await raw('GET', `/v1/deployments/${id}/files?unpublished=true`, { headers: { Authorization: admin.bearer } })).json.items.length, 1);
  assert.equal((await raw('GET', `/v1/deployments/${id}/files?unpublished=0`, { headers: { Authorization: admin.bearer } })).json.items.length, 0);
  assert.equal((await admin.real.listActivities({ deploymentId: id, query: { type: 'file.unpublished' } })).items.length, 1);
  // tombstones older than FILE_TOMBSTONE_DAYS leave the cloud view
  await adb.collection('deployments').doc(id).collection('files').doc(p.file.fileId).update({ unpublishedAt: new Date(Date.now() - 31 * 86400_000).toISOString() });
  assert.equal((await app.api.poll({ query: { wait: 0 } })).files!.length, 0);
  // unpublished while uploading: the open upload fails
  const p2 = await admin.real.publishFile({ deploymentId: id, body: { fileName: 'b.bin', sizeBytes: 3, sha256: shaOf('abc') } });
  await admin.real.unpublishFile({ deploymentId: id, fileId: p2.file.fileId });
  await rejects(admin.real.getUpload({ uploadId: p2.upload.uploadId }), 'gone', 410);
  await rejects(admin.real.unpublishFile({ deploymentId: id, fileId: 'nope' }), 'not-found');
  // the generic upload route does not start file uploads
  await rejects(admin.real.createUpload({ body: { purpose: 'file' as never, sizeBytes: 1, sha256: shaOf('a') } }), 'invalid');
});

test('files: a site registers (idempotent) + held; console fetch → fileFetches → the holder uploads → stored; site fetch request; agent unpublish', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const app = await enrollSite(admin, id, 'app');
  const db = await enrollSite(admin, id, 'db');
  const data = randomBytes(9 * 1024 * 1024 + 77); // two parts
  const body = reg({ fileName: 'dump.sql', note: 'nightly', via: 'path', sourcePath: '/srv/dump.sql' }, data);
  const e0 = Number((await app.api.poll({ query: { wait: 0 } })).cursor);
  const f = await app.api.registerAgentFile({ body });
  assert.equal(f.state, 'ready');
  assert.deepEqual(f.origin, { kind: 'site', siteSlug: 'app', via: 'path', sourcePath: '/srv/dump.sql' });
  assert.deepEqual(f.sites, ['app']);
  assert.equal(f.publishedBy, 'ops@example.com');
  assert.deepEqual(await app.api.registerAgentFile({ body }), f, 'idempotent');
  await rejects(db.api.registerAgentFile({ body }), 'conflict', 409);
  await rejects(app.api.registerAgentFile({ body: { ...body, sha256: shaOf('other') } }), 'conflict', 409);
  const v1 = await db.api.poll({ query: { wait: 0 } });
  assert.equal(Number(v1.cursor), e0 + 1);
  assert.equal(v1.files![0]!.fileId, f.fileId);
  assert.deepEqual(v1.fileFetches, []);
  const pub = await admin.real.listActivities({ deploymentId: id, query: { type: 'file.published' } });
  assert.equal(pub.items.length, 1);
  assert.equal(pub.items[0]!.origin, 'agent');
  // no cloud copy yet
  await rejects(admin.real.downloadFile({ deploymentId: id, fileId: f.fileId }), 'conflict', 409);
  await rejects(app.api.downloadAgentFile({ fileId: f.fileId }), 'conflict', 409);
  // db gets a copy over the mesh and reports it; the most recently seen holder is the one asked
  await db.api.reportAgentFileHeld({ fileId: f.fileId, body: { held: true } });
  assert.deepEqual((await admin.real.listFiles({ deploymentId: id })).items[0]!.sites, ['app', 'db']);
  await adb.collection('deployments').doc(id).collection('agents').doc('db').update({ lastSeenAt: new Date(Date.now() - 3600_000).toISOString() });
  await adb.collection('deployments').doc(id).collection('agents').doc('app').update({ lastSeenAt: new Date().toISOString() });
  // the asked site cannot start before being asked
  await rejects(app.api.startAgentFileUpload({ fileId: f.fileId }), 'conflict', 409);
  const fx = await admin.real.fetchFileToCloud({ deploymentId: id, fileId: f.fileId });
  assert.equal(fx.cloud.fetch!.from, 'app');
  assert.equal(fx.cloud.fetch!.requestedBy, admin.email);
  assert.equal(fx.cloud.fetch!.startedAt, null);
  const again = await admin.real.fetchFileToCloud({ deploymentId: id, fileId: f.fileId });
  assert.deepEqual(again.cloud.fetch, fx.cloud.fetch, 'idempotent while open');
  const v2 = await app.api.poll({ query: { wait: 0 } });
  assert.deepEqual(v2.fileFetches, [{ fileId: f.fileId, from: 'app' }]);
  // the holder sends its copy (agent upload endpoints)
  const st = await app.api.startAgentFileUpload({ fileId: f.fileId });
  assert.equal(st.upload.purpose, 'file');
  assert.equal(st.upload.partCount, 2);
  const resumed = await app.api.startAgentFileUpload({ fileId: f.fileId });
  assert.equal(resumed.upload.uploadId, st.upload.uploadId, 'resumes the open upload');
  const part = (i: number) => data.subarray(i * st.upload.partSize, Math.min(data.length, (i + 1) * st.upload.partSize));
  await app.api.putAgentUploadPart({ uploadId: st.upload.uploadId, index: '0', body: part(0) });
  const mid = (await admin.real.listFiles({ deploymentId: id })).items[0]!;
  assert.equal(mid.cloud.fetch!.bytesDone, st.upload.partSize);
  assert.ok(mid.cloud.fetch!.startedAt);
  await app.api.putAgentUploadPart({ uploadId: st.upload.uploadId, index: '1', body: part(1) });
  await app.api.completeAgentUpload({ uploadId: st.upload.uploadId });
  const v3 = await db.api.poll({ query: { wait: 0 } });
  assert.deepEqual(v3.fileFetches, []);
  assert.deepEqual(v3.files![0]!.cloud, { stored: true, fetch: null });
  const fetched = await admin.real.listActivities({ deploymentId: id, query: { type: 'file.fetched' } });
  assert.equal(fetched.items.length, 1);
  assert.deepEqual(fetched.items[0]!.data, { fileId: f.fileId, where: 'cloud', from: 'app' });
  const dl = await admin.real.downloadFile({ deploymentId: id, fileId: f.fileId });
  assert.equal(sha256Hex(Buffer.from(await dl.arrayBuffer())), body.sha256.slice(7));
  const rr = await raw('GET', `/v1/deployments/${id}/files/${f.fileId}/download`, { headers: { Authorization: admin.bearer, Range: 'bytes=0-9' } });
  assert.equal(rr.status, 206);
  assert.deepEqual(rr.body, data.subarray(0, 10));
  const adl = await db.api.downloadAgentFile({ fileId: f.fileId });
  assert.equal(Buffer.from(await adl.arrayBuffer()).length, data.length);
  await rejects(app.api.startAgentFileUpload({ fileId: f.fileId }), 'conflict', 409);

  // a site asks the cloud for a copy it cannot get over the mesh
  const small = randomBytes(1000);
  const g = await db.api.registerAgentFile({ body: reg({ fileName: 'g.txt' }, small) });
  await rejects(app.api.requestAgentFileFetch({ fileId: g.fileId, body: { actor: 'nope' } }), 'invalid');
  const rq = await app.api.requestAgentFileFetch({ fileId: g.fileId, body: { actor: 'ops@example.com' } });
  assert.equal(rq.cloud.fetch!.from, 'db');
  assert.equal(rq.cloud.fetch!.requestedBy, 'site:app');
  assert.deepEqual((await db.api.poll({ query: { wait: 0 } })).fileFetches, [{ fileId: g.fileId, from: 'db' }]);
  // the asked site dropped its copy (it was the only one): the fetch waits for a holder
  await db.api.reportAgentFileHeld({ fileId: g.fileId, body: { held: false } });
  const w = (await admin.real.listFiles({ deploymentId: id })).items.find((x) => x.fileId === g.fileId)!;
  assert.deepEqual(w.sites, []);
  assert.equal(w.cloud.fetch!.from, null);
  assert.deepEqual((await db.api.poll({ query: { wait: 0 } })).fileFetches, []);
  await db.api.reportAgentFileHeld({ fileId: g.fileId, body: { held: true } });
  assert.deepEqual((await db.api.poll({ query: { wait: 0 } })).fileFetches, [{ fileId: g.fileId, from: 'db' }]);
  const su = await db.api.startAgentFileUpload({ fileId: g.fileId });
  await db.api.putAgentUploadPart({ uploadId: su.upload.uploadId, index: '0', body: small });
  await db.api.completeAgentUpload({ uploadId: su.upload.uploadId });
  const got = await app.api.downloadAgentFile({ fileId: g.fileId });
  assert.deepEqual(Buffer.from(await got.arrayBuffer()), small);
  // no holder, no cloud copy → conflict
  const h = await app.api.registerAgentFile({ body: reg({ fileName: 'h.txt' }, small) });
  await app.api.reportAgentFileHeld({ fileId: h.fileId, body: { held: false } });
  await rejects(admin.real.fetchFileToCloud({ deploymentId: id, fileId: h.fileId }), 'conflict', 409);
  await rejects(app.api.reportAgentFileHeld({ fileId: 'missing', body: { held: true } }), 'not-found');

  // agent unpublish → tombstone everywhere, the cloud copy deleted
  await rejects(app.api.unpublishAgentFile({ fileId: f.fileId, body: { actor: '' } }), 'invalid');
  const e1 = Number((await db.api.poll({ query: { wait: 0 } })).cursor);
  assert.deepEqual(await app.api.unpublishAgentFile({ fileId: f.fileId, body: { actor: 'ops@example.com' } }), { deleted: true });
  const v4 = await db.api.poll({ query: { wait: 0 } });
  assert.equal(Number(v4.cursor), e1 + 1);
  const tomb = v4.files!.find((x) => x.fileId === f.fileId)!;
  assert.equal(tomb.state, 'unpublished');
  assert.equal(tomb.unpublishedBy, 'ops@example.com');
  await rejects(db.api.downloadAgentFile({ fileId: f.fileId }), 'gone', 410);
  await rejects(app.api.requestAgentFileFetch({ fileId: f.fileId, body: { actor: 'ops@example.com' } }), 'gone', 410);
  const un = await admin.real.listActivities({ deploymentId: id, query: { type: 'file.unpublished' } });
  assert.equal(un.items[0]!.origin, 'agent');
  // re-register of the unpublished file returns its tombstone
  assert.equal((await app.api.registerAgentFile({ body })).state, 'unpublished');
});

test('files: permissions + validation', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const app = await enrollSite(admin, id, 'app');
  const ok = { fileName: 'a.bin', sizeBytes: 3, sha256: shaOf('abc') };
  for (const [b, code] of [
    [{ ...ok, fileName: 'a/b' }, 'invalid'],
    [{ ...ok, fileName: 'a\\b' }, 'invalid'],
    [{ ...ok, fileName: 'a\0b' }, 'invalid'],
    [{ ...ok, fileName: '' }, 'invalid'],
    [{ ...ok, fileName: 'x'.repeat(256) }, 'invalid'],
    [{ ...ok, sizeBytes: 0 }, 'invalid'],
    [{ ...ok, sha256: 'sha256:abc' }, 'invalid'],
    [{ ...ok, note: 'n'.repeat(501) }, 'invalid'],
    [{ ...ok, extra: 1 }, 'invalid'],
    [{ ...ok, sizeBytes: 3 * 1024 ** 3 }, 'too-large'],
  ] as const)
    await rejects(admin.real.publishFile({ deploymentId: id, body: b as never }), code);
  const data = Buffer.from('abc');
  for (const b of [reg({ fileId: 'bad/id' }, data), reg({ fileId: '' }, data), { ...reg({}, data), publishedBy: 'x' }, { ...reg({}, data), via: 'mail' }, { ...reg({}, data), publishedAt: 'never' }, reg({ via: 'path', sourcePath: 'rel/path' }, data), reg({ fileName: '..' }, data)])
    await rejects(app.api.registerAgentFile({ body: b as never }), 'invalid');
  // a legacy transfer id is a valid fileId too
  assert.equal((await app.api.registerAgentFile({ body: reg({ fileId: 'AbC123xyz-legacy' }, data) })).fileId, 'AbC123xyz-legacy');

  const p = await admin.real.publishFile({ deploymentId: id, body: ok });
  const reader = await person([{ resource: 'deployment.transfers', actions: ['read'], scope: { kind: 'all' } }]);
  await rejects(reader.real.publishFile({ deploymentId: id, body: ok }), 'forbidden');
  await rejects(reader.real.unpublishFile({ deploymentId: id, fileId: p.file.fileId }), 'forbidden');
  assert.equal((await reader.real.listFiles({ deploymentId: id })).items.length, 2);
  await rejects(reader.real.downloadFile({ deploymentId: id, fileId: p.file.fileId }), 'conflict');
  const nobody = await person([{ resource: 'deployment.config', actions: ['read'], scope: { kind: 'all' } }]);
  await rejects(nobody.real.listFiles({ deploymentId: id }), 'forbidden');
  await rejects(nobody.real.fetchFileToCloud({ deploymentId: id, fileId: p.file.fileId }), 'forbidden');
  // another person cannot send the parts of the publisher's upload
  await rejects(reader.real.putUploadPart({ uploadId: p.upload.uploadId, index: '0', body: data }), 'not-found');
  // sha mismatch on complete: the upload fails, the file stays uploading
  const bad = await admin.real.publishFile({ deploymentId: id, body: { ...ok, sha256: shaOf('xyz') } });
  await admin.real.putUploadPart({ uploadId: bad.upload.uploadId, index: '0', body: data });
  await rejects(admin.real.completeUpload({ uploadId: bad.upload.uploadId }), 'invalid');
  assert.equal((await admin.real.listFiles({ deploymentId: id })).items.find((x) => x.fileId === bad.file.fileId)!.state, 'uploading');
});

test('files: migrate-m turns transfers into files (fileId = transferId, object kept, legacy transfer expired on unpublish)', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const app = await enrollSite(admin, id, 'app');
  const data = randomBytes(2000);
  // to-site, fetched by the site
  const c = await admin.api.createTransfer({ deploymentId: id, siteSlug: 'app', body: { fileName: 'to-site.bin', sizeBytes: data.length, sha256: shaOf(data), note: 'n1' } });
  await admin.api.putUploadPart({ uploadId: c.upload.uploadId, index: '0', body: data });
  await admin.api.completeUpload({ uploadId: c.upload.uploadId });
  await app.api.reportAgentTransfer({ transferId: c.transfer.transferId, body: { status: 'fetched', actor: 'ops@example.com' } });
  // to-cloud
  const up = randomBytes(500);
  const ac = await app.api.createAgentTransfer({ body: { fileName: 'to-cloud.bin', sizeBytes: up.length, sha256: shaOf(up), actor: 'ops@example.com' } });
  await app.api.putAgentUploadPart({ uploadId: ac.upload.uploadId, index: '0', body: up });
  await app.api.completeAgentUpload({ uploadId: ac.upload.uploadId });
  // still uploading + deleted → skipped
  const open = await admin.api.createTransfer({ deploymentId: id, siteSlug: 'app', body: { fileName: 'open.bin', sizeBytes: 3, sha256: shaOf('abc') } });
  const del = await admin.api.createTransfer({ deploymentId: id, siteSlug: 'app', body: { fileName: 'del.bin', sizeBytes: 3, sha256: shaOf('abc') } });
  await admin.api.deleteTransfer({ deploymentId: id, transferId: del.transfer.transferId });

  const plan = await planMigrationM(adb, ports.blobs, { deploymentId: id });
  const by = (tid: string) => plan.items.find((i) => i.transferId === tid)!;
  assert.equal(by(open.transfer.transferId).action, 'skip');
  assert.equal(by(del.transfer.transferId).action, 'skip');
  assert.equal(by(c.transfer.transferId).action, 'create');
  assert.equal((await adb.collection('deployments').doc(id).collection('files').get()).size, 0, 'the plan writes nothing');
  const e0 = Number((await app.api.poll({ query: { wait: 0 } })).cursor);
  const r = await applyMigrationM(adb, plan);
  assert.equal(r.applied, 2);
  const v = await app.api.poll({ query: { wait: 0 } });
  assert.equal(Number(v.cursor), e0 + 1);
  const fs = (tid: string) => v.files!.find((x) => x.fileId === tid) as SharedFile;
  const a = fs(c.transfer.transferId);
  assert.deepEqual(a.origin, { kind: 'cloud' });
  assert.equal(a.publishedBy, admin.email);
  assert.equal(a.note, 'n1');
  assert.deepEqual(a.sites, ['app']);
  assert.deepEqual(a.cloud, { stored: true, fetch: null });
  const b = fs(ac.transfer.transferId);
  assert.deepEqual(b.origin, { kind: 'site', siteSlug: 'app', via: 'browser', sourcePath: null });
  assert.equal(b.publishedBy, 'ops@example.com');
  assert.equal(b.note, null);
  // the old object serves the file
  const dl = await admin.real.downloadFile({ deploymentId: id, fileId: a.fileId });
  assert.deepEqual(Buffer.from(await dl.arrayBuffer()), data);
  // rerun: nothing to do
  const again = await planMigrationM(adb, ports.blobs, { deploymentId: id });
  assert.equal(again.counts.create, 0);
  assert.equal(again.counts.exists, 2);
  // unpublishing a migrated file expires its transfer (old agents drop the copy) and deletes the object
  await admin.real.unpublishFile({ deploymentId: id, fileId: a.fileId });
  const vd = await app.api.poll({ query: { wait: 0 } });
  assert.ok(vd.deletedTransfersToSite!.some((t) => t.transferId === c.transfer.transferId));
  await rejects(admin.api.downloadTransfer({ deploymentId: id, transferId: c.transfer.transferId }), 'gone', 410);
  const [objs] = await abucket.getFiles({ prefix: `transfers/${id}/${c.transfer.transferId}` });
  assert.equal(objs.length, 0);
});
