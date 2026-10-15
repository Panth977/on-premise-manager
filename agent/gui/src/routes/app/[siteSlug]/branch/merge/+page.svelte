<script lang="ts">
  /**
   * DEPLOYMENT › Branch › Merge (§21 #104): a cloud commit → this site, cherry-pick with the shared MergeView. Removals
   * read "remove locally" (things this site has and the cloud commit does not). "Merge & apply" → the agent applies the
   * result (snapshot → apply → verify, rollback on failure); the merge commit is written when it succeeds.
   * §21 #111 the site of the URL is the target: another site than this agent → that site merges (over the mesh).
   */
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { appHref } from '$lib/nav';
  import { branch, uncommittedTotal } from '$lib/state/branch.svelte';
  import { overview } from '$lib/state/app.svelte';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toast.svelte';
  import { draft } from '$lib/state/draft.svelte';
  import { provideAgentBranchHost } from '$lib/branch/branchHost';
  import { siteBranchApi } from '$lib/branch/siteBranch';
  import { Resource } from '$lib/state/resource.svelte';
  import { commitSummary, type CompareRequest, type MergePlan } from '@opm/shared/branch';
  import MergeView from '$opm/branch/MergeView.svelte';
  import PageHead from '$lib/ui/PageHead.svelte';
  import Select from '$lib/ui/Select.svelte';
  import Icon from '$lib/ui/Icon.svelte';

  const site = $derived(page.params.siteSlug ?? '*');
  provideAgentBranchHost(undefined, () => page.params.siteSlug);
  const remote = $derived(siteBranchApi(site).remote);
  const remoteBranch = new Resource(() => siteBranchApi(page.params.siteSlug).state());
  const br = $derived(remote ? remoteBranch : branch);
  $effect(() => {
    const r = br;
    if (remote) r.reset();
    if (!r.loaded) r.load();
  });
  const source = $derived(page.url.searchParams.get('source') ?? br.data?.cloud.head?.commitId ?? null);
  const dep = $derived(overview.data?.deploymentId ?? session.info?.deploymentId ?? '');
  /** the target of the merge: the site of the page */
  const self = $derived(br.data?.siteSlug ?? remote ?? overview.data?.self ?? '');
  let plan = $state<MergePlan | null>(null);
  const request = $derived<CompareRequest | null>(source && dep && self ? { source: { branch: { kind: 'deployment', deploymentId: dep }, at: { commitId: source } }, target: { kind: 'agent', deploymentId: dep, siteSlug: self } } : null);
  // a new source → a new plan
  const lastKey = $derived(request ? JSON.stringify(request) : '');
  $effect(() => {
    void lastKey;
    plan = null;
  });
  const options = $derived((br.data?.cloud.recent ?? []).map((c) => ({ value: c.commitId, label: `seq ${c.seq} · ${commitSummary(c.message)}`, hint: c.author.email })));
  const dirty = $derived(remote ? (br.data ? br.data.uncommitted.drift + br.data.uncommitted.notes : 0) : uncommittedTotal());
</script>

<PageHead icon="branch" eyebrow={remote ? `Site ${remote} › Branch` : 'Deployment › Branch'} title={remote ? `Merge from the cloud into ${remote}` : 'Merge from the cloud'} sub="Tick what {remote ? `site ${remote}` : 'this site'} should take. Conflicts (changed here and in the cloud since the last merge) need a decision. Removals are shown in their own group." />

<p class="row back"><a href={appHref(site, 'branch')} data-testid="merge-back"><Icon name="chevronLeft" size={14} />Branch</a></p>

{#if dirty}
  <div class="banner warn" style="margin-bottom:14px" data-testid="merge-dirty-banner">
    <Icon name="alert" />
    <div class="grow">{dirty} {dirty === 1 ? 'change' : 'changes'} made on {remote ? `${remote}` : 'this server'} outside the agent {dirty === 1 ? 'is' : 'are'} not committed. Commit or discard them first — a merge never mixes with changes nobody recorded.</div>
    <a class="btn sm" href={appHref(site, 'branch?tab=uncommitted')}>Review uncommitted</a>
  </div>
{/if}
{#if plan?.references?.updatedSites.length}
  <div class="banner small" style="margin-bottom:12px" data-testid="merge-references"><Icon name="info" /><div class="grow">References to other sites ({plan.references.updatedSites.join(', ')}) are refreshed from this cloud commit — they are not items: each site merges its own part.</div></div>
{/if}

{#if request}
  {#key lastKey}
    <MergeView
      bind:plan
      {request}
      pendingNote={draft.dirty && !remote ? `This browser holds ${draft.pendingCount} ${draft.pendingCount === 1 ? 'change' : 'changes'} of this site (not committed). After the merge they are offered for a rebase onto the new head.` : null}
      onmerged={(r) => {
        toasts.info('Merge started', `Applying on ${remote ?? 'this site'}; the commit is written when it succeeds.`);
        void r;
        goto(appHref(site, 'branch'));
      }}
    >
      {#snippet sourcePicker()}
        <Select size="sm" value={source} {options} ariaLabel="Cloud commit" testid="merge-source-pick" onchange={(v) => goto(appHref(site, `branch/merge?source=${v}`), { replaceState: true })} />
      {/snippet}
    </MergeView>
  {/key}
{:else}
  <p class="muted">The cloud has no commit yet.</p>
{/if}

<style>
  .back {
    margin: -6px 0 12px;
  }
  .back a {
    display: inline-flex;
    gap: 4px;
    align-items: center;
  }
</style>
