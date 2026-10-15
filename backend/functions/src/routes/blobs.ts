/** Blobs (file contents) + human uploads (SPEC-final §6.1 Blobs + uploads, §5.2 Blobs). */
import { BLOB_STREAM_MAX, RE, SEED_ARCHIVE_MAX, SMALL_BLOB_MAX, type CreateUploadRequest } from '@opm/shared';
import { col } from '../core/fire.js';
import { E } from '../core/errors.js';
import { has, hasOnAnyDeployment } from '../core/perm.js';
import { asObject, hexOf, isHex64, onlyKeys, sha256Hex, V } from '../core/util.js';
import { exists, paths, serveObject, writeBytes } from '../core/storage.js';
import { completeUpload, loadUpload, newUpload, putPart, uploadView } from '../core/uploads.js';
import { dropStage, inStore, loadStage, openStage, parseBlobList, writeStaged, STAGE_FILE_MAX } from '../core/stages.js';
import { stagingObject } from '@opm/shared';
import { Reply } from '../core/reply.js';
import type { Ctx, Handlers } from '../core/router.js';

export const BLOB_UPLOAD_MAX = 50 * 1024 * 1024;

async function canUploadBlobs(c: Ctx) {
  if (has(c, 'templates', 'write')) return;
  if (await hasOnAnyDeployment(c.account!, 'deployment.config', 'write')) return;
  throw E.forbidden('needs templates.write or deployment.config.write');
}
async function canDownloadBlobs(c: Ctx) {
  if (has(c, 'templates', 'read')) return;
  if (await hasOnAnyDeployment(c.account!, 'deployment.config', 'read')) return;
  throw E.forbidden('needs templates.read or deployment.config.read');
}
export function parseShaParam(s: string): string {
  const hex = hexOf(s);
  if (!isHex64(hex)) throw E.bad('sha256', 'V_FORMAT', 'sha256 must be 64 lowercase hex (optionally "sha256:" prefixed)');
  return hex;
}
const human = (c: Ctx) => ({ kind: 'human' as const, uid: c.account!.uid });

