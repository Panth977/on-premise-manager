/**
 * §21 #123 Files — one shared bucket per deployment (shared/src/files.ts). Console: list / publish (browser → cloud) /
 * get a copy into the cloud / download / unpublish. Agents: register a file the site published (metadata only), report
 * a copy held or dropped, send its copy when the cloud view lists it in fileFetches, ask the cloud to fetch a copy,
 * download the cloud copy, unpublish. Every change agents must see bumps agentEpoch (the cloud view carries the listing).
 * The legacy transfers (§21 #100, routes/transfers.ts) are unchanged.
 */
import { FILE_MAX, FILE_NOTE_MAX, RE, ulid, type AgentRegisterFileRequest, type PublishFileRequest, type SharedFile, type UploadPurpose } from '@opm/shared';
import { col, db, nowIso } from '../core/fire.js';
import { E } from '../core/errors.js';
import { loadDep, needDep } from '../core/perm.js';
import { asObject, hexOf, onlyKeys, V } from '../core/util.js';
import { decodeDeployment } from '../core/codec.js';
import { CloudLog } from '../core/activities.js';
import { newUpload, uploadView, type StoredUpload } from '../core/uploads.js';
import { noRedirectFor, paths, remove, serveObject } from '../core/storage.js';
import { fileView, newFetch, pickHolder, RE_FILE_ID, type StoredFile } from '../core/files.js';
import type { Ctx, Handlers } from '../core/router.js';
import type { ActivityOrigin } from '@opm/shared';

const FILE_PURPOSE: UploadPurpose = 'file';

/** fileName 1–255 without / \ NUL; sizeBytes 1..FILE_MAX; sha256; note ≤ FILE_NOTE_MAX (null / absent = none) */
export function validateFileBody(b: Record<string, unknown>): PublishFileRequest {
  const v = new V();
  const fn = b.fileName;
  if (typeof fn !== 'string' || fn.length < 1 || fn.length > 255 || /[/\\\0]/.test(fn) || fn === '.' || fn === '..') v.err('fileName', 'V_FORMAT', 'fileName must be 1–255 characters without / \\ or NUL');
  if (!Number.isInteger(b.sizeBytes) || (b.sizeBytes as number) < 1) v.err('sizeBytes', 'V_RANGE', 'sizeBytes must be an integer ≥ 1');
  if (typeof b.sha256 !== 'string' || !RE.sha256.test(b.sha256)) v.err('sha256', 'V_FORMAT', 'sha256 must be "sha256:" + 64 hex');
  if (b.note !== undefined && b.note !== null && (typeof b.note !== 'string' || b.note.length > FILE_NOTE_MAX)) v.err('note', 'V_LENGTH', `note must be ≤ ${FILE_NOTE_MAX} characters`);
  v.throwIf();
  if ((b.sizeBytes as number) > FILE_MAX) throw E.tooLarge(`files are at most ${FILE_MAX} bytes`);
  return { fileName: fn as string, sizeBytes: b.sizeBytes as number, sha256: b.sha256 as string, note: (b.note as string | null | undefined) || null };
}
function fileIdOf(c: Ctx): string {
  const id = c.params.fileId!;
  if (!RE_FILE_ID.test(id)) throw E.notFound('file not found');
  return id;
}
const sortNewest = (a: SharedFile, b: SharedFile) => (a.publishedAt < b.publishedAt ? 1 : a.publishedAt > b.publishedAt ? -1 : a.fileId < b.fileId ? 1 : -1);
const actorEmail = (b: Record<string, unknown>): string => {
  if (typeof b.actor !== 'string' || !RE.email.test(b.actor)) throw E.bad('actor', 'V_FORMAT', 'actor (email) is required');
  return b.actor;
};

