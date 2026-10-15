<script lang="ts">
  /** Tiny inline trend line (no axes). null = gap. tone colours from the tokens. */
  let { values, width = 96, height = 24, max = null, tone = 'accent', label }: { values: (number | null)[]; width?: number; height?: number; max?: number | null; tone?: 'accent' | 'amber' | 'red' | 'green' | 'blue' | 'violet'; label?: string } = $props();
  const geo = $derived.by(() => {
    const nums = values.filter((v): v is number => v !== null && Number.isFinite(v));
    if (nums.length < 2) return null;
    const hi = Math.max(max ?? 0, ...nums) || 1;
    const lo = max !== null ? 0 : Math.min(...nums) * 0.9;
    const span = hi - lo || 1;
    const dx = width / Math.max(1, values.length - 1);
    let line = '';
    let area = '';
    let seg: [number, number][] = [];
    const flush = () => {
      if (seg.length > 1) {
        line += 'M' + seg.map(([x, y]) => `${x.toFixed(1)},${y.toFixed(1)}`).join('L');
        area += `M${seg[0][0].toFixed(1)},${height}L` + seg.map(([x, y]) => `${x.toFixed(1)},${y.toFixed(1)}`).join('L') + `L${seg[seg.length - 1][0].toFixed(1)},${height}Z`;
      }
      seg = [];
    };
    values.forEach((v, i) => {
      if (v === null || !Number.isFinite(v)) return flush();
      seg.push([i * dx, height - 1.5 - ((v - lo) / span) * (height - 3)]);
    });
    flush();
    const last = [...values].reverse().find((v) => v !== null);
    const li = values.lastIndexOf(last ?? null);
    return { line, area, end: last !== undefined && last !== null ? [li * dx, height - 1.5 - ((last - lo) / span) * (height - 3)] : null };
  });
</script>

<svg class="spark {tone}" {width} {height} viewBox="0 0 {width} {height}" role={label ? 'img' : undefined} aria-label={label} aria-hidden={label ? undefined : 'true'}>
  {#if geo}
    <path class="a" d={geo.area} />
    <path class="l" d={geo.line} />
    {#if geo.end}<circle cx={geo.end[0]} cy={geo.end[1]} r="2" />{/if}
  {:else}
    <line x1="0" x2={width} y1={height - 2} y2={height - 2} class="none" />
  {/if}
</svg>

<style>
  .spark {
    display: block;
    overflow: visible;
    --c: var(--accent);
  }
  .spark.amber {
    --c: var(--amber);
  }
  .spark.red {
    --c: var(--red);
  }
  .spark.green {
    --c: var(--green);
  }
  .spark.blue {
    --c: var(--blue);
  }
  .spark.violet {
    --c: var(--violet);
  }
  .l {
    fill: none;
    stroke: var(--c);
    stroke-width: 1.5;
    stroke-linejoin: round;
    stroke-linecap: round;
  }
  .a {
    fill: var(--c);
    opacity: 0.12;
  }
  circle {
    fill: var(--c);
  }
  .none {
    stroke: var(--border-strong);
    stroke-dasharray: 2 3;
  }
</style>
