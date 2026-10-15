/**
 * Config diff (cloud /diff page, agent local-proposal preview, agent proposal detail, rollback plan).
 * Secrets never show values: before/after are "set" / "not set" / "changed" / "not changed".
 */
import type { Config, ConfigView, Container, DiffChange, Site, SiteVolume, VolumeTree } from './types.js';
import type { DevToolPreset } from './devtools.js';
import { canonicalize } from './canonical.js';

type AnyConfig = Config | ConfigView;
const j = (v: unknown) => JSON.stringify(v);

function secretState(s: { value: unknown; isSet?: boolean } | undefined): 'set' | 'not set' {
  if (!s) return 'not set';
  if ('isSet' in s && typeof s.isSet === 'boolean') return s.isSet ? 'set' : 'not set';
  return s.value !== null && s.value !== undefined && s.value !== '' ? 'set' : 'not set';
}

/**
 * @param serviceSlug kept for compatibility (containers are keyed by containerSlug; identity by default)
 * @param secretChanged optional: tells whether a secret value changed (needed when both sides are masked views)
 */
export function diffConfigs(before: AnyConfig | null, after: AnyConfig, serviceSlug: (slug: string) => string = (s) => s, secretChanged?: (slug: string) => boolean): DiffChange[] {
  const out: DiffChange[] = [];
  const b: AnyConfig = before ?? { variables: [], secrets: [], files: [], sites: [] };
  const push = (c: DiffChange) => out.push(c);
  // variables
  for (const v of after.variables) {
    const o = b.variables.find((x) => x.variableSlug === v.variableSlug);
    if (!o) push({ section: 'variables', siteSlug: null, path: v.variableSlug, kind: 'added', before: null, after: v.value });
    else if (j(o) !== j(v)) push({ section: 'variables', siteSlug: null, path: v.variableSlug, kind: 'changed', before: o.type !== v.type || j({ ...o, value: 0 }) !== j({ ...v, value: 0 }) ? o : o.value, after: o.type !== v.type || j({ ...o, value: 0 }) !== j({ ...v, value: 0 }) ? v : v.value });
  }
  for (const o of b.variables) if (!after.variables.some((x) => x.variableSlug === o.variableSlug)) push({ section: 'variables', siteSlug: null, path: o.variableSlug, kind: 'removed', before: o.value, after: null });
  // secrets
  for (const s of after.secrets) {
    const o = b.secrets.find((x) => x.secretSlug === s.secretSlug);
    if (!o) {
      push({ section: 'secrets', siteSlug: null, path: s.secretSlug, kind: 'added', before: null, after: secretState(s as never) });
      continue;
    }
    const defChanged = j({ ...o, value: null, isSet: null, rotatedAt: null }) !== j({ ...s, value: null, isSet: null, rotatedAt: null });
    const valueChanged = secretChanged ? secretChanged(s.secretSlug) : 'isSet' in s ? secretState(o as never) !== secretState(s as never) || o.rotatedAt !== s.rotatedAt : o.value !== s.value;
    if (defChanged || valueChanged) push({ section: 'secrets', siteSlug: null, path: s.secretSlug, kind: 'changed', before: secretState(o as never), after: valueChanged ? 'changed' : 'not changed' });
  }
  for (const o of b.secrets) if (!after.secrets.some((x) => x.secretSlug === o.secretSlug)) push({ section: 'secrets', siteSlug: null, path: o.secretSlug, kind: 'removed', before: secretState(o as never), after: null });
  // files
  for (const f of after.files) {
    const o = b.files.find((x) => x.fileSlug === f.fileSlug);
    if (!o) push({ section: 'files', siteSlug: null, path: f.fileSlug, kind: 'added', before: null, after: f.content });
    else if (j(o) !== j(f)) push({ section: 'files', siteSlug: null, path: f.fileSlug, kind: 'changed', before: j({ ...o, content: null }) === j({ ...f, content: null }) ? o.content : o, after: j({ ...o, content: null }) === j({ ...f, content: null }) ? f.content : f });
  }
  for (const o of b.files) if (!after.files.some((x) => x.fileSlug === o.fileSlug)) push({ section: 'files', siteSlug: null, path: o.fileSlug, kind: 'removed', before: o.content, after: null });
  // sites
  for (const s of after.sites) {
    const o = b.sites.find((x) => x.siteSlug === s.siteSlug);
    if (!o) {
      push({ section: 'sites', siteSlug: s.siteSlug, path: s.siteSlug, kind: 'added', before: null, after: { label: s.label, host: s.config.host } });
      for (const v of s.config.volumes ?? []) push({ section: 'volumes', siteSlug: s.siteSlug, path: `${s.siteSlug}/volumes/${v.volumeSlug}`, kind: 'added', before: null, after: v });
      for (const c of s.config.containers) push({ section: 'containers', siteSlug: s.siteSlug, path: `${s.siteSlug}/${serviceSlug(c.containerSlug)}`, kind: 'added', before: null, after: { service: c.serviceSlug, version: c.version } });
      diffDevTools([], s.config.devTools ?? [], s.siteSlug, push);
      continue;
    }
    diffSite(o, s, serviceSlug, push);
  }
  for (const o of b.sites)
    if (!after.sites.some((x) => x.siteSlug === o.siteSlug)) {
      push({ section: 'sites', siteSlug: o.siteSlug, path: o.siteSlug, kind: 'removed', before: { label: o.label, host: o.config.host }, after: null });
      diffDevTools(o.config.devTools ?? [], [], o.siteSlug, push);
    }
  return out;
}

