/** Round C (SPEC-final §21 #51–56): container instances, auto host, files with render, blob streaming, detected addresses, migrate-c. */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createCloudClient, duplicateContainer, newSiteVolume, undeclaredVolumes, type Config, type Container, type Site, type Variable } from '@opm/shared';
import { abucket, adb, BASE, HELLO, helloDeployment, newSiteKey, person, raw, rejects, seedHelloCatalog, sha256Hex, uniq, agentClient, publish } from './helpers.ts';
import { noRedirectFor, serveMode } from '../src/core/storage.ts';
import { jcsHash } from '../src/core/util.ts';
import { applyPlanC, planMigrationC } from '../scripts/migrate-c.ts';
import { verifySchema } from '../scripts/verify-schema.ts';

type Doc = Record<string, any>;
const appSite = (cfg: Config) => structuredClone(cfg.sites.find((s) => s.siteSlug === 'app')!) as Site;

test('instances: two containers of the same image on one site → save, publish, catalog, usage, delete conflict', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const site = appSite(HELLO().config);
  const api = site.config.containers.find((c) => c.containerSlug === 'hello-api')!;
  const api2 = duplicateContainer(api, 'hello-api-2');
  api2.env = [...api2.env.filter((e) => e.name !== 'GREETING'), { name: 'GREETING', value: 'second instance', secretRef: null }];
  site.config.containers.push(api2);
  // round D: the duplicate's `<new>-data` volume must be declared on the site
  for (const v of undeclaredVolumes(site.config)) site.config.volumes.push(newSiteVolume(v));
  // legacy (pre-E) route shape on purpose: { pathPrefix } without responseHeaders is normalized on write
  site.config.edge.routes.push({ routeId: 'api2', hosts: [], match: { pathPrefix: '/api2', methods: null }, action: { ...(site.config.edge.routes[0]!.action as Doc), containerSlug: 'hello-api-2' }, order: site.config.edge.routes.length + 1 } as never);
  const w = await admin.api.putDraftSite({ deploymentId: id, siteSlug: 'app', body: { ifRevision: 0, site } });
  assert.equal(w.validation.ok, true);
  const d = await admin.api.getDeployment({ deploymentId: id });
  const saved = d.draft.sites.find((s) => s.siteSlug === 'app')!.config;
  assert.deepEqual(saved.containers.map((c) => [c.containerSlug, c.serviceSlug, c.version]), [['hello-api', 'hello-api', 'v1'], ['hello-web', 'hello-web', 'v1'], ['hello-api-2', 'hello-api', 'v1']]);
  assert.deepEqual(saved.containers[2]!.volumes[0]!.source, { kind: 'volume', volumeSlug: 'hello-api-2-data' });
  assert.ok(saved.volumes.some((v) => v.volumeSlug === 'hello-api-2-data'));
  // a duplicate containerSlug is refused
  const dup = structuredClone(site);
  dup.config.containers.push({ ...api2 });
  await rejects(admin.api.putDraftSite({ deploymentId: id, siteSlug: 'app', body: { ifRevision: 1, site: dup } }), 'invalid');
  const p = await publish(admin.api, id, { ifRevision: 1, summary: 'two api instances' } );
  const rev = await admin.api.getRevision({ deploymentId: id, revId: p.revision.revId });
  assert.deepEqual(Object.keys(rev.catalog.images).sort(), ['hello-api:v1', 'hello-web:v1', 'redis:v1'], 'one catalog entry per image');
  assert.equal(rev.config.sites.find((s) => s.siteSlug === 'app')!.config.containers.length, 3);
  // usage: one row per container instance
  const su = await admin.api.serviceUsage({ serviceSlug: 'hello-api' });
  const mine = su.items.filter((u) => u.id === id);
  assert.deepEqual([...new Set(mine.map((u) => u.containerSlug))].sort(), ['hello-api', 'hello-api-2']);
  const iu = await admin.api.imageUsage({ serviceSlug: 'hello-api', version: 'v1' });
  assert.ok(iu.items.some((u) => u.id === id && u.containerSlug === 'hello-api-2'));
  const e = await rejects(admin.api.deleteImage({ serviceSlug: 'hello-api', version: 'v1' }), 'conflict');
  assert.ok((e.details.usedBy as Doc[]).some((u) => u.containerSlug === 'hello-api-2'));
});

