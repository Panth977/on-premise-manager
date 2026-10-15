<script lang="ts">
  /** Integer input with − / + buttons (clamped to min/max on step; typing is free, validation shows range errors). */
  import Icon from './Icon.svelte';
  import { Minus, Plus } from './icons';
  type Props = {
    value: number;
    min?: number;
    max?: number;
    step?: number;
    disabled?: boolean;
    invalid?: boolean;
    size?: 'md' | 'sm';
    id?: string;
    testid?: string;
    ariaLabel?: string;
    suffix?: string;
    onchange?: (v: number) => void;
  };
  let { value = $bindable(), min = -Infinity, max = Infinity, step = 1, disabled = false, invalid = false, size = 'md', id, testid, ariaLabel, suffix, onchange }: Props = $props();
  const clamp = (n: number) => Math.min(max, Math.max(min, n));
  function bump(d: number) {
    const base = Number.isFinite(value) ? value : Number.isFinite(min) ? min : 0;
    value = clamp(base + d);
    onchange?.(value);
  }
  function oninput(e: Event & { currentTarget: HTMLInputElement }) {
    const t = e.currentTarget.value.trim();
    value = t === '' ? NaN : Number(t);
    onchange?.(value);
  }
</script>

<div class="ns {size}" class:invalid class:disabled data-testid={testid}>
  <button type="button" class="b" aria-label="Decrease" tabindex="-1" disabled={disabled || value <= min} onclick={() => bump(-step)}><Icon icon={Minus} size={13} /></button>
  <input {id} class="v" inputmode="numeric" value={Number.isFinite(value) ? String(value) : ''} {oninput} {disabled} aria-label={ariaLabel} onkeydown={(e) => (e.key === 'ArrowUp' ? (e.preventDefault(), bump(step)) : e.key === 'ArrowDown' ? (e.preventDefault(), bump(-step)) : null)} />
  {#if suffix}<span class="sfx">{suffix}</span>{/if}
  <button type="button" class="b" aria-label="Increase" tabindex="-1" disabled={disabled || value >= max} onclick={() => bump(step)}><Icon icon={Plus} size={13} /></button>
</div>

<style>
  .ns {
    display: inline-flex;
    align-items: stretch;
    height: var(--control-h);
    border: 1px solid var(--border-strong);
    border-radius: var(--r-md);
    background: var(--surface);
    overflow: hidden;
    max-width: 100%;
    width: 140px;
  }
  .ns.sm {
    height: var(--control-h-sm);
    width: 120px;
  }
  .ns:focus-within {
    border-color: var(--accent);
    box-shadow: var(--focus-ring);
  }
  .ns.invalid {
    border-color: var(--red);
  }
  .ns.disabled {
    background: var(--surface-2);
  }
  .b {
    border: 0;
    background: var(--surface-2);
    color: var(--text-2);
    width: 28px;
    display: grid;
    place-items: center;
    cursor: pointer;
    flex: none;
  }
  .b:hover:not(:disabled) {
    background: var(--surface-hover);
    color: var(--text);
  }
  .b:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }
  .v {
    border: 0;
    outline: none;
    background: transparent;
    min-width: 0;
    flex: 1;
    text-align: center;
    font-variant-numeric: tabular-nums;
    color: var(--text);
    font-size: var(--fs-md);
  }
  .v:focus {
    box-shadow: none;
  }
  .sfx {
    align-self: center;
    color: var(--text-3);
    font-size: var(--fs-xs);
    padding-right: 4px;
  }
</style>
