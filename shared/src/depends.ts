/**
 * Container start order inside a site (SPEC-final §15.5, §21 #93): explicit depends, IMPLICIT depends (a
 * `containers.<slug>.*` expression in env, image env defaults or a mounted rendered file) and SUGGESTED depends (a literal
 * mention of another container's slug that looks like a hostname: `mqtt://mqtt:1883`, `host: "mqtt"`, `redis:6379`).
 * Go mirror: agent/internal/validate/depends.go; vectors: shared/testdata/depends-vectors.json.
 *
 * - explicit + implicit = "start after + wait ready" (the agent's APPLY waits for them).
 * - suggested = INFO only (container editor + publish review offer "add it to Depends on"); it only ORDERS the start
 *   (referenced containers first) and never blocks.
 * - startOrder: referenced containers (explicit, implicit or suggested) start before their referrers; ties keep the
 *   DECLARED order (site.containers); a cycle (only possible through implicit / suggested edges — explicit cycles are
 *   V_CYCLE errors) falls back to the declared order for the containers in it, with a warning.
 * - dnsFailureHint: a crash-looping container whose log says it could not resolve a container of the same site.
 */
import { exprRefs } from './expr.js';
import type { ContainerEnv, ContainerMount, FileDef, ImageEnv, SiteConfig } from './types.js';

export type DependKind = 'explicit' | 'implicit' | 'suggested';
/** Where a reference was found: "env MQTT_URL", "image env MQTT_URL", "file broker-conf". */
export interface InferredDepend {
  containerSlug: string;
  from: string[];
}
export interface ContainerDepends {
  containerSlug: string;
  /** Container.depends (enabled + disabled targets, as configured) */
  explicit: string[];
  /** containers.<slug>.* references to OTHER enabled containers that are not explicit (start after + wait ready) */
  implicit: InferredDepend[];
  /** literal hostname-like mentions of other enabled containers that are neither explicit nor implicit (INFO) */
  suggested: InferredDepend[];
}
export interface DependsInput {
  site: Pick<SiteConfig, 'containers'>;
  /** `${serviceSlug}:${version}` → the image env (defaults are used when the container does not set the variable) */
  images?: Record<string, { env?: readonly ImageEnv[] | readonly unknown[] }>;
  /** config.files (render mode: only 'expressions' files have implicit refs; literal mentions are searched in any text file) */
  files?: readonly Pick<FileDef, 'fileSlug' | 'editor' | 'render'>[];
  /** fileSlug → text content (when known) */
  fileContents?: Record<string, string>;
}

const HOSTNAME_CH = /[A-Za-z0-9_.-]/;
const HOST_KEY = /(host|server|broker|addr|endpoint|upstream|url|uri|dsn)/i;
const isStr = (v: unknown): v is string => typeof v === 'string';

/**
 * The slugs (from `slugs`) that `text` mentions as a hostname. A mention is the slug as a whole hostname (no
 * `[A-Za-z0-9_.-]` before it; no `[A-Za-z0-9_-]` after it and no `.x` domain continuation) in one of these places:
 *   1. a URL authority: `scheme://[userinfo@]slug` (e.g. `mqtt://mqtt:1883`, `redis://redis`, `amqp://u:p@rabbit/`)
 *   2. host:port: `slug:<1–5 digits>` (e.g. `mqtt:1883`)
 *   3. after a host-like key: `<key>[quote] [:=] [quote]slug` where the key holds host / server / broker / addr /
 *      endpoint / upstream / url / uri / dsn (e.g. `host: "mqtt"`, `MQTT_HOST=mqtt`, `"broker": "mqtt"`)
 * Case-sensitive (slugs are lower case); the context is the text of the mention's line. Order = position of the mention.
 */
