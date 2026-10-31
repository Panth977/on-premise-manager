/**
 * @opm/shared · types.ts
 *
 * NORMATIVE wire format of On-Prem Manager (OPM). Every document stored in Firestore / agent SQLite and
 * every API request / response body uses exactly these names. JSON on the wire. See SPEC-final.md.
 *
 * Conventions
 * - `Ts` is ISO 8601 UTC with "Z" (e.g. "2026-10-07T09:30:00Z"); milliseconds optional on input, the cloud
 *   writes millisecond precision ("2026-10-07T09:30:00.123Z"). Firestore stores Timestamps; the API converts.
 * - Optional fields (`?`) may be absent; `| null` fields are always present.
 * - "View" types are what the API returns (secrets masked). "Input" types are request bodies.
 */

// ───────────────────────────────────────────────────────────── common scalars
export type Id = string; //        Firestore auto id: ^[A-Za-z0-9]{20}$
export type Slug = string; //      ^[a-z][a-z0-9-]{1,39}$ (2–40 chars), immutable after create
export type Uid = string; //       Firebase Auth uid (1–128 chars)
export type Ts = string; //        ISO 8601 UTC
export type Sha256 = string; //    "sha256:" + 64 lowercase hex
export type Ulid = string; //      ^[0-9A-HJKMNP-TV-Z]{26}$
export type RevId = Ulid;
export type Expr = string; //      contains at least one "{{ … }}" block
export type Value = string; //     plain string or Expr, ≤ 4096 chars
/**
 * Icon reference (services, templates, tags; also Account.img and Deployment.image):
 *   null                → the entity type's default icon
 *   "lucide:<name>"     → a Lucide icon (^[a-z0-9-]+$), rendered from the curated set in shared/src/icons.ts
 *   "upload:<iconId>"   → an uploaded icon (icons/{iconId}; bytes at GET /v1/icons/{iconId}/content, public)
 *   "<name>"            → legacy bare Lucide name (read as "lucide:<name>")
 *   "https://…"         → legacy URL (Account.img / Deployment.image only)
 *   "data:image/…"      → only inside Revision.catalog (uploaded icons are embedded so offline agent GUIs can show them)
 */
export type Icon = string | null;

export interface Audit {
  createdAt: Ts;
  createdBy: Uid;
  updatedAt: Ts;
  updatedBy: Uid; // "system" when OPM itself changed it
}

/** The masked placeholder returned instead of a secret / password value. */
export const MASK = '•••' as const;
export type Masked = typeof MASK;

// ───────────────────────────────────────────────────────────── reveal (SPEC-final §21 #84: every credential is viewable + regenerable any time)
/** Seconds a revealed credential stays visible in RevealValue (both GUIs), then it is masked again. */
export const REVEAL_SECONDS = 60 as const;
/** Value-returning (reveal / regenerate) requests per person per minute (cloud: per uid; agent GUI: per session). */
export const REVEAL_RATE_PER_MINUTE = 30 as const;
/**
 * Common shape of every response that carries a credential value (reveal AND regenerate / create responses).
 * `value` = the text RevealValue shows and copies. Served with `Cache-Control: no-store`.
 */
export interface RevealResponse {
  value: string;
  hideAfterSec: typeof REVEAL_SECONDS;
}

// ───────────────────────────────────────────────────────────── errors
export type ErrorCode =
  | 'unauthenticated' // 401
  | 'session-expired' // 401 agent GUI
  | 'invalid-key' // 401 agent GUI login
  | 'actor-required' // 401 agent GUI write without actor token
  | 'forbidden' // 403
  | 'not-found' // 404
  | 'method-not-allowed' // 405
  | 'conflict' // 409
  | 'stale' // 409
  | 'not-available' // 404 §9.10 this cloud has no tunnel key (no release private key)
  | 'tunnel-key-changed' // 409 §9.10 a sealed request names another key: fetch the key again
  | 'undecryptable' // 400 §9.10 a sealed request cannot be decrypted
  | 'gone' // 410
  | 'too-large' // 413
  | 'invalid' // 422
  | 'gate-closed' // 409 agent: approve / rollback gate is not open
  | 'locked' // 409 agent: another update holds the lock
  | 'no-internet' // 503 agent peer fetch-request
  | 'unavailable' // 503 a peer / the cloud cannot be reached
  | 'rate-limited' // 429 (Retry-After header)
  | 'internal'; // 500 (details.requestId)

export const ERROR_HTTP_STATUS: Record<ErrorCode, number> = {
  unauthenticated: 401,
  'session-expired': 401,
  'invalid-key': 401,
  'actor-required': 401,
  forbidden: 403,
  'not-found': 404,
  'method-not-allowed': 405,
  'not-available': 404,
  'tunnel-key-changed': 409,
  undecryptable: 400,
  conflict: 409,
  stale: 409,
  gone: 410,
  'too-large': 413,
  invalid: 422,
  'gate-closed': 409,
  locked: 409,
  'no-internet': 503,
  unavailable: 503,
  'rate-limited': 429,
  internal: 500,
};

export interface ValidationIssue {
  /** JSON-ish path, e.g. "sites[app].config.containers[web].port[http].hostPort" */
  path: string;
  /** Stable code, see VALIDATION_CODES in validate.ts (V_*) or an expression code (E_*) */
  code: string;
  message: string;
  /** info (round E): advice only, never counted as a warning or blocker (e.g. V_VAR_OPTIONAL_NULL in templates) */
  severity: 'error' | 'warning' | 'info';
}

export interface ApiErrorBody {
  error: {
    code: ErrorCode;
    message: string;
    /** invalid → { errors: ValidationIssue[] }; internal → { requestId }; conflict → resource specific */
    details?: Record<string, unknown>;
  };
}

// ───────────────────────────────────────────────────────────── developer tools
export interface Registry extends Audit {
  registryId: Id;
  label: string; //       1–80
  url: string; //         https URL without path; default "https://registry-1.docker.io"
  username: string; //    1–255
  password: string; //    1–8192 (a GCP service-account key JSON for _json_key is ~2.4 KB), stored plain (prototype); NEVER returned except by reveal
  lastCheck: { at: Ts; ok: boolean; message: string } | null;
}
export type RegistryView = Omit<Registry, 'password'> & { password: Masked; isSet: boolean };

export interface Service extends Audit {
  /** THE id (one identifier per entity): document id services/{serviceSlug}; ^[a-z][a-z0-9-]{1,39}$, unique, immutable;
   *  the default containerSlug of its containers on a site (containers are instances: Container.containerSlug is their name) */
  serviceSlug: Slug;
  label: string;
  icon: Icon;
  description: string;
}

export type Platform = 'linux/amd64' | 'linux/arm64';
export type ImageKind = 'service' | 'job';
export type ImportMode = 'copy' | 'gateway';
export type ImportStatus = 'pending' | 'copying' | 'ready' | 'failed';

export interface ImagePort {
  name: Slug; //          unique in the image
  port: number | Expr; // 1–65535 or ONE whole block using var.* / deployment.id
  protocol: 'tcp' | 'udp';
  description: string;
}
export interface ImageEnv {
  name: string; //        ^[A-Z_][A-Z0-9_]*$, unique
  /**
   * The default. Absent = no default. Plain text or an expression (field imageEnv). When `options` is set it must be
   * one of the options (no expression). Legacy documents may hold string[] here = options with the first as default;
   * read them through normalizeImageEnv() (validate.ts).
   */
  value?: Value;
  /** allowed values ("choice"): 1–50 unique strings; containers must resolve to one of them (V_OPTION); null = free text */
  options?: string[] | null;
  required: boolean;
  secret: boolean; //     true → container must use secretRef (or an expression with secret.*); no default allowed
  description: string;
}
export interface ImageVolume {
  name: Slug;
  mountPath: string; //   absolute path, not "/"; may hold {{ }} (field mountPath: var, deployment, self, container incl. container.env.<NAME>) and must start with a literal "/"
  /**
   * What the image keeps at mountPath (volume-kind fix, §21 #83): 'dir' = a folder, 'file' = one file (e.g.
   * /data/flows.json). Always stored explicitly; a request without it (and legacy documents / revision catalogs, where
   * the key is absent) get `inferImageVolumeKind(mountPath)`: a last path segment with an extension → file, else dir.
   * Read it with `imageVolumeKind(v)`. Registry Volumes entries are always 'dir'. Containers: a folder data volume on a
   * 'file' image volume → V_VOLUME_KIND_MISMATCH (error); a single-file data volume on a 'dir' one → V_VOLUME_KIND_IMAGE (warning).
   */
  kind: ImageVolumeKind;
  description: string;
}
export type ImageVolumeKind = 'dir' | 'file';
/** An image volume in a register / patch request: kind optional (absent → inferred from the path and stored explicitly). */
export type ImageVolumeInput = Omit<ImageVolume, 'kind'> & { kind?: ImageVolumeKind };
export interface HealthCheckBase {
  intervalSec: number; // 1–3600, default 10
  timeoutSec: number; //  1–600, default 5
  retries: number; //     1–100, default 3
  startPeriodSec: number; // 0–3600, default 30
}
export type HealthCheck = HealthCheckBase &
  (
    | { type: 'http'; portName: Slug; path: string; expectStatus: number }
    | { type: 'tcp'; portName: Slug }
    | { type: 'exec'; command: string[]; expectExitCode: number }
    | { type: 'log'; pattern: string }
  );

export interface ImageLayer {
  digest: Sha256;
  sizeBytes: number;
  mediaType: string; // e.g. "application/vnd.oci.image.layer.v1.tar+gzip"
}
export interface ImageImport {
  mode: ImportMode;
  status: ImportStatus;
  error: string | null;
  sizeBytes: number | null; // sum of layers
  /** manifest media type of the platform manifest that was stored */
  manifestMediaType: string | null;
  configDigest: Sha256 | null; // image config blob (= docker image id)
  layers: ImageLayer[];
  startedAt: Ts | null;
  finishedAt: Ts | null;
}
/** A parsed docker reference `[host/]repository[:tag][@sha256:digest]` (parseImageReference in validate.ts). */
export interface ImageRefParts {
  host: string; //         "docker.io" for Docker Hub (docker.io / index.docker.io / registry-1.docker.io), else the registry host[:port]
  repository: string; //  Docker Hub single names get "library/" (redis → library/redis)
  tag: string | null; //   null only when a digest is given; "latest" when neither is given
  digest: Sha256 | null;
}
export interface Image extends Audit {
  /**
   * The OPM version of the image: the document id (services/{serviceSlug}/images/{version}), unique per service, immutable.
   * RE.version ^[a-z0-9][a-z0-9._-]{0,63}$ — e.g. v1, v2, 3.1.0. Containers reference serviceSlug + version; on a site the
   * image is tagged `<serviceSlug>:<version>` (e.g. northwind-backend:v1).
   */
  version: string;
  serviceSlug: Slug; //       parent (denormalized)
  /** the full docker reference to import, as entered: e.g. "docker.io/northwind/backend:3.0.9" (≤ 512) */
  reference: string;
  /** parsed + normalized `reference` (stored for queries / display) */
  source: ImageRefParts;
  /** registry whose credentials are used (matched by host; the user may override); null = public, no credentials */
  registryId: Id | null;
  digest: Sha256 | null; // manifest digest, set by import, immutable once set
  platform: Platform; //  immutable (identity)
  import: ImageImport; // identity: set by the import job only
  kind: ImageKind; //     editable (§21 #82: everything but the identity is editable)
  entrypoint: string[] | null; // editable
  command: string[] | null; // editable (containers may override)
  workdir: string | null; // editable
  user: string | null; // editable
  ports: ImagePort[]; //  editable: add / remove / rename (in-use renames + removals → 409 in-use, PatchImageRequest)
  env: ImageEnv[]; //     editable: add / remove / rename
  volumes: ImageVolume[]; // editable: add / remove / rename
  healthcheck: HealthCheck | null; // editable
  stopTimeoutSec: number; // 1–600, default 30, editable
  description: string; // editable
}

/** What an agent needs of an image; copied into Revision.catalog at publish so revisions are self-contained. */
export interface ImageSpec {
  serviceSlug: Slug;
  version: string;
  digest: Sha256;
  configDigest: Sha256;
  platform: Platform;
  manifestMediaType: string;
  layers: ImageLayer[];
  sizeBytes: number;
  kind: ImageKind;
  entrypoint: string[] | null;
  command: string[] | null;
  workdir: string | null;
  user: string | null;
  ports: ImagePort[];
  env: ImageEnv[];
  volumes: ImageVolume[];
  healthcheck: HealthCheck | null;
  stopTimeoutSec: number;
  /**
   * §21 #113 a SITE image (digest "", version `local-<12 hex>`, added on a site or adopted from Docker): the pullable
   * docker reference it came from (pinned `@sha256:` when known). Absent for catalog images / images built on the host.
   */
  reference?: string;
}
/** §21 #104 / #113: a site image has no blobs in the cloud (digest ""), it lives in the site's Docker. */
export const isSiteImage = (s: Pick<ImageSpec, 'digest'>) => !s.digest;

export interface NotFoundPage {
  notFoundPageId: Id;
  label: string; //      1–80
  htmlCode: string; //   ≤ 262144 bytes UTF-8
  createdAt: Ts;
  createdBy: Uid;
}

export interface Tag extends Audit {
  tagSlug: Slug;
  label: string;
  icon: Icon;
  color: string; // ^#[0-9a-f]{6}$
}

// ───────────────────────────────────────────────────────────── access
export type Action = 'read' | 'write' | 'publish' | 'admin';
export type DeveloperResource = 'registries' | 'services' | 'images' | 'notFoundPages' | 'templates' | 'tags' | 'accounts';
export type DeploymentScopedResource =
  | 'deployment.setup'
  | 'deployment.config'
  | 'deployment.secrets'
  | 'deployment.activities'
  | 'deployment.transfers';
export type Resource = DeveloperResource | 'deployments' | DeploymentScopedResource;
export type Scope = { kind: 'all' } | { kind: 'tags'; tagSlugs: Slug[] } | { kind: 'deployments'; deploymentIds: Id[] };
export interface AccessRule {
  resource: Resource;
  actions: Action[]; // 1–4 unique; must be valid for the resource (see access.ts VALID_ACTIONS)
  scope: Scope;
}
/** API view of a stored credential: metadata only (the value comes from its reveal endpoint). */
export interface StoredCredentialMeta {
  setAt: Ts;
  setBy: Uid;
}
export interface Account {
  uid: Uid;
  img: Icon; //         avatar: "upload:<iconId>", "lucide:<name>", legacy https URL, or null (initials)
  name: string; //      1–80
  email: string; //     unique, lower-case, immutable
  phone: string | null; // E.164 ^\+[1-9]\d{6,14}$
  access: AccessRule[];
  disabled: boolean;
  createdAt: Ts;
  createdBy: Uid; // "bootstrap" for the first admin
  lastLoginAt: Ts | null;
  /**
   * §21 #84: the last password an admin set ("Set new password…" or account creation), viewable with
   * revealAccountPassword until the person changes their own password (Auth tokensValidAfterTime moves → cleared).
   * null = none stored. The API returns only { setAt, setBy }; Firestore holds AccountDoc.lastSetPassword.
   */
  lastSetPassword: StoredCredentialMeta | null;
  /** the last password-reset link (Firebase oob link, 1 h), viewable with revealAccountResetLink until it expires / is used */
  lastResetLink: (StoredCredentialMeta & { expiresAt: Ts }) | null;
}
/** Firestore accounts/{uid}: the API Account + the stored values (never returned except by the reveal endpoints). */
export type AccountDoc = Omit<Account, 'lastSetPassword' | 'lastResetLink'> & {
  /** validAfter = Auth tokensValidAfterTime (ISO) right after the set; a later one = the person changed it → cleared */
  lastSetPassword: (StoredCredentialMeta & { value: string; validAfter: Ts | null }) | null;
  lastResetLink: (StoredCredentialMeta & { value: string; expiresAt: Ts; validAfter: Ts | null }) | null;
};
// ───────────────────────────────────────────────────────────── root audit log (SPEC-final §6.2)
/** What an audit entry is about. */
export const AUDIT_TARGET_TYPES = ['registry', 'service', 'image', 'notFoundPage', 'template', 'tag', 'account', 'deployment', 'upload', 'blob', 'icon', 'system', 'github'] as const;
export type AuditTargetType = (typeof AUDIT_TARGET_TYPES)[number];
/**
 * Every root-scope audit action. Human writes (every non-GET human request outside /v1/deployments/{id}/…) and
 * system jobs. Deployment-internal actions are activities (ACTIVITY_TYPES), not audit entries.
 */
export const AUDIT_ACTIONS = [
  'registry.created',
  'registry.updated',
  'registry.deleted',
  'registry.tested',
  'registry.password-revealed',
  'service.created',
  'service.updated',
  'service.deleted',
  'image.inspected',
  'image.registered',
  'image.updated',
  'image.deleted',
  'image.import-retried',
  'image.import-ready', //  system (import job)
  'image.import-failed', // system (import job)
  'not-found-page.created',
  'not-found-page.deleted',
  'template.created',
  'template.updated',
  'template.deleted',
  'template.committed', //          §21 #104 working copy committed
  'template.merged', //             §21 #104 merge / cherry-pick into a template (data.kind)
  'template.reverted',
  'template.reset',
  'template.working-discarded',
  'tag.created',
  'tag.updated',
  'tag.deleted',
  'account.created',
  'account.updated', //        profile only (name / phone / img)
  'account.access-changed', // access rules changed (profile changes in the same request are in `changes`)
  'account.disabled',
  'account.enabled',
  'account.password-reset', //       legacy (before §21 #84): a password-set link was made
  'account.password-set', //         §21 #84 "Set new password…" (also written at account creation)
  'account.password-revealed',
  'account.reset-link-created',
  'account.reset-link-revealed',
  'deployment.created',
  'deployment.updated',
  'deployment.deleted',
  'deployment.access-key-revealed', //      §21 #84: deployment-scoped credential ops are audited AND write a _cloud activity
  'deployment.access-key-rotated',
  'deployment.install-command-created', //  also Regenerate (replaceActive)
  'deployment.install-command-revealed',
  'deployment.upgrade-command-revealed', //    §21 #92: the enrolled site's "Upgrade / reinstall command" viewed
  'deployment.upgrade-command-regenerated', // §21 #92: a new site-bound maintenance token minted (old upgrade command revoked)
  'deployment.secret-revealed',
  'deployment.secret-regenerated',
  'deployment.agent-upgrade-requested', //  §21 #90: "Upgrade agent" / "Upgrade all" in the cloud console (also a _cloud activity)
  'deployment.agent-upgrade-cancelled', //  §21 #90: a queued request / a run cancelled before it started on the site
  'deployment.template-synced', //   §21 #94: Template sync applied to the draft (also a _cloud activity draft.template-synced)
  'deployment.template-unlinked', // §21 #94
  'deployment.committed', //        §21 #104 working copy committed (= a release; also a _cloud activity branch.committed)
  'deployment.merged', //           §21 #104 merge / cherry-pick into a deployment (data.kind)
  'deployment.reverted',
  'deployment.reset',
  'deployment.working-discarded',
  'blob.uploaded',
  'upload.created',
  'upload.completed',
  'icon.uploaded', //          also when the same bytes already existed (data.existing = true)
  'icon.deleted',
  'github.updated', //         §21 #106 Admin → GitHub: owner / repo / enabled / token changed (token "***"; data.tokenReplaced / tokenRemoved)
  'github.tested', //          §21 #106 Test connection
  'github.token-revealed', //  §21 #106 token viewed (60 s)
  'github.synced', //          §21 #106 Sync now
  'github.resynced', //        §21 #106 Resync everything
  'github.push-failed', //     §21 #106 system: a branch stopped pushing (first failure of a run of failures; data { gitBranch, status, message })
  'system.daily-cleanup', //   system (daily job; one summary entry per run)
  'system.migration', //       system (data migration scripts; data = { migration, dryRun, changes })
] as const;
export type AuditAction = (typeof AUDIT_ACTIONS)[number];
/**
 * Default action + target type of every audited CLOUD_API op (the router writes one entry per request, success or
 * failure; handlers may refine the action, e.g. patchAccount → account.access-changed, disableAccount → account.enabled).
 * putUploadPart is the only human write without an entry (logged once at completeUpload with the part count).
 */
