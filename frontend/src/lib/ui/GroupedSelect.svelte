<script lang="ts" module>
  export interface GOption {
    value: string;
    label: string;
    /** second line, muted (e.g. "shared with core-worker") */
    sub?: string;
    /** small badge on the right (e.g. "no backup") */
    badge?: string;
    mono?: boolean;
    /** an action entry (e.g. "+ New data volume…"), shown in the accent colour */
    action?: boolean;
    disabled?: boolean;
  }
  export interface GGroup {
    label?: string;
    options: GOption[];
  }
</script>

<script lang="ts">
  /**
   * A select with option groups (listbox, not a segmented control). The list is portaled to <body> and floats at
   * --z-popover (above every Modal / Drawer: those are portaled divs at --z-overlay + 10 per level, not <dialog>s).
   * Keyboard: Enter / Space / ↓ / ↑ open; ↑ ↓ Home End PageUp PageDown move; Enter / Space pick; Esc closes;
   * Tab leaves; type-ahead (letters) moves to the next matching label when open and picks it directly when closed,
   * like a native <select>.
   * Test ids: trigger = `testid` (+ data-value), list = `${testid}-list`, option = `${testid}-opt-${value}`
   * (without a testid: `gs-option`); every option also carries data-value.
   */
  import { tick } from 'svelte';
  import Icon from './Icon.svelte';
  import { ChevronDown, Check } from './icons';
  import { portal, floating } from './portal';

  type Props = {
    groups: GGroup[];
    value: string | null;
    placeholder?: string;
    /** text shown for the current value when it is not an option (e.g. an undeclared volume) */
    fallback?: string | null;
    invalid?: boolean;
    warn?: boolean;
    disabled?: boolean;
    size?: 'sm' | 'md';
    testid?: string;
    ariaLabel?: string;
    /** id of the trigger button (so a <label for> focuses / opens it) */
    id?: string;
    /** minimum list width in px (capped by the viewport) */
    minWidth?: number;
    onselect: (value: string) => void;
  };
  let { groups, value, placeholder = 'Choose…', fallback = null, invalid = false, warn = false, disabled = false, size = 'sm', testid, ariaLabel, id, minWidth = 280, onselect }: Props = $props();

  let open = $state(false);
  let active = $state(-1);
  let btn = $state<HTMLButtonElement | null>(null);
  let list = $state<HTMLElement | null>(null);
  let typed = '';
  let typedAt = 0;
  const lid = `gs-${Math.random().toString(36).slice(2, 8)}`;
  const flat = $derived(groups.flatMap((g) => g.options));
  const enabled = $derived(flat.map((o, i) => ({ o, i })).filter((x) => !x.o.disabled));
  const current = $derived(value === null ? null : (flat.find((o) => o.value === value) ?? null));

  async function show() {
    if (disabled) return;
    open = true;
    typed = '';
    active = Math.max(0, flat.findIndex((o) => o.value === value && !o.action));
    if (flat[active]?.disabled) active = enabled[0]?.i ?? -1;
    await tick();
    list?.focus();
    scroll();
  }
  function hide(focus = true) {
    open = false;
    if (focus) btn?.focus();
  }
  function pick(o: GOption) {
    if (o.disabled) return;
    hide();
    onselect(o.value);
  }
  function move(d: 1 | -1 | 'first' | 'last') {
    if (!enabled.length) return;
    const pos = enabled.findIndex((x) => x.i === active);
    let n: number;
    if (d === 'first') n = 0;
    else if (d === 'last') n = enabled.length - 1;
    else n = pos < 0 ? (d === 1 ? 0 : enabled.length - 1) : Math.min(enabled.length - 1, Math.max(0, pos + d));
    active = enabled[n]!.i;
    scroll();
  }
  function scroll() {
    queueMicrotask(() => list?.querySelector('.opt.active')?.scrollIntoView({ block: 'nearest' }));
  }
  const printable = (e: KeyboardEvent) => e.key.length === 1 && e.key !== ' ' && !e.ctrlKey && !e.metaKey && !e.altKey;
  /** index of the next enabled, non-action option whose label starts with the typed prefix (from `from`) */
  function typeAhead(ch: string, from: number): number {
    const now = Date.now();
    typed = now - typedAt < 700 ? typed + ch.toLowerCase() : ch.toLowerCase();
    typedAt = now;
    if (!flat.length) return -1;
    // a fresh single letter cycles through the matches; a longer prefix stays on the current one if it still matches
    const start = typed.length === 1 ? from + 1 : Math.max(from, 0);
    for (let k = 0; k < flat.length; k++) {
      const i = (((start + k) % flat.length) + flat.length) % flat.length;
      const o = flat[i]!;
      if (!o.disabled && !o.action && o.label.toLowerCase().startsWith(typed)) return i;
    }
    return -1;
  }
  function onBtnKey(e: KeyboardEvent) {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp' || e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      show();
    } else if (printable(e)) {
      // closed type-ahead picks directly, like a native select
      e.preventDefault();
      const i = typeAhead(e.key, flat.findIndex((o) => o.value === value));
      const o = i >= 0 ? flat[i] : undefined;
      if (o && o.value !== value) onselect(o.value);
    }
  }
  function onListKey(e: KeyboardEvent) {
    if (e.key === 'ArrowDown') (e.preventDefault(), move(1));
    else if (e.key === 'ArrowUp') (e.preventDefault(), move(-1));
    else if (e.key === 'Home' || e.key === 'PageUp') (e.preventDefault(), move('first'));
    else if (e.key === 'End' || e.key === 'PageDown') (e.preventDefault(), move('last'));
    else if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      const o = flat[active];
      if (o) pick(o);
    } else if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      hide();
    } else if (e.key === 'Tab') hide(false);
    else if (printable(e)) {
      e.preventDefault();
      const i = typeAhead(e.key, active);
      if (i >= 0) {
        active = i;
        scroll();
      }
    }
  }
  function onfocusout(e: FocusEvent) {
    const t = e.relatedTarget as Node | null;
    if (!list?.contains(t) && t !== btn) open = false;
  }
  /** groups with the flat index of every option (keyboard + aria-activedescendant) */
  const indexed = $derived.by(() => {
    let n = 0;
    return groups.map((g) => ({ label: g.label, options: g.options.map((o) => ({ o, i: n++ })) }));
  });
