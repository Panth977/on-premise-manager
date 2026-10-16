/**
 * §21 #106 GitHub mirror worker: drains the queue of ONE git branch at a time (lease on the branch doc), in orderKey
 * order, and pushes with the Git Data API.
 *
 *   commit / volume-snapshot / catch-up  → push every OPM history entry (commits; agent branches: + persist snapshots)
 *                                          after the last pushed one, oldest first (up to the item; catch-up: all).
 *                                          Idempotent: the branch state (_lastOrderKey) + the remote head's trailers
 *                                          (a crash between the ref update and the state write is recovered).
 *   rebuild                              → the whole history from a root commit, force-updated (deterministic shas).
 *   drift / drift-clear                  → uncommitted/<site>: ONE snapshot commit on top of the agent branch head
 *                                          (force), or the ref deleted.
 * A failure stops the branch (order!) with backoff; a rate limit or a refused token pauses the whole mirror.
 */
import {
  branchKey,
  compareUtf16,
  MIRROR_DEFAULT_BRANCH,
  MIRROR_MAX_FILE_BYTES,
  MIRROR_TRAILERS,
  mirrorBackoffSec,
  mirrorBranchDocId,
  mirrorBranchName,
  mirrorCommitMessage,
  mirrorOrderKey,
  mirrorSitePerson,
  parseMirrorTrailers,
  ulid,
  type BranchRef,
  type Commit,
  type Config,
  type ConfigView,
  type ExportNote,
  type ExportPersistSnapshot,
  type ExportResult,
  type MirrorBranchState,
  type MirrorQueueItem,
} from '@opm/shared';
import { logger } from '../ports/index.js';
import { createHash } from 'node:crypto';
import { db, nowIso } from './fire.js';
import { blobs } from '../ports/index.js';
import { env } from './env.js';
import { hexOf } from './util.js';
import { auditSystem } from './audit.js';
import { commitsOf, decodeCommit, bcol } from './branches.js';
import { GithubClient, GithubError, gitBlobSha, type TreeEntry } from './github.js';
import { clientOf, isActive, loadMirror, mcol, repoKeyOf, targetOf, touchBranch, invalidateMirrorCache, type MirrorTarget, type StoredMirror } from './mirror.js';
import { buildRequest, exportTree, newExportCtx, personOf, type ExportCtx } from './mirror-export.js';
import { readTree, vcCol } from '../routes/volume-content.js';

const LEASE_MS = 3 * 60_000;

// ───────────────────────────────────────── history
interface Entry {
  type: 'commit' | 'snapshot';
  id: string;
  seq: number | null;
  createdAt: string;
  orderKey: string;
  /** snapshot */
  site?: string;
  volume?: string;
  treeHash?: string;
  fileCount?: number;
}
async function history(t: MirrorTarget): Promise<Entry[]> {
  const r = t.ref;
  const out: Entry[] = [];
  const bref: BranchRef = r.kind === 'template' ? { kind: 'template', templateId: r.templateId! } : r.kind === 'deployment' ? { kind: 'deployment', deploymentId: r.deploymentId! } : { kind: 'agent', deploymentId: r.deploymentId!, siteSlug: r.siteSlug! };
  const cs = await commitsOf(bref).orderBy('seq', 'asc').select('commitId', 'seq', 'createdAt').get();
  for (const d of cs.docs) {
    const x = d.data();
    out.push({ type: 'commit', id: x.commitId, seq: x.seq, createdAt: x.createdAt, orderKey: mirrorOrderKey(x.createdAt, x.seq, x.commitId) });
  }
  if (r.kind === 'agent') {
    const vols = await vcCol(r.deploymentId!).get();
    for (const v of vols.docs) {
      if (!v.id.startsWith(`${r.siteSlug}:`)) continue;
      const vs = await v.ref.collection('versions').where('origin', '==', 'snapshot').select('versionId', 'seq', 'createdAt', 'treeHash', 'volumeSlug', 'fileCount').get();
      for (const d of vs.docs) {
        const x = d.data();
        out.push({ type: 'snapshot', id: x.versionId, seq: x.seq, createdAt: x.createdAt, orderKey: mirrorOrderKey(x.createdAt, x.seq, x.versionId), site: r.siteSlug!, volume: x.volumeSlug ?? v.id.split(':')[1], treeHash: x.treeHash, fileCount: x.fileCount ?? 0 });
      }
    }
  }
  out.sort((a, b) => (a.orderKey < b.orderKey ? -1 : a.orderKey > b.orderKey ? 1 : 0));
  return out;
}