export const AUDIT_OPS = {
  createRegistry: { action: 'registry.created', targetType: 'registry' },
  patchRegistry: { action: 'registry.updated', targetType: 'registry' },
  deleteRegistry: { action: 'registry.deleted', targetType: 'registry' },
  testRegistry: { action: 'registry.tested', targetType: 'registry' },
  revealRegistryPassword: { action: 'registry.password-revealed', targetType: 'registry' },
  createService: { action: 'service.created', targetType: 'service' },
  patchService: { action: 'service.updated', targetType: 'service' },
  deleteService: { action: 'service.deleted', targetType: 'service' },
  inspectImage: { action: 'image.inspected', targetType: 'image' },
  registerImage: { action: 'image.registered', targetType: 'image' },
  patchImage: { action: 'image.updated', targetType: 'image' },
  deleteImage: { action: 'image.deleted', targetType: 'image' },
  retryImport: { action: 'image.import-retried', targetType: 'image' },
  createNotFoundPage: { action: 'not-found-page.created', targetType: 'notFoundPage' },
  deleteNotFoundPage: { action: 'not-found-page.deleted', targetType: 'notFoundPage' },
  createTemplate: { action: 'template.created', targetType: 'template' },
  patchTemplate: { action: 'template.updated', targetType: 'template' },
  deleteTemplate: { action: 'template.deleted', targetType: 'template' },
  commitTemplate: { action: 'template.committed', targetType: 'template' },
  discardTemplateWorking: { action: 'template.working-discarded', targetType: 'template' },
  merge: { action: 'deployment.merged', targetType: 'deployment' }, //        handler refines: template.merged for template targets
  revertCommit: { action: 'deployment.reverted', targetType: 'deployment' }, // handler refines: template.reverted
  resetToCommit: { action: 'deployment.reset', targetType: 'deployment' }, //  handler refines: template.reset
  commitDeployment: { action: 'deployment.committed', targetType: 'deployment' },
  discardDeploymentWorking: { action: 'deployment.working-discarded', targetType: 'deployment' },
  createTag: { action: 'tag.created', targetType: 'tag' },
  patchTag: { action: 'tag.updated', targetType: 'tag' },
  deleteTag: { action: 'tag.deleted', targetType: 'tag' },
  createAccount: { action: 'account.created', targetType: 'account' },
  patchAccount: { action: 'account.updated', targetType: 'account' },
  disableAccount: { action: 'account.disabled', targetType: 'account' },
  resetAccountPassword: { action: 'account.reset-link-created', targetType: 'account' },
  setAccountPassword: { action: 'account.password-set', targetType: 'account' },
  revealAccountPassword: { action: 'account.password-revealed', targetType: 'account' },
  revealAccountResetLink: { action: 'account.reset-link-revealed', targetType: 'account' },
  createDeployment: { action: 'deployment.created', targetType: 'deployment' },
  patchDeployment: { action: 'deployment.updated', targetType: 'deployment' },
  deleteDeployment: { action: 'deployment.deleted', targetType: 'deployment' },
  revealAccessKey: { action: 'deployment.access-key-revealed', targetType: 'deployment' },
  rotateAccessKey: { action: 'deployment.access-key-rotated', targetType: 'deployment' },
  createInstallToken: { action: 'deployment.install-command-created', targetType: 'deployment' },
  revealInstallCommand: { action: 'deployment.install-command-revealed', targetType: 'deployment' },
  revealUpgradeCommand: { action: 'deployment.upgrade-command-revealed', targetType: 'deployment' },
  regenerateUpgradeCommand: { action: 'deployment.upgrade-command-regenerated', targetType: 'deployment' },
  revealSecret: { action: 'deployment.secret-revealed', targetType: 'deployment' },
  regenerateSecret: { action: 'deployment.secret-regenerated', targetType: 'deployment' },
  requestAgentUpgrade: { action: 'deployment.agent-upgrade-requested', targetType: 'deployment' },
  upgradeAllAgents: { action: 'deployment.agent-upgrade-requested', targetType: 'deployment' },
  cancelAgentUpgrade: { action: 'deployment.agent-upgrade-cancelled', targetType: 'deployment' },
  cancelAgentUpgradeRun: { action: 'deployment.agent-upgrade-cancelled', targetType: 'deployment' },
  applyTemplateSync: { action: 'deployment.template-synced', targetType: 'deployment' },
  unlinkTemplate: { action: 'deployment.template-unlinked', targetType: 'deployment' },
  uploadBlob: { action: 'blob.uploaded', targetType: 'blob' },
  createUpload: { action: 'upload.created', targetType: 'upload' },
  completeUpload: { action: 'upload.completed', targetType: 'upload' },
  uploadIcon: { action: 'icon.uploaded', targetType: 'icon' },
  deleteIcon: { action: 'icon.deleted', targetType: 'icon' },
  patchGithubMirror: { action: 'github.updated', targetType: 'github' },
  testGithubMirror: { action: 'github.tested', targetType: 'github' },
  revealGithubToken: { action: 'github.token-revealed', targetType: 'github' },
  syncGithubMirror: { action: 'github.synced', targetType: 'github' },
  resyncGithubMirror: { action: 'github.resynced', targetType: 'github' },
} as const satisfies Record<string, { action: AuditAction; targetType: AuditTargetType }>;
/**
 * §21 #84: deployment-scoped ops (/v1/deployments/{id}/…) that ARE written to the root audit log too (credential
 * reveals / regenerations); every other deployment-internal op is only a _cloud activity.
 */
export const AUDIT_DEPLOYMENT_OPS = ['revealAccessKey', 'rotateAccessKey', 'createInstallToken', 'revealInstallCommand', 'revealUpgradeCommand', 'regenerateUpgradeCommand', 'revealSecret', 'regenerateSecret', 'requestAgentUpgrade', 'upgradeAllAgents', 'cancelAgentUpgrade', 'cancelAgentUpgradeRun', 'applyTemplateSync', 'unlinkTemplate', 'commitDeployment', 'discardDeploymentWorking'] as const;
/**
 * §21 #84: every CLOUD_API op whose response carries a credential value. The router sends `Cache-Control: no-store`
 * on them and rate-limits them per uid (REVEAL_RATE_PER_MINUTE, fixed 1-minute window, Firestore counter
 * `rateLimits/reveal_<uid>_<yyyymmddhhmm>`; 429 rate-limited, details.retryAfterSec, Retry-After).
 */
export const CREDENTIAL_OPS = [
  'createDeployment',
  'revealAccessKey',
  'rotateAccessKey',
  'createInstallToken',
  'revealInstallCommand',
  'revealUpgradeCommand',
  'regenerateUpgradeCommand',
  'revealSecret',
  'regenerateSecret',
  'revealRegistryPassword',
  'createAccount',
  'setAccountPassword',
  'revealAccountPassword',
  'resetAccountPassword',
  'revealAccountResetLink',
  'revealGithubToken',
] as const;
/** Human writes that are NOT audited (putUploadPart: logged at completeUpload; previewTemplateSync: a dry run that writes nothing, §21 #94; §21 #107 commit staging: the commit is the one entry — createUpload / completeUpload with a stageId are skipped by the router too). */
export const AUDIT_EXEMPT_OPS = ['putUploadPart', 'previewTemplateSync', 'compareBranches', 'previewMerge', 'createStage', 'putStageBlob', 'deleteStage'] as const;
/** One changed leaf. Secret values are "***" (secret = true); values > 2 KB are AuditTruncated. */
export interface AuditChange {
  path: string; //      e.g. "label", "sites[app].containers[redis].env.FOO", "variables[port].value"
  before: unknown; //   null when the field / document did not exist
  after: unknown; //    null when the field / document was removed
  secret?: boolean; //  true: a secret value changed (both sides masked as "***", or null when unset)
}
/** Replaces a value whose canonical JSON is larger than 2 KB. */
export interface AuditTruncated {
  $truncated: true;
  bytes: number; //   size of the full value (JSON)
  preview: string; // first 2000 characters of the (redacted) JSON
}
export interface AuditLogEntry {
  auditId: Id;
  ts: Ts;
  /** the CLOUD_API op name (e.g. "patchTemplate"), or "system:<job>" (system:importImage, system:daily) */
  op: string;
  /** uid of the person, or "system" */
  actor: Uid | 'system';
  actorEmail: string | null;
  action: AuditAction | string; // AUDIT_ACTIONS (string for entries written before the catalog)
  outcome: 'ok' | 'error';
  /** HTTP status of the answer (system entries: 200 ok / 500 error) */
  status: number;
  /** ErrorCode of a failed request (forbidden, invalid, conflict, …), null when ok */
  errorCode: string | null;
  errorMessage: string | null;
  targetType: AuditTargetType;
  /** document path of the target, e.g. "registries/{registryId}", "services/{id}/images/{version}", "blobs/sha256:…" */
  target: string;
  requestId: string | null; // X-Request-Id of the request (null for system entries)
  ip: string | null;
  userAgent: string | null;
  /** deep diff of the target document (before → after); create = the created doc, delete = the deleted doc.
   *  ≤ 200 entries (changesTotal tells how many there were); secrets redacted */
  changes: AuditChange[];
  changesTotal: number;
  /** extra facts (redacted like changes), e.g. { removedFrom } for deleteTag, { partCount } for completeUpload */
  data: Record<string, unknown>;
}
/** GET /v1/audit-log query (one filter runs in Firestore, the others in memory). */
export interface AuditLogQuery {
  action?: string; //     exact action or a prefix ending in "." (e.g. "template.")
  actor?: string; //      uid, "system", or an e-mail (contains "@")
  targetType?: AuditTargetType;
  target?: string; //     exact target path, e.g. "templates/abc"
  outcome?: 'ok' | 'error';
  from?: Ts; //           ts ≥ from
  to?: Ts; //             ts ≤ to
  cursor?: string;
  limit?: number; //      default 100, max 500
}

// ───────────────────────────────────────────────────────────── config (templates + deployments)
export type VariableType = 'string' | 'number' | 'boolean' | 'enum' | 'list' | 'json';
export type VariableValue = string | number | boolean | string[] | Record<string, unknown> | unknown[] | null;
export interface Variable {
  variableSlug: Slug;
  label: string;
  description: string;
  type: VariableType;
  value: VariableValue; // template = default; deployment = actual value
  required: boolean; //   default false
  options: string[] | null; // enum only, 1–100 unique
  validation: { min: number | null; max: number | null; pattern: string | null } | null;
}
export type SecretCharset = 'alnum' | 'hex' | 'base64url';
export interface SecretDef {
  secretSlug: Slug;
  label: string;
  description: string;
  required: boolean; // default true
  generate: { charset: SecretCharset; length: number } | null; // length 8–128
  value: string | null; // plain (prototype); ALWAYS null in templates; ≤ 8192 bytes
  rotatedAt: Ts | null;
}
export type SecretDefView = Omit<SecretDef, 'value'> & { value: Masked | null; isSet: boolean };
/** Request item for PUT draft/secrets and agent local proposals.
 *  plainValue: absent/undefined = keep the current value; null = clear; string = set (rotatedAt = now).
 *  generateNow: true = the server generates a new value with `generate` (overrides plainValue). */
export type SecretDefInput = Omit<SecretDef, 'value' | 'rotatedAt'> & { plainValue?: string | null; generateNow?: boolean };

/**
 * Where something lives on the site server (round D). "agentHome" = the agent data dir (`--data-dir` / OPM_DATA_DIR,
 * default /var/lib/opm). auto = the default location: files `<agentHome>/files/<fileSlug>`, site volumes
 * `<agentHome>/volumes/<volumeSlug>`. path: "./x/y" = relative to agentHome (no ".." segments), or an absolute path used
 * as is (isSafeMountPath, no {{ }}). Locations are unique per site and never nested in each other (V_LOCATION).
 */
export type StorageLocation = { kind: 'auto' } | { kind: 'path'; path: string };
export type FileRender = 'expressions' | 'none';
export interface FileDef {
  fileSlug: Slug;
  label: string;
  description: string;
  mime: string; //     type/subtype
  editor: 'text' | 'binary';
  maxBytes: number; // 1–52428800, default 1048576
  content: { sha256: Sha256; sizeBytes: number } | null; // blob "files/<hex>" (the SOURCE; never the rendered bytes)
  /**
   * 'expressions' (default for editor text): the content is a template; {{ }} blocks resolve PER MOUNTING CONTAINER at
   * staging on the site (field "file": var, secret, file, site, self, deployment, container incl. container.env.*).
   * Literal "{{" is written {{ "{{" }}. Rendered bytes live only on the site (mode 0400, read-only mount).
   * 'none' (default for editor binary; binary files are always 'none'): mounted byte for byte.
   * Missing on legacy documents = the default for the editor (normalizeFileDef).
   * Round D: a file is rendered ONCE PER SITE (roots var, secret, site, self, deployment — container.* and file.* are
   * E_NOT_ALLOWED: the copy is shared by every container of the site).
   */
  render: FileRender;
  /**
   * Where the ONE copy of this file lives on each site (round D; default auto = <agentHome>/files/<fileSlug>). Every
   * container that mounts the file bind-mounts that single host file read-only. Mode 0400 when the rendered content holds
   * a secret in clear, else 0444 (§21 #103: also when every secret only went through mosquitto_passwd — a salted hash the
   * container reads as a non-root user). Missing on legacy documents = auto (normalizeFileDef).
   */
  location: StorageLocation;
}

export interface ContainerEnv {
  name: string; //               ^[A-Z_][A-Z0-9_]*$, unique in the container
  value: Value | null; //        exactly one of value / secretRef
  secretRef: Slug | null;
}
/**
 * A data folder of a site (SPEC §2.2 "Site volumes", round D). Declared on the site; containers mount it by volumeSlug
 * (VolumeSource kind 'volume'). On the agent: its location (auto = `<agentHome>/volumes/<volumeSlug>/`). Several containers (and several mounts) may share
 * one. Snapshots archive exactly the declared volumes with backup = true (host paths are never backed up).
 */
export interface SiteVolume {
  volumeSlug: string; //  the ONLY identifier, unique per site, RE.volumeSlug ^[a-z0-9][a-z0-9_.-]{0,99}$ (never renamed: rename = new volume)
  label: string; //       0–80 (empty = the GUIs show the volumeSlug)
  description: string; // 0–2000
  backup: boolean; //     default true: included in manual / scheduled / pre-update snapshots
  /** where the folder lives on the server: auto = <agentHome>/volumes/<volumeSlug> (default); backed up wherever it is */
  location: StorageLocation;
  /**
   * round E: 'dir' (default) = a folder; 'file' = ONE persistent read-write file at the location (auto: the file
   * <agentHome>/volumes/<volumeSlug>), mounted at a FILE path in the container (mount path must not end with "/").
   * Apps that save by writing a temp file + rename (e.g. Node-RED flows.json) need a 'dir' volume: a single-file bind
   * mount cannot be renamed over (EBUSY). Backed up / restored like any volume.
   */
  kind: SiteVolumeKind;
  /**
   * round E: starter content, applied ONLY when the volume is created or empty (never re-seeded); null = start empty.
   * §21 #101: the volume's OWN content (`tree` / `single`, blobs in cloud Storage, a manifest here); Files are never
   * volume content (legacy `files` / `file` seeds exist only in revisions published before 0.12).
   */
  seed: VolumeSeed | null;
  /**
   * §21 #101 "persist to cloud" (folder volumes only): the agent snapshots the live tree to cloud Storage (versioned),
   * the cloud edits it and sends it back (approved on the site). OMITTED when off (absent = off — keeps revisions
   * hash-compatible with agents < 0.12; read it with `volumePersist(v)`).
   */
  persist?: VolumePersist;
  /** §21 #112 automatic backups of this volume; OMITTED = the daily scheduled site snapshot (7 kept), as before */
  backupPolicy?: VolumeBackupPolicy;
}
export type SiteVolumeKind = 'dir' | 'file';

// ───────────────────────── §21 #112 per-volume automatic backups (own block: keep merges easy)
export const VOLUME_BACKUP_SCHEDULES = ['off', 'hourly', '6h', 'daily', 'weekly'] as const;
export type VolumeBackupSchedule = (typeof VOLUME_BACKUP_SCHEDULES)[number];
/**
 * How often the agent snapshots this one volume and how many automatic backups it keeps (keep 1 … 100). hourly / 6h
 * are taken live (the containers keep running); daily / weekly stop the volume's containers around the archive, like
 * the scheduled site snapshot. A volume with its own policy leaves the daily site snapshot (no double backups).
 */
export interface VolumeBackupPolicy {
  schedule: VolumeBackupSchedule;
  keep: number;
}
export const DEFAULT_VOLUME_BACKUP_POLICY: VolumeBackupPolicy = { schedule: 'daily', keep: 7 };
/** one snapshot on the site that holds the volume (any reason) — every one is restorable from the Volumes page */
export interface VolumeBackup {
  snapshotId: string;
  createdAt: Ts;
  reason: string; // volume-auto | scheduled | pre-update | manual | imported
  note: string;
  createdBy: string;
  sizeBytes: number; // the archive of this volume
  auto: boolean; //   taken by the volume's own schedule
}
/** §21 #112 GET /_opm/api/sites/{site}/agent/process — the agent process itself (not Docker, not the containers) */
export interface AgentProcessStats {
  siteSlug: string;
  version: string;
  pid: number;
  uptimeSec: number;
  rssBytes: number | null; //   /proc/self (null off Linux)
  cpuPct: number | null; //     of one core, since the previous call (null on the first call / off Linux)
  openFds: number | null;
  threads: number | null;
  heapInUseBytes: number;
  heapSysBytes: number;
  memLimitBytes: number; //      Go soft limit (GOMEMLIMIT or the default)
  goroutines: number;
  gcCount: number;
  gcPauseLastMs: number;
  dockerCalls: number; //        requests to the daemon since start
  dockerCallsPerMin: number | null;
  dockerCacheLive: boolean; //   the events stream is up (lists / inspects served from the cache)
  dockerCacheHits: number;
  store: { ok: boolean; lastError: string | null; failures: number };
  disk: { opmDbBytes: number; metricsDbBytes: number; blobsBytes: number; snapshotsBytes: number; snapshotBudgetBytes: number; logsBytes: number; freeBytes: number; totalBytes: number };
  warnings: string[];
  loops: { name: string; periodSec: number; ticks: number; running: boolean; panics: number; lastMs?: number; lastEnd?: Ts; runningForMs?: number; lastPanic?: string }[];
}
/** GET /_opm/api/sites/{site}/volumes/{volumeSlug}/backups */
export interface VolumeBackupsResponse {
  volumeSlug: string;
  policy: VolumeBackupPolicy;
  explicit: boolean; // the volume has its own policy (else the daily site snapshot)
  nextAt: Ts | null;
  items: VolumeBackup[];
}
/**
 * §21 #101 one file of a volume's own content (a manifest entry; the bytes are the content-addressed blob
 * `files/<hex>` in cloud Storage, like FileDef.content). path = relative inside the volume ("/"-separated, no "." /
 * ".." / empty segments, no NUL / control chars / "\", ≤ 1024 UTF-8 bytes, segments ≤ 255 bytes; `contentPathError`).
 * mode = permission bits "0644" (^0[0-7]{3}$). render: 'expressions' = rendered ONCE PER SITE when the starter
 * content is written (like a File with render expressions; only in starter content); persisted versions are always 'none'.
 */
export interface VolumeTreeEntry {
  path: string;
  sha256: Sha256;
  size: number; //   bytes, 0 – VOLUME_CONTENT_LIMITS.fileMaxBytes
  mode: string; //   "0644" (default), "0755", "0600" …
  render: FileRender;
}
/**
 * §21 #101 a volume file tree (manifest). Canonical form (`normalizeVolumeTree`): entries sorted by path (UTF-16 code
 * units, as JCS), `dirs` = only EMPTY folders (folders implied by an entry or a deeper dir are dropped), sorted.
 * treeHash = "sha256:" + hex(sha256(JCS(canonical tree))) — vectors shared/testdata/volume-content-vectors.json (Go mirror).
 */
export interface VolumeTree {
  entries: VolumeTreeEntry[];
  dirs: string[];
}
/** §21 #101 persist-to-cloud settings of a folder volume (SiteVolume.persist; absent = off). */
export interface VolumePersist {
  /** gitignore-lite patterns never snapshotted nor touched by an apply (≤ 50, 1–200 chars; `excludePatternError`) */
  exclude: string[];
  /** size cap of one snapshot (sum of the non-excluded files), 1 MiB – 200 MiB, default 50 MiB */
  maxBytes: number;
  /** snapshot automatically when the live tree changed and has been quiet for 2 min (≤ 1 per 15 min); default true */
  auto: boolean;
}
/**
 * Starter content of a site volume (round E). Applied by the agent at APPLY, before containers start, only when the
 * volume does not exist yet or is empty (dir without entries / file of size 0); then marked seeded (marker
 * `.opm-seeded` in a dir volume, agent DB state for both) so it never seeds again. Later seed changes never touch
 * existing data ("seed changed — applies only to new / empty volumes"); the agent GUI "Reset to starter content…"
 * re-applies it on purpose (snapshot first, 60 s + ACCEPT).
 *   files   — dir volumes: entries[].path relative inside the volume (segments [A-Za-z0-9._-], no "." / "..", unique,
 *             1–100 entries) ← a template file's content (render 'expressions' → rendered once per site)
 *   file    — file volumes: the file's content (rendered like above)
 *   archive — dir volumes: a .tar.gz blob (CreateUploadRequest purpose 'seed-archive', ≤ SEED_ARCHIVE_MAX); never
 *             rendered; regular files + dirs only, relative safe paths, no symlinks / hard links / devices
 */
export type VolumeSeed =
  /** §21 #101 folder volumes: the volume's own file tree (≤ 1000 files, ≤ 1000 empty folders, ≤ 200 MiB in total) */
  | ({ kind: 'tree' } & VolumeTree)
  /** §21 #101 single-file volumes: the file's own content */
  | { kind: 'single'; sha256: Sha256; size: number; mode: string; render: FileRender }
  /** @deprecated §21 #101 — legacy (Files as volume content): accepted only in revisions published before 0.12 */
  | { kind: 'files'; entries: { path: string; fileSlug: Slug }[] }
  /** @deprecated §21 #101 — legacy (a File as volume content): accepted only in revisions published before 0.12 */
  | { kind: 'file'; fileSlug: Slug }
  | { kind: 'archive'; blobSha256: Sha256; name: string; size: number };
export const SEED_ARCHIVE_MAX = 200 * 1024 * 1024;
/** Where a mount gets its content from (SPEC §2.2 "Container instances" + "Site volumes"). */
export type VolumeSource =
  | { kind: 'volume'; volumeSlug: string } // a volume DECLARED in SiteConfig.volumes (else V_VOLUME_UNDECLARED); shareable; readOnly default false
  | { kind: 'host'; path: string } //       advanced: bind of an absolute host path (literal, isSafeMountPath); never backed up; readOnly default false
  | { kind: 'file'; fileSlug: Slug } //     always read-only; ONE copy per site at FileDef.location (rendered once per site)
  | { kind: 'secret'; secretSlug: Slug } // always read-only; ONE copy per site at <agentHome>/secrets/<secretSlug>, mode 0400
  | { kind: 'none' }; //                    image volume not mounted (container-local storage, lost on recreate)
