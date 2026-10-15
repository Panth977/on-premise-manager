/**
 * Mock SQL engine of the dev tools (§21 #97) — a tiny interpreter good enough for the workbench: SELECT (columns,
 * *, literals, count/sum/avg/min/max, WHERE with AND/OR, = <> < > <= >= LIKE ILIKE IN IS [NOT] NULL, GROUP BY,
 * ORDER BY, LIMIT/OFFSET), INSERT … VALUES, UPDATE … SET … WHERE, DELETE, CREATE/DROP/TRUNCATE/ALTER (tags),
 * BEGIN/COMMIT/ROLLBACK, pg_sleep(n) (cancellable), version(), EXPLAIN. Errors carry pg-like codes + positions.
 * One database per kind (every connection of a kind sees the same data), seeded deterministically.
 */
import type { ResultColumn, SchemaColumn, SchemaFunction, SchemaIndex, SqlSchemaResponse, TableDetailResponse } from '@opm/shared/devtools';

export type SqlKind = 'postgres' | 'sqlite' | 'cassandra';
export interface MCol {
  name: string;
  type: string;
  nullable: boolean;
  pk: boolean;
  default?: string | null;
  role?: string | null;
}
export type Row = Record<string, unknown>;
export interface MTable {
  ns: string;
  name: string;
  kind: 'table' | 'view' | 'materialized-view';
  cols: MCol[];
  rows: Row[];
  comment: string | null;
  indexes: SchemaIndex[];
  constraints: { name: string; type: string; definition: string }[];
  /** views: computed from another table */
  viewOf?: { table: string; where: (r: Row) => boolean; cols: string[] };
}
export interface MNamespace {
  name: string;
  system: boolean;
  meta: Record<string, string>;
  functions: SchemaFunction[];
}
export interface MDb {
  kind: SqlKind;
  version: string;
  database: string;
  namespaces: MNamespace[];
  tables: MTable[];
  defaultNs: string;
}

// ───────────────────────────── seeded data
function rng(seed: number) {
  let s = seed >>> 0;
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0;
    return s / 4294967296;
  };
}
const FIRST = ['Asha', 'Ravi', 'Mira', 'Karan', 'Neha', 'Arjun', 'Isha', 'Dev', 'Tara', 'Vikram', 'Leela', 'Omar', 'Zoya', 'Kabir', 'Anya', 'Rohan'];
const LAST = ['Shah', 'Patel', 'Mehta', 'Rao', 'Iyer', 'Kapoor', 'Das', 'Joshi', 'Nair', 'Khan', 'Bose', 'Gill'];
const SITES = ['ahmedabad', 'pune', 'delhi', 'mumbai', 'chennai'];
const T0 = Date.parse('2026-10-08T06:00:00.000Z');
function uuid(r: () => number) {
  const h = () => Math.floor(r() * 16).toString(16);
  const p = (n: number) => Array.from({ length: n }, h).join('');
  return `${p(8)}-${p(4)}-4${p(3)}-a${p(3)}-${p(12)}`;
}

function pgDb(): MDb {
  const r = rng(97);
  const users: Row[] = [];
  for (let i = 1; i <= 2500; i++) {
    const f = FIRST[i % FIRST.length]!;
    const l = LAST[(i * 7) % LAST.length]!;
    users.push({
      id: i,
      email: `${f.toLowerCase()}.${l.toLowerCase()}${i}@acme.example`,
      name: `${f} ${l}`,
      role: i % 17 === 0 ? 'admin' : i % 5 === 0 ? 'operator' : 'viewer',
      active: i % 9 !== 0,
      created_at: new Date(T0 - i * 3_600_000 * 7).toISOString(),
      profile: i % 4 === 0 ? null : { theme: i % 2 ? 'dark' : 'light', lang: i % 3 ? 'en' : 'hi', notifications: { email: i % 2 === 0, sms: i % 5 === 0 } },
    });
  }
  const devices: Row[] = [];
  for (let i = 0; i < 40; i++)
    devices.push({ id: uuid(r), label: `AQ-${(1000 + i * 37).toString(36).toUpperCase()}`, site: SITES[i % SITES.length], kind: i % 3 ? 'polludrone' : 'dustroid', last_seen: new Date(T0 - Math.floor(r() * 86_400_000)).toISOString(), battery: Math.round(r() * 1000) / 10, meta: { fw: `4.${i % 6}.${i % 3}`, sim: i % 2 ? 'airtel' : 'jio' } });
  const readings: Row[] = [];
  for (let i = 0; i < 3000; i++) {
    const d = devices[i % devices.length]!;
    readings.push({ device_id: d.id, ts: new Date(T0 - Math.floor(i / devices.length) * 900_000).toISOString(), pm25: Math.round(r() * 2500) / 10, pm10: Math.round(r() * 4000) / 10, temp: Math.round((18 + r() * 20) * 10) / 10 });
  }
  const big = Array.from({ length: 3200 }, (_, i) => `Line ${i + 1}: the quick brown fox jumps over the lazy dog.`).join('\n');
  const documents: Row[] = [
    { id: 1, title: 'Calibration report Q3', body: big, data: '\\x255044462d312e370a25e2e3cfd30a' },
    { id: 2, title: 'Short note', body: 'Filters replaced on SENSOR-1.', data: null },
    { id: 3, title: 'Config dump', body: JSON.stringify({ sites: SITES, thresholds: { pm25: 60, pm10: 100 } }), data: '\\x00ff10' },
  ];
  const events: Row[] = [];
  for (let i = 1; i <= 300; i++) events.push({ id: i, at: new Date(T0 - i * 600_000).toISOString(), actor: `${FIRST[i % FIRST.length]!.toLowerCase()}@acme.example`, action: ['login', 'device.update', 'report.export', 'user.invite'][i % 4], payload: { ip: `10.0.1.${i % 200}`, ok: i % 11 !== 0 } });
  const ucols: MCol[] = [
    { name: 'id', type: 'int4', nullable: false, pk: true, default: "nextval('users_id_seq'::regclass)" },
    { name: 'email', type: 'text', nullable: false, pk: false },
    { name: 'name', type: 'text', nullable: false, pk: false },
    { name: 'role', type: 'text', nullable: false, pk: false, default: "'viewer'::text" },
    { name: 'active', type: 'bool', nullable: false, pk: false, default: 'true' },
    { name: 'created_at', type: 'timestamptz', nullable: false, pk: false, default: 'now()' },
    { name: 'profile', type: 'jsonb', nullable: true, pk: false },
  ];
  return {
    kind: 'postgres',
    version: 'PostgreSQL 16.4 (Debian 16.4-1.pgdg120+1) on x86_64-pc-linux-gnu',
    database: 'northwind',
    defaultNs: 'public',
    namespaces: [
      { name: 'public', system: false, meta: { owner: 'opm' }, functions: [{ name: 'device_count', kind: 'function', arguments: 'site text', returns: 'integer' }, { name: 'touch_updated_at', kind: 'trigger', arguments: '', returns: 'trigger' }] },
      { name: 'audit', system: false, meta: { owner: 'opm' }, functions: [] },
      { name: 'information_schema', system: true, meta: { owner: 'postgres' }, functions: [] },
      { name: 'pg_catalog', system: true, meta: { owner: 'postgres' }, functions: [{ name: 'now', kind: 'function', arguments: '', returns: 'timestamp with time zone' }, { name: 'pg_sleep', kind: 'function', arguments: 'double precision', returns: 'void' }] },
    ],
    tables: [
      { ns: 'public', name: 'users', kind: 'table', cols: ucols, rows: users, comment: 'Console users', indexes: [{ name: 'users_pkey', definition: 'CREATE UNIQUE INDEX users_pkey ON public.users USING btree (id)', unique: true, primary: true }, { name: 'users_email_key', definition: 'CREATE UNIQUE INDEX users_email_key ON public.users USING btree (email)', unique: true, primary: false }], constraints: [{ name: 'users_pkey', type: 'PRIMARY KEY', definition: 'PRIMARY KEY (id)' }, { name: 'users_email_key', type: 'UNIQUE', definition: 'UNIQUE (email)' }, { name: 'users_role_check', type: 'CHECK', definition: "CHECK (role = ANY (ARRAY['admin','operator','viewer']))" }] },
      {
        ns: 'public',
        name: 'devices',
        kind: 'table',
        cols: [
          { name: 'id', type: 'uuid', nullable: false, pk: true, default: 'gen_random_uuid()' },
          { name: 'label', type: 'text', nullable: false, pk: false },
          { name: 'site', type: 'text', nullable: false, pk: false },
          { name: 'kind', type: 'text', nullable: false, pk: false },
          { name: 'last_seen', type: 'timestamptz', nullable: true, pk: false },
          { name: 'battery', type: 'numeric(4,1)', nullable: true, pk: false },
          { name: 'meta', type: 'jsonb', nullable: true, pk: false },
        ],
        rows: devices,
        comment: 'Monitoring devices',
        indexes: [{ name: 'devices_pkey', definition: 'CREATE UNIQUE INDEX devices_pkey ON public.devices USING btree (id)', unique: true, primary: true }, { name: 'devices_site_idx', definition: 'CREATE INDEX devices_site_idx ON public.devices USING btree (site)', unique: false, primary: false }],
        constraints: [{ name: 'devices_pkey', type: 'PRIMARY KEY', definition: 'PRIMARY KEY (id)' }],
      },
      {
        ns: 'public',
        name: 'readings',
        kind: 'table',
        cols: [
          { name: 'device_id', type: 'uuid', nullable: false, pk: true },
          { name: 'ts', type: 'timestamptz', nullable: false, pk: true },
          { name: 'pm25', type: 'float8', nullable: true, pk: false },
          { name: 'pm10', type: 'float8', nullable: true, pk: false },
          { name: 'temp', type: 'float8', nullable: true, pk: false },
        ],
        rows: readings,
        comment: null,
        indexes: [{ name: 'readings_pkey', definition: 'CREATE UNIQUE INDEX readings_pkey ON public.readings USING btree (device_id, ts)', unique: true, primary: true }],
        constraints: [{ name: 'readings_pkey', type: 'PRIMARY KEY', definition: 'PRIMARY KEY (device_id, ts)' }, { name: 'readings_device_id_fkey', type: 'FOREIGN KEY', definition: 'FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE CASCADE' }],
      },
      {
        ns: 'public',
        name: 'documents',
        kind: 'table',
        cols: [
          { name: 'id', type: 'int4', nullable: false, pk: true },
          { name: 'title', type: 'text', nullable: false, pk: false },
          { name: 'body', type: 'text', nullable: true, pk: false },
          { name: 'data', type: 'bytea', nullable: true, pk: false },
        ],
        rows: documents,
        comment: 'Large values (cell truncation demo)',
        indexes: [{ name: 'documents_pkey', definition: 'CREATE UNIQUE INDEX documents_pkey ON public.documents USING btree (id)', unique: true, primary: true }],
        constraints: [{ name: 'documents_pkey', type: 'PRIMARY KEY', definition: 'PRIMARY KEY (id)' }],
      },
      { ns: 'public', name: 'active_users', kind: 'view', cols: ucols.slice(0, 4).map((c) => ({ ...c, pk: false, default: null })), rows: [], comment: 'Users with active = true', indexes: [], constraints: [], viewOf: { table: 'users', where: (x) => x.active === true, cols: ['id', 'email', 'name', 'role'] } },
      {
        ns: 'audit',
        name: 'events',
        kind: 'table',
        cols: [
          { name: 'id', type: 'int8', nullable: false, pk: true },
          { name: 'at', type: 'timestamptz', nullable: false, pk: false, default: 'now()' },
          { name: 'actor', type: 'text', nullable: true, pk: false },
          { name: 'action', type: 'text', nullable: false, pk: false },
          { name: 'payload', type: 'jsonb', nullable: true, pk: false },
        ],
        rows: events,
        comment: null,
        indexes: [{ name: 'events_pkey', definition: 'CREATE UNIQUE INDEX events_pkey ON audit.events USING btree (id)', unique: true, primary: true }],
        constraints: [{ name: 'events_pkey', type: 'PRIMARY KEY', definition: 'PRIMARY KEY (id)' }],
      },
      { ns: 'information_schema', name: 'tables', kind: 'view', cols: [{ name: 'table_schema', type: 'name', nullable: true, pk: false }, { name: 'table_name', type: 'name', nullable: true, pk: false }, { name: 'table_type', type: 'varchar', nullable: true, pk: false }], rows: [], comment: null, indexes: [], constraints: [] },
      { ns: 'pg_catalog', name: 'pg_stat_activity', kind: 'view', cols: [{ name: 'pid', type: 'int4', nullable: true, pk: false }, { name: 'usename', type: 'name', nullable: true, pk: false }, { name: 'state', type: 'text', nullable: true, pk: false }, { name: 'query', type: 'text', nullable: true, pk: false }], rows: [{ pid: 4121, usename: 'opm', state: 'active', query: 'SELECT …' }, { pid: 4122, usename: 'northwind', state: 'idle', query: 'COMMIT' }], comment: null, indexes: [], constraints: [] },
    ],
  };
}

