/**
 * §21 #104 branches + commits: Firestore storage of commits / merge bases / agent-branch mirrors, branch resolution,
 * uncommitted counts and the compat (pre-0.14) proposal writer. Pure merge logic: shared/src/branch.ts.
 *
 * Firestore:
 *   templates/{id}/commits/{commitId}                         Commit (_config, _data JSON strings; catalog null)
 *   deployments/{id}/commits/{commitId}                       Commit (_config, _catalog, _data JSON strings)
 *   deployments/{id}/agentBranches/{site}                     AgentBranchDoc
 *   deployments/{id}/agentBranches/{site}/commits/{commitId}  Commit (pushed by the agent)
 *   {templates|deployments}/{id}/mergeBases/{branchKeyDocId}  MergeBase (_base, _skipped JSON strings)
 * A commit whose JSON (config + catalog) is larger than COMMIT_INLINE_MAX lives in Storage `commits/<hash hex>.json`
 * (content-addressed, written BEFORE the transaction) and the doc keeps `_configRef`.
 */
import {
  branchKey,
  branchKeyDocId,
  commitRef,
  commitSummary,
  emptyConfig,
  supportsBranches,
  uncommittedCount,
  ulid,
  type AgentBranchDoc,
  type AgentDoc,
  type BranchRef,
  type BranchState,
  type Commit,
  type CommitAuthor,
  type CommitSummary,
  type Config,
  type MergeBase,
  type Proposal,
  type Revision,
  type RevisionCatalog,
} from '@opm/shared';
import { col, db, nowIso, type Tx } from './fire.js';
import { blobs } from '../driver/index.js';
import { E } from './errors.js';
import { normalizeStored } from './config.js';
import type { CloudLog } from './activities.js';
import type { Ctx } from './router.js';
import { decodeRevision, encodeRevision, type StoredDeployment } from './codec.js';

export const COMMIT_INLINE_MAX = 850 * 1024;

// ───────────────────────────────────────── collections
export const bcol = {
  templateCommits: (t: string) => col.templates().doc(t).collection('commits'),
  deploymentCommits: (d: string) => col.deployments().doc(d).collection('commits'),
  agentBranches: (d: string) => col.deployments().doc(d).collection('agentBranches'),
  agentCommits: (d: string, s: string) => col.deployments().doc(d).collection('agentBranches').doc(s).collection('commits'),
  mergeBases: (b: BranchRef) => (b.kind === 'template' ? col.templates().doc(b.templateId) : col.deployments().doc(b.deploymentId)).collection('mergeBases'),
};
export function commitsOf(b: BranchRef) {
  if (b.kind === 'template') return bcol.templateCommits(b.templateId);
  if (b.kind === 'deployment') return bcol.deploymentCommits(b.deploymentId);
  return bcol.agentCommits(b.deploymentId, b.siteSlug);
}

// ───────────────────────────────────────── codec
export function encodeCommit(c: Commit, external = false): Record<string, unknown> {
  const { config, catalog, data, ...rest } = c;
  const out: Record<string, unknown> = { ...rest, branchKey: branchKey(c.branch), _data: JSON.stringify(data ?? {}) };
  if (external) out._configRef = `commits/${c.hash.replace(/^sha256:/, '')}.json`;
  else {
    out._config = JSON.stringify(config);
    out._catalog = catalog ? JSON.stringify(catalog) : null;
  }
  return out;
}
/** Write the JSON of a big commit to Storage first (content-addressed; idempotent). Returns true when external. */
export async function storeCommitBody(c: Commit): Promise<boolean> {
  const body = JSON.stringify({ config: c.config, catalog: c.catalog });
  if (body.length <= COMMIT_INLINE_MAX) return false;
  await blobs().put(`commits/${c.hash.replace(/^sha256:/, '')}.json`, Buffer.from(body, 'utf8'), { contentType: 'application/json' });
  return true;
}
export async function decodeCommit(d: Record<string, unknown>, opts: { human?: boolean } = {}): Promise<Commit> {
  const { _config, _catalog, _configRef, _data, branchKey: _bk, ...rest } = d as Record<string, unknown> & { _config?: string; _catalog?: string | null; _configRef?: string; _data?: string };
  void _bk;
  let config: Config;
  let catalog: RevisionCatalog | null;
  if (_configRef) {
    const buf = await blobs().get(_configRef);
    if (!buf) throw Object.assign(new Error(`commit body ${_configRef} is not stored`), { code: 404 });
    const j = JSON.parse(buf.toString('utf8')) as { config: Config; catalog: RevisionCatalog | null };
    config = j.config;
    catalog = j.catalog;
  } else {
    config = _config ? (JSON.parse(_config) as Config) : emptyConfig();
    catalog = _catalog ? (JSON.parse(_catalog) as RevisionCatalog) : null;
  }
  return { ...(rest as unknown as Commit), config: opts.human ? normalizeStored(config) : config, catalog, data: _data ? JSON.parse(_data) : {} };
}
/** CommitSummary straight from the stored doc (no config read). */
export function summaryOfDoc(d: Record<string, unknown>): CommitSummary {
  const r = d as unknown as Commit;
  return { commitId: r.commitId, branch: r.branch, seq: r.seq, parentId: r.parentId, kind: r.kind, message: r.message, author: r.author, createdAt: r.createdAt, source: r.source ?? null, revertOf: r.revertOf ?? null, resetTo: r.resetTo ?? null, rollbackOf: r.rollbackOf ?? null, hash: r.hash, stats: r.stats };
}
export async function loadCommit(b: BranchRef, commitId: string, tx?: Tx, opts: { human?: boolean } = {}): Promise<Commit | null> {
  const ref = commitsOf(b).doc(commitId);
  const s = tx ? await tx.get(ref) : await ref.get();
  return s.exists ? decodeCommit(s.data()!, opts) : null;
}
/**
 * What a site runs (O2 / O3). The heartbeat `headRevId` is a legacy revision id (agents < 0.14) or, in branch mode, the
 * id of a commit on the site's agent branch (cloud mirror; a legacy site's head is a deployment commit) → its config +
 * seq, and the DEPLOYMENT seq it came from (the agent-branch head → mergedFrom). null = unknown (not pushed yet).
 */
