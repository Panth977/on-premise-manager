/**
 * §21 #101 mock of VOLUME_SYNC_API (/_opm/api/sites/{site}/volume-sync/…): persist-to-cloud volumes, one per sync state.
 * The LIVE tree is the tools mock tree of the volume (lib/mock/tools.ts) — edits in Docker › Volumes › Files change the
 * live state, an apply writes into it. Base / cloud trees + their file texts live here.
 *
 *   app/hello-api-data     in sync            app/api-env        waiting to upload (queued)
 *   app/hello-api-2-data   no snapshot yet    db/redis-data      error (over the cap; Snapshot now → too-large)
 *   northwind/nodered-data   conflict (text + binary conflicts, cloud-only + live-only changes)
 *   northwind/ez-integrations  cloud ahead (clean fast-forward)       northwind/ez-scripts  live changed
 *
 * Knob: localStorage `opm.mock.vsyncLiveMoves=1` → the next apply answers 409 conflict ("live changed since the plan")
 * after adding a file to the live tree (the GUI re-plans).
 */
import type { ApproveStartResponse, FastForwardUse, SiteConfig } from '@opm/shared/types';
import {
  filterExcluded,
  mergeVolumePush,
  normalizeVolumeTree,
  planVolumePush,
  volumeTreeHashInput,
  type VolumeContentPush,
  type VolumeContentPushResult,
  type VolumeLocalSnapshot,
  type VolumePlanKind,
  type VolumePushPlan,
  type VolumePushResolution,
  type VolumeSnapshotTrigger,
  type VolumeSyncApplyRequest,
  type VolumeSyncState,
  type VolumeSyncStatus,
} from '@opm/shared/volume-content';
import type { VolumePersist, VolumeTree, VolumeTreeEntry } from '@opm/shared/types';
import { fakeHex, fakeSha, mockUlid, iso } from './data';
import { mockVolume, type MockNode } from './tools';

type Json = Record<string, unknown>;
type MockSessionLike = { sessionId: string };
export interface VolSyncDeps {
  requireSession: () => MockSessionLike;
  requireActor: (h: Headers, s: MockSessionLike) => string;
  json: (status: number, body: unknown) => Response;
  fail: (code: string, message: string, details?: Json) => never;
  reach: (site: string) => void;
  head: () => { config: { sites: { siteSlug: string; config: SiteConfig }[] } } | null;
  addActivity: (site: string, type: string, summary: string, actor: string | null, data?: Json) => void;
  snapshot: (site: string, volumeSlug: string, actor: string, note: string) => string;
  users: (site: string, volumeSlug: string) => { slug: string; running: boolean }[];
  updateRunning: () => boolean;
  guard: (site: string, volumeSlug: string) => void;
  startIntent: (site: string, volumeSlug: string, actor: string, sessionId: string) => ApproveStartResponse;
  useIntent: (site: string, volumeSlug: string, intentId: string, sessionId: string, actor: string) => FastForwardUse | null;
  ffSuffix: (ff: FastForwardUse | null) => string;
}

const MiB = 1024 * 1024;
const enc = new TextEncoder();
const now = () => Date.now();
/** content store: sha → text (binary = null + size) */
const blobs = new Map<string, { text: string | null; size: number }>();
const shaText = (t: string) => {
  const sha = fakeSha(t);
  blobs.set(sha, { text: t, size: enc.encode(t).length });
  return sha;
};
const shaBin = (size: number, tag: string) => {
  const sha = fakeSha(`bin:${tag}:${size}`);
  blobs.set(sha, { text: null, size });
  return sha;
};
const fileT = (path: string, text: string, mode = '0644'): VolumeTreeEntry => ({ path, sha256: shaText(text), size: enc.encode(text).length, mode, render: 'none' });
const fileB = (path: string, size: number, tag: string, mode = '0644'): VolumeTreeEntry => ({ path, sha256: shaBin(size, tag), size, mode, render: 'none' });
const treeHash = (t: VolumeTree) => `sha256:${fakeHex(volumeTreeHashInput(t))}`;

