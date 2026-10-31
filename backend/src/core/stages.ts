/**
 * §21 #107 commit staging (shared/src/staging.ts, docs-v2/branches-commits.md §16). One Commit = one transaction incl.
 * the bytes: the browser uploads the NEW blobs of a commit into a stage (Storage staging/<stageId>/<hex>, Firestore
 * stages/{stageId}); the commit verifies them (size + sha256), PROMOTES them into the content-addressed store
 * (files/<hex>) and only then runs its Firestore transaction. Every reader (getBlob, getAgentBlob, the GitHub mirror)
 * reads the store only, so a crash at any point leaves either no visible commit (promoted blobs are unreferenced) or a
 * commit whose blobs are all readable. Any failed commit deletes its stage; abandoned stages expire (Storage lifecycle
 * rule staging/ ≥ 1 day + the daily job).
 */
import { RE_STAGE_ID, SEED_ARCHIVE_MAX, STAGE_MAX_BLOBS, STAGE_MAX_BYTES, STAGE_TTL_MS, newConfigBlobs, stagingObject, ulid, type BlobNeed, type Config, type Stage, type ValidationIssue } from '@opm/shared';
import { db, isoPlus, nowIso } from './fire.js';
import { blobs } from '../driver/index.js';
import { ApiError, E } from './errors.js';
import { env } from './env.js';
import { exists, hashObject, paths, remove, removePrefix } from './storage.js';
import { hexOf, isHex64 } from './util.js';
import type { Ctx } from './router.js';

export const BLOB_FILE_MAX = 50 * 1024 * 1024;
export interface StageDoc {
  stageId: string;
  createdBy: string;
  createdAt: string;
  expiresAt: string;
  blobs: number;
}
export const stagesCol = () => db().collection('stages');
export const stagePrefix = (stageId: string) => `staging/${stageId}/`;

async function pool<T, R>(items: readonly T[], n: number, f: (x: T) => Promise<R>): Promise<R[]> {
  const out: R[] = new Array(items.length);
  let i = 0;
  await Promise.all(
    Array.from({ length: Math.min(n, items.length) }, async () => {
      while (i < items.length) {
        const k = i++;
        out[k] = await f(items[k]!);
      }
    }),
  );
  return out;
}
/** size of an object, null when absent */
async function meta(path: string): Promise<{ size: number; sha: string | null } | null> {
  const m = await blobs().head(path);
  return m ? { size: m.size, sha: m.meta.opmSha256 ?? null } : null;
}
/** the blob is in the store (files/<hex>, or the legacy registry blob path) */
export async function inStore(hex: string): Promise<boolean> {
  return (await exists(paths.file(hex))) || (await exists(paths.blob(hex)));
}

export function parseBlobList(v: unknown): BlobNeed[] {
  if (!Array.isArray(v)) throw E.bad('blobs', 'V_TYPE', 'blobs must be a list of { sha256, sizeBytes }');
  if (v.length > STAGE_MAX_BLOBS) throw E.bad('blobs', 'V_TOO_MANY', `at most ${STAGE_MAX_BLOBS} blobs per commit`);
  const seen = new Map<string, BlobNeed>();
  v.forEach((x, i) => {
    const o = x as Record<string, unknown> | null;
    if (!o || typeof o !== 'object') throw E.bad(`blobs[${i}]`, 'V_TYPE', 'each blob must be { sha256, sizeBytes }');
    if (typeof o.sha256 !== 'string' || !/^sha256:[0-9a-f]{64}$/.test(o.sha256)) throw E.bad(`blobs[${i}].sha256`, 'V_FORMAT', 'sha256 must be "sha256:" + 64 lowercase hex');
    if (!Number.isInteger(o.sizeBytes) || (o.sizeBytes as number) < 0) throw E.bad(`blobs[${i}].sizeBytes`, 'V_RANGE', 'sizeBytes must be an integer ≥ 0');
    if ((o.sizeBytes as number) > SEED_ARCHIVE_MAX) throw E.tooLarge(`blobs[${i}]: at most ${SEED_ARCHIVE_MAX} bytes`);
    seen.set(o.sha256, { sha256: o.sha256, sizeBytes: o.sizeBytes as number });
  });
  const list = [...seen.values()];
  if (list.reduce((n, b) => n + b.sizeBytes, 0) > STAGE_MAX_BYTES) throw E.tooLarge(`at most ${STAGE_MAX_BYTES} new bytes per commit`);
  return list;
}

