<script lang="ts">
  import { pendingCountOf } from '$lib/branch/pendingCount';
  import type { DeploymentSummary, ValidationIssue } from '@opm/shared';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { onMount, untrack } from 'svelte';
  import { api, errIssues } from '$lib/api';
  import { PagedLoader } from '$lib/resource.svelte';
  import { fromIssue, fromPoint } from '$lib/branch/FromPicker.svelte';
  import { session } from '$lib/session.svelte';
  import { toast } from '$lib/toast.svelte';
  import { catalog } from '$lib/stores/catalog.svelte';
  import { slugIssues, validateDeploymentMeta } from '$lib/util/validators';
  import { mergeIssues, issue } from '$lib/util/issues';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Drawer from '$lib/ui/Drawer.svelte';
  import DeploymentForm, { blankDeployment, type DeploymentDraft } from '$lib/forms/DeploymentForm.svelte';
  import CardSkeleton from '$lib/ui/CardSkeleton.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import ErrorState from '$lib/ui/ErrorState.svelte';
  import SearchInput from '$lib/ui/SearchInput.svelte';
  import IconView from '$lib/ui/IconView.svelte';
  import RelTime from '$lib/ui/RelTime.svelte';
  import TagChip from '$lib/ui/TagChip.svelte';
  import { handOffAccessKey } from '$lib/stores/handoff';
  import { Plus, Rocket, Clock } from '$lib/ui/icons';
  import Icon from '$lib/ui/Icon.svelte';

  const tag = $derived(page.url.searchParams.get('tag'));
  const list = new PagedLoader<DeploymentSummary>((cursor) => api.listDeployments({ query: { tag: tag ?? undefined, cursor: cursor ?? undefined } }));
  $effect(() => {
    void tag;
    untrack(() => list.reset());
  });
  onMount(() => catalog.ensure());
  let q = $state('');
  const rows = $derived(list.items.filter((d) => !q || d.label.toLowerCase().includes(q.toLowerCase())));
  const tagOf = (s: string) => catalog.tags.find((t) => t.tagSlug === s) ?? null;
  function setTag(t: string | null) {
    const u = new URL(page.url);
    if (t) u.searchParams.set('tag', t);
    else u.searchParams.delete('tag');
    goto(u.pathname + u.search, { replaceState: true, keepFocus: true, noScroll: true });
  }

  // ── who may create
  const canAll = $derived(session.can('deployments', 'write'));
  const scopedTags = $derived.by(() => {
    const s = new Set<string>();
    for (const r of session.account?.access ?? []) if (r.resource === 'deployments' && r.actions.includes('write') && r.scope.kind === 'tags') r.scope.tagSlugs.forEach((t) => s.add(t));
    return [...s];
  });
  const canCreate = $derived(canAll || scopedTags.length > 0);

  // ── new deployment drawer (6 fields → drawer)
  let open = $state(false);
  let f = $state<DeploymentDraft>(blankDeployment());
  let touched = $state(false);
  let saving = $state(false);
  let server = $state<ValidationIssue[]>([]);
  const local = $derived.by(() => {
    const out = validateDeploymentMeta({ label: f.label, image: f.image, tagSlugs: f.tagSlugs });
    slugIssues(f.slug, f.slugManual, out);
    const fi = fromIssue(f);
    if (fi) out.push(issue(fi.path, 'V_REQUIRED', fi.message));
    if (!canAll && !f.tagSlugs.some((t) => scopedTags.includes(t))) out.push(issue('tagSlugs', 'V_REQUIRED', `Your access needs one of: ${scopedTags.join(', ')}`));
    return out;
  });
  const issues = $derived(mergeIssues(local, server));
  const canSeeAccounts = $derived(session.can('accounts', 'read'));
  function openNew() {
    f = blankDeployment();
    if (!session.can('templates', 'read')) f.source = 'empty';
    touched = false;
    server = [];
    open = true;
  }
  async function save(e: Event) {
    e.preventDefault();
    touched = true;
    if (local.length) return;
    saving = true;
    server = [];
    try {
      const from = fromPoint(f);
      const r = await api.createDeployment({ body: { label: f.label.trim(), ...(f.slugManual && f.slug ? { slug: f.slug } : {}), templateId: null, from, ...(from && f.copySecretValues ? { copySecretValues: true } : {}), tagSlugs: f.tagSlugs, managerAccountUids: f.managers, image: f.image } });
      toast.success(`Deployment ${r.deployment.label} created`, 'The agent GUI access key is shown on Setup (View it again any time).');
      open = false;
      // the key goes to Setup in memory only (never the URL or browser storage) and shows there for its 60 s
      handOffAccessKey(r.deployment.deploymentId, { value: r.accessKey, hideAfterSec: r.hideAfterSec });
      await goto(`/app/deployments/${r.deployment.deploymentId}/setup`);
    } catch (err) {
      server = errIssues(err);
      toast.error(err, 'Not created');
    } finally {
      saving = false;
    }
  }
