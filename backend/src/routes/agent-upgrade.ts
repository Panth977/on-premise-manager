/**
 * §21 #90 agent upgrades from the cloud console (never automatic):
 *   - the pinned release (deps/manifest.json agent { version, notes }, else OPM_AGENT_VERSION / agent/latest)
 *   - agents/{site}.upgradeRequest (one queued request per site; CloudView.agentUpgrades; expires after 24 h)
 *   - deployments.agentUpgradeRun ("Upgrade all": one site at a time in siteSlug order, the first failure stops it)
 *   - heartbeat `upgrade` progress → clears the request, advances / stops the run (settleUpgrade)
 */
import {
  AGENT_UPGRADE_REQUEST_TTL_SEC,
  agentUpgradeAvailable,
  compareAgentVersions,
  GUI_UPGRADE_MIN_VERSION,
  supportsGuiUpgrade,
  ulid,
  type AgentDoc,
  type AgentReleaseInfo,
  type AgentUpgradePhase,
  type AgentUpgradeProgress,
  type AgentUpgradeRequest,
  type AgentUpgradeRun,
  type DepsManifest,
  type SiteAgentUpgradeView,
} from '@opm/shared';
import { col, db, nowIso, type Tx } from '../core/fire.js';
import { ApiError, E } from '../core/errors.js';
import { loadDep, needDep } from '../core/perm.js';
import { asObject, onlyKeys } from '../core/util.js';
import { decodeDeployment, type StoredDeployment } from '../core/codec.js';
import { CloudLog } from '../core/activities.js';
import { loadDepsManifest, resolveAgentVersion } from './install.js';
import type { Ctx, Handlers } from '../core/router.js';
import type { DocRef } from '../driver/index.js';

export const RELEASE_NOTES_MAX = 8000;
const TERMINAL: AgentUpgradePhase[] = ['done', 'failed', 'rolled-back'];
const PHASES: AgentUpgradePhase[] = ['queued', 'download', 'verify', 'swap', 'restart', 'health', 'done', 'failed', 'rolled-back'];
export const EXPIRED_ERROR = 'expired: the agent did not pick it up within 24 h';

/** The text Setup / Status show (and 422 details.hint) for agents below GUI_UPGRADE_MIN_VERSION. */
export function upgradeHint(installed: string | null | undefined): string {
  return `Agent ${installed || '(unknown)'} cannot upgrade itself from the GUI: rerun the install command of this site once (it stays valid for upgrades) to reach agent >= ${GUI_UPGRADE_MIN_VERSION}; later upgrades are one click here.`;
}

/**
 * The pinned agent release (one helper for agentRelease, the poll view, Setup and Status): deps/manifest.json agent
 * { version, notes }, else OPM_AGENT_VERSION / agent/latest (notes null); null = nothing pinned / uploaded.
 * `m` = an already loaded manifest (undefined → loaded here).
 */
export async function agentReleaseInfo(m?: DepsManifest | null): Promise<AgentReleaseInfo | null> {
  const man = m === undefined ? await loadDepsManifest() : m;
  if (man?.agent?.version) {
    const n = typeof man.agent.notes === 'string' && man.agent.notes.trim() ? man.agent.notes.slice(0, RELEASE_NOTES_MAX) : null;
    return { version: man.agent.version, notes: n };
  }
  try {
    return { version: await resolveAgentVersion(null), notes: null };
  } catch {
    return null;
  }
}

const ms = (ts: string | null | undefined) => (ts ? Date.parse(ts) : NaN);
/** the agent reported a phase for this request (it started it) */
export const started = (a: Pick<AgentDoc, 'upgrade'>, req: AgentUpgradeRequest) => !!a.upgrade && a.upgrade.requestId === req.requestId;
/** the queued request of a site, null when absent or expired (and not started by the agent) */
export function liveRequest(a: Pick<AgentDoc, 'upgradeRequest' | 'upgrade'>, now = Date.now()): AgentUpgradeRequest | null {
  const r = a.upgradeRequest;
  if (!r || typeof r !== 'object' || !r.requestId) return null;
  if (ms(r.expiresAt) <= now && !started(a, r)) return null;
  return r;
}
const expiredRequest = (a: Pick<AgentDoc, 'upgradeRequest' | 'upgrade'>, now: number): AgentUpgradeRequest | null => {
  const r = a.upgradeRequest;
  return r && r.requestId && ms(r.expiresAt) <= now && !started(a, r) ? r : null;
};

