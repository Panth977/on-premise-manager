/**
 * Dev tools presets (§21 #97) — view helpers for the site "Dev tools" section of the template / deployment editors.
 * The contract is `@opm/shared` devtools.ts (DevToolPreset & co.); `SiteConfig.devTools` is OMITTED when empty (§21 #97 a).
 */
import type { Config, DevToolCredential, DevToolKind, DevToolPreset, Site, SiteConfig, ValidationIssue } from '@opm/shared';
import { credentialText } from '$opm/forms/devtools/devtools';
import { DEVTOOL_CONTAINER_ENV_MIN_AGENT, DEVTOOL_KIND_LABEL, DEVTOOL_LITERAL_MIN_AGENT, compareAgentVersions, devToolContainerEnvRefs, devToolLiteralFields, devToolTargetText, siteFilterAgentNeed } from '@opm/shared';
import type { IconNode } from '../../ui/icons';
import { Webhook, Database, FileBox, Columns3, Zap, Radio } from '../../ui/icons';

/** Agent version from which a site accepts revisions with presets (§21 #97 a, k). */
export const DEVTOOLS_MIN_AGENT = '0.11.0';
/** §21 #99: agent version from which presets may use {{ containers.<c>.env.<NAME> }} (field devtool). */
export const DEVTOOLS_ENV_MIN_AGENT = DEVTOOL_CONTAINER_ENV_MIN_AGENT;
/** §21 #103: agent version from which a preset may hold a static password / token value (Value mode). */
export const DEVTOOLS_LITERAL_MIN_AGENT = DEVTOOL_LITERAL_MIN_AGENT;

export const KIND_ICON: Record<DevToolKind, IconNode> = { http: Webhook, postgres: Database, sqlite: FileBox, cassandra: Columns3, redis: Zap, mqtt: Radio };
export const KIND_BLURB: Record<DevToolKind, string> = {
  http: 'Postman-like requests to a container, host or URL',
  postgres: 'SQL editor, schema browser, explain',
  sqlite: 'A database file in a site volume or a container path',
  cassandra: 'CQL editor and keyspace browser',
  redis: 'Key browser and console',
  mqtt: 'Publish and live subscriptions',
};
/** One line: "user postgres · password: secret pg-password · db acme". */
export function authSummary(p: DevToolPreset): string {
  const a = p.auth;
  const parts: string[] = [];
  const cred = (label: string, c: DevToolCredential) => {
    const t = credentialText(c);
    if (t) parts.push(`${label}: ${t}`);
  };
  switch (p.kind) {
    case 'sqlite':
      return 'no auth (file)';
    case 'http':
      if (a.httpScheme === 'none') return 'no auth';
      if (a.httpScheme === 'basic') {
        parts.push(`basic ${a.username ?? '?'}`);
        cred('password', a.password);
      } else if (a.httpScheme === 'bearer') {
        parts.push('bearer');
        cred('token', a.token);
      } else {
        parts.push(`api key ${a.apiKeyHeader || 'X-API-Key'}`);
        cred('token', a.token);
      }
      break;
    case 'postgres':
    case 'cassandra':
      if (a.username) parts.push(`user ${a.username}`);
      cred('password', a.password);
      if (a.database) parts.push(`${p.kind === 'postgres' ? 'db' : 'keyspace'} ${a.database}`);
      break;
    case 'redis':
      if (a.username) parts.push(`user ${a.username}`);
      cred('password', a.password);
      parts.push(`db ${a.database ?? '0'}`);
      break;
    case 'mqtt':
      if (a.username) parts.push(`user ${a.username}`);
      cred('password', a.password);
      if (a.clientId) parts.push(`client ${a.clientId}`);
      break;
  }
  return parts.length ? parts.join(' · ') : 'no auth';
}

/** Issues of one preset: path `sites[S].config.devTools[<slug>]…` (or `[<index>]`). */
export function presetIssues(issues: ValidationIssue[], siteSlug: string, slug: string, index: number): ValidationIssue[] {
  const b = `sites[${siteSlug}].config.devTools`;
  const keys = [`${b}[${slug}]`, `${b}[${index}]`];
  return issues.filter((i) => keys.some((k) => i.path === k || i.path.startsWith(k + '.') || i.path.startsWith(k + '[')));
}
/** Issues of the list itself (V_DEVTOOL_MAX …). */
export function listIssues(issues: ValidationIssue[], siteSlug: string): ValidationIssue[] {
  const b = `sites[${siteSlug}].config.devTools`;
  return issues.filter((i) => i.path === b);
}

/** Store the list on the site; an empty list REMOVES the key (never `devTools: []`, §21 #97 a). */
export function setSiteDevTools(sc: SiteConfig, list: DevToolPreset[]) {
  if (list.length) sc.devTools = list;
  else delete sc.devTools;
}
/** Remove an empty `devTools` key from every site before a write (defensive; the editors never create one). */
export function stripEmptyDevTools<T extends Pick<Site, 'config'>>(site: T): T {
  if (site.config.devTools && !site.config.devTools.length) delete site.config.devTools;
  return site;
}

/** core semver (x.y.z) of an agent version ("0.10.1-10081328" → "0.10.1") */
function core(v: string): string | null {
  return /^v?(\d+\.\d+\.\d+)/.exec(v.trim())?.[1] ?? null;
}
/** false when the reported agent version is older than `min` (default 0.11.0; unknown / unparsable → true: nothing to warn about). */
export function agentSupportsDevTools(agentVersion: string | null | undefined, min: string = DEVTOOLS_MIN_AGENT): boolean {
  if (!agentVersion) return true;
  const c = core(agentVersion);
  return !c || compareAgentVersions(c, min) >= 0;
}
const maxVersion = (a: string, b: string) => (compareAgentVersions(a, b) >= 0 ? a : b);
/**
 * What the presets of a site need: 0.11.0 for presets, 0.11.1 when one uses {{ containers.<c>.env.<NAME> }} (§21 #99),
 * 0.13.1 when one holds a static password / token value (§21 #103). `literals` = "<presetSlug>.<field>".
 */