function sqliteDb(): MDb {
  const jobs: Row[] = [];
  for (let i = 1; i <= 120; i++) jobs.push({ id: i, name: ['report.daily', 'sync.devices', 'cleanup.tmp', 'email.digest'][i % 4], status: i % 13 === 0 ? 'failed' : i % 3 === 0 ? 'queued' : 'done', attempts: i % 4, payload: JSON.stringify({ run: i, site: SITES[i % SITES.length] }), created_at: new Date(T0 - i * 1_800_000).toISOString().replace('T', ' ').slice(0, 19) });
  return {
    kind: 'sqlite',
    version: 'SQLite 3.46.1',
    database: '/var/lib/opm/sites/app/vol/hello-api-data/app.db',
    defaultNs: 'main',
    namespaces: [{ name: 'main', system: false, meta: { file: 'app.db' }, functions: [] }],
    tables: [
      {
        ns: 'main',
        name: 'settings',
        kind: 'table',
        cols: [
          { name: 'key', type: 'TEXT', nullable: false, pk: true },
          { name: 'value', type: 'TEXT', nullable: true, pk: false },
          { name: 'updated_at', type: 'TEXT', nullable: true, pk: false, default: 'CURRENT_TIMESTAMP' },
        ],
        rows: [
          { key: 'theme', value: 'dark', updated_at: '2026-10-07 10:00:00' },
          { key: 'retention_days', value: '30', updated_at: '2026-10-01 08:12:00' },
          { key: 'smtp_host', value: 'mail.acme.example', updated_at: '2026-09-21 18:40:00' },
          { key: 'features', value: '{"heatmap":true,"exports":false}', updated_at: '2026-10-05 12:00:00' },
        ],
        comment: null,
        indexes: [{ name: 'sqlite_autoindex_settings_1', definition: 'PRIMARY KEY (key)', unique: true, primary: true }],
        constraints: [{ name: 'pk', type: 'PRIMARY KEY', definition: 'PRIMARY KEY (key)' }],
      },
      {
        ns: 'main',
        name: 'jobs',
        kind: 'table',
        cols: [
          { name: 'id', type: 'INTEGER', nullable: false, pk: true },
          { name: 'name', type: 'TEXT', nullable: false, pk: false },
          { name: 'status', type: 'TEXT', nullable: false, pk: false, default: "'queued'" },
          { name: 'attempts', type: 'INTEGER', nullable: false, pk: false, default: '0' },
          { name: 'payload', type: 'JSON', nullable: true, pk: false },
          { name: 'created_at', type: 'DATETIME', nullable: false, pk: false },
        ],
        rows: jobs,
        comment: null,
        indexes: [{ name: 'jobs_status', definition: 'CREATE INDEX jobs_status ON jobs(status)', unique: false, primary: false }],
        constraints: [{ name: 'pk', type: 'PRIMARY KEY', definition: 'PRIMARY KEY (id AUTOINCREMENT)' }],
      },
    ],
  };
}

