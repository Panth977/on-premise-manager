/**
 * §21 #101 mock of the volume content cloud API (VOLUME_CONTENT_CLOUD_API, human endpoints) + its seed, kept out of
 * db.ts / server.ts (two small hooks there: `ensureVcSeed(db)` per request, `volumeContentHandlers(deps)` merged into the
 * handler table). State lives in the same MockDb (key `vc`, persisted with it; a reset re-seeds).
 *
 * Seed: deployment "Node-RED plant" (DepNodeRedFlows00003, tag gov, head = rev 1, agent 0.11.1 → the < 0.12 warnings)
 * whose site `flows` runs node-red with the folder volume `nodered-data` (own starter content: flows.json, settings.js
 * (render expressions), package.json, lib/helper.js; persist on). The draft changes flows.json + adds README.md (Diff page
 * per-file changes). Versions: #1 snapshot (after-apply), #2 edit (admin), #3 snapshot (auto, the site changed
 * flows.json + added lib/extra.js); a pending push of #2 (base #1) → state "conflict". Template "Node-RED (volume content)"
 * (TplNodeRedContent007) has the same site.
 * Migration: templates Northwind client (nginx) and Single (volume kinds) get own
 * content instead of their legacy Files seeds. Template "Legacy (Files as volume content)" (TplLegacySeeds000008) keeps legacy seeds (folder +
 * single file) and a File mounted inside a writable volume, for the Convert / Move-into-the-volume UI.
 * Simulation: a snapshot request is answered after ~1.5 s with a manual-cloud snapshot of the simulated live tree.
 * Test hook: `window.__opmMockVc.state(name)` puts the seeded volume into a sync state (screenshots),
 * `window.__opmMockVc.apply()` applies the pending push like a person on the site would.
 */
import type { Account, Config, Deployment, Revision, SiteConfig, VolumeTree, VolumeTreeEntry, VolumeContentPush, VolumeContentState, VolumeContentVersion, VolumeSyncReport, ValidationIssue, Template } from '@opm/shared';
import { migrateVolumeFileSeeds, cloudSyncState, compileExcludes, countTreeChanges, defaultContainer, defaultSiteConfig, isExcluded, newEdgeHost, normalizeVolumeTree, treeStats, ulid, volumeTreeHash, volumeTreeIssues, VOLUME_CONTENT_LIMITS, VOLUME_PERSIST_DEFAULT_EXCLUDE } from '@opm/shared';
import { ago, b64, buildCatalog, getDb, now, pseudoHash, saveNow, sha256Hex, memBlobs, type MockDb } from './db';

export const VC_DEPLOYMENT = 'DepNodeRedFlows00003';
export const VC_TEMPLATE = 'TplNodeRedContent007';
export const VC_SITE = 'flows';
export const VC_VOLUME = 'nodered-data';
export const VC_LEGACY_TEMPLATE = 'TplLegacySeeds000008';
const ADMIN = 'uAdmin00000000000001';
const MIN = 60_000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;

type StoredVersion = VolumeContentVersion & { tree: VolumeTree };
interface VcDoc {
  versions: StoredVersion[];
  push: VolumeContentPush | null;
  snapshotRequest: VolumeContentState['snapshotRequest'];
  live: (VolumeSyncReport & { reportedAt: string }) | null;
  /** the simulated live tree of the site */
  liveTree: VolumeTree;
  updatedAt: string | null;
}
type VcDb = MockDb & { vc?: Record<string, Record<string, VcDoc>>; vcSeeded?: number };
const VC_SEED_VERSION = 5; // 5: §21 #117 persist content is literal (render none)

const enc = new TextEncoder();
const hex = async (s: string) => sha256Hex(enc.encode(s));
async function putText(db: MockDb, text: string): Promise<{ sha256: string; size: number }> {
  const bytes = enc.encode(text);
  const h = await sha256Hex(bytes);
  db.blobs[h] = b64(bytes);
  return { sha256: `sha256:${h}`, size: bytes.length };
}
const treeHashOf = (t: VolumeTree) => volumeTreeHash(t, hex);

