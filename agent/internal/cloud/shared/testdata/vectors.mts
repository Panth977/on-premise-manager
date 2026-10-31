/** Prints inputs + TS outputs of every ported function (shared_test.go). Run with tsx from backend/. */
import { readFileSync } from 'node:fs';
import * as V from '../../../../../shared/src/validate.ts';
import * as IE from '../../../../../shared/src/image-edit.ts';
import * as T from '../../../../../shared/src/types.ts';
import * as ST from '../../../../../shared/src/staging.ts';
import * as F from '../../../../../shared/src/files.ts';
import * as TR from '../../../../../shared/src/transfers.ts';
import * as VC from '../../../../../shared/src/volume-content.ts';
import { slugifyLabel, uniqueSlug } from '../../../../../shared/src/github-mirror.ts';
import { activityHashInput } from '../../../../../shared/src/canonical.ts';
import { auditActionMatches } from '../../../../../shared/src/audit.ts';
import { mutate } from './mutate.mts';

const load = (f: string) => JSON.parse(readFileSync(new URL(`../../../../../shared/testdata/${f}`, import.meta.url), 'utf8'));
const HELLO = load('hello-config.json');
const CAT = load('hello-catalog.json');
const VOLS = load('volume-content-vectors.json');
const HOSTFS = load('hostfs-vectors.json');
const UNDEF = { $undef: true };
const enc = (a: unknown[]) => a.map((x) => (x === undefined ? UNDEF : x));
const out: Record<string, unknown[]> = {};
const run = (name: string, fn: (...a: any[]) => unknown, ...args: unknown[]) => {
  const snapshot = enc(structuredClone(args));
  let r: { out?: unknown; throw?: string };
  try {
    const v = fn(...args);
    r = { out: v === undefined ? null : v };
  } catch (e) {
    r = { throw: (e as Error).message };
  }
  (out[name] ??= []).push({ args: snapshot, ...r });
};
const iss = (f: (is: V.Issues) => void, mode: V.ValidateMode = 'publish') => {
  const is = new V.Issues(mode);
  f(is);
  return is.list;
};

// ── values
const vals: Record<string, unknown> = {
  MASK: T.MASK, REVEAL_SECONDS: T.REVEAL_SECONDS, REVEAL_RATE_PER_MINUTE: T.REVEAL_RATE_PER_MINUTE, SEED_ARCHIVE_MAX: T.SEED_ARCHIVE_MAX, GUI_UPGRADE_MIN_VERSION: T.GUI_UPGRADE_MIN_VERSION,
  AGENT_UPGRADE_HEALTH_SEC: T.AGENT_UPGRADE_HEALTH_SEC, AGENT_UPGRADE_REQUEST_TTL_SEC: T.AGENT_UPGRADE_REQUEST_TTL_SEC, UPLOAD_PART_SIZE: T.UPLOAD_PART_SIZE, SMALL_BLOB_MAX: T.SMALL_BLOB_MAX,
  BLOB_STREAM_MAX: T.BLOB_STREAM_MAX, TRANSFER_MAX: T.TRANSFER_MAX, INTENT_MAX_SPEED: T.INTENT_MAX_SPEED, INTENT_SPEED_HEARTBEAT_MS: T.INTENT_SPEED_HEARTBEAT_MS, INTENT_SPEED_DECAY_MS: T.INTENT_SPEED_DECAY_MS,
  INTENT_SPEED_X4_AFTER_MS: T.INTENT_SPEED_X4_AFTER_MS, INTENT_SPEED_EASE_MS: T.INTENT_SPEED_EASE_MS, ICON_MAX_BYTES: T.ICON_MAX_BYTES, CLOCK_SKEW_WARN_SEC: T.CLOCK_SKEW_WARN_SEC, AGENT_LEGACY_SCHEMA: T.AGENT_LEGACY_SCHEMA,
  VOLUME_COPIES_DIR: T.VOLUME_COPIES_DIR, VOLUME_OP_RATE_PER_MINUTE: T.VOLUME_OP_RATE_PER_MINUTE, VOLUME_RESTORE_UPLOAD_MAX: T.VOLUME_RESTORE_UPLOAD_MAX, HOST_FS_MAX_ENTRIES: T.HOST_FS_MAX_ENTRIES,
  VOLUME_BACKUP_SCHEDULES: T.VOLUME_BACKUP_SCHEDULES, DEFAULT_VOLUME_BACKUP_POLICY: T.DEFAULT_VOLUME_BACKUP_POLICY, PROPOSAL_TERMINAL: T.PROPOSAL_TERMINAL, INSTALL_TOKEN_KINDS: T.INSTALL_TOKEN_KINDS, INTENT_SPEEDS: T.INTENT_SPEEDS,
  ICON_CONTENT_TYPES: T.ICON_CONTENT_TYPES, AUDIT_TARGET_TYPES: T.AUDIT_TARGET_TYPES, AUDIT_ACTIONS: T.AUDIT_ACTIONS, AUDIT_DEPLOYMENT_OPS: T.AUDIT_DEPLOYMENT_OPS, CREDENTIAL_OPS: T.CREDENTIAL_OPS, AUDIT_EXEMPT_OPS: T.AUDIT_EXEMPT_OPS,
  ACTIVITY_TYPES: T.ACTIVITY_TYPES, AUDIT_OPS: T.AUDIT_OPS, ERROR_HTTP_STATUS: T.ERROR_HTTP_STATUS,
  STAGE_TTL_MS: ST.STAGE_TTL_MS, STAGE_MAX_BLOBS: ST.STAGE_MAX_BLOBS, STAGE_MAX_BYTES: ST.STAGE_MAX_BYTES, STAGING_PREFIX: ST.STAGING_PREFIX,
  FILE_MAX: F.FILE_MAX, FILE_NOTE_MAX: F.FILE_NOTE_MAX, FILE_TOMBSTONE_DAYS: F.FILE_TOMBSTONE_DAYS, HOST_FS_PSEUDO: TR.HOST_FS_PSEUDO, HOST_FS_SECRETS: TR.HOST_FS_SECRETS,
  VOLUME_CONTENT_LIMITS: VC.VOLUME_CONTENT_LIMITS, VOLUME_SEED_MARKER: VC.VOLUME_SEED_MARKER, VOLUME_CONTENT_MIN_AGENT: VC.VOLUME_CONTENT_MIN_AGENT, VOLUME_CONTENT_COMMIT_MIN_AGENT: VC.VOLUME_CONTENT_COMMIT_MIN_AGENT,
  VOLUME_SYNC_SCAN_SEC: VC.VOLUME_SYNC_SCAN_SEC, VOLUME_SYNC_QUIET_SEC: VC.VOLUME_SYNC_QUIET_SEC, VOLUME_SYNC_MIN_INTERVAL_SEC: VC.VOLUME_SYNC_MIN_INTERVAL_SEC, VOLUME_CONTENT_APPLY_WAIT_SEC: VC.VOLUME_CONTENT_APPLY_WAIT_SEC,
  DEFAULT_FILE_MODE: VC.DEFAULT_FILE_MODE, VOLUME_ALWAYS_EXCLUDED: VC.VOLUME_ALWAYS_EXCLUDED, VOLUME_PERSIST_DEFAULT_EXCLUDE: VC.VOLUME_PERSIST_DEFAULT_EXCLUDE, VOLUME_SYNC_STATE_LABEL: VC.VOLUME_SYNC_STATE_LABEL,
  EMPTY_VOLUME_TREE: VC.EMPTY_VOLUME_TREE, IMAGE_IDENTITY_FIELDS: IE.IMAGE_IDENTITY_FIELDS, IMAGE_EDITABLE_FIELDS: IE.IMAGE_EDITABLE_FIELDS, IMAGE_NAME_KINDS: IE.IMAGE_NAME_KINDS,
  LIMITS: V.LIMITS, RESERVED_SITE_SLUGS: V.RESERVED_SITE_SLUGS, RESERVED_CONTAINER_SLUGS: V.RESERVED_CONTAINER_SLUGS, EDGE_METHODS: V.EDGE_METHODS,
};

