/**
 * §21 #101 volume content + persist-to-cloud volumes (shared/src/volume-content.ts: VOLUME_CONTENT_CLOUD_API).
 *
 * Firestore
 *   deployments/{id}/volumeContent/{siteSlug}:{volumeSlug}            state (latest, lastSnapshot, push, snapshotRequest, live)
 *   deployments/{id}/volumeContent/{key}/versions/{versionId}          VolumeContentVersion (ULID, seq per volume)
 *   deployments/{id}/volumeContent/{key}/pushes/{pushId}               VolumeContentPush history (state.push = the newest)
 * Storage: the canonical tree JSON of every version is the content-addressed blob files/<treeHash hex>; file bytes are
 * files/<sha hex> (POST /v1/blobs, uploads, or PUT /v1/agent/volume-content/blobs/{sha256}). Blobs are never deleted.
 * Internal state fields: `_open` (pending push or open snapshot request → cloud view), `lastSeq`.
 */
import {
  cloudSyncState,
  compileExcludes,
  diffVolumeTrees,
  isExcluded,
  looksLikeText,
  normalizeVolumeTree,
  RE,
  treeStats,
  ulid,
  VOLUME_CONTENT_LIMITS,
  volumeTreeHashInput,
  volumeTreeIssues,
  type AgentVolumePushReport,
  type AgentVolumeSnapshotRequest,
  type Config,
  type SiteConfig,
  type ValidationIssue,
  type VolumeContentCloudItem,
  type VolumeContentPush,
  type VolumeContentPushResult,
  type VolumeContentState,
  type VolumeContentVersion,
  type VolumeContentVersionSummary,
  type VolumePersist,
  type VolumeSnapshotTrigger,
  type VolumeSyncReport,
  type VolumeTree,
} from '@opm/shared';
import { col, db, nowIso, type Tx } from '../core/fire.js';
import { blobs } from '../ports/index.js';
import { ApiError, E } from '../core/errors.js';
import { loadDep, needDep } from '../core/perm.js';
import { asObject, hexOf, isHex64, onlyKeys, sha256Hex } from '../core/util.js';
import { decodeDeployment, decodeRevision, type StoredDeployment } from '../core/codec.js';
import { siteRunningCommit } from '../core/branches.js';
import { CloudLog } from '../core/activities.js';
import { exists, paths, readText, sizeOf, writeBytes } from '../core/storage.js';
import { Reply } from '../core/reply.js';
import { jsonHeaders, type Ctx, type Handlers } from '../core/router.js';
import { clampLimit } from './accounts.js';
import { dropStage, ensureBlobs, optStageId, testFault } from '../core/stages.js';

type Doc = Record<string, unknown>;
const isObj = (v: unknown): v is Doc => !!v && typeof v === 'object' && !Array.isArray(v);
const isInt = (v: unknown, min = 0): v is number => typeof v === 'number' && Number.isInteger(v) && v >= min;
const isIso = (v: unknown): v is string => typeof v === 'string' && v.length <= 40 && Number.isFinite(Date.parse(v));

export const vcCol = (deploymentId: string) => db().collection('deployments').doc(deploymentId).collection('volumeContent');
export const vcKey = (siteSlug: string, volumeSlug: string) => `${siteSlug}:${volumeSlug}`;
const versionsCol = (deploymentId: string, key: string) => vcCol(deploymentId).doc(key).collection('versions');
const pushesCol = (deploymentId: string, key: string) => vcCol(deploymentId).doc(key).collection('pushes');

/** the stored state document (fields may be missing on a doc created by a heartbeat) */
interface StoredState {
  deploymentId: string;
  siteSlug: string;
  volumeSlug: string;
  latest: VolumeContentVersionSummary | null;
  lastSnapshot: VolumeContentVersionSummary | null;
  versionCount: number;
  lastSeq: number;
  push: VolumeContentPush | null;
  snapshotRequest: VolumeContentState['snapshotRequest'];
  live: VolumeContentState['live'];
  updatedAt: string | null;
  _open: boolean;
}
function decodeState(deploymentId: string, siteSlug: string, volumeSlug: string, d: Doc | undefined): StoredState {
  return {
    deploymentId,
    siteSlug,
    volumeSlug,
    latest: (d?.latest as StoredState['latest']) ?? null,
    lastSnapshot: (d?.lastSnapshot as StoredState['lastSnapshot']) ?? null,
    versionCount: (d?.versionCount as number) ?? 0,
    lastSeq: (d?.lastSeq as number) ?? 0,
    push: (d?.push as StoredState['push']) ?? null,
    snapshotRequest: (d?.snapshotRequest as StoredState['snapshotRequest']) ?? null,
    live: (d?.live as StoredState['live']) ?? null,
    updatedAt: (d?.updatedAt as string | null) ?? null,
    _open: !!d?._open,
  };
}
const openOf = (s: Pick<StoredState, 'push' | 'snapshotRequest'>) => s.push?.status === 'pending' || !!s.snapshotRequest;
const summaryOf = (v: VolumeContentVersion): VolumeContentVersionSummary => ({ versionId: v.versionId, seq: v.seq, origin: v.origin, treeHash: v.treeHash, createdAt: v.createdAt, createdBy: v.createdBy });

function stateView(s: StoredState, persist: VolumePersist | null, persistInHead: boolean): VolumeContentState {
  return {
    deploymentId: s.deploymentId,
    siteSlug: s.siteSlug,
    volumeSlug: s.volumeSlug,
    persist,
    persistInHead,
    latest: s.latest,
    lastSnapshot: s.lastSnapshot,
    versionCount: s.versionCount,
    push: s.push,
    snapshotRequest: s.snapshotRequest,
    live: s.live,
    sync: cloudSyncState(s),
    updatedAt: s.updatedAt,
  };
}

// ───────────────────────────────────────── configs: draft / head persist settings
const persistOf = (sc: Pick<SiteConfig, 'volumes'> | null | undefined, volumeSlug: string): VolumePersist | null => (sc?.volumes ?? []).find((v) => v?.volumeSlug === volumeSlug && v.kind !== 'file')?.persist ?? null;
const draftSite = (dep: StoredDeployment, siteSlug: string) => dep.draft.sites.find((s) => s.siteSlug === siteSlug)?.config ?? null;

/**
 * siteSlug → the site config that site runs: its own reported head (a legacy revision or, O3, an agent-branch commit),
 * else the legacy deployment head revision (a site that never reported runs nothing of the branch head yet).
 */
