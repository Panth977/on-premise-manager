/**
 * Template sync (SPEC-final §21 #94, §6.1 "Template sync", §17.3 "Template sync").
 *
 * A deployment is DETACHED from its template: the draft is a deep copy made at create time and nothing reads the live
 * template afterwards. Template changes reach a deployment only through this manual 3-way review:
 *
 *   base   = the template's config at srcTemplate.baseVersion (stored in the deployment: srcTemplate.baseSnapshot)
 *   theirs = the template's config now (or another template: "Change template…" / "Link a template…")
 *   ours   = the deployment draft
 *
 * The configs are split into ITEMS with a stable key (lists keyed by their slug / name, never by position):
 *   variables/<slug>                       definition (everything but the value)
 *   variables/<slug>/value                 the template DEFAULT vs the deployment value (kind "default", opt-in only)
 *   secrets/<slug>                         definition + generate rule (values are never compared, shown or copied)
 *   files/<slug>                           metadata + content ref (sha256)
 *   sites/<site>                           a whole site (when the template or the draft does not have it)
 *   sites/<site>/settings/<field>          label host agentPort agentGuiAliasPort notFoundPageId depends
 *   sites/<site>/edge/<field>              httpPort httpsPort maxBodyBytes
 *   sites/<site>/edge/hosts/<hostname>     sites/<site>/edge/routes/<routeId>     sites/<site>/volumes/<volumeSlug>
 *   sites/<site>/devtools/<presetSlug>     a dev-tool preset (§21 #97 d; absent devTools = []; an empty list is omitted)
 *   sites/<site>/containers/<slug>         a whole container (when one side does not have it; a rename = remove + add)
 *   sites/<site>/containers/<slug>/<field> image (serviceSlug + version) enabled command entrypoint restart resources
 *                                          healthcheck depends ports volumes
 *   sites/<site>/containers/<slug>/env/<NAME>
 * A site / container present in BOTH the template and the draft is compared field by field (base fields absent when
 * the base lacks it), so "template bumped the image version" and "I changed an env value" never collide.
 *
 * Pure functions, no I/O: the backend (plan / preview / apply), the frontend mock and the tests share them.
 */
import { canonicalize } from './canonical.js';
import { maskDevToolLiterals } from './diff.js';
import type { DevToolPreset } from './devtools.js';
import type {
  Config,
  ConfigView,
  Container,
  DiffSection,
  SecretDef,
  SecretDefView,
  Site,
  TemplateSyncChange,
  TemplateSyncChoice,
  TemplateSyncCounts,
  TemplateSyncKind,
  TemplateSyncMissing,
  TemplateSyncOp,
  TemplateSyncSkip,
  Variable,
  VariableValue,
} from './types.js';

type AnyConfig = Config | ConfigView;
/** a value on one side; has = false: the item does not exist there */
export interface SyncSlot {
  has: boolean;
  v: unknown;
}
const NONE: SyncSlot = { has: false, v: null };
const slot = (v: unknown): SyncSlot => (v === undefined ? NONE : { has: true, v });
const clone = <T>(v: T): T => (v === undefined ? v : (JSON.parse(JSON.stringify(v)) as T));

function canon(v: unknown): string {
  try {
    return canonicalize(v);
  } catch {
    return JSON.stringify(v) ?? 'undefined';
  }
}
const same = (a: SyncSlot, b: SyncSlot) => a.has === b.has && (!a.has || canon(a.v) === canon(b.v));
const isScalar = (v: unknown) => v === null || typeof v === 'string' || typeof v === 'number' || typeof v === 'boolean';

/** Stable short hash (cyrb53, 2 × 32 bit → 16 hex) of an item value; "-" = absent. Used by skip records. */
export function templateSyncHash(s: SyncSlot | unknown): string {
  const x = s && typeof s === 'object' && 'has' in (s as object) && 'v' in (s as object) ? (s as SyncSlot) : slot(s);
  if (!x.has) return '-';
  const str = canon(x.v);
  let h1 = 0xdeadbeef;
  let h2 = 0x41c6ce57;
  for (let i = 0; i < str.length; i++) {
    const ch = str.charCodeAt(i);
    h1 = Math.imul(h1 ^ ch, 2654435761);
    h2 = Math.imul(h2 ^ ch, 1597334677);
  }
  h1 = Math.imul(h1 ^ (h1 >>> 16), 2246822507) ^ Math.imul(h2 ^ (h2 >>> 13), 3266489909);
  h2 = Math.imul(h2 ^ (h2 >>> 16), 2246822507) ^ Math.imul(h1 ^ (h1 >>> 13), 3266489909);
  return (h2 >>> 0).toString(16).padStart(8, '0') + (h1 >>> 0).toString(16).padStart(8, '0');
}

