<script lang="ts">
  import { detectEscLost, renderAnsiLines, stripAnsi } from '@opm/shared';
  import Toggle from './Toggle.svelte';
  import CopyButton from './CopyButton.svelte';
  import Icon from './Icon.svelte';
  import { Download } from './icons';
  import { saveBlob } from '../util/download';

  /**
   * A log tail with ANSI colours (SPEC §21 #93) on top of the shared parser (`@opm/shared` ansi.ts): SGR → styled spans with
   * the state carried across lines, every other escape removed. "Colors" off = plain visible text; "Raw" = the stored bytes
   * with ESC shown as ␛. A stream whose ESC bytes were lost ("[0m[1m[31merror[0m") is detected once for the whole tail
   * (detectEscLost) — a plain "[1]" is never touched. Copy = the visible text (stripAnsi); Download = the raw bytes.
   */
  type Props = { lines: readonly string[]; testid?: string; filename?: string; maxHeight?: string };
  let { lines, testid = 'ansi-log', filename = 'log.txt', maxHeight = '320px' }: Props = $props();
  let colors = $state(true);
  let raw = $state(false);
  const escLost = $derived(detectEscLost(lines));
  const html = $derived(renderAnsiLines(lines, { colors, raw, escLost }));
  const visible = $derived(lines.map((l) => stripAnsi(l, { escLost })).join('\n'));
  function download() {
    saveBlob(new Blob([lines.join('\n') + '\n'], { type: 'text/plain;charset=utf-8' }), filename);
  }
</script>

<div class="alog" data-testid={testid} data-colors={colors && !raw ? 'on' : 'off'} data-raw={raw ? 'on' : 'off'} data-esc-lost={escLost ? 'true' : 'false'}>
  <div class="bar">
    <Toggle bind:checked={colors} disabled={raw} label="Colors" testid="{testid}-colors" />
    <Toggle bind:checked={raw} label="Raw" testid="{testid}-raw" />
    {#if escLost}<span class="xs faint" data-tip="The ESC bytes of this log were lost on the way; bare [..m codes are read as colours">ESC lost</span>{/if}
    <span class="grow"></span>
    <CopyButton text={visible} tip="Copy the visible text" testid="{testid}-copy" />
    <button type="button" class="btn sm icon ghost" onclick={download} data-tip="Download the raw log" aria-label="Download the raw log" data-testid="{testid}-download"><Icon icon={Download} size={13} /></button>
  </div>
  <div class="body code" style="max-height:{maxHeight}" data-testid="{testid}-body">
    {#each html as h, i (i)}<div class="ln">{@html h || '&#8203;'}</div>{/each}
  </div>
</div>

<style>
  .alog {
    display: grid;
    gap: 6px;
    min-width: 0;
  }
  .bar {
    display: flex;
    align-items: center;
    gap: 14px;
    flex-wrap: wrap;
    font-size: var(--fs-sm, 12px);
  }
  .grow {
    flex: 1;
  }
  .body {
    color: var(--ansi-fg);
    background: var(--ansi-bg);
    overflow: auto;
    white-space: normal;
    padding: 8px 10px;
  }
  .ln {
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    min-height: 1.4em;
    line-height: 1.4;
  }
</style>
