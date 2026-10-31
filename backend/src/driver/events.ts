/**
 * Events, Scheduler and Queue ports (SPEC §21 #108).
 *
 * Events — document-change handlers. Business code declares them ONCE (core/triggers.ts: pattern + kind + handler);
 * the SQLite write path appends a row to the `_outbox` table of the SAME database file in the SAME transaction as the
 * write that matches a trigger pattern (change-data capture, so no write path can forget to emit); a dispatcher in
 * opm-cloud drains the outboxes after every commit and on a 1 s poll (other processes, restarts), at-least-once, one
 * handler run per row, failures logged and dropped.
 * Queue — background work is a durable queue of documents + a created-trigger (the GitHub mirror queue
 *   githubMirror/config/queue/* with leases, drained by mirrorQueue, retried by the mirrorRetry schedule). The
 *   outbox/trigger is the wake-up, the documents are the queue.
 * Scheduler — named jobs with a schedule string; an in-process timer (opm-cloud) that
 *   records the last run in the global DB so a restart neither skips nor doubles a run.
 */
import type { DocData } from './types/docstore.js';

export interface DocEvent {
  /** full document path */
  path: string;
  /** values of the {placeholders} of the pattern */
  params: Record<string, string>;
  before: DocData | null;
  after: DocData | null;
}
export interface DocTrigger {
  name: string;
  /** created: fires once when the document is created · written: create / update / delete */
  kind: 'created' | 'written';
  /** e.g. templates/{templateId}/commits/{commitId} */
  pattern: string;
  timeoutSeconds: number;
  memory: '256MiB' | '512MiB' | '1GiB';
  handler(e: DocEvent): Promise<void>;
}

export interface ScheduledJob {
  name: string;
  /** App Engine cron subset used by OPM: "every day HH:MM" | "every N minutes" (UTC) */
  schedule: string;
  timeoutSeconds: number;
  memory: '256MiB' | '512MiB' | '1GiB';
  run(): Promise<void>;
}

/** placeholders of a trigger pattern matched against a document path; null = no match */
export function matchPattern(pattern: string, path: string): Record<string, string> | null {
  const p = pattern.split('/');
  const s = path.split('/');
  if (p.length !== s.length) return null;
  const out: Record<string, string> = {};
  for (let i = 0; i < p.length; i++) {
    const m = /^\{([A-Za-z0-9_]+)\}$/.exec(p[i]!);
    if (m) out[m[1]!] = s[i]!;
    else if (p[i] !== s[i]) return null;
  }
  return out;
}

/** next run (ms) of a schedule after `from` */
export function nextRun(schedule: string, from: number): number {
  let m = /^every day (\d{2}):(\d{2})$/.exec(schedule);
  if (m) {
    const d = new Date(from);
    let t = Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate(), Number(m[1]), Number(m[2]));
    if (t <= from) t += 86_400_000;
    return t;
  }
  m = /^every (\d+) minutes?$/.exec(schedule);
  if (m) {
    const step = Number(m[1]) * 60_000;
    return Math.floor(from / step) * step + step;
  }
  throw new Error(`unsupported schedule "${schedule}"`);
}