test('auto host: host null in a template + deployment publishes; {{ var.x }} with no value → V_HOST_AUTO warning, never an error', async () => {
  const admin = await person('admin');
  await seedHelloCatalog();
  const h = HELLO();
  const banner = await admin.api.uploadBlob({ body: new TextEncoder().encode(h.banner) });
  assert.equal(banner.sha256, h.config.files[0]!.content!.sha256);
  const sites = h.config.sites.map((s) => ({ ...s, config: { ...s.config, host: null } }));
  const t = await admin.api.createTemplate({ body: { label: uniq('auto') } });
  const tw = await admin.api.patchTemplate({ templateId: t.templateId, body: { ifVersion: 1, variables: h.config.variables, secrets: h.config.secrets.map((s) => ({ ...s, value: null, rotatedAt: null })), files: h.config.files, sites } });
  assert.equal(tw.validation.ok, true);
  assert.deepEqual(tw.template.sites.map((s) => s.config.host), [null, null]);
  const d = await admin.api.createDeployment({ body: { label: uniq('auto'), templateId: t.templateId, tagSlugs: [], managerAccountUids: [] } });
  const id = d.deployment.deploymentId;
  const diff = await admin.api.getDiff({ deploymentId: id });
  // §21 #105: the diff is what the head (create) commit changed; it is publishable
  assert.deepEqual(diff.blockers.map((b) => b.code), [], JSON.stringify(diff.blockers));
  await publish(admin.api, id, { ifRevision: 0, summary: 'auto hosts' } );
  // host "" is stored as null
  const site = appSite(h.config);
  site.config.host = '';
  await admin.api.putDraftSite({ deploymentId: id, siteSlug: 'app', body: { ifRevision: 0, site } });
  assert.equal((await admin.api.getDeployment({ deploymentId: id })).draft.sites.find((s) => s.siteSlug === 'app')!.config.host, null);
  // the user's case: host {{ var.server-ip }} with no value
  const vars: Variable[] = [...h.config.variables, { variableSlug: 'server-ip', label: 'Server IP', description: '', type: 'string', value: null, required: false, options: null, validation: null }];
  await admin.api.putDraftSection({ deploymentId: id, section: 'variables', body: { ifRevision: 1, items: vars } });
  site.config.host = '{{ var.server-ip }}';
  const w = await admin.api.putDraftSite({ deploymentId: id, siteSlug: 'app', body: { ifRevision: 2, site } });
  assert.ok(w.validation.issues.some((i) => i.code === 'V_HOST_AUTO' && i.severity === 'warning'), JSON.stringify(w.validation.issues));
  const diff2 = await admin.api.getDiff({ deploymentId: id });
  assert.ok(diff2.warnings.some((i) => i.code === 'V_HOST_AUTO'));
  assert.ok(!diff2.blockers.some((i) => /host/i.test(i.path)), JSON.stringify(diff2.blockers));
  assert.equal(diff2.publishable, true, JSON.stringify(diff2.blockers));
  await publish(admin.api, id, { ifRevision: 3, summary: 'host expression without value' } );
});

test('legacy shapes on write are normalized (template PATCH + draft PUT): containerSlug, ports, explicit image volumes, depends, proxy, host', async () => {
  const admin = await person('admin');
  const { dep, templateId } = await helloDeployment(admin);
  const legacy = legacySite(appSite(HELLO().config));
  const w = await admin.api.putDraftSite({ deploymentId: dep.deploymentId, siteSlug: 'app', body: { ifRevision: 0, site: legacy as unknown as Site } });
  assert.equal(w.validation.ok, true);
  const sc = (await admin.api.getDeployment({ deploymentId: dep.deploymentId })).draft.sites.find((s) => s.siteSlug === 'app')!.config;
  assertMigratedApp(sc);
  const raw0 = JSON.parse((await adb.collection('deployments').doc(dep.deploymentId).get()).data()!._draft);
  assert.ok(!JSON.stringify(raw0).includes('"port":'), 'stored in the new shape');
  // template PATCH
  const tpl = await admin.api.getTemplate({ templateId });
  const sites = tpl.sites.map((s) => (s.siteSlug === 'app' ? (legacy as unknown as Site) : s));
  const tw = await admin.api.patchTemplate({ templateId, body: { ifVersion: tpl.version, sites } });
  assertMigratedApp(tw.template.sites.find((s) => s.siteSlug === 'app')!.config);
  // legacy documents written directly to Firestore are normalized on read
  const body = JSON.parse((await adb.collection('templates').doc(templateId).get()).data()!._body);
  body.sites = body.sites.map((s: Doc) => (s.siteSlug === 'app' ? legacy : s));
  await adb.collection('templates').doc(templateId).update({ _body: JSON.stringify(body) });
  const read = await admin.api.getTemplate({ templateId });
  const rc = read.sites.find((s) => s.siteSlug === 'app')!.config;
  assert.deepEqual(rc.containers.map((c) => c.containerSlug), ['hello-api', 'hello-web']);
  assert.equal(rc.host, null);
  assert.equal((rc.edge.routes[0]!.action as Doc).containerSlug, 'hello-api');
});