function cqlDb(): MDb {
  const r = rng(11);
  const devs = Array.from({ length: 12 }, () => uuid(r));
  const rows: Row[] = [];
  for (let i = 0; i < 600; i++) rows.push({ device_id: devs[i % devs.length], day: '2026-10-08', ts: new Date(T0 - Math.floor(i / devs.length) * 60_000).toISOString(), pm25: Math.round(r() * 1500) / 10, noise_db: Math.round((40 + r() * 40) * 10) / 10 });
  const devices: Row[] = devs.map((d, i) => ({ id: d, name: `sensor-${i + 1}`, tags: ['outdoor', i % 2 ? 'roof' : 'street'], attrs: { site: SITES[i % SITES.length]!, fw: `2.${i}` } }));
  return {
    kind: 'cassandra',
    version: 'Cassandra 4.1.5',
    database: 'telemetry',
    defaultNs: 'telemetry',
    namespaces: [
      { name: 'telemetry', system: false, meta: { replication: "{'class': 'SimpleStrategy', 'replication_factor': '1'}", durable_writes: 'true' }, functions: [] },
      { name: 'system', system: true, meta: { replication: "{'class': 'LocalStrategy'}" }, functions: [] },
      { name: 'system_schema', system: true, meta: { replication: "{'class': 'LocalStrategy'}" }, functions: [] },
    ],
    tables: [
      {
        ns: 'telemetry',
        name: 'readings_by_device',
        kind: 'table',
        cols: [
          { name: 'device_id', type: 'uuid', nullable: false, pk: true, role: 'partition_key' },
          { name: 'day', type: 'text', nullable: false, pk: true, role: 'partition_key' },
          { name: 'ts', type: 'timestamp', nullable: false, pk: true, role: 'clustering' },
          { name: 'pm25', type: 'double', nullable: true, pk: false, role: 'regular' },
          { name: 'noise_db', type: 'double', nullable: true, pk: false, role: 'regular' },
        ],
        rows,
        comment: 'One partition per device and day',
        indexes: [],
        constraints: [{ name: 'primary key', type: 'PRIMARY KEY', definition: 'PRIMARY KEY ((device_id, day), ts) WITH CLUSTERING ORDER BY (ts DESC)' }],
      },
      {
        ns: 'telemetry',
        name: 'devices',
        kind: 'table',
        cols: [
          { name: 'id', type: 'uuid', nullable: false, pk: true, role: 'partition_key' },
          { name: 'name', type: 'text', nullable: true, pk: false, role: 'regular' },
          { name: 'tags', type: 'set<text>', nullable: true, pk: false, role: 'regular' },
          { name: 'attrs', type: 'map<text, text>', nullable: true, pk: false, role: 'regular' },
        ],
        rows: devices,
        comment: null,
        indexes: [],
        constraints: [{ name: 'primary key', type: 'PRIMARY KEY', definition: 'PRIMARY KEY (id)' }],
      },
      { ns: 'system', name: 'local', kind: 'table', cols: [{ name: 'key', type: 'text', nullable: false, pk: true, role: 'partition_key' }, { name: 'release_version', type: 'text', nullable: true, pk: false, role: 'regular' }], rows: [{ key: 'local', release_version: '4.1.5' }], comment: null, indexes: [], constraints: [] },
    ],
  };
}

const DBS: Partial<Record<SqlKind, MDb>> = {};
export function dbOf(kind: SqlKind): MDb {
  return (DBS[kind] ??= kind === 'postgres' ? pgDb() : kind === 'sqlite' ? sqliteDb() : cqlDb());
}

// ───────────────────────────── schema endpoints
export function displayOf(type: string): ResultColumn['display'] {
  const t = type.toLowerCase();
  if (/^(int|bigint|smallint|numeric|decimal|float|double|real|integer|counter|varint|serial)/.test(t) || /^int[248]$/.test(t) || t === 'float8' || t === 'float4') return 'number';
  if (/^bool/.test(t)) return 'bool';
  if (/json|^map|^set|^list|^frozen|^tuple|\[\]$/.test(t)) return 'json';
  if (/timestamp|^date|^time/.test(t)) return 'datetime';
  if (/bytea|blob/.test(t)) return 'bytes';
  if (t === 'uuid' || t === 'timeuuid') return 'uuid';
  if (/text|char|name|string|ascii/.test(t)) return 'text';
  return 'other';
}
function schemaCol(c: MCol): SchemaColumn {
  return { name: c.name, type: c.type, nullable: c.nullable, default: c.default ?? null, primaryKey: c.pk, role: c.role ?? null, comment: null };
}
function rowsOf(db: MDb, t: MTable): Row[] {
  if (!t.viewOf) return t.rows;
  const base = db.tables.find((x) => x.name === t.viewOf!.table);
  return (base?.rows ?? []).filter(t.viewOf.where).map((r) => Object.fromEntries(t.viewOf!.cols.map((c) => [c, r[c]])));
}
function sizeOf(t: MTable) {
  return t.kind === 'view' ? null : Math.max(8192, Math.round(JSON.stringify(t.rows.slice(0, 50)).length * (t.rows.length / Math.max(1, Math.min(50, t.rows.length))) * 1.4));
}
export function schemaOf(kind: SqlKind): SqlSchemaResponse {
  const db = dbOf(kind);
  return {
    kind,
    serverVersion: db.version.split(' on ')[0]!.replace(/ \(.*\)$/, ''),
    database: db.database,
    namespaces: db.namespaces.map((n) => ({
      name: n.name,
      system: n.system,
      meta: n.meta,
      functions: n.functions,
      tables: db.tables
        .filter((t) => t.ns === n.name)
        .map((t) => ({ name: t.name, kind: t.kind, rowEstimate: kind === 'postgres' && t.kind === 'table' ? rowsOf(db, t).length : null, sizeBytes: kind === 'cassandra' ? null : sizeOf(t), columns: t.cols.map(schemaCol), comment: t.comment })),
    })),
    loadedAt: new Date().toISOString(),
    truncated: false,
  };
}
export function tableDetailOf(kind: SqlKind, ns: string, table: string): TableDetailResponse | null {
  const db = dbOf(kind);
  const t = db.tables.find((x) => x.ns === ns && x.name === table);
  if (!t) return null;
  const q = (s: string) => (kind === 'postgres' ? `${t.ns}.${s}` : s);
  const ddl =
    t.kind === 'view'
      ? `CREATE VIEW ${q(t.name)} AS\n SELECT ${t.viewOf?.cols.join(', ') ?? '*'}\n   FROM ${q(t.viewOf?.table ?? '?')}\n  WHERE active;`
      : `CREATE TABLE ${q(t.name)} (\n${t.cols.map((c) => `    ${c.name} ${c.type}${c.nullable ? '' : ' NOT NULL'}${c.default ? ` DEFAULT ${c.default}` : ''}`).join(',\n')}${t.constraints.length ? ',\n' + t.constraints.map((c) => `    CONSTRAINT ${c.name.includes(' ') ? `"${c.name}"` : c.name} ${c.definition}`).join(',\n') : ''}\n);${t.indexes
          .filter((i) => !i.primary)
          .map((i) => `\n${i.definition};`)
          .join('')}`;
  return { namespace: ns, table, columns: t.cols.map(schemaCol), indexes: t.indexes, constraints: t.constraints, ddl, rowEstimate: kind === 'cassandra' ? null : rowsOf(db, t).length, sizeBytes: kind === 'cassandra' ? null : sizeOf(t) };
}

// ───────────────────────────── tokenizer + interpreter
export class SqlError extends Error {
  constructor(
    message: string,
    public code: string,
    public position: number | null = null,
    public detail: string | null = null,
    public hint: string | null = null,
  ) {
    super(message);
  }
}
type Tok = { t: 'id' | 'qid' | 'num' | 'str' | 'param' | 'op' | 'eof'; v: string; pos: number };
function lex(s: string): Tok[] {
  const out: Tok[] = [];
  let i = 0;
  while (i < s.length) {
    const c = s[i]!;
    if (/\s/.test(c)) {
      i++;
      continue;
    }
    if (c === '-' && s[i + 1] === '-') {
      const e = s.indexOf('\n', i);
      i = e < 0 ? s.length : e;
      continue;
    }
    if (c === '/' && s[i + 1] === '*') {
      const e = s.indexOf('*/', i + 2);
      i = e < 0 ? s.length : e + 2;
      continue;
    }
    if (c === "'") {
      let j = i + 1;
      let v = '';
      while (j < s.length) {
        if (s[j] === "'" && s[j + 1] === "'") {
          v += "'";
          j += 2;
          continue;
        }
        if (s[j] === "'") break;
        v += s[j];
        j++;
      }
      if (j >= s.length) throw new SqlError('unterminated quoted string at or near "' + s.slice(i, i + 12) + '"', '42601', i + 1);
      out.push({ t: 'str', v, pos: i });
      i = j + 1;
      continue;
    }
    if (c === '"' || c === '`') {
      const j = s.indexOf(c, i + 1);
      out.push({ t: 'qid', v: s.slice(i + 1, j < 0 ? s.length : j), pos: i });
      i = j < 0 ? s.length : j + 1;
      continue;
    }
    if (/[0-9]/.test(c) || (c === '.' && /[0-9]/.test(s[i + 1] ?? ''))) {
      const m = /^[0-9]*\.?[0-9]+(e[+-]?[0-9]+)?/i.exec(s.slice(i))!;
      out.push({ t: 'num', v: m[0], pos: i });
      i += m[0].length;
      continue;
    }
    if (c === '$' && /[0-9]/.test(s[i + 1] ?? '')) {
      const m = /^\$[0-9]+/.exec(s.slice(i))!;
      out.push({ t: 'param', v: m[0], pos: i });
      i += m[0].length;
      continue;
    }
    if (c === '?') {
      out.push({ t: 'param', v: '?', pos: i });
      i++;
      continue;
    }
    if (/[A-Za-z_]/.test(c)) {
      const m = /^[A-Za-z_][A-Za-z0-9_$]*/.exec(s.slice(i))!;
      out.push({ t: 'id', v: m[0], pos: i });
      i += m[0].length;
      continue;
    }
    const two = s.slice(i, i + 2);
    if (['<>', '!=', '<=', '>=', '::', '||'].includes(two)) {
      out.push({ t: 'op', v: two, pos: i });
      i += 2;
      continue;
    }
    out.push({ t: 'op', v: c, pos: i });
    i++;
  }
  out.push({ t: 'eof', v: '', pos: s.length });
  return out;
}

