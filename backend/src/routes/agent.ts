/** Agent (signed) endpoints: poll, heartbeat, heads, proposals, images, blobs, activities, transfers, uploads, relay. */
import { FieldValue } from '../driver/index.js';
import {
  imageKey,
  RE,
  type Activity,
  type AgentActivitiesResponse,
  type AgentClockSkew,
  type AgentCreateTransferRequest,
  type AgentDoc,
  type AgentTransferStatusRequest,
  type CloudView,
  type HeartbeatRequest,
  type Image,
  type ImageManifestResponse,
  type Proposal,
  type ProposalStatusReport,
  type RelayRequest,
  type Revision,
  type Transfer,
  type TransferMeta,
  type UptimeDay,
} from '@opm/shared';
import { col, db, nowIso, pub } from '../core/fire.js';
import { ApiError, E } from '../core/errors.js';
import { asObject, canonicalSafe, hexOf, isHex64, jcsHash, sameJson, ymd } from '../core/agentutil.js';
import { decodeDeployment, decodeRevision, encodeRevision, type StoredDeployment } from '../core/codec.js';
import { CloudLog, appendCloud, ingestSiteLog } from '../core/activities.js';
import { isTerminal, OPEN_STATUSES } from '../core/catalog.js';
import { paths, noRedirectFor, serveObject, exists } from '../core/storage.js';
import { fetchLayer } from '../core/importer.js';
import { completeUpload, expectedPartSize, loadUpload, newUpload, putPart, uploadView } from '../core/uploads.js';
import { verifyAgent, type AgentAuth } from '../core/auth.js';
import { dispatch, type Ctx, type Handlers } from '../core/router.js';
import { Reply } from '../core/reply.js';
import { env } from '../core/env.js';
import { detectedAddressesError, detectedAddressesOf, peerInfo } from './install.js';
import { proposalMeta } from './deployments.js';
import { newTransfer, validateTransferBody } from './transfers.js';
import { agentReleaseInfo, cleanProgress, liveRequests, settleUpgrade, upgradeProgressError } from './agent-upgrade.js';
import { pushBlobAllowed, storeVolumeSync, volumeContentCloudItems, volumeSyncError } from './volume-content.js';
import { remoteGuiCloudItems } from './remote-gui.js';
import { portTunnelCloudItems } from './port-tunnels.js';
import { filesCloudItems, type StoredFile } from '../core/files.js';
import { allowedCommits, cloudBranchView, legacyHeadMirror, storeHeartbeatBranch } from './agent-branches.js';
import { bcol, encodeCommit } from '../core/branches.js';
import { commitStats, type Commit } from '@opm/shared';

const A = (c: Ctx): AgentAuth => c.agent!;

// ───────────────────────────────────────── §21 #86 heartbeat guiPorts + edge.defaultEdge / bindErrors[].owner
const GUI_KINDS = ['edge', 'gui-alias', 'agent-port'];
const isPort = (n: unknown) => Number.isInteger(n) && (n as number) >= 1 && (n as number) <= 65535;
/** the round-H additions of EdgeReport (the rest is stored as sent, as before) */
export function edgeExtrasError(v: unknown): string | null {
  if (v === undefined || v === null) return null;
  if (typeof v !== 'object' || Array.isArray(v)) return 'edge must be an object or null';
  const e = v as Record<string, unknown>;
  if (e.defaultEdge !== undefined && typeof e.defaultEdge !== 'boolean') return 'edge.defaultEdge must be a boolean';
  if (e.bindErrors !== undefined) {
    if (!Array.isArray(e.bindErrors)) return 'edge.bindErrors must be a list';
    for (const b of e.bindErrors) {
      const o = (b ?? {}) as Record<string, unknown>;
      if (o.owner !== undefined && o.owner !== null && (typeof o.owner !== 'string' || o.owner.length > 255)) return 'edge.bindErrors[].owner must be null or a string ≤ 255';
    }
  }
  return null;
}
export function guiPortsError(v: unknown): string | null {
  if (v === undefined || v === null) return null;
  if (!Array.isArray(v)) return 'guiPorts must be a list';
  if (v.length > 16) return 'guiPorts has at most 16 entries';
  for (const g of v) {
    if (!g || typeof g !== 'object') return 'guiPorts entries must be objects';
    const o = g as Record<string, unknown>;
    if (!isPort(o.port)) return 'guiPorts[].port must be 1–65535';
    if (o.scheme !== 'http' && o.scheme !== 'https') return 'guiPorts[].scheme must be http | https';
    if (!GUI_KINDS.includes(o.kind as string)) return `guiPorts[].kind must be ${GUI_KINDS.join(' | ')}`;
  }
  return null;
}
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const RELAY_MAX = 4 * 1024 * 1024;