/** Setup / Status per-site view (null when not enrolled). */
export function upgradeViewOf(a: AgentDoc | undefined | null, release: AgentReleaseInfo | null, now = Date.now()): SiteAgentUpgradeView | null {
  if (!a) return null;
  const installed = a.agentVersion ?? '';
  const latest = release?.version ?? null;
  const supported = supportsGuiUpgrade(installed);
  return {
    installed,
    latest,
    available: agentUpgradeAvailable(installed, latest),
    supported,
    hint: supported ? null : upgradeHint(installed),
    request: liveRequest(a, now),
    progress: a.upgrade ?? null,
  };
}

/** CloudView.agentUpgrades: unexpired requests of every site (each agent runs only its own). */
export function liveRequests(agents: AgentDoc[], now = Date.now()): AgentUpgradeRequest[] {
  return agents
    .map((a) => liveRequest(a, now))
    .filter((r): r is AgentUpgradeRequest => !!r)
    .sort((x, y) => (x.siteSlug < y.siteSlug ? -1 : 1));
}

// ───────────────────────────────────────── heartbeat `upgrade` (validated loosely: a bad field never breaks heartbeats beyond the essentials)
const str = (v: unknown, max: number): string | null => (typeof v === 'string' ? v.slice(0, max) : null);
const num = (v: unknown): number | null => (typeof v === 'number' && Number.isFinite(v) ? v : null);
export function upgradeProgressError(v: unknown): string | null {
  if (v === undefined || v === null) return null;
  if (typeof v !== 'object' || Array.isArray(v)) return 'upgrade must be an object or null';
  const o = v as Record<string, unknown>;
  if (typeof o.upgradeId !== 'string' || !o.upgradeId || o.upgradeId.length > 64) return 'upgrade.upgradeId must be a string (1–64)';
  if (!PHASES.includes(o.phase as AgentUpgradePhase)) return `upgrade.phase must be ${PHASES.join(' | ')}`;
  if (o.requestId !== undefined && o.requestId !== null && (typeof o.requestId !== 'string' || o.requestId.length > 64)) return 'upgrade.requestId must be null or a string ≤ 64';
  if (o.runId !== undefined && o.runId !== null && (typeof o.runId !== 'string' || o.runId.length > 64)) return 'upgrade.runId must be null or a string ≤ 64';
  if (JSON.stringify(o).length > 16 * 1024) return 'upgrade is larger than 16 KiB';
  return null;
}
/** the stored copy (known fields only; siteSlug = the signing site) */
export function cleanProgress(v: Record<string, unknown>, siteSlug: string): AgentUpgradeProgress {
  const via = ['gui', 'cloud', 'cli', 'mesh'].includes(v.via as string) ? (v.via as AgentUpgradeProgress['via']) : 'cli';
  const ver = v.verification && typeof v.verification === 'object' && !Array.isArray(v.verification) ? (v.verification as Record<string, unknown>) : null;
  const tri = (x: unknown) => (typeof x === 'boolean' ? x : null);
  return {
    upgradeId: v.upgradeId as string,
    requestId: str(v.requestId, 64),
    runId: str(v.runId, 64),
    via,
    siteSlug,
    from: str(v.from, 64) ?? '',
    to: str(v.to, 64) ?? '',
    phase: v.phase as AgentUpgradePhase,
    progress: num(v.progress),
    bytesDone: num(v.bytesDone) ?? 0,
    bytesTotal: num(v.bytesTotal),
    verification: ver
      ? {
          version: str(ver.version, 64) ?? '',
          sha256: str(ver.sha256, 128) ?? '',
          signatureOk: ver.signatureOk === true,
          publicKeySource: (['agent.json', 'cloud', 'flag'].includes(ver.publicKeySource as string) ? ver.publicKeySource : 'cloud') as 'agent.json' | 'cloud' | 'flag',
          publicKeyFingerprint: str(ver.publicKeyFingerprint, 128) ?? '',
          sha256Ok: tri(ver.sha256Ok),
          selfTestOk: tri(ver.selfTestOk),
          error: str(ver.error, 2000),
          checkedAt: str(ver.checkedAt, 64) ?? '',
        }
      : null,
    error: str(v.error, 2000),
    actor: str(v.actor, 320),
    startedAt: str(v.startedAt, 64) ?? '',
    updatedAt: str(v.updatedAt, 64) ?? nowIso(),
    finishedAt: str(v.finishedAt, 64),
  };
}

