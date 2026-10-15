/** Formatting + threshold helpers of the toolbox pages (round T). */
import { METRIC_THRESHOLDS, type MetricAlert, type ThresholdLevel, type ToolsContainer } from '@opm/shared/agent-tools';
import { bytes } from '$lib/format';
import type { Tone } from '$lib/ui/tones';

export { bytes };

export function pctNum(v: number | null | undefined, digits = 0): string {
  if (v === null || v === undefined || !Number.isFinite(v)) return '—';
  return `${v.toFixed(digits)} %`;
}
/** bytes per second → "12 KiB/s" */
export function rate(v: number | null | undefined): string {
  if (v === null || v === undefined || !Number.isFinite(v)) return '—';
  return `${bytes(Math.round(v))}/s`;
}
/** "23 d 5 h" / "3 h 12 m" / "45 s" */
export function uptime(sec: number | null | undefined): string {
  if (sec === null || sec === undefined || !Number.isFinite(sec) || sec < 0) return '—';
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d > 0) return `${d} d ${h} h`;
  if (h > 0) return `${h} h ${m} m`;
  if (m > 0) return `${m} m`;
  return `${Math.floor(sec)} s`;
}
export function sinceSec(ts: string | null | undefined): number | null {
  if (!ts) return null;
  const t = Date.parse(ts);
  return Number.isFinite(t) ? Math.max(0, (Date.now() - t) / 1000) : null;
}

/** latest-value level (amber when above the threshold now); red comes from the API alerts (sustained ≥ 5 min) */
export function levelOf(pct: number | null | undefined, kind: 'cpu' | 'mem' | 'disk', alert?: MetricAlert | null): ThresholdLevel {
  if (alert) return alert.level;
  if (pct === null || pct === undefined) return 'ok';
  const th = kind === 'cpu' ? METRIC_THRESHOLDS.cpuPct : kind === 'mem' ? METRIC_THRESHOLDS.memPct : METRIC_THRESHOLDS.diskPct;
  if (kind === 'disk') return pct > th ? 'red' : 'ok';
  return pct > th ? 'amber' : 'ok';
}
export const levelTone: Record<ThresholdLevel, Tone> = { ok: '', amber: 'warn', red: 'bad' };

export function stateTone(c: Pick<ToolsContainer, 'state' | 'health' | 'manuallyStopped'>): Tone {
  if (c.manuallyStopped && c.state !== 'running') return '';
  if (c.state === 'running') return c.health === 'unhealthy' ? 'bad' : c.health === 'starting' ? 'warn' : 'ok';
  if (c.state === 'restarting' || c.state === 'paused' || c.state === 'created') return 'warn';
  if (c.state === 'exited') return 'bad';
  return 'bad';
}

/** stable color per container in merged views (works on the dark log surface) */
const PALETTE = ['#60a5fa', '#34d399', '#f472b6', '#fbbf24', '#a78bfa', '#22d3ee', '#fb923c', '#a3e635'];
export function colorFor(key: string, keys: string[]): string {
  const i = keys.indexOf(key);
  if (i >= 0) return PALETTE[i % PALETTE.length];
  let h = 0;
  for (const ch of key) h = (h * 31 + ch.charCodeAt(0)) >>> 0;
  return PALETTE[h % PALETTE.length];
}

/** short timestamp for log lines: "10-08 03:12:44.123" */
export function logTs(t: string): string {
  return t.length >= 23 ? `${t.slice(5, 10)} ${t.slice(11, 23)}` : t;
}
