/** Object storage helpers (BlobStore port, §21 #108): streamed downloads with Range or 302 to a 5-minute signed URL; hashed uploads. */
import { createHash } from 'node:crypto';
import { Readable, Transform } from 'node:stream';
import { pipeline } from 'node:stream/promises';
import { blobs } from '../driver/index.js';
import { env } from './env.js';
import { E } from './errors.js';
import { Reply } from './reply.js';

export const paths = {
  blob: (hex: string) => `blobs/sha256/${hex}`,
  file: (hex: string) => `files/${hex}`,
  transfer: (dep: string, tid: string) => `transfers/${dep}/${tid}`,
  /** §21 #123 the cloud copy of a shared file (a file migrated from a transfer keeps its transfers/… object) */
  sharedFile: (dep: string, fileId: string) => `shared-files/${dep}/${fileId}`,
  uploadPart: (uid: string, n: number) => `uploads/${uid}/part-${n}`,
  logo: (dep: string) => `logos/${dep}`,
  bundle: (dep: string, name: string) => `activity-bundles/${dep}/${name}`,
  agentBinary: (v: string, os: string, arch: string) => `agent/${v}/${os}-${arch}/opm-agent`,
  agentLatest: 'agent/latest',
};

export async function exists(path: string): Promise<boolean> {
  return (await blobs().head(path)) !== null;
}
export async function sizeOf(path: string): Promise<number | null> {
  const m = await blobs().head(path);
  return m ? m.size : null;
}
export async function readText(path: string): Promise<string | null> {
  const b = await blobs().get(path);
  return b ? b.toString('utf8') : null;
}
export async function writeBytes(path: string, data: Buffer, contentType = 'application/octet-stream') {
  await blobs().put(path, data, { contentType, cacheControl: 'private, max-age=0' });
}
export async function remove(path: string) {
  await blobs().delete(path);
}
export async function removePrefix(prefix: string) {
  await blobs().deletePrefix(prefix);
}

/** Stream a web/Node readable into Storage while hashing; returns { hex, size }. Deletes the object on hash mismatch. */
export async function streamToStorage(path: string, src: Readable, expectHex?: string, contentType = 'application/octet-stream'): Promise<{ hex: string; size: number }> {
  const h = createHash('sha256');
  let size = 0;
  const tap = new Transform({
    transform(chunk: Buffer, _enc, cb) {
      h.update(chunk);
      size += chunk.length;
      cb(null, chunk);
    },
  });
  await pipeline(src, tap, blobs().writeStream(path, { contentType }));
  const hex = h.digest('hex');
  if (expectHex && hex !== expectHex) {
    await remove(path);
    throw new Error(`sha256 mismatch for ${path}: got ${hex}, expected ${expectHex}`);
  }
  return { hex, size };
}

/** sha256 + size of a stored object (streamed). */
export async function hashObject(path: string): Promise<{ hex: string; size: number }> {
  const h = createHash('sha256');
  let size = 0;
  await pipeline(blobs().stream(path), new Transform({
    transform(chunk: Buffer, _e, cb) {
      h.update(chunk);
      size += chunk.length;
      cb();
    },
  }));
  return { hex: h.digest('hex'), size };
}

export interface ServeOpts {
  range?: string | undefined;
  headers?: Record<string, string>;
  /** relayed requests never redirect (the asking site may have no internet) */
  noRedirect?: boolean;
  fileName?: string;
  contentType?: string;
  /** bodies up to this size always stream with 200/206 (never 302), e.g. BLOB_STREAM_MAX for human blob downloads */
  streamMax?: number;
}
/**
 * §21 #114: an agent request that carries an Origin header was sent by a browser: the agent GUI tab of the internet tunnel
 * (§9.10). It must stream, never 302: the redirect target (/_blob or a GCS signed URL) does not allow the GUI's origin
 * (http://<host>:8080, any address), so the browser drops the response and the download fails. The agent itself never
 * sends Origin, so direct downloads keep the redirect.
 */
export function noRedirectFor(headers: Record<string, string | undefined>, relayedBy?: string | null): boolean {
  return !!relayedBy || !!headers['origin'];
}
/** Redirect or stream (pure; tested): 302 only when redirects are on, not disabled, and the body is above streamMax. */
export function serveMode(size: number, o: { redirect: boolean; noRedirect?: boolean; streamMax?: number }): 'redirect' | 'stream' {
  if (!o.redirect || o.noRedirect) return 'stream';
  if (o.streamMax !== undefined && size <= o.streamMax) return 'stream';
  return 'redirect';
}
/** 200 stream (Range → 206) or 302 to a 5-minute signed URL (OPM_BLOB_REDIRECT=1; GCS v4 / opm-cloud HMAC). */
export async function serveObject(path: string, o: ServeOpts = {}): Promise<Reply> {
  const size = await sizeOf(path);
  if (size === null) throw E.notFound('the content is not stored');
  const headers: Record<string, string> = { ...(o.headers ?? {}), 'Accept-Ranges': 'bytes' };
  if (o.fileName) headers['Content-Disposition'] = `attachment; filename="${o.fileName.replace(/["\\\r\n]/g, '_')}"; filename*=UTF-8''${encodeURIComponent(o.fileName)}`;
  if (serveMode(size, { redirect: env.blobRedirect, noRedirect: o.noRedirect, streamMax: o.streamMax }) === 'redirect') {
    const url = await blobs().signedReadUrl(path, { expiresMs: 5 * 60 * 1000, disposition: headers['Content-Disposition'] });
    return new Reply(302, { ...headers, Location: url, 'Cache-Control': 'no-store' }, null);
  }
  headers['Content-Type'] = o.contentType ?? 'application/octet-stream';
  let start = 0;
  let end = size - 1;
  let status = 200;
  if (o.range) {
    const m = /^bytes=(\d*)-(\d*)$/.exec(o.range.trim());
    let ok = !!m && !(m[1] === '' && m[2] === '');
    if (ok && m) {
      if (m[1] === '') {
        const n = Number(m[2]);
        start = Math.max(0, size - n);
      } else {
        start = Number(m[1]);
        if (m[2] !== '') end = Math.min(Number(m[2]), size - 1);
      }
      ok = start <= end && start < size;
    }
    if (!ok) return new Reply(416, { ...headers, 'Content-Range': `bytes */${size}`, 'Content-Type': 'text/plain' }, 'range not satisfiable');
    status = 206;
    headers['Content-Range'] = `bytes ${start}-${end}/${size}`;
  }
  headers['Content-Length'] = String(size === 0 ? 0 : end - start + 1);
  if (size === 0) return new Reply(200, headers, Buffer.alloc(0));
  const stream = blobs().stream(path, { start, end });
  return new Reply(status, headers, stream);
}