// ───────────────────────────────────────── shared by the console and the agents
/** the cloud wants a copy: cloud.fetch from a holding site (listed in that site's fileFetches). Idempotent. */
async function requestFetch(deploymentId: string, fileId: string, requestedBy: string, requester: string | null): Promise<SharedFile> {
  const depRef = col.deployments().doc(deploymentId);
  const fRef = col.files(deploymentId).doc(fileId);
  return db().runTransaction(async (tx) => {
    const [ds, fs] = await Promise.all([tx.get(depRef), tx.get(fRef)]);
    if (!fs.exists) throw E.notFound('file not found');
    const f = fs.data() as StoredFile;
    if (f.state === 'unpublished') throw E.gone('the file was unpublished');
    if (f.state === 'uploading') throw E.conflict('the file is still uploading to the cloud');
    if (f.cloud.stored) return fileView(f);
    const open = f.cloud.fetch;
    if (open && !open.error && open.from) return fileView(f); // already asked
    const from = await pickHolder(tx, deploymentId, f.sites, requester);
    if (!from) throw E.conflict('no site holds a copy of the file');
    const fetch = newFetch(requestedBy, from);
    const dep = decodeDeployment(ds.data()!);
    if (f._uploadId) tx.update(col.uploads().doc(f._uploadId), { status: 'failed', error: 'the fetch was requested again' });
    tx.update(fRef, { 'cloud.fetch': fetch, _uploadId: null });
    tx.update(depRef, { agentEpoch: dep.agentEpoch + 1 });
    return fileView({ ...f, cloud: { stored: false, fetch }, _uploadId: null });
  });
}

/** unpublish: tombstone, the open upload failed, the cloud copy deleted (a migrated file also expires its transfer). Idempotent. */
async function unpublish(deploymentId: string, fileId: string, who: { actor: string | null; email: string; origin: ActivityOrigin }): Promise<{ deleted: true }> {
  const depRef = col.deployments().doc(deploymentId);
  const fRef = col.files(deploymentId).doc(fileId);
  const gone = await db().runTransaction(async (tx) => {
    const [ds, fs] = await Promise.all([tx.get(depRef), tx.get(fRef)]);
    if (!fs.exists) throw E.notFound('file not found');
    const f = fs.data() as StoredFile;
    if (f.state === 'unpublished') return null;
    const legacy = f._legacyTransferId ? await tx.get(col.transfers(deploymentId).doc(f._legacyTransferId)) : null;
    const dep = decodeDeployment(ds.data()!);
    const log = new CloudLog(deploymentId, dep);
    tx.update(fRef, { state: 'unpublished', unpublishedAt: nowIso(), unpublishedBy: who.email, cloud: { stored: false, fetch: null }, sites: [], _uploadId: null });
    if (f._uploadId) tx.update(col.uploads().doc(f._uploadId), { status: 'failed', error: 'the file was unpublished' });
    // §21 #100 agents that still use the transfer drop their copy too (deletedTransfersToSite)
    if (legacy?.exists && legacy.data()!.status !== 'expired') tx.update(legacy.ref, { status: 'expired', error: 'unpublished' });
    log.add(tx, { type: 'file.unpublished', summary: `File ${f.fileName} unpublished`, data: { fileId, fileName: f.fileName }, actor: who.actor, origin: who.origin });
    tx.update(depRef, { ...log.depUpdate(), agentEpoch: dep.agentEpoch + 1 });
    return f;
  });
  if (gone) await remove(gone._storagePath).catch(() => undefined);
  return { deleted: true };
}

async function loadFile(deploymentId: string, fileId: string): Promise<StoredFile> {
  const s = await col.files(deploymentId).doc(fileId).get();
  if (!s.exists) throw E.notFound('file not found');
  return s.data() as StoredFile;
}
/** the cloud copy, or why there is none (410 unpublished, 409 not in the cloud yet) */
function cloudCopy(f: StoredFile): StoredFile {
  if (f.state === 'unpublished') throw E.gone('the file was unpublished');
  if (f.state === 'uploading' || !f.cloud.stored) throw E.conflict('the cloud has no copy of the file yet');
  return f;
}

// ───────────────────────────────────────── handlers
export const fileHandlers: Pick<
  Handlers,
  'listFiles' | 'publishFile' | 'fetchFileToCloud' | 'downloadFile' | 'unpublishFile' | 'registerAgentFile' | 'reportAgentFileHeld' | 'startAgentFileUpload' | 'requestAgentFileFetch' | 'downloadAgentFile' | 'unpublishAgentFile'
