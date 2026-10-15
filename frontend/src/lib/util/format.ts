/** Formatting helpers (dates, sizes, ids). */
export function relTime(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return '—';
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const s = Math.round((now - t) / 1000);
  const fut = s < 0;
  const a = Math.abs(s);
  let out: string;
  if (a < 20) return 'just now';
  if (a < 60) out = `${a} s`;
  else if (a < 3600) out = `${Math.floor(a / 60)} min`;
  else if (a < 86400) out = `${Math.floor(a / 3600)} h`;
  else if (a < 86400 * 30) out = `${Math.floor(a / 86400)} d`;
  else if (a < 86400 * 365) out = `${Math.floor(a / (86400 * 30))} mo`;
  else out = `${Math.floor(a / (86400 * 365))} y`;
  return fut ? `in ${out}` : `${out} ago`;
}
export function fmtDate(iso: string | null | undefined): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString(undefined, { year: 'numeric', month: 'short', day: '2-digit', hour: '2-digit', minute: '2-digit' });
}
export function bytes(n: number | null | undefined): string {
  if (n === null || n === undefined) return '—';
  if (n < 1024) return `${n} B`;
  const u = ['KiB', 'MiB', 'GiB', 'TiB'];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${u[i]}`;
}
export function shortSha(s: string | null | undefined, n = 12): string {
  if (!s) return '—';
  const h = s.startsWith('sha256:') ? s.slice(7) : s;
  return h.slice(0, n);
}
export function initials(name: string | null | undefined): string {
  const p = (name ?? '').trim().split(/\s+/).filter(Boolean);
  if (!p.length) return '?';
  return ((p[0]![0] ?? '') + (p.length > 1 ? p[p.length - 1]![0] ?? '' : p[0]![1] ?? '')).toUpperCase();
}
export function plural(n: number, one: string, many = one + 's'): string {
  return `${n} ${n === 1 ? one : many}`;
}
export function pct(f: number | null | undefined, digits = 0): string {
  if (f === null || f === undefined || Number.isNaN(f)) return '—';
  return `${(f * 100).toFixed(digits)} %`;
}
/** "XXXXXXXXXXXXXXXXXXXXXXXX" → "XXXX-XXXX-XXXX-XXXX-XXXX-XXXX" */
export function groupKey(k: string): string {
  const c = k.replace(/[^A-Za-z2-7]/g, '').toUpperCase();
  if (c.length !== 24) return k;
  return c.match(/.{4}/g)!.join('-');
}
/** Stable colour for a string (avatars, initials). */
export function hueOf(s: string): number {
  let h = 0;
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) >>> 0;
  return h % 360;
}
export function durationLeft(iso: string, now = Date.now()): string {
  const ms = Date.parse(iso) - now;
  if (ms <= 0) return 'expired';
  const m = Math.floor(ms / 60000);
  const h = Math.floor(m / 60);
  if (h > 0) return `${h} h ${m % 60} min`;
  const s = Math.floor(ms / 1000) % 60;
  return `${m} min ${s} s`;
}
export function jsonPretty(v: unknown): string {
  return JSON.stringify(v, null, 2);
}