// ───────────────────────────────────────── items
interface Unit {
  key: string;
  section: DiffSection;
  siteSlug: string | null;
  label: string;
  /** 'value' = a variable default (kind "default", never a conflict) */
  role: 'item' | 'value';
  b: SyncSlot;
  t: SyncSlot;
  o: SyncSlot;
  /** write the chosen slot into cfg (the draft being built) */
  set: (cfg: Config, s: SyncSlot) => void;
}

const varDef = (v: Variable) => {
  const { value, ...def } = v;
  void value;
  return def;
};
const secretDef = (s: SecretDef | SecretDefView) => ({ secretSlug: s.secretSlug, label: s.label, description: s.description, required: s.required, generate: s.generate ?? null });

/** Insert / replace / remove one keyed item of a list; new items go after their predecessor in the template order. */
function upsert<T>(list: T[], keyOf: (x: T) => string, key: string, value: T | undefined, order: string[]) {
  const i = list.findIndex((x) => keyOf(x) === key);
  if (value === undefined) {
    if (i >= 0) list.splice(i, 1);
    return;
  }
  if (i >= 0) {
    list[i] = value;
    return;
  }
  const at = order.indexOf(key);
  if (at >= 0) {
    for (let k = at - 1; k >= 0; k--) {
      const j = list.findIndex((x) => keyOf(x) === order[k]);
      if (j >= 0) return void list.splice(j + 1, 0, value);
    }
    for (let k = at + 1; k < order.length; k++) {
      const j = list.findIndex((x) => keyOf(x) === order[k]);
      if (j >= 0) return void list.splice(j, 0, value);
    }
  }
  list.push(value);
}
const byKey = <T>(list: readonly T[] | undefined, keyOf: (x: T) => string) => {
  const m = new Map<string, T>();
  for (const x of list ?? []) m.set(keyOf(x), x);
  return m;
};
const keysOf = <T>(lists: (readonly T[] | undefined)[], keyOf: (x: T) => string) => {
  const out: string[] = [];
  for (const l of lists) for (const x of l ?? []) if (!out.includes(keyOf(x))) out.push(keyOf(x));
  return out;
};
const siteOf = (cfg: Config, slug: string) => cfg.sites.find((s) => s.siteSlug === slug);
const containerOf = (cfg: Config, site: string, c: string) => siteOf(cfg, site)?.config.containers.find((x) => x.containerSlug === c);

const SITE_SETTINGS = ['label', 'host', 'agentPort', 'agentGuiAliasPort', 'notFoundPageId', 'depends'] as const;
const EDGE_FIELDS = ['httpPort', 'httpsPort', 'maxBodyBytes'] as const;
const CONTAINER_FIELDS = ['image', 'enabled', 'command', 'entrypoint', 'restart', 'resources', 'healthcheck', 'depends', 'ports', 'volumes'] as const;
type ContainerField = (typeof CONTAINER_FIELDS)[number];
const containerField = (c: Container | undefined, f: ContainerField): SyncSlot => {
  if (!c) return NONE;
  if (f === 'image') return slot({ serviceSlug: c.serviceSlug, version: c.version });
  return slot(c[f] ?? null);
};

