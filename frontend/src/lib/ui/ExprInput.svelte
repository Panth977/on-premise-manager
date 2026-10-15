<script lang="ts" module>
  import type { ExprContext, ExprField } from '@opm/shared';
  import { CONTAINERS_ENV_FIELDS, FIELD_ROOTS, FILTERS } from '@opm/shared';

  export interface ExprSuggestion {
    label: string;
    insert: string;
    detail: string;
    kind: string;
    /** caret offset from the end of insert (e.g. inside quotes) */
    back?: number;
    /** a path prefix ("containers.postgress.") — inserting it keeps the list open on its children */
    group?: boolean;
  }

  function short(v: unknown): string {
    if (v === null || v === undefined) return 'null';
    if (typeof v === 'string') return v.length > 28 ? `"${v.slice(0, 26)}…"` : `"${v}"`;
    const s = JSON.stringify(v);
    return s.length > 30 ? s.slice(0, 28) + '…' : s;
  }

  /** Every path an expression of this field may use, from a context (values shown as detail). */
  export function exprPaths(ctx: ExprContext | null, field: ExprField): ExprSuggestion[] {
    if (!ctx) return [];
    const roots = FIELD_ROOTS[field] as readonly string[];
    const out: ExprSuggestion[] = [];
    const add = (label: string, detail: string, kind: string) => roots.includes(kind) && out.push({ label, insert: label, detail, kind });
    for (const [k, v] of Object.entries(ctx.var ?? {})) add(`var.${k}`, short(v), 'var');
    for (const [k, v] of Object.entries(ctx.secret ?? {})) add(`secret.${k}`, v === null ? 'no value' : '••• secret', 'secret');
    for (const [k, v] of Object.entries(ctx.file ?? {})) add(`file.${k}.path`, v.path ?? 'not mounted here', 'file');
    for (const [s, st] of Object.entries(ctx.site ?? {})) {
      add(`site.${s}.host`, st.host ? st.host.replace(/^<auto:([a-z0-9-]+)>$/, 'auto · $1') : '—', 'site');
      for (const [svc, ports] of Object.entries(st.port ?? {})) for (const [p, hp] of Object.entries(ports)) add(`site.${s}.port.${svc}.${p}`, hp === null ? 'no host port' : String(hp), 'site');
    }
    add('self.slug', ctx.self.slug || '—', 'self');
    add('self.host', ctx.self.host ? ctx.self.host.replace(/^<auto:([a-z0-9-]+)>$/, 'auto · $1') : '—', 'self');
    add('deployment.id', ctx.deployment.id, 'deployment');
    // §21 #88: other containers of this site — names first, then their internal ports
    const peers = Object.entries(ctx.containers ?? {});
    for (const [c, info] of peers) add(`containers.${c}.host`, info.enabled ? `${info.host} (site network)` : `${info.host} · disabled`, 'containers');
    for (const [c, info] of peers) for (const [p, n] of Object.entries(info.port)) add(`containers.${c}.port.${p}`, n === null ? 'does not resolve' : `${n} (internal)`, 'containers');
    // §21 #99: another container's FINAL env (dev-tool presets only; siteDevtoolExprContext fills containers[c].env)
    if (CONTAINERS_ENV_FIELDS.includes(field))
      for (const [c, info] of peers)
        for (const [k, v] of Object.entries(info.env ?? {})) add(`containers.${c}.env.${k}`, v.error ? `${v.error.code}` : v.secret ? '••• secret' : v.value === null ? 'no value' : short(v.value), 'containers');
    if (ctx.container) {
      add('container.slug', ctx.container.slug, 'container');
      for (const [k, v] of Object.entries(ctx.container.env ?? {})) add(`container.env.${k}`, v.secret ? '••• secret' : short(v.value), 'container');
    }
    return out;
  }
  /** with at most this many matching paths every path is listed (under its group rows); above it only the groups */
  const FLAT_MAX = 14;
  /**
   * Completion items for the path token under the caret, one level at a time (SPEC §10; §21 #99 "pick containers →
   * postgress → env → POSTGRES_USER"): the paths starting with the token are grouped by their next segment — a segment
   * with one path is that path, a segment with several is a group row ("containers.postgress." · "6 paths") that
   * inserts the prefix and keeps the list open. When few paths match (≤ FLAT_MAX) the paths of each group follow its
   * row, so short lists stay flat. A non-empty token also finds paths that CONTAIN it (e.g. "POSTGRES").
   */
  export function completePaths(paths: ExprSuggestion[], token: string): ExprSuggestion[] {
    const t = token.toLowerCase();
    const starts = paths.filter((s) => s.label.toLowerCase().startsWith(t));
    const base = token.slice(0, token.lastIndexOf('.') + 1);
    const groups = new Map<string, ExprSuggestion[]>();
    for (const s of starts) {
      const seg = s.label.slice(base.length).split('.')[0]!;
      const key = s.label.slice(0, base.length) + seg;
      const g = groups.get(key);
      if (g) g.push(s);
      else groups.set(key, [s]);
    }
    const expand = starts.length <= FLAT_MAX;
    const out: ExprSuggestion[] = [];
    for (const [key, list] of groups) {
      if (list.length === 1 && list[0]!.label === key) {
        out.push(list[0]!);
        continue;
      }
      const envs = list.every((x) => /^containers\.[^.]+\.env\./.test(x.label)) && key.endsWith('.env');
      out.push({ label: `${key}.`, insert: `${key}.`, detail: envs ? `${list.length} env var${list.length === 1 ? '' : 's'} ›` : `${list.length} path${list.length === 1 ? '' : 's'} ›`, kind: list[0]!.kind, group: true });
      if (expand) out.push(...list);
    }
    const contains = t ? paths.filter((s) => !s.label.toLowerCase().startsWith(t) && s.label.toLowerCase().includes(t)) : [];
    return [...out, ...contains].slice(0, 80);
  }
  const FILTER_SUGG: ExprSuggestion[] = FILTERS.map((f) => {
    if (f === 'default') return { label: 'default("…")', insert: 'default("")', back: 2, detail: 'fallback for null or ""', kind: 'filter' };
    if (f === 'join') return { label: 'join(",")', insert: 'join(",")', detail: 'list → text', kind: 'filter' };
    const d: Record<string, string> = { upper: 'UPPER CASE', lower: 'lower case', trim: 'strip spaces', base64: 'Base64 of UTF-8', url: 'percent-encode', int: 'to integer', json: 'canonical JSON', sha256: 'SHA-256 hex', sha512: 'SHA-512 hex', mosquitto_passwd: 'Mosquitto password-file hash ($7$); env / file / dev tool only' };
    return { label: f, insert: f, detail: d[f] ?? '', kind: 'filter' };
  });

  type Tok = { t: string; c: string };
  /** Split the value into highlighted runs (tolerant of broken input). Works on code points. */
  export function highlight(src: string, errPos: number | null): Tok[] {
    const cps = Array.from(src);
    const cls: string[] = new Array(cps.length).fill('tx');
    let i = 0;
    while (i < cps.length) {
      if (cps[i] === '{' && cps[i + 1] === '{') {
        cls[i] = cls[i + 1] = 'br';
        let j = i + 2;
        let inStr = false;
        let afterPipe = false;
        while (j < cps.length && !(!inStr && cps[j] === '}' && cps[j + 1] === '}')) {
          const ch = cps[j]!;
          if (inStr) {
            cls[j] = 'st';
            if (ch === '\\') {
              cls[j + 1] = 'st';
              j += 2;
              continue;
            }
            if (ch === '"') inStr = false;
          } else if (ch === '"') {
            inStr = true;
            cls[j] = 'st';
          } else if (ch === '|') {
            cls[j] = 'op';
            afterPipe = true;
          } else if (/[A-Za-z0-9_.\-]/.test(ch)) {
            cls[j] = afterPipe ? 'fl' : /[0-9]/.test(ch) && (j === 0 || !/[A-Za-z_.\-]/.test(cps[j - 1] ?? '')) ? 'nu' : 'pa';
          } else if (ch === '(' || ch === ')' || ch === ',') cls[j] = 'op';
          else cls[j] = 'bk';
          j++;
        }
        if (j < cps.length) cls[j] = cls[j + 1] = 'br';
        i = j + 2;
      } else i++;
    }
    const toks: Tok[] = [];
    for (let k = 0; k < cps.length; k++) {
      const c = k === errPos ? `${cls[k]} er` : cls[k]!;
      const last = toks[toks.length - 1];
      if (last && last.c === c) last.t += cps[k];
      else toks.push({ t: cps[k]!, c });
    }
    if (errPos !== null && errPos >= cps.length) toks.push({ t: ' ', c: 'er end' });
    return toks;
  }
