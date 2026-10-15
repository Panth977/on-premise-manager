<script lang="ts">
  /**
   * Quick actions of one live container (round T; nav2: the links open the tabs of the container page) — Logs / Terminal /
   * Monitoring + Restart / Stop / Start (confirm modal, session + actor). Used by Docker › Containers, the container page
   * and the Status page. Failed copies: read-only.
   */
  import type { ToolsContainer } from '@opm/shared/agent-tools';
  import Icon from '$lib/ui/Icon.svelte';
  import Confirm from '$lib/ui/Confirm.svelte';
  import { toasts } from '$lib/state/toast.svelte';
  import { tools } from './api';
  import { appHref } from '$lib/nav';

  type C = Pick<ToolsContainer, 'ref' | 'state' | 'failedCopy' | 'expectedRunning' | 'manuallyStopped'> & { siteSlug: string };
  let {
    c,
    links = true,
    power = true,
    onchanged,
    size = 'xs',
  }: { c: C; links?: boolean; power?: boolean; onchanged?: (c: ToolsContainer) => void; size?: 'xs' | 'sm' } = $props();

  let act = $state<'restart' | 'stop' | 'start' | null>(null);
  let open = $state(false);
  const running = $derived(c.state === 'running' || c.state === 'restarting');
  const LABEL = { restart: 'Restart', stop: 'Stop', start: 'Start' } as const;
  const tabHref = (tab: string) => appHref(c.siteSlug, `containers/${encodeURIComponent(c.ref)}?tab=${tab}`);
  function ask(a: 'restart' | 'stop' | 'start') {
    act = a;
    open = true;
  }
  async function run() {
    if (!act) return;
    const a = act;
    try {
      const args = { siteSlug: c.siteSlug, ref: c.ref, body: {} };
      const r = a === 'restart' ? await tools.restartContainer(args) : a === 'stop' ? await tools.stopContainer(args) : await tools.startContainer(args);
      toasts.ok(`${c.ref} ${a === 'stop' ? 'stopped' : a === 'start' ? 'started' : 'restarted'}`, a === 'stop' ? 'Marked "stopped manually" until you start it or the next update.' : undefined);
      onchanged?.(r.container);
    } catch (e) {
      toasts.err(e, `${LABEL[a]} failed`);
      return false;
    }
  }
</script>

<span class="qa" data-testid="qa-{c.ref}">
  {#if links}
    <a class="btn ghost icon {size}" href={tabHref('logs')} title="Logs" aria-label="Logs of {c.ref}" data-testid="qa-logs"><Icon name="logs" size={14} /></a>
    {#if !c.failedCopy}
      <a class="btn ghost icon {size}" class:off={!running} href={running ? tabHref('terminal') : undefined} aria-disabled={!running} title={running ? 'Terminal' : 'Terminal (not running)'} aria-label="Terminal in {c.ref}" data-testid="qa-terminal"><Icon name="terminal" size={14} /></a>
      <a class="btn ghost icon {size}" href={tabHref('monitoring')} title="Monitoring" aria-label="Monitoring of {c.ref}" data-testid="qa-monitoring"><Icon name="chart" size={14} /></a>
    {/if}
  {/if}
  {#if links}
    <a class="btn ghost icon {size}" href={tabHref('inspect')} title="Inspect" aria-label="Inspect {c.ref}" data-testid="qa-inspect"><Icon name="search" size={14} /></a>
  {/if}
  {#if power && !c.failedCopy}
    <span class="sep" aria-hidden="true"></span>
    <button class="btn ghost icon {size}" onclick={() => ask('restart')} title="Restart" aria-label="Restart {c.ref}" data-testid="qa-restart"><Icon name="rotate" size={14} /></button>
    {#if running}
      <button class="btn ghost icon {size}" onclick={() => ask('stop')} title="Stop" aria-label="Stop {c.ref}" data-testid="qa-stop"><Icon name="stop" size={14} /></button>
    {:else}
      <button class="btn ghost icon {size}" onclick={() => ask('start')} title="Start" aria-label="Start {c.ref}" data-testid="qa-start"><Icon name="play" size={14} /></button>
    {/if}
  {/if}
</span>

<Confirm bind:open title="{act ? LABEL[act] : ''} {c.ref}?" confirmLabel={act ? LABEL[act] : 'OK'} danger={act === 'stop'} onconfirm={run} testid="container-action-confirm">
  <div class="stack tight small">
    {#if act === 'restart'}
      <p>Docker restarts <strong class="mono">{c.ref}</strong> on site <strong>{c.siteSlug}</strong> (stop timeout of the image, then start). Open connections are dropped.</p>
    {:else if act === 'stop'}
      <p>Docker stops <strong class="mono">{c.ref}</strong> on site <strong>{c.siteSlug}</strong>. Routes to it answer 503 until it runs again.</p>
      {#if c.expectedRunning}
        <div class="banner warn small" data-testid="expected-running-warning"><Icon name="alert" /><div class="grow">The health system expects this container running; it will show as <strong>stopped manually</strong>, not failed, until you start it or the next update.</div></div>
      {/if}
    {:else if act === 'start'}
      <p>Docker starts <strong class="mono">{c.ref}</strong> on site <strong>{c.siteSlug}</strong>{c.manuallyStopped ? ' and clears "stopped manually"' : ''}.</p>
    {/if}
    <p class="faint xs">Written to Activities with your name.</p>
  </div>
</Confirm>

<style>
  .qa {
    display: inline-flex;
    align-items: center;
    gap: 1px;
    white-space: nowrap;
  }
  .sep {
    width: 1px;
    height: 14px;
    background: var(--border);
    margin: 0 3px;
  }
  a.off {
    opacity: 0.4;
    pointer-events: none;
  }
</style>
