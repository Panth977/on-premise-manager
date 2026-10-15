<script lang="ts">
  /**
   * Value input that understands {{ … }} expressions: autocomplete of the paths the field may use (typing "{{"
   * or Ctrl+Space inside a block), live resolved preview (secrets masked) or the error code at its position.
   * `resolved` overrides the local tryResolve (container fields resolved together by resolveContainerConfig:
   * container.env.<NAME>, E_CYCLE).
   */
  import { tick } from 'svelte';
  import { isExpr, tryResolve, checkExpr, type ExprContext, type ExprError, type ExprField, type ResolveResult } from '@opm/shared/expr';
  import { exprPaths, suggestFor, tokenAt, type Suggestion, type Token } from './exprSuggest';
  import { follow, hidePop, placeBelow, showPop } from '../ui/popover';
  import Deferred from '../ui/Deferred.svelte';
  interface Props {
    value: string | null;
    onchange: (v: string) => void;
    ctx: ExprContext | null;
    field: ExprField;
    id?: string;
    placeholder?: string;
    invalid?: boolean;
    disabled?: boolean;
    size?: 'sm' | 'md';
    /** env names of this container (image env + container env) for container.env.<NAME> */
    envNames?: string[];
    resolved?: { ok: true; result: ResolveResult } | { ok: false; error: ExprError } | null;
    label?: string;
    testid?: string;
    /** show the preview also for plain values (e.g. mount paths) */
    alwaysPreview?: boolean;
  }
  let { value, onchange, ctx, field, id, placeholder, invalid = false, disabled = false, size = 'sm', envNames = [], resolved, label, testid, alwaysPreview = false }: Props = $props();
  const uid = $props.id();
  const v = $derived(value ?? '');
  const res = $derived.by(() => {
    if (!isExpr(v)) return null;
    const syn = checkExpr(v, field);
    if (syn.length) return { ok: false as const, msg: syn[0].message, code: syn[0].code, pos: syn[0].pos };
    if (resolved) return resolved.ok ? { ok: true as const, text: resolved.result.masked, secret: resolved.result.secret } : { ok: false as const, msg: resolved.error.message, code: resolved.error.code, pos: resolved.error.pos };
    if (!ctx) return null;
    const r = tryResolve(v, ctx, field);
    return r.ok ? { ok: true as const, text: r.result.masked, secret: r.result.secret } : { ok: false as const, msg: r.error.message, code: r.error.code, pos: r.error.pos };
  });

  // ── autocomplete
  let input: HTMLInputElement | undefined = $state();
  let pop: HTMLDivElement | undefined = $state();
  let tok = $state<Token | null>(null);
  let active = $state(0);
  const paths = $derived(exprPaths(field, ctx, envNames));
  const items = $derived<Suggestion[]>(tok ? suggestFor(tok, paths) : []);
  const open = $derived(!!tok && items.length > 0);
  $effect(() => {
    if (!pop) return;
    if (open && input) {
      showPop(pop);
      placeBelow(pop, input);
      return follow(pop, input);
    }
    hidePop(pop);
  });
  function refresh(force = false) {
    if (!input) return;
    const caret = input.selectionStart ?? v.length;
    const t = tokenAt(input.value, caret);
    if (!t) return close();
    if (!force && tok === null && t.text === '' && !input.value.slice(0, caret).trimEnd().endsWith('{{') && t.kind === 'path') return close();
    tok = t;
    active = 0;
  }
  function close() {
    tok = null;
  }
  async function pick(s: Suggestion) {
    if (!input || !tok) return;
    const before = input.value.slice(0, tok.from);
    const after = input.value.slice(tok.to);
    const lead = before.endsWith('{{') ? ' ' : before.endsWith('|') ? ' ' : '';
    const tail = tok.unclosed ? ' }}' : '';
    const next = before + lead + s.insert + tail + after;
    const caret = (before + lead + s.insert).length;
    close();
    onchange(next);
    await tick();
    input.value = next;
    input.focus();
    input.setSelectionRange(caret, caret);
  }
  function key(e: KeyboardEvent) {
    if (e.key === ' ' && e.ctrlKey) {
      e.preventDefault();
      refresh(true);
      return;
    }
    if (!open) return;
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      active = (active + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length;
      tick().then(() => pop?.querySelector('.opt.active')?.scrollIntoView({ block: 'nearest' }));
    } else if (e.key === 'Enter' || e.key === 'Tab') {
      e.preventDefault();
      pick(items[active]);
    } else if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      close();
    }
  }
