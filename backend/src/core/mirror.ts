/**
 * §21 #106 GitHub mirror — configuration, branch identity, queue (enqueue side) and the status rows.
 *
 * Firestore (server only; firestore.rules deny every client access)
 *   githubMirror/config                       GithubMirrorDoc (+ internal: _repoKey, and in the EMULATOR only _apiBase,
 *                                             _stubExporter, _maxFileBytes — test hooks, ignored in production)
 *   githubMirror/config/branches/{docId}      MirrorBranchState (+ _lastOrderKey, _lastSeq, _leaseId)
 *   githubMirror/config/queue/{itemId}        MirrorQueueItem (itemId = mirrorItemId(…): enqueueing twice = one item)
 *   githubMirror/config/blobs/{sha256hex}     { repoKey, gitSha, size } — content already on GitHub (per repository)
 * The worker (mirror-worker.ts) drains one branch at a time in orderKey order.
 */
import {
  MIRROR_EXPORT_FORMAT,
  maskGithubToken,
  mirrorBranchDocId,
  mirrorBranchName,
  mirrorItemId,
  mirrorOrderKey,
  ulid,
  type GithubMirrorDoc,
  type GithubMirrorView,
  type MirrorBranchKind,
  type MirrorBranchRef,
  type MirrorBranchState,
  type MirrorQueueItem,
  type MirrorQueueOp,
} from '@opm/shared';
import { logger } from '../driver/index.js';
import { col, db, nowIso } from './fire.js';
import { env } from './env.js';
import { ensureSlug } from './slugs.js';
import { GITHUB_API_DEFAULT, GithubClient } from './github.js';

export const mcol = {
  config: () => db().collection('githubMirror').doc('config'),
  branches: () => db().collection('githubMirror').doc('config').collection('branches'),
  queue: () => db().collection('githubMirror').doc('config').collection('queue'),
  blobs: () => db().collection('githubMirror').doc('config').collection('blobs'),
};

export type StoredMirror = GithubMirrorDoc & { _repoKey?: string; _apiBase?: string; _stubExporter?: boolean; _maxFileBytes?: number; _scope?: string[] };
export const EMPTY_MIRROR: GithubMirrorDoc = {
  enabled: false,
  owner: '',
  repo: '',
  token: null,
  tokenSetAt: null,
  tokenSetBy: null,
  initializedAt: null,
  lastTest: null,
  pausedUntil: null,
  pauseReason: null,
  createdAt: '',
  createdBy: '',
  updatedAt: '',
  updatedBy: '',
};
export async function loadMirror(): Promise<StoredMirror> {
  const s = await mcol.config().get();
  return { ...EMPTY_MIRROR, ...((s.data() as StoredMirror | undefined) ?? {}) };
}
let cache: { at: number; m: StoredMirror } | null = null;
/** cached ≤ 15 s per instance (hooks on hot paths: heartbeat) */
export async function mirrorCached(): Promise<StoredMirror> {
  if (cache && Date.now() - cache.at < 15_000 && !env.emulator) return cache.m;
  const m = await loadMirror();
  cache = { at: Date.now(), m };
  return m;
}
export const invalidateMirrorCache = () => (cache = null);
export const isConfigured = (m: GithubMirrorDoc) => !!(m.owner && m.repo && m.token);
export const isActive = (m: GithubMirrorDoc) => m.enabled && isConfigured(m);
export const repoKeyOf = (m: GithubMirrorDoc) => `${m.owner.toLowerCase()}/${m.repo.toLowerCase()}`;
export const apiBaseOf = (m: StoredMirror) => (env.emulator && m._apiBase ? m._apiBase : GITHUB_API_DEFAULT);
export function clientOf(m: StoredMirror): GithubClient {
  return new GithubClient(m.token ?? '', m.owner, m.repo, apiBaseOf(m));
}
export const repoUrlOf = (m: GithubMirrorDoc) => (m.owner && m.repo ? `https://github.com/${m.owner}/${m.repo}` : null);

