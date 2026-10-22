/**
 * Fake cloud API (VITE_MOCK=1): a `fetch` that implements every human endpoint of CLOUD_API on the in-memory
 * MockDb with the same rules as the backend (validation via @opm/shared, can() checks, ifVersion / ifRevision
 * conflicts, masking, audit + activities). Time-based simulations: image imports, proposal staging, key rollout.
 */
import {
  CLOUD_API,
  installTokenState,
  MASK,
  OpmApiError,
  ERROR_HTTP_STATUS,
  PROPOSAL_TERMINAL,
  allowed,
  canSeeDeployment,
  isAdmin,
  validateAccessRules,
  validateConfig,
  validateRegisterImage,
  validateImageSpec,
  imageNameChanges,
  imageNameUses,
  renameImageNames,
  diffConfigs,
  ulid,
  RE,
  LIMITS,
  type Account,
  type GuiPort,
  type InstallToken,
  type AgentDoc,
  type AccessTarget,
  type Action,
  type Activity,
  type Config,
  type ConfigView,
  type Deployment,
  type DeploymentView,
  type DiffResponse,
  type ErrorCode,
  type Image,
  type ImageImpactItem,
  type ImageInUseDetails,
  type ImageNameUse,
  type PatchImageRequest,
  type Proposal,
  type ProposalMeta,
  type Resource,
  type Revision,
  type SecretDef,
  type SecretDefInput,
  type Site,
  type Template,
  type Transfer,
  type SharedFile,
  FILE_MAX,
  FILE_NOTE_MAX,
  type ValidationContext,
  type ValidationIssue,
  type Variable,
  type FileDef,
  type ImageEnv,
  type EndpointDef,
  type AuditLogEntry,
  type AuditTargetType,
  AUDIT_OPS,
  CREDENTIAL_OPS,
  REVEAL_SECONDS,
  REVEAL_RATE_PER_MINUTE,
  type AccountDoc,
  auditDiff,
  auditActionMatches,
  redactSecrets,
  checkIcon,
  parseImageReference,
  matchRegistries,
  checkPicture,
  normalizeImageEnv,
  normalizeImageVolume,
  normalizeConfig,
  normalizeSiteConfig,
  ICON_CONTENT_TYPES,
  ICON_MAX_BYTES,
  Issues,
  SEED_ARCHIVE_MAX,
  type IconDoc,
  type IconUser,
  type RegistryRepository,
  type RegistryTag,
  supportsGuiUpgrade,
  agentUpgradeAvailable,
  GUI_UPGRADE_MIN_VERSION,
  AGENT_UPGRADE_REQUEST_TTL_SEC,
  type AgentUpgradeProgress,
  type AgentUpgradeRequest,
  type AgentUpgradeRun,
  type SiteAgentUpgradeView,
  planTemplateSync,
  applyTemplateSync,
  countTemplateSync,
  nextSkipRecords,
  templateSyncMissing,
  templateConfig,
  type SrcTemplate,
  type SrcTemplateView,
  type TemplateSyncApplyRequest,
  type TemplateSyncPlan,
  type TemplateSyncPreviewResponse,
  type TemplateSyncStatus,
  slugifyLabel,
  uniqueSlug,
  ENTITY_SLUG_RE,
} from '@opm/shared';
import { githubHandlers, ensureGithub } from './github';
import { ensureVcSeed, volumeContentHandlers } from './volume-content';
import { newConfigBlobs, STAGE_MAX_BLOBS, STAGE_TTL_MS, type BlobNeed } from '@opm/shared/staging';
import { ensureBranches, branchHandlers, branchHelpers, refreshTemplate, refreshDeployment, type BranchCtx } from './branches';
import { getDb, saveNow, now, autoId, randHex, fakeSha, genSecret, genAccessKey, b64, unb64, sha256Hex, memBlobs, pseudoHash, buildCatalog, type MockDb, type MockUpload, resetDb, ago, MOCK_AGENT_RELEASE } from './db';

// ───────────────────────────────────────────── plumbing
class Err extends Error {
  constructor(
    public code: ErrorCode,
    message: string,
    public details: Record<string, unknown> = {},
  ) {
    super(message);
  }
}
const invalid = (issues: ValidationIssue[], message = 'validation failed') => new Err('invalid', message, { errors: issues });
const issue = (path: string, code: string, message: string): ValidationIssue => ({ path, code, message, severity: 'error' });

interface Ctx {
  method: string;
  params: Record<string, string>;
  query: URLSearchParams;
  body: unknown;
  raw: Uint8Array | null;
  headers: Headers;
  me: Account;
  db: MockDb;
}
type Handler = (c: Ctx) => unknown | Promise<unknown>;

const routes: { op: string; method: string; re: RegExp; keys: string[] }[] = [];
for (const [op, def] of Object.entries(CLOUD_API)) {
  if (def.auth !== 'human') continue;
  const keys: string[] = [];
  const src = def.path.replace(/[.*+?^$()|[\]\\]/g, '\\$&').replace(/\\?\{([A-Za-z0-9_]+)\\?\}/g, (_m, k: string) => {
    keys.push(k);
    return '([^/]+)';
  });
  routes.push({ op, method: def.method, re: new RegExp(`^${src}$`), keys });
}

export function mockLogin(email: string, password: string): string | null {
  const db = getDb();
  const u = db.users.find((x) => x.email.toLowerCase() === email.trim().toLowerCase() && x.password === password);
  return u ? u.uid : null;
}
/**
 * §21 #108 the /reset-password page in mock mode: token `mock-reset:<email>` of a seeded user is valid (once per page
 * load); anything else is expired / invalid (like opm-cloud's 410).
 */
const usedResetTokens = new Set<string>();
export function mockResetCheck(token: string): string | null {
  if (usedResetTokens.has(token) || !token.startsWith('mock-reset:')) return null;
  const email = token.slice('mock-reset:'.length).toLowerCase();
  return getDb().users.find((x) => x.email.toLowerCase() === email)?.email ?? null;
}
export function mockResetConfirm(token: string, password: string): boolean {
  const email = mockResetCheck(token);
  const u = email ? getDb().users.find((x) => x.email === email) : null;
  if (!u) return false;
  u.password = password;
  usedResetTokens.add(token);
  saveNow();
  return true;
}
/** test hook: window.__opmMockReset() */
if (typeof window !== 'undefined') {
  (window as unknown as { __opmMockReset: () => void }).__opmMockReset = resetDb;
  /** test hook: call the fake API directly (e.g. a request the UI would not allow) */
  (window as unknown as { __opmMockFetch: typeof mockFetch }).__opmMockFetch = (i, init) => mockFetch(i, init);
  /** test hook: does this e-mail + password log in? (uid or null) */
  (window as unknown as { __opmMockLogin: typeof mockLogin }).__opmMockLogin = (e, p) => mockLogin(e, p);
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const latency = () => {
  try {
    const v = localStorage.getItem('opm.mock.latency');
    if (v !== null) return Number(v);
  } catch {
    /* ignore */
  }
  return 120 + Math.random() * 180;
};

export async function mockFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url);
  const method = (init?.method ?? 'GET').toUpperCase();
  const headers = new Headers(init?.headers);
  await sleep(latency());
  const reqId = typeof crypto !== 'undefined' && 'randomUUID' in crypto ? crypto.randomUUID() : randHex(16);
  // test hook: window.__opmMockWrites = the last 50 write requests { method, path, body } (raw JSON text, as sent)
  const sent = init?.body;
  if (method !== 'GET' && typeof window !== 'undefined' && headers.get('Content-Type') === 'application/json' && (typeof sent === 'string' || sent instanceof Uint8Array)) {
    const w = window as unknown as { __opmMockWrites?: { method: string; path: string; body: string }[] };
    (w.__opmMockWrites ??= []).push({ method, path: url.pathname, body: typeof sent === 'string' ? sent : new TextDecoder().decode(sent) });
    if (w.__opmMockWrites.length > 50) w.__opmMockWrites.shift();
  }
  let rec: AuditRec | null = null;
  try {
    const route = routes.find((r) => r.method === method && r.re.test(url.pathname));
    if (!route) {
      const any = routes.some((r) => r.re.test(url.pathname));
      throw new Err(any ? 'method-not-allowed' : 'not-found', any ? `method ${method} not allowed` : `no route ${method} ${url.pathname}`);
    }
    const m = route.re.exec(url.pathname)!;
    const params: Record<string, string> = {};
    route.keys.forEach((k, i) => (params[k] = decodeURIComponent(m[i + 1]!)));
    const db = getDb();
    await ensureVcSeed(db); // §21 #101 mock (volume-content.ts)
    ensureBranches(db, { buildCatalog }); // §21 #104 lazy migrate-i + seed scenario (branches.ts)
    ensureSlugs(db); // §21 #106 mock migrate-k
    ensureGithub(db); // §21 #106 GitHub mirror seed (github.ts)
    tick(db);
    tickFiles(db);
    const auth = headers.get('Authorization') ?? '';
    const uid = auth.startsWith('Bearer mock:') ? auth.slice('Bearer mock:'.length) : null;
    if (!uid) throw new Err('unauthenticated', 'missing or invalid ID token');
    const me = db.accounts[uid];
    if (!me) throw new Err('forbidden', 'no account');
    if (me.disabled) throw new Err('forbidden', 'account disabled');
    let raw: Uint8Array | null = null;
    let body: unknown = undefined;
    const def = CLOUD_API[route.op as keyof typeof CLOUD_API] as EndpointDef;
    if (init?.body !== undefined && init.body !== null) {
      raw = await toBytes(init.body);
      if (def.body === 'json') {
        try {
          body = JSON.parse(new TextDecoder().decode(raw));
        } catch {
          throw invalid([issue('', 'V_TYPE', 'body is not JSON')], 'body is not JSON');
        }
      }
    }
    const h = handlers[route.op];
    if (!h) throw new Err('internal', `mock has no handler for ${route.op}`);
    const c: Ctx = { method, params, query: url.searchParams, body, raw, headers, me, db };
    // §21 #107 uploads into a commit stage are part of the commit (its one audit entry): not audited
    const staged = (route.op === 'createUpload' && !!(body as { stageId?: unknown } | undefined)?.stageId) || (route.op === 'completeUpload' && uploadStage.has(params.uploadId ?? ''));
    if (route.op in AUDIT_OPS && !staged) rec = auditBegin(c, route.op as keyof typeof AUDIT_OPS, reqId);
    const credential = (CREDENTIAL_OPS as readonly string[]).includes(route.op);
    if (credential) rateLimit(me.uid);
    const out = await h(c);
    if (rec) auditEnd(rec, out, null);
    saveNow();
    if (out instanceof Response) return out;
    return new Response(JSON.stringify(out ?? {}), { status: def.ok ?? 200, headers: { 'Content-Type': 'application/json', 'X-Request-Id': reqId, 'X-OPM-Server-Time': String(Math.floor(serverNowMs() / 1000)), ...(credential ? { 'Cache-Control': 'no-store' } : {}) } });
  } catch (e) {
    const err = e instanceof Err ? e : e instanceof OpmApiError ? new Err(e.code as ErrorCode, e.message, e.details) : new Err('internal', (e as Error)?.message ?? 'internal', { requestId: reqId });
    if (!(e instanceof Err)) console.error('[mock]', e);
    if (rec) {
      auditEnd(rec, null, err);
      saveNow();
    }
    const retry: Record<string, string> = err.code === 'rate-limited' ? { 'Retry-After': String((err.details as { retryAfterSec?: number }).retryAfterSec ?? 60) } : {};
    return new Response(JSON.stringify({ error: { code: err.code, message: err.message, details: err.details } }), { status: ERROR_HTTP_STATUS[err.code] ?? 500, headers: { 'Content-Type': 'application/json', 'X-Request-Id': reqId, 'X-OPM-Server-Time': String(Math.floor(serverNowMs() / 1000)), ...retry } });
  }
}

/**
 * §21 #92 the mock "cloud clock" = the browser clock + localStorage['opm.mock.serverSkewMs'] (tests: a browser whose clock
 * is wrong, like the user's 5 h 30 min). Server-written times (lastSeenAt, serverTime, X-OPM-Server-Time) use it.
 */
function serverNowMs(): number {
  let skew = 0;
  try {
    skew = Number(localStorage.getItem('opm.mock.serverSkewMs') ?? 0) || 0;
  } catch {
    /* no storage */
  }
  return Date.now() + skew;
}
const serverNowIso = () => new Date(serverNowMs()).toISOString();

/**
 * §21 #84: REVEAL_RATE_PER_MINUTE value-returning requests (CREDENTIAL_OPS) per uid per fixed minute (like the backend
 * counter rateLimits/reveal_<uid>_<yyyymmddhhmm>); in memory only (a reload starts a fresh window).
 */
const revealCounts = new Map<string, number>();
function rateLimit(uid: string) {
  const t = Date.now();
  const minute = Math.floor(t / 60_000);
  const k = `${uid}_${minute}`;
  const n = (revealCounts.get(k) ?? 0) + 1;
  revealCounts.set(k, n);
  for (const key of revealCounts.keys()) if (!key.endsWith(`_${minute}`)) revealCounts.delete(key);
  if (n > REVEAL_RATE_PER_MINUTE) {
    const retryAfterSec = Math.max(1, Math.ceil(((minute + 1) * 60_000 - t) / 1000));
    throw new Err('rate-limited', `too many reveals: at most ${REVEAL_RATE_PER_MINUTE} per minute`, { retryAfterSec });
  }
}
/** test hook: window.__opmMockRevealReset() clears the reveal rate-limit counters */
if (typeof window !== 'undefined') (window as unknown as { __opmMockRevealReset: () => void }).__opmMockRevealReset = () => revealCounts.clear();

async function toBytes(b: BodyInit): Promise<Uint8Array> {
  if (b instanceof Uint8Array) return b;
  if (b instanceof ArrayBuffer) return new Uint8Array(b);
  if (typeof b === 'string') return new TextEncoder().encode(b);
  if (b instanceof Blob) return new Uint8Array(await b.arrayBuffer());
  if (ArrayBuffer.isView(b)) return new Uint8Array(b.buffer, b.byteOffset, b.byteLength);
  return new Uint8Array(await new Response(b).arrayBuffer());
}
const json = (v: unknown) => v;
const bytesResponse = (bytes: Uint8Array, name = 'download.bin', type = 'application/octet-stream') =>
  new Response(bytes as unknown as BodyInit, { status: 200, headers: { 'Content-Type': type, 'Content-Length': String(bytes.length), 'Content-Disposition': `attachment; filename="${name}"` } });

