import { test } from 'node:test';
import assert from 'node:assert/strict';
import { randomBytes } from 'node:crypto';
import { UPLOAD_PART_SIZE } from '@opm/shared';
import { abucket, adb, enrollSite, helloDeployment, person, raw, rejects, sha256Hex } from './helpers.ts';
import { runDaily } from '../functions/src/jobs/daily.ts';

test('blobs: small upload, sha header check, dedupe, download + Range, permissions', async () => {
  const admin = await person('admin');
  const data = randomBytes(1000);
  const hex = sha256Hex(data);
  await rejects(admin.api.uploadBlob({ body: data, headers: { 'X-OPM-Sha256': 'sha256:' + '0'.repeat(64) } }), 'invalid');
  const b = await admin.api.uploadBlob({ body: data, headers: { 'X-OPM-Sha256': `sha256:${hex}` } });
  assert.deepEqual(b, { sha256: `sha256:${hex}`, sizeBytes: 1000 });
  assert.deepEqual(await admin.api.uploadBlob({ body: data }), b, 'idempotent');
  const g = await admin.api.getBlob({ sha256: b.sha256 });
  assert.deepEqual(Buffer.from(await g.arrayBuffer()), data);
  const r = await raw('GET', `/v1/blobs/${hex}`, { headers: { Authorization: admin.bearer, Range: 'bytes=-10' } });
  assert.equal(r.status, 206);
  assert.deepEqual(r.body, data.subarray(990));
  const bad = await raw('GET', `/v1/blobs/${hex}`, { headers: { Authorization: admin.bearer, Range: 'bytes=5000-' } });
  assert.equal(bad.status, 416);
  await rejects(admin.api.getBlob({ sha256: 'sha256:' + 'b'.repeat(64) }), 'not-found');
  const nobody = await person([]);
  await rejects(nobody.api.uploadBlob({ body: data }), 'forbidden');
  await rejects(nobody.api.getBlob({ sha256: b.sha256 }), 'forbidden');
  const cfgWriter = await person([{ resource: 'deployment.config', actions: ['write'], scope: { kind: 'tags', tagSlugs: ['x'] } }]);
  assert.equal((await cfgWriter.api.uploadBlob({ body: data })).sha256, b.sha256, 'config.write on any deployment may upload');
});

