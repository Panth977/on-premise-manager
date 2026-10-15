<script lang="ts">
  import type { Image, Service, ValidationIssue } from '@opm/shared';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { onDestroy, untrack } from 'svelte';
  import { api, errIssues } from '$lib/api';
  import { Loader } from '$lib/resource.svelte';
  import { session } from '$lib/session.svelte';
  import { toast } from '$lib/toast.svelte';
  import { catalog } from '$lib/stores/catalog.svelte';
  import { validateService } from '$lib/util/validators';
  import { mergeIssues } from '$lib/util/issues';
  import { bytes, shortSha } from '$lib/util/format';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import HistoryButton from '$lib/audit/HistoryButton.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import ServiceForm, { blankService, type ServiceDraft } from '$lib/forms/ServiceForm.svelte';
  import ConfirmDialog from '$lib/ui/ConfirmDialog.svelte';
  import TableSkeleton from '$lib/ui/TableSkeleton.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import ErrorState from '$lib/ui/ErrorState.svelte';
  import RelTime from '$lib/ui/RelTime.svelte';
  import StatusBadge from '$lib/ui/StatusBadge.svelte';
  import CopyButton from '$lib/ui/CopyButton.svelte';
  import { Plus, Pencil, Trash2, Link, Package } from '$lib/ui/icons';

  const sid = $derived(page.params.serviceSlug!);
  const svc = new Loader<Service>(() => api.getService({ serviceSlug: sid }));
  const imgs = new Loader<Image[]>(() => api.listImages({ serviceSlug: sid }).then((r) => r.items));
  const regs = new Loader(() => (session.can('registries', 'read') ? api.listRegistries({}).then((r) => r.items) : Promise.resolve([])));
  $effect(() => {
    void sid;
    untrack(() => {
      svc.load();
      imgs.load();
      regs.load();
    });
  });
  const regLabel = (id: string | null) => (id ? (regs.data?.find((r) => r.registryId === id)?.label ?? id) : 'public');

  // poll every 3 s while an import runs
  let timer: ReturnType<typeof setInterval> | null = null;
  const busy = $derived((imgs.data ?? []).some((i) => i.import.status === 'pending' || i.import.status === 'copying'));
  $effect(() => {
    if (busy && !timer) timer = setInterval(() => imgs.load(true), 3000);
    if (!busy && timer) {
      clearInterval(timer);
      timer = null;
      catalog.invalidate();
    }
  });
  onDestroy(() => timer && clearInterval(timer));

  const canWrite = $derived(session.can('services', 'write'));
  const canImages = $derived(session.can('images', 'write'));
  const imageCount = $derived(imgs.data?.length ?? 0);
  /** repositories the versions come from (host/repository, newest first, unique) */
  const repos = $derived([...new Set((imgs.data ?? []).map((i) => `${i.source.host}/${i.source.repository}`))]);

  // edit
  let editOpen = $state(false);
  let f = $state<ServiceDraft>(blankService());
  let touched = $state(false);
  let saving = $state(false);
  let server = $state<ValidationIssue[]>([]);
  const local = $derived(validateService(f, false));
  const issues = $derived(mergeIssues(local, server));
  function openEdit() {
    const s = svc.data!;
    f = { label: s.label, serviceSlug: s.serviceSlug, icon: s.icon, description: s.description };
    touched = false;
    server = [];
    editOpen = true;
  }
  async function save(e: Event) {
    e.preventDefault();
    touched = true;
    if (local.length) return;
    const s = svc.data!;
    const body: { label?: string; icon?: string | null; description?: string } = {};
    if (f.label !== s.label) body.label = f.label.trim();
    if (f.icon !== s.icon) body.icon = f.icon;
    if (f.description !== s.description) body.description = f.description;
    if (!Object.keys(body).length) {
      editOpen = false;
      return;
    }
    saving = true;
    try {
      svc.set(await api.patchService({ serviceSlug: sid, body }));
      catalog.invalidate();
      toast.success('Service saved');
      editOpen = false;
    } catch (err) {
      server = errIssues(err);
      toast.error(err, 'Not saved');
    } finally {
      saving = false;
    }
  }

  let delOpen = $state(false);
  async function doDelete() {
    await api.deleteService({ serviceSlug: sid });
    catalog.invalidate();
    toast.success('Service deleted');
    goto('/app/services');
  }
</script>

<svelte:head><title>{svc.data?.label ?? 'Service'} · OPM</title></svelte:head>

