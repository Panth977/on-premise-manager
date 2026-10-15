<script lang="ts">
  import AccountPicker from '$lib/ui/AccountPicker.svelte';
  import Select, { type SelectGroup, type SelectOption } from '$lib/ui/Select.svelte';
  import { AUDIT_ACTIONS, AUDIT_TARGET_TYPES, type AuditLogEntry, type AuditLogQuery, type AuditTargetType } from '@opm/shared';
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { api } from '$lib/api';
  import { PagedLoader } from '$lib/resource.svelte';
  import { session } from '$lib/session.svelte';
  import { catalog } from '$lib/stores/catalog.svelte';
  import { fmtDate } from '$lib/util/format';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Drawer from '$lib/ui/Drawer.svelte';
  import TableSkeleton from '$lib/ui/TableSkeleton.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import ErrorState from '$lib/ui/ErrorState.svelte';
  import RelTime from '$lib/ui/RelTime.svelte';
  import { ScrollText, Lock, X, RefreshCw } from '$lib/ui/icons';
  import AuditOutcome from '$lib/audit/AuditOutcome.svelte';
  import AuditEntryDetail from '$lib/audit/AuditEntryDetail.svelte';
  import { AUDIT_ACTION_PREFIXES, AUDIT_TARGET_TYPE_LABELS, auditActionLabel, auditActionTone, auditActorLabel, auditTargetHref, normalizeAudit } from '$lib/audit/audit';

  const admin = $derived(session.isAdmin);

  // ── filters live in the URL (?action=&actor=&type=&outcome=&from=&to=&limit=)
  const sp = $derived(page.url.searchParams);
  const fAction = $derived(sp.get('action') ?? '');
  const fActor = $derived(sp.get('actor') ?? '');
  const fType = $derived(sp.get('type') ?? '');
  const fOutcome = $derived(sp.get('outcome') ?? '');
  const actionGroups = $derived<SelectGroup[]>([
    { options: [{ value: '', label: 'All actions' }, ...(fAction && !AUDIT_ACTION_PREFIXES.includes(fAction) && !(AUDIT_ACTIONS as readonly string[]).includes(fAction) ? [{ value: fAction, label: fAction, mono: true }] : [])] },
    { label: 'Groups', options: AUDIT_ACTION_PREFIXES.map((p) => ({ value: p, label: `${p}*`, mono: true })) },
    { label: 'Actions', options: AUDIT_ACTIONS.map((a) => ({ value: a, label: a, sub: auditActionLabel(a), mono: true })) },
  ]);
  const TYPE_OPTIONS: SelectOption[] = [{ value: '', label: 'All types' }, ...AUDIT_TARGET_TYPES.map((t) => ({ value: t, label: AUDIT_TARGET_TYPE_LABELS[t] }))];
  const OUTCOME_OPTIONS: SelectOption[] = [{ value: '', label: 'All' }, { value: 'ok', label: 'ok' }, { value: 'error', label: 'error' }];
  const fFrom = $derived(sp.get('from') ?? '');
  const fTo = $derived(sp.get('to') ?? '');
  const limit = $derived(Number(sp.get('limit')) || 50);
  const anyFilter = $derived(!!(fAction || fActor || fType || fOutcome || fFrom || fTo));

  /** yyyy-mm-dd (local day) → ISO start / end of that day */
  const dayStart = (d: string) => (d ? new Date(`${d}T00:00:00`).toISOString() : undefined);
  const dayEnd = (d: string) => (d ? new Date(`${d}T23:59:59.999`).toISOString() : undefined);
  const query = $derived<AuditLogQuery>({
    action: fAction || undefined,
    actor: fActor || undefined,
    targetType: (AUDIT_TARGET_TYPES as readonly string[]).includes(fType) ? (fType as AuditTargetType) : undefined,
    outcome: fOutcome === 'ok' || fOutcome === 'error' ? fOutcome : undefined,
    from: dayStart(fFrom),
    to: dayEnd(fTo),
    limit,
  });
  const log = new PagedLoader<AuditLogEntry>((cursor) => api.listAuditLog({ query: { ...query, cursor: cursor ?? undefined } }).then((r) => ({ items: r.items.map(normalizeAudit), cursor: r.cursor })));

  // reload from the API whenever the filters change
  const queryKey = $derived(JSON.stringify(query));
  $effect(() => {
    void queryKey;
    if (!admin) return;
    untrack(() => {
      log.reset();
      catalog.ensure();
    });
  });

  function setFilter(k: string, v: string) {
    const u = new URL(page.url);
    if (v) u.searchParams.set(k, v);
    else u.searchParams.delete(k);
    goto(`${u.pathname}${u.search}`, { replaceState: true, keepFocus: true, noScroll: true });
  }
  function clearFilters() {
    const u = new URL(page.url);
    for (const k of ['action', 'actor', 'type', 'outcome', 'from', 'to']) u.searchParams.delete(k);
    goto(`${u.pathname}${u.search}`, { replaceState: true, keepFocus: true, noScroll: true });
  }
  /** the actor filter as a picker value: uid / "system" (legacy e-mail URLs map to the uid) */
  const actorValue = $derived(fActor ? [fActor.includes('@') ? (catalog.accounts.find((a) => a.email === fActor)?.uid ?? fActor) : fActor] : []);

  let sel = $state<AuditLogEntry | null>(null);
  let drawerOpen = $state(false);
  function openEntry(e: AuditLogEntry) {
    sel = e;
    drawerOpen = true;
  }
