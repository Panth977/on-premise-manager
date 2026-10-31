/**
 * DocStore port (SPEC §21 #108) — the document-store operations OPM actually uses, and nothing more.
 *
 * The shape is the path model the business code was written against: collections of JSON documents, sub-collections
 * as paths, collection groups.
 *
 * Implementation: docstore.ts (SQLite: one global file + one file per deployment, `{ id, parent, value JSON, ix_* }`
 * tables).
 *
 * Semantics every adapter guarantees (conformance: test/40-adapter-conformance.test.ts):
 *  - documents are plain JSON objects; the only non-JSON values are `Timestamp` (below) and `Buffer` (bytes);
 *  - `undefined` properties are dropped on write (deep), like Firestore `ignoreUndefinedProperties`;
 *  - `update()` keys are dotted field paths (`a.b.c`); `set(…, { merge: true })` deep-merges maps;
 *  - `create()` fails with code `already-exists` when the document exists; `update()` fails with `not-found`;
 *  - queries: equality / `in` / `array-contains` / range on one field, `orderBy` (+ implicit order by document path
 *    in the direction of the last orderBy), `limit`, `startAfter(snapshot | values…)`, `select(fields…)`;
 *  - transactions: reads before writes, serializable for the documents (and query results) read; the callback may run
 *    more than once on contention; a transaction may span collections of different files (local adapter: see its
 *    README for the ordering rule).
 */

/** Document body. Field values are JSON, `Timestamp`, `Buffer`, or nested objects / arrays of those. */
// eslint-disable-next-line @typescript-eslint/no-explicit-any -- documents are schemaless JSON (decoded by core/codec.ts)
export type DocData = { [field: string]: any };

export type WhereOp = '==' | 'in' | 'array-contains' | '<' | '<=' | '>' | '>=';
export type OrderDir = 'asc' | 'desc';

export interface DocSnap {
  readonly id: string;
  readonly ref: DocRef;
  readonly exists: boolean;
  data(): DocData | undefined;
  /** value at a dotted field path */
  get(field: string): unknown;
}
export interface QueryDocSnap extends DocSnap {
  data(): DocData;
}
export interface QuerySnap {
  readonly docs: QueryDocSnap[];
  readonly empty: boolean;
  readonly size: number;
  forEach(cb: (d: QueryDocSnap) => void): void;
}

export interface Query {
  where(field: string, op: WhereOp, value: unknown): Query;
  orderBy(field: string, dir?: OrderDir): Query;
  limit(n: number): Query;
  /** a snapshot of the previous page's last document, or the orderBy values of it */
  startAfter(...cursor: unknown[]): Query;
  /** projection: data() holds only these fields (none = existence only, data() is {}) */
  select(...fields: string[]): Query;
  get(): Promise<QuerySnap>;
}
export interface CollectionRef extends Query {
  readonly id: string;
  /** slash path of the collection, e.g. deployments/D1/commits */
  readonly path: string;
  /** the document this collection hangs under; null for a root collection */
  readonly parent: DocRef | null;
  /** no id → a new random 20-character id */
  doc(id?: string): DocRef;
}
export interface DocRef {
  readonly id: string;
  readonly path: string;
  readonly parent: CollectionRef;
  collection(id: string): CollectionRef;
  get(): Promise<DocSnap>;
  set(data: DocData, opts?: SetOptions): Promise<unknown>;
  create(data: DocData): Promise<unknown>;
  update(data: DocData): Promise<unknown>;
  delete(): Promise<unknown>;
}
export interface SetOptions {
  merge?: boolean;
}

export interface Tx {
  get(ref: DocRef): Promise<DocSnap>;
  get(q: Query): Promise<QuerySnap>;
  getAll(...refs: DocRef[]): Promise<DocSnap[]>;
  set(ref: DocRef, data: DocData, opts?: SetOptions): Tx;
  create(ref: DocRef, data: DocData): Tx;
  update(ref: DocRef, data: DocData): Tx;
  delete(ref: DocRef): Tx;
}
export interface WriteBatch {
  set(ref: DocRef, data: DocData, opts?: SetOptions): WriteBatch;
  create(ref: DocRef, data: DocData): WriteBatch;
  update(ref: DocRef, data: DocData): WriteBatch;
  delete(ref: DocRef): WriteBatch;
  commit(): Promise<unknown>;
}
/** non-atomic parallel writes (activities ingestion); each write settles on its own */
export interface BulkWriter {
  create(ref: DocRef, data: DocData): Promise<unknown>;
  set(ref: DocRef, data: DocData, opts?: SetOptions): Promise<unknown>;
  update(ref: DocRef, data: DocData): Promise<unknown>;
  delete(ref: DocRef): Promise<unknown>;
  /** return true to retry the failed write */
  onWriteError(cb: (e: StoreError & { failedAttempts: number }) => boolean): void;
  close(): Promise<void>;
}

