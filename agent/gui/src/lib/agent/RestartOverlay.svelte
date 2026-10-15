<script lang="ts">
  /** Blocking "Restarting the agent…" overlay (round I): polls /_opm/api/health every 2 s (≤ 120 s) — see restart.svelte.ts. */
  import Modal from '$lib/ui/Modal.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Progress from '$lib/ui/Progress.svelte';
  import { session } from '$lib/state/session.svelte';
  import { restart, RESTART_MAX_MS } from './restart.svelte';

  const elapsed = $derived.by(() => {
    void session.tick;
    return restart.active ? Math.max(0, Math.round((Date.now() - restart.startedMs) / 1000)) : 0;
  });
</script>

<Modal open={restart.active} title={restart.title} closable={false} size="sm" testid="restart-overlay">
  <div class="stack">
    {#if restart.timedOut}
      <div class="banner bad" data-testid="restart-timeout">
        <Icon name="alert" />
        <div class="grow">
          <strong>The agent did not come back within 2 minutes.</strong>
          <p class="small" style="margin:4px 0 0">On the server: <code>sudo systemctl status opm-agent</code> and <code>sudo journalctl -u opm-agent -n 100</code>. After an upgrade the boot guard restores <code>opm-agent.prev</code> at the next start if the new binary never became healthy.</p>
        </div>
      </div>
    {:else}
      <div class="row hd">
        <span class="spin big" aria-hidden="true"></span>
        <div class="grow">
          {#if restart.detail}<p style="margin:0">{restart.detail}</p>{/if}
          <p class="small muted" style="margin:2px 0 0">The containers keep running. You stay logged in — this page continues by itself when the agent answers again.</p>
        </div>
      </div>
      <Progress value={elapsed / (RESTART_MAX_MS / 1000)} label="Waiting for the agent" />
      <div class="row between small muted">
        <span data-testid="restart-status">{restart.sawDown ? 'The agent stopped · waiting for it to answer' : 'Waiting for the agent to stop'}{restart.lastNote ? ` — ${restart.lastNote}` : ''}</span>
        <span class="mono nowrap">{elapsed} s · check {restart.attempts}</span>
      </div>
    {/if}
  </div>
  {#snippet footer()}
    {#if restart.timedOut}
      <Btn onclick={() => restart.dismiss()}>Close</Btn>
      <Btn onclick={() => location.reload()}>Reload page</Btn>
      <Btn variant="primary" icon="refresh" onclick={() => restart.retry()} data-testid="restart-retry">Keep waiting</Btn>
    {:else}
      <span class="faint small">Checks GET /_opm/api/health every 2 s · up to 2 minutes</span>
    {/if}
  {/snippet}
</Modal>

<style>
  .hd {
    align-items: flex-start;
    flex-wrap: nowrap;
  }
  .spin.big {
    width: 22px;
    height: 22px;
    border-width: 2.5px;
    flex: none;
    margin-top: 2px;
    color: var(--accent);
  }
</style>
