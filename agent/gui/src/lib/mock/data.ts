/**
 * Mock agent data (VITE_MOCK=1 only — never in the production bundle). Based on the hello-world e2e fixture
 * (shared/testdata/hello-config.json + hello-catalog.json): deployment with sites app (this site) and db.
 */
import helloConfig from '@opm/shared/testdata/hello-config.json';
import helloCatalog from '@opm/shared/testdata/hello-catalog.json';
import type { Activity, CertStatus, Config, Container, EdgeRoute, SiteConfig, DebugLine, ImageSpec, Proposal, Revision, RevisionCatalog, SiteStaging, Snapshot, SnapshotSummary, Transfer, UptimeCell } from '@opm/shared/types';

export const DEP = 'DepHelloWorld0000001';
export const SELF = 'app';
export const ACCESS_KEY = 'K7QF2M3XPL4D6WZTR3VB6HJN';
export const MOCK_API = 'https://api.opm.example';

const now = Date.now();
export const iso = (ms: number) => new Date(ms).toISOString();
export const ago = (sec: number) => iso(now - sec * 1000);

/** deterministic fake sha256 hex */
export function fakeHex(s: string): string {
  let h1 = 0x811c9dc5;
  let out = '';
  for (let r = 0; r < 8; r++) {
    for (let i = 0; i < s.length; i++) h1 = Math.imul(h1 ^ s.charCodeAt(i), 16777619) >>> 0;
    h1 = Math.imul(h1 ^ r, 2246822507) >>> 0;
    out += h1.toString(16).padStart(8, '0');
  }
  return out;
}
export const fakeSha = (s: string) => `sha256:${fakeHex(s)}`;

const ULID_CH = '0123456789ABCDEFGHJKMNPQRSTVWXYZ';
let ulidN = 0;
export function mockUlid(ms = Date.now()): string {
  let t = ms;
  let time = '';
  for (let i = 0; i < 10; i++) {
    time = ULID_CH[t % 32] + time;
    t = Math.floor(t / 32);
  }
  ulidN++;
  let rnd = '';
  let x = ulidN * 7919 + Math.floor(Math.random() * 1e6);
  for (let i = 0; i < 16; i++) {
    rnd += ULID_CH[(x + i * 13) % 32];
    x = Math.floor(x / 3) + 17;
  }
  return time + rnd;
}

