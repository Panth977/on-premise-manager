// §21 #104 branches: vectors (shared with Go), keys, messages, commits, versions, API tables.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import {
  applyMerge,
  branchKey,
  branchKeyDocId,
  BRANCH_AGENT_API,
  BRANCH_CLOUD_API,
  checkCommitMessage,
  commitStats,
  commitSummary,
  configHash,
  isFastForward,
  itemHash,
  makeCommit,
  markSecretValues,
  parseBranchKey,
  parsePointParam,
  planMerge,
  pointParam,
  suggestMessage,
  supportsBranches,
  uncommittedCount,
  verifyCommitLink,
  type BranchRef,
} from './branch.js';
import { CLOUD_API } from './cloud-api.js';
import { defaultSiteConfig } from './validate.js';
import { AGENT_API } from './agent-api.js';
import { ACTIVITY_TYPES, AUDIT_ACTIONS, AUDIT_OPS, type Config } from './types.js';

const V = JSON.parse(readFileSync(new URL('../testdata/branch-vectors.json', import.meta.url), 'utf8'));

test('vectors: plans + applies', () => {
  for (const c of V.cases) {
    if (c.revert) continue;
    const items = planMerge(c.base, c.source, c.target, c.opts);
    assert.deepEqual(
      items.map((i) => ({ key: i.key, kind: i.kind, effect: i.effect, preselect: i.preselect, requires: i.requires, sourceHash: i.sourceHash, scalar: i.scalar })),
      c.items,
      c.name,
    );
    assert.equal(isFastForward(items), c.fastForward, c.name);
    if (c.choices) {
      const r = applyMerge(c.base, c.source, c.target, c.choices, { ...c.opts, values: c.values });
      assert.equal(configHash(r.config), c.result.hash, c.name);
      assert.deepEqual(r.taken, c.result.taken, c.name);
      assert.deepEqual(r.notTaken.map((x) => x.key), c.result.notTaken, c.name);
      assert.deepEqual(r.unresolved, c.result.unresolved, c.name);
      assert.equal(r.kind, c.result.kind, c.name);
    }
  }
});

test('vectors: hashes, counts, item hashes', () => {
  for (const h of V.hashes) assert.equal(configHash(h.config), h.hash);
  for (const c of V.counts) assert.equal(uncommittedCount(c.head, c.working, c.opts), c.expect);
  assert.deepEqual(commitStats(V.stats.parent, V.stats.config, { secretValues: true }), V.stats.expect);
  for (const h of V.itemHashes) assert.equal(itemHash(h.absent ? { has: false, v: null } : { has: true, v: h.value }), h.hash);
});

test('branch keys round trip', () => {
  const refs: BranchRef[] = [
    { kind: 'template', templateId: 'abc' },
    { kind: 'deployment', deploymentId: 'EByKdKkGRHOdkm5emrg4' },
    { kind: 'agent', deploymentId: 'EByKdKkGRHOdkm5emrg4', siteSlug: 'site-01' },
  ];
  for (const r of refs) assert.deepEqual(parseBranchKey(branchKey(r)), r);
  assert.equal(branchKeyDocId('agent:x/site-01'), 'agent:x~site-01');
  assert.equal(parseBranchKey('agent:x'), null);
  assert.equal(parseBranchKey('template:x/site'), null);
  assert.equal(parseBranchKey('nope:x'), null);
  const p = parsePointParam('deployment:d1@01JABCDEFGHJKMNPQRSTVWXYZ0')!;
  assert.deepEqual(p.at, { commitId: '01JABCDEFGHJKMNPQRSTVWXYZ0' });
  assert.equal(pointParam(p), 'deployment:d1@01JABCDEFGHJKMNPQRSTVWXYZ0');
  assert.equal(parsePointParam('template:t1')!.at, 'head');
});

test('commit messages', () => {
  assert.equal(checkCommitMessage('').length, 1);
  assert.equal(checkCommitMessage('   ').length, 1);
  assert.equal(checkCommitMessage(undefined).length, 1);
  assert.equal(checkCommitMessage('fix theme').length, 0);
  assert.equal(checkCommitMessage('x'.repeat(201)).length, 1);
  assert.equal(checkCommitMessage('short\n' + 'y'.repeat(1500)).length, 0);
  assert.equal(checkCommitMessage('a\n' + 'y'.repeat(2000)).length, 1);
  assert.equal(commitSummary('  first line \nbody'), 'first line');
  assert.ok(suggestMessage([{ label: 'Variable theme', effect: 'change' }]).startsWith('1 changed: theme'));
});

