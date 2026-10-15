/**
 * Edge helpers of the shared edge forms (SPEC §12, §21 #102) — one copy for both GUIs (were frontend config-editor/edge.ts
 * and agent/gui lib/edge.ts). Image lookups go through the ConfigHost, never an app store.
 */
import type { Config, Edge, EdgeHost, EdgeResponseHeaders, EdgeRoute, Site, ValidationIssue } from '@opm/shared/types';
import type { ExprContext } from '@opm/shared/expr';
import { isExpr, tryResolve } from '@opm/shared/expr';
import { containerPorts, imagePortNumber, varValues } from '@opm/shared/validate';
import { formatBytes, type EdgeOrderEntry } from '@opm/shared/edge-resolve';
import type { NginxImportContainer } from '@opm/shared/nginx-import';
import type { ConfigHost } from '../host';
import { issuesUnder } from '../host';

function res(v: string, ctx: ExprContext | null): string {
  if (!ctx || !isExpr(v)) return v;
  const r = tryResolve(v, ctx, 'edge');
  return r.ok ? r.result.value : v;
}
const resMap = (m: Record<string, string>, ctx: ExprContext | null) => Object.fromEntries(Object.entries(m).map(([k, v]) => [k, res(v, ctx)]));
const resHeaders = (h: EdgeResponseHeaders, ctx: ExprContext | null): EdgeResponseHeaders => ({ ...h, set: resMap(h.set, ctx), add: resMap(h.add, ctx) });

/** The edge with every `{{ }}` value resolved (hostnames, redirect.to, rewrite replacements, header values); unresolvable values stay as written. */
export function resolvedEdge(edge: Edge, ctx: ExprContext | null): Edge {
  const hostMap = new Map(edge.hosts.map((h) => [h.hostname, res(h.hostname, ctx)]));
  return {
    ...edge,
    hosts: edge.hosts.map((h) => ({ ...h, hostname: hostMap.get(h.hostname)!, responseHeaders: resHeaders(h.responseHeaders, ctx) })),
    routes: edge.routes.map((r) => {
      const a = r.action;
      const action: EdgeRoute['action'] =
        a.type === 'redirect'
          ? { ...a, to: res(a.to, ctx) }
          : a.type === 'rewrite'
            ? { ...a, replacement: res(a.replacement, ctx) }
            : { ...a, setHeaders: resMap(a.setHeaders, ctx), rewrite: a.rewrite ? { ...a.rewrite, replacement: res(a.rewrite.replacement, ctx) } : null };
      return { ...r, hosts: r.hosts.map((x) => hostMap.get(x) ?? res(x, ctx)), action, responseHeaders: r.responseHeaders ? resHeaders(r.responseHeaders, ctx) : null };
    }),
  };
}
/** containerSlug → { portName → container port } for resolveEdgeRequest. */
export function portMaps(host: ConfigHost, site: Site): Record<string, Record<string, number>> {
  const vars = varValues(host.config());
  const out: Record<string, Record<string, number>> = {};
  for (const c of site.config.containers) {
    const m: Record<string, number> = {};
    for (const p of host.image(c.serviceSlug, c.version)?.ports ?? []) {
      const n = imagePortNumber(p, vars, host.deploymentId);
      if (n !== null) m[p.name] = n;
    }
    out[c.containerSlug] = m;
  }
  return out;
}
/** The containers of a site as the nginx importer sees them (image port numbers resolved, edge flag per port). */
export function importContainers(host: ConfigHost, site: Site): NginxImportContainer[] {
  const vars = varValues(host.config());
  return site.config.containers.map((c) => {
    const img = host.image(c.serviceSlug, c.version);
    const edgeOf = img ? Object.fromEntries(containerPorts(c, img).map((p) => [p.portName, p.edge])) : {};
    return {
      containerSlug: c.containerSlug,
      ports: (img?.ports ?? []).flatMap((p) => {
        const n = imagePortNumber(p, vars, host.deploymentId);
        return n === null ? [] : [{ portName: p.name, port: n, edge: edgeOf[p.name] !== false }];
      }),
    };
  });
}
/** Validate the whole working config with this site replaced by a candidate; returns the issues under `prefix`. */
export function issuesWithSite(host: ConfigHost, site: Site, prefix: string): ValidationIssue[] {
  const cfg = host.config();
  const candidate: Config = { ...cfg, sites: cfg.sites.map((s) => (s.siteSlug === site.siteSlug ? site : s)) };
  return issuesUnder(host.validate(candidate), prefix);
}