export type VolumeSourceKind = VolumeSource['kind'];
export interface ContainerMount {
  /** the image volume this row maps (one row per image volume); null = an extra mount */
  imageVolume: Slug | null;
  /** extra mounts: required; image-volume rows: null = the image mountPath (or an override). May hold {{ }} (field mountPath). */
  mountPath: string | null;
  source: VolumeSource; //  extra mounts cannot use kind 'none'. An image volume WITHOUT a row is not mounted (warning V_VOLUME_NOT_MOUNTED)
  readOnly: boolean; //     volume + host: default false; file + secret: must be true (V_READONLY)
}
export type HostIp = '0.0.0.0' | '127.0.0.1';
export interface ContainerPort {
  portName: Slug; //             a port name of the image (one row per image port; missing row = not published, edge true)
  hostPort: number | Expr | null; // null = not published; ≠ 80/443/edge/agent ports, unique per site + protocol
  hostIp: HostIp; //             bind address of the published port; default '0.0.0.0'
  edge: boolean; //              the edge may proxy to this port (default true)
}
/**
 * One container INSTANCE on a site. The same service + version may run many times per site with different env / ports /
 * volumes. containerSlug is the ONLY identifier: unique per site, the DNS name on the site network, used in expressions
 * (container.slug, containers.<containerSlug>.host|port.<portName>, site.<site>.port.<containerSlug>.<portName>), edge
 * targets, depends and docker names (the containerSlug; agent.json containerNames "prefixed": opm_<dep>_<site>_<containerSlug>,
 * SPEC §21 #88). The image tag stays `<serviceSlug>:<version>` (shared by instances).
 */
export interface Container {
  containerSlug: Slug; //        ^[a-z][a-z0-9-]{1,39}$; default = serviceSlug, then serviceSlug-2, -3 … (suggestContainerSlug)
  serviceSlug: Slug; //          cloud only (local edits cannot change it)
  version: string; //            Image.version of that service; cloud only
  enabled: boolean;
  env: ContainerEnv[]; //        overrides of the image env + extra vars
  ports: ContainerPort[];
  volumes: ContainerMount[];
  command: string[] | null; //   overrides the image command
  entrypoint: string[] | null; // overrides the image entrypoint
  restart: 'always' | 'on-failure' | 'never';
  resources: { cpus: number | null; memoryMb: number | null }; // cpus 0.1–64, memoryMb 64–1048576
  healthcheck: HealthCheck | null; // null = the image health check
  depends: { containerSlug: Slug }[]; // containers of the same site, no cycles (sites: SiteConfig.depends)
}

export type EdgeTls =
  | { mode: 'none' }
  | { mode: 'internal' }
  | {
      mode: 'acme';
      email: string;
      challenge: 'http-01' | 'tls-alpn-01' | 'dns-01';
      dnsProvider: string | null; // cloudflare | digitalocean | duckdns | godaddy | route53 | googleclouddns | namecheap (SPEC §12.4)
      dnsTokenSecretSlug: Slug | null;
    }
  | { mode: 'files'; certFileSlug: Slug; keySecretSlug: Slug };
/**
 * Response headers (round E, nginx add_header / proxy_hide_header). Order applied to a response:
 * upstream headers → `remove` → `set` (replaces any value) → `add` (appends; duplicates allowed, as nginx add_header).
 * `set` / `add` apply only to statuses 200 201 204 206 301 302 303 304 307 308 unless the name is in `always`
 * (nginx "always": every status, also the edge's own 404 / 413 / 500 / 502 / 503 pages). `remove` applies to every
 * proxied response. Values may hold `{{ }}` (field edge) and the nginx variables of SPEC §12.6.
 */
export interface EdgeResponseHeaders {
  set: Record<string, Value>; //   nginx proxy_hide_header X + add_header X
  add: Record<string, Value>; //   nginx add_header X (alone)
  remove: string[]; //             nginx proxy_hide_header X (alone)
  always: string[]; //             names of set / add entries that apply to every status (nginx add_header … always)
}
export interface EdgeHost {
  hostname: string; // DNS name | "*.x.y" | IPv4 | IPv6 | "*" ; may be Expr (var.*, deployment.id, self.*)
  tls: EdgeTls;
  redirectHttpToHttps: boolean;
  /** round E: host level (nginx server level) response headers; routes with responseHeaders null inherit them. Default empty. */
  responseHeaders: EdgeResponseHeaders;
}
/** nginx `rewrite <regex> <replacement>`: regex (RE2 ∩ JS) against the current path; replacement uses $1…$9 + §12.6 variables. */
export interface EdgeRewrite {
  regex: string;
  replacement: Value;
}
export interface EdgeProxyAction {
  type: 'proxy';
  containerSlug: Slug; //      a container of THIS site
  portName: Slug; //           a port of that container's image whose ContainerPort.edge is true
  stripPrefix: boolean; //     prefix / exact routes only: drop the matched path from the upstream path
  /** round E: nginx `rewrite … break` (any pattern); exclusive with stripPrefix; null = none */
  rewrite: EdgeRewrite | null;
  addPrefix: string | null; // starts with "/"
  setHeaders: Record<string, Value>; // request header name → Value (Expr allowed: var.*, deployment.id, self.*; §12.6 variables)
  removeHeaders: string[];
  websocket: boolean; //       default true (round E)
  connectTimeoutSec: number; // 1–3600, default 60 (nginx proxy_connect_timeout)
  sendTimeoutSec: number; //   1–86400, default 60: idle time between two writes to the upstream (nginx proxy_send_timeout)
  readTimeoutSec: number; //   1–86400, default 60: time to the response header AND idle time between two reads (nginx proxy_read_timeout). Legacy `timeoutSec`
  requestBuffering: boolean; // default false = stream the request body; true = buffer up to 8 MiB in memory first (nginx proxy_request_buffering)
  responseBuffering: boolean; // default false = flush every write (Caddy flush_interval -1); text/event-stream always flushes (nginx proxy_buffering)
  maxBodyBytes: number | null; // null = inherit Edge.maxBodyBytes; 0 = unlimited; else 1–1099511627776 → 413 above (nginx client_max_body_size)
}
export interface EdgeRedirectAction {
  type: 'redirect';
  to: Value; //                absolute URL or path; placeholders {path} {query} {host} + §12.6 variables ($uri $args $1 …); Expr allowed
  status: 301 | 302 | 303 | 307 | 308;
  keepPath: boolean;
}
/** round E: internal rewrite of the path, then the routes are matched again (nginx `rewrite … last`); ≤ 10 per request, else 500. */
export interface EdgeRewriteAction {
  type: 'rewrite';
  regex: string; //            RE2 ∩ JS, against the current path (no query)
  replacement: Value; //       new path (may hold "?query": then it REPLACES the query); $1…$9 + §12.6 variables
}
export type EdgeRouteAction = EdgeProxyAction | EdgeRedirectAction | EdgeRewriteAction;
/**
 * round E (nginx location semantics, SPEC §12.1):
 *   exact  — the path equals `path` (nginx `location = /x`)
 *   prefix — the path starts with `path` (plain string prefix, nginx `location /x/`; `stopRegex` = nginx `^~`)
 *   regex  — `path` is a regular expression (RE2 syntax that JS also understands; a leading `(?i)` = case-insensitive,
 *            nginx `~` / `~*`) matched against the decoded request path without the query
 * Legacy shape `{ pathPrefix, methods }` = kind prefix (normalizeSiteConfig).
 */
export type EdgeMatchKind = 'prefix' | 'exact' | 'regex';
export interface EdgeMatch {
  kind: EdgeMatchKind;
  path: string;
  stopRegex: boolean; //       prefix only; false for exact / regex
  methods: string[] | null; // null = all; a route is invisible to other methods
}
export interface EdgeRoute {
  routeId: Slug; //            ^[a-z0-9][a-z0-9-]{0,62}$, unique in the site
  hosts: string[]; //          hostnames from edge.hosts; [] = all hosts
  match: EdgeMatch;
  action: EdgeRouteAction;
  /** integer ≥ 1; only the RELATIVE order of regex routes matters (exact > longest prefix are order-free); GUIs renumber 1..n */
  order: number;
  /** round E: null = inherit the host's responseHeaders; an object REPLACES them for this route (nginx add_header inheritance). Must be null for rewrite routes. */
  responseHeaders: EdgeResponseHeaders | null;
}
export interface Edge {
  httpPort: number; //         default 80
  httpsPort: number | null; // default 443; null = no TLS listener
  /** round E: request body limit of every proxy route (nginx http/server client_max_body_size); null = unlimited. Over → 413. */
  maxBodyBytes: number | null;
  hosts: EdgeHost[];
  routes: EdgeRoute[];
}
export interface SiteConfig {
  /**
   * Address of the site for containers of other sites and users (IP or DNS); Expr: var.*, deployment.id.
   * null = AUTO (default): the agent's advertised / detected primary address (AgentDoc.advertiseHost, detectedAddresses).
   * A host expression that resolves to null or "" also falls back to auto (warning V_HOST_AUTO in the cloud, never an error).
   */
  host: Value | null;
  agentPort: number; //        default 7080 (mesh + fallback GUI), 1024–65535
  agentGuiAliasPort: number | null; // default 8080, null = off, 1024–65535
  /** the data folders of this site (round D); containers mount them by volumeSlug. Default []. */
  volumes: SiteVolume[];
  containers: Container[];
  depends: { siteSlug: Slug }[];
  notFoundPageId: Id | null;
  edge: Edge;
  /**
   * §21 #97 dev-tool presets of this site (HTTP / PostgreSQL / SQLite / Cassandra / Redis / MQTT connections the agent
   * GUI lists read-only). Credentials only as expressions (`{{ secret.x }}`). OMITTED when empty (absent = [] — keeps
   * revisions of deployments without presets hash-compatible with agents < 0.11; siteDevTools() reads it).
   */
  devTools?: import('./devtools.js').DevToolPreset[];
}
export interface Site {
  siteSlug: Slug; //           not one of RESERVED_SITE_SLUGS
  label: string;
  config: SiteConfig;
}
export interface Config {
  variables: Variable[];
  secrets: SecretDef[];
  files: FileDef[];
  sites: Site[];
}
export type ConfigView = Omit<Config, 'secrets'> & { secrets: SecretDefView[] };
export type ConfigInput = Omit<Config, 'secrets'> & { secrets: SecretDefInput[] };

export interface Template extends Audit {
  templateId: Id;
  /**
   * §21 #106 the stable entity slug (GitHub mirror branch template/<slug>): slugifyLabel(label) made unique among
   * templates at create (or the requested CreateTemplateRequest.slug), IMMUTABLE (PATCH slug → invalid V_IMMUTABLE).
   * Older documents got one from migrate-k. RE.slug.
   */
  slug: Slug;
  label: string;
  icon: Icon;
  description: string;
  version: number; // 1 at create, +1 on every details PATCH and every commit that changes the config
  variables: Variable[]; //   §21 #105: the config fields are a COPY of the head commit config, written only by commits (no working copy)
  secrets: SecretDef[]; // value always null
  files: FileDef[];
  sites: Site[];
  /** §21 #104 branch state (head commit, origin, counts); absent on pre-0.14 documents until migrate-i */
  branch?: import('./branch.js').BranchState;
}
export type TemplateSummary = Pick<Template, 'templateId' | 'slug' | 'label' | 'icon' | 'description' | 'version' | 'updatedAt'> & {
  siteSlugs: Slug[];
  counts: { variables: number; secrets: number; files: number; sites: number };
  usedBy: number;
  /** §21 #104 */
  branch?: import('./branch.js').BranchState | null;
};

// ───────────────────────────────────────────────────────────── deployments
export interface HeadRef {
  revId: RevId;
  seq: number;
  hash: Sha256;
  reportedAt: Ts;
}
export interface Deployment extends Audit {
  deploymentId: Id;
  /** §21 #106 the stable entity slug (GitHub mirror branches deployment/<slug>/…): like Template.slug (unique among deployments, deleted ones too), immutable. */
  slug: Slug;
  label: string;
  image: Icon; //                     logo: "upload:<iconId>", "lucide:<name>", legacy https URL, or null
  tagSlugs: Slug[]; //                ≤ 20
  managerAccountUids: Uid[];
  /**
   * §21 #94: the template this deployment was cloned from (or linked to in Template sync); null = none. The deployment is
   * DETACHED: nothing reads the live template for its config (validation, diff, publish, files, staging); template edits /
   * deletion never change it. Only the manual Template sync (3-way: baseSnapshot ↔ the template now ↔ the draft) moves
   * template changes into the draft.
   */
  srcTemplate: SrcTemplate | null;
  draft: Config; //                    §21 #105: a COPY of the head commit config, written only by commits (the name is kept for storage compat; there is no working copy)
  draftVersion: number; //            +1 on every commit that changes the config
  head: HeadRef | null; //            last head reported by an agent
  accessKeyHash: string; //           argon2id PHC string; never returned by the API (agents get it in the cloud view)
  /** §21 #84: the plaintext key ("XXXX-…", grouped), stored like secrets; returned ONLY by create / rotate / revealAccessKey;
   *  null = deployment created before #84 (not stored → "Regenerate to view"). Never in DeploymentView or the cloud view. */
  accessKey: string | null;
  accessKeyVersion: number; //        1 at create, +1 per rotate
  accessKeyRotatedAt: Ts;
  accessKeyRotatedBy: Uid;
  /** monotonically increasing counter; +1 on every change that agents must see (cloud view). The poll cursor. */
  agentEpoch: number;
  cloudActivitySeq: number; //        last seq of the "_cloud" activity log of this deployment
  deleted: boolean; //                soft delete flag (deleted deployments vanish from lists; agents see deleted:true)
  /** §21 #90 the current / last cloud "Upgrade all" run (absent = never) */
  agentUpgradeRun?: AgentUpgradeRun | null;
  /** §21 #104 branch state: head commit (what the sites should run), origin, counts. `draft` = a copy of the head commit config (§21 #105); `head`
   *  above = the LEGACY head reported by agents < 0.14 (compat proposals, §21 #104 j). Absent until migrate-i. */
  branch?: import('./branch.js').BranchState;
}
export type DeploymentView = Omit<Deployment, 'draft' | 'accessKeyHash' | 'accessKey' | 'srcTemplate'> & {
  draft: ConfigView;
  srcTemplate: SrcTemplateView | null;
};
/** §21 #94 one template change the user did not take in a Template sync (kept as "skipped" until the template changes it again). */
export interface TemplateSyncSkip {
  key: string; //            TemplateSyncChange.key
  theirsHash: string; //     templateSyncHash of the template's value when skipped; a different value = a new change again
  templateVersion: number; // the template version it was skipped at
  skippedAt: Ts;
  skippedBy: Uid;
}
/** §21 #94 Deployment.srcTemplate (stored; the snapshots are internal JSON strings in Firestore). */
export interface SrcTemplate {
  templateId: Id;
  templateVersion: number; // the template version cloned at create (or linked at by Template sync "Change template…")
  snapshot: Template; //      the template as copied at templateVersion (secret values null)
  copiedAt: Ts;
  /** the template version the draft was last synced from (= templateVersion until the first sync) */
  baseVersion: number;
  /** the template's config at baseVersion = the 3-way merge base (self-contained: templates keep no version history) */
  baseSnapshot: Config;
  lastSyncedAt: Ts | null;
  lastSyncedBy: Uid | null;
  skipped: TemplateSyncSkip[];
}
export interface SrcTemplateView {
  templateId: Id;
  templateVersion: number;
  copiedAt: Ts;
  label: string; // the template label at copy / last sync
  baseVersion: number;
  lastSyncedAt: Ts | null;
  lastSyncedBy: Uid | null;
  skippedCount: number;
}
export interface DeploymentSummary {
  deploymentId: Id;
  slug: Slug;
  label: string;
  image: string | null;
  tagSlugs: Slug[];
  siteSlugs: Slug[];
  sitesOnline: number;
  sitesTotal: number;
  head: { revId: RevId; seq: number } | null;
  pending: { revId: RevId; seq: number; status: ProposalStatus } | null;
  updatedAt: Ts;
  /** §21 #104 */
  branch?: import('./branch.js').BranchState | null;
}

/** Item copied into Revision.catalog so that revisions are self-contained (offline sites, rollbacks). */
export interface RevisionCatalog {
  services: Record<Slug, { serviceSlug: Slug; label: string; icon: Icon }>; // icon: "upload:…" is embedded as a data: URL
  /** key = `${serviceSlug}:${version}` */
  images: Record<string, ImageSpec>;
  /** only the pages referenced by the config */
  notFoundPages: Record<Id, { notFoundPageId: Id; label: string; htmlCode: string }>;
}
export type RevisionOrigin = 'cloud' | 'agent';
export interface Revision {
  revId: RevId;
  seq: number; //               parent.seq + 1; first = 1
  parentRevId: RevId | null;
  origin: RevisionOrigin;
  actor: string; //             uid (cloud) or email (agent)
  createdAt: Ts;
  config: Config; //            plain secret values (agents need them); masked in every human read
  hash: Sha256; //              sha256(JCS(config))
  catalog: RevisionCatalog;
  catalogHash: Sha256; //       sha256(JCS(catalog))
  summary: string; //           1–200
  kind: 'change' | 'rollback';
  rollbackOf: { snapshotId: Ulid; seq: number } | null;
}
export type RevisionView = Omit<Revision, 'config'> & { config: ConfigView };
export type RevisionSummary = Pick<Revision, 'revId' | 'seq' | 'parentRevId' | 'origin' | 'actor' | 'createdAt' | 'hash' | 'summary' | 'kind'>;

export type ProposalStatus =
  | 'published'
  | 'staging'
  | 'ready'
  | 'approved'
  | 'applying'
  | 'applied'
  | 'failed-rolled-back'
  | 'stale'
  | 'superseded';
export const PROPOSAL_TERMINAL: readonly ProposalStatus[] = ['applied', 'failed-rolled-back', 'stale', 'superseded'];
export type StagingState = 'pending' | 'downloading' | 'ready' | 'failed';
export interface SiteStaging {
  staging: StagingState;
  progress: number; // 0..1
  error: string | null;
  updatedAt: Ts | null;
}
export interface Proposal {
  revId: RevId;
  parentRevId: RevId | null;
  seq: number;
  summary: string;
  origin: RevisionOrigin;
  kind: 'change' | 'rollback';
  createdAt: Ts;
  createdBy: string; // uid or email
  status: ProposalStatus;
  /** every site of (parent config ∪ proposed config); initialised to pending */
  sites: Record<Slug, SiteStaging>;
  approvedBy: string | null; // declared actor email
  approvedAt: Ts | null;
  /** §21 #95 agent-local: the approval wait was fast-forwarded (absent / null = waited at ×1) */
  approvedFastForward?: FastForwardUse | null;
  updateId: Ulid | null;
  result: { ok: boolean; message: string; snapshotId: Ulid | null } | null;
  supersededBy: RevId | null;
  updatedAt: Ts;
}
export type ProposalMeta = Pick<Proposal, 'revId' | 'parentRevId' | 'seq' | 'summary' | 'status' | 'origin' | 'kind' | 'createdAt' | 'createdBy'>;

export interface PeerInfo {
  siteSlug: Slug;
  publicKey: string; //       Ed25519 raw 32-byte public key, base64 (std, padded)
  advertiseHost: string; //   address other agents use for the mesh (agent.json advertiseHost or detected IP)
  agentPort: number; //       the agentPort the agent listens on now
  enrolledAt: Ts;
}

export interface CertStatus {
  siteSlug: Slug;
  hostname: string;
  mode: EdgeTls['mode'];
  challenge: string | null;
  issuer: string | null;
  notBefore: Ts | null;
  notAfter: Ts | null;
  lastRenewalAt: Ts | null;
  lastError: string | null;
  nextRenewalAt: Ts | null;
}
export interface EdgeReport {
  revSeq: number | null;
  reloadedAt: Ts | null;
  lastReloadError: string | null;
  boundPorts: number[];
  bindErrors: {
    port: number;
    purpose: 'edge-http' | 'edge-https' | 'gui-alias' | 'agent';
    error: string;
    /** §21 #86: name of the process holding the port (Linux /proc, agent runs as root); null = unknown / old agent */
    owner?: string | null;
  }[];
  certs: CertStatus[];
  /** §21 #86: true = the built-in default edge runs (no head yet, or this site is not in the head config): host "*",
   *  no TLS, /_opm/* GUI, / → 302 /_opm/app, everything else → "no configuration applied yet". Absent on old agents. */
  defaultEdge?: boolean;
}
/** §21 #86: one host port on which the agent GUI (/_opm/) answers right now (heartbeat; the cloud Setup page lists URLs). */
export interface GuiPort {
  port: number;
  scheme: 'http' | 'https';
  kind: 'edge' | 'gui-alias' | 'agent-port';
}
export interface HeartbeatStaging {
  revId: RevId;
  state: StagingState;
  progress: number;
}
export interface AgentDoc {
  siteSlug: Slug;
  publicKey: string;
  enrolledAt: Ts;
  agentVersion: string;
  lastSeenAt: Ts;
  online: boolean; //        computed on read: now - lastSeenAt < 90 s
  ip: string;
  hostname: string;
  advertiseHost: string;
  agentPort: number;
  os: string; //            "linux" | "darwin"
  arch: string; //          "amd64" | "arm64"
  relayVia: Slug | null; // set when the last report came through POST /v1/agent/relay
  headRevId: RevId | null;
  staging: HeartbeatStaging | null;
  keyVersion: number; //    accessKeyVersion this site has
  ackSeq: number; //        highest contiguous activity seq stored in the cloud
  metrics: HostMetricsSummary | null;
  edge: EdgeReport | null;
  peersSeen: { siteSlug: Slug; lastSeenAt: Ts }[];
  /** non-loopback IPs of the host (IPv4 first) + the hostname; [] on old agents */
  detectedAddresses: string[];
  /** §21 #86 (absent on agents < 0.8): the host ports the GUI answers on, from the last heartbeat */
  guiPorts?: GuiPort[];
  /** §21 #90: the queued cloud upgrade request of this site (null / absent = none). Shown in CloudView.agentUpgrades. */
  upgradeRequest?: AgentUpgradeRequest | null;
  /** §21 #90: the last upgrade progress the agent reported in a heartbeat (kept after it finished) */
  upgrade?: AgentUpgradeProgress | null;
  /** §21 #92: agent clock − server receive time from the last heartbeat with `agentTime` (null / absent = not measured:
   *  agents before 0.11 do not send it). lastSeenAt is ALWAYS the server receive time, never the agent's clock. */
  clockSkew?: AgentClockSkew | null;
}

