// §21 #101 volume content: vectors (shared with Go), editor ops, validation, migration, diff.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import {
  cloudSyncState,
  compileExcludes,
  contentPathError,
  countTreeChanges,
  excludePatternError,
  isExcluded,
  mergeVolumePush,
  migrateVolumeFileSeeds,
  normalizeVolumeTree,
  planVolumePush,
  treeDelete,
  treeDuplicate,
  treeMkdir,
  treeMove,
  treeNodes,
  treePutFile,
  usesVolumeContent,
  volumeTreeHash,
  volumeTreeHashInput,
  volumeTreeIssues,
  VOLUME_CONTENT_CLOUD_API,
  TreeEditError,
} from './volume-content.js';
import { CLOUD_API } from './cloud-api.js';
import { checkLocalEdit, defaultSiteConfig, normalizeSiteVolume, validateConfig, VALIDATION_CODES, type ValidationContext } from './validate.js';
import { diffConfigs } from './diff.js';
import { ACTIVITY_TYPES, type Config, type VolumeTree } from './types.js';

const V = JSON.parse(readFileSync(new URL('../testdata/volume-content-vectors.json', import.meta.url), 'utf8'));
const hex = (s: string) => createHash('sha256').update(s).digest('hex');
const H = (s: string) => `sha256:${hex(s)}`;

test('vectors: paths', () => {
  for (const v of V.paths) assert.equal(contentPathError(v.path) === null, v.ok, JSON.stringify(v.path).slice(0, 60));
});
test('vectors: canonical trees + hashes', async () => {
  for (const v of V.trees) {
    assert.equal(volumeTreeHashInput(v.tree), v.canonical, v.name);
    assert.equal(await volumeTreeHash(v.tree, hex), v.hash, v.name);
    assert.deepEqual(normalizeVolumeTree(v.tree), v.normalized, v.name);
    // idempotent + order-independent
    assert.equal(volumeTreeHashInput(normalizeVolumeTree(v.tree)), v.canonical);
    assert.equal(volumeTreeHashInput({ entries: [...v.tree.entries].reverse(), dirs: [...v.tree.dirs].reverse() }), v.canonical);
  }
});
test('vectors: excludes + patterns', () => {
  for (const v of V.excludes) assert.equal(isExcluded(v.path, v.isDir, compileExcludes(v.patterns)), v.excluded, `${v.patterns} ${v.path}`);
  for (const v of V.patterns) assert.equal(excludePatternError(v.pattern) === null, v.ok, v.pattern);
});
test('vectors: plans + merges', () => {
  for (const p of V.plans) {
    const items = planVolumePush(p.base && normalizeVolumeTree(p.base), normalizeVolumeTree(p.live), normalizeVolumeTree(p.cloud));
    assert.deepEqual(
      items.map((i) => ({ path: i.path, kind: i.kind, liveOp: i.liveOp, cloudOp: i.cloudOp })),
      p.items,
      p.name,
    );
  }
  for (const m of V.merges) {
    const p = V.plans.find((x: { name: string }) => x.name === m.plan);
    const t = mergeVolumePush(normalizeVolumeTree(p.base), normalizeVolumeTree(p.live), normalizeVolumeTree(p.cloud), m.resolution, m.choices);
    assert.equal(H(volumeTreeHashInput(t)), m.hash, m.name);
  }
});

