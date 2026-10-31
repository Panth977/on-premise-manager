/**
 * migrate-k — §21 #106 entity slugs. Whole database, idempotent.
 *
 * Every template and every deployment (deleted ones too) without a valid `slug` gets one: slugifyLabel(label) made
 * unique in its collection (templates prefix "t-", deployments "d-" when the label does not start with a letter),
 * oldest createdAt first so the older entity keeps the plain slug. Existing slugs are never changed (they name GitHub
 * mirror branches). A slug used twice (should never happen) is REPORTED, not changed.
 * One `system.migration` audit entry per --apply run that changed something (op system:migrate-k, data.migration 'k').
 *
 * Usage (from backend/):
 *   npx tsx scripts/migrate-k.ts --data-dir <dir> [--dry-run | --apply] [--report <file.json>]
 * Default is the dry run; only --apply writes.
 */
import { writeFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
import type { DocStore as Firestore } from '../src/driver/index.js';
import { auditActionKeys, RE, slugifyLabel, uniqueSlug } from '@opm/shared';

export interface EntityK {
  path: string;
  kind: 'template' | 'deployment';
  label: string;
  slug: string | null;
  action: 'assign' | 'none' | 'duplicate';
  newSlug?: string;
}
export interface PlanK {
  entities: EntityK[];
  counts: { assign: number; none: number; duplicate: number };
}

export async function planMigrationK(db: Firestore): Promise<PlanK> {
  const entities: EntityK[] = [];
  for (const kind of ['template', 'deployment'] as const) {
    const s = await db.collection(kind === 'template' ? 'templates' : 'deployments').get();
    const docs = s.docs.map((d) => ({ id: d.id, label: String(d.data().label ?? ''), slug: d.data().slug as unknown, createdAt: String(d.data().createdAt ?? '') })).sort((a, b) => a.createdAt.localeCompare(b.createdAt) || a.id.localeCompare(b.id));
    const taken = new Set<string>();
    const seen = new Map<string, number>();
    for (const d of docs) if (typeof d.slug === 'string' && RE.slug.test(d.slug)) seen.set(d.slug, (seen.get(d.slug) ?? 0) + 1);
    for (const k of seen.keys()) taken.add(k);
    for (const d of docs) {
      const path = `${kind === 'template' ? 'templates' : 'deployments'}/${d.id}`;
      if (typeof d.slug === 'string' && RE.slug.test(d.slug)) {
        entities.push({ path, kind, label: d.label, slug: d.slug, action: (seen.get(d.slug) ?? 0) > 1 ? 'duplicate' : 'none' });
        continue;
      }
      const ns = uniqueSlug(slugifyLabel(d.label, kind === 'template' ? 't-' : 'd-'), taken);
      taken.add(ns);
      entities.push({ path, kind, label: d.label, slug: typeof d.slug === 'string' ? d.slug : null, action: 'assign', newSlug: ns });
    }
  }
  const counts = { assign: 0, none: 0, duplicate: 0 };
  for (const e of entities) counts[e.action]++;
  return { entities, counts };
}

export function printPlanK(p: PlanK) {
  console.log(`entities: ${p.entities.length} — assign ${p.counts.assign}, keep ${p.counts.none}, duplicate ${p.counts.duplicate}`);
  for (const e of p.entities) if (e.action !== 'none') console.log(`  ${e.action.padEnd(9)} ${e.path} "${e.label}"${e.newSlug ? ` → ${e.newSlug}` : ` (${e.slug})`}`);
}

export async function applyMigrationK(db: Firestore, p: PlanK): Promise<{ applied: number; refused: string[] }> {
  let applied = 0;
  const refused: string[] = [];
  for (const e of p.entities) {
    if (e.action !== 'assign') continue;
    const ref = db.doc(e.path);
    const ok = await db.runTransaction(async (tx) => {
      const s = await tx.get(ref);
      const cur = s.data()?.slug;
      if (!s.exists || (typeof cur === 'string' && RE.slug.test(cur))) return false; // changed since the plan (or assigned by ensureSlug)
      const clash = await tx.get(db.collection(e.kind === 'template' ? 'templates' : 'deployments').where('slug', '==', e.newSlug));
      if (!clash.empty) return false;
      tx.update(ref, { slug: e.newSlug });
      return true;
    });
    if (ok) applied++;
    else refused.push(`${e.path}: changed since the plan (rerun)`);
  }
  if (applied) {
    const ref = db.collection('auditLog').doc();
    await ref.create({
      auditId: ref.id,
      ts: new Date().toISOString(),
      op: 'system:migrate-k',
      actor: 'system',
      actorEmail: null,
      action: 'system.migration',
      outcome: 'ok',
      status: 200,
      errorCode: null,
      errorMessage: null,
      targetType: 'system',
      target: 'system/migration-k',
      requestId: null,
      ip: null,
      userAgent: null,
      changesTotal: 0,
      _changes: '[]',
      _data: JSON.stringify({ migration: 'k', dryRun: false, applied, refused, counts: p.counts, entities: p.entities.filter((x) => x.action !== 'none') }).slice(0, 900_000),
      _actionKeys: auditActionKeys('system.migration'),
    });
  }
  return { applied, refused };
}

async function main() {
  const { arg, flag, init } = await import('./_admin.ts');
  const project = arg('project');
  const apply = flag('apply');
  const report = arg('report');
  if (!project) {
    console.error('usage: tsx scripts/migrate-k.ts --project <id> [--dry-run (default) | --apply] [--report <file.json>]');
    process.exit(2);
  }
  const { db } = init(project);
  console.log(`migrate-k on ${project} — ${apply ? 'APPLY' : 'dry run (nothing is written)'}`);
  const plan = await planMigrationK(db);
  printPlanK(plan);
  let result: { applied: number; refused: string[] } | null = null;
  if (apply) {
    result = await applyMigrationK(db, plan);
    console.log(result.applied ? `applied ${result.applied} (+ one system.migration audit entry)` : 'nothing to do');
    for (const r of result.refused) console.log(`REFUSED ${r}`);
  }
  if (report) writeFileSync(report, JSON.stringify({ project, apply, counts: plan.counts, entities: plan.entities, result }, null, 1));
  if (result?.refused.length) process.exit(3);
}
if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href)
  main().catch((e) => {
    console.error(e);
    process.exit(1);
  });
