<script lang="ts">
  /**
   * One terminal session: xterm.js ↔ WebSocket (binary frames = bytes, text frames = JSON control). Toolbar: copy,
   * paste, font size, fit, fullscreen, end session. Idle warning banner from the agent (30 min idle timeout).
   */
  import { onDestroy, tick } from 'svelte';
  import type { TerminalControl } from '@opm/shared/agent-tools';
  import Icon from '$lib/ui/Icon.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import { toasts } from '$lib/state/toast.svelte';
  import { session } from '$lib/state/session.svelte';
  import { openTerminalSocket } from './api';
  import { loadXterm, TERM_THEME, type XTerminal, type XFitAddon } from './xterm';

  export type ConnState = 'connecting' | 'open' | 'closed' | 'error';
  let {
    wsPath,
    target,
    site,
    kind,
    active,
    onstate,
    onend,
  }: { wsPath: string; target: string; site: string; kind: 'container' | 'host'; active: boolean; onstate?: (s: ConnState) => void; onend?: () => void } = $props();

  let host = $state<HTMLDivElement | null>(null);
  let wrap = $state<HTMLDivElement | null>(null);
  let conn = $state<ConnState>('connecting');
  let reason = $state<string | null>(null);
  let idleLeft = $state<number | null>(null);
  let fontSize = $state(13);
  let term: XTerminal | null = null;
  let fit: XFitAddon | null = null;
  let ws: WebSocket | null = null;
  let pingT: ReturnType<typeof setInterval> | null = null;
  let ro: ResizeObserver | null = null;
  const enc = new TextEncoder();

  function setConn(s: ConnState) {
    conn = s;
    onstate?.(s);
  }
  function sendCtl(v: TerminalControl) {
    if (ws && ws.readyState === 1) ws.send(JSON.stringify(v));
  }
  function doFit() {
    if (!term || !fit || !host || host.offsetParent === null) return;
    try {
      fit.fit();
      sendCtl({ type: 'resize', cols: term.cols, rows: term.rows });
    } catch {
      /* not visible yet */
    }
  }
  async function init() {
    let X;
    try {
      X = await loadXterm();
    } catch {
      setConn('error');
      reason = 'The terminal component (xterm.js) did not load.';
      return;
    }
    if (!host) return;
    term = new X.Terminal({ fontFamily: "'JetBrains Mono Variable', ui-monospace, Menlo, monospace", fontSize, cursorBlink: true, scrollback: 5000, theme: TERM_THEME, allowProposedApi: false, convertEol: false });
    fit = new X.FitAddon();
    term.loadAddon(fit);
    term.open(host);
    await tick();
    doFit();
    ws = openTerminalSocket(wsPath);
    ws.onopen = () => {
      setConn('open');
      doFit();
      term?.focus();
      pingT = setInterval(() => sendCtl({ type: 'ping' }), 25_000);
    };
    ws.onmessage = (e: MessageEvent) => {
      if (typeof e.data === 'string') {
        let c: TerminalControl;
        try {
          c = JSON.parse(e.data) as TerminalControl;
        } catch {
          return;
        }
        if (c.type === 'idle-warning') idleLeft = c.secondsLeft;
        else if (c.type === 'exit') reason = c.reason || `exited (${c.code ?? '—'})`;
        else if (c.type === 'error') {
          reason = c.message;
          term?.write(`\r\n\x1b[31m${c.message}\x1b[0m\r\n`);
        }
        return;
      }
      idleLeft = null;
      term?.write(new Uint8Array(e.data as ArrayBuffer));
    };
    ws.onerror = () => {
      if (conn !== 'connecting') return;
      // §21 #118 f: the cloud carries requests, not WebSockets (yet)
      reason = session.session?.remote
        ? 'The terminal does not work through the cloud yet: open the agent GUI on the site itself (its own address) for a shell.'
        : 'The agent refused the connection (ticket expired or used, or the session limit).';
    };
    ws.onclose = (e: CloseEvent) => {
      if (pingT) clearInterval(pingT);
      if (!reason) reason = e.reason || (e.code === 1000 ? 'session ended' : `connection closed (${e.code})`);
      setConn(conn === 'connecting' ? 'error' : 'closed');
      term?.write(`\r\n\x1b[2m— ${reason} —\x1b[0m\r\n`);
    };
    term.onData((d) => {
      idleLeft = null;
      if (ws && ws.readyState === 1) ws.send(enc.encode(d));
    });
    ro = new ResizeObserver(() => doFit());
    ro.observe(host);
  }
  $effect(() => {
    if (host && !term) init();
  });
  $effect(() => {
    if (active) tick().then(() => (doFit(), term?.focus()));
  });
  $effect(() => {
    if (term) {
      term.options.fontSize = fontSize;
      doFit();
    }
  });
  export function end() {
    ws?.close(1000, 'closed by the operator');
    onend?.();
  }
  async function copy() {
    const s = term?.getSelection() ?? '';
    if (!s) return toasts.info('Select text in the terminal first');
    try {
      await navigator.clipboard.writeText(s);
      toasts.ok('Copied');
    } catch {
      toasts.push('bad', 'Copy failed', 'The browser refused clipboard access (needs https or localhost).');
    }
  }
  async function paste() {
    try {
      const t = await navigator.clipboard.readText();
      if (t && ws && ws.readyState === 1) ws.send(enc.encode(t));
      term?.focus();
    } catch {
      toasts.push('bad', 'Paste failed', 'Use Ctrl/Cmd + V inside the terminal instead.');
    }
  }
  function fullscreen() {
    if (document.fullscreenElement) document.exitFullscreen();
    else wrap?.requestFullscreen?.().then(() => setTimeout(doFit, 100));
  }
  onDestroy(() => {
    if (pingT) clearInterval(pingT);
    ro?.disconnect();
    ws?.close(1000, 'page closed');
    term?.dispose();
  });