function buildUnits(base: AnyConfig | null, theirs: AnyConfig, ours: AnyConfig): Unit[] {
  const B: AnyConfig = base ?? { variables: [], secrets: [], files: [], sites: [] };
  const units: Unit[] = [];
  const push = (u: Unit) => units.push(u);

  // variables: definition + (opt-in) default value
  {
    const bm = byKey(B.variables, (x) => x.variableSlug);
    const tm = byKey(theirs.variables, (x) => x.variableSlug);
    const om = byKey(ours.variables, (x) => x.variableSlug);
    const order = theirs.variables.map((x) => x.variableSlug);
    for (const slug of keysOf([B.variables, theirs.variables, ours.variables], (x) => x.variableSlug)) {
      const bv = bm.get(slug);
      const tv = tm.get(slug);
      const ov = om.get(slug);
      push({
        key: `variables/${slug}`,
        section: 'variables',
        siteSlug: null,
        label: `Variable ${slug}`,
        role: 'item',
        b: slot(bv && varDef(bv)),
        t: slot(tv && varDef(tv)),
        o: slot(ov && varDef(ov)),
        set: (cfg, s) => {
          const cur = cfg.variables.find((x) => x.variableSlug === slug);
          // the value belongs to the deployment: keep it; a NEW variable starts with the template default
          const next = s.has ? ({ ...clone(s.v as Omit<Variable, 'value'>), value: cur ? cur.value : clone(tv?.value ?? null) } as Variable) : undefined;
          upsert(cfg.variables, (x) => x.variableSlug, slug, next, order);
        },
      });
      if (tv && ov) {
        const tb = bv ? canon(bv.value ?? null) !== canon(tv.value ?? null) : true; // the template changed its default (or no base)
        if (tb && canon(tv.value ?? null) !== canon(ov.value ?? null))
          push({
            key: `variables/${slug}/value`,
            section: 'variables',
            siteSlug: null,
            label: `Variable ${slug} — value`,
            role: 'value',
            b: slot(bv ? (bv.value ?? null) : undefined),
            t: slot(tv.value ?? null),
            o: slot(ov.value ?? null),
            set: (cfg, s) => {
              const cur = cfg.variables.find((x) => x.variableSlug === slug);
              if (cur && s.has) cur.value = clone(s.v as VariableValue);
            },
          });
      }
    }
  }
  // secrets: definitions + generate rules only
  {
    const bm = byKey(B.secrets as (SecretDef | SecretDefView)[], (x) => x.secretSlug);
    const tm = byKey(theirs.secrets as (SecretDef | SecretDefView)[], (x) => x.secretSlug);
    const om = byKey(ours.secrets as (SecretDef | SecretDefView)[], (x) => x.secretSlug);
    const order = theirs.secrets.map((x) => x.secretSlug);
    for (const slug of keysOf<SecretDef | SecretDefView>([B.secrets, theirs.secrets, ours.secrets], (x) => x.secretSlug)) {
      const b = bm.get(slug);
      const t = tm.get(slug);
      const o = om.get(slug);
      push({
        key: `secrets/${slug}`,
        section: 'secrets',
        siteSlug: null,
        label: `Secret ${slug}`,
        role: 'item',
        b: slot(b && secretDef(b)),
        t: slot(t && secretDef(t)),
        o: slot(o && secretDef(o)),
        set: (cfg, s) => {
          const cur = cfg.secrets.find((x) => x.secretSlug === slug);
          // values never come from the template: keep the draft value; a new secret starts without one (the cloud
          // generates it when the secret has a generate rule, like at create)
          const next = s.has ? ({ ...clone(s.v as object), value: cur ? cur.value : null, rotatedAt: cur ? cur.rotatedAt : null } as SecretDef) : undefined;
          upsert(cfg.secrets, (x) => x.secretSlug, slug, next, order);
        },
      });
    }
  }
  // files: metadata + content ref
  {
    const bm = byKey(B.files, (x) => x.fileSlug);
    const tm = byKey(theirs.files, (x) => x.fileSlug);
    const om = byKey(ours.files, (x) => x.fileSlug);
    const order = theirs.files.map((x) => x.fileSlug);
    for (const slug of keysOf([B.files, theirs.files, ours.files], (x) => x.fileSlug))
      push({
        key: `files/${slug}`,
        section: 'files',
        siteSlug: null,
        label: `File ${slug}`,
        role: 'item',
        b: slot(bm.get(slug)),
        t: slot(tm.get(slug)),
        o: slot(om.get(slug)),
        set: (cfg, s) => upsert(cfg.files, (x) => x.fileSlug, slug, s.has ? clone(s.v as Config['files'][number]) : undefined, order),
      });
  }
  // sites
  {
    const bm = byKey(B.sites, (x) => x.siteSlug);
    const tm = byKey(theirs.sites, (x) => x.siteSlug);
    const om = byKey(ours.sites, (x) => x.siteSlug);
    const order = theirs.sites.map((x) => x.siteSlug);
    for (const ss of keysOf([B.sites, theirs.sites, ours.sites], (x) => x.siteSlug)) {
      const b = bm.get(ss);
      const t = tm.get(ss);
      const o = om.get(ss);
      if (!t || !o) {
        push({
          key: `sites/${ss}`,
          section: 'sites',
          siteSlug: ss,
          label: `Site ${ss}`,
          role: 'item',
          b: slot(b),
          t: slot(t),
          o: slot(o),
          set: (cfg, s) => upsert(cfg.sites, (x) => x.siteSlug, ss, s.has ? clone(s.v as Site) : undefined, order),
        });
        continue;
      }
      siteUnits(ss, b, t, o, push);
    }
  }
  return units;
}

