/**
 * Branches and commits (SPEC-final §21 #104, docs-v2/branches-commits.md).
 *
 * Every template, every deployment and every (deployment, site) pair is a BRANCH = a linear list of immutable COMMITS.
 * §21 #105: there is NO server-side working copy — edits live in the browser (one pending change set per entity, kept in
 * localStorage per entity + head commit) until "Commit" (message required) writes a new commit from the base head plus
 * the changes (optimistic: 409 stale when the head moved → the GUI rebases with `rebaseChanges`). On the agent,
 * "uncommitted" means ONLY drift (changes made outside the agent). Nothing reaches history without a message. Any two branches can be
 * compared and cherry-picked (template ↔ template ↔ deployment ↔ agent branch) with ONE engine (this file; Go port
 * agent/internal/branch passes testdata/branch-vectors.json).
 *
 * Items (keys) — lists keyed by slug / name, never position; a site / container / volume present on ONE side only is
 * one whole item (a rename = remove + add):
 *   variables/<slug>                         definition without the value
 *   variables/<slug>/value                   the value
 *   secrets/<slug>                           definition + generate rule
 *   secrets/<slug>/value                     { sha256 } of the plain value (only when both sides hold values)
 *   files/<slug>                             metadata + content ref (sha256)
 *   sites/<s>                                a whole site
 *   sites/<s>/settings/<field>               label host agentPort agentGuiAliasPort notFoundPageId depends
 *   sites/<s>/edge/<field>                   httpPort httpsPort maxBodyBytes
 *   sites/<s>/edge/hosts/<hostname>          sites/<s>/edge/routes/<routeId>
 *   sites/<s>/volumes/<v>                    a whole volume (one side lacks it) or its definition without own content
 *   sites/<s>/volumes/<v>/content/<path>     one starter-content file (tree seeds; single-file seeds: …/content)
 *   sites/<s>/volumes/<v>/content/<dir>/     one empty folder of the starter content
 *   sites/<s>/devtools/<presetSlug>          a dev-tool preset
 *   sites/<s>/containers/<c>                 a whole container
 *   sites/<s>/containers/<c>/<field>         image enabled command entrypoint restart resources healthcheck depends ports volumes
 *   sites/<s>/containers/<c>/env/<NAME>      value string / null / { secretRef }
 *
 * Pure functions, no I/O: backend, frontend (mock + previews), agent GUI and tests share them.
 */
import { canonicalize, compareUtf16 } from './canonical.js';
import { maskDevToolLiterals } from './diff.js';
import type { DevToolPreset } from './devtools.js';
import { sha256, toHex, utf8 } from './hash.js';
import { ep } from './http.js';
import type {
  Config,
  ConfigInput,
  ConfigView,
  Container,
  DiffChange,
  DiffSection,
  FastForwardUse,
  FileDef,
  Id,
  ProposalStatus,
  RevId,
  RevisionCatalog,
  SecretDef,
  SecretDefView,
  Sha256,
  Site,
  SiteStaging,
  SiteVolume,
  VolumeSeed,
  Slug,
  Ts,
  Uid,
  Ulid,
  UpdateRun,
  ValidationIssue,
  Variable,
  VariableValue,
  VolumeTreeEntry,
  PageResponse,
  ListResponse,
} from './types.js';

// ───────────────────────────────────────────────────────────────────────────── constants
/** First agent version with branches (agent branch, drift, merge); older agents get compat proposals (§21 #104 j). */
export const BRANCHES_MIN_AGENT = '0.14.0';
export const COMMIT_MESSAGE_MAX = 2000;
export const COMMIT_SUMMARY_MAX = 200;
/** at most this many picked keys are stored in CommitSource.picks (more → truncated) */
export const COMMIT_PICKS_MAX = 500;
/** at most this many keys in CommitStats.keys */
export const COMMIT_STATS_KEYS_MAX = 200;
/** deployment commits the agent may fetch / the cloud serves images + blobs for (head + N newest) */
export const CLOUD_RECENT_COMMITS = 10;
/** the message of a create commit when none is given */
export const createMessage = (label: string, seq: number | null) => (seq === null ? `created from ${label}` : `created from ${label}@${seq}`);

// ───────────────────────────────────────────────────────────────────────────── types
export type BranchKind = 'template' | 'deployment' | 'agent';
export type BranchRef = { kind: 'template'; templateId: Id } | { kind: 'deployment'; deploymentId: Id } | { kind: 'agent'; deploymentId: Id; siteSlug: Slug };
/** A point to compare from: a commit of a branch, its head, or the working copy (own branch: Review). */
export interface BranchPoint {
  branch: BranchRef;
  at: 'head' | 'working' | { commitId: Ulid };
}
export type CommitKind = 'create' | 'change' | 'merge' | 'cherry-pick' | 'revert' | 'reset' | 'drift' | 'rollback' | 'migrated' | 'legacy';
export const COMMIT_KINDS: readonly CommitKind[] = ['create', 'change', 'merge', 'cherry-pick', 'revert', 'reset', 'drift', 'rollback', 'migrated', 'legacy'];

export interface CommitAuthor {
  /** unknown (§21 #113): an agent auto-commit of changes made in Docker outside OPM ("UNKNOWN (changed in Docker)") */
  kind: 'person' | 'system' | 'unknown';
  email: string; //          account email (cloud) / declared actor (agent) / "system"
  uid: Uid | null; //        cloud account uid; null on the agent
  name: string | null; //    account name at commit time
  siteSlug: Slug | null; //  agent commits: the site whose GUI was used
}
/** §21 #113 a site's drift policy: what happens with changes made in Docker outside OPM */
export interface DriftPolicy {
  mode: 'ask' | 'auto-commit';
}
export interface CommitSource {
  branch: BranchRef;
  commitId: Ulid | null; //  null: a pre-0.14 template state without a commit (migrated base)
  seq: number | null;
  label: string; //          "Single", "Test 02", "Test 02 · site-01"
  picks: string[] | 'all';
  skipped: number; //        source changes not taken
  truncated?: boolean;
}
export interface CommitStats {
  added: number;
  changed: number;
  removed: number;
  keys: string[]; // ≤ COMMIT_STATS_KEYS_MAX
  truncated: boolean;
}
export interface CommitData {
  /** agent drift commits: the drift notes accepted */
  acceptedDrift?: { fingerprint: string; label: string; detail: string }[];
  /** agent: persist-to-cloud content recorded by this commit (a #101 snapshot taken at commit time) */
  volumeContent?: { siteSlug: Slug; volumeSlug: string; treeHash: Sha256; versionId: Ulid | null }[];
  /** migrated: the revision / template version it came from */
  migratedFrom?: { revId?: RevId; seq?: number; proposalStatus?: ProposalStatus; templateVersion?: number; localProposal?: boolean };
  /** agent commits: the update run that applied it (null = no runtime change, e.g. drift) */
  updateId?: Ulid | null;
  /** rollback commits: fast-forward use of the restore wait (§21 #95) */
  fastForward?: FastForwardUse | null;
  /** cloud: the compat revision / proposal written for agents < 0.14 (§21 #104 j) */
  compat?: { revId: RevId; seq: number } | null;
  /**
   * §21 #105 secret VALUES changed by this commit — slugs only, never a value (the values live in the commit config,
   * masked in every human read). set = a typed value, generated = from the generate rule, cleared = value removed.
   */
  secretValues?: SecretValueChanges;
  /** §21 #105 migrate-j: a pre-0.14.1 server working copy auto-committed so nothing is lost */
  autoCommitted?: { from: 'cloud-working-copy' | 'agent-working-copy'; items: number };
}
export interface SecretValueChanges {
  set: Slug[];
  generated: Slug[];
  cleared: Slug[];
}
export interface Commit {
  commitId: Ulid;
  branch: BranchRef;
  seq: number; //               parent.seq + 1; first = 1
  parentId: Ulid | null;
  kind: CommitKind;
  message: string; //           1–2000; first line 1–200 = the summary
  author: CommitAuthor;
  createdAt: Ts;
  source: CommitSource | null;
  revertOf: { commitId: Ulid; seq: number } | null;
  resetTo: { commitId: Ulid; seq: number } | null;
  rollbackOf: { snapshotId: Ulid } | null;
  config: Config; //            templates: secret values null; deployment + agent: plain values (masked in every human read)
  hash: Sha256; //              sha256(JCS(config)) — equal to the revision hash of the same config
  catalog: RevisionCatalog | null;
  catalogHash: Sha256 | null;
  stats: CommitStats;
  data: CommitData;
}
export type CommitView = Omit<Commit, 'config'> & { config: ConfigView };
export type CommitSummary = Pick<Commit, 'commitId' | 'branch' | 'seq' | 'parentId' | 'kind' | 'message' | 'author' | 'createdAt' | 'source' | 'revertOf' | 'resetTo' | 'rollbackOf' | 'hash' | 'stats'>;
export type CommitRef = Pick<Commit, 'commitId' | 'seq' | 'hash' | 'message' | 'author' | 'createdAt' | 'kind'>;
export interface BranchOrigin {
  branch: BranchRef;
  commitId: Ulid | null;
  seq: number | null;
  label: string;
  /** migrated pre-0.14 links: the template version the deployment was cloned from */
  templateVersion?: number;
}
/** Template.branch / Deployment.branch */
export interface BranchState {
  head: CommitRef | null; // null only for migrated never-published deployments
  origin: BranchOrigin | null;
  commitCount: number;
  // §21 #105: `uncommitted` REMOVED — the cloud keeps no working copy (pending edits live in the browser; migrate-j
  // deleted the field). Readers of 0.14.0 documents must ignore it.
}
export interface MergeSkip {
  key: string;
  theirsHash: string;
  sourceSeq: number | null;
  skippedAt: Ts;
  skippedBy: string;
}
/** The merge base of (target, source), stored on the target, keyed by the source branch key. */
export interface MergeBase {
  sourceKey: string;
  source: BranchRef;
  sourceCommitId: Ulid | null;
  sourceSeq: number | null;
  targetCommitId: Ulid | null;
  base: Config; // secret VALUES replaced by "sha256:<hex>" markers (secretValueMarker) — never stored twice in plain
  skipped: MergeSkip[];
  label: string; // "Single@12", "template Single v51 (pre-0.14 base)"
  updatedAt: Ts;
  updatedBy: string;
}

// ── items + plans
export type MergeKind = 'add' | 'remove' | 'change' | 'theirs' | 'ours' | 'conflict' | 'skipped';
export const MERGE_KINDS: readonly MergeKind[] = ['add', 'remove', 'change', 'theirs', 'ours', 'conflict', 'skipped'];
export type MergeSection = DiffSection;
export interface MaskedSlot {
  has: boolean;
  value: unknown;
}
export interface MergeItem {
  key: string;
  section: MergeSection;
  siteSlug: Slug | null;
  label: string;
  kind: MergeKind;
  /** what taking the SOURCE does to the target */
  effect: 'add' | 'change' | 'remove';
  base: MaskedSlot | null; // null = no base
  target: MaskedSlot;
  source: MaskedSlot;
  preselect: boolean;
  scalar: boolean;
  /** keys this item needs in the result (only keys that are items of the same plan) */
  requires: string[];
  /** permission / scope reason; null = pickable */
  locked: string | null;
  /** files + volume content: blob refs for text diffs */
  blob?: { base: Sha256 | null; target: Sha256 | null; source: Sha256 | null; volumeSlug?: string; path?: string; fileSlug?: string };
  note?: string;
  /** itemHash of the source slot (skip records) */
  sourceHash: string;
  /** set on a skipped item */
  skippedAt?: Ts;
}
export interface MergePlanBase {
  kind: 'record' | 'origin' | 'none';
  sourceCommitId: Ulid | null;
  label: string;
}
export interface MergePlan {
  source: { point: BranchPoint; label: string; commit: CommitRef | null };
  target: { branch: BranchRef; label: string; head: CommitRef | null; workingClean: boolean; uncommitted: number };
  base: MergePlanBase;
  items: MergeItem[];
  counts: Record<MergeKind, number>;
  fastForward: boolean;
  /** agent target: the reference copies of other sites that the merge refreshes (§21 #104 D6) */
  references?: { updatedSites: Slug[] };
  diffOnly: boolean;
}
export interface MergeChoice {
  key: string;
  take: 'source' | 'target' | 'custom';
  value?: unknown;
}
export interface MergeRequest {
  source: BranchPoint;
  target: BranchRef;
  /** the target head the plan was made against (409 stale when it moved) */
  targetHead: Ulid | null;
  /** the source commit the plan was made against (409 source-changed when a head point moved) */
  sourceCommit: Ulid | null;
  choices: MergeChoice[];
  values?: Record<string, VariableValue>;
  /** deployment target only: values for required secrets the merge adds without one (plain; never echoed back) */
  secretValues?: Record<string, string>;
}
export interface MergeMissing {
  variables: { variableSlug: Slug; label: string; type: Variable['type']; options: string[] | null; required: boolean }[];
  secrets: { secretSlug: Slug; label: string }[];
}
export interface MergePreview {
  result: ConfigView;
  changes: DiffChange[];
  validation: { errors: ValidationIssue[]; warnings: ValidationIssue[] };
  unresolved: string[];
  missing: MergeMissing;
  /** bad choices (unknown key = the plan moved, custom on a non-scalar …) */
  choiceErrors: { key: string; message: string }[];
  kind: 'merge' | 'cherry-pick';
  taken: number;
  skipped: number;
  /** agent: what the apply engine would do per container */
  runtime?: { containerSlug: string; action: string }[];
  warnings: string[];
}
export interface CommitDetail {
  commit: CommitView;
  /** items vs its parent (two-way) */
  changes: MergeItem[];
}
/**
 * §21 #105 POST /v1/templates/{id}/commits · POST /v1/deployments/{id}/commits — the ONLY persisting action of the
 * cloud config editors. The browser sends the full resulting config (the base head + its pending changes); the
 * server writes a new commit directly (no working copy). Optimistic concurrency: `baseCommitId` must equal the current
 * head (null = the branch has no commit yet), else 409 `stale` { reason: 'head-moved', head } and the GUI rebases.
 */
