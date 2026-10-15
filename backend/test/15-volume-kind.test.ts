/** Image volume kind (SPEC §21 #83): ImageVolume.kind dir | file on register / patch / read, inspect prefill = dir,
 *  container kind matrix (V_VOLUME_KIND_MISMATCH error, V_FILE_VOLUME_RENAME info) through the API, migrate-f. */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { newSiteVolume, type Container, type Image, type Site } from '@opm/shared';
import { adb, helloDeployment, person, rejects, sha256Hex, uniq } from './helpers.ts';
import { prefillFromConfig } from '../functions/src/routes/catalog.ts';
import { applyPlanF, planMigrationF } from '../scripts/migrate-f.ts';
import { verifySchema } from '../scripts/verify-schema.ts';

type Doc = Record<string, any>;

/** A READY node-red-like image written straight to Firestore, volumes WITHOUT kind (as stored before the fix). */
async function seedLegacyImage(slug: string) {
  const now = '2026-10-01T00:00:00.000Z';
  const audit = { createdAt: now, createdBy: 'test', updatedAt: now, updatedBy: 'test' };
  await adb.collection('services').doc(slug).set({ serviceSlug: slug, label: slug, icon: null, description: '', ...audit });
  const hex = (k: string) => sha256Hex(`${slug}:${k}`);
  const img = {
    version: 'v1',
    serviceSlug: slug,
    reference: 'docker.io/nodered/node-red:4.0',
    source: { host: 'docker.io', repository: 'nodered/node-red', tag: '4.0', digest: null },
    registryId: null,
    digest: `sha256:${hex('m')}`,
    platform: 'linux/amd64',
    import: { mode: 'copy', status: 'ready', error: null, sizeBytes: 100, manifestMediaType: 'application/vnd.oci.image.manifest.v1+json', configDigest: `sha256:${hex('c')}`, layers: [{ digest: `sha256:${hex('l')}`, sizeBytes: 100, mediaType: 'application/vnd.oci.image.layer.v1.tar+gzip' }], startedAt: now, finishedAt: now },
    kind: 'service',
    entrypoint: null,
    command: null,
    workdir: null,
    user: null,
    ports: [{ name: 'http', port: 1880, protocol: 'tcp', description: '' }],
    env: [],
    volumes: [
      { name: 'flow', mountPath: '/data/flows.json', description: '' },
      { name: 'settings', mountPath: '/data/settings.js', description: '' },
      { name: 'lib', mountPath: '/data/lib', description: '' },
    ],
    healthcheck: null,
    stopTimeoutSec: 30,
    description: '',
    ...audit,
  };
  await adb.collection('services').doc(slug).collection('images').doc('v1').set(img);
}

function nodeRed(slug: string, flowVolume: string): Container {
  return {
    containerSlug: slug,
    serviceSlug: slug,
    version: 'v1',
    enabled: true,
    env: [],
    ports: [{ portName: 'http', hostPort: null, hostIp: '0.0.0.0', edge: true }],
    volumes: [
      { imageVolume: 'flow', mountPath: null, source: { kind: 'volume', volumeSlug: flowVolume }, readOnly: false },
      { imageVolume: 'settings', mountPath: null, source: { kind: 'none' }, readOnly: false },
      { imageVolume: 'lib', mountPath: null, source: { kind: 'none' }, readOnly: false },
    ],
    command: null,
    entrypoint: null,
    restart: 'always',
    resources: { cpus: null, memoryMb: null },
    healthcheck: null,
    depends: [],
  };
}

