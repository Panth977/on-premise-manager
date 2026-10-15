<script lang="ts">
  /**
   * §21 #110 the update lock is ADVISORY: a running update / rollback never freezes the GUI. This bar says so and lets the
   * person stop it on every site of its plan (POST updates/{id}/terminate): just stop · stop + roll back to the pre-update
   * backup · stop + restart every agent + roll back. Per-site results come back as a toast.
   */
  import type { UpdateLock } from '@opm/shared/types';
  import type { TerminateUpdateRequest, TerminateUpdateResponse } from '@opm/shared/agent-self';
  import { api, errMessage } from '$lib/api';
  import { overview } from '$lib/state/app.svelte';
  import { toasts } from '$lib/state/toast.svelte';
  import { agentAll } from '$lib/agent/state.svelte';
  import { restart } from '$lib/agent/restart.svelte';
  import Btn from './Btn.svelte';
  import Icon from './Icon.svelte';
  import Time from './Time.svelte';
  import Confirm from './Confirm.svelte';

  let { lock, href }: { lock: UpdateLock | null; href: string } = $props();

  type Mode = 'stop' | 'rollback' | 'restart';
  let mode = $state<Mode | null>(null);
  let confirmOpen = $state(false);
  let results = $state<TerminateUpdateResponse | null>(null);

  const isRollback = $derived(lock?.kind === 'rollback');
  /** a merge / commit run is an update too (kind merge) */
  const what = $derived(isRollback ? 'rollback' : 'update');
  const title = $derived(mode === 'stop' ? `Stop the ${what}?` : mode === 'rollback' ? `Stop the ${what} and roll back?` : `Restart every agent and ${isRollback ? 'stop the rollback' : 'roll back'}?`);
  const confirmLabel = $derived(mode === 'stop' ? `Stop ${what}` : mode === 'rollback' ? 'Stop & roll back' : isRollback ? 'Stop & restart agents' : 'Stop, restart & roll back');

  function ask(m: Mode) {
    mode = m;
    confirmOpen = true;
  }

  async function go() {
    const m = mode;
    const id = lock?.updateId;
    if (!m || !id) return false;
    const body: TerminateUpdateRequest = m === 'stop' ? {} : m === 'rollback' ? { rollback: true } : isRollback ? { restartAgent: true } : { rollback: true, restartAgent: true };
    const prevStartedAt = agentAll.data?.find((x) => x.self)?.startedAt ?? null;
    let r: TerminateUpdateResponse;
    try {
      r = await api.terminateUpdate({ updateId: id, body });
    } catch (e) {
      toasts.err(e, `Could not stop the ${what}`);
      return false;
    }
    results = r;
    const bad = r.sites.filter((s) => !s.ok);
    const lines = r.sites.map((s) => `${s.siteSlug}: ${s.message}`).join(' · ');
    toasts.push(bad.length ? 'warn' : 'ok', bad.length ? `Stopped with problems on ${bad.map((s) => s.siteSlug).join(', ')}` : m === 'stop' ? `The ${what} was stopped on every site` : m === 'rollback' ? 'Stopped — rolling back to the pre-update backup' : 'Stopped — the agents restart now', lines, bad.length ? 0 : 8000);
    if (m === 'restart') {
      // this agent restarts too: the overlay waits for it and the page continues on the same session
      void restart
        .wait({ site: overview.data?.self ?? '', prevStartedAt, title: 'Restarting the agents…', detail: isRollback ? 'Every agent restarts after the rollback was stopped. This page reconnects when this agent answers again.' : 'Every agent restarts, then each site rolls back to its pre-update backup. This page reconnects when this agent answers again.' })
        .then((res) => {
          if (res === 'ok') {
            toasts.ok('The agent is back', isRollback ? 'same session' : 'same session · the rollback runs now on every site');
            overview.load();
            agentAll.load();
          }
        });
    } else overview.load();
    return true;
  }
</script>

