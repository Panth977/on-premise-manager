<script lang="ts">
  /**
   * Local container editor (SPEC §18, §21 #52/#53): one container INSTANCE of the head config, keyed by containerSlug,
   * rendered with the SHARED container form ($opm ContainerForm, §21 #102 — the same form as the cloud console).
   * §21 #113 a site is standalone: every field is a local edit; the image is picked with "Change image…" (catalog
   * version, docker reference, image on this host); Duplicate and Remove work here. The app keeps: page head + config
   * tabs, the local-edit bar, the new-volume drawer and the template file preview.
   */
  import { base } from '$app/paths';
  import { goto } from '$app/navigation';
  import { duplicateContainer, suggestContainerSlug } from '@opm/shared/validate';
  import type { Container } from '@opm/shared/types';
  import { applyVersion } from '$opm/forms/container/container';
  import ImagePicker from '$lib/editors/ImagePicker.svelte';
  import Confirm from '$lib/ui/Confirm.svelte';
  import { agentHomes, headConfig } from '$lib/state/app.svelte';
  import { draft } from '$lib/state/draft.svelte';
  import type { SiteVolume } from '@opm/shared/types';
  import { toasts } from '$lib/state/toast.svelte';
  import ContainerForm, { type NewVolumeRequest } from '$opm/forms/container/ContainerForm.svelte';
  import VolumeModal from '$lib/editors/VolumeModal.svelte';
  import { loadFileText, renderPreview } from '$lib/containerPreview';
  import PageHead from '$lib/ui/PageHead.svelte';
  import ConfigTabs from '$lib/ui/ConfigTabs.svelte';
  import Load from '$lib/ui/Load.svelte';
  import Empty from '$lib/ui/Empty.svelte';
  import NoHead from '$lib/ui/NoHead.svelte';
  import LocalEditBar from '$lib/ui/LocalEditBar.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Drawer from '$lib/ui/Drawer.svelte';
  import Deferred from '$lib/ui/Deferred.svelte';
  import { loadSiteFileTexts } from '$lib/depends';

  /** nav2: the container editor is a component — SITE › Config › Containers › {c} and the Config tab of the container page */
  let { site: slugProp, containerSlug: cProp, embedded = false }: { site: string; containerSlug: string; embedded?: boolean } = $props();
  const slug = $derived(slugProp);
  const containerSlug = $derived(cProp);
  const site = $derived(draft.work?.sites.find((s) => s.siteSlug === slug));
  const idx = $derived(site ? site.config.containers.findIndex((x) => x.containerSlug === containerSlug) : -1);
  const headC = $derived(draft.base?.config.sites.find((s) => s.siteSlug === slug)?.config.containers.find((x) => x.containerSlug === containerSlug));
  const dirty = $derived(idx >= 0 && !!site && JSON.stringify(site.config.containers[idx]) !== JSON.stringify(headC));
  const depId = $derived(draft.deploymentId);
  $effect(() => {
    if (slug && draft.work) loadSiteFileTexts(slug);
  });
  $effect(() => agentHomes.ensure(slug));
  const home = $derived(agentHomes.of(slug));
  let errors = $state(0);
  let warnings = $state(0);

  // ── "+ New data volume…": the agent's volume drawer, then the form selects the new volume
  let newOpen = $state(false);
  let newReq = $state<NewVolumeRequest | null>(null);
  function onNewVolume(req: NewVolumeRequest) {
    newReq = req;
    newOpen = true;
  }
  function createVolume(v: SiteVolume) {
    if (!site) return;
    if (!Array.isArray(site.config.volumes)) site.config.volumes = [];
    site.config.volumes.push(v);
    newReq?.done(v.volumeSlug);
    toasts.ok(`Volume ${v.volumeSlug} declared on site ${slug}`, 'Selected for this mount.');
    newReq = null;
  }

  // ── §21 #113 image / duplicate / remove (local edits; committed from the change bar)
  let pickOpen = $state(false);
  let removeOpen = $state(false);
  const cur = $derived(site && idx >= 0 ? site.config.containers[idx] : undefined);
  function changeImage(serviceSlug: string, version: string) {
    if (!cur) return;
    const img = draft.base?.catalog?.images[`${serviceSlug}:${version}`];
    cur.serviceSlug = serviceSlug;
    applyVersion(cur, version, img);
    toasts.ok(`Image of ${containerSlug}: ${serviceSlug}:${version}`, 'Commit from the change bar to apply it on this site.');
  }
  function duplicate() {
    if (!site || !cur) return;
    const name = suggestContainerSlug(cur.serviceSlug, site.config.containers.map((c) => c.containerSlug));
    site.config.containers.push(duplicateContainer($state.snapshot(cur) as Container, name));
    goto(`${base}/app/${slug}/config/containers/${name}`);
  }
  function remove() {
    if (!site || idx < 0) return;
    const gone = containerSlug;
    site.config.containers.splice(idx, 1);
    for (const c of site.config.containers) c.depends = c.depends.filter((d) => d.containerSlug !== gone);
    toasts.ok(`Container ${gone} removed`, 'Commit from the change bar to remove it on this site (a pre-change snapshot is taken first).');
    goto(`${base}/app/${slug}/config/containers`);
  }

  // ── file preview (template files rendered for this site, secrets masked)
  let pvOpen = $state(false);
  let pvFile = $state('');
  let pvLoading = $state(false);
  let pvErr = $state<string | null>(null);
  let pvText = $state<string | null>(null);
  const cfg = $derived(draft.asConfig('work'));
  const pvResult = $derived(pvText !== null && cfg ? renderPreview(cfg, depId, slug, pvText) : null);
  async function preview(fileSlug: string) {
    pvFile = fileSlug;
    pvOpen = true;
    pvErr = null;
    pvText = null;
    pvLoading = true;
    try {
      pvText = await loadFileText(fileSlug);
    } catch (e) {
      pvErr = e instanceof Error ? e.message : String(e);
    } finally {
      pvLoading = false;
    }
  }
