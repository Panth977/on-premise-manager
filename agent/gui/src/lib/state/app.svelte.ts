/** Deployment-wide data the shell needs everywhere (header, sidebar, stale markers). Polled by the app layout. */
import type { OverviewResponse, SiteTile } from '@opm/shared/types';
import { PROPOSAL_TERMINAL } from '@opm/shared/types';
import { api } from '../api';
import { Resource } from './resource.svelte';
import { session } from './session.svelte';
import { draft } from './draft.svelte';

export const overview = new Resource(async () => {
  const o = await api.overview({});
  session.setServerTime(o.serverTime);
  const det: Record<string, string> = {};
  for (const t of o.sites) if (t.advertiseHost) det[t.siteSlug] = t.advertiseHost;
  if (JSON.stringify(det) !== JSON.stringify(draft.detectedHosts)) draft.detectedHosts = det;
  return o;
});
export const proposals = new Resource(() => api.listProposals({}));
export const headConfig = new Resource(async () => {
  const c0 = await api.config({});
  // §21 #113: edits may use every image the site knows (head ∪ recent cloud commits ∪ drift-adopted ∪ site images)
  const c = c0.knownCatalog && c0.catalog ? { ...c0, catalog: { services: { ...c0.knownCatalog.services, ...c0.catalog.services }, images: { ...c0.knownCatalog.images, ...c0.catalog.images }, notFoundPages: { ...c0.knownCatalog.notFoundPages, ...c0.catalog.notFoundPages } } } : c0;
  // §21 #105: the edits live in this browser (draft store + localStorage); the agent keeps no working copy
  draft.adopt(c, overview.data?.deploymentId ?? session.info?.deploymentId ?? '', session.info?.siteSlug ?? overview.data?.self ?? '');
  return c;
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
