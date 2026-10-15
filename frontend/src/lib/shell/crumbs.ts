import type { Crumb } from './Shell.svelte';

/** Titles of the site sections (round D: one route per section, no tab rows). */
export const SITE_SECTIONS = ['containers', 'edge', 'volumes', 'devtools', 'settings'] as const;
export type SiteSection = (typeof SITE_SECTIONS)[number];
export const SECTION_TITLE: Record<SiteSection, string> = { containers: 'Containers', edge: 'Edge', volumes: 'Volumes', devtools: 'Dev tools', settings: 'Settings' };

/** Old `?tab=` values of the site editor (round C and before) → section. */
export function sectionOfTab(tab: string | null): SiteSection {
  if (tab === 'edge') return 'edge';
  if (tab === 'site' || tab === 'settings') return 'settings';
  if (tab === 'volumes') return 'volumes';
  if (tab === 'devtools') return 'devtools';
  return 'containers';
}

const PAGE_TITLES: Record<string, string> = {
  variables: 'Variables',
  secrets: 'Secrets',
  files: 'Files',
  sites: 'Sites',
  setup: 'Setup',
  status: 'Status',
  activities: 'Activities',
  diff: 'Compare',
  compare: 'Compare',
  branches: 'Agent branches',
  history: 'History',
  'template-sync': 'Compare',
};

/**
 * Breadcrumb of an inner-scope page from its path:
 * Templates › Single › Sites › site-01 › Containers › cassandra (and Deployments › … for deployments).
 */
export function scopeCrumbs(root: Crumb, scopeLabel: string, base: string, pathname: string): Crumb[] {
  const out: Crumb[] = [root, { label: scopeLabel || '…', href: base }];
  if (!pathname.startsWith(base)) return out;
  const parts = pathname.slice(base.length).split('/').filter(Boolean).map(decodeURIComponent);
  if (!parts.length) {
    out.push({ label: 'Overview' });
    return out;
  }
  const [p0, p1, p2, p3] = parts;
  out.push({ label: PAGE_TITLES[p0!] ?? p0!, href: `${base}/${p0}` });
  if (p0 === 'activities' && p1 === 'upload') out.push({ label: 'Upload' });
  if (p0 !== 'sites' || !p1) return out;
  const site = `${base}/sites/${p1}`;
  out.push({ label: p1, href: `${site}/containers`, mono: true });
  if (!p2) return out;
  if (p2 === 'transfers' || p2 === 'send' || p2 === 'receive') out.push({ label: 'Transfers' });
  else if ((SITE_SECTIONS as readonly string[]).includes(p2)) {
    out.push({ label: SECTION_TITLE[p2 as SiteSection], href: `${site}/${p2}` });
    if (p2 === 'containers' && p3) out.push({ label: p3 === 'new' ? 'New container' : p3, mono: p3 !== 'new' });
    if (p2 === 'edge' && p3 === 'route') out.push({ label: 'Route' });
    if (p2 === 'edge' && p3 === 'import') out.push({ label: 'Import nginx.conf' });
    if (p2 === 'volumes' && p3 === 'content') out.push({ label: 'Content' });
  }
  return out;
}
