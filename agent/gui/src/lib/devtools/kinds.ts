/** Dev tools (§21 #97): kind → page / icon / labels, connection helpers, per-site local preferences. */
import { DEVTOOL_KIND_LABEL, type DevToolConnection, type DevToolKind } from '@opm/shared/devtools';
import { appHref } from '$lib/nav';
import type { SelectOption } from '$lib/ui/listbox';

export type DevPage = 'sql' | 'redis' | 'mqtt' | 'http';
export const KIND_ICON: Record<DevToolKind, string> = { http: 'send', postgres: 'database', sqlite: 'file', cassandra: 'columns', redis: 'layers', mqtt: 'radio' };
export const KIND_SHORT: Record<DevToolKind, string> = { http: 'HTTP', postgres: 'PostgreSQL', sqlite: 'SQLite', cassandra: 'Cassandra', redis: 'Redis', mqtt: 'MQTT' };
export const PAGE_KINDS: Record<DevPage, DevToolKind[]> = { sql: ['postgres', 'sqlite', 'cassandra'], redis: ['redis'], mqtt: ['mqtt'], http: ['http'] };
export const PAGE_LABEL: Record<DevPage, string> = { sql: 'SQL', redis: 'Redis', mqtt: 'MQTT', http: 'HTTP client' };
export const PAGE_ICON: Record<DevPage, string> = { sql: 'database', redis: 'layers', mqtt: 'radio', http: 'send' };
export const pageOfKind = (k: DevToolKind): DevPage => (k === 'postgres' || k === 'sqlite' || k === 'cassandra' ? 'sql' : k);
export { DEVTOOL_KIND_LABEL };

/** the workbench of a connection */
export function connHref(site: string, c: Pick<DevToolConnection, 'id' | 'kind'>): string {
  return appHref(site, `devtools/${pageOfKind(c.kind)}?c=${encodeURIComponent(c.id)}`);
}

export function connOptions(list: DevToolConnection[], kinds: DevToolKind[]): SelectOption[] {
  return list
    .filter((c) => kinds.includes(c.kind))
    .map((c) => ({
      value: c.id,
      label: c.name,
      icon: KIND_ICON[c.kind],
      group: kinds.length > 1 ? KIND_SHORT[c.kind] : c.source === 'preset' ? 'Presets' : 'Manual',
      dot: c.problem ? 'red' : c.state.status === 'connected' ? 'green' : c.state.status === 'error' ? 'red' : 'gray',
      hint: c.source === 'preset' ? 'preset' : undefined,
      badge: c.readOnly ? { text: 'read-only', tone: 'amber' } : undefined,
    }));
}

/** remembered per browser: the last connection of a tool on a site (never anything secret) */
function lsGet(k: string): string | null {
  try {
    return localStorage.getItem(k);
  } catch {
    return null;
  }
}
function lsSet(k: string, v: string | null) {
  try {
    if (v === null) localStorage.removeItem(k);
    else localStorage.setItem(k, v);
  } catch {
    /* storage blocked */
  }
}
export const lastConn = {
  get: (site: string, page: DevPage) => lsGet(`opm.dev.last.${site}.${page}`),
  set: (site: string, page: DevPage, id: string) => lsSet(`opm.dev.last.${site}.${page}`, id),
};
export function loadJson<T>(k: string, fallback: T): T {
  const v = lsGet(k);
  if (!v) return fallback;
  try {
    return JSON.parse(v) as T;
  } catch {
    return fallback;
  }
}
export function saveJson(k: string, v: unknown) {
  lsSet(k, v === null ? null : JSON.stringify(v));
}

/** "connected · PostgreSQL 16.4" */
export function stateText(c: DevToolConnection): string {
  const s = c.state;
  if (s.status === 'connected') return `connected${s.serverVersion ? ` · ${s.serverVersion}` : ''}`;
  if (s.status === 'error') return `error${s.lastError ? ` · ${s.lastError}` : ''}`;
  return 'idle';
}
export const stateTone = (c: DevToolConnection) => (c.state.status === 'connected' ? 'green' : c.state.status === 'error' ? 'red' : 'gray');
