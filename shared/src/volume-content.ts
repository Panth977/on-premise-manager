/**
 * §21 #101 — a volume's OWN content (file tree) + "persist to cloud" volumes. Normative contract (SPEC-final §2.2
 * "Volume content", §6.1 "Volume content", §7.7, §18 Volumes, §21 #101). The Go agent mirrors the path rules, the
 * canonical tree + hash, the exclude matcher and the 3-way plan in agent/internal/volcontent (same vectors:
 * shared/testdata/volume-content-vectors.json).
 *
 * Model
 *   - Starter content (config): `SiteVolume.seed` kind `tree` (folder volumes) / `single` (single-file volumes) = a manifest
 *     (path → sha256, size, mode, render) inside the config; the bytes are content-addressed blobs `files/<hex>` in cloud
 *     Storage (uploaded with POST /v1/blobs or a multi-part upload, like File contents). Written ONCE into a new / empty
 *     volume (round-E seeding rules, unchanged). Files (Config.files) are NEVER volume content (V_VOLUME_SEED_FILE_REF).
 *   - Persist to cloud (`SiteVolume.persist`, folder volumes): the agent snapshots the live tree (minus excludes) into
 *     versioned cloud content (`deployments/{id}/volumeContent/{site}:{volume}` + `versions/{versionId}`; the tree itself
 *     is the blob `files/<treeHash hex>`). The cloud edits a version (→ a new version), and SENDS one to the site
 *     (a push). The site applies it only after a person approves it in the agent GUI (60 s intent wait, ⏩), with a 3-way
 *     check against the live tree (overwrite / keep live / per file). Store-and-forward both ways.
 */
import { canonicalize, compareUtf16 } from './canonical.js';
import { createClient, ep, type AuthProvider, type Client } from './http.js';
import type * as T from './types.js';
import type { Config, FileDef, FileRender, ListResponse, Sha256, SiteConfig, SiteVolume, Slug, Ts, Ulid, VolumePersist, VolumeSeed, VolumeTree, VolumeTreeEntry } from './types.js';

// ───────────────────────────────────────────────────────────── limits
export const VOLUME_CONTENT_LIMITS = {
  /** one file of a volume's content (starter content in the cloud editor; = the multi-part blob limit) */
  fileMaxBytes: 50 * 1024 * 1024,
  /** one file in an agent snapshot (single PUT to the cloud; bigger files are skipped with reason too-large) */
  snapshotFileMaxBytes: 24 * 1024 * 1024,
  /** starter content (config): files / empty folders / total bytes */
  seedMaxEntries: 1000,
  seedMaxDirs: 1000,
  seedMaxTotalBytes: 200 * 1024 * 1024,
  /** persisted versions: files / empty folders per snapshot or edit */
  versionMaxEntries: 10000,
  versionMaxDirs: 5000,
  /** text files up to this size open in the code editor; bigger (or binary) ones show size + download */
  editorTextMaxBytes: 2 * 1024 * 1024,
  pathMaxBytes: 1024,
  segmentMaxBytes: 255,
  /** persist.maxBytes range + default */
  persistMinBytes: 1024 * 1024,
  persistMaxBytes: 200 * 1024 * 1024,
  persistDefaultBytes: 50 * 1024 * 1024,
  excludeMax: 50,
  excludePatternMax: 200,
  noteMax: 500,
  /** versions kept per volume; older AUTO snapshots beyond this are pruned by the daily job (blobs stay, content-addressed) */
  versionsKept: 200,
} as const;

/** the agent's marker in a seeded folder volume: never content, never snapshotted, never touched by an apply */
export const VOLUME_SEED_MARKER = '.opm-seeded';
/** always excluded (in addition to VolumePersist.exclude) */
export const VOLUME_ALWAYS_EXCLUDED = [VOLUME_SEED_MARKER] as const;
/** default excludes of a NEW persist setting (Node-RED / npm caches) */
export const VOLUME_PERSIST_DEFAULT_EXCLUDE = ['node_modules/', '.npm/', '.cache/', '*.log'] as const;
/** agent ≥ this version understands tree / single seeds and persist (the cloud warns per site, like dev-tool presets) */
export const VOLUME_CONTENT_MIN_AGENT = '0.12.0';
/** §21 #117 agents that write a persist volume's committed content into the live volume on merge (older: starter only) */
export const VOLUME_CONTENT_COMMIT_MIN_AGENT = '0.19.0';

/** Auto snapshot timing (agent): scan the cheap fingerprint every SCAN s; snapshot when changed and quiet for QUIET s; at most one auto snapshot per MIN_INTERVAL s. */
export const VOLUME_SYNC_SCAN_SEC = 60;
export const VOLUME_SYNC_QUIET_SEC = 120;
export const VOLUME_SYNC_MIN_INTERVAL_SEC = 900;
/** the "Apply cloud version" intent wait (fast-forwardable, §21 #95) */
export const VOLUME_CONTENT_APPLY_WAIT_SEC = 60;

export const DEFAULT_FILE_MODE = '0644';
export const RE_FILE_MODE = /^0[0-7]{3}$/;

export function defaultVolumePersist(): VolumePersist {
  return { exclude: [...VOLUME_PERSIST_DEFAULT_EXCLUDE], maxBytes: VOLUME_CONTENT_LIMITS.persistDefaultBytes, auto: true };
}
/** SiteVolume.persist read (absent / null = off). */
export function volumePersist(v: Pick<SiteVolume, 'persist'> | null | undefined): VolumePersist | null {
  return v?.persist ?? null;
}
/** true when a site config uses §21 #101 features an agent < 0.12 cannot apply (tree / single seeds, persist). */
export function usesVolumeContent(sc: Pick<SiteConfig, 'volumes'> | null | undefined): boolean {
  return (sc?.volumes ?? []).some((v) => !!v && (!!v.persist || v.seed?.kind === 'tree' || v.seed?.kind === 'single'));
}
/** A legacy seed that references Files (§21 #101: rejected in templates / drafts / publish; only old revisions hold it). */
export function isLegacyFileSeed(seed: VolumeSeed | null | undefined): seed is Extract<VolumeSeed, { kind: 'files' | 'file' }> {
  return !!seed && (seed.kind === 'files' || seed.kind === 'file');
}

// ───────────────────────────────────────────────────────────── paths
const utf8 = new TextEncoder();
const byteLen = (s: string) => utf8.encode(s).length;
// eslint-disable-next-line no-control-regex
const BAD_CHARS = /[\u0000-\u001f\u007f\\]/;

/** Error text for a content path, or null when valid (see VolumeTreeEntry). */
export function contentPathError(p: unknown): string | null {
  if (typeof p !== 'string' || p === '') return 'the path is empty';
  if (byteLen(p) > VOLUME_CONTENT_LIMITS.pathMaxBytes) return `the path is longer than ${VOLUME_CONTENT_LIMITS.pathMaxBytes} bytes`;
  if (BAD_CHARS.test(p)) return 'the path holds a control character or "\\"';
  if (p.startsWith('/')) return 'the path must be relative (no leading "/")';
  if (p.endsWith('/')) return 'the path must not end with "/"';
  for (const seg of p.split('/')) {
    if (seg === '') return 'the path has an empty segment ("//")';
    if (seg === '.' || seg === '..') return 'the path must not contain "." or ".." segments';
    if (byteLen(seg) > VOLUME_CONTENT_LIMITS.segmentMaxBytes) return `a name is longer than ${VOLUME_CONTENT_LIMITS.segmentMaxBytes} bytes`;
  }
  if (p === VOLUME_SEED_MARKER) return `${VOLUME_SEED_MARKER} is reserved (the agent's seed marker)`;
  return null;
}
export const parentDir = (p: string) => (p.includes('/') ? p.slice(0, p.lastIndexOf('/')) : '');
export const baseName = (p: string) => p.slice(p.lastIndexOf('/') + 1);
export const joinPath = (dir: string, name: string) => (dir ? `${dir}/${name}` : name);
/** "a/b/c" → ["a", "a/b"] */
export function ancestorDirs(p: string): string[] {
  const out: string[] = [];
  let i = p.indexOf('/');
  while (i >= 0) {
    out.push(p.slice(0, i));
    i = p.indexOf('/', i + 1);
  }
  return out;
}
const isUnder = (p: string, dir: string) => p.startsWith(dir + '/');

