<script lang="ts">
  /**
   * Time-series line chart (hand-rolled SVG): y axis with 4 nice ticks, time axis, gaps for null, an optional
   * threshold line, hover crosshair + tooltip with every series value. Width follows the container.
   */
  export interface Series {
    label: string;
    values: (number | null)[];
    color: string; //  CSS colour or var(--…)
    dashed?: boolean;
    area?: boolean;
  }
  let {
    t,
    series,
    height = 180,
    yFmt = (v: number) => String(Math.round(v)),
    yMax = null,
    threshold = null,
    testid,
    label,
  }: { t: number[]; series: Series[]; height?: number; yFmt?: (v: number) => string; yMax?: number | null; threshold?: { value: number; label: string } | null; testid?: string; label: string } = $props();

  let width = $state(600);
  let hover = $state<number | null>(null);
  const PAD = { l: 52, r: 12, t: 10, b: 22 };

  function nice(max: number): number[] {
    if (max <= 0) return [0, 1];
    const raw = max / 4;
    const mag = 10 ** Math.floor(Math.log10(raw));
    const step = [1, 2, 2.5, 5, 10].map((m) => m * mag).find((s) => s >= raw) ?? raw;
    return Array.from({ length: Math.ceil(max / step) + 1 }, (_, i) => i * step);
  }
  const geo = $derived.by(() => {
    const n = t.length;
    const w = Math.max(120, width);
    const iw = w - PAD.l - PAD.r;
    const ih = height - PAD.t - PAD.b;
    let max = yMax ?? 0;
    if (yMax === null) for (const s of series) for (const v of s.values) if (v !== null && Number.isFinite(v) && v > max) max = v;
    if (threshold && yMax === null) max = Math.max(max, threshold.value * 1.05);
    const ticks = nice(max || 1);
    const top = ticks[ticks.length - 1];
    const x = (i: number) => PAD.l + (n <= 1 ? 0 : (i / (n - 1)) * iw);
    const y = (v: number) => PAD.t + ih - (v / top) * ih;
    const paths = series.map((s) => {
      let d = '';
      let a = '';
      let seg: string[] = [];
      let first = -1;
      let lastI = -1;
      const flush = () => {
        if (seg.length > 1) {
          d += 'M' + seg.join('L');
          if (s.area) a += `M${x(first).toFixed(1)},${y(0).toFixed(1)}L${seg.join('L')}L${x(lastI).toFixed(1)},${y(0).toFixed(1)}Z`;
        }
        seg = [];
        first = -1;
      };
      s.values.forEach((v, i) => {
        if (v === null || !Number.isFinite(v)) return flush();
        if (first < 0) first = i;
        lastI = i;
        seg.push(`${x(i).toFixed(1)},${y(Math.min(v, top)).toFixed(1)}`);
      });
      flush();
      return { d, a };
    });
    // time ticks: ~5 labels
    const span = n > 1 ? t[n - 1] - t[0] : 0;
    const xt: { x: number; label: string }[] = [];
    if (n > 1) {
      const k = Math.max(1, Math.floor(n / (w < 420 ? 3 : 5)));
      for (let i = 0; i < n; i += k) {
        const d = new Date(t[i] * 1000);
        const hh = `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
        xt.push({ x: x(i), label: span > 2 * 86400 ? `${d.getMonth() + 1}/${d.getDate()} ${hh}` : hh });
      }
    }
    return { w, iw, ih, ticks, top, x, y, paths, xt };
  });
  function onMove(e: PointerEvent) {
    const r = (e.currentTarget as SVGElement).getBoundingClientRect();
    const px = e.clientX - r.left;
    const n = t.length;
    if (n < 2) return;
    const i = Math.round(((px - PAD.l) / geo.iw) * (n - 1));
    hover = Math.max(0, Math.min(n - 1, i));
  }
  const tip = $derived.by(() => {
    if (hover === null) return null;
    const d = new Date(t[hover] * 1000);
    return {
      x: geo.x(hover),
      time: d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: t.length > 1 && t[1] - t[0] < 60 ? '2-digit' : undefined }),
      rows: series.map((s) => ({ label: s.label, color: s.color, v: s.values[hover!] })),
    };
  });
</script>

<div class="lc" bind:clientWidth={width} data-testid={testid}>
  <svg width={geo.w} {height} role="img" aria-label={label} onpointermove={onMove} onpointerleave={() => (hover = null)}>
    {#each geo.ticks as tk (tk)}
      <line class="grid" x1={PAD.l} x2={geo.w - PAD.r} y1={geo.y(tk)} y2={geo.y(tk)} />
      <text class="yl" x={PAD.l - 6} y={geo.y(tk) + 3.5}>{yFmt(tk)}</text>
    {/each}
    {#each geo.xt as xt, i (i)}
      <text class="xl" x={xt.x} y={height - 6}>{xt.label}</text>
    {/each}
    {#if threshold && threshold.value <= geo.top}
      <line class="th" x1={PAD.l} x2={geo.w - PAD.r} y1={geo.y(threshold.value)} y2={geo.y(threshold.value)} />
      <text class="thl" x={geo.w - PAD.r} y={geo.y(threshold.value) - 4}>{threshold.label}</text>
    {/if}
    {#each series as s, i (s.label)}
      {#if geo.paths[i].a}<path d={geo.paths[i].a} fill={s.color} opacity="0.1" />{/if}
      <path d={geo.paths[i].d} fill="none" stroke={s.color} stroke-width="1.6" stroke-dasharray={s.dashed ? '4 3' : undefined} stroke-linejoin="round" />
    {/each}
    {#if tip}
      <line class="cross" x1={tip.x} x2={tip.x} y1={PAD.t} y2={PAD.t + geo.ih} />
      {#each series as s (s.label)}
        {@const v = s.values[hover!]}
        {#if v !== null && v !== undefined}<circle cx={tip.x} cy={geo.y(Math.min(v, geo.top))} r="3" fill={s.color} />{/if}
      {/each}
    {/if}
  </svg>
  {#if tip}
    <div class="tip" style="left:{Math.min(tip.x + 10, geo.w - 170)}px">
      <div class="tt">{tip.time}</div>
      {#each tip.rows as r (r.label)}
        <div class="tr"><i style="background:{r.color}"></i><span class="grow">{r.label}</span><b>{r.v === null || r.v === undefined ? '—' : yFmt(r.v)}</b></div>
      {/each}
    </div>
  {/if}
  <div class="legend">
    {#each series as s (s.label)}<span><i style="background:{s.color}" class:dash={s.dashed}></i>{s.label}</span>{/each}
  </div>
</div>

<style>
  .lc {
    position: relative;
    width: 100%;
    min-width: 0;
  }
  svg {
    display: block;
    touch-action: none;
  }
  .grid {
    stroke: var(--border);
    stroke-width: 1;
  }
  .yl,
  .xl,
  .thl {
    font-size: 10.5px;
    fill: var(--text-3);
    font-family: var(--font-sans);
  }
  .yl {
    text-anchor: end;
  }
  .xl {
    text-anchor: middle;
  }
  .th {
    stroke: var(--amber);
    stroke-dasharray: 4 3;
  }
  .thl {
    text-anchor: end;
    fill: var(--amber-text);
  }
  .cross {
    stroke: var(--text-3);
    stroke-dasharray: 2 2;
  }
  .tip {
    position: absolute;
    top: 6px;
    width: 160px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    box-shadow: var(--shadow-md, 0 4px 14px rgba(0, 0, 0, 0.12));
    padding: 6px 8px;
    font-size: 11.5px;
    pointer-events: none;
    z-index: 2;
  }
  .tt {
    color: var(--text-3);
    margin-bottom: 3px;
  }
  .tr {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .tr i,
  .legend i {
    width: 8px;
    height: 8px;
    border-radius: 2px;
    flex: none;
  }
  .legend {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 14px;
    font-size: 11.5px;
    color: var(--text-2);
    padding: 2px 0 0 52px;
  }
  .legend span {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  .legend i.dash {
    height: 2px;
  }
</style>
