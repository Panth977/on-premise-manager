/**
 * Template sync (SPEC-final §21 #94, §6.1 "Template sync"). A deployment is detached from its template: this is the
 * ONLY code path that reads a template for an existing deployment, and only when a person opens / applies the review.
 * 3-way: base = srcTemplate.baseSnapshot (template@baseVersion, stored in the deployment), theirs = the template now,
 * ours = the draft. Pure merge logic: shared/src/template-sync.ts.
 */
import {
  applyTemplateSync,
  countTemplateSync,
  diffConfigs,
  nextSkipRecords,
  planTemplateSync,
  templateConfig,
  templateSyncMissing,
  type Config,
  type SrcTemplate,
  type Template,
  type TemplateSyncApplyRequest,
  type TemplateSyncApplyResponse,
  type TemplateSyncChoice,
  type TemplateSyncPlan,
  type TemplateSyncPreviewResponse,
  type TemplateSyncStatus,
  type ValidationIssue,
  type VariableValue,
} from '@opm/shared';
import { col, db, nowIso } from '../core/fire.js';
import { E } from '../core/errors.js';
import { has, loadDep, needDep, targetOf } from '../core/perm.js';
import { asObject, clone, generateSecret, onlyKeys } from '../core/util.js';
import { decodeDeployment, decodeTemplate, draftField, srcTemplateFields, type StoredDeployment } from '../core/codec.js';
import { loadCatalog, loadDetectedHosts, validationCtx } from '../core/catalog.js';
import { CloudLog } from '../core/activities.js';
import { maskConfig, srcTemplateView } from '../core/views.js';
import { safeValidate } from '../core/config.js';
import type { Ctx, Handlers } from '../core/router.js';

async function readTemplate(id: string): Promise<Template | null> {
  const s = await col.templates().doc(id).get();
  return s.exists ? decodeTemplate(s.data()!) : null;
}
const uid = (c: Ctx) => c.account!.uid;

/** The base of a plan against templateId: the stored base of the link (also when relinking: the OLD base), else none. */
function baseOf(dep: StoredDeployment): Config | null {
  return dep.srcTemplate ? dep.srcTemplate.baseSnapshot : null;
}
function needSyncRead(c: Ctx, dep: StoredDeployment) {
  needDep(c, dep, 'deployment.config', 'read');
  if (!has(c, 'templates', 'read')) throw E.forbidden('needs templates.read (Template sync compares the deployment with its template)');
}

interface Body {
  ifRevision: number;
  templateId: string;
  templateVersion: number;
  choices: TemplateSyncChoice[];
  values: Record<string, VariableValue>;
}
function parseBody(c: Ctx): Body {
  const b = asObject(c.json());
  onlyKeys(b, ['ifRevision', 'templateId', 'templateVersion', 'choices', 'values']);
  const errs: ValidationIssue[] = [];
  const e = (path: string, code: string, message: string) => errs.push({ path, code, message, severity: 'error' });
  if (!Number.isInteger(b.ifRevision)) e('ifRevision', 'V_REQUIRED', 'ifRevision (the draftVersion you reviewed) is required');
  if (typeof b.templateId !== 'string' || !b.templateId) e('templateId', 'V_REQUIRED', 'templateId is required');
  if (!Number.isInteger(b.templateVersion)) e('templateVersion', 'V_REQUIRED', 'templateVersion (the template version you reviewed) is required');
  if (!Array.isArray(b.choices)) e('choices', 'V_TYPE', 'choices must be a list');
  else
    (b.choices as unknown[]).forEach((x, i) => {
      const o = x as Record<string, unknown>;
      if (!o || typeof o !== 'object' || typeof o.key !== 'string' || !['theirs', 'ours', 'custom'].includes(o.take as string)) e(`choices[${i}]`, 'V_TYPE', 'each choice is { key, take: theirs | ours | custom, value? }');
    });
  if (b.values !== undefined && (typeof b.values !== 'object' || b.values === null || Array.isArray(b.values))) e('values', 'V_TYPE', 'values must be an object { variableSlug: value }');
  if (errs.length) throw E.invalid(errs);
  return { ifRevision: b.ifRevision as number, templateId: b.templateId as string, templateVersion: b.templateVersion as number, choices: b.choices as TemplateSyncChoice[], values: (b.values as Record<string, VariableValue>) ?? {} };
}

/** Merge + checks shared by preview and apply (no writes). */
function merge(dep: StoredDeployment, tpl: Template, body: Body) {
  if (dep.draftVersion !== body.ifRevision) throw E.conflict(`the draft changed (draftVersion ${dep.draftVersion}) — refresh the review`, { current: dep.draftVersion });
  if (tpl.version !== body.templateVersion) throw E.conflict(`the template changed (version ${tpl.version}) — refresh the review`, { reason: 'template-changed', current: tpl.version });
  const relink = dep.srcTemplate?.templateId !== tpl.templateId;
  const skipped = relink ? [] : (dep.srcTemplate?.skipped ?? []);
  const r = applyTemplateSync(baseOf(dep), templateConfig(tpl), dep.draft, body.choices, { skipped, values: body.values });
  if (r.errors.length) throw E.invalid(r.errors.map((x) => ({ path: x.key, code: 'V_SYNC_CHOICE', message: `${x.key}: ${x.message}`, severity: 'error' as const })));
  // a secret the sync ADDS with a generate rule gets a value now (the create-time rule; values never come from templates)
  const now = nowIso();
  const before = new Set(dep.draft.secrets.map((s) => s.secretSlug));
  r.config.secrets = r.config.secrets.map((s) => (!before.has(s.secretSlug) && s.generate && (s.value === null || s.value === '') ? { ...s, value: generateSecret(s.generate.charset, s.generate.length), rotatedAt: now } : s));
  return { r, relink, skipped };
}

/** §21 #104: Template sync is replaced by Compare + cherry-pick between any two branches. */
const gone = async () => {
  throw E.gone('Template sync was replaced by branches (§21 #104): compare the deployment with its template and cherry-pick', { hint: 'POST /v1/branches/compare { source: { branch: { kind: "template", templateId }, at: "head" }, target: { kind: "deployment", deploymentId } }' });
};
export const templateSyncHandlers: Pick<Handlers, 'getTemplateSyncStatus' | 'getTemplateSyncPlan' | 'previewTemplateSync' | 'applyTemplateSync' | 'unlinkTemplate'> = {
  getTemplateSyncStatus: gone,
  getTemplateSyncPlan: gone,
  previewTemplateSync: gone,
  applyTemplateSync: gone,
  unlinkTemplate: gone,
};