// ── catalog (ImageSpec built from the hello catalog; keyed by serviceSlug / `<serviceSlug>:<version>`)
type CatImg = (typeof helloCatalog.images)[keyof typeof helloCatalog.images];
const images: Record<string, ImageSpec> = {};
for (const [key, raw] of Object.entries(helloCatalog.images) as [string, CatImg][]) {
  const [serviceSlug, version] = key.split(':');
  const layers = [0, 1, 2].map((i) => ({ digest: fakeSha(`${key}:layer:${i}`), sizeBytes: [3_412_118, 9_871_002, 1_204_551][i], mediaType: 'application/vnd.oci.image.layer.v1.tar+gzip' }));
  images[key] = {
    serviceSlug,
    version,
    digest: fakeSha(`${key}:manifest`),
    configDigest: fakeSha(`${key}:config`),
    platform: 'linux/amd64',
    manifestMediaType: 'application/vnd.oci.image.manifest.v1+json',
    layers,
    sizeBytes: layers.reduce((a, l) => a + l.sizeBytes, 0),
    kind: raw.kind as 'service',
    entrypoint: null,
    command: serviceSlug === 'redis' ? ['redis-server'] : ['/hello'],
    workdir: null,
    user: null,
    ports: raw.ports as ImageSpec['ports'],
    env: JSON.parse(JSON.stringify(raw.env)) as ImageSpec['env'],
    volumes: JSON.parse(JSON.stringify(raw.volumes)) as ImageSpec['volumes'],
    healthcheck: (raw.healthcheck ?? null) as ImageSpec['healthcheck'],
    stopTimeoutSec: 30,
  };
}
// round E: the client nginx.conf site (e2e/fixtures/client-nginx.conf) — containers named like PROD (northwind-backend-<role>, not the ez-<role> upstream names of the nginx.conf)
const EXTRA_IMAGES: Record<string, { label: string; icon: string; port: number; env?: ImageSpec['env']; volumes?: ImageSpec['volumes'] }> = {
  // round T: the image volume "config" (a FILE) — every backend role mounts the SAME deployment file (user screenshot, northwind-backend-terminal)
  'ez-backend:v3': { label: 'Northwind backend', icon: 'lucide:server', port: 8080, volumes: [{ name: 'config', mountPath: '/app/helper/config/env/production.ts', kind: 'file', description: 'Environment config of the backend' }], env: [{ name: 'NODE_ENV', value: 'worker', options: ['integration', 'dc', 'worker', 'pg-listen', 'terminal', 'migrate', 'admin', 'heatmap'], required: false, secret: false, description: 'Process role' }] },
  'northwind-admin:v2': { label: 'Northwind admin', icon: 'lucide:shield', port: 3000 },
  'pg-admin:v8': { label: 'pgAdmin', icon: 'lucide:database', port: 80 },
  'nodered:v4': { label: 'Node-RED', icon: 'lucide:activity', port: 1880 },
  'northwind-web:v3': { label: 'Northwind web', icon: 'lucide:globe', port: 80 },
  // §21 #93: the broker the backends talk to (the user's failed update: admin started before mqtt)
  'mqtt:v2': { label: 'MQTT broker', icon: 'lucide:activity', port: 1883 },
  // §21 #104/#105 the image of the container created by hand on the server (drift): a LOCAL image catalog entry
  'adminer:local-1f2e3d4c5b6a': { label: 'adminer (local image)', icon: 'lucide:database', port: 8080 },
};
for (const [key, x] of Object.entries(EXTRA_IMAGES)) {
  const [serviceSlug, version] = key.split(':');
  const layers = [0, 1].map((i) => ({ digest: fakeSha(`${key}:layer:${i}`), sizeBytes: [21_412_118, 4_871_002][i], mediaType: 'application/vnd.oci.image.layer.v1.tar+gzip' }));
  images[key] = {
    serviceSlug,
    version,
    digest: fakeSha(`${key}:manifest`),
    configDigest: fakeSha(`${key}:config`),
    platform: 'linux/amd64',
    manifestMediaType: 'application/vnd.oci.image.manifest.v1+json',
    layers,
    sizeBytes: layers.reduce((a, l) => a + l.sizeBytes, 0),
    kind: 'service',
    entrypoint: null,
    command: null,
    workdir: null,
    user: null,
    ports: [{ name: 'http', port: x.port, protocol: 'tcp', description: 'HTTP' }],
    env: x.env ?? [],
    volumes: x.volumes ?? [],
    healthcheck: null,
    stopTimeoutSec: 30,
  };
}
// phase U demo data on hello-api:v1 — a choice env (options), a LEGACY list env (value: string[] → normalizeImageEnv),
// and an image volume whose mount path uses container.env.NODE_ENV
const api = images['hello-api:v1'];
api.env.push(
  { name: 'NODE_ENV', value: 'worker', options: ['integration', 'dc', 'worker', 'pg-listen', 'terminal', 'migrate'], required: false, secret: false, description: 'Process role of the backend' },
  { name: 'LOG_LEVEL', value: ['info', 'debug', 'warn'], required: false, secret: false, description: 'Log level (legacy list: first = default)' } as unknown as ImageSpec['env'][number],
);
api.volumes.push({ name: 'env-config', mountPath: '/app/helper/config/env/{{ container.env.NODE_ENV }}.ts', kind: 'dir', description: 'Per-role config file' /* explicit Folder (the mock mounts the folder volume api-env here; §21 #83) */ });
/** 16×16 PNG used as an uploaded icon embedded in the catalog (data: URL) */
export const DATA_ICON = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAK0lEQVR4nGMQifL+TwlmABGq+TFk4eFswP8TNljxqAH0NGDg0wH9DaAEAwAKywjnXeL7uQAAAABJRU5ErkJggg==';
const ICONS: Record<string, string | null> = { 'hello-api': 'lucide:server', 'hello-web': DATA_ICON, redis: null };
export const NFP_ID = helloCatalog.notFoundPageIds[0];
export const catalog: RevisionCatalog = {
  services: {
    ...Object.fromEntries(Object.entries(helloCatalog.services).map(([slug, s]) => [slug, { serviceSlug: slug, label: s.label, icon: slug in ICONS ? ICONS[slug] : s.icon }])),
    ...Object.fromEntries(Object.entries(EXTRA_IMAGES).map(([k, x]) => [k.split(':')[0], { serviceSlug: k.split(':')[0], label: x.label, icon: x.icon }])),
  },
  images,
  notFoundPages: {
    [NFP_ID]: {
      notFoundPageId: NFP_ID,
      label: 'Hello 404',
      htmlCode: '<!doctype html><html><head><meta charset="utf-8"><title>Not here</title></head><body style="font-family:sans-serif;background:#103;color:#fff;text-align:center;padding:12vh"><h1 style="font-size:64px">404</h1><p>Nothing at this address. Try the <a style="color:#7fd" href="/">hello page</a>.</p></body></html>',
    },
  },
};

// ── file blobs (sha → text)
export const fileBlobs: Record<string, string> = {};
fileBlobs[(helloConfig.config.files[0].content as { sha256: string }).sha256] = helloConfig.banner;
/** a JSON template file (FileDef.render 'expressions') — round D part 2: rendered ONCE per site, shared by every container that mounts it */
export const API_CONFIG_TEXT = '{\n  "site": "{{ self.slug }}",\n  "theme": "{{ var.theme }}",\n  "token": "{{ secret.api-token }}"\n}\n';
const API_CONFIG_SHA = fakeSha(API_CONFIG_TEXT);
fileBlobs[API_CONFIG_SHA] = API_CONFIG_TEXT;

/** round C: advertised / detected address of every agent (SiteTile.advertiseHost → auto hosts) */
export const ADVERTISE: Record<string, string> = { app: '10.0.1.10', db: '10.0.1.11', northwind: '10.0.2.20' };
/** round D part 2: agentHome (agent data dir) of every site */
export const AGENT_HOME: Record<string, string> = { app: '/var/lib/opm', db: '/opt/opm-db', northwind: '/srv/opm' };

