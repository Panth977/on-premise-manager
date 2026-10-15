import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { Issues, imageVolumeKind, inferImageVolumeKind, normalizeImageVolume, validateConfig, validateImageVolumes, type ValidationContext } from './validate.js';
import type { Config, ContainerMount, ImageVolume } from './types.js';

// shared/testdata/volume-kind-vectors.json (§21 #83) — the Go mirror reads the same file (agent/internal/validate/volumekind_test.go).
const load = (f: string) => JSON.parse(readFileSync(fileURLToPath(new URL(`../testdata/${f}`, import.meta.url)), 'utf8'));
const HELLO = load('hello-config.json') as { deploymentId: string; config: Config };
const CAT = load('hello-catalog.json') as Omit<ValidationContext, 'mode' | 'deploymentId'>;
interface MountCase {
  name: string;
  imagePath: string;
  imageKind: 'dir' | 'file' | null;
  siteKind: 'dir' | 'file';
  row: { imageVolume: string | null; mountPath: string | null; readOnly: boolean; source: 'volume' | 'file' | 'secret' };
  expect: { path: string; code: string; severity: string }[];
}
const V = load('volume-kind-vectors.json') as {
  codes: string[];
  modes: ValidationContext['mode'][];
  infer: { mountPath: string; kind: 'dir' | 'file' }[];
  imageVolumes: { name: string; volume: ImageVolume; expect: MountCase['expect'] }[];
  mounts: MountCase[];
};

test('volume kind vectors: inferImageVolumeKind', () => {
  for (const x of V.infer) assert.equal(inferImageVolumeKind(x.mountPath), x.kind, x.mountPath);
});

for (const c of V.imageVolumes)
  test(`volume kind vector (image): ${c.name}`, () => {
    const is = new Issues('publish');
    validateImageVolumes([c.volume], is);
    assert.deepEqual(
      is.list.map(({ path, code, severity }) => ({ path, code, severity })),
      c.expect,
    );
  });

function build(c: MountCase): { config: Config; images: ValidationContext['images'] } {
  const config = structuredClone(HELLO.config);
  const images = structuredClone(CAT.images);
  const iv = { name: 't', mountPath: c.imagePath, description: '' } as ImageVolume;
  if (c.imageKind) iv.kind = c.imageKind;
  images['hello-api:v1']!.volumes.push(iv);
  const app = config.sites.find((s) => s.siteSlug === 'app')!.config;
  app.volumes.push({ volumeSlug: 'tv', label: 'tv', description: '', backup: true, location: { kind: 'auto' }, kind: c.siteKind, seed: null });
  const source: ContainerMount['source'] =
    c.row.source === 'volume' ? { kind: 'volume', volumeSlug: 'tv' } : c.row.source === 'file' ? { kind: 'file', fileSlug: 'banner' } : { kind: 'secret', secretSlug: 'api-token' };
  app.containers.find((x) => x.containerSlug === 'hello-api')!.volumes.push({ imageVolume: c.row.imageVolume, mountPath: c.row.mountPath, readOnly: c.row.readOnly, source });
  return { config, images };
}

for (const c of V.mounts)
  for (const mode of V.modes)
    test(`volume kind vector (${mode}): ${c.name}`, () => {
      const { config, images } = build(c);
      const is = validateConfig(config, { mode, deploymentId: HELLO.deploymentId, services: CAT.services, images, notFoundPageIds: CAT.notFoundPageIds });
      assert.deepEqual(
        is.list.filter((x) => V.codes.includes(x.code)).map(({ path, code, severity }) => ({ path, code, severity })),
        c.expect,
      );
    });

test('normalizeImageVolume / imageVolumeKind: explicit kind wins, absent → inferred, idempotent', () => {
  const a = normalizeImageVolume({ name: 'flow', mountPath: '/data/flows.json', description: '' });
  assert.equal(a.kind, 'file');
  assert.deepEqual(normalizeImageVolume(a), a);
  assert.equal(normalizeImageVolume({ name: 'flow', mountPath: '/data/flows.json', kind: 'dir', description: '' }).kind, 'dir');
  assert.equal(imageVolumeKind({ mountPath: '/data' }), 'dir');
  assert.equal(imageVolumeKind({ mountPath: '/data', kind: 'file' }), 'file');
});
