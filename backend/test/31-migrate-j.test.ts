/**
 * §21 #105 migrate-j on 0.14.0-shaped data: a template + a deployment whose stored working copy differs from the head
 * (branch.uncommitted > 0), a never-committed deployment with a config, a clean one that still carries
 * branch.uncommitted: 0. Dry run (nothing written), apply (commits "Uncommitted changes from before 0.14.1
 * (auto-committed)", field removed), a concurrent change between plan and apply is refused, rerun = 0, verify-schema 0.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { applyMigrationJ, MIGRATE_J_MESSAGE, planMigrationJ } from '../scripts/migrate-j.ts';
import { verifySchema } from '../scripts/verify-schema.ts';
import { adb, helloDeployment, person } from './helpers.ts';

type Doc = Record<string, any>;

test('migrate-j: working copies → auto-commits, branch.uncommitted removed, concurrent change refused, idempotent, verify-schema', async () => {
  const admin = await person('admin');
  const a = await helloDeployment(admin, 'dirty dep (migrate-j)');
  const b = await helloDeployment(admin, 'clean dep (migrate-j)');
  const c = await helloDeployment(admin, 'never committed (migrate-j)');
  const tRef = adb.collection('templates').doc(a.templateId);
  const aRef = adb.collection('deployments').doc(a.dep.deploymentId);
  const bRef = adb.collection('deployments').doc(b.dep.deploymentId);
  const cRef = adb.collection('deployments').doc(c.dep.deploymentId);
  // 0.14.0 state: template working copy theme red (uncommitted 1)
  const t0 = (await tRef.get()).data()!;
  const body = JSON.parse(t0._body);
  body.variables.find((v: Doc) => v.variableSlug === 'theme').value = 'red';
  await tRef.update({ _body: JSON.stringify(body), 'branch.uncommitted': 1 });
  // deployment working copy: theme purple + a new secret value (uncommitted 2)
  const a0 = (await aRef.get()).data()!;
  const draft = JSON.parse(a0._draft);
  draft.variables.find((v: Doc) => v.variableSlug === 'theme').value = 'blue';
  draft.secrets[0].value = 'migrated-secret-value';
  await aRef.update({ _draft: JSON.stringify(draft), 'branch.uncommitted': 2 });
  // clean, but the field is there
  await bRef.update({ 'branch.uncommitted': 0 });
  // never committed: no head, no commits, a config
  for (const d of (await cRef.collection('commits').get()).docs) await d.ref.delete();
  const c0 = (await cRef.get()).data()!;
  await cRef.update({ branch: { head: null, origin: c0.branch.origin ?? null, commitCount: 0, uncommitted: 5 } });

  const mine = (p: string) => [a.templateId, a.dep.deploymentId, b.dep.deploymentId, c.dep.deploymentId].some((x) => p.includes(x));
  // dry run: nothing written
  const { plan, work } = await planMigrationJ(adb);
  const e = (p: string) => plan.entities.find((x) => x.path === p)!;
  assert.equal(e(`templates/${a.templateId}`).action, 'commit');
  assert.deepEqual(e(`templates/${a.templateId}`).keys, ['variables/theme/value']);
  assert.equal(e(`deployments/${a.dep.deploymentId}`).action, 'commit');
  assert.deepEqual([...e(`deployments/${a.dep.deploymentId}`).keys!].sort(), ['secrets/api-token/value', 'variables/theme/value']);
  assert.equal(e(`deployments/${b.dep.deploymentId}`).action, 'strip');
  assert.equal(e(`deployments/${c.dep.deploymentId}`).action, 'commit');
  assert.equal(e(`deployments/${c.dep.deploymentId}`).newSeq, 1);
  assert.equal((await aRef.get()).data()!.branch.uncommitted, 2, 'dry run wrote nothing');
  const v0 = (await verifySchema(adb, { requireBranches: true, requireNoWorkingCopy: true })).filter(mine);
  assert.ok(v0.some((l) => l.includes('branch.uncommitted is still stored')), v0.join('\n'));
  assert.ok(v0.some((l) => l.includes('config copy differs')), v0.join('\n'));

  // a concurrent change between plan and apply wins: the clean deployment b gets an API commit
  const bi = await (await import('./helpers.ts')).depInput(admin.real, b.dep.deploymentId);
  bi.variables.find((v) => v.variableSlug === 'theme')!.value = 'yellow';
  const bh = (await admin.real.getDeployment({ deploymentId: b.dep.deploymentId })).branch!.head!.commitId;
  await admin.real.commitDeployment({ deploymentId: b.dep.deploymentId, body: { message: 'concurrent', baseCommitId: bh, config: bi } });
  const agentEpoch0 = (await aRef.get()).data()!.agentEpoch;
  const r = await applyMigrationJ(adb, plan, work.filter((w) => mine(w.ref)));
  assert.ok(r.refused.some((x) => x.includes(b.dep.deploymentId)), JSON.stringify(r));
  assert.equal(r.refused.filter(mine).length, 1);

  // results
  const tAfter = (await tRef.get()).data()!;
  assert.equal('uncommitted' in tAfter.branch, false);
  const tc = (await tRef.collection('commits').doc(tAfter.branch.head.commitId).get()).data()!;
  assert.equal(tc.message, MIGRATE_J_MESSAGE);
  assert.equal(tc.author.kind, 'system');
  assert.deepEqual(JSON.parse(tc._data).autoCommitted, { from: 'cloud-working-copy', items: 1 });
  const aAfter = (await aRef.get()).data()!;
  assert.equal('uncommitted' in aAfter.branch, false);
  assert.equal(aAfter.agentEpoch, agentEpoch0 + 1);
  const ac = (await aRef.collection('commits').doc(aAfter.branch.head.commitId).get()).data()!;
  const adata = JSON.parse(ac._data);
  assert.deepEqual(adata.secretValues, { set: ['api-token'], generated: [], cleared: [] });
  assert.ok(!ac._data.includes('migrated-secret-value'));
  assert.ok(JSON.parse(ac._catalog).images, 'deployment commits carry a catalog');
  const view = await admin.real.getDeployment({ deploymentId: a.dep.deploymentId });
  assert.equal(view.draft.variables.find((v) => v.variableSlug === 'theme')!.value, 'blue');
  assert.equal((await admin.real.revealSecret({ deploymentId: a.dep.deploymentId, secretSlug: 'api-token' })).value, 'migrated-secret-value');
  const cAfter = (await cRef.get()).data()!;
  assert.equal(cAfter.branch.head.seq, 1);
  assert.equal(cAfter.branch.commitCount, 1);
  // the API commit on b already rewrote its branch without the field (#105 writer)
  assert.equal('uncommitted' in (await bRef.get()).data()!.branch, false);
  const audit = await adb.collection('auditLog').where('op', '==', 'system:migrate-j').get();
  assert.ok(audit.docs.length >= 1);

  // rerun: nothing left for these docs; verify-schema clean
  const again = await planMigrationJ(adb);
  assert.deepEqual(again.plan.entities.filter((x) => mine(x.path) && x.action !== 'none').map((x) => x.path), []);
  const v1 = (await verifySchema(adb, { requireBranches: true, requireNoWorkingCopy: true })).filter(mine).filter((l) => !l.includes('/revisions/'));
  assert.deepEqual(v1, []);
});