export const blobHandlers: Pick<Handlers, 'uploadBlob' | 'getBlob' | 'createUpload' | 'getUpload' | 'putUploadPart' | 'completeUpload' | 'createStage' | 'putStageBlob' | 'deleteStage'> = {
  async uploadBlob(c) {
    await canUploadBlobs(c);
    if (c.body.length > SMALL_BLOB_MAX) throw E.tooLarge(`raw body larger than ${SMALL_BLOB_MAX} bytes (use POST /v1/uploads)`);
    const hex = sha256Hex(c.body);
    const claimed = c.headers['x-opm-sha256'];
    if (claimed && hexOf(claimed.trim().toLowerCase()) !== hex) throw E.bad('X-OPM-Sha256', 'V_FORMAT', `sha256 mismatch: body is sha256:${hex}`);
    const p = paths.file(hex);
    if (!(await exists(p))) await writeBytes(p, c.body);
    return { sha256: `sha256:${hex}`, sizeBytes: c.body.length };
  },
  async getBlob(c) {
    await canDownloadBlobs(c);
    const hex = parseShaParam(c.params.sha256!);
    // round C (§21 #55): ≤ BLOB_STREAM_MAX → 200 stream (no cross-origin redirect → no bucket CORS needed); larger → 302.
    // Content-addressed: the ETag never changes for a sha256, so If-None-Match answers 304.
    const etag = `"${hex}"`;
    const cache = { ETag: etag, 'Cache-Control': 'private, max-age=300' };
    const inm = c.headers['if-none-match'];
    if (inm && inm.split(',').some((t) => t.trim().replace(/^W\//, '') === etag)) {
      if (!(await exists(paths.file(hex)))) throw E.notFound('the content is not stored');
      return new Reply(304, { ...cache, 'X-OPM-Sha256': hex }, null);
    }
    const r = await serveObject(paths.file(hex), { range: c.headers['range'], headers: { 'X-OPM-Sha256': hex }, streamMax: BLOB_STREAM_MAX });
    if (r.status !== 302) Object.assign(r.headers, cache);
    return r;
  },
  async createUpload(c) {
    await canUploadBlobs(c);
    const b = asObject(c.json());
    onlyKeys(b, ['purpose', 'sizeBytes', 'sha256', 'stageId']);
    // §21 #107: into a commit stage (not the store); not audited — the commit is the one entry
    const stageId = b.stageId === undefined || b.stageId === null ? null : (await loadStage(b.stageId, c.account!.uid)).stageId;
    if (stageId) c.audit = { skip: true };
    const r = b as unknown as CreateUploadRequest;
    const v = new V();
    if (r.purpose !== 'blob' && r.purpose !== 'seed-archive') v.err('purpose', 'V_ENUM', 'purpose must be blob or seed-archive (transfers start with POST …/transfers)');
    if (!Number.isInteger(r.sizeBytes) || r.sizeBytes < 0) v.err('sizeBytes', 'V_RANGE', 'sizeBytes must be an integer ≥ 0');
    if (typeof r.sha256 !== 'string' || !RE.sha256.test(r.sha256)) v.err('sha256', 'V_FORMAT', 'sha256 must be "sha256:" + 64 hex');
    v.throwIf();
    // round E: a volume seed archive (.tar.gz) goes to the same content-addressed store (files/<hex>) with a larger limit;
    // the stored Upload keeps purpose 'blob' (UploadPurpose has no 'seed-archive') + internal _seedArchive
    const seed = r.purpose === 'seed-archive';
    const max = seed ? SEED_ARCHIVE_MAX : BLOB_UPLOAD_MAX;
    if (r.sizeBytes > max) throw E.tooLarge(`${seed ? 'seed archives' : 'blobs'} are at most ${max} bytes`);
    if (seed && r.sizeBytes < 1) throw E.bad('sizeBytes', 'V_RANGE', 'a seed archive is at least 1 byte');
    const u = newUpload({ purpose: 'blob', seedArchive: seed, deploymentId: null, transferId: null, sizeBytes: r.sizeBytes, sha256: r.sha256, createdBy: c.account!.uid, creatorSite: null, target: stageId ? stagingObject(stageId, hexOf(r.sha256)) : paths.file(hexOf(r.sha256)), stageId });
    await col.uploads().doc(u.uploadId).create(u);
    return uploadView(u);
  },
  async getUpload(c) {
    const u = await loadUpload(c.params.uploadId!, human(c), { allowClosed: true });
    if (u.status === 'failed') throw E.gone(`the upload failed${u.error ? `: ${u.error}` : ''}`);
    if (u.status === 'open' && Date.parse(u.expiresAt) <= Date.now()) throw E.gone('the upload expired');
    return uploadView(u);
  },
  async putUploadPart(c) {
    const u = await loadUpload(c.params.uploadId!, human(c));
    return putPart(u, c.params.index!, c.body);
  },
  async completeUpload(c) {
    const u = await loadUpload(c.params.uploadId!, human(c));
    if (u._stageId) c.audit = { skip: true };
    return completeUpload(u, { id: c.account!.uid, siteSlug: null });
  },
  // ── §21 #107 commit staging (never audited: AUDIT_EXEMPT_OPS)
  async createStage(c) {
    await canUploadBlobs(c);
    const b = asObject(c.json());
    onlyKeys(b, ['blobs']);
    return openStage(c.account!.uid, parseBlobList(b.blobs));
  },
  async putStageBlob(c) {
    const st = await loadStage(c.params.stageId, c.account!.uid);
    const hex = parseShaParam(c.params.sha256!);
    if (c.body.length > SMALL_BLOB_MAX) throw E.tooLarge(`raw body larger than ${SMALL_BLOB_MAX} bytes (use POST /v1/uploads with stageId)`);
    if (c.body.length > STAGE_FILE_MAX) throw E.tooLarge(`at most ${STAGE_FILE_MAX} bytes`);
    const got = sha256Hex(c.body);
    if (got !== hex) throw E.bad('sha256', 'V_FORMAT', `sha256 mismatch: body is sha256:${got}`);
    if (await inStore(hex)) return { sha256: `sha256:${hex}`, sizeBytes: c.body.length, staged: false };
    await writeStaged(st.stageId, hex, c.body);
    return { sha256: `sha256:${hex}`, sizeBytes: c.body.length, staged: true };
  },
  async deleteStage(c) {
    const st = await loadStage(c.params.stageId, c.account!.uid, { allowExpired: true });
    await dropStage(st.stageId, c.account!.uid);
    return { deleted: true };
  },
};
