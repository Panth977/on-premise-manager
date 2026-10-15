/**
 * §21 #107 pending BYTES of a browser change set (both GUIs). File contents, volume starter content and archives are
 * pending changes like any other edit: nothing is uploaded while the person edits. The bytes live
 *   - in memory (this object), and
 *   - in IndexedDB, in the SAME database as the draft (drafts.ts, DRAFT_DB store `blobs`): one record per app + user +
 *     entity + sha256 (`draftBlobKey`) with the base head it was made on; the draft (StoredPending.blobs) lists the
 *     sha256s it needs. Expiry, logout, Discard, a stale draft and a successful Commit remove the bytes with the draft.
 * IndexedDB may be unavailable (private window, blocked site data): the bytes then stay in memory only (`persistent` = false).
 *
 *   const pb = new PendingBlobs('cloud', user, 'template:abc');
 *   const ref = await pb.put(bytes, baseCommitId)   → { sha256, sizeBytes } (kept in memory + IndexedDB)
 *   pb.get(sha) / pb.has(sha) / pb.shas()
 *   await pb.restore(shas)                          → sha256s that could NOT be restored (lost bytes)
 *   await pb.retain(usedShas)                       → drop bytes nothing references any more
 *   await pb.rebase(newBaseCommitId)                → re-label after a rebase
 *   await pb.clear()                                → Discard / after Commit
 */
import { DRAFT_STORES, draftBlobKey, draftKey, type PendingApp } from '@opm/shared/pending';
import { draftDb, idbDone as done, idbReq as req } from './drafts';
import { sha256 as sha256Sync, toHex } from '@opm/shared/hash';

export interface PendingBlobRecord {
  key: string;
  owner: string;
  sha256: string;
  size: number;
  base: string | null;
  savedAt: string;
  bytes: ArrayBuffer;
}

/** "sha256:<hex>" of bytes: WebCrypto when available (secure contexts), else the shared pure-TS sha256 (plain-http agent GUIs) */
export async function sha256Of(bytes: Uint8Array): Promise<string> {
  try {
    const subtle = typeof crypto !== 'undefined' ? crypto.subtle : undefined;
    if (subtle) return 'sha256:' + toHex(new Uint8Array(await subtle.digest('SHA-256', bytes as BufferSource)));
  } catch {
    /* fall through */
  }
  return 'sha256:' + toHex(sha256Sync(bytes));
}

const PENDING_BLOB_STORE = DRAFT_STORES.blobs;
const openDb = draftDb;

export class PendingBlobs {
  readonly app: PendingApp;
  readonly user: string;
  readonly branchKey: string;
  #mem = new Map<string, Uint8Array>();
  /** false once IndexedDB failed (bytes kept in memory only) */
  persistent = true;
  /** called after every change (apps bump a reactive counter) */
  onchange: (() => void) | null = null;

