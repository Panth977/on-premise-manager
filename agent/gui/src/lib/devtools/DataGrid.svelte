<script lang="ts">
  /**
   * Virtualised result grid (§21 #97): sticky header with type + PK key, sort (click), column resize (drag the edge),
   * client-side filter, cell / range (shift) / row (row number) / column (Ctrl/⌘-click header) selection, keyboard
   * (arrows, shift+arrows, Enter, Ctrl/⌘-C = TSV, Escape), type-aware cells (NULL explicit, bool, JSON, numbers right,
   * truncated values marked). With `edits`: double-click / Enter edits a cell (pending changes coloured), Delete marks
   * rows, new rows at the end. Without: double-click opens the value viewer.
   */
  import type { CellValue, ResultColumn } from '@opm/shared/devtools';
  import Icon from '$lib/ui/Icon.svelte';
  import { cellText, defaultWidth, editText, isTrunc, parseEdit, toTsv, copyText } from './grid';
  import type { EditState } from './sql/edits.svelte';

  interface Props {
    columns: ResultColumn[];
    rows: CellValue[][];
    pk?: number[];
    filter?: string;
    edits?: EditState | null;
    onopen?: (row: number, col: number, value: CellValue | undefined) => void;
    testid?: string;
    height?: string;
  }
  let { columns, rows, pk = [], filter = '', edits = null, onopen, testid = 'grid', height = '100%' }: Props = $props();

  const RH = 28;
  const NW = 56;
  type VRow = { t: 'r'; i: number } | { t: 'n'; k: number };
  let widths = $state<number[]>([]);
  $effect(() => {
    const c = columns;
    widths = c.map((x) => defaultWidth(x));
  });
  let sort = $state<{ col: number; desc: boolean } | null>(null);
  const view = $derived.by((): VRow[] => {
    const q = filter.trim().toLowerCase();
    let idx = rows.map((_, i) => i);
    if (q) idx = idx.filter((i) => rows[i]!.some((v, j) => cellText(v, columns[j]).toLowerCase().includes(q)));
    if (sort) {
      const { col, desc } = sort;
      const d = columns[col]?.display;
      idx = idx.slice().sort((a, b) => {
        const x = edits?.updates[a]?.[col] ?? rows[a]![col];
        const y = edits?.updates[b]?.[col] ?? rows[b]![col];
        if (x === null || x === undefined) return y === null || y === undefined ? 0 : 1;
        if (y === null || y === undefined) return -1;
        const c = d === 'number' ? Number(x) - Number(y) : cellText(x).localeCompare(cellText(y), undefined, { numeric: true });
        return desc ? -c : c;
      });
    }
    const out: VRow[] = idx.map((i) => ({ t: 'r', i }));
    for (let k = 0; k < (edits?.inserts.length ?? 0); k++) out.push({ t: 'n', k });
    return out;
  });
  const total = $derived(columns.reduce((a, _, i) => a + (widths[i] ?? 120), NW));
  const pkSet = $derived(new Set(pk));

  // ── virtualisation
  let body: HTMLDivElement | undefined = $state();
  let scrollTop = $state(0);
  let vh = $state(400);
  const first = $derived(Math.max(0, Math.floor(scrollTop / RH) - 8));
  const last = $derived(Math.min(view.length, Math.ceil((scrollTop + vh) / RH) + 8));
  const visible = $derived(view.slice(first, last).map((r, j) => ({ r, vi: first + j })));

  // ── selection (view row index × column index)
  let sel = $state<{ r0: number; c0: number; r1: number; c1: number } | null>(null);
  const rect = $derived(sel ? { r0: Math.min(sel.r0, sel.r1), r1: Math.max(sel.r0, sel.r1), c0: Math.min(sel.c0, sel.c1), c1: Math.max(sel.c0, sel.c1) } : null);
  const inSel = (vi: number, c: number) => !!rect && vi >= rect.r0 && vi <= rect.r1 && c >= rect.c0 && c <= rect.c1;
  function valueAt(r: VRow, c: number): CellValue | undefined {
    if (r.t === 'n') return edits?.inserts[r.k]?.[c] ?? null;
    const u = edits?.updates[r.i];
    return u && c in u ? u[c] : rows[r.i]![c];
  }
  function clickCell(e: MouseEvent, vi: number, c: number) {
    if (e.shiftKey && sel) sel = { ...sel, r1: vi, c1: c };
    else sel = { r0: vi, c0: c, r1: vi, c1: c };
    grid?.focus({ preventScroll: true });
  }
  function clickRowNo(e: MouseEvent, vi: number) {
    const c1 = columns.length - 1;
    if (e.shiftKey && sel) sel = { ...sel, r1: vi, c0: 0, c1 };
    else sel = { r0: vi, r1: vi, c0: 0, c1 };
    grid?.focus({ preventScroll: true });
  }
  function clickHead(e: MouseEvent, c: number) {
    if (e.metaKey || e.ctrlKey || e.shiftKey) {
      sel = e.shiftKey && sel ? { r0: 0, r1: Math.max(0, view.length - 1), c0: sel.c0, c1: c } : { r0: 0, r1: Math.max(0, view.length - 1), c0: c, c1: c };
      return;
    }
    sort = !sort || sort.col !== c ? { col: c, desc: false } : !sort.desc ? { col: c, desc: true } : null;
  }
  export function selectionTsv(withHeader = false): string | null {
    if (!rect) return null;
    const cols = columns.slice(rect.c0, rect.c1 + 1).map((c) => c.name);
    const out: (CellValue | undefined)[][] = [];
    for (let vi = rect.r0; vi <= rect.r1 && vi < view.length; vi++) {
      const r = view[vi]!;
      const row: (CellValue | undefined)[] = [];
      for (let c = rect.c0; c <= rect.c1; c++) row.push(valueAt(r, c));
      out.push(row);
    }
    return toTsv(withHeader ? cols : null, out);
  }
  export function allTsv(): string {
    return toTsv(
      columns.map((c) => c.name),
      view.map((r) => columns.map((_, c) => valueAt(r, c))),
    );
  }
  export function selectedRows(): number[] {
    if (!rect) return [];
    const out: number[] = [];
    for (let vi = rect.r0; vi <= rect.r1; vi++) {
      const r = view[vi];
      if (r?.t === 'r') out.push(r.i);
    }
    return out;
  }
  let grid: HTMLDivElement | undefined = $state();
  function ensureVisible(vi: number) {
    if (!body) return;
    if (vi * RH < body.scrollTop) body.scrollTop = vi * RH;
    else if ((vi + 1) * RH > body.scrollTop + body.clientHeight - RH) body.scrollTop = (vi + 2) * RH - body.clientHeight;
  }
  async function key(e: KeyboardEvent) {
    if (editing) return;
    const mod = e.metaKey || e.ctrlKey;
    if (mod && e.key.toLowerCase() === 'c') {
      const t = selectionTsv();
      if (t !== null) {
        e.preventDefault();
        await copyText(t);
        flash = 'Copied';
        setTimeout(() => (flash = null), 1200);
      }
      return;
    }
    if (mod && e.key.toLowerCase() === 'a') {
      e.preventDefault();
      sel = { r0: 0, c0: 0, r1: Math.max(0, view.length - 1), c1: columns.length - 1 };
      return;
    }
    if (!sel) return;
    const mv: Record<string, [number, number]> = { ArrowUp: [-1, 0], ArrowDown: [1, 0], ArrowLeft: [0, -1], ArrowRight: [0, 1], PageDown: [Math.floor(vh / RH), 0], PageUp: [-Math.floor(vh / RH), 0] };
    const d = mv[e.key];
    if (d) {
      e.preventDefault();
      const r = Math.max(0, Math.min(view.length - 1, sel.r1 + d[0]));
      const c = Math.max(0, Math.min(columns.length - 1, sel.c1 + d[1]));
      sel = e.shiftKey ? { ...sel, r1: r, c1: c } : { r0: r, c0: c, r1: r, c1: c };
      ensureVisible(r);
      return;
    }
    if (e.key === 'Escape') sel = null;
    if (e.key === 'Enter' && rect) {
      e.preventDefault();
      dbl(rect.r0, rect.c0);
    }
    if ((e.key === 'Delete' || e.key === 'Backspace') && edits && rect) {
      e.preventDefault();
      edits.toggleDelete(selectedRows());
    }
  }
  let flash = $state<string | null>(null);

  // ── resize
  function startResize(e: PointerEvent, c: number) {
    e.preventDefault();
    e.stopPropagation();
    const x0 = e.clientX;
    const w0 = widths[c] ?? 120;
    const move = (ev: PointerEvent) => (widths[c] = Math.max(48, Math.min(900, w0 + ev.clientX - x0)));
    const up = () => (window.removeEventListener('pointermove', move), window.removeEventListener('pointerup', up));
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  }
  function autoFit(c: number) {
    const sample = view.slice(0, 200).map((r) => cellText(valueAt(r, c), columns[c]).length);
    widths[c] = Math.max(60, Math.min(600, Math.max(columns[c]!.name.length + 4, ...sample) * 7.4 + 20));
  }

  // ── inline edit
  let editing = $state<{ vi: number; c: number; text: string; err: string | null } | null>(null);
  function dbl(vi: number, c: number) {
    const r = view[vi];
    if (!r) return;
    const v = valueAt(r, c);
    if (edits && !isTrunc(v)) {
      editing = { vi, c, text: editText(v, columns[c]!), err: null };
      return;
    }
    if (r.t === 'r') onopen?.(r.i, c, v);
  }
  function commit(nullValue = false) {
    if (!editing || !edits) return;
    const r = view[editing.vi];
    const col = columns[editing.c]!;
    let v: CellValue;
    try {
      v = nullValue ? null : parseEdit(editing.text, col);
    } catch (err) {
      editing.err = err instanceof Error ? err.message : String(err);
      return;
    }
    if (r?.t === 'r') edits.set(r.i, editing.c, v, rows[r.i]![editing.c]);
    else if (r?.t === 'n') edits.setNew(r.k, editing.c, v);
    editing = null;
    grid?.focus({ preventScroll: true });
  }
  const changed = (r: VRow, c: number) => r.t === 'r' && !!edits?.updates[r.i] && c in edits.updates[r.i]!;
  const deleted = (r: VRow) => r.t === 'r' && !!edits?.deletes[r.i];
  const stats = $derived.by(() => {
    if (!rect || (rect.r0 === rect.r1 && rect.c0 === rect.c1)) return null;
    const nums: number[] = [];
    for (let vi = rect.r0; vi <= rect.r1 && vi < view.length; vi++)
      for (let c = rect.c0; c <= rect.c1; c++) {
        const v = valueAt(view[vi]!, c);
        if (typeof v === 'number') nums.push(v);
      }
    const cells = (rect.r1 - rect.r0 + 1) * (rect.c1 - rect.c0 + 1);
    if (!nums.length) return { cells, sum: null, avg: null };
    const sum = nums.reduce((a, b) => a + b, 0);
    return { cells, sum: Math.round(sum * 1000) / 1000, avg: Math.round((sum / nums.length) * 1000) / 1000 };
  });