// ───────────────────────────────────────────── access
function need(c: Ctx, resource: Resource, action: Action, target: AccessTarget | null = null) {
  if (!allowed(c.me, resource, action, target)) throw new Err('forbidden', `you need ${resource}.${action}${target ? ` on ${target.deploymentId}` : ''}`);
}
const targetOf = (d: Deployment): AccessTarget => ({ deploymentId: d.deploymentId, tagSlugs: d.tagSlugs, managerAccountUids: d.managerAccountUids });
function dep(c: Ctx, id = c.params.deploymentId!): Deployment {
  const d = c.db.deployments[id];
  if (!d || d.deleted || !canSeeDeployment(c.me, targetOf(d))) throw new Err('not-found', `deployment ${id} not found`);
  return d;
}
function needDep(c: Ctx, d: Deployment, resource: Resource, action: Action) {
  need(c, resource, action, targetOf(d));
}
// ───────────────────────────────────────────── root audit log (like the backend router: one entry per audited op)
interface AuditRec {
  c: Ctx;
  op: keyof typeof AUDIT_OPS;
  reqId: string;
  target: string | null;
  before: unknown;
}
const COLLECTION: Record<AuditTargetType, string> = { registry: 'registries', service: 'services', image: 'images', notFoundPage: 'notFoundPages', template: 'templates', tag: 'tags', account: 'accounts', deployment: 'deployments', upload: 'uploads', blob: 'blobs', icon: 'icons', system: 'system', github: 'githubMirror' };
const str = (v: unknown) => (typeof v === 'string' && v ? v : null);
function auditTargetBefore(c: Ctx, op: keyof typeof AUDIT_OPS): string | null {
  const p = c.params;
  const b = (c.body && typeof c.body === 'object' ? c.body : {}) as Record<string, unknown>;
  switch (AUDIT_OPS[op].targetType) {
    case 'registry':
      return p.registryId ? `registries/${p.registryId}` : null;
    case 'service':
      return p.serviceSlug ? `services/${p.serviceSlug}` : str(b.serviceSlug) ? `services/${b.serviceSlug}` : null;
    case 'image': {
      const version = p.version ?? str(b.version);
      return version ? `services/${p.serviceSlug}/images/${version}` : null;
    }
    case 'notFoundPage':
      return p.notFoundPageId ? `notFoundPages/${p.notFoundPageId}` : null;
    case 'template':
      return p.templateId ? `templates/${p.templateId}` : null;
    case 'tag': {
      const slug = p.tagSlug ?? str(b.tagSlug);
      return slug ? `tags/${slug}` : null;
    }
    case 'account':
      return p.uid ? `accounts/${p.uid}` : null;
    case 'deployment':
      return p.deploymentId ? `deployments/${p.deploymentId}` : null;
    case 'upload':
      return p.uploadId ? `uploads/${p.uploadId}` : null;
    case 'icon':
      return p.iconId ? `icons/${p.iconId}` : null;
    case 'github':
      return 'githubMirror/config';
    default:
      return null;
  }
}
function auditTargetAfter(op: keyof typeof AUDIT_OPS, out: unknown): string | null {
  const o = (out && typeof out === 'object' ? out : {}) as Record<string, unknown>;
  const sub = (k: string) => (o[k] && typeof o[k] === 'object' ? (o[k] as Record<string, unknown>) : {});
  switch (op) {
    case 'createRegistry':
      return str(o.registryId) && `registries/${o.registryId}`;
    case 'createService':
      return str(o.serviceSlug) && `services/${o.serviceSlug}`;
    case 'createNotFoundPage':
      return str(o.notFoundPageId) && `notFoundPages/${o.notFoundPageId}`;
    case 'createTemplate':
      return str(o.templateId) && `templates/${o.templateId}`;
    case 'createAccount':
      return str(sub('account').uid) && `accounts/${sub('account').uid}`;
    case 'createDeployment':
      return str(sub('deployment').deploymentId) && `deployments/${sub('deployment').deploymentId}`;
    case 'uploadBlob':
      return str(o.sha256) && `blobs/${o.sha256}`;
    case 'createUpload':
      return str(o.uploadId) && `uploads/${o.uploadId}`;
    case 'uploadIcon':
      return str(sub('icon').iconId) && `icons/${sub('icon').iconId}`;
    default:
      return null;
  }
}
/** The stored document at an audit target path (a deep copy without mock-private fields), null when missing. */
function auditDoc(db: MockDb, target: string | null): unknown {
  if (!target) return null;
  const [kind, id, sub, tag] = target.split('/');
  let doc: unknown = null;
  if (kind === 'registries') doc = db.registries[id!];
  else if (kind === 'services' && sub === 'images') doc = db.images[id!]?.[tag!];
  else if (kind === 'services') doc = db.services[id!];
  else if (kind === 'notFoundPages') doc = db.notFoundPages[id!];
  else if (kind === 'templates') doc = db.templates[id!];
  else if (kind === 'tags') doc = db.tags[id!];
  else if (kind === 'accounts') doc = db.accounts[id!];
  else if (kind === 'deployments') doc = db.deployments[id!];
  else if (kind === 'uploads') doc = db.uploads[id!] ? uploadView(db.uploads[id!]!) : null;
  else if (kind === 'icons') doc = db.icons[id!] ? iconView(db.icons[id!]!) : null;
  else if (kind === 'githubMirror') {
    const g = (db as MockDb & { github?: { doc: Record<string, unknown> } }).github?.doc;
    // §21 #106 the token is never in the audit log: "***" when set (tokenSetAt shows a replacement)
    doc = g ? { ...g, token: g.token ? '***' : null, lastTest: null } : null;
  }
  if (!doc) return null;
  return strip(JSON.parse(JSON.stringify(doc)) as Record<string, unknown>);
}
function auditBegin(c: Ctx, op: keyof typeof AUDIT_OPS, reqId: string): AuditRec {
  const target = auditTargetBefore(c, op);
  return { c, op, reqId, target, before: auditDoc(c.db, target) };
}
function auditData(r: AuditRec, out: unknown): Record<string, unknown> {
  const o = (out && typeof out === 'object' ? out : {}) as Record<string, unknown>;
  const b = (r.c.body && typeof r.c.body === 'object' ? r.c.body : {}) as Record<string, unknown>;
  switch (r.op) {
    case 'testRegistry':
      return { ok: o.ok, message: o.message };
    case 'patchGithubMirror':
      return { tokenReplaced: typeof b.token === 'string', tokenRemoved: b.token === null, fields: Object.keys(b) };
    case 'testGithubMirror':
      return { ok: o.ok, failed: Array.isArray(o.checks) ? (o.checks as { ok: boolean; name: string }[]).filter((k) => !k.ok).map((k) => k.name) : [] };
    case 'syncGithubMirror':
    case 'resyncGithubMirror':
      return { gitBranch: b.gitBranch ?? null, enqueued: o.enqueued ?? null, branches: o.branches ?? null };
    case 'deleteTag':
      return { removedFrom: o.removedFrom };
    case 'completeUpload': {
      const u = (o.upload ?? {}) as Record<string, unknown>;
      return { partCount: u.partCount, purpose: u.purpose, transferId: u.transferId ?? null };
    }
    case 'createAccount':
      return { email: b.email, passwordSet: true };
    case 'revealSecret':
    case 'regenerateSecret':
      return { secretSlug: r.c.params.secretSlug };
    case 'createInstallToken':
      return { siteSlug: b.siteSlug, tokenHash: o.tokenHash ?? null, replaceActive: !!b.replaceActive };
    case 'revealInstallCommand':
      return { siteSlug: o.siteSlug ?? null, tokenHash: r.c.params.tokenHash };
    case 'revealUpgradeCommand':
      return { siteSlug: r.c.params.siteSlug, tokenHash: o.tokenHash ?? null, kind: o.kind ?? null };
    case 'regenerateUpgradeCommand':
      return { siteSlug: r.c.params.siteSlug, tokenHash: o.tokenHash ?? null, kind: 'maintenance', revoked: o._revoked ?? [] };
    case 'revealAccessKey':
    case 'rotateAccessKey':
      return { version: o.version ?? null };
    case 'resetAccountPassword':
    case 'revealAccountResetLink':
      return { expiresAt: o.expiresAt ?? null };
    case 'uploadBlob':
      return { sizeBytes: o.sizeBytes };
    case 'createUpload':
      return { purpose: b.purpose, sizeBytes: b.sizeBytes };
    case 'disableAccount':
      return { disabled: !!b.disabled };
    case 'createDeployment':
      return { templateId: b.templateId ?? null };
    case 'applyTemplateSync':
      return { templateId: b.templateId ?? null, templateVersion: b.templateVersion ?? null, taken: o.taken ?? null, skipped: o.skipped ?? null };
    case 'unlinkTemplate':
      return {};
    case 'uploadIcon': {
      const i = (o.icon ?? {}) as Record<string, unknown>;
      return { existing: o.existing, name: i.name, contentType: i.contentType, sizeBytes: i.sizeBytes };
    }
    case 'inspectImage':
      return { reference: b.reference, registryId: b.registryId ?? null };
    case 'requestAgentUpgrade':
      return { siteSlug: r.c.params.siteSlug, version: b.version ?? null, requestId: (o.request as { requestId?: string } | undefined)?.requestId ?? null };
    case 'cancelAgentUpgrade':
      return { siteSlug: r.c.params.siteSlug };
    case 'upgradeAllAgents':
    case 'cancelAgentUpgradeRun':
      return { version: (o.run as { version?: string } | undefined)?.version ?? b.version ?? null, runId: (o.run as { runId?: string } | undefined)?.runId ?? null };
    default:
      return {};
  }
}
function auditEnd(r: AuditRec, out: unknown, err: Err | null) {
  const { c, op } = r;
  const def = AUDIT_OPS[op];
  const target = r.target ?? (err ? null : auditTargetAfter(op, out)) ?? (def.targetType === 'image' ? `services/${c.params.serviceSlug}/images` : COLLECTION[def.targetType]);
  let action: string = def.action;
  const b = (c.body && typeof c.body === 'object' ? c.body : {}) as Record<string, unknown>;
  let changes: AuditLogEntry['changes'] = [];
  let changesTotal = 0;
  let after: unknown = null;
  if (!err) {
    after = def.targetType === 'blob' ? out : auditDoc(c.db, target);
    const d = auditDiff(r.before, after);
    changes = d.changes;
    changesTotal = d.total;
  }
  if (op === 'patchAccount') {
    const accessChanged = err ? b.access !== undefined : JSON.stringify((r.before as Account | null)?.access ?? null) !== JSON.stringify((after as Account | null)?.access ?? null);
    action = accessChanged ? 'account.access-changed' : 'account.updated';
  }
  if (op === 'disableAccount') action = b.disabled === false ? 'account.enabled' : 'account.disabled';
  c.db.auditLog.unshift({
    auditId: autoId(),
    ts: now(),
    op,
    actor: c.me.uid,
    actorEmail: c.me.email,
    action,
    outcome: err ? 'error' : 'ok',
    status: err ? (ERROR_HTTP_STATUS[err.code] ?? 500) : ((CLOUD_API[op] as EndpointDef).ok ?? 200),
    errorCode: err ? err.code : null,
    errorMessage: err ? err.message : null,
    targetType: def.targetType,
    target,
    requestId: r.reqId,
    ip: '127.0.0.1',
    userAgent: typeof navigator !== 'undefined' ? navigator.userAgent : null,
    changes,
    changesTotal,
    data: redactSecrets(auditData(r, err ? null : out)),
  });
}
/** A system job entry (actor "system"), e.g. the image import finishing. */
function systemAudit(db: MockDb, e: Pick<AuditLogEntry, 'op' | 'action' | 'targetType' | 'target' | 'data'> & { before: unknown; after: unknown; error?: string | null }) {
  const d = auditDiff(e.before, e.after);
  db.auditLog.unshift({ auditId: autoId(), ts: now(), op: e.op, actor: 'system', actorEmail: null, action: e.action, outcome: e.error ? 'error' : 'ok', status: e.error ? 500 : 200, errorCode: e.error ? 'internal' : null, errorMessage: e.error ?? null, targetType: e.targetType, target: e.target, requestId: null, ip: null, userAgent: null, changes: d.changes, changesTotal: d.total, data: redactSecrets(e.data) });
}
function cloudActivity(c: Ctx, d: Deployment, type: string, summary: string, data: Record<string, unknown>) {
  const list = (c.db.activities[d.deploymentId] ??= []);
  const prev = list.filter((a) => a.siteSlug === '_cloud').sort((a, b) => b.seq - a.seq)[0];
  d.cloudActivitySeq += 1;
  const a: Activity = { activityId: `_cloud:${d.cloudActivitySeq}`, deploymentId: d.deploymentId, siteSlug: '_cloud', seq: d.cloudActivitySeq, ts: now(), actor: c.me.uid, origin: 'cloud', type, summary, data, prevHash: prev?.hash ?? null, hash: fakeSha(), uploadedVia: null, uploadedAt: null };
  list.push(a);
}
function bodyOf<T>(c: Ctx): T {
  if (c.body === undefined || c.body === null || typeof c.body !== 'object') throw invalid([issue('', 'V_TYPE', 'body must be an object')]);
  return c.body as T;
}
function unknownFields(b: object, allowedKeys: string[]) {
  const bad = Object.keys(b).filter((k) => !allowedKeys.includes(k));
  if (bad.length) throw invalid(bad.map((k) => issue(k, 'V_TYPE', `unknown field ${k}`)));
}
function checkLabel(v: unknown, path: string, out: ValidationIssue[]) {
  if (typeof v !== 'string' || !v.trim()) out.push(issue(path, 'V_REQUIRED', 'required'));
  else if (v.length > LIMITS.label) out.push(issue(path, 'V_LENGTH', `at most ${LIMITS.label} characters`));
}
function throwIf(out: ValidationIssue[]) {
  if (out.length) throw invalid(out);
}
/** shared checkIcon / checkPicture → ValidationIssue[] */
function iconIssues(v: unknown, path: string, out: ValidationIssue[], picture = false) {
  const is = new Issues('publish');
  (picture ? checkPicture : checkIcon)(v, path, is);
  out.push(...is.errors);
}
const iconView = (i: MockDb['icons'][string]): IconDoc => {
  const { dataUrl: _d, ...rest } = i;
  void _d;
  return rest;
};
function iconUsers(db: MockDb, id: string): IconUser[] {
  const ref = `upload:${id}`;
  const out: IconUser[] = [];
  for (const s of Object.values(db.services)) if (s.icon === ref) out.push({ type: 'service', id: s.serviceSlug, label: s.label });
  for (const t of Object.values(db.templates)) if (t.icon === ref) out.push({ type: 'template', id: t.templateId, label: t.label });
  for (const t of Object.values(db.tags)) if (t.icon === ref) out.push({ type: 'tag', id: t.tagSlug, label: t.label });
  for (const d of Object.values(db.deployments)) if (!d.deleted && d.image === ref) out.push({ type: 'deployment', id: d.deploymentId, label: d.label });
  for (const a of Object.values(db.accounts)) if (a.img === ref) out.push({ type: 'account', id: a.uid, label: a.name });
  return out;
}
function needAnyWrite(c: Ctx) {
  if (!isAdmin(c.me) && !c.me.access.some((r) => r.actions.includes('write'))) throw new Err('forbidden', 'you need a write permission to manage icons');
}
// ── registry browsing data (fake catalog of a Docker Hub / Artifact Registry)
const DAYMS = 86_400_000;
function listableSource(url: string): 'docker-hub' | 'artifact-registry' | null {
  if (url.includes('docker.io')) return 'docker-hub';
  if (url.includes('pkg.dev')) return 'artifact-registry';
  return null;
}
const ago2 = (ms: number) => new Date(Date.now() - ms).toISOString();
function repoCatalog(source: 'docker-hub' | 'artifact-registry'): RegistryRepository[] {
  const r = (name: string, description: string | null, official: boolean, stars: number | null, pullCount: number | null, days: number): RegistryRepository => ({ name, description, official, stars, pullCount, updatedAt: ago2(days * DAYMS) });
  if (source === 'docker-hub')
    return [
      r('library/redis', 'Redis is an open source key-value store that functions as a data structure server.', true, 13000, 1_000_000_000, 3),
      r('library/postgres', 'The PostgreSQL object-relational database system provides reliability and data integrity.', true, 14000, 1_000_000_000, 5),
      r('library/nginx', 'Official build of Nginx.', true, 20000, 1_000_000_000, 4),
      r('library/node', 'Node.js is a JavaScript-based platform for server-side and networking applications.', true, 13500, 1_000_000_000, 6),
      r('acme/core-api', 'Core API', false, 3, 1200, 0.2),
      r('acme/hello-web', 'Hello web page', false, 1, 340, 9),
      r('northwind/northwind', 'Northwind images', false, 2, 5400, 2),
    ];
  return [r('example/opm-test/hello-web', null, false, null, null, 1), r('example/opm-test/hello-api', null, false, null, null, 1), r('example/opm-test/redis', null, false, null, null, 2)];
}
function tagCatalog(repo: string): RegistryTag[] | null {
  const D = DAYMS;
  const H = 3_600_000;
  const t = (tag: string, ms: number, size: number): RegistryTag => ({ tag, pushedAt: ago2(ms), digest: pseudoHash(repo + ':' + tag), sizeBytes: size });
  const m: Record<string, RegistryTag[]> = {
    'library/redis': [t('7.4.1', 3 * D, 45_000_000), t('7.4.1-alpine', 3 * D + 60_000, 17_000_000), t('7.4', 3 * D + 120_000, 45_000_000), t('alpine', 3 * D + 180_000, 17_000_000), t('latest', 3 * D + 240_000, 45_000_000), t('7.2.5', 40 * D, 43_000_000), t('7.2.5-alpine', 40 * D + 60_000, 16_000_000), t('7.0.15', 120 * D, 41_000_000)],
    'library/postgres': [t('17.2', 4 * D, 150_000_000), t('latest', 4 * D + 60_000, 150_000_000), t('16.6', 30 * D, 148_000_000), t('15.10', 60 * D, 140_000_000)],
    'library/nginx': [t('1.27.3', 5 * D, 70_000_000), t('1.27.3-alpine', 5 * D + 60_000, 20_000_000), t('latest', 5 * D + 120_000, 70_000_000), t('1.26.2', 50 * D, 68_000_000), t('stable', 50 * D + 60_000, 68_000_000)],
    'library/node': [t('22.12.0', 7 * D, 390_000_000), t('22-alpine', 7 * D + 60_000, 55_000_000), t('20.18.1', 25 * D, 380_000_000)],
    'acme/core-api': [t('v3.0.10', 4 * H, 88_000_000), t('v3.0.9', 2 * D, 87_000_000), t('v3.0.8', 6 * D, 87_000_000), t('v3.0.7', 20 * D, 86_000_000)],
    'acme/hello-web': [t('1.0.0', 9 * D, 9_000_000)],
    'northwind/northwind': [t('backend-3.0.10', 5 * H, 121_000_000), t('backend-3.0.9', 2 * D, 120_000_000), t('backend-3.0.8', 6 * D, 119_000_000)],
    'example/opm-test/hello-web': [t('1.2.0', 1 * D, 9_000_000), t('1.1.0', 8 * D, 9_000_000), t('1.0.0', 20 * D, 9_000_000)],
    'example/opm-test/hello-api': [t('1.2.0', 1 * D, 12_000_000), t('1.1.0', 8 * D, 12_000_000), t('1.0.0', 20 * D, 12_000_000)],
    'example/opm-test/redis': [t('7.4.1', 2 * D, 45_000_000), t('1.2.0', 3 * D, 45_000_000), t('1.1.0', 8 * D, 45_000_000), t('1.0.0', 20 * D, 45_000_000)],
  };
  const list = m[repo];
  return list ? [...list].sort((a, b) => (b.pushedAt ?? '').localeCompare(a.pushedAt ?? '')) : null;
}

// ───────────────────────────────────────────── views
const regView = (r: MockDb['registries'][string]) => ({ ...r, password: MASK, isSet: !!r.password });
function configView(c: Config): ConfigView {
  return { ...c, secrets: c.secrets.map((s) => ({ ...s, value: s.value ? MASK : null, isSet: !!s.value })) };
}
/** §21 #94 the stored link with defaults for documents written before Template sync (base = the copied snapshot). */
function linkOf(d: Deployment): SrcTemplate | null {
  const l = d.srcTemplate as (Partial<SrcTemplate> & Pick<SrcTemplate, 'templateId' | 'templateVersion' | 'snapshot' | 'copiedAt'>) | null;
  if (!l) return null;
  return {
    templateId: l.templateId,
    templateVersion: l.templateVersion,
    snapshot: l.snapshot,
    copiedAt: l.copiedAt,
    baseVersion: l.baseVersion ?? l.templateVersion,
    baseSnapshot: l.baseSnapshot ?? templateConfig(l.snapshot),
    lastSyncedAt: l.lastSyncedAt ?? null,
    lastSyncedBy: l.lastSyncedBy ?? null,
    skipped: l.skipped ?? [],
  };
}
function linkView(l: SrcTemplate): SrcTemplateView {
  return { templateId: l.templateId, templateVersion: l.templateVersion, copiedAt: l.copiedAt, label: l.snapshot.label, baseVersion: l.baseVersion, lastSyncedAt: l.lastSyncedAt, lastSyncedBy: l.lastSyncedBy, skippedCount: l.skipped.length };
}
function depView(d: Deployment): DeploymentView {
  const { accessKeyHash: _h, accessKey: _k, srcTemplate: _s, draft, ...rest } = d;
  void _h;
  void _k;
  void _s;
  const l = linkOf(d);
  return { ...rest, draft: configView(draft), srcTemplate: l ? linkView(l) : null };
}
const meta = (p: Proposal): ProposalMeta => ({ revId: p.revId, parentRevId: p.parentRevId, seq: p.seq, summary: p.summary, status: p.status, origin: p.origin, kind: p.kind, createdAt: p.createdAt, createdBy: p.createdBy });
const isOpen = (p: Proposal) => !PROPOSAL_TERMINAL.includes(p.status);

function vctx(db: MockDb, mode: ValidationContext['mode'], deploymentId: string): ValidationContext {
  const services: ValidationContext['services'] = {};
  const images: ValidationContext['images'] = {};
  for (const s of Object.values(db.services)) services[s.serviceSlug] = { label: s.label };
  for (const [sid, tags] of Object.entries(db.images))
    for (const img of Object.values(tags)) images[`${sid}:${img.version}`] = { kind: img.kind, ports: img.ports, env: img.env, volumes: img.volumes, healthcheck: img.healthcheck, importStatus: img.import.status };
  return { mode, deploymentId, services, images, notFoundPageIds: Object.keys(db.notFoundPages), detectedHosts: deploymentId === 'template' ? undefined : detectedHosts(db, deploymentId) };
}
/** siteSlug → advertised address of the enrolled sites of a deployment (auto hosts, SPEC §21 #51) */
function detectedHosts(db: MockDb, deploymentId: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [slug, a] of Object.entries(db.agents[deploymentId] ?? {})) if (a.advertiseHost) out[slug] = a.advertiseHost;
  return out;
}
/** images by `<serviceSlug>:<version>` (volumes) for normalizeConfig / normalizeSiteConfig */
function imageVolumes(db: MockDb): Record<string, Pick<Image, 'volumes'>> {
  const out: Record<string, Pick<Image, 'volumes'>> = {};
  for (const [sid, tags] of Object.entries(db.images)) for (const img of Object.values(tags)) out[`${sid}:${img.version}`] = { volumes: img.volumes };
  return out;
}

// ───────────────────────────────────────────── simulations
function tick(db: MockDb) {
  const t = Date.now();
  for (const tags of Object.values(db.images))
    for (const img of Object.values(tags)) {
      if (img.import.status === 'pending' && (img._copyAt ?? 0) <= t) {
        img.import.status = 'copying';
        img.import.startedAt = now();
      }
      if (img.import.status === 'copying' && (img._readyAt ?? 0) <= t) {
        const before = strip(JSON.parse(JSON.stringify(img)) as Record<string, unknown>);
        if (img._fail) {
          img.import.status = 'failed';
          img.import.error = `manifest unknown: ${img.reference} not found in the registry`;
        } else {
          const layers = [
            { digest: fakeSha(), sizeBytes: 2_000_000 + Math.floor(Math.random() * 9_000_000), mediaType: 'application/vnd.oci.image.layer.v1.tar+gzip' },
            { digest: fakeSha(), sizeBytes: 300_000 + Math.floor(Math.random() * 2_000_000), mediaType: 'application/vnd.oci.image.layer.v1.tar+gzip' },
          ];
          img.import = { ...img.import, status: 'ready', error: null, layers, sizeBytes: layers.reduce((a, l) => a + l.sizeBytes, 0), manifestMediaType: 'application/vnd.oci.image.manifest.v1+json', configDigest: fakeSha() };
          img.digest = fakeSha();
        }
        img.import.finishedAt = now();
        delete img._copyAt;
        delete img._readyAt;
        delete img._fail;
        const failed = img.import.status === 'failed';
        systemAudit(db, { op: 'system:importImage', action: failed ? 'image.import-failed' : 'image.import-ready', targetType: 'image', target: `services/${img.serviceSlug}/images/${img.version}`, data: { status: img.import.status, error: img.import.error, sizeBytes: img.import.sizeBytes }, before, after: strip(JSON.parse(JSON.stringify(img)) as Record<string, unknown>), error: failed ? img.import.error : null });
      }
    }
  for (const [depId, list] of Object.entries(db.proposals)) {
    const agents = db.agents[depId] ?? {};
    for (const p of list) {
      if (!p._sim || !isOpen(p) || p.status === 'approved' || p.status === 'applying') continue;
      const el = (t - p._sim) / 1000;
      if (el < 1.5) continue;
      let allReady = true;
      for (const [slug, st] of Object.entries(p.sites)) {
        if (!agents[slug]) {
          allReady = false;
          continue;
        }
        const prog = Math.min(1, (el - 1.5) / 6);
        st.progress = prog;
        st.staging = prog >= 1 ? 'ready' : 'downloading';
        st.updatedAt = now();
        if (prog < 1) allReady = false;
        if (agents[slug]) agents[slug]!.staging = { revId: p.revId, state: st.staging, progress: prog };
      }
      p.status = allReady ? 'ready' : 'staging';
      p.updatedAt = now();
    }
  }
  upgradeTick(db, t);
  for (const [depId, agents] of Object.entries(db.agents)) {
    const d = db.deployments[depId];
    for (const a of Object.values(agents)) {
      a.lastSeenAt = new Date(serverNowMs() - 4000 - Math.floor(Math.random() * 8000)).toISOString(); // §21 #92 server receive time
      a.online = true;
      const ka = (a as unknown as { _keyAt?: number })._keyAt;
      if (d && a.keyVersion < d.accessKeyVersion && ka && ka <= t) a.keyVersion = d.accessKeyVersion;
    }
  }
}

