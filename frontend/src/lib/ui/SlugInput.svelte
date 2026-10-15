<script lang="ts" module>
  /** Label → slug (^[a-z][a-z0-9-]{1,39}$ best effort). */
  export function slugify(label: string, max = 40): string {
    return label
      .toLowerCase()
      .normalize('NFKD')
      .replace(/[̀-ͯ]/g, '')
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^[^a-z]+/, '')
      .slice(0, max)
      .replace(/-+$/, '');
  }
</script>

<script lang="ts">
  /**
   * Slug derived from a label: shows the derived slug locked (follows `from`); the pencil unlocks free editing,
   * the reset button goes back to auto. `fixed` = read-only (slug cannot change after create).
   */
  import Icon from './Icon.svelte';
  import { Pencil, RotateCcw, Lock } from './icons';
  /** manual (bindable, default false): start unlocked (e.g. editing an existing id that must not be re-derived). */
  type Props = { value: string; from: string; derive?: (label: string) => string; fixed?: boolean; invalid?: boolean; id?: string; testid?: string; placeholder?: string; max?: number; manual?: boolean };
  let { value = $bindable(), from, derive, fixed = false, invalid = false, id, testid, placeholder = 'my-slug', max = 40, manual = $bindable(false) }: Props = $props();
  let el = $state<HTMLInputElement | null>(null);
  $effect(() => {
    const s = derive ? derive(from ?? '') : slugify(from ?? '', max);
    if (!manual && !fixed && value !== s) value = s;
  });
  function unlock() {
    manual = true;
    queueMicrotask(() => el?.focus());
  }
</script>

<div class="si" class:invalid class:locked={!manual || fixed} data-testid={testid ? `${testid}-wrap` : undefined}>
  {#if fixed}<Icon icon={Lock} size={13} class="lk" />{/if}
  <input bind:this={el} {id} class="v mono" bind:value readonly={!manual || fixed} {placeholder} maxlength={max} spellcheck="false" autocomplete="off" data-testid={testid} oninput={(e) => (value = e.currentTarget.value.toLowerCase())} />
  {#if !fixed}
    {#if manual}
      <button type="button" class="b" title="Derive from the label again" aria-label="Derive the slug from the label" onclick={() => (manual = false)} data-testid={testid ? `${testid}-auto` : undefined}><Icon icon={RotateCcw} size={13} /></button>
    {:else}
      <span class="auto">auto</span>
      <button type="button" class="b" title="Edit the slug" aria-label="Edit the slug" onclick={unlock} data-testid={testid ? `${testid}-edit` : undefined}><Icon icon={Pencil} size={13} /></button>
    {/if}
  {/if}
</div>

<style>
  .si {
    display: flex;
    align-items: center;
    height: var(--control-h);
    border: 1px solid var(--border-strong);
    border-radius: var(--r-md);
    background: var(--surface);
    padding: 0 4px 0 10px;
    gap: 6px;
  }
  .si.locked {
    background: var(--surface-2);
  }
  .si:focus-within {
    border-color: var(--accent);
    box-shadow: var(--focus-ring);
  }
  .si.invalid {
    border-color: var(--red);
  }
  .si :global(.lk) {
    color: var(--text-3);
  }
  .v {
    flex: 1;
    min-width: 0;
    border: 0;
    outline: none;
    background: transparent;
    color: var(--text);
    font-size: var(--fs-md);
  }
  .v:focus {
    box-shadow: none;
  }
  .locked .v {
    color: var(--text-2);
  }
  .auto {
    font-size: var(--fs-xs);
    color: var(--text-3);
    background: var(--surface-active);
    border-radius: 999px;
    padding: 1px 7px;
  }
  .b {
    border: 0;
    background: transparent;
    color: var(--text-2);
    width: 28px;
    height: 28px;
    border-radius: var(--r-sm);
    display: grid;
    place-items: center;
    cursor: pointer;
  }
  .b:hover {
    background: var(--surface-hover);
    color: var(--text);
  }
</style>