</script>

<div class="pane" class:hidden={!active} bind:this={wrap} data-testid="terminal-pane">
  <div class="tb">
    <span class="tgt">
      <Icon name={kind === 'host' ? 'server' : 'container'} size={14} />
      <strong class="mono">{target}</strong><span class="faint">on {site}</span>
      <span class="st {conn}" data-testid="term-state">{conn === 'open' ? 'connected' : conn === 'connecting' ? 'connecting…' : conn === 'closed' ? 'ended' : 'failed'}</span>
    </span>
    <span class="grow"></span>
    <button class="btn ghost icon xs" onclick={copy} title="Copy selection" aria-label="Copy selection"><Icon name="copy" size={14} /></button>
    <button class="btn ghost icon xs" onclick={paste} title="Paste" aria-label="Paste" disabled={conn !== 'open'}><Icon name="paste" size={14} /></button>
    <span class="sep"></span>
    <button class="btn ghost icon xs" onclick={() => (fontSize = Math.max(9, fontSize - 1))} title="Smaller text" aria-label="Smaller text"><Icon name="minus" size={14} /></button>
    <span class="fs mono">{fontSize}</span>
    <button class="btn ghost icon xs" onclick={() => (fontSize = Math.min(24, fontSize + 1))} title="Bigger text" aria-label="Bigger text"><Icon name="plus" size={14} /></button>
    <button class="btn ghost xs fit" onclick={doFit} title="Fit the terminal to the window">Fit</button>
    <button class="btn ghost icon xs" onclick={fullscreen} title="Full screen" aria-label="Full screen"><Icon name="maximize" size={14} /></button>
    <span class="sep"></span>
    <Btn size="xs" variant="danger" icon="power" onclick={end} disabled={conn === 'closed' || conn === 'error'} data-testid="term-end">End</Btn>
  </div>
  {#if idleLeft !== null}
    <div class="banner warn small idle" data-testid="idle-warning"><Icon name="clock" /><div class="grow">No input for a while — this session closes in {Math.ceil(idleLeft / 60)} min. Type anything to keep it.</div></div>
  {/if}
  {#if (conn === 'closed' || conn === 'error') && reason}
    <div class="banner {conn === 'error' ? 'bad' : 'info'} small idle"><Icon name={conn === 'error' ? 'alert' : 'info'} /><div class="grow">{reason}</div></div>
  {/if}
  <div class="xt" bind:this={host} data-testid="xterm"></div>
</div>

<style>
  .pane {
    display: flex;
    flex-direction: column;
    background: var(--log-bg);
    border-radius: 0 0 var(--r-lg) var(--r-lg);
    overflow: hidden;
    min-height: 0;
  }
  .pane.hidden {
    display: none;
  }
  .pane:fullscreen {
    border-radius: 0;
  }
  .tb {
    display: flex;
    align-items: center;
    gap: 2px;
    padding: 6px 10px;
    border-bottom: 1px solid #26262c;
    color: #d4d4d8;
    flex-wrap: wrap;
  }
  .tb .btn.ghost {
    color: #a1a1aa;
  }
  .tb .btn.ghost:hover:not(:disabled) {
    background: #26262c;
    color: #fafafa;
  }
  .tgt {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    font-size: var(--fs-sm);
    min-width: 0;
  }
  .tgt .faint {
    color: #71717a;
  }
  .st {
    font-size: 11px;
    padding: 1px 8px;
    border-radius: var(--r-full);
    background: #26262c;
    color: #a1a1aa;
  }
  .st.open {
    background: rgba(74, 222, 128, 0.14);
    color: #86efac;
  }
  .st.error {
    background: rgba(248, 113, 113, 0.14);
    color: #fca5a5;
  }
  .sep {
    width: 1px;
    height: 16px;
    background: #2e2e35;
    margin: 0 4px;
  }
  .fs {
    font-size: 11px;
    color: #a1a1aa;
    min-width: 18px;
    text-align: center;
  }
  .idle {
    margin: 8px 10px 0;
  }
  .xt {
    height: min(58vh, 480px);
    padding: 8px 4px 4px 10px;
  }
  .pane:fullscreen .xt {
    height: calc(100vh - 48px);
  }
  .xt :global(.xterm) {
    height: 100%;
  }
  .xt :global(.xterm-viewport) {
    background: transparent !important;
  }
</style>