// ───────────────────────────────────────────── §21 #90 agent upgrade simulation (cloud request → agent heartbeat progress)
type MockAgent = AgentDoc & { _upgAt?: number; _upgFail?: 'pin' | 'download' | 'verify' | 'health' | null; _upgHold?: boolean; _upgSha?: string };
type MockRun = AgentUpgradeRun & { _uid?: string };
const UPGRADE_HINT = `Rerun the install command of this site to reach agent ≥ ${GUI_UPGRADE_MIN_VERSION}; later upgrades are one click here.`;
/** the agent picks a queued request up on its next poll (real: ≤ ~30 s; mock: 2 s) */
const PICK_MS = 2000;
/** phase schedule (ms after the agent started the upgrade) */
const PHASES: [AgentUpgradeProgress['phase'], number][] = [
  ['download', 0],
  ['verify', 3000],
  ['swap', 4000],
  ['restart', 4600],
  ['health', 5800],
  ['done', 7300],
];
const BIN_SIZE = 18_874_368;
const ACTIVE_PHASES = new Set(['queued', 'download', 'verify', 'swap', 'restart', 'health']);
export function upgradeView(a: AgentDoc | undefined): SiteAgentUpgradeView | null {
  if (!a) return null;
  const supported = supportsGuiUpgrade(a.agentVersion);
  return {
    installed: a.agentVersion,
    latest: MOCK_AGENT_RELEASE.version,
    available: agentUpgradeAvailable(a.agentVersion, MOCK_AGENT_RELEASE.version),
    supported,
    hint: supported ? null : UPGRADE_HINT,
    request: a.upgradeRequest ?? null,
    progress: a.upgrade ?? null,
  };
}
/** a site activity (origin agent, uploaded via pipe), chained after the site's last one */
function siteActivity(db: MockDb, depId: string, site: string, actor: string | null, type: string, summary: string, data: Record<string, unknown>) {
  const list = (db.activities[depId] ??= []);
  const prev = list.filter((x) => x.siteSlug === site).sort((x, y) => y.seq - x.seq)[0];
  const seq = (prev?.seq ?? 0) + 1;
  const ts = now();
  list.push({ activityId: `${site}:${seq}`, deploymentId: depId, siteSlug: site, seq, ts, actor, origin: 'agent', type, summary, data, prevHash: prev?.hash ?? null, hash: fakeSha(), uploadedVia: 'pipe', uploadedAt: ts });
}
function cloudActivityAs(db: MockDb, d: Deployment, actor: string | null, type: string, summary: string, data: Record<string, unknown>) {
  const list = (db.activities[d.deploymentId] ??= []);
  const prev = list.filter((a) => a.siteSlug === '_cloud').sort((a, b) => b.seq - a.seq)[0];
  d.cloudActivitySeq += 1;
  list.push({ activityId: `_cloud:${d.cloudActivitySeq}`, deploymentId: d.deploymentId, siteSlug: '_cloud', seq: d.cloudActivitySeq, ts: now(), actor, origin: 'cloud', type, summary, data, prevHash: prev?.hash ?? null, hash: fakeSha(), uploadedVia: null, uploadedAt: null });
}
function newUpgradeRequest(d: Deployment, a: AgentDoc, by: { email: string; uid: string }, runId: string | null): AgentUpgradeRequest {
  const t = Date.now();
  const req: AgentUpgradeRequest = { requestId: ulid(), siteSlug: a.siteSlug, version: MOCK_AGENT_RELEASE.version, requestedBy: by.email, requestedByUid: by.uid, requestedAt: new Date(t).toISOString(), expiresAt: new Date(t + AGENT_UPGRADE_REQUEST_TTL_SEC * 1000).toISOString(), runId };
  a.upgradeRequest = req;
  d.agentEpoch += 1;
  return req;
}
/** queue the next pending site of a running run; none left → the run is done */
function runNext(db: MockDb, d: Deployment) {
  const run = d.agentUpgradeRun as MockRun | null | undefined;
  if (!run || run.status !== 'running') return;
  if (run.sites.some((x) => x.status === 'requested' || x.status === 'running')) return;
  const next = run.sites.find((x) => x.status === 'pending');
  if (!next) {
    run.status = run.sites.some((x) => x.status === 'failed') ? 'failed' : 'done';
    run.finishedAt = now();
    return;
  }
  const a = db.agents[d.deploymentId]?.[next.siteSlug];
  if (!a) {
    next.status = 'skipped';
    next.error = 'not enrolled any more';
    next.at = now();
    return runNext(db, d);
  }
  const req = newUpgradeRequest(d, a, { email: run.requestedBy, uid: run._uid ?? '' }, run.runId);
  next.status = 'requested';
  next.requestId = req.requestId;
  next.from = a.agentVersion;
  next.at = now();
  cloudActivityAs(db, d, run._uid ?? null, 'agent.upgrade-requested', `Agent upgrade of ${a.siteSlug} to ${req.version} requested (upgrade all)`, { requestId: req.requestId, siteSlug: a.siteSlug, version: req.version, runId: run.runId });
}
function runSiteUpdate(db: MockDb, d: Deployment, p: AgentUpgradeProgress) {
  const run = d.agentUpgradeRun as MockRun | null | undefined;
  if (!run || !p.runId || run.runId !== p.runId) return;
  const e = run.sites.find((x) => x.requestId === p.requestId);
  if (!e) return;
  const prevStatus = e.status;
  if (ACTIVE_PHASES.has(p.phase)) e.status = 'running';
  else if (p.phase === 'done') e.status = 'done';
  else e.status = 'failed';
  e.error = p.error;
  if (prevStatus !== e.status) e.at = now();
  if (run.status !== 'running') return;
  if (e.status === 'failed') {
    // the first failure stops the run
    for (const x of run.sites) if (x.status === 'pending') x.status = 'cancelled';
    run.status = 'failed';
    run.finishedAt = now();
  } else if (e.status === 'done') runNext(db, d);
}
function finishUpgrade(db: MockDb, d: Deployment, a: MockAgent, phase: 'done' | 'failed' | 'rolled-back', error: string | null) {
  const p = a.upgrade!;
  const ts = now();
  p.failedPhase = phase === 'done' ? null : phase === 'rolled-back' ? 'health' : a._upgFail === 'pin' ? 'queued' : (a._upgFail ?? 'download');
  p.phase = phase;
  p.progress = null;
  p.error = error;
  p.updatedAt = ts;
  p.finishedAt = ts;
  delete a._upgAt;
  const durationSec = Math.round((Date.now() - Date.parse(p.startedAt)) / 1000);
  if (phase === 'done') {
    a.agentVersion = p.to;
    siteActivity(db, d.deploymentId, a.siteSlug, p.actor, 'agent.upgraded', `Agent upgraded to ${p.to}`, { upgradeId: p.upgradeId, from: p.from, to: p.to, durationSec, via: p.via });
  } else if (phase === 'rolled-back') {
    siteActivity(db, d.deploymentId, a.siteSlug, p.actor, 'agent.upgrade-failed', `Agent upgrade to ${p.to} failed in phase health`, { upgradeId: p.upgradeId, from: p.from, to: p.to, phase: 'health', error, via: p.via });
    siteActivity(db, d.deploymentId, a.siteSlug, p.actor, 'agent.rolled-back', `Agent rolled back to ${p.from}`, { upgradeId: p.upgradeId, from: p.to, to: p.from, reason: 'not healthy within 60 s after the restart', by: 'watcher' });
  } else {
    siteActivity(db, d.deploymentId, a.siteSlug, p.actor, 'agent.upgrade-failed', `Agent upgrade to ${p.to} failed`, { upgradeId: p.upgradeId, from: p.from, to: p.to, phase: a._upgFail ?? 'download', error, via: p.via });
  }
  // the heartbeat handler clears the request once its progress is terminal
  if (a.upgradeRequest && a.upgradeRequest.requestId === p.requestId) a.upgradeRequest = null;
  runSiteUpdate(db, d, p);
}
function upgradeTick(db: MockDb, t: number) {
  for (const [depId, agents] of Object.entries(db.agents)) {
    const d = db.deployments[depId];
    if (!d) continue;
    for (const a0 of Object.values(agents)) {
      const a = a0 as MockAgent;
      const req = a.upgradeRequest;
      if (req && Date.parse(req.expiresAt) <= t && a.upgrade?.requestId !== req.requestId) a.upgradeRequest = null;
      // the agent picks the request up on its next poll
      if (a.upgradeRequest && a.upgrade?.requestId !== a.upgradeRequest.requestId && !a._upgHold && t >= Date.parse(a.upgradeRequest.requestedAt) + PICK_MS) {
        const r = a.upgradeRequest;
        const ts = now();
        a.upgrade = { upgradeId: ulid(), requestId: r.requestId, runId: r.runId, via: 'cloud', siteSlug: a.siteSlug, from: a.agentVersion, to: r.version, phase: 'download', failedPhase: null, progress: 0, bytesDone: 0, bytesTotal: BIN_SIZE, verification: null, error: null, actor: r.requestedBy, startedAt: ts, updatedAt: ts, finishedAt: null };
        a._upgAt = t;
        a._upgSha = fakeSha();
        if (r.version !== MOCK_AGENT_RELEASE.version || a._upgFail === 'pin') {
          finishUpgrade(db, d, a, 'failed', `the pinned release moved to ${MOCK_AGENT_RELEASE.version}; request again`);
          continue;
        }
        siteActivity(db, depId, a.siteSlug, r.requestedBy, 'agent.upgrade-started', `Agent upgrade ${a.agentVersion} → ${r.version} started`, { upgradeId: a.upgrade.upgradeId, from: a.agentVersion, to: r.version, via: 'cloud', requestId: r.requestId, ...(r.runId ? { runId: r.runId } : {}) });
        runSiteUpdate(db, d, a.upgrade);
      }
      const p = a.upgrade;
      if (!p || !ACTIVE_PHASES.has(p.phase) || a._upgAt === undefined) continue;
      const el = t - a._upgAt;
      let phase: AgentUpgradeProgress['phase'] = 'download';
      for (const [ph, at] of PHASES) if (el >= at) phase = ph;
      if (a._upgFail === 'download' && el >= 1500) {
        p.bytesDone = Math.round(BIN_SIZE * 0.5);
        finishUpgrade(db, d, a, 'failed', 'download: connection reset by peer (3 retries) — the download resumes when you request again');
        continue;
      }
      if (phase === 'download') {
        const f = Math.min(1, el / 3000);
        p.progress = f;
        p.bytesDone = Math.round(BIN_SIZE * f);
      } else {
        p.progress = null;
        p.bytesDone = BIN_SIZE;
        p.verification = {
          version: p.to,
          sha256: a._upgSha ?? fakeSha(),
          signatureOk: a._upgFail !== 'verify',
          publicKeySource: 'agent.json',
          publicKeyFingerprint: 'SHA256:q3Jd0mVb7TQ9yYH1m3oP2r8sXcKf4wLzA5eGnBiUu0E',
          sha256Ok: a._upgFail === 'verify' ? null : el >= 3400 ? true : null,
          selfTestOk: a._upgFail === 'verify' ? null : el >= 3800 ? true : null,
          error: a._upgFail === 'verify' ? 'release signature does not verify with the release key from agent.json' : null,
          checkedAt: p.verification?.checkedAt ?? now(),
        };
        if (a._upgFail === 'verify') {
          p.phase = 'verify';
          finishUpgrade(db, d, a, 'failed', 'verify: release signature does not verify with the release key from agent.json — refusing the binary');
          continue;
        }
      }
      if (phase === 'done') {
        if (a._upgFail === 'health') finishUpgrade(db, d, a, 'rolled-back', `the new agent did not report healthy within 60 s — restored ${p.from} (watcher)`);
        else finishUpgrade(db, d, a, 'done', null);
        continue;
      }
      if (p.phase !== phase) p.updatedAt = now();
      p.phase = phase;
    }
  }
}
/** test hook: window.__opmMockAgent(deploymentId, siteSlug, patch) — patch a mock agent doc, e.g.
 *  { agentVersion: '0.9.0' }, { _upgFail: 'download' | 'verify' | 'health' | 'pin' | null }, { _upgHold: true } (never picks up) */
if (typeof window !== 'undefined')
  (window as unknown as { __opmMockAgent: (dep: string, site: string, patch: Record<string, unknown>) => boolean }).__opmMockAgent = (dep, site, patch) => {
    const a = getDb().agents[dep]?.[site];
    if (!a) return false;
    Object.assign(a, patch);
    saveNow();
    return true;
  };

// ───────────────────────────────────────────── usage
type Usage = { kind: 'deployment' | 'template'; id: string; label: string; siteSlug: string; containerSlug: string; serviceSlug: string; version: string };
function usage(db: MockDb, pred: (serviceSlug: string, version: string) => boolean): Usage[] {
  const out: Usage[] = [];
  const seen = new Set<string>();
  const addFrom = (kind: Usage['kind'], id: string, label: string, sites: Site[]) => {
    for (const s of sites)
      for (const c of s.config.containers)
        if (pred(c.serviceSlug, c.version)) {
          const k = `${kind}|${id}|${s.siteSlug}|${c.containerSlug}|${c.version}`;
          if (seen.has(k)) continue;
          seen.add(k);
          out.push({ kind, id, label, siteSlug: s.siteSlug, containerSlug: c.containerSlug, serviceSlug: c.serviceSlug, version: c.version });
        }
  };
  for (const t of Object.values(db.templates)) addFrom('template', t.templateId, t.label, t.sites);
  for (const d of Object.values(db.deployments)) {
    if (d.deleted) continue;
    addFrom('deployment', d.deploymentId, d.label, d.draft.sites);
    const head = d.head ? db.revisions[d.deploymentId]?.find((r) => r.revId === d.head!.revId) : null;
    if (head) addFrom('deployment', d.deploymentId, d.label, head.config.sites);
  }
  return out;
}
function pageUsage(db: MockDb, pageId: string): Usage[] {
  const out: Usage[] = [];
  for (const t of Object.values(db.templates)) for (const s of t.sites) if (s.config.notFoundPageId === pageId) out.push({ kind: 'template', id: t.templateId, label: t.label, siteSlug: s.siteSlug, containerSlug: '', serviceSlug: '', version: '' });
  for (const d of Object.values(db.deployments)) {
    if (d.deleted) continue;
    for (const s of d.draft.sites) if (s.config.notFoundPageId === pageId) out.push({ kind: 'deployment', id: d.deploymentId, label: d.label, siteSlug: s.siteSlug, containerSlug: '', serviceSlug: '', version: '' });
  }
  return out;
}

