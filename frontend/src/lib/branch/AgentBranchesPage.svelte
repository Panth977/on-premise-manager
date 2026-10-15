<script lang="ts">
  /**
   * §21 #104 Agent branches of a deployment: one row per site — its head commit, how far it is behind the deployment head,
   * how many changes it committed itself (ahead) / differs, changes made on the server not yet committed there (drift), the
   * agent version (< 0.14 = legacy: proposals still, no commits of its own). Compare opens …/compare?source=agent:…;
   * History opens the agent branch's commits (read only — only the agent writes it) in a drawer.
   */
  import { onDestroy, onMount, untrack } from 'svelte';
  import { branchKey, type AgentBranchView, type BranchRef } from '@opm/shared';
  import HistoryView from '$opm/branch/HistoryView.svelte';
  import { setBranchHost } from '$opm/branch/host';
  import { api } from '../api';
  import { Loader } from '../resource.svelte';
  import { createCloudBranchHost } from './branchHost';
  import type { DeploymentScope } from '../config-editor/scope.svelte';
  import PageHeader from '../ui/PageHeader.svelte';
  import Drawer from '../ui/Drawer.svelte';
  import Button from '../ui/Button.svelte';
  import RelTime from '../ui/RelTime.svelte';
  import TableSkeleton from '../ui/TableSkeleton.svelte';
  import ErrorState from '../ui/ErrorState.svelte';
  import EmptyState from '../ui/EmptyState.svelte';
  import { GitBranch, GitCompare, History } from '../ui/icons';

  let { scope }: { scope: DeploymentScope } = $props();
  const sc = untrack(() => scope);
  const id = sc.id;
  const list = new Loader(() => api.listAgentBranches({ deploymentId: id }).then((r) => r.items));
  let timer: ReturnType<typeof setInterval> | null = null;
  onMount(() => {
    list.load();
    timer = setInterval(() => !document.hidden && list.load(true), 30_000);
  });
  onDestroy(() => timer && clearInterval(timer));
  setBranchHost(
    createCloudBranchHost({
      target: null,
      label: (b) => (b.kind === 'agent' ? `${sc.label} · ${b.siteSlug}` : b.kind === 'deployment' && b.deploymentId === id ? sc.label : branchKey(b)),
      canWrite: false,
      deploymentId: id,
    }),
  );
  const head = $derived(scope.branchState?.head ?? null);
  const refOf = (a: AgentBranchView): BranchRef => ({ kind: 'agent', deploymentId: id, siteSlug: a.siteSlug });
  let drawer = $state<AgentBranchView | null>(null);
  let drawerOpen = $state(false);
  function openHistory(a: AgentBranchView) {
    drawer = a;
    drawerOpen = true;
  }
  const drift = (a: AgentBranchView) => (a.uncommitted ? a.uncommitted.drift : 0);
  /** §21 #105: agents 0.14.0 kept saved GUI edits on the agent (0.14.1 keeps them in the browser) */
  const savedEdits = (a: AgentBranchView) => a.uncommitted?.working ?? 0;
</script>

<PageHeader title="Agent branches" subtitle="Each site's agent keeps its own branch of commits. Compare one with this deployment to take changes made on that site; the sites merge the deployment's commits themselves." />

{#if list.loading && !list.data}
  <TableSkeleton cols={7} />
{:else if list.error}
  <ErrorState message={list.error} onretry={() => list.load()} />
{:else if !list.data?.length}
  <EmptyState icon={GitBranch} title="No agent branches yet" text="A site gets one when its agent enrolls." />
{:else}
  <div class="table-wrap">
    <table class="table" data-testid="agent-branches">
      <thead><tr><th>Site</th><th>Head</th><th>vs {head ? `commit ${head.seq}` : 'deployment'}</th><th>On the server</th><th>Agent</th><th class="act"></th></tr></thead>
      <tbody>
        {#each list.data as a (a.siteSlug)}
          <tr data-testid="ab-row-{a.siteSlug}">
            <td><span class="chip mono">{a.siteSlug}</span></td>
            <td style="max-width:320px">
              {#if a.head}
                <div class="row" style="gap:6px;min-width:0"><span class="badge outline mono">{a.head.seq}</span><span class="ellipsis small">{a.head.message.split('\n')[0]}</span></div>
                <div class="xs faint">{a.head.kind} · {a.head.author.email} · <RelTime ts={a.head.createdAt} /></div>
              {:else}<span class="faint small">nothing applied yet</span>{/if}
            </td>
            <td>
              <div class="row wrap" style="gap:4px" data-testid="ab-state-{a.siteSlug}">
                {#if a.behind}<span class="badge amber" data-tip="deployment commits this site has not merged yet">{a.behind} behind</span>{/if}
                {#if a.ahead}<span class="badge accent" data-tip="commits made on the site">{a.ahead} ahead</span>{/if}
                {#if a.differs}<span class="badge outline" data-tip="items whose value differs from the deployment head">{a.differs} differ{a.differs === 1 ? 's' : ''}</span>{/if}
                {#if !a.behind && !a.ahead && !a.differs}<span class="badge green">in sync</span>{/if}
              </div>
            </td>
            <td>
              {#if a.legacy || a.needsAgent014}<span class="faint small">not reported (agent &lt; 0.14)</span>
              {:else if drift(a)}<span class="text-amber small" data-testid="ab-drift-{a.siteSlug}">{drift(a)} change{drift(a) === 1 ? '' : 's'} made on the server</span>{#if a.uncommitted?.scannedAt}<div class="xs faint">scanned <RelTime ts={a.uncommitted.scannedAt} /></div>{/if}
              {:else if !savedEdits(a)}<span class="faint small">none</span>{/if}
              {#if !a.legacy && !a.needsAgent014 && savedEdits(a)}<div class="text-amber small" data-testid="ab-saved-{a.siteSlug}">{savedEdits(a)} saved edit{savedEdits(a) === 1 ? '' : 's'} on the agent ({a.agentVersion ?? '0.14.0'})</div>{/if}
            </td>
            <td>
              <span class="mono small">{a.agentVersion ?? '—'}</span>
              {#if a.needsAgent014}<div><span class="badge outline" data-testid="ab-needs-{a.siteSlug}" data-tip="Agents before 0.14 still get proposals: no commits of their own, no Compare with this site.">needs agent ≥ 0.14</span></div>{/if}
            </td>
            <td class="act">
              <div class="row" style="gap:4px;justify-content:flex-end;flex-wrap:nowrap">
                {#if a.head && !a.needsAgent014}<Button size="sm" icon={GitCompare} href="/app/deployments/{id}/compare?source={encodeURIComponent(branchKey(refOf(a)))}" data-testid="ab-compare-{a.siteSlug}">Compare</Button>{/if}
                {#if a.head}<Button size="sm" variant="ghost" icon={History} onclick={() => openHistory(a)} data-testid="ab-history-{a.siteSlug}">History</Button>{/if}
              </div>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/if}

<Drawer bind:open={drawerOpen} title={drawer ? `Site ${drawer.siteSlug} — agent branch` : ''} subtitle="Read only: only that site's agent commits to its branch." width="wide" testid="ab-history-drawer">
  {#if drawer}
    {#key drawer.siteSlug}<HistoryView branch={refOf(drawer)} label="{scope.label} · {drawer.siteSlug}" testid="ab-history" />{/key}
  {/if}
</Drawer>

<style>
  .act {
    width: 1%;
    white-space: nowrap;
  }
  .text-amber {
    color: var(--amber-text);
  }
</style>
