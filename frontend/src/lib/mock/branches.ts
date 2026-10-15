/**
 * §21 #104 mock of branches + commits (VITE_MOCK=1): every human BRANCH_CLOUD_API endpoint on the in-memory MockDb, with
 * the same rules as the backend (docs-v2/branches-commits.md §3, §4.5, §7.1) through the shared engine
 * (@opm/shared/branch). Hooked into server.ts like volume-content.ts: `ensureBranches(db)` per request (a lazy
 * "migrate-i": every template / deployment without `branch` gets its migrated commits) and `branchHandlers(deps)` merged
 * into the handler table.
 *
 * Seed scenario (once): template "Hello world" gets a second commit after ACME was created from it (greeting "Hello from
 * OPM 2.0", redis restart on-failure, drops the unused variable support-email both had) → ACME ← Hello world shows clean
 * changes + a removal ("Removed by this merge"); ACME's own presets + pg-password are "only in the target". A conflict
 * appears once ACME commits its own greeting (tests / screenshots do that). Agent branches of ACME: app on 0.14 with a
 * local drift commit (LOG_LEVEL=debug on its first container) + 2 uncommitted server changes; db / lab legacy (< 0.14).
 */
import {
  applyMerge,
  applyRevert,
  branchKey,
  checkCommitMessage,
  commitRef,
  commitSummaryOf,
  commitSummary,
  configHash,
  createMessage,
  diffConfigs,
  makeCommit,
  markSecretValues,
  mergeMissing,
  needsAgentMessage,
  nextSkips,
  planMerge,
  resetPlan,
  revertPlan,
  sourcePicks,
  supportsBranches,
  uncommittedItems,
  secretValueChanges,
  hasSecretValueChanges,
  type ConfigInput,
  type SecretDef,
  type SecretDefInput,
  countMerge,
  isFastForward,
  ulid,
  validateConfig,
  type AgentBranchDoc,
  type AgentBranchView,
  type BranchPoint,
  type BranchRef,
  type BranchState,
  type Commit,
  type CommitAuthor,
  type CommitDetail,
  type CompareRequest,
  type Config,
  type ConfigView,
  type Deployment,
  type ItemizeOptions,
  type MergeBase,
  type MergeItem,
  type MergePlan,
  type MergePreview,
  type MergeRequest,
  type Template,
  type ValidationContext,
  type ValidationIssue,
  type Account,
  type Resource,
  type Action,
  type ErrorCode,
  type RevisionCatalog,
} from '@opm/shared';
import { ago, now, type MockDb } from './db';

const ADMIN = 'uAdmin00000000000001';
const HOUR = 3_600_000;
const DAY = 24 * HOUR;
const empty = (): Config => ({ variables: [], secrets: [], files: [], sites: [] });
const clone = <T>(v: T): T => structuredClone(v);

export interface BranchCtx {
  params: Record<string, string>;
  query: URLSearchParams;
  body: unknown;
  me: Account;
  db: MockDb;
}
export interface BranchDeps {
  err: (code: ErrorCode, message: string, details?: Record<string, unknown>) => Error;
  need: (c: BranchCtx, r: Resource, a: Action) => void;
  dep: (c: BranchCtx, id: string) => Deployment;
  needDep: (c: BranchCtx, d: Deployment, r: Resource, a: Action) => void;
  canDep: (c: BranchCtx, d: Deployment, r: Resource, a: Action) => boolean;
  can: (c: BranchCtx, r: Resource, a: Action) => boolean;
  cloudActivity: (c: BranchCtx, d: Deployment, type: string, summary: string, data: Record<string, unknown>) => void;
  vctx: (db: MockDb, mode: ValidationContext['mode'], deploymentId: string) => ValidationContext;
  normalize: (db: MockDb, c: Config) => Config;
  configView: (c: Config) => ConfigView;
  buildCatalog: (db: MockDb, c: Config) => RevisionCatalog;
  genSecret: (charset: 'alnum' | 'hex' | 'base64url', length: number) => string;
}

// ───────────────────────────────────────────── storage
type Store = { commits: Record<string, Commit[]>; mergeBases: Record<string, Record<string, MergeBase>>; agentBranches: Record<string, Record<string, AgentBranchDoc>>; seeded: boolean; migratedJ?: boolean };
function store(db: MockDb): Store {
  const x = db as MockDb & { br?: Store };
  return (x.br ??= { commits: {}, mergeBases: {}, agentBranches: {}, seeded: false });
}
/** §21 #105 migrate-j message */
export const AUTO_COMMIT_MESSAGE = 'Uncommitted changes from before 0.14.1 (auto-committed)';
const commitsOf = (db: MockDb, b: BranchRef) => (store(db).commits[branchKey(b)] ??= []);
const headOf = (db: MockDb, b: BranchRef): Commit | null => commitsOf(db, b).at(-1) ?? null;
const tplRef = (id: string): BranchRef => ({ kind: 'template', templateId: id });
const depRef = (id: string): BranchRef => ({ kind: 'deployment', deploymentId: id });
const agentRef = (id: string, site: string): BranchRef => ({ kind: 'agent', deploymentId: id, siteSlug: site });
const tplConfig = (t: Template): Config => ({ variables: t.variables, secrets: t.secrets, files: t.files, sites: t.sites });
const system: CommitAuthor = { kind: 'system', email: 'system', uid: null, name: null, siteSlug: null };
function authorOf(db: MockDb, uid: string | null): CommitAuthor {
  const a = uid ? db.accounts[uid] : null;
  return a ? { kind: 'person', email: a.email, uid: a.uid, name: a.name ?? null, siteSlug: null } : system;
}
const scopeOpts = (src: BranchRef, tgt: BranchRef): ItemizeOptions => ({
  secretValues: src.kind !== 'template' && tgt.kind !== 'template',
  scope: src.kind === 'agent' ? src.siteSlug : tgt.kind === 'agent' ? tgt.siteSlug : null,
});
function setState(entity: { branch?: BranchState }, head: Commit | null, count: number, _working: Config, origin: BranchState['origin'] | undefined, _secretValues: boolean) {
  void _working;
  void _secretValues;
  // §21 #105: no `uncommitted` (the cloud keeps no working copy)
  entity.branch = {
    head: head ? commitRef(head) : null,
    origin: origin !== undefined ? origin : (entity.branch?.origin ?? null),
    commitCount: count,
  };
}
export function refreshTemplate(db: MockDb, t: Template) {
  const list = commitsOf(db, tplRef(t.templateId));
  setState(t, list.at(-1) ?? null, list.length, tplConfig(t), undefined, false);
}
export function refreshDeployment(db: MockDb, d: Deployment) {
  const list = commitsOf(db, depRef(d.deploymentId));
  setState(d, list.at(-1) ?? null, list.length, d.draft, undefined, true);
}