export async function siteRunningCommit(deploymentId: string, siteSlug: string, headId: string | null): Promise<{ config: Config; seq: number; deploymentSeq: number | null } | null> {
  if (!headId) return null;
  const rev = await col.revisions(deploymentId).doc(headId).get();
  if (rev.exists) {
    const r = decodeRevision(rev.data()!);
    return { config: r.config, seq: r.seq, deploymentSeq: r.seq };
  }
  const ac = await loadCommit({ kind: 'agent', deploymentId, siteSlug }, headId);
  if (ac) {
    let deploymentSeq = ac.source?.branch?.kind === 'deployment' ? (ac.source.seq ?? null) : null;
    if (deploymentSeq === null) {
      const ab = await bcol.agentBranches(deploymentId).doc(siteSlug).get();
      const d = ab.exists ? (ab.data() as AgentBranchDoc) : null;
      if (d?.head?.commitId === headId) deploymentSeq = d.mergedFrom?.seq ?? null;
    }
    return { config: ac.config, seq: ac.seq, deploymentSeq };
  }
  const dc = await loadCommit({ kind: 'deployment', deploymentId }, headId);
  return dc ? { config: dc.config, seq: dc.seq, deploymentSeq: dc.seq } : null;
}
/** The deployment branch head config (§21 #104), else the legacy head revision (pre-migration deployments). */
export async function deploymentHeadConfig(dep: Pick<StoredDeployment, 'deploymentId' | 'branch' | 'head'>): Promise<Config | null> {
  if (dep.branch?.head) {
    const cm = await loadCommit({ kind: 'deployment', deploymentId: dep.deploymentId }, dep.branch.head.commitId);
    if (cm) return cm.config;
  }
  if (dep.head) {
    const r = await col.revisions(dep.deploymentId).doc(dep.head.revId).get();
    if (r.exists) return decodeRevision(r.data()!).config;
  }
  return null;
}
export async function writeCommit(tx: Tx, c: Commit, external: boolean) {
  tx.create(commitsOf(c.branch).doc(c.commitId), encodeCommit(c, external));
}

export function encodeMergeBase(m: MergeBase): Record<string, unknown> {
  const { base, skipped, ...rest } = m;
  return { ...rest, _base: JSON.stringify(base), _skipped: JSON.stringify(skipped ?? []) };
}
export function decodeMergeBase(d: Record<string, unknown>): MergeBase {
  const { _base, _skipped, ...rest } = d as Record<string, unknown> & { _base?: string; _skipped?: string };
  return { ...(rest as unknown as MergeBase), base: _base ? normalizeStored(JSON.parse(_base) as Config) : emptyConfig(), skipped: _skipped ? JSON.parse(_skipped) : [] };
}
export async function loadMergeBase(target: BranchRef, source: BranchRef, tx?: Tx): Promise<MergeBase | null> {
  const ref = bcol.mergeBases(target).doc(branchKeyDocId(branchKey(source)));
  const s = tx ? await tx.get(ref) : await ref.get();
  return s.exists ? decodeMergeBase(s.data()!) : null;
}
export function setMergeBase(tx: Tx, target: BranchRef, m: MergeBase) {
  tx.set(bcol.mergeBases(target).doc(branchKeyDocId(m.sourceKey)), encodeMergeBase(m));
}

// ───────────────────────────────────────── authors + states
export function personAuthor(c: Ctx): CommitAuthor {
  const a = c.account!;
  return { kind: 'person', email: a.email ?? a.uid, uid: a.uid, name: a.name ?? null, siteSlug: null };
}
export const SYSTEM_AUTHOR: CommitAuthor = { kind: 'system', email: 'system', uid: null, name: null, siteSlug: null };

