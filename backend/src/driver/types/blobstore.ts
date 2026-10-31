/**
 * BlobStore port (SPEC §21 #108) — object storage as OPM uses it: content-addressed blobs / files, transfers, upload
 * parts, staging objects, agent binaries + the deps manifest, activity bundles, commit bodies.
 *
 * The filesystem under <dataDir>/blobs; metadata in a sidecar; read URLs = opm-cloud `/_blob/<path>?exp&sig` with a
 * short-lived HMAC token; lifecycle = a sweep job.
 */
import type { Readable, Writable } from 'node:stream';

export interface BlobMeta {
  size: number;
  contentType: string;
  /** custom metadata (e.g. opmSha256) */
  meta: Record<string, string>;
  /** creation time, ms */
  createdMs: number;
  /** last write / metadata change, ms */
  updatedMs: number;
}
export interface PutOptions {
  contentType?: string;
  cacheControl?: string;
  meta?: Record<string, string>;
}
export interface ListedBlob {
  path: string;
  size: number;
  createdMs: number;
}
export interface LifecycleRule {
  /** objects whose path starts with one of these */
  prefixes: string[];
  /** delete when older than this many days */
  ageDays: number;
}

export interface BlobStore {
  /** server-side concatenation of objects (GCS compose); false → callers concatenate by streaming */
  readonly canCompose: boolean;
  put(path: string, data: Buffer, o?: PutOptions): Promise<void>;
  /** a write stream; the object becomes visible when the stream finishes */
  writeStream(path: string, o?: PutOptions): Writable;
  /** whole object, null when missing */
  get(path: string): Promise<Buffer | null>;
  /** inclusive byte range like HTTP Range */
  stream(path: string, range?: { start: number; end: number }): Readable;
  head(path: string): Promise<BlobMeta | null>;
  setMeta(path: string, meta: Record<string, string>): Promise<void>;
  /** missing objects are ignored */
  delete(path: string): Promise<void>;
  deletePrefix(prefix: string): Promise<void>;
  list(prefix: string): Promise<ListedBlob[]>;
  copy(src: string, dst: string): Promise<void>;
  compose(sources: string[], dst: string): Promise<void>;
  /** short-lived download URL (GCS v4 signed / opm-cloud HMAC token) */
  signedReadUrl(path: string, o: { expiresMs: number; disposition?: string }): Promise<string>;
  /** the lifecycle rules (storage.lifecycle.json). Firebase: the bucket applies them itself (no-op); local: deletes
   *  matching objects, returns how many */
  sweep(rules: LifecycleRule[], now?: number): Promise<number>;
}