const FLOWS_V1 = `[
  { "id": "tab1", "type": "tab", "label": "Plant integration" },
  { "id": "in1", "type": "mqtt in", "z": "tab1", "topic": "plant/+/telemetry", "broker": "broker1" },
  { "id": "fn1", "type": "function", "z": "tab1", "name": "normalize", "func": "msg.payload.ts = Date.now();\\nreturn msg;" },
  { "id": "out1", "type": "http request", "z": "tab1", "method": "POST", "url": "http://core-api:8080/v1/ingest" }
]
`;
const FLOWS_V2 = `[
  { "id": "tab1", "type": "tab", "label": "Plant integration" },
  { "id": "in1", "type": "mqtt in", "z": "tab1", "topic": "plant/+/telemetry", "broker": "broker1" },
  { "id": "fn1", "type": "function", "z": "tab1", "name": "normalize", "func": "msg.payload.ts = Date.now();\\nmsg.payload.unit = 'ppm';\\nreturn msg;" },
  { "id": "out1", "type": "http request", "z": "tab1", "method": "POST", "url": "http://core-api:8080/v2/ingest" }
]
`;
const FLOWS_SITE = `[
  { "id": "tab1", "type": "tab", "label": "Plant integration" },
  { "id": "in1", "type": "mqtt in", "z": "tab1", "topic": "plant/+/telemetry", "broker": "broker1" },
  { "id": "fn1", "type": "function", "z": "tab1", "name": "normalize", "func": "msg.payload.ts = Date.now();\\nreturn msg;" },
  { "id": "fn2", "type": "function", "z": "tab1", "name": "client hook", "func": "return require('./lib/extra.js')(msg);" },
  { "id": "out1", "type": "http request", "z": "tab1", "method": "POST", "url": "http://core-api:8080/v1/ingest" }
]
`;
const FLOWS_DRAFT = FLOWS_V1.replace('"Plant integration"', '"Plant integration (v2)"');
const SETTINGS = `module.exports = {
  uiPort: 1880,
  flowFile: "flows.json",
  httpAdminRoot: "/x/nodered/",
  // rendered once per site when the starter content is written
  credentialSecret: "{{ deployment.id }}-{{ site.slug }}",
  logging: { console: { level: "info" } },
};
`;
const PACKAGE = `{
  "name": "plant-flows",
  "private": true,
  "dependencies": { "node-red-contrib-modbus": "5.43.0" }
}
`;
const HELPER = `// shared helpers of the plant flows
module.exports.round = (n, d = 2) => Math.round(n * 10 ** d) / 10 ** d;
`;
const EXTRA = `module.exports = (msg) => {
  msg.payload.client = "acme";
  return msg;
};
`;
const README = `# Plant flows

Starter content of the Node-RED data volume. Edited in the cloud console (Volume › Content).
`;

const entry = (path: string, b: { sha256: string; size: number }, render: 'none' | 'expressions' = 'none'): VolumeTreeEntry => ({ path, sha256: b.sha256, size: b.size, mode: '0644', render });

function nodeRedSite(db: MockDb, seed: VolumeTree): SiteConfig {
  const sc = defaultSiteConfig(null);
  const img = db.images['nodered']?.['v1'];
  const c = defaultContainer('nodered', 'v1', { kind: img?.kind ?? 'service', ports: img?.ports ?? [{ name: 'http', port: 1880, protocol: 'tcp', description: '' }], env: img?.env ?? [], volumes: img?.volumes ?? [], healthcheck: img?.healthcheck ?? null }, 'node-red');
  c.volumes = [{ imageVolume: null, mountPath: '/data', source: { kind: 'volume', volumeSlug: VC_VOLUME }, readOnly: false }];
  sc.containers = [c];
  sc.volumes = [{ volumeSlug: VC_VOLUME, label: 'Node-RED data', description: 'flows, settings and the client integration code', backup: true, location: { kind: 'auto' }, kind: 'dir', seed: { kind: 'tree', ...normalizeVolumeTree(seed) }, persist: { exclude: [...VOLUME_PERSIST_DEFAULT_EXCLUDE], maxBytes: 50 * 1024 * 1024, auto: true } }];
  sc.edge = { ...sc.edge, hosts: [newEdgeHost('*')], routes: [] };
  return sc;
}

