/**
 * Firebase DocStore adapter: a thin wrapper over the Firestore Admin SDK. The only translation is of the two port
 * value types — Timestamp and FieldTransform — so every document keeps exactly the shape it had before §21 #108.
 */
import {
  FieldValue as FsFieldValue,
  Timestamp as FsTimestamp,
  type BulkWriter as FsBulkWriter,
  type CollectionReference,
  type DocumentReference,
  type DocumentSnapshot,
  type Firestore,
  type Query as FsQuery,
  type QuerySnapshot,
  type Transaction,
  type WriteBatch as FsWriteBatch,
} from 'firebase-admin/firestore';
import {
  FieldTransform,
  Timestamp,
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
} from '../../ports/docstore.js';

// ───────────────────────────────────────── value translation
/** The Timestamp / FieldValue classes of the SDK copy that built the Firestore instance (a test or a gcloud-token script
 *  may load another copy of @google-cloud/firestore than this module; Firestore rejects foreign instances). */
export interface FirestoreSdk {
  Timestamp: { new (seconds: number, nanoseconds: number): unknown };
  FieldValue: { increment(n: number): unknown; arrayUnion(...v: unknown[]): unknown; arrayRemove(...v: unknown[]): unknown; delete(): unknown; serverTimestamp(): unknown };
}
let SDK: FirestoreSdk = { Timestamp: FsTimestamp, FieldValue: FsFieldValue as unknown as FirestoreSdk['FieldValue'] };
function toFs(v: unknown): unknown {
  if (v instanceof Timestamp) return new SDK.Timestamp(v.seconds, v.nanoseconds);
  if (v instanceof FieldTransform) {
    switch (v.op) {
      case 'increment':
        return SDK.FieldValue.increment(v.operand as number);
      case 'arrayUnion':
        return SDK.FieldValue.arrayUnion(...((v.operand as unknown[]).map(toFs)));
      case 'arrayRemove':
        return SDK.FieldValue.arrayRemove(...((v.operand as unknown[]).map(toFs)));
      case 'delete':
        return SDK.FieldValue.delete();
      case 'serverTimestamp':
        return SDK.FieldValue.serverTimestamp();
    }
  }
  if (Array.isArray(v)) return v.map(toFs);
  if (v && typeof v === 'object' && !Buffer.isBuffer(v) && !(v instanceof Uint8Array) && Object.getPrototypeOf(v) === Object.prototype) {
    const o: Record<string, unknown> = {};
    for (const [k, x] of Object.entries(v)) o[k] = toFs(x);
    return o;
  }
  return v;
}
/** a Firestore Timestamp of ANY @google-cloud/firestore copy (scripts in gcloud-token mode build their own client) */
function isFsTimestamp(v: unknown): v is FsTimestamp {
  if (v instanceof FsTimestamp) return true;
  const o = v as { seconds?: unknown; nanoseconds?: unknown; toMillis?: unknown; constructor?: { name?: string } } | null;
  return !!o && typeof o === 'object' && !(v instanceof Timestamp) && typeof o.seconds === 'number' && typeof o.nanoseconds === 'number' && typeof o.toMillis === 'function' && o.constructor?.name === 'Timestamp';
}
function fromFs(v: unknown): unknown {
  if (isFsTimestamp(v)) return new Timestamp(v.seconds, v.nanoseconds);
  if (Array.isArray(v)) return v.map(fromFs);
  if (v && typeof v === 'object' && !Buffer.isBuffer(v) && !(v instanceof Uint8Array) && Object.getPrototypeOf(v) === Object.prototype) {
    let changed = false;
    const o: Record<string, unknown> = {};
    for (const [k, x] of Object.entries(v)) {
      const y = fromFs(x);
      if (y !== x) changed = true;
      o[k] = y;
    }
    return changed ? o : v;
  }
  return v;
}
const dataToFs = (d: DocData) => toFs(d) as Record<string, unknown>;
function cursorToFs(c: unknown[]): unknown[] {
  return c.map((x) => (x instanceof FbSnap ? x.raw : toFs(x)));
}

