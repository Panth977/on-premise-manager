/** Image edit (SPEC-final §6.1 patchImage, §21 #82): everything but the identity is editable; in-use renames / removals. */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { defaultSiteConfig, newSiteVolume, type Config, type Container, type EdgeRoute, type Image, type ImageInUseDetails } from '@opm/shared';
import { adb, HELLO, person, rejects, sha256Hex, uniq, type Person, publish } from './helpers.ts';

type Doc = Record<string, any>;

/** A READY image written straight to Firestore (no import), unique service. */
async function seedImage(slug: string, version: string) {
  const now = new Date().toISOString();
  const audit = { createdAt: now, createdBy: 'test', updatedAt: now, updatedBy: 'test' };
  await adb.collection('services').doc(slug).set({ serviceSlug: slug, label: slug, icon: null, description: '', ...audit });
  const hex = (k: string) => sha256Hex(`${slug}:${version}:${k}`);
  const layers = [{ digest: `sha256:${hex('l1')}`, sizeBytes: 100, mediaType: 'application/vnd.oci.image.layer.v1.tar+gzip' }];
  const img: Image = {
    version,
    serviceSlug: slug,
    reference: `docker.io/library/busybox:${version}`,
    source: { host: 'docker.io', repository: 'library/busybox', tag: version, digest: null },
    registryId: null,
    digest: `sha256:${hex('m')}`,
    platform: 'linux/amd64',
    import: { mode: 'copy', status: 'ready', error: null, sizeBytes: 100, manifestMediaType: 'application/vnd.oci.image.manifest.v1+json', configDigest: `sha256:${hex('c')}`, layers, startedAt: now, finishedAt: now },
    kind: 'service',
    entrypoint: null,
    command: null,
    workdir: null,
    user: null,
    ports: [
      { name: 'http', port: 8080, protocol: 'tcp', description: 'HTTP' },
      { name: 'metrics', port: 9100, protocol: 'tcp', description: '' },
    ],
    env: [
      { name: 'NODE_ENV', value: 'prod', options: ['prod', 'dev'], required: false, secret: false, description: '' },
      { name: 'LOG', value: 'info', options: null, required: false, secret: false, description: '' },
    ],
    volumes: [
      { name: 'data', mountPath: '/data', kind: 'dir', description: '' },
      { name: 'cache', mountPath: '/cache', kind: 'dir', description: '' },
    ],
    healthcheck: { type: 'http', portName: 'http', path: '/healthz', expectStatus: 200, intervalSec: 10, timeoutSec: 5, retries: 3, startPeriodSec: 5 },
    stopTimeoutSec: 30,
    description: '',
    ...audit,
  };
  await adb.collection('services').doc(slug).collection('images').doc(version).set(img);
  return img;
}

/** One site "app": container <slug> (ports http published, env NODE_ENV + an expression, volume data), a second container
 *  of another version reading site.app.port.<slug>.http, an edge route to <slug>:http. */
function config(slug: string): Pick<Config, 'variables' | 'secrets' | 'files' | 'sites'> {
  const sc = defaultSiteConfig();
  sc.volumes = [newSiteVolume('app-data')];
  const c = (cs: string, version: string, hostPort: number | null): Container => ({
    containerSlug: cs,
    serviceSlug: slug,
    version,
    enabled: true,
    env: [
      { name: 'NODE_ENV', value: 'dev', secretRef: null },
      { name: 'CFG', value: '/cfg/{{ container.env.NODE_ENV }}.json', secretRef: null },
    ],
    ports: [
      { portName: 'http', hostPort, hostIp: '0.0.0.0', edge: true },
      { portName: 'metrics', hostPort: null, hostIp: '0.0.0.0', edge: true },
    ],
    volumes: [
      { imageVolume: 'data', mountPath: null, source: { kind: 'volume', volumeSlug: 'app-data' }, readOnly: false },
      { imageVolume: 'cache', mountPath: null, source: { kind: 'none' }, readOnly: false },
    ],
    command: null,
    entrypoint: null,
    restart: 'always',
    resources: { cpus: null, memoryMb: null },
    healthcheck: null,
    depends: [],
  });
  const main = c(slug, 'v1', 18080);
  const other = c(`${slug}-b`, 'v2', null);
  other.env.push({ name: 'UP', value: 'http://{{ site.app.host }}:{{ site.app.port.' + slug + '.http }}', secretRef: null });
  sc.containers = [main, other];
  const route = structuredClone(HELLO().config.sites.find((s) => s.siteSlug === 'app')!.config.edge.routes[0]!) as EdgeRoute;
  route.routeId = 'main';
  (route.action as Doc).containerSlug = slug;
  (route.action as Doc).portName = 'http';
  sc.edge.routes = [route];
  return { variables: [], secrets: [], files: [], sites: [{ siteSlug: 'app', label: 'App', config: sc }] };
}

