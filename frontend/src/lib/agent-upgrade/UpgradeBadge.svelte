<script lang="ts">
  import type { SiteAgentUpgradeView } from '@opm/shared';
  import { PHASE_LABEL, upgradeState } from './upgrade';

  /** §21 #90 "Upgrade available" / "Up to date" / "Upgrade queued" / "Upgrading" badge of one site */
  let { view, testid = 'upgrade-badge' }: { view: SiteAgentUpgradeView | null; testid?: string } = $props();
  const st = $derived(upgradeState(view));
  const kind = $derived.by(() => {
    if (!view || !view.latest) return null;
    if (st === 'queued') return 'queued';
    if (st === 'running') return 'running';
    return view.available ? 'available' : 'current';
  });
</script>

{#if view && kind}
  {#if kind === 'queued'}
    <span class="badge blue" data-testid={testid} data-state="queued" data-tip="Waiting for the agent to pick the request up (next poll)"><span class="dot pulse"></span>Upgrade queued</span>
  {:else if kind === 'running'}
    <span class="badge blue" data-testid={testid} data-state="running" data-tip="{view.installed} → {view.progress?.to}"><span class="dot pulse"></span>Upgrading · {PHASE_LABEL[view.progress!.phase].toLowerCase()}</span>
  {:else if kind === 'available'}
    <span class="badge amber" data-testid={testid} data-state="available" data-tip="{view.installed} → {view.latest}">Upgrade available</span>
  {:else}
    <span class="badge green" data-testid={testid} data-state="current" data-tip="runs the pinned release {view.latest}">Up to date</span>
  {/if}
{/if}

<style>
  .pulse {
    animation: pulse 1.2s ease-in-out infinite;
  }
  @keyframes pulse {
    50% {
      opacity: 0.3;
    }
  }
</style>
