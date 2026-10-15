/**
 * Root audit log (SPEC-final §6.2). One entry per human non-GET request outside /v1/deployments/{id}/… plus the deployment credential ops of
 * AUDIT_DEPLOYMENT_OPS (§21 #84) (success AND
 * failure), written by the router hook (auditBegin before the handler, auditFinish after it), plus system entries
 * (image import job, daily job). Handlers refine the entry through `c.audit` (action / target / targetType / data).
 *
 * Firestore layout of auditLog/{auditId}: the AuditLogEntry fields, except `changes` and `data`, which are stored as
 * JSON strings (`_changes`, `_data`; Firestore rejects nested arrays), plus `_actionKeys` (action + its prefixes, for
 * array-contains prefix filters). Values in `changes` / `data` are redacted (shared/src/audit.ts) before they are stored.
 */
import {
  AUDIT_DEPLOYMENT_OPS,
  AUDIT_EXEMPT_OPS,
  AUDIT_OPS,
  auditActionKeys,
  auditDiff,
  capAuditValue,
  redactSecrets,
  type AuditLogEntry,
  type AuditTargetType,
  type ErrorCode,
} from '@opm/shared';
import { logger } from '../ports/index.js';
import { col, db, nowIso, pub } from './fire.js';
import { ApiError } from './errors.js';
import { decodeDeployment, decodeTemplate } from './codec.js';
import type { Ctx, OpName } from './router.js';

/** What a handler may set on c.audit to refine the entry. */
export interface AuditHint {
  action?: string;
  target?: string;
  targetType?: AuditTargetType;
  data?: Record<string, unknown>;
  /** §21 #107: no entry at all (createUpload / completeUpload into a commit stage — the commit is the one entry) */
  skip?: boolean;
}
/** Error thrown by verifyHuman for an authenticated person without a usable account (403): still audited. */
export interface ActorOnError {
  auditActor?: { uid: string; email: string | null };
}

const DEPLOYMENT_INTERNAL = /^\/v1\/deployments\/[^/]+\/.+/;
const EXEMPT = new Set<string>(AUDIT_EXEMPT_OPS); // putUploadPart + dry runs (previewTemplateSync, §21 #104 compareBranches / previewMerge)
/** §21 #84: deployment-scoped credential ops are audited too (they keep writing their _cloud activity). */
const DEPLOYMENT_AUDITED = new Set<string>(AUDIT_DEPLOYMENT_OPS);

/** Human non-GET request outside the deployment scope (and not a per-part upload PUT), or a deployment credential op. */
export function isAudited(op: OpName, method: string, auth: string, path: string): boolean {
  if (auth !== 'human' || method === 'GET' || EXEMPT.has(op)) return false;
  return !DEPLOYMENT_INTERNAL.test(path) || DEPLOYMENT_AUDITED.has(op);
}

type Spec = { action: string; targetType: AuditTargetType };
const specOf = (op: string): Spec => (AUDIT_OPS as Record<string, Spec>)[op] ?? { action: `${op}`, targetType: 'system' };

function bodyOf(c: Ctx): Record<string, unknown> {
  if (c.def.body !== 'json') return {};
  try {
    const b = c.json();
    return b && typeof b === 'object' && !Array.isArray(b) ? (b as Record<string, unknown>) : {};
  } catch {
    return {};
  }
}
const str = (v: unknown): string | null => (typeof v === 'string' && v ? v : null);
const get = (o: unknown, ...keys: string[]): unknown => keys.reduce<unknown>((x, k) => (x && typeof x === 'object' ? (x as Record<string, unknown>)[k] : undefined), o);