  constructor(app: PendingApp, user: string, branchKey: string) {
    this.app = app;
    this.user = user;
    this.branchKey = branchKey;
  }
  get owner() {
    return draftKey(this.app, this.user, this.branchKey);
  }
  get size() {
    return this.#mem.size;
  }
  get(sha: string): Uint8Array | undefined {
    return this.#mem.get(sha);
  }
  has(sha: string): boolean {
    return this.#mem.has(sha);
  }
  shas(): string[] {
    return [...this.#mem.keys()].sort();
  }
  bytesTotal(): number {
    let n = 0;
    for (const b of this.#mem.values()) n += b.length;
    return n;
  }
  #changed() {
    try {
      this.onchange?.();
    } catch {
      /* ignore */
    }
  }

  /** keep bytes (hashing them) — returns the BlobRef the config points at */
  async put(bytes: Uint8Array, base: string | null = null): Promise<{ sha256: string; sizeBytes: number }> {
    const sha = await sha256Of(bytes);
    await this.putKnown(sha, bytes, base);
    return { sha256: sha, sizeBytes: bytes.length };
  }
  /** keep bytes whose sha256 the caller already computed */
  async putKnown(sha: string, bytes: Uint8Array, base: string | null = null): Promise<void> {
    const had = this.#mem.has(sha);
    this.#mem.set(sha, bytes);
    if (!had) this.#changed();
    const db = await openDb();
    if (!db) {
      this.persistent = false;
      return;
    }
    try {
      const tx = db.transaction(PENDING_BLOB_STORE, 'readwrite');
      const ab = bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) as ArrayBuffer;
      const rec: PendingBlobRecord = { key: draftBlobKey(this.app, this.user, this.branchKey, sha), owner: this.owner, sha256: sha, size: bytes.length, base, savedAt: new Date().toISOString(), bytes: ab };
      tx.objectStore(PENDING_BLOB_STORE).put(rec);
      await done(tx);
    } catch {
      this.persistent = false; // quota / blocked: memory only
    }
  }
  /** load stored bytes into memory; returns the sha256s that are not stored (their bytes are lost) */
  async restore(shas: readonly string[]): Promise<string[]> {
    const want = [...new Set(shas)].filter((s) => !this.#mem.has(s));
    if (!want.length) return [];
    const db = await openDb();
    if (!db) return want;
    const lost: string[] = [];
    try {
      const tx = db.transaction(PENDING_BLOB_STORE, 'readonly');
      const st = tx.objectStore(PENDING_BLOB_STORE);
      for (const sha of want) {
        const r = (await req(st.get(draftBlobKey(this.app, this.user, this.branchKey, sha)))) as PendingBlobRecord | undefined;
        if (r?.bytes) this.#mem.set(sha, new Uint8Array(r.bytes));
        else lost.push(sha);
      }
    } catch {
      return want.filter((s) => !this.#mem.has(s));
    }
    if (lost.length !== want.length) this.#changed();
    return lost;
  }
  /** every record of this entity (tests / diagnostics) */
  async stored(): Promise<Omit<PendingBlobRecord, 'bytes'>[]> {
    const db = await openDb();
    if (!db) return [];
    try {
      const tx = db.transaction(PENDING_BLOB_STORE, 'readonly');
      const all = (await req(tx.objectStore(PENDING_BLOB_STORE).index('owner').getAll(this.owner))) as PendingBlobRecord[];
      return all.map(({ bytes: _b, ...r }) => (void _b, r));
    } catch {
      return [];
    }
  }
  async #deleteWhere(pred: (sha: string) => boolean): Promise<void> {
    const db = await openDb();
    if (!db) return;
    try {
      const tx = db.transaction(PENDING_BLOB_STORE, 'readwrite');
      const st = tx.objectStore(PENDING_BLOB_STORE);
      const keys = (await req(st.index('owner').getAllKeys(this.owner))) as string[];
      for (const k of keys) {
        const sha = k.slice(k.lastIndexOf('|') + 1);
        if (pred(sha)) st.delete(k);
      }
      await done(tx);
    } catch {
      /* storage unavailable */
    }
  }
  /** drop the bytes nothing references any more */
  async retain(keep: Iterable<string>): Promise<void> {
    const k = new Set(keep);
    let changed = false;
    for (const sha of [...this.#mem.keys()])
      if (!k.has(sha)) {
        this.#mem.delete(sha);
        changed = true;
      }
    if (changed) this.#changed();
    await this.#deleteWhere((sha) => !k.has(sha));
  }
  /** re-label the stored records with the head the set is now based on (after a rebase) */
  async rebase(base: string | null): Promise<void> {
    const db = await openDb();
    if (!db) return;
    try {
      const tx = db.transaction(PENDING_BLOB_STORE, 'readwrite');
      const st = tx.objectStore(PENDING_BLOB_STORE);
      const all = (await req(st.index('owner').getAll(this.owner))) as PendingBlobRecord[];
      for (const r of all) if (r.base !== base) st.put({ ...r, base });
      await done(tx);
    } catch {
      /* ignore */
    }
  }
  /** Discard / after a successful Commit: memory + IndexedDB */
  async clear(): Promise<void> {
    const had = this.#mem.size > 0;
    this.#mem.clear();
    if (had) this.#changed();
    await this.#deleteWhere(() => true);
  }
}