// ───────────────────────────────────────────── handlers
const handlers: Record<string, Handler> = {
  getMe: (c) => {
    c.me.lastLoginAt = now();
    return { account: c.me, isAdmin: isAdmin(c.me) };
  },

  // registries
  listRegistries: (c) => {
    need(c, 'registries', 'read');
    return { items: Object.values(c.db.registries).sort((a, b) => a.label.localeCompare(b.label)).map(regView) };
  },
  createRegistry: (c) => {
    need(c, 'registries', 'write');
    const b = bodyOf<{ label: string; url?: string; username: string; password: string }>(c);
    unknownFields(b, ['label', 'url', 'username', 'password']);
    const out: ValidationIssue[] = [];
    checkLabel(b.label, 'label', out);
    if (b.url !== undefined && b.url !== '' && !RE.httpsUrl.test(b.url)) out.push(issue('url', 'V_FORMAT', 'url must be https://host[:port] without a path'));
    const anon = !b.username && !b.password;
    if (!anon && !b.username) out.push(issue('username', 'V_REQUIRED', 'required (or leave both username and password empty for anonymous access)'));
    if (!anon && !b.password) out.push(issue('password', 'V_REQUIRED', 'required (or leave both username and password empty for anonymous access)'));
    throwIf(out);
    const id = autoId();
    const t = now();
    c.db.registries[id] = { registryId: id, label: b.label, url: b.url || 'https://registry-1.docker.io', username: b.username ?? '', password: b.password ?? '', lastCheck: null, createdAt: t, createdBy: c.me.uid, updatedAt: t, updatedBy: c.me.uid };
    return regView(c.db.registries[id]!);
  },
  getRegistry: (c) => {
    need(c, 'registries', 'read');
    const r = c.db.registries[c.params.registryId!];
    if (!r) throw new Err('not-found', 'registry not found');
    return regView(r);
  },
  patchRegistry: (c) => {
    need(c, 'registries', 'write');
    const r = c.db.registries[c.params.registryId!];
    if (!r) throw new Err('not-found', 'registry not found');
    const b = bodyOf<{ label?: string; url?: string; username?: string; password?: string }>(c);
    unknownFields(b, ['label', 'url', 'username', 'password']);
    const out: ValidationIssue[] = [];
    if (b.label !== undefined) checkLabel(b.label, 'label', out);
    if (b.url !== undefined && !RE.httpsUrl.test(b.url)) out.push(issue('url', 'V_FORMAT', 'url must be https://host[:port] without a path'));
    const nextUser = b.username ?? r.username;
    const nextPass = b.password ?? r.password;
    if (nextUser && !nextPass) out.push(issue('password', 'V_REQUIRED', 'required when a username is set'));
    if (!nextUser && nextPass) out.push(issue('username', 'V_REQUIRED', 'required when a password is set'));
    throwIf(out);
    const reset = (b.url !== undefined && b.url !== r.url) || (b.username !== undefined && b.username !== r.username) || b.password !== undefined;
    Object.assign(r, b, { updatedAt: now(), updatedBy: c.me.uid });
    if (reset) r.lastCheck = null;
    return regView(r);
  },
  deleteRegistry: (c) => {
    need(c, 'registries', 'write');
    const r = c.db.registries[c.params.registryId!];
    if (!r) throw new Err('not-found', 'registry not found');
    const usedBy: string[] = [];
    for (const [sid, tags] of Object.entries(c.db.images)) for (const img of Object.values(tags)) if (img.registryId === r.registryId) usedBy.push(`${sid}:${img.version}`);
    if (usedBy.length) throw new Err('conflict', `registry is used by ${usedBy.length} image(s)`, { usedBy });
    delete c.db.registries[r.registryId];
    return { deleted: true };
  },
  revealRegistryPassword: (c) => {
    need(c, 'registries', 'write');
    const r = c.db.registries[c.params.registryId!];
    if (!r) throw new Err('not-found', 'registry not found');
    return { password: r.password, value: r.password, hideAfterSec: REVEAL_SECONDS };
  },
  testRegistry: (c) => {
    need(c, 'registries', 'write');
    const r = c.db.registries[c.params.registryId!];
    if (!r) throw new Err('not-found', 'registry not found');
    let repo: string | null = null;
    if (c.raw && c.raw.length) {
      try {
        const j = JSON.parse(new TextDecoder().decode(c.raw)) as { repository?: unknown };
        if (typeof j.repository === 'string' && j.repository.trim()) repo = j.repository.trim();
      } catch {
        throw invalid([issue('', 'V_TYPE', 'body is not JSON')], 'body is not JSON');
      }
    }
    const bad = /bad|wrong|invalid/i.test(r.username + r.password + r.url);
    const anon = !r.username && !r.password;
    let ok = !bad;
    let message = bad ? 'HTTP 401 Unauthorized: {"errors":[{"code":"UNAUTHORIZED","message":"authentication required"}]}' : anon ? 'anonymous access ok (public images only)' : 'login ok';
    if (ok && repo) {
      const src = listableSource(r.url);
      if (src && !tagCatalog(repo)) {
        ok = false;
        message = `repository ${repo} not found, or account ${r.username || 'anonymous'} has no pull access`;
      } else message = `${anon ? 'anonymous access ok' : 'login ok'} · pull access to ${repo}`;
    }
    r.lastCheck = { at: now(), ok, message };
    return { ok, message, at: r.lastCheck.at };
  },
  listRegistryRepositories: (c) => {
    need(c, 'images', 'write');
    const r = c.db.registries[c.params.registryId!];
    if (!r) throw new Err('not-found', 'registry not found');
    const src = listableSource(r.url);
    if (!src) return { items: [], source: 'none', supported: false, message: 'This registry does not allow listing repositories — type the repository name.' };
    const q = (c.query.get('q') ?? '').trim().toLowerCase();
    return { items: repoCatalog(src).filter((x) => !q || x.name.toLowerCase().includes(q) || (x.description ?? '').toLowerCase().includes(q)), source: src, supported: true, message: null };
  },
  listRegistryTags: (c) => {
    need(c, 'images', 'write');
    const r = c.db.registries[c.params.registryId!];
    if (!r) throw new Err('not-found', 'registry not found');
    const repo = (c.query.get('repository') ?? '').trim();
    if (!RE.repository.test(repo)) throw invalid([issue('repository', 'V_FORMAT', `repository "${repo}" must look like library/redis`)]);
    const src = listableSource(r.url);
    if (!src) return { items: [], source: 'none', supported: false, message: 'Tags cannot be listed for this registry — type the tag.' };
    const list = tagCatalog(repo);
    if (!list || !repoCatalog(src).some((x) => x.name === repo)) throw new Err('not-found', `repository ${repo} not found, or the account has no pull access`);
    const q = (c.query.get('q') ?? '').trim().toLowerCase();
    return { items: list.filter((x) => !q || x.tag.toLowerCase().includes(q)), source: src, supported: true, message: null };
  },

  // icons
  listIcons: (c) => ({
    items: Object.values(c.db.icons)
      .sort((a, b) => b.createdAt.localeCompare(a.createdAt))
      .map((i) => ({ ...iconView(i), usedBy: iconUsers(c.db, i.iconId).length })),
  }),
  uploadIcon: async (c) => {
    needAnyWrite(c);
    const type = (c.headers.get('Content-Type') ?? '').split(';')[0]!.trim().toLowerCase();
    if (!(ICON_CONTENT_TYPES as readonly string[]).includes(type)) throw invalid([issue('contentType', 'V_ENUM', `Content-Type must be one of ${ICON_CONTENT_TYPES.join(', ')}`)]);
    const bytes = c.raw ?? new Uint8Array();
    if (!bytes.length) throw invalid([issue('body', 'V_REQUIRED', 'the file is empty')]);
    if (bytes.length > ICON_MAX_BYTES) throw new Err('too-large', `icon is larger than ${ICON_MAX_BYTES / 1024} KiB`);
    if (type === 'image/svg+xml') {
      const txt = new TextDecoder().decode(bytes);
      if (/<script/i.test(txt) || /\son\w+\s*=/i.test(txt)) throw invalid([issue('body', 'V_FORMAT', 'unsafe SVG (scripts / event handlers)')]);
    }
    const sha = 'sha256:' + (await sha256Hex(bytes));
    const ex = Object.values(c.db.icons).find((i) => i.sha256 === sha);
    if (ex) return { icon: iconView(ex), existing: true };
    const name = (c.query.get('name') ?? '').trim().slice(0, 80) || 'icon';
    const id = autoId();
    c.db.icons[id] = { iconId: id, name, contentType: type as IconDoc['contentType'], sizeBytes: bytes.length, sha256: sha, createdBy: c.me.uid, createdAt: now(), dataUrl: `data:${type};base64,${b64(bytes)}` };
    return { icon: iconView(c.db.icons[id]!), existing: false };
  },
  deleteIcon: (c) => {
    needAnyWrite(c);
    const i = c.db.icons[c.params.iconId!];
    if (!i) throw new Err('not-found', 'icon not found');
    const users = iconUsers(c.db, i.iconId);
    if (users.length) throw new Err('conflict', `icon is used by ${users.length} item(s)`, { users });
    delete c.db.icons[i.iconId];
    return { deleted: true };
  },

  // services
  listServices: (c) => {
    need(c, 'services', 'read');
    return {
      items: Object.values(c.db.services)
        .sort((a, b) => a.label.localeCompare(b.label))
        .map((s) => ({ ...s, imageCount: Object.keys(c.db.images[s.serviceSlug] ?? {}).length, usedBy: new Set(usage(c.db, (sid) => sid === s.serviceSlug).filter((u) => u.kind === 'deployment').map((u) => u.id)).size })),
    };
  },
  createService: (c) => {
    need(c, 'services', 'write');
    const b = bodyOf<{ label: string; serviceSlug: string; icon?: string | null; description?: string }>(c);
    unknownFields(b, ['label', 'serviceSlug', 'icon', 'description']);
    const out: ValidationIssue[] = [];
    checkLabel(b.label, 'label', out);
    if (!RE.slug.test(b.serviceSlug ?? '')) out.push(issue('serviceSlug', 'V_FORMAT', 'name (id) must match ^[a-z][a-z0-9-]{1,39}$'));
    iconIssues(b.icon, 'icon', out);
    if ((b.description ?? '').length > LIMITS.description) out.push(issue('description', 'V_LENGTH', 'too long'));
    throwIf(out);
    if (c.db.services[b.serviceSlug]) throw new Err('conflict', `service ${b.serviceSlug} exists`, { field: 'serviceSlug' });
    const t = now();
    c.db.services[b.serviceSlug] = { serviceSlug: b.serviceSlug, label: b.label, icon: b.icon ?? null, description: b.description ?? '', createdAt: t, createdBy: c.me.uid, updatedAt: t, updatedBy: c.me.uid };
    c.db.images[b.serviceSlug] = {};
    return c.db.services[b.serviceSlug];
  },
  getService: (c) => {
    need(c, 'services', 'read');
    const s = c.db.services[c.params.serviceSlug!];
    if (!s) throw new Err('not-found', 'service not found');
    return s;
  },
  patchService: (c) => {
    need(c, 'services', 'write');
    const s = c.db.services[c.params.serviceSlug!];
    if (!s) throw new Err('not-found', 'service not found');
    const b = bodyOf<{ label?: string; icon?: string | null; description?: string; serviceSlug?: string }>(c);
    if ('serviceSlug' in b) throw invalid([issue('serviceSlug', 'V_IMMUTABLE', 'the name (id) cannot change')]);
    unknownFields(b, ['label', 'icon', 'description']);
    const out: ValidationIssue[] = [];
    if (b.label !== undefined) checkLabel(b.label, 'label', out);
    if (b.icon !== undefined) iconIssues(b.icon, 'icon', out);
    if (b.description !== undefined && b.description.length > LIMITS.description) out.push(issue('description', 'V_LENGTH', 'too long'));
    throwIf(out);
    Object.assign(s, b, { updatedAt: now(), updatedBy: c.me.uid });
    return s;
  },
  deleteService: (c) => {
    need(c, 'services', 'write');
    const s = c.db.services[c.params.serviceSlug!];
    if (!s) throw new Err('not-found', 'service not found');
    const n = Object.keys(c.db.images[s.serviceSlug] ?? {}).length;
    if (n) throw new Err('conflict', `the service has ${n} image(s); delete them first`);
    delete c.db.services[s.serviceSlug];
    delete c.db.images[s.serviceSlug];
    return { deleted: true };
  },
  serviceUsage: (c) => {
    need(c, 'services', 'read');
    if (!c.db.services[c.params.serviceSlug!]) throw new Err('not-found', 'service not found');
    return { items: usage(c.db, (sid) => sid === c.params.serviceSlug) };
  },

  // images
  listImages: (c) => {
    need(c, 'images', 'read');
    if (!c.db.services[c.params.serviceSlug!]) throw new Err('not-found', 'service not found');
    return { items: Object.values(c.db.images[c.params.serviceSlug!] ?? {}).map(strip).sort((a, b) => b.createdAt.localeCompare(a.createdAt)) };
  },
  inspectImage: (c) => {
    need(c, 'images', 'write');
    const s = c.db.services[c.params.serviceSlug!];
    if (!s) throw new Err('not-found', 'service not found');
    const b = bodyOf<{ reference: string; registryId?: string | null; platform?: string }>(c);
    unknownFields(b, ['reference', 'registryId', 'platform']);
    const pr = parseImageReference(b.reference);
    if (!pr.ok) throw invalid([issue('reference', 'V_REFERENCE', pr.error)]);
    if (b.registryId && !c.db.registries[b.registryId]) throw new Err('not-found', 'registry not found');
    const repo = pr.parts.repository;
    if (/missing|nope/.test(pr.parts.tag ?? '')) throw new Err('unavailable', `manifest unknown: ${b.reference.trim()} not found`);
    if (/core-api|northwind/.test(repo))
      return {
        digest: fakeSha(),
        platform: b.platform ?? 'linux/amd64',
        sizeBytes: 88_000_000,
        layers: 7,
        entrypoint: null,
        command: ['deno', 'task', 'start'],
        workdir: '/app',
        user: null,
        ports: [{ name: 'http', port: 8080, protocol: 'tcp', description: '' }],
        env: [
          { name: 'NODE_ENV', value: 'production', options: null, required: false, secret: false, description: '' },
          { name: 'PORT', value: '8080', options: null, required: false, secret: false, description: '' },
          { name: 'LOG_IN_CONSOLE', value: 'true', options: null, required: false, secret: false, description: '' },
        ],
        volumes: [{ name: 'config', mountPath: '/app/helper/config/env/production.ts', kind: 'dir', description: '' }],
      };
    const isRedis = /redis/.test(repo);
    const isApi = /api/.test(repo);
    return {
      digest: fakeSha(),
      platform: b.platform ?? 'linux/amd64',
      sizeBytes: 12_345_678,
      layers: 3,
      entrypoint: isRedis ? ['docker-entrypoint.sh'] : null,
      command: isRedis ? ['redis-server'] : ['/app'],
      workdir: isRedis ? '/data' : null,
      user: isRedis ? null : '65532:65532',
      ports: [{ name: isRedis ? 'p6379' : 'p8080', port: isRedis ? 6379 : 8080, protocol: 'tcp', description: '' }],
      env: isRedis ? [{ name: 'REDIS_VERSION', value: '7.4.1', options: null, required: false, secret: false, description: '' }] : [{ name: 'PORT', value: '8080', options: null, required: false, secret: false, description: '' }, ...(isApi ? [{ name: 'DATA_DIR', value: '/data', options: null, required: false, secret: false, description: '' }] : [])],
      volumes: isRedis || isApi ? [{ name: 'data', mountPath: '/data', kind: 'dir', description: '' }] : [], // docker VOLUME = folder (§21 #83)
    };
  },
  registerImage: (c) => {
    need(c, 'images', 'write');
    const s = c.db.services[c.params.serviceSlug!];
    if (!s) throw new Err('not-found', 'service not found');
    const b = bodyOf<Record<string, unknown>>(c);
    unknownFields(b, ['version', 'reference', 'registryId', 'platform', 'mode', 'kind', 'entrypoint', 'command', 'workdir', 'user', 'ports', 'env', 'volumes', 'healthcheck', 'stopTimeoutSec', 'description']);
    const is = validateRegisterImage(b);
    if (!is.ok) throw invalid(is.errors);
    const pr = parseImageReference(b.reference);
    if (!pr.ok) throw invalid([issue('reference', 'V_REFERENCE', pr.error)]);
    let registryId: string | null;
    if (b.registryId === undefined) registryId = matchRegistries(pr.parts.host, Object.values(c.db.registries))[0]?.registryId ?? null;
    else {
      registryId = (b.registryId as string | null) ?? null;
      if (registryId && !c.db.registries[registryId]) throw new Err('not-found', 'registry not found');
    }
    const version = b.version as string;
    if (c.db.images[s.serviceSlug]?.[version]) throw new Err('conflict', `version ${version} exists`, { reason: 'version-exists', field: 'version' });
    const t = now();
    const reference = (b.reference as string).trim();
    const img: Image & { _copyAt?: number; _readyAt?: number; _fail?: boolean } = {
      version,
      serviceSlug: s.serviceSlug,
      reference,
      source: pr.parts,
      registryId,
      digest: null,
      platform: (b.platform as Image['platform']) ?? 'linux/amd64',
      import: { mode: b.mode as Image['import']['mode'], status: 'pending', error: null, sizeBytes: null, manifestMediaType: null, configDigest: null, layers: [], startedAt: null, finishedAt: null },
      kind: b.kind as Image['kind'],
      entrypoint: (b.entrypoint as string[] | null) ?? null,
      command: (b.command as string[] | null) ?? null,
      workdir: (b.workdir as string | null) ?? null,
      user: (b.user as string | null) ?? null,
      ports: (b.ports as Image['ports']) ?? [],
      env: ((b.env as Image['env']) ?? []).map((e) => normalizeImageEnv(e)),
      volumes: ((b.volumes as Image['volumes']) ?? []).map(normalizeImageVolume),
      healthcheck: (b.healthcheck as Image['healthcheck']) ?? null,
      stopTimeoutSec: (b.stopTimeoutSec as number) ?? 30,
      description: (b.description as string) ?? '',
      createdAt: t,
      createdBy: c.me.uid,
      updatedAt: t,
      updatedBy: c.me.uid,
      _copyAt: Date.now() + 1500,
      _readyAt: Date.now() + 6000,
      _fail: /fail/.test(reference),
    };
    (c.db.images[s.serviceSlug] ??= {})[version] = img;
    return strip(img);
  },
  getImage: (c) => strip(img(c)),
  patchImage: (c) => {
    need(c, 'images', 'write');
    const i = img(c);
    const b = bodyOf<PatchImageRequest & Record<string, unknown>>(c);
    const identity = ['version', 'serviceSlug', 'source', 'digest', 'platform', 'import', 'mode', 'layers', 'createdAt', 'createdBy', 'updatedAt', 'updatedBy'];
    const bad = Object.keys(b).filter((k) => identity.includes(k));
    if (bad.length) throw invalid(bad.map((k) => issue(k, 'V_IMMUTABLE', `${k} cannot be changed (image identity) — clone as a new version`)));
    unknownFields(b, ['reference', 'registryId', 'kind', 'entrypoint', 'command', 'workdir', 'user', 'ports', 'env', 'volumes', 'healthcheck', 'stopTimeoutSec', 'description', 'renames', 'renameEverywhere']);
    const srcChange = (b.registryId !== undefined && b.registryId !== i.registryId) || (b.reference !== undefined && b.reference.trim() !== i.reference);
    if (srcChange && i.import.status !== 'failed') throw new Err('conflict', 'reference / registry can change only while the import failed');
    const out: ValidationIssue[] = [];
    if (b.registryId && !c.db.registries[b.registryId]) out.push(issue('registryId', 'V_REF', 'registry not found'));
    let parts: Image['source'] | null = null;
    if (b.reference !== undefined) {
      const pr = parseImageReference(b.reference);
      if (pr.ok) parts = pr.parts;
      else out.push(issue('reference', 'V_REFERENCE', pr.error));
    }
    throwIf(out);
    const cur = structuredClone(i);
    cur.env = cur.env.map((e) => normalizeImageEnv(e));
    const next = structuredClone(cur);
    if (b.registryId !== undefined) next.registryId = b.registryId ?? null;
    if (b.reference !== undefined && parts) {
      next.reference = b.reference.trim();
      next.source = parts;
    }
    if (b.kind !== undefined) next.kind = b.kind;
    if (b.entrypoint !== undefined) next.entrypoint = b.entrypoint;
    if (b.command !== undefined) next.command = b.command;
    if (b.workdir !== undefined) next.workdir = b.workdir || null;
    if (b.user !== undefined) next.user = b.user || null;
    if (b.description !== undefined) next.description = b.description;
    if (b.healthcheck !== undefined) next.healthcheck = b.healthcheck;
    if (b.stopTimeoutSec !== undefined) next.stopTimeoutSec = b.stopTimeoutSec;
    if (Array.isArray(b.ports)) {
      const partial = b.ports.every((p) => !('port' in p) && !('protocol' in p));
      if (partial)
        for (const p of b.ports) {
          const t = next.ports.find((x) => x.name === p.name);
          if (!t) out.push(issue(`ports[${p.name}]`, 'V_REF', `unknown port ${p.name}`));
          else t.description = p.description;
        }
      else next.ports = (b.ports as Image['ports']).map((p) => ({ name: p.name, port: p.port, protocol: p.protocol, description: p.description ?? '' }));
    }
    if (Array.isArray(b.env)) {
      const partial = b.env.every((e) => !('required' in e) && !('secret' in e));
      if (partial)
        for (const e of b.env as { name: string; value?: string | null; options?: string[] | null; description?: string }[]) {
          const x = next.env.find((y) => y.name === e.name);
          if (!x) {
            out.push(issue(`env[${e.name}]`, 'V_REF', `unknown env ${e.name}`));
            continue;
          }
          if (e.description !== undefined) x.description = e.description;
          if (e.options !== undefined) x.options = e.options;
          if (e.value === null) delete x.value;
          else if (e.value !== undefined) x.value = e.value;
        }
      else next.env = (b.env as ImageEnv[]).map((e) => normalizeImageEnv({ ...e, description: e.description ?? '', options: e.options ?? null }));
    }
    if (Array.isArray(b.volumes)) next.volumes = b.volumes.map((v) => ({ name: v.name, mountPath: v.mountPath, kind: v.kind as Image['volumes'][number]['kind'], description: v.description ?? '' }));
    throwIf(out);
    const is = new Issues('publish');
    validateImageSpec(next as unknown as Record<string, unknown>, is);
    if (is.ok) next.volumes = next.volumes.map(normalizeImageVolume);
    const ch = imageNameChanges(cur, next, b.renames ?? []);
    if (!is.ok || ch.issues.length) throw invalid([...is.errors, ...ch.issues]);
    // impact: renamed / removed names used by templates + deployment drafts (heads keep their own copy)
    const changes = [...ch.renamed.map((r) => ({ kind: r.kind, name: r.from, change: 'renamed' as const, to: r.to as string | null })), ...ch.removed.map((r) => ({ kind: r.kind, name: r.name, change: 'removed' as const, to: null }))];
    if (changes.length) {
      const sv = i.serviceSlug;
      const ver = i.version;
      const tpls = Object.values(c.db.templates);
      const deps = Object.values(c.db.deployments).filter((d) => !d.deleted);
      const items: ImageImpactItem[] = changes.map((x) => {
        const usedBy: ImageNameUse[] = [];
        for (const t of tpls) for (const u of imageNameUses(t, sv, ver, x.kind, x.name)) usedBy.push({ kind: 'template', templateId: t.templateId, label: t.label, source: 'template', ...u });
        for (const d of deps) for (const u of imageNameUses(d.draft, sv, ver, x.kind, x.name)) usedBy.push({ kind: 'deployment', deploymentId: d.deploymentId, label: d.label, source: 'draft', ...u });
        return { ...x, usedBy };
      });
      const blocking = items.filter((x) => x.usedBy.length && (x.change === 'removed' || !b.renameEverywhere));
      if (blocking.length) throw new Err('conflict', `${blocking.length} renamed / removed name(s) are still used by templates or deployment drafts`, { reason: 'in-use', items: blocking } satisfies ImageInUseDetails);
      const renames = ch.renamed.filter((r) => items.some((x) => x.change === 'renamed' && x.kind === r.kind && x.name === r.from && x.usedBy.length));
      if (b.renameEverywhere && renames.length) {
        for (const t of tpls) {
          const r = renameImageNames(t, sv, ver, renames);
          if (!r.count) continue;
          t.sites = r.config.sites;
          t.version += 1;
          t.updatedAt = now();
          t.updatedBy = c.me.uid;
        }
        // §21 #94 deployments are detached from their templates: their drafts are never rewritten (they need a
        // Template sync or a manual edit; the 409 details listed them)
      }
    }
    next.updatedAt = now();
    next.updatedBy = c.me.uid;
    c.db.images[i.serviceSlug]![i.version] = next;
    return strip(next);
  },
  deleteImage: (c) => {
    need(c, 'images', 'write');
    const i = img(c);
    const u = usage(c.db, (sid, v) => sid === i.serviceSlug && v === i.version);
    if (u.length) throw new Err('conflict', `image is used by ${u.length} template/deployment site(s)`, { usedBy: u });
    delete c.db.images[i.serviceSlug]![i.version];
    return { deleted: true };
  },
  retryImport: (c) => {
    need(c, 'images', 'write');
    const i = img(c) as Image & { _copyAt?: number; _readyAt?: number; _fail?: boolean };
    if (i.import.status !== 'failed') throw new Err('conflict', `import is ${i.import.status}, not failed`);
    i.import = { ...i.import, status: 'pending', error: null, startedAt: null, finishedAt: null };
    i._copyAt = Date.now() + 1200;
    i._readyAt = Date.now() + 5000;
    // a fixed source imports; a reference that still names a missing tag fails again
    i._fail = /fail|1\.1\.0-rc1/.test(i.reference);
    return strip(i);
  },
  imageUsage: (c) => {
    const i = img(c);
    return { items: usage(c.db, (sid, v) => sid === i.serviceSlug && v === i.version) };
  },

  // not-found pages
  listNotFoundPages: (c) => {
    need(c, 'notFoundPages', 'read');
    return { items: Object.values(c.db.notFoundPages).map(({ htmlCode, ...p }) => ({ ...p, sizeBytes: new TextEncoder().encode(htmlCode).length, usedBy: pageUsage(c.db, p.notFoundPageId).length })).sort((a, b) => b.createdAt.localeCompare(a.createdAt)) };
  },
  createNotFoundPage: (c) => {
    need(c, 'notFoundPages', 'write');
    const b = bodyOf<{ label: string; htmlCode: string }>(c);
    unknownFields(b, ['label', 'htmlCode']);
    const out: ValidationIssue[] = [];
    checkLabel(b.label, 'label', out);
    if (!b.htmlCode) out.push(issue('htmlCode', 'V_REQUIRED', 'required'));
    throwIf(out);
    if (new TextEncoder().encode(b.htmlCode).length > LIMITS.notFoundPageBytes) throw new Err('too-large', `htmlCode is larger than ${LIMITS.notFoundPageBytes} bytes`);
    const id = autoId();
    c.db.notFoundPages[id] = { notFoundPageId: id, label: b.label, htmlCode: b.htmlCode, createdAt: now(), createdBy: c.me.uid };
    return c.db.notFoundPages[id];
  },
  getNotFoundPage: (c) => {
    need(c, 'notFoundPages', 'read');
    const p = c.db.notFoundPages[c.params.notFoundPageId!];
    if (!p) throw new Err('not-found', 'page not found');
    return { ...p, usedBy: pageUsage(c.db, p.notFoundPageId) };
  },
  deleteNotFoundPage: (c) => {
    need(c, 'notFoundPages', 'write');
    const p = c.db.notFoundPages[c.params.notFoundPageId!];
    if (!p) throw new Err('not-found', 'page not found');
    const u = pageUsage(c.db, p.notFoundPageId);
    if (u.length) throw new Err('conflict', `page is used by ${u.length} site(s)`, { usedBy: u });
    delete c.db.notFoundPages[p.notFoundPageId];
    return { deleted: true };
  },

  // templates
  listTemplates: (c) => {
    need(c, 'templates', 'read');
    return {
      items: Object.values(c.db.templates)
        .map((t) => ({ templateId: t.templateId, slug: t.slug, label: t.label, icon: t.icon, description: t.description, version: t.version, updatedAt: t.updatedAt, siteSlugs: t.sites.map((s) => s.siteSlug), counts: { variables: t.variables.length, secrets: t.secrets.length, files: t.files.length, sites: t.sites.length }, usedBy: Object.values(c.db.deployments).filter((d) => !d.deleted && (d.branch?.origin?.branch.kind === 'template' ? (d.branch.origin.branch as { templateId: string }).templateId : d.srcTemplate?.templateId) === t.templateId).length, branch: (refreshTemplate(c.db, t), t.branch ?? null) }))
        .sort((a, b) => b.updatedAt.localeCompare(a.updatedAt)),
    };
  },
  createTemplate: (c) => {
    need(c, 'templates', 'write');
    const b = bodyOf<{ label: string; slug?: string; icon?: string | null; description?: string; from?: import('@opm/shared').BranchPoint | null; message?: string }>(c);
    unknownFields(b, ['label', 'slug', 'icon', 'description', 'from', 'message', 'copySecretValues']);
    const out: ValidationIssue[] = [];
    checkLabel(b.label, 'label', out);
    const slug = pickSlug(Object.values(c.db.templates).map((x) => x.slug), b.slug, b.label, out);
    iconIssues(b.icon, 'icon', out);
    throwIf(out);
    // §21 #104 create from a template / deployment / agent branch: secret values dropped, generate rules kept
    const src = fromPoint(c, b.from ?? null);
    const cfg: Config = src ? { ...src.config, secrets: src.config.secrets.map((x) => ({ ...x, value: null, rotatedAt: null })) } : { variables: [], secrets: [], files: [], sites: [] };
    const id = autoId();
    const t = now();
    const tpl: Template = { templateId: id, slug, label: b.label, icon: b.icon ?? null, description: b.description ?? '', version: 1, ...cfg, createdAt: t, createdBy: c.me.uid, updatedAt: t, updatedBy: c.me.uid };
    c.db.templates[id] = tpl;
    branchHelpers.create(c.db, branchHelpers.tplRef(id), tpl, cfg, c.me.uid, src ? { ref: src.info.ref, label: src.info.label, commit: src.commit, config: src.config } : null, b.message, null);
    return tpl;
  },
  getTemplate: (c) => {
    need(c, 'templates', 'read');
    const t = c.db.templates[c.params.templateId!];
    if (!t) throw new Err('not-found', 'template not found');
    refreshTemplate(c.db, t);
    // like the API: documents written before a contract change are normalized on read (round D: legacy data sources)
    return { ...t, ...normalizeConfig({ files: t.files, sites: t.sites }, imageVolumes(c.db)) };
  },
  patchTemplate: (c) => {
    need(c, 'templates', 'write');
    const t = c.db.templates[c.params.templateId!];
    if (!t) throw new Err('not-found', 'template not found');
    const b = bodyOf<Partial<Template> & { ifVersion: number }>(c);
    unknownFields(b, ['ifVersion', 'label', 'icon', 'description', 'variables', 'secrets', 'files', 'sites', 'slug']);
    if ('slug' in b) throw invalid([issue('slug', 'V_IMMUTABLE', 'the slug never changes after create (it names the GitHub branch)')]);
    if (['variables', 'secrets', 'files', 'sites'].some((k) => k in b)) throw new Err('gone', 'the template config changes only by a commit (§21 #105)', { hint: 'POST /v1/templates/{templateId}/commits { message, baseCommitId, config }' });
    if (b.ifVersion !== t.version) throw new Err('conflict', `template is at version ${t.version}`, { current: t.version });
    const out: ValidationIssue[] = [];
    if (b.label !== undefined) checkLabel(b.label, 'label', out);
    if (b.icon !== undefined) iconIssues(b.icon, 'icon', out);
    throwIf(out);
    const next: Template = { ...t, ...Object.fromEntries(Object.entries(b).filter(([k]) => k !== 'ifVersion')) } as Template;
    // the API normalizes writes (legacy container shape, host "" → null, file render default)
    Object.assign(next, normalizeConfig({ files: next.files, sites: next.sites }, imageVolumes(c.db)));
    const v = validateConfig({ variables: next.variables, secrets: next.secrets, files: next.files, sites: next.sites }, vctx(c.db, 'template', 'template'));
    if (!v.ok) throw invalid(v.errors);
    next.version = t.version + 1;
    next.updatedAt = now();
    next.updatedBy = c.me.uid;
    c.db.templates[t.templateId] = next;
    refreshTemplate(c.db, next);
    return { template: next, validation: { ok: v.ok, issues: v.list } };
  },
  deleteTemplate: (c) => {
    need(c, 'templates', 'write');
    if (!c.db.templates[c.params.templateId!]) throw new Err('not-found', 'template not found');
    delete c.db.templates[c.params.templateId!];
    return { deleted: true };
  },

  // blobs + uploads
  uploadBlob: async (c) => {
    if (!allowed(c.me, 'templates', 'write') && !anyDep(c, 'deployment.config', 'write')) throw new Err('forbidden', 'you need templates.write or deployment.config.write');
    const bytes = c.raw ?? new Uint8Array();
    if (bytes.length > 24 * 1024 * 1024) throw new Err('too-large', 'body larger than 24 MiB');
    const hex = await sha256Hex(bytes);
    const want = c.headers.get('X-OPM-Sha256');
    if (want && want.replace(/^sha256:/, '') !== hex) throw invalid([issue('X-OPM-Sha256', 'V_FORMAT', 'sha256 mismatch')], 'sha256 mismatch');
    storeBlob(c.db, hex, bytes);
    return { sha256: `sha256:${hex}`, sizeBytes: bytes.length };
  },
  getBlob: (c) => {
    if (!allowed(c.me, 'templates', 'read') && !anyDep(c, 'deployment.config', 'read')) throw new Err('forbidden', 'you need templates.read or deployment.config.read');
    const hex = c.params.sha256!.replace(/^sha256:/, '');
    const bytes = loadBlob(c.db, hex);
    if (!bytes) throw new Err('not-found', 'blob not found');
    return bytesResponse(bytes);
  },
  createUpload: (c) => {
    if (!allowed(c.me, 'templates', 'write') && !anyDep(c, 'deployment.config', 'write')) throw new Err('forbidden', 'you need templates.write or deployment.config.write');
    const b = bodyOf<{ purpose: 'blob' | 'seed-archive'; sizeBytes: number; sha256: string; stageId?: string }>(c);
    if (b.stageId !== undefined) stageOf(c, b.stageId);
    if (b.purpose !== 'blob' && b.purpose !== 'seed-archive') throw invalid([issue('purpose', 'V_ENUM', 'purpose must be blob | seed-archive')]);
    if (!RE.sha256.test(b.sha256)) throw invalid([issue('sha256', 'V_FORMAT', 'sha256 must be sha256:<hex>')]);
    if (b.purpose === 'blob' && b.sizeBytes > 50 * 1024 * 1024) throw new Err('too-large', 'blob uploads are limited to 50 MiB');
    if (b.purpose === 'seed-archive' && b.sizeBytes > SEED_ARCHIVE_MAX) throw new Err('too-large', `seed archives are limited to ${SEED_ARCHIVE_MAX / 1048576} MiB`);
    const u = newUpload(c, 'blob', b.sizeBytes, b.sha256, null, null);
    if (b.stageId) uploadStage.set(u.uploadId, b.stageId);
    // round E: a seed archive is stored like any blob (content-addressed); only the purpose / limit differ
    if (b.purpose === 'seed-archive') {
      const raw = c.db.uploads[u.uploadId];
      if (raw) raw.purpose = 'seed-archive';
      return { ...u, purpose: 'seed-archive' };
    }
    return u;
  },
  getUpload: (c) => uploadView(upl(c)),
  putUploadPart: (c) => {
    const u = upl(c);
    if (u.status !== 'open') throw new Err('gone', `upload is ${u.status}`);
    const i = Number(c.params.index);
    if (!Number.isInteger(i) || i < 0 || i >= u.partCount) throw invalid([issue('index', 'V_RANGE', 'index out of range')]);
    const bytes = c.raw ?? new Uint8Array();
    const expect = i === u.partCount - 1 ? u.sizeBytes - u.partSize * (u.partCount - 1) : u.partSize;
    if (bytes.length !== expect) throw invalid([issue('body', 'V_LENGTH', `part ${i} must be ${expect} bytes (got ${bytes.length})`)]);
    if (u.sizeBytes > 4 * 1024 * 1024) memBlobs.set(`${u.uploadId}:${i}`, bytes);
    else u.parts[i] = b64(bytes);
    if (!u.partsReceived.includes(i)) u.partsReceived.push(i);
    u.partsReceived.sort((a, b) => a - b);
    return { received: u.partsReceived };
  },
  completeUpload: async (c) => {
    const u = upl(c);
    if (u.status !== 'open') throw new Err('gone', `upload is ${u.status}`);
    const missing = Array.from({ length: u.partCount }, (_, i) => i).filter((i) => !u.partsReceived.includes(i));
    if (missing.length) throw new Err('invalid', `missing parts ${missing.join(', ')}`, { missing, errors: [] });
    const chunks = Array.from({ length: u.partCount }, (_, i) => memBlobs.get(`${u.uploadId}:${i}`) ?? (u.parts[i] ? unb64(u.parts[i]!) : new Uint8Array()));
    const all = new Uint8Array(chunks.reduce((a, x) => a + x.length, 0));
    let off = 0;
    for (const ch of chunks) {
      all.set(ch, off);
      off += ch.length;
    }
    const hex = await sha256Hex(all);
    if (`sha256:${hex}` !== u.sha256 || all.length !== u.sizeBytes) {
      u.status = 'failed';
      u.error = 'sha256 or size mismatch';
      throw invalid([issue('sha256', 'V_FORMAT', 'the joined parts do not match sha256 / sizeBytes')], 'sha256 mismatch');
    }
    u.status = 'complete';
    u.parts = {};
    for (let i = 0; i < u.partCount; i++) memBlobs.delete(`${u.uploadId}:${i}`);
    let transfer: Transfer | null = null;
    const into = uploadStage.get(u.uploadId);
    if (into) {
      const st = stages.get(into);
      if (!st) throw new Err('gone', 'the stage of this upload was dropped');
      st.blobs.set(hex, all);
      stageLog.push({ op: 'put', stageId: into, sha: `sha256:${hex}`, size: all.length, at: Date.now(), multipart: true });
    } else if (u.target === 'blob') storeBlob(c.db, hex, all);
    else if (u.target === 'file') {
      // §21 #123 a browser publish into the cloud: the bytes are in the cloud store → ready
      storeBlob(c.db, `file:${u.transferId}`, all);
      const d = c.db.deployments[u.deploymentId!]!;
      const f = (c.db.files[d.deploymentId] ?? []).find((x) => x.fileId === u.transferId);
      if (f && f.state === 'uploading') {
        f.state = 'ready';
        f.cloud = { stored: true, fetch: null };
        cloudActivity(c, d, 'file.published', `File ${f.fileName} published`, { fileId: f.fileId, fileName: f.fileName, sizeBytes: f.sizeBytes, origin: f.origin });
        d.agentEpoch += 1;
      }
    } else {
      storeBlob(c.db, `transfer:${u.transferId}`, all);
      const d = c.db.deployments[u.deploymentId!]!;
      transfer = c.db.transfers[d.deploymentId]!.find((t) => t.transferId === u.transferId) ?? null;
      if (transfer) {
        transfer.status = 'ready';
        cloudActivity(c, d, 'transfer.sent', `File ${transfer.fileName} sent to ${transfer.siteSlug}`, { transferId: transfer.transferId, direction: 'to-site', fileName: transfer.fileName, sizeBytes: transfer.sizeBytes });
        d.agentEpoch += 1;
      }
    }
    return { upload: uploadView(u), blob: u.target === 'blob' ? { sha256: u.sha256, sizeBytes: u.sizeBytes } : null, transfer };
  },

  // tags
  listTags: (c) => {
    need(c, 'tags', 'read');
    return { items: Object.values(c.db.tags).map((t) => ({ ...t, deployments: Object.values(c.db.deployments).filter((d) => !d.deleted && d.tagSlugs.includes(t.tagSlug)).length })).sort((a, b) => a.tagSlug.localeCompare(b.tagSlug)) };
  },
  createTag: (c) => {
    need(c, 'tags', 'write');
    const b = bodyOf<{ tagSlug: string; label: string; icon?: string | null; color: string }>(c);
    unknownFields(b, ['tagSlug', 'label', 'icon', 'color']);
    const out: ValidationIssue[] = [];
    if (!RE.slug.test(b.tagSlug ?? '')) out.push(issue('tagSlug', 'V_FORMAT', 'tag slug must match ^[a-z][a-z0-9-]{1,39}$'));
    checkLabel(b.label, 'label', out);
    iconIssues(b.icon, 'icon', out);
    if (!RE.color.test(b.color ?? '')) out.push(issue('color', 'V_FORMAT', 'color must be #rrggbb'));
    throwIf(out);
    if (c.db.tags[b.tagSlug]) throw new Err('conflict', `tag ${b.tagSlug} exists`, { field: 'tagSlug' });
    const t = now();
    c.db.tags[b.tagSlug] = { ...b, icon: b.icon ?? null, createdAt: t, createdBy: c.me.uid, updatedAt: t, updatedBy: c.me.uid };
    return c.db.tags[b.tagSlug];
  },
  patchTag: (c) => {
    need(c, 'tags', 'write');
    const t = c.db.tags[c.params.tagSlug!];
    if (!t) throw new Err('not-found', 'tag not found');
    const b = bodyOf<{ label?: string; icon?: string | null; color?: string }>(c);
    unknownFields(b, ['label', 'icon', 'color']);
    const out: ValidationIssue[] = [];
    if (b.label !== undefined) checkLabel(b.label, 'label', out);
    if (b.icon !== undefined) iconIssues(b.icon, 'icon', out);
    if (b.color !== undefined && !RE.color.test(b.color)) out.push(issue('color', 'V_FORMAT', 'color must be #rrggbb'));
    throwIf(out);
    Object.assign(t, b, { updatedAt: now(), updatedBy: c.me.uid });
    return t;
  },
  deleteTag: (c) => {
    need(c, 'tags', 'write');
    const slug = c.params.tagSlug!;
    if (!c.db.tags[slug]) throw new Err('not-found', 'tag not found');
    let deployments = 0;
    let accessRules = 0;
    for (const d of Object.values(c.db.deployments))
      if (d.tagSlugs.includes(slug)) {
        d.tagSlugs = d.tagSlugs.filter((x) => x !== slug);
        deployments++;
      }
    for (const a of Object.values(c.db.accounts))
      for (const r of a.access)
        if (r.scope.kind === 'tags' && r.scope.tagSlugs.includes(slug)) {
          r.scope.tagSlugs = r.scope.tagSlugs.filter((x) => x !== slug);
          accessRules++;
        }
    delete c.db.tags[slug];
    return { deleted: true, removedFrom: { deployments, accessRules } };
  },

  // accounts
  listAccounts: (c) => {
    need(c, 'accounts', 'read');
    return { items: Object.values(c.db.accounts).sort((a, b) => a.name.localeCompare(b.name)).map(accView) };
  },
  createAccount: (c) => {
    need(c, 'accounts', 'admin');
    const b = bodyOf<{ name: string; email: string; phone?: string | null; img?: string | null; access?: Account['access'] }>(c);
    unknownFields(b, ['name', 'email', 'phone', 'img', 'access']);
    const out: ValidationIssue[] = [];
    checkLabel(b.name, 'name', out);
    if (!RE.email.test(b.email ?? '')) out.push(issue('email', 'V_FORMAT', 'not an email address'));
    if (b.phone && !RE.e164.test(b.phone)) out.push(issue('phone', 'V_FORMAT', 'phone must be E.164'));
    if (b.img) iconIssues(b.img, 'img', out, true);
    out.push(...validateAccessRules(b.access ?? [], Object.keys(c.db.tags), Object.keys(c.db.deployments)));
    throwIf(out);
    const email = b.email.trim().toLowerCase();
    if (Object.values(c.db.accounts).some((a) => a.email === email)) throw new Err('conflict', `an account with ${email} exists`, { field: 'email' });
    const uid = autoId();
    const t = now();
    const password = genPassword();
    c.db.accounts[uid] = { uid, img: b.img ?? null, name: b.name, email, phone: b.phone ?? null, access: b.access ?? [], disabled: false, createdAt: t, createdBy: c.me.uid, lastLoginAt: null, lastSetPassword: { value: password, setAt: t, setBy: c.me.uid, validAfter: t }, lastResetLink: null };
    c.db.users.push({ uid, email, password });
    return { account: accView(c.db.accounts[uid]!), value: password, hideAfterSec: REVEAL_SECONDS };
  },
  getAccount: (c) => {
    if (c.params.uid !== c.me.uid) need(c, 'accounts', 'read');
    const a = c.db.accounts[c.params.uid!];
    if (!a) throw new Err('not-found', 'account not found');
    return accView(a);
  },
  patchAccount: (c) => {
    const a = c.db.accounts[c.params.uid!];
    if (!a) throw new Err('not-found', 'account not found');
    const b = bodyOf<{ name?: string; phone?: string | null; img?: string | null; access?: Account['access'] }>(c);
    unknownFields(b, ['name', 'phone', 'img', 'access']);
    if (a.uid !== c.me.uid && (b.name !== undefined || b.phone !== undefined || b.img !== undefined)) need(c, 'accounts', 'write');
    if (b.access !== undefined) need(c, 'accounts', 'admin');
    const out: ValidationIssue[] = [];
    if (b.name !== undefined) checkLabel(b.name, 'name', out);
    if (b.phone && !RE.e164.test(b.phone)) out.push(issue('phone', 'V_FORMAT', 'phone must be E.164 (e.g. +919812345678)'));
    if (b.img) iconIssues(b.img, 'img', out, true);
    if (b.access !== undefined) out.push(...validateAccessRules(b.access, Object.keys(c.db.tags), Object.keys(c.db.deployments)).map((i) => ({ ...i, path: i.path.startsWith('access') ? i.path : `access${i.path.startsWith('[') ? '' : '.'}${i.path}` })));
    throwIf(out);
    if (b.access !== undefined && isAdmin(a) && !isAdmin({ ...a, access: b.access })) {
      const admins = Object.values(c.db.accounts).filter((x) => !x.disabled && isAdmin(x));
      if (admins.length <= 1) throw new Err('conflict', 'this is the last account with accounts.admin');
    }
    if (b.name !== undefined) a.name = b.name;
    if (b.phone !== undefined) a.phone = b.phone || null;
    if (b.img !== undefined) a.img = b.img || null;
    if (b.access !== undefined) a.access = b.access;
    return accView(a);
  },
  disableAccount: (c) => {
    need(c, 'accounts', 'admin');
    const a = c.db.accounts[c.params.uid!];
    if (!a) throw new Err('not-found', 'account not found');
    const b = bodyOf<{ disabled: boolean }>(c);
    if (b.disabled && a.uid === c.me.uid) throw new Err('conflict', 'you cannot disable yourself');
    if (b.disabled && isAdmin(a) && Object.values(c.db.accounts).filter((x) => !x.disabled && isAdmin(x)).length <= 1) throw new Err('conflict', 'this is the last account with accounts.admin');
    a.disabled = !!b.disabled;
    return accView(a);
  },
  resetAccountPassword: (c) => {
    need(c, 'accounts', 'admin');
    const a = c.db.accounts[c.params.uid!];
    if (!a) throw new Err('not-found', 'account not found');
    const t = now();
    const link = `https://opm.example.com/__/auth/action?mode=resetPassword&oobCode=MOCK${randHex(24)}&apiKey=mock`;
    a.lastResetLink = { value: link, setAt: t, setBy: c.me.uid, expiresAt: new Date(Date.now() + 3600_000).toISOString(), validAfter: a.lastSetPassword?.validAfter ?? null };
    return resetLinkView(a);
  },
  revealAccountResetLink: (c) => {
    need(c, 'accounts', 'admin');
    const a = c.db.accounts[c.params.uid!];
    if (!a) throw new Err('not-found', 'account not found');
    if (!a.lastResetLink) throw new Err('gone', 'no reset link stored — make a new one', { reason: 'not-stored' });
    if (Date.parse(a.lastResetLink.expiresAt) <= Date.now()) throw new Err('gone', 'the reset link expired — make a new one', { reason: 'expired' });
    return resetLinkView(a);
  },
  setAccountPassword: (c) => {
    need(c, 'accounts', 'admin');
    const a = c.db.accounts[c.params.uid!];
    if (!a) throw new Err('not-found', 'account not found');
    const t = now();
    const password = genPassword();
    const u = c.db.users.find((x) => x.uid === a.uid);
    if (u) u.password = password;
    else c.db.users.push({ uid: a.uid, email: a.email, password });
    // tokensValidAfterTime moves (sessions end) → the old reset link is not valid any more
    a.lastSetPassword = { value: password, setAt: t, setBy: c.me.uid, validAfter: t };
    a.lastResetLink = null;
    return { value: password, hideAfterSec: REVEAL_SECONDS, setAt: t, setBy: c.me.uid };
  },
  revealAccountPassword: (c) => {
    need(c, 'accounts', 'admin');
    const a = c.db.accounts[c.params.uid!];
    if (!a) throw new Err('not-found', 'account not found');
    const p = a.lastSetPassword;
    if (!p) throw new Err('gone', 'no password stored — set a new one', { reason: 'not-stored' });
    return { value: p.value, hideAfterSec: REVEAL_SECONDS, setAt: p.setAt, setBy: p.setBy };
  },
  listAuditLog: (c) => {
    need(c, 'accounts', 'admin');
    const q = c.query;
    const action = q.get('action');
    const actor = q.get('actor');
    const targetType = q.get('targetType');
    const target = q.get('target');
    const outcome = q.get('outcome');
    const from = q.get('from');
    const to = q.get('to');
    if (outcome && outcome !== 'ok' && outcome !== 'error') throw invalid([issue('outcome', 'V_ENUM', 'outcome must be ok or error')]);
    const list = c.db.auditLog
      .filter((e) => {
        if (action && !auditActionMatches(e.action, action)) return false;
        if (actor) {
          if (actor.includes('@')) {
            if ((e.actorEmail ?? '').toLowerCase() !== actor.trim().toLowerCase()) return false;
          } else if (e.actor !== actor) return false;
        }
        if (targetType && e.targetType !== targetType) return false;
        if (target && e.target !== target) return false;
        if (outcome && (e.outcome ?? 'ok') !== outcome) return false;
        if (from && e.ts < from) return false;
        if (to && e.ts > to) return false;
        return true;
      })
      .sort((a, b) => b.ts.localeCompare(a.ts));
    return page(list, c, Math.min(500, Number(q.get('limit') ?? 100) || 100));
  },

  // deployments
  listDeployments: (c) => {
    const tag = c.query.get('tag');
    const list = Object.values(c.db.deployments)
      .filter((d) => !d.deleted && canSeeDeployment(c.me, targetOf(d)) && (!tag || d.tagSlugs.includes(tag)))
      .sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))
      .map((d) => {
        const agents = c.db.agents[d.deploymentId] ?? {};
        const sites = new Set([...d.draft.sites.map((s) => s.siteSlug), ...Object.keys(agents)]);
        const pend = (c.db.proposals[d.deploymentId] ?? []).filter(isOpen).sort((a, b) => b.revId.localeCompare(a.revId))[0];
        return { deploymentId: d.deploymentId, slug: d.slug, label: d.label, image: d.image, tagSlugs: d.tagSlugs, siteSlugs: [...sites].sort(), sitesOnline: Object.values(agents).filter((a) => a.online).length, sitesTotal: sites.size, head: d.head ? { revId: d.head.revId, seq: d.head.seq } : null, pending: pend ? { revId: pend.revId, seq: pend.seq, status: pend.status } : null, updatedAt: d.updatedAt, branch: (refreshDeployment(c.db, d), d.branch ?? null) };
      });
    return page(list, c, 100);
  },
  createDeployment: (c) => {
    const b = bodyOf<{ label: string; templateId: string | null; tagSlugs: string[]; managerAccountUids: string[]; image?: string | null; from?: import('@opm/shared').BranchPoint | null; message?: string; copySecretValues?: boolean }>(c);
    unknownFields(b, ['label', 'slug', 'templateId', 'tagSlugs', 'managerAccountUids', 'image', 'from', 'message', 'copySecretValues']);
    const okAll = allowed(c.me, 'deployments', 'write');
    const okTag = c.me.access.some((r) => r.resource === 'deployments' && r.actions.includes('write') && r.scope.kind === 'tags' && r.scope.tagSlugs.some((t) => (b.tagSlugs ?? []).includes(t)));
    if (!okAll && !okTag) throw new Err('forbidden', 'you need deployments.write (scope all, or a tag scope that covers one of the tags)');
    const out: ValidationIssue[] = [];
    checkLabel(b.label, 'label', out);
    const slug = pickSlug(Object.values(c.db.deployments).map((x) => x.slug), (b as { slug?: string }).slug, b.label, out, 'd-');
    for (const t of b.tagSlugs ?? []) if (!c.db.tags[t]) out.push(issue('tagSlugs', 'V_REF', `tag ${t} does not exist`));
    for (const u of b.managerAccountUids ?? []) if (!c.db.accounts[u]) out.push(issue('managerAccountUids', 'V_REF', `account ${u} does not exist`));
    if (b.image) iconIssues(b.image, 'image', out, true);
    throwIf(out);
    // §21 #104: `from` (any branch point) wins; templateId = the legacy shortcut for from = that template's head
    const fromP = b.from ?? (b.templateId ? { branch: { kind: 'template' as const, templateId: b.templateId }, at: 'head' as const } : null);
    if (b.templateId && !b.from && !c.db.templates[b.templateId]) throw new Err('not-found', 'template not found');
    const src = fromP ? fromPoint(c, fromP) : null;
    const tpl = src && src.info.ref.kind === 'template' ? c.db.templates[(src.info.ref as { templateId: string }).templateId]! : null;
    const copyValues = !!b.copySecretValues && !!src && src.info.ref.kind !== 'template' && src.info.canSecretsRead;
    if (b.copySecretValues && src && src.info.ref.kind !== 'template' && !src.info.canSecretsRead) throw new Err('forbidden', 'copying secret values needs deployment.secrets.read on the source');
    const id = autoId();
    const t = now();
    const draft: Config = src ? structuredClone(src.config) : { variables: [], secrets: [], files: [], sites: [] };
    draft.secrets = draft.secrets.map((s) => (s.generate ? { ...s, value: genSecret(s.generate.charset, s.generate.length), rotatedAt: t } : copyValues && s.value ? { ...s, rotatedAt: s.rotatedAt ?? t } : { ...s, value: null, rotatedAt: null }));
    const key = genAccessKey();
    const d: Deployment = { deploymentId: id, slug, label: b.label, image: b.image ?? null, tagSlugs: b.tagSlugs ?? [], managerAccountUids: b.managerAccountUids ?? [], srcTemplate: tpl ? { templateId: tpl.templateId, templateVersion: tpl.version, snapshot: structuredClone(tpl), copiedAt: t, baseVersion: tpl.version, baseSnapshot: structuredClone(templateConfig(tpl)), lastSyncedAt: null, lastSyncedBy: null, skipped: [] } : null, draft, draftVersion: 0, head: null, accessKeyHash: '$argon2id$mock', accessKey: key, accessKeyVersion: 1, accessKeyRotatedAt: t, accessKeyRotatedBy: c.me.uid, agentEpoch: 1, cloudActivitySeq: 0, deleted: false, createdAt: t, createdBy: c.me.uid, updatedAt: t, updatedBy: c.me.uid };
    c.db.deployments[id] = d;
    c.db.revisions[id] = [];
    c.db.proposals[id] = [];
    c.db.agents[id] = {};
    c.db.installTokens[id] = [];
    c.db.activities[id] = [];
    c.db.transfers[id] = [];
    c.db.files[id] = [];
    const first = branchHelpers.create(c.db, branchHelpers.depRef(id), d, draft, c.me.uid, src ? { ref: src.info.ref, label: src.info.label, commit: src.commit, config: src.config } : null, b.message, buildCatalog(c.db, draft));
    cloudActivity(c, d, 'deployment.created', `Deployment ${d.label} created${src ? ` from ${src.info.label}${src.commit ? `@${src.commit.seq}` : ''}` : ' (empty)'}`, { label: d.label, templateId: tpl?.templateId ?? null, from: fromP });
    cloudActivity(c, d, 'branch.created', `Commit 1: ${first.message}`, { branch: { kind: 'deployment', deploymentId: id }, commitId: first.commitId, seq: 1, from: fromP });
    return { deployment: depView(d), accessKey: key, hideAfterSec: REVEAL_SECONDS };
  },
  getDeployment: (c) => {
    const d = dep(c);
    refreshDeployment(c.db, d);
    return depView({ ...d, draft: normalizeConfig(d.draft, imageVolumes(c.db)) });
  },
  patchDeployment: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployments', 'write');
    const b = bodyOf<{ label?: string; image?: string | null; tagSlugs?: string[]; managerAccountUids?: string[] }>(c);
    unknownFields(b, ['label', 'image', 'tagSlugs', 'managerAccountUids', 'slug']);
    if ('slug' in b) throw invalid([issue('slug', 'V_IMMUTABLE', 'the slug never changes after create (it names the GitHub branches)')]);
    const out: ValidationIssue[] = [];
    if (b.label !== undefined) checkLabel(b.label, 'label', out);
    for (const t of b.tagSlugs ?? []) if (!c.db.tags[t]) out.push(issue('tagSlugs', 'V_REF', `tag ${t} does not exist`));
    for (const u of b.managerAccountUids ?? []) if (!c.db.accounts[u]) out.push(issue('managerAccountUids', 'V_REF', `account ${u} does not exist`));
    if (b.image) iconIssues(b.image, 'image', out, true);
    throwIf(out);
    const changed = Object.keys(b).filter((k) => JSON.stringify((d as unknown as Record<string, unknown>)[k]) !== JSON.stringify((b as Record<string, unknown>)[k]));
    Object.assign(d, b, { updatedAt: now(), updatedBy: c.me.uid });
    if (b.image === '') d.image = null;
    cloudActivity(c, d, 'deployment.updated', `Deployment updated (${changed.join(', ') || 'no change'})`, { changed });
    return depView(d);
  },
  deleteDeployment: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployments', 'write');
    const b = bodyOf<{ confirm: string }>(c);
    if (b.confirm !== d.label) throw invalid([issue('confirm', 'V_FORMAT', 'confirm must equal the deployment label')]);
    d.deleted = true;
    d.agentEpoch += 1;
    cloudActivity(c, d, 'deployment.deleted', `Deployment ${d.label} deleted`, { label: d.label });
    return { deleted: true };
  },
  // §21 #105: no server working copy — the editors commit (POST …/commits { message, baseCommitId, config })
  putDraftSection: () => {
    throw new Err('gone', 'there is no draft any more (§21 #105): commit the change set', { hint: 'POST /v1/deployments/{deploymentId}/commits { message, baseCommitId, config }' });
  },
  putDraftSite: () => {
    throw new Err('gone', 'there is no draft any more (§21 #105): commit the change set', { hint: 'POST /v1/deployments/{deploymentId}/commits { message, baseCommitId, config }' });
  },
  revealSecret: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.secrets', 'read');
    const s = d.draft.secrets.find((x) => x.secretSlug === c.params.secretSlug);
    if (!s || !s.value) throw new Err('not-found', 'secret has no value');
    cloudActivity(c, d, 'secret.revealed', `Secret ${s.secretSlug} revealed in the cloud`, { secretSlug: s.secretSlug, actor: c.me.uid, where: 'cloud' });
    return { value: s.value, hideAfterSec: REVEAL_SECONDS };
  },
  regenerateSecret: () => {
    throw new Err('gone', '"Generate new value" is a change committed like any other (§21 #105)', { hint: 'commit with secrets[].generateNow' });
  },
  // ── §21 #94 template sync
  getTemplateSyncStatus: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.config', 'read');
    const l = linkOf(d);
    const off: TemplateSyncStatus = { linked: false, link: null, templateLabel: null, currentVersion: null, templateDeleted: false, newerVersions: 0, pendingChanges: 0, conflicts: 0 };
    if (!l) return off;
    const tpl = c.db.templates[l.templateId];
    if (!tpl) return { ...off, linked: true, link: linkView(l), templateLabel: l.snapshot.label, templateDeleted: true };
    const k = countTemplateSync(planTemplateSync(l.baseSnapshot, tplCfg(c, tpl), d.draft, l.skipped));
    return { ...off, linked: true, link: linkView(l), templateLabel: tpl.label, currentVersion: tpl.version, newerVersions: Math.max(0, tpl.version - l.baseVersion), pendingChanges: k.theirs + k.conflict + k.default, conflicts: k.conflict } satisfies TemplateSyncStatus;
  },
  getTemplateSyncPlan: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.config', 'read');
    need(c, 'templates', 'read');
    const s = syncSides(c, d, c.query.get('templateId'));
    const changes = planTemplateSync(s.base, s.theirs, d.draft, s.relink ? [] : (s.link?.skipped ?? []));
    const t = s.tpl;
    return {
      link: s.link ? linkView(s.link) : null,
      template: { templateId: t.templateId, label: t.label, icon: t.icon, version: t.version, updatedAt: t.updatedAt, updatedBy: t.updatedBy },
      relink: s.relink,
      baseVersion: s.link?.baseVersion ?? null,
      draftVersion: d.draftVersion,
      base: configView(s.base ?? { variables: [], secrets: [], files: [], sites: [] }),
      theirs: configView(s.theirs),
      ours: configView(d.draft),
      changes, // secret items are definitions only (planTemplateSync never carries a value)
      counts: countTemplateSync(changes),
    } satisfies TemplateSyncPlan;
  },
  previewTemplateSync: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.config', 'read');
    need(c, 'templates', 'read');
    const r = runSync(c, d);
    const config = r.config;
    const v = validateConfig(config, vctx(c.db, 'draft', d.deploymentId));
    return {
      draft: configView(config),
      changes: diffConfigs(d.draft, config, (x) => x, (slug) => (d.draft.secrets.find((x) => x.secretSlug === slug)?.value ?? null) !== (config.secrets.find((x) => x.secretSlug === slug)?.value ?? null)),
      validation: { ok: v.ok && r.unresolved.length === 0, issues: v.list },
      unresolved: r.unresolved,
      taken: r.taken.length,
      skipped: r.notTaken.length,
      missing: templateSyncMissing(config),
    } satisfies TemplateSyncPreviewResponse;
  },
  applyTemplateSync: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.config', 'write');
    need(c, 'templates', 'read');
    const r = runSync(c, d);
    if (r.taken.some((k) => k.startsWith('secrets/'))) needDep(c, d, 'deployment.secrets', 'write');
    if (r.unresolved.length) throw invalid(r.unresolved.map((k) => issue(k, 'V_SYNC_UNRESOLVED', 'choose take template / keep mine for this conflict')), `${r.unresolved.length} conflict${r.unresolved.length === 1 ? '' : 's'} without a choice`);
    const config = normalizeConfig(r.config, imageVolumes(c.db));
    const v = validateConfig(config, vctx(c.db, 'draft', d.deploymentId));
    if (!v.ok) throw invalid(v.errors);
    const t = now();
    const prev = r.side.link;
    const tpl = r.side.tpl;
    const changed = JSON.stringify(config) !== JSON.stringify(d.draft);
    if (changed) {
      d.draft = config;
      d.draftVersion += 1;
    }
    const base: SrcTemplate = r.side.relink || !prev ? { templateId: tpl.templateId, templateVersion: tpl.version, snapshot: structuredClone(tpl), copiedAt: t, baseVersion: tpl.version, baseSnapshot: templateConfig(tpl), lastSyncedAt: null, lastSyncedBy: null, skipped: [] } : prev;
    const link: SrcTemplate = { ...base, baseVersion: tpl.version, baseSnapshot: structuredClone(templateConfig(r.side.theirs)), lastSyncedAt: t, lastSyncedBy: c.me.uid, skipped: nextSkipRecords(r.notTaken, r.side.relink ? [] : (prev?.skipped ?? []), tpl.version, t, c.me.uid) };
    d.srcTemplate = link;
    d.updatedAt = t;
    d.updatedBy = c.me.uid;
    const conflicts = r.plan.filter((x) => x.kind === 'conflict').length;
    const summary = r.side.relink ? `Template changed to "${tpl.label}" v${tpl.version} (${r.taken.length} taken, ${r.notTaken.length} skipped)` : `Template "${tpl.label}" synced v${prev?.baseVersion} → v${tpl.version} (${r.taken.length} taken, ${r.notTaken.length} skipped)`;
    cloudActivity(c, d, 'draft.template-synced', summary, { templateId: tpl.templateId, fromVersion: prev?.baseVersion ?? null, toVersion: tpl.version, relinked: r.side.relink, ...(r.side.relink && prev ? { previousTemplateId: prev.templateId } : {}), taken: r.taken.length, skipped: r.notTaken.length, conflicts, draftVersion: d.draftVersion, keys: r.taken.slice(0, 100) });
    return { draftVersion: d.draftVersion, validation: { ok: true, issues: v.list }, link: linkView(link), taken: r.taken.length, skipped: r.notTaken.length, missing: templateSyncMissing(config) };
  },
  unlinkTemplate: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.config', 'write');
    const b = bodyOf<{ confirm?: unknown }>(c);
    if (b.confirm !== true) throw invalid([issue('confirm', 'V_REQUIRED', 'confirm must be true')]);
    const l = linkOf(d);
    if (!l) throw new Err('not-found', 'the deployment is not linked to a template');
    d.srcTemplate = null;
    d.updatedAt = now();
    d.updatedBy = c.me.uid;
    cloudActivity(c, d, 'deployment.template-unlinked', `Template "${l.snapshot.label}" unlinked (the draft stays as it is)`, { templateId: l.templateId, baseVersion: l.baseVersion });
    return { link: null };
  },
  getDiff: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.config', 'read');
    return diff(c.db, d);
  },
  publish: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.config', 'publish');
    const b = bodyOf<{ ifRevision: number; summary: string }>(c);
    if (b.ifRevision !== d.draftVersion) throw new Err('conflict', `draft is at version ${d.draftVersion}`, { current: d.draftVersion });
    if (!b.summary || !b.summary.trim() || b.summary.length > LIMITS.summary) throw invalid([issue('summary', 'V_LENGTH', 'summary must be 1–200 characters')]);
    const df = diff(c.db, d);
    if (df.blockers.length) throw invalid(df.blockers, 'the draft has publish blockers');
    const props = c.db.proposals[d.deploymentId]!;
    if (props.some((p) => p.status === 'approved' || p.status === 'applying')) throw new Err('conflict', 'an update is running');
    const headRev = d.head ? c.db.revisions[d.deploymentId]!.find((r) => r.revId === d.head!.revId) : null;
    const config = structuredClone(d.draft);
    const catalog = buildCatalog(c.db, config);
    const rev: Revision = { revId: ulid(), seq: (d.head?.seq ?? 0) + 1, parentRevId: d.head?.revId ?? null, origin: 'cloud', actor: c.me.uid, createdAt: now(), config, hash: pseudoHash(config), catalog, catalogHash: pseudoHash(catalog), summary: b.summary.trim(), kind: 'change', rollbackOf: null };
    c.db.revisions[d.deploymentId]!.push(rev);
    const sites: Proposal['sites'] = {};
    for (const s of new Set([...(headRev?.config.sites ?? []).map((x) => x.siteSlug), ...config.sites.map((x) => x.siteSlug)])) sites[s] = { staging: 'pending', progress: 0, error: null, updatedAt: null };
    for (const p of props)
      if (['published', 'staging', 'ready'].includes(p.status)) {
        p.status = 'superseded';
        p.supersededBy = rev.revId;
        p.updatedAt = now();
        cloudActivity(c, d, 'proposal.superseded', `Proposal ${p.seq} superseded by ${rev.seq}`, { revId: p.revId, byRevId: rev.revId });
      }
    const p: Proposal & { _sim?: number } = { revId: rev.revId, parentRevId: rev.parentRevId, seq: rev.seq, summary: rev.summary, origin: 'cloud', kind: 'change', createdAt: rev.createdAt, createdBy: c.me.uid, status: 'published', sites, approvedBy: null, approvedAt: null, updateId: null, result: null, supersededBy: null, updatedAt: now(), _sim: Date.now() };
    props.push(p);
    d.agentEpoch += 1;
    cloudActivity(c, d, 'proposal.published', `Proposal ${rev.seq} published: ${rev.summary}`, { revId: rev.revId, parentRevId: rev.parentRevId, seq: rev.seq, summary: rev.summary });
    const { _sim: _s, ...pub } = p;
    void _s;
    return { proposal: pub, revision: revSummary(rev) };
  },
  resetDraftToHead: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.config', 'write');
    const b = bodyOf<{ confirm: boolean }>(c);
    if (b.confirm !== true) throw invalid([issue('confirm', 'V_REQUIRED', 'confirm must be true')]);
    const headRev = d.head ? c.db.revisions[d.deploymentId]!.find((r) => r.revId === d.head!.revId) : null;
    if (!headRev) throw new Err('not-found', 'the deployment has no head yet');
    d.draft = structuredClone(headRev.config);
    d.draftVersion += 1;
    d.updatedAt = now();
    d.updatedBy = c.me.uid;
    cloudActivity(c, d, 'draft.reset-to-head', `Draft reset to head seq ${headRev.seq}`, { headRevId: headRev.revId });
    return { draftVersion: d.draftVersion };
  },
  listProposals: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.config', 'read');
    const limit = Number(c.query.get('limit') ?? 20);
    return { items: (c.db.proposals[d.deploymentId] ?? []).slice().sort((a, b) => b.revId.localeCompare(a.revId)).slice(0, limit).map(({ _sim, ...p }) => (void _sim, p)) };
  },
  listRevisions: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.config', 'read');
    return page((c.db.revisions[d.deploymentId] ?? []).slice().sort((a, b) => b.seq - a.seq).map(revSummary), c, 100);
  },
  getRevision: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.config', 'read');
    const r = (c.db.revisions[d.deploymentId] ?? []).find((x) => x.revId === c.params.revId);
    if (!r) throw new Err('not-found', 'revision not found');
    return { ...r, config: configView(r.config) };
  },
  getSetup: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.setup', 'read');
    const agents = c.db.agents[d.deploymentId] ?? {};
    const headRev = d.head ? c.db.revisions[d.deploymentId]!.find((r) => r.revId === d.head!.revId) : null;
    const slugs = new Set([...d.draft.sites.map((s) => s.siteSlug), ...(headRev?.config.sites ?? []).map((s) => s.siteSlug), ...Object.keys(agents)]);
    const t = Date.now();
    return {
      accessKey: { version: d.accessKeyVersion, rotatedAt: d.accessKeyRotatedAt, rotatedBy: d.accessKeyRotatedBy, stored: !!d.accessKey },
      installDeps: { dockerVersion: '28.5.2', minDockerApi: '1.41', agentVersion: MOCK_AGENT_RELEASE.version },
      apiBase: 'https://app.opm.example',
      agentRelease: { version: MOCK_AGENT_RELEASE.version, notes: MOCK_AGENT_RELEASE.notes },
      agentUpgradeRun: d.agentUpgradeRun ? strip(d.agentUpgradeRun as MockRun) : null,
      serverTime: serverNowIso(),
      sites: [...slugs].sort().map((slug) => {
        const a = agents[slug];
        const up = upgradeTokenOf(c.db.installTokens[d.deploymentId] ?? [], a);
        const label = d.draft.sites.find((s) => s.siteSlug === slug)?.label ?? headRev?.config.sites.find((s) => s.siteSlug === slug)?.label ?? slug;
        return {
          siteSlug: slug,
          label,
          inDraft: d.draft.sites.some((s) => s.siteSlug === slug),
          inHead: !!headRev?.config.sites.some((s) => s.siteSlug === slug),
          agent: a ? { agentVersion: a.agentVersion, enrolledAt: a.enrolledAt, lastSeenAt: a.lastSeenAt, online: a.online, hostname: a.hostname, ip: a.ip, keyVersion: a.keyVersion, os: a.os, arch: a.arch, advertiseHost: a.advertiseHost, detectedAddresses: a.detectedAddresses ?? [], clockSkew: a.clockSkew ?? null } : null,
          upgradeCommand: a ? { tokenHash: up?.tokenHash ?? null, kind: up ? (up.kind ?? 'install') : null, stored: !!up?.token, createdAt: up?.createdAt ?? null, createdBy: up?.createdBy ?? null } : null,
          guiUrls: a ? mockGuiUrls(a) : [],
          defaultEdge: a?.edge?.defaultEdge ?? null,
          portAlerts: (a?.edge?.bindErrors ?? [])
            .filter((b) => b.purpose === 'edge-http' || b.purpose === 'edge-https')
            .map((b) => ({ port: b.port, purpose: b.purpose as 'edge-http' | 'edge-https', owner: b.owner ?? null, message: `port ${b.port} is used by ${b.owner ?? 'another program'} — stop it or change edge.${b.purpose === 'edge-https' ? 'httpsPort' : 'httpPort'}` })),
          upgrade: upgradeView(a),
          tokens: (c.db.installTokens[d.deploymentId] ?? [])
            .filter((x) => x.siteSlug === slug)
            .sort((x, y) => y.createdAt.localeCompare(x.createdAt))
            .slice(0, 10)
            .map(({ token, ...x }) => {
              const state = tokenState(x, t);
              const kind = x.kind ?? 'install';
              return { ...x, kind, expiresAt: x.expiresAt ?? null, state, stored: kind === 'install' && state === 'active' && !!token, upgradeCommand: !!up && up.tokenHash === x.tokenHash };
            }),
        };
      }),
    };
  },
  rotateAccessKey: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.setup', 'write');
    d.accessKeyVersion += 1;
    d.accessKeyRotatedAt = now();
    d.accessKeyRotatedBy = c.me.uid;
    d.agentEpoch += 1;
    for (const a of Object.values(c.db.agents[d.deploymentId] ?? {})) (a as unknown as { _keyAt: number })._keyAt = Date.now() + 5000;
    d.accessKey = genAccessKey();
    cloudActivity(c, d, 'access-key.rotated', `Access key rotated (version ${d.accessKeyVersion})`, { version: d.accessKeyVersion });
    return { accessKey: d.accessKey, value: d.accessKey, hideAfterSec: REVEAL_SECONDS, version: d.accessKeyVersion, rotatedAt: d.accessKeyRotatedAt };
  },
  revealAccessKey: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.setup', 'write');
    if (!d.accessKey) throw new Err('gone', 'the access key of this deployment is not stored (created before keys were kept) — regenerate to view', { reason: 'not-stored' });
    cloudActivity(c, d, 'access-key.revealed', `Access key revealed (version ${d.accessKeyVersion})`, { version: d.accessKeyVersion });
    return { accessKey: d.accessKey, value: d.accessKey, hideAfterSec: REVEAL_SECONDS, version: d.accessKeyVersion, rotatedAt: d.accessKeyRotatedAt };
  },
  createInstallToken: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.setup', 'write');
    const b = bodyOf<{ siteSlug: string; replaceActive?: boolean }>(c);
    unknownFields(b, ['siteSlug', 'replaceActive']);
    if (!d.draft.sites.some((s) => s.siteSlug === b.siteSlug)) throw new Err('not-found', `site ${b.siteSlug} is not in the draft`);
    if (c.db.agents[d.deploymentId]?.[b.siteSlug]) throw new Err('conflict', `site ${b.siteSlug} is enrolled; revoke the enrollment first`);
    const raw = crypto.getRandomValues(new Uint8Array(32));
    const token = 'opi_' + b64(raw).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    const tokenHash = randHex(64);
    const t = now();
    const expiresAt = new Date(Date.now() + 24 * 3600_000).toISOString();
    const list = (c.db.installTokens[d.deploymentId] ??= []);
    if (b.replaceActive)
      for (const old of list.filter((x) => x.siteSlug === b.siteSlug && (x.kind ?? 'install') === 'install' && tokenState(x, Date.now()) === 'active')) {
        old.revokedAt = t;
        old.token = null;
        cloudActivity(c, d, 'install-token.revoked', `Install command for ${old.siteSlug} revoked (regenerated)`, { siteSlug: old.siteSlug, tokenHash: old.tokenHash });
      }
    list.push({ tokenHash, siteSlug: b.siteSlug, token, expiresAt, usedAt: null, revokedAt: null, createdAt: t, createdBy: c.me.uid });
    cloudActivity(c, d, 'install-token.created', `Install command created for ${b.siteSlug}`, { siteSlug: b.siteSlug, tokenHash, expiresAt });
    const command = installCommand(token);
    return { command, value: command, hideAfterSec: REVEAL_SECONDS, token, tokenHash, siteSlug: b.siteSlug, expiresAt };
  },
  revealInstallCommand: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.setup', 'write');
    const tk = (c.db.installTokens[d.deploymentId] ?? []).find((x) => x.tokenHash === c.params.tokenHash);
    if (!tk) throw new Err('not-found', 'install command not found');
    if (tk.kind === 'maintenance') throw new Err('gone', 'this is the upgrade command of an enrolled site', { reason: 'maintenance' });
    const state = tokenState(tk, Date.now());
    if (state !== 'active') throw new Err('gone', `the install command was ${state}`, { reason: state });
    if (!tk.token) throw new Err('gone', 'the install command is not stored (created before commands were kept) — regenerate to view', { reason: 'not-stored' });
    cloudActivity(c, d, 'install-token.revealed', `Install command for ${tk.siteSlug} revealed`, { siteSlug: tk.siteSlug, tokenHash: tk.tokenHash });
    const command = installCommand(tk.token);
    return { command, value: command, hideAfterSec: REVEAL_SECONDS, token: tk.token, tokenHash: tk.tokenHash, siteSlug: tk.siteSlug, expiresAt: tk.expiresAt ?? '' };
  },
  // §21 #92 the enrolled site's "Upgrade / reinstall command"
  revealUpgradeCommand: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.setup', 'write');
    const slug = c.params.siteSlug!;
    const a = c.db.agents[d.deploymentId]?.[slug];
    if (!a) throw new Err('not-found', `site ${slug} is not enrolled (create an install command instead)`);
    const tk = upgradeTokenOf(c.db.installTokens[d.deploymentId] ?? [], a);
    if (!tk?.token) throw new Err('gone', 'the upgrade command of this site is not stored (its token was created before commands were kept) — Regenerate to view a new one', { reason: 'not-stored' });
    const kind = tk.kind ?? 'install';
    cloudActivity(c, d, 'install-command.revealed', `Upgrade / reinstall command for site ${slug} viewed`, { siteSlug: slug, tokenHash: tk.tokenHash, kind });
    const command = installCommand(tk.token);
    return { command, value: command, hideAfterSec: REVEAL_SECONDS, token: tk.token, tokenHash: tk.tokenHash, siteSlug: slug, kind, enrolledAt: a.enrolledAt };
  },
  regenerateUpgradeCommand: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.setup', 'write');
    const slug = c.params.siteSlug!;
    const a = c.db.agents[d.deploymentId]?.[slug];
    if (!a) throw new Err('not-found', `site ${slug} is not enrolled (create an install command instead)`);
    const list = (c.db.installTokens[d.deploymentId] ??= []);
    const t = now();
    const old = enrollmentTokens(list, a);
    for (const o of old) {
      o.revokedAt = t;
      o.token = null;
    }
    const raw = crypto.getRandomValues(new Uint8Array(32));
    const token = 'opi_' + b64(raw).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    const tokenHash = randHex(64);
    list.push({ tokenHash, siteSlug: slug, kind: 'maintenance', token, expiresAt: null, boundEnrolledAt: a.enrolledAt, usedAt: null, revokedAt: null, createdAt: t, createdBy: c.me.uid });
    const revoked = old.map((o) => o.tokenHash);
    cloudActivity(c, d, 'install-command.regenerated', `Upgrade / reinstall command for site ${slug} regenerated${revoked.length ? ' (the previous command no longer works)' : ''}`, { siteSlug: slug, tokenHash, revoked });
    const command = installCommand(token);
    return { command, value: command, hideAfterSec: REVEAL_SECONDS, token, tokenHash, siteSlug: slug, kind: 'maintenance', enrolledAt: a.enrolledAt, _revoked: revoked };
  },
  revokeInstallToken: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.setup', 'write');
    const tk = (c.db.installTokens[d.deploymentId] ?? []).find((x) => x.tokenHash === c.params.tokenHash);
    if (!tk) throw new Err('not-found', 'token not found');
    if (tk.usedAt) throw new Err('gone', 'the token was already used');
    tk.revokedAt = now();
    tk.token = null;
    cloudActivity(c, d, 'install-token.revoked', `Install command for ${tk.siteSlug} revoked`, { siteSlug: tk.siteSlug, tokenHash: tk.tokenHash });
    return { revoked: true };
  },
  revokeAgent: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.setup', 'write');
    const b = bodyOf<{ confirm: string }>(c);
    if (b.confirm !== c.params.siteSlug) throw invalid([issue('confirm', 'V_FORMAT', 'confirm must equal the site slug')]);
    const ag = c.db.agents[d.deploymentId]?.[c.params.siteSlug!];
    if (!ag) throw new Err('not-found', 'site is not enrolled');
    // §21 #92 the enrollment's upgrade command ends with it
    for (const o of enrollmentTokens(c.db.installTokens[d.deploymentId] ?? [], ag)) {
      if (o.kind === 'maintenance') o.revokedAt = now();
      o.token = null;
    }
    delete c.db.agents[d.deploymentId]![c.params.siteSlug!];
    d.agentEpoch += 1;
    cloudActivity(c, d, 'agent.revoked', `Enrollment of ${c.params.siteSlug} revoked`, { siteSlug: c.params.siteSlug });
    return { revoked: true };
  },
  // §21 #90 cloud "Upgrade agent" / "Upgrade all" (never automatic)
  requestAgentUpgrade: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.setup', 'write');
    const slug = c.params.siteSlug!;
    const b = bodyOf<{ version: string; confirm: string }>(c);
    unknownFields(b, ['version', 'confirm']);
    const a = c.db.agents[d.deploymentId]?.[slug];
    if (!a) throw invalid([issue('siteSlug', 'V_NOT_ENROLLED', `site ${slug} is not enrolled`)], `site ${slug} is not enrolled`);
    if (b.confirm !== slug) throw invalid([issue('confirm', 'V_FORMAT', 'confirm must equal the site slug')]);
    if (b.version !== MOCK_AGENT_RELEASE.version) throw new Err('stale', `the pinned agent release moved to ${MOCK_AGENT_RELEASE.version} — reload and request again`, { version: MOCK_AGENT_RELEASE.version });
    if (!supportsGuiUpgrade(a.agentVersion)) throw new Err('invalid', `agent ${a.agentVersion} cannot upgrade itself`, { hint: UPGRADE_HINT, errors: [issue('siteSlug', 'V_AGENT_TOO_OLD', UPGRADE_HINT)] });
    if (!agentUpgradeAvailable(a.agentVersion, MOCK_AGENT_RELEASE.version)) throw invalid([issue('version', 'V_ALREADY', `site ${slug} already runs ${a.agentVersion}`)], `site ${slug} already runs ${a.agentVersion}`);
    if (a.upgradeRequest) throw new Err('conflict', `an upgrade of ${slug} is already queued`);
    if (a.upgrade && ACTIVE_PHASES.has(a.upgrade.phase)) throw new Err('conflict', `an upgrade of ${slug} is running`);
    if (d.agentUpgradeRun?.status === 'running') throw new Err('conflict', 'an "Upgrade all" run is going');
    const req = newUpgradeRequest(d, a, { email: c.me.email, uid: c.me.uid }, null);
    cloudActivity(c, d, 'agent.upgrade-requested', `Agent upgrade of ${slug} to ${req.version} requested`, { requestId: req.requestId, siteSlug: slug, version: req.version });
    return { request: req };
  },
  cancelAgentUpgrade: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.setup', 'write');
    const slug = c.params.siteSlug!;
    const a = c.db.agents[d.deploymentId]?.[slug];
    const req = a?.upgradeRequest;
    if (!a || !req) throw new Err('not-found', `no queued upgrade request for ${slug}`);
    if (a.upgrade?.requestId === req.requestId) throw new Err('conflict', `the agent of ${slug} already started the upgrade — it cannot be cancelled now`);
    if (req.runId && d.agentUpgradeRun?.runId === req.runId && d.agentUpgradeRun.status === 'running') throw new Err('conflict', 'this request belongs to an "Upgrade all" run — cancel the run');
    a.upgradeRequest = null;
    d.agentEpoch += 1;
    cloudActivity(c, d, 'agent.upgrade-cancelled', `Agent upgrade of ${slug} cancelled`, { requestId: req.requestId, siteSlug: slug });
    return { cancelled: true };
  },
  upgradeAllAgents: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.setup', 'write');
    const b = bodyOf<{ version: string; confirm: string }>(c);
    unknownFields(b, ['version', 'confirm']);
    if (b.confirm !== 'UPGRADE') throw invalid([issue('confirm', 'V_FORMAT', 'type UPGRADE to confirm')]);
    if (b.version !== MOCK_AGENT_RELEASE.version) throw new Err('stale', `the pinned agent release moved to ${MOCK_AGENT_RELEASE.version} — reload and request again`, { version: MOCK_AGENT_RELEASE.version });
    if (d.agentUpgradeRun?.status === 'running') throw new Err('conflict', 'an "Upgrade all" run is already going');
    const agents = Object.values(c.db.agents[d.deploymentId] ?? {}).sort((x, y) => x.siteSlug.localeCompare(y.siteSlug));
    const busy = agents.find((a) => a.upgradeRequest || (a.upgrade && ACTIVE_PHASES.has(a.upgrade.phase)));
    if (busy) throw new Err('conflict', `an upgrade of ${busy.siteSlug} is queued or running — wait for it or cancel it first`);
    const below = agents.filter((a) => agentUpgradeAvailable(a.agentVersion, MOCK_AGENT_RELEASE.version));
    if (!below.some((a) => supportsGuiUpgrade(a.agentVersion))) throw invalid([issue('', 'V_NOTHING_TO_UPGRADE', `no site can be upgraded to ${MOCK_AGENT_RELEASE.version} from here`)], `no site can be upgraded to ${MOCK_AGENT_RELEASE.version} from here`);
    const run: MockRun = {
      runId: ulid(),
      origin: 'cloud',
      coordinator: null,
      version: MOCK_AGENT_RELEASE.version,
      requestedBy: c.me.email,
      requestedAt: now(),
      status: 'running',
      sites: below.map((a) => (supportsGuiUpgrade(a.agentVersion) ? { siteSlug: a.siteSlug, status: 'pending', from: a.agentVersion, requestId: null, error: null, at: null } : { siteSlug: a.siteSlug, status: 'skipped', from: a.agentVersion, requestId: null, error: UPGRADE_HINT, at: now() })),
      finishedAt: null,
      _uid: c.me.uid,
    };
    d.agentUpgradeRun = run;
    cloudActivity(c, d, 'agent.upgrade-requested', `Upgrade all sites to ${run.version} started (${run.sites.filter((x) => x.status === 'pending').length} sites)`, { runId: run.runId, version: run.version, sites: run.sites.map((x) => x.siteSlug) });
    runNext(c.db, d);
    return { run: strip(run) };
  },
  cancelAgentUpgradeRun: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.setup', 'write');
    const run = d.agentUpgradeRun as MockRun | null | undefined;
    if (!run) throw new Err('not-found', 'no "Upgrade all" run');
    if (run.status !== 'running') throw new Err('conflict', `the run is ${run.status}`);
    for (const x of run.sites) {
      if (x.status === 'pending') x.status = 'cancelled';
      if (x.status === 'requested') {
        const a = c.db.agents[d.deploymentId]?.[x.siteSlug];
        if (a?.upgradeRequest?.requestId === x.requestId && a.upgrade?.requestId !== x.requestId) {
          a.upgradeRequest = null;
          x.status = 'cancelled';
          x.at = now();
        }
      }
    }
    run.status = 'cancelled';
    run.finishedAt = now();
    d.agentEpoch += 1;
    cloudActivity(c, d, 'agent.upgrade-cancelled', 'Upgrade all cancelled', { runId: run.runId });
    return { run: strip(run) };
  },
  getStatus: (c) => {
    const d = dep(c);
    const agents = c.db.agents[d.deploymentId] ?? {};
    const headRev = d.head ? c.db.revisions[d.deploymentId]!.find((r) => r.revId === d.head!.revId) : null;
    const slugs = [...new Set([...d.draft.sites.map((s) => s.siteSlug), ...(headRev?.config.sites ?? []).map((s) => s.siteSlug), ...Object.keys(agents)])].sort();
    const revs = c.db.revisions[d.deploymentId] ?? [];
    return {
      serverTime: serverNowIso(),
      sites: slugs.map((slug) => {
        const a = agents[slug];
        return {
          siteSlug: slug,
          label: d.draft.sites.find((s) => s.siteSlug === slug)?.label ?? slug,
          inHead: !!headRev?.config.sites.some((s) => s.siteSlug === slug),
          enrolled: !!a,
          online: !!a?.online,
          lastSeenAt: a?.lastSeenAt ?? null,
          relayVia: a?.relayVia ?? null,
          advertiseHost: a?.advertiseHost ?? null,
          detectedAddresses: a?.detectedAddresses ?? [],
          agentVersion: a?.agentVersion ?? null,
          headRevId: a?.headRevId ?? null,
          headSeq: a?.headRevId ? (revs.find((r) => r.revId === a.headRevId)?.seq ?? null) : null,
          staging: a?.staging ?? null,
          keyVersion: a?.keyVersion ?? null,
          metrics: a?.metrics ?? null,
          edge: a?.edge ?? null,
          upgrade: upgradeView(a),
          clockSkew: a?.clockSkew ?? null,
        };
      }),
      uptime: slugs.filter((s) => agents[s]).map((slug) => ({ siteSlug: slug, days: uptimeDays(agents[slug]!.enrolledAt, slug) })),
    };
  },
  listActivities: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.activities', 'read');
    const q = c.query;
    const site = q.get('site');
    const type = q.get('type');
    const from = q.get('from');
    const to = q.get('to');
    let list = (c.db.activities[d.deploymentId] ?? []).slice();
    if (site) list = list.filter((a) => (site === 'cloud' ? a.siteSlug === '_cloud' : a.siteSlug === site));
    if (q.get('origin')) list = list.filter((a) => a.origin === q.get('origin'));
    if (type) list = list.filter((a) => (type.endsWith('.') ? a.type.startsWith(type) : a.type === type || a.type.startsWith(type + '.')));
    if (q.get('actor')) list = list.filter((a) => (a.actor ?? '').toLowerCase().includes(q.get('actor')!.toLowerCase()));
    if (from) list = list.filter((a) => a.ts >= from);
    if (to) list = list.filter((a) => a.ts <= to);
    if (q.get('via')) list = list.filter((a) => a.uploadedVia === q.get('via'));
    list.sort((a, b) => b.ts.localeCompare(a.ts) || a.siteSlug.localeCompare(b.siteSlug) || b.seq - a.seq);
    return page(list, c, Math.min(500, Number(q.get('limit') ?? 100)));
  },
  uploadActivities: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.activities', 'write');
    const bytes = c.raw ?? new Uint8Array();
    if (!bytes.length) throw invalid([issue('body', 'V_REQUIRED', 'empty file')], 'empty file');
    if (bytes.length > 24 * 1024 * 1024) throw new Err('too-large', 'bundle larger than 24 MiB');
    const text = new TextDecoder().decode(bytes.slice(0, Math.min(bytes.length, 65536)));
    if (/BADSIG/.test(text)) throw invalid([issue('signature.sig', 'V_FORMAT', 'signature does not verify with the key of site app')], 'bad signature');
    const tampered = /TAMPER/.test(text);
    const sites = Object.keys(c.db.agents[d.deploymentId] ?? {}).map((s) => ({ siteSlug: s, fromSeq: 1, toSeq: 12 }));
    cloudActivity(c, d, 'activities.uploaded', `Activity log uploaded by hand (${tampered ? 9 : 12} known, ${tampered ? 3 : 0} rejected)`, { inserted: 0, skipped: tampered ? 9 : 12, rejected: tampered ? 3 : 0, sites: sites.map((x) => x.siteSlug) });
    return { inserted: 0, skipped: tampered ? 9 : 12, rejected: tampered ? 3 : 0, errors: tampered ? [{ activityId: 'app:10', reason: 'hash mismatch (entry changed after signing)' }, { activityId: 'app:11', reason: 'chain broken before this entry' }, { activityId: 'app:12', reason: 'chain broken before this entry' }] : [], sites, signatureOk: true };
  },
  listTransfers: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.transfers', 'read');
    const dir = c.query.get('direction');
    return { items: (c.db.transfers[d.deploymentId] ?? []).filter((t) => t.siteSlug === c.params.siteSlug && (!dir || t.direction === dir)).sort((a, b) => b.createdAt.localeCompare(a.createdAt)) };
  },
  createTransfer: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.transfers', 'write');
    const b = bodyOf<{ fileName: string; sizeBytes: number; sha256: string; note?: string }>(c);
    unknownFields(b, ['fileName', 'sizeBytes', 'sha256', 'note']);
    const out: ValidationIssue[] = [];
    if (!b.fileName || b.fileName.length > 255 || /[/\\\0]/.test(b.fileName)) out.push(issue('fileName', 'V_FORMAT', 'file name must be 1–255 characters without / \\ NUL'));
    if (!RE.sha256.test(b.sha256 ?? '')) out.push(issue('sha256', 'V_FORMAT', 'sha256 must be sha256:<hex>'));
    if ((b.note ?? '').length > 500) out.push(issue('note', 'V_LENGTH', 'note must be ≤ 500 characters'));
    throwIf(out);
    if (b.sizeBytes > 2 * 1024 * 1024 * 1024) throw new Err('too-large', 'transfers are limited to 2 GiB');
    if (!d.draft.sites.some((s) => s.siteSlug === c.params.siteSlug) && !c.db.agents[d.deploymentId]?.[c.params.siteSlug!]) throw new Err('not-found', 'site not found');
    const tid = autoId();
    const t = now();
    const transfer: Transfer = { transferId: tid, deploymentId: d.deploymentId, siteSlug: c.params.siteSlug!, direction: 'to-site', fileName: b.fileName, sizeBytes: b.sizeBytes, sha256: b.sha256, storagePath: `transfers/${d.deploymentId}/${tid}`, uploadId: null, createdAt: t, createdBy: c.me.uid, note: b.note ?? '', expiresAt: null, status: 'uploading', savedPath: null, error: null, fetchedAt: null };
    const u = newUpload(c, 'transfer', b.sizeBytes, b.sha256, d.deploymentId, tid);
    transfer.uploadId = u.uploadId;
    c.db.transfers[d.deploymentId]!.push(transfer);
    return { transfer, upload: u };
  },
  downloadTransfer: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.transfers', 'read');
    const t = (c.db.transfers[d.deploymentId] ?? []).find((x) => x.transferId === c.params.transferId);
    if (!t) throw new Err('not-found', 'transfer not found');
    if (t.status === 'expired') throw new Err('gone', 'the transfer expired');
    if (t.status === 'uploading') throw new Err('not-found', 'the file is still uploading');
    const bytes = loadBlob(c.db, `transfer:${t.transferId}`) ?? new TextEncoder().encode(`mock content of ${t.fileName}\n`);
    if (t.direction === 'to-cloud' && t.status === 'ready') {
      t.status = 'downloaded';
      cloudActivity(c, d, 'transfer.received', `File ${t.fileName} from ${t.siteSlug} downloaded`, { transferId: t.transferId, direction: 'to-cloud', fileName: t.fileName });
    }
    return bytesResponse(bytes, t.fileName);
  },
  deleteTransfer: (c) => {
    const d = dep(c);
    needDep(c, d, 'deployment.transfers', 'write');
    const t = (c.db.transfers[d.deploymentId] ?? []).find((x) => x.transferId === c.params.transferId);
    if (!t) throw new Err('not-found', 'transfer not found');
    t.status = 'expired';
    if (t.direction === 'to-site') d.agentEpoch += 1;
    cloudActivity(c, d, 'transfer.deleted', `File ${t.fileName} (${t.siteSlug}) deleted`, { transferId: t.transferId, direction: t.direction, fileName: t.fileName });
    return { deleted: true };
  },
};
// §21 #123 shared files (one bucket per deployment)
function sharedFile(c: Ctx, d: Deployment): SharedFile {
  const f = (c.db.files[d.deploymentId] ?? []).find((x) => x.fileId === c.params.fileId);
  if (!f) throw new Err('not-found', 'file not found');
  return f;
}
Object.assign(handlers, {
  listFiles: (c: Ctx) => {
    const d = dep(c);
    needDep(c, d, 'deployment.transfers', 'read');
    const all = ['1', 'true'].includes(c.query.get('unpublished') ?? '');
    return { items: (c.db.files[d.deploymentId] ?? []).filter((f) => all || f.state !== 'unpublished').sort((a, b) => b.publishedAt.localeCompare(a.publishedAt)) };
  },
  publishFile: (c: Ctx) => {
    const d = dep(c);
    needDep(c, d, 'deployment.transfers', 'write');
    const b = bodyOf<{ fileName: string; sizeBytes: number; sha256: string; note?: string | null }>(c);
    unknownFields(b, ['fileName', 'sizeBytes', 'sha256', 'note']);
    const out: ValidationIssue[] = [];
    if (typeof b.fileName !== 'string' || !b.fileName || b.fileName.length > 255 || /[/\\\0]/.test(b.fileName)) out.push(issue('fileName', 'V_FORMAT', 'file name must be 1–255 characters without / \\ NUL'));
    if (!Number.isInteger(b.sizeBytes) || b.sizeBytes < 0) out.push(issue('sizeBytes', 'V_RANGE', 'sizeBytes must be a whole number ≥ 0'));
    if (!RE.sha256.test(b.sha256 ?? '')) out.push(issue('sha256', 'V_FORMAT', 'sha256 must be sha256:<hex>'));
    if ((b.note ?? '').length > FILE_NOTE_MAX) out.push(issue('note', 'V_LENGTH', `note must be ≤ ${FILE_NOTE_MAX} characters`));
    throwIf(out);
    if (b.sizeBytes > FILE_MAX) throw new Err('too-large', 'files are limited to 2 GiB');
    const file: SharedFile = { fileId: ulid(), fileName: b.fileName, sizeBytes: b.sizeBytes, sha256: b.sha256, note: b.note?.trim() || null, publishedAt: now(), publishedBy: c.me.email, origin: { kind: 'cloud' }, state: 'uploading', unpublishedAt: null, unpublishedBy: null, cloud: { stored: false, fetch: null }, sites: [] };
    const upload = newUpload(c, 'file', b.sizeBytes, b.sha256, d.deploymentId, file.fileId);
    (c.db.files[d.deploymentId] ??= []).push(file);
    return { file, upload };
  },
  fetchFileToCloud: (c: Ctx) => {
    const d = dep(c);
    needDep(c, d, 'deployment.transfers', 'read');
    const f = sharedFile(c, d);
    if (f.state === 'unpublished') throw new Err('gone', 'the file was unpublished');
    if (f.cloud.stored) return f;
    if (f.state === 'uploading') throw new Err('conflict', 'the file is still uploading');
    if (!f.sites.length) throw new Err('conflict', 'no site holds a copy of this file');
    if (f.cloud.fetch && !f.cloud.fetch.error) return f;
    f.cloud.fetch = { requestedAt: now(), requestedBy: c.me.email, from: f.sites[0]!, startedAt: null, bytesDone: 0, error: null };
    d.agentEpoch += 1;
    return f;
  },
  downloadFile: (c: Ctx) => {
    const d = dep(c);
    needDep(c, d, 'deployment.transfers', 'read');
    const f = sharedFile(c, d);
    if (f.state === 'unpublished') throw new Err('gone', 'the file was unpublished');
    if (!f.cloud.stored) throw new Err('conflict', 'the cloud has no copy yet (get it into the cloud first)');
    const bytes = loadBlob(c.db, `file:${f.fileId}`) ?? new TextEncoder().encode(`mock content of ${f.fileName}\n`);
    cloudActivity(c, d, 'file.downloaded', `File ${f.fileName} downloaded`, { fileId: f.fileId, where: 'cloud' });
    return bytesResponse(bytes, f.fileName);
  },
  unpublishFile: (c: Ctx) => {
    const d = dep(c);
    needDep(c, d, 'deployment.transfers', 'write');
    const f = sharedFile(c, d);
    if (f.state !== 'unpublished') {
      f.state = 'unpublished';
      f.unpublishedAt = now();
      f.unpublishedBy = c.me.email;
      f.cloud = { stored: false, fetch: null };
      f.sites = [];
      memBlobs.delete(`file:${f.fileId}`);
      delete c.db.blobs[`file:${f.fileId}`];
      d.agentEpoch += 1;
      cloudActivity(c, d, 'file.unpublished', `File ${f.fileName} unpublished`, { fileId: f.fileId, fileName: f.fileName });
    }
    return { deleted: true };
  },
} satisfies Record<string, Handler>);

