<script lang="ts">
  /**
   * Add / edit one declared site volume — the shared VolumeForm ($opm, SPEC §21 #102: the same form as the cloud
   * console) in a side drawer. Local edit: name + kind fixed after create; starter content and persist-to-cloud are cloud
   * configuration (§21 #101, V_LOCAL_VOLUME_CONTENT) — shown locked, kept as is; "Edit in the cloud console".
   * Used by the site Volumes page and by "+ New data volume…" on the container page.
   */
  import { untrack } from 'svelte';
  import type { SiteVolume } from '@opm/shared/types';
  import VolumeForm, { finishVolume, newVolume } from '$opm/forms/volumes/VolumeForm.svelte';
  import Drawer from '$lib/ui/Drawer.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import { consoleDeploymentUrl } from '$lib/cloud';
  import { session } from '$lib/state/session.svelte';

  interface Props {
    open: boolean;
    /** null = add a new volume */
    editing: SiteVolume | null;
    /** prefill of the name when adding (e.g. suggestVolumeSlug(<container>-<imagevolume>)) */
    suggest?: string;
    /** volumeSlugs already declared on the site */
    taken: string[];
    siteSlug: string;
    /** the agent data dir of the site ("./" = relative to it); undefined = not known yet */
    agentHome?: string;
    onsave: (v: SiteVolume) => void;
  }
  let { open = $bindable(false), editing, suggest = '', taken, siteSlug, agentHome, onsave }: Props = $props();
  let value = $state<SiteVolume>(newVolume());
  let submitted = $state(false);
  let valid = $state(false);
  let ready = $state(false);
  $effect(() => {
    if (!open) return void (ready = false);
    const v = editing ? (structuredClone($state.snapshot(editing)) as SiteVolume) : newVolume(suggest);
    untrack(() => {
      value = v;
      submitted = false;
      ready = true;
    });
  });
  const consoleUrl = $derived(consoleDeploymentUrl(session.info, `/sites/${encodeURIComponent(siteSlug)}/volumes${editing ? `/content?volume=${encodeURIComponent(editing.volumeSlug)}` : ''}`));
  function submit(e: SubmitEvent) {
    e.preventDefault();
    submitted = true;
    if (!valid) return;
    onsave(finishVolume($state.snapshot(value) as SiteVolume));
    open = false;
  }
</script>

<Drawer bind:open title={editing ? `Edit volume ${editing.volumeSlug}` : 'New data volume'} subtitle="Site {siteSlug}" testid="volume-modal">
  <form id="vm-form-el" onsubmit={submit} novalidate>
    {#if open && ready}<VolumeForm bind:value bind:valid mode={editing ? 'edit' : 'create'} {siteSlug} {taken} {agentHome} consoleHref={consoleUrl} {submitted} />{/if}
  </form>
  {#snippet footer()}
    <Btn onclick={() => (open = false)} data-testid="vm-cancel">Cancel</Btn>
    <Btn type="submit" form="vm-form-el" variant="primary" disabled={!valid} data-testid="vm-save">{editing ? 'Apply' : 'Add volume'}</Btn>
  {/snippet}
</Drawer>
