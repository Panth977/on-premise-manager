/**
 * Dev tools (§7.6, §21 #97 / #99 / #102) — the data side of the ONE dev-tool form (DevToolForm): picker sources for the
 * cloud preset editor (ConfigHost) and for the agent connection drawer (DevToolsInfo), credential helpers, the agent's
 * manual-connection checks, preset ⇄ connection conversion and the "unused fields reset" cleanup.
 */
import type { ExprContext } from '@opm/shared/expr';
import type { Config, Site, ValidationIssue } from '@opm/shared/types';
import { imageKey, normalizeStoredDevToolPreset, siteDevtoolExprContext, type CatalogImage } from '@opm/shared/validate';
import {
  DEVTOOL_DEFAULT_PORT,
  containerEnvOfExpr,
  defaultDevToolTarget,
  type DevToolConnection,
  type DevToolConnectionInput,
  type DevToolCredential,
  type DevToolKind,
  type DevToolPreset,
  type DevToolTarget,
  type DevToolTargetKind,
  type DevToolsInfo,
} from '@opm/shared/devtools';
import type { SelectOption } from '../../primitives/listbox';
import type { ConfigHost } from '../host';
import { secretOptions } from '../pickers';

export const TARGET_LABEL: Record<DevToolTargetKind, string> = {
  container: 'Container',
  host: 'Host : port',
  url: 'Base URL',
  'volume-file': 'Volume file',
  'container-file': 'Container path',
};
export const KIND_ICON: Record<DevToolKind, string> = { http: 'send', postgres: 'database', sqlite: 'file', cassandra: 'columns', redis: 'layers', mqtt: 'radio' };
export const READONLY_HINT: Record<DevToolKind, string> = {
  postgres: 'Runs inside BEGIN READ ONLY: the server refuses writes.',
  sqlite: 'Opens the file read-only (mode=ro + query_only).',
  cassandra: 'Write statements are refused by the agent.',
  redis: 'Write commands are refused by the agent.',
  mqtt: 'No publish — subscriptions only.',
  http: 'Only GET / HEAD / OPTIONS.',
};

/** §21 #99: one env var of a container (names + secret flag; detail = preview or "••• secret"). */
export interface EnvChoice {
  name: string;
  secret: boolean;
  detail: string;
}
/** The env vars of a container in a devtool expression context (sorted by name). */
export function containerEnvChoices(ctx: ExprContext | null, containerSlug: string | null | undefined): EnvChoice[] {
  if (!ctx || !containerSlug) return [];
  const env = ctx.containers?.[containerSlug]?.env ?? {};
  return Object.entries(env)
    .map(([name, e]) => ({ name, secret: e.secret, detail: e.error ? e.error.code : e.secret ? '••• secret' : e.value === null ? 'no value' : e.value.length > 40 ? `${e.value.slice(0, 39)}…` : e.value }))
    .sort((a, b) => a.name.localeCompare(b.name));
}

/** `{{ secret.x }}` (only that) → x */
const SECRET_ONLY = /^\{\{\s*secret\.([a-z][a-z0-9-]*)\s*\}\}$/;
export function secretOfCredential(c: DevToolCredential): string | null {
  return c.kind === 'expr' ? (SECRET_ONLY.exec(c.expr.trim())?.[1] ?? null) : null;
}
export const secretExpr = (slug: string) => `{{ secret.${slug} }}`;
/** "secret pg-password" · "env POSTGRES_PASSWORD of postgres" · "expression …" · "stored value" · null (none) */
export function credentialText(c: DevToolCredential): string | null {
  if (c.kind === 'none') return null;
  if (c.kind === 'literal') return 'static value';
  const s = secretOfCredential(c);
  if (s) return `secret ${s}`;
  const e = containerEnvOfExpr(c.expr);
  return e ? `env ${e.name} of ${e.containerSlug}` : `expression ${c.expr}`;
}

/**
 * Everything the form's pickers need — built from a ConfigHost (cloud presets) or from DevToolsInfo (agent connections
 * and the agent's read-only preset view), so the form itself depends on neither.
 */
