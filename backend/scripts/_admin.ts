/**
 * Shared bootstrap for scripts (§21 #108: adapter-aware).
 *   firebase (default): emulator when *_EMULATOR_HOST is set, else ADC / scripts key / gcloud token against the project.
 *   local: `--adapter local --data-dir <dir>` (or OPM_ADAPTER=local + OPM_DATA_DIR) — the opm-cloud data dir (SQLite +
 *          filesystem + built-in auth); --project is then only a label.
 * init() returns the PORTS (DocStore / AuthAdmin) plus a Bucket-shaped shim over the BlobStore, and installs the same
 * ports for the backend modules the script imports (functions/src/core/*).
 */
import { execFileSync } from 'node:child_process';
import { existsSync, readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { homedir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { initializeApp, applicationDefault, cert, getApps, type Credential } from 'firebase-admin/app';
import { getAuth } from 'firebase-admin/auth';
import { getFirestore, Timestamp as FaTimestamp, FieldValue as FaFieldValue, type Firestore } from 'firebase-admin/firestore';
import { getStorage } from 'firebase-admin/storage';
import { Firestore as GcFirestore, Timestamp as GcTimestamp, FieldValue as GcFieldValue } from '@google-cloud/firestore';
import { Storage as GcStorage } from '@google-cloud/storage';
import { OAuth2Client } from 'google-auth-library';
import { setPorts, type AuthAdmin, type BlobStore, type DocStore, type Ports } from '../functions/src/ports/index.js';
import { firebasePorts, type Bucket } from '../functions/src/adapters/firebase/index.js';
import { localPorts } from '../functions/src/adapters/local/index.js';

export function arg(name: string, def?: string): string | undefined {
  const i = process.argv.indexOf(`--${name}`);
  if (i >= 0 && i + 1 < process.argv.length && !process.argv[i + 1]!.startsWith('--')) return process.argv[i + 1];
  const eq = process.argv.find((a) => a.startsWith(`--${name}=`));
  if (eq) return eq.slice(name.length + 3);
  return def;
}
export const flag = (name: string) => process.argv.includes(`--${name}`);

/** --adapter local | OPM_ADAPTER=local */
export function adapterArg(): 'firebase' | 'local' {
  const a = arg('adapter') ?? process.env.OPM_ADAPTER ?? 'firebase';
  if (a !== 'firebase' && a !== 'local') throw new Error(`--adapter must be firebase | local (got ${a})`);
  return a;
}

export { bucketShim, type BucketShim } from '../functions/src/ports/bucket-shim.js';
import { bucketShim, type BucketShim } from '../functions/src/ports/bucket-shim.js';

export interface ScriptClients {
  kind: 'firebase' | 'local';
  emulator: boolean;
  db: DocStore;
  auth: AuthAdmin;
  blobs: BlobStore;
  bucket: BucketShim;
  ports: Ports;
  /** the raw GCS bucket / Firestore / Auth (firebase only: migration walks collections the port does not enumerate) */
  gcsBucket: Bucket | null;
  firestore: Firestore | null;
  close(): Promise<void>;
}

export function init(projectId: string, bucket?: string): ScriptClients {
  if (adapterArg() === 'local') {
    const dataDir = arg('data-dir') ?? process.env.OPM_DATA_DIR;
    if (!dataDir) throw new Error('--adapter local needs --data-dir <dir> (or OPM_DATA_DIR)');
    const c = initLocal(dataDir);
    setPorts(c.ports);
    return c;
  }
  const c = initFirebase(projectId, bucket);
  setPorts(c.ports);
  return c;
}
/** local data dir clients (does NOT install them as the global ports) */
export function initLocal(dataDir: string): ScriptClients {
  const p = localPorts({ dataDir, publicUrl: process.env.OPM_PUBLIC_URL ?? process.env.OPM_API_BASE ?? 'http://localhost:8790', consoleUrl: process.env.OPM_CONSOLE_URL });
  return { kind: 'local', emulator: false, db: p.store, auth: p.auth, blobs: p.blobs, bucket: bucketShim(p.blobs), ports: p, gcsBucket: null, firestore: null, close: () => p.close() };
}
/** Firebase clients of a project (emulator / ADC / scripts key / gcloud token; does NOT install them as the global ports) */
export function initFirebase(projectId: string, bucket?: string): ScriptClients {
  const emulator = !!(process.env.FIRESTORE_EMULATOR_HOST || process.env.FIREBASE_AUTH_EMULATOR_HOST || process.env.FIREBASE_STORAGE_EMULATOR_HOST);
  if (!process.env.GOOGLE_CLOUD_QUOTA_PROJECT && !emulator) process.env.GOOGLE_CLOUD_QUOTA_PROJECT = projectId;
  const storageBucket = bucket ?? process.env.OPM_BUCKET ?? (emulator && projectId.startsWith('demo-') ? `${projectId}.appspot.com` : `${projectId}.firebasestorage.app`);
  const gcloud = !emulator && useGcloudToken();
  if (!getApps().length) initializeApp(emulator ? { projectId, storageBucket } : { projectId, storageBucket, credential: gcloud ? gcloudCredential() : credential() });
  let fs: Firestore;
  let bkt;
  if (gcloud) {
    // firebase-admin's Firestore / Storage accept only SA keys or ADC: build them directly with an OAuth2 client
    const authClient = gcloudAuthClient(projectId);
    fs = new GcFirestore({ projectId, authClient } as never) as unknown as Firestore;
    // @google-cloud/storage bundles its own google-auth-library (v9): an OAuth2Client of another copy is not recognised
    // (requests go out anonymous), so build the storage client from storage's own copy.
    bkt = new GcStorage({ projectId, authClient: gcloudAuthClient(projectId, storageAuthLib()) } as never).bucket(storageBucket);
  } else {
    fs = getFirestore();
    bkt = getStorage().bucket(storageBucket);
  }
  try {
    fs.settings({ ignoreUndefinedProperties: true });
  } catch {
    /* already initialised */
  }
  const gcsBucket = bkt as Bucket;
  const p = firebasePorts({ db: fs, bucket: gcsBucket, auth: getAuth(), sdk: (gcloud ? { Timestamp: GcTimestamp, FieldValue: GcFieldValue } : { Timestamp: FaTimestamp, FieldValue: FaFieldValue }) as never });
  return { kind: 'firebase', emulator, db: p.store, auth: p.auth, blobs: p.blobs, bucket: bucketShim(p.blobs), ports: p, gcsBucket, firestore: fs, close: async () => undefined };
}

const adcPath = () => process.env.GOOGLE_APPLICATION_CREDENTIALS || join(homedir(), '.config/gcloud/application_default_credentials.json');
const keyPath = () => join(dirname(fileURLToPath(import.meta.url)), '../../.secrets/scripts-sa-key.json');
/** OPM_GCLOUD_TOKEN=1, or neither ADC nor the scripts key exists → short-lived `gcloud auth print-access-token`. */
function useGcloudToken(): boolean {
  return process.env.OPM_GCLOUD_TOKEN === '1' || (!existsSync(adcPath()) && !existsSync(keyPath()));
}

/**
 * Access token of the logged-in gcloud user (`gcloud auth print-access-token [--account $OPM_GCLOUD_ACCOUNT]`), cached
 * ~50 min. No long-lived keys on disk.
 */
let tok: { token: string; until: number } | null = null;
function gcloudToken(): { token: string; until: number } {
  if (tok && Date.now() < tok.until) return tok;
  const args = ['auth', 'print-access-token'];
  if (process.env.OPM_GCLOUD_ACCOUNT) args.push('--account', process.env.OPM_GCLOUD_ACCOUNT);
  const token = execFileSync('gcloud', args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'inherit'] }).trim();
  if (!token) throw new Error('gcloud auth print-access-token returned nothing (run `gcloud auth login`)');
  tok = { token, until: Date.now() + 50 * 60_000 };
  return tok;
}
function gcloudCredential(): Credential {
  return {
    async getAccessToken() {
      const t = gcloudToken();
      return { access_token: t.token, expires_in: 3000 };
    },
  };
}
function storageAuthLib(): { OAuth2Client: typeof OAuth2Client } {
  const req = createRequire(createRequire(import.meta.url).resolve('@google-cloud/storage'));
  return req('google-auth-library') as { OAuth2Client: typeof OAuth2Client };
}
function gcloudAuthClient(projectId: string, lib: { OAuth2Client: typeof OAuth2Client } = { OAuth2Client }): OAuth2Client {
  const c = new lib.OAuth2Client();
  c.quotaProjectId = process.env.GOOGLE_CLOUD_QUOTA_PROJECT || projectId;
  c.refreshHandler = async () => {
    const t = gcloudToken();
    return { access_token: t.token, expiry_date: t.until };
  };
  const t = gcloudToken();
  c.setCredentials({ access_token: t.token, expiry_date: t.until });
  return c;
}

/**
 * Credentials for prod: GOOGLE_APPLICATION_CREDENTIALS / gcloud ADC when present, else the scripts service-account key
 * .secrets/scripts-sa-key.json. Without both (or OPM_GCLOUD_TOKEN=1): the gcloud CLI user token (see init()).
 */
function credential(): Credential {
  if (existsSync(adcPath())) return applicationDefault();
  if (existsSync(keyPath())) return cert(JSON.parse(readFileSync(keyPath(), 'utf8')));
  throw new Error('no credentials: set GOOGLE_APPLICATION_CREDENTIALS, run `gcloud auth application-default login`, or `gcloud auth login` (OPM_GCLOUD_TOKEN=1)');
}