export async function headSiteConfigs(dep: StoredDeployment): Promise<Map<string, SiteConfig>> {
  const agents = await col.agents(dep.deploymentId).select('headRevId').get();
  const bySite = new Map<string, string>();
  for (const a of agents.docs) if (typeof a.data().headRevId === 'string') bySite.set(a.id, a.data().headRevId as string);
  const depHead = await legacyHeadConfig(dep);
  const out = new Map<string, SiteConfig>();
  const sites = new Set<string>([...bySite.keys(), ...(depHead?.sites ?? []).map((s) => s.siteSlug)]);
  await Promise.all(
    [...sites].map(async (site) => {
      const run = await siteRunningCommit(dep.deploymentId, site, bySite.get(site) ?? null);
      const sc = (run?.config ?? depHead)?.sites.find((s) => s.siteSlug === site)?.config;
      if (sc) out.set(site, sc);
    }),
  );
  return out;
}
async function legacyHeadConfig(dep: StoredDeployment): Promise<Config | null> {
  if (!dep.head) return null;
  const r = await col.revisions(dep.deploymentId).doc(dep.head.revId).get();
  return r.exists ? decodeRevision(r.data()!).config : null;
}
async function headSiteConfig(dep: StoredDeployment, siteSlug: string): Promise<SiteConfig | null> {
  const a = await col.agents(dep.deploymentId).doc(siteSlug).get();
  const run = await siteRunningCommit(dep.deploymentId, siteSlug, (a.data()?.headRevId as string | undefined) ?? null);
  const cfg = run?.config ?? (await legacyHeadConfig(dep));
  return cfg?.sites.find((s) => s.siteSlug === siteSlug)?.config ?? null;
}
/** the persist setting of the signing site's head (agents): a legacy revision, else (O3) the agent-branch commit with that id */
async function signerHeadConfig(deploymentId: string, siteSlug: string, headRevId: string | null): Promise<SiteConfig | null> {
  const run = await siteRunningCommit(deploymentId, siteSlug, headRevId);
  return run?.config.sites.find((s) => s.siteSlug === siteSlug)?.config ?? null;
}

interface Target {
  dep: StoredDeployment;
  siteSlug: string;
  volumeSlug: string;
  key: string;
  state: StoredState;
  draftPersist: VolumePersist | null;
  headPersist: VolumePersist | null;
}
/** a persist-to-cloud volume of a deployment: persist on in the draft or in the head, or stored versions exist → else 404 */
async function loadTarget(c: Ctx, action: 'read' | 'write' | 'publish'): Promise<Target> {
  const dep = await loadDep(c);
  needDep(c, dep, 'deployment.config', action);
  const siteSlug = c.params.siteSlug!;
  const volumeSlug = c.params.volumeSlug!;
  if (!RE.slug.test(siteSlug) || !RE.volumeSlug.test(volumeSlug)) throw E.notFound('volume not found');
  const key = vcKey(siteSlug, volumeSlug);
  const [s, head] = await Promise.all([vcCol(dep.deploymentId).doc(key).get(), headSiteConfig(dep, siteSlug)]);
  const draftPersist = persistOf(draftSite(dep, siteSlug), volumeSlug);
  const headPersist = persistOf(head, volumeSlug);
  if (!draftPersist && !headPersist && !(s.exists && ((s.data()!.versionCount as number) ?? 0) > 0)) throw E.notFound(`volume ${volumeSlug} of site ${siteSlug} is not persisted to the cloud`);
  return { dep, siteSlug, volumeSlug, key, state: decodeState(dep.deploymentId, siteSlug, volumeSlug, s.data()), draftPersist, headPersist };
}

// ───────────────────────────────────────── trees + blobs
/** store the canonical tree JSON as files/<hex> (content-addressed); returns the tree hash */
export async function storeTree(tree: VolumeTree): Promise<string> {
  const input = volumeTreeHashInput(tree);
  const hex = sha256Hex(input);
  if (!(await exists(paths.file(hex)))) await writeBytes(paths.file(hex), Buffer.from(input, 'utf8'), 'application/json');
  return `sha256:${hex}`;
}
export async function readTree(treeHash: string): Promise<VolumeTree | null> {
  const t = await readText(paths.file(hexOf(treeHash)));
  if (t === null) return null;
  return normalizeVolumeTree(JSON.parse(t) as VolumeTree);
}
async function pool<T, R>(items: T[], n: number, f: (x: T) => Promise<R>): Promise<R[]> {
  const out: R[] = new Array(items.length);
  let i = 0;
  await Promise.all(
    Array.from({ length: Math.min(n, items.length) }, async () => {
      while (i < items.length) {
        const k = i++;
        out[k] = await f(items[k]!);
      }
    }),
  );
  return out;
}
/** sha256s (sha256:<hex>) not stored as files/<hex> */
export async function missingBlobs(shas: string[]): Promise<string[]> {
  const uniq = [...new Set(shas)];
  const ok = await pool(uniq, 25, (s) => exists(paths.file(hexOf(s))));
  return uniq.filter((_, i) => !ok[i]).sort();
}
const treeShas = (t: VolumeTree) => t.entries.map((e) => e.sha256);

function treeBodyIssues(tree: unknown, at = 'tree'): ValidationIssue[] {
  return volumeTreeIssues(tree, false).map((x) => ({ path: x.path ? `${at}.entries[${x.path}]` : at, code: x.code, message: x.message, severity: 'error' as const }));
}
function totalTooLarge(t: VolumeTree): ValidationIssue | null {
  const total = treeStats(t).totalBytes;
  return total > VOLUME_CONTENT_LIMITS.persistMaxBytes ? { path: 'tree', code: 'V_TOO_LARGE', message: `a persisted version is at most ${VOLUME_CONTENT_LIMITS.persistMaxBytes / 1048576} MiB in total`, severity: 'error' } : null;
}
function excludedIssues(t: VolumeTree, exclude: readonly string[]): ValidationIssue[] {
  const c = compileExcludes(exclude);
  const out: ValidationIssue[] = [];
  for (const e of t.entries) if (isExcluded(e.path, false, c)) out.push({ path: `tree.entries[${e.path}]`, code: 'V_VOLUME_CONTENT_EXCLUDED', message: `${e.path} matches the volume's exclude patterns (it could never be snapshotted)`, severity: 'error' });
  for (const d of t.dirs) if (isExcluded(d, true, c)) out.push({ path: `tree.dirs[${d}]`, code: 'V_VOLUME_CONTENT_EXCLUDED', message: `folder ${d} matches the volume's exclude patterns`, severity: 'error' });
  return out;
}
const blobMissing = (missing: string[]) => {
  const e = E.invalid([{ path: 'tree', code: 'V_BLOB_MISSING', message: `${missing.length} file(s) of the tree are not uploaded (${missing.slice(0, 3).join(', ')}${missing.length > 3 ? ', …' : ''})`, severity: 'error' }]);
  (e.details as Doc).missing = missing;
  return e;
};

/** stored version → API (docs written before restoredFromVersionId existed default it to null) */
const decodeVersion = (d: Doc): VolumeContentVersion => ({ ...(d as unknown as VolumeContentVersion), restoredFromVersionId: (d.restoredFromVersionId as string | null | undefined) ?? null });
async function loadVersion(deploymentId: string, key: string, versionId: string, tx?: Tx): Promise<VolumeContentVersion | null> {
  const ref = versionsCol(deploymentId, key).doc(versionId);
  const s = tx ? await tx.get(ref) : await ref.get();
  return s.exists ? decodeVersion(s.data()!) : null;
}