// ───────────────────────────────────────────── lazy migration (= migrate-i) + seed scenario
export function ensureBranches(db: MockDb, deps: Pick<BranchDeps, 'buildCatalog'>) {
  const st = store(db);
  for (const t of Object.values(db.templates)) {
    if (t.branch) continue;
    const c = makeCommit({ commitId: ulid(Date.parse(t.updatedAt) || Date.now()), branch: tplRef(t.templateId), parent: null, kind: 'migrated', message: `Migrated: template version ${t.version} (templates kept no history before 0.14)`, author: authorOf(db, t.updatedBy), createdAt: t.updatedAt, config: clone(tplConfig(t)), data: { migratedFrom: { templateVersion: t.version } } });
    st.commits[branchKey(tplRef(t.templateId))] = [c];
    setState(t, c, 1, tplConfig(t), null, false);
  }
  for (const d of Object.values(db.deployments)) {
    if (d.branch) continue;
    const ref = depRef(d.deploymentId);
    const list: Commit[] = [];
    const revs = db.revisions[d.deploymentId] ?? [];
    // the applied chain from the legacy head
    const chain = [];
    let cur = d.head ? revs.find((r) => r.revId === d.head!.revId) : undefined;
    while (cur) {
      chain.unshift(cur);
      const p = cur.parentRevId;
      cur = p ? revs.find((r) => r.revId === p) : undefined;
    }
    for (const r of chain) {
      const parent = list.at(-1) ?? null;
      list.push({ ...makeCommit({ commitId: r.revId, branch: ref, parent, kind: 'migrated', message: r.summary?.trim() || `migrated revision ${r.seq}`, author: r.origin === 'cloud' ? authorOf(db, r.actor) : { kind: 'person', email: r.actor, uid: null, name: null, siteSlug: null }, createdAt: r.createdAt, config: clone(r.config), catalog: r.catalog, data: { migratedFrom: { revId: r.revId, seq: r.seq } } }), seq: r.seq });
    }
    st.commits[branchKey(ref)] = list;
    const l = d.srcTemplate;
    const origin: BranchState['origin'] = l ? { branch: tplRef(l.templateId), commitId: null, seq: null, label: l.snapshot?.label ?? 'template', templateVersion: l.templateVersion } : null;
    if (l) {
      (st.mergeBases[branchKey(ref)] ??= {})[branchKey(tplRef(l.templateId))] = {
        sourceKey: branchKey(tplRef(l.templateId)),
        source: tplRef(l.templateId),
        sourceCommitId: null,
        sourceSeq: null,
        targetCommitId: list.at(-1)?.commitId ?? null,
        base: clone(l.baseSnapshot ?? tplConfig(l.snapshot)),
        skipped: (l.skipped ?? []).map((s) => ({ key: s.key, theirsHash: s.theirsHash, sourceSeq: null, skippedAt: s.skippedAt, skippedBy: s.skippedBy })),
        label: `template ${l.snapshot?.label ?? ''} v${l.baseVersion ?? l.templateVersion} (pre-0.14 base)`,
        updatedAt: l.lastSyncedAt ?? l.copiedAt,
        updatedBy: l.lastSyncedBy ?? 'system',
      };
    }
    setState(d, list.at(-1) ?? null, list.length, d.draft, origin, true);
    // agent branches (legacy mirrors) for every enrolled site
    const ab = (st.agentBranches[d.deploymentId] ??= {});
    for (const [site, a] of Object.entries(db.agents[d.deploymentId] ?? {})) {
      if (ab[site]) continue;
      const h = list.find((c) => c.commitId === a.headRevId) ?? null;
      ab[site] = { siteSlug: site, head: h ? commitRef(h) : null, legacy: !supportsBranches(a.agentVersion), mergedFrom: h ? { commitId: h.commitId, seq: h.seq } : null, uncommitted: null, pushedAt: null, agentVersion: a.agentVersion ?? null };
    }
  }
  if (!st.seeded) {
    st.seeded = true;
    seedScenario(db, deps);
  }
  if (!st.migratedJ) {
    st.migratedJ = true;
    migrateJ(db, deps);
  }
}

