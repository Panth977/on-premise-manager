/**
 * §21 #125 port tunnels — API side. The console lists / adds / removes a deployment's tunnels; agents long-poll for new
 * connections and move the bytes (core/port-tunnels.ts holds the listeners and the queues). VM cloud only: the
 * listeners live in this process; on Firebase every call answers unavailable.
 *
 * Stored: deployments/{d}/portTunnels/{tunnelId} (the tunnel) and tunnelPorts/{port} ({deploymentId, tunnelId}: one
 * tunnel per public port across every deployment, made in the same transaction).
 */
import { PORT_TUNNEL_LIMITS, ulid, validatePortTunnel, portTunnelTargetText, type CreatePortTunnelRequest, type PortTunnelCloudItem, type PortTunnelsResponse } from '@opm/shared';
import { col, db, nowIso } from '../core/fire.js';
import { E } from '../core/errors.js';
import { loadDep, needDep } from '../core/perm.js';
import { decodeDeployment } from '../core/codec.js';
import { CloudLog } from '../core/activities.js';
import { env } from '../core/env.js';
import { adapterKind } from '../ports/index.js';
import type { Handlers } from '../core/router.js';
import { agentTunnels, configuredPorts, freePorts, hubRunning, listen, next, portTaken, recv, send, tunnelStatus, unlisten, type StoredPortTunnel } from '../core/port-tunnels.js';

const tunnelPortDoc = (port: number) => db().collection('tunnelPorts').doc(String(port));
const tunnelsCol = (d: string) => col.deployments().doc(d).collection('portTunnels');

/** why tunnels do not work on this cloud, or null */
function unavailableReason(): string | null {
  if (adapterKind() !== 'local' || !hubRunning()) return 'Port tunnels need the single-process cloud on a VM (opm-cloud); this cloud cannot listen on ports.';
  if (!configuredPorts().length) return 'No public ports: set TUNNEL_FREE_PORTS on the cloud (e.g. 2000-2100,3000) and publish those ports from the cloud container to its VM.';
  return null;
}
/** the host the tunnel addresses name: TUNNEL_PUBLIC_HOST (a name that reaches the VM directly — a proxy such as a CDN in
 *  front of the public URL does not carry raw TCP on other ports), else the public URL's host */
const cloudHost = () => {
  const h = (process.env.TUNNEL_PUBLIC_HOST ?? '').trim();
  if (h) return h;
  try {
    return new URL(env.apiBase).hostname;
  } catch {
    return 'localhost';
  }
};
const waitOf = (v: string | null) => {
  const w = Number(v ?? 25);
  return Number.isFinite(w) ? Math.max(0, Math.min(30, Math.floor(w))) * 1000 : 25_000;
};
const csv = (v: string | null) => (v ?? '').split(',').map((x) => x.trim()).filter(Boolean).slice(0, 500);

async function storedTunnels(deploymentId: string): Promise<StoredPortTunnel[]> {
  return (await tunnelsCol(deploymentId).get()).docs.map((d) => d.data() as StoredPortTunnel).sort((a, b) => a.publicPort - b.publicPort);
}

/** every stored tunnel of every deployment (server start) */
export async function allPortTunnels(): Promise<StoredPortTunnel[]> {
  const out: StoredPortTunnel[] = [];
  for (const p of (await db().collection('tunnelPorts').get()).docs) {
    const { deploymentId, tunnelId } = p.data() as { deploymentId: string; tunnelId: string };
    const t = await tunnelsCol(deploymentId).doc(tunnelId).get();
    if (t.exists) out.push(t.data() as StoredPortTunnel);
  }
  return out;
}

/** CloudView.tunnels (only on a cloud that carries tunnels: the agent then polls) */
export async function portTunnelCloudItems(deploymentId: string): Promise<PortTunnelCloudItem[] | undefined> {
  if (unavailableReason()) return undefined;
  const host = cloudHost();
  return (await storedTunnels(deploymentId)).map(({ deploymentId: _d, ...t }) => ({ ...t, address: `${host}:${t.publicPort}` }));
}

