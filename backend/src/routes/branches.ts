/**
 * §21 #104 branches + commits (docs-v2/branches-commits.md §3, §7.1). Templates, deployments and agent branches are
 * linear lists of immutable commits. §21 #105: there is no working copy — the browser sends its change set with
 * Commit ({ message, baseCommitId, config }); the template fields / deployment draft are a copy of the head config;
 * any two branches compare + cherry-pick through shared/src/branch.ts.
 */
import {
  canonicalize,
  applyMerge,
  applyRevert,
  branchKey,
  checkCommitMessage,
  commitRef,
  commitSummary,
  commitSummaryOf,
  countMerge,
  diffConfigs,
  isFastForward,
  makeCommit,
  secretValueChanges,
  hasSecretValueChanges,
  markSecretValues,
  mergeMissing,
  nextSkips,
  parseBranchKey,
  planMerge,
  pointLabel,
  resetPlan,
  revertPlan,
  sameBranch,
  sourcePicks,
  supportsBranches,
  uncommittedItems,
  verifyCommitLink,
  normalizeConfig,
  type AgentBranchDoc,
  type AgentBranchView,
  type AgentDoc,
  type BranchOrigin,
  type BranchPoint,
  type BranchRef,
  type BranchState,
  type Commit,
  type CommitDetail,
  type CommitKind,
  type CommitRef,
  type CommitSource,
  type CommitView,
  type Config,
  type RevisionCatalog,
  type MergeBase,
  type MergeChoice,
  type MergeItem,
  type MergePlan,
  type MergePreview,
  type Template,
  type SecretDefInput,
  type ValidationIssue,
  type VariableValue,
} from '@opm/shared';
import { col, db, nowIso } from '../core/fire.js';
import { E } from '../core/errors.js';
import { has, loadDep, need, needDep, targetOf } from '../core/perm.js';
import { asObject, clone, generateSecret, onlyKeys } from '../core/util.js';
import { decodeDeployment, decodeTemplate, draftField, encodeTemplate, type StoredDeployment } from '../core/codec.js';
import { buildRevisionCatalog, imageVolumes, loadCatalog, loadDetectedHosts, validationCtx, withSiteImages, type Catalog } from '../core/catalog.js';
import { CloudLog, appendCloud } from '../core/activities.js';
import { maskConfig } from '../core/views.js';
import { normFiles, normSecretDefs, normSites, normVariables, safeValidate } from '../core/config.js';
import { mergeSecrets } from './deployments.js';
import { clampLimit } from './accounts.js';
import { seedBlobContents } from './volume-content.js';
import { commitBlobs, dropStage, optStageId } from '../core/stages.js';
import {
  bcol,
  commitsOf,
  countUncommitted,
  decodeCommit,
  loadCommit,
  loadMergeBase,
  newCommitId,
  personAuthor,
  prepCompat,
  readAgentBranch,
  scopeFor,
  secretValuesFor,
  setMergeBase,
  storeCommitBody,
  summaryOfDoc,
  writeCommit,
  writeCompat,
} from '../core/branches.js';
import type { Ctx, Handlers } from '../core/router.js';

const templateConfig = (t: Pick<Config, 'variables' | 'secrets' | 'files' | 'sites'>): Config => ({ variables: t.variables, secrets: t.secrets, files: t.files, sites: t.sites });

// ───────────────────────────────────────── body parsing
export function asBranchRef(v: unknown, path: string): BranchRef {
  if (typeof v === 'string') {
    const r = parseBranchKey(v);
    if (r) return r;
  } else if (v && typeof v === 'object') {
    const o = v as Record<string, unknown>;
    if (o.kind === 'template' && typeof o.templateId === 'string' && o.templateId) return { kind: 'template', templateId: o.templateId };
    if (o.kind === 'deployment' && typeof o.deploymentId === 'string' && o.deploymentId) return { kind: 'deployment', deploymentId: o.deploymentId };
    if (o.kind === 'agent' && typeof o.deploymentId === 'string' && typeof o.siteSlug === 'string' && o.deploymentId && o.siteSlug) return { kind: 'agent', deploymentId: o.deploymentId, siteSlug: o.siteSlug };
  }
  throw E.bad(path, 'V_TYPE', `${path} must be a branch ({ kind: template | deployment | agent, … } or a branch key)`);
}
export function asPoint(v: unknown, path: string): BranchPoint {
  if (!v || typeof v !== 'object') throw E.bad(path, 'V_TYPE', `${path} must be { branch, at }`);
  const o = v as Record<string, unknown>;
  const branch = asBranchRef(o.branch, `${path}.branch`);
  const at = o.at ?? 'head';
  if (at === 'head' || at === 'working') return { branch, at };
  if (at && typeof at === 'object' && typeof (at as { commitId?: unknown }).commitId === 'string') return { branch, at: { commitId: (at as { commitId: string }).commitId } };
  throw E.bad(`${path}.at`, 'V_TYPE', `${path}.at must be head | working | { commitId }`);
}
function asChoices(v: unknown): MergeChoice[] {
  if (v === undefined) return [];
  if (!Array.isArray(v)) throw E.bad('choices', 'V_TYPE', 'choices must be a list');
  return v.map((x, i) => {
    if (!x || typeof x !== 'object' || typeof (x as MergeChoice).key !== 'string') throw E.bad(`choices[${i}]`, 'V_TYPE', 'a choice is { key, take, value? }');
    const c = x as MergeChoice;
    if (!['source', 'target', 'custom'].includes(c.take)) throw E.bad(`choices[${i}].take`, 'V_ENUM', 'take must be source | target | custom');
    return { key: c.key, take: c.take, ...(c.take === 'custom' ? { value: c.value } : {}) };
  });
}
function asValues(v: unknown): Record<string, VariableValue> | undefined {
  if (v === undefined || v === null) return undefined;
  if (typeof v !== 'object' || Array.isArray(v)) throw E.bad('values', 'V_TYPE', 'values must be an object');
  return v as Record<string, VariableValue>;
}
function asSecretValues(v: unknown, target: BranchRef): Record<string, string> | undefined {
  if (v === undefined || v === null) return undefined;
  if (typeof v !== 'object' || Array.isArray(v) || Object.values(v).some((x) => typeof x !== 'string')) throw E.bad('secretValues', 'V_TYPE', 'secretValues must be an object of strings');
  const out = Object.fromEntries(Object.entries(v as Record<string, string>).filter(([, x]) => x !== ''));
  if (!Object.keys(out).length) return undefined;
  if (target.kind !== 'deployment') throw E.bad('secretValues', 'V_TEMPLATE_SECRET_VALUE', 'secret values can only be set when merging into a deployment');
  return out;
}
function needMessage(m: unknown): string {
  const issues = checkCommitMessage(m);
  if (issues.length) throw E.invalid(issues);
  return (m as string).trim();
}

