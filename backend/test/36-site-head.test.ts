/**
 * O2 + O3: a branch-mode site (agent ≥ 0.14) reports an AGENT-BRANCH commit id as its head (heartbeat headRevId), not a
 * revision. Status resolves it (inHead from the deployment branch head, headSeq = the deployment seq the site merged)
 * and persist-to-cloud agent calls read the persist settings from that commit (no 403).
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { makeCommit, ulid, type BranchRef, type ConfigInput } from '@opm/shared';
import { adb, depInput, enrollSite, helloDeployment, person, sha256Hex } from './helpers.ts';

const MiB = 1024 * 1024;
const enc = (s: string) => new TextEncoder().encode(s);

test('O2 + O3: agent-branch head → status inHead / headSeq, persist-to-cloud blob upload allowed', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const app = await enrollSite(admin, id, 'app');
  await adb.collection('deployments').doc(id).collection('agents').doc('app').update({ agentVersion: '0.16.0-test' });
  // a release with persist-to-cloud on the app volume
  const d0 = await admin.real.getDeployment({ deploymentId: id });
  const ci: ConfigInput = await depInput(admin.real, id);
  const site = ci.sites.find((s) => s.siteSlug === 'app')!;
  site.config.volumes[0]!.persist = { exclude: [], maxBytes: 10 * MiB, auto: true };
  const c1 = await admin.real.commitDeployment({ deploymentId: id, body: { message: 'persist on', baseCommitId: d0.branch!.head!.commitId, config: ci } });
  // the site merges it on its own branch and pushes the mirror
  const got = await app.api.getAgentCommit({ commitId: c1.commitId });
  const own: BranchRef = { kind: 'agent', deploymentId: id, siteSlug: 'app' };
  const author = { kind: 'person' as const, email: 'ops@site.test', uid: null, name: null, siteSlug: 'app' };
  const a1 = makeCommit({ commitId: ulid(), branch: own, parent: null, kind: 'merge', message: 'merge cloud seq ' + c1.seq, author, createdAt: new Date().toISOString(), config: got.commit.config, catalog: got.commit.catalog, source: { branch: { kind: 'deployment', deploymentId: id }, commitId: c1.commitId, seq: c1.seq, label: dep.label, picks: 'all', skipped: 0 }, scope: 'app' });
  await app.api.pushAgentBranch({ body: { commits: [a1] } });
  // before the head is reported: no persist-to-cloud head → 403
  const A = `{"x":"${ulid()}"}`;
  const sha = `sha256:${sha256Hex(A)}` as `sha256:${string}`;
  await assert.rejects(app.api.agentPutBlob({ sha256: sha, body: enc(A) }), (e: { status?: number }) => e.status === 403);
  // heartbeat with the agent-branch commit id as the head
  await app.api.heartbeat({ body: { siteSlug: 'app', agentVersion: '0.16.0-test', headRevId: a1.commitId, staging: null, peersSeen: [], metrics: { cpu: 0, memPct: 0, diskPct: 0 }, edge: null, keyVersion: 1, advertiseHost: '127.0.0.1', agentPort: 7080, ip: '127.0.0.1', hostname: 'h', branch: { head: { commitId: a1.commitId, seq: a1.seq, hash: a1.hash }, mergedFrom: { commitId: c1.commitId, seq: c1.seq }, uncommitted: null } } as never });
  // O2: status
  const st = await admin.real.getStatus({ deploymentId: id });
  const row = st.sites.find((s) => s.siteSlug === 'app')!;
  assert.equal(row.inHead, true, 'inHead from the deployment branch head');
  assert.equal(row.headRevId, a1.commitId);
  assert.equal(row.headSeq, c1.seq, 'headSeq = the deployment seq the site runs');
  // O3: persist-to-cloud upload from the agent-branch head
  const put = await app.api.agentPutBlob({ sha256: sha, body: enc(A) });
  assert.equal(put.sha256, sha);
  // the volume content view sees persist in the head the site runs
  const vc = await admin.real.getVolumeContent({ deploymentId: id, siteSlug: 'app', volumeSlug: site.config.volumes[0]!.volumeSlug });
  assert.equal(vc.persistInHead, true);
});