export interface DevToolSources {
  ctx: ExprContext | null;
  containers: SelectOption[];
  ports(containerSlug: string, kind: DevToolKind): SelectOption[];
  defaultPort(containerSlug: string, kind: DevToolKind): string;
  volumes: SelectOption[];
  /** mounts of a container (container-file target: click to fill the path) */
  mounts?(containerSlug: string): { mountPath: string; label: string }[];
  envChoices(containerSlug: string | null): EnvChoice[];
  secrets: { value: string; label?: string; hint?: string }[];
  defaultTarget(kind: DevToolKind, tk: DevToolTargetKind): DevToolTarget;
  /** agent defaults shown as placeholders (timeout / rows) */
  limits?: { statementTimeoutSec: number; maxRows: number };
}

/** Cloud editor (template / deployment) — a site of the working config. */
export function presetSources(host: ConfigHost, siteSlug: string): DevToolSources {
  const cfg = host.config();
  const site = cfg.sites.find((s) => s.siteSlug === siteSlug);
  const sc = site?.config;
  const images: Record<string, CatalogImage> = {};
  for (const s of cfg.sites)
    for (const c of s.config.containers) {
      const img = host.image(c.serviceSlug, c.version);
      if (img) images[imageKey(c.serviceSlug, c.version)] = { kind: img.kind, ports: img.ports, env: img.env, volumes: img.volumes, healthcheck: img.healthcheck };
    }
  const secretsSet = host.secretIsSet ? cfg.secrets.filter((s) => host.secretIsSet!(s.secretSlug)).map((s) => s.secretSlug) : undefined;
  let ctx: ExprContext | null = null;
  try {
    ctx = siteDevtoolExprContext(cfg, { deploymentId: host.deploymentId, secretsSet, images, services: {}, mode: host.scope === 'template' ? 'template' : host.scope === 'agent' ? 'agent' : 'draft' }, siteSlug);
  } catch {
    ctx = null;
  }
  const imgPorts = (containerSlug: string) => {
    const c = sc?.containers.find((x) => x.containerSlug === containerSlug);
    return c ? (host.image(c.serviceSlug, c.version)?.ports ?? []) : [];
  };
  const guess = (kind: DevToolKind): string => {
    const list = sc?.containers ?? [];
    if (!list.length) return '';
    if (kind !== 'sqlite') {
      const want = DEVTOOL_DEFAULT_PORT[kind];
      const byPort = list.find((c) => imgPorts(c.containerSlug).some((p) => p.port === want));
      if (byPort) return byPort.containerSlug;
    }
    const k = kind === 'postgres' ? 'pg' : kind;
    return (list.find((c) => c.containerSlug.includes(k) || c.serviceSlug.includes(kind === 'postgres' ? 'postgres' : kind)) ?? list[0]!).containerSlug;
  };
  const defaultPort = (containerSlug: string, kind: DevToolKind) => {
    const ports = imgPorts(containerSlug);
    if (kind === 'sqlite') return '';
    return (ports.find((p) => p.port === DEVTOOL_DEFAULT_PORT[kind]) ?? ports[0])?.name ?? '';
  };
  return {
    ctx,
    containers: (sc?.containers ?? []).map((c) => ({ value: c.containerSlug, label: c.containerSlug, hint: `${c.serviceSlug}:${c.version}`, icon: 'container', badge: c.enabled ? undefined : { text: 'disabled', tone: 'amber' as const } })),
    ports: (cs, kind) => imgPorts(cs).map((p) => ({ value: p.name, label: `${p.name} · ${p.port}/${p.protocol}`, hint: p.description || undefined, badge: kind !== 'sqlite' && p.port === DEVTOOL_DEFAULT_PORT[kind] ? { text: 'default', tone: 'blue' as const } : undefined })),
    defaultPort,
    volumes: (sc?.volumes ?? []).map((v) => ({ value: v.volumeSlug, label: v.volumeSlug, hint: v.label && v.label !== v.volumeSlug ? v.label : undefined, icon: v.kind === 'file' ? 'file' : 'hardDrive', badge: v.kind === 'file' ? { text: 'single file', tone: 'gray' as const } : undefined, disabled: v.kind === 'file' })),
    envChoices: (cs) => containerEnvChoices(ctx, cs),
    secrets: secretOptions(host),
    defaultTarget: (kind, tk) => {
      switch (tk) {
        case 'container': {
          const c = guess(kind);
          return { kind: 'container', containerSlug: c, portName: c ? defaultPort(c, kind) : '' };
        }
        case 'host':
          return { kind: 'host', host: '', port: kind === 'sqlite' ? 0 : DEVTOOL_DEFAULT_PORT[kind] };
        case 'url':
          return { kind: 'url', url: 'http://' };
        case 'volume-file':
          return { kind: 'volume-file', volumeSlug: (sc?.volumes ?? []).find((v) => v.kind !== 'file')?.volumeSlug ?? '', path: '' };
        case 'container-file':
          return { kind: 'container-file', containerSlug: guess(kind), path: '' };
      }
    },
  };
}

