<script lang="ts">
  /**
   * §21 #96 Empty a volume: 60 s wait (⏩ fast-forward) + typed volume name → stop its containers (reverse start order),
   * optional snapshot of the volume first (default on), delete the contents (the folder / file itself, its owner and mode
   * stay), start them again. → onstarted(op) (OpProgress shows the rest).
   */
  import type { VolumeOp } from '@opm/shared/types';
  import { api } from '$lib/api';
  import IntentFlow from '$lib/ui/IntentFlow.svelte';

  interface Props {
    open: boolean;
    siteSlug: string;
    volumeSlug: string;
    kind: 'dir' | 'file';
    /** containers that mount it (they stop for the action) */
    users: string[];
    snapshotDefault?: boolean;
    onstarted: (op: VolumeOp) => void;
  }
  let { open = $bindable(false), siteSlug, volumeSlug, kind, users, snapshotDefault = true, onstarted }: Props = $props();
  let snapshot = $state(true);
  $effect(() => {
    if (open) snapshot = snapshotDefault;
  });
  const intentApi = {
    get: (intentId: string) => api.siteIntent({ siteSlug, intentId }),
    speed: (intentId: string, speed: 1 | 2 | 4) => api.siteIntentSpeed({ siteSlug, intentId, body: { speed } }),
    cancel: (intentId: string) => api.siteCancelIntent({ siteSlug, intentId }),
  };
</script>

<IntentFlow
  bind:open
  title="Empty volume {volumeSlug}"
  what="emptying the volume"
  testid="empty-flow"
  cancelTestid="empty-cancel"
  confirmTestid="empty-go"
  confirmLabel="Empty the volume"
  word={volumeSlug}
  startingText="Checking that no update or other action is running…"
  waitText="Everything inside the volume is deleted. Take the 60 s to make sure this is the right volume on the right site (or make a copy first)."
  {intentApi}
  start={() => api.emptyVolumeStart({ siteSlug, volumeSlug })}
  confirm={async (body) => onstarted(await api.emptyVolume({ siteSlug, volumeSlug, body: { ...body, snapshot } }))}
>
  {#snippet accept()}
    Type <strong class="mono">{volumeSlug}</strong> to {kind === 'file' ? 'truncate the file to 0 bytes' : 'delete everything inside the folder'} on site <strong>{siteSlug}</strong>.
    {#if users.length}<div class="small">{users.join(', ')} {users.length === 1 ? 'is' : 'are'} stopped first (reverse start order) and started again afterwards.</div>{/if}
  {/snippet}
  {#snippet extra()}
    <label class="switch" data-testid="empty-snapshot-label"><input type="checkbox" bind:checked={snapshot} data-testid="empty-snapshot" />Take a snapshot of this volume first (rollback point; listed under Backups)</label>
    {#if !snapshot}<p class="tiny warn-text" style="margin:4px 0 0">Without a snapshot (or a copy) the data cannot come back.</p>{/if}
  {/snippet}
</IntentFlow>
