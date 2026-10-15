// §21 #101 — generates shared/testdata/volume-content-vectors.json (Go mirror: agent/internal/volcontent).
// Inputs + the hand-written expectations (paths, excludes, plans, merges) live HERE; the script computes the canonical
// tree strings + hashes and asserts every hand-written expectation against the TS implementation before writing.
// Run: cd shared && npm run build && node scripts/gen-volume-content-vectors.mjs
import { createHash } from 'node:crypto';
import { writeFileSync } from 'node:fs';
import assert from 'node:assert/strict';
import { compileExcludes, contentPathError, excludePatternError, isExcluded, mergeVolumePush, normalizeVolumeTree, planVolumePush, volumeTreeHashInput } from '../dist/volume-content.js';

const H = (s) => 'sha256:' + createHash('sha256').update(s).digest('hex');
const e = (path, body, mode = '0644', render = 'none') => ({ path, sha256: H(body), size: Buffer.byteLength(body), mode, render });

const paths = [
  ['flows.json', true],
  ['lib/flows/a.json', true],
  ['dir with space/x y.txt', true],
  ['ünïcödé/файл.js', true],
  ['emoji/😀.txt', true],
  ['.config.users.json', true],
  ['.opm-seeded', false],
  ['sub/.opm-seeded', true],
  ['', false],
  ['/abs', false],
  ['trailing/', false],
  ['a//b', false],
  ['./a', false],
  ['a/../b', false],
  ['a\\b', false],
  ['tab\there', false],
  ['nul\u0000x', false],
  ['x'.repeat(255), true],
  ['x'.repeat(256), false],
  [Array.from({ length: 205 }, () => 'abcd').join('/'), false], // 1024 chars total = 205*4+204 = 1024 → ok? (computed below)
];
const pathVectors = paths.map(([p, ok]) => {
  const err = contentPathError(p);
  return { path: p, ok: err === null, _expected: ok };
});
// the 1024-byte case: assert what the rule says (≤ 1024 bytes)
pathVectors[pathVectors.length - 1]._expected = Buffer.byteLength(paths[paths.length - 1][0]) <= 1024;
for (const v of pathVectors) assert.equal(v.ok, v._expected, `path ${JSON.stringify(v.path).slice(0, 60)}`);

const trees = [
  { name: 'empty', tree: { entries: [], dirs: [] } },
  { name: 'node-red', tree: { entries: [e('settings.js', 'module.exports = {}\n'), e('flows.json', '[]', '0644', 'expressions'), e('lib/flows/x.json', '{"a":1}'), e('flows_cred.json', '{}', '0600')], dirs: ['lib', 'context', 'context/global'] } },
  { name: 'unsorted-unicode', tree: { entries: [e('ü.txt', 'u'), e('z.txt', 'z'), e('A.txt', 'A'), e('a.txt', 'a'), e('😀.txt', 'e'), e('～.txt', 'w')], dirs: ['Z', 'z/y', 'z'] } },
  { name: 'defaults-filled', tree: { entries: [{ path: 'run.sh', sha256: H('#!/bin/sh\n'), size: 10 }], dirs: [] } },
  { name: 'dir-equal-to-entry-dropped', tree: { entries: [e('a', 'x')], dirs: ['a', 'b/c', 'b'] } },
];
const treeVectors = trees.map(({ name, tree }) => {
  const canonical = volumeTreeHashInput(tree);
  return { name, tree, normalized: normalizeVolumeTree(tree), canonical, hash: H(canonical) };
});

const excludeCases = [
  // [patterns, path, isDir, excluded]
  [['node_modules/'], 'node_modules', true, true],
  [['node_modules/'], 'node_modules/a/b.js', false, true],
  [['node_modules/'], 'lib/node_modules/x.js', false, true],
  [['node_modules/'], 'node_modules', false, false],
  [['*.log'], 'a.log', false, true],
  [['*.log'], 'logs/b.log', false, true],
  [['*.log'], 'logs', true, false],
  [['/flows.json'], 'flows.json', false, true],
  [['/flows.json'], 'x/flows.json', false, false],
  [['lib/*.json'], 'lib/a.json', false, true],
  [['lib/*.json'], 'lib/sub/a.json', false, false],
  [['lib/**'], 'lib/sub/a.json', false, true],
  [['lib/**'], 'lib', true, false],
  [['**/cache/'], 'a/b/cache/x', false, true],
  [['**/cache/'], 'cache/x', false, true],
  [['a?.txt'], 'ab.txt', false, true],
  [['a?.txt'], 'a/.txt', false, false],
  [['.cache/'], '.cache/z', false, true],
  [[], '.opm-seeded', false, true],
  [[], 'sub/.opm-seeded', false, false],
  [['*.backup'], '.flows.json.backup', false, true],
  [['x.(1)+'], 'x.(1)+', false, true],
];
const excludeVectors = excludeCases.map(([patterns, path, isDir, excluded]) => {
  const got = isExcluded(path, isDir, compileExcludes(patterns));
  assert.equal(got, excluded, `exclude ${JSON.stringify(patterns)} ${path} dir=${isDir}`);
  return { patterns, path, isDir, excluded };
});
const patternErrors = [
  ['node_modules/', true],
  ['*.log', true],
  ['/a/b', true],
  ['', false],
  [' x', false],
  ['!keep', false],
  ['[ab]', false],
  ['a//b', false],
  ['../x', false],
  ['/', false],
].map(([p, ok]) => {
  assert.equal(excludePatternError(p) === null, ok, `pattern ${p}`);
  return { pattern: p, ok };
});

