/**
 * §21 #118 remote agent GUI — the browser side on the VM cloud: `<cloud>/app/deployments/{id}/remote/{site}/…`.
 * The remote session cookie (set by openRemoteGui) names the person; the request is queued for the site
 * (core/remote-gui.ts) and answered once the site's agent long-polled it and posted the result. The agent GUI build is
 * unchanged: every `/_opm` path of its HTML / JS / CSS and of redirects is re-prefixed here. The agent's own cookies
 * never reach the browser (the agent keeps the person's session itself).
 */
import type http from 'node:http';
import { createHash } from 'node:crypto';
import type { Duplex } from 'node:stream';
import type { RemoteGuiWsFrame } from '@opm/shared';
import { newStream, toSite } from '../core/remote-gui.js';

import { REMOTE_GUI_LIMITS, isRewritableType, parseRemoteGuiPath, remoteGuiPrefix, rewriteRemoteLocation, rewriteRemoteText } from '@opm/shared';
import { forward, page, touchSession, type RemoteResult } from '../core/remote-gui.js';
import { remoteCookieName } from '../routes/remote-gui.js';

/** request headers forwarded to the agent (no cookies / auth of the cloud, no hop-by-hop, no encodings) */
const FORWARD = new Set(['accept', 'accept-language', 'content-type', 'if-none-match', 'if-modified-since', 'range', 'x-requested-with', 'authorization']);
/** response headers never passed back */
const DROP = new Set(['set-cookie', 'content-length', 'content-encoding', 'transfer-encoding', 'connection', 'keep-alive', 'strict-transport-security']);

function cookieOf(header: string | undefined, name: string): string | undefined {
  for (const part of (header ?? '').split(';')) {
    const i = part.indexOf('=');
    if (i > 0 && part.slice(0, i).trim() === name) return part.slice(i + 1).trim();
  }
  return undefined;
}

export interface RemoteHttpOut {
  status: number;
  headers: Record<string, string>;
  body: Buffer;
}

/** null = not a remote GUI path */
export async function serveRemoteGui(req: { method: string; rawUrl: string; path: string; headers: Record<string, string | undefined>; body: () => Promise<Buffer> }): Promise<RemoteHttpOut | null> {
  const t = parseRemoteGuiPath(req.path);
  if (!t) return null;
  const prefix = remoteGuiPrefix(t.deploymentId, t.siteSlug);
  const s = touchSession(cookieOf(req.headers.cookie, remoteCookieName(t.siteSlug)), t.deploymentId, t.siteSlug);
  if (!s) return out(page(401, 'Open the agent GUI from the cloud console', `This link needs a remote session: open the deployment in the cloud console, go to Status and choose "Open agent GUI" next to site ${t.siteSlug}.`));
  // the bare prefix → the site's status page
  if (req.path === prefix || req.path === `${prefix}/`) return { status: 302, headers: { Location: `${prefix}/_opm/app/${t.siteSlug}/status`, 'Cache-Control': 'no-store' }, body: Buffer.alloc(0) };
  if (!t.agentPath.startsWith('/_opm')) return out(page(404, 'Not found', 'Only the agent GUI (/_opm) is served through the cloud.'));
  // an event stream WITHOUT follow ends by itself (the lines in the range, then `end`): carried as one answer. A follow
  // stream never ends: not carried (yet).
  const fol = /[?&]follow=(1|true)(&|$)/.test(req.rawUrl);
  if ((req.headers.accept ?? '').includes('text/event-stream') && fol)
    return out(page(501, 'Not available through the cloud yet', 'Live log follow works only when the agent GUI is opened on the site itself (turn follow off to read the logs here).'));
  let body: Buffer;
  try {
    body = req.method === 'GET' || req.method === 'HEAD' ? Buffer.alloc(0) : await req.body();
  } catch {
    return out(page(413, 'Too large', `A request through the cloud is at most ${REMOTE_GUI_LIMITS.requestBodyMax / 1048576} MiB.`));
  }
  if (body.length > REMOTE_GUI_LIMITS.requestBodyMax) return out(page(413, 'Too large', `A request through the cloud is at most ${REMOTE_GUI_LIMITS.requestBodyMax / 1048576} MiB.`));
  const qi = req.rawUrl.indexOf('?');
  const headers: Record<string, string> = {};
  for (const [k, v] of Object.entries(req.headers)) if (v !== undefined && FORWARD.has(k)) headers[k] = v;
  const r = await forward(t.deploymentId, t.siteSlug, {
    sessionId: s.sessionId,
    method: req.method,
    path: t.agentPath + (qi >= 0 ? req.rawUrl.slice(qi) : ''),
    headers,
    bodyB64: body.length ? body.toString('base64') : null,
    actor: s.actor,
  });
  return out(r, prefix);
}

