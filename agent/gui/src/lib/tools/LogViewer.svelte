<script lang="ts">
  /**
   * Live log viewer (round T, nav2 §21 #97: a component, no page of its own). src containers = the container page's Logs
   * tab (one container, "Merge with…" adds others merged by time); agent = Debug › Live agent log; access = Network ›
   * Edge access log. ANSI colours (§21 #93), follow (EventSource, reconnect with since), search, ≤ 5000 lines in the DOM.
   */
  import { untrack, tick } from 'svelte';
  import type { AccessLogLine, AgentLogLine, LogStreamLine, ToolsContainer } from '@opm/shared/agent-tools';
  import { toasts } from '$lib/state/toast.svelte';
  import { tools, toolsPath, openLogStream, downloadUrl, type StreamHandle } from '$lib/tools/api';
  import { colorFor, logTs, bytes } from '$lib/tools/fmt';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Select from '$lib/ui/Select.svelte';
  import MultiSelect from '$lib/ui/MultiSelect.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Empty from '$lib/ui/Empty.svelte';
  import AnsiToggles from '$lib/ui/AnsiToggles.svelte';
  import { AnsiStreams, ansiPrefs, lineHtml, textHtml } from '$lib/ansi.svelte';
  import { stripAnsi } from '@opm/shared/ansi';

  type Src = 'containers' | 'agent' | 'access';
  type Row = { id: number; kind: 'c'; l: LogStreamLine } | { id: number; kind: 'a'; l: AgentLogLine } | { id: number; kind: 'x'; l: AccessLogLine };
  const MAX = 5000;

  let { site, src, container }: { site: string; src: Src; container?: string } = $props();
  const slug = $derived(site);

  // containers of the site
  let cons = $state<ToolsContainer[]>([]);
  let sel = $state<string[]>([]);
  $effect(() => {
    const s = slug;
    if (s === '*') return;
    untrack(async () => {
      try {
        const r = await tools.toolsContainers({ siteSlug: s });
        cons = r.items.filter((c) => c.siteSlug === s);
        if (container) sel = [container, ...extra.filter((x) => x !== container && cons.some((c) => c.ref === x))];
        else {
          sel = cons.filter((c) => c.state === 'running' && !c.failedCopy).map((c) => c.ref);
          if (!sel.length && cons.length) sel = [cons[0].ref];
        }
      } catch (e) {
        toasts.err(e, 'Containers failed to load');
      }
    });
  });
  const refs = $derived(cons.map((c) => c.ref));
  /** container mode: other containers merged into this view */
  let extra = $state<string[]>([]);
  function setExtra(v: string[]) {
    extra = v;
    if (container) sel = [container, ...v.filter((x) => x !== container)];
  }
  function toggle(ref: string) {
    sel = sel.includes(ref) ? sel.filter((x) => x !== ref) : [...sel, ref];
  }

  // filters
  let stdout = $state(true);
  let stderr = $state(true);
  let range = $state<'15m' | '1h' | '24h' | 'custom'>('1h');
  let sinceIn = $state('');
  let untilIn = $state('');
  let tail = $state(500);
  let follow = $state(true);
  let level = $state<'debug' | 'info' | 'warn' | 'error'>('info');
  let showTs = $state(true);
  let wrap = $state(false);
  const sinceIso = $derived.by(() => {
    if (range === 'custom') return sinceIn ? new Date(sinceIn).toISOString() : undefined;
    return new Date(Date.now() - { '15m': 900, '1h': 3600, '24h': 86400 }[range] * 1000).toISOString();
  });
  const untilIso = $derived(range === 'custom' && untilIn ? new Date(untilIn).toISOString() : undefined);
  const canFollow = $derived(!untilIso);

  // stream
  let rows = $state.raw<Row[]>([]);
  let nextId = 0;
  let conn = $state<'idle' | 'connecting' | 'live' | 'done' | 'error'>('idle');
  let streamMsg = $state<string | null>(null);
  let buf: Row[] = [];
  let flushT: ReturnType<typeof setTimeout> | null = null;
  let handle: StreamHandle | null = null;
  let newWhilePaused = $state(0);
  /** §21 #93: container lines parsed once on arrival; SGR state + ESC-lost detection per container + stdout/stderr */
  const streams = new AnsiStreams();
  const mode = $derived(ansiPrefs.mode);

  function flush() {
    flushT = null;
    if (!buf.length) return;
    const add = buf;
    buf = [];
    const next = rows.length + add.length > MAX ? [...rows, ...add].slice(-MAX) : [...rows, ...add];
    if (next.length && next.length < rows.length + add.length) streams.prune(next[0].id);
    rows = next;
    if (paused) newWhilePaused += add.length;
    else tick().then(scrollBottom);
  }
  function push(r: Row) {
    if (r.kind === 'c') streams.add(r.id, `${r.l.c}|${r.l.s}`, r.l.x);
    buf.push(r);
    if (!flushT) flushT = setTimeout(flush, 80);
  }
  const key = $derived(JSON.stringify([slug, src, sel, stdout, stderr, sinceIso && range === 'custom' ? sinceIso : range, untilIso, tail, follow && canFollow, level]));
  $effect(() => {
    void key;
    if (slug === '*') return;
    return untrack(() => start());
  });
  function start(): () => void {
    handle?.close();
    rows = [];
    buf = [];
    streams.reset();
    paused = false;
    newWhilePaused = 0;
    if (src === 'containers' && sel.length === 0) {
      conn = 'idle';
      return () => {};
    }
    conn = 'connecting';
    streamMsg = null;
    const fol = follow && canFollow;
    const q = { since: sinceIso, until: untilIso, tail, follow: fol };
    const url =
      src === 'containers'
        ? toolsPath('logStream', { siteSlug: slug }, { ...q, containers: sel.join(','), stdout, stderr })
        : src === 'agent'
          ? toolsPath('agentLogStream', { siteSlug: slug }, { ...q, level })
          : toolsPath('accessLogStream', { siteSlug: slug }, q);
    const h = openLogStream<{ t: string }>(
      url,
      {
        open: () => (conn = fol ? 'live' : 'connecting'),
        meta: () => (conn = fol ? 'live' : 'connecting'),
        line: (l) => {
          if (conn !== 'live' && fol) conn = 'live';
          push(src === 'containers' ? { id: nextId++, kind: 'c', l: l as unknown as LogStreamLine } : src === 'agent' ? { id: nextId++, kind: 'a', l: l as unknown as AgentLogLine } : { id: nextId++, kind: 'x', l: l as unknown as AccessLogLine });
        },
        end: () => {
          flush();
          conn = 'done';
        },
        error: (m) => {
          streamMsg = m;
          if (m !== 'reconnecting…') conn = 'error';
        },
      },
      { follow: fol },
    );
    handle = h;
    return () => h.close();
  }
  function clear() {
    rows = [];
    streams.reset();
    newWhilePaused = 0;
  }

  // scrolling: stick to the bottom unless the user scrolled up
  let box = $state<HTMLDivElement | null>(null);
  let paused = $state(false);
  function onScroll() {
    if (!box) return;
    const atBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 24;
    paused = !atBottom;
    if (atBottom) newWhilePaused = 0;
  }
  function scrollBottom() {
    if (box && !paused) box.scrollTop = box.scrollHeight;
  }
  function jumpLatest() {
    paused = false;
    newWhilePaused = 0;
    tick().then(() => box && (box.scrollTop = box.scrollHeight));
  }

  // search (client side)
  let q = $state('');
  let cur = $state(0);
  // search matches the VISIBLE text (escape codes removed)
  const textOf = (r: Row) => (r.kind === 'c' ? (streams.get(r.id)?.plain ?? stripAnsi(r.l.x)) : r.kind === 'a' ? `${r.l.module} ${stripAnsi(r.l.msg)} ${JSON.stringify(r.l.fields)}` : `${r.l.method} ${r.l.host}${r.l.uri} ${r.l.status} ${r.l.remote}`);
  const matches = $derived(q ? rows.filter((r) => textOf(r).toLowerCase().includes(q.toLowerCase())).map((r) => r.id) : []);
  $effect(() => {
    void q;
    cur = 0;
  });
  function go(d: number) {
    if (!matches.length) return;
    cur = (cur + d + matches.length) % matches.length;
    paused = true;
    tick().then(() => box?.querySelector(`[data-row="${matches[cur]}"]`)?.scrollIntoView({ block: 'center' }));
  }
  function parts(text: string): { t: string; m: boolean }[] {
    if (!q) return [{ t: text, m: false }];
    const out: { t: string; m: boolean }[] = [];
    const lo = text.toLowerCase();
    const ql = q.toLowerCase();
    let i = 0;
    for (;;) {
      const j = lo.indexOf(ql, i);
      if (j < 0) break;
      if (j > i) out.push({ t: text.slice(i, j), m: false });
      out.push({ t: text.slice(j, j + q.length), m: true });
      i = j + q.length;
    }
    if (i < text.length) out.push({ t: text.slice(i), m: false });
    return out;
  }

  async function dl() {
    try {
      const url = toolsPath('logDownload', { siteSlug: slug }, { containers: sel.join(','), stdout, stderr, since: sinceIso, until: untilIso, tail: Math.max(tail, 5000) });
      await downloadUrl(url, `opm-${slug}-logs.log`);
    } catch (e) {
      toasts.err(e, 'Download failed');
    }
  }
  function dlLocal() {
    const text = rows.map((r) => (r.kind === 'a' ? `${r.l.t} ${r.l.level} ${r.l.module} ${r.l.msg} ${JSON.stringify(r.l.fields)}` : r.kind === 'x' ? `${r.l.t} ${r.l.remote} ${r.l.method} ${r.l.host}${r.l.uri} ${r.l.status} ${r.l.size} ${r.l.durMs}ms` : '')).join('\n') + '\n';
    const a = document.createElement('a');
    a.href = URL.createObjectURL(new Blob([text], { type: 'text/plain' }));
    a.download = `opm-${slug}-${src}-${new Date().toISOString().replace(/[:.]/g, '-')}.log`;
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 1000);
  }
  const lvColor = { debug: '#73737e', info: '#60a5fa', warn: '#fbbf24', error: '#f87171' } as const;
  const stTone = (s: number) => (s >= 500 ? 'bad' : s >= 400 ? 'warn' : s >= 300 ? 'info' : 'ok');
