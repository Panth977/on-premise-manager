/** Deployments, drafts, diff, publish, proposals + revisions (SPEC-final §2.2, §6.1 Deployments / Diff / Publish). */

import {
  auditDiff,
  canSeeDeployment,
  diffConfigs,
  emptyConfig,
  LIMITS,
  normalizeConfig,
  RE,
  REVEAL_SECONDS,
  templateConfig,
  ulid,
  type Config,
  type CreateDeploymentRequest,
  type DeploymentSummary,
  type DiffResponse,
  type DraftSection,
  type PatchDeploymentRequest,
  type Proposal,
  type ProposalMeta,
  type RegenerateSecretResponse,
  type Revision,
  type RevisionSummary,
  type SecretDef,
  type SecretDefInput,
  type Site,
  type Template,
  type ValidationIssue,
} from '@opm/shared';
import { col, db, nowIso, type Tx } from '../core/fire.js';
import { E } from '../core/errors.js';
import { has, loadDep, need, needDep, targetOf } from '../core/perm.js';
import { asObject, clone, generateSecret, jcsHash, onlyKeys, V, vLabel } from '../core/util.js';
import { vIconFormat, vIconRefs } from '../core/icons.js';
import { decodeDeployment, decodeRevision, decodeTemplate, draftField, encodeDeployment, encodeRevision, humanRevision, type StoredDeployment } from '../core/codec.js';
import { buildRevisionCatalog, imageVolumes, isTerminal, loadCatalog, loadDetectedHosts, OPEN_STATUSES, validationCtx, withSiteImages, type Catalog } from '../core/catalog.js';
import { CloudLog, type CloudEntry } from '../core/activities.js';
import { newAccessKey } from '../core/accesskey.js';
import { deploymentView, revisionView } from '../core/views.js';
import { normFiles, normSecretDefs, normSites, normVariables, safeValidate } from '../core/config.js';
import { clampLimit } from './accounts.js';
import { seedBlobContents } from './volume-content.js';
import { commitRef, createMessage, makeCommit, type BranchState } from '@opm/shared';
import { loadCommit, newCommitId, personAuthor, prepCompat, setMergeBase, storeCommitBody, writeCommit, writeCompat } from '../core/branches.js';
import { createSource, originOf } from './branches.js';
import type { Ctx, Handlers } from '../core/router.js';
import { checkRequestedSlug, newSlug } from '../core/slugs.js';
import type { DocSnap } from '../driver/index.js';

const uid = (c: Ctx) => c.account!.uid;
export const proposalMeta = (p: Proposal): ProposalMeta => ({
  revId: p.revId,
  parentRevId: p.parentRevId,
  seq: p.seq,
  summary: p.summary,
  status: p.status,
  origin: p.origin,
  kind: p.kind,
  createdAt: p.createdAt,
  createdBy: p.createdBy,
});
export const revisionSummary = (r: Revision): RevisionSummary => ({
  revId: r.revId,
  seq: r.seq,
  parentRevId: r.parentRevId,
  origin: r.origin,
  actor: r.actor,
  createdAt: r.createdAt,
  hash: r.hash,
  summary: r.summary,
  kind: r.kind,
});

export async function loadRevision(deploymentId: string, revId: string, tx?: Tx): Promise<Revision | null> {
  const ref = col.revisions(deploymentId).doc(revId);
  const s = tx ? await tx.get(ref) : await ref.get();
  return s.exists ? decodeRevision(s.data()!) : null;
}
export async function openProposals(deploymentId: string, tx?: Tx): Promise<Proposal[]> {
  const q = col.proposals(deploymentId).where('status', 'in', [...OPEN_STATUSES]);
  const s = tx ? await tx.get(q) : await q.get();
  return s.docs.map((d) => d.data() as Proposal).sort((a, b) => (a.revId < b.revId ? 1 : -1));
}