// ───────────────────────────────────────────────────────────── §21 #90 agent self-upgrade (GUI + cloud)
/** First agent version that upgrades itself from the agent GUI / the cloud console. Older agents: rerun the install
 *  command (the upgrade-only script, §19.1) once to reach a version ≥ this. */
export const GUI_UPGRADE_MIN_VERSION = '0.10.0';
/** A new agent must report healthy within this many seconds after the restart, else it is rolled back to opm-agent.prev. */
export const AGENT_UPGRADE_HEALTH_SEC = 60;
/** A cloud upgrade request that no agent picked up expires after this many seconds. */
export const AGENT_UPGRADE_REQUEST_TTL_SEC = 24 * 3600;
/** The pinned agent release (deps/manifest.json agent; else OPM_AGENT_VERSION / agent/latest). notes = optional release notes (plain text / light markdown, ≤ 8000 chars). */
export interface AgentReleaseInfo {
  version: string;
  notes: string | null;
}
export type AgentUpgradePhase = 'queued' | 'download' | 'verify' | 'swap' | 'restart' | 'health' | 'done' | 'failed' | 'rolled-back';
export type AgentUpgradeVia = 'gui' | 'cloud' | 'cli' | 'mesh';
/** One upgrade attempt on one site (agent: <agentHome>/upgrade/state.json, survives the restart; heartbeat `upgrade`). */
export interface AgentUpgradeProgress {
  upgradeId: Ulid;
  /** the cloud request this attempt serves (via cloud) */
  requestId: string | null;
  /** the upgrade-all run (cloud run id or agent mesh run id) */
  runId: string | null;
  via: AgentUpgradeVia;
  siteSlug: Slug;
  from: string;
  to: string;
  phase: AgentUpgradePhase;
  /** download fraction 0..1 while phase = download; null otherwise */
  progress: number | null;
  bytesDone: number;
  bytesTotal: number | null;
  /** the phase that failed when phase = failed | rolled-back (rolled-back → usually health); absent / null otherwise */
  failedPhase?: AgentUpgradePhase | null;
  /** filled in phase verify (and kept) */
  verification: ReleaseVerification | null;
  error: string | null;
  actor: string | null; // email
  startedAt: Ts;
  updatedAt: Ts;
  finishedAt: Ts | null;
}
/** Result of checking a release (agent: opm-agent verify-release parity). */
export interface ReleaseVerification {
  version: string;
  sha256: string; //            the pinned sha256 (hex)
  signatureOk: boolean; //      Ed25519 signature of the sha256 hex by the release key
  publicKeySource: 'agent.json' | 'cloud' | 'flag';
  /** "SHA256:" + base64 (no padding) of sha256(raw public key) — the same style as ssh fingerprints */
  publicKeyFingerprint: string;
  /** downloaded file sha256 = pinned sha256; null = not downloaded yet */
  sha256Ok: boolean | null;
  /** the downloaded binary answered `<new> version` with the expected version; null = not run yet */
  selfTestOk: boolean | null;
  error: string | null;
  checkedAt: Ts;
}
/** A person asked the cloud to upgrade one site (never automatic). agents/{site}.upgradeRequest; CloudView.agentUpgrades. */
export interface AgentUpgradeRequest {
  requestId: string; //   ULID
  siteSlug: Slug;
  version: string; //     the pinned version at request time; the agent refuses (upgrade-failed) when the pin moved
  requestedBy: string; // email (the agent uses it as the actor of its activities)
  requestedByUid: Uid;
  requestedAt: Ts;
  expiresAt: Ts; //       requestedAt + AGENT_UPGRADE_REQUEST_TTL_SEC
  runId: string | null;
}
export type AgentUpgradeRunSiteStatus = 'pending' | 'requested' | 'running' | 'done' | 'failed' | 'skipped' | 'cancelled';
/** "Upgrade all": one site at a time, stop on the first failure (cloud: deployments/{id}.agentUpgradeRun; agent mesh:
 *  the coordinating agent's <agentHome>/upgrade/run.json). */
export interface AgentUpgradeRun {
  runId: string;
  origin: 'cloud' | 'agent';
  /** agent origin: the coordinating site (always upgraded LAST, because it restarts) */
  coordinator: Slug | null;
  version: string;
  requestedBy: string;
  requestedAt: Ts;
  status: 'running' | 'done' | 'failed' | 'cancelled';
  sites: { siteSlug: Slug; status: AgentUpgradeRunSiteStatus; from: string | null; requestId: string | null; error: string | null; at: Ts | null }[];
  finishedAt: Ts | null;
}
/** Compare agent versions "MAJOR.MINOR.PATCH[-BUILD]" (BUILD = the MMDDHHMM stamp of make dist): core numerically, then
 *  BUILD (absent = 0; numeric when both are digits, else string order). Unparsable versions sort first. */
export function compareAgentVersions(a: string, b: string): number {
  const p = (v: string): [number, number, number, string] | null => {
    const m = /^v?(\d+)\.(\d+)\.(\d+)(?:[-+](.+))?$/.exec((v ?? '').trim());
    return m ? [Number(m[1]), Number(m[2]), Number(m[3]), m[4] ?? ''] : null;
  };
  const x = p(a);
  const y = p(b);
  if (!x || !y) return x ? 1 : y ? -1 : 0;
  for (let i = 0; i < 3; i++) if (x[i] !== y[i]) return (x[i] as number) < (y[i] as number) ? -1 : 1;
  const [s, t] = [x[3], y[3]];
  if (s === t) return 0;
  if (s === '') return -1;
  if (t === '') return 1;
  if (/^\d+$/.test(s) && /^\d+$/.test(t)) return Number(s) < Number(t) ? -1 : Number(s) > Number(t) ? 1 : 0;
  return s < t ? -1 : 1;
}
/** true when the agent can upgrade itself from the GUI / the cloud (core version ≥ GUI_UPGRADE_MIN_VERSION). */
export function supportsGuiUpgrade(agentVersion: string | null | undefined): boolean {
  if (!agentVersion) return false;
  const core = /^v?(\d+\.\d+\.\d+)/.exec(agentVersion.trim());
  return !!core && compareAgentVersions(core[1], GUI_UPGRADE_MIN_VERSION) >= 0;
}
/** true when `latest` is newer than `installed` (an unknown side → false). */
export function agentUpgradeAvailable(installed: string | null | undefined, latest: string | null | undefined): boolean {
  if (!installed || !latest) return false;
  return compareAgentVersions(installed, latest) < 0;
}
export interface HostMetricsSummary {
  cpu: number; //     0..1
  memPct: number; //  0..1
  diskPct: number; // 0..1
  /** §21 #112 the agent process itself (agents ≥ 0.17.1; absent on older ones) */
  agentRssBytes?: number;
  agentCpuPct?: number; // of one core, since the previous heartbeat
  agentWarnings?: number; // how many self-warnings (disk, store, cloud key, dead letters, stuck loops)
}
/** §21 #92 install token kinds:
 *  - `install` (default; absent on older docs): one fresh-site enrollment within 24 h; after it enrolled the site it stays
 *    valid for upgrade / reinstall / downgrade of THAT enrollment (upgrade-only script, §21 #86), never for a second enroll.
 *  - `maintenance`: minted by "Regenerate" of an enrolled site's upgrade command; bound to the enrollment
 *    (`boundEnrolledAt` = agent.enrolledAt); can only fetch the upgrade-only script / binary / deps of that site, never
 *    enrolls; no expiry — ends when regenerated again (revokedAt) or when the enrollment is revoked. */
export type InstallTokenKind = 'install' | 'maintenance';
export const INSTALL_TOKEN_KINDS = ['install', 'maintenance'] as const satisfies readonly InstallTokenKind[];
export interface InstallToken {
  tokenHash: string; // doc id = sha256 hex of the token string (without "sha256:" prefix)
  siteSlug: Slug;
  /** §21 #92 absent = 'install' (docs before #92) */
  kind?: InstallTokenKind;
  /** §21 #84 / #92: the plaintext "opi_…" token (revealInstallCommand / revealUpgradeCommand). Kept while the token can be
   *  used: an install token while active AND after it enrolled the site (the enrollment's upgrade command, #92); a
   *  maintenance token until it is revoked. Set to null on revoke, on expiry of an unused install token (daily job) and
   *  when the enrollment it belongs to is revoked. null on tokens created before #84 and on install tokens used before #92
   *  ("not stored — Regenerate"). Never in InstallTokenView. */
  token: string | null;
  /** install: createdAt + 24 h (enroll deadline; no expiry for the upgrade use after enrolling). maintenance: null (never). */
  expiresAt: Ts | null;
  /** §21 #92 maintenance only: agent.enrolledAt of the enrollment this token belongs to */
  boundEnrolledAt?: Ts | null;
  usedAt: Ts | null;
  revokedAt: Ts | null;
  createdAt: Ts;
  createdBy: Uid;
}

// ───────────────────────────────────────────────────────────── activities
export type ActivityOrigin = 'agent' | 'cloud' | 'system';
export interface Activity {
  activityId: string; // "<siteSlug>:<seq>"; cloud-origin log uses siteSlug "_cloud"
  deploymentId: Id;
  siteSlug: string; //   Slug or "_cloud"
  seq: number; //        1, 2, 3 … per log, no gaps
  ts: Ts;
  actor: string | null; // email (agent), uid (cloud), null = system
  origin: ActivityOrigin;
  type: string; //       ^[a-z]+(-[a-z]+)*(\.[a-z-]+)*$ — see ACTIVITY_TYPES
  summary: string; //    1–300
  data: Record<string, unknown>; // ≤ 16 KB canonical JSON, never secret values
  prevHash: Sha256 | null; // null only for seq 1
  hash: Sha256; //       see canonical.ts activityHashInput()
  uploadedVia: 'pipe' | 'manual' | null; // set by the cloud
  uploadedAt: Ts | null; //                 set by the cloud
}
export const ACTIVITY_TYPES = [
  'install.enrolled',
  'agent.started',
  'agent.port-bind-failed',
  'login.succeeded',
  'login.failed',
  'logout',
  'actor.declared',
  'access-key.rotated',
  'access-key.received',
  'access-key.revealed', //  §21 #84 (cloud: revealAccessKey)
  'proposal.published',
  'proposal.created',
  'proposal.staged',
  'proposal.stale',
  'proposal.superseded',
  'proposal.approved',
  'update.started',
  'update.snapshot',
  'update.applied',
  'update.failed',
  'update.rolled-back',
  'update.abandoned',
  'update.terminated',
  'update.recovered',
  'config.local-change',
  'snapshot.created',
  'snapshot.downloaded',
  'snapshot.imported',
  'snapshot.deleted',
  'snapshot.rolled-back',
  'volume.seeded',
  'volume.reset-to-seed',
  'transfer.sent',
  'transfer.received',
  'transfer.saved',
  'file.published', //      §21 #123 data { fileId, fileName, sizeBytes, origin }
  'file.fetched', //        a copy arrived (data { fileId, where: 'cloud' | <site>, from })
  'file.downloaded', //     to a browser (data { fileId, where: 'cloud' | <site> })
  'file.saved', //          written to a server path (data { fileId, path })
  'file.copy-dropped', //   a site deleted its copy (data { fileId })
  'file.unpublished', //    data { fileId, fileName }
  'secret.revealed',
  'secret.regenerated', //   §21 #84 (cloud: regenerateSecret; data { secretSlug, draftVersion })
  'secret.fetched',
  'internet.tunnel.started',
  'internet.tunnel.plaintext',
  'internet.tunnel.stopped',
  'internet.tunnel.revoked',
  'internet.tunnel.expired',
  'debug.downloaded',
  'edge.reloaded',
  'edge.reload-failed',
  'cert.issued',
  'cert.renewed',
  'cert.failed',
  'cert.local-ca-downloaded',
  'site.unreachable',
  'site.reachable',
  'deployment.created',
  'deployment.updated',
  'install-token.created',
  'install-token.revoked',
  'install-token.revealed', // §21 #84 (cloud: revealInstallCommand)
  'install-command.revealed', //    §21 #92 (cloud: revealUpgradeCommand) data { siteSlug, tokenHash, kind }
  'install-command.regenerated', // §21 #92 (cloud: regenerateUpgradeCommand) data { siteSlug, tokenHash, revoked: tokenHash[] }
  'agent.revoked',
  'draft.reset-to-head',
  'draft.updated', //       a draft section / site saved in the cloud (data.paths = changed paths, secrets redacted)
  'transfer.deleted', //    a transfer deleted in the cloud
  'activities.uploaded', // a .opmlog uploaded by hand (counts)
  'deployment.deleted', //  soft delete
  // §21 #89 agent operator toolbox (agent/internal/tools)
  'terminal.opened', //     data { terminalId, kind: container|host, target, command, via?, forwardedTo? }
  'terminal.closed', //     data { terminalId, kind, target, durationSec, exitCode, reason, bytesOut }
  'container.restarted', // data { containerSlug, ref, reason? }
  'container.stopped', //   data { containerSlug, ref, reason?, expectedRunning } (manuallyStopped until start / next update)
  'container.started', //   data { containerSlug, ref, reason? }
  'tool.netcheck', //       data { from, check, target, ok, durationMs, summary }
  'volume.downloaded', //   data { volumeSlug, path, kind: file|archive }
  'docker.images-pruned', // data { deleted, reclaimedBytes }
  // §21 #90 agent self-management (agent GUI Agent page, cloud "Upgrade agent", opm-agent upgrade)
  'agent.upgrade-started', //  data { upgradeId, from, to, via: gui|cloud|cli|mesh, requestId?, runId? }
  'agent.upgraded', //         data { upgradeId, from, to, durationSec, via } (written by the NEW agent after it came up healthy)
  'agent.upgrade-failed', //   data { upgradeId, from, to, phase, error, via }
  'agent.rolled-back', //      data { upgradeId, from (failed target), to (restored version), reason, by: watcher|guard }
  'agent.restarted', //        data { reason, via: systemd|exec } (restart from the GUI; upgrades write agent.upgrade-* instead)
  'agent.config-changed', //   data { changed: string[], before, after } (agent.json safe settings; never secrets)
  'agent.support-bundle-downloaded', // data { sizeBytes, sections: string[] }
  'agent.upgrade-requested', // cloud: data { requestId, siteSlug, version, runId? } (+ audit deployment.agent-upgrade-requested)
  'agent.upgrade-cancelled', // cloud: data { requestId?, runId?, siteSlug? }
  // §21 #118 remote agent GUI
  'remote-gui.opened', //         cloud: data { siteSlug, sessionId } (a person opened the site's agent GUI through the cloud)
  'remote-gui.session', //        agent: data { sessionId, cloudSessionId, email } (the agent session made for that person)
  'agent.remote-gui-changed', //  agent: data { enabled }
  // §21 #125 port tunnels
  'tunnel.created', //   cloud: data { tunnelId, siteSlug, target, publicPort }
  'tunnel.deleted', //   cloud: data { tunnelId, siteSlug, publicPort }
  'tunnel.enabled', //   agent: data { tunnelId, target, publicPort }
  'tunnel.disabled', //  agent: data { tunnelId, target, publicPort }
  // §21 #119 moving the agent home
  'agent.home-move-started', //   agent: data { from, to, mode, sizeBytes, stopped }
  'agent.home-moved', //          agent: data { from, to, mode, oldCopy, sizeBytes, files }
  'agent.home-move-failed', //    agent: data { from, to, error }
  'agent.home-old-deleted', //    agent: data { path, sizeBytes }
  // §21 #94 Template sync (cloud)
  'draft.template-synced', //        data { templateId, fromVersion, toVersion, relinked, previousTemplateId?, taken, skipped, conflicts, draftVersion, keys }
  'deployment.template-unlinked', // data { templateId, baseVersion }
  // §21 #96 volume manager (agent GUI Tools → Volumes)
  'volume.emptied', //            data { volumeSlug, siteSlug, opId, snapshotId, stopped[], bytesBefore, durationMs, fastForward? }
  'volume.copied', //             data { volumeSlug, siteSlug, opId, target { kind, path, volumeSlug }, bytes, files, stopped[], preserve, durationMs, copyId }
  'volume.restored', //           data { volumeSlug, siteSlug, opId, source { kind, ref, path }, snapshotId, bytes, files, stopped[], durationMs, fastForward? }
  'volume.op-failed', //          data { volumeSlug, siteSlug, opId, kind, step, error, stopped[], durationMs }
  'volume.copy-deleted', //       data { copyId, volumeSlug, path, sizeBytes }
  'volume.action-forwarded', //   the forwarding site of a mesh volume action: data { siteSlug, forwardedTo, method, action, status }
  'volume.file-written', //       data { volumeSlug, path, sizeBytes, created }
  'volume.file-uploaded', //      data { volumeSlug, path, sizeBytes, overwritten }
  'volume.file-folder-created', // data { volumeSlug, path }
  'volume.file-moved', //         data { volumeSlug, from, to, overwritten }
  'volume.file-deleted', //       data { volumeSlug, paths[], entries }
  // §21 #101 volume content + persist-to-cloud (agent: snapshot / apply; cloud `_cloud`: edits, sends)
  'volume.placeholder-filled', //       agent: data { volumeSlug, path, sha256, size, formerFileSlug } — a 0-byte former File mountpoint got its content (migration)
  'volume-content.snapshot', //         agent: data { volumeSlug, snapshotId, trigger, treeHash, fileCount, totalBytes, excludedCount, skippedCount, requestId?, pushId? }
  'volume-content.snapshot-uploaded', // agent: data { volumeSlug, snapshotId, versionId, seq }
  'volume-content.snapshot-failed', //  agent: data { volumeSlug, trigger, error }
  'volume-content.conflict', //         agent: data { volumeSlug, pushId, versionId, conflicts, liveTreeHash, baseTreeHash }
  'volume-content.applied', //          agent: data { volumeSlug, pushId, versionId, resolution, written, deleted, keptLive, snapshotId, restarted[], fastForward? }
  'volume-content.kept-live', //        agent: data { volumeSlug, pushId, versionId }
  'volume-content.apply-failed', //     agent: data { volumeSlug, pushId, versionId, step, error }
  'volume-content.version-created', //  cloud: data { siteSlug, volumeSlug, versionId, seq, origin, treeHash, fileCount, totalBytes, baseVersionId, added, removed, modified }
  'volume-content.snapshot-requested', // cloud: data { siteSlug, volumeSlug, requestId }
  'volume-content.sent', //             cloud: data { siteSlug, volumeSlug, pushId, versionId, baseVersionId, restart, supersededPushId }
  'volume-content.push-cancelled', //   cloud: data { siteSlug, volumeSlug, pushId, versionId }
  // §21 #97 dev tools (agent GUI Dev tools; data DevToolQueryActivity for devtools.query — never result data)
  'devtools.query', //               data DevToolQueryActivity { connectionId, connectionName, kind, target, op, statement (redacted, ≤ 4 KiB), durationMs, rows, status, ok, error, … }
  'devtools.query-cancelled', //     data { connectionId, connectionName, kind, queryId, wasRunning }
  'devtools.connection-created', //  data { connectionId, name, kind, target, readOnly, duplicatedFrom? }
  'devtools.connection-updated', //  data { connectionId, name, kind, target, readOnly, changed[], credentialChanged }
  'devtools.connection-deleted', //  data { connectionId, name, kind }
  'devtools.connection-tested', //   data { connectionId|null, name, kind, target, ok, latencyMs, error }
  'devtools.credential-revealed', // data { connectionId|environmentId, name, field }
  'devtools.mqtt-published', //      data { connectionId, connectionName, topic, qos, retain, bytes, ok, error, durationMs }
  'devtools.mqtt-subscribed', //     data { connectionId, connectionName, sessionId, filters[] }
  'devtools.mqtt-unsubscribed', //   data { connectionId, connectionName, sessionId, filters[], messages, durationSec, reason }
  'devtools.action-forwarded', //    the forwarding site of a mesh dev-tools write: data { siteSlug, forwardedTo, method, action, connectionId, status }
  // §21 #104 branches + commits (data always { branch, commitId, seq, message (first line), source?, picks, skipped })
  'branch.created', //        cloud: a template / deployment created from another branch (data.from)
  'branch.committed', //      cloud (deployment) / agent: working copy committed
  'branch.merged', //         every source change taken
  'branch.cherry-picked', //  some source changes taken
  'branch.reverted',
  'branch.reset',
  'branch.merge-failed', //   agent: the apply of a merge failed and was rolled back (no commit) — data { step, siteSlug, error, failures? }
  'working.discarded', //     cloud: working copy reset to the head; agent: GUI edits dropped
  'drift.detected', //        agent: data { items, notes, keys[≤50] } (only when the set changes)
  'drift.committed', //       agent: data { commitId, keys }
  'drift.discarded', //       agent: data { keys, updateId }
  'drift.accepted', //        agent: data { fingerprints, commitId }
  'agent-branch.pushed', //   cloud: data { siteSlug, commitIds[≤20], head }
] as const;
export type ActivityType = (typeof ACTIVITY_TYPES)[number];

