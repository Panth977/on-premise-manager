/**
 * Local DocStore (SPEC §21 #108): SQLite via node:sqlite — one global file + one file per deployment (files.ts),
 * Firestore semantics per ports/docstore.ts. See README.md for the transaction model, the cross-file commit order and
 * the outbox.
 *
 * Query notes: typed comparisons (a string filter never matches numbers / booleans / timestamps); documents whose
 * orderBy field is null or MISSING are included (sorted first ascending, last descending) — Firestore leaves out
 * documents missing an orderBy field; OPM never relies on that. Mixed-type fields order NULL < numbers (booleans 0|1,
 * timestamps epoch ms) < strings. Document paths compare as UTF-8 strings.
 */
import { randomInt } from 'node:crypto';
import { EventEmitter } from 'node:events';
import {
  StoreError,
  Timestamp,
  logger,
  matchPattern,
  type BulkWriter,
  type CollectionRef,
  type DocData,
  type DocRef,
  type DocSnap,
  type DocStore,
  type OrderDir,
  type Query,
  type QueryDocSnap,
  type QuerySnap,
  type SetOptions,
  type Tx,
  type WhereOp,
  type WriteBatch,
} from './index.js';
import { applyMerge, applySet, applyUpdate, checkField, decodeDoc, encodeDoc, getField, ixOf, kindOf, project, type ValueKind } from './codec.js';
import { Files, GLOBAL, Handle, isBusy, ixSql, jsonPath, locateCollection, locateDoc, q, tableCollectionId, type ColLoc } from './files.js';
import { ixColumn, type IndexRegistry } from './registry.js';

export interface OutboxTrigger {
  name: string;
  kind: 'created' | 'written';
  pattern: string;
}

const ID_CHARS = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
export const autoId = (n = 20) => Array.from({ length: n }, () => ID_CHARS[randomInt(ID_CHARS.length)]).join('');
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

// ───────────────────────────────────────── snapshots
class LocalSnap implements QueryDocSnap {
  constructor(
    private readonly store: LocalDocStore,
    readonly path: string,
    private readonly raw: string | null,
    private readonly fields: string[] | null = null,
  ) {}
  get id() {
    return this.path.slice(this.path.lastIndexOf('/') + 1);
  }
  get ref(): DocRef {
    return this.store.doc(this.path);
  }
  get exists() {
    return this.raw !== null;
  }
  data(): DocData {
    if (this.raw === null) return undefined as unknown as DocData;
    const d = decodeDoc(this.raw);
    return this.fields ? project(d, this.fields) : d;
  }
  get(field: string): unknown {
    return this.raw === null ? undefined : getField(this.data(), field);
  }
  /** full document (cursor values) */
  full(): DocData | null {
    return this.raw === null ? null : decodeDoc(this.raw);
  }
}
function qsnap(docs: LocalSnap[]): QuerySnap {
  return { docs, empty: docs.length === 0, size: docs.length, forEach: (cb) => docs.forEach(cb) };
}

// ───────────────────────────────────────── queries
interface Filter {
  field: string;
  op: WhereOp;
  value: unknown;
}
interface Order {
  field: string;
  dir: OrderDir;
}
type Target = { kind: 'col'; loc: ColLoc } | { kind: 'deployments' } | { kind: 'group'; id: string };
interface QSpec {
  target: Target;
  filters: Filter[];
  orders: Order[];
  limit: number | null;
  cursor: unknown[] | null;
  select: string[] | null;
}
const NAME = '__name__';
const RANGE = new Set<WhereOp>(['<', '<=', '>', '>=']);
interface Row {
  path: string;
  raw: string;
  keys: (string | number | null)[];
}