// ───────────────────────────────────────── branch resolution
export interface Resolved {
  ref: BranchRef;
  label: string;
  head: Commit | null;
  /** agent branches: uncommitted drift reported by the site (cloud branches: always 0 — §21 #105 no working copy) */
  uncommitted: number;
  origin: BranchOrigin | null;
  dep?: StoredDeployment;
  tpl?: Template;
  agent?: AgentBranchDoc | null;
}
export async function resolveBranch(c: Ctx, ref: BranchRef, mode: 'read' | 'write'): Promise<Resolved> {
  if (ref.kind === 'template') {
    need(c, 'templates', mode === 'read' ? 'read' : 'write');
    const s = await col.templates().doc(ref.templateId).get();
    if (!s.exists) throw E.notFound('template not found');
    const tpl = decodeTemplate(s.data()!);
    const head = tpl.branch?.head ? await loadCommit(ref, tpl.branch.head.commitId, undefined, { human: true }) : null;
    return { ref, label: tpl.label, head, uncommitted: 0, origin: tpl.branch?.origin ?? null, tpl };
  }
  const dep = await loadDep(c, ref.deploymentId);
  if (ref.kind === 'deployment') {
    needDep(c, dep, 'deployment.config', mode === 'read' ? 'read' : 'publish');
    const head = dep.branch?.head ? await loadCommit(ref, dep.branch.head.commitId, undefined, { human: true }) : null;
    return { ref, label: dep.label, head, uncommitted: 0, origin: dep.branch?.origin ?? null, dep };
  }
  needDep(c, dep, 'deployment.config', 'read');
  if (mode === 'write') throw E.forbidden('agent branches are written only by their agent (merge in the agent GUI of the site)');
  const agent = await readAgentBranch(dep.deploymentId, ref.siteSlug);
  if (!agent) {
    const a = await col.agents(dep.deploymentId).doc(ref.siteSlug).get();
    if (!a.exists) throw E.notFound(`site ${ref.siteSlug} has no agent branch`);
  }
  const head = agent?.head && !agent.legacy ? await loadCommit(ref, agent.head.commitId, undefined, { human: true }) : agent?.head ? await loadCommit({ kind: 'deployment', deploymentId: dep.deploymentId }, agent.head.commitId, undefined, { human: true }) : null;
  return { ref, label: `${dep.label} · ${ref.siteSlug}`, head, uncommitted: agent?.uncommitted ? agent.uncommitted.working + agent.uncommitted.drift + agent.uncommitted.notes : 0, origin: null, dep, agent };
}
/** The config + commit of a point (read permission). */
export async function resolvePoint(c: Ctx, p: BranchPoint): Promise<{ r: Resolved; config: Config; commit: Commit | null; label: string }> {
  const r = await resolveBranch(c, p.branch, 'read');
  if (p.at === 'working') throw E.bad('at', 'V_ENUM', 'there is no server working copy (§21 #105): pending changes live in the browser — compare them there, or use head / { commitId }');
  if (p.at === 'head') {
    if (!r.head) throw E.notFound(`${r.label} has no commit yet`);
    return { r, config: r.head.config, commit: r.head, label: pointLabel(r.label, r.head.seq) };
  }
  const cm = await loadCommit(p.branch, p.at.commitId, undefined, { human: true });
  if (!cm) throw E.notFound('commit not found');
  return { r, config: cm.config, commit: cm, label: pointLabel(r.label, cm.seq) };
}

/** §4.5 base selection */
async function selectBase(source: { r: Resolved; commit: Commit | null }, target: Resolved): Promise<{ base: Config | null; skipped: MergeBase['skipped']; info: MergePlan['base'] }> {
  const rec = await loadMergeBase(target.ref, source.r.ref);
  if (rec) return { base: rec.base, skipped: rec.skipped ?? [], info: { kind: 'record', sourceCommitId: rec.sourceCommitId, label: rec.label } };
  const o = target.origin;
  if (o && sameBranch(o.branch, source.r.ref) && o.commitId) {
    const cm = await loadCommit(source.r.ref, o.commitId, undefined, { human: true });
    if (cm) return { base: cm.config, skipped: [], info: { kind: 'origin', sourceCommitId: cm.commitId, label: pointLabel(o.label, cm.seq) } };
  }
  const so = source.r.origin;
  if (so && sameBranch(so.branch, target.ref) && so.commitId) {
    const cm = await loadCommit(target.ref, so.commitId, undefined, { human: true });
    if (cm) return { base: cm.config, skipped: [], info: { kind: 'origin', sourceCommitId: cm.commitId, label: pointLabel(target.label, cm.seq) } };
  }
  // agent branch → its deployment: the deployment commit the site last merged
  if (source.r.ref.kind === 'agent' && target.ref.kind === 'deployment' && source.r.ref.deploymentId === target.ref.deploymentId && source.r.agent?.mergedFrom) {
    const cm = await loadCommit(target.ref, source.r.agent.mergedFrom.commitId, undefined, { human: true });
    if (cm) return { base: cm.config, skipped: [], info: { kind: 'origin', sourceCommitId: cm.commitId, label: pointLabel(target.label, cm.seq) } };
  }
  if (source.r.ref.kind === 'deployment' && target.ref.kind === 'agent' && target.agent?.mergedFrom) {
    const cm = await loadCommit(source.r.ref, target.agent.mergedFrom.commitId, undefined, { human: true });
    if (cm) return { base: cm.config, skipped: [], info: { kind: 'origin', sourceCommitId: cm.commitId, label: pointLabel(source.r.label, cm.seq) } };
  }
  return { base: null, skipped: [], info: { kind: 'none', sourceCommitId: null, label: 'no common history' } };
}

/** permission locks of secret items (§3.3) */
function lockItems(c: Ctx, items: MergeItem[], source: Resolved, target: Resolved) {
  const srcRead = source.ref.kind === 'template' ? true : has(c, 'deployment.secrets', 'read', targetOf(source.dep!));
  const tgtWrite = target.ref.kind === 'template' ? has(c, 'templates', 'write') : target.dep ? has(c, 'deployment.secrets', 'write', targetOf(target.dep)) : false;
  for (const it of items) {
    if (it.section !== 'secrets') continue;
    if (it.key.endsWith('/value')) {
      if (!srcRead || !tgtWrite) it.locked = 'needs deployment.secrets.read on the source and deployment.secrets.write on the target';
    } else if (target.ref.kind === 'deployment' && !tgtWrite) it.locked = 'needs deployment.secrets.write on the target';
  }
}

