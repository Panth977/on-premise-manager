<!--
  Custom multi-select dropdown (round T; port of the cloud console's MultiSelect). Same option shape, list and
  keyboard as Select.svelte; Enter / Space / click toggle an option and the list stays open; the trigger shows up to
  two chips + "+N" (or "All" / the placeholder). Option testids `${testid}-opt-${value}`; footer "Select all" / "Clear".
-->
<script lang="ts">
  import { tick } from 'svelte';
  import Icon from './Icon.svelte';
  import { closePop, followList, groupOptions, matches, openPop, placeList, TONE, type SelectOption } from './listbox';

  interface Props {
    values?: string[];
    options: SelectOption[];
    placeholder?: string;
    searchable?: boolean;
    disabled?: boolean;
    testid?: string;
    ariaLabel?: string;
    size?: 'sm' | 'md';
    onchange?: (v: string[]) => void;
    id?: string;
    /** trigger text when every option is selected (default "All") */
    allLabel?: string;
  }
  let { values = $bindable<string[]>([]), options, placeholder = 'Choose…', searchable, disabled = false, testid, ariaLabel, size = 'md', onchange, id, allLabel = 'All' }: Props = $props();

  const uid = $props.id();
  const lid = `${uid}-lb`;
  let btn = $state<HTMLButtonElement>();
  let pop = $state<HTMLDivElement>();
  let search = $state<HTMLInputElement>();
  let optsEl = $state<HTMLDivElement>();
  let open = $state(false);
  let q = $state('');
  let active = $state(-1);
  let typed = '';
  let typedAt = 0;

  const canSearch = $derived(searchable ?? options.length > 8);
  const chosen = $derived(options.filter((o) => values.includes(o.value)));
  const allOn = $derived(options.length > 0 && options.filter((o) => !o.disabled).every((o) => values.includes(o.value)));
  const groups = $derived(groupOptions(options.filter((o) => !canSearch || matches(o, q))));
  const visible = $derived(groups.flatMap((g) => g.items.map((x) => x.o)));

  $effect(() => {
    if (!open || !pop || !btn) return;
    void visible.length;
    placeList(pop, btn, 220);
    return followList(pop, btn, 220);
  });
  $effect(() => {
    if (!open) return;
    const out = (e: PointerEvent) => {
      const t = e.target as Node;
      if (!btn?.contains(t) && !pop?.contains(t)) hide(false);
    };
    document.addEventListener('pointerdown', out, true);
    return () => document.removeEventListener('pointerdown', out, true);
  });

  async function show() {
    if (disabled || open) return;
    q = '';
    typed = '';
    open = true;
    await tick();
    openPop(pop);
    if (pop && btn) placeList(pop, btn, 220);
    active = visible.findIndex((o) => !o.disabled);
    (canSearch ? search : optsEl)?.focus();
  }
  function hide(focus = true) {
    if (!open) return;
    open = false;
    typed = '';
    closePop(pop);
    if (focus) btn?.focus();
  }
  function set(next: string[]) {
    // keep the option order
    values = options.map((o) => o.value).filter((v) => next.includes(v));
    onchange?.(values);
  }
  function toggle(o: SelectOption) {
    if (o.disabled) return;
    set(values.includes(o.value) ? values.filter((v) => v !== o.value) : [...values, o.value]);
  }
  function scrollActive() {
    tick().then(() => pop?.querySelector('.opt.active')?.scrollIntoView({ block: 'nearest' }));
  }
  function move(d: 1 | -1 | 'first' | 'last') {
    const en = visible.map((o, i) => ({ o, i })).filter((x) => !x.o.disabled);
    if (!en.length) return;
    const pos = en.findIndex((x) => x.i === active);
    let n: number;
    if (d === 'first') n = 0;
    else if (d === 'last') n = en.length - 1;
    else n = pos < 0 ? 0 : Math.min(en.length - 1, Math.max(0, pos + d));
    active = en[n].i;
    scrollActive();
  }
  function typeAhead(ch: string) {
    const now = Date.now();
    typed = now - typedAt < 700 ? typed + ch.toLowerCase() : ch.toLowerCase();
    typedAt = now;
    const start = typed.length === 1 ? active + 1 : Math.max(active, 0);
    for (let k = 0; k < visible.length; k++) {
      const i = (start + k) % visible.length;
      if (!visible[i].disabled && visible[i].label.toLowerCase().startsWith(typed)) {
        active = i;
        scrollActive();
        return;
      }
    }
  }
  function onBtnKey(e: KeyboardEvent) {
    if (['ArrowDown', 'ArrowUp', 'Enter', ' '].includes(e.key)) {
      e.preventDefault();
      show();
    }
  }
  function onListKey(e: KeyboardEvent) {
    if (e.key === 'ArrowDown') (e.preventDefault(), move(1));
    else if (e.key === 'ArrowUp') (e.preventDefault(), move(-1));
    else if (e.key === 'Home' && !canSearch) (e.preventDefault(), move('first'));
    else if (e.key === 'End' && !canSearch) (e.preventDefault(), move('last'));
    else if (e.key === 'Enter' || (e.key === ' ' && !canSearch)) {
      e.preventDefault();
      const o = visible[active];
      if (o) toggle(o);
    } else if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      hide();
    } else if (e.key === 'Tab') hide(false);
    else if (!canSearch && e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) typeAhead(e.key);
  }