class LocalQuery implements Query {
  constructor(
    protected readonly store: LocalDocStore,
    readonly spec: QSpec,
  ) {}
  private with(p: Partial<QSpec>): Query {
    return new LocalQuery(this.store, { ...this.spec, ...p });
  }
  where(field: string, op: WhereOp, value: unknown): Query {
    checkField(field);
    if (!['==', 'in', 'array-contains', '<', '<=', '>', '>='].includes(op)) throw new StoreError('invalid-argument', `unsupported operator ${op}`);
    if (op === 'in' && (!Array.isArray(value) || !value.length || value.length > 30)) throw new StoreError('invalid-argument', "'in' needs a non-empty array of at most 30 values");
    if (op === 'in') (value as unknown[]).forEach(kindOf);
    else kindOf(value);
    return this.with({ filters: [...this.spec.filters, { field, op, value }] });
  }
  orderBy(field: string, dir: OrderDir = 'asc'): Query {
    checkField(field);
    return this.with({ orders: [...this.spec.orders, { field, dir }] });
  }
  limit(n: number): Query {
    return this.with({ limit: n });
  }
  startAfter(...cursor: unknown[]): Query {
    return this.with({ cursor });
  }
  select(...fields: string[]): Query {
    fields.forEach(checkField);
    return this.with({ select: fields });
  }
  async get(): Promise<QuerySnap> {
    return this.store.runQuerySnap(this.spec);
  }
}
class LocalCollection extends LocalQuery implements CollectionRef {
  constructor(
    store: LocalDocStore,
    readonly path: string,
    loc: ColLoc | null,
  ) {
    super(store, { target: loc ? { kind: 'col', loc } : { kind: 'deployments' }, filters: [], orders: [], limit: null, cursor: null, select: null });
  }
  get id() {
    return this.path.slice(this.path.lastIndexOf('/') + 1);
  }
  get parent(): DocRef | null {
    const i = this.path.lastIndexOf('/');
    return i < 0 ? null : this.store.doc(this.path.slice(0, i));
  }
  doc(id?: string): DocRef {
    return this.store.doc(`${this.path}/${id === undefined ? autoId() : id}`);
  }
}

// ───────────────────────────────────────── writes
type WriteKind = 'set' | 'merge' | 'create' | 'update' | 'delete';
interface WriteOp {
  kind: WriteKind;
  path: string;
  data?: DocData;
}
interface ReadRec {
  keys: string[];
  /** true when what was read is unchanged (runs under the write locks) */
  check(): boolean;
}

class LocalDocRef implements DocRef {
  constructor(
    private readonly store: LocalDocStore,
    readonly path: string,
  ) {
    locateDoc(path);
  }
  get id() {
    return this.path.slice(this.path.lastIndexOf('/') + 1);
  }
  get parent(): CollectionRef {
    return this.store.collection(this.path.slice(0, this.path.lastIndexOf('/')));
  }
  collection(id: string): CollectionRef {
    return this.store.collection(`${this.path}/${id}`);
  }
  async get(): Promise<DocSnap> {
    return new LocalSnap(this.store, this.path, this.store.readRaw(this.path));
  }
  set(data: DocData, opts?: SetOptions) {
    return this.store.write([{ kind: opts?.merge ? 'merge' : 'set', path: this.path, data }]);
  }
  create(data: DocData) {
    return this.store.write([{ kind: 'create', path: this.path, data }]);
  }
  update(data: DocData) {
    return this.store.write([{ kind: 'update', path: this.path, data }]);
  }
  delete() {
    return this.store.write([{ kind: 'delete', path: this.path }]);
  }
}
const pathOf = (r: DocRef) => r.path;