/** Secrets of SecretDefInput items merged with the stored values (plainValue semantics, generateNow). */
export function mergeSecrets(items: SecretDefInput[], current: SecretDef[]): SecretDef[] {
  const now = nowIso();
  return items.map((it) => {
    const cur = current.find((s) => s.secretSlug === it.secretSlug);
    const { plainValue, generateNow, ...def } = it as SecretDefInput & Record<string, unknown>;
    delete (def as Record<string, unknown>).value;
    delete (def as Record<string, unknown>).rotatedAt;
    delete (def as Record<string, unknown>).isSet;
    const base = normSecretDefs([def])[0]!;
    let value: string | null;
    if (generateNow) {
      if (!base.generate) throw E.bad(`secrets[${it.secretSlug}].generateNow`, 'V_REQUIRED', 'generateNow needs a generate rule');
      value = generateSecret(base.generate.charset, base.generate.length);
    } else if (plainValue === undefined) value = cur ? cur.value : base.generate ? generateSecret(base.generate.charset, base.generate.length) : null;
    else if (plainValue === null) value = null;
    else if (typeof plainValue === 'string') value = plainValue;
    else throw E.bad(`secrets[${it.secretSlug}].plainValue`, 'V_TYPE', 'plainValue must be a string, null or absent');
    const changed = !cur || (cur.value ?? null) !== value;
    return { ...base, value, rotatedAt: changed ? (value === null ? null : now) : (cur?.rotatedAt ?? null) };
  });
}

function draftCheck(config: Config, deploymentId: string, cat: Catalog, detected?: Record<string, string>) {
  const res = safeValidate(config, validationCtx('draft', deploymentId, cat, undefined, detected));
  if (res.errors.length) throw E.invalid(res.errors);
  return { ok: true, issues: res.issues };
}

function slugMap(cat: Catalog, extra?: Record<string, { serviceSlug: string }>) {
  return (slug: string) => cat.services[slug]?.serviceSlug ?? extra?.[slug]?.serviceSlug ?? slug;
}

/** Blockers + warnings for publishing the draft (§6.1 Diff). */
export function publishCheck(dep: StoredDeployment, head: Pick<Revision, 'config' | 'catalog'> | null, cat: Catalog, detected?: Record<string, string>, blobContents?: Record<string, string>) {
  // §21 #101 blobContents: texts of starter-content entries with render 'expressions' (V_FILE_RENDER, publish level)
  const res = safeValidate(dep.draft, { ...validationCtx('publish', dep.deploymentId, cat, undefined, detected), ...(blobContents ? { blobContents } : {}) });
  const changes = diffConfigs(head?.config ?? null, dep.draft, slugMap(cat, head?.catalog.services), (slug) => {
    const a = head?.config.secrets.find((s) => s.secretSlug === slug)?.value ?? null;
    const b = dep.draft.secrets.find((s) => s.secretSlug === slug)?.value ?? null;
    return a !== b;
  });
  // §21 #105: no working copy — `head` here is the PARENT of the head commit, the diff = what the newest commit changed
  const blockers: ValidationIssue[] = [...res.errors];
  return { changes, blockers, warnings: res.warnings };
}

async function summaries(deps: StoredDeployment[]): Promise<DeploymentSummary[]> {
  return Promise.all(
    deps.map(async (d) => {
      const [agents, open] = await Promise.all([col.agents(d.deploymentId).select('lastSeenAt').get(), openProposals(d.deploymentId)]);
      const enrolled = agents.docs.map((a) => a.id);
      const draftSlugs = d.draft.sites.map((s) => s.siteSlug);
      const all = new Set([...draftSlugs, ...enrolled]);
      const online = agents.docs.filter((a) => Date.now() - Date.parse(a.data().lastSeenAt) < 90_000).length;
      const p = open[0];
      return {
        deploymentId: d.deploymentId,
        slug: d.slug,
        label: d.label,
        image: d.image,
        tagSlugs: d.tagSlugs,
        siteSlugs: draftSlugs,
        sitesOnline: online,
        sitesTotal: all.size,
        head: d.head ? { revId: d.head.revId, seq: d.head.seq } : null,
        pending: p ? { revId: p.revId, seq: p.seq, status: p.status } : null,
        updatedAt: d.updatedAt,
        branch: d.branch ?? null,
      };
    }),
  );
}