export async function openStage(uid: string, blobs: BlobNeed[]): Promise<Stage> {
  const stageId = ulid();
  const doc: StageDoc = { stageId, createdBy: uid, createdAt: nowIso(), expiresAt: isoPlus(STAGE_TTL_MS), blobs: blobs.length };
  await stagesCol().doc(stageId).create(doc);
  const have = await pool(blobs, 25, (b) => inStore(hexOf(b.sha256)));
  const missing = blobs.filter((_, i) => !have[i]);
  return { stageId, expiresAt: doc.expiresAt, missing, present: blobs.length - missing.length };
}

/** the caller's open stage (404 for another person's / unknown id; 410 expired) */
export async function loadStage(stageId: unknown, uid: string, o: { allowExpired?: boolean } = {}): Promise<StageDoc> {
  if (typeof stageId !== 'string' || !RE_STAGE_ID.test(stageId)) throw E.bad('stageId', 'V_FORMAT', 'stageId must be a stage id (POST /v1/stages)');
  const s = await stagesCol().doc(stageId).get();
  const d = s.data() as StageDoc | undefined;
  if (!d || d.createdBy !== uid) throw E.notFound('stage not found');
  if (!o.allowExpired && Date.parse(d.expiresAt) <= Date.now()) throw E.gone('the stage expired — commit again (the content is still in your browser)');
  return d;
}

/** write one verified blob into the stage (metadata opmSha256 = the server-computed hash) */
export async function writeStaged(stageId: string, hex: string, data: Buffer): Promise<void> {
  await blobs().put(stagingObject(stageId, hex), data, { contentType: 'application/octet-stream', cacheControl: 'private, max-age=0', meta: { opmSha256: hex } });
}
export async function markStaged(path: string, hex: string): Promise<void> {
  await blobs().setMeta(path, { opmSha256: hex });
}

/** delete a stage (objects + doc). Only the creator's stage is touched. Never throws. */
export async function dropStage(stageId: string | undefined | null, uid?: string): Promise<void> {
  if (!stageId || typeof stageId !== 'string' || !RE_STAGE_ID.test(stageId)) return;
  try {
    const s = await stagesCol().doc(stageId).get();
    const d = s.data() as StageDoc | undefined;
    if (uid && d && d.createdBy !== uid) return;
    await removePrefix(stagePrefix(stageId)).catch(() => undefined);
    if (s.exists) await stagesCol().doc(stageId).delete();
  } catch {
    /* best effort: the lifecycle rule + daily job clean up */
  }
}

/** where a sha is referenced in a config (issue paths) */
function refPaths(config: Pick<Config, 'files' | 'sites'>): Map<string, string> {
  const m = new Map<string, string>();
  const put = (sha: unknown, p: string) => {
    if (typeof sha === 'string' && !m.has(sha)) m.set(sha, p);
  };
  for (const f of config.files ?? []) if (f?.content) put(f.content.sha256, `files[${f.fileSlug}].content`);
  for (const s of config.sites ?? [])
    for (const v of s?.config?.volumes ?? []) {
      const seed = v?.seed;
      const at = `sites[${s.siteSlug}].config.volumes[${v.volumeSlug}].seed`;
      if (seed?.kind === 'tree') for (const e of seed.entries ?? []) put(e?.sha256, `${at}.entries[${e?.path}]`);
      else if (seed?.kind === 'single') put(seed.sha256, at);
      else if (seed?.kind === 'archive') put(seed.blobSha256, at);
    }
  return m;
}

export type TestFault = 'after-verify' | 'after-promote';
/** emulator only: x-opm-test-fault simulates a crash at that point of a commit */
export function testFault(c: Ctx): TestFault | null {
  if (!env.emulator) return null;
  const v = c.headers['x-opm-test-fault'];
  return v === 'after-verify' || v === 'after-promote' ? v : null;
}
function crash(at: TestFault): never {
  throw new ApiError('internal', `test fault: crash ${at}`);
}

/**
 * Verify every needed blob (store, else the stage: size + sha256) and promote the staged ones into the store.
 * 422 invalid V_BLOB_MISSING (details.missing) when one is neither stored nor staged correctly.
 */
