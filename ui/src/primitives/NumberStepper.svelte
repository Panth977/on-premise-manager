<script lang="ts">
  /**
   * Integer / decimal input with − / + buttons (ports, timeouts, sizes). nullable = empty means "off / not set"
   * (placeholder explains it). Out-of-range values are kept so the shared validator can show its message.
   */
  import Icon from './Icon.svelte';
  interface Props {
    value?: number | null;
    onchange?: (v: number | null) => void;
    min?: number;
    max?: number;
    step?: number;
    nullable?: boolean;
    placeholder?: string;
    id?: string;
    label?: string;
    invalid?: boolean;
    disabled?: boolean;
    testid?: string;
    size?: 'sm' | 'md';
    suffix?: string;
  }
  let { value = $bindable(null), onchange, min, max, step = 1, nullable = false, placeholder, id, label = 'value', invalid = false, disabled = false, testid, size = 'md', suffix }: Props = $props();
  const dec = $derived((step % 1 !== 0 ? String(step).split('.')[1]?.length : 0) ?? 0);
  function bump(d: number) {
    const base = value === null || Number.isNaN(value) ? (d > 0 ? (min ?? 0) - step : (max ?? min ?? 0) + step) : value;
    let n = Number((base + d * step).toFixed(dec));
    if (min !== undefined) n = Math.max(min, n);
    if (max !== undefined) n = Math.min(max, n);
    set(n);
  }
  function set(n: number | null) {
    value = n;
    onchange?.(n);
  }
  function oninput(e: Event & { currentTarget: HTMLInputElement }) {
    const t = e.currentTarget.value.trim();
    if (t === '') set(nullable ? null : NaN);
    else set(Number(t));
  }
</script>

<div class="stp {size}" class:invalid class:disabled>
  <button type="button" tabindex="-1" aria-label="Decrease {label}" disabled={disabled || (value !== null && min !== undefined && value <= min)} onclick={() => bump(-1)}><Icon name="minus" size={13} /></button>
  <input {id} type="number" inputmode={dec ? 'decimal' : 'numeric'} {min} {max} {step} {placeholder} {disabled} value={value === null || Number.isNaN(value) ? '' : value} aria-invalid={invalid ? 'true' : undefined} aria-label={id ? undefined : label} data-testid={testid} {oninput} />
  {#if suffix}<span class="sfx">{suffix}</span>{/if}
  <button type="button" tabindex="-1" aria-label="Increase {label}" disabled={disabled || (value !== null && max !== undefined && value >= max)} onclick={() => bump(1)}><Icon name="plus" size={13} /></button>
</div>

<style>
  .stp {
    display: inline-flex;
    align-items: stretch;
    height: 34px;
    border: 1px solid var(--border-strong);
    border-radius: var(--r-md);
    background: var(--surface);
    min-width: 0;
    max-width: 100%;
    width: 100%;
    overflow: hidden;
  }
  .stp.sm {
    height: 28px;
  }
  .stp:focus-within {
    border-color: var(--accent);
    box-shadow: var(--focus-ring);
  }
  .stp.invalid {
    border-color: var(--red);
  }
  .stp.disabled {
    background: var(--gray-soft);
  }
  input {
    flex: 1;
    min-width: 40px;
    border: 0;
    outline: 0;
    background: transparent;
    font: inherit;
    font-size: 13.5px;
    color: var(--text);
    text-align: center;
    font-variant-numeric: tabular-nums;
    -moz-appearance: textfield;
    appearance: textfield;
  }
  .sm input {
    font-size: 12.5px;
  }
  input::-webkit-inner-spin-button,
  input::-webkit-outer-spin-button {
    -webkit-appearance: none;
    margin: 0;
  }
  button {
    flex: none;
    width: 30px;
    border: 0;
    background: var(--surface-2);
    color: var(--text-2);
    display: grid;
    place-items: center;
    cursor: pointer;
    padding: 0;
  }
  .sm button {
    width: 24px;
  }
  button:first-child {
    border-right: 1px solid var(--border);
  }
  button:last-child {
    border-left: 1px solid var(--border);
  }
  button:hover:not(:disabled) {
    background: var(--gray-soft);
    color: var(--text);
  }
  button:disabled {
    color: var(--text-3);
    cursor: not-allowed;
  }
  .sfx {
    align-self: center;
    padding-right: 6px;
    font-size: 12px;
    color: var(--text-2);
  }
</style>