</script>

<svelte:head><title>Deployments · OPM</title></svelte:head>
<div class="page">
  <PageHeader title="Deployments" subtitle="One on-prem installation per client. Open one to edit its working copy, commit releases and watch its sites." icon={Rocket}>
    {#snippet actions()}
      <SearchInput bind:value={q} placeholder="Search by label" />
      {#if canCreate}<Button variant="primary" icon={Plus} onclick={openNew} data-testid="new-deployment">New deployment</Button>{/if}
    {/snippet}
  </PageHeader>

  {#if catalog.tags.length}
    <div class="filters" role="group" aria-label="Filter by tag">
      <button type="button" class="fc" class:on={!tag} onclick={() => setTag(null)} data-testid="filter-all">all</button>
      {#each catalog.tags as t (t.tagSlug)}
        <button type="button" class="fc" class:on={tag === t.tagSlug} style="--c:{t.color}" onclick={() => setTag(t.tagSlug)} data-testid="filter-{t.tagSlug}"><span class="sw"></span>{t.label}</button>
      {/each}
    </div>
  {/if}

  {#if list.loading && !list.loaded}
    <CardSkeleton count={3} />
  {:else if list.error && !list.items.length}
    <ErrorState message={list.error} onretry={() => list.reset()} />
  {:else if !list.items.length}
    <EmptyState icon={Rocket} title={tag ? `No deployment tagged ${tag}` : 'No deployments yet'} text={tag ? 'Choose another tag or “all”.' : 'Create a deployment from a template, then install the agent on each site.'}>
      {#snippet action()}{#if canCreate && !tag}<Button variant="primary" icon={Plus} onclick={openNew}>New deployment</Button>{/if}{/snippet}
    </EmptyState>
  {:else if !rows.length}
    <EmptyState compact title="No match" text="No deployment matches “{q}”." />
  {:else}
    <div class="cards" data-testid="deployment-cards">
      {#each rows as d (d.deploymentId)}
        <a class="card dc" href="/app/deployments/{d.deploymentId}" data-testid="deployment-card">
          <div class="top">
            <span class="logo"><IconView value={d.image} initials={d.label} size={36} /></span>
            <div class="grow" style="min-width:0">
              <div class="strong ellipsis lbl">{d.label}</div>
              <div class="faint xs mono ellipsis">{d.deploymentId}</div>
            </div>
            {#if pendingCountOf(`deployment:${d.deploymentId}`)}<span class="badge amber" data-testid="dep-uncommitted" data-tip="kept in this browser until you commit">{pendingCountOf(`deployment:${d.deploymentId}`)} not committed</span>{/if}
          </div>
          {#if d.tagSlugs.length}<div class="row wrap tags">{#each d.tagSlugs as t (t)}<TagChip slug={t} tag={tagOf(t)} />{/each}</div>{/if}
          <dl class="stats">
            <div>
              <dt>Sites online</dt>
              <dd>
                <span class="strong">{d.sitesOnline} / {d.sitesTotal}</span>
                <span class="dots" aria-hidden="true">{#each d.siteSlugs as s, i (s)}<span class="dot {i < d.sitesOnline ? 'green' : 'gray'}" title={s}></span>{/each}</span>
              </dd>
              {#if d.siteSlugs.length}<dd class="faint xs ellipsis">{d.siteSlugs.join(', ')}</dd>{/if}
            </div>
            <div>
              <dt>Head commit</dt>
              <dd data-testid="dep-head" class="ellipsis">{#if d.branch?.head}<span class="badge outline mono">{d.branch.head.seq}</span> <span class="small" title={d.branch.head.message.split('\n')[0]}>{d.branch.head.message.split('\n')[0]}</span>{:else}<span class="faint small">no commit yet</span>{/if}</dd>
            </div>
          </dl>
          <div class="foot xs muted"><span class="row" style="gap:4px"><Icon icon={Clock} size={12} />updated <RelTime ts={d.updatedAt} /></span></div>
        </a>
      {/each}
    </div>
    {#if list.hasMore}<div class="more"><Button loading={list.loadingMore} onclick={() => list.more()}>Load more</Button></div>{/if}
  {/if}
</div>

<Drawer bind:open title="New deployment" subtitle="Copies the source into the first commit and generates secrets and the access key." busy={saving} testid="deployment-drawer">
  <form id="dep-form" onsubmit={save} novalidate>
    <DeploymentForm bind:value={f} mode="create" {issues} {server} show={touched || server.length > 0} onlyTags={canAll ? null : scopedTags} {canSeeAccounts} />
  </form>
  {#snippet footer()}
    <Button onclick={() => (open = false)} disabled={saving}>Cancel</Button>
    <Button type="submit" form="dep-form" variant="primary" loading={saving} disabled={touched && local.length > 0} data-testid="dep-save">Create deployment</Button>
  {/snippet}
</Drawer>


<style>
  .filters {
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
    margin: -4px 0 16px;
  }
  .fc {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 28px;
    padding: 0 12px;
    border-radius: 999px;
    border: 1px solid var(--border-strong);
    background: var(--surface);
    cursor: pointer;
    font-size: var(--fs-sm);
    color: var(--text-2);
    font-weight: var(--fw-medium);
  }
  .fc:hover {
    background: var(--surface-hover);
  }
  .fc.on {
    background: var(--accent);
    border-color: var(--accent);
    color: #fff;
  }
  .fc .sw {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--c);
  }
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
    gap: var(--sp-4);
  }
  .dc {
    padding: 16px;
    display: flex;
    flex-direction: column;
    gap: 12px;
    color: var(--text);
    transition:
      border-color var(--dur-fast),
      box-shadow var(--dur-fast),
      transform var(--dur-fast);
  }
  .dc:hover {
    text-decoration: none;
    border-color: var(--border-strong);
    box-shadow: var(--shadow-md);
    transform: translateY(-1px);
  }
  .top {
    display: flex;
    gap: 10px;
    align-items: center;
  }
  .lbl {
    font-size: var(--fs-lg);
  }
  .logo {
    width: 38px;
    height: 38px;
    border-radius: 10px;
    flex: none;
    display: grid;
    place-items: center;
    overflow: hidden;
  }
  .logo :global(.ini) {
    border-radius: 10px;
    font-weight: 700;
  }
  .tags {
    gap: 4px;
  }
  .stats > div {
    min-width: 0;
  }
  .stats {
    display: grid;
    grid-template-columns: 1.4fr 1fr;
    gap: 10px;
    margin: 0;
    padding: 10px 12px;
    background: var(--surface-2);
    border-radius: var(--r-md);
    border: 1px solid var(--border);
  }
  .stats dt {
    font-size: var(--fs-xs);
    color: var(--text-3);
    margin-bottom: 3px;
  }
  .stats dd {
    margin: 0;
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .dots {
    display: inline-flex;
    gap: 4px;
  }
  .foot {
    margin-top: auto;
  }
  .more {
    display: flex;
    justify-content: center;
    margin-top: 16px;
  }
</style>