class LocalTx implements Tx {
  readonly reads: ReadRec[] = [];
  readonly writes: WriteOp[] = [];
  constructor(private readonly store: LocalDocStore) {}
  private noReadAfterWrite() {
    if (this.writes.length) throw new StoreError('invalid-argument', 'Firestore transactions require all reads to be executed before all writes.');
  }
  get(ref: DocRef): Promise<DocSnap>;
  get(q: Query): Promise<QuerySnap>;
  async get(x: DocRef | Query): Promise<DocSnap | QuerySnap> {
    this.noReadAfterWrite();
    if (x instanceof LocalQuery) {
      const { rows, keys } = this.store.runQuery(x.spec);
      const sig = JSON.stringify(rows.map((r) => [r.path, r.raw]));
      // multi-file queries also lock the global file: a common lock for transactions whose file sets differ
      const multi = x.spec.target.kind !== 'col';
      this.reads.push({ keys: multi ? [...new Set([...keys, GLOBAL])] : keys, check: () => JSON.stringify(this.store.runQuery(x.spec).rows.map((r) => [r.path, r.raw])) === sig });
      return qsnap(rows.map((r) => new LocalSnap(this.store, r.path, r.raw, x.spec.select)));
    }
    return this.readDoc(pathOf(x as DocRef));
  }
  private readDoc(path: string): DocSnap {
    const raw = this.store.readRaw(path);
    const loc = locateDoc(path);
    this.reads.push({ keys: [loc.key], check: () => this.store.readRaw(path) === raw });
    return new LocalSnap(this.store, path, raw);
  }
  async getAll(...refs: DocRef[]): Promise<DocSnap[]> {
    this.noReadAfterWrite();
    return refs.map((r) => this.readDoc(pathOf(r)));
  }
  set(ref: DocRef, data: DocData, opts?: SetOptions): Tx {
    this.writes.push({ kind: opts?.merge ? 'merge' : 'set', path: pathOf(ref), data });
    return this;
  }
  create(ref: DocRef, data: DocData): Tx {
    this.writes.push({ kind: 'create', path: pathOf(ref), data });
    return this;
  }
  update(ref: DocRef, data: DocData): Tx {
    this.writes.push({ kind: 'update', path: pathOf(ref), data });
    return this;
  }
  delete(ref: DocRef): Tx {
    this.writes.push({ kind: 'delete', path: pathOf(ref) });
    return this;
  }
}
class LocalBatch implements WriteBatch {
  private readonly ops: WriteOp[] = [];
  constructor(private readonly store: LocalDocStore) {}
  set(ref: DocRef, data: DocData, opts?: SetOptions): WriteBatch {
    this.ops.push({ kind: opts?.merge ? 'merge' : 'set', path: pathOf(ref), data });
    return this;
  }
  create(ref: DocRef, data: DocData): WriteBatch {
    this.ops.push({ kind: 'create', path: pathOf(ref), data });
    return this;
  }
  update(ref: DocRef, data: DocData): WriteBatch {
    this.ops.push({ kind: 'update', path: pathOf(ref), data });
    return this;
  }
  delete(ref: DocRef): WriteBatch {
    this.ops.push({ kind: 'delete', path: pathOf(ref) });
    return this;
  }
  async commit() {
    if (this.ops.length) await this.store.write(this.ops);
    return [];
  }
}
class LocalBulk implements BulkWriter {
  private readonly pending = new Set<Promise<unknown>>();
  private onErr: ((e: StoreError & { failedAttempts: number }) => boolean) | null = null;
  constructor(private readonly store: LocalDocStore) {}
  private run(op: WriteOp): Promise<unknown> {
    const p = (async () => {
      for (let attempt = 1; ; attempt++) {
        try {
          return await this.store.write([op]);
        } catch (e) {
          const err = Object.assign(e as StoreError, { failedAttempts: attempt });
          const retry = this.onErr ? this.onErr(err) : (err.status === 'aborted' || err.status === 'unavailable') && attempt < 10;
          if (!retry) throw e;
          await sleep(Math.min(1000, 10 * 2 ** attempt));
        }
      }
    })();
    this.pending.add(p);
    const done = () => this.pending.delete(p);
    p.then(done, done);
    return p;
  }
  create(ref: DocRef, data: DocData) {
    return this.run({ kind: 'create', path: pathOf(ref), data });
  }
  set(ref: DocRef, data: DocData, opts?: SetOptions) {
    return this.run({ kind: opts?.merge ? 'merge' : 'set', path: pathOf(ref), data });
  }
  update(ref: DocRef, data: DocData) {
    return this.run({ kind: 'update', path: pathOf(ref), data });
  }
  delete(ref: DocRef) {
    return this.run({ kind: 'delete', path: pathOf(ref) });
  }
  onWriteError(cb: (e: StoreError & { failedAttempts: number }) => boolean) {
    this.onErr = cb;
  }
  async close() {
    while (this.pending.size) await Promise.allSettled([...this.pending]);
  }
}