/** §21 #105 = backend/scripts/migrate-j.ts: a config copy that differs from its head becomes an auto commit */
function migrateJ(db: MockDb, deps: Pick<BranchDeps, 'buildCatalog'>) {
  for (const t of Object.values(db.templates)) {
    const ref = tplRef(t.templateId);
    const head = headOf(db, ref);
    const cfg = clone(tplConfig(t));
    const items = uncommittedItems(head?.config ?? null, cfg).length;
    if (!items) continue;
    const c = makeCommit({ commitId: ulid(), branch: ref, parent: head, kind: 'change', message: AUTO_COMMIT_MESSAGE, author: system, createdAt: now(), config: cfg, data: { autoCommitted: { from: 'cloud-working-copy', items } } });
    commitsOf(db, ref).push(c);
    refreshTemplate(db, t);
  }
  for (const d of Object.values(db.deployments)) {
    const ref = depRef(d.deploymentId);
    const head = headOf(db, ref);
    const cfg = clone(d.draft);
    const items = uncommittedItems(head?.config ?? null, cfg, { secretValues: true }).length;
    if (!items || (!head && !cfg.sites.length && !cfg.variables.length)) continue;
    const c = makeCommit({ commitId: ulid(), branch: ref, parent: head, kind: 'change', message: AUTO_COMMIT_MESSAGE, author: system, createdAt: now(), config: cfg, catalog: deps.buildCatalog(db, cfg), data: { autoCommitted: { from: 'cloud-working-copy', items } } });
    commitsOf(db, ref).push(c);
    d.agentEpoch += 1;
    refreshDeployment(db, d);
  }
}

function seedScenario(db: MockDb, deps: Pick<BranchDeps, 'buildCatalog'>) {
  const st = store(db);
  const tpl = db.templates['TplHelloWorld0000001'];
  const acme = db.deployments['DepAcmePlant00000001'];
  if (!tpl || !acme) return;
  const tref = tplRef(tpl.templateId);
  const t1 = headOf(db, tref)!;
  // an unused variable both had at the clone; the template drops it later (→ "Removed by this merge" on ACME ← Hello world)
  const support = { variableSlug: 'support-email', label: 'Support e-mail', description: 'Shown nowhere any more.', type: 'string' as const, value: 'support@example.com', required: false, options: null, validation: null };
  const d0 = headOf(db, depRef(acme.deploymentId));
  for (const cfg of [t1.config, tpl as Config, acme.draft, ...(d0 ? [d0.config] : [])]) if (!cfg.variables.some((v) => v.variableSlug === support.variableSlug)) cfg.variables.push(clone(support));
  refreshDeployment(db, acme);
  // ACME was created from Hello world v3 = t1: origin + merge base point at t1
  acme.branch!.origin = { branch: tref, commitId: t1.commitId, seq: t1.seq, label: tpl.label };
  const mb = st.mergeBases[branchKey(depRef(acme.deploymentId))]?.[branchKey(tref)];
  if (mb) Object.assign(mb, { sourceCommitId: t1.commitId, sourceSeq: t1.seq, base: clone(t1.config), label: `${tpl.label}@${t1.seq}` });
  // the template moved on after ACME was cloned
  const next = clone(t1.config);
  next.variables = next.variables.filter((v) => v.variableSlug !== support.variableSlug);
  const greeting = next.variables.find((v) => v.variableSlug === 'greeting');
  if (greeting) greeting.value = 'Hello from OPM 2.0';
  const redis = next.sites.find((s) => s.siteSlug === 'db')?.config.containers.find((c) => c.containerSlug === 'redis');
  if (redis) redis.restart = 'on-failure';
  const t2 = makeCommit({ commitId: ulid(Date.now() - DAY), branch: tref, parent: t1, kind: 'change', message: 'Greeting for 2.0; redis restarts on failure; drop support-email\n\nsupport-email was not used anywhere any more.', author: authorOf(db, ADMIN), createdAt: ago(DAY), config: next });
  commitsOf(db, tref).push(t2);
  Object.assign(tpl, clone(next)); // seed: the working copy = the new head (version unchanged: tests count versions)
  refreshTemplate(db, tpl);
  // agent branches of ACME: app on 0.14 with a local drift commit
  const dref = depRef(acme.deploymentId);
  const d1 = headOf(db, dref);
  const ab = (st.agentBranches[acme.deploymentId] ??= {});
  if (d1) {
    const aref = agentRef(acme.deploymentId, 'app');
    const a1: Commit = { ...clone(d1), branch: aref };
    const cfg = clone(d1.config);
    const app = cfg.sites.find((s) => s.siteSlug === 'app');
    const c0 = app?.config.containers[0];
    if (c0) c0.env = [...c0.env.filter((e) => e.name !== 'LOG_LEVEL'), { name: 'LOG_LEVEL', value: 'debug', secretRef: null }];
    const a2 = makeCommit({ commitId: ulid(Date.now() - 5 * HOUR), branch: aref, parent: a1, kind: 'drift', message: `Hotfix on site: LOG_LEVEL=debug on ${c0?.containerSlug ?? 'the api'}\n\nSet by hand with docker run while chasing a timeout; recorded from drift.`, author: { kind: 'person', email: 'ops@example.com', uid: null, name: null, siteSlug: 'app' }, createdAt: ago(5 * HOUR), config: cfg, catalog: d1.catalog ?? deps.buildCatalog(db, cfg), scope: 'app' });
    st.commits[branchKey(aref)] = [a1, a2];
    ab.app = { siteSlug: 'app', head: commitRef(a2), legacy: false, mergedFrom: { commitId: d1.commitId, seq: d1.seq }, uncommitted: { working: 0, drift: 2, notes: 1, scannedAt: ago(4 * 60_000) }, pushedAt: ago(5 * HOUR), agentVersion: '0.14.0-10091200' };
  }
}

