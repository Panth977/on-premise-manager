<script lang="ts">
  /**
   * One statement's outcome (§21 #97): error (code, detail, hint, "Show in editor"), command tag, rows (virtualised grid,
   * Load more via resultPage, filter, copy TSV, CSV / JSON / NDJSON export links, value viewer with "Download full
   * value"), explain plan (tree from FORMAT JSON + text), and inline editing of `SELECT * FROM <table>` results.
   */
  import type { CellValue, ResultColumn, SqlSchemaResponse, StatementResult } from '@opm/shared/devtools';
  import { toasts } from '$lib/state/toast.svelte';
  import { downloadUrl } from '$lib/tools/api';
  import Icon from '$lib/ui/Icon.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import Confirm from '$lib/ui/Confirm.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import DataGrid from '../DataGrid.svelte';
  import CellViewer from '../CellViewer.svelte';
  import { dev, devPath } from '../api';
  import { copyText, isTrunc } from '../grid';
  import { hidePop, placeBelow, showPop } from '$lib/ui/popover';
  import PlanTree, { selfTime, type PlanNode } from './PlanTree.svelte';
  import { EditState, editTargetOf } from './edits.svelte';

  interface Props {
    result: StatementResult;
    site: string;
    kind: 'postgres' | 'sqlite' | 'cassandra';
    schema: SqlSchemaResponse | null;
    readOnly: boolean;
    onshowerror?: (position: number) => void;
    /** run generated statements (inline edit save); true = ok */
    onapply?: (statements: string[]) => Promise<boolean>;
  }
  let { result, site, kind, schema, readOnly, onshowerror, onapply }: Props = $props();

  // svelte-ignore state_referenced_locally
  let rows = $state<CellValue[][]>(result.rows);
  let loading = $state(false);
  let filter = $state('');
  let grid: DataGrid | undefined = $state();
  const more = $derived(result.resultId !== null && rows.length < result.totalRows);
  async function loadMore() {
    if (!result.resultId) return;
    loading = true;
    try {
      const p = await dev.resultPage({ siteSlug: site, resultId: result.resultId, query: { offset: rows.length } });
      rows = [...rows, ...p.rows];
    } catch (e) {
      toasts.err(e, 'Could not load more rows');
    } finally {
      loading = false;
    }
  }
  function exportUrl(format: 'csv' | 'json' | 'ndjson') {
    return devPath('resultExport', { siteSlug: site, resultId: result.resultId ?? '' }, { format });
  }
  let menuEl: HTMLDivElement | undefined = $state();
  let anchor: HTMLSpanElement | undefined = $state();
  function toggleExport() {
    if (!menuEl || !anchor) return;
    if (menuEl.matches(':popover-open')) return hidePop(menuEl);
    showPop(menuEl);
    placeBelow(menuEl, anchor, 160);
  }
  async function doExport(f: 'csv' | 'json' | 'ndjson') {
    hidePop(menuEl);
    try {
      await downloadUrl(exportUrl(f), `result.${f}`);
    } catch (e) {
      toasts.err(e, 'Export failed');
    }
  }
  async function copy(withHeader: boolean) {
    const t = grid?.selectionTsv(withHeader) ?? grid?.allTsv() ?? '';
    if (await copyText(t)) toasts.ok(grid?.selectionTsv() ? 'Selection copied (TSV)' : 'All loaded rows copied (TSV)');
  }

  // viewer
  let viewer = $state<{ open: boolean; col: ResultColumn | null; value: CellValue | undefined; url: string | null }>({ open: false, col: null, value: undefined, url: null });
  function openCell(row: number, col: number, value: CellValue | undefined) {
    viewer = { open: true, col: result.columns[col] ?? null, value, url: result.resultId && !(isTrunc(value) && value.fetchable === false) ? devPath('resultValue', { siteSlug: site, resultId: result.resultId }, { row, col }) : null };
  }

  // explain
  type PlanView = 'tree' | 'text';
  let planView = $state<PlanView>('tree');
  const planRoot = $derived.by((): { root: PlanNode; planning: number | null; exec: number | null } | null => {
    const j = result.plan?.json;
    const top = Array.isArray(j) ? (j[0] as Record<string, unknown> | undefined) : null;
    if (!top || typeof top.Plan !== 'object') return null;
    return { root: top.Plan as PlanNode, planning: (top['Planning Time'] as number) ?? null, exec: (top['Execution Time'] as number) ?? null };
  });
  const maxSelf = $derived.by(() => {
    let m = 0;
    const walk = (n: PlanNode) => {
      m = Math.max(m, selfTime(n) ?? 0);
      (n.Plans ?? []).forEach(walk);
    };
    if (planRoot) walk(planRoot.root);
    return m;
  });

  // inline editing
  const target = $derived(result.type === 'rows' ? editTargetOf(result.statement, result.columns, schema, kind) : { target: null, reason: '' });
  const edits = new EditState();
  const editable = $derived(!!target.target && !readOnly);
  let review = $state(false);
  let discardOpen = $state(false);
  let saving = $state(false);
  const pending = $derived(target.target ? edits.statements(target.target, result.columns, rows) : []);
  async function save() {
    if (!onapply || !pending.length) return;
    saving = true;
    try {
      if (await onapply(pending)) {
        edits.clear();
        review = false;
      }
    } finally {
      saving = false;
    }
  }
  function delRows() {
    const r = grid?.selectedRows() ?? [];
    if (!r.length) return toasts.info('Select rows first (click the row numbers).');
    edits.toggleDelete(r);
  }
  const pkIdx = $derived(target.target?.pk ?? (schema ? pkFromSchema() : []));
  function pkFromSchema(): number[] {
    const m = /\bfrom\s+(?:"?([\w$]+)"?\s*\.\s*)?"?([\w$]+)"?/i.exec(result.statement);
    if (!m || !schema) return [];
    const t = schema.namespaces.flatMap((n) => (!m[1] || n.name === m[1] ? n.tables : [])).find((x) => x.name === m[2]);
    if (!t) return [];
    return result.columns.map((c, i) => (t.columns.find((x) => x.name === c.name)?.primaryKey ? i : -1)).filter((i) => i >= 0);
  }