test('uploads: multi-part ≥ 24 MiB (8 MiB parts), resume state, exact part sizes, missing parts, sha mismatch', async () => {
  const admin = await person('admin');
  const size = 3 * UPLOAD_PART_SIZE + 12345; // 24 MiB + 12345 B → 4 parts
  const data = randomBytes(size);
  const sha = `sha256:${sha256Hex(data)}`;
  await rejects(admin.api.createUpload({ body: { purpose: 'blob', sizeBytes: 51 * 1024 * 1024, sha256: sha } }), 'too-large', 413);
  await rejects(admin.api.createUpload({ body: { purpose: 'transfer' as never, sizeBytes: 1, sha256: sha } }), 'invalid');
  const u = await admin.api.createUpload({ body: { purpose: 'blob', sizeBytes: size, sha256: sha } });
  assert.equal(u.partCount, 4);
  assert.equal(u.partSize, UPLOAD_PART_SIZE);
  assert.equal(u.status, 'open');
  const part = (i: number) => data.subarray(i * UPLOAD_PART_SIZE, Math.min(size, (i + 1) * UPLOAD_PART_SIZE));
  await rejects(admin.api.putUploadPart({ uploadId: u.uploadId, index: '0', body: part(0).subarray(1) }), 'invalid');
  await rejects(admin.api.putUploadPart({ uploadId: u.uploadId, index: '9', body: part(0) }), 'invalid');
  assert.deepEqual((await admin.api.putUploadPart({ uploadId: u.uploadId, index: '0', body: part(0) })).received, [0]);
  assert.deepEqual((await admin.api.putUploadPart({ uploadId: u.uploadId, index: '0', body: part(0) })).received, [0], 'idempotent');
  await admin.api.putUploadPart({ uploadId: u.uploadId, index: '3', body: part(3) });
  const e = await rejects(admin.api.completeUpload({ uploadId: u.uploadId }), 'invalid');
  assert.deepEqual(e.details.missing, [1, 2]);
  assert.deepEqual((await admin.api.getUpload({ uploadId: u.uploadId })).partsReceived, [0, 3]);
  await admin.api.putUploadPart({ uploadId: u.uploadId, index: '1', body: part(1) });
  await admin.api.putUploadPart({ uploadId: u.uploadId, index: '2', body: part(2) });
  const done = await admin.api.completeUpload({ uploadId: u.uploadId });
  assert.equal(done.upload.status, 'complete');
  assert.deepEqual(done.blob, { sha256: sha, sizeBytes: size });
  const got = await admin.api.getBlob({ sha256: sha });
  assert.equal(sha256Hex(Buffer.from(await got.arrayBuffer())), sha.slice(7));
  const [parts] = await abucket.getFiles({ prefix: `uploads/${u.uploadId}/` });
  assert.equal(parts.length, 0, 'parts deleted');
  // other people cannot touch the upload
  const other = await person('admin');
  await rejects(other.api.getUpload({ uploadId: u.uploadId }), 'not-found');
  // sha mismatch → failed
  const small = randomBytes(100);
  const u2 = await admin.api.createUpload({ body: { purpose: 'blob', sizeBytes: 100, sha256: `sha256:${'c'.repeat(64)}` } });
  await admin.api.putUploadPart({ uploadId: u2.uploadId, index: '0', body: small });
  await rejects(admin.api.completeUpload({ uploadId: u2.uploadId }), 'invalid');
  await rejects(admin.api.getUpload({ uploadId: u2.uploadId }), 'gone', 410);
  // zero-byte upload = one empty part
  const z = await admin.api.createUpload({ body: { purpose: 'blob', sizeBytes: 0, sha256: `sha256:${sha256Hex('')}` } });
  assert.equal(z.partCount, 1);
  await admin.api.putUploadPart({ uploadId: z.uploadId, index: '0', body: new Uint8Array() });
  assert.equal((await admin.api.completeUpload({ uploadId: z.uploadId })).upload.status, 'complete');
});

