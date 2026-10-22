/**
 * In-memory fake of the cloud backend state (VITE_MOCK=1). Persisted to localStorage so reloads and
 * Playwright page.goto() keep the state. Seeded with the hello-world catalog / template / deployment.
 */
import type {
  AccountDoc,
  Activity,
  AgentDoc,
  AuditLogEntry,
  Config,
  Deployment,
  Image,
  IconDoc,
  InstallToken,
  NotFoundPage,
  Proposal,
  Registry,
  Revision,
  Service,
  Tag,
  Template,
  Transfer,
  Upload,
  SharedFile,
  ImageSpec,
  RevisionCatalog,
  SiteConfig,
} from '@opm/shared';
import { normalizeStoredDevToolPreset, newDevToolPreset, fullAdminRules, canonicalize, ulid, parseImageReference, normalizeImageVolume, defaultContainer, defaultSiteConfig, newEdgeRoute, newProxyAction, newEdgeHost } from '@opm/shared';
import helloConfig from '@opm/shared/testdata/hello-config.json';
import helloCatalog from '@opm/shared/testdata/hello-catalog.json';

export interface MockUser {
  uid: string;
  email: string;
  password: string;
}
export interface MockUpload extends Upload {
  parts: Record<number, string>; // base64
  target: 'blob' | 'transfer' | 'file';
}
export interface MockDb {
  v: number;
  users: MockUser[];
  accounts: Record<string, AccountDoc>;
  registries: Record<string, Registry>;
  icons: Record<string, IconDoc & { dataUrl: string }>;
  services: Record<string, Service>;
  images: Record<string, Record<string, Image & { _readyAt?: number; _copyAt?: number; _fail?: boolean }>>;
  notFoundPages: Record<string, NotFoundPage>;
  tags: Record<string, Tag>;
  auditLog: AuditLogEntry[];
  templates: Record<string, Template>;
  deployments: Record<string, Deployment>;
  revisions: Record<string, Revision[]>;
  proposals: Record<string, (Proposal & { _sim?: number })[]>;
  agents: Record<string, Record<string, AgentDoc>>;
  installTokens: Record<string, InstallToken[]>;
  activities: Record<string, Activity[]>;
  transfers: Record<string, Transfer[]>;
  /** §21 #123 shared files per deployment (published + tombstones) */
  files: Record<string, SharedFile[]>;
  uploads: Record<string, MockUpload>;
  blobs: Record<string, string>; // hex → base64 (small blobs only; big ones live in memory)
  seq: number;
}

export const DB_VERSION = 27; // 27: §21 #123 shared files (ACME: in the cloud, site-only, on two sites, no copy, a tombstone); 26: §21 #106 entity slugs + GitHub mirror (mock/github.ts); // 25: §21 #105 no working copy (mock migrate-j: config copies ≠ head auto-committed); // 24: §21 #104 branches (lazy migrate-i in mock/branches.ts: commits, merge bases, agent branches + seed scenario); 23: §21 #100 transfers store-and-forward states (ACME app: waiting / on the site / arriving / stored); 22: §21 #99 template "Postgres (dev tools)" (service postgresql, container postgress); 21: §21 #97 dev-tool presets on ACME db (postgres + redis) + secret pg-password; 20: §21 #93 ANSI update.failed log tail + hint (ACME app), North grid main with an mqtt mention (suggested depends) + an implicit containers.* ref; 19: §21 #92 upgrade commands (stored install tokens / maintenance) + agent clockSkew
const KEY = 'opm.mock.db';
export const memBlobs = new Map<string, Uint8Array>();

let db: MockDb | null = null;
// another tab changed the mock state → reload it on the next request (lets two tabs produce real conflicts)
if (typeof window !== 'undefined')
  window.addEventListener('storage', (e) => {
    if (e.key === KEY) db = null;
  });

export function now(): string {
  return new Date().toISOString();
}
export function ago(ms: number): string {
  return new Date(Date.now() - ms).toISOString();
}
const MIN = 60_000;
const HOUR = 3_600_000;
const DAY = 86_400_000;
/** §21 #90 the pinned agent release of the mock cloud (deps/manifest.json agent) */
export const MOCK_AGENT_RELEASE = {
  version: '0.10.0-10081200',
  notes: 'Agent self-management from the GUIs (§21 #90)\n- Upgrade the agent from the cloud console or the agent GUI: signed download, verify, atomic swap, restart, health check\n- Automatic rollback to the previous binary when the new agent is not healthy within 60 s (watcher + boot guard)\n- Agent page: version, build, enrollment, cloud link, agent.json settings, restart, support bundle\n- Upgrade all sites one at a time; the first failure stops the run',
};

const ALNUM = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
export function autoId(): string {
  const b = crypto.getRandomValues(new Uint8Array(20));
  return Array.from(b, (x) => ALNUM[x % 62]).join('');
}
export function randHex(n: number): string {
  const b = crypto.getRandomValues(new Uint8Array(Math.ceil(n / 2)));
  return Array.from(b, (x) => x.toString(16).padStart(2, '0'))
    .join('')
    .slice(0, n);
}
export function fakeSha(): string {
  return 'sha256:' + randHex(64);
}
export function genSecret(charset: 'alnum' | 'hex' | 'base64url', length: number): string {
  const set = charset === 'hex' ? '0123456789abcdef' : charset === 'base64url' ? ALNUM + '_-' : ALNUM;
  const b = crypto.getRandomValues(new Uint8Array(length));
  return Array.from(b, (x) => set[x % set.length]).join('');
}
const B32 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
export function genAccessKey(): string {
  const b = crypto.getRandomValues(new Uint8Array(24));
  const k = Array.from(b, (x) => B32[x % 32]).join('');
  return k.match(/.{4}/g)!.join('-');
}
export function b64(bytes: Uint8Array): string {
  let s = '';
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(s);
}
export function unb64(s: string): Uint8Array {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}
export async function sha256Hex(bytes: Uint8Array): Promise<string> {
  const d = await crypto.subtle.digest('SHA-256', bytes as unknown as ArrayBuffer);
  return Array.from(new Uint8Array(d), (x) => x.toString(16).padStart(2, '0')).join('');
}
/** sync, non-crypto stand-in for revision hashes (display only in mock) */
export function pseudoHash(v: unknown): string {
  const s = canonicalize(v);
  let h1 = 0x811c9dc5,
    h2 = 0x01000193;
  for (let i = 0; i < s.length; i++) {
    h1 = Math.imul(h1 ^ s.charCodeAt(i), 16777619) >>> 0;
    h2 = Math.imul(h2 + s.charCodeAt(i), 2246822519) >>> 0;
  }
  const part = (h1.toString(16) + h2.toString(16)).padEnd(16, '0');
  return 'sha256:' + part.repeat(4).slice(0, 64);
}

export function getDb(): MockDb {
  if (db) return db;
  try {
    const raw = localStorage.getItem(KEY);
    if (raw) {
      const parsed = JSON.parse(raw) as MockDb;
      if (parsed.v === DB_VERSION) {
        db = parsed;
        return db;
      }
    }
  } catch {
    /* fresh */
  }
  db = seed();
  save();
  return db;
}
let saveTimer: ReturnType<typeof setTimeout> | null = null;
export function save() {
  if (!db) return;
  if (saveTimer) clearTimeout(saveTimer);
  saveTimer = setTimeout(() => {
    try {
      localStorage.setItem(KEY, JSON.stringify(db));
    } catch {
      /* quota: keep memory */
    }
  }, 30);
}
export function saveNow() {
  if (!db) return;
  try {
    localStorage.setItem(KEY, JSON.stringify(db));
  } catch {
    /* ignore */
  }
}
/** data: URL of an uploaded icon (mock stand-in for GET /v1/icons/{id}/content), null when unknown. */
export function mockIconDataUrl(id: string): string | null {
  return getDb().icons[id]?.dataUrl ?? null;
}
export function resetDb() {
  db = seed();
  saveNow();
}

// ───────────────────────────────────────────── seed
const ADMIN = 'uAdmin00000000000001';
const VIEWER = 'uViewer0000000000002';
const OPS = 'uOps0000000000000003';
export const SEED_USERS = { admin: { email: 'admin@example.com', password: 'admin123' }, viewer: { email: 'viewer@example.com', password: 'viewer123' }, ops: { email: 'ops@example.com', password: 'ops12345' } };

function audit(by: string, at: string) {
  return { createdAt: at, createdBy: by, updatedAt: at, updatedBy: by };
}

function hcImage(serviceSlug: string, version: string, registryId: string | null, reference: string, spec: Record<string, unknown>, at: string): Image {
  const layers = [
    { digest: fakeSha(), sizeBytes: 3_412_224 + version.length * 1000, mediaType: 'application/vnd.oci.image.layer.v1.tar+gzip' },
    { digest: fakeSha(), sizeBytes: 5_812_002, mediaType: 'application/vnd.oci.image.layer.v1.tar+gzip' },
  ];
  const pr = parseImageReference(reference);
  if (!pr.ok) throw new Error(`seed reference ${reference}: ${pr.error}`);
  return {
    ...audit(ADMIN, at),
    version,
    serviceSlug,
    reference,
    source: pr.parts,
    registryId,
    digest: fakeSha(),
    platform: 'linux/arm64',
    import: { mode: 'copy', status: 'ready', error: null, sizeBytes: layers.reduce((a, l) => a + l.sizeBytes, 0), manifestMediaType: 'application/vnd.oci.image.manifest.v1+json', configDigest: fakeSha(), layers, startedAt: at, finishedAt: at },
    kind: (spec.kind as 'service' | 'job') ?? 'service',
    entrypoint: null,
    command: null,
    workdir: null,
    user: null,
    ports: (spec.ports as Image['ports']) ?? [],
    env: ((spec.env as Image['env']) ?? []).map((e) => ({ ...e, options: e.options ?? null })),
    volumes: (spec.volumes as Image['volumes']) ?? [],
    healthcheck: (spec.healthcheck as Image['healthcheck']) ?? null,
    stopTimeoutSec: 30,
    description: '',
  };
}

