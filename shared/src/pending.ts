/**
 * §21 #105 browser change sets ("pending changes", docs-v2/branches-commits.md §15). There is no server-side working
 * copy: every edit of a template / deployment (cloud console) or of a site (agent GUI) lives in the browser as ONE
 * pending change set per entity, shared by all its pages, until "Commit" writes it with a message.
 *
 * The set is persisted in localStorage per entity so a reload or an accidental close loses nothing:
 *   key   = pendingStorageKey(app, branchKey)   e.g. "opm.pending.v1.cloud.deployment:EByKd…"
 *   value = StoredPending (JSON): the head it was made on + the edited config (ConfigInput)
 * On load: same head → offer "Restore unsaved changes"; the head moved → a STALE set the user can review (rebase with
 * branch.ts `rebaseChanges`) or discard.
 *
 * Secret VALUES never touch storage: a typed `plainValue` is dropped before writing (its slug is listed in
 * `droppedValues`, the restore banner says "re-enter the value of X"). `plainValue: null` (clear) and `generateNow`
 * hold no secret and are kept. Pure functions; the apps own the storage access (try / catch around localStorage).
 */
import type { ConfigInput, SecretDefInput, Slug, Ts, Ulid } from './types.js';

export const PENDING_VERSION = 1 as const;
export type PendingApp = 'cloud' | 'agent';
export const pendingStorageKey = (app: PendingApp, branchKey: string) => `opm.pending.v${PENDING_VERSION}.${app}.${branchKey}`;
/** every key of an app (cleanup / tests) */
export const pendingStoragePrefix = (app: PendingApp) => `opm.pending.v${PENDING_VERSION}.${app}.`;

export interface StoredPending {
  v: typeof PENDING_VERSION;
  /** branchKey of the entity (template:<id> | deployment:<id> | agent:<dep>/<site>) */
  branch: string;
  /** the head commit the changes were made on (null = the branch had no commit) */
  baseCommitId: Ulid | null;
  baseSeq: number | null;
  savedAt: Ts;
  /** the edited config; secrets without typed values (see droppedValues) */
  config: ConfigInput;
  /** secrets whose typed value was NOT stored (values never leave memory) */
  droppedValues: Slug[];
  /** number of pending items when stored (banner text only) */
  count: number;
  /** app extras (e.g. agent GUI file texts shown in the editor); never secrets */
  extra?: Record<string, unknown>;
  /**
   * §21 #107: sha256s of the pending BYTES this set needs (File contents, volume content, archives not uploaded yet).
   * localStorage is too small for bytes: they live in IndexedDB (`ui/src/branch/pending-blobs.ts`, database
   * PENDING_BLOB_DB, one record per entity + sha256 with the base head it was made on) and in memory.
   */
  blobs?: string[];
}
/** §21 #107 IndexedDB of pending bytes (both GUIs; per origin): database name, version, object store */
export const PENDING_BLOB_DB = 'opm-pending-blobs';
export const PENDING_BLOB_DB_VERSION = 1;
export const PENDING_BLOB_STORE = 'blobs';
/** record key = owner + "|" + sha256 ; owner = pendingStorageKey(app, branchKey) */
export const pendingBlobKey = (app: PendingApp, branchKey: string, sha256: string) => `${pendingStorageKey(app, branchKey)}|${sha256}`;

/** drop typed secret values (keep null = clear and generateNow) */
export function stripSecretValues(input: ConfigInput): { config: ConfigInput; dropped: Slug[] } {
  const dropped: Slug[] = [];
  const secrets: SecretDefInput[] = input.secrets.map((s) => {
    if (typeof s.plainValue === 'string') {
      dropped.push(s.secretSlug);
      const { plainValue: _p, ...rest } = s;
      void _p;
      return { ...rest };
    }
    return { ...s };
  });
  return { config: { ...input, secrets }, dropped };
}

export function toStoredPending(i: { branch: string; baseCommitId: Ulid | null; baseSeq: number | null; config: ConfigInput; count: number; now: Ts; extra?: Record<string, unknown>; blobs?: string[] }): StoredPending {
  const { config, dropped } = stripSecretValues(JSON.parse(JSON.stringify(i.config)) as ConfigInput);
  return { v: PENDING_VERSION, branch: i.branch, baseCommitId: i.baseCommitId, baseSeq: i.baseSeq, savedAt: i.now, config, droppedValues: dropped, count: i.count, ...(i.extra ? { extra: i.extra } : {}), ...(i.blobs?.length ? { blobs: [...new Set(i.blobs)].sort() } : {}) };
}

