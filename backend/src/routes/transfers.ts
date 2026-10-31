/** Human transfers: list / send to a site / download from a site / delete (SPEC-final §6.1 Transfers, §14.7). */
import { RE, TRANSFER_MAX, type CreateTransferRequest, type Transfer } from '@opm/shared';
import { col, db, nowIso } from '../core/fire.js';
import { E } from '../core/errors.js';
import { loadDep, needDep } from '../core/perm.js';
import { asObject, hexOf, onlyKeys, V } from '../core/util.js';
import { decodeDeployment } from '../core/codec.js';
import { CloudLog } from '../core/activities.js';
import { newUpload, uploadView } from '../core/uploads.js';
import { paths, remove, serveObject } from '../core/storage.js';
import type { Handlers } from '../core/router.js';

export function validateTransferBody(b: Record<string, unknown>): CreateTransferRequest {
  const v = new V();
  const fn = b.fileName;
  if (typeof fn !== 'string' || fn.length < 1 || fn.length > 255 || /[/\\\0]/.test(fn) || fn === '.' || fn === '..') v.err('fileName', 'V_FORMAT', 'fileName must be 1–255 characters without / \\ or NUL');
  if (!Number.isInteger(b.sizeBytes) || (b.sizeBytes as number) < 0) v.err('sizeBytes', 'V_RANGE', 'sizeBytes must be an integer ≥ 0');
  if (typeof b.sha256 !== 'string' || !RE.sha256.test(b.sha256)) v.err('sha256', 'V_FORMAT', 'sha256 must be "sha256:" + 64 hex');
  if (b.note !== undefined && (typeof b.note !== 'string' || b.note.length > 500)) v.err('note', 'V_LENGTH', 'note must be ≤ 500 characters');
  v.throwIf();
  if ((b.sizeBytes as number) > TRANSFER_MAX) throw E.tooLarge(`transfers are at most ${TRANSFER_MAX} bytes`);
  return b as unknown as CreateTransferRequest;
}
export function newTransfer(o: { deploymentId: string; siteSlug: string; direction: Transfer['direction']; body: CreateTransferRequest; createdBy: string; uploadId: string }): Transfer {
  const ref = col.transfers(o.deploymentId).doc();
  const now = Date.now();
  return {
    transferId: ref.id,
    deploymentId: o.deploymentId,
    siteSlug: o.siteSlug,
    direction: o.direction,
    fileName: o.body.fileName,
    sizeBytes: o.body.sizeBytes,
    sha256: o.body.sha256,
    storagePath: paths.transfer(o.deploymentId, ref.id),
    uploadId: o.uploadId,
    createdAt: new Date(now).toISOString(),
    createdBy: o.createdBy,
    note: o.body.note ?? '',
    expiresAt: null, // transfers never expire; only Delete removes one
    status: 'uploading',
    savedPath: null,
    error: null,
    fetchedAt: null, // §21 #100: set when the site's agent has the file (to-site)
  };
}

