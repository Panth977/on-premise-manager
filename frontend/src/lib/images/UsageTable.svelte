<script lang="ts">
  import type { UsageItem } from '@opm/shared';
  import { catalog } from '../stores/catalog.svelte';
  import TableSkeleton from '../ui/TableSkeleton.svelte';
  import EmptyState from '../ui/EmptyState.svelte';
  import ErrorState from '../ui/ErrorState.svelte';
  import { CheckCircle } from '../ui/icons';
  import { onMount } from 'svelte';
  let { items, loading, error, onretry, emptyText }: { items: UsageItem[] | null; loading: boolean; error: string | null; onretry: () => void; emptyText: string } = $props();
  onMount(() => catalog.ensure());
  const href = (u: UsageItem) => `/app/${u.kind === 'template' ? 'templates' : 'deployments'}/${u.id}/sites/${u.siteSlug}${u.containerSlug ? `/containers/${u.containerSlug}` : ''}`;
</script>

{#if loading && !items}
  <TableSkeleton cols={5} rows={3} />
{:else if error}
  <ErrorState message={error} {onretry} />
{:else if !items?.length}
  <EmptyState icon={CheckCircle} title="Not used" text={emptyText} />
{:else}
  <div class="table-wrap">
    <table class="table" data-testid="usage-table">
      <thead><tr><th>Kind</th><th>Name</th><th>Site</th><th>Container</th><th>Version</th></tr></thead>
      <tbody>
        {#each items as u, i (i)}
          <tr>
            <td><span class="badge {u.kind === 'template' ? 'violet' : 'blue'}">{u.kind}</span></td>
            <td><a href={u.kind === 'template' ? `/app/templates/${u.id}` : `/app/deployments/${u.id}`} class="strong">{u.label}</a></td>
            <td><a href={href(u)} class="mono">{u.siteSlug}</a></td>
            <td class="mono">{u.containerSlug}{#if u.containerSlug && u.containerSlug !== u.serviceSlug}<div class="xs faint">{u.serviceSlug}</div>{/if}</td>
            <td class="mono">{u.version}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/if}
