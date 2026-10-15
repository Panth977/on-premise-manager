/**
 * §21 #107 local DRAFTS — every pending change set (config + file bytes) of both apps lives in IndexedDB
 * (shared/src/pending.ts DRAFT_DB: stores drafts / blobs / meta). localStorage is no longer used; an old
 * `opm.pending.v1.<app>.*` entry is migrated once (`initDrafts`).
 *
 *   await initDrafts(app, user)        once per signed-in session (layout): purge when the app was last open > 1 h ago,
 *                                      clear the previous user's drafts on an account switch, migrate localStorage,
 *                                      start the heartbeat (every 30 s + visibilitychange / pagehide) → returns stop()
 *   await readDraft(app, user, branch) → { draft, validity: 'valid' | 'stale' } | null   (validity vs headCommitId)
 *   await writeDraft(app, user, stored)                (auto-save: debounce with `autosaver`)
 *   await clearDraft(app, user, branch)                draft + its bytes (Discard, after a commit, stale)
 *   await clearUserDrafts(app, user)                   logout
 *   watchDraft(app, user, branch, cb)                  another tab wrote / cleared it (BroadcastChannel)
 *   autosaver(fn, ms?)                                 → { schedule(), flush(), cancel() }
 *
 * Bytes: `PendingBlobs` (pending-blobs.ts) stores into the same database (store `blobs`, owner = draftKey).
 * Every call is try / catch: without IndexedDB (private window, blocked site data) drafts stay in memory only.
 */
import {
  DRAFT_AUTOSAVE_MS,
  DRAFT_CHANNEL,
  DRAFT_DB,
  DRAFT_DB_VERSION,
  DRAFT_HEARTBEAT_MS,
  DRAFT_STORES,
  aliveKey,
  draftKey,
  draftValidity,
  draftsExpired,
  parseStoredPending,
  pendingStoragePrefix,
  userKey,
  type PendingApp,
  type StoredDraft,
  type StoredPending,
} from '@opm/shared/pending';

let dbp: Promise<IDBDatabase | null> | null = null;
/** the drafts database (null = unavailable) */
export function draftDb(): Promise<IDBDatabase | null> {
  if (dbp) return dbp;
  dbp = new Promise<IDBDatabase | null>((res) => {
    try {
      if (typeof indexedDB === 'undefined') return res(null);
      const rq = indexedDB.open(DRAFT_DB, DRAFT_DB_VERSION);
      rq.onupgradeneeded = () => {
        const db = rq.result;
        if (!db.objectStoreNames.contains(DRAFT_STORES.drafts)) {
          const st = db.createObjectStore(DRAFT_STORES.drafts, { keyPath: 'key' });
          st.createIndex('app', 'app', { unique: false });
        }
        if (!db.objectStoreNames.contains(DRAFT_STORES.blobs)) {
          const st = db.createObjectStore(DRAFT_STORES.blobs, { keyPath: 'key' });
          st.createIndex('owner', 'owner', { unique: false });
        }
        if (!db.objectStoreNames.contains(DRAFT_STORES.meta)) db.createObjectStore(DRAFT_STORES.meta, { keyPath: 'key' });
      };
      rq.onsuccess = () => res(rq.result);
      rq.onerror = () => res(null);
      rq.onblocked = () => res(null);
    } catch {
      res(null);
    }
  });
  return dbp;
}
export function idbReq<T>(r: IDBRequest<T>): Promise<T> {
  return new Promise((res, rej) => {
    r.onsuccess = () => res(r.result);
    r.onerror = () => rej(r.error);
  });
}
export function idbDone(tx: IDBTransaction): Promise<void> {
  return new Promise((res, rej) => {
    tx.oncomplete = () => res();
    tx.onerror = () => rej(tx.error);
    tx.onabort = () => rej(tx.error);
  });
}

// ───── cross-tab notifications
type Msg = { key: string; op: 'write' | 'clear' };
let chan: BroadcastChannel | null | undefined;
function channel(): BroadcastChannel | null {
  if (chan !== undefined) return chan;
  try {
    chan = typeof BroadcastChannel === 'undefined' ? null : new BroadcastChannel(DRAFT_CHANNEL);
  } catch {
    chan = null;
  }
  return chan;
}
function notify(m: Msg) {
  try {
    channel()?.postMessage(m);
  } catch {
    /* ignore */
  }
}

