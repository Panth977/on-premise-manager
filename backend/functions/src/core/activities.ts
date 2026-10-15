/**
 * Activities: the per-deployment "_cloud" log (hash chained like site logs, §13.5 / decision 24),
 * ingestion of site logs (pipe + manual .opmlog, §6.1). The root audit log is core/audit.ts.
 */
import { FieldValue } from '../ports/index.js';
import { activityHashInput, canonicalize, type Activity, type ActivityOrigin } from '@opm/shared';
import { col, db, isAlreadyExists, nowIso, type Tx } from './fire.js';
import { decodeActivity, decodeDeployment, encodeActivity } from './codec.js';
import { sha256Hex } from './util.js';
import { E } from './errors.js';

export const CLOUD_LOG = '_cloud';
const MAX_DATA_BYTES = 16 * 1024;

export function activityHash(a: Omit<Activity, 'hash' | 'uploadedVia' | 'uploadedAt'>): string {
  return 'sha256:' + sha256Hex(activityHashInput(a));
}

export interface CloudEntry {
  type: string;
  summary: string;
  data: Record<string, unknown>;
  actor: string | null;
  origin?: ActivityOrigin;
}
/** Collects "_cloud" entries inside one transaction. Call depUpdate() and merge into the deployment write. */
export class CloudLog {
  private seq: number;
  private last: string | null;
  readonly written: Activity[] = [];
  constructor(
    readonly deploymentId: string,
    dep: { cloudActivitySeq: number; _cloudLastHash: string | null },
  ) {
    this.seq = dep.cloudActivitySeq ?? 0;
    this.last = dep._cloudLastHash ?? null;
  }
  add(tx: Tx, e: CloudEntry): Activity {
    const seq = this.seq + 1;
    let data = e.data ?? {};
    if (canonicalize(data).length > MAX_DATA_BYTES) data = { truncated: true };
    const base = {
      activityId: `${CLOUD_LOG}:${seq}`,
      deploymentId: this.deploymentId,
      siteSlug: CLOUD_LOG,
      seq,
      ts: nowIso(),
      actor: e.actor,
      origin: e.origin ?? ('cloud' as ActivityOrigin),
      type: e.type,
      summary: e.summary.slice(0, 300) || e.type,
      data,
      prevHash: seq === 1 ? null : this.last,
    };
    const a: Activity = { ...base, hash: activityHash(base), uploadedVia: null, uploadedAt: null };
    tx.create(col.activities(this.deploymentId).doc(a.activityId), encodeActivity(a));
    this.seq = seq;
    this.last = a.hash;
    this.written.push(a);
    return a;
  }
  depUpdate() {
    return { cloudActivitySeq: this.seq, _cloudLastHash: this.last };
  }
  get changed() {
    return this.written.length > 0;
  }
}

/** Append "_cloud" entries in their own small transaction (for read-only endpoints such as reveal / fetch). */
export async function appendCloud(deploymentId: string, entries: CloudEntry[]) {
  if (!entries.length) return;
  await db().runTransaction(async (tx) => {
    const ref = col.deployments().doc(deploymentId);
    const snap = await tx.get(ref);
    if (!snap.exists) return;
    const dep = decodeDeployment(snap.data()!);
    const log = new CloudLog(deploymentId, dep);
    for (const e of entries) log.add(tx, e);
    tx.update(ref, log.depUpdate());
  });
}

// ───────────────────────────────────────── ingestion (pipe + manual)
export interface IngestResult {
  inserted: number;
  skipped: number;
  rejected: number;
  errors: { activityId: string; reason: string }[];
  ackSeq: number;
  fromSeq: number;
  toSeq: number;
}

function shapeError(a: Activity, deploymentId: string, siteSlug: string): string | null {
  if (!a || typeof a !== 'object') return 'entry is not an object';
  if (a.deploymentId !== deploymentId) return 'deploymentId does not match';
  if (a.siteSlug !== siteSlug) return 'siteSlug does not match';
  if (!Number.isInteger(a.seq) || a.seq < 1) return 'seq must be an integer ≥ 1';
  if (a.activityId !== `${siteSlug}:${a.seq}`) return `activityId must be ${siteSlug}:${a.seq}`;
  if (typeof a.ts !== 'string' || !Number.isFinite(Date.parse(a.ts))) return 'ts must be an ISO time';
  if (typeof a.type !== 'string' || !/^[a-z]+(-[a-z]+)*(\.[a-z-]+)*$/.test(a.type)) return 'type has a bad format';
  if (typeof a.summary !== 'string') return 'summary must be a string';
  if (a.actor !== null && typeof a.actor !== 'string') return 'actor must be a string or null';
  if (!['agent', 'cloud', 'system'].includes(a.origin)) return 'origin must be agent | cloud | system';
  if (!a.data || typeof a.data !== 'object' || Array.isArray(a.data)) return 'data must be an object';
  if (a.prevHash !== null && typeof a.prevHash !== 'string') return 'prevHash must be a string or null';
  if (typeof a.hash !== 'string') return 'hash must be a string';
  return null;
}

/**
 * Verify + store entries of ONE site log. Entries sorted by seq; each hash recomputed; prevHash chained to the
 * previous entry (or the stored record seq−1; seq 1 needs prevHash null). The first broken entry and every later
 * one are rejected. Valid entries are created if missing (never overwritten). Updates agents/{slug}.ackSeq.
 */