// plans (3-way) — expected kinds hand-written
const B = { entries: [e('a.json', 'A0'), e('b.json', 'B0'), e('c.json', 'C0'), e('d.json', 'D0'), e('e.json', 'E0')], dirs: ['keep', 'gone'] };
const L = { entries: [e('a.json', 'A0'), e('b.json', 'B-live'), e('c.json', 'C-live'), e('e.json', 'E-same'), e('n-live.json', 'NL')], dirs: ['keep', 'live-dir'] };
const C = { entries: [e('a.json', 'A-cloud'), e('b.json', 'B0'), e('c.json', 'C-cloud'), e('d.json', 'D0'), e('e.json', 'E-same'), e('n-cloud.json', 'NC')], dirs: ['keep', 'cloud-dir'] };
const plans = [
  {
    name: 'three-way',
    base: B,
    live: L,
    cloud: C,
    expect: [
      ['a.json', 'cloud'],
      ['b.json', 'live'],
      ['c.json', 'conflict'],
      ['d.json', 'live'], // live removed it, cloud unchanged → kept removed
      ['n-cloud.json', 'cloud'],
      ['n-live.json', 'live'],
    ],
  },
  { name: 'unknown-base', base: null, live: L, cloud: C, expect: [['a.json', 'conflict'], ['b.json', 'conflict'], ['c.json', 'conflict'], ['d.json', 'conflict'], ['n-cloud.json', 'conflict'], ['n-live.json', 'conflict']] },
  { name: 'fast-forward', base: B, live: B, cloud: C, expect: [['a.json', 'cloud'], ['c.json', 'cloud'], ['e.json', 'cloud'], ['n-cloud.json', 'cloud']] },
  { name: 'mode-only', base: { entries: [e('run.sh', 'x')], dirs: [] }, live: { entries: [e('run.sh', 'x')], dirs: [] }, cloud: { entries: [e('run.sh', 'x', '0755')], dirs: [] }, expect: [['run.sh', 'cloud']] },
];
const planVectors = plans.map((p) => {
  const items = planVolumePush(p.base && normalizeVolumeTree(p.base), normalizeVolumeTree(p.live), normalizeVolumeTree(p.cloud));
  assert.deepEqual(items.map((i) => [i.path, i.kind]), p.expect, p.name);
  return { name: p.name, base: p.base, live: p.live, cloud: p.cloud, items: items.map((i) => ({ path: i.path, kind: i.kind, liveOp: i.liveOp, cloudOp: i.cloudOp })) };
});
const merges = [
  { name: 'per-file-cloud', plan: 'three-way', resolution: 'per-file', choices: { 'c.json': 'cloud' } },
  { name: 'per-file-live', plan: 'three-way', resolution: 'per-file', choices: { 'c.json': 'live' } },
  { name: 'overwrite', plan: 'three-way', resolution: 'overwrite', choices: {} },
  { name: 'keep-live', plan: 'three-way', resolution: 'keep-live', choices: {} },
].map((m) => {
  const p = plans.find((x) => x.name === m.plan);
  const t = mergeVolumePush(normalizeVolumeTree(p.base), normalizeVolumeTree(p.live), normalizeVolumeTree(p.cloud), m.resolution, m.choices);
  return { ...m, result: t, hash: H(volumeTreeHashInput(t)) };
});
// hand checks of the per-file merge
const pf = merges[0].result;
assert.deepEqual(pf.entries.map((x) => x.path), ['a.json', 'b.json', 'c.json', 'e.json', 'n-cloud.json', 'n-live.json']);
assert.equal(pf.entries.find((x) => x.path === 'c.json').sha256, H('C-cloud'));
assert.equal(pf.entries.find((x) => x.path === 'b.json').sha256, H('B-live'));
assert.deepEqual(pf.dirs, ['cloud-dir', 'keep', 'live-dir']);
assert.throws(() => mergeVolumePush(normalizeVolumeTree(B), normalizeVolumeTree(L), normalizeVolumeTree(C), 'per-file', {}));

const out = {
  $comment: '§21 #101 volume content vectors (generated by shared/scripts/gen-volume-content-vectors.mjs; Go: agent/internal/volcontent). hash = "sha256:" + hex(sha256(canonical)).',
  paths: pathVectors.map(({ path, ok }) => ({ path, ok })),
  trees: treeVectors,
  excludes: excludeVectors,
  patterns: patternErrors,
  plans: planVectors,
  merges,
};
writeFileSync(new URL('../testdata/volume-content-vectors.json', import.meta.url), JSON.stringify(out, null, 2) + '\n');
console.log(`volume-content vectors: ${pathVectors.length} paths, ${treeVectors.length} trees, ${excludeVectors.length} excludes, ${planVectors.length} plans, ${merges.length} merges`);
