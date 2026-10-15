/** Cell helpers of the dev tools data grid (§21 #97): display text, TSV, type-aware parsing for inline edits. */
import type { CellTruncated, CellValue, ResultColumn } from '@opm/shared/devtools';

export function isTrunc(v: unknown): v is CellTruncated {
  return typeof v === 'object' && v !== null && (v as { $truncated?: unknown }).$truncated === true;
}
/** one-line text of a cell (grid) */
export function cellText(v: CellValue | undefined, col?: ResultColumn): string {
  if (v === null || v === undefined) return 'NULL';
  if (isTrunc(v)) return v.preview.replace(/\s+/g, ' ').slice(0, 300);
  if (typeof v === 'object') return JSON.stringify(v);
  if (typeof v === 'boolean') return v ? 'true' : 'false';
  if (col?.display === 'datetime' && typeof v === 'string') return v.replace('T', ' ').replace(/\.000Z$/, 'Z');
  return String(v).replace(/\r?\n/g, '↵ ').slice(0, 1000);
}
/** the full value as text (copy, viewer) */
export function cellFull(v: CellValue | undefined): string {
  if (v === null || v === undefined) return '';
  if (isTrunc(v)) return v.preview;
  if (typeof v === 'object') return JSON.stringify(v);
  return String(v);
}
export function tsvCell(v: CellValue | undefined): string {
  const s = cellFull(v);
  return /[\t\n\r"]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
}
export function toTsv(header: string[] | null, rows: (CellValue | undefined)[][]): string {
  const out: string[] = [];
  if (header) out.push(header.map((h) => tsvCell(h)).join('\t'));
  for (const r of rows) out.push(r.map(tsvCell).join('\t'));
  return out.join('\n');
}
/** default column width by type */
export function defaultWidth(c: ResultColumn): number {
  const head = Math.min(260, c.name.length * 7.5 + 46);
  const base = c.display === 'number' ? 100 : c.display === 'bool' ? 76 : c.display === 'datetime' ? 196 : c.display === 'uuid' ? 290 : c.display === 'json' ? 260 : c.display === 'bytes' ? 150 : 190;
  return Math.max(head, base);
}
/** parse an edited text for a column (inline editing); throws a message on bad input */
export function parseEdit(text: string, col: ResultColumn): CellValue {
  const t = text;
  if (col.display === 'number') {
    if (t.trim() === '') throw new Error('Enter a number (or NULL).');
    const n = Number(t);
    if (Number.isNaN(n)) throw new Error(`"${t}" is not a number.`);
    return n;
  }
  if (col.display === 'bool') {
    if (/^(t|true|1|yes)$/i.test(t.trim())) return true;
    if (/^(f|false|0|no)$/i.test(t.trim())) return false;
    throw new Error('true or false.');
  }
  if (col.display === 'json') {
    try {
      return JSON.parse(t) as CellValue;
    } catch (e) {
      throw new Error(`Not valid JSON: ${e instanceof Error ? e.message : String(e)}`);
    }
  }
  return t;
}
/** the text to put in the editor for a value */
export function editText(v: CellValue | undefined, col: ResultColumn): string {
  if (v === null || v === undefined) return '';
  if (col.display === 'json' && typeof v === 'object') return JSON.stringify(v, null, 2);
  return cellFull(v);
}
export function fmtCount(n: number | null | undefined): string {
  if (n === null || n === undefined) return '—';
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(n >= 10_000_000 ? 0 : 1)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(n >= 10_000 ? 0 : 1)}k`;
  return String(n);
}
/** bytes → "00 ff 10 …" hex dump lines */
export function hexDump(bytes: Uint8Array, max = 4096): string {
  const lines: string[] = [];
  for (let i = 0; i < Math.min(bytes.length, max); i += 16) {
    const chunk = [...bytes.subarray(i, i + 16)];
    lines.push(`${i.toString(16).padStart(6, '0')}  ${chunk.map((b) => b.toString(16).padStart(2, '0')).join(' ').padEnd(47)}  ${chunk.map((b) => (b >= 32 && b < 127 ? String.fromCharCode(b) : '.')).join('')}`);
  }
  if (bytes.length > max) lines.push(`… ${bytes.length - max} more bytes`);
  return lines.join('\n');
}
/** pg bytea text ("\x0aff…") / base64 → bytes (best effort) */
export function bytesOf(v: string): Uint8Array {
  if (/^\\x[0-9a-f]*$/i.test(v)) {
    const h = v.slice(2);
    const out = new Uint8Array(h.length / 2);
    for (let i = 0; i < out.length; i++) out[i] = parseInt(h.slice(i * 2, i * 2 + 2), 16);
    return out;
  }
  try {
    const bin = atob(v);
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out;
  } catch {
    return new TextEncoder().encode(v);
  }
}
export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    // plain-http LAN: no async clipboard → execCommand fallback
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.cssText = 'position:fixed;left:-9999px;top:0';
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand('copy');
    ta.remove();
    return ok;
  }
}
