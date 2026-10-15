<script lang="ts">
  /**
   * §21 #105 the ONE change bar of a template / deployment scope (every config page): "N changes (not committed)" ·
   * Discard · Review · Commit…. The pending change set lives in the browser (scope); Commit (message required) is the
   * only persisting action — a deployment commit is a release (deployment.config.publish). A 409 stale answer rebases
   * the set onto the new head (clean → the dialog stays open: review and commit again; conflicts → RebaseDialog).
   * §21 #107: the commit first uploads the new content to a stage (progress in the dialog), then commits — one
   * transaction; a failure keeps every change (bytes included) and shows the error in the dialog.
   */
  import { needsAgentMessage, validateConfig, type ValidationIssue } from '@opm/shared';
  import ChangeBar from '$opm/primitives/ChangeBar.svelte';
  import CommitDialog from '$opm/branch/CommitDialog.svelte';
  import { goto } from '$app/navigation';
  import { api, errMsg, isApiError } from '../api';
  import { toast } from '../toast.svelte';
  import { session } from '../session.svelte';
  import ConfirmDialog from '../ui/ConfirmDialog.svelte';
  import type { AnyScope } from '../config-editor/scope.svelte';
  import { featureAgentWarnings, loadFileTexts } from './checks';
  import { commitUi } from './commit-ui.svelte';
  import { untrack } from 'svelte';

  let { scope, always = false }: { scope: AnyScope; always?: boolean } = $props();
  const count = $derived(scope.loaded ? scope.pendingCount : 0);
  const errors = $derived(scope.loaded && count ? validateConfig(scope.working(), scope.vctx(scope.kind === 'template' ? 'template' : 'publish')).errors.length : 0);
  const blocked = $derived(
    !scope.canCommit
      ? scope.kind === 'deployment'
        ? 'A deployment commit is a release — it needs deployment.config.publish'
        : 'Needs templates.write'
      : scope.kind === 'deployment' && scope.secretsChanged && !scope.canWriteSecrets
        ? 'Secret changes need deployment.secrets.write'
        : null,
  );

  let open = $state(false);
  let issues = $state<ValidationIssue[]>([]);
  let warnings = $state<string[]>([]);
  let info = $state<string | null>(null);
  let discardOpen = $state(false);
  const items = $derived(scope.pendingItems);

  async function prepare() {
    issues = validateConfig(scope.working(), scope.vctx(scope.kind === 'template' ? 'template' : 'publish')).errors;
    warnings = [];
    if (scope.kind === 'deployment') {
      await scope.loadStatus();
      const fw = featureAgentWarnings(scope, await loadFileTexts(scope));
      warnings.push(...fw.devtools, ...fw.content);
      try {
        const ab = await api.listAgentBranches({ deploymentId: scope.id });
        for (const a of ab.items) if (a.needsAgent014) warnings.push(needsAgentMessage(a.siteSlug, a.agentVersion ?? null));
      } catch {
        /* warnings are optional */
      }
    }
  }
  async function openCommit(note: string | null = null) {
    info = note;
    await prepare();
    open = true;
  }
  // "Commit…" requested from elsewhere (Compare review)
  let seen = commitUi.requests;
  $effect(() => {
    const n = commitUi.requests;
    if (n === seen) return;
    seen = n;
    untrack(() => void openCommit());
  });
  async function submit(message: string) {
    try {
      const c = await scope.commit(message);
      open = false;
      info = null;
      toast.success(`Commit ${c.seq} recorded`, scope.kind === 'deployment' ? 'This is now the cloud head: each site sees it and a person there merges it.' : undefined);
    } catch (e) {
      if (isApiError(e) && e.code === 'stale') {
        const reopen = () => void openCommit(`Rebased onto commit ${scope.headCommit?.seq ?? '—'}${scope.headCommit?.author?.email ? ` by ${scope.headCommit.author.email}` : ''} — review and commit again.`);
        const clean = await scope.refreshAndRebase(reopen);
        if (clean) {
          info = `Someone committed meanwhile. Your changes were rebased onto commit ${scope.headCommit?.seq ?? '—'}${scope.headCommit?.author?.email ? ` by ${scope.headCommit.author.email}` : ''} — review and commit again.`;
          await prepare();
          return;
        }
        open = false;
        return;
      }
      throw new Error(errMsg(e));
    }
  }
</script>

{#if scope.loaded && scope.canWrite && (always || count > 0)}
  <ChangeBar
    {count}
    {errors}
    note={scope.stale ? 'made on an older head — rebase first' : scope.kind === 'deployment' ? 'in this browser · a commit is a release' : 'in this browser'}
    emptyText="No changes — everything is committed."
    onCommit={scope.canCommit ? () => void openCommit() : undefined}
    blocked={scope.stale ? 'The head moved — Review & rebase first (banner above)' : blocked}
    busy={scope.committing}
    ondiscard={() => (discardOpen = true)}
    onreview={() => goto(`${scope.base}/compare?review=working`)}
    testid="change-bar"
  />
{/if}

<CommitDialog bind:open title="Commit {count} change{count === 1 ? '' : 's'}" primaryLabel={scope.kind === 'deployment' ? 'Commit (release)' : 'Commit'} {items} {issues} {warnings} {info} {blocked} busy={scope.committing} progress={scope.commitProgress} uploadNote={scope.uploadNote} author={session.account?.email ?? ''} intro={scope.kind === 'deployment' ? 'A deployment commit is a release: the sites see it as the cloud head and a person on each site merges it.' : undefined} onsubmit={submit} />
<ConfirmDialog bind:open={discardOpen} title="Discard {count} change{count === 1 ? '' : 's'}?" message="Every change of {scope.label} not committed yet is dropped from this browser; the editors show the head commit again." confirmLabel="Discard" danger onconfirm={() => { scope.discardPending(); toast.success('Changes discarded', 'The editors show the head commit again.'); }} testid="pending-discard-dialog" />