// ───────────────────────────────────────────────────────────── transfers + uploads
export type TransferDirection = 'to-site' | 'to-cloud';
export type TransferStatus = 'uploading' | 'ready' | 'saved-on-server' | 'downloaded' | 'expired';
export interface Transfer {
  transferId: Id;
  deploymentId: Id;
  siteSlug: Slug;
  direction: TransferDirection;
  fileName: string; //   1–255, no "/" "\\" NUL
  sizeBytes: number; //  ≤ 2147483648
  sha256: Sha256;
  storagePath: string; // "transfers/<deploymentId>/<transferId>"
  uploadId: Id | null;
  createdAt: Ts;
  createdBy: string; //  uid (cloud) or email (agent)
  note: string; //       0–500
  expiresAt: Ts | null; // null: transfers never expire (only Delete removes one); a date on rows written before that is ignored
  status: TransferStatus;
  savedPath: string | null;
  error: string | null;
  /** §21 #100 (cloud, to-site): when the site's agent had the file stored on the site (agents ≥ 0.11.2 report `fetched`);
   *  null / absent = not fetched yet (or an older agent that does not report it) */
  fetchedAt?: Ts | null;
  /** §21 #100 (agent API, to-cloud): the server path the file was sent from; null / absent = a browser upload */
  sourcePath?: string | null;
  /** §21 #100 (agent API only, never stored in the cloud): store-and-forward progress on this site */
  delivery?: TransferDelivery;
}
/**
 * §21 #100 store-and-forward state on the agent:
 * - to-cloud: `queued` (stored on the site, waiting for a cloud route / retry) → `uploading` (parts going up) → `stored` (the cloud has it)
 * - to-site:  `pending` (stored in the cloud, waiting to be fetched) → `downloading` → `on-site` (stored on this site)
 */
export type TransferPhase = 'queued' | 'uploading' | 'stored' | 'pending' | 'downloading' | 'on-site';
export interface TransferDelivery {
  phase: TransferPhase;
  bytesDone: number; //            bytes the receiving side has (to-cloud: parts the cloud acknowledged; to-site: bytes on disk)
  attempts: number; //             failed attempts since the last success (0 = none)
  lastError: string | null;
  lastAttemptAt: Ts | null;
  nextAttemptAt: Ts | null; //     null = now / as soon as a route exists
}
export type TransferMeta = Pick<Transfer, 'transferId' | 'siteSlug' | 'direction' | 'fileName' | 'sizeBytes' | 'sha256' | 'status' | 'createdAt' | 'createdBy' | 'note' | 'expiresAt'>;

export type UploadPurpose = 'blob' | 'transfer' | 'seed-archive' | 'file'; // file: §21 #123 shared files (Upload.transferId = the fileId) // seed-archive (round E): .tar.gz volume starter content ≤ SEED_ARCHIVE_MAX
export interface Upload {
  uploadId: Id;
  purpose: UploadPurpose;
  deploymentId: Id | null;
  transferId: Id | null;
  sizeBytes: number;
  sha256: Sha256;
  partSize: number; //   8388608 (8 MiB); last part may be smaller
  partCount: number; //  ceil(sizeBytes / partSize), ≥ 1
  partsReceived: number[];
  status: 'open' | 'complete' | 'failed';
  error: string | null;
  createdAt: Ts;
  createdBy: string;
  expiresAt: Ts; //      createdAt + 24 h
}
export const UPLOAD_PART_SIZE = 8 * 1024 * 1024;
export const SMALL_BLOB_MAX = 24 * 1024 * 1024; // POST /v1/blobs raw body limit
/** GET /v1/blobs/{sha256} for humans: bodies up to this size stream with 200 (no redirect); larger → 302 signed URL */
export const BLOB_STREAM_MAX = 8 * 1024 * 1024;
export const TRANSFER_MAX = 2 * 1024 * 1024 * 1024;

export interface UptimeDay {
  siteSlug: Slug;
  date: string; //     yyyymmdd UTC
  beats: number[]; //  24 items: heartbeats received per hour (expected 120)
  hours: number[]; //  24 items: min(1, beats/120)
  updatedAt: Ts;
}

// ───────────────────────────────────────────────────────────── agent: cloud view, mesh, distribution
export interface CloudView {
  fetchedAt: Ts; //               cloud SERVER time of the poll answer
  source: 'cloud' | `peer:${string}`;
  cursor: string; //              String(deployment.agentEpoch)
  deploymentId: Id;
  deleted: boolean;
  proposals: ProposalMeta[]; //   every proposal whose status is not terminal, plus the 5 newest terminal ones
  transfersToSite: TransferMeta[]; // to-site transfers with status ready (all sites; each agent filters its own)
  /** to-site transfers deleted in the cloud (all sites; each agent drops its own copy); absent on old clouds */
  deletedTransfersToSite?: { transferId: Id; siteSlug: Slug }[];
  draftHead: { revId: RevId; seq: number } | null; // deployment.head as the cloud knows it
  accessKey: { hash: string; version: number };
  peers: PeerInfo[]; //           every enrolled site of the deployment
  /** cloud console origin (env OPM_CONSOLE_URL, else the first https OPM_FRONTEND_ORIGINS entry); absent on old clouds */
  consoleUrl?: string | null;
  /** Deployment.label (the agent GUI shows it); absent on old clouds */
  deploymentLabel?: string | null;
  /** §21 #90 the pinned agent release (version + notes) for the Agent page; absent on old clouds */
  agentRelease?: AgentReleaseInfo | null;
  /** §21 #90 queued, unexpired cloud upgrade requests (each agent runs only its own siteSlug's, once per requestId) */
  agentUpgrades?: AgentUpgradeRequest[];
  /** §21 #101 persist-to-cloud volumes with a PENDING push or an open snapshot request (all sites; each agent filters its own) */
  volumeContent?: import('./volume-content.js').VolumeContentCloudItem[];
  /** §21 #123 shared files: the whole listing (+ tombstones) and the copies the cloud wants from a site; absent on old clouds */
  files?: import('./files.js').SharedFile[];
  fileFetches?: import('./files.js').CloudViewFiles['fileFetches'];
  /** §21 #118 sites with an open remote GUI session (each agent long-polls agentRemoteGuiNext while its site is listed) */
  remoteGui?: import('./remote-gui.js').RemoteGuiCloudItem[];
  /** §21 #125 the port tunnels of the deployment; present only on a cloud that carries tunnels (the agent then polls agentTunnelNext) */
  tunnels?: import('./tunnels.js').PortTunnelCloudItem[];
  /** §21 #104 the deployment branch head (what the sites should run) + the newest commits; absent on old clouds */
  branch?: import('./branch.js').CloudBranchView;
}
export interface HaveList {
  siteSlug: Slug;
  generatedAt: Ts;
  internet: boolean; // reached the cloud in the last 90 s (directly)
  blobs: Sha256[];
}
export type BlobKind = 'layer' | 'config' | 'manifest' | 'file';
export interface FetchRequest {
  sha256: Sha256;
  sizeBytes: number;
  kind: BlobKind;
  ref: { serviceSlug: Slug; version: string } | { fileSlug: Slug; revId: RevId };
}
export interface FetchRequestResponse {
  fetcher: Slug;
  status: 'queued' | 'downloading' | 'have' | 'failed';
  error: string | null;
}

export interface UpdateLock {
  updateId: Ulid;
  kind: 'update' | 'rollback' | 'merge';
  revId: RevId; //          target revision
  coordinator: Slug;
  startedAt: Ts;
  lastContactAt: Ts;
}
/** GET /_opm/peer/ping response. Also used by the GUI for the site tiles. */
export interface PeerPing {
  deploymentId: Id;
  siteSlug: Slug;
  agentVersion: string;
  serverTime: Ts;
  head: { revId: RevId; seq: number; hash: Sha256 } | null;
  lock: UpdateLock | null;
  proposals: { revId: RevId; status: ProposalStatus; staging: SiteStaging }[];
  internet: boolean;
  cloudRoute: 'direct' | 'relay' | 'tunnel' | 'none';
  cloudFetchedAt: Ts | null;
  keyVersion: number;
  agentPort: number;
  guiAliasPort: number | null;
  edge: { running: boolean; httpPort: number | null; httpsPort: number | null; lastError: string | null };
  inConfig: boolean; //       this site is in the head config
  /** §21 #104 agents ≥ 0.14: the site's branch (head, merged from, uncommitted) */
  branch?: import('./branch.js').AgentBranchReport | null;
}

export type UpdateStepName = 'lock' | 'snapshot' | 'apply' | 'verify' | 'commit' | 'rollback' | 'unlock';
export interface UpdatePlan {
  updateId: Ulid;
  /** §21 #104 'merge' = a branch merge / commit-with-apply / discard / revert / reset / fast-forward (per-site targets) */
  kind: 'update' | 'rollback' | 'merge';
  revision: Revision; //        target revision (rollback: the new "rollback to …" revision built by the coordinator)
  fromRevId: RevId | null; //   head before the update
  preSnapshotId: Ulid; //       id of the pre-update snapshot (same on every site)
  targetSnapshotId: Ulid | null; // rollback only
  sites: Slug[]; //             participating sites (head config ∪ target config)
  order: Slug[]; //             apply order (topological by site depends; removed sites first)
  coordinator: Slug;
  approvedBy: string;
  approvedAt: Ts;
  /** §21 #104 per-site targets (0.14 plans): each site moves from ITS head to ITS new commit; `revision` = the
   *  coordinator's own target (or the first site's) for old readers */
  targets?: Record<Slug, { revision: Revision; fromRevId: RevId | null }>;
  /** §21 #104 the commit each site writes at COMMIT (message, kind, source …; config = targets[site].revision.config) */
  commits?: Record<Slug, Omit<import('./branch.js').Commit, 'config' | 'catalog'>>;
}
export interface UpdateStepRequest {
  step: UpdateStepName;
  plan?: UpdatePlan; //         required for "lock"
}
export type ContainerAction = 'keep' | 'recreate' | 'create' | 'remove' | 'stop';
export interface UpdateSiteState {
  updateId: Ulid;
  siteSlug: Slug;
  step: UpdateStepName | null;
  state: 'idle' | 'running' | 'ok' | 'failed';
  error: string | null;
  preSnapshotId: Ulid | null;
  startedAt: Ts | null;
  finishedAt: Ts | null;
  containers: {
    containerSlug: Slug;
    serviceSlug: Slug;
    action: ContainerAction;
    state: 'pending' | 'running' | 'ok' | 'failed';
    error: string | null;
    /** §21 #87: diagnostics captured before the rollback (absent on old agents / containers that did not fail) */
    failure?: ContainerFailure | null;
  }[];
}
/** §21 #87: one readiness / health check of a container while an update waited for it */
export interface HealthCheckRecord {
  at: Ts;
  ok: boolean;
  /** false = inside the image's startPeriodSec (or "ready after 10 s" for type none): it does not count towards retries */
  counted: boolean;
  message: string;
}
/** §21 #87: what OPM saw when a container failed APPLY / VERIFY (captured BEFORE the rollback removes it) */
export interface ContainerFailure {
  containerSlug: Slug;
  dockerName: string;
  /** the stopped failed container, renamed `<dockerName>-failed-<unix>` for `docker logs`; removed after the next
   *  successful update of the site or after 24 h; null = not kept (e.g. it never got created) */
  keptAs: string | null;
  error: string;
  state: { status: string; exitCode: number; oomKilled: boolean; restartCount: number; error: string; startedAt: string; finishedAt: string } | null;
  /** docker inspect port map, e.g. "8080/tcp=0.0.0.0:6007,127.0.0.1:32768" ("none" while Docker restarts it) */
  ports: string;
  /** last ≤ 200 log lines (stdout + stderr, "<ts> <stream> <text>"), deployment secrets redacted */
  logTail: string[];
  healthHistory: HealthCheckRecord[];
  capturedAt: Ts;
  /** §21 #93: a log-pattern hint, e.g. "northwind-backend-admin could not resolve `mqtt` (EAI_AGAIN) — is mqtt
   *  running/healthy? add it to Depends on" (DNS failure naming a container of this site); null / absent = none */
  hint?: string | null;
}
export interface UpdateRun {
  updateId: Ulid;
  kind: 'update' | 'rollback' | 'merge';
  revId: RevId;
  coordinator: Slug;
  approvedBy: string;
  approvedAt: Ts;
  phase: UpdateStepName | 'done' | 'failed';
  result: { ok: boolean; message: string; snapshotId: Ulid | null } | null;
  sites: Record<Slug, UpdateSiteState>;
  startedAt: Ts;
  finishedAt: Ts | null;
  /** §21 #95: the approval / rollback wait was fast-forwarded (absent / null = waited at ×1) */
  fastForward?: FastForwardUse | null;
}

export interface ApproveIntent {
  intentId: Ulid;
  kind: 'update' | 'rollback' | 'volume-reset' | 'volume-empty' | 'volume-restore' | 'volume-content-apply';
  revId: RevId | null; //       update
  snapshotId: Ulid | null; //   rollback
  /** volume-reset (round E): the volume to reset to its starter content (this site) */
  volume?: { siteSlug: Slug; volumeSlug: string } | null;
  actor: string;
  sessionId: Ulid;
  notBefore: Ts; //             createdAt + 60 s (OPM_APPROVE_WAIT_SEC) = the LATEST ready time (all at ×1)
  expiresAt: Ts; //             notBefore + 600 s
  /** §21 #95: the effective wait the agent requires (60, or OPM_APPROVE_WAIT_SEC); absent on agents before 0.10.2 */
  waitSec?: number;
}

// ───────────────────────────────────────────────────────────── §21 #95 hold-to-fast-forward of an intent wait
/**
 * The 60 s intent wait (approve, rollback to a snapshot, reset a volume to its starter content) counts EFFECTIVE seconds:
 * effective = Σ speed × real dt, integrated by the agent on ITS OWN clock. While the person holds "Hold to fast-forward"
 * the GUI sends `intentSpeed {speed}` on every change plus a heartbeat every INTENT_SPEED_HEARTBEAT_MS while speed > 1. A
 * speed applies from the moment the agent received it until the next speed message, or until INTENT_SPEED_DECAY_MS pass
 * without one — then it decays to ×1 server-side (a lost release never keeps it fast). ACCEPT needs effective ≥ waitSec, so
 * the minimum real wait is waitSec / INTENT_MAX_SPEED (15 s for 60 s).
 */
export type IntentSpeed = 1 | 2 | 4;
export const INTENT_SPEEDS: readonly IntentSpeed[] = [1, 2, 4];
export const INTENT_MAX_SPEED: IntentSpeed = 4;
/** GUI heartbeat while speed > 1 */
export const INTENT_SPEED_HEARTBEAT_MS = 500;
/** agent: a speed > 1 without a heartbeat for this long decays to ×1 */
export const INTENT_SPEED_DECAY_MS = 1500;
/** GUI: hold → ×2 at once, ×4 after this much continuous holding */
export const INTENT_SPEED_X4_AFTER_MS = 1500;
/** GUI: release → the shown speed eases back to ×1 over this long (the agent gets ×1 at once) */
export const INTENT_SPEED_EASE_MS = 400;

export interface IntentSpeedRequest {
  speed: IntentSpeed;
}
/** The agent's authoritative view of an intent wait (answer of getIntent / intentSpeed). */
export interface IntentProgress {
  intentId: Ulid;
  kind: ApproveIntent['kind'];
  waitSec: number; //           the effective seconds required
  effectiveSec: number; //      accumulated Σ speed × dt up to serverTime
  remainingSec: number; //      max(0, waitSec − effectiveSec)
  speed: IntentSpeed; //        the speed the agent applies now (1 after a decay)
  /** a speed > 1 decays to ×1 at this time unless a heartbeat arrives (null at ×1) */
  speedUntil: Ts | null;
  maxSpeed: IntentSpeed; //     the highest speed used so far
  ready: boolean; //            effectiveSec ≥ waitSec (ACCEPT allowed)
  createdAt: Ts;
  serverTime: Ts; //            the agent clock when this answer was made
}
/** Recorded in the approve / rollback / volume-reset activity data (and run / proposal) when speed > 1 was used. */
export interface FastForwardUse {
  usedBy: string; //            the actor who held the button (the intent actor)
  maxSpeed: IntentSpeed; //     2 | 4
  realSeconds: number; //       real seconds from the start of the wait until it was complete (1 decimal)
  effectiveSeconds: number; //  the effective seconds counted (= waitSec)
}

export interface GateCheck {
  open: boolean;
  /** (a) parent = head everywhere · (b) every site reachable · (c) staging ready · (d) no update running */
  /** warn (§21 #110): ok, but worth a look — a running update never closes a gate (starting stops it) */
  conditions: { key: 'parent' | 'reachable' | 'staged' | 'idle' | 'status'; ok: boolean; detail: string; warn?: boolean }[];
  sites: Slug[];
}

// ───────────────────────────────────────────────────────────── agent: snapshots, logs, tunnel, actors
export type SnapshotReason = 'pre-update' | 'manual' | 'scheduled' | 'imported' | 'volume-auto'; // §21 #112 volume-auto = a volume's own schedule
export interface SnapshotVolume {
  /** the declared site volume (SiteVolume.volumeSlug); restore is keyed by it. Snapshots written before round D carry `name` (read as volumeSlug). */
  volumeSlug: string;
  /** containerSlugs that mount it at snapshot time (informational; a restore does not depend on them) */
  containers: Slug[];
  sizeBytes: number; // archive size
  sha256: Sha256; //    archive sha256
  path: string; //      relative to the snapshot dir: "sites/<siteSlug>/volumes/<volumeSlug>.tar.zst"
}
export interface SnapshotSite {
  config: Config;
  images: { serviceSlug: Slug; version: string; digest: Sha256; configDigest: Sha256 }[];
  volumes: SnapshotVolume[];
  sizeBytes: number;
}
export interface Snapshot {
  snapshotId: Ulid;
  createdAt: Ts;
  reason: SnapshotReason;
  revId: RevId | null; //  head at snapshot time (null = empty head)
  seq: number; //          0 when the head was empty
  sites: Record<Slug, SnapshotSite>;
  createdBy: string; //    email or "system"
  note: string; //         0–500, the only editable field
  /** revision + catalog at snapshot time (needed to restore offline) */
  revision: Revision | null;
}
export interface SnapshotSummary {
  snapshotId: Ulid;
  createdAt: Ts;
  reason: SnapshotReason;
  revId: RevId | null;
  seq: number;
  createdBy: string;
  note: string;
  sizeBytes: number;
  sites: Slug[]; //        sites that hold their part
  missingSites: Slug[]; // sites of the snapshot config whose part is missing (cannot roll back)
  hasImages: boolean;
}
export interface OpmsnapManifest {
  format: 'opmsnap/1';
  deploymentId: Id;
  snapshot: Snapshot;
  parts: { path: string; sizeBytes: number; sha256: Sha256 }[];
  includeImages: boolean;
  images: { serviceSlug: Slug; version: string; digest: Sha256; configDigest: Sha256; blobs: Sha256[] }[];
  createdAt: Ts;
  createdBy: string;
  signedBy: { siteSlug: Slug; publicKey: string };
}
export interface OpmlogManifest {
  format: 'opmlog/1';
  deploymentId: Id;
  createdAt: Ts;
  createdBy: string;
  signedBy: { siteSlug: Slug; publicKey: string };
  sites: { siteSlug: Slug; fromSeq: number; toSeq: number; count: number }[];
  activities: { path: 'activities.ndjson'; sizeBytes: number; sha256: Sha256 };
  debug: { siteSlug: Slug; path: string; sizeBytes: number; sha256: Sha256 }[];
}

export type TunnelLeaseStatus = 'active' | 'stopped' | 'revoked' | 'expired';
export interface TunnelLease {
  leaseId: Ulid;
  actor: string;
  siteSlug: Slug;
  userAgent: string;
  startedAt: Ts;
  lastPollAt: Ts;
  status: TunnelLeaseStatus;
  reqs: number;
  bytes: number;
  lastError: string | null;
  /** §9.10 every cloud call of this lease is sealed end to end, the tab sees ciphertext. false: an agent without the
   *  feature, or `tunnelPlaintext` on an agent without a pinned release key */
  encrypted: boolean;
}
/** §9.10 GET /v1/agent/tunnel-key: the cloud's X25519 key, certified by the release key */
export interface TunnelKeyResponse {
  v: 1;
  kid: string; //       first 16 hex of the sha256 of the public key
  publicKey: string; // base64 X25519
  origin: string; //    origin of the cloud's public URL, part of the signed string
  sig: string; //       base64url Ed25519 by the release key over `opm-tunnel-key-v1\n<publicKey>\n<origin>\n`
}
/** §9.10 POST /v1/agent/sealed: `ct` = AES-256-GCM of the JSON of a RelayInner. The answer is a framed AEAD stream. */
export interface SealedRequest {
  v: 1;
  kid: string;
  epk: string; // base64 ephemeral X25519 public key, one per request
  ct: string; //  base64
}
export interface TunnelRequest {
  reqId: Ulid;
  method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';
  url: string; //                 absolute https URL (cloud API) — the agent never queues other hosts
  headers: Record<string, string>;
  bodyB64: string | null; //      ≤ 4 MiB decoded
  createdAt: Ts;
  timeoutSec: number; //          5–600, default 60
}
export interface TunnelResult {
  leaseId: Ulid;
  reqId: Ulid;
  status: number; //              0 = network error in the browser
  headers: Record<string, string>;
  bodyB64: string | null;
  chunked: boolean;
  error: string | null;
}