interface Planned {
  source: Awaited<ReturnType<typeof resolvePoint>>;
  target: Resolved;
  base: Awaited<ReturnType<typeof selectBase>>;
  opts: { secretValues: boolean; scope: string | null };
}
async function planFor(c: Ctx, sp: BranchPoint, tref: BranchRef, mode: 'read' | 'write'): Promise<Planned> {
  const source = await resolvePoint(c, sp);
  const target = await resolveBranch(c, tref, mode);
  if (sameBranch(source.r.ref, target.ref) && mode === 'write') throw E.bad('source', 'V_REF', 'source and target are the same branch (use revert / reset)');
  const base = await selectBase(source, target);
  return { source, target, base, opts: { secretValues: secretValuesFor(source.r.ref, target.ref), scope: scopeFor(source.r.ref, target.ref) } };
}

// ───────────────────────────────────────── deployment / template commit writer
interface DeployCheck {
  blockers: ValidationIssue[];
  warnings: ValidationIssue[];
}
async function deploymentCheck(dep: StoredDeployment, config: Config, head: Commit | null, cat: Catalog): Promise<DeployCheck> {
  const [detected, blobContents] = await Promise.all([loadDetectedHosts(dep.deploymentId), seedBlobContents(config)]);
  const res = safeValidate(config, { ...validationCtx('publish', dep.deploymentId, cat, undefined, detected), ...(blobContents ? { blobContents } : {}) });
  const blockers: ValidationIssue[] = [...res.errors];
  if (head && uncommittedItems(head.config, config, { secretValues: true }).length === 0) blockers.push({ path: '', code: 'V_NO_CHANGE', message: 'nothing to commit: the result equals the head', severity: 'error' });
  // agent-version notes (§21 #104 j): old sites still get the commit as a proposal
  const agents = await col.agents(dep.deploymentId).select('agentVersion').get();
  const warnings = [...res.warnings];
  for (const a of agents.docs) {
    const v = (a.data() as AgentDoc).agentVersion ?? null;
    if (!supportsBranches(v)) warnings.push({ path: `sites[${a.id}]`, code: 'W_AGENT_BRANCHES', message: `${a.id} runs agent ${v ?? 'unknown'} — needs agent ≥ 0.14 for agent branches; it still receives this commit as a proposal (approve it in its agent GUI, whole commit only)`, severity: 'warning' });
  }
  return { blockers, warnings };
}
const templateCheck = (config: Config, head: Commit | null, cat: Catalog) => {
  const res = safeValidate(config, validationCtx('template', 'template', cat));
  const blockers = [...res.errors];
  if (head && uncommittedItems(head.config, config).length === 0) blockers.push({ path: '', code: 'V_NO_CHANGE', message: 'nothing to commit: the result equals the head', severity: 'error' });
  return { blockers, warnings: res.warnings };
};
/** a deployment result: secrets the merge ADDED with a generate rule get a value (the create rule) */
function fillGeneratedSecrets(cfg: Config, now: string): string[] {
  const out: string[] = [];
  for (const s of cfg.secrets) if ((s.value === null || s.value === undefined || s.value === '') && s.generate) ((s.value = generateSecret(s.generate.charset, s.generate.length)), (s.rotatedAt = now), out.push(s.secretSlug));
  return out;
}

export interface WriteSpec {
  kind: CommitKind;
  message: string;
  config: Config;
  source?: CommitSource | null;
  /** §21 #113: the catalog of the merged source commit (its site images are valid images of the result) */
  sourceCatalog?: RevisionCatalog | null;
  revertOf?: Commit['revertOf'];
  resetTo?: Commit['resetTo'];
  /** the merge base record to write (merges) */
  mergeBase?: Omit<MergeBase, 'targetCommitId'> | null;
  /** optimistic check: the head the change was made on (409 stale { reason: 'head-moved', head } otherwise) */
  expectHead: string | null;
  /** activity type (deployments) */
  activity: string;
  /** §21 #105 slugs whose secret value the commit sets / generates / clears (recorded, never the value) */
  generated?: string[];
  activitySummary: string;
  activityData?: Record<string, unknown>;
  data?: Commit['data'];
  /** §21 #107: after validation, BEFORE anything is written — verify + promote the commit's staged blobs */
  beforeWrite?: (config: Config) => Promise<void>;
}
/** Write one commit on a template or deployment branch (validation, catalog, compat, working copy := result). */
export async function commitTo(c: Ctx, target: Resolved, spec: WriteSpec, cat?: Catalog): Promise<{ commit: Commit; validation: DeployCheck }> {
  // §21 #113 site images of the head (kept by later commits) and of the merged source are valid images
  const catalog0 = withSiteImages(cat ?? (await loadCatalog()), target.head?.catalog, spec.sourceCatalog);
  const now = nowIso();
  if (target.ref.kind === 'agent') throw E.forbidden('agent branches are written only by their agent');
  if ((target.head?.commitId ?? null) !== spec.expectHead) throw E.stale(`${target.label} moved (head is now seq ${target.head?.seq ?? 0}) — rebase your changes and commit again`, { reason: 'head-moved', head: target.head ? commitRef(target.head) : null });
  let config = clone(spec.config);
  const generated = new Set(spec.generated ?? []);
  if (target.ref.kind === 'deployment') {
    config = normalizeConfig(config, imageVolumes(catalog0));
    for (const s of fillGeneratedSecrets(config, now)) generated.add(s);
  } else config.secrets = config.secrets.map((s) => ({ ...s, value: null, rotatedAt: null }));
  const secretValues = target.ref.kind === 'deployment' ? secretValueChanges(target.head?.config.secrets ?? [], config.secrets, [...generated]) : null;
  const chk = target.ref.kind === 'deployment' ? await deploymentCheck(target.dep!, config, target.head, catalog0) : templateCheck(config, target.head, catalog0);
  if (chk.blockers.length) throw E.invalid(chk.blockers, `cannot commit: ${chk.blockers[0]!.message}`);
  if (spec.beforeWrite) await spec.beforeWrite(config);
  const catalog = target.ref.kind === 'deployment' ? await buildRevisionCatalog(config, catalog0) : null;
  const commit = makeCommit({
    commitId: newCommitId(),
    branch: target.ref,
    parent: target.head,
    kind: spec.kind,
    message: spec.message,
    author: personAuthor(c),
    createdAt: now,
    config,
    catalog,
    source: spec.source ?? null,
    revertOf: spec.revertOf ?? null,
    resetTo: spec.resetTo ?? null,
    data: { ...(spec.data ?? {}), ...(secretValues && hasSecretValueChanges(secretValues) ? { secretValues } : {}) },
  });
  const external = await storeCommitBody(commit);
  const uid = c.account!.uid;
  if (target.ref.kind === 'template') {
    const ref = col.templates().doc(target.ref.templateId);
    await db().runTransaction(async (tx) => {
      const s = await tx.get(ref);
      if (!s.exists) throw E.notFound('template not found');
      const cur = decodeTemplate(s.data()!);
      if ((cur.branch?.head?.commitId ?? null) !== spec.expectHead) throw E.stale('the template moved — rebase your changes and commit again', { reason: 'head-moved', head: cur.branch?.head ?? null });
      await writeCommit(tx, commit, external);
      const branch: BranchState = { head: commitRef(commit), origin: cur.branch?.origin ?? null, commitCount: (cur.branch?.commitCount ?? 0) + 1 };
      const changed = canonicalize(templateConfig(cur)) !== canonicalize(config);
      tx.set(ref, encodeTemplate({ ...cur, ...config, version: cur.version + (changed ? 1 : 0), updatedAt: now, updatedBy: uid, branch }));
      if (spec.mergeBase) setMergeBase(tx, target.ref, { ...spec.mergeBase, targetCommitId: commit.commitId });
    });
    return { commit, validation: chk };
  }
  const depId = target.ref.deploymentId;
  const ref = col.deployments().doc(depId);
  await db().runTransaction(async (tx) => {
    const s = await tx.get(ref);
    const dep = decodeDeployment(s.data()!);
    if (dep.deleted) throw E.notFound('deployment not found');
    if ((dep.branch?.head?.commitId ?? null) !== spec.expectHead) throw E.stale('the deployment moved — rebase your changes and commit again', { reason: 'head-moved', head: dep.branch?.head ?? null });
    const prep = await prepCompat(tx, depId);
    const log = new CloudLog(depId, dep);
    const compat = writeCompat(tx, dep, commit, prep, log, false, uid);
    commit.data = { ...commit.data, compat };
    await writeCommit(tx, commit, external);
    log.add(tx, { type: spec.activity, summary: spec.activitySummary.slice(0, 300), data: { branch: branchKey(target.ref), commitId: commit.commitId, seq: commit.seq, message: commitSummary(commit.message), kind: commit.kind, ...(commit.source ? { source: branchKey(commit.source.branch), sourceCommitId: commit.source.commitId, picks: commit.source.picks === 'all' ? 'all' : commit.source.picks.length, skipped: commit.source.skipped } : {}), ...(compat ? { compat } : {}), ...(commit.data.secretValues ? { secretValues: commit.data.secretValues } : {}), ...(spec.activityData ?? {}) }, actor: uid });
    const branch: BranchState = { head: commitRef(commit), origin: dep.branch?.origin ?? null, commitCount: (dep.branch?.commitCount ?? 0) + 1 };
    const changed = canonicalize(dep.draft) !== canonicalize(config);
    tx.update(ref, { ...draftField(config), draftVersion: dep.draftVersion + (changed ? 1 : 0), branch, agentEpoch: dep.agentEpoch + 1, ...log.depUpdate(), updatedAt: now, updatedBy: uid });
    if (spec.mergeBase) setMergeBase(tx, target.ref, { ...spec.mergeBase, targetCommitId: commit.commitId });
  });
  return { commit, validation: chk };
}