test('files: render defaults (text → expressions, binary → none); binary + expressions → 422 invalid', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const h = HELLO();
  const { render, ...noRender } = h.config.files[0]!;
  void render;
  const bin = { fileSlug: 'logo', label: 'Logo', description: '', mime: 'image/png', editor: 'binary' as const, maxBytes: 4096, content: null };
  const w = await admin.api.putDraftSection({ deploymentId: id, section: 'files', body: { ifRevision: 0, items: [noRender, bin] as never } });
  assert.equal(w.validation.ok, true);
  const files = (await admin.api.getDeployment({ deploymentId: id })).draft.files;
  assert.deepEqual(files.map((f) => [f.fileSlug, f.render]), [['banner', 'expressions'], ['logo', 'none']]);
  const e = await rejects(admin.api.putDraftSection({ deploymentId: id, section: 'files', body: { ifRevision: 1, items: [noRender, { ...bin, render: 'expressions' }] as never } }), 'invalid', 422);
  assert.match(JSON.stringify(e.details), /V_ENUM/);
  await rejects(admin.api.putDraftSection({ deploymentId: id, section: 'files', body: { ifRevision: 1, items: [{ ...noRender, render: 'jinja' }] as never } }), 'invalid', 422);
  const off = await admin.api.putDraftSection({ deploymentId: id, section: 'files', body: { ifRevision: 1, items: [{ ...noRender, render: 'none' }] as never } });
  assert.equal(off.validation.ok, true);
  assert.equal((await admin.api.getDeployment({ deploymentId: id })).draft.files[0]!.render, 'none');
});

test('blobs: human GET ≤ BLOB_STREAM_MAX → 200 body + ETag + private cache (304 on If-None-Match); larger → 302 (serveMode)', async () => {
  const admin = await person('admin');
  const text = `round-c ${uniq()}\n`;
  const b = await admin.api.uploadBlob({ body: new TextEncoder().encode(text) });
  const hex = b.sha256.slice(7);
  const r = await raw('GET', `/v1/blobs/${b.sha256}`, { headers: { Authorization: admin.bearer, Origin: 'http://localhost:5173' } });
  assert.equal(r.status, 200);
  assert.equal(r.body.toString('utf8'), text);
  assert.equal(r.headers.get('content-type'), 'application/octet-stream');
  assert.equal(r.headers.get('etag'), `"${hex}"`);
  assert.equal(r.headers.get('cache-control'), 'private, max-age=300');
  assert.equal(r.headers.get('access-control-allow-origin'), 'http://localhost:5173');
  assert.match(r.headers.get('access-control-expose-headers') ?? '', /ETag/);
  const nm = await raw('GET', `/v1/blobs/${b.sha256}`, { headers: { Authorization: admin.bearer, 'If-None-Match': `"${hex}"` } });
  assert.equal(nm.status, 304);
  assert.equal(nm.body.length, 0);
  // decision used by serveObject (prod: OPM_BLOB_REDIRECT=1; the emulator never redirects)
  const MAX = 8 * 1024 * 1024;
  assert.equal(serveMode(MAX, { redirect: true, streamMax: MAX }), 'stream');
  assert.equal(serveMode(MAX + 1, { redirect: true, streamMax: MAX }), 'redirect');
  assert.equal(serveMode(MAX + 1, { redirect: false, streamMax: MAX }), 'stream');
  assert.equal(serveMode(MAX + 1, { redirect: true, noRedirect: true, streamMax: MAX }), 'stream');
  assert.equal(serveMode(10, { redirect: true }), 'redirect', 'agent blobs (no streamMax) keep redirecting');
  // §21 #114: a tunnelled agent download (sent by the GUI tab: Origin header) or a relayed one streams; the agent itself redirects
  assert.equal(noRedirectFor({ origin: 'http://10.0.0.5:8080' }), true);
  assert.equal(noRedirectFor({}, 'app2'), true);
  assert.equal(noRedirectFor({ range: 'bytes=0-9' }, null), false);
});

