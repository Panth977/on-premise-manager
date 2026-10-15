/** Templates (SPEC-final §2.2 Template, §6.1 Templates). */
import { LIMITS, type CreateTemplateRequest, type PatchTemplateRequest, type Template, type TemplateSummary } from '@opm/shared';
import { col, db, nowIso } from '../core/fire.js';
import { E } from '../core/errors.js';
import { need } from '../core/perm.js';
import { asObject, onlyKeys, V, vDescription, vLabel } from '../core/util.js';
import { vIconFormat, vIconRefs } from '../core/icons.js';
import { decodeTemplate, encodeTemplate } from '../core/codec.js';
import { loadCatalog, validationCtx } from '../core/catalog.js';
import { safeValidate } from '../core/config.js';
import type { Handlers } from '../core/router.js';
import { commitRef, createMessage, makeCommit, type BranchState, type Config } from '@opm/shared';
import { newCommitId, personAuthor, setMergeBase, storeCommitBody, stripUncommitted, writeCommit } from '../core/branches.js';
import { createSource, originOf } from './branches.js';
import { clone } from '../core/util.js';
import { checkRequestedSlug, newSlug } from '../core/slugs.js';

async function loadTemplate(id: string): Promise<Template> {
  const s = await col.templates().doc(id).get();
  if (!s.exists) throw E.notFound('template not found');
  return decodeTemplate(s.data()!);
}

