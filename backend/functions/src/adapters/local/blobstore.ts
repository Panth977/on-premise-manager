/**
 * Local BlobStore: objects at <dataDir>/blobs/<object path> (the same object paths as the GCS bucket), metadata in a
 * sidecar <dataDir>/blobmeta/<path>.json, atomic writes (temp file in <dataDir>/tmp + rename). Read URLs are opm-cloud
 * `/_blob/<path>?exp&sig[&rd]` with an HMAC-SHA256 token (handleBlobHttp serves them); lifecycle = sweep().
 */
import { createHmac, randomBytes, timingSafeEqual } from 'node:crypto';
import { createReadStream, createWriteStream, mkdirSync, readFileSync, writeFileSync, type WriteStream } from 'node:fs';
import { copyFile, mkdir, readFile, readdir, rename, rm, rmdir, stat, unlink, writeFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { Readable, Writable } from 'node:stream';
import type { BlobMeta, BlobStore, LifecycleRule, ListedBlob, PutOptions } from '../../ports/blobstore.js';

interface Sidecar {
  contentType: string;
  cacheControl?: string;
  meta: Record<string, string>;
  createdMs: number;
  updatedMs: number;
}
const notFound = (p: string) => Object.assign(new Error(`No such object: ${p}`), { code: 404 });

/** object path rules: relative, no empty / "." / ".." segments, no backslashes or NUL */
export function checkBlobPath(p: string): string {
  if (typeof p !== 'string' || !p || p.startsWith('/') || p.includes('\\') || p.includes('\0')) throw Object.assign(new Error(`invalid object path "${p}"`), { code: 400 });
  for (const s of p.split('/')) if (!s || s === '.' || s === '..') throw Object.assign(new Error(`invalid object path "${p}"`), { code: 400 });
  return p;
}
function secretFile(file: string): Buffer {
  try {
    return readFileSync(file);
  } catch {
    mkdirSync(dirname(file), { recursive: true, mode: 0o700 });
    try {
      writeFileSync(file, randomBytes(32), { mode: 0o600, flag: 'wx' });
    } catch (e) {
      if ((e as NodeJS.ErrnoException).code !== 'EEXIST') throw e;
    }
    return readFileSync(file);
  }
}
/** a secret from env (utf8) or a generated 32-byte key file (0600) under <dataDir>/secrets */
export function loadSecret(envName: string, dataDir: string, name: string): Buffer {
  const v = process.env[envName];
  if (v) {
    if (v.length < 16) throw new Error(`${envName} must be at least 16 characters`);
    return Buffer.from(v, 'utf8');
  }
  return secretFile(join(dataDir, 'secrets', name));
}

export class LocalBlobStore implements BlobStore {
  readonly kind = 'local' as const;
  readonly canCompose = false;
  readonly root: string;
  private readonly metaRoot: string;
  private readonly tmp: string;
  private key: Buffer | null = null;
  constructor(
    readonly dataDir: string,
    readonly publicUrl: string,
  ) {
    this.root = join(dataDir, 'blobs');
    this.metaRoot = join(dataDir, 'blobmeta');
    this.tmp = join(dataDir, 'tmp');
    for (const d of [this.root, this.metaRoot, this.tmp]) mkdirSync(d, { recursive: true });
  }
  private file(p: string) {
    return join(this.root, checkBlobPath(p));
  }
  private side(p: string) {
    return join(this.metaRoot, `${checkBlobPath(p)}.json`);
  }
  private tmpName() {
    return join(this.tmp, `${Date.now()}-${randomBytes(8).toString('hex')}`);
  }
  private async readSide(p: string): Promise<Sidecar | null> {
    try {
      return JSON.parse(await readFile(this.side(p), 'utf8')) as Sidecar;
    } catch {
      return null;
    }
  }
  private async writeSide(p: string, s: Sidecar) {
    const f = this.side(p);
    await mkdir(dirname(f), { recursive: true });
    const t = this.tmpName();
    await writeFile(t, JSON.stringify(s));
    await rename(t, f);
  }
  /** move a finished temp file into place (sidecar first, so a visible object always has its metadata) */
  private async publish(tmp: string, p: string, o: PutOptions) {
    const now = Date.now();
    await this.writeSide(p, { contentType: o.contentType ?? 'application/octet-stream', ...(o.cacheControl ? { cacheControl: o.cacheControl } : {}), meta: { ...(o.meta ?? {}) }, createdMs: now, updatedMs: now });
    const f = this.file(p);
    await mkdir(dirname(f), { recursive: true });
    await rename(tmp, f);
  }
  async put(path: string, data: Buffer, o: PutOptions = {}) {
    checkBlobPath(path);
    const t = this.tmpName();
    try {
      await writeFile(t, data);
      await this.publish(t, path, o);
    } catch (e) {
      await rm(t, { force: true });
      throw e;
    }
  }
  writeStream(path: string, o: PutOptions = {}): Writable {
    checkBlobPath(path);
    const t = this.tmpName();
    let ws: WriteStream | null = null;
    let published = false;
    const open = () => (ws ??= createWriteStream(t));
    return new Writable({
      write(chunk: Buffer, _enc, cb) {
        const s = open();
        if (s.write(chunk)) cb();
        else s.once('drain', () => cb());
      },
      final: (cb) => {
        const s = open();
        s.end(() => {
          this.publish(t, path, o).then(
            () => {
              published = true;
              cb();
            },
            (e) => rm(t, { force: true }).finally(() => cb(e as Error)),
          );
        });
      },
      destroy(err, cb) {
        if (ws && !ws.closed) ws.destroy();
        if (published) return cb(err);
        rm(t, { force: true }).finally(() => cb(err));
      },
    });
  }
  async get(path: string): Promise<Buffer | null> {
    try {
      return await readFile(this.file(path));
    } catch (e) {
      if ((e as NodeJS.ErrnoException).code === 'ENOENT' || (e as NodeJS.ErrnoException).code === 'ENOTDIR') return null;
      throw e;
    }
  }
  stream(path: string, range?: { start: number; end: number }): Readable {
    const s = createReadStream(this.file(path), range ? { start: range.start, end: range.end } : {});
    s.on('error', (e: NodeJS.ErrnoException) => {
      if (e.code === 'ENOENT') Object.assign(e, { code: 404 });
    });
    return s;
  }
  async head(path: string): Promise<BlobMeta | null> {
    let st;
    try {
      st = await stat(this.file(path));
    } catch {
      return null;
    }
    if (!st.isFile()) return null;
    const s = await this.readSide(path);
    return { size: st.size, contentType: s?.contentType ?? 'application/octet-stream', meta: s?.meta ?? {}, createdMs: s?.createdMs ?? st.birthtimeMs, updatedMs: s?.updatedMs ?? st.mtimeMs };
  }
  /** cacheControl of an object (served by handleBlobHttp) */
  async cacheControl(path: string): Promise<string | undefined> {
    return (await this.readSide(path))?.cacheControl;
  }
  async setMeta(path: string, meta: Record<string, string>) {
    const h = await this.head(path);
    if (!h) throw notFound(path);
    const s = (await this.readSide(path)) ?? { contentType: h.contentType, meta: {}, createdMs: h.createdMs, updatedMs: h.updatedMs };
    const m = { ...s.meta };
    for (const [k, v] of Object.entries(meta)) {
      if (v === null || v === undefined) delete m[k];
      else m[k] = String(v);
    }
    await this.writeSide(path, { ...s, meta: m, updatedMs: Date.now() });
  }
  private async prune(dir: string, stop: string) {
    while (dir.startsWith(stop) && dir !== stop) {
      try {
        await rmdir(dir);
      } catch {
        return;
      }
      dir = dirname(dir);
    }
  }
  async delete(path: string) {
    const f = this.file(path);
    await unlink(f).catch(() => undefined);
    await unlink(this.side(path)).catch(() => undefined);
    await this.prune(dirname(f), this.root);
    await this.prune(dirname(this.side(path)), this.metaRoot);
  }
  async deletePrefix(prefix: string) {
    for (const b of await this.list(prefix)) await this.delete(b.path);
  }
  /** every object whose path starts with the raw string prefix ('' = all), sorted by path */
  async list(prefix: string): Promise<ListedBlob[]> {
    const i = prefix.lastIndexOf('/');
    const base = i < 0 ? '' : prefix.slice(0, i);
    if (base) checkBlobPath(base);
    const out: ListedBlob[] = [];
    const walk = async (rel: string) => {
      let ents;
      try {
        ents = await readdir(join(this.root, rel), { withFileTypes: true });
      } catch {
        return;
      }
      for (const e of ents) {
        const p = rel ? `${rel}/${e.name}` : e.name;
        if (e.isDirectory()) {
          if (p.startsWith(prefix) || prefix.startsWith(`${p}/`)) await walk(p);
        } else if (e.isFile() && p.startsWith(prefix)) {
          const st = await stat(join(this.root, p)).catch(() => null);
          if (!st) continue;
          const s = await this.readSide(p);
          out.push({ path: p, size: st.size, createdMs: s?.createdMs ?? st.birthtimeMs });
        }
      }
    };
    await walk(base);
    return out.sort((a, b) => Buffer.compare(Buffer.from(a.path), Buffer.from(b.path)));
  }
  async copy(src: string, dst: string) {
    const s = await this.readSide(src);
    const t = this.tmpName();
    try {
      await copyFile(this.file(src), t);
    } catch (e) {
      if ((e as NodeJS.ErrnoException).code === 'ENOENT') throw notFound(src);
      throw e;
    }
    try {
      await this.publish(t, dst, { contentType: s?.contentType, cacheControl: s?.cacheControl, meta: s?.meta });
    } catch (e) {
      await rm(t, { force: true });
      throw e;
    }
  }
  async compose(): Promise<void> {
    throw new Error('compose is not supported by the local BlobStore (canCompose=false: concatenate by streaming)');
  }
  private secret(): Buffer {
    return (this.key ??= loadSecret('OPM_BLOB_SECRET', this.dataDir, 'blob-hmac.key'));
  }
  sign(path: string, exp: number, disposition: string): string {
    return createHmac('sha256', this.secret()).update(`${path}\n${exp}\n${disposition}`).digest('base64url');
  }
  verify(path: string, exp: number, disposition: string, sig: string): boolean {
    const want = Buffer.from(this.sign(path, exp, disposition));
    const got = Buffer.from(sig);
    return want.length === got.length && timingSafeEqual(want, got);
  }
  async signedReadUrl(path: string, o: { expiresMs: number; disposition?: string }) {
    checkBlobPath(path);
    const exp = Date.now() + o.expiresMs;
    const sig = this.sign(path, exp, o.disposition ?? '');
    const enc = path.split('/').map(encodeURIComponent).join('/');
    return `${this.publicUrl}/_blob/${enc}?exp=${exp}&sig=${sig}${o.disposition ? `&rd=${encodeURIComponent(o.disposition)}` : ''}`;
  }
  async sweep(rules: LifecycleRule[], now = Date.now()) {
    let n = 0;
    for (const r of rules) {
      const cutoff = now - r.ageDays * 86_400_000;
      for (const p of r.prefixes) {
        for (const b of await this.list(p)) {
          if (b.createdMs < cutoff) {
            await this.delete(b.path);
            n++;
          }
        }
      }
    }
    // stale temp files (crashed writes) older than a day
    for (const f of await readdir(this.tmp).catch(() => [] as string[])) {
      const st = await stat(join(this.tmp, f)).catch(() => null);
      if (st && st.mtimeMs < now - 86_400_000) await rm(join(this.tmp, f), { force: true });
    }
    return n;
  }
}

// ───────────────────────────────────────── HTTP (/_blob/…)
export interface BlobHttpResponse {
  status: number;
  headers: Record<string, string>;
  body: Buffer | string | Readable | null;
}
const EXPOSE = 'Content-Type, Content-Length, ETag, Content-Disposition, Range, Accept-Ranges, Content-Range';

/** serves GET / HEAD /_blob/<path>?exp&sig[&rd] (signed read URLs); CORS like storage.cors.json */
export async function handleBlobHttp(req: { method: string; url: string; headers: Record<string, string | undefined> }, blobs: LocalBlobStore, o: { allowedOrigins: string[] }): Promise<BlobHttpResponse> {
  const hdr = (n: string) => {
    for (const [k, v] of Object.entries(req.headers)) if (k.toLowerCase() === n) return v;
    return undefined;
  };
  const origin = hdr('origin');
  const cors: Record<string, string> = {};
  if (origin && (o.allowedOrigins.includes(origin) || o.allowedOrigins.includes('*'))) {
    cors['Access-Control-Allow-Origin'] = origin;
    cors['Access-Control-Expose-Headers'] = EXPOSE;
    cors.Vary = 'Origin';
  }
  const err = (status: number, msg: string): BlobHttpResponse => ({ status, headers: { ...cors, 'Content-Type': 'text/plain; charset=utf-8', 'Cache-Control': 'no-store' }, body: req.method === 'HEAD' ? null : msg });
  if (req.method === 'OPTIONS') {
    return { status: 204, headers: { ...cors, ...(cors['Access-Control-Allow-Origin'] ? { 'Access-Control-Allow-Methods': 'GET, HEAD', 'Access-Control-Allow-Headers': hdr('access-control-request-headers') ?? 'Range', 'Access-Control-Max-Age': '3600' } : {}) }, body: null };
  }
  if (req.method !== 'GET' && req.method !== 'HEAD') return { ...err(405, 'method not allowed'), headers: { ...err(405, '').headers, Allow: 'GET, HEAD, OPTIONS' } };
  const u = new URL(req.url, 'http://local');
  const i = u.pathname.indexOf('/_blob/');
  if (i < 0) return err(404, 'not found');
  let path: string;
  try {
    path = checkBlobPath(
      u.pathname
        .slice(i + 7)
        .split('/')
        .map((s) => decodeURIComponent(s))
        .join('/'),
    );
  } catch {
    return err(400, 'bad object path');
  }
  const exp = Number(u.searchParams.get('exp'));
  const sig = u.searchParams.get('sig') ?? '';
  const rd = u.searchParams.get('rd') ?? '';
  if (!Number.isFinite(exp) || !sig || !blobs.verify(path, exp, rd, sig)) return err(403, 'invalid signature');
  if (exp < Date.now()) return err(403, 'expired');
  const h = await blobs.head(path);
  if (!h) return err(404, 'no such object');
  const base: Record<string, string> = {
    ...cors,
    'Content-Type': h.contentType,
    'Accept-Ranges': 'bytes',
    ETag: `"${h.size.toString(16)}-${Math.floor(h.updatedMs).toString(16)}"`,
    'Last-Modified': new Date(h.updatedMs).toUTCString(),
    'Cache-Control': (await blobs.cacheControl(path)) ?? 'private, max-age=0',
    'X-Content-Type-Options': 'nosniff',
    ...(rd ? { 'Content-Disposition': rd } : {}),
  };
  const range = hdr('range');
  if (range) {
    const m = /^bytes=(\d*)-(\d*)$/.exec(range.trim());
    if (m && (m[1] || m[2])) {
      let start: number;
      let end: number;
      if (!m[1]) {
        const n = Number(m[2]);
        start = Math.max(0, h.size - n);
        end = h.size - 1;
        if (n === 0) start = h.size;
      } else {
        start = Number(m[1]);
        end = m[2] ? Math.min(Number(m[2]), h.size - 1) : h.size - 1;
      }
      if (start >= h.size || start > end) return { status: 416, headers: { ...cors, 'Content-Range': `bytes */${h.size}`, 'Accept-Ranges': 'bytes' }, body: null };
      return { status: 206, headers: { ...base, 'Content-Range': `bytes ${start}-${end}/${h.size}`, 'Content-Length': String(end - start + 1) }, body: req.method === 'HEAD' ? null : blobs.stream(path, { start, end }) };
    }
    // multi-range / malformed: serve the whole object (RFC 9110 allows ignoring Range)
  }
  return { status: 200, headers: { ...base, 'Content-Length': String(h.size) }, body: req.method === 'HEAD' ? null : h.size === 0 ? Buffer.alloc(0) : blobs.stream(path) };
}
