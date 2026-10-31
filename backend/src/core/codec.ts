/**
 * Firestore codecs. Decision (TEAM.md): opaque JSON trees (configs, catalogs, template bodies, activity data)
 * are stored as JSON strings so that (a) Firestore's "no nested arrays" rule never rejects a json variable,
 * and (b) the bytes that were hashed (Revision.hash, Activity.hash) round-trip exactly. Ts stay ISO strings
 * (activity ts must stay byte-exact for the hash chain); internal sort keys are numbers (_tsMs).
 */
import type { Activity, Config, Deployment, Revision, SrcTemplate, Template, TemplateSyncSkip } from '@opm/shared';
import { emptyConfig, templateConfig } from '@opm/shared';
import { normalizeStored } from './config.js';
import { slugOf } from './slugs.js';

export type StoredDeployment = Deployment & { _cloudLastHash: string | null };

export function decodeDeployment(d: Record<string, unknown>): StoredDeployment {
  const { _draft, _srcSnapshot, _srcBase, _srcSkipped, srcTemplate, ...rest } = d as Record<string, unknown> & { _draft?: string; _srcSnapshot?: string; _srcBase?: string | null; _srcSkipped?: string | null; srcTemplate?: Record<string, unknown> | null };
  let src: SrcTemplate | null = null;
  if (srcTemplate) {
    const snapshot = _srcSnapshot ? normalizeStored(JSON.parse(_srcSnapshot) as Template) : (null as unknown as Template);
    const templateVersion = srcTemplate.templateVersion as number;
    // §21 #94: deployments created before Template sync have no base fields → base = the snapshot copied at create
    src = {
      templateId: srcTemplate.templateId as string,
      templateVersion,
      copiedAt: srcTemplate.copiedAt as string,
      snapshot,
      baseVersion: typeof srcTemplate.baseVersion === 'number' ? srcTemplate.baseVersion : templateVersion,
      baseSnapshot: _srcBase ? normalizeStored(JSON.parse(_srcBase) as Config) : snapshot ? templateConfig(snapshot) : emptyConfig(),
      lastSyncedAt: (srcTemplate.lastSyncedAt as string | null | undefined) ?? null,
      lastSyncedBy: (srcTemplate.lastSyncedBy as string | null | undefined) ?? null,
      skipped: _srcSkipped ? (JSON.parse(_srcSkipped) as TemplateSyncSkip[]) : [],
    };
  }
  return {
    ...(rest as unknown as Deployment),
    slug: slugOf(rest, 'deployment'), // §21 #106 (pre-migrate-k documents: derived, not stored)
    draft: _draft ? normalizeStored(JSON.parse(_draft) as Config) : emptyConfig(),
    srcTemplate: src,
    _cloudLastHash: (d._cloudLastHash as string | null) ?? null,
  };
}
export function encodeDeployment(d: StoredDeployment): Record<string, unknown> {
  const { draft, srcTemplate, ...rest } = d;
  return { ...rest, _draft: JSON.stringify(draft), ...srcTemplateFields(srcTemplate) };
}
/**
 * §21 #94 the stored form of Deployment.srcTemplate: a small map (label + versions, readable by listTemplates' usedBy)
 * + JSON strings for the snapshot (template at copy), the base (template config at baseVersion) and the skip records.
 */
export function srcTemplateFields(src: SrcTemplate | null): Record<string, unknown> {
  if (!src) return { srcTemplate: null, _srcSnapshot: null, _srcBase: null, _srcSkipped: null };
  return {
    srcTemplate: {
      templateId: src.templateId,
      templateVersion: src.templateVersion,
      copiedAt: src.copiedAt,
      label: src.snapshot?.label ?? '',
      baseVersion: src.baseVersion,
      lastSyncedAt: src.lastSyncedAt,
      lastSyncedBy: src.lastSyncedBy,
    },
    _srcSnapshot: JSON.stringify(src.snapshot),
    _srcBase: JSON.stringify(src.baseSnapshot),
    _srcSkipped: JSON.stringify(src.skipped ?? []),
  };
}
/** Partial update of the draft. */
export const draftField = (draft: Config) => ({ _draft: JSON.stringify(draft) });

export function decodeTemplate(d: Record<string, unknown>): Template {
  const { _body, _siteSlugs, ...rest } = d as Record<string, unknown> & { _body?: string };
  void _siteSlugs;
  const body = _body ? (JSON.parse(_body) as Pick<Template, 'variables' | 'secrets' | 'files' | 'sites'>) : { variables: [], secrets: [], files: [], sites: [] };
  return { ...(rest as unknown as Template), slug: slugOf(rest, 'template'), ...normalizeStored(body) };
}
export function encodeTemplate(t: Template): Record<string, unknown> {
  const { variables, secrets, files, sites, ...rest } = t;
  return { ...rest, _body: JSON.stringify({ variables, secrets, files, sites }), _siteSlugs: sites.map((s) => s.siteSlug) };
}

/**
 * Revisions are decoded byte-exact (the hash covers the config; agents get them unchanged). Human views (getRevision,
 * diff, usage, reset-to-head) use humanRevision() which normalizes legacy shapes (round C) — the stored hash is kept.
 */
export function humanRevision(r: Revision): Revision {
  return { ...r, config: normalizeStored(r.config) };
}
export function decodeRevision(d: Record<string, unknown>): Revision {
  const { _config, _catalog, ...rest } = d as Record<string, unknown> & { _config: string; _catalog: string };
  return { ...(rest as unknown as Revision), config: JSON.parse(_config), catalog: JSON.parse(_catalog) };
}
export function encodeRevision(r: Revision): Record<string, unknown> {
  const { config, catalog, ...rest } = r;
  return { ...rest, _config: JSON.stringify(config), _catalog: JSON.stringify(catalog) };
}

export function decodeActivity(d: Record<string, unknown>): Activity {
  const { _data, _tsMs, _typeKeys, ...rest } = d as Record<string, unknown>;
  void _tsMs;
  void _typeKeys;
  return { ...(rest as unknown as Activity), data: _data ? JSON.parse(_data as string) : {} };
}
/** "update.applied" → ["update.", "update.applied"] (exact or prefix filters with one array-contains). */
export function typeKeys(type: string): string[] {
  const parts = type.split('.');
  const out: string[] = [];
  for (let i = 1; i < parts.length; i++) out.push(parts.slice(0, i).join('.') + '.');
  out.push(type);
  return out;
}
export function encodeActivity(a: Activity): Record<string, unknown> {
  const { data, ...rest } = a;
  const ms = Date.parse(a.ts);
  return { ...rest, _data: JSON.stringify(data ?? {}), _tsMs: Number.isFinite(ms) ? ms : 0, _typeKeys: typeKeys(a.type) };
}