// ── head config (hello config + an internal-TLS host on app so the certificate panel has data)
// round C: site db has host null (auto); hello-api:v1 runs twice on app (hello-api + hello-api-2 with another
// NODE_ENV, its own data volume, a local-only published port and the rendered JSON file api-config)
export function baseConfig(): Config {
  const c = JSON.parse(JSON.stringify(helloConfig.config)) as Config;
  const app = c.sites.find((s) => s.siteSlug === 'app')!;
  app.config.edge.httpsPort = 8443;
  app.config.edge.hosts.unshift({ hostname: 'hello.acme.internal', tls: { mode: 'internal' }, redirectHttpToHttps: false, responseHeaders: { set: {}, add: {}, remove: [], always: [] } });
  const db = c.sites.find((s) => s.siteSlug === 'db')!;
  db.config.host = null;
  c.files.push({ fileSlug: 'api-config', label: 'API config', description: 'Per-container JSON config (template)', mime: 'application/json', editor: 'text', maxBytes: 4096, content: { sha256: API_CONFIG_SHA, sizeBytes: new TextEncoder().encode(API_CONFIG_TEXT).length }, render: 'expressions', location: { kind: 'path', path: './config/api.json' } });
  const api1 = app.config.containers.find((x) => x.containerSlug === 'hello-api')!;
  const api2 = JSON.parse(JSON.stringify(api1)) as typeof api1;
  api2.containerSlug = 'hello-api-2';
  api2.env = [...api2.env.filter((e) => e.name !== 'NODE_ENV'), { name: 'NODE_ENV', value: 'dc', secretRef: null }];
  api2.ports = [{ portName: 'http', hostPort: 9081, hostIp: '127.0.0.1', edge: false }];
  api2.volumes = [
    { imageVolume: 'data', mountPath: null, source: { kind: 'volume', volumeSlug: 'hello-api-2-data' }, readOnly: false },
    { imageVolume: 'env-config', mountPath: null, source: { kind: 'volume', volumeSlug: 'api-env' }, readOnly: false },
    { imageVolume: null, mountPath: '/app/config.json', source: { kind: 'file', fileSlug: 'api-config' }, readOnly: true },
  ];
  api2.depends = [];
  app.config.containers.splice(app.config.containers.indexOf(api1) + 1, 0, api2);
  // round D: declared site volumes — api-env is shared by hello-api + hello-api-2, hello-api-2-data is not backed up,
  // hello-web has a host-path mount (never backed up)
  api1.volumes.push(
    { imageVolume: 'env-config', mountPath: null, source: { kind: 'volume', volumeSlug: 'api-env' }, readOnly: false },
    // part 2: the same file mounted by both API containers = ONE copy on the site ("shared copy")
    { imageVolume: null, mountPath: '/app/config.json', source: { kind: 'file', fileSlug: 'api-config' }, readOnly: true },
  );
  app.config.volumes.push(
    { volumeSlug: 'api-env', label: 'API env config', description: 'Per-role config files of both API containers', backup: true, location: { kind: 'auto' }, kind: 'dir', seed: { kind: 'files', entries: [{ path: 'worker.ts', fileSlug: 'api-config' }] } },
    { volumeSlug: 'hello-api-2-data', label: 'API 2 scratch data', description: 'Cache only — rebuilt on start', backup: false, location: { kind: 'path', path: './cache/hello-api-2' }, kind: 'dir', seed: null },
  );
  app.config.containers.find((x) => x.containerSlug === 'hello-web')!.volumes.push({ imageVolume: null, mountPath: '/var/log/hello', source: { kind: 'host', path: '/srv/hello/logs' }, readOnly: false });
  // §21 #101 persist to cloud: app hello-api-data (in sync), api-env (waiting to upload), hello-api-2-data (no snapshot
  // yet); db redis-data (error: over the cap) — lib/mock/volsync.ts gives each one its sync state
  const persistOn = (sc: { volumes: Config['sites'][number]['config']['volumes'] }, slug: string, exclude: string[], maxMiB: number, auto = true) => {
    const v = sc.volumes.find((x) => x.volumeSlug === slug);
    if (v) v.persist = { exclude, maxBytes: maxMiB * 1024 * 1024, auto };
  };
  persistOn(app.config, 'hello-api-data', ['logs/', '*.gz'], 50);
  persistOn(app.config, 'api-env', ['archive/'], 10);
  persistOn(app.config, 'hello-api-2-data', [], 5, false);
  persistOn(db.config, 'redis-data', [], 50);
  // round E: the client site behind the nginx.conf of e2e/fixtures/client-nginx.conf (TLS files mode → cert file + key secret)
  c.files.push({ fileSlug: 'client-cert', label: 'Client TLS certificate', description: 'PEM chain of env.client.example.gov.in', mime: 'application/x-pem-file', editor: 'text', maxBytes: 65536, content: { sha256: CLIENT_CERT_SHA, sizeBytes: CLIENT_CERT.length }, render: 'none', location: { kind: 'auto' } });
  c.secrets.push({ secretSlug: 'client-key', label: 'Client TLS key', description: 'PEM private key', required: true, generate: null, value: '-----BEGIN PRIVATE KEY-----\nMOCK\n-----END PRIVATE KEY-----\n', rotatedAt: '2026-10-01T10:00:00.000Z' });
  c.files.push({ fileSlug: 'nodered-flows', label: 'Node-RED starter flow', description: 'flows.json written into nodered-data when it is new / empty', mime: 'application/json', editor: 'text', maxBytes: 262144, content: { sha256: FLOWS_SHA, sizeBytes: FLOWS.length }, render: 'none', location: { kind: 'auto' } });
  c.files.push({ fileSlug: 'ez-backend-env', label: 'Northwind backend env', description: 'production.ts of every backend role', mime: 'text/plain', editor: 'text', maxBytes: 65536, content: { sha256: EZ_ENV_SHA, sizeBytes: EZ_ENV.length }, render: 'none', location: { kind: 'auto' } });
  c.sites.push({ siteSlug: 'northwind', label: 'Northwind client', config: northwindSite() });
  return c;
}

