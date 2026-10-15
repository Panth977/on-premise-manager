/**
 * SQLite file layer of the local DocStore: path routing (global vs deployment file), handle cache (LRU), lazy table
 * creation + index-column upgrades. Synchronous (node:sqlite DatabaseSync).
 *
 *   <dataDir>/db/global.sqlite                  every path NOT under deployments/{id}
 *   <dataDir>/db/deployments/<id>.sqlite        deployments/{id} (table "deployment") + deployments/{id}/**
 *
 * Table shape: (id TEXT PRIMARY KEY = full document path, parent TEXT NOT NULL = collection path, value TEXT NOT NULL =
 * JSON, ix_<field> …). `_outbox` (trigger rows) and `_schema` (per-table registry version) live in every file.
 */
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import { DatabaseSync, type StatementSync } from 'node:sqlite';
import { StoreError } from '../../ports/docstore.js';
import { ixColumn, type IndexRegistry } from './registry.js';

/** file key: GLOBAL or a deployment id */
export const GLOBAL = '@global';
const DEP_ID = /^[A-Za-z0-9]{1,64}$/;
const COLL_ID = /^[A-Za-z][A-Za-z0-9]*(?:_[A-Za-z0-9]+)*$/;
const bad = (m: string) => new StoreError('invalid-argument', m);

export interface ColLoc {
  key: string;
  table: string;
  /** collection path */
  parent: string;
}
export interface DocLoc extends ColLoc {
  path: string;
  id: string;
}

function checkSegs(path: string): string[] {
  const s = path.split('/');
  for (const x of s) {
    if (!x || x === '.' || x === '..' || /^__.*__$/.test(x) || Buffer.byteLength(x) > 1500) throw bad(`invalid path "${path}"`);
  }
  return s;
}
function tableOf(ids: string[], path: string): string {
  for (const c of ids) if (!COLL_ID.test(c)) throw bad(`unsupported collection id "${c}" in "${path}" (letters, digits, single "_")`);
  return ids.join('__');
}
/** collection path → file + table; 'deployments' itself (root) → null (every deployment file) */
export function locateCollection(path: string): ColLoc | null {
  const s = checkSegs(path);
  if (s.length % 2 !== 1) throw bad(`"${path}" is not a collection path`);
  if (s[0] === 'deployments') {
    if (s.length === 1) return null;
    const dep = s[1]!;
    if (!DEP_ID.test(dep)) throw bad(`deployment id "${dep}" must be [A-Za-z0-9]{1,64}`);
    const ids = s.slice(2).filter((_, i) => i % 2 === 0);
    if (ids[0] === 'deployment') throw bad('collection id "deployment" is reserved under deployments/{id}');
    return { key: dep, table: tableOf(ids, path), parent: path };
  }
  return { key: GLOBAL, table: tableOf(s.filter((_, i) => i % 2 === 0), path), parent: path };
}
export function locateDoc(path: string): DocLoc {
  const s = checkSegs(path);
  if (s.length % 2 !== 0) throw bad(`"${path}" is not a document path`);
  const parent = s.slice(0, -1).join('/');
  const id = s[s.length - 1]!;
  if (s[0] === 'deployments' && s.length === 2) {
    if (!DEP_ID.test(id)) throw bad(`deployment id "${id}" must be [A-Za-z0-9]{1,64}`);
    return { key: id, table: 'deployment', parent, path, id };
  }
  return { ...locateCollection(parent)!, path, id };
}
/** the collection id a table holds (collectionGroup) */
export const tableCollectionId = (t: string) => (t === 'deployment' ? 'deployments' : t.split('__').pop()!);

