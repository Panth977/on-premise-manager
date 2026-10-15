<script lang="ts">
  /**
   * Reset a volume to its starter content (round E, destructive): POST …/reset/start → 60 s countdown (Cancel · ⏩ Hold
   * to fast-forward, §21 #95) → type ACCEPT → POST …/reset → ondone(ResetVolumeResponse) (the agent takes a snapshot of
   * the volume first, stops its containers, wipes it, re-seeds it and starts them again). The intent lives on the site
   * that holds the volume, so its wait / speed / cancel go through /sites/{siteSlug}/intents/… (forwarded over the mesh).
   */
  import type { Snippet } from 'svelte';
  import type { ResetVolumeResponse } from '@opm/shared/types';
  import { api } from '../api';
  import IntentFlow from './IntentFlow.svelte';

  interface Props {
    open: boolean;
    siteSlug: string;
    volumeSlug: string;
    details?: Snippet;
    ondone: (r: ResetVolumeResponse) => void;
  }
  let { open = $bindable(false), siteSlug, volumeSlug, details, ondone }: Props = $props();
  const intentApi = {
    get: (intentId: string) => api.siteIntent({ siteSlug, intentId }),
    speed: (intentId: string, speed: 1 | 2 | 4) => api.siteIntentSpeed({ siteSlug, intentId, body: { speed } }),
    cancel: (intentId: string) => api.siteCancelIntent({ siteSlug, intentId }),
  };
</script>

<IntentFlow
  bind:open
  title="Reset {volumeSlug} to its starter content"
  {details}
  what="the reset"
  testid="reset-flow"
  cancelTestid="reset-cancel"
  confirmTestid="start-reset"
  confirmLabel="Reset the volume"
  startingText="Preparing the reset…"
  waitText="Read what changes. Everything in the volume is replaced by the starter content. The reset cannot start before the wait ends; Cancel stops it."
  {intentApi}
  start={() => api.resetVolumeStart({ siteSlug, volumeSlug })}
  confirm={async (body) => ondone(await api.resetVolume({ siteSlug, volumeSlug, body }))}
>
  {#snippet accept()}
    Type <strong>ACCEPT</strong> to WIPE volume <code>{volumeSlug}</code> on site {siteSlug} and write the starter content again. A snapshot of the volume is taken first; the containers that mount it are stopped and started again.
  {/snippet}
</IntentFlow>
