#!/usr/bin/env node
/**
 * Static server that emulates EXACTLY the routing the Go agent must implement for the embedded GUI (SPEC §7.1):
 *   /                        → 302 /_opm/app            (GUI alias port behaviour)
 *   /_opm  and /_opm/        → 302 /_opm/app
 *   /_opm/assets/**          → the file (hashed names under assets/immutable/ → Cache-Control immutable); missing → 404
 *   /_opm/login, /_opm/app, /_opm/app/**  → index.html (Cache-Control: no-cache)   ← the SPA fallback
 *   anything else under /_opm/ (except /_opm/api/*, /_opm/healthz, /_opm/peer/*) → the agent 404 page (HTML, 404)
 * Pre-compressed .br / .gz siblings are served when the client accepts them.
 * Usage: node tests/serve.mjs [dir=build-mock] [port=4173]
 */
import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';

const dir = path.resolve(process.argv[2] ?? 'build-mock');
const port = Number(process.argv[3] ?? 4173);
const TYPES = { '.html': 'text/html; charset=utf-8', '.js': 'text/javascript; charset=utf-8', '.css': 'text/css; charset=utf-8', '.json': 'application/json', '.svg': 'image/svg+xml', '.png': 'image/png', '.ico': 'image/x-icon', '.woff2': 'font/woff2' };
const AGENT_404 = '<!doctype html><html><head><meta charset="utf-8"><title>OPM agent · 404</title></head><body style="font-family:sans-serif;text-align:center;padding:10vh"><h1>OPM agent · 404</h1><p>This page does not exist.</p><p><a href="/_opm/app">Back to /_opm/app</a></p></body></html>';

function send(req, res, file, cache) {
  const ae = String(req.headers['accept-encoding'] ?? '');
  const type = TYPES[path.extname(file)] ?? 'application/octet-stream';
  let f = file;
  let enc = null;
  if (/\bbr\b/.test(ae) && fs.existsSync(file + '.br')) [f, enc] = [file + '.br', 'br'];
  else if (/\bgzip\b/.test(ae) && fs.existsSync(file + '.gz')) [f, enc] = [file + '.gz', 'gzip'];
  const h = { 'Content-Type': type, 'Cache-Control': cache, Vary: 'Accept-Encoding', 'X-Content-Type-Options': 'nosniff' };
  if (enc) h['Content-Encoding'] = enc;
  res.writeHead(200, h);
  fs.createReadStream(f).pipe(res);
}

http
  .createServer((req, res) => {
    const u = new URL(req.url ?? '/', 'http://x');
    const p = decodeURIComponent(u.pathname);
    if (p === '/' || p === '/_opm' || p === '/_opm/') {
      res.writeHead(302, { Location: '/_opm/app' });
      return res.end();
    }
    if (p.startsWith('/_opm/assets/')) {
      const file = path.join(dir, p.slice('/_opm/'.length));
      if (!file.startsWith(dir) || !fs.existsSync(file) || !fs.statSync(file).isFile()) {
        res.writeHead(404, { 'Content-Type': 'text/plain' });
        return res.end('not found');
      }
      return send(req, res, file, p.startsWith('/_opm/assets/immutable/') ? 'public, max-age=31536000, immutable' : 'no-cache');
    }
    if (p === '/_opm/login' || p === '/_opm/app' || p.startsWith('/_opm/app/')) return send(req, res, path.join(dir, 'index.html'), 'no-cache');
    if (p === '/_opm/healthz') {
      res.writeHead(200, { 'Content-Type': 'text/plain' });
      return res.end('ok');
    }
    if (p.startsWith('/_opm/api/')) {
      res.writeHead(404, { 'Content-Type': 'application/json' });
      return res.end(JSON.stringify({ error: { code: 'not-found', message: 'static test server: no agent API (use VITE_MOCK=1)' } }));
    }
    res.writeHead(404, { 'Content-Type': 'text/html; charset=utf-8' });
    res.end(AGENT_404);
  })
  .listen(port, '127.0.0.1', () => console.log(`serving ${dir} on http://127.0.0.1:${port}/_opm/`));
