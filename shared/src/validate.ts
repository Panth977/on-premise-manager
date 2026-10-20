/**
 * Config validation shared by the cloud backend, the cloud frontend and (ported) the agent.
 * Normative: SPEC-final.md §10. Every issue has a stable code (VALIDATION_CODES).
 *
 * Levels: every check is "save" level (structural — the API refuses to store the document) or "publish" level
 * (the document can be stored as a draft, but it cannot be published / staged). validateConfig() returns
 * publish-level issues as warnings in mode "template" / "draft" and as errors in mode "publish" / "agent".
 */
import {
  autoHostPlaceholder,
  checkExpr,
  exprLineCol,
  exprRefs,
  exprFilters,
  FILTER_MIN_AGENT,
  isExpr,
  parseTemplate,
  resolve,
  resolveNumberField,
  resolveContainer,
  ExprError,
  DEPLOY_PLACEHOLDER,
  isDeferredValue,
  type ContainerResolution,
  type EnvSource,
  type ExprContext,
  type ExprField,
  type ExprValue,
  type MountSource,
  type PeerEnv,
  type ResolveHooks,
  type ResolveResult,
} from './expr.js';
import { inferDepends, startOrder } from './depends.js';
import { checkEdgeRegex, EDGE_DEFAULTS, HOP_BY_HOP_HEADERS, emptyResponseHeaders } from './edge-resolve.js';
import type {
  Config,
  Container,
  ContainerMount,
  ContainerPort,
  VolumeSource,
  Edge,
  EdgeHost,
  EdgeRoute,
  EdgeProxyAction,
  EdgeResponseHeaders,
  FileDef,
  HealthCheck,
  ImageEnv,
  ImagePort,
  ImageVolume,
  ImageVolumeKind,
  SecretDef,
  Site,
  SiteConfig,
  SiteVolume,
  SiteVolumeKind,
  VolumeSeed,
  VolumePersist,
  VolumeTree,
  StorageLocation,
  ValidationIssue,
  Variable,
  ImageKind,
} from './types.js';
import { SEED_ARCHIVE_MAX } from './types.js';
import { VOLUME_BACKUP_SCHEDULES } from './types.js'; // §21 #112
import { canonicalize } from './canonical.js';
import { VOLUME_CONTENT_LIMITS, compileExcludes, excludePatternError, formatContentBytes, isExcluded, normalizeVolumeTree, volumeTreeIssues } from './volume-content.js';
import { DEVTOOL_KINDS, DEVTOOL_PRESETS_MAX, DEVTOOL_SLUG_RE, DEVTOOL_TARGETS, normalizeDevToolPreset, siteDevTools, type DevToolCredential, type DevToolKind, type DevToolPreset } from './devtools.js';