// ───── meta
async function getMeta<T>(key: string): Promise<T | null> {
  const db = await draftDb();
  if (!db) return null;
  try {
    const r = (await idbReq(db.transaction(DRAFT_STORES.meta, 'readonly').objectStore(DRAFT_STORES.meta).get(key))) as ({ key: string } & T) | undefined;
    return r ?? null;
  } catch {
    return null;
  }
}
async function putMeta(key: string, v: Record<string, unknown>): Promise<void> {
  const db = await draftDb();
  if (!db) return;
  try {
    const tx = db.transaction(DRAFT_STORES.meta, 'readwrite');
    tx.objectStore(DRAFT_STORES.meta).put({ key, ...v });
    await idbDone(tx);
  } catch {
    /* ignore */
  }
}
/** the app's heartbeat now (tests may pass a fake clock) */
export async function touchAlive(app: PendingApp, now = Date.now()): Promise<void> {
  await putMeta(aliveKey(app), { lastAlive: new Date(now).toISOString() });
}
export async function lastAlive(app: PendingApp): Promise<string | null> {
  return (await getMeta<{ lastAlive: string }>(aliveKey(app)))?.lastAlive ?? null;
}

// ───── drafts
async function deleteWhere(app: PendingApp, pred: (d: { key: string; user: string }) => boolean): Promise<number> {
  const db = await draftDb();
  if (!db) return 0;
  let n = 0;
  try {
    const tx = db.transaction([DRAFT_STORES.drafts, DRAFT_STORES.blobs], 'readwrite');
    const ds = tx.objectStore(DRAFT_STORES.drafts);
    const bs = tx.objectStore(DRAFT_STORES.blobs);
    const all = (await idbReq(ds.index('app').getAll(app))) as StoredDraft[];
    const gone = new Set<string>();
    for (const d of all)
      if (pred(d)) {
        ds.delete(d.key);
        gone.add(d.key);
        n++;
      }
    // bytes: their owner = a draft key of this app (also orphans whose draft was never written)
    const keys = (await idbReq(bs.getAllKeys())) as string[];
    for (const k of keys) {
      const owner = k.slice(0, k.lastIndexOf('|'));
      if (!owner.startsWith(`${app}|`)) continue;
      const user = owner.split('|')[1] ?? '';
      if (gone.has(owner) || pred({ key: owner, user })) bs.delete(k);
    }
    await idbDone(tx);
    for (const k of gone) notify({ key: k, op: 'clear' });
  } catch {
    /* ignore */
  }
  return n;
}
export async function clearUserDrafts(app: PendingApp, user: string): Promise<number> {
  return deleteWhere(app, (d) => d.user === user);
}
export async function clearAllDrafts(app: PendingApp): Promise<number> {
  return deleteWhere(app, () => true);
}

/** one-time migration of the #105 localStorage change sets into IndexedDB (for `user`) */
async function migrateLocalStorage(app: PendingApp, user: string): Promise<number> {
  let n = 0;
  try {
    if (typeof localStorage === 'undefined') return 0;
    const prefix = pendingStoragePrefix(app);
    const keys: string[] = [];
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i);
      if (k?.startsWith(prefix)) keys.push(k);
    }
    for (const k of keys) {
      const branch = k.slice(prefix.length);
      const p = parseStoredPending(localStorage.getItem(k), branch);
      if (p && p.count > 0 && !(await readRaw(app, user, branch))) {
        await writeDraft(app, user, p);
        n++;
      }
      localStorage.removeItem(k);
    }
  } catch {
    /* storage unavailable */
  }
  return n;
}

export interface DraftsSession {
  /** drafts purged because the app was last open more than 1 h ago */
  expired: number;
  /** drafts of another account removed */
  otherUser: number;
  migrated: number;
  stop(): void;
}
/**
 * Once per signed-in session (layout). Order: expiry check BEFORE the first heartbeat, account switch, localStorage
 * migration, then the heartbeat. `now` = clock for tests.
 */
export async function initDrafts(app: PendingApp, user: string, o: { now?: () => number; heartbeatMs?: number } = {}): Promise<DraftsSession> {
  const now = o.now ?? (() => Date.now());
  let expired = 0;
  let otherUser = 0;
  if (draftsExpired(await lastAlive(app), now())) expired = await clearAllDrafts(app);
  const prev = (await getMeta<{ user: string }>(userKey(app)))?.user ?? null;
  if (prev && prev !== user) otherUser = await clearUserDrafts(app, prev);
  await putMeta(userKey(app), { user });
  await touchAlive(app, now());
  const migrated = await migrateLocalStorage(app, user);
  let stopped = false;
  const beat = () => {
    if (!stopped) void touchAlive(app, now());
  };
  const iv = typeof setInterval !== 'undefined' ? setInterval(beat, o.heartbeatMs ?? DRAFT_HEARTBEAT_MS) : null;
  const vis = () => beat();
  if (typeof document !== 'undefined') document.addEventListener('visibilitychange', vis);
  if (typeof window !== 'undefined') window.addEventListener('pagehide', vis);
  return {
    expired,
    otherUser,
    migrated,
    stop() {
      stopped = true;
      if (iv) clearInterval(iv);
      if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', vis);
      if (typeof window !== 'undefined') window.removeEventListener('pagehide', vis);
    },
  };
}
/** logout: stop the heartbeat and delete the user's drafts */
export async function endDrafts(app: PendingApp, user: string, s?: DraftsSession | null): Promise<void> {
  s?.stop();
  await clearUserDrafts(app, user);
}