// ── regex differential: every RE on tricky strings
const RE_SAMPLES = ['a@b.c', 'a b@c.d', 'a b@c.d', 'a @b.c', 'a﻿@b.c', 'https://x', 'https://a b', 'https://a　', 'https://a/b', 'https://a?b', 'upload:aaaaaaaaaaaaaaaaaaaa', 'lucide:x-1', 'abc', 'A', 'Exampleſ.com', 'exampleK.com', 'EXAMPLE.com', '*.a.b', 'a..b', '-a', '10.0.0.1', '256.0.0.1', '01.1.1.1', '+15551234567', '+٣٤٥٦٧٨٩٠', 'x\n', 'abc\n', 'a@b.c\n', '#aabbcc', 'sha256:' + 'a'.repeat(64), 'sha256:' + 'a'.repeat(64) + '\n', '01ARZ3NDEKTSV4RRFFQ69G5FAV', 'a/b_c.d-e', 'text/plain', 'X-Header', 'install.enrolled', 'a-b.c-d', 'ﬁ.com', 'v1', 'V1', '0.1-x', 'Ab_C1', 'a'.repeat(40), 'a'.repeat(41), 'route-1'];
const reVals: Record<string, Record<string, boolean>> = {};
for (const [k, re] of Object.entries(V.RE)) reVals[k] = Object.fromEntries(RE_SAMPLES.map((s) => [s, (re as RegExp).test(s)]));

// ── normalizers
const images = { 'hello-web:v1': { volumes: [{ name: 'data', mountPath: '/data' }, { name: 'cfg', mountPath: '/etc/app/' }] } };
const legacyContainer = { serviceSlug: 'hello-web', version: 'v1', z_unknown: 1, port: [{ portName: 'http', hostPort: 8080, protocol: 'tcp' }, { portName: 'q' }], volume: [{ kind: 'data', name: 'data', mountPath: '/data' }, { kind: 'data', name: 'other', mountPath: '/o' }, { kind: 'file', fileSlug: 'f', mountPath: '/f' }, { kind: 'secret', secretSlug: 's', mountPath: '/s' }, { kind: 'zzz' }], depends: [{ serviceSlug: 'x' }, { containerSlug: 'y' }, {}], env: [{ name: 'A' }, { name: 'B', value: 'b', secretRef: 's', extra: 1 }], resources: { cpus: 1 } };
const roundC = { containerSlug: 'c1', serviceSlug: 'hello-web', version: 'v1', enabled: false, ports: [{ portName: 'p', hostPort: '{{ var.x }}', hostIp: '127.0.0.1', edge: false, extra: 1 }], volumes: [{ imageVolume: 'data', source: { kind: 'data', name: 'old' }, readOnly: true }, { imageVolume: null, mountPath: '/x', source: { kind: 'file', fileSlug: 'f' }, readOnly: false }, { imageVolume: 'cfg', source: null }, { imageVolume: 'v', source: { kind: 'volume', volumeSlug: 'd' }, mountPath: '/m', readOnly: true, zz: 1 }, { source: 'str' }, { source: { kind: 'secret', secretSlug: 's' } }, { source: { kind: 'host', path: '/h' }, readOnly: null }], command: ['a'], entrypoint: [], restart: 'never', healthcheck: { type: 'tcp' }, depends: [] };
const edges: unknown[] = [
  {}, { httpPort: null, httpsPort: null, maxBodyBytes: 5, hosts: [{ hostname: 'a', tls: { mode: 'none' }, unknown: 1 }, { responseHeaders: { set: { a: 'b' }, zz: 1 } }], routes: [] },
  { routes: [{ routeId: 'r', match: { pathPrefix: '/a', extra: 1, kind: undefined }, action: { type: 'proxy', serviceSlug: 's', timeoutSec: 30, custom: 1 } }] },
  { routes: [{ routeId: 'r', hosts: ['a'], match: { kind: 'exact', path: '/x', stopRegex: true, methods: [] }, action: { type: 'proxy', containerSlug: 'c', portName: 'p', websocket: false, readTimeoutSec: 5, timeoutSec: 9, rewrite: { regex: 'a' }, connectTimeoutSec: 1, requestBuffering: true, z: 2 }, order: 2, responseHeaders: { set: {} }, unk: 1 }] },
  { routes: [{ routeId: 'r', match: { kind: 'prefix', path: '/x', stopRegex: true, methods: ['GET'] }, action: { type: 'static-response', status: 200 }, responseHeaders: null }] },
  { routes: [{ z: 1, action: null, match: null }, { action: 'x', match: 'ab' }, { match: { methods: 'GET' } }, { match: { methods: 3 } }] },
  { routes: [null] }, { hosts: [null] }, { hosts: 'x' }, { routes: 3 }, { routes: {} }, { hosts: ['ab'] }, { routes: ['x'] },
];
const devtools: unknown[] = [
  undefined, null, [], 'x', 5, {}, [1, null, 'x', {}, { kind: 'nope' }, { kind: 'http' }],
  [{ kind: 'postgres', presetSlug: 'p', name: 'n', extra: 1, target: { kind: 'container', containerSlug: 'c', portName: 'p', zz: 1 }, auth: { password: { kind: 'expr', expr: '{{ secret.x }}', z: 1 }, token: { kind: 'literal', value: 'v', z: 1 }, junk: 1 }, options: { tls: true, defaultHeaders: [{ name: 'a', value: 'b', z: 1 }, 'x'], junk: 2 }, readOnly: true }],
  [{ kind: 'redis' }, { kind: 'sqlite', target: { kind: 'volume-file', volumeSlug: 'v', path: 'a.db' } }, { kind: 'mqtt', auth: null, options: null, target: null }, { kind: 'cassandra', auth: 'ab', options: [1], target: 'x' }],
  [{ kind: 'http', target: { kind: 'url', url: 'u' } }, { kind: 'http', target: { kind: 'host', host: 'h', port: 1, z: 1 } }, { kind: 'http', target: { kind: 'container-file', containerSlug: 'c', path: '/p' } }, { kind: 'http', target: { kind: 'other', a: 1 } }, { kind: 'http', target: { kind: 5 } }],
  [{ kind: 'http', target: { kind: 'constructor' } }], [{ kind: 'http', auth: { password: 'x', token: 5, httpScheme: null }, options: { defaultHeaders: 'x' } }], [{ kind: 'http', auth: { password: { kind: 'x', z: 1 }, token: null } }],
];
const vols: unknown[] = [
  { volumeSlug: 'a' }, { volumeSlug: 'a', label: 'L', description: 'd', backup: false, location: { kind: 'path', path: './x', z: 1 }, kind: 'file', seed: { kind: 'single', sha256: 's' }, zz: 1 },
  { volumeSlug: 'a', seed: { kind: 'tree', entries: [{ path: 'b/c', sha256: 's', size: 1 }, { path: 'a', sha256: 's', size: 1, mode: '0755', render: 'expressions', z: 1 }, { path: 'a' }, null, 5, { path: 3 }], dirs: ['b', 'd/e', 'd', '', 3, 'a', 'x', 'x'], other: 1 } },
  { volumeSlug: 'a', seed: { kind: 'tree' } }, { volumeSlug: 'a', seed: { kind: 'tree', entries: 3 } }, { volumeSlug: 'a', seed: { kind: 'tree', entries: 'ab' } }, { volumeSlug: 'a', seed: { kind: 'tree', dirs: 'ab' } }, { volumeSlug: 'a', seed: { kind: 'tree', dirs: 3 } },
  { volumeSlug: 'a', persist: {} }, { volumeSlug: 'a', persist: true }, { volumeSlug: 'a', persist: { exclude: 'x', maxBytes: 0, auto: false } }, { volumeSlug: 'a', persist: { exclude: ['x'], maxBytes: null, auto: null, z: 1 } }, { volumeSlug: 'a', persist: null }, { volumeSlug: 'a', seed: 5 }, { volumeSlug: 'a', seed: null, location: 'x' }, null, 'x', 5,
];
const siteCfgs: unknown[] = [
  {}, { volumes: [] }, { host: '  ' }, { host: '10.0.0.1', agentPort: null, agentGuiAliasPort: null }, { host: 5, agentGuiAliasPort: 9000, depends: [{ siteSlug: 'x' }], notFoundPageId: 'n', extra: 1 },
  { containers: [legacyContainer] }, { volumes: [], containers: [legacyContainer] }, { containers: [roundC, roundC] }, { volumes: [{ volumeSlug: 'old' }], containers: [roundC] }, { volumes: 'x', containers: [{ serviceSlug: 'hello-web', version: 'v1', ports: [], volumes: [] }] },
  { containers: [{ containerSlug: 'c2', serviceSlug: 'hello-web', version: 'v1', ports: [], volumes: [{ imageVolume: 'x', source: { kind: 'volume' } }] }] }, { containers: [{ containerSlug: 'c2', serviceSlug: 'hello-web', version: 'v1', ports: [], volumes: [{ imageVolume: 'x', source: { kind: 'volume', volumeSlug: 3 } }, { source: { kind: 'volume', volumeSlug: 3 } }] }] },
  { edge: {} }, { edge: 5 }, { edge: 'ab' }, { edge: null, devTools: [{ kind: 'http' }] }, { devTools: 'x' }, { containers: [null] }, { containers: 3 }, { containers: 'ab' }, { containers: [5] }, { containers: ['x'] }, { depends: 5, containers: [{ ports: 3, volumes: 3, port: 3 }] }, { containers: [{ volume: 3 }] }, { containers: [{ env: 3 }] }, { containers: [{ depends: 3 }] }, { containers: [{ depends: [null] }] }, { containers: [{ env: [null] }] }, { containers: [{ ports: [null] }] }, { containers: [{ volumes: [null] }] }, { containers: [{ port: [null] }] }, { containers: [{ volume: [null] }] }, { containers: [{ resources: 'x', serviceSlug: 'a' }] }, null, 'x', 5, [], undefined,
];
const noHello = structuredClone(HELLO.config);
for (const s of noHello.sites) delete s.config.volumes;
const configs: unknown[] = [
  HELLO.config, noHello, { files: [], sites: [], variables: [{ variableSlug: 'a' }], zz: 1 }, { files: [{ fileSlug: 'f', editor: 'binary', render: 'expressions', location: { kind: 'path', path: './a', z: 1 }, z: 1 }, { editor: 'text' }, { render: 'none', location: null }, 'ab', 5, []], sites: [] },
  { files: [], sites: [{ siteSlug: 'a', z: 1, config: {} }, { config: {}, siteSlug: 'b' }] }, { files: [null], sites: [] }, { files: [], sites: [null] }, { files: [], sites: [{}] }, { files: 3, sites: [] }, { files: [], sites: 3 }, { sites: [] }, { files: [] }, null, 5, 'x', [], {},
];
for (const c of configs) (run('normalizeConfig', V.normalizeConfig, c), run('normalizeConfig', V.normalizeConfig, c, images));
for (let s = 1; s <= 400; s++) {
  const base = s % 2 ? HELLO.config : noHello;
  run('normalizeConfig', V.normalizeConfig, mutate(base, s, s % 3 ? 'any' : 'typed'), s % 5 ? CAT.images : undefined);
}
for (const c of siteCfgs) (run('normalizeSiteConfig', V.normalizeSiteConfig, c), run('normalizeSiteConfig', V.normalizeSiteConfig, c, images));
for (const v of vols) run('normalizeSiteVolume', V.normalizeSiteVolume, v);
for (const e of edges) run('normalizeEdge', V.normalizeEdge, e);
for (const d of devtools) run('normalizeSiteDevTools', V.normalizeSiteDevTools, d);
for (const c of [legacyContainer, roundC, {}, null]) for (const img of [null, images['hello-web:v1'], undefined]) for (const o of [undefined, { implicitVolumes: true }]) run('normalizeContainer', V.normalizeContainer, c, img, o);
run('normalizeContainer', V.normalizeContainer, { ...legacyContainer }, { volumes: 3 }, undefined);
run('normalizeContainer', V.normalizeContainer, { ...legacyContainer }, {}, undefined);
for (const l of [null, undefined, 5, 'x', [], {}, { kind: 'auto', path: 'x' }, { kind: 'path' }, { kind: 'path', path: './x', z: 1 }]) run('normalizeLocation', V.normalizeLocation, l);
for (const e of [{ name: 'A', required: true, secret: false }, { name: 'A', value: ['a', 'b', 3], z: 1 }, { value: [] }, { value: 3, options: ['a'] }, { value: null, description: 'd', options: null }, { value: ['x'], options: ['y'] }, null, 'x', 5, []]) run('normalizeImageEnv', V.normalizeImageEnv, e);
for (const v of [{ name: 'a', mountPath: '/data/flows.json' }, { name: 'a', mountPath: '/data' }, { name: 'a', mountPath: '/data/', kind: 'file', description: 'd', z: 1 }, { mountPath: 3, kind: null }, { name: 'a', mountPath: '/app/{{ container.env.X }}.ts' }, null, 5]) run('normalizeImageVolume', V.normalizeImageVolume, v);
for (const p of ['/a.json', '/a.d', '/conf.d', '/a/b.c1', '/a.1', '/.config', '/opt/app-1.2', '/a/', 3, null, '/a.abcdefghijk', '/{{ x\ny }}.ts', '/x\n.ts', '/x.ts\n', '/a.D', '.ts', '/.ts', '/a..ts']) run('inferImageVolumeKind', V.inferImageVolumeKind, p);
run('emptyConfig', V.emptyConfig);