test('commits: hash, seq, stats, verify', () => {
  const cfg: Config = { variables: [], secrets: [], files: [], sites: [] };
  const author = { kind: 'person' as const, email: 'a@b.c', uid: 'u1', name: 'A', siteSlug: null };
  const c1 = makeCommit({ commitId: 'C1', branch: { kind: 'template', templateId: 't' }, parent: null, kind: 'create', message: 'created empty', author, createdAt: '2026-10-09T00:00:00.000Z', config: cfg });
  assert.equal(c1.seq, 1);
  assert.equal(c1.hash, configHash(cfg));
  const cfg2: Config = { ...cfg, variables: [{ variableSlug: 'x', label: 'X', description: '', type: 'string', value: '1', required: false, options: null, validation: null }] };
  const c2 = makeCommit({ commitId: 'C2', branch: c1.branch, parent: c1, kind: 'change', message: 'add x', author, createdAt: '2026-10-09T00:01:00.000Z', config: cfg2 });
  assert.equal(c2.seq, 2);
  assert.equal(c2.parentId, 'C1');
  assert.deepEqual([c2.stats.added, c2.stats.changed, c2.stats.removed], [1, 0, 0]);
  assert.equal(verifyCommitLink(c2, c1), null);
  assert.match(verifyCommitLink({ ...c2, hash: 'sha256:00' }, c1)!, /hash/);
  assert.match(verifyCommitLink({ ...c2, seq: 5 }, c1)!, /seq/);
});

test('secret values never stored plain in merge bases', () => {
  const cfg = { variables: [], files: [], sites: [], secrets: [{ secretSlug: 's', label: 'S', description: '', required: true, generate: null, value: 'plain', rotatedAt: null }] } as unknown as Config;
  const m = markSecretValues(cfg);
  assert.match(String(m.secrets[0].value), /^sha256:[0-9a-f]{64}$/);
  // a marked base still compares equal to the plain value it was made from
  assert.deepEqual(planMerge(m, cfg, cfg, { secretValues: true }), []);
});

test('agent versions', () => {
  assert.equal(supportsBranches('0.14.0-10091200'), true);
  assert.equal(supportsBranches('0.13.1-10091124'), false);
  assert.equal(supportsBranches('1.0.0'), true);
  assert.equal(supportsBranches(null), false);
});

test('API tables, activities, audit', () => {
  for (const k of Object.keys(BRANCH_CLOUD_API)) assert.ok(k in CLOUD_API, k);
  for (const k of Object.keys(BRANCH_AGENT_API)) assert.ok(k in AGENT_API, k);
  for (const t of ['branch.committed', 'branch.merged', 'branch.cherry-picked', 'branch.reverted', 'branch.reset', 'drift.detected', 'drift.committed', 'agent-branch.pushed']) assert.ok((ACTIVITY_TYPES as readonly string[]).includes(t), t);
  for (const op of ['commitTemplate', 'merge', 'revertCommit', 'resetToCommit', 'commitDeployment']) {
    const a = (AUDIT_OPS as Record<string, { action: string }>)[op];
    assert.ok(a && (AUDIT_ACTIONS as readonly string[]).includes(a.action), op);
  }
});

// §21 #117 persist-to-cloud content: a persist volume without content (seed null) is an empty tree, so the server's
// files become per-file content items (never a whole-volume change); a target without a tree takes the source's tree
// whole (the files were dropped before).
test('§21 #117 persist volume content merges per file; seed null → tree keeps the files', () => {
  const sha = (c: string) => `sha256:${c.repeat(64)}`;
  const e = (path: string, c: string) => ({ path, sha256: sha(c), size: 1, mode: '0644', render: 'none' as const });
  const persist = { exclude: [], maxBytes: 50 * 1024 * 1024, auto: true };
  const cfg = (seed: unknown, withPersist: boolean): Config =>
    ({
      variables: [],
      secrets: [],
      files: [],
      sites: [{ siteSlug: 's', label: 'S', config: { ...defaultSiteConfig(null), volumes: [{ volumeSlug: 'v', label: '', description: '', backup: true, location: { kind: 'auto' }, kind: 'dir', seed, ...(withPersist ? { persist } : {}) }] } }],
    }) as unknown as Config;
  const live = cfg({ kind: 'tree', entries: [e('a.json', 'a'), e('b.js', 'b')], dirs: [] }, true);
  const items = planMerge(null, live, cfg(null, true));
  assert.deepEqual(items.map((i) => i.key).sort(), ['sites/s/volumes/v/content/a.json', 'sites/s/volumes/v/content/b.js']);
  const r = applyMerge(null, live, cfg(null, true), items.map((i) => ({ key: i.key, take: 'source' as const })));
  assert.deepEqual((r.config.sites[0]!.config.volumes[0]!.seed as { entries: { path: string }[] }).entries.map((x) => x.path), ['a.json', 'b.js']);
  // starter content (no persist): the target has no tree → the definition item brings the source's tree whole
  const src = cfg({ kind: 'tree', entries: [e('x.txt', 'c')], dirs: [] }, false);
  const it2 = planMerge(null, src, cfg(null, false));
  assert.deepEqual(it2.map((i) => i.key), ['sites/s/volumes/v']);
  const r2 = applyMerge(null, src, cfg(null, false), [{ key: 'sites/s/volumes/v', take: 'source' }]);
  assert.deepEqual((r2.config.sites[0]!.config.volumes[0]!.seed as { entries: { path: string }[] }).entries.map((x) => x.path), ['x.txt']);
});