export function imageSpecOf(img: Image): ImageSpec {
  return {
    serviceSlug: img.serviceSlug,
    version: img.version,
    digest: img.digest ?? fakeSha(),
    configDigest: img.import.configDigest ?? fakeSha(),
    platform: img.platform,
    manifestMediaType: img.import.manifestMediaType ?? 'application/vnd.oci.image.manifest.v1+json',
    layers: img.import.layers,
    sizeBytes: img.import.sizeBytes ?? 0,
    kind: img.kind,
    entrypoint: img.entrypoint,
    command: img.command,
    workdir: img.workdir,
    user: img.user,
    ports: img.ports,
    env: img.env,
    volumes: img.volumes,
    healthcheck: img.healthcheck,
    stopTimeoutSec: img.stopTimeoutSec,
  };
}

export function buildCatalog(d: MockDb, config: Config): RevisionCatalog {
  const cat: RevisionCatalog = { services: {}, images: {}, notFoundPages: {} };
  for (const s of config.sites) {
    for (const c of s.config.containers) {
      const svc = d.services[c.serviceSlug];
      if (svc) {
        const up = svc.icon?.startsWith('upload:') ? d.icons[svc.icon.slice(7)]?.dataUrl : undefined;
        cat.services[svc.serviceSlug] = { serviceSlug: svc.serviceSlug, label: svc.label, icon: up ?? svc.icon };
      }
      const img = d.images[c.serviceSlug]?.[c.version];
      if (img && svc) cat.images[`${c.serviceSlug}:${c.version}`] = imageSpecOf(img);
    }
    const p = s.config.notFoundPageId ? d.notFoundPages[s.config.notFoundPageId] : null;
    if (p) cat.notFoundPages[p.notFoundPageId] = { notFoundPageId: p.notFoundPageId, label: p.label, htmlCode: p.htmlCode };
  }
  return cat;
}

function act(depId: string, site: string, seq: number, ts: string, actor: string | null, origin: Activity['origin'], type: string, summary: string, data: Record<string, unknown> = {}, prev: string | null): Activity {
  return { activityId: `${site}:${seq}`, deploymentId: depId, siteSlug: site, seq, ts, actor, origin, type, summary, data, prevHash: prev, hash: fakeSha(), uploadedVia: site === '_cloud' ? null : 'pipe', uploadedAt: site === '_cloud' ? null : ts };
}

function agentDoc(slug: string, enrolledAt: string, headRevId: string | null, port: number, ip: string): AgentDoc {
  return {
    siteSlug: slug,
    publicKey: btoa(randHex(32)),
    enrolledAt,
    agentVersion: '0.1.0',
    lastSeenAt: now(),
    online: true,
    ip,
    hostname: `opm-${slug}-01`,
    advertiseHost: ip,
    detectedAddresses: [ip, `opm-${slug}-01`],
    agentPort: port,
    os: 'linux',
    arch: 'arm64',
    relayVia: null,
    headRevId,
    staging: null,
    keyVersion: 1,
    ackSeq: 12,
    metrics: { cpu: slug === 'app' ? 0.23 : 0.07, memPct: slug === 'app' ? 0.41 : 0.28, diskPct: 0.36 },
    edge: {
      revSeq: 1,
      reloadedAt: ago(2 * HOUR),
      lastReloadError: null,
      boundPorts: slug === 'app' ? [8081, 8090, 7080] : [8082, 8091, 7081],
      // §21 #86: app runs the built-in default edge on :80; db cannot bind :80 (nginx holds it)
      bindErrors: slug === 'app' ? [] : [{ port: 80, purpose: 'edge-http', error: 'listen tcp :80: bind: address already in use', owner: 'nginx' }],
      certs: [],
      defaultEdge: slug === 'app',
    },
    peersSeen: [{ siteSlug: slug === 'app' ? 'db' : 'app', lastSeenAt: ago(8000) }],
    guiPorts:
      slug === 'app'
        ? [
            { port: 80, scheme: 'http', kind: 'edge' },
            { port: 8090, scheme: 'http', kind: 'gui-alias' },
            { port: 7080, scheme: 'http', kind: 'agent-port' },
          ]
        : [
            { port: 8091, scheme: 'http', kind: 'gui-alias' },
            { port: 7081, scheme: 'http', kind: 'agent-port' },
          ],
  };
}