// ───────────────────────────────────────── pushing
interface Push {
  cl: GithubClient;
  m: StoredMirror;
  x: ExportCtx;
  /** sha256 hex of content → git sha (this run) */
  known: Map<string, string>;
}
const maxFileBytes = (m: StoredMirror) => (env.emulator && m._maxFileBytes ? m._maxFileBytes : MIRROR_MAX_FILE_BYTES);

async function gitShaFor(p: Push, hex: string, bytes: () => Promise<Buffer>): Promise<string> {
  const hit = p.known.get(hex);
  if (hit) return hit;
  const repoKey = repoKeyOf(p.m);
  const cref = mcol.blobs().doc(hex);
  const c = await cref.get();
  if (c.exists && c.data()!.repoKey === repoKey) {
    p.known.set(hex, c.data()!.gitSha);
    return c.data()!.gitSha;
  }
  const b = await bytes();
  const sha = gitBlobSha(b);
  if (!(await p.cl.getBlobExists(sha))) {
    const got = await p.cl.createBlob(b);
    if (got !== sha) throw new Error(`GitHub blob sha ${got} ≠ ${sha}`);
  }
  await cref.set({ repoKey, gitSha: sha, size: b.length, at: nowIso() });
  p.known.set(hex, sha);
  return sha;
}
const sha256Hex = (b: Buffer) => createHash('sha256').update(b).digest('hex');