{#if lock}
<div class="lockbar" role="status" data-testid="lock-banner" data-kind={lock.kind}>
  <span class="spin" aria-hidden="true"></span>
  <span class="grow txt">
    {isRollback ? 'A rollback' : 'An update'} is running on all sites (coordinator <strong class="mono">{lock.coordinator}</strong>, started <Time ts={lock.startedAt} />).
    <span class="muted">You can keep working — starting another update stops this one.</span>
  </span>
  <span class="acts">
    <Btn size="sm" icon="stop" onclick={() => ask('stop')} data-testid="lock-stop">Stop {what}</Btn>
    {#if !isRollback}<Btn size="sm" icon="history" onclick={() => ask('rollback')} data-testid="lock-stop-rollback">Stop &amp; roll back</Btn>{/if}
    <Btn size="sm" variant="ghost" icon="rotate" onclick={() => ask('restart')} data-testid="lock-stop-restart">{isRollback ? 'Stop & restart agents' : 'Stop, restart agents & roll back'}</Btn>
    <a class="btn ghost sm" {href} data-testid="lock-view">View progress <Icon name="chevronRight" size={14} /></a>
  </span>
</div>
{/if}
{#if results}
  <div class="lockres" data-testid="lock-results">
    <span class="strong nowrap">Stop {results.updateId.slice(-6)}:</span>
    <ul>
      {#each results.sites as s (s.siteSlug)}
        <li class:bad={!s.ok}><Icon name={s.ok ? 'check' : 'x'} size={13} /><span class="mono strong">{s.siteSlug}</span> <span class="muted">{s.message}</span></li>
      {/each}
    </ul>
    <button class="btn ghost icon sm" aria-label="Dismiss the results" onclick={() => (results = null)}><Icon name="x" size={14} /></button>
  </div>
{/if}

<Confirm bind:open={confirmOpen} {title} {confirmLabel} danger onconfirm={go} testid="lock-confirm">
  {#if mode === 'stop'}
    <p>Stops the {what} on <strong>every site</strong> now.</p>
    <ul class="small">
      <li>Nothing is rolled back: the containers stay as they are — some may be half-updated (new version on one site, old on another).</li>
      <li>You can then commit, merge or roll back to a backup as usual.</li>
    </ul>
  {:else if mode === 'rollback'}
    <p>Stops the {what} on <strong>every site</strong>, then each site rolls back to its <strong>pre-update backup</strong> (the configuration and the volumes of that backup).</p>
    <ul class="small">
      <li>Data written to those volumes since the backup is replaced by the backup.</li>
      <li>The rollback shows here as a running rollback; it can be stopped the same way.</li>
    </ul>
  {:else if mode === 'restart'}
    {#if isRollback}
      <p>Stops the rollback and <strong>restarts every agent</strong>. Use it when the agent itself seems stuck.</p>
    {:else}
      <p><strong>Restarts every agent</strong>, then each site rolls back to its <strong>pre-update backup</strong> (configuration + volumes). Use it when the agent itself seems stuck.</p>
    {/if}
    <ul class="small">
      <li>Containers keep running while the agents restart.</li>
      <li>This page loses the connection for a moment and reconnects when the agent answers again.</li>
    </ul>
  {/if}
  <p class="faint tiny" style="margin:0">Any site can stop it; the result of every site is shown afterwards.</p>
</Confirm>

<style>
  .lockbar {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px 10px;
    padding: 8px 16px;
    background: var(--amber-soft);
    color: var(--text);
    border-bottom: 1px solid color-mix(in srgb, var(--amber) 30%, transparent);
    font-size: var(--fs-md);
  }
  .lockbar .spin {
    color: var(--amber-text);
  }
  .txt {
    min-width: min(100%, 320px);
  }
  .acts {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: center;
  }
  .lockres {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    padding: 6px 16px;
    background: var(--surface-2);
    border-bottom: 1px solid var(--border);
    font-size: var(--fs-sm);
  }
  .lockres ul {
    list-style: none;
    margin: 0;
    padding: 0;
    flex: 1;
    display: flex;
    flex-wrap: wrap;
    gap: 4px 16px;
  }
  .lockres li {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    color: var(--green-text);
  }
  .lockres li.bad {
    color: var(--red-text);
  }
</style>