function siteUnits(ss: string, b: Site | undefined, t: Site, o: Site, push: (u: Unit) => void) {
  const pre = `sites/${ss}`;
  for (const f of SITE_SETTINGS) {
    const get = (s: Site | undefined): SyncSlot => (!s ? NONE : f === 'label' ? slot(s.label) : slot((s.config as unknown as Record<string, unknown>)[f] ?? null));
    push({
      key: `${pre}/settings/${f}`,
      section: 'sites',
      siteSlug: ss,
      label: `${ss} · ${f}`,
      role: 'item',
      b: get(b),
      t: get(t),
      o: get(o),
      set: (cfg, s) => {
        const site = siteOf(cfg, ss);
        if (!site || !s.has) return;
        if (f === 'label') site.label = s.v as string;
        else (site.config as unknown as Record<string, unknown>)[f] = clone(s.v);
      },
    });
  }
  for (const f of EDGE_FIELDS) {
    const get = (s: Site | undefined): SyncSlot => (!s ? NONE : slot((s.config.edge as unknown as Record<string, unknown>)[f] ?? null));
    push({
      key: `${pre}/edge/${f}`,
      section: 'edge',
      siteSlug: ss,
      label: `${ss} · edge ${f}`,
      role: 'item',
      b: get(b),
      t: get(t),
      o: get(o),
      set: (cfg, s) => {
        const site = siteOf(cfg, ss);
        if (site && s.has) (site.config.edge as unknown as Record<string, unknown>)[f] = clone(s.v);
      },
    });
  }
  const keyed = <T>(
    lists: [T[] | undefined, T[], T[]],
    keyOf: (x: T) => string,
    section: DiffSection,
    seg: string,
    label: (k: string) => string,
    listOf: (site: Site) => T[],
  ) => {
    const [bl, tl, ol] = lists;
    const bm = byKey(bl, keyOf);
    const tm = byKey(tl, keyOf);
    const om = byKey(ol, keyOf);
    const order = tl.map(keyOf);
    for (const k of keysOf([bl, tl, ol], keyOf))
      push({
        key: `${pre}/${seg}/${k}`,
        section,
        siteSlug: ss,
        label: label(k),
        role: 'item',
        b: slot(bm.get(k)),
        t: slot(tm.get(k)),
        o: slot(om.get(k)),
        set: (cfg, s) => {
          const site = siteOf(cfg, ss);
          if (site) upsert(listOf(site), keyOf, k, s.has ? clone(s.v as T) : undefined, order);
        },
      });
  };
  keyed([b?.config.edge.hosts, t.config.edge.hosts, o.config.edge.hosts], (x) => x.hostname, 'edge', 'edge/hosts', (k) => `${ss} · edge host ${k}`, (s) => s.config.edge.hosts);
  keyed([b?.config.edge.routes, t.config.edge.routes, o.config.edge.routes], (x) => x.routeId, 'edge', 'edge/routes', (k) => `${ss} · edge route ${k}`, (s) => s.config.edge.routes);
  keyed([b?.config.volumes, t.config.volumes ?? [], o.config.volumes ?? []], (x) => x.volumeSlug, 'volumes', 'volumes', (k) => `${ss} · volume ${k}`, (s) => (s.config.volumes ??= []));
  keyed([b?.config.devTools, t.config.devTools ?? [], o.config.devTools ?? []], (x) => x.presetSlug, 'devTools', 'devtools', (k) => `${ss} · dev tool ${k}`, (s) => (s.config.devTools ??= []));
  // containers
  const bm = byKey(b?.config.containers, (x) => x.containerSlug);
  const tm = byKey(t.config.containers, (x) => x.containerSlug);
  const om = byKey(o.config.containers, (x) => x.containerSlug);
  const order = t.config.containers.map((x) => x.containerSlug);
  for (const cs of keysOf([b?.config.containers, t.config.containers, o.config.containers], (x) => x.containerSlug)) {
    const bc = bm.get(cs);
    const tc = tm.get(cs);
    const oc = om.get(cs);
    const cpre = `${pre}/containers/${cs}`;
    if (!tc || !oc) {
      push({
        key: cpre,
        section: 'containers',
        siteSlug: ss,
        label: `${ss} · container ${cs}`,
        role: 'item',
        b: slot(bc),
        t: slot(tc),
        o: slot(oc),
        set: (cfg, s) => {
          const site = siteOf(cfg, ss);
          if (site) upsert(site.config.containers, (x) => x.containerSlug, cs, s.has ? clone(s.v as Container) : undefined, order);
        },
      });
      continue;
    }
    for (const f of CONTAINER_FIELDS)
      push({
        key: `${cpre}/${f}`,
        section: 'containers',
        siteSlug: ss,
        label: `${ss} · ${cs} · ${f}`,
        role: 'item',
        b: containerField(bc, f),
        t: containerField(tc, f),
        o: containerField(oc, f),
        set: (cfg, s) => {
          const c = containerOf(cfg, ss, cs);
          if (!c || !s.has) return;
          if (f === 'image') {
            const v = s.v as { serviceSlug: string; version: string };
            c.serviceSlug = v.serviceSlug;
            c.version = v.version;
          } else (c as unknown as Record<string, unknown>)[f] = clone(s.v);
        },
      });
    // env rows compare as their value (a string / null) or { secretRef } — like the Diff page — so a conflict on an
    // env VALUE can be resolved with a merged value inline
    const envVal = (e: Container['env'][number] | undefined): SyncSlot => (!e ? NONE : slot(e.secretRef ? { secretRef: e.secretRef } : (e.value ?? null)));
    const ebm = byKey(bc?.env, (x) => x.name);
    const etm = byKey(tc.env, (x) => x.name);
    const eom = byKey(oc.env, (x) => x.name);
    const eorder = tc.env.map((x) => x.name);
    for (const name of keysOf([bc?.env, tc.env, oc.env], (x) => x.name))
      push({
        key: `${cpre}/env/${name}`,
        section: 'containers',
        siteSlug: ss,
        label: `${ss} · ${cs} · env ${name}`,
        role: 'item',
        b: envVal(ebm.get(name)),
        t: envVal(etm.get(name)),
        o: envVal(eom.get(name)),
        set: (cfg, s) => {
          const c = containerOf(cfg, ss, cs);
          if (!c) return;
          const v = s.v as string | null | { secretRef: string };
          const row = !s.has ? undefined : v && typeof v === 'object' ? { name, value: null, secretRef: v.secretRef } : { name, value: v === null ? null : String(v), secretRef: null };
          upsert(c.env, (x) => x.name, name, row, eorder);
        },
      });
  }
}

