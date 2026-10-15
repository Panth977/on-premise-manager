<script lang="ts">
  /**
   * Value viewer of one cell (§21 #97): type-aware — JSON pretty (folding editor), text, bytes as hex dump, datetime
   * as UTC / local / epoch; NULL explicit; a truncated value shows its preview + "Download full value" (resultValue).
   */
  import type { CellValue, ResultColumn } from '@opm/shared/devtools';
  import Modal from '$lib/ui/Modal.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import { toasts } from '$lib/state/toast.svelte';
  import { downloadUrl } from '$lib/tools/api';
  import Editor from './cm/Editor.svelte';
  import { bytesOf, cellFull, copyText, hexDump, isTrunc } from './grid';

  interface Props {
    open: boolean;
    column: ResultColumn | null;
    value: CellValue | undefined;
    /** link of the full value (truncated cells) */
    fullUrl?: string | null;
  }
  let { open = $bindable(false), column, value, fullUrl = null }: Props = $props();
  type Mode = 'json' | 'text' | 'hex';
  const isJson = $derived(column?.display === 'json' || (typeof value === 'object' && value !== null && !isTrunc(value)) || (typeof value === 'string' && /^\s*[[{]/.test(value) && (() => { try { JSON.parse(value); return true; } catch { return false; } })()));
  let mode = $state<Mode>('text');
  $effect(() => {
    if (open) mode = column?.display === 'bytes' ? 'hex' : isJson ? 'json' : 'text';
  });
  const raw = $derived(cellFull(value));
  const pretty = $derived.by(() => {
    if (!isJson || isTrunc(value)) return raw;
    try {
      return JSON.stringify(typeof value === 'string' ? JSON.parse(value) : value, null, 2);
    } catch {
      return raw;
    }
  });
  const hex = $derived(mode === 'hex' ? hexDump(column?.display === 'bytes' && typeof value === 'string' ? bytesOf(value) : new TextEncoder().encode(raw)) : '');
  const dt = $derived(column?.display === 'datetime' && typeof value === 'string' && !Number.isNaN(Date.parse(value)) ? new Date(value) : null);
  async function copy() {
    if (await copyText(mode === 'json' ? pretty : raw)) toasts.ok('Copied');
  }
</script>

<Modal bind:open title={column ? `${column.name}` : 'Value'} description={column ? `${column.type}${isTrunc(value) ? ` · ${value.sizeBytes.toLocaleString()} bytes (cut at the inline limit)` : ''}` : undefined} size="lg" testid="cell-viewer">
  <div class="stack">
    {#if value === null || value === undefined}
      <p class="null">NULL <span class="faint small">(no value — not an empty string)</span></p>
    {:else}
      <div class="row gap-s wrap">
        <SegmentedControl options={[{ value: 'text' as Mode, label: 'Text' }, { value: 'json' as Mode, label: 'JSON', disabled: !isJson }, { value: 'hex' as Mode, label: 'Hex' }]} value={mode} onchange={(v) => (mode = v)} label="View as" size="sm" testid="cell-viewer-mode" />
        <span class="grow"></span>
        {#if isTrunc(value) && fullUrl && value.fetchable !== false}<Btn size="sm" icon="download" onclick={() => downloadUrl(fullUrl!, `${column?.name ?? 'value'}.txt`)} data-testid="cell-full">Download full value</Btn>{/if}
        <Btn size="sm" variant="ghost" icon="copy" onclick={copy}>Copy</Btn>
      </div>
      {#if dt}
        <div class="kvx small" data-testid="cell-dt">
          <span class="faint">UTC</span><span class="mono">{dt.toISOString()}</span>
          <span class="faint">Local</span><span class="mono">{dt.toLocaleString()}</span>
          <span class="faint">Epoch ms</span><span class="mono">{dt.getTime()}</span>
        </div>
      {/if}
      {#if mode === 'json'}
        <div class="box"><Editor value={pretty} lang="json" readonly height="min(56vh, 520px)" testid="cell-viewer-json" ariaLabel="Value (JSON)" /></div>
      {:else if mode === 'hex'}
        <pre class="pre mono">{hex}</pre>
      {:else}
        <pre class="pre mono" data-testid="cell-viewer-text">{raw}</pre>
      {/if}
      {#if isTrunc(value)}<p class="hint" data-testid="cell-trunc-hint">{value.fetchable === false ? 'Only the first part is shown here and this value cannot be downloaded from the result — open it in the key browser.' : 'Only the first part is shown here. The full value stays on the agent with the result (15 min).'}</p>{/if}
    {/if}
  </div>
</Modal>

<style>
  .null {
    font-style: italic;
    color: var(--text-3);
  }
  .wrap {
    flex-wrap: wrap;
  }
  .box {
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    overflow: hidden;
  }
  .pre {
    margin: 0;
    max-height: min(56vh, 520px);
    overflow: auto;
    padding: 10px 12px;
    background: var(--code-bg);
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    font-size: 12.5px;
    white-space: pre-wrap;
    word-break: break-word;
  }
  .kvx {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 2px 12px;
  }
</style>
