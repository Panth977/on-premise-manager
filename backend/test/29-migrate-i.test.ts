/**
 * §21 #104 migrate-i on pre-0.14 shaped data (like prod: template "Single"; deployment "Test 02" with an applied chain,
 * a draft ≠ head and an open proposal; "Dev Server" with a srcTemplate base + skipped items and an agent-origin
 * rollback revision; one deployment with an update running → skipped). Dry run, apply, rerun = 0 changes, verify-schema.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { applyPlanI, planMigrationI, printPlanI } from '../scripts/migrate-i.ts';
import { verifySchema } from '../scripts/verify-schema.ts';
import { adb, depInput, enrollSite, helloDeployment, HELLO, person, type Person } from './helpers.ts';

type Doc = Record<string, any>;
const clone = <T>(v: T): T => JSON.parse(JSON.stringify(v)) as T;

/** Make a branch-era deployment look pre-0.14: no branch, no commits / mergeBases / agentBranches. */
async function toLegacy(id: string) {
  const ref = adb.collection('deployments').doc(id);
  for (const sub of ['commits', 'mergeBases', 'agentBranches']) for (const d of (await ref.collection(sub).get()).docs) await d.ref.delete();
  const { FieldValue } = await import('../functions/src/ports/index.js');
  await ref.update({ branch: FieldValue.delete() });
}
async function legacyTemplate(templateId: string) {
  const ref = adb.collection('templates').doc(templateId);
  for (const d of (await ref.collection('commits').get()).docs) await d.ref.delete();
  const { FieldValue } = await import('../functions/src/ports/index.js');
  await ref.update({ branch: FieldValue.delete() });
}
async function commitVar(admin: Person, id: string, value: string, message: string) {
  const d = await admin.real.getDeployment({ deploymentId: id });
  const config = await depInput(admin.real, id);
  config.variables = config.variables.map((v) => (v.variableSlug === 'theme' ? { ...v, value } : v));
  return admin.real.commitDeployment({ deploymentId: id, body: { message, baseCommitId: d.branch!.head!.commitId, config } });
}
const markApplied = async (id: string, revId: string, seq: number, hash: string) => adb.collection('deployments').doc(id).update({ head: { revId, seq, hash, reportedAt: new Date().toISOString() } });

