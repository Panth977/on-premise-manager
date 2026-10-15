<script lang="ts" module>
  export interface SegOption<T extends string = string> {
    value: T;
    label?: string;
    title?: string;
  }
</script>

<script lang="ts" generics="T extends string">
  /** Radio group as a segmented control (arrow keys move + select). Options: values or { value, label, title }. */
  type Props = {
    options: readonly (T | SegOption<T>)[];
    value: T;
    disabled?: boolean;
    size?: 'md' | 'sm';
    ariaLabel?: string;
    id?: string;
    testid?: string;
    onchange?: (v: T) => void;
  };
  let { options, value = $bindable(), disabled = false, size = 'md', ariaLabel, id, testid, onchange }: Props = $props();
  const opts = $derived(options.map((o) => (typeof o === 'string' ? { value: o as T, label: o as string } : { ...o, label: o.label ?? o.value })));
  let root = $state<HTMLElement | null>(null);
  function pick(v: T) {
    if (disabled || v === value) return;
    value = v;
    onchange?.(v);
  }
  function onkeydown(e: KeyboardEvent) {
    const i = opts.findIndex((o) => o.value === value);
    let n = -1;
    if (e.key === 'ArrowRight' || e.key === 'ArrowDown') n = (i + 1) % opts.length;
    else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') n = (i - 1 + opts.length) % opts.length;
    else if (e.key === 'Home') n = 0;
    else if (e.key === 'End') n = opts.length - 1;
    if (n < 0) return;
    e.preventDefault();
    pick(opts[n]!.value);
    root?.querySelectorAll<HTMLButtonElement>('button')[n]?.focus();
  }
</script>

<div class="seg {size}" role="radiogroup" tabindex="-1" aria-label={ariaLabel} {id} bind:this={root} data-testid={testid} {onkeydown}>
  {#each opts as o (o.value)}
    <button type="button" role="radio" aria-checked={o.value === value} class:on={o.value === value} tabindex={o.value === value || (!opts.some((x) => x.value === value) && o === opts[0]) ? 0 : -1} title={o.title} {disabled} onclick={() => pick(o.value)} data-testid={testid ? `${testid}-${o.value}` : undefined}>{o.label}</button>
  {/each}
</div>

<style>
  .seg {
    align-self: flex-start;
    display: inline-flex;
    max-width: 100%;
    flex-wrap: wrap;
    border: 1px solid var(--border-strong);
    border-radius: var(--r-md);
    background: var(--surface-2);
    padding: 2px;
    gap: 2px;
  }
  .seg button {
    border: 0;
    background: transparent;
    padding: 0 12px;
    min-height: calc(var(--control-h) - 6px);
    cursor: pointer;
    font-size: var(--fs-md);
    color: var(--text-2);
    border-radius: calc(var(--r-md) - 2px);
    white-space: nowrap;
  }
  .seg.sm button {
    min-height: calc(var(--control-h-sm) - 6px);
    padding: 0 9px;
    font-size: var(--fs-sm);
  }
  .seg button:hover:not(:disabled):not(.on) {
    color: var(--text);
    background: var(--surface-hover);
  }
  .seg button.on {
    background: var(--surface);
    color: var(--accent-text);
    font-weight: var(--fw-medium);
    box-shadow: var(--shadow-sm);
  }
  .seg button:disabled {
    cursor: not-allowed;
    opacity: 0.6;
  }
  .seg button:focus-visible {
    outline: none;
    box-shadow: var(--focus-ring);
  }
</style>
