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
  untrack(() => fn());
  const t = setInterval(() => {
    if (!stopped && document.visibilityState === 'visible') untrack(() => fn());
  }, ms);
  const vis = () => {
    if (document.visibilityState === 'visible' && !stopped) untrack(() => fn());
  };
  document.addEventListener('visibilitychange', vis);
  return () => {
    stopped = true;
    clearInterval(t);
    document.removeEventListener('visibilitychange', vis);
  };
}