type Expr =
  | { k: 'col'; name: string; qual: string | null; pos: number }
  | { k: 'lit'; v: unknown }
  | { k: 'param'; i: number; pos: number }
  | { k: 'star' }
  | { k: 'agg'; fn: string; arg: Expr | null; pos: number }
  | { k: 'fn'; fn: string; args: Expr[]; pos: number }
  | { k: 'bin'; op: string; a: Expr; b: Expr }
  | { k: 'not'; a: Expr }
  | { k: 'isnull'; a: Expr; not: boolean }
  | { k: 'in'; a: Expr; list: Expr[]; not: boolean }
  | { k: 'like'; a: Expr; b: Expr; ci: boolean; not: boolean };

class P {
  i = 0;
  constructor(
    public toks: Tok[],
    public src: string,
  ) {}
  peek(o = 0) {
    return this.toks[this.i + o]!;
  }
  kw(...w: string[]) {
    const t = this.peek();
    return t.t === 'id' && w.includes(t.v.toUpperCase());
  }
  eat(...w: string[]) {
    if (this.kw(...w)) return this.toks[this.i++]!;
    return null;
  }
  op(v: string) {
    const t = this.peek();
    if (t.t === 'op' && t.v === v) {
      this.i++;
      return true;
    }
    return false;
  }
  expect(w: string) {
    if (!this.eat(w) && !this.op(w)) this.err();
  }
  err(): never {
    const t = this.peek();
    throw new SqlError(t.t === 'eof' ? 'syntax error at end of input' : `syntax error at or near "${t.t === 'str' ? `'${t.v}'` : t.v}"`, '42601', t.pos + 1);
  }
  ident(): { v: string; pos: number } {
    const t = this.peek();
    if (t.t === 'id' || t.t === 'qid') {
      this.i++;
      return { v: t.v, pos: t.pos };
    }
    this.err();
  }
  /** or-expression */
  expr(): Expr {
    let a = this.and();
    while (this.eat('OR')) a = { k: 'bin', op: 'OR', a, b: this.and() };
    return a;
  }
  and(): Expr {
    let a = this.not();
    while (this.eat('AND')) a = { k: 'bin', op: 'AND', a, b: this.not() };
    return a;
  }
  not(): Expr {
    if (this.eat('NOT')) return { k: 'not', a: this.not() };
    return this.cmp();
  }
  cmp(): Expr {
    const a = this.add();
    if (this.eat('IS')) {
      const not = !!this.eat('NOT');
      this.expect('NULL');
      return { k: 'isnull', a, not };
    }
    const not = !!this.eat('NOT');
    if (this.eat('IN')) {
      this.expect('(');
      const list: Expr[] = [this.add()];
      while (this.op(',')) list.push(this.add());
      this.expect(')');
      return { k: 'in', a, list, not };
    }
    const lk = this.eat('LIKE', 'ILIKE');
    if (lk) return { k: 'like', a, b: this.add(), ci: lk.v.toUpperCase() === 'ILIKE', not };
    if (not) this.err();
    const t = this.peek();
    if (t.t === 'op' && ['=', '<>', '!=', '<', '>', '<=', '>='].includes(t.v)) {
      this.i++;
      return { k: 'bin', op: t.v, a, b: this.add() };
    }
    return a;
  }
  add(): Expr {
    let a = this.atom();
    for (;;) {
      const t = this.peek();
      if (t.t === 'op' && ['+', '-', '*', '/', '||'].includes(t.v) && !(t.v === '*' && false)) {
        this.i++;
        a = { k: 'bin', op: t.v, a, b: this.atom() };
      } else if (t.t === 'op' && t.v === '::') {
        this.i++;
        this.ident();
        if (this.op('(')) {
          while (!this.op(')')) this.i++;
        }
      } else break;
    }
    return a;
  }
  atom(): Expr {
    const t = this.peek();
    if (t.t === 'num') {
      this.i++;
      return { k: 'lit', v: Number(t.v) };
    }
    if (t.t === 'str') {
      this.i++;
      return { k: 'lit', v: t.v };
    }
    if (t.t === 'param') {
      this.i++;
      return { k: 'param', i: t.v === '?' ? -1 : Number(t.v.slice(1)) - 1, pos: t.pos };
    }
    if (t.t === 'op' && t.v === '(') {
      this.i++;
      const e = this.expr();
      this.expect(')');
      return e;
    }
    if (t.t === 'op' && t.v === '-') {
      this.i++;
      const e = this.atom();
      return { k: 'bin', op: '-', a: { k: 'lit', v: 0 }, b: e };
    }
    if (t.t === 'op' && t.v === '*') {
      this.i++;
      return { k: 'star' };
    }
    if (t.t === 'id' || t.t === 'qid') {
      const up = t.v.toUpperCase();
      if (t.t === 'id' && (up === 'NULL' || up === 'TRUE' || up === 'FALSE')) {
        this.i++;
        return { k: 'lit', v: up === 'NULL' ? null : up === 'TRUE' };
      }
      if (t.t === 'id' && (up === 'CURRENT_TIMESTAMP' || up === 'CURRENT_DATE')) {
        this.i++;
        return { k: 'fn', fn: 'now', args: [], pos: t.pos };
      }
      this.i++;
      if (this.peek().t === 'op' && this.peek().v === '(') {
        this.i++;
        const fn = t.v.toLowerCase();
        if (['count', 'sum', 'avg', 'min', 'max'].includes(fn)) {
          let arg: Expr | null = null;
          this.eat('DISTINCT');
          if (!this.op(')')) {
            arg = this.expr();
            this.expect(')');
          }
          return { k: 'agg', fn, arg: arg && arg.k === 'star' ? null : arg, pos: t.pos };
        }
        const args: Expr[] = [];
        if (!this.op(')')) {
          args.push(this.expr());
          while (this.op(',')) args.push(this.expr());
          this.expect(')');
        }
        return { k: 'fn', fn, args, pos: t.pos };
      }
      if (this.peek().t === 'op' && this.peek().v === '.') {
        this.i++;
        if (this.op('*')) return { k: 'star' };
        const c = this.ident();
        return { k: 'col', name: c.v, qual: t.v, pos: c.pos };
      }
      return { k: 'col', name: t.v, qual: null, pos: t.pos };
    }
    this.err();
  }
}

export interface ExecCtx {
  params: unknown[];
  kind: SqlKind;
  readOnly: boolean;
  /** pg_sleep: resolves after ms or rejects when cancelled */
  sleep: (ms: number) => Promise<void>;
}
export interface StmtOut {
  type: 'rows' | 'command' | 'explain';
  commandTag: string | null;
  rowsAffected: number | null;
  columns: ResultColumn[];
  rows: unknown[][];
  plan?: { text: string; json: unknown | null } | null;
  notices?: string[];
}

function cmpVals(a: unknown, b: unknown): number {
  if (a === null || a === undefined) return b === null || b === undefined ? 0 : 1;
  if (b === null || b === undefined) return -1;
  if (typeof a === 'number' && typeof b === 'number') return a - b;
  if (typeof a === 'boolean' && typeof b === 'boolean') return Number(a) - Number(b);
  return String(typeof a === 'object' ? JSON.stringify(a) : a).localeCompare(String(typeof b === 'object' ? JSON.stringify(b) : b));
}
function eq(a: unknown, b: unknown) {
  if (a === null || b === null || a === undefined || b === undefined) return null;
  if (typeof a === 'number' || typeof b === 'number') return Number(a) === Number(b);
  if (typeof a === 'boolean' || typeof b === 'boolean') return String(a) === String(b) || (a === true && b === 't') || (b === true && a === 't');
  return String(a) === String(b);
}

