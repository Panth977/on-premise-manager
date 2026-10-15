/**
 * In-process scheduler of opm-cloud (Scheduler port, local delivery, §21 #108). Each job's last run is a document
 * (`opmScheduler/{name}`), claimed with a lease in a transaction, so a restart neither skips a missed run (it runs at
 * start-up) nor doubles one, and a second opm-cloud process on the same data dir cannot run it twice.
 */
import { randomUUID } from 'node:crypto';
import { logger, nextRun, store, type ScheduledJob } from '../ports/index.js';

const DOC = (name: string) => store().collection('opmScheduler').doc(name);

async function claim(job: ScheduledJob, now: number, owner: string): Promise<boolean> {
  return store().runTransaction(async (tx) => {
    const s = await tx.get(DOC(job.name));
    const d = s.data();
    if (!d) {
      // first start: schedule from now (a fresh install does not run the daily job immediately)
      tx.set(DOC(job.name), { name: job.name, lastRunMs: now, lastOk: null, lastError: null, leaseUntil: 0, owner: null });
      return false;
    }
    if ((d.leaseUntil as number) > now) return false;
    if (nextRun(job.schedule, d.lastRunMs as number) > now) return false;
    tx.set(DOC(job.name), { ...d, leaseUntil: now + job.timeoutSeconds * 1000 + 60_000, owner }, { merge: false });
    return true;
  });
}

export function startScheduler(jobs: ScheduledJob[], o: { tickMs?: number } = {}) {
  const owner = randomUUID();
  const running = new Set<string>();
  let stopped = false;
  const tick = async () => {
    for (const job of jobs) {
      if (stopped || running.has(job.name)) continue;
      const now = Date.now();
      let mine = false;
      try {
        mine = await claim(job, now, owner);
      } catch (e) {
        logger.warn('scheduler claim failed', { job: job.name, error: (e as Error).message });
      }
      if (!mine) continue;
      running.add(job.name);
      void (async () => {
        const t0 = Date.now();
        let err: string | null = null;
        try {
          await job.run();
        } catch (e) {
          err = (e as Error)?.message ?? String(e);
          logger.error('scheduled job failed', { job: job.name, error: (e as Error)?.stack ?? err });
        }
        try {
          await DOC(job.name).set({ name: job.name, lastRunMs: now, lastOk: err === null, lastError: err, lastDurationMs: Date.now() - t0, leaseUntil: 0, owner: null });
        } catch (e) {
          logger.warn('scheduler record failed', { job: job.name, error: (e as Error).message });
        }
        running.delete(job.name);
      })();
    }
  };
  const timer = setInterval(() => void tick(), o.tickMs ?? 15_000);
  void tick();
  return {
    async stop() {
      stopped = true;
      clearInterval(timer);
      for (let i = 0; i < 300 && running.size; i++) await new Promise((r) => setTimeout(r, 100));
    },
    running: () => [...running],
  };
}