export const templateHandlers: Pick<Handlers, 'listTemplates' | 'createTemplate' | 'getTemplate' | 'patchTemplate' | 'deleteTemplate'> = {
  async listTemplates(c) {
    need(c, 'templates', 'read');
    const [q, deps] = await Promise.all([col.templates().get(), col.deployments().where('deleted', '==', false).select('srcTemplate', 'branch').get()]);
    const used = new Map<string, number>();
    for (const d of deps.docs) {
      const ob = (d.data().branch as { origin?: { branch?: { kind?: string; templateId?: string } } } | undefined)?.origin?.branch;
      const t = (ob?.kind === 'template' ? ob.templateId : undefined) ?? (d.data().srcTemplate as { templateId?: string } | null)?.templateId;
      if (t) used.set(t, (used.get(t) ?? 0) + 1);
    }
    const items: TemplateSummary[] = q.docs
      .map((d) => decodeTemplate(d.data()))
      .map((t) => ({
        templateId: t.templateId,
        slug: t.slug,
        label: t.label,
        icon: t.icon,
        description: t.description,
        version: t.version,
        updatedAt: t.updatedAt,
        siteSlugs: t.sites.map((s) => s.siteSlug),
        counts: { variables: t.variables.length, secrets: t.secrets.length, files: t.files.length, sites: t.sites.length },
        usedBy: used.get(t.templateId) ?? 0,
        branch: t.branch ? stripUncommitted(t.branch) : null,
      }))
      .sort((a, b) => a.label.localeCompare(b.label));
    return { items };
  },
  async createTemplate(c) {
    need(c, 'templates', 'write');
    const b = asObject(c.json());
    onlyKeys(b, ['label', 'slug', 'icon', 'description', 'from', 'message']);
    if (b.message !== undefined && (typeof b.message !== 'string' || !b.message.trim() || b.message.length > 2000)) throw E.bad('message', 'V_COMMIT_MESSAGE', 'message must be 1–2000 characters');
    const v = new V();
    vLabel(v, b.label);
    vIconFormat(v, b.icon, 'icon');
    vDescription(v, b.description);
    checkRequestedSlug(v, b.slug);
    if (v.ok) await vIconRefs(v, [{ path: 'icon', value: b.icon }]);
    v.throwIf();
    const r = b as unknown as CreateTemplateRequest;
    // §21 #104 create from a template / deployment / agent branch: deep copy, secret values dropped, generate rules kept
    const src = await createSource(c, b.from);
    const ref = col.templates().doc();
    const t = nowIso();
    const cfg: Config = src ? clone(src.config) : { variables: [], secrets: [], files: [], sites: [] };
    cfg.secrets = cfg.secrets.map((s) => ({ ...s, value: null, rotatedAt: null }));
    const o = src ? originOf(src) : null;
    const message = typeof r.message === 'string' && r.message.trim() ? r.message.trim() : src ? createMessage(src.r.label, src.commit?.seq ?? null) : 'created empty';
    const commit = makeCommit({ commitId: newCommitId(), branch: { kind: 'template', templateId: ref.id }, parent: null, kind: 'create', message, author: personAuthor(c), createdAt: t, config: cfg, source: o?.source ?? null });
    const external = await storeCommitBody(commit);
    const branch: BranchState = { head: commitRef(commit), origin: o?.origin ?? null, commitCount: 1 };
    const tpl: Template = {
      templateId: ref.id,
      slug: '',
      label: r.label.trim(),
      icon: r.icon ?? null,
      description: r.description ?? '',
      version: 1,
      ...cfg,
      createdAt: t,
      createdBy: c.account!.uid,
      updatedAt: t,
      updatedBy: c.account!.uid,
      branch,
    };
    await db().runTransaction(async (tx) => {
      tpl.slug = await newSlug(tx, 'template', tpl.label, b.slug); // §21 #106 (reads before writes)
      await writeCommit(tx, commit, external);
      if (o) setMergeBase(tx, { kind: 'template', templateId: ref.id }, { ...o.mergeBase, updatedBy: c.account!.email ?? c.account!.uid, targetCommitId: commit.commitId });
      tx.create(ref, encodeTemplate(tpl));
    });
    c.audit = { data: { from: o?.source ?? null, commitId: commit.commitId } };
    return tpl;
  },
  async getTemplate(c) {
    need(c, 'templates', 'read');
    return loadTemplate(c.params.templateId!);
  },
  async patchTemplate(c) {
    need(c, 'templates', 'write');
    const b = asObject(c.json());
    onlyKeys(b, ['ifVersion', 'label', 'icon', 'description', 'variables', 'secrets', 'files', 'sites'], ['templateId', 'slug', 'version', 'createdAt', 'createdBy']);
    const p = b as unknown as PatchTemplateRequest;
    const v = new V();
    if (!Number.isInteger(p.ifVersion)) v.err('ifVersion', 'V_REQUIRED', 'ifVersion is required');
    vLabel(v, p.label, 'label', LIMITS.label, false);
    vIconFormat(v, p.icon, 'icon');
    vDescription(v, p.description);
    if (v.ok) await vIconRefs(v, [{ path: 'icon', value: p.icon }]);
    v.throwIf();
    // §21 #105: the config changes only by a commit (no working copy)
    for (const k of ['variables', 'secrets', 'files', 'sites'] as const)
      if (b[k] !== undefined) throw E.gone(`the template config is changed only by a commit (§21 #105): ${k} cannot be patched`, { hint: 'POST /v1/templates/{templateId}/commits { message, baseCommitId, config }' });
    const cat = await loadCatalog();
    const ref = col.templates().doc(c.params.templateId!);
    return db().runTransaction(async (tx) => {
      const s = await tx.get(ref);
      if (!s.exists) throw E.notFound('template not found');
      const cur = decodeTemplate(s.data()!);
      if (cur.version !== p.ifVersion) throw E.conflict(`the template changed (version ${cur.version})`, { current: cur.version });
      const next: Template = {
        ...cur,
        label: p.label !== undefined ? p.label.trim() : cur.label,
        icon: p.icon !== undefined ? p.icon : (cur.icon ?? null),
        description: p.description ?? cur.description,
        version: cur.version + 1,
        updatedAt: nowIso(),
        updatedBy: c.account!.uid,
      };
      if (next.branch) next.branch = stripUncommitted(next.branch);
      const res = safeValidate({ variables: next.variables, secrets: next.secrets, files: next.files, sites: next.sites }, validationCtx('template', 'template', cat));
      tx.set(ref, encodeTemplate(next));
      return { template: next, validation: { ok: true, issues: res.issues } };
    });
  },
  async deleteTemplate(c) {
    need(c, 'templates', 'write');
    await loadTemplate(c.params.templateId!);
    await col.templates().doc(c.params.templateId!).delete();
    return { deleted: true };
  },
};