// ───────────────────────── the live tree = the tools mock tree
function nodeSha(n: Extract<MockNode, { type: 'file' }>, path: string): string {
  if (n.text !== undefined && !n.binary) return shaText(n.text);
  const tag = (n as { tag?: string }).tag ?? path;
  return shaBin(n.size, tag);
}
function liveRaw(site: string, slug: string): VolumeTree {
  const v = mockVolume(site, slug);
  const entries: VolumeTreeEntry[] = [];
  const dirs: string[] = [];
  const walk = (n: MockNode, p: string) => {
    if (n.type === 'file') entries.push({ path: p, sha256: nodeSha(n, p), size: n.size, mode: (n as { mode?: string }).mode ?? '0644', render: 'none' });
    else if (n.type === 'dir') {
      const kids = Object.entries(n.children);
      if (!kids.length && p) dirs.push(p);
      for (const [k, c] of kids) walk(c, p ? `${p}/${k}` : k);
    }
  };
  if (v) walk(v.root, '');
  return normalizeVolumeTree({ entries, dirs });
}
function liveTree(site: string, slug: string, persist: VolumePersist): { tree: VolumeTree; excluded: number } {
  const raw = liveRaw(site, slug);
  const tree = filterExcluded(raw, persist.exclude);
  return { tree, excluded: raw.entries.length - tree.entries.length };
}
/** write the target tree into the tools mock tree (excluded paths untouched) */
function converge(site: string, slug: string, persist: VolumePersist, target: VolumeTree): { written: number; deleted: number } {
  const v = mockVolume(site, slug);
  if (!v || v.root.type !== 'dir') return { written: 0, deleted: 0 };
  const live = liveTree(site, slug, persist).tree;
  const L = new Map(live.entries.map((e) => [e.path, e] as const));
  const T = new Map(target.entries.map((e) => [e.path, e] as const));
  let written = 0;
  let deleted = 0;
  const dirOf = (p: string, create: boolean): Extract<MockNode, { type: 'dir' }> | null => {
    let n = v.root as Extract<MockNode, { type: 'dir' }>;
    for (const seg of p.split('/').filter(Boolean)) {
      let c = n.children[seg];
      if (!c || c.type !== 'dir') {
        if (!create) return null;
        c = { type: 'dir', children: {} };
        n.children[seg] = c;
      }
      n = c as Extract<MockNode, { type: 'dir' }>;
    }
    return n;
  };
  for (const [p] of L) {
    if (T.has(p)) continue;
    const d = dirOf(p.slice(0, Math.max(0, p.lastIndexOf('/'))), false);
    if (d) delete d.children[p.slice(p.lastIndexOf('/') + 1)];
    deleted++;
  }
  for (const [p, e] of T) {
    const l = L.get(p);
    if (l && l.sha256 === e.sha256 && l.mode === e.mode) continue;
    const b = blobs.get(e.sha256);
    const d = dirOf(p.slice(0, Math.max(0, p.lastIndexOf('/'))), true)!;
    const name = p.slice(p.lastIndexOf('/') + 1);
    d.children[name] = b?.text != null ? ({ type: 'file', size: e.size, text: b.text, mode: e.mode } as MockNode) : ({ type: 'file', size: e.size, binary: true, tag: e.sha256, mode: e.mode } as MockNode);
    written++;
  }
  for (const dd of target.dirs) dirOf(dd, true);
  return { written, deleted };
}

