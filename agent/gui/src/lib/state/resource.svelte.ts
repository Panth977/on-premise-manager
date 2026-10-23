/** Loading / error / data for one API read, with stale-while-revalidate polling. */
import { untrack } from 'svelte';

export class Resource<T> {
  data = $state<T | undefined>(undefined);
  error = $state<unknown>(null);
  loading = $state(false);
  /** at least one successful load */
  loaded = $state(false);
  updatedAt = $state<number | null>(null);
  #fn: () => Promise<T>;
  #seq = 0;

  constructor(fn: () => Promise<T>) {
    this.#fn = fn;
  }
  /** load(); concurrent calls: the newest answer wins */
  async load(): Promise<T | undefined> {
    const my = ++this.#seq;
    this.loading = true;
    try {
      const d = await this.#fn();
      if (my === this.#seq) {
        this.data = d;
        this.error = null;
        this.loaded = true;
        this.updatedAt = Date.now();
      }
      return d;
    } catch (e) {
      if (my === this.#seq) this.error = e;
      return undefined;
    } finally {
      if (my === this.#seq) this.loading = false;
    }
  }
  reset() {
    this.#seq++;
    this.data = undefined;
    this.error = null;
    this.loaded = false;
    this.loading = false;
  }
}

/**
 * Call inside a component (or $effect.root): runs fn now and every `ms` while the tab is visible.
 * Returns the cleanup (use as `$effect(() => poll(...))`).
 */
export function poll(fn: () => unknown, ms: number): () => void {
  let stopped = false;
  // a tick is skipped while the previous call is still running: a slow answer is never thrown away by a newer request
  let busy = false;
  const run = () => {
    if (busy) return;
    const r = untrack(() => fn());
    if (r instanceof Promise) {
      busy = true;
      const done = () => (busy = false);
      r.then(done, done);
    }
  };
  run();
  const t = setInterval(() => {
    if (!stopped && document.visibilityState === 'visible') run();
  }, ms);
  const vis = () => {
    if (document.visibilityState === 'visible' && !stopped) run();
  };
  document.addEventListener('visibilitychange', vis);
  return () => {
    stopped = true;
    clearInterval(t);
    document.removeEventListener('visibilitychange', vis);
  };
}
