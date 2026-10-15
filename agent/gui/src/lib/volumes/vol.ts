/**
 * §21 #96 volume manager — GUI helpers: a small line diff (the editor's "Review changes"), op labels, uploads.
 */
import type { VolumeOp, VolumeOpStepName } from '@opm/shared/types';
import { TOOLS_API } from '@opm/shared/agent-tools';
import { uploadRawTo } from '$lib/api';
import { toolsPath } from '$lib/tools/api';

export type DiffLine = { kind: 'same' | 'add' | 'del'; text: string; a: number | null; b: number | null };

/** line diff (LCS; inputs ≤ 1 MiB, lines capped at 4000 each side — beyond that a summary diff) */
export function lineDiff(before: string, after: string): DiffLine[] {
  const a = before.split('\n');
  const b = after.split('\n');
  if (a.length > 4000 || b.length > 4000) {
    return [...a.map((t, i) => ({ kind: 'del' as const, text: t, a: i + 1, b: null })), ...b.map((t, i) => ({ kind: 'add' as const, text: t, a: null, b: i + 1 }))];
  }
  const n = a.length;
  const m = b.length;
  const dp: Uint32Array[] = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1));
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
  const out: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (a[i] === b[j]) out.push({ kind: 'same', text: a[i], a: ++i, b: ++j });
    else if (dp[i + 1][j] >= dp[i][j + 1]) out.push({ kind: 'del', text: a[i], a: ++i, b: null });
    else out.push({ kind: 'add', text: b[j], a: null, b: ++j });
  }
  while (i < n) out.push({ kind: 'del', text: a[i], a: ++i, b: null });
  while (j < m) out.push({ kind: 'add', text: b[j], a: null, b: ++j });
  return out;
}
/** only the changed lines with `ctx` lines around them; gaps become null */
export function hunks(d: DiffLine[], ctx = 3): (DiffLine | null)[] {
  const keep = new Set<number>();
  d.forEach((l, i) => {
    if (l.kind !== 'same') for (let k = Math.max(0, i - ctx); k <= Math.min(d.length - 1, i + ctx); k++) keep.add(k);
  });
  const out: (DiffLine | null)[] = [];
  let last = -1;
  for (const i of [...keep].sort((x, y) => x - y)) {
    if (last >= 0 && i > last + 1) out.push(null);
    out.push(d[i]);
    last = i;
  }
  return out;
}

export const OP_LABEL: Record<VolumeOp['kind'], string> = { empty: 'Empty', copy: 'Copy', restore: 'Restore' };
export const STEP_LABEL: Record<VolumeOpStepName, string> = { stop: 'Stop containers', snapshot: 'Snapshot first', copy: 'Copy data', empty: 'Delete contents', restore: 'Restore data', start: 'Start containers' };

/** upload one file into a volume (missing folders are created by the agent) */
export function uploadToVolume(siteSlug: string, volumeSlug: string, path: string, file: Blob, overwrite: boolean, onProgress?: (f: number) => void) {
  const url = toolsPath('uploadVolumeFile', { siteSlug, volumeSlug }, { path, overwrite: overwrite ? '1' : undefined });
  return uploadRawTo<{ entry: unknown }>({ op: 'uploadVolumeFile', method: TOOLS_API.uploadVolumeFile.method, auth: TOOLS_API.uploadVolumeFile.auth }, url, file, onProgress);
}