export async function mirrorView(m: StoredMirror): Promise<GithubMirrorView> {
  const [branches, pending] = await Promise.all([mcol.branches().select('status', 'pushedAt').get(), mcol.queue().where('state', 'in', ['pending', 'failed', 'running']).select('state').get()]);
  let synced = 0;
  let queued = 0;
  let errors = 0;
  let lastPushAt: string | null = null;
  for (const d of branches.docs) {
    const st = d.data().status as string;
    if (st === 'synced') synced++;
    else if (st === 'queued' || st === 'pushing') queued++;
    else if (st === 'error' || st === 'paused') errors++;
    const p = d.data().pushedAt as string | null;
    if (p && (!lastPushAt || p > lastPushAt)) lastPushAt = p;
  }
  return {
    configured: isConfigured(m),
    enabled: m.enabled,
    owner: m.owner,
    repo: m.repo,
    repoUrl: repoUrlOf(m),
    token: maskGithubToken(m.token),
    tokenSet: !!m.token,
    tokenSetAt: m.tokenSetAt,
    tokenSetBy: m.tokenSetBy,
    initializedAt: m.initializedAt,
    lastTest: m.lastTest,
    pausedUntil: m.pausedUntil,
    pauseReason: m.pauseReason,
    summary: { branches: branches.size, synced, queued, errors, pendingItems: pending.size, lastPushAt },
    exportFormat: MIRROR_EXPORT_FORMAT,
    updatedAt: m.updatedAt || null,
    updatedBy: m.updatedBy || null,
  };
}

// ───────────────────────────────────────── branch identity
export interface MirrorTarget {
  gitBranch: string;
  ref: MirrorBranchRef;
  label: string;
  entitySlug: string;
}
export const refOf = (kind: MirrorBranchKind, ids: { templateId?: string | null; deploymentId?: string | null; siteSlug?: string | null }): MirrorBranchRef => ({
  kind,
  templateId: kind === 'template' ? (ids.templateId ?? null) : null,
  deploymentId: kind === 'template' ? null : (ids.deploymentId ?? null),
  siteSlug: kind === 'agent' || kind === 'uncommitted' ? (ids.siteSlug ?? null) : null,
});
/** the git branch of a template / deployment / agent / uncommitted branch (assigns the entity slug when missing) */
export async function targetOf(ref: MirrorBranchRef): Promise<MirrorTarget> {
  if (ref.kind === 'template') {
    const slug = await ensureSlug('template', ref.templateId!);
    const t = await col.templates().doc(ref.templateId!).get();
    return { gitBranch: mirrorBranchName('template', slug), ref, label: String(t.data()?.label ?? slug), entitySlug: slug };
  }
  const slug = await ensureSlug('deployment', ref.deploymentId!);
  const d = await col.deployments().doc(ref.deploymentId!).get();
  const label = String(d.data()?.label ?? slug);
  if (ref.kind === 'deployment') return { gitBranch: mirrorBranchName('deployment', slug), ref, label, entitySlug: slug };
  const site = ref.siteSlug!;
  return { gitBranch: mirrorBranchName(ref.kind, slug, site), ref, label: ref.kind === 'agent' ? `${label} · ${site}` : `${label} · ${site} (uncommitted)`, entitySlug: slug };
}