function seed(): MockDb {
  const t0 = ago(20 * DAY);
  const d: MockDb = {
    v: DB_VERSION,
    users: [
      { uid: ADMIN, ...SEED_USERS.admin },
      { uid: VIEWER, ...SEED_USERS.viewer },
      { uid: OPS, ...SEED_USERS.ops },
    ],
    accounts: {},
    registries: {},
    icons: {},
    services: {},
    images: {},
    notFoundPages: {},
    tags: {},
    auditLog: [],
    templates: {},
    deployments: {},
    revisions: {},
    proposals: {},
    agents: {},
    installTokens: {},
    activities: {},
    transfers: {},
    files: {},
    uploads: {},
    blobs: {},
    seq: 0,
  };
  d.accounts[ADMIN] = { uid: ADMIN, img: null, name: 'Asha Admin', email: SEED_USERS.admin.email, phone: '+919812345678', access: fullAdminRules(), disabled: false, createdAt: t0, createdBy: 'bootstrap', lastLoginAt: ago(5 * MIN), lastSetPassword: null, lastResetLink: null };
  d.accounts[VIEWER] = {
    uid: VIEWER,
    img: null,
    name: 'Vik Viewer',
    email: SEED_USERS.viewer.email,
    phone: null,
    access: [
      { resource: 'deployments', actions: ['read'], scope: { kind: 'all' } },
      { resource: 'deployment.config', actions: ['read'], scope: { kind: 'all' } },
      { resource: 'deployment.activities', actions: ['read'], scope: { kind: 'all' } },
      { resource: 'templates', actions: ['read'], scope: { kind: 'all' } },
      { resource: 'registries', actions: ['read'], scope: { kind: 'all' } },
      { resource: 'accounts', actions: ['read'], scope: { kind: 'all' } },
    ],
    disabled: false,
    createdAt: ago(10 * DAY),
    createdBy: ADMIN,
    lastLoginAt: ago(2 * DAY),
    // §21 #84: the password an admin set is stored (viewable with revealAccountPassword)
    lastSetPassword: { value: SEED_USERS.viewer.password, setAt: ago(10 * DAY), setBy: ADMIN, validAfter: null },
    lastResetLink: null,
  };
  d.accounts[OPS] = {
    uid: OPS,
    img: null,
    name: 'Omar Ops',
    email: SEED_USERS.ops.email,
    phone: null,
    access: [
      { resource: 'deployments', actions: ['read', 'write'], scope: { kind: 'tags', tagSlugs: ['pilot'] } },
      { resource: 'deployment.config', actions: ['read', 'write', 'publish'], scope: { kind: 'tags', tagSlugs: ['pilot'] } },
      { resource: 'deployment.setup', actions: ['read', 'write'], scope: { kind: 'tags', tagSlugs: ['pilot'] } },
    ],
    disabled: false,
    createdAt: ago(8 * DAY),
    createdBy: ADMIN,
    lastLoginAt: null,
    // created before §21 #84: nothing stored
    lastSetPassword: null,
    lastResetLink: null,
  };

  const regAr = 'RegArtifact000000001';
  const regHub = 'RegDockerHub00000001';
  d.registries[regAr] = { ...audit(ADMIN, t0), registryId: regAr, label: 'Artifact Registry (asia-south1)', url: 'https://asia-south1-docker.pkg.dev', username: '_json_key', password: '{"type":"service_account","project_id":"your-firebase-project"}', lastCheck: { at: ago(3 * HOUR), ok: true, message: 'login ok' } };
  d.registries[regHub] = { ...audit(ADMIN, t0), registryId: regHub, label: 'Docker Hub', url: 'https://registry-1.docker.io', username: '', password: '', lastCheck: { at: ago(2 * DAY), ok: true, message: 'anonymous access ok (public images only)' } };
  const regHubBot = 'RegDockerHubBot00001';
  d.registries[regHubBot] = { ...audit(ADMIN, t0), registryId: regHubBot, label: 'Docker Hub (acme-bot)', url: 'https://registry-1.docker.io', username: 'acme-bot', password: 'dckr_pat_example', lastCheck: null };
  const regHarbor = 'RegHarborOnPrem00001';
  d.registries[regHarbor] = { ...audit(ADMIN, t0), registryId: regHarbor, label: 'Harbor (on-prem)', url: 'https://harbor.acme.example', username: 'robot$opm', password: 'harbor-secret', lastCheck: null };

  const cat = helloCatalog as unknown as { services: Record<string, { serviceSlug: string; label: string; icon: string }>; images: Record<string, Record<string, unknown>> };
  const repos: Record<string, string> = { 'hello-web': 'example/opm-test/hello-web', 'hello-api': 'example/opm-test/hello-api', redis: 'example/opm-test/redis' };
  const dockerTag: Record<string, string> = { 'hello-web': '1.0.0', 'hello-api': '1.0.0', redis: '7.4.1' };
  const AR = 'asia-south1-docker.pkg.dev';
  for (const s of Object.values(cat.services)) {
    d.services[s.serviceSlug] = { ...audit(ADMIN, t0), serviceSlug: s.serviceSlug, label: s.label, icon: s.icon, description: s.serviceSlug === 'redis' ? 'In-memory store with AOF persistence.' : `The ${s.label} test image.` };
    d.images[s.serviceSlug] = {};
  }
  for (const [key, spec] of Object.entries(cat.images)) {
    const [slug, version] = key.split(':') as [string, string];
    d.images[slug]![version] = hcImage(slug, version, regAr, `${AR}/${repos[slug] ?? `example/opm-test/${slug}`}:${dockerTag[slug] ?? '1.0.0'}`, spec, t0);
  }
  // an extra image to show a failed import
  d.images['hello-web']!['v2'] = { ...hcImage('hello-web', 'v2', regAr, `${AR}/example/opm-test/hello-web:1.1.0-rc1`, cat.images['hello-web:v1']!, ago(DAY)), digest: null, import: { mode: 'copy', status: 'failed', error: `manifest unknown: ${AR}/example/opm-test/hello-web:1.1.0-rc1 not found in the registry`, sizeBytes: null, manifestMediaType: null, configDigest: null, layers: [], startedAt: ago(DAY), finishedAt: ago(DAY) } };

  // Core API: choice env (NODE_ENV) + a mount path with container.env (phase U)
  const core = 'core-api';
  d.services[core] = { ...audit(ADMIN, ago(7 * DAY)), serviceSlug: core, label: 'Core API', icon: 'lucide:server-cog', description: 'Northwind core API (Deno).' };
  d.images[core] = {};
  const coreSpec = (at: string, version: string, dockerTag: string): Image => ({
    ...hcImage(core, version, regHubBot, `docker.io/acme/core-api:${dockerTag}`, {}, at),
    platform: 'linux/amd64',
    kind: 'service',
    entrypoint: null,
    command: ['deno', 'task', 'start'],
    workdir: '/app',
    user: null,
    ports: [{ name: 'http', port: 8080, protocol: 'tcp', description: 'API' }],
    env: [
      { name: 'NODE_ENV', value: 'integration', options: ['integration', 'dc', 'worker', 'pg-listen', 'terminal', 'migrate'], required: true, secret: false, description: 'Which env config file to load' },
      { name: 'LOG_IN_CONSOLE', value: 'false', options: ['true', 'false'], required: false, secret: false, description: '' },
      { name: 'PORT', value: '8080', options: null, required: false, secret: false, description: '' },
      { name: 'DB_PASSWORD', options: null, required: true, secret: true, description: 'Postgres password' },
    ],
    volumes: [
      { name: 'config', mountPath: '/app/helper/config/env/{{ container.env.NODE_ENV }}.ts', kind: 'file', description: 'env config file' },
      { name: 'data', mountPath: '/data', kind: 'dir', description: '' },
    ],
    healthcheck: { type: 'http', portName: 'http', path: '/healthz', expectStatus: 200, intervalSec: 10, timeoutSec: 5, retries: 3, startPeriodSec: 30 },
    stopTimeoutSec: 20,
    description: 'Core API (Deno).',
  });
  d.images[core]!['v1'] = coreSpec(ago(6 * DAY), 'v1', 'v3.0.8');
  d.images[core]!['v2'] = coreSpec(ago(2 * DAY), 'v2', 'v3.0.9');

  // icons library: one uploaded (unused) SVG
  const iconId = 'IcnAcmeLogo000000001';
  const svg = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><rect x="2" y="2" width="20" height="20" rx="5" fill="#0ea5e9"/><path d="M7 16l5-9 5 9z" fill="#fff"/></svg>';
  const svgBytes = new TextEncoder().encode(svg);
  d.icons[iconId] = { iconId, name: 'acme-logo', contentType: 'image/svg+xml', sizeBytes: svgBytes.length, sha256: pseudoHash(svg), createdBy: ADMIN, createdAt: ago(5 * DAY), dataUrl: `data:image/svg+xml;base64,${b64(svgBytes)}` };

  const nfp = 'NfpHello000000000001';
  d.notFoundPages[nfp] = { notFoundPageId: nfp, label: 'Hello 404', htmlCode: '<!doctype html>\n<html><head><meta charset="utf-8"><title>Not found</title>\n<style>body{font-family:system-ui;background:#0f172a;color:#e2e8f0;display:grid;place-items:center;height:100vh;margin:0}h1{font-size:64px;margin:0}</style></head>\n<body><div style="text-align:center"><h1>Hello 404</h1><p>This page does not exist.</p></div></body></html>\n', createdAt: t0, createdBy: ADMIN };

  d.tags['gov'] = { ...audit(ADMIN, t0), tagSlug: 'gov', label: 'Government', icon: 'landmark', color: '#2563eb' };
  d.tags['pilot'] = { ...audit(ADMIN, t0), tagSlug: 'pilot', label: 'Pilot', icon: 'flask-conical', color: '#d97706' };
  d.tags['air-gapped'] = { ...audit(ADMIN, t0), tagSlug: 'air-gapped', label: 'Air-gapped', icon: 'shield', color: '#7c3aed' };

  // template
  const hc = helloConfig as unknown as { config: Config; banner: string };
  const tplCfg: Config = structuredClone(hc.config);
  tplCfg.secrets = tplCfg.secrets.map((s) => ({ ...s, value: null, rotatedAt: null }));
  const tplId = 'TplHelloWorld0000001';
  d.templates[tplId] = { ...audit(ADMIN, ago(15 * DAY)), templateId: tplId, slug: 'hello-world', label: 'Hello world', icon: 'rocket', description: 'Counter API + web page + redis on two sites (app, db).', version: 3, ...tplCfg };
  // round C: a template with an auto host (null) and the same service + version used twice (containers are instances)
  const coreImg = d.images[core]!['v2']!;
  const coreCat = { kind: coreImg.kind, ports: coreImg.ports, env: coreImg.env, volumes: coreImg.volumes, healthcheck: coreImg.healthcheck };
  const apiC = defaultContainer(core, 'v2', coreCat, 'core-api');
  apiC.env = [
    { name: 'NODE_ENV', value: 'integration', secretRef: null },
    { name: 'DB_PASSWORD', value: null, secretRef: 'db-password' },
  ];
  apiC.ports = [{ portName: 'http', hostPort: 18080, hostIp: '0.0.0.0', edge: true }];
  const workerC = defaultContainer(core, 'v2', coreCat, 'core-worker');
  workerC.env = [
    { name: 'NODE_ENV', value: 'worker', secretRef: null },
    { name: 'DB_PASSWORD', value: null, secretRef: 'db-password' },
  ];
  workerC.ports = [{ portName: 'http', hostPort: null, hostIp: '0.0.0.0', edge: false }];
  // round D: volumes are declared on the site; the worker shares the data volume of core-api; the env config file
  // image volume is explicitly not mounted
  apiC.volumes = [
    { imageVolume: 'config', mountPath: null, source: { kind: 'none' }, readOnly: false },
    { imageVolume: 'data', mountPath: null, source: { kind: 'volume', volumeSlug: 'core-api-data' }, readOnly: false },
  ];
  workerC.volumes = structuredClone(apiC.volumes);
  workerC.depends = [{ containerSlug: 'core-api' }];
  const coreSite = defaultSiteConfig(null);
  const redisImg = d.images['redis']!['v1']!;
  const cacheC = defaultContainer('redis', 'v1', { kind: redisImg.kind, ports: redisImg.ports, env: redisImg.env, volumes: redisImg.volumes, healthcheck: redisImg.healthcheck }, 'cache');
  cacheC.ports = cacheC.ports.map((p) => ({ ...p, edge: false }));
  cacheC.volumes = [{ imageVolume: 'data', mountPath: null, source: { kind: 'volume', volumeSlug: 'cache-data' }, readOnly: false }];
  coreSite.containers = [apiC, workerC, cacheC];
  coreSite.volumes = [
    { volumeSlug: 'core-api-data', label: 'Core API data', description: 'Uploads and local state of the core API (shared with the worker).', backup: true, location: { kind: 'auto' }, kind: 'dir', seed: null },
    { volumeSlug: 'cache-data', label: 'Redis cache', description: 'Rebuilt on start; not worth backing up.', backup: false, location: { kind: 'path', path: './volumes/cache/redis' }, kind: 'dir', seed: null },
  ];
  coreSite.edge.routes = [{ ...newEdgeRoute('api', '/', { ...newProxyAction('core-api', 'http'), websocket: false }) }];
  const tplCore = 'TplNorthwindCore0002';
  d.templates[tplCore] = {
    ...audit(ADMIN, ago(16 * DAY)),
    templateId: tplCore,
    slug: 'northwind-core',
    label: 'Northwind core',
    icon: 'lucide:server-cog',
    description: 'Core API + a worker of the same image on one site with an auto-detected host.',
    version: 2,
    variables: [{ variableSlug: 'server-ip', label: 'Server IP', description: 'Not known ahead of time (round C: leave the host on auto)', type: 'string', value: null, required: false, options: null, validation: null }],
    secrets: [{ secretSlug: 'db-password', label: 'DB password', description: '', required: true, generate: { charset: 'alnum', length: 32 }, value: null, rotatedAt: null }],
    files: [],
    sites: [{ siteSlug: 'core', label: 'Core server', config: coreSite }],
  };
  // round D: a template stored before round D (no site volumes, a round-C `data` source saved read-only by the old
  // editor bug) — the API (and this mock) normalize it on read: declared volume, source volume, read-write
  const tplLegacy = 'TplLegacyRoundC00003';
  const legacyRedis = { ...defaultContainer('redis', 'v1', { kind: redisImg.kind, ports: redisImg.ports, env: redisImg.env, volumes: redisImg.volumes, healthcheck: redisImg.healthcheck }, 'cache'), volumes: [{ imageVolume: 'data', mountPath: null, source: { kind: 'data', name: 'cache-data' }, readOnly: true }] };
  const legacySite = { ...defaultSiteConfig(null), containers: [legacyRedis] } as Record<string, unknown>;
  delete legacySite.volumes;
  d.templates[tplLegacy] = {
    ...audit(ADMIN, ago(20 * DAY)),
    templateId: tplLegacy,
    slug: 'legacy-round-c',
    label: 'Legacy (round C)',
    icon: 'lucide:archive',
    description: 'Stored before site volumes existed: opens with cache-data declared and read-write.',
    version: 4,
    variables: [],
    secrets: [],
    files: [],
    sites: [{ siteSlug: 'old', label: 'Old site', config: legacySite as unknown as SiteConfig }],
  };
  // round E: "Northwind client (nginx)" — containers named like e2e/fixtures/client-nginx.conf so the nginx importer resolves
  // every upstream; route x-ez-terminal-s3 reproduces the user's mistake (Cache-Control set as a REQUEST header).
  const extraSvc = (slug: string, label: string, icon: string, port: number, ref: string) => {
    d.services[slug] = { ...audit(ADMIN, ago(9 * DAY)), serviceSlug: slug, label, icon, description: `${label} (round E mock).` };
    d.images[slug] = { v1: hcImage(slug, 'v1', regHub, ref, { ports: [{ name: 'http', port, protocol: 'tcp', description: '' }] }, ago(9 * DAY)) };
  };
  extraSvc('northwind-admin', 'Northwind admin', 'lucide:layout-dashboard', 3000, 'docker.io/acme/northwind-admin:1.0.0');
  extraSvc('pg-admin', 'pgAdmin', 'lucide:database', 80, 'docker.io/dpage/pgadmin4:8.12');
  extraSvc('nodered', 'Node-RED', 'lucide:workflow', 1880, 'docker.io/nodered/node-red:4.0.5');
  extraSvc('northwind-web', 'Northwind web', 'lucide:globe', 80, 'docker.io/acme/northwind-web:1.0.0');
  const clientSite = defaultSiteConfig(null);
  const mk = (svc: string, slug: string, nodeEnv?: string) => {
    const im = d.images[svc]!['v1'] ?? d.images[svc]!['v2']!;
    const ver = svc === core ? 'v2' : 'v1';
    const img = svc === core ? coreImg : im;
    const c = defaultContainer(svc, ver, { kind: img.kind, ports: img.ports, env: img.env, volumes: img.volumes, healthcheck: img.healthcheck }, slug);
    if (svc === core) {
      c.env = [
        { name: 'NODE_ENV', value: nodeEnv ?? 'integration', secretRef: null },
        { name: 'DB_PASSWORD', value: null, secretRef: 'db-password' },
      ];
      c.volumes = [
        { imageVolume: 'config', mountPath: null, source: { kind: 'none' }, readOnly: false },
        { imageVolume: 'data', mountPath: null, source: { kind: 'none' }, readOnly: false },
      ];
    }
    return c;
  };
  clientSite.containers = [
    mk(core, 'ez-integration', 'integration'),
    mk(core, 'ez-dc', 'dc'),
    mk(core, 'ez-worker', 'worker'),
    mk(core, 'ez-pg-listen', 'pg-listen'),
    mk(core, 'ez-terminal', 'terminal'),
    mk(core, 'ez-migrate', 'migrate'),
    mk(core, 'ez-admin', 'integration'),
    mk(core, 'ez-heatmap', 'integration'),
    mk('northwind-admin', 'northwind-admin'),
    mk('pg-admin', 'pg-admin'),
    mk('nodered', 'nodered'),
    mk('northwind-web', 'northwind-web'),
  ];
  // round E (B): Node-RED keeps its flows in a FOLDER volume seeded with two files (applied when created / empty)
  clientSite.volumes = [
    {
      volumeSlug: 'nodered-data',
      label: 'Node-RED data',
      description: 'flows + settings; prefilled, then edited on site',
      backup: true,
      location: { kind: 'auto' },
      kind: 'dir',
      seed: { kind: 'files', entries: [{ path: 'flows.json', fileSlug: 'nodered-flow' }, { path: 'settings.js', fileSlug: 'nodered-settings' }] },
    },
  ];
  clientSite.containers.find((x) => x.containerSlug === 'nodered')!.volumes = [{ imageVolume: null, mountPath: '/data', source: { kind: 'volume', volumeSlug: 'nodered-data' }, readOnly: false }];
  clientSite.edge.hosts = [{ ...newEdgeHost('*'), responseHeaders: { set: {}, add: { 'Cache-Control': 'no-store, no-cache, must-revalidate, proxy-revalidate, max-age=0', Pragma: 'no-cache', Expires: '0' }, remove: [], always: [] } }];
  clientSite.edge.maxBodyBytes = 100 * 1024 * 1024;
  clientSite.edge.routes = [
    newEdgeRoute('x-ez-terminal', '/x/ez-terminal/', { ...newProxyAction('ez-terminal', 'http'), stripPrefix: true }, 1),
    // the user's mistake: nginx `proxy_hide_header … + add_header Cache-Control "public, …"` mapped to a REQUEST header
    newEdgeRoute('x-ez-terminal-s3', '/x/ez-terminal/s3/', { ...newProxyAction('ez-terminal', 'http'), rewrite: { regex: '^/x/ez-terminal(/.*)$', replacement: '$1' }, setHeaders: { 'Cache-Control': 'public, max-age=3600, must-revalidate' } }, 2),
    newEdgeRoute('root', '/', newProxyAction('northwind-web', 'http'), 3),
  ];
  const enc = (t: string) => b64(new TextEncoder().encode(t));
  d.blobs['db624d2bd7d77b12880714a2e2a9d8b24e3f8a55fcc384e54cf31640a620afdc'] = enc('[\n  { "id": "tab1", "type": "tab", "label": "Device types" }\n]\n');
  d.blobs['cef5ec97f974719f668237e4792780a04579332da2c5ffed2688a4e346e4de0b'] = enc('module.exports = {\n  httpAdminRoot: "/x/nodered/",\n  flowFile: "flows.json",\n};\n');
  // round E: a site whose containers are named like the user's "Single" (northwind-backend-*, node-red) — the importer
  // cannot resolve the fixture by name; the Upstreams step prefills suggestUpstream() guesses
  const site02 = defaultSiteConfig(null);
  site02.containers = [
    mk(core, 'northwind-backend-terminal', 'terminal'),
    mk(core, 'northwind-backend-integration', 'integration'),
    mk(core, 'northwind-backend-admin', 'integration'),
    mk(core, 'northwind-backend-worker', 'worker'),
    mk('nodered', 'node-red'),
    mk('northwind-web', 'northwind-web'),
  ];
  const tplClient = 'TplNorthwindClient04';
  d.templates[tplClient] = {
    ...audit(ADMIN, ago(25 * DAY)),
    templateId: tplClient,
    slug: 'northwind-client-nginx',
    label: 'Northwind client (nginx)',
    icon: 'lucide:network',
    description: 'One site with the containers of the client nginx.conf (round E importer demo).',
    version: 1,
    variables: [],
    secrets: [
      { secretSlug: 'db-password', label: 'DB password', description: '', required: true, generate: { charset: 'alnum', length: 32 }, value: null, rotatedAt: null },
      { secretSlug: 'client-key', label: 'TLS key (client.key)', description: 'PEM private key of env.client.example.gov.in', required: true, generate: null, value: null, rotatedAt: null },
    ],
    files: [
      { fileSlug: 'client-crt', label: 'TLS certificate (client.crt)', description: 'PEM chain of env.client.example.gov.in', mime: 'application/x-pem-file', editor: 'text', maxBytes: 65536, content: null, render: 'none', location: { kind: 'auto' } },
      { fileSlug: 'nodered-flow', label: 'Node-RED flows', description: 'starter flows.json', mime: 'application/json', editor: 'text', maxBytes: 1048576, content: { sha256: 'sha256:db624d2bd7d77b12880714a2e2a9d8b24e3f8a55fcc384e54cf31640a620afdc', sizeBytes: 63 }, render: 'none', location: { kind: 'auto' } },
      { fileSlug: 'nodered-settings', label: 'Node-RED settings', description: 'settings.js', mime: 'application/javascript', editor: 'text', maxBytes: 65536, content: { sha256: 'sha256:cef5ec97f974719f668237e4792780a04579332da2c5ffed2688a4e346e4de0b', sizeBytes: 80 }, render: 'none', location: { kind: 'auto' } },
    ],
    sites: [
      { siteSlug: 'site-01', label: 'Client server', config: clientSite },
      { siteSlug: 'site-02', label: 'Named like Single', config: site02 },
    ],
  };
  // round E (A): required variable without a template value (set at deployment) used in the site host + an env value →
  // no warnings in the template (DEPLOY_PLACEHOLDER); an optional variable without value → one info V_VAR_OPTIONAL_NULL.
  const reqSite = defaultSiteConfig('{{ var.server-ip }}');
  const reqApi = defaultContainer(core, 'v2', coreCat, 'backend-terminal');
  reqApi.env = [
    { name: 'NODE_ENV', value: 'terminal', secretRef: null },
    { name: 'DB_PASSWORD', value: null, secretRef: 'db-password' },
    { name: 'SERVER_URL', value: 'http://{{ var.server-ip }}:8080', secretRef: null },
  ];
  reqApi.volumes = [
    { imageVolume: 'config', mountPath: null, source: { kind: 'none' }, readOnly: false },
    { imageVolume: 'data', mountPath: null, source: { kind: 'none' }, readOnly: false },
  ];
  reqSite.containers = [reqApi];
  const tplReq = 'TplRequiredVars00005';
  d.templates[tplReq] = {
    ...audit(ADMIN, ago(24 * DAY)),
    templateId: tplReq,
    slug: 'single-required-vars',
    label: 'Single (required vars)',
    icon: 'lucide:server',
    description: 'server-ip is required and typed at deployment time: no warnings in the template.',
    version: 1,
    variables: [
      { variableSlug: 'server-ip', label: 'Server IP', description: 'Typed when the deployment is created', type: 'string', value: null, required: true, options: null, validation: null },
      { variableSlug: 'banner-text', label: 'Banner text', description: 'Optional', type: 'string', value: null, required: false, options: null, validation: null },
    ],
    secrets: [{ secretSlug: 'db-password', label: 'DB password', description: '', required: true, generate: { charset: 'alnum', length: 32 }, value: null, rotatedAt: null }],
    files: [],
    sites: [{ siteSlug: 'site-01', label: 'Server', config: reqSite }],
  };
  // §21 #83 (user report): image node-red:v1 declares FILE image volumes flow → /data/flows.json and settings →
  // /data/settings.js; template "Single (volume kinds)" mounts single-file site volumes on them (the user's "Single")
  d.services['node-red'] = { ...audit(ADMIN, ago(8 * DAY)), serviceSlug: 'node-red', label: 'Node-RED', icon: 'lucide:workflow', description: 'Node-RED (volume kind mock).' };
  d.images['node-red'] = {
    v1: hcImage('node-red', 'v1', regHub, 'docker.io/nodered/node-red:4.0.5', {
      ports: [{ name: 'http', port: 1880, protocol: 'tcp', description: '' }],
      volumes: [
        { name: 'flow', mountPath: '/data/flows.json', kind: 'file', description: 'flows' },
        { name: 'settings', mountPath: '/data/settings.js', kind: 'file', description: 'settings' },
      ],
    }, ago(8 * DAY)),
  };
  const nrImg = d.images['node-red']!['v1']!;
  const kindSite = defaultSiteConfig(null);
  const nr = defaultContainer('node-red', 'v1', { kind: nrImg.kind, ports: nrImg.ports, env: nrImg.env, volumes: nrImg.volumes, healthcheck: nrImg.healthcheck }, 'node-red');
  nr.volumes = [
    { imageVolume: 'flow', mountPath: null, source: { kind: 'volume', volumeSlug: 'nodered-flow' }, readOnly: false },
    { imageVolume: 'settings', mountPath: null, source: { kind: 'volume', volumeSlug: 'nodered-settings' }, readOnly: true },
  ];
  kindSite.containers = [nr];
  kindSite.volumes = [
    { volumeSlug: 'nodered-flow', label: 'Node-RED flows', description: '', backup: true, location: { kind: 'auto' }, kind: 'file', seed: { kind: 'file', fileSlug: 'nodered-flow' } },
    { volumeSlug: 'nodered-settings', label: 'Node-RED settings', description: '', backup: true, location: { kind: 'auto' }, kind: 'file', seed: { kind: 'file', fileSlug: 'nodered-settings' } },
    { volumeSlug: 'nodered-data', label: 'Node-RED data', description: '', backup: true, location: { kind: 'auto' }, kind: 'dir', seed: null },
  ];
  kindSite.containers.push({ ...defaultContainer('node-red', 'v1', { kind: nrImg.kind, ports: nrImg.ports, env: nrImg.env, volumes: nrImg.volumes, healthcheck: nrImg.healthcheck }, 'node-red-2'), volumes: [{ imageVolume: 'flow', mountPath: null, source: { kind: 'volume', volumeSlug: 'nodered-data' }, readOnly: false }, { imageVolume: 'settings', mountPath: null, source: { kind: 'none' }, readOnly: false }], enabled: false });
  const tplKinds = 'TplVolumeKinds000006';
  d.templates[tplKinds] = {
    ...audit(ADMIN, ago(23 * DAY)),
    templateId: tplKinds,
    slug: 'single-volume-kinds',
    label: 'Single (volume kinds)',
    icon: 'lucide:server',
    description: 'node-red with single-file volumes on File image volumes (flows.json, settings.js).',
    version: 1,
    variables: [],
    secrets: [],
    files: [
      { fileSlug: 'nodered-flow', label: 'Node-RED flows', description: 'starter flows.json', mime: 'application/json', editor: 'text', maxBytes: 1048576, content: { sha256: 'sha256:db624d2bd7d77b12880714a2e2a9d8b24e3f8a55fcc384e54cf31640a620afdc', sizeBytes: 63 }, render: 'none', location: { kind: 'auto' } },
      { fileSlug: 'nodered-settings', label: 'Node-RED settings', description: 'settings.js', mime: 'application/javascript', editor: 'text', maxBytes: 65536, content: { sha256: 'sha256:cef5ec97f974719f668237e4792780a04579332da2c5ffed2688a4e346e4de0b', sizeBytes: 80 }, render: 'none', location: { kind: 'auto' } },
    ],
    sites: [{ siteSlug: 'site-01', label: 'Server', config: kindSite }],
  };
  // §21 #99 (user report: "in the expression I am not able to select the env variable of container"): template
  // "Postgres (dev tools)" — site db runs postgres:17 as container `postgress` (port `service`); POSTGRES_USER is a plain
  // container value, POSTGRES_PASSWORD comes from the secret pg-password, POSTGRES_DB / PGDATA are image defaults. The
  // dev-tool preset drawer completes {{ containers.postgress.env.<NAME> }} and suggests those env names.
  d.services['postgresql'] = { ...audit(ADMIN, ago(40 * DAY)), serviceSlug: 'postgresql', label: 'PostgreSQL', icon: 'lucide:database', description: 'PostgreSQL (dev tools mock).' };
  d.images['postgresql'] = {
    v17: hcImage('postgresql', 'v17', regHub, 'docker.io/library/postgres:17.2', {
      ports: [{ name: 'service', port: 5432, protocol: 'tcp', description: 'PostgreSQL' }],
      env: [
        { name: 'POSTGRES_USER', value: 'postgres', required: false, secret: false, description: 'superuser name' },
        { name: 'POSTGRES_PASSWORD', required: true, secret: true, description: 'superuser password' },
        { name: 'POSTGRES_DB', required: false, secret: false, description: 'default database (= the user)' },
        { name: 'PGDATA', value: '/var/lib/postgresql/data', required: false, secret: false, description: 'data folder' },
      ],
      volumes: [{ name: 'data', mountPath: '/var/lib/postgresql/data', kind: 'dir', description: 'database files' }],
    }, ago(40 * DAY)),
  };
  const pgImg = d.images['postgresql']!['v17']!;
  const pgSite = defaultSiteConfig(null);
  const pgC = defaultContainer('postgresql', 'v17', { kind: pgImg.kind, ports: pgImg.ports, env: pgImg.env, volumes: pgImg.volumes, healthcheck: pgImg.healthcheck }, 'postgress');
  pgC.env = [
    { name: 'POSTGRES_USER', value: 'plant', secretRef: null },
    { name: 'POSTGRES_PASSWORD', value: null, secretRef: 'pg-password' },
  ];
  pgC.volumes = [{ imageVolume: 'data', mountPath: null, source: { kind: 'volume', volumeSlug: 'pg-data' }, readOnly: false }];
  pgSite.containers = [pgC];
  pgSite.volumes = [{ volumeSlug: 'pg-data', label: 'PostgreSQL data', description: '', backup: true, location: { kind: 'auto' }, kind: 'dir', seed: null }];
  const tplPg = 'TplPostgresDevTool07';
  d.templates[tplPg] = {
    ...audit(ADMIN, ago(40 * DAY)),
    templateId: tplPg,
    slug: 'postgres-dev-tools',
    label: 'Postgres (dev tools)',
    icon: 'lucide:database',
    description: 'One PostgreSQL container `postgress`; dev-tool presets read its env (§21 #99).',
    version: 1,
    variables: [],
    secrets: [{ secretSlug: 'pg-password', label: 'PostgreSQL password', description: 'Password of the postgres superuser.', required: true, generate: { charset: 'alnum', length: 24 }, value: null, rotatedAt: null }],
    files: [],
    sites: [{ siteSlug: 'db', label: 'DB server', config: pgSite }],
  };
  const banner = new TextEncoder().encode(hc.banner);
  d.blobs['170d0b7ecf5138276579feee3f1d76f872c47676a374560bb8bf6467c9f3e805'] = b64(banner);

  // deployment ACME (head seq 1, draft with theme yellow)
  const depId = 'DepAcmePlant00000001';
  const headCfg: Config = structuredClone(hc.config);
  headCfg.secrets = headCfg.secrets.map((x) => ({ ...x, rotatedAt: ago(3 * DAY) }));
  const rev1: Revision = { revId: ulid(Date.now() - 2 * DAY), seq: 1, parentRevId: null, origin: 'cloud', actor: ADMIN, createdAt: ago(2 * DAY), config: headCfg, hash: pseudoHash(headCfg), catalog: { services: {}, images: {}, notFoundPages: {} }, catalogHash: '', summary: 'release 1', kind: 'change', rollbackOf: null };
  rev1.catalog = buildCatalog(d, headCfg);
  rev1.catalogHash = pseudoHash(rev1.catalog);
  const draft: Config = structuredClone(headCfg);
  draft.variables.find((v) => v.variableSlug === 'theme')!.value = 'yellow';
  // §21 #97 dev-tool presets on site db (agent 0.9.0 → the publish dialog warns: presets need agent 0.11.0+): a PostgreSQL
  // on the db server (host target, password = {{ secret.pg-password }}) and the redis container (read-only)
  draft.secrets.push({ secretSlug: 'pg-password', label: 'PostgreSQL password', description: 'Password of the postgres user on the db server.', required: true, generate: null, value: 'pg-Secr3t-Value', rotatedAt: ago(DAY) });
  const dbSite = draft.sites.find((s) => s.siteSlug === 'db')!;
  dbSite.config.devTools = [
    normalizeStoredDevToolPreset({
      ...newDevToolPreset('postgres'),
      name: 'Plant database',
      description: 'PostgreSQL of the plant historian on the db server.',
      target: { kind: 'host', host: '10.0.0.12', port: 5432 },
      auth: { ...newDevToolPreset('postgres').auth, username: 'postgres', password: { kind: 'expr', expr: '{{ secret.pg-password }}' }, database: 'acme' },
    }),
    normalizeStoredDevToolPreset({
      ...newDevToolPreset('redis'),
      name: 'Redis cache',
      description: 'The redis container of this site.',
      target: { kind: 'container', containerSlug: 'redis', portName: 'tcp' },
      readOnly: true,
    }),
  ];
  d.deployments[depId] = {
    ...audit(ADMIN, ago(3 * DAY)),
    deploymentId: depId,
    slug: 'acme-plant',
    label: 'ACME plant',
    image: null,
    tagSlugs: ['gov', 'pilot'],
    managerAccountUids: [],
    srcTemplate: { templateId: tplId, templateVersion: 3, snapshot: d.templates[tplId]!, copiedAt: ago(3 * DAY), baseVersion: 3, baseSnapshot: structuredClone(tplCfg), lastSyncedAt: null, lastSyncedBy: null, skipped: [] },
    draft,
    draftVersion: 7,
    head: { revId: rev1.revId, seq: 1, hash: rev1.hash, reportedAt: ago(2 * DAY - 14 * MIN) },
    accessKeyHash: '$argon2id$v=19$m=65536,t=3,p=1$mock$mock',
    accessKey: null, // created before §21 #84 → "not stored — regenerate to view"
    accessKeyVersion: 1,
    accessKeyRotatedAt: ago(3 * DAY),
    accessKeyRotatedBy: ADMIN,
    agentEpoch: 9,
    cloudActivitySeq: 6,
    deleted: false,
  };
  d.revisions[depId] = [rev1];
  d.proposals[depId] = [
    { revId: rev1.revId, parentRevId: null, seq: 1, summary: 'release 1', origin: 'cloud', kind: 'change', createdAt: rev1.createdAt, createdBy: ADMIN, status: 'applied', sites: { app: { staging: 'ready', progress: 1, error: null, updatedAt: ago(2 * DAY) }, db: { staging: 'ready', progress: 1, error: null, updatedAt: ago(2 * DAY) } }, approvedBy: 'ops@example.com', approvedAt: ago(2 * DAY - 9 * MIN), updateId: ulid(Date.now() - 2 * DAY), result: { ok: true, message: 'applied', snapshotId: ulid(Date.now() - 2 * DAY) }, supersededBy: null, updatedAt: ago(2 * DAY - 13 * MIN) },
  ];
  const enrolledAt = ago(3 * DAY - HOUR);
  d.agents[depId] = { app: agentDoc('app', enrolledAt, rev1.revId, 7080, '10.0.0.11'), db: agentDoc('db', enrolledAt, rev1.revId, 7081, '10.0.0.12') };
  // §21 #90 agent versions vs the pinned release MOCK_AGENT_RELEASE (0.10.0-10081200): app = upgrade available,
  // db = 0.9.0 (below GUI_UPGRADE_MIN_VERSION → rerun-install hint), lab (enrolled, removed from the draft) = up to date
  d.agents[depId].app!.agentVersion = '0.10.0-10080900';
  d.agents[depId].db!.agentVersion = '0.9.0-10080530';
  const lab = agentDoc('lab', ago(2 * DAY), rev1.revId, 7082, '10.0.0.13');
  lab.agentVersion = MOCK_AGENT_RELEASE.version;
  lab.edge = { ...lab.edge!, bindErrors: [], defaultEdge: false, boundPorts: [80, 8092, 7082] };
  lab.guiPorts = [
    { port: 80, scheme: 'http', kind: 'edge' },
    { port: 7082, scheme: 'http', kind: 'agent-port' },
  ];
  lab.headRevId = rev1.revId;
  d.agents[depId].lab = lab;
  // §21 #92: app + lab kept their install token's plaintext (= their upgrade command, View); db's token is from before
  // commands were kept (token null → "not stored — Regenerate", like the real Dev Server)
  const opi = (n: string) => 'opi_' + (n + randHex(43)).slice(0, 43);
  d.installTokens[depId] = [
    { tokenHash: randHex(64), siteSlug: 'app', kind: 'install', token: opi('app'), expiresAt: ago(2 * DAY), usedAt: enrolledAt, revokedAt: null, createdAt: ago(3 * DAY), createdBy: ADMIN },
    { tokenHash: randHex(64), siteSlug: 'db', token: null, expiresAt: ago(2 * DAY), usedAt: enrolledAt, revokedAt: null, createdAt: ago(3 * DAY), createdBy: ADMIN },
    { tokenHash: randHex(64), siteSlug: 'lab', kind: 'install', token: opi('lab'), expiresAt: ago(DAY), usedAt: lab.enrolledAt, revokedAt: null, createdAt: ago(2 * DAY + HOUR), createdBy: ADMIN },
  ];
  // §21 #92 agent clocks: app in sync, db 5 h 30 min ahead (warning), lab not measured (agent < 0.11)
  d.agents[depId].app!.clockSkew = { skewSec: 1, measuredAt: now() };
  d.agents[depId].db!.clockSkew = { skewSec: 19800, measuredAt: now() };
  const A: Activity[] = [];
  let h: string | null = null;
  const push = (site: string, seq: number, ts: string, actor: string | null, origin: Activity['origin'], type: string, summary: string, data: Record<string, unknown> = {}) => {
    const a = act(depId, site, seq, ts, actor, origin, type, summary, data, seq === 1 ? null : h);
    h = a.hash;
    A.push(a);
  };
  push('_cloud', 1, ago(3 * DAY), ADMIN, 'cloud', 'deployment.created', 'Deployment ACME plant created from Hello world v3', { label: 'ACME plant', templateId: tplId });
  push('_cloud', 2, ago(3 * DAY - 2 * MIN), ADMIN, 'cloud', 'install-token.created', 'Install command created for app', { siteSlug: 'app' });
  push('_cloud', 3, ago(3 * DAY - 3 * MIN), ADMIN, 'cloud', 'install-token.created', 'Install command created for db', { siteSlug: 'db' });
  push('_cloud', 4, ago(3 * DAY - HOUR), null, 'cloud', 'install.enrolled', 'Site app enrolled', { siteSlug: 'app' });
  push('_cloud', 5, ago(3 * DAY - HOUR + MIN), null, 'cloud', 'install.enrolled', 'Site db enrolled', { siteSlug: 'db' });
  push('_cloud', 6, ago(2 * DAY), ADMIN, 'cloud', 'proposal.published', 'Proposal 1 published: release 1', { revId: rev1.revId, seq: 1, summary: 'release 1' });
  for (const site of ['app', 'db']) {
    h = null;
    const base = 3 * DAY - HOUR;
    push(site, 1, ago(base), null, 'agent', 'install.enrolled', `Enrolled as ${site}`, { agentVersion: '0.1.0', publicKeyFingerprint: randHex(16) });
    push(site, 2, ago(base - 5000), null, 'system', 'agent.started', 'Agent 0.1.0 started', { agentVersion: '0.1.0', headRevId: null });
    push(site, 3, ago(2 * DAY - 5 * MIN), null, 'system', 'proposal.staged', 'Proposal 1 staged: ready', { revId: rev1.revId, result: 'ready', durationMs: 48211 });
    push(site, 4, ago(2 * DAY - 8 * MIN), null, 'agent', 'login.succeeded', `Login on ${site}`, { siteSlug: site, keyVersion: 1, ip: '10.0.0.5', userAgent: 'Mozilla/5.0' });
    push(site, 5, ago(2 * DAY - 8 * MIN - 2000), 'ops@example.com', 'agent', 'actor.declared', 'ops@example.com is working', { email: 'ops@example.com', siteSlug: site });
    if (site === 'app') push(site, 6, ago(2 * DAY - 9 * MIN), 'ops@example.com', 'agent', 'proposal.approved', 'Proposal 1 approved', { revId: rev1.revId, approver: 'ops@example.com' });
    const s0 = site === 'app' ? 7 : 6;
    if (site === 'app') push(site, s0, ago(2 * DAY - 9 * MIN - 1000), 'ops@example.com', 'agent', 'update.started', 'Update to seq 1 started', { revId: rev1.revId, kind: 'update', sites: ['db', 'app'] });
    const s1 = site === 'app' ? 8 : 6;
    push(site, s1, ago(2 * DAY - 10 * MIN), 'ops@example.com', 'agent', 'update.snapshot', 'Pre-update snapshot done', { revId: rev1.revId, sizeBytes: 1024 });
    push(site, s1 + 1, ago(2 * DAY - 12 * MIN), null, 'system', 'edge.reloaded', 'Edge reloaded (1 host, ' + (site === 'app' ? 3 : 0) + ' routes)', { revSeq: 1, hosts: 1, routes: site === 'app' ? 3 : 0, durationMs: 12 });
    push(site, s1 + 2, ago(2 * DAY - 13 * MIN), 'ops@example.com', 'agent', 'update.applied', 'Applied seq 1', { revId: rev1.revId, seq: 1, durationMs: 241_002 });
    // §21 #87: a failed update carries the failed containers (exit code, restarts, log tail)
    if (site === 'app')
      push(site, s1 + 3, ago(DAY), null, 'system', 'update.failed', 'apply failed on app: hello-api: unhealthy', {
        revId: rev1.revId,
        step: 'apply',
        siteSlug: 'app',
        error: 'hello-api: unhealthy: 3 checks failed in a row after the 30 s start period: container is restarting: last exit code 3, 7 restarts',
        containerSlug: 'hello-api',
        failures: [{ containerSlug: 'hello-api', error: 'hello-api: unhealthy: container is restarting: last exit code 3, 7 restarts', exitCode: 3, oomKilled: false, restartCount: 7, ports: 'none', keptAs: 'opm_dep_app_hello-api-failed-1791400000', logTail: ['2026-10-07T03:00:01.000Z stderr fatal: DB_URL is not set'] }],
      });
    // §21 #90 agent self-management activities (app): CLI upgrade, settings change, restart, support bundle, a rolled-back attempt
    if (site === 'app') {
      const u1 = ulid(Date.now() - 20 * HOUR);
      const u2 = ulid(Date.now() - 6 * HOUR);
      push(site, 12, ago(20 * HOUR), 'ops@example.com', 'agent', 'agent.upgrade-started', 'Agent upgrade 0.9.2-10080200 → 0.10.0-10080900 started', { upgradeId: u1, from: '0.9.2-10080200', to: '0.10.0-10080900', via: 'cli' });
      push(site, 13, ago(20 * HOUR - 41_000), 'ops@example.com', 'agent', 'agent.upgraded', 'Agent upgraded to 0.10.0-10080900', { upgradeId: u1, from: '0.9.2-10080200', to: '0.10.0-10080900', durationSec: 41, via: 'cli' });
      push(site, 14, ago(19 * HOUR), 'ops@example.com', 'agent', 'agent.config-changed', 'Agent settings changed: tools.hostShell, edgeHttpPort', { changed: ['tools.hostShell', 'edgeHttpPort'], before: { 'tools.hostShell': false, edgeHttpPort: 80 }, after: { 'tools.hostShell': true, edgeHttpPort: 80 } });
      push(site, 15, ago(19 * HOUR - 5000), 'ops@example.com', 'agent', 'agent.restarted', 'Agent restarted (settings changed)', { reason: 'settings changed', via: 'systemd' });
      push(site, 16, ago(18 * HOUR), 'ops@example.com', 'agent', 'agent.support-bundle-downloaded', 'Support bundle downloaded (182 KB)', { sizeBytes: 186_412, sections: ['info', 'agent.json', 'logs', 'docker', 'system'] });
      push(site, 17, ago(6 * HOUR), 'admin@example.com', 'agent', 'agent.upgrade-started', 'Agent upgrade 0.10.0-10080900 → 0.10.0-10081000 started', { upgradeId: u2, from: '0.10.0-10080900', to: '0.10.0-10081000', via: 'gui' });
      push(site, 18, ago(6 * HOUR - 75_000), 'admin@example.com', 'agent', 'agent.upgrade-failed', 'Agent upgrade to 0.10.0-10081000 failed in phase health', { upgradeId: u2, from: '0.10.0-10080900', to: '0.10.0-10081000', phase: 'health', error: 'the new agent did not report healthy within 60 s', via: 'gui' });
      push(site, 19, ago(6 * HOUR - 76_000), 'admin@example.com', 'agent', 'agent.rolled-back', 'Agent rolled back to 0.10.0-10080900', { upgradeId: u2, from: '0.10.0-10081000', to: '0.10.0-10080900', reason: 'not healthy within 60 s after the restart', by: 'watcher' });
    }
    // §21 #93: a Deno app's log tail with real ANSI (ESC bytes stored) + the agent's DNS hint
    if (site === 'app')
      push(site, 20, ago(2 * HOUR), null, 'system', 'update.failed', 'apply failed on app: northwind-backend-admin: unhealthy', {
        revId: rev1.revId,
        step: 'apply',
        siteSlug: 'app',
        error: 'northwind-backend-admin: unhealthy: container is restarting: last exit code 1, 4 restarts',
        containerSlug: 'northwind-backend-admin',
        failures: [
          {
            containerSlug: 'northwind-backend-admin',
            error: 'northwind-backend-admin: unhealthy: container is restarting: last exit code 1, 4 restarts',
            exitCode: 1,
            oomKilled: false,
            restartCount: 4,
            ports: 'none',
            keptAs: 'northwind-backend-admin-failed-1791480000',
            hint: 'northwind-backend-admin could not resolve `mqtt` (EAI_AGAIN) — is mqtt running/healthy? add it to Depends on',
            logTail: [
              'Task start deno run -A --unstable-kv main.ts',
              '[1] listening on http://0.0.0.0:3000 (bracketed text stays text)',
              '\x1b[0m\x1b[1m\x1b[31merror\x1b[0m: Uncaught (in promise) Error: getaddrinfo EAI_AGAIN mqtt',
              '    \x1b[0m\x1b[2m\x1b[38;5;245mat \x1b[0mnode:dns:\x1b[33m118\x1b[0m:\x1b[33m26\x1b[0m',
              '    \x1b[0m\x1b[2m\x1b[38;5;245mat \x1b[0mconnectMqtt (\x1b[36mfile:///app/helper/mqtt.ts\x1b[0m:\x1b[33m12\x1b[0m:\x1b[33m9\x1b[0m)',
              '\x1b]0;deno\x07\x1b[2Kexited with code 1',
            ],
          },
          {
            // a tail whose ESC bytes were lost on the way (bare "[0m[1m[31m…"): detected per stream, "[1]" stays text
            containerSlug: 'northwind-backend-worker',
            error: 'northwind-backend-worker: not started: northwind-backend-admin failed',
            exitCode: 1,
            oomKilled: false,
            restartCount: 0,
            ports: 'none',
            keptAs: null,
            logTail: ['[1] worker booting', '[0m[1m[31merror[0m: connect ECONNREFUSED northwind-backend-admin:3000', '    [0m[2mat [0mmain.ts:4:1'],
          },
        ],
      });
  }
  d.activities[depId] = A;
  d.transfers[depId] = [
    { transferId: 'TrfDbDump00000000001', deploymentId: depId, siteSlug: 'db', direction: 'to-cloud', fileName: 'redis-dump-2026-10-05.rdb', sizeBytes: 182_331, sha256: fakeSha(), storagePath: `transfers/${depId}/TrfDbDump00000000001`, uploadId: null, createdAt: ago(DAY), createdBy: 'ops@example.com', note: 'nightly dump for analysis', expiresAt: null, status: 'ready', savedPath: null, error: null },
    { transferId: 'TrfAppCfg00000000002', deploymentId: depId, siteSlug: 'app', direction: 'to-site', fileName: 'license.txt', sizeBytes: 1_204, sha256: fakeSha(), storagePath: `transfers/${depId}/TrfAppCfg00000000002`, uploadId: null, createdAt: ago(5 * HOUR), createdBy: ADMIN, note: 'new license', expiresAt: null, status: 'saved-on-server', savedPath: '/opt/acme/license.txt', error: null, fetchedAt: ago(5 * HOUR - 60_000) },
    // §21 #100 store-and-forward states
    { transferId: 'TrfAppWait00000000003', deploymentId: depId, siteSlug: 'app', direction: 'to-site', fileName: 'firmware-2.4.1.bin', sizeBytes: 48_234_112, sha256: fakeSha(), storagePath: `transfers/${depId}/TrfAppWait00000000003`, uploadId: null, createdAt: ago(40 * 60_000), createdBy: ADMIN, note: 'for the gateway', expiresAt: null, status: 'ready', savedPath: null, error: null, fetchedAt: null },
    { transferId: 'TrfAppSite00000000004', deploymentId: depId, siteSlug: 'app', direction: 'to-site', fileName: 'calibration.csv', sizeBytes: 9_812, sha256: fakeSha(), storagePath: `transfers/${depId}/TrfAppSite00000000004`, uploadId: null, createdAt: ago(3 * HOUR), createdBy: ADMIN, note: '', expiresAt: null, status: 'ready', savedPath: null, error: null, fetchedAt: ago(2 * HOUR) },
    { transferId: 'TrfAppLogs00000000005', deploymentId: depId, siteSlug: 'app', direction: 'to-cloud', fileName: 'app-2026-10-08.log', sizeBytes: 2_404_112, sha256: fakeSha(), storagePath: `transfers/${depId}/TrfAppLogs00000000005`, uploadId: null, createdAt: ago(10 * 60_000), createdBy: 'ops@example.com', note: 'error at 09:12', expiresAt: null, status: 'uploading', savedPath: null, error: null },
    { transferId: 'TrfAppDump00000000006', deploymentId: depId, siteSlug: 'app', direction: 'to-cloud', fileName: 'pg_dump.sql.gz', sizeBytes: 81_220_331, sha256: fakeSha(), storagePath: `transfers/${depId}/TrfAppDump00000000006`, uploadId: null, createdAt: ago(DAY), createdBy: 'ops@example.com', note: '', expiresAt: null, status: 'ready', savedPath: null, error: null },
  ];

  // §21 #123 shared files: one in the cloud (+ a copy on app), one only on db (published from a server path), one on two
  // sites, one nobody holds any more, one unpublished (tombstone)
  const sf = (o: Partial<SharedFile> & Pick<SharedFile, 'fileId' | 'fileName' | 'sizeBytes' | 'publishedAt' | 'origin'>): SharedFile => ({ sha256: fakeSha(), note: null, publishedBy: SEED_USERS.admin.email, state: 'ready', unpublishedAt: null, unpublishedBy: null, cloud: { stored: false, fetch: null }, sites: [], ...o });
  d.files[depId] = [
    sf({ fileId: '01JF000000000000000000000A', fileName: 'license-2027.lic', sizeBytes: 1_204, publishedAt: ago(5 * HOUR), origin: { kind: 'cloud' }, note: 'new license for every site', cloud: { stored: true, fetch: null }, sites: ['app'] }),
    sf({ fileId: '01JF000000000000000000000B', fileName: 'pg_dump.sql.gz', sizeBytes: 81_220_331, publishedAt: ago(DAY), publishedBy: 'ops@example.com', origin: { kind: 'site', siteSlug: 'db', via: 'path', sourcePath: '/var/backups/pg_dump.sql.gz' }, note: 'nightly dump for analysis', sites: ['db'] }),
    sf({ fileId: '01JF000000000000000000000C', fileName: 'firmware-2.4.1.bin', sizeBytes: 48_234_112, publishedAt: ago(40 * MIN), publishedBy: 'ops@example.com', origin: { kind: 'site', siteSlug: 'app', via: 'browser', sourcePath: null }, note: 'for the gateway', sites: ['app', 'db'] }),
    sf({ fileId: '01JF000000000000000000000D', fileName: 'gateway-notes.txt', sizeBytes: 3_812, publishedAt: ago(2 * DAY), origin: { kind: 'site', siteSlug: 'lab', via: 'browser', sourcePath: null } }),
    sf({ fileId: '01JF000000000000000000000E', fileName: 'calibration-old.csv', sizeBytes: 9_812, publishedAt: ago(3 * DAY), origin: { kind: 'cloud' }, state: 'unpublished', unpublishedAt: ago(2 * DAY), unpublishedBy: SEED_USERS.admin.email }),
  ];

  // a second deployment: empty, no head, not installed
  const dep2 = 'DepNorthGrid00000002';
  // §21 #93: site main runs mqtt + northwind-backend-admin (env MQTT_URL=mqtt://mqtt:1883, NO depends → suggested) +
  // northwind-backend-worker (ADMIN_URL uses containers.northwind-backend-admin.* → implicit depend)
  const eaImg = d.images['northwind-admin']!['v1']!;
  const eaCat = { kind: eaImg.kind, ports: eaImg.ports, env: eaImg.env, volumes: eaImg.volumes, healthcheck: eaImg.healthcheck };
  const ngAdmin = defaultContainer('northwind-admin', 'v1', eaCat, 'northwind-backend-admin');
  ngAdmin.env = [{ name: 'MQTT_URL', value: 'mqtt://mqtt:1883', secretRef: null }];
  const ngWorker = defaultContainer('northwind-admin', 'v1', eaCat, 'northwind-backend-worker');
  ngWorker.env = [{ name: 'ADMIN_URL', value: 'http://{{ containers.northwind-backend-admin.host }}:{{ containers.northwind-backend-admin.port.http }}', secretRef: null }];
  const ngMqtt = defaultContainer('redis', 'v1', { kind: redisImg.kind, ports: redisImg.ports, env: redisImg.env, volumes: redisImg.volumes, healthcheck: redisImg.healthcheck }, 'mqtt');
  ngMqtt.ports = ngMqtt.ports.map((p) => ({ ...p, edge: false }));
  ngMqtt.volumes = redisImg.volumes.map((v) => ({ imageVolume: v.name, mountPath: null, source: { kind: 'none' as const }, readOnly: false }));
  d.deployments[dep2] = {
    ...audit(ADMIN, ago(DAY)),
    deploymentId: dep2,
    slug: 'north-grid',
    label: 'North grid',
    image: null,
    tagSlugs: ['gov'],
    managerAccountUids: [OPS],
    srcTemplate: null,
    draft: { variables: [], secrets: [], files: [], sites: [{ siteSlug: 'main', label: 'Main site', config: { host: null, agentPort: 7080, agentGuiAliasPort: 8080, volumes: [], containers: [ngAdmin, ngWorker, ngMqtt], depends: [], notFoundPageId: null, edge: { httpPort: 80, httpsPort: 443, maxBodyBytes: null, hosts: [newEdgeHost('*')], routes: [] } } }] },
    draftVersion: 1,
    head: null,
    accessKeyHash: '$argon2id$mock',
    accessKey: genAccessKey(),
    accessKeyVersion: 1,
    accessKeyRotatedAt: ago(DAY),
    accessKeyRotatedBy: ADMIN,
    agentEpoch: 1,
    cloudActivitySeq: 1,
    deleted: false,
  };
  d.revisions[dep2] = [];
  d.proposals[dep2] = [];
  d.agents[dep2] = {};
  // an active install command made before §21 #84: its token is not stored ("not stored — regenerate to view")
  d.installTokens[dep2] = [{ tokenHash: randHex(64), siteSlug: 'main', token: null, expiresAt: new Date(Date.now() + 20 * HOUR).toISOString(), usedAt: null, revokedAt: null, createdAt: ago(4 * HOUR), createdBy: ADMIN }];
  d.activities[dep2] = [act(dep2, '_cloud', 1, ago(DAY), ADMIN, 'cloud', 'deployment.created', 'Deployment North grid created (empty)', { label: 'North grid' }, null)];
  d.transfers[dep2] = [];
  d.files[dep2] = [];

  const ae = (e: Partial<AuditLogEntry> & Pick<AuditLogEntry, 'ts' | 'op' | 'action' | 'targetType' | 'target'>): AuditLogEntry => {
    const sys = e.actor === 'system';
    return {
      auditId: autoId(),
      actor: ADMIN,
      actorEmail: sys ? null : SEED_USERS.admin.email,
      outcome: 'ok',
      status: 200,
      errorCode: null,
      errorMessage: null,
      requestId: sys ? null : crypto.randomUUID(),
      ip: sys ? null : '127.0.0.1',
      userAgent: sys ? null : 'Mozilla/5.0 (seed)',
      changes: [],
      changesTotal: e.changes?.length ?? 0,
      data: {},
      ...e,
    };
  };
  const webId = 'hello-web';
  d.auditLog = [
    ae({ ts: ago(5 * DAY), op: 'uploadIcon', action: 'icon.uploaded', targetType: 'icon', target: `icons/${iconId}`, status: 201, changes: [{ path: 'name', before: null, after: 'acme-logo' }, { path: 'contentType', before: null, after: 'image/svg+xml' }], data: { existing: false, name: 'acme-logo', contentType: 'image/svg+xml', sizeBytes: svgBytes.length } }),
    ae({ ts: ago(3 * HOUR), op: 'revealRegistryPassword', action: 'registry.password-revealed', targetType: 'registry', target: `registries/${regAr}` }),
    ae({ ts: ago(20 * HOUR), op: 'system:daily', actor: 'system', action: 'system.daily-cleanup', targetType: 'system', target: 'system/daily', data: { expiredUploads: 2, expiredTransfers: 1, deletedBlobs: 0 } }),
    ae({ ts: ago(DAY), op: 'system:importImage', actor: 'system', action: 'image.import-failed', targetType: 'image', target: `services/${webId}/images/v2`, outcome: 'error', status: 500, errorCode: 'internal', errorMessage: 'manifest unknown: registry.example.com/opm-test/hello-web:1.1.0-rc1 not found in the registry', changes: [{ path: 'import.status', before: 'copying', after: 'failed' }, { path: 'import.error', before: null, after: 'manifest unknown: registry.example.com/opm-test/hello-web:1.1.0-rc1 not found in the registry' }], data: { status: 'failed' } }),
    ae({ ts: ago(DAY + HOUR), op: 'registerImage', action: 'image.registered', targetType: 'image', target: `services/${webId}/images/v2`, status: 201, changes: [{ path: 'version', before: null, after: 'v2' }, { path: 'reference', before: null, after: 'registry.example.com/opm-test/hello-web:1.1.0-rc1' }, { path: 'registryId', before: null, after: regAr }, { path: 'kind', before: null, after: 'service' }] }),
    ae({ ts: ago(2 * DAY), op: 'createTag', actor: VIEWER, actorEmail: SEED_USERS.viewer.email, action: 'tag.created', targetType: 'tag', target: 'tags/lab', outcome: 'error', status: 403, errorCode: 'forbidden', errorMessage: 'you need tags.write' }),
    ae({ ts: ago(2 * DAY + HOUR), op: 'patchTag', action: 'tag.updated', targetType: 'tag', target: 'tags/pilot', changes: [{ path: 'color', before: '#f59e0b', after: '#d97706' }, { path: 'label', before: 'Pilots', after: 'Pilot' }] }),
    ae({ ts: ago(4 * DAY), op: 'patchTemplate', action: 'template.updated', targetType: 'template', target: `templates/${tplId}`, changes: [{ path: 'description', before: 'Counter API + web page + redis.', after: 'Counter API + web page + redis on two sites (app, db).' }, { path: 'secrets[api-token].value', before: '***', after: '***', secret: true }, { path: 'version', before: 2, after: 3 }] }),
    ae({ ts: ago(8 * DAY), op: 'createAccount', action: 'account.created', targetType: 'account', target: `accounts/${OPS}`, status: 201, changes: [{ path: 'email', before: null, after: SEED_USERS.ops.email }, { path: 'name', before: null, after: 'Omar Ops' }], data: { email: SEED_USERS.ops.email } }),
    ae({ ts: ago(10 * DAY - HOUR), op: 'patchAccount', action: 'account.access-changed', targetType: 'account', target: `accounts/${VIEWER}`, changes: [{ path: 'access[0]', before: null, after: { resource: 'deployments', actions: ['read'], scope: { kind: 'all' } } }] }),
    ae({ ts: ago(10 * DAY), op: 'createAccount', action: 'account.created', targetType: 'account', target: `accounts/${VIEWER}`, status: 201, changes: [{ path: 'email', before: null, after: SEED_USERS.viewer.email }, { path: 'name', before: null, after: 'Vik Viewer' }], data: { email: SEED_USERS.viewer.email } }),
  ];
  // §21 #83: every image volume has an explicit kind (hello-catalog fixtures have none: inferred from the path)
  for (const tags of Object.values(d.images)) for (const i of Object.values(tags)) i.volumes = i.volumes.map(normalizeImageVolume);
  return d;
}