// ───────────────────────────────────────── cloud view
export async function buildCloudView(deploymentId: string, dep: StoredDeployment): Promise<CloudView> {
  const [open, recent, transfers, deletedTransfers, agents, release, volumeContent, branch, files, tunnels] = await Promise.all([
    col.proposals(deploymentId).where('status', 'in', [...OPEN_STATUSES]).get(),
    col.proposals(deploymentId).orderBy('revId', 'desc').limit(25).get(),
    col.transfers(deploymentId).where('direction', '==', 'to-site').where('status', '==', 'ready').get(),
    col.transfers(deploymentId).where('direction', '==', 'to-site').where('status', '==', 'expired').get(),
    col.agents(deploymentId).get(),
    agentReleaseInfo().catch(() => null),
    volumeContentCloudItems(deploymentId), // §21 #101
    cloudBranchView(deploymentId, dep), // §21 #104
    filesCloudItems(deploymentId), // §21 #123
    portTunnelCloudItems(deploymentId), // §21 #125
  ]);
  const openP = open.docs.map((d) => d.data() as Proposal);
  const terminal = recent.docs
    .map((d) => d.data() as Proposal)
    .filter((p) => isTerminal(p.status))
    .slice(0, 5);
  const proposals = [...openP, ...terminal].sort((a, b) => (a.revId < b.revId ? 1 : -1)).map(proposalMeta);
  const now = Date.now();
  // transfers never expire: expiresAt is always sent as null, so agents that still honour it never expire one (also
  // the rows written before the expiry was removed)
  const transfersToSite: TransferMeta[] = transfers.docs
    .map((d) => d.data() as Transfer)
    .map((t) => ({ transferId: t.transferId, siteSlug: t.siteSlug, direction: t.direction, fileName: t.fileName, sizeBytes: t.sizeBytes, sha256: t.sha256, status: t.status, createdAt: t.createdAt, createdBy: t.createdBy, note: t.note, expiresAt: null }));
  return {
    fetchedAt: new Date().toISOString(),
    source: 'cloud',
    cursor: String(dep.agentEpoch),
    deploymentId,
    deleted: !!dep.deleted,
    proposals,
    transfersToSite,
    deletedTransfersToSite: deletedTransfers.docs.map((d) => d.data() as Transfer).map((t) => ({ transferId: t.transferId, siteSlug: t.siteSlug })),
    draftHead: dep.head ? { revId: dep.head.revId, seq: dep.head.seq } : null,
    accessKey: { hash: dep.accessKeyHash, version: dep.accessKeyVersion },
    peers: agents.docs.map((d) => peerInfo(d.data() as AgentDoc)).sort((a, b) => (a.siteSlug < b.siteSlug ? -1 : 1)),
    consoleUrl: env.consoleUrl,
    deploymentLabel: dep.label ?? null,
    // §21 #90
    agentRelease: release,
    agentUpgrades: liveRequests(agents.docs.map((d) => d.data() as AgentDoc), now),
    // §21 #101 pending pushes + open snapshot requests (all sites; each agent filters its own)
    volumeContent,
    // §21 #104 the deployment branch head = what the sites should run (agents ≥ 0.14 merge it)
    branch,
    // §21 #118 sites with an open remote GUI session (their agents long-poll for the browser's requests)
    remoteGui: remoteGuiCloudItems(deploymentId),
    // §21 #123 shared files: the whole listing (+ tombstones) and the copies the cloud wants from a site
    files: files.files,
    fileFetches: files.fileFetches,
    // §21 #125 port tunnels (absent: this cloud carries none; the agent does not poll)
    ...(tunnels ? { tunnels } : {}),
  };
}

// ───────────────────────────────────────── what a site may download (head + open proposals)
interface Allowed {
  images: Map<string, { serviceSlug: string; version: string }>;
  blobs: Map<string, { kind: 'manifest' | 'config' | 'layer' | 'file'; serviceSlug?: string; version?: string }>;
}
const allowedCache = new Map<string, { at: number; v: Allowed }>();
async function allowedFor(deploymentId: string, dep: StoredDeployment): Promise<Allowed> {
  const key = `${deploymentId}:${dep.agentEpoch}`;
  const hit = allowedCache.get(key);
  if (hit && Date.now() - hit.at < 60_000) return hit.v;
  const open = await col.proposals(deploymentId).where('status', 'in', [...OPEN_STATUSES]).select().get();
  const revIds = new Set(open.docs.map((d) => d.id));
  if (dep.head) revIds.add(dep.head.revId);
  // every site's own head too (sites may lag behind deployment.head)
  const agents = await col.agents(deploymentId).select('headRevId').get();
  for (const a of agents.docs) if (a.data().headRevId) revIds.add(a.data().headRevId);
  const revs = revIds.size ? await db().getAll(...[...revIds].map((r) => col.revisions(deploymentId).doc(r))) : [];
  const v: Allowed = { images: new Map(), blobs: new Map() };
  // §21 #104: the deployment head commit + its newest commits + every site's agent-branch head
  const fromCommits = await allowedCommits(deploymentId);
  const sources: { config: Revision['config']; catalog: Revision['catalog'] | null }[] = [...revs.filter((s) => s.exists).map((s) => decodeRevision(s.data()!)), ...fromCommits];
  for (const r of sources) {
    if (!r.catalog) r.catalog = { services: {}, images: {}, notFoundPages: {} };
    for (const site of r.config.sites)
      for (const c of site.config.containers) {
        v.images.set(imageKey(c.serviceSlug, c.version), { serviceSlug: c.serviceSlug, version: c.version });
        const spec = r.catalog.images[imageKey(c.serviceSlug, c.version)];
        if (spec) {
          v.blobs.set(hexOf(spec.digest), { kind: 'manifest', serviceSlug: c.serviceSlug, version: c.version });
          v.blobs.set(hexOf(spec.configDigest), { kind: 'config', serviceSlug: c.serviceSlug, version: c.version });
          for (const l of spec.layers) v.blobs.set(hexOf(l.digest), { kind: 'layer', serviceSlug: c.serviceSlug, version: c.version });
        }
      }
    for (const f of r.config.files) if (f.content) v.blobs.set(hexOf(f.content.sha256), { kind: 'file' });
    // round E: volume seed archives (stored like file contents, files/<hex>); seed 'files' / 'file' name config files (above)
    for (const site of r.config.sites)
      for (const vol of site.config.volumes ?? []) {
        if (vol.seed?.kind === 'archive' && typeof vol.seed.blobSha256 === 'string') v.blobs.set(hexOf(vol.seed.blobSha256), { kind: 'file' });
        // §21 #101 the volume's own content (tree / single seeds; blobs files/<hex>)
        if (vol.seed?.kind === 'tree') for (const e of vol.seed.entries ?? []) if (typeof e?.sha256 === 'string') v.blobs.set(hexOf(e.sha256), { kind: 'file' });
        if (vol.seed?.kind === 'single' && typeof vol.seed.sha256 === 'string') v.blobs.set(hexOf(vol.seed.sha256), { kind: 'file' });
      }
  }
  allowedCache.set(key, { at: Date.now(), v });
  if (allowedCache.size > 500) for (const [k, x] of allowedCache) if (Date.now() - x.at > 60_000) allowedCache.delete(k);
  return v;
}

async function loadRev(deploymentId: string, revId: string): Promise<Revision> {
  const s = await col.revisions(deploymentId).doc(revId).get();
  if (!s.exists) throw E.notFound('revision not found');
  return decodeRevision(s.data()!);
}
async function secretFetched(c: Ctx, r: Revision) {
  const slugs = r.config.secrets.filter((s) => s.value !== null && s.value !== undefined && s.value !== '').map((s) => s.secretSlug);
  if (!r.config.secrets.length) return;
  await appendCloud(A(c).deploymentId, [{ type: 'secret.fetched', summary: `Site ${A(c).siteSlug} fetched revision seq ${r.seq} with ${slugs.length} secret value(s)`, data: { siteSlug: A(c).siteSlug, secretSlugs: slugs, revId: r.revId }, actor: null }]);
}

