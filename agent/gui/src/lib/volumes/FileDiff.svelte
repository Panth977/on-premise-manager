<script lang="ts">
  /**
   * §21 #101 per-file diff of an "Apply cloud version" plan item: text = side by side or unified (fetched with
   * volumeSyncFile side=live|cloud|base); binary / large = sizes, hashes and modes of base / live / cloud.
   */
  import { untrack } from 'svelte';
  import type { VolumePlanItem, VolumePlanSide } from '@opm/shared/volume-content';
  import { formatContentBytes, formatMode } from '@opm/shared/volume-content';
  import { errMessage } from '$lib/api';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import { hunks, lineDiff, type DiffLine } from './vol';
  import { fetchSide, shortHash, type SideContent } from './sync';

  interface Props {
    siteSlug: string;
    volumeSlug: string;
    item: VolumePlanItem;
    baseKnown: boolean;
  }
  let { siteSlug, volumeSlug, item, baseKnown }: Props = $props();
  type Side = 'base' | 'live' | 'cloud';
  type Pair = 'live-cloud' | 'base-live' | 'base-cloud';
  let pair = $state<Pair>('live-cloud');
  let view = $state<'split' | 'unified'>(typeof window !== 'undefined' && window.innerWidth < 760 ? 'unified' : 'split');
  const sides = $derived(pair === 'live-cloud' ? (['live', 'cloud'] as const) : pair === 'base-live' ? (['base', 'live'] as const) : (['base', 'cloud'] as const));
  const SIDE_LABEL: Record<Side, string> = { base: 'Base (last snapshot the cloud knew)', live: 'Live (on the site now)', cloud: 'Cloud version' };
  const SHORT: Record<Side, string> = { base: 'Base', live: 'Live', cloud: 'Cloud' };
  const meta = (s: Side): VolumePlanSide | null => item[s];

  const cache = new Map<string, Promise<SideContent>>();
  function load(s: Side): Promise<SideContent> {
    const k = `${item.path}|${s}`;
    let p = cache.get(k);
    if (!p) {
      p = fetchSide(siteSlug, volumeSlug, s, item.path, meta(s)?.size ?? null);
      cache.set(k, p);
    }
    return p;
  }
  let left = $state<SideContent | null>(null);
  let right = $state<SideContent | null>(null);
  let err = $state<string | null>(null);
  $effect(() => {
    const [a, b] = sides;
    void item.path;
    untrack(() => {
      left = right = null;
      err = null;
      Promise.all([load(a), load(b)])
        .then(([x, y]) => ((left = x), (right = y)))
        .catch((e) => (err = errMessage(e)));
    });
  });
  const textual = $derived(!!left && !!right && left.kind !== 'binary' && left.kind !== 'large' && right.kind !== 'binary' && right.kind !== 'large');
  const diff = $derived.by((): DiffLine[] => {
    if (!textual || !left || !right) return [];
    const a = left.kind === 'text' ? left.text : '';
    const b = right.kind === 'text' ? right.text : '';
    return lineDiff(a, b);
  });
  const stats = $derived({ add: diff.filter((l) => l.kind === 'add').length, del: diff.filter((l) => l.kind === 'del').length });
  const unified = $derived(hunks(diff));
  /** side-by-side rows: runs of deletions paired with the following additions */
  type Row = { l: DiffLine | null; r: DiffLine | null; changed: boolean } | null;
  const rows = $derived.by((): Row[] => {
    const out: { l: DiffLine | null; r: DiffLine | null; changed: boolean }[] = [];
    let i = 0;
    while (i < diff.length) {
      const d = diff[i];
      if (d.kind === 'same') {
        out.push({ l: d, r: d, changed: false });
        i++;
        continue;
      }
      const dels: DiffLine[] = [];
      const adds: DiffLine[] = [];
      while (i < diff.length && diff[i].kind === 'del') dels.push(diff[i++]);
      while (i < diff.length && diff[i].kind === 'add') adds.push(diff[i++]);
      for (let k = 0; k < Math.max(dels.length, adds.length); k++) out.push({ l: dels[k] ?? null, r: adds[k] ?? null, changed: true });
    }
    const keep = new Set<number>();
    out.forEach((r, k) => {
      if (r.changed) for (let j = Math.max(0, k - 3); j <= Math.min(out.length - 1, k + 3); j++) keep.add(j);
    });
    const res: Row[] = [];
    let last = -1;
    for (const k of [...keep].sort((x, y) => x - y)) {
      if (last >= 0 && k > last + 1) res.push(null);
      res.push(out[k]);
      last = k;
    }
    return res;
  });
  const pairOpts = $derived([
    { value: 'live-cloud' as const, label: 'Live ↔ Cloud' },
    { value: 'base-live' as const, label: 'Base → Live', disabled: !baseKnown || !item.base },
    { value: 'base-cloud' as const, label: 'Base → Cloud', disabled: !baseKnown || !item.base },
  ]);
</script>

