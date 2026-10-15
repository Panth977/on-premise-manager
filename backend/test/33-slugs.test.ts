/**
 * §21 #106 entity slugs: Template.slug / Deployment.slug at create (from the label, unique, or requested; 409
 * slug-taken), immutable on PATCH, in views + summaries; migrate-k (dry run / apply / rerun 0, oldest keeps the plain
 * slug, audit) + verify-schema.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { slugifyLabel } from '@opm/shared';
import { adb, person, rejects, uniq } from './helpers.ts';
import { applyMigrationK, planMigrationK } from '../scripts/migrate-k.ts';
import { verifySchema } from '../scripts/verify-schema.ts';

test('template + deployment slugs: derived, unique, requested, immutable, listed', async () => {
  const admin = await person('admin');
  const label = `Slug Plant ${uniq('')}`;
  const t1 = await admin.real.createTemplate({ body: { label } });
  assert.equal(t1.slug, slugifyLabel(label));
  const t2 = await admin.real.createTemplate({ body: { label } });
  assert.equal(t2.slug, `${t1.slug}-2`, 'same label → -2');
  const want = uniq('custom-');
  const t3 = await admin.real.createTemplate({ body: { label: 'Anything', slug: want } });
  assert.equal(t3.slug, want);
  const e = await rejects(admin.real.createTemplate({ body: { label: 'Other', slug: want } }), 'conflict', 409);
  assert.equal(e.details.reason, 'slug-taken');
  await rejects(admin.real.createTemplate({ body: { label: 'Bad', slug: 'Bad Slug' } }), 'invalid', 422);
  const p = await rejects(admin.real.patchTemplate({ templateId: t1.templateId, body: { ifVersion: 1, slug: 'x-new' } as never }), 'invalid', 422);
  assert.equal((p.details.errors as { code: string }[])[0]!.code, 'V_IMMUTABLE');
  const list = await admin.real.listTemplates({});
  assert.equal(list.items.find((x) => x.templateId === t1.templateId)!.slug, t1.slug);
  // a label starting with a digit
  const t4 = await admin.real.createTemplate({ body: { label: `2026 ${uniq('')}` } });
  assert.match(t4.slug, /^t-2026-/);
  // deployments
  const dl = `Dep ${uniq('')}`;
  const d1 = await admin.real.createDeployment({ body: { label: dl, templateId: null, tagSlugs: [], managerAccountUids: [] } });
  assert.equal(d1.deployment.slug, slugifyLabel(dl));
  const d2 = await admin.real.createDeployment({ body: { label: dl, templateId: null, tagSlugs: [], managerAccountUids: [] } });
  assert.equal(d2.deployment.slug, `${d1.deployment.slug}-2`);
  await rejects(admin.real.createDeployment({ body: { label: 'x', slug: d1.deployment.slug, templateId: null, tagSlugs: [], managerAccountUids: [] } }), 'conflict', 409);
  await rejects(admin.real.patchDeployment({ deploymentId: d1.deployment.deploymentId, body: { slug: 'zz' } as never }), 'invalid', 422);
  const dlist = await admin.real.listDeployments({});
  assert.equal(dlist.items.find((x) => x.deploymentId === d1.deployment.deploymentId)?.slug, d1.deployment.slug);
  // a deleted deployment keeps its slug reserved
  await admin.real.deleteDeployment({ deploymentId: d2.deployment.deploymentId, body: { confirm: dl } as never }).catch(() => undefined);
  const d3 = await admin.real.createDeployment({ body: { label: dl, templateId: null, tagSlugs: [], managerAccountUids: [] } });
  assert.equal(d3.deployment.slug, `${d1.deployment.slug}-3`);
});

test('migrate-k: legacy documents get a slug (oldest keeps the plain one), idempotent, audited; verify-schema clean', async () => {
  const lab = `Legacy ${uniq('')}`;
  const now = Date.now();
  const mk = async (coll: 'templates' | 'deployments', i: number) => {
    const ref = adb.collection(coll).doc();
    await ref.set({ label: lab, createdAt: new Date(now - (10 - i) * 1000).toISOString(), [coll === 'templates' ? 'templateId' : 'deploymentId']: ref.id, ...(coll === 'deployments' ? { deleted: true } : {}) });
    return ref;
  };
  const tOld = await mk('templates', 1);
  const tNew = await mk('templates', 2);
  const dOld = await mk('deployments', 1);
  const plan = await planMigrationK(adb);
  const mine = plan.entities.filter((e) => [tOld.path, tNew.path, dOld.path].includes(e.path));
  assert.equal(mine.length, 3);
  assert.ok(mine.every((e) => e.action === 'assign'));
  assert.equal(mine.find((e) => e.path === tOld.path)!.newSlug, slugifyLabel(lab));
  assert.equal(mine.find((e) => e.path === tNew.path)!.newSlug, `${slugifyLabel(lab)}-2`);
  const r = await applyMigrationK(adb, plan);
  assert.ok(r.applied >= 3);
  assert.equal((await tOld.get()).data()!.slug, slugifyLabel(lab));
  assert.equal((await tNew.get()).data()!.slug, `${slugifyLabel(lab)}-2`);
  assert.equal((await dOld.get()).data()!.slug, slugifyLabel(lab));
  const again = await planMigrationK(adb);
  assert.equal(again.counts.assign, 0, 'rerun: nothing to do');
  const au = await adb.collection('auditLog').where('op', '==', 'system:migrate-k').get();
  assert.ok(au.size >= 1);
  const v = (await verifySchema(adb)).filter((l) => /slug/.test(l));
  assert.deepEqual(v, []);
  for (const ref of [tOld, tNew, dOld]) await ref.delete();
});