// ───────────────────────────────────────── SQL building
function sqlCmp(a: string | number | null, b: string | number | null): number {
  const rank = (x: unknown) => (x === null ? 0 : typeof x === 'number' ? 1 : 2);
  const ra = rank(a);
  const rb = rank(b);
  if (ra !== rb) return ra - rb;
  if (ra === 0) return 0;
  if (ra === 1) return (a as number) - (b as number);
  return Buffer.compare(Buffer.from(a as string), Buffer.from(b as string));
}
type Bind = string | number | null;

// ───────────────────────────────────────── the store
export interface LocalDocStoreOptions {
  dataDir: string;
  registry: IndexRegistry;
  strict: boolean;
  triggers: OutboxTrigger[];
}
export class LocalDocStore implements DocStore {
  readonly files: Files;
  /** 'outbox' (fileKey) after a commit that wrote outbox rows */
  readonly events = new EventEmitter();
  readonly triggers: OutboxTrigger[];
  private readonly trace = process.env.OPM_LOCAL_TRACE_XTX === '1';
  constructor(o: LocalDocStoreOptions) {
    this.files = new Files(o.dataDir, o.registry, o.strict);
    this.triggers = o.triggers;
    this.events.setMaxListeners(100);
  }

  collection(path: string): CollectionRef {
    return new LocalCollection(this, path, locateCollection(path));
  }
  doc(path: string): DocRef {
    return new LocalDocRef(this, path);
  }
  collectionGroup(collectionId: string): Query {
    if (!/^[A-Za-z][A-Za-z0-9_]*$/.test(collectionId)) throw new StoreError('invalid-argument', `bad collection id ${collectionId}`);
    return new LocalQuery(this, { target: { kind: 'group', id: collectionId }, filters: [], orders: [], limit: null, cursor: null, select: null });
  }
  async getAll(...refs: DocRef[]): Promise<DocSnap[]> {
    return refs.map((r) => new LocalSnap(this, r.path, this.readRaw(r.path)));
  }
  batch(): WriteBatch {
    return new LocalBatch(this);
  }
  bulkWriter(): BulkWriter {
    return new LocalBulk(this);
  }
  async runTransaction<T>(fn: (tx: Tx) => Promise<T>, opts?: { maxAttempts?: number }): Promise<T> {
    const max = opts?.maxAttempts ?? 5;
    for (let attempt = 1; ; attempt++) {
      const tx = new LocalTx(this);
      const out = await fn(tx);
      if (this.commit(tx.reads, tx.writes)) return out;
      if (attempt >= max) throw new StoreError('aborted', `transaction aborted after ${attempt} attempts (contention)`);
      await sleep(randomInt(1, Math.min(250, 5 * 2 ** attempt) + 2));
    }
  }
  async close() {
    this.files.closeAll();
  }

