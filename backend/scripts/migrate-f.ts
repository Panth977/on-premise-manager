/**
 * migrate-f — image volume kind (SPEC §21 #83): every stored image volume without `kind` gets the kind inferred from its
 * mount path (shared `inferImageVolumeKind`: a last path segment with an extension → 'file', e.g. /data/flows.json; else
 * 'dir'). Only missing keys are filled; an explicit kind is never changed. Image `updatedAt` / `updatedBy` are kept.
 *   in: services/{serviceSlug}/images/{version} (the `volumes` list) — NOTHING ELSE. Revision catalogs (heads, proposals,
 *   stored revisions) keep their own ImageSpec copy (hash + signature unchanged); readers infer the same kind there.
 *   NOTES (no change): per template / deployment draft container, the effect of the new kinds on its image volume rows —
 *   a single-file data volume on a now-File image volume (the V_VOLUME_KIND_IMAGE warning is gone), a folder data volume on
 *   a now-File image volume (new V_VOLUME_KIND_MISMATCH error: pick a single-file volume, or set the image volume to Folder).
 *   One `system.migration` audit entry per --apply run that changes something (op system:migrate-f, data.migration = 'f').
 *   Idempotent: a second run plans 0 changes.
 *
 * Usage (from backend/):
 *   npx tsx scripts/migrate-f.ts --data-dir <dir> [--dry-run | --apply]
 * Default is the dry run; only --apply writes.
 */
import { pathToFileURL } from 'node:url';
import type { DocStore as Firestore } from '../functions/src/ports/index.js';
import { auditActionKeys, imageKey, imageVolumeKind, inferImageVolumeKind } from '@opm/shared';

type Doc = Record<string, unknown>;
const isObj = (v: unknown): v is Doc => !!v && typeof v === 'object' && !Array.isArray(v);

export type Op = { op: 'update'; path: string; data: Record<string, unknown>; why: string };
export interface DocSummary {
  path: string;
  lines: string[];
  notes: string[];
}
export interface PlanF {
  ops: Op[];
  docs: DocSummary[];
  warnings: string[];
  counts: Record<string, number>;
}

/** Fill the missing kinds of one image's volumes; null = nothing to do. */
export function convertImageVolumes(vols: unknown): { next: Doc[] | null; lines: string[] } {
  const lines: string[] = [];
  if (!Array.isArray(vols)) return { next: null, lines };
  let changed = false;
  const next = vols.map((v) => {
    if (!isObj(v) || v.kind === 'dir' || v.kind === 'file') return v as Doc;
    if (v.kind !== undefined && v.kind !== null) {
      lines.push(`volume ${String(v.name)}: invalid kind ${JSON.stringify(v.kind)} left as is (fix it on the image page)`);
      return v;
    }
    changed = true;
    const kind = inferImageVolumeKind(v.mountPath);
    lines.push(`volume ${String(v.name)} (${String(v.mountPath)}) → kind ${kind} (${kind === 'file' ? 'File' : 'Folder'})`);
    return { ...v, kind };
  });
  return { next: changed ? next : null, lines };
}

/** Notes for the image volume rows of one config (template body / draft) under the new kinds. */
function configNotes(cfg: Doc, kinds: Map<string, Map<string, 'dir' | 'file'>>, changedKeys: Set<string>): string[] {
  const out: string[] = [];
  for (const site of Array.isArray(cfg.sites) ? (cfg.sites as Doc[]) : []) {
    const sc = isObj(site?.config) ? site.config : null;
    if (!sc) continue;
    const vols = Array.isArray(sc.volumes) ? (sc.volumes as Doc[]) : [];
    for (const c of Array.isArray(sc.containers) ? (sc.containers as Doc[]) : []) {
      const key = imageKey(String(c.serviceSlug), String(c.version));
      if (!changedKeys.has(key)) continue;
      for (const row of Array.isArray(c.volumes) ? (c.volumes as Doc[]) : []) {
        if (!isObj(row) || typeof row.imageVolume !== 'string' || (row.mountPath !== null && row.mountPath !== undefined)) continue;
        const src = isObj(row.source) ? row.source : {};
        if (src.kind !== 'volume') continue;
        const ivKind = kinds.get(key)?.get(row.imageVolume);
        if (!ivKind) continue;
        const sv = vols.find((x) => x?.volumeSlug === src.volumeSlug);
        const svKind = sv?.kind === 'file' ? 'file' : 'dir';
        const at = `site ${String(site.siteSlug)} container ${String(c.containerSlug ?? c.serviceSlug)} image volume ${row.imageVolume}`;
        if (svKind === 'file' && ivKind === 'file') out.push(`${at}: single-file volume ${String(src.volumeSlug)} on a File image volume — V_VOLUME_KIND_IMAGE warning gone`);
        else if (svKind === 'dir' && ivKind === 'file') out.push(`${at}: folder volume ${String(src.volumeSlug)} on a File image volume — NEW error V_VOLUME_KIND_MISMATCH (pick a single-file volume, or set the image volume to Folder)`);
      }
    }
  }
  return out;
}

