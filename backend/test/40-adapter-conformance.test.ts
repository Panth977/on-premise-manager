/**
 * §21 #108 adapter conformance: the same DocStore / BlobStore suite against the local adapter (always, fresh temp data
 * dir, strict index registry) and the Firebase adapter (when FIRESTORE_EMULATOR_HOST is set: inside
 * `firebase emulators:exec`).
 */
import { mkdtempSync, rmSync, mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { conformance, CONFORMANCE_INDEXES } from './conformance.ts';

const TMP = join(import.meta.dirname, '.tmp');

conformance('local', async () => {
  mkdirSync(TMP, { recursive: true });
  const dir = mkdtempSync(join(TMP, 'conf-'));
  const { localPorts } = await import('../functions/src/adapters/local/index.js');
  const p = localPorts({ dataDir: dir, publicUrl: 'http://127.0.0.1:1', strict: true, indexes: CONFORMANCE_INDEXES });
  return {
    store: p.store,
    blobs: p.blobs,
    done: async () => {
      await p.close();
      rmSync(dir, { recursive: true, force: true });
    },
  };
});

if (process.env.FIRESTORE_EMULATOR_HOST) {
  conformance('firebase (emulator)', async () => {
    const project = process.env.GCLOUD_PROJECT || 'demo-opm';
    process.env.FIREBASE_STORAGE_EMULATOR_HOST ??= '127.0.0.1:9199';
    const { initializeApp, getApps } = await import('firebase-admin/app');
    const { getFirestore, Timestamp, FieldValue } = await import('firebase-admin/firestore');
    const { getStorage } = await import('firebase-admin/storage');
    const { getAuth } = await import('firebase-admin/auth');
    const { firebasePorts } = await import('../functions/src/adapters/firebase/index.js');
    const bucket = `${project}.appspot.com`;
    const app = getApps().find((a) => a.name === 'conformance') ?? initializeApp({ projectId: project, storageBucket: bucket }, 'conformance');
    const fs = getFirestore(app);
    try {
      fs.settings({ ignoreUndefinedProperties: true });
    } catch {
      /* already set */
    }
    const p = firebasePorts({ db: fs, bucket: getStorage(app).bucket(bucket), auth: getAuth(app), sdk: { Timestamp, FieldValue } as never });
    return { store: p.store, blobs: p.blobs, done: async () => undefined };
  });
}