function out(r: RemoteResult, prefix?: string): RemoteHttpOut {
  const headers: Record<string, string> = {};
  for (const [k, v] of Object.entries(r.headers)) if (!DROP.has(k.toLowerCase())) headers[k] = v;
  let body = r.body;
  if (prefix) {
    const loc = Object.keys(headers).find((k) => k.toLowerCase() === 'location');
    if (loc) headers[loc] = rewriteRemoteLocation(headers[loc]!, prefix);
    const ct = Object.keys(headers).find((k) => k.toLowerCase() === 'content-type');
    if (ct && isRewritableType(headers[ct])) {
      body = Buffer.from(rewriteRemoteText(body.toString('utf8'), prefix), 'utf8');
      // the rewritten text differs per prefix: never reuse it from a shared cache
      delete headers.etag;
      delete headers.ETag;
      headers['Cache-Control'] = 'private, no-cache';
    }
  }
  return { status: r.status, headers, body };
}

/** http.IncomingMessage → Buffer (limit) */
export function readRemoteBody(req: http.IncomingMessage): Promise<Buffer> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = [];
    let n = 0;
    req.on('data', (c: Buffer) => {
      n += c.length;
      if (n > REMOTE_GUI_LIMITS.requestBodyMax) {
        req.destroy();
        reject(new Error('too large'));
      } else chunks.push(c);
    });
    req.on('end', () => resolve(Buffer.concat(chunks)));
    req.on('error', reject);
  });
}

// ───────────────────────── §21 #118 g WebSocket upgrades (terminal, MQTT client): relayed frame by frame

const WS_GUID = '258EAFA5-E914-47DA-95CA-C5AB0DC85B11';
const WS_MAX = 8 * 1024 * 1024;

function wsFrame(op: number, payload: Buffer): Buffer {
  const n = payload.length;
  const head = n < 126 ? Buffer.alloc(2) : n < 65536 ? Buffer.alloc(4) : Buffer.alloc(10);
  head[0] = 0x80 | op;
  if (n < 126) head[1] = n;
  else if (n < 65536) {
    head[1] = 126;
    head.writeUInt16BE(n, 2);
  } else {
    head[1] = 127;
    head.writeBigUInt64BE(BigInt(n), 2);
  }
  return Buffer.concat([head, payload]);
}