// ── image references
for (const r of [undefined, null, 3, '', '   ', 'redis', 'redis:7.4', 'library/redis', 'docker.io/redis', 'index.docker.io/redis:1', 'registry-1.docker.io/a/b:c', 'ghcr.io/acme/api:1.2.0', 'localhost/a', 'localhost:5000/a:b', 'HTTPS://ghcr.io/a/b', 'docker://x/y', 'a b', ' redis ', 'a b', 'Redis', 'a:b:c', 'a@sha256:' + 'a'.repeat(64), 'a:1@sha256:' + 'a'.repeat(64), 'a@sha256:xx', 'a:-x', 'a:', 'ghcr.io/', 'GHCR.IO/a/b', 'x'.repeat(513), '/a', 'a//b', 'İ.x/a/b', 'ſ://a', 'a/b:c/d', '@sha256:' + 'b'.repeat(64)]) run('parseImageReference', V.parseImageReference, r);
for (const p of [{ host: 'h', repository: 'r', tag: 't', digest: null }, { host: 'h', repository: 'r', tag: null, digest: 'd' }, { host: 'h', repository: 'r', tag: '', digest: '' }]) run('formatImageReference', V.formatImageReference, p);
for (const u of ['https://registry-1.docker.io', 'HTTP://Ghcr.io/x/y', 'ghcr.io', 'index.docker.io/v2/', 'a/b\nc', 'a/b\nc/d', '']) run('registryHost', V.registryHost, u);
const regs = ['b', 'a', 'B', 'A', 'é', 'e', 'f', '_x', '-y', 'a b', 'ab', 'a-b', 'a_b', '1', '10', '2', 'Z', 'z', 'ß', 'ss'].map((label, i) => ({ registryId: `r${i}`, url: i % 3 ? 'https://ghcr.io/' : 'https://docker.io', label }));
run('matchRegistries', V.matchRegistries, 'ghcr.io', regs);
run('matchRegistries', V.matchRegistries, 'docker.io', regs);
run('matchRegistries', V.matchRegistries, 'none', regs);
for (const [l, t] of [['v3', []], ['3.1.0', []], ['rc-9', []], ['abc', []], ['', []], [null, []], ['v1', ['v2', 'v3']], ['v9', ['v10']], ['1x', []], ['a\nb1', []], ['99999999999999999999', []]] as const) run('nextVersion', V.nextVersion, l, t);
for (const v of [null, undefined, 'lucide:x', 'x-1', 'upload:' + 'a'.repeat(20), 'upload:x', 'Bad', 5, 'https://x']) {
  run('checkIcon', (x) => iss((is) => V.checkIcon(x, 'p', is)), v);
  run('checkPicture', (x) => iss((is) => V.checkPicture(x, 'p', is)), v);
  run('iconUploadId', V.iconUploadId, v);
}
for (const v of ['https://a b', 'https://x/y']) run('checkPicture', (x) => iss((is) => V.checkPicture(x, 'p', is)), v);