// ───────────────────────────────────────────────────────────── canonical tree + hash
const cmp = (a: string, b: string) => compareUtf16(a, b);

/**
 * Canonical tree: entries sorted by path; each entry exactly { path, sha256, size, mode, render } (missing mode → 0644,
 * missing render → none); `dirs` deduplicated, sorted, and only EMPTY folders (a dir that is an ancestor of an entry or
 * of another dir is implied and dropped; a dir equal to an entry path is dropped). Pure; duplicates are NOT merged
 * (validation reports them) — the first occurrence wins.
 */
export function normalizeVolumeTree(t: Partial<VolumeTree> | null | undefined): VolumeTree {
  const seen = new Set<string>();
  const entries: VolumeTreeEntry[] = [];
  for (const e of t?.entries ?? []) {
    if (!e || typeof e.path !== 'string' || seen.has(e.path)) continue;
    seen.add(e.path);
    entries.push({ path: e.path, sha256: e.sha256, size: e.size, mode: e.mode ?? DEFAULT_FILE_MODE, render: e.render ?? 'none' });
  }
  entries.sort((a, b) => cmp(a.path, b.path));
  const implied = new Set<string>();
  for (const e of entries) for (const d of ancestorDirs(e.path)) implied.add(d);
  const rawDirs = [...new Set((t?.dirs ?? []).filter((d): d is string => typeof d === 'string' && d !== ''))];
  for (const d of rawDirs) for (const a of ancestorDirs(d)) implied.add(a);
  const dirs = rawDirs.filter((d) => !implied.has(d) && !seen.has(d)).sort(cmp);
  return { entries, dirs };
}
/** The exact string whose sha256 is the tree hash: JCS of the canonical tree. */
export function volumeTreeHashInput(t: Partial<VolumeTree> | null | undefined): string {
  return canonicalize(normalizeVolumeTree(t));
}
/** "sha256:" + hex(sha256(volumeTreeHashInput(t))); pass a sync or async sha256-hex function (node:crypto, WebCrypto). */
export async function volumeTreeHash(t: Partial<VolumeTree> | null | undefined, sha256Hex: (s: string) => string | Promise<string>): Promise<Sha256> {
  return `sha256:${await sha256Hex(volumeTreeHashInput(t))}`;
}
export const EMPTY_VOLUME_TREE: VolumeTree = Object.freeze({ entries: [], dirs: [] }) as VolumeTree;
export function treeStats(t: VolumeTree): { fileCount: number; dirCount: number; totalBytes: number } {
  const dirs = new Set<string>(t.dirs);
  for (const e of t.entries) for (const d of ancestorDirs(e.path)) dirs.add(d);
  return { fileCount: t.entries.length, dirCount: dirs.size, totalBytes: t.entries.reduce((n, e) => n + (e.size || 0), 0) };
}

/** Issues of a tree (validation + the editors). `forSeed`: starter-content limits, else version limits. */
export interface TreeIssue {
  path: string; // the entry / dir path ('' = the whole tree)
  code: 'V_VOLUME_CONTENT_PATH' | 'V_DUPLICATE' | 'V_VOLUME_CONTENT' | 'V_FORMAT' | 'V_TOO_MANY' | 'V_TOO_LARGE';
  message: string;
}
export function volumeTreeIssues(t: unknown, forSeed: boolean): TreeIssue[] {
  const out: TreeIssue[] = [];
  const tree = t as Partial<VolumeTree> | null;
  if (!tree || typeof tree !== 'object' || !Array.isArray(tree.entries) || !Array.isArray(tree.dirs)) return [{ path: '', code: 'V_VOLUME_CONTENT', message: 'the content needs entries[] and dirs[]' }];
  const L = VOLUME_CONTENT_LIMITS;
  const maxE = forSeed ? L.seedMaxEntries : L.versionMaxEntries;
  const maxD = forSeed ? L.seedMaxDirs : L.versionMaxDirs;
  if (tree.entries.length > maxE) out.push({ path: '', code: 'V_TOO_MANY', message: `at most ${maxE} files` });
  if (tree.dirs.length > maxD) out.push({ path: '', code: 'V_TOO_MANY', message: `at most ${maxD} empty folders` });
  const files = new Set<string>();
  let total = 0;
  for (const e of tree.entries) {
    const p = (e as VolumeTreeEntry | null)?.path;
    const pe = contentPathError(p);
    if (pe) {
      out.push({ path: String(p ?? ''), code: 'V_VOLUME_CONTENT_PATH', message: pe });
      continue;
    }
    if (files.has(p!)) out.push({ path: p!, code: 'V_DUPLICATE', message: `${p} is listed twice` });
    files.add(p!);
    if (typeof e.sha256 !== 'string' || !/^sha256:[0-9a-f]{64}$/.test(e.sha256)) out.push({ path: p!, code: 'V_FORMAT', message: `${p}: upload the content (sha256)` });
    if (typeof e.size !== 'number' || !Number.isInteger(e.size) || e.size < 0 || e.size > L.fileMaxBytes) out.push({ path: p!, code: 'V_TOO_LARGE', message: `${p}: a file is 0 B – ${L.fileMaxBytes / 1048576} MiB` });
    else total += e.size;
    if (typeof e.mode !== 'string' || !RE_FILE_MODE.test(e.mode)) out.push({ path: p!, code: 'V_FORMAT', message: `${p}: mode must look like 0644` });
    if (e.render !== 'none' && e.render !== 'expressions') out.push({ path: p!, code: 'V_FORMAT', message: `${p}: render must be none | expressions` });
    else if (!forSeed && e.render === 'expressions') out.push({ path: p!, code: 'V_VOLUME_CONTENT', message: `${p}: only starter content renders expressions — persisted content is literal (render none)` });
  }
  for (const p of files)
    for (const d of ancestorDirs(p))
      if (files.has(d)) out.push({ path: d, code: 'V_VOLUME_CONTENT', message: `${d} is a file, but ${p} is inside it` });
  const dirSet = new Set<string>();
  for (const d of tree.dirs) {
    const pe = contentPathError(d);
    if (pe) out.push({ path: String(d ?? ''), code: 'V_VOLUME_CONTENT_PATH', message: `folder: ${pe}` });
    else if (files.has(d)) out.push({ path: d, code: 'V_VOLUME_CONTENT', message: `${d} is a file and a folder` });
    else if (dirSet.has(d)) out.push({ path: d, code: 'V_DUPLICATE', message: `folder ${d} is listed twice` });
    else {
      dirSet.add(d);
      for (const a of ancestorDirs(d)) if (files.has(a)) out.push({ path: a, code: 'V_VOLUME_CONTENT', message: `${a} is a file, but folder ${d} is inside it` });
    }
  }
  if (forSeed && total > L.seedMaxTotalBytes) out.push({ path: '', code: 'V_TOO_LARGE', message: `starter content is at most ${L.seedMaxTotalBytes / 1048576} MiB in total` });
  return out;
}

// ───────────────────────────────────────────────────────────── excludes (gitignore-lite)
/**
 * Exclude patterns (VolumePersist.exclude), matched against the relative path of every file AND folder of the walk:
 *   - a trailing "/" = folders only (the folder and everything below it is excluded; a FILE of that name is not);
 *   - a pattern with a "/" elsewhere (or a leading "/", which is stripped) is ANCHORED: matched against the whole path
 *     from the volume root; otherwise it is matched against every single name (basename) at any depth;
 *   - glob: `*` = any run of characters except "/", `?` = one character except "/", `**` = any run including "/"
 *     (anchored patterns only; "a/**" = everything below a). No negation, no [classes], no escapes.
 *   A file / folder below an excluded folder is excluded. `.opm-seeded` (root) is always excluded.
 */