// ───────────────────────────────────────── classification
const opOf = (from: SyncSlot, to: SyncSlot): TemplateSyncOp => (!from.has && to.has ? 'added' : from.has && !to.has ? 'removed' : !same(from, to) ? 'changed' : 'none');

/** The 3-way rule of one item (null = nothing to show: neither side changed it). */
export function classifySync(b: SyncSlot, t: SyncSlot, o: SyncSlot): Exclude<TemplateSyncKind, 'skipped' | 'default'> | null {
  const tb = same(t, b);
  const ob = same(o, b);
  if (tb && ob) return null;
  if (tb) return 'ours';
  if (ob) return 'theirs';
  if (same(t, o)) return 'same';
  return 'conflict';
}

function kindOf(u: Unit, skips: Map<string, TemplateSyncSkip>): TemplateSyncKind | null {
  if (u.role === 'value') return 'default';
  const k = classifySync(u.b, u.t, u.o);
  if (k === 'ours') {
    const s = skips.get(u.key);
    if (s && s.theirsHash === templateSyncHash(u.t)) return 'skipped';
  }
  return k;
}

const shown = (u: Unit, v: unknown): unknown => (u.section === 'devTools' && v && typeof v === 'object' ? maskDevToolLiterals(v as DevToolPreset) : v);
const toChange = (u: Unit, kind: TemplateSyncKind, skip?: TemplateSyncSkip): TemplateSyncChange => ({
  key: u.key,
  section: u.section,
  siteSlug: u.siteSlug,
  label: u.label,
  kind,
  theirsOp: opOf(u.b, u.t),
  oursOp: opOf(u.b, u.o),
  // §21 #103: a preset's static password / token value is never shown (compared raw: theirsHash / kinds)
  base: u.b.has ? shown(u, u.b.v) : null,
  theirs: u.t.has ? shown(u, u.t.v) : null,
  ours: u.o.has ? shown(u, u.o.v) : null,
  present: { base: u.b.has, theirs: u.t.has, ours: u.o.has },
  scalar: (!u.t.has || isScalar(u.t.v)) && (!u.o.has || isScalar(u.o.v)) && (u.t.has || u.o.has),
  theirsHash: templateSyncHash(u.t),
  ...(kind === 'skipped' && skip ? { skippedAt: skip.skippedAt } : {}),
});

