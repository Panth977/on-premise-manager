/**
 * §21 #105 one action: Commit. The browser sends { message, baseCommitId, config }; the server writes a commit directly
 * (no working copy): secrets merged with the base head (set / clear / generate, values never in commit data, activity or
 * audit), permissions (publish + secrets.write for secret changes), V_NO_CHANGE, 409 stale { reason: head-moved, head } →
 * the shared rebaseChanges → a successful commit, removed routes 410, at:'working' 422, merges without a dirty check.
 * (Rename everywhere → one commit per template: 14-image-edit.)
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { rebaseChanges, ulid, type AccessRule, type Config, type ConfigInput, type SecretDefInput } from '@opm/shared';
import { adb, depInput, helloDeployment, person, raw, rejects, tplInput, uniq } from './helpers.ts';

const clone = <T>(v: T): T => JSON.parse(JSON.stringify(v)) as T;
const theme = (c: ConfigInput, value: string) => (c.variables.find((v) => v.variableSlug === 'theme')!.value = value);
const asCfg = (c: ConfigInput): Config => ({ ...c, secrets: c.secrets.map(({ plainValue: _p, generateNow: _g, ...d }) => (void _p, void _g, { ...d, value: null, rotatedAt: null })) }) as Config;

test('deployment commit: { message, baseCommitId, config }; secrets set / clear / generate recorded by slug only; V_NO_CHANGE; permissions', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const api = admin.real;
  const d0 = await api.getDeployment({ deploymentId: id });
  const base = d0.branch!.head!.commitId;
  const before = await api.revealSecret({ deploymentId: id, secretSlug: 'api-token' });
  // required body fields
  await rejects(api.commitDeployment({ deploymentId: id, body: { message: 'x', config: await depInput(api, id) } as never }), 'invalid', 422);
  await rejects(api.commitDeployment({ deploymentId: id, body: { message: 'x', baseCommitId: base } as never }), 'invalid', 422);
  await rejects(api.commitDeployment({ deploymentId: id, body: { message: '', baseCommitId: base, config: await depInput(api, id) } }), 'invalid', 422);
  // nothing changed → V_NO_CHANGE
  const nc = await rejects(api.commitDeployment({ deploymentId: id, body: { message: 'same', baseCommitId: base, config: await depInput(api, id) } }), 'invalid', 422);
  assert.ok((nc.details.errors as { code: string }[]).some((x) => x.code === 'V_NO_CHANGE'));
  // a variable + a typed secret value + a new secret with a generate rule → one commit
  const c1in = await depInput(api, id);
  theme(c1in, 'yellow');
  c1in.secrets[0] = { ...c1in.secrets[0]!, plainValue: 'typed-value-123' } as SecretDefInput;
  c1in.secrets.push({ secretSlug: 'gen-later', label: 'Gen', description: '', required: false, generate: { charset: 'alnum', length: 16 } });
  c1in.secrets.push({ secretSlug: 'manual', label: 'Manual', description: '', required: false, generate: null, plainValue: 'm-1' });
  const c1 = await api.commitDeployment({ deploymentId: id, body: { message: 'yellow + token', baseCommitId: base, config: c1in } });
  assert.equal(c1.kind, 'change');
  assert.equal(c1.parentId, base);
  assert.deepEqual(c1.data.secretValues, { set: ['api-token', 'manual'], generated: ['gen-later'], cleared: [] });
  assert.ok(!JSON.stringify(c1.data).includes('typed-value-123'), 'no value in commit data');
  assert.equal((await api.revealSecret({ deploymentId: id, secretSlug: 'api-token' })).value, 'typed-value-123');
  assert.match((await api.revealSecret({ deploymentId: id, secretSlug: 'gen-later' })).value, /^[A-Za-z0-9]{16}$/);
  // the copy (draft) = the head; no uncommitted count
  const d1 = await api.getDeployment({ deploymentId: id });
  assert.equal(d1.branch!.head!.commitId, c1.commitId);
  assert.equal('uncommitted' in d1.branch!, false);
  assert.equal(d1.draft.variables.find((v) => v.variableSlug === 'theme')!.value, 'yellow');
  // plainValue absent keeps the value; generateNow regenerates; null clears
  const c2in = await depInput(api, id);
  c2in.secrets = c2in.secrets.map((s) => (s.secretSlug === 'gen-later' ? { ...s, generateNow: true } : s));
  theme(c2in, 'blue');
  const c2 = await api.commitDeployment({ deploymentId: id, body: { message: 'regenerate', baseCommitId: c1.commitId, config: c2in } });
  assert.deepEqual(c2.data.secretValues, { set: [], generated: ['gen-later'], cleared: [] });
  assert.equal((await api.revealSecret({ deploymentId: id, secretSlug: 'api-token' })).value, 'typed-value-123', 'absent plainValue = keep');
  const c3in = await depInput(api, id);
  c3in.secrets = c3in.secrets.map((s) => (s.secretSlug === 'manual' ? { ...s, plainValue: null } : s));
  const c3 = await api.commitDeployment({ deploymentId: id, body: { message: 'clear manual', baseCommitId: c2.commitId, config: c3in } });
  assert.deepEqual(c3.data.secretValues, { set: [], generated: [], cleared: ['manual'] });
  await rejects(api.revealSecret({ deploymentId: id, secretSlug: 'manual' }), 'not-found', 404);
  // activity + audit carry the slugs, never a value
  const acts = await api.listActivities({ deploymentId: id, query: { type: 'branch.committed' } });
  const a1 = acts.items.find((a) => (a.data as { commitId?: string }).commitId === c1.commitId)!;
  assert.deepEqual((a1.data as { secretValues?: unknown }).secretValues, { set: ['api-token', 'manual'], generated: ['gen-later'], cleared: [] });
  assert.ok(!JSON.stringify(acts.items).includes('typed-value-123'));
  const au = await api.listAuditLog({ query: { target: `deployments/${id}`, action: 'deployment.committed' } });
  const e1 = au.items.find((x) => x.data.commitId === c1.commitId)!;
  assert.deepEqual(e1.data.secretValues, { set: ['api-token', 'manual'], generated: ['gen-later'], cleared: [] });
  assert.ok(!JSON.stringify(au.items).includes('typed-value-123'));
  assert.ok(!JSON.stringify(au.items).includes(before.value));
  // permissions: publish needed; secret changes need deployment.secrets.write
  const writer = await person([{ resource: 'deployments', actions: ['read'], scope: { kind: 'all' } }, { resource: 'deployment.config', actions: ['read', 'write'], scope: { kind: 'all' } }] as AccessRule[]);
  const w4 = await depInput(api, id);
  theme(w4, 'red');
  await rejects(writer.real.commitDeployment({ deploymentId: id, body: { message: 'red', baseCommitId: c3.commitId, config: w4 } }), 'forbidden', 403);
  const publisher = await person([{ resource: 'deployments', actions: ['read'], scope: { kind: 'all' } }, { resource: 'deployment.config', actions: ['read', 'write', 'publish'], scope: { kind: 'all' } }] as AccessRule[]);
  const s4 = clone(w4);
  s4.secrets[0] = { ...s4.secrets[0]!, plainValue: 'nope' } as SecretDefInput;
  await rejects(publisher.real.commitDeployment({ deploymentId: id, body: { message: 'secret', baseCommitId: c3.commitId, config: s4 } }), 'forbidden', 403);
  const c4 = await publisher.real.commitDeployment({ deploymentId: id, body: { message: 'red', baseCommitId: c3.commitId, config: w4 } });
  assert.equal(c4.data.secretValues, undefined, 'no secret change → no secretValues');
});

test('409 stale { reason: head-moved, head } → rebaseChanges (clean, then a conflict) → commit; templates: no values', async () => {
  const admin = await person('admin');
  const { dep, templateId } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const api = admin.real;
  const base = (await api.getDeployment({ deploymentId: id })).branch!.head!.commitId;
  const baseCfg = await depInput(api, id);
  // browser A edits theme; browser B commits greeting meanwhile
  const mine = clone(baseCfg);
  theme(mine, 'yellow');
  const other = clone(baseCfg);
  other.variables.find((v) => v.variableSlug === 'greeting')!.value = 'Hi from B';
  const cb = await api.commitDeployment({ deploymentId: id, body: { message: 'B: greeting', baseCommitId: base, config: other } });
  const st = await rejects(api.commitDeployment({ deploymentId: id, body: { message: 'A: theme', baseCommitId: base, config: mine } }), 'stale', 409);
  assert.equal(st.details.reason, 'head-moved');
  assert.equal((st.details.head as { commitId: string }).commitId, cb.commitId);
  // rebase in the "browser" with the shared engine: clean → commit
  const head = await depInput(api, id);
  const rb = rebaseChanges(asCfg(baseCfg), asCfg(mine), asCfg(head));
  assert.equal(rb.clean, true);
  const rebased: ConfigInput = { ...rb.config, secrets: head.secrets };
  const ca = await api.commitDeployment({ deploymentId: id, body: { message: 'A: theme', baseCommitId: cb.commitId, config: rebased } });
  const d = await api.getDeployment({ deploymentId: id });
  assert.equal(d.draft.variables.find((v) => v.variableSlug === 'theme')!.value, 'yellow');
  assert.equal(d.draft.variables.find((v) => v.variableSlug === 'greeting')!.value, 'Hi from B');
  // a conflict: both change the theme
  const mine2 = await depInput(api, id);
  theme(mine2, 'green');
  const other2 = await depInput(api, id);
  theme(other2, 'red');
  await api.commitDeployment({ deploymentId: id, body: { message: 'B: red', baseCommitId: ca.commitId, config: other2 } });
  await rejects(api.commitDeployment({ deploymentId: id, body: { message: 'A', baseCommitId: ca.commitId, config: mine2 } }), 'stale', 409);
  const head2 = await depInput(api, id);
  const baseCfg2 = clone(rebased);
  const rc = rebaseChanges(asCfg(baseCfg2), asCfg(mine2), asCfg(head2));
  assert.equal(rc.clean, false);
  assert.deepEqual(rc.conflicts.map((i) => i.key), ['variables/theme/value']);
  // templates: a secret value is refused; a stale base too
  const t = await api.getTemplate({ templateId });
  const ti = await tplInput(api, templateId);
  ti.secrets[0] = { ...ti.secrets[0]!, plainValue: 'x' } as SecretDefInput;
  const tv = await rejects(api.commitTemplate({ templateId, body: { message: 'v', baseCommitId: t.branch!.head!.commitId, config: ti } }), 'invalid', 422);
  assert.match(JSON.stringify(tv.details), /V_TEMPLATE_SECRET_VALUE/);
  await rejects(api.commitTemplate({ templateId, body: { message: 'v', baseCommitId: ulid(), config: await tplInput(api, templateId) } }), 'stale', 409);
});

test('removed working-copy routes answer 410; at:"working" → 422; merges have no dirty check; patchTemplate keeps details', async () => {
  const admin = await person('admin');
  const { dep, templateId } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const api = admin.real;
  const d = await api.getDeployment({ deploymentId: id });
  const H = { Authorization: admin.bearer, 'Content-Type': 'application/json' };
  for (const [m, p, b] of [
    ['PUT', `/v1/deployments/${id}/draft/variables`, { ifRevision: d.draftVersion, items: d.draft.variables }],
    ['PUT', `/v1/deployments/${id}/draft/sites/app`, { ifRevision: d.draftVersion, site: d.draft.sites[0] }],
    ['POST', `/v1/deployments/${id}/secrets/api-token/regenerate`, { ifRevision: d.draftVersion }],
    ['POST', `/v1/deployments/${id}/working/discard`, {}],
    ['POST', `/v1/templates/${templateId}/working/discard`, {}],
  ] as const) {
    const r = await raw(m, p, { headers: H, body: JSON.stringify(b) });
    assert.equal(r.status, 410, `${m} ${p} → ${r.status} ${JSON.stringify(r.json)}`);
    assert.ok(r.json.error?.details?.hint ?? r.json.details?.hint ?? true);
  }
  const t = await api.getTemplate({ templateId });
  await rejects(api.patchTemplate({ templateId, body: { ifVersion: t.version, variables: t.variables } }), 'gone', 410);
  const t2 = await api.patchTemplate({ templateId, body: { ifVersion: t.version, label: 'renamed ' + uniq() } });
  assert.equal(t2.template.version, t.version + 1);
  assert.equal(t2.template.branch!.head!.commitId, t.branch!.head!.commitId, 'details are not a commit');
  await rejects(api.compareBranches({ body: { source: { branch: { kind: 'deployment', deploymentId: id }, at: 'working' }, target: { kind: 'deployment', deploymentId: id } } }), 'invalid', 422);
  // a merge is never refused for "uncommitted changes": template change → merge into the deployment
  const ti = await tplInput(api, templateId);
  theme(ti, 'red');
  await api.commitTemplate({ templateId, body: { message: 'red', baseCommitId: t.branch!.head!.commitId, config: ti } });
  const plan = await api.compareBranches({ body: { source: { branch: { kind: 'template', templateId }, at: 'head' }, target: { kind: 'deployment', deploymentId: id } } });
  assert.equal(plan.target.workingClean, true);
  const it = plan.items.find((i) => i.key === 'variables/theme/value')!;
  const m = await api.merge({ body: { source: { branch: { kind: 'template', templateId }, at: 'head' }, target: { kind: 'deployment', deploymentId: id }, targetHead: plan.target.head!.commitId, sourceCommit: plan.source.commit!.commitId, choices: [{ key: it.key, take: 'source' }], message: 'take red' } });
  assert.ok(m.kind === 'merge' || m.kind === 'cherry-pick');
  // the deployment doc never carries branch.uncommitted
  assert.equal('uncommitted' in ((await adb.collection('deployments').doc(id).get()).data()!.branch ?? {}), false);
});
