/**
 * Root audit log helpers (SPEC-final §6.2): secret redaction and the before → after deep diff stored in
 * AuditLogEntry.changes. Used by the backend (writes) and by the frontend mock.
 *
 * Redaction rules (a value is a SECRET when):
 *   - its key matches /password|secret|token|accessKey|privateKey/i, it is a string or number, and the key is not an
 *     identifier (…Slug, …Slugs, …Id, …Ids, …Version, …At, …By, …Count, …Fingerprint, tokenHash, secret = boolean);
 *   - it is `value` / `plainValue` of an item of a `secrets` list (template / deployment SecretDef);
 *   - it is `value` of an object with `secret: true` (image env defaults marked secret);
 *   - it is a non-identifier leaf INSIDE an object stored under a secret-named key (§21 #84: e.g.
 *     `lastSetPassword: { value, setAt, setBy }`, `lastResetLink: { value, … }`) — `value` is masked, `setAt` stays.
 * Secrets are shown as "***" (null / "" stay as they are, so "is set" stays visible). A diff compares the RAW values
 * and only then masks them, so a changed secret yields { path, before: "***", after: "***", secret: true }.
 */
import type { AuditChange, AuditTruncated } from './types.js';

export const AUDIT_MAX_CHANGES = 200;
export const AUDIT_MAX_VALUE_BYTES = 2048;
export const SECRET_MASK = '***';

const SECRET_KEY = /password|secret|token|accesskey|privatekey|resetlink/i;
const SAFE_KEY = /(slug|slugs|id|ids|version|at|by|count|fingerprint|^tokenhash)$/i;
/** List items are matched by the first of these keys that every item has (unique, string) — readable paths. */
const ID_KEYS = ['siteSlug', 'variableSlug', 'secretSlug', 'fileSlug', 'serviceSlug', 'routeId', 'hostname', 'name', 'tagSlug', 'resource', 'uid', 'path'];

/** scope = we are inside an object stored under a secret-named key (every non-identifier leaf is secret) */
type Ctx = { parent: unknown; listKey: string | null; scope?: boolean };

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

/** Is `key` of `parent` (an item of the list `listKey`, if any) a secret value? */
export function isSecretField(key: string, parent: unknown, listKey: string | null, scope = false): boolean {
  if (scope && !SAFE_KEY.test(key)) return true;
  if ((key === 'value' || key === 'plainValue') && listKey === 'secrets') return true;
  if (key === 'value' && isPlainObject(parent) && parent.secret === true) return true;
  return SECRET_KEY.test(key) && !SAFE_KEY.test(key);
}
const isPrim = (v: unknown) => typeof v === 'string' || typeof v === 'number';
const isObj = (v: unknown) => typeof v === 'object' && v !== null;
function maskValue(v: unknown): unknown {
  if (v === null || v === undefined || v === '') return v ?? null;
  if (typeof v === 'string' || typeof v === 'number') return SECRET_MASK;
  return v;
}

/** Children of an object stored under a secret-named key are secret leaves (identifiers excepted). */
function childScope(ctx: Ctx, key: string | null): boolean {
  return !!ctx.scope || (key !== null && SECRET_KEY.test(key) && !SAFE_KEY.test(key));
}
function redactIn(v: unknown, ctx: Ctx, key: string | null): unknown {
  if (key !== null && isSecretField(key, ctx.parent, ctx.listKey, ctx.scope) && (typeof v === 'string' || typeof v === 'number')) return maskValue(v);
  if (Array.isArray(v)) return v.map((x) => redactIn(x, { parent: v, listKey: key, scope: ctx.scope }, null));
  if (isPlainObject(v)) {
    const out: Record<string, unknown> = {};
    const scope = childScope(ctx, key);
    for (const [k, x] of Object.entries(v)) out[k] = redactIn(x, { parent: v, listKey: ctx.listKey && key === null ? ctx.listKey : null, scope }, k);
    return out;
  }
  return v;
}
/** A deep copy with every secret value replaced by "***" (see the rules above). */
export function redactSecrets<T>(v: T): T {
  return redactIn(v, { parent: null, listKey: null }, null) as T;
}

function jsonBytes(v: unknown): number {
  try {
    return new TextEncoder().encode(JSON.stringify(v) ?? 'null').length;
  } catch {
    return 0;
  }
}
/** Values whose JSON is larger than 2 KB become { $truncated, bytes, preview } (input must already be redacted). */
export function capAuditValue(v: unknown, max = AUDIT_MAX_VALUE_BYTES): unknown {
  if (v === undefined) return null;
  const bytes = jsonBytes(v);
  if (bytes <= max) return v;
  const t: AuditTruncated = { $truncated: true, bytes, preview: (JSON.stringify(v) ?? '').slice(0, 2000) };
  return t;
}
export function isAuditTruncated(v: unknown): v is AuditTruncated {
  return isPlainObject(v) && v.$truncated === true;
}

function idKeyOf(a: unknown[], b: unknown[]): string | null {
  const all = [...a, ...b];
  if (!all.length || !all.every(isPlainObject)) return null;
  for (const k of ID_KEYS) {
    const okA = a.every((x) => typeof (x as Record<string, unknown>)[k] === 'string');
    const okB = b.every((x) => typeof (x as Record<string, unknown>)[k] === 'string');
    if (!okA || !okB) continue;
    const ua = new Set(a.map((x) => (x as Record<string, unknown>)[k]));
    const ub = new Set(b.map((x) => (x as Record<string, unknown>)[k]));
    if (ua.size === a.length && ub.size === b.length) return k;
  }
  return null;
}
const IDENT = /^[A-Za-z_$][A-Za-z0-9_$-]*$/;
function join(path: string, key: string): string {
  if (!path) return IDENT.test(key) ? key : `[${JSON.stringify(key)}]`;
  return IDENT.test(key) ? `${path}.${key}` : `${path}[${JSON.stringify(key)}]`;
}
function same(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (typeof a !== typeof b || a === null || b === null || typeof a !== 'object') return false;
  return JSON.stringify(a) === JSON.stringify(b);
}