/**
 * The 3-way plan: every item that the template or the draft changed since the base (base null = no base: everything
 * the template has is an addition). `skipped` = skip records of the earlier syncs (TemplateSyncSkip).
 */
export function planTemplateSync(base: AnyConfig | null, theirs: AnyConfig, ours: AnyConfig, skipped: readonly TemplateSyncSkip[] = []): TemplateSyncChange[] {
  const skips = new Map(skipped.map((s) => [s.key, s]));
  const out: TemplateSyncChange[] = [];
  for (const u of buildUnits(base, theirs, ours)) {
    const k = kindOf(u, skips);
    if (k) out.push(toChange(u, k, skips.get(u.key)));
  }
  return out;
}

export function countTemplateSync(changes: readonly Pick<TemplateSyncChange, 'kind'>[]): TemplateSyncCounts {
  const c: TemplateSyncCounts = { theirs: 0, ours: 0, same: 0, conflict: 0, skipped: 0, default: 0, templateChanges: 0 };
  for (const x of changes) c[x.kind]++;
  c.templateChanges = c.theirs + c.conflict + c.same + c.default;
  return c;
}

/** kinds a user can take (checkbox); conflicts need an explicit choice */
export const SYNC_TAKEABLE: readonly TemplateSyncKind[] = ['theirs', 'conflict', 'skipped', 'default'];
/** "Select all clean template changes" = every kind theirs (never conflicts, skipped or defaults) */
export const cleanTemplateChoices = (changes: readonly TemplateSyncChange[]): TemplateSyncChoice[] => changes.filter((c) => c.kind === 'theirs').map((c) => ({ key: c.key, take: 'theirs' as const }));

export interface TemplateSyncApplyResult {
  config: Config;
  /** keys taken (theirs / custom) */
  taken: string[];
  /** template changes NOT taken (kinds theirs / conflict / skipped): the backend records them as skipped */
  notTaken: TemplateSyncChange[];
  /** conflicts without a choice */
  unresolved: string[];
  /** bad choices (unknown key = the plan moved, custom on a non-scalar, values for unknown variables) */
  errors: { key: string; message: string }[];
  plan: TemplateSyncChange[];
}

/**
 * Apply the chosen items to a copy of OURS (the draft, with its secret values). Nothing else changes: items not taken
 * keep the draft's value. `values` = variable values entered in the review (applied last).
 */
