<script lang="ts" module>
  export interface HeatCell {
    /** 0..1, null = no data */
    value: number | null;
    label: string;
  }
  export function heatTone(v: number | null): 'none' | 'green' | 'amber' | 'red' {
    if (v === null) return 'none';
    if (v >= 0.99) return 'green';
    if (v >= 0.95) return 'amber';
    return 'red';
  }
</script>

<script lang="ts">
  let { cells, height = 26, label = 'Uptime', testid }: { cells: HeatCell[]; height?: number; label?: string; testid?: string } = $props();
</script>

<div class="hm" style="--n:{cells.length};height:{height}px" role="img" aria-label={label} data-testid={testid}>
  {#each cells as c, i (i)}
    <span class="cell {heatTone(c.value)}" data-tip="{c.label}: {c.value === null ? 'no data' : (c.value * 100).toFixed(c.value >= 0.995 || c.value === 0 ? 0 : 1) + ' %'}"></span>
  {/each}
</div>

<style>
  .hm {
    display: grid;
    grid-template-columns: repeat(var(--n), minmax(0, 1fr));
    gap: 2px;
    width: 100%;
  }
  .cell {
    border-radius: 2px;
    background: var(--heat-none);
    min-width: 2px;
  }
  .cell.green {
    background: var(--green);
  }
  .cell.amber {
    background: var(--amber);
  }
  .cell.red {
    background: var(--red);
  }
  .cell:hover {
    outline: 2px solid var(--text);
    outline-offset: 0;
  }
</style>
