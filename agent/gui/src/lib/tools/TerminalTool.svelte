<script lang="ts">
  /**
   * Terminal (round T; nav2 §21 #97: a component). mode container = the container page's Terminal tab (exec into that
   * container); mode host = Agent › Host shell (typed site slug first). Tabs ≤ 4 per site, open sessions + transcripts of
   * this target.
   */
  import { untrack } from 'svelte';
  import { hostShellConfirmOk, TERMINAL_LIMITS, type TerminalSession, type ToolsContainer, type ToolsInfo, type TranscriptMeta } from '@opm/shared/agent-tools';
  import { poll } from '$lib/state/resource.svelte';
  import { toasts } from '$lib/state/toast.svelte';
  import { errMessage, isApiError } from '$lib/api';
  import { tools, downloadUrl, fetchUrlText } from '$lib/tools/api';
  import { toolsInfo } from '$lib/tools/site';
  import { bytes, uptime } from '$lib/tools/fmt';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import Drawer from '$lib/ui/Drawer.svelte';
  import Confirm from '$lib/ui/Confirm.svelte';
  import Time from '$lib/ui/Time.svelte';
  import Empty from '$lib/ui/Empty.svelte';
  import AnsiLog from '$lib/ui/AnsiLog.svelte';
  import TerminalPane, { type ConnState } from '$lib/tools/TerminalPane.svelte';

  let { site, mode, container = '' }: { site: string; mode: 'container' | 'host'; container?: string } = $props();
  const slug = $derived(site);

  let info = $state<ToolsInfo | null>(null);
  let infoErr = $state<string | null>(null);
  let cons = $state<ToolsContainer[]>([]);
  $effect(() => {
    const s = slug;
    if (s === '*') return;
    untrack(async () => {
      try {
        info = await toolsInfo(s, true);
        infoErr = null;
        const r = await tools.toolsContainers({ siteSlug: s });
        cons = r.items.filter((c) => c.siteSlug === s && !c.failedCopy);
        ctr = container;
      } catch (e) {
        infoErr = errMessage(e);
      }
    });
  });

  // new session form
  const kind = $derived(mode);
  let ctr = $state('');
  let shell = $state<'sh' | 'bash' | 'custom'>('sh');
  let custom = $state('/bin/sh -l');
  let busy = $state(false);
  let hostOpen = $state(false);
  let typed = $state('');
  const running = $derived(cons.filter((c) => c.state === 'running'));
  const ctrRunning = $derived(running.some((c) => c.ref === container));
  const mine = (x: { kind: string; target: string }) => (mode === 'host' ? x.kind === 'host' : x.kind === 'container' && x.target === container);

  // open tabs in this page
  interface Tab {
    sessionId: string;
    wsPath: string;
    target: string;
    site: string;
    kind: 'container' | 'host';
    conn: ConnState;
  }
  let tabs = $state<Tab[]>([]);
  let activeId = $state<string | null>(null);
  const panes: Record<string, { end: () => void } | undefined> = $state({});
  const openOnSite = $derived(Math.max(tabs.filter((t) => t.conn === 'open' || t.conn === 'connecting').length, info?.activeSessions ?? 0));
  const full = $derived(tabs.filter((t) => t.conn === 'open' || t.conn === 'connecting').length >= TERMINAL_LIMITS.maxSessions);

  async function create(confirm?: string) {
    if (slug === '*') return;
    busy = true;
    try {
      const body =
        kind === 'host'
          ? { kind: 'host' as const, confirm, cols: 120, rows: 32 }
          : { kind: 'container' as const, containerSlug: ctr, shell, command: shell === 'custom' ? custom.trim().split(/\s+/).filter(Boolean) : undefined, cols: 120, rows: 32 };
      const r = await tools.createTerminal({ siteSlug: slug, body });
      const t: Tab = { sessionId: r.sessionId, wsPath: r.wsPath, target: kind === 'host' ? `host (${info?.hostShell.user ?? 'root'})` : ctr, site: slug, kind, conn: 'connecting' };
      tabs = [...tabs, t];
      activeId = t.sessionId;
      hostOpen = false;
      setTimeout(loadSessions, 400);
    } catch (e) {
      if (isApiError(e) && e.code === 'conflict' && e.details.max) toasts.push('warn', `${e.details.max} sessions are already open on ${slug}`, 'End one first (Open sessions below).');
      else toasts.err(e, 'Could not open the terminal');
      if (kind === 'host') return false;
    } finally {
      busy = false;
    }
  }
  function onTabState(id: string, s: ConnState) {
    tabs = tabs.map((t) => (t.sessionId === id ? { ...t, conn: s } : t));
    if (s === 'open') loadSessions();
    if (s === 'closed' || s === 'error') setTimeout(() => (loadSessions(), loadTranscripts()), 300);
  }
  function closeTab(id: string) {
    const t = tabs.find((x) => x.sessionId === id);
    if (t && (t.conn === 'open' || t.conn === 'connecting')) panes[id]?.end();
    tabs = tabs.filter((x) => x.sessionId !== id);
    if (activeId === id) activeId = tabs[tabs.length - 1]?.sessionId ?? null;
  }

  // server lists
  let sessions = $state<TerminalSession[]>([]);
  let transcripts = $state<TranscriptMeta[] | null>(null);
  async function loadSessions() {
    if (slug === '*') return;
    try {
      sessions = (await tools.terminalSessions({ siteSlug: slug })).items.filter(mine);
    } catch {
      /* shown as empty */
    }
  }
  async function loadTranscripts() {
    if (slug === '*') return;
    try {
      transcripts = (await tools.transcripts({ siteSlug: slug })).items.filter(mine);
    } catch (e) {
      transcripts = [];
      toasts.err(e, 'Transcripts failed to load');
    }
  }
  $effect(() => {
    void slug;
    untrack(loadTranscripts);
    return poll(loadSessions, 10_000);
  });
  let killFor = $state<TerminalSession | null>(null);
  let killOpen = $state(false);
  async function kill() {
    if (!killFor) return;
    try {
      await tools.closeTerminal({ siteSlug: slug, sessionId: killFor.sessionId });
      toasts.ok('Session ended');
      const mine = tabs.find((t) => t.sessionId === killFor!.sessionId);
      if (mine) closeTab(mine.sessionId);
      loadSessions();
      loadTranscripts();
    } catch (e) {
      toasts.err(e, 'Could not end the session');
      return false;
    }
  }

  // transcript viewer
  let tvOpen = $state(false);
  let tvFor = $state<TranscriptMeta | null>(null);
  let tvText = $state<string | null>(null);
  async function view(t: TranscriptMeta) {
    tvFor = t;
    tvText = null;
    tvOpen = true;
    try {
      const k = await tools.transcriptTicket({ siteSlug: slug, id: t.id, query: { format: 'text' } });
      tvText = await fetchUrlText(k.url);
    } catch (e) {
      tvText = `Could not load the transcript: ${errMessage(e)}`;
    }
  }
  async function dlT(t: TranscriptMeta, format: 'cast' | 'text') {
    try {
      const k = await tools.transcriptTicket({ siteSlug: slug, id: t.id, query: { format } });
      await downloadUrl(k.url, `opm-${slug}-terminal-${t.id}.${format === 'cast' ? 'cast' : 'txt'}`);
    } catch (e) {
      toasts.err(e, 'Download failed');
    }
  }
  const execOff = $derived(info && !info.containerExec.enabled);
  const hostOff = $derived(info && !info.hostShell.enabled);