async function setup() {
  const admin = await person('admin');
  const slug = uniq('ie');
  await seedImage(slug, 'v1');
  await seedImage(slug, 'v2');
  const cfg = config(slug);
  const t = await admin.api.createTemplate({ body: { label: uniq('tpl') } });
  const tw = await admin.api.patchTemplate({ templateId: t.templateId, body: { ifVersion: 1, ...cfg } });
  assert.equal(tw.validation.ok, true, JSON.stringify(tw.validation.issues));
  const d = await admin.api.createDeployment({ body: { label: uniq('dep'), templateId: t.templateId, tagSlugs: [], managerAccountUids: [] } });
  const id = d.deployment.deploymentId;
  const p = await publish(admin.api, id, { ifRevision: 0, summary: 'r1' } );
  return { admin, slug, templateId: t.templateId, templateVersion: tw.template.version, id, revId: p.revision.revId };
}
const container = (cfg: Pick<Config, 'sites'>, cs: string) => cfg.sites[0]!.config.containers.find((c) => c.containerSlug === cs)!;

test('edit every editable field of an unused image (same validation as registration)', async () => {
  const admin: Person = await person('admin');
  const slug = uniq('ie');
  await seedImage(slug, 'v1');
  const next = await admin.api.patchImage({
    serviceSlug: slug,
    version: 'v1',
    body: {
      kind: 'job',
      entrypoint: ['/bin/sh', '-c'],
      command: ['run.sh'],
      workdir: '/srv',
      user: '1000:1000',
      ports: [
        { name: 'web', port: 8081, protocol: 'tcp', description: 'Web' },
        { name: 'dns', port: 53, protocol: 'udp', description: '' },
      ],
      env: [
        { name: 'APP_ENV', value: 'dev', options: ['dev', 'prod'], required: true, secret: false, description: 'mode' },
        { name: 'TOKEN', required: true, secret: true, description: '', options: null },
      ],
      volumes: [{ name: 'store', mountPath: '/var/{{ container.env.APP_ENV }}', description: 'data' }],
      healthcheck: { type: 'tcp', portName: 'web', intervalSec: 5, timeoutSec: 2, retries: 2, startPeriodSec: 0 },
      stopTimeoutSec: 45,
      description: 'edited',
      renames: [{ kind: 'port', from: 'http', to: 'web' }],
    },
  });
  assert.equal(next.kind, 'job');
  assert.deepEqual(next.entrypoint, ['/bin/sh', '-c']);
  assert.deepEqual(next.command, ['run.sh']);
  assert.equal(next.workdir, '/srv');
  assert.equal(next.user, '1000:1000');
  assert.deepEqual(next.ports.map((p) => `${p.name}:${p.port}/${p.protocol}`), ['web:8081/tcp', 'dns:53/udp']);
  assert.deepEqual(next.env.map((e) => [e.name, e.value, e.options, e.required, e.secret]), [['APP_ENV', 'dev', ['dev', 'prod'], true, false], ['TOKEN', undefined, null, true, true]]);
  assert.deepEqual(next.volumes, [{ name: 'store', mountPath: '/var/{{ container.env.APP_ENV }}', kind: 'dir', description: 'data' }], 'kind inferred + stored (§21 #83)');
  assert.equal(next.healthcheck?.type, 'tcp');
  assert.equal(next.stopTimeoutSec, 45);
  assert.equal(next.description, 'edited');
  assert.equal(next.digest, (await adb.doc(`services/${slug}/images/v1`).get()).data()!.digest, 'identity kept');
  assert.equal(next.import.status, 'ready');
  // workdir / user cleared by null or ""
  const cleared = await admin.api.patchImage({ serviceSlug: slug, version: 'v1', body: { workdir: null, user: '' } });
  assert.equal(cleared.workdir, null);
  assert.equal(cleared.user, null);
  // validation = registration rules
  const bad = await rejects(
    admin.api.patchImage({ serviceSlug: slug, version: 'v1', body: { kind: 'daemon' as never, workdir: 'rel', ports: [{ name: 'a', port: 70000, protocol: 'tcp', description: '' }], env: [{ name: 'bad name', required: false, secret: false, description: '' }], volumes: [{ name: 'v', mountPath: 'rel', description: '' }], healthcheck: { type: 'tcp', portName: 'nope', intervalSec: 5, timeoutSec: 2, retries: 2, startPeriodSec: 0 } } }),
    'invalid',
  );
  const paths = (bad.details.errors as Doc[]).map((e) => e.path);
  for (const p of ['kind', 'workdir', 'ports[a].port', 'env[bad name].name', 'volumes[v].mountPath', 'healthcheck.portName']) assert.ok(paths.includes(p), `${p} in ${paths.join(', ')}`);
  // bad renames
  const br = await rejects(admin.api.patchImage({ serviceSlug: slug, version: 'v1', body: { renames: [{ kind: 'port', from: 'nope', to: 'web' }] } }), 'invalid');
  assert.match(JSON.stringify(br.details), /V_RENAME/);
  // legacy partial form still works; mixing → V_TYPE
  const leg = await admin.api.patchImage({ serviceSlug: slug, version: 'v1', body: { ports: [{ name: 'web', description: 'legacy' }], env: [{ name: 'APP_ENV', value: 'prod' }] } });
  assert.equal(leg.ports[0]!.description, 'legacy');
  assert.equal(leg.ports.length, 2);
  assert.equal(leg.env[0]!.value, 'prod');
  await rejects(admin.api.patchImage({ serviceSlug: slug, version: 'v1', body: { ports: [{ name: 'web', description: 'x' }, { name: 'dns', port: 53, protocol: 'udp', description: '' }] as never } }), 'invalid');
  // audit entry carries the change summary
  const a = await admin.api.listAuditLog({ query: { target: `services/${slug}/images/v1`, action: 'image.updated' } });
  const first = a.items.at(-1)!;
  assert.deepEqual(first.data.renamed, [{ kind: 'port', from: 'http', to: 'web' }]);
  assert.ok(first.changes.some((ch) => ch.path.startsWith('kind')), JSON.stringify(first.changes.map((x) => x.path)));
});

