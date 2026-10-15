/**
 * Outbox dispatcher (Events port, local delivery). Every file has `_outbox` rows written in the same SQLite transaction
 * as the matching document write (docstore.ts). The dispatcher wakes after local commits and polls every file whose
 * mtime changed (other processes write too), claims rows with a lease, runs the handler, deletes the row when the
 * handler settles (errors logged + dropped = Cloud Functions retry:false). Expired leases are re-run (at-least-once).
 */
import { statSync } from 'node:fs';
import { Timestamp, logger, matchPattern, type DocTrigger } from '../../ports/index.js';
import { decodeDoc } from './codec.js';
import type { LocalDocStore } from './docstore.js';
import { isBusy } from './files.js';

const LEASE_MS = 15 * 60_000;
interface OutRow {
  seq: number;
  trigger: string;
  path: string;
  before: string | null;
  after: string | null;
}

/** OPM_OUTBOX_MIN_AGE_MS: deliver a row only after it is this old (default 0; the test stack uses ~1 s, the latency of a
 *  Cloud Functions trigger, so timing-sensitive tests see the same ordering on both adapters) */
const MIN_AGE_MS = () => Math.max(0, Number(process.env.OPM_OUTBOX_MIN_AGE_MS) || 0);
export function startOutboxDispatcher(store: LocalDocStore, triggers: DocTrigger[], o: { concurrency?: number; pollMs?: number } = {}): { stop(): Promise<void>; idle(): Promise<void> } {
  const concurrency = Math.max(1, o.concurrency ?? 8);
  const pollMs = o.pollMs ?? 1000;
  const byName = new Map(triggers.map((t) => [t.name, t]));
  const dirty = new Set<string>();
  /** last seen mtime signature per file, and whether rows leased elsewhere were left */
  const seen = new Map<string, string>();
  const leasedElsewhere = new Set<string>();
  const running = new Set<Promise<void>>();
  let stopped = false;
  let pumping = false;

  const sig = (file: string) => {
    let s = '';
    for (const f of [file, `${file}-wal`]) {
      try {
        const st = statSync(f);
        s += `${st.mtimeMs}:${st.size};`;
      } catch {
        s += '-;';
      }
    }
    return s;
  };
  const claim = (key: string, n: number): OutRow[] => {
    const h = store.files.handle(key, false);
    if (!h) return [];
    const now = Date.now();
    try {
      const rows = h
        .prep('UPDATE _outbox SET leaseUntil = ? WHERE seq IN (SELECT seq FROM _outbox WHERE (leaseUntil IS NULL OR leaseUntil < ?) AND createdMs <= ? ORDER BY seq LIMIT ?) RETURNING seq, trigger, path, before, after')
        .all(now + LEASE_MS, now, now - MIN_AGE_MS(), n) as unknown as OutRow[];
      if (rows.length < n) {
        const left = h.prep('SELECT count(*) AS n FROM _outbox').get() as { n: number };
        if (left.n > rows.length) leasedElsewhere.add(key);
        else leasedElsewhere.delete(key);
      }
      return rows;
    } catch (e) {
      if (isBusy(e)) {
        dirty.add(key);
        return [];
      }
      throw e;
    }
  };
  const done = (key: string, seq: number) => {
    try {
      store.files.handle(key, false)?.prep('DELETE FROM _outbox WHERE seq = ?').run(seq);
    } catch (e) {
      logger.warn('outbox: delete failed (the row re-runs when its lease expires)', { file: key, seq, err: String(e) });
    }
  };
  const run = (key: string, r: OutRow) => {
    const t = byName.get(r.trigger);
    const p = (async () => {
      if (!t) {
        logger.warn('outbox: no handler for trigger (row dropped)', { trigger: r.trigger, path: r.path });
        return;
      }
      const params = matchPattern(t.pattern, r.path) ?? {};
      try {
        await t.handler({ path: r.path, params, before: r.before === null ? null : decodeDoc(r.before), after: r.after === null ? null : decodeDoc(r.after) });
      } catch (e) {
        logger.error(`trigger ${t.name} failed`, { path: r.path, err: e instanceof Error ? (e.stack ?? e.message) : String(e) });
      }
    })().finally(() => {
      done(key, r.seq);
      running.delete(p);
      dirty.add(key);
      pump();
    });
    running.add(p);
  };
  function pump() {
    if (pumping || stopped) return;
    pumping = true;
    try {
      while (dirty.size && running.size < concurrency) {
        const key = dirty.values().next().value as string;
        dirty.delete(key);
        const rows = claim(key, concurrency - running.size);
        for (const r of rows) run(key, r);
        if (rows.length && running.size >= concurrency) dirty.add(key);
      }
    } catch (e) {
      logger.error('outbox dispatcher error', { err: String(e) });
    } finally {
      pumping = false;
    }
  }
  const poll = () => {
    if (stopped) return;
    try {
      for (const key of store.files.allKeys()) {
        const s = sig(store.files.fileOf(key));
        if (seen.get(key) !== s || leasedElsewhere.has(key)) {
          seen.set(key, s);
          dirty.add(key);
        }
      }
    } catch (e) {
      logger.error('outbox poll error', { err: String(e) });
    }
    pump();
  };
  const onCommit = (key: string) => {
    dirty.add(key);
    queueMicrotask(pump);
  };
  store.events.on('outbox', onCommit);
  const timer = setInterval(poll, pollMs);
  timer.unref?.();
  poll();

  const pendingRows = () => {
    for (const key of store.files.allKeys()) {
      const h = store.files.handle(key, false);
      if (h && (h.prep('SELECT 1 AS x FROM _outbox LIMIT 1').get() as unknown)) return true;
    }
    return false;
  };
  return {
    async stop() {
      stopped = true;
      clearInterval(timer);
      store.events.off('outbox', onCommit);
      while (running.size) await Promise.allSettled([...running]);
    },
    async idle() {
      for (;;) {
        while (running.size) await Promise.allSettled([...running]);
        if (!pendingRows()) {
          await new Promise((r) => setImmediate(r));
          if (!running.size && !pendingRows()) return;
          continue;
        }
        for (const k of store.files.allKeys()) dirty.add(k);
        pump();
        await new Promise((r) => setTimeout(r, running.size ? 0 : 20));
      }
    },
  };
}

/** Firestore TTL policies: agentNonces.expiresAt / rateLimits.expireAt older than now → deleted. Returns the count. */
export async function sweepDocTtl(store: LocalDocStore, now: number = Date.now()): Promise<number> {
  let n = 0;
  for (const [coll, field] of [
    ['agentNonces', 'expiresAt'],
    ['rateLimits', 'expireAt'],
  ] as const) {
    for (;;) {
      const s = await store.collection(coll).where(field, '<', Timestamp.fromMillis(now)).select().limit(500).get();
      if (s.empty) break;
      const b = store.batch();
      for (const d of s.docs) b.delete(d.ref);
      await b.commit();
      n += s.size;
      if (s.size < 500) break;
    }
  }
  return n;
}
