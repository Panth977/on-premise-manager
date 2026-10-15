/**
 * §21 #106 Admin → GitHub (GITHUB_MIRROR_CLOUD_API, perm accounts.admin) + the agent drift report.
 * The token is a credential stored like a registry password (masked everywhere, revealGithubToken = CREDENTIAL_OPS:
 * no-store, 30/min/uid, audited github.token-revealed). Every change is audited by the router hook (data refined here).
 */
import {
  canonicalize,
  GITHUB_OWNER_RE,
  GITHUB_REPO_RE,
  GITHUB_TOKEN_RE,
  REVEAL_SECONDS,
  type AgentBranchDrift,
  type AgentDriftPushRequest,
  type GithubCheck,
  type GithubTestResponse,
  type MirrorBranchState,
  type MirrorQueueItem,
  type PatchGithubMirrorRequest,
} from '@opm/shared';
import { db, nowIso } from '../core/fire.js';
import { E } from '../core/errors.js';
import { need } from '../core/perm.js';
import { asObject, onlyKeys, V, sha256Hex } from '../core/util.js';
import { bcol } from '../core/branches.js';
import { GithubClient, GithubError } from '../core/github.js';
import { allTargets, apiBaseOf, enqueue, enqueueAll, invalidateMirrorCache, isActive, isConfigured, loadMirror, mcol, mirrorHook, mirrorView, refOf, targetOf, type StoredMirror } from '../core/mirror.js';
import { drainDue, recoverStuck } from '../core/mirror-worker.js';
import type { Handlers } from '../core/router.js';
import { mirrorBranchDocId } from '@opm/shared';
import type { CollectionRef } from '../ports/index.js';

const admin = (c: Parameters<Handlers['getGithubMirror']>[0]) => need(c, 'accounts', 'admin');

/** delete every document of a collection (small: branch rows / queue items / blob cache of ONE repository) */
async function clearCollection(ref: CollectionRef) {
  for (;;) {
    const s = await ref.limit(400).get();
    if (s.empty) return;
    const b = db().batch();
    for (const d of s.docs) b.delete(d.ref);
    await b.commit();
  }
}

export async function testConnection(m: StoredMirror): Promise<GithubTestResponse> {
  const at = nowIso();
  const checks: GithubCheck[] = [];
  const cl = new GithubClient(m.token ?? '', m.owner, m.repo, apiBaseOf(m));
  let repo: GithubTestResponse['repo'] = null;
  let login: string | null = null;
  let rate: GithubTestResponse['rate'] = null;
  const fail = (name: GithubCheck['name'], e: unknown) => checks.push({ name, ok: false, message: e instanceof GithubError ? (e.status === 401 ? 'the token is invalid or expired (401)' : e.message) : String((e as Error)?.message ?? e) });
  try {
    login = await cl.getUser();
    checks.push({ name: 'token', ok: true, message: login ? `token of ${login}` : 'token accepted' });
  } catch (e) {
    fail('token', e);
  }
  let pushPerm: boolean | undefined;
  try {
    const r = await cl.getRepo();
    if (!r) checks.push({ name: 'repo', ok: false, message: `repository ${m.owner}/${m.repo} not found, or the token cannot see it (fine-grained token: select the repository)` });
    else {
      pushPerm = r.permissions?.push;
      const empty = await cl.isEmpty().catch(() => false);
      repo = { fullName: r.full_name, private: r.private, defaultBranch: r.default_branch, empty, htmlUrl: r.html_url };
      checks.push({ name: 'repo', ok: true, message: `${r.full_name} (${r.private ? 'private' : 'PUBLIC — anyone can read the configs'}${empty ? ', empty: OPM creates the first commit on main' : ''})` });
    }
  } catch (e) {
    fail('repo', e);
  }
  if (repo) {
    try {
      await cl.req('GET', `/repos/${encodeURIComponent(m.owner)}/${encodeURIComponent(m.repo)}/git/matching-refs/heads/`, undefined, { ok409: true });
      checks.push({ name: 'contents-read', ok: true, message: 'Contents: read' });
    } catch (e) {
      fail('contents-read', e);
    }
    if (repo.empty) {
      // the Git Data API refuses an empty repository: the write check uses the repository permission
      checks.push({ name: 'contents-write', ok: pushPerm !== false, message: pushPerm === false ? 'the token has no write access (Contents: read and write)' : 'Contents: write (verified at the first push: the repository is empty)' });
    } else
      try {
        await cl.createBlob(Buffer.from(`opm mirror write check ${at}\n`, 'utf8')); // an unreferenced blob (garbage-collected by GitHub)
        checks.push({ name: 'contents-write', ok: true, message: 'Contents: read and write' });
      } catch (e) {
        fail('contents-write', e);
        if (e instanceof GithubError && (e.status === 403 || e.status === 404)) checks[checks.length - 1]!.message = 'the token has no write access to the repository (fine-grained token: Contents → Read and write)';
      }
  }
  try {
    rate = await cl.rateLimit();
    checks.push({ name: 'rate', ok: !rate || rate.remaining > 0, message: rate ? `${rate.remaining}/${rate.limit} requests left (resets ${rate.resetAt})` : 'unknown' });
  } catch (e) {
    fail('rate', e);
  }
  return { ok: checks.every((x) => x.ok), at, checks, repo, login, rate };
}