</script>

<svelte:head><title>Audit log · OPM</title></svelte:head>
<div class="page">
  <PageHeader
    title="Audit log"
    subtitle="Every root-scope request, successful or refused: registries, services, images, 404 pages, templates, tags, accounts, deployments (create / update / delete), uploads, and system jobs, with the before → after changes. Actions inside a deployment are in that deployment's Activities."
    icon={ScrollText}
  >
    {#snippet actions()}{#if admin}<Button icon={RefreshCw} onclick={() => log.reset()} loading={log.loading && log.loaded} data-testid="audit-refresh">Refresh</Button>{/if}{/snippet}
  </PageHeader>
  {#if !admin}
    <EmptyState icon={Lock} title="Admins only" text="The audit log needs accounts.admin." />
  {:else}
    <div class="filters" data-testid="audit-filters">
      <div class="f fs">
        <span class="xs faint">Action</span>
        <Select value={fAction} groups={actionGroups} size="sm" ariaLabel="Action" testid="audit-filter-action" minWidth={320} onchange={(v) => setFilter('action', v)} />
      </div>
      <div class="f actorf">
        <span class="xs faint">Actor</span>
        <AccountPicker single value={actorValue} extra={[{ value: 'system', label: 'system', hint: 'jobs (imports, cleanup)', icon: 'lucide:cpu' }]} placeholder="Anyone" onchange={(v) => setFilter('actor', v[0] ?? '')} testid="audit-filter-actor" />
      </div>
      <div class="f fs">
        <span class="xs faint">Target type</span>
        <Select value={fType} options={TYPE_OPTIONS} fallback={fType || null} size="sm" ariaLabel="Target type" testid="audit-filter-type" onchange={(v) => setFilter('type', v)} />
      </div>
      <div class="f fs">
        <span class="xs faint">Outcome</span>
        <Select value={fOutcome} options={OUTCOME_OPTIONS} fallback={fOutcome || null} size="sm" ariaLabel="Outcome" testid="audit-filter-outcome" onchange={(v) => setFilter('outcome', v)} />
      </div>
      <label class="f">
        <span class="xs faint">From</span>
        <input type="date" class="input sm" value={fFrom} max={fTo || undefined} onchange={(e) => setFilter('from', e.currentTarget.value)} data-testid="audit-filter-from" />
      </label>
      <label class="f">
        <span class="xs faint">To</span>
        <input type="date" class="input sm" value={fTo} min={fFrom || undefined} onchange={(e) => setFilter('to', e.currentTarget.value)} data-testid="audit-filter-to" />
      </label>
      {#if anyFilter}<Button size="sm" variant="ghost" icon={X} onclick={clearFilters} data-testid="audit-clear">Clear filters</Button>{/if}
    </div>

    {#if log.loading && !log.items.length}
      <TableSkeleton cols={5} rows={8} />
    {:else if log.error && !log.items.length}
      <ErrorState message={log.error} onretry={() => log.reset()} />
    {:else if !log.items.length}
      {#if anyFilter}
        <EmptyState icon={ScrollText} title="No matching entries" text="No audit entry matches these filters.">
          {#snippet action()}<Button onclick={clearFilters}>Clear filters</Button>{/snippet}
        </EmptyState>
      {:else}
        <EmptyState icon={ScrollText} title="Nothing logged yet" />
      {/if}
    {:else}
      <div class="table-wrap" class:dim={log.loading}>
        <table class="table" data-testid="audit-table">
          <thead><tr><th>Time</th><th>Actor</th><th>Action</th><th>Target</th><th>Outcome</th></tr></thead>
          <tbody>
            {#each log.items as e (e.auditId)}
              {@const href = auditTargetHref(e.target)}
              <tr class="clickable" data-testid="audit-row" data-action={e.action} data-outcome={e.outcome} onclick={() => openEntry(e)}>
                <td class="nowrap small"><RelTime ts={e.ts} /><div class="faint xs">{fmtDate(e.ts)}</div></td>
                <td class="small">
                  {#if e.actor === 'system'}<span class="badge outline">system</span>{:else}<span class="ellipsis actor" title={e.actor}>{auditActorLabel(e, catalog.accounts)}</span>{/if}
                </td>
                <td><button type="button" class="linkish" onclick={(ev) => (ev.stopPropagation(), openEntry(e))} aria-label="Open {e.action} entry"><span class="badge {auditActionTone(e.action)}" data-tip={auditActionLabel(e.action)}>{e.action}</span></button></td>
                <td class="mono small tgt">{#if href}<a {href} onclick={(ev) => ev.stopPropagation()}>{e.target}</a>{:else}{e.target}{/if}</td>
                <td><AuditOutcome entry={e} /></td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      {#if log.error}<p class="small text-red" style="margin-top:8px">{log.error}</p>{/if}
      <div class="more">
        {#if log.hasMore}<Button loading={log.loadingMore} onclick={() => log.more()} data-testid="load-more">Load more</Button>{:else}<span class="faint small">End of the log · {log.items.length} entries</span>{/if}
      </div>
    {/if}
  {/if}
</div>

<Drawer bind:open={drawerOpen} title={sel ? sel.action : 'Audit entry'} subtitle={sel ? fmtDate(sel.ts) : undefined} width="wide" testid="audit-drawer">
  {#if sel}<AuditEntryDetail entry={sel} />{/if}
</Drawer>

<style>
  .filters {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-end;
    gap: var(--sp-3);
    margin-bottom: var(--sp-4);
  }
  .f {
    display: flex;
    flex-direction: column;
    gap: 3px;
    min-width: 0;
  }
  .fs {
    width: 190px;
  }
  .f .input {
    min-width: 150px;
  }
  .actorf {
    width: 230px;
  }
  .actor {
    display: inline-block;
    max-width: 220px;
  }
  .tgt {
    max-width: 320px;
    word-break: break-all;
  }
  .linkish {
    border: 0;
    background: transparent;
    padding: 0;
    cursor: pointer;
  }
  .dim {
    opacity: 0.6;
    transition: opacity var(--dur-fast);
  }
  .more {
    display: flex;
    justify-content: center;
    margin-top: 16px;
  }
</style>