function checkRevisionShape(r: Revision, path = 'revision') {
  const bad = (m: string) => E.bad(path, 'V_TYPE', m);
  if (!r || typeof r !== 'object') throw bad('revision is required');
  if (typeof r.revId !== 'string' || !RE.ulid.test(r.revId)) throw bad('revId must be a ULID');
  if (!Number.isInteger(r.seq) || r.seq < 1) throw bad('seq must be an integer ≥ 1');
  if (r.parentRevId !== null && (typeof r.parentRevId !== 'string' || !RE.ulid.test(r.parentRevId))) throw bad('parentRevId must be a ULID or null');
  if (!r.config || typeof r.config !== 'object' || !r.catalog || typeof r.catalog !== 'object') throw bad('config and catalog are required');
  if (r.origin !== 'cloud' && r.origin !== 'agent') throw bad('origin must be cloud | agent');
  if (r.kind !== 'change' && r.kind !== 'rollback') throw bad('kind must be change | rollback');
  if (typeof r.summary !== 'string' || typeof r.actor !== 'string' || typeof r.createdAt !== 'string') throw bad('summary, actor and createdAt are required');
  if (jcsHash(r.config) !== r.hash) throw E.bad(`${path}.hash`, 'V_FORMAT', 'hash does not match sha256(JCS(config))');
  if (jcsHash(r.catalog) !== r.catalogHash) throw E.bad(`${path}.catalogHash`, 'V_FORMAT', 'catalogHash does not match sha256(JCS(catalog))');
  void canonicalSafe;
}
const cleanRevision = (r: Revision): Revision => ({
  revId: r.revId,
  seq: r.seq,
  parentRevId: r.parentRevId,
  origin: r.origin,
  actor: r.actor,
  createdAt: r.createdAt,
  config: r.config,
  hash: r.hash,
  catalog: r.catalog,
  catalogHash: r.catalogHash,
  summary: r.summary,
  kind: r.kind,
  rollbackOf: r.rollbackOf ?? null,
});

/** Derived proposal status (§6.1 reportProposalStatus, §14.2). */
export function deriveStatus(p: Proposal): Proposal['status'] {
  if (!['published', 'staging', 'ready'].includes(p.status)) return p.status;
  const vals = Object.values(p.sites ?? {});
  if (vals.length && vals.every((s) => s.staging === 'ready')) return 'ready';
  if (vals.some((s) => s.staging !== 'pending')) return 'staging';
  return p.status === 'published' ? 'published' : 'staging';
}

async function replyBytes(r: Reply): Promise<Buffer> {
  if (r.body === null) return Buffer.alloc(0);
  if (Buffer.isBuffer(r.body)) return r.body;
  if (typeof r.body === 'string') return Buffer.from(r.body, 'utf8');
  const chunks: Buffer[] = [];
  let n = 0;
  for await (const ch of r.body) {
    n += (ch as Buffer).length;
    if (n > RELAY_MAX) {
      r.body.destroy();
      throw new ApiError('too-large', `relayed responses are at most ${RELAY_MAX} bytes (use smart distribution)`);
    }
    chunks.push(ch as Buffer);
  }
  return Buffer.concat(chunks);
}

/** §21 #92 HeartbeatRequest.agentTime: optional ISO 8601 UTC timestamp ("…Z"). */
export function agentTimeError(v: unknown): string | null {
  if (v === undefined || v === null) return null;
  if (typeof v !== 'string' || v.length > 40 || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?Z$/.test(v) || !Number.isFinite(Date.parse(v))) return 'agentTime must be an ISO 8601 UTC timestamp (…Z)';
  return null;
}
/** §21 #92 skew = agent clock − server receive time, whole seconds (positive = agent ahead) */
export function clockSkewOf(agentTime: string, receivedAt: Date): AgentClockSkew {
  return { skewSec: Math.round((Date.parse(agentTime) - receivedAt.getTime()) / 1000), measuredAt: receivedAt.toISOString() };
}

export const agentHandlers: Pick<
  Handlers,
  | 'poll'
  | 'heartbeat'
  | 'reportHead'
  | 'getAgentProposal'
  | 'registerLocalProposal'
  | 'reportProposalStatus'
  | 'getAgentRevision'
  | 'getImageManifest'
  | 'getAgentBlob'
  | 'uploadAgentActivities'
  | 'listAgentTransfers'
  | 'createAgentTransfer'
  | 'reportAgentTransfer'
  | 'downloadAgentTransfer'
  | 'getAgentUpload'
  | 'putAgentUploadPart'
  | 'completeAgentUpload'
  | 'relay'
