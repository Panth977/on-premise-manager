<script lang="ts">
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import type { ValidationIssue } from '@opm/shared';
  import { api, errIssues } from '$lib/api';
  import { Loader } from '$lib/resource.svelte';
  import { getDeploymentScope } from '$lib/config-editor/context';
  import { toast } from '$lib/toast.svelte';
  import { validateDeploymentMeta } from '$lib/util/validators';
  import { mergeIssues } from '$lib/util/issues';
  import { personName, siteName } from '$lib/util/people';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import HistoryButton from '$lib/audit/HistoryButton.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Drawer from '$lib/ui/Drawer.svelte';
  import IdChip from '$lib/ui/IdChip.svelte';
  import DeploymentForm, { blankDeployment, type DeploymentDraft } from '$lib/forms/DeploymentForm.svelte';
  import { session } from '$lib/session.svelte';
  import ConfirmDialog from '$lib/ui/ConfirmDialog.svelte';
  import RelTime from '$lib/ui/RelTime.svelte';
  import TableSkeleton from '$lib/ui/TableSkeleton.svelte';
  import ErrorState from '$lib/ui/ErrorState.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import { Pencil, Trash2, Server, GitBranch, GitCompare, LayoutTemplate, ArrowRight, ScrollText } from '$lib/ui/icons';

  const scope = getDeploymentScope();
  const id = scope.id;
  const d = $derived(scope.deployment!);
  const canWrite = $derived(scope.can('deployments', 'write'));
  const canCfg = $derived(scope.can('deployment.config', 'read'));
  const canAct = $derived(scope.can('deployment.activities', 'read'));
  const canSeeAccounts = $derived(session.can('accounts', 'read'));

  const status = new Loader(() => api.getStatus({ deploymentId: id }));
  const acts = new Loader(() => api.listActivities({ deploymentId: id, query: { limit: 10 } }).then((r) => r.items));
  // §21 #104 branch: last commits + agent branches
  const commits = new Loader(() => api.listDeploymentCommits({ deploymentId: id, query: { limit: 5 } }).then((r) => r.items));
  const agentBranches = new Loader(() => api.listAgentBranches({ deploymentId: id }).then((r) => r.items));
  onMount(() => {
    status.load();
    if (canAct) acts.load();
    if (canCfg) {
      commits.load();
      agentBranches.load();
    }
  });
  const online = $derived(status.data ? status.data.sites.filter((s) => s.online).length : null);
  const total = $derived(status.data ? status.data.sites.length : null);
  const br = $derived(d.branch ?? null);
  const head = $derived(br?.head ?? null);
  const origin = $derived(br?.origin ?? null);
  const abBehind = $derived((agentBranches.data ?? []).filter((a) => a.behind > 0).length);
  const abDrift = $derived((agentBranches.data ?? []).filter((a) => a.ahead > 0 || (a.uncommitted?.drift ?? 0) > 0).length);
  const abOld = $derived((agentBranches.data ?? []).filter((a) => a.needsAgent014).length);

  // edit modal
  let editOpen = $state(false);
  let f = $state<DeploymentDraft>(blankDeployment());
  let saving = $state(false);
  let touched = $state(false);
  let server = $state<ValidationIssue[]>([]);
  const local = $derived(validateDeploymentMeta({ label: f.label, image: f.image, tagSlugs: f.tagSlugs }));
  const issues = $derived(mergeIssues(local, server));
  function openEdit() {
    f = { ...blankDeployment(), label: d.label, slug: d.slug ?? '', slugManual: true, image: d.image ?? null, source: 'empty', tagSlugs: [...d.tagSlugs], managers: [...d.managerAccountUids] };
    touched = false;
    server = [];
    editOpen = true;
  }
  const same = (a: string[], b: string[]) => JSON.stringify([...a].sort()) === JSON.stringify([...b].sort());
  async function save(e: Event) {
    e.preventDefault();
    touched = true;
    if (local.length) return;
    const body: { label?: string; image?: string | null; tagSlugs?: string[]; managerAccountUids?: string[] } = {};
    if (f.label.trim() !== d.label) body.label = f.label.trim();
    if (f.image !== (d.image ?? null)) body.image = f.image;
    if (!same(f.tagSlugs, d.tagSlugs)) body.tagSlugs = f.tagSlugs;
    if (!same(f.managers, d.managerAccountUids)) body.managerAccountUids = f.managers;
    if (!Object.keys(body).length) return void (editOpen = false);
    saving = true;
    server = [];
    try {
      await api.patchDeployment({ deploymentId: id, body });
      await scope.refresh();
      toast.success('Details saved');
      editOpen = false;
    } catch (err) {
      server = errIssues(err);
      toast.error(err, 'Not saved');
    } finally {
      saving = false;
    }
  }
  let delOpen = $state(false);
  async function doDelete() {
    await api.deleteDeployment({ deploymentId: id, body: { confirm: d.label } });
    toast.success(`Deployment ${d.label} deleted`);
    goto('/app/deployments');
  }
