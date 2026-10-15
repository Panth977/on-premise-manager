/**
 * Multi-part uploads through the backend (decision 14): 8 MiB parts in uploads/<id>/part-<n>, joined on complete
 * (GCS compose ≤ 32 per call, iteratively; concat fallback e.g. on the emulator), then stream-verified (size + sha256).
 */
import { FieldValue } from '../ports/index.js';
import { PassThrough } from 'node:stream';
import { pipeline } from 'node:stream/promises';
import { UPLOAD_PART_SIZE, type CompleteUploadResponse, type Transfer, type Upload, type UploadPurpose } from '@opm/shared';
import { col, db, isoPlus, nowIso, pub, type Tx } from './fire.js';
import { blobs } from '../ports/index.js';
import { ApiError, E } from './errors.js';
import { env } from './env.js';
import { hashObject, paths, remove, removePrefix, streamToStorage, exists, sizeOf } from './storage.js';
import { hexOf } from './util.js';
import { CloudLog } from './activities.js';
import { decodeDeployment } from './codec.js';
import { logger } from '../ports/index.js';

export type StoredUpload = Upload & { _creatorSite: string | null; _target: string; /** round E: CreateUploadRequest purpose 'seed-archive' (≤ SEED_ARCHIVE_MAX, gzip) */ _seedArchive?: boolean; /** §21 #107: joined into this commit stage (target staging/<id>/<hex>) */ _stageId?: string };
export const UPLOAD_TTL_MS = 24 * 3600_000;

export function newUpload(o: { purpose: UploadPurpose; seedArchive?: boolean; deploymentId: string | null; transferId: string | null; sizeBytes: number; sha256: string; createdBy: string; creatorSite: string | null; target: string; stageId?: string | null }): StoredUpload {
  const ref = col.uploads().doc();
  return {
    uploadId: ref.id,
    purpose: o.purpose,
    deploymentId: o.deploymentId,
    transferId: o.transferId,
    sizeBytes: o.sizeBytes,
    sha256: o.sha256,
    partSize: UPLOAD_PART_SIZE,
    partCount: Math.max(1, Math.ceil(o.sizeBytes / UPLOAD_PART_SIZE)),
    partsReceived: [],
    status: 'open',
    error: null,
    createdAt: nowIso(),
    createdBy: o.createdBy,
    expiresAt: isoPlus(UPLOAD_TTL_MS),
    _creatorSite: o.creatorSite,
    _target: o.target,
    ...(o.seedArchive ? { _seedArchive: true } : {}),
    ...(o.stageId ? { _stageId: o.stageId } : {}),
  };
}
export const uploadView = (u: StoredUpload): Upload => ({ ...pub<Upload>(u as unknown as Record<string, unknown>), partsReceived: [...(u.partsReceived ?? [])].sort((a, b) => a - b) });

export type Creator = { kind: 'human'; uid: string } | { kind: 'agent'; deploymentId: string; siteSlug: string };
export async function loadUpload(id: string, who: Creator, opts: { allowClosed?: boolean } = {}): Promise<StoredUpload> {
  const s = await col.uploads().doc(id).get();
  if (!s.exists) throw E.notFound('upload not found');
  const u = s.data() as StoredUpload;
  const mine = who.kind === 'human' ? !u._creatorSite && u.createdBy === who.uid : u._creatorSite === who.siteSlug && u.deploymentId === who.deploymentId;
  if (!mine) throw E.notFound('upload not found');
  if (!opts.allowClosed) {
    if (u.status === 'failed') throw E.gone(`the upload failed${u.error ? `: ${u.error}` : ''}`);
    if (Date.parse(u.expiresAt) <= Date.now() && u.status === 'open') throw E.gone('the upload expired');
  }
  return u;
}