export interface DocStore {
  collection(path: string): CollectionRef;
  doc(path: string): DocRef;
  collectionGroup(collectionId: string): Query;
  getAll(...refs: DocRef[]): Promise<DocSnap[]>;
  runTransaction<T>(fn: (tx: Tx) => Promise<T>, opts?: { maxAttempts?: number }): Promise<T>;
  batch(): WriteBatch;
  bulkWriter(): BulkWriter;
  /** flush + close handles */
  close?(): Promise<void>;
}

// ───────────────────────────────────────── values

// brands: `instanceof` must hold across duplicate module instances (tsx loads a file as ESM and CJS in tests/scripts)
const TS_BRAND = Symbol.for('opm.Timestamp');
const FT_BRAND = Symbol.for('opm.FieldTransform');

/** Point in time with millisecond precision used by the TTL fields (agentNonces.expiresAt, rateLimits.expireAt). */
export class Timestamp {
  readonly [TS_BRAND] = true;
  static [Symbol.hasInstance](x: unknown): boolean {
    return !!x && typeof x === 'object' && (x as Record<symbol, unknown>)[TS_BRAND] === true;
  }
  constructor(
    readonly seconds: number,
    readonly nanoseconds: number,
  ) {}
  static fromMillis(ms: number): Timestamp {
    const s = Math.floor(ms / 1000);
    return new Timestamp(s, Math.round((ms - s * 1000) * 1e6));
  }
  static fromDate(d: Date): Timestamp {
    return Timestamp.fromMillis(d.getTime());
  }
  static now(): Timestamp {
    return Timestamp.fromMillis(Date.now());
  }
  toMillis(): number {
    return this.seconds * 1000 + Math.floor(this.nanoseconds / 1e6);
  }
  toDate(): Date {
    return new Date(this.toMillis());
  }
  isEqual(o: Timestamp): boolean {
    return o instanceof Timestamp && o.seconds === this.seconds && o.nanoseconds === this.nanoseconds;
  }
  toJSON() {
    return { _seconds: this.seconds, _nanoseconds: this.nanoseconds };
  }
}

/** Write-time transforms. Adapters translate them (Firestore FieldValue / applied in the local write path). */
export class FieldTransform {
  readonly [FT_BRAND] = true;
  static [Symbol.hasInstance](x: unknown): boolean {
    return !!x && typeof x === 'object' && (x as Record<symbol, unknown>)[FT_BRAND] === true;
  }
  private constructor(
    readonly op: 'increment' | 'arrayUnion' | 'arrayRemove' | 'delete' | 'serverTimestamp',
    readonly operand: unknown,
  ) {}
  static increment(n: number) {
    return new FieldTransform('increment', n);
  }
  static arrayUnion(...v: unknown[]) {
    return new FieldTransform('arrayUnion', v);
  }
  static arrayRemove(...v: unknown[]) {
    return new FieldTransform('arrayRemove', v);
  }
  static delete() {
    return new FieldTransform('delete', null);
  }
  static serverTimestamp() {
    return new FieldTransform('serverTimestamp', null);
  }
}
/** `FieldValue.increment(1)` etc. — same spelling as before the port existed */
export const FieldValue = {
  increment: (n: number) => FieldTransform.increment(n),
  arrayUnion: (...v: unknown[]) => FieldTransform.arrayUnion(...v),
  arrayRemove: (...v: unknown[]) => FieldTransform.arrayRemove(...v),
  delete: () => FieldTransform.delete(),
  serverTimestamp: () => FieldTransform.serverTimestamp(),
};

// ───────────────────────────────────────── errors

/** gRPC-style codes (numeric codes kept for callers that compare `e.code === 6`). */
export type StoreErrorCode = 'already-exists' | 'not-found' | 'aborted' | 'failed-precondition' | 'invalid-argument' | 'unavailable';
const NUM: Record<StoreErrorCode, number> = { 'already-exists': 6, 'not-found': 5, aborted: 10, 'failed-precondition': 9, 'invalid-argument': 3, unavailable: 14 };
export class StoreError extends Error {
  readonly code: number;
  readonly status: StoreErrorCode;
  constructor(status: StoreErrorCode, message: string) {
    super(`${NUM[status]} ${status.toUpperCase().replace(/-/g, '_')}: ${message}`);
    this.status = status;
    this.code = NUM[status];
  }
}
export function storeErrorCode(e: unknown): StoreErrorCode | null {
  const c = (e as { code?: unknown; status?: unknown })?.code;
  const s = (e as { status?: unknown })?.status;
  for (const [k, n] of Object.entries(NUM)) if (c === n || c === k || s === k || c === k.toUpperCase().replace(/-/g, '_')) return k as StoreErrorCode;
  return null;
}