function findTable(db: MDb, name: string, qual: string | null, pos: number): MTable {
  const t = db.tables.find((x) => x.name === name && (qual ? x.ns === qual : x.ns === db.defaultNs || db.kind !== 'postgres')) ?? (qual ? undefined : db.tables.find((x) => x.name === name && !db.namespaces.find((n) => n.name === x.ns)?.system));
  if (!t) {
    const full = qual ? `${qual}.${name}` : name;
    if (db.kind === 'sqlite') throw new SqlError(`no such table: ${full}`, 'SQLITE_ERROR', pos + 1);
    if (db.kind === 'cassandra') throw new SqlError(`table ${full} does not exist`, 'InvalidRequest', pos + 1);
    throw new SqlError(`relation "${full}" does not exist`, '42P01', pos + 1);
  }
  return t;
}

export async function runStatement(db: MDb, stmt: string, ctx: ExecCtx): Promise<StmtOut> {
  const toks = lex(stmt);
  const p = new P(toks, stmt);
  let paramN = 0;
  const param = (e: { i: number; pos: number }) => {
    const i = e.i < 0 ? paramN++ : e.i;
    if (i >= ctx.params.length) throw new SqlError(`there is no parameter $${i + 1}`, '42P02', e.pos + 1, null, 'Add a value for it in the Params panel.');
    return ctx.params[i];
  };
  const first = p.peek();
  const kw = first.t === 'id' ? first.v.toUpperCase() : '';
  const cmd = (tag: string, n: number | null = null): StmtOut => ({ type: 'command', commandTag: tag, rowsAffected: n, columns: [], rows: [] });
  const ro = (verb: string) => {
    if (!ctx.readOnly) return;
    if (db.kind === 'postgres') throw new SqlError(`cannot execute ${verb} in a read-only transaction`, '25006', null, null, 'This connection is read-only (BEGIN READ ONLY).');
    if (db.kind === 'sqlite') throw new SqlError('attempt to write a readonly database', 'SQLITE_READONLY', null);
  };
  if (kw === 'EXPLAIN') {
    p.i++;
    const analyze = !!p.eat('ANALYZE', 'ANALYSE');
    p.eat('VERBOSE');
    if (db.kind === 'sqlite') p.eat('QUERY') && p.eat('PLAN');
    const rest = stmt.slice(p.peek().pos);
    return explainOf(db, rest, analyze, ctx);
  }
  if (kw === 'SELECT' || kw === 'WITH' || kw === 'VALUES' || kw === 'TABLE') {
    if (kw !== 'SELECT') throw new SqlError(`mock agent: ${kw} is not supported by the mock SQL engine`, '0A000', first.pos + 1);
    return select(db, p, ctx, param);
  }
  if (['BEGIN', 'START', 'COMMIT', 'END', 'ROLLBACK', 'SAVEPOINT', 'RELEASE'].includes(kw)) return cmd(kw === 'START' ? 'START TRANSACTION' : kw === 'END' ? 'COMMIT' : kw);
  if (kw === 'SET' || kw === 'USE' || kw === 'PRAGMA') {
    if (kw === 'PRAGMA') {
      const name = toks[1]?.v ?? '';
      if (name === 'table_info') {
        const tn = toks.find((t, i) => i > 1 && (t.t === 'id' || t.t === 'str' || t.t === 'qid'))?.v ?? '';
        const t = findTable(db, tn, null, 0);
        return { type: 'rows', commandTag: null, rowsAffected: null, columns: ['cid', 'name', 'type', 'notnull', 'dflt_value', 'pk'].map((n) => ({ name: n, type: n === 'cid' || n === 'notnull' || n === 'pk' ? 'INTEGER' : 'TEXT', display: n === 'cid' || n === 'notnull' || n === 'pk' ? ('number' as const) : ('text' as const) })), rows: t.cols.map((c, i) => [i, c.name, c.type, c.nullable ? 0 : 1, c.default ?? null, c.pk ? 1 : 0]) };
      }
      return { type: 'rows', commandTag: null, rowsAffected: null, columns: [{ name: name, type: 'TEXT', display: 'text' }], rows: [[name === 'journal_mode' ? 'wal' : name === 'user_version' ? 3 : 'ok']] };
    }
    return cmd(kw);
  }
  if (kw === 'INSERT' || kw === 'UPSERT' || kw === 'REPLACE') {
    ro('INSERT');
    p.i++;
    p.expect('INTO');
    const a = p.ident();
    let qual: string | null = null;
    let name = a.v;
    if (p.op('.')) {
      qual = a.v;
      name = p.ident().v;
    }
    const t = findTable(db, name, qual, a.pos);
    let cols = t.cols.map((c) => c.name);
    if (p.op('(')) {
      cols = [p.ident().v];
      while (p.op(',')) cols.push(p.ident().v);
      p.expect(')');
    }
    for (const c of cols) if (!t.cols.find((x) => x.name === c)) throw new SqlError(`column "${c}" of relation "${t.name}" does not exist`, '42703', null);
    p.expect('VALUES');
    const rows: Row[] = [];
    do {
      p.expect('(');
      const vals: Expr[] = [p.expr()];
      while (p.op(',')) vals.push(p.expr());
      p.expect(')');
      if (vals.length !== cols.length) throw new SqlError('INSERT has more expressions than target columns', '42601', null);
      const row: Row = {};
      for (const c of t.cols) row[c.name] = c.default?.startsWith('nextval') ? Math.max(0, ...t.rows.map((r) => Number(r[c.name]) || 0)) + 1 + rows.length : c.default === 'now()' || c.default === 'CURRENT_TIMESTAMP' ? new Date().toISOString() : c.default === 'true' ? true : null;
      cols.forEach((c, i) => (row[c] = coerce(t, c, ev(vals[i]!, {}, param, ctx))));
      for (const c of t.cols) if (!c.nullable && (row[c.name] === null || row[c.name] === undefined)) throw new SqlError(`null value in column "${c.name}" of relation "${t.name}" violates not-null constraint`, '23502', null, `Failing row contains (${t.cols.map((x) => fmtVal(row[x.name])).join(', ')}).`);
      const pk = t.cols.filter((c) => c.pk);
      if (pk.length && t.rows.some((r) => pk.every((c) => eq(r[c.name], row[c.name])))) throw new SqlError(`duplicate key value violates unique constraint "${t.name}_pkey"`, '23505', null, `Key (${pk.map((c) => c.name).join(', ')})=(${pk.map((c) => fmtVal(row[c.name])).join(', ')}) already exists.`);
      rows.push(row);
    } while (p.op(','));
    t.rows.push(...rows);
    if (p.eat('RETURNING')) return returning(t, rows, p);
    return cmd(db.kind === 'postgres' ? `INSERT 0 ${rows.length}` : `INSERT ${rows.length}`, rows.length);
  }
  if (kw === 'UPDATE') {
    ro('UPDATE');
    p.i++;
    const a = p.ident();
    let qual: string | null = null;
    let name = a.v;
    if (p.op('.')) {
      qual = a.v;
      name = p.ident().v;
    }
    const t = findTable(db, name, qual, a.pos);
    p.expect('SET');
    const sets: { c: string; e: Expr; pos: number }[] = [];
    do {
      const c = p.ident();
      if (!t.cols.find((x) => x.name === c.v)) throw new SqlError(`column "${c.v}" of relation "${t.name}" does not exist`, '42703', c.pos + 1);
      p.expect('=');
      sets.push({ c: c.v, e: p.expr(), pos: c.pos });
    } while (p.op(','));
    const where = p.eat('WHERE') ? p.expr() : null;
    if (where) checkCols(where, t);
    const hit = t.rows.filter((r) => !where || truthy(ev(where, r, param, ctx)));
    for (const r of hit) for (const s of sets) r[s.c] = coerce(t, s.c, ev(s.e, r, param, ctx));
    if (p.eat('RETURNING')) return returning(t, hit, p);
    return cmd(`UPDATE ${hit.length}`, hit.length);
  }
  if (kw === 'DELETE') {
    ro('DELETE');
    p.i++;
    p.expect('FROM');
    const a = p.ident();
    let qual: string | null = null;
    let name = a.v;
    if (p.op('.')) {
      qual = a.v;
      name = p.ident().v;
    }
    const t = findTable(db, name, qual, a.pos);
    const where = p.eat('WHERE') ? p.expr() : null;
    if (where) checkCols(where, t);
    const keep = t.rows.filter((r) => where && !truthy(ev(where, r, param, ctx)));
    const n = t.rows.length - keep.length;
    t.rows = keep;
    return cmd(`DELETE ${n}`, n);
  }
  if (kw === 'TRUNCATE') {
    ro('TRUNCATE TABLE');
    p.i++;
    p.eat('TABLE');
    const a = p.ident();
    let qual: string | null = null;
    let name = a.v;
    if (p.op('.')) {
      qual = a.v;
      name = p.ident().v;
    }
    findTable(db, name, qual, a.pos).rows = [];
    return cmd('TRUNCATE TABLE');
  }
  if (kw === 'DROP') {
    ro('DROP TABLE');
    p.i++;
    const what = p.ident().v.toUpperCase();
    p.eat('IF') && p.eat('EXISTS');
    const a = p.ident();
    let qual: string | null = null;
    let name = a.v;
    if (p.op('.')) {
      qual = a.v;
      name = p.ident().v;
    }
    if (what === 'TABLE' || what === 'VIEW') {
      const t = findTable(db, name, qual, a.pos);
      db.tables = db.tables.filter((x) => x !== t);
    }
    return cmd(`DROP ${what}`);
  }
  if (kw === 'CREATE') {
    ro('CREATE TABLE');
    const m = /^\s*create\s+table\s+(?:if\s+not\s+exists\s+)?(?:([A-Za-z_][\w]*)\.)?([A-Za-z_][\w]*)\s*\(([\s\S]*)\)\s*;?\s*$/i.exec(stmt);
    if (m) {
      const ns = m[1] ?? db.defaultNs;
      if (db.tables.some((t) => t.ns === ns && t.name === m[2])) throw new SqlError(`relation "${m[2]}" already exists`, '42P07', null);
      const cols: MCol[] = m[3]!
        .split(/,(?![^(]*\))/)
        .map((s) => s.trim())
        .filter((s) => s && !/^(primary|constraint|unique|foreign|check)\b/i.test(s))
        .map((s) => {
          const [n, ...rest] = s.split(/\s+/);
          const ty = rest.join(' ');
          return { name: n!.replace(/"/g, ''), type: (/^[^\s]+(\([^)]*\))?/.exec(ty)?.[0] ?? 'text').toLowerCase(), nullable: !/not null|primary key/i.test(ty), pk: /primary key/i.test(ty) };
        });
      db.tables.push({ ns, name: m[2]!, kind: 'table', cols, rows: [], comment: null, indexes: [], constraints: [] });
    }
    const what = (toks[1]?.v ?? 'TABLE').toUpperCase();
    return cmd(`CREATE ${what === 'UNIQUE' ? 'INDEX' : what}`);
  }
  if (kw === 'ALTER') {
    ro('ALTER TABLE');
    return cmd(`ALTER ${(toks[1]?.v ?? 'TABLE').toUpperCase()}`);
  }
  if (kw === 'VACUUM' || kw === 'ANALYZE') return cmd(kw);
  if (kw === 'GRANT' || kw === 'REVOKE' || kw === 'COMMENT') {
    ro(kw);
    return cmd(kw);
  }
  if (kw === 'DESCRIBE' || kw === 'DESC') {
    const t = findTable(db, toks[toks[1]?.v.toUpperCase() === 'TABLE' ? 2 : 1]?.v ?? '', null, 0);
    return { type: 'rows', commandTag: null, rowsAffected: null, columns: [{ name: 'column', type: 'text', display: 'text' }, { name: 'type', type: 'text', display: 'text' }, { name: 'kind', type: 'text', display: 'text' }], rows: t.cols.map((c) => [c.name, c.type, c.role ?? '']) };
  }
  throw new SqlError(`syntax error at or near "${first.v}"`, db.kind === 'sqlite' ? 'SQLITE_ERROR' : db.kind === 'cassandra' ? 'SyntaxException' : '42601', first.pos + 1);
}

function returning(t: MTable, rows: Row[], p: P): StmtOut {
  const cols: string[] = [];
  if (p.op('*')) cols.push(...t.cols.map((c) => c.name));
  else {
    cols.push(p.ident().v);
    while (p.op(',')) cols.push(p.ident().v);
  }
  return { type: 'rows', commandTag: `RETURNING ${rows.length}`, rowsAffected: rows.length, columns: cols.map((c) => colOf(t, c)), rows: rows.map((r) => cols.map((c) => r[c] ?? null)) };
}
function colOf(t: MTable, c: string): ResultColumn {
  const col = t.cols.find((x) => x.name === c);
  return { name: c, type: col?.type ?? 'text', display: displayOf(col?.type ?? 'text') };
}
function coerce(t: MTable, c: string, v: unknown): unknown {
  const col = t.cols.find((x) => x.name === c);
  if (!col || v === null) return v;
  const d = displayOf(col.type);
  if (d === 'number') {
    const n = Number(v);
    if (Number.isNaN(n)) throw new SqlError(`invalid input syntax for type ${col.type}: "${String(v)}"`, '22P02', null);
    return n;
  }
  if (d === 'bool') return v === true || v === 't' || v === 'true' || v === 1 || v === '1';
  if (d === 'json' && typeof v === 'string') {
    try {
      return JSON.parse(v);
    } catch {
      throw new SqlError(`invalid input syntax for type json`, '22P02', null, `Token "${v.slice(0, 20)}" is invalid.`);
    }
  }
  return v;
}
function fmtVal(v: unknown) {
  return v === null || v === undefined ? 'null' : typeof v === 'object' ? JSON.stringify(v) : String(v);
}
function truthy(v: unknown) {
  return v === true;
}
function checkCols(e: Expr, t: MTable) {
  const walk = (x: Expr) => {
    if (x.k === 'col' && !t.cols.find((c) => c.name === x.name)) throw new SqlError(`column "${x.name}" does not exist`, '42703', x.pos + 1, null, t.cols.length ? `Columns of ${t.name}: ${t.cols.map((c) => c.name).join(', ')}` : null);
    if (x.k === 'bin') (walk(x.a), walk(x.b));
    if (x.k === 'not' || x.k === 'isnull') walk(x.a);
    if (x.k === 'in') (walk(x.a), x.list.forEach(walk));
    if (x.k === 'like') (walk(x.a), walk(x.b));
    if (x.k === 'fn') x.args.forEach(walk);
  };
  walk(e);
}

function ev(e: Expr, row: Row, param: (e: { i: number; pos: number }) => unknown, ctx: ExecCtx, group?: Row[]): unknown {
  switch (e.k) {
    case 'lit':
      return e.v;
    case 'param':
      return param(e);
    case 'col':
      return row[e.name] ?? null;
    case 'star':
      return null;
    case 'agg': {
      const rows = group ?? [row];
      const vals = e.arg ? rows.map((r) => ev(e.arg!, r, param, ctx)).filter((v) => v !== null && v !== undefined) : rows;
      if (e.fn === 'count') return vals.length;
      const nums = (vals as unknown[]).map(Number);
      if (!nums.length) return null;
      if (e.fn === 'sum') return Math.round(nums.reduce((a, b) => a + b, 0) * 1000) / 1000;
      if (e.fn === 'avg') return Math.round((nums.reduce((a, b) => a + b, 0) / nums.length) * 1000) / 1000;
      if (e.fn === 'min') return (vals as unknown[]).reduce((a, b) => (cmpVals(a, b) <= 0 ? a : b));
      return (vals as unknown[]).reduce((a, b) => (cmpVals(a, b) >= 0 ? a : b));
    }
    case 'fn': {
      const a = e.args.map((x) => ev(x, row, param, ctx, group));
      switch (e.fn) {
        case 'now':
        case 'current_timestamp':
          return new Date().toISOString();
        case 'version':
          return ctx.kind === 'postgres' ? dbOf('postgres').version : dbOf(ctx.kind).version;
        case 'sqlite_version':
          return '3.46.1';
        case 'lower':
          return a[0] === null ? null : String(a[0]).toLowerCase();
        case 'upper':
          return a[0] === null ? null : String(a[0]).toUpperCase();
        case 'length':
        case 'char_length':
          return a[0] === null ? null : String(a[0]).length;
        case 'coalesce':
          return a.find((x) => x !== null && x !== undefined) ?? null;
        case 'round':
          return a[0] === null ? null : Math.round(Number(a[0]) * 10 ** Number(a[1] ?? 0)) / 10 ** Number(a[1] ?? 0);
        case 'current_database':
          return dbOf(ctx.kind).database;
        case 'current_user':
          return 'opm';
        case 'gen_random_uuid':
        case 'uuid':
          return crypto.randomUUID();
        case 'json_build_object':
        case 'jsonb_build_object': {
          const o: Record<string, unknown> = {};
          for (let i = 0; i + 1 < a.length; i += 2) o[String(a[i])] = a[i + 1];
          return o;
        }
        default:
          throw new SqlError(`function ${e.fn}() does not exist`, '42883', e.pos + 1, null, 'No function matches the given name and argument types. You might need to add explicit type casts.');
      }
    }
    case 'not': {
      const v = ev(e.a, row, param, ctx, group);
      return v === null ? null : !v;
    }
    case 'isnull': {
      const v = ev(e.a, row, param, ctx, group);
      return e.not ? v !== null && v !== undefined : v === null || v === undefined;
    }
    case 'in': {
      const v = ev(e.a, row, param, ctx, group);
      const hit = e.list.some((x) => eq(v, ev(x, row, param, ctx, group)) === true);
      return e.not ? !hit : hit;
    }
    case 'like': {
      const v = ev(e.a, row, param, ctx, group);
      const pat = String(ev(e.b, row, param, ctx, group));
      if (v === null) return null;
      const re = new RegExp('^' + pat.replace(/[.*+?^${}()|[\]\\]/g, '\\$&').replace(/%/g, '.*').replace(/_/g, '.') + '$', e.ci ? 'is' : 's');
      const hit = re.test(typeof v === 'object' ? JSON.stringify(v) : String(v));
      return e.not ? !hit : hit;
    }
    case 'bin': {
      if (e.op === 'AND') {
        const a = ev(e.a, row, param, ctx, group);
        if (a === false) return false;
        const b = ev(e.b, row, param, ctx, group);
        return a === true && b === true ? true : b === false ? false : null;
      }
      if (e.op === 'OR') {
        const a = ev(e.a, row, param, ctx, group);
        if (a === true) return true;
        const b = ev(e.b, row, param, ctx, group);
        return b === true ? true : a === false && b === false ? false : null;
      }
      const a = ev(e.a, row, param, ctx, group);
      const b = ev(e.b, row, param, ctx, group);
      if (e.op === '||') return a === null || b === null ? null : `${a}${b}`;
      if (['+', '-', '*', '/'].includes(e.op)) {
        if (a === null || b === null) return null;
        const x = Number(a);
        const y = Number(b);
        if (e.op === '/' && y === 0) throw new SqlError('division by zero', '22012', null);
        return e.op === '+' ? x + y : e.op === '-' ? x - y : e.op === '*' ? x * y : Number.isInteger(x) && Number.isInteger(y) ? Math.trunc(x / y) : x / y;
      }
      if (a === null || b === null || a === undefined || b === undefined) return null;
      if (e.op === '=') return eq(a, b);
      if (e.op === '<>' || e.op === '!=') return !eq(a, b);
      const c = typeof a === 'number' || typeof b === 'number' ? Number(a) - Number(b) : cmpVals(a, b);
      return e.op === '<' ? c < 0 : e.op === '>' ? c > 0 : e.op === '<=' ? c <= 0 : c >= 0;
    }
  }
}

function exprName(e: Expr): string {
  if (e.k === 'col') return e.name;
  if (e.k === 'agg') return e.fn;
  if (e.k === 'fn') return e.fn;
  return '?column?';
}

async function select(db: MDb, p: P, ctx: ExecCtx, param: (e: { i: number; pos: number }) => unknown): Promise<StmtOut> {
  p.i++; // SELECT
  p.eat('DISTINCT');
  const items: { e: Expr; alias: string | null; star: boolean }[] = [];
  do {
    if (p.op('*')) {
      items.push({ e: { k: 'star' }, alias: null, star: true });
      continue;
    }
    const e = p.expr();
    let alias: string | null = null;
    if (p.eat('AS')) alias = p.ident().v;
    else if ((p.peek().t === 'id' && !p.kw('FROM', 'WHERE', 'GROUP', 'ORDER', 'LIMIT', 'OFFSET', 'UNION')) || p.peek().t === 'qid') alias = p.ident().v;
    items.push({ e, alias, star: e.k === 'star' });
  } while (p.op(','));
  // pg_sleep (cancellable)
  for (const it of items)
    if (it.e.k === 'fn' && it.e.fn === 'pg_sleep') {
      const sec = Number(ev(it.e.args[0] ?? { k: 'lit', v: 1 }, {}, param, ctx));
      await ctx.sleep(sec * 1000);
      return { type: 'rows', commandTag: 'SELECT 1', rowsAffected: null, columns: [{ name: it.alias ?? 'pg_sleep', type: 'void', display: 'other' }], rows: [['']] };
    }
  let t: MTable | null = null;
  let rows: Row[] = [{}];
  if (p.eat('FROM')) {
    const a = p.ident();
    let qual: string | null = null;
    let name = a.v;
    if (p.op('.')) {
      qual = a.v;
      name = p.ident().v;
    }
    t = findTable(db, name, qual, a.pos);
    rows = rowsOf(db, t);
    if (p.eat('AS') || (p.peek().t === 'id' && !p.kw('WHERE', 'GROUP', 'ORDER', 'LIMIT', 'OFFSET', 'ALLOW'))) p.ident();
    for (const it of items) if (!it.star) checkCols(it.e, t);
  } else {
    for (const it of items) if (it.e.k === 'col') throw new SqlError(`column "${it.e.name}" does not exist`, '42703', it.e.pos + 1);
  }
  let where: Expr | null = null;
  if (p.eat('WHERE')) {
    where = p.expr();
    if (t) checkCols(where, t);
  }
  let groupBy: Expr[] = [];
  if (p.eat('GROUP')) {
    p.expect('BY');
    groupBy = [p.expr()];
    while (p.op(',')) groupBy.push(p.expr());
  }
  const order: { e: Expr; desc: boolean }[] = [];
  if (p.eat('ORDER')) {
    p.expect('BY');
    do {
      const e = p.expr();
      const desc = !!p.eat('DESC');
      if (!desc) p.eat('ASC');
      p.eat('NULLS') && p.eat('FIRST', 'LAST');
      order.push({ e, desc });
    } while (p.op(','));
  }
  let limit: number | null = null;
  let offset = 0;
  for (let k = 0; k < 2; k++) {
    if (p.eat('LIMIT')) limit = Number(ev(p.add(), {}, param, ctx));
    if (p.eat('OFFSET')) offset = Number(ev(p.add(), {}, param, ctx));
  }
  p.eat('ALLOW') && p.eat('FILTERING');
  if (p.peek().t !== 'eof' && !(p.peek().t === 'op' && p.peek().v === ';')) p.err();
  let out = where ? rows.filter((r) => truthy(ev(where!, r, param, ctx))) : rows.slice();
  const hasAgg = items.some((it) => JSON.stringify(it.e).includes('"k":"agg"'));
  let resultRows: { row: Row; group?: Row[] }[];
  if (groupBy.length || hasAgg) {
    const groups = new Map<string, Row[]>();
    for (const r of out) {
      const k = JSON.stringify(groupBy.map((g) => ev(g, r, param, ctx)));
      groups.set(k, [...(groups.get(k) ?? []), r]);
    }
    if (!groups.size && !groupBy.length) groups.set('[]', []);
    resultRows = [...groups.values()].map((g) => ({ row: g[0] ?? {}, group: g }));
  } else resultRows = out.map((row) => ({ row }));
  const cols: ResultColumn[] = [];
  const getters: ((x: { row: Row; group?: Row[] }) => unknown)[] = [];
  for (const it of items) {
    if (it.star) {
      if (!t) throw new SqlError('SELECT * with no tables specified is not valid', '42601', null);
      for (const c of t.viewOf ? t.cols : t.cols) {
        cols.push({ name: c.name, type: c.type, display: displayOf(c.type) });
        getters.push((x) => x.row[c.name] ?? null);
      }
      continue;
    }
    const name = it.alias ?? exprName(it.e);
    const type = it.e.k === 'col' && t ? (t.cols.find((c) => c.name === (it.e as { name: string }).name)?.type ?? 'text') : it.e.k === 'agg' ? (it.e.fn === 'count' ? 'int8' : 'numeric') : it.e.k === 'lit' ? (typeof it.e.v === 'number' ? 'int4' : typeof it.e.v === 'boolean' ? 'bool' : 'text') : it.e.k === 'fn' && (it.e.fn === 'now' || it.e.fn === 'current_timestamp') ? 'timestamptz' : it.e.k === 'fn' && it.e.fn.includes('json') ? 'jsonb' : 'text';
    cols.push({ name, type, display: displayOf(type) });
    const e = it.e;
    getters.push((x) => ev(e, x.row, param, ctx, x.group));
  }
  if (order.length) {
    resultRows.sort((a, b) => {
      for (const o of order) {
        const byAlias = o.e.k === 'col' ? cols.findIndex((c) => c.name === (o.e as { name: string }).name) : -1;
        const av = o.e.k === 'lit' && typeof o.e.v === 'number' ? getters[o.e.v - 1]?.(a) : byAlias >= 0 && !t?.cols.find((c) => c.name === (o.e as { name: string }).name) ? getters[byAlias]!(a) : ev(o.e, a.row, param, ctx, a.group);
        const bv = o.e.k === 'lit' && typeof o.e.v === 'number' ? getters[o.e.v - 1]?.(b) : byAlias >= 0 && !t?.cols.find((c) => c.name === (o.e as { name: string }).name) ? getters[byAlias]!(b) : ev(o.e, b.row, param, ctx, b.group);
        const c = cmpVals(av, bv);
        if (c) return o.desc ? -c : c;
      }
      return 0;
    });
  }
  resultRows = resultRows.slice(offset, limit === null ? undefined : offset + limit);
  const data = resultRows.map((x) => getters.map((g) => g(x)));
  out = [];
  return { type: 'rows', commandTag: `SELECT ${data.length}`, rowsAffected: null, columns: cols, rows: data };
}

function explainOf(db: MDb, rest: string, analyze: boolean, ctx: ExecCtx): StmtOut {
  if (db.kind === 'cassandra') throw new SqlError('EXPLAIN is not supported by Cassandra', 'SyntaxException', 1);
  const m = /\bfrom\s+(?:([A-Za-z_]\w*)\.)?([A-Za-z_]\w*)/i.exec(rest);
  const t = m ? findTable(db, m[2]!, m[1] ?? null, 0) : null;
  const n = t ? rowsOf(db, t).length : 1;
  const where = /\bwhere\b([\s\S]*?)(\border\b|\blimit\b|$)/i.exec(rest)?.[1]?.trim() ?? null;
  const pkCol = t?.cols.find((c) => c.pk)?.name;
  const byPk = !!(where && pkCol && new RegExp(`\\b${pkCol}\\s*=`).test(where));
  const order = /\border\s+by\s+([\w.]+)/i.exec(rest)?.[1] ?? null;
  const limit = /\blimit\s+(\d+)/i.exec(rest)?.[1];
  const rowsOut = byPk ? 1 : where ? Math.max(1, Math.round(n / 9)) : n;
  if (db.kind === 'sqlite') {
    const text = t ? (byPk ? `SEARCH ${t.name} USING INTEGER PRIMARY KEY (rowid=?)` : `SCAN ${t.name}`) + (order ? '\nUSE TEMP B-TREE FOR ORDER BY' : '') : 'SCAN CONSTANT ROW';
    return { type: 'explain', commandTag: 'EXPLAIN', rowsAffected: null, columns: [{ name: 'id', type: 'INTEGER', display: 'number' }, { name: 'parent', type: 'INTEGER', display: 'number' }, { name: 'detail', type: 'TEXT', display: 'text' }], rows: text.split('\n').map((d, i) => [i + 2, 0, d]), plan: { text: text.split('\n').map((d) => `|--${d}`).join('\n'), json: null } };
  }
  const ms = (x: number) => Math.round(x * 1000) / 1000;
  const scan: Record<string, unknown> = t
    ? byPk
      ? { 'Node Type': 'Index Scan', 'Relation Name': t.name, Schema: t.ns, Alias: t.name, 'Index Name': `${t.name}_pkey`, 'Index Cond': `(${where})`, 'Startup Cost': 0.28, 'Total Cost': 8.3, 'Plan Rows': 1, 'Plan Width': 64 }
      : { 'Node Type': 'Seq Scan', 'Relation Name': t.name, Schema: t.ns, Alias: t.name, ...(where ? { Filter: `(${where})`, 'Rows Removed by Filter': analyze ? n - rowsOut : undefined } : {}), 'Startup Cost': 0, 'Total Cost': ms(n * 0.0125 + 1), 'Plan Rows': rowsOut, 'Plan Width': 72 }
    : { 'Node Type': 'Result', 'Startup Cost': 0, 'Total Cost': 0.01, 'Plan Rows': 1, 'Plan Width': 4 };
  if (analyze) Object.assign(scan, { 'Actual Startup Time': 0.012, 'Actual Total Time': ms(rowsOut * 0.0009 + 0.02), 'Actual Rows': rowsOut, 'Actual Loops': 1, 'Shared Hit Blocks': Math.ceil(n / 60), 'Shared Read Blocks': 0 });
  let top: Record<string, unknown> = scan;
  if (order) top = { 'Node Type': 'Sort', 'Sort Key': [order], 'Startup Cost': ms(Number(scan['Total Cost']) + 4), 'Total Cost': ms(Number(scan['Total Cost']) + 6), 'Plan Rows': rowsOut, 'Plan Width': 72, ...(analyze ? { 'Actual Startup Time': 0.4, 'Actual Total Time': 0.6, 'Actual Rows': rowsOut, 'Actual Loops': 1, 'Sort Method': 'quicksort', 'Sort Space Used': 48, 'Sort Space Type': 'Memory' } : {}), Plans: [{ ...scan, 'Parent Relationship': 'Outer' }] };
  if (limit) top = { 'Node Type': 'Limit', 'Startup Cost': top['Startup Cost'], 'Total Cost': top['Total Cost'], 'Plan Rows': Math.min(rowsOut, Number(limit)), 'Plan Width': 72, ...(analyze ? { 'Actual Startup Time': 0.4, 'Actual Total Time': 0.61, 'Actual Rows': Math.min(rowsOut, Number(limit)), 'Actual Loops': 1 } : {}), Plans: [{ ...top, 'Parent Relationship': 'Outer' }] };
  const json = [{ Plan: top, ...(analyze ? { 'Planning Time': 0.081, 'Execution Time': ms(Number(top['Actual Total Time'] ?? 0.1) + 0.03) } : {}) }];
  const lines: string[] = [];
  const walk = (node: Record<string, unknown>, depth: number) => {
    const pad = depth ? ' '.repeat(depth * 6 - 4) + '->  ' : '';
    const name = `${node['Node Type']}${node['Relation Name'] ? `${node['Index Name'] ? ` using ${node['Index Name']}` : ''} on ${node['Relation Name']}` : ''}`;
    lines.push(`${pad}${name}  (cost=${node['Startup Cost']}..${node['Total Cost']} rows=${node['Plan Rows']} width=${node['Plan Width']})${analyze ? ` (actual time=${node['Actual Startup Time']}..${node['Actual Total Time']} rows=${node['Actual Rows']} loops=1)` : ''}`);
    const ind = ' '.repeat(depth ? depth * 6 + 2 : 2);
    if (node['Index Cond']) lines.push(`${ind}Index Cond: ${node['Index Cond']}`);
    if (node.Filter) lines.push(`${ind}Filter: ${node.Filter}`);
    if (node['Rows Removed by Filter'] !== undefined) lines.push(`${ind}Rows Removed by Filter: ${node['Rows Removed by Filter']}`);
    if (node['Sort Key']) lines.push(`${ind}Sort Key: ${(node['Sort Key'] as string[]).join(', ')}`);
    for (const c of (node.Plans as Record<string, unknown>[] | undefined) ?? []) walk(c, depth + 1);
  };
  walk(top, 0);
  if (analyze) lines.push('Planning Time: 0.081 ms', `Execution Time: ${json[0]!['Execution Time']} ms`);
  void ctx;
  return { type: 'explain', commandTag: 'EXPLAIN', rowsAffected: null, columns: [{ name: 'QUERY PLAN', type: 'text', display: 'text' }], rows: lines.map((l) => [l]), plan: { text: lines.join('\n'), json } };
}

/** deep copy of the tables (transaction rollback) */
export function snapshotDb(db: MDb): MTable[] {
  return db.tables.map((t) => ({ ...t, rows: t.rows.map((r) => ({ ...r })) }));
}
export function restoreDb(db: MDb, tables: MTable[]) {
  db.tables = tables;
}