</script>

{#if !embedded}
<PageHead icon="box" eyebrow="Site {slug} · config · container" title={containerSlug} sub="The same container form as the cloud console. Everything changes on this site — the image too; every change is a commit of this site's branch.">
  {#snippet actions()}
    {#if dirty}<span class="badge accent" data-testid="container-edited">edited</span>{/if}
    <a class="btn" href="{base}/app/{slug}/config/containers"><Icon name="chevronLeft" size={14} />Containers</a>
    {#if cur}<span class="mono small" data-testid="container-image" title="The image this container runs">{cur.serviceSlug}:{cur.version}</span>{/if}
    <Btn variant="ghost" icon="layers" disabled={!draft.work || !cur} onclick={() => (pickOpen = true)} data-testid="change-image">Change image…</Btn>
    <Btn variant="ghost" icon="copy" disabled={!draft.work || !cur} onclick={duplicate} data-testid="duplicate-container">Duplicate</Btn>
    <Btn variant="ghost" icon="trash" disabled={!draft.work || !cur} onclick={() => (removeOpen = true)} data-testid="remove-container">Remove</Btn>
  {/snippet}
</PageHead>
<ConfigTabs site={slug} />
{/if}

<Load loading={headConfig.loading} loaded={headConfig.loaded} error={headConfig.error} retry={() => headConfig.load()}>
  {#if !draft.base?.head}
    <section class="card"><NoHead /></section>
  {:else if !site || idx < 0}
    <section class="card"><Empty icon="box" title="No such container" text="Site {slug} has no container named {containerSlug}. Add one on the Containers page."><a class="btn" href="{base}/app/{slug}/config/containers">Back to containers</a></Empty></section>
  {:else}
    <ContainerForm
      bind:value={site.config.containers[idx]!}
      {site}
      mode={draft.work ? 'edit' : 'read'}
      agentHome={home}
      onnewvolume={onNewVolume}
      onpreviewfile={preview}
      href={{ volumes: `${base}/app/${slug}/config/volumes`, settings: `${base}/app/${slug}/config/settings`, containers: `${base}/app/${slug}/config/containers`, container: (x) => `${base}/app/${slug}/config/containers/${x}` }}
      bind:errors
      bind:warnings
    />
  {/if}
  <LocalEditBar />
</Load>

<ImagePicker bind:open={pickOpen} current={cur ? { serviceSlug: cur.serviceSlug, version: cur.version } : null} onpick={changeImage} title="Change the image of {containerSlug}" />
<Confirm bind:open={removeOpen} title="Remove container {containerSlug}?" confirmLabel="Remove" danger onconfirm={remove}>
  <p>The container is removed from site {slug} when you commit (a pre-change snapshot is taken first, so History → revert brings it back). Its data volumes stay.</p>
</Confirm>

<VolumeModal bind:open={newOpen} editing={null} suggest={newReq?.suggested ?? ''} taken={(site?.config.volumes ?? []).map((v) => v.volumeSlug)} siteSlug={slug} agentHome={agentHomes.map[slug]} onsave={createVolume} />

<Drawer bind:open={pvOpen} wide title="Preview {pvFile} for site {slug}" subtitle="Rendered once for site {slug} — the same read-only copy for every container that mounts it; secrets are masked (•••)." testid="file-preview">
  {#if pvLoading}
    <div class="row muted"><span class="spin"></span>Loading the file…</div>
  {:else if pvErr}
    <div class="banner bad small"><Icon name="alert" /><div class="grow">Could not load the file: {pvErr}</div></div>
  {:else if pvResult && !pvResult.ok}
    <div class="banner bad small" role="alert"><Icon name="alert" /><div class="grow"><span class="issue-code">{pvResult.code}</span>{pvFile}:{pvResult.line}:{pvResult.col}: {pvResult.message}</div></div>
  {:else if pvResult && pvResult.ok}
    <pre class="pv" data-testid="file-preview-text"><Deferred text={pvResult.masked} /></pre>
  {:else}
    <div class="muted small">Nothing to preview.</div>
  {/if}
</Drawer>

<style>
  .pv {
    margin: 0;
    padding: 10px 12px;
    background: var(--code-bg);
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    font-size: 12.5px;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    max-height: 70vh;
    overflow: auto;
  }
</style>