/** parse a stored value (null = absent, unreadable, other version or other entity) */
export function parseStoredPending(raw: string | null | undefined, branch: string): StoredPending | null {
  if (!raw) return null;
  try {
    const v = JSON.parse(raw) as StoredPending;
    if (!v || v.v !== PENDING_VERSION || v.branch !== branch || !v.config || !Array.isArray(v.config.secrets)) return null;
    if (!Array.isArray(v.droppedValues)) v.droppedValues = [];
    if (v.blobs !== undefined && (!Array.isArray(v.blobs) || v.blobs.some((x) => typeof x !== 'string'))) delete v.blobs;
    return v;
  } catch {
    return null;
  }
}

/** 'same' = made on the current head (offer restore); 'stale' = the head moved (review = rebase, or discard) */
export const pendingState = (p: Pick<StoredPending, 'baseCommitId'>, headCommitId: Ulid | null): 'same' | 'stale' => ((p.baseCommitId ?? null) === (headCommitId ?? null) ? 'same' : 'stale');

// ───────────────────────────────────────── §21 #107 drafts in IndexedDB (user follow-up)
/**
 * ALL pending change sets (config + bytes) are stored in IndexedDB (database DRAFT_DB) in both apps — localStorage is
 * no longer used (an old `opm.pending.v1.*` entry is migrated once). Records are keyed per app + USER + entity:
 *   drafts  key draftKey(app, user, branchKey)            value = StoredDraft (StoredPending + owner fields)
 *   blobs   key draftKey(app, user, branchKey) + "|" + sha  (index "owner")
 *   meta    key "alive:<app>"  { lastAlive }  — heartbeat of the app (every DRAFT_HEARTBEAT_MS while a tab is open,
 *           and on visibilitychange / pagehide); "user:<app>" { user } — the last signed-in user.
 * Expiry is per app origin: on load, when lastAlive is older than DRAFT_TTL_MS (1 h) every draft of the app is purged;
 * while any tab is open the heartbeat keeps them alive however long the session lasts. Logout or another account →
 * that user's drafts are deleted. A draft is valid ONLY when its base commit is the entity's CURRENT head: otherwise it
 * is discarded automatically (one-time notice) — no restore / rebase of a stale draft (the in-session 409 rebase stays).
 */
export const DRAFT_DB = 'opm-drafts';
export const DRAFT_DB_VERSION = 1;
export const DRAFT_STORES = { drafts: 'drafts', blobs: 'blobs', meta: 'meta' } as const;
export const DRAFT_TTL_MS = 60 * 60_000;
export const DRAFT_HEARTBEAT_MS = 30_000;
/** debounce of the auto-save on every change */
export const DRAFT_AUTOSAVE_MS = 400;
/** BroadcastChannel name (other tabs of the same app learn about writes / clears) */
export const DRAFT_CHANNEL = 'opm-drafts';
export const draftKey = (app: PendingApp, user: string, branchKey: string) => `${app}|${user}|${branchKey}`;
export const draftBlobKey = (app: PendingApp, user: string, branchKey: string, sha256: string) => `${draftKey(app, user, branchKey)}|${sha256}`;
export const aliveKey = (app: PendingApp) => `alive:${app}`;
export const userKey = (app: PendingApp) => `user:${app}`;
export interface StoredDraft extends StoredPending {
  key: string;
  app: PendingApp;
  user: string;
}
/** the app was last open more than DRAFT_TTL_MS ago (null lastAlive = no heartbeat ever → nothing to keep) */
export function draftsExpired(lastAlive: Ts | null | undefined, now: number): boolean {
  if (!lastAlive) return true;
  const t = Date.parse(lastAlive);
  return !Number.isFinite(t) || now - t > DRAFT_TTL_MS;
}
/** 'valid' = made on the current head (offer Restore / Discard); 'stale' = discard automatically with a notice */
export const draftValidity = (p: Pick<StoredPending, 'baseCommitId'>, headCommitId: Ulid | null): 'valid' | 'stale' => (pendingState(p, headCommitId) === 'same' ? 'valid' : 'stale');
/** the one-time notice of an auto-discarded draft */
export function staleDraftNotice(baseSeq: number | null, headSeq: number | null): string {
  const b = baseSeq === null ? 'no commit' : `commit ${baseSeq}`;
  const h = headSeq === null ? 'no commit' : `commit ${headSeq}`;
  return `Your local draft was based on ${b}; the latest is ${h}, so it was discarded.`;
}
