<script lang="ts">
  /**
   * "Response headers → browser" (nginx add_header / proxy_hide_header) for a host or a route — one editor for both
   * GUIs (§21 #102). Rows: name · value (expression) · set (replace the container's value) / add (append) · always (also
   * on error responses); remove chips. Calls onchange(headers, rowErrors) on every edit; re-reads `value` when it
   * changes from outside. Test ids: {testid}-name-{i} {testid}-value-{i} {testid}-mode-{i}-set|add {testid}-always-{i}
   * {testid}-del-{i} {testid}-add {testid}-remove {testid}-errors.
   */
  import type { EdgeResponseHeaders } from '@opm/shared/types';
  import type { ExprContext } from '@opm/shared/expr';
  import Combobox from '../../primitives/Combobox.svelte';
  import ExprInput from '../../primitives/ExprInput.svelte';
  import SegmentedControl from '../../primitives/SegmentedControl.svelte';
  import ChipList from '../../primitives/ChipList.svelte';
  import Toggle from '../../primitives/Toggle.svelte';
  import Btn from '../../primitives/Btn.svelte';
  import { RESPONSE_HEADERS, headersOf, rowErrors, rowsOf, type RespRow } from './headers';

  type Props = { value: EdgeResponseHeaders; ctx?: ExprContext | null; disabled?: boolean; testid?: string; onchange: (h: EdgeResponseHeaders, errors: string[]) => void };
  let { value, ctx = null, disabled = false, testid = 'response-headers', onchange }: Props = $props();
  let rows = $state<RespRow[]>([]);
  let remove = $state<string[]>([]);
  let last = '';
  $effect.pre(() => {
    const j = JSON.stringify(value);
    if (j === last) return;
    last = j;
    rows = rowsOf(value);
    remove = [...value.remove];
  });
  const errs = $derived(rowErrors(rows));
  function emit() {
    const snap = $state.snapshot(rows) as RespRow[];
    const h = headersOf(snap, $state.snapshot(remove) as string[]);
    last = JSON.stringify(h);
    onchange(h, rowErrors(snap));
  }
  const nameOpts = $derived(RESPONSE_HEADERS.filter((h) => !rows.some((x) => x.name.toLowerCase() === h.toLowerCase())).map((h) => ({ value: h })));
  const MODES = [
    { value: 'set' as const, label: 'set', title: 'Replace the container’s value (nginx proxy_hide_header + add_header)' },
    { value: 'add' as const, label: 'add', title: 'Append; the container’s value stays (nginx add_header)' },
  ];
</script>

<div class="rh" data-testid={testid}>
  {#if rows.length}
    <div class="hdr"><span>Header</span><span>Value</span><span>Mode</span><span title="nginx “always”: also on error responses (4xx / 5xx and the edge’s own 404 / 413 / 502 pages)">Always</span><span></span></div>
  {/if}
  {#each rows as r, i (i)}
    <div class="r" data-testid="{testid}-row-{i}">
      <div class="c"><Combobox value={r.name} options={nameOpts} allowCustom mono size="sm" placeholder="Cache-Control" ariaLabel="Response header name" {disabled} testid="{testid}-name-{i}" onchange={(t) => ((r.name = t.trim()), emit())} /></div>
      <div class="c"><ExprInput value={r.value} onchange={(v) => ((r.value = v), emit())} field="edge" {ctx} size="sm" preview={false} placeholder="public, max-age=3600" ariaLabel="Value of {r.name || 'header'}" {disabled} testid="{testid}-value-{i}" /></div>
      <div class="c"><SegmentedControl options={MODES} value={r.mode} size="sm" {disabled} ariaLabel="Mode of {r.name || 'header'}" testid="{testid}-mode-{i}" onchange={(m) => ((r.mode = m), emit())} /></div>
      <div class="c al"><Toggle checked={r.always} {disabled} ariaLabel="Always (also on error responses)" testid="{testid}-always-{i}" onchange={(v) => ((r.always = v), emit())} /></div>
      {#if !disabled}<Btn size="sm" variant="ghost" icon="x" aria-label="Remove row {r.name}" onclick={() => (rows.splice(i, 1), emit())} data-testid="{testid}-del-{i}" />{:else}<span></span>{/if}
    </div>
  {/each}
  {#if errs.length}<div class="field-err" data-testid="{testid}-errors">{errs.join(' · ')}</div>{/if}
  {#if !disabled}<div><Btn size="sm" icon="plus" onclick={() => (rows.push({ name: '', value: '', mode: 'set', always: false }), emit())} data-testid="{testid}-add">Add response header</Btn></div>{/if}
  <div class="rm">
    <span class="hint">Remove from the container’s response (nginx proxy_hide_header)</span>
    <ChipList value={remove} mono placeholder="X-Powered-By" {disabled} testid="{testid}-remove" onchange={(v) => ((remove = v), emit())} />
  </div>
</div>

<style>
  .rh {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .hdr,
  .r {
    display: grid;
    grid-template-columns: minmax(130px, 1fr) minmax(0, 1.5fr) auto 56px 28px;
    gap: 6px;
    align-items: center;
  }
  .hdr {
    font-size: var(--fs-xs);
    color: var(--text-3);
  }
  .c {
    min-width: 0;
  }
  .al {
    display: flex;
    justify-content: center;
  }
  .rm {
    display: flex;
    flex-direction: column;
    gap: 4px;
    margin-top: 4px;
  }
  @media (max-width: 640px) {
    .hdr {
      display: none;
    }
    .r {
      grid-template-columns: minmax(0, 1fr) auto auto auto;
      padding-bottom: 6px;
      border-bottom: 1px dashed var(--border);
    }
    .r > .c:nth-child(2) {
      grid-column: 1 / -1;
      order: 5;
    }
  }
</style>