// ───────────────────────────────────────────── handlers
export function branchHandlers(x: BranchDeps): Record<string, (c: BranchCtx) => unknown> {
  const gone = (what: string) => () => {
    throw x.err('gone', `${what} was replaced by branches (§21 #104)`, { hint: 'branches: use Commit / Compare' });
  };
  const invalid = (issues: ValidationIssue[], message = 'validation failed', extra: Record<string, unknown> = {}) => x.err('invalid', message, { errors: issues, ...extra });
  const issue = (path: string, code: string, message: string): ValidationIssue => ({ path, code, message, severity: 'error' });
  const body = <T>(c: BranchCtx) => (c.body ?? {}) as T;

  interface Info {
    ref: BranchRef;
    label: string;
    head: Commit | null;
    commits: Commit[];
    working: Config | null;
    canWrite: boolean;
    canSecretsRead: boolean;
    canSecretsWrite: boolean;
    tpl?: Template;
    dep?: Deployment;
  }
  function info(c: BranchCtx, ref: BranchRef | undefined | null): Info {
    if (!ref || typeof ref !== 'object') throw invalid([issue('branch', 'V_REQUIRED', 'a branch is required')]);
    if (ref.kind === 'template') {
      x.need(c, 'templates', 'read');
      const t = c.db.templates[ref.templateId];
      if (!t) throw x.err('not-found', 'template not found');
      const commits = commitsOf(c.db, ref);
      return { ref, label: t.label, head: commits.at(-1) ?? null, commits, working: tplConfig(t), canWrite: x.can(c, 'templates', 'write'), canSecretsRead: true, canSecretsWrite: true, tpl: t };
    }
    const d = x.dep(c, ref.deploymentId);
    x.needDep(c, d, 'deployment.config', 'read');
    const commits = commitsOf(c.db, ref);
    if (ref.kind === 'deployment') return { ref, label: d.label, head: commits.at(-1) ?? null, commits, working: d.draft, canWrite: x.canDep(c, d, 'deployment.config', 'publish'), canSecretsRead: x.canDep(c, d, 'deployment.secrets', 'read'), canSecretsWrite: x.canDep(c, d, 'deployment.secrets', 'write'), dep: d };
    if (!store(c.db).agentBranches[d.deploymentId]?.[ref.siteSlug]) throw x.err('not-found', `no agent branch for site ${ref.siteSlug}`);
    return { ref, label: `${d.label} · ${ref.siteSlug}`, head: commits.at(-1) ?? null, commits, working: null, canWrite: false, canSecretsRead: x.canDep(c, d, 'deployment.secrets', 'read'), canSecretsWrite: false, dep: d };
  }
  function at(i: Info, p: BranchPoint['at']): { config: Config; commit: Commit | null } {
    if (p === 'working') throw invalid([issue('source.at', 'V_ENUM', 'there is no working copy in the cloud (§21 #105): the browser compares its own pending changes')]);
    if (p === 'head' || !p) {
      if (i.head) return { config: i.head.config, commit: i.head };
      if (i.working) return { config: i.working, commit: null };
      return { config: empty(), commit: null };
    }
    const cm = i.commits.find((k) => k.commitId === p.commitId);
    if (!cm) throw x.err('not-found', `commit ${p.commitId} not found on ${i.label}`);
    return { config: cm.config, commit: cm };
  }

  function base(c: BranchCtx, tgt: Info, src: Info): { config: Config | null; kind: 'record' | 'origin' | 'none'; sourceCommitId: string | null; label: string; skipped: MergeBase['skipped'] } {
    const st = store(c.db);
    const tk = branchKey(tgt.ref);
    const sk = branchKey(src.ref);
    const rec = st.mergeBases[tk]?.[sk];
    if (rec) return { config: rec.base, kind: 'record', sourceCommitId: rec.sourceCommitId, label: rec.label, skipped: rec.skipped };
    const torig = (tgt.tpl ?? tgt.dep)?.branch?.origin;
    if (torig && branchKey(torig.branch) === sk && torig.commitId) {
      const cm = src.commits.find((k) => k.commitId === torig.commitId);
      if (cm) return { config: cm.config, kind: 'origin', sourceCommitId: cm.commitId, label: `${src.label}@${cm.seq} (origin)`, skipped: [] };
    }
    const sorig = (src.tpl ?? src.dep)?.branch?.origin;
    if (src.ref.kind !== 'agent' && sorig && branchKey(sorig.branch) === tk && sorig.commitId) {
      const cm = tgt.commits.find((k) => k.commitId === sorig.commitId);
      if (cm) return { config: cm.config, kind: 'origin', sourceCommitId: null, label: `${tgt.label}@${cm.seq} (origin of ${src.label})`, skipped: [] };
    }
    // agent branch ↔ its deployment: the newest agent commit that is (or came from) a deployment commit
    const [ag, dp] = src.ref.kind === 'agent' ? [src, tgt] : tgt.ref.kind === 'agent' ? [tgt, src] : [null, null];
    if (ag && dp && dp.ref.kind === 'deployment' && dp.ref.deploymentId === (ag.ref as { deploymentId: string }).deploymentId) {
      for (const k of [...ag.commits].reverse()) {
        const id = k.source && branchKey(k.source.branch) === branchKey(dp.ref) ? k.source.commitId : k.commitId;
        const dc = dp.commits.find((y) => y.commitId === id);
        if (dc) return { config: dc.config, kind: 'origin', sourceCommitId: ag === src ? k.commitId : dc.commitId, label: `${dp.label}@${dc.seq} (last shared commit)`, skipped: [] };
      }
    }
    return { config: null, kind: 'none', sourceCommitId: null, label: 'no common history', skipped: [] };
  }

  function plan(c: BranchCtx, req: CompareRequest): MergePlan {
    const src = info(c, req.source?.branch);
    const s = at(src, req.source.at);
    const tp = req.target && 'branch' in req.target ? (req.target as BranchPoint) : null;
    const tref = (tp ? tp.branch : req.target) as BranchRef;
    const tgt = info(c, tref);
    const diffOnly = !!req.diffOnly || !!tp || !tgt.canWrite;
    const t = tp ? at(tgt, tp.at) : at(tgt, 'head');
    const opts = scopeOpts(src.ref, tgt.ref);
    const b = diffOnly && tp ? { config: null, kind: 'none' as const, sourceCommitId: null, label: 'two points compared', skipped: [] } : base(c, tgt, src);
    const items = planMerge(b.config, s.config, t.config, { ...opts, skipped: b.skipped });
    for (const it of items) {
      if (it.key.endsWith('/value') && it.section === 'secrets' && !(src.canSecretsRead && tgt.canSecretsWrite)) it.locked = 'you need deployment.secrets.read on the source and deployment.secrets.write on the target';
    }
    const uc = 0; // §21 #105: no working copy in the cloud
    return {
      source: { point: req.source, label: src.label, commit: s.commit ? commitRef(s.commit) : null },
      target: { branch: tgt.ref, label: tgt.label, head: t.commit ? commitRef(t.commit) : null, workingClean: uc === 0, uncommitted: uc },
      base: { kind: b.kind, sourceCommitId: b.sourceCommitId, label: b.label },
      items,
      counts: countMerge(items),
      fastForward: isFastForward(items),
      diffOnly,
    };
  }

  /** preview + merge share this: the result, validation, plan pieces */
  function run(c: BranchCtx, req: MergeRequest, write: boolean) {
    const src = info(c, req.source?.branch);
    const tgt = info(c, req.target);
    if (write && !tgt.canWrite) throw x.err('forbidden', tgt.ref.kind === 'agent' ? 'agent branches are written only by their agent' : `you need ${tgt.ref.kind === 'template' ? 'templates.write' : 'deployment.config.publish'}`);
    if (tgt.ref.kind === 'agent') throw x.err('forbidden', 'agent branches are written only by their agent');
    const s = at(src, req.source.at);
    const head = tgt.head;
    if ((req.targetHead ?? null) !== (head?.commitId ?? null)) throw x.err('conflict', `${tgt.label} moved (head ${head ? `seq ${head.seq}` : 'none'}) — refresh`, { reason: 'stale', head: head ? commitRef(head) : null });
    if (req.source.at === 'head' && (req.sourceCommit ?? null) !== (s.commit?.commitId ?? null)) throw x.err('conflict', `${src.label} has a newer commit — refresh`, { reason: 'source-changed' });
    const b = base(c, tgt, src);
    const opts = scopeOpts(src.ref, tgt.ref);
    const r = applyMerge(b.config, s.config, clone(head?.config ?? tgt.working ?? empty()), req.choices ?? [], { ...opts, skipped: b.skipped, values: (req.values ?? {}) as Record<string, never> });
    if (r.taken.some((k) => k.startsWith('secrets/') && k.endsWith('/value')) && !(src.canSecretsRead && tgt.canSecretsWrite)) throw x.err('forbidden', 'secret values need deployment.secrets.read on the source and deployment.secrets.write on the target');
    let config = x.normalize(c.db, r.config);
    if (tgt.ref.kind === 'deployment') {
      const t = now();
      const hd = head?.config.secrets ?? [];
      for (const sx of config.secrets) if (!hd.some((y) => y.secretSlug === sx.secretSlug) && sx.generate && !sx.value) Object.assign(sx, { value: x.genSecret(sx.generate.charset, sx.generate.length), rotatedAt: t });
    } else config = { ...config, secrets: config.secrets.map((sx) => ({ ...sx, value: null, rotatedAt: null })) };
    const mode = tgt.ref.kind === 'template' ? 'template' : 'publish';
    const v = validateConfig(config, x.vctx(c.db, mode, tgt.ref.kind === 'template' ? 'template' : tgt.dep!.deploymentId));
    return { src, tgt, s, b, r, config, v, head };
  }
  const agentWarnings = (c: BranchCtx, d: Deployment) =>
    Object.entries(c.db.agents[d.deploymentId] ?? {})
      .filter(([, a]) => !supportsBranches(store(c.db).agentBranches[d.deploymentId]?.[a.siteSlug]?.agentVersion ?? a.agentVersion))
      .map(([slug, a]) => needsAgentMessage(slug, a.agentVersion ?? null));

  function commitTo(c: BranchCtx, i: Info, config: Config, kind: Commit['kind'], message: string, extra: Partial<Parameters<typeof makeCommit>[0]> = {}): Commit {
    const parent = i.head;
    const cm = makeCommit({ commitId: ulid(), branch: i.ref, parent, kind, message, author: authorOf(c.db, c.me.uid), createdAt: now(), config: clone(config), catalog: i.ref.kind === 'deployment' ? x.buildCatalog(c.db, config) : null, ...extra });
    commitsOf(c.db, i.ref).push(cm);
    if (i.tpl) {
      Object.assign(i.tpl, clone(config), { version: i.tpl.version + 1, updatedAt: cm.createdAt, updatedBy: c.me.uid });
      refreshTemplate(c.db, i.tpl);
    }
    if (i.dep) {
      i.dep.draft = clone(config);
      i.dep.draftVersion += 1;
      i.dep.agentEpoch += 1;
      i.dep.updatedAt = cm.createdAt;
      i.dep.updatedBy = c.me.uid;
      refreshDeployment(c.db, i.dep);
    }
    return cm;
  }
  const checkMsg = (m: unknown) => {
    const iss = checkCommitMessage(m);
    if (iss.length) throw invalid(iss, iss[0]!.message);
    return String(m).trim();
  };
  const detail = (c: BranchCtx, list: Commit[], id: string, secretValues: boolean, scope: string | null = null): CommitDetail => {
    const i = list.findIndex((k) => k.commitId === id);
    if (i < 0) throw x.err('not-found', 'commit not found');
    const cm = list[i]!;
    const parent = list[i - 1] ?? null;
    return { commit: { ...cm, config: x.configView(cm.config) }, changes: uncommittedItems(parent?.config ?? null, cm.config, { secretValues, scope }) };
  };
  const pageOf = <T>(c: BranchCtx, list: T[]) => {
    const off = Number(c.query.get('cursor') ?? 0) || 0;
    const limit = Math.max(1, Number(c.query.get('limit') ?? 50) || 50);
    return { items: list.slice(off, off + limit), cursor: off + limit < list.length ? String(off + limit) : null };
  };
  const listCommits = (c: BranchCtx, list: Commit[]) => pageOf(c, [...list].reverse().map(commitSummaryOf));

  function agentViews(c: BranchCtx, d: Deployment): AgentBranchView[] {
    const st = store(c.db);
    const dhead = headOf(c.db, depRef(d.deploymentId));
    const dcommits = commitsOf(c.db, depRef(d.deploymentId));
    return Object.values(st.agentBranches[d.deploymentId] ?? {})
      .sort((a, b) => a.siteSlug.localeCompare(b.siteSlug))
      .map((doc) => {
        const ac = commitsOf(c.db, agentRef(d.deploymentId, doc.siteSlug));
        const merged = doc.mergedFrom ? dcommits.find((k) => k.commitId === doc.mergedFrom!.commitId) : null;
        const behind = dhead && merged ? Math.max(0, dhead.seq - merged.seq) : dhead ? dhead.seq : 0;
        const at = merged ? ac.findIndex((k) => k.commitId === merged.commitId || k.source?.commitId === merged.commitId) : -1;
        const ahead = at >= 0 ? ac.slice(at + 1).filter((k) => !['merge', 'cherry-pick', 'migrated', 'create'].includes(k.kind)).length : 0;
        const ahd = ac.at(-1);
        const differs = ahd && dhead ? planMerge(null, ahd.config, dhead.config, { secretValues: true, scope: doc.siteSlug }).length : 0;
        const ver = doc.agentVersion ?? c.db.agents[d.deploymentId]?.[doc.siteSlug]?.agentVersion ?? null;
        return { ...doc, head: ahd ? commitRef(ahd) : doc.head, agentVersion: ver, behind, ahead, differs, needsAgent014: !supportsBranches(ver) };
      });
  }

  const goneWorking = () => {
    throw x.err('gone', 'there is no working copy any more (§21 #105): pending changes live in the browser and are discarded there', { hint: 'commit: POST …/commits { message, baseCommitId, config }' });
  };
  /**
   * §21 #105 commit body { message, baseCommitId, config: ConfigInput } → the resulting config from the base head + the
   * browser changes (409 stale when the head moved; secrets: plainValue absent = keep the head value, null = clear,
   * string = set, generateNow = generate; templates hold no values).
   */
  function commitBody(c: BranchCtx, i: Info) {
    const b = body<{ message?: unknown; baseCommitId?: string | null; config?: ConfigInput; ifVersion?: unknown; ifRevision?: unknown }>(c);
    const message = checkMsg(b.message);
    if (!b.config || typeof b.config !== 'object' || !Array.isArray(b.config.secrets)) throw invalid([issue('config', 'V_REQUIRED', 'config (the resulting config) is required (§21 #105)')]);
    if (!('baseCommitId' in b)) throw invalid([issue('baseCommitId', 'V_REQUIRED', 'baseCommitId (the head the changes were made on) is required')]);
    const head = i.head;
    if ((b.baseCommitId ?? null) !== (head?.commitId ?? null)) throw x.err('stale', `${i.label} moved (head is now commit ${head?.seq ?? 0}) — rebase your changes`, { reason: 'head-moved', head: head ? commitRef(head) : null });
    const before = head?.config.secrets ?? [];
    const t = now();
    const generated: string[] = [];
    const secrets = b.config.secrets.map((s) => {
      const { plainValue, generateNow, ...def } = s as SecretDefInput;
      const old = before.find((y) => y.secretSlug === s.secretSlug);
      let value: string | null = old?.value ?? null;
      if (i.ref.kind === 'template') {
        if (typeof plainValue === 'string') throw invalid([issue(`secrets[${s.secretSlug}].plainValue`, 'V_TEMPLATE_SECRET', 'templates never hold secret values')]);
        value = null;
      } else if (generateNow) {
        if (!def.generate) throw invalid([issue(`secrets[${s.secretSlug}].generateNow`, 'V_REQUIRED', 'generateNow needs a generate rule')]);
        value = x.genSecret(def.generate.charset, def.generate.length);
        generated.push(s.secretSlug);
      } else if (plainValue !== undefined) value = plainValue === '' ? null : plainValue;
      else if (!old && def.generate) {
        value = x.genSecret(def.generate.charset, def.generate.length);
        generated.push(s.secretSlug);
      }
      return { ...def, value, rotatedAt: value !== (old?.value ?? null) ? t : (old?.rotatedAt ?? null) } as SecretDef;
    });
    const config = x.normalize(c.db, { variables: clone(b.config.variables), secrets, files: clone(b.config.files), sites: clone(b.config.sites) });
    const secretValues = secretValueChanges(before, config.secrets, generated);
    if (head && uncommittedItems(head.config, config, { secretValues: i.ref.kind !== 'template' }).length === 0) throw invalid([issue('', 'V_NO_CHANGE', 'nothing to commit: the result equals the head')], 'nothing to commit');
    const secretsChanged = JSON.stringify(head?.config.secrets ?? []) !== JSON.stringify(config.secrets);
    return { message, config, secretValues, secretsChanged };
  }

  /** create-from (templates + deployments): the config of the source point, with an origin */
  function fromPoint(c: BranchCtx, p: BranchPoint | null | undefined) {
    if (!p) return null;
    const i = info(c, p.branch);
    const a = at(i, p.at === 'working' ? 'head' : p.at);
    return { info: i, config: clone(a.config), commit: a.commit };
  }

  return {
    // templates
    listTemplateCommits: (c) => listCommits(c, info(c, tplRef(c.params.templateId!)).commits),
    getTemplateCommit: (c) => detail(c, info(c, tplRef(c.params.templateId!)).commits, c.params.commitId!, false),
    commitTemplate: (c) => {
      const i = info(c, tplRef(c.params.templateId!));
      if (!i.canWrite) throw x.err('forbidden', 'you need templates.write');
      const { message, config } = commitBody(c, i);
      const v = validateConfig(config, x.vctx(c.db, 'template', 'template'));
      if (!v.ok) throw invalid(v.errors);
      return commitTo(c, i, config, 'change', message);
    },
    discardTemplateWorking: goneWorking,
    // deployments
    listDeploymentCommits: (c) => listCommits(c, info(c, depRef(c.params.deploymentId!)).commits),
    getDeploymentCommit: (c) => detail(c, info(c, depRef(c.params.deploymentId!)).commits, c.params.commitId!, true),
    commitDeployment: (c) => {
      const i = info(c, depRef(c.params.deploymentId!));
      if (!i.canWrite) throw x.err('forbidden', 'a deployment commit is a release: you need deployment.config.publish');
      const { message, config, secretValues, secretsChanged } = commitBody(c, i);
      if (secretsChanged && !i.canSecretsWrite) throw x.err('forbidden', 'secret changes need deployment.secrets.write');
      const v = validateConfig(config, x.vctx(c.db, 'publish', i.dep!.deploymentId));
      if (!v.ok) throw invalid(v.errors, 'the change set has commit blockers');
      const cm = commitTo(c, i, config, 'change', message, { data: hasSecretValueChanges(secretValues) ? { secretValues } : {} });
      x.cloudActivity(c, i.dep!, 'branch.committed', `Commit ${cm.seq}: ${commitSummary(message)}`, { branch: i.ref, commitId: cm.commitId, seq: cm.seq, message: commitSummary(message), ...(hasSecretValueChanges(secretValues) ? { secretValues } : {}) });
      return cm;
    },
    discardDeploymentWorking: goneWorking,
    listAgentBranches: (c) => {
      const d = x.dep(c, c.params.deploymentId!);
      x.needDep(c, d, 'deployment.config', 'read');
      return { items: agentViews(c, d) };
    },
    listAgentBranchCommits: (c) => listCommits(c, info(c, agentRef(c.params.deploymentId!, c.params.siteSlug!)).commits),
    getAgentBranchCommit: (c) => detail(c, info(c, agentRef(c.params.deploymentId!, c.params.siteSlug!)).commits, c.params.commitId!, true, c.params.siteSlug!),
    // compare / merge
    compareBranches: (c) => plan(c, body<CompareRequest>(c)),
    previewMerge: (c) => {
      const req = body<MergeRequest>(c);
      const { r, config, v, head, tgt, src } = run(c, req, false);
      const out: MergePreview = {
        result: x.configView(config),
        changes: diffConfigs(head?.config ?? null, config, (s) => s, (slug) => (head?.config.secrets.find((y) => y.secretSlug === slug)?.value ?? null) !== (config.secrets.find((y) => y.secretSlug === slug)?.value ?? null)),
        validation: { errors: v.errors, warnings: v.list.filter((y) => y.severity === 'warning') },
        unresolved: r.unresolved,
        missing: mergeMissing(config),
        choiceErrors: r.errors,
        kind: r.kind,
        taken: r.taken.length,
        skipped: r.notTaken.length,
        warnings: tgt.dep ? agentWarnings(c, tgt.dep) : [],
      };
      void src;
      return out;
    },
    merge: (c) => {
      const req = body<MergeRequest & { message: string }>(c);
      const message = checkMsg(req.message);
      const { r, config, v, tgt, src, s, b } = run(c, req, true);
      if (r.errors.length) throw invalid(r.errors.map((e) => issue(e.key, 'V_MERGE_CHOICE', e.message)), r.errors[0]!.message);
      if (r.unresolved.length) throw invalid(r.unresolved.map((k) => issue(k, 'V_MERGE_UNRESOLVED', 'choose keep target / take source for this conflict')), `${r.unresolved.length} conflict${r.unresolved.length === 1 ? '' : 's'} without a choice`);
      if (!r.taken.length) throw invalid([issue('choices', 'V_NO_CHANGE', 'pick at least one change')], 'nothing picked');
      if (!v.ok) throw invalid(v.errors, 'the result has errors');
      const cm = commitTo(c, tgt, config, r.kind, message, { source: { branch: src.ref, commitId: s.commit?.commitId ?? null, seq: s.commit?.seq ?? null, label: src.label, ...sourcePicks(r.taken, r.kind), skipped: r.notTaken.length } });
      const st = store(c.db);
      const prev = st.mergeBases[branchKey(tgt.ref)]?.[branchKey(src.ref)]?.skipped ?? b.skipped ?? [];
      (st.mergeBases[branchKey(tgt.ref)] ??= {})[branchKey(src.ref)] = { sourceKey: branchKey(src.ref), source: src.ref, sourceCommitId: s.commit?.commitId ?? null, sourceSeq: s.commit?.seq ?? null, targetCommitId: cm.commitId, base: markSecretValues(clone(s.config)), skipped: nextSkips(r.notTaken, prev, s.commit?.seq ?? null, now(), c.me.email), label: `${src.label}@${s.commit?.seq ?? 'working'}`, updatedAt: now(), updatedBy: c.me.email };
      if (tgt.dep) x.cloudActivity(c, tgt.dep, r.kind === 'merge' ? 'branch.merged' : 'branch.cherry-picked', `${r.kind === 'merge' ? 'Merged' : 'Cherry-picked'} ${r.taken.length} change${r.taken.length === 1 ? '' : 's'} from ${src.label}: ${commitSummary(message)}`, { branch: tgt.ref, commitId: cm.commitId, seq: cm.seq, source: cm.source, picks: r.taken.length, skipped: r.notTaken.length });
      return cm;
    },
    revertCommit: (c) => {
      const b = body<{ branch: BranchRef; commitId: string; message: string; ifHead: string | null; choices?: { key: string; take: 'source' | 'target' | 'custom'; value?: unknown }[] }>(c);
      const i = info(c, b.branch);
      if (!i.canWrite || i.ref.kind === 'agent') throw x.err('forbidden', 'you cannot write this branch');
      const message = checkMsg(b.message);
      if ((b.ifHead ?? null) !== (i.head?.commitId ?? null)) throw x.err('conflict', 'the branch moved — refresh', { reason: 'stale' });
      const k = i.commits.findIndex((y) => y.commitId === b.commitId);
      if (k < 0) throw x.err('not-found', 'commit not found');
      if (k === 0) throw invalid([issue('commitId', 'V_REVERT', 'the first commit of a branch cannot be reverted')]);
      const opts = { secretValues: i.ref.kind !== 'template' };
      const r = applyRevert(i.commits[k]!.config, i.commits[k - 1]!.config, clone(i.head!.config), b.choices ?? [], opts);
      if (r.unresolved.length) throw invalid(r.unresolved.map((y) => issue(y, 'V_MERGE_UNRESOLVED', 'a later commit changed this item — choose')), 'conflicts need a choice');
      if (!r.taken.length) throw invalid([issue('commitId', 'V_NO_CHANGE', 'nothing to revert: later commits already changed everything it did')]);
      const cm = commitTo(c, i, x.normalize(c.db, r.config), 'revert', message, { revertOf: { commitId: i.commits[k]!.commitId, seq: i.commits[k]!.seq } });
      if (i.dep) x.cloudActivity(c, i.dep, 'branch.reverted', `Reverted commit ${i.commits[k]!.seq}: ${commitSummary(message)}`, { branch: i.ref, commitId: cm.commitId, seq: cm.seq, revertOf: cm.revertOf });
      return cm;
    },
    resetToCommit: (c) => {
      const b = body<{ branch: BranchRef; commitId: string; message: string; ifHead: string | null }>(c);
      const i = info(c, b.branch);
      if (!i.canWrite || i.ref.kind === 'agent') throw x.err('forbidden', 'you cannot write this branch');
      const message = checkMsg(b.message);
      if ((b.ifHead ?? null) !== (i.head?.commitId ?? null)) throw x.err('conflict', 'the branch moved — refresh', { reason: 'stale' });
      const t = i.commits.find((y) => y.commitId === b.commitId);
      if (!t) throw x.err('not-found', 'commit not found');
      if (t === i.head) throw invalid([issue('commitId', 'V_NO_CHANGE', 'that commit is the head')]);
      const cm = commitTo(c, i, t.config, 'reset', message, { resetTo: { commitId: t.commitId, seq: t.seq } });
      if (i.dep) x.cloudActivity(c, i.dep, 'branch.reset', `Reset to commit ${t.seq}: ${commitSummary(message)}`, { branch: i.ref, commitId: cm.commitId, seq: cm.seq, resetTo: cm.resetTo });
      return cm;
    },
    // removed (410 gone)
    publish: gone('publish'),
    resetDraftToHead: gone('reset to head'),
    getTemplateSyncStatus: gone('Template sync'),
    getTemplateSyncPlan: gone('Template sync'),
    previewTemplateSync: gone('Template sync'),
    applyTemplateSync: gone('Template sync'),
    unlinkTemplate: gone('Template sync'),
    // helpers used by server.ts (not routes)
    __fromPoint: fromPoint as never,
  };
}

