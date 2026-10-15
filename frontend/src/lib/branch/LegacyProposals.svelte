<script lang="ts">
  /** §21 #104 proposals written before 0.14 (and compat proposals for agents < 0.14): read only, collapsed. */
  import { onMount } from 'svelte';
  import type { Proposal } from '@opm/shared';
  import { api } from '../api';
  import RelTime from '../ui/RelTime.svelte';
  import StatusBadge from '../ui/StatusBadge.svelte';
  let { deploymentId }: { deploymentId: string } = $props();
  let items = $state<Proposal[]>([]);
  onMount(() => {
    api
      .listProposals({ deploymentId, query: { limit: 50 } })
      .then((r) => (items = r.items))
      .catch(() => {});
  });
</script>

{#if items.length}
  <details class="card legacy" data-testid="legacy-proposals">
    <summary class="card-pad strong">Legacy proposals ({items.length}) <span class="muted small">— before agent branches (§21 #104), and the compat proposals agents &lt; 0.14 still receive. Read only.</span></summary>
    <div class="table-wrap">
      <table class="table">
        <thead><tr><th>Seq</th><th>Status</th><th>Origin</th><th>Summary</th><th>Created</th></tr></thead>
        <tbody>
          {#each items as p (p.revId)}
            <tr><td class="mono">{p.seq}</td><td><StatusBadge kind="proposal" value={p.status} /></td><td>{p.origin}</td><td>{p.summary}</td><td><RelTime ts={p.createdAt} /></td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  </details>
{/if}

<style>
  .legacy {
    margin-top: 18px;
  }
  summary {
    cursor: pointer;
  }
</style>