/** status rows: every mirrorable branch + stored rows (uncommitted / deleted entities) */
async function branchRows(): Promise<MirrorBranchState[]> {
  const [targets, stored, queue] = await Promise.all([allTargets(), mcol.branches().get(), mcol.queue().where('state', 'in', ['pending', 'failed', 'running']).get()]);
  const rows = new Map<string, MirrorBranchState>();
  for (const d of stored.docs) {
    const { _lastOrderKey, _leaseId, _lastSeq, ...r } = d.data() as MirrorBranchState & Record<string, unknown>;
    void _lastOrderKey;
    void _leaseId;
    void _lastSeq;
    // a lease renewal racing a branch-table clear (disable / repo change) can leave a fragment without gitBranch
    if (typeof r.gitBranch !== 'string' || !r.branch) continue;
    rows.set(r.gitBranch, r as MirrorBranchState);
  }
  const now = nowIso();
  for (const t of targets)
    if (!rows.has(t.gitBranch))
      rows.set(t.gitBranch, { gitBranch: t.gitBranch, branch: t.ref, label: t.label, status: 'never', last: null, gitSha: null, gitUrl: null, treeUrl: null, pushedAt: null, queue: { pending: 0, failed: 0, nextAttemptAt: null }, lastError: null, notes: [], leaseUntil: null, updatedAt: now });
  const q = new Map<string, { pending: number; failed: number; next: string | null }>();
  for (const d of queue.docs) {
    const it = d.data() as MirrorQueueItem;
    const x = q.get(it.gitBranch) ?? { pending: 0, failed: 0, next: null };
    if (it.state === 'failed') x.failed++;
    else x.pending++;
    if (!x.next || it.nextAttemptAt < x.next) x.next = it.nextAttemptAt;
    q.set(it.gitBranch, x);
  }
  for (const r of rows.values()) {
    const x = q.get(r.gitBranch);
    r.queue = { pending: x?.pending ?? 0, failed: x?.failed ?? 0, nextAttemptAt: x?.next ?? null };
  }
  const order = (k: string) => ({ template: 0, deployment: 1, agent: 2, uncommitted: 3 })[k] ?? 9;
  return [...rows.values()].sort((a, b) => a.gitBranch.split('/')[1]!.localeCompare(b.gitBranch.split('/')[1]!) || order(a.branch.kind) - order(b.branch.kind) || a.gitBranch.localeCompare(b.gitBranch));
}

