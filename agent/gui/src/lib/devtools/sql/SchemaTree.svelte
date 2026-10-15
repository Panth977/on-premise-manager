<script lang="ts">
  /**
   * Schema sidebar (§21 #97): namespaces (system ones folded under "System"), tables / views with row estimate + size,
   * columns (🔑 PK, type, nullable), functions. Search. Row actions on hover / tap and a context menu (right-click):
   * Select first 100 rows · Insert name into the editor · Copy name · Details (indexes, constraints, DDL).
   */
  import type { SchemaNamespace, SchemaTable, SqlSchemaResponse } from '@opm/shared/devtools';
  import Icon from '$lib/ui/Icon.svelte';
  import { bytes } from '$lib/format';
  import { fmtCount } from '../grid';

  export type TreeAction = 'select' | 'insert' | 'copy' | 'details';
  interface Props {
    schema: SqlSchemaResponse | null;
    loading: boolean;
    error: string | null;
    onrefresh: () => void;
    onaction: (a: TreeAction, ns: SchemaNamespace, t: SchemaTable | null, text: string) => void;
    oninsert: (text: string) => void;
  }
  let { schema, loading, error, onrefresh, onaction, oninsert }: Props = $props();
  let q = $state('');
  let open = $state<Record<string, boolean>>({});
  let showSystem = $state(false);
  const isOpen = (k: string, def = false) => open[k] ?? def;
  const toggle = (k: string, def = false) => (open = { ...open, [k]: !isOpen(k, def) });
  const user = $derived((schema?.namespaces ?? []).filter((n) => !n.system));
  const sys = $derived((schema?.namespaces ?? []).filter((n) => n.system));
  const match = (s: string) => !q.trim() || s.toLowerCase().includes(q.trim().toLowerCase());
  const tablesOf = (n: SchemaNamespace) => n.tables.filter((t) => match(t.name) || t.columns.some((c) => match(c.name)));
  const qualified = (n: SchemaNamespace, t: SchemaTable) => (schema?.kind === 'sqlite' ? t.name : `${n.name}.${t.name}`);

  let menu = $state<{ x: number; y: number; ns: SchemaNamespace; t: SchemaTable | null; text: string } | null>(null);
  function ctx(e: MouseEvent, ns: SchemaNamespace, t: SchemaTable | null, text: string) {
    e.preventDefault();
    menu = { x: Math.min(e.clientX, window.innerWidth - 230), y: Math.min(e.clientY, window.innerHeight - 180), ns, t, text };
  }
  function act(a: TreeAction) {
    if (!menu) return;
    const m = menu;
    menu = null;
    onaction(a, m.ns, m.t, m.text);
  }
</script>

<svelte:window onclick={() => (menu = null)} onkeydown={(e) => e.key === 'Escape' && (menu = null)} />