export const portTunnelHandlers: Pick<Handlers, 'listPortTunnels' | 'createPortTunnel' | 'deletePortTunnel' | 'agentPortTunnelNext' | 'agentPortTunnelRecv' | 'agentPortTunnelSend'> = {
  async listPortTunnels(c) {
    const dep = await loadDep(c);
    needDep(c, dep, 'deployment.config', 'read');
    const reason = unavailableReason();
    const host = cloudHost();
    const sites = dep.draft.sites.map((s) => s.siteSlug);
    if (reason && adapterKind() !== 'local') return { available: false, reason, host, freePorts: [], agents: sites.map((siteSlug) => ({ siteSlug, connected: false, enabledOnSite: null })), items: [] } satisfies PortTunnelsResponse;
    const items = (await storedTunnels(dep.deploymentId)).map((t) => tunnelStatus(t, host));
    return { available: !reason, reason, host, freePorts: reason ? [] : freePorts(), agents: agentTunnels(dep.deploymentId, sites), items } satisfies PortTunnelsResponse;
  },

  async createPortTunnel(c) {
    const dep = await loadDep(c);
    needDep(c, dep, 'deployment.config', 'publish');
    const reason = unavailableReason();
    if (reason) throw E.unavailable(reason);
    const body = c.json<CreatePortTunnelRequest>();
    const bad = validatePortTunnel(body);
    if (bad) throw E.bad('body', 'V_FORMAT', bad);
    if (!dep.draft.sites.some((s) => s.siteSlug === body.siteSlug)) throw E.notFound(`site ${body.siteSlug} does not exist`);
    if (!configuredPorts().includes(body.publicPort)) throw E.bad('publicPort', 'V_RANGE', `port ${body.publicPort} is not one of the cloud's tunnel ports (TUNNEL_FREE_PORTS)`);
    if (portTaken(body.publicPort)) throw E.conflict(`port ${body.publicPort} is already used by another tunnel`);
    const a = c.account!;
    const t: StoredPortTunnel = {
      tunnelId: ulid(),
      deploymentId: dep.deploymentId,
      siteSlug: body.siteSlug,
      target: body.target,
      publicPort: body.publicPort,
      label: body.label?.trim() || null,
      createdAt: nowIso(),
      createdBy: a.email,
    };
    const depRef = col.deployments().doc(dep.deploymentId);
    await db().runTransaction(async (tx) => {
      const [ds, ps] = await Promise.all([tx.get(depRef), tx.get(tunnelPortDoc(t.publicPort))]);
      if (ps.exists) throw E.conflict(`port ${t.publicPort} is already used by another tunnel`);
      const d = decodeDeployment(ds.data()!);
      const log = new CloudLog(d.deploymentId, d);
      tx.create(tunnelPortDoc(t.publicPort), { deploymentId: t.deploymentId, tunnelId: t.tunnelId, port: t.publicPort });
      tx.create(tunnelsCol(t.deploymentId).doc(t.tunnelId), t);
      log.add(tx, { type: 'tunnel.created', summary: `Tunnel ${portTunnelTargetText(t.target)} of site ${t.siteSlug} on port ${t.publicPort} added (off until switched on on the site)`, data: { tunnelId: t.tunnelId, siteSlug: t.siteSlug, target: t.target, publicPort: t.publicPort }, actor: a.uid });
      tx.update(depRef, { ...log.depUpdate(), agentEpoch: d.agentEpoch + 1 });
    });
    listen(t);
    // the listener binds asynchronously: give it a moment so the answer shows the real state
    await new Promise((r) => setTimeout(r, 50));
    return tunnelStatus(t, cloudHost());
  },

  async deletePortTunnel(c) {
    const dep = await loadDep(c);
    needDep(c, dep, 'deployment.config', 'publish');
    const ref = tunnelsCol(dep.deploymentId).doc(c.params.tunnelId!);
    const depRef = col.deployments().doc(dep.deploymentId);
    const t = await db().runTransaction(async (tx) => {
      const [ds, ts] = await Promise.all([tx.get(depRef), tx.get(ref)]);
      if (!ts.exists) throw E.notFound('tunnel not found');
      const t = ts.data() as StoredPortTunnel;
      const ps = await tx.get(tunnelPortDoc(t.publicPort));
      const d = decodeDeployment(ds.data()!);
      const log = new CloudLog(d.deploymentId, d);
      tx.delete(ref);
      if (ps.exists && (ps.data() as { tunnelId: string }).tunnelId === t.tunnelId) tx.delete(ps.ref);
      log.add(tx, { type: 'tunnel.deleted', summary: `Tunnel ${portTunnelTargetText(t.target)} of site ${t.siteSlug} on port ${t.publicPort} removed`, data: { tunnelId: t.tunnelId, siteSlug: t.siteSlug, publicPort: t.publicPort }, actor: c.account!.uid });
      tx.update(depRef, { ...log.depUpdate(), agentEpoch: d.agentEpoch + 1 });
      return t;
    });
    if (adapterKind() === 'local') unlisten(t.publicPort);
    return { deleted: true as const };
  },

  async agentPortTunnelNext(c) {
    const { deploymentId, siteSlug } = c.agent!;
    if (unavailableReason()) return { opens: [] };
    const rg = c.query.get('remoteGui');
    const opens = await next(deploymentId, siteSlug, { enabled: csv(c.query.get('enabled')), remoteGui: rg === '1' ? true : rg === '0' ? false : null, reach: csv(c.query.get('reach')) }, waitOf(c.query.get('wait')), c.signal);
    return { opens };
  },

  async agentPortTunnelRecv(c) {
    const { deploymentId, siteSlug } = c.agent!;
    return recv(c.params.connId!, deploymentId, siteSlug, waitOf(c.query.get('wait')), c.signal);
  },

  async agentPortTunnelSend(c) {
    const { deploymentId, siteSlug } = c.agent!;
    if (c.body.length > PORT_TUNNEL_LIMITS.chunkMax * 1.4 + 4096) throw E.tooLarge(`at most ${PORT_TUNNEL_LIMITS.chunkMax} bytes per call`);
    const b = c.json<{ dataB64?: unknown; close?: unknown; error?: unknown }>();
    const data = typeof b.dataB64 === 'string' ? Buffer.from(b.dataB64, 'base64') : Buffer.alloc(0);
    const accepted = await send(c.params.connId!, deploymentId, siteSlug, data, b.close === true, typeof b.error === 'string' ? b.error.slice(0, 500) : null);
    return { accepted };
  },
};
