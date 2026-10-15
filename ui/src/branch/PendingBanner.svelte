<script lang="ts">
  /**
   * §21 #107 local draft prompt (both GUIs). A draft is stored in IndexedDB (drafts.ts) and is valid ONLY when it was
   * made on the entity's current head:
   *   state 'same'  → "You have unsaved changes from <time> (N changes) — Restore or Discard?"
   *   state 'stale' → one-time notice "Your local draft was based on commit X; the latest is Y, so it was discarded."
   *                   (the app has already deleted it; the only button is Dismiss — no restore / rebase of a stale draft)
   * Lists the secrets whose typed value was not kept (values never touch storage).
   */
  import { staleDraftNotice } from '@opm/shared/pending';
  import Btn from '../primitives/Btn.svelte';
  import Icon from '../primitives/Icon.svelte';
  import { savedAgo } from './pending';
  interface Props {
    state: 'same' | 'stale';
    count: number;
    savedAt: string;
    baseSeq?: number | null;
    headSeq?: number | null;
    droppedValues?: readonly string[];
    busy?: boolean;
    onrestore?: () => void;
    ondiscard: () => void;
    testid?: string;
  }
  let { state, count, savedAt, baseSeq = null, headSeq = null, droppedValues = [], busy = false, onrestore, ondiscard, testid = 'pending-banner' }: Props = $props();
  const when = $derived.by(() => {
    const t = Date.parse(savedAt);
    if (!Number.isFinite(t)) return '';
    const d = new Date(t);
    const sameDay = d.toDateString() === new Date().toDateString();
    return `${sameDay ? d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : d.toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' })}, ${savedAgo(savedAt)}`;
  });
</script>

<div class="banner {state === 'stale' ? 'warn' : 'info'} pb" data-testid={testid} data-state={state} role="status">
  <Icon name={state === 'stale' ? 'alert' : 'info'} />
  <div class="grow">
    <div class="small">
      {#if state === 'same'}
        You have <strong>unsaved changes</strong> from {when} ({count} change{count === 1 ? '' : 's'}) — Restore or Discard?
      {:else}
        <span data-testid="{testid}-notice">{staleDraftNotice(baseSeq, headSeq)}</span>
      {/if}
    </div>
    {#if state === 'same' && droppedValues.length}<div class="tiny muted" data-testid="{testid}-dropped">Secret values are never stored in the browser — re-enter: {droppedValues.join(', ')}.</div>{/if}
  </div>
  <div class="acts">
    {#if state === 'same'}
      <Btn size="sm" variant="ghost" onclick={ondiscard} disabled={busy} data-testid="{testid}-discard">Discard</Btn>
      <Btn size="sm" variant="primary" onclick={() => onrestore?.()} {busy} data-testid="{testid}-restore">Restore</Btn>
    {:else}
      <Btn size="sm" variant="ghost" onclick={ondiscard} data-testid="{testid}-dismiss">Dismiss</Btn>
    {/if}
  </div>
</div>

<style>
  .pb {
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
  }
  .grow {
    flex: 1 1 220px;
    min-width: 0;
  }
  .acts {
    display: flex;
    gap: 8px;
    margin-left: auto;
  }
</style>