// ───────────────────────────────────────── views
async function listCommitsOf(c: Ctx, ref: BranchRef) {
  const limit = clampLimit(c, 50, 200);
  let q = commitsOf(ref).orderBy('seq', 'desc');
  const cursor = c.query.get('cursor');
  if (cursor) {
    const s = await commitsOf(ref).doc(cursor).get();
    if (!s.exists) throw E.bad('cursor', 'V_FORMAT', 'bad cursor');
    q = q.startAfter(s);
  }
  const r = await q.select('commitId', 'branch', 'seq', 'parentId', 'kind', 'message', 'author', 'createdAt', 'source', 'revertOf', 'resetTo', 'rollbackOf', 'hash', 'stats').limit(limit + 1).get();
  const docs = r.docs.slice(0, limit);
  return { items: docs.map((d) => summaryOfDoc(d.data())), cursor: r.docs.length > limit ? docs.at(-1)!.id : null };
}
async function commitDetail(ref: BranchRef, commitId: string, scope: string | null = null): Promise<CommitDetail> {
  const cm = await loadCommit(ref, commitId, undefined, { human: true });
  if (!cm) throw E.notFound('commit not found');
  const parent = cm.parentId ? await loadCommit(ref, cm.parentId, undefined, { human: true }) : null;
  const changes = uncommittedItems(parent?.config ?? null, cm.config, { secretValues: ref.kind !== 'template', scope });
  return { commit: { ...cm, config: maskConfig(cm.config) }, changes };
}

function planResponse(p: Planned, items: MergeItem[], diffOnly: boolean): MergePlan {
  return {
    source: { point: { branch: p.source.r.ref, at: p.source.commit ? { commitId: p.source.commit.commitId } : 'head' }, label: p.source.label, commit: p.source.commit ? commitRef(p.source.commit) : null },
    target: { branch: p.target.ref, label: p.target.label, head: p.target.head ? commitRef(p.target.head) : null, workingClean: p.target.uncommitted === 0, uncommitted: p.target.uncommitted },
    base: p.base.info,
    items,
    counts: countMerge(items),
    fastForward: isFastForward(items),
    diffOnly,
  };
}

/** apply a request to the plan (shared by preview + merge) */
async function mergeResult(c: Ctx, b: Record<string, unknown>, mode: 'read' | 'write') {
  const sp = asPoint(b.source, 'source');
  const tref = asBranchRef(b.target, 'target');
  const secretValues = asSecretValues(b.secretValues, tref);
  const p = await planFor(c, sp, tref, mode);
  if (!p.target.head && tref.kind !== 'template' && tref.kind !== 'deployment') throw E.notFound('the target has no commit yet');
  if (b.targetHead !== undefined && (b.targetHead ?? null) !== (p.target.head?.commitId ?? null)) throw E.stale(`${p.target.label} moved (head is now seq ${p.target.head?.seq ?? 0}) — refresh`, { head: p.target.head ? commitRef(p.target.head) : null });
  if (sp.at === 'head' && b.sourceCommit !== undefined && b.sourceCommit !== null && b.sourceCommit !== (p.source.commit?.commitId ?? null)) throw E.conflict(`${p.source.r.label} moved — refresh`, { reason: 'source-changed', head: p.source.commit ? commitRef(p.source.commit) : null });
  const choices = asChoices(b.choices);
  const values = asValues(b.values);
  const targetCfg = p.target.head?.config ?? { variables: [], secrets: [], files: [], sites: [] };
  const items = planMerge(p.base.base, p.source.config, targetCfg, { ...p.opts, skipped: p.base.skipped });
  lockItems(c, items, p.source.r, p.target);
  const locked = new Map(items.filter((i) => i.locked).map((i) => [i.key, i.locked!]));
  for (const ch of choices) if (ch.take !== 'target' && locked.has(ch.key)) throw E.forbidden(`${ch.key}: ${locked.get(ch.key)}`);
  const r = applyMerge(p.base.base, p.source.config, targetCfg, choices, { ...p.opts, skipped: p.base.skipped, values });
  if (secretValues) {
    if (mode === 'write') needDep(c, p.target.dep!, 'deployment.secrets', 'write');
    const now = nowIso();
    for (const [slug, value] of Object.entries(secretValues)) {
      const s = r.config.secrets.find((x) => x.secretSlug === slug);
      if (!s) r.errors.push({ key: `secrets/${slug}/value`, message: `secret ${slug} is not in the result` });
      else ((s.value = value), (s.rotatedAt = now));
    }
  }
  return { p, r, choices, items };
}