</script>

<button
  bind:this={btn}
  {id}
  type="button"
  class="gs {size}"
  class:invalid
  class:warn
  class:ph={!current && !fallback}
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
  <span class="cur">
    {#if current}
      <span class="lb" class:mono={current.mono}>{current.label}</span>
    {:else if fallback}
      <span class="lb mono">{fallback}</span>
    {:else}
      <span class="lb">{placeholder}</span>
    {/if}
  </span>
  <Icon icon={ChevronDown} size={14} class="chev" />
</button>
{#if open}
  <div class="pop" bind:this={list} id={lid} role="listbox" tabindex="-1" aria-label={ariaLabel} aria-activedescendant={active >= 0 ? `${lid}-${active}` : undefined} onkeydown={onListKey} {onfocusout} use:portal use:floating={{ anchor: btn, matchWidth: true, minWidth }} data-testid={testid ? `${testid}-list` : undefined}>
    {#each indexed as g, gi (gi)}
      {#if g.options.length}
        <div class="grp" role="group" aria-label={g.label}>
          {#if g.label}<div class="gl">{g.label}</div>{/if}
          {#each g.options as { o, i } (o.value)}
            <div
              id="{lid}-{i}"
              role="option"
              tabindex="-1"
              aria-selected={o.value === value}
              aria-disabled={o.disabled || undefined}
              class="opt"
              class:active={i === active}
              class:act={o.action}
              class:dis={o.disabled}
              onmousedown={(e) => e.preventDefault()}
              onmouseenter={() => !o.disabled && (active = i)}
              onclick={() => pick(o)}
              onkeydown={() => {}}
              data-testid={testid ? `${testid}-opt-${o.value}` : 'gs-option'}
              data-value={o.value}
            >
              <span class="ck">{#if o.value === value && !o.action}<Icon icon={Check} size={13} />{/if}</span>
              <span class="txt">
                <span class="lb" class:mono={o.mono}>{o.label}</span>
                {#if o.sub}<span class="sb">{o.sub}</span>{/if}
              </span>
              {#if o.badge}<span class="badge outline bdg">{o.badge}</span>{/if}
            </div>
          {/each}
        </div>
      {/if}
    {/each}
  </div>
{/if}

<style>
  .gs {
    display: flex;
    align-items: center;
    gap: 6px;
    width: 100%;
    min-width: 0;
    min-height: var(--control-h);
    padding: 3px 6px 3px 10px;
    border: 1px solid var(--border-strong);
    border-radius: var(--r-md);
    background: var(--surface);
    color: var(--text);
    cursor: pointer;
    text-align: left;
    font-size: var(--fs-md);
  }
  .gs.sm {
    min-height: var(--control-h-sm);
    font-size: var(--fs-sm);
    padding: 2px 6px 2px 9px;
  }
  .gs:hover:not(:disabled) {
    border-color: var(--text-3);
  }
  .gs:focus-visible {
    outline: none;
    border-color: var(--accent);
    box-shadow: var(--focus-ring);
  }
  .gs:disabled {
    background: var(--surface-2);
    cursor: not-allowed;
    opacity: 0.8;
  }
  .gs.invalid {
    border-color: var(--red);
  }
  .gs.warn {
    border-color: var(--amber);
    border-style: dashed;
  }
  .gs.ph .lb {
    color: var(--text-3);
  }
  .gs.warn.ph .lb {
    color: var(--amber-text);
  }
  .cur {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: baseline;
    gap: 8px;
  }
  .lb {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .sb {
    font-size: 11.5px;
    color: var(--text-3);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .mono {
    font-family: var(--font-mono);
    font-size: 12.5px;
  }
  .gs :global(.chev) {
    color: var(--text-3);
    flex: none;
  }
  .pop {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    box-shadow: var(--shadow-lg);
    overflow-y: auto;
    padding: 4px;
    max-height: 360px;
    outline: none;
  }
  .grp + .grp {
    border-top: 1px solid var(--border);
    margin-top: 4px;
    padding-top: 4px;
  }
  .gl {
    font-size: 10.5px;
    font-weight: 600;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--text-3);
    padding: 6px 8px 3px 30px;
  }
  .opt {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 5px 8px 5px 6px;
    border-radius: var(--r-sm);
    cursor: pointer;
    color: var(--text);
    font-size: var(--fs-md);
  }
  .opt.active {
    background: var(--surface-hover);
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
    width: 18px;
    display: inline-grid;
    place-items: center;
    color: var(--accent-text);
    flex: none;
  }
  .txt {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .bdg {
    flex: none;
    font-size: 10.5px;
  }
</style>
