<script lang="ts">
  import type { SetupResponse } from '@opm/shared';
  import { api, errCode } from '$lib/api';
  import { toast } from '$lib/toast.svelte';
  import { personName } from '$lib/util/people';
  import Button from '$lib/ui/Button.svelte';
  import ConfirmDialog from '$lib/ui/ConfirmDialog.svelte';
  import RelTime from '$lib/ui/RelTime.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import { Clock, ListOrdered, Package, Rocket, X } from '$lib/ui/icons';
  import UpgradeProgress from './UpgradeProgress.svelte';
  import ReleaseNotes from './ReleaseNotes.svelte';
  import { RUN_SITE_LABEL, RUN_SITE_TONE, RUN_TONE, upgradeState } from './upgrade';

  /**
   * §21 #90 deployment level: the pinned agent release (+ "What's new"), how many sites are below it, "Upgrade all"
   * (typed UPGRADE, sequential, stops on the first failure, never automatic) and the run panel.
   */
  let { deploymentId, data, canWrite, onchanged }: { deploymentId: string; data: SetupResponse; canWrite: boolean; onchanged: () => void } = $props();

  const rel = $derived(data.agentRelease);
  const run = $derived(data.agentUpgradeRun);
  const running = $derived(run?.status === 'running');
  const enrolled = $derived(data.sites.filter((s) => s.agent && s.upgrade));
  const below = $derived(enrolled.filter((s) => s.upgrade!.available));
  const eligible = $derived(below.filter((s) => s.upgrade!.supported).sort((a, b) => a.siteSlug.localeCompare(b.siteSlug)));
  const skipped = $derived(below.filter((s) => !s.upgrade!.supported).sort((a, b) => a.siteSlug.localeCompare(b.siteSlug)));
  const current = $derived(enrolled.filter((s) => !s.upgrade!.available));
  /** a single-site request / upgrade is going → the backend refuses a run (conflict) */
  const singleBusy = $derived(enrolled.find((s) => ['queued', 'running'].includes(upgradeState(s.upgrade)) && !(s.upgrade!.request?.runId ?? s.upgrade!.progress?.runId)));

  let allOpen = $state(false);
  let cancelOpen = $state(false);

  async function upgradeAll() {
    try {
      await api.upgradeAllAgents({ deploymentId, body: { version: rel!.version, confirm: 'UPGRADE' } });
      toast.success('Upgrade all started', `${eligible.length} ${eligible.length === 1 ? 'site' : 'sites'}, one at a time.`);
    } catch (e) {
      const c = errCode(e);
      if (c === 'stale' || c === 'conflict') {
        toast.error(e, c === 'stale' ? 'The pinned release moved — the page was reloaded' : 'Could not start the run');
        onchanged();
        return;
      }
      throw e;
    }
    onchanged();
  }
  async function cancelRun() {
    try {
      await api.cancelAgentUpgradeRun({ deploymentId });
      toast.success('Upgrade all cancelled', 'A site that already started its upgrade finishes it.');
    } catch (e) {
      if (errCode(e) !== 'conflict') throw e;
      toast.error(e, 'The run already ended');
    }
    onchanged();
  }
  const siteOf = (slug: string) => data.sites.find((s) => s.siteSlug === slug) ?? null;
</script>