test('migrate-i: revisions → commits (same ids), open proposal → head, draft → working copy, srcTemplate → origin + merge base, agent mirrors; skip running; idempotent; verify-schema', async () => {
  const admin = await person('admin');
  // "Test 02": applied chain r1 → r2, an open proposal r3 on top, a draft that differs from it
  const t02 = await helloDeployment(admin, 'Test 02 (migrate-i)');
  const id = t02.dep.deploymentId;
  await enrollSite(admin, id, 'app');
  const props0 = (await admin.api.listProposals({ deploymentId: id })).items;
  const r1 = await admin.api.getRevision({ deploymentId: id, revId: props0[0]!.revId });
  await markApplied(id, r1.revId, 1, r1.hash);
  const c2 = await commitVar(admin, id, 'yellow', 'init fix 7');
  await markApplied(id, c2.data.compat!.revId, c2.data.compat!.seq, c2.hash);
  const c3 = await commitVar(admin, id, 'blue', 'site-01/northwind-frontend/version v1 → v2');
  // a pre-0.14 draft that differs from the head (written as stored data: there is no draft API since §21 #105)
  const dr = adb.collection('deployments').doc(id);
  const draft0 = JSON.parse((await dr.get()).data()!._draft);
  draft0.variables.find((v: Doc) => v.variableSlug === 'greeting').value = 'uncommitted edit';
  await dr.update({ _draft: JSON.stringify(draft0) });
  // the srcTemplate base gets a skipped record (like Dev Server's Template sync state)
  const depRef = adb.collection('deployments').doc(id);
  await depRef.update({ _srcSkipped: JSON.stringify([{ key: 'variables/theme/value', theirsHash: '0123456789abcdef', templateVersion: 2, skippedAt: new Date().toISOString(), skippedBy: admin.uid }]) });
  await adb.collection('deployments').doc(id).collection('agents').doc('app').update({ headRevId: c2.data.compat!.revId, agentVersion: '0.13.0-10090239' });
  await toLegacy(id);
  await legacyTemplate(t02.templateId);
  // a deployment with an update running → skipped
  const run = await helloDeployment(admin, 'running (migrate-i)');
  const pr = (await admin.api.listProposals({ deploymentId: run.dep.deploymentId })).items[0]!;
  await adb.collection('deployments').doc(run.dep.deploymentId).collection('proposals').doc(pr.revId).update({ status: 'applying' });
  await toLegacy(run.dep.deploymentId);

  const plan = await planMigrationI(adb);
  const lines: string[] = [];
  printPlanI(plan, (l) => lines.push(l));
  const e = plan.entities.find((x) => x.path === `deployments/${id}`)!;
  assert.ok(e, lines.join('\n'));
  assert.equal(e.commits, 3, e.lines.join('\n'));
  assert.equal(e.head!.commitId, c3.data.compat!.revId, 'the open proposal is the new head');
  assert.match(e.head!.message, /was proposal seq 3, published/);
  assert.equal(e.uncommitted, 1);
  assert.ok(e.lines.some((l) => /merge base = template v\d+ with 1 skipped/.test(l)), e.lines.join('\n'));
  assert.ok(e.lines.some((l) => /agent branch app: head seq 2/.test(l)), e.lines.join('\n'));
  const tpl = plan.entities.find((x) => x.path === `templates/${t02.templateId}`)!;
  assert.equal(tpl.commits, 1);
  assert.match(tpl.head!.message, /^Migrated: template version \d+/);
  const sk = plan.entities.find((x) => x.path === `deployments/${run.dep.deploymentId}`)!;
  assert.match(sk.skipped!, /update is running/);
  // dry run wrote nothing
  assert.equal((await depRef.get()).data()!.branch, undefined);

  await applyPlanI(adb, plan);
  const dep = await admin.api.getDeployment({ deploymentId: id });
  assert.equal(dep.branch!.head!.seq, 3);
  assert.equal(dep.branch!.commitCount, 3);
  assert.equal('uncommitted' in dep.branch!, false, '§21 #105: migrate-i writes no uncommitted count');
  assert.equal(dep.branch!.origin!.branch.kind, 'template');
  const commits = await admin.api.listDeploymentCommits({ deploymentId: id });
  assert.deepEqual(commits.items.map((x) => x.commitId), [c3.data.compat!.revId, c2.data.compat!.revId, r1.revId], 'commitId = revId');
  assert.ok(commits.items.every((x) => x.kind === 'migrated'));
  assert.equal(commits.items[1]!.message, 'init fix 7');
  const mb = (await depRef.collection('mergeBases').doc(`template:${t02.templateId}`).get()).data()!;
  assert.equal(JSON.parse(mb._skipped)[0].key, 'variables/theme/value');
  const ab = (await depRef.collection('agentBranches').doc('app').get()).data()!;
  assert.equal(ab.legacy, true);
  assert.equal(ab.head.commitId, c2.data.compat!.revId);
  // proposals untouched (old agents can still approve the open one)
  assert.equal((await depRef.collection('proposals').doc(c3.data.compat!.revId).get()).data()!.status, 'published');
  const audit = await adb.collection('auditLog').where('op', '==', 'system:migrate-i').get();
  assert.ok(audit.docs.length >= 1);
  // the migrated branch works: compare with the template sees the srcTemplate base
  const cmp = await admin.api.compareBranches({ body: { source: { branch: { kind: 'template', templateId: t02.templateId }, at: 'head' }, target: { kind: 'deployment', deploymentId: id } } });
  assert.equal(cmp.base.kind, 'record');
  // rerun: nothing for these docs
  const plan2 = await planMigrationI(adb);
  assert.ok(!plan2.ops.some((o) => o.path.includes(id) || o.path.includes(t02.templateId)), JSON.stringify(plan2.ops.map((o) => o.path)));
  const mine = (l: string) => l.includes(id) || l.includes(t02.templateId);
  const v = (await verifySchema(adb, { requireBranches: true })).filter(mine).filter((l) => !l.includes('/revisions/'));
  assert.deepEqual(v, []);
  void clone;
  void HELLO;
  void ({} as Doc);
});
