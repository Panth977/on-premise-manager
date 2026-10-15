<script lang="ts">
  /**
   * "Request headers → container" of a proxy route (nginx proxy_set_header / proxy_set_header X "") — one editor for both
   * GUIs (§21 #102): the edge defaults (chips; "overridden" when a row sets the same name), rows (name combobox + value
   * expression) and the remove chips. A row whose header only means something on a RESPONSE (isResponseOnlyHeader)
   * shows W_RESPONSE_HEADER_AS_REQUEST with the one-click fix "Move to response headers" (onmove); hop-by-hop names warn.
   * Test ids: req-defaults · route-headers · hdr-name-{i} · hdr-value-{i} · hdr-del-{i} · hdr-warn-{i} · hdr-move-{i} ·
   * hdr-hop-{i} · add-header · route-remove-headers.
   */
  import type { ExprContext } from '@opm/shared/expr';
  import { isResponseOnlyHeader } from '@opm/shared/validate';
  import { EDGE_DEFAULT_REQUEST_HEADERS, HOP_BY_HOP_HEADERS } from '@opm/shared/edge-resolve';
  import Combobox from '../../primitives/Combobox.svelte';
  import ExprInput from '../../primitives/ExprInput.svelte';
  import ChipList from '../../primitives/ChipList.svelte';
  import Field from '../../primitives/Field.svelte';
  import Btn from '../../primitives/Btn.svelte';
  import Icon from '../../primitives/Icon.svelte';
  import type { ValidationIssue } from '@opm/shared/types';
  import { REQUEST_HEADERS, rowErrors } from './headers';
  interface Props {
    set: Record<string, string>;
    remove: string[];
    onchange: (set: Record<string, string>, remove: string[], errors: string[]) => void;
    ctx: ExprContext | null;
    onmove?: (name: string) => void;
    disabled?: boolean;
    removeIssues?: readonly ValidationIssue[];
  }
  let { set, remove, onchange, ctx, onmove, disabled = false, removeIssues = [] }: Props = $props();
  const DEFAULT_VALUES: Record<string, string> = { Host: 'the request host (kept)', 'X-Real-IP': 'client IP', 'X-Forwarded-For': 'client IP (replaced, not appended)', 'X-Forwarded-Proto': 'http / https', 'X-Forwarded-Host': 'the request host' };
  const hopNames = HOP_BY_HOP_HEADERS.map((h) => h.toLowerCase());
  type Row = { name: string; value: string };
  let rows = $state<Row[]>([]);
  let last = '';
  $effect.pre(() => {
    const j = JSON.stringify(set);
    if (j === last) return;
    last = j;
    rows = Object.entries(set).map(([name, value]) => ({ name, value }));
  });
  const errs = $derived(rowErrors(rows));
  function emit(rm = remove) {
    const v: Record<string, string> = {};
    for (const r of rows) if (r.name.trim()) v[r.name.trim()] = r.value;
    last = JSON.stringify(v);
    onchange(v, rm, rowErrors($state.snapshot(rows) as Row[]));
  }
  const overridden = (n: string) => rows.some((h) => h.name.trim().toLowerCase() === n.toLowerCase());
  const nameOpts = $derived(REQUEST_HEADERS.filter((h) => !rows.some((x) => x.name.toLowerCase() === h.toLowerCase())).map((h) => ({ value: h })));
</script>