test('identity fields are rejected; reference only while the import failed', async () => {
  const admin = await person('admin');
  const slug = uniq('ie');
  await seedImage(slug, 'v1');
  for (const [k, v] of Object.entries({ version: 'v9', serviceSlug: 'x', source: {}, digest: 'sha256:00', platform: 'linux/arm64', import: {}, mode: 'gateway', layers: [], createdAt: 'x', createdBy: 'x' })) {
    const e = await rejects(admin.api.patchImage({ serviceSlug: slug, version: 'v1', body: { [k]: v } as never }), 'invalid');
    assert.match(JSON.stringify(e.details), /V_IMMUTABLE/, k);
  }
  await rejects(admin.api.patchImage({ serviceSlug: slug, version: 'v1', body: { reference: 'docker.io/library/busybox:1.37' } }), 'conflict', 409);
});

test('in-use rename → 409 in-use with the list; rename everywhere updates TEMPLATES only (§21 #94: drafts detached), never the head; Template sync takes the rename', async () => {
  const { admin, slug, templateId, templateVersion, id, revId } = await setup();
  const dep0 = await admin.api.getDeployment({ deploymentId: id });
  const body = {
    ports: [
      { name: 'web', port: 8080, protocol: 'tcp' as const, description: 'HTTP' },
      { name: 'metrics', port: 9100, protocol: 'tcp' as const, description: '' },
    ],
    env: [
      { name: 'APP_ENV', value: 'prod', options: ['prod', 'dev'], required: false, secret: false, description: '' },
      { name: 'LOG', value: 'info', options: null, required: false, secret: false, description: '' },
    ],
    volumes: [
      { name: 'store', mountPath: '/data', description: '' },
      { name: 'cache', mountPath: '/cache', kind: 'dir' as const, description: '' },
    ],
    healthcheck: { type: 'http' as const, portName: 'web', path: '/healthz', expectStatus: 200, intervalSec: 10, timeoutSec: 5, retries: 3, startPeriodSec: 5 },
    renames: [
      { kind: 'port' as const, from: 'http', to: 'web' },
      { kind: 'env' as const, from: 'NODE_ENV', to: 'APP_ENV' },
      { kind: 'volume' as const, from: 'data', to: 'store' },
    ],
  };
  const e = await rejects(admin.api.patchImage({ serviceSlug: slug, version: 'v1', body }), 'conflict', 409);
  const det = e.details as unknown as ImageInUseDetails;
  assert.equal(det.reason, 'in-use');
  assert.deepEqual(det.items.map((x) => `${x.kind}:${x.name}>${x.to}:${x.change}`), ['port:http>web:renamed', 'env:NODE_ENV>APP_ENV:renamed', 'volume:data>store:renamed']);
  const port = det.items[0]!.usedBy;
  assert.ok(port.some((u) => u.kind === 'template' && u.templateId === templateId && u.containerSlug === slug && u.field === 'ports[http]'));
  assert.ok(port.some((u) => u.kind === 'deployment' && u.deploymentId === id && u.source === 'draft' && u.field === 'edge.routes[main].portName'));
  assert.ok(port.some((u) => u.containerSlug === `${slug}-b` && u.field.startsWith('env[UP].value')), 'expression in another container');
  assert.ok(det.items[1]!.usedBy.some((u) => u.field === 'env[CFG].value'));
  assert.ok(!det.items.some((x) => x.usedBy.some((u) => u.containerSlug === `${slug}-b` && !u.field.startsWith('env[UP]'))), 'the v2 container is not a use of v1');
  assert.equal((await admin.api.getImage({ serviceSlug: slug, version: 'v1' })).ports[0]!.name, 'http', 'nothing saved');

  // rename everywhere
  const img = await admin.api.patchImage({ serviceSlug: slug, version: 'v1', body: { ...body, renameEverywhere: true } });
  assert.deepEqual(img.ports.map((p) => p.name), ['web', 'metrics']);
  const t = await admin.api.getTemplate({ templateId });
  assert.equal(t.version, templateVersion + 1);
  const tc = container(t, slug);
  assert.equal(tc.ports[0]!.portName, 'web');
  assert.equal(tc.env[0]!.name, 'APP_ENV');
  assert.equal(tc.env[1]!.value, '/cfg/{{ container.env.APP_ENV }}.json');
  assert.equal(tc.volumes[0]!.imageVolume, 'store');
  assert.equal((t.sites[0]!.config.edge.routes[0]!.action as Doc).portName, 'web');
  assert.equal(container(t, `${slug}-b`).env.find((x) => x.name === 'UP')!.value, `http://{{ site.app.host }}:{{ site.app.port.${slug}.web }}`);
  assert.equal(container(t, `${slug}-b`).ports[0]!.portName, 'http', 'other version untouched');
  // §21 #94: the deployment draft is detached — NOT rewritten (old names kept, no draft.updated activity)
  const dep = await admin.api.getDeployment({ deploymentId: id });
  assert.equal(dep.draftVersion, dep0.draftVersion, 'draft untouched');
  assert.equal(container(dep.draft, slug).ports[0]!.portName, 'http');
  assert.equal((dep.draft.sites[0]!.config.edge.routes[0]!.action as Doc).portName, 'http');
  const rev = await admin.api.getRevision({ deploymentId: id, revId });
  assert.equal(container(rev.config, slug).ports[0]!.portName, 'http', 'head unchanged');
  assert.ok(rev.catalog.images[`${slug}:v1`]!.ports.some((p) => p.name === 'http'), 'head keeps its ImageSpec copy');
  const acts = await admin.api.listActivities({ deploymentId: id, query: { type: 'draft.updated' } });
  assert.ok(!acts.items.some((a) => (a.data as Doc).image === `${slug}:v1`), 'no draft.updated by the rename');
  // audit: templates rewritten + the deployments that were NOT rewritten (need a Template sync or a manual edit)
  const au = await admin.api.listAuditLog({ query: { target: `services/${slug}/images/v1`, action: 'image.updated' } });
  const re = au.items[0]!.data.renamedEverywhere as Doc;
  assert.ok(re.templates.some((x: Doc) => x.templateId === templateId));
  assert.ok(re.deploymentsNotRewritten.some((x: Doc) => x.deploymentId === id && x.references > 0), JSON.stringify(re));
  const ta = await admin.api.listAuditLog({ query: { target: `templates/${templateId}`, action: 'template.committed' } });
  assert.ok(ta.items.some((x) => x.data.reason === 'image-rename-everywhere' && x.requestId === au.items[0]!.requestId));
  // the stale draft is not publishable until it is fixed …
  const diff0 = await admin.api.getDiff({ deploymentId: id });
  assert.equal(diff0.publishable, false, 'old names on the renamed image');
  // §21 #105: the rewrite is ONE COMMIT on the template (no working copy); then compare template → deployment and
  // cherry-pick the rename (replaces Template sync)
  const th = await admin.api.listTemplateCommits({ templateId, query: { limit: 1 } });
  assert.match(th.items[0]!.message, /^Rename everywhere: /);
  assert.equal(th.items[0]!.author.email, admin.email);
  assert.equal(th.items[0]!.commitId, t.branch!.head!.commitId);
  const plan = await admin.api.compareBranches({ body: { source: { branch: { kind: 'template', templateId }, at: 'head' }, target: { kind: 'deployment', deploymentId: id } } });
  assert.equal(plan.base.kind, 'record');
  assert.ok(plan.items.some((x) => x.key === `sites/app/containers/${slug}/ports` && x.kind === 'theirs'), JSON.stringify(plan.items.map((x) => [x.key, x.kind])));
  assert.ok(plan.items.some((x) => x.key === 'sites/app/edge/routes/main' && x.kind === 'theirs'));
  const choices = plan.items.filter((x) => x.kind === 'theirs').map((x) => ({ key: x.key, take: 'source' as const }));
  const head = (await admin.api.getDeployment({ deploymentId: id })).branch!.head!.commitId;
  const m = await admin.api.merge({ body: { source: { branch: { kind: 'template', templateId }, at: 'head' }, target: { kind: 'deployment', deploymentId: id }, targetHead: head, sourceCommit: plan.source.commit!.commitId, choices, message: 'take the port rename' } });
  assert.equal(m.kind, 'merge');
  const dep2 = await admin.api.getDeployment({ deploymentId: id });
  assert.equal(container(dep2.draft, slug).ports[0]!.portName, 'web');
});

