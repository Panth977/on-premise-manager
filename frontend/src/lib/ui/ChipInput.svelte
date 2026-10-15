<script lang="ts">
  import Icon from './Icon.svelte';
  import { X } from './icons';
  type Props = {
    value: string[];
    placeholder?: string;
    max?: number;
    disabled?: boolean;
    invalid?: boolean;
    id?: string;
    testid?: string;
    /** mark the first chip as the default */
    firstIsDefault?: boolean;
    onchange?: (v: string[]) => void;
  };
  let { value = $bindable([]), placeholder = 'Type and press Enter', max = 100, disabled = false, invalid = false, id, testid, firstIsDefault = false, onchange }: Props = $props();
  let text = $state('');
  let dupe = $state(false);
  function commit() {
    const t = text.trim();
    if (!t) return;
    if (value.includes(t)) {
      dupe = true;
      return;
    }
    if (value.length >= max) return;
    value = [...value, t];
    text = '';
    dupe = false;
    onchange?.(value);
  }
  function remove(i: number) {
    value = value.filter((_, j) => j !== i);
    onchange?.(value);
  }
  function onkey(e: KeyboardEvent) {
    if (e.key === 'Enter' || e.key === ',') {
      e.preventDefault();
      commit();
    } else if (e.key === 'Backspace' && !text && value.length) {
      remove(value.length - 1);
    }
  }
</script>

<div class="ci input" class:invalid class:disabled data-testid={testid}>
  {#each value as v, i (`${i}:${v}`)}
    <span class="chip c" class:def={firstIsDefault && i === 0} title={firstIsDefault && i === 0 ? 'default' : undefined}>
      {v}
      {#if !disabled}<button type="button" aria-label="Remove {v}" onclick={() => remove(i)}><Icon icon={X} size={11} /></button>{/if}
    </span>
  {/each}
  {#if !disabled && value.length < max}
    <input {id} bind:value={text} onkeydown={onkey} onblur={commit} oninput={() => (dupe = false)} placeholder={value.length ? '' : placeholder} aria-label={placeholder} />
  {/if}
</div>
{#if dupe}<div class="xs text-amber">“{text.trim()}” is already in the list</div>{/if}

<style>
  .ci {
    height: auto;
    min-height: var(--control-h);
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    padding: 4px 6px;
    align-items: center;
  }
  .ci:focus-within {
    border-color: var(--accent);
    box-shadow: var(--focus-ring);
  }
  .ci.disabled {
    background: var(--surface-2);
  }
  input {
    border: 0;
    outline: none;
    background: transparent;
    flex: 1;
    min-width: 100px;
    height: 22px;
    font-size: var(--fs-md);
  }
  input:focus {
    box-shadow: none;
  }
  .c {
    font-size: var(--fs-sm);
    color: var(--text);
    background: var(--surface-active);
    border-color: transparent;
  }
  .c.def {
    background: var(--accent-soft);
    color: var(--accent-text);
  }
  .c button {
    border: 0;
    background: transparent;
    padding: 0;
    cursor: pointer;
    color: inherit;
    display: inline-flex;
    opacity: 0.6;
  }
  .c button:hover {
    opacity: 1;
  }
</style>
