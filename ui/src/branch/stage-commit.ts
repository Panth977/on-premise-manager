/**
 * §21 #107 ONE Commit = ONE transaction (both GUIs). `commitWithStage` runs the three steps of shared/src/staging.ts:
 *
 *   1. needs = newConfigBlobs(base head, commit config)          (nothing new → no stage, plain commit)
 *   2. t.createStage(needs) → { stageId, missing }               (blobs the store already has are never re-sent)
 *      every missing blob: bytes from the pending set → t.putBlob (progress: n of m files, bytes)
 *   3. commit(stageId)                                            (the server verifies + promotes, then commits)
 *
 * Any failure → t.deleteStage(stageId) (best effort; the server also drops the stage on a failed commit and the
 * lifecycle rule / daily job remove abandoned ones) and the error is rethrown: the caller keeps every pending change,
 * bytes included, so the person can retry (409 stale → rebase as in #105).
 */
import { stageProgressText, totalBytes, type BlobNeed, type Stage, type StageProgress } from '@opm/shared/staging';

export interface StageTransport {
  createStage(blobs: BlobNeed[]): Promise<Stage>;
  /** upload one blob into the stage; onBytes(n) = bytes of THIS blob sent so far (optional granularity) */
  putBlob(stageId: string, need: BlobNeed, bytes: Uint8Array, onBytes?: (n: number) => void): Promise<void>;
  deleteStage(stageId: string): Promise<void>;
}
/** the content of a pending file is not in this browser any more (e.g. IndexedDB cleared) */
export class LostBytesError extends Error {
  readonly missing: BlobNeed[];
  constructor(missing: BlobNeed[]) {
    super(`The content of ${missing.length} file${missing.length === 1 ? '' : 's'} is no longer in this browser (site data cleared?) — upload ${missing.length === 1 ? 'it' : 'them'} again or discard the change.`);
    this.name = 'LostBytesError';
    this.missing = missing;
  }
}

export async function commitWithStage<R>(o: {
  needs: BlobNeed[];
  bytesOf: (sha: string) => Uint8Array | undefined;
  transport: StageTransport;
  commit: (stageId: string | undefined) => Promise<R>;
  onProgress?: (p: StageProgress | null, text: string | null) => void;
}): Promise<R> {
  const report = (p: StageProgress | null) => o.onProgress?.(p, stageProgressText(p));
  if (!o.needs.length) {
    report({ phase: 'committing', done: 0, total: 0, bytesDone: 0, bytesTotal: 0 });
    try {
      return await o.commit(undefined);
    } finally {
      report(null);
    }
  }
  report({ phase: 'checking', done: 0, total: o.needs.length, bytesDone: 0, bytesTotal: totalBytes(o.needs) });
  let stageId: string | null = null;
  try {
    const st = await o.transport.createStage(o.needs);
    stageId = st.stageId;
    const lost = st.missing.filter((m) => !o.bytesOf(m.sha256));
    if (lost.length) throw new LostBytesError(lost);
    const total = st.missing.length;
    const bytesTotal = totalBytes(st.missing);
    let doneN = 0;
    let doneBytes = 0;
    report({ phase: 'uploading', done: 0, total, bytesDone: 0, bytesTotal });
    for (const m of st.missing) {
      const b = o.bytesOf(m.sha256)!;
      await o.transport.putBlob(st.stageId, m, b, (n) => report({ phase: 'uploading', done: doneN, total, bytesDone: doneBytes + Math.min(n, b.length), bytesTotal }));
      doneN++;
      doneBytes += b.length;
      report({ phase: 'uploading', done: doneN, total, bytesDone: doneBytes, bytesTotal });
    }
    report({ phase: 'committing', done: total, total, bytesDone: bytesTotal, bytesTotal });
    return await o.commit(st.stageId);
  } catch (e) {
    if (stageId) await o.transport.deleteStage(stageId).catch(() => undefined);
    throw e;
  } finally {
    report(null);
  }
}