/** Agent dev tools (manual connections + the read-only preset view) — from GET devtools/info. */
export function infoSources(info: DevToolsInfo, siteSlug: string): DevToolSources {
  const ctx: ExprContext = {
    deployment: { id: '' },
    self: { slug: siteSlug, host: null },
    var: Object.fromEntries(info.variables.map((v) => [v, `(variable ${v})`])),
    secret: Object.fromEntries(info.secrets.map((s) => [s, null])),
    containers: Object.fromEntries(
      info.containers.map((c) => [
        c.containerSlug,
        { host: c.containerSlug, enabled: true, port: Object.fromEntries(c.ports.map((p) => [p.portName, p.containerPort])), env: Object.fromEntries((c.env ?? []).map((e) => [e.name, { value: e.secret ? '•••' : `(env ${e.name})`, secret: e.secret }])) },
      ]),
    ),
  } as ExprContext;
  const ofC = (cs: string) => info.containers.find((c) => c.containerSlug === cs);
  const defaultPort = (cs: string, kind: DevToolKind) => {
    const ports = ofC(cs)?.ports ?? [];
    if (kind === 'sqlite') return '';
    return (ports.find((p) => p.containerPort === DEVTOOL_DEFAULT_PORT[kind]) ?? ports[0])?.portName ?? '';
  };
  return {
    ctx,
    containers: info.containers.map((c) => ({ value: c.containerSlug, label: c.containerSlug, hint: c.image, icon: 'container', badge: c.running ? undefined : { text: 'stopped', tone: 'amber' as const } })),
    ports: (cs, kind) =>
      (ofC(cs)?.ports ?? []).map((p) => ({ value: p.portName, label: `${p.portName} · ${p.containerPort}/${p.protocol}`, hint: p.hostPort ? `host ${p.hostPort}` : undefined, badge: kind !== 'sqlite' && p.containerPort === DEVTOOL_DEFAULT_PORT[kind] ? { text: 'default', tone: 'blue' as const } : undefined })),
    defaultPort,
    volumes: info.volumes.map((v) => ({ value: v.volumeSlug, label: v.volumeSlug, hint: v.label || undefined, icon: v.kind === 'file' ? 'file' : 'hardDrive', badge: v.kind === 'file' ? { text: 'single file', tone: 'gray' as const } : undefined, disabled: v.kind === 'file' })),
    mounts: (cs) => (ofC(cs)?.mounts ?? []).map((m) => ({ mountPath: m.mountPath, label: m.volumeSlug ?? m.kind })),
    envChoices: (cs) => (cs ? (ofC(cs)?.env ?? []).map((e) => ({ name: e.name, secret: e.secret, detail: e.secret ? '••• secret' : 'plain value' })).sort((a, b) => a.name.localeCompare(b.name)) : []),
    secrets: info.secrets.map((s) => ({ value: s })),
    defaultTarget: (kind, tk) => {
      if (tk === 'container') {
        const want = kind === 'sqlite' ? -1 : DEVTOOL_DEFAULT_PORT[kind];
        const hit = info.containers.find((c) => c.ports.some((p) => p.containerPort === want));
        return hit ? { kind: 'container', containerSlug: hit.containerSlug, portName: defaultPort(hit.containerSlug, kind) } : { kind: 'container', containerSlug: '', portName: '' };
      }
      if (tk === 'host') return { kind: 'host', host: '', port: kind === 'sqlite' ? 0 : DEVTOOL_DEFAULT_PORT[kind] };
      if (tk === 'url') return { kind: 'url', url: 'https://' };
      if (tk === 'volume-file') return { kind: 'volume-file', volumeSlug: info.volumes.find((v) => v.kind !== 'file')?.volumeSlug ?? '', path: '' };
      return { kind: 'container-file', containerSlug: '', path: '' };
    },
    limits: { statementTimeoutSec: info.limits.statementTimeoutSec, maxRows: info.limits.maxRows },
  };
}

