<script lang="ts">
  import type { AgentReleaseInfo, AgentUpgradeRun, SetupResponse } from '@opm/shared';
  import { api, errCode } from '$lib/api';
  import { toast } from '$lib/toast.svelte';
  import { personName } from '$lib/util/people';
  import Button from '$lib/ui/Button.svelte';
  import ConfirmDialog from '$lib/ui/ConfirmDialog.svelte';
  import CopyButton from '$lib/ui/CopyButton.svelte';
  import RelTime from '$lib/ui/RelTime.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import { ArrowRight, Clock, Info, Rocket, Terminal, X } from '$lib/ui/icons';
  import UpgradeBadge from './UpgradeBadge.svelte';
  import UpgradeProgress from './UpgradeProgress.svelte';
  import ReleaseNotes from './ReleaseNotes.svelte';
  import UpgradeCommand from './UpgradeCommand.svelte';
  import { hasCliUpgrade, upgradeState } from './upgrade';

  /** §21 #90 the agent-upgrade block of one enrolled site on the Setup page */
  type SetupSite = SetupResponse['sites'][number];
  let { deploymentId, site, release, run, canWrite, onchanged }: { deploymentId: string; site: SetupSite; release: AgentReleaseInfo | null; run: AgentUpgradeRun | null; canWrite: boolean; onchanged: () => void } = $props();

  const v = $derived(site.upgrade);
  const st = $derived(upgradeState(v));
  const runGoing = $derived(run?.status === 'running');
  /** the queued request belongs to a running "Upgrade all" run → cancel the run instead */
  const inRun = $derived(!!v?.request?.runId && runGoing && run?.runId === v.request.runId);
  const busy = $derived(st === 'queued' || st === 'running');
  /** §21 #92 the upgrade / reinstall command sits inside the upgrade path when the agent cannot upgrade itself */
  const inPath = $derived(!!v && !v.supported && v.available);

  let confirmOpen = $state(false);
  let cancelling = $state(false);
  async function request() {
    if (!v?.latest) return;
    try {
      await api.requestAgentUpgrade({ deploymentId, siteSlug: site.siteSlug, body: { version: v.latest, confirm: site.siteSlug } });
      toast.success(`Upgrade of ${site.siteSlug} queued`, 'The agent picks it up on its next poll (≤ ~30 s).');
    } catch (e) {
      const c = errCode(e);
      if (c === 'stale' || c === 'conflict') {
        toast.error(e, c === 'stale' ? 'The pinned release moved — the page was reloaded' : 'Could not queue the upgrade');
        onchanged();
        return;
      }
      throw e;
    }
    onchanged();
  }
  async function cancel() {
    cancelling = true;
    try {
      await api.cancelAgentUpgrade({ deploymentId, siteSlug: site.siteSlug });
      toast.success(`Upgrade of ${site.siteSlug} cancelled`);
    } catch (e) {
      toast.error(e, errCode(e) === 'conflict' ? 'Too late to cancel' : 'Could not cancel');
    } finally {
      cancelling = false;
      onchanged();
    }
  }
</script>