const CLIENT_CERT = '-----BEGIN CERTIFICATE-----\nMIIBmock\n-----END CERTIFICATE-----\n';
const CLIENT_CERT_SHA = fakeSha(CLIENT_CERT);
fileBlobs[CLIENT_CERT_SHA] = CLIENT_CERT;
export const CLIENT_HOST = 'env.client.example.gov.in';
const FLOWS = '[\n  { "id": "f1", "type": "tab", "label": "Device types" },\n  { "id": "n1", "type": "http in", "z": "f1", "url": "/api/v0002/device/types", "method": "get" }\n]\n';
const FLOWS_SHA = fakeSha(FLOWS);
fileBlobs[FLOWS_SHA] = FLOWS;
/** §21 #101: the volume's own starter content (blobs live in the cloud; the mock keeps the texts here) */
export const NR_SETTINGS = "module.exports = {\n  uiPort: 1880,\n  httpNodeRoot: '/x/nodered',\n  mqttHost: '{{ containers.mqtt.host }}',\n};\n";
const NR_SETTINGS_SHA = fakeSha(NR_SETTINGS);
fileBlobs[NR_SETTINGS_SHA] = NR_SETTINGS;
const EZ_ENV = 'export default { db: process.env.PG_URL };\n';
const EZ_ENV_SHA = fakeSha(EZ_ENV);
fileBlobs[EZ_ENV_SHA] = EZ_ENV;

/** a container instance of the client site (one http port, edge on) */
function cont(containerSlug: string, image: string, nodeEnv?: string): Container {
  const [serviceSlug, version] = image.split(':');
  return { containerSlug, serviceSlug, version, enabled: true, env: nodeEnv ? [{ name: 'NODE_ENV', value: nodeEnv, secretRef: null }] : [], ports: [{ portName: 'http', hostPort: null, hostIp: '0.0.0.0', edge: true }], volumes: [], command: null, entrypoint: null, restart: 'always', resources: { cpus: null, memoryMb: null }, healthcheck: null, depends: [] };
}
/**
 * The client site: the edge is the nginx.conf of e2e/fixtures/client-nginx.conf written by hand in the round E shape —
 * WITH the real user mistake on x-ez-terminal-s3: Cache-Control "public, max-age=3600, must-revalidate" was put in the
 * REQUEST headers (sent to the container) instead of the response headers (W_RESPONSE_HEADER_AS_REQUEST).
 */