  // ─── reads
  /** committed JSON of a document, null when missing */
  readRaw(path: string): string | null {
    const loc = locateDoc(path);
    const h = this.files.handle(loc.key, false);
    if (!h || !this.files.ensure(h, loc.table, false)) return null;
    const r = h.prep(`SELECT value FROM ${q(loc.table)} WHERE id = ?`).get(path) as { value: string } | undefined;
    return r ? r.value : null;
  }
  runQuerySnap(spec: QSpec): QuerySnap {
    return qsnap(this.runQuery(spec).rows.map((r) => new LocalSnap(this, r.path, r.raw, spec.select)));
  }
  /** rows + the file keys the query looked at */
  runQuery(spec: QSpec): { rows: Row[]; keys: string[] } {
    const targets: { key: string; table: string; parent: string | null }[] = [];
    const t = spec.target;
    if (t.kind === 'col') targets.push({ ...t.loc });
    else if (t.kind === 'deployments') for (const k of this.files.deploymentKeys()) targets.push({ key: k, table: 'deployment', parent: 'deployments' });
    else
      for (const k of this.files.allKeys()) {
        const h = this.files.handle(k, false);
        if (!h) continue;
        for (const tb of this.files.tables(h)) if (tableCollectionId(tb) === t.id) targets.push({ key: k, table: tb, parent: null });
      }
    const orders = this.effectiveOrders(spec);
    const keys = [...new Set(targets.map((x) => x.key))];
    let rows: Row[] = [];
    for (const tg of targets) {
      const h = this.files.handle(tg.key, false);
      if (!h || !this.files.ensure(h, tg.table, false)) continue;
      rows.push(...this.queryTable(h, tg.table, tg.parent, spec, orders));
    }
    if (targets.length > 1) {
      rows.sort((a, b) => {
        for (let i = 0; i < orders.length; i++) {
          const c = sqlCmp(a.keys[i]!, b.keys[i]!);
          if (c) return orders[i]!.dir === 'asc' ? c : -c;
        }
        return 0;
      });
      if (spec.limit !== null) rows = rows.slice(0, spec.limit);
    }
    return { rows, keys };
  }
  private effectiveOrders(spec: QSpec): Order[] {
    const o = [...spec.orders];
    if (!o.length) {
      const r = spec.filters.find((f) => RANGE.has(f.op));
      if (r) o.push({ field: r.field, dir: 'asc' });
    }
    o.push({ field: NAME, dir: o.length ? o[o.length - 1]!.dir : 'asc' });
    return o;
  }
  /** SQL expression of a field in a table: its ix column, else the JSON fallback (throws in strict mode) */
  private expr(table: string, field: string): string {
    if (field === NAME) return 'id';
    if (this.files.scalarFields(table).includes(field)) return q(ixColumn(field));
    if (this.files.strict) throw new StoreError('failed-precondition', `local store (strict): no index column for "${table}.${field}" — add it to adapters/local/registry.ts`);
    return ixSql(field);
  }
  private queryTable(h: Handle, table: string, parent: string | null, spec: QSpec, orders: Order[]): Row[] {
    const where: string[] = [];
    const binds: Bind[] = [];
    if (parent !== null) {
      where.push('parent = ?');
      binds.push(parent);
    }
    const guard = (k: ValueKind, field: string, e: string) => {
      const p = jsonPath(field);
      switch (k) {
        case 'string':
          return `typeof(${e}) = 'text'`;
        case 'number':
          return `json_type(value, '${p}') IN ('integer', 'real')`;
        case 'boolean':
          return `json_type(value, '${p}') IN ('true', 'false')`;
        case 'timestamp':
          return `json_type(value, '${p}') = 'object'`;
        case 'null':
          return `json_type(value, '${p}') = 'null'`;
      }
    };
    for (const f of spec.filters) {
      if (f.op === 'array-contains') {
        if (!this.files.registry[table]?.array?.includes(f.field) && this.files.strict)
          throw new StoreError('failed-precondition', `local store (strict): "${table}.${f.field}" is not registered as an array field — add it to adapters/local/registry.ts`);
        const p = jsonPath(f.field);
        const k = kindOf(f.value);
        const jc =
          k === 'string'
            ? "j.type = 'text' AND j.value = ?"
            : k === 'number'
              ? "j.type IN ('integer', 'real') AND j.value = ?"
              : k === 'boolean'
                ? `j.type = '${f.value ? 'true' : 'false'}'`
                : k === 'null'
                  ? "j.type = 'null'"
                  : `j.type = 'object' AND json_extract(j.value, '$."$timestamp"."seconds"') * 1000 + json_extract(j.value, '$."$timestamp"."nanos"') / 1000000 = ?`;
        where.push(`json_type(value, '${p}') = 'array' AND EXISTS (SELECT 1 FROM json_each(t.value, '${p}') AS j WHERE ${jc})`);
        if (k !== 'boolean' && k !== 'null') binds.push(ixOf(f.value));
        continue;
      }
      const e = this.expr(table, f.field);
      if (f.op === 'in') {
        const byKind = new Map<ValueKind, unknown[]>();
        for (const v of f.value as unknown[]) byKind.set(kindOf(v), [...(byKind.get(kindOf(v)) ?? []), v]);
        const parts: string[] = [];
        for (const [k, vs] of byKind) {
          if (k === 'null') parts.push(guard('null', f.field, e));
          else {
            parts.push(`(${e} IN (${vs.map(() => '?').join(', ')}) AND ${guard(k, f.field, e)})`);
            binds.push(...vs.map(ixOf));
          }
        }
        where.push(`(${parts.join(' OR ')})`);
        continue;
      }
      const k = kindOf(f.value);
      if (k === 'null') {
        // Firestore: == null matches null values; range on null matches nothing useful
        where.push(f.op === '==' ? guard('null', f.field, e) : '0');
        continue;
      }
      where.push(`${e} ${f.op === '==' ? '=' : f.op} ? AND ${guard(k, f.field, e)}`);
      binds.push(ixOf(f.value));
    }
    // cursor
    if (spec.cursor) {
      let vals: (string | number | null)[];
      let n: number;
      const c0 = spec.cursor[0];
      if (spec.cursor.length === 1 && c0 instanceof LocalSnap) {
        const d = c0.full();
        if (!d) throw new StoreError('invalid-argument', 'startAfter: the cursor document does not exist');
        vals = orders.map((o) => (o.field === NAME ? c0.path : ixOf(getField(d, o.field))));
        n = orders.length;
      } else {
        if (spec.cursor.some((x) => x && typeof x === 'object' && 'ref' in (x as object) && !(x instanceof Timestamp))) throw new StoreError('invalid-argument', 'startAfter: foreign snapshot');
        n = spec.cursor.length;
        if (n > orders.length) throw new StoreError('invalid-argument', 'startAfter: more cursor values than orderBy fields');
        vals = spec.cursor.map((v) => {
          kindOf(v);
          return ixOf(v);
        });
      }
      const ors: string[] = [];
      for (let i = 0; i < n; i++) {
        const parts: string[] = [];
        for (let j = 0; j < i; j++) {
          parts.push(`${this.expr(table, orders[j]!.field)} IS ?`);
          binds.push(vals[j]!);
        }
        const e = this.expr(table, orders[i]!.field);
        const v = vals[i]!;
        if (orders[i]!.dir === 'asc') {
          if (v === null) parts.push(`${e} IS NOT NULL`);
          else {
            parts.push(`${e} > ?`);
            binds.push(v);
          }
        } else if (v === null) parts.push('0');
        else {
          parts.push(`(${e} < ? OR ${e} IS NULL)`);
          binds.push(v);
        }
        ors.push(`(${parts.join(' AND ')})`);
      }
      where.push(`(${ors.join(' OR ')})`);
    }
    const sel = orders.map((o, i) => `${this.expr(table, o.field)} AS k${i}`).join(', ');
    let sql = `SELECT id, value, ${sel} FROM ${q(table)} AS t${where.length ? ` WHERE ${where.join(' AND ')}` : ''} ORDER BY ${orders.map((o, i) => `k${i} ${o.dir === 'asc' ? 'ASC' : 'DESC'}`).join(', ')}`;
    if (spec.limit !== null) {
      sql += ' LIMIT ?';
      binds.push(spec.limit);
    }
    const out = h.prep(sql).all(...binds) as Record<string, string | number | null>[];
    return out.map((r) => ({ path: r.id as string, raw: r.value as string, keys: orders.map((_, i) => r[`k${i}`] ?? null) }));
  }