// ───────────────────────────────────────── wrappers
class FbSnap implements QueryDocSnap {
  constructor(
    readonly raw: DocumentSnapshot,
    private readonly fs: Firestore,
  ) {}
  get id() {
    return this.raw.id;
  }
  get ref(): DocRef {
    return new FbDocRef(this.raw.ref, this.fs);
  }
  get exists() {
    return this.raw.exists;
  }
  data(): DocData {
    return fromFs(this.raw.data()) as DocData;
  }
  get(field: string): unknown {
    return fromFs(this.raw.get(field));
  }
}
function qsnap(s: QuerySnapshot, fs: Firestore): QuerySnap {
  const docs = s.docs.map((d) => new FbSnap(d, fs));
  return { docs, empty: docs.length === 0, size: docs.length, forEach: (cb) => docs.forEach(cb) };
}

class FbQuery implements Query {
  constructor(
    readonly raw: FsQuery,
    protected readonly fs: Firestore,
  ) {}
  where(field: string, op: WhereOp, value: unknown): Query {
    return new FbQuery(this.raw.where(field, op, toFs(value)), this.fs);
  }
  orderBy(field: string, dir: OrderDir = 'asc'): Query {
    return new FbQuery(this.raw.orderBy(field, dir), this.fs);
  }
  limit(n: number): Query {
    return new FbQuery(this.raw.limit(n), this.fs);
  }
  startAfter(...cursor: unknown[]): Query {
    return new FbQuery(this.raw.startAfter(...cursorToFs(cursor)), this.fs);
  }
  select(...fields: string[]): Query {
    return new FbQuery(this.raw.select(...fields), this.fs);
  }
  async get(): Promise<QuerySnap> {
    return qsnap(await this.raw.get(), this.fs);
  }
}
class FbCollectionRef extends FbQuery implements CollectionRef {
  constructor(
    readonly rawCol: CollectionReference,
    fs: Firestore,
  ) {
    super(rawCol, fs);
  }
  get id() {
    return this.rawCol.id;
  }
  get path() {
    return this.rawCol.path;
  }
  get parent(): DocRef | null {
    const p = this.rawCol.parent;
    return p ? new FbDocRef(p, this.fs) : null;
  }
  doc(id?: string): DocRef {
    return new FbDocRef(id === undefined ? this.rawCol.doc() : this.rawCol.doc(id), this.fs);
  }
}
class FbDocRef implements DocRef {
  constructor(
    readonly raw: DocumentReference,
    private readonly fs: Firestore,
  ) {}
  get id() {
    return this.raw.id;
  }
  get path() {
    return this.raw.path;
  }
  get parent(): CollectionRef {
    return new FbCollectionRef(this.raw.parent, this.fs);
  }
  collection(id: string): CollectionRef {
    return new FbCollectionRef(this.raw.collection(id), this.fs);
  }
  async get(): Promise<DocSnap> {
    return new FbSnap(await this.raw.get(), this.fs);
  }
  set(data: DocData, opts?: SetOptions) {
    return opts?.merge ? this.raw.set(dataToFs(data), { merge: true }) : this.raw.set(dataToFs(data));
  }
  create(data: DocData) {
    return this.raw.create(dataToFs(data));
  }
  update(data: DocData) {
    return this.raw.update(dataToFs(data));
  }
  delete() {
    return this.raw.delete();
  }
}
const rawRef = (r: DocRef) => (r as FbDocRef).raw;