export async function ingestSiteLog(deploymentId: string, siteSlug: string, entriesIn: Activity[], via: 'pipe' | 'manual'): Promise<IngestResult> {
  const entries = [...entriesIn].sort((a, b) => a.seq - b.seq);
  const res: IngestResult = { inserted: 0, skipped: 0, rejected: 0, errors: [], ackSeq: 0, fromSeq: entries[0]?.seq ?? 0, toSeq: entries.at(-1)?.seq ?? 0 };
  const acts = col.activities(deploymentId);
  // stored records of the batch range + seq-1 of the first entry
  const seqs = new Set<number>();
  for (const e of entries) if (Number.isInteger(e?.seq)) seqs.add(e.seq);
  if (entries[0] && entries[0].seq > 1) seqs.add(entries[0].seq - 1);
  const refs = [...seqs].map((s) => acts.doc(`${siteSlug}:${s}`));
  const stored = new Map<number, Activity>();
  for (let i = 0; i < refs.length; i += 300) {
    const snaps = await db().getAll(...refs.slice(i, i + 300));
    for (const s of snaps) if (s.exists) {
      const a = decodeActivity(s.data()!);
      stored.set(a.seq, a);
    }
  }
  let broken = false;
  let prev: Activity | null = null;
  const toCreate: Activity[] = [];
  const at = nowIso();
  for (const e of entries) {
    const id = typeof e?.activityId === 'string' ? e.activityId : `${siteSlug}:${e?.seq}`;
    if (broken) {
      res.rejected++;
      res.errors.push({ activityId: id, reason: 'after a broken entry' });
      continue;
    }
    let reason = shapeError(e, deploymentId, siteSlug);
    if (!reason) {
      const recomputed = activityHash(e);
      if (recomputed !== e.hash) reason = 'hash does not match the entry content';
    }
    if (!reason) {
      let expectedPrev: string | null | undefined;
      if (e.seq === 1) expectedPrev = null;
      else if (prev && prev.seq === e.seq - 1) expectedPrev = prev.hash;
      else if (stored.has(e.seq - 1)) expectedPrev = stored.get(e.seq - 1)!.hash;
      else expectedPrev = undefined;
      if (expectedPrev === undefined) reason = `missing seq ${e.seq - 1}`;
      else if (e.prevHash !== expectedPrev) reason = 'prevHash does not match the previous entry';
    }
    if (!reason && prev && prev.seq === e.seq) reason = 'duplicate seq';
    if (reason) {
      broken = true;
      res.rejected++;
      res.errors.push({ activityId: id, reason });
      continue;
    }
    const existing = stored.get(e.seq);
    if (existing) {
      if (existing.hash !== e.hash) {
        // never change stored records; a different chain from here on cannot be accepted
        broken = true;
        res.rejected++;
        res.errors.push({ activityId: id, reason: 'differs from the stored record' });
        continue;
      }
      res.skipped++;
      prev = existing;
      continue;
    }
    const clean: Activity = {
      activityId: e.activityId,
      deploymentId,
      siteSlug,
      seq: e.seq,
      ts: e.ts,
      actor: e.actor,
      origin: e.origin,
      type: e.type,
      summary: e.summary,
      data: e.data,
      prevHash: e.prevHash,
      hash: e.hash,
      uploadedVia: via,
      uploadedAt: at,
    };
    toCreate.push(clean);
    prev = clean;
  }
  if (toCreate.length) {
    const bw = db().bulkWriter();
    bw.onWriteError((err) => {
      if (isAlreadyExists(err)) return false;
      return err.failedAttempts < 5;
    });
    const results = toCreate.map((a) =>
      bw.create(acts.doc(a.activityId), encodeActivity(a)).then(
        () => 'ok' as const,
        (err) => (isAlreadyExists(err) ? ('exists' as const) : Promise.reject(err)),
      ),
    );
    await bw.close();
    for (const r of await Promise.all(results)) if (r === 'ok') res.inserted++;
    else res.skipped++;
  }
  res.ackSeq = await advanceAck(deploymentId, siteSlug);
  return res;
}

/** Highest contiguous seq stored for a site, from agents/{slug}.ackSeq upwards; persisted on the agent doc. */
export async function advanceAck(deploymentId: string, siteSlug: string): Promise<number> {
  const agentRef = col.agents(deploymentId).doc(siteSlug);
  const agentSnap = await agentRef.get();
  let ack = (agentSnap.data()?.ackSeq as number | undefined) ?? 0;
  const acts = col.activities(deploymentId);
  for (;;) {
    const refs = Array.from({ length: 100 }, (_, i) => acts.doc(`${siteSlug}:${ack + 1 + i}`));
    const snaps = await db().getAll(...refs);
    let n = 0;
    for (const s of snaps) {
      if (!s.exists) break;
      n++;
    }
    ack += n;
    if (n < 100) break;
  }
  if (agentSnap.exists && ack !== agentSnap.data()!.ackSeq) await agentRef.update({ ackSeq: ack });
  return ack;
}

export const inc = (n = 1) => FieldValue.increment(n);
export { E };
