/**
 * §21 #106 — GitHub mirror: every OPM branch is mirrored (push only, OPM → GitHub) into ONE GitHub repository as git
 * branches, and every git branch holds an equivalent docker compose setup WITHOUT the agent (clone → cd sites/<site> →
 * cp .env.example .env → fill → `docker compose up`). Normative with SPEC-final §21 #106 and docs-v2/github-mirror.md.
 *
 * Pieces
 *   - Branch names (`mirrorBranchName`): template/<slug>, deployment/<slug>/cloud, deployment/<slug>/agent/<site>,
 *     deployment/<slug>/uncommitted/<site>. <slug> = the stable entity slug (Template.slug / Deployment.slug: assigned at
 *     create from the label, unique, immutable — `slugifyLabel` + `uniqueSlug`).
 *   - One git commit per OPM commit (same message, author name / email, author + committer date = commit createdAt) with
 *     the trailers `OPM-Commit: <commitId>` + `OPM-Branch` + `OPM-Seq` (`mirrorCommitMessage`); agent branches also get one
 *     commit per persist-to-cloud snapshot (`OPM-Volume-Version`), uncommitted/<site> one snapshot commit of the drift
 *     (`OPM-Drift`). Nothing else is ever pushed to these branches. Deterministic trees + metadata → same git sha on a rebuild.
 *   - The tree is produced by the exporter (Go `agent/internal/export`; WASM `opm-export.wasm` in Cloud Functions; the same
 *     code runs at `docker compose up` as `opm/bin/opm-render init` for values that only exist on the VM: secrets, auto
 *     host, cross-site hosts, required variables of templates). Wire format: ExportRequest → ExportResult below.
 *   - Queue in Firestore (`githubMirror/config/queue/{itemId}`, idempotent item ids, ordered per branch, retry / backoff),
 *     pushed with the GitHub REST Git Data API (blobs / trees / commits / refs), no git binary.
 *   - Admin → GitHub page (accounts.admin): repo owner / name, token (fine-grained PAT, Contents read + write; stored like a
 *     registry password: masked, 60 s reveal, audited, replaceable), enabled, Test connection, per-branch status, Sync now,
 *     Resync everything. Every config change is audited.
 */
import { createClient, ep, type AuthProvider, type Client } from './http.js';
import type { Config, ConfigView, Id, ListResponse, Masked, RevisionCatalog, RevealResponse, Sha256, Slug, Ts, Ulid, VolumeTree } from './types.js';

// ───────────────────────────────────────────────────────────── constants
/** Export wire / tree format (opm/export.json `format`; opm-render refuses an unknown major). */
export const MIRROR_EXPORT_FORMAT = 1 as const;
/** git trailers (one per line after a blank line, "Key: value"); OPM-Commit is THE idempotency key of a mirrored commit. */
export const MIRROR_TRAILERS = {
  commit: 'OPM-Commit', //          commitId (ULID) of the OPM commit
  branch: 'OPM-Branch', //          branchKey (template:<id> | deployment:<id> | agent:<dep>/<site>)
  seq: 'OPM-Seq', //                commit seq on the OPM branch
  volume: 'OPM-Volume-Version', //  persist snapshot versionId (agent branches)
  drift: 'OPM-Drift', //            drift fingerprint (uncommitted/<site>)
} as const;
/** retry backoff of a failing queue item (seconds, by attempt; the last value repeats). */
export const MIRROR_BACKOFF_SEC = [30, 60, 120, 300, 900, 1800, 3600] as const;
/** a file bigger than this is never pushed (GitHub blob limit); it is listed in `<dir>/.opm-skipped.txt` + a note. */
export const MIRROR_MAX_FILE_BYTES = 100 * 1024 * 1024;
/** pinned helper images of the compose export (digest-pinned by the exporter; see docs-v2/github-mirror.md). */
export const MIRROR_HELPER_IMAGES = {
  init: 'busybox:1.37@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e',
  edge: 'caddy:2.11.7@sha256:f2a1290d0463aad60660d4ec134943f183ee2a5f6c3eb7bf32dd984f2f020772',
} as const;
/** default branch created ONLY when the repository is empty (the Git Data API refuses an empty repository). */
export const MIRROR_DEFAULT_BRANCH = 'main' as const;