export interface ActorTokenClaims {
  email: string;
  sid: Ulid; //    session id
  dep: Id;
  site: Slug;
  iat: number;
  exp: number; //  = session expiry (unix seconds)
}
export interface ActorRow {
  email: string;
  lastUsedAt: Ts;
  count: number;
}
export interface AgentSession {
  sessionId: Ulid;
  createdAt: Ts;
  expiresAt: Ts; // createdAt + 6 h
  keyVersion: number;
  ip: string;
  userAgent: string;
}

// ───────────────────────────────────────────────────────────── agent: status + logs
export interface ContainerStatus {
  siteSlug: Slug;
  /** managed containers: the containerSlug; unmanaged ones (managed=false): the docker container name */
  containerSlug: Slug;
  /** managed containers: the service slug; unmanaged ones: "" (version "" and image = its docker image) */
  serviceSlug: Slug;
  version: string;
  /** the readable docker reference on the site: `<serviceSlug>:<version>` */
  image: string;
  kind: ImageKind;
  enabled: boolean;
  managed: boolean; //   false = a container without the opm.deployment label (shown as "not managed")
  state: 'running' | 'exited' | 'restarting' | 'created' | 'paused' | 'dead' | 'missing';
  exitCode: number | null;
  health: 'healthy' | 'unhealthy' | 'starting' | 'completed' | 'none';
  ready: boolean; //     "completed" rule
  restarts: number;
  cpuPct: number | null;
  memMb: number | null;
  startedAt: Ts | null;
  ports: { portName: Slug; containerPort: number; hostPort: number | null; protocol: 'tcp' | 'udp' }[];
  specHash: string | null;
  revSeq: number | null;
}
export interface HostMetrics {
  cpuPct: number; //  0..100
  memUsedMb: number;
  memTotalMb: number;
  diskUsedPct: number; // 0..100 of the dataDir filesystem
  diskFreeMb: number;
  loadAvg1: number | null;
}
export interface UptimeCell {
  t: Ts; //           bucket start
  up: number; //      0..1 fraction up (null samples excluded)
  samples: number;
}
export interface SiteStatus {
  siteSlug: Slug;
  reachable: boolean;
  lastSeenAt: Ts | null; // for a peer: last answer
  agentVersion: string | null;
  host: HostMetrics | null;
  containers: ContainerStatus[];
  uptime24h: { subject: string; cells: UptimeCell[] }[]; //  144 cells × 10 min (subject = "site" or containerSlug)
  uptime30d: { subject: string; cells: UptimeCell[] }[]; //  720 cells × 1 h
  certs: CertStatus[];
  edge: EdgeReport | null;
  /** §21 #112 the agent's own problems (disk floor / snapshot budget, store writes, cloud key, dead letters, stuck loops) */
  agentWarnings?: string[];
}
export interface DebugLine {
  ts: Ts;
  level: 'debug' | 'info' | 'warn' | 'error';
  module: string; // cloudlink | mesh | stager | expr | driver | edge | tunnel | health | coordinator | snapshot | pipe | transfer | auth | gui | distrib
  msg: string;
  fields: Record<string, unknown>;
  site?: Slug; //  set when merged from several sites
}

/** /etc/opm/agent.json (written by the install script). */
export interface AgentConfigFile {
  apiBase: string; //          e.g. "https://api.opm.example" — requests go to apiBase + "/v1/…"
  deploymentId: Id;
  siteSlug: Slug;
  dataDir?: string; //         default "/var/lib/opm"
  agentPort?: number; //       listen port before the first head (default 7080)
  guiAliasPort?: number | null; // default 8080 before the first head
  advertiseHost?: string; //   mesh address announced to peers (default: first non-loopback IPv4)
  dockerHost?: string; //      default "unix:///var/run/docker.sock" (or $DOCKER_HOST)
  proxy?: string | null; //    outbound HTTPS proxy
  installTokenFile?: string; // default "/etc/opm/install-token" (deleted after enroll)
  /** §21 #86 HTTP port of the built-in default edge (before the first head / site not in the head): default 80;
   *  null = no default edge (GUI only on guiAliasPort / agentPort). With a head the edge uses SiteConfig.edge.httpPort. */
  edgeHttpPort?: number | null;
  /** release signing public key (base64), written by the install script; `opm-agent upgrade` verifies with it */
  releasePublicKey?: string;
  /** §9.10 only without releasePublicKey: let the browser tunnel carry cloud calls in clear (an activity on every lease
   *  start). Without it such an agent's tunnel calls fail; with a releasePublicKey it has no effect. */
  tunnelPlaintext?: boolean;
  /** cloud console origin for the agent GUI links (fallback when the cloud does not send consoleUrl) */
  consoleUrl?: string;
  /** §21 #88 docker container names: "plain" (default; the docker name = the containerSlug) | "prefixed"
   *  (opm_<deploymentId>_<siteSlug>_<containerSlug>, for several sites on one Docker host) */
  containerNames?: 'plain' | 'prefixed';
  /** §21 #89 operator toolbox switches (defaults true) */
  tools?: { hostShell?: boolean; containerExec?: boolean };
  hostShell?: { user?: string; shell?: string };
}

// ═════════════════════════════════════════════════════════════ API DTOs — cloud API (/v1/*)
export interface ListResponse<T> {
  items: T[];
}
export interface PageResponse<T> {
  items: T[];
  cursor: string | null; // opaque; pass back as ?cursor=
}
export interface Deleted {
  deleted: true;
}
export interface ValidationReport {
  ok: boolean; //                 true when there is no issue with severity "error"
  issues: ValidationIssue[];
}
export interface UsageItem {
  kind: 'deployment' | 'template';
  id: Id;
  label: string;
  siteSlug: Slug;
  containerSlug: Slug;
  serviceSlug: Slug;
  version: string;
}

export interface MeResponse {
  account: Account;
  /** convenience flags for the GUI (the API checks again) */
  isAdmin: boolean;
}

// ───────────────────────────────────────────────────────────── icons library (icons/{iconId})
export const ICON_CONTENT_TYPES = ['image/png', 'image/svg+xml', 'image/webp'] as const;
export type IconContentType = (typeof ICON_CONTENT_TYPES)[number];
export const ICON_MAX_BYTES = 65536;
export interface IconDoc {
  iconId: Id;
  name: string; //          1–80 (file name without extension by default)
  contentType: IconContentType;
  sizeBytes: number; //     1–65536 (after SVG sanitizing)
  sha256: Sha256; //        of the stored bytes; uploads are de-duplicated by it
  createdBy: Uid;
  createdAt: Ts;
}
/** Where an icon is used (DELETE /v1/icons/{id} answers 409 conflict with details.users = IconUser[]). */
export interface IconUser {
  type: 'service' | 'template' | 'tag' | 'deployment' | 'account';
  id: string;
  label: string;
}
export type IconListItem = IconDoc & { usedBy: number };
/** POST /v1/icons (raw body = the file bytes, Content-Type = one of ICON_CONTENT_TYPES, query ?name=). */
export interface UploadIconResponse {
  icon: IconDoc;
  /** true = these bytes were uploaded before; the existing icon is returned (nothing new stored) */
  existing: boolean;
}

// ───────────────────────────────────────────────────────────── registry browsing (register-image pickers; GET, not audited)
export interface RegistryRepository {
  name: string; //               e.g. "library/redis", "acme/api", "example/opm-test/hello-api"
  description: string | null;
  official: boolean; //          Docker Hub official image
  stars: number | null;
  pullCount: number | null;
  updatedAt: Ts | null;
}
export interface RegistryRepositoriesResponse {
  items: RegistryRepository[];
  /** how the list was made; "none" = this registry cannot list repositories (type the name) */
  source: 'docker-hub' | 'artifact-registry' | 'catalog' | 'none';
  /** false = listing is not supported or failed; the form keeps free text */
  supported: boolean;
  message: string | null;
}
export interface RegistryTag {
  tag: string;
  pushedAt: Ts | null;
  digest: Sha256 | null;
  sizeBytes: number | null;
}
export interface RegistryTagsResponse {
  items: RegistryTag[]; //       newest first when pushedAt is known, else by name
  source: 'docker-hub' | 'artifact-registry' | 'registry-v2' | 'none';
  supported: boolean;
  message: string | null;
}

export interface CreateRegistryRequest {
  label: string;
  url?: string;
  /** "" + "" = anonymous (public images, e.g. Docker Hub library/redis); otherwise both 1–255 / 1–8192 */
  username: string;
  password: string;
}
export interface PatchRegistryRequest {
  label?: string;
  url?: string;
  username?: string;
  password?: string;
}
export interface RevealPasswordResponse extends RevealResponse {
  /** = value (kept for older clients) */
  password: string;
}
export interface RegistryTestResponse {
  ok: boolean;
  message: string;
  at: Ts;
}

export type ServiceListItem = Service & { imageCount: number; usedBy: number };
export interface CreateServiceRequest {
  label: string;
  serviceSlug: Slug; //        "Name (id)": auto-derived from the label in the form, fixed after create
  icon?: Icon; //              default null
  description?: string;
}
export interface PatchServiceRequest {
  label?: string;
  icon?: Icon;
  description?: string;
}
export interface RegisterImageRequest {
  version: string; //       RE.version; the document id; exists → 409 conflict (details.reason "version-exists")
  reference: string; //     full docker reference, e.g. docker.io/northwind/backend:3.0.9
  /** omitted → the registry whose URL host matches the reference host (first by label; none → null = public); null = public */
  registryId?: Id | null;
  platform?: Platform; //   default linux/amd64
  mode: ImportMode;
  kind: ImageKind;
  entrypoint?: string[] | null;
  command?: string[] | null;
  workdir?: string | null;
  user?: string | null;
  ports?: ImagePort[];
  env?: ImageEnv[];
  volumes?: ImageVolumeInput[]; // kind optional: absent → inferred from the path (§21 #83)
  healthcheck?: HealthCheck | null;
  stopTimeoutSec?: number;
  description?: string;
}
export interface InspectImageRequest {
  reference: string;
  registryId?: Id | null; // same rule as RegisterImageRequest
  platform?: Platform;
}
/** Read from the registry (manifest + image config) to prefill the register form. Nothing is stored. */
export interface InspectImageResponse {
  digest: Sha256;
  platform: Platform;
  sizeBytes: number;
  layers: number;
  entrypoint: string[] | null;
  command: string[] | null;
  workdir: string | null;
  user: string | null;
  ports: ImagePort[]; //     from ExposedPorts, names "p<port>" (e.g. "p8080"), protocol from the key
  env: ImageEnv[]; //        from Env (value = default), required false, secret false
  volumes: ImageVolume[]; // from Volumes, names from the last path segment ("/data" → "data", unique, "volume-<n>" fallback); kind always 'dir'
}
/**
 * PATCH /v1/services/{serviceSlug}/images/{version} — everything except the image IDENTITY is editable (SPEC §2.2 Image,
 * §6.1 patchImage, §21 #82). Identity (never editable → invalid V_IMMUTABLE): version, serviceSlug, source, digest,
 * platform, import (mode, status, layers …), createdAt / createdBy. reference + registryId: only while
 * import.status = failed (else 409 conflict); there is no re-fetch of a ready image ("Clone as new version" instead).
 *
 * `ports` / `env` / `volumes` = the FULL new list (rows added / removed / edited). Legacy partial form (still accepted):
 * every port item only `{ name, description }`, every env item without `required` / `secret` → merged by name into the
 * existing rows (unknown name → invalid V_REF). Mixing full and partial items → invalid V_TYPE.
 *
 * Renames: a row whose name changed is listed in `renames` ({ kind, from: old name, to: new name }); a name of the old
 * list that is neither kept nor a rename source is REMOVED. A renamed / removed name still used by a template or a
 * deployment DRAFT (container of this serviceSlug + version: port rows, health check / edge proxy portName, env rows,
 * `container.env.<NAME>` / `site.<s>.port.<containerSlug>.<port>` expressions, volume rows) → 409 conflict with
 * details `ImageInUseDetails` (reason "in-use"). `renameEverywhere: true` rewrites those references of RENAMED names in
 * every TEMPLATE in the same transaction. §21 #94: deployment drafts are detached and NEVER rewritten — they keep the old
 * names (listed in the 409 details + the audit entry's deploymentsNotRewritten) until a Template sync or a manual edit;
 * heads, revisions and proposals never change (own ImageSpec copy); removals stay blocked until the containers stop using the name.
 */
export interface PatchImageRequest {
  /** reference / registryId: only while import.status is "failed" (fix a wrong source, then retry-import); else 409 conflict */
  reference?: string;
  registryId?: Id | null;
  kind?: ImageKind;
  entrypoint?: string[] | null;
  command?: string[] | null;
  workdir?: string | null;
  user?: string | null;
  ports?: ImagePort[] | { name: Slug; description: string }[];
  env?: ImageEnv[] | { name: string; value?: Value | null; options?: string[] | null; description?: string }[]; // legacy partial: value null = remove the default; options null = free text
  volumes?: ImageVolumeInput[]; // kind optional: absent → inferred from the path (§21 #83)
  healthcheck?: HealthCheck | null;
  stopTimeoutSec?: number;
  description?: string;
  /** rows of the new lists whose name changed (old → new); every other old name missing from the new list is removed */
  renames?: ImageRename[];
  /** rewrite in-use RENAMED names in templates + deployment drafts (one transaction); default false → 409 in-use */
  renameEverywhere?: boolean;
}
/** The three kinds of image names that containers reference. */
export type ImageNameKind = 'port' | 'env' | 'volume';
export interface ImageRename {
  kind: ImageNameKind;
  from: string; // the name in the stored image
  to: string; //   the name in the new list
}
/** One reference of an image name in a template / deployment draft (shared/src/image-edit.ts imageNameUses). */
export interface ImageNameUse {
  kind: 'template' | 'deployment';
  templateId?: Id; //   kind template
  deploymentId?: Id; // kind deployment
  label: string; //     template / deployment label
  source: 'template' | 'draft';
  siteSlug: Slug;
  containerSlug: Slug; // the container (of this image) whose name is referenced
  /** where: "ports[http]", "healthcheck.portName", "edge.routes[api].portName", "env[NODE_ENV]", "env[X].value",
   *  "volumes[0].mountPath", "volumes[data]", "env[URL].value (site.<s>.port.<c>.<p>)" (then containerSlug = the REFERENCING container) */
  field: string;
}
export interface ImageImpactItem {
  kind: ImageNameKind;
  name: string; //               the stored name
  change: 'renamed' | 'removed';
  to: string | null; //          the new name (renamed) or null
  usedBy: ImageNameUse[];
}
/** 409 conflict details of patchImage when renamed / removed names are in use. */
export interface ImageInUseDetails {
  reason: 'in-use';
  items: ImageImpactItem[];
}

export type NotFoundPageListItem = Omit<NotFoundPage, 'htmlCode'> & { usedBy: number; sizeBytes: number };
export interface CreateNotFoundPageRequest {
  label: string;
  htmlCode: string;
}
export type NotFoundPageDetail = NotFoundPage & { usedBy: UsageItem[] };

/** §21 #104 create from another branch (template / deployment / agent branch) */
type CreateFromFields = import('./branch.js').CreateFrom;
export interface CreateTemplateRequest extends CreateFromFields {
  label: string;
  /** §21 #106 optional: the entity slug (RE.slug, unique among templates → 409 conflict details.reason slug-taken); default uniqueSlug(slugifyLabel(label)) */
  slug?: Slug;
  icon?: Icon; // default null
  description?: string;
}
export interface PatchTemplateRequest {
  ifVersion: number;
  label?: string;
  icon?: Icon;
  description?: string;
  /** @deprecated §21 #105 — config fields answer 410 gone (commit them: POST /v1/templates/{id}/commits) */
  variables?: Variable[];
  /** @deprecated §21 #105 */
  secrets?: SecretDef[];
  /** @deprecated §21 #105 */
  files?: FileDef[];
  /** @deprecated §21 #105 */
  sites?: Site[];
}
export interface TemplateWriteResponse {
  template: Template;
  validation: ValidationReport;
}

export interface BlobRef {
  sha256: Sha256;
  sizeBytes: number;
}
export interface CreateUploadRequest {
  /** 'blob' ≤ 50 MiB (files); 'seed-archive' ≤ SEED_ARCHIVE_MAX (200 MiB, .tar.gz volume starter content; round E) */
  purpose: 'blob' | 'seed-archive';
  sizeBytes: number;
  sha256: Sha256;
  /** §21 #107: join into this commit stage (staging/<stageId>/<hex>) instead of the store; not audited */
  stageId?: Ulid;
}
export interface UploadPartResponse {
  received: number[];
}
export interface CompleteUploadResponse {
  upload: Upload;
  blob: BlobRef | null;
  transfer: Transfer | null;
}

export type TagListItem = Tag & { deployments: number };
export interface CreateTagRequest {
  tagSlug: Slug;
  label: string;
  icon?: Icon; // default null
  color: string;
}
export interface PatchTagRequest {
  label?: string;
  icon?: Icon;
  color?: string;
}
export interface DeleteTagResponse {
  deleted: true;
  removedFrom: { deployments: number; accessRules: number };
}

export interface CreateAccountRequest {
  name: string;
  email: string;
  phone?: string | null;
  img?: Icon;
  access?: AccessRule[]; // needs accounts.admin when non-empty
}
/**
 * §21 #84: creation sets a generated password (stored like setAccountPassword → viewable with revealAccountPassword until
 * the person changes it). value = that password, shown in RevealValue for 60 s; no set-password link any more.
 */
export interface CreateAccountResponse extends RevealResponse {
  account: Account;
}
/** POST /v1/accounts/{uid}/password ("Set new password…"): a generated strong password (20 chars, alnum) set via the Admin SDK. */
export interface SetAccountPasswordResponse extends RevealResponse {
  setAt: Ts;
  setBy: Uid;
}
/** POST /v1/accounts/{uid}/password/reveal */
export type RevealAccountPasswordResponse = SetAccountPasswordResponse;
/** POST /v1/accounts/{uid}/password-reset (new link, stored) and …/password-reset/reveal: value = the link. */
export interface ResetLinkResponse extends RevealResponse {
  /** = value (kept for older clients) */
  passwordSetLink: string;
  createdAt: Ts;
  createdBy: Uid;
  expiresAt: Ts; // createdAt + 1 h (Firebase oob code lifetime)
}
export interface PatchAccountRequest {
  name?: string;
  phone?: string | null;
  img?: Icon;
  access?: AccessRule[]; // needs accounts.admin
}
export interface DisableAccountRequest {
  disabled: boolean;
}

export interface CreateDeploymentRequest extends CreateFromFields {
  label: string;
  /** §21 #106 optional: the entity slug (unique among deployments → 409 conflict details.reason slug-taken); default from the label */
  slug?: Slug;
  /** legacy shortcut for from = { branch: template, at: head }; ignored when `from` is given */
  templateId: Id | null;
  tagSlugs: Slug[];
  managerAccountUids: Uid[];
  image?: Icon;
}
export interface CreateDeploymentResponse {
  deployment: DeploymentView;
  /** the agent GUI access key ("XXXX-XXXX-XXXX-XXXX-XXXX-XXXX"); stored (revealAccessKey shows it again any time) */
  accessKey: string;
  hideAfterSec: typeof REVEAL_SECONDS;
}
export interface PatchDeploymentRequest {
  label?: string;
  image?: Icon;
  tagSlugs?: Slug[];
  managerAccountUids?: Uid[];
}
export interface DeleteDeploymentRequest {
  confirm: string; // must equal the label
}
export type DraftSection = 'variables' | 'secrets' | 'files' | 'sites';
export interface PutDraftSectionRequest {
  ifRevision: number;
  items: Variable[] | SecretDefInput[] | FileDef[] | Site[];
}
export interface PutDraftSiteRequest {
  ifRevision: number;
  site: Site;
}
export interface DraftWriteResponse {
  draftVersion: number;
  validation: ValidationReport;
}
/** cloud revealSecret / regenerate + agent GUI revealSecret (hideAfterSec 60, §21 #84) */
export type RevealSecretResponse = RevealResponse;
/** POST /v1/deployments/{id}/secrets/{secretSlug}/regenerate — Regenerate in RevealValue (secret must have a generate rule). */
export interface RegenerateSecretRequest {
  ifRevision: number; // must equal draftVersion (409 conflict otherwise)
}
export interface RegenerateSecretResponse extends RevealResponse {
  draftVersion: number;
  rotatedAt: Ts;
}
/** devTools (§21 #97 d): path `<site>/devtools/<presetSlug>`; Template sync key `sites/<site>/devtools/<presetSlug>`. */
export type DiffSection = 'variables' | 'secrets' | 'files' | 'sites' | 'volumes' | 'containers' | 'edge' | 'devTools';
export interface DiffChange {
  section: DiffSection;
  siteSlug: Slug | null;
  /** e.g. "api-workers", "app/web/env/LOG_LEVEL", "app/edge/routes/api" */
  path: string;
  kind: 'added' | 'removed' | 'changed';
  before: unknown; // secrets: "set" | "not set"
  after: unknown; //  secrets: "changed" | "not changed" | "set" | "not set"
  /** round E: a hint the review shows next to the change (e.g. "seed changed — applies only to new / empty volumes") */
  note?: string;
  /** §21 #101 one file of a volume's content (path `<site>/volumes/<v>/content/<path>`): the blobs to fetch for a text diff */
  blob?: { volumeSlug: string; path: string; before: Sha256 | null; after: Sha256 | null };
}
export interface DiffResponse {
  head: { revId: RevId; seq: number; reportedAt: Ts } | null;
  draftVersion: number;
  changes: DiffChange[];
  publishable: boolean;
  blockers: ValidationIssue[]; //  errors that block publish
  warnings: ValidationIssue[];
  /** proposals that are not terminal (to show "pending" / "stale" banners) */
  pending: ProposalMeta[];
}
export interface PublishRequest {
  ifRevision: number;
  summary: string;
}
export interface PublishResponse {
  proposal: Proposal;
  revision: RevisionSummary;
}
export interface ResetToHeadRequest {
  confirm: true;
}