test('register / patch / read: kind explicit (absent → inferred from the path), invalid kind → V_ENUM, inspect volumes = dir', async () => {
  const admin = await person('admin');
  const slug = uniq('vk');
  await admin.api.createService({ body: { label: slug, serviceSlug: slug } });
  const reg = await admin.api.createRegistry({ body: { label: 'dead', url: 'https://127.0.0.1:1', username: 'u', password: 'p' } });
  const body = {
    version: 'v1',
    reference: '127.0.0.1:1/acme/node-red:4',
    registryId: reg.registryId,
    mode: 'copy' as const,
    kind: 'service' as const,
    volumes: [
      { name: 'flow', mountPath: '/data/flows.json', description: '' }, // auto → file
      { name: 'data', mountPath: '/data', description: '' }, // auto → dir
      { name: 'conf', mountPath: '/etc/app.conf', kind: 'dir' as const, description: '' }, // explicit wins
    ],
  };
  const img = await admin.api.registerImage({ serviceSlug: slug, body });
  assert.deepEqual(img.volumes.map((v) => [v.name, v.kind]), [['flow', 'file'], ['data', 'dir'], ['conf', 'dir']]);
  const stored = (await adb.collection('services').doc(slug).collection('images').doc('v1').get()).data()!;
  assert.deepEqual(stored.volumes.map((v: Doc) => v.kind), ['file', 'dir', 'dir'], 'stored explicitly');
  const bad = await rejects(admin.api.registerImage({ serviceSlug: slug, body: { ...body, version: 'v2', volumes: [{ name: 'x', mountPath: '/x', kind: 'folder' as never, description: '' }] } }), 'invalid', 422);
  assert.ok(JSON.stringify(bad.details).includes('volumes[x].kind') && JSON.stringify(bad.details).includes('V_ENUM'), JSON.stringify(bad.details));
  const slash = await rejects(admin.api.registerImage({ serviceSlug: slug, body: { ...body, version: 'v3', volumes: [{ name: 'x', mountPath: '/x/', kind: 'file', description: '' }] } }), 'invalid', 422);
  assert.match(JSON.stringify(slash.details), /V_VOLUME_KIND/);
  // patch: change kind (Folder → File) + a new row without kind (inferred)
  const p = await admin.api.patchImage({
    serviceSlug: slug,
    version: 'v1',
    body: { volumes: [...img.volumes.map((v) => (v.name === 'conf' ? { ...v, kind: 'file' as const } : v)), { name: 'settings', mountPath: '/data/settings.js', description: '' }] },
  });
  assert.deepEqual(p.volumes.map((v) => [v.name, v.kind]), [['flow', 'file'], ['data', 'dir'], ['conf', 'file'], ['settings', 'file']]);
  const st2 = (await adb.collection('services').doc(slug).collection('images').doc('v1').get()).data()!;
  assert.deepEqual(st2.volumes.map((v: Doc) => v.kind), ['file', 'dir', 'file', 'file']);
  const pb = await rejects(admin.api.patchImage({ serviceSlug: slug, version: 'v1', body: { volumes: [{ name: 'flow', mountPath: '/data/flows.json', kind: 'blob' as never, description: '' }] } }), 'invalid', 422);
  assert.match(JSON.stringify(pb.details), /V_ENUM/);
  // registry VOLUME entries are folders, even with an extension in the path
  const pre = prefillFromConfig({ config: { Volumes: { '/data': {}, '/data/flows.json': {} } } } as never);
  assert.deepEqual(pre.volumes.map((v) => [v.mountPath, v.kind]), [['/data', 'dir'], ['/data/flows.json', 'dir']]);
});