</script>


<section class="card">
  <div class="bar">
    {#if src === 'containers' && container}
      <div class="row wrap" data-testid="log-containers">
        <span class="chipbtn on static"><i style="background:{colorFor(container, refs)}"></i><span class="mono">{container}</span></span>
        {#if cons.length > 1}
          <MultiSelect size="sm" values={extra} onchange={setExtra} placeholder="Merge with…" allLabel="Merged with every container" ariaLabel="Merge the logs of other containers" testid="log-merge" options={cons.filter((c) => c.ref !== container).map((c) => ({ value: c.ref, label: c.ref, hint: c.failedCopy ? 'failed copy' : c.state, icon: 'container' }))} />
          {#if extra.length}<span class="faint xs">merged by time · line tags show the container</span>{/if}
        {/if}
      </div>
    {:else if src === 'containers'}
      <div class="chips" role="group" aria-label="Containers" data-testid="log-containers">
        {#each cons as c (c.ref)}
          <button class="chipbtn" class:on={sel.includes(c.ref)} aria-pressed={sel.includes(c.ref)} onclick={() => toggle(c.ref)} title={`${c.image} · ${c.state}`}>
            <i style="background:{colorFor(c.ref, refs)}"></i><span class="mono">{c.ref}</span>{#if c.state !== 'running'}<span class="faint xs">{c.failedCopy ? 'failed copy' : c.state}</span>{/if}
          </button>
        {/each}
        {#if cons.length > 1}
          <button class="btn ghost xs" onclick={() => (sel = sel.length === cons.length ? [] : cons.map((c) => c.ref))}>{sel.length === cons.length ? 'None' : 'All'}</button>
        {/if}
      </div>
    {/if}
    <div class="row wrap">
      <SegmentedControl size="sm" label="Time range" options={[{ value: '15m', label: '15 m' }, { value: '1h', label: '1 h' }, { value: '24h', label: '24 h' }, { value: 'custom', label: 'Custom' }]} value={range} onchange={(v) => (range = v as typeof range)} testid="log-range" />
      {#if range === 'custom'}
        <label class="lab">from <input class="input sm" type="datetime-local" bind:value={sinceIn} aria-label="Since" /></label>
        <label class="lab">to <input class="input sm" type="datetime-local" bind:value={untilIn} aria-label="Until" /></label>
      {/if}
      <label class="lab">tail
        <Select size="sm" value={String(tail)} onchange={(v) => (tail = Number(v))} ariaLabel="Lines per container" testid="log-tail" options={[100, 500, 1000, 5000, 10000].map((n) => ({ value: String(n), label: String(n) }))} />
      </label>
      {#if src === 'containers'}
        <label class="switch"><input type="checkbox" bind:checked={stdout} />stdout</label>
        <label class="switch"><input type="checkbox" bind:checked={stderr} />stderr</label>
      {:else if src === 'agent'}
        <SegmentedControl size="sm" label="Minimum level" options={[{ value: 'debug', label: '≥ debug' }, { value: 'info', label: '≥ info' }, { value: 'warn', label: '≥ warn' }, { value: 'error', label: 'error' }]} value={level} onchange={(v) => (level = v as typeof level)} testid="log-level" />
      {/if}
      <label class="switch" title={canFollow ? 'Keep the stream open and append new lines' : 'Not with an end time'}><input type="checkbox" bind:checked={follow} disabled={!canFollow} data-testid="log-follow" />follow</label>
      <span class="live {conn}" data-testid="log-state">{conn === 'live' ? 'live' : conn === 'connecting' ? 'loading…' : conn === 'done' ? 'complete' : conn === 'error' ? 'stopped' : 'idle'}</span>
    </div>
    <div class="row wrap">
      <div class="search">
        <Icon name="search" size={14} />
        <input class="input sm" placeholder="Search in the lines" bind:value={q} onkeydown={(e) => e.key === 'Enter' && go(e.shiftKey ? -1 : 1)} aria-label="Search" data-testid="log-search" />
        {#if q}<span class="cnt" data-testid="log-matches">{matches.length ? `${cur + 1} / ${matches.length}` : '0'}</span>{/if}
        <button class="btn ghost icon xs" onclick={() => go(-1)} disabled={!matches.length} aria-label="Previous match"><Icon name="arrowUp" size={13} /></button>
        <button class="btn ghost icon xs" onclick={() => go(1)} disabled={!matches.length} aria-label="Next match"><Icon name="arrowDown" size={13} /></button>
      </div>
      <span class="grow"></span>
      {#if src !== 'access'}<AnsiToggles small testid="log-ansi" />{/if}
      <label class="switch"><input type="checkbox" bind:checked={showTs} />timestamps</label>
      <label class="switch"><input type="checkbox" bind:checked={wrap} />wrap</label>
      <Btn size="sm" variant="ghost" icon="trash" onclick={clear}>Clear</Btn>
      {#if src === 'containers'}
        <Btn size="sm" icon="download" onclick={dl} disabled={!sel.length} data-testid="log-download">Download .log</Btn>
      {:else}
        <Btn size="sm" icon="download" onclick={dlLocal} disabled={!rows.length} data-testid="log-download">Download .log</Btn>
      {/if}
    </div>
  </div>

  <div class="viewwrap">
    {#if streamMsg && conn === 'error'}<div class="banner bad small" style="margin:10px"><Icon name="alert" /><div class="grow">{streamMsg}</div><Btn size="xs" onclick={() => start()}>Retry</Btn></div>{/if}
    {#if src === 'containers' && sel.length === 0}
      <Empty icon="logs" title={cons.length ? 'Pick a container' : 'No container yet'} text={cons.length ? 'Choose one or more containers above to merge their logs.' : `No OPM container on ${slug} yet.`} compact />
    {:else if src === 'access'}
      <div class="tablebox" bind:this={box} onscroll={onScroll} data-testid="access-log">
        <table class="table compact acc">
          <thead><tr><th>Time</th><th>Status</th><th>Method</th><th>Host + path</th><th class="num">Duration</th><th class="num">Size</th><th>Client</th><th>Upstream</th></tr></thead>
          <tbody>
            {#each rows as r (r.id)}
              {#if r.kind === 'x'}
                <tr data-row={r.id} class:hit={matches[cur] === r.id}>
                  <td class="mono xs nowrap">{logTs(r.l.t)}</td>
                  <td><span class="badge {stTone(r.l.status)}">{r.l.status}</span></td>
                  <td class="mono small">{r.l.method}</td>
                  <td class="mono small uri" title={r.l.host + r.l.uri}><span class="faint">{r.l.host}</span>{#each parts(r.l.uri) as p, i (i)}{#if p.m}<mark>{p.t}</mark>{:else}{p.t}{/if}{/each}</td>
                  <td class="num small" class:warn-text={r.l.durMs > 1000}>{r.l.durMs < 10 ? r.l.durMs.toFixed(1) : Math.round(r.l.durMs)} ms</td>
                  <td class="num small">{bytes(r.l.size)}</td>
                  <td class="mono xs">{r.l.remote}</td>
                  <td class="mono xs faint">{r.l.upstream ?? '—'}</td>
                </tr>
              {/if}
            {:else}
              <tr><td colspan="8" class="muted small">{conn === 'connecting' ? 'Loading…' : 'No requests in this range.'}</td></tr>
            {/each}
          </tbody>
        </table>
      </div>
    {:else}
      <div class="logview" class:wrap bind:this={box} onscroll={onScroll} data-testid="log-lines">
        {#each rows as r (r.id)}
          <div class="ln" data-row={r.id} class:hit={matches[cur] === r.id} class:err={r.kind === 'c' && r.l.s === 'stderr'}>
            {#if showTs}<span class="ts">{logTs(r.l.t)}</span>{/if}
            {#if r.kind === 'c'}
              {#if sel.length > 1}<span class="tag" style="color:{colorFor(r.l.c, refs)}">{r.l.c}</span>{/if}
              {@const pl = streams.get(r.id)}
              <span class="tx ansi-line">{#if pl}{@html lineHtml(pl, mode, q)}{:else}{r.l.x}{/if}</span>
            {:else if r.kind === 'a'}
              <span class="lv" style="color:{lvColor[r.l.level]}">{r.l.level}</span><span class="mod">{r.l.module}</span>
              <span class="tx ansi-line">{@html textHtml(r.l.msg, mode, q)}{#if Object.keys(r.l.fields).length}<span class="f"> {JSON.stringify(r.l.fields)}</span>{/if}</span>
            {/if}
          </div>
        {:else}
          <div class="ln empty">{conn === 'connecting' ? 'Loading…' : 'No lines in this range.'}</div>
        {/each}
      </div>
    {/if}
    {#if paused && (newWhilePaused > 0 || conn === 'live')}
      <button class="jump" onclick={jumpLatest} data-testid="log-jump"><Icon name="toBottom" size={14} />{newWhilePaused ? `${newWhilePaused} new — jump to latest` : 'Jump to latest'}</button>
    {/if}
  </div>
  <div class="foot row between">
    <span class="faint xs">{rows.length} {rows.length === 1 ? 'line' : 'lines'}{rows.length >= MAX ? ` (last ${MAX} kept)` : ''}{paused && conn === 'live' ? ' · paused while you scroll' : ''}</span>
    <span class="faint xs">{src === 'access' ? 'edge-access.log (rotated 20 MiB × 5)' : src === 'agent' ? 'agent debug log · secrets are never written' : 'Docker logs API'}</span>
  </div>
</section>

<style>
  .bar {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 12px 14px;
    border-bottom: 1px solid var(--border);
  }
  .row.wrap {
    flex-wrap: wrap;
    gap: 8px 12px;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .chipbtn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 26px;
    padding: 0 10px;
    border-radius: var(--r-full);
    border: 1px solid var(--border);
    background: var(--surface);
    color: var(--text-2);
    font-size: var(--fs-sm);
    cursor: pointer;
  }
  .chipbtn i {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    opacity: 0.35;
  }
  .chipbtn.on {
    border-color: var(--accent);
    background: var(--accent-soft);
    color: var(--accent-text);
  }
  .chipbtn.static {
    cursor: default;
  }
  .chipbtn.on i {
    opacity: 1;
  }
  .lab {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: var(--fs-sm);
    color: var(--text-2);
  }
  .lab .input {
    width: auto;
  }
  .live {
    font-size: 11.5px;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    color: var(--text-3);
  }
  .live::before {
    content: '';
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--border-strong);
  }
  .live.live {
    color: var(--green-text);
  }
  .live.live::before {
    background: var(--green);
    box-shadow: 0 0 0 3px var(--green-soft);
  }
  .live.error::before {
    background: var(--red);
  }
  .search {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    color: var(--text-3);
  }
  .search .input {
    width: 220px;
  }
  .cnt {
    font-size: 11.5px;
    color: var(--text-2);
    min-width: 40px;
    text-align: center;
    font-variant-numeric: tabular-nums;
  }
  .viewwrap {
    position: relative;
  }
  .logview {
    font-family: var(--font-mono);
    font-size: 12px;
    line-height: 1.6;
    background: var(--log-bg);
    color: var(--log-text);
    height: min(62vh, 640px);
    overflow: auto;
    padding: 8px 0;
  }
  .ln {
    display: flex;
    gap: 10px;
    padding: 0 14px;
    white-space: pre;
  }
  .logview.wrap .ln {
    white-space: pre-wrap;
    word-break: break-all;
  }
  .ln:hover {
    background: rgba(255, 255, 255, 0.04);
  }
  .ln.hit {
    background: rgba(251, 191, 36, 0.14);
  }
  .ln.err .tx {
    color: #fca5a5;
  }
  .ln.empty {
    color: #73737e;
  }
  .ts {
    color: #73737e;
    flex: none;
  }
  .tag {
    flex: none;
    min-width: 0;
    max-width: 180px;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .lv {
    flex: none;
    width: 40px;
    font-weight: 700;
  }
  .mod {
    flex: none;
    width: 76px;
    color: #a78bfa;
  }
  .f {
    color: #a8a8b3;
  }
  .tx {
    min-width: 0;
  }
  mark {
    background: #fbbf24;
    color: #18181b;
    border-radius: 2px;
  }
  .tablebox {
    height: min(62vh, 640px);
    overflow: auto;
  }
  .acc .uri {
    max-width: 420px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .acc tr.hit td {
    background: var(--amber-soft);
  }
  .jump {
    position: absolute;
    right: 18px;
    bottom: 14px;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    border: 0;
    border-radius: var(--r-full);
    background: var(--accent);
    color: #fff;
    font-size: var(--fs-sm);
    padding: 6px 12px;
    box-shadow: 0 6px 18px rgba(0, 0, 0, 0.25);
    cursor: pointer;
  }
  .foot {
    padding: 8px 14px;
    border-top: 1px solid var(--border);
  }
  @media (max-width: 700px) {
    .search .input {
      width: 150px;
    }
  }
</style>
