<script lang="ts">
  /**
   * §21 #104/#105 checks above the browser change set (Compare · Not committed) — what Diff used to show: per-site agent
   * feature warnings, commit blockers (publish-level validation), warnings, and the suggested depends with "Add"
   * (applies the container's depends into the browser change set, never automatically; §21 #105).
   */
  import { onMount } from 'svelte';
  import { validateConfig, type ValidationIssue } from '@opm/shared';
  import type { DeploymentScope } from '../config-editor/scope.svelte';
  import { draftIssueLink } from '../util/issue-links';
  import { parseDependSuggestion } from '../config-editor/depends';
  import { toast } from '../toast.svelte';
  import IssuesList from '../ui/IssuesList.svelte';
  import Icon from '../ui/Icon.svelte';
  import { TriangleAlert } from '../ui/icons';
  import { featureAgentWarnings, loadFileTexts } from './checks';

  let { scope, onchanged }: { scope: DeploymentScope; onchanged?: () => void } = $props();
  let fileTexts = $state<Record<string, string>>({});
  onMount(async () => {
    if (!scope.status) await scope.loadStatus();
    fileTexts = await loadFileTexts(scope);
  });
  const agentW = $derived(scope.loaded ? featureAgentWarnings(scope, fileTexts) : { devtools: [], content: [] });
  const report = $derived(scope.loaded ? validateConfig(scope.working(), scope.vctx('publish')) : null);
  const blockers = $derived(report?.errors ?? []);
  const warnings = $derived((report?.list ?? []).filter((i) => i.severity === 'warning'));
  const suggestions = $derived((report?.list ?? []).filter((i) => i.code === 'V_DEPENDS_SUGGESTED'));
  const linkOf = (i: ValidationIssue) => draftIssueLink(scope.id, i);
  async function addDepend(siteSlug: string, containerSlug: string, target: string) {
    scope.openSite(siteSlug);
    const c = scope.siteDrafts[siteSlug]?.config.containers.find((x) => x.containerSlug === containerSlug);
    if (!c) return;
    if (!c.depends.some((d) => d.containerSlug === target)) c.depends = [...c.depends, { containerSlug: target }];
    if (await scope.saveSite(siteSlug)) {
      toast.success(`${target} added to ${containerSlug}`, 'Not committed yet.');
      onchanged?.();
    }
  }
  const suggestionAction = (i: ValidationIssue) => {
    const sg = parseDependSuggestion(i);
    if (!sg || !scope.canWrite) return null;
    return { label: 'Add', run: () => addDepend(sg.siteSlug, sg.containerSlug, sg.target), testid: `review-add-depend-${sg.containerSlug}-${sg.target}`, tip: `Add ${sg.target} to the Depends on of ${sg.containerSlug} (not committed until Commit)` };
  };
</script>

<div class="stack checks" data-testid="working-checks">
  {#each agentW.devtools as w, k (k)}<div class="banner warn" data-testid="devtools-agent-banner"><Icon icon={TriangleAlert} size={16} class="icon" /><div>{w}</div></div>{/each}
  {#each agentW.content as w, k (k)}<div class="banner warn" data-testid="content-agent-banner"><Icon icon={TriangleAlert} size={16} class="icon" /><div>{w}</div></div>{/each}
  <div class="two">
    <IssuesList issues={blockers} title="Commit blockers" {linkOf} emptyText="Nothing blocks a commit." testid="blockers" />
    <IssuesList issues={warnings} title="Warnings" {linkOf} emptyText="No warnings." testid="warnings" />
  </div>
  {#if suggestions.length}<IssuesList issues={suggestions} title="Suggestions" {linkOf} actionOf={suggestionAction} testid="suggestions" />{/if}
</div>

<style>
  .checks {
    margin-bottom: var(--sp-4);
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
</style>