async function checkTagsManagers(v: V, tagSlugs: unknown, managers: unknown) {
  if (tagSlugs !== undefined) {
    if (!Array.isArray(tagSlugs) || tagSlugs.length > 20 || tagSlugs.some((t) => typeof t !== 'string')) v.err('tagSlugs', 'V_LENGTH', 'tagSlugs must be a list of ≤ 20 tag slugs');
    else {
      if (new Set(tagSlugs).size !== tagSlugs.length) v.err('tagSlugs', 'V_DUPLICATE', 'tagSlugs must be unique');
      if (tagSlugs.length) {
        const snaps = await db().getAll(...tagSlugs.map((t: string) => col.tags().doc(t)));
        snaps.forEach((s) => !s.exists && v.err('tagSlugs', 'V_REF', `tag "${s.id}" does not exist`));
      }
    }
  }
  if (managers !== undefined) {
    if (!Array.isArray(managers) || managers.length > 100 || managers.some((t) => typeof t !== 'string' || !t)) v.err('managerAccountUids', 'V_TYPE', 'managerAccountUids must be a list of account uids');
    else {
      if (new Set(managers).size !== managers.length) v.err('managerAccountUids', 'V_DUPLICATE', 'managerAccountUids must be unique');
      if (managers.length) {
        const snaps = await db().getAll(...managers.map((m: string) => col.accounts().doc(m)));
        snaps.forEach((s) => !s.exists && v.err('managerAccountUids', 'V_REF', `account "${s.id}" does not exist`));
      }
    }
  }
}

const SECTIONS: DraftSection[] = ['variables', 'secrets', 'files', 'sites'];

export const deploymentHandlers: Pick<
  Handlers,
  | 'listDeployments'
  | 'createDeployment'
  | 'getDeployment'
  | 'patchDeployment'
  | 'deleteDeployment'
  | 'putDraftSection'
  | 'putDraftSite'
  | 'revealSecret'
  | 'regenerateSecret'
  | 'getDiff'
  | 'publish'
  | 'resetDraftToHead'
  | 'listProposals'
  | 'listRevisions'
  | 'getRevision'
