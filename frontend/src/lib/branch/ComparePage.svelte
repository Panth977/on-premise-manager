<script lang="ts">
  import { untrack } from 'svelte';
  /**
   * §21 #104 Compare page of a template / deployment scope (replaces Diff + Template sync): pick a SOURCE (any template,
   * deployment or agent branch of this deployment, at its head or one of its commits) and cherry-pick its changes into
   * this branch (MergeView), or review the browser's pending changes (`?review=working`, §21 #105: computed in the
   * browser from the scope's pending set — the server keeps no working copy; read only + Commit…).
   * Query: `?source=<branchKey>[@<commitId>]` (pointParam), `?review=working`.
   */
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { branchKey, countMerge, parsePointParam, pointParam, parseBranchKey, needsAgentMessage, type BranchPoint, type BranchRef, type CommitSummary, type CompareRequest, type MergePlan } from '@opm/shared';
  import Button from '../ui/Button.svelte';
  import { requestCommit } from './commit-ui.svelte';
  import MergeView from '$opm/branch/MergeView.svelte';
  import { setBranchHost, type SourceOption } from '$opm/branch/host';
  import { createCloudBranchHost, commitsOf, rememberLabel, sourceOptions } from './branchHost';
  import type { AnyScope } from '../config-editor/scope.svelte';
  import PageHeader from '../ui/PageHeader.svelte';
  import Select from '../ui/Select.svelte';
  import SegmentedControl from '../ui/SegmentedControl.svelte';
  import EmptyState from '../ui/EmptyState.svelte';
  import { GitCompare } from '../ui/icons';
  import { toast } from '../toast.svelte';
  import { api } from '../api';
  import WorkingChecks from './WorkingChecks.svelte';

  let { scope }: { scope: AnyScope } = $props();
  const self = $derived(scope.branchRef);
  const sc = untrack(() => scope);
  rememberLabel(sc.branchRef, sc.label);
  const canMerge = $derived(sc.kind === 'template' ? sc.canWrite : sc.can('deployment.config', 'publish'));
  let version = $state(0);
  const host = setBranchHost(
    createCloudBranchHost({
      target: sc.branchRef,
      label: (b) => (branchKey(b) === branchKey(sc.branchRef) ? sc.label : branchKey(b)),
      get canWrite() {
        return canMerge;
      },
      canSecrets: sc.kind === 'deployment' ? { read: sc.can('deployment.secrets', 'read'), write: sc.can('deployment.secrets', 'write') } : undefined,
      deploymentId: sc.kind === 'deployment' ? sc.id : null,
      changed: () => {
        void sc.refresh?.();
        version++;
      },
    } as Parameters<typeof createCloudBranchHost>[0]),
  );
  void host;

  const review = $derived(page.url.searchParams.get('review') === 'working');
  const sourceParam = $derived(page.url.searchParams.get('source'));
  const origin = $derived(scope.branchState?.origin ?? null);
  const point = $derived.by<BranchPoint | null>(() => {
    if (review) return null;
    if (sourceParam) return parsePointParam(sourceParam);
    return origin ? { branch: origin.branch, at: 'head' } : null;
  });

  let options = $state<SourceOption[]>([]);
  $effect(() => {
    void sourceOptions(scope.branchRef, scope.kind === 'deployment' ? scope.id : null)
      .then((o) => (options = o))
      .catch(() => {});
  });
  const groups = $derived.by(() => {
    const g = new Map<string, { value: string; label: string; sub?: string }[]>();
    for (const o of options) {
      const k = o.group ?? '';
      if (!g.has(k)) g.set(k, []);
      g.get(k)!.push({ value: o.value, label: o.label, sub: o.hint });
    }
    return [...g].map(([label, options]) => ({ label, options }));
  });
  // commits of the chosen source (commit picker)
  let commits = $state<CommitSummary[]>([]);
  let commitsFor = '';
  $effect(() => {
    const b = point?.branch;
    const k = b ? branchKey(b) : '';
    if (k === commitsFor) return;
    commitsFor = k;
    commits = [];
    if (b) void commitsOf(b, null, 50).then((r) => (commits = r.items)).catch(() => {});
  });
  const commitOptions = $derived([{ value: 'head', label: 'Head (newest commit)' }, ...commits.map((c) => ({ value: c.commitId, label: `${c.seq} · ${c.message.split('\n')[0]}`, sub: `${c.kind} · ${c.author.email}` }))]);
  const commitValue = $derived(point && typeof point.at === 'object' ? point.at.commitId : 'head');

  function pick(branch: string | null, commit: string | null) {
    const b = branch ? parseBranchKey(branch) : null;
    if (!b) return;
    const p: BranchPoint = { branch: b, at: commit && commit !== 'head' ? { commitId: commit } : 'head' };
    void goto(`${scope.base}/compare?source=${encodeURIComponent(pointParam(p))}`, { keepFocus: true, noScroll: true });
  }
  const request = $derived.by<CompareRequest | null>(() => {
    if (review || !point) return null;
    return { source: point, target: self as BranchRef };
  });
  const reqKey = $derived(request ? JSON.stringify(request) + ':' + version : '');
  let warnings = $state<string[]>([]);
  $effect(() => {
    if (scope.kind !== 'deployment') return;
    void api
      .listAgentBranches({ deploymentId: scope.id })
      .then((r) => (warnings = r.items.filter((a) => a.needsAgent014).map((a) => needsAgentMessage(a.siteSlug, a.agentVersion ?? null))))
      .catch(() => {});
  });
  // §21 #105 review of the browser change set: a read-only plan built from the scope's pending items
  const reviewPlan = $derived.by<MergePlan | null>(() => {
    if (!review || !scope.loaded) return null;
    const items = scope.pendingItems;
    return {
      source: { point: { branch: self, at: 'working' }, label: `${scope.label} · this browser (not committed)`, commit: null },
      target: { branch: self, label: `${scope.label}@${scope.baseSeq ?? '—'}`, head: scope.headCommit, workingClean: true, uncommitted: 0 },
      base: { kind: 'none', sourceCommitId: null, label: 'your changes vs the head they were made on' },
      items,
      counts: countMerge(items),
      fastForward: false,
      diffOnly: true,
    };
  });
  const reviewKey = $derived(review ? JSON.stringify(scope.pendingItems.map((i) => [i.key, i.source])) : '');
  const pendingNote = $derived(scope.pendingCount ? `This browser holds ${scope.pendingCount} change${scope.pendingCount === 1 ? '' : 's'} of ${scope.label} not committed yet — after this merge they are rebased onto the merge commit.` : null);
  const isAgentSource = $derived(point?.branch.kind === 'agent');