class FbTx implements Tx {
  constructor(
    private readonly t: Transaction,
    private readonly fs: Firestore,
  ) {}
  get(ref: DocRef): Promise<DocSnap>;
  get(q: Query): Promise<QuerySnap>;
  async get(x: DocRef | Query): Promise<DocSnap | QuerySnap> {
    if (x instanceof FbDocRef) return new FbSnap(await this.t.get(x.raw), this.fs);
    return qsnap(await this.t.get((x as FbQuery).raw), this.fs);
  }
  async getAll(...refs: DocRef[]): Promise<DocSnap[]> {
    if (!refs.length) return [];
    return (await this.t.getAll(...refs.map(rawRef))).map((s) => new FbSnap(s, this.fs));
  }
  set(ref: DocRef, data: DocData, opts?: SetOptions): Tx {
    if (opts?.merge) this.t.set(rawRef(ref), dataToFs(data), { merge: true });
    else this.t.set(rawRef(ref), dataToFs(data));
    return this;
  }
  create(ref: DocRef, data: DocData): Tx {
    this.t.create(rawRef(ref), dataToFs(data));
    return this;
  }
  update(ref: DocRef, data: DocData): Tx {
    this.t.update(rawRef(ref), dataToFs(data));
    return this;
  }
  delete(ref: DocRef): Tx {
    this.t.delete(rawRef(ref));
    return this;
  }
}
class FbBatch implements WriteBatch {
  constructor(private readonly b: FsWriteBatch) {}
  set(ref: DocRef, data: DocData, opts?: SetOptions): WriteBatch {
    if (opts?.merge) this.b.set(rawRef(ref), dataToFs(data), { merge: true });
    else this.b.set(rawRef(ref), dataToFs(data));
    return this;
  }
  create(ref: DocRef, data: DocData): WriteBatch {
    this.b.create(rawRef(ref), dataToFs(data));
    return this;
  }
  update(ref: DocRef, data: DocData): WriteBatch {
    this.b.update(rawRef(ref), dataToFs(data));
    return this;
  }
  delete(ref: DocRef): WriteBatch {
    this.b.delete(rawRef(ref));
    return this;
  }
  commit() {
    return this.b.commit();
  }
}
class FbBulk implements BulkWriter {
  constructor(private readonly w: FsBulkWriter) {}
  create(ref: DocRef, data: DocData) {
    return this.w.create(rawRef(ref), dataToFs(data));
  }
  set(ref: DocRef, data: DocData, opts?: SetOptions) {
    return opts?.merge ? this.w.set(rawRef(ref), dataToFs(data), { merge: true }) : this.w.set(rawRef(ref), dataToFs(data));
  }
  update(ref: DocRef, data: DocData) {
    return this.w.update(rawRef(ref), dataToFs(data));
  }
  delete(ref: DocRef) {
    return this.w.delete(rawRef(ref));
  }
  onWriteError(cb: Parameters<BulkWriter['onWriteError']>[0]) {
    this.w.onWriteError((e) => cb(e as never));
  }
  close() {
    return this.w.close();
  }
}

export class FirebaseDocStore implements DocStore {
  readonly kind = 'firebase' as const;
  constructor(
    readonly fs: Firestore,
    sdk?: FirestoreSdk,
  ) {
    if (sdk) SDK = sdk;
  }
  collection(path: string): CollectionRef {
    return new FbCollectionRef(this.fs.collection(path), this.fs);
  }
  doc(path: string): DocRef {
    return new FbDocRef(this.fs.doc(path), this.fs);
  }
  collectionGroup(id: string): Query {
    return new FbQuery(this.fs.collectionGroup(id), this.fs);
  }
  async getAll(...refs: DocRef[]): Promise<DocSnap[]> {
    if (!refs.length) return [];
    return (await this.fs.getAll(...refs.map(rawRef))).map((s) => new FbSnap(s, this.fs));
  }
  runTransaction<T>(fn: (tx: Tx) => Promise<T>, opts?: { maxAttempts?: number }): Promise<T> {
    return this.fs.runTransaction((t) => fn(new FbTx(t, this.fs)), opts?.maxAttempts ? { maxAttempts: opts.maxAttempts } : undefined);
  }
  batch(): WriteBatch {
    return new FbBatch(this.fs.batch());
  }
  bulkWriter(): BulkWriter {
    return new FbBulk(this.fs.bulkWriter());
  }
}
