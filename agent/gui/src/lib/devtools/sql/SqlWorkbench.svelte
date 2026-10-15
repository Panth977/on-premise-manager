<script lang="ts">
  /**
   * SQL workbench (§21 #97) shared by PostgreSQL, SQLite and Cassandra (CQL): CodeMirror 6 editor (dialect, schema
   * completion, hover cards, lint, find / replace, multi-cursor, fold, comment), editor tabs (persisted per site +
   * connection in localStorage), Run (Mod-Enter = selection or the statement under the cursor), Run all
   * (Shift-Mod-Enter), Explain / Explain analyze (pg), params ($1… / ?), transaction, timeout, max rows, Cancel,
   * results dock (one tab per statement), schema sidebar, saved queries, history. Destructive statements: the agent
   * answers 422 confirmRequired → typed connection-name modal → re-run with confirm.
   */
  import { onDestroy, onMount, untrack } from 'svelte';
  import { stripSqlComments, type DevToolConnection, type DevToolHistoryItem, type DevToolSaved, type DevToolsInfo, type ExecuteResponse, type SchemaNamespace, type SchemaTable, type SqlSchemaResponse } from '@opm/shared/devtools';
  import { errMessage, isApiError } from '$lib/api';
  import { appHref } from '$lib/nav';
  import { toasts } from '$lib/state/toast.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import NumberStepper from '$lib/ui/NumberStepper.svelte';
  import Editor from '../cm/Editor.svelte';
  import type { EditorHandle, EditorSchema } from '../cm/load';
  import TypedConfirm from '../TypedConfirm.svelte';
  import SavedPanel from '../SavedPanel.svelte';
  import SaveDialog from '../SaveDialog.svelte';
  import HistoryPanel from '../HistoryPanel.svelte';
  import { confirmReasons, dev, newQueryId } from '../api';
  import { devStore } from '../state.svelte';
  import { copyText } from '../grid';
  import { loadJson, saveJson } from '../kinds';
  import { sqlStatementRanges } from '../sqlRanges';
  import SchemaTree, { type TreeAction } from './SchemaTree.svelte';
  import TableDrawer from './TableDrawer.svelte';
  import ResultPane from './ResultPane.svelte';
  import { quoteIdent } from './edits.svelte';

  let { conn, info, site }: { conn: DevToolConnection; info: DevToolsInfo; site: string } = $props();
  // svelte-ignore state_referenced_locally
  const kind = conn.kind as 'postgres' | 'sqlite' | 'cassandra';
  // svelte-ignore state_referenced_locally
  const KEY = `opm.dev.sql.${site}.${conn.id}`;

  // ── tabs (persisted; never credentials — only the operator's statements)
  interface Tab {
    id: string;
    title: string;
    text: string;
    saved: DevToolSaved | null;
    params: string[];
  }
  const starter = kind === 'cassandra' ? 'SELECT * FROM system.local;\n' : kind === 'sqlite' ? "SELECT name, type FROM sqlite_master WHERE type = 'table';\n" : 'SELECT now(), current_database(), version();\n';
  const stored = loadJson<{ tabs: Tab[]; active: string } | null>(KEY, null);
  const initTabs: Tab[] = stored?.tabs?.length ? stored.tabs : [{ id: 't1', title: 'Query 1', text: starter, saved: null, params: [] }];
  let tabs = $state<Tab[]>(initTabs);
  let active = $state<string>(stored?.active && initTabs.some((t) => t.id === stored.active) ? stored.active : initTabs[0]!.id);
  const tab = $derived(tabs.find((t) => t.id === active) ?? tabs[0]!);
  let persistT: ReturnType<typeof setTimeout> | null = null;
  $effect(() => {
    const snap = JSON.stringify({ tabs, active });
    untrack(() => {
      if (persistT) clearTimeout(persistT);
      persistT = setTimeout(() => saveJson(KEY, JSON.parse(snap)), 300);
    });
  });
  function newTab(text = '', title?: string, saved: DevToolSaved | null = null) {
    const n = Math.max(0, ...tabs.map((t) => Number(/^t(\d+)$/.exec(t.id)?.[1] ?? 0))) + 1;
    const t: Tab = { id: `t${n}`, title: title ?? `Query ${n}`, text, saved, params: [] };
    tabs = [...tabs, t];
    active = t.id;
    return t;
  }
  function closeTab(id: string) {
    if (tabs.length === 1) {
      tabs = [{ ...tabs[0]!, text: '', title: 'Query 1', saved: null }];
      return;
    }
    const i = tabs.findIndex((t) => t.id === id);
    tabs = tabs.filter((t) => t.id !== id);
    if (active === id) active = tabs[Math.max(0, i - 1)]!.id;
    delete runs[id];
  }
  let renaming = $state<string | null>(null);

  // ── schema
  let schema = $state<SqlSchemaResponse | null>(null);
  let schemaErr = $state<string | null>(null);
  let schemaLoading = $state(false);
  async function loadSchema(refresh = false, quiet = false) {
    schemaLoading = true;
    schemaErr = null;
    try {
      schema = await dev.schema({ siteSlug: site, connectionId: conn.id, query: refresh ? { refresh: true } : {} });
      if (refresh && !quiet) toasts.ok('Schema refreshed');
    } catch (e) {
      schemaErr = errMessage(e);
    } finally {
      schemaLoading = false;
      void devStore.refreshConns();
    }
  }
  onMount(() => loadSchema());
  const edSchema = $derived<EditorSchema | null>(schema ? { defaultNamespace: kind === 'postgres' ? 'public' : (schema.namespaces.find((n) => !n.system)?.name ?? null), namespaces: schema.namespaces } : null);

  // ── editor
  let handle = $state<EditorHandle | null>(null);
  const paramCount = $derived.by(() => {
    const s = stripSqlComments(tab.text);
    if (kind === 'postgres') return Math.max(0, ...[...s.matchAll(/\$(\d+)/g)].map((m) => Number(m[1])));
    return (s.match(/\?/g) ?? []).length;
  });
  let showParams = $state(false);
  $effect(() => {
    if (paramCount > 0) untrack(() => (showParams = true));
  });
  function paramValue(s: string | undefined): unknown {
    const t = (s ?? '').trim();
    if (/^null$/i.test(t)) return null;
    if (t === 'true' || t === 'false') return t === 'true';
    if (/^-?\d+(\.\d+)?(e[+-]?\d+)?$/i.test(t)) return Number(t);
    if (/^[[{"]/.test(t)) {
      try {
        return JSON.parse(t);
      } catch {
        /* text */
      }
    }
    return s ?? '';
  }
  const paramType = (s: string | undefined) => {
    const v = paramValue(s);
    return v === null ? 'null' : Array.isArray(v) ? 'array' : typeof v === 'object' ? 'json' : typeof v;
  };

  // ── options
  let transaction = $state(false);
  let timeoutSec = $state<number | null>(null);
  let maxRows = $state<number | null>(null);

  // ── runs (one per tab)
  interface Run {
    queryId: string;
    text: string;
    from: number;
    startedAt: number;
    running: boolean;
    res: ExecuteResponse | null;
    error: string | null;
    active: number;
    explain: 'plan' | 'analyze' | null;
  }
  let runs = $state<Record<string, Run>>({});
  const run = $derived(runs[tab.id] ?? null);
  let now = $state(Date.now());
  let ticker: ReturnType<typeof setInterval> | null = null;
  onDestroy(() => ticker && clearInterval(ticker));
  let confirm = $state<{ open: boolean; reasons: string[]; what: string; again: (typed: string) => void }>({ open: false, reasons: [], what: '', again: () => {} });

  async function execute(text: string, from: number, explain: 'plan' | 'analyze' | null, confirmText?: string) {
    const t = tab;
    if (!text.trim()) return toasts.info('Nothing to run', 'Write a statement, or put the cursor in one.');
    const queryId = newQueryId();
    runs[t.id] = { queryId, text, from, startedAt: Date.now(), running: true, res: runs[t.id]?.res ?? null, error: null, active: 0, explain };
    now = Date.now();
    ticker ??= setInterval(() => (now = Date.now()), 100);
    handle?.clearError();
    try {
      const res = await dev.executeSql({
        siteSlug: site,
        connectionId: conn.id,
        body: { statement: text, params: paramCount ? Array.from({ length: paramCount }, (_, i) => paramValue(t.params[i])) : undefined, explain, queryId, timeoutSec: timeoutSec ?? undefined, maxRows: maxRows ?? undefined, transaction: kind !== 'cassandra' && transaction ? true : undefined, confirm: confirmText },
      });
      const failed = res.results.findIndex((r) => !r.ok);
      runs[t.id] = { ...runs[t.id]!, running: false, res, active: failed >= 0 ? failed : Math.max(0, res.results.length - 1) };
      if (failed >= 0) showError(t.id, failed);
      if (res.results.some((r) => r.ok && r.type === 'command' && /^(CREATE|DROP|ALTER)/i.test(r.commandTag ?? ''))) loadSchema(true, true);
    } catch (e) {
      const reasons = confirmReasons(e);
      if (reasons) {
        runs[t.id] = { ...runs[t.id]!, running: false, error: null };
        confirm = { open: true, reasons, what: text, again: (typed) => execute(text, from, explain, typed) };
        return;
      }
      runs[t.id] = { ...runs[t.id]!, running: false, error: errMessage(e), res: null };
      if (isApiError(e) && e.status === 409) toasts.err(e, 'Not run');
    } finally {
      if (!Object.values(runs).some((r) => r.running) && ticker) {
        clearInterval(ticker);
        ticker = null;
      }
    }
  }
  function runTarget(explain: 'plan' | 'analyze' | null = null) {
    if (!handle) return;
    const r = handle.runTarget();
    execute(r.text, r.from, explain);
  }
  function runAll() {
    if (!handle) return;
    const r = handle.all();
    execute(r.text, r.from, null);
  }
  async function cancel() {
    const r = run;
    if (!r?.running) return;
    try {
      const c = await dev.cancel({ siteSlug: site, connectionId: conn.id, body: { queryId: r.queryId } });
      toasts.info(c.cancelled ? 'Cancel sent' : 'It had already finished');
    } catch (e) {
      toasts.err(e, 'Cancel failed');
    }
  }
  /** statement i of the run → its offset in the editor; error position (1-based in the statement) → mark */
  function showError(tabId: string, i: number) {
    const r = runs[tabId];
    const res = r?.res?.results[i];
    if (!r || !res?.error || tabId !== active) return;
    const ranges = sqlStatementRanges(r.text);
    const st = ranges[res.index];
    if (!st) return;
    const base = r.from + st.from;
    const pos = res.error.position;
    if (pos !== null && pos > 0) handle?.markError(base + pos - 1, base + pos - 1, res.error.message);
    else handle?.markError(base, base + st.text.length, res.error.message);
  }
  async function applyEdits(stmts: string[], original: string): Promise<boolean> {
    const go = async (typed?: string): Promise<boolean> => {
      try {
        const res = await dev.executeSql({ siteSlug: site, connectionId: conn.id, body: { statement: stmts.join(';\n'), queryId: newQueryId(), transaction: true, confirm: typed } });
        const bad = res.results.find((r) => !r.ok);
        if (bad) {
          toasts.push('bad', 'Changes not saved (rolled back)', bad.error?.message);
          return false;
        }
        toasts.ok(`Saved ${stmts.length} ${stmts.length === 1 ? 'change' : 'changes'}`);
        const r = runs[tab.id];
        execute(original, r?.from ?? 0, null);
        return true;
      } catch (e) {
        const reasons = confirmReasons(e);
        if (reasons) {
          confirm = { open: true, reasons, what: stmts.join(';\n'), again: (t) => void go(t) };
          return false;
        }
        toasts.err(e, 'Changes not saved');
        return false;
      }
    };
    return go();
  }

  // ── sidebar
  type Side = 'schema' | 'saved' | 'history';
  let side = $state<Side>('schema');
  let sideOpen = $state(typeof window === 'undefined' || window.innerWidth > 900);
  let savedV = $state(0);
  let histV = $state(0);
  $effect(() => {
    if (run && !run.running) untrack(() => histV++);
  });
  let td = $state<{ open: boolean; ns: string; table: string; q: string }>({ open: false, ns: '', table: '', q: '' });
  function qualifiedOf(ns: SchemaNamespace, t: SchemaTable) {
    return kind === 'sqlite' ? quoteIdent(t.name) : `${quoteIdent(ns.name)}.${quoteIdent(t.name)}`;
  }
  async function treeAction(a: TreeAction, ns: SchemaNamespace, t: SchemaTable | null, text: string) {
    if (a === 'insert') return handle?.insert(text);
    if (a === 'copy') return (await copyText(text)) && toasts.ok(`Copied ${text}`);
    if (!t) return;
    if (a === 'details') td = { open: true, ns: ns.name, table: t.name, q: qualifiedOf(ns, t) };
    if (a === 'select') selectFirst(ns, t);
  }
  function selectFirst(ns: SchemaNamespace | string, t: SchemaTable | string) {
    const q = typeof ns === 'string' ? td.q : qualifiedOf(ns, t as SchemaTable);
    const name = typeof t === 'string' ? t : t.name;
    const text = `SELECT * FROM ${q} LIMIT 100;`;
    const tb = newTab(text, name);
    setTimeout(() => {
      if (active === tb.id) execute(text, 0, null);
    }, 30);
  }
  function openSaved(s: DevToolSaved) {
    const ex = tabs.find((t) => t.saved?.id === s.id);
    if (ex) {
      active = ex.id;
      return;
    }
    newTab(s.content, s.name, s);
  }
  function openHistory(h: DevToolHistoryItem) {
    newTab(h.statement, `${h.op} ${new Date(h.ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}`);
  }
  let saveOpen = $state(false);
  let savedCollections = $state<string[]>([]);
  async function openSave() {
    try {
      const r = await dev.listSaved({ siteSlug: site, query: { kind } });
      savedCollections = [...new Set(r.items.map((x) => x.collection).filter(Boolean))];
    } catch {
      savedCollections = [];
    }
    saveOpen = true;
  }
  function onSaved(s: DevToolSaved) {
    const t = tabs.find((x) => x.id === active);
    if (t) {
      t.saved = s;
      t.title = s.name;
    }
    savedV++;
  }

  // ── split editor / results (persisted ratio)
  let editorH = $state(loadJson<number>('opm.dev.sql.editorH', 260));
  function startSplit(e: PointerEvent) {
    e.preventDefault();
    const y0 = e.clientY;
    const h0 = editorH;
    const move = (ev: PointerEvent) => (editorH = Math.max(90, Math.min(window.innerHeight - 260, h0 + ev.clientY - y0)));
    const up = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
      saveJson('opm.dev.sql.editorH', editorH);
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  }
  const elapsed = $derived(run?.running ? ((now - run.startedAt) / 1000).toFixed(1) : null);
  const hasExplain = kind !== 'cassandra';
</script>

<div class="wb" class:noside={!sideOpen} data-testid="sql-workbench">
  {#if sideOpen}
    <aside class="side" aria-label="Schema, saved queries and history">
      <div class="stabs" role="tablist">
        {#each [['schema', 'Schema', 'tree'], ['saved', 'Saved', 'save'], ['history', 'History', 'history']] as [k, l, ic] (k)}
          <button role="tab" aria-selected={side === k} class:on={side === k} onclick={() => (side = k as Side)} data-testid="side-tab-{k}"><Icon name={ic} size={13} />{l}</button>
        {/each}
        <button class="btn ghost icon xs hide" onclick={() => (sideOpen = false)} aria-label="Hide the sidebar" title="Hide the sidebar"><Icon name="chevronLeft" size={14} /></button>
      </div>
      <div class="sbody">
        {#if side === 'schema'}
          <SchemaTree {schema} loading={schemaLoading} error={schemaErr} onrefresh={() => loadSchema(true)} onaction={treeAction} oninsert={(t) => handle?.insert(t)} />
        {:else if side === 'saved'}
          <SavedPanel {site} kinds={[kind]} connectionId={conn.id} onopen={openSaved} version={savedV} />
        {:else}
          <HistoryPanel {site} connectionId={conn.id} onopen={openHistory} version={histV} />
        {/if}
      </div>
    </aside>
  {/if}

  <section class="main">
    <div class="etabs" role="tablist" aria-label="Editor tabs">
      {#if !sideOpen}<button class="btn ghost icon sm" onclick={() => (sideOpen = true)} aria-label="Show the sidebar" title="Schema · saved · history" data-testid="side-show"><Icon name="tree" size={14} /></button>{/if}
      <div class="tl">
        {#each tabs as t (t.id)}
          <div class="et" class:on={t.id === active} role="tab" aria-selected={t.id === active} tabindex="0" onclick={() => (active = t.id)} onkeydown={(e) => e.key === 'Enter' && (active = t.id)} ondblclick={() => (renaming = t.id)} data-testid="etab-{t.id}">
            {#if t.saved}<Icon name="save" size={11} />{/if}
            {#if renaming === t.id}
              <!-- svelte-ignore a11y_autofocus -->
              <input class="rn" value={t.title} autofocus onblur={(e) => ((t.title = e.currentTarget.value.trim() || t.title), (renaming = null))} onkeydown={(e) => e.key === 'Enter' && e.currentTarget.blur()} aria-label="Tab name" />
            {:else}
              <span class="tt">{t.title}</span>
            {/if}
            {#if runs[t.id]?.running}<span class="spin sm" aria-label="running"></span>{/if}
            <button class="x" onclick={(e) => (e.stopPropagation(), closeTab(t.id))} aria-label="Close {t.title}" tabindex="-1"><Icon name="x" size={11} /></button>
          </div>
        {/each}
      </div>
      <button class="btn ghost icon sm" onclick={() => newTab()} aria-label="New tab" title="New tab" data-testid="etab-new"><Icon name="plus" size={14} /></button>
    </div>

    <div class="toolbar">
      <Btn variant="primary" size="sm" icon="play" disabled={!handle || !!run?.running} onclick={() => runTarget()} title="Run the selection, or the statement under the cursor (Ctrl/⌘-Enter)" data-testid="sql-run">Run</Btn>
      <Btn size="sm" icon="fastForward" disabled={!handle || !!run?.running} onclick={runAll} title="Run every statement of the tab (Shift-Ctrl/⌘-Enter)" data-testid="sql-run-all">Run all</Btn>
      {#if hasExplain}
        <Btn size="sm" variant="ghost" icon="list" disabled={!handle || !!run?.running} onclick={() => runTarget('plan')} title="Show the plan without running" data-testid="sql-explain">Explain</Btn>
        {#if kind === 'postgres'}<Btn size="sm" variant="ghost" icon="timer" disabled={!handle || !!run?.running} onclick={() => runTarget('analyze')} title="EXPLAIN ANALYZE: runs the statement and measures it" data-testid="sql-explain-analyze">Analyze</Btn>{/if}
      {/if}
      {#if run?.running}
        <Btn size="sm" variant="danger" icon="stop" onclick={cancel} data-testid="sql-cancel">Cancel</Btn>
        <span class="small mono running" data-testid="sql-elapsed">{elapsed} s</span>
      {/if}
      <span class="sep"></span>
      {#if kind !== 'cassandra'}<label class="switch small" title={conn.readOnly && kind === 'postgres' ? 'Read-only connection: every execution already runs in BEGIN READ ONLY (transaction control is refused)' : 'Run every statement in one transaction (rolled back on error)'}><input type="checkbox" bind:checked={transaction} disabled={conn.readOnly && kind === 'postgres'} data-testid="sql-tx" />Transaction</label>{/if}
      <span class="opt" title="Timeout (empty = connection / agent default)"><Icon name="timer" size={13} /><NumberStepper value={timeoutSec} onchange={(v) => (timeoutSec = v)} min={1} max={3600} step={5} nullable size="sm" placeholder={String(conn.options.timeoutSec ?? info.limits.statementTimeoutSec)} suffix="s" label="Timeout seconds" /></span>
      <span class="opt hide-s" title="Rows per page (empty = default)"><Icon name="list" size={13} /><NumberStepper value={maxRows} onchange={(v) => (maxRows = v)} min={1} max={10000} step={100} nullable size="sm" placeholder={String(conn.options.maxRows ?? info.limits.maxRows)} label="Rows per page" /></span>
      <button class="btn ghost sm" class:on={showParams} onclick={() => (showParams = !showParams)} aria-pressed={showParams} data-testid="sql-params-toggle"><Icon name="braces" size={13} />Params{paramCount ? ` (${paramCount})` : ''}</button>
      <span class="grow"></span>
      {#if conn.readOnly}<span class="badge amber" title="The agent refuses writes on this connection"><Icon name="lock" size={11} />read-only</span>{/if}
      <Btn size="sm" variant="ghost" icon="save" onclick={openSave} title="Save to collections (Ctrl/⌘-S)" data-testid="sql-save">{tab.saved ? 'Save' : 'Save…'}</Btn>
    </div>

    {#if showParams}
      <div class="params" data-testid="sql-params">
        {#if !paramCount}
          <span class="faint small">No parameters in this tab. Use {kind === 'postgres' ? '$1, $2 …' : '? placeholders'} in the statement; values are sent separately (never pasted into the SQL).</span>
        {:else}
          {#each Array.from({ length: paramCount }, (_, i) => i) as i (i)}
            <label class="prm">
              <span class="mono small pn">{kind === 'postgres' ? `$${i + 1}` : `?${i + 1}`}</span>
              <input class="input sm mono" value={tab.params[i] ?? ''} oninput={(e) => (tab.params[i] = e.currentTarget.value)} placeholder="value" data-testid="sql-param-{i + 1}" />
              <span class="faint xs pt">{paramType(tab.params[i])}</span>
            </label>
          {/each}
          <span class="faint xs">NULL · true / false · numbers · JSON are typed; "quoted" forces text.</span>
        {/if}
      </div>
    {/if}

    <div class="ed" style:height="{editorH}px">
      {#key tab.id}
        <Editor value={tab.text} lang={kind} schema={edSchema} onchange={(v) => (tab.text = v)} onrun={() => runTarget()} onrunall={runAll} onsave={openSave} bind:handle testid="sql-editor" ariaLabel="SQL editor" placeholder="Write {kind === 'cassandra' ? 'CQL' : 'SQL'} — Ctrl/⌘-Enter runs the statement under the cursor" />
      {/key}
    </div>
    <div class="splitter" role="separator" aria-orientation="horizontal" aria-label="Resize the editor" onpointerdown={startSplit}><i></i></div>

    <div class="dock" data-testid="results">
      {#if !run}
        <div class="hint pad">Run a statement to see results here. <span class="kbd">Ctrl/⌘</span>+<span class="kbd">Enter</span> runs the statement under the cursor (or the selection), <span class="kbd">Shift</span>+<span class="kbd">Ctrl/⌘</span>+<span class="kbd">Enter</span> runs everything.</div>
      {:else if run.error}
        <div class="banner bad" data-testid="sql-error"><Icon name="alert" /><div class="grow">{run.error}</div></div>
      {:else if run.res}
        {@const res = run.res}
        <div class="rtabs">
          <div class="rtl" role="tablist" aria-label="Statement results">
            {#each res.results as r, i (i)}
              <button role="tab" aria-selected={run.active === i} class:on={run.active === i} class:bad={!r.ok} onclick={() => ((runs[tab.id]!.active = i), !r.ok && showError(tab.id, i))} data-testid="rtab-{i}" title={r.statement}>
                <Icon name={!r.ok ? 'alert' : r.type === 'rows' ? 'table' : r.type === 'explain' ? 'list' : 'check'} size={12} />
                <span>{i + 1} · {!r.ok ? 'error' : r.type === 'rows' ? `${r.totalRows.toLocaleString()} ${r.totalRows === 1 ? 'row' : 'rows'}` : (r.commandTag ?? 'ok')}</span>
              </button>
            {/each}
          </div>
          <span class="grow"></span>
          <span class="faint xs nowrap">{res.results.length} {res.results.length === 1 ? 'statement' : 'statements'} · {res.durationMs} ms{run.running ? ' · running again…' : ''}</span>
          {#if res.activityId}<a class="xs" href={appHref(site, 'activities')} title="Logged as devtools.query ({res.activityId})">logged</a>{/if}
        </div>
        {@const cur = res.results[run.active]}
        {#if cur}
          {#key `${res.queryId}:${run.active}`}
            <div class="rbody"><ResultPane result={cur} {site} {kind} {schema} readOnly={conn.readOnly} onshowerror={() => showError(tab.id, run.active)} onapply={(s) => applyEdits(s, cur.statement)} /></div>
          {/key}
        {/if}
      {:else if run.running}
        <div class="pad running"><span class="spin"></span> Running… {elapsed} s</div>
      {/if}
    </div>
  </section>
</div>

<TypedConfirm bind:open={confirm.open} connectionName={conn.name} reasons={confirm.reasons} what={confirm.what} onconfirm={(t) => confirm.again(t)} />
<SaveDialog bind:open={saveOpen} {site} {kind} connectionId={conn.id} content={tab.text} existing={tab.saved} collections={savedCollections} suggestedName={tab.title.startsWith('Query ') ? '' : tab.title} onsaved={onSaved} />
<TableDrawer bind:open={td.open} {site} connectionId={conn.id} {kind} namespace={td.ns} table={td.table} qualified={td.q} onselect={() => selectFirst(td.ns, td.table)} oninsert={(t) => handle?.insert(t)} />

<style>
  .wb {
    display: grid;
    grid-template-columns: 270px minmax(0, 1fr);
    gap: 10px;
    height: calc(100dvh - 132px);
    min-height: 540px;
  }
  .wb.noside {
    grid-template-columns: minmax(0, 1fr);
  }
  .side {
    display: flex;
    flex-direction: column;
    min-height: 0;
    border: 1px solid var(--border);
    border-radius: var(--r-lg);
    background: var(--surface);
    overflow: hidden;
  }
  .stabs {
    display: flex;
    align-items: center;
    gap: 2px;
    padding: 4px 4px 0;
    border-bottom: 1px solid var(--border);
    background: var(--surface-2);
  }
  .stabs button[role='tab'] {
    all: unset;
    display: inline-flex;
    gap: 5px;
    align-items: center;
    padding: 6px 9px 7px;
    font-size: 12.5px;
    color: var(--text-2);
    cursor: pointer;
    border-bottom: 2px solid transparent;
    margin-bottom: -1px;
  }
  .stabs button.on {
    color: var(--text);
    border-bottom-color: var(--accent);
    font-weight: var(--fw-medium);
  }
  .stabs .hide {
    margin-left: auto;
  }
  .sbody {
    flex: 1;
    min-height: 0;
  }
  .main {
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
    border: 1px solid var(--border);
    border-radius: var(--r-lg);
    background: var(--surface);
    overflow: hidden;
  }
  .etabs {
    display: flex;
    align-items: flex-end;
    gap: 2px;
    padding: 4px 6px 0;
    background: var(--surface-2);
    border-bottom: 1px solid var(--border);
    min-width: 0;
  }
  .tl {
    display: flex;
    gap: 2px;
    overflow-x: auto;
    scrollbar-width: thin;
    min-width: 0;
  }
  .et {
    display: inline-flex;
    gap: 5px;
    align-items: center;
    padding: 5px 6px 6px 10px;
    border: 1px solid transparent;
    border-bottom: 0;
    border-radius: 6px 6px 0 0;
    font-size: 12.5px;
    color: var(--text-2);
    cursor: pointer;
    white-space: nowrap;
    max-width: 200px;
  }
  .et.on {
    background: var(--surface);
    border-color: var(--border);
    color: var(--text);
    margin-bottom: -1px;
    padding-bottom: 7px;
  }
  .tt {
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .et .x {
    all: unset;
    display: grid;
    place-items: center;
    width: 16px;
    height: 16px;
    border-radius: 4px;
    color: var(--text-3);
    cursor: pointer;
  }
  .et .x:hover {
    background: var(--surface-hover);
    color: var(--text);
  }
  .rn {
    font: inherit;
    width: 110px;
    border: 1px solid var(--accent);
    border-radius: 4px;
    padding: 0 4px;
    background: var(--surface);
    color: var(--text);
  }
  .spin.sm {
    width: 10px;
    height: 10px;
  }
  .toolbar {
    display: flex;
    gap: 6px;
    align-items: center;
    flex-wrap: wrap;
    padding: 6px 8px;
    border-bottom: 1px solid var(--border);
  }
  .sep {
    width: 1px;
    height: 20px;
    background: var(--border);
    margin: 0 2px;
  }
  .opt {
    display: inline-flex;
    gap: 4px;
    align-items: center;
    color: var(--text-3);
  }
  .opt :global(input) {
    width: 52px;
  }
  .btn.on {
    background: var(--accent-soft);
    color: var(--accent-text);
  }
  .running {
    color: var(--accent-text);
  }
  .params {
    display: flex;
    gap: 8px 14px;
    flex-wrap: wrap;
    align-items: center;
    padding: 6px 10px;
    border-bottom: 1px solid var(--border);
    background: var(--surface-2);
  }
  .prm {
    display: inline-flex;
    gap: 6px;
    align-items: center;
  }
  .prm .input {
    width: 170px;
    height: 26px;
  }
  .pn {
    color: var(--violet-text);
  }
  .pt {
    width: 44px;
  }
  .ed {
    min-height: 90px;
    flex: none;
  }
  .splitter {
    height: 7px;
    cursor: row-resize;
    display: grid;
    place-items: center;
    border-top: 1px solid var(--border);
    border-bottom: 1px solid var(--border);
    background: var(--surface-2);
    flex: none;
    touch-action: none;
  }
  .splitter i {
    width: 36px;
    height: 3px;
    border-radius: 2px;
    background: var(--border-strong);
  }
  .dock {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    padding: 6px 8px 8px;
    gap: 6px;
  }
  .pad {
    padding: 12px 6px;
  }
  .rtabs {
    display: flex;
    gap: 8px;
    align-items: center;
    min-width: 0;
  }
  .rtl {
    display: flex;
    gap: 4px;
    overflow-x: auto;
    min-width: 0;
    scrollbar-width: thin;
  }
  .rtl button {
    all: unset;
    display: inline-flex;
    gap: 5px;
    align-items: center;
    padding: 3px 9px;
    border-radius: var(--r-full);
    border: 1px solid var(--border);
    font-size: 12px;
    cursor: pointer;
    white-space: nowrap;
    color: var(--text-2);
  }
  .rtl button.on {
    background: var(--accent-soft);
    border-color: color-mix(in srgb, var(--accent) 40%, var(--border));
    color: var(--accent-text);
  }
  .rtl button.bad {
    color: var(--red-text);
  }
  .rtl button.bad.on {
    background: var(--red-soft);
    border-color: color-mix(in srgb, var(--red) 40%, var(--border));
  }
  .rbody {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
  }
  @media (max-width: 900px) {
    .wb {
      grid-template-columns: minmax(0, 1fr);
      height: auto;
    }
    .side {
      max-height: 340px;
    }
    .main {
      min-height: 640px;
    }
    .ed {
      height: 220px !important;
    }
    .hide-s {
      display: none;
    }
  }
</style>
