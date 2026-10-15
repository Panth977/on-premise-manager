<script lang="ts">
  /**
   * Container page of the cloud console (SPEC §21 #52–53): the page chrome around the SHARED container form
   * ($opm ContainerForm, §21 #102 — the same form the agent GUI renders for its local edits).
   * containerSlug = null → "new" (…/containers/new, optional ?duplicate= / ?service=&version=): a local draft that is
   * added to the site on "Add container". Otherwise edits the container in the scope's browser change set (§21 #105:
   * shared with the site editor and every other page; nothing is sent until the scope's Commit…). The app keeps: header +
   * Apply / Discard / Cancel, the leave guard of a NEW container, duplicate / remove, the new-volume drawer and the
   * §21 #101 "move into the volume's content" fix.
   */
  import type { Container, Site, SiteVolume, SiteVolumeKind } from '@opm/shared';
  import { RE, defaultVolumeSlug, newSiteVolume, undeclaredVolumes, volumeUsers, duplicateContainer, suggestContainerSlug } from '@opm/shared';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { onMount } from 'svelte';
  import ContainerForm, { type NewVolumeRequest } from '$opm/forms/container/ContainerForm.svelte';
  import { freshContainer } from '$opm/forms/container/container';
  import Button from '../ui/Button.svelte';
  import VolumeDrawer from './VolumeDrawer.svelte';
  import ConfirmDialog from '../ui/ConfirmDialog.svelte';
  import EmptyState from '../ui/EmptyState.svelte';
  import IconView from '../ui/IconView.svelte';
  import Icon from '../ui/Icon.svelte';
  import { ArrowLeft, Trash2, Undo2, CircleAlert, TriangleAlert, Boxes } from '../ui/icons';
  import DraftBanner from './DraftBanner.svelte';
  import { defaultVersion } from './VersionPicker.svelte';
  import { catalog } from '../stores/catalog.svelte';
  import { useLeaveGuard } from '../util/guard.svelte';
  import type { AnyScope } from './scope.svelte';
  import { convertSiteSeeds } from './volume-content/content';
  import { toast } from '../toast.svelte';

  type Props = { scope: AnyScope; siteSlug: string; containerSlug: string | null; duplicate?: string | null; service?: string | null; version?: string | null };
  let { scope, siteSlug, containerSlug, duplicate = null, service = null, version = null }: Props = $props();
  const isNew = $derived(containerSlug === null);
  $effect(() => {
    if (scope.loaded) scope.openSite(siteSlug);
  });
  onMount(() => catalog.ensure());
  const site = $derived<Site | undefined>(scope.siteDrafts[siteSlug]);
  const sitePath = $derived(`${scope.base}/sites/${siteSlug}`);
  const ro = $derived(!scope.canWrite);

  // ───── the container being edited
  let draftC = $state<Container | null>(null);
  let inited = $state(false);
  let leaving = $state(false);
  const idx = $derived(!isNew && site ? site.config.containers.findIndex((x) => x.containerSlug === containerSlug) : -1);
  const c = $derived<Container | null>(isNew ? draftC : idx >= 0 ? site!.config.containers[idx]! : null);
  let errors = $state(0);
  let warnings = $state(0);
  let nameError = $state<string | null>(null);

  // new: initialise once (duplicate / service + version from the URL)
  $effect(() => {
    if (!isNew || inited || !site || !catalog.loaded) return;
    inited = true;
    const names = site.config.containers.map((x) => x.containerSlug);
    if (duplicate) {
      const src = site.config.containers.find((x) => x.containerSlug === duplicate);
      if (src) {
        draftC = duplicateContainer($state.snapshot(src) as Container, suggestContainerSlug(src.serviceSlug, names));
        declareMissing(src);
        return;
      }
    }
    if (service && catalog.service(service)) {
      const ver = version && catalog.image(service, version) ? version : defaultVersion(service);
      const img = catalog.image(service, ver);
      if (img) draftC = freshContainer(service, ver, img, suggestContainerSlug(service, names), scope.config.secrets.map((s) => s.secretSlug));
    }
  });
  // old links …/containers/{serviceSlug}: redirect when exactly one container runs that service
  $effect(() => {
    if (isNew || c || !site || !containerSlug) return;
    const m = site.config.containers.filter((x) => x.serviceSlug === containerSlug);
    if (m.length === 1) goto(`${sitePath}/containers/${m[0]!.containerSlug}`, { replaceState: true });
  });

  const svc = $derived(c ? catalog.service(c.serviceSlug) : null);
  const img = $derived(c ? catalog.image(c.serviceSlug, c.version) : null);
  const dirty = $derived(isNew ? !!draftC && !leaving : scope.siteDirty(siteSlug));
  useLeaveGuard(
    () => isNew && !!draftC && !leaving,
    () => null,
  );
  const siteErrors = $derived(site ? scope.validate().list.filter((i) => i.severity === 'error' && i.path.startsWith(`sites[${siteSlug}]`)).length : 0);

  // ───── volumes declared from THIS page for a new container (taken back on Cancel while unused)
  let declaredHere = $state<string[]>([]);
  /** a duplicate renames `<old>-<iv>` volumes: declare them like the original (label / description / backup copied) */
  function declareMissing(src: Container) {
    if (!site || !draftC) return;
    const cand = { containers: [...site.config.containers, $state.snapshot(draftC) as Container], volumes: site.config.volumes };
    for (const slug of undeclaredVolumes(cand)) {
      const row = draftC.volumes.find((v) => v.source.kind === 'volume' && v.source.volumeSlug === slug);
      const orig = row?.imageVolume ? site.config.volumes.find((x) => x.volumeSlug === defaultVolumeSlug(src.containerSlug, row.imageVolume!)) : undefined;
      site.config.volumes.push(orig ? { volumeSlug: slug, label: orig.label === orig.volumeSlug ? slug : orig.label, description: orig.description, backup: orig.backup, location: { kind: 'auto' }, kind: orig.kind ?? 'dir', seed: orig.seed ? structuredClone($state.snapshot(orig.seed)) : null } : newSiteVolume(slug));
      declaredHere = [...declaredHere, slug];
    }
  }
  /** the name of a new container changed: volumes this page declared as `<old>-<imageVolume>` follow it (when free) */
  function renameDeclared(old: string, n: string) {
    if (!site || !draftC) return;
    for (const v of draftC.volumes) {
      if (!v.imageVolume || v.source.kind !== 'volume' || v.source.volumeSlug !== defaultVolumeSlug(old, v.imageVolume) || !declaredHere.includes(v.source.volumeSlug)) continue;
      const from = v.source.volumeSlug;
      const to = defaultVolumeSlug(n, v.imageVolume);
      if (!n || !RE.volumeSlug.test(to) || site.config.volumes.some((x) => x.volumeSlug === to)) continue;
      const d = site.config.volumes.find((x) => x.volumeSlug === from);
      if (d) ((d.volumeSlug = to), d.label === from && (d.label = to));
      declaredHere = declaredHere.map((x) => (x === from ? to : x));
      v.source = { kind: 'volume', volumeSlug: to };
    }
  }
  let newVolOpen = $state(false);
  let newVolReq = $state<NewVolumeRequest | null>(null);
  function onNewVolume(req: NewVolumeRequest) {
    newVolReq = req;
    newVolOpen = true;
  }
  function declareNew(v: SiteVolume) {
    if (!site) return;
    site.config.volumes.push(v);
    if (isNew) declaredHere = [...declaredHere, v.volumeSlug];
    newVolReq?.done(v.volumeSlug);
    newVolReq = null;
  }
  function declareExisting(slug: string) {
    if (!site || site.config.volumes.some((x) => x.volumeSlug === slug)) return;
    site.config.volumes.push(newSiteVolume(slug));
    if (isNew) declaredHere = [...declaredHere, slug];
  }
  /** the image page of this container's image, focused on one image volume, with a way back here (§21 #83 fix links) */
  const imageVolumeHref = (name: string) => (c ? `/app/services/${encodeURIComponent(c.serviceSlug)}/image/${encodeURIComponent(c.version)}?volume=${encodeURIComponent(name)}&from=${encodeURIComponent(page.url.pathname + page.url.search)}` : '#');
  /** §21 #101 one-click fix of V_FILE_IN_WRITABLE_VOLUME: the shared migration moves the File's content into the volume's own content */
  function moveIntoVolumeContent() {
    if (!site || isNew) return;
    const r = convertSiteSeeds(scope.config.files, $state.snapshot(site) as Site);
    if (!r.moved.length) return void toast.warning('Not moved', r.notes[0] ?? 'nothing to move');
    site.config.volumes = r.config.volumes;
    site.config.containers = r.config.containers;
    toast.success("Moved into the volume's content", `${r.moved.map((m) => `${m.fileSlug} → ${m.volumeSlug}/${m.path}`).join(', ')} — not committed yet`);
  }

  // ───── save / cancel / remove
  let saving = $state(false);
  const canSave = $derived(!!c && !!img && !nameError && errors === 0 && (!isNew || siteErrors === 0));
  const saveTip = $derived(!c ? 'Pick a service first' : nameError ? nameError : errors ? `${errors} error(s) in this container` : isNew && siteErrors ? `Site ${siteSlug} has ${siteErrors} error(s)` : isNew ? 'Adds the container to the change set (not committed yet)' : 'Back to the site — the changes stay in this browser until Commit…');
  async function save() {
    if (!site || !c || !canSave) return;
    if (!isNew) {
      await scope.saveSite(siteSlug);
      toast.success(`Container ${c.containerSlug} applied`, 'Not committed yet — Commit… records it with a message.');
      goto(`${sitePath}/containers`);
      return;
    }
    saving = true;
    const added = $state.snapshot(draftC) as Container;
    site.config.containers.push(added);
    if (declaredHere.length) {
      const used = volumeUsers(site.config);
      site.config.volumes = site.config.volumes.filter((v) => !declaredHere.includes(v.volumeSlug) || !!used[v.volumeSlug]);
    }
    leaving = true;
    const ok = await scope.saveSite(siteSlug);
    saving = false;
    if (ok) toast.success(`Container ${added.containerSlug} added`, 'Not committed yet — Commit… records it with a message.');
    goto(ok ? `${sitePath}/containers` : `${sitePath}/containers/${added.containerSlug}`);
  }
  function cancel() {
    leaving = true;
    if (isNew && site && declaredHere.length) {
      const used = volumeUsers(site.config);
      site.config.volumes = site.config.volumes.filter((v) => !declaredHere.includes(v.volumeSlug) || !!used[v.volumeSlug]);
    }
    goto(`${sitePath}/containers`);
  }
  let rmOpen = $state(false);
  function removeContainer() {
    if (!site || !c) return;
    const id = c.containerSlug;
    site.config.containers = site.config.containers.filter((x) => x.containerSlug !== id);
    for (const x of site.config.containers) x.depends = x.depends.filter((d) => d.containerSlug !== id);
    goto(`${sitePath}/containers`);
  }
  const newVolKind = $derived<SiteVolumeKind>(newVolReq?.kind ?? 'dir');