async function syncLike(c: Parameters<Handlers['syncGithubMirror']>[0], op: 'catch-up' | 'rebuild') {
  admin(c);
  const b = asObject(c.json());
  onlyKeys(b, ['gitBranch']);
  if (b.gitBranch !== undefined && (typeof b.gitBranch !== 'string' || !b.gitBranch)) throw E.bad('gitBranch', 'V_TYPE', 'gitBranch must be a branch name');
  const m = await loadMirror();
  if (!isActive(m)) throw E.conflict('the GitHub mirror is disabled (enable it with an owner, a repository and a token first)', { reason: 'disabled' });
  // Sync now / Resync: unpause, retry failed items at once
  await mcol.config().set({ pausedUntil: null, pauseReason: null }, { merge: true });
  invalidateMirrorCache();
  const failed = await mcol.queue().where('state', 'in', ['failed', 'running']).get();
  for (const d of failed.docs) if (!b.gitBranch || d.data().gitBranch === b.gitBranch) await d.ref.update({ state: 'pending', nextAttemptAt: nowIso() });
  await recoverStuck();
  const r = await enqueueAll(op, op === 'rebuild' ? 'resync' : 'sync', b.gitBranch as string | undefined);
  if (b.gitBranch && !r.branches) throw E.notFound(`no mirrored branch ${b.gitBranch}`);
  const processed = await drainDue(60_000);
  c.audit = { data: { op, gitBranch: b.gitBranch ?? null, enqueued: r.enqueued, branches: r.branches, processed } };
  return { enqueued: r.enqueued, branches: r.branches, processed };
}