/** idempotent: adds the §21 #101 seed to a db that does not have it (fresh seed, reset, older stored db) */
export async function ensureVcSeed(dbIn: MockDb): Promise<void> {
  const db = dbIn as VcDb;
  if (db.vcSeeded === VC_SEED_VERSION) return;
  db.vcSeeded = VC_SEED_VERSION;
  const f1 = await putText(db, FLOWS_V1);
  const f2 = await putText(db, FLOWS_V2);
  const fs = await putText(db, FLOWS_SITE);
  const fd = await putText(db, FLOWS_DRAFT);
  const st = await putText(db, SETTINGS);
  const pk = await putText(db, PACKAGE);
  const hp = await putText(db, HELPER);
  const ex = await putText(db, EXTRA);
  const rd = await putText(db, README);
  const headSeed = normalizeVolumeTree({ entries: [entry('flows.json', f1), entry('settings.js', st), entry('package.json', pk), entry('lib/helper.js', hp)], dirs: ['backups'] });
  const draftSeed = normalizeVolumeTree({ entries: [entry('flows.json', fd), entry('settings.js', st), entry('package.json', pk), entry('lib/helper.js', hp), entry('README.md', rd)], dirs: ['backups'] });

  // migrate-h on the Northwind client template (its nodered-data volume took flows.json / settings.js from Files)
  const imageVolumes = (svc: string, ver: string) => db.images[svc]?.[ver]?.volumes.map((v) => ({ name: v.name, mountPath: v.mountPath })) ?? null;
  for (const id of ['TplNorthwindClient04', 'TplVolumeKinds000006']) {
    const t = db.templates[id];
    if (!t) continue;
    const r = migrateVolumeFileSeeds({ files: t.files, sites: t.sites }, imageVolumes);
    t.files = r.config.files;
    t.sites = r.config.sites;
  }
  // legacy template: Files as volume content (folder + single file) + a File inside a writable volume
  const flowFile = { sha256: 'sha256:db624d2bd7d77b12880714a2e2a9d8b24e3f8a55fcc384e54cf31640a620afdc', sizeBytes: 63 };
  const settingsFile = { sha256: 'sha256:cef5ec97f974719f668237e4792780a04579332da2c5ffed2688a4e346e4de0b', sizeBytes: 80 };
  const legacySite = nodeRedSite(db, { entries: [], dirs: [] });
  legacySite.volumes = [
    { volumeSlug: 'nodered-data', label: 'Node-RED data', description: '', backup: true, location: { kind: 'auto' }, kind: 'dir', seed: { kind: 'files', entries: [{ path: 'flows.json', fileSlug: 'nodered-flow' }] } },
    { volumeSlug: 'nodered-settings', label: 'Node-RED settings', description: '', backup: true, location: { kind: 'auto' }, kind: 'file', seed: { kind: 'file', fileSlug: 'nodered-settings' } },
  ];
  legacySite.containers[0]!.volumes = [
    { imageVolume: null, mountPath: '/data', source: { kind: 'volume', volumeSlug: 'nodered-data' }, readOnly: false },
    { imageVolume: null, mountPath: '/data/lib/defaults.js', source: { kind: 'file', fileSlug: 'nodered-settings' }, readOnly: true },
  ];
  db.templates[VC_LEGACY_TEMPLATE] = {
    createdAt: ago(90 * DAY),
    createdBy: ADMIN,
    updatedAt: ago(90 * DAY),
    updatedBy: ADMIN,
    templateId: VC_LEGACY_TEMPLATE,
    slug: 'legacy-files-as-volume-content',
    label: 'Legacy (Files as volume content)',
    icon: 'lucide:archive',
    description: 'Made before §21 #101: volumes take their starter content from Files, and a File is mounted inside a writable volume.',
    version: 1,
    variables: [],
    secrets: [],
    files: [
      { fileSlug: 'nodered-flow', label: 'Node-RED flows', description: 'starter flows.json', mime: 'application/json', editor: 'text', maxBytes: 1048576, content: flowFile, render: 'none', location: { kind: 'auto' } },
      { fileSlug: 'nodered-settings', label: 'Node-RED settings', description: 'settings.js', mime: 'application/javascript', editor: 'text', maxBytes: 65536, content: settingsFile, render: 'none', location: { kind: 'auto' } },
    ],
    sites: [{ siteSlug: 'site-01', label: 'Server', config: legacySite }],
  } as Template;

  // template
  db.templates[VC_TEMPLATE] = {
    createdAt: ago(60 * DAY),
    createdBy: ADMIN,
    updatedAt: ago(60 * DAY),
    updatedBy: ADMIN,
    templateId: VC_TEMPLATE,
    slug: 'node-red-volume-content',
    label: 'Node-RED (volume content)',
    icon: 'lucide:workflow',
    description: 'node-red with a folder volume that has its own content (flows, settings, client code) and persists to the cloud.',
    version: 2,
    variables: [],
    secrets: [],
    files: [],
    sites: [{ siteSlug: VC_SITE, label: 'Flow server', config: nodeRedSite(db, draftSeed) }],
  } as Template;

  // deployment (head rev 1 = headSeed, draft = draftSeed)
  const headCfg: Config = { variables: [], secrets: [], files: [], sites: [{ siteSlug: VC_SITE, label: 'Flow server', config: nodeRedSite(db, headSeed) }] };
  const rev1: Revision = { revId: ulid(Date.now() - 3 * DAY), seq: 1, parentRevId: null, origin: 'cloud', actor: ADMIN, createdAt: ago(3 * DAY), config: headCfg, hash: pseudoHash(headCfg), catalog: buildCatalog(db, headCfg), catalogHash: '', summary: 'Node-RED with persisted flows', kind: 'change', rollbackOf: null };
  rev1.catalogHash = pseudoHash(rev1.catalog);
  const draft: Config = structuredClone(headCfg);
  draft.sites[0]!.config.volumes[0]!.seed = { kind: 'tree', ...draftSeed };
  db.deployments[VC_DEPLOYMENT] = {
    createdAt: ago(4 * DAY),
    createdBy: ADMIN,
    updatedAt: ago(HOUR),
    updatedBy: ADMIN,
    deploymentId: VC_DEPLOYMENT,
    slug: 'node-red-plant',
    label: 'Node-RED plant',
    image: null,
    tagSlugs: ['gov'],
    managerAccountUids: [],
    srcTemplate: null,
    draft,
    draftVersion: 3,
    head: { revId: rev1.revId, seq: 1, hash: rev1.hash, reportedAt: ago(3 * DAY - 20 * MIN) },
    accessKeyHash: '$argon2id$mock',
    accessKey: null,
    accessKeyVersion: 1,
    accessKeyRotatedAt: ago(4 * DAY),
    accessKeyRotatedBy: ADMIN,
    agentEpoch: 4,
    cloudActivitySeq: 1,
    deleted: false,
  } as Deployment;
  db.revisions[VC_DEPLOYMENT] = [rev1];
  db.proposals[VC_DEPLOYMENT] = [];
  db.installTokens[VC_DEPLOYMENT] = [];
  db.transfers[VC_DEPLOYMENT] = [];
  db.activities[VC_DEPLOYMENT] = [{ activityId: '_cloud:1', deploymentId: VC_DEPLOYMENT, siteSlug: '_cloud', seq: 1, ts: ago(4 * DAY), actor: ADMIN, origin: 'cloud', type: 'deployment.created', summary: 'Deployment Node-RED plant created', data: { label: 'Node-RED plant' }, prevHash: null, hash: pseudoHash('vc1'), uploadedVia: null, uploadedAt: null }];
  const tplAgent = Object.values(db.agents['DepAcmePlant00000001'] ?? {})[0];
  db.agents[VC_DEPLOYMENT] = {};
  if (tplAgent) {
    const a = structuredClone(tplAgent);
    a.siteSlug = VC_SITE;
    a.agentVersion = '0.11.1-10090100';
    a.headRevId = rev1.revId;
    a.hostname = 'opm-flows-01';
    a.ip = a.advertiseHost = '10.0.4.20';
    a.detectedAddresses = ['10.0.4.20'];
    a.lastSeenAt = now();
    db.agents[VC_DEPLOYMENT]![VC_SITE] = a;
  }

  // cloud copy: #1 snapshot (after-apply), #2 edit, #3 snapshot (auto), pending push of #2
  const live1 = headSeed;
  const t1 = normalizeVolumeTree({ entries: live1.entries.map((e) => ({ ...e, render: 'none' as const })), dirs: live1.dirs });
  const t2 = normalizeVolumeTree({ entries: t1.entries.map((e) => (e.path === 'flows.json' ? entry('flows.json', f2) : e)), dirs: t1.dirs });
  const t3 = normalizeVolumeTree({ entries: [...t1.entries.map((e) => (e.path === 'flows.json' ? entry('flows.json', fs) : e)), entry('lib/extra.js', ex)], dirs: t1.dirs });
  const doc: VcDoc = { versions: [], push: null, snapshotRequest: null, live: null, liveTree: t3, updatedAt: null };
  const ctx = { deploymentId: VC_DEPLOYMENT, siteSlug: VC_SITE, volumeSlug: VC_VOLUME };
  const v1 = await addVersion(doc, ctx, t1, 'snapshot', { trigger: 'after-apply', createdBy: `agent:${VC_SITE}`, createdAt: ago(3 * DAY - 18 * MIN), excludedCount: 214 });
  const v2 = await addVersion(doc, ctx, t2, 'edit', { createdBy: ADMIN, createdAt: ago(DAY + 2 * HOUR), baseVersionId: v1.versionId, note: 'v2 ingest endpoint + unit' });
  const v3 = await addVersion(doc, ctx, t3, 'snapshot', { trigger: 'auto', createdBy: `agent:${VC_SITE}`, createdAt: ago(5 * HOUR), excludedCount: 231 });
  doc.push = { pushId: ulid(Date.now() - DAY), siteSlug: VC_SITE, volumeSlug: VC_VOLUME, versionId: v2.versionId, versionSeq: v2.seq, treeHash: v2.treeHash, baseVersionId: v1.versionId, baseTreeHash: v1.treeHash, restart: true, note: 'new ingest API', status: 'pending', createdAt: ago(DAY + HOUR), createdBy: ADMIN, resolvedAt: null, resolvedBy: null, result: null };
  doc.live = { volumeSlug: VC_VOLUME, state: 'conflict', liveTreeHash: v3.treeHash, snapshotTreeHash: v3.treeHash, liveChanged: false, queued: 0, scannedAt: ago(40_000), lastError: null, reportedAt: ago(30_000) };
  doc.updatedAt = ago(30_000);
  db.vc = { ...(db.vc ?? {}), [VC_DEPLOYMENT]: { [`${VC_SITE}:${VC_VOLUME}`]: doc } };
}