/** §21 #123 mock: a site asked by fileFetches starts uploading after ~1 s and is done ~4 s later (cloud.stored) */
const FETCH_START_MS = 1000;
const FETCH_MS = 4000;
function tickFiles(db: MockDb) {
  const t = Date.now();
  for (const [depId, list] of Object.entries(db.files ?? {}))
    for (const f of list) {
      const fx = f.cloud.fetch;
      if (!fx || fx.error || f.cloud.stored || f.state !== 'ready') continue;
      const el = t - Date.parse(fx.requestedAt);
      if (el < FETCH_START_MS) continue;
      fx.startedAt ??= new Date(Date.parse(fx.requestedAt) + FETCH_START_MS).toISOString();
      fx.bytesDone = Math.min(f.sizeBytes, Math.floor((f.sizeBytes * (el - FETCH_START_MS)) / FETCH_MS));
      if (el >= FETCH_START_MS + FETCH_MS) {
        f.cloud = { stored: true, fetch: null };
        const d = db.deployments[depId];
        if (d) cloudActivityAs(db, d, null, 'file.fetched', `File ${f.fileName} copied into the cloud from ${fx.from}`, { fileId: f.fileId, where: 'cloud', from: fx.from });
      }
    }
}

// §21 #101 volume content cloud API (mock/volume-content.ts)
const bh = branchHandlers({
  err: (code, message, details) => new Err(code, message, details),
  need: (c, r, a) => need(c as Ctx, r, a),
  dep: (c, id) => dep(c as Ctx, id),
  needDep: (c, d, r, a) => needDep(c as Ctx, d, r, a),
  canDep: (c, d, r, a) => allowed((c as Ctx).me, r, a, targetOf(d)),
  can: (c, r, a) => allowed((c as Ctx).me, r, a),
  cloudActivity: (c, d, t, s, data) => cloudActivity(c as Ctx, d, t, s, data),
  vctx,
  normalize: (db, cfg) => normalizeConfig(cfg, imageVolumes(db)),
  configView,
  buildCatalog,
  genSecret,
});
const fromPoint = bh.__fromPoint as unknown as (c: BranchCtx, p: unknown) => { info: { ref: import('@opm/shared').BranchRef; label: string; dep?: Deployment; canSecretsRead: boolean }; config: Config; commit: import('@opm/shared').Commit | null } | null;
delete (bh as Record<string, unknown>).__fromPoint;
Object.assign(handlers, bh);
Object.assign(handlers, githubHandlers({ err: (code, message, details) => new Err(code, message, details), invalid, issue, needAdmin: (c) => need(c as Ctx, 'accounts', 'admin') }) as unknown as Record<string, Handler>);
Object.assign(handlers, volumeContentHandlers({ err: (code, message, details) => new Err(code, message, details), dep: (c) => dep(c as Ctx), needDep: (c, d, r, a) => needDep(c as Ctx, d, r, a), cloudActivity: (c, d, t, s, data) => cloudActivity(c as Ctx, d, t, s, data) }));