</script>

<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
<div class="dgrid" bind:this={grid} tabindex="0" role="grid" aria-rowcount={view.length} aria-colcount={columns.length} onkeydown={key} data-testid={testid} style:height>
  <div class="scroller" bind:this={body} bind:clientHeight={vh} onscroll={(e) => (scrollTop = e.currentTarget.scrollTop)}>
    <div class="inner" style:width="{total}px" style:height="{(view.length + 1) * RH}px">
      <div class="head" role="row" style:width="{total}px">
        <div class="rn hc" style:width="{NW}px">#</div>
        {#each columns as col, c (c)}
          <div class="hc" class:sorted={sort?.col === c} role="columnheader" aria-sort={sort?.col === c ? (sort.desc ? 'descending' : 'ascending') : 'none'} style:width="{widths[c]}px" title="{col.name} · {col.type}{pkSet.has(c) ? ' · primary key' : ''} — click: sort · Ctrl/⌘-click: select column" data-testid="{testid}-h-{col.name}">
            <button class="hb" onclick={(e) => clickHead(e, c)}>
              {#if pkSet.has(c)}<span class="key" aria-label="primary key">🔑</span>{/if}
              <span class="hn">{col.name}</span>
              <span class="ht">{col.type}</span>
              {#if sort?.col === c}<Icon name={sort.desc ? 'arrowDown' : 'arrowUp'} size={11} />{/if}
            </button>
            <span class="rz" role="separator" aria-orientation="vertical" aria-label="Resize {col.name}" onpointerdown={(e) => startResize(e, c)} ondblclick={() => autoFit(c)}></span>
          </div>
        {/each}
      </div>
      {#each visible as { r, vi } (r.t === 'r' ? `r${r.i}` : `n${r.k}`)}
        <div class="gr" role="row" class:del={deleted(r)} class:new={r.t === 'n'} style:transform="translateY({(vi + 1) * RH}px)" style:width="{total}px" data-row={vi}>
          <button class="rn" style:width="{NW}px" onclick={(e) => clickRowNo(e, vi)} tabindex="-1" aria-label="Select row {vi + 1}">{r.t === 'n' ? '+' : r.i + 1}</button>
          {#each columns as col, c (c)}
            {@const v = valueAt(r, c)}
            <div
              class="cell {col.display}"
              class:sel={inSel(vi, c)}
              class:chg={changed(r, c)}
              class:nul={v === null || v === undefined}
              class:trunc={isTrunc(v)}
              role="gridcell"
              tabindex="-1"
              style:width="{widths[c]}px"
              onclick={(e) => clickCell(e, vi, c)}
              ondblclick={() => dbl(vi, c)}
              onkeydown={() => {}}
              data-col={col.name}>
              {#if v === null || v === undefined}<span class="null">NULL</span>{:else if typeof v === 'boolean'}<span class="bool {v ? 't' : 'f'}">{v ? 'true' : 'false'}</span>{:else if isTrunc(v)}<span class="tb" title="{v.sizeBytes} bytes — cut at the inline limit">…{Math.round(v.sizeBytes / 1024)} KiB</span>{cellText(v, col)}{:else}{cellText(v, col)}{/if}
            </div>
          {/each}
        </div>
      {/each}
      {#if !view.length}
        <div class="empty" style:top="{RH}px">{rows.length ? 'No row matches the filter.' : '(no rows)'}</div>
      {/if}
    </div>
  </div>
  {#if editing}
    {@const col = columns[editing.c]!}
    <div class="editor" role="dialog" aria-label="Edit {col.name}" data-testid="{testid}-editor">
      <div class="eh"><span class="mono strong">{col.name}</span><span class="faint xs">{col.type}</span></div>
      {#if col.display === 'json' || editing.text.length > 80 || editing.text.includes('\n')}
        <!-- svelte-ignore a11y_autofocus -->
        <textarea class="input mono" rows="6" bind:value={editing.text} autofocus onkeydown={(e) => (e.key === 'Escape' ? (editing = null) : (e.metaKey || e.ctrlKey) && e.key === 'Enter' ? commit() : null)} data-testid="{testid}-edit-input"></textarea>
      {:else}
        <!-- svelte-ignore a11y_autofocus -->
        <input class="input mono" bind:value={editing.text} autofocus onkeydown={(e) => (e.key === 'Escape' ? (editing = null) : e.key === 'Enter' ? commit() : null)} data-testid="{testid}-edit-input" />
      {/if}
      {#if editing.err}<span class="field-err">{editing.err}</span>{/if}
      <div class="ea">
        <button class="btn sm ghost" onclick={() => commit(true)} data-testid="{testid}-edit-null">Set NULL</button>
        <span class="grow"></span>
        <button class="btn sm ghost" onclick={() => (editing = null)}>Cancel</button>
        <button class="btn sm primary" onclick={() => commit()} data-testid="{testid}-edit-ok">Apply</button>
      </div>
    </div>
  {/if}
  <div class="status" aria-live="polite">
    <span>{view.length.toLocaleString()} {view.length === 1 ? 'row' : 'rows'}{filter.trim() ? ` (filtered from ${rows.length.toLocaleString()})` : ''}</span>
    {#if stats}<span>· {stats.cells} cells{stats.sum !== null ? ` · sum ${stats.sum} · avg ${stats.avg}` : ''}</span>{/if}
    {#if flash}<span class="ok-text">· {flash}</span>{/if}
    <span class="grow"></span>
    <span class="faint xs hide-m">click · shift-click · row # · Ctrl/⌘-click header · Ctrl/⌘-C{edits ? ' · double-click to edit · Del marks rows' : ' · double-click to view'}</span>
  </div>
</div>

<style>
  .dgrid {
    position: relative;
    display: flex;
    flex-direction: column;
    min-height: 0;
    min-width: 0;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    background: var(--surface);
    outline: none;
    font-size: 12.5px;
  }
  .dgrid:focus-visible {
    box-shadow: var(--focus-ring);
  }
  .scroller {
    flex: 1;
    min-height: 0;
    overflow: auto;
    position: relative;
  }
  .inner {
    position: relative;
  }
  .head {
    position: sticky;
    top: 0;
    z-index: 2;
    display: flex;
    height: 28px;
    background: var(--surface-2);
    border-bottom: 1px solid var(--border);
  }
  .hc {
    position: relative;
    flex: none;
    display: flex;
    align-items: center;
    border-right: 1px solid var(--border);
    min-width: 0;
  }
  .hc.rn {
    justify-content: center;
    color: var(--text-3);
    position: sticky;
    left: 0;
    background: var(--surface-2);
    z-index: 3;
  }
  .hb {
    all: unset;
    display: flex;
    align-items: center;
    gap: 5px;
    width: 100%;
    height: 100%;
    padding: 0 8px;
    cursor: pointer;
    min-width: 0;
    box-sizing: border-box;
  }
  .hb:hover {
    background: var(--surface-hover);
  }
  .hn {
    font-weight: var(--fw-semibold);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .ht {
    color: var(--text-3);
    font-size: 11px;
    font-family: var(--font-mono);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    flex: 1;
    min-width: 0;
  }
  .key {
    font-size: 11px;
  }
  .rz {
    position: absolute;
    right: -4px;
    top: 0;
    bottom: 0;
    width: 8px;
    cursor: col-resize;
    z-index: 1;
  }
  .rz:hover {
    background: color-mix(in srgb, var(--accent) 35%, transparent);
  }
  .gr {
    position: absolute;
    left: 0;
    top: 0;
    height: 28px;
    display: flex;
    border-bottom: 1px solid color-mix(in srgb, var(--border) 70%, transparent);
  }
  .gr:hover .cell {
    background: color-mix(in srgb, var(--accent) 4%, transparent);
  }
  .rn {
    flex: none;
    position: sticky;
    left: 0;
    z-index: 1;
    border: 0;
    border-right: 1px solid var(--border);
    background: var(--surface-2);
    color: var(--text-3);
    font: inherit;
    font-size: 11px;
    text-align: right;
    padding: 0 8px;
    cursor: pointer;
  }
  .cell {
    flex: none;
    padding: 0 8px;
    line-height: 27px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    border-right: 1px solid color-mix(in srgb, var(--border) 60%, transparent);
    font-family: var(--font-mono);
    cursor: cell;
    box-sizing: border-box;
  }
  .cell.number {
    text-align: right;
    color: var(--blue-text);
  }
  .cell.json {
    color: var(--violet-text);
  }
  .cell.datetime,
  .cell.uuid {
    color: var(--text-2);
  }
  .cell.sel {
    background: color-mix(in srgb, var(--accent) 16%, transparent) !important;
    box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--accent) 45%, transparent);
  }
  .cell.chg {
    background: color-mix(in srgb, var(--amber) 22%, transparent);
  }
  .gr.del .cell {
    background: color-mix(in srgb, var(--red) 12%, transparent);
    text-decoration: line-through;
    color: var(--red-text);
  }
  .gr.new .cell {
    background: color-mix(in srgb, var(--green) 12%, transparent);
  }
  .null {
    color: var(--text-3);
    font-style: italic;
    font-family: var(--font-sans);
    font-size: 11px;
  }
  .bool.t {
    color: var(--green-text);
  }
  .bool.f {
    color: var(--red-text);
  }
  .tb {
    font-family: var(--font-sans);
    font-size: 10.5px;
    background: var(--amber-soft);
    color: var(--amber-text);
    border-radius: 4px;
    padding: 1px 4px;
    margin-right: 6px;
  }
  .empty {
    position: absolute;
    left: 0;
    right: 0;
    padding: 16px;
    color: var(--text-3);
    text-align: center;
  }
  .status {
    display: flex;
    gap: 6px;
    align-items: center;
    padding: 4px 10px;
    border-top: 1px solid var(--border);
    color: var(--text-2);
    font-size: var(--fs-xs);
    background: var(--surface-2);
    border-radius: 0 0 var(--r-md) var(--r-md);
    min-width: 0;
    overflow: hidden;
    white-space: nowrap;
  }
  .editor {
    position: absolute;
    left: 50%;
    top: 40px;
    transform: translateX(-50%);
    width: min(440px, calc(100% - 16px));
    background: var(--surface);
    border: 1px solid var(--border-strong);
    border-radius: var(--r-lg);
    box-shadow: var(--shadow-lg);
    padding: 10px;
    display: flex;
    flex-direction: column;
    gap: 8px;
    z-index: 5;
  }
  .eh {
    display: flex;
    gap: 8px;
    align-items: baseline;
  }
  .ea {
    display: flex;
    gap: 6px;
    align-items: center;
  }
  @media (max-width: 700px) {
    .hide-m {
      display: none;
    }
  }
</style>
