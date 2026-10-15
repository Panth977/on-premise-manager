<script lang="ts" module>
  export interface ComboOption {
    value: string;
    label?: string;
    /** right-aligned muted text (description, current value, …) */
    hint?: string;
    group?: string;
    disabled?: boolean;
  }
</script>

<script lang="ts">
  /**
   * Searchable picker (ARIA combobox + listbox). allowCustom = the typed text is the value (suggestions only,
   * e.g. env names, filters); otherwise the value must be one of the options (typing only searches).
   */
  import { tick } from 'svelte';
  import Icon from './Icon.svelte';
  import { follow, hidePop, placeBelow, showPop } from './popover';
  interface Props {
    value?: string;
    onchange?: (v: string) => void;
    ariaLabel?: string;
    options: ComboOption[];
    allowCustom?: boolean;
    placeholder?: string;
    id?: string;
    label?: string;
    mono?: boolean;
    size?: 'sm' | 'md';
    invalid?: boolean;
    disabled?: boolean;
    testid?: string;
    emptyText?: string;
    width?: string;
  }
  let { value = $bindable(''), onchange, options, allowCustom = false, placeholder, id, label, ariaLabel, mono = false, size = 'md', invalid = false, disabled = false, testid, emptyText = 'No matches', width }: Props = $props();
  const uid = $props.id();
  const listId = `${uid}-lb`;
  let input: HTMLInputElement | undefined = $state();
  let pop: HTMLDivElement | undefined = $state();
  let open = $state(false);
  let query = $state<string | null>(null); // null = not searching (show the value)
  let active = $state(0);

  const selected = $derived(options.find((o) => o.value === value));
  const display = $derived(query ?? (allowCustom ? value : (selected?.label ?? value)));
  const filtered = $derived.by(() => {
    const q = (query ?? '').trim().toLowerCase();
    if (!q || (!allowCustom && query === (selected?.label ?? value))) return options;
    return options.filter((o) => o.value.toLowerCase().includes(q) || (o.label ?? '').toLowerCase().includes(q) || (o.hint ?? '').toLowerCase().includes(q));
  });

  $effect(() => {
    if (!open || !pop || !input) return;
    void filtered.length;
    placeBelow(pop, input);
    return follow(pop, input);
  });

  async function openList() {
    if (disabled || open) return;
    open = true;
    const i = filtered.findIndex((o) => o.value === value);
    active = i >= 0 ? i : 0;
    await tick();
    showPop(pop);
    if (pop && input) placeBelow(pop, input);
    scrollActive();
  }
  function set(v: string) {
    value = v;
    onchange?.(v);
  }
  function closeList() {
    open = false;
    hidePop(pop);
  }
  function pick(o: ComboOption) {
    if (o.disabled) return;
    query = null;
    set(o.value);
    closeList();
  }
  function scrollActive() {
    pop?.querySelector(`#${CSS.escape(`${uid}-o${active}`)}`)?.scrollIntoView({ block: 'nearest' });
  }
  function move(d: number) {
    if (!filtered.length) return;
    let i = active;
    for (let k = 0; k < filtered.length; k++) {
      i = (i + d + filtered.length) % filtered.length;
      if (!filtered[i].disabled) break;
    }
    active = i;
    tick().then(scrollActive);
  }
  function onkey(e: KeyboardEvent) {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      if (!open) openList();
      else move(1);
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      if (!open) openList();
      else move(-1);
    } else if (e.key === 'Enter') {
      if (open && filtered[active]) {
        e.preventDefault();
        pick(filtered[active]);
      }
    } else if (e.key === 'Escape') {
      if (open) {
        e.preventDefault();
        e.stopPropagation();
        query = null;
        closeList();
      }
    } else if (e.key === 'Tab') closeList();
  }
  function oninput(e: Event & { currentTarget: HTMLInputElement }) {
    query = e.currentTarget.value;
    active = 0;
    if (allowCustom) set(query);
    openList();
  }
  function onblur() {
    // a click on an option uses mousedown + preventDefault, so blur here means "left the field"
    if (allowCustom && query !== null) set(query);
    query = null;
    closeList();
  }
</script>

<div class="cb {size}" class:disabled style:width>
  <input
    bind:this={input}
    {id}
    class="input {size === 'sm' ? 'sm' : ''}"
    class:mono
    role="combobox"
    aria-expanded={open}
    aria-controls={listId}
    aria-autocomplete="list"
    aria-activedescendant={open && filtered[active] ? `${uid}-o${active}` : undefined}
    aria-label={ariaLabel ?? label}
    aria-invalid={invalid ? 'true' : undefined}
    autocomplete="off"
    spellcheck="false"
    {placeholder}
    {disabled}
    value={display}
    data-testid={testid}
    onfocus={(e) => e.currentTarget.select()}
    onclick={openList}
    {oninput}
    onkeydown={onkey}
    {onblur}
  />
  <button type="button" class="chev" tabindex="-1" aria-hidden="true" {disabled} onmousedown={(e) => (e.preventDefault(), input?.focus(), open ? closeList() : openList())}><Icon name="chevronDown" size={14} /></button>
</div>
<div bind:this={pop} popover="manual" id={listId} role="listbox" class="lb" data-testid={testid && open ? `${testid}-list` : undefined} aria-label={(ariaLabel ?? label) ? `${ariaLabel ?? label} options` : undefined}>
  {#if open}
    {#each filtered as o, i (i)}
      {#if o.group && (i === 0 || filtered[i - 1].group !== o.group)}<div class="grp" role="presentation">{o.group}</div>{/if}
      <div id="{uid}-o{i}" data-testid="combo-option" data-value={o.value} role="option" class="opt" class:active={i === active} aria-selected={o.value === value} aria-disabled={o.disabled || undefined} tabindex="-1" onmousedown={(e) => (e.preventDefault(), pick(o))} onmousemove={() => (active = i)}>
        <span class="v" class:mono>{o.label ?? o.value}</span>{#if o.hint}<span class="h">{o.hint}</span>{/if}
      </div>
    {:else}
      <div class="none">{allowCustom && query ? `"${query}" (custom value)` : emptyText}</div>
    {/each}
  {/if}
</div>

<style>
  .cb {
    position: relative;
    min-width: 0;
    width: 100%;
  }
  .cb .input {
    padding-right: 28px;
    text-overflow: ellipsis;
  }
  .cb .input.mono {
    font-family: var(--font-mono);
    font-size: 12.5px;
  }
  .chev {
    position: absolute;
    right: 4px;
    top: 50%;
    transform: translateY(-50%);
    border: 0;
    background: none;
    color: var(--text-2);
    display: grid;
    place-items: center;
    width: 22px;
    height: 22px;
    border-radius: 4px;
    cursor: pointer;
    padding: 0;
  }
  .chev:hover:not(:disabled) {
    background: var(--gray-soft);
  }
  .disabled .chev {
    cursor: not-allowed;
  }
</style>
