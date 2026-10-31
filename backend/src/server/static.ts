/**
 * Static frontend with SPA fallback — what Firebase Hosting does for frontend/build (firebase.json: every path that is
 * not a file → /index.html). Immutable SvelteKit assets get long caching, everything else no-cache.
 */
import { createReadStream, existsSync, statSync } from 'node:fs';
import path from 'node:path';
import type { Readable } from 'node:stream';

const TYPES: Record<string, string> = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.gif': 'image/gif',
  '.webp': 'image/webp',
  '.ico': 'image/x-icon',
  '.txt': 'text/plain; charset=utf-8',
  '.woff': 'font/woff',
  '.woff2': 'font/woff2',
  '.ttf': 'font/ttf',
  '.map': 'application/json; charset=utf-8',
  '.webmanifest': 'application/manifest+json',
  '.wasm': 'application/wasm',
  '.mp4': 'video/mp4',
  '.webm': 'video/webm',
};

export interface StaticReply {
  status: number;
  headers: Record<string, string>;
  body: Readable | string | null;
}
export function serveStatic(root: string, method: string, urlPath: string): StaticReply | null {
  if (method !== 'GET' && method !== 'HEAD') return null;
  let rel: string;
  try {
    rel = decodeURIComponent(urlPath.split('?')[0]!);
  } catch {
    return { status: 400, headers: { 'Content-Type': 'text/plain' }, body: 'bad path' };
  }
  const full = path.resolve(root, '.' + rel);
  if (!full.startsWith(path.resolve(root))) return { status: 404, headers: { 'Content-Type': 'text/plain' }, body: 'not found' };
  let file = full;
  let st = existsSync(file) ? statSync(file) : null;
  if (st?.isDirectory()) {
    file = path.join(file, 'index.html');
    st = existsSync(file) ? statSync(file) : null;
  }
  if (!st || !st.isFile()) {
    // SPA fallback (Hosting rewrite "**" → /index.html)
    file = path.join(root, 'index.html');
    if (!existsSync(file)) return null;
    st = statSync(file);
  }
  const ext = path.extname(file).toLowerCase();
  const immutable = /\/_app\/immutable\//.test(file);
  const headers: Record<string, string> = {
    'Content-Type': TYPES[ext] ?? 'application/octet-stream',
    'Content-Length': String(st.size),
    'Cache-Control': immutable ? 'public, max-age=31536000, immutable' : 'no-cache',
    'X-Content-Type-Options': 'nosniff',
  };
  return { status: 200, headers, body: method === 'HEAD' ? null : createReadStream(file) };
}