/** The working object of the form: a preset (presetSlug '' for a connection). */
export type DevToolDraft = DevToolPreset;

/** plain deep copy (works on Svelte $state proxies, unlike structuredClone) */
const plain = <T>(v: T): T => JSON.parse(JSON.stringify(v)) as T;

export function presetToDraft(p: DevToolPreset): DevToolDraft {
  return plain(p);
}
/** A connection (API shape) → the form's working object. */
export function connectionToDraft(c: DevToolConnection | DevToolConnectionInput): DevToolDraft {
  const id = 'id' in c && c.id.startsWith('p:') ? c.id.slice(2) : '';
  return plain({ presetSlug: id, name: c.name, description: c.description, kind: c.kind, target: c.target, auth: c.auth, options: c.options, readOnly: c.readOnly });
}
/** The form's working object → a manual connection body (empty optional texts → null, empty header rows dropped). */
export function draftToConnectionInput(d: DevToolDraft): DevToolConnectionInput {
  const x = plain({ name: d.name.trim(), description: d.description, kind: d.kind, target: d.target, auth: d.auth, options: d.options, readOnly: d.readOnly });
  x.options.defaultHeaders = x.options.defaultHeaders.filter((h) => h.name.trim());
  for (const k of ['username', 'database', 'clientId', 'apiKeyHeader'] as const) if (x.auth[k] !== null && x.auth[k]!.trim() === '') x.auth[k] = null;
  return x;
}
/** A blank connection of a kind (agent "Add connection"), target prefilled from the sources. */
export function blankDraft(kind: DevToolKind, src?: DevToolSources): DevToolDraft {
  const d: DevToolDraft = { presetSlug: '', name: '', description: '', kind, target: defaultDevToolTarget(kind), auth: { username: null, password: { kind: 'none' }, database: null, clientId: null, httpScheme: 'none', token: { kind: 'none' }, apiKeyHeader: null }, options: { tls: false, tlsInsecure: false, tlsServerName: null, sslMode: null, timeoutSec: null, maxRows: null, consistency: null, scheme: null, basePath: null, followRedirects: null, defaultHeaders: [], cleanSession: null, keepAliveSec: null }, readOnly: false };
  if (src && d.target.kind === 'container') d.target = src.defaultTarget(kind, 'container');
  return d;
}

/** Fields the kind does not use are reset, so the stored preset / connection carries no ignored values. */
export function cleanedDraft(src: DevToolDraft): DevToolDraft {
  const x = plain(src);
  const a = x.auth;
  const none: DevToolCredential = { kind: 'none' };
  if (x.kind !== 'http') ((a.httpScheme = 'none'), (a.token = none), (a.apiKeyHeader = null));
  if (x.kind !== 'mqtt') a.clientId = null;
  if (x.kind === 'sqlite') ((a.username = null), (a.password = none), (a.database = null));
  if (x.kind === 'http') {
    if (a.httpScheme !== 'basic') ((a.username = null), (a.password = none));
    if (a.httpScheme === 'none' || a.httpScheme === 'basic') a.token = none;
    if (a.httpScheme !== 'api-key') a.apiKeyHeader = null;
    a.database = null;
  }
  const o = x.options;
  if (x.kind !== 'postgres') o.sslMode = null;
  if (x.kind !== 'cassandra') o.consistency = null;
  if (x.kind !== 'mqtt') ((o.cleanSession = null), (o.keepAliveSec = null));
  if (x.kind !== 'http') ((o.scheme = null), (o.basePath = null), (o.followRedirects = null), (o.defaultHeaders = []));
  if (x.kind === 'http' && x.target.kind === 'url') o.scheme = null;
  if (!['postgres', 'sqlite', 'cassandra'].includes(x.kind)) o.maxRows = null;
  if (x.kind === 'sqlite') ((o.tls = false), (o.tlsInsecure = false), (o.tlsServerName = null));
  if (!o.tls && x.kind !== 'http' && x.kind !== 'postgres') o.tlsServerName = null;
  x.name = x.name.trim();
  return x;
}
/** The stored preset shape (cloud: put it into SiteConfig.devTools of the site working copy). */
export function draftToPreset(d: DevToolDraft): DevToolPreset {
  return normalizeStoredDevToolPreset(cleanedDraft(d));
}