</script>

<div class="rp" data-testid="result-{result.index}">
  {#if !result.ok && result.error}
    <div class="err" data-testid="result-error">
      <div class="et"><Icon name="alert" size={16} /><strong>{result.error.message}</strong></div>
      <div class="kvs">
        {#if result.error.code}<span class="faint">Code</span><span class="mono">{result.error.code}</span>{/if}
        {#if result.error.position !== null}<span class="faint">Position</span><span class="mono">{result.error.position} <button class="btn ghost xs" onclick={() => onshowerror?.(result.error!.position!)} data-testid="show-error">Show in editor</button></span>{/if}
        {#if result.error.detail}<span class="faint">Detail</span><span>{result.error.detail}</span>{/if}
        {#if result.error.hint}<span class="faint">Hint</span><span>{result.error.hint}</span>{/if}
      </div>
      <pre class="st mono">{result.statement}</pre>
    </div>
  {:else if result.type === 'command'}
    <div class="cmd" data-testid="result-command">
      <Icon name="check" size={18} />
      <div>
        <div class="strong mono">{result.commandTag ?? 'OK'}</div>
        <div class="faint small">{result.rowsAffected !== null ? `${result.rowsAffected} ${result.rowsAffected === 1 ? 'row' : 'rows'} affected · ` : ''}{result.durationMs} ms</div>
        {#each result.notices ?? [] as n (n)}<div class="small blue-text">NOTICE: {n}</div>{/each}
      </div>
    </div>
  {:else if result.type === 'explain'}
    <div class="planbar">
      <SegmentedControl options={[{ value: 'tree' as PlanView, label: 'Tree', disabled: !planRoot }, { value: 'text' as PlanView, label: 'Text' }]} value={planRoot ? planView : 'text'} onchange={(v) => (planView = v)} label="Plan view" size="sm" testid="plan-view" />
      {#if planRoot?.planning !== null && planRoot?.planning !== undefined}<span class="faint small">planning {planRoot.planning} ms · execution {planRoot.exec} ms</span>{/if}
    </div>
    {#if planRoot && planView === 'tree'}
      <ul class="plan" data-testid="plan-tree"><PlanTree node={planRoot.root} max={maxSelf} /></ul>
    {:else}
      <pre class="plantext mono" data-testid="plan-text">{result.plan?.text ?? result.rows.map((r) => r.join(' ')).join('\n')}</pre>
    {/if}
  {:else}
    <div class="tb">
      <span class="small"><strong>{rows.length.toLocaleString()}</strong>{result.totalRows > rows.length ? ` of ${result.totalRows.toLocaleString()}` : ''} {result.totalRows === 1 ? 'row' : 'rows'}{result.truncated ? '+' : ''} · {result.durationMs} ms</span>
      {#if result.truncated}<span class="badge amber" title="More rows existed beyond the agent's buffer (bufferRows / maxResultBytes); add a LIMIT / WHERE">truncated</span>{/if}
      {#if more}<Btn size="xs" icon="arrowDown" busy={loading} onclick={loadMore} data-testid="load-more">Load more</Btn>{/if}
      <input class="input sm filter" placeholder="Filter loaded rows…" bind:value={filter} aria-label="Filter rows" data-testid="result-filter" />
      <span class="grow"></span>
      {#if editable}
        <span class="badge green" title="SELECT * on a table with a primary key: double-click a cell to edit">editable</span>
        <Btn size="xs" variant="ghost" icon="plus" onclick={() => edits.addRow(result.columns.length)} data-testid="edit-add-row">Row</Btn>
        <Btn size="xs" variant="ghost" icon="trash" onclick={delRows} data-testid="edit-del-rows">Delete</Btn>
        {#if edits.count}
          <span class="badge amber" data-testid="edit-pending">{edits.count} pending</span>
          <Btn size="xs" variant="ghost" onclick={() => (discardOpen = true)} data-testid="edit-discard">Discard</Btn>
          <Btn size="xs" variant="primary" icon="save" onclick={() => (review = true)} data-testid="edit-save">Save…</Btn>
        {/if}
      {:else if result.type === 'rows' && target.reason && kind !== 'cassandra'}
        <span class="faint xs nowrap ro-hint" title={readOnly ? 'Read-only connection' : target.reason}><Icon name="lock" size={11} />{readOnly ? 'read-only' : 'not editable'}</span>
      {/if}
      <Btn size="xs" variant="ghost" icon="copy" onclick={() => copy(false)} title="Copy the selection (or every loaded row) as TSV" data-testid="copy-tsv">Copy</Btn>
      {#if result.resultId}
        <span class="exp" bind:this={anchor}>
          <Btn size="xs" variant="ghost" icon="download" onclick={toggleExport} aria-haspopup="menu" data-testid="export">Export</Btn>
        </span>
        <div class="menu" role="menu" popover="auto" bind:this={menuEl} data-testid="export-menu">
          {#each [['csv', 'CSV'], ['json', 'JSON'], ['ndjson', 'NDJSON']] as [f, l] (f)}
            <a role="menuitem" href={exportUrl(f as 'csv')} onclick={(e) => (e.preventDefault(), doExport(f as 'csv'))} data-testid="export-{f}"><span class="strong">{l}</span> <span class="faint xs">all {result.totalRows.toLocaleString()} rows</span></a>
          {/each}
        </div>
      {/if}
    </div>
    <div class="gwrap">
      <DataGrid bind:this={grid} columns={result.columns} {rows} pk={pkIdx} {filter} edits={editable ? edits : null} onopen={openCell} testid="grid-{result.index}" />
    </div>
  {/if}
</div>

<CellViewer bind:open={viewer.open} column={viewer.col} value={viewer.value} fullUrl={viewer.url} />
<Modal bind:open={review} title="Save {edits.count} {edits.count === 1 ? 'change' : 'changes'}?" description="These statements run in one transaction on {target.target?.namespace}.{target.target?.table} and are logged like any query." size="lg" testid="edit-review">
  <pre class="gen mono" data-testid="edit-sql">{pending.join(';\n')};</pre>
  {#snippet footer()}
    <Btn variant="ghost" onclick={() => (review = false)}>Back</Btn>
    <Btn variant="primary" busy={saving} onclick={save} data-testid="edit-run">Run {pending.length} {pending.length === 1 ? 'statement' : 'statements'}</Btn>
  {/snippet}
</Modal>
<Confirm bind:open={discardOpen} title="Discard {edits.count} pending {edits.count === 1 ? 'change' : 'changes'}?" confirmLabel="Discard" danger onconfirm={() => edits.clear()} testid="edit-discard-confirm" />

<style>
  .rp {
    display: flex;
    flex-direction: column;
    gap: 6px;
    height: 100%;
    min-height: 0;
    min-width: 0;
  }
  .tb {
    display: flex;
    gap: 6px;
    align-items: center;
    flex-wrap: wrap;
    min-width: 0;
  }
  .filter {
    width: 180px;
    height: 26px;
  }
  .gwrap {
    flex: 1;
    min-height: 140px;
    display: flex;
  }
  .gwrap > :global(.dgrid) {
    flex: 1;
  }
  .err {
    border: 1px solid color-mix(in srgb, var(--red) 40%, var(--border));
    background: var(--red-soft);
    border-radius: var(--r-md);
    padding: 10px 12px;
    color: var(--text);
    display: flex;
    flex-direction: column;
    gap: 6px;
    overflow: auto;
  }
  .et {
    display: flex;
    gap: 8px;
    align-items: flex-start;
    color: var(--red-text);
  }
  .kvs {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 3px 12px;
    font-size: var(--fs-sm);
  }
  .st {
    margin: 0;
    font-size: 12px;
    white-space: pre-wrap;
    color: var(--text-2);
    max-height: 120px;
    overflow: auto;
  }
  .cmd {
    display: flex;
    gap: 10px;
    align-items: flex-start;
    padding: 14px;
    color: var(--green-text);
  }
  .cmd > div {
    color: var(--text);
  }
  .planbar {
    display: flex;
    gap: 10px;
    align-items: center;
    flex-wrap: wrap;
  }
  .plan {
    margin: 0;
    padding: 4px 8px;
    overflow: auto;
    flex: 1;
  }
  .plantext,
  .gen {
    margin: 0;
    padding: 10px 12px;
    background: var(--code-bg);
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    font-size: 12px;
    overflow: auto;
    flex: 1;
    white-space: pre;
  }
  .gen {
    white-space: pre-wrap;
    max-height: 50vh;
  }
  .exp {
    display: inline-flex;
  }
  .menu:not(:popover-open) {
    display: none;
  }
  .menu {
    position: fixed;
    inset: auto;
    margin: 0;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    box-shadow: var(--shadow-md);
    padding: 4px;
    display: flex;
    flex-direction: column;
    min-width: 190px;
  }
  .menu a {
    padding: 6px 10px;
    border-radius: var(--r-sm);
    color: var(--text);
    display: flex;
    justify-content: space-between;
    gap: 10px;
  }
  .menu a:hover {
    background: var(--surface-hover);
    text-decoration: none;
  }
  .ro-hint {
    display: inline-flex;
    gap: 4px;
    align-items: center;
  }
</style>