async function addVersion(
  doc: VcDoc,
  ctx: { deploymentId: string; siteSlug: string; volumeSlug: string },
  tree: VolumeTree,
  origin: VolumeContentVersion['origin'],
  o: { trigger?: VolumeContentVersion['trigger']; createdBy: string; createdAt?: string; baseVersionId?: string | null; restoredFromVersionId?: string | null; note?: string; excludedCount?: number; requestId?: string | null; pushId?: string | null },
): Promise<StoredVersion> {
  const t = normalizeVolumeTree(tree);
  const prev = doc.versions[doc.versions.length - 1] ?? null;
  const s = treeStats(t);
  const createdAt = o.createdAt ?? now();
  const v: StoredVersion = {
    versionId: ulid(Date.parse(createdAt)),
    seq: (prev?.seq ?? 0) + 1,
    origin,
    treeHash: await treeHashOf(t),
    createdAt,
    createdBy: o.createdBy,
    deploymentId: ctx.deploymentId,
    siteSlug: ctx.siteSlug,
    volumeSlug: ctx.volumeSlug,
    trigger: origin === 'snapshot' ? (o.trigger ?? 'auto') : null,
    fileCount: s.fileCount,
    dirCount: s.dirCount,
    totalBytes: s.totalBytes,
    baseVersionId: o.baseVersionId ?? null,
    restoredFromVersionId: o.restoredFromVersionId ?? null,
    note: o.note ?? '',
    excludedCount: o.excludedCount ?? 0,
    skippedCount: 0,
    skipped: [],
    agentSnapshotId: origin === 'snapshot' ? ulid() : null,
    requestId: o.requestId ?? null,
    pushId: o.pushId ?? null,
    changes: countTreeChanges(prev?.tree ?? null, t),
    tree: t,
  };
  doc.versions.push(v);
  doc.updatedAt = createdAt;
  return v;
}