<div class="fd" data-testid="file-diff-{item.path}">
  <div class="fdbar">
    <SegmentedControl size="sm" label="Compare" value={pair} options={pairOpts} onchange={(v) => (pair = v)} testid="diff-pair" />
    {#if textual}<SegmentedControl size="sm" label="Diff view" value={view} options={[{ value: 'split' as const, label: 'Side by side' }, { value: 'unified' as const, label: 'Unified' }]} onchange={(v) => (view = v)} testid="diff-view" />{/if}
    {#if textual && diff.length}<span class="small"><span class="ok-text">+{stats.add}</span> <span class="bad-text">−{stats.del}</span> lines</span>{/if}
  </div>
  {#if err}
    <div class="banner bad small" data-testid="diff-err"><Icon name="alert" /><div class="grow">{err}</div></div>
  {:else if !left || !right}
    <span class="skel" style="height:120px"></span>
  {:else if !textual}
    <div class="table-wrap">
      <table class="table compact" data-testid="diff-binary">
        <thead><tr><th></th><th class="num">Size</th><th>sha256</th><th>Mode</th></tr></thead>
        <tbody>
          {#each ['base', 'live', 'cloud'] as const as s (s)}
            {@const m = meta(s)}
            {#if s !== 'base' || baseKnown}
              <tr><td class="small">{SHORT[s]}</td><td class="num small">{m ? formatContentBytes(m.size) : '—'}</td><td class="mono xs">{m ? shortHash(m.sha256) : 'absent'}</td><td class="mono xs">{m ? `${m.mode} ${formatMode(m.mode)}` : ''}</td></tr>
            {/if}
          {/each}
        </tbody>
      </table>
    </div>
    <p class="tiny muted" style="margin:6px 0 0">{left.kind === 'large' || right.kind === 'large' ? 'Larger than 2 MiB' : 'Binary content'} — compared by size and hash only.</p>
  {:else}
    {#if (meta(sides[0])?.mode ?? null) !== (meta(sides[1])?.mode ?? null) && meta(sides[0]) && meta(sides[1])}
      <div class="tiny muted" data-testid="diff-mode">mode {meta(sides[0])!.mode} → {meta(sides[1])!.mode}</div>
    {/if}
    {#if diff.length === 0 || (stats.add === 0 && stats.del === 0)}
      <div class="muted small pad">{left.kind === 'absent' ? `Absent in ${SHORT[sides[0]]}` : right.kind === 'absent' ? `Absent in ${SHORT[sides[1]]}` : 'Same text'}{left.kind === 'absent' || right.kind === 'absent' ? ' (empty)' : ''}.</div>
    {:else if view === 'unified'}
      <div class="diff" data-testid="diff-unified">
        <div class="dh"><span>{SIDE_LABEL[sides[0]]}{left.kind === 'absent' ? ' — absent' : ''}</span><span>→ {SIDE_LABEL[sides[1]]}{right.kind === 'absent' ? ' — absent' : ''}</span></div>
        {#each unified as l, i (i)}
          {#if l === null}<div class="gap">…</div>{:else}<div class="dl {l.kind}"><span class="no">{l.a ?? ''}</span><span class="no">{l.b ?? ''}</span><span class="sg">{l.kind === 'add' ? '+' : l.kind === 'del' ? '−' : ' '}</span><span class="tx">{l.text}</span></div>{/if}
        {/each}
      </div>
    {:else}
      <div class="diff split" data-testid="diff-split">
        <div class="sh"><span>{SIDE_LABEL[sides[0]]}{left.kind === 'absent' ? ' — absent' : ''}</span><span>{SIDE_LABEL[sides[1]]}{right.kind === 'absent' ? ' — absent' : ''}</span></div>
        {#each rows as r, i (i)}
          {#if r === null}
            <div class="gap">…</div>
          {:else}
            <div class="sr">
              <div class="sc {r.changed ? (r.l ? 'del' : 'none') : ''}"><span class="no">{r.l?.a ?? ''}</span><span class="tx">{r.l?.text ?? ''}</span></div>
              <div class="sc {r.changed ? (r.r ? 'add' : 'none') : ''}"><span class="no">{r.r?.b ?? ''}</span><span class="tx">{r.r?.text ?? ''}</span></div>
            </div>
          {/if}
        {/each}
      </div>
    {/if}
  {/if}
</div>

<style>
  .fd {
    display: flex;
    flex-direction: column;
    gap: 8px;
    min-width: 0;
  }
  .fdbar {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
  }
  .pad {
    padding: 8px 2px;
  }
  .diff {
    font-family: var(--font-mono);
    font-size: 12px;
    line-height: 1.6;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    background: var(--code-bg);
    overflow: auto;
    max-height: 46vh;
  }
  .dh,
  .sh {
    position: sticky;
    top: 0;
    z-index: 1;
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 8px;
    padding: 4px 10px;
    background: var(--surface-2);
    border-bottom: 1px solid var(--border);
    font-family: var(--font-sans);
    font-size: var(--fs-xs);
    color: var(--text-2);
  }
  .dh {
    display: flex;
    justify-content: space-between;
  }
  .dl {
    display: flex;
    white-space: pre;
  }
  .dl.add,
  .sc.add {
    background: color-mix(in srgb, var(--green) 14%, transparent);
  }
  .dl.del,
  .sc.del {
    background: color-mix(in srgb, var(--red) 14%, transparent);
  }
  .sc.none {
    background: var(--surface-2);
  }
  .sr {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  }
  .sc {
    display: flex;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    min-width: 0;
  }
  .sc + .sc {
    border-left: 1px solid var(--border);
  }
  .no {
    flex: none;
    width: 40px;
    text-align: right;
    padding-right: 8px;
    color: var(--text-3);
    user-select: none;
  }
  .sg {
    width: 16px;
    flex: none;
    user-select: none;
  }
  .tx {
    padding-right: 8px;
    min-width: 0;
  }
  .gap {
    color: var(--text-3);
    padding: 0 12px;
    background: var(--surface-2);
  }
</style>