// ───────────────────────────────────────── SQL helpers
export const q = (ident: string) => `"${ident.replace(/"/g, '""')}"`;
export const jsonPath = (field: string) => '$' + field.split('.').map((s) => `."${s}"`).join('');
/** the ix encoding computed in SQL from the JSON (fallback queries + backfill); equals codec.ixOf */
export function ixSql(field: string): string {
  const p = jsonPath(field);
  const ts = `${p}."$timestamp"`;
  return `(CASE json_type(value, '${p}') WHEN 'true' THEN 1 WHEN 'false' THEN 0 WHEN 'integer' THEN json_extract(value, '${p}') WHEN 'real' THEN json_extract(value, '${p}') WHEN 'text' THEN json_extract(value, '${p}') WHEN 'object' THEN (CASE WHEN json_type(value, '${ts}') = 'object' THEN json_extract(value, '${ts}."seconds"') * 1000 + json_extract(value, '${ts}."nanos"') / 1000000 END) END)`;
}
export function isBusy(e: unknown): boolean {
  const x = e as { errcode?: number; message?: string };
  return x?.errcode === 5 || x?.errcode === 517 || x?.errcode === 6 || /database is locked|SQLITE_BUSY|database table is locked/i.test(String(x?.message ?? ''));
}

// ───────────────────────────────────────── handles
export class Handle {
  readonly stmts = new Map<string, StatementSync>();
  /** tables whose schema matches the registry in this connection */
  readonly ensured = new Set<string>();
  inTx = false;
  constructor(
    readonly key: string,
    readonly file: string,
    readonly db: DatabaseSync,
  ) {}
  prep(sql: string): StatementSync {
    let s = this.stmts.get(sql);
    if (!s) {
      if (this.stmts.size > 400) this.stmts.clear();
      s = this.db.prepare(sql);
      this.stmts.set(sql, s);
    }
    return s;
  }
}

export class Files {
  readonly dbDir: string;
  private readonly handles = new Map<string, Handle>();
  private readonly pinned = new Map<string, number>();
  private readonly versions = new Map<string, string>();
  constructor(
    dataDir: string,
    readonly registry: IndexRegistry,
    readonly strict: boolean,
    private readonly cap = 256,
  ) {
    this.dbDir = join(dataDir, 'db');
    mkdirSync(join(this.dbDir, 'deployments'), { recursive: true });
  }
  fileOf(key: string): string {
    if (key === GLOBAL) return join(this.dbDir, 'global.sqlite');
    if (!DEP_ID.test(key)) throw bad(`deployment id "${key}" must be [A-Za-z0-9]{1,64}`);
    return join(this.dbDir, 'deployments', `${key}.sqlite`);
  }
  /** every deployment file key (sorted) */
  deploymentKeys(): string[] {
    const out: string[] = [];
    for (const f of readdirSync(join(this.dbDir, 'deployments'))) {
      const m = /^([A-Za-z0-9]{1,64})\.sqlite$/.exec(f);
      if (m) out.push(m[1]!);
    }
    return out.sort();
  }
  allKeys(): string[] {
    return [...this.deploymentKeys(), GLOBAL];
  }
  /** an open handle; null when the file does not exist and !create */
  handle(key: string, create: boolean): Handle | null {
    const h = this.handles.get(key);
    if (h) {
      this.handles.delete(key);
      this.handles.set(key, h);
      return h;
    }
    const file = this.fileOf(key);
    if (!create && !existsSync(file)) return null;
    const db = new DatabaseSync(file);
    db.exec('PRAGMA busy_timeout = 5000');
    db.exec('PRAGMA journal_mode = WAL');
    db.exec('PRAGMA synchronous = NORMAL');
    db.exec(
      'CREATE TABLE IF NOT EXISTS _outbox (seq INTEGER PRIMARY KEY AUTOINCREMENT, trigger TEXT NOT NULL, path TEXT NOT NULL, before TEXT, after TEXT, createdMs INTEGER NOT NULL, leaseUntil INTEGER);' +
        'CREATE TABLE IF NOT EXISTS _schema (tbl TEXT PRIMARY KEY, version TEXT NOT NULL);',
    );
    const nh = new Handle(key, file, db);
    this.handles.set(key, nh);
    this.evict();
    return nh;
  }
  pin(keys: string[]) {
    for (const k of keys) this.pinned.set(k, (this.pinned.get(k) ?? 0) + 1);
  }
  unpin(keys: string[]) {
    for (const k of keys) {
      const n = (this.pinned.get(k) ?? 1) - 1;
      if (n <= 0) this.pinned.delete(k);
      else this.pinned.set(k, n);
    }
    this.evict();
  }
  private evict() {
    if (this.handles.size <= this.cap) return;
    for (const [k, h] of this.handles) {
      if (this.handles.size <= this.cap) break;
      if (this.pinned.has(k) || h.inTx) continue;
      this.handles.delete(k);
      try {
        h.db.close();
      } catch {
        /* already closed */
      }
    }
  }
  closeAll() {
    for (const h of this.handles.values()) {
      try {
        h.db.close();
      } catch {
        /* ignore */
      }
    }
    this.handles.clear();
  }