test('transfers: cloud → site (ready, cloud view, agent download, saved) and site → cloud (download → downloaded); delete; daily expiry', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const app = await enrollSite(admin, id, 'app');
  const data = randomBytes(200_000);
  const sha = `sha256:${sha256Hex(data)}`;
  await rejects(admin.api.createTransfer({ deploymentId: id, siteSlug: 'app', body: { fileName: 'a/b', sizeBytes: 1, sha256: sha } }), 'invalid');
  await rejects(admin.api.createTransfer({ deploymentId: id, siteSlug: 'app', body: { fileName: 'x', sizeBytes: 3 * 1024 ** 3, sha256: sha } }), 'too-large');
  await rejects(admin.api.createTransfer({ deploymentId: id, siteSlug: 'zzz', body: { fileName: 'x', sizeBytes: 1, sha256: sha } }), 'not-found');
  const c = await admin.api.createTransfer({ deploymentId: id, siteSlug: 'app', body: { fileName: 'manual.pdf', sizeBytes: data.length, sha256: sha, note: 'read me' } });
  assert.equal(c.transfer.status, 'uploading');
  assert.equal(c.transfer.storagePath, `transfers/${id}/${c.transfer.transferId}`);
  assert.equal(c.upload.purpose, 'transfer');
  const v0 = await app.api.poll({ query: { wait: 0 } });
  assert.equal(v0.transfersToSite.length, 0);
  await admin.api.putUploadPart({ uploadId: c.upload.uploadId, index: '0', body: data });
  const done = await admin.api.completeUpload({ uploadId: c.upload.uploadId });
  assert.equal(done.transfer?.status, 'ready');
  const v1 = await app.api.poll({ query: { wait: 0 } });
  assert.equal(Number(v1.cursor), Number(v0.cursor) + 1);
  assert.equal(v1.transfersToSite[0]!.transferId, c.transfer.transferId);
  const lt = await app.api.listAgentTransfers({ query: { direction: 'to-site' } });
  assert.equal(lt.items.length, 1);
  const dl = await app.api.downloadAgentTransfer({ transferId: c.transfer.transferId });
  assert.equal(sha256Hex(Buffer.from(await dl.arrayBuffer())), sha.slice(7));
  await rejects(app.api.reportAgentTransfer({ transferId: c.transfer.transferId, body: { status: 'saved-on-server', actor: 'ops@example.com' } }), 'invalid');
  await app.api.reportAgentTransfer({ transferId: c.transfer.transferId, body: { status: 'saved-on-server', savedPath: '/srv/manual.pdf', actor: 'ops@example.com' } });
  const lst = await admin.api.listTransfers({ deploymentId: id, siteSlug: 'app', query: { direction: 'to-site' } });
  assert.equal(lst.items[0]!.status, 'saved-on-server');
  assert.equal(lst.items[0]!.savedPath, '/srv/manual.pdf');
  const acts = await admin.api.listActivities({ deploymentId: id, query: { type: 'transfer.' } });
  // transfer.sent twice: stage "started" (createTransfer) and stage "uploaded" (complete → ready)
  assert.deepEqual(acts.items.map((a) => a.type).sort(), ['transfer.saved', 'transfer.sent', 'transfer.sent']);
  assert.deepEqual(acts.items.filter((a) => a.type === 'transfer.sent').map((a) => a.data.stage).sort(), ['started', 'uploaded']);
  // site → cloud
  const up = randomBytes(5000);
  const usha = `sha256:${sha256Hex(up)}`;
  await rejects(app.api.createAgentTransfer({ body: { fileName: 'dump.sql', sizeBytes: up.length, sha256: usha, actor: 'nope' } }), 'invalid');
  const ac = await app.api.createAgentTransfer({ body: { fileName: 'dump.sql', sizeBytes: up.length, sha256: usha, actor: 'ops@example.com', note: 'db dump' } });
  assert.equal(ac.transfer.direction, 'to-cloud');
  assert.equal(ac.transfer.createdBy, 'ops@example.com');
  await rejects(admin.api.putUploadPart({ uploadId: ac.upload.uploadId, index: '0', body: up }), 'not-found', 404);
  await app.api.putAgentUploadPart({ uploadId: ac.upload.uploadId, index: '0', body: up });
  assert.deepEqual((await app.api.getAgentUpload({ uploadId: ac.upload.uploadId })).partsReceived, [0]);
  const acd = await app.api.completeAgentUpload({ uploadId: ac.upload.uploadId });
  assert.equal(acd.transfer?.status, 'ready');
  await app.api.reportAgentTransfer({ transferId: ac.transfer.transferId, body: { status: 'ready', actor: 'ops@example.com' } });
  const hd = await admin.api.downloadTransfer({ deploymentId: id, transferId: ac.transfer.transferId });
  assert.deepEqual(Buffer.from(await hd.arrayBuffer()), up);
  assert.match(hd.headers.get('content-disposition') ?? '', /dump\.sql/);
  const tc = await admin.api.listTransfers({ deploymentId: id, siteSlug: 'app', query: { direction: 'to-cloud' } });
  assert.equal(tc.items[0]!.status, 'downloaded');
  const recv = await admin.api.listActivities({ deploymentId: id, query: { type: 'transfer.received' } });
  assert.equal(recv.items.length, 1);
  // delete
  const c2 = await admin.api.createTransfer({ deploymentId: id, siteSlug: 'app', body: { fileName: 'b.bin', sizeBytes: 3, sha256: `sha256:${sha256Hex('abc')}` } });
  await admin.api.putUploadPart({ uploadId: c2.upload.uploadId, index: '0', body: new TextEncoder().encode('abc') });
  await admin.api.completeUpload({ uploadId: c2.upload.uploadId });
  const e0 = (await app.api.poll({ query: { wait: 0 } })).cursor;
  await admin.api.deleteTransfer({ deploymentId: id, transferId: c2.transfer.transferId });
  const vd = await app.api.poll({ query: { wait: 0 } });
  assert.equal(Number(vd.cursor), Number(e0) + 1);
  // the agents learn of the deletion, so they drop their copy too
  assert.deepEqual(vd.deletedTransfersToSite, [{ transferId: c2.transfer.transferId, siteSlug: 'app' }]);
  assert.ok(!vd.transfersToSite.some((t) => t.transferId === c2.transfer.transferId));
  await rejects(admin.api.downloadTransfer({ deploymentId: id, transferId: c2.transfer.transferId }), 'gone', 410);
  const [objs] = await abucket.getFiles({ prefix: `transfers/${id}/${c2.transfer.transferId}` });
  assert.equal(objs.length, 0);
  // daily job: transfers never expire — even a row with a past expiresAt (written before the expiry was removed)
  // survives and still downloads; an old upload + old uptime are cleaned
  await adb.collection('deployments').doc(id).collection('transfers').doc(ac.transfer.transferId).update({ expiresAt: new Date(Date.now() - 1000).toISOString() });
  const stale = await admin.api.createUpload({ body: { purpose: 'blob', sizeBytes: 10, sha256: `sha256:${'d'.repeat(64)}` } });
  await adb.collection('uploads').doc(stale.uploadId).update({ expiresAt: new Date(Date.now() - 1000).toISOString() });
  await adb.collection('deployments').doc(id).collection('uptime').doc('app_20000101').set({ siteSlug: 'app', date: '20000101', beats: Array(24).fill(0), hours: Array(24).fill(0), updatedAt: '' });
  const r = await runDaily();
  assert.ok(r.uploadsExpired >= 1);
  assert.ok(r.uptimeDeleted >= 1);
  await rejects(admin.api.getUpload({ uploadId: stale.uploadId }), 'gone');
  assert.equal((await admin.api.downloadTransfer({ deploymentId: id, transferId: ac.transfer.transferId })).status, 200);
  assert.notEqual((await adb.collection('deployments').doc(id).collection('transfers').doc(ac.transfer.transferId).get()).data()!.status, 'expired');
  assert.equal(ac.transfer.expiresAt, null);
  assert.equal((await adb.collection('deployments').doc(id).collection('uptime').doc('app_20000101').get()).exists, false);
  // permission: transfers.read cannot create
  const reader = await person([{ resource: 'deployment.transfers', actions: ['read'], scope: { kind: 'all' } }]);
  await rejects(reader.api.createTransfer({ deploymentId: id, siteSlug: 'app', body: { fileName: 'x', sizeBytes: 1, sha256: sha } }), 'forbidden');
  assert.ok((await reader.api.listTransfers({ deploymentId: id, siteSlug: 'app', query: { direction: 'to-site' } })).items.length >= 1);
});