export function expectedPartSize(u: Upload, index: number): number {
  if (index < u.partCount - 1) return u.partSize;
  return u.sizeBytes - u.partSize * (u.partCount - 1);
}
export async function putPart(u: StoredUpload, indexStr: string, body: Buffer): Promise<{ received: number[] }> {
  if (!/^\d+$/.test(indexStr)) throw E.bad('index', 'V_FORMAT', 'index must be an integer');
  const index = Number(indexStr);
  if (index >= u.partCount) throw E.bad('index', 'V_RANGE', `index must be 0–${u.partCount - 1}`);
  if (u.status === 'complete') throw E.gone('the upload is complete');
  const want = expectedPartSize(u, index);
  if (body.length !== want) throw E.bad('body', 'V_LENGTH', `part ${index} must be exactly ${want} bytes (got ${body.length})`);
  // round E: a seed archive must be gzip (.tar.gz); checked on the first part (magic 1f 8b)
  if (u._seedArchive && index === 0 && !(body[0] === 0x1f && body[1] === 0x8b)) throw E.bad('body', 'V_FORMAT', 'a seed archive must be a .tar.gz (gzip) file');
  await blobs().put(paths.uploadPart(u.uploadId, index), body, { contentType: 'application/octet-stream' });
  await col.uploads().doc(u.uploadId).update({ partsReceived: FieldValue.arrayUnion(index) });
  const set = new Set([...(u.partsReceived ?? []), index]);
  return { received: [...set].sort((a, b) => a - b) };
}

async function concatParts(u: StoredUpload, target: string, expectHex: string) {
  const out = new PassThrough();
  const writing = streamToStorage(target, out, expectHex);
  try {
    for (let i = 0; i < u.partCount; i++) await pipeline(blobs().stream(paths.uploadPart(u.uploadId, i)), out, { end: false });
    out.end();
  } catch (e) {
    out.destroy(e as Error);
    await writing.catch(() => undefined);
    throw e;
  }
  return writing;
}
async function composeParts(u: StoredUpload, target: string) {
  const b = blobs();
  const parts = Array.from({ length: u.partCount }, (_, i) => paths.uploadPart(u.uploadId, i));
  let first = true;
  for (let i = 0; i < parts.length || first; ) {
    const take = first ? 32 : 31;
    const chunk = parts.slice(i, i + take);
    const sources = first ? chunk : [target, ...chunk];
    await b.compose(sources, target);
    i += take;
    first = false;
  }
}