test('container kind matrix through the draft API + migrate-f (legacy image: dry run → apply → rerun 0, verify-schema, audit)', async () => {
  const admin = await person('admin');
  const slug = uniq('nr');
  await seedLegacyImage(slug);
  // legacy doc read through the API: kinds inferred
  const got = await admin.api.getImage({ serviceSlug: slug, version: 'v1' });
  assert.deepEqual(got.volumes.map((v) => [v.name, v.kind]), [['flow', 'file'], ['settings', 'file'], ['lib', 'dir']]);
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const draft = await admin.api.getDeployment({ deploymentId: id });
  const app = structuredClone(draft.draft.sites.find((s) => s.siteSlug === 'app')!) as Site;
  app.config.volumes.push({ ...newSiteVolume('nodered-flow'), kind: 'file' }, newSiteVolume('nodered-data'));
  // folder volume on the File image volume flow → error (save level)
  app.config.containers.push(nodeRed(slug, 'nodered-data'));
  const e = await rejects(admin.api.putDraftSite({ deploymentId: id, siteSlug: 'app', body: { ifRevision: 0, site: app } }), 'invalid', 422);
  const issue = (e.details.errors as Doc[]).find((x) => x.code === 'V_VOLUME_KIND_MISMATCH');
  assert.ok(issue, JSON.stringify(e.details));
  assert.equal(issue.path, `sites[app].config.containers[${slug}].volumes[flow].source.volumeSlug`);
  assert.match(issue.message, /folder volume nodered-data cannot be mounted on file path \/data\/flows\.json .* use a single-file volume, or change the image volume to Folder/);
  // the user's case: single-file volume on the File image volume → no V_VOLUME_KIND_IMAGE, only the rename info
  app.config.containers[app.config.containers.length - 1] = nodeRed(slug, 'nodered-flow');
  app.config.volumes = app.config.volumes.filter((v) => v.volumeSlug !== 'nodered-data');
  const w = await admin.api.putDraftSite({ deploymentId: id, siteSlug: 'app', body: { ifRevision: 0, site: app } });
  const mine = w.validation.issues.filter((x) => x.path.includes(`containers[${slug}]`));
  assert.deepEqual(
    mine.filter((x) => x.code.startsWith('V_VOLUME_KIND') || x.code === 'V_FILE_VOLUME_RENAME').map((x) => [x.code, x.severity]),
    [['V_FILE_VOLUME_RENAME', 'info']],
    JSON.stringify(mine),
  );
  // migrate-f: dry run
  const vs0 = (await verifySchema(adb)).filter((l) => l.startsWith(`services/${slug}/images/v1`));
  assert.ok(vs0.some((l) => /volume flow: kind missing/.test(l)), vs0.join('\n'));
  const plan = await planMigrationF(adb);
  const ip = `services/${slug}/images/v1`;
  const op = plan.ops.find((o) => o.path === ip)!;
  assert.ok(op, plan.ops.map((o) => o.path).join('\n'));
  const doc = plan.docs.find((d) => d.path.startsWith(ip))!;
  assert.deepEqual(doc.lines, ['volume flow (/data/flows.json) → kind file (File)', 'volume settings (/data/settings.js) → kind file (File)', 'volume lib (/data/lib) → kind dir (Folder)']);
  const note = plan.docs.find((d) => d.path.startsWith(`deployments/${id} `))!;
  assert.ok(note.notes.some((n) => n.includes(`container ${slug} image volume flow: single-file volume nodered-flow on a File image volume — V_VOLUME_KIND_IMAGE warning gone`)), JSON.stringify(note));
  assert.equal((await adb.doc(ip).get()).data()!.volumes[0].kind, undefined, 'dry run wrote nothing');
  // apply
  await applyPlanF(adb, plan);
  const after = (await adb.doc(ip).get()).data()!;
  assert.deepEqual(after.volumes.map((v: Doc) => [v.name, v.kind, v.mountPath, v.description]), [['flow', 'file', '/data/flows.json', ''], ['settings', 'file', '/data/settings.js', ''], ['lib', 'dir', '/data/lib', '']]);
  assert.equal(after.updatedAt, '2026-10-01T00:00:00.000Z', 'updatedAt unchanged');
  const audit = (await adb.collection('auditLog').where('action', '==', 'system.migration').get()).docs.map((x) => ({ op: x.data().op, data: JSON.parse(x.data()._data) })).filter((x) => x.data.migration === 'f');
  assert.ok(audit.some((a) => a.op === 'system:migrate-f' && a.data.docs.some((d: Doc) => d.path.startsWith(ip))), JSON.stringify(audit).slice(0, 400));
  // idempotent + verify-schema clean for this image
  const again = await planMigrationF(adb);
  assert.ok(!again.ops.some((o) => o.path === ip));
  assert.deepEqual((await verifySchema(adb)).filter((l) => l.startsWith(ip)), []);
});