export interface CommitWorkingRequest {
  message: string;
  /** the head commit the change set was made on */
  baseCommitId: Ulid | null;
  /**
   * The resulting config. Secrets are SecretDefInput: plainValue ABSENT = keep the base head's value, null = clear,
   * string = set; generateNow = a new value from the generate rule (templates hold no values: plainValue refused).
   */
  config: ConfigInput;
  /**
   * §21 #107: the commit stage holding the NEW blobs of `config` (staging.ts). Required when the config references a
   * blob the base head does not and the store does not have yet (else 422 invalid V_BLOB_MISSING, details.missing).
   * The server promotes the staged blobs into the store before the commit becomes visible and deletes the stage
   * afterwards — also when the commit fails.
   */
  stageId?: Ulid;
}
/** @deprecated §21 #105 — the discard endpoints answer 410 gone (pending changes are discarded in the browser) */
export interface DiscardWorkingRequest {
  ifVersion?: number;
  ifRevision?: number;
}
export interface RevertRequest {
  branch: BranchRef;
  commitId: Ulid;
  message: string;
  ifHead: Ulid | null;
  choices?: MergeChoice[];
}
export interface ResetRequest {
  branch: BranchRef;
  commitId: Ulid;
  message: string;
  ifHead: Ulid | null;
}
export interface CompareRequest {
  source: BranchPoint;
  target: BranchRef | BranchPoint;
  /** compare two points (read only): no target head / working rules */
  diffOnly?: boolean;
}
/** "Create from" on POST /v1/templates and POST /v1/deployments */
export interface CreateFrom {
  from?: BranchPoint | null;
  message?: string;
  /** deployment ← deployment / agent: copy secret values without a generate rule (needs deployment.secrets.read on the source) */
  copySecretValues?: boolean;
}

// ── cloud mirror of agent branches
export interface AgentBranchUncommitted {
  /** §21 #105: always 0 from agent 0.14.1 (GUI edits live in the browser); 0.14.0 agents may still report saved edits */
  working: number;
  drift: number;
  notes: number;
  scannedAt: Ts | null;
}
export interface AgentBranchDoc {
  siteSlug: Slug;
  head: CommitRef | null;
  legacy: boolean;
  mergedFrom: { commitId: Ulid; seq: number } | null;
  uncommitted: AgentBranchUncommitted | null;
  pushedAt: Ts | null;
  agentVersion: string | null;
}
export interface AgentBranchView extends AgentBranchDoc {
  behind: number;
  ahead: number;
  differs: number;
  needsAgent014: boolean;
}
/** CloudView.branch (agents ≥ 0.14) */
export interface CloudBranchView {
  head: (CommitRef & { catalogHash: Sha256 | null }) | null;
  recent: CommitRef[];
}
/** HeartbeatRequest.branch / PeerPing.branch */
export interface AgentBranchReport {
  head: CommitRef | null;
  mergedFrom: { commitId: Ulid; seq: number } | null;
  uncommitted: AgentBranchUncommitted;
}
export interface PushAgentBranchRequest {
  commits: Commit[];
}
export interface PushAgentBranchResponse {
  stored: Ulid[];
  head: CommitRef | null;
}

// ── drift (agent)
export type DriftOrigin = 'docker' | 'file' | 'secret-file' | 'volume-content' | 'edit';
export interface DriftItem extends MergeItem {
  origin: DriftOrigin;
  detail: string;
  observedAt: Ts;
}
export type DriftNoteAction = 'discard' | 'accept' | 'take-bytes';
export interface DriftNote {
  fingerprint: string;
  key: string | null;
  label: string;
  detail: string;
  actions: DriftNoteAction[];
}
export interface DriftReport {
  scannedAt: Ts;
  headCommitId: Ulid | null;
  items: DriftItem[];
  notes: DriftNote[];
  localImages: string[];
}
export interface UncommittedResponse {
  /** GUI edits (origin "edit") + drift items, one row per key (an edited + drifted key shows once, origin edit) */
  items: DriftItem[];
  notes: DriftNote[];
  scannedAt: Ts | null;
  headCommitId: Ulid | null;
}
export interface AgentBranchState {
  siteSlug: Slug;
  cloud: { head: (CommitRef & { catalogHash: Sha256 | null }) | null; recent: CommitRef[]; staged: Record<Slug, SiteStaging> };
  local: { head: CommitRef | null; commits: CommitSummary[]; mergedFrom: { commitId: Ulid; seq: number } | null; ahead: number; behind: number; differs: number };
  uncommitted: AgentBranchUncommitted;
  /** a merge / commit-with-apply / fast-forward running on this site */
  run: UpdateRun | null;
  /** every site (Fast-forward all; `*` views) */
  sites: { siteSlug: Slug; reachable: boolean; agentVersion: string | null; head: CommitRef | null; mergedFrom: { commitId: Ulid; seq: number } | null; uncommitted: AgentBranchUncommitted | null; strictlyBehind: boolean; canFastForward: boolean; reason: string | null }[];
}
/**
 * POST /_opm/api/branch/commit. Two modes (§21 #105), never both in one request:
 *  - GUI EDITS: `config` (+ `baseCommitId`) = the edited config from the browser (local-edit rules apply). The agent
 *    writes the commit only after the existing apply engine succeeded on this site (staging, health, rollback on
 *    failure: then NO commit is written and the browser keeps the changes). Refused with 409 conflict
 *    { reason: 'uncommitted-drift', drift, notes } while changes made outside the agent are uncommitted (commit or
 *    discard them first), 409 stale { reason: 'head-moved', head } when the head is not `baseCommitId`.
 *  - DRIFT: `keys` / `accept` / `takeBytes` = record changes made outside the agent (no apply: they are live already).
 */
export interface LocalCommitRequest {
  message: string;
  /** GUI edits mode: the full edited config (secrets: plainValue absent = keep the head value) */
  config?: ConfigInput;
  /** GUI edits mode: the head the browser edited (required with `config`) */
  baseCommitId?: Ulid | null;
  /** GUI edits mode, §21 #107: the local stage with the new blobs (POST /_opm/api/stages); promoted before the apply */
  stageId?: Ulid;
  /** drift mode: uncommitted drift keys to commit (default all) */
  keys?: string[];
  /** drift note fingerprints accepted with this commit */
  accept?: string[];
  /** drift notes of edited rendered files: take the edited bytes as new File content (render none) */
  takeBytes?: string[];
}
export interface LocalCommitResponse {
  commit: Commit | null; // set when nothing had to be applied (drift only)
  run: UpdateRun | null; //  set when an apply runs (edits); the commit is written at COMMIT
}
export interface LocalDiscardRequest {
  keys: string[];
  notes?: string[];
}
export interface LocalCompareRequest {
  source: 'cloud' | { cloudCommitId: Ulid } | { localCommitId: Ulid };
  diffOnly?: boolean;
}
export interface LocalMergeRequest {
  sourceCommitId: Ulid;
  targetHead: Ulid | null;
  choices: MergeChoice[];
  values?: Record<string, VariableValue>;
  message?: string; // required by mergeLocal, ignored by the preview
}
export interface FastForwardAllRequest {
  sourceCommitId: Ulid;
  sites: Slug[];
  message: string;
}
export interface LocalRevertRequest {
  commitId: Ulid;
  message: string;
  targetHead: Ulid | null;
  choices?: MergeChoice[];
}

