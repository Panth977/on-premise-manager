/**
 * §21 #104 agent side of branches: the deployment head in the cloud view, commit fetches, the agent-branch push
 * (store-and-forward mirror), heartbeat `branch`, and the legacy (pre-0.14) mirrors of reportHead / local proposals.
 */
import { onAgentHeartbeat } from '../core/mirror-hooks.js';
import {
  CLOUD_RECENT_COMMITS,
  commitRef,
  commitSummary,
  supportsBranches,
  verifyCommitLink,
  type AgentBranchDoc,
  type AgentBranchReport,
  type BranchRef,
  type CloudBranchView,
  type Commit,
  type CommitRef,
  type Revision,
} from '@opm/shared';
import { col, db, nowIso, type Tx } from '../core/fire.js';
import { E } from '../core/errors.js';
import { asObject } from '../core/util.js';
import { CloudLog } from '../core/activities.js';
import { decodeDeployment, type StoredDeployment } from '../core/codec.js';
import { bcol, decodeCommit, encodeCommit, storeCommitBody } from '../core/branches.js';
import { agentCommit } from './branches.js';
import type { Ctx, Handlers } from '../core/router.js';

const PUSH_MAX = 20;

export async function cloudBranchView(deploymentId: string, dep: StoredDeployment): Promise<CloudBranchView | undefined> {
  if (!dep.branch?.head) return { head: null, recent: [] };
  const q = await bcol.deploymentCommits(deploymentId).orderBy('seq', 'desc').select('commitId', 'seq', 'hash', 'message', 'author', 'createdAt', 'kind', 'catalogHash').limit(CLOUD_RECENT_COMMITS).get();
  const recent = q.docs.map((d) => d.data() as Commit).map((c) => commitRef(c));
  const h = q.docs.find((d) => d.id === dep.branch!.head!.commitId)?.data() as Commit | undefined;
  return { head: { ...dep.branch.head, catalogHash: h?.catalogHash ?? null }, recent };
}

/** commits whose images / blobs a site may download (head + newest deployment commits + agent-branch heads) */
export async function allowedCommits(deploymentId: string): Promise<{ config: Commit['config']; catalog: Commit['catalog'] }[]> {
  const out: { config: Commit['config']; catalog: Commit['catalog'] }[] = [];
  const [recent, branches] = await Promise.all([bcol.deploymentCommits(deploymentId).orderBy('seq', 'desc').limit(CLOUD_RECENT_COMMITS + 1).get(), bcol.agentBranches(deploymentId).get()]);
  for (const d of recent.docs) {
    const cm = await decodeCommit(d.data());
    out.push({ config: cm.config, catalog: cm.catalog });
  }
  for (const b of branches.docs) {
    const doc = b.data() as AgentBranchDoc;
    if (!doc.head || doc.legacy) continue;
    const s = await bcol.agentCommits(deploymentId, b.id).doc(doc.head.commitId).get();
    if (s.exists) {
      const cm = await decodeCommit(s.data()!);
      out.push({ config: cm.config, catalog: cm.catalog });
    }
  }
  return out;
}

/** heartbeat.branch (agents ≥ 0.14) → agentBranches/{site} (uncommitted, head, mergedFrom). */
export async function storeHeartbeatBranch(deploymentId: string, siteSlug: string, rep: unknown, agentVersion: string) {
  if (rep === undefined || rep === null) return;
  if (typeof rep !== 'object') throw E.bad('branch', 'V_TYPE', 'branch must be an object or null');
  const r = rep as AgentBranchReport;
  const u = r.uncommitted;
  const num = (n: unknown) => (Number.isInteger(n) && (n as number) >= 0 ? (n as number) : 0);
  const upd: Partial<AgentBranchDoc> = {
    siteSlug,
    legacy: false,
    agentVersion,
    uncommitted: u && typeof u === 'object' ? { working: num(u.working), drift: num(u.drift), notes: num(u.notes), scannedAt: typeof u.scannedAt === 'string' ? u.scannedAt : null } : null,
  };
  if (r.head && typeof r.head === 'object' && typeof r.head.commitId === 'string') upd.head = cleanRef(r.head);
  if (r.mergedFrom && typeof r.mergedFrom.commitId === 'string' && Number.isInteger(r.mergedFrom.seq)) upd.mergedFrom = { commitId: r.mergedFrom.commitId, seq: r.mergedFrom.seq };
  await bcol.agentBranches(deploymentId).doc(siteSlug).set(upd, { merge: true });
  // §21 #106 the agent is online: its branch head not on GitHub yet → catch-up (best effort, never fails the heartbeat)
  await onAgentHeartbeat(deploymentId, siteSlug, upd.head?.commitId ?? null);
}
const cleanRef = (h: CommitRef): CommitRef => ({ commitId: h.commitId, seq: h.seq, hash: h.hash, message: String(h.message ?? '').slice(0, 2000), author: h.author, createdAt: h.createdAt, kind: h.kind });