function diffSite(o: Site, s: Site, slug: (id: string) => string, push: (c: DiffChange) => void) {
  const ss = s.siteSlug;
  for (const k of ['label'] as const) if (o[k] !== s[k]) push({ section: 'sites', siteSlug: ss, path: `${ss}/${k}`, kind: 'changed', before: o[k], after: s[k] });
  for (const k of ['host', 'agentPort', 'agentGuiAliasPort', 'notFoundPageId', 'depends'] as const)
    if (j(o.config[k]) !== j(s.config[k])) push({ section: 'sites', siteSlug: ss, path: `${ss}/${k}`, kind: 'changed', before: o.config[k], after: s.config[k] });
  // declared volumes (round D), keyed by volumeSlug
  const ov = o.config.volumes ?? [];
  const sv = s.config.volumes ?? [];
  for (const v of sv) {
    const x = ov.find((y) => y.volumeSlug === v.volumeSlug);
    if (!x) {
      push({ section: 'volumes', siteSlug: ss, path: `${ss}/volumes/${v.volumeSlug}`, kind: 'added', before: null, after: v });
      diffVolumeContent(null, v, ss, push);
    } else if (j(x) !== j(v)) {
      // §21 #101 own content (tree / single) is listed per file; the volume row changes only for the other fields
      const xo = contentOf(x.seed);
      const vo = contentOf(v.seed);
      const perFile = !!xo || !!vo;
      const rest = (y: SiteVolume) => ({ ...y, seed: contentOf(y.seed) ? (perFile ? 'content' : y.seed) : y.seed });
      const notes: string[] = [];
      if (j(x.seed ?? null) !== j(v.seed ?? null)) notes.push(VOLUME_SEED_CHANGED_NOTE);
      if ((x.kind ?? 'dir') !== (v.kind ?? 'dir')) notes.push(VOLUME_KIND_CHANGED_NOTE);
      if (!perFile || j(rest(x)) !== j(rest(v)) || (!!xo !== !!vo))
        push({ section: 'volumes', siteSlug: ss, path: `${ss}/volumes/${v.volumeSlug}`, kind: 'changed', before: x, after: v, ...(notes.length ? { note: notes.join(' · ') } : {}) });
      if (perFile) diffVolumeContent(x, v, ss, push);
    }
  }
  for (const x of ov) if (!sv.some((y) => y.volumeSlug === x.volumeSlug)) push({ section: 'volumes', siteSlug: ss, path: `${ss}/volumes/${x.volumeSlug}`, kind: 'removed', before: x, after: null });
  diffDevTools(o.config.devTools ?? [], s.config.devTools ?? [], ss, push);
  // edge
  const oe = o.config.edge;
  const se = s.config.edge;
  for (const k of ['httpPort', 'httpsPort'] as const) if (oe[k] !== se[k]) push({ section: 'edge', siteSlug: ss, path: `${ss}/edge/${k}`, kind: 'changed', before: oe[k], after: se[k] });
  for (const h of se.hosts) {
    const x = oe.hosts.find((y) => y.hostname === h.hostname);
    if (!x) push({ section: 'edge', siteSlug: ss, path: `${ss}/edge/hosts/${h.hostname}`, kind: 'added', before: null, after: h });
    else if (j(x) !== j(h)) push({ section: 'edge', siteSlug: ss, path: `${ss}/edge/hosts/${h.hostname}`, kind: 'changed', before: x, after: h });
  }
  for (const x of oe.hosts) if (!se.hosts.some((y) => y.hostname === x.hostname)) push({ section: 'edge', siteSlug: ss, path: `${ss}/edge/hosts/${x.hostname}`, kind: 'removed', before: x, after: null });
  for (const r of se.routes) {
    const x = oe.routes.find((y) => y.routeId === r.routeId);
    if (!x) push({ section: 'edge', siteSlug: ss, path: `${ss}/edge/routes/${r.routeId}`, kind: 'added', before: null, after: r });
    else if (j(x) !== j(r)) push({ section: 'edge', siteSlug: ss, path: `${ss}/edge/routes/${r.routeId}`, kind: 'changed', before: x, after: r });
  }
  for (const x of oe.routes) if (!se.routes.some((y) => y.routeId === x.routeId)) push({ section: 'edge', siteSlug: ss, path: `${ss}/edge/routes/${x.routeId}`, kind: 'removed', before: x, after: null });
  // containers
  for (const c of s.config.containers) {
    const x = o.config.containers.find((y) => y.containerSlug === c.containerSlug);
    const base = `${ss}/${slug(c.containerSlug)}`;
    if (!x) {
      push({ section: 'containers', siteSlug: ss, path: base, kind: 'added', before: null, after: { service: c.serviceSlug, version: c.version } });
      continue;
    }
    diffContainer(x, c, base, ss, push);
  }
  for (const x of o.config.containers) if (!s.config.containers.some((y) => y.containerSlug === x.containerSlug)) push({ section: 'containers', siteSlug: ss, path: `${ss}/${slug(x.containerSlug)}`, kind: 'removed', before: { service: x.serviceSlug, version: x.version }, after: null });
}