  // ─── writes
  /** non-transactional write(s) (one atomic commit); retried on lock timeouts */
  async write(ops: WriteOp[]): Promise<unknown> {
    for (let attempt = 1; ; attempt++) {
      if (this.commit([], ops)) return undefined;
      if (attempt >= 5) throw new StoreError('unavailable', 'database busy');
      await sleep(randomInt(5, 50 * attempt));
    }
  }
  /**
   * The commit path (synchronous): BEGIN IMMEDIATE on every involved file (deployment files sorted by id, global
   * LAST), re-validate the reads, apply the writes + outbox rows, COMMIT deployment files first and global last.
   * false = a read changed / a lock timed out (the caller re-runs). Write errors (create / update preconditions) throw.
   */
  commit(reads: ReadRec[], writes: WriteOp[]): boolean {
    if (!reads.length && !writes.length) return true;
    const locs = writes.map((w) => locateDoc(w.path));
    const writeKeys = new Set(locs.map((l) => l.key));
    const all = new Set<string>([...writeKeys]);
    for (const r of reads) for (const k of r.keys) all.add(k);
    const keys = [...all].filter((k) => k !== GLOBAL).sort();
    if (all.has(GLOBAL)) keys.push(GLOBAL);
    if (this.trace && keys.length > 1) logger.info('local cross-db tx', { files: keys, paths: writes.map((w) => w.path) });
    this.files.pin(keys);
    const begun: Handle[] = [];
    const rollback = () => {
      for (const h of begun.reverse()) {
        try {
          h.db.exec('ROLLBACK');
        } catch {
          /* not in a transaction */
        }
        // DDL of this transaction (lazy CREATE TABLE / ALTER) is rolled back too
        h.ensured.clear();
        h.inTx = false;
      }
    };
    try {
      for (const k of keys) {
        const h = this.files.handle(k, writeKeys.has(k));
        if (!h) continue;
        try {
          h.db.exec('BEGIN IMMEDIATE');
        } catch (e) {
          if (isBusy(e)) {
            rollback();
            return false;
          }
          throw e;
        }
        h.inTx = true;
        begun.push(h);
      }
      for (const r of reads) {
        if (!r.check()) {
          rollback();
          return false;
        }
      }
      const now = Timestamp.now();
      const before = new Map<string, string | null>();
      const after = new Map<string, string | null>();
      const handles = new Map(begun.map((h) => [h.key, h]));
      for (let i = 0; i < writes.length; i++) {
        const w = writes[i]!;
        const loc = locs[i]!;
        const h = handles.get(loc.key)!;
        this.files.ensure(h, loc.table, true);
        const curRaw = after.has(w.path) ? after.get(w.path)! : this.readIn(h, loc.table, w.path);
        if (!before.has(w.path)) before.set(w.path, curRaw);
        const cur = curRaw === null ? null : decodeDoc(curRaw);
        let next: DocData | null;
        switch (w.kind) {
          case 'set':
            next = applySet(w.data!, now);
            break;
          case 'merge':
            next = applyMerge(cur, w.data!, now);
            break;
          case 'create':
            if (cur) throw new StoreError('already-exists', `Document already exists: ${w.path}`);
            next = applySet(w.data!, now);
            break;
          case 'update':
            if (!cur) throw new StoreError('not-found', `No document to update: ${w.path}`);
            next = applyUpdate(cur, w.data!, now);
            break;
          case 'delete':
            next = null;
            break;
        }
        const t = q(loc.table);
        if (next === null) {
          h.prep(`DELETE FROM ${t} WHERE id = ?`).run(w.path);
          after.set(w.path, null);
        } else {
          const raw = encodeDoc(next);
          const fields = this.files.scalarFields(loc.table);
          const cols = ['id', 'parent', 'value', ...fields.map(ixColumn)].map(q);
          const sql = `INSERT INTO ${t} (${cols.join(', ')}) VALUES (${cols.map(() => '?').join(', ')}) ON CONFLICT(id) DO UPDATE SET ${cols
            .slice(1)
            .map((c) => `${c} = excluded.${c}`)
            .join(', ')}`;
          h.prep(sql).run(w.path, loc.parent, raw, ...fields.map((f) => ixOf(getField(next, f))));
          after.set(w.path, raw);
        }
      }
      // outbox rows: one per changed document per matching trigger, in the same SQLite transaction
      const woke = new Set<string>();
      if (this.triggers.length) {
        const ms = Date.now();
        for (const [path, b] of before) {
          const a = after.get(path) ?? null;
          if (a === b) continue;
          for (const tr of this.triggers) {
            if (tr.kind === 'created' && !(b === null && a !== null)) continue;
            if (!matchPattern(tr.pattern, path)) continue;
            const h = handles.get(locateDoc(path).key)!;
            h.prep('INSERT INTO _outbox (trigger, path, before, after, createdMs) VALUES (?, ?, ?, ?, ?)').run(tr.name, path, b, a, ms);
            woke.add(h.key);
          }
        }
      }
      // deployment files first, global last (keys are in that order)
      for (const h of begun) {
        h.db.exec('COMMIT');
        h.inTx = false;
      }
      begun.length = 0;
      for (const k of woke) this.events.emit('outbox', k);
      return true;
    } catch (e) {
      rollback();
      if (isBusy(e)) return false;
      throw e;
    } finally {
      for (const h of begun) h.inTx = false;
      this.files.unpin(keys);
    }
  }
  private readIn(h: Handle, table: string, path: string): string | null {
    const r = h.prep(`SELECT value FROM ${q(table)} WHERE id = ?`).get(path) as { value: string } | undefined;
    return r ? r.value : null;
  }