// ───────────────────────── per-volume state
interface VS {
  site: string;
  slug: string;
  label: string;
  history: VolumeLocalSnapshot[]; // newest first
  snapTree: VolumeTree | null;
  push: VolumeContentPush | null;
  baseTree: VolumeTree | null;
  cloudTree: VolumeTree | null;
  lastError: string | null;
  scannedAt: string;
  seq: number;
}
const S = new Map<string, VS>();
const key = (site: string, slug: string) => `${site}/${slug}`;
function snap(vs: VS, tree: VolumeTree, trigger: VolumeSnapshotTrigger, actor: string | null, agoSec: number, uploaded = true): VolumeLocalSnapshot {
  const totalBytes = tree.entries.reduce((n, e) => n + e.size, 0);
  const s: VolumeLocalSnapshot = { snapshotId: mockUlid(now() - agoSec * 1000), versionId: uploaded ? mockUlid(now() - agoSec * 1000 + 50) : null, seq: uploaded ? ++vs.seq : null, treeHash: treeHash(tree), trigger, takenAt: iso(now() - agoSec * 1000), uploadedAt: uploaded ? iso(now() - agoSec * 1000 + 4000) : null, fileCount: tree.entries.length, totalBytes, actor, error: null };
  vs.history.unshift(s);
  vs.snapTree = tree;
  return s;
}
function pushOf(vs: VS, cloud: VolumeTree, base: VolumeTree | null, by: string, note: string, restart: boolean, agoSec: number): VolumeContentPush {
  vs.cloudTree = cloud;
  vs.baseTree = base;
  return { pushId: mockUlid(now() - agoSec * 1000), siteSlug: vs.site, volumeSlug: vs.slug, versionId: mockUlid(now() - agoSec * 1000 - 10), versionSeq: vs.seq + 2, treeHash: treeHash(cloud), baseVersionId: base ? mockUlid(now() - 86400_000) : null, baseTreeHash: base ? treeHash(base) : null, restart, note, status: 'pending', createdAt: iso(now() - agoSec * 1000), createdBy: by, resolvedAt: null, resolvedBy: null, result: null };
}
function persistOf(d: VolSyncDeps, site: string, slug: string): VolumePersist | null {
  return d.head()?.config.sites.find((s) => s.siteSlug === site)?.config.volumes.find((v) => v.volumeSlug === slug)?.persist ?? null;
}

// ── nodered-data story: base = what the cloud last saw; live and cloud both changed flows.json differently
const FLOWS_BASE = '[\n  { "id": "f1", "type": "tab", "label": "Device types" },\n  { "id": "n1", "type": "http in", "z": "f1", "url": "/api/v0002/device/types", "method": "get" }\n]\n';
const FLOWS_LIVE = '[\n  { "id": "f1", "type": "tab", "label": "Device types" },\n  { "id": "n1", "type": "http in", "z": "f1", "url": "/api/v0002/device/types", "method": "get" },\n  { "id": "n7", "type": "mqtt in", "z": "f1", "topic": "site/+/alarm", "broker": "b1" }\n]\n';
const FLOWS_CLOUD = '[\n  { "id": "f1", "type": "tab", "label": "Device types (v2)" },\n  { "id": "n1", "type": "http in", "z": "f1", "url": "/api/v0002/device/types", "method": "get" },\n  { "id": "n2", "type": "function", "z": "f1", "name": "normalize", "func": "return msg;" }\n]\n';
const SETTINGS_BASE = "module.exports = {\n  uiPort: 1880,\n  httpNodeRoot: '/x/nodered',\n};\n";
const SETTINGS_CLOUD = "module.exports = {\n  uiPort: 1880,\n  httpNodeRoot: '/x/nodered',\n  logging: { console: { level: 'info' } },\n};\n";
const ACME = "// ACME webhook integration (edited in the cloud)\nmodule.exports = async function (msg) {\n  msg.payload = { device: msg.topic.split('/')[1], at: Date.now() };\n  return msg;\n};\n";
const README = '# Node-RED data\n\nFlows of the client integration.\n';
const LOCAL = "// added on site by the integrator\nmodule.exports = { retry: 3 };\n";