> = {
  async listDeployments(c) {
    const limit = clampLimit(c, 100, 500);
    const tag = c.query.get('tag');
    let q = col.deployments().where('deleted', '==', false);
    if (tag) q = q.where('tagSlugs', 'array-contains', tag);
    q = q.orderBy('updatedAt', 'desc');
    const cursor = c.query.get('cursor');
    let after: DocSnap | null = null;
    if (cursor) {
      after = await col.deployments().doc(cursor).get();
      if (!after.exists) throw E.bad('cursor', 'V_FORMAT', 'bad cursor');
    }
    const visible: StoredDeployment[] = [];
    let exhausted = false;
    for (let round = 0; round < 50 && visible.length <= limit && !exhausted; round++) {
      const page = await (after ? q.startAfter(after) : q).limit(200).get();
      if (page.docs.length < 200) exhausted = true;
      for (const d of page.docs) {
        after = d;
        const dep = decodeDeployment(d.data());
        if (canSeeDeployment(c.account, targetOf(dep))) visible.push(dep);
        if (visible.length > limit) break;
      }
    }
    const shown = visible.slice(0, limit);
    const items = await summaries(shown);
    return { items, cursor: visible.length > limit ? shown.at(-1)!.deploymentId : null };
  },
  async createDeployment(c) {
    const b = asObject(c.json());
    onlyKeys(b, ['label', 'slug', 'templateId', 'tagSlugs', 'managerAccountUids', 'image', 'from', 'message', 'copySecretValues']);
    const r = b as unknown as CreateDeploymentRequest;
    const v = new V();
    vLabel(v, r.label);
    vIconFormat(v, r.image, 'image', true);
    if (v.ok) await vIconRefs(v, [{ path: 'image', value: r.image }]);
    if (r.templateId !== null && r.templateId !== undefined && typeof r.templateId !== 'string') v.err('templateId', 'V_TYPE', 'templateId must be a string or null');
    if (r.tagSlugs === undefined) r.tagSlugs = [];
    if (r.managerAccountUids === undefined) r.managerAccountUids = [];
    await checkTagsManagers(v, r.tagSlugs, r.managerAccountUids);
    checkRequestedSlug(v, b.slug);
    v.throwIf();
    need(c, 'deployments', 'write', { deploymentId: '', tagSlugs: r.tagSlugs, managerAccountUids: [] });
    if (b.message !== undefined && (typeof b.message !== 'string' || !b.message.trim() || b.message.length > 2000)) throw E.bad('message', 'V_COMMIT_MESSAGE', 'message must be 1–2000 characters');
    // §21 #104 create from a template / deployment / agent branch (templateId = the legacy shortcut for a template head)
    const src = await createSource(c, b.from, r.templateId);
    const tpl: Template | null = src?.r.tpl ?? null;
    const ref = col.deployments().doc();
    const now = nowIso();
    const draft: Config = src ? clone(src.config) : emptyConfig();
    const fromDeployment = !!src && src.r.ref.kind !== 'template';
    const copyValues = fromDeployment && b.copySecretValues === true && has(c, 'deployment.secrets', 'read', targetOf(src!.r.dep!));
    if (fromDeployment && b.copySecretValues === true && !copyValues) throw E.forbidden('copySecretValues needs deployment.secrets.read on the source deployment');
    draft.secrets = draft.secrets.map((s) => (s.generate ? { ...s, value: generateSecret(s.generate.charset, s.generate.length), rotatedAt: now } : copyValues && s.value ? { ...s, rotatedAt: s.rotatedAt ?? now } : { ...s, value: null, rotatedAt: null }));
    const key = await newAccessKey();
    const dep: StoredDeployment = {
      deploymentId: ref.id,
      slug: '',
      label: r.label.trim(),
      image: r.image ?? null,
      tagSlugs: r.tagSlugs,
      managerAccountUids: r.managerAccountUids,
      // §21 #94 detached deep copy: the draft above + the snapshot / base below are copies; nothing reads the template later
      // §21 #104: the origin link lives in branch.origin (+ a merge base record); srcTemplate is legacy data (read by nothing new)
      srcTemplate: tpl ? { templateId: tpl.templateId, templateVersion: tpl.version, snapshot: { ...clone(tpl), ...clone(src!.config) }, copiedAt: now, baseVersion: tpl.version, baseSnapshot: clone(src!.config), lastSyncedAt: null, lastSyncedBy: null, skipped: [] } : null,
      draft,
      draftVersion: 0,
      head: null,
      accessKey: key.grouped, // §21 #84: stored (revealAccessKey); never in views / the agent cloud view
      accessKeyHash: key.hash,
      accessKeyVersion: 1,
      accessKeyRotatedAt: now,
      accessKeyRotatedBy: uid(c),
      agentEpoch: 1,
      cloudActivitySeq: 0,
      deleted: false,
      createdAt: now,
      createdBy: uid(c),
      updatedAt: now,
      updatedBy: uid(c),
      _cloudLastHash: null,
    };
    // §21 #104 the create commit (seq 1): "created from <label>@<seq>"
    const cat = await loadCatalog();
    const config = normalizeConfig(clone(draft), imageVolumes(cat));
    dep.draft = config;
    const catalog = await buildRevisionCatalog(config, cat);
    const o = src ? originOf(src) : null;
    const message = typeof b.message === 'string' && b.message.trim() ? b.message.trim() : src ? createMessage(src.r.label, src.commit?.seq ?? null) : 'created empty';
    const commit = makeCommit({ commitId: newCommitId(), branch: { kind: 'deployment', deploymentId: ref.id }, parent: null, kind: 'create', message, author: personAuthor(c), createdAt: now, config, catalog, source: o?.source ?? null });
    const external = await storeCommitBody(commit);
    const blocked = safeValidate(config, validationCtx('publish', ref.id, cat)).errors.length > 0;
    const branch: BranchState = { head: commitRef(commit), origin: o?.origin ?? null, commitCount: 1 };
    dep.branch = branch;
    await db().runTransaction(async (tx) => {
      dep.slug = await newSlug(tx, 'deployment', dep.label, b.slug); // §21 #106 (reads before writes)
      const prep = await prepCompat(tx, ref.id);
      const log = new CloudLog(ref.id, dep);
      log.add(tx, { type: 'deployment.created', summary: `Deployment "${dep.label}" created${src ? ` from ${src.label}` : ''}`, data: { label: dep.label, ...(tpl ? { templateId: tpl.templateId } : {}), ...(src ? { from: o!.source } : {}) }, actor: uid(c) });
      const compat = writeCompat(tx, dep, commit, prep, log, blocked, uid(c));
      commit.data = { ...commit.data, compat };
      await writeCommit(tx, commit, external);
      if (src) log.add(tx, { type: 'branch.created', summary: `Branch created from ${src.label}`, data: { branch: `deployment:${ref.id}`, commitId: commit.commitId, seq: 1, message: message.split('\n')[0], from: o!.source }, actor: uid(c) });
      if (o) setMergeBase(tx, { kind: 'deployment', deploymentId: ref.id }, { ...o.mergeBase, updatedBy: c.account!.email ?? uid(c), targetCommitId: commit.commitId });
      tx.create(ref, encodeDeployment({ ...dep, ...log.depUpdate(), agentEpoch: compat ? 2 : 1 }));
      Object.assign(dep, log.depUpdate(), { agentEpoch: compat ? 2 : 1 });
    });
    return { deployment: deploymentView(dep), accessKey: key.grouped, hideAfterSec: REVEAL_SECONDS };
  },
  async getDeployment(c) {
    const d = await loadDep(c);
    return deploymentView(d);
  },
  async patchDeployment(c) {
    const b = asObject(c.json());
    onlyKeys(b, ['label', 'image', 'tagSlugs', 'managerAccountUids'], ['deploymentId', 'slug', 'draft', 'draftVersion', 'head', 'agentEpoch', 'deleted', 'accessKeyVersion']);
    const p = b as PatchDeploymentRequest;
    const v = new V();
    if (p.label !== undefined) vLabel(v, p.label);
    vIconFormat(v, p.image, 'image', true);
    if (v.ok) await vIconRefs(v, [{ path: 'image', value: p.image }]);
    await checkTagsManagers(v, p.tagSlugs, p.managerAccountUids);
    v.throwIf();
    const cur = await loadDep(c);
    needDep(c, cur, 'deployments', 'write');
    if (p.tagSlugs !== undefined) need(c, 'deployments', 'write', { ...targetOf(cur), tagSlugs: p.tagSlugs });
    const ref = col.deployments().doc(cur.deploymentId);
    return db().runTransaction(async (tx) => {
      const s = await tx.get(ref);
      const dep = decodeDeployment(s.data()!);
      if (dep.deleted) throw E.notFound('deployment not found');
      const changed: string[] = [];
      const upd: Record<string, unknown> = {};
      for (const k of ['label', 'image', 'tagSlugs', 'managerAccountUids'] as const) {
        if (p[k] === undefined) continue;
        const val = k === 'label' ? (p.label as string).trim() : p[k];
        if (JSON.stringify(val) !== JSON.stringify(dep[k])) {
          changed.push(k);
          upd[k] = val;
        }
      }
      if (!changed.length) return deploymentView(dep);
      const log = new CloudLog(dep.deploymentId, dep);
      log.add(tx, { type: 'deployment.updated', summary: `Deployment updated (${changed.join(', ')})`, data: { changed }, actor: uid(c) });
      Object.assign(upd, log.depUpdate(), { updatedAt: nowIso(), updatedBy: uid(c) });
      tx.update(ref, upd);
      return deploymentView({ ...dep, ...(upd as Partial<StoredDeployment>) });
    });
  },
  async deleteDeployment(c) {
    const dep = await loadDep(c);
    needDep(c, dep, 'deployments', 'write');
    const b = asObject(c.json());
    if (b.confirm !== dep.label) throw E.bad('confirm', 'V_FORMAT', 'confirm must equal the deployment label');
    const ref = col.deployments().doc(dep.deploymentId);
    await db().runTransaction(async (tx) => {
      const cur = decodeDeployment((await tx.get(ref)).data()!);
      if (cur.deleted) throw E.notFound('deployment not found');
      const log = new CloudLog(cur.deploymentId, cur);
      log.add(tx, { type: 'deployment.deleted', summary: `Deployment "${cur.label}" deleted`, data: { label: cur.label }, actor: uid(c) });
      tx.update(ref, { deleted: true, agentEpoch: cur.agentEpoch + 1, ...log.depUpdate(), updatedAt: nowIso(), updatedBy: uid(c) });
    });
    return { deleted: true };
  },
  /** §21 #105 removed: no server working copy — the editors commit */
  async putDraftSection() {
    throw E.gone('the deployment config is changed only by a commit (§21 #105): there is no draft / working copy any more', { hint: 'POST /v1/deployments/{deploymentId}/commits { message, baseCommitId, config }' });
  },
  async putDraftSite() {
    throw E.gone('the deployment config is changed only by a commit (§21 #105): there is no draft / working copy any more', { hint: 'POST /v1/deployments/{deploymentId}/commits { message, baseCommitId, config }' });
  },
  async revealSecret(c) {
    const dep = await loadDep(c);
    needDep(c, dep, 'deployment.secrets', 'read');
    const slug = c.params.secretSlug!;
    const s = dep.draft.secrets.find((x) => x.secretSlug === slug);
    if (!s || s.value === null || s.value === undefined || s.value === '') throw E.notFound('the secret has no value');
    c.audit = { data: { secretSlug: slug } };
    // the activity is written in the same transaction that re-reads the value (no reveal without its log entry)
    const ref = col.deployments().doc(dep.deploymentId);
    const value = await db().runTransaction(async (tx) => {
      const cur = decodeDeployment((await tx.get(ref)).data()!);
      const sec = cur.draft.secrets.find((x) => x.secretSlug === slug);
      if (!sec || sec.value === null || sec.value === undefined || sec.value === '') throw E.notFound('the secret has no value');
      const log = new CloudLog(cur.deploymentId, cur);
      log.add(tx, { type: 'secret.revealed', summary: `Secret ${slug} revealed in the cloud`, data: { secretSlug: slug, actor: uid(c), where: 'cloud' }, actor: uid(c) });
      tx.update(ref, log.depUpdate());
      return sec.value;
    });
    return { value, hideAfterSec: REVEAL_SECONDS };
  },
  /** §21 #105 removed: "Generate new value" is a pending change (generateNow) written by the next commit */
  async regenerateSecret() {
    throw E.gone('regenerate is a pending change now (§21 #105): set generateNow on the secret and commit — the commit generates the value', { hint: 'POST /v1/deployments/{deploymentId}/commits { message, baseCommitId, config: { secrets: [{ …, generateNow: true }] } }' });
  },
  async getDiff(c) {
    const dep = await loadDep(c);
    needDep(c, dep, 'deployment.config', 'read');
    // §21 #105 (legacy endpoint): there is no working copy — the diff is what the HEAD commit changed vs its parent,
    // blockers / warnings = the head's publish-level validation (old clients see the current release)
    const ref = { kind: 'deployment' as const, deploymentId: dep.deploymentId };
    const head = dep.branch?.head ? await loadCommit(ref, dep.branch.head.commitId, undefined, { human: true }) : null;
    const parent = head?.parentId ? await loadCommit(ref, head.parentId, undefined, { human: true }) : null;
    const [cat0, detected, blobContents] = await Promise.all([loadCatalog(), loadDetectedHosts(dep.deploymentId), seedBlobContents(dep.draft)]);
    const cat = withSiteImages(cat0, head?.catalog); // §21 #113
    const chk = publishCheck(dep, parent ? { config: parent.config, catalog: parent.catalog ?? { services: {}, images: {}, notFoundPages: {} } } : null, cat, detected, blobContents);
    const res: DiffResponse = {
      head: head ? { revId: head.commitId, seq: head.seq, reportedAt: head.createdAt } : null,
      draftVersion: dep.draftVersion,
      changes: chk.changes,
      publishable: chk.blockers.length === 0,
      blockers: chk.blockers,
      warnings: chk.warnings,
      pending: [],
    };
    return res;
  },
  /** §21 #104: removed (commit the working copy instead) */
  async publish() {
    throw E.gone('publish was replaced by commits (§21 #104): commit your changes — the newest commit is what the sites run', { hint: 'POST /v1/deployments/{deploymentId}/commits { message, baseCommitId, config }' });
  },
  async resetDraftToHead() {
    throw E.gone('reset-to-head is gone (§21 #105): there is no working copy — pending changes live in the browser and are discarded there', { hint: 'discard in the browser' });
  },
  async listProposals(c) {
    const dep = await loadDep(c);
    needDep(c, dep, 'deployment.config', 'read');
    const limit = clampLimit(c, 20, 200);
    const q = await col.proposals(dep.deploymentId).orderBy('revId', 'desc').limit(limit).get();
    return { items: q.docs.map((d) => d.data() as Proposal) };
  },
  async listRevisions(c) {
    const dep = await loadDep(c);
    needDep(c, dep, 'deployment.config', 'read');
    const limit = clampLimit(c, 100, 500);
    let q = col.revisions(dep.deploymentId).orderBy('seq', 'desc').select('revId', 'seq', 'parentRevId', 'origin', 'actor', 'createdAt', 'hash', 'summary', 'kind');
    const cursor = c.query.get('cursor');
    if (cursor) {
      const s = await col.revisions(dep.deploymentId).doc(cursor).get();
      if (!s.exists) throw E.bad('cursor', 'V_FORMAT', 'bad cursor');
      q = q.startAfter(s);
    }
    const r = await q.limit(limit + 1).get();
    const docs = r.docs.slice(0, limit);
    return { items: docs.map((d) => d.data() as RevisionSummary), cursor: r.docs.length > limit ? docs.at(-1)!.id : null };
  },
  async getRevision(c) {
    const dep = await loadDep(c);
    needDep(c, dep, 'deployment.config', 'read');
    const r = await loadRevision(dep.deploymentId, c.params.revId!);
    if (!r) throw E.notFound('revision not found');
    return revisionView(humanRevision(r));
  },
};
export { isTerminal, RE };