// ── image specs
const okSpec = { kind: 'service', entrypoint: null, command: ['a'], workdir: '/w', user: 'a:b', ports: [{ name: 'http', port: 80, protocol: 'tcp', description: 'd' }], env: [{ name: 'A', required: true, secret: false, description: '', options: null }], volumes: [{ name: 'data', mountPath: '/data', kind: 'dir', description: '' }], healthcheck: { type: 'http', portName: 'http', path: '/', expectStatus: 200, intervalSec: 1, timeoutSec: 1, retries: 1, startPeriodSec: 0 }, stopTimeoutSec: 10, description: 'x' };
const specs: unknown[] = [okSpec, {}, { ports: 3 }, { ports: [null] }, { ports: [5] }, { ports: [{}] }, { ports: ['x', 'x'] }, { ports: [{ name: 'a', port: '{{ var.x }}', protocol: 'udp' }, { name: 'a', port: 'x', protocol: 'z' }, { name: 'Bad', port: 0 }, { name: 'bb', port: 70000 }, { name: 'cc', port: 1.5 }, { name: 'dd', port: '{{ nope }}' }] },
  { env: 3 }, { env: [null] }, { env: [5] }, { env: [{}] }, { env: [{ name: 'A', required: 1, secret: 1, value: 3 }, { name: 'A', required: true, secret: true, value: 'x', options: ['a'] }, { name: 'b', required: true, secret: false, options: [] }, { name: 'C', required: true, secret: false, options: ['a', 'a', ''], value: 'z' }, { name: 'D', required: false, secret: false, options: 'x' }, { name: 'E', required: false, secret: false, options: [1] }, { name: 'F', required: false, secret: false, options: ['{{ x }}'] }, { name: 'G', required: false, secret: false, value: '{{ nope }}' }, { name: 'H', required: false, secret: false, value: ['a', 'b'] }, { name: 'I', required: false, secret: false, value: '😀'.repeat(2049) }, { name: 'J', required: false, secret: false, description: 3 }, { name: 'K', required: false, secret: false, description: 'x'.repeat(2001) }] },
  { volumes: 3 }, { volumes: [null] }, { volumes: [5] }, { volumes: [{ name: 'aa', mountPath: '/x/' }, { name: 'aa', mountPath: '/x/', kind: 'file' }, { name: 'b', mountPath: '/a b', kind: 'zzz' }, { name: 'cc', mountPath: 'rel{{ var.x }}' }, { name: 'dd', mountPath: '/{{ nope }}' }, { name: 'ee', mountPath: 3 }, { name: 'ff', mountPath: '/f.json', kind: 'dir' }, { name: 'gg', mountPath: '/g.json/', kind: 'file' }, { name: 'hh', mountPath: '/' + '{{ var.x }}'.repeat(100) }] },
  { healthcheck: 5 }, { healthcheck: 'x' }, { healthcheck: [] }, { healthcheck: { type: 'zz' } }, { healthcheck: { type: 'constructor' } }, { healthcheck: { type: 'constructor', q: 1 } }, { healthcheck: { type: ['http'] } }, { healthcheck: { type: 'tcp', z: 1, intervalSec: 0, timeoutSec: 601, retries: 1.5, startPeriodSec: 'x', portName: 'x' } }, { ports: [{ name: 'p1', port: 1, protocol: 'tcp' }, 'zz'], healthcheck: { type: 'tcp', portName: 'nope' } }, { healthcheck: { type: 'http', path: 'x', expectStatus: 99, command: [] } }, { healthcheck: { type: 'exec', command: [], expectExitCode: 256 } }, { healthcheck: { type: 'exec', command: [1] } }, { healthcheck: { type: 'exec', command: Array(51).fill('a') } }, { healthcheck: { type: 'log', pattern: '(' } }, { healthcheck: { type: 'log', pattern: 3 } }, { healthcheck: { type: 'log', pattern: '(?=a)' } }, { healthcheck: { type: 'log', pattern: 'a\\1' } }, { healthcheck: { type: 'log', pattern: 'ok' } },
  { kind: 'x' }, { kind: null }, { entrypoint: 'x' }, { command: [1] }, { command: Array(101).fill('a') }, { command: ['😀'.repeat(2049)] }, { workdir: 'rel' }, { workdir: 3 }, { workdir: null }, { user: 'a b' }, { user: 3 }, { user: 'a:b:c' }, { stopTimeoutSec: 0 }, { stopTimeoutSec: null }, { stopTimeoutSec: 1.5 }, { stopTimeoutSec: 601 }, { description: null }, { description: 3 }];
const REGEXES = ['(', ')', '[', ']', '{', '}', '*', '+', '?', 'a**', 'a*?', 'a{2,1}', 'a{1,2}', '{1}', 'a{1', 'x{', '\\', '[z-a]', '[a-z]', '[\\d-a]', '[a-\\d]', '[\\w-\\d]', '(?<n>a)', '(?<n>a)(?<n>b)', '(?<1>a)', '(?x)', '(?i)a', '(?P<n>a)', '(?:', '(?', '^*', '$*', '\\b*', '(a)*', '[]', '[^]', '[a', '\\k<n>', '(?<n>a)\\k<n>', '(?<n>a)\\k<m>', '(?<n>a)\\k', '\\c', '\\x4', '\\u00', '\\p{L}', '\\Z', '\\Q', 'a|*', '(|)', '(*)', '[\\c]', '[\\x41-\\x40]', '[\\u0041-\\u0040]', '[b-\\x41]', '[\\101-\\100]', '|', '||', '()', '(?:)', '.', 'a{0}', 'a{,5}', '😀', '[😀-😁]', '[😁-😀]', '(?<😀>a)'];
for (const s of REGEXES) run('checkRe2', (x) => iss((is) => V.checkRe2(x, 'p', is)), s);
run('checkRe2', (x) => iss((is) => V.checkRe2(x, 'p', is)), 3);
for (const s of specs) run('validateImageSpec', (b) => iss((is) => V.validateImageSpec(b, is)), s);
const regOk = { version: 'v1', reference: 'redis:7', registryId: null, platform: 'linux/amd64', mode: 'copy', kind: 'service', ...okSpec };
for (const b of [regOk, {}, { ...regOk, version: 'V 1', reference: 3, registryId: '', platform: 'x', mode: 'x', kind: 'x' }, { ...regOk, registryId: 3 }, { ...regOk, version: null }, { ...regOk, platform: null }, ...(specs as object[]).map((s) => ({ ...regOk, ...s }))]) run('validateRegisterImage', (x) => V.validateRegisterImage(x).list, b);

