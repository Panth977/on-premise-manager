import { errMsg } from './api';

/** A loaded value with loading / error state (lists and detail pages). */
export class Loader<T> {
  data: T | null = $state(null);
  loading = $state(false);
  error: string | null = $state(null);
  /** the raw error (for status codes, e.g. 404 pages) */
  raw: unknown = $state.raw(null);
  #fn: () => Promise<T>;
  #seq = 0;
  constructor(fn: () => Promise<T>) {
    this.#fn = fn;
  }
  get ready() {
    return this.data !== null;
  }
  /** silent = keep showing the old data (polling / refresh after a write) */
  async load(silent = false): Promise<T | null> {
    const seq = ++this.#seq;
    if (!silent || this.data === null) this.loading = true;
    if (!silent) this.error = null;
    try {
      const d = await this.#fn();
      if (seq !== this.#seq) return d;
      this.data = d;
      this.error = null;
      this.raw = null;
      return d;
    } catch (e) {
      if (seq !== this.#seq) return null;
      if (!silent || this.data === null) {
        this.error = errMsg(e);
        this.raw = e;
      }
      return null;
    } finally {
      if (seq === this.#seq) this.loading = false;
    }
  }
  set(d: T) {
    this.data = d;
  }
}

/** A cursor-paged list ({ items, cursor }) with "Load more". */
export class PagedLoader<T> {
  items: T[] = $state([]);
  cursor: string | null = $state(null);
  loading = $state(false);
  loadingMore = $state(false);
  error: string | null = $state(null);
  loaded = $state(false);
  #fn: (cursor: string | null) => Promise<{ items: T[]; cursor: string | null }>;
  #seq = 0;
  constructor(fn: (cursor: string | null) => Promise<{ items: T[]; cursor: string | null }>) {
    this.#fn = fn;
  }
  get hasMore() {
    return this.cursor !== null;
  }
  async reset(silent = false) {
    const seq = ++this.#seq;
    if (!silent) this.loading = true;
    this.error = null;
    try {
      const r = await this.#fn(null);
      if (seq !== this.#seq) return;
      this.items = r.items;
      this.cursor = r.cursor;
      this.loaded = true;
    } catch (e) {
      if (seq !== this.#seq) return;
      this.error = errMsg(e);
    } finally {
      if (seq === this.#seq) this.loading = false;
    }
  }
  async more() {
    if (!this.cursor || this.loadingMore) return;
    const seq = this.#seq;
    this.loadingMore = true;
    try {
      const r = await this.#fn(this.cursor);
      if (seq !== this.#seq) return;
      this.items = [...this.items, ...r.items];
      this.cursor = r.cursor;
    } catch (e) {
      this.error = errMsg(e);
    } finally {
      this.loadingMore = false;
    }
  }
}