const ent = (path: string, body = path) => ({ path, sha256: H(body), size: body.length, mode: '0644', render: 'none' as const });
test('editor ops: create, mkdir, move, rename, duplicate, delete', () => {
  let t: VolumeTree = { entries: [], dirs: [] };
  t = treePutFile(t, ent('flows.json'));
  t = treeMkdir(t, 'lib');
  t = treePutFile(t, ent('lib/a.js'));
  assert.deepEqual(t.dirs, []); // lib is implied by lib/a.js
  assert.throws(() => treePutFile(t, ent('flows.json')), TreeEditError);
  t = treePutFile(t, { ...ent('flows.json', 'new'), path: 'flows.json' }, { overwrite: true });
  t = treeMove(t, 'lib', 'src/lib');
  assert.deepEqual(t.entries.map((e) => e.path), ['flows.json', 'src/lib/a.js']);
  assert.throws(() => treeMove(t, 'src', 'src/lib/x'), TreeEditError);
  t = treeMove(t, 'flows.json', 'flows2.json'); // rename
  t = treeDuplicate(t, 'flows2.json');
  assert.ok(t.entries.some((e) => e.path === 'flows2 copy.json'));
  t = treeDuplicate(t, 'src');
  assert.ok(t.entries.some((e) => e.path === 'src copy/lib/a.js'));
  t = treeDelete(t, 'src/lib/a.js');
  assert.deepEqual(t.dirs, ['src/lib']); // the parent stays as an empty folder
  t = treeDelete(t, 'src');
  assert.ok(!t.entries.some((e) => e.path.startsWith('src/')));
  const nodes = treeNodes(t);
  assert.equal(nodes[0]!.dir, true); // folders first
  assert.throws(() => treeMkdir(t, '../x'), TreeEditError);
});
test('tree issues', () => {
  assert.deepEqual(volumeTreeIssues({ entries: [ent('a'), ent('a/b')], dirs: [] }, true).map((x) => x.code), ['V_VOLUME_CONTENT']);
  assert.deepEqual(volumeTreeIssues({ entries: [ent('a'), ent('a')], dirs: [] }, true).map((x) => x.code), ['V_DUPLICATE']);
  assert.deepEqual(volumeTreeIssues({ entries: [{ ...ent('x'), render: 'expressions' as const }], dirs: [] }, false).map((x) => x.code), ['V_VOLUME_CONTENT']);
  assert.deepEqual(volumeTreeIssues({ entries: [{ ...ent('x'), mode: '644' }], dirs: [] }, true).map((x) => x.code), ['V_FORMAT']);
  assert.equal(countTreeChanges({ entries: [ent('a'), ent('b')], dirs: [] }, { entries: [ent('a', 'x'), ent('c')], dirs: [] }).modified, 1);
});