> = {
  async poll(c) {
    const { deploymentId, siteSlug } = A(c);
    const ws = c.query.get('wait');
    let wait = 25;
    if (ws !== null && ws !== '') {
      const n = Number(ws);
      if (!Number.isFinite(n) || n < 0) throw E.bad('wait', 'V_RANGE', 'wait must be 0–50');
      wait = Math.min(50, Math.floor(n));
    }
    const cursor = c.query.get('cursor');
    await col.agents(deploymentId).doc(siteSlug).update({ lastSeenAt: nowIso() }).catch(() => undefined);
    let dep = A(c).dep;
    if (cursor === String(dep.agentEpoch) && wait > 0 && !dep.deleted) {
      const deadline = Date.now() + wait * 1000;
      while (Date.now() < deadline && !c.signal?.aborted) {
        await sleep(Math.min(1000, Math.max(0, deadline - Date.now())));
        const s = await col.deployments().doc(deploymentId).get();
        const e = s.data()?.agentEpoch;
        if (e !== dep.agentEpoch || s.data()?.deleted) {
          dep = decodeDeployment(s.data()!);
          break;
        }
      }
      if (Date.now() >= deadline) dep = decodeDeployment((await col.deployments().doc(deploymentId).get()).data()!);
    }
    return buildCloudView(deploymentId, dep);
  },

  async heartbeat(c) {
    const { deploymentId, siteSlug, agent, relayedBy } = A(c);
    const b = asObject(c.json()) as unknown as HeartbeatRequest;
    if (b.siteSlug !== siteSlug) throw E.bad('siteSlug', 'V_REF', 'siteSlug must equal the signing site');
    if (typeof b.advertiseHost !== 'string' || !b.advertiseHost) throw E.bad('advertiseHost', 'V_REQUIRED', 'advertiseHost is required');
    if (!Number.isInteger(b.agentPort) || b.agentPort < 1 || b.agentPort > 65535) throw E.bad('agentPort', 'V_RANGE', 'agentPort must be 1–65535');
    const daErr = detectedAddressesError(b.detectedAddresses);
    if (daErr) throw E.bad('detectedAddresses', 'V_TYPE', daErr);
    const edErr = edgeExtrasError(b.edge);
    if (edErr) throw E.bad('edge', 'V_TYPE', edErr);
    const gpErr = guiPortsError(b.guiPorts);
    if (gpErr) throw E.bad('guiPorts', 'V_TYPE', gpErr);
    const upErr = upgradeProgressError(b.upgrade);
    if (upErr) throw E.bad('upgrade', 'V_TYPE', upErr);
    const atErr = agentTimeError(b.agentTime);
    if (atErr) throw E.bad('agentTime', 'V_FORMAT', atErr);
    const vsErr = volumeSyncError(b.volumeSync);
    if (vsErr) throw E.bad('volumeSync', 'V_TYPE', vsErr);
    // §21 #92: lastSeenAt is ALWAYS this server's receive time; the agent's own clock only feeds clockSkew
    const now = new Date();
    const upd: Partial<AgentDoc> = {
      lastSeenAt: now.toISOString(),
      agentVersion: String(b.agentVersion ?? agent.agentVersion),
      headRevId: b.headRevId ?? null,
      staging: b.staging ?? null,
      metrics: b.metrics ?? null,
      edge: b.edge ?? null,
      peersSeen: Array.isArray(b.peersSeen) ? b.peersSeen : [],
      keyVersion: Number.isInteger(b.keyVersion) ? b.keyVersion : agent.keyVersion,
      ip: String(b.ip ?? agent.ip),
      hostname: String(b.hostname ?? agent.hostname),
      advertiseHost: b.advertiseHost,
      agentPort: b.agentPort,
      relayVia: relayedBy,
      // round C: old agents omit the field → keep what is stored (or [])
      detectedAddresses: b.detectedAddresses === undefined || b.detectedAddresses === null ? (Array.isArray(agent.detectedAddresses) ? agent.detectedAddresses : []) : detectedAddressesOf(b.detectedAddresses),
      // §21 #86: old agents omit it → [] (the Setup page then falls back to :8080 / :7080)
      guiPorts: Array.isArray(b.guiPorts) ? b.guiPorts.map((g) => ({ port: g.port, scheme: g.scheme, kind: g.kind })) : [],
    };
    // §21 #90: the last upgrade progress (omitted / null → the stored one is kept)
    if (b.upgrade && typeof b.upgrade === 'object') upd.upgrade = cleanProgress(b.upgrade as unknown as Record<string, unknown>, siteSlug);
    // §21 #92: agent clock − receive time (old agents omit agentTime → the stored value is kept)
    if (typeof b.agentTime === 'string') upd.clockSkew = clockSkewOf(b.agentTime, now);
    const date = ymd(now);
    const hour = now.getUTCHours();
    const upRef = col.uptime(deploymentId).doc(`${siteSlug}_${date}`);
    await db().runTransaction(async (tx) => {
      const s = await tx.get(upRef);
      const u: UptimeDay = s.exists ? (s.data() as UptimeDay) : { siteSlug, date, beats: Array(24).fill(0), hours: Array(24).fill(0), updatedAt: '' };
      const beats = u.beats?.length === 24 ? [...u.beats] : Array(24).fill(0);
      const hours = u.hours?.length === 24 ? [...u.hours] : Array(24).fill(0);
      beats[hour] = (beats[hour] ?? 0) + 1;
      hours[hour] = Math.min(1, beats[hour] / 120);
      tx.set(upRef, { siteSlug, date, beats, hours, updatedAt: now.toISOString() });
      tx.update(col.agents(deploymentId).doc(siteSlug), upd);
    });
    if (b.advertiseHost !== agent.advertiseHost || b.agentPort !== agent.agentPort) await col.deployments().doc(deploymentId).update({ agentEpoch: FieldValue.increment(1) });
    // §21 #90: a queued cloud request → clear it when finished / reached / expired; advance the upgrade-all run
    if (agent.upgradeRequest) await settleUpgrade(deploymentId, siteSlug);
    // §21 #101 sync state of the persist-to-cloud volumes → volumeContent state.live
    await storeVolumeSync(deploymentId, siteSlug, b.volumeSync, now.toISOString());
    // §21 #104 the agent branch of this site (uncommitted drift / edits, head)
    await storeHeartbeatBranch(deploymentId, siteSlug, (b as unknown as { branch?: unknown }).branch, upd.agentVersion ?? '');
    return { ok: true, serverTime: now.toISOString() };
  },

  async reportHead(c) {
    const { deploymentId, siteSlug } = A(c);
    const b = asObject(c.json());
    const rev = b.revision as Revision;
    checkRevisionShape(rev);
    const revision = cleanRevision(rev);
    const depRef = col.deployments().doc(deploymentId);
    return db().runTransaction(async (tx) => {
      const [ds, rs, ps] = await Promise.all([
        tx.get(depRef),
        tx.get(col.revisions(deploymentId).doc(revision.revId)),
        revision.parentRevId ? tx.get(col.revisions(deploymentId).doc(revision.parentRevId)) : Promise.resolve(null),
      ]);
      const dep = decodeDeployment(ds.data()!);
      const open = (await tx.get(col.proposals(deploymentId).where('status', 'in', [...OPEN_STATUSES]))).docs.map((d) => d.data() as Proposal);
      const own = await tx.get(col.proposals(deploymentId).doc(revision.revId));
      // §21 #104: a pre-0.14 site's agent-branch mirror follows its reported head
      const mirror = await legacyHeadMirror(tx, deploymentId, siteSlug, revision, A(c).agent.agentVersion ?? null);
      if (rs.exists && rs.data()!.hash !== revision.hash) throw E.conflict('a different revision with this revId is stored');
      if (revision.parentRevId === null) {
        if (revision.seq !== 1) throw E.bad('revision.seq', 'V_RANGE', 'a revision without parent must have seq 1');
      } else {
        if (!ps || !ps.exists) throw E.bad('revision.parentRevId', 'V_REF', `unknown parent revision ${revision.parentRevId} (report the parent first)`);
        if (revision.seq !== (ps.data()!.seq as number) + 1) throw E.bad('revision.seq', 'V_RANGE', `seq must be parent.seq + 1 = ${(ps.data()!.seq as number) + 1}`);
      }
      const curSeq = dep.head?.seq ?? 0;
      if (dep.head && revision.seq === curSeq && dep.head.revId !== revision.revId) throw E.conflict(`fork: head seq ${curSeq} is ${dep.head.revId}`, { current: dep.head });
      const stored = !rs.exists;
      const now = nowIso();
      const log = new CloudLog(deploymentId, dep);
      const depUpd: Record<string, unknown> = {};
      let headChanged = false;
      let epoch = false;
      if (revision.seq > curSeq) {
        headChanged = true;
        epoch = true;
        depUpd.head = { revId: revision.revId, seq: revision.seq, hash: revision.hash, reportedAt: now };
        for (const p of open) {
          if (p.revId === revision.revId) continue;
          if (['published', 'staging', 'ready'].includes(p.status) && p.parentRevId !== revision.revId) {
            tx.update(col.proposals(deploymentId).doc(p.revId), { status: 'stale', updatedAt: now });
            log.add(tx, { type: 'proposal.stale', summary: `Proposal seq ${p.seq} is stale (head is now seq ${revision.seq})`, data: { revId: p.revId, parentRevId: p.parentRevId, headRevId: revision.revId }, actor: null });
          }
        }
      }
      if (revision.seq >= curSeq && own.exists) {
        const p = own.data() as Proposal;
        if (p.status !== 'applied') {
          tx.update(own.ref, { status: 'applied', updatedAt: now, result: p.result ?? { ok: true, message: 'applied', snapshotId: null } });
          epoch = true;
        }
      }
      if (stored) tx.create(col.revisions(deploymentId).doc(revision.revId), encodeRevision(revision));
      if (epoch) depUpd.agentEpoch = dep.agentEpoch + 1;
      if (log.changed) Object.assign(depUpd, log.depUpdate());
      if (Object.keys(depUpd).length) tx.update(depRef, depUpd);
      tx.update(col.agents(deploymentId).doc(siteSlug), { headRevId: revision.revId, lastSeenAt: now });
      mirror?.();
      return { stored, headChanged };
    });
  },

  async getAgentProposal(c) {
    const { deploymentId } = A(c);
    const ps = await col.proposals(deploymentId).doc(c.params.revId!).get();
    if (!ps.exists) throw E.notFound('proposal not found');
    const revision = await loadRev(deploymentId, c.params.revId!);
    await secretFetched(c, revision);
    return { proposal: ps.data() as Proposal, revision };
  },

  async registerLocalProposal(c) {
    const { deploymentId, siteSlug } = A(c);
    const b = asObject(c.json());
    const rev = b.revision as Revision;
    const prop = b.proposal as Proposal;
    checkRevisionShape(rev);
    if (!prop || typeof prop !== 'object' || prop.revId !== rev.revId) throw E.bad('proposal.revId', 'V_REF', 'proposal.revId must equal revision.revId');
    if (rev.origin !== 'agent' || prop.origin !== 'agent') throw E.bad('proposal.origin', 'V_ENUM', 'local proposals have origin agent');
    const revision = cleanRevision(rev);
    const depRef = col.deployments().doc(deploymentId);
    return db().runTransaction(async (tx) => {
      const [ds, rs, ps, ms] = await Promise.all([tx.get(depRef), tx.get(col.revisions(deploymentId).doc(revision.revId)), tx.get(col.proposals(deploymentId).doc(revision.revId)), tx.get(bcol.agentCommits(deploymentId, siteSlug).doc(revision.revId))]);
      const dep = decodeDeployment(ds.data()!);
      if (rs.exists && rs.data()!.hash !== revision.hash) throw E.conflict('a different revision with this revId is stored');
      if (ps.exists) return { stored: false };
      // §21 #104: a pre-0.14 local proposal is mirrored as a "legacy" commit of the site's agent branch (same id = revId,
      // so the 0.14 migration push of the same revision is recognised as already stored)
      let writeMirror: (() => void) | null = null;
      if (!ms.exists) {
        const parent = revision.parentRevId ? await tx.get(col.revisions(deploymentId).doc(revision.parentRevId)) : null;
        const legacy: Commit = { commitId: revision.revId, branch: { kind: 'agent', deploymentId, siteSlug }, seq: revision.seq, parentId: revision.parentRevId, kind: 'legacy', message: revision.summary || `local proposal seq ${revision.seq}`, author: { kind: 'person', email: revision.actor, uid: null, name: null, siteSlug }, createdAt: revision.createdAt, source: null, revertOf: null, resetTo: null, rollbackOf: null, config: revision.config, hash: revision.hash, catalog: revision.catalog, catalogHash: revision.catalogHash, stats: commitStats(parent?.exists ? decodeRevision(parent.data()!).config : null, revision.config, { secretValues: true, scope: siteSlug }), data: { migratedFrom: { revId: revision.revId, seq: revision.seq, localProposal: true } } };
        writeMirror = () => tx.create(bcol.agentCommits(deploymentId, siteSlug).doc(revision.revId), encodeCommit(legacy));
      }
      const open = (await tx.get(col.proposals(deploymentId).where('status', 'in', ['published', 'staging', 'ready']))).docs.map((d) => d.data() as Proposal);
      const now = nowIso();
      const sites: Proposal['sites'] = {};
      for (const [k, v] of Object.entries(prop.sites ?? {})) sites[k] = { staging: v?.staging ?? 'pending', progress: typeof v?.progress === 'number' ? v.progress : 0, error: v?.error ?? null, updatedAt: v?.updatedAt ?? null };
      if (!Object.keys(sites).length) for (const s of revision.config.sites) sites[s.siteSlug] = { staging: 'pending', progress: 0, error: null, updatedAt: null };
      const newer = open.some((p) => p.revId > revision.revId);
      const proposal: Proposal = {
        revId: revision.revId,
        parentRevId: revision.parentRevId,
        seq: revision.seq,
        summary: revision.summary,
        origin: 'agent',
        kind: revision.kind,
        createdAt: prop.createdAt ?? now,
        createdBy: prop.createdBy ?? revision.actor,
        status: newer ? 'superseded' : isTerminal(prop.status) || ['approved', 'applying'].includes(prop.status) ? prop.status : 'published',
        sites,
        approvedBy: prop.approvedBy ?? null,
        approvedAt: prop.approvedAt ?? null,
        updateId: prop.updateId ?? null,
        result: prop.result ?? null,
        supersededBy: newer ? open.filter((p) => p.revId > revision.revId).sort((a, x) => (a.revId < x.revId ? 1 : -1))[0]!.revId : null,
        updatedAt: now,
      };
      proposal.status = deriveStatus(proposal);
      const log = new CloudLog(deploymentId, dep);
      if (!newer)
        for (const p of open)
          if (p.revId < revision.revId) {
            tx.update(col.proposals(deploymentId).doc(p.revId), { status: 'superseded', supersededBy: revision.revId, updatedAt: now });
            log.add(tx, { type: 'proposal.superseded', summary: `Proposal seq ${p.seq} superseded by the local proposal seq ${revision.seq} (site ${siteSlug})`, data: { revId: p.revId, byRevId: revision.revId }, actor: null });
          }
      if (!rs.exists) tx.create(col.revisions(deploymentId).doc(revision.revId), encodeRevision(revision));
      tx.create(col.proposals(deploymentId).doc(revision.revId), proposal);
      writeMirror?.();
      tx.update(depRef, { agentEpoch: dep.agentEpoch + 1, ...(log.changed ? log.depUpdate() : {}) });
      return { stored: true };
    });
  },

  async reportProposalStatus(c) {
    const { deploymentId, siteSlug } = A(c);
    const b = asObject(c.json()) as unknown as ProposalStatusReport & { status?: string };
    if (b.siteSlug !== siteSlug) throw E.bad('siteSlug', 'V_REF', 'siteSlug must equal the signing site');
    if (b.staging !== undefined && !['pending', 'downloading', 'ready', 'failed'].includes(b.staging)) throw E.bad('staging', 'V_ENUM', 'staging must be pending | downloading | ready | failed');
    if (b.progress !== undefined && (typeof b.progress !== 'number' || b.progress < 0 || b.progress > 1)) throw E.bad('progress', 'V_RANGE', 'progress must be 0..1');
    if (b.status !== undefined && !['approved', 'applying', 'applied', 'failed-rolled-back', 'stale', 'ready'].includes(b.status)) throw E.bad('status', 'V_ENUM', 'status must be approved | applying | applied | failed-rolled-back | stale');
    const pRef = col.proposals(deploymentId).doc(c.params.revId!);
    const depRef = col.deployments().doc(deploymentId);
    return db().runTransaction(async (tx) => {
      const [ps, ds] = await Promise.all([tx.get(pRef), tx.get(depRef)]);
      if (!ps.exists) throw E.notFound('proposal not found');
      const cur = ps.data() as Proposal;
      const next: Proposal = JSON.parse(JSON.stringify(cur));
      const now = nowIso();
      const terminal = isTerminal(cur.status);
      if (!terminal && (b.staging !== undefined || b.progress !== undefined || b.error !== undefined)) {
        const s = next.sites[siteSlug] ?? { staging: 'pending', progress: 0, error: null, updatedAt: null };
        next.sites[siteSlug] = {
          staging: b.staging ?? s.staging,
          progress: b.progress ?? (b.staging === 'ready' ? 1 : s.progress),
          error: b.error !== undefined ? b.error : b.staging && b.staging !== 'failed' ? null : s.error,
          updatedAt: now,
        };
      }
      if (b.status) {
        const st = b.status as Proposal['status'];
        const allowed =
          st === 'applied' || st === 'failed-rolled-back'
            ? true
            : st === 'stale'
              ? !terminal
              : st === 'ready'
                ? cur.status === 'approved'
                : ['published', 'staging', 'ready', 'approved', 'applying'].includes(cur.status);
        if (allowed) next.status = st;
      }
      if (b.approvedBy !== undefined) next.approvedBy = b.approvedBy;
      if (b.approvedAt !== undefined) next.approvedAt = b.approvedAt;
      if (b.updateId !== undefined) next.updateId = b.updateId;
      if (b.result !== undefined) next.result = b.result;
      // LOCK failed: the coordinator reports the result without a status → approved goes back to ready
      if (!b.status && b.result && b.result.ok === false && cur.status === 'approved') next.status = 'ready';
      next.status = deriveStatus(next);
      const changed = !sameJson({ ...next, updatedAt: null }, { ...cur, updatedAt: null });
      if (!changed) return { ok: true, proposal: cur };
      next.updatedAt = now;
      tx.set(pRef, next);
      const dep = decodeDeployment(ds.data()!);
      tx.update(depRef, { agentEpoch: dep.agentEpoch + 1 });
      return { ok: true, proposal: next };
    });
  },

  async getAgentRevision(c) {
    const r = await loadRev(A(c).deploymentId, c.params.revId!);
    await secretFetched(c, r);
    return { revision: r };
  },

  async getImageManifest(c) {
    const { deploymentId, dep } = A(c);
    const { serviceSlug, version } = c.params as { serviceSlug: string; version: string };
    const allowed = await allowedFor(deploymentId, dep);
    if (!allowed.images.has(imageKey(serviceSlug, version))) throw E.forbidden('the image is not in the head or an open proposal');
    const s = await col.images(serviceSlug).doc(version).get();
    if (!s.exists) throw E.notFound('image not found');
    const img = s.data() as Image & { _configSizeBytes?: number };
    if (img.import.status !== 'ready' || !img.digest || !img.import.configDigest) throw E.bad('import', 'V_IMAGE_NOT_READY', `import status is ${img.import.status}`);
    const res: ImageManifestResponse = {
      serviceSlug,
      version,
      digest: img.digest,
      platform: img.platform,
      mediaType: img.import.manifestMediaType ?? 'application/vnd.oci.image.manifest.v1+json',
      config: { digest: img.import.configDigest, sizeBytes: img._configSizeBytes ?? 0 },
      layers: img.import.layers,
    };
    return res;
  },

  async getAgentBlob(c) {
    const { deploymentId, dep, relayedBy } = A(c);
    const hex = hexOf(c.params.sha256!);
    if (!isHex64(hex)) throw E.bad('sha256', 'V_FORMAT', 'sha256 must be "sha256:" + 64 hex');
    const allowed = await allowedFor(deploymentId, dep);
    // §21 #101 also: the trees + files of a pending volume-content push to this site (and its base)
    const what = allowed.blobs.get(hex) ?? ((await pushBlobAllowed(deploymentId, A(c).siteSlug, dep.agentEpoch, hex)) ? { kind: 'file' as const } : undefined);
    if (!what) throw E.forbidden('the blob does not belong to the head or an open proposal');
    let path = what.kind === 'file' ? paths.file(hex) : paths.blob(hex);
    if (what.kind === 'layer' && !(await exists(path)) && what.serviceSlug && what.version) {
      const s = await col.images(what.serviceSlug).doc(what.version).get();
      const img = s.data() as Image | undefined;
      if (img?.import.mode === 'gateway') await fetchLayer(img, `sha256:${hex}`);
    }
    if (what.kind === 'file' && !(await exists(path)) && (await exists(paths.blob(hex)))) path = paths.blob(hex);
    return serveObject(path, { range: c.headers['range'], headers: { 'X-OPM-Sha256': hex }, noRedirect: noRedirectFor(c.headers, relayedBy) });
  },

  async uploadAgentActivities(c) {
    const { deploymentId, siteSlug, agent } = A(c);
    const b = asObject(c.json());
    if (b.siteSlug !== siteSlug) throw E.bad('siteSlug', 'V_REF', 'siteSlug must equal the signing site');
    const entries = b.entries as Activity[];
    if (!Array.isArray(entries)) throw E.bad('entries', 'V_TYPE', 'entries must be a list');
    if (entries.length > 500) throw E.bad('entries', 'V_TOO_MANY', 'at most 500 entries per call');
    const ack = agent.ackSeq ?? 0;
    if (entries.length) {
      for (let i = 0; i < entries.length; i++) {
        const e = entries[i]!;
        if (!e || !Number.isInteger(e.seq)) throw E.bad(`entries[${i}].seq`, 'V_TYPE', 'seq must be an integer');
        if (i > 0 && e.seq !== entries[i - 1]!.seq + 1) throw E.bad(`entries[${i}].seq`, 'V_RANGE', `entries must be contiguous ascending (missing seq ${entries[i - 1]!.seq + 1})`);
        if (e.siteSlug !== siteSlug) throw E.bad(`entries[${i}].siteSlug`, 'V_REF', 'entries must be of the signing site');
      }
      if (entries[0]!.seq > ack + 1) throw E.bad('entries[0].seq', 'V_RANGE', `missing seq ${ack + 1}`);
    }
    const r = entries.length ? await ingestSiteLog(deploymentId, siteSlug, entries, 'pipe') : { inserted: 0, skipped: 0, rejected: 0, errors: [], ackSeq: ack };
    const res: AgentActivitiesResponse = { ackSeq: r.ackSeq, inserted: r.inserted, skipped: r.skipped, rejected: r.rejected, errors: r.errors };
    if (r.rejected > 0) throw new ApiError('invalid', `activity chain broken at ${r.errors[0]?.activityId}: ${r.errors[0]?.reason}`, { ...res, errors: r.errors.map((e) => ({ path: e.activityId, code: 'V_CHAIN', message: e.reason, severity: 'error' })), rejectedEntries: r.errors });
    return res;
  },

  async listAgentTransfers(c) {
    const { deploymentId, siteSlug } = A(c);
    const dir = c.query.get('direction') ?? 'to-site';
    if (dir !== 'to-site') throw E.bad('direction', 'V_ENUM', 'direction must be to-site');
    const q = await col.transfers(deploymentId).where('siteSlug', '==', siteSlug).where('direction', '==', 'to-site').orderBy('createdAt', 'desc').limit(500).get();
    return { items: q.docs.map((d) => d.data() as Transfer).filter((t) => ['ready', 'saved-on-server', 'downloaded'].includes(t.status)).map((t) => ({ ...t, expiresAt: null })) };
  },

  async createAgentTransfer(c) {
    const { deploymentId, siteSlug } = A(c);
    const b = asObject(c.json()) as unknown as AgentCreateTransferRequest & Record<string, unknown>;
    if (typeof b.actor !== 'string' || !RE.email.test(b.actor)) throw E.bad('actor', 'V_FORMAT', 'actor (email) is required');
    const body = validateTransferBody({ fileName: b.fileName, sizeBytes: b.sizeBytes, sha256: b.sha256, ...(b.note !== undefined ? { note: b.note } : {}) });
    const tRef = col.transfers(deploymentId).doc();
    const upload = newUpload({ purpose: 'transfer', deploymentId, transferId: tRef.id, sizeBytes: body.sizeBytes, sha256: body.sha256, createdBy: b.actor, creatorSite: siteSlug, target: paths.transfer(deploymentId, tRef.id) });
    const t: Transfer = { ...newTransfer({ deploymentId, siteSlug, direction: 'to-cloud', body, createdBy: b.actor, uploadId: upload.uploadId }), transferId: tRef.id, storagePath: paths.transfer(deploymentId, tRef.id) };
    const batch = db().batch();
    batch.create(tRef, t);
    batch.create(col.uploads().doc(upload.uploadId), upload);
    await batch.commit();
    return { transfer: t, upload: uploadView(upload) };
  },

  async reportAgentTransfer(c) {
    const { deploymentId, siteSlug } = A(c);
    const b = asObject(c.json()) as unknown as AgentTransferStatusRequest;
    if (!['ready', 'saved-on-server', 'downloaded', 'fetched'].includes(b.status)) throw E.bad('status', 'V_ENUM', 'status must be ready | saved-on-server | downloaded | fetched');
    if (b.status === 'saved-on-server' && (typeof b.savedPath !== 'string' || !b.savedPath.startsWith('/'))) throw E.bad('savedPath', 'V_FORMAT', 'savedPath (absolute) is required');
    const tRef = col.transfers(deploymentId).doc(c.params.transferId!);
    const depRef = col.deployments().doc(deploymentId);
    await db().runTransaction(async (tx) => {
      const [ts, ds] = await Promise.all([tx.get(tRef), tx.get(depRef)]);
      if (!ts.exists) throw E.notFound('transfer not found');
      const t = ts.data() as Transfer;
      if (t.siteSlug !== siteSlug) throw E.notFound('transfer not found');
      if (t.status === 'expired') throw E.bad('status', 'V_RANGE', 'the transfer expired');
      if (b.status === 'ready') {
        if (t.direction !== 'to-cloud') throw E.bad('status', 'V_ENUM', 'ready is reported only for to-cloud transfers');
        if (t.status === 'uploading') throw E.bad('status', 'V_RANGE', 'complete the upload first');
        return;
      }
      if (t.direction !== 'to-site') throw E.bad('status', 'V_ENUM', `${b.status} is reported only for to-site transfers`);
      if (t.status === 'uploading') throw E.bad('status', 'V_RANGE', 'the transfer is still uploading');
      // §21 #100: the site has the file (store-and-forward); saved / downloaded imply it
      const fetched = t.fetchedAt ? {} : { fetchedAt: nowIso() };
      if (b.status === 'fetched') {
        if (t.fetchedAt) return; // idempotent
        tx.update(tRef, fetched);
        return;
      }
      if (t.status === b.status && (b.status !== 'saved-on-server' || t.savedPath === b.savedPath)) return;
      const dep = decodeDeployment(ds.data()!);
      const log = new CloudLog(deploymentId, dep);
      tx.update(tRef, { status: b.status, ...fetched, ...(b.status === 'saved-on-server' ? { savedPath: b.savedPath } : {}) });
      log.add(tx, {
        type: b.status === 'saved-on-server' ? 'transfer.saved' : 'transfer.received',
        summary: b.status === 'saved-on-server' ? `File ${t.fileName} saved on site ${siteSlug} at ${b.savedPath}` : `File ${t.fileName} downloaded on site ${siteSlug}`,
        data: { transferId: t.transferId, direction: t.direction, fileName: t.fileName, sizeBytes: t.sizeBytes, siteSlug, ...(b.status === 'saved-on-server' ? { savedPath: b.savedPath } : {}) },
        actor: typeof b.actor === 'string' ? b.actor : null,
        origin: 'agent',
      });
      tx.update(depRef, { ...log.depUpdate(), ...(t.status === 'ready' ? { agentEpoch: dep.agentEpoch + 1 } : {}) });
    });
    return { ok: true };
  },

  async downloadAgentTransfer(c) {
    const { deploymentId, siteSlug, relayedBy } = A(c);
    const s = await col.transfers(deploymentId).doc(c.params.transferId!).get();
    if (!s.exists) throw E.notFound('transfer not found');
    const t = s.data() as Transfer;
    if (t.siteSlug !== siteSlug || t.direction !== 'to-site') throw E.notFound('transfer not found');
    if (t.status === 'expired') throw E.gone('the transfer was deleted');
    if (t.status === 'uploading') throw E.notFound('the transfer is still uploading');
    return serveObject(t.storagePath, { range: c.headers['range'], fileName: t.fileName, headers: { 'X-OPM-Sha256': hexOf(t.sha256) }, noRedirect: noRedirectFor(c.headers, relayedBy) });
  },

  async getAgentUpload(c) {
    const u = await loadUpload(c.params.uploadId!, { kind: 'agent', deploymentId: A(c).deploymentId, siteSlug: A(c).siteSlug }, { allowClosed: true });
    if (u.status === 'failed') throw E.gone(`the upload failed${u.error ? `: ${u.error}` : ''}`);
    if (u.status === 'open' && Date.parse(u.expiresAt) <= Date.now()) throw E.gone('the upload expired');
    return uploadView(u);
  },
  async putAgentUploadPart(c) {
    const u = await loadUpload(c.params.uploadId!, { kind: 'agent', deploymentId: A(c).deploymentId, siteSlug: A(c).siteSlug });
    const r = await putPart(u, c.params.index!, c.body);
    // §21 #123 a holder sending its copy: the console follows cloud.fetch.bytesDone
    if (u.purpose === 'file' && u.deploymentId && u.transferId) {
      const fRef = col.files(u.deploymentId).doc(u.transferId);
      const bytesDone = r.received.reduce((n, i) => n + expectedPartSize(u, i), 0);
      await db()
        .runTransaction(async (tx) => {
          const f = (await tx.get(fRef)).data() as StoredFile | undefined;
          if (f?.cloud.fetch && f._uploadId === u.uploadId) tx.update(fRef, { 'cloud.fetch.bytesDone': bytesDone });
        })
        .catch(() => undefined);
    }
    return r;
  },
  async completeAgentUpload(c) {
    const u = await loadUpload(c.params.uploadId!, { kind: 'agent', deploymentId: A(c).deploymentId, siteSlug: A(c).siteSlug });
    return completeUpload(u, { id: u.createdBy, siteSlug: A(c).siteSlug });
  },

  async relay(c) {
    const outer = A(c);
    const b = asObject(c.json()) as unknown as RelayRequest;
    const r = b.request;
    if (typeof b.forSite !== 'string' || !RE.slug.test(b.forSite)) throw E.bad('forSite', 'V_FORMAT', 'forSite must be a site slug');
    if (b.forSite === outer.siteSlug) throw E.bad('forSite', 'V_REF', 'a site cannot relay for itself');
    if (!r || typeof r !== 'object' || typeof r.method !== 'string' || typeof r.path !== 'string' || !r.headers || typeof r.headers !== 'object') throw E.bad('request', 'V_TYPE', 'request { method, path, headers, bodyB64 } is required');
    if (!r.path.startsWith('/v1/agent/') || r.path.startsWith('/v1/agent/relay') || r.path.startsWith('/v1/agent/enroll')) throw E.bad('request.path', 'V_FORMAT', 'only /v1/agent/* requests (not relay / enroll) can be relayed');
    const headers: Record<string, string> = {};
    for (const [k, v] of Object.entries(r.headers)) if (typeof v === 'string') headers[k.toLowerCase()] = v;
    if (headers['x-opm-site'] !== `${outer.deploymentId}/${b.forSite}`) throw E.bad('request.headers.X-OPM-Site', 'V_REF', `the inner request must be signed by ${outer.deploymentId}/${b.forSite}`);
    const body = r.bodyB64 ? Buffer.from(r.bodyB64, 'base64') : Buffer.alloc(0);
    let inner: AgentAuth;
    try {
      inner = await verifyAgent({ method: r.method.toUpperCase(), path: r.path, headers, body }, { allowDeleted: r.path.startsWith('/v1/agent/poll') });
    } catch (e) {
      if (e instanceof ApiError) {
        return { status: e.status, headers: { 'Content-Type': 'application/json; charset=utf-8', 'X-OPM-Server-Time': String(Math.floor(Date.now() / 1000)) }, bodyB64: Buffer.from(JSON.stringify({ error: { code: e.code, message: e.message, details: e.details } })).toString('base64') };
      }
      throw e;
    }
    inner.relayedBy = outer.siteSlug;
    const rep = await dispatch({ method: r.method, url: r.path, headers, body, ip: c.ip, signal: c.signal }, { preAgent: inner });
    let bytes: Buffer;
    try {
      bytes = await replyBytes(rep);
    } catch (e) {
      if (e instanceof ApiError) {
        return { status: e.status, headers: { 'Content-Type': 'application/json; charset=utf-8' }, bodyB64: Buffer.from(JSON.stringify({ error: { code: e.code, message: e.message, details: e.details } })).toString('base64') };
      }
      throw e;
    }
    if (inner.agent.relayVia !== outer.siteSlug) await col.agents(inner.deploymentId).doc(inner.siteSlug).update({ relayVia: outer.siteSlug }).catch(() => undefined);
    return { status: rep.status, headers: rep.headers, bodyB64: bytes.length ? bytes.toString('base64') : null };
  },
};
void pub;