// ───────────────────────────────────────────── §21 #107 commit staging (one Commit = one transaction incl. the bytes)
interface MockStage {
  stageId: string;
  createdBy: string;
  expiresAt: number;
  blobs: Map<string, Uint8Array>; // hex → bytes
}
/** stages live in memory only (a stage = one commit attempt) */
const stages = new Map<string, MockStage>();
/** uploadId → stageId (multi-part uploads into a stage) */
const uploadStage = new Map<string, string>();
type StageLogEntry = { op: 'create' | 'put' | 'delete' | 'dropped' | 'promoted'; stageId: string; sha?: string; size?: number; at: number; multipart?: boolean; missing?: number; requested?: number };
const stageLog: StageLogEntry[] = [];
/**
 * test hooks: window.__opmMockStages() → { open: [{ stageId, blobs }], log } ; window.__opmMockBlobExists(sha) ;
 * localStorage 'opm.mock.commitFault' = 'after-verify' | 'after-promote' → the next commits fail (500) at that point
 * (the stage is dropped, nothing is committed).
 */
if (typeof window !== 'undefined') {
  const w = window as unknown as Record<string, unknown>;
  w.__opmMockStages = () => ({ open: [...stages.values()].map((x) => ({ stageId: x.stageId, blobs: [...x.blobs.keys()].map((h) => `sha256:${h}`) })), log: stageLog.slice() });
  w.__opmMockBlobExists = (sha: string) => loadBlob(getDb(), sha.replace(/^sha256:/, '')) !== null;
}
const hexOfSha = (sha: string) => sha.replace(/^sha256:/, '');
function commitFault(): string | null {
  try {
    return localStorage.getItem('opm.mock.commitFault');
  } catch {
    return null;
  }
}
function stageOf(c: Ctx, id: string, allowExpired = false): MockStage {
  const st = stages.get(id);
  if (!st || st.createdBy !== c.me.uid) throw new Err('not-found', 'stage not found');
  if (!allowExpired && st.expiresAt < Date.now()) throw new Err('gone', 'the stage expired');
  return st;
}
function dropStage(id: string | null) {
  if (!id || !stages.has(id)) return;
  stages.delete(id);
  for (const [u, s2] of uploadStage) if (s2 === id) uploadStage.delete(u);
  stageLog.push({ op: 'dropped', stageId: id, at: Date.now() });
}
handlers.createStage = (c) => {
  if (!allowed(c.me, 'templates', 'write') && !anyDep(c, 'deployment.config', 'write')) throw new Err('forbidden', 'you need templates.write or deployment.config.write');
  const b = bodyOf<{ blobs?: BlobNeed[] }>(c);
  if (!Array.isArray(b.blobs)) throw invalid([issue('blobs', 'V_TYPE', 'blobs must be a list')]);
  if (b.blobs.length > STAGE_MAX_BLOBS) throw new Err('too-large', `at most ${STAGE_MAX_BLOBS} blobs per commit`);
  b.blobs.forEach((x, i) => {
    if (!x || typeof x.sha256 !== 'string' || !RE.sha256.test(x.sha256)) throw invalid([issue(`blobs[${i}].sha256`, 'V_FORMAT', 'sha256 must be sha256:<hex>')]);
    if (!Number.isInteger(x.sizeBytes) || x.sizeBytes < 0 || x.sizeBytes > SEED_ARCHIVE_MAX) throw invalid([issue(`blobs[${i}].sizeBytes`, 'V_RANGE', 'sizeBytes out of range')]);
  });
  const stageId = ulid();
  stages.set(stageId, { stageId, createdBy: c.me.uid, expiresAt: Date.now() + STAGE_TTL_MS, blobs: new Map() });
  const missing = b.blobs.filter((x) => loadBlob(c.db, hexOfSha(x.sha256)) === null);
  stageLog.push({ op: 'create', stageId, at: Date.now(), requested: b.blobs.length, missing: missing.length });
  return { stageId, expiresAt: new Date(Date.now() + STAGE_TTL_MS).toISOString(), missing, present: b.blobs.length - missing.length };
};
handlers.putStageBlob = async (c) => {
  const st = stageOf(c, c.params.stageId!);
  const hex = hexOfSha(c.params.sha256!);
  if (!/^[0-9a-f]{64}$/.test(hex)) throw invalid([issue('sha256', 'V_FORMAT', 'sha256 must be 64 hex')]);
  const bytes = c.raw ?? new Uint8Array();
  if (bytes.length > 24 * 1024 * 1024) throw new Err('too-large', 'body larger than 24 MiB (use POST /v1/uploads with stageId)');
  if ((await sha256Hex(bytes)) !== hex) throw invalid([issue('body', 'V_FORMAT', 'sha256 mismatch')], 'sha256 mismatch');
  if (loadBlob(c.db, hex) !== null) return { sha256: `sha256:${hex}`, sizeBytes: bytes.length, staged: false };
  st.blobs.set(hex, bytes);
  stageLog.push({ op: 'put', stageId: st.stageId, sha: `sha256:${hex}`, size: bytes.length, at: Date.now() });
  return { sha256: `sha256:${hex}`, sizeBytes: bytes.length, staged: true };
};
handlers.deleteStage = (c) => {
  const st = stageOf(c, c.params.stageId!, true);
  dropStage(st.stageId);
  stageLog.push({ op: 'delete', stageId: st.stageId, at: Date.now() });
  return { deleted: true };
};
/** verify every NEW blob (stored or staged with the right size) → 422 V_BLOB_MISSING; promote; run; drop the stage always */
async function withStage(c: Ctx, headCfg: Pick<Config, 'files' | 'sites'> | null, nextCfg: Pick<Config, 'files' | 'sites'> | null, stageId: string | null, run: () => unknown) {
  const st = stageId ? stageOf(c, stageId) : null;
  try {
    const needs = nextCfg ? newConfigBlobs(headCfg, nextCfg) : [];
    const missing = needs.filter((n) => loadBlob(c.db, hexOfSha(n.sha256)) === null && st?.blobs.get(hexOfSha(n.sha256))?.length !== n.sizeBytes);
    if (missing.length) throw new Err('invalid', `${missing.length} blob(s) not uploaded`, { errors: missing.map((m) => issue(`blobs[${m.sha256}]`, 'V_BLOB_MISSING', `content ${m.sha256.slice(0, 19)}… (${m.sizeBytes} bytes) is neither stored nor staged`)), missing });
    const fault = commitFault();
    if (fault === 'after-verify') throw new Err('internal', 'mock fault after verify (opm.mock.commitFault)');
    for (const n of needs) {
      const b = st?.blobs.get(hexOfSha(n.sha256));
      if (b && loadBlob(c.db, hexOfSha(n.sha256)) === null) {
        storeBlob(c.db, hexOfSha(n.sha256), b);
        stageLog.push({ op: 'promoted', stageId: st!.stageId, sha: n.sha256, size: b.length, at: Date.now() });
      }
    }
    if (fault === 'after-promote') throw new Err('internal', 'mock fault after promote (opm.mock.commitFault)');
    return await run();
  } finally {
    dropStage(stageId);
  }
}
{
  const ct = handlers.commitTemplate!;
  const cd = handlers.commitDeployment!;
  const cv = handlers.createVolumeContentVersion!;
  const sid = (c: Ctx) => {
    const v = (c.body as { stageId?: unknown } | undefined)?.stageId;
    return typeof v === 'string' && v ? v : null;
  };
  const stripStage = (c: Ctx) => {
    if (c.body && typeof c.body === 'object') delete (c.body as { stageId?: unknown }).stageId;
  };
  handlers.commitTemplate = async (c) => {
    const t = c.db.templates[c.params.templateId!];
    const b = (c.body ?? {}) as { baseCommitId?: string | null; config?: Config };
    const id = sid(c);
    stripStage(c);
    // stale first (409 before 422), like the backend
    if (!t || (b.baseCommitId ?? null) !== (t.branch?.head?.commitId ?? null)) return withStage(c, null, null, id, () => ct(c));
    return withStage(c, t, b.config ?? null, id, () => ct(c));
  };
  handlers.commitDeployment = async (c) => {
    const d = c.db.deployments[c.params.deploymentId!];
    const b = (c.body ?? {}) as { baseCommitId?: string | null; config?: Config };
    const id = sid(c);
    stripStage(c);
    if (!d || (b.baseCommitId ?? null) !== (d.branch?.head?.commitId ?? null)) return withStage(c, null, null, id, () => cd(c));
    return withStage(c, d.draft, b.config ?? null, id, () => cd(c));
  };
  handlers.createVolumeContentVersion = async (c) => {
    const id = sid(c);
    stripStage(c);
    const st = id ? stageOf(c, id) : null;
    try {
      for (const e of ((c.body as { tree?: { entries?: { sha256: string }[] } })?.tree?.entries ?? [])) {
        const bts = st?.blobs.get(hexOfSha(e.sha256));
        if (bts && loadBlob(c.db, hexOfSha(e.sha256)) === null) storeBlob(c.db, hexOfSha(e.sha256), bts);
      }
      return await cv(c);
    } finally {
      dropStage(id);
    }
  };
}