test('enroll + heartbeat store detectedAddresses (≤ 32 strings, each ≤ 255); status + setup expose them', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const enrollBody = (key: ReturnType<typeof newSiteKey>, extra: Doc) => ({ publicKey: key.publicKey, agentVersion: '0.3.0-test', hostname: 'host-app', ip: '10.0.0.7', advertiseHost: '10.0.0.7', agentPort: 7080, os: 'linux', arch: 'amd64', ...extra });
  const tok = await admin.api.createInstallToken({ deploymentId: id, body: { siteSlug: 'app' } });
  const key = newSiteKey();
  const anon = createCloudClient(BASE, () => `Bearer ${tok.token}`);
  await rejects(anon.enroll({ body: enrollBody(key, { detectedAddresses: Array.from({ length: 33 }, (_, i) => `10.0.0.${i}`) }) as never }), 'invalid');
  await rejects(anon.enroll({ body: enrollBody(key, { detectedAddresses: ['x'.repeat(256)] }) as never }), 'invalid');
  await anon.enroll({ body: enrollBody(key, { detectedAddresses: ['10.0.0.7', '192.168.1.4', 'host-app'] }) as never });
  const a0 = (await adb.collection('deployments').doc(id).collection('agents').doc('app').get()).data()!;
  assert.deepEqual(a0.detectedAddresses, ['10.0.0.7', '192.168.1.4', 'host-app']);
  const api = agentClient(key, id, 'app');
  const hb = { siteSlug: 'app', agentVersion: '0.3.0-test', headRevId: null, staging: null, peersSeen: [], metrics: { cpu: 0.1, memPct: 0.2, diskPct: 0.3 }, edge: null, keyVersion: 1, advertiseHost: '10.0.0.7', agentPort: 7080, ip: '10.0.0.7', hostname: 'host-app' };
  await api.heartbeat({ body: { ...hb, detectedAddresses: ['10.0.0.8', 'host-app'] } });
  assert.deepEqual((await adb.collection('deployments').doc(id).collection('agents').doc('app').get()).data()!.detectedAddresses, ['10.0.0.8', 'host-app']);
  await api.heartbeat({ body: hb });
  assert.deepEqual((await adb.collection('deployments').doc(id).collection('agents').doc('app').get()).data()!.detectedAddresses, ['10.0.0.8', 'host-app'], 'old agents (no field) keep the stored list');
  await rejects(api.heartbeat({ body: { ...hb, detectedAddresses: 'nope' as never } }), 'invalid');
  const st = await admin.api.getStatus({ deploymentId: id });
  const s = st.sites.find((x) => x.siteSlug === 'app') as Doc;
  assert.deepEqual(s.detectedAddresses, ['10.0.0.8', 'host-app']);
  assert.equal(s.advertiseHost, '10.0.0.7');
  const setup = await admin.api.getSetup({ deploymentId: id });
  assert.deepEqual((setup.sites.find((x) => x.siteSlug === 'app')!.agent as Doc).detectedAddresses, ['10.0.0.8', 'host-app']);
  // the auto host of an enrolled site resolves to its advertiseHost in validation (no placeholder for app)
  const site = appSite(HELLO().config);
  site.config.host = null;
  const w = await admin.api.putDraftSite({ deploymentId: id, siteSlug: 'app', body: { ifRevision: 0, site } });
  assert.ok(!JSON.stringify(w.validation.issues).includes('<auto:app>'), JSON.stringify(w.validation.issues));
});