</script>

<PageHeader title="Compare" subtitle={review ? `The changes of ${scope.label} held in this browser (not committed), compared with the head commit they were made on.` : `Pick a source branch and cherry-pick its changes into ${scope.label}. Merging commits them with a message.`}>
  {#snippet actions()}
    <SegmentedControl
      options={[
        { value: 'source', label: 'Another branch' },
        { value: 'working', label: `Not committed${scope.pendingCount ? ` (${scope.pendingCount})` : ''}` },
      ]}
      value={review ? 'working' : 'source'}
      onchange={(v: string) => void goto(v === 'working' ? `${scope.base}/compare?review=working` : `${scope.base}/compare${sourceParam ? `?source=${encodeURIComponent(sourceParam)}` : ''}`)}
      testid="compare-mode"
    />
  {/snippet}
</PageHeader>

{#if isAgentSource}
  <div class="banner info small" data-testid="compare-agent-banner">Changes made on site <b>{point?.branch.kind === 'agent' ? point.branch.siteSlug : ''}</b> — pick what the deployment should take. Changes not yet committed on the site are not shown (commit them in its agent GUI first).</div>
{/if}

{#snippet picker()}
  <div class="row pick" data-testid="compare-source-picker">
    <Select groups={groups} value={point ? branchKey(point.branch) : null} placeholder="Pick a source branch…" fallback={point ? branchKey(point.branch) : null} onchange={(v: string) => pick(v, null)} testid="compare-source" ariaLabel="Source branch" />
    {#if point}<Select options={commitOptions} value={commitValue} onchange={(v: string) => pick(branchKey(point!.branch), v)} testid="compare-commit" ariaLabel="Source commit" size="sm" />{/if}
  </div>
{/snippet}

{#if review && scope.kind === 'deployment'}<WorkingChecks {scope} onchanged={() => version++} />{/if}

{#if review}
  {#if scope.pendingCount === 0}
    <EmptyState icon={GitCompare} title="Nothing to review" text="Everything is committed — edit any page of {scope.label}; the changes stay in this browser until you commit them." />
  {:else}
    {#if canMerge}<div class="row end rv"><Button variant="primary" onclick={requestCommit} data-testid="review-commit">Commit {scope.pendingCount} change{scope.pendingCount === 1 ? '' : 's'}…</Button></div>{/if}
    {#key reviewKey}<MergeView plan={reviewPlan} diffOnly testid="review-view" />{/key}
  {/if}
{:else if request}
  {#key reqKey}
    <MergeView request={request} sourcePicker={picker} diffOnly={!canMerge} {warnings} {pendingNote} onmerged={(r) => toast.success(`Commit ${'seq' in r ? r.seq : ''} recorded`, scope.kind === 'deployment' ? 'This is now the cloud head; each site merges it.' : undefined)} />
  {/key}
{:else}
  <div class="card card-pad">{@render picker()}</div>
  <EmptyState icon={GitCompare} title="Pick a source" text="Any template, deployment{scope.kind === 'deployment' ? ' or agent branch of this deployment' : ''}. Its changes are listed field by field — including what it does NOT have (removals)." />
{/if}

<style>
  .pick {
    gap: 8px;
    flex-wrap: wrap;
    align-items: center;
  }
  .banner {
    margin-bottom: 12px;
  }
  .rv {
    margin-bottom: 10px;
  }
</style>
