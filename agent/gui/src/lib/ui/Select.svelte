<!--
  Custom single-select dropdown (round T; port of the cloud console's GroupedSelect / Combobox look). Replaces every
  native <select> in the agent GUI.
  - Trigger = button[data-testid=testid][data-value]; option = [data-testid=`${testid}-opt-${value}`] role=option.
  - List = popover="manual" in the top layer (works inside modal <dialog>s, never clipped) — see listbox.ts.
  - Keyboard: ↓ / ↑ / Enter / Space open; ↑ ↓ Home End move; Enter picks; Esc closes; Tab leaves; type-ahead when
    not searchable; a search box when searchable (default: more than 8 options).
  - Options: icon (Icon.svelte name), hint (right, muted), badge {text, tone}, group headings, disabled.
-->
<script lang="ts">
  import { tick } from 'svelte';
  import Icon from './Icon.svelte';
  import { closePop, followList, groupOptions, matches, openPop, placeList, TONE, type SelectOption } from './listbox';

  interface Props {
    value?: string | null;
    options: SelectOption[];
    placeholder?: string;
    searchable?: boolean;
    disabled?: boolean;
    testid?: string;
    ariaLabel?: string;
    size?: 'sm' | 'md';
    onchange?: (v: string) => void;
    id?: string;
    mono?: boolean;
    invalid?: boolean;
    /** text when the value is not one of the options (e.g. "(missing)") */
    fallback?: string;
  }
  let { value = $bindable<string | null>(null), options, placeholder = 'Choose…', searchable, disabled = false, testid, ariaLabel, size = 'md', onchange, id, mono = false, invalid = false, fallback }: Props = $props();

  const uid = $props.id();
  const lid = `${uid}-lb`;
  let btn = $state<HTMLButtonElement>();
  let pop = $state<HTMLDivElement>();
  let search = $state<HTMLInputElement>();
  let optsEl = $state<HTMLDivElement>();
  let open = $state(false);
  let q = $state('');
  let active = $state(-1); // index into `visible`
  let typed = '';
  let typedAt = 0;

  const canSearch = $derived(searchable ?? options.length > 8);
  const current = $derived(value == null ? undefined : options.find((o) => o.value === value));
  const groups = $derived(groupOptions(options.filter((o) => !canSearch || matches(o, q))));
  const visible = $derived(groups.flatMap((g) => g.items.map((x) => x.o)));

  $effect(() => {
    if (!open || !pop || !btn) return;
    void visible.length;
    placeList(pop, btn);
    return followList(pop, btn);
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
    if (pop && btn) placeList(pop, btn);
    active = visible.findIndex((o) => o.value === value && !o.disabled);
    if (active < 0) active = visible.findIndex((o) => !o.disabled);
    (canSearch ? search : optsEl)?.focus();
    scrollActive();
  }
  function hide(focus = true) {
    if (!open) return;
    open = false;
    typed = '';
    closePop(pop);
    if (focus) btn?.focus();
  }
  function pick(o: SelectOption) {
    if (o.disabled) return;
    if (o.action) {
      hide();
      onchange?.(o.value);
      return;
    }
    const changed = o.value !== value;
    value = o.value;
    hide();
    if (changed) onchange?.(o.value);
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
    else n = pos < 0 ? (d === 1 ? 0 : en.length - 1) : Math.min(en.length - 1, Math.max(0, pos + d));
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
      const o = visible[i];
      if (!o.disabled && o.label.toLowerCase().startsWith(typed)) {
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
    } else if (!canSearch && e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
      // type-ahead on the closed trigger picks directly (like a native select)
      typeAheadClosed(e.key);
    }
  }
  function typeAheadClosed(ch: string) {
    const cur = visible.findIndex((o) => o.value === value);
    active = cur;
    typeAhead(ch);
    const o = visible[active];
    if (o && !o.action && o.value !== value) {
      value = o.value;
      onchange?.(o.value);
    }
  }
  function onListKey(e: KeyboardEvent) {
    if (e.key === 'ArrowDown') (e.preventDefault(), move(1));
    else if (e.key === 'ArrowUp') (e.preventDefault(), move(-1));
    else if (e.key === 'Home' && !canSearch) (e.preventDefault(), move('first'));
    else if (e.key === 'End' && !canSearch) (e.preventDefault(), move('last'));
    else if (e.key === 'PageDown') (e.preventDefault(), move('last'));
    else if (e.key === 'PageUp') (e.preventDefault(), move('first'));
    else if (e.key === 'Enter' || (e.key === ' ' && !canSearch)) {
      e.preventDefault();
      const o = visible[active];
      if (o) pick(o);
    } else if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      hide();
    } else if (e.key === 'Tab') hide(false);
    else if (!canSearch && e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) typeAhead(e.key);
  }
  function oninput() {
    active = visible.findIndex((o) => !o.disabled);
  }
</script>

<button
  bind:this={btn}
  {id}
  type="button"
  class="sel {size}"
  class:invalid
  class:ph={!current && !fallback && !(value && value !== '')}
  role="combobox"
  aria-haspopup="listbox"
  aria-expanded={open}
  aria-controls={lid}
  aria-label={ariaLabel}
  {disabled}
  onclick={() => (open ? hide() : show())}
  onkeydown={onBtnKey}
  data-testid={testid}
  data-value={value ?? ''}
>
  {#if current?.dot}<i class="sdot {current.dot}" aria-hidden="true"></i>{:else if current?.icon}<Icon name={current.icon} size={size === 'sm' ? 13 : 14} />{/if}
  <span class="cur" class:mono>{current ? current.label : value ? (fallback ?? value) : (fallback ?? placeholder)}</span>
  {#if current?.badge}<span class="badge {TONE[current.badge.tone ?? 'gray']} bdg">{current.badge.text}</span>{/if}
  <span class="chev"><Icon name="chevronDown" size={14} /></span>
</button>
<div class="lbx" bind:this={pop} popover="manual" id={lid} data-testid={testid ? `${testid}-list` : undefined}>
  {#if open}
    {#if canSearch}
      <div class="srch">
        <Icon name="search" size={13} />
        <input bind:this={search} bind:value={q} {oninput} onkeydown={onListKey} placeholder="Search…" aria-label="Search {ariaLabel ?? 'options'}" aria-controls="{lid}-opts" aria-activedescendant={active >= 0 ? `${lid}-o${active}` : undefined} data-testid={testid ? `${testid}-search` : undefined} />
      </div>
    {/if}
    <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
    <div class="opts" id="{lid}-opts" role="listbox" tabindex={canSearch ? -1 : 0} aria-label={ariaLabel} aria-activedescendant={active >= 0 ? `${lid}-o${active}` : undefined} onkeydown={onListKey} bind:this={optsEl}>
      {#each groups as g, gi (gi)}
        <div class="grp" role="group" aria-label={g.label ?? undefined}>
          {#if g.label}<div class="gl">{g.label}</div>{/if}
          {#each g.items as { o } (o.value)}
            {@const vi = visible.indexOf(o)}
            <div
              id="{lid}-o{vi}"
              role="option"
              tabindex="-1"
              aria-selected={o.value === value}
              aria-disabled={o.disabled || undefined}
              class="opt"
              class:active={vi === active}
              class:dis={o.disabled}
              class:act={o.action}
              onmousedown={(e) => e.preventDefault()}
              onmouseenter={() => !o.disabled && (active = vi)}
              onclick={() => pick(o)}
              onkeydown={() => {}}
              data-testid={testid ? `${testid}-opt-${o.value}` : undefined}
              data-value={o.value}
            >
              <span class="ck">{#if o.value === value}<Icon name="check" size={13} />{/if}</span>
              {#if o.dot}<span class="oi"><i class="sdot {o.dot}" title={o.dot === 'green' ? 'reachable' : o.dot === 'red' ? 'unreachable' : undefined}></i></span>{:else if o.icon}<span class="oi"><Icon name={o.icon} size={14} /></span>{/if}
              <span class="ol" class:mono>{o.label}</span>
              {#if o.badge}<span class="badge {TONE[o.badge.tone ?? 'gray']} bdg">{o.badge.text}</span>{/if}
              {#if o.hint}<span class="oh">{o.hint}</span>{/if}
            </div>
          {/each}
        </div>
      {:else}
        <div class="none">No matches</div>
      {/each}
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
    height: var(--control-h, 34px);
    padding: 0 8px 0 10px;
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
    height: var(--control-h-sm, 28px);
    font-size: var(--fs-sm);
    padding: 0 6px 0 9px;
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
  .sel.invalid {
    border-color: var(--red);
  }
  .cur {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .sel.ph .cur {
    color: var(--text-3);
  }
  .mono {
    font-family: var(--font-mono);
    font-size: 0.92em;
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
    flex: none;
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
    padding: 6px 8px 6px 6px;
    border-radius: var(--r-sm, 5px);
    cursor: pointer;
    font-size: var(--fs-md);
    min-height: 30px;
  }
  .opt.active {
    background: var(--surface-hover);
  }
  .opt[aria-selected='true'] .ol {
    font-weight: var(--fw-medium);
  }
  .opt.act {
    color: var(--accent-text);
    font-weight: var(--fw-medium);
  }
  .opt.dis {
    opacity: 0.45;
    cursor: not-allowed;
  }
  .ck {
    width: 17px;
    display: inline-grid;
    place-items: center;
    color: var(--accent-text);
    flex: none;
  }
  .sdot {
    display: inline-block;
    width: 8px;
    height: 8px;
    margin: 0 3px;
    border-radius: 50%;
    flex: none;
    background: var(--text-3);
  }
  .sdot.green {
    background: var(--green);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--green) 18%, transparent);
  }
  .sdot.amber {
    background: var(--amber);
  }
  .sdot.red {
    background: var(--red);
  }
  .oi {
    display: inline-flex;
    color: var(--text-2);
    flex: none;
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
    max-width: 50%;
    font-size: var(--fs-xs);
    color: var(--text-3);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .bdg {
    flex: none;
    font-size: 10.5px;
  }
  .none {
    padding: 10px;
    color: var(--text-2);
    font-size: var(--fs-sm);
  }
</style>