// ───────────────────────────────────────────── handlers
interface VcCtx {
  params: Record<string, string>;
  query: URLSearchParams;
  body: unknown;
  me: Account;
  db: MockDb;
}
export interface VcDeps {
  err: (code: 'not-found' | 'invalid' | 'conflict' | 'forbidden', message: string, details?: Record<string, unknown>) => Error;
  dep: (c: VcCtx) => Deployment;
  needDep: (c: VcCtx, d: Deployment, resource: 'deployment.config', action: 'read' | 'write' | 'publish') => void;
  cloudActivity: (c: VcCtx, d: Deployment, type: string, summary: string, data: Record<string, unknown>) => void;
}
const summary = (v: StoredVersion | null) => (v ? { versionId: v.versionId, seq: v.seq, origin: v.origin, treeHash: v.treeHash, createdAt: v.createdAt, createdBy: v.createdBy } : null);
const strip = (v: StoredVersion): VolumeContentVersion => {
  const { tree: _t, ...rest } = v;
  void _t;
  return rest;
};
function draftVolume(d: Deployment, site: string, vol: string) {
  return d.draft.sites.find((s) => s.siteSlug === site)?.config.volumes?.find((v) => v.volumeSlug === vol) ?? null;
}
function headPersist(db: MockDb, d: Deployment, site: string, vol: string): boolean {
  const rev = d.head ? db.revisions[d.deploymentId]?.find((r) => r.revId === d.head!.revId) : null;
  return !!rev?.config.sites.find((s) => s.siteSlug === site)?.config.volumes?.find((v) => v.volumeSlug === vol)?.persist;
}
function docOf(db: MockDb, depId: string, site: string, vol: string, create: boolean): VcDoc | null {
  const vdb = db as VcDb;
  const all = (vdb.vc ??= {});
  const per = (all[depId] ??= {});
  const k = `${site}:${vol}`;
  if (!per[k] && create) per[k] = { versions: [], push: null, snapshotRequest: null, live: null, liveTree: { entries: [], dirs: [] }, updatedAt: null };
  return per[k] ?? null;
}
function stateOf(db: MockDb, d: Deployment, site: string, vol: string, doc: VcDoc | null): VolumeContentState {
  const versions = doc?.versions ?? [];
  const latest = versions[versions.length - 1] ?? null;
  const lastSnap = [...versions].reverse().find((v) => v.origin === 'snapshot') ?? null;
  const s: VolumeContentState = {
    deploymentId: d.deploymentId,
    siteSlug: site,
    volumeSlug: vol,
    persist: draftVolume(d, site, vol)?.persist ?? null,
    persistInHead: headPersist(db, d, site, vol),
    latest: summary(latest),
    lastSnapshot: summary(lastSnap),
    versionCount: versions.length,
    push: doc?.push ?? null,
    snapshotRequest: doc?.snapshotRequest ?? null,
    live: doc?.live ?? null,
    sync: 'in-sync',
    updatedAt: doc?.updatedAt ?? null,
  };
  s.sync = cloudSyncState(s);
  return s;
}
/** the site answers an open snapshot request after ~1.5 s (manual-cloud snapshot of its live tree) */
async function simulate(doc: VcDoc, ctx: { deploymentId: string; siteSlug: string; volumeSlug: string }) {
  const r = doc.snapshotRequest;
  if (!r || Date.now() - Date.parse(r.createdAt) < 1500) return;
  doc.snapshotRequest = null;
  const hash = await treeHashOf(doc.liveTree);
  const last = [...doc.versions].reverse().find((v) => v.origin === 'snapshot');
  if (!last || last.treeHash !== hash) await addVersion(doc, ctx, doc.liveTree, 'snapshot', { trigger: 'manual-cloud', createdBy: `agent:${ctx.siteSlug}`, requestId: r.requestId, excludedCount: 231 });
  doc.live = { volumeSlug: ctx.volumeSlug, state: 'in-sync', liveTreeHash: hash, snapshotTreeHash: hash, liveChanged: false, queued: 0, scannedAt: now(), lastError: null, reportedAt: now() };
  if (doc.push?.status === 'pending') doc.live.state = doc.push.baseTreeHash === hash ? 'cloud-ahead' : 'conflict';
}
function blobExists(db: MockDb, sha: string) {
  const h = sha.replace(/^sha256:/, '');
  return memBlobs.has(h) || db.blobs[h] !== undefined;
}