</script>

<button
  bind:this={btn}
  {id}
  type="button"
  class="sel {size}"
  class:ph={!chosen.length}
  role="combobox"
  aria-haspopup="listbox"
  aria-expanded={open}
  aria-controls={lid}
  aria-label={ariaLabel}
  {disabled}
  onclick={() => (open ? hide() : show())}
  onkeydown={onBtnKey}
  data-testid={testid}
  data-value={values.join(',')}
>
  <span class="cur">
    {#if !chosen.length}{placeholder}
    {:else if allOn && options.length > 2}<span class="chip">{allLabel}</span>
    {:else}
      {#each chosen.slice(0, 2) as c (c.value)}<span class="chip">{c.label}</span>{/each}
      {#if chosen.length > 2}<span class="more">+{chosen.length - 2}</span>{/if}
    {/if}
  </span>
  <span class="chev"><Icon name="chevronDown" size={14} /></span>
</button>
<div class="lbx" bind:this={pop} popover="manual" id={lid} data-testid={testid && open ? `${testid}-list` : undefined}>
  {#if open}
    {#if canSearch}
      <div class="srch">
        <Icon name="search" size={13} />
        <input bind:this={search} bind:value={q} oninput={() => (active = visible.findIndex((o) => !o.disabled))} onkeydown={onListKey} placeholder="Search…" aria-label="Search {ariaLabel ?? 'options'}" data-testid={testid ? `${testid}-search` : undefined} />
      </div>
    {/if}
    <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
    <div class="opts" role="listbox" aria-multiselectable="true" tabindex={canSearch ? -1 : 0} aria-label={ariaLabel} aria-activedescendant={active >= 0 ? `${lid}-o${active}` : undefined} onkeydown={onListKey} bind:this={optsEl}>
      {#each groups as g, gi (gi)}
        <div class="grp" role="group" aria-label={g.label ?? undefined}>
          {#if g.label}<div class="gl">{g.label}</div>{/if}
          {#each g.items as { o } (o.value)}
            {@const vi = visible.indexOf(o)}
            {@const on = values.includes(o.value)}
            <div
              id="{lid}-o{vi}"
              role="option"
              tabindex="-1"
              aria-selected={on}
              aria-disabled={o.disabled || undefined}
              class="opt"
              class:active={vi === active}
              class:dis={o.disabled}
              onmousedown={(e) => e.preventDefault()}
              onmouseenter={() => !o.disabled && (active = vi)}
              onclick={() => toggle(o)}
              onkeydown={() => {}}
              data-testid={testid ? `${testid}-opt-${o.value}` : undefined}
              data-value={o.value}
            >
              <span class="box" class:on>{#if on}<Icon name="check" size={11} />{/if}</span>
              {#if o.icon}<span class="oi"><Icon name={o.icon} size={14} /></span>{/if}
              <span class="ol">{o.label}</span>
              {#if o.badge}<span class="badge {TONE[o.badge.tone ?? 'gray']} bdg">{o.badge.text}</span>{/if}
              {#if o.hint}<span class="oh">{o.hint}</span>{/if}
            </div>
          {/each}
        </div>
      {:else}
        <div class="none">No matches</div>
      {/each}
    </div>
    <div class="foot">
      <button type="button" class="lnk" onmousedown={(e) => e.preventDefault()} onclick={() => set(options.filter((o) => !o.disabled).map((o) => o.value))} disabled={allOn}>Select all</button>
      <span class="cnt">{chosen.length} selected</span>
      <button type="button" class="lnk" onmousedown={(e) => e.preventDefault()} onclick={() => set([])} disabled={!chosen.length}>Clear</button>
    </div>
  {/if}
</div>

<style>
  .sel {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    width: 100%;
    min-width: 0;
    min-height: var(--control-h, 34px);
    padding: 2px 8px 2px 6px;
    border: 1px solid var(--border-strong);
    border-radius: var(--r-md);
    background: var(--surface);
    color: var(--text);
    font: inherit;
    font-size: var(--fs-md);
    text-align: left;
    cursor: pointer;
  }
  .sel.sm {
    min-height: var(--control-h-sm, 28px);
    font-size: var(--fs-sm);
  }
  .sel:hover:not(:disabled) {
    border-color: var(--text-3);
  }
  .sel:focus-visible,
  .sel[aria-expanded='true'] {
    outline: none;
    border-color: var(--accent);
    box-shadow: var(--focus-ring);
  }
  .sel:disabled {
    background: var(--surface-2);
    color: var(--text-3);
    cursor: not-allowed;
  }
  .cur {
    flex: 1;
    min-width: 0;
    display: flex;
    gap: 4px;
    align-items: center;
    overflow: hidden;
    white-space: nowrap;
  }
  .sel.ph .cur {
    color: var(--text-3);
    padding-left: 4px;
  }
  .chip {
    display: inline-flex;
    max-width: 140px;
    overflow: hidden;
    text-overflow: ellipsis;
    padding: 1px 7px;
    border-radius: var(--r-full);
    background: var(--accent-soft);
    color: var(--accent-text);
    font-size: var(--fs-xs);
    font-weight: var(--fw-medium);
  }
  .more {
    font-size: var(--fs-xs);
    color: var(--text-2);
  }
  .chev {
    display: inline-flex;
    color: var(--text-3);
    flex: none;
  }
  .lbx {
    position: fixed;
    inset: auto;
    margin: 0;
    padding: 0;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    background: var(--surface);
    color: var(--text);
    box-shadow: var(--shadow-lg);
    overflow: hidden;
    flex-direction: column;
  }
  .lbx:popover-open {
    display: flex;
  }
  .srch {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 6px 10px;
    border-bottom: 1px solid var(--border);
    color: var(--text-3);
  }
  .srch input,
  .srch input:focus,
  .srch input:focus-visible {
    border: 0;
    box-shadow: none;
    outline: none;
  }
  .srch input {
    flex: 1;
    min-width: 0;
    border: 0;
    outline: none;
    background: none;
    color: var(--text);
    font: inherit;
    font-size: var(--fs-sm);
    padding: 3px 0;
  }
  .opts {
    overflow-y: auto;
    padding: 4px;
    outline: none;
    min-height: 0;
    flex: 1;
  }
  .opts:focus-visible {
    box-shadow: none;
  }
  .grp + .grp {
    border-top: 1px solid var(--border);
    margin-top: 4px;
    padding-top: 4px;
  }
  .gl {
    font-size: 10.5px;
    font-weight: var(--fw-semibold);
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--text-3);
    padding: 6px 8px 3px 30px;
  }
  .opt {
    display: flex;
    align-items: center;
    gap: 7px;
    padding: 6px 8px;
    border-radius: var(--r-sm, 5px);
    cursor: pointer;
    font-size: var(--fs-md);
  }
  .opt.active {
    background: var(--surface-hover);
  }
  .opt.dis {
    opacity: 0.45;
    cursor: not-allowed;
  }
  .box {
    width: 15px;
    height: 15px;
    flex: none;
    display: inline-grid;
    place-items: center;
    border: 1px solid var(--border-strong);
    border-radius: 4px;
    color: #fff;
  }
  .box.on {
    background: var(--accent);
    border-color: var(--accent);
  }
  .oi {
    display: inline-flex;
    color: var(--text-2);
  }
  .ol {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .oh {
    flex: none;
    font-size: var(--fs-xs);
    color: var(--text-3);
  }
  .bdg {
    flex: none;
    font-size: 10.5px;
  }
  .foot {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 6px 10px;
    border-top: 1px solid var(--border);
    font-size: var(--fs-xs);
    flex: none;
  }
  .cnt {
    color: var(--text-3);
  }
  .lnk {
    border: 0;
    background: none;
    padding: 0;
    color: var(--accent-text);
    cursor: pointer;
    font: inherit;
  }
  .lnk:disabled {
    color: var(--text-3);
    cursor: default;
  }
  .none {
    padding: 10px;
    color: var(--text-2);
    font-size: var(--fs-sm);
  }
</style>