function northwindSite(): SiteConfig {
  const backends = ['integration', 'dc', 'worker', 'pg-listen', 'terminal', 'migrate', 'admin', 'heatmap'];
  const containers: Container[] = [
    ...backends.map((b) => ({ ...cont(`northwind-backend-${b}`, 'ez-backend:v3', b), volumes: [{ imageVolume: 'config', mountPath: null, source: { kind: 'file' as const, fileSlug: 'ez-backend-env' }, readOnly: true }] })),
    { ...cont('northwind-admin', 'northwind-admin:v2'), volumes: [{ imageVolume: null, mountPath: '/app/runtime.json', source: { kind: 'volume', volumeSlug: 'admin-runtime' }, readOnly: false }] },
    cont('pg-admin', 'pg-admin:v8'),
    { ...cont('nodered', 'nodered:v4'), volumes: [{ imageVolume: null, mountPath: '/data', source: { kind: 'volume', volumeSlug: 'nodered-data' }, readOnly: false }] },
    cont('northwind-web', 'northwind-web:v3'),
    cont('mqtt', 'mqtt:v2'),
  ];
  // §21 #93: northwind-backend-admin mentions `mqtt` as a hostname (SUGGESTED depend, no explicit depends) and references
  // node-red through an expression (IMPLICIT depend: start after + wait ready)
  // §21 #101: two persist-to-cloud folders of the integration backend
  containers
    .find((c) => c.containerSlug === 'northwind-backend-integration')!
    .volumes.push({ imageVolume: null, mountPath: '/app/integrations', source: { kind: 'volume', volumeSlug: 'ez-integrations' }, readOnly: false }, { imageVolume: null, mountPath: '/app/scripts', source: { kind: 'volume', volumeSlug: 'ez-scripts' }, readOnly: false });
  const admin = containers.find((c) => c.containerSlug === 'northwind-backend-admin')!;
  admin.env.push({ name: 'MQTT_URL', value: 'mqtt://mqtt:1883', secretRef: null }, { name: 'NODERED_URL', value: 'http://{{ containers.nodered.host }}:1880', secretRef: null });
  let order = 0;
  const proxy = (routeId: string, path: string, containerSlug: string, more: Partial<Extract<EdgeRoute['action'], { type: 'proxy' }>> = {}, rh: EdgeRoute['responseHeaders'] = null): EdgeRoute => ({
    routeId,
    hosts: [],
    match: { kind: 'prefix', path, stopRegex: false, methods: null },
    action: { type: 'proxy', containerSlug, portName: 'http', stripPrefix: true, rewrite: null, addPrefix: null, setHeaders: {}, removeHeaders: [], websocket: true, connectTimeoutSec: 60, sendTimeoutSec: 60, readTimeoutSec: 60, requestBuffering: true, responseBuffering: false, maxBodyBytes: null, ...more },
    order: ++order,
    responseHeaders: rh,
  });
  const s3Headers = { set: { 'Cache-Control': 'public, max-age=3600, must-revalidate' }, add: {}, remove: ['Pragma', 'Expires'], always: [] };
  const sse = { requestBuffering: false, responseBuffering: false };
  const routes: EdgeRoute[] = [
    proxy('x-ez-apis', '/x/ez-apis/', 'northwind-backend-integration'),
    proxy('x-ez-dc-worker', '/x/ez-dc-worker/', 'northwind-backend-dc'),
    proxy('x-ez-worker', '/x/ez-worker/', 'northwind-backend-worker'),
    proxy('x-ez-pg-listen', '/x/ez-pg-listen/', 'northwind-backend-pg-listen'),
    proxy('x-ez-terminal-real-time', '/x/ez-terminal/real-time/', 'northwind-backend-terminal', { ...sse, rewrite: { regex: '^/x/ez-terminal(/.*)$', replacement: '$1' }, stripPrefix: false }),
    // THE MISTAKE (real): the browser cache header sent to the container instead of the browser
    proxy('x-ez-terminal-s3', '/x/ez-terminal/s3/', 'northwind-backend-terminal', { rewrite: { regex: '^/x/ez-terminal(/.*)$', replacement: '$1' }, stripPrefix: false, setHeaders: { 'Cache-Control': 'public, max-age=3600, must-revalidate' } }),
    proxy('x-ez-terminal', '/x/ez-terminal/', 'northwind-backend-terminal'),
    proxy('x-ez-migrate', '/x/ez-migrate/', 'northwind-backend-migrate'),
    proxy('x-ez-admin-real-time', '/x/ez-admin/real-time/', 'northwind-backend-admin', { ...sse, rewrite: { regex: '^/x/ez-admin(/.*)$', replacement: '$1' }, stripPrefix: false }),
    proxy('x-ez-admin-s3', '/x/ez-admin/s3/', 'northwind-backend-admin', { rewrite: { regex: '^/x/ez-admin(/.*)$', replacement: '$1' }, stripPrefix: false }, s3Headers),
    proxy('x-ez-admin', '/x/ez-admin/', 'northwind-backend-admin'),
    proxy('x-admin', '/x/admin/', 'northwind-admin'),
    proxy('x-ez-heatmap', '/x/ez-heatmap/', 'northwind-backend-heatmap'),
    proxy('x-pg-admin', '/x/pg-admin/', 'pg-admin', { stripPrefix: false, setHeaders: { 'X-Script-Name': '/x/pg-admin' } }),
    proxy('x-nodered', '/x/nodered/', 'nodered', { stripPrefix: false, rewrite: { regex: '^/x/nodered(/.*)$', replacement: '$1' } }),
    proxy('swagger', '/swagger/', 'northwind-backend-integration', { stripPrefix: false }),
    proxy('apis', '/apis/', 'northwind-backend-integration'),
    { routeId: 'apis-docs', hosts: [], match: { kind: 'regex', path: '^/apis/(docs|_docs_/[^/]+)$', stopRegex: false, methods: null }, action: { type: 'redirect', to: '$uri/$is_args$args', status: 301, keepPath: false }, order: 1, responseHeaders: null },
    proxy('root', '/', 'northwind-web', { stripPrefix: false }),
    { routeId: 'api-v0002-device-types', hosts: [], match: { kind: 'prefix', path: '/api/v0002/device/types', stopRegex: false, methods: null }, action: { type: 'rewrite', regex: '^', replacement: '/x/nodered$request_uri' }, order: ++order, responseHeaders: null },
  ];
  return {
    host: null,
    agentPort: 7082,
    agentGuiAliasPort: 8092,
    // round E: a Node-RED folder prefilled with the flow (changed on site later) + a single-file volume
    volumes: [
      // §21 #101: own content (tree / single) + persist to cloud (one sync state per volume in lib/mock/volsync.ts)
      {
        volumeSlug: 'nodered-data',
        label: 'Node-RED data',
        description: 'Flows + settings; starts with the standard flow',
        backup: true,
        location: { kind: 'auto' },
        kind: 'dir',
        seed: { kind: 'tree', entries: [{ path: 'flows.json', sha256: FLOWS_SHA, size: FLOWS.length, mode: '0644', render: 'none' }, { path: 'settings.js', sha256: NR_SETTINGS_SHA, size: new TextEncoder().encode(NR_SETTINGS).length, mode: '0644', render: 'expressions' }], dirs: ['lib'] },
        persist: { exclude: ['node_modules/', '.npm/', '*.log'], maxBytes: 50 * 1024 * 1024, auto: true },
      },
      { volumeSlug: 'admin-runtime', label: 'Admin runtime settings', description: 'One JSON file the admin app edits', backup: true, location: { kind: 'auto' }, kind: 'file', seed: { kind: 'single', sha256: API_CONFIG_SHA, size: new TextEncoder().encode(API_CONFIG_TEXT).length, mode: '0644', render: 'expressions' } },
      { volumeSlug: 'ez-integrations', label: 'Client integrations', description: 'Integration scripts edited in the cloud', backup: true, location: { kind: 'auto' }, kind: 'dir', seed: null, persist: { exclude: ['node_modules/'], maxBytes: 20 * 1024 * 1024, auto: true } },
      { volumeSlug: 'ez-scripts', label: 'Ops scripts', description: 'Small scripts the team edits on site', backup: true, location: { kind: 'auto' }, kind: 'dir', seed: null, persist: { exclude: ['*.tmp'], maxBytes: 10 * 1024 * 1024, auto: false } },
    ],
    containers,
    depends: [],
    notFoundPageId: null,
    edge: {
      httpPort: 80,
      httpsPort: 443,
      maxBodyBytes: 100 * 1024 * 1024,
      hosts: [
        {
          hostname: CLIENT_HOST,
          tls: { mode: 'files', certFileSlug: 'client-cert', keySecretSlug: 'client-key' },
          redirectHttpToHttps: false,
          responseHeaders: { set: {}, add: { 'Cache-Control': 'no-store, no-cache, must-revalidate, proxy-revalidate, max-age=0', Pragma: 'no-cache', Expires: '0' }, remove: [], always: [] },
        },
      ],
      routes,
    },
  };
}