export function devToolsAgentNeed(presets: readonly DevToolPreset[]): { min: string; envRefs: string[]; literals: string[] } {
  const envRefs = [...new Set(presets.flatMap((p) => devToolContainerEnvRefs(p)))].sort();
  const literals = presets.flatMap((p) => devToolLiteralFields(p).map((f) => `${p.presetSlug}.${f}`));
  let min = presets.length ? DEVTOOLS_MIN_AGENT : '0.0.0';
  if (envRefs.length) min = maxVersion(min, DEVTOOLS_ENV_MIN_AGENT);
  if (literals.length) min = maxVersion(min, DEVTOOLS_LITERAL_MIN_AGENT);
  return { min, envRefs, literals };
}
export interface OldAgentSite {
  siteSlug: string;
  agentVersion: string;
  /** the version the site's config needs */
  min: string;
  /** "<container>.<NAME>" of {{ containers.<c>.env.<NAME> }} uses (set when only those need the newer agent) */
  envRefs: string[];
  /** §21 #103 "<presetSlug>.<field>" of static password / token values */
  literals?: string[];
  /** §21 #103 "<filter> (<where>)" of expression filters that need a newer agent (siteFilterAgentNeed) */
  filterUses?: string[];
  /** false = the site has no presets (only filters need the newer agent) */
  hasPresets?: boolean;
}
export function devToolsAgentWarning(o: Pick<OldAgentSite, 'siteSlug' | 'agentVersion' | 'envRefs' | 'literals' | 'filterUses' | 'hasPresets'>): string {
  const v = o.agentVersion;
  const parts: string[] = [];
  const presetsOld = o.hasPresets !== false && !agentSupportsDevTools(v, DEVTOOLS_MIN_AGENT);
  if (presetsOld) parts.push(`dev tool presets need agent ${DEVTOOLS_MIN_AGENT} or newer${o.envRefs.length ? `, and a container env in a preset needs ${DEVTOOLS_ENV_MIN_AGENT}` : ''}`);
  else if (o.envRefs.length && !agentSupportsDevTools(v, DEVTOOLS_ENV_MIN_AGENT)) {
    const first = o.envRefs[0]!.replace('.', '.env.');
    const more = o.envRefs.length > 1 ? ` and ${o.envRefs.length - 1} more` : '';
    parts.push(`a preset uses a container env ({{ containers.${first} }}${more}), which needs agent ${DEVTOOLS_ENV_MIN_AGENT} or newer`);
  }
  const lits = o.literals ?? [];
  if (lits.length && !agentSupportsDevTools(v, DEVTOOLS_LITERAL_MIN_AGENT))
    parts.push(`a preset holds a static password / token value (${lits.join(', ')}), which needs agent ${DEVTOOLS_LITERAL_MIN_AGENT} or newer`);
  const fu = o.filterUses ?? [];
  if (fu.length && !agentSupportsDevTools(v, DEVTOOLS_LITERAL_MIN_AGENT))
    parts.push(`the expression filter${fu.length > 1 ? 's' : ''} ${fu.join(', ')} need${fu.length > 1 ? '' : 's'} agent ${DEVTOOLS_LITERAL_MIN_AGENT} or newer`);
  return `Site ${o.siteSlug} runs agent ${v}; ${parts.join('; ')} — upgrade the agent first, otherwise the site refuses the revision.`;
}
/**
 * Sites of a config that need a newer agent than the one they report: dev-tool presets (0.11.0; containers.*.env
 * 0.11.1; static values 0.13.1) and §21 #103 expression filters (sha256 / sha512 / mosquitto_passwd: 0.13.1) in the site
 * config and in the rendered Files its containers mount (`fileContents`: fileSlug → source text, when loaded).
 */
export function oldAgentSites(config: Pick<Config, 'sites' | 'files'>, versions: readonly { siteSlug: string; agentVersion: string | null }[], fileContents?: Record<string, string>): OldAgentSite[] {
  const out: OldAgentSite[] = [];
  for (const s of config.sites) {
    const v = versions.find((x) => x.siteSlug === s.siteSlug)?.agentVersion ?? null;
    if (!v) continue;
    const presets = s.config.devTools ?? [];
    const need = devToolsAgentNeed(presets);
    const filters = siteFilterAgentNeed(config, s.siteSlug, fileContents);
    const min = filters ? maxVersion(need.min, filters.min) : need.min;
    if (!presets.length && !filters) continue;
    if (!agentSupportsDevTools(v, min)) out.push({ siteSlug: s.siteSlug, agentVersion: v, ...need, min, filterUses: filters?.uses ?? [], hasPresets: presets.length > 0 });
  }
  return out;
}

/** "Plant database — PostgreSQL · db-host:5432 · user postgres · password: secret pg-password · read-only" of a preset-like diff value (else null). */
export function presetDiffSummary(v: unknown): string | null {
  if (!v || typeof v !== 'object' || Array.isArray(v)) return null;
  const p = v as Partial<DevToolPreset>;
  if (!p.kind || !p.target || !p.auth || !(p.kind in KIND_ICON)) return null;
  try {
    const line = [DEVTOOL_KIND_LABEL[p.kind], devToolTargetText(p.target), authSummary(p as DevToolPreset), p.readOnly ? 'read-only' : null].filter(Boolean).join(' · ');
    return p.name ? `${p.name} — ${line}` : line;
  } catch {
    return null;
  }
}
