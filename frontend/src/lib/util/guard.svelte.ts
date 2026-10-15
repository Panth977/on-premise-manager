/**
 * "Leave without saving?" guard. Pages register a dirty check (+ an optional path prefix inside which
 * navigation is fine, e.g. site editor ↔ container editor share one working copy).
 */
import { onDestroy } from 'svelte';

interface Check {
  dirty: () => boolean;
  keepPrefix?: () => string | null;
}
class Guard {
  #checks = new Map<number, Check>();
  #n = 0;
  pending: URL | null = $state(null);
  bypass = false;
  register(c: Check): () => void {
    const id = ++this.#n;
    this.#checks.set(id, c);
    return () => this.#checks.delete(id);
  }
  anyDirty(): boolean {
    for (const c of this.#checks.values()) if (c.dirty()) return true;
    return false;
  }
  blocks(url: URL | null): boolean {
    for (const c of this.#checks.values()) {
      if (!c.dirty()) continue;
      const keep = c.keepPrefix?.() ?? null;
      if (url && keep && (url.pathname === keep || url.pathname.startsWith(keep.endsWith('/') ? keep : keep + '/'))) continue;
      return true;
    }
    return false;
  }
}
export const guard = new Guard();

/** Call during component init. */
export function useLeaveGuard(dirty: () => boolean, keepPrefix?: () => string | null) {
  const off = guard.register({ dirty, keepPrefix });
  onDestroy(off);
}
