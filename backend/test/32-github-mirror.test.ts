/**
 * §21 #106 GitHub mirror against a FAKE GitHub (test/fake-github.ts, real git objects):
 *   settings + validation + audit, token masked everywhere / reveal (60 s, audited, no-store, rate-limited) /
 *   test connection; empty repository init; branch naming (template / deployment / agent / uncommitted);
 *   one git commit per OPM commit (same message + trailers, author, date), ORDER per branch, IDEMPOTENCY (re-enqueue,
 *   crash between ref update and state write), retry / backoff on 5xx, pause on 401 and on rate limits, Sync now,
 *   Resync everything = the same shas, persist snapshots on the agent branch, drift → uncommitted branch reset / delete,
 *   heartbeat catch-up, > max file size skipped (.skipped.txt), a pushed tree = `git write-tree` of the same files.
 * The tree comes from the real exporter (dist/opm-export.wasm) when it is built, else from the emulator-only
 * stub (githubMirror/config._stubExporter): queue / push behaviour is the same.
 */
import { test, after } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { existsSync, mkdtempSync, mkdirSync, writeFileSync, chmodSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { makeCommit, mirrorBranchName, parseMirrorTrailers, ulid, MASK, type BranchRef, type Config, type MirrorBranchState } from '@opm/shared';
import { adb, abucket, depInput, enrollSite, helloDeployment, here, person, raw, rejects, sha256Hex, sleep, tplInput, uniq } from './helpers.ts';
import { FakeGithub } from './fake-github.ts';

const fake = new FakeGithub();
const OWNER = 'opm-test';
const REPO = uniq('mirror');
const GOOD = 'github_pat_11GOOD' + 'x'.repeat(40) + 'GOOD';
const RO = 'github_pat_11READ' + 'y'.repeat(40) + 'READ';
const NOUSER = 'github_pat_11NOUS' + 'z'.repeat(40) + 'NOUS';
const realExporter = existsSync(path.resolve(here, '../dist/opm-export.wasm'));
const cfgRef = adb.collection('githubMirror').doc('config');
const clone = <T>(v: T): T => JSON.parse(JSON.stringify(v)) as T;

async function until<T>(what: string, f: () => Promise<T | null | undefined | false>, ms = 90_000): Promise<T> {
  const t0 = Date.now();
  let last: unknown;
  for (;;) {
    try {
      const v = await f();
      if (v) return v as T;
    } catch (e) {
      last = e;
    }
    if (Date.now() - t0 > ms) throw new Error(`timeout waiting for ${what}${last ? `: ${(last as Error).message}` : ''}`);
    await sleep(400);
  }
}
const rows = async (admin: Awaited<ReturnType<typeof person>>) => (await admin.real.listGithubMirrorBranches({})).items;
const row = async (admin: Awaited<ReturnType<typeof person>>, b: string) => (await rows(admin)).find((r) => r.gitBranch === b);
const synced = (admin: Awaited<ReturnType<typeof person>>, b: string, n?: number) =>
  until(`${b} synced${n ? ` with ${n} commits` : ''}`, async () => {
    const r = await row(admin, b);
    if (!r || r.status !== 'synced' || r.queue.pending || r.queue.failed) return null;
    if (n !== undefined && fake.log(fake.repo(OWNER, REPO), b).length !== n) return null;
    return r;
  });

after(async () => {
  await cfgRef.set({ enabled: false }, { merge: true });
  await fake.stop();
});

async function editDeployment(admin: Awaited<ReturnType<typeof person>>, id: string, edit: (c: Config) => void, message: string) {
  const d = await admin.real.getDeployment({ deploymentId: id });
  const input = await depInput(admin.real, id);
  edit(input as unknown as Config);
  return admin.real.commitDeployment({ deploymentId: id, body: { message, baseCommitId: d.branch!.head!.commitId, config: input } });
}
const THEMES = ['green', 'yellow', 'blue', 'red'];
/** the next allowed theme value (enum green | yellow | blue | red): every call changes the config */
const theme = (_label: string) => (c: Config) => {
  const v = c.variables.find((x) => x.variableSlug === 'theme')!;
  v.value = THEMES[(THEMES.indexOf(String(v.value)) + 1) % THEMES.length]!;
};

test('settings: validation, audit of every change, token masked everywhere, reveal 60 s (audited, no-store, rate-limited), test connection', async () => {
  await fake.start();
  fake.addRepo(OWNER, REPO);
  fake.addToken(GOOD, { login: 'mirror-bot', repos: '*', write: true });
  fake.addToken(RO, { login: 'reader', repos: '*', write: false });
  fake.addToken(NOUSER, { login: 'fg', repos: [`${OWNER}/${REPO}`], write: true, noUser: true });
  // test hooks (emulator only): the fake GitHub, the stub exporter when Go is not built, only this file's entities
  await cfgRef.set({ _apiBase: fake.base, _stubExporter: !realExporter, _scope: [], enabled: false }, { merge: true });
  const admin = await person('admin');
  const viewer = await person([{ resource: 'accounts', actions: ['read'], scope: { kind: 'all' } }] as never);
  await rejects(viewer.real.getGithubMirror({}), 'forbidden', 403);
  await rejects(admin.real.patchGithubMirror({ body: { owner: '-bad' } }), 'invalid', 422);
  await rejects(admin.real.patchGithubMirror({ body: { repo: 'a/b' } }), 'invalid', 422);
  await rejects(admin.real.patchGithubMirror({ body: { token: 'short' } }), 'invalid', 422);
  await rejects(admin.real.patchGithubMirror({ body: { owner: OWNER, repo: REPO, enabled: true } }), 'invalid', 422); // no token yet
  await rejects(admin.real.patchGithubMirror({ body: { nope: 1 } as never }), 'invalid', 422);
  const v1 = await admin.real.patchGithubMirror({ body: { owner: OWNER, repo: REPO, token: GOOD } });
  assert.equal(v1.configured, true);
  assert.equal(v1.enabled, false);
  assert.equal(v1.token, 'github_pat_•••GOOD');
  assert.equal(v1.repoUrl, `https://github.com/${OWNER}/${REPO}`);
  assert.ok(!JSON.stringify(v1).includes(GOOD));
  assert.ok(!JSON.stringify(await admin.real.getGithubMirror({})).includes(GOOD));
  // audit: every change, the token never in clear
  const au = await admin.real.listAuditLog({ query: { action: 'github.' } });
  const upd = au.items.find((x) => x.action === 'github.updated' && x.outcome === 'ok')!;
  assert.ok(upd, 'github.updated');
  assert.equal(upd.data.tokenReplaced, true);
  assert.ok(au.items.some((x) => x.action === 'github.updated' && x.outcome === 'error'), 'failed changes are audited too');
  assert.ok(!JSON.stringify(au.items).includes(GOOD), 'no token in the audit log');
  // reveal: value, no-store, audited
  const r = await raw('POST', '/v1/github-mirror/reveal-token', { headers: { Authorization: admin.bearer } });
  assert.equal(r.status, 200);
  assert.equal(r.headers.get('cache-control'), 'no-store');
  const rv = JSON.parse(r.body.toString()) as { value: string; hideAfterSec: number };
  assert.equal(rv.value, GOOD);
  assert.equal(rv.hideAfterSec, 60);
  const au2 = await admin.real.listAuditLog({ query: { action: 'github.token-revealed' } });
  assert.ok(au2.items.some((x) => x.actor === admin.uid));
  assert.ok(!JSON.stringify(au2.items).includes(GOOD));
  await rejects(viewer.real.revealGithubToken({}), 'forbidden', 403);
  // test connection: ok (empty repo), read-only token, token without /user, missing repo, bad token
  const t1 = await admin.real.testGithubMirror({});
  assert.equal(t1.ok, true, JSON.stringify(t1.checks));
  assert.equal(t1.repo!.empty, true);
  assert.equal(t1.login, 'mirror-bot');
  assert.ok((await admin.real.getGithubMirror({})).lastTest?.ok);
  await admin.real.patchGithubMirror({ body: { token: RO } });
  const t2 = await admin.real.testGithubMirror({});
  assert.equal(t2.ok, false);
  assert.equal(t2.checks.find((c) => c.name === 'contents-write')!.ok, false);
  await admin.real.patchGithubMirror({ body: { token: NOUSER } });
  const t3 = await admin.real.testGithubMirror({});
  assert.equal(t3.ok, true, JSON.stringify(t3.checks));
  assert.equal(t3.login, null);
  await admin.real.patchGithubMirror({ body: { token: GOOD, repo: 'does-not-exist' } });
  const t4 = await admin.real.testGithubMirror({});
  assert.equal(t4.checks.find((c) => c.name === 'repo')!.ok, false);
  await admin.real.patchGithubMirror({ body: { token: 'github_pat_11BAD' + 'q'.repeat(40) } });
  const t5 = await admin.real.testGithubMirror({});
  assert.equal(t5.checks.find((c) => c.name === 'token')!.ok, false);
  const tested = await admin.real.listAuditLog({ query: { action: 'github.tested' } });
  assert.ok(tested.items.length >= 5);
  await admin.real.patchGithubMirror({ body: { token: GOOD, repo: REPO } });
  // remove the token → not configured; never visible
  const v2 = await admin.real.patchGithubMirror({ body: { token: null } });
  assert.equal(v2.tokenSet, false);
  await rejects(admin.real.revealGithubToken({}), 'not-found', 404);
  const rm = (await admin.real.listAuditLog({ query: { action: 'github.updated' } })).items[0]!;
  assert.equal(rm.data.tokenRemoved, true);
  await admin.real.patchGithubMirror({ body: { token: GOOD } });
  // sync while disabled → 409
  await rejects(admin.real.syncGithubMirror({ body: {} }), 'conflict', 409);
  // rate limit (30 / min per uid)
  let limited = false;
  for (let i = 0; i < 31 && !limited; i++) {
    const x = await raw('POST', '/v1/github-mirror/reveal-token', { headers: { Authorization: admin.bearer } });
    if (x.status === 429) limited = true;
  }
  assert.ok(limited, 'reveal is rate-limited');
});

let ctx: { admin: Awaited<ReturnType<typeof person>>; depId: string; tplId: string; tplBranch: string; depBranch: string; depSlug: string };

test('enable: empty repo initialized, every OPM commit = one git commit (message + trailers, author, date), deterministic tree = git write-tree', async () => {
  const admin = await person('admin');
  const { dep, templateId } = await helloDeployment(admin, `Mirror ${uniq('d')}`);
  await cfgRef.set({ _scope: [templateId, dep.deploymentId] }, { merge: true });
  const t = await admin.real.getTemplate({ templateId });
  assert.match(t.slug, /^[a-z][a-z0-9-]{1,39}$/);
  const d = await admin.real.getDeployment({ deploymentId: dep.deploymentId });
  assert.match(d.slug, /^mirror-/);
  const tplBranch = mirrorBranchName('template', t.slug);
  const depBranch = mirrorBranchName('deployment', d.slug);
  const v = await admin.real.patchGithubMirror({ body: { enabled: true } });
  assert.equal(v.enabled, true);
  const repo = fake.repo(OWNER, REPO);
  const tplCommits = (await admin.real.listTemplateCommits({ templateId })).items.sort((a, b) => a.seq - b.seq);
  await synced(admin, tplBranch, tplCommits.length);
  await synced(admin, depBranch, 1);
  // main initialized once (empty repository)
  assert.ok(repo.refs.get('heads/main'), 'main README');
  assert.ok(fake.checkout(repo, 'main').get('README.md')!.bytes.toString().includes('OPM mirror'));
  assert.ok((await admin.real.getGithubMirror({})).initializedAt);
  // one git commit per OPM commit, oldest first, same message + trailers + author + date
  const log = fake.log(repo, tplBranch).reverse();
  assert.deepEqual(log.map((c) => parseMirrorTrailers(c.message)['OPM-Commit']), tplCommits.map((c) => c.commitId));
  for (let i = 0; i < log.length; i++) {
    const g = log[i]!;
    const o = tplCommits[i]!;
    assert.ok(g.message.startsWith(o.message.trim()), 'same message');
    const tr = parseMirrorTrailers(g.message);
    assert.equal(tr['OPM-Branch'], `template:${templateId}`);
    assert.equal(tr['OPM-Seq'], String(o.seq));
    assert.ok(g.author.includes(`<${admin.email}>`), g.author);
    assert.ok(g.author.endsWith(`${Math.floor(Date.parse(o.createdAt) / 1000)} +0000`), 'author date = createdAt');
    assert.equal(g.author, g.committer, 'committer = author (deterministic)');
    assert.equal(g.parents.length, i === 0 ? 0 : 1);
  }
  // tree content
  const files = fake.checkout(repo, tplBranch);
  for (const p of ['README.md', '.gitignore']) assert.ok(files.has(p), p);
  assert.ok(files.get('README.md')!.bytes.toString().includes(tplCommits.at(-1)!.commitId), 'the README names the commit');
  assert.ok(![...files.keys()].some((p) => p.startsWith('opm/')), 'no OPM files in a Docker-only tree (§21 #115)');
  assert.ok([...files.keys()].some((p) => p.startsWith('sites/')), 'sites exported');
  // deployment branch: secret values never pushed
  const depFiles = fake.checkout(repo, depBranch);
  const secret = await admin.real.revealSecret({ deploymentId: dep.deploymentId, secretSlug: 'api-token' });
  for (const [p, f] of depFiles) assert.ok(!f.bytes.includes(Buffer.from(secret.value)), `secret value in ${p}`);
  // the pushed tree = `git write-tree` of the same files (real git object ids)
  const dir = mkdtempSync(path.join(tmpdir(), 'opm-gh-'));
  try {
    for (const [p, f] of files) {
      mkdirSync(path.dirname(path.join(dir, p)), { recursive: true });
      writeFileSync(path.join(dir, p), f.bytes);
      if (f.mode === '100755') chmodSync(path.join(dir, p), 0o755);
    }
    const git = (...a: string[]) => execFileSync('git', a, { cwd: dir, env: { ...process.env, GIT_CONFIG_GLOBAL: '/dev/null', GIT_CONFIG_NOSYSTEM: '1' } }).toString().trim();
    git('init', '-q');
    git('add', '-A', '-f');
    assert.equal(git('write-tree'), log.at(-1)!.tree, 'tree sha = git write-tree');
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
  // the status row
  const r = (await row(admin, tplBranch))!;
  assert.equal(r.gitSha, log.at(-1)!.sha);
  assert.equal(r.gitUrl, `https://github.com/${OWNER}/${REPO}/commit/${r.gitSha}`);
  assert.equal(r.last!.opmId, tplCommits.at(-1)!.commitId);
  assert.ok(!JSON.stringify(await rows(admin)).includes(GOOD));
  ctx = { admin, depId: dep.deploymentId, tplId: templateId, tplBranch, depBranch, depSlug: d.slug };
});

test('order + idempotency: commits pushed in seq order; re-enqueue / crash between ref update and state write → no duplicate', async () => {
  const { admin, depId, depBranch } = ctx;
  const repo = fake.repo(OWNER, REPO);
  // pause pushing, commit twice, unpause: both arrive, in seq order
  await cfgRef.set({ pausedUntil: new Date(Date.now() + 3600_000).toISOString(), pauseReason: 'test' }, { merge: true });
  const c2 = await editDeployment(admin, depId, theme('red'), 'theme red');
  const c3 = await editDeployment(admin, depId, theme('blue'), 'theme blue');
  await until('2 queued items', async () => (await adb.collection('githubMirror/config/queue').where('gitBranch', '==', depBranch).where('state', '==', 'pending').get()).size >= 2);
  const s1 = await admin.real.syncGithubMirror({ body: {} });
  assert.ok(s1.branches >= 2);
  await synced(admin, depBranch, 3);
  assert.deepEqual(fake.log(repo, depBranch).reverse().map((c) => parseMirrorTrailers(c.message)['OPM-Seq']), ['1', '2', '3']);
  assert.equal(fake.log(repo, depBranch)[0]!.message.split('\n')[0], 'theme blue');
  void c2;
  // re-enqueue the same commit (a duplicate trigger) → skipped, no new git commit
  const headSha = repo.refs.get(`heads/${depBranch}`)!;
  const ref = adb.collection('githubMirror/config/queue').doc(`${depBranch.replace(/\//g, '~')}__commit__again-${c3.commitId}`);
  await ref.set({ itemId: ref.id, gitBranch: depBranch, branch: { kind: 'deployment', templateId: null, deploymentId: depId, siteSlug: null }, op: 'commit', opmId: c3.commitId, seq: c3.seq, orderKey: `${c3.createdAt}|${String(c3.seq).padStart(10, '0')}|${c3.commitId}`, state: 'pending', attempts: 0, nextAttemptAt: new Date().toISOString(), createdAt: new Date().toISOString(), startedAt: null, doneAt: null, gitSha: null, lastError: null, reason: 'test' });
  await until('duplicate item processed', async () => ['skipped', 'done'].includes(((await ref.get()).data()?.state as string) ?? ''));
  assert.equal((await ref.get()).data()!.state, 'skipped');
  assert.equal(repo.refs.get(`heads/${depBranch}`), headSha);
  // crash: the ref moved but the state write was lost → the trailer of the remote head is adopted
  const bdoc = adb.collection('githubMirror/config/branches').doc(depBranch.replace(/\//g, '~'));
  const prev = fake.log(repo, depBranch)[1]!;
  const prevKey = (await adb.collection('deployments').doc(depId).collection('commits').doc(parseMirrorTrailers(prev.message)['OPM-Commit']!).get()).data()!;
  await bdoc.set({ gitSha: prev.sha, _lastOrderKey: `${prevKey.createdAt}|${String(prevKey.seq).padStart(10, '0')}|${prevKey.commitId}` }, { merge: true });
  await admin.real.syncGithubMirror({ body: { gitBranch: depBranch } });
  await synced(admin, depBranch, 3);
  assert.equal(repo.refs.get(`heads/${depBranch}`), headSha, 'no duplicate commit after the recovered crash');
  assert.equal((await bdoc.get()).data()!.gitSha, headSha);
});

test('retry / backoff on 5xx, pause on 401 and on rate limits, Sync now resumes', async () => {
  const { admin, depId, depBranch } = ctx;
  const repo = fake.repo(OWNER, REPO);
  fake.fail(/\/git\/commits$/, 502, 1, { method: 'POST' });
  const c4 = await editDeployment(admin, depId, theme('green'), 'theme green');
  const failed = await until('failed item', async () => {
    const q = await adb.collection('githubMirror/config/queue').where('gitBranch', '==', depBranch).where('state', '==', 'failed').get();
    return q.docs[0]?.data();
  });
  assert.equal(failed.attempts, 1);
  const wait = Date.parse(failed.nextAttemptAt) - Date.parse(failed.lastError.at);
  assert.ok(wait >= 25_000 && wait <= 35_000, `backoff ≈ 30 s (${wait})`);
  assert.equal(failed.lastError.status, 502);
  const r1 = (await row(admin, depBranch))!;
  assert.equal(r1.status, 'error');
  assert.equal(r1.queue.failed, 1);
  assert.ok((await admin.real.listAuditLog({ query: { action: 'github.push-failed' } })).items.some((x) => x.data.gitBranch === depBranch));
  await admin.real.syncGithubMirror({ body: {} });
  await synced(admin, depBranch, 4);
  assert.equal(parseMirrorTrailers(fake.log(repo, depBranch)[0]!.message)['OPM-Commit'], c4.commitId);
  // 401 → the whole mirror pauses
  fake.fail(/\/git\/trees$/, 401, 1, { method: 'POST' });
  await editDeployment(admin, depId, theme('pink'), 'theme pink');
  const paused = await until('paused', async () => {
    const m = await admin.real.getGithubMirror({});
    return m.pausedUntil ? m : null;
  });
  assert.match(paused.pauseReason!, /refused the token/);
  await admin.real.syncGithubMirror({ body: {} });
  await synced(admin, depBranch, 5);
  assert.equal((await admin.real.getGithubMirror({})).pausedUntil, null);
  // primary rate limit (403 + remaining 0) → paused until the reset
  fake.fail(/\/git\/blobs$/, 403, 1, { method: 'POST', headers: { 'x-ratelimit-remaining': '0', 'x-ratelimit-reset': String(Math.floor(Date.now() / 1000) + 1800) }, body: JSON.stringify({ message: 'API rate limit exceeded' }) });
  fake.fail(/\/git\/blobs\/[0-9a-f]+$/, 404, 50, { method: 'GET' }); // force blob uploads (cache misses)
  await adb.collection('githubMirror/config/blobs').get().then((s) => Promise.all(s.docs.map((d) => d.ref.delete())));
  await editDeployment(admin, depId, theme('cyan'), 'theme cyan');
  const rl = await until('rate-limit pause', async () => {
    const m = await admin.real.getGithubMirror({});
    return m.pausedUntil && /rate limit/.test(m.pauseReason ?? '') ? m : null;
  });
  assert.ok(Date.parse(rl.pausedUntil!) > Date.now() + 1500_000);
  fake.faults = [];
  await admin.real.syncGithubMirror({ body: {} });
  await synced(admin, depBranch, 6);
});

test('Resync everything: rebuilt from OPM history with the same shas', async () => {
  const { admin, tplBranch, depBranch } = ctx;
  const repo = fake.repo(OWNER, REPO);
  const before = { t: repo.refs.get(`heads/${tplBranch}`), d: repo.refs.get(`heads/${depBranch}`) };
  const r = await admin.real.resyncGithubMirror({ body: {} });
  assert.ok(r.enqueued >= 2);
  await until('rebuild items done', async () => (await adb.collection('githubMirror/config/queue').where('op', '==', 'rebuild').where('state', 'in', ['pending', 'failed', 'running']).get()).empty);
  assert.equal(repo.refs.get(`heads/${tplBranch}`), before.t, 'template branch: same sha');
  assert.equal(repo.refs.get(`heads/${depBranch}`), before.d, 'deployment branch: same sha');
  assert.ok((await admin.real.listAuditLog({ query: { action: 'github.resynced' } })).items.length >= 1);
});

test('files above the size limit are skipped with .skipped.txt + a note', async () => {
  const { admin, depId, depBranch } = ctx;
  const repo = fake.repo(OWNER, REPO);
  await cfgRef.set({ _maxFileBytes: 8 }, { merge: true }); // the hello banner File is larger
  await editDeployment(admin, depId, theme('orange'), 'theme orange');
  await synced(admin, depBranch, 7);
  const files = fake.checkout(repo, depBranch);
  const skippedFile = [...files.keys()].find((p) => p.endsWith('.skipped.txt'));
  assert.ok(skippedFile, 'a .skipped.txt');
  assert.match(files.get(skippedFile!)!.bytes.toString(), /bytes/);
  const r = (await row(admin, depBranch))!;
  assert.ok(r.notes.some((n) => n.code === 'SKIPPED_LARGE'));
  await cfgRef.set({ _maxFileBytes: null }, { merge: true });
});

test('agent branch (commits + persist snapshots), drift → uncommitted branch reset + delete, heartbeat catch-up, all four branch kinds', async () => {
  const { admin, depId, depSlug } = ctx;
  const repo = fake.repo(OWNER, REPO);
  const app = await enrollSite(admin, depId, 'app');
  await adb.collection('deployments').doc(depId).collection('agents').doc('app').update({ agentVersion: '0.15.0-test' });
  const head = (await admin.real.getDeployment({ deploymentId: depId })).branch!.head!;
  const got = await app.api.getAgentCommit({ commitId: head.commitId });
  const own: BranchRef = { kind: 'agent', deploymentId: depId, siteSlug: 'app' };
  const author = { kind: 'person' as const, email: 'ops@site.test', uid: null, name: 'Site Ops', siteSlug: 'app' };
  const a1 = makeCommit({ commitId: ulid(), branch: own, parent: null, kind: 'merge', message: `merge cloud seq ${head.seq}`, author, createdAt: new Date().toISOString(), config: got.commit.config, catalog: got.commit.catalog, source: { branch: { kind: 'deployment', deploymentId: depId }, commitId: head.commitId, seq: head.seq, label: 'x', picks: 'all', skipped: 0 }, scope: 'app' });
  await app.api.pushAgentBranch({ body: { commits: [a1] } });
  const agentBranch = mirrorBranchName('agent', depSlug, 'app');
  await synced(admin, agentBranch, 1);
  const g1 = fake.log(repo, agentBranch)[0]!;
  assert.equal(parseMirrorTrailers(g1.message)['OPM-Commit'], a1.commitId);
  assert.ok(g1.author.startsWith('Site Ops <ops@site.test>'));
  assert.ok([...fake.checkout(repo, agentBranch).keys()].every((p) => !p.startsWith('sites/') || p.startsWith('sites/app/')), 'agent branch: only its own site');
  // a persist-to-cloud snapshot → a commit on the agent branch under sites/app/volumes/<vol>/
  const vol = (got.commit.config.sites.find((s) => s.siteSlug === 'app')!.config.volumes ?? [])[0]?.volumeSlug ?? 'data';
  const body = Buffer.from('{"flows":[1,2,3]}\n');
  const fhex = sha256Hex(body);
  await abucket.file(`files/${fhex}`).save(body);
  const tree = { entries: [{ path: 'flows.json', sha256: `sha256:${fhex}`, size: body.length, mode: '0644', render: 'none' }], dirs: [] };
  const { canonicalize } = await import('@opm/shared');
  const tjson = canonicalize(tree);
  const thex = sha256Hex(tjson);
  await abucket.file(`files/${thex}`).save(Buffer.from(tjson));
  const versionId = ulid();
  const key = `app:${vol}`;
  await adb.collection('deployments').doc(depId).collection('volumeContent').doc(key).set({ siteSlug: 'app', volumeSlug: vol }, { merge: true });
  await adb.collection('deployments').doc(depId).collection('volumeContent').doc(key).collection('versions').doc(versionId).set({ versionId, seq: 1, origin: 'snapshot', treeHash: `sha256:${thex}`, createdAt: new Date().toISOString(), createdBy: 'agent:app', deploymentId: depId, siteSlug: 'app', volumeSlug: vol, trigger: 'manual-agent', fileCount: 1, dirCount: 0, totalBytes: body.length, baseVersionId: null, restoredFromVersionId: null, note: '', excludedCount: 0, skippedCount: 0, skipped: [], agentSnapshotId: ulid(), requestId: null, pushId: null, changes: { added: 1, changed: 0, removed: 0 } });
  await synced(admin, agentBranch, 2);
  const g2 = fake.log(repo, agentBranch)[0]!;
  assert.equal(parseMirrorTrailers(g2.message)['OPM-Volume-Version'], versionId);
  assert.match(g2.message, /^Volume snapshot app\//);
  if (!realExporter || got.commit.config.sites.find((s) => s.siteSlug === 'app')!.config.volumes?.some((v) => v.volumeSlug === vol)) {
    const f = fake.checkout(repo, agentBranch).get(`sites/app/volumes/${vol}/flows.json`);
    assert.ok(f, 'snapshot file in the tree');
    assert.deepEqual(f!.bytes, body);
  }
  // drift → uncommitted/<site>: one commit on top of the agent branch head; a new fingerprint resets it; clear deletes it
  const unc = mirrorBranchName('uncommitted', depSlug, 'app');
  const live = clone(got.commit.config) as unknown as Config;
  live.sites.find((s) => s.siteSlug === 'app')!.config.containers[0]!.env.push({ name: 'HOTFIX', value: 'on', secretRef: null });
  const liveView = { ...live, secrets: live.secrets.map((s) => ({ ...s, value: s.value ? MASK : null, isSet: !!s.value })) };
  // secret values are refused
  await rejects(app.api.agentPushDrift({ body: { headCommitId: a1.commitId, scannedAt: new Date().toISOString(), fingerprint: 'f0', items: [{ key: 'k', origin: 'docker', detail: '' }], notes: [], liveConfig: live as never } }), 'invalid', 422);
  const p1 = await app.api.agentPushDrift({ body: { headCommitId: a1.commitId, scannedAt: new Date().toISOString(), fingerprint: 'f1', items: [{ key: 'sites/app/containers/x/env/HOTFIX', origin: 'docker', detail: 'added by hand' }], notes: [], liveConfig: liveView as never } });
  assert.deepEqual(p1, { stored: true, mirrored: true });
  assert.deepEqual(await app.api.agentPushDrift({ body: { headCommitId: a1.commitId, scannedAt: new Date().toISOString(), fingerprint: 'f1', items: [{ key: 'sites/app/containers/x/env/HOTFIX', origin: 'docker', detail: 'added by hand' }], notes: [], liveConfig: liveView as never } }), { stored: false, mirrored: false }, 'same fingerprint = no change');
  await until('uncommitted branch', async () => repo.refs.get(`heads/${unc}`));
  const u1 = fake.log(repo, unc);
  assert.equal(u1[0]!.parents[0], repo.refs.get(`heads/${agentBranch}`), 'drift commit on top of the agent branch head');
  assert.equal(parseMirrorTrailers(u1[0]!.message)['OPM-Drift'], 'f1');
  assert.match(u1[0]!.message, /^Uncommitted changes on app \(1 items\)/);
  await app.api.agentPushDrift({ body: { headCommitId: a1.commitId, scannedAt: new Date(Date.now() + 1000).toISOString(), fingerprint: 'f2', items: [{ key: 'a', origin: 'docker', detail: '' }, { key: 'b', origin: 'docker', detail: '' }], notes: [], liveConfig: liveView as never } });
  await until('uncommitted reset', async () => {
    const h = repo.refs.get(`heads/${unc}`);
    return h && parseMirrorTrailers(fake.commit(repo, h).message)['OPM-Drift'] === 'f2';
  });
  assert.equal(fake.log(repo, unc).length, 3, 'reset: still ONE drift commit on the agent branch (2) — not stacked');
  // all four kinds in the status table
  const all = await rows(admin);
  for (const b of [ctx.tplBranch, ctx.depBranch, agentBranch, unc]) assert.ok(all.some((r) => r.gitBranch === b), b);
  assert.deepEqual([...new Set(all.filter((r) => [ctx.tplBranch, ctx.depBranch, agentBranch, unc].includes(r.gitBranch)).map((r: MirrorBranchState) => r.branch.kind))].sort(), ['agent', 'deployment', 'template', 'uncommitted']);
  // cleared → deleted
  await app.api.agentPushDrift({ body: { headCommitId: a1.commitId, scannedAt: new Date(Date.now() + 2000).toISOString(), fingerprint: 'clear', items: [], notes: [], liveConfig: null } });
  await until('uncommitted deleted', async () => !repo.refs.get(`heads/${unc}`));
  await until('row deleted', async () => (await row(admin, unc))?.status === 'deleted');
  // heartbeat: the agent head is not the mirrored one → catch-up item (agent online)
  await adb.collection('githubMirror/config/branches').doc(agentBranch.replace(/\//g, '~')).set({ last: { op: 'commit', opmId: 'other', seq: 1, message: '', at: '' } }, { merge: true });
  await app.api.heartbeat({ body: { siteSlug: 'app', agentVersion: '0.15.0-test', headRevId: null, staging: null, peersSeen: [], metrics: { cpu: 0, memPct: 0, diskPct: 0 } as never, edge: null, keyVersion: 1, advertiseHost: '127.0.0.1', agentPort: 7080, ip: '127.0.0.1', hostname: 'h', branch: { head: { commitId: a1.commitId, seq: 1, hash: a1.hash, message: a1.message, author: a1.author, createdAt: a1.createdAt, kind: a1.kind }, mergedFrom: null, uncommitted: null } } as never });
  await until('agent-online item', async () => (await adb.collection('githubMirror/config/queue').where('reason', '==', 'agent-online').get()).docs.find((d) => d.data().gitBranch === agentBranch));
});

test('queue listing + cleanup (disable)', async () => {
  const { admin } = ctx;
  const q = await admin.real.listGithubMirrorQueue({ query: { limit: 50 } });
  assert.ok(q.items.length > 0);
  assert.ok(!JSON.stringify(q).includes(GOOD));
  await admin.real.patchGithubMirror({ body: { enabled: false } });
  await cfgRef.set({ _scope: [] }, { merge: true });
  void tplInput;
});
