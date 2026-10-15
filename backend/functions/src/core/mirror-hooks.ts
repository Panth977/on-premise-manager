/**
 * §21 #106 enqueue side of the mirror, driven by Firestore triggers on the OPM history (every writer — commits,
 * merges, reverts, creates, migrations, agent pushes, persist snapshots — is covered without touching it) and by the
 * agent heartbeat ("agent online": its branch head ≠ the mirrored head → catch-up). Best effort: never throws.
 */
import { mirrorBranchDocId } from '@opm/shared';
import { enqueue, mcol, mirrorHook, refOf, targetOf } from './mirror.js';

export function onTemplateCommit(templateId: string, d: Record<string, unknown>) {
  return mirrorHook('template-commit', async () => {
    const t = await targetOf(refOf('template', { templateId }));
    await enqueue(t, { op: 'commit', opmId: String(d.commitId), seq: Number(d.seq), createdAt: String(d.createdAt), reason: 'commit' });
  });
}
export function onDeploymentCommit(deploymentId: string, d: Record<string, unknown>) {
  return mirrorHook('deployment-commit', async () => {
    const t = await targetOf(refOf('deployment', { deploymentId }));
    await enqueue(t, { op: 'commit', opmId: String(d.commitId), seq: Number(d.seq), createdAt: String(d.createdAt), reason: 'commit' });
  });
}
export function onAgentCommit(deploymentId: string, siteSlug: string, d: Record<string, unknown>) {
  return mirrorHook('agent-commit', async () => {
    const t = await targetOf(refOf('agent', { deploymentId, siteSlug }));
    await enqueue(t, { op: 'commit', opmId: String(d.commitId), seq: Number(d.seq), createdAt: String(d.createdAt), reason: 'agent-push' });
  });
}
/** a persist-to-cloud snapshot (origin snapshot only: what the site really has) → the site's agent branch */
export function onVolumeVersion(deploymentId: string, d: Record<string, unknown>) {
  if (d.origin !== 'snapshot' || typeof d.siteSlug !== 'string') return Promise.resolve();
  return mirrorHook('volume-snapshot', async () => {
    const t = await targetOf(refOf('agent', { deploymentId, siteSlug: d.siteSlug as string }));
    await enqueue(t, { op: 'volume-snapshot', opmId: String(d.versionId), seq: Number(d.seq), createdAt: String(d.createdAt), reason: 'snapshot' });
  });
}
/** heartbeat: the agent branch head is not what GitHub has → catch-up (one item per head) */
export function onAgentHeartbeat(deploymentId: string, siteSlug: string, headCommitId: string | null) {
  if (!headCommitId) return Promise.resolve();
  return mirrorHook('agent-online', async () => {
    const t = await targetOf(refOf('agent', { deploymentId, siteSlug }));
    const st = (await mcol.branches().doc(mirrorBranchDocId(t.gitBranch)).get()).data();
    if (st?.last?.opmId === headCommitId) return;
    await enqueue(t, { op: 'catch-up', opmId: `online-${headCommitId}`, seq: null, createdAt: '9999', reason: 'agent-online' });
  });
}