/** reportHead of a pre-0.14 agent → its agent branch mirror points at the revision (a deployment commit when migrated). */
export async function legacyHeadMirror(tx: Tx, deploymentId: string, siteSlug: string, revision: Revision, agentVersion: string | null) {
  if (supportsBranches(agentVersion)) return;
  const s = await tx.get(bcol.deploymentCommits(deploymentId).doc(revision.revId));
  const head: CommitRef = s.exists ? commitRef(s.data() as Commit) : { commitId: revision.revId, seq: revision.seq, hash: revision.hash, message: revision.summary || `revision ${revision.seq}`, author: { kind: 'person', email: revision.actor, uid: null, name: null, siteSlug: null }, createdAt: revision.createdAt, kind: 'legacy' };
  return () => tx.set(bcol.agentBranches(deploymentId).doc(siteSlug), { siteSlug, head, legacy: true, mergedFrom: s.exists ? { commitId: head.commitId, seq: head.seq } : null, agentVersion, pushedAt: nowIso() }, { merge: true });
}

export const agentBranchHandlers: Pick<Handlers, 'getAgentCommit' | 'listAgentCloudCommits' | 'pushAgentBranch'> = {
  async getAgentCommit(c) {
    const a = c.agent!;
    return { commit: await agentCommit(a.deploymentId, a.siteSlug, c.params.commitId!) };
  },
  async listAgentCloudCommits(c) {
    const a = c.agent!;
    const n = Number(c.query.get('limit') ?? 10);
    const limit = Number.isInteger(n) && n > 0 ? Math.min(50, n) : 10;
    const q = await bcol.deploymentCommits(a.deploymentId).orderBy('seq', 'desc').select('commitId', 'seq', 'hash', 'message', 'author', 'createdAt', 'kind').limit(limit).get();
    return { items: q.docs.map((d) => commitRef(d.data() as Commit)) };
  },
  async pushAgentBranch(c) {
    const { deploymentId, siteSlug, agent } = c.agent!;
    const b = asObject(c.json());
    if (!Array.isArray(b.commits) || !b.commits.length) throw E.bad('commits', 'V_REQUIRED', 'commits must be a non-empty list');
    if (b.commits.length > PUSH_MAX) throw E.bad('commits', 'V_LENGTH', `at most ${PUSH_MAX} commits per push`);
    const own: BranchRef = { kind: 'agent', deploymentId, siteSlug };
    const commits = (b.commits as Commit[]).slice().sort((x, y) => x.seq - y.seq);
    for (const [i, cm] of commits.entries()) {
      if (!cm || typeof cm !== 'object' || typeof cm.commitId !== 'string' || !Number.isInteger(cm.seq) || cm.seq < 1) throw E.bad(`commits[${i}]`, 'V_TYPE', 'a commit needs commitId, seq ≥ 1, config, hash');
      const br = cm.branch as BranchRef;
      if (!br || br.kind !== 'agent' || (br as { deploymentId: string }).deploymentId !== deploymentId || (br as { siteSlug: string }).siteSlug !== siteSlug) throw E.bad(`commits[${i}].branch`, 'V_REF', 'the branch must be this site (agent:<deploymentId>/<siteSlug>)');
      if (typeof cm.message !== 'string' || !cm.message.trim()) throw E.bad(`commits[${i}].message`, 'V_COMMIT_MESSAGE', 'a commit message is required');
      const err = verifyCommitLink(cm, null);
      if (err) throw E.bad(`commits[${i}].hash`, 'V_FORMAT', err);
    }
    const external = new Map<string, boolean>();
    for (const cm of commits) external.set(cm.commitId, await storeCommitBody(cm));
    const stored: string[] = [];
    const head = await db().runTransaction(async (tx) => {
      const coll = bcol.agentCommits(deploymentId, siteSlug);
      const brRef = bcol.agentBranches(deploymentId).doc(siteSlug);
      const depRef = col.deployments().doc(deploymentId);
      const [existing, bs, ds] = await Promise.all([Promise.all(commits.map((cm) => tx.get(coll.doc(cm.commitId)))), tx.get(brRef), tx.get(depRef)]);
      const parents = await Promise.all(commits.map((cm) => (cm.parentId && !commits.some((x) => x.commitId === cm.parentId) ? tx.get(coll.doc(cm.parentId)) : Promise.resolve(null))));
      const sameSeq = await Promise.all(commits.map((cm) => tx.get(coll.where('seq', '==', cm.seq).limit(1))));
      const known = new Map<string, number>();
      let top: Commit | null = null;
      for (const [i, cm] of commits.entries()) {
        const ex = existing[i]!;
        if (ex.exists) {
          if (ex.data()!.hash !== cm.hash) throw E.conflict(`commit ${cm.commitId} is stored with another hash`);
          known.set(cm.commitId, cm.seq);
          continue;
        }
        const other = sameSeq[i]!.docs.find((d) => d.id !== cm.commitId);
        if (other) throw E.conflict(`fork: seq ${cm.seq} is already ${other.id}`, { current: other.id });
        if (cm.parentId) {
          const inBatch = known.get(cm.parentId) ?? commits.find((x) => x.commitId === cm.parentId)?.seq;
          const pseq = inBatch ?? (parents[i]?.exists ? (parents[i]!.data()!.seq as number) : undefined);
          if (pseq === undefined) {
            // the first pushed commit after a migration may name a parent that was never pushed
            if (!cm.data?.migratedFrom) throw E.bad(`commits.${cm.commitId}.parentId`, 'V_REF', `unknown parent ${cm.parentId} (push the parent first)`);
          } else if (cm.seq !== pseq + 1) throw E.bad(`commits.${cm.commitId}.seq`, 'V_RANGE', `seq must be parent.seq + 1 = ${pseq + 1}`);
        } else if (cm.seq !== 1) throw E.bad(`commits.${cm.commitId}.seq`, 'V_RANGE', 'a commit without parent must have seq 1');
        known.set(cm.commitId, cm.seq);
        const clean: Commit = { commitId: cm.commitId, branch: own, seq: cm.seq, parentId: cm.parentId ?? null, kind: cm.kind, message: cm.message.trim(), author: cm.author, createdAt: cm.createdAt, source: cm.source ?? null, revertOf: cm.revertOf ?? null, resetTo: cm.resetTo ?? null, rollbackOf: cm.rollbackOf ?? null, config: cm.config, hash: cm.hash, catalog: cm.catalog ?? null, catalogHash: cm.catalogHash ?? null, stats: cm.stats ?? { added: 0, changed: 0, removed: 0, keys: [], truncated: false }, data: cm.data ?? {} };
        tx.create(coll.doc(cm.commitId), encodeCommit(clean, external.get(cm.commitId)));
        stored.push(cm.commitId);
        if (!top || cm.seq > top.seq) top = clean;
      }
      const cur = bs.exists ? (bs.data() as AgentBranchDoc) : null;
      const newestTop = commits.at(-1)!;
      const headRef = !cur?.head || cur.legacy || newestTop.seq >= cur.head.seq ? commitRef(newestTop) : cur.head;
      const mergedFrom = [...commits].reverse().find((x) => x.source?.branch?.kind === 'deployment' && x.source.commitId && x.source.seq !== null);
      tx.set(brRef, { siteSlug, head: headRef, legacy: false, pushedAt: nowIso(), agentVersion: agent.agentVersion ?? null, ...(mergedFrom ? { mergedFrom: { commitId: mergedFrom.source!.commitId!, seq: mergedFrom.source!.seq! } } : {}) }, { merge: true });
      if (stored.length) {
        const dep = decodeDeployment(ds.data()!);
        const log = new CloudLog(deploymentId, dep);
        log.add(tx, { type: 'agent-branch.pushed', summary: `Site ${siteSlug} pushed ${stored.length} commit(s); head seq ${headRef.seq}: ${commitSummary(headRef.message)}`, data: { siteSlug, commitIds: stored.slice(0, 20), head: headRef.commitId }, actor: null, origin: 'agent' });
        tx.update(depRef, log.depUpdate());
      }
      void top;
      return headRef;
    });
    return { stored, head };
  },
};
