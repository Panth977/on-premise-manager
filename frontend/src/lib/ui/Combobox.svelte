<script lang="ts" module>
  export interface ComboOption {
    value: string;
    /** shown text (default value) */
    label?: string;
    /** second line, muted */
    sub?: string;
    /** right side, muted (e.g. "3 days ago") */
    detail?: string;
    /** small badge (e.g. "official", "already registered") */
    badge?: string;
    /** Icon value rendered with IconView */
    icon?: string | null;
    disabled?: boolean;
  }
</script>

<script lang="ts">
  /**
   * Searchable combobox. Options are static (`options`) or async (`load(q)`, debounced, latest wins).
   * `freeText` = any typed text is a valid value (else the value must be an option; blur reverts).
   * Keyboard: ↓/↑ move, Enter picks, Esc closes, Tab leaves. States: loading, error (+ retry), empty, `message` line.
   */
  import IconView from './IconView.svelte';
  import Icon from './Icon.svelte';
  import Spinner from './Spinner.svelte';
  import { ChevronDown, X } from './icons';
  import { portal, floating } from './portal';
  import { untrack } from 'svelte';

  type Props = {
    value: string;
    options?: ComboOption[];
    /** typed = false when the list just opened (the text was not typed since) */
    load?: (q: string, typed: boolean) => Promise<ComboOption[]>;
    /** re-run load when this changes (e.g. the registry id) */
    loadKey?: unknown;
    freeText?: boolean;
    placeholder?: string;
    id?: string;
    testid?: string;
    invalid?: boolean;
    disabled?: boolean;
    size?: 'md' | 'sm';
    mono?: boolean;
    clearable?: boolean;
    emptyText?: string;
    /** info line at the bottom of the list (e.g. "listing not supported — type the name") */
    message?: string | null;
    /** filter static options by the typed text (default true) */
    filter?: boolean;
    debounceMs?: number;
    /** on open, load with the current text (not '') — for staged pickers whose text is a path */
    loadAll?: boolean;
    ariaLabel?: string;
    inputEl?: HTMLInputElement | null;
    /** return true to keep the list open and continue (staged pickers: registry → repository → tag) */
    onselect?: (o: ComboOption) => boolean | void;
    oninput?: (text: string) => void;
    onfocus?: () => void;
  };
  let {
    value = $bindable(''),
    options = [],
    load,
    loadKey,
    freeText = false,
    placeholder = '',
    id,
    testid,
    invalid = false,
    disabled = false,
    size = 'md',
    mono = false,
    clearable = false,
    emptyText = 'No matches',
    message = null,
    filter = true,
    debounceMs = 250,
    ariaLabel,
    inputEl = $bindable(null),
    onselect,
    oninput,
    loadAll = false,
    onfocus,
  }: Props = $props();

  let open = $state(false);
  let text = $state(value ?? '');
  let typed = $state(false);
  let active = $state(0);
  let root = $state<HTMLElement | null>(null);
  let popEl = $state<HTMLElement | null>(null);
  let remote = $state<ComboOption[]>([]);
  let loading = $state(false);
  let error = $state<string | null>(null);
  const lid = `cb-${Math.random().toString(36).slice(2, 8)}`;

  /** the text shown for a value: the option label when a picked (non-free-text) option has one */
  const display = (v: string) => (freeText ? v : ((options.find((o) => o.value === v) ?? remote.find((o) => o.value === v))?.label ?? v));
  $effect(() => {
    // external value change → show it
    const v = value ?? '';
    if (!open || !typed) text = display(v);
  });

  /** the typed text (raw; load() gets it as typed) */
  const q = $derived(typed ? text.trim() : '');
  const ql = $derived(q.toLowerCase());
  const source = $derived(load ? remote : options);
  const shown = $derived(!filter || load || !ql ? source : source.filter((o) => (o.label ?? o.value).toLowerCase().includes(ql) || o.value.toLowerCase().includes(ql) || (o.sub ?? '').toLowerCase().includes(ql)));

  let seq = 0;
  let timer: ReturnType<typeof setTimeout> | null = null;
  function fetchNow(query: string, isTyped = true) {
    if (!load) return;
    const my = ++seq;
    loading = true;
    error = null;
    load(query, isTyped)
      .then((r) => {
        if (my === seq) remote = r;
      })
      .catch((e) => {
        if (my === seq) {
          remote = [];
          error = e instanceof Error ? e.message : String(e);
        }
      })
      .finally(() => {
        if (my === seq) loading = false;
      });
  }
  function schedule() {
    if (!load) return;
    if (timer) clearTimeout(timer);
    timer = setTimeout(() => fetchNow(q), debounceMs);
  }
  $effect(() => {
    void loadKey;
    untrack(() => {
      remote = [];
      if (open) fetchNow(q);
    });
  });

  function openList() {
    if (disabled || open) return;
    open = true;
    typed = false;
    active = Math.max(0, shown.findIndex((o) => o.value === value));
    if (load) fetchNow(loadAll ? text.trim() : '', false);
  }
  function close(commit = true) {
    open = false;
    if (commit && typed) {
      const t = text.trim();
      const hit = source.find((o) => o.value === t || (o.label ?? '').toLowerCase() === t.toLowerCase());
      if (hit && !hit.disabled) pick(hit);
      else if (freeText) {
        value = t;
      } else text = display(value ?? '');
    }
    typed = false;
  }
  function pick(o: ComboOption) {
    if (o.disabled) return;
    value = o.value;
    text = freeText ? o.value : (o.label ?? o.value);
    typed = false;
    open = false;
    if (onselect?.(o) === true) {
      typed = true;
      open = true;
      active = 0;
      inputEl?.focus();
      fetchNow(o.value, false);
    }
  }
  /** open the list programmatically (e.g. after a clone) */
  export function openWith(t?: string) {
    if (t !== undefined) {
      text = t;
      typed = true;
    }
    open = true;
    active = 0;
    if (load) fetchNow(t !== undefined || loadAll ? text.trim() : '', t !== undefined);
    inputEl?.focus();
  }
  function onInput(e: Event & { currentTarget: HTMLInputElement }) {
    text = e.currentTarget.value;
    typed = true;
    if (!open) {
      open = true;
    }
    active = 0;
    if (freeText) value = text.trim();
    oninput?.(text);
    schedule();
  }
  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      if (!open) return openList();
      active = Math.min(active + 1, shown.length - 1);
      scrollActive();
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      active = Math.max(active - 1, 0);
      scrollActive();
    } else if (e.key === 'Enter') {
      if (!open) return;
      e.preventDefault();
      const o = shown[active];
      if (o && !o.disabled && (!typed || shown.length)) pick(o);
      else close(true);
    } else if (e.key === 'Escape') {
      if (open) {
        e.preventDefault();
        e.stopPropagation();
        open = false;
        text = display(value ?? '');
        typed = false;
      }
    } else if (e.key === 'Tab') close(true);
  }
  function scrollActive() {
    queueMicrotask(() => popEl?.querySelector('.opt.active')?.scrollIntoView({ block: 'nearest' }));
  }
  function onfocusout(e: FocusEvent) {
    const t = e.relatedTarget as Node | null;
    if (root?.contains(t) || popEl?.contains(t)) return;
    if (open) close(true);
  }
  function clear() {
    value = '';
    text = '';
    inputEl?.focus();
    oninput?.('');
  }
