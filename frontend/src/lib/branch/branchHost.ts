/**
 * §21 #104 the cloud console's BranchHost (ui/src/branch/host.ts) over CLOUD_API: compare / preview / merge (POST
 * /v1/branches/*), commit lists + details per branch kind, commit, revert / reset (plans computed with
 * @opm/shared/branch from the commit details), blob texts, the source picker options (templates, deployments, agent
 * branches) and locks for secret value items from can().
 */
import { branchKey, pointParam, revertPlan, resetPlan, type BranchRef, type CommitDetail, type MergeItem, type MergePlan } from '@opm/shared';
import type { BranchHost, SourceOption } from '$opm/branch/host';
import { api } from '../api';
import { fetchContent } from '../config-editor/files';
import { session } from '../session.svelte';

export interface CloudHostOptions {
  /** the branch "Commit…" commits (null on read-only pages) */
  target: BranchRef | null;
  label: (b: BranchRef) => string;
  canWrite: boolean;
  /** commit (unused: the scope PendingBar commits the browser change set, §21 #105) */
  commitWorking?: (message: string) => Promise<import('@opm/shared').Commit>;
  changed?: () => void;
  /** deployment-scoped permission checks (secret value locks) */
  canSecrets?: { read: boolean; write: boolean };
  /** the deployment whose agent branches are offered as sources */
  deploymentId?: string | null;
}

const labels = new Map<string, string>();
export function rememberLabel(b: BranchRef, label: string) {
  labels.set(branchKey(b), label);
}

export async function commitsOf(b: BranchRef, cursor: string | null, limit = 50) {
  const query = { cursor: cursor ?? undefined, limit };
  if (b.kind === 'template') return api.listTemplateCommits({ templateId: b.templateId, query });
  if (b.kind === 'deployment') return api.listDeploymentCommits({ deploymentId: b.deploymentId, query });
  return api.listAgentBranchCommits({ deploymentId: b.deploymentId, siteSlug: b.siteSlug, query });
}
export async function commitOf(b: BranchRef, commitId: string): Promise<CommitDetail> {
  if (b.kind === 'template') return api.getTemplateCommit({ templateId: b.templateId, commitId });
  if (b.kind === 'deployment') return api.getDeploymentCommit({ deploymentId: b.deploymentId, commitId });
  return api.getAgentBranchCommit({ deploymentId: b.deploymentId, siteSlug: b.siteSlug, commitId });
}

/** Source picker: every template, every readable deployment, the agent branches of the current deployment. */
export async function sourceOptions(exclude: BranchRef | null, deploymentId: string | null): Promise<SourceOption[]> {
  const out: SourceOption[] = [];
  const ex = exclude ? branchKey(exclude) : '';
  const [tpls, deps, ab] = await Promise.all([
    session.can('templates', 'read') ? api.listTemplates({}).then((r) => r.items).catch(() => []) : Promise.resolve([]),
    api.listDeployments({ query: { limit: 200 } }).then((r) => r.items).catch(() => []),
    deploymentId ? api.listAgentBranches({ deploymentId }).then((r) => r.items).catch(() => []) : Promise.resolve([]),
  ]);
  for (const t of tpls) {
    const b: BranchRef = { kind: 'template', templateId: t.templateId };
    rememberLabel(b, t.label);
    if (branchKey(b) !== ex) out.push({ value: branchKey(b), label: t.label, group: 'Templates', hint: t.branch?.head ? `commit ${t.branch.head.seq}` : `v${t.version}` });
  }
  for (const d of deps) {
    const b: BranchRef = { kind: 'deployment', deploymentId: d.deploymentId };
    rememberLabel(b, d.label);
    if (branchKey(b) !== ex) out.push({ value: branchKey(b), label: d.label, group: 'Deployments', hint: d.branch?.head ? `commit ${d.branch.head.seq}` : 'no commit yet' });
  }
  for (const a of ab) {
    if (!deploymentId) break;
    const b: BranchRef = { kind: 'agent', deploymentId, siteSlug: a.siteSlug };
    if (!a.head) continue;
    out.push({ value: branchKey(b), label: `site ${a.siteSlug}`, group: 'Agent branches (this deployment)', hint: `commit ${a.head.seq}${a.needsAgent014 ? ' · agent < 0.14' : ''}` });
  }
  return out;
}

export function lockSecretValues(plan: MergePlan, can: { read: boolean; write: boolean } | undefined): MergePlan {
  if (!can || (can.read && can.write)) return plan;
  for (const i of plan.items) if (i.section === 'secrets' && i.key.endsWith('/value') && !i.locked) i.locked = 'you need deployment.secrets.read on the source and deployment.secrets.write on the target';
  return plan;
}

export function createCloudBranchHost(o: CloudHostOptions): BranchHost {
  const parentConfig = async (b: BranchRef, d: CommitDetail) => (d.commit.parentId ? (await commitOf(b, d.commit.parentId)).commit.config : null);
  const head = async (b: BranchRef) => {
    const r = await commitsOf(b, null, 1);
    return r.items[0] ?? null;
  };
  const host: BranchHost = {
    scope: o.target?.kind === 'template' ? 'template' : 'deployment',
    async compare(req) {
      return lockSecretValues(await api.compareBranches({ body: req }), o.canSecrets);
    },
    preview: (req) => api.previewMerge({ body: req }),
    async merge(req) {
      const c = await api.merge({ body: req });
      o.changed?.();
      return c;
    },
    commits: (b, cursor) => commitsOf(b, cursor),
    commit: (b, id) => commitOf(b, id),
    async commitWorking(message) {
      if (!o.commitWorking) return null;
      const c = await o.commitWorking(message);
      o.changed?.();
      return c;
    },
    async revertPlan(b, id): Promise<MergeItem[]> {
      const d = await commitOf(b, id);
      const h = await head(b);
      const hd = h ? await commitOf(b, h.commitId) : d;
      return revertPlan(d.commit.config, await parentConfig(b, d), hd.commit.config, { secretValues: b.kind !== 'template' });
    },
    async revert(b, id, message, ifHead, choices) {
      const c = await api.revertCommit({ body: { branch: b, commitId: id, message, ifHead, choices } });
      o.changed?.();
      return c;
    },
    async resetPlan(b, id) {
      const d = await commitOf(b, id);
      const h = await head(b);
      const hd = h ? await commitOf(b, h.commitId) : d;
      return resetPlan(d.commit.config, hd.commit.config, { secretValues: b.kind !== 'template' });
    },
    async reset(b, id, message, ifHead) {
      const c = await api.resetToCommit({ body: { branch: b, commitId: id, message, ifHead } });
      o.changed?.();
      return c;
    },
    async blobText(sha) {
      try {
        const b = await fetchContent(sha);
        if (b.size > 2 << 20) return null;
        const t = await b.text();
        return /\u0000/.test(t.slice(0, 4096)) ? null : t;
      } catch {
        return null;
      }
    },
    canWrite: o.canWrite,
    labelOf: (b) => labels.get(branchKey(b)) ?? o.label(b),
    actor: () => session.account?.email ?? '',
    sourceOptions: () => sourceOptions(o.target, o.deploymentId ?? null),
    changed: () => o.changed?.(),
  };
  return host;
}
export { pointParam };
