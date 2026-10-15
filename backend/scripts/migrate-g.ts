/**
 * migrate-g — nothing is "shown once" (SPEC §21 #84): adds the new stored-credential fields as null where they are missing.
 *   deployments/{id} (incl. deleted)     accessKey: null        (pre-#84 key not stored → "not stored — regenerate to view")
 *   deployments/{id}/installTokens/{h}  token: null            (pre-#84 tokens: not stored)
 *   accounts/{uid}                      lastSetPassword: null, lastResetLink: null
 * Never rotates, regenerates or sets anything else (no updatedAt change). One `system.migration` audit entry per --apply
 * run that changes something (op system:migrate-g, data.migration = 'g'). Idempotent: a second run plans 0 changes.
 *
 * Usage (from backend/):
 *   emulator: FIRESTORE_EMULATOR_HOST=127.0.0.1:8085 npx tsx scripts/migrate-g.ts --project demo-opm [--dry-run]
 *   prod:     OPM_GCLOUD_TOKEN=1 OPM_GCLOUD_ACCOUNT=<owner> npx tsx scripts/migrate-g.ts --project your-firebase-project [--dry-run | --apply]
 * Default is the dry run; only --apply writes.
 */
import { pathToFileURL } from 'node:url';
import type { DocStore as Firestore } from '../functions/src/ports/index.js';
import { auditActionKeys } from '@opm/shared';

export type Op = { op: 'update'; path: string; data: Record<string, null>; why: string };
export interface PlanG {
  ops: Op[];
  counts: Record<string, number>;
}

export async function planMigrationG(db: Firestore): Promise<PlanG> {
  const plan: PlanG = { ops: [], counts: { deployments: 0, installTokens: 0, accounts: 0 } };
  for (const d of (await db.collection('deployments').get()).docs) {
    if (!('accessKey' in d.data())) {
      plan.ops.push({ op: 'update', path: d.ref.path, data: { accessKey: null }, why: `deployment "${String(d.data().label)}": accessKey not stored (pre-#84)` });
      plan.counts.deployments!++;
    }
    for (const t of (await d.ref.collection('installTokens').get()).docs) {
      if ('token' in t.data()) continue;
      plan.ops.push({ op: 'update', path: t.ref.path, data: { token: null }, why: `install token of site ${String(t.data().siteSlug)}: not stored (pre-#84)` });
      plan.counts.installTokens!++;
    }
  }
  for (const a of (await db.collection('accounts').get()).docs) {
    const x = a.data();
    const data: Record<string, null> = {};
    if (!('lastSetPassword' in x)) data.lastSetPassword = null;
    if (!('lastResetLink' in x)) data.lastResetLink = null;
    if (!Object.keys(data).length) continue;
    plan.ops.push({ op: 'update', path: a.ref.path, data, why: `account ${String(x.email)}: ${Object.keys(data).join(' + ')} = null` });
    plan.counts.accounts!++;
  }
  return plan;
}

/** Apply a plan (batches of 400) + one system.migration audit entry. */
export async function applyPlanG(db: Firestore, plan: PlanG): Promise<void> {
  if (!plan.ops.length) return;
  for (let i = 0; i < plan.ops.length; i += 400) {
    const b = db.batch();
    for (const o of plan.ops.slice(i, i + 400)) b.update(db.doc(o.path), o.data);
    await b.commit();
  }
  const ref = db.collection('auditLog').doc();
  await ref.create({
    auditId: ref.id,
    ts: new Date().toISOString(),
    op: 'system:migrate-g',
    actor: 'system',
    actorEmail: null,
    action: 'system.migration',
    outcome: 'ok',
    status: 200,
    errorCode: null,
    errorMessage: null,
    targetType: 'system',
    target: 'system/migration-g',
    requestId: null,
    ip: null,
    userAgent: null,
    changesTotal: 0,
    _changes: '[]',
    _data: JSON.stringify({ migration: 'g', dryRun: false, changes: plan.ops.length, counts: plan.counts, paths: plan.ops.slice(0, 500).map((o) => o.path) }),
    _actionKeys: auditActionKeys('system.migration'),
  });
}

export function printPlan(plan: PlanG, log: (s: string) => void = console.log) {
  for (const o of plan.ops) log(`● ${o.path}: ${JSON.stringify(o.data)} — ${o.why}`);
  log(`\ncounts: ${JSON.stringify(plan.counts)}\n${plan.ops.length} change(s)`);
}

async function main() {
  const { arg, flag, init } = await import('./_admin.ts');
  const project = arg('project');
  const apply = flag('apply');
  if (!project) {
    console.error('usage: tsx scripts/migrate-g.ts --project <id> [--dry-run (default) | --apply]');
    process.exit(2);
  }
  const { db, emulator } = init(project);
  console.log(`migrate-g on ${project}${emulator ? ' (emulator)' : ''} — ${apply ? 'APPLY' : 'dry run (nothing is written)'}`);
  const plan = await planMigrationG(db);
  printPlan(plan);
  if (apply) {
    await applyPlanG(db, plan);
    console.log(plan.ops.length ? 'applied (+ one system.migration audit entry)' : 'nothing to do');
  }
}
if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href)
  main().then(
    () => process.exit(0),
    (e) => {
      console.error(e);
      process.exit(1);
    },
  );