/** the newest SNAPSHOT ancestor of a version through the baseVersionId chain (a snapshot is its own base) */
export async function snapshotAncestor(deploymentId: string, key: string, v: VolumeContentVersion): Promise<VolumeContentVersion | null> {
  let cur: VolumeContentVersion | null = v;
  for (let hops = 0; cur && hops < 100; hops++) {
    if (cur.origin === 'snapshot') return cur;
    if (!cur.baseVersionId) return null;
    cur = await loadVersion(deploymentId, key, cur.baseVersionId);
  }
  return null;
}

interface NewVersion {
  origin: VolumeContentVersion['origin'];
  tree: VolumeTree;
  treeHash: string;
  baseVersionId: string | null;
  /** restore: the version whose tree was copied (else null) */
  restoredFromVersionId?: string | null;
  note: string;
  createdBy: string;
  trigger?: VolumeSnapshotTrigger | null;
  excludedCount?: number;
  skippedCount?: number;
  skipped?: VolumeContentVersion['skipped'];
  agentSnapshotId?: string | null;
  requestId?: string | null;
  pushId?: string | null;
}
/** a version doc (seq = state.lastSeq + 1); changes against the previous latest tree */
function buildVersion(t: { deploymentId: string; siteSlug: string; volumeSlug: string }, state: StoredState, n: NewVersion, prevTree: VolumeTree | null): VolumeContentVersion {
  const st = treeStats(n.tree);
  const changes = { added: 0, removed: 0, modified: 0 };
  for (const d of diffVolumeTrees(prevTree, n.tree)) changes[d.kind === 'changed' ? 'modified' : d.kind]++;
  return {
    versionId: ulid(),
    seq: state.lastSeq + 1,
    origin: n.origin,
    treeHash: n.treeHash,
    createdAt: nowIso(),
    createdBy: n.createdBy,
    deploymentId: t.deploymentId,
    siteSlug: t.siteSlug,
    volumeSlug: t.volumeSlug,
    trigger: n.trigger ?? null,
    fileCount: st.fileCount,
    dirCount: st.dirCount,
    totalBytes: st.totalBytes,
    baseVersionId: n.baseVersionId,
    restoredFromVersionId: n.restoredFromVersionId ?? null,
    note: n.note,
    excludedCount: n.excludedCount ?? 0,
    skippedCount: n.skippedCount ?? 0,
    skipped: n.skipped ?? [],
    agentSnapshotId: n.agentSnapshotId ?? null,
    requestId: n.requestId ?? null,
    pushId: n.pushId ?? null,
    changes,
  };
}
const versionCreatedEntry = (v: VolumeContentVersion, actor: string) => ({
  type: 'volume-content.version-created',
  summary: `Volume ${v.volumeSlug} of site ${v.siteSlug}: version ${v.seq} (${v.origin}) — ${v.fileCount} file(s), +${v.changes.added} −${v.changes.removed} ~${v.changes.modified}`,
  data: { siteSlug: v.siteSlug, volumeSlug: v.volumeSlug, versionId: v.versionId, seq: v.seq, origin: v.origin, treeHash: v.treeHash, fileCount: v.fileCount, totalBytes: v.totalBytes, baseVersionId: v.baseVersionId, added: v.changes.added, removed: v.changes.removed, modified: v.changes.modified },
  actor,
});

function vNote(v: unknown, at = 'note'): string {
  if (v === undefined || v === null) return '';
  if (typeof v !== 'string') throw E.bad(at, 'V_TYPE', `${at} must be a string`);
  if (v.length > VOLUME_CONTENT_LIMITS.noteMax) throw E.bad(at, 'V_LENGTH', `${at}: at most ${VOLUME_CONTENT_LIMITS.noteMax} characters`);
  return v;
}
const vUlidOrNull = (v: unknown, at: string): string | null => {
  if (v === null || v === undefined) return null;
  if (typeof v !== 'string' || !RE.ulid.test(v)) throw E.bad(at, 'V_FORMAT', `${at} must be a ULID or null`);
  return v;
};

/** write a new version in one transaction: state re-read, base check, seq, latest (+ lastSnapshot), _cloud log */
async function commitVersion(o: {
  deploymentId: string;
  siteSlug: string;
  volumeSlug: string;
  key: string;
  build: (state: StoredState) => NewVersion;
  /** throw inside the tx (409 …) given the fresh state; return an existing version to answer with instead */
  check?: (state: StoredState, tx: Tx) => Promise<VolumeContentVersion | null>;
  prevTree: VolumeTree | null;
  prevLatestId: string | null;
  log?: (v: VolumeContentVersion) => { type: string; summary: string; data: Doc; actor: string | null; origin?: 'agent' | 'cloud' } | null;
  extraState?: (state: StoredState, v: VolumeContentVersion) => Partial<StoredState>;
}): Promise<{ version: VolumeContentVersion; created: boolean }> {
  const depRef = col.deployments().doc(o.deploymentId);
  const stRef = vcCol(o.deploymentId).doc(o.key);
  return db().runTransaction(async (tx) => {
    const [ds, ss] = await Promise.all([tx.get(depRef), tx.get(stRef)]);
    if (!ds.exists) throw E.notFound('deployment not found');
    const state = decodeState(o.deploymentId, o.siteSlug, o.volumeSlug, ss.data());
    const existing = o.check ? await o.check(state, tx) : null;
    if (existing) return { version: existing, created: false };
    // the previous tree was read outside the tx: a concurrent new latest → retry the whole call (409 for edits via check)
    if ((state.latest?.versionId ?? null) !== o.prevLatestId) throw E.conflict('a newer version arrived; try again', { latest: state.latest });
    const v = buildVersion({ deploymentId: o.deploymentId, siteSlug: o.siteSlug, volumeSlug: o.volumeSlug }, state, o.build(state), o.prevTree);
    tx.create(versionsCol(o.deploymentId, o.key).doc(v.versionId), v);
    const next: Partial<StoredState> = {
      deploymentId: o.deploymentId,
      siteSlug: o.siteSlug,
      volumeSlug: o.volumeSlug,
      latest: summaryOf(v),
      versionCount: state.versionCount + 1,
      lastSeq: v.seq,
      updatedAt: v.createdAt,
      ...(v.origin === 'snapshot' ? { lastSnapshot: summaryOf(v) } : {}),
      ...(o.extraState ? o.extraState(state, v) : {}),
    };
    const merged = { ...state, ...next };
    next._open = openOf(merged);
    tx.set(stRef, next, { merge: true });
    const entry = o.log?.(v);
    if (entry) {
      const dep = decodeDeployment(ds.data()!);
      const log = new CloudLog(o.deploymentId, dep);
      log.add(tx, entry);
      tx.update(depRef, log.depUpdate());
    }
    return { version: v, created: true };
  });
}