</script>

<svelte:head><title>{d.label} · OPM</title></svelte:head>

<div class="page">
  <PageHeader title={d.label} iconName={d.image} iconType="deployment" subtitle="Overview of the deployment: sites, the head commit, the changes this browser holds and what each site's agent branch has.">
    {#if d.slug}<div class="row wrap small muted" data-testid="dep-slug-meta"><span data-tip="Fixed at create — the GitHub branches are deployment/{d.slug}/…"><IdChip id={d.slug} label="slug" /></span></div>{/if}
    {#snippet actions()}
      <HistoryButton target="deployments/{d.deploymentId}" title="History of {d.label}" />
      {#if canWrite}
        <Button icon={Pencil} onclick={openEdit} data-testid="edit-deployment">Edit deployment</Button>
        <Button variant="danger-outline" icon={Trash2} onclick={() => (delOpen = true)} data-testid="delete-deployment">Delete</Button>
      {/if}
    {/snippet}
  </PageHeader>

  <div class="grid-4 cards">
    <a class="card card-pad stat" href="/app/deployments/{id}/status" data-testid="card-sites">
      <div class="lab"><Icon icon={Server} size={14} />Sites online</div>
      {#if status.data}
        <div class="big">{online} <span class="faint">/ {total}</span></div>
        <div class="row wrap dots">{#each status.data.sites as s (s.siteSlug)}<span class="row small" style="gap:4px"><span class="dot {s.online ? 'green' : s.enrolled ? 'red' : 'gray'}"></span>{s.siteSlug}</span>{/each}</div>
      {:else if status.error}<div class="small text-red">{status.error}</div>{:else}<span class="skel" style="width:50%;height:22px"></span>{/if}
    </a>
    <a class="card card-pad stat" href="/app/deployments/{id}/history" data-testid="card-head">
      <div class="lab"><Icon icon={GitBranch} size={14} />Head commit</div>
      {#if head}
        <div class="big">commit {head.seq}</div>
        <div class="small muted ellipsis" data-testid="card-head-message">{head.message.split('\n')[0]}</div>
        <div class="xs faint">{head.author.email} · <RelTime ts={head.createdAt} /></div>
      {:else}
        <div class="big faint">none</div>
        <div class="small muted">Nothing committed yet — edit the config, then Commit….</div>
      {/if}
    </a>
    <a class="card card-pad stat" href="/app/deployments/{id}/compare?review=working" data-testid="card-uncommitted">
      <div class="lab"><Icon icon={GitCompare} size={14} />Not committed</div>
      {#if !canCfg}<div class="small faint">no access</div>
      {:else if scope.pendingCount > 0}
        <div class="big text-amber" data-testid="card-uncommitted-count">{scope.pendingCount}</div>
        <div class="small muted">change{scope.pendingCount === 1 ? '' : 's'} in this browser, not committed yet. Review and commit.</div>
      {:else}<div class="big faint">0</div><div class="small muted">Everything is committed.</div>{/if}
      {#if origin}<div class="xs faint ellipsis" data-testid="card-origin">from <span class="link-like">{origin.label}{origin.seq ? `@${origin.seq}` : ''}</span></div>{/if}
    </a>
    <a class="card card-pad stat" href="/app/deployments/{id}/branches" data-testid="card-agent-branches">
      <div class="lab"><Icon icon={LayoutTemplate} size={14} />Agent branches</div>
      {#if !canCfg}<div class="small faint">no access</div>
      {:else if agentBranches.data}
        <div class="big">{agentBranches.data.length} <span class="faint small">site{agentBranches.data.length === 1 ? '' : 's'}</span></div>
        <div class="row wrap" style="gap:4px">
          {#if abBehind}<span class="badge amber">{abBehind} behind</span>{/if}
          {#if abDrift}<span class="badge accent">{abDrift} with server changes</span>{/if}
          {#if abOld}<span class="badge outline">{abOld} need agent ≥ 0.14</span>{/if}
          {#if !abBehind && !abDrift && !abOld && agentBranches.data.length}<span class="small muted">all in sync</span>{/if}
        </div>
      {:else if agentBranches.error}<div class="small text-red">{agentBranches.error}</div>{:else}<span class="skel" style="width:50%;height:22px"></span>{/if}
    </a>
  </div>

  <div class="two section">
    {#if canAct}
      <section class="card">
        <div class="card-head"><h3>Last activities</h3><span class="spacer"></span><a class="small row" href="/app/deployments/{id}/activities">All activities <Icon icon={ArrowRight} size={13} /></a></div>
        {#if acts.loading && !acts.data}<div class="card-body"><TableSkeleton rows={4} cols={3} head={false} /></div>
        {:else if acts.error}<div class="card-body"><ErrorState message={acts.error} onretry={() => acts.load()} /></div>
        {:else if !acts.data?.length}<div class="card-body"><EmptyState compact icon={ScrollText} title="No activities yet" /></div>
        {:else}
          <ul class="acts" data-testid="overview-activities">
            {#each acts.data as a (a.activityId)}
              <li>
                <span class="chip">{siteName(a.siteSlug)}</span>
                <div class="grow" style="min-width:0">
                  <div class="ellipsis">{a.summary}</div>
                  <div class="xs faint"><code>{a.type}</code> · {a.actor ? personName(a.actor) : 'system'}</div>
                </div>
                <span class="xs muted"><RelTime ts={a.ts} /></span>
              </li>
            {/each}
          </ul>
        {/if}
      </section>
    {/if}
    {#if canCfg}
      <section class="card">
        <div class="card-head"><h3>Last commits</h3><span class="spacer"></span><a class="small row" href="/app/deployments/{id}/history">History <Icon icon={ArrowRight} size={13} /></a></div>
        {#if commits.loading && !commits.data}<div class="card-body"><TableSkeleton rows={3} cols={4} head={false} /></div>
        {:else if commits.error}<div class="card-body"><ErrorState message={commits.error} onretry={() => commits.load()} /></div>
        {:else if !commits.data?.length}<div class="card-body"><EmptyState compact icon={GitCompare} title="No commits yet" text="Edit the working copy and commit it with a message." /></div>
        {:else}
          <ul class="acts" data-testid="overview-commits">
            {#each commits.data as c (c.commitId)}
              <li>
                <span class="chip mono">{c.seq}</span>
                <div class="grow" style="min-width:0">
                  <div class="ellipsis">{c.message.split('\n')[0]}</div>
                  <div class="xs faint"><code>{c.kind}</code> · {c.author.email}{c.source ? ` · from ${c.source.label}` : ''}</div>
                </div>
                <span class="xs muted"><RelTime ts={c.createdAt} /></span>
              </li>
            {/each}
          </ul>
        {/if}
      </section>
    {/if}
  </div>
</div>

<Drawer bind:open={editOpen} title="Edit deployment" subtitle="Label, logo, tags and managers; the source was chosen at create." busy={saving} testid="deployment-drawer">
  <form id="dep-form" onsubmit={save} novalidate>
    <DeploymentForm bind:value={f} mode="edit" {issues} {server} show={touched || server.length > 0} origin={origin ? `${origin.label}${origin.seq ? `@${origin.seq}` : ''}` : null} {canSeeAccounts} />
  </form>
  {#snippet footer()}
    <Button onclick={() => (editOpen = false)} disabled={saving}>Cancel</Button>
    <Button type="submit" form="dep-form" variant="primary" loading={saving} disabled={touched && local.length > 0} data-testid="dep-save">Save details</Button>
  {/snippet}
</Drawer>

<ConfirmDialog bind:open={delOpen} title="Delete deployment {d.label}?" message="The deployment disappears from the cloud. Agents see it as deleted, stop polling and keep their containers running. Install tokens and the access key stop working." confirmLabel="Delete deployment" danger typed={d.label} typedLabel="Type the label" onconfirm={doDelete} />

<style>
  .cards {
    gap: var(--sp-4);
  }
  .stat {
    display: flex;
    flex-direction: column;
    gap: 6px;
    color: var(--text);
    min-height: 116px;
  }
  a.stat:hover {
    text-decoration: none;
    border-color: var(--border-strong);
    box-shadow: var(--shadow-md);
  }
  .lab {
    display: flex;
    align-items: center;
    gap: 6px;
    color: var(--text-2);
    font-size: var(--fs-sm);
    font-weight: var(--fw-medium);
  }
  .big {
    font-size: 22px;
    font-weight: 600;
    letter-spacing: -0.01em;
  }
  .dots {
    gap: 10px;
  }
  .two {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--sp-4);
  }
  @media (max-width: 900px) {
    .two {
      grid-template-columns: minmax(0, 1fr);
    }
  }
  .acts {
    list-style: none;
    margin: 0;
    padding: 4px 0;
  }
  .acts li {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 16px;
    border-bottom: 1px solid var(--border);
  }
  .acts li :global(.chip) {
    min-width: 52px;
    justify-content: center;
  }
  .acts li:last-child {
    border-bottom: 0;
  }
  .link-like {
    color: var(--accent);
  }
  .text-amber {
    color: var(--amber-text);
  }
</style>
