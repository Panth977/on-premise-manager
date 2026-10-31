import { createHash, randomBytes, randomInt } from 'node:crypto';
import { canonicalize, RE, LIMITS, type ValidationIssue } from '@opm/shared';
import { E } from './errors.js';

export const sha256Hex = (b: Buffer | Uint8Array | string) => createHash('sha256').update(b).digest('hex');
export const sha256Of = (b: Buffer | Uint8Array | string) => 'sha256:' + sha256Hex(b);
export const jcsHash = (v: unknown) => 'sha256:' + sha256Hex(canonicalize(v));
export const b64url = (b: Buffer | Uint8Array) => Buffer.from(b).toString('base64url');
export const fromB64url = (s: string) => Buffer.from(s, 'base64url');
export const hexOf = (sha: string) => (sha.startsWith('sha256:') ? sha.slice(7) : sha);
export const isHex64 = (s: unknown): s is string => typeof s === 'string' && /^[0-9a-f]{64}$/.test(s);

const CHARSETS = {
  alnum: 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789',
  hex: '0123456789abcdef',
  base64url: 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-',
} as const;
/** Crypto-random secret of an exact length from a charset (SPEC §2.2). */
export function generateSecret(charset: keyof typeof CHARSETS, length: number): string {
  const cs = CHARSETS[charset];
  let s = '';
  for (let i = 0; i < length; i++) s += cs[randomInt(cs.length)];
  return s;
}
export const randomToken = (bytes = 32) => b64url(randomBytes(bytes));

export const clone = <T>(v: T): T => (v === undefined ? v : (JSON.parse(JSON.stringify(v)) as T));
export const sameJson = (a: unknown, b: unknown) => canonicalize(a ?? null) === canonicalize(b ?? null);

// ───────────────────────────────────────── body validation helpers (simple bodies, §11 last paragraph)
export class V {
  readonly list: ValidationIssue[] = [];
  err(path: string, code: string, message: string) {
    this.list.push({ path, code, message, severity: 'error' });
  }
  throwIf() {
    if (this.list.length) throw E.invalid(this.list);
  }
  get ok() {
    return this.list.length === 0;
  }
}
export function asObject(b: unknown): Record<string, unknown> {
  if (!b || typeof b !== 'object' || Array.isArray(b)) throw E.bad('', 'V_TYPE', 'body must be a JSON object');
  return b as Record<string, unknown>;
}
/** Unknown body fields → invalid (V_TYPE "unknown field x"). */
export function onlyKeys(b: Record<string, unknown>, allowed: readonly string[], immutable: readonly string[] = []) {
  const v = new V();
  for (const k of Object.keys(b)) {
    if (allowed.includes(k)) continue;
    if (immutable.includes(k)) v.err(k, 'V_IMMUTABLE', `${k} cannot be changed`);
    else v.err(k, 'V_TYPE', `unknown field ${k}`);
  }
  v.throwIf();
}
export function vLabel(v: V, val: unknown, path = 'label', max = LIMITS.label, required = true) {
  if (val === undefined && !required) return;
  if (typeof val !== 'string' || val.trim() === '') return v.err(path, 'V_REQUIRED', `${path} is required`);
  if (val.length > max) v.err(path, 'V_LENGTH', `${path}: at most ${max} characters`);
}
export function vDescription(v: V, val: unknown, path = 'description') {
  if (val === undefined) return;
  if (typeof val !== 'string') return v.err(path, 'V_TYPE', `${path} must be a string`);
  if (val.length > LIMITS.description) v.err(path, 'V_LENGTH', `${path}: at most ${LIMITS.description} characters`);
}
export function vSlug(v: V, val: unknown, path: string) {
  if (typeof val !== 'string' || !RE.slug.test(val)) v.err(path, 'V_FORMAT', `${path} must match ^[a-z][a-z0-9-]{1,39}$`);
}
export function vHttpsUrlOrNull(v: V, val: unknown, path: string) {
  if (val === undefined || val === null) return;
  if (typeof val !== 'string' || !RE.httpsUrlAny.test(val) || val.length > 2048) v.err(path, 'V_FORMAT', `${path} must be an https URL or null`);
}
export function vString(v: V, val: unknown, path: string, min: number, max: number) {
  if (typeof val !== 'string') return v.err(path, min > 0 ? 'V_REQUIRED' : 'V_TYPE', `${path} must be a string`);
  if (val.length < min || val.length > max) v.err(path, min > 0 && val.length === 0 ? 'V_REQUIRED' : 'V_LENGTH', `${path} must be ${min}–${max} characters`);
}
export function vInt(v: V, val: unknown, path: string, min: number, max: number) {
  if (typeof val !== 'number' || !Number.isInteger(val)) return v.err(path, 'V_TYPE', `${path} must be an integer`);
  if (val < min || val > max) v.err(path, 'V_RANGE', `${path} must be ${min}–${max}`);
}

/** Group a 24-char access key as XXXX-XXXX-…. */
export const groupKey = (k: string) => k.match(/.{1,4}/g)!.join('-');

/** yyyymmdd (UTC) */
export const ymd = (d: Date) => d.toISOString().slice(0, 10).replace(/-/g, '');

/** Ed25519 public key (raw 32 bytes, base64 std) → fingerprint = first 16 hex of sha256(raw). */
export const keyFingerprint = (publicKeyB64: string) => sha256Hex(Buffer.from(publicKeyB64, 'base64')).slice(0, 16);