{#if v}
  <div class="au" data-testid="agent-upgrade" data-state={st}>
    <div class="row between wrap top">
      <span class="small strong row"><Icon icon={Rocket} size={14} />Agent version</span>
      {#if canWrite && v.available && !busy}
        <Button
          size="sm"
          variant={v.supported ? 'primary' : 'default'}
          icon={Rocket}
          disabled={!v.supported || runGoing}
          tip={!v.supported ? (v.hint ?? undefined) : runGoing ? 'An "Upgrade all" run is going' : undefined}
          onclick={() => (confirmOpen = true)}
          data-testid="upgrade-agent">{st === 'failed' || st === 'rolled-back' ? 'Try again' : 'Upgrade agent'}</Button
        >
      {/if}
    </div>
    <div class="row wrap ver">
      <span class="small">installed <b class="mono" data-testid="agent-installed">{v.installed}</b></span>
      {#if v.latest}
        <span class="small muted">· latest <b class="mono" data-testid="agent-latest">{v.latest}</b></span>
      {:else}
        <span class="small faint">· no release pinned</span>
      {/if}
      <UpgradeBadge view={v} />
    </div>

    {#if st === 'queued' && v.request}
      <div class="banner info small" data-testid="upgrade-queued">
        <Icon icon={Clock} size={15} class="icon" />
        <div class="grow">
          <div><b>Queued</b> — the agent picks it up on its next poll (≤ ~30 s).{#if site.agent && !site.agent.online} <span class="text-amber">The site is offline: it starts when the agent is back (the request expires <RelTime ts={v.request.expiresAt} />).</span>{/if}</div>
          <div class="xs faint">{v.request.version} · requested by {personName(v.request.requestedBy)} <RelTime ts={v.request.requestedAt} />{#if v.request.runId} · part of “Upgrade all”{/if}</div>
        </div>
        {#if canWrite && !inRun}<Button size="sm" variant="ghost" icon={X} loading={cancelling} onclick={cancel} data-testid="upgrade-cancel">Cancel</Button>{/if}
      </div>
    {/if}

    {#if v.progress && st !== 'queued'}
      <UpgradeProgress progress={v.progress} />
    {/if}

    <details class="fb" data-testid="upgrade-fallback">
      <summary class="xs muted">Upgrade / downgrade fallback (install command)</summary>
      <div class="xs muted fbb">
        Run the site's <b>Upgrade / reinstall command</b> (below) on the server: it detects the installed agent, asks <b>UPGRADE / DOWNGRADE / SAME</b>, keeps the whole setup (agent.json, enrollment, data, containers keep running) and rolls back by itself when the new agent is not healthy within 60 s.
        A specific version: put <code>OPM_AGENT_VERSION=&lt;version&gt;</code> after <code>sudo</code> (<code>… | sudo OPM_AGENT_VERSION=0.9.0 sh</code>, or the command's Options); without a terminal add <code>OPM_YES=1</code> (a downgrade also needs <code>OPM_ALLOW_DOWNGRADE=1</code>).
      </div>
    </details>

    {#if !v.supported && v.available}
      <div class="hint-box" data-testid="upgrade-hint">
        <div class="row" style="gap:8px;align-items:flex-start"><Icon icon={Info} size={14} /><span class="small">{v.hint ?? 'This agent is too old to upgrade itself — rerun the install command of this site once.'} <a href="#upgrade-path-{site.siteSlug}" data-testid="upgrade-path-link">How to upgrade this site</a></span></div>
        <div class="path" id="upgrade-path-{site.siteSlug}" data-testid="upgrade-path">
          <div class="small strong row"><Icon icon={Terminal} size={14} />Upgrade {site.siteSlug} once from the server</div>
          {#if hasCliUpgrade(v.installed)}
            <div class="xs muted">Run on the server — it installs the pinned agent {v.latest ?? ''} (signature-checked, no token needed):</div>
            <div class="cmdline"><code class="mono" data-testid="upgrade-cli">sudo opm-agent upgrade</code><CopyButton text="sudo opm-agent upgrade" tip="Copy command" testid="upgrade-cli-copy" /></div>
            <div class="xs muted">or</div>
          {/if}
          <div class="xs muted" data-testid="upgrade-rerun">
            Run this site's upgrade command on the server ({site.upgradeCommand?.stored ? 'View → Copy' : 'not stored — Regenerate makes a new one'}); answer <b>y</b> to the UPGRADE question → {v.latest ?? 'the pinned agent'}. After that, upgrades are one click here.
          </div>
          <UpgradeCommand {deploymentId} {site} {canWrite} latest={v.latest} {onchanged} />
        </div>
      </div>
    {/if}
    {#if !inPath}
      <UpgradeCommand {deploymentId} {site} {canWrite} latest={v.latest} {onchanged} />
    {/if}
  </div>

  <ConfirmDialog
    bind:open={confirmOpen}
    title="Upgrade the agent of {site.siteSlug}?"
    confirmLabel="Upgrade agent"
    typed={site.siteSlug}
    typedLabel="Type the site slug"
    onconfirm={request}
    testid="upgrade-confirm"
  >
    {#snippet body()}
      <div class="stack tight">
        <div class="fromto" data-testid="upgrade-fromto"><b class="mono">{v.installed}</b><Icon icon={ArrowRight} size={14} /><b class="mono">{v.latest}</b></div>
        <p class="small muted">The agent downloads the pinned release, verifies its signature and sha256, swaps the binary and restarts. Containers keep running. If the new agent is not healthy within 60 s it rolls back to {v.installed} by itself.</p>
        {#if site.agent && !site.agent.online}<p class="small text-amber">{site.siteSlug} is offline — the upgrade starts when it is back (the request expires after 24 h).</p>{/if}
        <ReleaseNotes notes={release?.notes} version={v.latest ?? ''} summary="Release notes" testid="upgrade-confirm-notes" />
      </div>
    {/snippet}
  </ConfirmDialog>
{/if}

<style>
  .au {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding-top: 10px;
    border-top: 1px solid var(--border);
    min-width: 0;
  }
  .top,
  .ver {
    gap: 8px;
  }
  .fb summary {
    cursor: pointer;
    user-select: none;
  }
  .fbb {
    margin-top: 4px;
    line-height: 1.5;
    overflow-wrap: anywhere;
  }
  .hint-box {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 10px 12px;
    border-radius: var(--r-md);
    border: 1px solid color-mix(in srgb, var(--amber) 40%, var(--border));
    background: var(--amber-soft);
    color: var(--text);
  }
  .path {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 10px 12px;
    border-radius: var(--r-md);
    background: var(--surface);
    border: 1px solid var(--border);
    scroll-margin-top: 80px;
  }
  .path:target {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }
  .cmdline {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 6px 8px;
    border-radius: var(--r-sm, 6px);
    background: var(--surface-2);
    border: 1px solid var(--border);
    min-width: 0;
  }
  .cmdline code {
    flex: 1;
    min-width: 0;
    overflow-wrap: anywhere;
    font-size: 12px;
  }
  .fromto {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    padding: 8px 10px;
    border-radius: var(--r-md);
    background: var(--surface-2);
  }
</style>