async function readRaw(app: PendingApp, user: string, branch: string): Promise<StoredDraft | null> {
  const db = await draftDb();
  if (!db) return null;
  try {
    const r = (await idbReq(db.transaction(DRAFT_STORES.drafts, 'readonly').objectStore(DRAFT_STORES.drafts).get(draftKey(app, user, branch)))) as StoredDraft | undefined;
    if (!r) return null;
    const p = parseStoredPending(JSON.stringify(r), branch);
    return p ? ({ ...p, key: r.key, app, user } as StoredDraft) : null;
  } catch {
    return null;
  }
}
/** the stored draft of an entity + its validity against the CURRENT head (stale = discard it, notice) */
export async function readDraft(app: PendingApp, user: string, branch: string, headCommitId: string | null): Promise<{ draft: StoredDraft; validity: 'valid' | 'stale' } | null> {
  const d = await readRaw(app, user, branch);
  if (!d || d.count === 0) return null;
  return { draft: d, validity: draftValidity(d, headCommitId) };
}
/** auto-save (count 0 = clear). false = IndexedDB unavailable / full */
export async function writeDraft(app: PendingApp, user: string, stored: StoredPending): Promise<boolean> {
  if (stored.count === 0) {
    await clearDraft(app, user, stored.branch, { keepBlobs: true });
    return true;
  }
  const db = await draftDb();
  if (!db) return false;
  try {
    const key = draftKey(app, user, stored.branch);
    const tx = db.transaction(DRAFT_STORES.drafts, 'readwrite');
    tx.objectStore(DRAFT_STORES.drafts).put(JSON.parse(JSON.stringify({ ...stored, key, app, user })));
    await idbDone(tx);
    notify({ key, op: 'write' });
    return true;
  } catch {
    return false;
  }
}
/** Discard / after a commit / stale: the draft and (unless keepBlobs) its bytes */
export async function clearDraft(app: PendingApp, user: string, branch: string, o: { keepBlobs?: boolean } = {}): Promise<void> {
  const db = await draftDb();
  if (!db) return;
  const key = draftKey(app, user, branch);
  try {
    const tx = db.transaction([DRAFT_STORES.drafts, DRAFT_STORES.blobs], 'readwrite');
    tx.objectStore(DRAFT_STORES.drafts).delete(key);
    if (!o.keepBlobs) {
      const bs = tx.objectStore(DRAFT_STORES.blobs);
      for (const k of (await idbReq(bs.index('owner').getAllKeys(key))) as string[]) bs.delete(k);
    }
    await idbDone(tx);
    notify({ key, op: 'clear' });
  } catch {
    /* ignore */
  }
}
/** another tab wrote / cleared this entity's draft → cb(draft | null) */
export function watchDraft(app: PendingApp, user: string, branch: string, cb: (d: StoredDraft | null) => void): () => void {
  const c = channel();
  if (!c) return () => undefined;
  const key = draftKey(app, user, branch);
  const on = (e: MessageEvent<Msg>) => {
    if (e.data?.key !== key) return;
    if (e.data.op === 'clear') cb(null);
    else void readRaw(app, user, branch).then(cb);
  };
  c.addEventListener('message', on);
  return () => c.removeEventListener('message', on);
}
/** debounced auto-save */
export function autosaver(fn: () => void | Promise<void>, ms = DRAFT_AUTOSAVE_MS) {
  let t: ReturnType<typeof setTimeout> | null = null;
  return {
    schedule() {
      if (t) clearTimeout(t);
      t = setTimeout(() => {
        t = null;
        void fn();
      }, ms);
    },
    flush() {
      if (t) {
        clearTimeout(t);
        t = null;
        void fn();
      }
    },
    cancel() {
      if (t) clearTimeout(t);
      t = null;
    },
  };
}
