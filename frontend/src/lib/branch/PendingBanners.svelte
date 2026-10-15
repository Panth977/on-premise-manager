<script lang="ts">
  /**
   * §21 #105 top-of-page banners of a scope's browser change set + its persistence: the restore offer of a stored set
   * (same head → Restore; older head → Review & rebase), the stale banner (the head moved under pending changes), the
   * RebaseDialog (conflicts) and the auto-save on every change. §21 #107: drafts live in IndexedDB (ui/src/branch/
   * drafts.ts, per account, 1 h expiry); only a draft made on the current head is offered (Restore / Discard); a draft
   * made on an older head was discarded at load and a one-time notice says so.
   */
  import PendingBanner from '$opm/branch/PendingBanner.svelte';
  import RebaseDialog from '$opm/branch/RebaseDialog.svelte';
  import Button from '../ui/Button.svelte';
  import Icon from '../ui/Icon.svelte';
  import { TriangleAlert } from '../ui/icons';
  import type { AnyScope } from '../config-editor/scope.svelte';

  let { scope }: { scope: AnyScope } = $props();
  let busy = $state(false);
  // auto-save the draft on every change (deep read of the working config + secret ops; the scope debounces)
  $effect(() => {
    if (!scope.loaded) return;
    void JSON.stringify(scope.working());
    void JSON.stringify(scope.secretOps);
    void scope.baseCommitId;
    void scope.restoreOffer;
    void scope.blobsVersion;
    scope.persist();
  });
  let rebaseOpen = $state(false);
  $effect(() => {
    rebaseOpen = !!scope.rebaseState;
  });
  async function restore() {
    busy = true;
    try {
      await scope.acceptRestore();
    } finally {
      busy = false;
    }
  }
</script>

{#if scope.restoreOffer}
  {@const o = scope.restoreOffer}
  <div class="pbw"><PendingBanner state={o.state} count={o.stored.count} savedAt={o.stored.savedAt} baseSeq={o.stored.baseSeq} headSeq={scope.headCommit?.seq ?? null} droppedValues={o.stored.droppedValues} {busy} onrestore={restore} ondiscard={() => scope.discardRestore()} /></div>
{:else if scope.staleNotice}
  {@const n = scope.staleNotice}
  <div class="pbw"><PendingBanner state="stale" count={n.count} savedAt={n.savedAt} baseSeq={n.baseSeq} headSeq={n.headSeq} ondiscard={() => (scope.staleNotice = null)} /></div>
{:else if scope.stale && !scope.rebaseState}
  <div class="banner warn pbw" data-testid="pending-stale" role="status">
    <Icon icon={TriangleAlert} size={15} />
    <div class="grow small">Your {scope.pendingCount} change{scope.pendingCount === 1 ? '' : 's'} (not committed) {scope.pendingCount === 1 ? 'was' : 'were'} made on commit {scope.baseSeq ?? '—'}; the head is now commit {scope.headCommit?.seq ?? '—'}.</div>
    <Button size="sm" variant="ghost" onclick={() => scope.discardPending()} data-testid="pending-stale-discard">Discard</Button>
    <Button size="sm" variant="primary" onclick={() => scope.rebase()} data-testid="pending-stale-rebase">Review & rebase</Button>
  </div>
{/if}
{#if scope.lostBytes.length && !scope.restoreOffer}
  <div class="banner bad pbw" data-testid="pending-lost-bytes" role="alert">
    <Icon icon={TriangleAlert} size={15} />
    <div class="grow small">The content of {scope.lostBytes.length} pending file{scope.lostBytes.length === 1 ? '' : 's'} is no longer in this browser (site data cleared?): <span class="mono">{scope.lostBytes.slice(0, 6).join(', ')}{scope.lostBytes.length > 6 ? ` +${scope.lostBytes.length - 6}` : ''}</span>. Upload or edit {scope.lostBytes.length === 1 ? 'it' : 'them'} again, or Discard.</div>
    <Button size="sm" variant="ghost" onclick={() => (scope.lostBytes = [])} data-testid="pending-lost-bytes-dismiss">Dismiss</Button>
  </div>
{/if}
{#if scope.rebaseState}
  <RebaseDialog bind:open={rebaseOpen} conflicts={scope.rebaseState.conflicts} clean={scope.rebaseState.clean} headLabel={scope.rebaseState.headLabel} onapply={(ch) => scope.resolveRebase(ch)} ondiscard={() => scope.discardPending()} onclose={() => (scope.rebaseState = null)} />
{/if}

<style>
  .pbw {
    margin-bottom: 14px;
  }
  .banner.pbw {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }
  .grow {
    flex: 1 1 220px;
  }
</style>