// ───────────────────────────────────────── run helpers (pure: they return the new run + the request to queue)
type RunSite = AgentUpgradeRun['sites'][number];
const copyRun = (r: AgentUpgradeRun): AgentUpgradeRun => ({ ...r, sites: r.sites.map((s) => ({ ...s })) });
function newRequest(siteSlug: string, version: string, by: { email: string; uid: string }, runId: string | null, now: number): AgentUpgradeRequest {
  return {
    requestId: ulid(now),
    siteSlug,
    version,
    requestedBy: by.email,
    requestedByUid: by.uid,
    requestedAt: new Date(now).toISOString(),
    expiresAt: new Date(now + AGENT_UPGRADE_REQUEST_TTL_SEC * 1000).toISOString(),
    runId,
  };
}
function finish(run: AgentUpgradeRun, status: AgentUpgradeRun['status'], at: string) {
  run.status = status;
  run.finishedAt = at;
  for (const s of run.sites) if (s.status === 'pending') Object.assign(s, { status: 'cancelled', at });
}
/** Why a site cannot be upgraded by the run now (null = it can). */
function skipReason(a: AgentDoc | undefined, version: string): { status: 'skipped'; error: string } | null {
  if (!a) return { status: 'skipped', error: 'not enrolled' };
  if (!supportsGuiUpgrade(a.agentVersion)) return { status: 'skipped', error: upgradeHint(a.agentVersion) };
  if (compareAgentVersions(a.agentVersion, version) >= 0) return { status: 'skipped', error: `already on ${a.agentVersion}` };
  return null;
}
/** Queue the next pending site (siteSlug order); sites that cannot be upgraded any more are skipped; none left → done. */
function queueNext(run: AgentUpgradeRun, agents: Map<string, AgentDoc>, by: { email: string; uid: string }, now: number): AgentUpgradeRequest | null {
  const at = new Date(now).toISOString();
  for (const s of run.sites) {
    if (s.status !== 'pending') continue;
    const a = agents.get(s.siteSlug);
    const why = skipReason(a, run.version);
    if (why) {
      Object.assign(s, { status: why.status, error: why.error, at });
      continue;
    }
    const req = newRequest(s.siteSlug, run.version, by, run.runId, now);
    Object.assign(s, { status: 'requested', requestId: req.requestId, from: a!.agentVersion, at, error: null });
    return req;
  }
  finish(run, 'done', at);
  return null;
}

/** Expired, unstarted requests of the run's current site → that site failed, the run failed (remaining cancelled). */
export function applyRunExpiry(run: AgentUpgradeRun | null | undefined, agents: Map<string, AgentDoc>, now = Date.now()): { run: AgentUpgradeRun | null; changed: boolean } {
  if (!run || run.status !== 'running') return { run: run ?? null, changed: false };
  const out = copyRun(run);
  let changed = false;
  for (const s of out.sites) {
    if (s.status !== 'requested' || !s.requestId) continue;
    const a = agents.get(s.siteSlug);
    const r = a?.upgradeRequest;
    const exp = a && r && r.requestId === s.requestId ? expiredRequest(a, now) : null;
    if (!exp) continue;
    const at = new Date(now).toISOString();
    Object.assign(s, { status: 'failed', error: EXPIRED_ERROR, at });
    finish(out, 'failed', at);
    changed = true;
  }
  return { run: out, changed };
}

// ───────────────────────────────────────── settle on heartbeat (and expiry)
/**
 * After a heartbeat of a site that has an upgradeRequest: clear it when the agent's progress for that requestId is
 * done / failed / rolled-back, when the reported agentVersion reached the requested version, or when it expired
 * unstarted; advance / stop the run. One transaction (agent + deployment [+ all agents when a run advances]) — a second
 * heartbeat finds no request any more and does nothing.
 */