</script>

<script lang="ts">
  import { checkExpr, isExpr, tryResolve, type Resolved, type ResolveResult } from '@opm/shared';
  import { tick } from 'svelte';
  import { portal } from './portal';
  import AutoHostText from './AutoHostText.svelte';

  type Props = {
    value: string;
    field: ExprField;
    ctx?: ExprContext | null;
    multiline?: boolean;
    placeholder?: string;
    disabled?: boolean;
    invalid?: boolean;
    id?: string;
    testid?: string;
    preview?: boolean;
    /** numeric field (hostPort / imagePort): preview says "→ 8080" */
    numeric?: boolean;
    size?: 'md' | 'sm';
    oninput?: (v: string) => void;
    ariaLabel?: string;
    /**
     * Externally resolved result shown in the preview instead of resolving `value` over `ctx` (e.g. the container
     * resolution of shared resolveContainerConfig: container.env.<NAME> in dependency order, E_CYCLE). undefined = resolve here.
     */
    resolved?: Resolved<ResolveResult> | null;
  };
  let { value = $bindable(''), field, ctx = null, multiline = false, placeholder = '', disabled = false, invalid = false, id, testid, preview = true, numeric = false, size = 'md', oninput, ariaLabel, resolved = undefined }: Props = $props();

  let el = $state<HTMLInputElement | HTMLTextAreaElement | null>(null);
  let hl = $state<HTMLElement | null>(null);
  let open = $state(false);
  let active = $state(0);
  let token = $state('');
  let mode = $state<'path' | 'filter'>('path');
  let pos = $state({ top: 0, left: 0, width: 0, up: false, maxH: 300 });
  /** below the input, or above it when there is more room there (the list never leaves the viewport) */
  function place() {
    if (!el) return;
    const r = el.getBoundingClientRect();
    const below = window.innerHeight - r.bottom - 8;
    const above = r.top - 8;
    const up = below < 220 && above > below;
    // keep the list (up to 560 px wide) inside the viewport horizontally too (drawers sit at the right edge)
    const w = Math.min(560, window.innerWidth - 16);
    const left = Math.max(8, Math.min(r.left, window.innerWidth - 8 - w));
    pos = { top: up ? r.top - 3 : r.bottom + 3, left, width: Math.min(r.width, w), up, maxH: Math.max(120, Math.min(300, up ? above : below)) };
  }

  const hasBlock = $derived(typeof value === 'string' && value.includes('{{'));
  const syntaxErr = $derived(hasBlock ? (checkExpr(value, field)[0] ?? null) : null);
  const resolution = $derived(hasBlock && !syntaxErr ? (resolved !== undefined ? resolved : ctx ? tryResolve(value, ctx, field) : null) : null);
  const toks = $derived(highlight(value ?? '', syntaxErr ? syntaxErr.pos : null));
  const paths = $derived(exprPaths(ctx, field));
  const items = $derived.by(() => {
    if (mode === 'path') return completePaths(paths, token);
    const t = token.toLowerCase();
    const starts = FILTER_SUGG.filter((s) => s.label.toLowerCase().startsWith(t));
    const contains = t ? FILTER_SUGG.filter((s) => !s.label.toLowerCase().startsWith(t) && s.label.toLowerCase().includes(t)) : [];
    return [...starts, ...contains].slice(0, 14);
  });
  /** keep the highlighted row visible while moving with the keyboard */
  let acEl = $state<HTMLElement | null>(null);
  $effect(() => {
    const i = active;
    if (!open || !acEl) return;
    (acEl.querySelectorAll('.opt')[i] as HTMLElement | undefined)?.scrollIntoView?.({ block: 'nearest' });
  });

  function sync() {
    if (el && hl) {
      hl.scrollLeft = el.scrollLeft;
      hl.scrollTop = el.scrollTop;
    }
  }
  function context() {
    if (!el) return;
    const caret = el.selectionStart ?? value.length;
    const before = value.slice(0, caret);
    const o = before.lastIndexOf('{{');
    const c = before.lastIndexOf('}}');
    if (o < 0 || o < c) {
      open = false;
      return;
    }
    const m = /[A-Za-z0-9_.\-]*$/.exec(before);
    token = m ? m[0] : '';
    const pre = before.slice(o + 2, caret - token.length).trimEnd();
    mode = pre.endsWith('|') ? 'filter' : 'path';
    if (mode === 'path' && pre.length > 0 && !pre.endsWith('(') && !pre.endsWith(',')) {
      // already past the primary (e.g. "{{ var.x " … ) → only filters make sense after a pipe
      open = false;
      return;
    }
    active = 0;
    place();
    open = items.length > 0 || (mode === 'path' && paths.length > 0);
  }
  async function accept(s: ExprSuggestion) {
    if (!el) return;
    const caret = el.selectionStart ?? value.length;
    const start = caret - token.length;
    let rest = value.slice(caret);
    const nextClose = rest.indexOf('}}');
    const nextOpen = rest.indexOf('{{');
    const closed = nextClose >= 0 && (nextOpen < 0 || nextClose < nextOpen);
    const lead = value.slice(0, start);
    const needSpace = /\{\{$/.test(lead) ? ' ' : '';
    if (s.group) {
      // a path prefix: insert it and list its children (no closing "}}" yet)
      value = lead + needSpace + s.insert + rest;
      oninput?.(value);
      await tick();
      const at = (lead + needSpace + s.insert).length;
      el.focus();
      el.setSelectionRange(at, at);
      sync();
      context();
      return;
    }
    let ins = needSpace + s.insert;
    let tail = '';
    if (!closed) tail = ' }}';
    else if (!/^\s/.test(rest) && !rest.startsWith('}}') && !rest.startsWith('|')) ins += ' ';
    else if (rest.startsWith('}}')) ins += ' ';
    value = lead + ins + tail + rest;
    oninput?.(value);
    open = false;
    await tick();
    const pos = (lead + ins).length - (s.back ?? 0);
    el.focus();
    el.setSelectionRange(pos, pos);
    sync();
  }
  function onkeydown(e: KeyboardEvent) {
    if (!open || !items.length) {
      if (e.key === ' ' && e.ctrlKey) {
        e.preventDefault();
        context();
      }
      return;
    }
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      active = (active + 1) % items.length;
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      active = (active - 1 + items.length) % items.length;
    } else if (e.key === 'Enter' || e.key === 'Tab') {
      e.preventDefault();
      accept(items[active]!);
    } else if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      open = false;
    }
  }
  function onInput(e: Event) {
    value = (e.currentTarget as HTMLInputElement).value;
    oninput?.(value);
    context();
    sync();
  }
  const listId = `ex-${Math.random().toString(36).slice(2, 8)}`;