export interface AuditDiffOptions {
  /** top-level keys left out of the diff (default: updatedAt, updatedBy, cloudActivitySeq) */
  ignore?: string[];
  maxChanges?: number;
  maxValueBytes?: number;
}
export interface AuditDiffResult {
  changes: AuditChange[];
  /** number of changed leaves before the cap */
  total: number;
}

/**
 * Deep diff of two documents (null = did not exist). Objects recurse by key, lists recurse by an identity key
 * (siteSlug, variableSlug, name, …; else by index); a side that is missing yields one change with the whole value.
 * create (before null) → one change per top-level field; delete (after null) → the same, reversed.
 * Every value is redacted, then capped at 2 KB; the list is capped at 200 changes.
 */
export function auditDiff(before: unknown, after: unknown, opts: AuditDiffOptions = {}): AuditDiffResult {
  const ignore = new Set(opts.ignore ?? ['updatedAt', 'updatedBy', 'cloudActivitySeq']);
  const maxChanges = opts.maxChanges ?? AUDIT_MAX_CHANGES;
  const maxBytes = opts.maxValueBytes ?? AUDIT_MAX_VALUE_BYTES;
  const out: AuditChange[] = [];
  let total = 0;
  const emit = (path: string, a: unknown, b: unknown, ctx: Ctx, key: string | null) => {
    total++;
    if (out.length >= maxChanges) return;
    const secret = key !== null && isSecretField(key, ctx.parent, ctx.listKey, ctx.scope) && (isPrim(a) || isPrim(b)) && !isObj(a) && !isObj(b);
    if (secret) {
      out.push({ path, before: maskValue(a), after: maskValue(b), secret: true });
      return;
    }
    out.push({ path, before: capAuditValue(redactIn(a ?? null, ctx, key), maxBytes), after: capAuditValue(redactIn(b ?? null, ctx, key), maxBytes) });
  };
  const walk = (path: string, a: unknown, b: unknown, ctx: Ctx, key: string | null) => {
    if (same(a, b)) return;
    if (key !== null && isSecretField(key, ctx.parent, ctx.listKey, ctx.scope) && (isPrim(a) || isPrim(b))) return emit(path, a, b, ctx, key);
    if (isPlainObject(a) && isPlainObject(b)) {
      const keys = [...new Set([...Object.keys(a), ...Object.keys(b)])].sort();
      const childList = ctx.listKey && key === null ? ctx.listKey : null;
      const scope = childScope(ctx, key);
      for (const k of keys) {
        if (!path && ignore.has(k)) continue;
        walk(join(path, k), a[k], b[k], { parent: b[k] === undefined ? a : b, listKey: childList, scope }, k);
      }
      return;
    }
    if (Array.isArray(a) && Array.isArray(b)) {
      const idk = idKeyOf(a, b);
      if (idk) {
        const mapA = new Map(a.map((x) => [(x as Record<string, unknown>)[idk] as string, x]));
        const mapB = new Map(b.map((x) => [(x as Record<string, unknown>)[idk] as string, x]));
        const ids = [...mapA.keys(), ...[...mapB.keys()].filter((k) => !mapA.has(k))];
        for (const id of ids) walk(`${path}[${id}]`, mapA.get(id), mapB.get(id), { parent: b, listKey: key, scope: ctx.scope }, null);
        const orderA = [...mapA.keys()].filter((k) => mapB.has(k));
        const orderB = [...mapB.keys()].filter((k) => mapA.has(k));
        if (orderA.join('\u0000') !== orderB.join('\u0000')) emit(`${path}[*order]`, orderA, orderB, ctx, null);
        return;
      }
      const n = Math.max(a.length, b.length);
      for (let i = 0; i < n; i++) walk(`${path}[${i}]`, a[i], b[i], { parent: b, listKey: key, scope: ctx.scope }, null);
      return;
    }
    emit(path, a, b, ctx, key);
  };
  const root = { parent: null, listKey: null };
  if ((before === null || before === undefined) && isPlainObject(after)) {
    for (const k of Object.keys(after).sort()) if (!ignore.has(k)) emit(k, null, after[k], { parent: after, listKey: null }, k);
  } else if ((after === null || after === undefined) && isPlainObject(before)) {
    for (const k of Object.keys(before).sort()) if (!ignore.has(k)) emit(k, before[k], null, { parent: before, listKey: null }, k);
  } else walk('', before ?? null, after ?? null, root, null);
  return { changes: out, total };
}

/** action filter: exact, or a prefix ending in "." */
export function auditActionMatches(action: string, filter: string): boolean {
  return filter.endsWith('.') ? action.startsWith(filter) : action === filter;
}
/** "template.updated" → ["template.", "template.updated"] (stored for array-contains prefix filters). */
export function auditActionKeys(action: string): string[] {
  const parts = action.split('.');
  const out: string[] = [];
  for (let i = 1; i < parts.length; i++) out.push(parts.slice(0, i).join('.') + '.');
  out.push(action);
  return out;
}