export function excludePatternError(p: unknown): string | null {
  if (typeof p !== 'string' || p.trim() === '') return 'an exclude pattern is empty';
  if (p !== p.trim()) return 'an exclude pattern must not start or end with spaces';
  if (p.length > VOLUME_CONTENT_LIMITS.excludePatternMax) return `an exclude pattern is longer than ${VOLUME_CONTENT_LIMITS.excludePatternMax} characters`;
  if (BAD_CHARS.test(p)) return 'an exclude pattern holds a control character or "\\"';
  if (p.startsWith('!')) return 'negated patterns ("!x") are not supported';
  if (/[[\]]/.test(p)) return 'character classes ([…]) are not supported';
  const body = p.replace(/^\//, '').replace(/\/$/, '');
  if (body === '' || body.split('/').some((s) => s === '' || s === '.' || s === '..')) return 'an exclude pattern has an empty, "." or ".." segment';
  return null;
}
interface CompiledPattern {
  re: RegExp;
  dirOnly: boolean;
  anchored: boolean;
}
function globToRe(g: string, anchored: boolean): RegExp {
  let s = '';
  for (let i = 0; i < g.length; i++) {
    const c = g[i]!;
    if (c === '*') {
      if (g[i + 1] === '*' && anchored) {
        // "**/" = zero or more folders; "**" elsewhere = anything
        if (g[i + 2] === '/') {
          s += '(?:.*/)?';
          i += 2;
        } else {
          s += '.*';
          i += 1;
        }
      } else {
        s += '[^/]*';
        if (g[i + 1] === '*') i += 1;
      }
    } else if (c === '?') s += '[^/]';
    else s += c.replace(/[.+^${}()|\\]/g, '\\$&');
  }
  return new RegExp(`^${s}$`, 's');
}
export function compileExcludes(patterns: readonly string[]): CompiledPattern[] {
  const out: CompiledPattern[] = [];
  for (const raw of [...VOLUME_ALWAYS_EXCLUDED.map((x) => `/${x}`), ...patterns]) {
    if (excludePatternError(raw.replace(/^\//, '')) && raw !== `/${VOLUME_SEED_MARKER}`) continue;
    const dirOnly = raw.endsWith('/');
    let body = raw.replace(/\/$/, '');
    const anchored = body.startsWith('/') || body.includes('/');
    body = body.replace(/^\//, '');
    out.push({ re: globToRe(body, anchored), dirOnly, anchored });
  }
  return out;
}
/** Is this path (a file, or a folder when isDir) excluded by the compiled patterns (incl. any excluded ancestor folder)? */
export function isExcluded(path: string, isDir: boolean, compiled: readonly CompiledPattern[]): boolean {
  const own = (p: string, dir: boolean) =>
    compiled.some((c) => {
      if (c.dirOnly && !dir) return false;
      return c.anchored ? c.re.test(p) : c.re.test(baseName(p));
    });
  for (const a of ancestorDirs(path)) if (own(a, true)) return true;
  return own(path, isDir);
}
/** Convenience: the tree minus excluded entries / dirs. */
export function filterExcluded(t: VolumeTree, patterns: readonly string[]): VolumeTree {
  const c = compileExcludes(patterns);
  return normalizeVolumeTree({ entries: t.entries.filter((e) => !isExcluded(e.path, false, c)), dirs: t.dirs.filter((d) => !isExcluded(d, true, c)) });
}

// ───────────────────────────────────────────────────────────── 3-way plan (agent apply; the cloud uses it for previews)
/** what one side did since the base for a path */
export type VolumeSideOp = 'none' | 'added' | 'removed' | 'changed';
/**
 *   cloud     — only the cloud version changed it → taken
 *   live      — only the live volume changed it → kept
 *   same      — both changed it the same way → nothing to do
 *   conflict  — both changed it differently (or the base is unknown and live ≠ cloud) → the person chooses
 */
export type VolumePlanKind = 'cloud' | 'live' | 'same' | 'conflict';
export interface VolumePlanSide {
  sha256: Sha256;
  size: number;
  mode: string;
}
export interface VolumePlanItem {
  path: string;
  kind: VolumePlanKind;
  liveOp: VolumeSideOp;
  cloudOp: VolumeSideOp;
  base: VolumePlanSide | null;
  live: VolumePlanSide | null;
  cloud: VolumePlanSide | null;
}
const side = (e: VolumeTreeEntry | undefined): VolumePlanSide | null => (e ? { sha256: e.sha256, size: e.size, mode: e.mode } : null);
const sameEntry = (a: VolumeTreeEntry | undefined, b: VolumeTreeEntry | undefined) => (!a && !b) || (!!a && !!b && a.sha256 === b.sha256 && a.mode === b.mode);
const opOf = (base: VolumeTreeEntry | undefined, x: VolumeTreeEntry | undefined): VolumeSideOp => (sameEntry(base, x) ? 'none' : !base ? 'added' : !x ? 'removed' : 'changed');

/**
 * 3-way plan of FILES between base (the snapshot the cloud edit was based on; null = unknown), live (the volume now,
 * excludes applied) and cloud (the pushed version). Returns only paths where live ≠ cloud, sorted by path. With an unknown
 * base every difference is a conflict. Folders are merged without conflicts (mergeVolumePush).
 */
export function planVolumePush(base: VolumeTree | null, live: VolumeTree, cloud: VolumeTree): VolumePlanItem[] {
  const m = (t: VolumeTree | null) => new Map((t?.entries ?? []).map((e) => [e.path, e] as const));
  const B = m(base);
  const L = m(live);
  const C = m(cloud);
  const paths = [...new Set([...L.keys(), ...C.keys()])].sort(cmp);
  const out: VolumePlanItem[] = [];
  for (const p of paths) {
    const b = B.get(p);
    const l = L.get(p);
    const c = C.get(p);
    if (sameEntry(l, c)) continue;
    const liveOp = base ? opOf(b, l) : l ? 'changed' : 'removed';
    const cloudOp = base ? opOf(b, c) : c ? 'changed' : 'removed';
    let kind: VolumePlanKind;
    if (!base) kind = 'conflict';
    else if (liveOp === 'none') kind = 'cloud';
    else if (cloudOp === 'none') kind = 'live';
    else kind = 'conflict'; // both changed and live ≠ cloud
    out.push({ path: p, kind, liveOp, cloudOp, base: side(b), live: side(l), cloud: side(c) });
  }
  return out;
}
export type VolumePushResolution = 'overwrite' | 'keep-live' | 'per-file';
/**
 * The target tree of an apply:
 *   overwrite — the cloud version (every live-only change inside the scope is replaced / removed)
 *   keep-live — the live tree (nothing written; the push is marked kept-live)
 *   per-file  — cloud-only changes taken, live-only changes kept, every conflict decided by choices[path] ('cloud' | 'live');
 *               a conflict without a choice → throws (the GUI must ask). Empty folders: a 3-way set merge without conflicts.
 * Excluded paths are never part of live / cloud / target (the agent leaves them alone).
 */
export function mergeVolumePush(base: VolumeTree | null, live: VolumeTree, cloud: VolumeTree, resolution: VolumePushResolution, choices: Record<string, 'cloud' | 'live'> = {}): VolumeTree {
  if (resolution === 'overwrite') return normalizeVolumeTree(cloud);
  if (resolution === 'keep-live') return normalizeVolumeTree(live);
  const plan = planVolumePush(base, live, cloud);
  const out = new Map(live.entries.map((e) => [e.path, e] as const));
  const C = new Map(cloud.entries.map((e) => [e.path, e] as const));
  for (const it of plan) {
    const take = it.kind === 'cloud' ? 'cloud' : it.kind === 'conflict' ? choices[it.path] : 'live';
    if (take !== 'cloud' && take !== 'live') throw new Error(`conflict on ${it.path}: choose cloud or live`);
    if (take === 'cloud') {
      const c = C.get(it.path);
      if (c) out.set(it.path, c);
      else out.delete(it.path);
    }
  }
  const bd = new Set(base?.dirs ?? []);
  const ld = new Set(live.dirs);
  const cd = new Set(cloud.dirs);
  const dirs = new Set<string>();
  for (const d of new Set([...ld, ...cd])) {
    const inBase = bd.has(d);
    const keepLive = ld.has(d) && !(inBase && !cd.has(d)); // live has it and the cloud did not remove it
    const addCloud = cd.has(d) && !(inBase && !ld.has(d)); // the cloud has it and live did not remove it
    if (keepLive || addCloud) dirs.add(d);
  }
  return normalizeVolumeTree({ entries: [...out.values()], dirs: [...dirs] });
}
export interface TreeChangeCounts {
  added: number;
  removed: number;
  modified: number;
}
/** Per-file changes from a → b (mode or content). */
export function diffVolumeTrees(a: VolumeTree | null, b: VolumeTree | null): { path: string; kind: 'added' | 'removed' | 'changed'; before: VolumeTreeEntry | null; after: VolumeTreeEntry | null }[] {
  const A = new Map((a?.entries ?? []).map((e) => [e.path, e] as const));
  const Bm = new Map((b?.entries ?? []).map((e) => [e.path, e] as const));
  const out: { path: string; kind: 'added' | 'removed' | 'changed'; before: VolumeTreeEntry | null; after: VolumeTreeEntry | null }[] = [];
  for (const p of [...new Set([...A.keys(), ...Bm.keys()])].sort(cmp)) {
    const x = A.get(p);
    const y = Bm.get(p);
    if (x && y && x.sha256 === y.sha256 && x.mode === y.mode && x.render === y.render) continue;
    out.push({ path: p, kind: !x ? 'added' : !y ? 'removed' : 'changed', before: x ?? null, after: y ?? null });
  }
  return out;
}
export function countTreeChanges(a: VolumeTree | null, b: VolumeTree | null): TreeChangeCounts {
  const c = { added: 0, removed: 0, modified: 0 };
  for (const d of diffVolumeTrees(a, b)) c[d.kind === 'changed' ? 'modified' : d.kind]++;
  return c;
}

// ───────────────────────────────────────────────────────────── editor operations (pure; both GUIs)
export class TreeEditError extends Error {}
const clone = (t: VolumeTree): VolumeTree => ({ entries: t.entries.map((e) => ({ ...e })), dirs: [...t.dirs] });
export function treeHasFile(t: VolumeTree, p: string) {
  return t.entries.some((e) => e.path === p);
}
export function treeHasDir(t: VolumeTree, p: string) {
  return t.dirs.includes(p) || t.entries.some((e) => isUnder(e.path, p)) || t.dirs.some((d) => isUnder(d, p));
}
function assertFree(t: VolumeTree, p: string) {
  const pe = contentPathError(p);
  if (pe) throw new TreeEditError(pe);
  if (treeHasFile(t, p) || treeHasDir(t, p)) throw new TreeEditError(`${p} already exists`);
  for (const a of ancestorDirs(p)) if (treeHasFile(t, a)) throw new TreeEditError(`${a} is a file`);
}
/** add or replace a file (replace keeps nothing of the old entry) */
export function treePutFile(t: VolumeTree, e: VolumeTreeEntry, opts: { overwrite?: boolean } = {}): VolumeTree {
  const n = clone(t);
  const i = n.entries.findIndex((x) => x.path === e.path);
  if (i >= 0) {
    if (!opts.overwrite) throw new TreeEditError(`${e.path} already exists`);
    n.entries[i] = { ...e };
  } else {
    assertFree(n, e.path);
    n.entries.push({ ...e });
  }
  return normalizeVolumeTree(n);
}
export function treeMkdir(t: VolumeTree, p: string): VolumeTree {
  assertFree(t, p);
  return normalizeVolumeTree({ entries: t.entries, dirs: [...t.dirs, p] });
}
/** delete a file or a folder (recursively) */
export function treeDelete(t: VolumeTree, p: string): VolumeTree {
  const keepParent = parentDir(p);
  const n = normalizeVolumeTree({ entries: t.entries.filter((e) => e.path !== p && !isUnder(e.path, p)), dirs: t.dirs.filter((d) => d !== p && !isUnder(d, p)) });
  // the parent folder stays (as an empty folder) when this was its last child
  if (keepParent && !treeHasDir(n, keepParent)) return normalizeVolumeTree({ entries: n.entries, dirs: [...n.dirs, keepParent] });
  return n;
}
/** move / rename a file or a folder to `to` (the full new path); fails when `to` exists or is inside `from` */
export function treeMove(t: VolumeTree, from: string, to: string): VolumeTree {
  if (from === to) return t;
  if (isUnder(to, from)) throw new TreeEditError('a folder cannot be moved into itself');
  const isFile = treeHasFile(t, from);
  if (!isFile && !treeHasDir(t, from)) throw new TreeEditError(`${from} does not exist`);
  const rest = treeDelete(t, from);
  assertFree(rest, to);
  const re = (p: string) => (p === from ? to : isUnder(p, from) ? to + p.slice(from.length) : p);
  const entries = t.entries.filter((e) => e.path === from || isUnder(e.path, from)).map((e) => ({ ...e, path: re(e.path) }));
  const dirs = t.dirs.filter((d) => d === from || isUnder(d, from)).map(re);
  if (!isFile && entries.length === 0 && dirs.length === 0) dirs.push(to);
  return normalizeVolumeTree({ entries: [...rest.entries, ...entries], dirs: [...rest.dirs, ...dirs] });
}
/** "a/b.json" → "a/b copy.json", then "a/b copy 2.json" … (folders: "x copy") */
export function duplicateName(t: VolumeTree, p: string): string {
  const dir = parentDir(p);
  const name = baseName(p);
  const dot = treeHasFile(t, p) ? name.lastIndexOf('.') : -1;
  const stem = dot > 0 ? name.slice(0, dot) : name;
  const ext = dot > 0 ? name.slice(dot) : '';
  for (let i = 1; ; i++) {
    const cand = joinPath(dir, `${stem} copy${i === 1 ? '' : ` ${i}`}${ext}`);
    if (!treeHasFile(t, cand) && !treeHasDir(t, cand)) return cand;
  }
}
export function treeDuplicate(t: VolumeTree, p: string, to = duplicateName(t, p)): VolumeTree {
  assertFree(t, to);
  if (treeHasFile(t, p)) return treePutFile(t, { ...t.entries.find((e) => e.path === p)!, path: to });
  if (!treeHasDir(t, p)) throw new TreeEditError(`${p} does not exist`);
  const re = (x: string) => to + x.slice(p.length);
  return normalizeVolumeTree({
    entries: [...t.entries, ...t.entries.filter((e) => isUnder(e.path, p)).map((e) => ({ ...e, path: re(e.path) }))],
    dirs: [...t.dirs, ...t.dirs.filter((d) => d === p || isUnder(d, p)).map(re), ...(t.dirs.some((d) => d === p) ? [] : [to])],
  });
}
/** Nested view for tree widgets: folders first, then files, each by name (UTF-16). */
export interface TreeNode {
  name: string;
  path: string;
  dir: boolean;
  entry: VolumeTreeEntry | null;
  children: TreeNode[];
}
export function treeNodes(t: VolumeTree): TreeNode[] {
  const root: TreeNode = { name: '', path: '', dir: true, entry: null, children: [] };
  const dirs = new Map<string, TreeNode>([['', root]]);
  const ensure = (d: string): TreeNode => {
    const ex = dirs.get(d);
    if (ex) return ex;
    const n: TreeNode = { name: baseName(d), path: d, dir: true, entry: null, children: [] };
    dirs.set(d, n);
    ensure(parentDir(d)).children.push(n);
    return n;
  };
  for (const d of t.dirs) ensure(d);
  for (const e of t.entries) ensure(parentDir(e.path)).children.push({ name: baseName(e.path), path: e.path, dir: false, entry: e, children: [] });
  const sort = (n: TreeNode) => {
    n.children.sort((a, b) => (a.dir !== b.dir ? (a.dir ? -1 : 1) : cmp(a.name, b.name)));
    n.children.forEach(sort);
  };
  sort(root);
  return root.children;
}

// ───────────────────────────────────────────────────────────── editor helpers
/** CodeMirror language id by extension (null = plain text) */
export function languageForPath(p: string): 'json' | 'javascript' | 'typescript' | 'yaml' | 'html' | 'css' | 'markdown' | 'xml' | 'python' | 'sql' | 'shell' | 'ini' | null {
  const n = baseName(p).toLowerCase();
  const ext = n.includes('.') ? n.slice(n.lastIndexOf('.') + 1) : '';
  if (['json', 'jsonc', 'json5', 'map'].includes(ext) || n === '.babelrc' || n === '.eslintrc') return 'json';
  if (['js', 'mjs', 'cjs', 'jsx'].includes(ext)) return 'javascript';
  if (['ts', 'mts', 'cts', 'tsx'].includes(ext)) return 'typescript';
  if (['yml', 'yaml'].includes(ext)) return 'yaml';
  if (['html', 'htm', 'svelte', 'vue'].includes(ext)) return 'html';
  if (['css', 'scss', 'less'].includes(ext)) return 'css';
  if (['md', 'markdown'].includes(ext)) return 'markdown';
  if (['xml', 'svg'].includes(ext)) return 'xml';
  if (ext === 'py') return 'python';
  if (ext === 'sql') return 'sql';
  if (['sh', 'bash', 'zsh'].includes(ext) || n === 'dockerfile') return 'shell';
  if (['conf', 'cfg', 'ini', 'toml', 'env', 'properties'].includes(ext) || n.startsWith('.env')) return 'ini';
  return null;
}
/** Text heuristic on the first bytes: valid UTF-8 without NUL bytes. */
export function looksLikeText(bytes: Uint8Array): boolean {
  const head = bytes.subarray(0, Math.min(bytes.length, 8192));
  if (head.includes(0)) return false;
  try {
    new TextDecoder('utf-8', { fatal: true }).decode(head.length < bytes.length ? trimUtf8Tail(head) : head);
    return true;
  } catch {
    return false;
  }
}
function trimUtf8Tail(b: Uint8Array): Uint8Array {
  // drop an incomplete trailing sequence (cut at 8 KiB)
  let i = b.length - 1;
  let n = 0;
  while (i >= 0 && n < 4 && (b[i]! & 0xc0) === 0x80) {
    i--;
    n++;
  }
  return i >= 0 && b[i]! >= 0xc0 ? b.subarray(0, i) : b;
}
export function formatMode(mode: string): string {
  if (!RE_FILE_MODE.test(mode)) return mode;
  const bits = mode.slice(1).split('').map(Number);
  return bits.map((b) => `${b & 4 ? 'r' : '-'}${b & 2 ? 'w' : '-'}${b & 1 ? 'x' : '-'}`).join('');
}
export function formatContentBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1048576) return `${(n / 1024).toFixed(n < 10240 ? 1 : 0)} KiB`;
  if (n < 1073741824) return `${(n / 1048576).toFixed(n < 10485760 ? 1 : 0)} MiB`;
  return `${(n / 1073741824).toFixed(1)} GiB`;
}

// ───────────────────────────────────────────────────────────── migration (§21 #101; backend/scripts/migrate-h.ts)
export interface VolumeContentMigration<C> {
  config: C;
  changed: boolean;
  /** human lines: what changed */
  lines: string[];
  /** notes: needs a person (nothing changed for that item) */
  notes: string[];
  /** fileSlugs removed from config.files (nothing references them any more) */
  removedFiles: string[];
  /** File mounts moved into a writable volume's content (existing sites: the agent fills the 0-byte mountpoint once) */
  moved: { siteSlug: string; containerSlug: string; fileSlug: string; volumeSlug: string; path: string }[];
}
type MigCfg = Pick<Config, 'files' | 'sites'>;
/**
 * Pure, idempotent conversion of a template / draft config:
 *  1. every legacy seed (`files` → `tree`, `file` → `single`) takes the referenced File's content blob (sha256 + size),
 *     mode 0644, render = the File's render mode (so `{{ }}` blocks still render once per site); an entry whose File is
 *     missing or has no content is dropped with a NOTE;
 *  2. every File mounted INSIDE a writable volume mount of the same container (literal mount paths, image path or row
 *     override) moves into that volume's content at the relative path (the mount row becomes source none for an image
 *     volume row, or is removed for an extra mount); a host / archive-seeded / single-file target or an expression path
 *     is a NOTE (validation keeps the error);
 *  3. Files that nothing references any more (no mount, no seed, no edge certificate, no `file.<slug>` expression — a
 *     plain text search of the sites) are removed from config.files.
 * `imageVolumes(serviceSlug, version)` returns the image's volume list (mount paths) — rows without their own mountPath
 * need it; unknown → those rows are skipped with a NOTE.
 */
export function migrateVolumeFileSeeds<C extends MigCfg>(config: C, imageVolumes: (serviceSlug: string, version: string) => { name: string; mountPath: string }[] | null): VolumeContentMigration<C> {
  const cfg = structuredClone(config) as C;
  const lines: string[] = [];
  const notes: string[] = [];
  const moved: VolumeContentMigration<C>['moved'] = [];
  const touched = new Set<string>(); // fileSlugs whose role changed
  const fileOf = (slug: string): FileDef | undefined => cfg.files.find((f) => f.fileSlug === slug);
  const renderOf = (f: FileDef): FileRender => f.render ?? (f.editor === 'binary' ? 'none' : 'expressions');
  for (const site of cfg.sites) {
    const sc = site.config;
    for (const v of sc.volumes ?? []) {
      const sd = v.seed;
      if (!sd || (sd.kind !== 'files' && sd.kind !== 'file')) continue;
      if (sd.kind === 'file') {
        touched.add(sd.fileSlug);
        const f = fileOf(sd.fileSlug);
        if (!f?.content) {
          notes.push(`site ${site.siteSlug}: volume ${v.volumeSlug} started from file ${sd.fileSlug}, which has no content — starter content removed (starts empty)`);
          v.seed = null;
        } else {
          v.seed = { kind: 'single', sha256: f.content.sha256, size: f.content.sizeBytes, mode: DEFAULT_FILE_MODE, render: renderOf(f) };
          lines.push(`site ${site.siteSlug}: volume ${v.volumeSlug} (single file) ← content of file ${sd.fileSlug} (${f.content.sizeBytes} B, ${f.content.sha256.slice(0, 19)}…, render ${renderOf(f)})`);
        }
        continue;
      }
      const entries: VolumeTreeEntry[] = [];
      for (const e of sd.entries) {
        touched.add(e.fileSlug);
        const f = fileOf(e.fileSlug);
        if (!f?.content) {
          notes.push(`site ${site.siteSlug}: volume ${v.volumeSlug} entry ${e.path} ← file ${e.fileSlug} has no content — entry dropped`);
          continue;
        }
        entries.push({ path: e.path, sha256: f.content.sha256, size: f.content.sizeBytes, mode: DEFAULT_FILE_MODE, render: renderOf(f) });
        lines.push(`site ${site.siteSlug}: volume ${v.volumeSlug} content ${e.path} ← content of file ${e.fileSlug} (${f.content.sizeBytes} B, ${f.content.sha256.slice(0, 19)}…, render ${renderOf(f)})`);
      }
      v.seed = entries.length ? { kind: 'tree', ...normalizeVolumeTree({ entries, dirs: [] }) } : null;
    }
    // 2. Files nested in a writable volume mount of the same container
    for (const c of sc.containers ?? []) {
      const ivs = imageVolumes(c.serviceSlug, c.version);
      const pathOf = (m: { imageVolume: string | null; mountPath: string | null }): string | null => {
        if (m.mountPath) return m.mountPath;
        if (!m.imageVolume) return null;
        return ivs?.find((x) => x.name === m.imageVolume)?.mountPath ?? null;
      };
      const writable = (c.volumes ?? [])
        .filter((m) => (m.source.kind === 'volume' || m.source.kind === 'host') && m.readOnly === false)
        .map((m) => ({ m, path: pathOf(m) }))
        .filter((x): x is { m: typeof x.m; path: string } => !!x.path && !x.path.includes('{{'));
      const keep: typeof c.volumes = [];
      for (const m of c.volumes ?? []) {
        if (m.source.kind !== 'file') {
          keep.push(m);
          continue;
        }
        const fp = pathOf(m);
        if (!fp) {
          if (m.imageVolume && !ivs) notes.push(`site ${site.siteSlug}: ${c.containerSlug} mounts file ${m.source.fileSlug} on image volume ${m.imageVolume} — image ${c.serviceSlug}:${c.version} unknown, not checked`);
          keep.push(m);
          continue;
        }
        if (fp.includes('{{')) {
          keep.push(m);
          continue;
        }
        const host = writable.find((w) => fp === w.path.replace(/\/+$/, '') || fp.startsWith(w.path.replace(/\/+$/, '') + '/'));
        if (!host) {
          keep.push(m);
          continue;
        }
        const fileSlug = m.source.fileSlug;
        const where = `site ${site.siteSlug}: ${c.containerSlug} mounts file ${fileSlug} at ${fp} inside the writable `;
        if (host.m.source.kind !== 'volume') {
          notes.push(`${where}host folder ${host.path} — move the mount path outside it or mount the folder read-only (left as is)`);
          keep.push(m);
          continue;
        }
        const volumeSlug = host.m.source.volumeSlug;
        const vol = (sc.volumes ?? []).find((x) => x.volumeSlug === volumeSlug);
        const rel = fp.slice(host.path.replace(/\/+$/, '').length + 1);
        const f = fileOf(fileSlug);
        const relErr = contentPathError(rel);
        if (!vol || vol.kind === 'file' || relErr || (vol.seed && vol.seed.kind !== 'tree') || !f?.content) {
          notes.push(`${where}volume ${volumeSlug} (${host.path}) — cannot move it into the volume's content (${!vol ? 'volume not declared' : vol.kind === 'file' ? 'single-file volume' : relErr ? relErr : !f?.content ? 'the file has no content' : `starter content is ${vol.seed!.kind}`}); fix it by hand (left as is)`);
          keep.push(m);
          continue;
        }
        const tree: VolumeTree = vol.seed?.kind === 'tree' ? { entries: vol.seed.entries, dirs: vol.seed.dirs } : { entries: [], dirs: [] };
        if (tree.entries.some((e) => e.path === rel)) notes.push(`${where}volume ${volumeSlug}: its content already has ${rel} — kept the content's version`);
        else tree.entries.push({ path: rel, sha256: f.content.sha256, size: f.content.sizeBytes, mode: DEFAULT_FILE_MODE, render: renderOf(f) });
        vol.seed = { kind: 'tree', ...normalizeVolumeTree(tree) };
        touched.add(fileSlug);
        moved.push({ siteSlug: site.siteSlug, containerSlug: c.containerSlug, fileSlug, volumeSlug, path: rel });
        if (m.imageVolume) {
          keep.push({ ...m, source: { kind: 'none' }, readOnly: false, mountPath: m.mountPath });
          lines.push(`${where}volume ${volumeSlug} (${host.path}) → moved into the volume's content as ${rel} (${f.content.sizeBytes} B, render ${renderOf(f)}); image volume ${m.imageVolume} → Not mounted (the volume provides it)`);
        } else lines.push(`${where}volume ${volumeSlug} (${host.path}) → moved into the volume's content as ${rel} (${f.content.sizeBytes} B, render ${renderOf(f)}); extra mount removed`);
      }
      c.volumes = keep;
    }
  }
  // 3. remove Files nothing references any more (conservative text search over the sites)
  const removedFiles: string[] = [];
  const sitesText = JSON.stringify(cfg.sites);
  for (const slug of touched) {
    const quoted = JSON.stringify(slug);
    const exprRef = new RegExp(`file\\.${slug.replace(/[-]/g, '\\-')}(?![a-z0-9-])`);
    if (sitesText.includes(`"fileSlug":${quoted}`) || sitesText.includes(`"certFileSlug":${quoted}`) || exprRef.test(sitesText)) continue;
    if (!fileOf(slug)) continue;
    cfg.files = cfg.files.filter((f) => f.fileSlug !== slug);
    removedFiles.push(slug);
    lines.push(`file ${slug} removed: nothing mounts it any more (its content lives in the volume's own content now)`);
  }
  for (const slug of touched) if (!removedFiles.includes(slug) && fileOf(slug)) lines.push(`file ${slug} kept: still mounted read-only (or referenced) elsewhere`);
  return { config: cfg, changed: lines.length > 0 || notes.some((n) => n.includes('removed') || n.includes('dropped')), lines, notes, removedFiles, moved };
}

// ───────────────────────────────────────────────────────────── cloud records
export type VolumeContentOrigin = 'snapshot' | 'edit' | 'restore';
/** what made an agent snapshot */
export type VolumeSnapshotTrigger = 'manual-cloud' | 'manual-agent' | 'auto' | 'before-apply' | 'after-apply';
export interface VolumeContentVersionSummary {
  versionId: Ulid;
  seq: number;
  origin: VolumeContentOrigin;
  treeHash: Sha256;
  createdAt: Ts;
  createdBy: string;
}
/** `deployments/{id}/volumeContent/{siteSlug}:{volumeSlug}/versions/{versionId}`; the tree = blob `files/<treeHash hex>` */
export interface VolumeContentVersion extends VolumeContentVersionSummary {
  deploymentId: string;
  siteSlug: Slug;
  volumeSlug: string;
  trigger: VolumeSnapshotTrigger | null; //   snapshots only
  fileCount: number;
  dirCount: number;
  totalBytes: number;
  /**
   * edit: the version the editor started from; restore: the LATEST version at the time of the restore (so a Send of a
   * restore plans against what the site had then — live = that snapshot → a clean fast-forward back to the old content);
   * snapshot: null. The push base = the newest SNAPSHOT ancestor along this chain.
   */
  baseVersionId: Ulid | null;
  /** restore only: the version whose tree was copied */
  restoredFromVersionId: Ulid | null;
  note: string; //                             ≤ 500
  /** snapshots: files left out by the excludes / skipped (symlinks, special files, bad names, too large) */
  excludedCount: number;
  skippedCount: number;
  skipped: { path: string; reason: 'symlink' | 'special' | 'bad-name' | 'too-large' }[]; // ≤ 100 listed
  /** snapshots: the agent's id (idempotent store-and-forward re-uploads), the cloud request / push it answered */
  agentSnapshotId: Ulid | null;
  requestId: Ulid | null;
  pushId: Ulid | null;
  /** change counts against the previous version (by seq) */
  changes: TreeChangeCounts;
}
export type VolumeContentPushStatus = 'pending' | 'applied' | 'kept-live' | 'cancelled' | 'superseded' | 'failed';
export interface VolumeContentPushResult {
  resolution: VolumePushResolution | 'fast-forward' | 'up-to-date';
  written: number;
  deleted: number;
  keptLive: number;
  /** the volume snapshot taken before writing (Backups) */
  snapshotId: Ulid | null;
  restarted: string[];
  error: string | null;
  /** the version uploaded right after the apply (trigger after-apply) */
  afterVersionId: Ulid | null;
}
/** a cloud → site send of one version (state.push = the newest one) */
export interface VolumeContentPush {
  pushId: Ulid;
  siteSlug: Slug;
  volumeSlug: string;
  versionId: Ulid;
  /** the version's seq (labels: "cloud version seq 7"); absent on pushes stored before it existed */
  versionSeq?: number;
  treeHash: Sha256;
  /** the newest SNAPSHOT ancestor of the version (edit / restore chain): what the cloud believed the live tree was; null = unknown (every difference is a conflict) */
  baseVersionId: Ulid | null;
  baseTreeHash: Sha256 | null;
  /** restart the containers that mount the volume after writing (the site can change it when approving) */
  restart: boolean;
  note: string;
  status: VolumeContentPushStatus;
  createdAt: Ts;
  createdBy: string;
  resolvedAt: Ts | null;
  resolvedBy: string | null;
  result: VolumeContentPushResult | null;
}
/** what the agent last reported (heartbeat volumeSync) */
export interface VolumeSyncReport {
  volumeSlug: string;
  state: VolumeSyncState;
  liveTreeHash: Sha256 | null;
  /** the last snapshot's hash (uploaded or still queued) */
  snapshotTreeHash: Sha256 | null;
  liveChanged: boolean;
  queued: number;
  scannedAt: Ts | null;
  lastError: string | null;
}
/** `deployments/{id}/volumeContent/{siteSlug}:{volumeSlug}` (+ the draft's persist setting, joined on read) */
export interface VolumeContentState {
  deploymentId: string;
  siteSlug: Slug;
  volumeSlug: string;
  /** the draft's SiteVolume.persist (null = not persist-to-cloud in the draft any more; history is kept) */
  persist: VolumePersist | null;
  /** is persist on in the HEAD revision the site runs (the agent snapshots only then) */
  persistInHead: boolean;
  latest: VolumeContentVersionSummary | null;
  lastSnapshot: VolumeContentVersionSummary | null;
  versionCount: number;
  push: VolumeContentPush | null;
  snapshotRequest: { requestId: Ulid; createdAt: Ts; createdBy: string } | null;
  live: (VolumeSyncReport & { reportedAt: Ts }) | null;
  /** cloud-side summary for badges: in-sync | live-changed | cloud-ahead | conflict | never-snapshotted */
  sync: VolumeSyncState;
  updatedAt: Ts | null;
}
/** CloudView.volumeContent item (only volumes with a pending push or an open snapshot request) */
export interface VolumeContentCloudItem {
  siteSlug: Slug;
  volumeSlug: string;
  push: VolumeContentPush | null; //   status pending only
  snapshotRequest: { requestId: Ulid; createdAt: Ts; createdBy: string } | null;
}
export interface CreateVolumeContentVersionRequest {
  /** the version the editor started from; must equal the current latest (else 409 conflict details { latest }) — null when there is none */
  baseVersionId: Ulid | null;
  tree: VolumeTree;
  note: string;
  /** §21 #107: the stage holding the new blobs of `tree` (POST /v1/stages); promoted before the version is written */
  stageId?: Ulid;
}
export interface VolumeContentVersionResponse {
  version: VolumeContentVersion;
  tree: VolumeTree;
}
export interface RestoreVolumeContentRequest {
  note: string;
}
export interface SendVolumeContentRequest {
  versionId: Ulid;
  restart: boolean;
  note: string;
}
/** agent → cloud: one snapshot (idempotent by snapshotId) */
export interface AgentVolumeSnapshotRequest {
  snapshotId: Ulid;
  trigger: VolumeSnapshotTrigger;
  takenAt: Ts;
  /** the actor (manual) or null (auto / after-apply: createdBy "agent:<site>") */
  actor: string | null;
  tree: VolumeTree;
  treeHash: Sha256;
  excludedCount: number;
  skippedCount: number;
  skipped: VolumeContentVersion['skipped'];
  requestId: Ulid | null;
  pushId: Ulid | null;
  note: string;
}
export interface AgentVolumeSnapshotResponse {
  version: VolumeContentVersion;
  /** true = this snapshotId was stored before (a retry) */
  existing: boolean;
}
export interface AgentBlobsCheckRequest {
  sha256s: Sha256[]; // ≤ 1000
}
export interface AgentBlobsCheckResponse {
  missing: Sha256[];
}
export interface AgentVolumePushReport {
  status: 'applied' | 'kept-live' | 'failed';
  resolvedBy: string;
  resolvedAt: Ts;
  result: VolumeContentPushResult;
}

// ───────────────────────────────────────────────────────────── agent local view (Volumes module)
/**
 *   in-sync           — live = the last snapshot, no pending push
 *   live-changed      — the live tree differs from the last snapshot (an auto / manual snapshot will upload it)
 *   cloud-ahead       — a pending push and live = its base (a clean fast-forward)
 *   conflict          — a pending push and live ≠ its base (3-way choice)
 *   never-snapshotted — persist is on, no snapshot yet
 *   pending-upload    — snapshots wait for the cloud (store-and-forward)
 *   error             — the last scan / snapshot failed (lastError)
 */
export type VolumeSyncState = 'in-sync' | 'live-changed' | 'cloud-ahead' | 'conflict' | 'never-snapshotted' | 'pending-upload' | 'error';
export interface VolumeLocalSnapshot {
  snapshotId: Ulid;
  /** null = queued (not uploaded yet) */
  versionId: Ulid | null;
  seq: number | null;
  treeHash: Sha256;
  trigger: VolumeSnapshotTrigger;
  takenAt: Ts;
  uploadedAt: Ts | null;
  fileCount: number;
  totalBytes: number;
  actor: string | null;
  error: string | null;
}
export interface VolumeSyncStatus {
  siteSlug: Slug;
  volumeSlug: string;
  label: string;
  persist: VolumePersist;
  state: VolumeSyncState;
  lastSnapshot: VolumeLocalSnapshot | null;
  live: { treeHash: Sha256 | null; fileCount: number; totalBytes: number; excludedCount: number; scannedAt: Ts | null; changed: boolean } | null;
  push: VolumeContentPush | null; // pending only
  queued: number;
  lastError: string | null;
  /** the 20 newest local snapshots (uploaded + queued) */
  history: VolumeLocalSnapshot[];
  /** containers that mount the volume (restart candidates) */
  users: string[];
  /** §21 #117 the content the site's head commits (state compares the live tree with it); absent on agents < 0.19 */
  committed?: { treeHash: Sha256; fileCount: number } | null;
}
export interface VolumeSyncSnapshotRequest {
  note?: string;
}
export interface VolumeSyncSnapshotResponse {
  snapshot: VolumeLocalSnapshot;
  /** false = no change since the last snapshot (nothing new stored) */
  created: boolean;
}
export interface VolumePushPlan {
  pushId: Ulid;
  versionId: Ulid;
  baseKnown: boolean;
  baseTreeHash: Sha256 | null;
  liveTreeHash: Sha256;
  cloudTreeHash: Sha256;
  /** live = base: applying is a clean fast-forward (no question) */
  fastForward: boolean;
  /** live = cloud: nothing to write */
  upToDate: boolean;
  items: VolumePlanItem[];
  counts: Record<VolumePlanKind, number>;
  /** excluded paths are never touched */
  exclude: string[];
  users: string[];
  restart: boolean;
}
export interface VolumeSyncApplyRequest {
  /** omitted = no intent wait (an apply is a merge, §21 #104 (e)); an intent from an older GUI is still checked */
  intentId?: Ulid;
  pushId: Ulid;
  resolution: VolumePushResolution;
  /** per-file: every conflict path → cloud | live */
  choices?: Record<string, 'cloud' | 'live'>;
  restart: boolean;
  /** the plan's liveTreeHash: the agent refuses (409 conflict) when the live tree changed since the plan */
  liveTreeHash: Sha256;
}
export interface VolumeSyncApplyResponse {
  push: VolumeContentPush;
  result: VolumeContentPushResult;
}

// ───────────────────────────────────────────────────────────── API tables
const DEP = '/v1/deployments/{deploymentId}/volume-content';
const ONE = `${DEP}/{siteSlug}/{volumeSlug}`;
/** Cloud API additions (spread into CLOUD_API; SPEC §6). Deployment-scope ops write `_cloud` activities. */
export const VOLUME_CONTENT_CLOUD_API = {
  listVolumeContent: ep<void, ListResponse<VolumeContentState>>()({ method: 'GET', path: DEP, auth: 'human', perm: 'deployment.config.read', body: 'none', res: 'json', errors: ['not-found'], summary: '§21 #101: every persist-to-cloud volume of the draft (and every volume with stored versions), with sync state.' }),
  getVolumeContent: ep<void, VolumeContentState>()({ method: 'GET', path: ONE, auth: 'human', perm: 'deployment.config.read', body: 'none', res: 'json', errors: ['not-found'], summary: '§21 #101: one volume: latest version, last snapshot, pending push, snapshot request, last agent report.' }),
  listVolumeContentVersions: ep<void, ListResponse<VolumeContentVersion>, { cursor?: string; limit?: number }>()({ method: 'GET', path: `${ONE}/versions`, auth: 'human', perm: 'deployment.config.read', body: 'none', res: 'json', errors: ['not-found'], summary: '§21 #101: versions newest first (seq desc).' }),
  getVolumeContentVersion: ep<void, VolumeContentVersionResponse>()({ method: 'GET', path: `${ONE}/versions/{versionId}`, auth: 'human', perm: 'deployment.config.read', body: 'none', res: 'json', errors: ['not-found'], summary: '§21 #101: one version + its tree (file bytes: GET /v1/blobs/{sha256}).' }),
  createVolumeContentVersion: ep<CreateVolumeContentVersionRequest, VolumeContentVersionResponse>()({ method: 'POST', path: `${ONE}/versions`, auth: 'human', perm: 'deployment.config.write', body: 'json', res: 'json', ok: 201, errors: ['gone'], summary: 'REMOVED §21 #117 (410 gone): persist-to-cloud content is committed with the deployment (Volume › Content → Commit) and written by the site when it merges.' }),
  restoreVolumeContentVersion: ep<RestoreVolumeContentRequest, VolumeContentVersionResponse>()({ method: 'POST', path: `${ONE}/versions/{versionId}/restore`, auth: 'human', perm: 'deployment.config.write', body: 'json', res: 'json', ok: 201, errors: ['gone'], summary: 'REMOVED §21 #117 (410 gone): persist-to-cloud content is committed with the deployment (Volume › Content → Commit) and written by the site when it merges.' }),
  requestVolumeSnapshot: ep<void, VolumeContentState>()({ method: 'POST', path: `${ONE}/snapshot`, auth: 'human', perm: 'deployment.config.write', body: 'none', res: 'json', errors: ['gone'], summary: 'REMOVED §21 #117 (410 gone): persist-to-cloud content is committed with the deployment (Volume › Content → Commit) and written by the site when it merges.' }),
  sendVolumeContent: ep<SendVolumeContentRequest, VolumeContentPush>()({ method: 'POST', path: `${ONE}/push`, auth: 'human', perm: 'deployment.config.publish', body: 'json', res: 'json', ok: 201, errors: ['gone'], summary: 'REMOVED §21 #117 (410 gone): persist-to-cloud content is committed with the deployment (Volume › Content → Commit) and written by the site when it merges.' }),
  cancelVolumeContentPush: ep<void, VolumeContentState>()({ method: 'DELETE', path: `${ONE}/push`, auth: 'human', perm: 'deployment.config.publish', body: 'none', res: 'json', errors: ['gone'], summary: 'REMOVED §21 #117 (410 gone): persist-to-cloud content is committed with the deployment (Volume › Content → Commit) and written by the site when it merges.' }),
  // agent (site key)
  agentCheckBlobs: ep<AgentBlobsCheckRequest, AgentBlobsCheckResponse>()({ method: 'POST', path: '/v1/agent/volume-content/blobs/check', auth: 'agent', perm: 'site key', body: 'json', res: 'json', errors: ['invalid'], summary: '§21 #101: which of these blobs (≤ 1000) the cloud does not have yet.' }),
  agentPutBlob: ep<void, { sha256: Sha256; sizeBytes: number }>()({ method: 'PUT', path: '/v1/agent/volume-content/blobs/{sha256}', auth: 'agent', perm: 'site key (a persist-to-cloud volume of the signer in the head)', body: 'raw', res: 'json', errors: ['invalid', 'too-large', 'forbidden'], summary: '§21 #101: upload one file of a snapshot (raw ≤ 24 MiB, verified against {sha256}; content-addressed files/<hex>; existing → 200).' }),
  agentCreateVolumeSnapshot: ep<AgentVolumeSnapshotRequest, AgentVolumeSnapshotResponse>()({ method: 'POST', path: '/v1/agent/volume-content/{volumeSlug}/versions', auth: 'agent', perm: 'site key (persist on for that volume of the signer in the head)', body: 'json', res: 'json', ok: 201, errors: ['invalid', 'forbidden'], summary: '§21 #101: store a snapshot as a version (the AGENT logs volume-content.snapshot-uploaded in its own chain; no _cloud entry) (idempotent by snapshotId; treeHash verified; every blob must exist → 422 V_BLOB_MISSING details.missing). Clears the answered snapshot request.' }),
  agentReportVolumePush: ep<AgentVolumePushReport, VolumeContentPush>()({ method: 'POST', path: '/v1/agent/volume-content/{volumeSlug}/pushes/{pushId}', auth: 'agent', perm: 'site key', body: 'json', res: 'json', errors: ['not-found', 'invalid'], summary: '§21 #101: the site applied / kept live / failed a push (idempotent; a superseded / cancelled push keeps its status). agentEpoch+1.' }),
} as const;

const S = 'session' as const;
const SA = 'session+actor' as const;
const AV = '/_opm/api/sites/{siteSlug}/volume-sync';
/** Agent local API (Volumes module, §7.7); another site = forwarded over the mesh like the volume manager. */
export const VOLUME_SYNC_API = {
  listVolumeSync: ep<void, ListResponse<VolumeSyncStatus>>()({ method: 'GET', path: AV, auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: '§21 #101: every persist-to-cloud volume of the head with its sync state.' }),
  getVolumeSync: ep<void, VolumeSyncStatus>()({ method: 'GET', path: `${AV}/{volumeSlug}`, auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: '§21 #101: one volume (rescans the fingerprint when older than 10 s).' }),
  snapshotVolumeSync: ep<VolumeSyncSnapshotRequest, VolumeSyncSnapshotResponse>()({ method: 'POST', path: `${AV}/{volumeSlug}/snapshot`, auth: SA, perm: null, body: 'json', res: 'json', ok: 201, errors: ['not-found', 'invalid', 'locked', 'conflict', 'too-large'], summary: '§21 #101: Snapshot now (manual-agent): hash, copy changed files into the blob store, queue for upload (uploads at once when the cloud is reachable). too-large: over persist.maxBytes (details { totalBytes, maxBytes, biggest[] }).' }),
  planVolumeSync: ep<void, VolumePushPlan>()({ method: 'GET', path: `${AV}/{volumeSlug}/plan`, auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'invalid', 'unavailable'], summary: '§21 #101: the 3-way plan of the pending push against the live tree (fetches the cloud + base trees). invalid: no pending push.' }),
  volumeSyncFile: ep<void, Response, { side: 'live' | 'cloud' | 'base'; path: string }>()({ method: 'GET', path: `${AV}/{volumeSlug}/file`, auth: S, perm: null, body: 'none', res: 'raw', errors: ['not-found', 'invalid', 'unavailable'], summary: '§21 #101: bytes of one file on one side of the plan (per-file diff).' }),
  applyVolumeSyncStart: ep<void, T.ApproveStartResponse>()({ method: 'POST', path: `${AV}/{volumeSlug}/apply/start`, auth: SA, perm: null, body: 'none', res: 'json', ok: 201, errors: ['not-found', 'invalid', 'locked', 'conflict'], summary: '§21 #101: intent (kind volume-content-apply, 60 s, fast-forwardable) to apply the pending push.' }),
  applyVolumeSync: ep<VolumeSyncApplyRequest, VolumeSyncApplyResponse>()({ method: 'POST', path: `${AV}/{volumeSlug}/apply`, auth: SA, perm: null, body: 'json', res: 'json', errors: ['not-found', 'invalid', 'gate-closed', 'locked', 'conflict'], summary: '§21 #101: apply after the wait: before-apply upload of live changes, volume snapshot (Backups), [stop users], converge live → target, [start users], after-apply snapshot, report to the cloud. conflict: the live tree changed since the plan (re-plan).' }),
  keepLiveVolumeSync: ep<{ pushId: Ulid }, VolumeContentPush>()({ method: 'POST', path: `${AV}/{volumeSlug}/keep-live`, auth: SA, perm: null, body: 'json', res: 'json', errors: ['not-found', 'invalid'], summary: '§21 #101: decline the pending push (nothing written; status kept-live; a snapshot of live is uploaded so the cloud sees it).' }),
} as const;
export function createVolumeSyncClient(baseUrl: string, auth: AuthProvider | null = null, init?: Parameters<typeof createClient>[3]): Client<typeof VOLUME_SYNC_API> {
  return createClient(VOLUME_SYNC_API, baseUrl, auth, init);
}

/** cloud-side sync summary from the stored state (badges) */
export function cloudSyncState(s: Pick<VolumeContentState, 'push' | 'lastSnapshot' | 'live'>): VolumeSyncState {
  const pending = s.push?.status === 'pending' ? s.push : null;
  const liveHash = s.live?.liveTreeHash ?? s.lastSnapshot?.treeHash ?? null;
  if (pending) {
    if (pending.baseTreeHash && liveHash === pending.baseTreeHash) return 'cloud-ahead';
    if (liveHash === pending.treeHash) return 'in-sync';
    // the agent ran the per-file plan for this live tree: no file changed on both sides → it applies without a choice
    if (s.live?.state === 'cloud-ahead' && s.live.liveTreeHash === liveHash) return 'cloud-ahead';
    return 'conflict';
  }
  if (!s.lastSnapshot && !s.live?.snapshotTreeHash) return 'never-snapshotted';
  if (s.live?.state === 'error') return 'error';
  if (s.live?.queued) return 'pending-upload';
  if (s.live?.liveChanged) return 'live-changed';
  return 'in-sync';
}
export const VOLUME_SYNC_STATE_LABEL: Record<VolumeSyncState, string> = {
  'in-sync': 'In sync',
  'live-changed': 'Live changed',
  'cloud-ahead': 'Cloud ahead',
  conflict: 'Conflict',
  'never-snapshotted': 'No snapshot yet',
  'pending-upload': 'Waiting to upload',
  error: 'Error',
};