</script>

<svelte:window onscroll={() => open && place()} onresize={() => open && place()} />

<div class="ex" class:ml={multiline} class:sm={size === 'sm'} data-testid={testid}>
  <div class="wrap" class:invalid={invalid || !!syntaxErr} class:disabled>
    <div class="hl" bind:this={hl} aria-hidden="true">{#each toks as t, i (i)}<span class={t.c}>{t.t}</span>{/each}{#if !value}<span class="ph">{placeholder}</span>{/if}&#8203;</div>
    {#if multiline}
      <textarea
        bind:this={el}
        {id}
        {value}
        {disabled}
        rows="3"
        spellcheck="false"
        autocomplete="off"
        aria-label={ariaLabel}
        aria-autocomplete="list"
        aria-controls={open ? listId : undefined}
        oninput={onInput}
        {onkeydown}
        onscroll={sync}
        onclick={context}
        onblur={() => setTimeout(() => (open = false), 150)}
        data-testid={testid ? `${testid}-input` : undefined}
      ></textarea>
    {:else}
      <input
        bind:this={el}
        {id}
        {value}
        {disabled}
        spellcheck="false"
        autocomplete="off"
        aria-label={ariaLabel}
        role="combobox"
        aria-expanded={open}
        aria-autocomplete="list"
        aria-controls={open ? listId : undefined}
        oninput={onInput}
        {onkeydown}
        onscroll={sync}
        onclick={context}
        onkeyup={(e) => (e.key === 'ArrowLeft' || e.key === 'ArrowRight' || e.key === 'Home' || e.key === 'End') && context()}
        onblur={() => setTimeout(() => (open = false), 150)}
        data-testid={testid ? `${testid}-input` : undefined}
      />
    {/if}
  </div>
  {#if open && items.length}
    <div class="ac" use:portal role="listbox" id={listId} style="{pos.up ? `bottom:${window.innerHeight - pos.top}px` : `top:${pos.top}px`};left:{pos.left}px;min-width:{pos.width}px;max-height:{pos.maxH}px" bind:this={acEl} data-testid={testid ? `${testid}-ac` : undefined}>
      {#each items as s, i (s.label)}
        <button type="button" role="option" aria-selected={i === active} class="opt" class:on={i === active} class:grp={s.group} onmousedown={(e) => (e.preventDefault(), accept(s))} onmouseenter={() => (active = i)} data-path={s.label}>
          <span class="k k-{s.kind}">{s.kind}</span>
          <span class="lb">{s.label}</span>
          <span class="dt">{s.detail}</span>
        </button>
      {/each}
      <div class="foot">↑↓ choose · Enter insert · Esc close · Ctrl+Space open</div>
    </div>
  {/if}
  {#if preview && hasBlock}
    <div class="pv" data-testid={testid ? `${testid}-preview` : undefined}>
      {#if syntaxErr}
        <span class="e">{syntaxErr.code}: {syntaxErr.message} (at {syntaxErr.pos})</span>
      {:else if resolution && resolution.ok}
        <span class="arrow">→</span><code class="res">{#if resolution.result.masked === ''}""{:else}<AutoHostText text={resolution.result.masked} />{/if}</code>
        {#if resolution.result.secret}<span class="badge amber">secret</span>{/if}
      {:else if resolution && !resolution.ok}
        <span class="w">{resolution.error.code}: {resolution.error.message}</span>
      {:else if !ctx}
        <span class="faint">expression · resolved on the site</span>
      {/if}
    </div>
  {/if}
  {#if numeric && !hasBlock && value !== '' && !/^\d+$/.test(value)}
    <div class="pv"><span class="e">must be a number or one {'{{ }}'} block</span></div>
  {/if}
</div>

<style>
  .ex {
    position: relative;
    min-width: 0;
    width: 100%;
  }
  .wrap {
    position: relative;
    border: 1px solid var(--border-strong);
    border-radius: var(--r-md);
    background: var(--surface);
    transition:
      border-color var(--dur-fast),
      box-shadow var(--dur-fast);
  }
  .wrap:focus-within {
    border-color: var(--accent);
    box-shadow: var(--focus-ring);
  }
  .wrap.invalid {
    border-color: var(--red);
  }
  .wrap.disabled {
    background: var(--surface-2);
  }
  .hl,
  input,
  textarea {
    font-family: var(--font-mono);
    font-size: 12.5px;
    line-height: 20px;
    padding: 5px 10px;
    margin: 0;
    border: 0;
    letter-spacing: 0;
    tab-size: 2;
  }
  .sm .hl,
  .sm input {
    padding: 2px 8px;
    font-size: 12px;
  }
  .hl {
    position: absolute;
    inset: 0;
    white-space: pre;
    overflow: hidden;
    pointer-events: none;
    color: var(--text);
  }
  .ml .hl {
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  input,
  textarea {
    position: relative;
    display: block;
    width: 100%;
    background: transparent;
    color: transparent;
    caret-color: var(--text);
    outline: none;
    height: 30px;
    resize: vertical;
  }
  .sm input {
    height: 24px;
  }
  textarea {
    height: auto;
    min-height: 66px;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  input:focus,
  textarea:focus {
    box-shadow: none;
  }
  input::selection,
  textarea::selection {
    background: color-mix(in srgb, var(--accent) 30%, transparent);
    color: transparent;
  }
  .ph {
    color: var(--text-3);
  }
  .hl :global(.br) {
    color: var(--violet-text);
    font-weight: 600;
  }
  .hl :global(.pa) {
    color: var(--blue-text);
  }
  .hl :global(.fl) {
    color: var(--green-text);
  }
  .hl :global(.st) {
    color: var(--amber-text);
  }
  .hl :global(.nu) {
    color: var(--amber-text);
  }
  .hl :global(.op) {
    color: var(--text-3);
  }
  .hl :global(.br),
  .hl :global(.pa),
  .hl :global(.fl),
  .hl :global(.st),
  .hl :global(.op),
  .hl :global(.bk),
  .hl :global(.nu) {
    background: color-mix(in srgb, var(--violet-soft) 60%, transparent);
  }
  .hl :global(.er) {
    text-decoration: wavy underline var(--red);
    text-underline-offset: 3px;
    background: color-mix(in srgb, var(--red-soft) 80%, transparent);
  }
  .ac {
    position: fixed;
    z-index: var(--z-popover);
    max-width: min(560px, 92vw);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    box-shadow: var(--shadow-lg);
    padding: 4px;
    max-height: 300px;
    overflow-y: auto;
  }
  .opt {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    border: 0;
    background: transparent;
    padding: 5px 8px;
    border-radius: var(--r-sm);
    cursor: pointer;
    text-align: left;
    font-size: 12.5px;
  }
  .opt.on {
    background: var(--accent-soft);
  }
  .k {
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    padding: 1px 5px;
    border-radius: 4px;
    background: var(--gray-soft);
    color: var(--text-2);
    min-width: 64px;
    text-align: center;
    flex: none;
  }
  .k-var {
    background: var(--blue-soft);
    color: var(--blue-text);
  }
  .k-secret {
    background: var(--amber-soft);
    color: var(--amber-text);
  }
  .k-site {
    background: var(--violet-soft);
    color: var(--violet-text);
  }
  .k-filter {
    background: var(--green-soft);
    color: var(--green-text);
  }
  .lb {
    font-family: var(--font-mono);
    white-space: nowrap;
  }
  .grp .lb {
    font-weight: 600;
  }
  .grp .dt {
    font-family: inherit;
  }
  .k-containers {
    background: var(--accent-soft);
    color: var(--accent-text);
  }
  .dt {
    margin-left: auto;
    color: var(--text-3);
    font-size: 11.5px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 200px;
    font-family: var(--font-mono);
  }
  .foot {
    font-size: 10.5px;
    color: var(--text-3);
    padding: 4px 8px 2px;
    border-top: 1px solid var(--border);
    margin-top: 4px;
  }
  .pv {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-top: 4px;
    font-size: var(--fs-xs);
    min-width: 0;
  }
  .arrow {
    color: var(--text-3);
  }
  .res {
    font-size: 11.5px;
    background: var(--green-soft);
    color: var(--green-text);
    padding: 0 5px;
    border-radius: 4px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 100%;
  }
  .e {
    color: var(--red-text);
    font-family: var(--font-mono);
  }
  .w {
    color: var(--amber-text);
    font-family: var(--font-mono);
  }
</style>