<div class="tree" data-testid="schema-tree">
  <div class="th">
    <input class="input sm grow" placeholder="Search tables, columns…" bind:value={q} aria-label="Search the schema" data-testid="schema-search" />
    <button class="btn ghost icon sm" onclick={onrefresh} aria-label="Refresh the schema" title="Refresh the schema" data-testid="schema-refresh"><Icon name="refresh" size={14} class={loading ? 'spinning' : ''} /></button>
  </div>
  {#if schema}
    <div class="db faint xs" title={schema.database ?? ''}><Icon name="database" size={12} />{schema.database ?? '—'}{schema.truncated ? ' · truncated (2000 tables)' : ''}</div>
  {/if}
  <div class="tb">
    {#if error}
      <div class="bad-text small pad">{error}</div>
    {:else if !schema}
      <div class="pad stack"><span class="skel" style="width:80%"></span><span class="skel" style="width:60%"></span><span class="skel" style="width:70%"></span></div>
    {:else}
      {#each [...user, ...(showSystem || q.trim() ? sys : [])] as ns (ns.name)}
        {@const tables = tablesOf(ns)}
        {@const nk = `ns:${ns.name}`}
        {@const nOpen = isOpen(nk, !ns.system && (user.length <= 2 || ns.name === schema.database || ns.name === 'public' || ns.name === 'main')) || !!q.trim()}
        <div class="ns">
          <button class="node nsn" onclick={() => toggle(nk, nOpen)} oncontextmenu={(e) => ctx(e, ns, null, ns.name)} aria-expanded={nOpen} data-testid="ns-{ns.name}">
            <Icon name={nOpen ? 'chevronDown' : 'chevronRight'} size={12} />
            <Icon name={schema.kind === 'cassandra' ? 'columns' : 'folder'} size={14} />
            <span class="nm">{ns.name}</span>
            {#if ns.system}<span class="badge outline sm">system</span>{/if}
            <span class="cnt">{ns.tables.length}</span>
          </button>
          {#if nOpen}
            {#each tables as t (t.name)}
              {@const tk = `t:${ns.name}.${t.name}`}
              {@const tOpen = isOpen(tk) || (!!q.trim() && !match(t.name))}
              <div class="tbl">
                <div class="node tn" role="treeitem" aria-selected="false" aria-expanded={tOpen} tabindex="0" oncontextmenu={(e) => ctx(e, ns, t, qualified(ns, t))} onclick={() => toggle(tk)} onkeydown={(e) => e.key === 'Enter' && toggle(tk)} ondblclick={() => oninsert(qualified(ns, t))} data-testid="tbl-{t.name}" title="{t.kind}{t.comment ? ` — ${t.comment}` : ''}{t.sizeBytes !== null ? ` · ${bytes(t.sizeBytes)}` : ''}">
                  <Icon name={tOpen ? 'chevronDown' : 'chevronRight'} size={12} />
                  <Icon name={t.kind === 'table' || t.kind === 'partitioned-table' ? 'table' : 'eye'} size={13} />
                  <span class="nm">{t.name}</span>
                  {#if t.rowEstimate !== null}<span class="cnt" title="~{t.rowEstimate.toLocaleString()} rows (estimate)">{fmtCount(t.rowEstimate)}</span>{/if}
                  <span class="ra">
                    <button class="btn ghost icon xs" onclick={(e) => (e.stopPropagation(), onaction('select', ns, t, qualified(ns, t)))} aria-label="Select first 100 rows of {t.name}" title="Select first 100 rows" data-testid="tbl-select-{t.name}"><Icon name="play" size={12} /></button>
                    <button class="btn ghost icon xs" onclick={(e) => (e.stopPropagation(), onaction('details', ns, t, qualified(ns, t)))} aria-label="Details of {t.name}" title="Indexes, constraints, DDL" data-testid="tbl-details-{t.name}"><Icon name="info" size={12} /></button>
                  </span>
                </div>
                {#if tOpen}
                  <div class="cols">
                    {#each t.columns as c (c.name)}
                      <button class="node cn" class:hit={!!q.trim() && match(c.name)} onclick={() => oninsert(c.name)} oncontextmenu={(e) => ctx(e, ns, t, c.name)} title="{c.name} {c.type}{c.nullable ? '' : ' not null'}{c.default ? ` default ${c.default}` : ''}{c.role ? ` · ${c.role}` : ''} — click: insert">
                        {#if c.primaryKey}<span class="key" aria-label="primary key">🔑</span>{:else}<span class="dotc"></span>{/if}
                        <span class="nm mono">{c.name}</span>
                        <span class="ty mono">{c.type}{c.nullable ? '' : ' !'}</span>
                      </button>
                    {/each}
                  </div>
                {/if}
              </div>
            {:else}
              <div class="faint xs pad">{q.trim() ? 'no match' : 'no tables'}</div>
            {/each}
            {#if ns.functions.length && !q.trim()}
              {@const fk = `f:${ns.name}`}
              <button class="node fn" onclick={() => toggle(fk)} aria-expanded={isOpen(fk)}>
                <Icon name={isOpen(fk) ? 'chevronDown' : 'chevronRight'} size={12} /><Icon name="code" size={13} /><span class="nm">functions</span><span class="cnt">{ns.functions.length}</span>
              </button>
              {#if isOpen(fk)}
                <div class="cols">
                  {#each ns.functions as f (f.name + f.arguments)}
                    <button class="node cn" onclick={() => oninsert(`${f.name}(`)} title="{f.kind} {f.name}({f.arguments}) → {f.returns}"><span class="dotc"></span><span class="nm mono">{f.name}</span><span class="ty mono">({f.arguments}) → {f.returns}</span></button>
                  {/each}
                </div>
              {/if}
            {/if}
          {/if}
        </div>
      {/each}
      {#if sys.length && !q.trim()}
        <button class="sysbtn faint xs" onclick={() => (showSystem = !showSystem)} data-testid="schema-system">{showSystem ? 'Hide' : 'Show'} {sys.length} system {sys.length === 1 ? 'namespace' : 'namespaces'}</button>
      {/if}
    {/if}
  </div>
</div>

{#if menu}
  <div class="ctxmenu" role="menu" style:left="{menu.x}px" style:top="{menu.y}px" data-testid="schema-menu">
    {#if menu.t}<button role="menuitem" onclick={() => act('select')}><Icon name="play" size={13} />Select first 100 rows</button>{/if}
    <button role="menuitem" onclick={() => act('insert')}><Icon name="arrowRight" size={13} />Insert into editor</button>
    <button role="menuitem" onclick={() => act('copy')}><Icon name="copy" size={13} />Copy name</button>
    {#if menu.t}<button role="menuitem" onclick={() => act('details')}><Icon name="info" size={13} />Details (indexes, DDL)</button>{/if}
  </div>
{/if}

<style>
  .tree {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
  }
  .th {
    display: flex;
    gap: 4px;
    padding: 8px;
    border-bottom: 1px solid var(--border);
  }
  .th .input {
    height: 28px;
    min-width: 0;
  }
  .db {
    display: flex;
    gap: 5px;
    align-items: center;
    padding: 6px 10px 2px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .tb {
    overflow: auto;
    flex: 1;
    min-height: 0;
    padding: 4px 4px 12px;
    font-size: 13px;
  }
  .pad {
    padding: 8px 10px;
  }
  .node {
    all: unset;
    box-sizing: border-box;
    display: flex;
    align-items: center;
    gap: 5px;
    width: 100%;
    padding: 3px 6px;
    border-radius: var(--r-sm);
    cursor: pointer;
    min-width: 0;
    color: var(--text);
  }
  .node:hover,
  .node:focus-visible {
    background: var(--surface-hover);
  }
  .node:focus-visible {
    box-shadow: var(--focus-ring);
  }
  .nm {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
  }
  .nsn .nm {
    font-weight: var(--fw-semibold);
  }
  .cnt {
    margin-left: auto;
    font-size: 11px;
    color: var(--text-3);
    font-variant-numeric: tabular-nums;
    padding-left: 6px;
    flex: none;
  }
  .tbl {
    padding-left: 12px;
  }
  .fn {
    margin-left: 12px;
    width: calc(100% - 12px);
    color: var(--text-2);
  }
  .tn .ra {
    display: none;
    gap: 0;
    flex: none;
  }
  .tn:hover .ra,
  .tn:focus-within .ra {
    display: inline-flex;
  }
  .tn:hover .cnt {
    display: none;
  }
  .cols {
    padding-left: 22px;
    border-left: 1px solid var(--border);
    margin-left: 18px;
  }
  .cn {
    padding: 1px 6px;
    font-size: 12px;
  }
  .cn.hit {
    background: var(--amber-soft);
  }
  .cn .ty {
    margin-left: auto;
    color: var(--text-3);
    font-size: 11px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 50%;
    padding-left: 6px;
  }
  .key {
    font-size: 10px;
    width: 12px;
  }
  .dotc {
    width: 12px;
    height: 12px;
    flex: none;
    position: relative;
  }
  .dotc::after {
    content: '';
    position: absolute;
    left: 4px;
    top: 4px;
    width: 4px;
    height: 4px;
    border-radius: 50%;
    background: var(--border-strong);
  }
  .sysbtn {
    all: unset;
    cursor: pointer;
    padding: 8px 10px;
    display: block;
  }
  .sysbtn:hover {
    color: var(--text);
  }
  .badge.sm {
    height: 16px;
    font-size: 10px;
    padding: 0 5px;
  }
  .ctxmenu {
    position: fixed;
    z-index: var(--z-popover);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    box-shadow: var(--shadow-lg);
    padding: 4px;
    display: flex;
    flex-direction: column;
    min-width: 210px;
  }
  .ctxmenu button {
    all: unset;
    display: flex;
    gap: 8px;
    align-items: center;
    padding: 6px 10px;
    border-radius: var(--r-sm);
    cursor: pointer;
    font-size: var(--fs-sm);
  }
  .ctxmenu button:hover,
  .ctxmenu button:focus-visible {
    background: var(--surface-hover);
  }
  :global(.spinning) {
    animation: spin 0.8s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
</style>