test('removal of an in-use name is blocked (also with renameEverywhere); unused removal, additions and port retargets are ok', async () => {
  const { admin, slug } = await setup();
  const cur = await admin.api.getImage({ serviceSlug: slug, version: 'v1' });
  const e = await rejects(admin.api.patchImage({ serviceSlug: slug, version: 'v1', body: { volumes: cur.volumes.filter((v) => v.name !== 'data'), renameEverywhere: true } }), 'conflict', 409);
  const det = e.details as unknown as ImageInUseDetails;
  assert.deepEqual(det.items.map((x) => `${x.kind}:${x.name}:${x.change}`), ['volume:data:removed']);
  assert.ok(det.items[0]!.usedBy.some((u) => u.field === 'volumes[data]'));
  // a removal disguised as a different name (no rename entry) is a removal too
  const e2 = await rejects(admin.api.patchImage({ serviceSlug: slug, version: 'v1', body: { ports: [{ ...cur.ports[0]!, name: 'web' }, cur.ports[1]!], healthcheck: null } }), 'conflict', 409);
  assert.equal((e2.details as unknown as ImageInUseDetails).items[0]!.change, 'removed');
  // cache is mounted as "none" on the container: a row = still a use; LOG is not used → removable
  const env = await admin.api.patchImage({ serviceSlug: slug, version: 'v1', body: { env: [...cur.env.filter((x) => x.name !== 'LOG'), { name: 'NEW_VAR', value: 'x', options: null, required: false, secret: false, description: 'added' }] } });
  assert.deepEqual(env.env.map((x) => x.name), ['NODE_ENV', 'NEW_VAR']);
  // port number / protocol change of an in-use port: allowed (mappings reference the name)
  const pt = await admin.api.patchImage({ serviceSlug: slug, version: 'v1', body: { ports: [{ ...cur.ports[0]!, port: 8081 }, cur.ports[1]!] } });
  assert.equal(pt.ports[0]!.port, 8081);
  const au = await admin.api.listAuditLog({ query: { target: `services/${slug}/images/v1`, action: 'image.updated' } });
  assert.ok(au.items.some((x) => Array.isArray(x.data.portRetargeted)));
});
