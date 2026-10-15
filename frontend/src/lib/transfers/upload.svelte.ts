/**
 * Cloud → site file transfers: incremental sha256 (hash-wasm), POST transfers, 8 MiB parts, complete.
 * Progress lives in a module store so the list rows show it (and uploads continue across page changes).
 * Resumable after a reload: the pending {transferId, uploadId, name, size, lastModified} is kept in localStorage;
 * the person picks the same file again → GET /v1/uploads/{id} → only the missing parts are sent.
 */
import { createSHA256 } from 'hash-wasm';
import { UPLOAD_PART_SIZE, type Transfer } from '@opm/shared';
import { api, errMsg } from '../api';

export interface Pending {
  deploymentId: string;
  siteSlug: string;
  transferId: string;
  uploadId: string;
  fileName: string;
  size: number;
  lastModified: number;
}
export interface Progress {
  key: string;
  siteSlug: string;
  deploymentId: string;
  fileName: string;
  size: number;
  transferId: string | null;
  phase: 'hashing' | 'uploading' | 'completing' | 'done' | 'error';
  done: number; // bytes hashed / uploaded
  error: string | null;
}

const KEY = 'opm.transfers.pending';
const HASH_CHUNK = 4 * 1024 * 1024;

class TransferUploads {
  items: Progress[] = $state([]);
  #n = 0;
  forSite(deploymentId: string, siteSlug: string) {
    return this.items.filter((p) => p.deploymentId === deploymentId && p.siteSlug === siteSlug);
  }
  byTransfer(transferId: string) {
    return this.items.find((p) => p.transferId === transferId) ?? null;
  }
  #new(deploymentId: string, siteSlug: string, file: File): Progress {
    const p: Progress = { key: `u${++this.#n}`, deploymentId, siteSlug, fileName: file.name, size: file.size, transferId: null, phase: 'hashing', done: 0, error: null };
    this.items.push(p);
    return this.items[this.items.length - 1]!;
  }
  dismiss(key: string) {
    this.items = this.items.filter((p) => p.key !== key);
  }

  /** start a new transfer; resolves with the ready transfer (throws on error, progress keeps the error) */
  async send(deploymentId: string, siteSlug: string, file: File, note: string, onCreated?: () => void): Promise<Transfer> {
    const p = this.#new(deploymentId, siteSlug, file);
    try {
      const sha256 = await sha256File(file, (n) => (p.done = n));
      const r = await api.createTransfer({ deploymentId, siteSlug, body: { fileName: file.name, sizeBytes: file.size, sha256, ...(note.trim() ? { note: note.trim() } : {}) } });
      p.transferId = r.transfer.transferId;
      savePending({ deploymentId, siteSlug, transferId: r.transfer.transferId, uploadId: r.upload.uploadId, fileName: file.name, size: file.size, lastModified: file.lastModified });
      onCreated?.();
      return await this.#upload(p, r.upload.uploadId, file, []);
    } catch (e) {
      p.phase = 'error';
      p.error = errMsg(e);
      throw e;
    }
  }

  /** resume an interrupted upload with the same file */
  async resume(pend: Pending, file: File): Promise<Transfer> {
    if (file.name !== pend.fileName || file.size !== pend.size) throw new Error(`Pick the same file: ${pend.fileName} (${pend.size} bytes)`);
    const p = this.#new(pend.deploymentId, pend.siteSlug, file);
    p.transferId = pend.transferId;
    try {
      const u = await api.getUpload({ uploadId: pend.uploadId });
      if (u.status === 'complete') {
        clearPending(pend.transferId);
        p.phase = 'done';
        p.done = p.size;
        return { transferId: pend.transferId } as Transfer;
      }
      if (u.status === 'failed') throw new Error(`The upload failed on the server${u.error ? `: ${u.error}` : ''}. Delete the transfer and send the file again.`);
      return await this.#upload(p, pend.uploadId, file, u.partsReceived);
    } catch (e) {
      p.phase = 'error';
      p.error = errMsg(e);
      throw e;
    }
  }

  async #upload(p: Progress, uploadId: string, file: File, have: number[]): Promise<Transfer> {
    p.phase = 'uploading';
    const parts = Math.max(1, Math.ceil(file.size / UPLOAD_PART_SIZE));
    p.done = have.reduce((a, i) => a + Math.min(UPLOAD_PART_SIZE, file.size - i * UPLOAD_PART_SIZE), 0);
    for (let i = 0; i < parts; i++) {
      if (have.includes(i)) continue;
      const blob = file.slice(i * UPLOAD_PART_SIZE, Math.min(file.size, (i + 1) * UPLOAD_PART_SIZE));
      let tries = 0;
      for (;;) {
        try {
          await api.putUploadPart({ uploadId, index: String(i), body: blob });
          break;
        } catch (e) {
          if (++tries >= 3) throw e;
          await new Promise((r) => setTimeout(r, 800 * tries));
        }
      }
      p.done += blob.size;
    }
    p.phase = 'completing';
    const done = await api.completeUpload({ uploadId });
    clearPending(p.transferId!);
    p.phase = 'done';
    p.done = p.size;
    setTimeout(() => this.dismiss(p.key), 4000);
    return done.transfer!;
  }
}
export const uploads = new TransferUploads();

export async function sha256File(file: Blob, onProgress?: (bytes: number) => void): Promise<string> {
  const h = await createSHA256();
  h.init();
  for (let off = 0; off < file.size; off += HASH_CHUNK) {
    const buf = new Uint8Array(await file.slice(off, off + HASH_CHUNK).arrayBuffer());
    h.update(buf);
    onProgress?.(Math.min(file.size, off + HASH_CHUNK));
  }
  if (file.size === 0) onProgress?.(0);
  return `sha256:${h.digest('hex')}`;
}

function readAll(): Record<string, Pending> {
  try {
    return JSON.parse(localStorage.getItem(KEY) ?? '{}') as Record<string, Pending>;
  } catch {
    return {};
  }
}
function savePending(p: Pending) {
  const all = readAll();
  all[p.transferId] = p;
  try {
    localStorage.setItem(KEY, JSON.stringify(all));
  } catch {
    /* ignore */
  }
}
export function clearPending(transferId: string) {
  const all = readAll();
  delete all[transferId];
  try {
    localStorage.setItem(KEY, JSON.stringify(all));
  } catch {
    /* ignore */
  }
}
export function pendingOf(transferId: string): Pending | null {
  return readAll()[transferId] ?? null;
}