// §21 #100 store-and-forward: nobody needs to be on the receiving end, in either direction.
test('transfers §21 #100: fetched → fetchedAt (once, idempotent); to-cloud completes with nobody viewing; to-site waits in the cloud view', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const app = await enrollSite(admin, id, 'app');
  // cloud → site: the operator uploads and leaves; the site has not polled yet
  const data = randomBytes(3000);
  const c = await admin.api.createTransfer({ deploymentId: id, siteSlug: 'app', body: { fileName: 'later.bin', sizeBytes: data.length, sha256: `sha256:${sha256Hex(data)}` } });
  assert.equal(c.transfer.fetchedAt, null);
  await rejects(app.api.reportAgentTransfer({ transferId: c.transfer.transferId, body: { status: 'fetched', actor: 'ops@example.com' } }), 'invalid'); // still uploading
  await admin.api.putUploadPart({ uploadId: c.upload.uploadId, index: '0', body: data });
  await admin.api.completeUpload({ uploadId: c.upload.uploadId });
  // the agent comes online later: the file is still in its cloud view and downloadable
  for (let i = 0; i < 2; i++) {
    const v = await app.api.poll({ query: { wait: 0 } });
    assert.deepEqual(v.transfersToSite.map((t) => t.transferId), [c.transfer.transferId]);
  }
  const dl = await app.api.downloadAgentTransfer({ transferId: c.transfer.transferId });
  assert.deepEqual(Buffer.from(await dl.arrayBuffer()), data);
  await app.api.reportAgentTransfer({ transferId: c.transfer.transferId, body: { status: 'fetched', actor: 'ops@example.com' } });
  const l1 = (await admin.api.listTransfers({ deploymentId: id, siteSlug: 'app', query: { direction: 'to-site' } })).items[0]!;
  assert.equal(l1.status, 'ready', 'fetched does not change the status');
  assert.ok(l1.fetchedAt);
  await new Promise((r) => setTimeout(r, 20));
  await app.api.reportAgentTransfer({ transferId: c.transfer.transferId, body: { status: 'fetched', actor: 'ops@example.com' } });
  const l2 = (await admin.api.listTransfers({ deploymentId: id, siteSlug: 'app', query: { direction: 'to-site' } })).items[0]!;
  assert.equal(l2.fetchedAt, l1.fetchedAt, 'set once');
  const acts = await admin.api.listActivities({ deploymentId: id, query: { type: 'transfer.' } });
  assert.equal(acts.items.filter((a) => a.type === 'transfer.received' || a.type === 'transfer.saved').length, 0, 'fetched writes no activity');
  // saved-on-server sets fetchedAt when an older report never came
  const c2 = await admin.api.createTransfer({ deploymentId: id, siteSlug: 'app', body: { fileName: 'b.txt', sizeBytes: 3, sha256: `sha256:${sha256Hex('abc')}` } });
  await admin.api.putUploadPart({ uploadId: c2.upload.uploadId, index: '0', body: new TextEncoder().encode('abc') });
  await admin.api.completeUpload({ uploadId: c2.upload.uploadId });
  await app.api.reportAgentTransfer({ transferId: c2.transfer.transferId, body: { status: 'saved-on-server', savedPath: '/srv/b.txt', actor: 'ops@example.com' } });
  const s2 = (await admin.api.listTransfers({ deploymentId: id, siteSlug: 'app', query: { direction: 'to-site' } })).items.find((t) => t.transferId === c2.transfer.transferId)!;
  assert.equal(s2.status, 'saved-on-server');
  assert.ok(s2.fetchedAt);
  // site → cloud: create + part + complete with no human call in between; then a human list shows it ready
  const up = randomBytes(4000);
  const ac = await app.api.createAgentTransfer({ body: { fileName: 'nobody.log', sizeBytes: up.length, sha256: `sha256:${sha256Hex(up)}`, actor: 'ops@example.com' } });
  await app.api.putAgentUploadPart({ uploadId: ac.upload.uploadId, index: '0', body: up });
  assert.equal((await app.api.completeAgentUpload({ uploadId: ac.upload.uploadId })).transfer?.status, 'ready');
  await rejects(app.api.reportAgentTransfer({ transferId: ac.transfer.transferId, body: { status: 'fetched', actor: 'ops@example.com' } }), 'invalid'); // to-cloud
  const tc = (await admin.api.listTransfers({ deploymentId: id, siteSlug: 'app', query: { direction: 'to-cloud' } })).items;
  assert.equal(tc.find((t) => t.transferId === ac.transfer.transferId)?.status, 'ready');
  const hd = await admin.api.downloadTransfer({ deploymentId: id, transferId: ac.transfer.transferId });
  assert.deepEqual(Buffer.from(await hd.arrayBuffer()), up);
});