  // ─── enumeration (migration tool, backups)
  /** every document of every file, decoded, ordered by path (UTF-8 order) */
  *allDocuments(): Iterable<{ path: string; data: DocData }> {
    // deployment ids are [A-Za-z0-9] and sort above "/", so the deployment files in key order yield paths in order;
    // merge that chain with the global file
    const files = this.files;
    const self = this;
    function* deps() {
      for (const k of files.deploymentKeys()) yield* self.fileDocs(k);
    }
    const g = this.fileDocs(GLOBAL)[Symbol.iterator]();
    const d = deps()[Symbol.iterator]();
    let a = g.next();
    let b = d.next();
    while (!a.done || !b.done) {
      const takeA = !a.done && (b.done || Buffer.compare(Buffer.from(a.value.path), Buffer.from(b.value.path)) < 0);
      const v = takeA ? a.value! : b.value!;
      if (takeA) a = g.next();
      else b = d.next();
      yield { path: v.path, data: decodeDoc(v.raw) };
    }
  }
  private *fileDocs(key: string): Iterable<{ path: string; raw: string }> {
    const h = this.files.handle(key, false);
    if (!h) return;
    const tables = this.files.tables(h);
    if (!tables.length) return;
    this.files.pin([key]);
    const sql = tables.map((t) => `SELECT id, value FROM ${q(t)}`).join(' UNION ALL ') + ' ORDER BY id';
    try {
      // .all(): a lazily iterated StatementSync can be finalized (GC / handle reuse) while the generator is suspended
      const rows = h.db.prepare(sql).all() as { id: string; value: string }[];
      for (const r of rows) yield { path: r.id, raw: r.value };
    } finally {
      this.files.unpin([key]);
    }
  }
  countDocuments(): number {
    let n = 0;
    for (const k of this.files.allKeys()) {
      const h = this.files.handle(k, false);
      if (!h) continue;
      for (const t of this.files.tables(h)) n += (h.prep(`SELECT count(*) AS n FROM ${q(t)}`).get() as { n: number }).n;
    }
    return n;
  }
}