  tables(h: Handle): string[] {
    return (h.prep("SELECT name FROM sqlite_master WHERE type = 'table' AND substr(name, 1, 1) <> '_' AND name NOT LIKE 'sqlite%'").all() as { name: string }[]).map((r) => r.name);
  }
  private version(table: string): string {
    let v = this.versions.get(table);
    if (!v) {
      v = createHash('sha256')
        .update(JSON.stringify(this.registry[table] ?? {}))
        .digest('hex')
        .slice(0, 16);
      this.versions.set(table, v);
    }
    return v;
  }
  /** registered scalar fields of a table (each has an ix column once ensured) */
  scalarFields(table: string): string[] {
    return this.registry[table]?.scalar ?? [];
  }
  /**
   * Make the table match the registry. create=false: returns false when the table does not exist. Runs DDL inside the
   * caller's transaction (h.inTx) or its own BEGIN IMMEDIATE.
   */
  ensure(h: Handle, table: string, create: boolean): boolean {
    if (h.ensured.has(table)) return true;
    const exists = !!h.prep("SELECT 1 AS x FROM sqlite_master WHERE type = 'table' AND name = ?").get(table);
    if (!exists && !create) return false;
    const ver = this.version(table);
    if (exists) {
      const row = h.prep('SELECT version FROM _schema WHERE tbl = ?').get(table) as { version: string } | undefined;
      if (row?.version === ver) {
        h.ensured.add(table);
        return true;
      }
    }
    const own = !h.inTx;
    if (own) h.db.exec('BEGIN IMMEDIATE');
    try {
      this.upgrade(h, table, ver);
      if (own) h.db.exec('COMMIT');
    } catch (e) {
      if (own) h.db.exec('ROLLBACK');
      throw e;
    }
    h.ensured.add(table);
    return true;
  }
  private upgrade(h: Handle, table: string, ver: string) {
    const t = q(table);
    h.db.exec(`CREATE TABLE IF NOT EXISTS ${t} (id TEXT PRIMARY KEY, parent TEXT NOT NULL, value TEXT NOT NULL)`);
    const spec = this.registry[table] ?? {};
    const have = new Set((h.db.prepare(`PRAGMA table_info(${t})`).all() as { name: string }[]).map((r) => r.name));
    for (const f of spec.scalar ?? []) {
      const c = ixColumn(f);
      if (have.has(c)) continue;
      h.db.exec(`ALTER TABLE ${t} ADD COLUMN ${q(c)}`);
      h.db.exec(`UPDATE ${t} SET ${q(c)} = ${ixSql(f)}`);
    }
    const idx = (cols: string[]) => {
      const name = `${table}__${createHash('sha256').update(cols.join(',')).digest('hex').slice(0, 10)}`;
      h.db.exec(`CREATE INDEX IF NOT EXISTS ${q(name)} ON ${t} (${cols.map(q).join(', ')})`);
    };
    idx(['parent', 'id']);
    for (const f of spec.scalar ?? []) idx(['parent', ixColumn(f)]);
    for (const c of spec.composite ?? []) idx(['parent', ...c.map(ixColumn)]);
    for (const f of spec.group ?? []) idx([ixColumn(f)]);
    h.db.prepare('INSERT INTO _schema (tbl, version) VALUES (?, ?) ON CONFLICT(tbl) DO UPDATE SET version = excluded.version').run(table, ver);
  }
}