// ── image edit
const bImg = { ports: [{ name: 'http', port: 80, protocol: 'tcp' }, { name: 'admin', port: 81, protocol: 'tcp' }], env: [{ name: 'A' }, { name: 'B' }], volumes: [{ name: 'data' }, { name: 'logs' }] };
const aImg = { ports: [{ name: 'web', port: 8080, protocol: 'tcp' }, { name: 'admin', port: 81, protocol: 'udp' }, { name: 'new', port: 1, protocol: 'tcp' }], env: [{ name: 'A2' }, { name: 'C' }], volumes: [{ name: 'data' }] };
for (const r of [undefined, null, 'x', [], [{ kind: 'port', from: 'http', to: 'web' }], [{ kind: 'port', from: 'http', to: 'web' }, { kind: 'port', from: 'http', to: 'new' }, { kind: 'env', from: 'A', to: 'A2' }], [null, 5, {}, { kind: 'x', from: 'a', to: 'b' }, { kind: 'port', from: 1, to: 'b' }, { kind: 'port', from: 'a', to: 'a' }, { kind: 'env', from: 'Z', to: 'A2' }, { kind: 'env', from: 'A', to: 'Z' }, { kind: 'volume', from: 'logs', to: 'data' }]]) run('imageNameChanges', IE.imageNameChanges, bImg, aImg, r);
run('imageNameChanges', IE.imageNameChanges, { ports: [{ name: 'a', port: 1, protocol: 'tcp' }, { name: 'b', port: 1, protocol: 'tcp' }], env: [], volumes: [] }, { ports: [{ name: 'a', port: 1, protocol: 'tcp' }, { name: 'c', port: 1, protocol: 'tcp' }], env: [], volumes: [] }, [{ kind: 'port', from: 'a', to: 'c' }]);
const edit = { sites: [{ siteSlug: 'app', config: { containers: [
  { containerSlug: 'w', serviceSlug: 's', version: 'v1', ports: [{ portName: 'http' }, { portName: 'x' }], healthcheck: { type: 'http', portName: 'http' }, env: [{ name: 'A', value: '{{ container.env.B }} container.env.B {{ xcontainer.env.B }} {{ container.env.BB }} {{ container.env.B-x }} {{ container.env.B.y }}' }, { name: 'B', value: 'x' }, { name: 'C', value: '{{ site.app.port.w.http }} {{ site.app.port.w.httpx }}\n{{ site.app.port.w.http }}' }, { name: 'D', value: 3 }], volumes: [{ imageVolume: 'data', mountPath: '/a/{{ container.env.B }}' }, { imageVolume: null, mountPath: '/{{ container.env.B }}' }, { imageVolume: 'z', mountPath: null }] },
  { containerSlug: 'o', serviceSlug: 's', version: 'v2', env: [{ name: 'E', value: '{{ site.app.port.w.http }}' }], ports: [{ portName: 'http' }] },
  { serviceSlug: 's', version: 'v1', env: [{ name: 'A', value: 'y' }], healthcheck: { type: 'exec', portName: 'http' } },
], edge: { routes: [{ routeId: 'r', action: { type: 'proxy', containerSlug: 'w', portName: 'http' } }, { routeId: 'q', action: { type: 'redirect' } }, { routeId: 't', action: null }] } } }, { siteSlug: 'b', config: null }, { siteSlug: 'c', config: { containers: [{ containerSlug: 'c', serviceSlug: 'q', env: [{ name: 'Z', value: '{{ site.app.port.w.http | default("x") }}' }] }] } }] };
for (const [k, n] of [['port', 'http'], ['port', 'x'], ['port', 'nope'], ['env', 'A'], ['env', 'B'], ['volume', 'data'], ['volume', 'z']] as const) run('imageNameUses', IE.imageNameUses, edit, 's', 'v1', k, n);
run('imageNameUses', IE.imageNameUses, { sites: null }, 's', 'v1', 'env', 'A');
run('imageNameUses', IE.imageNameUses, {}, 's', 'v1', 'env', 'A');
run('imageNameUses', IE.imageNameUses, { sites: [null] }, 's', 'v1', 'env', 'A');
for (const r of [[], [{ kind: 'port', from: 'http', to: 'web' }], [{ kind: 'env', from: 'B', to: 'A' }], [{ kind: 'env', from: 'B', to: 'A' }, { kind: 'env', from: 'A', to: 'B' }], [{ kind: 'env', from: 'B', to: 'Q' }, { kind: 'env', from: 'BB', to: 'R' }], [{ kind: 'volume', from: 'data', to: 'd2' }], [{ kind: 'port', from: 'http', to: 'x' }, { kind: 'port', from: 'x', to: 'http' }, { kind: 'env', from: 'A', to: 'A2' }, { kind: 'volume', from: 'z', to: 'zz' }]]) run('renameImageNames', IE.renameImageNames, edit, 's', 'v1', r);
run('renameImageNames', IE.renameImageNames, { sites: [] }, 's', 'v1', [{ kind: 'port', from: 'a', to: 'b' }]);
const selfImg = { env: [{ name: 'X', value: '{{ container.env.A }}', z: 1 }, { name: 'Y' }, { name: 'Z', value: 3 }], volumes: [{ name: 'v', mountPath: '/a/{{ container.env.A }}', z: 1 }, { name: 'w', mountPath: '/b' }], other: 1 };
for (const r of [[], [{ kind: 'env', from: 'A', to: 'B' }]]) run('renameImageSelfRefs', IE.renameImageSelfRefs, selfImg, r);
run('renameImageSelfRefs', IE.renameImageSelfRefs, { env: [], volumes: [{}] }, [{ kind: 'env', from: 'A', to: 'B' }]);
run('renameImageSelfRefs', IE.renameImageSelfRefs, { env: [], volumes: [{ mountPath: 3 }] }, [{ kind: 'env', from: 'A', to: 'B' }]);
run('imageNames', IE.imageNames, { z: 1, volumes: [], env: [], ports: [] });

// ── types
const VERSIONS = ['1.0.0', 'v1.0.0', '1.0.0-1', '1.0.0+2', '1.0.0-10', '1.0.0-9', '1.0.0-a', '1.0.0-b', '0.10.0', '0.9.9', '0.10.0-05011200', ' 1.2.3 ', 'x', '', '1.2', '10.0.0', '2.0.0', '1.0.0\n', '1.0.0-a\nb', 'v0.10.0x'];
for (const a of VERSIONS) {
  for (const b of VERSIONS) {
    run('compareAgentVersions', T.compareAgentVersions, a, b);
    run('agentUpgradeAvailable', T.agentUpgradeAvailable, a, b);
    run('agentInstallAction', T.agentInstallAction, a, b);
  }
  run('supportsGuiUpgrade', T.supportsGuiUpgrade, a);
}
for (const [d, t] of [[null, {}], [1, {}], [2, { schema: 1 }], [2, { schema: 3, minSchema: 3 }], [3, { schema: 3, minSchema: 3 }], [undefined, { schema: null, minSchema: null }]] as const) run('agentSchemaCompatible', T.agentSchemaCompatible, d, t);
for (const s of [0, 0.4, 0.5, 59, 59.5, 60, 89, 90, 3599, 3600, 5400, 7199, 7170, 7198, 86399, 86400, 90000, 88200, 172800, 171000, -130, -7200]) run('formatSkewDuration', T.formatSkewDuration, s);
for (const s of [null, undefined, 'x', 120, -120, 121, -121, 19800, 90000]) run('clockSkewWarning', T.clockSkewWarning, s);
for (const t of [{ kind: 'install' }, { kind: 'install', revokedAt: 't' }, { kind: 'maintenance', usedAt: 'x', expiresAt: '2000-01-01T00:00:00.000Z' }, { kind: 'install', usedAt: 't' }, { kind: 'install', expiresAt: '2000-01-01T00:00:00.000Z' }, { kind: 'install', expiresAt: '2100-01-01T00:00:00.000Z' }, { kind: 'install', expiresAt: 'nonsense' }, { kind: 'install', expiresAt: '2020-06-01T00:00:00.000Z' }]) run('installTokenState', T.installTokenState, t, Date.parse('2020-06-01T00:00:00.000Z'));
for (const s of [{ digest: '' }, { digest: 'x' }, {}]) run('isSiteImage', T.isSiteImage, s);
for (const [a, f] of [['a.b', 'a.'], ['a.b', 'a.b'], ['a.b', 'b.'], ['a', 'a.'], ['a.', 'a.'], ['a', ''], ['', '']]) run('auditActionMatches', auditActionMatches, a, f);
for (const a of [{ activityId: 'a', deploymentId: 'd', siteSlug: 's', seq: 1, ts: 't', actor: null, origin: 'o', type: 'x', summary: 'ü', data: { b: 1, a: [1.5, 'x', null, { z: 1, y: 2 }] }, prevHash: null }, { activityId: 'a', deploymentId: 'd', siteSlug: 's', seq: 2, ts: 't', actor: 'me', origin: 'o', type: 'x', summary: 's', data: {}, prevHash: 'sha256:x', extra: 1 }, { prevHash: null, data: {} }]) run('activityHashInput', activityHashInput, a);

