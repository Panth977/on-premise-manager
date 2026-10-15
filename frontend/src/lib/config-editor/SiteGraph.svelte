<script lang="ts">
  import type { Site } from '@opm/shared';
  import { siteOrder } from '@opm/shared';
  /** Site dependency graph: arrow from a site to what it depends on; cycles in red. Dependencies are drawn left. */
  let { sites, base }: { sites: Site[]; base: string } = $props();
  const W = 168;
  const H = 52;
  const GX = 64;
  const GY = 22;
  const order = $derived(siteOrder({ sites }));
  const cyc = $derived(new Set('cycle' in order ? order.cycle : []));
  const layout = $derived.by(() => {
    const by = new Map(sites.map((s) => [s.siteSlug, s]));
    const level = new Map<string, number>();
    const visit = (slug: string, stack: Set<string>): number => {
      if (level.has(slug)) return level.get(slug)!;
      if (stack.has(slug)) return 0;
      stack.add(slug);
      const deps = (by.get(slug)?.config.depends ?? []).map((d) => d.siteSlug).filter((d) => by.has(d) && d !== slug);
      const l = deps.length ? Math.max(...deps.map((d) => visit(d, stack))) + 1 : 0;
      stack.delete(slug);
      level.set(slug, l);
      return l;
    };
    for (const s of sites) visit(s.siteSlug, new Set());
    const cols: string[][] = [];
    for (const s of [...sites].sort((a, b) => a.siteSlug.localeCompare(b.siteSlug))) (cols[level.get(s.siteSlug)!] ??= []).push(s.siteSlug);
    const pos = new Map<string, { x: number; y: number }>();
    cols.forEach((col, ci) => col.forEach((slug, ri) => pos.set(slug, { x: 12 + ci * (W + GX), y: 12 + ri * (H + GY) })));
    const width = 24 + Math.max(1, cols.length) * (W + GX) - GX;
    const height = 24 + Math.max(1, ...cols.map((c) => c.length)) * (H + GY) - GY;
    const edges: { from: string; to: string; bad: boolean; d: string }[] = [];
    for (const s of sites)
      for (const d of s.config.depends) {
        const a = pos.get(s.siteSlug);
        const b = pos.get(d.siteSlug);
        if (!a || !b) continue;
        const bad = cyc.has(s.siteSlug) && cyc.has(d.siteSlug);
        let path: string;
        if (s.siteSlug === d.siteSlug) path = `M ${a.x + W / 2} ${a.y} c 30 -30, 60 -10, 30 ${H / 2}`;
        else if (b.x < a.x) {
          const x1 = a.x;
          const y1 = a.y + H / 2;
          const x2 = b.x + W;
          const y2 = b.y + H / 2;
          path = `M ${x1} ${y1} C ${x1 - GX / 2} ${y1}, ${x2 + GX / 2} ${y2}, ${x2 + 4} ${y2}`;
        } else {
          const x1 = a.x + W / 2;
          const y1 = a.y + H;
          const x2 = b.x + W / 2;
          const y2 = b.y + H;
          path = `M ${x1} ${y1} C ${x1} ${y1 + 40}, ${x2 + 40} ${y2 + 40}, ${x2 + 6} ${y2 + 4}`;
        }
        edges.push({ from: s.siteSlug, to: d.siteSlug, bad, d: path });
      }
    return { pos, width, height: height + 8, edges };
  });
</script>

{#if !sites.length}
  <p class="faint small">No sites yet.</p>
{:else}
  <div class="graph" data-testid="site-graph">
    <svg width={layout.width} height={layout.height} viewBox="0 0 {layout.width} {layout.height}" role="img" aria-label="Site dependency graph">
      <defs>
        <marker id="arr" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0 0 L10 5 L0 10 z" fill="var(--text-3)" /></marker>
        <marker id="arr-bad" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0 0 L10 5 L0 10 z" fill="var(--red)" /></marker>
      </defs>
      {#each layout.edges as e, i (i)}
        <path d={e.d} fill="none" stroke={e.bad ? 'var(--red)' : 'var(--text-3)'} stroke-width="1.6" marker-end="url(#{e.bad ? 'arr-bad' : 'arr'})" />
      {/each}
      {#each sites as s (s.siteSlug)}
        {@const p = layout.pos.get(s.siteSlug)!}
        <a href="{base}/sites/{s.siteSlug}/containers">
          <rect x={p.x} y={p.y} width={W} height={H} rx="10" class="node" class:bad={cyc.has(s.siteSlug)} />
          <text x={p.x + 12} y={p.y + 21} class="t1">{s.siteSlug}</text>
          <text x={p.x + 12} y={p.y + 38} class="t2">{s.config.containers.length} container{s.config.containers.length === 1 ? '' : 's'} · {s.config.edge.routes.length} route{s.config.edge.routes.length === 1 ? '' : 's'}</text>
        </a>
      {/each}
    </svg>
  </div>
  <p class="xs faint legend">Arrow = “depends on”. {#if 'order' in order}Apply order: <span class="mono">{order.order.join(' → ')}</span>{:else}<span class="text-red">Cycle: {order.cycle.join(' → ')}</span>{/if}</p>
{/if}

<style>
  .graph {
    overflow-x: auto;
    padding: 4px 0;
  }
  .node {
    fill: var(--surface);
    stroke: var(--border-strong);
    stroke-width: 1.2;
  }
  a:hover .node,
  a:focus .node {
    stroke: var(--accent);
    fill: var(--accent-soft);
  }
  .node.bad {
    stroke: var(--red);
    fill: var(--red-soft);
  }
  .t1 {
    font-family: var(--font-mono);
    font-size: 13px;
    font-weight: 600;
    fill: var(--text);
  }
  .t2 {
    font-size: 11px;
    fill: var(--text-3);
  }
  .legend {
    margin-top: 4px;
  }
</style>