</script>


<div class="stack">
  {#if infoErr}
    <div class="banner bad"><Icon name="alert" /><div class="grow">{infoErr}</div></div>
  {/if}
  {#if info && execOff && hostOff}
    <div class="card card-pad">
      <Empty icon="lock" title="Terminals are turned off on {slug}" text="Both container exec and the host shell are disabled in the agent configuration of this site." compact>
        {#snippet detail()}<code class="cfg">{info?.configPath ?? '/etc/opm/agent.json'} → "tools": {'{'} "containerExec": true, "hostShell": true {'}'}</code>{/snippet}
      </Empty>
    </div>
  {:else}
    <section class="card new" data-testid="term-new">
      <div class="card-head">
        <h2>New session</h2>
        <span class="muted small" data-testid="term-count">{openOnSite} of {TERMINAL_LIMITS.maxSessions} open on {slug}</span>
      </div>
      <div class="card-body nb">
        {#if kind === 'container'}
          <div class="field">
            <span class="label">Container</span>
            <span class="row tight"><Icon name="container" size={14} /><strong class="mono" data-testid="term-container">{container}</strong>{#if cons.length && !ctrRunning}<span class="badge warn">not running</span>{/if}</span>
          </div>
          <div class="field">
            <span class="label">Shell</span>
            <SegmentedControl label="Shell" options={[{ value: 'sh', label: 'sh' }, { value: 'bash', label: 'bash' }, { value: 'custom', label: 'Custom' }]} value={shell} onchange={(v) => (shell = v as typeof shell)} testid="term-shell" />
          </div>
          {#if shell === 'custom'}
            <div class="field grow-f">
              <label class="label" for="t-cmd">Command</label>
              <input id="t-cmd" class="input mono" bind:value={custom} placeholder="/bin/ash" />
            </div>
          {/if}
          <div class="go"><Btn variant="primary" icon="terminal" busy={busy} disabled={!ctr || !ctrRunning || !!execOff || full} title={!ctrRunning ? 'The container is not running' : undefined} onclick={() => create()} data-testid="term-open">Open terminal</Btn></div>
        {:else}
          <div class="hostinfo small">
            <Icon name="shield" size={15} />
            <span>Runs <code>{info?.hostShell.shell ?? '/bin/bash'}</code> as <strong>{info?.hostShell.user ?? 'root'}</strong> on the host of <strong>{slug}</strong>. You confirm with the site name.</span>
          </div>
          <div class="go"><Btn variant="primary" icon="server" disabled={full || !!hostOff} onclick={() => ((typed = ''), (hostOpen = true))} data-testid="term-open-host">Open host shell…</Btn></div>
        {/if}
      </div>
      {#if info && (mode === 'container' ? execOff : hostOff)}
        <div class="off small" data-testid="tool-disabled">
          <Icon name="lock" size={14} />
          <span>{mode === 'container' ? 'Container exec' : 'The host shell'} is turned off on {slug}. To allow it, set <code>"tools": {'{'} "{mode === 'container' ? 'containerExec' : 'hostShell'}": true {'}'}</code> in <code>{info.configPath ?? '/etc/opm/agent.json'}</code> and restart the agent.</span>
        </div>
      {/if}
      <div class="rec faint xs"><Icon name="info" size={12} /> Output is recorded on this site for {TERMINAL_LIMITS.transcriptDays} days (never uploaded; known secret values are masked). Sessions close after {TERMINAL_LIMITS.idleTimeoutSec / 60} min without input.</div>
    </section>

    {#if tabs.length}
      <section class="term card">
        <div class="ttabs" role="tablist" aria-label="Terminal sessions">
          {#each tabs as t (t.sessionId)}
            <div class="ttab" class:on={t.sessionId === activeId} role="presentation">
              <button role="tab" aria-selected={t.sessionId === activeId} onclick={() => (activeId = t.sessionId)} data-testid="term-tab">
                <i class="d {t.conn}"></i><Icon name={t.kind === 'host' ? 'server' : 'container'} size={13} /><span class="mono">{t.target}</span>
              </button>
              <button class="x" aria-label="Close {t.target}" title={t.conn === 'open' ? 'End and close' : 'Close'} onclick={() => closeTab(t.sessionId)}><Icon name="x" size={12} /></button>
            </div>
          {/each}
        </div>
        {#each tabs as t (t.sessionId)}
          <TerminalPane bind:this={panes[t.sessionId]} wsPath={t.wsPath} target={t.target} site={t.site} kind={t.kind} active={t.sessionId === activeId} onstate={(s) => onTabState(t.sessionId, s)} />
        {/each}
      </section>
    {/if}
  {/if}

  <section class="card">
    <div class="card-head"><h2>Open sessions · {mode === 'host' ? `host of ${slug}` : container}</h2><Btn size="sm" icon="refresh" variant="ghost" onclick={loadSessions} aria-label="Refresh sessions" /></div>
    {#if sessions.length === 0}
      <div class="card-body"><p class="muted small" style="margin:0">No terminal is open {mode === 'host' ? 'on the host' : 'in this container'}.</p></div>
    {:else}
      <div class="table-wrap">
        <table class="table" data-testid="term-sessions">
          <thead><tr><th>Target</th><th>Person</th><th>Started</th><th>Last activity</th><th class="num">Output</th><th class="actions"></th></tr></thead>
          <tbody>
            {#each sessions as s (s.sessionId)}
              <tr>
                <td><span class="row tight"><Icon name={s.kind === 'host' ? 'server' : 'container'} size={14} /><span class="mono">{s.target}</span>{#if s.via}<span class="badge info" title="opened from site {s.via} over the mesh">via {s.via}</span>{/if}{#if !s.open}<span class="badge warn" title="reserved; the browser has not connected yet">connecting</span>{/if}</span></td>
                <td class="small">{s.actor ?? '—'}</td>
                <td class="small"><Time ts={s.startedAt} /></td>
                <td class="small"><Time ts={s.lastActivityAt} /></td>
                <td class="num small">{bytes(s.bytesOut)}</td>
                <td class="actions"><Btn size="xs" variant="danger" onclick={() => ((killFor = s), (killOpen = true))}>End</Btn></td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>

  <section class="card">
    <div class="card-head"><h2>Transcripts<span class="muted small">{' · '}last {TERMINAL_LIMITS.transcriptDays} days</span></h2><span class="muted small">kept on {slug} only, never uploaded</span></div>
    {#if transcripts === null}
      <div class="card-body"><span class="skel" style="height:80px"></span></div>
    {:else if transcripts.length === 0}
      <Empty icon="terminal" title="No transcripts" text="Every terminal session leaves a transcript here for {TERMINAL_LIMITS.transcriptDays} days." compact />
    {:else}
      <div class="table-wrap">
        <table class="table" data-testid="transcripts">
          <thead><tr><th>Target</th><th>Person</th><th>Started</th><th>Duration</th><th class="num">Size</th><th>Exit</th><th class="actions"></th></tr></thead>
          <tbody>
            {#each transcripts as t (t.id)}
              <tr>
                <td><span class="row tight"><Icon name={t.kind === 'host' ? 'server' : 'container'} size={14} /><span class="mono">{t.target}</span>{#if t.via}<span class="badge info">via {t.via}</span>{/if}</span><div class="tiny faint mono">{t.command.join(' ')}</div></td>
                <td class="small">{t.actor ?? '—'}</td>
                <td class="small"><Time ts={t.startedAt} /></td>
                <td class="small">{t.endedAt ? uptime(t.durationSec) : 'open'}</td>
                <td class="num small">{bytes(t.sizeBytes)}</td>
                <td class="small">{t.exitCode ?? '—'}</td>
                <td class="actions nowrap">
                  <Btn size="xs" icon="eye" onclick={() => view(t)} data-testid="transcript-view">View</Btn>
                  <Btn size="xs" variant="ghost" icon="download" onclick={() => dlT(t, 'text')} title="Plain text">.txt</Btn>
                  <Btn size="xs" variant="ghost" icon="download" onclick={() => dlT(t, 'cast')} title="asciicast v2 (replay with asciinema)">.cast</Btn>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>
</div>

<Modal bind:open={hostOpen} title="Open a host shell on {slug}?" size="sm" testid="host-confirm">
  <div class="stack tight small">
    <div class="banner warn small"><Icon name="alert" /><div class="grow">Runs as <strong>{info?.hostShell.user ?? 'root'}</strong> on <strong>{slug}</strong> — full control of the machine. Every keystroke's output is recorded for {TERMINAL_LIMITS.transcriptDays} days.</div></div>
    <label class="label" for="host-typed">Type the site name <code>{slug}</code> to continue</label>
    <input id="host-typed" class="input mono" bind:value={typed} autocomplete="off" spellcheck="false" data-testid="host-typed" onkeydown={(e) => e.key === 'Enter' && hostShellConfirmOk(slug, typed) && create(typed)} />
  </div>
  {#snippet footer()}
    <Btn variant="ghost" onclick={() => (hostOpen = false)}>Cancel</Btn>
    <Btn variant="danger" icon="server" busy={busy} disabled={!hostShellConfirmOk(slug, typed)} onclick={() => create(typed)} data-testid="host-go">Open host shell</Btn>
  {/snippet}
</Modal>

<Confirm bind:open={killOpen} title="End this session?" danger confirmLabel="End session" message="{killFor?.target} — {killFor?.actor ?? 'someone'}'s shell closes now (the transcript stays)." onconfirm={kill} />

<Drawer bind:open={tvOpen} wide title="Transcript · {tvFor?.target ?? ''}" subtitle={tvFor ? `${tvFor.actor ?? 'unknown'} · ${tvFor.command.join(' ')} · known secrets masked` : ''} testid="transcript-drawer">
  {#if tvText === null}
    <span class="skel" style="height:240px"></span>
  {:else}
    <AnsiLog text={tvText} toggles class="tv" testid="transcript-text" empty="" />
  {/if}
  {#snippet footer()}
    {#if tvFor}
      <Btn variant="ghost" icon="download" onclick={() => tvFor && dlT(tvFor, 'text')}>Download .txt</Btn>
      <Btn icon="download" onclick={() => tvFor && dlT(tvFor, 'cast')}>Download .cast</Btn>
    {/if}
  {/snippet}
</Drawer>

<style>
  .nb {
    display: flex;
    flex-wrap: wrap;
    gap: 14px 18px;
    align-items: flex-end;
  }
  .nb .field {
    margin: 0;
  }
  .nb :global(#t-ctr) {
    min-width: 260px;
  }
  .grow-f {
    flex: 1;
    min-width: 200px;
  }
  .go {
    margin-left: auto;
  }
  .hostinfo {
    display: flex;
    gap: 8px;
    align-items: center;
    color: var(--text-2);
    max-width: 520px;
  }
  .off {
    display: flex;
    gap: 8px;
    align-items: flex-start;
    margin: 0 16px 12px;
    padding: 8px 12px;
    background: var(--surface-2);
    border: 1px dashed var(--border-strong);
    border-radius: var(--r-md);
    color: var(--text-2);
  }
  .rec {
    display: flex;
    gap: 6px;
    align-items: center;
    padding: 0 16px 12px;
  }
  .cfg {
    display: block;
    font-size: 12px;
  }
  .term {
    overflow: hidden;
  }
  .ttabs {
    display: flex;
    gap: 2px;
    padding: 6px 8px 0;
    background: #1b1b20;
    overflow-x: auto;
  }
  .ttab {
    display: flex;
    align-items: center;
    border-radius: var(--r-md) var(--r-md) 0 0;
    color: #a1a1aa;
  }
  .ttab.on {
    background: var(--log-bg);
    color: #fafafa;
  }
  .ttab button {
    all: unset;
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 7px 4px 7px 12px;
    font-size: var(--fs-sm);
  }
  .ttab .x {
    padding: 7px 10px 7px 6px;
    opacity: 0.6;
  }
  .ttab .x:hover {
    opacity: 1;
  }
  .ttab button:focus-visible {
    outline: 2px solid var(--accent);
  }
  .d {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: #52525b;
  }
  .d.open {
    background: #4ade80;
  }
  .d.connecting {
    background: #fbbf24;
  }
  .d.error {
    background: #f87171;
  }
  .row.tight {
    gap: 6px;
  }
  :global(.logbox.tv) {
    max-height: none;
    min-height: 50vh;
  }
</style>