// ───────────────────────────────────────────────────────────── template sync (§21 #94, shared/src/template-sync.ts)
/**
 * Classification of one item of the 3-way Template sync (base = template@baseVersion, theirs = the template now,
 * ours = the deployment draft):
 *   theirs   — only the template changed it (clean; take it with the checkbox)
 *   ours     — only the draft changed it ("your change"; kept, shown greyed)
 *   same     — both changed it the same way (no-op)
 *   conflict — both changed it differently: take template / keep mine / (scalars) a merged value
 *   skipped  — a template change not taken in an earlier sync (unchanged since); can still be taken
 *   default  — variables only: the template changed a variable's DEFAULT value; values belong to the deployment and are
 *              never overwritten unless the user ticks "use template default"
 */
export type TemplateSyncKind = 'theirs' | 'ours' | 'same' | 'conflict' | 'skipped' | 'default';
export type TemplateSyncOp = 'added' | 'removed' | 'changed' | 'none';
export interface TemplateSyncChange {
  /** stable identity: "variables/<slug>", "variables/<slug>/value", "secrets/<slug>", "files/<slug>", "sites/<site>",
   *  "sites/<site>/settings/<field>", "sites/<site>/edge/<field>", "sites/<site>/edge/hosts/<hostname>",
   *  "sites/<site>/edge/routes/<routeId>", "sites/<site>/volumes/<volumeSlug>", "sites/<site>/devtools/<presetSlug>", "sites/<site>/containers/<slug>",
   *  "sites/<site>/containers/<slug>/<field>", "sites/<site>/containers/<slug>/env/<NAME>" */
  key: string;
  section: DiffSection;
  siteSlug: Slug | null;
  /** human path, e.g. "app / api / env PORT" */
  label: string;
  kind: TemplateSyncKind;
  theirsOp: TemplateSyncOp; // what the template did since the base
  oursOp: TemplateSyncOp; //   what the draft did since the base
  /** values (null when absent — see present); secrets: the definition only (never a value); variables: the definition without value */
  base: unknown;
  theirs: unknown;
  ours: unknown;
  present: { base: boolean; theirs: boolean; ours: boolean };
  /** both sides are scalars (string / number / boolean / null): a conflict may be resolved with a merged value */
  scalar: boolean;
  theirsHash: string;
  /** set when kind = skipped */
  skippedAt?: Ts;
}
export interface TemplateSyncCounts {
  theirs: number;
  ours: number;
  same: number;
  conflict: number;
  skipped: number;
  default: number;
  /** template changes since the last sync = theirs + conflict + same + default */
  templateChanges: number;
}
export interface TemplateSyncStatus {
  linked: boolean;
  link: SrcTemplateView | null;
  templateLabel: string | null;
  /** null = not linked or the template was deleted */
  currentVersion: number | null;
  templateDeleted: boolean;
  /** currentVersion − baseVersion (0 when not linked / deleted) */
  newerVersions: number;
  /** actionable template changes since the last sync = counts theirs + conflict + default (0 when not linked / deleted) */
  pendingChanges: number;
  conflicts: number;
}
export interface TemplateSyncPlan {
  link: SrcTemplateView | null;
  template: { templateId: Id; label: string; icon: Icon; version: number; updatedAt: Ts; updatedBy: Uid };
  /** true when the template differs from the linked one ("Change template…" / "Link a template…") */
  relink: boolean;
  /** the base: template@baseVersion of the linked template; null = no base (link: everything is an addition) */
  baseVersion: number | null;
  draftVersion: number;
  base: ConfigView;
  theirs: ConfigView;
  ours: ConfigView;
  changes: TemplateSyncChange[];
  counts: TemplateSyncCounts;
}
export type TemplateSyncTake = 'theirs' | 'ours' | 'custom';
export interface TemplateSyncChoice {
  key: string;
  take: TemplateSyncTake;
  /** take = custom (scalar items only): the merged value */
  value?: unknown;
}
export interface TemplateSyncApplyRequest {
  ifRevision: number; //       the draftVersion that was reviewed
  templateId: Id; //           the template reviewed (≠ the linked one = relink)
  templateVersion: number; //  the template version reviewed (changed meanwhile → 409 conflict)
  choices: TemplateSyncChoice[]; // missing = not taken (conflicts MUST have a choice)
  /** variable values entered in the review (e.g. required variables the template added) */
  values?: Record<Slug, VariableValue>;
}
export interface TemplateSyncMissing {
  variables: { variableSlug: Slug; label: string; type: VariableType; options: string[] | null; required: boolean }[];
  secrets: { secretSlug: Slug; label: string }[];
}
export interface TemplateSyncPreviewResponse {
  /** the draft as it would be after Apply */
  draft: ConfigView;
  /** draft before → after */
  changes: DiffChange[];
  validation: { ok: boolean; issues: ValidationIssue[] };
  unresolved: string[]; // keys of conflicts without a choice
  taken: number;
  skipped: number;
  missing: TemplateSyncMissing;
}
export interface TemplateSyncApplyResponse {
  draftVersion: number;
  validation: { ok: true; issues: ValidationIssue[] };
  link: SrcTemplateView;
  taken: number;
  skipped: number;
  missing: TemplateSyncMissing;
}
export interface UnlinkTemplateRequest {
  confirm: true;
}

export interface InstallTokenView {
  tokenHash: string;
  siteSlug: Slug;
  /** §21 #92 ('install' for docs without kind) */
  kind: InstallTokenKind;
  /** null = no expiry (maintenance) */
  expiresAt: Ts | null;
  usedAt: Ts | null;
  revokedAt: Ts | null;
  createdAt: Ts;
  createdBy: Uid;
  /** maintenance tokens are 'active' until revoked (they never expire and are never "used") */
  state: 'active' | 'used' | 'expired' | 'revoked';
  /** §21 #84: true = active AND the token is stored → "View" (revealInstallCommand); false on active pre-#84 tokens
   *  ("not stored — regenerate to view") and on used / expired / revoked ones (their state is shown, not the value).
   *  Always false for maintenance tokens (they are viewed through the site's upgrade command). */
  stored: boolean;
  /** §21 #92: this token is the enrolled site's current upgrade command (works for the upgrade-only script now) */
  upgradeCommand: boolean;
}
/** §21 #92 the enrolled site's "Upgrade / reinstall command" (Setup site card). The token is the enrollment's own install
 *  token (kind install, used for the current enrollment) or the newest unrevoked maintenance token bound to it. */
export interface SiteUpgradeCommandView {
  /** null = no usable token for the current enrollment (e.g. its token doc is gone) → Regenerate */
  tokenHash: string | null;
  kind: InstallTokenKind | null;
  /** the plaintext is stored → View (revealUpgradeCommand); false → "not stored — Regenerate" */
  stored: boolean;
  createdAt: Ts | null;
  createdBy: Uid | null;
}
/** §21 #92 revealUpgradeCommand / regenerateUpgradeCommand; value = command (the same `curl … | sudo sh` form as the
 *  install command; on the server it runs the upgrade-only flow of §19.1 / §21 #91) */
export interface UpgradeCommandResponse extends RevealResponse {
  command: string;
  token: string;
  tokenHash: string;
  siteSlug: Slug;
  kind: InstallTokenKind;
  /** agent.enrolledAt of the enrollment the command belongs to */
  enrolledAt: Ts;
}
/** §21 #92 skew of an agent's clock: |agent − server| above this many seconds → warning on Setup / Status */
export const CLOCK_SKEW_WARN_SEC = 120;
/** §21 #92 "5 h 30 min" / "3 min" / "2 d 4 h" for a number of seconds (≥ 0) */
export function formatSkewDuration(sec: number): string {
  const s = Math.round(Math.abs(sec));
  if (s < 60) return `${s} s`;
  if (s < 3600) return `${Math.round(s / 60)} min`;
  if (s < 86400) {
    const h = Math.floor(s / 3600);
    const m = Math.round((s % 3600) / 60);
    return m === 0 ? `${h} h` : m === 60 ? `${h + 1} h` : `${h} h ${m} min`;
  }
  const d = Math.floor(s / 86400);
  const h = Math.round((s % 86400) / 3600);
  return h === 0 ? `${d} d` : `${d} d ${h} h`;
}
/** §21 #92 the warning text for an agent clock skew (agent − server, seconds); null when |skew| ≤ CLOCK_SKEW_WARN_SEC */
export function clockSkewWarning(skewSec: number | null | undefined): string | null {
  if (typeof skewSec !== 'number' || !Number.isFinite(skewSec) || Math.abs(skewSec) <= CLOCK_SKEW_WARN_SEC) return null;
  return `agent clock is ${formatSkewDuration(skewSec)} ${skewSec > 0 ? 'ahead' : 'behind'} — fix NTP on the server (e.g. sudo timedatectl set-ntp true)`;
}
/** §21 #92 state of an install token: maintenance tokens are active until revoked (never used / expired) */
export function installTokenState(t: Pick<InstallToken, 'kind' | 'usedAt' | 'revokedAt' | 'expiresAt'>, nowMs: number = Date.now()): InstallTokenView['state'] {
  // revoked wins: a used install token can be revoked later (Regenerate of the site's upgrade command, §21 #92)
  if (t.revokedAt) return 'revoked';
  if (t.kind === 'maintenance') return 'active';
  if (t.usedAt) return 'used';
  if (t.expiresAt && Date.parse(t.expiresAt) <= nowMs) return 'expired';
  return 'active';
}
/** §21 #92 agent clock vs the cloud (from the last heartbeat that carried `agentTime`) */
export interface AgentClockSkew {
  /** agent clock − server receive time, whole seconds (positive = the agent clock is ahead) */
  skewSec: number;
  /** server time of the heartbeat that measured it */
  measuredAt: Ts;
}
/** Storage deps/manifest.json — pinned install dependencies (written by backend/scripts/upload-deps.ts). The install script
 *  never installs "latest": versions + sha256 are embedded at script generation. */
export interface DepsFile {
  path: string; //   Storage path, e.g. deps/docker/27.5.1/amd64/docker-27.5.1.tgz
  sha256: string; // hex
  size: number;
}
export interface DepsManifest {
  docker: { version: string; files: { amd64: DepsFile; arm64: DepsFile } };
  /** minimum Docker Engine API version the agent accepts (an existing Docker below it is never touched → script fails) */
  minDockerApi: string;
  /** notes (§21 #90, optional): release notes shown on the agent GUI Agent page + the cloud Setup page;
   *  schema / minSchema (§21 #91) from agent/<v>/release.json */
  agent: { version: string; notes?: string | null; schema?: number | null; minSchema?: number | null } | null;
  updatedAt: Ts;
}
export interface SetupResponse {
  /** stored = the plaintext is kept (§21 #84) → View; false = pre-#84 deployment → "not stored — regenerate to view" */
  accessKey: { version: number; rotatedAt: Ts; rotatedBy: Uid; stored: boolean };
  apiBase: string;
  /** what the install script installs when missing (deps/manifest.json); null = no manifest uploaded yet */
  installDeps: { dockerVersion: string; minDockerApi: string; agentVersion: string | null } | null;
  /** §21 #90 the pinned agent release (= what "Upgrade agent" installs); null = none pinned */
  agentRelease: AgentReleaseInfo | null;
  /** §21 #90 the current / last cloud "Upgrade all" run; null = never */
  agentUpgradeRun: AgentUpgradeRun | null;
  /** §21 #92 the cloud's clock when it answered (the console corrects relative times for a wrong browser clock) */
  serverTime: Ts;
  sites: {
    siteSlug: Slug;
    label: string;
    inDraft: boolean;
    inHead: boolean;
    agent: (Pick<AgentDoc, 'agentVersion' | 'enrolledAt' | 'lastSeenAt' | 'online' | 'hostname' | 'ip' | 'keyVersion' | 'os' | 'arch' | 'advertiseHost' | 'detectedAddresses'> & { clockSkew: AgentClockSkew | null }) | null;
    /** §21 #92 the "Upgrade / reinstall command" of an enrolled site; null when not enrolled */
    upgradeCommand: SiteUpgradeCommandView | null;
    /** §21 #86: agent GUI URLs from the last heartbeat (detectedAddresses × guiPorts; port 80 / the edge first); [] when
     *  not enrolled or for agents < 0.8 (then the frontend falls back to :8080 / :7080) */
    guiUrls: { url: string; kind: GuiPort['kind'] }[];
    /** §21 #86: true = the site runs the built-in default edge (no applied configuration yet); null = unknown / old agent */
    defaultEdge: boolean | null;
    /** §21 #86: edge ports the agent cannot bind (from heartbeat edge.bindErrors, purpose edge-http / edge-https), e.g.
     *  message "port 80 is used by nginx — stop it or change edge.httpPort" */
    portAlerts: { port: number; purpose: 'edge-http' | 'edge-https'; owner: string | null; message: string }[];
    /** §21 #90 null when not enrolled */
    upgrade: SiteAgentUpgradeView | null;
    tokens: InstallTokenView[];
  }[];
}
/** §21 #90 per-site agent upgrade state for the Setup / Status pages */
export interface SiteAgentUpgradeView {
  installed: string;
  latest: string | null;
  /** latest is newer than installed */
  available: boolean;
  /** installed ≥ GUI_UPGRADE_MIN_VERSION (else: rerun the install command once — `hint`) */
  supported: boolean;
  /** "Rerun the install command of this site to reach agent ≥ 0.10.0; later upgrades are one click here." when !supported */
  hint: string | null;
  request: AgentUpgradeRequest | null;
  /** the last progress the agent reported (heartbeat) */
  progress: AgentUpgradeProgress | null;
}
/** rotate (= Regenerate, typed ACCEPT in the GUI) and revealAccessKey; value = accessKey */
export interface RotateAccessKeyResponse extends RevealResponse {
  accessKey: string;
  version: number;
  rotatedAt: Ts;
}
export type RevealAccessKeyResponse = RotateAccessKeyResponse;
export interface CreateInstallTokenRequest {
  siteSlug: Slug;
  /** §21 #84 Regenerate: revoke this site's active tokens (install-token.revoked each) in the same transaction */
  replaceActive?: boolean;
}
/** create / Regenerate and revealInstallCommand; value = command */
export interface CreateInstallTokenResponse extends RevealResponse {
  /** the full shell command (token inside, header form) */
  command: string;
  /** "opi_" + base64url(32 random bytes) — stored while usable (revealInstallCommand; after enrolling: revealUpgradeCommand) */
  token: string;
  tokenHash: string;
  siteSlug: Slug;
  expiresAt: Ts;
}
export type RevealInstallCommandResponse = CreateInstallTokenResponse;
export interface RevokeAgentRequest {
  confirm: Slug; // must equal the siteSlug
}

export interface SiteCloudStatus {
  siteSlug: Slug;
  label: string;
  inHead: boolean;
  enrolled: boolean;
  online: boolean;
  lastSeenAt: Ts | null;
  relayVia: Slug | null;
  agentVersion: string | null;
  headRevId: RevId | null;
  headSeq: number | null;
  staging: HeartbeatStaging | null;
  keyVersion: number | null;
  metrics: HostMetricsSummary | null;
  edge: EdgeReport | null;
  /** auto host of the site (null until enrolled) */
  advertiseHost: string | null;
  detectedAddresses: string[];
  /** §21 #90 null when not enrolled */
  upgrade: SiteAgentUpgradeView | null;
  /** §21 #92 agent clock skew (null = not measured / not enrolled) */
  clockSkew: AgentClockSkew | null;
}
export interface StatusResponse {
  serverTime: Ts;
  sites: SiteCloudStatus[];
  uptime: { siteSlug: Slug; days: { date: string; onlineFraction: number; hours: number[] }[] }[]; // 30 days, oldest first
}
export interface ActivitiesQuery {
  site?: string;
  origin?: ActivityOrigin;
  type?: string; //   exact type or a prefix ending in "." (e.g. "update.")
  actor?: string;
  from?: Ts;
  to?: Ts;
  cursor?: string;
  limit?: number; //  default 100, max 500
  via?: 'pipe' | 'manual';
}
export interface UploadActivitiesResponse {
  inserted: number;
  skipped: number;
  rejected: number;
  errors: { activityId: string; reason: string }[];
  sites: { siteSlug: Slug; fromSeq: number; toSeq: number }[];
  signatureOk: boolean;
}
export interface CreateTransferRequest {
  fileName: string;
  sizeBytes: number;
  sha256: Sha256;
  note?: string;
}
export interface CreateTransferResponse {
  transfer: Transfer;
  upload: Upload;
}

// ───────────────────────────────────────────────────────────── install + agent ↔ cloud
export interface EnrollRequest {
  publicKey: string; //   Ed25519 raw public key, base64
  agentVersion: string;
  hostname: string;
  ip: string;
  advertiseHost: string;
  agentPort: number;
  os: string;
  arch: string;
  /** non-loopback IPs (IPv4 first) + hostname; optional for old agents */
  detectedAddresses?: string[];
}
export interface EnrollResponse {
  deploymentId: Id;
  siteSlug: Slug;
  peers: PeerInfo[];
  accessKey: { hash: string; version: number };
  serverTime: Ts;
  /** same as CloudView.consoleUrl / deploymentLabel (optional; new installs know them before the first poll) */
  consoleUrl?: string | null;
  deploymentLabel?: string | null;
}
export interface PollQuery {
  cursor?: string;
  wait?: number; // seconds to hold the request when nothing changed: default 25, max 50, 0 = answer at once
}
export type PollResponse = CloudView;
export interface HeartbeatRequest {
  siteSlug: Slug;
  agentVersion: string;
  headRevId: RevId | null;
  staging: HeartbeatStaging | null;
  peersSeen: { siteSlug: Slug; lastSeenAt: Ts }[];
  metrics: HostMetricsSummary;
  edge: EdgeReport | null;
  keyVersion: number;
  advertiseHost: string;
  agentPort: number;
  ip: string;
  hostname: string;
  /** non-loopback IPs (IPv4 first) + hostname; optional for old agents */
  detectedAddresses?: string[];
  /** §21 #86 (optional for old agents) */
  guiPorts?: GuiPort[];
  /** §21 #90 current / last upgrade attempt (sent at once on every phase change, then with every heartbeat until 24 h after it finished) */
  upgrade?: AgentUpgradeProgress | null;
  /** §21 #92 the agent's own clock when it sent this heartbeat (raw, NOT corrected by the X-OPM-Server-Time offset);
   *  the cloud stores clockSkew = agentTime − receive time. Optional (agents before 0.11 omit it). */
  agentTime?: Ts;
  /** §21 #101 sync state of every persist-to-cloud volume of this site (agents ≥ 0.12; absent on older agents) */
  volumeSync?: import('./volume-content.js').VolumeSyncReport[];
  /** §21 #104 the agent branch of this site (agents ≥ 0.14) */
  branch?: import('./branch.js').AgentBranchReport | null;
}
export interface HeartbeatResponse {
  ok: true;
  serverTime: Ts;
}
/** §21 #86 `opm-agent upgrade` (enrolled site, signed by the site key): the pinned agent release for this os/arch =
 *  deps/manifest.json agent.version, else OPM_AGENT_VERSION / agent/latest (the same pin the install script embeds). */
export interface AgentReleaseResponse {
  version: string;
  os: 'linux' | 'darwin';
  arch: 'amd64' | 'arm64';
  sha256: string; //           hex of the binary
  signature: string; //        Ed25519 (release key) of the sha256 hex, base64url
  releasePublicKey: string; // base64; agent.json releasePublicKey wins when set (must match)
  /** GET path of the binary (signed agent request): /v1/agent/binary?os=&arch=&version= */
  binaryPath: string;
  /** §21 #90 release notes (deps/manifest.json agent.notes); null / absent = none */
  notes?: string | null;
  /** §21 #90 binary size in bytes (download progress); absent on old clouds */
  sizeBytes?: number | null;
  /** §21 #91 SQLite schema of this build (opm.db PRAGMA user_version it migrates to) and the oldest DB schema it opens;
   *  null = unknown (builds before 0.10.0 = AGENT_LEGACY_SCHEMA) */
  schema?: number | null;
  minSchema?: number | null;
}
/** §21 #91 build metadata (`opm-agent version --json`; make dist writes dist/release.json; upload-agent.ts stores
 *  agent/<v>/release.json; served as AgentReleaseResponse.schema/minSchema + headers X-OPM-Agent-Schema / -Min-Schema). */
