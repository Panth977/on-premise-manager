/**
 * migrate-h — §21 #101: volumes get their OWN content; Files are never volume content and never sit inside a writable
 * volume. Runs the shared `migrateVolumeFileSeeds` on every template / draft config:
 *   1. legacy seeds `files` → `tree`, `file` → `single` (each entry takes the referenced File's content blob sha256 + size,
 *      mode 0644, render = the File's render mode; an entry whose File has no content is dropped with a NOTE);
 *   2. a File mounted INSIDE a writable volume mount of the same container moves into that volume's content (the image
 *      volume row becomes "Not mounted" / the extra mount is removed); host / single-file / archive targets and expression
 *      paths are NOTES (left as is; validation keeps V_FILE_IN_WRITABLE_VOLUME);
 *   3. Files nothing references any more are removed from `files`.
 *   in: templates `_body` (version + updatedAt KEPT), deployments `_draft` + `_srcSnapshot` + `_srcBase` (also deleted
 *   deployments). NOT revisions: immutable history (hash), old agents keep applying them, agent 0.12 still decodes legacy
 *   seeds.
 *   Drafts (decision, TEAM.md "Round VC"): a changed draft gets draftVersion + 1 and updatedAt = now (updatedBy
 *   "system:migrate-h") so an open editor's next write answers 409 and reloads instead of writing the legacy seed back;
 *   `_srcSnapshot` / `_srcBase` change silently (Template sync then sees no fake template change).
 *   Image volumes (rows without their own mountPath) come from services/{serviceSlug}/images/{version}.
 *   Existing sites: the agent (≥ 0.12) fills the 0-byte former File mountpoint inside the volume ONCE at the next apply
 *   (activity volume.placeholder-filled) — printed per moved mount.
 *   One `system.migration` audit entry per --apply run that changes something (op system:migrate-h, data.migration 'h').
 *   Idempotent: a second run plans 0 changes.
 *
 * Usage (from backend/):
 *   emulator: FIRESTORE_EMULATOR_HOST=127.0.0.1:8085 npx tsx scripts/migrate-h.ts --project demo-opm [--dry-run]
 *   prod:     OPM_GCLOUD_TOKEN=1 OPM_GCLOUD_ACCOUNT=<owner> npx tsx scripts/migrate-h.ts --project your-firebase-project [--dry-run | --apply]
 * Default is the dry run; only --apply writes.
 */
import { pathToFileURL } from 'node:url';
import type { DocStore as Firestore } from '../functions/src/ports/index.js';
import { auditActionKeys, isLegacyFileSeed, migrateVolumeFileSeeds, normalizeVolumeTree, treeStats, volumeTreeHashInput, type Config, type FileDef, type VolumeSeed } from '@opm/shared';
import { sha256Hex } from '../functions/src/core/util.ts';

type Doc = Record<string, unknown>;
type MigCfg = Pick<Config, 'files' | 'sites'>;
const isObj = (v: unknown): v is Doc => !!v && typeof v === 'object' && !Array.isArray(v);

export type Op = { op: 'update'; path: string; data: Record<string, unknown>; why: string };
export interface DocSummary {
  path: string;
  lines: string[];
  notes: string[];
}
export interface PlanH {
  ops: Op[];
  docs: DocSummary[];
  warnings: string[];
  counts: Record<string, number>;
}
type ImageVolumes = (serviceSlug: string, version: string) => { name: string; mountPath: string }[] | null;

