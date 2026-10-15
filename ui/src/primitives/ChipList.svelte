<script lang="ts">
  /** Editable list of strings (variable lists, command lists). Enter / comma adds; Backspace on empty removes last. */
  import Icon from './Icon.svelte';
  interface Props {
    value?: string[];
    onchange?: (v: string[]) => void;
    testid?: string;
    invalid?: boolean;
    placeholder?: string;
    disabled?: boolean;
    id?: string;
    mono?: boolean;
    max?: number;
  }
  let { value = $bindable([]), onchange, placeholder = 'Add…', disabled = false, id, mono = false, max = 500, testid, invalid = false }: Props = $props();
  let text = $state('');
  function set(v: string[]) {
    value = v;
    onchange?.(v);
  }
  function add() {
    const t = text.trim();
    if (!t || value.length >= max) return;
    set([...value, t]);
    text = '';
  }
  function key(e: KeyboardEvent) {
    if (e.key === 'Enter' || (e.key === ',' && !mono)) {
      e.preventDefault();
      add();
    } else if (e.key === 'Backspace' && !text && value.length) set(value.slice(0, -1));
  }
</script>

<div class="chips" class:disabled class:invalid data-testid={testid}>
  {#each value as v, i (i)}
    <span class="chip" class:mono>{v}{#if !disabled}<button type="button" aria-label="Remove {v}" onclick={() => set(value.filter((_, k) => k !== i))}><Icon name="x" size={11} /></button>{/if}</span>
  {/each}
  {#if !disabled}<input {id} class:mono bind:value={text} onkeydown={key} onblur={add} {placeholder} aria-label={placeholder} data-testid={testid ? `${testid}-input` : undefined} />{/if}
</div>

<style>
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    align-items: center;
    min-height: 34px;
    padding: 3px 6px;
    border: 1px solid var(--border-strong);
    border-radius: var(--r-md);
    background: var(--surface);
    min-width: 0;
  }
  .chips:focus-within {
    border-color: var(--accent);
    box-shadow: var(--focus-ring);
  }
  .chips.invalid {
    border-color: var(--red);
  }
  .chips.disabled {
    background: var(--gray-soft);
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    font-size: 12.5px;
    background: var(--gray-soft);
    border-radius: 6px;
    padding: 2px 4px 2px 8px;
    max-width: 100%;
    overflow-wrap: anywhere;
  }
  .chip.mono,
  input.mono {
    font-family: var(--font-mono);
    font-size: 12px;
  }
  .chip button {
    border: 0;
    background: none;
    cursor: pointer;
    color: var(--text-2);
    padding: 2px;
    display: grid;
    border-radius: 4px;
  }
  .chip button:hover {
    color: var(--red-text);
    background: var(--red-soft);
  }
  input {
    flex: 1;
    min-width: 90px;
    border: 0;
    outline: 0;
    background: transparent;
    font: inherit;
    font-size: 13px;
    color: var(--text);
    height: 26px;
  }
</style>
