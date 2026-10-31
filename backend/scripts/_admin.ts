/**
 * Shared bootstrap for scripts: `--data-dir <dir>` (or OPM_DATA_DIR) is the opm-cloud data dir (SQLite + filesystem +
 * built-in auth). init() returns the PORTS (DocStore / AuthAdmin) plus a Bucket-shaped shim over the BlobStore, and
 * installs the same ports for the backend modules the script imports (src/core/*).
 */
import { setPorts, type AuthAdmin, type BlobStore, type DocStore, type Ports } from '../src/driver/index.js';
import { localPorts } from '../src/driver/local.js';

export function arg(name: string, def?: string): string | undefined {
  const i = process.argv.indexOf(`--${name}`);
  if (i >= 0 && i + 1 < process.argv.length && !process.argv[i + 1]!.startsWith('--')) return process.argv[i + 1];
  const eq = process.argv.find((a) => a.startsWith(`--${name}=`));
  if (eq) return eq.slice(name.length + 3);
  return def;
}
export const flag = (name: string) => process.argv.includes(`--${name}`);

export { bucketShim, type BucketShim } from '../src/driver/bucket-shim.js';
import { bucketShim, type BucketShim } from '../src/driver/bucket-shim.js';

export interface ScriptClients {
  db: DocStore;
  auth: AuthAdmin;
  blobs: BlobStore;
  bucket: BucketShim;
  ports: Ports;
  close(): Promise<void>;
}

/** `project` is only a label in the scripts' output. */
export function init(_project?: string, _bucket?: string): ScriptClients {
  const dataDir = arg('data-dir') ?? process.env.OPM_DATA_DIR;
  if (!dataDir) throw new Error('needs --data-dir <dir> (or OPM_DATA_DIR)');
  const p = localPorts({ dataDir, publicUrl: process.env.OPM_PUBLIC_URL ?? process.env.OPM_API_BASE ?? 'http://localhost:8790', consoleUrl: process.env.OPM_CONSOLE_URL });
  setPorts(p);
  return { db: p.store, auth: p.auth, blobs: p.blobs, bucket: bucketShim(p.blobs), ports: p, close: () => p.close() };
}