// ───────────────────────────────────────────────────────────── branch names
export type MirrorBranchKind = 'template' | 'deployment' | 'agent' | 'uncommitted';
/** What a git branch mirrors. templateId for template; deploymentId (+ siteSlug for agent / uncommitted) otherwise. */
export interface MirrorBranchRef {
  kind: MirrorBranchKind;
  templateId: Id | null;
  deploymentId: Id | null;
  siteSlug: Slug | null;
}
/** Entity slug rule (= RE.slug of validate.ts): 2–40 chars, starts with a letter. */
export const ENTITY_SLUG_RE = /^[a-z][a-z0-9-]{1,39}$/;
/** The git branch of a mirrored OPM branch. Throws on a bad slug. */
export function mirrorBranchName(kind: MirrorBranchKind, entitySlug: string, siteSlug?: string | null): string {
  if (!ENTITY_SLUG_RE.test(entitySlug)) throw new Error(`bad entity slug ${JSON.stringify(entitySlug)}`);
  if (kind === 'template') return `template/${entitySlug}`;
  if (kind === 'deployment') return `deployment/${entitySlug}/cloud`;
  if (!siteSlug || !ENTITY_SLUG_RE.test(siteSlug)) throw new Error(`bad site slug ${JSON.stringify(siteSlug)}`);
  return `deployment/${entitySlug}/${kind}/${siteSlug}`;
}
/** Inverse of mirrorBranchName (null = not a mirror branch name). */
export function parseMirrorBranchName(name: string): { kind: MirrorBranchKind; entitySlug: string; siteSlug: string | null } | null {
  const S = '([a-z][a-z0-9-]{1,39})';
  let m = new RegExp(`^template/${S}$`).exec(name);
  if (m) return { kind: 'template', entitySlug: m[1]!, siteSlug: null };
  m = new RegExp(`^deployment/${S}/cloud$`).exec(name);
  if (m) return { kind: 'deployment', entitySlug: m[1]!, siteSlug: null };
  m = new RegExp(`^deployment/${S}/(agent|uncommitted)/${S}$`).exec(name);
  if (m) return { kind: m[2] as 'agent' | 'uncommitted', entitySlug: m[1]!, siteSlug: m[3]! };
  return null;
}
/** Firestore doc id of a git branch (no "/" allowed in ids): "/" → "~". */
export function mirrorBranchDocId(gitBranch: string): string {
  return gitBranch.replace(/\//g, '~');
}

// ───────────────────────────────────────────────────────────── entity slugs (Template.slug / Deployment.slug)
/**
 * Slug from a label: NFKD, ASCII letters / digits kept (lowercase), every other run → "-", trimmed, ≤ 40 chars; a slug
 * that does not start with a letter gets the prefix (default "x-"); empty → the prefix + "1" ("x-1").
 */
export function slugifyLabel(label: string, prefix = 'x-'): string {
  let s = (label ?? '')
    .normalize('NFKD')
    .replace(/[̀-ͯ]/g, '')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
  if (s === '') s = prefix + '1';
  else if (!/^[a-z]/.test(s)) s = prefix + s;
  s = s.slice(0, 40).replace(/-+$/g, '');
  if (s.length < 2) s = (s + '-1').slice(0, 40);
  return s;
}
/** First free of base, base-2, base-3 … (≤ 40 chars: the base is cut to make room for the suffix). */
export function uniqueSlug(base: string, taken: ReadonlySet<string>): string {
  if (!taken.has(base)) return base;
  for (let n = 2; ; n++) {
    const suf = `-${n}`;
    const s = base.slice(0, 40 - suf.length).replace(/-+$/g, '') + suf;
    if (!taken.has(s)) return s;
  }
}

// ───────────────────────────────────────────────────────────── commit messages + trailers
/**
 * The git commit message: the OPM message (trailing whitespace trimmed, CRLF → LF), a blank line, then the trailers in
 * the given order ("Key: value"; values single-line). An OPM message is never empty (§21 #104 a).
 */
export function mirrorCommitMessage(message: string, trailers: [string, string][]): string {
  const body = (message ?? '').replace(/\r\n?/g, '\n').replace(/\s+$/g, '');
  const lines = trailers.map(([k, v]) => `${k}: ${String(v).replace(/[\r\n]+/g, ' ').trim()}`);
  return `${body}\n\n${lines.join('\n')}\n`;
}
/** Trailers of the LAST paragraph of a message (later keys win). */
export function parseMirrorTrailers(message: string): Record<string, string> {
  const paras = (message ?? '').replace(/\r\n?/g, '\n').trim().split(/\n\s*\n/);
  const last = paras[paras.length - 1] ?? '';
  const out: Record<string, string> = {};
  for (const line of last.split('\n')) {
    const m = /^([A-Za-z][A-Za-z0-9-]*):\s?(.*)$/.exec(line);
    if (!m) return {};
    out[m[1]!] = m[2]!;
  }
  return out;
}
/** A git author / committer: OPM commit author (person: account name + email; system: "OPM" <opm@users.noreply.opm>). */
export interface MirrorPerson {
  name: string;
  email: string;
  date: Ts;
}
export const MIRROR_SYSTEM_PERSON = { name: 'OPM', email: 'opm@users.noreply.opm' } as const;
/** agent-side commits / snapshots without a person: "OPM site <site>" <opm-site-<site>@users.noreply.opm>. */
export function mirrorSitePerson(siteSlug: string): { name: string; email: string } {
  return { name: `OPM site ${siteSlug}`, email: `opm-site-${siteSlug}@users.noreply.opm` };
}

// ───────────────────────────────────────────────────────────── .env names
/** .env variable of a secret / required variable / site host. Slugs never hold "_" so the mapping is injective. */
export function mirrorEnvName(kind: 'secret' | 'var' | 'site-host' | 'derived', slug: string, extra?: string): string {
  const up = (s: string) => s.toUpperCase().replace(/[^A-Z0-9]/g, '_');
  switch (kind) {
    case 'secret':
      return `OPM_SECRET_${up(slug)}`;
    case 'var':
      return `OPM_VAR_${up(slug)}`;
    case 'site-host':
      return `OPM_SITE_${up(slug)}_HOST`;
    case 'derived':
      return `OPM_DERIVED_${up(slug)}_${up(extra ?? '')}`;
  }
}
/** this site's own address when its host is auto (= the VM address) */
export const MIRROR_ENV_SITE_HOST = 'OPM_SITE_HOST' as const;
/** optional overrides of the published edge ports (default = the config's edge httpPort / httpsPort) */
export const MIRROR_ENV_EDGE_PORTS = { http: 'OPM_EDGE_HTTP_PORT', https: 'OPM_EDGE_HTTPS_PORT' } as const;

// ───────────────────────────────────────────────────────────── GitHub repository + token
export const GITHUB_OWNER_RE = /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$/;
export const GITHUB_REPO_RE = /^(?!\.{1,2}$)[A-Za-z0-9._-]{1,100}$/;
/** fine-grained PAT (github_pat_…) or classic (ghp_…); anything 20–255 printable chars is accepted (GitHub Apps tokens…) */
export const GITHUB_TOKEN_RE = /^[\x21-\x7e]{20,255}$/;
/** "github_pat_11AB…wxyz" → "github_pat_•••wxyz" (prefix up to the first "_" run kept, last 4 kept); never the value. */
export function maskGithubToken(token: string | null | undefined): string | null {
  if (!token) return null;
  const m = /^(github_pat_|ghp_|gho_|ghu_|ghs_|ghr_)/.exec(token);
  const tail = token.slice(-4);
  return `${m ? m[1] : ''}•••${tail}`;
}

/** Firestore `githubMirror/config` (server-only; never readable by clients — firestore.rules deny). */
export interface GithubMirrorDoc {
  enabled: boolean;
  owner: string;
  repo: string;
  /** plain token, stored like Registry.password (SPEC §21 #1, #84): masked in every read, revealed only by revealGithubToken */
  token: string | null;
  tokenSetAt: Ts | null;
  tokenSetBy: string | null;
  /** set when the mirror created the first commit of an EMPTY repository (README on MIRROR_DEFAULT_BRANCH) */
  initializedAt: Ts | null;
  lastTest: GithubTestResponse | null;
  /** rate limited / auth failure: no item is pushed before this time */
  pausedUntil: Ts | null;
  pauseReason: string | null;
  createdAt: Ts;
  createdBy: string;
  updatedAt: Ts;
  updatedBy: string;
}
export interface GithubMirrorView {
  configured: boolean; //     owner + repo + token set
  enabled: boolean;
  owner: string;
  repo: string;
  repoUrl: string | null; //  https://github.com/<owner>/<repo>
  token: string | null; //    maskGithubToken(…) — never the value
  tokenSet: boolean;
  tokenSetAt: Ts | null;
  tokenSetBy: string | null;
  initializedAt: Ts | null;
  lastTest: GithubTestResponse | null;
  pausedUntil: Ts | null;
  pauseReason: string | null;
  summary: { branches: number; synced: number; queued: number; errors: number; pendingItems: number; lastPushAt: Ts | null };
  exportFormat: typeof MIRROR_EXPORT_FORMAT;
  /** sha256 of the opm-render binaries put into every branch (opm/bin), by arch */
  tool: { version: string; amd64: Sha256 | null; arm64: Sha256 | null };
  updatedAt: Ts | null;
  updatedBy: string | null;
}
/** PATCH: any subset. token: string = replace, null = remove (disables pushing). Enabling needs owner + repo + token. */
export interface PatchGithubMirrorRequest {
  owner?: string;
  repo?: string;
  enabled?: boolean;
  token?: string | null;
}
export interface GithubCheck {
  name: 'token' | 'repo' | 'contents-read' | 'contents-write' | 'rate';
  ok: boolean;
  message: string;
}
export interface GithubTestResponse {
  ok: boolean;
  at: Ts;
  checks: GithubCheck[];
  repo: { fullName: string; private: boolean; defaultBranch: string | null; empty: boolean; htmlUrl: string } | null;
  /** the token's login (GET /user), when the token may read it */
  login: string | null;
  rate: { limit: number; remaining: number; resetAt: Ts } | null;
}
export type RevealGithubTokenResponse = RevealResponse;

// ───────────────────────────────────────────────────────────── queue + branch state
export type MirrorQueueOp =
  | 'commit' //            one OPM commit (template / deployment / agent branch)
  | 'volume-snapshot' //   one persist-to-cloud snapshot version (agent branch)
  | 'drift' //             uncommitted/<site>: reset to ONE snapshot commit of the drift on top of the agent branch
  | 'drift-clear' //       uncommitted/<site>: delete the ref (drift committed or discarded)
  | 'catch-up' //          push every OPM commit / snapshot of the branch not on GitHub yet, in order (Sync now, agent online)
  | 'rebuild'; //          rebuild the branch from OPM history (seq 1 … head), force-update the ref (Resync everything)
export type MirrorItemState = 'pending' | 'running' | 'done' | 'skipped' | 'failed';
/**
 * Firestore `githubMirror/config/queue/{itemId}`; itemId = mirrorItemId(…) (idempotent: enqueueing twice = one item).
 * Processed in `orderKey` order per gitBranch, one branch at a time (lease on the branch doc).
 */
export interface MirrorQueueItem {
  itemId: string;
  gitBranch: string;
  branch: MirrorBranchRef;
  op: MirrorQueueOp;
  /** commitId (commit) | versionId (volume-snapshot) | fingerprint (drift) | "" */
  opmId: string;
  seq: number | null;
  /** sortable: createdAt ISO of the OPM object + "|" + seq zero-padded (10) + "|" + opmId */
  orderKey: string;
  state: MirrorItemState;
  attempts: number;
  nextAttemptAt: Ts;
  createdAt: Ts;
  startedAt: Ts | null;
  doneAt: Ts | null;
  gitSha: string | null;
  lastError: { at: Ts; message: string; status: number | null } | null;
  /** who / what queued it: "commit" (writer hook), "agent-push", "agent-online", "snapshot", "drift", "sync", "resync", "enable" */
  reason: string;
}
export function mirrorOrderKey(createdAt: Ts, seq: number | null, opmId: string): string {
  return `${createdAt}|${String(seq ?? 0).padStart(10, '0')}|${opmId}`;
}
/** idempotent queue item id (Firestore-safe): <docId of branch>__<op>__<opmId or "-"> (≤ 1500 bytes) */
export function mirrorItemId(gitBranch: string, op: MirrorQueueOp, opmId: string): string {
  return `${mirrorBranchDocId(gitBranch)}__${op}__${opmId || '-'}`.replace(/[^A-Za-z0-9~_.-]/g, '_');
}
/** backoff before attempt n+1 after n failed attempts (n ≥ 1). */
export function mirrorBackoffSec(attempts: number): number {
  const i = Math.max(0, Math.min(MIRROR_BACKOFF_SEC.length - 1, attempts - 1));
  return MIRROR_BACKOFF_SEC[i]!;
}
export type MirrorBranchStatus = 'synced' | 'queued' | 'pushing' | 'error' | 'paused' | 'deleted' | 'never';
/** Firestore `githubMirror/config/branches/{mirrorBranchDocId}` + the status table row. */
export interface MirrorBranchState {
  gitBranch: string;
  branch: MirrorBranchRef;
  /** "Single", "Test 02", "Test 02 · site-01", "Test 02 · site-01 (uncommitted)" */
  label: string;
  status: MirrorBranchStatus;
  /** the newest OPM object on GitHub */
  last: { op: MirrorQueueOp; opmId: string; seq: number | null; message: string; at: Ts } | null;
  gitSha: string | null;
  /** https://github.com/<owner>/<repo>/commit/<sha> */
  gitUrl: string | null;
  /** https://github.com/<owner>/<repo>/tree/<branch> */
  treeUrl: string | null;
  pushedAt: Ts | null;
  queue: { pending: number; failed: number; nextAttemptAt: Ts | null };
  lastError: { at: Ts; message: string; status: number | null } | null;
  /** export notes of the last pushed tree (what is NOT equivalent without the agent), ≤ 50 */
  notes: ExportNote[];
  /** lease: a worker pushing this branch until this time */
  leaseUntil: Ts | null;
  updatedAt: Ts;
}
export interface MirrorSyncResponse {
  enqueued: number;
  branches: number;
  /** items processed inline before the response (bounded by time) */
  processed: number;
}

// ───────────────────────────────────────────────────────────── agent → cloud drift (agent ≥ 0.15.0)
export interface DriftItemSummary {
  key: string; //    merge item key (§4.1 of docs-v2/branches-commits.md)
  origin: 'docker' | 'file' | 'secret-file' | 'volume-content';
  detail: string;
}
export interface DriftNoteSummary {
  fingerprint: string;
  label: string;
  detail: string;
}
/** POST /v1/agent/branch/drift: the site's uncommitted drift (no items and no notes = cleared). Secret values never sent. */
export interface AgentDriftPushRequest {
  headCommitId: Ulid | null;
  scannedAt: Ts;
  /** sha256 of the sorted item keys + note fingerprints + the liveConfig hash; the same fingerprint twice = no change */
  fingerprint: string;
  items: DriftItemSummary[];
  notes: DriftNoteSummary[];
  /** head config with every drift item applied (secrets masked); null when cleared */
  liveConfig: ConfigView | null;
}
export interface AgentDriftPushResponse {
  stored: boolean;
  /** a mirror item was queued (mirror enabled) */
  mirrored: boolean;
}
/** AgentBranchDoc.drift (stored part) */
export interface AgentBranchDrift {
  headCommitId: Ulid | null;
  scannedAt: Ts;
  fingerprint: string;
  items: DriftItemSummary[];
  notes: DriftNoteSummary[];
  receivedAt: Ts;
}

// ───────────────────────────────────────────────────────────── exporter wire format (Go internal/export ⇄ backend)
/**
 * Input of the exporter (stdin of opm-export.wasm; also written into every tree as `opm/export.json`, which
 * `opm-render init` reads on the VM). NEVER holds a secret value: `config.secrets[].value` is null everywhere and
 * `secretsSet` lists the secrets that have a value in OPM.
 */
export interface ExportRequest {
  format: typeof MIRROR_EXPORT_FORMAT;
  branch: {
    kind: MirrorBranchKind;
    gitBranch: string;
    entitySlug: string;
    label: string;
    templateId: Id | null;
    deploymentId: Id | null;
    siteSlug: Slug | null;
  };
  commit: {
    /** OPM commitId | volume versionId | drift fingerprint */
    id: string;
    seq: number | null;
    kind: string; //    CommitKind | "volume-snapshot" | "drift"
    message: string;
    author: { name: string; email: string };
    createdAt: Ts;
    /** config hash of the OPM commit (sha256(JCS(config)), null for drift) */
    hash: Sha256 | null;
    parentId: string | null;
  };
  /** the deployment id used by expressions (deployment.id, mosquitto_passwd salt, network names); templates: "template-<slug>" */
  deploymentId: string;
  config: Config;
  secretsSet: Slug[];
  catalog: RevisionCatalog;
  /** pull references by image key `<serviceSlug>:<version>` (from services/{s}/images/{v}: host / repository / tag / digest) */
  images: Record<string, ExportImageRef>;
  /** UTF-8 sources of every render-expressions blob the sites use (File contents + starter content entries), by sha256 hex */
  texts: Record<string, string>;
  /** sites to export: template / deployment = every site; agent / uncommitted = the own site */
  sites: Slug[];
  /** agent branches: the latest persist-to-cloud snapshot per site + volume at this point of the branch history */
  persist: Record<Slug, Record<string, ExportPersistSnapshot>>;
  /** opm-render binaries put into opm/bin (the backend fills them from its build) */
  tool: { version: string; amd64: { sha256: Sha256; size: number } | null; arm64: { sha256: Sha256; size: number } | null };
}
export interface ExportImageRef {
  /** what `image:` says: "<host>/<repository>[:<tag>]@<digest>" ("docker.io/library/redis:7.4@sha256:…"); a local
   *  image (drift, `<svc>:local-<hex>`) or an image whose doc is gone: "<serviceSlug>:<version>" + note */
  pull: string;
  registryHost: string | null;
  /** registry has credentials in OPM (README: docker login <host>) */
  private: boolean;
  /** "linux/amd64" — the platform OPM imported */
  platform: string;
}
export interface ExportPersistSnapshot {
  versionId: Ulid;
  seq: number;
  createdAt: Ts;
  treeHash: Sha256;
  tree: VolumeTree;
}
export type ExportFileMode = '100644' | '100755';
/** One file of the tree. Exactly one of text / base64 / blob / tool. Sorted by path (UTF-16 order). */
export interface ExportFile {
  path: string;
  mode: ExportFileMode;
  text?: string;
  base64?: string;
  /** sha256 hex of a cloud Storage blob `files/<hex>` (File contents with render none, starter content, snapshots) */
  blob?: string;
  /** opm-render binary of this arch (the backend's build artefact) */
  tool?: 'linux-amd64' | 'linux-arm64';
  size: number;
}
export interface ExportNote {
  /** e.g. HEALTH_LOG (log health check has no compose equivalent), EDGE_DNS01, LOCAL_IMAGE, ENV_DERIVED, SKIPPED_LARGE … */
  code: string;
  site: Slug | null;
  message: string;
}
export interface ExportEnvVar {
  name: string;
  site: Slug;
  kind: 'secret' | 'var' | 'site-host' | 'edge-port' | 'other';
  required: boolean;
  description: string;
}
export interface ExportResult {
  format: typeof MIRROR_EXPORT_FORMAT;
  files: ExportFile[];
  notes: ExportNote[];
  env: ExportEnvVar[];
}

// ───────────────────────────────────────────────────────────── API
const GH = '/v1/github-mirror';
export const GITHUB_MIRROR_CLOUD_API = {
  getGithubMirror: ep<void, GithubMirrorView>()({ method: 'GET', path: GH, auth: 'human', perm: 'accounts.admin', body: 'none', res: 'json', errors: [], summary: '§21 #106: the mirror settings (token masked) + summary.' }),
  patchGithubMirror: ep<PatchGithubMirrorRequest, GithubMirrorView>()({ method: 'PATCH', path: GH, auth: 'human', perm: 'accounts.admin', body: 'json', res: 'json', errors: ['invalid'], activities: ['audit:github.updated'], summary: '§21 #106: change owner / repo / enabled / token (string = replace, null = remove). Enabling needs owner + repo + token; enabling (or changing the repository) queues a catch-up of every branch. Audited (token change: "***"; data.tokenReplaced / tokenRemoved).' }),
  testGithubMirror: ep<void, GithubTestResponse>()({ method: 'POST', path: `${GH}/test`, auth: 'human', perm: 'accounts.admin', body: 'none', res: 'json', errors: ['invalid'], activities: ['audit:github.tested'], summary: '§21 #106: GET /repos (exists, private, empty), contents read (matching-refs), contents write (an unreferenced blob), rate limit; stored as lastTest. invalid: owner / repo / token missing.' }),
  revealGithubToken: ep<void, RevealGithubTokenResponse>()({ method: 'POST', path: `${GH}/reveal-token`, auth: 'human', perm: 'accounts.admin', body: 'none', res: 'json', errors: ['not-found', 'rate-limited'], activities: ['audit:github.token-revealed'], summary: '§21 #106: the token (RevealValue 60 s; audited; no-store; 30/min per uid). not-found: no token.' }),
  listGithubMirrorBranches: ep<void, ListResponse<MirrorBranchState>>()({ method: 'GET', path: `${GH}/branches`, auth: 'human', perm: 'accounts.admin', body: 'none', res: 'json', errors: [], summary: '§21 #106: one row per mirrored branch (every template, deployment, agent branch, uncommitted branch), queue state + last error.' }),
  listGithubMirrorQueue: ep<void, ListResponse<MirrorQueueItem>, { gitBranch?: string; state?: MirrorItemState; limit?: number }>()({ method: 'GET', path: `${GH}/queue`, auth: 'human', perm: 'accounts.admin', body: 'none', res: 'json', errors: [], summary: '§21 #106: queue items (newest first; ≤ 200).' }),
  syncGithubMirror: ep<{ gitBranch?: string }, MirrorSyncResponse>()({ method: 'POST', path: `${GH}/sync`, auth: 'human', perm: 'accounts.admin', body: 'json', res: 'json', errors: ['invalid', 'conflict'], activities: ['audit:github.synced'], summary: '§21 #106: Sync now — queue a catch-up of every branch (or one) and process due items now (failed items retried at once). conflict: the mirror is disabled.' }),
  resyncGithubMirror: ep<{ gitBranch?: string }, MirrorSyncResponse>()({ method: 'POST', path: `${GH}/resync`, auth: 'human', perm: 'accounts.admin', body: 'json', res: 'json', errors: ['invalid', 'conflict'], activities: ['audit:github.resynced'], summary: '§21 #106: Resync everything — rebuild every branch (or one) from OPM history and force-update the refs (deterministic: unchanged history keeps its git shas).' }),
  // agent (site key)
  agentPushDrift: ep<AgentDriftPushRequest, AgentDriftPushResponse>()({ method: 'POST', path: '/v1/agent/branch/drift', auth: 'agent', perm: 'site key', body: 'json', res: 'json', errors: ['invalid'], summary: '§21 #106: the site reports its uncommitted drift (agent ≥ 0.15.0; when the fingerprint changes and at start). Stored on the AgentBranchDoc; the mirror resets / deletes deployment/<slug>/uncommitted/<site>.' }),
} as const;

export type GithubMirrorClient = Client<typeof GITHUB_MIRROR_CLOUD_API>;
export function createGithubMirrorClient(baseUrl: string, auth: AuthProvider | null, init?: { fetch?: typeof fetch }): GithubMirrorClient {
  return createClient(GITHUB_MIRROR_CLOUD_API, baseUrl, auth, init);
}

/** Masked marker reused from types (re-exported for the GUIs). */
export type { Masked };
