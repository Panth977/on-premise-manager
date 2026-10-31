/**
 * migrate-j — §21 #105 "one action: Commit" (docs-v2/branches-commits.md §15, J14). Whole database, idempotent.
 *
 * Since #105 there is no server-side working copy: the template config fields / Deployment.draft are a COPY of the
 * head commit config, written only by commits. Before 0.14.1 they were a working copy that could hold uncommitted
 * edits. This migration, for every template and every deployment (deleted ones too):
 *   1. copy ≠ head (merge items > 0; deployments compare secret values by hash), or no head and a non-empty copy →
 *      ONE commit kind "change", author system, message "Uncommitted changes from before 0.14.1 (auto-committed)",
 *      data.autoCommitted { from: 'cloud-working-copy', items } (+ data.secretValues slugs for deployments), stats,
 *      deployments: catalog built from the live catalog (icons are kept as references, not embedded) + agentEpoch+1.
 *      Validation is REPORTED, never enforced (a migration must not lose data). No compat proposal is written for
 *      sites < 0.14 (reported: they get the next commit made in the console as a proposal). No cloud activity.
 *   2. `branch.uncommitted` is removed (the whole `branch` map is rewritten without it).
 * Every write is one transaction per entity that RE-READS the entity and refuses to write when its head or its copy
 * changed since the plan (a concurrent commit through the API wins; rerun the migration).
 * One `system.migration` audit entry per --apply run that changed something (op system:migrate-j, data.migration 'j').
 *
 * Usage (from backend/):
 *   npx tsx scripts/migrate-j.ts --data-dir <dir> [--dry-run | --apply] [--report <file.json>]
 * Default is the dry run; only --apply writes.
 */
import { writeFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
import type { DocStore as Firestore } from '../src/driver/index.js';
import {
  auditActionKeys,
  commitRef,
  hasSecretValueChanges,
  makeCommit,
  secretValueChanges,
  uncommittedItems,
  ulid,
  validateConfig,
  type BranchRef,
  type BranchState,
  type Commit,
  type CommitAuthor,
  type Config,
  type RevisionCatalog,
} from '@opm/shared';
import { decodeDeployment, decodeTemplate } from '../src/core/codec.ts';
import { encodeCommit, COMMIT_INLINE_MAX } from '../src/core/branches.ts';
import { normalizeStored, setImageVolumes } from '../src/core/config.ts';
import { catalogImage, imageSpec, imageView, serviceView, type Catalog } from '../src/core/catalog.ts';

type Doc = Record<string, unknown>;
export const MIGRATE_J_MESSAGE = 'Uncommitted changes from before 0.14.1 (auto-committed)';
const SYSTEM: CommitAuthor = { kind: 'system', email: 'system', uid: null, name: null, siteSlug: null };
const isEmpty = (c: Config) => !c.variables.length && !c.secrets.length && !c.files.length && !c.sites.length;
const cfgOf = (t: Pick<Config, 'variables' | 'secrets' | 'files' | 'sites'>): Config => ({ variables: t.variables, secrets: t.secrets, files: t.files, sites: t.sites });

export interface EntityJ {
  path: string;
  kind: 'template' | 'deployment';
  label: string;
  action: 'commit' | 'strip' | 'none';
  items: number;
  head: { seq: number; commitId: string } | null;
  newSeq?: number;
  keys?: string[];
  validationErrors?: string[];
  notes: string[];
}
export interface PlanJ {
  entities: EntityJ[];
  counts: { templates: number; deployments: number; commits: number; stripped: number; unchanged: number };
  warnings: string[];
}
interface Work {
  e: EntityJ;
  ref: string;
  /** the raw entity doc fields the plan was made on (re-checked in the transaction) */
  headId: string | null;
  copyRaw: string;
  commit: Commit | null;
  branch: BranchState;
  agentEpoch?: number;
}

async function readCommitConfig(db: Firestore, bucket: Bucket | undefined, path: string): Promise<Commit | null> {
  const s = await db.doc(path).get();
  if (!s.exists) return null;
  const d = s.data() as Doc & { _config?: string; _catalog?: string | null; _configRef?: string; _data?: string };
  let config: Config;
  if (d._configRef) {
    if (!bucket) throw new Error(`${path}: the body lives in Storage (${d._configRef}) — pass a bucket`);
    const [buf] = await bucket.file(d._configRef).download();
    config = (JSON.parse(buf.toString('utf8')) as { config: Config }).config;
  } else config = d._config ? (JSON.parse(d._config) as Config) : { variables: [], secrets: [], files: [], sites: [] };
  return { ...(d as unknown as Commit), config };
}
interface Bucket {
  file: (p: string) => { save: (b: Buffer, o: object) => Promise<unknown>; download: () => Promise<[Buffer]> };
}

async function loadCatalogFrom(db: Firestore): Promise<Catalog> {
  const [svc, imgs, pages] = await Promise.all([db.collection('services').get(), db.collectionGroup('images').get(), db.collection('notFoundPages').select('label').get()]);
  const services: Catalog['services'] = {};
  for (const d of svc.docs) services[d.id] = serviceView(d.data(), d.id);
  const images: Catalog['images'] = {};
  for (const d of imgs.docs) {
    const parent = d.ref.parent.parent;
    if (!parent || parent.parent.id !== 'services') continue;
    const i = imageView(d.data(), d.id, parent.id);
    images[`${i.serviceSlug}:${i.version}`] = i;
  }
  return { services, images, notFoundPageIds: pages.docs.map((d) => d.id) };
}
async function catalogFor(db: Firestore, config: Config, cat: Catalog): Promise<{ catalog: RevisionCatalog; missing: string[] }> {
  const out: RevisionCatalog = { services: {}, images: {}, notFoundPages: {} };
  const missing: string[] = [];
  const pages = new Set<string>();
  for (const site of config.sites) {
    if (site.config.notFoundPageId) pages.add(site.config.notFoundPageId);
    for (const c of site.config.containers) {
      const s = cat.services[c.serviceSlug];
      const i = cat.images[`${c.serviceSlug}:${c.version}`];
      if (!s || !i) {
        missing.push(`${c.serviceSlug}:${c.version}`);
        continue;
      }
      out.services[s.serviceSlug] = { serviceSlug: s.serviceSlug, label: s.label, icon: s.icon ?? null };
      out.images[`${c.serviceSlug}:${c.version}`] = imageSpec(i);
    }
  }
  for (const id of pages) {
    const p = await db.collection('notFoundPages').doc(id).get();
    if (p.exists) out.notFoundPages[id] = { notFoundPageId: id, label: String(p.data()!.label ?? ''), htmlCode: String(p.data()!.htmlCode ?? '') };
    else missing.push(`notFoundPage ${id}`);
  }
  return { catalog: out, missing };
}
function vctx(cat: Catalog, mode: 'template' | 'publish', deploymentId: string) {
  const services: Record<string, { label: string }> = {};
  for (const [k, s] of Object.entries(cat.services)) services[k] = { label: s.label };
  const images: Record<string, ReturnType<typeof catalogImage>> = {};
  for (const [k, i] of Object.entries(cat.images)) images[k] = catalogImage(i);
  return { mode, deploymentId, services, images, notFoundPageIds: cat.notFoundPageIds } as Parameters<typeof validateConfig>[1];
}
const stripBranch = (b: Doc | undefined): Doc | undefined => {
  if (!b) return b;
  const { uncommitted: _u, ...rest } = b;
  void _u;
  return rest;
};

export async function planMigrationJ(db: Firestore, bucket?: Bucket): Promise<{ plan: PlanJ; work: Work[] }> {
  const plan: PlanJ = { entities: [], counts: { templates: 0, deployments: 0, commits: 0, stripped: 0, unchanged: 0 }, warnings: [] };
  const work: Work[] = [];
  const cat = await loadCatalogFrom(db);
  const vols: Record<string, { volumes: Catalog['images'][string]['volumes'] }> = {};
  for (const [k, i] of Object.entries(cat.images)) vols[k] = { volumes: Array.isArray(i.volumes) ? i.volumes : [] };
  setImageVolumes(vols);
  const now = new Date().toISOString();

  // ── templates
  for (const d of (await db.collection('templates').get()).docs) {
    plan.counts.templates++;
    const raw = d.data() as Doc;
    const t = decodeTemplate(raw);
    const branch = (raw.branch as Doc | undefined) ?? undefined;
    const ref: BranchRef = { kind: 'template', templateId: d.id };
    const headRef = (branch?.head as { commitId?: string; seq?: number } | null | undefined) ?? null;
    const head = headRef?.commitId ? await readCommitConfig(db, bucket, `templates/${d.id}/commits/${headRef.commitId}`) : null;
    const e: EntityJ = { path: `templates/${d.id}`, kind: 'template', label: t.label, action: 'none', items: 0, head: headRef?.commitId ? { seq: headRef.seq ?? 0, commitId: headRef.commitId } : null, notes: [] };
    if (!branch) e.notes.push('no branch (migrate-i not run?) — skipped');
    if (headRef?.commitId && !head) e.notes.push(`head commit ${headRef.commitId} is missing — skipped`);
    const copy = { ...cfgOf(t), secrets: t.secrets.map((s) => ({ ...s, value: null, rotatedAt: null })) };
    const items = branch && (head || !headRef?.commitId) ? (head ? uncommittedItems(normalizeStored(head.config), copy) : isEmpty(copy) ? [] : uncommittedItems(null, copy)) : [];
    const hasU = !!branch && 'uncommitted' in branch;
    let commit: Commit | null = null;
    if (items.length) {
      commit = makeCommit({ commitId: ulid(), branch: ref, parent: head ? { commitId: head.commitId, seq: head.seq, config: normalizeStored(head.config) } : null, kind: 'change', message: MIGRATE_J_MESSAGE, author: SYSTEM, createdAt: now, config: copy, catalog: null, data: { autoCommitted: { from: 'cloud-working-copy', items: items.length } } });
      const v = validateConfig(copy, vctx(cat, 'template', 'template'));
      e.validationErrors = v.errors.slice(0, 20).map((i) => `${i.path}: ${i.message}`);
      e.action = 'commit';
      e.items = items.length;
      e.newSeq = commit.seq;
      e.keys = items.slice(0, 50).map((i) => i.key);
    } else if (hasU) e.action = 'strip';
    plan.entities.push(e);
    if (e.action === 'none') {
      plan.counts.unchanged++;
      continue;
    }
    if (e.action === 'commit') plan.counts.commits++;
    else plan.counts.stripped++;
    const nb: BranchState = commit ? { head: commitRef(commit), origin: (branch?.origin as BranchState['origin']) ?? null, commitCount: Number(branch?.commitCount ?? 0) + 1 } : (stripBranch(branch) as unknown as BranchState);
    work.push({ e, ref: e.path, headId: headRef?.commitId ?? null, copyRaw: String(raw._body ?? ''), commit, branch: nb });
  }

  // ── deployments
  for (const d of (await db.collection('deployments').get()).docs) {
    plan.counts.deployments++;
    const raw = d.data() as Doc;
    const dep = decodeDeployment(raw);
    const branch = (raw.branch as Doc | undefined) ?? undefined;
    const ref: BranchRef = { kind: 'deployment', deploymentId: d.id };
    const headRef = (branch?.head as { commitId?: string; seq?: number } | null | undefined) ?? null;
    const head = headRef?.commitId ? await readCommitConfig(db, bucket, `deployments/${d.id}/commits/${headRef.commitId}`) : null;
    const e: EntityJ = { path: `deployments/${d.id}`, kind: 'deployment', label: `${dep.label}${dep.deleted ? ' (deleted)' : ''}`, action: 'none', items: 0, head: headRef?.commitId ? { seq: headRef.seq ?? 0, commitId: headRef.commitId } : null, notes: [] };
    if (!branch) e.notes.push('no branch (migrate-i not run?) — skipped');
    if (headRef?.commitId && !head) e.notes.push(`head commit ${headRef.commitId} is missing — skipped`);
    const copy = dep.draft;
    const items = branch && (head || !headRef?.commitId) ? (head ? uncommittedItems(normalizeStored(head.config), copy, { secretValues: true }) : isEmpty(copy) ? [] : uncommittedItems(null, copy, { secretValues: true })) : [];
    const hasU = !!branch && 'uncommitted' in branch;
    let commit: Commit | null = null;
    if (items.length) {
      const { catalog, missing } = await catalogFor(db, copy, cat);
      if (missing.length) e.notes.push(`catalog entries missing (kept out of the commit catalog): ${missing.join(', ')}`);
      const sv = secretValueChanges(head?.config.secrets ?? [], copy.secrets);
      commit = makeCommit({ commitId: ulid(), branch: ref, parent: head ? { commitId: head.commitId, seq: head.seq, config: normalizeStored(head.config) } : null, kind: 'change', message: MIGRATE_J_MESSAGE, author: SYSTEM, createdAt: now, config: copy, catalog, data: { autoCommitted: { from: 'cloud-working-copy', items: items.length }, ...(hasSecretValueChanges(sv) ? { secretValues: sv } : {}) } });
      const v = validateConfig(copy, vctx(cat, 'publish', d.id));
      e.validationErrors = v.errors.slice(0, 20).map((i) => `${i.path}: ${i.message}`);
      const agents = await db.collection('deployments').doc(d.id).collection('agents').select('agentVersion').get();
      if (agents.docs.length) e.notes.push(`${agents.docs.length} enrolled site(s): the sites see this commit as the new cloud head (no compat proposal is written by the migration; sites < 0.14 get the next console commit as a proposal)`);
      e.action = 'commit';
      e.items = items.length;
      e.newSeq = commit.seq;
      e.keys = items.slice(0, 50).map((i) => i.key);
    } else if (hasU) e.action = 'strip';
    plan.entities.push(e);
    if (e.action === 'none') {
      plan.counts.unchanged++;
      continue;
    }
    if (e.action === 'commit') plan.counts.commits++;
    else plan.counts.stripped++;
    const nb: BranchState = commit ? { head: commitRef(commit), origin: (branch?.origin as BranchState['origin']) ?? null, commitCount: Number(branch?.commitCount ?? 0) + 1 } : (stripBranch(branch) as unknown as BranchState);
    work.push({ e, ref: e.path, headId: headRef?.commitId ?? null, copyRaw: String(raw._draft ?? ''), commit, branch: nb, agentEpoch: Number(raw.agentEpoch ?? 0) });
  }
  return { plan, work };
}

/** Apply: one transaction per entity (re-read: head + copy unchanged, else refused) + one system.migration audit entry. */
export async function applyMigrationJ(db: Firestore, plan: PlanJ, work: Work[], bucket?: Bucket): Promise<{ applied: number; refused: string[] }> {
  const refused: string[] = [];
  let applied = 0;
  for (const w of work) {
    let external = false;
    if (w.commit) {
      const body = JSON.stringify({ config: w.commit.config, catalog: w.commit.catalog });
      if (body.length > COMMIT_INLINE_MAX) {
        if (!bucket) throw new Error(`${w.ref}: commit body > ${COMMIT_INLINE_MAX} B needs Storage`);
        await bucket.file(`commits/${w.commit.hash.replace(/^sha256:/, '')}.json`).save(Buffer.from(body, 'utf8'), { contentType: 'application/json', resumable: false });
        external = true;
      }
    }
    try {
      await db.runTransaction(async (tx) => {
        const s = await tx.get(db.doc(w.ref));
        if (!s.exists) throw new Error('gone');
        const cur = s.data() as Doc;
        const curHead = ((cur.branch as Doc | undefined)?.head as { commitId?: string } | null | undefined)?.commitId ?? null;
        const curCopy = String((w.e.kind === 'template' ? cur._body : cur._draft) ?? '');
        if (curHead !== w.headId || curCopy !== w.copyRaw) throw new Error(`changed since the plan (head ${curHead ?? 'none'}): a concurrent commit wins — rerun`);
        if (w.commit) tx.create(db.doc(`${w.ref}/commits/${w.commit.commitId}`), encodeCommit(w.commit, external));
        const upd: Doc = { branch: w.branch };
        if (w.commit && w.e.kind === 'deployment') upd.agentEpoch = Number(cur.agentEpoch ?? 0) + 1;
        tx.update(db.doc(w.ref), upd);
      });
      applied++;
    } catch (e) {
      refused.push(`${w.ref}: ${e instanceof Error ? e.message : String(e)}`);
    }
  }
  if (applied) {
    const ref = db.collection('auditLog').doc();
    await ref.create({
      auditId: ref.id,
      ts: new Date().toISOString(),
      op: 'system:migrate-j',
      actor: 'system',
      actorEmail: null,
      action: 'system.migration',
      outcome: 'ok',
      status: 200,
      errorCode: null,
      errorMessage: null,
      targetType: 'system',
      target: 'system/migration-j',
      requestId: null,
      ip: null,
      userAgent: null,
      changesTotal: 0,
      _changes: '[]',
      _data: JSON.stringify({ migration: 'j', dryRun: false, applied, refused, counts: plan.counts, entities: plan.entities.filter((e) => e.action !== 'none'), warnings: plan.warnings.slice(0, 100) }).slice(0, 900_000),
      _actionKeys: auditActionKeys('system.migration'),
    });
  }
  return { applied, refused };
}

export function printPlanJ(plan: PlanJ, log: (s: string) => void = console.log) {
  for (const e of plan.entities) {
    if (e.action === 'none' && !e.notes.length) continue;
    log(`\n● ${e.path} "${e.label}" — ${e.action === 'commit' ? `COMMIT seq ${e.newSeq} (${e.items} uncommitted item(s) on ${e.head ? `seq ${e.head.seq}` : 'no head'})` : e.action === 'strip' ? 'remove branch.uncommitted' : 'unchanged'}`);
    for (const k of e.keys ?? []) log(`    ${k}`);
    for (const v of e.validationErrors ?? []) log(`    validation (reported, not enforced): ${v}`);
    for (const n of e.notes) log(`    note: ${n}`);
  }
  for (const w of plan.warnings) log(`WARN ${w}`);
  const writes = plan.counts.commits + plan.counts.stripped;
  log(`\ncounts: ${JSON.stringify(plan.counts)}\n${writes} change(s)`);
}

async function main() {
  const { arg, flag, init } = await import('./_admin.ts');
  const project = arg('project');
  const apply = flag('apply');
  const report = arg('report');
  if (!project) {
    console.error('usage: tsx scripts/migrate-j.ts --project <id> [--dry-run (default) | --apply] [--report <file.json>]');
    process.exit(2);
  }
  const { db, bucket } = init(project);
  console.log(`migrate-j on ${project} — ${apply ? 'APPLY' : 'dry run (nothing is written)'}`);
  const { plan, work } = await planMigrationJ(db, bucket as never);
  printPlanJ(plan);
  let result: { applied: number; refused: string[] } | null = null;
  if (apply) {
    result = await applyMigrationJ(db, plan, work, bucket as never);
    console.log(result.applied ? `applied ${result.applied} (+ one system.migration audit entry)` : 'nothing to do');
    for (const r of result.refused) console.log(`REFUSED ${r}`);
  }
  if (report) writeFileSync(report, JSON.stringify({ project, apply, counts: plan.counts, warnings: plan.warnings, entities: plan.entities, result }, null, 1));
  if (result?.refused.length) process.exit(3);
}
if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href)
  main().then(
    () => process.exit(0),
    (e) => {
      console.error(e);
      process.exit(1);
    },
  );