function nodeT(text: string): MockNode {
  return { type: 'file', size: enc.encode(text).length, text };
}
function setLive(site: string, slug: string, children: Record<string, MockNode>) {
  const v = mockVolume(site, slug);
  if (v && v.root.type === 'dir') {
    for (const k of Object.keys(v.root.children)) delete v.root.children[k];
    Object.assign(v.root.children, children);
  }
}
let seeded = false;
/** set up the states + live trees (index.ts calls it at load so the tools volume list shows the same trees) */
export function seedAll(d: VolSyncDeps) {
  if (seeded) return;
  seeded = true;
  const mk = (site: string, slug: string, label: string): VS => {
    const vs: VS = { site, slug, label, history: [], snapTree: null, push: null, baseTree: null, cloudTree: null, lastError: null, scannedAt: iso(now() - 20_000), seq: 0 };
    S.set(key(site, slug), vs);
    return vs;
  };
  const p = (site: string, slug: string) => persistOf(d, site, slug) ?? { exclude: [], maxBytes: 50 * MiB, auto: true };
  // in sync: two uploaded snapshots, the newest = live
  {
    const vs = mk('app', 'hello-api-data', 'API data');
    const live = liveTree('app', 'hello-api-data', p('app', 'hello-api-data')).tree;
    snap(vs, normalizeVolumeTree({ entries: live.entries.filter((e) => e.path !== 'counter.json'), dirs: live.dirs }), 'auto', null, 86400 * 2);
    snap(vs, live, 'auto', null, 3600 * 5);
  }
  // waiting to upload: one uploaded, the newest queued
  {
    const vs = mk('app', 'api-env', 'API env config');
    const live = liveTree('app', 'api-env', p('app', 'api-env')).tree;
    snap(vs, normalizeVolumeTree({ entries: live.entries.filter((e) => e.path !== 'production.ts'), dirs: [] }), 'auto', null, 86400);
    snap(vs, live, 'manual-agent', 'ops@acme.example', 600, false);
  }
  mk('app', 'hello-api-2-data', 'API 2 scratch data');
  {
    const vs = mk('db', 'redis-data', 'Redis data');
    vs.lastError = 'snapshot refused: 85.0 MiB of files is over the cap of 50.0 MiB (persist.maxBytes) — biggest: appendonly.aof 61.0 MiB, dump.rdb 24.0 MiB';
  }
  // conflict
  {
    const vs = mk('northwind', 'nodered-data', 'Node-RED data');
    setLive('northwind', 'nodered-data', {
      'flows.json': nodeT(FLOWS_LIVE),
      'settings.js': nodeT(SETTINGS_BASE),
      'README.md': nodeT(README),
      lib: { type: 'dir', children: { 'local-only.js': nodeT(LOCAL) } },
      assets: { type: 'dir', children: { 'logo.png': { type: 'file', size: 48_213, binary: true, tag: 'logo-live' } as MockNode } },
      node_modules: { type: 'dir', children: { '.package-lock.json': nodeT('{}\n') } },
      'node-red.log': nodeT('10 Oct 03:00:01 - [info] Started flows\n'),
    });
    const base = normalizeVolumeTree({ entries: [fileT('flows.json', FLOWS_BASE), fileT('settings.js', SETTINGS_BASE), fileT('README.md', README), fileB('assets/logo.png', 41_200, 'logo-base')], dirs: ['lib'] });
    snap(vs, base, 'auto', null, 86400 * 3);
    const cloud = normalizeVolumeTree({ entries: [fileT('flows.json', FLOWS_CLOUD), fileT('settings.js', SETTINGS_CLOUD), fileT('lib/integrations/acme.js', ACME), fileB('assets/logo.png', 52_880, 'logo-cloud')], dirs: [] });
    vs.push = pushOf(vs, cloud, base, 'dev@acme.example', 'ACME webhook + normalize step', true, 1800);
  }
  // cloud ahead: live = the last snapshot = the push base
  {
    const vs = mk('northwind', 'ez-integrations', 'Client integrations');
    setLive('northwind', 'ez-integrations', { 'acme.js': nodeT(ACME), 'mapping.json': nodeT('{\n  "temp": "t",\n  "hum": "h"\n}\n') });
    const live = liveTree('northwind', 'ez-integrations', p('northwind', 'ez-integrations')).tree;
    snap(vs, live, 'auto', null, 7200);
    const cloud = normalizeVolumeTree({ entries: [...live.entries.filter((e) => e.path !== 'mapping.json'), fileT('mapping.json', '{\n  "temp": "t",\n  "hum": "h",\n  "pm25": "p"\n}\n'), fileT('vendors/beta.js', 'module.exports = (m) => m;\n')], dirs: [] });
    vs.push = pushOf(vs, cloud, live, 'dev@acme.example', 'add PM2.5 mapping', false, 900);
  }
  // live changed: an older snapshot, then edits on site
  {
    const vs = mk('northwind', 'ez-scripts', 'Ops scripts');
    setLive('northwind', 'ez-scripts', { 'backup.sh': { ...nodeT('#!/bin/sh\nset -e\npg_dump "$DB" > /backups/db.sql\ngzip -f /backups/db.sql\n'), mode: '0755' } as unknown as MockNode, 'rotate.sh': { ...nodeT('#!/bin/sh\nfind /logs -mtime +7 -delete\n'), mode: '0755' } as unknown as MockNode });
    snap(vs, normalizeVolumeTree({ entries: [{ ...fileT('backup.sh', '#!/bin/sh\npg_dump "$DB" > /backups/db.sql\n'), mode: '0755' }], dirs: [] }), 'manual-agent', 'ops@acme.example', 86400 * 4);
  }
}