<div class="page">
  {#if svc.loading && !svc.data}
    <div class="stack"><span class="skel" style="width:240px;height:22px"></span><span class="skel" style="width:420px"></span></div>
  {:else if svc.error}
    <PageHeader title="Service" back={{ href: '/app/services', label: 'Services' }} />
    <ErrorState message={svc.error} onretry={() => svc.load()} />
  {:else if svc.data}
    {@const s = svc.data}
    <PageHeader title={s.label} iconName={s.icon} iconType="service" back={{ href: '/app/services', label: 'Services' }} subtitle={s.description || undefined} readOnly={!canWrite}>
      {#snippet meta()}<code class="badge outline mono" title="Name (id)">{s.serviceSlug}</code>{/snippet}
      {#if repos.length}
        <div class="row wrap meta small muted" data-testid="service-repos">
          <span>Image{repos.length === 1 ? '' : 's'} from</span>
          {#each repos as r (r)}<code class="mono">{r}</code>{/each}
        </div>
      {/if}
      {#snippet actions()}
        <Button icon={Link} href="/app/services/{sid}/used" data-testid="service-used">Used by</Button>
        <HistoryButton target="services/{sid}" title="History of {s.label}" />
        {#if canWrite}
          <Button icon={Pencil} onclick={openEdit} data-testid="edit-service">Edit service</Button>
          <span data-tip={imageCount ? 'Delete the images first' : undefined}>
            <Button icon={Trash2} variant="danger-outline" disabled={imageCount > 0 || !imgs.data} onclick={() => (delOpen = true)} data-testid="delete-service">Delete service</Button>
          </span>
        {/if}
      {/snippet}
    </PageHeader>

    <div class="section-title">
      <h3>Images</h3>
      <span class="muted small">{imageCount} version{imageCount === 1 ? '' : 's'}</span>
      <span class="spacer"></span>
      {#if canImages}<Button variant="primary" icon={Plus} href="/app/services/{sid}/register-image" data-testid="register-image">Register image</Button>{/if}
    </div>
    {#if imgs.loading && !imgs.data}
      <TableSkeleton cols={9} />
    {:else if imgs.error}
      <ErrorState message={imgs.error} onretry={() => imgs.load()} />
    {:else if !imgs.data?.length}
      <EmptyState icon={Package} title="No images yet" text="Register a version (v1) from a docker image reference; the cloud imports its layers.">
        {#snippet action()}{#if canImages}<Button variant="primary" icon={Plus} href="/app/services/{sid}/register-image">Register image</Button>{/if}{/snippet}
      </EmptyState>
    {:else}
      <div class="table-wrap">
        <table class="table" data-testid="images-table">
          <thead><tr><th>Version</th><th>Image reference</th><th>Registry</th><th>Kind</th><th>Platform</th><th>Digest</th><th class="num">Size</th><th>Import</th><th>Created</th></tr></thead>
          <tbody>
            {#each imgs.data as i (i.version)}
              <tr class="clickable" onclick={() => goto(`/app/services/${sid}/image/${encodeURIComponent(i.version)}`)} data-testid="image-row">
                <td><a class="strong mono" href="/app/services/{sid}/image/{encodeURIComponent(i.version)}" onclick={(e) => e.stopPropagation()}>{i.version}</a></td>
                <td class="mono small ref" title={i.reference}>{i.reference}</td>
                <td class="small nowrap">{regLabel(i.registryId)}</td>
                <td><span class="badge {i.kind === 'job' ? 'violet' : 'outline'}">{i.kind}</span></td>
                <td class="mono small">{i.platform}</td>
                <td class="nowrap" onclick={(e) => e.stopPropagation()}>{#if i.digest}<code class="mono small">{shortSha(i.digest)}</code><CopyButton text={i.digest} />{:else}<span class="faint">—</span>{/if}</td>
                <td class="num small">{bytes(i.import.sizeBytes)}</td>
                <td><StatusBadge kind="import" value={i.import.status} tip={i.import.error ?? undefined} /> <span class="small faint">{i.import.mode}</span></td>
                <td class="small muted"><RelTime ts={i.createdAt} /></td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      {#if busy}<p class="small muted" style="margin-top:8px">An import is running — this list refreshes every 3 s.</p>{/if}
    {/if}
  {/if}
</div>

<Modal bind:open={editOpen} title="Edit service" busy={saving} testid="service-modal">
  <form id="svc-edit" onsubmit={save} novalidate>
    <ServiceForm bind:value={f} mode="edit" {issues} {server} show={touched || server.length > 0} />
  </form>
  {#snippet footer()}
    <Button onclick={() => (editOpen = false)} disabled={saving}>Cancel</Button>
    <Button type="submit" form="svc-edit" variant="primary" loading={saving} disabled={touched && local.length > 0} data-testid="svc-save">Save</Button>
  {/snippet}
</Modal>

<ConfirmDialog bind:open={delOpen} title="Delete service {svc.data?.serviceSlug}?" message="The service has no images. Templates or deployments that name it would fail validation." confirmLabel="Delete service" danger onconfirm={doDelete} />

<style>
  .meta {
    margin-top: 8px;
    gap: 8px;
  }
  .ref {
    max-width: 360px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