// ───────────────────────────────────────────────────────────── patterns + limits
export const RE = {
  slug: /^[a-z][a-z0-9-]{1,39}$/,
  /** SiteVolume.volumeSlug (VolumeSource volume.volumeSlug): a declared data folder of a site (suggested `<containerSlug>-<imageVolume>`) */
  volumeSlug: /^[a-z0-9][a-z0-9_.-]{0,99}$/,
  id: /^[A-Za-z0-9]{20}$/,
  /** docker tag (inside an image reference) */
  tag: /^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$/,
  /** OPM image version = Image document id (v1, v2, 3.1.0 …) */
  version: /^[a-z0-9][a-z0-9._-]{0,63}$/,
  envName: /^[A-Z_][A-Z0-9_]*$/,
  /** service / template / tag icons: "lucide:<name>", "upload:<iconId>" or a legacy bare Lucide name (null = default icon) */
  icon: /^(lucide:[a-z0-9-]+|upload:[A-Za-z0-9]{20}|[a-z0-9-]+)$/,
  /** Account.img / Deployment.image: an icon reference or a legacy https URL */
  picture: /^(lucide:[a-z0-9-]+|upload:[A-Za-z0-9]{20}|https:\/\/\S+)$/,
  color: /^#[0-9a-f]{6}$/,
  sha256: /^sha256:[0-9a-f]{64}$/,
  ulid: /^[0-9A-HJKMNP-TV-Z]{26}$/,
  routeId: /^[a-z0-9][a-z0-9-]{0,62}$/,
  email: /^[^@\s]+@[^@\s]+\.[^@\s]+$/,
  e164: /^\+[1-9]\d{6,14}$/,
  repository: /^[a-z0-9]+([._-][a-z0-9]+)*(\/[a-z0-9]+([._-][a-z0-9]+)*)*$/,
  mime: /^[a-z0-9][a-z0-9.+-]*\/[a-z0-9][a-z0-9.+-]*$/,
  httpsUrl: /^https:\/\/[^\s/?#]+$/,
  httpsUrlAny: /^https:\/\/\S+$/,
  headerName: /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/,
  ipv4: /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/,
  dnsName: /^(\*\.)?[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$/i,
  activityType: /^[a-z]+(-[a-z]+)*(\.[a-z-]+)*$/,
};
/** Site slugs that would collide with agent GUI routes (/_opm/app/{siteSlug}/…). */
export const RESERVED_SITE_SLUGS = ['backups', 'files', 'internet', 'revisions', 'secrets', 'sites', 'updates', 'variables'] as const;
/** Container slugs that would collide with GUI routes (…/containers/new). */
export const RESERVED_CONTAINER_SLUGS = ['new'] as const;
export const LIMITS = {
  label: 80,
  description: 2000,
  value: 4096,
  variables: 200,
  secrets: 100,
  files: 100,
  sites: 20,
  containersPerSite: 50,
  envPerContainer: 200,
  volumesPerContainer: 50,
  volumesPerSite: 100,
  portsPerImage: 50,
  hosts: 50,
  routes: 200,
  enumOptions: 100,
  imageEnvList: 50,
  secretBytes: 8192,
  fileMaxBytes: 52428800,
  notFoundPageBytes: 262144,
  commandItems: 100,
  summary: 200,
  note: 500,
};
export const EDGE_METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS'] as const;

export const VALIDATION_CODES: Record<string, string> = {
  V_REQUIRED: 'A required field is missing or empty.',
  V_TYPE: 'A field has the wrong JSON type.',
  V_FORMAT: 'A string does not match its pattern (slug, tag, email, URL, …).',
  V_ENUM: 'A value is not one of the allowed values.',
  V_RANGE: 'A number is out of its range.',
  V_LENGTH: 'A string or list is too short or too long.',
  V_DUPLICATE: 'An id / slug / name is used twice where it must be unique.',
  V_RESERVED: 'A reserved value (site slug, container slug "new", /_opm/ prefix).',
  V_REF: 'A reference to something that does not exist (service, tag, image, file, secret, site, page, container).',
  V_CYCLE: 'Dependencies form a cycle.',
  V_DEPENDS_SUGGESTED: 'info (§21 #93): a container mentions another container of the site as a hostname — add it to Depends on?',
  V_START_ORDER_CYCLE: 'warning (§21 #93): implicit / suggested references form a cycle; those containers start in the declared order.',
  V_IMMUTABLE: 'An immutable field was changed.',
  V_TEMPLATE_SECRET_VALUE: 'A template holds a secret value (never allowed).',
  V_VALUE_TYPE: 'A variable value does not match the variable type.',
  V_VALUE_RULE: 'A variable value breaks min / max / pattern / options.',
  V_VAR_REQUIRED: 'A required variable has no value (publish).',
  V_SECRET_REQUIRED: 'A required secret has no value (publish).',
  V_FILE_CONTENT: 'A mounted file has no content, or content is larger than maxBytes.',
  V_IMAGE_NOT_READY: 'The image import is not ready (publish).',
  V_ENV_ONE_OF: 'An env item needs exactly one of value / secretRef.',
  V_ENV_REQUIRED: 'A required image env has no default and no container value (publish).',
  V_ENV_SECRET: 'An image env marked secret must use secretRef or a secret.* expression (publish).',
  V_OPTION: 'An env value is not one of the allowed values (ImageEnv.options) of the image env (image default: save; container value after resolution: publish).',
  V_IMAGE_ENV: 'Image env rules (secret with a default or options, options size).',
  V_HEALTHCHECK: 'Health check rules.',
  V_PORT_CONFLICT: 'Two listeners of a site use the same host port.',
  V_PORT_EDGE: 'A container publishes 80, 443 or an edge port (add an edge route instead).',
  V_VOLUME: 'Volume rules (site volume fields; one row per image volume, known image volume, source kind, host path).',
  V_VOLUME_UNDECLARED: 'A mount uses a volume that is not declared in the site volumes.',
  V_VOLUME_UNUSED: 'A declared site volume is mounted by no container (warning; its data stays on disk).',
  V_VOLUME_SEED: 'Volume content rules (kind match, entry paths, files exist, archive size / name; a persist volume: a literal tree within the persisted limits, §21 #117).',
  V_VOLUME_SEED_FILE_REF: 'A volume takes its content from Files (legacy seed files / file): Files are static and read-only and are never volume content (§21 #101).',
  V_VOLUME_CONTENT_PATH: 'A path of a volume\'s own content is not a safe relative path (no "." / ".." / empty segments, no control characters, ≤ 1024 bytes).',
  V_TOO_LARGE: 'A file or the whole content is over its size limit.',
  V_VOLUME_PERSIST: 'Persist-to-cloud settings (folder volumes only; exclude patterns; 1–200 MiB size cap).',
  V_FILE_IN_WRITABLE_VOLUME: 'A File is mounted inside a writable volume / host mount of the same container (Files are static and read-only and never sit on a writable volume, §21 #101).',
  V_LOCAL_VOLUME_CONTENT: 'Local edit: a volume\'s persist-to-cloud setting is cloud configuration (§21 #113: everything else changes on a site).',
  V_VOLUME_CONTENT_EXCLUDED: 'A persisted version or the committed content of a persist volume holds a path the volume\'s exclude patterns leave out (it could never be snapshotted).',
  V_BLOB_MISSING: 'Content blobs referenced by a tree were not uploaded.',
  V_VOLUME_KIND: 'A single-file volume must be mounted at a file path (no trailing "/"); SiteVolume.kind / ImageVolume.kind is dir | file; a file image volume needs a file path.',
  V_VOLUME_KIND_IMAGE: 'A single-file data volume on an image volume the image declares as a Folder (ImageVolume.kind dir): it replaces the whole folder with one file (warning).',
  V_VOLUME_KIND_MISMATCH: 'A folder data volume on an image volume the image declares as a File (ImageVolume.kind file): Docker cannot mount a folder on a file path (error).',
  V_FILE_VOLUME_RENAME: 'A single-file data volume mounted read-write: apps that save by rename (e.g. Node-RED flows.json) fail with EBUSY (info, once per mount).',
  V_VOLUME_NOT_MOUNTED: 'An image volume has no row: it is not mounted and its data is lost when the container is recreated (warning).',
  V_READONLY: 'A file or secret mount must be read-only.',
  V_LOCATION: 'A storage location (file / site volume on the server) is not "./relative" without "..", not a safe absolute path, uses a reserved agent directory, or collides with / is nested in another location of the site.',
  V_JOB_RESTART: 'A job must use restart never or on-failure.',
  V_MOUNT: 'Mount path rules (absolute, not "/", unique).',
  V_PATH: 'A mount path with {{ }} must start with "/" and resolve to an absolute path that is not "/" (no spaces, no "..").',
  V_HOST: 'A resolved site host is not an IP or a DNS name (publish).',
  V_HOST_AUTO: 'The site host expression has no value (an OPTIONAL variable is empty), so the site uses its auto-detected address (warning; draft / publish only, never blocks).',
  V_VAR_OPTIONAL_NULL: 'Template: an optional variable has no value and no template default (info, once per variable).',
  V_FILE_RENDER: 'A file with render "expressions" does not resolve for a container that mounts it (message: file:line:col).',
  V_FILE_JSON: 'A rendered application/json file is not valid JSON (staging error).',
  V_EDGE_HOST: 'Edge host rules (format, duplicates, TLS mode needs).',
  V_EDGE_TLS: 'Edge TLS rules.',
  V_EDGE_ROUTE: 'Edge route rules.',
  V_EDGE_SHADOW: 'Deprecated (round E): no longer raised — longest-prefix matching cannot shadow; identical matches are V_DUPLICATE.',
  V_EDGE_REGEX: 'A route / rewrite regex is invalid or uses a feature RE2 (agent) and JavaScript (GUIs) do not share.',
  V_EDGE_HEADER: 'Edge header rules (request / response headers: names, values, always list).',
  W_RESPONSE_HEADER_AS_REQUEST: 'A proxy route SETS a request header that only means something on responses (Cache-Control public/max-age/no-store, Expires, Pragma, Set-Cookie, CSP, HSTS, X-Frame-Options, Access-Control-Allow-*): it goes to the container, not the browser. Fix: move it to the response headers (warning).',
  V_EDGE_HOP: 'A hop-by-hop header (Connection, Upgrade, …) is set: the edge manages it itself, the value is ignored (warning).',
  V_EDGE_BODY: 'Edge body size limit rules (maxBodyBytes).',
  V_EDGE_NO_HOSTS: 'The edge has no hosts (warning).',
  V_EDGE_TLS_NO_HTTPS: 'A host has TLS but the edge has no httpsPort (warning).',
  V_EDGE_ACME_INTERNET: 'ACME http-01 / tls-alpn-01 needs internet and inbound reachability (warning).',
  V_EDGE_TARGET_DISABLED: 'A proxy route targets a disabled container → 503 (warning).',
  V_NO_SITES: 'A config needs at least one site (publish).',
  V_NO_CHANGE: 'A publish or local proposal is identical to the head (publish).',
  V_LOCAL_SITES: 'Local edit: sites cannot be added or removed on a site.',
  V_REFERENCE: 'A docker image reference is not valid ([host/]repository[:tag][@sha256:digest]).',
  V_PATTERN: 'A regular expression is not valid RE2.',
  V_TOO_MANY: 'Too many items.',
  // §21 #97 dev-tool presets (SiteConfig.devTools; validateSiteDevTools, Go mirror validate/devtools.go)
  V_DEVTOOL_SLUG: 'A dev-tool preset slug does not match ^[a-z0-9][a-z0-9-]{0,62}$.',
  V_DEVTOOL_DUP: 'A dev-tool preset slug is used twice on the site.',
  V_DEVTOOL_MAX: 'A site has more than 50 dev-tool presets.',
  V_DEVTOOL_KIND: 'A dev-tool preset kind is not http | postgres | sqlite | cassandra | redis | mqtt.',
  V_DEVTOOL_TARGET: 'A dev-tool preset target kind is not allowed for the preset kind, or a target field is missing / invalid (container + port name, host + port, http(s) URL, volume + path, container + path).',
  V_DEVTOOL_CONTAINER: 'A dev-tool preset targets a container that does not exist on the site (error) or is disabled (warning).',
  V_DEVTOOL_PORT: 'A dev-tool preset port name is not a port of the image of the target container.',
  V_DEVTOOL_VOLUME: 'A SQLite preset file is not in a declared folder volume of the site with a safe relative path (no "..", no leading "/"), or a container path is not absolute.',
  V_DEVTOOL_LITERAL: 'Warning (§21 #103): a dev-tool preset password / token is a static value — stored in plain text in the config; anyone who can read the config sees it. Use a secret for production. Needs agent 0.13.1+ on the site.',
  V_DEVTOOL_SECRET: 'A dev-tool preset references a secret that does not exist.',
  V_DEVTOOL_VAR: 'A dev-tool preset references a variable that does not exist.',
  V_DEVTOOL_ENV: 'A dev-tool preset references containers.<c>.env.<NAME> and NAME is not an env var of that container (image env or container env) (§21 #99).',
  V_DEVTOOL_PLAIN_ENV: 'A dev-tool preset password / token references a container env that holds a plain (non-secret) value (warning, §21 #99).',
  V_DEVTOOL_AUTH: 'Dev-tool auth rules (postgres username; cassandra username + password both or none; redis database 0–15; http basic → username, bearer / api-key → token; mqtt clientId ≤ 65535 bytes); unused fields → warning.',
  V_DEVTOOL_OPTION: 'Dev-tool option rules (timeoutSec 1–3600, maxRows 1–10000, keepAliveSec 0–65535, basePath starts with "/", header names, enums).',
};

// ───────────────────────────────────────────────────────────── issue collector
type Level = 'save' | 'publish';
export type ValidateMode = 'template' | 'draft' | 'publish' | 'agent';
export class Issues {
  readonly list: ValidationIssue[] = [];
  constructor(readonly mode: ValidateMode = 'publish') {}
  err(path: string, code: string, message: string, level: Level = 'save') {
    if (code === 'E_DEFERRED') return; // set at deployment (required variable / secret): never an issue
    const strict = this.mode === 'publish' || this.mode === 'agent';
    this.list.push({ path, code, message, severity: level === 'save' || strict ? 'error' : 'warning' });
  }
  warn(path: string, code: string, message: string) {
    if (code === 'E_DEFERRED') return;
    this.list.push({ path, code, message, severity: 'warning' });
  }
  info(path: string, code: string, message: string) {
    this.list.push({ path, code, message, severity: 'info' });
  }
  get errors() {
    return this.list.filter((x) => x.severity === 'error');
  }
  get ok() {
    return this.errors.length === 0;
  }
}

const isInt = (v: unknown): v is number => typeof v === 'number' && Number.isInteger(v);
const isStr = (v: unknown): v is string => typeof v === 'string';
const utf8Len = (s: string) => new TextEncoder().encode(s).length;

export function checkLabel(v: unknown, path: string, is: Issues, max = LIMITS.label) {
  if (!isStr(v) || v.trim() === '') return is.err(path, 'V_REQUIRED', 'required');
  if (v.length > max) is.err(path, 'V_LENGTH', `at most ${max} characters`);
}
export function checkDescription(v: unknown, path: string, is: Issues) {
  if (v === undefined) return;
  if (!isStr(v)) return is.err(path, 'V_TYPE', 'must be a string');
  if (v.length > LIMITS.description) is.err(path, 'V_LENGTH', `at most ${LIMITS.description} characters`);
}
export function checkSlug(v: unknown, path: string, is: Issues) {
  if (!isStr(v) || !RE.slug.test(v)) is.err(path, 'V_FORMAT', `"${String(v)}" is not a slug (^[a-z][a-z0-9-]{1,39}$)`);
}
/** Service / template / tag icon: null, "lucide:<name>", "upload:<iconId>" or a legacy bare Lucide name. */
export function checkIcon(v: unknown, path: string, is: Issues) {
  if (v === null || v === undefined) return;
  if (!isStr(v) || !RE.icon.test(v)) is.err(path, 'V_FORMAT', 'icon must be null, lucide:<name> or upload:<iconId>');
}
/** Account.img / Deployment.image: null, an icon reference or an https URL. */
export function checkPicture(v: unknown, path: string, is: Issues) {
  if (v === null || v === undefined) return;
  if (!isStr(v) || !RE.picture.test(v)) is.err(path, 'V_FORMAT', 'must be null, lucide:<name>, upload:<iconId> or an https URL');
}
/** "lucide:x" / "x" → { kind: 'lucide', name } · "upload:id" → { kind: 'upload', id } · URL / data URL → { kind: 'url' } · null → null */
export function parseIcon(v: string | null | undefined): { kind: 'lucide'; name: string } | { kind: 'upload'; id: string } | { kind: 'url'; url: string } | null {
  if (!v) return null;
  if (v.startsWith('lucide:')) return { kind: 'lucide', name: v.slice(7) };
  if (v.startsWith('upload:')) return { kind: 'upload', id: v.slice(7) };
  if (v.startsWith('https://') || v.startsWith('data:image/')) return { kind: 'url', url: v };
  if (/^[a-z0-9-]+$/.test(v)) return { kind: 'lucide', name: v };
  return null;
}
/** The icon ids ("upload:<id>") a value references (for usage checks). */
export function iconUploadId(v: unknown): string | null {
  return isStr(v) && v.startsWith('upload:') ? v.slice(7) : null;
}
export function checkPort(v: unknown, path: string, is: Issues, min = 1) {
  if (!isInt(v) || v < min || v > 65535) is.err(path, 'V_RANGE', `${String(v)} must be an integer ${min}–65535`);
}
/** A resolved mount path: absolute, not "/", no whitespace, no "..", ≤ 1024. */
export function isSafeMountPath(v: string): boolean {
  return v.startsWith('/') && v !== '/' && !/\s/.test(v) && !v.includes('..') && v.length <= 1024;
}
/**
 * Mount path as written (save level). Literal → V_MOUNT rules. With {{ }} → must start with a literal "/" (V_PATH),
 * valid expression for field mountPath; the resolved value is checked at publish (V_PATH).
 */
function checkMountPath(v: unknown, path: string, is: Issues) {
  if (isStr(v) && isExpr(v)) {
    if (!v.startsWith('/')) is.err(path, 'V_PATH', 'mount path must be absolute: start with "/" (e.g. /app/config/{{ container.env.NODE_ENV }}.ts)');
    if (v.length > 1024) is.err(path, 'V_LENGTH', 'at most 1024 characters');
    checkExprField(v, 'mountPath', path, is);
    return;
  }
  if (!isStr(v) || !isSafeMountPath(v)) is.err(path, 'V_MOUNT', 'mount path must be absolute, not "/", without spaces or ".."');
}
/** Reject regex features RE2 does not have (look-around, back-references) and invalid patterns. */
export function checkRe2(pattern: unknown, path: string, is: Issues) {
  if (!isStr(pattern)) return is.err(path, 'V_TYPE', 'pattern must be a string');
  if (/\(\?[=!]|\(\?<[=!]|\\[1-9]/.test(pattern)) return is.err(path, 'V_PATTERN', 'look-around and back-references are not RE2');
  try {
    new RegExp(pattern);
  } catch (e) {
    is.err(path, 'V_PATTERN', `invalid regular expression: ${(e as Error).message}`);
  }
}
function dupes<T>(items: T[], key: (t: T) => string): Set<string> {
  const seen = new Set<string>();
  const d = new Set<string>();
  for (const it of items) {
    const k = key(it);
    if (seen.has(k)) d.add(k);
    seen.add(k);
  }
  return d;
}
function checkExprField(v: string, field: ExprField, path: string, is: Issues) {
  for (const e of checkExpr(v, field)) is.err(path, e.code, `${e.message} (at ${e.pos})`);
}

// ───────────────────────────────────────────────────────────── images
export function validateImagePorts(ports: unknown, is: Issues, base = 'ports') {
  if (!Array.isArray(ports)) return is.err(base, 'V_TYPE', 'must be a list');
  if (ports.length > LIMITS.portsPerImage) is.err(base, 'V_TOO_MANY', `at most ${LIMITS.portsPerImage}`);
  const d = dupes(ports as ImagePort[], (p) => p.name);
  (ports as ImagePort[]).forEach((p, i) => {
    const at = `${base}[${p?.name ?? i}]`;
    checkSlug(p.name, `${at}.name`, is);
    if (d.has(p.name)) is.err(`${at}.name`, 'V_DUPLICATE', `port name "${p.name}" is used twice`);
    if (typeof p.port === 'number') checkPort(p.port, `${at}.port`, is);
    else if (isStr(p.port) && isExpr(p.port)) checkExprField(p.port, 'imagePort', `${at}.port`, is);
    else is.err(`${at}.port`, 'V_TYPE', 'port must be a number or one whole {{ }} block');
    if (p.protocol !== 'tcp' && p.protocol !== 'udp') is.err(`${at}.protocol`, 'V_ENUM', 'protocol must be tcp | udp');
    checkDescription(p.description, `${at}.description`, is);
  });
}
/**
 * Normalize an ImageEnv as stored / sent. Legacy `value: string[]` (allowed values, first = default) becomes
 * `options` + `value` = the first item; missing options → null. Pure.
 */
export function normalizeImageEnv(e: ImageEnv | (Omit<ImageEnv, 'value'> & { value?: unknown })): ImageEnv {
  const raw = e as ImageEnv & { value?: unknown };
  const out: ImageEnv = { name: raw.name, required: raw.required, secret: raw.secret, description: raw.description ?? '', options: raw.options ?? null };
  if (Array.isArray(raw.value)) {
    const list = (raw.value as unknown[]).filter(isStr);
    out.options = raw.options ?? list;
    if (list.length) out.value = list[0];
  } else if (raw.value !== undefined && raw.value !== null) out.value = raw.value as string;
  return out;
}
export function validateImageEnv(env: unknown, is: Issues, base = 'env') {
  if (!Array.isArray(env)) return is.err(base, 'V_TYPE', 'must be a list');
  env = (env as ImageEnv[]).map((e) => (e && typeof e === 'object' ? normalizeImageEnv(e) : e));
  const d = dupes(env as ImageEnv[], (e) => e.name);
  (env as ImageEnv[]).forEach((e, i) => {
    const at = `${base}[${e?.name ?? i}]`;
    if (!isStr(e.name) || !RE.envName.test(e.name)) is.err(`${at}.name`, 'V_FORMAT', `"${String(e.name)}" does not match ^[A-Z_][A-Z0-9_]*$`);
    if (d.has(e.name)) is.err(`${at}.name`, 'V_DUPLICATE', `env "${e.name}" is defined twice`);
    if (typeof e.required !== 'boolean') is.err(`${at}.required`, 'V_TYPE', 'required must be true / false');
    if (typeof e.secret !== 'boolean') is.err(`${at}.secret`, 'V_TYPE', 'secret must be true / false');
    const opts = e.options;
    if (opts !== null && opts !== undefined) {
      if (!Array.isArray(opts) || opts.some((x) => !isStr(x))) is.err(`${at}.options`, 'V_TYPE', 'options must be a list of strings or null');
      else {
        if (opts.length < 1 || opts.length > LIMITS.imageEnvList) is.err(`${at}.options`, 'V_LENGTH', `a choice needs 1–${LIMITS.imageEnvList} options`);
        if (new Set(opts).size !== opts.length) is.err(`${at}.options`, 'V_DUPLICATE', 'options must be unique');
        if (opts.some((x) => x === '' || x.length > LIMITS.value)) is.err(`${at}.options`, 'V_LENGTH', `options must be 1–${LIMITS.value} characters`);
        if (opts.some((x) => isExpr(x))) is.err(`${at}.options`, 'V_FORMAT', 'options are plain values (no {{ }})');
        if (e.secret) is.err(`${at}.options`, 'V_IMAGE_ENV', 'a secret env cannot have options');
      }
    }
    if (e.value !== undefined) {
      if (e.secret) is.err(`${at}.value`, 'V_IMAGE_ENV', 'a secret env cannot have a default value');
      if (isStr(e.value)) {
        if (e.value.length > LIMITS.value) is.err(`${at}.value`, 'V_LENGTH', `at most ${LIMITS.value} characters`);
        if (Array.isArray(opts)) {
          if (!opts.includes(e.value)) is.err(`${at}.value`, 'V_OPTION', `the default "${e.value}" must be one of the options (${opts.join(', ')})`);
        } else checkExprField(e.value, 'imageEnv', `${at}.value`, is);
      } else is.err(`${at}.value`, 'V_TYPE', 'value must be a string');
    }
    checkDescription(e.description, `${at}.description`, is);
  });
}
/**
 * Default kind of an image volume from its mount path (§21 #83): the last path segment (with `{{ }}` blocks replaced by
 * "x") has an extension — a name, a dot, 1–10 letters / digits with at least one letter, not ".d" (conf.d is a folder) —
 * → 'file' (/data/flows.json, /data/settings.js, /app/env/{{ container.env.NODE_ENV }}.ts); anything else (no extension,
 * trailing "/", dot files like /root/.config, versions like /opt/app-1.2) → 'dir'. Pure; the GUIs' "Auto" and the
 * migration use it.
 */
export function inferImageVolumeKind(mountPath: unknown): ImageVolumeKind {
  if (!isStr(mountPath) || mountPath.endsWith('/')) return 'dir';
  const plain = mountPath.replace(/\{\{[\s\S]*?\}\}/g, 'x');
  const last = plain.slice(plain.lastIndexOf('/') + 1);
  const m = /^[^.].*\.([A-Za-z0-9]{1,10})$/.exec(last);
  return m && /[A-Za-z]/.test(m[1]!) && m[1]!.toLowerCase() !== 'd' ? 'file' : 'dir';
}
/** The kind of an image volume: the stored value, or (legacy documents / revision catalogs without the key) inferred from the path. */
export function imageVolumeKind(v: Pick<ImageVolume, 'mountPath'> & { kind?: unknown }): ImageVolumeKind {
  return v.kind === 'dir' || v.kind === 'file' ? v.kind : inferImageVolumeKind(v.mountPath);
}
/** An ImageVolume as stored: kind always explicit (missing → inferred from the path), description ''. Idempotent; an invalid kind is kept for the validator. */
export function normalizeImageVolume(v: Omit<ImageVolume, 'kind'> & { kind?: unknown }): ImageVolume {
  return { name: v.name, mountPath: v.mountPath, kind: (v.kind === undefined || v.kind === null ? inferImageVolumeKind(v.mountPath) : v.kind) as ImageVolumeKind, description: v.description ?? '' };
}
/** Label of a kind in both GUIs and in issue messages. */
export const VOLUME_KIND_LABEL: Record<ImageVolumeKind, string> = { dir: 'Folder', file: 'File' };
/** Info text for a read-write single-file volume mount (V_FILE_VOLUME_RENAME). */
export const FILE_VOLUME_RENAME_INFO =
  'apps that save by rename (e.g. Node-RED flows.json) fail on single-file mounts (EBUSY) — use a folder volume seeded with the file';
export function validateImageVolumes(vols: unknown, is: Issues, base = 'volumes') {
  if (!Array.isArray(vols)) return is.err(base, 'V_TYPE', 'must be a list');
  const d = dupes(vols as ImageVolume[], (v) => v.name);
  const dm = dupes(vols as ImageVolume[], (v) => v.mountPath); // literal duplicates; resolved ones are checked per container
  (vols as ImageVolume[]).forEach((v, i) => {
    const at = `${base}[${v?.name ?? i}]`;
    checkSlug(v.name, `${at}.name`, is);
    if (d.has(v.name)) is.err(`${at}.name`, 'V_DUPLICATE', `volume "${v.name}" is defined twice`);
    checkMountPath(v.mountPath, `${at}.mountPath`, is);
    if (dm.has(v.mountPath)) is.err(`${at}.mountPath`, 'V_MOUNT', `mount path ${v.mountPath} is used twice`);
    if (v.kind !== undefined && v.kind !== 'dir' && v.kind !== 'file') is.err(`${at}.kind`, 'V_ENUM', 'kind must be dir (Folder) | file (File)');
    else if (imageVolumeKind(v) === 'file' && isStr(v.mountPath) && v.mountPath.endsWith('/'))
      is.err(`${at}.mountPath`, 'V_VOLUME_KIND', `image volume ${v.name} is a File: its mount path must not end with "/" (or set it to Folder)`);
    checkDescription(v.description, `${at}.description`, is);
  });
}
export function validateHealthCheck(hc: unknown, portNames: string[], is: Issues, base = 'healthcheck') {
  if (hc === null || hc === undefined) return;
  const h = hc as HealthCheck & Record<string, unknown>;
  const allowed: Record<string, string[]> = {
    http: ['portName', 'path', 'expectStatus'],
    tcp: ['portName'],
    exec: ['command', 'expectExitCode'],
    log: ['pattern'],
  };
  if (!allowed[h.type]) return is.err(`${base}.type`, 'V_ENUM', 'type must be http | tcp | exec | log');
  const common = ['type', 'intervalSec', 'timeoutSec', 'retries', 'startPeriodSec'];
  for (const k of Object.keys(h)) if (!common.includes(k) && !allowed[h.type]!.includes(k)) is.err(`${base}.${k}`, 'V_HEALTHCHECK', `field ${k} is not allowed for type ${h.type}`);
  const rng = (k: string, min: number, max: number) => {
    if (!isInt(h[k]) || (h[k] as number) < min || (h[k] as number) > max) is.err(`${base}.${k}`, 'V_RANGE', `${k} must be an integer ${min}–${max}`);
  };
  rng('intervalSec', 1, 3600);
  rng('timeoutSec', 1, 600);
  rng('retries', 1, 100);
  rng('startPeriodSec', 0, 3600);
  if (h.type === 'http' || h.type === 'tcp') {
    if (!portNames.includes(h.portName)) is.err(`${base}.portName`, 'V_REF', `"${String(h.portName)}" is not one of the image ports (${portNames.join(', ') || 'none'})`);
  }
  if (h.type === 'http') {
    if (!isStr(h.path) || !h.path.startsWith('/')) is.err(`${base}.path`, 'V_FORMAT', 'path must start with /');
    rng('expectStatus', 100, 599);
  }
  if (h.type === 'exec') {
    if (!Array.isArray(h.command) || h.command.length < 1 || h.command.length > 50 || h.command.some((x) => !isStr(x))) is.err(`${base}.command`, 'V_LENGTH', 'command must be 1–50 strings');
    rng('expectExitCode', 0, 255);
  }
  if (h.type === 'log') checkRe2(h.pattern, `${base}.pattern`, is);
}
export function validateCommand(v: unknown, path: string, is: Issues) {
  if (v === null || v === undefined) return;
  if (!Array.isArray(v) || v.length > LIMITS.commandItems || v.some((x) => !isStr(x) || x.length > LIMITS.value)) is.err(path, 'V_TYPE', `must be null or a list of ≤ ${LIMITS.commandItems} strings`);
}

/**
 * The editable image spec (kind, runtime, ports, env, volumes, health check, stop timeout, description) — the SAME rules
 * for registerImage and patchImage (§21 #82). Absent fields are skipped (register defaults apply); patchImage passes the
 * merged next image so every field is present.
 */
export function validateImageSpec(b: Record<string, unknown>, is: Issues) {
  if (b.kind !== undefined && b.kind !== 'service' && b.kind !== 'job') is.err('kind', 'V_ENUM', 'kind must be service | job');
  validateCommand(b.entrypoint, 'entrypoint', is);
  validateCommand(b.command, 'command', is);
  if (b.workdir !== undefined && b.workdir !== null && (!isStr(b.workdir) || !b.workdir.startsWith('/'))) is.err('workdir', 'V_FORMAT', 'workdir must be an absolute path');
  if (b.user !== undefined && b.user !== null && (!isStr(b.user) || !/^[A-Za-z0-9_.-]+(:[A-Za-z0-9_.-]+)?$/.test(b.user))) is.err('user', 'V_FORMAT', 'user must be "name" or "uid:gid"');
  validateImagePorts(b.ports ?? [], is);
  validateImageEnv(b.env ?? [], is);
  validateImageVolumes(b.volumes ?? [], is);
  validateHealthCheck(b.healthcheck ?? null, (Array.isArray(b.ports) ? (b.ports as ImagePort[]) : []).map((p) => p?.name), is);
  if (b.stopTimeoutSec !== undefined && (!isInt(b.stopTimeoutSec) || b.stopTimeoutSec < 1 || b.stopTimeoutSec > 600)) is.err('stopTimeoutSec', 'V_RANGE', 'stopTimeoutSec must be 1–600');
  checkDescription(b.description, 'description', is);
}

/** POST /v1/services/{id}/images body. */
export function validateRegisterImage(b: Record<string, unknown>): Issues {
  const is = new Issues('publish');
  if (!isStr(b.version) || !RE.version.test(b.version)) is.err('version', 'V_FORMAT', `version "${String(b.version ?? '')}" must look like v1, v2 or 3.1.0 (lower case, ^[a-z0-9][a-z0-9._-]{0,63}$)`);
  const pr = parseImageReference(b.reference);
  if (!pr.ok) is.err('reference', 'V_REFERENCE', pr.error);
  if (b.registryId !== undefined && b.registryId !== null && (!isStr(b.registryId) || !b.registryId)) is.err('registryId', 'V_TYPE', 'registryId must be an id or null (public)');
  if (b.platform !== undefined && b.platform !== 'linux/amd64' && b.platform !== 'linux/arm64') is.err('platform', 'V_ENUM', 'platform must be linux/amd64 | linux/arm64');
  if (b.mode !== 'copy' && b.mode !== 'gateway') is.err('mode', 'V_ENUM', 'mode must be copy | gateway');
  if (b.kind !== 'service' && b.kind !== 'job') is.err('kind', 'V_ENUM', 'kind must be service | job');
  validateImageSpec({ ...b, kind: undefined }, is);
  return is;
}

// ───────────────────────────────────────────────────────────── variables, secrets, files
export function variableValueMatches(v: Pick<Variable, 'type' | 'options'>, value: unknown): boolean {
  if (value === null) return true;
  switch (v.type) {
    case 'string':
      return isStr(value);
    case 'number':
      return typeof value === 'number' && Number.isFinite(value);
    case 'boolean':
      return typeof value === 'boolean';
    case 'enum':
      return isStr(value) && !!v.options && v.options.includes(value);
    case 'list':
      return Array.isArray(value) && value.every(isStr);
    case 'json':
      return typeof value === 'object' && value !== null;
  }
}
export function validateVariable(v: Variable, base: string, is: Issues, mode: ValidateMode = is.mode) {
  checkSlug(v.variableSlug, `${base}.variableSlug`, is);
  checkLabel(v.label, `${base}.label`, is);
  checkDescription(v.description, `${base}.description`, is);
  if (!['string', 'number', 'boolean', 'enum', 'list', 'json'].includes(v.type)) return is.err(`${base}.type`, 'V_ENUM', 'type must be string | number | boolean | enum | list | json');
  if (typeof v.required !== 'boolean') is.err(`${base}.required`, 'V_TYPE', 'required must be true / false');
  if (v.type === 'enum') {
    if (!Array.isArray(v.options) || v.options.length < 1 || v.options.length > LIMITS.enumOptions || v.options.some((x) => !isStr(x))) is.err(`${base}.options`, 'V_LENGTH', `enum needs 1–${LIMITS.enumOptions} string options`);
    else if (new Set(v.options).size !== v.options.length) is.err(`${base}.options`, 'V_DUPLICATE', 'options must be unique');
  } else if (v.options !== null && v.options !== undefined) is.err(`${base}.options`, 'V_TYPE', 'options are only for type enum (use null)');
  const val = v.validation;
  if (val !== null && val !== undefined) {
    if ((val.min !== null || val.max !== null) && v.type !== 'number') is.err(`${base}.validation`, 'V_TYPE', 'min / max are only for type number');
    if (val.pattern !== null && v.type !== 'string') is.err(`${base}.validation`, 'V_TYPE', 'pattern is only for type string');
    if (val.min !== null && typeof val.min !== 'number') is.err(`${base}.validation.min`, 'V_TYPE', 'min must be a number or null');
    if (val.max !== null && typeof val.max !== 'number') is.err(`${base}.validation.max`, 'V_TYPE', 'max must be a number or null');
    if (typeof val.min === 'number' && typeof val.max === 'number' && val.min > val.max) is.err(`${base}.validation`, 'V_RANGE', 'min > max');
    if (val.pattern !== null) checkRe2(val.pattern, `${base}.validation.pattern`, is);
  }
  if (!variableValueMatches(v, v.value)) return is.err(`${base}.value`, 'V_VALUE_TYPE', `value does not match type ${v.type}${v.type === 'enum' ? ` (one of ${(v.options ?? []).join(', ')})` : ''}`);
  if (v.value !== null && val) {
    if (v.type === 'number') {
      if (val.min !== null && (v.value as number) < val.min) is.err(`${base}.value`, 'V_VALUE_RULE', `value must be ≥ ${val.min}`);
      if (val.max !== null && (v.value as number) > val.max) is.err(`${base}.value`, 'V_VALUE_RULE', `value must be ≤ ${val.max}`);
    }
    if (v.type === 'string' && val.pattern) {
      try {
        if (!new RegExp(`^(?:${val.pattern})$`).test(v.value as string)) is.err(`${base}.value`, 'V_VALUE_RULE', `value does not match ${val.pattern}`);
      } catch {
        /* reported above */
      }
    }
  }
  if (v.type === 'string' && isStr(v.value) && v.value.length > LIMITS.value) is.err(`${base}.value`, 'V_LENGTH', `at most ${LIMITS.value} characters`);
  const empty = v.value === null || v.value === '';
  // template: a required variable is filled in at deployment (the deploy form asks for it) → no issue at all
  if (v.required && empty && mode !== 'template') is.err(`${base}.value`, 'V_VAR_REQUIRED', `${v.variableSlug} is required — set it before publishing`, 'publish');
  if (!v.required && empty && mode === 'template')
    is.info(`${base}.value`, 'V_VAR_OPTIONAL_NULL', `optional var ${v.variableSlug} has no value; add | default(…) where it is used, or a template default`);
}
export function validateSecretDef(s: SecretDef, base: string, is: Issues, template: boolean) {
  checkSlug(s.secretSlug, `${base}.secretSlug`, is);
  checkLabel(s.label, `${base}.label`, is);
  checkDescription(s.description, `${base}.description`, is);
  if (typeof s.required !== 'boolean') is.err(`${base}.required`, 'V_TYPE', 'required must be true / false');
  if (s.generate !== null && s.generate !== undefined) {
    if (!['alnum', 'hex', 'base64url'].includes(s.generate.charset)) is.err(`${base}.generate.charset`, 'V_ENUM', 'charset must be alnum | hex | base64url');
    if (!isInt(s.generate.length) || s.generate.length < 8 || s.generate.length > 128) is.err(`${base}.generate.length`, 'V_RANGE', 'length must be 8–128');
  }
  if (template && s.value !== null && s.value !== undefined) is.err(`${base}.value`, 'V_TEMPLATE_SECRET_VALUE', 'templates never hold secret values');
  if (isStr(s.value) && utf8Len(s.value) > LIMITS.secretBytes) is.err(`${base}.value`, 'V_LENGTH', `at most ${LIMITS.secretBytes} bytes`);
  if (!template && s.required && (s.value === null || s.value === undefined || s.value === '')) is.err(`${base}.value`, 'V_SECRET_REQUIRED', `secret ${s.secretSlug} is required — set it before publishing`, 'publish');
}
export function validateFileDef(f: FileDef, base: string, is: Issues) {
  checkSlug(f.fileSlug, `${base}.fileSlug`, is);
  checkLabel(f.label, `${base}.label`, is);
  checkDescription(f.description, `${base}.description`, is);
  if (!isStr(f.mime) || !RE.mime.test(f.mime)) is.err(`${base}.mime`, 'V_FORMAT', 'mime must be type/subtype');
  if (f.editor !== 'text' && f.editor !== 'binary') is.err(`${base}.editor`, 'V_ENUM', 'editor must be text | binary');
  if (f.render !== undefined && f.render !== 'expressions' && f.render !== 'none') is.err(`${base}.render`, 'V_ENUM', 'render must be expressions | none');
  else if (f.editor === 'binary' && f.render === 'expressions') is.err(`${base}.render`, 'V_ENUM', 'binary files are mounted as they are (render none)');
  if (!isInt(f.maxBytes) || f.maxBytes < 1 || f.maxBytes > LIMITS.fileMaxBytes) is.err(`${base}.maxBytes`, 'V_RANGE', `maxBytes must be 1–${LIMITS.fileMaxBytes}`);
  checkLocation(f.location, 'files', `${base}.location`, is);
  if (f.content !== null && f.content !== undefined) {
    if (!isStr(f.content.sha256) || !RE.sha256.test(f.content.sha256)) is.err(`${base}.content.sha256`, 'V_FORMAT', 'sha256 must be "sha256:" + 64 hex');
    if (!isInt(f.content.sizeBytes) || f.content.sizeBytes < 0) is.err(`${base}.content.sizeBytes`, 'V_RANGE', 'sizeBytes must be ≥ 0');
    else if (isInt(f.maxBytes) && f.content.sizeBytes > f.maxBytes) is.err(`${base}.content`, 'V_FILE_CONTENT', `content is ${f.content.sizeBytes} bytes; maxBytes is ${f.maxBytes}`);
  }
}

// ───────────────────────────────────────────────────────────── catalog view used for validation
/** A service as validation sees it; the map key IS the serviceSlug. */
export interface CatalogService {
  label?: string;
}
export interface CatalogImage {
  kind: ImageKind;
  ports: ImagePort[];
  env: ImageEnv[];
  volumes: ImageVolume[];
  healthcheck?: HealthCheck | null;
  /** absent = ready (revision catalogs only hold ready images) */
  importStatus?: 'pending' | 'copying' | 'ready' | 'failed';
}
export interface ValidationContext {
  mode: ValidateMode;
  /** used for deployment.id in expressions; templates use "template" */
  deploymentId: string;
  services: Record<string, CatalogService>;
  /** key `${serviceSlug}:${version}` */
  images: Record<string, CatalogImage>;
  /** existing not-found page ids; undefined = do not check */
  notFoundPageIds?: readonly string[];
  /** secret slugs that have a value (cloud views mask values); undefined = use SecretDef.value */
  secretsSet?: readonly string[];
  /** siteSlug → advertised / detected address of enrolled sites (auto hosts); cloud: AgentDoc.advertiseHost */
  detectedHosts?: Record<string, string>;
  /**
   * fileSlug → text content (render 'expressions' files). When given (agent staging; cloud editor preview), every file
   * is rendered for every container that mounts it: V_FILE_RENDER / V_FILE_JSON (publish level).
   */
  fileContents?: Record<string, string>;
  /**
   * §21 #101 sha256 → text content of volume content entries with render 'expressions' (starter content). When given,
   * each is rendered once per site: V_FILE_RENDER at `…volumes[v].seed.entries[path]` (publish level).
   */
  blobContents?: Record<string, string>;
}
export const imageKey = (serviceSlug: string, version: string) => `${serviceSlug}:${version}`;

// ───────────────────────────────────────────────────────────── docker image references
const DOCKER_HUB_HOSTS = ['docker.io', 'index.docker.io', 'registry-1.docker.io'];
/**
 * Parse a docker reference `[host[:port]/]repository[:tag][@sha256:<64 hex>]` as the docker CLI does: the first path part
 * is a host when it has "." or ":" or is "localhost". Docker Hub hosts (docker.io, index.docker.io, registry-1.docker.io)
 * normalize to "docker.io" and single names get "library/". No tag and no digest → tag "latest". The repository must be
 * lower case (RE.repository), the tag RE.tag.
 */
export function parseImageReference(ref: unknown): { ok: true; parts: import('./types.js').ImageRefParts } | { ok: false; error: string } {
  if (!isStr(ref) || !ref.trim()) return { ok: false, error: 'enter an image reference, e.g. docker.io/library/redis:7.4 or ghcr.io/acme/api:1.2.0' };
  let r = ref.trim();
  if (r.length > 512) return { ok: false, error: 'at most 512 characters' };
  if (/\s/.test(r)) return { ok: false, error: 'an image reference has no spaces' };
  r = r.replace(/^[a-z]+:\/\//i, ''); // tolerate a pasted https:// / docker:// prefix
  let digest: string | null = null;
  const at = r.indexOf('@');
  if (at >= 0) {
    digest = r.slice(at + 1);
    r = r.slice(0, at);
    if (!RE.sha256.test(digest)) return { ok: false, error: `digest "${digest}" must be sha256: + 64 lower-case hex` };
  }
  let tag: string | null = null;
  const lastSlash = r.lastIndexOf('/');
  const colon = r.lastIndexOf(':');
  if (colon > lastSlash) {
    tag = r.slice(colon + 1);
    r = r.slice(0, colon);
    if (!RE.tag.test(tag)) return { ok: false, error: `tag "${tag}" is not a docker tag (letters, digits, _ . -; at most 128; not starting with . or -)` };
  }
  let host = 'docker.io';
  const first = r.split('/')[0]!;
  if (r.includes('/') && (first.includes('.') || first.includes(':') || first === 'localhost')) {
    host = first.toLowerCase();
    r = r.slice(first.length + 1);
  }
  if (DOCKER_HUB_HOSTS.includes(host)) host = 'docker.io';
  if (host === 'docker.io' && r && !r.includes('/')) r = `library/${r}`;
  if (!r) return { ok: false, error: 'the repository is missing, e.g. docker.io/northwind/northwind:1.0' };
  if (!RE.repository.test(r)) return { ok: false, error: `repository "${r}" is not valid: lower-case letters, digits and . _ - separated by /` };
  if (tag === null && digest === null) tag = 'latest';
  return { ok: true, parts: { host, repository: r, tag, digest } };
}
/** The canonical text of parsed parts: host/repository[:tag][@digest]. */
export function formatImageReference(p: import('./types.js').ImageRefParts): string {
  return `${p.host}/${p.repository}${p.tag ? `:${p.tag}` : ''}${p.digest ? `@${p.digest}` : ''}`;
}
/** Host of a registry URL as a reference host ("https://registry-1.docker.io" → "docker.io"). */
export function registryHost(url: string): string {
  const h = url.replace(/^https?:\/\//i, '').replace(/\/.*$/, '').toLowerCase();
  return DOCKER_HUB_HOSTS.includes(h) ? 'docker.io' : h;
}
/** The registries whose URL host equals the reference host (several → the form shows a dropdown; none → public). */
export function matchRegistries<R extends { registryId: string; url: string; label: string }>(host: string, registries: readonly R[]): R[] {
  return registries.filter((r) => registryHost(r.url) === host).sort((a, b) => a.label.localeCompare(b.label));
}
/** Suggest the next version after the latest one: v3 → v4, 3.1.0 → 3.1.1, rc-9 → rc-10, none → v1. */
export function nextVersion(latest: string | null | undefined, taken: readonly string[] = []): string {
  let cand = 'v1';
  if (latest) {
    const m = /^(.*?)(\d+)([^\d]*)$/.exec(latest);
    cand = m ? `${m[1]}${Number(m[2]) + 1}${m[3]}` : `${latest}-2`;
  }
  while (taken.includes(cand)) cand = nextVersion(cand);
  return cand;
}

// ───────────────────────────────────────────────────────────── graphs
/** Kahn topological sort; nodes tie-broken by `key`. Returns the order (dependencies first) or the nodes in a cycle. */
export function topoOrder(nodes: string[], dependsOn: (n: string) => string[], key: (n: string) => string = (n) => n): { order: string[] } | { cycle: string[] } {
  const indeg = new Map<string, number>();
  const users = new Map<string, string[]>();
  for (const n of nodes) {
    indeg.set(n, 0);
    users.set(n, []);
  }
  for (const n of nodes)
    for (const d of new Set(dependsOn(n))) {
      if (!indeg.has(d) || d === n) continue;
      indeg.set(n, indeg.get(n)! + 1);
      users.get(d)!.push(n);
    }
  const ready = nodes.filter((n) => indeg.get(n) === 0).sort((a, b) => (key(a) < key(b) ? -1 : 1));
  const order: string[] = [];
  while (ready.length) {
    const n = ready.shift()!;
    order.push(n);
    for (const u of users.get(n)!) {
      indeg.set(u, indeg.get(u)! - 1);
      if (indeg.get(u) === 0) {
        ready.push(u);
        ready.sort((a, b) => (key(a) < key(b) ? -1 : 1));
      }
    }
  }
  if (order.length === nodes.length) return { order };
  return { cycle: nodes.filter((n) => !order.includes(n)) };
}
/** Site apply order (site depends: dependencies first), tie-break by slug. */
export function siteOrder(config: Pick<Config, 'sites'>): { order: string[] } | { cycle: string[] } {
  const by = new Map(config.sites.map((s) => [s.siteSlug, s]));
  return topoOrder(
    config.sites.map((s) => s.siteSlug),
    (n) => by.get(n)!.config.depends.map((d) => d.siteSlug),
  );
}
/** Container start order inside a site (enabled containers only, by containerSlug), tie-break by containerSlug. */
export function containerOrder(site: SiteConfig, _services?: Record<string, CatalogService>): { order: string[] } | { cycle: string[] } {
  const enabled = site.containers.filter((c) => c.enabled);
  const by = new Map(enabled.map((c) => [c.containerSlug, c]));
  return topoOrder(
    enabled.map((c) => c.containerSlug),
    (n) => (by.get(n)!.depends ?? []).map((d) => d.containerSlug),
    (n) => n,
  );
}

// ───────────────────────────────────────────────────────────── container instances: names, ports, mounts
/** Suggest a containerSlug for a new instance of a service: the service slug, then <slug>-2, -3 … (≤ 40 chars). */
export function suggestContainerSlug(serviceSlug: string, taken: readonly string[]): string {
  if (!taken.includes(serviceSlug) && !(RESERVED_CONTAINER_SLUGS as readonly string[]).includes(serviceSlug)) return serviceSlug;
  if ((RESERVED_CONTAINER_SLUGS as readonly string[]).includes(serviceSlug)) taken = [...taken, serviceSlug];
  for (let i = 2; ; i++) {
    const suf = `-${i}`;
    const cand = `${serviceSlug.slice(0, 40 - suf.length).replace(/-+$/, '')}${suf}`;
    if (!taken.includes(cand)) return cand;
  }
}
/** Suggested volumeSlug for "+ New data volume…" of an image volume in a container: `<containerSlug>-<imageVolume>` (≤ 100 chars). */
export const defaultVolumeSlug = (containerSlug: string, imageVolume: string) => `${containerSlug}-${imageVolume}`.slice(0, 100);
/** A new declared site volume (backup on, label = volumeSlug unless given). */
export function newSiteVolume(
  volumeSlug: string,
  label: string = volumeSlug,
  description = '',
  backup = true,
  location: StorageLocation = { kind: 'auto' },
  kind: SiteVolumeKind = 'dir',
  seed: VolumeSeed | null = null,
): SiteVolume {
  return { volumeSlug, label, description, backup, location, kind, seed };
}
/** A seed entry path: relative inside the volume, segments [A-Za-z0-9._-], no "." / ".." segments, ≤ 255 chars. */
export const SEED_PATH_RE = /^(?!\/)(?:[A-Za-z0-9._-]+\/)*[A-Za-z0-9._-]+$/;
export function seedPathError(p: unknown): string | null {
  if (!isStr(p) || p === '') return 'enter a path inside the volume, e.g. flows.json or lib/settings.js';
  if (p.length > 255) return 'at most 255 characters';
  if (p.startsWith('/')) return 'the path is relative to the volume: no leading "/"';
  if (!SEED_PATH_RE.test(p)) return 'letters, digits, . _ - and "/" between folders only';
  if (p.split('/').some((x) => x === '.' || x === '..')) return '"." and ".." are not allowed';
  return null;
}
/** Short text of a seed for tables ("Empty", "2 files", "archive x.tar.gz", "file nodered-flow"). */
export function seedLabel(seed: VolumeSeed | null | undefined): string {
  if (!seed) return 'Empty';
  if (seed.kind === 'files') return `${seed.entries.length} file${seed.entries.length === 1 ? '' : 's'}`;
  if (seed.kind === 'file') return `file ${seed.fileSlug}`;
  if (seed.kind === 'tree') {
    const n = seed.entries.length;
    return n === 0 && seed.dirs.length === 0 ? 'Empty' : `${n} file${n === 1 ? '' : 's'}`;
  }
  if (seed.kind === 'single') return `1 file (${seed.size} B)`;
  return `archive ${seed.name}`;
}
/** Warning text shown for single-file volumes (both GUIs + docs). */
export const FILE_VOLUME_RENAME_WARNING =
  'Apps that save by writing a temp file and renaming it (e.g. Node-RED flows.json) need a folder volume — a single-file Docker mount breaks on rename. Use a folder volume seeded with the files instead.';
/** One use of a site volume by a container mount. mountPath as written (null = the image volume path). */
export interface VolumeUse {
  containerSlug: string;
  imageVolume: string | null;
  mountPath: string | null;
  readOnly: boolean;
  enabled: boolean;
}
/** volumeSlug → the container mounts that use it (every container, enabled or not), for "used by" / "shared with". */
export function volumeUsers(sc: Pick<SiteConfig, 'containers'>): Record<string, VolumeUse[]> {
  const out: Record<string, VolumeUse[]> = {};
  for (const c of sc.containers ?? [])
    for (const v of Array.isArray(c.volumes) ? c.volumes : [])
      if (v?.source?.kind === 'volume') (out[v.source.volumeSlug] ??= []).push({ containerSlug: c.containerSlug, imageVolume: v.imageVolume, mountPath: v.mountPath, readOnly: !!v.readOnly, enabled: c.enabled !== false });
  return out;
}
/** volumeSlugs that mounts of the site use but the site does not declare (in first-use order). */
export function undeclaredVolumes(sc: Pick<SiteConfig, 'containers' | 'volumes'>): string[] {
  const declared = new Set((sc.volumes ?? []).map((v) => v.volumeSlug));
  return Object.keys(volumeUsers(sc)).filter((v) => !declared.has(v));
}
/** Free volumeSlug based on `base` (base, base-2, base-3 …) among the declared ones. */
export function suggestVolumeSlug(base: string, taken: readonly string[]): string {
  const b = base.toLowerCase().replace(/[^a-z0-9_.-]+/g, '-').replace(/^[^a-z0-9]+/, '').slice(0, 100) || 'data';
  if (!taken.includes(b)) return b;
  for (let i = 2; ; i++) {
    const suf = `-${i}`;
    const cand = `${b.slice(0, 100 - suf.length)}${suf}`;
    if (!taken.includes(cand)) return cand;
  }
}

/** One effective mount of a container (image-volume rows with source none are left out). */
export interface EffectiveMount {
  /** "image:<name>" for image-volume rows (implicit or explicit), "volume[i]" for extra mounts (i = index in Container.volumes) */
  key: string;
  imageVolume: string | null;
  /** the mount path as written (may hold {{ }}) */
  mountPath: string;
  source: VolumeSource;
  readOnly: boolean;
}
/**
 * The effective mounts of a container: image-volume rows (in image volume order) + the extra mounts, in this order.
 * An image volume without a row, or with source none, is not mounted (round D: there is no implicit data volume).
 */
export function containerMounts(c: Pick<Container, 'containerSlug' | 'volumes'>, img: Pick<CatalogImage, 'volumes'>): EffectiveMount[] {
  const out: EffectiveMount[] = [];
  const vols = Array.isArray(c.volumes) ? c.volumes : [];
  for (const iv of img.volumes) {
    const row = vols.find((v) => v && v.imageVolume === iv.name);
    if (!row || !row.source || row.source.kind === 'none') continue;
    const source = row.source;
    const ro = source.kind === 'file' || source.kind === 'secret' ? true : !!row.readOnly;
    out.push({ key: `image:${iv.name}`, imageVolume: iv.name, mountPath: row.mountPath ?? iv.mountPath, source, readOnly: ro });
  }
  vols.forEach((v, i) => {
    if (!v || v.imageVolume !== null || !v.source || v.source.kind === 'none') return;
    const ro = v.source.kind === 'file' || v.source.kind === 'secret' ? true : !!v.readOnly;
    out.push({ key: `volume[${i}]`, imageVolume: null, mountPath: v.mountPath ?? '', source: v.source, readOnly: ro });
  });
  return out;
}
/** The port rows of a container for every image port (missing row = not published, edge true). */
export function containerPorts(c: Pick<Container, 'ports'>, img: Pick<CatalogImage, 'ports'>): (ContainerPort & { protocol: 'tcp' | 'udp' })[] {
  const rows = Array.isArray(c.ports) ? c.ports : [];
  return img.ports.map((ip) => {
    const r = rows.find((x) => x && x.portName === ip.name);
    return { portName: ip.name, hostPort: r?.hostPort ?? null, hostIp: r?.hostIp ?? '0.0.0.0', edge: r?.edge ?? true, protocol: ip.protocol };
  });
}

// ───────────────────────────────────────────────────────────── expression contexts
/**
 * var.* values. defer (default true; false in agent mode): a REQUIRED variable without a value (null / "") becomes
 * DEPLOY_PLACEHOLDER — it is set at deployment, so its usages are never reported (only the variable: V_VAR_REQUIRED).
 */
export function varValues(config: Pick<Config, 'variables'>, defer = true): Record<string, ExprValue> {
  const out: Record<string, ExprValue> = {};
  for (const v of config.variables) out[v.variableSlug] = defer && v.required && (v.value === null || v.value === undefined || v.value === '') ? DEPLOY_PLACEHOLDER : ((v.value ?? null) as ExprValue);
  return out;
}
/** secret.* values for expressions: plain values, or "•••" / null with secretsSet (masked cloud views); defer: required + unset → DEPLOY_PLACEHOLDER. */
export function secretValues(config: Pick<Config, 'secrets'>, secretsSet?: readonly string[], defer = true): Record<string, string | null> {
  const out: Record<string, string | null> = {};
  for (const s of config.secrets) {
    const v = secretsSet ? (secretsSet.includes(s.secretSlug) ? '•••' : null) : (s.value ?? null);
    out[s.secretSlug] = defer && s.required && (v === null || v === '') ? DEPLOY_PLACEHOLDER : v;
  }
  return out;
}
const deferFor = (mode: ValidateMode | undefined) => mode !== 'agent';
export interface SiteHost {
  /** the address used for site.<slug>.host / self.host; null = auto and not known (agent mode: E_NULL at use) */
  host: string | null;
  /** an error of the host expression (not E_NULL: a null / empty result falls back to auto) */
  error: ExprError | null;
  /** true when the address is the auto one (host null, or the expression had no value) */
  auto: boolean;
  /** set when the host expression resolved to null / "" and the site falls back to auto (warning V_HOST_AUTO) */
  fallback: { message: string } | null;
}
export interface SiteHostOptions {
  /** siteSlug → advertised / detected primary address (cloud: AgentDoc.advertiseHost; agent: peers + itself) */
  detected?: Record<string, string>;
  /** true (cloud): unknown auto hosts become autoHostPlaceholder(slug); false (agent): null */
  placeholder?: boolean;
  /** default true (false in agent mode): required variables without a value resolve to DEPLOY_PLACEHOLDER (varValues) */
  defer?: boolean;
}
/** Resolve every SiteConfig.host (field "host": var.*, deployment.id only); null host = auto (SPEC §2.2). */
export function resolveSiteHosts(config: Pick<Config, 'variables' | 'sites'>, deploymentId: string, o: SiteHostOptions = {}): Record<string, SiteHost> {
  const vars = varValues(config, o.defer ?? true);
  const placeholder = o.placeholder ?? true;
  const out: Record<string, SiteHost> = {};
  for (const s of config.sites) {
    const auto = o.detected?.[s.siteSlug] ?? (placeholder ? autoHostPlaceholder(s.siteSlug) : null);
    const h = s.config?.host;
    if (h === null || h === undefined || (isStr(h) && h.trim() === '')) {
      out[s.siteSlug] = { host: auto, error: null, auto: true, fallback: null };
      continue;
    }
    const ctx: ExprContext = { deployment: { id: deploymentId }, self: { slug: s.siteSlug, host: null }, var: vars };
    try {
      const v = resolve(h, ctx, 'host').value;
      if (v.trim() === '') out[s.siteSlug] = { host: auto, error: null, auto: true, fallback: { message: hostFallbackMessage(h, vars) } };
      else out[s.siteSlug] = { host: v, error: null, auto: false, fallback: null };
    } catch (e) {
      if (!(e instanceof ExprError)) throw e;
      if (e.code === 'E_NULL') out[s.siteSlug] = { host: auto, error: null, auto: true, fallback: { message: hostFallbackMessage(h, vars) } };
      else out[s.siteSlug] = { host: null, error: e, auto: false, fallback: null };
    }
  }
  return out;
}
function hostFallbackMessage(src: string, vars: Record<string, ExprValue>): string {
  let names: string[] = [];
  try {
    names = exprRefs(src)
      .filter((r) => r.startsWith('var.'))
      .map((r) => r.slice(4))
      .filter((n) => vars[n] === null || vars[n] === undefined || vars[n] === '');
  } catch {
    /* syntax errors are reported elsewhere */
  }
  const what = names.length ? `var ${names.join(', ')} has no value` : 'the host expression has no value';
  return `${what} — leave host on auto or set the variable (the site uses its detected address)`;
}
/** Resolve a container port number of an image port (number or {{ }} block). Returns null when it cannot resolve. */
export function imagePortNumber(p: ImagePort, vars: Record<string, ExprValue>, deploymentId: string): number | null {
  if (typeof p.port === 'number') return p.port;
  try {
    return resolveNumberField(p.port, { deployment: { id: deploymentId }, self: { slug: '', host: null }, var: vars }, 'imagePort');
  } catch {
    return null;
  }
}
/** site.<slug>.host / site.<slug>.port.<containerSlug>.<portName> map for every site (unresolvable hostPorts → null). */
export function siteExprMap(
  config: Pick<Config, 'variables' | 'sites'>,
  ctx: Pick<ValidationContext, 'services' | 'deploymentId'> & Partial<Pick<ValidationContext, 'images' | 'detectedHosts' | 'mode'>>,
  hosts?: Record<string, SiteHost>,
): NonNullable<ExprContext['site']> {
  const vars = varValues(config, deferFor(ctx.mode));
  const hs = hosts ?? resolveSiteHosts(config, ctx.deploymentId, { detected: ctx.detectedHosts, placeholder: ctx.mode !== 'agent', defer: deferFor(ctx.mode) });
  const out: NonNullable<ExprContext['site']> = {};
  for (const s of config.sites) {
    const port: Record<string, Record<string, number | null>> = {};
    for (const c of s.config.containers ?? []) {
      if (!ctx.services[c.serviceSlug] || !isStr(c.containerSlug)) continue;
      const img = ctx.images?.[imageKey(c.serviceSlug, c.version)];
      const rows = img ? containerPorts(c, img) : (Array.isArray(c.ports) ? c.ports : []);
      const m: Record<string, number | null> = {};
      for (const p of rows) {
        let hp: number | null = null;
        try {
          hp = resolveNumberField(p.hostPort as number | string | null, { deployment: { id: ctx.deploymentId }, self: { slug: s.siteSlug, host: hs[s.siteSlug]?.host ?? null }, var: vars }, 'hostPort');
        } catch {
          hp = null;
        }
        m[p.portName] = hp;
      }
      port[c.containerSlug] = m;
    }
    out[s.siteSlug] = { host: hs[s.siteSlug]?.host ?? null, port };
  }
  return out;
}
/**
 * containers.<containerSlug>.host / .port.<portName> of ONE site (§21 #88): every container of the site (self included,
 * disabled ones too) → host = the containerSlug (its network alias on the site network; resolves in both docker naming
 * modes, only from containers on that network), port = the INTERNAL port of each image port (imagePortNumber; null when
 * it does not resolve or the image is unknown).
 */
export function siteContainersMap(
  config: Pick<Config, 'variables' | 'sites'>,
  ctx: Pick<ValidationContext, 'deploymentId'> & Partial<Pick<ValidationContext, 'images' | 'mode'>>,
  siteSlug: string,
): NonNullable<ExprContext['containers']> {
  const out: NonNullable<ExprContext['containers']> = {};
  const site = config.sites.find((s) => s.siteSlug === siteSlug);
  if (!site) return out;
  const vars = varValues(config, deferFor(ctx.mode));
  for (const c of site.config.containers ?? []) {
    if (!c || !isStr(c.containerSlug)) continue;
    const img = ctx.images?.[imageKey(c.serviceSlug, c.version)];
    const port: Record<string, number | null> = {};
    for (const p of img?.ports ?? []) port[p.name] = imagePortNumber(p, vars, ctx.deploymentId);
    out[c.containerSlug] = { host: c.containerSlug, enabled: c.enabled !== false, port };
  }
  return out;
}
/**
 * The ExprContext for one container (env values, image env defaults, mount paths, rendered files).
 * secrets: the plain values from config (the cloud may pass masked configs with secretsSet; then "•••" is used).
 * file.<slug>.path of a mounted file is answered by resolveContainer() (the resolved mount path); here: the literal
 * mount path of the first mount of that file, or null when the container does not mount it.
 */
export function containerExprContext(
  config: Config,
  ctx: Pick<ValidationContext, 'services' | 'deploymentId' | 'secretsSet'> & Partial<Pick<ValidationContext, 'images' | 'detectedHosts' | 'mode'>>,
  siteSlug: string,
  c: Container,
  siteMap?: NonNullable<ExprContext['site']>,
): ExprContext {
  const sm = siteMap ?? siteExprMap(config, ctx);
  const secret = secretValues(config, ctx.secretsSet, deferFor(ctx.mode));
  const img = ctx.images?.[imageKey(c.serviceSlug, c.version)];
  const mounts = containerMounts(c, img ?? { volumes: [] });
  const file: Record<string, { path: string | null }> = {};
  for (const f of config.files) {
    const m = mounts.find((x) => x.source.kind === 'file' && x.source.fileSlug === f.fileSlug);
    file[f.fileSlug] = { path: m ? m.mountPath : null };
  }
  return {
    deployment: { id: ctx.deploymentId },
    self: { slug: siteSlug, host: sm[siteSlug]?.host ?? null },
    container: { slug: c.containerSlug },
    var: varValues(config, deferFor(ctx.mode)),
    secret,
    file,
    site: sm,
    containers: siteContainersMap(config, ctx, siteSlug),
  };
}
/**
 * The env + mount sources of one container (SPEC §10.5): every image env not set by the container (field imageEnv,
 * its default or null), every container env (field env; secretRef → the secret value), and every effective mount
 * (containerMounts: key "image:<name>" / "volume[i]").
 */
export function containerSources(config: Pick<Config, 'secrets'>, c: Container, img: Pick<CatalogImage, 'env' | 'volumes'>, secrets?: Record<string, string | null>): { env: EnvSource[]; mounts: MountSource[] } {
  const env: EnvSource[] = [];
  const given = new Set(c.env.map((e) => e.name));
  const sec = secrets ?? secretValues(config);
  for (const e of c.env) {
    if (e.secretRef !== null && e.secretRef !== undefined) env.push({ name: e.name, src: null, field: 'env', secretRef: { value: sec[e.secretRef] ?? null } });
    else env.push({ name: e.name, src: e.value ?? null, field: 'env' });
  }
  for (const raw of img.env) {
    const ie = normalizeImageEnv(raw);
    if (!given.has(ie.name)) env.push({ name: ie.name, src: ie.value ?? null, field: 'imageEnv' });
  }
  const mounts: MountSource[] = containerMounts(c, img).map((m) => ({ key: m.key, src: m.mountPath, fileSlug: m.source.kind === 'file' ? m.source.fileSlug : null }));
  return { env, mounts };
}
/**
 * Resolve the env vars and mount paths of one container in dependency order (container.env.<NAME>, file.<slug>.path;
 * E_CYCLE). `ectx` defaults to containerExprContext(). The driver uses the resolved values at APPLY.
 */
export function resolveContainerConfig(
  config: Config,
  ctx: Pick<ValidationContext, 'services' | 'deploymentId' | 'secretsSet'>,
  c: Container,
  img: Pick<CatalogImage, 'env' | 'volumes'>,
  ectx: ExprContext,
): { env: EnvSource[]; mounts: MountSource[]; resolution: ContainerResolution } {
  const src = containerSources(config, c, img, ectx.secret ?? undefined);
  return { ...src, resolution: resolveContainer(ectx, src.env, src.mounts) };
}
export function edgeExprContext(config: Pick<Config, 'variables' | 'sites'>, deploymentId: string, siteSlug: string, hosts?: Record<string, SiteHost>, defer = true): ExprContext {
  const hs = hosts ?? resolveSiteHosts(config, deploymentId, { defer });
  return { deployment: { id: deploymentId }, self: { slug: siteSlug, host: hs[siteSlug]?.host ?? null }, var: varValues(config, defer) };
}

// ───────────────────────────────────────────────────────────── files with expressions (render per container)
/** The render mode of a file (legacy documents without `render`: text → expressions, binary → none). */
export function fileRenderMode(f: Pick<FileDef, 'editor'> & { render?: FileDef['render'] }): FileDef['render'] {
  if (f.editor === 'binary') return 'none';
  return f.render ?? 'expressions';
}
export function normalizeFileDef(f: FileDef | (Omit<FileDef, 'render' | 'location'> & { render?: FileDef['render']; location?: StorageLocation })): FileDef {
  return { ...(f as FileDef), render: fileRenderMode(f), location: normalizeLocation((f as { location?: StorageLocation }).location) };
}

// ───────────────────────────────────────────────────────────── storage locations (round D)
/** Top-level names under agentHome that belong to the agent itself (a "./" location may not use them, except its own kind dir). */
export const AGENT_HOME_RESERVED = ['site.key', 'opm.db', 'opm.db-wal', 'opm.db-shm', 'blobs', 'snapshots', 'certs', 'edge', 'transfers', 'tunnel', 'logs', 'notfound', 'rendered', 'secrets', 'vol', 'files', 'volumes'] as const;
export const AUTO_LOCATION: StorageLocation = { kind: 'auto' };
export function normalizeLocation(l: StorageLocation | null | undefined): StorageLocation {
  if (!l || typeof l !== 'object' || l.kind !== 'path') return { kind: 'auto' };
  return { kind: 'path', path: l.path };
}
/**
 * The location as a path: "./files/<slug>" / "./volumes/<slug>" for auto, the "./…" or absolute path as written.
 * With agentHome, "./" paths become absolute (agentHome + rest).
 */
export function locationPath(l: StorageLocation | null | undefined, kind: 'files' | 'volumes', slug: string, agentHome?: string): string {
  const rel = !l || l.kind !== 'path' ? `./${kind}/${slug}` : l.path;
  if (agentHome === undefined || !rel.startsWith('./')) return rel;
  return `${agentHome.replace(/\/+$/, '')}/${rel.slice(2)}`;
}
/** Display text for a location: "auto · <agentHome>/files/<slug>" or the custom path. */
export function locationLabel(l: StorageLocation | null | undefined, kind: 'files' | 'volumes', slug: string, agentHome = '<agentHome>'): string {
  return !l || l.kind !== 'path' ? `auto · ${agentHome}/${kind}/${slug}` : l.path;
}
/** Rules of one location (V_LOCATION): "./" + segments without "." / ".." / empty, not a reserved agent dir (except its own kind dir), or a safe absolute path; no {{ }}. */
export function checkLocation(l: unknown, kind: 'files' | 'volumes', path: string, is: Issues) {
  if (l === undefined || l === null) return; // missing = auto (normalized on write)
  const x = l as { kind?: unknown; path?: unknown };
  if (typeof x !== 'object' || (x.kind !== 'auto' && x.kind !== 'path')) return is.err(path, 'V_LOCATION', 'location must be { kind: "auto" } or { kind: "path", path }');
  if (x.kind === 'auto') return;
  if (!isStr(x.path) || x.path === '' || isExpr(x.path as string)) return is.err(`${path}.path`, 'V_LOCATION', 'a custom location needs a path ("./…" = relative to the agent home, or an absolute path); no {{ }}');
  const p = x.path as string;
  if (p.startsWith('./')) {
    const segs = p.slice(2).split('/');
    if (segs.some((g) => g === '' || g === '.' || g === '..' || /\s/.test(g))) return is.err(`${path}.path`, 'V_LOCATION', `"${p}": a "./" path is relative to the agent home: no empty, ".", ".." segments or spaces`);
    if ((AGENT_HOME_RESERVED as readonly string[]).includes(segs[0]!) && segs[0] !== kind) return is.err(`${path}.path`, 'V_LOCATION', `"${p}": ./${segs[0]} belongs to the agent`);
    if (segs[0] === kind && segs.length < 2) return is.err(`${path}.path`, 'V_LOCATION', `"${p}": pick a name inside ./${kind}/`);
  } else if (!p.startsWith('/') || !isSafeMountPath(p)) return is.err(`${path}.path`, 'V_LOCATION', `"${p}" must start with "./" (agent home) or be an absolute path (not "/", no spaces, no "..")`);
}
const nestedOrSame = (a: string, b: string) => a === b || a.startsWith(`${b}/`) || b.startsWith(`${a}/`);
export interface FileExprIssue {
  code: string;
  message: string;
  pos: number;
  line: number;
  col: number;
}
/**
 * Check a file template without a container (editor live validation): syntax, filters, allowed roots, path shapes.
 * Returns every block error (a syntax error stops the scan) with 1-based line:col.
 */
export function checkFileContent(src: string): FileExprIssue[] {
  const out: FileExprIssue[] = [];
  const at = (e: ExprError) => ({ code: e.code, message: e.message, pos: e.pos, ...exprLineCol(src, e.pos) });
  let segs;
  try {
    segs = parseTemplate(src);
  } catch (e) {
    if (e instanceof ExprError) return [at(e)];
    throw e;
  }
  for (const s of segs) {
    if (s.kind !== 'block') continue;
    // re-check one block at a time so that every bad block is reported
    const text = Array.from(src).slice(s.start, s.end).join('');
    for (const e of checkExpr(text, 'file')) out.push(at(new ExprError(e.code, e.message, s.start + e.pos)));
  }
  return out;
}
export interface RenderedFile {
  ok: true;
  value: string;
  /** secrets replaced by "••••" (previews, logs) */
  masked: string;
  secret: boolean;
  refs: string[];
  /** §21 #103: every secret part is a mosquitto_passwd hash → the agent writes the file 0444 (else 0400 when secret) */
  secretHashed?: boolean;
}
export interface RenderFileError {
  ok: false;
  code: string;
  message: string;
  pos: number;
  line: number;
  col: number;
}
/**
 * Render a file with render 'expressions' ONCE for a site (round D): field "file" (roots var, secret, site, self,
 * deployment; container.* / file.* → E_NOT_ALLOWED "files are shared by all containers on the site"). ectx =
 * siteFileExprContext(). Errors carry line:col (V_FILE_RENDER at staging).
 */
export function renderSiteFile(src: string, ectx: ExprContext): RenderedFile | RenderFileError {
  try {
    const r: ResolveResult = resolve(src, ectx, 'file');
    return { ok: true, value: r.value, masked: r.masked, secret: r.secret, refs: r.refs, ...(r.secretHashed ? { secretHashed: true } : {}) };
  } catch (e) {
    if (!(e instanceof ExprError)) throw e;
    return { ok: false, code: e.code, message: e.message, pos: e.pos, ...exprLineCol(src, e.pos) };
  }
}
/** The expression context of the files of one site: var, secret (cloud: secretsSet → "•••"), site, self, deployment. */
export function siteFileExprContext(
  config: Config,
  ctx: Pick<ValidationContext, 'deploymentId' | 'secretsSet'> & Partial<Pick<ValidationContext, 'services' | 'images' | 'detectedHosts' | 'mode'>>,
  siteSlug: string,
  siteMap?: NonNullable<ExprContext['site']>,
): ExprContext {
  const sm = siteMap ?? siteExprMap(config, ctx as never);
  const secret = secretValues(config, ctx.secretsSet, deferFor(ctx.mode));
  return {
    deployment: { id: ctx.deploymentId },
    self: { slug: siteSlug, host: sm[siteSlug]?.host ?? null },
    var: varValues(config, deferFor(ctx.mode)),
    secret,
    site: sm,
    containers: siteContainersMap(config, ctx, siteSlug),
  };
}
/**
 * §21 #99 containers.<slug>.env.<NAME> of ONE site: the FINAL env value of every container (the image default
 * overridden by the container env, expressions resolved in dependency order with resolveContainerConfig — exactly what
 * the driver applies). value null = no value; secret = secretRef / secret.* value (masked "•••" with secretsSet);
 * error = the value failed (its code is re-raised at the reference). Unknown image → only the container env.
 */
export function siteContainersEnv(
  config: Config,
  ctx: Pick<ValidationContext, 'deploymentId' | 'secretsSet'> & Partial<Pick<ValidationContext, 'services' | 'images' | 'detectedHosts' | 'mode'>>,
  siteSlug: string,
  siteMap?: NonNullable<ExprContext['site']>,
): Record<string, Record<string, PeerEnv>> {
  const out: Record<string, Record<string, PeerEnv>> = {};
  const site = config.sites.find((s) => s.siteSlug === siteSlug);
  if (!site) return out;
  const sm = siteMap ?? siteExprMap(config, { services: {}, ...ctx } as never);
  const vctx = { services: {}, ...ctx } as Pick<ValidationContext, 'services' | 'deploymentId' | 'secretsSet'> & Partial<Pick<ValidationContext, 'images' | 'detectedHosts' | 'mode'>>;
  for (const c of site.config.containers ?? []) {
    if (!c || !isStr(c.containerSlug) || !Array.isArray(c.env)) continue;
    const env: Record<string, PeerEnv> = {};
    try {
      const img = ctx.images?.[imageKey(c.serviceSlug, c.version)];
      const ectx = containerExprContext(config, vctx, siteSlug, c, sm);
      const cr = resolveContainerConfig(config, vctx, c, { env: (img?.env ?? []).map(normalizeImageEnv), volumes: img?.volumes ?? [] }, ectx);
      for (const src of cr.env) {
        const r = cr.resolution.env[src.name];
        if (!r) continue;
        if (!r.ok) env[src.name] = { value: null, secret: false, error: { code: r.error.code, message: r.error.message } };
        else env[src.name] = { value: src.secretRef ? src.secretRef.value : src.src === null ? null : r.result.value, secret: r.result.secret };
      }
    } catch {
      /* a broken container (reported by validateConfig) has no env here */
    }
    out[c.containerSlug] = env;
  }
  return out;
}
/**
 * §21 #99 The expression context of dev-tool preset values (field `devtool`): the site file context (var, secret, site,
 * self, deployment, containers) + `containers[<slug>].env` (siteContainersEnv). The agent resolves presets with the same
 * context of its head config (Go validate.SiteDevtoolExprContext).
 */
export function siteDevtoolExprContext(
  config: Config,
  ctx: Pick<ValidationContext, 'deploymentId' | 'secretsSet'> & Partial<Pick<ValidationContext, 'services' | 'images' | 'detectedHosts' | 'mode'>>,
  siteSlug: string,
  siteMap?: NonNullable<ExprContext['site']>,
): ExprContext {
  const sm = siteMap ?? siteExprMap(config, { services: {}, ...ctx } as never);
  const base = siteFileExprContext(config, ctx, siteSlug, sm);
  const env = siteContainersEnv(config, ctx, siteSlug, sm);
  const containers: NonNullable<ExprContext['containers']> = {};
  for (const [slug, peer] of Object.entries(base.containers ?? {})) containers[slug] = { ...peer, env: env[slug] ?? {} };
  return { ...base, containers };
}
/** true when the mime is JSON (application/json or +json): the rendered output must parse (V_FILE_JSON). */
export const isJsonMime = (mime: string) => /^application\/([a-z0-9.+-]*\+)?json$/i.test(mime);
/** Check rendered JSON; returns null or the parse error text. */
export function jsonError(text: string): string | null {
  try {
    JSON.parse(text);
    return null;
  } catch (e) {
    return (e as Error).message;
  }
}

// ───────────────────────────────────────────────────────────── edge
export interface EdgeValidateOptions {
  base?: string;
  agentPort: number;
  agentGuiAliasPort: number | null;
  /** containers of the site: containerSlug → { enabled, hostPorts (resolved numbers), portNames of the image, edgePorts (ContainerPort.edge true) } */
  containers?: Record<string, { enabled: boolean; hostPorts: number[]; portNames: string[]; edgePorts?: string[]; slug: string }>;
  fileSlugs?: readonly string[];
  secretSlugs?: readonly string[];
}
export function isIpLiteral(s: string): boolean {
  return RE.ipv4.test(s) || (s.includes(':') && /^[0-9a-f:.]+$/i.test(s));
}
export function validateEdge(edge: Edge, o: EdgeValidateOptions, is: Issues) {
  const b = o.base ?? 'edge';
  checkPort(edge.httpPort, `${b}.httpPort`, is);
  if (edge.httpsPort !== null) checkPort(edge.httpsPort, `${b}.httpsPort`, is);
  if (edge.httpsPort !== null && edge.httpPort === edge.httpsPort) is.err(`${b}.httpsPort`, 'V_PORT_CONFLICT', 'httpPort and httpsPort must differ');
  for (const [k, p] of [['httpPort', edge.httpPort], ['httpsPort', edge.httpsPort]] as const) {
    if (p === null) continue;
    if (p === o.agentPort) is.err(`${b}.${k}`, 'V_PORT_CONFLICT', `${k} ${p} is the agentPort`);
    if (p === o.agentGuiAliasPort) is.err(`${b}.${k}`, 'V_PORT_CONFLICT', `${k} ${p} is the agentGuiAliasPort`);
  }
  if (!Array.isArray(edge.hosts)) return is.err(`${b}.hosts`, 'V_TYPE', 'hosts must be a list');
  if (!Array.isArray(edge.routes)) return is.err(`${b}.routes`, 'V_TYPE', 'routes must be a list');
  if (edge.hosts.length === 0) is.warn(`${b}.hosts`, 'V_EDGE_NO_HOSTS', 'no hosts: every request gets 404 (HTTP) or a TLS failure');
  if (edge.hosts.length > LIMITS.hosts) is.err(`${b}.hosts`, 'V_TOO_MANY', `at most ${LIMITS.hosts} hosts`);
  if (edge.routes.length > LIMITS.routes) is.err(`${b}.routes`, 'V_TOO_MANY', `at most ${LIMITS.routes} routes`);
  const names = edge.hosts.map((h) => (h.hostname ?? '').toLowerCase());
  edge.hosts.forEach((h: EdgeHost, i) => {
    const n = h.hostname;
    const at = `${b}.hosts[${n || i}]`;
    if (!isStr(n) || n === '') return is.err(`${at}.hostname`, 'V_REQUIRED', 'hostname is empty');
    if (names.indexOf(n.toLowerCase()) !== i) is.err(`${at}.hostname`, 'V_DUPLICATE', `host ${n} is listed two times`);
    if (isExpr(n)) checkExprField(n, 'edge', `${at}.hostname`, is);
    else if (n !== '*' && !isIpLiteral(n) && !RE.dnsName.test(n)) is.err(`${at}.hostname`, 'V_EDGE_HOST', `"${n}" is not a DNS name, a wildcard, an IP or "*"`);
    const t = h.tls;
    if (!t || !['none', 'internal', 'acme', 'files'].includes(t.mode)) return is.err(`${at}.tls.mode`, 'V_ENUM', 'tls.mode must be none | internal | acme | files');
    if (t.mode === 'acme') {
      if (n === '*' || isIpLiteral(n)) is.err(`${at}.tls`, 'V_EDGE_TLS', `ACME cannot issue a certificate for an IP or "*"; use internal`);
      if (!isStr(t.email) || !RE.email.test(t.email)) is.err(`${at}.tls.email`, 'V_FORMAT', 'ACME needs an email');
      if (!['http-01', 'tls-alpn-01', 'dns-01'].includes(t.challenge)) is.err(`${at}.tls.challenge`, 'V_ENUM', 'challenge must be http-01 | tls-alpn-01 | dns-01');
      if (n.startsWith('*.') && t.challenge !== 'dns-01') is.err(`${at}.tls.challenge`, 'V_EDGE_TLS', 'a wildcard certificate needs the dns-01 challenge');
      if (t.challenge === 'dns-01') {
        if (!t.dnsProvider || !t.dnsTokenSecretSlug) is.err(`${at}.tls`, 'V_EDGE_TLS', 'dns-01 needs dnsProvider and dnsTokenSecretSlug');
        else if (o.secretSlugs && !o.secretSlugs.includes(t.dnsTokenSecretSlug)) is.err(`${at}.tls.dnsTokenSecretSlug`, 'V_REF', `secret ${t.dnsTokenSecretSlug} does not exist`);
      } else {
        const need = t.challenge === 'http-01' ? 80 : 443;
        is.warn(`${at}.tls`, 'V_EDGE_ACME_INTERNET', `${t.challenge} needs internet on this site AND ${n} reachable from the internet on port ${need}`);
        if ((need === 80 ? edge.httpPort : edge.httpsPort) !== need) is.err(`${at}.tls.challenge`, 'V_EDGE_TLS', `${t.challenge} needs the edge on port ${need}`);
      }
    }
    if (t.mode === 'files') {
      if (!t.certFileSlug || !t.keySecretSlug) is.err(`${at}.tls`, 'V_EDGE_TLS', 'files mode needs certFileSlug and keySecretSlug');
      if (t.certFileSlug && o.fileSlugs && !o.fileSlugs.includes(t.certFileSlug)) is.err(`${at}.tls.certFileSlug`, 'V_REF', `file ${t.certFileSlug} does not exist`);
      if (t.keySecretSlug && o.secretSlugs && !o.secretSlugs.includes(t.keySecretSlug)) is.err(`${at}.tls.keySecretSlug`, 'V_REF', `secret ${t.keySecretSlug} does not exist`);
    }
    if (t.mode !== 'none' && edge.httpsPort === null) is.warn(`${at}.tls`, 'V_EDGE_TLS_NO_HTTPS', 'TLS is set but httpsPort is null (no HTTPS listener)');
    if (typeof h.redirectHttpToHttps !== 'boolean') is.err(`${at}.redirectHttpToHttps`, 'V_TYPE', 'must be true / false');
    else if (h.redirectHttpToHttps && (t.mode === 'none' || edge.httpsPort === null)) is.err(`${at}.redirectHttpToHttps`, 'V_EDGE_TLS', 'redirectHttpToHttps needs TLS and an httpsPort');
  });
  // edge level body limit (null = unlimited)
  if (edge.maxBodyBytes !== null && edge.maxBodyBytes !== undefined && (!isInt(edge.maxBodyBytes) || edge.maxBodyBytes < 0 || edge.maxBodyBytes > EDGE_DEFAULTS.maxBodyLimit))
    is.err(`${b}.maxBodyBytes`, 'V_EDGE_BODY', 'maxBodyBytes must be null (unlimited) or 0–1 TiB in bytes');
  edge.hosts.forEach((h, i) => {
    if (h.responseHeaders !== undefined && h.responseHeaders !== null) checkResponseHeaders(h.responseHeaders, `${b}.hosts[${h.hostname || i}].responseHeaders`, is);
  });
  const ids = edge.routes.map((r) => r.routeId);
  const matchKey = (r: EdgeRoute) => JSON.stringify([r.match?.kind ?? 'prefix', r.match?.path, (r.hosts ?? []).map((x) => String(x).toLowerCase()).sort(), (r.match?.methods ?? []).slice().sort()]);
  const keys = edge.routes.map(matchKey);
  edge.routes.forEach((r: EdgeRoute, i) => {
    const at = `${b}.routes[${r.routeId}]`;
    if (!isStr(r.routeId) || !RE.routeId.test(r.routeId)) is.err(`${at}.routeId`, 'V_FORMAT', `route id "${String(r.routeId)}" is not a slug (^[a-z0-9][a-z0-9-]{0,62}$)`);
    if (ids.indexOf(r.routeId) !== ids.lastIndexOf(r.routeId)) is.err(`${at}.routeId`, 'V_DUPLICATE', `route id ${r.routeId} is used two times`);
    if (!isInt(r.order) || r.order < 1) is.err(`${at}.order`, 'V_RANGE', 'order must be an integer ≥ 1');
    const kind = r.match?.kind;
    const p = r.match?.path;
    if (!['prefix', 'exact', 'regex'].includes(kind)) is.err(`${at}.match.kind`, 'V_ENUM', 'match.kind must be prefix | exact | regex');
    else if (kind === 'regex') {
      const e = isStr(p) ? checkEdgeRegex(p) : 'the regex is empty';
      if (e) is.err(`${at}.match.path`, 'V_EDGE_REGEX', e);
    } else if (!isStr(p) || !p.startsWith('/') || /[\s?#]/.test(p)) is.err(`${at}.match.path`, 'V_EDGE_ROUTE', 'the path must start with "/" and hold no spaces, "?" or "#"');
    else if (p === '/_opm' || p.startsWith('/_opm/')) is.err(`${at}.match.path`, 'V_RESERVED', '/_opm/ is reserved for the agent');
    if (typeof r.match?.stopRegex !== 'boolean') is.err(`${at}.match.stopRegex`, 'V_TYPE', 'stopRegex must be true / false');
    else if (r.match.stopRegex && kind !== 'prefix') is.err(`${at}.match.stopRegex`, 'V_EDGE_ROUTE', '"stop regex" (nginx ^~) is only for prefix routes');
    if (keys.indexOf(keys[i]!) !== i) is.err(`${at}.match`, 'V_DUPLICATE', `route ${r.routeId} has the same match as route ${edge.routes[keys.indexOf(keys[i]!)]!.routeId} (it would never be used)`);
    if (!Array.isArray(r.hosts)) is.err(`${at}.hosts`, 'V_TYPE', 'hosts must be a list');
    else for (const x of r.hosts) if (!edge.hosts.some((h) => h.hostname.toLowerCase() === String(x).toLowerCase())) is.err(`${at}.hosts`, 'V_REF', `host ${x} is not in edge.hosts`);
    const ms = r.match?.methods;
    if (ms !== null && ms !== undefined) {
      if (!Array.isArray(ms)) is.err(`${at}.match.methods`, 'V_TYPE', 'methods must be null or a list');
      else for (const m of ms) if (!(EDGE_METHODS as readonly string[]).includes(m)) is.err(`${at}.match.methods`, 'V_ENUM', `method ${m} is not one of ${EDGE_METHODS.join(', ')}`);
    }
    const a = r.action;
    if (!a || (a.type !== 'proxy' && a.type !== 'redirect' && a.type !== 'rewrite')) return is.err(`${at}.action.type`, 'V_ENUM', 'action.type must be proxy | redirect | rewrite');
    if (r.responseHeaders !== null && r.responseHeaders !== undefined) {
      if (a.type === 'rewrite') is.err(`${at}.responseHeaders`, 'V_EDGE_HEADER', 'a rewrite route has no response of its own: set the headers on the route it rewrites to');
      else checkResponseHeaders(r.responseHeaders, `${at}.responseHeaders`, is);
    }
    if (a.type === 'proxy') {
      const c = o.containers?.[a.containerSlug];
      if (!isStr(a.containerSlug) || !a.containerSlug) is.err(`${at}.action.containerSlug`, 'V_REQUIRED', 'pick the container this route proxies to');
      else if (o.containers && !c) is.err(`${at}.action.containerSlug`, 'V_REF', `container ${a.containerSlug} does not exist on this site`);
      else if (c && !c.portNames.includes(a.portName)) is.err(`${at}.action.portName`, a.portName ? 'V_REF' : 'V_REQUIRED', a.portName ? `port ${a.portName} does not exist on the image of ${c.slug}` : `pick the port of ${c.slug} this route proxies to`);
      else if (c && c.edgePorts && !c.edgePorts.includes(a.portName)) is.err(`${at}.action.portName`, 'V_EDGE_ROUTE', `port ${a.portName} of container ${c.slug} is closed to the edge (ports[].edge = false)`);
      else if (c && !c.enabled) is.warn(`${at}.action.containerSlug`, 'V_EDGE_TARGET_DISABLED', `container ${c.slug} is disabled: this route answers 503`);
      const rng = (k: 'connectTimeoutSec' | 'sendTimeoutSec' | 'readTimeoutSec', max: number) => {
        if (!isInt(a[k]) || a[k] < 1 || a[k] > max) is.err(`${at}.action.${k}`, 'V_RANGE', `${k} must be 1–${max} seconds`);
      };
      rng('connectTimeoutSec', EDGE_DEFAULTS.maxConnectTimeoutSec);
      rng('sendTimeoutSec', EDGE_DEFAULTS.maxTimeoutSec);
      rng('readTimeoutSec', EDGE_DEFAULTS.maxTimeoutSec);
      for (const k of ['stripPrefix', 'websocket', 'requestBuffering', 'responseBuffering'] as const) if (typeof a[k] !== 'boolean') is.err(`${at}.action.${k}`, 'V_TYPE', `${k} must be true / false`);
      if (a.maxBodyBytes !== null && (!isInt(a.maxBodyBytes) || a.maxBodyBytes < 0 || a.maxBodyBytes > EDGE_DEFAULTS.maxBodyLimit))
        is.err(`${at}.action.maxBodyBytes`, 'V_EDGE_BODY', 'maxBodyBytes must be null (inherit the edge), 0 (unlimited) or up to 1 TiB in bytes');
      if (a.addPrefix !== null && (!isStr(a.addPrefix) || !a.addPrefix.startsWith('/'))) is.err(`${at}.action.addPrefix`, 'V_EDGE_ROUTE', 'addPrefix must start with "/"');
      if (a.stripPrefix && kind === 'regex') is.err(`${at}.action.stripPrefix`, 'V_EDGE_ROUTE', 'strip prefix needs a prefix or exact route; use rewrite for a regex route');
      if (a.rewrite !== null && a.rewrite !== undefined) {
        if (a.stripPrefix) is.err(`${at}.action.rewrite`, 'V_EDGE_ROUTE', 'use either strip prefix or rewrite, not both');
        checkRewrite(a.rewrite.regex, a.rewrite.replacement, `${at}.action.rewrite`, is);
      }
      for (const [k, v] of Object.entries(a.setHeaders ?? {})) {
        if (!RE.headerName.test(k)) is.err(`${at}.action.setHeaders`, 'V_FORMAT', `header name "${k}" is not valid`);
        else if ((HOP_BY_HOP_HEADERS as readonly string[]).some((x) => x.toLowerCase() === k.toLowerCase()))
          is.warn(`${at}.action.setHeaders.${k}`, 'V_EDGE_HOP', `${k} is managed by the edge (WebSocket upgrades and keep-alive work without it); the value is ignored`);
        if (!isStr(v)) is.err(`${at}.action.setHeaders.${k}`, 'V_TYPE', 'header value must be a string');
        else if (isExpr(v)) checkExprField(v, 'edge', `${at}.action.setHeaders.${k}`, is);
        if (isStr(v) && isResponseOnlyHeader(k, v))
          is.warn(
            `${at}.action.setHeaders.${k}`,
            'W_RESPONSE_HEADER_AS_REQUEST',
            `${k} is set as a REQUEST header (sent to the container), but it only means something on responses. For nginx add_header / proxy_hide_header use "Response headers → browser" (fix: moveToResponseHeaders).`,
          );
      }
      for (const k of a.removeHeaders ?? []) if (!RE.headerName.test(k)) is.err(`${at}.action.removeHeaders`, 'V_FORMAT', `header name "${k}" is not valid`);
    } else if (a.type === 'redirect') {
      if (!isStr(a.to) || a.to === '') is.err(`${at}.action.to`, 'V_REQUIRED', 'redirect needs "to"');
      else if (isExpr(a.to)) checkExprField(a.to, 'edge', `${at}.action.to`, is);
      if (![301, 302, 303, 307, 308].includes(a.status)) is.err(`${at}.action.status`, 'V_ENUM', 'status must be 301, 302, 303, 307 or 308');
      if (typeof a.keepPath !== 'boolean') is.err(`${at}.action.keepPath`, 'V_TYPE', 'keepPath must be true / false');
      else if (a.keepPath && kind !== 'prefix') is.err(`${at}.action.keepPath`, 'V_EDGE_ROUTE', 'keep path needs a prefix route; use $uri / $1 in "to" for exact and regex routes');
    } else {
      checkRewrite(a.regex, a.replacement, `${at}.action`, is);
    }
  });
}
/**
 * True when a request header with this name / value only makes sense on a RESPONSE (it was meant for the browser):
 * Cache-Control with public / private / max-age / s-maxage / no-store / must-revalidate / proxy-revalidate / immutable,
 * Expires, Pragma, Set-Cookie, Content-Security-Policy(-Report-Only), Strict-Transport-Security, X-Frame-Options,
 * Access-Control-Allow-* / Access-Control-Expose-Headers / Access-Control-Max-Age.
 */
export function isResponseOnlyHeader(name: string, value = ''): boolean {
  const n = name.toLowerCase();
  if (n === 'cache-control') return /\b(public|private|max-age|s-maxage|no-store|must-revalidate|proxy-revalidate|immutable)\b/i.test(value);
  if (['expires', 'pragma', 'set-cookie', 'content-security-policy', 'content-security-policy-report-only', 'strict-transport-security', 'x-frame-options'].includes(n)) return true;
  return n.startsWith('access-control-allow-') || n === 'access-control-expose-headers' || n === 'access-control-max-age';
}
/**
 * One-click fix of W_RESPONSE_HEADER_AS_REQUEST (pure; returns a new route): move request header `name` of a proxy route
 * to the route's response headers as `set` (nginx proxy_hide_header + add_header: replaces the upstream value). A route
 * that inherited the host's response headers (null) starts from a COPY of the host's list, so nothing the host added is
 * lost (nginx: a route's own list replaces the host's).
 */
export function moveToResponseHeaders(route: EdgeRoute, name: string, host?: Pick<EdgeHost, 'responseHeaders'> | null): EdgeRoute {
  if (route.action.type !== 'proxy') return route;
  const key = Object.keys(route.action.setHeaders).find((k) => k.toLowerCase() === name.toLowerCase());
  if (key === undefined) return route;
  const value = route.action.setHeaders[key]!;
  const { [key]: _drop, ...rest } = route.action.setHeaders;
  void _drop;
  const base: EdgeResponseHeaders = structuredClone(route.responseHeaders ?? host?.responseHeaders ?? emptyResponseHeaders());
  const strip = (m: Record<string, string>) => Object.fromEntries(Object.entries(m).filter(([k]) => k.toLowerCase() !== key.toLowerCase()));
  base.add = strip(base.add);
  base.set = { ...strip(base.set), [key]: value };
  base.remove = base.remove.filter((k) => k.toLowerCase() !== key.toLowerCase());
  return { ...route, action: { ...route.action, setHeaders: rest }, responseHeaders: base };
}
/** §21 #101 tree / single entries with render 'expressions' (sha256 → where), rendered once per site like Files */
export type ContentRenders = Map<string, string[]>;
function checkSeed(v: SiteVolume, vp: string, config: Config, is: Issues, renders: Set<string>, contentRenders?: ContentRenders) {
  checkPersist(v, vp, is);
  checkBackupPolicy(v, vp, is); // §21 #112
  const sd = v.seed;
  if (sd === null || sd === undefined) return;
  if (typeof sd !== 'object' || !['tree', 'single', 'files', 'file', 'archive'].includes((sd as { kind?: string }).kind ?? '')) return is.err(`${vp}.seed`, 'V_VOLUME_SEED', 'seed must be null, tree, single or archive');
  const kind = v.kind ?? 'dir';
  // §21 #101 Files are static and read-only: never volume content. Legacy seeds survive only in old revisions (agent mode).
  if ((sd.kind === 'files' || sd.kind === 'file') && is.mode !== 'agent')
    return is.err(
      `${vp}.seed`,
      'V_VOLUME_SEED_FILE_REF',
      `volume ${v.volumeSlug} takes its starter content from Files (${sd.kind === 'file' ? sd.fileSlug : sd.entries.map((e) => e.fileSlug).join(', ')}) — Files are static and read-only and are never volume content: give the volume its own content (Volume → Content; backend/scripts/migrate-h.ts converts it)`,
    );
  // §21 #117 a persist-to-cloud volume's seed is its COMMITTED content: a literal tree within the persisted limits
  const persist = v.persist && typeof v.persist === 'object' ? v.persist : null;
  if (persist && sd.kind !== 'tree') return is.err(`${vp}.seed`, 'V_VOLUME_SEED', `volume ${v.volumeSlug} persists to the cloud: its content is a file tree (kind tree)`);
  if (sd.kind === 'tree') {
    if (kind !== 'dir') return is.err(`${vp}.seed`, 'V_VOLUME_SEED', 'a single-file volume has one file of content (kind single), not a tree');
    for (const x of volumeTreeIssues(sd, !persist)) is.err(x.path ? `${vp}.seed.entries[${x.path}]` : `${vp}.seed`, x.code === 'V_VOLUME_CONTENT' ? 'V_VOLUME_SEED' : x.code, `volume ${v.volumeSlug}: ${x.message}`);
    if (persist) checkPersistContent(v, sd, persist, vp, is);
    else if (contentRenders) for (const e of sd.entries ?? []) if (e?.render === 'expressions' && isStr(e.sha256)) contentRenders.set(e.sha256, [...(contentRenders.get(e.sha256) ?? []), `${vp}.seed.entries[${e.path}]`]);
    return;
  }
  if (sd.kind === 'single') {
    if (kind !== 'file') return is.err(`${vp}.seed`, 'V_VOLUME_SEED', 'a folder volume has a tree of content (kind tree), not a single file');
    const at = `${vp}.seed`;
    if (!isStr(sd.sha256) || !RE.sha256.test(sd.sha256)) is.err(`${at}.sha256`, 'V_FORMAT', 'upload the content');
    if (!isInt(sd.size) || sd.size < 0 || sd.size > VOLUME_CONTENT_LIMITS.fileMaxBytes) is.err(`${at}.size`, 'V_TOO_LARGE', `the file is 0 B – ${VOLUME_CONTENT_LIMITS.fileMaxBytes / 1048576} MiB`);
    if (!isStr(sd.mode) || !/^0[0-7]{3}$/.test(sd.mode)) is.err(`${at}.mode`, 'V_FORMAT', 'mode must look like 0644');
    if (sd.render !== 'none' && sd.render !== 'expressions') is.err(`${at}.render`, 'V_ENUM', 'render must be none | expressions');
    else if (sd.render === 'expressions' && contentRenders && isStr(sd.sha256)) contentRenders.set(sd.sha256, [...(contentRenders.get(sd.sha256) ?? []), at]);
    return;
  }
  if (sd.kind === 'file' && kind !== 'file') return is.err(`${vp}.seed`, 'V_VOLUME_SEED', 'a folder volume starts from files or an archive (a single file seed is for single-file volumes)');
  if (sd.kind !== 'file' && kind === 'file') return is.err(`${vp}.seed`, 'V_VOLUME_SEED', 'a single-file volume starts from one file');
  const fileRef = (slug: unknown, at: string) => {
    const f = config.files.find((x) => x.fileSlug === slug);
    if (!f) return is.err(at, slug ? 'V_REF' : 'V_REQUIRED', slug ? `file ${String(slug)} does not exist` : 'pick the file with the starter content');
    if (!f.content) is.err(at, 'V_FILE_CONTENT', `file ${f.fileSlug} has no content`, 'publish');
    if (fileRenderMode(f) === 'expressions') renders.add(f.fileSlug);
  };
  if (sd.kind === 'file') return fileRef(sd.fileSlug, `${vp}.seed.fileSlug`);
  if (sd.kind === 'files') {
    if (!Array.isArray(sd.entries) || sd.entries.length < 1 || sd.entries.length > 100) return is.err(`${vp}.seed.entries`, 'V_VOLUME_SEED', 'starter files need 1–100 entries');
    const paths = sd.entries.map((e) => e?.path);
    sd.entries.forEach((e, i) => {
      const at = `${vp}.seed.entries[${i}]`;
      const pe = seedPathError(e?.path);
      if (pe) is.err(`${at}.path`, 'V_VOLUME_SEED', pe);
      else if (paths.indexOf(e.path) !== i) is.err(`${at}.path`, 'V_DUPLICATE', `${e.path} is listed twice`);
      else if (paths.some((q, k) => k !== i && isStr(q) && q.startsWith(e.path + '/'))) is.err(`${at}.path`, 'V_VOLUME_SEED', `${e.path} is a file, but another entry puts files inside it`);
      fileRef(e?.fileSlug, `${at}.fileSlug`);
    });
    return;
  }
  if (!isStr(sd.blobSha256) || !RE.sha256.test(sd.blobSha256)) is.err(`${vp}.seed.blobSha256`, 'V_FORMAT', 'upload the archive (.tar.gz)');
  if (!isStr(sd.name) || !/\.(tar\.gz|tgz)$/i.test(sd.name) || sd.name.length > 200) is.err(`${vp}.seed.name`, 'V_VOLUME_SEED', 'the archive must be a .tar.gz / .tgz');
  if (!isInt(sd.size) || sd.size < 1 || sd.size > SEED_ARCHIVE_MAX) is.err(`${vp}.seed.size`, 'V_VOLUME_SEED', `the archive must be 1 B – ${SEED_ARCHIVE_MAX / 1048576} MiB`);
}
/** §21 #117 the committed content of a persist volume: at most persist.maxBytes, nothing under the excludes. */
function checkPersistContent(v: SiteVolume, sd: VolumeTree, persist: VolumePersist, vp: string, is: Issues) {
  let total = 0;
  for (const e of sd.entries ?? []) if (isInt(e?.size)) total += e.size;
  if (isInt(persist.maxBytes) && total > persist.maxBytes) is.err(`${vp}.seed`, 'V_TOO_LARGE', `volume ${v.volumeSlug}: the content is ${formatContentBytes(total)} — at most ${formatContentBytes(persist.maxBytes)} (Persist to cloud › size cap)`);
  if (!Array.isArray(persist.exclude) || persist.exclude.some((p) => excludePatternError(p))) return;
  const ex = compileExcludes(persist.exclude);
  for (const e of sd.entries ?? []) if (isStr(e?.path) && isExcluded(e.path, false, ex)) is.err(`${vp}.seed.entries[${e.path}]`, 'V_VOLUME_CONTENT_EXCLUDED', `volume ${v.volumeSlug}: ${e.path} is excluded from the persisted content (it could never come back from the site)`);
  for (const d of sd.dirs ?? []) if (isStr(d) && isExcluded(d, true, ex)) is.err(`${vp}.seed.entries[${d}]`, 'V_VOLUME_CONTENT_EXCLUDED', `volume ${v.volumeSlug}: folder ${d} is excluded from the persisted content`);
}
/** §21 #101 SiteVolume.persist: folder volumes only; excludes, size cap, auto flag. */
/** §21 #112 SiteVolume.backupPolicy (absent = the daily site snapshot keeps 7) — Go checkBackupPolicy. */
function checkBackupPolicy(v: SiteVolume, vp: string, is: Issues) {
  const p = v.backupPolicy as unknown;
  if (p === undefined) return;
  const at = `${vp}.backupPolicy`;
  if (p === null || typeof p !== 'object' || Array.isArray(p)) return is.err(at, 'V_TYPE', 'backupPolicy must be absent (the daily site snapshot) or { schedule, keep }');
  const x = p as Record<string, unknown>;
  if (!(VOLUME_BACKUP_SCHEDULES as readonly unknown[]).includes(x.schedule)) is.err(`${at}.schedule`, 'V_ENUM', 'schedule must be off, hourly, 6h, daily or weekly');
  if (x.schedule !== 'off' && (typeof x.keep !== 'number' || !Number.isInteger(x.keep) || x.keep < 1 || x.keep > 100)) is.err(`${at}.keep`, 'V_RANGE', `keep must be 1 … 100 (got ${String(x.keep)})`);
  if (x.schedule !== 'off' && !v.backup) is.warn(at, 'V_VOLUME_BACKUP_OFF', `volume ${v.volumeSlug} has backup off: its automatic backups do not run`);
}

function checkPersist(v: SiteVolume, vp: string, is: Issues) {
  const p = v.persist as unknown;
  if (p === undefined) return;
  const at = `${vp}.persist`;
  if (p === null || typeof p !== 'object' || Array.isArray(p)) return is.err(at, 'V_TYPE', 'persist must be absent (off) or { exclude, maxBytes, auto }');
  const x = p as Record<string, unknown>;
  if ((v.kind ?? 'dir') !== 'dir') is.err(at, 'V_VOLUME_PERSIST', `volume ${v.volumeSlug} is a single file: persist to cloud needs a folder volume`);
  if (!Array.isArray(x.exclude)) is.err(`${at}.exclude`, 'V_TYPE', 'exclude must be a list of patterns');
  else {
    if (x.exclude.length > VOLUME_CONTENT_LIMITS.excludeMax) is.err(`${at}.exclude`, 'V_TOO_MANY', `at most ${VOLUME_CONTENT_LIMITS.excludeMax} exclude patterns`);
    x.exclude.forEach((pat, i) => {
      const e = excludePatternError(pat);
      if (e) is.err(`${at}.exclude[${i}]`, 'V_VOLUME_PERSIST', e);
    });
    for (const d of dupes(x.exclude as string[], (q) => q)) is.err(`${at}.exclude`, 'V_DUPLICATE', `exclude pattern ${d} is listed twice`);
  }
  if (!isInt(x.maxBytes) || x.maxBytes < VOLUME_CONTENT_LIMITS.persistMinBytes || x.maxBytes > VOLUME_CONTENT_LIMITS.persistMaxBytes)
    is.err(`${at}.maxBytes`, 'V_RANGE', `the snapshot size cap is ${VOLUME_CONTENT_LIMITS.persistMinBytes / 1048576} – ${VOLUME_CONTENT_LIMITS.persistMaxBytes / 1048576} MiB`);
  if (typeof x.auto !== 'boolean') is.err(`${at}.auto`, 'V_TYPE', 'auto must be true / false');
  for (const k of Object.keys(x)) if (!['exclude', 'maxBytes', 'auto'].includes(k)) is.err(`${at}.${k}`, 'V_FORMAT', `persist has no field ${k}`);
}
function checkRewrite(regex: unknown, replacement: unknown, at: string, is: Issues) {
  const e = isStr(regex) ? checkEdgeRegex(regex) : 'the regex is empty';
  if (e) is.err(`${at}.regex`, 'V_EDGE_REGEX', e);
  if (!isStr(replacement) || replacement === '') is.err(`${at}.replacement`, 'V_REQUIRED', 'the rewrite needs a replacement (the new path, e.g. $1 or /x$request_uri)');
  else if (isExpr(replacement)) checkExprField(replacement, 'edge', `${at}.replacement`, is);
}
function checkResponseHeaders(h: EdgeResponseHeaders, at: string, is: Issues) {
  if (!h || typeof h !== 'object') return is.err(at, 'V_TYPE', 'responseHeaders must be an object');
  const names: string[] = [];
  for (const part of ['set', 'add'] as const) {
    const m = h[part];
    if (!m || typeof m !== 'object' || Array.isArray(m)) {
      is.err(`${at}.${part}`, 'V_TYPE', `${part} must be an object of header → value`);
      continue;
    }
    for (const [k, v] of Object.entries(m)) {
      if (!RE.headerName.test(k)) is.err(`${at}.${part}`, 'V_FORMAT', `header name "${k}" is not valid`);
      if (names.some((x) => x.toLowerCase() === k.toLowerCase())) is.err(`${at}.${part}.${k}`, 'V_EDGE_HEADER', `${k} is in both set and add: use one`);
      names.push(k);
      if (!isStr(v)) is.err(`${at}.${part}.${k}`, 'V_TYPE', 'header value must be a string');
      else if (isExpr(v)) checkExprField(v, 'edge', `${at}.${part}.${k}`, is);
    }
  }
  if (!Array.isArray(h.remove)) is.err(`${at}.remove`, 'V_TYPE', 'remove must be a list');
  else for (const k of h.remove) if (!isStr(k) || !RE.headerName.test(k)) is.err(`${at}.remove`, 'V_FORMAT', `header name "${String(k)}" is not valid`);
  if (!Array.isArray(h.always)) is.err(`${at}.always`, 'V_TYPE', 'always must be a list');
  else for (const k of h.always) if (!names.some((x) => x.toLowerCase() === String(k).toLowerCase())) is.err(`${at}.always`, 'V_EDGE_HEADER', `"always" lists ${String(k)}, which is not in set / add`);
}

// ───────────────────────────────────────────────────────────── config
export function defaultEdge(): Edge {
  return { httpPort: 80, httpsPort: 443, maxBodyBytes: null, hosts: [newEdgeHost('*')], routes: [] };
}
/** A new edge host (TLS none, no redirect, no response headers). */
export function newEdgeHost(hostname: string): EdgeHost {
  return { hostname, tls: { mode: 'none' }, redirectHttpToHttps: false, responseHeaders: emptyResponseHeaders() };
}
/** A new proxy action with the round E defaults (websocket on, 60 s timeouts, streaming, inherit body limit). */
export function newProxyAction(containerSlug = '', portName = ''): EdgeProxyAction {
  return {
    type: 'proxy',
    containerSlug,
    portName,
    stripPrefix: false,
    rewrite: null,
    addPrefix: null,
    setHeaders: {},
    removeHeaders: [],
    websocket: true,
    connectTimeoutSec: EDGE_DEFAULTS.connectTimeoutSec,
    sendTimeoutSec: EDGE_DEFAULTS.sendTimeoutSec,
    readTimeoutSec: EDGE_DEFAULTS.readTimeoutSec,
    requestBuffering: false,
    responseBuffering: false,
    maxBodyBytes: null,
  };
}
/** A new prefix route. */
export function newEdgeRoute(routeId: string, path: string, action: EdgeRoute['action'], order = 1): EdgeRoute {
  return { routeId, hosts: [], match: { kind: 'prefix', path, stopRegex: false, methods: null }, action, order, responseHeaders: null };
}
/**
 * Normalize an edge (pure): legacy shapes → round E. match {pathPrefix} → {kind 'prefix', path, stopRegex false};
 * proxy timeoutSec → readTimeoutSec (connect / send 60), missing rewrite / buffering / maxBodyBytes → defaults (websocket
 * KEPT as stored); route responseHeaders missing → null; host responseHeaders missing → empty; edge maxBodyBytes missing → null.
 */
export function normalizeEdge(e: Partial<Edge> & Record<string, unknown>): Edge {
  const d = defaultEdge();
  const rh = (h: unknown): EdgeResponseHeaders => {
    const x = (h ?? {}) as Partial<EdgeResponseHeaders>;
    return { set: x.set ?? {}, add: x.add ?? {}, remove: x.remove ?? [], always: x.always ?? [] };
  };
  return {
    httpPort: e.httpPort ?? 80,
    httpsPort: e.httpsPort === undefined ? 443 : e.httpsPort,
    maxBodyBytes: e.maxBodyBytes ?? null,
    hosts: (e.hosts ?? d.hosts).map((h) => ({ ...h, responseHeaders: rh((h as Partial<EdgeHost>).responseHeaders) })),
    routes: (e.routes ?? []).map((r0) => {
      const r = r0 as EdgeRoute & { match: EdgeRoute['match'] & { pathPrefix?: string } };
      const a0 = r.action as EdgeRoute['action'] & { serviceSlug?: string; timeoutSec?: number };
      let action: EdgeRoute['action'] = a0;
      if (a0?.type === 'proxy') {
        const x = a0 as Partial<EdgeProxyAction> & { serviceSlug?: string; timeoutSec?: number };
        const legacyRead = typeof x.timeoutSec === 'number' ? x.timeoutSec : undefined;
        const { serviceSlug, timeoutSec, ...rest } = x;
        void timeoutSec;
        action = {
          ...newProxyAction(),
          ...rest,
          type: 'proxy',
          containerSlug: x.containerSlug ?? serviceSlug ?? '',
          portName: x.portName ?? '',
          rewrite: x.rewrite ?? null,
          addPrefix: x.addPrefix ?? null,
          setHeaders: x.setHeaders ?? {},
          removeHeaders: x.removeHeaders ?? [],
          websocket: x.websocket ?? true,
          readTimeoutSec: x.readTimeoutSec ?? legacyRead ?? EDGE_DEFAULTS.readTimeoutSec,
          maxBodyBytes: x.maxBodyBytes ?? null,
        };
      }
      const m = r.match ?? ({} as EdgeRoute['match'] & { pathPrefix?: string });
      const kind = m.kind ?? 'prefix';
      const { pathPrefix, ...mrest } = m;
      return {
        ...r,
        action,
        hosts: r.hosts ?? [],
        match: { ...mrest, kind, path: m.path ?? pathPrefix ?? '/', stopRegex: kind === 'prefix' ? (m.stopRegex ?? false) : false, methods: m.methods?.length ? m.methods : null },
        responseHeaders: r.responseHeaders ? rh(r.responseHeaders) : null,
      };
    }),
  };
}
/** A new site config. host null = auto-detect (default). */
export function defaultSiteConfig(host: string | null = null): SiteConfig {
  return { host, agentPort: 7080, agentGuiAliasPort: 8080, volumes: [], containers: [], depends: [], notFoundPageId: null, edge: defaultEdge() };
}
export function emptyConfig(): Config {
  return { variables: [], secrets: [], files: [], sites: [] };
}
/**
 * A new container instance of an image: one port row per image port (not published, edge on), NO volume rows (every image
 * volume starts "not mounted" with warning V_VOLUME_NOT_MOUNTED until the user picks a declared site volume / file / secret /
 * host path / Not mounted), env: required entries without default get value "" (choice: the first option; secret:
 * secretRef "") so the editor shows them.
 */
export function defaultContainer(serviceSlug: string, version: string, image: CatalogImage, containerSlug: string = serviceSlug): Container {
  return {
    containerSlug,
    serviceSlug,
    version,
    enabled: true,
    env: image.env.map(normalizeImageEnv).filter((e) => e.required && e.value === undefined).map((e) => ({ name: e.name, value: e.secret ? null : (e.options?.[0] ?? ''), secretRef: e.secret ? '' : null })),
    ports: image.ports.map((p) => ({ portName: p.name, hostPort: null, hostIp: '0.0.0.0' as const, edge: true })),
    volumes: [],
    command: null,
    entrypoint: null,
    restart: image.kind === 'job' ? 'never' : 'always',
    resources: { cpus: null, memoryMb: null },
    healthcheck: null,
    depends: [],
  };
}
/**
 * Copy a container under a new name ("Duplicate container"): same image, env, ports (host ports cleared so they do not
 * conflict), volumes (a volume named `<old>-<imageVolume>` becomes `<new>-<imageVolume>` — the caller declares it, see
 * undeclaredVolumes; every other volume / host / file / secret source is kept, i.e. shared), runtime.
 */
export function duplicateContainer(c: Container, containerSlug: string): Container {
  const copy = JSON.parse(JSON.stringify(c)) as Container;
  copy.containerSlug = containerSlug;
  copy.ports = copy.ports.map((p) => ({ ...p, hostPort: null }));
  copy.volumes = copy.volumes.map((v) =>
    v.imageVolume && v.source.kind === 'volume' && v.source.volumeSlug === defaultVolumeSlug(c.containerSlug, v.imageVolume) ? { ...v, source: { kind: 'volume', volumeSlug: defaultVolumeSlug(containerSlug, v.imageVolume) } } : v,
  );
  copy.depends = copy.depends.filter((d) => d.containerSlug !== containerSlug);
  return copy;
}

/** Validate a whole Config (template, cloud draft, publish, or agent staging). */
export function validateConfig(config: Config, ctx: ValidationContext): Issues {
  const is = new Issues(ctx.mode);
  const template = ctx.mode === 'template';
  if (!config || typeof config !== 'object') {
    is.err('', 'V_TYPE', 'config must be an object');
    return is;
  }
  for (const k of ['variables', 'secrets', 'files', 'sites'] as const) if (!Array.isArray(config[k])) is.err(k, 'V_TYPE', `${k} must be a list`);
  if (!is.ok) return is;
  // sections
  if (config.variables.length > LIMITS.variables) is.err('variables', 'V_TOO_MANY', `at most ${LIMITS.variables}`);
  if (config.secrets.length > LIMITS.secrets) is.err('secrets', 'V_TOO_MANY', `at most ${LIMITS.secrets}`);
  if (config.files.length > LIMITS.files) is.err('files', 'V_TOO_MANY', `at most ${LIMITS.files}`);
  if (config.sites.length > LIMITS.sites) is.err('sites', 'V_TOO_MANY', `at most ${LIMITS.sites}`);
  for (const d of dupes(config.variables, (v) => v.variableSlug)) is.err(`variables[${d}]`, 'V_DUPLICATE', `variable ${d} is defined twice`);
  for (const d of dupes(config.secrets, (v) => v.secretSlug)) is.err(`secrets[${d}]`, 'V_DUPLICATE', `secret ${d} is defined twice`);
  for (const d of dupes(config.files, (v) => v.fileSlug)) is.err(`files[${d}]`, 'V_DUPLICATE', `file ${d} is defined twice`);
  for (const d of dupes(config.sites, (v) => v.siteSlug)) is.err(`sites[${d}]`, 'V_DUPLICATE', `site ${d} is defined twice`);
  config.variables.forEach((v) => validateVariable(v, `variables[${v.variableSlug}]`, is, ctx.mode));
  config.secrets.forEach((s) => {
    if (ctx.secretsSet && !template) {
      const set = ctx.secretsSet.includes(s.secretSlug);
      validateSecretDef({ ...s, value: set ? 'x' : null }, `secrets[${s.secretSlug}]`, is, false);
    } else validateSecretDef(s, `secrets[${s.secretSlug}]`, is, template);
  });
  config.files.forEach((f) => validateFileDef(f, `files[${f.fileSlug}]`, is));
  if (config.sites.length === 0) is.err('sites', 'V_NO_SITES', 'a deployment needs at least one site', 'publish');

  const siteSlugs = config.sites.map((s) => s.siteSlug);
  const fileSlugs = config.files.map((f) => f.fileSlug);
  const secretSlugs = config.secrets.map((s) => s.secretSlug);
  const hosts = resolveSiteHosts(config, ctx.deploymentId, { detected: ctx.detectedHosts, placeholder: ctx.mode !== 'agent', defer: deferFor(ctx.mode) });
  const siteMap = siteExprMap(config, ctx, hosts);
  const so = siteOrder(config);
  if ('cycle' in so) is.err('sites', 'V_CYCLE', `site depends form a cycle: ${so.cycle.join(' → ')}`);

  for (const site of config.sites) {
    validateSite(site, config, ctx, is, { siteSlugs, fileSlugs, secretSlugs, hosts, siteMap });
    validateSiteDevTools(site, config, ctx.images, is);
  }
  return is;
}

function validateSite(
  site: Site,
  config: Config,
  ctx: ValidationContext,
  is: Issues,
  g: { siteSlugs: string[]; fileSlugs: string[]; secretSlugs: string[]; hosts: Record<string, SiteHost>; siteMap: NonNullable<ExprContext['site']> },
) {
  const b = `sites[${site.siteSlug}]`;
  checkSlug(site.siteSlug, `${b}.siteSlug`, is);
  if ((RESERVED_SITE_SLUGS as readonly string[]).includes(site.siteSlug)) is.err(`${b}.siteSlug`, 'V_RESERVED', `"${site.siteSlug}" is reserved (agent GUI routes)`);
  checkLabel(site.label, `${b}.label`, is);
  const sc = site.config;
  if (!sc || typeof sc !== 'object') return is.err(`${b}.config`, 'V_TYPE', 'config must be an object');
  const cb = `${b}.config`;
  // host: null / "" = auto (no issue); an expression with no value falls back to auto (warning only)
  if (sc.host !== null && sc.host !== undefined && !isStr(sc.host)) is.err(`${cb}.host`, 'V_TYPE', 'host must be null (auto), an IP, a DNS name or {{ var.… }}');
  else if (isStr(sc.host) && sc.host.trim() !== '') {
    if (sc.host.length > LIMITS.value) is.err(`${cb}.host`, 'V_LENGTH', `at most ${LIMITS.value} characters`);
    if (isExpr(sc.host)) checkExprField(sc.host, 'host', `${cb}.host`, is);
    const h = g.hosts[site.siteSlug];
    const syntaxOk = !isExpr(sc.host) || checkExpr(sc.host, 'host').length === 0;
    if (h?.fallback) {
      if (ctx.mode !== 'template') is.warn(`${cb}.host`, 'V_HOST_AUTO', h.fallback.message);
    } else if (h?.error && syntaxOk) is.err(`${cb}.host`, h.error.code, h.error.message, 'publish');
    else if (h && !h.auto && h.host !== null && !isDeferredValue(h.host) && !isIpLiteral(h.host) && !(RE.dnsName.test(h.host) && !h.host.startsWith('*'))) is.err(`${cb}.host`, 'V_HOST', `"${h.host}" is not an IP or a DNS name`, 'publish');
  }
  // ports of the agent
  checkPort(sc.agentPort, `${cb}.agentPort`, is, 1024);
  if (sc.agentGuiAliasPort !== null) {
    checkPort(sc.agentGuiAliasPort, `${cb}.agentGuiAliasPort`, is, 1024);
    if (sc.agentGuiAliasPort === sc.agentPort) is.err(`${cb}.agentGuiAliasPort`, 'V_PORT_CONFLICT', `${sc.agentGuiAliasPort} is already used by agentPort`);
  }
  // depends
  if (!Array.isArray(sc.depends)) is.err(`${cb}.depends`, 'V_TYPE', 'depends must be a list');
  else
    for (const d of sc.depends) {
      if (d.siteSlug === site.siteSlug) is.err(`${cb}.depends`, 'V_CYCLE', 'a site cannot depend on itself');
      else if (!g.siteSlugs.includes(d.siteSlug)) is.err(`${cb}.depends`, 'V_REF', `site ${d.siteSlug} does not exist`);
    }
  if (sc.notFoundPageId !== null && ctx.notFoundPageIds && !ctx.notFoundPageIds.includes(sc.notFoundPageId)) is.err(`${cb}.notFoundPageId`, 'V_REF', `not-found page ${sc.notFoundPageId} does not exist`);
  // volumes (round D): the declared data folders of the site
  const declaredVolumes = new Set<string>();
  const seedRenders = new Set<string>(); // files used by volume seeds (rendered once per site like mounted files)
  const contentRenders: ContentRenders = new Map(); // §21 #101 volume content entries with render expressions
  if (sc.volumes === undefined || sc.volumes === null) is.err(`${cb}.volumes`, 'V_REQUIRED', 'volumes must be a list (may be empty)');
  else if (!Array.isArray(sc.volumes)) is.err(`${cb}.volumes`, 'V_TYPE', 'volumes must be a list');
  else {
    if (sc.volumes.length > LIMITS.volumesPerSite) is.err(`${cb}.volumes`, 'V_TOO_MANY', `at most ${LIMITS.volumesPerSite}`);
    for (const d of dupes(sc.volumes, (v) => v?.volumeSlug)) is.err(`${cb}.volumes[${d}]`, 'V_DUPLICATE', `volume ${d} is declared twice`);
    for (const v of sc.volumes) {
      if (!v || typeof v !== 'object') {
        is.err(`${cb}.volumes`, 'V_TYPE', 'a volume needs volumeSlug, label, description, backup');
        continue;
      }
      const vp = `${cb}.volumes[${v.volumeSlug}]`;
      if (!isStr(v.volumeSlug) || !RE.volumeSlug.test(v.volumeSlug)) is.err(`${vp}.volumeSlug`, 'V_FORMAT', `volume name "${String(v.volumeSlug)}" must match ^[a-z0-9][a-z0-9_.-]{0,99}$`);
      else declaredVolumes.add(v.volumeSlug);
      if (!isStr(v.label) || v.label.length > LIMITS.label) is.err(`${vp}.label`, 'V_LENGTH', `label must be a string of at most ${LIMITS.label} characters`);
      if (!isStr(v.description) || v.description.length > LIMITS.description) is.err(`${vp}.description`, 'V_LENGTH', `description must be a string of at most ${LIMITS.description} characters`);
      if (typeof v.backup !== 'boolean') is.err(`${vp}.backup`, 'V_TYPE', 'backup must be true / false');
      checkLocation(v.location, 'volumes', `${vp}.location`, is);
      if (v.kind !== 'dir' && v.kind !== 'file') is.err(`${vp}.kind`, 'V_VOLUME_KIND', 'kind must be dir | file');
      checkSeed(v, vp, config, is, seedRenders, contentRenders);
    }
    // locations of this site: the files (one copy per site) + the declared volumes; unique and never nested
    const locs: { what: string; path: string; at: string }[] = [];
    for (const f of config.files) if (f && isStr(f.fileSlug)) locs.push({ what: `file ${f.fileSlug}`, path: locationPath(f.location, 'files', f.fileSlug), at: `files[${f.fileSlug}].location` });
    for (const v of sc.volumes) if (v && isStr(v.volumeSlug)) locs.push({ what: `volume ${v.volumeSlug}`, path: locationPath(v.location, 'volumes', v.volumeSlug), at: `${cb}.volumes[${v.volumeSlug}].location` });
    for (let i = 0; i < locs.length; i++)
      for (let k = 0; k < i; k++) {
        const a = locs[i]!;
        const b = locs[k]!;
        if (a.path.startsWith('./') !== b.path.startsWith('./')) continue; // relative vs absolute: only the agent can compare (staging)
        if (nestedOrSame(a.path.replace(/\/+$/, ''), b.path.replace(/\/+$/, '')))
          is.err(a.at.startsWith('files[') && b.at.startsWith('files[') ? a.at : a.at.startsWith('files[') ? b.at : a.at, 'V_LOCATION', `${a.what} (${a.path}) and ${b.what} (${b.path}) are the same place or nested on site ${site.siteSlug}`);
      }
    if (Array.isArray(sc.containers)) {
      const used = volumeUsers(sc);
      for (const v of sc.volumes) if (v && isStr(v.volumeSlug) && !used[v.volumeSlug]) is.warn(`${cb}.volumes[${v.volumeSlug}]`, 'V_VOLUME_UNUSED', `volume ${v.volumeSlug} is declared but no container mounts it (its data stays on disk${v.backup ? ' and is still backed up' : ''})`);
    }
  }
  // containers
  if (!Array.isArray(sc.containers)) return is.err(`${cb}.containers`, 'V_TYPE', 'containers must be a list');
  if (sc.containers.length > LIMITS.containersPerSite) is.err(`${cb}.containers`, 'V_TOO_MANY', `at most ${LIMITS.containersPerSite}`);
  for (const d of dupes(sc.containers, (c) => c.containerSlug)) is.err(`${cb}.containers[${d}].containerSlug`, 'V_DUPLICATE', `container name ${d} is used twice on this site`);
  const vars = varValues(config, deferFor(ctx.mode));
  const listeners: { port: number; proto: 'tcp' | 'udp'; what: string; path: string }[] = [];
  listeners.push({ port: sc.agentPort, proto: 'tcp', what: 'agentPort', path: `${cb}.agentPort` });
  if (sc.agentGuiAliasPort !== null) listeners.push({ port: sc.agentGuiAliasPort, proto: 'tcp', what: 'agentGuiAliasPort', path: `${cb}.agentGuiAliasPort` });
  const edgePorts = [sc.edge?.httpPort, sc.edge?.httpsPort].filter((x): x is number => isInt(x));
  const containerInfo: NonNullable<EdgeValidateOptions['containers']> = {};
  const services = ctx.services;
  const slugs = sc.containers.map((c) => c.containerSlug);
  // file renders: fileSlug → mounting containers (rendered after the loop)
  const renders = new Set<string>(seedRenders); // fileSlugs with render 'expressions' mounted by an enabled container of this site (+ volume seeds)
  for (const c of sc.containers) {
    const at = `${cb}.containers[${c.containerSlug}]`;
    checkSlug(c.containerSlug, `${at}.containerSlug`, is);
    if ((RESERVED_CONTAINER_SLUGS as readonly string[]).includes(c.containerSlug)) is.err(`${at}.containerSlug`, 'V_RESERVED', `"${c.containerSlug}" is reserved (GUI route …/containers/new)`);
    const slug = services[c.serviceSlug] ? c.serviceSlug : undefined;
    if (!slug) {
      is.err(`${at}.serviceSlug`, 'V_REF', `service ${c.serviceSlug} does not exist`);
      continue;
    }
    const img = ctx.images[imageKey(c.serviceSlug, c.version)];
    if (!isStr(c.version) || !RE.version.test(c.version)) is.err(`${at}.version`, 'V_FORMAT', `version "${String(c.version)}" is not an image version (v1, 3.1.0 …)`);
    if (!img) {
      is.err(`${at}.version`, 'V_REF', `image ${slug}:${c.version} does not exist`);
      continue;
    }
    if (img.importStatus && img.importStatus !== 'ready') is.err(`${at}.version`, 'V_IMAGE_NOT_READY', `image ${slug}:${c.version} import is ${img.importStatus}`, 'publish');
    if (typeof c.enabled !== 'boolean') is.err(`${at}.enabled`, 'V_TYPE', 'enabled must be true / false');
    if (!['always', 'on-failure', 'never'].includes(c.restart)) is.err(`${at}.restart`, 'V_ENUM', 'restart must be always | on-failure | never');
    if (img.kind === 'job' && c.restart === 'always') is.err(`${at}.restart`, 'V_JOB_RESTART', 'a job must use restart never or on-failure');
    validateCommand(c.command, `${at}.command`, is);
    validateCommand(c.entrypoint, `${at}.entrypoint`, is);
    const r = c.resources ?? { cpus: null, memoryMb: null };
    if (r.cpus !== null && (typeof r.cpus !== 'number' || r.cpus < 0.1 || r.cpus > 64)) is.err(`${at}.resources.cpus`, 'V_RANGE', 'cpus must be 0.1–64 or null');
    if (r.memoryMb !== null && (!isInt(r.memoryMb) || r.memoryMb < 64 || r.memoryMb > 1048576)) is.err(`${at}.resources.memoryMb`, 'V_RANGE', 'memoryMb must be an integer 64–1048576 or null');
    if (c.healthcheck !== null && c.healthcheck !== undefined) validateHealthCheck(c.healthcheck, img.ports.map((p) => p.name), is, `${at}.healthcheck`);
    // env
    if (!Array.isArray(c.env)) {
      is.err(`${at}.env`, 'V_TYPE', 'env must be a list');
      continue;
    }
    if (c.env.length > LIMITS.envPerContainer) is.err(`${at}.env`, 'V_TOO_MANY', `at most ${LIMITS.envPerContainer}`);
    for (const d of dupes(c.env, (e) => e.name)) is.err(`${at}.env[${d}]`, 'V_DUPLICATE', `env ${d} is set twice`);
    const ectx = containerExprContext(config, ctx, site.siteSlug, c, g.siteMap);
    const imgEnv = img.env.map(normalizeImageEnv);
    const cr = resolveContainerConfig(config, ctx, c, { ...img, env: imgEnv }, ectx);
    for (const e of c.env) {
      const ep = `${at}.env[${e.name}]`;
      if (!isStr(e.name) || !RE.envName.test(e.name)) is.err(`${ep}.name`, 'V_FORMAT', `"${String(e.name)}" does not match ^[A-Z_][A-Z0-9_]*$`);
      const hv = e.value !== null && e.value !== undefined;
      const hs = e.secretRef !== null && e.secretRef !== undefined;
      if (hv === hs) is.err(ep, 'V_ENV_ONE_OF', `needs exactly one of value / secretRef (it has ${hv ? 'both' : 'neither'})`);
      if (hs && !g.secretSlugs.includes(e.secretRef!)) is.err(`${ep}.secretRef`, 'V_REF', `secret ${e.secretRef} does not exist`);
      const ie = imgEnv.find((x) => x.name === e.name);
      if (hv) {
        if (!isStr(e.value)) is.err(`${ep}.value`, 'V_TYPE', 'value must be a string');
        else {
          if (e.value.length > LIMITS.value) is.err(`${ep}.value`, 'V_LENGTH', `at most ${LIMITS.value} characters`);
          const se = checkExpr(e.value, 'env');
          if (se.length) for (const x of se) is.err(`${ep}.value`, x.code, `${x.message} (at ${x.pos})`);
          else {
            const rr = cr.resolution.env[e.name];
            if (rr && !rr.ok) is.err(`${ep}.value`, rr.error.code, `${rr.error.message} (at ${rr.error.pos})`, 'publish');
            else if (rr?.ok && ie?.options && !rr.result.secret && !rr.result.deferred && !ie.options.includes(rr.result.value))
              is.err(`${ep}.value`, 'V_OPTION', `"${rr.result.value}" is not one of the allowed values of ${e.name}: ${ie.options.join(', ')}`, 'publish');
          }
        }
      }
      if (ie?.secret && hv && isStr(e.value) && !/\{\{[^}]*(secret\.|container\.env\.)/.test(e.value)) is.err(ep, 'V_ENV_SECRET', `image env ${e.name} is secret: use secretRef or a secret.* expression`, 'publish');
    }
    for (const ie of imgEnv) {
      const given = c.env.find((x) => x.name === ie.name);
      if (!given && ie.required && ie.value === undefined) is.err(`${at}.env`, 'V_ENV_REQUIRED', `image env ${ie.name} is required (no default)`, 'publish');
      if (!given && isStr(ie.value)) {
        const rr = cr.resolution.env[ie.name];
        if (rr && !rr.ok) is.err(`${at}.env[${ie.name}]`, rr.error.code, `image default: ${rr.error.message}`, 'publish');
      }
    }
    // volumes: one row per image volume (missing = not mounted, warning) + extra mounts; mount paths may hold {{ }}
    if (!Array.isArray(c.volumes)) is.err(`${at}.volumes`, 'V_TYPE', 'volumes must be a list');
    else {
      if (c.volumes.length > LIMITS.volumesPerContainer) is.err(`${at}.volumes`, 'V_TOO_MANY', `at most ${LIMITS.volumesPerContainer}`);
      for (const d of dupes(
        c.volumes.filter((v) => v?.imageVolume),
        (v) => v.imageVolume!,
      ))
        is.err(`${at}.volumes[${d}]`, 'V_DUPLICATE', `image volume ${d} is mapped twice`);
      for (const iv of img.volumes)
        if (!c.volumes.some((v) => v?.imageVolume === iv.name))
          is.warn(`${at}.volumes[${iv.name}]`, 'V_VOLUME_NOT_MOUNTED', `image volume ${iv.name} (${iv.mountPath}) is not mounted: its data is lost when the container is recreated. Pick a data volume, or choose "Not mounted"`);
      c.volumes.forEach((v, i) => {
        const vp = `${at}.volumes[${v?.imageVolume ?? i}]`;
        if (!v || typeof v !== 'object' || !v.source || typeof v.source !== 'object') return is.err(vp, 'V_TYPE', 'a volume row needs imageVolume, mountPath, source, readOnly');
        if (v.imageVolume !== null && !img.volumes.some((x) => x.name === v.imageVolume)) is.err(`${vp}.imageVolume`, 'V_REF', `image ${slug}:${c.version} has no volume ${v.imageVolume}`);
        if (v.imageVolume === null) {
          if (v.mountPath === null || v.mountPath === undefined || v.mountPath === '') is.err(`${vp}.mountPath`, 'V_REQUIRED', 'an extra mount needs a mount path');
          if (v.source.kind === 'none') is.err(`${vp}.source.kind`, 'V_VOLUME', 'an extra mount needs a source (volume, host, file or secret)');
        }
        if (v.mountPath !== null && v.mountPath !== undefined) checkMountPath(v.mountPath, `${vp}.mountPath`, is);
        if (typeof v.readOnly !== 'boolean') is.err(`${vp}.readOnly`, 'V_TYPE', 'readOnly must be true / false');
        const src = v.source;
        if ((src.kind === 'file' || src.kind === 'secret') && v.readOnly !== true) is.err(`${vp}.readOnly`, 'V_READONLY', `a ${src.kind} mount is always read-only (readOnly must be true)`);
        if (src.kind === 'volume') {
          if (!isStr(src.volumeSlug) || !RE.volumeSlug.test(src.volumeSlug)) is.err(`${vp}.source.volumeSlug`, 'V_FORMAT', `volume name "${String(src.volumeSlug)}" must match ^[a-z0-9][a-z0-9_.-]{0,99}$`);
          else if (Array.isArray(sc.volumes) && !declaredVolumes.has(src.volumeSlug))
            is.err(`${vp}.source.volumeSlug`, 'V_VOLUME_UNDECLARED', `volume ${src.volumeSlug} is not declared on site ${site.siteSlug}: add it under the site's Volumes`);
          else {
            // kind matrix (§21 #83): only at the image's own path (an image volume row without its own mount path)
            const svKind = sc.volumes.find((x) => x?.volumeSlug === src.volumeSlug)?.kind === 'file' ? 'file' : 'dir';
            const iv = v.imageVolume !== null ? img.volumes.find((x) => x.name === v.imageVolume) : undefined;
            const atImagePath = !!iv && (v.mountPath === null || v.mountPath === undefined);
            const ivKind = iv ? imageVolumeKind(iv) : null;
            if (svKind === 'file') {
              const mp = v.mountPath ?? iv?.mountPath ?? '';
              if (!mp || mp.endsWith('/')) is.err(`${vp}.mountPath`, 'V_VOLUME_KIND', `volume ${src.volumeSlug} is a single file: mount it at a file path (e.g. /data/flows.json), not a folder`);
              else if (atImagePath && ivKind === 'dir')
                is.warn(
                  `${vp}.source.volumeSlug`,
                  'V_VOLUME_KIND_IMAGE',
                  `image volume ${iv!.name} (${iv!.mountPath}) is a Folder in image ${slug}:${c.version}; the single-file volume ${src.volumeSlug} replaces the whole folder with one file — pick a folder volume here, or set image volume ${iv!.name} to File on the image page if ${iv!.mountPath} is a file`,
                );
              if (v.readOnly === false) is.info(`${vp}.source.volumeSlug`, 'V_FILE_VOLUME_RENAME', `${src.volumeSlug} is a single-file volume mounted read-write: ${FILE_VOLUME_RENAME_INFO}`);
            } else if (atImagePath && ivKind === 'file')
              is.err(
                `${vp}.source.volumeSlug`,
                'V_VOLUME_KIND_MISMATCH',
                `folder volume ${src.volumeSlug} cannot be mounted on file path ${iv!.mountPath} (image volume ${iv!.name} is a File) — use a single-file volume, or change the image volume to Folder on the image page`,
              );
          }
        } else if (src.kind === 'host') {
          if (!isStr(src.path) || isExpr(src.path) || !isSafeMountPath(src.path)) is.err(`${vp}.source.path`, 'V_VOLUME', 'host path must be an absolute path, not "/", without spaces, ".." or {{ }}');
        } else if (src.kind === 'file') {
          const f = config.files.find((x) => x.fileSlug === src.fileSlug);
          if (!f) is.err(`${vp}.source.fileSlug`, 'V_REF', `file ${src.fileSlug} does not exist`);
          else {
            if (!f.content) is.err(`${vp}.source.fileSlug`, 'V_FILE_CONTENT', `file ${src.fileSlug} has no content`, 'publish');
            if (c.enabled && fileRenderMode(f) === 'expressions') renders.add(f.fileSlug);
          }
        } else if (src.kind === 'secret') {
          if (!g.secretSlugs.includes(src.secretSlug)) is.err(`${vp}.source.secretSlug`, 'V_REF', `secret ${src.secretSlug} does not exist`);
        } else if (src.kind !== 'none')
          is.err(`${vp}.source.kind`, 'V_ENUM', (src as { kind: string }).kind === 'data' ? 'source kind "data" was replaced by declared site volumes ({ kind: "volume", volumeSlug })' : 'source.kind must be volume | host | file | secret | none');
      });
      // effective mounts: resolved paths unique
      const mounts = new Map<string, { what: string; expr: boolean }>();
      for (const m of containerMounts(c, img)) {
        const vp = m.imageVolume ? `${at}.volumes[${m.imageVolume}].mountPath` : `${at}.volumes[${m.key.slice(7, -1)}].mountPath`;
        const rr = cr.resolution.mounts[m.key];
        const expr = isExpr(m.mountPath);
        if (expr && m.mountPath.startsWith('/') && !checkExpr(m.mountPath, 'mountPath').length && rr) {
          if (!rr.ok) is.err(vp, rr.error.code, `${rr.error.message} (at ${rr.error.pos})`, 'publish');
          else if (!rr.result.deferred && !isSafeMountPath(rr.result.value)) is.err(vp, 'V_PATH', `resolves to "${rr.result.value}": a mount path must be absolute, not "/", without spaces or ".."`, 'publish');
        }
        const eff = rr?.ok ? rr.result.value : m.mountPath;
        const prev = mounts.get(eff);
        const what = m.imageVolume ? `image volume ${m.imageVolume}` : m.key;
        if (prev) is.err(vp, 'V_MOUNT', `mount path ${eff} is also used by ${prev.what}`, prev.expr || expr ? 'publish' : 'save');
        else mounts.set(eff, { what, expr });
      }
      // §21 #101 a File is static and read-only: never mounted on / inside a writable volume or host mount of this container
      const effOf = (m: EffectiveMount) => {
        const rr = cr.resolution.mounts[m.key];
        return { path: (rr?.ok && !rr.result.deferred ? rr.result.value : m.mountPath).replace(/\/+$/, ''), expr: isExpr(m.mountPath), ok: !isExpr(m.mountPath) || (!!rr?.ok && !rr.result.deferred) };
      };
      const all = containerMounts(c, img);
      for (const fm of all) {
        if (fm.source.kind !== 'file') continue;
        const f = effOf(fm);
        if (!f.ok || !f.path) continue;
        for (const wm of all) {
          if (wm.readOnly || (wm.source.kind !== 'volume' && wm.source.kind !== 'host')) continue;
          const w = effOf(wm);
          if (!w.ok || !w.path || !(f.path === w.path || f.path.startsWith(w.path + '/'))) continue;
          const fileSlug = fm.source.fileSlug;
          const what = wm.source.kind === 'volume' ? `volume ${wm.source.volumeSlug}` : `host folder ${wm.source.path}`;
          const fix = wm.source.kind === 'volume' ? `put it into the volume's own content (Volume → Content) as ${f.path.slice(w.path.length + 1) || '(the mount itself)'}, mount the file outside ${w.path}, or mount ${what} read-only` : `mount the file outside ${w.path}, or mount the host folder read-only`;
          const fp = fm.imageVolume ? `${at}.volumes[${fm.imageVolume}].source.fileSlug` : `${at}.volumes[${fm.key.slice(7, -1)}].source.fileSlug`;
          const msg = `file ${fileSlug} is mounted at ${f.path} inside the writable ${what} (${w.path}) — Files are static and read-only and are never mounted on a writable volume: ${fix}`;
          if (is.mode === 'agent') is.warn(fp, 'V_FILE_IN_WRITABLE_VOLUME', msg);
          else is.err(fp, 'V_FILE_IN_WRITABLE_VOLUME', msg, f.expr || w.expr ? 'publish' : 'save');
          break;
        }
      }
    }
    // ports: one row per image port (missing = not published, edge on)
    const hostPorts: number[] = [];
    if (!Array.isArray(c.ports)) is.err(`${at}.ports`, 'V_TYPE', 'ports must be a list');
    else {
      for (const d of dupes(c.ports, (p) => p.portName)) is.err(`${at}.ports[${d}]`, 'V_DUPLICATE', `port ${d} is listed twice`);
      for (const p of c.ports) {
        const pp = `${at}.ports[${p.portName}]`;
        const ip = img.ports.find((x) => x.name === p.portName);
        if (!ip) is.err(`${pp}.portName`, 'V_REF', `image ${slug}:${c.version} has no port ${p.portName}`);
        if (p.hostIp !== '0.0.0.0' && p.hostIp !== '127.0.0.1') is.err(`${pp}.hostIp`, 'V_ENUM', 'hostIp must be 0.0.0.0 | 127.0.0.1');
        if (typeof p.edge !== 'boolean') is.err(`${pp}.edge`, 'V_TYPE', 'edge must be true / false');
        if (p.hostPort === null) continue;
        let n: number | null = null;
        if (typeof p.hostPort === 'number') {
          checkPort(p.hostPort, `${pp}.hostPort`, is);
          n = isInt(p.hostPort) ? p.hostPort : null;
        } else if (isStr(p.hostPort) && isExpr(p.hostPort)) {
          const se = checkExpr(p.hostPort, 'hostPort');
          if (se.length) for (const x of se) is.err(`${pp}.hostPort`, x.code, x.message);
          else
            try {
              n = resolveNumberField(p.hostPort, { deployment: { id: ctx.deploymentId }, self: { slug: site.siteSlug, host: null }, var: vars }, 'hostPort');
            } catch (x) {
              if (!(x instanceof ExprError)) throw x;
              is.err(`${pp}.hostPort`, x.code, x.message, 'publish');
            }
        } else is.err(`${pp}.hostPort`, 'V_TYPE', 'hostPort must be null, a number, or one whole {{ }} block');
        if (n === null) continue;
        if (n === 80 || n === 443 || edgePorts.includes(n)) is.err(`${pp}.hostPort`, 'V_PORT_EDGE', `host port ${n} belongs to the edge; add a route instead`);
        hostPorts.push(n);
        if (c.enabled) listeners.push({ port: n, proto: ip?.protocol ?? 'tcp', what: `${c.containerSlug}.${p.portName}`, path: `${pp}.hostPort` });
      }
    }
    // depends (containers of this site)
    if (!Array.isArray(c.depends)) is.err(`${at}.depends`, 'V_TYPE', 'depends must be a list');
    else
      for (const d of c.depends) {
        if (d.containerSlug === c.containerSlug) is.err(`${at}.depends`, 'V_CYCLE', 'a container cannot depend on itself');
        else if (!slugs.includes(d.containerSlug)) is.err(`${at}.depends`, 'V_REF', `container ${d.containerSlug} does not exist on this site`);
      }
    const ports = containerPorts(c, img);
    containerInfo[c.containerSlug] = { enabled: c.enabled, hostPorts, portNames: img.ports.map((x) => x.name), edgePorts: ports.filter((p) => p.edge).map((p) => p.portName), slug: c.containerSlug };
  }
  const co = containerOrder(sc);
  if ('cycle' in co) is.err(`${cb}.containers`, 'V_CYCLE', `container depends form a cycle: ${co.cycle.join(' → ')}`);
  // §21 #93: implicit (containers.<slug>.*) + suggested (literal hostname) depends; a reference cycle → declared order
  if (!('cycle' in co)) {
    const inf = inferDepends({ site: sc, images: ctx.images, files: config.files, fileContents: ctx.fileContents });
    for (const d of inf)
      for (const sg of d.suggested)
        is.info(`${cb}.containers[${d.containerSlug}].depends`, 'V_DEPENDS_SUGGESTED', `${d.containerSlug} mentions \`${sg.containerSlug}\` (${sg.from.join(', ')}) — add it to Depends on?`);
    const so = startOrder(sc, inf);
    if (so.cycle.length)
      is.warn(`${cb}.containers`, 'V_START_ORDER_CYCLE', `containers ${so.cycle.join(', ')} reference each other (expressions or hostnames): they start in the declared order`);
  }
  // §21 #88: containers.<slug>.* of a DISABLED container still resolves, but nothing answers on that name
  const disabledRef = (src: unknown, path: string) => {
    if (!isExpr(src)) return;
    let refs: string[];
    try {
      refs = exprRefs(src);
    } catch {
      return;
    }
    const seen = new Set<string>();
    for (const r of refs) {
      const m = /^containers\.([^.]+)\./.exec(r);
      if (!m || seen.has(m[1]!)) continue;
      seen.add(m[1]!);
      if (containerInfo[m[1]!] && !containerInfo[m[1]!]!.enabled)
        is.warn(path, 'V_CONTAINER_REF_DISABLED', `container ${m[1]} is disabled: ${r} resolves, but nothing answers on it`);
    }
  };
  for (const c of sc.containers ?? []) {
    if (!c || c.enabled === false || !Array.isArray(c.env)) continue;
    const at = `${cb}.containers[${c.containerSlug}]`;
    for (const e of c.env) if (e && isStr(e.value)) disabledRef(e.value, `${at}.env[${e.name}].value`);
    const img = ctx.images?.[imageKey(c.serviceSlug, c.version)];
    for (const raw of img?.env ?? []) {
      const ie = normalizeImageEnv(raw);
      if (!c.env.some((x) => x?.name === ie.name) && isStr(ie.value)) disabledRef(ie.value, `${at}.env[${ie.name}]`);
    }
  }
  if (ctx.fileContents) for (const slug of renders) disabledRef(ctx.fileContents[slug], `files[${slug}]`);
  // rendered files (only when the contents are known: agent staging, cloud preview)
  if (ctx.fileContents && renders.size) {
    const fctx = siteFileExprContext(config, ctx, site.siteSlug, g.siteMap);
    for (const slug of renders) {
      const f = config.files.find((x) => x.fileSlug === slug)!;
      const content = ctx.fileContents[slug];
      if (content === undefined) continue;
      const out = renderSiteFile(content, fctx);
      const r = { fileSlug: slug };
      const where = `site ${site.siteSlug}`;
      if (!out.ok && out.code === 'E_DEFERRED') continue;
      if (!out.ok) is.err(`files[${r.fileSlug}]`, 'V_FILE_RENDER', `${r.fileSlug}:${out.line}:${out.col}: ${out.code} ${out.message} (${where})`, 'publish');
      else if (isJsonMime(f.mime)) {
        const je = isDeferredValue(out.value) ? null : jsonError(out.value);
        if (je) is.err(`files[${r.fileSlug}]`, 'V_FILE_JSON', `${r.fileSlug}: the rendered file is not valid JSON for ${where}: ${je.replace(out.value, '…')}`, 'publish');
      }
    }
  }
  // §21 #101 volume content with render 'expressions' (only when the contents are known)
  if (ctx.blobContents && contentRenders.size) {
    const fctx = siteFileExprContext(config, ctx, site.siteSlug, g.siteMap);
    for (const [sha, wheres] of contentRenders) {
      const content = ctx.blobContents[sha];
      if (content === undefined) continue;
      const out = renderSiteFile(content, fctx);
      if (!out.ok && out.code === 'E_DEFERRED') continue;
      for (const w of wheres) {
        disabledRef(content, w);
        if (!out.ok) is.err(w, 'V_FILE_RENDER', `${w.replace(/^.*\.seed\.entries\[|\]$/g, '')}:${out.line}:${out.col}: ${out.code} ${out.message} (site ${site.siteSlug})`, 'publish');
      }
    }
  }
  // edge
  if (!sc.edge || typeof sc.edge !== 'object') is.err(`${cb}.edge`, 'V_TYPE', 'edge must be an object');
  else {
    validateEdge(sc.edge, { base: `${cb}.edge`, agentPort: sc.agentPort, agentGuiAliasPort: sc.agentGuiAliasPort, containers: containerInfo, fileSlugs: g.fileSlugs, secretSlugs: g.secretSlugs }, is);
    edgePorts.forEach((p, i) => listeners.push({ port: p, proto: 'tcp', what: i === 0 ? 'edge.httpPort' : 'edge.httpsPort', path: `${cb}.edge` }));
    // edge expressions resolve (publish)
    const ex = edgeExprContext(config, ctx.deploymentId, site.siteSlug, g.hosts, deferFor(ctx.mode));
    const tryEdge = (v: string, path: string) => {
      if (!isExpr(v) || checkExpr(v, 'edge').length) return;
      try {
        resolve(v, ex, 'edge');
      } catch (x) {
        if (!(x instanceof ExprError)) throw x;
        is.err(path, x.code, x.message, 'publish');
      }
    };
    sc.edge.hosts?.forEach((h) => isStr(h.hostname) && tryEdge(h.hostname, `${cb}.edge.hosts[${h.hostname}].hostname`));
    sc.edge.routes?.forEach((r) => {
      if (r.action?.type === 'redirect' && isStr(r.action.to)) tryEdge(r.action.to, `${cb}.edge.routes[${r.routeId}].action.to`);
      if (r.action?.type === 'proxy') for (const [k, v] of Object.entries(r.action.setHeaders ?? {})) if (isStr(v)) tryEdge(v, `${cb}.edge.routes[${r.routeId}].action.setHeaders.${k}`);
      if (r.action?.type === 'proxy' && r.action.rewrite && isStr(r.action.rewrite.replacement)) tryEdge(r.action.rewrite.replacement, `${cb}.edge.routes[${r.routeId}].action.rewrite.replacement`);
      if (r.action?.type === 'rewrite' && isStr(r.action.replacement)) tryEdge(r.action.replacement, `${cb}.edge.routes[${r.routeId}].action.replacement`);
      for (const part of ['set', 'add'] as const)
        for (const [k, v] of Object.entries(r.responseHeaders?.[part] ?? {})) if (isStr(v)) tryEdge(v, `${cb}.edge.routes[${r.routeId}].responseHeaders.${part}.${k}`);
    });
    sc.edge.hosts?.forEach((h) => {
      for (const part of ['set', 'add'] as const)
        for (const [k, v] of Object.entries(h.responseHeaders?.[part] ?? {})) if (isStr(v)) tryEdge(v, `${cb}.edge.hosts[${h.hostname}].responseHeaders.${part}.${k}`);
    });
  }
  // listener conflicts (same port + protocol; agent and edge listeners are tcp; hostIp does not matter: 0.0.0.0 covers all)
  const seen = new Map<string, string>();
  for (const l of listeners) {
    if (!isInt(l.port)) continue;
    const k = `${l.proto}/${l.port}`;
    const prev = seen.get(k);
    if (prev && !(l.what.startsWith('edge.') && prev.startsWith('edge.'))) is.err(l.path, 'V_PORT_CONFLICT', `${l.port}/${l.proto} is already used by ${prev}`);
    else seen.set(k, l.what);
  }
}

// ───────────────────────────────────────────────────────────── local edit rules (agent GUI)
const sameJson = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);
/**
 * Compare the head config with a locally edited one; returns V_LOCAL_* issues (errors). §21 #113: a site is standalone
 * (Portainer-like) — variables, secrets, files, containers (any field, incl. the image), volumes, edge, settings and
 * dev-tool presets may be added, changed and removed locally; every change is a commit of the agent branch. What stays:
 * the list of sites is the deployment's (a site cannot add / remove sites), and a volume's persist-to-cloud setting
 * belongs to the cloud content versions (§21 #101).
 */
export function checkLocalEdit(before: Config, after: Config): ValidationIssue[] {
  const is = new Issues('agent');
  const sets = <T>(a: T[], b: T[], key: (t: T) => string) => sameJson(a.map(key).sort(), b.map(key).sort());
  if (!sets(before.sites, after.sites, (v) => v.siteSlug)) is.err('sites', 'V_LOCAL_SITES', 'sites cannot be added or removed on a site');
  for (const s of after.sites) {
    const o = before.sites.find((x) => x.siteSlug === s.siteSlug);
    if (!o) continue;
    for (const v of s.config.volumes ?? []) {
      const ov = (o.config.volumes ?? []).find((x) => x.volumeSlug === v.volumeSlug);
      if (ov && canonJson(ov.persist ?? null) !== canonJson(v.persist ?? null))
        is.err(`sites[${s.siteSlug}].config.volumes[${v.volumeSlug}]`, 'V_LOCAL_VOLUME_CONTENT', `volume ${v.volumeSlug}: its persist-to-cloud setting changes only in the cloud (Volume → Content)`);
    }
  }
  return is.list;
}

// ───────────────────────────────────────────────────────────── normalize (API writes, legacy documents)
type LegacyVolume = { kind: 'data'; name: string; mountPath: string } | { kind: 'file'; fileSlug: string; mountPath: string } | { kind: 'secret'; secretSlug: string; mountPath: string };
type LegacyPort = { portName: string; hostPort: number | string | null; protocol?: 'tcp' | 'udp' };
export interface NormalizeContainerOptions {
  /**
   * true for a site config written before round D (no `volumes` field): an image volume WITHOUT a row was the implicit data
   * volume `<containerSlug>-<imageVolume>` then, so it becomes an explicit row with source volume (when the image is known).
   */
  implicitVolumes?: boolean;
}
/** Convert one legacy / current mount source. Legacy `{kind:'data', name}` → `{kind:'volume', volumeSlug: name}`. */
function normalizeVolumeSource(src: unknown): { source: VolumeSource; legacyData: boolean } {
  const s = src as { kind?: string; name?: string } | null | undefined;
  if (!s || typeof s !== 'object') return { source: { kind: 'none' }, legacyData: false };
  if (s.kind === 'data') return { source: { kind: 'volume', volumeSlug: s.name as string }, legacyData: true };
  return { source: s as VolumeSource, legacyData: false };
}
/**
 * Normalize one container as stored / sent. Accepts the legacy one-per-service shape (no containerSlug, `port`,
 * `volume`, depends {serviceSlug}) and the round-C data source: containerSlug = serviceSlug; port[] → ports (hostIp
 * 0.0.0.0, edge true); legacy data mounts → source `{kind:'volume', volumeSlug: <the old data volume name>}` with
 * readOnly RESET to false (round D: the round-C editor stored read-only for data volumes by mistake); file / secret →
 * read-only extra mounts; depends → {containerSlug}. The caller declares the volumes (normalizeSiteConfig). Pure.
 */
export function normalizeContainer(raw: Partial<Container> & Record<string, unknown>, img?: Pick<CatalogImage, 'volumes'> | null, o: NormalizeContainerOptions = {}): Container {
  const containerSlug = (raw.containerSlug as string | undefined) ?? (raw.serviceSlug as string);
  let ports: ContainerPort[];
  if (Array.isArray(raw.ports)) ports = raw.ports.map((p) => ({ portName: p.portName, hostPort: p.hostPort ?? null, hostIp: p.hostIp ?? '0.0.0.0', edge: p.edge ?? true }));
  else ports = ((raw.port as LegacyPort[] | undefined) ?? []).map((p) => ({ portName: p.portName, hostPort: p.hostPort ?? null, hostIp: '0.0.0.0' as const, edge: true }));
  let volumes: ContainerMount[];
  let implicit = !!o.implicitVolumes;
  if (Array.isArray(raw.volumes))
    volumes = raw.volumes.map((v) => {
      const { source, legacyData } = normalizeVolumeSource(v.source);
      const readOnly = source.kind === 'file' || source.kind === 'secret' ? true : legacyData ? false : (v.readOnly ?? false);
      return { imageVolume: v.imageVolume ?? null, mountPath: v.mountPath ?? null, source, readOnly };
    });
  else {
    volumes = [];
    implicit = true; // pre-round-C: image volumes were always mounted
    for (const v of (raw.volume as LegacyVolume[] | undefined) ?? []) {
      if (v.kind === 'data') {
        const iv = img?.volumes.find((x) => x.name === v.name);
        volumes.push({ imageVolume: v.name, mountPath: iv && iv.mountPath === v.mountPath ? null : v.mountPath, source: { kind: 'volume', volumeSlug: defaultVolumeSlug(containerSlug, v.name) }, readOnly: false });
      } else if (v.kind === 'file') volumes.push({ imageVolume: null, mountPath: v.mountPath, source: { kind: 'file', fileSlug: v.fileSlug }, readOnly: true });
      else if (v.kind === 'secret') volumes.push({ imageVolume: null, mountPath: v.mountPath, source: { kind: 'secret', secretSlug: v.secretSlug }, readOnly: true });
    }
  }
  // legacy implicit image volumes were data volumes: make the rows explicit when the image is known
  if (implicit) for (const iv of img?.volumes ?? []) if (!volumes.some((x) => x.imageVolume === iv.name)) volumes.push({ imageVolume: iv.name, mountPath: null, source: { kind: 'volume', volumeSlug: defaultVolumeSlug(containerSlug, iv.name) }, readOnly: false });
  const deps = ((raw.depends as { containerSlug?: string; serviceSlug?: string }[] | undefined) ?? []).map((d) => ({ containerSlug: (d.containerSlug ?? d.serviceSlug) as string }));
  return {
    containerSlug,
    serviceSlug: raw.serviceSlug as string,
    version: raw.version as string,
    enabled: raw.enabled ?? true,
    env: (raw.env ?? []).map((e) => ({ name: e.name, value: e.value ?? null, secretRef: e.secretRef ?? null })),
    ports,
    volumes,
    command: raw.command ?? null,
    entrypoint: raw.entrypoint ?? null,
    restart: raw.restart ?? 'always',
    resources: { cpus: raw.resources?.cpus ?? null, memoryMb: raw.resources?.memoryMb ?? null },
    healthcheck: raw.healthcheck ?? null,
    depends: deps,
  };
}
/** true when a container (or any container of a site config / config) still has the legacy one-per-service shape. */
export function isLegacyContainer(raw: unknown): boolean {
  const c = raw as Record<string, unknown>;
  return !!c && typeof c === 'object' && (c.containerSlug === undefined || 'port' in c || 'volume' in c || !Array.isArray(c.ports) || !Array.isArray(c.volumes));
}
/** true when a site config was written before round D: no `volumes` list, or a mount with the old source kind "data". */
export function isLegacySiteConfig(sc: unknown): boolean {
  const x = sc as Partial<SiteConfig> | null;
  if (!x || typeof x !== 'object') return false;
  if (!Array.isArray(x.volumes)) return true;
  return (x.containers ?? []).some((c) => isLegacyContainer(c) || (Array.isArray(c.volumes) && c.volumes.some((v) => (v?.source as { kind?: string } | undefined)?.kind === 'data')));
}
/** Normalize one declared site volume (defaults: label = volumeSlug when missing, description "", backup true). */
export function normalizeSiteVolume(v: Partial<SiteVolume> & { volumeSlug: string }): SiteVolume {
  const seed = v.seed?.kind === 'tree' ? { kind: 'tree' as const, ...normalizeVolumeTree(v.seed) } : (v.seed ?? null);
  // §21 #101 persist is OMITTED when off (agents < 0.12 re-hash revisions without the key)
  const persist = v.persist ? { persist: { exclude: Array.isArray(v.persist.exclude) ? v.persist.exclude : [], maxBytes: v.persist.maxBytes ?? VOLUME_CONTENT_LIMITS.persistDefaultBytes, auto: v.persist.auto ?? true } } : {};
  return { volumeSlug: v.volumeSlug, label: v.label ?? v.volumeSlug, description: v.description ?? '', backup: v.backup ?? true, location: normalizeLocation(v.location), kind: v.kind ?? 'dir', seed, ...persist };
}
/**
 * Fill defaults the API applies on write (missing optional fields) and convert legacy shapes. Pure; returns a new object.
 * Round D: a site config without `volumes` is legacy — implicit image volumes become explicit rows (image known), old
 * `data` sources become `volume` sources (readOnly false), and every volume they use is DECLARED on the site
 * (label = volumeSlug, backup true). A current config is not auto-declared (undeclared → V_VOLUME_UNDECLARED).
 */
export function normalizeSiteConfig(sc: Partial<SiteConfig>, images?: Record<string, Pick<CatalogImage, 'volumes'>>): SiteConfig {
  const d = defaultSiteConfig();
  const host = sc.host === undefined || sc.host === null || (typeof sc.host === 'string' && sc.host.trim() === '') ? null : sc.host;
  const preD = !Array.isArray(sc.volumes);
  const volumes: SiteVolume[] = (Array.isArray(sc.volumes) ? sc.volumes : []).map((v) => normalizeSiteVolume(v));
  const legacyNames = new Set<string>();
  const containers = (sc.containers ?? []).map((c) => {
    const raw = c as Container & Record<string, unknown>;
    for (const v of Array.isArray(raw.volumes) ? raw.volumes : []) {
      const k = v?.source as { kind?: string; name?: string } | undefined;
      if (k?.kind === 'data' && typeof k.name === 'string') legacyNames.add(k.name);
    }
    const n = normalizeContainer(raw, images?.[imageKey(c.serviceSlug, c.version)] ?? null, { implicitVolumes: preD });
    if (preD || !Array.isArray(raw.volumes)) for (const v of n.volumes) if (v.source.kind === 'volume') legacyNames.add(v.source.volumeSlug);
    return n;
  });
  for (const name of legacyNames) if (!volumes.some((v) => v.volumeSlug === name)) volumes.push(newSiteVolume(name));
  return {
    host,
    agentPort: sc.agentPort ?? d.agentPort,
    agentGuiAliasPort: sc.agentGuiAliasPort === undefined ? d.agentGuiAliasPort : sc.agentGuiAliasPort,
    volumes,
    containers,
    depends: sc.depends ?? [],
    notFoundPageId: sc.notFoundPageId ?? null,
    edge: sc.edge ? normalizeEdge(sc.edge as Edge & Record<string, unknown>) : d.edge,
    ...normalizeSiteDevTools(sc.devTools),
  };
}
/** Normalize a whole config (files: render default; sites: normalizeSiteConfig). Pure. */
export function normalizeConfig<C extends Pick<Config, 'files' | 'sites'>>(config: C, images?: Record<string, Pick<CatalogImage, 'volumes'>>): C {
  return {
    ...config,
    files: config.files.map((f) => normalizeFileDef(f)),
    sites: config.sites.map((s) => ({ ...s, config: normalizeSiteConfig(s.config, images) })),
  };
}

// ───────────────────────────────────────────────────────────── dev-tool presets (SPEC-final §7.6, §21 #97)
/** Roots an expression in a dev-tool preset may use (resolved on the site from the head config) = FIELD_ROOTS.devtool. */
export const DEVTOOL_EXPR_ROOTS = ['var', 'secret', 'site', 'self', 'deployment', 'containers'] as const;
/** §21 #99: the expression field of every dev-tool preset value (the only field with containers.<c>.env.<NAME>). */
export const DEVTOOL_EXPR_FIELD: ExprField = 'devtool';
const DEVTOOL_HTTP_SCHEMES = ['none', 'basic', 'bearer', 'api-key'] as const;
const PG_SSL_MODES = ['disable', 'prefer', 'require', 'verify-full'] as const;
const CQL_CONSISTENCY = ['ANY', 'ONE', 'TWO', 'THREE', 'QUORUM', 'ALL', 'LOCAL_QUORUM', 'EACH_QUORUM', 'LOCAL_ONE'] as const;
export const DEVTOOL_HEADERS_MAX = 50;
const REDIS_DB_RE = /^(?:[0-9]|1[0-5])$/;

function canonJson(v: unknown): string {
  try {
    return canonicalize(v);
  } catch {
    return JSON.stringify(v) ?? 'undefined';
  }
}

/**
 * Syntax + roots + path shapes of a value in a dev-tool preset (username, database, clientId, apiKeyHeader, password /
 * token expressions, default header values, url target). Returns at most one error (like checkExpr).
 */
export function checkDevToolExpr(src: string): ExprError[] {
  return checkExpr(src, DEVTOOL_EXPR_FIELD);
}

/**
 * §21 #99 true when env NAME of container c holds a secret value (secretRef, or a value using secret.* — also through
 * container.env.<OTHER>). Image defaults cannot hold secrets (field imageEnv). Unknown name → false.
 */
export function containerEnvIsSecret(c: Pick<Container, 'env'>, name: string, seen: Set<string> = new Set()): boolean {
  if (seen.has(name)) return false;
  seen.add(name);
  const e = (Array.isArray(c.env) ? c.env : []).find((x) => x && x.name === name);
  if (!e) return false;
  if (e.secretRef !== null && e.secretRef !== undefined) return true;
  if (!isStr(e.value) || !isExpr(e.value)) return false;
  let refs: string[] = [];
  try {
    refs = exprRefs(e.value);
  } catch {
    return false;
  }
  return refs.some((r) => r.startsWith('secret.') || (r.startsWith('container.env.') && containerEnvIsSecret(c, r.slice('container.env.'.length), seen)));
}
/** §21 #99 env names of a container for containers.<c>.env.<NAME>: container env + image env (null image = unknown). */
export function containerEnvNames(c: Pick<Container, 'env'>, img: { env?: readonly { name: string }[] } | null | undefined): { names: string[]; imageKnown: boolean } {
  const names: string[] = [];
  for (const e of Array.isArray(c.env) ? c.env : []) if (e && isStr(e.name) && !names.includes(e.name)) names.push(e.name);
  for (const e of img?.env ?? []) if (e && isStr(e.name) && !names.includes(e.name)) names.push(e.name);
  return { names, imageKnown: !!img && Array.isArray(img.env) };
}

/** A relative path inside a volume: 1–1024 chars, no leading "/", no "\\" or NUL, no ".." segment, no {{ }}. */
export function isSafeRelativePath(p: unknown): boolean {
  if (!isStr(p) || p.length === 0 || p.length > 1024) return false;
  if (p.startsWith('/') || p.includes('\\') || p.includes('\0') || p.includes('{{')) return false;
  return !p.split('/').some((seg) => seg === '..');
}

const isObj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === 'object' && !Array.isArray(v);
const nonEmpty = (v: unknown): v is string => isStr(v) && v.trim() !== '';

/**
 * Validate SiteConfig.devTools of one site (§21 #97 b). Every issue is save level (the API refuses to store it) except
 * the warnings (disabled target container, auth fields the kind does not use). `images` = the catalog images keyed
 * `${serviceSlug}:${version}` (ValidationContext.images); a container whose image is not in it is not port-checked
 * (the container itself already reports V_REF).
 *
 * Issue paths: `sites[<site>].config.devTools` (list) / `sites[<site>].config.devTools[<presetSlug>].<field>`.
 */
export function validateSiteDevTools(site: Site, config: Pick<Config, 'variables' | 'secrets'>, images: Record<string, Pick<CatalogImage, 'ports'> & Partial<Pick<CatalogImage, 'env'>>>, is: Issues) {
  const sc = site?.config as (SiteConfig & { devTools?: unknown }) | undefined;
  if (!sc || typeof sc !== 'object') return;
  const lp = `sites[${site.siteSlug}].config.devTools`;
  const list = sc.devTools;
  if (list === undefined || list === null) return;
  if (!Array.isArray(list)) return is.err(lp, 'V_TYPE', 'devTools must be a list of presets');
  if (list.length > DEVTOOL_PRESETS_MAX) is.err(lp, 'V_DEVTOOL_MAX', `at most ${DEVTOOL_PRESETS_MAX} dev-tool presets per site (${list.length})`);
  for (const d of dupes(list.filter(isObj), (p) => String(p.presetSlug))) is.err(`${lp}[${d}]`, 'V_DEVTOOL_DUP', `preset ${d} is defined twice on site ${site.siteSlug}`);
  const containers = Array.isArray(sc.containers) ? sc.containers : [];
  const volumes = Array.isArray(sc.volumes) ? sc.volumes : [];
  const secretSlugs = new Set((config.secrets ?? []).map((s) => s.secretSlug));
  const varSlugs = new Set((config.variables ?? []).map((v) => v.variableSlug));
  for (const raw of list) {
    if (!isObj(raw)) {
      is.err(lp, 'V_TYPE', 'a preset must be an object');
      continue;
    }
    const pp = `${lp}[${String(raw.presetSlug)}]`;
    if (!isStr(raw.presetSlug) || !DEVTOOL_SLUG_RE.test(raw.presetSlug)) is.err(`${pp}.presetSlug`, 'V_DEVTOOL_SLUG', `preset slug "${String(raw.presetSlug)}" must match ^[a-z0-9][a-z0-9-]{0,62}$`);
    checkLabel(raw.name, `${pp}.name`, is);
    checkDescription(raw.description, `${pp}.description`, is);
    if (raw.readOnly !== undefined && typeof raw.readOnly !== 'boolean') is.err(`${pp}.readOnly`, 'V_TYPE', 'readOnly must be true / false');
    if (!(DEVTOOL_KINDS as readonly unknown[]).includes(raw.kind)) {
      is.err(`${pp}.kind`, 'V_DEVTOOL_KIND', `kind "${String(raw.kind)}" must be one of ${DEVTOOL_KINDS.join(', ')}`);
      continue;
    }
    const kind = raw.kind as DevToolKind;
    // expression-bearing fields: syntax / roots (E_*) + secret / var references
    const exprField = (v: unknown, path: string): boolean => {
      if (!isStr(v) || !isExpr(v)) return true;
      const errs = checkDevToolExpr(v);
      for (const e of errs) is.err(path, e.code, `${e.message} (at ${e.pos})`);
      if (errs.length) return false;
      for (const ref of exprRefs(v)) {
        const [root, slug, sub, name] = ref.split('.');
        if (root === 'secret' && slug && !secretSlugs.has(slug)) is.err(path, 'V_DEVTOOL_SECRET', `secret ${slug} does not exist`);
        if (root === 'var' && slug && !varSlugs.has(slug)) is.err(path, 'V_DEVTOOL_VAR', `variable ${slug} does not exist`);
        if (root === 'containers' && slug) {
          // §21 #99: containers.<c>.* names a container of this site; .env.<NAME> an env of its image or the container
          const c = containers.find((x) => x && x.containerSlug === slug);
          if (!c) is.err(path, 'V_DEVTOOL_CONTAINER', `container ${slug} (in {{ ${ref} }}) does not exist on site ${site.siteSlug}`);
          else if (sub === 'env' && name) {
            const { names, imageKnown } = containerEnvNames(c, images[imageKey(c.serviceSlug, c.version)]);
            if (!names.includes(name) && imageKnown) is.err(path, 'V_DEVTOOL_ENV', `container ${slug} has no env var ${name} (its env: ${names.join(', ') || 'none'})`);
          }
        }
      }
      return true;
    };
    /** §21 #99 b: a password / token from a container env that holds a plain value → warning (not a literal, but readable in the config) */
    const plainEnv = (src: string, path: string, f: string) => {
      let refs: string[] = [];
      try {
        refs = exprRefs(src);
      } catch {
        return;
      }
      for (const ref of refs) {
        const [root, slug, sub, name] = ref.split('.');
        if (root !== 'containers' || sub !== 'env' || !slug || !name) continue;
        const c = containers.find((x) => x && x.containerSlug === slug);
        if (!c || !containerEnvNames(c, images[imageKey(c.serviceSlug, c.version)]).names.includes(name)) continue;
        if (!containerEnvIsSecret(c, name))
          is.warn(path, 'V_DEVTOOL_PLAIN_ENV', `env ${name} of container ${slug} holds a plain value, not a secret: the ${f} is readable in the config — set that env from a secret (secretRef or {{ secret.x }})`);
      }
    };
    // target
    const t = raw.target;
    const tp = `${pp}.target`;
    const allowed = DEVTOOL_TARGETS[kind];
    const containerOf = (slug: unknown, at: string) => {
      if (!nonEmpty(slug)) {
        is.err(`${at}.containerSlug`, 'V_DEVTOOL_TARGET', 'pick the container of this site');
        return null;
      }
      const c = containers.find((x) => x && x.containerSlug === slug);
      if (!c) {
        is.err(`${at}.containerSlug`, 'V_DEVTOOL_CONTAINER', `container ${slug} does not exist on site ${site.siteSlug}`);
        return null;
      }
      if (c.enabled === false) is.warn(`${at}.containerSlug`, 'V_DEVTOOL_CONTAINER', `container ${slug} is disabled: the preset cannot connect until it runs`);
      return c;
    };
    if (!isObj(t) || !(allowed as readonly unknown[]).includes(t.kind)) {
      is.err(tp, 'V_DEVTOOL_TARGET', `a ${kind} preset target must be one of ${allowed.join(', ')}`);
    } else if (t.kind === 'container') {
      const c = containerOf(t.containerSlug, tp);
      if (!nonEmpty(t.portName)) is.err(`${tp}.portName`, 'V_DEVTOOL_TARGET', 'pick the port of the container');
      else if (c) {
        const img = images[imageKey(c.serviceSlug, c.version)];
        if (img && !containerPorts(c, img).some((p) => p.portName === t.portName)) is.err(`${tp}.portName`, 'V_DEVTOOL_PORT', `port ${t.portName} is not a port of the image of ${c.containerSlug} (${img.ports.map((p) => p.name).join(', ') || 'no ports'})`);
      }
    } else if (t.kind === 'host') {
      const h = t.host;
      if (!nonEmpty(h) || h.includes('{{') || h.length > 253 || !(isIpLiteral(h) || (RE.dnsName.test(h) && !h.startsWith('*')))) is.err(`${tp}.host`, 'V_DEVTOOL_TARGET', `host "${String(h)}" must be an IP or a DNS name (no {{ }})`);
      if (!isInt(t.port) || t.port < 1 || t.port > 65535) is.err(`${tp}.port`, 'V_DEVTOOL_TARGET', `port ${String(t.port)} must be an integer 1–65535`);
    } else if (t.kind === 'url') {
      if (!isStr(t.url) || !/^https?:\/\/\S/i.test(t.url) || t.url.length > LIMITS.value) is.err(`${tp}.url`, 'V_DEVTOOL_TARGET', 'url must be an absolute http:// or https:// URL (≤ 4096 characters; may hold {{ var.x }})');
      else exprField(t.url, `${tp}.url`);
    } else if (t.kind === 'volume-file') {
      if (!nonEmpty(t.volumeSlug)) is.err(`${tp}.volumeSlug`, 'V_DEVTOOL_TARGET', 'pick a declared volume of the site');
      else {
        const v = volumes.find((x) => x && x.volumeSlug === t.volumeSlug);
        if (!v) is.err(`${tp}.volumeSlug`, 'V_DEVTOOL_VOLUME', `volume ${t.volumeSlug} is not declared on site ${site.siteSlug}`);
        else if (v.kind === 'file') is.err(`${tp}.volumeSlug`, 'V_DEVTOOL_VOLUME', `volume ${t.volumeSlug} is a single-file volume: SQLite needs a folder volume (journal / WAL files live next to the database)`);
      }
      if (!isSafeRelativePath(t.path)) is.err(`${tp}.path`, 'V_DEVTOOL_VOLUME', `path "${String(t.path)}" must be a relative path inside the volume (no leading "/", no "..")`);
    } else if (t.kind === 'container-file') {
      containerOf(t.containerSlug, tp);
      if (!isStr(t.path) || !isSafeMountPath(t.path) || t.path.includes('{{')) is.err(`${tp}.path`, 'V_DEVTOOL_VOLUME', `path "${String(t.path)}" must be an absolute path in the container (not "/", no spaces, no "..")`);
    }
    // auth
    const a = raw.auth;
    const ap = `${pp}.auth`;
    if (!isObj(a)) is.err(ap, 'V_DEVTOOL_AUTH', 'auth must be an object');
    else {
      for (const f of ['username', 'database', 'clientId', 'apiKeyHeader'] as const) {
        const v = a[f];
        if (v !== null && v !== undefined && !isStr(v)) is.err(`${ap}.${f}`, 'V_TYPE', `${f} must be a string or null`);
        else exprField(v, `${ap}.${f}`);
      }
      const cred = (c: unknown, f: 'password' | 'token'): DevToolCredential['kind'] | null => {
        const at = `${ap}.${f}`;
        if (c === undefined || c === null) return 'none';
        if (!isObj(c)) {
          is.err(at, 'V_DEVTOOL_AUTH', `${f} must be { kind: none } or { kind: expr, expr }`);
          return null;
        }
        if (c.kind === 'none') return 'none';
        // §21 #103 (supersedes #97 b "never a literal"): a static value is allowed, with a non-blocking warning
        const plainText = `the ${f} is stored in plain text in the config — anyone who can read the config sees it; use a secret for production`;
        if (c.kind === 'literal') {
          if (!isStr(c.value) || c.value === '') is.err(at, 'V_DEVTOOL_AUTH', `${f} value must be a non-empty string (pick None for no ${f})`);
          else if (c.value.length > LIMITS.value) is.err(at, 'V_DEVTOOL_AUTH', `${f} value must be at most ${LIMITS.value} characters`);
          else is.warn(at, 'V_DEVTOOL_LITERAL', plainText);
          return 'literal';
        }
        if (c.kind !== 'expr') {
          is.err(at, 'V_DEVTOOL_AUTH', `${f}.kind must be none, literal or expr`);
          return null;
        }
        if (!isStr(c.expr) || c.expr.trim() === '') {
          is.err(at, 'V_DEVTOOL_AUTH', `${f}.expr must be a non-empty expression`);
          return 'expr';
        }
        if (!isExpr(c.expr)) {
          is.warn(at, 'V_DEVTOOL_LITERAL', `${plainText} (an expression without {{ }} is a static value)`);
          return 'expr';
        }
        if (exprField(c.expr, at)) {
          if (exprRefs(c.expr).length === 0) is.warn(at, 'V_DEVTOOL_LITERAL', `${plainText} (the expression references no secret or variable)`);
          else plainEnv(c.expr, at, f);
        }
        return 'expr';
      };
      const pw = cred(a.password, 'password');
      const tk = cred(a.token, 'token');
      const scheme = a.httpScheme ?? 'none';
      if (!(DEVTOOL_HTTP_SCHEMES as readonly unknown[]).includes(scheme)) is.err(`${ap}.httpScheme`, 'V_DEVTOOL_AUTH', `httpScheme must be one of ${DEVTOOL_HTTP_SCHEMES.join(', ')}`);
      const user = a.username;
      switch (kind) {
        case 'postgres':
          if (!nonEmpty(user)) is.err(`${ap}.username`, 'V_DEVTOOL_AUTH', 'PostgreSQL needs a username');
          break;
        case 'cassandra':
          if (nonEmpty(user) !== (pw !== 'none' && pw !== null)) is.err(ap, 'V_DEVTOOL_AUTH', 'Cassandra needs both username and password, or neither');
          break;
        case 'redis':
          if (a.database !== null && a.database !== undefined && !(isStr(a.database) && REDIS_DB_RE.test(a.database))) is.err(`${ap}.database`, 'V_DEVTOOL_AUTH', `Redis database must be "0"–"15" or null (got "${String(a.database)}")`);
          break;
        case 'http':
          if (scheme === 'basic' && !nonEmpty(user)) is.err(`${ap}.username`, 'V_DEVTOOL_AUTH', 'basic auth needs a username');
          if ((scheme === 'bearer' || scheme === 'api-key') && tk === 'none') is.err(`${ap}.token`, 'V_DEVTOOL_AUTH', `${scheme} auth needs a token (an expression such as {{ secret.api-token }})`);
          if (scheme === 'api-key' && a.apiKeyHeader !== null && a.apiKeyHeader !== undefined && !(isStr(a.apiKeyHeader) && (RE.headerName.test(a.apiKeyHeader) || isExpr(a.apiKeyHeader))))
            is.err(`${ap}.apiKeyHeader`, 'V_DEVTOOL_AUTH', `"${String(a.apiKeyHeader)}" is not a valid header name`);
          break;
        case 'mqtt':
          if (isStr(a.clientId) && utf8Len(a.clientId) > 65535) is.err(`${ap}.clientId`, 'V_DEVTOOL_AUTH', 'clientId must be at most 65535 bytes');
          break;
      }
      // fields the kind does not use (warnings: the agent ignores them)
      if (kind !== 'http' && (scheme !== 'none' || tk !== 'none' || (a.apiKeyHeader !== null && a.apiKeyHeader !== undefined))) is.warn(ap, 'V_DEVTOOL_AUTH', `httpScheme / token / apiKeyHeader are only used by HTTP presets (ignored for ${kind})`);
      if (kind !== 'mqtt' && a.clientId !== null && a.clientId !== undefined) is.warn(`${ap}.clientId`, 'V_DEVTOOL_AUTH', `clientId is only used by MQTT presets (ignored for ${kind})`);
      if (kind === 'sqlite' && ((user !== null && user !== undefined) || pw !== 'none' || (a.database !== null && a.database !== undefined))) is.warn(ap, 'V_DEVTOOL_AUTH', 'SQLite has no username / password / database (ignored)');
    }
    // options
    const o = raw.options;
    const op = `${pp}.options`;
    if (!isObj(o)) is.err(op, 'V_DEVTOOL_OPTION', 'options must be an object');
    else {
      const bad = (f: string, msg: string) => is.err(`${op}.${f}`, 'V_DEVTOOL_OPTION', msg);
      const nil = (v: unknown) => v === null || v === undefined;
      const intIn = (f: 'timeoutSec' | 'maxRows' | 'keepAliveSec', min: number, max: number) => {
        const v = o[f];
        if (!nil(v) && (!isInt(v) || v < min || v > max)) bad(f, `${f} must be an integer ${min}–${max} or null (got ${String(v)})`);
      };
      for (const f of ['tls', 'tlsInsecure'] as const) if (o[f] !== undefined && typeof o[f] !== 'boolean') bad(f, `${f} must be true / false`);
      for (const f of ['followRedirects', 'cleanSession'] as const) if (!nil(o[f]) && typeof o[f] !== 'boolean') bad(f, `${f} must be true / false or null`);
      if (!nil(o.tlsServerName) && !isStr(o.tlsServerName)) bad('tlsServerName', 'tlsServerName must be a string or null');
      if (!nil(o.sslMode) && !(PG_SSL_MODES as readonly unknown[]).includes(o.sslMode)) bad('sslMode', `sslMode must be one of ${PG_SSL_MODES.join(', ')} or null`);
      if (!nil(o.consistency) && !(CQL_CONSISTENCY as readonly unknown[]).includes(o.consistency)) bad('consistency', `consistency must be one of ${CQL_CONSISTENCY.join(', ')} or null`);
      if (!nil(o.scheme) && o.scheme !== 'http' && o.scheme !== 'https') bad('scheme', 'scheme must be http, https or null');
      intIn('timeoutSec', 1, 3600);
      intIn('maxRows', 1, 10000);
      intIn('keepAliveSec', 0, 65535);
      if (!nil(o.basePath) && !(isStr(o.basePath) && o.basePath.startsWith('/'))) bad('basePath', `basePath must start with "/" (got "${String(o.basePath)}")`);
      const hs = o.defaultHeaders;
      if (!nil(hs)) {
        if (!Array.isArray(hs)) bad('defaultHeaders', 'defaultHeaders must be a list of { name, value }');
        else {
          if (hs.length > DEVTOOL_HEADERS_MAX) bad('defaultHeaders', `at most ${DEVTOOL_HEADERS_MAX} default headers`);
          hs.forEach((h, i) => {
            const hp = `defaultHeaders[${i}]`;
            if (!isObj(h)) return bad(hp, 'a header must be { name, value }');
            if (!isStr(h.name) || !RE.headerName.test(h.name)) bad(`${hp}.name`, `"${String(h.name)}" is not a valid header name`);
            if (!isStr(h.value) || h.value.length > LIMITS.value) bad(`${hp}.value`, `header value must be a string of at most ${LIMITS.value} characters`);
            else exprField(h.value, `${op}.${hp}.value`);
          });
        }
      }
    }
  }
}

const pick = <T extends object>(src: Record<string, unknown>, keys: readonly (keyof T & string)[]): T => {
  const out: Record<string, unknown> = {};
  for (const k of keys) out[k] = src[k];
  return out as T;
};
const TARGET_KEYS: Record<string, readonly string[]> = {
  container: ['kind', 'containerSlug', 'portName'],
  host: ['kind', 'host', 'port'],
  url: ['kind', 'url'],
  'volume-file': ['kind', 'volumeSlug', 'path'],
  'container-file': ['kind', 'containerSlug', 'path'],
};
function shapeCredential(c: unknown): unknown {
  if (!isObj(c)) return c ?? { kind: 'none' };
  if (c.kind === 'none') return { kind: 'none' };
  if (c.kind === 'expr') return { kind: 'expr', expr: c.expr };
  if (c.kind === 'literal') return { kind: 'literal', value: c.value };
  return c;
}
/**
 * One stored preset in its EXACT shape (§21 #97 a: agents re-hash the parsed config, so a preset holds only the known
 * keys — every field present, unknown keys dropped; target keys per target kind). Defaults from normalizeDevToolPreset.
 * A value that is not a preset object with a known kind is returned unchanged (the validator reports it).
 */
export function normalizeStoredDevToolPreset(p: unknown): DevToolPreset {
  if (!isObj(p) || !(DEVTOOL_KINDS as readonly unknown[]).includes(p.kind)) return p as unknown as DevToolPreset;
  const n = normalizeDevToolPreset(p as Partial<DevToolPreset> & { kind: DevToolKind }) as unknown as Record<string, unknown>;
  const t = n.target;
  const target = isObj(t) && typeof t.kind === 'string' && TARGET_KEYS[t.kind] ? pick<Record<string, unknown>>(t, TARGET_KEYS[t.kind] as string[]) : t;
  const a = (isObj(n.auth) ? n.auth : {}) as Record<string, unknown>;
  const o = (isObj(n.options) ? n.options : {}) as Record<string, unknown>;
  const nul = (v: unknown) => (v === undefined ? null : v);
  return {
    presetSlug: n.presetSlug,
    name: n.name,
    description: n.description ?? '',
    kind: n.kind,
    target,
    auth: {
      username: nul(a.username),
      password: shapeCredential(a.password),
      database: nul(a.database),
      clientId: nul(a.clientId),
      httpScheme: a.httpScheme ?? 'none',
      token: shapeCredential(a.token),
      apiKeyHeader: nul(a.apiKeyHeader),
    },
    options: {
      tls: o.tls ?? false,
      tlsInsecure: o.tlsInsecure ?? false,
      tlsServerName: nul(o.tlsServerName),
      sslMode: nul(o.sslMode),
      timeoutSec: nul(o.timeoutSec),
      maxRows: nul(o.maxRows),
      consistency: nul(o.consistency),
      scheme: nul(o.scheme),
      basePath: nul(o.basePath),
      followRedirects: nul(o.followRedirects),
      defaultHeaders: Array.isArray(o.defaultHeaders) ? o.defaultHeaders.map((h) => (isObj(h) ? { name: h.name, value: h.value } : h)) : (o.defaultHeaders ?? []),
      cleanSession: nul(o.cleanSession),
      keepAliveSec: nul(o.keepAliveSec),
    },
    readOnly: n.readOnly ?? false,
  } as unknown as DevToolPreset;
}
/**
 * `{ devTools }` to spread into a normalized SiteConfig: absent / null / [] → {} (the key is OMITTED — §21 #97 a: never
 * write devTools: []); a list → every preset in its stored shape; anything else is kept (the validator says V_TYPE).
 */
export function normalizeSiteDevTools(v: unknown): { devTools?: DevToolPreset[] } {
  if (v === undefined || v === null) return {};
  if (!Array.isArray(v)) return { devTools: v as DevToolPreset[] };
  if (v.length === 0) return {};
  return { devTools: v.map(normalizeStoredDevToolPreset) };
}

/**
 * §21 #103: expression filters of a site that need a newer agent (FILTER_MIN_AGENT): every `{{ }}` string of the site
 * config (container env, dev-tool presets, edge, host …) plus the content of every rendered File a container of the
 * site mounts (`fileContents`: fileSlug → source text; files without a text are skipped). An older agent refuses such a
 * revision at staging (E_UNKNOWN_FILTER), so the cloud warns per site. null = nothing needs a newer agent.
 * `uses` = "<filter> (<where>)" lines, e.g. "mosquitto_passwd (file mqtt-pwfile)".
 */
export function siteFilterAgentNeed(config: Pick<Config, 'sites' | 'files'>, siteSlug: string, fileContents?: Record<string, string>): { min: string; uses: string[] } | null {
  const site = config.sites.find((s) => s.siteSlug === siteSlug);
  if (!site) return null;
  let min: string | null = null;
  const uses: string[] = [];
  const scan = (src: string, where: string) => {
    if (!src.includes('{{')) return;
    for (const f of exprFilters(src)) {
      const v = FILTER_MIN_AGENT[f];
      if (!v) continue;
      if (min === null || cmpCore(v, min) > 0) min = v;
      const u = `${f} (${where})`;
      if (!uses.includes(u)) uses.push(u);
    }
  };
  const walk = (v: unknown, where: string) => {
    if (typeof v === 'string') scan(v, where);
    else if (Array.isArray(v)) for (const x of v) walk(x, where);
    else if (v && typeof v === 'object') for (const x of Object.values(v)) walk(x, where);
  };
  for (const c of site.config.containers ?? []) walk(c, `container ${c.containerSlug}`);
  for (const p of site.config.devTools ?? []) walk(p, `dev tool ${p.presetSlug}`);
  const { containers: _c, devTools: _d, ...rest } = site.config;
  walk(rest, 'site settings');
  if (fileContents) {
    const mounted = new Set<string>();
    for (const c of site.config.containers ?? []) for (const m of c.volumes ?? []) if (m?.source?.kind === 'file') mounted.add(m.source.fileSlug);
    for (const f of config.files ?? []) if (f.render === 'expressions' && mounted.has(f.fileSlug) && typeof fileContents[f.fileSlug] === 'string') scan(fileContents[f.fileSlug]!, `file ${f.fileSlug}`);
  }
  return min === null ? null : { min, uses };
}
function cmpCore(a: string, b: string): number {
  const pa = a.split('.').map(Number);
  const pb = b.split('.').map(Number);
  for (let i = 0; i < 3; i++) if ((pa[i] ?? 0) !== (pb[i] ?? 0)) return (pa[i] ?? 0) - (pb[i] ?? 0);
  return 0;
}
