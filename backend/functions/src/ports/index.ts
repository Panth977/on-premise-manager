/**
 * The adapter registry (SPEC §21 #108). Business code calls store() / blobs() / authAdmin(); an entry point installs
 * the adapter set first (functions/src/server.ts, scripts/_admin.ts, test/helpers.ts).
 */
import type { DocStore } from './docstore.js';
import type { BlobStore } from './blobstore.js';
import type { AuthAdmin } from './auth.js';

export interface Ports {
  store: DocStore;
  blobs: BlobStore;
  auth: AuthAdmin;
}
// kept on globalThis: a module loaded twice (tsx ESM + CJS views of the same file in tests/scripts) shares ONE set
const KEY = Symbol.for('opm.ports');
type G = typeof globalThis & { [KEY]?: Ports | null };
export function setPorts(p: Ports) {
  (globalThis as G)[KEY] = p;
}
export function hasPorts() {
  return !!(globalThis as G)[KEY];
}
function need(): Ports {
  const p = (globalThis as G)[KEY];
  if (!p) throw new Error('no storage adapter installed (call setPorts first)');
  return p;
}
export const store = (): DocStore => need().store;
export const blobs = (): BlobStore => need().blobs;
export const authAdmin = (): AuthAdmin => need().auth;

// explicit re-exports (tsx loads functions/ sources as CJS: `export *` is invisible to ESM named imports there)
export { Timestamp, FieldTransform, FieldValue, StoreError, storeErrorCode } from './docstore.js';
export type { DocData, WhereOp, OrderDir, DocSnap, QueryDocSnap, QuerySnap, Query, CollectionRef, DocRef, SetOptions, Tx, WriteBatch, BulkWriter, DocStore, StoreErrorCode } from './docstore.js';
export type { BlobMeta, PutOptions, ListedBlob, LifecycleRule, BlobStore } from './blobstore.js';
export { AuthError } from './auth.js';
export type { AuthUser, DecodedIdToken, CreateUserInput, UpdateUserInput, AuthAdmin } from './auth.js';
export { matchPattern, nextRun } from './events.js';
export type { DocEvent, DocTrigger, ScheduledJob } from './events.js';
export { logger } from './log.js';
