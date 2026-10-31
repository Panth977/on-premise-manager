/**
 * Local adapter value codec + document mutation helpers (pure, no I/O).
 *
 * Stored JSON: plain JSON; Timestamp → {"$timestamp":{"seconds":S,"nanos":N}}; Buffer / Uint8Array → {"$bytes":"<b64>"};
 * Date → Timestamp (like Firestore); `undefined` properties dropped (deep). Index columns hold `ixOf(value)`.
 */
import { FieldTransform, StoreError, Timestamp, type DocData } from './types/docstore.js';

export type Json = null | boolean | number | string | Json[] | { [k: string]: Json };

export function isPlainObject(v: unknown): v is Record<string, unknown> {
  if (!v || typeof v !== 'object') return false;
  const p = Object.getPrototypeOf(v);
  return p === Object.prototype || p === null;
}
const bad = (m: string) => new StoreError('invalid-argument', m);

// ───────────────────────────────────────── encode / decode
function enc(v: unknown, where: string): Json | undefined {
  if (v === undefined) return undefined;
  if (v === null || typeof v === 'string' || typeof v === 'boolean') return v;
  if (typeof v === 'number') {
    if (!Number.isFinite(v)) throw bad(`${where}: non-finite numbers are not supported`);
    return v;
  }
  if (v instanceof Timestamp) return { $timestamp: { seconds: v.seconds, nanos: v.nanoseconds } };
  if (v instanceof Date) return enc(Timestamp.fromDate(v), where);
  if (Buffer.isBuffer(v) || v instanceof Uint8Array) return { $bytes: Buffer.from(v.buffer, v.byteOffset, v.byteLength).toString('base64') };
  if (v instanceof FieldTransform) throw bad(`${where}: a FieldTransform cannot be stored (only at a top-level / map position of a write)`);
  if (Array.isArray(v)) {
    return v.map((x, i) => {
      if (x === undefined) throw bad(`${where}[${i}]: undefined inside an array`);
      return enc(x, `${where}[${i}]`) as Json;
    });
  }
  if (isPlainObject(v)) {
    const o: Record<string, Json> = {};
    for (const [k, x] of Object.entries(v)) {
      const e = enc(x, `${where}.${k}`);
      if (e !== undefined) o[k] = e;
    }
    return o;
  }
  throw bad(`${where}: unsupported value type ${Object.prototype.toString.call(v)}`);
}
/** document → stored JSON string */
export function encodeDoc(d: DocData): string {
  return JSON.stringify(enc(d, 'doc') ?? {});
}
export function encodeValue(v: unknown): Json {
  return enc(v, 'value') ?? null;
}
function dec(v: Json): unknown {
  if (Array.isArray(v)) return v.map(dec);
  if (v && typeof v === 'object') {
    const keys = Object.keys(v);
    if (keys.length === 1) {
      const t = v.$timestamp as { seconds?: unknown; nanos?: unknown } | undefined;
      if (t && typeof t === 'object' && typeof t.seconds === 'number' && typeof t.nanos === 'number') return new Timestamp(t.seconds, t.nanos);
      if (typeof v.$bytes === 'string') return Buffer.from(v.$bytes, 'base64');
    }
    const o: Record<string, unknown> = {};
    for (const k of keys) o[k] = dec(v[k]!);
    return o;
  }
  return v;
}
export function decodeDoc(raw: string): DocData {
  return dec(JSON.parse(raw) as Json) as DocData;
}

/** canonical form for equality (arrayUnion / arrayRemove): sorted map keys */
function canon(v: Json): string {
  if (Array.isArray(v)) return `[${v.map(canon).join(',')}]`;
  if (v && typeof v === 'object')
    return `{${Object.keys(v)
      .sort()
      .map((k) => `${JSON.stringify(k)}:${canon(v[k]!)}`)
      .join(',')}}`;
  return JSON.stringify(v);
}
export const valueKey = (v: unknown) => canon(encodeValue(v));

// ───────────────────────────────────────── index column values
/** value of an index column: string / number as is, boolean 0|1, Timestamp → epoch ms, anything else NULL */
export function ixOf(v: unknown): string | number | null {
  if (typeof v === 'string') return v;
  if (typeof v === 'number') return Number.isFinite(v) ? v : null;
  if (typeof v === 'boolean') return v ? 1 : 0;
  if (v instanceof Timestamp) return v.toMillis();
  if (v instanceof Date) return v.getTime();
  return null;
}
export type ValueKind = 'string' | 'number' | 'boolean' | 'timestamp' | 'null';
export function kindOf(v: unknown): ValueKind {
  if (v === null) return 'null';
  if (typeof v === 'string') return 'string';
  if (typeof v === 'number') return 'number';
  if (typeof v === 'boolean') return 'boolean';
  if (v instanceof Timestamp || v instanceof Date) return 'timestamp';
  throw bad(`unsupported filter / cursor value ${Object.prototype.toString.call(v)} (maps, arrays and bytes cannot be compared)`);
}

