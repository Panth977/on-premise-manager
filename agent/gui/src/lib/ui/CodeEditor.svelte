<script lang="ts">
  /** Light code editor: textarea + line numbers, Tab inserts 2 spaces, byte counter (no heavy deps in the embedded GUI). */
  let { value = $bindable(''), id, rows = 22, maxBytes, readonly = false, label = 'Content' }: { value: string; id?: string; rows?: number; maxBytes?: number; readonly?: boolean; label?: string } = $props();
  let ta: HTMLTextAreaElement | undefined = $state();
  let gutter: HTMLDivElement | undefined = $state();
  const lines = $derived(value.split('\n').length);
  const size = $derived(new TextEncoder().encode(value).length);
  function key(e: KeyboardEvent) {
    if (e.key !== 'Tab' || readonly || !ta) return;
    e.preventDefault();
    const s = ta.selectionStart;
    const en = ta.selectionEnd;
    value = value.slice(0, s) + '  ' + value.slice(en);
    requestAnimationFrame(() => ta && (ta.selectionStart = ta.selectionEnd = s + 2));
  }
</script>

<div class="ed">
  <div class="gutter" bind:this={gutter} aria-hidden="true">{#each Array(lines) as _, i (i)}<div>{i + 1}</div>{/each}</div>
  <textarea bind:this={ta} {id} bind:value {rows} {readonly} spellcheck="false" aria-label={label} onkeydown={key} onscroll={() => gutter && ta && (gutter.scrollTop = ta.scrollTop)} data-testid="code-editor"></textarea>
</div>
<div class="row between hint" style="margin-top:4px">
  <span>{lines} lines</span>
  <span class:bad-text={maxBytes !== undefined && size > maxBytes}>{size.toLocaleString()} bytes{#if maxBytes !== undefined} / max {maxBytes.toLocaleString()}{/if}</span>
</div>

<style>
  .ed {
    display: flex;
    border: 1px solid var(--border-strong);
    border-radius: var(--r-md);
    background: var(--surface);
    overflow: hidden;
  }
  .ed:focus-within {
    border-color: var(--accent);
    box-shadow: var(--focus-ring);
  }
  .gutter {
    padding: 8px 8px 8px 10px;
    background: var(--surface-2);
    border-right: 1px solid var(--border);
    color: var(--text-3);
    font-family: var(--font-mono);
    font-size: 12px;
    line-height: 1.6;
    text-align: right;
    user-select: none;
    overflow: hidden;
    min-width: 42px;
  }
  textarea {
    flex: 1;
    border: 0;
    outline: 0;
    resize: vertical;
    padding: 8px 10px;
    font-family: var(--font-mono);
    font-size: 12px;
    line-height: 1.6;
    background: transparent;
    color: var(--text);
    white-space: pre;
    overflow: auto;
    min-height: 200px;
    tab-size: 2;
  }
</style>
