/**
 * Navigation model (nav2, SPEC §18 / §21 #97): ONE site selection for the whole GUI, part of every URL
 * (/_opm/app/{site|*}/{module}/…), remembered per browser tab (sessionStorage). The sidebar is four accordion sections;
 * every module lives under the selected site. Deployment-wide modules ignore the selection (or use it as a filter);
 * modules that need one site show "Choose a site…" under "*".
 */
import { base } from '$app/paths';
import { page } from '$app/state';

export type SectionKey = 'docker' | 'devtools' | 'agent' | 'deployment' | 'site';
export interface NavModule {
  k: string;
  label: string;
  icon: string;
  sec: SectionKey;
  /** the module works on ONE site: under "*" it shows the "Choose a site" state */
  one?: boolean;
  /** deployment-wide (the selection is only a filter / highlight): keeps the query string when the site changes */
  wide?: boolean;
}
export const SECTIONS: { k: SectionKey; label: string }[] = [
  { k: 'docker', label: 'Docker' },
  { k: 'devtools', label: 'Dev tools' },
  { k: 'agent', label: 'Agent' },
  { k: 'deployment', label: 'Deployment' },
  { k: 'site', label: 'Site' },
];
export const MODULES: NavModule[] = [
  { k: 'containers', label: 'Containers', icon: 'container', sec: 'docker' },
  { k: 'volumes', label: 'Volumes', icon: 'hardDrive', sec: 'docker' },
  { k: 'docker', label: 'Docker', icon: 'layers', sec: 'docker' },
  { k: 'network', label: 'Network', icon: 'network', sec: 'docker', one: true },
  // §21 #97 dev tools: module keys with a "/" are sub-pages of /devtools (moduleOf maps a path to them)
  { k: 'devtools', label: 'Connections', icon: 'plug', sec: 'devtools', one: true },
  { k: 'devtools/sql', label: 'SQL', icon: 'database', sec: 'devtools', one: true },
  { k: 'devtools/redis', label: 'Redis', icon: 'layers', sec: 'devtools', one: true },
  { k: 'devtools/mqtt', label: 'MQTT', icon: 'radio', sec: 'devtools', one: true },
  { k: 'devtools/http', label: 'HTTP client', icon: 'send', sec: 'devtools', one: true },
  { k: 'devtools/history', label: 'History', icon: 'history', sec: 'devtools', one: true },
  { k: 'agent', label: 'Agent', icon: 'server', sec: 'agent' },
  { k: 'debug', label: 'Debug', icon: 'bug', sec: 'agent' },
  // §21 #123: Files — the deployment's shared bucket as this site sees it (URL /transfers kept; replaced §21 #100 send/receive)
  { k: 'transfers', label: 'Shared files', icon: 'folder', sec: 'agent', one: true },
  { k: 'internet', label: 'Internet', icon: 'globe', sec: 'agent', wide: true },
  { k: 'sites', label: 'Sites', icon: 'grid', sec: 'deployment', wide: true },
  { k: 'variables', label: 'Variables', icon: 'variable', sec: 'deployment', wide: true },
  { k: 'secrets', label: 'Secrets', icon: 'key', sec: 'deployment', wide: true },
  { k: 'files', label: 'Files', icon: 'file', sec: 'deployment', wide: true },
  // §21 #104: Branch replaces Diff (cloud head · this site · uncommitted · merge · history); /diff redirects here
  { k: 'branch', label: 'Branch', icon: 'branch', sec: 'deployment', wide: true },
  { k: 'backups', label: 'Backups', icon: 'archive', sec: 'deployment', wide: true },
  { k: 'status', label: 'Status', icon: 'gauge', sec: 'site' },
  { k: 'activities', label: 'Activities', icon: 'activity', sec: 'site' },
  { k: 'config', label: 'Config', icon: 'cog', sec: 'site' },
];
export const moduleByKey = (k: string | undefined) => MODULES.find((m) => m.k === k);
/** §21 #97: the dev tools sub-pages (postgres / sqlite / cassandra are aliases of the SQL workbench) */
export const DEVTOOL_PAGES = ['sql', 'redis', 'mqtt', 'http', 'history'] as const;
/** the module of a parsed app path (dev tools sub-pages are their own sidebar items) */
export function moduleOf(loc: { mod: string; rest: string[] } | null): NavModule | undefined {
  if (!loc) return undefined;
  if (loc.mod === 'devtools') {
    const r = loc.rest[0] ?? '';
    const k = ['postgres', 'sqlite', 'cassandra'].includes(r) ? 'sql' : r;
    return moduleByKey((DEVTOOL_PAGES as readonly string[]).includes(k) ? `devtools/${k}` : 'devtools');
  }
  return moduleByKey(loc.mod);
}
/** data-testid suffix of a module's sidebar link ("devtools/sql" → "devtools-sql") */
export const navTestId = (k: string) => k.replace(/\//g, '-');

/** /_opm/app/{site}/{path} */
export function appHref(site: string, path = 'status'): string {
  return `${base}/app/${site}/${path.replace(/^\//, '')}`;
}

/** a module of the CURRENT site selection (components without their own site: Diff links, Backups links, …) */
export function siteHref(path: string): string {
  return appHref(page.params.siteSlug ?? (storedSite() || '*'), path);
}

/** {site, module, rest[]} of an app path (null outside /_opm/app/{site}/…) */
export function parseAppPath(pathname: string): { site: string; mod: string; rest: string[] } | null {
  const m = /^\/_opm\/app\/([^/]+)(?:\/(.*))?$/.exec(pathname);
  if (!m) return null;
  const segs = (m[2] ?? '').split('/').filter(Boolean);
  return { site: decodeURIComponent(m[1]), mod: segs[0] ?? '', rest: segs.slice(1) };
}

/** where the same module lives on another site (the top selector and the inline chooser) */
export function switchSiteHref(pathname: string, search: string, site: string): string {
  const p = parseAppPath(pathname);
  if (!p || !p.mod) return appHref(site, 'status');
  const mod = moduleByKey(p.mod);
  let path = p.mod;
  if (p.mod === 'config' && site !== '*' && ['containers', 'edge', 'volumes', 'settings'].includes(p.rest[0] ?? '')) path += `/${p.rest[0]}`;
  if (p.mod === 'activities' && p.rest[0] === 'download') path += '/download';
  // dev tools: same tool on the other site; the connection (?c=) belongs to a site, so the query is dropped
  if (p.mod === 'devtools' && p.rest[0]) path += `/${p.rest[0]}`;
  const q = mod?.wide || p.mod === 'debug' || p.mod === 'agent' ? search : '';
  return appHref(site, path) + q;
}

// ── the selection (per tab)
const KEY = 'opm.site';
export function storedSite(): string {
  try {
    return sessionStorage.getItem(KEY) ?? '';
  } catch {
    return '';
  }
}
export function storeSite(s: string) {
  try {
    sessionStorage.setItem(KEY, s);
  } catch {
    /* storage blocked */
  }
}

// ── accordion state (localStorage, all tabs)
const OPEN_KEY = 'opm.nav.open';
export function loadOpen(): Partial<Record<SectionKey, boolean>> {
  try {
    const v = JSON.parse(localStorage.getItem(OPEN_KEY) ?? '{}');
    return v && typeof v === 'object' ? v : {};
  } catch {
    return {};
  }
}
export function saveOpen(v: Partial<Record<SectionKey, boolean>>) {
  try {
    localStorage.setItem(OPEN_KEY, JSON.stringify(v));
  } catch {
    /* storage blocked */
  }
}