// ───────────────────────────────────────── migrate-c
function legacyContainer(c: Container): Doc {
  return {
    serviceSlug: c.serviceSlug,
    version: c.version,
    enabled: c.enabled,
    env: c.env,
    port: c.ports.map((p) => ({ portName: p.portName, hostPort: p.hostPort, protocol: 'tcp' })),
    volume: c.volumes.flatMap((v): Doc[] => {
      if (v.imageVolume) return [{ kind: 'data', name: v.imageVolume, mountPath: '/data' }];
      if (v.source.kind === 'file') return [{ kind: 'file', fileSlug: v.source.fileSlug, mountPath: v.mountPath }];
      return [];
    }),
    command: c.command,
    restart: c.restart,
    resources: c.resources,
    depends: c.depends.map((d) => ({ serviceSlug: d.containerSlug })),
  };
}
function legacySite(s: Site): Doc {
  return {
    ...s,
    config: {
      ...(({ volumes, ...rest }) => (void volumes, rest))(s.config),
      host: '',
      containers: s.config.containers.map(legacyContainer),
      edge: { ...s.config.edge, routes: s.config.edge.routes.map((r) => (r.action.type === 'proxy' ? { ...r, action: (({ containerSlug, ...rest }) => ({ ...rest, serviceSlug: containerSlug }))(r.action as Doc) } : r)) },
    },
  };
}
function legacyConfig(cfg: Doc): Doc {
  return { ...cfg, files: (cfg.files as Doc[]).map(({ render, ...f }) => (void render, f)), sites: (cfg.sites as Site[]).map((s) => (s.siteSlug === 'app' ? legacySite(s) : s)) };
}
function assertMigratedApp(sc: Doc) {
  assert.equal(sc.host, null);
  assert.deepEqual(sc.containers.map((c: Doc) => c.containerSlug), ['hello-api', 'hello-web']);
  const a = sc.containers[0];
  assert.ok(!('port' in a) && !('volume' in a));
  assert.deepEqual(a.ports, [{ portName: 'http', hostPort: null, hostIp: '0.0.0.0', edge: true }]);
  assert.deepEqual(a.volumes, [{ imageVolume: 'data', mountPath: null, source: { kind: 'volume', volumeSlug: 'hello-api-data' }, readOnly: false }]);
  assert.deepEqual(sc.volumes, [{ volumeSlug: 'hello-api-data', label: 'hello-api-data', description: '', backup: true, location: { kind: 'auto' }, kind: 'dir', seed: null }], 'round D: legacy volumes declared');
  assert.deepEqual(sc.containers[1].volumes, [{ imageVolume: null, mountPath: '/etc/hello/banner.txt', source: { kind: 'file', fileSlug: 'banner' }, readOnly: true }]);
  assert.deepEqual(sc.containers[1].depends, [{ containerSlug: 'hello-api' }]);
  assert.equal(sc.edge.routes[0].action.containerSlug, 'hello-api');
  assert.ok(!('serviceSlug' in sc.edge.routes[0].action));
}