// ───────────────────────────────────────── enqueue
export interface EnqueueOpts {
  op: MirrorQueueOp;
  opmId: string;
  seq: number | null;
  createdAt: string;
  reason: string;
  /** due time (default now) */
  at?: string;
}
/** Create a queue item (idempotent by itemId) + mark the branch queued. Returns false when it already existed. */
export async function enqueue(t: MirrorTarget, o: EnqueueOpts): Promise<boolean> {
  const now = nowIso();
  if (env.emulator) {
    // EMULATOR test hook: only the entities a test lists (the emulator DB holds every earlier test's data)
    const sc = (await mirrorCached())._scope;
    if (Array.isArray(sc) && !sc.includes(t.ref.templateId ?? t.ref.deploymentId ?? '')) return false;
  }
  const itemId = mirrorItemId(t.gitBranch, o.op, o.opmId);
  const item: MirrorQueueItem = {
    itemId,
    gitBranch: t.gitBranch,
    branch: t.ref,
    op: o.op,
    opmId: o.opmId,
    seq: o.seq,
    orderKey: mirrorOrderKey(o.createdAt, o.seq, o.opmId),
    state: 'pending',
    attempts: 0,
    nextAttemptAt: o.at ?? now,
    createdAt: now,
    startedAt: null,
    doneAt: null,
    gitSha: null,
    lastError: null,
    reason: o.reason,
  };
  try {
    await mcol.queue().doc(itemId).create(item);
  } catch (e) {
    const c = (e as { code?: unknown }).code;
    if (c === 6 || c === 'already-exists') return false;
    throw e;
  }
  await touchBranch(t, { status: 'queued' });
  return true;
}
/** create the branch row if missing / set fields */
export async function touchBranch(t: MirrorTarget, patch: Partial<MirrorBranchState> & Record<string, unknown>) {
  const ref = mcol.branches().doc(mirrorBranchDocId(t.gitBranch));
  await db().runTransaction(async (tx) => {
    const s = await tx.get(ref);
    const now = nowIso();
    const row: MirrorBranchState = { gitBranch: t.gitBranch, branch: t.ref, label: t.label, status: 'never', last: null, gitSha: null, gitUrl: null, treeUrl: null, pushedAt: null, queue: { pending: 0, failed: 0, nextAttemptAt: null }, lastError: null, notes: [], leaseUntil: null, updatedAt: now };
    if (!s.exists) {
      tx.create(ref, { ...row, ...patch, label: t.label, updatedAt: now });
    } else {
      const cur = s.data() as Partial<MirrorBranchState>;
      // a running push keeps "pushing"
      const p = { ...patch };
      if (p.status === 'queued' && cur.status === 'pushing') delete p.status;
      // the worker may have leased the branch first (acquire = set merge: no gitBranch / branch): the mirrorQueue trigger
      // of the item enqueue() created just before can run before this (the local outbox dispatcher fires at once) →
      // complete such a row with the fields it lacks (never overwrites a present field)
      const fill = Object.fromEntries(Object.entries(row).filter(([k]) => cur[k as keyof MirrorBranchState] === undefined));
      tx.update(ref, { ...fill, ...p, label: t.label, updatedAt: now });
    }
  });
}

/** Best-effort hook: never throws (a mirror problem must never fail an OPM write). */
export async function mirrorHook(name: string, f: () => Promise<unknown>): Promise<void> {
  try {
    const m = await mirrorCached();
    if (!isActive(m)) return;
    await f();
  } catch (e) {
    logger.warn('github mirror hook failed', { hook: name, error: (e as Error)?.message ?? String(e) });
  }
}

/** a run id for catch-up / rebuild items (Sync now / Resync / enable: one item per run per branch) */
export const runId = (p: string) => `${p}-${ulid()}`;

/** every mirrorable branch: templates, deployments (not deleted), agent branches of those, uncommitted ones with drift */
export async function allTargets(): Promise<MirrorTarget[]> {
  const out: MirrorTarget[] = [];
  const [tpls, deps] = await Promise.all([col.templates().select('label', 'slug').get(), col.deployments().where('deleted', '==', false).select('label', 'slug').get()]);
  for (const d of tpls.docs) out.push(await targetOf(refOf('template', { templateId: d.id })));
  for (const d of deps.docs) {
    out.push(await targetOf(refOf('deployment', { deploymentId: d.id })));
    const abs = await col.deployments().doc(d.id).collection('agentBranches').get();
    for (const a of abs.docs) {
      const data = a.data();
      if (data.head || !data.legacy) out.push(await targetOf(refOf('agent', { deploymentId: d.id, siteSlug: a.id })));
      const drift = data.drift as { items?: unknown[]; notes?: unknown[] } | undefined;
      if (drift && ((drift.items?.length ?? 0) > 0 || (drift.notes?.length ?? 0) > 0)) out.push(await targetOf(refOf('uncommitted', { deploymentId: d.id, siteSlug: a.id })));
    }
  }
  return out;
}

/** queue a catch-up (or rebuild) of every branch; returns the number of items created */
export async function enqueueAll(op: 'catch-up' | 'rebuild', reason: string, only?: string): Promise<{ enqueued: number; branches: number }> {
  const targets = (await allTargets()).filter((t) => !only || t.gitBranch === only);
  let n = 0;
  const id = runId(reason);
  for (const t of targets) {
    const kindOp: MirrorQueueOp = t.ref.kind === 'uncommitted' ? 'drift' : op;
    const opmId = t.ref.kind === 'uncommitted' ? `${id}` : id;
    if (await enqueue(t, { op: kindOp, opmId, seq: null, createdAt: '9999', reason })) n++;
  }
  return { enqueued: n, branches: targets.length };
}
