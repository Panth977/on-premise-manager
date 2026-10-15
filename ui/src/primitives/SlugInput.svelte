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
   * Slug derived from a label (both GUIs, §21 #102): shows the derived slug (follows `from`, "auto"); the pencil unlocks
   * free editing, the reset button goes back to auto. `fixed` = cannot change after create (lock). `disabled` = read mode.
   * Test ids: {testid} (the input) · {testid}-wrap · {testid}-edit · {testid}-auto.
   */
  import Icon from './Icon.svelte';
  type Props = { value: string; from: string; fixed?: boolean; disabled?: boolean; invalid?: boolean; id?: string; testid?: string; placeholder?: string; max?: number; manual?: boolean; onchange?: (v: string) => void };
  let { value = $bindable(), from, fixed = false, disabled = false, invalid = false, id, testid, placeholder = 'my-slug', max = 40, manual = $bindable(false), onchange }: Props = $props();
  let el = $state<HTMLInputElement | null>(null);
  const locked = $derived(fixed || disabled);
  function set(v: string) {
    if (v === value) return;
    value = v;
    onchange?.(v);
  }
  $effect(() => {
    const s = slugify(from ?? '', max);
    if (!manual && !locked && value !== s) set(s);
  });
  function unlock() {
    manual = true;
    queueMicrotask(() => el?.focus());
  }
</script>

<div class="si" class:invalid class:locked={!manual || locked} class:dis={disabled} data-testid={testid ? `${testid}-wrap` : undefined}>
  {#if fixed}<span class="lk" title="Fixed after create"><Icon name="lock" size={13} /></span>{/if}
  <input bind:this={el} {id} class="v mono" {value} readonly={!manual || locked} {disabled} {placeholder} maxlength={max} spellcheck="false" autocomplete="off" aria-invalid={invalid ? 'true' : undefined} data-testid={testid} oninput={(e) => set(e.currentTarget.value.toLowerCase())} />
  {#if !locked}
    {#if manual}
      <button type="button" class="b" title="Derive from the label again" aria-label="Derive the slug from the label" onclick={() => (manual = false)} data-testid={testid ? `${testid}-auto` : undefined}><Icon name="refresh" size={13} /></button>
    {:else}
      <span class="auto">auto</span>
      <button type="button" class="b" title="Edit the slug" aria-label="Edit the slug" onclick={unlock} data-testid={testid ? `${testid}-edit` : undefined}><Icon name="edit" size={13} /></button>
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
    min-width: 0;
  }
  .si.locked {
    background: var(--surface-2);
  }
  .si.dis {
    cursor: not-allowed;
  }
  .si:focus-within {
    border-color: var(--accent);
    box-shadow: var(--focus-ring);
  }
  .si.invalid {
    border-color: var(--red);
  }
  .lk {
    display: inline-flex;
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
    height: 100%;
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