function status(d: VolSyncDeps, vs: VS): VolumeSyncStatus {
  const persist = persistOf(d, vs.site, vs.slug) ?? { exclude: [], maxBytes: 50 * MiB, auto: true };
  const { tree, excluded } = liveTree(vs.site, vs.slug, persist);
  const lh = treeHash(tree);
  const last = vs.history[0] ?? null;
  const changed = !!vs.snapTree && treeHash(vs.snapTree) !== lh;
  const queued = vs.history.filter((h) => !h.versionId && !h.error).length;
  let state: VolumeSyncState;
  if (vs.lastError) state = 'error';
  else if (vs.push) {
    state = vs.push.baseTreeHash && lh === vs.push.baseTreeHash ? 'cloud-ahead' : lh === vs.push.treeHash ? 'in-sync' : 'conflict';
    // like the agent: no file changed on both sides since a known base → it applies without a choice
    if (state === 'conflict' && vs.baseTree && vs.cloudTree && !planVolumePush(vs.baseTree, tree, vs.cloudTree).some((i) => i.kind === 'conflict')) state = 'cloud-ahead';
  }
  else if (!last) state = 'never-snapshotted';
  else if (queued) state = 'pending-upload';
  else if (changed) state = 'live-changed';
  else state = 'in-sync';
  return {
    siteSlug: vs.site,
    volumeSlug: vs.slug,
    label: vs.label,
    persist,
    state,
    lastSnapshot: last,
    live: { treeHash: lh, fileCount: tree.entries.length, totalBytes: tree.entries.reduce((n, e) => n + e.size, 0), excludedCount: excluded, scannedAt: vs.scannedAt, changed: vs.snapTree ? changed : true },
    push: vs.push,
    queued,
    lastError: vs.lastError,
    history: vs.history.slice(0, 20),
    users: d.users(vs.site, vs.slug).map((u) => u.slug),
  };
}
function plan(d: VolSyncDeps, vs: VS): VolumePushPlan {
  if (!vs.push || !vs.cloudTree) d.fail('invalid', `no pending push for volume ${vs.slug}`);
  const persist = persistOf(d, vs.site, vs.slug)!;
  const live = liveTree(vs.site, vs.slug, persist).tree;
  const items = planVolumePush(vs.baseTree, live, vs.cloudTree!);
  const counts: Record<VolumePlanKind, number> = { cloud: 0, live: 0, same: 0, conflict: 0 };
  for (const i of items) counts[i.kind]++;
  const lh = treeHash(live);
  return { pushId: vs.push!.pushId, versionId: vs.push!.versionId, baseKnown: !!vs.baseTree, baseTreeHash: vs.push!.baseTreeHash, liveTreeHash: lh, cloudTreeHash: vs.push!.treeHash, fastForward: !!vs.push!.baseTreeHash && lh === vs.push!.baseTreeHash, upToDate: lh === vs.push!.treeHash, items, counts, exclude: persist.exclude, users: d.users(vs.site, vs.slug).map((u) => u.slug), restart: vs.push!.restart };
}