const short = (sha: string) => `${sha.slice(0, 19)}…`;
function seedText(sd: VolumeSeed | null | undefined, files: FileDef[]): string {
  if (!sd) return 'none (starts empty)';
  const f = (slug: string) => {
    const x = files.find((y) => y.fileSlug === slug);
    return x?.content ? `${x.content.sizeBytes} B ${short(x.content.sha256)}` : 'no content';
  };
  if (sd.kind === 'files') return `files [${sd.entries.map((e) => `${e.path} ← file ${e.fileSlug} (${f(e.fileSlug)})`).join(', ')}]`;
  if (sd.kind === 'file') return `file ${sd.fileSlug} (${f(sd.fileSlug)})`;
  if (sd.kind === 'tree') {
    const t = normalizeVolumeTree(sd);
    const st = treeStats(t);
    return `tree ${st.fileCount} file(s), ${t.dirs.length} empty folder(s), ${st.totalBytes} B, treeHash ${short(`sha256:${sha256Hex(volumeTreeHashInput(t))}`)} [${t.entries.map((e) => `${e.path} ${e.size} B ${short(e.sha256)} render ${e.render}`).join(', ')}]`;
  }
  if (sd.kind === 'single') return `single ${sd.size} B ${short(sd.sha256)} render ${sd.render}`;
  return `archive ${sd.name}`;
}

/** Migrate one config-like; lines = before / after per changed volume + the shared lines; notes = the shared notes. */
export function convertConfigH(cfg: MigCfg, imageVolumes: ImageVolumes, tally: Record<string, number>): { next: MigCfg | null; lines: string[]; notes: string[] } {
  if (!cfg || !Array.isArray(cfg.sites) || !Array.isArray(cfg.files)) return { next: null, lines: [], notes: [] };
  const r = migrateVolumeFileSeeds(cfg, imageVolumes);
  const lines: string[] = [];
  if (r.changed) {
    for (const site of cfg.sites)
      for (const v of site.config?.volumes ?? []) {
        const after = r.config.sites.find((s) => s.siteSlug === site.siteSlug)?.config.volumes?.find((x) => x.volumeSlug === v.volumeSlug);
        if (JSON.stringify(v.seed ?? null) === JSON.stringify(after?.seed ?? null)) continue;
        lines.push(`site ${site.siteSlug}: volume ${v.volumeSlug} before: ${seedText(v.seed, cfg.files)}`);
        lines.push(`site ${site.siteSlug}: volume ${v.volumeSlug} after:  ${seedText(after?.seed, r.config.files)}`);
        if (isLegacyFileSeed(v.seed)) tally.seedsConverted = (tally.seedsConverted ?? 0) + 1;
      }
    lines.push(...r.lines);
    for (const m of r.moved) lines.push(`site ${m.siteSlug}: ${m.containerSlug}: file ${m.fileSlug} → volume ${m.volumeSlug} content ${m.path} — existing sites: the agent (≥ 0.12) fills the 0-byte former mountpoint ${m.path} once at the next apply (volume.placeholder-filled)`);
    tally.mountsMoved = (tally.mountsMoved ?? 0) + r.moved.length;
    tally.filesRemoved = (tally.filesRemoved ?? 0) + r.removedFiles.length;
  }
  tally.notes = (tally.notes ?? 0) + r.notes.length;
  return { next: r.changed ? r.config : null, lines, notes: r.notes };
}

/** image volumes of the catalog (services/{s}/images/{v}.volumes) */
export async function loadImageVolumes(db: Firestore): Promise<ImageVolumes> {
  const map = new Map<string, { name: string; mountPath: string }[]>();
  for (const s of (await db.collection('services').get()).docs)
    for (const i of (await s.ref.collection('images').get()).docs) {
      const vols = i.data().volumes;
      map.set(`${s.id}:${i.id}`, Array.isArray(vols) ? (vols as Doc[]).filter(isObj).map((v) => ({ name: String(v.name), mountPath: String(v.mountPath) })) : []);
    }
  return (serviceSlug, version) => map.get(`${serviceSlug}:${version}`) ?? null;
}