/** Firestore document path of the target (null = no document, e.g. blobs / inspect). result is set after success. */
function docPathOf(op: string, c: Ctx, result: unknown): string | null {
  const p = c.params;
  const b = bodyOf(c);
  switch (op) {
    case 'createRegistry': {
      const id = str(get(result, 'registryId'));
      return id ? `registries/${id}` : null;
    }
    case 'patchRegistry':
    case 'deleteRegistry':
    case 'testRegistry':
    case 'revealRegistryPassword':
      return `registries/${p.registryId}`;
    case 'createService': {
      const id = str(get(result, 'serviceSlug'));
      return id ? `services/${id}` : null;
    }
    case 'patchService':
    case 'deleteService':
      return `services/${p.serviceSlug}`;
    case 'registerImage': {
      const version = str(get(result, 'version')) ?? str(b.version);
      return version && result ? `services/${p.serviceSlug}/images/${version}` : null;
    }
    case 'patchImage':
    case 'deleteImage':
    case 'retryImport':
      return `services/${p.serviceSlug}/images/${p.version}`;
    case 'createNotFoundPage': {
      const id = str(get(result, 'notFoundPageId'));
      return id ? `notFoundPages/${id}` : null;
    }
    case 'deleteNotFoundPage':
      return `notFoundPages/${p.notFoundPageId}`;
    case 'createTemplate': {
      const id = str(get(result, 'templateId'));
      return id ? `templates/${id}` : null;
    }
    case 'patchTemplate':
    case 'deleteTemplate':
    case 'commitTemplate':
    case 'discardTemplateWorking':
      return `templates/${p.templateId}`;
    case 'createTag': {
      const id = str(get(result, 'tagSlug'));
      return id ? `tags/${id}` : null;
    }
    case 'patchTag':
    case 'deleteTag':
      return `tags/${p.tagSlug}`;
    case 'createAccount': {
      const id = str(get(result, 'account', 'uid'));
      return id ? `accounts/${id}` : null;
    }
    case 'patchAccount':
    case 'disableAccount':
    case 'resetAccountPassword':
    case 'setAccountPassword':
    case 'revealAccountPassword':
    case 'revealAccountResetLink':
      return `accounts/${p.uid}`;
    case 'createDeployment': {
      const id = str(get(result, 'deployment', 'deploymentId'));
      return id ? `deployments/${id}` : null;
    }
    case 'patchDeployment':
    case 'deleteDeployment':
    case 'commitDeployment':
    case 'discardDeploymentWorking':
      return `deployments/${p.deploymentId}`;
    case 'createUpload': {
      const id = str(get(result, 'uploadId'));
      return id ? `uploads/${id}` : null;
    }
    case 'completeUpload':
      return `uploads/${p.uploadId}`;
    case 'uploadIcon': {
      // a dedupe hit (existing: true) changes nothing: no document diff (target set by the handler)
      const id = str(get(result, 'icon', 'iconId'));
      return id && get(result, 'existing') !== true ? `icons/${id}` : null;
    }
    case 'deleteIcon':
      return `icons/${p.iconId}`;
    // §21 #106 Admin → GitHub: one settings document (the token is masked by readAuditDoc)
    case 'patchGithubMirror':
    case 'testGithubMirror':
    case 'revealGithubToken':
    case 'syncGithubMirror':
    case 'resyncGithubMirror':
      return 'githubMirror/config';
    default:
      return null;
  }
}

/** Target path when there is no document (failed creates, blobs, inspect): the collection or a body-derived id. */
function fallbackTarget(op: string, c: Ctx, result: unknown, spec: Spec): string {
  const b = bodyOf(c);
  switch (op) {
    case 'inspectImage':
      return `services/${c.params.serviceSlug}/images`;
    case 'registerImage':
      return str(b.version) ? `services/${c.params.serviceSlug}/images/${b.version}` : `services/${c.params.serviceSlug}/images`;
    case 'uploadBlob': {
      const sha = str(get(result, 'sha256'));
      return sha ? `blobs/${sha}` : 'blobs';
    }
    case 'createTag':
      return str(b.tagSlug) ? `tags/${b.tagSlug}` : 'tags';
    case 'createAccount':
      return 'accounts';
    case 'createRegistry':
      return 'registries';
    case 'createService':
      return str(b.serviceSlug) ? `services/${b.serviceSlug}` : 'services';
    case 'createNotFoundPage':
      return 'notFoundPages';
    case 'createTemplate':
      return 'templates';
    case 'createDeployment':
      return 'deployments';
    case 'createUpload':
      return 'uploads';
    case 'uploadIcon':
      return 'icons';
    default:
      // deployment credential ops (§21 #84): target = the deployment, no document diff (values are never logged)
      if (DEPLOYMENT_AUDITED.has(op)) return `deployments/${c.params.deploymentId}`;
      return spec.targetType;
  }
}

/** Read a target document as the API sees it (codecs applied, internal "_" fields removed). */
export async function readAuditDoc(path: string): Promise<Record<string, unknown> | null> {
  const s = await db().doc(path).get();
  if (!s.exists) return null;
  const raw = s.data()!;
  const top = path.split('/')[0];
  if (top === 'githubMirror') {
    // §21 #106: the token is a credential — only "set / not set" reaches the audit diff; lastTest is noise
    const { token, lastTest, ...rest } = pub<Record<string, unknown>>(raw);
    void lastTest;
    return { ...rest, token: token ? '***' : null };
  }
  if (top === 'templates') return pub(decodeTemplate(raw) as unknown as Record<string, unknown>);
  if (top === 'deployments') {
    const { accessKey, ...d } = decodeDeployment(raw); // the plaintext key is never diffed (§21 #84)
    void accessKey;
    return pub(d as unknown as Record<string, unknown>);
  }
  return pub(raw);
}