export function makeRevision(seq: number, config: Config, opts: Partial<Revision> & { createdMs: number }): Revision {
  return {
    revId: opts.revId ?? mockUlid(opts.createdMs),
    seq,
    parentRevId: opts.parentRevId ?? null,
    origin: opts.origin ?? 'cloud',
    actor: opts.actor ?? 'uidOps0000000000000001',
    createdAt: iso(opts.createdMs),
    config,
    hash: fakeSha(JSON.stringify(config)),
    catalog,
    catalogHash: fakeSha(JSON.stringify(catalog)),
    summary: opts.summary ?? `release ${seq}`,
    kind: opts.kind ?? 'change',
    rollbackOf: opts.rollbackOf ?? null,
  };
}

export function stagingOf(state: SiteStaging['staging'], progress: number, updatedMs: number, error: string | null = null): SiteStaging {
  return { staging: state, progress, error, updatedAt: iso(updatedMs) };
}

export function makeProposal(rev: Revision, status: Proposal['status'], sites: Record<string, SiteStaging>, extra: Partial<Proposal> = {}): Proposal {
  return {
    revId: rev.revId,
    parentRevId: rev.parentRevId,
    seq: rev.seq,
    summary: rev.summary,
    origin: rev.origin,
    kind: rev.kind,
    createdAt: rev.createdAt,
    createdBy: rev.origin === 'cloud' ? 'ops@acme.example' : rev.actor,
    status,
    sites,
    approvedBy: null,
    approvedAt: null,
    updateId: null,
    result: null,
    supersededBy: null,
    updatedAt: rev.createdAt,
    ...extra,
  };
}

// ── activities
const ACTS: [string, string, string | null, (i: number) => Record<string, unknown>][] = [
  ['agent.started', 'Agent started (0.1.0)', null, () => ({ agentVersion: '0.1.0' })],
  ['login.succeeded', 'Login with access key v1', null, () => ({ keyVersion: 1, ip: '10.0.1.50' })],
  ['actor.declared', 'ravi@acme.example is working now', 'ravi@acme.example', () => ({ ip: '10.0.1.50' })],
  ['proposal.staged', 'Proposal seq 7 staged: ready', null, () => ({ result: 'ready', durationMs: 18342 })],
  ['proposal.approved', 'Proposal seq 7 approved', 'ravi@acme.example', () => ({ approver: 'ravi@acme.example' })],
  ['update.started', 'Update started (seq 7)', 'ravi@acme.example', () => ({ kind: 'update', sites: ['db', 'app'] })],
  ['update.snapshot', 'Pre-update snapshot taken', null, () => ({ sizeBytes: 81920 })],
  ['update.applied', 'Revision seq 7 applied', null, () => ({ seq: 7, durationMs: 23011 })],
  ['edge.reloaded', 'Edge reloaded: 2 hosts, 3 routes', null, () => ({ hosts: 2, routes: 3, durationMs: 41 })],
  ['cert.issued', 'Certificate issued for hello.acme.internal', null, () => ({ hostname: 'hello.acme.internal', mode: 'internal' })],
  ['secret.revealed', 'Secret api-token revealed', 'mira@acme.example', () => ({ secretSlug: 'api-token', where: 'agent' })],
  ['snapshot.created', 'Snapshot created (scheduled)', null, () => ({ reason: 'scheduled' })],
  ['site.unreachable', 'Site db unreachable', null, () => ({ peerSlug: 'db' })],
  ['site.reachable', 'Site db reachable again', null, () => ({ peerSlug: 'db' })],
  ['transfer.saved', 'Saved plant-layout.pdf to /srv/drop/plant-layout.pdf', 'mira@acme.example', () => ({ fileName: 'plant-layout.pdf' })],
  ['logout', 'Logout', 'ravi@acme.example', () => ({})],
];
export function makeActivities(site: string, count: number): Activity[] {
  const out: Activity[] = [];
  let prev: string | null = null;
  for (let i = 1; i <= count; i++) {
    const [type, summary, actor, data] = ACTS[(i * 7 + (site === 'db' ? 3 : 0)) % ACTS.length];
    const ts = ago((count - i) * 1730 + (site === 'db' ? 400 : 0));
    const hash = fakeSha(`${site}:${i}:${type}`);
    out.push({
      activityId: `${site}:${i}`,
      deploymentId: DEP,
      siteSlug: site,
      seq: i,
      ts,
      actor,
      origin: actor ? 'agent' : 'system',
      type,
      summary,
      data: { siteSlug: site, ...data(i), ...(actor ? { sessionId: '01JF8Q2Z3M4N5P6Q7R8S9T0V1W' } : {}) },
      prevHash: prev,
      hash,
      uploadedVia: i < count - 3 ? 'pipe' : null,
      uploadedAt: i < count - 3 ? ts : null,
    });
    prev = hash;
  }
  return out.reverse(); // newest first
}

