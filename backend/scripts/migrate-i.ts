/**
 * migrate-i — §21 #104 branches + commits (docs-v2/branches-commits.md §8). Whole database, idempotent.
 *   Templates (no `branch` yet): one commit seq 1, kind "migrated", message "Migrated: template version <v> (templates kept
 *     no history before 0.14)", config = the current fields (secret values null) → `branch` { head, origin null, 1, 0 }.
 *   Deployments (no `branch` yet; deleted ones too):
 *     1. the APPLIED revision chain (deployment.head → parentRevId …) → commits with the SAME ids (commitId = revId), seq,
 *        message = the revision summary (else "migrated revision <seq>"), author = the actor (uid → account email / name;
 *        agent origin = the email), config + catalog byte-identical (hash verified), data.migratedFrom { revId, seq }.
 *        Revisions off the chain (superseded / stale proposals) are not commits (they stay readable as legacy history).
 *     2. an OPEN proposal (published / staging / ready) whose parent is the head → one more commit (id = its revId) =
 *        the new deployment head ("… (was proposal seq n, <status>)"); the proposal doc is NOT changed (agents < 0.14 can
 *        still approve it). approved / applying → the deployment is SKIPPED ("an update is running — run again").
 *     3. the draft stays the working copy; branch.uncommitted = items(head → draft).
 *     4. srcTemplate → branch.origin (template, commitId null, templateVersion) + mergeBases/template:<id> (base =
 *        _srcBase, skipped = _srcSkipped 1:1, same key grammar); srcTemplate itself is kept (frozen).
 *     5. agentBranches/{site} for every enrolled site (if missing): head = its reported headRevId (legacy true while the
 *        agent is < 0.14), mergedFrom = the same commit when it is on the deployment branch.
 *     6. never applied and no open proposal → no commit is invented: branch.head null (the GUI says "commit to release").
 *   One `system.migration` audit entry per --apply run that changes something (op system:migrate-i, data.migration 'i').
 *
 * Usage (from backend/):
 *   npx tsx scripts/migrate-i.ts --data-dir <dir> [--dry-run | --apply] [--report <file.json>]
 * Default is the dry run; only --apply writes.
 */
import { writeFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
import type { DocStore as Firestore } from '../src/driver/index.js';
import {
  auditActionKeys,
  branchKey,
  branchKeyDocId,
  commitRef,
  commitStats,
  configHash,
  markSecretValues,
  supportsBranches,
  uncommittedCount,
  ulid,
  type AgentBranchDoc,
  type AgentDoc,
  type BranchState,
  type Commit,
  type CommitAuthor,
  type CommitRef,
  type Config,
  type MergeBase,
  type Proposal,
  type Revision,
} from '@opm/shared';
import { decodeDeployment, decodeRevision, decodeTemplate } from '../src/core/codec.ts';
import { encodeCommit, encodeMergeBase, COMMIT_INLINE_MAX } from '../src/core/branches.ts';
import { normalizeStored } from '../src/core/config.ts';

type Doc = Record<string, unknown>;
export type OpI = { op: 'create' | 'set' | 'update'; path: string; data: Record<string, unknown>; why: string; bytes: number; storage?: { path: string; body: string } };
export interface EntityReport {
  path: string;
  kind: 'template' | 'deployment';
  label: string;
  lines: string[];
  commits: number;
  head: { seq: number; commitId: string; message: string } | null;
  uncommitted: number;
  skipped?: string;
}
export interface PlanI {
  ops: OpI[];
  entities: EntityReport[];
  warnings: string[];
  counts: Record<string, number>;
}
const TEMPLATE_CFG = (t: { variables: Config['variables']; secrets: Config['secrets']; files: Config['files']; sites: Config['sites'] }): Config => ({ variables: t.variables, secrets: t.secrets, files: t.files, sites: t.sites });

export async function planMigrationI(db: Firestore): Promise<PlanI> {
  const plan: PlanI = { ops: [], entities: [], warnings: [], counts: {} };
  const count = (k: string, n = 1) => (plan.counts[k] = (plan.counts[k] ?? 0) + n);
  const accounts = new Map<string, { email: string; name: string | null }>();
  for (const a of (await db.collection('accounts').get()).docs) accounts.set(a.id, { email: String(a.data().email ?? a.id), name: (a.data().name as string | null) ?? null });
  const author = (actor: string | null | undefined, origin?: string): CommitAuthor => {
    if (!actor) return { kind: 'system', email: 'system', uid: null, name: null, siteSlug: null };
    if (origin === 'agent' || actor.includes('@')) return { kind: 'person', email: actor, uid: null, name: null, siteSlug: null };
    const acc = accounts.get(actor);
    return { kind: 'person', email: acc?.email ?? actor, uid: actor, name: acc?.name ?? null, siteSlug: null };
  };
  const push = (o: Omit<OpI, 'bytes'>) => plan.ops.push({ ...o, bytes: JSON.stringify(o.data).length });
  const commitOp = (base: string, c: Commit, why: string) => {
    const body = JSON.stringify({ config: c.config, catalog: c.catalog });
    const external = body.length > COMMIT_INLINE_MAX;
    push({ op: 'create', path: `${base}/commits/${c.commitId}`, data: encodeCommit(c, external), why, ...(external ? { storage: { path: `commits/${c.hash.replace(/^sha256:/, '')}.json`, body } } : {}) });
  };

  // ── templates
  for (const d of (await db.collection('templates').get()).docs) {
    const raw = d.data() as Doc;
    if (raw.branch) continue;
    const t = decodeTemplate(raw);
    const cfg = TEMPLATE_CFG(t);
    cfg.secrets = cfg.secrets.map((s) => ({ ...s, value: null, rotatedAt: null }));
    const c: Commit = {
      commitId: ulid(Date.parse(t.updatedAt) || Date.now()),
      branch: { kind: 'template', templateId: d.id },
      seq: 1,
      parentId: null,
      kind: 'migrated',
      message: `Migrated: template version ${t.version} (templates kept no history before 0.14)`,
      author: author(t.updatedBy),
      createdAt: t.updatedAt,
      source: null,
      revertOf: null,
      resetTo: null,
      rollbackOf: null,
      config: cfg,
      hash: configHash(cfg),
      catalog: null,
      catalogHash: null,
      stats: commitStats(null, cfg),
      data: { migratedFrom: { templateVersion: t.version } },
    };
    const branch: BranchState = { head: commitRef(c), origin: null, commitCount: 1 };
    commitOp(`templates/${d.id}`, c, 'template migrated commit');
    push({ op: 'update', path: `templates/${d.id}`, data: { branch }, why: 'template branch' });
    plan.entities.push({ path: `templates/${d.id}`, kind: 'template', label: t.label, lines: [`commit seq 1 "${c.message}" (${c.stats.added} items)`], commits: 1, head: { seq: 1, commitId: c.commitId, message: c.message }, uncommitted: 0 });
    count('templates');
    count('commits');
  }

  // ── deployments
  for (const d of (await db.collection('deployments').get()).docs) {
    const raw = d.data() as Doc;
    if (raw.branch) continue;
    const dep = decodeDeployment(raw);
    const base = `deployments/${d.id}`;
    const rep: EntityReport = { path: base, kind: 'deployment', label: `${dep.label}${dep.deleted ? ' (deleted)' : ''}`, lines: [], commits: 0, head: null, uncommitted: 0 };
    const [revSnap, propSnap, agentSnap, abSnap] = await Promise.all([db.collection(`${base}/revisions`).get(), db.collection(`${base}/proposals`).get(), db.collection(`${base}/agents`).get(), db.collection(`${base}/agentBranches`).get()]);
    const revs = new Map<string, Revision>(revSnap.docs.map((x) => [x.id, decodeRevision(x.data())]));
    const props = propSnap.docs.map((x) => x.data() as Proposal);
    const running = props.filter((p) => p.status === 'approved' || p.status === 'applying');
    if (running.length) {
      rep.skipped = `an update is running (proposal seq ${running.map((p) => p.seq).join(', ')} ${running.map((p) => p.status).join(', ')}) — run again after it`;
      plan.warnings.push(`${base} "${dep.label}": ${rep.skipped}`);
      plan.entities.push(rep);
      count('deploymentsSkipped');
      continue;
    }
    // 1. the applied chain
    const chain: Revision[] = [];
    let hole: string | null = null;
    for (let id = dep.head?.revId ?? null; id; ) {
      const r = revs.get(id);
      if (!r) {
        hole = id;
        break;
      }
      chain.unshift(r);
      id = r.parentRevId;
    }
    if (hole) {
      rep.skipped = `revision ${hole} of the head chain is missing`;
      plan.warnings.push(`${base} "${dep.label}": ${rep.skipped}`);
      plan.entities.push(rep);
      count('deploymentsSkipped');
      continue;
    }
    const commits: Commit[] = [];
    const mk = (r: Revision, extra = '', status?: Proposal['status']): Commit => {
      const parent = commits.at(-1) ?? null;
      const h = configHash(r.config);
      if (h !== r.hash) plan.warnings.push(`${base}: revision ${r.revId} seq ${r.seq}: stored hash ${r.hash} ≠ recomputed ${h} (kept the recomputed hash for the commit)`);
      if (r.seq !== (parent?.seq ?? 0) + 1) plan.warnings.push(`${base}: revision ${r.revId} has seq ${r.seq}, its commit gets seq ${(parent?.seq ?? 0) + 1}`);
      return {
        commitId: r.revId,
        branch: { kind: 'deployment', deploymentId: d.id },
        seq: (parent?.seq ?? 0) + 1,
        parentId: parent?.commitId ?? null,
        kind: 'migrated',
        message: ((r.summary?.trim() || `migrated revision ${r.seq}`) + extra).slice(0, 2000),
        author: author(r.actor, r.origin),
        createdAt: r.createdAt,
        source: null,
        revertOf: null,
        resetTo: null,
        rollbackOf: r.rollbackOf ? { snapshotId: r.rollbackOf.snapshotId } : null,
        config: r.config,
        hash: h,
        catalog: r.catalog,
        catalogHash: r.catalogHash,
        stats: commitStats(parent?.config ? normalizeStored(parent.config) : null, normalizeStored(r.config), { secretValues: true }),
        data: { migratedFrom: { revId: r.revId, seq: r.seq, ...(status ? { proposalStatus: status } : {}) } },
      };
    };
    for (const r of chain) {
      const c = mk(r, r.origin === 'agent' ? ' (local edit on a site, pre-0.14)' : '');
      commits.push(c);
      rep.lines.push(`commit seq ${c.seq} ← revision ${r.revId} seq ${r.seq} (${r.origin}${r.kind === 'rollback' ? ', rollback' : ''}): "${c.message.split('\n')[0]}"`);
    }
    // 2. the open proposal on top of the head
    const open = props.filter((p) => ['published', 'staging', 'ready'].includes(p.status) && p.parentRevId === (dep.head?.revId ?? null)).sort((a, b) => (a.revId < b.revId ? 1 : -1))[0];
    if (open) {
      const r = revs.get(open.revId);
      if (r) {
        const c = mk(r, ` (was proposal seq ${open.seq}, ${open.status})`, open.status);
        commits.push(c);
        rep.lines.push(`commit seq ${c.seq} ← open proposal ${open.revId} seq ${open.seq} (${open.status}) = the new cloud head (proposal kept for agents < 0.14)`);
        count('openProposalsToHead');
      } else plan.warnings.push(`${base}: open proposal ${open.revId} has no revision`);
    }
    for (const p of props) if (['published', 'staging', 'ready'].includes(p.status) && p !== open) rep.lines.push(`proposal seq ${p.seq} (${p.status}, parent ${p.parentRevId ?? 'none'}) — not on the head: left as is`);
    for (const p of props) if (p.status === 'stale' || p.status === 'superseded') count('proposalsLeftAsHistory');
    for (const c of commits) commitOp(base, c, 'deployment migrated commit');
    const head = commits.at(-1) ?? null;
    // 3. working copy
    const uncommitted = uncommittedCount(head ? normalizeStored(head.config) : null, dep.draft, { secretValues: true });
    // 4. srcTemplate → origin + merge base
    let origin: BranchState['origin'] = null;
    if (dep.srcTemplate) {
      const s = dep.srcTemplate;
      const label = s.snapshot?.label ?? (raw.srcTemplate as { label?: string } | null)?.label ?? s.templateId;
      origin = { branch: { kind: 'template', templateId: s.templateId }, commitId: null, seq: null, label, templateVersion: s.templateVersion };
      const mb: MergeBase = {
        sourceKey: branchKey(origin.branch),
        source: origin.branch,
        sourceCommitId: null,
        sourceSeq: null,
        targetCommitId: head?.commitId ?? null,
        base: markSecretValues(s.baseSnapshot),
        skipped: (s.skipped ?? []).map((k) => ({ key: k.key, theirsHash: k.theirsHash, sourceSeq: null, skippedAt: k.skippedAt, skippedBy: k.skippedBy })),
        label: `template ${label} v${s.baseVersion} (pre-0.14 base)`,
        updatedAt: new Date().toISOString(),
        updatedBy: 'system:migrate-i',
      };
      push({ op: 'set', path: `${base}/mergeBases/${branchKeyDocId(mb.sourceKey)}`, data: encodeMergeBase(mb), why: 'merge base from srcTemplate' });
      rep.lines.push(`origin: template ${label} (${s.templateId}) cloned v${s.templateVersion}; merge base = template v${s.baseVersion} with ${mb.skipped.length} skipped item(s)`);
      count('mergeBases');
    }
    const branch: BranchState = { head: head ? commitRef(head) : null, origin, commitCount: commits.length }; // §21 #105: no uncommitted count (migrate-j commits a differing draft)
    push({ op: 'update', path: base, data: { branch }, why: 'deployment branch' });
    // 5. agent-branch mirrors
    const haveAB = new Set(abSnap.docs.map((x) => x.id));
    for (const a of agentSnap.docs) {
      if (haveAB.has(a.id)) continue;
      const ag = a.data() as AgentDoc;
      const onBranch = commits.find((c) => c.commitId === ag.headRevId);
      const r = ag.headRevId ? revs.get(ag.headRevId) : undefined;
      const hRef: CommitRef | null = onBranch ? commitRef(onBranch) : r ? { commitId: r.revId, seq: r.seq, hash: r.hash, message: r.summary || `revision ${r.seq}`, author: author(r.actor, r.origin), createdAt: r.createdAt, kind: 'legacy' } : null;
      const ab: AgentBranchDoc = { siteSlug: a.id, head: hRef, legacy: !supportsBranches(ag.agentVersion ?? null), mergedFrom: onBranch ? { commitId: onBranch.commitId, seq: onBranch.seq } : null, uncommitted: null, pushedAt: null, agentVersion: ag.agentVersion ?? null };
      push({ op: 'set', path: `${base}/agentBranches/${a.id}`, data: ab as unknown as Record<string, unknown>, why: 'agent branch mirror' });
      const localOpen = props.filter((p) => p.origin === 'agent' && ['published', 'staging', 'ready'].includes(p.status));
      rep.lines.push(`agent branch ${a.id}: head ${hRef ? `seq ${hRef.seq} (${hRef.commitId})` : 'none'}, agent ${ag.agentVersion ?? '?'}${ab.legacy ? ' (needs ≥ 0.14: keeps getting compat proposals)' : ''}${localOpen.length ? `; ${localOpen.length} open local proposal(s) become uncommitted changes when it upgrades` : ''}`);
      count('agentBranches');
    }
    rep.commits = commits.length;
    rep.head = head ? { seq: head.seq, commitId: head.commitId, message: head.message.split('\n')[0]! } : null;
    rep.uncommitted = uncommitted;
    rep.lines.push(`working copy (draftVersion ${dep.draftVersion}): ${uncommitted} uncommitted item(s) vs ${head ? `seq ${head.seq}` : 'no head (commit to release)'}`);
    plan.entities.push(rep);
    count('deployments');
    count('commits', commits.length);
  }
  return plan;
}

/** Apply (Storage bodies first, then batches ≤ 4 MB / 400 ops) + one system.migration audit entry. */
export async function applyPlanI(db: Firestore, plan: PlanI, bucket?: { file: (p: string) => { save: (b: Buffer, o: object) => Promise<unknown> } }): Promise<void> {
  if (!plan.ops.length) return;
  for (const o of plan.ops) if (o.storage) await bucket!.file(o.storage.path).save(Buffer.from(o.storage.body, 'utf8'), { contentType: 'application/json', resumable: false });
  let i = 0;
  while (i < plan.ops.length) {
    const b = db.batch();
    let n = 0;
    let bytes = 0;
    while (i < plan.ops.length && n < 400 && (n === 0 || bytes + plan.ops[i]!.bytes < 4_000_000)) {
      const o = plan.ops[i]!;
      if (o.op === 'create') b.create(db.doc(o.path), o.data);
      else if (o.op === 'set') b.set(db.doc(o.path), o.data);
      else b.update(db.doc(o.path), o.data);
      bytes += o.bytes;
      n++;
      i++;
    }
    await b.commit();
  }
  const ref = db.collection('auditLog').doc();
  const entities = plan.entities.map((e) => ({ ...e, lines: e.lines.slice(0, 60).map((l) => l.slice(0, 300)) }));
  await ref.create({
    auditId: ref.id,
    ts: new Date().toISOString(),
    op: 'system:migrate-i',
    actor: 'system',
    actorEmail: null,
    action: 'system.migration',
    outcome: 'ok',
    status: 200,
    errorCode: null,
    errorMessage: null,
    targetType: 'system',
    target: 'system/migration-i',
    requestId: null,
    ip: null,
    userAgent: null,
    changesTotal: 0,
    _changes: '[]',
    _data: JSON.stringify({ migration: 'i', dryRun: false, changes: plan.ops.length, counts: plan.counts, entities, warnings: plan.warnings.slice(0, 100) }).slice(0, 900_000),
    _actionKeys: auditActionKeys('system.migration'),
  });
}

export function printPlanI(plan: PlanI, log: (s: string) => void = console.log) {
  for (const e of plan.entities) {
    log(`\n● ${e.path} "${e.label}"${e.skipped ? ` — SKIPPED: ${e.skipped}` : ''}`);
    for (const l of e.lines) log(`    ${l}`);
  }
  if (plan.warnings.length) log('');
  for (const w of plan.warnings) log(`WARN ${w}`);
  log(`\ncounts: ${JSON.stringify(plan.counts)}\n${plan.ops.length} write(s)`);
}

async function main() {
  const { arg, flag, init } = await import('./_admin.ts');
  const project = arg('project');
  const apply = flag('apply');
  const report = arg('report');
  if (!project) {
    console.error('usage: tsx scripts/migrate-i.ts --project <id> [--dry-run (default) | --apply] [--report <file.json>]');
    process.exit(2);
  }
  const { db, bucket } = init(project);
  console.log(`migrate-i on ${project} — ${apply ? 'APPLY' : 'dry run (nothing is written)'}`);
  const plan = await planMigrationI(db);
  printPlanI(plan);
  if (report) writeFileSync(report, JSON.stringify({ project, apply, counts: plan.counts, warnings: plan.warnings, entities: plan.entities }, null, 1));
  if (apply) {
    await applyPlanI(db, plan, bucket as never);
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