export function applyTemplateSync(
  base: AnyConfig | null,
  theirs: AnyConfig,
  ours: Config,
  choices: readonly TemplateSyncChoice[],
  opts: { skipped?: readonly TemplateSyncSkip[]; values?: Record<string, VariableValue> } = {},
): TemplateSyncApplyResult {
  const skips = new Map((opts.skipped ?? []).map((s) => [s.key, s]));
  const units = buildUnits(base, theirs, ours);
  const cfg = clone(ours);
  const byKeyChoice = new Map<string, TemplateSyncChoice>();
  const errors: { key: string; message: string }[] = [];
  for (const c of choices) {
    if (!c || typeof c.key !== 'string') continue;
    if (byKeyChoice.has(c.key)) errors.push({ key: c.key, message: 'chosen twice' });
    byKeyChoice.set(c.key, c);
  }
  const taken: string[] = [];
  const notTaken: TemplateSyncChange[] = [];
  const unresolved: string[] = [];
  const plan: TemplateSyncChange[] = [];
  const seen = new Set<string>();
  for (const u of units) {
    const kind = kindOf(u, skips);
    if (!kind) continue;
    const ch = toChange(u, kind, skips.get(u.key));
    plan.push(ch);
    seen.add(u.key);
    const choice = byKeyChoice.get(u.key);
    if (!SYNC_TAKEABLE.includes(kind)) continue; // ours / same: nothing to take
    if (!choice || choice.take === 'ours') {
      if (kind === 'conflict' && !choice) unresolved.push(u.key);
      if (kind !== 'default') notTaken.push(ch);
      continue;
    }
    if (choice.take === 'theirs') u.set(cfg, u.t);
    else if (choice.take === 'custom') {
      if (!ch.scalar || !isScalar(choice.value)) {
        errors.push({ key: u.key, message: 'a merged value is only possible for a scalar item (string, number, true / false, null)' });
        continue;
      }
      u.set(cfg, { has: true, v: choice.value });
    } else {
      errors.push({ key: u.key, message: `take must be theirs, ours or custom` });
      continue;
    }
    taken.push(u.key);
  }
  for (const k of byKeyChoice.keys()) if (!seen.has(k)) errors.push({ key: k, message: 'no such change in the plan (the template or the draft changed — refresh)' });
  for (const [slug, value] of Object.entries(opts.values ?? {})) {
    const v = cfg.variables.find((x) => x.variableSlug === slug);
    if (!v) errors.push({ key: `variables/${slug}/value`, message: `variable ${slug} is not in the resulting draft` });
    else v.value = clone(value);
  }
  // §21 #97 a: an empty preset list is never written (the key stays absent)
  for (const s of cfg.sites) if (s?.config && Array.isArray(s.config.devTools) && s.config.devTools.length === 0) delete s.config.devTools;
  return { config: cfg, taken, notTaken, unresolved, errors, plan };
}

/** Required variables without a value and required secrets that cannot be generated and have no value (after a sync). */
export function templateSyncMissing(cfg: AnyConfig): TemplateSyncMissing {
  return {
    variables: cfg.variables
      .filter((v) => v.required && (v.value === null || v.value === '' || v.value === undefined))
      .map((v) => ({ variableSlug: v.variableSlug, label: v.label, type: v.type, options: v.options ?? null, required: v.required })),
    secrets: (cfg.secrets as (SecretDef | SecretDefView)[])
      .filter((s) => s.required && !s.generate && ('isSet' in s ? !s.isSet : s.value === null || s.value === '' || s.value === undefined))
      .map((s) => ({ secretSlug: s.secretSlug, label: s.label })),
  };
}

/** New skip records after an apply: template changes not taken (kept from earlier syncs when unchanged). */
export function nextSkipRecords(notTaken: readonly TemplateSyncChange[], previous: readonly TemplateSyncSkip[], templateVersion: number, now: string, uid: string): TemplateSyncSkip[] {
  const prev = new Map(previous.map((s) => [s.key, s]));
  return notTaken.map((c) => {
    const p = prev.get(c.key);
    if (p && p.theirsHash === c.theirsHash) return p;
    return { key: c.key, theirsHash: c.theirsHash, templateVersion, skippedAt: now, skippedBy: uid };
  });
}

/** The config part of a template (what is compared). */
export const templateConfig = (t: Pick<Config, 'variables' | 'secrets' | 'files' | 'sites'>): Config => ({ variables: t.variables, secrets: t.secrets, files: t.files, sites: t.sites });
