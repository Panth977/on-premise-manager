<script lang="ts">
  /**
   * Sticky change bar (SPEC §21 #102, §21 #105): the ONE bar of an entity's pending change set — the browser holds
   * every edit until Commit. "N changes (not committed) · n errors · note" + Discard · Review · Commit…. There is no
   * Save button: Commit (message required) is the only persisting action. The SAME bar in the cloud console (template /
   * deployment scope, every config page) and the agent GUI (every config page of the site).
   */
  import Btn from './Btn.svelte';
  interface Props {
    /** pending changes (items between the head and the browser change set) */
    count: number;
    errors?: number;
    note?: string;
    emptyText?: string;
    /** Commit… (opens the commit dialog). Absent = the person may not commit (bar shows the reason in `blocked`). */
    onCommit?: () => void;
    commitLabel?: string;
    /** extra reason Commit is unavailable now (e.g. "An update is running", "Needs deployment.config.publish") */
    blocked?: string | null;
    busy?: boolean;
    ondiscard?: () => void;
    onreview?: () => void;
    reviewLabel?: string;
    testid?: string;
    commitTestid?: string;
    reviewTestid?: string;
    discardTestid?: string;
  }
  let { count, errors = 0, note, emptyText = 'No changes — everything is committed.', onCommit, commitLabel = 'Commit…', blocked = null, busy = false, ondiscard, onreview, reviewLabel = 'Review', testid = 'change-bar', commitTestid = 'change-bar-commit', reviewTestid = 'review-changes', discardTestid = 'change-bar-discard' }: Props = $props();
  const dirty = $derived(count > 0);
</script>

<div class="bar" class:dirty data-testid={testid} data-count={count} role="region" aria-label="Changes not committed">
  <div class="grow info">
    {#if dirty}
      <span class="badge accent" data-testid="{testid}-count"><i class="dot"></i>{count} change{count === 1 ? '' : 's'} (not committed)</span>
      {#if errors}<span class="badge bad red">{errors} {errors === 1 ? 'error' : 'errors'}</span>{/if}
      {#if note}<span class="muted small hide-sm">{note}</span>{/if}
    {:else}
      <span class="muted small">{emptyText}</span>
    {/if}
  </div>
  {#if ondiscard}<Btn size="sm" variant="ghost" disabled={!dirty || busy} onclick={ondiscard} data-testid={discardTestid}>Discard</Btn>{/if}
  {#if onreview}<Btn size="sm" icon="search" disabled={!dirty} onclick={onreview} data-testid={reviewTestid}>{reviewLabel}</Btn>{/if}
  {#if onCommit || blocked}<Btn size="sm" variant="primary" icon="check" {busy} disabled={!dirty || errors > 0 || !!blocked || !onCommit} title={blocked ?? (errors ? 'Fix the errors first' : 'Commit with a message')} onclick={() => onCommit?.()} data-testid={commitTestid}>{commitLabel}</Btn>{/if}
</div>

<style>
  .bar {
    position: sticky;
    bottom: 12px;
    z-index: var(--z-editbar, var(--z-sticky));
    margin-top: 20px;
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
    padding: 10px 12px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 12px;
    box-shadow: var(--shadow-md);
  }
  .bar.dirty {
    border-color: color-mix(in srgb, var(--accent) 45%, var(--border));
    box-shadow:
      0 0 0 3px color-mix(in srgb, var(--accent) 10%, transparent),
      var(--shadow-md);
  }
  .grow {
    flex: 1;
    min-width: 0;
  }
  .info {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
  }
  .dot {
    display: inline-block;
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: currentColor;
    margin-right: 5px;
  }
  @media (max-width: 600px) {
    .hide-sm {
      display: none;
    }
    .info {
      flex-basis: 100%;
    }
  }
</style>