/** Reply 200 (an existing version answered instead of a new one; ok = 201 otherwise) */
const ok200 = (body: unknown) => new Reply(200, { ...jsonHeaders }, JSON.stringify(body));

// ───────────────────────────────────────── cloud view + agent blob access (hooks for routes/agent.ts)
/** CloudView.volumeContent: every volume with a pending push or an open snapshot request (all sites). */
export async function volumeContentCloudItems(deploymentId: string): Promise<VolumeContentCloudItem[]> {
  const q = await vcCol(deploymentId).where('_open', '==', true).get();
  return q.docs
    .map((d) => {
      const x = d.data();
      const push = (x.push as VolumeContentPush | null) ?? null;
      return { siteSlug: x.siteSlug as string, volumeSlug: x.volumeSlug as string, push: push?.status === 'pending' ? push : null, snapshotRequest: (x.snapshotRequest as VolumeContentCloudItem['snapshotRequest']) ?? null };
    })
    .filter((i) => i.push || i.snapshotRequest)
    .sort((a, b) => (a.siteSlug === b.siteSlug ? (a.volumeSlug < b.volumeSlug ? -1 : 1) : a.siteSlug < b.siteSlug ? -1 : 1));
}

const pushBlobCache = new Map<string, { at: number; hexes: Set<string> }>();
/**
 * getAgentBlob: the tree-JSON blobs + entry blobs of a PENDING push's version and its base version, for the signing site
 * only (the agent plans and applies the push). Cached per deployment epoch for 60 s.
 */
export async function pushBlobAllowed(deploymentId: string, siteSlug: string, epoch: number, hex: string): Promise<boolean> {
  const ck = `${deploymentId}:${siteSlug}:${epoch}`;
  let hit = pushBlobCache.get(ck);
  if (!hit || Date.now() - hit.at > 60_000) {
    const hexes = new Set<string>();
    const q = await vcCol(deploymentId).where('siteSlug', '==', siteSlug).where('_open', '==', true).get();
    for (const d of q.docs) {
      const p = d.data().push as VolumeContentPush | null;
      if (p?.status !== 'pending') continue;
      for (const th of [p.treeHash, p.baseTreeHash]) {
        if (!th) continue;
        hexes.add(hexOf(th));
        const t = await readTree(th);
        for (const e of t?.entries ?? []) hexes.add(hexOf(e.sha256));
      }
    }
    hit = { at: Date.now(), hexes };
    pushBlobCache.set(ck, hit);
    if (pushBlobCache.size > 500) for (const [k, x] of pushBlobCache) if (Date.now() - x.at > 60_000) pushBlobCache.delete(k);
  }
  return hit.hexes.has(hex);
}

/** HeartbeatRequest.volumeSync: shape check (null = ok / absent) */
export function volumeSyncError(v: unknown): string | null {
  if (v === undefined || v === null) return null;
  if (!Array.isArray(v)) return 'volumeSync must be a list';
  if (v.length > 200) return 'volumeSync has at most 200 entries';
  const states = ['in-sync', 'live-changed', 'cloud-ahead', 'conflict', 'never-snapshotted', 'pending-upload', 'error'];
  const shaOrNull = (x: unknown) => x === null || x === undefined || (typeof x === 'string' && RE.sha256.test(x));
  for (const r of v) {
    if (!isObj(r)) return 'volumeSync entries must be objects';
    if (typeof r.volumeSlug !== 'string' || !RE.volumeSlug.test(r.volumeSlug)) return 'volumeSync[].volumeSlug must be a volume slug';
    if (!states.includes(r.state as string)) return `volumeSync[].state must be ${states.join(' | ')}`;
    if (!shaOrNull(r.liveTreeHash) || !shaOrNull(r.snapshotTreeHash)) return 'volumeSync[] tree hashes must be "sha256:" + 64 hex or null';
    if (r.liveChanged !== undefined && typeof r.liveChanged !== 'boolean') return 'volumeSync[].liveChanged must be a boolean';
    if (r.queued !== undefined && !isInt(r.queued)) return 'volumeSync[].queued must be an integer ≥ 0';
    if (r.scannedAt !== undefined && r.scannedAt !== null && !isIso(r.scannedAt)) return 'volumeSync[].scannedAt must be an ISO time or null';
    if (r.lastError !== undefined && r.lastError !== null && typeof r.lastError !== 'string') return 'volumeSync[].lastError must be a string or null';
  }
  return null;
}
/** HeartbeatRequest.volumeSync → state.live (+ reportedAt = server receive time) per volume. */
const LIVE_REFRESH_MS = 10 * 60_000;
/** compared to decide whether a heartbeat report changed (scannedAt / reportedAt ignored) */
const liveKey = (l: Doc | null | undefined) => (l ? JSON.stringify([l.state, l.liveTreeHash ?? null, l.snapshotTreeHash ?? null, !!l.liveChanged, l.queued ?? 0, l.lastError ?? null]) : null);
export async function storeVolumeSync(deploymentId: string, siteSlug: string, list: VolumeSyncReport[] | null | undefined, now: string): Promise<void> {
  if (!Array.isArray(list) || !list.length) return;
  const refs = list.map((r) => vcCol(deploymentId).doc(vcKey(siteSlug, r.volumeSlug)));
  const stored = await db().getAll(...refs);
  const b = db().batch();
  let writes = 0;
  for (const [i, r] of list.entries()) {
    const live: VolumeSyncReport & { reportedAt: string } = {
      volumeSlug: r.volumeSlug,
      state: r.state,
      liveTreeHash: r.liveTreeHash ?? null,
      snapshotTreeHash: r.snapshotTreeHash ?? null,
      liveChanged: !!r.liveChanged,
      queued: r.queued ?? 0,
      scannedAt: r.scannedAt ?? null,
      lastError: typeof r.lastError === 'string' ? r.lastError.slice(0, 2000) : null,
      reportedAt: now,
    };
    // unchanged (all but scannedAt) and reported within 10 min → no write
    const prev = stored[i]?.data()?.live as Doc | undefined;
    if (prev && liveKey(prev) === liveKey(live as unknown as Doc) && Date.parse(now) - Date.parse(String(prev.reportedAt)) < LIVE_REFRESH_MS) continue;
    writes++;
    b.set(vcCol(deploymentId).doc(vcKey(siteSlug, r.volumeSlug)), { deploymentId, siteSlug, volumeSlug: r.volumeSlug, live }, { merge: true });
  }
  if (writes) await b.commit();
}