export async function settleUpgrade(deploymentId: string, siteSlug: string): Promise<void> {
  const depRef = col.deployments().doc(deploymentId);
  const aRef = col.agents(deploymentId).doc(siteSlug);
  await db().runTransaction(async (tx) => {
    const [as, ds] = await Promise.all([tx.get(aRef), tx.get(depRef)]);
    if (!as.exists || !ds.exists) return;
    const a = as.data() as AgentDoc;
    const req = a.upgradeRequest;
    if (!req || !req.requestId) return;
    const now = Date.now();
    const at = new Date(now).toISOString();
    const p = a.upgrade && a.upgrade.requestId === req.requestId ? a.upgrade : null;
    let outcome: 'done' | 'failed' | 'expired' | null = null;
    let error: string | null = null;
    if (p && TERMINAL.includes(p.phase)) {
      outcome = p.phase === 'done' ? 'done' : 'failed';
      error = p.phase === 'done' ? null : `${p.phase}${p.error ? `: ${p.error}` : ''}`.slice(0, 2000);
    } else if (compareAgentVersions(a.agentVersion, req.version) >= 0) outcome = 'done';
    else if (!p && ms(req.expiresAt) <= now) {
      outcome = 'expired';
      error = EXPIRED_ERROR;
    }
    const dep = decodeDeployment(ds.data()!);
    const run = dep.agentUpgradeRun && req.runId && dep.agentUpgradeRun.runId === req.runId ? copyRun(dep.agentUpgradeRun) : null;
    const site = run?.sites.find((s) => s.requestId === req.requestId) ?? null;
    if (!outcome) {
      // the agent started it: run site requested → running
      if (p && run && site && site.status === 'requested') {
        Object.assign(site, { status: 'running', at });
        tx.update(depRef, { agentUpgradeRun: run });
      }
      return;
    }
    const agents = new Map<string, AgentDoc>();
    if (run && run.status === 'running' && outcome === 'done') for (const d of (await tx.get(col.agents(deploymentId))).docs) agents.set(d.id, d.data() as AgentDoc);
    agents.set(siteSlug, { ...a, upgradeRequest: null });
    const log = new CloudLog(deploymentId, dep);
    const writes: { ref: DocRef; data: Record<string, unknown> }[] = [{ ref: aRef, data: { upgradeRequest: null } }];
    if (outcome === 'expired') log.add(tx, { type: 'agent.upgrade-cancelled', summary: `Agent upgrade request for site ${siteSlug} expired (the agent did not pick it up within 24 h)`, data: { requestId: req.requestId, siteSlug, ...(req.runId ? { runId: req.runId } : {}), reason: 'expired' }, actor: null, origin: 'system' });
    if (run && site) {
      Object.assign(site, { status: outcome === 'done' ? 'done' : 'failed', error, at });
      if (run.status === 'running') {
        if (outcome !== 'done') finish(run, 'failed', at);
        else {
          const next = queueNext(run, agents, { email: req.requestedBy, uid: req.requestedByUid }, now);
          if (next) {
            writes.push({ ref: col.agents(deploymentId).doc(next.siteSlug), data: { upgradeRequest: next } });
            log.add(tx, { type: 'agent.upgrade-requested', summary: `Agent upgrade of site ${next.siteSlug} to ${next.version} queued (upgrade all, after ${siteSlug})`, data: { requestId: next.requestId, siteSlug: next.siteSlug, version: next.version, runId: run.runId }, actor: req.requestedByUid });
          }
        }
      }
    }
    for (const w of writes) tx.update(w.ref, w.data);
    tx.update(depRef, { ...log.depUpdate(), agentEpoch: dep.agentEpoch + 1, ...(run ? { agentUpgradeRun: run } : {}) });
  });
}

/** daily: clear expired unstarted requests (activity agent.upgrade-cancelled reason expired) and fail their runs. */
export async function expireAgentUpgrades(deploymentId: string, now = Date.now()): Promise<number> {
  const depRef = col.deployments().doc(deploymentId);
  return db().runTransaction(async (tx) => {
    const [ds, as] = await Promise.all([tx.get(depRef), tx.get(col.agents(deploymentId))]);
    if (!ds.exists) return 0;
    const dep = decodeDeployment(ds.data()!);
    const agents = new Map(as.docs.map((d) => [d.id, d.data() as AgentDoc]));
    const { run, changed } = applyRunExpiry(dep.agentUpgradeRun, agents, now);
    const log = new CloudLog(deploymentId, dep);
    let n = 0;
    for (const [slug, a] of agents) {
      const r = expiredRequest(a, now);
      if (!r) continue;
      tx.update(col.agents(deploymentId).doc(slug), { upgradeRequest: null });
      log.add(tx, { type: 'agent.upgrade-cancelled', summary: `Agent upgrade request for site ${slug} expired (the agent did not pick it up within 24 h)`, data: { requestId: r.requestId, siteSlug: slug, ...(r.runId ? { runId: r.runId } : {}), reason: 'expired' }, actor: null, origin: 'system' });
      n++;
    }
    if (n || changed) tx.update(depRef, { ...log.depUpdate(), agentEpoch: dep.agentEpoch + 1, ...(changed ? { agentUpgradeRun: run } : {}) });
    return n;
  });
}

