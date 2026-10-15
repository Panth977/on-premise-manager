<script lang="ts">
  import type { AuditLogEntry } from '@opm/shared';
  import { onMount } from 'svelte';
  import { api } from '$lib/api';
  import { PagedLoader } from '$lib/resource.svelte';
  import { catalog } from '$lib/stores/catalog.svelte';
  import { fmtDate } from '$lib/util/format';
  import Button from '$lib/ui/Button.svelte';
  import RelTime from '$lib/ui/RelTime.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import ErrorState from '$lib/ui/ErrorState.svelte';
  import TableSkeleton from '$lib/ui/TableSkeleton.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import { ChevronDown, ChevronRight, History } from '$lib/ui/icons';
  import AuditChanges from './AuditChanges.svelte';
  import AuditOutcome from './AuditOutcome.svelte';
  import { auditActionLabel, auditActionTone, auditActorLabel, normalizeAudit } from './audit';

  /** Audit entries of one target (e.g. "templates/{id}"), newest first, with an expandable before → after diff. */
  let { target, pageSize = 25 }: { target: string; pageSize?: number } = $props();

  const log = new PagedLoader<AuditLogEntry>((cursor) => api.listAuditLog({ query: { target, cursor: cursor ?? undefined, limit: pageSize } }).then((r) => ({ items: r.items.map(normalizeAudit), cursor: r.cursor })));
  let open = $state<Record<string, boolean>>({});
  onMount(() => {
    log.reset();
    catalog.ensure();
  });
</script>

<div data-testid="audit-history">
  <p class="small muted" style="margin:0 0 12px">Every change of <code class="mono">{target}</code> from the root audit log, newest first.</p>
  {#if log.loading && !log.loaded}
    <TableSkeleton cols={3} rows={4} />
  {:else if log.error && !log.items.length}
    <ErrorState message={log.error} onretry={() => log.reset()} />
  {:else if !log.items.length}
    <EmptyState compact icon={History} title="No history yet" text="Nothing was logged for this item." />
  {:else}
    <ul class="list">
      {#each log.items as e (e.auditId)}
        <li class="item" data-testid="history-entry">
          <button type="button" class="head" aria-expanded={!!open[e.auditId]} onclick={() => (open[e.auditId] = !open[e.auditId])}>
            <Icon icon={open[e.auditId] ? ChevronDown : ChevronRight} size={14} />
            <span class="badge {auditActionTone(e.action)}" data-tip={auditActionLabel(e.action)}>{e.action}</span>
            <AuditOutcome entry={e} />
            <span class="grow small ellipsis">{auditActorLabel(e, catalog.accounts)}</span>
            <span class="small muted nowrap" title={fmtDate(e.ts)}><RelTime ts={e.ts} /></span>
          </button>
          {#if open[e.auditId]}
            <div class="body">
              {#if e.outcome === 'error' && e.errorMessage}<p class="small text-red" style="margin:0 0 8px">{e.errorCode ? `${e.errorCode}: ` : ''}{e.errorMessage}</p>{/if}
              <AuditChanges changes={e.changes} total={e.changesTotal} />
              {#if Object.keys(e.data).length}<pre class="code wrap" style="margin-top:8px">{JSON.stringify(e.data, null, 2)}</pre>{/if}
            </div>
          {/if}
        </li>
      {/each}
    </ul>
    {#if log.error}<p class="small text-red" style="margin-top:8px">{log.error}</p>{/if}
    <div class="more">
      {#if log.hasMore}<Button size="sm" loading={log.loadingMore} onclick={() => log.more()} data-testid="history-load-more">Load more</Button>{:else}<span class="faint small">{log.items.length} entr{log.items.length === 1 ? 'y' : 'ies'}</span>{/if}
    </div>
  {/if}
</div>

<style>
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--sp-2);
  }
  .item {
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    background: var(--surface);
  }
  .head {
    display: flex;
    align-items: center;
    gap: var(--sp-2);
    width: 100%;
    padding: var(--sp-2) var(--sp-3);
    border: 0;
    background: transparent;
    cursor: pointer;
    text-align: left;
    color: var(--text);
    min-width: 0;
  }
  .head:hover {
    background: var(--surface-hover);
  }
  .body {
    padding: 0 var(--sp-3) var(--sp-3);
  }
  .more {
    display: flex;
    justify-content: center;
    margin-top: var(--sp-3);
  }
</style>
