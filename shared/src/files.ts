/**
 * §21 #123 Files — one shared bucket per deployment (replaces the per-site send / receive transfers of §21 #100).
 *
 * - **Publish** from the cloud console (browser → cloud), or from any site: a browser upload through that site's agent
 *   GUI, or a path on that site's server. Only the LISTING spreads (cloud view → every agent; site ↔ site over the
 *   mesh, so sites without the cloud still see each other's files); the bytes stay where they were published.
 * - **Download on request only** — nothing is fetched by itself. A site gets a copy from a site that holds one (mesh)
 *   or from the cloud; the cloud gets a copy by asking a holding site to upload it (`fileFetches` in the cloud view —
 *   the cloud never calls a site). Every place that got a copy keeps it and serves it to the others.
 * - On a site the copy can be downloaded to the browser or saved to a path on the server; in the cloud console it
 *   downloads to the browser.
 * - **Unpublish** (deployment.transfers write) removes the file from the listing everywhere and deletes every copy;
 *   a copy someone saved to a server path stays. Files never expire.
 */
import { ep } from './http.js';
import type { ListResponse, Sha256, Slug, Ts, Ulid, Upload, Deleted } from './types.js';

/** largest file (same as the transfers before: 2 GiB) */
export const FILE_MAX = 2 * 1024 * 1024 * 1024;
export const FILE_NOTE_MAX = 500;
/** unpublished files are still listed (as tombstones) this long, so offline sites learn about them */
export const FILE_TOMBSTONE_DAYS = 30;

/** where a file was published */
export type SharedFileOrigin = { kind: 'cloud' } | { kind: 'site'; siteSlug: Slug; via: 'browser' | 'path'; sourcePath: string | null };
/** uploading = the publisher's bytes are still arriving at the source (a browser upload to the cloud) */
export type SharedFileState = 'uploading' | 'ready' | 'unpublished';

/** the cloud getting a copy from a holding site (requested by a person in the console, or by a site) */
export interface SharedFileCloudFetch {
  requestedAt: Ts;
  requestedBy: string; // email, or "site:<slug>"
  from: Slug | null; //   the site asked to upload (null: no holder is known yet)
  startedAt: Ts | null; // the site started uploading
  bytesDone: number;
  error: string | null;
}

export interface SharedFile {
  fileId: Ulid; //        made by the publisher (a site publishes offline too)
  fileName: string; //    1–255, no / \ NUL
  sizeBytes: number;
  sha256: Sha256;
  note: string | null;
  publishedAt: Ts;
  publishedBy: string; // email
  origin: SharedFileOrigin;
  state: SharedFileState;
  unpublishedAt: Ts | null;
  unpublishedBy: string | null;
  /** copies: the cloud store and the sites known to hold one */
  cloud: { stored: boolean; fetch: SharedFileCloudFetch | null };
  sites: Slug[];
}

/** a site getting a copy (agent API only) */
export interface SharedFileSiteJob {
  phase: 'queued' | 'downloading' | 'waiting-cloud' | 'failed';
  from: 'cloud' | `peer:${string}` | null;
  bytesDone: number;
  error: string | null;
  startedAt: Ts;
}
/** a file as one site sees it */
export interface SiteSharedFile extends SharedFile {
  /** this site holds a copy */
  here: boolean;
  /** this site is getting a copy now */
  job: SharedFileSiteJob | null;
  /** where it can come from right now: holding sites reachable over the mesh, and the cloud */
  sources: { peers: Slug[]; cloud: boolean };
  /** the listing reached this site from: the cloud, or (cloud unreachable) a peer */
  listedBy: 'cloud' | 'peer' | 'local';
}

// ───────────────────────── requests

export interface PublishFileRequest {
  fileName: string;
  sizeBytes: number;
  sha256: Sha256;
  note?: string | null;
}
export interface PublishFileResponse {
  file: SharedFile;
  upload: Upload;
}
export interface PublishFromPathRequest {
  path: string;
  note?: string | null;
}
export interface SaveFileRequest {
  path: string;
}
/** a site registers a file it published (metadata only; idempotent by fileId) */
export interface AgentRegisterFileRequest {
  fileId: Ulid;
  fileName: string;
  sizeBytes: number;
  sha256: Sha256;
  note: string | null;
  publishedAt: Ts;
  publishedBy: string;
  via: 'browser' | 'path';
  sourcePath: string | null;
}
export interface AgentFileHeldRequest {
  held: boolean;
}
export interface AgentFileActorRequest {
  actor: string;
}
export interface AgentFileUploadResponse {
  upload: Upload;
}

/** the files part of the cloud view (every agent: the whole listing) */
export interface CloudViewFiles {
  files: SharedFile[]; //            published (ready / uploading) + tombstones of the last FILE_TOMBSTONE_DAYS
  /** copies the cloud wants from a site: the agent of `from` uploads its copy */
  fileFetches: { fileId: Ulid; from: Slug }[];
}