// ───────────────────────────────────────── human endpoints
function by(c: Ctx) {
  return { email: c.account!.email || c.account!.uid, uid: c.account!.uid };
}
async function pinFor(c: Ctx, version: unknown): Promise<AgentReleaseInfo> {
  if (typeof version !== 'string' || !version) throw E.bad('version', 'V_REQUIRED', 'version (the pinned agent version you saw) is required');
  const rel = await agentReleaseInfo();
  if (!rel) throw E.notFound('no agent release is pinned (upload the agent + deps manifest first)');
  if (rel.version !== version) throw E.stale(`the pinned agent release moved to ${rel.version}; reload and request again`, { pinned: rel.version, requested: version });
  void c;
  return rel;
}
const invalidHint = (message: string, hint: string | null, path = '') => new ApiError('invalid', message, { errors: [{ path, code: 'V_STATE', message, severity: 'error' }], ...(hint ? { hint } : {}) });
const runRunning = (dep: StoredDeployment, agents: Map<string, AgentDoc>, now: number) => {
  const { run, changed } = applyRunExpiry(dep.agentUpgradeRun, agents, now);
  return { run, changed, running: run?.status === 'running' };
};
async function agentsIn(tx: Tx, deploymentId: string) {
  return new Map((await tx.get(col.agents(deploymentId))).docs.map((d) => [d.id, d.data() as AgentDoc]));
}

