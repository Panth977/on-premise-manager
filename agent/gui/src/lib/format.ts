/** Formatting helpers (dates relative with ISO in a tooltip, sizes, durations). */
export function toMs(ts: string | number | null | undefined): number | null {
  if (ts === null || ts === undefined || ts === '') return null;
  const n = typeof ts === 'number' ? ts : Date.parse(ts);
  return Number.isFinite(n) ? n : null;
}

export function relTime(ts: string | null | undefined, now = Date.now()): string {
  const t = toMs(ts);
  if (t === null) return '—';
  const d = Math.round((now - t) / 1000);
  const future = d < 0;
  const a = Math.abs(d);
  let s: string;
  if (a < 10) return future ? 'in a moment' : 'just now';
  if (a < 60) s = `${a} s`;
  else if (a < 3600) s = `${Math.floor(a / 60)} min`;
  else if (a < 86400) s = `${Math.floor(a / 3600)} h`;
  else if (a < 86400 * 45) s = `${Math.floor(a / 86400)} d`;
  else return fmtDate(ts);
  return future ? `in ${s}` : `${s} ago`;
}

const pad = (n: number) => String(n).padStart(2, '0');
export function fmtClock(ts: string | null | undefined): string {
  const t = toMs(ts);
  if (t === null) return '—';
  const d = new Date(t);
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
}
export function fmtDate(ts: string | null | undefined): string {
  const t = toMs(ts);
  if (t === null) return '—';
  const d = new Date(t);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}
export function fmtDateTime(ts: string | null | undefined): string {
  const t = toMs(ts);
  if (t === null) return '—';
  const d = new Date(t);
  return `${fmtDate(ts)} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

export function bytes(n: number | null | undefined): string {
  if (n === null || n === undefined || !Number.isFinite(n)) return '—';
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

export function pct(f: number | null | undefined, digits = 0): string {
  if (f === null || f === undefined || !Number.isFinite(f)) return '—';
  return `${(f * 100).toFixed(digits)} %`;
}

/** "4 h 12 m" / "12 m 5 s" */
export function duration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '0 s';
  const s = Math.floor(ms / 1000);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  if (h > 0) return `${h} h ${m} m`;
  if (m > 0) return `${m} m ${sec} s`;
  return `${sec} s`;
}
export function mmss(sec: number): string {
  const s = Math.max(0, Math.ceil(sec));
  return `${Math.floor(s / 60)}:${pad(s % 60)}`;
}

export function shortHash(h: string | null | undefined, n = 12): string {
  if (!h) return '—';
  const x = h.startsWith('sha256:') ? h.slice(7) : h;
  return x.slice(0, n);
}

export function plural(n: number, one: string, many = `${one}s`): string {
  return `${n} ${n === 1 ? one : many}`;
}

export function jsonPretty(v: unknown): string {
  try {
    return JSON.stringify(v, null, 2) ?? 'null';
  } catch {
    return String(v);
  }
}

/** Show any config value compactly (diff tables, head value columns). */
export function showValue(v: unknown): string {
  if (v === null || v === undefined) return '—';
  if (typeof v === 'string') return v === '' ? '""' : v;
  if (typeof v === 'number' || typeof v === 'boolean') return String(v);
  if (Array.isArray(v) && v.every((x) => typeof x === 'string')) return v.join(', ');
  return JSON.stringify(v);
}