const MODULES = ['cloudlink', 'mesh', 'stager', 'distrib', 'driver', 'edge', 'health', 'coordinator', 'snapshot', 'pipe', 'transfer', 'tunnel', 'auth', 'gui'];
const MSGS: [DebugLine['level'], string, Record<string, unknown>][] = [
  ['info', 'poll answered', { cursor: '41', changed: false, ms: 25012 }],
  ['debug', 'ping ok', { peer: 'db', rttMs: 3 }],
  ['info', 'heartbeat sent', { route: 'direct', ms: 88 }],
  ['debug', 'health check passed', { serviceSlug: 'hello-api', type: 'http', status: 200 }],
  ['warn', 'peer slow to answer', { peer: 'db', rttMs: 1840 }],
  ['info', 'activities uploaded', { from: 410, to: 412, ackSeq: 412 }],
  ['debug', 'have list refreshed', { blobs: 14 }],
  ['error', 'cloud call failed, retry in 4 s', { op: 'heartbeat', error: 'dial tcp: i/o timeout' }],
  ['info', 'edge reloaded', { hosts: 2, routes: 3 }],
  ['debug', 'sampler tick', { cpu: 0.11, memMb: 412 }],
];
export function makeDebug(site: string, count: number): DebugLine[] {
  const out: DebugLine[] = [];
  for (let i = 0; i < count; i++) {
    const [level, msg, fields] = MSGS[(i * 3 + (site === 'db' ? 1 : 0)) % MSGS.length];
    out.push({ ts: ago(i * 37 + (site === 'db' ? 5 : 0)), level, module: MODULES[(i * 5) % MODULES.length], msg, fields });
  }
  return out; // newest first
}

export function uptimeCells(count: number, stepMin: number, seed: number, noDataTail = 0): UptimeCell[] {
  const cells: UptimeCell[] = [];
  const start = now - count * stepMin * 60_000;
  for (let i = 0; i < count; i++) {
    const r = Math.sin(i * 12.9898 + seed * 78.233) * 43758.5453;
    const f = r - Math.floor(r);
    const up = i < noDataTail ? 0 : f > 0.985 ? 0.4 : f > 0.96 ? 0.93 : 1;
    cells.push({ t: iso(start + i * stepMin * 60_000), up, samples: i < noDataTail ? 0 : stepMin * 2 });
  }
  return cells;
}

export function certsFor(site: string): CertStatus[] {
  if (site === 'northwind')
    return [{ siteSlug: 'northwind', hostname: CLIENT_HOST, mode: 'files', challenge: null, issuer: 'Client Gov CA', notBefore: ago(86400 * 40), notAfter: iso(now + 86400_000 * 325), lastRenewalAt: null, lastError: null, nextRenewalAt: null }];
  if (site !== 'app') return [];
  return [
    { siteSlug: 'app', hostname: 'hello.acme.internal', mode: 'internal', challenge: null, issuer: 'OPM app local CA', notBefore: ago(3 * 3600), notAfter: iso(now + 9 * 3600_000), lastRenewalAt: ago(3 * 3600), lastError: null, nextRenewalAt: iso(now + 5 * 3600_000) },
    { siteSlug: 'app', hostname: '*', mode: 'none', challenge: null, issuer: null, notBefore: null, notAfter: null, lastRenewalAt: null, lastError: null, nextRenewalAt: null },
  ];
}