<div class="stack rq">
  <p class="hint">nginx <code>proxy_set_header</code>. Sent to the container, the browser never sees them. Cache-Control, CSP, CORS … for the browser belong in “Response headers → browser”.</p>
  <div class="defs" data-testid="req-defaults">
    <span class="hint">Always sent:</span>
    {#each EDGE_DEFAULT_REQUEST_HEADERS as n (n)}<span class="chip def" class:over={overridden(n)} title={overridden(n) ? 'overridden by a row below' : DEFAULT_VALUES[n]}><span class="mono">{n}</span><span class="faint">{overridden(n) ? 'overridden' : DEFAULT_VALUES[n]}</span></span>{/each}
  </div>
  <div class="hdrs" data-testid="route-headers">
    {#each rows as h, i (i)}
      {@const n = h.name.trim()}
      {@const respOnly = !!n && isResponseOnlyHeader(n, h.value)}
      {@const hop = hopNames.includes(n.toLowerCase())}
      <div class="hrow" class:warnrow={respOnly || hop}>
        <div class="hn"><Combobox value={h.name} options={nameOpts} allowCustom mono size="sm" placeholder="X-Script-Name" ariaLabel="Header name" {disabled} testid="hdr-name-{i}" onchange={(t) => ((h.name = t.trim()), emit())} /></div>
        <div class="hv"><ExprInput value={h.value} onchange={(v) => ((h.value = v), emit())} field="edge" {ctx} size="sm" placeholder={'$host or {{ self.slug }}'} ariaLabel="Value of {n || 'header'}" {disabled} testid="hdr-value-{i}" /></div>
        {#if !disabled}<Btn size="sm" variant="ghost" icon="x" aria-label="Remove header {n}" onclick={() => (rows.splice(i, 1), emit())} data-testid="hdr-del-{i}" />{/if}
      </div>
      {#if respOnly}
        <div class="banner warn small fix" data-testid="hdr-warn-{i}" data-code="W_RESPONSE_HEADER_AS_REQUEST" role="alert">
          <Icon name="alert" size={14} />
          <div class="grow"><span class="issue-code">W_RESPONSE_HEADER_AS_REQUEST</span><strong>{n}</strong> only means something on a RESPONSE: here it is sent to the container, the browser never gets it. For nginx <code>add_header</code> / <code>proxy_hide_header</code> use “Response headers → browser”.</div>
          {#if !disabled && onmove}<Btn size="sm" variant="primary" icon="arrowDown" onclick={() => onmove(n)} data-testid="hdr-move-{i}">Move to response headers</Btn>{/if}
        </div>
      {:else if hop}
        <div class="field-warn" data-testid="hdr-hop-{i}"><span class="issue-code">V_EDGE_HOP</span>{n} is managed by the edge (WebSocket upgrades and keep-alive work without it); the value is ignored — remove the row.</div>
      {/if}
    {/each}
    {#if errs.length}<div class="field-err">{errs.join(' · ')}</div>{/if}
    {#if !disabled}<div><Btn size="sm" icon="plus" onclick={() => rows.push({ name: '', value: '' })} data-testid="add-header">Add request header</Btn></div>{/if}
    <p class="hint">Variables: <code>$host</code> <code>$remote_addr</code> <code>$scheme</code> <code>$request_uri</code> <code>$http_&lt;name&gt;</code> and <code>{'{{ }}'}</code> expressions. A row overrides a default with the same name.</p>
  </div>
  <Field label="Remove request headers" id="r-rh" hint="Not forwarded to the container (names, Enter after each)" issues={removeIssues}>
    <ChipList id="r-rh" value={remove} mono placeholder="Cookie" {disabled} testid="route-remove-headers" onchange={(v) => emit(v)} />
  </Field>
</div>

<style>
  .defs {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: center;
  }
  .chip.def {
    display: inline-flex;
    gap: 6px;
    align-items: center;
    padding: 2px 8px;
    border-radius: var(--r-full);
    border: 1px solid var(--border);
    background: var(--surface-2);
    font-size: var(--fs-xs);
  }
  .chip.def.over {
    text-decoration: line-through;
    opacity: 0.6;
  }
  .hdrs {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .hrow {
    display: grid;
    grid-template-columns: minmax(140px, 0.8fr) minmax(0, 1.4fr) 28px;
    gap: 6px;
    align-items: start;
  }
  .hn,
  .hv {
    min-width: 0;
  }
  .fix {
    align-items: center;
    flex-wrap: wrap;
  }
  .faint {
    color: var(--text-3);
  }
  @media (max-width: 640px) {
    .hrow {
      grid-template-columns: minmax(0, 1fr) 28px;
    }
    .hv {
      grid-column: 1 / -1;
      grid-row: 2;
    }
  }
</style>