// ── staging
const blobs = { files: [{ content: { sha256: 'sha256:' + 'b'.repeat(64), sizeBytes: 5 } }, { content: null }, null, { content: { sha256: 'a'.repeat(64), sizeBytes: 'x' } }, { content: { sha256: 'bad', sizeBytes: 1 } }, { content: { sha256: 'sha256:' + 'b'.repeat(64), sizeBytes: 9 } }], sites: [null, { config: null }, { config: { volumes: [null, { seed: null }, { seed: { kind: 'tree', entries: [null, { sha256: 'c'.repeat(64), size: 3 }] } }, { seed: { kind: 'tree' } }, { seed: { kind: 'single', sha256: 'd'.repeat(64), size: 1 } }, { seed: { kind: 'archive', blobSha256: 'e'.repeat(64), size: 2 } }, { seed: { kind: 'files' } }] } }] };
for (const c of [blobs, null, undefined, {}, { files: 3 }, { sites: [{ config: { volumes: 3 } }] }]) run('configBlobs', ST.configBlobs, c);
run('newConfigBlobs', ST.newConfigBlobs, null, blobs);
run('newConfigBlobs', ST.newConfigBlobs, { files: [{ content: { sha256: 'sha256:' + 'b'.repeat(64) } }] }, blobs);
run('stagingObject', ST.stagingObject, 's', 'h');
for (const s of ['sha256:ab', 'ab']) run('blobHex', ST.blobHex, s);
run('totalBytes', ST.totalBytes, [{ sizeBytes: 1 }, { sizeBytes: 0 }, {}, { sizeBytes: 2.5 }]);
for (const p of [null, { phase: 'checking' }, { phase: 'committing' }, { phase: 'uploading', done: 1, total: 1, bytesDone: 0, bytesTotal: 512 }, { phase: 'uploading', done: 1, total: 2, bytesDone: 1536, bytesTotal: 5242880 }, { phase: 'uploading', done: 0, total: 3, bytesDone: 1572864, bytesTotal: 10485760 }, { phase: 'uploading', done: 0, total: 3, bytesDone: -5, bytesTotal: 110048768 }, { phase: 'uploading', done: 0, total: 3, bytesDone: 1101004, bytesTotal: 1048576 * 2.5 }]) run('stageProgressText', ST.stageProgressText, p);
for (const s of ['01ARZ3NDEKTSV4RRFFQ69G5FAV', '01arz', '01ARZ3NDEKTSV4RRFFQ69G5FAV\n', 'I'.repeat(26)]) run('RE_STAGE_ID', (x) => ST.RE_STAGE_ID.test(x), s);

// ── files
const sf = (o: object) => ({ fileId: 'f', state: 'ready', cloud: { stored: false, fetch: null }, sites: [], here: false, job: null, sources: { peers: [], cloud: false }, origin: { kind: 'cloud' }, ...o });
const sfs = [sf({}), sf({ state: 'unpublished' }), sf({ state: 'uploading' }), sf({ here: true }), sf({ job: { phase: 'failed' } }), sf({ job: { phase: 'waiting-cloud' } }), sf({ job: { phase: 'queued' } }), sf({ sources: { peers: ['a'], cloud: false } }), sf({ sources: { peers: [], cloud: true } }), sf({ sites: ['x', 'y'] }), sf({ cloud: { stored: true, fetch: null }, sites: ['x'] }), sf({ cloud: { stored: false, fetch: { error: 'e' } } }), sf({ cloud: { stored: false, fetch: { error: null } } }), sf({ origin: { kind: 'site', siteSlug: 's', via: 'path', sourcePath: '/p' } }), sf({ origin: { kind: 'site', siteSlug: 's', via: 'path', sourcePath: null } }), sf({ origin: { kind: 'site', siteSlug: 's', via: 'browser', sourcePath: null } })];
for (const f of sfs) {
  run('siteFileBadge', F.siteFileBadge, f);
  run('cloudFileBadge', F.cloudFileBadge, f);
  run('fileCopiesText', F.fileCopiesText, f);
  run('fileOriginText', F.fileOriginText, f);
}

// ── transfers
for (const p of ['/', '', 'a', '//a/./b/../c/', '/..', '/a/..', '/a\0b', '/a b/']) {
  run('fsClean', TR.fsClean, p);
  run('fsParent', TR.fsParent, p);
  run('fsCrumbs', TR.fsCrumbs, p);
}
const protectedPaths = ['/home/opm', '/opt/opm/', '', '/', 'rel'];
for (const p of ['', 'a', '/home/opm', '/home/opm/x', '/home/opmx', '/opt/opm/y', '/proc', '/proc/1', '/procx', '/etc/shadow', '/etc/shadow-', '/etc/ssh/ssh_host_rsa_key', '/etc/ssh/ssh_host_rsa_key.pub', '/etc/ok', '/var/log/x', '/run/a']) run('hostFsBlockReason', TR.hostFsBlockReason, p, protectedPaths);
run('hostFsVectors', (v) => v, HOSTFS);
const tr = (o: object) => ({ direction: 'to-site', status: 'ready', sizeBytes: 100, fetchedAt: null, savedPath: null, ...o });
const d = (o: object) => ({ phase: 'uploading', attempts: 0, lastError: null, bytesDone: 0, ...o });
const trs = [tr({}), tr({ status: 'expired' }), tr({ status: 'uploading' }), tr({ status: 'saved-on-server', savedPath: '/p' }), tr({ status: 'downloaded' }), tr({ fetchedAt: 't' }), tr({ delivery: d({ phase: 'downloading', bytesDone: 50 }) }), tr({ delivery: d({ phase: 'downloading', bytesDone: 500, attempts: 2, lastError: 'e' }) }), tr({ delivery: d({ phase: 'pending' }) }), tr({ delivery: d({ phase: 'pending', attempts: 1 }) }), tr({ delivery: d({ phase: 'done' }) }), tr({ direction: 'to-cloud', status: 'downloaded' }), tr({ direction: 'to-cloud' }), tr({ direction: 'to-cloud', status: 'uploading' }), tr({ direction: 'to-cloud', status: 'uploading', delivery: d({ bytesDone: 10 }) }), tr({ direction: 'to-cloud', status: 'uploading', sizeBytes: 0, delivery: d({ bytesDone: 10 }) }), tr({ direction: 'to-cloud', status: 'uploading', delivery: d({ phase: 'queued', bytesDone: 5, attempts: 1 }) }), tr({ direction: 'to-cloud', status: 'uploading', delivery: d({ phase: 'queued' }) }), tr({ direction: 'to-cloud', status: 'uploading', delivery: d({ phase: 'queued', attempts: 2, lastError: 'x' }) })];
for (const t of trs) for (const s of ['agent', 'cloud']) run('transferState', TR.transferState, t, s);

