<script lang="ts" generics="T extends string | number">
  /** Single choice from a small closed set (2–5 options): ARIA radiogroup with roving focus (arrow keys). */
  interface Opt {
    value: T;
    label?: string;
    title?: string;
    disabled?: boolean;
  }
  interface Props {
    options: Opt[];
    value?: T;
    onchange?: (v: T) => void;
    label?: string;
    ariaLabel?: string;
    id?: string;
    size?: 'sm' | 'md';
    disabled?: boolean;
    testid?: string;
  }
  let { options, value = $bindable(), onchange, label, ariaLabel, id, size = 'md', disabled = false, testid }: Props = $props();
  let root: HTMLDivElement | undefined = $state();
  function key(e: KeyboardEvent, i: number) {
    const d = e.key === 'ArrowRight' || e.key === 'ArrowDown' ? 1 : e.key === 'ArrowLeft' || e.key === 'ArrowUp' ? -1 : 0;
    if (!d) return;
    e.preventDefault();
    let j = i;
    for (let k = 0; k < options.length; k++) {
      j = (j + d + options.length) % options.length;
      if (!options[j].disabled) break;
    }
    if (options[j].disabled) return;
    pick(options[j].value);
    (root?.querySelectorAll('button')[j] as HTMLButtonElement | undefined)?.focus();
  }
  function pick(v: T) {
    if (v === value) return;
    value = v;
    onchange?.(v);
  }
  const current = $derived(options.findIndex((o) => o.value === value));
</script>

<div bind:this={root} {id} class="seg {size}" role="radiogroup" aria-label={ariaLabel ?? label} data-testid={testid}>
  {#each options as o, i (o.value)}
    <button
      type="button"
      role="radio"
      aria-checked={o.value === value}
      tabindex={(current < 0 ? i === 0 : i === current) ? 0 : -1}
      disabled={disabled || o.disabled}
      title={o.title}
      onclick={() => pick(o.value)}
      data-testid={testid ? `${testid}-${o.value}` : undefined}
      onkeydown={(e) => key(e, i)}>{o.label ?? String(o.value)}</button>
  {/each}
</div>

<style>
  .seg {
    display: inline-flex;
    flex-wrap: wrap;
    gap: 2px;
    padding: 2px;
    border: 1px solid var(--border-strong);
    border-radius: var(--r-md);
    background: var(--gray-soft);
    max-width: 100%;
    align-self: flex-start;
  }
  button {
    font: inherit;
    font-size: 13px;
    border: 0;
    background: none;
    color: var(--text-2);
    padding: 0 11px;
    height: 28px;
    border-radius: 5px;
    cursor: pointer;
    white-space: nowrap;
  }
  .sm button {
    font-size: 12px;
    height: 22px;
    padding: 0 8px;
  }
  button:hover:not(:disabled):not([aria-checked='true']) {
    color: var(--text);
    background: color-mix(in srgb, var(--surface) 60%, transparent);
  }
  button[aria-checked='true'] {
    background: var(--surface);
    color: var(--accent-text);
    font-weight: var(--fw-semibold);
    box-shadow: var(--shadow-sm);
  }
  button:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }
</style>