function diffContainer(o: Container, c: Container, base: string, ss: string, push: (c: DiffChange) => void) {
  for (const k of ['serviceSlug', 'version', 'enabled', 'command', 'entrypoint', 'restart', 'resources', 'healthcheck', 'depends', 'volumes', 'ports'] as const)
    if (j(o[k]) !== j(c[k])) push({ section: 'containers', siteSlug: ss, path: `${base}/${k}`, kind: 'changed', before: o[k], after: c[k] });
  for (const e of c.env) {
    const x = o.env.find((y) => y.name === e.name);
    if (!x) push({ section: 'containers', siteSlug: ss, path: `${base}/env/${e.name}`, kind: 'added', before: null, after: e.secretRef ? { secretRef: e.secretRef } : e.value });
    else if (j(x) !== j(e)) push({ section: 'containers', siteSlug: ss, path: `${base}/env/${e.name}`, kind: 'changed', before: x.secretRef ? { secretRef: x.secretRef } : x.value, after: e.secretRef ? { secretRef: e.secretRef } : e.value });
  }
  for (const x of o.env) if (!c.env.some((y) => y.name === x.name)) push({ section: 'containers', siteSlug: ss, path: `${base}/env/${x.name}`, kind: 'removed', before: x.secretRef ? { secretRef: x.secretRef } : x.value, after: null });
}
/**
 * Dev-tool presets of a site (§21 #97 d), keyed by presetSlug: path `<site>/devtools/<presetSlug>`. Values are shown as
 * they are (credentials are expressions such as {{ secret.x }}) EXCEPT static values (§21 #103: literal password / token):
 * compared raw, shown as "••••" — never in clear; a change of only the static value gets the note DEVTOOL_VALUE_CHANGED_NOTE.
 * Absent devTools = [].
 */