// ───────────────────────────────────────── §21 #105 direct commits
function asBase(b: Record<string, unknown>): string | null {
  if (!('baseCommitId' in b)) throw E.bad('baseCommitId', 'V_REQUIRED', 'baseCommitId (the head commit your changes were made on, or null) is required');
  if (b.baseCommitId !== null && (typeof b.baseCommitId !== 'string' || !b.baseCommitId)) throw E.bad('baseCommitId', 'V_TYPE', 'baseCommitId must be a commit id or null');
  return b.baseCommitId as string | null;
}
function staleCheck(t: Resolved, base: string | null) {
  if ((t.head?.commitId ?? null) !== base) throw E.stale(`${t.label} moved (head is now seq ${t.head?.seq ?? 0}) — rebase your changes and commit again`, { reason: 'head-moved', head: t.head ? commitRef(t.head) : null });
}
/** the save-level check of the old section writes, on the change set AS SENT (before normalizeConfig coerces anything) */
function saveLevel(config: Config, mode: 'template' | 'draft', deploymentId: string, cat: Catalog) {
  const res = safeValidate(config, validationCtx(mode, deploymentId, cat));
  if (res.errors.length) throw E.invalid(res.errors, `cannot save: ${res.errors[0]!.message}`);
}
/**
 * The browser change set → a full Config, normalized exactly like the pre-#105 section writes. Secrets are
 * SecretDefInput merged with the BASE HEAD values (plainValue absent = keep, null = clear, string = set, generateNow).
 * Templates hold no values (a string plainValue / generateNow → 422).
 */
export function configFromInput(kind: 'template' | 'deployment', input: unknown, baseSecrets: Config['secrets'], cat: Catalog): { config: Config; generated: string[] } {
  if (!input || typeof input !== 'object' || Array.isArray(input)) throw E.bad('config', 'V_REQUIRED', 'config (the full resulting config: variables, secrets, files, sites) is required');
  const o = input as Record<string, unknown>;
  for (const k of ['variables', 'secrets', 'files', 'sites']) if (!Array.isArray(o[k])) throw E.bad(`config.${k}`, 'V_TYPE', `config.${k} must be a list`);
  (o.secrets as unknown[]).forEach((x, i) => {
    if (!x || typeof x !== 'object') throw E.bad(`config.secrets[${i}]`, 'V_TYPE', 'each secret must be an object');
    const sx = x as SecretDefInput & { value?: unknown };
    if (kind === 'template' && ((typeof sx.plainValue === 'string' && sx.plainValue !== '') || sx.generateNow)) throw E.bad(`config.secrets[${i}].plainValue`, 'V_TEMPLATE_SECRET_VALUE', 'templates hold no secret values (set them in a deployment)');
  });
  const items = (o.secrets as (SecretDefInput & { value?: unknown; rotatedAt?: unknown })[]).map(({ value: _v, rotatedAt: _r, ...x }) => (void _v, void _r, x as SecretDefInput));
  const generated = items.filter((x) => x.generateNow === true || (x.plainValue === undefined && !baseSecrets.some((s) => s.secretSlug === x.secretSlug) && !!x.generate)).map((x) => x.secretSlug);
  const secrets = kind === 'template' ? normSecretDefs(items.map(({ plainValue: _p, generateNow: _g, ...d }) => (void _p, void _g, d))).map((s) => ({ ...s, value: null, rotatedAt: null })) : mergeSecrets(items, baseSecrets);
  const config: Config = { variables: normVariables(o.variables), secrets, files: normFiles(o.files), sites: normSites(o.sites, imageVolumes(cat)) };
  return { config, generated: kind === 'template' ? [] : generated };
}

/** O1: every human response carrying a commit masks its secret values (the plain commit is only for agents: agentCommit). */
const commitView = (cm: Commit): CommitView => ({ ...cm, config: maskConfig(cm.config) });

/** §21 #105 + #107 the deployment commit (the handler wraps it so the stage is always dropped) */
async function commitDeploymentBody(c: Ctx, b: Record<string, unknown>, stageId: string | undefined): Promise<Commit> {
  const message = needMessage(b.message);
  const base = asBase(b);
  const t = await resolveBranch(c, { kind: 'deployment', deploymentId: c.params.deploymentId! }, 'write');
  c.audit = { data: { message: commitSummary(message) } };
  staleCheck(t, base);
  const cat = withSiteImages(await loadCatalog(), t.head?.catalog); // §21 #113 site images the head already runs
  const { config, generated } = configFromInput('deployment', b.config, t.head?.config.secrets ?? t.dep!.draft.secrets, cat);
  saveLevel(config, 'draft', t.ref.kind === 'deployment' ? t.ref.deploymentId : 'deployment', cat);
  // secret definitions or values changed → deployment.secrets.write (as the old secrets section write)
  const defs = (l: Config['secrets']) => canonicalize(l.map(({ value: _v, rotatedAt: _r, ...d }) => (void _v, void _r, d)));
  const before = t.head?.config.secrets ?? [];
  const vals = secretValueChanges(before, config.secrets, generated);
  if (defs(before) !== defs(config.secrets) || hasSecretValueChanges(vals)) needDep(c, t.dep!, 'deployment.secrets', 'write');
  const { commit } = await commitTo(c, t, { kind: 'change', message, config, generated, expectHead: base, activity: 'branch.committed', activitySummary: `Committed seq ${(t.head?.seq ?? 0) + 1}: ${commitSummary(message)}`, beforeWrite: commitBlobs(c, t.head?.config ?? null, stageId) }, cat);
  c.audit = { data: { commitId: commit.commitId, seq: commit.seq, message: commitSummary(message), compat: commit.data.compat ?? null, ...(commit.data.secretValues ? { secretValues: commit.data.secretValues } : {}) } };
  return commit;
}