export async function planMigrationH(db: Firestore): Promise<PlanH> {
  const plan: PlanH = { ops: [], docs: [], warnings: [], counts: {} };
  const tally: Record<string, number> = {};
  const count = (k: string, n = 1) => (plan.counts[k] = (plan.counts[k] ?? 0) + n);
  const iv = await loadImageVolumes(db);
  const parse = (s: unknown, where: string): MigCfg | null => {
    if (typeof s !== 'string') return null;
    try {
      const x = JSON.parse(s);
      return isObj(x) ? (x as unknown as MigCfg) : null;
    } catch (e) {
      plan.warnings.push(`${where}: JSON does not parse (${(e as Error).message}) — skipped`);
      return null;
    }
  };

  for (const d of (await db.collection('templates').get()).docs) {
    const t = d.data();
    const path = `templates/${d.id}`;
    const body = parse(t._body, path);
    if (!body) continue;
    const r = convertConfigH(body, iv, tally);
    if (!r.next) {
      if (r.notes.length) plan.docs.push({ path: `${path} "${t.label}" v${t.version} (no change)`, lines: [], notes: r.notes });
      continue;
    }
    plan.ops.push({ op: 'update', path, data: { _body: JSON.stringify({ ...body, ...r.next }) }, why: 'template volume content (version / updatedAt unchanged)' });
    plan.docs.push({ path: `${path} "${t.label}" v${t.version}`, lines: r.lines, notes: r.notes });
    count('templates');
  }
  for (const d of (await db.collection('deployments').get()).docs) {
    const x = d.data();
    const path = `deployments/${d.id}`;
    const upd: Doc = {};
    const lines: string[] = [];
    const notes: string[] = [];
    for (const [field, label] of [
      ['_draft', 'draft'],
      ['_srcSnapshot', 'srcTemplate snapshot'],
      ['_srcBase', 'srcTemplate base'],
    ] as const) {
      const cfg = parse(x[field], `${path} ${label}`);
      if (!cfg) continue;
      const r = convertConfigH(cfg, iv, tally);
      notes.push(...r.notes.map((l) => `${label}: ${l}`));
      if (!r.next) continue;
      upd[field] = JSON.stringify({ ...cfg, ...r.next });
      lines.push(...r.lines.map((l) => `${label}: ${l}`));
    }
    if ('_draft' in upd) {
      const dv = typeof x.draftVersion === 'number' ? x.draftVersion : 0;
      upd.draftVersion = dv + 1;
      upd.updatedAt = new Date().toISOString();
      upd.updatedBy = 'system:migrate-h';
      lines.push(`draftVersion ${dv} → ${dv + 1} (open editors reload)`);
    }
    if (Object.keys(upd).length) {
      plan.ops.push({ op: 'update', path, data: upd, why: `deployment ${Object.keys(upd).filter((k) => k.startsWith('_')).join(', ')} volume content` });
      plan.docs.push({ path: `${path} "${x.label}"${x.deleted ? ' (deleted)' : ''}`, lines, notes });
      count('deployments');
    } else if (notes.length) plan.docs.push({ path: `${path} "${x.label}"${x.deleted ? ' (deleted)' : ''} (no change)`, lines: [], notes });
  }
  for (const [k, v] of Object.entries(tally)) count(k, v);
  return plan;
}

/** Apply a plan (batches of 400) + one system.migration audit entry. */
export async function applyPlanH(db: Firestore, plan: PlanH): Promise<void> {
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
    op: 'system:migrate-h',
    actor: 'system',
    actorEmail: null,
    action: 'system.migration',
    outcome: 'ok',
    status: 200,
    errorCode: null,
    errorMessage: null,
    targetType: 'system',
    target: 'system/migration-h',
    requestId: null,
    ip: null,
    userAgent: null,
    changesTotal: 0,
    _changes: '[]',
    _data: JSON.stringify({ migration: 'h', dryRun: false, changes: plan.ops.length, counts: plan.counts, docs, docsTotal: plan.docs.length, warnings: plan.warnings.slice(0, 100) }),
    _actionKeys: auditActionKeys('system.migration'),
  });
}

export function printPlan(plan: PlanH, log: (s: string) => void = console.log) {
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
    console.error('usage: tsx scripts/migrate-h.ts --project <id> [--dry-run (default) | --apply]');
    process.exit(2);
  }
  const { db, emulator } = init(project);
  console.log(`migrate-h on ${project}${emulator ? ' (emulator)' : ''} — ${apply ? 'APPLY' : 'dry run (nothing is written)'}`);
  const plan = await planMigrationH(db);
  printPlan(plan);
  if (apply) {
    await applyPlanH(db, plan);
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
