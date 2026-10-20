/**
 * §21 #118 remote agent GUI — the browser side on the VM cloud: `<cloud>/app/deployments/{id}/remote/{site}/…`.
 * The remote session cookie (set by openRemoteGui) names the person; the request is queued for the site
 * (core/remote-gui.ts) and answered once the site's agent long-polled it and posted the result. The agent GUI build is
 * unchanged: every `/_opm` path of its HTML / JS / CSS and of redirects is re-prefixed here. The agent's own cookies
 * never reach the browser (the agent keeps the person's session itself).
 */
import type http from 'node:http';
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
  if ((req.headers.upgrade ?? '').toLowerCase() === 'websocket' || (req.headers.accept ?? '').includes('text/event-stream'))
    return out(page(501, 'Not available through the cloud yet', 'Live streams (terminal, log follow, MQTT client) work only when the agent GUI is opened on the site itself.'));
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
