// §21 #104 — generates shared/testdata/branch-vectors.json (Go mirror: agent/internal/branch).
// Inputs + hand-written expectations live HERE; the script asserts every expectation against the TS implementation
// (dist/branch.js) before writing, so the vectors are reviewed facts, not a snapshot of whatever the code does.
// Run: cd shared && npm run build && node scripts/gen-branch-vectors.mjs
import { readFileSync, writeFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import assert from 'node:assert/strict';
import { applyMerge, applyRevert, commitStats, configHash, isFastForward, itemHash, nextSkips, planMerge, rebaseChanges, resetPlan, revertPlan, uncommittedCount, withReferenceSites } from '../dist/branch.js';
import { canonicalize } from '../dist/canonical.js';

const hello = JSON.parse(readFileSync(new URL('../testdata/hello-config.json', import.meta.url), 'utf8')).config;
const clone = (x) => JSON.parse(JSON.stringify(x));
const H = (s) => 'sha256:' + createHash('sha256').update(s).digest('hex');
const site = (c, s) => c.sites.find((x) => x.siteSlug === s);
const ctr = (c, s, k) => site(c, s).config.containers.find((x) => x.containerSlug === k);
const mut = (c, f) => {
  const x = clone(c);
  f(x);
  return x;
};
const template = mut(hello, (c) => c.secrets.forEach((s) => ((s.value = null), (s.rotatedAt = null))));

/** compact item for vectors */
const it = (i) => ({ key: i.key, kind: i.kind, effect: i.effect, preselect: i.preselect, requires: i.requires, sourceHash: i.sourceHash, scalar: i.scalar });
const keysKinds = (items) => Object.fromEntries(items.map((i) => [i.key, i.kind]));

const cases = [];
function add(name, { base, source, target, opts = {}, expect, choices = null, values = undefined, expectApply = null }) {
  const items = planMerge(base, source, target, opts);
  // hand-written expectations: key → kind (exact set)
  assert.deepEqual(keysKinds(items), expect, `${name}: plan`);
  const v = { name, base, source, target, opts, items: items.map(it), fastForward: isFastForward(items) };
  if (choices) {
    const r = applyMerge(base, source, target, choices, { ...opts, values });
    if (expectApply) expectApply(r);
    v.choices = choices;
    if (values) v.values = values;
    v.result = { config: r.config, hash: configHash(r.config), taken: r.taken, notTaken: r.notTaken.map((x) => x.key), unresolved: r.unresolved, errors: r.errors.map((e) => e.key), kind: r.kind };
  }
  cases.push(v);
}

// 1 identical → nothing
add('identical', { base: hello, source: hello, target: hello, opts: { secretValues: true }, expect: {} });

// 2 two-way: source adds a variable, target has an extra env row, image differs
{
  const src = mut(hello, (c) => {
    c.variables.push({ variableSlug: 'motd', label: 'MOTD', description: '', type: 'string', value: 'hi', required: false, options: null, validation: null });
    ctr(c, 'db', 'redis').version = 'v2';
  });
  const tgt = mut(hello, (c) => ctr(c, 'db', 'redis').env.push({ name: 'EXTRA', value: '1', secretRef: null }));
  add('two-way add / remove / change', {
    base: null,
    source: src,
    target: tgt,
    opts: { secretValues: true },
    expect: { 'variables/motd': 'add', 'sites/db/containers/redis/image': 'change', 'sites/db/containers/redis/env/EXTRA': 'remove' },
    choices: [
      { key: 'variables/motd', take: 'source' },
      { key: 'sites/db/containers/redis/env/EXTRA', take: 'source' },
    ],
    expectApply: (r) => {
      assert.equal(r.config.variables.at(-1).variableSlug, 'motd');
      assert.equal(ctr(r.config, 'db', 'redis').env.length, 0);
      assert.equal(ctr(r.config, 'db', 'redis').version, 'v1');
      assert.equal(r.kind, 'cherry-pick');
      assert.deepEqual(r.notTaken.map((x) => x.key), ['sites/db/containers/redis/image']);
    },
  });
}

// 3 three-way: theirs / ours / conflict + skipped
{
  const base = hello;
  const src = mut(hello, (c) => {
    c.variables[0].value = 'yellow'; // theme value: theirs
    ctr(c, 'db', 'redis').resources = { cpus: 1, memoryMb: 512 }; // conflict with target
    site(c, 'db').config.agentPort = 7181; // skipped earlier (hash recorded below)
  });
  const tgt = mut(hello, (c) => {
    c.variables[1].value = 'Hi there'; // greeting value: ours
    ctr(c, 'db', 'redis').resources = { cpus: 2, memoryMb: 256 }; // conflict
  });
  const skipHash = itemHash({ has: true, v: 7181 });
  const skipped = [{ key: 'sites/db/settings/agentPort', theirsHash: skipHash, sourceSeq: 3, skippedAt: '2026-10-09T00:00:00.000Z', skippedBy: 'a@b.c' }];
  add('three-way theirs / ours / conflict / skipped', {
    base,
    source: src,
    target: tgt,
    opts: { secretValues: true, skipped },
    expect: {
      'variables/theme/value': 'theirs',
      'variables/greeting/value': 'ours',
      'sites/db/settings/agentPort': 'skipped',
      'sites/db/containers/redis/resources': 'conflict',
    },
    choices: [
      { key: 'variables/theme/value', take: 'source' },
      { key: 'sites/db/containers/redis/resources', take: 'target' },
    ],
    expectApply: (r) => {
      assert.equal(r.config.variables[0].value, 'yellow');
      assert.equal(r.config.variables[1].value, 'Hi there');
      assert.deepEqual(ctr(r.config, 'db', 'redis').resources, { cpus: 2, memoryMb: 256 });
      assert.deepEqual(r.unresolved, []);
      assert.deepEqual(r.notTaken.map((x) => x.key).sort(), ['sites/db/containers/redis/resources', 'sites/db/settings/agentPort']);
      const sk = nextSkips(r.notTaken, skipped, 4, '2026-10-09T01:00:00.000Z', 'x@y.z');
      assert.equal(sk.find((s) => s.key === 'sites/db/settings/agentPort').skippedAt, '2026-10-09T00:00:00.000Z'); // kept (same hash)
      assert.equal(sk.length, 2);
    },
  });
}

// 3b the realistic second compare: after a merge the base IS the source; the change not taken comes back as ours → skipped
{
  const src = mut(hello, (c) => (site(c, 'db').config.agentPort = 7181));
  const skipped = [{ key: 'sites/db/settings/agentPort', theirsHash: itemHash({ has: true, v: 7181 }), sourceSeq: 3, skippedAt: '2026-10-09T00:00:00.000Z', skippedBy: 'a@b.c' }];
  add('second compare: not taken → skipped', { base: src, source: src, target: hello, opts: { secretValues: true, skipped }, expect: { 'sites/db/settings/agentPort': 'skipped' } });
  add('second compare: no skip record → ours', { base: src, source: src, target: hello, opts: { secretValues: true }, expect: { 'sites/db/settings/agentPort': 'ours' } });
  const src2 = mut(src, (c) => (site(c, 'db').config.agentPort = 7281)); // the source changed it again → a normal change
  add('second compare: source changed again → conflict', { base: src, source: src2, target: hello, opts: { secretValues: true, skipped }, expect: { 'sites/db/settings/agentPort': 'conflict' } });
}

// 4 unresolved conflict + custom scalar
{
  const src = mut(hello, (c) => (site(c, 'app').label = 'App (source)'));
  const tgt = mut(hello, (c) => (site(c, 'app').label = 'App (target)'));
  add('conflict custom scalar', {
    base: hello,
    source: src,
    target: tgt,
    opts: { secretValues: true },
    expect: { 'sites/app/settings/label': 'conflict' },
    choices: [{ key: 'sites/app/settings/label', take: 'custom', value: 'App (merged)' }],
    expectApply: (r) => {
      assert.equal(site(r.config, 'app').label, 'App (merged)');
      assert.equal(r.kind, 'merge');
    },
  });
  add('conflict unresolved', { base: hello, source: src, target: tgt, opts: { secretValues: true }, expect: { 'sites/app/settings/label': 'conflict' }, choices: [], expectApply: (r) => assert.deepEqual(r.unresolved, ['sites/app/settings/label']) });
}

// 5 deletions: the source removed a container (theirs remove) + target-only container (ours "added in target")
{
  const src = mut(hello, (c) => (site(c, 'app').config.containers = site(c, 'app').config.containers.filter((x) => x.containerSlug !== 'hello-web')));
  const tgt = mut(hello, (c) => site(c, 'db').config.containers.push({ ...clone(ctr(c, 'db', 'redis')), containerSlug: 'redis-2', ports: [] }));
  const removeWeb = mut(hello, (c) => {
    site(c, 'app').config.containers = site(c, 'app').config.containers.filter((x) => x.containerSlug !== 'hello-web');
    site(c, 'app').config.edge.routes = site(c, 'app').config.edge.routes.filter((r) => r.action.containerSlug !== 'hello-web');
  });
  void src;
  add('deletions: removed in source + only in target', {
    base: hello,
    source: removeWeb,
    target: tgt,
    opts: { secretValues: true },
    expect: Object.fromEntries([
      ['sites/app/containers/hello-web', 'theirs'],
      ...site(hello, 'app').config.edge.routes.filter((r) => r.action.containerSlug === 'hello-web').map((r) => [`sites/app/edge/routes/${r.routeId}`, 'theirs']),
      ['sites/db/containers/redis-2', 'ours'],
    ]),
    choices: [
      { key: 'sites/app/containers/hello-web', take: 'source' },
      ...site(hello, 'app').config.edge.routes.filter((r) => r.action.containerSlug === 'hello-web').map((r) => ({ key: `sites/app/edge/routes/${r.routeId}`, take: 'source' })),
      { key: 'sites/db/containers/redis-2', take: 'source' },
    ],
    expectApply: (r) => {
      assert.equal(ctr(r.config, 'app', 'hello-web'), undefined);
      assert.equal(ctr(r.config, 'db', 'redis-2'), undefined);
      assert.equal(r.kind, 'merge');
    },
  });
}

// 6 secret values: compared as hashes; taking copies the plain value; templates never compare values
{
  const src = mut(hello, (c) => ((c.secrets[0].value = 'NEWSECRETVALUE0000000000'), (c.secrets[0].rotatedAt = '2026-10-09T02:00:00.000Z')));
  add('secret value two-way', {
    base: null,
    source: src,
    target: hello,
    opts: { secretValues: true },
    expect: { 'secrets/api-token/value': 'change' },
    choices: [{ key: 'secrets/api-token/value', take: 'source' }],
    expectApply: (r) => {
      assert.equal(r.config.secrets[0].value, 'NEWSECRETVALUE0000000000');
      assert.equal(r.config.secrets[0].rotatedAt, '2026-10-09T02:00:00.000Z');
    },
  });
  add('template vs deployment: no secret value items', { base: null, source: template, target: hello, opts: { secretValues: false }, expect: {} });
}

// 7 fast-forward: every item theirs → preselected
{
  const src = mut(hello, (c) => {
    c.variables[0].value = 'blue';
    ctr(c, 'db', 'redis').restart = 'on-failure';
  });
  add('fast-forward', {
    base: hello,
    source: src,
    target: hello,
    opts: { secretValues: true },
    expect: { 'variables/theme/value': 'theirs', 'sites/db/containers/redis/restart': 'theirs' },
    choices: [
      { key: 'variables/theme/value', take: 'source' },
      { key: 'sites/db/containers/redis/restart', take: 'source' },
    ],
    expectApply: (r) => {
      assert.equal(configHash(r.config), configHash(src));
      assert.equal(r.kind, 'merge');
    },
  });
}

// 8 requires: a new container referenced by a new route
{
  const src = mut(hello, (c) => {
    const a = site(c, 'app');
    a.config.containers.push({ ...clone(a.config.containers[0]), containerSlug: 'web-2', ports: [], depends: [] });
    a.config.edge.routes.push({ ...clone(a.config.edge.routes[0]), routeId: 'web2', order: 99, action: { ...clone(a.config.edge.routes[0].action), type: 'proxy', containerSlug: 'web-2' } });
  });
  const items = planMerge(null, src, hello, { secretValues: true });
  const route = items.find((i) => i.key === 'sites/app/edge/routes/web2');
  assert.ok(route.requires.includes('sites/app/containers/web-2'), 'route requires its new container');
  add('requires: route → new container', { base: null, source: src, target: hello, opts: { secretValues: true }, expect: { 'sites/app/containers/web-2': 'add', 'sites/app/edge/routes/web2': 'add' } });
}

// 9 agent scope: only shared sections + own site
{
  const src = mut(hello, (c) => {
    site(c, 'app').label = 'App 2';
    site(c, 'db').label = 'DB 2';
    c.variables[0].value = 'red';
  });
  add('agent scope db', { base: hello, source: src, target: hello, opts: { secretValues: true, scope: 'db' }, expect: { 'variables/theme/value': 'theirs', 'sites/db/settings/label': 'theirs' } });
  const { config, updatedSites } = withReferenceSites(hello, src, 'db');
  assert.deepEqual(updatedSites, ['app']);
  assert.equal(site(config, 'app').label, 'App 2');
  assert.equal(site(config, 'db').label, 'Database site');
}

// 10 volume content per file
{
  const tree = (entries, dirs = []) => ({ kind: 'tree', entries, dirs });
  const e = (path, body) => ({ path, sha256: H(body), size: Buffer.byteLength(body), mode: '0644', render: 'none' });
  const withSeed = (seed) => mut(hello, (c) => (site(c, 'db').config.volumes[0].seed = seed));
  const b = withSeed(tree([e('a.txt', 'a'), e('b.txt', 'b')]));
  const s = withSeed(tree([e('a.txt', 'A'), e('b.txt', 'b'), e('c/d.txt', 'd')], ['empty']));
  const t = withSeed(tree([e('a.txt', 'a')]));
  add('volume content per file', {
    base: b,
    source: s,
    target: t,
    opts: { secretValues: true },
    expect: {
      'sites/db/volumes/redis-data/content/a.txt': 'theirs',
      'sites/db/volumes/redis-data/content/b.txt': 'ours',
      'sites/db/volumes/redis-data/content/c/d.txt': 'theirs',
      'sites/db/volumes/redis-data/content/empty/': 'theirs',
    },
    choices: [
      { key: 'sites/db/volumes/redis-data/content/c/d.txt', take: 'source' },
      { key: 'sites/db/volumes/redis-data/content/empty/', take: 'source' },
      { key: 'sites/db/volumes/redis-data/content/b.txt', take: 'source' },
    ],
    expectApply: (r) => {
      const seed = site(r.config, 'db').config.volumes[0].seed;
      assert.deepEqual(seed.entries.map((x) => x.path), ['a.txt', 'b.txt', 'c/d.txt']);
      assert.equal(seed.entries[0].sha256, H('a'));
      assert.deepEqual(seed.dirs, ['empty']);
      assert.equal(r.kind, 'cherry-pick');
    },
  });
}

// 11 revert / reset
{
  const c1 = hello;
  const c2 = mut(c1, (c) => (c.variables[0].value = 'yellow'));
  const c3 = mut(c2, (c) => (ctr(c, 'db', 'redis').restart = 'no'));
  const rv = revertPlan(c2, c1, c3, { secretValues: true });
  assert.deepEqual(keysKinds(rv), { 'variables/theme/value': 'theirs' });
  const r = applyRevert(c2, c1, c3, [], { secretValues: true });
  assert.equal(r.config.variables[0].value, 'green');
  assert.equal(ctr(r.config, 'db', 'redis').restart, 'no');
  const c4 = mut(c3, (c) => (c.variables[0].value = 'blue')); // a later commit changed the same item → conflict
  assert.deepEqual(keysKinds(revertPlan(c2, c1, c4, { secretValues: true })), { 'variables/theme/value': 'conflict' });
  const rs = resetPlan(c1, c3, { secretValues: true });
  assert.ok(rs.every((i) => i.preselect));
  cases.push({ name: 'revert', revert: { commit: c2, parent: c1, head: c3 }, items: rv.map(it), result: { config: r.config, hash: configHash(r.config) } });
}

// 12 §21 #105 rebase of a browser change set onto a moved head (source = mine, target = the new head)
{
  const oldHead = hello;
  // mine: theme → yellow, redis restart → no; the new head (someone else committed): redis restart → on-failure + file removed
  const mine = mut(oldHead, (c) => ((c.variables[0].value = 'yellow'), (ctr(c, 'db', 'redis').restart = 'no')));
  const newHead = mut(oldHead, (c) => ((ctr(c, 'db', 'redis').restart = 'on-failure'), (c.files = [])));
  const expect = { 'variables/theme/value': 'theirs', 'sites/db/containers/redis/restart': 'conflict', 'files/banner': 'ours' };
  add('rebase: clean change + conflict, head-only removal kept', {
    base: oldHead,
    source: mine,
    target: newHead,
    opts: { secretValues: false },
    expect,
    choices: [
      { key: 'variables/theme/value', take: 'source' },
      { key: 'sites/db/containers/redis/restart', take: 'source' },
    ],
    expectApply: (r) => {
      assert.equal(r.config.variables[0].value, 'yellow');
      assert.equal(ctr(r.config, 'db', 'redis').restart, 'no');
      assert.equal(r.config.files.length, 0, 'the head removal stays');
      assert.deepEqual(r.unresolved, []);
    },
  });
  // TS helper: without a choice the conflict is reported and the head value kept; clean = false
  const rb = rebaseChanges(oldHead, mine, newHead);
  assert.equal(rb.clean, false);
  assert.deepEqual(rb.conflicts.map((i) => i.key), ['sites/db/containers/redis/restart']);
  assert.equal(rb.config.variables[0].value, 'yellow');
  assert.equal(ctr(rb.config, 'db', 'redis').restart, 'on-failure');
  const rb2 = rebaseChanges(oldHead, mine, newHead, [{ key: 'sites/db/containers/redis/restart', take: 'target' }]);
  assert.equal(rb2.clean, true);
  assert.equal(ctr(rb2.config, 'db', 'redis').restart, 'on-failure');
  // no conflict at all: auto
  const mine2 = mut(oldHead, (c) => (c.variables[0].value = 'yellow'));
  const rb3 = rebaseChanges(oldHead, mine2, newHead);
  assert.equal(rb3.clean, true);
  assert.equal(configHash(rb3.config), configHash(mut(newHead, (c) => (c.variables[0].value = 'yellow'))));
}

// hash + counts
const hashes = [hello, template].map((c) => ({ config: c, hash: configHash(c), canonical: canonicalize(c).length }));
assert.equal(hashes[0].hash, H(canonicalize(hello)));
const counts = [{ head: hello, working: mut(hello, (c) => ((c.variables[0].value = 'red'), (c.files = []))), opts: { secretValues: true }, expect: 2 }];
for (const c of counts) assert.equal(uncommittedCount(c.head, c.working, c.opts), c.expect);
const stats = commitStats(hello, counts[0].working, { secretValues: true });
assert.deepEqual([stats.added, stats.changed, stats.removed], [0, 1, 1]);
const itemHashes = [null, 'x', 1, true, { a: [1, 2] }].map((v) => ({ value: v, hash: itemHash({ has: true, v }) }));
itemHashes.push({ absent: true, hash: itemHash({ has: false, v: null }) });

writeFileSync(new URL('../testdata/branch-vectors.json', import.meta.url), JSON.stringify({ description: '§21 #104 / #105 branch compare / merge / rebase vectors (generated by scripts/gen-branch-vectors.mjs; TS dist/branch.js and Go internal/branch must agree)', cases, hashes, counts, stats: { parent: hello, config: counts[0].working, expect: stats }, itemHashes }, null, 1) + '\n');
console.log(`branch-vectors.json: ${cases.length} cases`);
