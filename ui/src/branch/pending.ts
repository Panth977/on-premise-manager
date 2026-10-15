/**
 * @deprecated §21 #107: change sets are stored in IndexedDB — use ./drafts.ts (readDraft / writeDraft / clearDraft /
 * watchDraft / initDrafts). These localStorage helpers remain only for the one-time migration and old tests.
 *
 * §21 #105 browser change sets — localStorage I/O shared by the cloud console and the agent GUI (pure helpers +
 * types: @opm/shared/pending). Every access is wrapped in try / catch: storage may be absent (private window,
 * blocked site data, previews); the editors then simply keep the set in memory.
 *
 *   readPending(app, branchKey)            → StoredPending | null
 *   writePending(app, stored)              → boolean (false = storage unavailable / full)
 *   clearPending(app, branchKey)
 *   watchPending(app, branchKey, cb)       → unsubscribe; cb(stored | null) when ANOTHER tab wrote / cleared the key
 */
import { parseStoredPending, pendingStorageKey, type PendingApp, type StoredPending } from '@opm/shared/pending';

function ls(): Storage | null {
  try {
    return typeof localStorage === 'undefined' ? null : localStorage;
  } catch {
    return null;
  }
}
export function readPending(app: PendingApp, branchKey: string): StoredPending | null {
  try {
    return parseStoredPending(ls()?.getItem(pendingStorageKey(app, branchKey)), branchKey);
  } catch {
    return null;
  }
}
export function writePending(app: PendingApp, stored: StoredPending): boolean {
  try {
    const s = ls();
    if (!s) return false;
    s.setItem(pendingStorageKey(app, stored.branch), JSON.stringify(stored));
    return true;
  } catch {
    return false;
  }
}
export function clearPending(app: PendingApp, branchKey: string): void {
  try {
    ls()?.removeItem(pendingStorageKey(app, branchKey));
  } catch {
    /* storage unavailable */
  }
}
export function watchPending(app: PendingApp, branchKey: string, cb: (p: StoredPending | null) => void): () => void {
  if (typeof window === 'undefined') return () => undefined;
  const key = pendingStorageKey(app, branchKey);
  const on = (e: StorageEvent) => {
    if (e.key !== key) return;
    cb(parseStoredPending(e.newValue, branchKey));
  };
  window.addEventListener('storage', on);
  return () => window.removeEventListener('storage', on);
}
/** "5 min ago" style age of a stored set */
export function savedAgo(savedAt: string, now = Date.now()): string {
  const t = Date.parse(savedAt);
  if (!Number.isFinite(t)) return '';
  const s = Math.max(0, Math.round((now - t) / 1000));
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  return `${Math.round(s / 86400)} d ago`;
}