// ───────────────────────────────────────── daily: prune AUTO snapshots beyond the newest versionsKept per volume
export async function pruneVolumeContent(): Promise<number> {
  const keep = VOLUME_CONTENT_LIMITS.versionsKept;
  const states = await db().collectionGroup('volumeContent').where('versionCount', '>', keep).get();
  let pruned = 0;
  for (const st of states.docs) {
    const s = st.data();
    const vs = await st.ref.collection('versions').orderBy('seq', 'desc').get();
    const pushes = await st.ref.collection('pushes').get();
    const pinned = new Set<string>();
    for (const p of pushes.docs) {
      const x = p.data() as VolumeContentPush;
      for (const id of [x.versionId, x.baseVersionId, x.result?.afterVersionId]) if (id) pinned.add(id);
    }
    for (const v of vs.docs) {
      const b = v.data().baseVersionId as string | null;
      if (b) pinned.add(b); // a base of another version (the push base chain)
    }
    for (const x of [s.latest, s.lastSnapshot] as (VolumeContentVersionSummary | null)[]) if (x) pinned.add(x.versionId);
    const doomed = vs.docs.slice(keep).filter((v) => {
      const x = v.data() as VolumeContentVersion;
      return x.origin === 'snapshot' && x.trigger === 'auto' && !pinned.has(x.versionId);
    });
    if (!doomed.length) continue;
    for (let i = 0; i < doomed.length; i += 400) {
      const b = db().batch();
      for (const d of doomed.slice(i, i + 400)) b.delete(d.ref);
      await b.commit();
    }
    await db().runTransaction(async (tx) => {
      const cur = await tx.get(st.ref);
      tx.update(st.ref, { versionCount: Math.max(0, ((cur.data()?.versionCount as number) ?? 0) - doomed.length) });
    });
    pruned += doomed.length;
  }
  return pruned;
}

// ───────────────────────────────────────── publish / diff: texts of starter content entries that render expressions
/** sha256 → text of every tree / single seed entry with render 'expressions' (≤ editorTextMaxBytes, UTF-8 text only). */
export async function seedBlobContents(config: Pick<Config, 'sites'>): Promise<Record<string, string>> {
  const shas = new Set<string>();
  for (const s of config.sites ?? [])
    for (const v of s.config?.volumes ?? []) {
      const sd = v?.seed;
      if (sd?.kind === 'tree') for (const e of sd.entries ?? []) if (e?.render === 'expressions' && typeof e.sha256 === 'string' && RE.sha256.test(e.sha256)) shas.add(e.sha256);
      if (sd?.kind === 'single' && sd.render === 'expressions' && typeof sd.sha256 === 'string' && RE.sha256.test(sd.sha256)) shas.add(sd.sha256);
    }
  const out: Record<string, string> = {};
  await pool([...shas], 10, async (sha) => {
    const p = paths.file(hexOf(sha));
    const size = await sizeOf(p);
    if (size === null || size > VOLUME_CONTENT_LIMITS.editorTextMaxBytes) return;
    const bytes = await blobs().get(p);
    if (!bytes || !looksLikeText(bytes)) return;
    out[sha] = bytes.toString('utf8');
  });
  return out;
}

// ───────────────────────────────────────── handlers
const uid = (c: Ctx) => c.account!.uid;

/** §21 #101 + #107 save an edit as a new version (the handler drops the stage whatever happens) */
async function createVersionBody(c: Ctx, t: Target, b: Record<string, unknown>, stageId: string | undefined) {
  const baseVersionId = vUlidOrNull(b.baseVersionId, 'baseVersionId');
  const note = vNote(b.note);
  const ti = treeBodyIssues(b.tree);
  if (ti.length) throw E.invalid(ti);
  const tree = normalizeVolumeTree(b.tree as VolumeTree);
  const big = totalTooLarge(tree);
  if (big) throw E.invalid([big]);
  const persist = t.draftPersist ?? t.headPersist;
  const ex = excludedIssues(tree, persist?.exclude ?? []);
  if (ex.length) throw E.invalid(ex);
  if ((t.state.latest?.versionId ?? null) !== baseVersionId) throw E.conflict('a newer version arrived (reload to see it)', { latest: t.state.latest });
  if (stageId) {
    // §21 #107: staged blobs verified + promoted before the version is written
    const needs = [...new Map(tree.entries.map((e) => [e.sha256, { sha256: e.sha256, sizeBytes: e.size }])).values()];
    await ensureBlobs({ needs, stageId, uid: uid(c), fault: testFault(c) });
  } else {
    const missing = await missingBlobs(treeShas(tree));
    if (missing.length) throw blobMissing(missing);
  }
  const treeHash = await storeTree(tree);
  // identical tree → the current latest (nothing new)
  if (t.state.latest && t.state.latest.treeHash === treeHash) {
    const v = await loadVersion(t.dep.deploymentId, t.key, t.state.latest.versionId);
    if (v) return ok200({ version: v, tree });
  }
  const prevTree = t.state.latest ? await readTree(t.state.latest.treeHash) : null;
  const r = await commitVersion({
    deploymentId: t.dep.deploymentId,
    siteSlug: t.siteSlug,
    volumeSlug: t.volumeSlug,
    key: t.key,
    prevTree,
    prevLatestId: baseVersionId,
    build: () => ({ origin: 'edit', tree, treeHash, baseVersionId, note, createdBy: uid(c) }),
    log: (v) => versionCreatedEntry(v, uid(c)),
  });
  return { version: r.version, tree };
}

export const volumeContentHandlers: Pick<
  Handlers,
  | 'listVolumeContent'
  | 'getVolumeContent'
  | 'listVolumeContentVersions'
  | 'getVolumeContentVersion'
  | 'createVolumeContentVersion'
  | 'restoreVolumeContentVersion'
  | 'requestVolumeSnapshot'
  | 'sendVolumeContent'
  | 'cancelVolumeContentPush'
  | 'agentCheckBlobs'
  | 'agentPutBlob'
  | 'agentCreateVolumeSnapshot'
  | 'agentReportVolumePush'
