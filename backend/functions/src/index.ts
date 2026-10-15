/**
 * OPM cloud backend — Cloud Functions entry (Firebase Functions gen 2, region asia-south1), Firebase adapter.
 *   api          HTTPS — every /v1/* route of shared/src/cloud-api.ts (app.ts handleApi)
 *   importImage  Firestore trigger — services/{serviceSlug}/images/{version} when import.status becomes pending
 *   daily        scheduled — cleanup (uptime 30 d, transfers, uploads, nonces, install tokens, bundles)
 *   §21 #106 GitHub mirror: mirrorTemplateCommit / mirrorDeploymentCommit / mirrorAgentCommit / mirrorVolumeVersion
 *   (Firestore onCreate → queue item), mirrorQueue (onCreate of a queue item → drain that branch), mirrorRetry
 *   (every 5 min: due / failed items, stale leases)
 * The triggers and jobs are declared once in app.ts (TRIGGERS / JOBS, §21 #108) and exported here under the same
 * function names and options as before; opm-cloud (server.ts) delivers the same handlers from the SQLite outbox.
 */
import { setGlobalOptions } from 'firebase-functions/v2';
import { onRequest } from 'firebase-functions/v2/https';
import { onDocumentCreated, onDocumentWritten } from 'firebase-functions/v2/firestore';
import { onSchedule } from 'firebase-functions/v2/scheduler';
import { Readable } from 'node:stream';
import { setPorts, logger, Timestamp, type DocData, type DocTrigger, type ScheduledJob } from './ports/index.js';
import { firebasePorts } from './adapters/firebase/index.js';
import { Timestamp as FsTimestamp } from 'firebase-admin/firestore';
import { handleApi, JOBS, TRIGGERS } from './app.js';

// nothing touches the ports at module load (fire.ts / storage.ts are lazy), so installing them here is early enough
setPorts(firebasePorts());

setGlobalOptions({ region: 'asia-south1' });

export const api = onRequest({ timeoutSeconds: 540, memory: '1GiB', cpu: 1, concurrency: 80, invoker: 'public', maxInstances: 20, cors: false }, async (req, res) => {
  const headers: Record<string, string | undefined> = {};
  for (const [k, v] of Object.entries(req.headers)) headers[k.toLowerCase()] = Array.isArray(v) ? v.join(', ') : v;
  const ac = new AbortController();
  res.on('close', () => {
    if (!res.writableFinished) ac.abort();
  });
  const body: Buffer = Buffer.isBuffer(req.rawBody) ? req.rawBody : Buffer.alloc(0);
  const ip = String(req.headers['x-forwarded-for'] ?? req.ip ?? '').split(',')[0]!.trim();
  const out = await handleApi({ method: req.method, rawUrl: req.originalUrl || req.url || '/', headers, body, ip, signal: ac.signal });
  res.status(out.status);
  res.set(out.headers);
  if (out.body === null) res.end();
  else if (Buffer.isBuffer(out.body) || typeof out.body === 'string') res.end(out.body);
  else {
    const s = out.body as Readable;
    s.on('error', (e) => {
      logger.warn('stream error', { error: e.message, path: out.path });
      res.destroy(e);
    });
    s.pipe(res);
  }
});

/** Firestore event data → the port's plain document (Timestamps translated) */
function plain(d: Record<string, unknown> | undefined): DocData | null {
  if (!d) return null;
  const conv = (v: unknown): unknown => {
    if (v instanceof FsTimestamp || (v && typeof v === 'object' && (v as { constructor?: { name?: string } }).constructor?.name === 'Timestamp' && typeof (v as { toMillis?: unknown }).toMillis === 'function' && !(v instanceof Timestamp))) return new Timestamp((v as FsTimestamp).seconds, (v as FsTimestamp).nanoseconds);
    if (Array.isArray(v)) return v.map(conv);
    if (v && typeof v === 'object' && Object.getPrototypeOf(v) === Object.prototype) return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, conv(x)]));
    return v;
  };
  return conv(d) as DocData;
}
const trig = (name: string): DocTrigger => TRIGGERS.find((t) => t.name === name)!;
const job = (name: string): ScheduledJob => JOBS.find((j) => j.name === name)!;
const OPTS = (t: DocTrigger) => ({ document: t.pattern, timeoutSeconds: t.timeoutSeconds, memory: t.memory, retry: false });
function created(name: string) {
  const t = trig(name);
  return onDocumentCreated(OPTS(t), async (event) => {
    const after = plain(event.data?.data());
    if (after) await t.handler({ path: event.document, params: event.params as Record<string, string>, before: null, after });
  });
}
function written(name: string) {
  const t = trig(name);
  return onDocumentWritten(OPTS(t), async (event) => {
    await t.handler({ path: event.document, params: event.params as Record<string, string>, before: plain(event.data?.before?.data()), after: plain(event.data?.after?.data()) });
  });
}
function scheduled(name: string) {
  const j = job(name);
  return onSchedule({ schedule: j.schedule, timeZone: 'UTC', timeoutSeconds: j.timeoutSeconds, memory: j.memory }, async () => {
    await j.run();
  });
}

export const importImage = written('importImage');
export const daily = scheduled('daily');
export const mirrorTemplateCommit = created('mirrorTemplateCommit');
export const mirrorDeploymentCommit = created('mirrorDeploymentCommit');
export const mirrorAgentCommit = created('mirrorAgentCommit');
export const mirrorVolumeVersion = created('mirrorVolumeVersion');
export const mirrorQueue = created('mirrorQueue');
export const mirrorRetry = scheduled('mirrorRetry');