> = {
  async listFiles(c) {
    const dep = await loadDep(c);
    needDep(c, dep, 'deployment.transfers', 'read');
    const withTombstones = ['1', 'true'].includes(c.query.get('unpublished') ?? '');
    const q = withTombstones ? await col.files(dep.deploymentId).get() : await col.files(dep.deploymentId).where('state', 'in', ['uploading', 'ready']).get();
    return { items: q.docs.map((d) => fileView(d.data() as StoredFile)).sort(sortNewest) };
  },

  async publishFile(c) {
    const dep = await loadDep(c);
    needDep(c, dep, 'deployment.transfers', 'write');
    const b = asObject(c.json());
    onlyKeys(b, ['fileName', 'sizeBytes', 'sha256', 'note']);
    const body = validateFileBody(b);
    const a = c.account!;
    const fileId = ulid();
    const target = paths.sharedFile(dep.deploymentId, fileId);
    const upload = newUpload({ purpose: FILE_PURPOSE, deploymentId: dep.deploymentId, transferId: fileId, sizeBytes: body.sizeBytes, sha256: body.sha256, createdBy: a.uid, creatorSite: null, target });
    const f: StoredFile = {
      fileId,
      fileName: body.fileName,
      sizeBytes: body.sizeBytes,
      sha256: body.sha256,
      note: body.note ?? null,
      publishedAt: nowIso(),
      publishedBy: a.email,
      origin: { kind: 'cloud' },
      state: 'uploading',
      unpublishedAt: null,
      unpublishedBy: null,
      cloud: { stored: false, fetch: null },
      sites: [],
      _storagePath: target,
      _uploadId: upload.uploadId,
    };
    const depRef = col.deployments().doc(dep.deploymentId);
    await db().runTransaction(async (tx) => {
      const d = decodeDeployment((await tx.get(depRef)).data()!);
      tx.create(col.files(d.deploymentId).doc(fileId), f);
      tx.create(col.uploads().doc(upload.uploadId), upload);
      const log = new CloudLog(d.deploymentId, d);
      log.add(tx, { type: 'file.published', summary: `File ${f.fileName} published from the cloud (${f.sizeBytes} bytes)`, data: { fileId, fileName: f.fileName, sizeBytes: f.sizeBytes, origin: f.origin }, actor: a.uid });
      tx.update(depRef, { ...log.depUpdate(), agentEpoch: d.agentEpoch + 1 });
    });
    return { file: fileView(f), upload: uploadView(upload) };
  },

  async fetchFileToCloud(c) {
    const dep = await loadDep(c);
    needDep(c, dep, 'deployment.transfers', 'read');
    return requestFetch(dep.deploymentId, fileIdOf(c), c.account!.email, null);
  },

  async downloadFile(c) {
    const dep = await loadDep(c);
    needDep(c, dep, 'deployment.transfers', 'read');
    const f = cloudCopy(await loadFile(dep.deploymentId, fileIdOf(c)));
    // one activity per download (a resumed Range request that does not start at 0 is the same download)
    const range = c.headers['range'];
    if (!range || /^bytes=0-/.test(range.trim())) {
      const depRef = col.deployments().doc(dep.deploymentId);
      await db().runTransaction(async (tx) => {
        const d = decodeDeployment((await tx.get(depRef)).data()!);
        const log = new CloudLog(d.deploymentId, d);
        log.add(tx, { type: 'file.downloaded', summary: `File ${f.fileName} downloaded from the cloud`, data: { fileId: f.fileId, where: 'cloud' }, actor: c.account!.uid });
        tx.update(depRef, log.depUpdate());
      });
    }
    return serveObject(f._storagePath, { range, fileName: f.fileName, headers: { 'X-OPM-Sha256': hexOf(f.sha256) } });
  },

  async unpublishFile(c) {
    const dep = await loadDep(c);
    needDep(c, dep, 'deployment.transfers', 'write');
    return unpublish(dep.deploymentId, fileIdOf(c), { actor: c.account!.uid, email: c.account!.email, origin: 'cloud' });
  },

  // ── agents (site key)
  async registerAgentFile(c) {
    const { deploymentId, siteSlug } = c.agent!;
    const b = asObject(c.json()) as Record<string, unknown>;
    const body = validateFileBody({ fileName: b.fileName, sizeBytes: b.sizeBytes, sha256: b.sha256, note: b.note });
    const v = new V();
    if (typeof b.fileId !== 'string' || !RE_FILE_ID.test(b.fileId)) v.err('fileId', 'V_FORMAT', 'fileId must be 1–64 characters A–Z a–z 0–9 -');
    if (typeof b.publishedAt !== 'string' || !Number.isFinite(Date.parse(b.publishedAt))) v.err('publishedAt', 'V_FORMAT', 'publishedAt must be an ISO time');
    if (typeof b.publishedBy !== 'string' || !RE.email.test(b.publishedBy)) v.err('publishedBy', 'V_FORMAT', 'publishedBy (email) is required');
    if (b.via !== 'browser' && b.via !== 'path') v.err('via', 'V_ENUM', 'via must be browser | path');
    if (b.sourcePath !== undefined && b.sourcePath !== null && (typeof b.sourcePath !== 'string' || !b.sourcePath.startsWith('/') || b.sourcePath.length > 4096)) v.err('sourcePath', 'V_FORMAT', 'sourcePath must be null or an absolute path ≤ 4096');
    v.throwIf();
    const r = b as unknown as AgentRegisterFileRequest;
    const fRef = col.files(deploymentId).doc(r.fileId);
    const depRef = col.deployments().doc(deploymentId);
    return db().runTransaction(async (tx) => {
      const [ds, fs] = await Promise.all([tx.get(depRef), tx.get(fRef)]);
      if (fs.exists) {
        const cur = fs.data() as StoredFile;
        const mine = cur.origin.kind === 'site' && cur.origin.siteSlug === siteSlug && cur.sha256 === body.sha256 && cur.sizeBytes === body.sizeBytes;
        if (!mine) throw E.conflict(`file ${r.fileId} is already published with other content or from another place`);
        return fileView(cur); // idempotent (an unpublished one comes back as its tombstone)
      }
      const f: StoredFile = {
        fileId: r.fileId,
        fileName: body.fileName,
        sizeBytes: body.sizeBytes,
        sha256: body.sha256,
        note: body.note ?? null,
        publishedAt: new Date(Date.parse(r.publishedAt)).toISOString(),
        publishedBy: r.publishedBy,
        origin: { kind: 'site', siteSlug, via: r.via, sourcePath: r.via === 'path' ? (r.sourcePath ?? null) : null },
        state: 'ready',
        unpublishedAt: null,
        unpublishedBy: null,
        cloud: { stored: false, fetch: null },
        sites: [siteSlug],
        _storagePath: paths.sharedFile(deploymentId, r.fileId),
        _uploadId: null,
      };
      const dep = decodeDeployment(ds.data()!);
      const log = new CloudLog(deploymentId, dep);
      tx.create(fRef, f);
      log.add(tx, { type: 'file.published', summary: `File ${f.fileName} published from site ${siteSlug} (${f.sizeBytes} bytes)`, data: { fileId: f.fileId, fileName: f.fileName, sizeBytes: f.sizeBytes, origin: f.origin }, actor: r.publishedBy, origin: 'agent' });
      tx.update(depRef, { ...log.depUpdate(), agentEpoch: dep.agentEpoch + 1 });
      return fileView(f);
    });
  },

  async reportAgentFileHeld(c) {
    const { deploymentId, siteSlug } = c.agent!;
    const b = asObject(c.json());
    if (typeof b.held !== 'boolean') throw E.bad('held', 'V_TYPE', 'held must be a boolean');
    const fileId = fileIdOf(c);
    const fRef = col.files(deploymentId).doc(fileId);
    const depRef = col.deployments().doc(deploymentId);
    await db().runTransaction(async (tx) => {
      const [ds, fs] = await Promise.all([tx.get(depRef), tx.get(fRef)]);
      if (!fs.exists) throw E.notFound('file not found');
      const f = fs.data() as StoredFile;
      if (f.state === 'unpublished') return; // the site drops its copy on the tombstone
      const has = f.sites.includes(siteSlug);
      if (has === b.held) return;
      const sites = b.held ? [...f.sites, siteSlug].sort() : f.sites.filter((s) => s !== siteSlug);
      const upd: Record<string, unknown> = { sites };
      const fetch = f.cloud.fetch;
      // an open fetch follows the holders: a first holder is asked; the asked site dropped its copy → another one
      if (fetch && !f.cloud.stored && (b.held ? !fetch.from : fetch.from === siteSlug)) {
        const from = b.held ? siteSlug : await pickHolder(tx, deploymentId, sites);
        upd['cloud.fetch'] = { ...fetch, from, startedAt: null, bytesDone: 0, error: null };
        if (f._uploadId && !b.held) {
          tx.update(col.uploads().doc(f._uploadId), { status: 'failed', error: `site ${siteSlug} dropped its copy` });
          upd._uploadId = null;
        }
      }
      const dep = decodeDeployment(ds.data()!);
      tx.update(fRef, upd);
      tx.update(depRef, { agentEpoch: dep.agentEpoch + 1 });
    });
    return { ok: true as const };
  },

  async startAgentFileUpload(c) {
    const { deploymentId, siteSlug } = c.agent!;
    const fileId = fileIdOf(c);
    const fRef = col.files(deploymentId).doc(fileId);
    return db().runTransaction(async (tx) => {
      const fs = await tx.get(fRef);
      if (!fs.exists) throw E.notFound('file not found');
      const f = fs.data() as StoredFile;
      if (f.state === 'unpublished') throw E.gone('the file was unpublished');
      if (f.state === 'uploading') throw E.conflict('the file is still uploading to the cloud');
      if (f.cloud.stored) throw E.conflict('the cloud already has a copy');
      const fetch = f.cloud.fetch;
      if (!fetch) throw E.conflict('the cloud did not ask for a copy');
      if (fetch.from !== siteSlug && !f.sites.includes(siteSlug)) throw E.conflict(`site ${siteSlug} is not asked for a copy`);
      // resume the open upload of this site
      if (f._uploadId) {
        const us = await tx.get(col.uploads().doc(f._uploadId));
        const u = us.data() as StoredUpload | undefined;
        if (u && u.status === 'open' && u._creatorSite === siteSlug && Date.parse(u.expiresAt) > Date.now()) {
          if (!fetch.startedAt || fetch.from !== siteSlug) tx.update(fRef, { 'cloud.fetch': { ...fetch, from: siteSlug, startedAt: fetch.startedAt ?? nowIso(), error: null } });
          return { upload: uploadView(u) };
        }
        if (u && u.status === 'open') tx.update(us.ref, { status: 'failed', error: 'replaced by a new upload' });
      }
      const upload = newUpload({ purpose: FILE_PURPOSE, deploymentId, transferId: fileId, sizeBytes: f.sizeBytes, sha256: f.sha256, createdBy: fetch.requestedBy, creatorSite: siteSlug, target: f._storagePath });
      tx.create(col.uploads().doc(upload.uploadId), upload);
      tx.update(fRef, { 'cloud.fetch': { ...fetch, from: siteSlug, startedAt: nowIso(), bytesDone: 0, error: null }, _uploadId: upload.uploadId });
      return { upload: uploadView(upload) };
    });
  },

  async requestAgentFileFetch(c) {
    const { deploymentId, siteSlug } = c.agent!;
    actorEmail(asObject(c.json()));
    return requestFetch(deploymentId, fileIdOf(c), `site:${siteSlug}`, siteSlug);
  },

  async downloadAgentFile(c) {
    const { deploymentId, relayedBy } = c.agent!;
    const f = cloudCopy(await loadFile(deploymentId, fileIdOf(c)));
    return serveObject(f._storagePath, { range: c.headers['range'], fileName: f.fileName, headers: { 'X-OPM-Sha256': hexOf(f.sha256) }, noRedirect: noRedirectFor(c.headers, relayedBy) });
  },

  async unpublishAgentFile(c) {
    const { deploymentId } = c.agent!;
    const actor = actorEmail(asObject(c.json()));
    return unpublish(deploymentId, fileIdOf(c), { actor, email: actor, origin: 'agent' });
  },
};