// ───────────────────────────────────────────── §21 #106 entity slugs
/** requested slug (RE + unique → 409 conflict reason slug-taken) or uniqueSlug(slugifyLabel(label)) */
function pickSlug(taken: (string | undefined)[], want: string | undefined, label: string, out: ValidationIssue[], prefix = 'x-'): string {
  const set = new Set(taken.filter((x): x is string => !!x));
  if (want !== undefined && want !== null && want !== '') {
    if (typeof want !== 'string' || !ENTITY_SLUG_RE.test(want)) {
      out.push(issue('slug', 'V_FORMAT', 'slug: 2–40 characters a-z 0-9 -, starting with a letter'));
      return want as string;
    }
    if (set.has(want)) throw new Err('conflict', `slug ${want} is already used`, { reason: 'slug-taken', slug: want });
    return want;
  }
  return uniqueSlug(slugifyLabel(label ?? '', prefix), set);
}
/** mock migrate-k: give every template / deployment without a slug one (oldest first) */
function ensureSlugs(db: MockDb) {
  for (const [coll, prefix] of [[db.templates, 'x-'], [db.deployments, 'd-']] as const) {
    const docs = Object.values(coll as Record<string, { slug?: string; label: string; createdAt: string }>);
    if (docs.every((x) => x.slug)) continue;
    const taken = new Set(docs.map((x) => x.slug).filter((x): x is string => !!x));
    for (const x of docs.sort((a, b) => a.createdAt.localeCompare(b.createdAt))) {
      if (x.slug) continue;
      x.slug = uniqueSlug(slugifyLabel(x.label, prefix), taken);
      taken.add(x.slug);
    }
  }
}

