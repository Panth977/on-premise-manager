/** Deployment-wide data the shell needs everywhere (header, sidebar, stale markers). Polled by the app layout. */
import type { AgentConfigResponse, OverviewResponse, SiteTile } from '@opm/shared/types';
import { PROPOSAL_TERMINAL } from '@opm/shared/types';
import { api } from '../api';
import { Resource } from './resource.svelte';
import { session } from './session.svelte';
import { draft, draftFor, ensureDrafts, madeVersion, pageSite, setSelfSite, storeMade } from './draft.svelte';

export const overview = new Resource(async () => {
  const o = await api.overview({});
  session.setServerTime(o.serverTime);
  setSelfSite(o.self);
  ensureDrafts([o.self, ...o.sites.map((t) => t.siteSlug)]); // after the await: made outside any effect
  const det: Record<string, string> = {};
  for (const t of o.sites) if (t.advertiseHost) det[t.siteSlug] = t.advertiseHost;
  if (JSON.stringify(det) !== JSON.stringify(draft.detectedHosts)) draft.detectedHosts = det;
  return o;
});
export const proposals = new Resource(() => api.listProposals({}));
/**
 * §21 #126 the head config of ONE site's branch, per site (the page's site: `headConfig`; another site's comes from that
 * site over the mesh). Each load hands the head to that site's change set (the edits live in this browser).
 */
const heads = new Map<string, Resource<AgentConfigResponse>>();
export function headConfigFor(site: string): Resource<AgentConfigResponse> {
  let r = heads.get(site);
  if (!r) {
    // the site's store is looked up at load time (it may be made a moment after this resource)
    r = new Resource(async () => {
      const d = draftFor(site);
      const c = await d.loadHead();
      d.adopt(c, overview.data?.deploymentId ?? session.info?.deploymentId ?? '', site);
      return c;
    });
    heads.set(site, r);
    storeMade();
  }
  return r!;
}
/** the head config of the page's site (same API as one Resource) */
export const headConfig: Resource<AgentConfigResponse> = new Proxy({} as Resource<AgentConfigResponse>, {
  get(_, k) {
    void madeVersion();
    const r = headConfigFor(pageSite());
    const v = Reflect.get(r, k, r);
    return typeof v === 'function' ? v.bind(r) : v;
  },
});

export const openProposalCount = () => (proposals.data?.items ?? []).filter((p) => !PROPOSAL_TERMINAL.includes(p.proposal.status)).length;

/** "auto · 10.0.1.10" for a site whose host is auto (null), "auto" when the address is not known yet */
export function autoHostText(slug: string): string {
  const a = draft.detectedHosts[slug];
  return a ? `auto · ${a}` : 'auto';
}

export function siteTile(o: OverviewResponse | undefined, slug: string): SiteTile | undefined {
  return o?.sites.find((s) => s.siteSlug === slug);
}
/** slugs of every site shown in selectors: head-config sites first, then other enrolled peers */
export function siteSlugs(o: OverviewResponse | undefined): string[] {
  if (!o) return [];
  return [...o.sites].sort((a, b) => Number(b.inConfig) - Number(a.inConfig) || a.siteSlug.localeCompare(b.siteSlug)).map((s) => s.siteSlug);
}
export function unreachableCount(o: OverviewResponse | undefined): number {
  return (o?.sites ?? []).filter((s) => !s.self && s.enrolled && !s.reachable).length;
}

class Prefs {
  perSite = $state('*');
  transferSite = $state('');
  theme = $state<'system' | 'light' | 'dark'>('system');
  constructor() {
    if (typeof localStorage === 'undefined') return;
    try {
      this.perSite = localStorage.getItem('opm.perSite') ?? '*';
      this.transferSite = localStorage.getItem('opm.transferSite') ?? '';
      const t = localStorage.getItem('opm.theme');
      this.theme = t === 'light' || t === 'dark' ? t : 'system';
    } catch {
      /* storage blocked */
    }
  }
  save() {
    try {
      localStorage.setItem('opm.perSite', this.perSite);
      localStorage.setItem('opm.transferSite', this.transferSite);
      if (this.theme === 'system') localStorage.removeItem('opm.theme');
      else localStorage.setItem('opm.theme', this.theme);
    } catch {
      /* storage blocked */
    }
    if (this.theme === 'system') delete document.documentElement.dataset.theme;
    else document.documentElement.dataset.theme = this.theme;
  }
}
export const prefs = new Prefs();

/**
 * Round D part 2: agentHome per site (the agent data dir; "./" locations are relative to it). Only siteVolumes carries it,
 * so it is learnt from those answers (Backups cards, Volumes page, Files page) and cached for the tab.
 */
class AgentHomes {
  map = $state<Record<string, string>>({});
  #pending = new Set<string>();
  set(site: string, home: string) {
    if (this.map[site] !== home) this.map[site] = home;
  }
  /** fetch once per site (errors ignored: the label then says <agentHome>) */
  ensure(site: string) {
    if (!site || site === '*' || this.map[site] || this.#pending.has(site)) return;
    this.#pending.add(site);
    api
      .siteVolumes({ siteSlug: site })
      .then((r) => this.set(site, r.agentHome))
      .catch(() => {})
      .finally(() => this.#pending.delete(site));
  }
  of(site: string): string {
    return this.map[site] ?? '<agentHome>';
  }
}
export const agentHomes = new AgentHomes();