// ── volume content
for (const p of [...VOLS.paths.map((x: { path: string }) => x.path), 3, null, '', 'a'.repeat(1025), 'a/' + 'b'.repeat(256), 'ü'.repeat(513), '.opm-seeded', 'x/.opm-seeded', 'a\\b', 'a\x7fb']) run('contentPathError', VC.contentPathError, p);
for (const p of [...VOLS.patterns.map((x: { pattern: string }) => x.pattern), 3, null, 'x'.repeat(201), '😀'.repeat(101), '/', '//', 'a/', '/a/', 'a\tb', ' x']) run('excludePatternError', VC.excludePatternError, p);
for (const e of VOLS.excludes) run('isExcluded', (ps: string[], p: string, dir: boolean) => VC.isExcluded(p, dir, VC.compileExcludes(ps)), e.patterns, e.path, e.isDir);
for (const [ps, p, dir] of [[['a/**/b'], 'a/b', false], [['a/**/b'], 'a/x/y/b', false], [['a/**'], 'a/x', false], [['**/x'], 'q/x', false], [['*.log'], 'a/b.log', false], [['a?c'], 'a/c', false], [['a?c'], 'abc', false], [['a.c'], 'abc', false], [['a+b', '(x)', '{y}', '^z', 'q$', 'p|r'], 'a+b', false], [['a+b', '(x)', '{y}', '^z', 'q$', 'p|r'], 'p|r', false], [['a b'], 'a b', false], [['x/'], 'x', false], [['x/'], 'x', true], [['/.opm-seeded'], '.opm-seeded', false], [[], '.opm-seeded', false], [[], 'a/.opm-seeded', false], [['bad!', '!no', '[x]'], '[x]', false], [['**'], 'a/b', false], [['***'], 'ab', false], [['/a**b'], 'axb', false], [['/a***b'], 'axyb', false], [['ü?'], 'üx', false], [['a\nb'], 'a\nb', false]] as const) run('isExcluded', (ps: string[], p: string, dir: boolean) => VC.isExcluded(p, dir, VC.compileExcludes(ps)), ps, p, dir);
const treeE = (path: string, o: object = {}) => ({ path, sha256: 'sha256:' + 'a'.repeat(64), size: 1, mode: '0644', render: 'none', ...o });
const trees: any[] = VOLS.trees.map((x: { tree: unknown }) => x.tree);
const T1 = { entries: [treeE('a/b.json'), treeE('a/c/d.txt', { size: 5 }), treeE('e', { render: 'expressions' }), treeE('.env')], dirs: ['x/y', 'z'] };
trees.push(T1, { entries: [treeE('é'), treeE('z'), treeE('😀'), treeE('￿')], dirs: ['😀d', '￿d', 'é/x'] }, null, undefined, {}, { entries: 3 }, { dirs: 3 });
for (const t of trees) {
  run('normalizeVolumeTree', VC.normalizeVolumeTree, t);
  run('volumeTreeHashInput', VC.volumeTreeHashInput, t);
}
for (const t of [T1, VC.normalizeVolumeTree(T1), { entries: [], dirs: [] }]) {
  run('treeStats', VC.treeStats, t);
  run('treeNodes', VC.treeNodes, t);
  run('filterExcluded', VC.filterExcluded, t, ['*.txt', 'z/', '/x/**']);
  for (const p of ['a', 'a/b.json', 'a/c', 'z', 'nope', 'x', 'x/y', 'e', '.env']) {
    run('treeHasFile', VC.treeHasFile, t, p);
    run('treeHasDir', VC.treeHasDir, t, p);
    run('treeDelete', VC.treeDelete, t, p);
    run('duplicateName', VC.duplicateName, t, p);
    run('treeDuplicate', VC.treeDuplicate, t, p);
    run('treeMkdir', VC.treeMkdir, t, p);
    run('treeMkdir', VC.treeMkdir, t, p + '/n');
    for (const to of ['q', 'a/q', 'e/q', 'a/b.json', 'z/n']) run('treeMove', VC.treeMove, t, p, to);
    run('treeMove', VC.treeMove, t, p, p);
    run('treeMove', VC.treeMove, t, p, p + '/sub');
    run('treePutFile', VC.treePutFile, t, treeE(p, { z: 1 }));
    run('treePutFile', VC.treePutFile, t, treeE(p, { z: 1 }), { overwrite: true });
  }
  run('treeDuplicate', VC.treeDuplicate, t, 'a/b.json', 'q');
  run('treeDuplicate', VC.treeDuplicate, t, 'a', 'q');
  run('treeDuplicate', VC.treeDuplicate, t, 'x', 'a/b.json');
  run('treePutFile', VC.treePutFile, t, treeE('/bad'));
  run('treePutFile', VC.treePutFile, t, treeE('e/x'));
}
for (const t of [T1, { entries: [treeE('a'), treeE('a'), treeE('a/b'), treeE('../x'), null, { path: 3 }, treeE('b', { sha256: 'x', size: -1, mode: 'x', render: 'x' }), treeE('c', { size: 1.5 }), treeE('d', { size: 60 * 1048576, render: 'expressions' })], dirs: ['a', 'q', 'q', 'a/x', 3, null, '/bad'] }, { entries: [], dirs: 3 }, null, 5, { entries: Array(1001).fill(treeE('m')).map((e, i) => ({ ...e, path: `f${i}` })), dirs: Array(1001).fill(0).map((_, i) => `d${i}`) }, { entries: Array(5).fill(0).map((_, i) => treeE(`g${i}`, { size: 50 * 1048576 })), dirs: [] }]) for (const s of [true, false]) run('volumeTreeIssues', VC.volumeTreeIssues, t, s);
for (const p of VOLS.plans) {
  run('planVolumePush', VC.planVolumePush, p.base, p.live, p.cloud);
  run('diffVolumeTrees', VC.diffVolumeTrees, p.base, p.cloud);
  run('countTreeChanges', VC.countTreeChanges, p.base, p.cloud);
  run('diffVolumeTrees', VC.diffVolumeTrees, p.live, null);
  for (const res of ['overwrite', 'keep-live', 'per-file']) {
    run('mergeVolumePush', VC.mergeVolumePush, p.base, p.live, p.cloud, res);
    const c = Object.fromEntries(VC.planVolumePush(p.base, p.live, p.cloud).map((x, i) => [x.path, i % 2 ? 'cloud' : 'live']));
    run('mergeVolumePush', VC.mergeVolumePush, p.base, p.live, p.cloud, res, c);
  }
}
for (const m of VOLS.merges) {
  const p = VOLS.plans.find((x: { name: string }) => x.name === m.plan);
  run('mergeVolumePush', VC.mergeVolumePush, p.base, p.live, p.cloud, m.resolution, m.choices);
}
for (const n of ['a.json', 'A.JSON', 'x.map', '.babelrc', '.eslintrc', 'a.mjs', 'a.tsx', 'a.yml', 'a.vue', 'a.less', 'a.markdown', 'a.svg', 'a.py', 'a.sql', 'a.zsh', 'Dockerfile', 'd/dockerfile', 'a.toml', '.env', '.env.local', 'x.env', 'a.properties', 'noext', 'a.b.c', 'a.', 'a.go']) run('languageForPath', VC.languageForPath, n);
for (const m of ['0644', '0755', '0000', '0777', '644', 'x', '0888']) run('formatMode', VC.formatMode, m);
for (const n of [0, 1, 1023, 1024, 1536, 10239, 10240, 1048575, 1048576, 2621440, 10485759, 10485760, 1073741823, 1073741824, 5368709120, 1126, 1177, 2.5]) run('formatContentBytes', VC.formatContentBytes, n);
const U = (s: string) => [...Buffer.from(s, 'utf8')];
const longUtf = 'x'.repeat(8191) + 'é' + 'yz';
for (const b of [[], U('hello'), [0], [0xff], U('é'), [0xc3], U(longUtf), [...U('x'.repeat(8190)), 0xf0, 0x9f, 0x98, 0x80, 0x61], [...Array(9000).fill(0x61), 0], [...Array(8191).fill(0x61), 0xff, 0x61], [0xed, 0xa0, 0x80], [0xc0, 0x80], [0xef, 0xbb, 0xbf, 0x61]]) run('looksLikeText', (x: number[]) => VC.looksLikeText(Uint8Array.from(x)), b);
for (const v of [null, undefined, {}, { persist: null }, { persist: { a: 1 } }]) run('volumePersist', VC.volumePersist, v);
for (const s of [null, undefined, {}, { volumes: [] }, { volumes: [null] }, { volumes: [{ persist: {} }] }, { volumes: [{ seed: { kind: 'tree' } }] }, { volumes: [{ seed: { kind: 'single' } }] }, { volumes: [{ seed: { kind: 'archive' } }] }, { volumes: [{ seed: null }] }]) run('usesVolumeContent', VC.usesVolumeContent, s);
for (const s of [null, undefined, { kind: 'file' }, { kind: 'files' }, { kind: 'tree' }]) run('isLegacyFileSeed', VC.isLegacyFileSeed, s);
run('defaultVolumePersist', VC.defaultVolumePersist);
for (const p of ['a/b/c', 'a', '', 'a/']) {
  run('parentDir', VC.parentDir, p);
  run('baseName', VC.baseName, p);
  run('ancestorDirs', VC.ancestorDirs, p);
  run('joinPath', VC.joinPath, p, 'n');
}
const ls = (o: object) => ({ state: 'in-sync', liveTreeHash: null, snapshotTreeHash: null, queued: false, liveChanged: false, ...o });
for (const s of [{}, { lastSnapshot: { treeHash: 'a' } }, { live: ls({ snapshotTreeHash: 's' }) }, { lastSnapshot: { treeHash: 'a' }, live: ls({ state: 'error' }) }, { lastSnapshot: { treeHash: 'a' }, live: ls({ queued: true }) }, { lastSnapshot: { treeHash: 'a' }, live: ls({ liveChanged: true }) }, { push: { status: 'done', treeHash: 'x' }, lastSnapshot: { treeHash: 'a' } }, { push: { status: 'pending', treeHash: 'x', baseTreeHash: 'a' }, lastSnapshot: { treeHash: 'a' } }, { push: { status: 'pending', treeHash: 'a', baseTreeHash: null }, lastSnapshot: { treeHash: 'a' } }, { push: { status: 'pending', treeHash: 'x', baseTreeHash: 'y' }, live: ls({ state: 'cloud-ahead', liveTreeHash: 'z' }) }, { push: { status: 'pending', treeHash: 'x', baseTreeHash: 'y' }, live: ls({ state: 'other', liveTreeHash: 'z' }) }, { push: { status: 'pending', treeHash: 'x', baseTreeHash: null }, live: ls({ state: 'cloud-ahead' }), lastSnapshot: { treeHash: 'a' } }]) run('cloudSyncState', VC.cloudSyncState, s);
const sha = 'sha256:' + 'f'.repeat(64);
const migFile = (fileSlug: string, o: object = {}) => ({ fileSlug, label: fileSlug, mime: 'text/plain', editor: 'text', maxBytes: 100, render: 'expressions', content: { sha256: sha, sizeBytes: 3 }, ...o });
const migCfg = (volumes: unknown[], containers: unknown[], files: unknown[]) => ({ files, sites: [{ siteSlug: 'app', config: { volumes, containers } }] });
const c0 = (vols2: unknown[], o: object = {}) => ({ containerSlug: 'c', serviceSlug: 's', version: 'v1', volumes: vols2, ...o });
const migs = [
  migCfg([{ volumeSlug: 'v', seed: { kind: 'files', entries: [{ path: 'a', fileSlug: 'f1' }, { path: 'b', fileSlug: 'gone' }, { path: 'c', fileSlug: 'nc' }] } }, { volumeSlug: 'w', seed: { kind: 'file', fileSlug: 'f2' } }, { volumeSlug: 'x', seed: { kind: 'file', fileSlug: 'nc' } }, { volumeSlug: 'y', seed: { kind: 'files', entries: [] } }, { volumeSlug: 'z', seed: { kind: 'tree', entries: [], dirs: [] } }], [], [migFile('f1'), migFile('f2', { editor: 'binary', render: undefined }), migFile('nc', { content: null }), migFile('keep')]),
  migCfg([{ volumeSlug: 'v', seed: null }, { volumeSlug: 't', seed: { kind: 'tree', entries: [{ path: 'exists', sha256: sha, size: 1, mode: '0644', render: 'none' }], dirs: ['d'] } }, { volumeSlug: 's', kind: 'file', seed: null }, { volumeSlug: 'a', seed: { kind: 'archive' } }], [c0([{ imageVolume: null, mountPath: '/data/', source: { kind: 'volume', volumeSlug: 'v' }, readOnly: false }, { imageVolume: null, mountPath: '/t', source: { kind: 'volume', volumeSlug: 't' }, readOnly: false }, { imageVolume: null, mountPath: '/s', source: { kind: 'volume', volumeSlug: 's' }, readOnly: false }, { imageVolume: null, mountPath: '/a', source: { kind: 'volume', volumeSlug: 'a' }, readOnly: false }, { imageVolume: null, mountPath: '/nv', source: { kind: 'volume', volumeSlug: 'nv' }, readOnly: false }, { imageVolume: null, mountPath: '/h', source: { kind: 'host', path: '/x' }, readOnly: false }, { imageVolume: null, mountPath: '/ro', source: { kind: 'volume', volumeSlug: 'v' }, readOnly: true }, { imageVolume: null, mountPath: '/{{ x }}', source: { kind: 'volume', volumeSlug: 'v' }, readOnly: false },
    { imageVolume: null, mountPath: '/data/a.txt', source: { kind: 'file', fileSlug: 'f1' }, readOnly: true }, { imageVolume: 'iv', mountPath: '/data/sub/b.txt', source: { kind: 'file', fileSlug: 'f2' }, readOnly: true }, { imageVolume: null, mountPath: '/t/exists', source: { kind: 'file', fileSlug: 'f3' }, readOnly: true }, { imageVolume: null, mountPath: '/t/new', source: { kind: 'file', fileSlug: 'f3' }, readOnly: true }, { imageVolume: null, mountPath: '/s/x', source: { kind: 'file', fileSlug: 'f4' }, readOnly: true }, { imageVolume: null, mountPath: '/a/x', source: { kind: 'file', fileSlug: 'f4' }, readOnly: true }, { imageVolume: null, mountPath: '/nv/x', source: { kind: 'file', fileSlug: 'f4' }, readOnly: true }, { imageVolume: null, mountPath: '/h/x', source: { kind: 'file', fileSlug: 'f4' }, readOnly: true }, { imageVolume: null, mountPath: '/data/n', source: { kind: 'file', fileSlug: 'nc' }, readOnly: true }, { imageVolume: null, mountPath: '/data/../x', source: { kind: 'file', fileSlug: 'f1' }, readOnly: true }, { imageVolume: null, mountPath: '/data/{{ y }}', source: { kind: 'file', fileSlug: 'f1' }, readOnly: true }, { imageVolume: null, mountPath: null, source: { kind: 'file', fileSlug: 'f1' }, readOnly: true }, { imageVolume: 'known', mountPath: null, source: { kind: 'file', fileSlug: 'f5' }, readOnly: true }, { imageVolume: 'unk', mountPath: null, source: { kind: 'file', fileSlug: 'f5' }, readOnly: true }, { imageVolume: null, mountPath: '/other', source: { kind: 'file', fileSlug: 'f6' }, readOnly: true }])], [migFile('f1'), migFile('f2'), migFile('f3'), migFile('f4'), migFile('nc', { content: null }), migFile('f5'), migFile('f6')]),
  migCfg([{ volumeSlug: 'v', seed: { kind: 'file', fileSlug: 'r1' } }, { volumeSlug: 'w', seed: { kind: 'file', fileSlug: 'r2' } }, { volumeSlug: 'u', seed: { kind: 'file', fileSlug: 'r3' } }, { volumeSlug: 'q', seed: { kind: 'file', fileSlug: 'r-4' } }], [c0([], { env: [{ name: 'X', value: '{{ file.r1 }}' }, { name: 'Y', value: '{{ file.r2x }} {{ file.r3 }}' }, { name: 'Z', value: '{{ file.r-4a }} file.r-4' }] })], [migFile('r1'), migFile('r2'), migFile('r3'), migFile('r-4')]),
];
const ivFn = (s: string, v: string) => (v === 'v1' ? [{ name: 'known', mountPath: '/data/k' }, { name: 'iv', mountPath: '/iv' }] : null);
for (const c of migs) {
  run('migrateVolumeFileSeeds', (x, known) => VC.migrateVolumeFileSeeds(x, known ? ivFn : () => null), c, true);
  run('migrateVolumeFileSeeds', (x, known) => VC.migrateVolumeFileSeeds(x, known ? ivFn : () => null), c, false);
}

// ── slugs
for (const v of ['Hello World', 'Ünï çødé', '123 go', '', '!!!', 'a', 'x'.repeat(60), 'trail---', 'ﬁ ligature', '日本語', 'K Kelvin', 'İstanbul', 'x'.repeat(39) + '-y', '1', '-']) for (const p of ['x-', 't-', 'd-']) run('slugifyLabel', slugifyLabel, v, p);
for (const [b, t] of [['a', []], ['a', ['a']], ['a', ['a', 'a-2', 'a-3']], ['x'.repeat(40), ['x'.repeat(40)]], ['x'.repeat(37) + '-yy', ['x'.repeat(37) + '-yy']]] as const) run('uniqueSlug', (base: string, tk: string[]) => uniqueSlug(base, new Set(tk)), b, t);

console.log(JSON.stringify({ vals, reSamples: RE_SAMPLES, reVals, images: CAT.images, fns: out }));
