<script lang="ts">
  /**
   * Add / edit a site volume — the shared VolumeForm ($opm, SPEC §21 #102: the same form in the agent GUI) in a side
   * drawer. Used by the site Volumes page and by "+ New data volume…" on the container page. Writes nothing itself:
   * `onsave(volume, openContent)` gets the result (openContent = "Add & edit content" was used).
   * Test id of the drawer stays `volume-modal` (round D specs).
   */
  import { untrack } from 'svelte';
  import { SEED_ARCHIVE_MAX, type SiteVolume, type SiteVolumeKind } from '@opm/shared';
  import VolumeForm, { finishVolume, newVolume, type SeedMode } from '$opm/forms/volumes/VolumeForm.svelte';
  import Drawer from '../ui/Drawer.svelte';
  import Button from '../ui/Button.svelte';
    import { getScope } from './context';

  type Props = {
    open: boolean;
    /** null = a new volume */
    edit?: SiteVolume | null;
    /** prefilled name of a new volume (e.g. `<container>-<imagevolume>`) */
    suggested?: string;
    /** volumeSlugs already declared on the site */
    taken: string[];
    siteSlug: string;
    /** §21 #101 the content page of a volume (`…/volumes/content?volume=`); null = no link (e.g. the container page) */
    contentHref?: ((volumeSlug: string) => string) | null;
    /** e.g. "for cassandra · data" */
    context?: string | null;
    /** a new volume of this kind (e.g. a file mount on the container page) */
    defaultKind?: SiteVolumeKind;
    onsave: (v: SiteVolume, openContent?: boolean) => void;
  };
  let { open = $bindable(), edit = null, suggested = '', taken, siteSlug, contentHref = null, context = null, defaultKind = 'dir', onsave }: Props = $props();
  const scope = getScope();
  const ro = $derived(!scope.canWrite);
  let value = $state<SiteVolume>(newVolume());
  let submitted = $state(false);
  let valid = $state(false);
  let busy = $state(false);
  let seedMode = $state<SeedMode>('empty');
  let ready = $state(false);
  $effect(() => {
    if (!open) return void (ready = false);
    const v = edit ? (structuredClone($state.snapshot(edit)) as SiteVolume) : newVolume(suggested, defaultKind);
    untrack(() => {
      value = v;
      seedMode = v.seed ? (v.seed.kind === 'archive' ? 'archive' : 'own') : 'empty';
      submitted = false;
      ready = true;
    });
  });
  const href = $derived(contentHref ? (slug: string, tab?: 'persist') => contentHref(slug) + (tab ? `&tab=${tab}` : '') : null);
  /** §21 #107 the archive is a pending change (bytes in memory + IndexedDB); Commit… uploads it */
  async function upload(f: File) {
    if (f.size > SEED_ARCHIVE_MAX) throw new Error(`a starter archive is at most ${SEED_ARCHIVE_MAX / 1048576} MiB`);
    const bytes = new Uint8Array(await f.arrayBuffer());
    if (!(bytes[0] === 0x1f && bytes[1] === 0x8b)) throw new Error('a starter archive must be a .tar.gz (gzip) file');
    return scope.putBytes(bytes);
  }
  function submit(e: Event, openContent = false) {
    e.preventDefault();
    submitted = true;
    if (!valid) return;
    onsave(finishVolume($state.snapshot(value) as SiteVolume), openContent);
    open = false;
  }
</script>

<Drawer bind:open title={ro ? `Volume ${edit?.volumeSlug ?? ''}` : edit ? `Edit volume ${edit.volumeSlug}` : 'New data volume'} subtitle={context ?? `Site ${siteSlug}`} {busy} testid="volume-modal">
  <form id="volume-form-el" onsubmit={submit} novalidate>
    {#if open && ready}<VolumeForm bind:value bind:valid bind:busy bind:seedMode mode={ro ? 'read' : edit ? 'edit' : 'create'} {siteSlug} {taken} contentHref={href} uploadArchive={upload} {submitted} />{/if}
  </form>
  {#snippet footer()}
    <Button onclick={() => (open = false)} data-testid="vm-cancel">{ro ? 'Close' : 'Cancel'}</Button>
    {#if !ro}
      {#if contentHref && !edit && seedMode === 'own'}<Button onclick={(e) => submit(e, true)} disabled={!valid} data-testid="vm-save-edit">Add & edit content</Button>{/if}
      <Button type="submit" form="volume-form-el" variant="primary" disabled={!valid} data-testid="vm-save">{edit ? 'Apply' : 'Add volume'}</Button>
    {/if}
  {/snippet}
</Drawer>