</script>

<div class="cb {size}" class:invalid class:disabled bind:this={root} {onfocusout}>
  <input
    bind:this={inputEl}
    {id}
    class="in"
    class:mono
    role="combobox"
    aria-expanded={open}
    aria-controls={lid}
    aria-autocomplete="list"
    aria-label={ariaLabel}
    autocomplete="off"
    spellcheck="false"
    value={text}
    {placeholder}
    {disabled}
    oninput={onInput}
    {onkeydown}
    onfocus={() => onfocus?.()}
    onclick={openList}
    data-testid={testid}
  />
  {#if loading && open}<span class="sp"><Spinner size={13} /></span>{/if}
  {#if clearable && value && !disabled}<button type="button" class="x" tabindex="-1" aria-label="Clear" onclick={clear}><Icon icon={X} size={13} /></button>{/if}
  <button type="button" class="chev" tabindex="-1" aria-label="Show options" {disabled} onclick={() => (open ? close(false) : (inputEl?.focus(), openList()))}><Icon icon={ChevronDown} size={14} /></button>
</div>
{#if open}
  <div class="pop" bind:this={popEl} use:portal use:floating={{ anchor: root, matchWidth: true, minWidth: 260 }} id={lid} role="listbox" tabindex="-1" {onfocusout} data-testid={testid ? `${testid}-list` : undefined}>
    {#if loading && !shown.length}
      <div class="st"><Spinner size={14} /> Loading…</div>
    {:else if error}
      <div class="st err">{error} <button type="button" class="link" onclick={() => fetchNow(q)}>Retry</button></div>
    {/if}
    {#each shown as o, i (`${i}:${o.value}`)}
      <button
        type="button"
        role="option"
        aria-selected={o.value === value}
        aria-disabled={o.disabled || undefined}
        class="opt"
        class:active={i === active}
        class:sel={o.value === value}
        class:dis={o.disabled}
        tabindex="-1"
        onmousedown={(e) => e.preventDefault()}
        onmouseenter={() => (active = i)}
        onclick={() => pick(o)}
        data-testid="combo-option"
        data-value={o.value}
      >
        {#if o.icon !== undefined}<IconView value={o.icon} size={16} />{/if}
        <span class="txt">
          <span class="lb" class:mono>{o.label ?? o.value}</span>
          {#if o.sub}<span class="sub">{o.sub}</span>{/if}
        </span>
        {#if o.badge}<span class="badge outline bdg">{o.badge}</span>{/if}
        {#if o.detail}<span class="det">{o.detail}</span>{/if}
      </button>
    {/each}
    {#if !loading && !error && !shown.length}
      <div class="st">{freeText && text.trim() ? `Use “${text.trim()}”` : emptyText}</div>
    {/if}
    {#if message}<div class="msg">{message}</div>{/if}
  </div>
{/if}

<style>
  .cb {
    position: relative;
    display: flex;
    align-items: center;
    height: var(--control-h);
    border: 1px solid var(--border-strong);
    border-radius: var(--r-md);
    background: var(--surface);
    min-width: 0;
  }
  .cb.sm {
    height: var(--control-h-sm);
  }
  .cb:focus-within {
    border-color: var(--accent);
    box-shadow: var(--focus-ring);
  }
  .cb.invalid {
    border-color: var(--red);
  }
  .cb.disabled {
    background: var(--surface-2);
  }
  .in {
    flex: 1;
    min-width: 0;
    height: 100%;
    border: 0;
    outline: none;
    background: transparent;
    padding: 0 4px 0 10px;
    color: var(--text);
    font-size: var(--fs-md);
  }
  .sm .in {
    font-size: var(--fs-sm);
  }
  .in:focus {
    box-shadow: none;
  }
  .mono {
    font-family: var(--font-mono);
  }
  .sp {
    display: inline-flex;
    color: var(--text-3);
    padding: 0 4px;
  }
  .chev,
  .x {
    border: 0;
    background: transparent;
    color: var(--text-3);
    height: 100%;
    width: 28px;
    display: grid;
    place-items: center;
    cursor: pointer;
    flex: none;
  }
  .chev:hover,
  .x:hover {
    color: var(--text);
  }
  .pop {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    box-shadow: var(--shadow-lg);
    overflow-y: auto;
    padding: 4px;
    max-height: 320px;
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
    color: var(--text);
    font-size: var(--fs-md);
  }
  .opt.active {
    background: var(--surface-hover);
  }
  .opt.sel .lb {
    color: var(--accent-text);
    font-weight: var(--fw-medium);
  }
  .opt.dis {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .txt {
    display: flex;
    flex-direction: column;
    min-width: 0;
    flex: 1;
  }
  .lb {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .lb.mono {
    font-size: var(--fs-sm);
  }
  .sub {
    font-size: var(--fs-xs);
    color: var(--text-3);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .det {
    font-size: var(--fs-xs);
    color: var(--text-3);
    white-space: nowrap;
  }
  .bdg {
    font-size: 10.5px;
  }
  .st {
    display: flex;
    gap: 8px;
    align-items: center;
    padding: 8px 10px;
    color: var(--text-3);
    font-size: var(--fs-sm);
  }
  .st.err {
    color: var(--red-text);
  }
  .link {
    border: 0;
    background: transparent;
    color: var(--accent-text);
    cursor: pointer;
    padding: 0;
    text-decoration: underline;
  }
  .msg {
    border-top: 1px solid var(--border);
    margin-top: 4px;
    padding: 7px 10px 4px;
    color: var(--amber-text);
    font-size: var(--fs-xs);
  }
</style>
