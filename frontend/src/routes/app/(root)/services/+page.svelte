<script lang="ts">
  import type { ValidationIssue } from '@opm/shared';
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import { api, errIssues, isApiError } from '$lib/api';
  import { Loader } from '$lib/resource.svelte';
  import { session } from '$lib/session.svelte';
  import { toast } from '$lib/toast.svelte';
  import { catalog } from '$lib/stores/catalog.svelte';
  import { validateService } from '$lib/util/validators';
  import { mergeIssues, issue } from '$lib/util/issues';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import ServiceForm, { blankService, type ServiceDraft } from '$lib/forms/ServiceForm.svelte';
  import TableSkeleton from '$lib/ui/TableSkeleton.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import ErrorState from '$lib/ui/ErrorState.svelte';
  import SearchInput from '$lib/ui/SearchInput.svelte';
  import IconView from '$lib/ui/IconView.svelte';
  import { Plus, Boxes } from '$lib/ui/icons';

  const list = new Loader(() => api.listServices({}).then((r) => r.items));
  onMount(() => list.load());
  const canWrite = $derived(session.can('services', 'write'));
  let q = $state('');
  const rows = $derived((list.data ?? []).filter((s) => !q || `${s.label} ${s.serviceSlug} ${s.description}`.toLowerCase().includes(q.toLowerCase())));

  let open = $state(false);
  let f = $state<ServiceDraft>(blankService());
  let touched = $state(false);
  let saving = $state(false);
  let server = $state<ValidationIssue[]>([]);
  const local = $derived(validateService(f, true));
  const issues = $derived(mergeIssues(local, server));

  function openNew() {
    f = blankService();
    touched = false;
    server = [];
    open = true;
  }
  async function save(e: Event) {
    e.preventDefault();
    touched = true;
    if (local.length) return;
    saving = true;
    server = [];
    try {
      const s = await api.createService({ body: { label: f.label.trim(), serviceSlug: f.serviceSlug, icon: f.icon, description: f.description } });
      toast.success(`Service ${s.serviceSlug} created`);
      catalog.invalidate();
      open = false;
      goto(`/app/services/${s.serviceSlug}`);
    } catch (err) {
      if (isApiError(err) && err.code === 'conflict') server = [issue('serviceSlug', 'V_DUPLICATE', 'This name (id) is already used by another service')];
      else server = errIssues(err);
      toast.error(err, 'Not created');
    } finally {
      saving = false;
    }
  }
</script>

<svelte:head><title>Services · OPM</title></svelte:head>

<div class="page">
  <PageHeader title="Services" subtitle="A service is a named kind of container (e.g. postgres). Its name (id) is the DNS name on the site network and the name in expressions." icon={Boxes} readOnly={!canWrite}>
    {#snippet actions()}
      <SearchInput bind:value={q} placeholder="Search services" />
      {#if canWrite}<Button variant="primary" icon={Plus} onclick={openNew} data-testid="new-service">New service</Button>{/if}
    {/snippet}
  </PageHeader>

  {#if list.loading && !list.data}
    <TableSkeleton cols={6} />
  {:else if list.error}
    <ErrorState message={list.error} onretry={() => list.load()} />
  {:else if !list.data?.length}
    <EmptyState icon={Boxes} title="No services yet" text="Create a service, then register image versions for it.">
      {#snippet action()}{#if canWrite}<Button variant="primary" icon={Plus} onclick={openNew}>New service</Button>{/if}{/snippet}
    </EmptyState>
  {:else if !rows.length}
    <EmptyState compact title="No match" text="No service matches “{q}”." />
  {:else}
    <div class="table-wrap">
      <table class="table" data-testid="services-table">
        <thead><tr><th style="width:44px"></th><th>Label</th><th>Name (id)</th><th>Description</th><th class="num">Versions</th><th class="num">Used by</th></tr></thead>
        <tbody>
          {#each rows as s (s.serviceSlug)}
            <tr class="clickable" onclick={() => goto(`/app/services/${s.serviceSlug}`)} data-testid="service-row">
              <td><span class="ic"><IconView value={s.icon} type="service" size={16} /></span></td>
              <td><a href="/app/services/{s.serviceSlug}" class="strong" onclick={(e) => e.stopPropagation()}>{s.label}</a></td>
              <td><code class="mono small">{s.serviceSlug}</code></td>
              <td class="small muted desc">{s.description || '—'}</td>
              <td class="num">{s.imageCount}</td>
              <td class="num">{s.usedBy ? `${s.usedBy} deployment${s.usedBy === 1 ? '' : 's'}` : '—'}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>

<Modal bind:open title="New service" subtitle="Images (versions) are registered on the service page afterwards." busy={saving} testid="service-modal">
  <form id="svc-form" onsubmit={save} novalidate>
    <ServiceForm bind:value={f} mode="create" {issues} {server} show={touched || server.length > 0} />
  </form>
  {#snippet footer()}
    <Button onclick={() => (open = false)} disabled={saving}>Cancel</Button>
    <Button type="submit" form="svc-form" variant="primary" loading={saving} disabled={touched && local.length > 0} data-testid="svc-save">Create service</Button>
  {/snippet}
</Modal>

<style>
  .ic {
    width: 28px;
    height: 28px;
    border-radius: 7px;
    background: var(--accent-soft);
    color: var(--accent-text);
    display: grid;
    place-items: center;
  }
  .desc {
    max-width: 420px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
