<script lang="ts" module>
  export const SWATCHES = ['#2563eb', '#4f46e5', '#7c3aed', '#c026d3', '#db2777', '#dc2626', '#ea580c', '#d97706', '#65a30d', '#16a34a', '#0d9488', '#0891b2', '#475569', '#71717a'];
</script>

<script lang="ts">
  /** Colour = swatches + a custom lower-case hex (#rrggbb) with the native picker. */
  import Icon from './Icon.svelte';
  import { Check } from './icons';
  type Props = { value: string; disabled?: boolean; invalid?: boolean; id?: string; testid?: string; onchange?: (v: string) => void };
  let { value = $bindable(), disabled = false, invalid = false, id, testid, onchange }: Props = $props();
  function set(v: string) {
    value = v.toLowerCase();
    onchange?.(value);
  }
  const custom = $derived(!SWATCHES.includes(value));
</script>

<div class="cp" data-testid={testid}>
  <div class="sw" role="radiogroup" aria-label="Colour">
    {#each SWATCHES as c (c)}
      <button type="button" role="radio" aria-checked={value === c} class="s" style="--c:{c}" aria-label={c} title={c} {disabled} onclick={() => set(c)} data-testid="swatch-{c.slice(1)}">
        {#if value === c}<Icon icon={Check} size={12} stroke={3} />{/if}
      </button>
    {/each}
  </div>
  <div class="row cu">
    <label class="nat" class:on={custom} style="--c:{/^#[0-9a-f]{6}$/i.test(value) ? value : '#ffffff'}" title="Custom colour">
      <input type="color" value={/^#[0-9a-f]{6}$/i.test(value) ? value.toLowerCase() : '#000000'} oninput={(e) => set(e.currentTarget.value)} {disabled} aria-label="Pick a custom colour" />
    </label>
    <input {id} class="input sm mono hex" class:invalid value={value} oninput={(e) => set(e.currentTarget.value.trim())} maxlength="7" placeholder="#4f46e5" {disabled} aria-label="Hex colour" data-testid={testid ? `${testid}-hex` : undefined} />
  </div>
</div>

<style>
  .cp {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .sw {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .s {
    width: 24px;
    height: 24px;
    border-radius: 50%;
    border: 2px solid var(--surface);
    box-shadow: 0 0 0 1px var(--border-strong);
    background: var(--c);
    color: #fff;
    display: grid;
    place-items: center;
    cursor: pointer;
    padding: 0;
  }
  .s[aria-checked='true'] {
    box-shadow: 0 0 0 2px var(--c);
  }
  .s:focus-visible {
    outline: none;
    box-shadow: var(--focus-ring);
  }
  .cu {
    gap: 8px;
    align-items: center;
  }
  .nat {
    width: 28px;
    height: 28px;
    border-radius: var(--r-md);
    border: 1px solid var(--border-strong);
    background: conic-gradient(#ef4444, #eab308, #22c55e, #06b6d4, #6366f1, #d946ef, #ef4444);
    position: relative;
    cursor: pointer;
    flex: none;
  }
  .nat.on {
    background: var(--c);
  }
  .nat input {
    position: absolute;
    inset: 0;
    opacity: 0;
    cursor: pointer;
    width: 100%;
    height: 100%;
  }
  .hex {
    width: 110px;
  }
</style>
