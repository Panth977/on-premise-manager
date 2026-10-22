/**
 * opm-cloud HTTP front (§21 #108). One port serves:
 *   /v1/*            the same router as the Cloud Function `api` (app.ts handleApi)
 *   /auth/*          built-in sign-in (local adapter)
 *   /_blob/*         short-lived HMAC download URLs (local adapter; replaces GCS v4 signed URLs)
 *   /opm-config.json runtime config of the frontend (which auth provider to use)
 *   /healthz         liveness
 *   /app/deployments/{id}/remote/{site}/…  §21 #118 a site's agent GUI through the cloud (server/remote-gui.ts)
 *   everything else  the built frontend with SPA fallback (= Firebase Hosting)
 */
import http from 'node:http';
import { Readable } from 'node:stream';
import { handleApi } from '../app.js';
import { logger, adapterKind } from '../ports/index.js';
import type { CloudConfig } from './config.js';
import { serveStatic } from './static.js';
import { handleRemoteUpgrade, readRemoteBody, serveRemoteGui } from './remote-gui.js';

export interface LocalHttpHandlers {
  blob(req: { method: string; url: string; headers: Record<string, string | undefined> }): Promise<{ status: number; headers: Record<string, string>; body: Buffer | string | Readable | null }>;
  auth(req: { method: string; url: string; headers: Record<string, string | undefined>; body: Buffer; ip: string }): Promise<{ status: number; headers: Record<string, string>; body: Buffer | string | Readable | null }>;
  authConfig(): Record<string, unknown>;
}

class BodyTooLarge extends Error {}
function readBody(req: http.IncomingMessage, limit: number): Promise<Buffer> {
  return new Promise((resolve, reject) => {
    const declared = Number(req.headers['content-length'] ?? 0);
    if (declared > limit) return reject(new BodyTooLarge());
    const chunks: Buffer[] = [];
    let n = 0;
    req.on('data', (c: Buffer) => {
      n += c.length;
      if (n > limit) {
        req.destroy();
        reject(new BodyTooLarge());
      } else chunks.push(c);
    });
    req.on('end', () => resolve(Buffer.concat(chunks)));
    req.on('error', reject);
  });
}

function send(res: http.ServerResponse, status: number, headers: Record<string, string>, body: Buffer | string | Readable | null, path: string) {
  res.writeHead(status, headers);
  if (body === null || body === undefined) return void res.end();
  if (Buffer.isBuffer(body) || typeof body === 'string') return void res.end(body);
  body.on('error', (e) => {
    logger.warn('stream error', { error: e.message, path });
    res.destroy(e);
  });
  body.pipe(res);
}

export const VERSION = process.env.OPM_CLOUD_VERSION ?? 'dev';

export function createServer(cfg: CloudConfig, local: LocalHttpHandlers | null): http.Server {
  const server = http.createServer(async (req, res) => {
    const rawUrl = req.url || '/';
    const qi = rawUrl.indexOf('?');
    const path = qi >= 0 ? rawUrl.slice(0, qi) : rawUrl;
    const headers: Record<string, string | undefined> = {};
    for (const [k, v] of Object.entries(req.headers)) headers[k.toLowerCase()] = Array.isArray(v) ? v.join(', ') : v;
    const ip = cfg.trustProxy && headers['x-forwarded-for'] ? headers['x-forwarded-for']!.split(',')[0]!.trim() : (req.socket.remoteAddress ?? '');
    try {
      // §21 #118 before /v1/: an agent path may contain anything
      if (path.startsWith('/app/deployments/') && path.includes('/remote/')) {
        const r = await serveRemoteGui({ method: req.method ?? 'GET', rawUrl, path, headers, body: () => readRemoteBody(req) });
        if (r) return send(res, r.status, r.headers, r.body, path);
      }
      if (path.includes('/v1/')) {
        const ac = new AbortController();
        res.on('close', () => {
          if (!res.writableFinished) ac.abort();
        });
        let body: Buffer;
        try {
          body = req.method === 'GET' || req.method === 'HEAD' || req.method === 'OPTIONS' ? Buffer.alloc(0) : await readBody(req, cfg.bodyLimit);
        } catch (e) {
          if (e instanceof BodyTooLarge) return send(res, 413, { 'Content-Type': 'application/json' }, JSON.stringify({ error: { code: 'too-large', message: `request body over ${cfg.bodyLimit} bytes`, details: {} } }), path);
          throw e;
        }
        const out = await handleApi({ method: req.method ?? 'GET', rawUrl, headers, body, ip, signal: ac.signal });
        return send(res, out.status, out.headers, out.body, out.path);
      }
      if (path === '/healthz') return send(res, 200, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' }, JSON.stringify({ ok: true, adapter: adapterKind(), version: VERSION }), path);
      if (path === '/opm-config.json') {
        const conf = cfg.adapter === 'local' && local ? { auth: local.authConfig() } : { auth: { provider: 'firebase' } };
        return send(res, 200, { 'Content-Type': 'application/json; charset=utf-8', 'Cache-Control': 'no-store' }, JSON.stringify(conf), path);
      }
      if (local && path.startsWith('/_blob/')) {
        const out = await local.blob({ method: req.method ?? 'GET', url: rawUrl, headers });
        return send(res, out.status, out.headers, out.body, path);
      }
      if (local && path.startsWith('/auth/')) {
        let body: Buffer;
        try {
          body = req.method === 'GET' || req.method === 'HEAD' ? Buffer.alloc(0) : await readBody(req, 1024 * 1024);
        } catch (e) {
          if (e instanceof BodyTooLarge) return send(res, 413, { 'Content-Type': 'application/json' }, '{"error":{"code":"too-large","message":"body too large"}}', path);
          throw e;
        }
        const out = await local.auth({ method: req.method ?? 'GET', url: rawUrl, headers, body, ip });
        return send(res, out.status, out.headers, out.body, path);
      }
      if (cfg.staticDir) {
        const s = serveStatic(cfg.staticDir, req.method ?? 'GET', rawUrl);
        if (s) return send(res, s.status, s.headers, s.body, path);
      }
      return send(res, 404, { 'Content-Type': 'text/plain; charset=utf-8' }, 'not found', path);
    } catch (e) {
      logger.error('opm-cloud request failed', { path, error: (e as Error)?.stack ?? String(e) });
      if (!res.headersSent) send(res, 500, { 'Content-Type': 'application/json' }, JSON.stringify({ error: { code: 'internal', message: 'internal error', details: {} } }), path);
      else res.destroy();
    }
  });
  // §21 #118 g WebSocket upgrades of the remote agent GUI (anything else: refused)
  server.on('upgrade', (req, socket, head) => {
    try {
      if (!handleRemoteUpgrade(req, socket, head)) socket.end('HTTP/1.1 404 Not Found\r\nConnection: close\r\nContent-Length: 0\r\n\r\n');
    } catch (e) {
      logger.error('remote gui upgrade failed', { error: (e as Error)?.message });
      socket.destroy();
    }
  });
  return server;
}
