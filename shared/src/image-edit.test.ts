import { test } from 'node:test';
import assert from 'node:assert/strict';
import { imageNameChanges, imageNameUses, renameImageNames, renameImageSelfRefs } from './image-edit.js';
import { defaultSiteConfig, validateImageSpec, validateRegisterImage, Issues } from './validate.js';
import type { Config, Container, EdgeRoute, ImagePort } from './types.js';

const port = (name: string, p = 8080, protocol: 'tcp' | 'udp' = 'tcp'): ImagePort => ({ name, port: p, protocol, description: '' });
const before = { ports: [port('http'), port('metrics', 9100)], env: [{ name: 'NODE_ENV' }, { name: 'PORT' }], volumes: [{ name: 'data' }, { name: 'cache' }] };

test('imageNameChanges: rename, remove, add, retarget', () => {
  const after = { ports: [port('web', 8081), port('metrics', 9100, 'udp')], env: [{ name: 'APP_ENV' }, { name: 'PORT' }, { name: 'LOG' }], volumes: [{ name: 'data' }] };
  const ch = imageNameChanges(before, after, [
    { kind: 'port', from: 'http', to: 'web' },
    { kind: 'env', from: 'NODE_ENV', to: 'APP_ENV' },
  ]);
  assert.deepEqual(ch.issues, []);
  assert.deepEqual(ch.renamed.map((r) => `${r.kind}:${r.from}>${r.to}`), ['port:http>web', 'env:NODE_ENV>APP_ENV']);
  assert.deepEqual(ch.removed, [{ kind: 'volume', name: 'cache' }]);
  assert.deepEqual(ch.added, [{ kind: 'env', name: 'LOG' }]);
  assert.deepEqual(ch.portRetargeted, [
    { name: 'web', from: '8080/tcp', to: '8081/tcp' },
    { name: 'metrics', from: '9100/tcp', to: '9100/udp' },
  ]);
});

test('imageNameChanges: without a rename entry a changed name is remove + add; swap works; bad renames → V_RENAME', () => {
  const ch = imageNameChanges(before, { ...before, ports: [port('web'), port('metrics', 9100)] });
  assert.deepEqual(ch.removed, [{ kind: 'port', name: 'http' }]);
  assert.deepEqual(ch.added, [{ kind: 'port', name: 'web' }]);
  const sw = imageNameChanges(before, { ...before, ports: [port('metrics'), port('http', 9100)] }, [
    { kind: 'port', from: 'http', to: 'metrics' },
    { kind: 'port', from: 'metrics', to: 'http' },
  ]);
  assert.deepEqual(sw.issues, []);
  assert.deepEqual(sw.removed, []);
  const bad = imageNameChanges(before, before, [
    { kind: 'port', from: 'nope', to: 'http' },
    { kind: 'env', from: 'PORT', to: 'PORT' },
    { kind: 'volume', from: 'data', to: 'missing' },
    { kind: 'x' as never, from: 'a', to: 'b' },
  ]);
  assert.equal(bad.issues.length, 4);
  assert.ok(bad.issues.every((i) => i.code === 'V_RENAME'));
  // kept AND renamed from the same origin
  const both = imageNameChanges(before, { ...before, ports: [port('http'), port('web'), port('metrics', 9100)] }, [{ kind: 'port', from: 'http', to: 'web' }]);
  assert.ok(both.issues.some((i) => /both kept and renamed/.test(i.message)));
});

function cfg(): Config {
  const c = (slug: string, version = 'v1'): Container => ({
    containerSlug: slug,
    serviceSlug: 'api',
    version,
    enabled: true,
    env: [
      { name: 'NODE_ENV', value: 'prod', secretRef: null },
      { name: 'CFG', value: '/cfg/{{ container.env.NODE_ENV }}.json and container.env.NODE_ENV', secretRef: null },
    ],
    ports: [{ portName: 'http', hostPort: 8080, hostIp: '0.0.0.0', edge: true }],
    volumes: [
      { imageVolume: 'data', mountPath: null, source: { kind: 'volume', volumeSlug: 'api-data' }, readOnly: false },
      { imageVolume: null, mountPath: '/etc/{{ container.env.NODE_ENV }}', source: { kind: 'host', path: '/srv' }, readOnly: true },
    ],
    command: null,
    entrypoint: null,
    restart: 'always',
    resources: { cpus: null, memoryMb: null },
    healthcheck: { type: 'http', portName: 'http', path: '/', expectStatus: 200, intervalSec: 10, timeoutSec: 5, retries: 3, startPeriodSec: 30 },
    depends: [],
  });
  const route = { routeId: 'api', hosts: [], match: { kind: 'prefix', path: '/', stopRegex: false, methods: null }, action: { type: 'proxy', containerSlug: 'api', portName: 'http' }, order: 1, responseHeaders: null } as unknown as EdgeRoute;
  const app = defaultSiteConfig();
  app.containers = [c('api'), c('api-old', 'v0')];
  app.edge.routes = [route];
  const other = defaultSiteConfig();
  const web = c('web');
  web.serviceSlug = 'web';
  web.env = [{ name: 'API', value: 'http://{{ site.app.host }}:{{ site.app.port.api.http }}', secretRef: null }];
  other.containers = [web];
  return { variables: [], secrets: [], files: [], sites: [{ siteSlug: 'app', label: 'App', config: app }, { siteSlug: 'db', label: 'Db', config: other }] };
}

