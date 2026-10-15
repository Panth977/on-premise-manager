/**
 * §21 #104 the agent GUI's BranchHost (ui/src/branch/host.ts): the shared CommitDialog / MergeView / HistoryView talk
 * to this site's branch through BRANCH_AGENT_API. Merges, commits with edits, reverts and resets return the UpdateRun
 * (the commit is written when the run reaches COMMIT); the page shows the existing UpdateProgress panel.
 * Compares of two local commits (History) are computed here from the two masked commits (the shared engine).
 */
import { setBranchHost, type BranchHost, type MergeResult } from '$opm/branch/host';
import { planMerge, countMerge, revertPlan as revertItems, resetPlan as resetItems, commitRef, type BranchRef, type CommitDetail, type MergePlan } from '@opm/shared/branch';
import type { Config } from '@opm/shared/types';
import { errMessage, isApiError } from '../api';
import { session } from '../state/session.svelte';
import { branch, refreshBranch } from '../state/branch.svelte';
import { headConfig, overview } from '../state/app.svelte';
import { siteBranchApi } from './siteBranch';

/**
 * Go answers a merge / revert / reset while changes made on the server are not committed with 409 conflict +
 * details.reason "uncommitted-drift" (§21 #105; 0.14.0 agents: "dirty-working-copy").
 */
function friendly<T>(p: Promise<T>): Promise<T> {
  return p.catch((e: unknown) => {
    if (isApiError(e) && e.code === 'conflict' && (e.details?.reason === 'uncommitted-drift' || e.details?.reason === 'dirty-working-copy')) {
      const n = Number(e.details.drift ?? 0) + Number(e.details.notes ?? 0) || Number(e.details.uncommitted ?? 0);
      throw new Error(`${n ? `${n} ` : ''}${n === 1 ? 'change' : 'changes'} made on this server outside the agent ${n === 1 ? 'is' : 'are'} not committed — commit or discard them on the Branch page (Uncommitted) first.`);
    }
    if (isApiError(e)) throw new Error(errMessage(e));
    throw e;
  });
}
const asCfg = (d: CommitDetail) => d.commit.config as unknown as Config;
const commitIdOf = (at: unknown): string | null => (at && typeof at === 'object' && 'commitId' in (at as object) ? (at as { commitId: string }).commitId : null);

/** §21 #111 `site`: the branch of that site (another site → relayed over the mesh); omitted / this agent → this site */
export function agentBranchHost(onRun?: (r: MergeResult | null) => void, site?: string | null | (() => string | null | undefined)): BranchHost {
  // read per call: the page component stays mounted when the site of the URL changes
  const sb = () => siteBranchApi(typeof site === 'function' ? site() : site);
  const getLocal = (id: string) => sb().commit(id);
  async function headDetail(): Promise<CommitDetail | null> {
    let st = sb().remote ? await sb().state() : branch.data;
    if (!st) st = await branch.load();
    const h = st?.local.head;
    return h ? getLocal(h.commitId) : null;
  }
  const host: BranchHost = {
    scope: 'agent',
    canWrite: true,
    async compare(req) {
      const src = commitIdOf(req.source.at);
      if (req.diffOnly && req.source.branch.kind === 'agent') {
        // History: two local commits (or one vs the head) → computed here
        const tgtPoint = 'branch' in req.target && 'at' in req.target ? req.target : null;
        const [a, b] = await Promise.all([getLocal(src!), tgtPoint && commitIdOf(tgtPoint.at) ? getLocal(commitIdOf(tgtPoint.at)!) : headDetail()]);
        const items = planMerge(null, asCfg(a), b ? asCfg(b) : { variables: [], secrets: [], files: [], sites: [] }, { secretValues: false });
        const plan: MergePlan = {
          source: { point: req.source, label: `this site@${a.commit.seq}`, commit: commitRef(a.commit) },
          target: { branch: req.source.branch, label: b ? `this site@${b.commit.seq}` : 'empty', head: b ? commitRef(b.commit) : null, workingClean: true, uncommitted: 0 },
          base: { kind: 'none', sourceCommitId: null, label: 'two commits of this site' },
          items,
          counts: countMerge(items),
          fastForward: false,
          diffOnly: true,
        };
        return plan;
      }
      return sb().compare({ source: src ? { cloudCommitId: src } : 'cloud', diffOnly: req.diffOnly });
    },
    preview: (req) => sb().preview({ sourceCommitId: req.sourceCommit ?? commitIdOf(req.source.at) ?? '', targetHead: req.targetHead, choices: req.choices, values: req.values }),
    merge: (req) => friendly(sb().merge({ sourceCommitId: req.sourceCommit ?? commitIdOf(req.source.at) ?? '', targetHead: req.targetHead, choices: req.choices, values: req.values, message: req.message })),
    commits: (_b, cursor) => sb().commits(cursor ? { cursor } : {}),
    commit: (_b, id) => getLocal(id),
    /** §21 #105: records the changes made on the server (drift); GUI edits are committed by the change bar */
    async commitWorking(message) {
      const r = await friendly(sb().commitDrift({ message }));
      return r.run ?? r.commit;
    },
    async revertPlan(_b, id) {
      const c = await getLocal(id);
      const [p, h] = await Promise.all([c.commit.parentId ? getLocal(c.commit.parentId) : null, headDetail()]);
      return revertItems(asCfg(c), p ? asCfg(p) : null, h ? asCfg(h) : asCfg(c), { secretValues: false });
    },
    revert: (_b, id, message, ifHead, choices) => friendly(sb().revert({ commitId: id, message, targetHead: ifHead, choices })),
    async resetPlan(_b, id) {
      const [c, h] = await Promise.all([getLocal(id), headDetail()]);
      return resetItems(asCfg(c), h ? asCfg(h) : asCfg(c), { secretValues: false });
    },
    reset: (_b, id, message, ifHead) => friendly(sb().reset({ commitId: id, message, targetHead: ifHead })),
    /** §21 #104 GET branch/blobs/{sha256}: text for the merge view diffs (null = binary, missing or > 8 MiB) */
    async blobText(sha256) {
      try {
        const r = await sb().blob(sha256);
        if (!r.ok) return null;
        const buf = new Uint8Array(await r.arrayBuffer());
        if (buf.length > 1024 * 1024 || buf.subarray(0, 8000).includes(0)) return null;
        return new TextDecoder('utf-8', { fatal: true }).decode(buf);
      } catch {
        return null;
      }
    },
    labelOf: (b: BranchRef) => (b.kind === 'deployment' ? 'cloud' : b.kind === 'agent' ? (sb().remote ? `site ${b.siteSlug}` : `this site (${b.siteSlug})`) : `template ${b.templateId}`),
    actor: () => session.actor?.email ?? 'unknown',
    changed(r) {
      refreshBranch();
      headConfig.load();
      overview.load();
      onRun?.(r);
    },
  };
  return host;
}
/** set the agent BranchHost in the current component context */
export const provideAgentBranchHost = (onRun?: (r: MergeResult | null) => void, site?: string | null | (() => string | null | undefined)) => setBranchHost(agentBranchHost(onRun, site));
