<script lang="ts" module>
  export interface MsOption {
    value: string;
    label: string;
    hint?: string;
    color?: string;
    /** Icon value (IconView) */
    icon?: string | null;
    /** render an avatar (name → initials, img → Icon value) */
    avatar?: { name: string; img: string | null };
  }
</script>

<script lang="ts">
  import Icon from './Icon.svelte';
  import IconView from './IconView.svelte';
  import Avatar from './Avatar.svelte';
  import { ChevronDown, X, Check, Search } from './icons';
  import { tick } from 'svelte';
  import { portal, floating } from './portal';

  type Props = {
    options: MsOption[];
    value: string[];
    placeholder?: string;
    disabled?: boolean;
    id?: string;
    invalid?: boolean;
    onchange?: (v: string[]) => void;
    testid?: string;
    emptyText?: string;
    /** single choice: picking replaces the value and closes the list */
    single?: boolean;
  };
  let { options, value = $bindable([]), placeholder = 'Select…', disabled = false, id, invalid = false, onchange, testid, emptyText = 'Nothing to choose', single = false }: Props = $props();
  let open = $state(false);
  let q = $state('');
  let root = $state<HTMLElement | null>(null);
  let searchEl = $state<HTMLInputElement | null>(null);
  let popEl = $state<HTMLElement | null>(null);
  let active = $state(0);
  const lid = `ms-${Math.random().toString(36).slice(2, 8)}`;
  const filtered = $derived(options.filter((o) => !q || o.label.toLowerCase().includes(q.toLowerCase()) || o.value.toLowerCase().includes(q.toLowerCase())));
  const selected = $derived(value.map((v) => options.find((o) => o.value === v) ?? { value: v, label: v }));

  function toggle(v: string) {
    if (single) {
      value = value.includes(v) ? [] : [v];
      open = false;
      onchange?.(value);
      return;
    }
    value = value.includes(v) ? value.filter((x) => x !== v) : [...value, v];
    onchange?.(value);
  }
  function remove(v: string, e: Event) {
    e.stopPropagation();
    value = value.filter((x) => x !== v);
    onchange?.(value);
  }
  async function openList() {
    if (disabled) return;
    open = true;
    active = 0;
    await tick();
    searchEl?.focus();
  }
  function onkey(e: KeyboardEvent) {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      active = Math.min(active + 1, filtered.length - 1);
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      active = Math.max(active - 1, 0);
    } else if (e.key === 'Enter') {
      e.preventDefault();
      const o = filtered[active];
      if (o) toggle(o.value);
    } else if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      open = false;
    }
  }
  function onfocusout(e: FocusEvent) {
    const t = e.relatedTarget as Node | null;
    if (root && !root.contains(t) && !popEl?.contains(t)) open = false;
  }
</script>