test('imageNameUses: every place, only containers of serviceSlug:version', () => {
  const u = imageNameUses(cfg(), 'api', 'v1', 'port', 'http');
  assert.deepEqual(
    u.map((x) => `${x.siteSlug}/${x.containerSlug}/${x.field}`),
    ['app/api/ports[http]', 'app/api/healthcheck.portName', 'app/api/edge.routes[api].portName', 'db/web/env[API].value (site.app.port.api.http)'],
  );
  assert.deepEqual(
    imageNameUses(cfg(), 'api', 'v1', 'env', 'NODE_ENV').map((x) => x.field),
    ['env[NODE_ENV]', 'env[CFG].value', 'volumes[1].mountPath'],
  );
  assert.deepEqual(imageNameUses(cfg(), 'api', 'v1', 'volume', 'data').map((x) => x.field), ['volumes[data]']);
  assert.deepEqual(imageNameUses(cfg(), 'api', 'v1', 'volume', 'cache'), []);
  assert.deepEqual(imageNameUses(cfg(), 'api', 'v9', 'port', 'http'), []);
});

test('renameImageNames: rewrites every reference (blocks only), other versions untouched, pure', () => {
  const src = cfg();
  const r = renameImageNames(src, 'api', 'v1', [
    { kind: 'port', from: 'http', to: 'web' },
    { kind: 'env', from: 'NODE_ENV', to: 'APP_ENV' },
    { kind: 'volume', from: 'data', to: 'store' },
  ]);
  assert.deepEqual(r.issues, []);
  const api = r.config.sites[0]!.config.containers[0]!;
  assert.equal(api.ports[0]!.portName, 'web');
  assert.equal((api.healthcheck as { portName: string }).portName, 'web');
  assert.equal((r.config.sites[0]!.config.edge.routes[0]!.action as { portName: string }).portName, 'web');
  assert.equal(api.env[0]!.name, 'APP_ENV');
  assert.equal(api.env[1]!.value, '/cfg/{{ container.env.APP_ENV }}.json and container.env.NODE_ENV', 'text outside {{ }} is literal');
  assert.equal(api.volumes[0]!.imageVolume, 'store');
  assert.equal(api.volumes[1]!.mountPath, '/etc/{{ container.env.APP_ENV }}');
  assert.equal(r.config.sites[1]!.config.containers[0]!.env[0]!.value, 'http://{{ site.app.host }}:{{ site.app.port.api.web }}');
  const old = r.config.sites[0]!.config.containers[1]!;
  assert.equal(old.ports[0]!.portName, 'http', 'v0 container untouched');
  assert.equal(r.count, 8);
  assert.equal(src.sites[0]!.config.containers[0]!.ports[0]!.portName, 'http', 'input not mutated');
  // after the rename nothing references the old names
  for (const [k, n] of [['port', 'http'], ['env', 'NODE_ENV'], ['volume', 'data']] as const) assert.deepEqual(imageNameUses(r.config, 'api', 'v1', k, n), []);
});

test('renameImageNames: swap + env collision', () => {
  const sw = renameImageNames(cfg(), 'api', 'v1', [
    { kind: 'env', from: 'NODE_ENV', to: 'CFG' },
    { kind: 'env', from: 'CFG', to: 'NODE_ENV' },
  ]);
  assert.deepEqual(sw.issues, []);
  assert.deepEqual(sw.config.sites[0]!.config.containers[0]!.env.map((e) => e.name), ['CFG', 'NODE_ENV']);
  const col = renameImageNames(cfg(), 'api', 'v1', [{ kind: 'env', from: 'NODE_ENV', to: 'CFG' }]);
  assert.equal(col.issues[0]!.code, 'V_DUPLICATE');
});

test('renameImageSelfRefs + validateImageSpec = register rules', () => {
  const img = renameImageSelfRefs({ env: [{ name: 'B', value: '{{ container.env.A }}-x', required: false, secret: false, description: '', options: null }], volumes: [{ name: 'v', mountPath: '/x/{{ container.env.A }}', kind: 'dir', description: '' }] }, [{ kind: 'env', from: 'A', to: 'AA' }]);
  assert.equal(img.env[0]!.value, '{{ container.env.AA }}-x');
  assert.equal(img.volumes[0]!.mountPath, '/x/{{ container.env.AA }}');
  const is = new Issues('publish');
  validateImageSpec({ kind: 'daemon', workdir: 'rel', user: 'a b', ports: [port('http'), port('http')], env: [{ name: 'bad', required: false, secret: false, description: '' }], volumes: [{ name: 'v', mountPath: 'rel', description: '' }], healthcheck: { type: 'tcp', portName: 'nope', intervalSec: 10, timeoutSec: 5, retries: 3, startPeriodSec: 0 }, stopTimeoutSec: 0, description: '' }, is);
  const paths = is.errors.map((e) => e.path).sort();
  for (const p of ['kind', 'workdir', 'user', 'ports[http].name', 'env[bad].name', 'volumes[v].mountPath', 'healthcheck.portName', 'stopTimeoutSec']) assert.ok(paths.includes(p), `${p} in ${paths.join(',')}`);
  // registration still reports kind once
  const reg = validateRegisterImage({ version: 'v1', reference: 'redis', mode: 'copy', kind: 'x' });
  assert.equal(reg.errors.filter((e) => e.path === 'kind').length, 1);
});
