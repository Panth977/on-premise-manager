/**
 * §21 #107 — ONE Commit = ONE transaction, including the bytes (docs-v2/branches-commits.md §16).
 *
 * File content (FileDef.content), volume starter content (seed tree / single / archive) and every other blob a config
 * references are PENDING CHANGES of the browser change set like any other edit: nothing is uploaded while the person
 * edits. Commit does, in this order:
 *
 *   1. createStage { blobs: every blob the commit config references that the base head does not }
 *        → { stageId, missing }   (missing = not in the content-addressed store yet; unchanged blobs are never re-sent)
 *   2. putStageBlob (raw ≤ SMALL_BLOB_MAX) / createUpload { purpose, sizeBytes, sha256, stageId } (multi-part, larger)
 *        for every missing blob → staging area (cloud: Storage `staging/<stageId>/<hex>`; agent: <dataDir>/staging/<id>/)
 *        — progress "n of m files · bytes"
 *   3. commit { message, baseCommitId, config, stageId }
 *        server: stale check → validation → every NEW blob (in config, not in the head) must be in the store or in the
 *        stage with the right size AND sha256 (else 422 invalid V_BLOB_MISSING, details.missing) → PROMOTE the staged
 *        blobs into the store (files/<hex>, idempotent copy) → the commit transaction (head check again) → delete the stage.
 *   4. on ANY failure (upload, 422, 409 stale, network, crash) nothing is visible: the server deletes the stage (best
 *      effort; abandoned stages expire: Storage lifecycle staging/ > 1 day + the daily job), the browser keeps every
 *      pending change INCLUDING the bytes (memory + IndexedDB) so the person can retry; 409 → rebase as in #105.
 *
 * Crash safety: blobs are promoted BEFORE the commit becomes visible, so a crash at any point leaves either no commit
 * (promoted blobs are unreferenced content-addressed objects: nothing reads them) or a commit whose blobs are all
 * readable. Readers (agent getAgentBlob, the GitHub mirror, the console) only ever read the store, never the stage.
 */
import { ep } from './http.js';
import type { Config, ConfigInput, Deleted, Sha256, SiteConfig, Ts, Ulid } from './types.js';

/** a stage lives at most this long (the Storage lifecycle rule deletes `staging/` objects older than 1 day) */
export const STAGE_TTL_MS = 24 * 3600_000;
/** blobs per stage (= new blobs per commit) */
export const STAGE_MAX_BLOBS = 5000;
/** total new bytes per commit */
export const STAGE_MAX_BYTES = 1024 * 1024 * 1024;
/** Storage prefix of the cloud staging area (lifecycle rule: age > 1 day → delete) */
export const STAGING_PREFIX = 'staging/';
export const stagingObject = (stageId: string, hex: string) => `${STAGING_PREFIX}${stageId}/${hex}`;
/** server-issued stage ids are ULIDs */
export const RE_STAGE_ID = /^[0-9A-HJKMNP-TV-Z]{26}$/;

/** one blob a commit needs */
export interface BlobNeed {
  sha256: Sha256; // "sha256:" + 64 hex
  sizeBytes: number;
}
export interface CreateStageRequest {
  /** every NEW blob of the commit config (newConfigBlobs) */
  blobs: BlobNeed[];
}
export interface Stage {
  stageId: Ulid;
  expiresAt: Ts;
  /** blobs the store does not have yet: upload them into the stage (putStageBlob / createUpload with stageId) */
  missing: BlobNeed[];
  /** how many of the requested blobs the store already has (not re-uploaded) */
  present: number;
}
export interface StageBlobResponse {
  sha256: Sha256;
  sizeBytes: number;
  /** false = the store already had it (nothing written to the stage) */
  staged: boolean;
}
/** 422 invalid details when a commit references a blob that is neither stored nor staged */
export interface BlobMissingDetails {
  missing: BlobNeed[];
}

const hexOf = (s: string) => (s.startsWith('sha256:') ? s.slice(7) : s);
const RE_SHA = /^sha256:[0-9a-f]{64}$/;

type CfgLike = Pick<Config, 'files' | 'sites'> | Pick<ConfigInput, 'files' | 'sites'>;
/** every blob a config references, unique by sha256, sorted: File contents + volume seeds (tree entries, single, archive) */
export function configBlobs(config: CfgLike | null | undefined): BlobNeed[] {
  const out = new Map<string, BlobNeed>();
  if (!config) return [];
  const add = (sha: unknown, size: unknown) => {
    if (typeof sha !== 'string') return;
    const s = sha.startsWith('sha256:') ? sha : `sha256:${sha}`;
    if (!RE_SHA.test(s) || out.has(s)) return;
    out.set(s, { sha256: s, sizeBytes: typeof size === 'number' && Number.isFinite(size) ? size : 0 });
  };
  for (const f of config.files ?? []) if (f?.content) add(f.content.sha256, f.content.sizeBytes);
  for (const site of config.sites ?? []) {
    const sc = site?.config as SiteConfig | undefined;
    for (const v of sc?.volumes ?? []) {
      const seed = v?.seed;
      if (!seed) continue;
      if (seed.kind === 'tree') for (const e of seed.entries ?? []) add(e?.sha256, e?.size);
      else if (seed.kind === 'single') add(seed.sha256, seed.size);
      else if (seed.kind === 'archive') add(seed.blobSha256, seed.size);
    }
  }
  return [...out.values()].sort((a, b) => (a.sha256 < b.sha256 ? -1 : a.sha256 > b.sha256 ? 1 : 0));
}
/** blobs of `next` that `base` does not reference (what a commit may need to upload) */
export function newConfigBlobs(base: CfgLike | null | undefined, next: CfgLike): BlobNeed[] {
  const have = new Set(configBlobs(base).map((b) => b.sha256));
  return configBlobs(next).filter((b) => !have.has(b.sha256));
}
export const blobHex = hexOf;
export const totalBytes = (l: readonly BlobNeed[]) => l.reduce((n, b) => n + (b.sizeBytes || 0), 0);