/** ExportResult → git tree sha (+ notes, incl. skipped big files) */
async function pushTree(p: Push, res: ExportResult): Promise<{ tree: string; notes: ExportNote[] }> {
  const max = maxFileBytes(p.m);
  const notes = [...res.notes];
  const skipped = new Map<string, string[]>();
  const entries: TreeEntry[] = [];
  for (const f of res.files) {
    if (f.size > max) {
      const dir = f.path.includes('/') ? f.path.slice(0, f.path.lastIndexOf('/')) : '';
      skipped.set(dir, [...(skipped.get(dir) ?? []), `${f.path.slice(dir ? dir.length + 1 : 0)}\t${f.size} bytes`]);
      notes.push({ code: 'SKIPPED_LARGE', site: /^sites\/([^/]+)\//.exec(f.path)?.[1] ?? null, message: `${f.path} (${f.size} bytes) is larger than ${max} bytes and was not pushed` });
      continue;
    }
    let sha: string;
    if (f.blob !== undefined) {
      const hex = hexOf(f.blob);
      sha = await gitShaFor(p, hex, async () => { const b = await blobs().get(`files/${hex}`); if (!b) throw Object.assign(new Error(`files/${hex} is not stored`), { code: 404 }); return b; });
    } else {
      const bytes = f.text !== undefined ? Buffer.from(f.text, 'utf8') : f.base64 !== undefined ? Buffer.from(f.base64, 'base64') : Buffer.alloc(0);
      sha = await gitShaFor(p, sha256Hex(bytes), async () => bytes);
    }
    entries.push({ path: f.path, mode: f.mode, type: 'blob', sha });
  }
  for (const [dir, list] of skipped) {
    const text = `# Files OPM did not push to GitHub (larger than ${max} bytes; GitHub refuses files over 100 MB).\n# They exist in OPM (cloud Storage / the site) only.\n${list.sort().join('\n')}\n`;
    const b = Buffer.from(text, 'utf8');
    entries.push({ path: `${dir ? `${dir}/` : ''}.skipped.txt`, mode: '100644', type: 'blob', sha: await gitShaFor(p, sha256Hex(b), async () => b) });
  }
  entries.sort((a, b) => compareUtf16(a.path, b.path));
  return { tree: await p.cl.createTree(entries), notes };
}

async function ensureInitialized(p: Push) {
  if (p.m.initializedAt) return;
  if (await p.cl.isEmpty()) {
    const readme = `# OPM mirror\n\nThis repository is written by the On-Prem Manager (OPM) GitHub mirror.\n\nBranches:\n\n- \`template/<slug>\` — a template\n- \`deployment/<slug>/cloud\` — a deployment (what its sites should run)\n- \`deployment/<slug>/agent/<site>\` — the commits of one site (+ persist-to-cloud volume snapshots)\n- \`deployment/<slug>/uncommitted/<site>\` — changes made on that site outside OPM, not committed yet\n- \`deployment/<slug>/site/<site>\` — a site run with Docker only: written by that machine, never by the mirror\n\nEvery branch runs each site with Docker alone (docker compose + Caddy + Portainer, no agent): the last resort when the agent is not allowed. Open \`sites/<site>/README.md\` of a branch for the exact steps: clone, \`git switch -c deployment/<slug>/site/<site>\`, fill \`site.env\`, \`./deploy.sh\`, push; later \`git merge\` the cloud branch and \`./deploy.sh\` again.\n`;
    await p.cl.putContents('README.md', Buffer.from(readme, 'utf8'), 'OPM mirror: initialize the repository', MIRROR_DEFAULT_BRANCH, { name: 'OPM', email: 'opm@users.noreply.opm' });
  }
  const at = nowIso();
  await mcol.config().set({ initializedAt: at }, { merge: true });
  p.m.initializedAt = at;
}

const branchMeta = (t: MirrorTarget) => ({ kind: t.ref.kind, gitBranch: t.gitBranch, entitySlug: t.entitySlug, label: t.label, templateId: t.ref.templateId, deploymentId: t.ref.deploymentId, siteSlug: t.ref.siteSlug });
const depIdFor = (t: MirrorTarget) => (t.ref.kind === 'template' ? `template-${t.entitySlug}` : t.ref.deploymentId!);
const opmBranchRef = (t: MirrorTarget): BranchRef => (t.ref.kind === 'template' ? { kind: 'template', templateId: t.ref.templateId! } : t.ref.kind === 'deployment' ? { kind: 'deployment', deploymentId: t.ref.deploymentId! } : { kind: 'agent', deploymentId: t.ref.deploymentId!, siteSlug: t.ref.siteSlug! });
const sitesFor = (t: MirrorTarget, c: Config) => (t.ref.kind === 'agent' || t.ref.kind === 'uncommitted' ? [t.ref.siteSlug!] : c.sites.map((s) => s.siteSlug));

/** the persist overlay of an agent branch at an entry: newest snapshot per volume up to (and incl.) orderKey */
async function persistAt(t: MirrorTarget, hist: Entry[], orderKey: string, config: Config, cache: Map<string, Awaited<ReturnType<typeof readTree>>>): Promise<Record<string, Record<string, ExportPersistSnapshot>>> {
  if (t.ref.kind !== 'agent' && t.ref.kind !== 'uncommitted') return {};
  const site = t.ref.siteSlug!;
  const vols = new Set((config.sites.find((s) => s.siteSlug === site)?.config.volumes ?? []).map((v) => v.volumeSlug));
  const newest = new Map<string, Entry>();
  for (const e of hist) if (e.type === 'snapshot' && e.orderKey <= orderKey && vols.has(e.volume!)) newest.set(e.volume!, e);
  const out: Record<string, ExportPersistSnapshot> = {};
  for (const [vol, e] of [...newest].sort((a, b) => compareUtf16(a[0], b[0]))) {
    if (!cache.has(e.treeHash!)) cache.set(e.treeHash!, await readTree(e.treeHash!));
    const tree = cache.get(e.treeHash!);
    if (tree) out[vol] = { versionId: e.id, seq: e.seq ?? 0, createdAt: e.createdAt, treeHash: e.treeHash!, tree };
  }
  return Object.keys(out).length ? { [site]: out } : {};
}

interface Built {
  message: string;
  person: { name: string; email: string; date: string };
  res: ExportResult;
}
/** export + commit metadata of one history entry (null = nothing to push: a snapshot before any commit) */
async function buildEntry(p: Push, t: MirrorTarget, hist: Entry[], e: Entry, commits: Map<string, Commit>, trees: Map<string, Awaited<ReturnType<typeof readTree>>>): Promise<Built | null> {
  const bref = opmBranchRef(t);
  const load = async (id: string) => {
    if (!commits.has(id)) {
      const s = await commitsOf(bref).doc(id).get();
      commits.set(id, await decodeCommit(s.data()!));
    }
    return commits.get(id)!;
  };
  if (e.type === 'commit') {
    const c = await load(e.id);
    const req = await buildRequest(p.x, {
      repository: `${p.m.owner}/${p.m.repo}`,
      branch: branchMeta(t),
      commit: { id: c.commitId, seq: c.seq, kind: c.kind, message: c.message, author: personOf(c.author, t.ref.siteSlug), createdAt: c.createdAt, hash: c.hash, parentId: c.parentId },
      deploymentId: depIdFor(t),
      config: c.config,
      catalog: c.catalog,
      sites: sitesFor(t, c.config),
      persist: await persistAt(t, hist, e.orderKey, c.config, trees),
    });
    const res = await exportTree(req, !!p.m._stubExporter);
    const message = mirrorCommitMessage(c.message, [
      [MIRROR_TRAILERS.commit, c.commitId],
      [MIRROR_TRAILERS.branch, branchKey(bref)],
      [MIRROR_TRAILERS.seq, String(c.seq)],
    ]);
    return { message, person: { ...personOf(c.author, t.ref.siteSlug), date: c.createdAt }, res };
  }
  // snapshot: the config of the newest commit before it
  const base = [...hist].reverse().find((x) => x.type === 'commit' && x.orderKey < e.orderKey);
  if (!base) return null;
  const c = await load(base.id);
  const who = mirrorSitePerson(e.site!);
  const msg = `Volume snapshot ${e.site}/${e.volume} #${e.seq} (${e.fileCount ?? 0} files)`;
  const req = await buildRequest(p.x, {
      repository: `${p.m.owner}/${p.m.repo}`,
    branch: branchMeta(t),
    commit: { id: e.id, seq: e.seq, kind: 'volume-snapshot', message: msg, author: who, createdAt: e.createdAt, hash: e.treeHash ?? null, parentId: c.commitId },
    deploymentId: depIdFor(t),
    config: c.config,
    catalog: c.catalog,
    sites: sitesFor(t, c.config),
    persist: await persistAt(t, hist, e.orderKey, c.config, trees),
  });
  const res = await exportTree(req, !!p.m._stubExporter);
  const message = mirrorCommitMessage(msg, [
    [MIRROR_TRAILERS.volume, e.id],
    [MIRROR_TRAILERS.branch, branchKey(bref)],
  ]);
  return { message, person: { ...who, date: e.createdAt }, res };
}

type BranchDoc = MirrorBranchState & { _lastOrderKey?: string | null; _leaseId?: string | null };
const bref = (gitBranch: string) => mcol.branches().doc(mirrorBranchDocId(gitBranch));
const urls = (m: StoredMirror, gitBranch: string, sha: string | null) => ({ gitUrl: sha ? `https://github.com/${m.owner}/${m.repo}/commit/${sha}` : null, treeUrl: `https://github.com/${m.owner}/${m.repo}/tree/${gitBranch}` });

/** push history entries after the branch state, up to `upto` (null = all). Returns the number of git commits made. */
async function catchUp(p: Push, t: MirrorTarget, upto: string | null, rebuild: boolean): Promise<number> {
  const hist = await history(t);
  const st = (await bref(t.gitBranch).get()).data() as BranchDoc | undefined;
  let last = rebuild ? null : (st?._lastOrderKey ?? null);
  let parent: string | null = rebuild ? null : (st?.gitSha ?? null);
  const remote = await p.cl.getRef(t.gitBranch);
  if (!rebuild && remote && remote !== parent) {
    // recovery: the remote head was written by us but the state write was lost (crash) — adopt it via its trailers
    const rc = await p.cl.getCommit(remote);
    const tr = parseMirrorTrailers(rc?.message ?? '');
    const id = tr[MIRROR_TRAILERS.commit] ?? tr[MIRROR_TRAILERS.volume];
    const hit = id ? hist.find((h) => h.id === id) : undefined;
    if (hit && (!last || hit.orderKey > last)) last = hit.orderKey;
    parent = remote; // never rewrite a remote branch: continue on top of it
  }
  const todo = hist.filter((h) => (!last || h.orderKey > last) && (upto === null || h.orderKey <= upto));
  if (!todo.length && !rebuild) {
    if (remote !== (st?.gitSha ?? null) || (last ?? null) !== (st?._lastOrderKey ?? null)) await bref(t.gitBranch).set({ gitSha: remote, _lastOrderKey: last, ...urls(p.m, t.gitBranch, remote) }, { merge: true });
    return 0;
  }
  await ensureInitialized(p);
  const commits = new Map<string, Commit>();
  const trees = new Map<string, Awaited<ReturnType<typeof readTree>>>();
  let made = 0;
  let notes: ExportNote[] = [];
  let lastEntry: Entry | null = null;
  for (const e of rebuild ? hist : todo) {
    const b = await buildEntry(p, t, hist, e, commits, trees);
    if (!b) {
      last = e.orderKey;
      continue;
    }
    const pt = await pushTree(p, b.res);
    notes = pt.notes;
    const sha = await p.cl.createCommit({ message: b.message, tree: pt.tree, parents: parent ? [parent] : [], author: b.person, committer: b.person });
    if (!rebuild) await p.cl.setRef(t.gitBranch, sha, false);
    parent = sha;
    last = e.orderKey;
    lastEntry = e;
    made++;
    if (!rebuild) await saveState(p, t, sha, last, e, b.message, notes);
  }
  if (rebuild) {
    if (parent) await p.cl.setRef(t.gitBranch, parent, true);
    if (lastEntry) await saveState(p, t, parent!, last, lastEntry, '', notes);
  }
  return made;
}
async function saveState(p: Push, t: MirrorTarget, sha: string, last: string | null, e: Entry, message: string, notes: ExportNote[]) {
  const summary = message.split('\n')[0] ?? '';
  await bref(t.gitBranch).set(
    { gitSha: sha, _lastOrderKey: last, pushedAt: nowIso(), last: { op: e.type === 'commit' ? 'commit' : 'volume-snapshot', opmId: e.id, seq: e.seq, message: summary, at: e.createdAt }, notes: notes.slice(0, 50), lastError: null, ...urls(p.m, t.gitBranch, sha) },
    { merge: true },
  );
}

/** uncommitted/<site>: one snapshot commit of the drift on the agent branch head (force) or the ref deleted */
async function pushDrift(p: Push, t: MirrorTarget): Promise<number> {
  const dep = t.ref.deploymentId!;
  const site = t.ref.siteSlug!;
  const ab = await bcol.agentBranches(dep).doc(site).get();
  const d = ab.data() ?? {};
  const drift = d.drift as { headCommitId: string | null; scannedAt: string; fingerprint: string; items: { key: string }[]; notes: unknown[] } | undefined;
  const live = typeof d._driftLive === 'string' ? (JSON.parse(d._driftLive) as ConfigView) : null;
  if (!drift || (!drift.items.length && !drift.notes.length) || !live) {
    await p.cl.deleteRef(t.gitBranch);
    await bref(t.gitBranch).set({ status: 'deleted', gitSha: null, gitUrl: null, pushedAt: nowIso(), last: { op: 'drift-clear', opmId: drift?.fingerprint ?? '', seq: null, message: 'drift committed or discarded: branch deleted', at: nowIso() } }, { merge: true });
    return 1;
  }
  const st = (await bref(t.gitBranch).get()).data() as BranchDoc | undefined;
  if (st?.last?.opmId === drift.fingerprint && st.status !== 'deleted' && (await p.cl.getRef(t.gitBranch))) return 0; // same drift already pushed
  await ensureInitialized(p);
  const agentT = await targetOf({ kind: 'agent', templateId: null, deploymentId: dep, siteSlug: site });
  const parent = ((await bref(agentT.gitBranch).get()).data() as BranchDoc | undefined)?.gitSha ?? (await p.cl.getRef(agentT.gitBranch));
  const config: Config = { ...live, secrets: live.secrets.map(({ isSet, value: _v, ...s }) => (void _v, { ...s, value: isSet ? 'set' : null })) } as Config;
  let catalog: Commit['catalog'] = null;
  if (drift.headCommitId) {
    const hc = await bcol.agentCommits(dep, site).doc(drift.headCommitId).get();
    if (hc.exists) catalog = (await decodeCommit(hc.data()!)).catalog;
  }
  const who = mirrorSitePerson(site);
  const msg = `Uncommitted changes on ${site} (${drift.items.length} items) — not committed in OPM\n\n${drift.items.slice(0, 50).map((i) => `- ${i.key}`).join('\n')}${drift.items.length > 50 ? `\n- … ${drift.items.length - 50} more` : ''}${drift.notes.length ? `\n\n${drift.notes.length} drift note(s) (not expressible as config)` : ''}`;
  const req = await buildRequest(p.x, {
      repository: `${p.m.owner}/${p.m.repo}`,
    branch: branchMeta(t),
    commit: { id: drift.fingerprint, seq: null, kind: 'drift', message: msg, author: who, createdAt: drift.scannedAt, hash: null, parentId: drift.headCommitId },
    deploymentId: dep,
    config,
    catalog,
    sites: [site],
    persist: {},
  });
  const res = await exportTree(req, !!p.m._stubExporter);
  const pt = await pushTree(p, res);
  const message = mirrorCommitMessage(msg, [
    [MIRROR_TRAILERS.drift, drift.fingerprint],
    [MIRROR_TRAILERS.branch, branchKey({ kind: 'agent', deploymentId: dep, siteSlug: site })],
  ]);
  const person = { ...who, date: drift.scannedAt };
  const sha = await p.cl.createCommit({ message, tree: pt.tree, parents: parent ? [parent] : [], author: person, committer: person });
  await p.cl.setRef(t.gitBranch, sha, true);
  await bref(t.gitBranch).set({ gitSha: sha, pushedAt: nowIso(), last: { op: 'drift', opmId: drift.fingerprint, seq: null, message: msg.split('\n')[0], at: drift.scannedAt }, notes: pt.notes.slice(0, 50), lastError: null, ...urls(p.m, t.gitBranch, sha) }, { merge: true });
  return 1;
}

// ───────────────────────────────────────── draining
async function acquire(gitBranch: string, leaseId: string): Promise<boolean> {
  const ref = bref(gitBranch);
  return db().runTransaction(async (tx) => {
    const s = await tx.get(ref);
    const cur = s.data() as BranchDoc | undefined;
    if (cur?.leaseUntil && Date.parse(cur.leaseUntil) > Date.now() && cur._leaseId !== leaseId) return false;
    tx.set(ref, { leaseUntil: new Date(Date.now() + LEASE_MS).toISOString(), _leaseId: leaseId, status: 'pushing' }, { merge: true });
    return true;
  });
}
async function release(gitBranch: string, leaseId: string) {
  const ref = bref(gitBranch);
  const q = await mcol.queue().where('gitBranch', '==', gitBranch).where('state', 'in', ['pending', 'failed', 'running']).get();
  const items = q.docs.map((d) => d.data() as MirrorQueueItem);
  const failed = items.filter((i) => i.state === 'failed');
  const m = await loadMirror();
  const paused = m.pausedUntil && Date.parse(m.pausedUntil) > Date.now();
  await db().runTransaction(async (tx) => {
    const s = await tx.get(ref);
    const cur = s.data() as BranchDoc | undefined;
    if (cur?._leaseId !== leaseId) return;
    const status = cur?.status === 'deleted' && !items.length ? 'deleted' : failed.length ? (paused ? 'paused' : 'error') : items.length ? (paused ? 'paused' : 'queued') : cur?.gitSha || cur?.status === 'deleted' ? (cur?.status === 'deleted' ? 'deleted' : 'synced') : 'never';
    const next = items.map((i) => i.nextAttemptAt).sort()[0] ?? null;
    tx.set(ref, { leaseUntil: null, _leaseId: null, status, queue: { pending: items.length - failed.length, failed: failed.length, nextAttemptAt: next }, updatedAt: nowIso() }, { merge: true });
  });
}

async function pauseFor(e: GithubError) {
  const until = e.retryAt ?? Date.now() + 3600_000;
  await mcol.config().set({ pausedUntil: new Date(until).toISOString(), pauseReason: e.rateLimited ? `GitHub rate limit: pushing resumes at ${new Date(until).toISOString()}` : `GitHub refused the token or the repository (${e.status}): ${e.message.slice(0, 200)} — fix the token / permissions, then Sync now` }, { merge: true });
  invalidateMirrorCache();
}

/** process one item (already marked running) */
async function processItem(p: Push, it: MirrorQueueItem): Promise<{ state: 'done' | 'skipped'; made: number }> {
  const t = await targetOf(it.branch);
  if (t.gitBranch !== it.gitBranch) return { state: 'skipped', made: 0 }; // the entity slug changed (never) / bad item
  let made = 0;
  switch (it.op) {
    case 'commit':
    case 'volume-snapshot':
      made = await catchUp(p, t, it.orderKey, false);
      break;
    case 'catch-up':
      made = await catchUp(p, t, null, false);
      break;
    case 'rebuild':
      made = await catchUp(p, t, null, true);
      break;
    case 'drift':
    case 'drift-clear':
      made = await pushDrift(p, t);
      break;
  }
  return { state: made ? 'done' : 'skipped', made };
}

/** Drain one branch (time budget). Returns the number of items processed. Never throws. */
export async function drainBranch(gitBranch: string, budgetMs = 400_000): Promise<number> {
  const leaseId = ulid();
  const started = Date.now();
  let processed = 0;
  try {
    if (!(await acquire(gitBranch, leaseId))) return 0;
  } catch (e) {
    logger.warn('github mirror: lease failed', { gitBranch, error: (e as Error).message });
    return 0;
  }
  try {
    for (;;) {
      if (Date.now() - started > budgetMs) break;
      const m = await loadMirror();
      if (!isActive(m)) break;
      if (m.pausedUntil && Date.parse(m.pausedUntil) > Date.now()) break;
      const q = await mcol.queue().where('gitBranch', '==', gitBranch).where('state', 'in', ['pending', 'failed', 'running']).get();
      const items = q.docs.map((d) => d.data() as MirrorQueueItem).sort((a, b) => (a.orderKey < b.orderKey ? -1 : a.orderKey > b.orderKey ? 1 : compareUtf16(a.itemId, b.itemId)));
      const it = items[0];
      if (!it) break;
      if (Date.parse(it.nextAttemptAt) > Date.now()) break; // the oldest item waits (backoff): keep the order
      const p: Push = { cl: clientOf(m), m, x: newExportCtx(), known: new Map() };
      await mcol.queue().doc(it.itemId).update({ state: 'running', startedAt: nowIso() });
      await bref(gitBranch).set({ leaseUntil: new Date(Date.now() + LEASE_MS).toISOString() }, { merge: true });
      try {
        const r = await processItem(p, it);
        const st = (await bref(gitBranch).get()).data() as BranchDoc | undefined;
        await mcol.queue().doc(it.itemId).update({ state: r.state, doneAt: nowIso(), gitSha: st?.gitSha ?? null, lastError: null });
        processed++;
      } catch (e) {
        const ge = e instanceof GithubError ? e : null;
        const attempts = it.attempts + 1;
        const err = { at: nowIso(), message: ((e as Error)?.message ?? String(e)).slice(0, 1000), status: ge?.status ?? null };
        const next = ge?.rateLimited ? new Date(ge.retryAt!).toISOString() : new Date(Date.now() + mirrorBackoffSec(attempts) * 1000).toISOString();
        await mcol.queue().doc(it.itemId).update({ state: 'failed', attempts, nextAttemptAt: next, lastError: err });
        await bref(gitBranch).set({ lastError: err }, { merge: true });
        logger.warn('github mirror: push failed', { gitBranch, op: it.op, attempts, error: err.message });
        if (attempts === 1) await auditSystem({ job: 'github-mirror', action: 'github.push-failed', targetType: 'github', target: `githubMirror/branches/${gitBranch}`, ok: false, errorMessage: err.message, data: { gitBranch, op: it.op, opmId: it.opmId, status: err.status } });
        if (ge && (ge.rateLimited || ge.auth)) await pauseFor(ge);
        break;
      }
    }
  } catch (e) {
    logger.error('github mirror: drain failed', { gitBranch, error: (e as Error)?.stack ?? String(e) });
  } finally {
    await release(gitBranch, leaseId).catch((e) => logger.warn('github mirror: release failed', { gitBranch, error: (e as Error).message }));
  }
  return processed;
}

/** Every branch with a due item (scheduler / Sync now). */
export async function drainDue(budgetMs = 480_000): Promise<number> {
  const started = Date.now();
  const m = await loadMirror();
  if (!isActive(m)) return 0;
  if (m.pausedUntil && Date.parse(m.pausedUntil) > Date.now()) return 0;
  const q = await mcol.queue().where('state', 'in', ['pending', 'failed', 'running']).get();
  const now = Date.now();
  const due = new Set<string>();
  for (const d of q.docs) {
    const it = d.data() as MirrorQueueItem;
    // a "running" item whose worker died (lease expired) is retried
    if (Date.parse(it.nextAttemptAt) <= now) due.add(it.gitBranch);
  }
  let n = 0;
  for (const b of [...due].sort()) {
    const left = budgetMs - (Date.now() - started);
    if (left < 20_000) break;
    n += await drainBranch(b, left - 10_000);
  }
  return n;
}

/** reset items stuck "running" by a dead worker (lease expired) back to pending */
export async function recoverStuck(): Promise<void> {
  const q = await mcol.queue().where('state', '==', 'running').get();
  for (const d of q.docs) {
    const it = d.data() as MirrorQueueItem;
    const b = (await bref(it.gitBranch).get()).data() as BranchDoc | undefined;
    if (!b?.leaseUntil || Date.parse(b.leaseUntil) < Date.now()) await d.ref.update({ state: 'pending' });
  }
}
export { touchBranch, mirrorBranchName };