let liveMoveUsed = false;
export async function volsyncFetch(url: URL, init: RequestInit, d: VolSyncDeps): Promise<Response> {
  await new Promise((r) => setTimeout(r, 60 + Math.random() * 100));
  {
    seedAll(d);
    const m = /^\/_opm\/api\/sites\/([^/]+)\/volume-sync(?:\/([^/]+)(?:\/(.+))?)?$/.exec(url.pathname)!;
    const site = decodeURIComponent(m[1]);
    const slug = m[2] ? decodeURIComponent(m[2]) : null;
    const rest = m[3] ?? '';
    const method = (init.method ?? 'GET').toUpperCase();
    const s = d.requireSession();
    d.reach(site);
    const body = typeof init.body === 'string' && init.body ? (JSON.parse(init.body) as Json) : init.body instanceof Uint8Array ? (JSON.parse(new TextDecoder().decode(init.body)) as Json) : {};
    if (!d.head()?.config.sites.some((x) => x.siteSlug === site)) d.fail('not-found', `no site ${site}`);
    if (!slug) {
      if (method !== 'GET') d.fail('not-found', 'no such endpoint');
      const items = [...S.values()].filter((v) => v.site === site && persistOf(d, site, v.slug)).map((v) => status(d, v));
      return d.json(200, { items, nextCursor: null });
    }
    const vs = S.get(key(site, slug));
    if (!vs || !persistOf(d, site, slug)) d.fail('not-found', `volume ${slug} is not a persist-to-cloud volume of site ${site}`);
    const v = vs!;
    const persist = persistOf(d, site, slug)!;
    if (method === 'GET' && rest === '') return d.json(200, status(d, v));
    if (method === 'POST' && rest === 'snapshot') {
      const actor = d.requireActor(new Headers(init.headers), s);
      d.guard(site, slug);
      const live = liveTree(site, slug, persist);
      const total = live.tree.entries.reduce((n, e) => n + e.size, 0);
      if (total > persist.maxBytes) {
        const biggest = [...live.tree.entries].sort((a, b) => b.size - a.size).slice(0, 5).map((e) => ({ path: e.path, size: e.size }));
        v.lastError = `snapshot refused: ${(total / MiB).toFixed(1)} MiB of files is over the cap of ${(persist.maxBytes / MiB).toFixed(1)} MiB (persist.maxBytes)`;
        d.addActivity(site, 'volume-content.snapshot-failed', `Snapshot of ${slug} refused: over the size cap`, actor, { volumeSlug: slug, totalBytes: total, maxBytes: persist.maxBytes });
        d.fail('too-large', v.lastError, { totalBytes: total, maxBytes: persist.maxBytes, biggest });
      }
      v.lastError = null;
      v.scannedAt = iso(now());
      if (v.snapTree && treeHash(v.snapTree) === treeHash(live.tree) && v.history[0]) return d.json(201, { snapshot: v.history[0], created: false });
      // uploads at once (the cloud is reachable in the mock); queued snapshots go up with it
      for (const h of v.history) if (!h.versionId && !h.error) Object.assign(h, { versionId: mockUlid(), seq: ++v.seq, uploadedAt: iso(now()) });
      const sn = snap(v, live.tree, 'manual-agent', actor, 0, true);
      d.addActivity(site, 'volume-content.snapshot', `Snapshot of ${slug} (${sn.fileCount} files) — uploaded as version ${sn.seq}`, actor, { volumeSlug: slug, trigger: 'manual-agent', snapshotId: sn.snapshotId, versionId: sn.versionId });
      return d.json(201, { snapshot: sn, created: true });
    }
    if (method === 'GET' && rest === 'plan') return d.json(200, plan(d, v));
    if (method === 'GET' && rest === 'file') {
      const side = url.searchParams.get('side');
      const path = url.searchParams.get('path') ?? '';
      const tree = side === 'live' ? liveTree(site, slug, persist).tree : side === 'cloud' ? v.cloudTree : side === 'base' ? v.baseTree : null;
      if (!tree) d.fail('invalid', `no ${side} tree`);
      const e = tree!.entries.find((x) => x.path === path);
      if (!e) d.fail('not-found', `${path} is not in the ${side} tree`);
      const b = blobs.get(e!.sha256);
      const bytes = b?.text != null ? enc.encode(b.text) : (() => {
        const a = new Uint8Array(Math.min(e!.size, 4096));
        a[0] = 0x89;
        return a;
      })();
      return new Response(bytes as unknown as BodyInit, { status: 200, headers: { 'Content-Type': 'application/octet-stream' } });
    }
    if (method === 'POST' && rest === 'apply/start') {
      const actor = d.requireActor(new Headers(init.headers), s);
      if (!v.push) d.fail('invalid', `no pending push for volume ${slug}`);
      d.guard(site, slug);
      return d.json(201, d.startIntent(site, slug, actor, s.sessionId));
    }
    if (method === 'POST' && rest === 'apply') {
      const actor = d.requireActor(new Headers(init.headers), s);
      const b = body as unknown as VolumeSyncApplyRequest;
      if (!v.push || b.pushId !== v.push.pushId) d.fail('invalid', 'this push is not pending any more (the cloud sent another one or cancelled it) — reload');
      d.guard(site, slug);
      // an apply is a merge (§21 #104 (e)): no intent wait; an intent from an older GUI is still honoured
      const ff = b.intentId ? d.useIntent(site, slug, String(b.intentId), s.sessionId, actor) : null;
      const live0 = liveTree(site, slug, persist).tree;
      if (localStorage.getItem('opm.mock.vsyncLiveMoves') === '1' && !liveMoveUsed) {
        liveMoveUsed = true;
        const vol = mockVolume(site, slug);
        if (vol && vol.root.type === 'dir') vol.root.children['written-meanwhile.txt'] = nodeT('a file the container wrote after the plan\n');
      }
      const live = liveTree(site, slug, persist).tree;
      if (treeHash(live) !== b.liveTreeHash) d.fail('conflict', 'the live volume changed since the plan (a file was written meanwhile) — plan again');
      void live0;
      const p = plan(d, v);
      const resolution: VolumePushResolution = b.resolution;
      let target: VolumeTree;
      try {
        target = mergeVolumePush(v.baseTree, live, v.cloudTree!, resolution, b.choices ?? {});
      } catch (e) {
        d.fail('invalid', e instanceof Error ? e.message : String(e));
      }
      // before-apply upload of live changes, a Backups snapshot, stop users, converge, start users, after-apply snapshot
      if (v.snapTree && treeHash(v.snapTree) !== treeHash(live)) snap(v, live, 'before-apply', actor, 0, true);
      const snapshotId = d.snapshot(site, slug, actor, `before applying cloud version seq ${v.seq + 1}`);
      const users = d.users(site, slug).filter((u) => u.running).map((u) => u.slug);
      const { written, deleted } = converge(site, slug, persist, target!);
      const after = liveTree(site, slug, persist).tree;
      const keptLive = p.items.filter((i) => (i.kind === 'live' && resolution !== 'overwrite') || (i.kind === 'conflict' && resolution === 'per-file' && b.choices?.[i.path] === 'live')).length;
      const sn = snap(v, after, 'after-apply', null, 0, true);
      const kind = p.fastForward ? 'fast-forward' : p.upToDate ? 'up-to-date' : resolution;
      const result: VolumeContentPushResult = { resolution: kind, written, deleted, keptLive, snapshotId, restarted: b.restart ? users : [], error: null, afterVersionId: sn.versionId };
      const push: VolumeContentPush = { ...v.push!, status: 'applied', resolvedAt: iso(now()), resolvedBy: actor, result };
      v.push = null;
      v.baseTree = v.cloudTree = null;
      d.addActivity(site, 'volume-content.applied', `Cloud version applied to ${slug} (${kind}: ${written} written, ${deleted} deleted, ${keptLive} kept live)${d.ffSuffix(ff)}`, actor, { volumeSlug: slug, pushId: push.pushId, ...result, ...(ff ? { fastForward: ff } : {}) });
      return d.json(200, { push, result });
    }
    if (method === 'POST' && rest === 'keep-live') {
      const actor = d.requireActor(new Headers(init.headers), s);
      if (!v.push || body.pushId !== v.push.pushId) d.fail('invalid', 'this push is not pending any more — reload');
      const live = liveTree(site, slug, persist).tree;
      if (!v.snapTree || treeHash(v.snapTree) !== treeHash(live)) snap(v, live, 'manual-agent', actor, 0, true);
      const push: VolumeContentPush = { ...v.push!, status: 'kept-live', resolvedAt: iso(now()), resolvedBy: actor, result: { resolution: 'keep-live', written: 0, deleted: 0, keptLive: plan(d, v).items.length, snapshotId: null, restarted: [], error: null, afterVersionId: v.history[0]?.versionId ?? null } };
      v.push = null;
      v.baseTree = v.cloudTree = null;
      d.addActivity(site, 'volume-content.kept-live', `Kept the live content of ${slug} (cloud version not applied)`, actor, { volumeSlug: slug, pushId: push.pushId });
      return d.json(200, push);
    }
    return d.fail('not-found', `mock: no volume-sync endpoint ${method} ${url.pathname}`);
  }
}
