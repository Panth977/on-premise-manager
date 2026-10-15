/**
 * §21 #107 local drafts of the cloud console (ui/src/branch/drafts.ts over IndexedDB `opm-drafts`). Every pending
 * change set (config + file bytes) of a template / deployment is auto-saved per signed-in account; it survives a
 * refresh or leaving the site and is offered on return ("Restore or Discard?") — only when it was made on the entity's
 * CURRENT head (otherwise it is discarded with a notice). Drafts expire 1 h after the console was last open (heartbeat
 * every 30 s while a tab is open); logging out deletes the account's drafts.
 *
 *   startDrafts(uid)   once after sign-in (app layout): expiry purge → account switch → localStorage migration → heartbeat
 *   draftsReady()      resolves when the start-up purge is done (scopes read drafts only after it)
 *   endCloudDrafts()   logout (session.logout)
 *   draftUser()        the uid the drafts belong to
 *   draftCounts        reactive branchKey → change count of the stored drafts (template / deployment lists)
 */
import { DRAFT_STORES, type StoredDraft } from '@opm/shared/pending';
import { draftDb, endDrafts, idbReq, initDrafts, type DraftsSession } from '$opm/branch/drafts';

let uid: string | null = null;
let sess: DraftsSession | null = null;
let ready: Promise<void> = Promise.resolve();
let resolveReady: (() => void) | null = null;
let started: string | null = null;

export const draftState = $state<{ counts: Record<string, number>; expired: number }>({ counts: {}, expired: 0 });

/** the account the drafts belong to ('' before sign-in) */
export const draftUser = () => uid ?? '';
export function draftsReady(): Promise<void> {
  return ready;
}
export function startDrafts(user: string): Promise<void> {
  if (started === user) return ready;
  started = user;
  uid = user;
  ready = new Promise<void>((r) => (resolveReady = r));
  void (async () => {
    try {
      sess?.stop();
      sess = await initDrafts('cloud', user);
      draftState.expired = sess.expired;
    } catch {
      /* drafts unavailable: memory only */
    } finally {
      resolveReady?.();
      void refreshDraftCounts();
    }
  })();
  return ready;
}
export async function endCloudDrafts(): Promise<void> {
  if (!uid) return;
  try {
    await endDrafts('cloud', uid, sess);
  } catch {
    /* ignore */
  }
  sess = null;
  started = null;
  uid = null;
  draftState.counts = {};
}
/** stored draft counts of this account (lists show "N not committed") */
export async function refreshDraftCounts(): Promise<void> {
  await ready;
  const db = await draftDb();
  if (!db || !uid) return;
  try {
    const all = (await idbReq(db.transaction(DRAFT_STORES.drafts, 'readonly').objectStore(DRAFT_STORES.drafts).index('app').getAll('cloud'))) as StoredDraft[];
    const out: Record<string, number> = {};
    for (const d of all) if (d.user === uid && d.count > 0) out[d.branch] = d.count;
    draftState.counts = out;
  } catch {
    /* ignore */
  }
}
/** a scope saved / cleared its draft */
export function noteDraftCount(branch: string, count: number) {
  if ((draftState.counts[branch] ?? 0) === count) return;
  const next = { ...draftState.counts };
  if (count > 0) next[branch] = count;
  else delete next[branch];
  draftState.counts = next;
}