export const transferHandlers: Pick<Handlers, 'listTransfers' | 'createTransfer' | 'downloadTransfer' | 'deleteTransfer'> = {
  async listTransfers(c) {
    const dep = await loadDep(c);
    needDep(c, dep, 'deployment.transfers', 'read');
    const direction = c.query.get('direction');
    if (direction !== 'to-site' && direction !== 'to-cloud') throw E.bad('direction', 'V_ENUM', 'direction must be to-site | to-cloud');
    const q = await col.transfers(dep.deploymentId).where('siteSlug', '==', c.params.siteSlug!).where('direction', '==', direction).orderBy('createdAt', 'desc').limit(500).get();
    return { items: q.docs.map((d) => d.data() as Transfer) };
  },
  async createTransfer(c) {
    const dep = await loadDep(c);
    needDep(c, dep, 'deployment.transfers', 'write');
    const slug = c.params.siteSlug!;
    const known = dep.draft.sites.some((s) => s.siteSlug === slug) || (await col.agents(dep.deploymentId).doc(slug).get()).exists;
    if (!known) throw E.notFound(`site ${slug} is not in the deployment`);
    const b = asObject(c.json());
    onlyKeys(b, ['fileName', 'sizeBytes', 'sha256', 'note']);
    const body = validateTransferBody(b);
    const tRef = col.transfers(dep.deploymentId).doc();
    const upload = newUpload({ purpose: 'transfer', deploymentId: dep.deploymentId, transferId: tRef.id, sizeBytes: body.sizeBytes, sha256: body.sha256, createdBy: c.account!.uid, creatorSite: null, target: paths.transfer(dep.deploymentId, tRef.id) });
    const t = { ...newTransfer({ deploymentId: dep.deploymentId, siteSlug: slug, direction: 'to-site', body, createdBy: c.account!.uid, uploadId: upload.uploadId }), transferId: tRef.id, storagePath: paths.transfer(dep.deploymentId, tRef.id) };
    const depRef = col.deployments().doc(dep.deploymentId);
    await db().runTransaction(async (tx) => {
      const d = decodeDeployment((await tx.get(depRef)).data()!);
      tx.create(tRef, t);
      tx.create(col.uploads().doc(upload.uploadId), upload);
      const log = new CloudLog(d.deploymentId, d);
      log.add(tx, {
        type: 'transfer.sent',
        summary: `Sending ${t.fileName} to site ${slug} started (${t.sizeBytes} bytes)`,
        data: { stage: 'started', transferId: t.transferId, direction: t.direction, fileName: t.fileName, sizeBytes: t.sizeBytes, siteSlug: slug, uploadId: upload.uploadId },
        actor: c.account!.uid,
      });
      tx.update(depRef, log.depUpdate());
    });
    return { transfer: t, upload: uploadView(upload) };
  },
  async downloadTransfer(c) {
    const dep = await loadDep(c);
    needDep(c, dep, 'deployment.transfers', 'read');
    const tRef = col.transfers(dep.deploymentId).doc(c.params.transferId!);
    const s = await tRef.get();
    if (!s.exists) throw E.notFound('transfer not found');
    const t = s.data() as Transfer;
    if (t.status === 'expired') throw E.gone('the transfer was deleted');
    if (t.status === 'uploading') throw E.notFound('the transfer is still uploading');
    if (t.direction === 'to-cloud' && t.status === 'ready') {
      const depRef = col.deployments().doc(dep.deploymentId);
      await db().runTransaction(async (tx) => {
        const [ds, ts] = await Promise.all([tx.get(depRef), tx.get(tRef)]);
        const cur = ts.data() as Transfer;
        if (cur.status !== 'ready') return;
        const d = decodeDeployment(ds.data()!);
        const log = new CloudLog(d.deploymentId, d);
        tx.update(tRef, { status: 'downloaded' });
        log.add(tx, { type: 'transfer.received', summary: `File ${cur.fileName} from site ${cur.siteSlug} downloaded`, data: { transferId: cur.transferId, direction: cur.direction, fileName: cur.fileName, sizeBytes: cur.sizeBytes, siteSlug: cur.siteSlug }, actor: c.account!.uid });
        tx.update(depRef, log.depUpdate());
      });
    }
    return serveObject(t.storagePath, { range: c.headers['range'], fileName: t.fileName, headers: { 'X-OPM-Sha256': hexOf(t.sha256) } });
  },
  async deleteTransfer(c) {
    const dep = await loadDep(c);
    needDep(c, dep, 'deployment.transfers', 'write');
    const tRef = col.transfers(dep.deploymentId).doc(c.params.transferId!);
    const depRef = col.deployments().doc(dep.deploymentId);
    const t = await db().runTransaction(async (tx) => {
      const [ds, ts] = await Promise.all([tx.get(depRef), tx.get(tRef)]);
      if (!ts.exists) throw E.notFound('transfer not found');
      const cur = ts.data() as Transfer;
      const d = decodeDeployment(ds.data()!);
      tx.update(tRef, { status: 'expired', error: cur.status === 'expired' ? cur.error : 'deleted' });
      const log = new CloudLog(d.deploymentId, d);
      log.add(tx, {
        type: 'transfer.deleted',
        summary: `Transfer ${cur.fileName} (${cur.direction === 'to-site' ? `to site ${cur.siteSlug}` : `from site ${cur.siteSlug}`}) deleted`,
        data: { transferId: cur.transferId, direction: cur.direction, fileName: cur.fileName, sizeBytes: cur.sizeBytes, siteSlug: cur.siteSlug, previousStatus: cur.status },
        actor: c.account!.uid,
      });
      tx.update(depRef, { ...log.depUpdate(), ...(cur.direction === 'to-site' && cur.status !== 'expired' ? { agentEpoch: d.agentEpoch + 1 } : {}) });
      if (cur.uploadId && cur.status === 'uploading') tx.update(col.uploads().doc(cur.uploadId), { status: 'failed', error: 'the transfer was deleted' });
      return cur;
    });
    await remove(t.storagePath).catch(() => undefined);
    void nowIso;
    return { deleted: true };
  },
};
