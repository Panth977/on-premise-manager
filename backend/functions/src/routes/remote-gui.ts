/**
 * §21 #118 remote agent GUI — API side: the console opens a remote session (cookie + the site listed in the cloud view),
 * agents long-poll for the browser requests and post the answers. The browser side (serving
 * /app/deployments/{id}/remote/{site}/…) is server/remote-gui.ts. Needs ONE cloud process (the VM cloud): the queue
 * lives in memory (core/remote-gui.ts).
 */
import { REMOTE_GUI_HEADERS_HEADER, REMOTE_GUI_LIMITS, REMOTE_GUI_SESSION_SEC, REMOTE_GUI_STATUS_HEADER, RE, remoteGuiPrefix } from '@opm/shared';
import { col, db } from '../core/fire.js';
import { E } from '../core/errors.js';
import { loadDep, needDep } from '../core/perm.js';
import { decodeDeployment } from '../core/codec.js';
import { CloudLog } from '../core/activities.js';
import { Reply } from '../core/reply.js';
import { jsonHeaders, type Handlers } from '../core/router.js';
import { adapterKind } from '../ports/index.js';
import { activeSites, connected, deliver, next, openSession, recv, streamOf, toBrowser } from '../core/remote-gui.js';

/** the cookie of a remote session (one per site; Path = the remote prefix) */
export const remoteCookieName = (siteSlug: string) => `opm_remote_${siteSlug.replace(/-/g, '_')}`;

export const remoteGuiHandlers: Pick<Handlers, 'openRemoteGui' | 'agentRemoteGuiNext' | 'agentRemoteGuiResult' | 'agentRemoteGuiWsRecv' | 'agentRemoteGuiWsSend'> = {
  async openRemoteGui(c) {
    const dep = await loadDep(c);
    needDep(c, dep, 'deployment.config', 'publish');
    const siteSlug = c.params.siteSlug!;
    if (!RE.slug.test(siteSlug) || !dep.draft.sites.some((s) => s.siteSlug === siteSlug)) throw E.notFound(`site ${siteSlug} does not exist`);
    if (adapterKind() !== 'local') throw E.unavailable('the remote agent GUI needs the single-process cloud (opm-cloud on a VM)');
    const a = c.account!;
    const wasActive = activeSites(dep.deploymentId).some((x) => x.siteSlug === siteSlug);
    const s = openSession(dep.deploymentId, siteSlug, { uid: a.uid, email: a.email, name: a.name || null });
    // the cloud view lists the site: its agent starts long-polling (agentEpoch+1 wakes every poll at once)
    const depRef = col.deployments().doc(dep.deploymentId);
    await db().runTransaction(async (tx) => {
      const d = decodeDeployment((await tx.get(depRef)).data()!);
      const log = new CloudLog(d.deploymentId, d);
      log.add(tx, { type: 'remote-gui.opened', summary: `${a.email} opened the agent GUI of site ${siteSlug} through the cloud`, data: { siteSlug, sessionId: s.sessionId.slice(0, 8) }, actor: a.uid });
      tx.update(depRef, { ...log.depUpdate(), ...(wasActive ? {} : { agentEpoch: d.agentEpoch + 1 }) });
    });
    const prefix = remoteGuiPrefix(dep.deploymentId, siteSlug);
    const secure = (c.headers['x-forwarded-proto'] ?? '').toLowerCase() === 'https' || /^https:/i.test(c.headers.origin ?? '');
    const cookie = `${remoteCookieName(siteSlug)}=${s.sessionId}; Path=${prefix}; Max-Age=${REMOTE_GUI_SESSION_SEC}; HttpOnly; SameSite=Lax${secure ? '; Secure' : ''}`;
    const body = { url: `${prefix}/_opm/app/${siteSlug}/status`, expiresAt: new Date(s.expiresAt).toISOString(), connected: connected(dep.deploymentId, siteSlug) };
    return new Reply(200, { ...jsonHeaders, 'Set-Cookie': cookie, 'Cache-Control': 'no-store' }, JSON.stringify(body));
  },

  async agentRemoteGuiNext(c) {
    const { deploymentId, siteSlug } = c.agent!;
    const w = Number(c.query.get('wait') ?? 25);
    const waitMs = Number.isFinite(w) ? Math.max(0, Math.min(30, Math.floor(w))) * 1000 : 25_000;
    return { items: await next(deploymentId, siteSlug, waitMs, c.signal) };
  },

  async agentRemoteGuiWsRecv(c) {
    const { deploymentId, siteSlug } = c.agent!;
    const s = streamOf(c.params.streamId!, deploymentId, siteSlug);
    if (!s) return { frames: [], closed: true };
    const w = Number(c.query.get('wait') ?? 25);
    return recv(s, Number.isFinite(w) ? Math.max(0, Math.min(30, Math.floor(w))) * 1000 : 25_000, c.signal);
  },

  async agentRemoteGuiWsSend(c) {
    const { deploymentId, siteSlug } = c.agent!;
    if (c.body.length > 8 * 1024 * 1024) throw E.tooLarge('at most 8 MiB per call');
    const s = streamOf(c.params.streamId!, deploymentId, siteSlug);
    if (!s || s.closed) return { accepted: false };
    const b = c.json<{ frames?: unknown }>();
    const frames = (Array.isArray(b.frames) ? b.frames : []).filter(
      (f): f is { op: 'text' | 'binary' | 'close'; dataB64: string; code?: number } => !!f && typeof f === 'object' && ['text', 'binary', 'close'].includes((f as { op?: string }).op ?? '') && typeof (f as { dataB64?: unknown }).dataB64 === 'string',
    );
    return { accepted: toBrowser(s, frames) };
  },

  async agentRemoteGuiResult(c) {
    const { deploymentId, siteSlug } = c.agent!;
    if (c.body.length > REMOTE_GUI_LIMITS.responseBodyMax) throw E.tooLarge(`a response is at most ${REMOTE_GUI_LIMITS.responseBodyMax} bytes`);
    const status = Number(c.headers[REMOTE_GUI_STATUS_HEADER] ?? 0);
    if (!Number.isInteger(status) || status < 100 || status > 599) throw E.bad(REMOTE_GUI_STATUS_HEADER, 'V_RANGE', 'status must be 100–599');
    let headers: Record<string, string> = {};
    const raw = c.headers[REMOTE_GUI_HEADERS_HEADER];
    if (raw) {
      try {
        const h = JSON.parse(Buffer.from(raw, 'base64').toString('utf8')) as Record<string, unknown>;
        headers = Object.fromEntries(Object.entries(h).filter(([, v]) => typeof v === 'string')) as Record<string, string>;
      } catch {
        throw E.bad(REMOTE_GUI_HEADERS_HEADER, 'V_FORMAT', 'headers must be base64 JSON');
      }
    }
    return { accepted: deliver(deploymentId, siteSlug, c.params.reqId!, { status, headers, body: c.body }) };
  },
};

/** CloudView.remoteGui (hook for routes/agent.ts) */
export const remoteGuiCloudItems = (deploymentId: string) => (adapterKind() === 'local' ? activeSites(deploymentId) : []);
