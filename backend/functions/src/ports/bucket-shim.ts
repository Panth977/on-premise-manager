/** A GCS-Bucket-shaped view of any BlobStore (scripts written against bucket.file(p).save/download/exists keep working on every adapter, §21 #108). */
import type { BlobStore } from './blobstore.js';

/** The subset of the GCS Bucket API the scripts use, over any BlobStore (local data dir or the bucket itself). */
export interface BucketShim {
  file(path: string): {
    save(data: Buffer | string, o?: { contentType?: string; resumable?: boolean; metadata?: { cacheControl?: string; metadata?: Record<string, string> } }): Promise<void>;
    download(): Promise<[Buffer]>;
    exists(): Promise<[boolean]>;
    delete(o?: { ignoreNotFound?: boolean }): Promise<void>;
    getMetadata(): Promise<[{ size: number; contentType: string; metadata: Record<string, string>; timeCreated: string }]>;
  };
  getFiles(o: { prefix: string }): Promise<[{ name: string; metadata: { size: number; timeCreated: string } }[]]>;
}
export function bucketShim(b: BlobStore): BucketShim {
  const nf = (p: string) => Object.assign(new Error(`No such object: ${p}`), { code: 404 });
  return {
    file: (p) => ({
      save: (data, o) => b.put(p, Buffer.isBuffer(data) ? data : Buffer.from(data, 'utf8'), { contentType: o?.contentType, cacheControl: o?.metadata?.cacheControl, meta: o?.metadata?.metadata }),
      async download() {
        const x = await b.get(p);
        if (!x) throw nf(p);
        return [x];
      },
      exists: async () => [(await b.head(p)) !== null],
      async delete(o) {
        if (!o?.ignoreNotFound && !(await b.head(p))) throw nf(p);
        await b.delete(p);
      },
      async getMetadata() {
        const m = await b.head(p);
        if (!m) throw nf(p);
        return [{ size: m.size, contentType: m.contentType, metadata: m.meta, timeCreated: new Date(m.createdMs).toISOString() }];
      },
    }),
    getFiles: async (o) => [(await b.list(o.prefix)).map((f) => ({ name: f.path, metadata: { size: f.size, timeCreated: new Date(f.createdMs).toISOString() } }))],
  };
}