export function volumeContentHandlers(x: VcDeps): Record<string, (c: VcCtx) => unknown> {
  const key = (c: VcCtx) => ({ deploymentId: c.params.deploymentId!, siteSlug: c.params.siteSlug!, volumeSlug: c.params.volumeSlug! });
  /** the volume must exist in the draft or have stored content */
  const target = async (c: VcCtx, action: 'read' | 'write' | 'publish') => {
    const d = x.dep(c);
    x.needDep(c, d, 'deployment.config', action);
    const k = key(c);
    const exists = !!draftVolume(d, k.siteSlug, k.volumeSlug);
    const doc = docOf(c.db, d.deploymentId, k.siteSlug, k.volumeSlug, exists);
    if (!doc) throw x.err('not-found', `volume ${k.volumeSlug} of site ${k.siteSlug} not found`);
    await simulate(doc, k);
    return { d, k, doc };
  };
  const invalid = (issues: ValidationIssue[], message = 'validation failed', extra: Record<string, unknown> = {}) => x.err('invalid', message, { errors: issues, ...extra });
  return {
    listVolumeContent: async (c) => {
      const d = x.dep(c);
      x.needDep(c, d, 'deployment.config', 'read');
      const items: VolumeContentState[] = [];
      const seen = new Set<string>();
      for (const s of d.draft.sites)
        for (const v of s.config.volumes ?? [])
          if (v.persist) {
            seen.add(`${s.siteSlug}:${v.volumeSlug}`);
            items.push(stateOf(c.db, d, s.siteSlug, v.volumeSlug, docOf(c.db, d.deploymentId, s.siteSlug, v.volumeSlug, false)));
          }
      for (const [k, doc] of Object.entries((c.db as VcDb).vc?.[d.deploymentId] ?? {}))
        if (!seen.has(k) && doc.versions.length) {
          const [site, vol] = k.split(':') as [string, string];
          items.push(stateOf(c.db, d, site, vol, doc));
        }
      return { items, nextCursor: null };
    },
    getVolumeContent: async (c) => {
      const { d, k, doc } = await target(c, 'read');
      return stateOf(c.db, d, k.siteSlug, k.volumeSlug, doc);
    },
    listVolumeContentVersions: async (c) => {
      const { doc } = await target(c, 'read');
      const limit = Math.min(200, Number(c.query.get('limit') ?? 50) || 50);
      return { items: [...doc.versions].reverse().slice(0, limit).map(strip), nextCursor: null };
    },
    getVolumeContentVersion: async (c) => {
      const { doc } = await target(c, 'read');
      const v = doc.versions.find((y) => y.versionId === c.params.versionId);
      if (!v) throw x.err('not-found', 'version not found');
      return { version: strip(v), tree: v.tree };
    },
    createVolumeContentVersion: async (c) => {
      const { d, k, doc } = await target(c, 'write');
      const b = (c.body ?? {}) as { baseVersionId?: string | null; tree?: VolumeTree; note?: string };
      const latest = doc.versions[doc.versions.length - 1] ?? null;
      if ((b.baseVersionId ?? null) !== (latest?.versionId ?? null)) throw x.err('conflict', `version ${latest?.seq} is newer than the one you edited`, { latest: summary(latest) });
      const issues = volumeTreeIssues(b.tree, false);
      if (issues.length) throw invalid(issues.map((i) => ({ path: `tree${i.path ? `.entries[${i.path}]` : ''}`, code: i.code, message: i.message, severity: 'error' as const })));
      const tree = normalizeVolumeTree(b.tree!);
      if ((b.note ?? '').length > VOLUME_CONTENT_LIMITS.noteMax) throw invalid([{ path: 'note', code: 'V_LENGTH', message: `at most ${VOLUME_CONTENT_LIMITS.noteMax} characters`, severity: 'error' }]);
      const ex = compileExcludes(draftVolume(d, k.siteSlug, k.volumeSlug)?.persist?.exclude ?? []);
      const bad = tree.entries.filter((e) => isExcluded(e.path, false, ex)).map((e) => e.path);
      if (bad.length) throw invalid(bad.map((p) => ({ path: `tree.entries[${p}]`, code: 'V_VOLUME_CONTENT_EXCLUDED', message: `${p} matches an exclude pattern`, severity: 'error' as const })));
      const missing = [...new Set(tree.entries.map((e) => e.sha256))].filter((s) => !blobExists(c.db, s));
      if (missing.length) throw invalid([{ path: 'tree', code: 'V_BLOB_MISSING', message: `${missing.length} blob(s) not uploaded`, severity: 'error' }], 'blobs missing', { missing });
      if (latest && latest.treeHash === (await treeHashOf(tree))) return { version: strip(latest), tree: latest.tree };
      const v = await addVersion(doc, k, tree, 'edit', { createdBy: c.me.uid, baseVersionId: latest?.versionId ?? null, note: (b.note ?? '').trim() });
      x.cloudActivity(c, d, 'volume-content.version-created', `Volume ${k.volumeSlug} (${k.siteSlug}): version ${v.seq} saved`, { siteSlug: k.siteSlug, volumeSlug: k.volumeSlug, versionId: v.versionId, seq: v.seq, origin: 'edit' });
      return { version: strip(v), tree: v.tree };
    },
    restoreVolumeContentVersion: async (c) => {
      const { d, k, doc } = await target(c, 'write');
      const from = doc.versions.find((y) => y.versionId === c.params.versionId);
      if (!from) throw x.err('not-found', 'version not found');
      const note = String((c.body as { note?: string } | undefined)?.note ?? '').trim() || `restore of version ${from.seq}`;
      const latest = doc.versions[doc.versions.length - 1] ?? null;
      if (latest && latest.treeHash === from.treeHash) return { version: strip(latest), tree: latest.tree };
      const v = await addVersion(doc, k, from.tree, 'restore', { createdBy: c.me.uid, baseVersionId: latest?.versionId ?? null, restoredFromVersionId: from.versionId, note });
      x.cloudActivity(c, d, 'volume-content.version-created', `Volume ${k.volumeSlug} (${k.siteSlug}): version ${from.seq} restored as ${v.seq}`, { siteSlug: k.siteSlug, volumeSlug: k.volumeSlug, versionId: v.versionId, seq: v.seq, origin: 'restore' });
      return { version: strip(v), tree: v.tree };
    },
    requestVolumeSnapshot: async (c) => {
      const { d, k, doc } = await target(c, 'write');
      if (!headPersist(c.db, d, k.siteSlug, k.volumeSlug)) throw invalid([{ path: '', code: 'V_VOLUME_PERSIST', message: 'persist is not on in the revision the site runs', severity: 'error' }], 'persist is off in the head');
      if (!doc.snapshotRequest) doc.snapshotRequest = { requestId: ulid(), createdAt: now(), createdBy: c.me.uid };
      d.agentEpoch += 1;
      x.cloudActivity(c, d, 'volume-content.snapshot-requested', `Snapshot of volume ${k.volumeSlug} (${k.siteSlug}) requested`, { siteSlug: k.siteSlug, volumeSlug: k.volumeSlug, requestId: doc.snapshotRequest.requestId });
      return stateOf(c.db, d, k.siteSlug, k.volumeSlug, doc);
    },
    sendVolumeContent: async (c) => {
      const { d, k, doc } = await target(c, 'publish');
      const b = (c.body ?? {}) as { versionId?: string; restart?: boolean; note?: string };
      const v = doc.versions.find((y) => y.versionId === b.versionId);
      if (!v) throw invalid([{ path: 'versionId', code: 'V_REF', message: 'version not found', severity: 'error' }]);
      let base: StoredVersion | null = v;
      while (base && base.origin !== 'snapshot') base = doc.versions.find((y) => y.versionId === base!.baseVersionId) ?? null;
      if (doc.push?.status === 'pending') {
        doc.push.status = 'superseded';
        doc.push.resolvedAt = now();
      }
      const p: VolumeContentPush = { pushId: ulid(), siteSlug: k.siteSlug, volumeSlug: k.volumeSlug, versionId: v.versionId, versionSeq: v.seq, treeHash: v.treeHash, baseVersionId: base?.versionId ?? null, baseTreeHash: base?.treeHash ?? null, restart: b.restart !== false, note: String(b.note ?? '').trim(), status: 'pending', createdAt: now(), createdBy: c.me.uid, resolvedAt: null, resolvedBy: null, result: null };
      doc.push = p;
      d.agentEpoch += 1;
      x.cloudActivity(c, d, 'volume-content.sent', `Volume ${k.volumeSlug}: version ${v.seq} sent to ${k.siteSlug}`, { siteSlug: k.siteSlug, volumeSlug: k.volumeSlug, versionId: v.versionId, seq: v.seq, pushId: p.pushId, restart: p.restart });
      return p;
    },
    cancelVolumeContentPush: async (c) => {
      const { d, k, doc } = await target(c, 'publish');
      if (doc.push?.status !== 'pending') throw x.err('conflict', 'no pending push');
      doc.push = { ...doc.push, status: 'cancelled', resolvedAt: now(), resolvedBy: c.me.uid };
      d.agentEpoch += 1;
      x.cloudActivity(c, d, 'volume-content.push-cancelled', `Volume ${k.volumeSlug} (${k.siteSlug}): pending push cancelled`, { siteSlug: k.siteSlug, volumeSlug: k.volumeSlug, pushId: doc.push.pushId });
      return stateOf(c.db, d, k.siteSlug, k.volumeSlug, doc);
    },
  };
}

