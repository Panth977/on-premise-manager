/**
 * RFC 8785 JSON Canonicalization Scheme (JCS) — the ONLY JSON form that is ever hashed in OPM
 * (Revision.hash, Revision.catalogHash, Activity.hash, spec_hash, the `json` expression filter).
 *
 * Rules (Go agent/internal/canonical implements the same; vectors in testdata/canonical-vectors.json):
 * - objects: keys sorted by UTF-16 code units, no whitespace; `undefined` members are dropped
 * - arrays: in order; `undefined` items become null (JSON.stringify behaviour)
 * - strings: JSON.stringify escaping (\" \\ \b \f \n \r \t, other < 0x20 as \u00xx lower hex; nothing else escaped)
 * - numbers: ECMAScript Number::toString (formatNumber below); NaN / ±Infinity are errors
 */

export class CanonicalError extends Error {}

/** ECMAScript Number.prototype.toString(10) — also used to turn numbers into text in expressions. */
export function formatNumber(n: number): string {
  if (!Number.isFinite(n)) throw new CanonicalError(`not a finite number: ${n}`);
  if (Object.is(n, -0)) return '0';
  return String(n);
}

export function canonicalize(value: unknown): string {
  return ser(value);
}

function ser(v: unknown): string {
  if (v === null) return 'null';
  switch (typeof v) {
    case 'boolean':
      return v ? 'true' : 'false';
    case 'number':
      return formatNumber(v);
    case 'string':
      return JSON.stringify(v);
    case 'object': {
      if (Array.isArray(v)) return '[' + v.map((x) => (x === undefined ? 'null' : ser(x))).join(',') + ']';
      const o = v as Record<string, unknown>;
      const keys = Object.keys(o)
        .filter((k) => o[k] !== undefined)
        .sort(compareUtf16);
      return '{' + keys.map((k) => JSON.stringify(k) + ':' + ser(o[k])).join(',') + '}';
    }
    default:
      throw new CanonicalError(`cannot canonicalize ${typeof v}`);
  }
}

/** Compare by UTF-16 code units (what JS < does on strings). */
export function compareUtf16(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}

/**
 * The exact string that is hashed for an activity:
 *   (prevHash ?? "") + JCS({ activityId, deploymentId, siteSlug, seq, ts, actor, origin, type, summary, data })
 * hash = "sha256:" + hex(sha256(utf8(that string))).
 * prevHash, hash, uploadedVia, uploadedAt are NOT part of the JSON.
 */
export function activityHashInput(a: {
  activityId: string;
  deploymentId: string;
  siteSlug: string;
  seq: number;
  ts: string;
  actor: string | null;
  origin: string;
  type: string;
  summary: string;
  data: Record<string, unknown>;
  prevHash: string | null;
}): string {
  const body = {
    activityId: a.activityId,
    deploymentId: a.deploymentId,
    siteSlug: a.siteSlug,
    seq: a.seq,
    ts: a.ts,
    actor: a.actor,
    origin: a.origin,
    type: a.type,
    summary: a.summary,
    data: a.data,
  };
  return (a.prevHash ?? '') + canonicalize(body);
}

/**
 * The string an agent signs (Ed25519) for a signed request:
 *   METHOD "\n" PATH "\n" TS "\n" NONCE "\n" BODYHASH
 * METHOD upper-case; PATH = path starting at "/v1/" (cloud) or "/_opm/peer/" (peer) including "?query" exactly as
 * sent; TS = unix seconds (decimal); NONCE = 16 random bytes base64url (no padding, 22 chars);
 * BODYHASH = lowercase hex sha256 of the raw body bytes (empty body → hash of "").
 */
export function signedRequestString(method: string, path: string, ts: number | string, nonce: string, bodySha256Hex: string): string {
  return `${method.toUpperCase()}\n${path}\n${ts}\n${nonce}\n${bodySha256Hex}`;
}

/**
 * The string an agent signs for a peer JSON response:
 *   "RESPONSE" "\n" STATUS "\n" REQUEST_NONCE "\n" TS "\n" BODYHASH
 */
export function signedResponseString(status: number, requestNonce: string, ts: number | string, bodySha256Hex: string): string {
  return `RESPONSE\n${status}\n${requestNonce}\n${ts}\n${bodySha256Hex}`;
}

export const SHA256_EMPTY_HEX = 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855';