/** peer listing: the files this site knows (its own publishing, its copies, tombstones) */
export interface PeerFilesResponse {
  siteSlug: Slug;
  files: SharedFile[];
}

// ───────────────────────── APIs

const H = 'human' as const;
export const FILES_CLOUD_API = {
  listFiles: ep<void, ListResponse<SharedFile>, { unpublished?: boolean }>()({ method: 'GET', path: '/v1/deployments/{deploymentId}/files', auth: H, perm: 'deployment.transfers.read', body: 'none', res: 'json', errors: ['not-found'], summary: '§21 #123: the deployment\'s shared files (newest first); unpublished=true also lists the tombstones.' }),
  publishFile: ep<PublishFileRequest, PublishFileResponse>()({ method: 'POST', path: '/v1/deployments/{deploymentId}/files', auth: H, perm: 'deployment.transfers.write', body: 'json', res: 'json', ok: 201, errors: ['not-found', 'invalid', 'too-large'], activities: ['file.published'], summary: '§21 #123: publish from the browser into the cloud: the file (state uploading) + an upload (purpose file, 8 MiB parts); complete → ready.' }),
  fetchFileToCloud: ep<void, SharedFile>()({ method: 'POST', path: '/v1/deployments/{deploymentId}/files/{fileId}/fetch', auth: H, perm: 'deployment.transfers.read', body: 'none', res: 'json', errors: ['not-found', 'conflict', 'gone'], summary: '§21 #123: get a copy into the cloud from a site that holds it (listed in that site\'s cloud view; the site uploads). Idempotent; conflict: no site holds it.' }),
  downloadFile: ep<void, Response>()({ method: 'GET', path: '/v1/deployments/{deploymentId}/files/{fileId}/download', auth: H, perm: 'deployment.transfers.read', body: 'none', res: 'raw', errors: ['not-found', 'conflict', 'gone'], activities: ['file.downloaded'], summary: '§21 #123: the cloud copy (Range; may 302 to a signed URL). conflict: the cloud has no copy yet (fetchFileToCloud).' }),
  unpublishFile: ep<void, Deleted>()({ method: 'DELETE', path: '/v1/deployments/{deploymentId}/files/{fileId}', auth: H, perm: 'deployment.transfers.write', body: 'none', res: 'json', errors: ['not-found'], activities: ['file.unpublished'], summary: '§21 #123: unpublish: tombstone, the cloud copy deleted; every site drops its copy (cloud view).' }),
} as const;

const A = 'agent' as const;
export const FILES_AGENT_CLOUD_API = {
  registerAgentFile: ep<AgentRegisterFileRequest, SharedFile>()({ method: 'POST', path: '/v1/agent/files', auth: A, perm: 'site key', body: 'json', res: 'json', errors: ['invalid', 'conflict'], activities: ['file.published'], summary: '§21 #123: a site publishes (metadata only, idempotent by fileId; the site holds the bytes).' }),
  reportAgentFileHeld: ep<AgentFileHeldRequest, { ok: true }>()({ method: 'POST', path: '/v1/agent/files/{fileId}/held', auth: A, perm: 'site key', body: 'json', res: 'json', errors: ['not-found'], summary: '§21 #123: the signing site holds (or dropped) a copy.' }),
  startAgentFileUpload: ep<void, AgentFileUploadResponse>()({ method: 'POST', path: '/v1/agent/files/{fileId}/upload', auth: A, perm: 'site key (a holder asked by fileFetches)', body: 'none', res: 'json', errors: ['not-found', 'conflict', 'gone'], summary: '§21 #123: the site starts sending its copy to the cloud (an upload, purpose file; agent upload endpoints; complete → cloud.stored).' }),
  requestAgentFileFetch: ep<AgentFileActorRequest, SharedFile>()({ method: 'POST', path: '/v1/agent/files/{fileId}/fetch', auth: A, perm: 'site key', body: 'json', res: 'json', errors: ['not-found', 'conflict', 'gone'], summary: '§21 #123: a site needs a copy it cannot get over the mesh: the cloud fetches it from a holder (the site then downloads it from the cloud).' }),
  downloadAgentFile: ep<void, Response>()({ method: 'GET', path: '/v1/agent/files/{fileId}/download', auth: A, perm: 'site key', body: 'none', res: 'raw', errors: ['not-found', 'conflict', 'gone'], summary: '§21 #123: the cloud copy (Range).' }),
  unpublishAgentFile: ep<AgentFileActorRequest, Deleted>()({ method: 'POST', path: '/v1/agent/files/{fileId}/unpublish', auth: A, perm: 'site key', body: 'json', res: 'json', errors: ['not-found'], activities: ['file.unpublished'], summary: '§21 #123: unpublish from a site (the person in actor).' }),
} as const;