// ── validation + migration on a Node-RED site (the user's real shape)
const img = {
  kind: 'service' as const,
  ports: [{ name: 'http', number: 1880, protocol: 'tcp' as const, description: '' }],
  env: [],
  volumes: [
    { name: 'data', mountPath: '/data', kind: 'dir' as const, description: '' },
    { name: 'settings', mountPath: '/data/settings.js', kind: 'file' as const, description: '' },
  ],
};
const ctx = (mode: ValidationContext['mode']): ValidationContext => ({ mode, deploymentId: 'd1', services: { 'node-red': {} }, images: { 'node-red:v1': img as never } });
function legacyConfig(): Config {
  const sc = defaultSiteConfig();
  sc.volumes = [{ volumeSlug: 'nodered-data', label: 'nodered-data', description: '', backup: true, location: { kind: 'auto' }, kind: 'dir', seed: { kind: 'files', entries: [{ path: 'flows.json', fileSlug: 'nodered-flow' }] } }];
  sc.containers = [
    {
      containerSlug: 'node-red',
      serviceSlug: 'node-red',
      version: 'v1',
      enabled: true,
      env: [],
      ports: [],
      volumes: [
        { imageVolume: 'data', mountPath: null, source: { kind: 'volume', volumeSlug: 'nodered-data' }, readOnly: false },
        { imageVolume: 'settings', mountPath: null, source: { kind: 'file', fileSlug: 'nodered-settings' }, readOnly: true },
      ],
      command: null,
      entrypoint: null,
      restart: 'always',
      resources: { cpus: null, memoryMb: null },
      healthcheck: null,
      depends: [],
    },
  ];
  const file = (slug: string, body: string, mime: string) => ({ fileSlug: slug, label: slug, description: '', mime, editor: 'text' as const, maxBytes: 1048576, content: { sha256: H(body), sizeBytes: body.length }, render: 'expressions' as const, location: { kind: 'auto' as const } });
  return { variables: [], secrets: [], files: [file('nodered-flow', '[{"url":"{{ containers.x.host }}"}]', 'application/json'), file('nodered-settings', 'module.exports={}', 'application/javascript')], sites: [{ siteSlug: 'site-01', label: 'Site 01', config: sc }] };
}
test('validation: legacy file seeds + a File inside a writable volume are errors (agent mode: accepted / warning)', () => {
  const cfg = legacyConfig();
  const codes = (m: ValidationContext['mode']) => validateConfig(cfg, ctx(m)).list.filter((x) => x.severity === 'error').map((x) => x.code);
  assert.ok(codes('draft').includes('V_VOLUME_SEED_FILE_REF'));
  assert.ok(codes('draft').includes('V_FILE_IN_WRITABLE_VOLUME'));
  assert.ok(codes('template').includes('V_VOLUME_SEED_FILE_REF'));
  assert.ok(!codes('agent').includes('V_VOLUME_SEED_FILE_REF'));
  assert.ok(!codes('agent').includes('V_FILE_IN_WRITABLE_VOLUME'));
  assert.ok(validateConfig(cfg, ctx('agent')).list.some((x) => x.code === 'V_FILE_IN_WRITABLE_VOLUME' && x.severity === 'warning'));
  for (const c of ['V_VOLUME_SEED_FILE_REF', 'V_FILE_IN_WRITABLE_VOLUME', 'V_VOLUME_PERSIST', 'V_VOLUME_CONTENT_PATH', 'V_LOCAL_VOLUME_CONTENT']) assert.ok(VALIDATION_CODES[c], c);
  // a writable File mount is still V_READONLY
  const c2 = legacyConfig();
  c2.sites[0]!.config.containers[0]!.volumes[1]!.readOnly = false;
  assert.ok(validateConfig(c2, ctx('draft')).list.some((x) => x.code === 'V_READONLY'));
  // read-only volume mount → no nesting error
  const c3 = legacyConfig();
  c3.sites[0]!.config.containers[0]!.volumes[0]!.readOnly = true;
  assert.ok(!validateConfig(c3, ctx('draft')).list.some((x) => x.code === 'V_FILE_IN_WRITABLE_VOLUME'));
});
test('migration: files seed → tree, nested settings File → volume content, unused Files removed, idempotent', () => {
  const ivs = () => img.volumes;
  const m = migrateVolumeFileSeeds(legacyConfig(), ivs);
  assert.equal(m.changed, true);
  const v = m.config.sites[0]!.config.volumes[0]!;
  assert.equal(v.seed?.kind, 'tree');
  const tree = v.seed as Extract<typeof v.seed, { kind: 'tree' }>;
  assert.deepEqual(tree.entries.map((e) => [e.path, e.render, e.mode]), [['flows.json', 'expressions', '0644'], ['settings.js', 'expressions', '0644']]);
  assert.equal(tree.entries[0]!.sha256, H('[{"url":"{{ containers.x.host }}"}]'));
  assert.deepEqual(m.removedFiles.sort(), ['nodered-flow', 'nodered-settings']);
  assert.deepEqual(m.config.files, []);
  assert.deepEqual(m.config.sites[0]!.config.containers[0]!.volumes[1]!.source, { kind: 'none' });
  assert.deepEqual(m.moved, [{ siteSlug: 'site-01', containerSlug: 'node-red', fileSlug: 'nodered-settings', volumeSlug: 'nodered-data', path: 'settings.js' }]);
  const errs = validateConfig(m.config, ctx('publish')).list.filter((x) => x.severity === 'error');
  assert.deepEqual(errs, []);
  const again = migrateVolumeFileSeeds(m.config, ivs);
  assert.equal(again.changed, false);
  assert.deepEqual(again.config, m.config);
  // a File still mounted read-only elsewhere is kept
  const c = legacyConfig();
  c.sites[0]!.config.containers[0]!.volumes.push({ imageVolume: null, mountPath: '/etc/flow.json', source: { kind: 'file', fileSlug: 'nodered-flow' }, readOnly: true });
  const m2 = migrateVolumeFileSeeds(c, ivs);
  assert.ok(m2.config.files.some((f) => f.fileSlug === 'nodered-flow'));
  assert.ok(m2.lines.some((l) => l.includes('file nodered-flow kept')));
  assert.ok(usesVolumeContent(m.config.sites[0]!.config));
  assert.ok(!usesVolumeContent(legacyConfig().sites[0]!.config));
});
test('persist: omitted when off, validated when on, folder volumes only; local edits cannot change persist', () => {
  const v = normalizeSiteVolume({ volumeSlug: 'x', persist: undefined } as never);
  assert.ok(!('persist' in v));
  const m = migrateVolumeFileSeeds(legacyConfig(), () => img.volumes).config;
  m.sites[0]!.config.volumes[0]!.persist = { exclude: ['node_modules/', '!x'], maxBytes: 10, auto: true };
  const codes = validateConfig(m, ctx('draft')).list.map((x) => x.code);
  assert.ok(codes.includes('V_VOLUME_PERSIST'));
  assert.ok(codes.includes('V_RANGE'));
  const after = structuredClone(m);
  after.sites[0]!.config.volumes[0]!.persist = { exclude: [], maxBytes: 1048576 * 5, auto: false };
  assert.ok(checkLocalEdit(m, after).some((x) => x.code === 'V_LOCAL_VOLUME_CONTENT'));
});
test('diff: per-file content changes with blob refs', () => {
  const a = migrateVolumeFileSeeds(legacyConfig(), () => img.volumes).config;
  const b = structuredClone(a);
  const t = b.sites[0]!.config.volumes[0]!.seed as Extract<NonNullable<(typeof b.sites)[0]['config']['volumes'][0]['seed']>, { kind: 'tree' }>;
  t.entries[0] = { ...t.entries[0]!, sha256: H('changed'), size: 7 };
  t.entries.push(ent('lib/new.js'));
  const ch = diffConfigs(a, b).filter((c) => c.path.includes('/content/'));
  assert.deepEqual(ch.map((c) => [c.path, c.kind]), [['site-01/volumes/nodered-data/content/flows.json', 'changed'], ['site-01/volumes/nodered-data/content/lib/new.js', 'added']]);
  assert.equal(ch[0]!.blob?.after, H('changed'));
});
test('API + activities', () => {
  for (const k of Object.keys(VOLUME_CONTENT_CLOUD_API)) assert.ok(k in CLOUD_API, k);
  for (const t of ['volume-content.snapshot', 'volume-content.applied', 'volume-content.sent', 'volume.placeholder-filled']) assert.ok((ACTIVITY_TYPES as readonly string[]).includes(t), t);
});

test('cloud sync state: a pending push is a conflict only when the agent did not find a clean merge', () => {
  const push = { status: 'pending', treeHash: 'sha256:c', baseTreeHash: 'sha256:b' } as never;
  const live = (liveTreeHash: string, state: string) => ({ volumeSlug: 'v', state, liveTreeHash, snapshotTreeHash: null, liveChanged: true, queued: 0, scannedAt: null, lastError: null, reportedAt: '' }) as never;
  assert.equal(cloudSyncState({ push, lastSnapshot: null, live: live('sha256:b', 'cloud-ahead') }), 'cloud-ahead');
  assert.equal(cloudSyncState({ push, lastSnapshot: null, live: live('sha256:c', 'in-sync') }), 'in-sync');
  assert.equal(cloudSyncState({ push, lastSnapshot: null, live: live('sha256:l', 'conflict') }), 'conflict');
  // live ≠ base, but the agent's per-file plan has no conflict
  assert.equal(cloudSyncState({ push, lastSnapshot: null, live: live('sha256:l', 'cloud-ahead') }), 'cloud-ahead');
});