export const branchHandlers: Pick<
  Handlers,
  | 'listTemplateCommits'
  | 'getTemplateCommit'
  | 'commitTemplate'
  | 'discardTemplateWorking'
  | 'listDeploymentCommits'
  | 'getDeploymentCommit'
  | 'commitDeployment'
  | 'discardDeploymentWorking'
  | 'listAgentBranches'
  | 'listAgentBranchCommits'
  | 'getAgentBranchCommit'
  | 'compareBranches'
  | 'previewMerge'
  | 'merge'
  | 'revertCommit'
  | 'resetToCommit'
> = {
  async listTemplateCommits(c) {
    const ref: BranchRef = { kind: 'template', templateId: c.params.templateId! };
    await resolveBranch(c, ref, 'read');
    return listCommitsOf(c, ref);
  },
  async getTemplateCommit(c) {
    const ref: BranchRef = { kind: 'template', templateId: c.params.templateId! };
    need(c, 'templates', 'read');
    return commitDetail(ref, c.params.commitId!);
  },
  async commitTemplate(c) {
    const b = asObject(c.json());
    onlyKeys(b, ['message', 'baseCommitId', 'config', 'stageId']);
    const stageId = optStageId(b);
    // §21 #107: one transaction incl. the bytes — the stage is deleted whatever happens (promoted blobs stay in the store)
    try {
      const message = needMessage(b.message);
      const base = asBase(b);
      const t = await resolveBranch(c, { kind: 'template', templateId: c.params.templateId! }, 'write');
      c.audit = { data: { message: commitSummary(message) } };
      staleCheck(t, base);
      const cat = await loadCatalog();
      const { config } = configFromInput('template', b.config, [], cat);
      saveLevel(config, 'template', 'template', cat);
      const { commit } = await commitTo(c, t, { kind: 'change', message, config, expectHead: base, activity: 'branch.committed', activitySummary: '', beforeWrite: commitBlobs(c, t.head?.config ?? null, stageId) }, cat);
      c.audit = { data: { commitId: commit.commitId, seq: commit.seq, message: commitSummary(message) } };
      return commitView(commit);
    } finally {
      await dropStage(stageId, c.account!.uid);
    }
  },
  async discardTemplateWorking() {
    throw E.gone('there is no working copy (§21 #105): pending changes live in the browser and are discarded there', { hint: 'discard in the browser' });
  },
  async listDeploymentCommits(c) {
    const ref: BranchRef = { kind: 'deployment', deploymentId: c.params.deploymentId! };
    await resolveBranch(c, ref, 'read');
    return listCommitsOf(c, ref);
  },
  async getDeploymentCommit(c) {
    const dep = await loadDep(c);
    needDep(c, dep, 'deployment.config', 'read');
    return commitDetail({ kind: 'deployment', deploymentId: dep.deploymentId }, c.params.commitId!);
  },
  async commitDeployment(c) {
    const b = asObject(c.json());
    onlyKeys(b, ['message', 'baseCommitId', 'config', 'stageId']);
    const stageId = optStageId(b);
    try {
      return commitView(await commitDeploymentBody(c, b, stageId));
    } finally {
      await dropStage(stageId, c.account!.uid);
    }
  },
  async discardDeploymentWorking() {
    throw E.gone('there is no working copy (§21 #105): pending changes live in the browser and are discarded there', { hint: 'discard in the browser' });
  },

  async listAgentBranches(c) {
    const dep = await loadDep(c);
    needDep(c, dep, 'deployment.config', 'read');
    const [agents, branches] = await Promise.all([col.agents(dep.deploymentId).get(), bcol.agentBranches(dep.deploymentId).get()]);
    const docs = new Map<string, AgentBranchDoc>(branches.docs.map((d) => [d.id, d.data() as AgentBranchDoc]));
    const versions = new Map<string, string | null>(agents.docs.map((d) => [d.id, (d.data() as AgentDoc).agentVersion ?? null]));
    const slugs = [...new Set([...docs.keys(), ...versions.keys()])].sort();
    const depRef: BranchRef = { kind: 'deployment', deploymentId: dep.deploymentId };
    const depHead = dep.branch?.head ? await loadCommit(depRef, dep.branch.head.commitId, undefined, { human: true }) : null;
    const items: AgentBranchView[] = [];
    for (const s of slugs) {
      const d = docs.get(s) ?? { siteSlug: s, head: null, legacy: !supportsBranches(versions.get(s) ?? null), mergedFrom: null, uncommitted: null, pushedAt: null, agentVersion: versions.get(s) ?? null };
      const agentVersion = versions.get(s) ?? d.agentVersion ?? null;
      let behind = 0;
      let ahead = 0;
      let differs = 0;
      if (depHead) behind = Math.max(0, depHead.seq - (d.mergedFrom?.seq ?? 0));
      if (d.head) {
        const aref: BranchRef = d.legacy ? depRef : { kind: 'agent', deploymentId: dep.deploymentId, siteSlug: s };
        const ah = await loadCommit(aref, d.head.commitId, undefined, { human: true });
        if (ah && depHead) {
          const diff = uncommittedItems(depHead.config, ah.config, { secretValues: true, scope: s });
          differs = diff.length;
          if (!d.legacy && diff.length) {
            const keys = new Set(diff.map((i) => i.key));
            const after = await bcol.agentCommits(dep.deploymentId, s).where('seq', '>', 0).orderBy('seq', 'desc').limit(50).get();
            for (const x of after.docs) {
              const cm = x.data() as Commit;
              if (cm.source?.branch?.kind === 'deployment') break;
              if ((cm.stats?.keys ?? []).some((k) => keys.has(k))) ahead++;
            }
          }
        }
      }
      items.push({ ...d, siteSlug: s, agentVersion, legacy: !supportsBranches(agentVersion), behind, ahead, differs, needsAgent014: !supportsBranches(agentVersion) });
    }
    return { items };
  },
  async listAgentBranchCommits(c) {
    const dep = await loadDep(c);
    needDep(c, dep, 'deployment.config', 'read');
    return listCommitsOf(c, { kind: 'agent', deploymentId: dep.deploymentId, siteSlug: c.params.siteSlug! });
  },
  async getAgentBranchCommit(c) {
    const dep = await loadDep(c);
    needDep(c, dep, 'deployment.config', 'read');
    return commitDetail({ kind: 'agent', deploymentId: dep.deploymentId, siteSlug: c.params.siteSlug! }, c.params.commitId!, c.params.siteSlug!);
  },
  async compareBranches(c) {
    const b = asObject(c.json());
    onlyKeys(b, ['source', 'target', 'diffOnly']);
    const sp = asPoint(b.source, 'source');
    const diffOnly = b.diffOnly === true;
    if (diffOnly) {
      // two points, read only: two-way, no locks
      const tp = b.target && typeof b.target === 'object' && 'branch' in (b.target as object) ? asPoint(b.target, 'target') : { branch: asBranchRef(b.target, 'target'), at: 'head' as const };
      const [s, t] = await Promise.all([resolvePoint(c, sp), resolvePoint(c, tp)]);
      const opts = { secretValues: secretValuesFor(s.r.ref, t.r.ref), scope: scopeFor(s.r.ref, t.r.ref) };
      const items = planMerge(null, s.config, t.config, { ...opts, preselectFastForward: false });
      const plan: MergePlan = {
        source: { point: sp, label: s.label, commit: s.commit ? commitRef(s.commit) : null },
        target: { branch: t.r.ref, label: t.label, head: t.commit ? commitRef(t.commit) : null, workingClean: t.r.uncommitted === 0, uncommitted: t.r.uncommitted },
        base: { kind: 'none', sourceCommitId: null, label: 'read-only compare' },
        items,
        counts: countMerge(items),
        fastForward: false,
        diffOnly: true,
      };
      return plan;
    }
    const tref = asBranchRef(b.target, 'target');
    const p = await planFor(c, sp, tref, 'read');
    const items = planMerge(p.base.base, p.source.config, p.target.head?.config ?? { variables: [], secrets: [], files: [], sites: [] }, { ...p.opts, skipped: p.base.skipped });
    lockItems(c, items, p.source.r, p.target);
    if (tref.kind === 'agent') for (const i of items) i.locked = 'agent branches are written only by their agent';
    return planResponse(p, items, tref.kind === 'agent');
  },
  async previewMerge(c) {
    const b = asObject(c.json());
    onlyKeys(b, ['source', 'target', 'targetHead', 'sourceCommit', 'choices', 'values', 'secretValues', 'message']); // message: ignored (same body as merge)
    const { p, r } = await mergeResult(c, b, 'read');
    const now = nowIso();
    const result = clone(r.config);
    if (p.target.ref.kind === 'deployment') fillGeneratedSecrets(result, now);
    const cat = withSiteImages(await loadCatalog(), p.target.head?.catalog, p.source.commit?.catalog);
    // UX: nothing picked → say so (the checks of an unchanged result only give misleading errors)
    const chk: DeployCheck = !r.taken.length
      ? { blockers: [{ path: 'choices', code: 'V_NO_CHANGE', message: 'nothing picked: choose at least one change to merge', severity: 'error' }], warnings: [] }
      : p.target.ref.kind === 'deployment' && p.target.dep
        ? await deploymentCheck(p.target.dep, result, p.target.head, cat)
        : templateCheck(result, p.target.head, cat);
    const res: MergePreview = {
      result: maskConfig(result),
      changes: diffConfigs(p.target.head?.config ?? null, result, undefined, (slug) => (p.target.head?.config.secrets.find((s) => s.secretSlug === slug)?.value ?? null) !== (result.secrets.find((s) => s.secretSlug === slug)?.value ?? null)),
      validation: { errors: chk.blockers, warnings: chk.warnings },
      unresolved: r.unresolved,
      missing: mergeMissing(result),
      choiceErrors: r.errors,
      kind: r.kind,
      taken: r.taken.length,
      skipped: r.notTaken.length,
      warnings: [],
    };
    return res;
  },
  async merge(c) {
    const b = asObject(c.json());
    onlyKeys(b, ['source', 'target', 'targetHead', 'sourceCommit', 'choices', 'values', 'secretValues', 'message']);
    const message = needMessage(b.message);
    const tref = asBranchRef(b.target, 'target');
    c.audit = { action: tref.kind === 'template' ? 'template.merged' : 'deployment.merged', targetType: tref.kind === 'template' ? 'template' : 'deployment', target: tref.kind === 'template' ? `templates/${tref.templateId}` : `deployments/${(tref as { deploymentId: string }).deploymentId}`, data: { source: (b.source as { branch?: unknown })?.branch ?? null } };
    const { p, r } = await mergeResult(c, b, 'write');
    if (r.errors.length) throw E.invalid(r.errors.map((e) => ({ path: e.key, code: 'V_MERGE_CHOICE', message: e.message, severity: 'error' as const })));
    if (r.unresolved.length) throw E.invalid(r.unresolved.map((k) => ({ path: k, code: 'V_MERGE_UNRESOLVED', message: `${k}: a conflict needs a choice (keep target, take source or a custom value)`, severity: 'error' as const })));
    if (!r.taken.length) throw E.bad('choices', 'V_NO_CHANGE', 'nothing picked: choose at least one change to merge');
    const now = nowIso();
    const by = c.account!.email ?? c.account!.uid;
    const src = p.source;
    const skipped = nextSkips(r.notTaken, p.base.skipped, src.commit?.seq ?? null, now, by);
    const mb: Omit<MergeBase, 'targetCommitId'> = { sourceKey: branchKey(src.r.ref), source: src.r.ref, sourceCommitId: src.commit?.commitId ?? null, sourceSeq: src.commit?.seq ?? null, base: markSecretValues(src.config), skipped, label: src.label, updatedAt: now, updatedBy: by };
    const source: CommitSource = { branch: src.r.ref, commitId: src.commit?.commitId ?? null, seq: src.commit?.seq ?? null, label: src.r.label, ...sourcePicks(r.taken, r.kind), skipped: r.notTaken.length };
    const { commit } = await commitTo(c, p.target, {
      kind: r.kind,
      message,
      config: r.config,
      source,
      sourceCatalog: src.commit?.catalog ?? null,
      mergeBase: mb,
      expectHead: p.target.head?.commitId ?? null,
      activity: r.kind === 'merge' ? 'branch.merged' : 'branch.cherry-picked',
      activitySummary: `${r.kind === 'merge' ? 'Merged' : 'Cherry-picked'} ${r.taken.length} change(s) from ${src.label} (seq ${(p.target.head?.seq ?? 0) + 1}): ${commitSummary(message)}`,
    });
    c.audit = { ...c.audit, action: tref.kind === 'template' ? 'template.merged' : 'deployment.merged', data: { kind: r.kind, source: branchKey(src.r.ref), sourceCommitId: src.commit?.commitId ?? null, commitId: commit.commitId, seq: commit.seq, taken: r.taken.length, skipped: r.notTaken.length, message: commitSummary(message) } };
    return commitView(commit);
  },
  async revertCommit(c) {
    const b = asObject(c.json());
    onlyKeys(b, ['branch', 'commitId', 'message', 'ifHead', 'choices']);
    const ref = asBranchRef(b.branch, 'branch');
    const message = needMessage(b.message);
    c.audit = { action: ref.kind === 'template' ? 'template.reverted' : 'deployment.reverted', targetType: ref.kind === 'template' ? 'template' : 'deployment', target: ref.kind === 'template' ? `templates/${ref.templateId}` : `deployments/${(ref as { deploymentId: string }).deploymentId}` };
    const t = await resolveBranch(c, ref, 'write');
    if (typeof b.commitId !== 'string') throw E.bad('commitId', 'V_REQUIRED', 'commitId is required');
    const cm = await loadCommit(ref, b.commitId, undefined, { human: true });
    if (!cm) throw E.notFound('commit not found');
    if (!cm.parentId) throw E.bad('commitId', 'V_REVERT', 'the first commit of a branch cannot be reverted (nothing before it) — use reset or edit');
    const parent = await loadCommit(ref, cm.parentId, undefined, { human: true });
    if ((b.ifHead ?? null) !== (t.head?.commitId ?? null)) throw E.stale('the branch moved — refresh', { head: t.head ? commitRef(t.head) : null });
    const opts = { secretValues: ref.kind !== 'template' };
    const r = applyRevert(cm.config, parent?.config ?? null, t.head!.config, asChoices(b.choices), opts);
    if (r.unresolved.length) throw E.invalid(r.unresolved.map((k) => ({ path: k, code: 'V_MERGE_UNRESOLVED', message: `${k}: a later commit changed this too — choose`, severity: 'error' as const })));
    void revertPlan;
    const { commit } = await commitTo(c, t, { kind: 'revert', message, config: r.config, revertOf: { commitId: cm.commitId, seq: cm.seq }, expectHead: t.head!.commitId, activity: 'branch.reverted', activitySummary: `Reverted seq ${cm.seq} (seq ${t.head!.seq + 1}): ${commitSummary(message)}` });
    c.audit = { ...c.audit, data: { revertOf: cm.commitId, commitId: commit.commitId, seq: commit.seq, message: commitSummary(message) } };
    return commitView(commit);
  },
  async resetToCommit(c) {
    const b = asObject(c.json());
    onlyKeys(b, ['branch', 'commitId', 'message', 'ifHead']);
    const ref = asBranchRef(b.branch, 'branch');
    const message = needMessage(b.message);
    c.audit = { action: ref.kind === 'template' ? 'template.reset' : 'deployment.reset', targetType: ref.kind === 'template' ? 'template' : 'deployment', target: ref.kind === 'template' ? `templates/${ref.templateId}` : `deployments/${(ref as { deploymentId: string }).deploymentId}` };
    const t = await resolveBranch(c, ref, 'write');
    if (typeof b.commitId !== 'string') throw E.bad('commitId', 'V_REQUIRED', 'commitId is required');
    const cm = await loadCommit(ref, b.commitId);
    if (!cm) throw E.notFound('commit not found');
    if ((b.ifHead ?? null) !== (t.head?.commitId ?? null)) throw E.stale('the branch moved — refresh', { head: t.head ? commitRef(t.head) : null });
    void resetPlan;
    const { commit } = await commitTo(c, t, { kind: 'reset', message, config: cm.config, resetTo: { commitId: cm.commitId, seq: cm.seq }, expectHead: t.head!.commitId, activity: 'branch.reset', activitySummary: `Reset to seq ${cm.seq} (seq ${t.head!.seq + 1}): ${commitSummary(message)}` });
    c.audit = { ...c.audit, data: { resetTo: cm.commitId, commitId: commit.commitId, seq: commit.seq, message: commitSummary(message) } };
    return commitView(commit);
  },
};