export async function ensureBlobs(o: { needs: BlobNeed[]; stageId?: string | null; uid: string; fault?: TestFault | null; paths?: Map<string, string> }): Promise<{ promoted: number }> {
  const { needs } = o;
  if (o.stageId) await loadStage(o.stageId, o.uid);
  const stored = await pool(needs, 25, (b) => inStore(hexOf(b.sha256)));
  const todo: { need: BlobNeed; hex: string; path: string }[] = [];
  const missing: { need: BlobNeed; why: string }[] = [];
  await pool(
    needs.map((n, i) => ({ n, i })),
    10,
    async ({ n, i }) => {
      if (stored[i]) return;
      const hex = hexOf(n.sha256);
      if (!o.stageId || !isHex64(hex)) return void missing.push({ need: n, why: 'not uploaded' });
      const path = stagingObject(o.stageId, hex);
      const m = await meta(path);
      if (!m) return void missing.push({ need: n, why: 'not uploaded' });
      if (m.size !== n.sizeBytes) return void missing.push({ need: n, why: `staged ${m.size} bytes, the config says ${n.sizeBytes}` });
      const sha = m.sha ?? (await hashObject(path)).hex;
      if (sha !== hex) {
        await remove(path).catch(() => undefined);
        return void missing.push({ need: n, why: 'staged bytes do not match the sha256' });
      }
      todo.push({ need: n, hex, path });
    },
  );
  if (missing.length) {
    missing.sort((a, b) => (a.need.sha256 < b.need.sha256 ? -1 : 1));
    const issues: ValidationIssue[] = missing.map((x) => ({ path: o.paths?.get(x.need.sha256) ?? 'config', code: 'V_BLOB_MISSING', message: `content ${x.need.sha256.slice(0, 19)}… (${x.need.sizeBytes} bytes): ${x.why} — upload it with the commit (POST /v1/stages)`, severity: 'error' }));
    const e = E.invalid(issues, `${missing.length} file content${missing.length === 1 ? ' is' : 's are'} missing: ${missing[0]!.why}`);
    (e.details as Record<string, unknown>).missing = missing.map((x) => x.need);
    throw e;
  }
  if (o.fault === 'after-verify') crash('after-verify');
  // promote (idempotent; content-addressed): BEFORE the commit becomes visible
  await pool(todo, 10, async (t) => {
    const dst = paths.file(t.hex);
    if (await exists(dst)) return;
    try {
      await blobs().copy(t.path, dst);
    } catch {
      const b = await blobs().get(t.path);
      if (!b) throw new Error(`staged object ${t.path} disappeared`);
      await blobs().put(dst, b, { contentType: 'application/octet-stream', cacheControl: 'private, max-age=0' });
    }
  });
  if (o.fault === 'after-promote') crash('after-promote');
  return { promoted: todo.length };
}

/** the commit check: new blobs of `config` vs the head; returns the hook for commitTo */
export function commitBlobs(c: Ctx, head: Pick<Config, 'files' | 'sites'> | null, stageId: string | undefined) {
  return async (config: Config) => {
    const needs = newConfigBlobs(head, config);
    if (!needs.length) return;
    await ensureBlobs({ needs, stageId, uid: c.account!.uid, fault: testFault(c), paths: refPaths(config) });
  };
}
export function optStageId(b: Record<string, unknown>): string | undefined {
  if (b.stageId === undefined || b.stageId === null) return undefined;
  if (typeof b.stageId !== 'string' || !RE_STAGE_ID.test(b.stageId)) throw E.bad('stageId', 'V_FORMAT', 'stageId must be a stage id (POST /v1/stages)');
  return b.stageId;
}

/** daily job: expired stage docs + their objects (orphan prefixes are removed by the Storage lifecycle rule) */
export async function cleanupStages(now = Date.now()): Promise<number> {
  const q = await stagesCol().where('expiresAt', '<=', new Date(now).toISOString()).limit(500).get();
  for (const d of q.docs) {
    await removePrefix(stagePrefix(d.id)).catch(() => undefined);
    await d.ref.delete().catch(() => undefined);
  }
  return q.size;
}
export { BLOB_FILE_MAX as STAGE_FILE_MAX };
