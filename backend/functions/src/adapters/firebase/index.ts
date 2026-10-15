/** The Firebase adapter set (production default for your-firebase-project). */
import { initializeApp, getApps } from 'firebase-admin/app';
import { getFirestore, type Firestore } from 'firebase-admin/firestore';
import { getStorage } from 'firebase-admin/storage';
import { getAuth, type Auth } from 'firebase-admin/auth';
import type { Ports } from '../../ports/index.js';
import { FirebaseDocStore, type FirestoreSdk } from './docstore.js';
import { FirebaseBlobStore, type Bucket } from './blobstore.js';
import { FirebaseAuthAdmin } from './auth.js';

export { FirebaseDocStore, FirebaseBlobStore, FirebaseAuthAdmin, type Bucket };

function app() {
  if (!getApps().length) initializeApp();
  return getApps()[0]!;
}
const isEmulator = () => process.env.FUNCTIONS_EMULATOR === 'true' || !!process.env.FIRESTORE_EMULATOR_HOST;

/**
 * Default app clients (Cloud Functions runtime / emulator / ADC). Scripts that build their own clients (gcloud-token
 * mode, scripts/_admin.ts) pass them in.
 */
export function firebasePorts(clients: { db?: Firestore; bucket?: Bucket; auth?: Auth; sdk?: FirestoreSdk } = {}): Ports {
  let db = clients.db;
  if (!db) {
    db = getFirestore(app());
    db.settings({ ignoreUndefinedProperties: true });
  }
  const bucketOf = () => clients.bucket ?? getStorage(app()).bucket(process.env.OPM_BUCKET || undefined);
  const authOf = () => clients.auth ?? getAuth(app());
  return {
    store: new FirebaseDocStore(db, clients.sdk),
    // GCS compose is not supported by the Storage emulator (uploads concatenate there, as before)
    blobs: new FirebaseBlobStore(bucketOf, { canCompose: !isEmulator() }),
    auth: new FirebaseAuthAdmin(authOf),
  };
}