// ───────────────────────────────────────── create from (templates + deployments)
/** Resolve `from` of a create request (read permission); `templateId` is the legacy shortcut for a template head. */
export async function createSource(c: Ctx, from: unknown, templateId?: unknown): Promise<{ point: BranchPoint; config: Config; commit: Commit | null; label: string; r: Resolved } | null> {
  if (from !== undefined && from !== null) {
    const p = asPoint(from, 'from');
    const s = await resolvePoint(c, p).catch(async (e) => {
      // a pre-migration template without a commit: its working copy
      if (p.branch.kind === 'template' && p.at === 'head') {
        const r = await resolveBranch(c, p.branch, 'read');
        if (!r.head && r.tpl) return { r, config: templateConfig(r.tpl), commit: null, label: r.label };
      }
      throw e;
    });
    return { point: p, ...s };
  }
  if (typeof templateId === 'string' && templateId) {
    const p: BranchPoint = { branch: { kind: 'template', templateId }, at: 'head' };
    // §21 #105: the template config copy IS its head (no working copy) — the shortcut copies the head commit
    const r = await resolveBranch(c, p.branch, 'read');
    const config = r.head ? r.head.config : templateConfig(r.tpl!);
    return { point: p, r, config, commit: r.head, label: r.head ? pointLabel(r.label, r.head.seq) : r.label };
  }
  return null;
}
/** origin + source + merge base of a new branch created from `src` */
export function originOf(src: NonNullable<Awaited<ReturnType<typeof createSource>>>): { origin: BranchOrigin; source: CommitSource; mergeBase: Omit<MergeBase, 'targetCommitId'> } {
  const now = nowIso();
  const origin: BranchOrigin = { branch: src.r.ref, commitId: src.commit?.commitId ?? null, seq: src.commit?.seq ?? null, label: src.r.label, ...(src.r.tpl && !src.commit ? { templateVersion: src.r.tpl.version } : {}) };
  const source: CommitSource = { branch: src.r.ref, commitId: src.commit?.commitId ?? null, seq: src.commit?.seq ?? null, label: src.r.label, picks: 'all', skipped: 0 };
  const mergeBase: Omit<MergeBase, 'targetCommitId'> = { sourceKey: branchKey(src.r.ref), source: src.r.ref, sourceCommitId: src.commit?.commitId ?? null, sourceSeq: src.commit?.seq ?? null, base: markSecretValues(src.config), skipped: [], label: src.label, updatedAt: now, updatedBy: 'create' };
  return { origin, source, mergeBase };
}

/** Agent-facing deployment commit fetch: plain config + secret.fetched (_cloud). */
export async function agentCommit(deploymentId: string, siteSlug: string, commitId: string): Promise<Commit> {
  const cm = await loadCommit({ kind: 'deployment', deploymentId }, commitId);
  if (!cm) throw E.notFound('commit not found');
  const slugs = cm.config.secrets.filter((s) => s.value !== null && s.value !== undefined && s.value !== '').map((s) => s.secretSlug);
  if (cm.config.secrets.length) await appendCloud(deploymentId, [{ type: 'secret.fetched', summary: `Site ${siteSlug} fetched commit seq ${cm.seq} with ${slugs.length} secret value(s)`, data: { siteSlug, secretSlugs: slugs, commitId: cm.commitId }, actor: null }]);
  return cm;
}
export { decodeCommit, verifyCommitLink, commitSummaryOf, type CommitRef };