> = {
  async listVolumeContent(c) {
    const dep = await loadDep(c);
    needDep(c, dep, 'deployment.config', 'read');
    const [states, heads] = await Promise.all([vcCol(dep.deploymentId).get(), headSiteConfigs(dep)]);
    const stored = new Map(states.docs.map((d) => [d.id, d.data()] as const));
    const keys = new Set<string>();
    for (const s of dep.draft.sites) for (const v of s.config.volumes ?? []) if (persistOf(s.config, v.volumeSlug)) keys.add(vcKey(s.siteSlug, v.volumeSlug));
    for (const [site, sc] of heads) for (const v of sc.volumes ?? []) if (persistOf(sc, v.volumeSlug)) keys.add(vcKey(site, v.volumeSlug));
    for (const [k, d] of stored) if (((d.versionCount as number) ?? 0) > 0) keys.add(k);
    const items = [...keys].sort().map((k) => {
      const i = k.indexOf(':');
      const site = k.slice(0, i);
      const vol = k.slice(i + 1);
      return stateView(decodeState(dep.deploymentId, site, vol, stored.get(k)), persistOf(draftSite(dep, site), vol), !!persistOf(heads.get(site), vol));
    });
    return { items };
  },

  async getVolumeContent(c) {
    const t = await loadTarget(c, 'read');
    return stateView(t.state, t.draftPersist, !!t.headPersist);
  },

  async listVolumeContentVersions(c) {
    const t = await loadTarget(c, 'read');
    const limit = clampLimit(c, 50, 200);
    const cur = c.query.get('cursor');
    let q = versionsCol(t.dep.deploymentId, t.key).orderBy('seq', 'desc');
    if (cur) {
      const n = Number(cur);
      if (!Number.isInteger(n) || n < 1) throw E.bad('cursor', 'V_FORMAT', 'bad cursor');
      q = q.startAfter(n);
    }
    const s = await q.limit(limit + 1).get();
    const items = s.docs.slice(0, limit).map((d) => decodeVersion(d.data()));
    return { items, cursor: s.docs.length > limit ? String(items.at(-1)!.seq) : null };
  },

  async getVolumeContentVersion(c) {
    const t = await loadTarget(c, 'read');
    const v = await loadVersion(t.dep.deploymentId, t.key, c.params.versionId!);
    if (!v) throw E.notFound('version not found');
    const tree = await readTree(v.treeHash);
    if (!tree) throw E.notFound('the tree of this version is not stored');
    return { version: v, tree };
  },

  async createVolumeContentVersion(c) {
    const t = await loadTarget(c, 'write');
    const b = asObject(c.json());
    onlyKeys(b, ['baseVersionId', 'tree', 'note', 'stageId']);
    const stageId = optStageId(b);
    try {
      return await createVersionBody(c, t, b, stageId);
    } finally {
      await dropStage(stageId, uid(c));
    }
  },
  async restoreVolumeContentVersion(c) {
    const t = await loadTarget(c, 'write');
    const b = asObject(c.json());
    onlyKeys(b, ['note']);
    const note = vNote(b.note);
    const src = await loadVersion(t.dep.deploymentId, t.key, c.params.versionId!);
    if (!src) throw E.notFound('version not found');
    const tree = await readTree(src.treeHash);
    if (!tree) throw E.notFound('the tree of this version is not stored');
    if (t.state.latest && t.state.latest.treeHash === src.treeHash) {
      const v = await loadVersion(t.dep.deploymentId, t.key, t.state.latest.versionId);
      if (v) return ok200({ version: v, tree });
    }
    const prevLatestId = t.state.latest?.versionId ?? null;
    const prevTree = t.state.latest ? await readTree(t.state.latest.treeHash) : null;
    const r = await commitVersion({
      deploymentId: t.dep.deploymentId,
      siteSlug: t.siteSlug,
      volumeSlug: t.volumeSlug,
      key: t.key,
      prevTree,
      prevLatestId,
      // base = the latest at restore time (a Send plans against what the site had then); restoredFrom = the copied version
      build: () => ({ origin: 'restore', tree, treeHash: src.treeHash, baseVersionId: prevLatestId, restoredFromVersionId: src.versionId, note: note || `restored version ${src.seq}`, createdBy: uid(c) }),
      log: (v) => versionCreatedEntry(v, uid(c)),
    });
    return { version: r.version, tree };
  },

  async requestVolumeSnapshot(c) {
    const t = await loadTarget(c, 'write');
    if (!t.headPersist) throw E.bad('persist', 'V_VOLUME_PERSIST', `volume ${t.volumeSlug} is not persisted to the cloud in the revision site ${t.siteSlug} runs (publish + apply it first)`);
    const depRef = col.deployments().doc(t.dep.deploymentId);
    const stRef = vcCol(t.dep.deploymentId).doc(t.key);
    const state = await db().runTransaction(async (tx) => {
      const [ds, ss] = await Promise.all([tx.get(depRef), tx.get(stRef)]);
      const dep = decodeDeployment(ds.data()!);
      const st = decodeState(t.dep.deploymentId, t.siteSlug, t.volumeSlug, ss.data());
      const req = { requestId: ulid(), createdAt: nowIso(), createdBy: uid(c) };
      const next: StoredState = { ...st, snapshotRequest: req, updatedAt: req.createdAt };
      next._open = openOf(next);
      tx.set(stRef, { deploymentId: t.dep.deploymentId, siteSlug: t.siteSlug, volumeSlug: t.volumeSlug, snapshotRequest: req, updatedAt: req.createdAt, _open: next._open }, { merge: true });
      const log = new CloudLog(dep.deploymentId, dep);
      log.add(tx, { type: 'volume-content.snapshot-requested', summary: `Snapshot of volume ${t.volumeSlug} requested from site ${t.siteSlug}`, data: { siteSlug: t.siteSlug, volumeSlug: t.volumeSlug, requestId: req.requestId }, actor: uid(c) });
      tx.update(depRef, { ...log.depUpdate(), agentEpoch: dep.agentEpoch + 1 });
      return next;
    });
    return stateView(state, t.draftPersist, !!t.headPersist);
  },

  async sendVolumeContent(c) {
    const t = await loadTarget(c, 'publish');
    const b = asObject(c.json());
    onlyKeys(b, ['versionId', 'restart', 'note']);
    if (typeof b.versionId !== 'string' || !RE.ulid.test(b.versionId)) throw E.bad('versionId', 'V_REQUIRED', 'versionId (a version of this volume) is required');
    if (typeof b.restart !== 'boolean') throw E.bad('restart', 'V_TYPE', 'restart must be true / false');
    const note = vNote(b.note);
    const v = await loadVersion(t.dep.deploymentId, t.key, b.versionId);
    if (!v) throw E.notFound('version not found');
    const base = await snapshotAncestor(t.dep.deploymentId, t.key, v);
    const depRef = col.deployments().doc(t.dep.deploymentId);
    const stRef = vcCol(t.dep.deploymentId).doc(t.key);
    return db().runTransaction(async (tx) => {
      const [ds, ss] = await Promise.all([tx.get(depRef), tx.get(stRef)]);
      const dep = decodeDeployment(ds.data()!);
      const st = decodeState(t.dep.deploymentId, t.siteSlug, t.volumeSlug, ss.data());
      const now = nowIso();
      const push: VolumeContentPush = {
        pushId: ulid(),
        siteSlug: t.siteSlug,
        volumeSlug: t.volumeSlug,
        versionId: v.versionId,
        versionSeq: v.seq,
        treeHash: v.treeHash,
        baseVersionId: base?.versionId ?? null,
        baseTreeHash: base?.treeHash ?? null,
        restart: b.restart as boolean,
        note,
        status: 'pending',
        createdAt: now,
        createdBy: uid(c),
        resolvedAt: null,
        resolvedBy: null,
        result: null,
      };
      const old = st.push?.status === 'pending' ? st.push : null;
      if (old) tx.set(pushesCol(t.dep.deploymentId, t.key).doc(old.pushId), { ...old, status: 'superseded', resolvedAt: now, resolvedBy: uid(c) }, { merge: true });
      tx.create(pushesCol(t.dep.deploymentId, t.key).doc(push.pushId), push);
      tx.set(stRef, { deploymentId: t.dep.deploymentId, siteSlug: t.siteSlug, volumeSlug: t.volumeSlug, push, updatedAt: now, _open: true }, { merge: true });
      const log = new CloudLog(dep.deploymentId, dep);
      log.add(tx, {
        type: 'volume-content.sent',
        summary: `Version ${v.seq} of volume ${t.volumeSlug} sent to site ${t.siteSlug}${old ? ' (replaces the pending one)' : ''}`,
        data: { siteSlug: t.siteSlug, volumeSlug: t.volumeSlug, pushId: push.pushId, versionId: v.versionId, baseVersionId: push.baseVersionId, restart: push.restart, supersededPushId: old?.pushId ?? null },
        actor: uid(c),
      });
      tx.update(depRef, { ...log.depUpdate(), agentEpoch: dep.agentEpoch + 1 });
      return push;
    });
  },

  async cancelVolumeContentPush(c) {
    const t = await loadTarget(c, 'publish');
    const depRef = col.deployments().doc(t.dep.deploymentId);
    const stRef = vcCol(t.dep.deploymentId).doc(t.key);
    const state = await db().runTransaction(async (tx) => {
      const [ds, ss] = await Promise.all([tx.get(depRef), tx.get(stRef)]);
      const dep = decodeDeployment(ds.data()!);
      const st = decodeState(t.dep.deploymentId, t.siteSlug, t.volumeSlug, ss.data());
      if (st.push?.status !== 'pending') throw E.conflict('no push is pending');
      const now = nowIso();
      const push: VolumeContentPush = { ...st.push, status: 'cancelled', resolvedAt: now, resolvedBy: uid(c) };
      const next: StoredState = { ...st, push, updatedAt: now };
      next._open = openOf(next);
      tx.set(pushesCol(t.dep.deploymentId, t.key).doc(push.pushId), push, { merge: true });
      tx.set(stRef, { push, updatedAt: now, _open: next._open }, { merge: true });
      const log = new CloudLog(dep.deploymentId, dep);
      log.add(tx, { type: 'volume-content.push-cancelled', summary: `Pending send of volume ${t.volumeSlug} to site ${t.siteSlug} cancelled`, data: { siteSlug: t.siteSlug, volumeSlug: t.volumeSlug, pushId: push.pushId, versionId: push.versionId }, actor: uid(c) });
      tx.update(depRef, { ...log.depUpdate(), agentEpoch: dep.agentEpoch + 1 });
      return next;
    });
    return stateView(state, t.draftPersist, !!t.headPersist);
  },

  // ───────────────────────────────────── agent (site key)
  async agentCheckBlobs(c) {
    const b = asObject(c.json());
    const list = b.sha256s;
    if (!Array.isArray(list)) throw E.bad('sha256s', 'V_TYPE', 'sha256s must be a list');
    if (list.length > 1000) throw E.bad('sha256s', 'V_TOO_MANY', 'at most 1000 per call');
    list.forEach((s, i) => {
      if (typeof s !== 'string' || !RE.sha256.test(s)) throw E.bad(`sha256s[${i}]`, 'V_FORMAT', 'must be "sha256:" + 64 hex');
    });
    return { missing: await missingBlobs(list as string[]) };
  },

  async agentPutBlob(c) {
    const { deploymentId, siteSlug, agent } = c.agent!;
    const hex = hexOf(c.params.sha256!);
    if (!isHex64(hex)) throw E.bad('sha256', 'V_FORMAT', 'sha256 must be "sha256:" + 64 hex');
    const sc = await signerHeadConfig(deploymentId, siteSlug, agent.headRevId ?? null);
    if (!(sc?.volumes ?? []).some((v) => persistOf(sc, v.volumeSlug))) throw E.forbidden('the site runs no persist-to-cloud volume');
    if (c.body.length > VOLUME_CONTENT_LIMITS.snapshotFileMaxBytes) throw E.tooLarge(`a snapshot file is at most ${VOLUME_CONTENT_LIMITS.snapshotFileMaxBytes} bytes`);
    const got = sha256Hex(c.body);
    if (got !== hex) throw E.bad('sha256', 'V_FORMAT', `sha256 mismatch: the body is sha256:${got}`);
    const p = paths.file(hex);
    if (!(await exists(p))) await writeBytes(p, c.body);
    return { sha256: `sha256:${hex}`, sizeBytes: c.body.length };
  },

  async agentCreateVolumeSnapshot(c) {
    const { deploymentId, siteSlug, agent } = c.agent!;
    const volumeSlug = c.params.volumeSlug!;
    if (!RE.volumeSlug.test(volumeSlug)) throw E.bad('volumeSlug', 'V_FORMAT', 'bad volume slug');
    const sc = await signerHeadConfig(deploymentId, siteSlug, agent.headRevId ?? null);
    if (!persistOf(sc, volumeSlug)) throw E.forbidden(`volume ${volumeSlug} is not persisted to the cloud in the head of site ${siteSlug}`);
    const b = asObject(c.json()) as unknown as AgentVolumeSnapshotRequest & Doc;
    onlyKeys(b, ['snapshotId', 'trigger', 'takenAt', 'actor', 'tree', 'treeHash', 'excludedCount', 'skippedCount', 'skipped', 'requestId', 'pushId', 'note']);
    if (typeof b.snapshotId !== 'string' || !RE.ulid.test(b.snapshotId)) throw E.bad('snapshotId', 'V_FORMAT', 'snapshotId must be a ULID');
    const triggers: VolumeSnapshotTrigger[] = ['manual-cloud', 'manual-agent', 'auto', 'before-apply', 'after-apply'];
    if (!triggers.includes(b.trigger)) throw E.bad('trigger', 'V_ENUM', `trigger must be ${triggers.join(' | ')}`);
    if (!isIso(b.takenAt)) throw E.bad('takenAt', 'V_FORMAT', 'takenAt must be an ISO time');
    if (b.actor !== null && b.actor !== undefined && (typeof b.actor !== 'string' || b.actor.length > 320)) throw E.bad('actor', 'V_TYPE', 'actor must be a string or null');
    if (typeof b.treeHash !== 'string' || !RE.sha256.test(b.treeHash)) throw E.bad('treeHash', 'V_FORMAT', 'treeHash must be "sha256:" + 64 hex');
    if (!isInt(b.excludedCount ?? 0) || !isInt(b.skippedCount ?? 0)) throw E.bad('excludedCount', 'V_TYPE', 'excludedCount / skippedCount must be integers ≥ 0');
    const reasons = ['symlink', 'special', 'bad-name', 'too-large'];
    const skipped = b.skipped ?? [];
    if (!Array.isArray(skipped) || skipped.length > 100 || skipped.some((s) => !isObj(s) || typeof s.path !== 'string' || s.path.length > 4096 || !reasons.includes(s.reason as string))) throw E.bad('skipped', 'V_TYPE', `skipped must be ≤ 100 of { path, reason ${reasons.join(' | ')} }`);
    const requestId = vUlidOrNull(b.requestId, 'requestId');
    const pushId = vUlidOrNull(b.pushId, 'pushId');
    const note = vNote(b.note);
    const ti = treeBodyIssues(b.tree);
    if (ti.length) throw E.invalid(ti);
    const tree = normalizeVolumeTree(b.tree);
    const big = totalTooLarge(tree);
    if (big) throw E.invalid([big]);
    if (`sha256:${sha256Hex(volumeTreeHashInput(tree))}` !== b.treeHash) throw E.bad('treeHash', 'V_FORMAT', 'treeHash does not match sha256(JCS(canonical tree))');
    const key = vcKey(siteSlug, volumeSlug);
    // idempotent: this snapshotId stored before → the stored version (a store-and-forward retry)
    const prior = await versionsCol(deploymentId, key).where('agentSnapshotId', '==', b.snapshotId).limit(1).get();
    if (!prior.empty) return ok200({ version: decodeVersion(prior.docs[0]!.data()), existing: true });
    const missing = await missingBlobs(treeShas(tree));
    if (missing.length) throw blobMissing(missing);
    await storeTree(tree);
    // a concurrent new latest (another upload / an edit) → re-read the previous tree and retry
    for (let attempt = 1; ; attempt++) {
      try {
        return await storeSnapshot(attempt);
      } catch (e) {
        if (attempt >= 3 || !(e instanceof ApiError) || e.code !== 'conflict') throw e;
      }
    }
    async function storeSnapshot(attempt: number) {
    void attempt;
    const st0 = decodeState(deploymentId, siteSlug, volumeSlug, (await vcCol(deploymentId).doc(key).get()).data());
    const prevTree = st0.latest ? await readTree(st0.latest.treeHash) : null;
    const r = await commitVersion({
      deploymentId,
      siteSlug,
      volumeSlug,
      key,
      prevTree,
      prevLatestId: st0.latest?.versionId ?? null,
      check: async (_s, tx) => {
        const again = await tx.get(versionsCol(deploymentId, key).where('agentSnapshotId', '==', b.snapshotId).limit(1));
        return again.empty ? null : decodeVersion(again.docs[0]!.data());
      },
      build: () => ({
        origin: 'snapshot',
        tree,
        treeHash: b.treeHash,
        baseVersionId: null,
        note,
        createdBy: typeof b.actor === 'string' && b.actor ? b.actor : `agent:${siteSlug}`,
        trigger: b.trigger,
        excludedCount: b.excludedCount ?? 0,
        skippedCount: b.skippedCount ?? 0,
        skipped: (skipped as VolumeContentVersion['skipped']).map((s) => ({ path: s.path, reason: s.reason })),
        agentSnapshotId: b.snapshotId,
        requestId,
        pushId,
      }),
      // the answered cloud request is cleared (an older / other request stays open)
      extraState: (s) => (requestId && s.snapshotRequest?.requestId === requestId ? { snapshotRequest: null } : {}),
      // no _cloud entry: the agent logs volume-content.snapshot-uploaded in its own chain
    });
    if (!r.created) return ok200({ version: r.version, existing: true });
    return { version: r.version, existing: false };
    }
  },

  async agentReportVolumePush(c) {
    const { deploymentId, siteSlug } = c.agent!;
    const volumeSlug = c.params.volumeSlug!;
    const pushId = c.params.pushId!;
    if (!RE.volumeSlug.test(volumeSlug) || !RE.ulid.test(pushId)) throw E.notFound('push not found');
    const b = asObject(c.json()) as unknown as AgentVolumePushReport & Doc;
    onlyKeys(b, ['status', 'resolvedBy', 'resolvedAt', 'result']);
    if (!['applied', 'kept-live', 'failed'].includes(b.status)) throw E.bad('status', 'V_ENUM', 'status must be applied | kept-live | failed');
    if (typeof b.resolvedBy !== 'string' || !b.resolvedBy || b.resolvedBy.length > 320) throw E.bad('resolvedBy', 'V_REQUIRED', 'resolvedBy is required');
    if (!isIso(b.resolvedAt)) throw E.bad('resolvedAt', 'V_FORMAT', 'resolvedAt must be an ISO time');
    const r = b.result as unknown as Doc;
    if (!isObj(r)) throw E.bad('result', 'V_TYPE', 'result is required');
    const resolutions = ['overwrite', 'keep-live', 'per-file', 'fast-forward', 'up-to-date'];
    if (!resolutions.includes(r.resolution as string)) throw E.bad('result.resolution', 'V_ENUM', `resolution must be ${resolutions.join(' | ')}`);
    for (const k of ['written', 'deleted', 'keptLive']) if (!isInt(r[k])) throw E.bad(`result.${k}`, 'V_TYPE', `${k} must be an integer ≥ 0`);
    if (r.restarted !== undefined && (!Array.isArray(r.restarted) || r.restarted.length > 200 || r.restarted.some((x) => typeof x !== 'string'))) throw E.bad('result.restarted', 'V_TYPE', 'restarted must be a list of container slugs');
    if (r.error !== undefined && r.error !== null && typeof r.error !== 'string') throw E.bad('result.error', 'V_TYPE', 'error must be a string or null');
    const result: VolumeContentPushResult = {
      resolution: r.resolution as VolumeContentPushResult['resolution'],
      written: r.written as number,
      deleted: r.deleted as number,
      keptLive: r.keptLive as number,
      snapshotId: vUlidOrNull(r.snapshotId, 'result.snapshotId'),
      restarted: (r.restarted as string[] | undefined) ?? [],
      error: typeof r.error === 'string' ? r.error.slice(0, 2000) : null,
      afterVersionId: vUlidOrNull(r.afterVersionId, 'result.afterVersionId'),
    };
    const key = vcKey(siteSlug, volumeSlug);
    const pRef = pushesCol(deploymentId, key).doc(pushId);
    const stRef = vcCol(deploymentId).doc(key);
    const depRef = col.deployments().doc(deploymentId);
    return db().runTransaction(async (tx) => {
      const [ps, ss, ds] = await Promise.all([tx.get(pRef), tx.get(stRef), tx.get(depRef)]);
      if (!ps.exists) throw E.notFound('push not found');
      const cur = ps.data() as VolumeContentPush;
      if (cur.siteSlug !== siteSlug || cur.volumeSlug !== volumeSlug) throw E.notFound('push not found');
      // idempotent; a superseded / cancelled (or already resolved) push keeps its status
      if (cur.status !== 'pending') return cur;
      const push: VolumeContentPush = { ...cur, status: b.status, resolvedAt: b.resolvedAt, resolvedBy: b.resolvedBy, result };
      tx.set(pRef, push);
      const st = decodeState(deploymentId, siteSlug, volumeSlug, ss.data());
      if (st.push?.pushId === pushId) {
        const next: StoredState = { ...st, push };
        tx.set(stRef, { push, updatedAt: nowIso(), _open: openOf(next) }, { merge: true });
      }
      const dep = decodeDeployment(ds.data()!);
      tx.update(depRef, { agentEpoch: dep.agentEpoch + 1 });
      return push;
    });
  },
};