export const isTemplateLike = (b: BranchRef) => b.kind === 'template';
/** compare secret values (deployment / agent on both sides) */
export const secretValuesFor = (a: BranchRef, b: BranchRef) => a.kind !== 'template' && b.kind !== 'template';
/** agent branches compare only the shared sections + their own site */
export function scopeFor(a: BranchRef, b: BranchRef): string | null {
  if (a.kind === 'agent') return a.siteSlug;
  if (b.kind === 'agent') return b.siteSlug;
  return null;
}
export function nextBranchState(prev: BranchState | undefined | null, head: Commit): BranchState {
  return { head: commitRef(head), origin: prev?.origin ?? null, commitCount: (prev?.commitCount ?? 0) + 1 };
}
/** §21 #105: 0.14.0 documents may still carry `branch.uncommitted` until migrate-j — never return / rewrite it */
export function stripUncommitted<B extends BranchState>(b: B): BranchState {
  const { uncommitted: _u, ...rest } = b as B & { uncommitted?: number };
  void _u;
  return rest;
}
export function countUncommitted(kind: 'template' | 'deployment', head: Config | null, working: Config): number {
  return uncommittedCount(head, working, { secretValues: kind === 'deployment' });
}

// ───────────────────────────────────────── compat proposals (§21 #104 j)
/** compat is needed unless at least one site is enrolled and every enrolled site runs agent ≥ 0.14 */
export async function needsCompat(deploymentId: string, tx?: Tx): Promise<boolean> {
  const q = col.agents(deploymentId).select('agentVersion');
  const s = tx ? await tx.get(q) : await q.get();
  if (!s.docs.length) return true;
  return s.docs.some((d) => !supportsBranches((d.data() as AgentDoc).agentVersion ?? null));
}
export interface CompatPrep {
  needed: boolean;
  open: Proposal[];
}
/** Reads for writeCompat — call BEFORE any write in the transaction. */
export async function prepCompat(tx: Tx, deploymentId: string): Promise<CompatPrep> {
  const needed = await needsCompat(deploymentId, tx);
  if (!needed) return { needed, open: [] };
  const s = await tx.get(col.proposals(deploymentId).where('status', 'in', ['published', 'staging', 'ready', 'approved', 'applying']));
  return { needed, open: s.docs.map((d) => d.data() as Proposal) };
}
/**
 * The old publish for agents < 0.14: revision (revId = commitId, parent = legacy head) + proposal "published",
 * superseding open ones. Skipped (null) when not needed, when an update runs, or when the commit has blockers.
 */
export function writeCompat(tx: Tx, dep: StoredDeployment, commit: Commit, prep: CompatPrep, log: CloudLog, blocked: boolean, actor: string): { revId: string; seq: number } | null {
  if (!prep.needed || blocked || !commit.catalog) return null;
  if (prep.open.some((p) => p.status === 'approved' || p.status === 'applying')) return null;
  const now = nowIso();
  const revision: Revision = {
    revId: commit.commitId,
    seq: (dep.head?.seq ?? 0) + 1,
    parentRevId: dep.head?.revId ?? null,
    origin: 'cloud',
    actor,
    createdAt: now,
    config: commit.config,
    hash: commit.hash,
    catalog: commit.catalog,
    catalogHash: commit.catalogHash!,
    summary: commitSummary(commit.message).slice(0, 200),
    kind: 'change',
    rollbackOf: null,
  };
  const sites: Proposal['sites'] = {};
  for (const sl of [...new Set(commit.config.sites.map((x) => x.siteSlug))].sort()) sites[sl] = { staging: 'pending', progress: 0, error: null, updatedAt: null };
  const proposal: Proposal = { revId: revision.revId, parentRevId: revision.parentRevId, seq: revision.seq, summary: revision.summary, origin: 'cloud', kind: 'change', createdAt: now, createdBy: actor, status: 'published', sites, approvedBy: null, approvedAt: null, updateId: null, result: null, supersededBy: null, updatedAt: now };
  for (const p of prep.open)
    if (p.status === 'published' || p.status === 'staging' || p.status === 'ready') {
      tx.update(col.proposals(dep.deploymentId).doc(p.revId), { status: 'superseded', supersededBy: revision.revId, updatedAt: now });
      log.add(tx, { type: 'proposal.superseded', summary: `Proposal seq ${p.seq} superseded by seq ${revision.seq}`, data: { revId: p.revId, byRevId: revision.revId }, actor });
    }
  tx.create(col.revisions(dep.deploymentId).doc(revision.revId), encodeRevision(revision));
  tx.create(col.proposals(dep.deploymentId).doc(revision.revId), proposal);
  log.add(tx, { type: 'proposal.published', summary: `Compat proposal seq ${revision.seq} (agents < 0.14): ${revision.summary}`, data: { revId: revision.revId, parentRevId: revision.parentRevId, seq: revision.seq, summary: revision.summary, commitId: commit.commitId, compat: true }, actor });
  return { revId: revision.revId, seq: revision.seq };
}

// ───────────────────────────────────────── agent-branch mirror helpers
export async function readAgentBranch(deploymentId: string, siteSlug: string, tx?: Tx): Promise<AgentBranchDoc | null> {
  const ref = bcol.agentBranches(deploymentId).doc(siteSlug);
  const s = tx ? await tx.get(ref) : await ref.get();
  return s.exists ? (s.data() as AgentBranchDoc) : null;
}
export const newCommitId = () => ulid();
export const nowTs = nowIso;
export { db, E };
