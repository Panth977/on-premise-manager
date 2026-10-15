<script lang="ts" module>
  export interface PlanNode {
    'Node Type'?: string;
    'Relation Name'?: string;
    'Index Name'?: string;
    Alias?: string;
    'Startup Cost'?: number;
    'Total Cost'?: number;
    'Plan Rows'?: number;
    'Actual Total Time'?: number;
    'Actual Rows'?: number;
    'Actual Loops'?: number;
    Filter?: string;
    'Index Cond'?: string;
    'Sort Key'?: string[];
    'Rows Removed by Filter'?: number;
    Plans?: PlanNode[];
    [k: string]: unknown;
  }
  /** time spent in the node itself (actual total − children), ms */
  export function selfTime(n: PlanNode): number | null {
    if (n['Actual Total Time'] === undefined) return null;
    const loops = n['Actual Loops'] ?? 1;
    const kids = (n.Plans ?? []).reduce((a, c) => a + (c['Actual Total Time'] ?? 0) * (c['Actual Loops'] ?? 1), 0);
    return Math.max(0, n['Actual Total Time'] * loops - kids);
  }
</script>

<script lang="ts">
  /** pg EXPLAIN (FORMAT JSON) as a tree (§21 #97): node, relation / index, cost, rows (estimate vs actual), self time bar. */
  import Self from './PlanTree.svelte';
  let { node, max, depth = 0 }: { node: PlanNode; max: number; depth?: number } = $props();
  const st = $derived(selfTime(node));
  const est = $derived(node['Plan Rows']);
  const act = $derived(node['Actual Rows']);
  const off = $derived(est !== undefined && act !== undefined && act > 0 ? Math.max(est / act, act / Math.max(1, est)) : 1);
  const extras = $derived(Object.entries(node).filter(([k]) => ['Filter', 'Index Cond', 'Hash Cond', 'Join Filter', 'Recheck Cond', 'Rows Removed by Filter', 'Sort Method', 'Sort Space Used', 'Shared Hit Blocks', 'Shared Read Blocks', 'Merge Cond', 'Group Key'].includes(k)));
</script>

<li class="pn" data-testid="plan-node">
  <div class="line">
    <span class="nt">{node['Node Type']}</span>
    {#if node['Relation Name']}<span class="rel mono">on {node['Relation Name']}{node.Alias && node.Alias !== node['Relation Name'] ? ` ${node.Alias}` : ''}</span>{/if}
    {#if node['Index Name']}<span class="idx mono">using {node['Index Name']}</span>{/if}
    {#if node['Sort Key']}<span class="faint mono">by {node['Sort Key'].join(', ')}</span>{/if}
    <span class="grow"></span>
    <span class="m" title="startup..total cost">cost {node['Startup Cost']}..{node['Total Cost']}</span>
    <span class="m" title="estimated rows{act !== undefined ? ' / actual rows' : ''}">rows {est ?? '?'}{act !== undefined ? ` / ${act}` : ''}{#if off >= 10}<span class="warn-text" title="the estimate is off by ×{Math.round(off)}"> ⚠</span>{/if}</span>
    {#if st !== null}<span class="m strong" title="time in this node (excluding children)">{st.toFixed(3)} ms</span>{/if}
  </div>
  {#if st !== null && max > 0}<div class="bar"><i style:width="{Math.max(1, (st / max) * 100)}%"></i></div>{/if}
  {#if extras.length}
    <div class="ex">
      {#each extras as [k, v] (k)}<span><span class="faint">{k}:</span> <span class="mono">{Array.isArray(v) ? v.join(', ') : String(v)}</span></span>{/each}
    </div>
  {/if}
  {#if node.Plans?.length}
    <ul class="kids">
      {#each node.Plans as c, i (i)}<Self node={c} {max} depth={depth + 1} />{/each}
    </ul>
  {/if}
</li>

<style>
  .pn {
    list-style: none;
    padding: 6px 0 2px;
  }
  .line {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 10px;
    align-items: baseline;
  }
  .nt {
    font-weight: var(--fw-semibold);
    color: var(--accent-text);
  }
  .rel,
  .idx {
    font-size: 12.5px;
  }
  .m {
    font-size: var(--fs-xs);
    color: var(--text-2);
    font-family: var(--font-mono);
    white-space: nowrap;
  }
  .bar {
    height: 4px;
    background: var(--gray-soft);
    border-radius: 2px;
    margin: 4px 0 2px;
    max-width: 420px;
  }
  .bar i {
    display: block;
    height: 100%;
    background: var(--amber);
    border-radius: 2px;
  }
  .ex {
    display: flex;
    flex-wrap: wrap;
    gap: 2px 14px;
    font-size: var(--fs-xs);
    margin-top: 2px;
  }
  .kids {
    margin: 2px 0 0 10px;
    padding-left: 14px;
    border-left: 2px solid var(--border);
  }
</style>
