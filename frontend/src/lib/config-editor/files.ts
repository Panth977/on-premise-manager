/**
 * File contents (FileDef.content) + volume content bytes: hashing, downloads, and — §21 #107 — the commit stage
 * transport. The config editors never upload while the person edits: bytes are PENDING (scope.putBytes → PendingBlobs,
 * memory + IndexedDB) and go up only inside Commit (`stageTransport`: POST /v1/stages, PUT /v1/stages/{id}/blobs/{sha}
 * ≤ 24 MiB, else the multi-part uploads API with stageId). Reads (`fetchContent`) look at the pending bytes of every
 * open scope first (`registerPendingBytes`), so editors, Review diffs and downloads show content not committed yet.
 * (The direct store uploads uploadContent / uploadSeedArchive of #101 / #105 are gone: POST /v1/blobs is unused.)
 */
import { SMALL_BLOB_MAX, UPLOAD_PART_SIZE } from '@opm/shared';
import type { BlobNeed } from '@opm/shared/staging';
import type { StageTransport } from '$opm/branch/stage-commit';
import { createSHA256 } from 'hash-wasm';
import { api, errMsg, fetchBlob } from '../api';

export async function sha256OfBlob(b: Blob, onProgress?: (f: number) => void): Promise<string> {
  const h = await createSHA256();
  h.init();
  const step = 4 * 1024 * 1024;
  for (let off = 0; off < b.size; off += step) {
    h.update(new Uint8Array(await b.slice(off, off + step).arrayBuffer()));
    onProgress?.(Math.min(1, (off + step) / Math.max(1, b.size)));
  }
  if (b.size === 0) onProgress?.(1);
  return 'sha256:' + h.digest('hex');
}

// ───── §21 #107 pending bytes of open scopes (content-addressed: any scope's bytes are valid for any reader)
type Resolver = (sha256: string) => Uint8Array | undefined;
const resolvers = new Set<Resolver>();
export function registerPendingBytes(r: Resolver): () => void {
  resolvers.add(r);
  return () => resolvers.delete(r);
}
export function pendingBytes(sha256: string): Uint8Array | undefined {
  for (const r of resolvers) {
    const b = r(sha256);
    if (b) return b;
  }
  return undefined;
}

/** Content of a blob: pending bytes in this browser first, else the download (200 stream ≤ 8 MiB or 302 → signed URL; errors carry the real text). */
export function fetchContent(sha256: string): Promise<Blob> {
  const p = pendingBytes(sha256);
  if (p) return Promise.resolve(new Blob([p as BlobPart]));
  return fetchBlob(sha256);
}

/**
 * §21 #107 the cloud commit stage transport (ui/src/branch/stage-commit.ts). `archives` = sha256s of volume seed archives
 * (multi-part purpose 'seed-archive', ≤ 200 MiB); other blobs > SMALL_BLOB_MAX use purpose 'blob' (≤ 50 MiB).
 */
export function stageTransport(archives: ReadonlySet<string> = new Set()): StageTransport {
  return {
    createStage: (blobs: BlobNeed[]) => api.createStage({ body: { blobs } }),
    async putBlob(stageId, need, bytes, onBytes) {
      const body = new Blob([bytes as BlobPart]);
      if (bytes.length <= SMALL_BLOB_MAX && !archives.has(need.sha256)) {
        await api.putStageBlob({ stageId, sha256: need.sha256, body, contentType: 'application/octet-stream' });
        onBytes?.(bytes.length);
        return;
      }
      const up = await api.createUpload({ body: { purpose: archives.has(need.sha256) ? 'seed-archive' : 'blob', sizeBytes: bytes.length, sha256: need.sha256, stageId } });
      for (let i = 0; i < up.partCount; i++) {
        if (up.partsReceived.includes(i)) continue;
        await api.putUploadPart({ uploadId: up.uploadId, index: String(i), body: body.slice(i * UPLOAD_PART_SIZE, Math.min(body.size, (i + 1) * UPLOAD_PART_SIZE)) });
        onBytes?.(Math.min(body.size, (i + 1) * UPLOAD_PART_SIZE));
      }
      await api.completeUpload({ uploadId: up.uploadId });
    },
    async deleteStage(stageId) {
      await api.deleteStage({ stageId });
    },
  };
}

export function saveAs(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 2000);
}

export async function downloadContent(sha256: string, name: string) {
  try {
    saveAs(await fetchContent(sha256), name);
  } catch (e) {
    throw new Error(errMsg(e));
  }
}

const EXT: Record<string, string> = { 'text/plain': 'txt', 'application/json': 'json', 'text/html': 'html', 'text/css': 'css', 'application/x-yaml': 'yaml', 'text/yaml': 'yaml', 'application/xml': 'xml', 'application/x-pem-file': 'pem', 'application/octet-stream': 'bin', 'text/csv': 'csv', 'application/toml': 'toml' };
export function fileName(slug: string, mime: string) {
  return `${slug}.${EXT[mime] ?? 'bin'}`;
}
/** Mime options for the file definition combobox (any other type/subtype may be typed). */
/** mime suggestions + text check — shared with the file form ($opm, §21 #102) */
export { MIME_OPTIONS, isTextMime } from '$opm/forms/files/mime';
export const COMMON_MIMES = ['text/plain', 'application/json', 'text/yaml', 'application/toml', 'text/html', 'text/css', 'text/csv', 'application/xml', 'application/x-pem-file', 'application/octet-stream', 'application/zip', 'image/png'];