const S = 'session' as const;
const SA = 'session+actor' as const;
export const FILES_AGENT_API = {
  listSiteFiles: ep<void, ListResponse<SiteSharedFile>, { unpublished?: boolean }>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/files', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: '§21 #123: the shared files as that site sees them (here, job, sources).' }),
  publishFromBrowser: ep<void, SiteSharedFile, { fileName: string; note?: string }>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/files/upload', auth: SA, perm: null, body: 'raw', res: 'json', ok: 201, errors: ['invalid', 'too-large', 'unavailable'], activities: ['file.published'], summary: '§21 #123: publish a browser upload; the site holds the bytes.' }),
  publishFromPath: ep<PublishFromPathRequest, SiteSharedFile>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/files/from-path', auth: SA, perm: null, body: 'json', res: 'json', ok: 201, errors: ['invalid', 'not-found', 'forbidden', 'too-large', 'unavailable'], activities: ['file.published'], summary: '§21 #123: publish a file of the site\'s server (copied into the agent home; the picker policy of §21 #100 applies).' }),
  fetchToSite: ep<void, SiteSharedFile>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/files/{fileId}/fetch', auth: SA, perm: null, body: 'none', res: 'json', ok: 202, errors: ['not-found', 'conflict', 'gone', 'unavailable'], summary: '§21 #123: get a copy onto that site (from a holding peer, else the cloud; else the cloud fetches it first). Follow the job in the listing.' }),
  downloadSiteFile: ep<void, Response>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/files/{fileId}/download', auth: S, perm: null, body: 'none', res: 'raw', errors: ['not-found', 'conflict', 'unavailable'], activities: ['file.downloaded'], summary: '§21 #123: that site\'s copy to the browser (conflict: not on the site yet — fetchToSite).' }),
  saveSiteFile: ep<SaveFileRequest, SiteSharedFile>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/files/{fileId}/save', auth: SA, perm: null, body: 'json', res: 'json', errors: ['not-found', 'invalid', 'conflict', 'unavailable'], activities: ['file.saved'], summary: '§21 #123: write that site\'s copy to a path on its server (must not exist; 0640).' }),
  dropSiteCopy: ep<void, SiteSharedFile>()({ method: 'DELETE', path: '/_opm/api/sites/{siteSlug}/files/{fileId}/copy', auth: SA, perm: null, body: 'none', res: 'json', errors: ['not-found', 'conflict', 'unavailable'], activities: ['file.copy-dropped'], summary: '§21 #123: delete that site\'s copy (the file stays published; conflict: it is the only copy).' }),
  unpublishSiteFile: ep<void, Deleted>()({ method: 'DELETE', path: '/_opm/api/sites/{siteSlug}/files/{fileId}', auth: SA, perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], activities: ['file.unpublished'], summary: '§21 #123: unpublish everywhere (through the cloud when reachable; else a tombstone that spreads over the mesh and reaches the cloud later).' }),
} as const;

export const FILES_PEER_API = {
  peerFiles: ep<void, PeerFilesResponse>()({ method: 'GET', path: '/_opm/peer/files', auth: 'peer', perm: null, body: 'none', res: 'json', errors: [], summary: '§21 #123: the files this site knows (its publishing, its copies, tombstones).' }),
  peerFileContent: ep<void, Response>()({ method: 'GET', path: '/_opm/peer/files/{fileId}/content', auth: 'peer', perm: null, body: 'none', res: 'raw', errors: ['not-found'], summary: '§21 #123: the bytes of a copy this site holds (Range).' }),
} as const;

// ───────────────────────── helpers

/** the badge of a file as seen on a site (agent GUI) or in the cloud console */
export type FileBadge = 'uploading' | 'here' | 'getting' | 'waiting-cloud' | 'failed' | 'available' | 'unavailable' | 'unpublished';
export function siteFileBadge(f: SiteSharedFile): FileBadge {
  if (f.state === 'unpublished') return 'unpublished';
  if (f.state === 'uploading') return 'uploading';
  if (f.here) return 'here';
  if (f.job) return f.job.phase === 'failed' ? 'failed' : f.job.phase === 'waiting-cloud' ? 'waiting-cloud' : 'getting';
  return f.sources.peers.length || f.sources.cloud || f.sites.length ? 'available' : 'unavailable';
}
export function cloudFileBadge(f: SharedFile): FileBadge {
  if (f.state === 'unpublished') return 'unpublished';
  if (f.state === 'uploading') return 'uploading';
  if (f.cloud.stored) return 'here';
  if (f.cloud.fetch) return f.cloud.fetch.error ? 'failed' : 'getting';
  return f.sites.length ? 'available' : 'unavailable';
}
/** "cloud, app1, app2" — where copies are */
export function fileCopiesText(f: SharedFile): string {
  const w = [...(f.cloud.stored ? ['cloud'] : []), ...f.sites];
  return w.length ? w.join(', ') : 'no copy';
}
export function fileOriginText(f: SharedFile): string {
  return f.origin.kind === 'cloud' ? 'the cloud (browser)' : `${f.origin.siteSlug} (${f.origin.via === 'path' ? f.origin.sourcePath ?? 'server path' : 'browser'})`;
}