/** Join + verify; marks the upload complete (or failed) and finishes the transfer. Idempotent once complete. */
export async function completeUpload(u: StoredUpload, actor: { id: string | null; siteSlug: string | null }): Promise<CompleteUploadResponse> {
  const ref = col.uploads().doc(u.uploadId);
  if (u.status === 'complete') return finishedResponse(u);
  // §21 #108 the local adapter commits the deployment file before the global one: a crash between them leaves the
  // transfer ready while this upload is still open (its parts already deleted) → finish the upload, do not fail it
  if (u.purpose === 'transfer' && u.deploymentId && u.transferId) {
    const t = await col.transfers(u.deploymentId).doc(u.transferId).get();
    if (t.exists && (t.data() as Transfer).status !== 'uploading' && (await sizeOf(u._target)) === u.sizeBytes) {
      await removePrefix(`uploads/${u.uploadId}/`).catch(() => undefined);
      await ref.update({ status: 'complete', error: null });
      return finishedResponse({ ...u, status: 'complete', error: null });
    }
  }
  const missing = Array.from({ length: u.partCount }, (_, i) => i).filter((i) => !(u.partsReceived ?? []).includes(i));
  if (missing.length) throw new ApiError('invalid', `parts missing: ${missing.slice(0, 20).join(', ')}`, { missing, errors: [{ path: 'parts', code: 'V_REQUIRED', message: 'parts missing', severity: 'error' }] });
  const hex = hexOf(u.sha256);
  const target = u._target;
  const already = u.purpose === 'blob' && (await exists(target)) && (await sizeOf(target)) === u.sizeBytes;
  if (!already) {
    try {
      if (blobs().canCompose && u.partCount > 1) {
        try {
          await composeParts(u, target);
          const h = await hashObject(target);
          if (h.hex !== hex || h.size !== u.sizeBytes) {
            await remove(target);
            throw new Error(`sha256 / size mismatch (got ${h.hex} / ${h.size} bytes)`);
          }
        } catch (e) {
          if (/mismatch/.test((e as Error).message)) throw e;
          logger.warn('compose failed, falling back to concat', { error: (e as Error).message });
          await concatParts(u, target, hex);
        }
      } else {
        const r = await concatParts(u, target, hex);
        if (r.size !== u.sizeBytes) {
          await remove(target);
          throw new Error(`size mismatch (got ${r.size} bytes, expected ${u.sizeBytes})`);
        }
      }
    } catch (e) {
      const msg = (e as Error).message.replace(/^.*?sha256 mismatch.*$/s, 'sha256 mismatch');
      await ref.update({ status: 'failed', error: msg });
      if (u.transferId && u.deploymentId) await col.transfers(u.deploymentId).doc(u.transferId).update({ error: msg }).catch(() => undefined);
      await removePrefix(`uploads/${u.uploadId}/`).catch(() => undefined);
      throw E.bad('sha256', 'V_FORMAT', `upload verification failed: ${msg}`);
    }
  }
  await removePrefix(`uploads/${u.uploadId}/`).catch(() => undefined);
  // §21 #107: a staged blob carries the verified hash (the commit trusts it instead of re-hashing)
  if (u._stageId) await blobs().setMeta(target, { opmSha256: hex });
  let transfer: Transfer | null = null;
  await db().runTransaction(async (tx) => {
    if (u.purpose === 'transfer' && u.deploymentId && u.transferId) transfer = await markTransferReady(tx, u.deploymentId, u.transferId, actor);
    tx.update(ref, { status: 'complete', error: null });
  });
  const done: StoredUpload = { ...u, status: 'complete', error: null, partsReceived: Array.from({ length: u.partCount }, (_, i) => i) };
  return { upload: uploadView(done), blob: u.purpose === 'blob' ? { sha256: u.sha256, sizeBytes: u.sizeBytes } : null, transfer };
}

async function finishedResponse(u: StoredUpload): Promise<CompleteUploadResponse> {
  let transfer: Transfer | null = null;
  if (u.purpose === 'transfer' && u.deploymentId && u.transferId) {
    const t = await col.transfers(u.deploymentId).doc(u.transferId).get();
    transfer = t.exists ? (t.data() as Transfer) : null;
  }
  return { upload: uploadView(u), blob: u.purpose === 'blob' ? { sha256: u.sha256, sizeBytes: u.sizeBytes } : null, transfer };
}

/** Transfer uploading → ready, activity transfer.sent, agentEpoch+1 for to-site. */
async function markTransferReady(tx: Tx, deploymentId: string, transferId: string, actor: { id: string | null; siteSlug: string | null }): Promise<Transfer> {
  const depRef = col.deployments().doc(deploymentId);
  const tRef = col.transfers(deploymentId).doc(transferId);
  const [ds, ts] = await Promise.all([tx.get(depRef), tx.get(tRef)]);
  const t = ts.data() as Transfer;
  if (!ds.exists || !ts.exists) throw E.notFound('transfer not found');
  if (t.status !== 'uploading') return t;
  const dep = decodeDeployment(ds.data()!);
  const log = new CloudLog(deploymentId, dep);
  const next: Transfer = { ...t, status: 'ready', error: null };
  tx.set(tRef, next);
  log.add(tx, {
    type: 'transfer.sent',
    summary: `${t.direction === 'to-site' ? `File ${t.fileName} sent to site ${t.siteSlug}` : `Site ${t.siteSlug} sent ${t.fileName} to the cloud`}`,
    data: { stage: 'uploaded', transferId, direction: t.direction, fileName: t.fileName, sizeBytes: t.sizeBytes, siteSlug: t.siteSlug },
    actor: actor.id,
  });
  tx.update(depRef, { ...log.depUpdate(), ...(t.direction === 'to-site' ? { agentEpoch: dep.agentEpoch + 1 } : {}) });
  return next;
}
export { paths };