export function literalHostMentions(text: string, slugs: readonly string[]): string[] {
  const found: { slug: string; pos: number }[] = [];
  if (!text) return [];
  for (const slug of new Set(slugs)) {
    if (!slug) continue;
    let from = 0;
    for (;;) {
      const i = text.indexOf(slug, from);
      if (i < 0) break;
      from = i + 1;
      const j = i + slug.length;
      const L = i > 0 ? text[i - 1]! : '';
      const R = j < text.length ? text[j]! : '';
      if (L && HOSTNAME_CH.test(L)) continue;
      if (R && /[A-Za-z0-9_-]/.test(R)) continue;
      if (R === '.' && /[A-Za-z0-9]/.test(text[j + 1] ?? '')) continue;
      const before = text.slice(text.lastIndexOf('\n', i - 1) + 1, i); // the line up to the mention
      const url = /[A-Za-z][A-Za-z0-9+.-]*:\/\/(?:[^\s/@"'`]*@)?$/.test(before);
      const hostPort = R === ':' && /^\d{1,5}(?!\d)/.test(text.slice(j + 1));
      const km = /([A-Za-z0-9_]+)["']?\s*[:=]\s*["']?$/.exec(before);
      const key = !!km && HOST_KEY.test(km[1]!);
      if (url || hostPort || key) {
        found.push({ slug, pos: i });
        break;
      }
    }
  }
  return found.sort((a, b) => a.pos - b.pos || (a.slug < b.slug ? -1 : 1)).map((f) => f.slug);
}

/** The `containers.<slug>` slugs referenced by an expression value (syntax errors → none). */
export function containerRefs(value: unknown): string[] {
  if (!isStr(value) || !value.includes('{{')) return [];
  let refs: string[];
  try {
    refs = exprRefs(value);
  } catch {
    return [];
  }
  const out: string[] = [];
  for (const r of refs) {
    const m = /^containers\.([^.]+)\./.exec(r);
    if (m && !out.includes(m[1]!)) out.push(m[1]!);
  }
  return out;
}

function imageEnvDefault(e: unknown): { name: string; value: string | undefined } | null {
  if (!e || typeof e !== 'object') return null;
  const o = e as { name?: unknown; value?: unknown };
  if (!isStr(o.name)) return null;
  if (Array.isArray(o.value)) return { name: o.name, value: o.value.find(isStr) };
  return { name: o.name, value: isStr(o.value) ? o.value : undefined };
}

const renderMode = (f: Pick<FileDef, 'editor' | 'render'>) => (f.editor === 'binary' ? 'none' : (f.render ?? 'expressions'));

/** The text sources of a container: env values (container + image defaults) and mounted files. */
function sourcesOf(c: { env?: readonly ContainerEnv[]; volumes?: readonly ContainerMount[]; serviceSlug: string; version: string }, input: DependsInput) {
  const env: { where: string; name: string; value: string }[] = [];
  const given = new Set<string>();
  for (const e of c.env ?? []) {
    if (!e || !isStr(e.name)) continue;
    given.add(e.name);
    if (isStr(e.value)) env.push({ where: `env ${e.name}`, name: e.name, value: e.value });
  }
  for (const raw of input.images?.[`${c.serviceSlug}:${c.version}`]?.env ?? []) {
    const d = imageEnvDefault(raw);
    if (d && !given.has(d.name) && d.value !== undefined) env.push({ where: `image env ${d.name}`, name: d.name, value: d.value });
  }
  const files: { where: string; slug: string; content: string; render: string }[] = [];
  const seen = new Set<string>();
  for (const m of c.volumes ?? []) {
    if (!m || m.source?.kind !== 'file') continue;
    const slug = m.source.fileSlug;
    if (seen.has(slug)) continue;
    seen.add(slug);
    const f = input.files?.find((x) => x.fileSlug === slug);
    const content = input.fileContents?.[slug];
    if (!f || f.editor === 'binary' || !isStr(content)) continue;
    files.push({ where: `file ${slug}`, slug, content, render: renderMode(f) });
  }
  return { env, files };
}

const add = (list: InferredDepend[], slug: string, where: string) => {
  const x = list.find((d) => d.containerSlug === slug);
  if (!x) list.push({ containerSlug: slug, from: [where] });
  else if (!x.from.includes(where)) x.from.push(where);
};

/** Explicit, implicit and suggested depends of every ENABLED container of the site (declared order). */
export function inferDepends(input: DependsInput): ContainerDepends[] {
  const cs = (input.site.containers ?? []).filter((c) => c && c.enabled);
  const enabled = cs.map((c) => c.containerSlug);
  const out: ContainerDepends[] = [];
  for (const c of cs) {
    const explicit = (c.depends ?? []).map((d) => d.containerSlug).filter(isStr);
    const implicit: InferredDepend[] = [];
    const suggested: InferredDepend[] = [];
    const others = enabled.filter((s) => s !== c.containerSlug);
    const { env, files } = sourcesOf(c, input);
    const scan = (where: string, value: string, exprs: boolean, literalText: string) => {
      if (exprs) for (const r of containerRefs(value)) if (r !== c.containerSlug && others.includes(r) && !explicit.includes(r)) add(implicit, r, where);
      for (const r of literalHostMentions(literalText, others)) if (!explicit.includes(r)) add(suggested, r, where);
    };
    // literal search skips the {{ }} blocks (an expression is not a literal mention)
    const noBlocks = (s: string) => s.replace(/\{\{[\s\S]*?\}\}/g, ' ');
    for (const e of env) scan(e.where, e.value, true, `${e.name}=${noBlocks(e.value)}`);
    for (const f of files) scan(f.where, f.content, f.render === 'expressions', f.render === 'expressions' ? noBlocks(f.content) : f.content);
    const imp = new Set(implicit.map((d) => d.containerSlug));
    out.push({ containerSlug: c.containerSlug, explicit, implicit, suggested: suggested.filter((d) => !imp.has(d.containerSlug)) });
  }
  return out;
}

export interface StartOrder {
  /** enabled containers, dependencies first */
  order: string[];
  /** containers whose implicit / suggested references formed a cycle (started in declared order); [] = none */
  cycle: string[];
}

/**
 * The APPLY start order of the enabled containers (§15.5): Kahn over explicit ∪ implicit ∪ suggested edges, ties in the
 * declared order. When no container is ready but some are left (a cycle), the left-over container with the lowest
 * declared index whose EXPLICIT depends are all placed is taken next (else the lowest declared one); every container
 * left at that moment is reported in `cycle`.
 */
export function startOrder(site: Pick<SiteConfig, 'containers'>, deps: readonly ContainerDepends[]): StartOrder {
  const cs = [...new Set((site.containers ?? []).filter((c) => c && c.enabled).map((c) => c.containerSlug))]; // duplicates are errors elsewhere
  const idx = new Map(cs.map((s, i) => [s, i]));
  const edges = new Map<string, Set<string>>();
  const explicitOf = new Map<string, Set<string>>();
  for (const s of cs) {
    edges.set(s, new Set());
    explicitOf.set(s, new Set());
  }
  for (const d of deps) {
    if (!edges.has(d.containerSlug)) continue;
    const e = edges.get(d.containerSlug)!;
    for (const x of d.explicit) if (idx.has(x) && x !== d.containerSlug) {
      e.add(x);
      explicitOf.get(d.containerSlug)!.add(x);
    }
    for (const x of [...d.implicit, ...d.suggested]) if (idx.has(x.containerSlug) && x.containerSlug !== d.containerSlug) e.add(x.containerSlug);
  }
  const placed = new Set<string>();
  const order: string[] = [];
  const cycle = new Set<string>();
  while (order.length < cs.length) {
    const left = cs.filter((s) => !placed.has(s));
    let next = left.find((s) => [...edges.get(s)!].every((x) => placed.has(x)));
    if (next === undefined) {
      for (const s of left) cycle.add(s);
      next = left.find((s) => [...explicitOf.get(s)!].every((x) => placed.has(x))) ?? left[0]!;
    }
    placed.add(next);
    order.push(next);
  }
  return { order, cycle: cs.filter((s) => cycle.has(s)) };
}

/** Effective "wait ready" depends of a container: explicit (enabled targets) + implicit. */
export function waitDepends(d: ContainerDepends, enabled: readonly string[]): string[] {
  const out = d.explicit.filter((x) => enabled.includes(x) && x !== d.containerSlug);
  for (const x of d.implicit) if (!out.includes(x.containerSlug)) out.push(x.containerSlug);
  return out;
}

// ───────────────────────────────────────────────────────────── DNS failure hint (update.failed, §15.4)
export interface DnsFailureHint {
  /** the container of THIS site that could not be resolved */
  containerSlug: string;
  /** EAI_AGAIN | ENOTFOUND | no such host | Name or service not known | Temporary failure in name resolution | Could not resolve host */
  code: string;
}
const ANSI_RE = /\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)?|[ -/]*[0-~])?/g;
const DNS_RES: { re: RegExp; code: (m: RegExpExecArray) => string; host: number }[] = [
  { re: /getaddrinfo (EAI_AGAIN|ENOTFOUND) ([A-Za-z0-9._-]+)/, code: (m) => m[1]!, host: 2 },
  { re: /lookup ([A-Za-z0-9._-]+)(?: on [^\s:]+:\d+)?: (no such host|server misbehaving|Temporary failure in name resolution)/, code: (m) => m[2]!, host: 1 },
  { re: /Could not resolve host:? ['"]?([A-Za-z0-9._-]+)/, code: () => 'Could not resolve host', host: 1 },
  { re: /([A-Za-z0-9._-]+): (Name or service not known|Temporary failure in name resolution|nodename nor servname provided, or not known)/, code: (m) => m[2]!, host: 1 },
];
const HOSTLESS = /(EAI_AGAIN|ENOTFOUND|Name or service not known|Temporary failure in name resolution|no such host)/;

/**
 * Scan a log tail (newest last; ANSI removed; "<ts> <stream> " prefixes allowed) for a DNS failure that names a container
 * of this site (`slugs`, the subject excluded). The newest matching line wins. A line with the error but without a host
 * (Python "Name or service not known") counts when one of the 3 lines around it mentions a slug as a hostname
 * (`hostname: "mqtt"`, `host='mqtt'`).
 */
export function dnsFailureHint(lines: readonly string[], slugs: readonly string[], subject: string): DnsFailureHint | null {
  const others = slugs.filter((s) => s !== subject);
  const clean = lines.map((l) => l.replace(ANSI_RE, ''));
  for (let i = clean.length - 1; i >= 0; i--) {
    const l = clean[i]!;
    for (const d of DNS_RES) {
      const m = d.re.exec(l);
      if (m && others.includes(m[d.host]!)) return { containerSlug: m[d.host]!, code: d.code(m) };
    }
    const h = HOSTLESS.exec(l);
    if (h) {
      for (let k = Math.max(0, i - 3); k <= Math.min(clean.length - 1, i + 3); k++) {
        const near = literalHostMentions(clean[k]!, others);
        if (near.length) return { containerSlug: near[0]!, code: h[1]! };
      }
    }
  }
  return null;
}

/** The update.failed summary hint (§15.4): "a could not resolve `mqtt` (EAI_AGAIN) — is mqtt running/healthy? add it to Depends on". */
export function dnsFailureMessage(subject: string, h: DnsFailureHint): string {
  return `${subject} could not resolve \`${h.containerSlug}\` (${h.code}) — is ${h.containerSlug} running/healthy? add it to Depends on`;
}