export const agentUpgradeHandlers: Pick<Handlers, 'requestAgentUpgrade' | 'cancelAgentUpgrade' | 'upgradeAllAgents' | 'cancelAgentUpgradeRun'> = {
  async requestAgentUpgrade(c) {
    const dep0 = await loadDep(c);
    needDep(c, dep0, 'deployment.setup', 'write');
    const slug = c.params.siteSlug!;
    const b = asObject(c.json());
    onlyKeys(b, ['version', 'confirm']);
    c.audit = { data: { siteSlug: slug, version: typeof b.version === 'string' ? b.version : null } };
    if (b.confirm !== slug) throw E.bad('confirm', 'V_FORMAT', 'confirm must equal the site slug');
    const rel = await pinFor(c, b.version);
    const depRef = col.deployments().doc(dep0.deploymentId);
    const aRef = col.agents(dep0.deploymentId).doc(slug);
    const req = await db().runTransaction(async (tx) => {
      const [ds, agents] = await Promise.all([tx.get(depRef), agentsIn(tx, dep0.deploymentId)]);
      const dep = decodeDeployment(ds.data()!);
      const now = Date.now();
      const a = agents.get(slug);
      if (!a) throw invalidHint(`site ${slug} is not enrolled`, null);
      if (!supportsGuiUpgrade(a.agentVersion)) throw invalidHint(`agent ${a.agentVersion} of site ${slug} cannot upgrade itself (needs >= ${GUI_UPGRADE_MIN_VERSION})`, upgradeHint(a.agentVersion));
      if (compareAgentVersions(a.agentVersion, rel.version) >= 0) throw invalidHint(`site ${slug} already runs agent ${a.agentVersion} (pinned ${rel.version})`, null);
      const live = liveRequest(a, now);
      if (live) throw E.conflict(`an upgrade of site ${slug} to ${live.version} is already queued (requested by ${live.requestedBy})`, { request: live });
      const { run, changed, running } = runRunning(dep, agents, now);
      if (running) throw E.conflict('an "Upgrade all" run is going; cancel it first', { runId: run!.runId });
      const p = a.upgrade;
      if (p && !TERMINAL.includes(p.phase) && p.phase !== 'queued' && now - ms(p.updatedAt) < 15 * 60_000) throw E.conflict(`an upgrade is running on site ${slug} (${p.phase})`, { progress: p });
      const r = newRequest(slug, rel.version, by(c), null, now);
      const log = new CloudLog(dep.deploymentId, dep);
      log.add(tx, { type: 'agent.upgrade-requested', summary: `Agent upgrade of site ${slug} to ${r.version} requested (from ${a.agentVersion})`, data: { requestId: r.requestId, siteSlug: slug, version: r.version, from: a.agentVersion }, actor: c.account!.uid });
      tx.update(aRef, { upgradeRequest: r });
      tx.update(depRef, { ...log.depUpdate(), agentEpoch: dep.agentEpoch + 1, ...(changed ? { agentUpgradeRun: run } : {}) });
      return r;
    });
    c.audit = { data: { siteSlug: slug, version: req.version, requestId: req.requestId } };
    return { request: req };
  },

  async cancelAgentUpgrade(c) {
    const dep0 = await loadDep(c);
    needDep(c, dep0, 'deployment.setup', 'write');
    const slug = c.params.siteSlug!;
    c.audit = { data: { siteSlug: slug } };
    const depRef = col.deployments().doc(dep0.deploymentId);
    const aRef = col.agents(dep0.deploymentId).doc(slug);
    const req = await db().runTransaction(async (tx) => {
      const [ds, as] = await Promise.all([tx.get(depRef), tx.get(aRef)]);
      if (!as.exists) throw E.notFound(`site ${slug} is not enrolled`);
      const a = as.data() as AgentDoc;
      const r = liveRequest(a);
      if (!r) throw E.notFound(`no queued agent upgrade request for site ${slug}`);
      if (started(a, r)) throw E.conflict(`the agent already started this upgrade (${a.upgrade!.phase}); it finishes or rolls back by itself`, { progress: a.upgrade });
      const dep = decodeDeployment(ds.data()!);
      const at = nowIso();
      // the current request of a running "Upgrade all" run: cancelling it cancels the run
      let run: AgentUpgradeRun | null = null;
      if (r.runId && dep.agentUpgradeRun?.runId === r.runId && dep.agentUpgradeRun.status === 'running') {
        run = copyRun(dep.agentUpgradeRun);
        const s = run.sites.find((x) => x.requestId === r.requestId);
        if (s) Object.assign(s, { status: 'cancelled', at } satisfies Partial<RunSite>);
        finish(run, 'cancelled', at);
      }
      const log = new CloudLog(dep.deploymentId, dep);
      log.add(tx, { type: 'agent.upgrade-cancelled', summary: `Agent upgrade request for site ${slug} cancelled${run ? ' (the upgrade-all run stops)' : ''}`, data: { requestId: r.requestId, siteSlug: slug, ...(r.runId ? { runId: r.runId } : {}) }, actor: c.account!.uid });
      tx.update(aRef, { upgradeRequest: null });
      tx.update(depRef, { ...log.depUpdate(), agentEpoch: dep.agentEpoch + 1, ...(run ? { agentUpgradeRun: run } : {}) });
      return r;
    });
    c.audit = { data: { siteSlug: slug, requestId: req.requestId, ...(req.runId ? { runId: req.runId } : {}) } };
    return { cancelled: true };
  },

  async upgradeAllAgents(c) {
    const dep0 = await loadDep(c);
    needDep(c, dep0, 'deployment.setup', 'write');
    const b = asObject(c.json());
    onlyKeys(b, ['version', 'confirm']);
    c.audit = { data: { version: typeof b.version === 'string' ? b.version : null } };
    if (b.confirm !== 'UPGRADE') throw E.bad('confirm', 'V_FORMAT', 'confirm must be UPGRADE');
    const rel = await pinFor(c, b.version);
    const depRef = col.deployments().doc(dep0.deploymentId);
    const run = await db().runTransaction(async (tx) => {
      const [ds, agents] = await Promise.all([tx.get(depRef), agentsIn(tx, dep0.deploymentId)]);
      const dep = decodeDeployment(ds.data()!);
      const now = Date.now();
      const at = new Date(now).toISOString();
      const cur = runRunning(dep, agents, now);
      if (cur.running) throw E.conflict('an "Upgrade all" run is already going', { runId: cur.run!.runId });
      for (const a of agents.values()) {
        const live = liveRequest(a, now);
        if (live) throw E.conflict(`site ${a.siteSlug} has a queued upgrade request (to ${live.version}); wait for it or cancel it first`, { request: live });
      }
      // enrolled sites below the pin + not-enrolled sites of the draft (skipped), siteSlug order
      const slugs = [...new Set([...agents.keys(), ...dep.draft.sites.map((s) => s.siteSlug)])].sort();
      const sites: RunSite[] = [];
      for (const slug of slugs) {
        const a = agents.get(slug);
        if (a && !agentUpgradeAvailable(a.agentVersion, rel.version)) continue; // up to date
        const why = skipReason(a, rel.version);
        sites.push({ siteSlug: slug, status: why ? 'skipped' : 'pending', from: a?.agentVersion ?? null, requestId: null, error: why?.error ?? null, at: why ? at : null });
      }
      const pending = sites.filter((s) => s.status === 'pending');
      if (!pending.length) {
        const old = sites.filter((s) => s.status === 'skipped' && agents.has(s.siteSlug));
        throw invalidHint(old.length ? `no site can upgrade from the cloud: ${old.map((s) => `${s.siteSlug} (${s.from})`).join(', ')} run agents below ${GUI_UPGRADE_MIN_VERSION}` : `every enrolled site already runs agent ${rel.version}`, old.length ? upgradeHint(old[0]!.from) : null);
      }
      const r: AgentUpgradeRun = { runId: ulid(now), origin: 'cloud', coordinator: null, version: rel.version, requestedBy: by(c).email, requestedAt: at, status: 'running', sites, finishedAt: null };
      const first = queueNext(r, agents, by(c), now)!;
      const log = new CloudLog(dep.deploymentId, dep);
      log.add(tx, { type: 'agent.upgrade-requested', summary: `Upgrade all agents to ${rel.version}: ${pending.length} site(s), one at a time (first ${first.siteSlug})`, data: { runId: r.runId, version: rel.version, sites: pending.map((s) => s.siteSlug), skipped: sites.filter((s) => s.status === 'skipped').map((s) => s.siteSlug), requestId: first.requestId, siteSlug: first.siteSlug }, actor: c.account!.uid });
      tx.update(col.agents(dep.deploymentId).doc(first.siteSlug), { upgradeRequest: first });
      tx.update(depRef, { ...log.depUpdate(), agentEpoch: dep.agentEpoch + 1, agentUpgradeRun: r });
      return r;
    });
    c.audit = { data: { runId: run.runId, version: run.version, sites: run.sites.filter((s) => s.status !== 'skipped').map((s) => s.siteSlug) } };
    return { run };
  },

  async cancelAgentUpgradeRun(c) {
    const dep0 = await loadDep(c);
    needDep(c, dep0, 'deployment.setup', 'write');
    const depRef = col.deployments().doc(dep0.deploymentId);
    const run = await db().runTransaction(async (tx) => {
      const [ds, agents] = await Promise.all([tx.get(depRef), agentsIn(tx, dep0.deploymentId)]);
      const dep = decodeDeployment(ds.data()!);
      if (!dep.agentUpgradeRun) throw E.notFound('no "Upgrade all" run');
      const now = Date.now();
      const cur = runRunning(dep, agents, now);
      if (!cur.running) throw E.conflict(`the "Upgrade all" run is ${cur.run!.status}`, { run: cur.run });
      const r = copyRun(cur.run!);
      const at = new Date(now).toISOString();
      let removed: AgentUpgradeRequest | null = null;
      for (const s of r.sites) {
        if (s.status !== 'requested' || !s.requestId) continue;
        const a = agents.get(s.siteSlug);
        const q = a?.upgradeRequest;
        if (a && q && q.requestId === s.requestId && !started(a, q)) {
          tx.update(col.agents(dep.deploymentId).doc(s.siteSlug), { upgradeRequest: null });
          Object.assign(s, { status: 'cancelled', at });
          removed = q;
        } else if (a && q && started(a, q)) Object.assign(s, { status: 'running', at }); // finishes by itself
      }
      finish(r, 'cancelled', at);
      const log = new CloudLog(dep.deploymentId, dep);
      log.add(tx, { type: 'agent.upgrade-cancelled', summary: `Upgrade all agents (${r.version}) cancelled${removed ? ` (queued request of site ${removed.siteSlug} removed)` : ''}`, data: { runId: r.runId, ...(removed ? { requestId: removed.requestId, siteSlug: removed.siteSlug } : {}) }, actor: c.account!.uid });
      tx.update(depRef, { ...log.depUpdate(), agentEpoch: dep.agentEpoch + 1, agentUpgradeRun: r });
      return r;
    });
    c.audit = { data: { runId: run.runId } };
    return { run };
  },
};