export const DEVTOOL_VALUE_CHANGED_NOTE = 'static password / token value changed (hidden)';
export const DEVTOOL_VALUE_MASK = '••••';
/** A preset with its static password / token value replaced by "••••" (diff, template sync, previews). */
export function maskDevToolLiterals<T extends DevToolPreset | null>(p: T): T {
  if (!p || typeof p !== 'object' || !p.auth) return p;
  const m = (c: DevToolPreset['auth']['password']) => (c && c.kind === 'literal' ? { kind: 'literal' as const, value: DEVTOOL_VALUE_MASK } : c);
  if (p.auth.password?.kind !== 'literal' && p.auth.token?.kind !== 'literal') return p;
  return { ...p, auth: { ...p.auth, password: m(p.auth.password), token: m(p.auth.token) } } as T;
}
const cj = (v: unknown) => {
  try {
    return canonicalize(v);
  } catch {
    return j(v);
  }
};
function diffDevTools(ol: readonly DevToolPreset[], sl: readonly DevToolPreset[], ss: string, push: (c: DiffChange) => void) {
  for (const p of sl) {
    const x = ol.find((y) => y.presetSlug === p.presetSlug);
    if (!x) push({ section: 'devTools', siteSlug: ss, path: `${ss}/devtools/${p.presetSlug}`, kind: 'added', before: null, after: maskDevToolLiterals(p) });
    else if (cj(x) !== cj(p)) {
      const mx = maskDevToolLiterals(x);
      const mp = maskDevToolLiterals(p);
      const c: DiffChange = { section: 'devTools', siteSlug: ss, path: `${ss}/devtools/${p.presetSlug}`, kind: 'changed', before: mx, after: mp };
      // both static and only the (hidden) value differs → say so, the masked rows look alike
      const lit = (q: DevToolPreset, f: 'password' | 'token') => (q.auth?.[f]?.kind === 'literal' ? String((q.auth[f] as { value: unknown }).value) : null);
      for (const f of ['password', 'token'] as const) if (lit(x, f) !== null && lit(p, f) !== null && lit(x, f) !== lit(p, f)) c.note = DEVTOOL_VALUE_CHANGED_NOTE;
      push(c);
    }
  }
  for (const x of ol) if (!sl.some((y) => y.presetSlug === x.presetSlug)) push({ section: 'devTools', siteSlug: ss, path: `${ss}/devtools/${x.presetSlug}`, kind: 'removed', before: maskDevToolLiterals(x), after: null });
}
/** §21 #101 the own content of a volume as a tree (single-file content = one entry at the path ""), else null. */
function contentOf(seed: SiteVolume['seed'] | undefined): VolumeTree | null {
  if (seed?.kind === 'tree') return { entries: seed.entries ?? [], dirs: seed.dirs ?? [] };
  if (seed?.kind === 'single') return { entries: [{ path: '', sha256: seed.sha256, size: seed.size, mode: seed.mode, render: seed.render }], dirs: [] };
  return null;
}
/** §21 #101 one DiffChange per changed file of a volume's own content: path `<site>/volumes/<v>/content/<path>` (single file: `…/content`). */
function diffVolumeContent(x: SiteVolume | null, v: SiteVolume, ss: string, push: (c: DiffChange) => void) {
  const a = contentOf(x?.seed);
  const b = contentOf(v.seed);
  if (!a && !b) return;
  const A = new Map((a?.entries ?? []).map((e) => [e.path, e] as const));
  const B = new Map((b?.entries ?? []).map((e) => [e.path, e] as const));
  const paths = [...new Set([...A.keys(), ...B.keys()])].sort();
  for (const p of paths) {
    const e0 = A.get(p) ?? null;
    const e1 = B.get(p) ?? null;
    if (e0 && e1 && e0.sha256 === e1.sha256 && e0.mode === e1.mode && e0.render === e1.render) continue;
    push({
      section: 'volumes',
      siteSlug: ss,
      path: `${ss}/volumes/${v.volumeSlug}/content${p ? `/${p}` : ''}`,
      kind: !e0 ? 'added' : !e1 ? 'removed' : 'changed',
      before: e0,
      after: e1,
      note: VOLUME_CONTENT_CHANGED_NOTE,
      blob: { volumeSlug: v.volumeSlug, path: p, before: e0?.sha256 ?? null, after: e1?.sha256 ?? null },
    });
  }
  const da = new Set(a?.dirs ?? []);
  const db = new Set(b?.dirs ?? []);
  for (const d of [...db].filter((d) => !da.has(d)).sort()) push({ section: 'volumes', siteSlug: ss, path: `${ss}/volumes/${v.volumeSlug}/content/${d}/`, kind: 'added', before: null, after: 'empty folder', note: VOLUME_CONTENT_CHANGED_NOTE });
  for (const d of [...da].filter((d) => !db.has(d)).sort()) push({ section: 'volumes', siteSlug: ss, path: `${ss}/volumes/${v.volumeSlug}/content/${d}/`, kind: 'removed', before: 'empty folder', after: null, note: VOLUME_CONTENT_CHANGED_NOTE });
}
/** §21 #101 */
export const VOLUME_CONTENT_CHANGED_NOTE = 'starter content — written only into new / empty volumes (persist-to-cloud volumes: use Send to site)';
/** Review hints for site volume changes (round E). */
export const VOLUME_SEED_CHANGED_NOTE = 'seed changed — applies only to new / empty volumes (existing data is never touched; use "Reset to starter content…" on the site)';
export const VOLUME_KIND_CHANGED_NOTE = 'kind changed — a site refuses this when the volume already holds data (use a new volume name instead)';