test('migrate-c: legacy template / draft / snapshot / revision → dry run reports, apply converts, rerun 0, verify-schema 0', async () => {
  const admin = await person('admin');
  const { dep, templateId } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const p = await publish(admin.api, id, { ifRevision: 0, summary: 'r1' } );
  // a text file whose {{ does not parse → render none; a binary file → none
  const badText = 'value: {{ var.theme \n';
  const bad = await admin.api.uploadBlob({ body: new TextEncoder().encode(badText) });
  const extraFiles = [
    { fileSlug: 'broken', label: 'Broken', description: '', mime: 'text/plain', editor: 'text', maxBytes: 4096, content: bad },
    { fileSlug: 'blob', label: 'Blob', description: '', mime: 'application/octet-stream', editor: 'binary', maxBytes: 4096, content: null },
  ];
  const tRef = adb.collection('templates').doc(templateId);
  const t0 = (await tRef.get()).data()!;
  const body = legacyConfig(JSON.parse(t0._body));
  body.files = [...body.files, ...extraFiles];
  await tRef.update({ _body: JSON.stringify(body) });
  const dRef = adb.collection('deployments').doc(id);
  const d0 = (await dRef.get()).data()!;
  await dRef.update({ _draft: JSON.stringify(legacyConfig(JSON.parse(d0._draft))), _srcSnapshot: JSON.stringify(legacyConfig(JSON.parse(d0._srcSnapshot))) });
  const rRef = dRef.collection('revisions').doc(p.revision.revId);
  const r0 = (await rRef.get()).data()!;
  const legacyRev = legacyConfig(JSON.parse(r0._config));
  await rRef.update({ _config: JSON.stringify(legacyRev), hash: jcsHash(legacyRev) });
  const mine = [`templates/${templateId}`, `deployments/${id}`];
  const isMine = (l: string) => mine.some((m) => l.startsWith(m));
  // verify-schema reports the legacy documents
  const v0 = (await verifySchema(adb)).filter(isMine);
  assert.ok(v0.some((l) => /containerSlug missing/.test(l)), v0.join('\n'));
  assert.ok(v0.some((l) => /render missing/.test(l)));
  assert.ok(v0.some((l) => /host ""/.test(l)));
  assert.ok(v0.some((l) => /proxy action must name containerSlug/.test(l)));
  // dry run
  const plan = await planMigrationC(adb, abucket);
  const paths = plan.ops.map((o) => o.path);
  for (const pth of [`templates/${templateId}`, `deployments/${id}`, `deployments/${id}/revisions/${p.revision.revId}`]) assert.ok(paths.includes(pth), pth);
  const tdoc = plan.docs.find((d) => d.path.startsWith(`templates/${templateId}`))!;
  assert.ok(tdoc.lines.some((l) => /file broken: render none \(contains \{\{ with/.test(l)), tdoc.lines.join('\n'));
  assert.ok(tdoc.lines.some((l) => l === 'file blob: render none (binary)'));
  assert.ok(tdoc.lines.some((l) => l === 'file banner: render expressions (valid {{ }} blocks)' || l === 'file banner: render expressions (no {{)'));
  assert.ok(tdoc.lines.some((l) => /host "" → null/.test(l)));
  assert.ok(tdoc.lines.some((l) => /LEGACY hello-api/.test(l)));
  assert.ok(tdoc.lines.some((l) => /proxy serviceSlug hello-api → containerSlug hello-api/.test(l)));
  assert.equal(JSON.parse((await tRef.get()).data()!._body).files.length, 3, 'dry run wrote nothing');
  assert.ok(!('render' in JSON.parse((await tRef.get()).data()!._body).files[0]));
  // apply
  await applyPlanC(adb, plan);
  const t1 = (await tRef.get()).data()!;
  assert.equal(t1.version, t0.version, 'template version unchanged');
  assert.equal(t1.updatedAt, t0.updatedAt);
  const b1 = JSON.parse(t1._body);
  assert.deepEqual(b1.files.map((f: Doc) => [f.fileSlug, f.render]), [['banner', 'expressions'], ['broken', 'none'], ['blob', 'none']]);
  assertMigratedApp(b1.sites.find((s: Doc) => s.siteSlug === 'app').config);
  const d1 = (await dRef.get()).data()!;
  assertMigratedApp(JSON.parse(d1._draft).sites.find((s: Doc) => s.siteSlug === 'app').config);
  assertMigratedApp(JSON.parse(d1._srcSnapshot).sites.find((s: Doc) => s.siteSlug === 'app').config);
  const r1 = (await rRef.get()).data()!;
  const rc = JSON.parse(r1._config);
  assertMigratedApp(rc.sites.find((s: Doc) => s.siteSlug === 'app').config);
  assert.equal(r1.hash, jcsHash(rc));
  assert.notEqual(r1.hash, jcsHash(legacyRev), 'hash recomputed');
  assert.equal(r1.catalogHash, jcsHash(JSON.parse(r1._catalog)));
  const audit = (await adb.collection('auditLog').where('action', '==', 'system.migration').get()).docs.map((x) => JSON.parse(x.data()._data)).filter((x) => x.migration === 'c');
  assert.ok(audit.some((a) => a.docs.some((d: Doc) => d.path.startsWith(`templates/${templateId}`)) && a.counts.templates >= 1));
  // idempotent + verify-schema clean
  const again = await planMigrationC(adb, abucket);
  assert.deepEqual(again.ops.filter((o) => isMine(o.path)).map((o) => o.path), []);
  const v1 = (await verifySchema(adb)).filter(isMine);
  assert.deepEqual(v1, []);
  // the API reads the migrated documents
  const api = await admin.api.getDeployment({ deploymentId: id });
  assert.deepEqual(api.draft.sites.find((s) => s.siteSlug === 'app')!.config.containers.map((c) => c.containerSlug), ['hello-api', 'hello-web']);
  void sha256Hex;
});