export interface AgentBuildMeta {
  version: string;
  schema: number;
  minSchema: number;
  commit: string | null;
  builtAt: string | null;
  os?: string;
  arch?: string;
}
/** §21 #91 agents before 0.10.0 have no schema metadata and no user_version: they count as schema 1. */
export const AGENT_LEGACY_SCHEMA = 1;
/** §21 #91 install-script decision for an installed agent vs the target (also used by tests + the cloud). */
export type AgentInstallAction = 'fresh' | 'upgrade' | 'downgrade' | 'same';
export function agentInstallAction(installed: string | null, target: string): AgentInstallAction {
  if (!installed) return 'fresh';
  const c = compareAgentVersions(installed, target);
  return c < 0 ? 'upgrade' : c > 0 ? 'downgrade' : 'same';
}
/** §21 #91 a target build can open the installed DB when dbSchema ≤ target.schema (and ≥ target.minSchema). Unknown
 *  numbers count as AGENT_LEGACY_SCHEMA. false → the downgrade is refused unless a DB backup with a compatible schema is restored. */
export function agentSchemaCompatible(dbSchema: number | null | undefined, target: { schema?: number | null; minSchema?: number | null }): boolean {
  const db = dbSchema ?? AGENT_LEGACY_SCHEMA;
  const max = target.schema ?? AGENT_LEGACY_SCHEMA;
  const min = target.minSchema ?? 0;
  return db <= max && db >= min;
}
/** §21 #90 cloud console "Upgrade agent" (one site) */
export interface RequestAgentUpgradeRequest {
  /** must equal the pinned version (else 409 stale: the pin moved, reload) */
  version: string;
  /** typed siteSlug */
  confirm: Slug;
}
export interface RequestAgentUpgradeResponse {
  request: AgentUpgradeRequest;
}
/** §21 #90 cloud console "Upgrade all" (sequential, stops on the first failure) */
export interface UpgradeAllAgentsRequest {
  version: string;
  /** typed "UPGRADE" */
  confirm: 'UPGRADE';
}
export interface AgentUpgradeRunResponse {
  run: AgentUpgradeRun;
}
export interface ReportHeadRequest {
  revision: Revision;
}
export interface ReportHeadResponse {
  stored: boolean; //      false = the revision was already known
  headChanged: boolean;
}
export interface AgentProposalResponse {
  proposal: Proposal;
  revision: Revision;
}
export interface RegisterLocalProposalRequest {
  proposal: Proposal;
  revision: Revision;
}
export interface ProposalStatusReport {
  siteSlug: Slug; //       the reporting site
  staging?: StagingState;
  progress?: number;
  error?: string | null;
  /** coordinator only */
  status?: 'approved' | 'applying' | 'applied' | 'failed-rolled-back' | 'stale';
  approvedBy?: string;
  approvedAt?: Ts;
  updateId?: Ulid;
  result?: { ok: boolean; message: string; snapshotId: Ulid | null };
}
export interface ProposalStatusResponse {
  ok: true;
  proposal: Proposal;
}
export interface ImageManifestResponse {
  serviceSlug: Slug;
  version: string;
  digest: Sha256;
  platform: Platform;
  mediaType: string;
  config: { digest: Sha256; sizeBytes: number };
  layers: ImageLayer[];
}
export interface AgentActivitiesRequest {
  siteSlug: Slug;
  entries: Activity[]; //  ≤ 500, ascending seq, contiguous
}
export interface AgentActivitiesResponse {
  ackSeq: number;
  inserted: number;
  skipped: number;
  rejected: number;
  errors: { activityId: string; reason: string }[];
}
export interface AgentCreateTransferRequest {
  fileName: string;
  sizeBytes: number;
  sha256: Sha256;
  note?: string;
  actor: string;
}
export interface AgentTransferStatusRequest {
  /** §21 #100: `fetched` = a to-site file is stored on the site (sets Transfer.fetchedAt; no status change) */
  status: 'ready' | 'saved-on-server' | 'downloaded' | 'fetched';
  savedPath?: string;
  actor: string;
}
export interface RelayRequest {
  forSite: Slug;
  request: { method: string; path: string; headers: Record<string, string>; bodyB64: string | null };
}
export interface RelayResponse {
  status: number;
  headers: Record<string, string>;
  bodyB64: string | null;
}

// ═════════════════════════════════════════════════════════════ API DTOs — agent local API (/_opm/api/*)
export interface AgentInfoResponse {
  deploymentId: Id;
  siteSlug: Slug;
  agentVersion: string;
  enrolled: boolean;
  hasAccessKey: boolean;
  keyVersion: number;
  serverTime: Ts;
  surface: 'edge' | 'gui-alias' | 'agent-port';
  /** cloud console origin (cloud view / enroll answer, else agent.json consoleUrl); null when unknown */
  consoleUrl?: string | null;
  /** agent.json apiBase */
  apiBase?: string | null;
  /** Deployment.label from the cloud view; null when unknown */
  deploymentLabel?: string | null;
  /** §21 #91 SQLite schema of this build (absent before 0.10.0) */
  schema?: number;
}
/** §21 #91 GET /_opm/api/health (public, cheap): the install script + the upgrade engine wait for ok after a restart. */
export interface AgentHealthResponse {
  ok: boolean; //          store open + listeners bound + startup finished
  version: string;
  schema: number;
  pid: number;
  startedAt: Ts;
  /** reached the cloud since start (direct or relay or tunnel) */
  cloud: { connected: boolean; route: 'direct' | 'relay' | 'tunnel' | 'none'; lastContactAt: Ts | null };
  docker: { ok: boolean };
  /** inherited systemd sockets in use (socket activation) */
  socketActivated: boolean;
  checks: { name: string; ok: boolean; message: string | null }[];
}
export interface LoginRequest {
  accessKey: string; // 24 base32 chars; dashes, spaces and case are ignored
}
export interface LoginResponse {
  sessionId: Ulid;
  expiresAt: Ts;
  keyVersion: number;
}
export interface SessionResponse {
  sessionId: Ulid;
  expiresAt: Ts;
  keyVersion: number;
  /** §21 #118 the session runs through the cloud as this person (the GUI adopts the actor token: no email prompt) */
  remote?: { email: string; name: string | null; actorToken: string; expiresAt: Ts } | null;
}
export interface ActorRequest {
  email: string;
}
export interface ActorResponse {
  actorToken: string; // JWT (EdDSA) with ActorTokenClaims
  expiresAt: Ts;
  email: string;
}
export interface RecentActorsResponse {
  actors: ActorRow[];
}
export interface SiteTile {
  siteSlug: Slug;
  label: string | null; //     from the head config (null when the site is not in it)
  self: boolean;
  enrolled: boolean;
  reachable: boolean;
  lastSeenAt: Ts | null;
  host: string | null; //      resolved SiteConfig.host
  advertiseHost: string | null;
  agentPort: number | null;
  agentVersion: string | null;
  head: { revId: RevId; seq: number } | null;
  staging: { revId: RevId; staging: SiteStaging } | null;
  cloudRoute: PeerPing['cloudRoute'] | null;
  containers: { total: number; ready: number };
  inConfig: boolean;
  lock: UpdateLock | null;
}
export interface OverviewResponse {
  deploymentId: Id;
  self: Slug;
  serverTime: Ts;
  head: { revId: RevId; seq: number; hash: Sha256; summary: string; createdAt: Ts } | null;
  sites: SiteTile[];
  /** links[a][b] = rtt ms when a reached b in the last 30 s, null = no link, "self" on the diagonal */
  links: Record<Slug, Record<Slug, number | null | 'self'>>;
  cloud: { route: PeerPing['cloudRoute']; lastOkAt: Ts | null; fetchedAt: Ts | null; source: CloudView['source'] | null };
  lock: UpdateLock | null;
  deleted: boolean;
}
export interface AgentConfigResponse {
  head: { revId: RevId; seq: number; hash: Sha256 } | null;
  config: ConfigView; //       empty config when there is no head
  catalog: RevisionCatalog | null;
  /** §21 #104 agents ≥ 0.14: the working copy = head ⊕ GUI edits (masked); absent = no edits / old agent */
  working?: ConfigView | null;
  /** §21 #113: every image a local edit can use (head + recent cloud commits + drift-adopted + site images ⊇ catalog) */
  knownCatalog?: RevisionCatalog;
}
/** §21 #113 POST /_opm/api/images/resolve — a docker reference (pulled when missing) or an image on the host. */
export interface SiteImageResolveRequest {
  reference?: string;
  imageId?: string;
  /** pull even when the host has the reference (a moved tag) */
  pull?: boolean;
  /** registry credentials for this pull only (never stored) */
  username?: string;
  password?: string;
}
export interface SiteImageResolveResponse {
  key: string; // <serviceSlug>:<version> (version local-<12 hex>)
  image: ImageSpec;
  service: { serviceSlug: Slug; label: string; icon: string | null };
  pulled: boolean;
}
/** §21 #113 GET /_opm/api/images */
export interface SiteImagesResponse {
  catalog: RevisionCatalog;
  host: { id: string; repoTags: string[]; sizeBytes: number; created: number }[];
}
export interface LocalProposalRequest {
  baseRevId: RevId; //         must be the current head
  summary: string; //          1–200
  config: ConfigInput; //      full config; secrets: plainValue semantics (absent = keep)
}
export interface LocalProposalPreviewResponse {
  changes: DiffChange[];
  validation: ValidationReport; // includes local-edit violations (V_LOCAL_*)
}
export interface LocalProposalResponse {
  proposal: Proposal;
  changes: DiffChange[];
}
export interface StagingDetail {
  siteSlug: Slug;
  staging: SiteStaging;
  layers: { total: number; have: number };
  files: { total: number; have: number };
  blobs: { sha256: Sha256; kind: BlobKind; sizeBytes: number; source: 'cloud' | `peer:${string}` | 'local' | null; state: 'have' | 'downloading' | 'waiting' | 'failed' }[];
  issues: ValidationIssue[];
}
export interface ProposalListItem {
  proposal: Proposal;
  isHeadParent: boolean;
}
export interface ProposalDetailResponse {
  proposal: Proposal;
  revision: RevisionView;
  changes: DiffChange[]; //   head → proposal
  staging: StagingDetail[]; // one per site of the proposal (peers over the mesh; stale ones marked)
  gate: GateCheck;
}
export interface ApproveStartResponse {
  intent: ApproveIntent;
  gate: GateCheck;
}
export interface ApproveRequest {
  intentId: Ulid;
  typed: string; // must be exactly "ACCEPT"
  /** §21 #104 branch mode: the commit message of a snapshot rollback (required there, 422 V_COMMIT_MESSAGE) */
  message?: string;
}
export interface ApproveResponse {
  updateId: Ulid;
  run: UpdateRun;
}
export interface CreateSnapshotRequest {
  note?: string;
}
export interface CreateSnapshotResponse {
  snapshotId: Ulid;
  /** the snapshot is taken on every reachable site of the head config; poll GET /_opm/api/snapshots/{id} */
  sites: Slug[];
}
export interface SnapshotDetailResponse {
  summary: SnapshotSummary;
  snapshot: Snapshot; // config secrets masked
}
export interface RollbackPlanResponse {
  snapshot: SnapshotSummary;
  images: { siteSlug: Slug; containerSlug: Slug; serviceSlug: Slug; from: string | null; to: string | null }[];
  changes: DiffChange[];
  /** volumes in the snapshot (restored by volumeSlug, wherever they are mounted now) */
  volumesRestored: { siteSlug: Slug; volumeSlug: string; sizeBytes: number }[];
  /** declared volumes of the current head that are not in the snapshot (untouched: backup off, new since, or no data then) */
  volumesKept: { siteSlug: Slug; volumeSlug: string; backup: boolean }[];
  dataLossSince: Ts;
  newSeq: number;
  gate: GateCheck;
}
/** One volume of a site on the agent Backups page (GET /_opm/api/sites/{siteSlug}/volumes). */
export interface SiteVolumeStatus {
  volumeSlug: string;
  label: string;
  description: string;
  backup: boolean; //    false for orphans
  /** false = a directory under <agentHome>/volumes/ that the head config no longer declares (kept on disk, never deleted automatically, never backed up) */
  declared: boolean;
  /** the absolute directory (file volumes: file) on this server (location resolved against agentHome) */
  path: string;
  location: StorageLocation; // as configured (orphans: auto)
  /** round E: as configured (orphans: dir) */
  kind: SiteVolumeKind;
  seed: VolumeSeed | null;
  /** when the starter content was applied on this site (null = never: started empty or created before round E) */
  seeded: { at: Ts; source: VolumeSeed['kind']; entries: number } | null;
  usedBy: { containerSlug: Slug; imageVolume: Slug | null; mountPath: string; readOnly: boolean }[];
  /** bytes on disk of the volume directory (apparent size, measured at most every 60 s); null = no directory yet */
  sizeBytes: number | null;
  /** newest snapshot of this site that contains the volume */
  lastSnapshot: { snapshotId: Ulid; createdAt: Ts; sizeBytes: number } | null;
}
export interface SiteVolumesResponse {
  siteSlug: Slug;
  /** the agent data dir of this site ("./" locations are relative to it) */
  agentHome: string;
  measuredAt: Ts;
  volumes: SiteVolumeStatus[];
  /** host-path mounts of the head config: NEVER backed up (the GUI says so) */
  hostPaths: { containerSlug: Slug; mountPath: string; path: string; readOnly: boolean }[];
}
/** POST /_opm/api/sites/{siteSlug}/volumes/{volumeSlug}/reset (round E): typed ACCEPT after the 60 s wait. */
export interface ResetVolumeResponse {
  siteSlug: Slug;
  volumeSlug: string;
  /** the manual snapshot of this volume taken first (note "before reset of <volumeSlug>") */
  snapshotId: Ulid;
  /** entries written (files seed: entries; file seed: 1; archive: regular files) */
  entries: number;
}
// ───────────────────────────────────────────────────────────── §21 #96 volume manager (agent GUI Tools → Volumes)
/**
 * Long volume actions run as an operation on the site that holds the volume (one at a time per volume; refused while an
 * update runs and vice versa). The GUI polls `getVolumeOp`. Containers that mount the volume and run are stopped in
 * REVERSE start order and started again in start order (§15.5) — also when the action fails.
 */
export type VolumeOpKind = 'empty' | 'copy' | 'restore';
export type VolumeOpStepName = 'stop' | 'snapshot' | 'copy' | 'empty' | 'restore' | 'start';
export interface VolumeOpStep {
  name: VolumeOpStepName;
  state: 'pending' | 'running' | 'done' | 'skipped' | 'failed';
  detail: string | null;
  startedAt: Ts | null;
  finishedAt: Ts | null;
}
export interface VolumeOpTarget {
  kind: 'path' | 'volume';
  /** absolute path on the server (the volume directory / file for kind volume) */
  path: string;
  volumeSlug: string | null;
}
export interface VolumeOpSource {
  kind: 'copy' | 'upload' | 'snapshot'; // §21 #112 snapshot = a backup of the volume (ref = snapshotId)
  /** copyId | uploadId | snapshotId */
  ref: string;
  path: string;
}
export interface VolumeOp {
  opId: Ulid;
  siteSlug: Slug;
  volumeSlug: string;
  volumeKind: 'dir' | 'file';
  kind: VolumeOpKind;
  actor: string;
  startedAt: Ts;
  finishedAt: Ts | null;
  /** the running step, or done | failed */
  phase: VolumeOpStepName | 'queued' | 'done' | 'failed';
  ok: boolean | null;
  error: string | null;
  steps: VolumeOpStep[];
  progress: { bytesDone: number; bytesTotal: number | null; files: number };
  /** containers stopped for the action (started again at the end) */
  stopped: string[];
  target: VolumeOpTarget | null; //  copy
  source: VolumeOpSource | null; //  restore
  snapshotId: Ulid | null; //        empty (optional) / restore (always)
  copyId: string | null; //          copy → the entry in the copies list
  resultBytes: number | null;
  durationMs: number | null;
  /** warnings (ownership could not be kept, xattrs unsupported, …) */
  warnings: string[];
  fastForward?: FastForwardUse | null;
}
export interface VolumeEmptyRequest {
  intentId: Ulid;
  /** the volume slug, typed */
  typed: string;
  /** take a snapshot of this volume first (default true in the GUI) */
  snapshot: boolean;
}
export type VolumeCopyTargetInput =
  | { kind: 'path'; /** absolute, or "./…" relative to agentHome; empty/absent = <agentHome>/volume-copies/<volume>-<yyyymmdd-hhmmss> */ path?: string | null }
  | { kind: 'volume'; volumeSlug: string; /** required when the target volume is not empty: its slug, typed */ overwrite?: string | null };
export interface VolumeCopyRequest {
  target: VolumeCopyTargetInput;
  /** stop the containers that mount the SOURCE (and a target volume) during the copy (GUI default: on for databases) */
  stopContainers: boolean;
  /** keep owner / group / mode / mtime / xattrs (default true) */
  preserve: boolean;
}
export interface VolumeRestoreRequest {
  intentId: Ulid;
  typed: string; //                 the volume slug
  /** §21 #112 kind 'snapshot': any snapshot on this site that holds the volume (VolumeBackupsResponse.items) */
  source: { kind: 'copy'; copyId: string } | { kind: 'upload'; uploadId: string } | { kind: 'snapshot'; snapshotId: string };
}
export interface VolumeRestoreUpload {
  uploadId: string;
  sizeBytes: number;
  /** regular files + dirs + symlinks in the archive (checked: no absolute paths, no "..", no devices) */
  entries: number;
  expiresAt: Ts; //                 1 h
}
export interface VolumeSnapshotRequest {
  note?: string;
}
export interface VolumeSnapshotResponse {
  snapshotId: Ulid;
  sizeBytes: number;
}
/** A copy made by "Copy to path" (listed until deleted; the data itself lives at `path`). */
export interface VolumeCopy {
  copyId: string;
  siteSlug: Slug;
  volumeSlug: string;
  volumeKind: 'dir' | 'file';
  path: string;
  /** inside <agentHome>/volume-copies/ (else a custom path the operator chose) */
  inCopiesDir: boolean;
  exists: boolean;
  sizeBytes: number | null;
  files: number | null;
  createdAt: Ts;
  createdBy: string;
  opId: Ulid | null;
}
export interface DeleteVolumeCopyRequest {
  /** the copyId, typed */
  confirm: string;
}
export const VOLUME_COPIES_DIR = 'volume-copies';
/** at most this many volume actions (empty / copy / restore / snapshot / restore upload) per session per minute */
export const VOLUME_OP_RATE_PER_MINUTE = 6;
/** restore upload (.tar.gz) limit — bigger data: copy it to the server and restore from a copy path */
export const VOLUME_RESTORE_UPLOAD_MAX = 2 * 1024 * 1024 * 1024;

export interface PatchSnapshotRequest {
  note: string;
}
export interface ContainerLogsResponse {
  siteSlug: Slug;
  containerSlug: Slug;
  serviceSlug: Slug;
  lines: { ts: Ts; stream: 'stdout' | 'stderr'; text: string }[];
}
export interface DownloadActivitiesQuery {
  from?: number; // seq (inclusive), default 1
  to?: number; //   seq (inclusive), default last
  includeDebug?: boolean;
}
export interface SaveTransferRequest {
  path: string; // absolute path on the server; must not exist
}
export interface SendFromPathRequest {
  path: string; // absolute path of a regular file on the server (§21 #100 safety policy: hostFsBlockReason)
  note?: string;
}
/** §21 #100: one entry of the server filesystem picker (GET …/transfers/fs) */
export interface HostFsEntry {
  name: string;
  path: string; //                 absolute
  type: 'dir' | 'file' | 'symlink' | 'other';
  /** symlinks: what the target is (`missing` = dangling); null for other types */
  targetType: 'dir' | 'file' | 'other' | 'missing' | null;
  linkTarget: string | null;
  sizeBytes: number | null; //     files (and symlinks to files): size; dirs / other: null
  mtime: Ts | null; //             null when lstat failed
  mode: string; //                 -rw-r--r-- ("" when lstat failed)
  hidden: boolean; //              name starts with "."
  /** the agent can read the file / list the directory (false = permission denied) */
  readable: boolean;
  /** why this entry cannot be sent (safety policy), null = allowed */
  blocked: string | null;
}
export interface HostFsListResponse {
  path: string; //                 cleaned absolute directory
  parent: string | null; //        null at "/"
  entries: HostFsEntry[]; //       directories first, then by name (case-insensitive)
  truncated: boolean; //           more than HOST_FS_MAX_ENTRIES entries
  hiddenCount: number; //          hidden entries left out (hidden=false)
  agentUser: string; //            the user the agent reads as (root on a standard install)
  /** the directory itself lies in a blocked area (its files cannot be sent) */
  blocked: string | null;
}
export const HOST_FS_MAX_ENTRIES = 5000;
export interface TunnelStartRequest {
  userAgent: string;
}
export interface TunnelLeaseResponse {
  lease: TunnelLease;
}
export interface TunnelStateResponse {
  lease: TunnelLease | null; // the active lease of the deployment (any site), if any
  holderSite: Slug | null;
  pending: number; //          queued requests on this site
  encrypted: boolean; //       §9.10 the lease (without a lease: this agent's setting) carries ciphertext only
}
export interface TunnelNextResponse {
  requests: TunnelRequest[];
}
export interface TunnelCompleteRequest {
  leaseId: Ulid;
  parts: number;
  sha256: Sha256;
}
export interface CertRenewResponse {
  status: 'renewed' | 'failed';
  cert: CertStatus;
}

// ═════════════════════════════════════════════════════════════ peer API (/_opm/peer/*)
export interface PeerRevisionResponse {
  revision: Revision;
}
export interface PeerProposalPush {
  proposal: Proposal;
  revision: Revision;
}
export interface PeerRelayRequest {
  /** the inner, already signed cloud request of the asking site */
  request: { method: string; path: string; headers: Record<string, string>; bodyB64: string | null };
}
export interface PeerActorsResponse {
  actors: ActorRow[];
}
export interface PeerSnapshotPartQuery {
  includeImages?: boolean;
}