</script>

<div class="vi">
  <input
    bind:this={input}
    {id}
    class="input mono {size === 'sm' ? 'sm' : ''}"
    value={v}
    {placeholder}
    {disabled}
    role="combobox"
    aria-expanded={open}
    aria-controls="{uid}-ac"
    aria-autocomplete="list"
    aria-activedescendant={open ? `${uid}-s${active}` : undefined}
    aria-label={label}
    aria-invalid={invalid || (res && !res.ok) ? 'true' : undefined}
    autocomplete="off"
    data-testid={testid}
    oninput={(e) => {
      onchange(e.currentTarget.value);
      refresh();
    }}
    onkeydown={key}
    onkeyup={(e) => (e.key === 'ArrowLeft' || e.key === 'ArrowRight' || e.key === 'Home' || e.key === 'End') && tok && refresh()}
    onblur={close}
    spellcheck="false"
  />
  <div bind:this={pop} popover="manual" id="{uid}-ac" role="listbox" class="lb" aria-label="Expression suggestions" data-testid="expr-suggestions">
    {#if open}
      {#each items as s, i (s.insert)}
        {#if i === 0 || items[i - 1].group !== s.group}<div class="grp" role="presentation">{s.group}</div>{/if}
        <div id="{uid}-s{i}" role="option" tabindex="-1" class="opt" class:active={i === active} aria-selected={i === active} onmousedown={(e) => (e.preventDefault(), pick(s))} onmousemove={() => (active = i)}>
          <span class="v mono">{s.insert}</span>{#if s.hint}<span class="h">{s.hint}</span>{/if}
        </div>
      {/each}
    {/if}
  </div>
</div>
{#if res}
  {#if res.ok}
    <div class="pv ok" title="Resolved on this site with the current values" data-testid="expr-preview">→ <code>{#if res.text === ''}""{:else}<Deferred text={res.text} />{/if}</code>{#if res.secret}<span class="badge warn" style="margin-left:4px">secret</span>{/if}</div>
  {:else}
    <div class="pv err" role="alert" data-testid="expr-error"><span class="badge bad">{res.code}</span> {res.msg}{#if res.pos >= 0 && res.pos <= v.length}<div class="caret mono"><span>{v.slice(0, res.pos)}</span><mark>{v.slice(res.pos, res.pos + 1) || ' '}</mark><span>{v.slice(res.pos + 1)}</span></div>{/if}</div>
  {/if}
{:else if alwaysPreview && v && resolved?.ok}
  <div class="pv ok" data-testid="expr-preview">→ <code>{resolved.result.value}</code></div>
{/if}

<style>
  .vi {
    min-width: 0;
  }
  .pv {
    font-size: 11.5px;
    margin-top: 3px;
    overflow-wrap: anywhere;
  }
  .pv.ok {
    color: var(--text-2);
  }
  .pv.ok code {
    color: var(--green-text);
    background: none;
    padding: 0;
  }
  .pv.err {
    color: var(--red-text);
  }
  .caret {
    margin-top: 2px;
    font-size: 11px;
    color: var(--text-2);
    white-space: pre-wrap;
  }
  mark {
    background: var(--red-soft);
    color: var(--red-text);
    border-bottom: 2px solid var(--red);
  }
</style>
