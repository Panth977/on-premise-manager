/**
 * Data access entry points of the business code. Since §21 #108 these delegate to the installed adapter set
 * (ports/index.ts): Firebase (Firestore / Cloud Storage / Firebase Auth) or local (SQLite / filesystem / built-in auth).
 * Nothing in core/ or routes/ imports firebase-admin any more.
 */
import { store, authAdmin, storeErrorCode, type DocStore, type Tx as PortTx, type DocRef } from '../driver/index.js';

/** the document store (name kept from the Firestore era: db().collection(…), db().runTransaction(…)) */
export function db(): DocStore {
  return store();
}
export function auth() {
  return authAdmin();
}
export type Tx = PortTx;
export type Ref = DocRef;

export const nowIso = () => new Date().toISOString();
export const isoPlus = (ms: number, from = Date.now()) => new Date(from + ms).toISOString();

/** Remove internal fields (prefixed "_") before a document leaves the API. */
export function pub<T>(doc: Record<string, unknown> | undefined | null): T {
  const out: Record<string, unknown> = {};
  if (!doc) return out as T;
  for (const [k, v] of Object.entries(doc)) if (!k.startsWith('_')) out[k] = v;
  return out as T;
}

export const col = {
  registries: () => db().collection('registries'),
  services: () => db().collection('services'),
  images: (serviceSlug: string) => db().collection('services').doc(serviceSlug).collection('images'),
  notFoundPages: () => db().collection('notFoundPages'),
  tags: () => db().collection('tags'),
  icons: () => db().collection('icons'),
  accounts: () => db().collection('accounts'),
  auditLog: () => db().collection('auditLog'),
  templates: () => db().collection('templates'),
  deployments: () => db().collection('deployments'),
  revisions: (d: string) => db().collection('deployments').doc(d).collection('revisions'),
  proposals: (d: string) => db().collection('deployments').doc(d).collection('proposals'),
  agents: (d: string) => db().collection('deployments').doc(d).collection('agents'),
  installTokens: (d: string) => db().collection('deployments').doc(d).collection('installTokens'),
  activities: (d: string) => db().collection('deployments').doc(d).collection('activities'),
  transfers: (d: string) => db().collection('deployments').doc(d).collection('transfers'),
  /** §21 #123 shared files (core/files.ts) */
  files: (d: string) => db().collection('deployments').doc(d).collection('files'),
  uptime: (d: string) => db().collection('deployments').doc(d).collection('uptime'),
  revokedKeys: (d: string) => db().collection('deployments').doc(d).collection('revokedKeys'),
  uploads: () => db().collection('uploads'),
  agentNonces: () => db().collection('agentNonces'),
};

/** "already exists" from any adapter (Firestore code 6 / local StoreError). */
export function isAlreadyExists(e: unknown): boolean {
  return storeErrorCode(e) === 'already-exists';
}