// ───────────────────────────────────────────── helpers
function strip<T extends object>(o: T): T {
  const out = { ...o } as Record<string, unknown>;
  for (const k of Object.keys(out)) if (k.startsWith('_')) delete out[k];
  return out as T;
}
function img(c: Ctx) {
  need(c, 'images', 'read');
  if (!c.db.services[c.params.serviceSlug!]) throw new Err('not-found', 'service not found');
  const i = c.db.images[c.params.serviceSlug!]?.[c.params.version!];
  if (!i) throw new Err('not-found', `image ${c.params.serviceSlug}:${c.params.version} not found`);
  return i;
}
function anyDep(c: Ctx, r: Resource, a: Action) {
  return Object.values(c.db.deployments).some((d) => !d.deleted && allowed(c.me, r, a, targetOf(d)));
}
function storeBlob(db: MockDb, key: string, bytes: Uint8Array) {
  if (bytes.length <= 512 * 1024) db.blobs[key] = b64(bytes);
  else memBlobs.set(key, bytes);
}
function loadBlob(db: MockDb, key: string): Uint8Array | null {
  if (memBlobs.has(key)) return memBlobs.get(key)!;
  if (db.blobs[key] !== undefined) return unb64(db.blobs[key]!);
  return null;
}
function newUpload(c: Ctx, target: 'blob' | 'transfer' | 'file', sizeBytes: number, sha256: string, deploymentId: string | null, transferId: string | null) {
  const partSize = 8 * 1024 * 1024;
  const u: MockUpload = { uploadId: autoId(), purpose: target, deploymentId, transferId, sizeBytes, sha256, partSize, partCount: Math.max(1, Math.ceil(sizeBytes / partSize)), partsReceived: [], status: 'open', error: null, createdAt: now(), createdBy: c.me.uid, expiresAt: new Date(Date.now() + 86400_000).toISOString(), parts: {}, target };
  c.db.uploads[u.uploadId] = u;
  return uploadView(u);
}
function uploadView(u: MockUpload) {
  const { parts: _p, target: _t, ...v } = u;
  void _p;
  void _t;
  return v;
}
function upl(c: Ctx): MockUpload {
  const u = c.db.uploads[c.params.uploadId!];
  if (!u || u.createdBy !== c.me.uid) throw new Err('not-found', 'upload not found');
  if (Date.parse(u.expiresAt) < Date.now()) throw new Err('gone', 'upload expired');
  return u;
}
function page<T>(list: T[], c: Ctx, def: number) {
  const off = Number(c.query.get('cursor') ?? 0) || 0;
  const limit = Math.max(1, Number(c.query.get('limit') ?? def) || def);
  const items = list.slice(off, off + limit);
  return { items, cursor: off + limit < list.length ? String(off + limit) : null };
}
const revSummary = (r: Revision) => ({ revId: r.revId, seq: r.seq, parentRevId: r.parentRevId, origin: r.origin, actor: r.actor, createdAt: r.createdAt, hash: r.hash, summary: r.summary, kind: r.kind });

// ───────────────────────────────────────────── template sync helpers (§21 #94; same rules as backend routes/template-sync.ts)
function tplCfg(c: Ctx, t: Template): Config {
  return normalizeConfig(structuredClone(templateConfig(t)), imageVolumes(c.db));
}
function syncSides(c: Ctx, d: Deployment, templateId: string | null) {
  const link = linkOf(d);
  const tid = templateId || link?.templateId;
  if (!tid) throw invalid([issue('templateId', 'V_REQUIRED', 'the deployment is not linked to a template — pick one (templateId)')]);
  const tpl = c.db.templates[tid];
  if (!tpl) throw new Err('not-found', 'template not found');
  const relink = !link || link.templateId !== tid;
  return { link, tpl, theirs: tplCfg(c, tpl), base: link ? link.baseSnapshot : null, relink };
}
function runSync(c: Ctx, d: Deployment) {
  const b = bodyOf<TemplateSyncApplyRequest>(c);
  unknownFields(b, ['ifRevision', 'templateId', 'templateVersion', 'choices', 'values']);
  if (!Number.isInteger(b.ifRevision)) throw invalid([issue('ifRevision', 'V_REQUIRED', 'ifRevision (the draftVersion you reviewed) is required')]);
  if (typeof b.templateId !== 'string' || !b.templateId) throw invalid([issue('templateId', 'V_REQUIRED', 'templateId is required')]);
  if (!Number.isInteger(b.templateVersion)) throw invalid([issue('templateVersion', 'V_REQUIRED', 'templateVersion is required')]);
  if (!Array.isArray(b.choices)) throw invalid([issue('choices', 'V_TYPE', 'choices must be a list')]);
  if (b.ifRevision !== d.draftVersion) throw new Err('conflict', `the draft changed (draftVersion ${d.draftVersion})`, { current: d.draftVersion });
  const side = syncSides(c, d, b.templateId);
  if (side.tpl.version !== b.templateVersion) throw new Err('conflict', `the template changed (version ${side.tpl.version}) — refresh the review`, { reason: 'template-changed', current: side.tpl.version });
  const r = applyTemplateSync(side.base, side.theirs, d.draft, b.choices, { skipped: side.relink ? [] : (side.link?.skipped ?? []), values: b.values ?? {} });
  if (r.errors.length) throw invalid(r.errors.map((e) => issue(e.key, 'V_SYNC_CHOICE', e.message)), r.errors[0]!.message);
  // a new secret with a generate rule gets its value now (like at create)
  const t = now();
  for (const s of r.config.secrets) if (!d.draft.secrets.some((x) => x.secretSlug === s.secretSlug) && s.generate && !s.value) Object.assign(s, { value: genSecret(s.generate.charset, s.generate.length), rotatedAt: t });
  return { ...r, side };
}

function writeDraft(c: Ctx, d: Deployment, next: Config) {
  next = normalizeConfig(next, imageVolumes(c.db));
  const v = validateConfig(next, vctx(c.db, 'draft', d.deploymentId));
  if (!v.ok) throw invalid(v.errors);
  const changed = auditDiff(d.draft, next, { maxValueBytes: 0 });
  const paths = changed.changes.map((x) => x.path).slice(0, 50);
  d.draft = next;
  d.draftVersion += 1;
  d.updatedAt = now();
  d.updatedBy = c.me.uid;
  cloudActivity(c, d, 'draft.updated', `Working copy updated (${changed.total} change${changed.total === 1 ? '' : 's'})`, { draftVersion: d.draftVersion, paths, changes: changed.total });
  refreshDeployment(c.db, d);
  return { draftVersion: d.draftVersion, validation: { ok: v.ok, issues: v.list } };
}

function diff(db: MockDb, d: Deployment): DiffResponse {
  // §21 #104: the working copy vs the branch head commit (shape kept; pending [] — no proposals)
  const hc = branchHelpers.headOf(db, branchHelpers.depRef(d.deploymentId));
  const headRev = hc ? { config: hc.config } : null;
  const changes = diffConfigs(headRev?.config ?? null, d.draft, (x) => x, (s) => (headRev?.config.secrets.find((x) => x.secretSlug === s)?.value ?? null) !== (d.draft.secrets.find((x) => x.secretSlug === s)?.value ?? null));
  const v = validateConfig(d.draft, vctx(db, 'publish', d.deploymentId));
  const blockers = v.errors.slice();
  if (headRev && changes.length === 0) blockers.push({ path: '', code: 'V_NO_CHANGE', message: 'the working copy equals the head commit', severity: 'error' });
  return {
    head: d.head ? { revId: d.head.revId, seq: d.head.seq, reportedAt: d.head.reportedAt } : null,
    draftVersion: d.draftVersion,
    changes,
    publishable: blockers.length === 0,
    blockers,
    warnings: v.list.filter((i) => i.severity === 'warning'),
    pending: [],
  };
}

function uptimeDays(enrolledAt: string, slug: string) {
  const days: { date: string; onlineFraction: number; hours: number[] }[] = [];
  const today = new Date();
  today.setUTCHours(0, 0, 0, 0);
  const enrolled = Date.parse(enrolledAt);
  const curHour = new Date().getUTCHours();
  for (let i = 29; i >= 0; i--) {
    const day = new Date(today.getTime() - i * 86400_000);
    const date = day.toISOString().slice(0, 10).replace(/-/g, '');
    const hours: number[] = [];
    for (let h = 0; h < 24; h++) {
      const start = day.getTime() + h * 3600_000;
      if (start + 3600_000 < enrolled || (i === 0 && h > curHour)) {
        hours.push(0);
        continue;
      }
      const seed = (date.charCodeAt(7) * 31 + h * 7 + slug.length * 13) % 97;
      let v = 1;
      if (slug === 'db' && i === 6 && h >= 2 && h < 5) v = h === 3 ? 0 : 0.6;
      else if (seed === 3) v = 0.92;
      else if (seed === 11) v = 0.97;
      hours.push(v);
    }
    const n = i === 0 ? curHour + 1 : 24;
    days.push({ date, onlineFraction: hours.slice(0, n).reduce((a, x) => a + x, 0) / n, hours });
  }
  return days;
}

export { ago };

// ───────────────────────────────────────────── §21 #84 credential helpers
/** API view of an account: the stored password / reset link become metadata only. */
function accView(a: AccountDoc): Account {
  const { lastSetPassword: p, lastResetLink: l, ...rest } = a;
  return { ...rest, lastSetPassword: p ? { setAt: p.setAt, setBy: p.setBy } : null, lastResetLink: l ? { setAt: l.setAt, setBy: l.setBy, expiresAt: l.expiresAt } : null };
}
function resetLinkView(a: AccountDoc) {
  const l = a.lastResetLink!;
  return { value: l.value, passwordSetLink: l.value, hideAfterSec: REVEAL_SECONDS, createdAt: l.setAt, createdBy: l.setBy, expiresAt: l.expiresAt };
}
function genPassword(): string {
  return genSecret('alnum', 20);
}
function tokenState(x: Pick<InstallToken, 'kind' | 'usedAt' | 'revokedAt' | 'expiresAt'>, t: number): 'active' | 'used' | 'expired' | 'revoked' {
  return installTokenState(x, t);
}
/** §21 #92 (= backend setup.ts upgradeTokenOf): newest unrevoked maintenance token of the enrollment, else its install token */
function upgradeTokenOf(list: InstallToken[], a: AgentDoc | undefined): InstallToken | null {
  if (!a) return null;
  const mine = list.filter((t) => t.siteSlug === a.siteSlug && !t.revokedAt);
  const m = mine.filter((t) => t.kind === 'maintenance' && t.boundEnrolledAt === a.enrolledAt).sort((x, y) => y.createdAt.localeCompare(x.createdAt))[0];
  return m ?? mine.find((t) => (t.kind ?? 'install') === 'install' && t.usedAt === a.enrolledAt) ?? null;
}
function enrollmentTokens(list: InstallToken[], a: AgentDoc): InstallToken[] {
  return list.filter((t) => t.siteSlug === a.siteSlug && !t.revokedAt && ((t.kind === 'maintenance' && t.boundEnrolledAt === a.enrolledAt) || ((t.kind ?? 'install') === 'install' && t.usedAt === a.enrolledAt)));
}
function installCommand(token: string): string {
  return `curl -fsSL -H "Authorization: Bearer ${token}" https://app.opm.example/v1/install/script | sudo sh`;
}

/** §21 #86 mock of the backend's Setup guiUrls: IPv4 addresses (else ip) × guiPorts, edge first. */
function mockGuiUrls(a: AgentDoc): { url: string; kind: GuiPort['kind'] }[] {
  const ports = a.guiPorts ?? [];
  if (!ports.length) return [];
  const v4 = (a.detectedAddresses ?? []).filter((x) => /^\d+\.\d+\.\d+\.\d+$/.test(x));
  const hosts = v4.length ? v4 : [a.ip];
  const rank = { edge: 0, 'gui-alias': 1, 'agent-port': 2 } as const;
  const sorted = [...ports].sort((x, y) => rank[x.kind] - rank[y.kind] || (x.scheme === 'http' ? -1 : 1));
  const out: { url: string; kind: GuiPort['kind'] }[] = [];
  for (const p of sorted)
    for (const h of hosts) {
      const dflt = (p.scheme === 'http' && p.port === 80) || (p.scheme === 'https' && p.port === 443);
      out.push({ url: `${p.scheme}://${h}${dflt ? '' : `:${p.port}`}/_opm/`, kind: p.kind });
    }
  return out;
}