/** Facts per op that are not in the target document (never secrets: data is redacted anyway). */
function defaultData(op: string, c: Ctx, result: unknown): Record<string, unknown> {
  const b = bodyOf(c);
  switch (op) {
    case 'testRegistry':
      return result ? { ok: get(result, 'ok'), message: get(result, 'message') } : {};
    case 'revealRegistryPassword':
      return { registryId: c.params.registryId };
    case 'inspectImage':
      return { reference: b.reference ?? null, registryId: b.registryId ?? null, platform: b.platform ?? 'linux/amd64', ...(result ? { digest: get(result, 'digest'), layers: get(result, 'layers'), sizeBytes: get(result, 'sizeBytes') } : {}) };
    case 'deleteTag':
      return result ? { removedFrom: get(result, 'removedFrom') } : {};
    case 'resetAccountPassword':
    case 'setAccountPassword':
    case 'revealAccountPassword':
    case 'revealAccountResetLink':
      return {}; // passwords / links are credentials: never logged (handlers add email etc.)
    // §21 #84 deployment credential ops: identifiers only (handlers refine: version / siteSlug / tokenHash / draftVersion)
    case 'revealAccessKey':
    case 'rotateAccessKey':
      return {};
    case 'createInstallToken':
      return typeof b.siteSlug === 'string' ? { siteSlug: b.siteSlug } : {};
    case 'revealInstallCommand':
      return { tokenHash: c.params.tokenHash };
    case 'revealUpgradeCommand':
    case 'regenerateUpgradeCommand':
      return { siteSlug: c.params.siteSlug };
    case 'revealSecret':
    case 'regenerateSecret':
      return { secretSlug: c.params.secretSlug };
    case 'uploadBlob':
      return result ? { sha256: get(result, 'sha256'), sizeBytes: get(result, 'sizeBytes') } : { sizeBytes: c.body.length };
    case 'createUpload':
      return { purpose: b.purpose ?? null, sizeBytes: b.sizeBytes ?? null, sha256: b.sha256 ?? null };
    case 'uploadIcon':
      return { contentType: c.headers['content-type'] ?? null, sizeBytes: c.body.length, name: c.query.get('name') };
    default:
      return {};
  }
}

export interface AuditPre {
  docPath: string | null;
  before: Record<string, unknown> | null;
}

/** Before the handler: read the target document (never throws). */
export async function auditBegin(c: Ctx): Promise<AuditPre> {
  const docPath = docPathOf(c.op, c, undefined);
  let before: Record<string, unknown> | null = null;
  if (docPath)
    try {
      before = await readAuditDoc(docPath);
    } catch (e) {
      logger.error('audit: before-read failed', { requestId: c.requestId, docPath, error: (e as Error).message });
    }
  return { docPath, before };
}

/** After the handler (success or failure): build + write the entry. Never throws. 401 (no actor) → nothing. */
export async function auditFinish(c: Ctx, pre: AuditPre | null, result: unknown, err: unknown, okStatus: number): Promise<void> {
  try {
    const actorErr = (err as ActorOnError | undefined)?.auditActor;
    const uid = c.account?.uid ?? actorErr?.uid ?? null;
    if (!uid) return; // unauthenticated: no actor
    const email = c.account?.email ?? actorErr?.email ?? null;
    const spec = specOf(c.op);
    const hint: AuditHint = c.audit ?? {};
    if (hint.skip) return;
    const ok = err === undefined;
    const status = ok ? okStatus : err instanceof ApiError ? err.status : 500;
    const errorCode: ErrorCode | null = ok ? null : err instanceof ApiError ? err.code : 'internal';
    const errorMessage = ok ? null : err instanceof ApiError ? err.message.slice(0, 500) : 'internal error';
    const docPath = ok && !pre?.docPath ? docPathOf(c.op, c, result) : (pre?.docPath ?? null);
    let before = pre?.before ?? null;
    let after: Record<string, unknown> | null = null;
    if (docPath && pre)
      try {
        after = await readAuditDoc(docPath);
      } catch (e) {
        logger.error('audit: after-read failed', { requestId: c.requestId, docPath, error: (e as Error).message });
        after = before;
      }
    if (!pre) before = after = null;
    const diff = auditDiff(before, after);
    let data: Record<string, unknown> = { ...defaultData(c.op, c, ok ? result : undefined), ...(hint.data ?? {}) };
    if (c.op === 'completeUpload' && (after ?? before)) {
      const u = (after ?? before)!;
      data = { partCount: u.partCount, purpose: u.purpose, sizeBytes: u.sizeBytes, sha256: u.sha256, deploymentId: u.deploymentId ?? null, transferId: u.transferId ?? null, ...data };
    }
    await writeAuditEntry({
      op: c.op,
      actor: uid,
      actorEmail: email,
      action: hint.action ?? spec.action,
      outcome: ok ? 'ok' : 'error',
      status,
      errorCode,
      errorMessage,
      targetType: hint.targetType ?? spec.targetType,
      target: hint.target ?? docPath ?? fallbackTarget(c.op, c, ok ? result : undefined, spec),
      requestId: c.requestId,
      ip: c.ip || null,
      userAgent: (c.headers['user-agent'] ?? '').slice(0, 300) || null,
      changes: diff.changes,
      changesTotal: diff.total,
      data,
    });
  } catch (e) {
    logger.error('audit: write failed', { requestId: c.requestId, op: c.op, error: (e as Error)?.stack ?? String(e) });
  }
}