export const githubMirrorHandlers: Pick<Handlers, 'getGithubMirror' | 'patchGithubMirror' | 'testGithubMirror' | 'revealGithubToken' | 'listGithubMirrorBranches' | 'listGithubMirrorQueue' | 'syncGithubMirror' | 'resyncGithubMirror' | 'agentPushDrift'> = {
  async getGithubMirror(c) {
    admin(c);
    return mirrorView(await loadMirror());
  },

  async patchGithubMirror(c) {
    admin(c);
    const b = asObject(c.json());
    onlyKeys(b, ['owner', 'repo', 'enabled', 'token']);
    const p = b as PatchGithubMirrorRequest;
    const v = new V();
    if (p.owner !== undefined && (typeof p.owner !== 'string' || !GITHUB_OWNER_RE.test(p.owner))) v.err('owner', 'V_FORMAT', 'owner must be a GitHub user or organization name (letters, digits, "-", ≤ 39)');
    if (p.repo !== undefined && (typeof p.repo !== 'string' || !GITHUB_REPO_RE.test(p.repo))) v.err('repo', 'V_FORMAT', 'repo must be a repository name (letters, digits, ".", "_", "-", ≤ 100)');
    if (p.enabled !== undefined && typeof p.enabled !== 'boolean') v.err('enabled', 'V_TYPE', 'enabled must be true or false');
    if (p.token !== undefined && p.token !== null && (typeof p.token !== 'string' || !GITHUB_TOKEN_RE.test(p.token.trim()))) v.err('token', 'V_FORMAT', 'token must be a GitHub token (20–255 printable characters, e.g. github_pat_…)');
    v.throwIf();
    const uid = c.account!.uid;
    const now = nowIso();
    const cur = await loadMirror();
    const next: StoredMirror = { ...cur };
    if (p.owner !== undefined) next.owner = p.owner;
    if (p.repo !== undefined) next.repo = p.repo;
    if (p.enabled !== undefined) next.enabled = p.enabled;
    const tokenReplaced = typeof p.token === 'string' && p.token.trim() !== cur.token;
    const tokenRemoved = p.token === null && !!cur.token;
    if (typeof p.token === 'string') next.token = p.token.trim();
    if (p.token === null) next.token = null;
    if (tokenReplaced || tokenRemoved) {
      next.tokenSetAt = tokenRemoved ? null : now;
      next.tokenSetBy = tokenRemoved ? null : uid;
    }
    if (next.enabled && !isConfigured(next)) throw E.bad('enabled', 'V_REQUIRED', 'enabling the mirror needs the owner, the repository and a token');
    const repoChanged = (cur.owner || cur.repo) && (next.owner.toLowerCase() !== cur.owner.toLowerCase() || next.repo.toLowerCase() !== cur.repo.toLowerCase());
    if (repoChanged) {
      next.initializedAt = null;
      next.lastTest = null;
    }
    if (repoChanged || tokenReplaced || tokenRemoved || p.enabled !== undefined) {
      next.pausedUntil = null;
      next.pauseReason = null;
    }
    if (!cur.createdAt) {
      next.createdAt = now;
      next.createdBy = uid;
    }
    next.updatedAt = now;
    next.updatedBy = uid;
    const { _apiBase, _stubExporter, _maxFileBytes, _scope, ...store } = next;
    void _apiBase;
    void _stubExporter;
    void _maxFileBytes;
    void _scope;
    await mcol.config().set({ ...store, _repoKey: `${next.owner.toLowerCase()}/${next.repo.toLowerCase()}` }, { merge: true });
    invalidateMirrorCache();
    if (repoChanged) {
      // the branch rows / queue / blob cache describe the OLD repository
      await clearCollection(mcol.queue());
      await clearCollection(mcol.branches());
    }
    const turnedOn = isActive(next) && (!isActive(cur) || repoChanged);
    let enqueued = 0;
    if (turnedOn) enqueued = (await enqueueAll('catch-up', 'enable')).enqueued;
    c.audit = { data: { tokenReplaced, tokenRemoved, repoChanged: !!repoChanged, enabled: next.enabled, ...(turnedOn ? { catchUpQueued: enqueued } : {}) } };
    return mirrorView(await loadMirror());
  },

  async testGithubMirror(c) {
    admin(c);
    const m = await loadMirror();
    if (!isConfigured(m)) throw E.bad('', 'V_REQUIRED', 'set the owner, the repository and the token first');
    const r = await testConnection(m);
    await mcol.config().set({ lastTest: r }, { merge: true });
    invalidateMirrorCache();
    c.audit = { data: { ok: r.ok, checks: r.checks.map((x) => `${x.name}:${x.ok ? 'ok' : 'failed'}`) } };
    return r;
  },

  async revealGithubToken(c) {
    admin(c);
    const m = await loadMirror();
    if (!m.token) throw E.notFound('no token is stored');
    c.audit = { data: { owner: m.owner, repo: m.repo } };
    return { value: m.token, hideAfterSec: REVEAL_SECONDS };
  },

  async listGithubMirrorBranches(c) {
    admin(c);
    return { items: await branchRows() };
  },

  async listGithubMirrorQueue(c) {
    admin(c);
    const gb = c.query.get('gitBranch');
    const st = c.query.get('state');
    const lim = Math.min(200, Math.max(1, Number(c.query.get('limit') ?? 100) || 100));
    const s = await mcol.queue().orderBy('createdAt', 'desc').limit(1000).get();
    const items = s.docs.map((d) => d.data() as MirrorQueueItem).filter((i) => (!gb || i.gitBranch === gb) && (!st || i.state === st));
    return { items: items.slice(0, lim) };
  },

  async syncGithubMirror(c) {
    return syncLike(c, 'catch-up');
  },
  async resyncGithubMirror(c) {
    return syncLike(c, 'rebuild');
  },

  // ── agent: uncommitted drift (agent ≥ 0.15.0)
  async agentPushDrift(c) {
    const { deploymentId, siteSlug } = c.agent!;
    const b = asObject(c.json()) as unknown as AgentDriftPushRequest & Record<string, unknown>;
    onlyKeys(b, ['headCommitId', 'scannedAt', 'fingerprint', 'items', 'notes', 'liveConfig']);
    const v = new V();
    if (b.headCommitId !== null && (typeof b.headCommitId !== 'string' || b.headCommitId.length > 64)) v.err('headCommitId', 'V_TYPE', 'headCommitId must be a commit id or null');
    if (typeof b.scannedAt !== 'string' || !Number.isFinite(Date.parse(b.scannedAt))) v.err('scannedAt', 'V_FORMAT', 'scannedAt must be an ISO time');
    if (typeof b.fingerprint !== 'string' || !b.fingerprint || b.fingerprint.length > 200) v.err('fingerprint', 'V_REQUIRED', 'fingerprint is required (≤ 200)');
    if (!Array.isArray(b.items) || b.items.length > 2000) v.err('items', 'V_TYPE', 'items must be a list (≤ 2000)');
    if (!Array.isArray(b.notes) || b.notes.length > 500) v.err('notes', 'V_TYPE', 'notes must be a list (≤ 500)');
    if (b.liveConfig !== null && (typeof b.liveConfig !== 'object' || Array.isArray(b.liveConfig) || !Array.isArray((b.liveConfig as { sites?: unknown }).sites))) v.err('liveConfig', 'V_TYPE', 'liveConfig must be a config view or null');
    v.throwIf();
    const items = (b.items as unknown[]).map((x) => x as Record<string, unknown>).map((x) => ({ key: String(x.key ?? '').slice(0, 500), origin: (['docker', 'file', 'secret-file', 'volume-content'].includes(String(x.origin)) ? x.origin : 'docker') as AgentBranchDrift['items'][number]['origin'], detail: String(x.detail ?? '').slice(0, 1000) }));
    const notes = (b.notes as unknown[]).map((x) => x as Record<string, unknown>).map((x) => ({ fingerprint: String(x.fingerprint ?? '').slice(0, 200), label: String(x.label ?? '').slice(0, 300), detail: String(x.detail ?? '').slice(0, 1000) }));
    const cleared = !items.length && !notes.length;
    // the live config is a VIEW: secret values must be masked — refuse anything that looks like a value
    let live: string | null = null;
    if (!cleared && b.liveConfig) {
      const secrets = ((b.liveConfig as { secrets?: { value?: unknown }[] }).secrets ?? []).map((s) => s.value);
      if (secrets.some((x) => x !== null && x !== undefined && x !== '•••')) throw E.bad('liveConfig.secrets', 'V_SECRET', 'liveConfig must not hold secret values (masked "•••" or null)');
      live = JSON.stringify(b.liveConfig);
      if (live.length > 900_000) throw E.tooLarge('liveConfig is larger than 900 KB');
    }
    const ref = bcol.agentBranches(deploymentId).doc(siteSlug);
    const prev = (await ref.get()).data()?.drift as AgentBranchDrift | undefined;
    const drift: AgentBranchDrift = { headCommitId: b.headCommitId ?? null, scannedAt: b.scannedAt, fingerprint: b.fingerprint, items, notes, receivedAt: nowIso() };
    if (prev && prev.fingerprint === drift.fingerprint && canonicalize(prev.items) === canonicalize(items)) return { stored: false, mirrored: false };
    await ref.set({ siteSlug, drift, _driftLive: live }, { merge: true });
    let mirrored = false;
    await mirrorHook('drift', async () => {
      const t = await targetOf(refOf('uncommitted', { deploymentId, siteSlug }));
      const wasCleared = !prev || (!prev.items.length && !prev.notes.length);
      if (cleared && wasCleared) return; // nothing on GitHub to delete
      mirrored = await enqueue(t, { op: cleared ? 'drift-clear' : 'drift', opmId: cleared ? `clear-${sha256Hex(drift.scannedAt + drift.fingerprint).slice(0, 16)}` : sha256Hex(drift.fingerprint).slice(0, 40), seq: null, createdAt: drift.scannedAt, reason: 'drift' });
    });
    return { stored: true, mirrored };
  },
};
export { mirrorBranchDocId };
