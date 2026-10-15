/**
 * Inline editing of a `SELECT * FROM <table>` result (§21 #97, like the reference workbench): pending updates per cell,
 * deleted rows, inserted rows; Save generates UPDATE / DELETE / INSERT keyed by the primary key (known from the
 * schema) and runs them through executeSql (so they are logged and classified like any statement).
 */
import type { CellValue, ResultColumn, SqlSchemaResponse } from '@opm/shared/devtools';
import { isTrunc } from '../grid';

export interface EditTarget {
  namespace: string;
  table: string;
  /** indexes of the PK columns in the result */
  pk: number[];
  dialect: 'postgres' | 'sqlite';
}

const IDENT_RE = /^[a-z_][a-z0-9_$]*$/;
export function quoteIdent(s: string): string {
  return IDENT_RE.test(s) ? s : `"${s.replace(/"/g, '""')}"`;
}
export function sqlLiteral(v: CellValue | undefined, col: ResultColumn, dialect: 'postgres' | 'sqlite'): string {
  if (v === null || v === undefined) return 'NULL';
  if (typeof v === 'number') return Number.isFinite(v) ? String(v) : 'NULL';
  if (typeof v === 'boolean') return dialect === 'sqlite' ? (v ? '1' : '0') : v ? 'TRUE' : 'FALSE';
  const s = typeof v === 'object' ? JSON.stringify(v) : String(v);
  const q = `'${s.replace(/'/g, "''")}'`;
  if (dialect === 'postgres' && col.display === 'json') return `${q}::${col.type === 'json' ? 'json' : 'jsonb'}`;
  if (dialect === 'postgres' && col.display === 'uuid') return `${q}::uuid`;
  if (dialect === 'postgres' && col.display === 'datetime') return `${q}::${col.type}`;
  return q;
}

/**
 * The editable target of a statement: `SELECT * FROM [ns.]table …` (no join, no group) on a table with a PK whose
 * columns are all in the result. null = not editable (with the reason).
 */
export function editTargetOf(statement: string, columns: ResultColumn[], schema: SqlSchemaResponse | null, kind: string): { target: EditTarget | null; reason: string } {
  if (kind !== 'postgres' && kind !== 'sqlite') return { target: null, reason: 'Inline editing is available for PostgreSQL and SQLite results.' };
  if (!schema) return { target: null, reason: 'The schema is not loaded.' };
  const s = statement.replace(/--[^\n]*|\/\*[\s\S]*?\*\//g, ' ').trim();
  const m = /^select\s+\*\s+from\s+((?:"[^"]+"|[A-Za-z_][\w$]*)(?:\s*\.\s*(?:"[^"]+"|[A-Za-z_][\w$]*))?)(\s+(?:as\s+)?(?!where\b|order\b|limit\b|offset\b)[A-Za-z_][\w$]*)?\s*(where\b[\s\S]*|order\b[\s\S]*|limit\b[\s\S]*|offset\b[\s\S]*|;?\s*)$/i.exec(s);
  if (!m || /\b(join|group\s+by|union|having|distinct)\b/i.test(s)) return { target: null, reason: 'Inline editing works on the result of a plain `SELECT * FROM <table>` (no joins, no grouping).' };
  const parts = m[1]!.split('.').map((x) => x.trim().replace(/^"|"$/g, ''));
  const table = parts[parts.length - 1]!;
  const nsName = parts.length === 2 ? parts[0]! : null;
  const ns = schema.namespaces.find((n) => (nsName ? n.name === nsName : !n.system && n.tables.some((t) => t.name === table)));
  const t = ns?.tables.find((x) => x.name === table);
  if (!ns || !t) return { target: null, reason: `${table} is not in the loaded schema (refresh it).` };
  if (t.kind !== 'table' && t.kind !== 'partitioned-table') return { target: null, reason: `${table} is a ${t.kind}; only tables are editable.` };
  const pkCols = t.columns.filter((c) => c.primaryKey).map((c) => c.name);
  if (!pkCols.length) return { target: null, reason: `${table} has no primary key: rows cannot be addressed safely.` };
  const pk = pkCols.map((n) => columns.findIndex((c) => c.name === n));
  if (pk.some((i) => i < 0)) return { target: null, reason: 'The primary key columns are not all in the result.' };
  return { target: { namespace: ns.name, table, pk, dialect: kind }, reason: '' };
}

export class EditState {
  /** original row index → column index → new value */
  updates = $state<Record<number, Record<number, CellValue>>>({});
  deletes = $state<Record<number, true>>({});
  inserts = $state<CellValue[][]>([]);
  count = $derived(Object.values(this.updates).reduce((n, r) => n + Object.keys(r).length, 0) + Object.keys(this.deletes).length + this.inserts.length);
  set(row: number, col: number, v: CellValue, original: CellValue | undefined) {
    const r = { ...(this.updates[row] ?? {}) };
    if (JSON.stringify(v) === JSON.stringify(original ?? null)) delete r[col];
    else r[col] = v;
    const u = { ...this.updates };
    if (Object.keys(r).length) u[row] = r;
    else delete u[row];
    this.updates = u;
  }
  toggleDelete(rows: number[]) {
    const d = { ...this.deletes };
    const all = rows.every((r) => d[r]);
    for (const r of rows) {
      if (all) delete d[r];
      else d[r] = true;
    }
    this.deletes = d;
  }
  addRow(cols: number) {
    this.inserts = [...this.inserts, Array.from({ length: cols }, () => null)];
  }
  setNew(k: number, col: number, v: CellValue) {
    const ins = this.inserts.map((r) => r.slice());
    ins[k]![col] = v;
    this.inserts = ins;
  }
  removeNew(k: number) {
    this.inserts = this.inserts.filter((_, i) => i !== k);
  }
  clear() {
    this.updates = {};
    this.deletes = {};
    this.inserts = [];
  }
  /** statements to run (in order: deletes, updates, inserts) */
  statements(t: EditTarget, columns: ResultColumn[], rows: CellValue[][]): string[] {
    const tbl = t.dialect === 'postgres' ? `${quoteIdent(t.namespace)}.${quoteIdent(t.table)}` : quoteIdent(t.table);
    const where = (row: CellValue[]) => t.pk.map((i) => `${quoteIdent(columns[i]!.name)} = ${sqlLiteral(row[i], columns[i]!, t.dialect)}`).join(' AND ');
    const out: string[] = [];
    for (const r of Object.keys(this.deletes).map(Number)) out.push(`DELETE FROM ${tbl} WHERE ${where(rows[r]!)}`);
    for (const [r, cols] of Object.entries(this.updates)) {
      const ri = Number(r);
      if (this.deletes[ri]) continue;
      const sets = Object.entries(cols).map(([c, v]) => `${quoteIdent(columns[Number(c)]!.name)} = ${sqlLiteral(v, columns[Number(c)]!, t.dialect)}`);
      out.push(`UPDATE ${tbl} SET ${sets.join(', ')} WHERE ${where(rows[ri]!)}`);
    }
    for (const row of this.inserts) {
      const idx = row.map((v, i) => (v === null ? -1 : i)).filter((i) => i >= 0);
      if (!idx.length) continue;
      out.push(`INSERT INTO ${tbl} (${idx.map((i) => quoteIdent(columns[i]!.name)).join(', ')}) VALUES (${idx.map((i) => sqlLiteral(row[i], columns[i]!, t.dialect)).join(', ')})`);
    }
    return out;
  }
}
/** a truncated cell cannot be edited inline (the full value is not in the browser) */
export const editableCell = (v: CellValue | undefined) => !isTrunc(v);