/**
 * Cloud preset: shared validateSiteDevTools (through host.validate over the config with this preset in place) + the slug
 * checks of a new preset. Paths returned RELATIVE to the preset (`name`, `target.containerSlug`, `auth.password` …).
 */
export function presetIssues(host: ConfigHost, siteSlug: string, d: DevToolDraft, o: { editing: boolean; original?: string | null }): ValidationIssue[] {
  const cfg: Config = host.config();
  const site = cfg.sites.find((s) => s.siteSlug === siteSlug);
  if (!site) return [];
  const cur = normalizeStoredDevToolPreset(plain(d));
  const others = (site.config.devTools ?? []).filter((x) => x.presetSlug !== (o.original ?? (o.editing ? d.presetSlug : null)));
  const probe: Site = { ...site, config: { ...site.config, devTools: [...others, cur] } };
  const all = host.validate({ ...cfg, sites: cfg.sites.map((s) => (s.siteSlug === siteSlug ? probe : s)) });
  const pp = `sites[${siteSlug}].config.devTools[${d.presetSlug}]`;
  const out = all.filter((i) => i.path === pp || i.path.startsWith(pp + '.')).map((i) => ({ ...i, path: i.path === pp ? '' : i.path.slice(pp.length + 1) }));
  if (!o.editing) {
    if (!d.presetSlug) out.unshift({ path: 'presetSlug', code: 'V_DEVTOOL_SLUG', message: 'Required', severity: 'error' });
    else if (others.some((x) => x.presetSlug === d.presetSlug)) out.unshift({ path: 'presetSlug', code: 'V_DEVTOOL_DUP', message: `site ${siteSlug} already has a preset ${d.presetSlug}`, severity: 'error' });
  }
  return out;
}

/** Agent manual connection: the checks the agent GUI ran before saving (paths relative, like presetIssues). */
export function connectionIssues(f: DevToolDraft): ValidationIssue[] {
  const out: ValidationIssue[] = [];
  const e = (path: string, message: string) => out.push({ path, code: 'V_DEVTOOL', message, severity: 'error' });
  if (!f.name.trim()) e('name', 'Give the connection a name.');
  const t = f.target;
  if (t.kind === 'container') {
    if (!t.containerSlug) e('target.containerSlug', 'Pick a container.');
    else if (!t.portName) e('target.portName', 'Pick a port.');
  } else if (t.kind === 'host') {
    if (!t.host.trim()) e('target.host', 'Host name or IP.');
    else if (/\{\{/.test(t.host)) e('target.host', 'No {{ }} expressions in a host target.');
    if (!(t.port >= 1 && t.port <= 65535)) e('target.port', 'Port 1–65535.');
  } else if (t.kind === 'url') {
    if (!/^https?:\/\/\S+/.test(t.url)) e('target.url', 'An absolute http:// or https:// URL.');
  } else if (t.kind === 'volume-file') {
    if (!t.volumeSlug) e('target.volumeSlug', 'Pick a volume.');
    if (!t.path.trim()) e('target.path', 'The database file inside the volume, e.g. app.db.');
    else if (t.path.startsWith('/') || t.path.split('/').includes('..')) e('target.path', 'A relative path inside the volume (no leading "/", no "..").');
  } else if (t.kind === 'container-file') {
    if (!t.containerSlug) e('target.containerSlug', 'Pick a container.');
    if (!t.path.startsWith('/')) e('target.path', 'An absolute path inside the container.');
  }
  if (f.kind === 'postgres' && !f.auth.username?.trim()) e('auth.username', 'PostgreSQL needs a user.');
  if (f.kind === 'cassandra' && !!f.auth.username?.trim() !== (f.auth.password.kind !== 'none')) e('auth.username', 'Cassandra: user and password both, or neither.');
  if (f.kind === 'http' && f.auth.httpScheme === 'basic' && !f.auth.username?.trim()) e('auth.username', 'Basic auth needs a user.');
  if (f.kind === 'http' && (f.auth.httpScheme === 'bearer' || f.auth.httpScheme === 'api-key') && f.auth.token.kind === 'none') e('auth.token', 'Add the token.');
  if (f.options.basePath && !f.options.basePath.startsWith('/')) e('options.basePath', 'Starts with "/".');
  if (f.options.timeoutSec !== null && !(f.options.timeoutSec >= 1 && f.options.timeoutSec <= 3600)) e('options.timeoutSec', 'Timeout 1–3600 s.');
  return out;
}
