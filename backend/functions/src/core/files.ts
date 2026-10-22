/**
 * §21 #123 shared files — the parts used by more than one module (routes/files.ts, routes/agent.ts, core/uploads.ts):
 * the stored document, the cloud view items, picking the holder the cloud fetches from, and finishing a 'file' upload.
 * Document: deployments/{dep}/files/{fileId} = SharedFile + backend-only fields (prefixed "_", never sent):
 *   _storagePath  the cloud copy's object: files/<dep>/<fileId>, or transfers/<dep>/<transferId> for a file migrated
 *                 from a §21 #100 transfer (migrate-m keeps the object where it was)
 *   _uploadId     the open upload into the cloud (the console's publish upload, or the holder's fetch upload)
 *   _legacyTransferId  migrate-m: the transfer this file was made from (unpublish expires it too, so old agents drop it)
 */
import { FILE_TOMBSTONE_DAYS, type SharedFile, type SharedFileCloudFetch } from '@opm/shared';
import { col, nowIso, pub, type Tx } from './fire.js';
import { decodeDeployment } from './codec.js';
import { CloudLog } from './activities.js';
import { E } from './errors.js';

export type StoredFile = SharedFile & { _storagePath: string; _uploadId: string | null; _legacyTransferId?: string | null };

/** fileId: a ULID made by the publisher, or (migrate-m) the transferId it was made from */
export const RE_FILE_ID = /^[A-Za-z0-9-]{1,64}$/;
export const fileView = (f: StoredFile): SharedFile => pub<SharedFile>(f as unknown as Record<string, unknown>);

/** the cloud view part: the listing (published + tombstones of the last FILE_TOMBSTONE_DAYS) + the fetches asked of a site */
export async function filesCloudItems(deploymentId: string): Promise<{ files: SharedFile[]; fileFetches: { fileId: string; from: string }[] }> {
  const cutoff = new Date(Date.now() - FILE_TOMBSTONE_DAYS * 86400_000).toISOString();
  const [live, dead] = await Promise.all([col.files(deploymentId).where('state', 'in', ['uploading', 'ready']).get(), col.files(deploymentId).where('unpublishedAt', '>=', cutoff).get()]);
  const all = [...live.docs, ...dead.docs].map((d) => d.data() as StoredFile).sort((a, b) => (a.publishedAt < b.publishedAt ? 1 : a.publishedAt > b.publishedAt ? -1 : a.fileId < b.fileId ? 1 : -1));
  const fileFetches = all.filter((f) => f.state === 'ready' && !f.cloud.stored && f.cloud.fetch?.from).map((f) => ({ fileId: f.fileId, from: f.cloud.fetch!.from! }));
  return { files: all.map(fileView), fileFetches };
}

/** the holding site the cloud asks for a copy: the one whose agent contacted the cloud most recently (null: none) */
export async function pickHolder(tx: Tx, deploymentId: string, sites: string[], exclude: string | null = null): Promise<string | null> {
  const cand = sites.filter((s) => s !== exclude);
  const pool = cand.length ? cand : sites;
  if (!pool.length) return null;
  const snaps = await Promise.all(pool.map((s) => tx.get(col.agents(deploymentId).doc(s))));
  const seen = (i: number) => Date.parse((snaps[i]!.data()?.lastSeenAt as string | undefined) ?? '') || 0;
  let best = 0;
  for (let i = 1; i < pool.length; i++) if (seen(i) > seen(best)) best = i;
  return pool[best]!;
}

export function newFetch(requestedBy: string, from: string | null): SharedFileCloudFetch {
  return { requestedAt: nowIso(), requestedBy, from, startedAt: null, bytesDone: 0, error: null };
}

/**
 * A 'file' upload completed (core/uploads.ts, inside its transaction): the console's publish → ready; a holder's fetch →
 * cloud.stored, fetch cleared, activity file.fetched. Both: agentEpoch+1. Returns false when the file was unpublished
 * meanwhile (the caller deletes the object it just wrote).
 */
export async function markFileStored(tx: Tx, deploymentId: string, fileId: string, uploadId: string, actor: { id: string | null; siteSlug: string | null }): Promise<boolean> {
  const depRef = col.deployments().doc(deploymentId);
  const fRef = col.files(deploymentId).doc(fileId);
  const [ds, fs] = await Promise.all([tx.get(depRef), tx.get(fRef)]);
  if (!ds.exists || !fs.exists) throw E.notFound('file not found');
  const f = fs.data() as StoredFile;
  if (f.state === 'unpublished') return false;
  if (f.cloud.stored && f.state === 'ready') return true;
  const dep = decodeDeployment(ds.data()!);
  const log = new CloudLog(deploymentId, dep);
  const upd: Record<string, unknown> = { state: 'ready', cloud: { stored: true, fetch: null }, ...(f._uploadId === uploadId ? { _uploadId: null } : {}) };
  if (f.state === 'ready') {
    const from = f.cloud.fetch?.from ?? actor.siteSlug;
    log.add(tx, { type: 'file.fetched', summary: `The cloud got a copy of ${f.fileName}${from ? ` from site ${from}` : ''}`, data: { fileId, where: 'cloud', from }, actor: actor.id, origin: actor.siteSlug ? 'agent' : 'cloud' });
  }
  tx.update(fRef, upd);
  tx.update(depRef, { ...(log.changed ? log.depUpdate() : {}), agentEpoch: dep.agentEpoch + 1 });
  return true;
}