<section class="card rel" data-testid="agent-release-panel">
  <div class="ri"><Icon icon={Package} size={20} /></div>
  <div class="grow" style="min-width:0">
    <div class="row between wrap" style="gap:8px">
      <h3>Agent release</h3>
      {#if canWrite && rel}
        <Button size="sm" icon={Rocket} variant={eligible.length && !running ? 'primary' : 'default'} disabled={!eligible.length || running || !!singleBusy} tip={running ? 'A run is going' : singleBusy ? `Wait for the upgrade of ${singleBusy.siteSlug} first` : !eligible.length ? 'No site can be upgraded from here' : undefined} onclick={() => (allOpen = true)} data-testid="upgrade-all">Upgrade all…</Button>
      {/if}
    </div>
    {#if rel}
      <p class="small muted">
        Pinned release <b class="mono" data-testid="release-version">{rel.version}</b> — what “Upgrade agent” and new installs get.
        <span data-testid="below-count">{#if below.length}<b class="text-amber">{below.length} of {enrolled.length} {enrolled.length === 1 ? 'site' : 'sites'} {below.length === 1 ? 'is' : 'are'} below it</b>{#if skipped.length}{' '}({skipped.length} need{skipped.length === 1 ? 's' : ''} the install command once){/if}.{:else if enrolled.length}Every enrolled site runs it.{:else}No site is enrolled yet.{/if}</span>
      </p>
      <ReleaseNotes notes={rel.notes} version={rel.version} testid="whats-new" />
    {:else}
      <p class="small muted" data-testid="release-version">No agent release is pinned in the cloud yet.</p>
    {/if}

    {#if run}
      <div class="run" data-testid="upgrade-run" data-status={run.status}>
        <div class="row between wrap" style="gap:8px">
          <span class="small row wrap" style="gap:6px"><Icon icon={ListOrdered} size={14} /><b>Upgrade all → <span class="mono">{run.version}</span></b><span class="badge {RUN_TONE[run.status]}" data-testid="run-status">{run.status}</span></span>
          <span class="xs faint">{personName(run.requestedBy)} · <RelTime ts={run.requestedAt} />{#if run.finishedAt}{' '}· ended <RelTime ts={run.finishedAt} />{/if}</span>
        </div>
        <ol class="rs">
          {#each run.sites as x, i (x.siteSlug)}
            {@const s = siteOf(x.siteSlug)}
            {@const live = (x.status === 'requested' || x.status === 'running') && s?.upgrade}
            <li data-testid="run-site" data-site={x.siteSlug} data-status={x.status}>
              <div class="row wrap rl">
                <span class="n xs faint">{i + 1}</span>
                <span class="mono strong small">{x.siteSlug}</span>
                {#if x.from}<span class="xs muted mono">{x.from}{#if x.status === 'done'}{' '}→ {run.version}{/if}</span>{/if}
                <span class="badge {RUN_SITE_TONE[x.status]}" data-testid="run-site-status">{RUN_SITE_LABEL[x.status]}</span>
                {#if x.at}<span class="xs faint"><RelTime ts={x.at} /></span>{/if}
              </div>
              {#if x.error}<div class="xs {x.status === 'skipped' ? 'muted' : 'text-red'} err" data-testid="run-site-error">{x.error}</div>{/if}
              {#if live}
                {@const v = s!.upgrade!}
                <div class="cur">
                  {#if v.progress && v.progress.requestId === x.requestId}
                    <UpgradeProgress progress={v.progress} testid="run-progress" />
                  {:else}
                    <div class="xs muted row"><Icon icon={Clock} size={13} />Queued — the agent picks it up on its next poll (≤ ~30 s).</div>
                  {/if}
                </div>
              {/if}
            </li>
          {/each}
        </ol>
        {#if running && canWrite}
          <div><Button size="sm" variant="danger-outline" icon={X} onclick={() => (cancelOpen = true)} data-testid="run-cancel">Cancel run</Button></div>
        {:else if run.status === 'failed'}
          <div class="xs muted">The run stopped at the first failure; the remaining sites were not touched. Fix the cause, then start “Upgrade all” again.</div>
        {/if}
      </div>
    {/if}
  </div>
</section>

<ConfirmDialog bind:open={allOpen} title="Upgrade every site to {rel?.version}?" confirmLabel="Upgrade all" typed="UPGRADE" disabled={!eligible.length} onconfirm={upgradeAll} testid="upgrade-all-confirm">
  {#snippet body()}
    <div class="stack tight">
      <p class="small muted">One site at a time, in this order. The next site starts only when the previous one is done; the first failure stops the run. Each agent restarts once (containers keep running) and rolls back by itself when it is not healthy within 60 s. Never automatic.</p>
      <div class="xs faint strong up">Order</div>
      <ol class="ord" data-testid="upgrade-all-order">
        {#each eligible as s (s.siteSlug)}
          <li data-testid="order-site" data-site={s.siteSlug}><span class="mono strong">{s.siteSlug}</span> <span class="xs muted mono">{s.upgrade!.installed} → {rel?.version}</span>{#if s.agent && !s.agent.online} <span class="xs text-amber">offline — waits for it</span>{/if}</li>
        {:else}
          <li class="faint small">No site can be upgraded from here.</li>
        {/each}
      </ol>
      {#if skipped.length}
        <div class="xs faint strong up">Skipped</div>
        <ul class="ord" data-testid="upgrade-all-skipped">
          {#each skipped as s (s.siteSlug)}
            <li data-testid="skipped-site" data-site={s.siteSlug}><span class="mono strong">{s.siteSlug}</span> <span class="xs muted mono">{s.upgrade!.installed}</span> — <span class="xs">{s.upgrade!.hint ?? 'agent too old to upgrade itself'}</span></li>
          {/each}
        </ul>
      {/if}
      {#if current.length}
        <div class="xs faint" data-testid="upgrade-all-current">Already on {rel?.version}: {current.map((s) => s.siteSlug).join(', ')}</div>
      {/if}
      <ReleaseNotes notes={rel?.notes} version={rel?.version ?? ''} summary="Release notes" testid="upgrade-all-notes" />
    </div>
  {/snippet}
</ConfirmDialog>
<ConfirmDialog bind:open={cancelOpen} title="Cancel “Upgrade all”?" message="Sites that have not started are not upgraded. A site that already started its upgrade finishes it." confirmLabel="Cancel run" cancelLabel="Keep running" danger onconfirm={cancelRun} testid="run-cancel-confirm" />

<style>
  .rel {
    display: flex;
    gap: 14px;
    padding: 18px 20px;
    align-items: flex-start;
    margin-bottom: var(--sp-4);
  }
  .ri {
    width: 40px;
    height: 40px;
    border-radius: 10px;
    background: var(--accent-soft);
    color: var(--accent-text);
    display: grid;
    place-items: center;
    flex: none;
  }
  .rel p {
    margin: 4px 0 8px;
  }
  .run {
    margin-top: 12px;
    padding: 12px;
    border: 1px solid var(--border);
    border-radius: var(--r-lg);
    background: var(--surface-2);
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .rs {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .rs > li {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 8px 10px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    min-width: 0;
  }
  .rl {
    gap: 8px;
  }
  .n {
    width: 16px;
    text-align: right;
  }
  .err {
    padding-left: 24px;
    overflow-wrap: anywhere;
  }
  .cur {
    padding: 4px 0 2px 24px;
  }
  .ord {
    margin: 0;
    padding-left: 20px;
    display: flex;
    flex-direction: column;
    gap: 4px;
    font-size: 13px;
  }
  .up {
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }
  @media (max-width: 560px) {
    .rel {
      padding: 14px;
    }
    .ri {
      display: none;
    }
    .cur,
    .err {
      padding-left: 0;
    }
  }
</style>