</script>

{#if !scope.loaded || (!site && scope.serverSite(siteSlug)) || (isNew && !inited && !!site)}
  <div class="stack"><span class="skel" style="height:60px"></span><span class="skel" style="height:300px"></span></div>
{:else if !site}
  <EmptyState icon={Boxes} title="Site {siteSlug} does not exist" text="It may have been removed.">
    {#snippet action()}<Button href="{scope.base}/sites" icon={ArrowLeft}>Back to sites</Button>{/snippet}
  </EmptyState>
{:else if !isNew && !c}
  <EmptyState icon={Boxes} title="No container {containerSlug} on {siteSlug}" text="It may have been removed or renamed.">
    {#snippet action()}<Button href="{sitePath}/containers" icon={ArrowLeft}>Back to site</Button>{/snippet}
  </EmptyState>
{:else}
  {#if scope.kind === 'deployment'}<DraftBanner deploymentId={scope.id} />{/if}
  <div class="sticky" data-testid="container-editor">
    <div class="bar">
      <div class="ttl">
        <span class="pic"><IconView value={svc?.icon ?? null} type="service" size={22} /></span>
        <div class="grow" style="min-width:0">
          <div class="row wrap">
            <h2 class="mono" data-testid="container-title">{isNew ? (c?.containerSlug ? c.containerSlug : 'New container') : c?.containerSlug}</h2>
            {#if isNew}<span class="badge blue">new</span>{/if}
            {#if ro}<span class="badge">read only</span>{/if}
            {#if c}<span class="muted mono small">{c.serviceSlug}:{c.version}</span>{/if}
            {#if dirty && !isNew}<span class="dirty" data-tip="Site {siteSlug} has changes not committed yet"></span>{/if}
          </div>
        </div>
      </div>
      <div class="acts">
        <span class="vc">
          {#if errors}<span class="badge red"><Icon icon={CircleAlert} size={11} />{errors}</span>{/if}
          {#if warnings}<span class="badge amber"><Icon icon={TriangleAlert} size={11} />{warnings}</span>{/if}
          {#if c && !errors && !warnings}<span class="badge green">valid</span>{/if}
        </span>
        {#if !ro}
          {#if dirty && !isNew}<Button icon={Undo2} onclick={() => scope.discardSite(siteSlug)} tip="Drops every change of site {siteSlug} not committed yet" data-testid="discard-container">Discard site changes</Button>{/if}
          <Button onclick={cancel} data-testid="cancel-container">Cancel</Button>
          <Button variant="primary" onclick={save} loading={scope.saving || saving} disabled={!canSave} tip={saveTip} data-testid="apply-container">{isNew ? 'Add container' : 'Apply'}</Button>
        {:else}
          <Button href="{sitePath}/containers" icon={ArrowLeft}>Back to site</Button>
        {/if}
      </div>
    </div>
  </div>

  {#if isNew}
    <ContainerForm bind:value={draftC} {site} mode={ro ? 'read' : 'create'} extraIssues={scope.saveIssues} duplicateOf={duplicate} onnewvolume={onNewVolume} ondeclare={declareExisting} {imageVolumeHref} onnamechange={renameDeclared} href={{ volumes: `${sitePath}/volumes`, settings: `${sitePath}/settings`, containers: `${sitePath}/containers` }} bind:errors bind:warnings bind:nameError>
      {#snippet aside()}
        {#if !ro}
          <div class="row sbtn">
            <Button onclick={cancel}>Cancel</Button>
            <Button variant="primary" onclick={save} loading={scope.saving || saving} disabled={!canSave} tip={saveTip} data-testid="apply-container-side">Add container</Button>
          </div>
        {/if}
      {/snippet}
    </ContainerForm>
  {:else}
    <ContainerForm bind:value={site.config.containers[idx]!} {site} mode={ro ? 'read' : 'edit'} extraIssues={scope.saveIssues} onnewvolume={onNewVolume} ondeclare={declareExisting} onmoveintocontent={moveIntoVolumeContent} {imageVolumeHref} href={{ volumes: `${sitePath}/volumes`, settings: `${sitePath}/settings`, containers: `${sitePath}/containers` }} bind:errors bind:warnings bind:nameError>
      {#snippet aside()}
        {#if !ro}
          <div class="row sbtn">
            <Button onclick={cancel}>Cancel</Button>
            <Button variant="primary" onclick={save} loading={scope.saving || saving} disabled={!canSave} tip={saveTip} data-testid="apply-container-side">Apply</Button>
          </div>
        {/if}
      {/snippet}
      {#snippet footer()}
        {#if !ro && c}
          <div class="row">
            <Button href="{sitePath}/containers/new?duplicate={encodeURIComponent(c.containerSlug)}" data-testid="duplicate-container">Duplicate</Button>
            <Button variant="danger-outline" icon={Trash2} onclick={() => (rmOpen = true)} data-testid="remove-container">Remove container</Button>
          </div>
        {/if}
      {/snippet}
    </ContainerForm>
  {/if}
{/if}

<VolumeDrawer bind:open={newVolOpen} suggested={newVolReq?.suggested ?? ''} taken={(site?.config.volumes ?? []).map((x) => x.volumeSlug)} {siteSlug} context={newVolReq?.context ?? null} defaultKind={newVolKind} onsave={declareNew} />
<ConfirmDialog bind:open={rmOpen} title="Remove container {c?.containerSlug ?? ''}?" message="Removed from the working copy of site {siteSlug}; save the site to keep it. Routes that target it become invalid." confirmLabel="Remove" danger onconfirm={removeContainer} testid="confirm-remove-container" />

<style>
  .sticky {
    position: sticky;
    top: var(--sticky-top, 0px);
    z-index: var(--z-sticky);
    background: var(--bg);
    padding: 4px 0 10px;
    margin: -4px 0 var(--sp-4);
    border-bottom: 1px solid var(--border);
  }
  .bar {
    display: flex;
    align-items: center;
    gap: 12px;
    flex-wrap: wrap;
    padding-top: 8px;
  }
  .ttl {
    display: flex;
    align-items: center;
    gap: 12px;
    flex: 1;
    min-width: 0;
  }
  .pic {
    width: 42px;
    height: 42px;
    border-radius: 10px;
    background: var(--surface);
    border: 1px solid var(--border);
    display: grid;
    place-items: center;
    color: var(--accent-text);
    flex: none;
  }
  h2 {
    font-size: var(--fs-xl);
  }
  .dirty {
    width: 9px;
    height: 9px;
    border-radius: 50%;
    background: var(--amber);
    display: inline-block;
  }
  .acts {
    display: flex;
    gap: 8px;
    flex-wrap: wrap;
    align-items: center;
  }
  .vc {
    display: inline-flex;
    gap: 4px;
  }
  .sbtn {
    justify-content: flex-end;
  }
</style>
