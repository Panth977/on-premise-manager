// §21 #107 commit staging: which blobs a commit needs, progress text, stored pending blob list.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { configBlobs, newConfigBlobs, stageProgressText, stagingObject, totalBytes, RE_STAGE_ID, STAGING_CLOUD_API, STAGING_AGENT_API } from './staging.js';
import { CLOUD_API } from './cloud-api.js';
import { AGENT_API } from './agent-api.js';
import { AUDIT_EXEMPT_OPS } from './types.js';
import { parseStoredPending, pendingBlobKey, toStoredPending } from './pending.js';
import type { Config } from './types.js';

const hello: Config = JSON.parse(readFileSync(new URL('../testdata/hello-config.json', import.meta.url), 'utf8')).config;
const clone = <T>(x: T): T => JSON.parse(JSON.stringify(x));
const sha = (c: string) => `sha256:${c.repeat(64)}`;

function withContent(): Config {
  const c = clone(hello);
  c.files = [{ ...(c.files[0] ?? { fileSlug: 'f', label: 'F', description: '', mime: 'text/plain', editor: 'text', maxBytes: 1048576, render: 'none', location: { kind: 'auto' } } as unknown as Config['files'][number]), fileSlug: 'f', content: { sha256: sha('a'), sizeBytes: 3 } }];
  const site = c.sites[0]!;
  site.config.volumes = [
    { volumeSlug: 'tree', label: '', kind: 'dir', seed: { kind: 'tree', entries: [{ path: 'a.txt', sha256: sha('b'), size: 5, mode: '0644', render: 'none' }, { path: 'dup.txt', sha256: sha('a'), size: 3, mode: '0644', render: 'none' }], dirs: [] } },
    { volumeSlug: 'one', label: '', kind: 'file', seed: { kind: 'single', sha256: sha('c'), size: 7, mode: '0644', render: 'none' } },
    { volumeSlug: 'arc', label: '', kind: 'dir', seed: { kind: 'archive', blobSha256: sha('d'), name: 'x.tar.gz', size: 11 } },
  ] as unknown as typeof site.config.volumes;
  return c;
}

test('configBlobs: files + tree + single + archive, unique, sorted', () => {
  const l = configBlobs(withContent());
  assert.deepEqual(l.map((b) => b.sha256), [sha('a'), sha('b'), sha('c'), sha('d')]);
  assert.equal(totalBytes(l), 3 + 5 + 7 + 11);
  assert.deepEqual(configBlobs(null), []);
});

test('newConfigBlobs: only what the base does not reference', () => {
  const base = withContent();
  const next = clone(base);
  next.files[0]!.content = { sha256: sha('e'), sizeBytes: 9 };
  // sha a is still used by dup.txt → not new; e is new
  assert.deepEqual(newConfigBlobs(base, next).map((b) => b.sha256), [sha('e')]);
  assert.deepEqual(newConfigBlobs(base, base), []);
  assert.deepEqual(newConfigBlobs(null, base).length, 4);
});

test('progress text + paths + ids', () => {
  assert.equal(stageProgressText(null), null);
  assert.equal(stageProgressText({ phase: 'uploading', done: 1, total: 3, bytesDone: 2 * 1048576, bytesTotal: 30 * 1048576 }), 'Uploading 1 of 3 files · 2.0 MB of 30 MB');
  assert.equal(stageProgressText({ phase: 'uploading', done: 0, total: 1, bytesDone: 0, bytesTotal: 12 }), 'Uploading 0 of 1 file · 0 B of 12 B');
  assert.equal(stageProgressText({ phase: 'committing', done: 3, total: 3, bytesDone: 1, bytesTotal: 1 }), 'Committing…');
  assert.equal(stagingObject('01HZZZZZZZZZZZZZZZZZZZZZZZ', 'ab'), 'staging/01HZZZZZZZZZZZZZZZZZZZZZZZ/ab');
  assert.ok(RE_STAGE_ID.test('01JA2B3C4D5E6F7G8H9JKMNPQR'));
  assert.ok(!RE_STAGE_ID.test('../x'));
});

test('API tables: stage ops spread, never audited', () => {
  for (const k of Object.keys(STAGING_CLOUD_API)) {
    assert.ok(k in CLOUD_API, k);
    assert.ok((AUDIT_EXEMPT_OPS as readonly string[]).includes(k), `${k} exempt`);
  }
  for (const k of Object.keys(STAGING_AGENT_API)) assert.ok(k in AGENT_API, k);
});

test('stored pending keeps the blob list (bytes live in IndexedDB)', () => {
  const s = toStoredPending({ branch: 'template:T', baseCommitId: '01H', baseSeq: 1, config: { ...clone(hello), secrets: [] }, count: 2, now: '2026-10-09T00:00:00.000Z', blobs: [sha('b'), sha('a'), sha('a')] });
  assert.deepEqual(s.blobs, [sha('a'), sha('b')]);
  const back = parseStoredPending(JSON.stringify(s), 'template:T');
  assert.deepEqual(back?.blobs, [sha('a'), sha('b')]);
  const bad = parseStoredPending(JSON.stringify({ ...s, blobs: 'x' }), 'template:T');
  assert.equal(bad?.blobs, undefined);
  assert.equal(pendingBlobKey('cloud', 'template:T', sha('a')), `opm.pending.v1.cloud.template:T|${sha('a')}`);
});
