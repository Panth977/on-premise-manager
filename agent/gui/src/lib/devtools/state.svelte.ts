/**
 * Dev tools state of the selected site (§21 #97): devtoolsInfo (switch, limits, pickers) + the connections (presets
 * + manual, with the pooled state). One store for every dev tools page; reloaded when the site changes.
 */
import type { DevToolConnection, DevToolsInfo } from '@opm/shared/devtools';
import { errCode, errMessage, isApiError } from '$lib/api';
import { dev } from './api';

class DevStore {
  site = $state<string | null>(null);
  info = $state<DevToolsInfo | null>(null);
  conns = $state<DevToolConnection[]>([]);
  loading = $state(false);
  error = $state<string | null>(null);
  /** agent.json tools.devTools = false (info.enabled false, or every call 403 forbidden) */
  disabledReason = $state<string | null>(null);
  loaded = $state(false);
  #seq = 0;

  async load(site: string, force = false) {
    if (!force && this.site === site && (this.loaded || this.loading)) return;
    const seq = ++this.#seq;
    if (this.site !== site) {
      this.info = null;
      this.conns = [];
      this.loaded = false;
    }
    this.site = site;
    this.loading = true;
    this.error = null;
    try {
      const info = await dev.devtoolsInfo({ siteSlug: site });
      if (seq !== this.#seq) return;
      this.info = info;
      if (!info.enabled) {
        this.disabledReason = info.reason ?? 'Dev tools are switched off in agent.json.';
        this.conns = [];
      } else {
        this.disabledReason = null;
        const list = await dev.listConnections({ siteSlug: site });
        if (seq !== this.#seq) return;
        this.conns = list.items;
      }
      this.loaded = true;
    } catch (e) {
      if (seq !== this.#seq) return;
      if (isApiError(e) && e.status === 403 && errCode(e) === 'forbidden') {
        this.disabledReason = errMessage(e);
        this.loaded = true;
      } else this.error = errMessage(e);
    } finally {
      if (seq === this.#seq) this.loading = false;
    }
  }
  async refreshConns() {
    if (!this.site || this.disabledReason) return;
    try {
      const list = await dev.listConnections({ siteSlug: this.site });
      this.conns = list.items;
    } catch {
      /* keep the last list */
    }
  }
  byId(id: string | null | undefined): DevToolConnection | null {
    return this.conns.find((c) => c.id === id) ?? null;
  }
  upsert(c: DevToolConnection) {
    const i = this.conns.findIndex((x) => x.id === c.id);
    if (i >= 0) this.conns[i] = c;
    else this.conns = [...this.conns, c];
  }
  remove(id: string) {
    this.conns = this.conns.filter((c) => c.id !== id);
  }
}
export const devStore = new DevStore();
