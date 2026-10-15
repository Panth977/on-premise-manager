/** Labels, tones and grouping for branch items / commits (§21 #104). Pure helpers shared by the branch components. */
import type { CommitKind, MergeItem, MergeKind } from '@opm/shared/branch';

export const KIND_LABEL: Record<MergeKind, string> = {
  add: 'Only in source',
  remove: 'Only in target',
  change: 'Differs',
  theirs: 'Changed in source',
  ours: 'Changed in target',
  conflict: 'Conflict',
  skipped: 'Skipped earlier',
};
export const KIND_TONE: Record<MergeKind, string> = {
  add: 'green',
  remove: 'red',
  change: 'blue',
  theirs: 'accent',
  ours: 'outline',
  conflict: 'amber',
  skipped: 'violet',
};
export const EFFECT_LABEL: Record<MergeItem['effect'], string> = { add: 'adds', change: 'changes', remove: 'removes' };
export const COMMIT_KIND_LABEL: Record<CommitKind, string> = {
  create: 'created',
  change: 'commit',
  merge: 'merge',
  'cherry-pick': 'cherry-pick',
  revert: 'revert',
  reset: 'reset',
  drift: 'server changes',
  rollback: 'rollback',
  migrated: 'migrated',
  legacy: 'legacy',
};
export const COMMIT_KIND_TONE: Record<CommitKind, string> = {
  create: 'green',
  change: 'outline',
  merge: 'accent',
  'cherry-pick': 'accent',
  revert: 'amber',
  reset: 'amber',
  drift: 'violet',
  rollback: 'red',
  migrated: 'outline',
  legacy: 'outline',
};

export type FilterKey = 'all' | 'source' | 'conflict' | 'target' | 'removal' | 'skipped';
export const FILTERS: { value: FilterKey; label: string }[] = [
  { value: 'all', label: 'All' },
  { value: 'source', label: 'Source changes' },
  { value: 'conflict', label: 'Conflicts' },
  { value: 'target', label: 'Target only' },
  { value: 'removal', label: 'Removals' },
  { value: 'skipped', label: 'Skipped' },
];
export function inFilter(i: MergeItem, f: FilterKey): boolean {
  if (f === 'all') return true;
  if (f === 'source') return i.kind === 'theirs' || i.kind === 'add' || i.kind === 'change';
  if (f === 'conflict') return i.kind === 'conflict';
  if (f === 'target') return i.kind === 'ours' || i.kind === 'remove';
  if (f === 'removal') return i.effect === 'remove';
  return i.kind === 'skipped';
}

/** What happens to the target when a removal is taken (the "Removed by this merge" group). */
export function removalText(i: MergeItem, scope: 'template' | 'deployment' | 'agent'): string {
  const k = i.key;
  const local = scope === 'agent';
  if (/^sites\/[^/]+\/containers\/[^/]+$/.test(k)) return local ? 'container stopped and removed on this site; its data folders stay' : 'container removed from the config (sites stop and remove it when they merge; data folders stay)';
  if (/^sites\/[^/]+\/volumes\/[^/]+$/.test(k)) return 'volume definition removed; the data folder stays on the server (orphan)';
  if (/^sites\/[^/]+$/.test(k)) return local ? 'every container of this site is stopped and removed; data folders stay' : 'site removed from the config';
  if (k.startsWith('files/')) return local ? 'file removed; its copy on this site is deleted' : 'file removed';
  if (/\/edge\/routes\//.test(k)) return 'route removed';
  if (/\/edge\/hosts\//.test(k)) return 'host removed';
  if (/\/env\//.test(k)) return 'environment variable removed';
  if (/\/content\//.test(k) || k.endsWith('/content')) return 'starter content entry removed (existing data is never touched)';
  if (k.startsWith('variables/')) return 'variable removed';
  if (k.startsWith('secrets/')) return 'secret removed';
  if (/\/devtools\//.test(k)) return 'dev-tool preset removed';
  return 'removed';
}

export interface ItemGroup {
  id: string;
  title: string;
  items: MergeItem[];
}
const SEC_TITLE: Record<string, string> = { sites: 'Settings', edge: 'Edge', volumes: 'Volumes', containers: 'Containers', devTools: 'Dev tools' };
/** Groups: Variables · Secrets · Files · per site Settings / Edge / Volumes / Containers / Dev tools · Sites (whole). */
export function groupItems(items: readonly MergeItem[]): ItemGroup[] {
  const order: string[] = [];
  const map = new Map<string, ItemGroup>();
  const put = (id: string, title: string, i: MergeItem) => {
    let g = map.get(id);
    if (!g) {
      g = { id, title, items: [] };
      map.set(id, g);
      order.push(id);
    }
    g.items.push(i);
  };
  for (const i of items) {
    if (i.section === 'variables') put('variables', 'Variables', i);
    else if (i.section === 'secrets') put('secrets', 'Secrets', i);
    else if (i.section === 'files') put('files', 'Files', i);
    else if (i.siteSlug && /^sites\/[^/]+$/.test(i.key)) put('sites', 'Sites', i);
    else put(`${i.siteSlug}:${i.section}`, `${i.siteSlug} · ${SEC_TITLE[i.section] ?? i.section}`, i);
  }
  const rank = (id: string) => (id === 'variables' ? 0 : id === 'secrets' ? 1 : id === 'files' ? 2 : id === 'sites' ? 3 : 4);
  return order.map((id) => map.get(id)!).sort((a, b) => rank(a.id) - rank(b.id));
}

/** Short display of a slot value (JSON for objects, quotes for strings). */
export function show(v: unknown, has: boolean): string {
  if (!has) return '—';
  if (v === null) return 'null';
  if (typeof v === 'string') return v;
  if (typeof v === 'number' || typeof v === 'boolean') return String(v);
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
}

/** Relative time ("5 min ago"). */
export function relTime(iso: string, now = Date.now()): string {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return iso;
  const s = Math.round((now - t) / 1000);
  if (s < 45) return 'just now';
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min ago`;
  const h = Math.round(m / 60);
  if (h < 36) return `${h} h ago`;
  const d = Math.round(h / 24);
  if (d < 45) return `${d} d ago`;
  return new Date(t).toISOString().slice(0, 10);
}

/** keys that depend on `key` (their requires include it) */
export const dependantsOf = (items: readonly MergeItem[], key: string) => items.filter((i) => i.requires.includes(key)).map((i) => i.key);