export function makeTransfers(site: string): Transfer[] {
  const t = (id: string, direction: Transfer['direction'], fileName: string, size: number, status: Transfer['status'], ageSec: number, by: string, note = '', savedPath: string | null = null, extra: Partial<Transfer> = {}): Transfer => ({
    transferId: id,
    deploymentId: DEP,
    siteSlug: site,
    direction,
    fileName,
    sizeBytes: size,
    sha256: fakeSha(id),
    storagePath: `transfers/${DEP}/${id}`,
    uploadId: null,
    createdAt: ago(ageSec),
    createdBy: by,
    note,
    expiresAt: null,
    status,
    savedPath,
    error: null,
    ...extra,
  });
  // §21 #100 store-and-forward delivery (agent API)
  const dv = (phase: NonNullable<Transfer['delivery']>['phase'], bytesDone: number, attempts = 0, lastError: string | null = null): Pick<Transfer, 'delivery'> => ({
    delivery: { phase, bytesDone, attempts, lastError, lastAttemptAt: attempts ? ago(40) : null, nextAttemptAt: attempts ? iso(now + 80_000) : null },
  });
  if (site === 'app')
    return [
      t('TrfPlantLayout000001', 'to-site', 'plant-layout.pdf', 2_481_334, 'saved-on-server', 86400 * 2, 'uidOps0000000000000001', 'Updated floor plan', '/srv/drop/plant-layout.pdf', dv('on-site', 2_481_334)),
      t('TrfLicense0000000002', 'to-site', 'license-2027.key', 1_812, 'ready', 3600 * 3, 'uidOps0000000000000001', 'New license, valid from Jan 1', null, dv('on-site', 1_812)),
      t('TrfFirmware000000005', 'to-site', 'sensor-firmware-v3.2.bin', 48_211_904, 'ready', 600, 'uidOps0000000000000001', 'Flash during the next window', null, dv('downloading', 28_927_142)),
      t('TrfCalib000000000006', 'to-site', 'calibration-2026-10.csv', 92_118, 'ready', 120, 'uidOps0000000000000001', '', null, dv('pending', 0, 2, 'dial tcp: lookup api.opm.example: i/o timeout')),
      t('TrfManual00000000007', 'to-site', 'operator-manual.pdf', 5_220_331, 'downloaded', 86400 * 6, 'uidOps0000000000000001', '', null, dv('on-site', 5_220_331)),
      t('TrfOldReport00000008', 'to-site', 'q2-report.xlsx', 311_002, 'expired', 86400 * 31, 'uidOps0000000000000001', '', null, dv('on-site', 0)),
      t('TrfLogsApp000000003', 'to-cloud', 'app-logs-2026-10-05.tar.gz', 18_204_113, 'ready', 86400, 'ravi@acme.example', 'Logs for ticket OPS-1182', null, { sourcePath: '/var/log/app/app-logs-2026-10-05.tar.gz', ...dv('stored', 18_204_113) }),
      t('TrfPgDump0000000009', 'to-cloud', 'pg-dump-2026-10-08.sql.gz', 412_336_120, 'uploading', 900, 'ravi@acme.example', 'Nightly dump for support', null, { sourcePath: '/srv/backups/pg-dump-2026-10-08.sql.gz', ...dv('uploading', 164_934_448) }),
      t('local-SupportBundle10', 'to-cloud', 'support-bundle.zip', 9_310_201, 'uploading', 1800, 'mira@acme.example', '', null, dv('queued', 0, 3, 'cloud unreachable (no direct or relay route)')),
      t('TrfShot00000000000011', 'to-cloud', 'hmi-screenshot.png', 811_220, 'downloaded', 86400 * 3, 'mira@acme.example', 'Error on the HMI', null, dv('stored', 811_220)),
    ];
  return [t('TrfDbDump0000000004', 'to-cloud', 'redis-dump.rdb', 4_102_331, 'downloaded', 86400 * 4, 'mira@acme.example', 'Backup for support', null, { sourcePath: '/var/lib/redis/dump.rdb', ...dv('stored', 4_102_331) })];
}

export function snapshotSummary(s: Snapshot, missing: string[] = []): SnapshotSummary {
  const sites = Object.keys(s.sites);
  return {
    snapshotId: s.snapshotId,
    createdAt: s.createdAt,
    reason: s.reason,
    revId: s.revId,
    seq: s.seq,
    createdBy: s.createdBy,
    note: s.note,
    sizeBytes: Object.values(s.sites).reduce((a, x) => a + x.sizeBytes, 0),
    sites: sites.filter((x) => !missing.includes(x)),
    missingSites: missing,
    hasImages: false,
  };
}

// ───────────────────────────────────────────── §21 #93: colored logs (the user's Deno app, agent 0.10.0)
/** Deno's colored error output as Docker stores it (ESC bytes kept): bold red "error", dim gray 256-color 245 "at" lines */
export const DENO_LINES = [
  '\x1b[0m\x1b[1m\x1b[31merror\x1b[0m: Uncaught (in promise) Error: getaddrinfo EAI_AGAIN mqtt',
  '    \x1b[0m\x1b[2m\x1b[38;5;245mat \x1b[0mGetAddrInfoReqWrap.onlookup [as oncomplete] (\x1b[0m\x1b[36mnode:dns\x1b[0m:\x1b[0m\x1b[33m107\x1b[0m:\x1b[0m\x1b[33m26\x1b[0m)',
  '    \x1b[0m\x1b[2m\x1b[38;5;245mat \x1b[0mMqttClient.connect (\x1b[0m\x1b[36mfile:///app/helper/mqtt.ts\x1b[0m:\x1b[0m\x1b[33m41\x1b[0m:\x1b[0m\x1b[33m11\x1b[0m)',
  '\x1b[32mINFO\x1b[39m \x1b[1mserver\x1b[22m listening on http://0.0.0.0:3000',
  '\x1b[33mWARN\x1b[39m mqtt reconnect in 5 s (attempt \x1b[1m3\x1b[22m)',
  '\x1b]0;deno: admin\x07\x1b[2K\x1b[1Gloading config \x1b[38;2;255;135;0mproduction.ts\x1b[0m',
];
/** a pipeline that LOST the ESC bytes (bare "[0m"); "[1] item" must stay text */
export const ESC_LOST_LINES = [
  '[0m[1m[31merror[0m: flow "Device types" failed: getaddrinfo ENOTFOUND mqtt',
  '[0m[32m8 Oct 03:00:01[0m - [info] Started flows',
  '[1] item queued for retry',
  '[0m[2m[38;5;245m    at Broker.connect (/usr/src/node-red/broker.js:88:9)[0m',
];