// ───────────────────────────────────────────────────────────────────────────── keys + labels
/** "template:<id>" | "deployment:<id>" | "agent:<deploymentId>/<site>" */
export function branchKey(b: BranchRef): string {
  if (b.kind === 'template') return `template:${b.templateId}`;
  if (b.kind === 'deployment') return `deployment:${b.deploymentId}`;
  return `agent:${b.deploymentId}/${b.siteSlug}`;
}
export function parseBranchKey(s: string): BranchRef | null {
  const m = /^(template|deployment|agent):([^/\s]+)(?:\/([a-z][a-z0-9-]{1,39}))?$/.exec(String(s ?? '').trim());
  if (!m) return null;
  if (m[1] === 'template' && !m[3]) return { kind: 'template', templateId: m[2] };
  if (m[1] === 'deployment' && !m[3]) return { kind: 'deployment', deploymentId: m[2] };
  if (m[1] === 'agent' && m[3]) return { kind: 'agent', deploymentId: m[2], siteSlug: m[3] };
  return null;
}
/** Firestore doc id of a branch key (no "/" allowed): "/" → "~" */
export const branchKeyDocId = (key: string) => key.replace(/\//g, '~');
export const sameBranch = (a: BranchRef, b: BranchRef) => branchKey(a) === branchKey(b);
/** "Single@12" style label of a commit point */
export const pointLabel = (label: string, seq: number | null | undefined) => (seq === null || seq === undefined ? label : `${label}@${seq}`);
/** Parse "<branchKey>[@<commitId>]" (frontend ?source= query) */
export function parsePointParam(s: string): BranchPoint | null {
  const at = String(s ?? '').lastIndexOf('@');
  const key = at > 0 ? s.slice(0, at) : s;
  const branch = parseBranchKey(key);
  if (!branch) return null;
  const rest = at > 0 ? s.slice(at + 1) : '';
  if (!rest || rest === 'head') return { branch, at: 'head' };
  if (rest === 'working') return { branch, at: 'working' };
  return { branch, at: { commitId: rest } };
}
export const pointParam = (p: BranchPoint) => branchKey(p.branch) + (p.at === 'head' ? '' : p.at === 'working' ? '@working' : `@${p.at.commitId}`);

// ───────────────────────────────────────────────────────────────────────────── messages
/** The first line of a commit message (the summary). */
export const commitSummary = (message: string) => String(message ?? '').trim().split(/\r?\n/)[0].trim();
/** Commit message rules: required, 1–2000 chars, first line 1–200 (V_COMMIT_MESSAGE). */
export function checkCommitMessage(message: unknown, path = 'message'): ValidationIssue[] {
  if (typeof message !== 'string' || !message.trim()) return [{ code: 'V_COMMIT_MESSAGE', path, message: 'a commit message is required', severity: 'error' } as ValidationIssue];
  const m = message.trim();
  if (m.length > COMMIT_MESSAGE_MAX) return [{ code: 'V_COMMIT_MESSAGE', path, message: `the commit message is ${m.length} characters (max ${COMMIT_MESSAGE_MAX})`, severity: 'error' } as ValidationIssue];
  const first = commitSummary(m);
  if (!first) return [{ code: 'V_COMMIT_MESSAGE', path, message: 'the first line (the summary) must not be empty', severity: 'error' } as ValidationIssue];
  if (first.length > COMMIT_SUMMARY_MAX) return [{ code: 'V_COMMIT_MESSAGE', path, message: `the first line is ${first.length} characters (max ${COMMIT_SUMMARY_MAX})`, severity: 'error' } as ValidationIssue];
  return [];
}

// ───────────────────────────────────────────────────────────────────────────── hashing
export const sha256HexOf = (s: string) => toHex(sha256(utf8(s)));
/** "sha256:" + hex(sha256(JCS(config))) — the commit hash (= the revision hash of the same config). */
export const configHash = (config: Config): Sha256 => `sha256:${sha256HexOf(canonicalize(config))}`;
export const catalogHashOf = (catalog: RevisionCatalog): Sha256 => `sha256:${sha256HexOf(canonicalize(catalog))}`;
/** what a secret value is compared / stored as in items and merge bases (never the plain value) */
export const secretValueMarker = (v: unknown): string | null => (v === null || v === undefined || v === '' ? null : `sha256:${sha256HexOf(String(v))}`);
/** a config with every secret value replaced by its marker (merge bases, previews) */
export function markSecretValues(c: Config): Config {
  return { ...c, secrets: c.secrets.map((s) => ({ ...s, value: secretValueMarker(s.value) })) };
}

function canon(v: unknown): string {
  try {
    return canonicalize(v);
  } catch {
    return JSON.stringify(v) ?? 'undefined';
  }
}
/** Stable short hash (cyrb53, 16 hex) of an item value; "-" = absent. Same function as templateSyncHash (#94). */
export function itemHash(s: Slot | unknown): string {
  const x = s && typeof s === 'object' && 'has' in (s as object) && 'v' in (s as object) ? (s as Slot) : slot(s);
  if (!x.has) return '-';
  const str = canon(x.v);
  let h1 = 0xdeadbeef;
  let h2 = 0x41c6ce57;
  for (let i = 0; i < str.length; i++) {
    const ch = str.charCodeAt(i);
    h1 = Math.imul(h1 ^ ch, 2654435761);
    h2 = Math.imul(h2 ^ ch, 1597334677);
  }
  h1 = Math.imul(h1 ^ (h1 >>> 16), 2246822507) ^ Math.imul(h2 ^ (h2 >>> 13), 3266489909);
  h2 = Math.imul(h2 ^ (h2 >>> 16), 2246822507) ^ Math.imul(h1 ^ (h1 >>> 13), 3266489909);
  return (h2 >>> 0).toString(16).padStart(8, '0') + (h1 >>> 0).toString(16).padStart(8, '0');
}

// ───────────────────────────────────────────────────────────────────────────── slots
export interface Slot {
  has: boolean;
  v: unknown;
}
const NONE: Slot = { has: false, v: null };
const slot = (v: unknown): Slot => (v === undefined ? NONE : { has: true, v });
const clone = <T>(v: T): T => (v === undefined ? v : (JSON.parse(JSON.stringify(v)) as T));
const same = (a: Slot, b: Slot) => a.has === b.has && (!a.has || canon(a.v) === canon(b.v));
const isScalar = (v: unknown) => v === null || typeof v === 'string' || typeof v === 'number' || typeof v === 'boolean';

type AnyConfig = Config | ConfigView;
export interface ItemizeOptions {
  /** agent branch: only the shared sections + this site (other sites are reference copies, §21 #104 D6) */
  scope?: Slug | null;
  /** compare secret values (both sides must hold values: deployment / agent); default: auto (both configs have a non-null value somewhere or are plain) */
  secretValues?: boolean;
}

// ───────────────────────────────────────────────────────────────────────────── units
interface Unit {
  key: string;
  section: MergeSection;
  siteSlug: Slug | null;
  label: string;
  b: Slot;
  t: Slot; // source
  o: Slot; // target
  /** write the chosen slot into cfg (the target being built); src = the source config (secret values) */
  set: (cfg: Config, s: Slot, src: AnyConfig) => void;
  /** keys this unit refers to in the SOURCE value (for requires) */
  refs?: (src: AnyConfig) => string[];
  blob?: (s: Slot) => Sha256 | null;
  blobMeta?: { volumeSlug?: string; path?: string; fileSlug?: string };
  note?: string;
  secretValue?: boolean;
}

const varDef = (v: Variable) => {
  const { value, ...def } = v;
  void value;
  return def;
};
const secretDef = (s: SecretDef | SecretDefView) => ({ secretSlug: s.secretSlug, label: s.label, description: s.description, required: s.required, generate: s.generate ?? null });

function upsert<T>(list: T[], keyOf: (x: T) => string, key: string, value: T | undefined, order: string[]) {
  const i = list.findIndex((x) => keyOf(x) === key);
  if (value === undefined) {
    if (i >= 0) list.splice(i, 1);
    return;
  }
  if (i >= 0) {
    list[i] = value;
    return;
  }
  const at = order.indexOf(key);
  if (at >= 0) {
    for (let k = at - 1; k >= 0; k--) {
      const j = list.findIndex((x) => keyOf(x) === order[k]);
      if (j >= 0) return void list.splice(j + 1, 0, value);
    }
    for (let k = at + 1; k < order.length; k++) {
      const j = list.findIndex((x) => keyOf(x) === order[k]);
      if (j >= 0) return void list.splice(j, 0, value);
    }
  }
  list.push(value);
}
const byKey = <T>(list: readonly T[] | undefined, keyOf: (x: T) => string) => {
  const m = new Map<string, T>();
  for (const x of list ?? []) m.set(keyOf(x), x);
  return m;
};
const keysOf = <T>(lists: (readonly T[] | undefined)[], keyOf: (x: T) => string) => {
  const out: string[] = [];
  const seen = new Set<string>();
  for (const l of lists)
    for (const x of l ?? []) {
      const k = keyOf(x);
      if (!seen.has(k)) {
        seen.add(k);
        out.push(k);
      }
    }
  return out;
};
const siteOf = (cfg: AnyConfig, slug: string) => cfg.sites.find((s) => s.siteSlug === slug);
const containerOf = (cfg: AnyConfig, site: string, c: string) => siteOf(cfg, site)?.config.containers.find((x) => x.containerSlug === c);
const volumeOf = (cfg: AnyConfig, site: string, v: string) => siteOf(cfg, site)?.config.volumes?.find((x) => x.volumeSlug === v);

export const SITE_SETTINGS = ['label', 'host', 'agentPort', 'agentGuiAliasPort', 'notFoundPageId', 'depends'] as const;
export const EDGE_FIELDS = ['httpPort', 'httpsPort', 'maxBodyBytes'] as const;
export const CONTAINER_FIELDS = ['image', 'enabled', 'command', 'entrypoint', 'restart', 'resources', 'healthcheck', 'depends', 'ports', 'volumes'] as const;
type ContainerField = (typeof CONTAINER_FIELDS)[number];
const containerField = (c: Container | undefined, f: ContainerField): Slot => {
  if (!c) return NONE;
  if (f === 'image') return slot({ serviceSlug: c.serviceSlug, version: c.version });
  return slot(c[f] ?? null);
};
/** the volume definition without its own content (tree / single seeds → a marker; content entries are separate items) */
const volumeDef = (v: SiteVolume | undefined): unknown => {
  if (!v) return undefined;
  const seed = v.seed as SiteVolume['seed'] | undefined;
  if (seed && (seed.kind === 'tree' || seed.kind === 'single')) return { ...v, seed: { kind: seed.kind } };
  return v;
};
/** §21 #117 a persist-to-cloud volume with seed null as the same volume with an empty tree */
function persistTree(v: SiteVolume | undefined): SiteVolume | undefined {
  return v && v.persist && !v.seed ? { ...v, seed: { kind: 'tree', entries: [], dirs: [] } } : v;
}
/** the tree seed of v to write content items into; a persist volume without content gets an empty tree */
function contentTreeOf(v: SiteVolume | undefined): Extract<VolumeSeed, { kind: 'tree' }> | null {
  if (!v) return null;
  if (!v.seed && v.persist) v.seed = { kind: 'tree', entries: [], dirs: [] };
  return v.seed?.kind === 'tree' ? v.seed : null;
}
const isContentSeed = (v: SiteVolume | undefined) => !!v?.seed && (v.seed.kind === 'tree' || v.seed.kind === 'single');

// references inside expressions / mounts → item keys (requires)
const EXPR_REF = /\{\{[^}]*?\b(var|secret|file)\.([A-Za-z0-9_.-]+)/g;
function exprRefs(v: unknown): string[] {
  const out: string[] = [];
  const s = typeof v === 'string' ? v : v && typeof v === 'object' ? canon(v) : '';
  for (const m of s.matchAll(EXPR_REF)) {
    const slug = m[2].replace(/\.+$/, '').split('.')[0];
    if (m[1] === 'var') out.push(`variables/${slug}`);
    else if (m[1] === 'secret') out.push(`secrets/${slug}`);
    else out.push(`files/${slug}`);
  }
  return out;
}
function mountRefs(ss: string, mounts: Container['volumes'] | undefined): string[] {
  const out: string[] = [];
  for (const m of mounts ?? []) {
    const src = (m as { source?: { kind: string; volumeSlug?: string; fileSlug?: string; secretSlug?: string } }).source;
    if (!src) continue;
    if (src.kind === 'volume' && src.volumeSlug) out.push(`sites/${ss}/volumes/${src.volumeSlug}`);
    if (src.kind === 'file' && src.fileSlug) out.push(`files/${src.fileSlug}`);
    if (src.kind === 'secret' && src.secretSlug) out.push(`secrets/${src.secretSlug}`);
    out.push(...exprRefs((m as { mountPath?: unknown }).mountPath));
  }
  return out;
}
function containerRefs(ss: string, c: Container | undefined): string[] {
  if (!c) return [];
  const out = [...mountRefs(ss, c.volumes)];
  for (const d of c.depends ?? []) out.push(`sites/${ss}/containers/${(d as { containerSlug: string }).containerSlug}`);
  for (const e of c.env ?? []) {
    if (e.secretRef) out.push(`secrets/${e.secretRef}`);
    out.push(...exprRefs(e.value));
  }
  out.push(...exprRefs(c.command), ...exprRefs(c.entrypoint));
  return out;
}

function buildUnits(base: AnyConfig | null, theirs: AnyConfig, ours: AnyConfig, opts: ItemizeOptions): Unit[] {
  const B: AnyConfig = base ?? { variables: [], secrets: [], files: [], sites: [] };
  const units: Unit[] = [];
  const push = (u: Unit) => units.push(u);
  const secretValues = opts.secretValues ?? false;

  // variables: definition + value
  {
    const bm = byKey(B.variables, (x) => x.variableSlug);
    const tm = byKey(theirs.variables, (x) => x.variableSlug);
    const om = byKey(ours.variables, (x) => x.variableSlug);
    const order = theirs.variables.map((x) => x.variableSlug);
    for (const slug of keysOf([B.variables, theirs.variables, ours.variables], (x) => x.variableSlug)) {
      const bv = bm.get(slug);
      const tv = tm.get(slug);
      const ov = om.get(slug);
      push({
        key: `variables/${slug}`,
        section: 'variables',
        siteSlug: null,
        label: `Variable ${slug}`,
        b: slot(bv && varDef(bv)),
        t: slot(tv && varDef(tv)),
        o: slot(ov && varDef(ov)),
        set: (cfg, s) => {
          const cur = cfg.variables.find((x) => x.variableSlug === slug);
          // a taken definition keeps the target value; a NEW variable starts with the source value
          const next = s.has ? ({ ...clone(s.v as Omit<Variable, 'value'>), value: cur ? cur.value : clone(tv?.value ?? null) } as Variable) : undefined;
          upsert(cfg.variables, (x) => x.variableSlug, slug, next, order);
        },
      });
      // the value is its own item when the variable exists on both sides (a new / removed variable carries its value)
      if (tv && ov)
        push({
          key: `variables/${slug}/value`,
          section: 'variables',
          siteSlug: null,
          label: `Variable ${slug} — value`,
          b: bv ? slot(bv.value ?? null) : NONE,
          t: slot(tv.value ?? null),
          o: slot(ov.value ?? null),
          set: (cfg, s) => {
            const cur = cfg.variables.find((x) => x.variableSlug === slug);
            if (cur && s.has) cur.value = clone(s.v as VariableValue);
          },
        });
    }
  }
  // secrets: definitions (+ values when both sides hold them)
  {
    const bm = byKey(B.secrets as (SecretDef | SecretDefView)[], (x) => x.secretSlug);
    const tm = byKey(theirs.secrets as (SecretDef | SecretDefView)[], (x) => x.secretSlug);
    const om = byKey(ours.secrets as (SecretDef | SecretDefView)[], (x) => x.secretSlug);
    const order = theirs.secrets.map((x) => x.secretSlug);
    for (const slug of keysOf<SecretDef | SecretDefView>([B.secrets, theirs.secrets, ours.secrets], (x) => x.secretSlug)) {
      const b = bm.get(slug);
      const t = tm.get(slug);
      const o = om.get(slug);
      push({
        key: `secrets/${slug}`,
        section: 'secrets',
        siteSlug: null,
        label: `Secret ${slug}`,
        b: slot(b && secretDef(b)),
        t: slot(t && secretDef(t)),
        o: slot(o && secretDef(o)),
        set: (cfg, s, src) => {
          const cur = cfg.secrets.find((x) => x.secretSlug === slug);
          const srcSecret = (src.secrets as SecretDef[]).find((x) => x.secretSlug === slug);
          // a taken definition keeps the target value; a NEW secret takes the source value when values are compared
          // (deployment / agent), else starts without one (the cloud generates it when it has a generate rule)
          const value = cur ? cur.value : secretValues && srcSecret && typeof srcSecret.value === 'string' && !String(srcSecret.value).startsWith('sha256:') ? srcSecret.value : null;
          const rotatedAt = cur ? cur.rotatedAt : value !== null ? (srcSecret?.rotatedAt ?? null) : null;
          const next = s.has ? ({ ...clone(s.v as object), value, rotatedAt } as SecretDef) : undefined;
          upsert(cfg.secrets, (x) => x.secretSlug, slug, next, order);
        },
      });
      if (secretValues && t && o) {
        const mv = (x: SecretDef | SecretDefView | undefined): Slot => (!x ? NONE : slot(typeof x.value === 'string' && x.value.startsWith('sha256:') ? x.value : secretValueMarker(x.value)));
        push({
          key: `secrets/${slug}/value`,
          section: 'secrets',
          siteSlug: null,
          label: `Secret ${slug} — value`,
          b: b ? mv(b) : NONE,
          t: mv(t),
          o: mv(o),
          secretValue: true,
          set: (cfg, s, src) => {
            const cur = cfg.secrets.find((x) => x.secretSlug === slug);
            const srcSecret = (src.secrets as SecretDef[]).find((x) => x.secretSlug === slug);
            if (!cur || !s.has || !srcSecret) return;
            cur.value = srcSecret.value;
            cur.rotatedAt = srcSecret.rotatedAt ?? cur.rotatedAt ?? null;
          },
        });
      }
    }
  }
  // files: metadata + content ref
  {
    const bm = byKey(B.files, (x) => x.fileSlug);
    const tm = byKey(theirs.files, (x) => x.fileSlug);
    const om = byKey(ours.files, (x) => x.fileSlug);
    const order = theirs.files.map((x) => x.fileSlug);
    for (const slug of keysOf([B.files, theirs.files, ours.files], (x) => x.fileSlug))
      push({
        key: `files/${slug}`,
        section: 'files',
        siteSlug: null,
        label: `File ${slug}`,
        b: slot(bm.get(slug)),
        t: slot(tm.get(slug)),
        o: slot(om.get(slug)),
        set: (cfg, s) => upsert(cfg.files, (x) => x.fileSlug, slug, s.has ? clone(s.v as FileDef) : undefined, order),
        blob: (s) => (s.has ? ((s.v as FileDef).content?.sha256 ?? null) : null),
        blobMeta: { fileSlug: slug },
      });
  }
  // sites
  {
    const bm = byKey(B.sites, (x) => x.siteSlug);
    const tm = byKey(theirs.sites, (x) => x.siteSlug);
    const om = byKey(ours.sites, (x) => x.siteSlug);
    const order = theirs.sites.map((x) => x.siteSlug);
    for (const ss of keysOf([B.sites, theirs.sites, ours.sites], (x) => x.siteSlug)) {
      if (opts.scope && ss !== opts.scope) continue;
      const b = bm.get(ss);
      const t = tm.get(ss);
      const o = om.get(ss);
      if (!t || !o) {
        push({
          key: `sites/${ss}`,
          section: 'sites',
          siteSlug: ss,
          label: `Site ${ss}`,
          b: slot(b),
          t: slot(t),
          o: slot(o),
          set: (cfg, s) => upsert(cfg.sites, (x) => x.siteSlug, ss, s.has ? clone(s.v as Site) : undefined, order),
          refs: (src) => {
            const site = siteOf(src, ss);
            const out: string[] = [];
            for (const c of site?.config.containers ?? []) out.push(...containerRefs(ss, c).filter((k) => !k.startsWith(`sites/${ss}/`)));
            return out;
          },
        });
        continue;
      }
      siteUnits(ss, b, t, o, push);
    }
  }
  return units;
}

function siteUnits(ss: string, b: Site | undefined, t: Site, o: Site, push: (u: Unit) => void) {
  const pre = `sites/${ss}`;
  for (const f of SITE_SETTINGS) {
    const get = (s: Site | undefined): Slot => (!s ? NONE : f === 'label' ? slot(s.label) : slot((s.config as unknown as Record<string, unknown>)[f] ?? null));
    push({
      key: `${pre}/settings/${f}`,
      section: 'sites',
      siteSlug: ss,
      label: `${ss} · ${f}`,
      b: get(b),
      t: get(t),
      o: get(o),
      set: (cfg, s) => {
        const site = siteOf(cfg, ss) as Site | undefined;
        if (!site || !s.has) return;
        if (f === 'label') site.label = s.v as string;
        else (site.config as unknown as Record<string, unknown>)[f] = clone(s.v);
      },
      refs: f === 'notFoundPageId' || f === 'depends' ? undefined : (src) => exprRefs((siteOf(src, ss)?.config as unknown as Record<string, unknown> | undefined)?.[f]),
    });
  }
  for (const f of EDGE_FIELDS) {
    const get = (s: Site | undefined): Slot => (!s ? NONE : slot((s.config.edge as unknown as Record<string, unknown>)[f] ?? null));
    push({
      key: `${pre}/edge/${f}`,
      section: 'edge',
      siteSlug: ss,
      label: `${ss} · edge ${f}`,
      b: get(b),
      t: get(t),
      o: get(o),
      set: (cfg, s) => {
        const site = siteOf(cfg, ss) as Site | undefined;
        if (site && s.has) (site.config.edge as unknown as Record<string, unknown>)[f] = clone(s.v);
      },
    });
  }
  const keyed = <T>(lists: [T[] | undefined, T[], T[]], keyOf: (x: T) => string, section: MergeSection, seg: string, label: (k: string) => string, listOf: (site: Site) => T[], refs?: (x: T) => string[]) => {
    const [bl, tl, ol] = lists;
    const bm = byKey(bl, keyOf);
    const tm = byKey(tl, keyOf);
    const om = byKey(ol, keyOf);
    const order = tl.map(keyOf);
    for (const k of keysOf([bl, tl, ol], keyOf))
      push({
        key: `${pre}/${seg}/${k}`,
        section,
        siteSlug: ss,
        label: label(k),
        b: slot(bm.get(k)),
        t: slot(tm.get(k)),
        o: slot(om.get(k)),
        set: (cfg, s) => {
          const site = siteOf(cfg, ss) as Site | undefined;
          if (site) upsert(listOf(site), keyOf, k, s.has ? clone(s.v as T) : undefined, order);
        },
        refs: refs ? () => (tm.get(k) ? refs(tm.get(k) as T) : []) : undefined,
      });
  };
  keyed([b?.config.edge.hosts, t.config.edge.hosts, o.config.edge.hosts], (x) => x.hostname, 'edge', 'edge/hosts', (k) => `${ss} · edge host ${k}`, (s) => s.config.edge.hosts, (h) => {
    const tls = (h as { tls?: { certFileSlug?: string; keyFileSlug?: string; keySecretSlug?: string } }).tls;
    return [tls?.certFileSlug ? `files/${tls.certFileSlug}` : '', tls?.keyFileSlug ? `files/${tls.keyFileSlug}` : '', tls?.keySecretSlug ? `secrets/${tls.keySecretSlug}` : ''].filter(Boolean);
  });
  keyed([b?.config.edge.routes, t.config.edge.routes, o.config.edge.routes], (x) => x.routeId, 'edge', 'edge/routes', (k) => `${ss} · edge route ${k}`, (s) => s.config.edge.routes, (r) => {
    const out: string[] = [];
    const a = r.action as { type: string; containerSlug?: string };
    if (a?.type === 'proxy' && a.containerSlug) out.push(`${pre}/containers/${a.containerSlug}`);
    for (const h of (r as { hosts?: string[] }).hosts ?? []) out.push(`${pre}/edge/hosts/${h}`);
    return out;
  });
  // volumes: whole when one side lacks it; else definition (without own content) + content entries
  {
    const bl = b?.config.volumes;
    const tl = t.config.volumes ?? [];
    const ol = o.config.volumes ?? [];
    const bm = byKey(bl, (x) => x.volumeSlug);
    const tm = byKey(tl, (x) => x.volumeSlug);
    const om = byKey(ol, (x) => x.volumeSlug);
    const order = tl.map((x) => x.volumeSlug);
    for (const vs of keysOf([bl, tl, ol], (x) => x.volumeSlug)) {
      const bv = bm.get(vs);
      const tv = tm.get(vs);
      const ov = om.get(vs);
      const vpre = `${pre}/volumes/${vs}`;
      if (!tv || !ov) {
        push({
          key: vpre,
          section: 'volumes',
          siteSlug: ss,
          label: `${ss} · volume ${vs}`,
          b: slot(bv),
          t: slot(tv),
          o: slot(ov),
          set: (cfg, s) => {
            const site = siteOf(cfg, ss) as Site | undefined;
            if (site) upsert((site.config.volumes ??= []), (x) => x.volumeSlug, vs, s.has ? clone(s.v as SiteVolume) : undefined, order);
          },
          note: !tv ? 'the data folder is kept on the server (orphan); only the definition is removed' : undefined,
        });
        continue;
      }
      // §21 #117 a persist volume without content (seed null) is an empty tree: its files are content items
      const [pbv, ptv, pov] = [persistTree(bv), persistTree(tv)!, persistTree(ov)!];
      push({
        key: vpre,
        section: 'volumes',
        siteSlug: ss,
        label: `${ss} · volume ${vs}`,
        b: slot(volumeDef(pbv)),
        t: slot(volumeDef(ptv)),
        o: slot(volumeDef(pov)),
        set: (cfg, s) => {
          const site = siteOf(cfg, ss) as Site | undefined;
          if (!site || !s.has) return;
          const cur = (site.config.volumes ?? []).find((x) => x.volumeSlug === vs);
          const next = clone(ptv) as SiteVolume;
          const ns = next.seed as SiteVolume['seed'] | undefined;
          const cs = cur?.seed as SiteVolume['seed'] | undefined;
          // keep the target's own content entries (content items move them one by one); a target without a tree has
          // no content items: it takes the source's tree whole
          if (ns?.kind === 'tree' && cs?.kind === 'tree') (next as { seed: unknown }).seed = { ...ns, entries: clone(cs.entries ?? []), dirs: clone(cs.dirs ?? []) };
          else if (ns?.kind === 'single' && cs?.kind === 'single') (next as { seed: unknown }).seed = { ...ns, sha256: cs.sha256, size: cs.size, mode: cs.mode, render: cs.render };
          upsert((site.config.volumes ??= []), (x) => x.volumeSlug, vs, next, order);
        },
      });
      if (isContentSeed(ptv) && isContentSeed(pov) && ptv.seed!.kind === pov.seed!.kind) contentUnits(ss, vs, pbv, ptv, pov, push);
    }
  }
  keyed([b?.config.devTools, t.config.devTools ?? [], o.config.devTools ?? []], (x) => x.presetSlug, 'devTools', 'devtools', (k) => `${ss} · dev tool ${k}`, (s) => (s.config.devTools ??= []), (p) => exprRefs(p));
  // containers
  const bm = byKey(b?.config.containers, (x) => x.containerSlug);
  const tm = byKey(t.config.containers, (x) => x.containerSlug);
  const om = byKey(o.config.containers, (x) => x.containerSlug);
  const order = t.config.containers.map((x) => x.containerSlug);
  for (const cs of keysOf([b?.config.containers, t.config.containers, o.config.containers], (x) => x.containerSlug)) {
    const bc = bm.get(cs);
    const tc = tm.get(cs);
    const oc = om.get(cs);
    const cpre = `${pre}/containers/${cs}`;
    if (!tc || !oc) {
      push({
        key: cpre,
        section: 'containers',
        siteSlug: ss,
        label: `${ss} · container ${cs}`,
        b: slot(bc),
        t: slot(tc),
        o: slot(oc),
        set: (cfg, s) => {
          const site = siteOf(cfg, ss) as Site | undefined;
          if (site) upsert(site.config.containers, (x) => x.containerSlug, cs, s.has ? clone(s.v as Container) : undefined, order);
        },
        refs: () => containerRefs(ss, tc),
        note: !tc ? 'the container is stopped and removed on the site; its data folders stay' : undefined,
      });
      continue;
    }
    for (const f of CONTAINER_FIELDS)
      push({
        key: `${cpre}/${f}`,
        section: 'containers',
        siteSlug: ss,
        label: `${ss} · ${cs} · ${f}`,
        b: containerField(bc, f),
        t: containerField(tc, f),
        o: containerField(oc, f),
        set: (cfg, s) => {
          const c = containerOf(cfg, ss, cs) as Container | undefined;
          if (!c || !s.has) return;
          if (f === 'image') {
            const v = s.v as { serviceSlug: string; version: string };
            c.serviceSlug = v.serviceSlug;
            c.version = v.version;
          } else (c as unknown as Record<string, unknown>)[f] = clone(s.v);
        },
        refs: f === 'volumes' ? () => mountRefs(ss, tc.volumes) : f === 'depends' ? () => (tc.depends ?? []).map((d) => `${pre}/containers/${(d as { containerSlug: string }).containerSlug}`) : f === 'command' || f === 'entrypoint' ? () => exprRefs(tc[f]) : undefined,
      });
    const envVal = (e: Container['env'][number] | undefined): Slot => (!e ? NONE : slot(e.secretRef ? { secretRef: e.secretRef } : (e.value ?? null)));
    const ebm = byKey(bc?.env, (x) => x.name);
    const etm = byKey(tc.env, (x) => x.name);
    const eom = byKey(oc.env, (x) => x.name);
    const eorder = tc.env.map((x) => x.name);
    for (const name of keysOf([bc?.env, tc.env, oc.env], (x) => x.name))
      push({
        key: `${cpre}/env/${name}`,
        section: 'containers',
        siteSlug: ss,
        label: `${ss} · ${cs} · env ${name}`,
        b: envVal(ebm.get(name)),
        t: envVal(etm.get(name)),
        o: envVal(eom.get(name)),
        set: (cfg, s) => {
          const c = containerOf(cfg, ss, cs) as Container | undefined;
          if (!c) return;
          const v = s.v as string | null | { secretRef: string };
          const row = !s.has ? undefined : v && typeof v === 'object' ? { name, value: null, secretRef: v.secretRef } : { name, value: v === null ? null : String(v), secretRef: null };
          upsert(c.env, (x) => x.name, name, row as Container['env'][number] | undefined, eorder);
        },
        refs: () => {
          const e = etm.get(name);
          return e ? [...(e.secretRef ? [`secrets/${e.secretRef}`] : []), ...exprRefs(e.value)] : [];
        },
      });
  }
}

export const CONTENT_NOTE_STARTER = 'starter content — written only into new / empty volumes';
export const CONTENT_NOTE_PERSIST = 'persist-to-cloud content — written into the volume on the site when merged';
/** §21 #101 starter content entries of a volume present on both sides with the same content kind. */
function contentUnits(ss: string, vs: string, bv: SiteVolume | undefined, tv: SiteVolume, ov: SiteVolume, push: (u: Unit) => void) {
  const vpre = `sites/${ss}/volumes/${vs}/content`;
  // §21 #117 a persist volume's content is committed content (written into the live volume on merge)
  const NOTE = tv.persist || ov.persist ? CONTENT_NOTE_PERSIST : CONTENT_NOTE_STARTER;
  const tseed = tv.seed!;
  if (tseed.kind === 'single') {
    const one = (v: SiteVolume | undefined): Slot => {
      const s = v?.seed;
      return s && s.kind === 'single' ? slot({ sha256: s.sha256, size: s.size, mode: s.mode, render: s.render }) : NONE;
    };
    push({
      key: vpre,
      section: 'volumes',
      siteSlug: ss,
      label: `${ss} · volume ${vs} · content`,
      b: one(bv),
      t: one(tv),
      o: one(ov),
      set: (cfg, s) => {
        const v = volumeOf(cfg, ss, vs) as SiteVolume | undefined;
        if (!v || !s.has || v.seed?.kind !== 'single') return;
        Object.assign(v.seed, clone(s.v as object));
      },
      blob: (s) => (s.has ? ((s.v as { sha256: string }).sha256 ?? null) : null),
      blobMeta: { volumeSlug: vs, path: '' },
      note: NOTE,
    });
    return;
  }
  const entries = (v: SiteVolume | undefined) => (v?.seed?.kind === 'tree' ? (v.seed.entries ?? []) : []);
  const dirs = (v: SiteVolume | undefined) => (v?.seed?.kind === 'tree' ? (v.seed.dirs ?? []) : []);
  const ebm = byKey(entries(bv), (e) => e.path);
  const etm = byKey(entries(tv), (e) => e.path);
  const eom = byKey(entries(ov), (e) => e.path);
  const sortEntries = (l: VolumeTreeEntry[]) => l.sort((a, b) => compareUtf16(a.path, b.path));
  for (const p of keysOf([entries(bv), entries(tv), entries(ov)], (e) => e.path)) {
    const ent = (e: VolumeTreeEntry | undefined): Slot => (!e ? NONE : slot({ sha256: e.sha256, size: e.size, mode: e.mode, render: e.render }));
    push({
      key: `${vpre}/${p}`,
      section: 'volumes',
      siteSlug: ss,
      label: `${ss} · volume ${vs} · ${p}`,
      b: ent(ebm.get(p)),
      t: ent(etm.get(p)),
      o: ent(eom.get(p)),
      set: (cfg, s) => {
        const v = volumeOf(cfg, ss, vs) as SiteVolume | undefined;
        const seed = contentTreeOf(v);
        if (!seed) return;
        const list = (seed.entries ??= []);
        const i = list.findIndex((e) => e.path === p);
        if (!s.has) {
          if (i >= 0) list.splice(i, 1);
          return;
        }
        const e = { path: p, ...clone(s.v as object) } as VolumeTreeEntry;
        if (i >= 0) list[i] = e;
        else list.push(e);
        sortEntries(list);
      },
      blob: (s) => (s.has ? ((s.v as { sha256: string }).sha256 ?? null) : null),
      blobMeta: { volumeSlug: vs, path: p },
      note: NOTE,
    });
  }
  const dset = (v: SiteVolume | undefined) => new Set(dirs(v));
  const bd = dset(bv);
  const td = dset(tv);
  const od = dset(ov);
  for (const d of keysOf([dirs(bv), dirs(tv), dirs(ov)], (x) => x)) {
    const ds = (set: Set<string>, has: boolean): Slot => (has && set.has(d) ? slot('empty folder') : NONE);
    push({
      key: `${vpre}/${d}/`,
      section: 'volumes',
      siteSlug: ss,
      label: `${ss} · volume ${vs} · ${d}/`,
      b: bv ? ds(bd, true) : NONE,
      t: ds(td, true),
      o: ds(od, true),
      set: (cfg, s) => {
        const v = volumeOf(cfg, ss, vs) as SiteVolume | undefined;
        const seed = contentTreeOf(v);
        if (!seed) return;
        const list = (seed.dirs ??= []);
        const i = list.indexOf(d);
        if (!s.has && i >= 0) list.splice(i, 1);
        if (s.has && i < 0) {
          list.push(d);
          list.sort(compareUtf16);
        }
      },
      note: NOTE,
    });
  }
}

// ───────────────────────────────────────────────────────────────────────────── classification
/**
 * The kind of one item (null = nothing to show: source and target are equal).
 * hasBase=false: two-way (add / remove / change); else 3-way (theirs / ours / conflict).
 */
export function classifyItem(b: Slot | null, t: Slot, o: Slot): Exclude<MergeKind, 'skipped'> | null {
  if (same(t, o)) return null;
  if (!b) {
    if (t.has && !o.has) return 'add';
    if (!t.has && o.has) return 'remove';
    return 'change';
  }
  const tb = same(t, b);
  const ob = same(o, b);
  if (tb) return 'ours';
  if (ob) return 'theirs';
  return 'conflict';
}
const effectOf = (t: Slot, o: Slot): MergeItem['effect'] => (t.has && !o.has ? 'add' : !t.has && o.has ? 'remove' : 'change');

const shown = (u: Unit, s: Slot): MaskedSlot => {
  if (!s.has) return { has: false, value: null };
  if (u.secretValue) return { has: true, value: s.v === null ? 'not set' : 'set' };
  if (u.section === 'secrets' && s.v && typeof s.v === 'object') return { has: true, value: s.v };
  if (u.section === 'devTools' && s.v && typeof s.v === 'object') return { has: true, value: maskDevToolLiterals(s.v as DevToolPreset) };
  if (u.key.startsWith('sites/') && !u.key.includes('/', 'sites/'.length) && s.v && typeof s.v === 'object') return { has: true, value: maskSiteDevTools(s.v as Site) };
  return { has: true, value: s.v };
};
const maskSiteDevTools = (s: Site): Site => (s.config?.devTools?.length ? { ...s, config: { ...s.config, devTools: s.config.devTools.map((p) => maskDevToolLiterals(p)) } } : s);

export interface PlanOptions extends ItemizeOptions {
  /** skip records of the merge base */
  skipped?: readonly MergeSkip[];
  /** pre-tick every item when the plan is a fast-forward (default true) */
  preselectFastForward?: boolean;
  /** pre-tick everything (reset to commit) */
  preselectAll?: boolean;
}

function toItem(u: Unit, kind: MergeKind, hasBase: boolean, skip?: MergeSkip): MergeItem {
  const it: MergeItem = {
    key: u.key,
    section: u.section,
    siteSlug: u.siteSlug,
    label: u.label,
    kind,
    effect: effectOf(u.t, u.o),
    base: hasBase ? shown(u, u.b) : null,
    target: shown(u, u.o),
    source: shown(u, u.t),
    preselect: false,
    scalar: !u.secretValue && (!u.t.has || isScalar(u.t.v)) && (!u.o.has || isScalar(u.o.v)) && (u.t.has || u.o.has),
    requires: [],
    locked: null,
    sourceHash: itemHash(u.t),
  };
  if (u.blob) it.blob = { base: hasBase ? u.blob(u.b) : null, target: u.blob(u.o), source: u.blob(u.t), ...(u.blobMeta ?? {}) };
  if (u.note && (it.effect === 'remove' || u.key.includes('/content'))) it.note = u.note;
  if (skip) it.skippedAt = skip.skippedAt;
  return it;
}

interface Planned {
  units: Unit[];
  items: MergeItem[];
  byKey: Map<string, Unit>;
}
function planUnits(base: AnyConfig | null, source: AnyConfig, target: AnyConfig, opts: PlanOptions = {}): Planned {
  const skips = new Map((opts.skipped ?? []).map((s) => [s.key, s]));
  const units = buildUnits(base, source, target, opts);
  const items: MergeItem[] = [];
  const byKeyU = new Map<string, Unit>();
  for (const u of units) {
    const k0 = classifyItem(base ? u.b : null, u.t, u.o);
    if (!k0) continue;
    let k: MergeKind = k0;
    const s = skips.get(u.key);
    // a source change NOT taken in an earlier merge: the base moved to the source, so it comes back as "ours"
    // (#94 rule); a theirs / conflict item with the same source value is the same skipped change
    if ((k0 === 'ours' || k0 === 'theirs' || k0 === 'conflict') && s && s.theirsHash === itemHash(u.t)) k = 'skipped';
    items.push(toItem(u, k, !!base, k === 'skipped' ? s : undefined));
    byKeyU.set(u.key, u);
  }
  // requires: only keys that are items of this plan and are not yet satisfied in the target
  const present = new Set(items.map((i) => i.key));
  for (const it of items) {
    const u = byKeyU.get(it.key)!;
    if (!u.refs || it.effect === 'remove') continue;
    const req = [...new Set(u.refs(source))].filter((k) => k !== it.key && present.has(k));
    // a reference needs the item only when the TARGET lacks the thing (taking it adds it)
    it.requires = req.filter((k) => {
      const r = items.find((x) => x.key === k)!;
      return r.effect === 'add';
    });
  }
  const ff = isFastForward(items);
  for (const it of items) {
    if (opts.preselectAll) it.preselect = true;
    else if (ff && opts.preselectFastForward !== false) it.preselect = true;
  }
  return { units, items, byKey: byKeyU };
}

/** every item is a clean source change (theirs) or a source-only addition: taking all = the source state */
export function isFastForward(items: readonly Pick<MergeItem, 'kind'>[]): boolean {
  return items.length > 0 && items.every((i) => i.kind === 'theirs' || i.kind === 'add');
}

/** The plan items of a compare (base null = two-way). */
export function planMerge(base: AnyConfig | null, source: AnyConfig, target: AnyConfig, opts: PlanOptions = {}): MergeItem[] {
  return planUnits(base, source, target, opts).items;
}
export function countMerge(items: readonly Pick<MergeItem, 'kind'>[]): Record<MergeKind, number> {
  const c = Object.fromEntries(MERGE_KINDS.map((k) => [k, 0])) as Record<MergeKind, number>;
  for (const i of items) c[i.kind]++;
  return c;
}
/** source changes = what "merge" (all) means: theirs + add + change + conflict + skipped (+ remove only in two-way) */
export const SOURCE_CHANGE_KINDS: readonly MergeKind[] = ['theirs', 'add', 'change', 'conflict', 'skipped'];
/** "Select all source changes" (never conflicts — they need a decision) */
export const cleanSourceChoices = (items: readonly MergeItem[]): MergeChoice[] => items.filter((i) => (i.kind === 'theirs' || i.kind === 'add' || i.kind === 'change' || i.kind === 'skipped') && !i.locked).map((i) => ({ key: i.key, take: 'source' as const }));
/** "Select all removals" */
export const removalChoices = (items: readonly MergeItem[]): MergeChoice[] => items.filter((i) => i.effect === 'remove' && !i.locked && i.kind !== 'conflict').map((i) => ({ key: i.key, take: 'source' as const }));

export interface ApplyMergeResult {
  config: Config;
  taken: string[];
  /** source changes NOT taken (theirs / conflict / skipped / add / change): recorded as skipped on the merge base */
  notTaken: MergeItem[];
  unresolved: string[];
  errors: { key: string; message: string }[];
  items: MergeItem[];
  kind: 'merge' | 'cherry-pick';
}

/**
 * Apply the chosen items to a copy of the TARGET (with its plain secret values). Items not taken keep the target's
 * value. `values` = variable values entered in the review (applied last). The SOURCE must hold plain secret values
 * when secret value items are taken.
 */
export function applyMerge(base: AnyConfig | null, source: AnyConfig, target: Config, choices: readonly MergeChoice[], opts: PlanOptions & { values?: Record<string, VariableValue> } = {}): ApplyMergeResult {
  const planned = planUnits(base, source, target, { ...opts, preselectFastForward: false });
  const cfg = clone(target);
  const chosen = new Map<string, MergeChoice>();
  const errors: { key: string; message: string }[] = [];
  for (const c of choices) {
    if (!c || typeof c.key !== 'string') continue;
    if (chosen.has(c.key)) errors.push({ key: c.key, message: 'chosen twice' });
    chosen.set(c.key, c);
  }
  const taken: string[] = [];
  const notTaken: MergeItem[] = [];
  const unresolved: string[] = [];
  for (const it of planned.items) {
    const u = planned.byKey.get(it.key)!;
    const choice = chosen.get(it.key);
    if (!choice || choice.take === 'target') {
      if (it.kind === 'conflict' && !choice) unresolved.push(it.key);
      if (SOURCE_CHANGE_KINDS.includes(it.kind)) notTaken.push(it);
      continue;
    }
    if (choice.take === 'source') u.set(cfg, u.t, source);
    else if (choice.take === 'custom') {
      if (!it.scalar || !isScalar(choice.value)) {
        errors.push({ key: it.key, message: 'a custom value is only possible for a scalar item (string, number, true / false, null)' });
        continue;
      }
      u.set(cfg, { has: true, v: choice.value }, source);
    } else {
      errors.push({ key: it.key, message: 'take must be source, target or custom' });
      continue;
    }
    taken.push(it.key);
  }
  const seen = new Set(planned.items.map((i) => i.key));
  for (const k of chosen.keys()) if (!seen.has(k)) errors.push({ key: k, message: 'no such change in the plan (a branch moved — refresh)' });
  for (const [slug, value] of Object.entries(opts.values ?? {})) {
    const v = cfg.variables.find((x) => x.variableSlug === slug);
    if (!v) errors.push({ key: `variables/${slug}/value`, message: `variable ${slug} is not in the result` });
    else v.value = clone(value);
  }
  for (const s of cfg.sites) if (s?.config && Array.isArray(s.config.devTools) && s.config.devTools.length === 0) delete s.config.devTools;
  const sourceChanges = planned.items.filter((i) => SOURCE_CHANGE_KINDS.includes(i.kind));
  const kind: ApplyMergeResult['kind'] = sourceChanges.every((i) => taken.includes(i.key)) ? 'merge' : 'cherry-pick';
  return { config: cfg, taken, notTaken, unresolved, errors, items: planned.items, kind };
}

/** New skip records after a merge: source changes not taken (kept from earlier merges when unchanged). */
export function nextSkips(notTaken: readonly MergeItem[], previous: readonly MergeSkip[], sourceSeq: number | null, now: string, by: string): MergeSkip[] {
  const prev = new Map(previous.map((s) => [s.key, s]));
  return notTaken
    .filter((c) => c.kind !== 'ours' && c.kind !== 'remove')
    .map((c) => {
      const p = prev.get(c.key);
      if (p && p.theirsHash === c.sourceHash) return p;
      return { key: c.key, theirsHash: c.sourceHash, sourceSeq, skippedAt: now, skippedBy: by };
    });
}

/** Required variables without a value and required secrets without a generate rule and value. */
export function mergeMissing(cfg: AnyConfig): MergeMissing {
  return {
    variables: cfg.variables
      .filter((v) => v.required && (v.value === null || v.value === '' || v.value === undefined))
      .map((v) => ({ variableSlug: v.variableSlug, label: v.label, type: v.type, options: v.options ?? null, required: v.required })),
    secrets: (cfg.secrets as (SecretDef | SecretDefView)[])
      .filter((s) => s.required && !s.generate && ('isSet' in s ? !s.isSet : s.value === null || s.value === '' || s.value === undefined))
      .map((s) => ({ secretSlug: s.secretSlug, label: s.label })),
  };
}

// ───────────────────────────────────────────────────────────────────────────── working copy / history helpers
/** Items between the head and the working copy (two-way: add / remove / change). */
export function uncommittedItems(head: AnyConfig | null, working: AnyConfig, opts: ItemizeOptions = {}): MergeItem[] {
  return planMerge(null, working, head ?? { variables: [], secrets: [], files: [], sites: [] }, { ...opts, preselectFastForward: false });
}
export const uncommittedCount = (head: AnyConfig | null, working: AnyConfig, opts: ItemizeOptions = {}) => uncommittedItems(head, working, opts).length;

/** Stats of a commit vs its parent (keys ≤ COMMIT_STATS_KEYS_MAX). */
export function commitStats(parent: AnyConfig | null, config: AnyConfig, opts: ItemizeOptions = {}): CommitStats {
  const items = uncommittedItems(parent, config, opts);
  let added = 0;
  let changed = 0;
  let removed = 0;
  for (const i of items) {
    if (i.effect === 'add') added++;
    else if (i.effect === 'remove') removed++;
    else changed++;
  }
  return { added, changed, removed, keys: items.slice(0, COMMIT_STATS_KEYS_MAX).map((i) => i.key), truncated: items.length > COMMIT_STATS_KEYS_MAX };
}

/** Revert of `commit` on a branch whose head config is `head`: base = commit, source = its parent, target = head. */
export function revertPlan(commitConfig: AnyConfig, parentConfig: AnyConfig | null, head: AnyConfig, opts: ItemizeOptions = {}): MergeItem[] {
  const parent = parentConfig ?? { variables: [], secrets: [], files: [], sites: [] };
  const items = planMerge(commitConfig, parent, head, { ...opts, preselectFastForward: false });
  for (const i of items) if (i.kind === 'theirs') i.preselect = true;
  // "ours" = a later commit changed something the reverted commit did not touch: nothing to undo
  return items.filter((i) => i.kind !== 'ours');
}
export function applyRevert(commitConfig: AnyConfig, parentConfig: AnyConfig | null, head: Config, choices: readonly MergeChoice[], opts: ItemizeOptions = {}): ApplyMergeResult {
  const parent = parentConfig ?? { variables: [], secrets: [], files: [], sites: [] };
  const all = planMerge(commitConfig, parent, head, opts);
  const picked = new Map(choices.map((c) => [c.key, c]));
  // clean undo items are taken unless the caller said otherwise; conflicts need a choice
  const eff: MergeChoice[] = [];
  for (const i of all) {
    if (i.kind === 'ours') continue;
    const c = picked.get(i.key);
    if (c) eff.push(c);
    else if (i.kind === 'theirs') eff.push({ key: i.key, take: 'source' });
  }
  return applyMerge(commitConfig, parent, head, eff, opts);
}
/** Reset to an older commit: two-way plan with every item taken. */
export function resetPlan(olderConfig: AnyConfig, head: AnyConfig, opts: ItemizeOptions = {}): MergeItem[] {
  return planMerge(null, olderConfig, head, { ...opts, preselectAll: true });
}

// ───────────────────────────────────────────────────────────────────────────── §21 #105 browser change sets
export interface RebaseResult {
  /** the new head + my changes (an unresolved conflict keeps the head's value) */
  config: Config;
  /** the rebase plan: theirs = my change (re-applied), ours = the new head's own change (kept), conflict = both */
  items: MergeItem[];
  /** conflicts without a choice (the GUI shows them: Keep the head · Take mine · Custom) */
  conflicts: MergeItem[];
  /** keys of my changes re-applied */
  taken: string[];
  /** no unresolved conflict and no bad choice: the rebase may run without asking */
  clean: boolean;
  errors: { key: string; message: string }[];
}
/**
 * Rebase a browser change set made on `base` (the old head) onto `head` (the new head) with the merge engine:
 * source = mine, target = the new head. My clean changes are re-applied, the head's own changes stay, conflicts
 * (both changed the same item differently) need a choice (`take: 'source'` = mine, `'target'` = the head's). Without
 * a base (the branch had no commit) every difference of mine is taken (two-way: add / change), head-only things stay.
 * Configs may be masked (secret values are carried separately by the GUI as SecretDefInput ops).
 */
export function rebaseChanges(base: AnyConfig | null, mine: AnyConfig, head: Config, choices: readonly MergeChoice[] = [], opts: ItemizeOptions = {}): RebaseResult {
  const items = planMerge(base, mine, head, { ...opts, preselectFastForward: false });
  const chosen = new Map(choices.map((c) => [c.key, c]));
  const eff: MergeChoice[] = [];
  for (const i of items) {
    const c = chosen.get(i.key);
    if (c) eff.push(c);
    else if (i.kind === 'theirs' || i.kind === 'skipped' || (base === null && (i.kind === 'add' || i.kind === 'change'))) eff.push({ key: i.key, take: 'source' });
  }
  const r = applyMerge(base, mine, head, eff, opts);
  return { config: r.config, items, conflicts: items.filter((i) => i.kind === 'conflict' && !chosen.has(i.key)), taken: r.taken, clean: !r.unresolved.length && !r.errors.length, errors: r.errors };
}
/**
 * §21 #105 secret value changes of a commit (slugs only — recorded in CommitData.secretValues, activities and audit,
 * never a value). `before` / `after` hold PLAIN values (server / agent side); `generated` = slugs given a generated value.
 */
export function secretValueChanges(before: readonly Pick<SecretDef, 'secretSlug' | 'value'>[], after: readonly Pick<SecretDef, 'secretSlug' | 'value'>[], generated: readonly Slug[] = []): SecretValueChanges {
  const empty = (v: unknown) => v === null || v === undefined || v === '';
  const gen = new Set(generated);
  const out: SecretValueChanges = { set: [], generated: [], cleared: [] };
  for (const a of after) {
    const b = before.find((x) => x.secretSlug === a.secretSlug);
    const bv = b?.value ?? null;
    if (empty(a.value)) {
      if (!empty(bv)) out.cleared.push(a.secretSlug);
    } else if (gen.has(a.secretSlug)) out.generated.push(a.secretSlug);
    else if (a.value !== bv) out.set.push(a.secretSlug);
  }
  for (const k of ['set', 'generated', 'cleared'] as const) out[k].sort(compareUtf16);
  return out;
}
export const hasSecretValueChanges = (c: SecretValueChanges | null | undefined) => !!c && c.set.length + c.generated.length + c.cleared.length > 0;

/** Scope helper: the agent branch config with OTHER sites replaced by the reference copies of `refs` (§21 #104 D6). */
export function withReferenceSites(own: Config, refs: Config, siteSlug: Slug): { config: Config; updatedSites: Slug[] } {
  const out = clone(own);
  const updated: Slug[] = [];
  const mine = out.sites.find((s) => s.siteSlug === siteSlug);
  const next: Site[] = [];
  for (const s of refs.sites) {
    if (s.siteSlug === siteSlug) {
      if (mine) next.push(mine);
      continue;
    }
    const prev = out.sites.find((x) => x.siteSlug === s.siteSlug);
    if (!prev || canon(prev) !== canon(s)) updated.push(s.siteSlug);
    next.push(clone(s));
  }
  if (mine && !next.includes(mine)) next.push(mine);
  for (const s of out.sites) if (s.siteSlug !== siteSlug && !refs.sites.some((x) => x.siteSlug === s.siteSlug)) updated.push(s.siteSlug);
  out.sites = next;
  return { config: out, updatedSites: updated };
}

/** A human summary of items ("3 changed · 1 added · 2 removed: web image, …") — the CommitDialog "Suggest" button. */
export function suggestMessage(items: readonly Pick<MergeItem, 'label' | 'effect'>[], prefix = ''): string {
  if (!items.length) return prefix.trim();
  const n = (e: MergeItem['effect']) => items.filter((i) => i.effect === e).length;
  const parts = [n('change') && `${n('change')} changed`, n('add') && `${n('add')} added`, n('remove') && `${n('remove')} removed`].filter(Boolean).join(' · ');
  const names = items.slice(0, 3).map((i) => i.label.replace(/^Variable |^Secret |^File /, '')).join(', ');
  const s = `${prefix}${parts}: ${names}${items.length > 3 ? ', …' : ''}`;
  return s.length > COMMIT_SUMMARY_MAX ? s.slice(0, COMMIT_SUMMARY_MAX - 1) + '…' : s;
}

// ───────────────────────────────────────────────────────────────────────────── agent versions
const verParts = (v: string) => String(v ?? '').replace(/^v/, '').split(/[-+]/)[0].split('.').map((x) => parseInt(x, 10) || 0);
/** agent ≥ 0.14.0 (pre-release / build suffixes ignored) */
export function supportsBranches(version: string | null | undefined): boolean {
  if (!version) return false;
  const [a, b, c] = verParts(version);
  const [x, y, z] = verParts(BRANCHES_MIN_AGENT);
  return a !== x ? a > x : b !== y ? b > y : c >= z;
}
export const needsAgentMessage = (siteSlug: string, version: string | null) =>
  `${siteSlug} runs agent ${version ?? 'unknown'} — needs agent ≥ ${BRANCHES_MIN_AGENT} for agent branches; it still receives this commit as a proposal (approve it in its agent GUI, whole commit only)`;

// ───────────────────────────────────────────────────────────────────────────── commits
export interface NewCommitInput {
  commitId: Ulid;
  branch: BranchRef;
  parent: Pick<Commit, 'commitId' | 'seq' | 'config'> | null;
  kind: CommitKind;
  message: string;
  author: CommitAuthor;
  createdAt: Ts;
  config: Config;
  catalog?: RevisionCatalog | null;
  source?: CommitSource | null;
  revertOf?: Commit['revertOf'];
  resetTo?: Commit['resetTo'];
  rollbackOf?: Commit['rollbackOf'];
  data?: CommitData;
  scope?: Slug | null;
}
/** Build a commit (hash, catalogHash, seq, stats). The caller checked the message (checkCommitMessage). */
export function makeCommit(i: NewCommitInput): Commit {
  const secretValues = i.branch.kind !== 'template';
  return {
    commitId: i.commitId,
    branch: i.branch,
    seq: (i.parent?.seq ?? 0) + 1,
    parentId: i.parent?.commitId ?? null,
    kind: i.kind,
    message: i.message.trim(),
    author: i.author,
    createdAt: i.createdAt,
    source: i.source ?? null,
    revertOf: i.revertOf ?? null,
    resetTo: i.resetTo ?? null,
    rollbackOf: i.rollbackOf ?? null,
    config: i.config,
    hash: configHash(i.config),
    catalog: i.catalog ?? null,
    catalogHash: i.catalog ? catalogHashOf(i.catalog) : null,
    stats: commitStats(i.parent?.config ?? null, i.config, { secretValues, scope: i.scope ?? null }),
    data: i.data ?? {},
  };
}
export const commitRef = (c: Pick<Commit, 'commitId' | 'seq' | 'hash' | 'message' | 'author' | 'createdAt' | 'kind'>): CommitRef => ({ commitId: c.commitId, seq: c.seq, hash: c.hash, message: c.message, author: c.author, createdAt: c.createdAt, kind: c.kind });
export const commitSummaryOf = (c: Commit | CommitView): CommitSummary => ({
  commitId: c.commitId,
  branch: c.branch,
  seq: c.seq,
  parentId: c.parentId,
  kind: c.kind,
  message: c.message,
  author: c.author,
  createdAt: c.createdAt,
  source: c.source,
  revertOf: c.revertOf,
  resetTo: c.resetTo,
  rollbackOf: c.rollbackOf,
  hash: c.hash,
  stats: c.stats,
});
/** CommitSource.picks capped at COMMIT_PICKS_MAX */
export function sourcePicks(taken: readonly string[], kind: 'merge' | 'cherry-pick'): Pick<CommitSource, 'picks' | 'truncated'> {
  if (kind === 'merge') return { picks: 'all' };
  return taken.length > COMMIT_PICKS_MAX ? { picks: taken.slice(0, COMMIT_PICKS_MAX), truncated: true } : { picks: [...taken] };
}

/** Verify a pushed / stored commit chain link (hash, seq, parent). */
export function verifyCommitLink(c: Pick<Commit, 'commitId' | 'seq' | 'parentId' | 'config' | 'hash' | 'catalog' | 'catalogHash'>, parent: Pick<Commit, 'commitId' | 'seq'> | null): string | null {
  if (configHash(c.config) !== c.hash) return `commit ${c.commitId}: hash does not match its config`;
  if (c.catalog && c.catalogHash && catalogHashOf(c.catalog) !== c.catalogHash) return `commit ${c.commitId}: catalogHash does not match its catalog`;
  if (parent) {
    if (c.parentId !== parent.commitId) return `commit ${c.commitId}: parent ${c.parentId} is not ${parent.commitId}`;
    if (c.seq !== parent.seq + 1) return `commit ${c.commitId}: seq ${c.seq} is not ${parent.seq + 1}`;
  }
  return null;
}

/** Mask a commit for human reads (secret values → MASK / isSet). */
export function maskCommit(c: Commit, maskConfig: (cfg: Config) => ConfigView): CommitView {
  return { ...c, config: maskConfig(c.config) };
}

// ───────────────────────────────────────────────────────────────────────────── API tables
/** Cloud endpoints (spread into CLOUD_API). */
export const BRANCH_CLOUD_API = {
  listTemplateCommits: ep<void, PageResponse<CommitSummary>, { cursor?: string; limit?: number }>()({ method: 'GET', path: '/v1/templates/{templateId}/commits', auth: 'human', perm: 'templates.read', body: 'none', res: 'json', errors: ['not-found'], summary: '§21 #104 commits of a template, newest first.' }),
  getTemplateCommit: ep<void, CommitDetail>()({ method: 'GET', path: '/v1/templates/{templateId}/commits/{commitId}', auth: 'human', perm: 'templates.read', body: 'none', res: 'json', errors: ['not-found'], summary: 'One template commit + its changes vs the parent.' }),
  commitTemplate: ep<CommitWorkingRequest, CommitView>()({ method: 'POST', path: '/v1/templates/{templateId}/commits', auth: 'human', perm: 'templates.write', body: 'json', res: 'json', ok: 201, errors: ['not-found', 'conflict', 'invalid', 'stale'], activities: ['audit:template.committed'], summary: '§21 #105 Commit = the only write of the template config: { message, baseCommitId, config, stageId? } (§21 #107: new blobs staged first, verified + promoted before the commit is visible) → a new commit from the base head + the browser changes (409 stale { reason: head-moved, head } when the head moved; V_NO_CHANGE when equal to the head; template validation errors block).' }),
  discardTemplateWorking: ep<DiscardWorkingRequest, { version: number; uncommitted: number }>()({ method: 'POST', path: '/v1/templates/{templateId}/working/discard', auth: 'human', perm: 'templates.write', body: 'json', res: 'json', errors: ['not-found', 'conflict'], activities: ['audit:template.working-discarded'], summary: 'REMOVED §21 #105 (410 gone): pending changes live in the browser and are discarded there.' }),
  listDeploymentCommits: ep<void, PageResponse<CommitSummary>, { cursor?: string; limit?: number }>()({ method: 'GET', path: '/v1/deployments/{deploymentId}/commits', auth: 'human', perm: 'deployment.config.read', body: 'none', res: 'json', errors: ['not-found'], summary: 'Commits of the deployment branch, newest first.' }),
  getDeploymentCommit: ep<void, CommitDetail>()({ method: 'GET', path: '/v1/deployments/{deploymentId}/commits/{commitId}', auth: 'human', perm: 'deployment.config.read', body: 'none', res: 'json', errors: ['not-found'], summary: 'One deployment commit (secrets masked) + its changes.' }),
  commitDeployment: ep<CommitWorkingRequest, CommitView>()({ method: 'POST', path: '/v1/deployments/{deploymentId}/commits', auth: 'human', perm: 'deployment.config.publish', body: 'json', res: 'json', ok: 201, errors: ['not-found', 'conflict', 'invalid', 'stale'], activities: ['branch.committed', 'audit:deployment.committed'], summary: '§21 #105 Commit = a release the sites see: { message, baseCommitId, config, stageId? } (§21 #107: new blobs staged first, verified + promoted before the commit is visible) → a new commit from the base head + the browser changes (409 stale when the head moved; publish-level blockers block; catalog built; agentEpoch+1; secret value changes recorded by slug; compat proposal for agents < 0.14).' }),
  discardDeploymentWorking: ep<DiscardWorkingRequest, { draftVersion: number; uncommitted: number }>()({ method: 'POST', path: '/v1/deployments/{deploymentId}/working/discard', auth: 'human', perm: 'deployment.config.write', body: 'json', res: 'json', errors: ['not-found', 'conflict'], activities: ['working.discarded', 'audit:deployment.working-discarded'], summary: 'REMOVED §21 #105 (410 gone): pending changes live in the browser and are discarded there.' }),
  listAgentBranches: ep<void, ListResponse<AgentBranchView>>()({ method: 'GET', path: '/v1/deployments/{deploymentId}/agent-branches', auth: 'human', perm: 'deployment.config.read', body: 'none', res: 'json', errors: ['not-found'], summary: 'Per site: the mirrored agent branch head, behind / ahead / differs, uncommitted drift, agent version.' }),
  listAgentBranchCommits: ep<void, PageResponse<CommitSummary>, { cursor?: string; limit?: number }>()({ method: 'GET', path: '/v1/deployments/{deploymentId}/agent-branches/{siteSlug}/commits', auth: 'human', perm: 'deployment.config.read', body: 'none', res: 'json', errors: ['not-found'], summary: 'Commits of one agent branch (cloud mirror), newest first.' }),
  getAgentBranchCommit: ep<void, CommitDetail>()({ method: 'GET', path: '/v1/deployments/{deploymentId}/agent-branches/{siteSlug}/commits/{commitId}', auth: 'human', perm: 'deployment.config.read', body: 'none', res: 'json', errors: ['not-found'], summary: 'One agent-branch commit (masked) + its changes.' }),
  compareBranches: ep<CompareRequest, MergePlan>()({ method: 'POST', path: '/v1/branches/compare', auth: 'human', perm: 'read on source + target', body: 'json', res: 'json', errors: ['not-found', 'invalid', 'forbidden'], summary: 'MergePlan source → target (3-way with the merge base, else two-way). diffOnly compares two points read-only. Not audited.' }),
  previewMerge: ep<MergeRequest, MergePreview>()({ method: 'POST', path: '/v1/branches/merge/preview', auth: 'human', perm: 'read on source + target', body: 'json', res: 'json', errors: ['not-found', 'invalid', 'conflict', 'forbidden'], summary: 'Dry run of merge: result (masked), changes, validation of the target kind, unresolved conflicts, missing values. Not audited.' }),
  merge: ep<MergeRequest & { message: string }, CommitView>()({ method: 'POST', path: '/v1/branches/merge', auth: 'human', perm: 'read on source + write on target (+ secrets perms for secret value items)', body: 'json', res: 'json', ok: 201, errors: ['not-found', 'invalid', 'conflict', 'stale', 'forbidden'], activities: ['branch.merged', 'branch.cherry-picked', 'audit:template.merged', 'audit:deployment.merged'], summary: 'Apply the picked items into the target head and commit (kind merge when every source change was taken, else cherry-pick); 409 stale when the target head moved; merge base updated. §21 #105: no working copy → no dirty-working-copy check.' }),
  revertCommit: ep<RevertRequest, CommitView>()({ method: 'POST', path: '/v1/branches/revert', auth: 'human', perm: 'write on the branch', body: 'json', res: 'json', ok: 201, errors: ['not-found', 'invalid', 'conflict', 'stale', 'forbidden'], activities: ['branch.reverted', 'audit:deployment.reverted', 'audit:template.reverted'], summary: 'A new commit undoing one commit (conflicts with later commits need choices).' }),
  resetToCommit: ep<ResetRequest, CommitView>()({ method: 'POST', path: '/v1/branches/reset', auth: 'human', perm: 'write on the branch', body: 'json', res: 'json', ok: 201, errors: ['not-found', 'invalid', 'conflict', 'stale', 'forbidden'], activities: ['branch.reset', 'audit:deployment.reset', 'audit:template.reset'], summary: 'A new commit whose config = an older commit of the same branch. History is never rewritten.' }),
  getAgentCommit: ep<void, { commit: Commit }>()({ method: 'GET', path: '/v1/agent/commits/{commitId}', auth: 'agent', perm: 'site key', body: 'none', res: 'json', errors: ['not-found'], activities: ['secret.fetched'], summary: 'A deployment-branch commit with plain secrets (the cloud head the site merges).' }),
  listAgentCloudCommits: ep<void, ListResponse<CommitRef>, { limit?: number }>()({ method: 'GET', path: '/v1/agent/commits', auth: 'agent', perm: 'site key', body: 'none', res: 'json', errors: [], summary: 'The newest deployment-branch commits (meta only).' }),
  pushAgentBranch: ep<PushAgentBranchRequest, PushAgentBranchResponse>()({ method: 'POST', path: '/v1/agent/branch/commits', auth: 'agent', perm: 'site key (branch site = signer, or relayed)', body: 'json', res: 'json', errors: ['invalid', 'conflict'], activities: ['agent-branch.pushed'], summary: 'Push agent-branch commits to the cloud mirror (≤ 20, oldest first; idempotent by commitId; hash / seq / parent verified).' }),
};

const S = 'session' as const;
const SA = 'session+actor' as const;
/** Agent local endpoints (spread into AGENT_API). */
export const BRANCH_AGENT_API = {
  branchState: ep<void, AgentBranchState>()({ method: 'GET', path: '/_opm/api/branch', auth: S, perm: null, body: 'none', res: 'json', errors: [], summary: '§21 #104 cloud head (+ staging per site), local branch (head, ahead / behind / differs), uncommitted counts, the running merge, every site (Fast-forward all).' }),
  listBranchCommits: ep<void, PageResponse<CommitSummary>, { cursor?: string; limit?: number }>()({ method: 'GET', path: '/_opm/api/branch/commits', auth: S, perm: null, body: 'none', res: 'json', errors: [], summary: 'Local branch commits, newest first.' }),
  getBranchCommit: ep<void, CommitDetail>()({ method: 'GET', path: '/_opm/api/branch/commits/{commitId}', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found'], summary: 'One local commit (masked) + its changes.' }),
  getCloudCommit: ep<void, CommitDetail>()({ method: 'GET', path: '/_opm/api/branch/cloud-commits/{commitId}', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: 'One deployment-branch commit (fetched + cached on demand; masked).' }),
  getBranchBlob: ep<void, Response>()({ method: 'GET', path: '/_opm/api/branch/blobs/{sha256}', auth: S, perm: null, body: 'none', res: 'raw', errors: ['not-found'], summary: 'File / volume-content bytes by sha256 (local blob store, else peers, else the cloud) for the merge view text diffs; ≤ 8 MiB.' }),
  getUncommitted: ep<void, UncommittedResponse>()({ method: 'GET', path: '/_opm/api/branch/uncommitted', auth: S, perm: null, body: 'none', res: 'json', errors: [], summary: 'GUI edits + drift items + drift notes (the latest scan).' }),
  putWorking: ep<{ config: ConfigInput }, { uncommitted: number; validation: { errors: ValidationIssue[]; warnings: ValidationIssue[] } }>()({ method: 'PUT', path: '/_opm/api/branch/working', auth: SA, perm: null, body: 'json', res: 'json', errors: ['invalid', 'locked'], summary: 'REMOVED §21 #105 (410 gone): GUI edits live in the browser until Commit (POST /_opm/api/branch/commit { config, baseCommitId, message }).' }),
  getDriftPolicy: ep<void, DriftPolicy>()({ method: 'GET', path: '/_opm/api/branch/drift-policy', auth: S, perm: null, body: 'none', res: 'json', errors: [], summary: '§21 #113 this site’s drift policy (ask | auto-commit).' }),
  putDriftPolicy: ep<DriftPolicy, DriftPolicy>()({ method: 'PUT', path: '/_opm/api/branch/drift-policy', auth: SA, perm: null, body: 'json', res: 'json', errors: ['invalid'], summary: '§21 #113 set this site’s drift policy: auto-commit = changes made in Docker outside OPM are committed by the agent (author UNKNOWN) once stable.' }),
  rescanDrift: ep<void, DriftReport>()({ method: 'POST', path: '/_opm/api/branch/drift/rescan', auth: SA, perm: null, body: 'none', res: 'json', errors: ['locked', 'unavailable'], activities: ['drift.detected'], summary: 'Scan Docker + files now.' }),
  commitLocal: ep<LocalCommitRequest, LocalCommitResponse>()({ method: 'POST', path: '/_opm/api/branch/commit', auth: SA, perm: null, body: 'json', res: 'json', ok: 201, errors: ['invalid', 'locked', 'conflict', 'stale'], activities: ['branch.committed', 'drift.committed', 'drift.accepted'], summary: '§21 #105 Commit: GUI edits ({ config, baseCommitId, stageId? } — §21 #107 staged blobs promoted first: applied by the engine, the commit is written only when the apply succeeds; 409 uncommitted-drift / stale) or drift ({ keys, accept, takeBytes }: recorded at once).' }),
  discardLocal: ep<LocalDiscardRequest, { run: UpdateRun | null }>()({ method: 'POST', path: '/_opm/api/branch/discard', auth: SA, perm: null, body: 'json', res: 'json', errors: ['invalid', 'locked'], activities: ['drift.discarded', 'working.discarded'], summary: 'Drop edits; re-apply the branch value of drift items / notes.' }),
  compareLocal: ep<LocalCompareRequest, MergePlan>()({ method: 'POST', path: '/_opm/api/branch/compare', auth: S, perm: null, body: 'json', res: 'json', errors: ['not-found', 'unavailable'], summary: 'MergePlan cloud commit → this site (scope: shared + own site; removals = "remove locally").' }),
  previewLocalMerge: ep<LocalMergeRequest, MergePreview>()({ method: 'POST', path: '/_opm/api/branch/merge/preview', auth: S, perm: null, body: 'json', res: 'json', errors: ['not-found', 'invalid', 'conflict'], summary: 'Result + agent validation + staging dry-run issues + per-container actions.' }),
  mergeLocal: ep<LocalMergeRequest, UpdateRun>()({ method: 'POST', path: '/_opm/api/branch/merge', auth: SA, perm: null, body: 'json', res: 'json', ok: 202, errors: ['not-found', 'invalid', 'conflict', 'locked', 'stale'], activities: ['branch.merged', 'branch.cherry-picked', 'branch.merge-failed'], summary: 'Merge & apply on this site (409 dirty-working-copy while anything is uncommitted); the commit is written at COMMIT.' }),
  fastForwardAll: ep<FastForwardAllRequest, UpdateRun>()({ method: 'POST', path: '/_opm/api/branch/fast-forward', auth: SA, perm: null, body: 'json', res: 'json', ok: 202, errors: ['invalid', 'conflict', 'locked', 'unavailable'], activities: ['branch.merged'], summary: 'Coordinated multi-site update to one cloud commit for clean, strictly-behind sites.' }),
  revertLocal: ep<LocalRevertRequest, UpdateRun>()({ method: 'POST', path: '/_opm/api/branch/revert', auth: SA, perm: null, body: 'json', res: 'json', ok: 202, errors: ['not-found', 'invalid', 'conflict', 'locked', 'stale'], activities: ['branch.reverted'], summary: 'Undo one local commit (new commit, applied).' }),
  resetLocal: ep<LocalRevertRequest, UpdateRun>()({ method: 'POST', path: '/_opm/api/branch/reset', auth: SA, perm: null, body: 'json', res: 'json', ok: 202, errors: ['not-found', 'invalid', 'conflict', 'locked', 'stale'], activities: ['branch.reset'], summary: 'Reset to an older local commit (new commit, applied).' }),
  // §21 #111 the per-site branch: the same endpoints on ANY site of the deployment (self → local, other → relayed over
  // the mesh with the person as actor). Commit of the GUI working copy and Fast-forward all stay on the /_opm/api/branch
  // endpoints of the agent the person is logged in to.
  // §21 #113 what happens with changes made in Docker outside OPM on that site (ask | auto-commit as UNKNOWN)
  siteGetDriftPolicy: ep<void, DriftPolicy>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/branch/drift-policy', auth: S, perm: null, body: 'none', res: 'json', errors: ['unavailable'], summary: '§21 #113 the drift policy of one site: ask (default, shown as uncommitted) | auto-commit (stable Docker changes are committed by the agent, author UNKNOWN).' }),
  sitePutDriftPolicy: ep<DriftPolicy, DriftPolicy>()({ method: 'PUT', path: '/_opm/api/sites/{siteSlug}/branch/drift-policy', auth: SA, perm: null, body: 'json', res: 'json', errors: ['invalid', 'unavailable'], summary: '§21 #113 set the drift policy of one site.' }),
  siteBranchState: ep<void, AgentBranchState>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/branch', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: '§21 #111 branchState of one site (unavailable: the site is not reachable).' }),
  siteListBranchCommits: ep<void, PageResponse<CommitSummary>, { cursor?: string; limit?: number }>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/branch/commits', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: '§21 #111 local branch commits of one site.' }),
  siteGetBranchCommit: ep<void, CommitDetail>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/branch/commits/{commitId}', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: '§21 #111 one local commit of one site.' }),
  siteGetBranchBlob: ep<void, Response>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/branch/blobs/{sha256}', auth: S, perm: null, body: 'none', res: 'raw', errors: ['not-found', 'unavailable'], summary: '§21 #111 getBranchBlob on one site.' }),
  siteGetUncommitted: ep<void, UncommittedResponse>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/branch/uncommitted', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: '§21 #111 drift + notes of one site.' }),
  siteRescanDrift: ep<void, DriftReport>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/branch/drift/rescan', auth: SA, perm: null, body: 'none', res: 'json', errors: ['locked', 'unavailable'], activities: ['drift.detected'], summary: '§21 #111 rescanDrift on one site.' }),
  siteCommitDrift: ep<LocalCommitRequest, LocalCommitResponse>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/branch/commit-drift', auth: SA, perm: null, body: 'json', res: 'json', ok: 201, errors: ['invalid', 'locked', 'conflict', 'unavailable'], activities: ['drift.committed', 'drift.accepted'], summary: '§21 #111 commitLocal on one site, drift mode only ({ message, keys, accept, takeBytes }); config / stageId → invalid (browser edits are committed on the agent the person is logged in to).' }),
  siteDiscardLocal: ep<LocalDiscardRequest, { run: UpdateRun | null }>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/branch/discard', auth: SA, perm: null, body: 'json', res: 'json', errors: ['invalid', 'locked', 'unavailable'], activities: ['drift.discarded'], summary: '§21 #111 discardLocal on one site.' }),
  siteCompareLocal: ep<LocalCompareRequest, MergePlan>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/branch/compare', auth: S, perm: null, body: 'json', res: 'json', errors: ['not-found', 'unavailable'], summary: '§21 #111 MergePlan cloud commit → that site.' }),
  sitePreviewLocalMerge: ep<LocalMergeRequest, MergePreview>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/branch/merge/preview', auth: S, perm: null, body: 'json', res: 'json', errors: ['not-found', 'invalid', 'conflict', 'unavailable'], summary: '§21 #111 merge preview on that site.' }),
  siteMergeLocal: ep<LocalMergeRequest, UpdateRun>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/branch/merge', auth: SA, perm: null, body: 'json', res: 'json', ok: 202, errors: ['not-found', 'invalid', 'conflict', 'locked', 'stale', 'unavailable'], activities: ['branch.merged', 'branch.cherry-picked', 'branch.merge-failed'], summary: '§21 #111 Merge & apply on that site only (the target logs it with the forwarding person as actor).' }),
  siteRevertLocal: ep<LocalRevertRequest, UpdateRun>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/branch/revert', auth: SA, perm: null, body: 'json', res: 'json', ok: 202, errors: ['not-found', 'invalid', 'conflict', 'locked', 'stale', 'unavailable'], activities: ['branch.reverted'], summary: '§21 #111 revertLocal on one site.' }),
  siteResetLocal: ep<LocalRevertRequest, UpdateRun>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/branch/reset', auth: SA, perm: null, body: 'json', res: 'json', ok: 202, errors: ['not-found', 'invalid', 'conflict', 'locked', 'stale', 'unavailable'], activities: ['branch.reset'], summary: '§21 #111 resetLocal on one site.' }),
  siteCurrentUpdate: ep<void, { run: UpdateRun | null }>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/updates/current', auth: S, perm: null, body: 'none', res: 'json', errors: ['unavailable'], summary: '§21 #111 the running (or last 10 min) update as that site sees it.' }),
  siteGetUpdate: ep<void, UpdateRun>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/updates/{updateId}', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: '§21 #111 one update run, read on that site.' }),
  siteAbandonUpdate: ep<void, { ok: true }>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/updates/{updateId}/abandon', auth: SA, perm: null, body: 'none', res: 'json', errors: ['not-found', 'conflict', 'unavailable'], summary: '§21 #111 abandon a run on that site.' }),
};
/** Peer endpoints (spread into PEER_API). */
export const BRANCH_PEER_API = {
  peerCloudCommit: ep<void, { commit: Commit }>()({ method: 'GET', path: '/_opm/peer/cloud-commits/{commitId}', auth: 'peer', perm: null, body: 'none', res: 'json', errors: ['not-found'], activities: ['secret.fetched'], summary: 'A cached deployment-branch commit (plain) for a site without a cloud route.' }),
  peerBranch: ep<void, AgentBranchReport & { siteSlug: Slug; agentVersion: string; strictlyBehind: boolean; canFastForward: boolean; reason: string | null }>()({ method: 'GET', path: '/_opm/peer/branch', auth: 'peer', perm: null, body: 'none', res: 'json', errors: [], summary: 'This site branch state (Fast-forward all, * views).' }),
};
