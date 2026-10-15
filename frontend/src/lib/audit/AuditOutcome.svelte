<script lang="ts">
  import type { AuditLogEntry } from '@opm/shared';
  let { entry: e }: { entry: Pick<AuditLogEntry, 'outcome' | 'status' | 'errorCode' | 'errorMessage'> } = $props();
</script>

{#if e.outcome === 'error'}
  <span class="badge red" data-testid="audit-outcome" data-outcome="error" data-tip={e.errorMessage ?? undefined}><span class="dot"></span>{e.status}{e.errorCode ? ` ${e.errorCode}` : ''}</span>
{:else}
  <span class="badge green" data-testid="audit-outcome" data-outcome="ok"><span class="dot"></span>ok</span>
{/if}
