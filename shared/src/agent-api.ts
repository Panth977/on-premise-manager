/**
 * Agent endpoint tables. Normative: SPEC-final.md §7 (local API) and §8 (peer API).
 * - AGENT_API: /_opm/api/* — the agent GUI (same origin, cookie opm_session + Authorization: Actor <token> on writes)
 * - PEER_API:  /_opm/peer/* — agent ↔ agent, signed requests, ONLY on agentPort
 * Every per-site endpoint accepts {siteSlug} = a site slug, or "*" (GET only) = merged over the mesh.
 */
import { REMOTE_GUI_AGENT_API, STORAGE_AGENT_API } from './remote-gui.js';
import { FILES_AGENT_API, FILES_PEER_API } from './files.js';
import { PORT_TUNNELS_AGENT_API } from './tunnels.js';
import { createClient, ep, type Client, type EndpointDef } from './http.js';
import type * as T from './types.js';
import type { TerminateUpdateRequest, TerminateUpdateResponse } from './agent-self.js';
import { BRANCH_AGENT_API, BRANCH_PEER_API } from './branch.js';
import { STAGING_AGENT_API } from './staging.js';

const S = 'session' as const;
const SA = 'session+actor' as const;

export const AGENT_API = {
  // ── public
  healthz: ep<void, Response>()({ method: 'GET', path: '/_opm/healthz', auth: 'public', perm: null, body: 'none', res: 'text', errors: [], summary: '"ok" (text/plain).' }),
  info: ep<void, T.AgentInfoResponse>()({ method: 'GET', path: '/_opm/api/info', auth: 'public', perm: null, body: 'none', res: 'json', errors: [], summary: 'Deployment + site identity for the login page (+ optional consoleUrl, apiBase, deploymentLabel for console links).' }),

  // ── login + actor
  login: ep<T.LoginRequest, T.LoginResponse>()({ method: 'POST', path: '/_opm/api/login', auth: 'public', perm: null, body: 'json', res: 'json', errors: ['invalid-key', 'rate-limited', 'unavailable'], activities: ['login.succeeded', 'login.failed'], summary: 'Check the access key (argon2id) → Set-Cookie opm_session (6 h). unavailable = the site has no access-key hash yet.' }),
  logout: ep<void, { loggedOut: true }>()({ method: 'POST', path: '/_opm/api/logout', auth: S, perm: null, body: 'none', res: 'json', errors: [], activities: ['logout'], summary: 'End the session; clear the cookie.' }),
  session: ep<void, T.SessionResponse>()({ method: 'GET', path: '/_opm/api/session', auth: S, perm: null, body: 'none', res: 'json', errors: ['session-expired'], summary: 'Current session.' }),
  declareActor: ep<T.ActorRequest, T.ActorResponse>()({ method: 'POST', path: '/_opm/api/actor', auth: S, perm: null, body: 'json', res: 'json', errors: ['invalid', 'rate-limited'], activities: ['actor.declared'], summary: '"Who is working right now?" → actor token (exp = session expiry).' }),
  recentActors: ep<void, T.RecentActorsResponse, { limit?: number }>()({ method: 'GET', path: '/_opm/api/actors/recent', auth: S, perm: null, body: 'none', res: 'json', errors: [], summary: 'Up to 10 most recently declared emails of the deployment.' }),

  // ── deployment-wide
  overview: ep<void, T.OverviewResponse>()({ method: 'GET', path: '/_opm/api/overview', auth: S, perm: null, body: 'none', res: 'json', errors: [], summary: 'All sites (tiles), mesh link matrix, cloud route, lock.' }),
  config: ep<void, T.AgentConfigResponse>()({ method: 'GET', path: '/_opm/api/config', auth: S, perm: null, body: 'none', res: 'json', errors: [], summary: 'Head config (secrets masked) + catalog.' }),
  // §21 #113 site images (Portainer-like): the images a local edit can use; resolve a docker reference / host image
  siteImages: ep<void, T.SiteImagesResponse>()({ method: 'GET', path: '/_opm/api/images', auth: S, perm: null, body: 'none', res: 'json', errors: [], summary: '§21 #113 knownCatalog (head + recent cloud commits + drift-adopted + site images) and the images of this host’s Docker.' }),
  resolveSiteImage: ep<T.SiteImageResolveRequest, T.SiteImageResolveResponse>()({ method: 'POST', path: '/_opm/api/images/resolve', auth: SA, perm: null, body: 'json', res: 'json', errors: ['invalid', 'not-found', 'unavailable'], summary: '§21 #113 a docker reference (pulled when missing, optional registry credentials for this pull only) or a host image → a site image catalog entry <repo>:local-<12 hex>, remembered until committed.' }),
  listRevisions: ep<void, T.ListResponse<T.RevisionSummary>, { limit?: number }>()({ method: 'GET', path: '/_opm/api/revisions', auth: S, perm: null, body: 'none', res: 'json', errors: [], summary: 'Local revision chain, newest first.' }),
  getRevision: ep<void, T.RevisionView>()({ method: 'GET', path: '/_opm/api/revisions/{revId}', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found'], summary: 'One revision (masked).' }),
  revealSecret: ep<void, T.RevealSecretResponse>()({ method: 'POST', path: '/_opm/api/secrets/{secretSlug}/reveal', auth: SA, perm: null, body: 'none', res: 'json', errors: ['not-found', 'rate-limited'], activities: ['secret.revealed'], summary: 'Head secret value (RevealValue 60 s; hideAfterSec 60; Cache-Control no-store; 30/min per session → 429 rate-limited).' }),
  getFileContent: ep<void, Response>()({ method: 'GET', path: '/_opm/api/files/{fileSlug}/content', auth: S, perm: null, body: 'none', res: 'raw', errors: ['not-found'], summary: 'Head content of a deployment file.' }),
  uploadBlob: ep<void, T.BlobRef>()({ method: 'POST', path: '/_opm/api/blobs', auth: SA, perm: null, body: 'raw', res: 'json', ok: 201, errors: ['too-large'], summary: '§21 #107: no longer used by the agent GUI (bytes go through POST /_opm/api/stages + commit { stageId }); kept for compatibility. Store new file content locally (≤ 50 MiB) for a local proposal; it reaches peers by smart distribution.' }),
  previewLocalProposal: ep<T.LocalProposalRequest, T.LocalProposalPreviewResponse>()({ method: 'POST', path: '/_opm/api/proposals/local/preview', auth: SA, perm: null, body: 'json', res: 'json', errors: ['stale', 'invalid'], summary: 'Diff + validation (incl. local-edit rules) without creating anything.' }),
  createLocalProposal: ep<T.LocalProposalRequest, T.LocalProposalResponse>()({ method: 'POST', path: '/_opm/api/proposals/local', auth: SA, perm: null, body: 'json', res: 'json', ok: 201, errors: ['stale', 'invalid', 'locked'], activities: ['proposal.created'], summary: 'Revision (origin agent) + proposal; pushed to every reachable peer and to the cloud.' }),

  // ── updates
  listProposals: ep<void, T.ListResponse<T.ProposalListItem>, { limit?: number }>()({ method: 'GET', path: '/_opm/api/proposals', auth: S, perm: null, body: 'none', res: 'json', errors: [], summary: 'Proposals (open first, then the 20 newest terminal ones).' }),
  getProposal: ep<void, T.ProposalDetailResponse>()({ method: 'GET', path: '/_opm/api/proposals/{revId}', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found'], summary: 'Diff vs head, staging per site, gate.' }),
  restage: ep<void, { ok: true }>()({ method: 'POST', path: '/_opm/api/proposals/{revId}/restage', auth: SA, perm: null, body: 'none', res: 'json', errors: ['not-found', 'conflict'], summary: 'Run staging again on every site where it failed (conflict when the proposal is terminal).' }),
  approveStart: ep<void, T.ApproveStartResponse>()({ method: 'POST', path: '/_opm/api/proposals/{revId}/approve/start', auth: SA, perm: null, body: 'none', res: 'json', ok: 201, errors: ['not-found', 'gate-closed', 'locked'], summary: 'Gate check → intent; the confirm is possible after notBefore (60 s).' }),
  approve: ep<T.ApproveRequest, T.ApproveResponse>()({ method: 'POST', path: '/_opm/api/proposals/{revId}/approve', auth: SA, perm: null, body: 'json', res: 'json', ok: 201, errors: ['not-found', 'invalid', 'gate-closed', 'locked', 'conflict'], activities: ['proposal.approved', 'update.started'], summary: 'typed must be "ACCEPT", now ≥ notBefore, gate open again → this agent coordinates the update.' }),
  cancelIntent: ep<void, { cancelled: true }>()({ method: 'DELETE', path: '/_opm/api/intents/{intentId}', auth: SA, perm: null, body: 'none', res: 'json', errors: ['not-found'], summary: 'Cancel a pending approve / rollback intent.' }),
  getIntent: ep<void, T.IntentProgress>()({ method: 'GET', path: '/_opm/api/intents/{intentId}', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found'], summary: '§21 #95: the authoritative wait of a pending intent of THIS session (effective / remaining seconds, speed, ready).' }),
  intentSpeed: ep<T.IntentSpeedRequest, T.IntentProgress>()({ method: 'POST', path: '/_opm/api/intents/{intentId}/speed', auth: SA, perm: null, body: 'json', res: 'json', errors: ['not-found', 'invalid'], summary: '§21 #95 hold-to-fast-forward: speed 1 | 2 | 4 from now (agent clock); while > 1 a heartbeat every 500 ms, else it decays to ×1 after 1.5 s.' }),
  siteIntent: ep<void, T.IntentProgress>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/intents/{intentId}', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: '§21 #95: getIntent of an intent held by that site (volume reset; forwarded over the mesh).' }),
  siteIntentSpeed: ep<T.IntentSpeedRequest, T.IntentProgress>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/intents/{intentId}/speed', auth: SA, perm: null, body: 'json', res: 'json', errors: ['not-found', 'invalid', 'unavailable'], summary: '§21 #95: intentSpeed of an intent held by that site (forwarded over the mesh).' }),
  siteCancelIntent: ep<void, { cancelled: true }>()({ method: 'DELETE', path: '/_opm/api/sites/{siteSlug}/intents/{intentId}', auth: SA, perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: '§21 #95: cancelIntent of an intent held by that site (forwarded over the mesh).' }),
  currentUpdate: ep<void, { run: T.UpdateRun | null }>()({ method: 'GET', path: '/_opm/api/updates/current', auth: S, perm: null, body: 'none', res: 'json', errors: [], summary: 'The running update (any coordinator), or the last finished one of the last 10 min.' }),
  getUpdate: ep<void, T.UpdateRun>()({ method: 'GET', path: '/_opm/api/updates/{updateId}', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found'], summary: 'One update run.' }),
  terminateUpdate: ep<TerminateUpdateRequest, TerminateUpdateResponse>()({ method: 'POST', path: '/_opm/api/updates/{updateId}/terminate', auth: SA, perm: null, body: 'json', res: 'json', errors: ['not-found'], activities: ['update.terminated', 'update.recovered', 'agent.restarted'], summary: '§21 #110: stop the update on every site of its plan now (any site; "current" = the one this site holds). Nothing is rolled back unless rollback (head config + the pre-update snapshot volumes, per site); restartAgent restarts every agent first. Never refused.' }),
  abandonUpdate: ep<void, { ok: true }>()({ method: 'POST', path: '/_opm/api/updates/{updateId}/abandon', auth: SA, perm: null, body: 'none', res: 'json', errors: ['not-found', 'conflict'], activities: ['update.abandoned'], summary: 'Only when the coordinator has been unreachable for 10 min: roll THIS site back to the pre-update snapshot and unlock it.' }),

  // ── backups
  listSnapshots: ep<void, T.ListResponse<T.SnapshotSummary>>()({ method: 'GET', path: '/_opm/api/snapshots', auth: S, perm: null, body: 'none', res: 'json', errors: [], summary: 'Snapshots of the deployment (merged over the mesh).' }),
  createSnapshot: ep<T.CreateSnapshotRequest, T.CreateSnapshotResponse>()({ method: 'POST', path: '/_opm/api/snapshots', auth: SA, perm: null, body: 'json', res: 'json', ok: 202, errors: ['locked', 'gate-closed', 'invalid'], activities: ['snapshot.created'], summary: 'Manual snapshot on every site of the head config (all must be reachable).' }),
  getSnapshot: ep<void, T.SnapshotDetailResponse>()({ method: 'GET', path: '/_opm/api/snapshots/{snapshotId}', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found'], summary: 'Snapshot detail (masked).' }),
  patchSnapshot: ep<T.PatchSnapshotRequest, T.SnapshotSummary>()({ method: 'PATCH', path: '/_opm/api/snapshots/{snapshotId}', auth: SA, perm: null, body: 'json', res: 'json', errors: ['not-found', 'invalid'], summary: 'Edit the note (all sites).' }),
  deleteSnapshot: ep<void, T.Deleted>()({ method: 'DELETE', path: '/_opm/api/snapshots/{snapshotId}', auth: SA, perm: null, body: 'none', res: 'json', errors: ['not-found', 'conflict'], activities: ['snapshot.deleted'], summary: 'Delete on all reachable sites (conflict: the pre-update snapshot of a running update).' }),
  downloadSnapshot: ep<void, Response, { includeImages?: boolean }>()({ method: 'GET', path: '/_opm/api/snapshots/{snapshotId}/download', auth: S, perm: null, body: 'none', res: 'raw', errors: ['not-found', 'unavailable'], activities: ['snapshot.downloaded'], summary: '.opmsnap (tar) of every site part; unavailable when a part is on an unreachable site.' }),
  importSnapshot: ep<void, T.SnapshotSummary>()({ method: 'POST', path: '/_opm/api/snapshots/import', auth: SA, perm: null, body: 'raw', res: 'json', ok: 201, errors: ['invalid', 'conflict', 'too-large'], activities: ['snapshot.imported'], summary: 'Upload .opmsnap; verify signature + every part; distribute site parts to peers.' }),
  rollbackPlan: ep<void, T.RollbackPlanResponse>()({ method: 'GET', path: '/_opm/api/snapshots/{snapshotId}/plan', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found'], summary: 'What a rollback changes + the gate.' }),
  rollbackStart: ep<void, T.ApproveStartResponse>()({ method: 'POST', path: '/_opm/api/snapshots/{snapshotId}/rollback/start', auth: SA, perm: null, body: 'none', res: 'json', ok: 201, errors: ['not-found', 'gate-closed', 'locked'], summary: 'Gate → intent (60 s).' }),
  rollback: ep<T.ApproveRequest, T.ApproveResponse>()({ method: 'POST', path: '/_opm/api/snapshots/{snapshotId}/rollback', auth: SA, perm: null, body: 'json', res: 'json', ok: 201, errors: ['not-found', 'invalid', 'gate-closed', 'locked'], activities: ['snapshot.rolled-back', 'update.started'], summary: 'ACCEPT → rollback update (pre-update snapshot first).' }),

  // ── per site ({siteSlug} or "*" for GETs)
  siteStatus: ep<void, T.ListResponse<T.SiteStatus>>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/status', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found'], summary: 'Containers, host, uptime, certs (one item per site).' }),
  // §21 #124 the status page in parts (each one item per site with only that part filled; the rest empty / null)
  siteStatusOverview: ep<void, T.ListResponse<T.SiteStatus>>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/status/overview', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found'], summary: 'Host metrics, agent version and agent warnings (no Docker call).' }),
  siteStatusContainers: ep<void, T.ListResponse<T.SiteStatus>>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/status/containers', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found'], summary: 'Containers (CPU / RAM from the latest monitoring sample).' }),
  siteStatusUptime: ep<void, T.ListResponse<T.SiteStatus>, { range?: '24h' | '30d' }>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/status/uptime', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'invalid'], summary: 'Uptime heatmaps: uptime24h (range 24h, default) or uptime30d (range 30d).' }),
  siteStatusEdge: ep<void, T.ListResponse<T.SiteStatus>>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/status/edge', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found'], summary: 'Edge report (ports, bind errors, reload) and certificates.' }),
  siteVolumes: ep<void, T.SiteVolumesResponse>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/volumes', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: 'Declared volumes of the head config (+ orphan directories) with size on disk, backup flag, users, last snapshot; host-path mounts (never backed up).' }),
  resetVolumeStart: ep<void, T.ApproveStartResponse>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/volumes/{volumeSlug}/reset/start', auth: SA, perm: null, body: 'none', res: 'json', ok: 201, errors: ['not-found', 'invalid', 'gate-closed', 'locked'], summary: 'Round E: intent (kind volume-reset, 60 s) to reset a volume of THIS site to its starter content (invalid: no seed).' }),
  resetVolume: ep<T.ApproveRequest, T.ResetVolumeResponse>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/volumes/{volumeSlug}/reset', auth: SA, perm: null, body: 'json', res: 'json', ok: 201, errors: ['not-found', 'invalid', 'gate-closed', 'locked'], activities: ['snapshot.created', 'volume.reset-to-seed', 'volume.seeded'], summary: 'Round E: typed ACCEPT ≥ notBefore → manual snapshot of the volume, stop its containers, wipe, re-seed, start them again.' }),
  // §21 #96 volume manager (per site; another site = forwarded over the mesh; both sites log)
  listVolumeOps: ep<void, T.ListResponse<T.VolumeOp>>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/volume-ops', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: '§21 #96: volume operations of the site (running first, then the 50 newest).' }),
  getVolumeOp: ep<void, T.VolumeOp>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/volume-ops/{opId}', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: '§21 #96: one volume operation (steps, progress, result) — the GUI polls it.' }),
  emptyVolumeStart: ep<void, T.ApproveStartResponse>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/volumes/{volumeSlug}/empty/start', auth: SA, perm: null, body: 'none', res: 'json', ok: 201, errors: ['not-found', 'locked', 'conflict', 'unavailable'], summary: '§21 #96: intent (kind volume-empty, 60 s, fast-forward) to empty a volume.' }),
  emptyVolume: ep<T.VolumeEmptyRequest, T.VolumeOp>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/volumes/{volumeSlug}/empty', auth: SA, perm: null, body: 'json', res: 'json', ok: 202, errors: ['not-found', 'invalid', 'locked', 'conflict', 'rate-limited', 'unavailable'], activities: ['volume.emptied', 'volume.op-failed', 'snapshot.created'], summary: '§21 #96: typed = volume slug after the wait → stop its containers (reverse start order), optional snapshot, delete the contents (the folder / file itself, owner and mode stay), start them again.' }),
  copyVolume: ep<T.VolumeCopyRequest, T.VolumeOp>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/volumes/{volumeSlug}/copy', auth: SA, perm: null, body: 'json', res: 'json', ok: 202, errors: ['not-found', 'invalid', 'locked', 'conflict', 'rate-limited', 'unavailable'], activities: ['volume.copied', 'volume.op-failed'], summary: '§21 #96: copy the contents to a host path (safe paths only) or into another declared volume (empty, or overwrite = its slug typed).' }),
  uploadVolumeRestore: ep<void, T.VolumeRestoreUpload, { fileName?: string }>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/volumes/{volumeSlug}/restore/upload', auth: SA, perm: null, body: 'raw', res: 'json', ok: 201, errors: ['not-found', 'invalid', 'too-large', 'rate-limited', 'unavailable'], summary: '§21 #96: a .tar.gz to restore from (checked, kept 1 h).' }),
  restoreVolumeStart: ep<void, T.ApproveStartResponse>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/volumes/{volumeSlug}/restore/start', auth: SA, perm: null, body: 'none', res: 'json', ok: 201, errors: ['not-found', 'locked', 'conflict', 'unavailable'], summary: '§21 #96: intent (kind volume-restore, 60 s, fast-forward).' }),
  restoreVolume: ep<T.VolumeRestoreRequest, T.VolumeOp>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/volumes/{volumeSlug}/restore', auth: SA, perm: null, body: 'json', res: 'json', ok: 202, errors: ['not-found', 'invalid', 'locked', 'conflict', 'rate-limited', 'unavailable'], activities: ['volume.restored', 'volume.op-failed', 'snapshot.created'], summary: '§21 #96: typed = volume slug → stop its containers, snapshot of the volume, replace the contents with the copy / archive, start them again.' }),
  snapshotVolume: ep<T.VolumeSnapshotRequest, T.VolumeSnapshotResponse>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/volumes/{volumeSlug}/snapshot', auth: SA, perm: null, body: 'json', res: 'json', ok: 201, errors: ['not-found', 'locked', 'conflict', 'rate-limited', 'unavailable'], activities: ['snapshot.created'], summary: '§21 #96: a manual snapshot of only this volume on this site (listed under Backups).' }),
  listVolumeBackups: ep<void, T.VolumeBackupsResponse>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/volumes/{volumeSlug}/backups', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: '§21 #112: every snapshot on this site that holds the volume (automatic, scheduled, pre-update, manual) newest first + the volume\'s backup policy and next automatic backup; restore one with restoreVolume source.kind = snapshot.' }),
  agentProcess: ep<void, T.AgentProcessStats>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/agent/process', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: '§21 #112: the agent process itself — RSS, Go heap, goroutines, GC, CPU %, open files, Docker calls + cache, store health, disk of its own files, loops.' }),
  listVolumeCopies: ep<void, T.ListResponse<T.VolumeCopy>>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/volume-copies', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: '§21 #96: copies made by Copy to path (size, date, whether the data still exists).' }),
  deleteVolumeCopy: ep<T.DeleteVolumeCopyRequest, T.Deleted>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/volume-copies/{copyId}/delete', auth: SA, perm: null, body: 'json', res: 'json', errors: ['not-found', 'invalid', 'conflict', 'unavailable'], activities: ['volume.copy-deleted'], summary: '§21 #96: delete a copy (its data under volume-copies/, or the custom path it was written to); confirm = copyId.' }),
  containerLogs: ep<void, T.ContainerLogsResponse, { tail?: number; q?: string; since?: string }>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/containers/{containerSlug}/logs', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: 'Container log lines (tail default 200, max 5000; q = substring filter).' }),
  siteActivities: ep<void, T.PageResponse<T.Activity>, { cursor?: string; limit?: number; type?: string; actor?: string }>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/activities', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: 'Activity log newest first (* = merged by ts).' }),
  downloadActivities: ep<void, Response, T.DownloadActivitiesQuery>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/activities/download', auth: SA, perm: null, body: 'none', res: 'raw', errors: ['not-found', 'unavailable'], activities: ['debug.downloaded'], summary: '.opmlog bundle signed by THIS site.' }),
  siteDebug: ep<void, T.ListResponse<T.DebugLine>, { level?: string; module?: string; since?: string; limit?: number; q?: string }>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/debug', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: 'Debug log lines newest first (limit default 500, max 5000).' }),
  listCerts: ep<void, T.ListResponse<T.CertStatus>, { site?: string }>()({ method: 'GET', path: '/_opm/api/certs', auth: S, perm: null, body: 'none', res: 'json', errors: [], summary: 'Edge certificate status (site default this site, * = all).' }),
  renewCert: ep<void, T.CertRenewResponse, { site?: string }>()({ method: 'POST', path: '/_opm/api/certs/{hostname}/renew', auth: SA, perm: null, body: 'none', res: 'json', errors: ['not-found', 'invalid', 'rate-limited'], activities: ['cert.renewed', 'cert.failed'], summary: 'Renew one host certificate now.' }),
  localCa: ep<void, Response, { site?: string }>()({ method: 'GET', path: '/_opm/api/certs/local-ca.crt', auth: 'public', perm: null, body: 'none', res: 'raw', errors: ['not-found'], activities: ['cert.local-ca-downloaded'], summary: 'PEM root of the site local CA.' }),

  // ── transfers
  listSiteTransfers: ep<void, T.ListResponse<T.Transfer>, { direction: T.TransferDirection }>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/transfers', auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: 'to-site = files from the cloud (receive); to-cloud = files this site sent.' }),
  sendFromBrowser: ep<void, T.Transfer, { fileName: string; note?: string }>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/transfers/upload', auth: SA, perm: null, body: 'raw', res: 'json', ok: 202, errors: ['invalid', 'too-large', 'unavailable'], activities: ['transfer.sent'], summary: 'Browser file → stored on that site → uploaded to the cloud in the background (≤ 2 GiB; §21 #100 store-and-forward: returns at once with delivery.phase queued).' }),
  sendFromPath: ep<T.SendFromPathRequest, T.Transfer>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/transfers/from-path', auth: SA, perm: null, body: 'json', res: 'json', ok: 202, errors: ['invalid', 'not-found', 'forbidden', 'too-large'], activities: ['transfer.sent'], summary: 'A regular file on that server → stored on the site → uploaded to the cloud in the background (§21 #100: forbidden = blocked by the safety policy).' }),
  hostFs: ep<void, T.HostFsListResponse, { path?: string; hidden?: boolean }>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/transfers/fs', auth: S, perm: null, body: 'none', res: 'json', errors: ['invalid', 'not-found', 'forbidden', 'unavailable'], summary: '§21 #100: one directory level of the server filesystem (default "/") for the Send › Path picker; read-only, not logged; forbidden = the agent may not list it; invalid details.type = "file" when path is not a directory.' }),
  saveTransfer: ep<T.SaveTransferRequest, T.Transfer>()({ method: 'POST', path: '/_opm/api/sites/{siteSlug}/transfers/{transferId}/save', auth: SA, perm: null, body: 'json', res: 'json', errors: ['not-found', 'invalid', 'conflict', 'unavailable'], activities: ['transfer.saved'], summary: 'Write a received file to an absolute path (exists → conflict; mode 0640, owner root).' }),
  downloadSiteTransfer: ep<void, Response>()({ method: 'GET', path: '/_opm/api/sites/{siteSlug}/transfers/{transferId}/download', auth: S, perm: null, body: 'none', res: 'raw', errors: ['not-found', 'unavailable'], activities: ['transfer.received'], summary: 'Browser download of a received (or sent) file.' }),

  // ── browser tunnel
  tunnelState: ep<void, T.TunnelStateResponse>()({ method: 'GET', path: '/_opm/api/tunnel', auth: S, perm: null, body: 'none', res: 'json', errors: [], summary: 'The active lease of the deployment, if any.' }),
  tunnelStart: ep<T.TunnelStartRequest, T.TunnelLeaseResponse>()({ method: 'POST', path: '/_opm/api/tunnel/start', auth: SA, perm: null, body: 'json', res: 'json', ok: 201, errors: ['conflict'], activities: ['internet.tunnel.started'], summary: 'Start a lease (conflict details.active = the active lease).' }),
  tunnelStop: ep<{ leaseId: string }, T.TunnelLeaseResponse>()({ method: 'POST', path: '/_opm/api/tunnel/stop', auth: SA, perm: null, body: 'json', res: 'json', errors: ['not-found', 'conflict'], activities: ['internet.tunnel.stopped'], summary: 'Stop the own lease.' }),
  tunnelRevoke: ep<{ leaseId: string }, T.TunnelLeaseResponse>()({ method: 'POST', path: '/_opm/api/tunnel/revoke', auth: SA, perm: null, body: 'json', res: 'json', errors: ['not-found', 'conflict'], activities: ['internet.tunnel.revoked'], summary: 'Revoke another tab lease.' }),
  tunnelNext: ep<void, T.TunnelNextResponse, { leaseId: string; max?: number }>()({ method: 'GET', path: '/_opm/api/tunnel/next', auth: SA, perm: null, body: 'none', res: 'json', errors: ['conflict'], summary: 'Long-poll ≤ 25 s, ≤ 4 requests.' }),
  tunnelResult: ep<T.TunnelResult, { accepted: boolean }>()({ method: 'POST', path: '/_opm/api/tunnel/result', auth: SA, perm: null, body: 'json', res: 'json', errors: ['not-found', 'conflict', 'too-large'], summary: 'Result for a reqId (idempotent).' }),
  tunnelChunk: ep<void, { received: number }, { leaseId: string; index: number }>()({ method: 'POST', path: '/_opm/api/tunnel/result/{reqId}/chunk', auth: SA, perm: null, body: 'raw', res: 'json', errors: ['not-found', 'invalid'], summary: 'One ≤ 4 MiB part of a large body, in order.' }),
  tunnelComplete: ep<T.TunnelCompleteRequest, { accepted: boolean }>()({ method: 'POST', path: '/_opm/api/tunnel/result/{reqId}/complete', auth: SA, perm: null, body: 'json', res: 'json', errors: ['not-found', 'invalid'], summary: 'Join parts + sha256 check.' }),
  // §21 #104 branches
  ...BRANCH_AGENT_API,
  // §21 #107 commit staging
  ...STAGING_AGENT_API,
  // §21 #118 remote agent GUI
  ...REMOTE_GUI_AGENT_API,
  ...FILES_AGENT_API,
  ...PORT_TUNNELS_AGENT_API,
  // §21 #119 storage + moving the agent home
  ...STORAGE_AGENT_API,
} as const satisfies Record<string, EndpointDef>;

const P = 'peer' as const;
export const PEER_API = {
  ping: ep<void, T.PeerPing>()({ method: 'GET', path: '/_opm/peer/ping', auth: P, perm: null, body: 'none', res: 'json', errors: [], summary: 'State every 10 s (head, lock, staging, internet, ports).' }),
  cloudView: ep<void, T.CloudView>()({ method: 'GET', path: '/_opm/peer/cloud-view', auth: P, perm: null, body: 'none', res: 'json', errors: [], summary: 'This agent cloud view (latest fetch wins).' }),
  have: ep<void, T.HaveList>()({ method: 'GET', path: '/_opm/peer/have', auth: P, perm: null, body: 'none', res: 'json', errors: [], summary: 'Blob have list.' }),
  blob: ep<void, Response>()({ method: 'GET', path: '/_opm/peer/blobs/{sha256}', auth: P, perm: null, body: 'none', res: 'raw', errors: ['not-found'], summary: 'Blob bytes (Range supported, response not signed: content-addressed).' }),
  fetchRequest: ep<T.FetchRequest, T.FetchRequestResponse>()({ method: 'POST', path: '/_opm/peer/fetch-request', auth: P, perm: null, body: 'json', res: 'json', ok: 202, errors: ['conflict', 'no-internet'], summary: 'Ask the fetcher to download a blob from the cloud.' }),
  revision: ep<void, T.PeerRevisionResponse>()({ method: 'GET', path: '/_opm/peer/revisions/{revId}', auth: P, perm: null, body: 'none', res: 'json', errors: ['not-found'], activities: ['secret.fetched'], summary: 'Full revision (plain secrets) for staging without internet.' }),
  pushProposal: ep<T.PeerProposalPush, { stored: boolean }>()({ method: 'POST', path: '/_opm/peer/proposals', auth: P, perm: null, body: 'json', res: 'json', errors: ['invalid'], summary: 'Distribute a local proposal (idempotent by revId).' }),
  relay: ep<T.PeerRelayRequest, T.RelayResponse>()({ method: 'POST', path: '/_opm/peer/relay', auth: P, perm: null, body: 'json', res: 'json', errors: ['no-internet'], summary: 'Forward an inner signed cloud request via POST /v1/agent/relay.' }),
  actors: ep<void, T.PeerActorsResponse>()({ method: 'GET', path: '/_opm/peer/actors', auth: P, perm: null, body: 'none', res: 'json', errors: [], summary: 'The actors table (merged by every agent every 60 s).' }),
  updateStep: ep<T.UpdateStepRequest, { accepted: true }>()({ method: 'POST', path: '/_opm/peer/update/{updateId}/step', auth: P, perm: 'signer must be the plan coordinator', body: 'json', res: 'json', ok: 202, errors: ['locked', 'invalid', 'conflict'], summary: 'Start one update step on this site (async).' }),
  updateState: ep<void, T.UpdateSiteState>()({ method: 'GET', path: '/_opm/peer/update/{updateId}', auth: P, perm: null, body: 'none', res: 'json', errors: ['not-found'], summary: 'Step state of this site (also refreshes lock.lastContactAt).' }),
  snapshotStep: ep<{ snapshotId: string; reason: 'manual' | 'scheduled'; note: string; createdBy: string }, { accepted: true }>()({ method: 'POST', path: '/_opm/peer/snapshots', auth: P, perm: null, body: 'json', res: 'json', ok: 202, errors: ['locked', 'invalid'], summary: 'Take this site part of a manual snapshot (async; state in snapshotPart).' }),
  snapshotIndex: ep<void, T.ListResponse<T.SnapshotSummary>>()({ method: 'GET', path: '/_opm/peer/snapshots', auth: P, perm: null, body: 'none', res: 'json', errors: [], summary: 'Snapshot parts held by this site.' }),
  snapshotPart: ep<void, Response, T.PeerSnapshotPartQuery>()({ method: 'GET', path: '/_opm/peer/snapshots/{snapshotId}/part', auth: P, perm: null, body: 'none', res: 'raw', errors: ['not-found'], summary: 'tar of this site part (sites/<slug>/… entries, optional images/blobs/…).' }),
  putSnapshotPart: ep<void, { stored: true }>()({ method: 'PUT', path: '/_opm/peer/snapshots/{snapshotId}/part', auth: P, perm: null, body: 'raw', res: 'json', errors: ['invalid', 'conflict'], summary: 'Store an imported part (tar, verified against the manifest in the first entry).' }),
  patchSnapshot: ep<T.PatchSnapshotRequest, { ok: true }>()({ method: 'PATCH', path: '/_opm/peer/snapshots/{snapshotId}', auth: P, perm: null, body: 'json', res: 'json', errors: ['not-found'], summary: 'Note change.' }),
  deleteSnapshot: ep<void, { ok: true }>()({ method: 'DELETE', path: '/_opm/peer/snapshots/{snapshotId}', auth: P, perm: null, body: 'none', res: 'json', errors: ['not-found', 'conflict'], summary: 'Delete this site part.' }),
  siteGet: ep<void, Response>()({ method: 'GET', path: '/_opm/peer/site/{rest}', auth: P, perm: null, body: 'none', res: 'raw', errors: ['not-found'], summary: 'Read-only mirror of GET /_opm/api/sites/{self}/<rest> for GUI data of another site ({rest} may contain "/").' }),
  sitePost: ep<void, Response>()({ method: 'POST', path: '/_opm/peer/site/{rest}', auth: P, perm: 'X-OPM-Actor + X-OPM-Session headers carry the actor', body: 'raw', res: 'raw', errors: ['not-found'], summary: 'Forwarded site action (save transfer, send from path, renew cert, upload).' }),
  // §21 #104
  ...BRANCH_PEER_API,
  // §21 #123
  ...FILES_PEER_API,
} as const satisfies Record<string, EndpointDef>;

export type AgentApi = typeof AGENT_API;
export type AgentClient = Client<AgentApi>;

/**
 * Client for the agent GUI (same origin). Sends the cookie automatically; adds Authorization: Actor <token> to
 * every endpoint whose auth is "session+actor".
 */
export function createAgentClient(baseUrl: string, getActorToken: () => string | null, init?: { fetch?: typeof fetch }): AgentClient {
  return createClient(
    AGENT_API,
    baseUrl,
    ({ endpoint }) => {
      if (endpoint.auth !== 'session+actor') return null;
      const t = getActorToken();
      return t ? `Actor ${t}` : null;
    },
    { fetch: init?.fetch, credentials: 'same-origin' },
  );
}