/** the 'upgrade' event of the HTTP server: true = it was a remote GUI path (handled) */
export function handleRemoteUpgrade(req: http.IncomingMessage, socket: Duplex, head: Buffer): boolean {
  const rawUrl = req.url || '/';
  const qi = rawUrl.indexOf('?');
  const path = qi >= 0 ? rawUrl.slice(0, qi) : rawUrl;
  const t = parseRemoteGuiPath(path);
  if (!t) return false;
  const deny = (status: number, msg: string) => {
    socket.end(`HTTP/1.1 ${status} ${msg}\r\nConnection: close\r\nContent-Length: 0\r\n\r\n`);
  };
  const headers: Record<string, string | undefined> = {};
  for (const [k, v] of Object.entries(req.headers)) headers[k.toLowerCase()] = Array.isArray(v) ? v.join(', ') : v;
  const s = touchSession(cookieOf(headers.cookie, remoteCookieName(t.siteSlug)), t.deploymentId, t.siteSlug);
  const key = headers['sec-websocket-key'];
  if (!s) return deny(401, 'Unauthorized'), true;
  if (!key || !t.agentPath.startsWith('/_opm')) return deny(400, 'Bad Request'), true;
  const stream = newStream(t.deploymentId, t.siteSlug);
  const fwd: Record<string, string> = {};
  for (const k of ['sec-websocket-protocol', 'user-agent']) if (headers[k]) fwd[k] = headers[k]!;
  let upgraded = false;
  socket.on('error', () => toSite(stream, null));
  socket.on('close', () => toSite(stream, null));
  void forward(t.deploymentId, t.siteSlug, { sessionId: s.sessionId, method: 'GET', path: t.agentPath + (qi >= 0 ? rawUrl.slice(qi) : ''), headers: fwd, bodyB64: null, actor: s.actor, ws: { streamId: stream.streamId } }).then((r) => {
    if (socket.destroyed) return toSite(stream, null);
    if (r.status !== 101) {
      toSite(stream, null);
      return deny(r.status === 401 || r.status === 403 ? r.status : 502, 'Not upgraded');
    }
    const accept = createHash('sha1').update(key + WS_GUID).digest('base64');
    const proto = Object.entries(r.headers).find(([k]) => k.toLowerCase() === 'sec-websocket-protocol')?.[1];
    socket.write(`HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ${accept}\r\n${proto ? `Sec-WebSocket-Protocol: ${proto}\r\n` : ''}\r\n`);
    upgraded = true;
    stream.toBrowser = (f: RemoteGuiWsFrame) => {
      if (socket.destroyed) return;
      const data = Buffer.from(f.dataB64, 'base64');
      if (f.op === 'close') {
        const p = Buffer.alloc(2 + data.length);
        p.writeUInt16BE(f.code ?? 1000, 0);
        data.copy(p, 2);
        socket.end(wsFrame(8, p));
      } else socket.write(wsFrame(f.op === 'text' ? 1 : 2, data));
    };
    // frames from the browser (masked; fragmentation joined; ping answered here)
    let buf = head.length ? Buffer.from(head) : Buffer.alloc(0);
    let parts: Buffer[] = [];
    let partOp = 0;
    const parse = () => {
      for (;;) {
        if (buf.length < 2) return;
        const fin = (buf[0]! & 0x80) !== 0;
        const op = buf[0]! & 0x0f;
        const masked = (buf[1]! & 0x80) !== 0;
        let len = buf[1]! & 0x7f;
        let off = 2;
        if (len === 126) {
          if (buf.length < 4) return;
          len = buf.readUInt16BE(2);
          off = 4;
        } else if (len === 127) {
          if (buf.length < 10) return;
          const big = buf.readBigUInt64BE(2);
          if (big > BigInt(WS_MAX)) return socket.destroy();
          len = Number(big);
          off = 10;
        }
        const mask = masked ? buf.subarray(off, off + 4) : null;
        if (masked) off += 4;
        if (buf.length < off + len) return;
        const payload = Buffer.from(buf.subarray(off, off + len));
        buf = buf.subarray(off + len);
        if (mask) for (let i = 0; i < payload.length; i++) payload[i]! ^= mask[i % 4]!;
        if (op === 9) {
          socket.write(wsFrame(10, payload));
          continue;
        }
        if (op === 10) continue;
        if (op === 8) {
          const code = payload.length >= 2 ? payload.readUInt16BE(0) : 1000;
          toSite(stream, { op: 'close', dataB64: payload.subarray(2).toString('base64'), code });
          toSite(stream, null);
          socket.end(wsFrame(8, payload.subarray(0, 2)));
          return;
        }
        if (op === 1 || op === 2) {
          partOp = op;
          parts = [payload];
        } else if (op === 0) parts.push(payload);
        if (fin && (op <= 2)) {
          const msg = Buffer.concat(parts);
          parts = [];
          if (msg.length > WS_MAX) return socket.destroy();
          toSite(stream, { op: partOp === 1 ? 'text' : 'binary', dataB64: msg.toString('base64') });
        }
      }
    };
    socket.on('data', (d: Buffer) => {
      buf = Buffer.concat([buf, d]);
      parse();
    });
    parse();
  });
  void upgraded;
  return true;
}