export const KIND_LABEL = { prefix: 'prefix', exact: 'exact', regex: 'regex' } as const;
/** nginx-like match text: "= /x", "^~ /x", "~* re", "/x/" */
export function matchText(r: EdgeRoute): string {
  const m = r.match;
  if (m.kind === 'exact') return `= ${m.path}`;
  if (m.kind === 'regex') return `${m.path.startsWith('(?i)') ? '~*' : '~'} ${m.path.replace(/^\(\?i\)/, '')}`;
  return `${m.stopRegex ? '^~ ' : ''}${m.path}`;
}
/** One-line action summary: proxy → container:port + path handling; redirect → status to; rewrite → regex → replacement. */
export function actionSummary(r: EdgeRoute): string {
  const a = r.action;
  if (a.type === 'redirect') return `${a.status} → ${a.to}${a.keepPath ? ' (+path)' : ''}`;
  if (a.type === 'rewrite') return `${a.regex} → ${a.replacement}`;
  const how = a.rewrite ? ` · rewrite ${a.rewrite.regex} → ${a.rewrite.replacement}` : a.stripPrefix ? ' · strip' : '';
  return `${a.containerSlug || '?'}:${a.portName || '?'}${how}${a.addPrefix ? ` · +${a.addPrefix}` : ''}`;
}
/** Short label of an effective-order group: exact / ^~ prefix / regex #n / prefix. */
export function groupLabel(o: EdgeOrderEntry, all: readonly EdgeOrderEntry[]): string {
  if (o.group === 'regex') return `regex #${all.filter((x) => x.group === 'regex').findIndex((x) => x.routeId === o.routeId) + 1}`;
  return o.group === 'prefix-stop' ? '^~ prefix' : o.group;
}
export const GROUP_TONE = { exact: 'green', 'prefix-stop': 'violet', regex: 'amber', prefix: 'blue' } as const;
export function headersCount(h: EdgeResponseHeaders | null | undefined): number {
  if (!h) return 0;
  return Object.keys(h.set).length + Object.keys(h.add).length + h.remove.length;
}
/** The host whose response headers a route inherits (its first host filter, else the first host). */
export function inheritHost(edge: Edge, r: Pick<EdgeRoute, 'hosts'>): EdgeHost | null {
  const n = r.hosts[0]?.toLowerCase();
  return (n ? edge.hosts.find((h) => h.hostname.toLowerCase() === n) : null) ?? edge.hosts[0] ?? null;
}
export function bytesText(n: number | null): string {
  return formatBytes(n);
}
/** Request URL for a hostname of the edge (previews): "*" / wildcards → a concrete name; https when it redirects to https. */
export function exampleUrl(edge: Edge, host: EdgeHost | null, pathAndQuery: string): string {
  const raw = host?.hostname ?? 'localhost';
  const name = raw === '*' || raw.includes('{{') ? 'localhost' : raw.startsWith('*.') ? 'www' + raw.slice(1) : raw;
  const https = !!host && host.redirectHttpToHttps && host.tls.mode !== 'none' && edge.httpsPort !== null;
  const port = https ? edge.httpsPort! : edge.httpPort;
  const hostPart = name.includes(':') && !name.startsWith('[') ? `[${name}]` : name;
  const p = pathAndQuery.startsWith('/') ? pathAndQuery : '/' + pathAndQuery;
  return `${https ? 'https' : 'http'}://${hostPart}${(https && port === 443) || (!https && port === 80) ? '' : ':' + port}${p}`;
}
/** route id from a path ("/x/ez-apis/" → "x-ez-apis"), unique among taken */
export function suggestRouteId(path: string, taken: readonly string[]): string {
  let b = path
    .toLowerCase()
    .replace(/\(\?i\)/g, '')
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 50);
  if (!b) b = 'route';
  if (!taken.includes(b)) return b;
  for (let i = 2; ; i++) if (!taken.includes(`${b}-${i}`)) return `${b}-${i}`;
}
/** move a regex route one place up / down among the regex routes (the only order that matters); regex orders become 1..k */
export function moveRegex(routes: EdgeRoute[], id: string, d: -1 | 1) {
  const list = routes.filter((r) => r.match.kind === 'regex').sort((a, b) => a.order - b.order || (a.routeId < b.routeId ? -1 : 1));
  const i = list.findIndex((r) => r.routeId === id);
  const j = i + d;
  if (i < 0 || j < 0 || j >= list.length) return;
  [list[i], list[j]] = [list[j]!, list[i]!];
  list.forEach((r, k) => (r.order = k + 1));
}
/** next order for a new route: regex routes go last among the regex routes */
export function nextOrder(routes: readonly EdgeRoute[], kind: EdgeRoute['match']['kind']): number {
  const pool = kind === 'regex' ? routes.filter((r) => r.match.kind === 'regex') : routes;
  return Math.max(0, ...pool.map((r) => r.order)) + 1;
}