export async function planMigrationF(db: Firestore): Promise<PlanF> {
  const plan: PlanF = { ops: [], docs: [], warnings: [], counts: {} };
  const count = (k: string, n = 1) => (plan.counts[k] = (plan.counts[k] ?? 0) + n);
  const kinds = new Map<string, Map<string, 'dir' | 'file'>>();
  const changedKeys = new Set<string>();
  for (const s of (await db.collection('services').get()).docs) {
    for (const d of (await s.ref.collection('images').get()).docs) {
      const i = d.data();
      const key = imageKey(s.id, d.id);
      const r = convertImageVolumes(i.volumes);
      const vols = (r.next ?? (Array.isArray(i.volumes) ? i.volumes : [])) as Doc[];
      kinds.set(key, new Map(vols.filter(isObj).map((v) => [String(v.name), imageVolumeKind(v as { mountPath: string; kind?: unknown })])));
      if (!r.next) {
        if (r.lines.length) plan.warnings.push(...r.lines.map((l) => `${d.ref.path}: ${l}`));
        continue;
      }
      changedKeys.add(key);
      plan.ops.push({ op: 'update', path: d.ref.path, data: { volumes: r.next }, why: 'image volumes → explicit kind (inferred from the path; updatedAt unchanged)' });
      plan.docs.push({ path: `${d.ref.path} (${key})`, lines: r.lines, notes: [] });
      count('images');
      count('volumes', r.lines.filter((l) => l.includes('→ kind')).length);
      count('volumesFile', r.lines.filter((l) => l.includes('→ kind file')).length);
    }
  }
  if (changedKeys.size) {
    for (const d of (await db.collection('templates').get()).docs) {
      const t = d.data();
      if (typeof t._body !== 'string') continue;
      const notes = configNotes(JSON.parse(t._body), kinds, changedKeys);
      if (notes.length) plan.docs.push({ path: `templates/${d.id} "${t.label}" (no change)`, lines: [], notes });
      count('notes', notes.length);
    }
    for (const d of (await db.collection('deployments').where('deleted', '==', false).get()).docs) {
      const x = d.data();
      if (typeof x._draft !== 'string') continue;
      const notes = configNotes(JSON.parse(x._draft), kinds, changedKeys);
      if (notes.length) plan.docs.push({ path: `deployments/${d.id} "${x.label}" draft (no change)`, lines: [], notes });
      count('notes', notes.length);
    }
  }
  return plan;
}

/** Apply a plan (batches of 400) + one system.migration audit entry. */
export async function applyPlanF(db: Firestore, plan: PlanF): Promise<void> {
  if (!plan.ops.length) return;
  for (let i = 0; i < plan.ops.length; i += 400) {
    const b = db.batch();
    for (const o of plan.ops.slice(i, i + 400)) b.update(db.doc(o.path), o.data);
    await b.commit();
  }
  const ref = db.collection('auditLog').doc();
  const cut = (l: string[]) => l.slice(0, 80).map((x) => x.slice(0, 400));
  let docs = plan.docs.map((d) => ({ path: d.path, lines: cut(d.lines), notes: cut(d.notes) }));
  while (JSON.stringify(docs).length > 600_000 && docs.length > 1) docs = docs.slice(0, Math.ceil(docs.length / 2));
  await ref.create({
    auditId: ref.id,
    ts: new Date().toISOString(),
    op: 'system:migrate-f',
    actor: 'system',
    actorEmail: null,
    action: 'system.migration',
    outcome: 'ok',
    status: 200,
    errorCode: null,
    errorMessage: null,
    targetType: 'system',
    target: 'system/migration-f',
    requestId: null,
    ip: null,
    userAgent: null,
    changesTotal: 0,
    _changes: '[]',
    _data: JSON.stringify({ migration: 'f', dryRun: false, changes: plan.ops.length, counts: plan.counts, docs, docsTotal: plan.docs.length, warnings: plan.warnings.slice(0, 100) }),
    _actionKeys: auditActionKeys('system.migration'),
  });
}

export function printPlan(plan: PlanF, log: (s: string) => void = console.log) {
  for (const d of plan.docs) {
    log(`\n● ${d.path}`);
    for (const l of d.lines) log(`    ${l}`);
    for (const n of d.notes) log(`    NOTE ${n}`);
  }
  if (plan.warnings.length) log('');
  for (const w of plan.warnings) log(`WARN ${w}`);
  log(`\ncounts: ${JSON.stringify(plan.counts)}\n${plan.ops.length} change(s)`);
}

async function main() {
  const { arg, flag, init } = await import('./_admin.ts');
  const project = arg('project');
  const apply = flag('apply');
  if (!project) {
    console.error('usage: tsx scripts/migrate-f.ts --project <id> [--dry-run (default) | --apply]');
    process.exit(2);
  }
  const { db } = init(project);
  console.log(`migrate-f on ${project} — ${apply ? 'APPLY' : 'dry run (nothing is written)'}`);
  const plan = await planMigrationF(db);
  printPlan(plan);
  if (apply) {
    await applyPlanF(db, plan);
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