/** Write one entry (data redacted + capped; changes must come from auditDiff). */
export async function writeAuditEntry(e: Omit<AuditLogEntry, 'auditId' | 'ts'>): Promise<AuditLogEntry> {
  const ref = col.auditLog().doc();
  const data = redactSecrets(e.data ?? {});
  for (const [k, v] of Object.entries(data)) data[k] = capAuditValue(v);
  const entry: AuditLogEntry = { auditId: ref.id, ts: nowIso(), ...e, data };
  await ref.create(encodeAudit(entry));
  return entry;
}

/** System entry (actor "system"), e.g. image import results and the daily job. Never throws. */
export async function auditSystem(o: { job: string; action: string; targetType: AuditTargetType; target: string; ok: boolean; errorMessage?: string | null; before?: Record<string, unknown> | null; after?: Record<string, unknown> | null; data?: Record<string, unknown> }) {
  try {
    const diff = o.before !== undefined || o.after !== undefined ? auditDiff(o.before ?? null, o.after ?? null) : { changes: [], total: 0 };
    await writeAuditEntry({
      op: `system:${o.job}`,
      actor: 'system',
      actorEmail: null,
      action: o.action,
      outcome: o.ok ? 'ok' : 'error',
      status: o.ok ? 200 : 500,
      errorCode: o.ok ? null : 'internal',
      errorMessage: o.ok ? null : (o.errorMessage ?? null)?.slice(0, 500) ?? null,
      targetType: o.targetType,
      target: o.target,
      requestId: null,
      ip: null,
      userAgent: null,
      changes: diff.changes,
      changesTotal: diff.total,
      data: o.data ?? {},
    });
  } catch (e) {
    logger.error('audit: system write failed', { job: o.job, action: o.action, error: (e as Error)?.message });
  }
}

export function encodeAudit(e: AuditLogEntry): Record<string, unknown> {
  const { changes, data, ...rest } = e;
  return { ...rest, _changes: JSON.stringify(changes ?? []), _data: JSON.stringify(data ?? {}), _actionKeys: auditActionKeys(e.action) };
}
/** Stored doc → entry (entries written before the catalog have `data` inline and lack the new fields). */
export function decodeAudit(d: Record<string, unknown>): AuditLogEntry {
  const { _changes, _data, _actionKeys, ...rest } = d as Record<string, unknown>;
  void _actionKeys;
  const r = rest as Partial<AuditLogEntry> & Record<string, unknown>;
  const changes = typeof _changes === 'string' ? JSON.parse(_changes) : [];
  return {
    auditId: r.auditId as string,
    ts: r.ts as string,
    op: (r.op as string) ?? '',
    actor: (r.actor as string) ?? '',
    actorEmail: (r.actorEmail as string | null) ?? null,
    action: (r.action as string) ?? '',
    outcome: (r.outcome as 'ok' | 'error') ?? 'ok',
    status: (r.status as number) ?? 200,
    errorCode: (r.errorCode as string | null) ?? null,
    errorMessage: (r.errorMessage as string | null) ?? null,
    targetType: (r.targetType as AuditTargetType) ?? ((r.target as string | undefined)?.startsWith('accounts/') ? 'account' : 'registry'),
    target: (r.target as string) ?? '',
    requestId: (r.requestId as string | null) ?? null,
    ip: (r.ip as string | null) ?? null,
    userAgent: (r.userAgent as string | null) ?? null,
    changes,
    changesTotal: (r.changesTotal as number) ?? changes.length,
    data: typeof _data === 'string' ? JSON.parse(_data) : ((r.data as Record<string, unknown>) ?? {}),
  };
}