/** helpers for createTemplate / createDeployment / getDiff in server.ts */
export const branchHelpers = {
  tplRef,
  depRef,
  commitsOf,
  headOf,
  store,
  /** the first commit of a new branch + its origin + a merge-base record */
  create(db: MockDb, ref: BranchRef, entity: { branch?: BranchState }, config: Config, by: string, from: { ref: BranchRef; label: string; commit: Commit | null; config: Config } | null, message: string | undefined, catalog: RevisionCatalog | null) {
    const m = (message ?? '').trim() || (from ? createMessage(from.label, from.commit?.seq ?? null) : 'created empty');
    const cm = makeCommit({ commitId: ulid(), branch: ref, parent: null, kind: 'create', message: m, author: authorOf(db, by), createdAt: now(), config: clone(config), catalog, source: from ? { branch: from.ref, commitId: from.commit?.commitId ?? null, seq: from.commit?.seq ?? null, label: from.label, picks: 'all', skipped: 0 } : null });
    commitsOf(db, ref).push(cm);
    const origin = from ? { branch: from.ref, commitId: from.commit?.commitId ?? null, seq: from.commit?.seq ?? null, label: from.label } : null;
    if (from) (store(db).mergeBases[branchKey(ref)] ??= {})[branchKey(from.ref)] = { sourceKey: branchKey(from.ref), source: from.ref, sourceCommitId: from.commit?.commitId ?? null, sourceSeq: from.commit?.seq ?? null, targetCommitId: cm.commitId, base: markSecretValues(clone(from.config)), skipped: [], label: `${from.label}@${from.commit?.seq ?? 'working'}`, updatedAt: now(), updatedBy: by };
    setState(entity, cm, 1, config, origin, ref.kind !== 'template');
    return cm;
  },
  configHash,
};
export const BR_SEED = { HOUR, DAY, ago };
