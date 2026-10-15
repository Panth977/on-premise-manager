<script lang="ts">
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import { api } from '$lib/api';
  import { Loader } from '$lib/resource.svelte';
  import { session } from '$lib/session.svelte';
  import { catalog } from '$lib/stores/catalog.svelte';
  import { bytes } from '$lib/util/format';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Button from '$lib/ui/Button.svelte';
  import TableSkeleton from '$lib/ui/TableSkeleton.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import ErrorState from '$lib/ui/ErrorState.svelte';
  import SearchInput from '$lib/ui/SearchInput.svelte';
  import RelTime from '$lib/ui/RelTime.svelte';
  import NewPageModal from '$lib/pages404/NewPageModal.svelte';
  import { Plus, FileCode } from '$lib/ui/icons';

  const list = new Loader(() => api.listNotFoundPages({}).then((r) => r.items));
  onMount(() => {
    list.load();
    catalog.ensure();
  });
  const canWrite = $derived(session.can('notFoundPages', 'write'));
  let q = $state('');
  const rows = $derived((list.data ?? []).filter((p) => !q || p.label.toLowerCase().includes(q.toLowerCase())));
  let open = $state(false);
  const who = (uid: string) => catalog.accounts.find((a) => a.uid === uid)?.name ?? uid;
</script>

<svelte:head><title>Not-found pages · OPM</title></svelte:head>
<div class="page">
  <PageHeader title="Not-found pages" subtitle="The page a site edge answers when no route matches (404) or a container is not ready (503). Pages are immutable." icon={FileCode} readOnly={!canWrite}>
    {#snippet actions()}
      <SearchInput bind:value={q} placeholder="Search pages" />
      {#if canWrite}<Button variant="primary" icon={Plus} onclick={() => (open = true)} data-testid="new-page">New page</Button>{/if}
    {/snippet}
  </PageHeader>
  {#if list.loading && !list.data}
    <TableSkeleton cols={5} />
  {:else if list.error}
    <ErrorState message={list.error} onretry={() => list.load()} />
  {:else if !list.data?.length}
    <EmptyState icon={FileCode} title="No pages yet" text="Sites use the built-in page until you add one.">
      {#snippet action()}{#if canWrite}<Button variant="primary" icon={Plus} onclick={() => (open = true)}>New page</Button>{/if}{/snippet}
    </EmptyState>
  {:else if !rows.length}
    <EmptyState compact title="No match" text="No page matches “{q}”." />
  {:else}
    <div class="table-wrap">
      <table class="table" data-testid="pages-table">
        <thead><tr><th>Label</th><th class="num">Size</th><th>Created by</th><th>Created</th><th class="num">Used by</th></tr></thead>
        <tbody>
          {#each rows as p (p.notFoundPageId)}
            <tr class="clickable" onclick={() => goto(`/app/not-found-pages/${p.notFoundPageId}`)} data-testid="page-row">
              <td><a href="/app/not-found-pages/{p.notFoundPageId}" class="strong" onclick={(e) => e.stopPropagation()}>{p.label}</a></td>
              <td class="num small">{bytes(p.sizeBytes)}</td>
              <td class="small">{who(p.createdBy)}</td>
              <td class="small muted"><RelTime ts={p.createdAt} /></td>
              <td class="num">{p.usedBy ? `${p.usedBy} site${p.usedBy === 1 ? '' : 's'}` : '—'}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>
<NewPageModal bind:open oncreated={(p) => goto(`/app/not-found-pages/${p.notFoundPageId}`)} />
