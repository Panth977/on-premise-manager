<script lang="ts">
  /**
   * §21 #96 Restore a volume from a copy or an uploaded .tar.gz: 60 s wait (⏩) + typed volume name → stop its containers,
   * snapshot of the volume first (always), replace the contents, start them again.
   */
  import type { VolumeOp } from '@opm/shared/types';
  import { api } from '$lib/api';
  import IntentFlow from '$lib/ui/IntentFlow.svelte';

  interface Props {
    open: boolean;
    siteSlug: string;
    volumeSlug: string;
    source: { kind: 'copy'; copyId: string; label: string } | { kind: 'upload'; uploadId: string; label: string } | { kind: 'snapshot'; snapshotId: string; label: string } | null;
    users: string[];
    onstarted: (op: VolumeOp) => void;
  }
  let { open = $bindable(false), siteSlug, volumeSlug, source, users, onstarted }: Props = $props();
  const intentApi = {
    get: (intentId: string) => api.siteIntent({ siteSlug, intentId }),
    speed: (intentId: string, speed: 1 | 2 | 4) => api.siteIntentSpeed({ siteSlug, intentId, body: { speed } }),
    cancel: (intentId: string) => api.siteCancelIntent({ siteSlug, intentId }),
  };
</script>

<IntentFlow
  bind:open
  title="Restore volume {volumeSlug}"
  what="the restore"
  testid="restore-flow"
  cancelTestid="restore-cancel"
  confirmTestid="restore-go"
  confirmLabel="Restore the volume"
  word={volumeSlug}
  startingText="Checking that no update or other action is running…"
  waitText="The current contents are replaced. A snapshot of the volume is taken first, so you can roll back from Backups."
  {intentApi}
  start={() => api.restoreVolumeStart({ siteSlug, volumeSlug })}
  confirm={async (body) => {
    if (!source) return;
    const src = source.kind === 'copy' ? { kind: 'copy' as const, copyId: source.copyId } : source.kind === 'snapshot' ? { kind: 'snapshot' as const, snapshotId: source.snapshotId } : { kind: 'upload' as const, uploadId: source.uploadId };
    onstarted(await api.restoreVolume({ siteSlug, volumeSlug, body: { ...body, source: src } }));
  }}
>
  {#snippet accept()}
    Type <strong class="mono">{volumeSlug}</strong> to replace its contents on site <strong>{siteSlug}</strong> with <strong>{source?.label}</strong>. A snapshot of the volume is taken first.
    {#if users.length}<div class="small">{users.join(', ')} {users.length === 1 ? 'is' : 'are'} stopped during the restore and started again.</div>{/if}
  {/snippet}
  {#snippet extra()}
    <div class="small">Source: <strong data-testid="restore-source">{source?.label}</strong></div>
  {/snippet}
</IntentFlow>
