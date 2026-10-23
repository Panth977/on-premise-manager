/**
 * Mock of the dev tools API (§7.6, §21 #97, DEVTOOLS_API): connections (presets of the head + manual ones with masked
 * literals + reveal), test / disconnect, schema + table detail (devsql.ts), executeSql with destructive 422 /
 * readOnly / transactions / cancel / paging / export / cell values, Redis (devredis.ts), MQTT (devmqtt.ts), HTTP
 * (a fake server), saved items, environments, history (+ activities). mock/index.ts routes every /devtools/ path here.
 * Knobs (localStorage): opm.mock.devtoolsOff=1 (agent.json tools.devTools false).
 */
import type { ErrorCode } from '@opm/shared/types';
import { ERROR_HTTP_STATUS, MASK, REVEAL_SECONDS } from '@opm/shared/types';
import {
  DEVTOOLS_DEFAULT_LIMITS,
  DEVTOOL_KINDS,
  applyEnvironment,
  classifyRedis,
  classifyStatement,
  defaultDevToolAuth,
  defaultDevToolOptions,
  devToolTargetText,
  parseRedisCommandLine,
  redactHttpRequest,
  redactStatement,
  splitSqlStatements,
  HTTP_READ_METHODS,
  type CellValue,
  type DevToolConnection,
  type DevToolConnectionInput,
  type DevToolEnvironment,
  type DevToolHistoryItem,
  type DevToolKind,
  type DevToolOp,
  type DevToolSaved,
  type DevToolsInfo,
  type ExecuteHttpRequest,
  type ExecuteHttpResponse,
  type ExecuteRedisRequest,
  type ExecuteSqlRequest,
  type HttpRequestSpec,
  type MqttPublishRequest,
  type MqttSubscription,
  type ResultColumn,
  type StatementResult,
} from '@opm/shared/devtools';
import * as SQL from './devsql';
import * as R from './devredis';
import * as M from './devmqtt';

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type AnySession = any;
interface Ctx {
  requireSession: () => AnySession;
  requireActor: (h: Headers, s: AnySession) => string;
  fail: (code: ErrorCode, message: string, details?: Record<string, unknown>) => never;
  json: (status: number, body: unknown, extra?: Record<string, string>) => Response;
  addActivity: (site: string, type: string, summary: string, actor: string | null, data?: Record<string, unknown>) => void;
  headSeq: () => number | null;
}
const SITES = ['app', 'db', 'northwind'];
const L = DEVTOOLS_DEFAULT_LIMITS;
const now = () => Date.now();
const iso = (ms = now()) => new Date(ms).toISOString();
const knob = (k: string) => {
  try {
    return localStorage.getItem(k);
  } catch {
    return null;
  }
};
let idn = 0;
const ULID = '0123456789ABCDEFGHJKMNPQRSTVWXYZ';
function mid(prefix: string) {
  let t = now();
  let s = '';
  for (let i = 0; i < 10; i++) {
    s = ULID[t % 32] + s;
    t = Math.floor(t / 32);
  }
  idn++;
  for (let i = 0; i < 16; i++) s += ULID[(idn * 7 + i * 13 + Math.floor(Math.random() * 32)) % 32];
  return `${prefix}${s}`;
}
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

// ───────────────────────────── info (head containers of each site)
type InfoContainer = DevToolsInfo['containers'][number];
const port = (portName: string, containerPort: number, hostPort: number | null = null) => ({ portName, containerPort, protocol: 'tcp', hostPort });
const CONTAINERS: Record<string, InfoContainer[]> = {
  app: [
    { containerSlug: 'hello-web', serviceSlug: 'hello-web', image: 'hello-web:v1', running: true, ports: [port('http', 80, 32768)], mounts: [] },
    { containerSlug: 'hello-api', serviceSlug: 'hello-api', image: 'hello-api:v1', running: true, ports: [port('http', 3000, 32769)], mounts: [{ mountPath: '/app/data', kind: 'volume', volumeSlug: 'hello-api-data' }, { mountPath: '/app/helper/config/env', kind: 'volume', volumeSlug: 'api-env' }] },
    { containerSlug: 'hello-api-2', serviceSlug: 'hello-api', image: 'hello-api:v1', running: false, ports: [port('http', 3000, 9081)], mounts: [{ mountPath: '/app/data', kind: 'volume', volumeSlug: 'hello-api-2-data' }] },
    { containerSlug: 'postgres', serviceSlug: 'postgres', image: 'postgres:16', running: true, ports: [port('pg', 5432, 5432)], mounts: [{ mountPath: '/var/lib/postgresql/data', kind: 'volume', volumeSlug: 'pg-data' }], env: [{ name: 'PGDATA', secret: false }, { name: 'POSTGRES_DB', secret: false }, { name: 'POSTGRES_PASSWORD', secret: true }, { name: 'POSTGRES_USER', secret: false }] },
    { containerSlug: 'redis', serviceSlug: 'redis', image: 'redis:7', running: true, ports: [port('redis', 6379)], mounts: [] },
    { containerSlug: 'mqtt', serviceSlug: 'mqtt', image: 'mqtt:v2', running: true, ports: [port('mqtt', 1883, 1883), port('ws', 9001)], mounts: [] },
    { containerSlug: 'cassandra', serviceSlug: 'cassandra', image: 'cassandra:4.1', running: true, ports: [port('cql', 9042)], mounts: [{ mountPath: '/var/lib/cassandra', kind: 'volume', volumeSlug: 'cassandra-data' }] },
  ],
  db: [
    { containerSlug: 'redis', serviceSlug: 'redis', image: 'redis:7', running: true, ports: [port('redis', 6379, 6379)], mounts: [] },
    { containerSlug: 'postgres', serviceSlug: 'postgres', image: 'postgres:16', running: true, ports: [port('pg', 5432)], mounts: [{ mountPath: '/var/lib/postgresql/data', kind: 'volume', volumeSlug: 'pg-data' }] },
  ],
  northwind: [
    { containerSlug: 'mqtt', serviceSlug: 'mqtt', image: 'mqtt:v2', running: true, ports: [port('http', 1883, 1883)], mounts: [] },
    { containerSlug: 'nodered', serviceSlug: 'nodered', image: 'nodered:v4', running: true, ports: [port('http', 1880, 1880)], mounts: [{ mountPath: '/data', kind: 'volume', volumeSlug: 'nodered-data' }] },
    { containerSlug: 'northwind-admin', serviceSlug: 'northwind-admin', image: 'northwind-admin:v2', running: true, ports: [port('http', 3000)], mounts: [{ mountPath: '/app/runtime.json', kind: 'volume', volumeSlug: 'admin-runtime' }] },
  ],
};
const VOLUMES: Record<string, DevToolsInfo['volumes']> = {
  app: [
    { volumeSlug: 'hello-api-data', label: 'API data', kind: 'dir' },
    { volumeSlug: 'api-env', label: 'API env', kind: 'dir' },
    { volumeSlug: 'hello-api-2-data', label: '', kind: 'dir' },
    { volumeSlug: 'pg-data', label: 'PostgreSQL data', kind: 'dir' },
    { volumeSlug: 'cassandra-data', label: 'Cassandra data', kind: 'dir' },
  ],
  db: [{ volumeSlug: 'pg-data', label: 'PostgreSQL data', kind: 'dir' }],
  northwind: [
    { volumeSlug: 'nodered-data', label: 'Node-RED data', kind: 'dir' },
    { volumeSlug: 'admin-runtime', label: 'Admin runtime', kind: 'file' },
  ],
};
function info(site: string): DevToolsInfo {
  const off = knob('opm.mock.devtoolsOff') === '1';
  return {
    siteSlug: site,
    enabled: !off,
    reason: off ? 'Dev tools are switched off on this site (agent.json "tools": {"devTools": false}).' : null,
    configPath: site === 'app' ? '/var/lib/opm/agent.json' : site === 'db' ? '/opt/opm-db/agent.json' : '/srv/opm/agent.json',
    limits: L,
    kinds: [...DEVTOOL_KINDS],
    containers: CONTAINERS[site] ?? [],
    volumes: VOLUMES[site] ?? [],
    secrets: ['api-token', 'pg-password', 'redis-password', 'mqtt-password', 'session-key'],
    variables: ['theme', 'db_user', 'api_base', 'mqtt_user'],
  };
}