<div class="ms" bind:this={root} {onfocusout} data-testid={testid}>
  <div
    class="ctl input"
    class:invalid
    class:disabled
    role="combobox"
    aria-expanded={open}
    aria-controls={lid}
    aria-haspopup="listbox"
    tabindex={disabled ? -1 : 0}
    {id}
    onclick={openList}
    onkeydown={(e) => (e.key === 'Enter' || e.key === ' ' || e.key === 'ArrowDown') && (e.preventDefault(), openList())}
  >
    <div class="chips">
      {#if !selected.length}<span class="ph">{placeholder}</span>{/if}
      {#each selected as s (s.value)}
        <span class="chip sel">
          {#if 'avatar' in s && s.avatar}<Avatar name={s.avatar.name} img={s.avatar.img} size={16} />{:else if 'icon' in s && s.icon !== undefined}<IconView value={s.icon} size={12} />{/if}
          {#if 'color' in s && s.color}<span class="dot" style="background:{s.color}"></span>{/if}
          {s.label}
          {#if !disabled}<button type="button" class="x" aria-label="Remove {s.label}" onclick={(e) => remove(s.value, e)}><Icon icon={X} size={11} /></button>{/if}
        </span>
      {/each}
    </div>
    <Icon icon={ChevronDown} size={14} class="chev" />
  </div>
  {#if open}
    <div class="pop" bind:this={popEl} use:portal use:floating={{ anchor: root, matchWidth: true }} {onfocusout} role="listbox" tabindex="-1" id={lid} aria-multiselectable="true" onkeydown={(e) => e.key === 'Escape' && (e.preventDefault(), e.stopPropagation(), (open = false))}>
      {#if options.length > 6}
        <div class="sr">
          <Icon icon={Search} size={13} />
          <input bind:this={searchEl} bind:value={q} placeholder="Search" onkeydown={onkey} aria-label="Search options" />
        </div>
      {:else}
        <input bind:this={searchEl} class="sr-only" onkeydown={onkey} aria-label="Options" />
      {/if}
      <div class="list">
        {#each filtered as o, i (o.value)}
          <button type="button" role="option" aria-selected={value.includes(o.value)} class="opt" class:active={i === active} onmouseenter={() => (active = i)} onclick={() => toggle(o.value)}>
            <span class="cb" class:radio={single} class:on={value.includes(o.value)}>{#if value.includes(o.value)}<Icon icon={Check} size={11} stroke={3} />{/if}</span>
            {#if o.avatar}<Avatar name={o.avatar.name} img={o.avatar.img} size={20} />{:else if o.icon !== undefined}<IconView value={o.icon} size={15} />{/if}
            {#if o.color}<span class="dot" style="background:{o.color}"></span>{/if}
            <span class="grow ellipsis">{o.label}</span>
            {#if o.hint}<span class="hint">{o.hint}</span>{/if}
          </button>
        {:else}
          <div class="none">{emptyText}</div>
        {/each}
      </div>
    </div>
  {/if}
</div>

<style>
  .ms {
    position: relative;
    min-width: 0;
  }
  .ctl {
    height: auto;
    min-height: var(--control-h);
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 3px 8px 3px 6px;
    cursor: pointer;
  }
  .ctl.disabled {
    cursor: not-allowed;
    background: var(--surface-2);
  }
  .ctl :global(.chev) {
    color: var(--text-3);
    margin-left: auto;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    min-width: 0;
    flex: 1;
  }
  .ph {
    color: var(--text-3);
    padding: 0 4px;
  }
  .chip.sel {
    background: var(--accent-soft);
    border-color: transparent;
    color: var(--accent-text);
    font-size: var(--fs-sm);
    height: 22px;
  }
  .x {
    border: 0;
    background: transparent;
    padding: 0;
    color: inherit;
    cursor: pointer;
    display: inline-flex;
    opacity: 0.7;
  }
  .x:hover {
    opacity: 1;
  }
  .pop {
    background: var(--surface);
    display: flex;
    flex-direction: column;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    box-shadow: var(--shadow-lg);
    overflow: hidden;
  }
  .sr {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 6px 10px;
    border-bottom: 1px solid var(--border);
    color: var(--text-3);
  }
  .sr input {
    border: 0;
    outline: none;
    background: transparent;
    flex: 1;
    color: var(--text);
  }
  .list {
    max-height: 240px;
    min-height: 0;
    flex: 1;
    overflow-y: auto;
    padding: 4px;
  }
  .opt {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    border: 0;
    background: transparent;
    padding: 6px 8px;
    border-radius: var(--r-sm);
    cursor: pointer;
    text-align: left;
    font-size: var(--fs-md);
  }
  .opt.active {
    background: var(--surface-hover);
  }
  .cb {
    width: 15px;
    height: 15px;
    border-radius: 4px;
    border: 1px solid var(--border-strong);
    display: grid;
    place-items: center;
    flex: none;
  }
  .cb.radio {
    border-radius: 50%;
  }
  .cb.on {
    background: var(--accent);
    border-color: var(--accent);
    color: #fff;
  }
  .hint {
    color: var(--text-3);
    font-size: var(--fs-xs);
  }
  .none {
    padding: 10px;
    color: var(--text-3);
    font-size: var(--fs-sm);
  }
</style>
