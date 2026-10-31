/** Image import against the real Artifact Registry test repo (skipped when offline or without the reader key). */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { abucket, here, person, rejects, sleep, uniq } from './helpers.ts';
import { fetchLayer } from '../src/core/importer.ts';

const KEY = resolve(here, '../../.secrets/registry-reader-key.json');
const REF = 'registry.example.com/opm-test/busybox:1.36';
const AR = 'https://asia-south1-docker.pkg.dev';

async function online(): Promise<boolean> {
  try {
    const r = await fetch(`${AR}/v2/`, { signal: AbortSignal.timeout(5000) });
    return r.status === 401 || r.ok;
  } catch {
    return false;
  }
}
const skip = !existsSync(KEY) ? 'no registry reader key' : !(await online()) ? 'offline' : false;

test('registry test + inspect + copy import + gateway import against Artifact Registry', { skip, timeout: 600_000 }, async () => {
  const admin = await person('admin');
  const reg = await admin.api.createRegistry({ body: { label: 'AR test', url: AR, username: '_json_key', password: readFileSync(KEY, 'utf8') } });
  const svc = await admin.api.createService({ body: { label: 'Busybox', serviceSlug: uniq('busybox'), icon: 'lucide:box' } });
  const tst = await admin.api.testRegistry({ registryId: reg.registryId });
  assert.equal(tst.ok, true, tst.message);
  assert.equal(tst.message, 'login ok');
  const bad = await admin.api.createRegistry({ body: { label: 'AR bad', url: AR, username: '_json_key', password: '{"bad":true}' } });
  assert.equal((await admin.api.testRegistry({ registryId: bad.registryId })).ok, false);
  const ins = await admin.api.inspectImage({ serviceSlug: svc.serviceSlug, body: { reference: REF, registryId: reg.registryId, platform: 'linux/arm64' } });
  assert.match(ins.digest, /^sha256:[0-9a-f]{64}$/);
  assert.equal(ins.layers, 1);
  assert.ok(ins.sizeBytes > 100_000);
  assert.deepEqual(ins.command, ['sh']);
  await rejects(admin.api.inspectImage({ serviceSlug: svc.serviceSlug, body: { reference: REF, registryId: reg.registryId, platform: 'linux/amd64' } }), 'invalid');
  await rejects(admin.api.inspectImage({ serviceSlug: svc.serviceSlug, body: { reference: REF.replace(':1.36', ':no-such-tag'), registryId: reg.registryId, platform: 'linux/arm64' } }), 'not-found');
  // gateway mode: manifest + config only
  const gsvc = await admin.api.createService({ body: { label: 'Busybox gw', serviceSlug: uniq('bbgw'), icon: 'lucide:box' } });
  const layerHex = '821d9dafb26dc8e4afce2273366520f49e38f8f679a72fc512f2a83e62f442b7';
  await abucket.file(`blobs/sha256/${layerHex}`).delete({ ignoreNotFound: true });
  await admin.api.registerImage({ serviceSlug: gsvc.serviceSlug, body: { version: 'v1', reference: REF, registryId: reg.registryId, platform: 'linux/arm64', mode: 'gateway', kind: 'service', command: ['sh'] } });
  let g = await admin.api.getImage({ serviceSlug: gsvc.serviceSlug, version: 'v1' });
  for (let i = 0; i < 240 && (g.import.status === 'pending' || g.import.status === 'copying'); i++) {
    await sleep(500);
    g = await admin.api.getImage({ serviceSlug: gsvc.serviceSlug, version: 'v1' });
  }
  assert.equal(g.import.status, 'ready', g.import.error ?? '');
  assert.equal(g.digest, ins.digest);
  assert.equal((await abucket.file(`blobs/sha256/${g.digest!.slice(7)}`).exists())[0], true, 'manifest stored');
  assert.equal((await abucket.file(`blobs/sha256/${g.import.configDigest!.slice(7)}`).exists())[0], true, 'config stored');
  assert.equal((await abucket.file(`blobs/sha256/${layerHex}`).exists())[0], false, 'gateway does not copy layers');
  await fetchLayer({ ...g, registryId: reg.registryId }, `sha256:${layerHex}`);
  assert.equal((await abucket.file(`blobs/sha256/${layerHex}`).exists())[0], true, 'first fetch streams the layer into Storage');
  await abucket.file(`blobs/sha256/${layerHex}`).delete({ ignoreNotFound: true });
  // copy mode
  const img = await admin.api.registerImage({ serviceSlug: svc.serviceSlug, body: { version: 'v1', reference: REF, registryId: reg.registryId, platform: 'linux/arm64', mode: 'copy', kind: 'service', command: ['sh'] } });
  assert.equal(img.import.status, 'pending');
  let cur = img;
  for (let i = 0; i < 600 && (cur.import.status === 'pending' || cur.import.status === 'copying'); i++) {
    await sleep(500);
    cur = await admin.api.getImage({ serviceSlug: svc.serviceSlug, version: 'v1' });
  }
  assert.equal(cur.import.status, 'ready', cur.import.error ?? '');
  assert.equal(cur.digest, ins.digest);
  assert.equal(cur.import.layers.length, 1);
  assert.equal(cur.import.layers[0]!.digest, `sha256:${layerHex}`);
  assert.equal(cur.import.sizeBytes, cur.import.layers[0]!.sizeBytes);
  assert.ok(cur.import.startedAt && cur.import.finishedAt);
  const [meta] = await abucket.file(`blobs/sha256/${layerHex}`).getMetadata();
  assert.equal(Number(meta.size), cur.import.layers[0]!.sizeBytes, 'layer copied, size matches');
  // wrong platform → failed with the message
  const asvc = await admin.api.createService({ body: { label: 'Busybox amd', serviceSlug: uniq('bbamd'), icon: 'lucide:box' } });
  await admin.api.registerImage({ serviceSlug: asvc.serviceSlug, body: { version: 'v1', reference: REF, registryId: reg.registryId, platform: 'linux/amd64', mode: 'copy', kind: 'service' } });
  let a = await admin.api.getImage({ serviceSlug: asvc.serviceSlug, version: 'v1' });
  for (let i = 0; i < 240 && (a.import.status === 'pending' || a.import.status === 'copying'); i++) {
    await sleep(500);
    a = await admin.api.getImage({ serviceSlug: asvc.serviceSlug, version: 'v1' });
  }
  assert.equal(a.import.status, 'failed');
  assert.match(a.import.error ?? '', /platform linux\/amd64 not in the image/);
});