// ───────────────────────────────────────── field paths
const FIELD_SEG = /^[A-Za-z0-9_$-]+$/;
export function checkField(field: string): string[] {
  const segs = field.split('.');
  if (!segs.every((s) => FIELD_SEG.test(s))) throw bad(`unsupported field path "${field}"`);
  return segs;
}
export function getField(d: unknown, field: string): unknown {
  let cur: unknown = d;
  for (const s of field.split('.')) {
    if (!isPlainObject(cur)) return undefined;
    cur = cur[s];
  }
  return cur;
}
function setPath(d: Record<string, unknown>, segs: string[], v: unknown) {
  let cur = d;
  for (let i = 0; i < segs.length - 1; i++) {
    const s = segs[i]!;
    if (!isPlainObject(cur[s])) cur[s] = {};
    cur = cur[s] as Record<string, unknown>;
  }
  cur[segs[segs.length - 1]!] = v;
}
function deletePath(d: Record<string, unknown>, segs: string[]) {
  let cur: unknown = d;
  for (let i = 0; i < segs.length - 1; i++) {
    if (!isPlainObject(cur)) return;
    cur = cur[segs[i]!];
  }
  if (isPlainObject(cur)) delete cur[segs[segs.length - 1]!];
}

// ───────────────────────────────────────── writes
const DELETE = Symbol('delete');
function transform(t: FieldTransform, current: unknown, now: Timestamp): unknown {
  switch (t.op) {
    case 'increment':
      return (typeof current === 'number' ? current : 0) + (t.operand as number);
    case 'arrayUnion': {
      const arr = Array.isArray(current) ? [...current] : [];
      const have = new Set(arr.map(valueKey));
      for (const x of t.operand as unknown[]) {
        const k = valueKey(x);
        if (!have.has(k)) {
          have.add(k);
          arr.push(x);
        }
      }
      return arr;
    }
    case 'arrayRemove': {
      const rm = new Set((t.operand as unknown[]).map(valueKey));
      return Array.isArray(current) ? current.filter((x) => !rm.has(valueKey(x))) : [];
    }
    case 'delete':
      return DELETE;
    case 'serverTimestamp':
      return now;
  }
}
/** a value written at a position whose previous content is `current` (maps: transforms relative to their fields) */
function resolve(v: unknown, current: unknown, now: Timestamp): unknown {
  if (v instanceof FieldTransform) return transform(v, current, now);
  if (isPlainObject(v)) {
    const o: Record<string, unknown> = {};
    for (const [k, x] of Object.entries(v)) {
      if (x === undefined) continue;
      const r = resolve(x, undefined, now);
      if (r !== DELETE) o[k] = r;
    }
    return o;
  }
  return v;
}
/** set(data) — replaces the document */
export function applySet(data: DocData, now: Timestamp): DocData {
  return resolve(data, undefined, now) as DocData;
}
/** set(data, { merge: true }) — deep-merges maps (an empty map is a leaf), arrays replaced */
export function applyMerge(current: DocData | null, data: DocData, now: Timestamp): DocData {
  const out = current ? structuredCloneDoc(current) : {};
  const walk = (target: Record<string, unknown>, src: Record<string, unknown>) => {
    for (const [k, v] of Object.entries(src)) {
      if (v === undefined) continue;
      if (v instanceof FieldTransform) {
        const r = transform(v, target[k], now);
        if (r === DELETE) delete target[k];
        else target[k] = r;
      } else if (isPlainObject(v) && Object.keys(v).length > 0) {
        if (!isPlainObject(target[k])) target[k] = {};
        walk(target[k] as Record<string, unknown>, v);
      } else target[k] = resolve(v, undefined, now);
    }
  };
  walk(out, data);
  return out;
}
/** update({ 'a.b': v }) — dotted field paths; a map value replaces the field */
export function applyUpdate(current: DocData, data: DocData, now: Timestamp): DocData {
  const out = structuredCloneDoc(current);
  for (const [path, v] of Object.entries(data)) {
    if (v === undefined) continue;
    const segs = checkField(path);
    const r = resolve(v, getField(out, path), now);
    if (r === DELETE) deletePath(out, segs);
    else setPath(out, segs, r);
  }
  return out;
}
/** deep copy keeping Timestamp / Buffer instances */
export function structuredCloneDoc<T>(v: T): T {
  if (Array.isArray(v)) return v.map(structuredCloneDoc) as T;
  if (isPlainObject(v)) {
    const o: Record<string, unknown> = {};
    for (const [k, x] of Object.entries(v)) o[k] = structuredCloneDoc(x);
    return o as T;
  }
  if (Buffer.isBuffer(v)) return Buffer.from(v) as T;
  return v;
}
/** select(fields…) projection */
export function project(d: DocData, fields: string[]): DocData {
  const out: Record<string, unknown> = {};
  for (const f of fields) {
    const v = getField(d, f);
    if (v !== undefined) setPath(out, f.split('.'), v);
  }
  return out;
}
