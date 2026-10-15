<script lang="ts">
  /** Uptime heatmap: green ≥ 99 %, amber < 99 %, red < 95 %, grey = no data. group = cells merged per column. */
  import type { UptimeCell } from '@opm/shared/types';
  import { fmtDateTime } from '../format';
  let { cells, group = 1, label, stepMin }: { cells: UptimeCell[]; group?: number; label: string; stepMin: number } = $props();
  const cols = $derived.by(() => {
    const out: { t: string; up: number; samples: number }[] = [];
    for (let i = 0; i < cells.length; i += group) {
      const g = cells.slice(i, i + group);
      const samples = g.reduce((a, c) => a + c.samples, 0);
      const up = samples ? g.reduce((a, c) => a + c.up * c.samples, 0) / samples : 0;
      out.push({ t: g[0].t, up, samples });
    }
    return out;
  });
  const tone = (c: { up: number; samples: number }) => (c.samples === 0 ? 'none' : c.up >= 0.99 ? 'ok' : c.up >= 0.95 ? 'warn' : 'bad');
  const avg = $derived.by(() => {
    const s = cells.reduce((a, c) => a + c.samples, 0);
    return s ? cells.reduce((a, c) => a + c.up * c.samples, 0) / s : null;
  });
</script>

<div class="hm-row">
  <div class="lbl mono small" title={label}>{label}</div>
  <div class="hm" style="grid-template-columns: repeat({cols.length}, minmax(0, 1fr)); gap: {cols.length > 60 ? 1 : 2}px" role="img" aria-label="{label}: uptime {avg === null ? 'no data' : `${(avg * 100).toFixed(2)} %`}">
    {#each cols as c, i (i)}<i class={tone(c)} title="{fmtDateTime(c.t)} · {group * stepMin >= 60 ? `${(group * stepMin) / 60} h` : `${group * stepMin} min`} · {c.samples ? `${(c.up * 100).toFixed(1)} % up` : 'no data'}"></i>{/each}
  </div>
  <div class="pct small mono" class:ok-text={avg !== null && avg >= 0.99} class:warn-text={avg !== null && avg < 0.99 && avg >= 0.95} class:bad-text={avg !== null && avg < 0.95}>{avg === null ? '—' : `${(avg * 100).toFixed(2)} %`}</div>
</div>

<style>
  .hm-row {
    display: grid;
    grid-template-columns: 110px minmax(0, 1fr) 64px;
    gap: 10px;
    align-items: center;
  }
  .lbl {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .hm {
    display: grid;
    gap: 2px;
    height: 22px;
  }
  .hm i {
    display: block;
    border-radius: 2px;
    min-width: 1px;
    background: var(--heat-none);
  }
  .hm i.ok {
    background: var(--green);
  }
  .hm i.warn {
    background: var(--amber);
  }
  .hm i.bad {
    background: var(--red);
  }
  .hm i:hover {
    outline: 2px solid var(--text);
  }
  .pct {
    text-align: right;
  }
  @media (max-width: 600px) {
    .hm-row {
      grid-template-columns: 70px minmax(0, 1fr) 54px;
      gap: 6px;
    }
  }
</style>