/** progress of the upload step of a commit (CommitDialog) */
export interface StageProgress {
  phase: 'checking' | 'uploading' | 'committing';
  /** files uploaded / to upload */
  done: number;
  total: number;
  bytesDone: number;
  bytesTotal: number;
}
export function stageProgressText(p: StageProgress | null): string | null {
  if (!p) return null;
  if (p.phase === 'checking') return 'Checking content…';
  if (p.phase === 'committing') return 'Committing…';
  const mb = (n: number) => (n < 1024 ? `${Math.max(0, n)} B` : n < 1024 * 1024 ? `${Math.round(n / 1024)} KB` : `${(n / 1048576).toFixed(n < 10 * 1048576 ? 1 : 0)} MB`);
  return `Uploading ${p.done} of ${p.total} file${p.total === 1 ? '' : 's'} · ${mb(p.bytesDone)} of ${mb(p.bytesTotal)}`;
}

// ───────────────────────────────────────── API (spread into CLOUD_API / AGENT_API)
const SA = 'session+actor' as const;
/**
 * Cloud: the staging area of ONE commit attempt. Not audited (AUDIT_EXEMPT_OPS): the commit is the one audit entry
 * (§21 #107: no separate "blob uploaded" noise). createUpload { …, stageId } (multi-part, > SMALL_BLOB_MAX) writes into
 * the stage too and is not audited either.
 */
export const STAGING_CLOUD_API = {
  createStage: ep<CreateStageRequest, Stage>()({ method: 'POST', path: '/v1/stages', auth: 'human', perm: 'templates.write or deployment.config.write (any deployment)', body: 'json', res: 'json', ok: 201, errors: ['invalid', 'too-large'], summary: '§21 #107 step 1 of a Commit: open a staging area (24 h) for the blobs the commit config references that the base head does not; answers which of them the content-addressed store is missing (only those are uploaded). Not audited.' }),
  putStageBlob: ep<void, StageBlobResponse>()({ method: 'PUT', path: '/v1/stages/{stageId}/blobs/{sha256}', auth: 'human', perm: 'the creator of the stage', body: 'raw', res: 'json', errors: ['not-found', 'gone', 'invalid', 'too-large'], summary: '§21 #107 step 2: one blob into the stage (raw ≤ SMALL_BLOB_MAX; verified against {sha256}; Storage staging/<stageId>/<hex>; larger blobs: POST /v1/uploads with stageId). Already in the store → 200 staged=false. Not audited.' }),
  deleteStage: ep<void, Deleted>()({ method: 'DELETE', path: '/v1/stages/{stageId}', auth: 'human', perm: 'the creator of the stage', body: 'none', res: 'json', errors: ['not-found'], summary: '§21 #107: drop a stage and its blobs (the browser calls it after a failed commit; the server also deletes the stage itself on every failed commit, and the Storage lifecycle rule + the daily job remove abandoned stages). Not audited.' }),
};
/** Agent GUI: the same three steps against the agent (staging dir <dataDir>/staging/<stageId>/; commitLocal { …, stageId }). */
export const STAGING_AGENT_API = {
  createLocalStage: ep<CreateStageRequest, Stage>()({ method: 'POST', path: '/_opm/api/stages', auth: SA, perm: null, body: 'json', res: 'json', ok: 201, errors: ['invalid', 'too-large'], summary: '§21 #107: open a staging area for a GUI commit; answers which blobs the agent store is missing.' }),
  putLocalStageBlob: ep<void, StageBlobResponse>()({ method: 'PUT', path: '/_opm/api/stages/{stageId}/blobs/{sha256}', auth: SA, perm: null, body: 'raw', res: 'json', errors: ['not-found', 'gone', 'invalid', 'too-large'], summary: '§21 #107: one blob (≤ 200 MiB — seed archives; files ≤ 50 MiB — verified against {sha256}) into the stage; nothing reaches the blob store before the commit.' }),
  deleteLocalStage: ep<void, Deleted>()({ method: 'DELETE', path: '/_opm/api/stages/{stageId}', auth: SA, perm: null, body: 'none', res: 'json', errors: ['not-found'], summary: '§21 #107: drop a stage (also done by the agent on a failed commit; stages older than 24 h are removed at start and hourly).' }),
};