// ───────────────────────────── connections
interface Conn {
  c: DevToolConnection;
  secret: { password?: string; token?: string };
}
const conns: Record<string, Conn[]> = { app: [], db: [], northwind: [] };
function mk(site: string, x: Partial<DevToolConnection> & Pick<DevToolConnection, 'id' | 'name' | 'kind' | 'target' | 'source'>, secret: Conn['secret'] = {}): Conn {
  const c: DevToolConnection = {
    siteSlug: site,
    presetRevSeq: null,
    description: '',
    auth: defaultDevToolAuth(x.kind),
    options: defaultDevToolOptions(),
    readOnly: false,
    createdBy: null,
    createdAt: null,
    updatedAt: null,
    resolvedTarget: null,
    state: { status: 'idle', connectedAt: null, lastUsedAt: null, lastError: null, serverVersion: null, runningQueries: 0 },
    problem: null,
    ...x,
  };
  return { c, secret };
}
(function seed() {
  const T = now();
  conns.app = [
    mk('app', { id: 'p:app-db', source: 'preset', name: 'App database', description: 'The northwind PostgreSQL of this site (from the deployment template).', kind: 'postgres', target: { kind: 'container', containerSlug: 'postgres', portName: 'pg' }, auth: { ...defaultDevToolAuth('postgres'), username: 'opm', password: { kind: 'expr', expr: '{{ secret.pg-password }}' }, database: 'northwind' }, state: { status: 'connected', connectedAt: iso(T - 3_600_000), lastUsedAt: iso(T - 120_000), lastError: null, serverVersion: 'PostgreSQL 16.4', runningQueries: 0 }, resolvedTarget: 'postgres → 172.18.0.4:5432' }),
    mk('app', { id: 'p:cache', source: 'preset', name: 'Cache', description: 'Redis used by hello-api for sessions and queues.', kind: 'redis', target: { kind: 'container', containerSlug: 'redis', portName: 'redis' }, auth: { ...defaultDevToolAuth('redis'), password: { kind: 'expr', expr: '{{ secret.redis-password }}' } }, resolvedTarget: 'redis → 172.18.0.5:6379' }),
    mk('app', { id: 'p:broker', source: 'preset', name: 'Broker', description: 'The site MQTT broker.', kind: 'mqtt', target: { kind: 'container', containerSlug: 'mqtt', portName: 'mqtt' }, auth: { ...defaultDevToolAuth('mqtt'), username: '{{ var.mqtt_user }}', password: { kind: 'expr', expr: '{{ secret.mqtt-password }}' } } }),
    mk('app', { id: 'p:api', source: 'preset', name: 'Hello API', description: 'hello-api over the site network.', kind: 'http', target: { kind: 'container', containerSlug: 'hello-api', portName: 'http' }, auth: { ...defaultDevToolAuth('http'), httpScheme: 'bearer', token: { kind: 'expr', expr: '{{ secret.api-token }}' } }, options: { ...defaultDevToolOptions(), basePath: '/api', defaultHeaders: [{ name: 'Accept', value: 'application/json' }] } }),
    mk('app', { id: 'p:reports', source: 'preset', name: 'Reports DB', description: '', kind: 'postgres', target: { kind: 'container', containerSlug: 'reports-db', portName: 'pg' }, auth: { ...defaultDevToolAuth('postgres'), username: 'reports' }, problem: 'container reports-db is not on site app in the head revision (removed?)' }),
    mk('app', { id: 'c_01J9DEVSQLITE00000000000001', source: 'manual', name: 'API app.db', description: 'SQLite file of hello-api', kind: 'sqlite', target: { kind: 'volume-file', volumeSlug: 'hello-api-data', path: 'app.db' }, createdBy: 'mira@acme.example', createdAt: iso(T - 5 * 86_400_000), updatedAt: iso(T - 5 * 86_400_000), resolvedTarget: 'app.db (volume hello-api-data)' }),
    mk('app', { id: 'c_01J9DEVANALYTICS0000000001', source: 'manual', name: 'Analytics (read-only)', description: 'Reporting replica on the db server', kind: 'postgres', target: { kind: 'host', host: '10.0.1.12', port: 5432 }, auth: { ...defaultDevToolAuth('postgres'), username: 'analyst', password: { kind: 'literal', value: MASK }, database: 'northwind' }, readOnly: true, createdBy: 'tester@acme.example', createdAt: iso(T - 2 * 86_400_000), updatedAt: iso(T - 86_400_000) }, { password: 'An4lyst-s3cret!' }),
    mk('app', { id: 'c_01J9DEVOLDREPLICA00000001', source: 'manual', name: 'Old replica', description: 'Decommissioned — times out', kind: 'postgres', target: { kind: 'host', host: '10.0.1.11', port: 5432 }, auth: { ...defaultDevToolAuth('postgres'), username: 'analyst' }, readOnly: true, createdBy: 'ravi@acme.example', createdAt: iso(T - 9 * 86_400_000), updatedAt: iso(T - 9 * 86_400_000), state: { status: 'error', connectedAt: null, lastUsedAt: iso(T - 7_200_000), lastError: 'timeout after 10 s', serverVersion: null, runningQueries: 0 } }),
    mk('app', { id: 'c_01J9DEVCASSANDRA000000001', source: 'manual', name: 'Telemetry (Cassandra)', description: '', kind: 'cassandra', target: { kind: 'container', containerSlug: 'cassandra', portName: 'cql' }, auth: { ...defaultDevToolAuth('cassandra'), database: 'telemetry' }, createdBy: 'mira@acme.example', createdAt: iso(T - 3 * 86_400_000), updatedAt: iso(T - 3 * 86_400_000) }),
    mk('app', { id: 'c_01J9DEVHTTPBIN00000000001', source: 'manual', name: 'Public status API', description: '', kind: 'http', target: { kind: 'url', url: 'https://status.acme.example' }, auth: { ...defaultDevToolAuth('http'), httpScheme: 'bearer', token: { kind: 'literal', value: MASK } }, createdBy: 'tester@acme.example', createdAt: iso(T - 86_400_000), updatedAt: iso(T - 86_400_000) }, { token: 'sk_live_mock_7f3a9c' }),
  ];
  conns.db = [mk('db', { id: 'p:db-cache', source: 'preset', name: 'DB cache', kind: 'redis', target: { kind: 'container', containerSlug: 'redis', portName: 'redis' } }), mk('db', { id: 'p:main-pg', source: 'preset', name: 'Main PostgreSQL', kind: 'postgres', target: { kind: 'container', containerSlug: 'postgres', portName: 'pg' }, auth: { ...defaultDevToolAuth('postgres'), username: 'opm', password: { kind: 'expr', expr: '{{ secret.pg-password }}' }, database: 'northwind' } })];
  // §21 #103: a preset whose password is a static Value (MASK in reads, Reveal returns it)
  conns.northwind = [mk('northwind', { id: 'p:broker', source: 'preset', name: 'Northwind broker', kind: 'mqtt', target: { kind: 'container', containerSlug: 'mqtt', portName: 'http' }, auth: { ...defaultDevToolAuth('mqtt'), username: 'northwind', password: { kind: 'literal', value: MASK } } }, { password: 'northwind-Mqtt-Static-103' })];
})();
function listConns(site: string, headSeq: number | null): DevToolConnection[] {
  const list = conns[site] ?? [];
  return [...list.filter((x) => x.c.source === 'preset'), ...list.filter((x) => x.c.source === 'manual')].map((x) => ({ ...x.c, presetRevSeq: x.c.source === 'preset' ? headSeq : null }));
}
function connOf(site: string, id: string, fail: Ctx['fail']): Conn {
  const c = (conns[site] ?? []).find((x) => x.c.id === id);
  if (!c) fail('not-found', `connection ${id} does not exist on site ${site}`);
  return c!;
}
function resolved(site: string, c: DevToolConnection): string {
  const t = c.target;
  if (t.kind === 'container') {
    const ct = (CONTAINERS[site] ?? []).find((x) => x.containerSlug === t.containerSlug);
    const p = ct?.ports.find((x) => x.portName === t.portName);
    const ip = `172.18.0.${4 + ((CONTAINERS[site] ?? []).indexOf(ct!) % 9)}`;
    return `${t.containerSlug} → ${ip}:${p?.containerPort ?? '?'}`;
  }
  if (t.kind === 'volume-file') return `${t.path} (volume ${t.volumeSlug})`;
  if (t.kind === 'container-file') return `${t.path} in ${t.containerSlug}`;
  return devToolTargetText(t);
}
function versionOf(kind: DevToolKind) {
  return kind === 'postgres' ? 'PostgreSQL 16.4' : kind === 'sqlite' ? 'SQLite 3.46.1' : kind === 'cassandra' ? 'Cassandra 4.1.5' : kind === 'redis' ? 'Redis 7.2.5' : kind === 'mqtt' ? 'Mosquitto 2.0.18 (MQTT 3.1.1)' : 'HTTP/1.1';
}
function checkInput(site: string, b: DevToolConnectionInput, fail: Ctx['fail']) {
  const errors: { path: string; code: string; message: string; severity: 'error' }[] = [];
  const e = (path: string, code: string, message: string) => errors.push({ path, code, message, severity: 'error' });
  if (!b.name?.trim()) e('name', 'V_DEVTOOL_NAME', 'Name is required (1–80 characters).');
  if (!DEVTOOL_KINDS.includes(b.kind)) e('kind', 'V_DEVTOOL_KIND', 'Unknown kind.');
  const t = b.target;
  if (t?.kind === 'container') {
    const ct = (CONTAINERS[site] ?? []).find((x) => x.containerSlug === t.containerSlug);
    if (!ct) e('target.containerSlug', 'V_DEVTOOL_CONTAINER', `Pick a container of site ${site}.`);
    else if (!ct.ports.find((p) => p.portName === t.portName)) e('target.portName', 'V_DEVTOOL_PORT', `Pick a port of ${t.containerSlug}.`);
  } else if (t?.kind === 'host') {
    if (!t.host?.trim() || /\{\{/.test(t.host)) e('target.host', 'V_DEVTOOL_TARGET', 'Enter a host name or IP (no {{ }}).');
    if (!(t.port >= 1 && t.port <= 65535)) e('target.port', 'V_DEVTOOL_TARGET', 'Port 1–65535.');
  } else if (t?.kind === 'url') {
    if (!/^https?:\/\/[^\s]+$/.test(t.url ?? '')) e('target.url', 'V_DEVTOOL_TARGET', 'An absolute http(s):// URL.');
  } else if (t?.kind === 'volume-file') {
    if (!(VOLUMES[site] ?? []).find((v) => v.volumeSlug === t.volumeSlug)) e('target.volumeSlug', 'V_DEVTOOL_VOLUME', 'Pick a declared volume of the site.');
    if (!t.path || t.path.startsWith('/') || t.path.split('/').includes('..')) e('target.path', 'V_DEVTOOL_VOLUME', 'A relative path inside the volume (no "..").');
  } else if (t?.kind === 'container-file') {
    const ct = (CONTAINERS[site] ?? []).find((x) => x.containerSlug === t.containerSlug);
    if (!ct) e('target.containerSlug', 'V_DEVTOOL_CONTAINER', 'Pick a container.');
    else if (!t.path?.startsWith('/')) e('target.path', 'V_DEVTOOL_TARGET', 'An absolute path inside the container.');
    else if (!ct.mounts.some((m) => t.path === m.mountPath || t.path.startsWith(m.mountPath.replace(/\/$/, '') + '/'))) e('target.path', 'V_DEVTOOL_TARGET', `${t.path} is not on a mount of ${ct.containerSlug} (container-local storage cannot be opened from outside).`);
  }
  if (b.kind === 'postgres' && !b.auth?.username?.trim()) e('auth.username', 'V_DEVTOOL_AUTH', 'PostgreSQL needs a user name.');
  if (b.kind === 'redis' && b.auth?.database && !/^([0-9]|1[0-5])$/.test(b.auth.database)) e('auth.database', 'V_DEVTOOL_AUTH', 'Redis database 0–15.');
  if (b.options?.timeoutSec !== null && b.options?.timeoutSec !== undefined && !(b.options.timeoutSec >= 1 && b.options.timeoutSec <= 3600)) e('options.timeoutSec', 'V_DEVTOOL_OPTION', 'Timeout 1–3600 s.');
  if (b.options?.basePath && !b.options.basePath.startsWith('/')) e('options.basePath', 'V_DEVTOOL_OPTION', 'Base path starts with "/".');
  if (errors.length) fail('invalid', 'The connection is not valid', { errors });
}
function testOf(site: string, kind: DevToolKind, c: Pick<DevToolConnection, 'target' | 'kind'>) {
  const target = c.target;
  const bad = (target.kind === 'host' && /^(10\.0\.1\.11|.*\.invalid)$/.test(target.host)) || (target.kind === 'container' && !(CONTAINERS[site] ?? []).find((x) => x.containerSlug === target.containerSlug)?.running);
  const r = resolved(site, { ...(c as DevToolConnection) });
  if (bad) return { ok: false, latencyMs: 10_000, serverVersion: null, resolvedTarget: r, message: target.kind === 'host' ? `dial tcp ${target.host}:${target.port}: i/o timeout (10 s)` : 'container is not running' };
  return { ok: true, latencyMs: 2 + Math.round(Math.random() * 9), serverVersion: versionOf(kind), resolvedTarget: r, message: 'connected' };
}
function touch(site: string, x: Conn, ok = true, err: string | null = null) {
  const s = x.c.state;
  s.lastUsedAt = iso();
  if (ok) {
    if (s.status !== 'connected') s.connectedAt = iso();
    s.status = 'connected';
    s.serverVersion = versionOf(x.c.kind);
    s.lastError = null;
    x.c.resolvedTarget ??= resolved(site, x.c);
  } else {
    s.status = 'error';
    s.lastError = err;
  }
}

// ───────────────────────────── history (one row per devtools.query activity)
const history: Record<string, DevToolHistoryItem[]> = { app: [], db: [], northwind: [] };
(function seedHistory() {
  const T = now();
  const rows: [string, string, DevToolKind, DevToolOp, string, boolean, number | null, number | null, string | null, string][] = [
    ['p:app-db', 'App database', 'postgres', 'sql', 'SELECT id, email, role FROM users WHERE active ORDER BY created_at DESC LIMIT 50', true, 50, null, null, 'mira@acme.example'],
    ['p:app-db', 'App database', 'postgres', 'explain', 'SELECT * FROM readings WHERE device_id = $1', true, null, null, null, 'mira@acme.example'],
    ['p:cache', 'Cache', 'redis', 'redis', 'HGETALL user:7', true, 5, null, null, 'tester@acme.example'],
    ['p:app-db', 'App database', 'postgres', 'sql', "UPDATE users SET role = 'operator' WHERE id = 12", true, 1, null, null, 'ravi@acme.example'],
    ['p:api', 'Hello API', 'http', 'http', 'GET http://hello-api:3000/api/health\nAuthorization: Bearer ••••••', true, null, 200, null, 'tester@acme.example'],
    ['c_01J9DEVOLDREPLICA00000001', 'Old replica', 'postgres', 'sql', 'SELECT count(*) FROM readings', false, null, null, 'timeout after 10 s', 'tester@acme.example'],
    ['p:broker', 'Broker', 'mqtt', 'mqtt-publish', 'PUBLISH northwind/config/thresholds qos 1 retain (41 bytes)', true, null, null, null, 'mira@acme.example'],
    ['p:app-db', 'App database', 'postgres', 'sql', 'DROP TABLE tmp_import', true, null, null, null, 'ravi@acme.example'],
    ['c_01J9DEVSQLITE00000000000001', 'API app.db', 'sqlite', 'sql', "SELECT * FROM jobs WHERE status = 'failed'", true, 9, null, null, 'tester@acme.example'],
    ['p:cache', 'Cache', 'redis', 'redis', 'AUTH ••••••', true, null, null, null, 'ravi@acme.example'],
  ];
  rows.forEach(([cid, cn, kind, op, st, ok, n, status, err, actor], i) => history.app!.push({ activityId: `app:h${i}`, ts: iso(T - (i + 1) * 1_380_000), actor, connectionId: cid, connectionName: cn, kind, op, statement: st, ok, durationMs: 3 + ((i * 37) % 900), rows: n, status, error: err, via: null }));
})();
function logQuery(ctx: Ctx, site: string, x: Conn, actor: string, op: DevToolOp, statement: string, ok: boolean, durationMs: number, rows: number | null, status: number | null, error: string | null, extra: { destructive?: boolean; confirmed?: boolean; queryId: string; statements?: number; rowsAffected?: number | null }) {
  const red = op === 'http' ? { text: statement, truncated: false } : redactStatement(x.c.kind, statement);
  const id = `${site}:dq${++idn}`;
  history[site] ??= [];
  history[site]!.unshift({ activityId: id, ts: iso(), actor, connectionId: x.c.id, connectionName: x.c.name, kind: x.c.kind, op, statement: red.text, ok, durationMs, rows, status, error, via: null });
  ctx.addActivity(site, 'devtools.query', `${x.c.name}: ${op} ${ok ? 'ok' : 'failed'}`, actor, {
    connectionId: x.c.id,
    connectionName: x.c.name,
    kind: x.c.kind,
    source: x.c.source,
    target: x.c.resolvedTarget ?? devToolTargetText(x.c.target),
    op,
    statement: red.text,
    statementTruncated: red.truncated,
    statements: extra.statements ?? 1,
    durationMs,
    rows,
    rowsAffected: extra.rowsAffected ?? null,
    status,
    ok,
    error,
    readOnly: x.c.readOnly,
    destructive: !!extra.destructive,
    confirmed: !!extra.confirmed,
    queryId: extra.queryId,
  });
  return id;
}

// ───────────────────────────── results buffer
type Buf = { kind: 'rows'; columns: ResultColumn[]; rows: unknown[][]; truncated: boolean } | { kind: 'body'; bytes: Uint8Array; contentType: string };
const results = new Map<string, Buf>();
const running = new Map<string, { cancel: () => void; conn: Conn; site: string }>();
function cellOut(v: unknown, row: number, col: number): CellValue {
  if (typeof v === 'string') {
    const n = new TextEncoder().encode(v).length;
    if (n > L.maxCellBytes) return { $truncated: true, preview: v.slice(0, 2000), sizeBytes: n, row, col };
  }
  return v as CellValue;
}
function pageOf(rows: unknown[][], offset: number) {
  return rows.map((r, i) => r.map((v, j) => cellOut(v, offset + i, j)));
}

// ───────────────────────────── saved + environments
const saved: Record<string, DevToolSaved[]> = { app: [], db: [], northwind: [] };
(function seedSaved() {
  const T = iso(now() - 86_400_000 * 3);
  const s = (kind: DevToolKind, collection: string, name: string, content: string, connectionId: string | null = null, by = 'mira@acme.example'): DevToolSaved => ({ id: mid('s_'), kind, connectionId, collection, name, description: '', content, createdBy: by, createdAt: T, updatedAt: T });
  saved.app = [
    s('postgres', 'Users', 'Recent sign-ups', 'SELECT id, email, name, created_at\nFROM users\nORDER BY created_at DESC\nLIMIT 100;'),
    s('postgres', 'Users', 'Admins', "SELECT * FROM users WHERE role = 'admin';"),
    s('postgres', 'Reports', 'Devices per site', 'SELECT site, count(*) AS devices, avg(battery) AS avg_battery\nFROM devices\nGROUP BY site\nORDER BY devices DESC;', 'p:app-db'),
    s('sqlite', '', 'Failed jobs', "SELECT * FROM jobs WHERE status = 'failed' ORDER BY id DESC;"),
    s('redis', 'Ops', 'Feature flags', 'GET config:feature-flags'),
    s('http', 'API', 'Health', JSON.stringify({ method: 'GET', url: '/health', query: [], headers: [], body: { kind: 'none' }, auth: null, followRedirects: null, tlsInsecure: null, timeoutSec: null } satisfies HttpRequestSpec)),
    s('http', 'API', 'Create user', JSON.stringify({ method: 'POST', url: '/users', query: [], headers: [{ name: 'Content-Type', value: 'application/json', enabled: true }], body: { kind: 'json', text: '{\n  "email": "{{email}}",\n  "name": "New user"\n}' }, auth: null, followRedirects: null, tlsInsecure: null, timeoutSec: null } satisfies HttpRequestSpec)),
    s('mqtt', 'Config', 'Thresholds', JSON.stringify({ topic: 'northwind/config/thresholds', payload: '{"pm25":60,"pm10":100,"noise":75}', encoding: 'json', qos: 1, retain: true })),
  ];
})();
interface Env {
  e: DevToolEnvironment;
  secrets: Record<string, string>;
}
const envs: Record<string, Env[]> = { app: [], db: [], northwind: [] };
envs.app = [
  { e: { id: 'e_01J9DEVLOCAL000000000000001', name: 'Local', variables: [{ name: 'userId', value: '7', secret: false, enabled: true }, { name: 'email', value: 'new.user@acme.example', secret: false, enabled: true }, { name: 'apiKey', value: MASK, secret: true, enabled: true }], createdBy: 'mira@acme.example', updatedAt: iso(now() - 86_400_000) }, secrets: { apiKey: 'ak_local_93jf02' } },
  { e: { id: 'e_01J9DEVSTAGING0000000000001', name: 'Staging', variables: [{ name: 'userId', value: '1', secret: false, enabled: true }, { name: 'email', value: 'qa@acme.example', secret: false, enabled: true }], createdBy: 'tester@acme.example', updatedAt: iso(now() - 2 * 86_400_000) }, secrets: {} },
];
const revealRate = new Map<string, number[]>();
function rateReveal(fail: Ctx['fail']) {
  const k = 'all';
  const list = (revealRate.get(k) ?? []).filter((t) => t > now() - 60_000);
  if (list.length >= L.revealPerMinute) fail('rate-limited', `too many reveals; try again in ${Math.ceil((list[0]! + 60_000 - now()) / 1000)} s`, { retryAfterSec: Math.ceil((list[0]! + 60_000 - now()) / 1000) });
  list.push(now());
  revealRate.set(k, list);
}

// ───────────────────────────── HTTP fake server
const PNG = 'iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAK0lEQVR4nGMQifL+TwlmABGq+TFk4eFswP8TNljxqAH0NGDg0wH9DaAEAwAKywjnXeL7uQAAAABJRU5ErkJggg==';
function b64(bytes: Uint8Array) {
  let s = '';
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(s);
}
function unb64(s: string) {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}
function baseUrl(site: string, c: DevToolConnection): string {
  const t = c.target;
  const sch = c.options.scheme ?? 'http';
  let b = '';
  if (t.kind === 'url') b = t.url;
  else if (t.kind === 'container') {
    const p = (CONTAINERS[site] ?? []).find((x) => x.containerSlug === t.containerSlug)?.ports.find((x) => x.portName === t.portName);
    b = `${sch}://${t.containerSlug}:${p?.containerPort ?? 80}`;
  } else if (t.kind === 'host') b = `${sch}://${t.host}:${t.port}`;
  return b.replace(/\/$/, '') + (c.options.basePath ?? '');
}
const resolveOpm = (s: string, mask: boolean) => s.replace(/\{\{\s*(secret|var)\.([\w-]+)\s*\}\}/g, (_m, r: string, n: string) => (r === 'secret' ? (mask ? '••••••' : `mock-${n}-value`) : n === 'api_base' ? 'https://api.acme.example' : n === 'mqtt_user' ? 'northwind' : `value-of-${n}`));
async function httpExec(site: string, x: Conn, b: ExecuteHttpRequest, signal: { cancelled: boolean }, fail: Ctx['fail']): Promise<ExecuteHttpResponse> {
  const spec = b.request;
  const env = (envs[site] ?? []).find((e) => e.e.id === b.environmentId);
  const vars: Record<string, string> = {};
  if (env) for (const v of env.e.variables) if (v.enabled) vars[v.name] = v.secret ? (env.secrets[v.name] ?? '') : String(v.value);
  Object.assign(vars, b.variables ?? {});
  const sub = (s: string, mask = false) => resolveOpm(applyEnvironment(s, vars), mask);
  let url = sub(spec.url.trim());
  if (!/^https?:\/\//i.test(url)) url = baseUrl(site, x.c) + (url.startsWith('/') || !url ? url : `/${url}`);
  const q = spec.query.filter((k) => k.enabled && k.name);
  if (q.length) url += (url.includes('?') ? '&' : '?') + q.map((k) => `${encodeURIComponent(sub(k.name))}=${encodeURIComponent(sub(k.value))}`).join('&');
  let u: URL;
  try {
    u = new URL(url);
  } catch {
    fail('invalid', `invalid URL: ${url}`);
  }
  if (/^\/_opm\/(api|peer)(\/|$)/.test(u!.pathname)) fail('invalid', 'refused: /_opm/api/* and /_opm/peer/* are the agent’s own API');
  if (x.c.readOnly && !(HTTP_READ_METHODS as readonly string[]).includes(spec.method)) fail('forbidden', `read-only connection: only ${HTTP_READ_METHODS.join(' / ')} are allowed`);
  const headers: [string, string][] = [];
  const masked: [string, string][] = [];
  for (const h of x.c.options.defaultHeaders) {
    headers.push([h.name, sub(h.value)]);
    masked.push([h.name, sub(h.value, true)]);
  }
  for (const h of spec.headers)
    if (h.enabled && h.name) {
      headers.push([sub(h.name), sub(h.value)]);
      masked.push([sub(h.name), /authorization|key|token|cookie/i.test(h.name) ? '••••••' : sub(h.value, true)]);
    }
  const auth = spec.auth ?? { scheme: x.c.auth.httpScheme, username: x.c.auth.username, password: null, token: null, apiKeyHeader: x.c.auth.apiKeyHeader, apiKeyIn: 'header' as const };
  if (auth.scheme === 'bearer') {
    headers.push(['Authorization', `Bearer ${spec.auth ? sub(auth.token ?? '') : 'conn-token'}`]);
    masked.push(['Authorization', 'Bearer ••••••']);
  } else if (auth.scheme === 'basic') {
    headers.push(['Authorization', `Basic ${btoa(`${sub(auth.username ?? '')}:${sub(auth.password ?? '')}`)}`]);
    masked.push(['Authorization', 'Basic ••••••']);
  } else if (auth.scheme === 'api-key') {
    if (auth.apiKeyIn === 'query') u!.searchParams.set(auth.apiKeyHeader ?? 'api_key', 'k');
    else {
      headers.push([auth.apiKeyHeader ?? 'X-API-Key', sub(auth.token ?? '')]);
      masked.push([auth.apiKeyHeader ?? 'X-API-Key', '••••••']);
    }
  }
  let reqBody = '';
  const bd = spec.body;
  if (bd.kind === 'json') {
    reqBody = sub(bd.text);
    if (!headers.some(([k]) => k.toLowerCase() === 'content-type')) (headers.push(['Content-Type', 'application/json']), masked.push(['Content-Type', 'application/json']));
  } else if (bd.kind === 'text') reqBody = sub(bd.text);
  else if (bd.kind === 'form') reqBody = bd.fields.filter((f) => f.enabled).map((f) => `${encodeURIComponent(f.name)}=${encodeURIComponent(sub(f.value))}`).join('&');
  else if (bd.kind === 'multipart') reqBody = bd.fields.filter((f) => f.enabled).map((f) => `--x\r\nContent-Disposition: form-data; name="${f.name}"${f.fileName ? `; filename="${f.fileName}"` : ''}\r\n\r\n${f.base64 ? `(${Math.round((f.value.length * 3) / 4)} bytes)` : sub(f.value)}`).join('\r\n');
  else if (bd.kind === 'binary') reqBody = atob(bd.base64);
  const t0 = now();
  const https = u!.protocol === 'https:';
  const redirects: { status: number; url: string }[] = [];
  let path = u!.pathname;
  const follow = spec.followRedirects ?? x.c.options.followRedirects ?? true;
  if (path.endsWith('/redirect') && follow) {
    redirects.push({ status: 302, url: u!.origin + path.replace(/redirect$/, 'old-health') }, { status: 301, url: u!.origin + path.replace(/redirect$/, 'health') });
    path = path.replace(/redirect$/, 'health');
  }
  const slow = path.endsWith('/slow') ? 4000 : 0;
  for (let w = 0; w < slow; w += 100) {
    if (signal.cancelled) fail('conflict', 'cancelled');
    await sleep(100);
  }
  if (/\.invalid$/.test(u!.hostname)) return { queryId: b.queryId, ok: false, error: `dial tcp: lookup ${u!.hostname}: no such host`, status: null, statusText: null, protocol: null, finalUrl: null, redirects: [], headers: [], cookies: [], contentType: null, sizeBytes: 0, headerBytes: 0, bodyText: null, bodyBase64: null, bodyTruncated: false, resultId: null, timing: { dnsMs: 3.1, connectMs: null, tlsMs: null, ttfbMs: null, downloadMs: null, totalMs: 3.4 }, sent: { method: spec.method, url: u!.toString(), headers: masked, bodyBytes: reqBody.length }, activityId: null };
  let status = 200;
  let ct = 'application/json; charset=utf-8';
  let body: Uint8Array;
  const cookies: ExecuteHttpResponse['cookies'] = [];
  const extraH: [string, string][] = [];
  const j = (v: unknown) => new TextEncoder().encode(JSON.stringify(v, null, 2));
  const m = spec.method;
  const userM = /\/users\/(\d+)$/.exec(path);
  if (path.endsWith('/health') || path.endsWith('/old-health')) body = j({ status: 'ok', service: 'hello-api', version: '1.4.2', uptimeSec: 86_211, checks: { db: 'ok', redis: 'ok', mqtt: 'ok' } });
  else if (path.endsWith('/users') && m === 'GET') {
    const lim = Number(u!.searchParams.get('limit') ?? 5);
    body = j({ items: Array.from({ length: Math.min(lim, 50) }, (_, i) => ({ id: i + 1, email: `user${i + 1}@acme.example`, role: i % 4 ? 'viewer' : 'admin', active: i % 6 !== 0 })), next: null });
  } else if (path.endsWith('/users') && (m === 'POST' || m === 'PUT')) {
    status = 201;
    let parsed: unknown = reqBody;
    try {
      parsed = JSON.parse(reqBody);
    } catch {
      /* text */
    }
    body = j({ id: 2501, created: true, received: parsed });
    extraH.push(['Location', `${u!.origin}${path}/2501`]);
  } else if (userM && m === 'GET') {
    if (Number(userM[1]) > 2500) ((status = 404), (body = j({ error: { code: 'not-found', message: `user ${userM[1]} does not exist` } })));
    else body = j({ id: Number(userM[1]), email: `user${userM[1]}@acme.example`, name: 'Asha Shah', role: 'operator', profile: { theme: 'dark', notifications: { email: true } } });
  } else if (userM && m === 'DELETE') {
    status = 204;
    body = new Uint8Array();
    ct = '';
  } else if (path.endsWith('/login') && m === 'POST') {
    body = j({ ok: true, user: { id: 7 } });
    cookies.push({ name: 'session', value: 'eyJhbGciOi…', domain: u!.hostname, path: '/', expires: iso(now() + 86_400_000), httpOnly: true, secure: https }, { name: 'theme', value: 'dark', domain: null, path: '/', expires: null, httpOnly: false, secure: false });
    extraH.push(['Set-Cookie', 'session=eyJhbGciOi…; Path=/; HttpOnly; SameSite=Lax'], ['Set-Cookie', 'theme=dark; Path=/']);
  } else if (path === '/' || path.endsWith('/html') || path.endsWith('/index.html')) {
    ct = 'text/html; charset=utf-8';
    body = new TextEncoder().encode('<!doctype html><html><head><title>Hello</title><style>body{font-family:sans-serif;padding:24px;color:#333}h1{color:#4f46e5}</style></head><body><h1>Hello from hello-web</h1><p>Served by the site server.</p><script>document.body.append(" (script ran in the sandbox)")</script></body></html>');
  } else if (path.endsWith('.png')) {
    ct = 'image/png';
    body = unb64(PNG);
  } else if (path.endsWith('/bin')) {
    ct = 'application/octet-stream';
    body = new Uint8Array(Array.from({ length: 300 }, (_, i) => (i * 37) % 256));
  } else if (path.endsWith('/big')) {
    ct = 'text/plain; charset=utf-8';
    body = new TextEncoder().encode(Array.from({ length: 50_000 }, (_, i) => `${String(i).padStart(6, '0')} lorem ipsum dolor sit amet consectetur`).join('\n'));
  } else if (path.endsWith('/echo')) {
    body = j({ method: m, url: u!.toString(), query: Object.fromEntries(u!.searchParams), headers: Object.fromEntries(headers.map(([k, v]) => [k, /authorization/i.test(k) ? '(sent)' : v])), body: reqBody || null });
  } else if (path.endsWith('/slow')) body = j({ slow: true, waitedMs: slow });
  else {
    status = 404;
    body = j({ error: { code: 'not-found', message: `no route ${m} ${path}` } });
  }
  const ttfb = 8 + Math.round(Math.random() * 40) + slow;
  const inline = L.inlineResponseBytes;
  const text = ct.startsWith('text/') || ct.includes('json') || ct.includes('xml') || ct.includes('javascript');
  const truncated = body.length > inline;
  let resultId: string | null = null;
  if (truncated || !text) {
    resultId = mid('r_');
    results.set(resultId, { kind: 'body', bytes: body, contentType: ct || 'application/octet-stream' });
  }
  const hs: [string, string][] = [['Content-Type', ct], ['Content-Length', String(body.length)], ['Date', new Date().toUTCString()], ['Server', 'hello-api/1.4.2'], ['X-Request-Id', mid('')], ...extraH].filter(([, v]) => v !== '') as [string, string][];
  return {
    queryId: b.queryId,
    ok: true,
    error: null,
    status,
    statusText: { 200: 'OK', 201: 'Created', 204: 'No Content', 404: 'Not Found' }[status] ?? '',
    protocol: https ? 'HTTP/2.0' : 'HTTP/1.1',
    finalUrl: redirects.length ? redirects[redirects.length - 1]!.url : u!.toString(),
    redirects,
    headers: hs,
    cookies,
    contentType: ct || null,
    sizeBytes: body.length,
    headerBytes: hs.reduce((a, [k, v]) => a + k.length + v.length + 4, 0),
    bodyText: text ? new TextDecoder().decode(body.subarray(0, inline)) : null,
    bodyBase64: !text && body.length <= inline ? b64(body) : null,
    bodyTruncated: truncated,
    resultId,
    timing: { dnsMs: 1.2, connectMs: 0.9, tlsMs: https ? 14.6 : null, ttfbMs: ttfb, downloadMs: Math.round(body.length / 50_000) + 1, totalMs: Math.round((now() - t0 + ttfb + 3) * 10) / 10 },
    sent: { method: spec.method, url: u!.toString(), headers: masked, bodyBytes: new TextEncoder().encode(reqBody).length },
    activityId: null,
  };
}

// ───────────────────────────── router
let lastActor: string | null = null;
function sqlKindOf(k: DevToolKind): SQL.SqlKind | null {
  return k === 'postgres' || k === 'sqlite' || k === 'cassandra' ? k : null;
}
export async function devtoolsFetch(url: URL, init: RequestInit, ctx: Ctx): Promise<Response> {
  const m = /^\/_opm\/api\/sites\/([^/]+)\/devtools\/(.+)$/.exec(url.pathname);
  if (!m) return ctx.json(404, { error: { code: 'not-found', message: `mock: no devtools endpoint ${url.pathname}` } });
  const site = decodeURIComponent(m[1]!);
  const rest = m[2]!;
  const method = (init.method ?? 'GET').toUpperCase();
  const q = url.searchParams;
  const headers = new Headers(init.headers);
  let body: Record<string, unknown> = {};
  if (typeof init.body === 'string') {
    try {
      body = JSON.parse(init.body);
    } catch {
      /* none */
    }
  } else if (init.body instanceof Uint8Array) {
    try {
      body = JSON.parse(new TextDecoder().decode(init.body));
    } catch {
      /* none */
    }
  }
  await sleep(40 + Math.random() * 70);
  const { fail } = ctx;
  try {
    const s = ctx.requireSession();
    const actor = () => (lastActor = ctx.requireActor(headers, s));
    if (site === '*') fail('invalid', '"*" is not supported: a connection lives on one site');
    if (!SITES.includes(site)) fail('not-found', `site ${site} does not exist`);
    const ok = (v: unknown, st = 200) => ctx.json(st, v);
    let r: RegExpExecArray | null;
    if (method === 'GET' && rest === 'info') return ok(info(site));
    if (knob('opm.mock.devtoolsOff') === '1') fail('forbidden', `Dev tools are switched off on site ${site} (agent.json "tools": {"devTools": false}).`, { reason: 'agent.json tools.devTools is false', setting: 'tools.devTools' });
    // ── connections
    if (rest === 'connections' && method === 'GET') return ok({ items: listConns(site, ctx.headSeq()) });
    if (rest === 'connections' && method === 'POST') {
      const a = actor();
      const b = body as unknown as DevToolConnectionInput;
      checkInput(site, b, fail);
      const sec: Conn['secret'] = {};
      if (b.auth.password.kind === 'literal') sec.password = String(b.auth.password.value);
      if (b.auth.token.kind === 'literal') sec.token = String(b.auth.token.value);
      const c = mk(site, { ...b, id: mid('c_'), source: 'manual', createdBy: a, createdAt: iso(), updatedAt: iso(), auth: { ...b.auth, password: b.auth.password.kind === 'literal' ? { kind: 'literal', value: MASK } : b.auth.password, token: b.auth.token.kind === 'literal' ? { kind: 'literal', value: MASK } : b.auth.token } }, sec);
      conns[site]!.push(c);
      ctx.addActivity(site, 'devtools.connection-created', `Dev tools connection "${c.c.name}" created`, a, { connectionId: c.c.id, name: c.c.name, kind: c.c.kind, target: devToolTargetText(c.c.target), readOnly: c.c.readOnly });
      return ok(c.c, 201);
    }
    if (rest === 'connections/test' && method === 'POST') {
      const a = actor();
      const b = body as unknown as DevToolConnectionInput;
      checkInput(site, { ...b, name: b.name || 'draft' }, fail);
      const masked = (b.auth.password.kind === 'literal' && b.auth.password.value === MASK) || (b.auth.token.kind === 'literal' && b.auth.token.value === MASK);
      if (masked && !(conns[site] ?? []).some((y) => y.c.id === q.get('connectionId'))) fail('invalid', 'The stored credential is masked: pass ?connectionId= of the saved connection (or type the value again).');
      await sleep(250);
      const t = testOf(site, b.kind, b);
      ctx.addActivity(site, 'devtools.connection-tested', `Dev tools connection test ${t.ok ? 'ok' : 'failed'}`, a, { connectionId: null, name: b.name, kind: b.kind, ok: t.ok, latencyMs: t.latencyMs });
      return ok(t);
    }
    if ((r = /^connections\/([^/]+)(?:\/(.+))?$/.exec(rest))) {
      const id = decodeURIComponent(r[1]!);
      const sub = r[2] ?? '';
      const x = connOf(site, id, fail);
      const c = x.c;
      if (!sub && method === 'PUT') {
        const a = actor();
        if (c.source === 'preset') fail('conflict', 'A preset comes from the cloud configuration: duplicate it to edit locally.');
        const b = body as unknown as DevToolConnectionInput;
        checkInput(site, b, fail);
        let credentialChanged = false;
        if (b.auth.password.kind === 'literal') {
          if (b.auth.password.value !== MASK) ((x.secret.password = String(b.auth.password.value)), (credentialChanged = true));
        } else if (x.secret.password !== undefined) ((x.secret.password = undefined), (credentialChanged = true));
        if (b.auth.token.kind === 'literal') {
          if (b.auth.token.value !== MASK) ((x.secret.token = String(b.auth.token.value)), (credentialChanged = true));
        } else if (x.secret.token !== undefined) ((x.secret.token = undefined), (credentialChanged = true));
        Object.assign(c, { ...b, auth: { ...b.auth, password: b.auth.password.kind === 'literal' ? { kind: 'literal', value: MASK } : b.auth.password, token: b.auth.token.kind === 'literal' ? { kind: 'literal', value: MASK } : b.auth.token }, updatedAt: iso() });
        c.state = { status: 'idle', connectedAt: null, lastUsedAt: c.state.lastUsedAt, lastError: null, serverVersion: null, runningQueries: 0 };
        ctx.addActivity(site, 'devtools.connection-updated', `Dev tools connection "${c.name}" updated`, a, { connectionId: c.id, name: c.name, kind: c.kind, credentialChanged });
        return ok(c);
      }
      if (!sub && method === 'DELETE') {
        const a = actor();
        if (c.source === 'preset') fail('conflict', 'A preset cannot be deleted here: remove it from the cloud configuration.');
        conns[site] = conns[site]!.filter((y) => y !== x);
        ctx.addActivity(site, 'devtools.connection-deleted', `Dev tools connection "${c.name}" deleted`, a, { connectionId: c.id, name: c.name, kind: c.kind });
        return ok({ deleted: true });
      }
      if (sub === 'duplicate' && method === 'POST') {
        const a = actor();
        const name = String((body as { name?: string }).name ?? '').trim() || `${c.name} (copy)`;
        const d = mk(site, { ...JSON.parse(JSON.stringify(c)), id: mid('c_'), source: 'manual', name, presetRevSeq: null, problem: null, createdBy: a, createdAt: iso(), updatedAt: iso(), state: { status: 'idle', connectedAt: null, lastUsedAt: null, lastError: null, serverVersion: null, runningQueries: 0 } }, { ...x.secret });
        conns[site]!.push(d);
        ctx.addActivity(site, 'devtools.connection-created', `Dev tools connection "${name}" created (copy of ${c.name})`, a, { connectionId: d.c.id, name, kind: d.c.kind, duplicatedFrom: c.id });
        return ok(d.c, 201);
      }
      if (sub === 'test' && method === 'POST') {
        const a = actor();
        await sleep(200);
        const t = c.problem ? { ok: false, latencyMs: 0, serverVersion: null, resolvedTarget: null, message: c.problem } : testOf(site, c.kind, c);
        touch(site, x, t.ok, t.ok ? null : t.message);
        if (t.resolvedTarget) c.resolvedTarget = t.resolvedTarget;
        ctx.addActivity(site, 'devtools.connection-tested', `Dev tools connection "${c.name}" tested: ${t.ok ? 'ok' : 'failed'}`, a, { connectionId: c.id, name: c.name, kind: c.kind, ok: t.ok, latencyMs: t.latencyMs, error: t.ok ? null : t.message });
        return ok(t);
      }
      if (sub === 'disconnect' && method === 'POST') {
        actor();
        const was = c.state.status === 'connected';
        c.state = { ...c.state, status: 'idle', connectedAt: null, serverVersion: null, runningQueries: 0 };
        return ok({ closed: was });
      }
      if (sub === 'reveal' && method === 'POST') {
        const a = actor();
        const field = (q.get('field') ?? 'password') as 'password' | 'token';
        const v = x.secret[field];
        if (v === undefined) fail('invalid', `${c.name} has no stored ${field} (expressions are revealed on the Secrets page)`);
        rateReveal(fail);
        ctx.addActivity(site, 'devtools.credential-revealed', `Dev tools ${field} of "${c.name}" revealed`, a, { connectionId: c.id, name: c.name, field });
        return ctx.json(200, { value: v, hideAfterSec: REVEAL_SECONDS }, { 'Cache-Control': 'no-store' });
      }
      if (c.problem && sub !== 'schema') fail('invalid', `${c.name}: ${c.problem}`);
      // ── schema
      if (sub === 'schema' && method === 'GET') {
        const k = sqlKindOf(c.kind);
        if (!k) fail('invalid', `${c.kind} has no schema`);
        if (c.problem) fail('unavailable', c.problem);
        if (c.target.kind === 'host' && c.target.host === '10.0.1.11') {
          touch(site, x, false, 'timeout after 10 s');
          fail('unavailable', `${c.name}: dial tcp 10.0.1.11:5432: i/o timeout`);
        }
        touch(site, x);
        return ok(SQL.schemaOf(k!));
      }
      if (sub === 'schema/table' && method === 'GET') {
        const k = sqlKindOf(c.kind);
        const d = k ? SQL.tableDetailOf(k, q.get('namespace') ?? '', q.get('table') ?? '') : null;
        if (!d) fail('not-found', `table ${q.get('namespace')}.${q.get('table')} does not exist`);
        return ok(d);
      }
      // ── SQL
      if (sub === 'sql' && method === 'POST') {
        const a = actor();
        const k = sqlKindOf(c.kind);
        if (!k) fail('invalid', `${c.kind} does not run SQL`);
        const b = body as unknown as ExecuteSqlRequest;
        const stmts = splitSqlStatements(b.statement ?? '');
        if (!stmts.length) fail('invalid', 'nothing to run (empty statement)');
        if (b.explain && k === 'cassandra') fail('invalid', 'EXPLAIN is not supported by Cassandra');
        const classes = stmts.map((st) => classifyStatement(k!, st));
        const reasons = [...new Set(classes.flatMap((cl) => cl.reasons))];
        const destructive = reasons.length > 0;
        if (destructive && (b.confirm ?? '').trim() !== c.name.trim()) fail('invalid', 'Destructive statement: type the connection name to confirm', { confirmRequired: true, reasons });
        if (k === 'cassandra' && c.readOnly && classes.some((cl) => cl.write)) fail('forbidden', `${c.name} is read-only: write statements are refused`);
        if (c.target.kind === 'host' && c.target.host === '10.0.1.11') {
          touch(site, x, false, 'timeout after 10 s');
          fail('unavailable', `${c.name}: dial tcp 10.0.1.11:5432: i/o timeout`);
        }
        if (running.size >= L.maxConcurrent) fail('conflict', `${L.maxConcurrent} executions are already running on this site`);
        const db = SQL.dbOf(k!);
        const signal = { cancelled: false, wake: null as null | (() => void) };
        running.set(b.queryId, { conn: x, site, cancel: () => ((signal.cancelled = true), signal.wake?.()) });
        c.state.runningQueries++;
        const t0 = now();
        const snap = b.transaction ? SQL.snapshotDb(db) : null;
        const out: StatementResult[] = [];
        const maxRows = Math.min(b.maxRows ?? c.options.maxRows ?? L.maxRows, 10000);
        const timeoutMs = (b.timeoutSec ?? c.options.timeoutSec ?? L.statementTimeoutSec) * 1000;
        let failed = false;
        try {
          for (let i = 0; i < stmts.length; i++) {
            const st = b.explain ? `EXPLAIN ${b.explain === 'analyze' ? 'ANALYZE ' : ''}${stmts[i]}` : stmts[i]!;
            const s0 = now();
            if (failed) break;
            try {
              const res = await SQL.runStatement(db, st, {
                params: b.params ?? [],
                kind: k!,
                readOnly: c.readOnly,
                sleep: (ms) =>
                  new Promise<void>((res2, rej) => {
                    if (signal.cancelled) return rej(new SQL.SqlError('canceling statement due to user request', '57014'));
                    if (ms > timeoutMs) {
                      const tm = setTimeout(() => rej(new SQL.SqlError('canceling statement due to statement timeout', '57014')), timeoutMs);
                      signal.wake = () => (clearTimeout(tm), rej(new SQL.SqlError('canceling statement due to user request', '57014')));
                      return;
                    }
                    const tm = setTimeout(res2, ms);
                    signal.wake = () => (clearTimeout(tm), rej(new SQL.SqlError('canceling statement due to user request', '57014')));
                  }),
              });
              const all = res.rows;
              const buffered = all.slice(0, L.bufferRows);
              let resultId: string | null = null;
              if (res.type !== 'command' && all.length) {
                resultId = mid('r_');
                results.set(resultId, { kind: 'rows', columns: res.columns, rows: buffered, truncated: all.length > L.bufferRows });
              }
              out.push({ index: i, statement: stmts[i]!, ok: true, error: null, type: res.type, commandTag: res.commandTag, rowsAffected: res.rowsAffected, columns: res.columns, rows: pageOf(buffered.slice(0, maxRows), 0), totalRows: buffered.length, truncated: all.length > L.bufferRows, resultId, durationMs: Math.max(1, now() - s0 + Math.round(Math.random() * 6)), plan: res.plan ?? null, notices: res.notices ?? [] });
            } catch (e) {
              if (!(e instanceof SQL.SqlError)) throw e;
              failed = true;
              const shift = st.length - stmts[i]!.length;
              const pos = e.position === null ? null : e.position - shift > 0 ? e.position - shift : null;
              out.push({ index: i, statement: stmts[i]!, ok: false, error: { message: e.message, code: e.code, position: pos, detail: e.detail, hint: e.hint }, type: 'command', commandTag: null, rowsAffected: null, columns: [], rows: [], totalRows: 0, truncated: false, resultId: null, durationMs: Math.max(1, now() - s0), plan: null });
            }
          }
          if (failed && snap) SQL.restoreDb(db, snap);
        } finally {
          running.delete(b.queryId);
          c.state.runningQueries = Math.max(0, c.state.runningQueries - 1);
        }
        touch(site, x);
        const okAll = out.every((o) => o.ok);
        const dur = now() - t0;
        const actId = logQuery(ctx, site, x, a, b.explain ? 'explain' : k === 'cassandra' ? 'cql' : 'sql', stmts.join(';\n'), okAll, dur, out.reduce((n, o) => n + (o.type === 'rows' ? o.totalRows : 0), 0) || null, null, out.find((o) => !o.ok)?.error?.message ?? null, { destructive, confirmed: destructive, queryId: b.queryId, statements: stmts.length, rowsAffected: out.reduce((n, o) => n + (o.rowsAffected ?? 0), 0) || null });
        return ok({ queryId: b.queryId, connectionId: c.id, ok: okAll, durationMs: dur, results: out, activityId: actId });
      }
      if (sub === 'cancel' && method === 'POST') {
        const a = actor();
        const qid = String((body as { queryId?: string }).queryId ?? '');
        const run = running.get(qid);
        run?.cancel();
        ctx.addActivity(site, 'devtools.query-cancelled', `Dev tools query on "${c.name}" cancelled`, a, { connectionId: c.id, connectionName: c.name, kind: c.kind, queryId: qid, wasRunning: !!run });
        return ok({ cancelled: !!run });
      }
      // ── Redis
      if (sub === 'redis' && method === 'POST') {
        const a = actor();
        if (c.kind !== 'redis') fail('invalid', 'not a Redis connection');
        const b = body as unknown as ExecuteRedisRequest;
        const argv = b.command ?? (b.line !== undefined ? parseRedisCommandLine(b.line) : null);
        if (!argv) fail('invalid', 'unbalanced quotes in the command line');
        if (!argv!.length) fail('invalid', 'empty command');
        const cl = classifyRedis(argv!);
        if (cl.blocked) fail('invalid', `${argv![0]!.toUpperCase()} is blocked in the dev tools (it would block or take over the connection / the server).`, { blocked: true, command: argv![0]!.toUpperCase() });
        if (cl.destructive && (b.confirm ?? '').trim() !== c.name.trim()) fail('invalid', 'Destructive command: type the connection name to confirm', { confirmRequired: true, reasons: cl.reasons });
        if (c.readOnly && cl.write) fail('forbidden', `${c.name} is read-only: ${argv![0]!.toUpperCase()} is a write command`);
        const t0 = now();
        let reply = null;
        let error: string | null = null;
        try {
          reply = R.redisExec(argv!);
        } catch (e) {
          if (!(e instanceof R.RedisErr)) throw e;
          error = e.message;
        }
        touch(site, x);
        const dur = Math.max(1, now() - t0 + Math.round(Math.random() * 3));
        const line = argv!.map((w) => (w === '' || /[\s"']/.test(w) ? JSON.stringify(w) : w)).join(' ');
        const actId = logQuery(ctx, site, x, a, 'redis', line, !error, dur, null, null, error, { destructive: cl.destructive, confirmed: cl.destructive, queryId: b.queryId });
        return ok({ queryId: b.queryId, ok: !error, durationMs: dur, command: redactStatement('redis', line).text, reply: error ? { type: 'error', value: error } : reply, error, activityId: actId });
      }
      if (sub === 'redis/keys' && method === 'GET') {
        touch(site, x);
        return ok(R.redisScan(q.get('pattern') ?? '*', q.get('cursor') ?? '0', Number(q.get('count') ?? 200), q.get('type') || null));
      }
      if (sub === 'redis/key' && method === 'GET') {
        const v = R.redisKey(q.get('key') ?? '', q.get('cursor'), Number(q.get('count') ?? 100));
        if (!v) fail('not-found', `key ${q.get('key')} does not exist (expired or deleted)`);
        return ok(v);
      }
      if (sub === 'redis/info' && method === 'GET') return ok(R.redisInfo());
      // ── MQTT
      if (sub === 'mqtt/publish' && method === 'POST') {
        const a = actor();
        const b = body as unknown as MqttPublishRequest;
        if (c.readOnly) fail('forbidden', `${c.name} is read-only: publishing is refused`);
        if (!b.topic || /[+#]/.test(b.topic)) fail('invalid', 'A topic without wildcards (+ #) is required');
        let bytes: Uint8Array;
        try {
          bytes = b.encoding === 'base64' ? unb64(b.payload.replace(/\s+/g, '')) : b.encoding === 'hex' ? new Uint8Array((b.payload.replace(/\s+/g, '').match(/../g) ?? []).map((h) => { const n = parseInt(h, 16); if (Number.isNaN(n)) throw new Error('hex'); return n; })) : new TextEncoder().encode(b.payload);
          if (b.encoding === 'json' && b.payload.trim()) JSON.parse(b.payload);
        } catch {
          fail('invalid', `The payload is not valid ${b.encoding}`);
        }
        if (b.retain && bytes!.length === 0 && (b.confirm ?? '').trim() !== c.name.trim()) fail('invalid', 'Clearing a retained message: type the connection name to confirm', { confirmRequired: true, reasons: [`clear the retained message of ${b.topic}`] });
        M.publish(b.topic, bytes!, b.qos, b.retain);
        touch(site, x);
        ctx.addActivity(site, 'devtools.mqtt-published', `Published to ${b.topic} via "${c.name}"`, a, { connectionId: c.id, connectionName: c.name, topic: b.topic, qos: b.qos, retain: b.retain, bytes: bytes!.length, ok: true });
        const actId = logQuery(ctx, site, x, a, 'mqtt-publish', `PUBLISH ${b.topic} qos ${b.qos}${b.retain ? ' retain' : ''} (${bytes!.length} bytes)`, true, 3, null, null, null, { queryId: mid('q_') });
        return ok({ ok: true, durationMs: 3, bytes: bytes!.length, activityId: actId });
      }
      if (sub === 'mqtt/sessions' && method === 'POST') {
        const a = actor();
        const subs = ((body as { subscriptions?: MqttSubscription[] }).subscriptions ?? []).filter((x2) => x2.filter);
        if (!subs.length) fail('invalid', 'add at least one topic filter');
        const ss = M.openSession(site, c.id, subs);
        if (!ss) fail('conflict', 'at most 4 MQTT connections can be live at once in one GUI session: stop the live view of another connection first');
        touch(site, x);
        ctx.addActivity(site, 'devtools.mqtt-subscribed', `MQTT session on "${c.name}" (${subs.map((y) => y.filter).join(', ')})`, a, { connectionId: c.id, connectionName: c.name, sessionId: ss!.id, filters: subs.map((y) => y.filter) });
        logQuery(ctx, site, x, a, 'mqtt-subscribe', subs.map((y) => `${y.filter} (qos ${y.qos})`).join(', '), true, 4, null, null, null, { queryId: mid('q_') });
        return ok({ sessionId: ss!.id, ticket: ss!.ticket, streamPath: `/_opm/api/sites/${site}/devtools/connections/${encodeURIComponent(c.id)}/mqtt/sessions/${ss!.id}/stream?ticket=${ss!.ticket}`, expiresAt: iso(now() + 60_000) }, 201);
      }
      if ((r = /^mqtt\/sessions\/([^/]+)\/(subscribe|unsubscribe|close)$/.exec(sub)) && method === 'POST') {
        const a = actor();
        const ss = M.sessionOf(r[1]!);
        if (!ss || ss.closed) fail('not-found', 'the MQTT session has ended');
        if (r[2] === 'close') {
          M.closeSession(ss!.id);
          ctx.addActivity(site, 'devtools.mqtt-unsubscribed', `MQTT session on "${c.name}" closed`, a, { connectionId: c.id, sessionId: ss!.id, messages: ss!.messages });
          return ok({ closed: true });
        }
        if (r[2] === 'subscribe') {
          const f = body as unknown as MqttSubscription;
          if (!f.filter) fail('invalid', 'filter is required');
          ss!.subs = [...ss!.subs.filter((y) => y.filter !== f.filter), { filter: f.filter, qos: f.qos ?? 0 }];
          for (const rm of M.retainedFor(f.filter)) ss!.es?.push({ ...rm, retained: true, at: iso() });
          ctx.addActivity(site, 'devtools.mqtt-subscribed', `MQTT filter ${f.filter} added on "${c.name}"`, a, { connectionId: c.id, sessionId: ss!.id, filters: [f.filter] });
        } else ss!.subs = ss!.subs.filter((y) => y.filter !== String((body as { filter?: string }).filter));
        return ok({ subscriptions: ss!.subs });
      }
      if (sub === 'mqtt/topics' && method === 'GET') return ok({ items: M.topicsSeen() });
      // ── HTTP
      if (sub === 'http' && method === 'POST') {
        const a = actor();
        if (c.kind !== 'http') fail('invalid', 'not an HTTP connection');
        const b = body as unknown as ExecuteHttpRequest;
        const signal = { cancelled: false };
        running.set(b.queryId, { conn: x, site, cancel: () => (signal.cancelled = true) });
        c.state.runningQueries++;
        try {
          const res = await httpExec(site, x, b, signal, fail);
          touch(site, x);
          res.activityId = logQuery(ctx, site, x, a, 'http', redactHttpRequest(res.sent.method, res.sent.url, res.sent.headers, res.sent.bodyBytes).text, res.ok, Math.round(res.timing.totalMs), null, res.status, res.error, { queryId: b.queryId });
          return ok(res);
        } finally {
          running.delete(b.queryId);
          c.state.runningQueries = Math.max(0, c.state.runningQueries - 1);
        }
      }
      fail('not-found', `mock: no devtools endpoint ${method} ${url.pathname}`);
    }
    // ── results
    if ((r = /^results\/([^/]+)(?:\/(value|export|body))?$/.exec(rest)) && method === 'GET') {
      const buf = results.get(r[1]!);
      if (!buf) fail('not-found', 'the result is gone (kept 15 min per session)');
      if (!r[2]) {
        if (buf!.kind !== 'rows') fail('not-found', 'not a row result');
        const b = buf as Extract<Buf, { kind: 'rows' }>;
        const off = Math.max(0, Number(q.get('offset') ?? 0));
        const lim = Math.min(Number(q.get('limit') ?? L.maxRows), 10000);
        return ok({ resultId: r[1], offset: off, rows: pageOf(b.rows.slice(off, off + lim), off), totalRows: b.rows.length, truncated: b.truncated });
      }
      if (r[2] === 'value') {
        const b = buf as Extract<Buf, { kind: 'rows' }>;
        const v = b.rows[Number(q.get('row'))]?.[Number(q.get('col'))];
        return new Response(typeof v === 'string' ? v : JSON.stringify(v), { status: 200, headers: { 'Content-Type': 'text/plain; charset=utf-8', 'Content-Disposition': `attachment; filename="value-r${q.get('row')}-c${q.get('col')}.txt"` } });
      }
      if (r[2] === 'body') {
        const b = buf as Extract<Buf, { kind: 'body' }>;
        return new Response(b.bytes as unknown as BodyInit, { status: 200, headers: { 'Content-Type': b.contentType, 'Content-Disposition': `attachment; filename="response-body"` } });
      }
      const b = buf as Extract<Buf, { kind: 'rows' }>;
      const fmt = q.get('format') ?? 'csv';
      const objs = () => b.rows.map((row) => Object.fromEntries(b.columns.map((col, i) => [col.name, row[i] ?? null])));
      const csvCell = (v: unknown) => {
        if (v === null || v === undefined) return '';
        const s = typeof v === 'object' ? JSON.stringify(v) : String(v);
        return /[",\n\r]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
      };
      const text = fmt === 'json' ? JSON.stringify(objs(), null, 2) : fmt === 'ndjson' ? objs().map((o) => JSON.stringify(o)).join('\n') + '\n' : [b.columns.map((col) => csvCell(col.name)).join(','), ...b.rows.map((row) => row.map(csvCell).join(','))].join('\r\n') + '\r\n';
      return new Response(text, { status: 200, headers: { 'Content-Type': fmt === 'csv' ? 'text/csv; charset=utf-8' : fmt === 'json' ? 'application/json' : 'application/x-ndjson', 'Content-Disposition': `attachment; filename="result-${r[1]!.slice(-8).toLowerCase()}.${fmt}"` } });
    }
    // ── saved
    if (rest === 'saved' && method === 'GET') {
      const k = q.get('kind');
      const cid = q.get('connectionId');
      return ok({ items: (saved[site] ?? []).filter((x) => (!k || x.kind === k) && (!cid || x.connectionId === null || x.connectionId === cid)) });
    }
    if (rest === 'saved' && method === 'POST') {
      const a = actor();
      const b = body as unknown as DevToolSaved;
      if (!b.name?.trim()) fail('invalid', 'A name is required', { errors: [{ path: 'name', code: 'V_REQUIRED', message: 'Name is required', severity: 'error' }] });
      const it: DevToolSaved = { id: mid('s_'), kind: b.kind, connectionId: b.connectionId ?? null, collection: b.collection ?? '', name: b.name.trim(), description: b.description ?? '', content: b.content ?? '', createdBy: a, createdAt: iso(), updatedAt: iso() };
      (saved[site] ??= []).push(it);
      return ok(it, 201);
    }
    if ((r = /^saved\/([^/]+)$/.exec(rest))) {
      actor();
      const list = saved[site] ?? [];
      const it = list.find((x) => x.id === r![1]);
      if (!it) fail('not-found', 'saved item not found');
      if (method === 'DELETE') {
        saved[site] = list.filter((x) => x !== it);
        return ok({ deleted: true });
      }
      if (method === 'PUT') {
        const b = body as unknown as DevToolSaved;
        Object.assign(it!, { kind: b.kind, connectionId: b.connectionId ?? null, collection: b.collection ?? '', name: b.name, description: b.description ?? '', content: b.content, updatedAt: iso() });
        return ok(it);
      }
    }
    // ── environments
    if (rest === 'environments' && method === 'GET') return ok({ items: (envs[site] ?? []).map((x) => x.e) });
    if (rest === 'environments' && method === 'POST') {
      const a = actor();
      const b = body as { name?: string; variables?: { name: string; value: string; secret: boolean; enabled: boolean }[] };
      if (!b.name?.trim()) fail('invalid', 'A name is required');
      const secrets: Record<string, string> = {};
      const vars = (b.variables ?? []).map((v) => {
        if (v.secret) secrets[v.name] = v.value;
        return { ...v, value: v.secret ? MASK : v.value };
      });
      const e: Env = { e: { id: mid('e_'), name: b.name!.trim(), variables: vars, createdBy: a, updatedAt: iso() }, secrets };
      (envs[site] ??= []).push(e);
      return ok(e.e, 201);
    }
    if ((r = /^environments\/([^/]+)(\/reveal)?$/.exec(rest))) {
      const a = actor();
      const e = (envs[site] ?? []).find((x) => x.e.id === r![1]);
      if (!e) fail('not-found', 'environment not found');
      if (r[2] && method === 'POST') {
        const name = q.get('name') ?? '';
        if (e!.secrets[name] === undefined) fail('not-found', `no secret variable ${name}`);
        rateReveal(fail);
        ctx.addActivity(site, 'devtools.credential-revealed', `Dev tools environment value ${e!.e.name}.${name} revealed`, a, { environmentId: e!.e.id, name, field: 'variable' });
        return ctx.json(200, { value: e!.secrets[name], hideAfterSec: REVEAL_SECONDS }, { 'Cache-Control': 'no-store' });
      }
      if (method === 'DELETE') {
        envs[site] = envs[site]!.filter((x) => x !== e);
        return ok({ deleted: true });
      }
      if (method === 'PUT') {
        const b = body as { name: string; variables: { name: string; value: string; secret: boolean; enabled: boolean }[] };
        const secrets: Record<string, string> = {};
        e!.e.variables = b.variables.map((v) => {
          if (v.secret) secrets[v.name] = v.value === MASK ? (e!.secrets[v.name] ?? '') : v.value;
          return { ...v, value: v.secret ? MASK : v.value };
        });
        e!.secrets = secrets;
        e!.e.name = b.name;
        e!.e.updatedAt = iso();
        return ok(e!.e);
      }
    }
    // ── history
    if (rest === 'history' && method === 'GET') {
      let list = history[site] ?? [];
      const cid = q.get('connectionId');
      const k = q.get('kind');
      const ac = q.get('actor');
      const mine = q.get('mine') === 'true' || q.get('mine') === '1';
      // the agent knows the declared actor of the GUI session; the mock remembers the last one seen
      const me = lastActor;
      if (cid) list = list.filter((x) => x.connectionId === cid);
      if (k) list = list.filter((x) => x.kind === k);
      if (ac) list = list.filter((x) => x.actor === ac);
      if (mine) list = list.filter((x) => x.actor === me);
      const off = Number(q.get('cursor') ?? 0) || 0;
      const lim = Math.min(Number(q.get('limit') ?? 50), 200);
      return ok({ items: list.slice(off, off + lim), cursor: off + lim < list.length ? String(off + lim) : null });
    }
    fail('not-found', `mock: no devtools endpoint ${method} ${url.pathname}`);
  } catch (e) {
    const err = e as { code?: string; message?: string; details?: Record<string, unknown> };
    if (err && typeof err.code === 'string' && !(e instanceof SQL.SqlError)) return ctx.json(ERROR_HTTP_STATUS[err.code as ErrorCode] ?? 500, { error: { code: err.code, message: err.message, details: err.details ?? {} } }, err.code === 'rate-limited' ? { 'Retry-After': String(err.details?.retryAfterSec ?? 60) } : {});
    throw e;
  }
  return ctx.json(500, {});
}
