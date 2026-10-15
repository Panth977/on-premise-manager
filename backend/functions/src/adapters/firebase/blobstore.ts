/** Firebase BlobStore adapter: the Cloud Storage bucket, exactly as used before §21 #108. */
import type { Readable, Writable } from 'node:stream';
import type { getStorage } from 'firebase-admin/storage';
import type { BlobMeta, BlobStore, LifecycleRule, ListedBlob, PutOptions } from '../../ports/blobstore.js';

export type Bucket = ReturnType<ReturnType<typeof getStorage>['bucket']>;
const is404 = (e: unknown) => (e as { code?: number }).code === 404;
const ms = (s: unknown) => (typeof s === 'string' && Number.isFinite(Date.parse(s)) ? Date.parse(s) : 0);

export class FirebaseBlobStore implements BlobStore {
  readonly kind = 'firebase' as const;
  readonly canCompose: boolean;
  constructor(
    private readonly bucketOf: () => Bucket,
    opts: { canCompose: boolean },
  ) {
    this.canCompose = opts.canCompose;
  }
  get bucket(): Bucket {
    return this.bucketOf();
  }
  async put(path: string, data: Buffer, o: PutOptions = {}) {
    await this.bucket.file(path).save(data, {
      resumable: false,
      contentType: o.contentType ?? 'application/octet-stream',
      metadata: { ...(o.cacheControl ? { cacheControl: o.cacheControl } : {}), ...(o.meta ? { metadata: o.meta } : {}) },
    });
  }
  writeStream(path: string, o: PutOptions = {}): Writable {
    return this.bucket.file(path).createWriteStream({
      resumable: false,
      contentType: o.contentType ?? 'application/octet-stream',
      validation: false,
      ...(o.cacheControl || o.meta ? { metadata: { ...(o.cacheControl ? { cacheControl: o.cacheControl } : {}), ...(o.meta ? { metadata: o.meta } : {}) } } : {}),
    });
  }
  async get(path: string): Promise<Buffer | null> {
    try {
      const [b] = await this.bucket.file(path).download();
      return b;
    } catch (e) {
      if (is404(e)) return null;
      throw e;
    }
  }
  stream(path: string, range?: { start: number; end: number }): Readable {
    return this.bucket.file(path).createReadStream({ validation: false, ...(range ? { start: range.start, end: range.end } : {}) });
  }
  async head(path: string): Promise<BlobMeta | null> {
    try {
      const [m] = await this.bucket.file(path).getMetadata();
      const meta: Record<string, string> = {};
      for (const [k, v] of Object.entries((m.metadata ?? {}) as Record<string, unknown>)) if (v !== null && v !== undefined) meta[k] = String(v);
      return { size: Number(m.size ?? 0), contentType: String(m.contentType ?? 'application/octet-stream'), meta, createdMs: ms(m.timeCreated), updatedMs: ms(m.updated) };
    } catch (e) {
      if (is404(e)) return null;
      throw e;
    }
  }
  async setMeta(path: string, meta: Record<string, string>) {
    await this.bucket.file(path).setMetadata({ metadata: meta });
  }
  async delete(path: string) {
    await this.bucket.file(path).delete({ ignoreNotFound: true });
  }
  async deletePrefix(prefix: string) {
    await this.bucket.deleteFiles({ prefix, force: true });
  }
  async list(prefix: string): Promise<ListedBlob[]> {
    const [files] = await this.bucket.getFiles({ prefix });
    return files.map((f) => ({ path: f.name, size: Number(f.metadata.size ?? 0), createdMs: ms(f.metadata.timeCreated) }));
  }
  async copy(src: string, dst: string) {
    await this.bucket.file(src).copy(this.bucket.file(dst));
  }
  async compose(sources: string[], dst: string) {
    await this.bucket.combine(
      sources.map((s) => this.bucket.file(s)),
      this.bucket.file(dst),
    );
  }
  async signedReadUrl(path: string, o: { expiresMs: number; disposition?: string }) {
    const [url] = await this.bucket.file(path).getSignedUrl({ version: 'v4', action: 'read', expires: Date.now() + o.expiresMs, ...(o.disposition ? { responseDisposition: o.disposition } : {}) });
    return url;
  }
  /** the bucket's lifecycle configuration (npm run lifecycle) does this in production */
  async sweep(_rules: LifecycleRule[]) {
    return 0;
  }
}
