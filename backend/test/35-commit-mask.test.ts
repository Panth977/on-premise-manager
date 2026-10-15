/**
 * O1: every human response that carries a commit masks the secret values (CommitView) — commit, merge, revert, reset,
 * template commit — also for a caller without deployment.secrets.read. Only the agent fetch (getAgentCommit) is plain.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { MASK, type AccessRule, type ConfigInput } from '@opm/shared';
import { depInput, helloDeployment, person, tplInput } from './helpers.ts';

const theme = (c: ConfigInput, value: string) => (c.variables.find((v) => v.variableSlug === 'theme')!.value = value);
function assertMasked(label: string, res: { config: { secrets: { value: unknown; isSet?: boolean }[] } }, plain: string) {
  assert.ok(!JSON.stringify(res).includes(plain), `${label}: the response carries a plain secret value`);
  assert.ok(res.config.secrets.length > 0, `${label}: secrets listed`);
  for (const s of res.config.secrets) {
    assert.ok(s.value === MASK || s.value === null, `${label}: ${JSON.stringify(s.value)} is not masked`);
    assert.equal(typeof s.isSet, 'boolean', `${label}: isSet`);
  }
}

test('O1: commit / merge / revert / reset / template commit responses mask secret values', async () => {
  const admin = await person('admin');
  const { dep, templateId } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const plain = (await admin.api.revealSecret({ deploymentId: id, secretSlug: 'api-token' })).value;
  assert.ok(plain.length >= 8);
  // a publisher WITHOUT deployment.secrets.read
  const pub = await person([
    { resource: 'deployments', actions: ['read'], scope: { kind: 'all' } },
    { resource: 'deployment.config', actions: ['read', 'write', 'publish'], scope: { kind: 'all' } },
    { resource: 'templates', actions: ['read', 'write'], scope: { kind: 'all' } },
  ] as AccessRule[]);
  const api = pub.real;
  const d0 = await api.getDeployment({ deploymentId: id });
  const head0 = d0.branch!.head!.commitId;
  // commit
  const ci = await depInput(api, id);
  theme(ci, 'yellow');
  const c1 = await api.commitDeployment({ deploymentId: id, body: { message: 'green', baseCommitId: head0, config: ci } });
  assertMasked('commitDeployment', c1, plain);
  // revert + reset
  const rv = await api.revertCommit({ body: { branch: { kind: 'deployment', deploymentId: id }, commitId: c1.commitId, message: 'Revert green', ifHead: c1.commitId } });
  assertMasked('revertCommit', rv, plain);
  const rs = await api.resetToCommit({ body: { branch: { kind: 'deployment', deploymentId: id }, commitId: c1.commitId, message: 'Reset to green', ifHead: rv.commitId } });
  assertMasked('resetToCommit', rs, plain);
  // template commit → merge into the deployment
  const t = await api.getTemplate({ templateId });
  const ti = await tplInput(api, templateId);
  theme(ti, 'red');
  const tc = await api.commitTemplate({ templateId, body: { message: 'purple', baseCommitId: t.branch!.head!.commitId, config: ti } });
  assert.ok(tc.config.secrets.every((s) => s.value === null || s.value === MASK), 'template commit masked');
  const plan = await api.compareBranches({ body: { source: { branch: { kind: 'template', templateId }, at: 'head' }, target: { kind: 'deployment', deploymentId: id } } });
  const it = plan.items.find((i) => i.key === 'variables/theme/value')!;
  const req = { source: { branch: { kind: 'template' as const, templateId }, at: 'head' as const }, target: { kind: 'deployment' as const, deploymentId: id }, targetHead: plan.target.head!.commitId, sourceCommit: plan.source.commit!.commitId, choices: [{ key: it.key, take: 'source' as const }] };
  const pv = await api.previewMerge({ body: req });
  assert.ok(!JSON.stringify(pv).includes(plain), 'previewMerge masked');
  assert.ok(!JSON.stringify(plan).includes(plain), 'compare masked');
  // UX: a preview with nothing picked says so
  const none = await api.previewMerge({ body: { ...req, choices: [] } });
  assert.equal(none.validation.errors.length, 1);
  assert.match(none.validation.errors[0]!.message, /nothing picked/);
  const m = await api.merge({ body: { ...req, message: 'take purple' } });
  assertMasked('merge', m, plain);
  // the reads stay masked too
  const det = await api.getDeploymentCommit({ deploymentId: id, commitId: m.commitId });
  assertMasked('getDeploymentCommit', det.commit, plain);
  // the value itself is untouched
  assert.equal((await admin.api.revealSecret({ deploymentId: id, secretSlug: 'api-token' })).value, plain);
});