// ───────────────────────────────────────────── test hooks (screenshots of every sync state, a site applying a push)
if (typeof window !== 'undefined') {
  const seeded = async () => {
    const db = getDb();
    await ensureVcSeed(db);
    return { db, doc: docOf(db, VC_DEPLOYMENT, VC_SITE, VC_VOLUME, true)! };
  };
  (window as unknown as { __opmMockVc: unknown }).__opmMockVc = {
    async state(name: VolumeContentState['sync']) {
      const { doc } = await seeded();
      const snaps = doc.versions.filter((v) => v.origin === 'snapshot');
      const last = snaps[snaps.length - 1]!;
      const live = (h: string | null, extra: Partial<VolumeSyncReport> = {}) => ({ volumeSlug: VC_VOLUME, state: name, liveTreeHash: h, snapshotTreeHash: last?.treeHash ?? null, liveChanged: false, queued: 0, scannedAt: now(), lastError: null, reportedAt: now(), ...extra });
      const pend = doc.push && (doc.push.status === 'pending' || name === 'cloud-ahead' || name === 'conflict') ? doc.push : null;
      if (name === 'never-snapshotted') {
        doc.versions = doc.versions.filter((v) => v.origin !== 'snapshot').map((v, i) => ({ ...v, seq: i + 1 }));
        doc.push = null;
        doc.live = null;
      } else if (name === 'cloud-ahead' && pend) {
        doc.push = { ...pend, status: 'pending', baseTreeHash: last.treeHash, baseVersionId: last.versionId };
        doc.live = live(last.treeHash);
      } else if (name === 'conflict' && pend) {
        doc.push = { ...pend, status: 'pending' };
        doc.live = live('sha256:' + 'c'.repeat(64), { liveChanged: true });
      } else {
        if (doc.push?.status === 'pending') doc.push = { ...doc.push, status: 'applied', resolvedAt: now(), resolvedBy: 'ops@example.com', result: { resolution: 'fast-forward', written: 2, deleted: 0, keptLive: 0, snapshotId: null, restarted: ['node-red'], error: null, afterVersionId: null } };
        doc.live = name === 'live-changed' ? live('sha256:' + 'd'.repeat(64), { liveChanged: true }) : name === 'pending-upload' ? live(last.treeHash, { queued: 2 }) : name === 'error' ? live(last.treeHash, { lastError: 'snapshot refused: 61.2 MiB is over the 50 MiB cap (biggest: node-red-backup.tar 40 MiB)' }) : live(last.treeHash);
      }
      saveNow();
    },
    async apply() {
      const { db, doc } = await seeded();
      const p = doc.push;
      if (p?.status !== 'pending') return;
      const v = doc.versions.find((y) => y.versionId === p.versionId)!;
      doc.liveTree = v.tree;
      const after = await addVersion(doc, { deploymentId: VC_DEPLOYMENT, siteSlug: VC_SITE, volumeSlug: VC_VOLUME }, v.tree, 'snapshot', { trigger: 'after-apply', createdBy: `agent:${VC_SITE}`, pushId: p.pushId });
      doc.push = { ...p, status: 'applied', resolvedAt: now(), resolvedBy: 'ops@example.com', result: { resolution: 'overwrite', written: 2, deleted: 1, keptLive: 0, snapshotId: ulid(), restarted: p.restart ? ['node-red'] : [], error: null, afterVersionId: after.versionId } };
      doc.live = { volumeSlug: VC_VOLUME, state: 'in-sync', liveTreeHash: after.treeHash, snapshotTreeHash: after.treeHash, liveChanged: false, queued: 0, scannedAt: now(), lastError: null, reportedAt: now() };
      void db;
      saveNow();
    },
  };
}
