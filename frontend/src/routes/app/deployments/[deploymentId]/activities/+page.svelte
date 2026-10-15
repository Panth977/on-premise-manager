<script lang="ts">
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { onMount } from 'svelte';
  import { ACTIVITY_TYPES, type Activity, type ActivitiesQuery, type ActivityOrigin } from '@opm/shared';
  import { api } from '$lib/api';
  import { PagedLoader } from '$lib/resource.svelte';
  import { getDeploymentScope } from '$lib/config-editor/context';
  import { personName, siteName } from '$lib/util/people';
  import { fmtDate, jsonPretty } from '$lib/util/format';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Drawer from '$lib/ui/Drawer.svelte';
  import TableSkeleton from '$lib/ui/TableSkeleton.svelte';
  import ErrorState from '$lib/ui/ErrorState.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import RelTime from '$lib/ui/RelTime.svelte';
  import IdChip from '$lib/ui/IdChip.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Combobox, { type ComboOption } from '$lib/ui/Combobox.svelte';
  import Select, { type SelectGroup, type SelectOption } from '$lib/ui/Select.svelte';
  import { activityTypeLabel } from '$lib/audit/audit';
  import { catalog } from '$lib/stores/catalog.svelte';
  import { ScrollText, Upload, X, SlidersHorizontal, Rocket, CheckCircle, CircleAlert, Undo2, RotateCcw, Settings, Download, Send, Ban, Lightbulb, type IconNode } from '$lib/ui/icons';
  import { bytes } from '$lib/util/format';
  import AnsiLog from '$lib/ui/AnsiLog.svelte';
  import InlineCode from '$lib/ui/InlineCode.svelte';

  const scope = getDeploymentScope();
  const id = scope.id;
  const canRead = $derived(scope.can('deployment.activities', 'read'));
  const canWrite = $derived(scope.can('deployment.activities', 'write'));

  type F = { site: string; origin: string; type: string; actor: string; from: string; to: string; via: string };
  const KEYS: (keyof F)[] = ['site', 'origin', 'type', 'actor', 'from', 'to', 'via'];
  function fromUrl(): F {
    const q = page.url.searchParams;
    return Object.fromEntries(KEYS.map((k) => [k, q.get(k) ?? ''])) as F;
  }
  let f = $state<F>(fromUrl());
  const toIso = (v: string) => (v ? new Date(v).toISOString() : undefined);
  function query(cursor: string | null): ActivitiesQuery {
    return {
      site: f.site || undefined,
      origin: (f.origin || undefined) as ActivityOrigin | undefined,
      type: f.type.trim() || undefined,
      actor: f.actor.trim() || undefined,
      from: toIso(f.from),
      to: toIso(f.to),
      via: (f.via || undefined) as 'pipe' | 'manual' | undefined,
      cursor: cursor ?? undefined,
      limit: Math.min(500, Math.max(1, Number(page.url.searchParams.get('limit')) || 50)),
    };
  }
  const list = new PagedLoader<Activity>((cursor) => api.listActivities({ deploymentId: id, query: query(cursor) }));
  onMount(() => {
    if (canRead) list.reset();
    scope.loadStatus();
    catalog.ensure();
  });

  let debounce: ReturnType<typeof setTimeout> | null = null;
  function apply() {
    const q = new URLSearchParams();
    for (const k of KEYS) if (f[k]) q.set(k, f[k]);
    const lim = page.url.searchParams.get('limit');
    if (lim) q.set('limit', lim);
    const s = q.toString();
    goto(`${page.url.pathname}${s ? `?${s}` : ''}`, { replaceState: true, keepFocus: true, noScroll: true });
    list.reset();
  }
  function changed(text = false) {
    if (debounce) clearTimeout(debounce);
    debounce = setTimeout(apply, text ? 350 : 0);
  }
  function clearAll() {
    f = { site: '', origin: '', type: '', actor: '', from: '', to: '', via: '' };
    apply();
  }
  const active = $derived(KEYS.filter((k) => f[k]).length);
  const prefixes = [...new Set(ACTIVITY_TYPES.filter((t) => t.includes('.')).map((t) => t.split('.')[0] + '.'))].sort();
  const types = [...ACTIVITY_TYPES].sort();
  /** actor = cloud account uid or an on-site e-mail (agent actors are not accounts) → free-text combobox */
  const actorOptions = $derived.by<ComboOption[]>(() => {
    const out: ComboOption[] = catalog.accounts.map((a) => ({ value: a.uid, label: a.name, sub: a.email, detail: 'cloud account' }));
    const seen = new Set(out.map((o) => o.value));
    for (const a of list.items) if (a.actor && !seen.has(a.actor)) (seen.add(a.actor), out.push({ value: a.actor, label: personName(a.actor), sub: personName(a.actor) !== a.actor ? a.actor : undefined, detail: a.origin === 'agent' ? 'on site' : a.origin }));
    return out;
  });
  const sites = $derived([...new Set([...scope.config.sites.map((s) => s.siteSlug), ...(scope.status?.sites.map((s) => s.siteSlug) ?? [])])].sort());
  /** single-value filters (the API takes one value each); '' = no filter */
  const siteOptions = $derived<SelectOption[]>([{ value: '', label: 'All sites' }, { value: 'cloud', label: 'cloud' }, ...sites.filter((s) => s !== 'cloud').map((s) => ({ value: s, label: s }))]);
  const ORIGIN_OPTIONS: SelectOption[] = [{ value: '', label: 'Any origin' }, { value: 'agent', label: 'agent' }, { value: 'cloud', label: 'cloud' }, { value: 'system', label: 'system' }];
  const VIA_OPTIONS: SelectOption[] = [{ value: '', label: 'Any upload' }, { value: 'pipe', label: 'pipe' }, { value: 'manual', label: 'manual (.opmlog)' }];
  const typeGroups = $derived<SelectGroup[]>([
    { options: [{ value: '', label: 'Any type' }, ...(f.type && !prefixes.includes(f.type) && !(types as string[]).includes(f.type) ? [{ value: f.type, label: f.type, mono: true }] : [])] },
    { label: 'Groups', options: prefixes.map((p) => ({ value: p, label: `${p}*`, mono: true })) },
    { label: 'Types', options: types.map((t) => ({ value: t, label: t, sub: activityTypeLabel(t), mono: true })) },
  ]);

  /** §21 #90 agent self-management activities: icon + tone */
  const AGENT_ICON: Record<string, [IconNode, string]> = {
    'agent.upgrade-started': [Rocket, 'blue'],
    'agent.upgraded': [CheckCircle, 'green'],
    'agent.upgrade-failed': [CircleAlert, 'red'],
    'agent.rolled-back': [Undo2, 'amber'],
    'agent.restarted': [RotateCcw, 'blue'],
    'agent.config-changed': [Settings, 'violet'],
    'agent.support-bundle-downloaded': [Download, 'blue'],
    'agent.upgrade-requested': [Send, 'blue'],
    'agent.upgrade-cancelled': [Ban, 'amber'],
  };
  const VIA_LABEL: Record<string, string> = { gui: 'agent GUI', cloud: 'cloud console', cli: 'opm-agent upgrade (CLI)', mesh: 'upgrade all (agent)', systemd: 'systemd', exec: 're-exec' };
  const BY_LABEL: Record<string, string> = { watcher: 'upgrade watcher (opm-agent.prev)', guard: 'boot guard (systemd ExecStartPre)' };
  const str = (v: unknown): string | null => (typeof v === 'string' && v ? v : typeof v === 'number' ? String(v) : null);
  const valText = (v: unknown) => (v === undefined ? '—' : typeof v === 'string' ? v : JSON.stringify(v));

  let sel = $state<Activity | null>(null);
  let drawerOpen = $state(false);
  function open(a: Activity) {
    sel = a;
    drawerOpen = true;
  }
</script>

<svelte:head><title>Activities · {scope.label} · OPM</title></svelte:head>

<div class="page">
  <PageHeader title="Activities" subtitle="The audit trail of every site (hash-chained, uploaded by the agents) and of the cloud, newest first." icon={ScrollText}>
    {#snippet actions()}
      {#if canWrite}<Button icon={Upload} href="/app/deployments/{id}/activities/upload" data-testid="upload-bundle">Upload a bundle</Button>{/if}
    {/snippet}
  </PageHeader>

  {#if !canRead}
    <EmptyState icon={ScrollText} title="No access" text="You need deployment.activities.read to see the activities." />
  {:else}
    <div class="card filters" data-testid="activity-filters">
      <span class="fi muted small"><Icon icon={SlidersHorizontal} size={14} />Filter</span>
      <div class="fsel"><Select value={f.site} options={siteOptions} fallback={f.site || null} size="sm" ariaLabel="Site" testid="f-site" onchange={(v) => ((f.site = v), changed())} /></div>
      <div class="fsel"><Select value={f.origin} options={ORIGIN_OPTIONS} size="sm" ariaLabel="Origin" testid="f-origin" onchange={(v) => ((f.origin = v), changed())} /></div>
      <div class="fsel type"><Select value={f.type} groups={typeGroups} size="sm" ariaLabel="Type" testid="f-type" minWidth={320} onchange={(v) => ((f.type = v), changed())} /></div>
      <div class="actor"><Combobox value={f.actor} options={actorOptions} freeText clearable size="sm" placeholder="Any actor" ariaLabel="Actor" emptyText="Type an e-mail or uid" testid="f-actor" onselect={(o) => ((f.actor = o.value), changed())} oninput={(t) => ((f.actor = t), changed(true))} /></div>
      <label class="dt small muted">From <input class="input sm" type="datetime-local" bind:value={f.from} onchange={() => changed()} /></label>
      <label class="dt small muted">To <input class="input sm" type="datetime-local" bind:value={f.to} onchange={() => changed()} /></label>
      <div class="fsel"><Select value={f.via} options={VIA_OPTIONS} size="sm" ariaLabel="Uploaded via" testid="f-via" onchange={(v) => ((f.via = v), changed())} /></div>
      {#if active}<Button size="sm" variant="ghost" icon={X} onclick={clearAll} data-testid="f-clear">Clear ({active})</Button>{/if}
    </div>

    {#if list.loading && !list.loaded}
      <TableSkeleton cols={8} rows={8} />
    {:else if list.error && !list.items.length}
      <ErrorState message={list.error} onretry={() => list.reset()} />
    {:else if !list.items.length}
      <EmptyState icon={ScrollText} title={active ? 'No activity matches the filters' : 'No activities yet'} text={active ? 'Change or clear the filters.' : 'Activities appear when the cloud or a site does something.'}>
        {#snippet action()}{#if active}<Button onclick={clearAll}>Clear filters</Button>{/if}{/snippet}
      </EmptyState>
    {:else}
      <div class="table-wrap">
        <table class="table" data-testid="activities-table">
          <thead><tr><th>Time</th><th>Site</th><th class="num">Seq</th><th>Origin</th><th>Actor</th><th>Type</th><th>Summary</th><th>Via</th></tr></thead>
          <tbody>
            {#each list.items as a (a.activityId)}
              <tr class="clickable" tabindex="0" onclick={() => open(a)} onkeydown={(e) => (e.key === 'Enter' || e.key === ' ') && (e.preventDefault(), open(a))} data-testid="activity-row">
                <td class="small nowrap"><RelTime ts={a.ts} /></td>
                <td><span class="chip">{siteName(a.siteSlug)}</span></td>
                <td class="num mono small">{a.seq}</td>
                <td class="small">{a.origin}</td>
                <td class="small ellipsis" style="max-width:160px">{a.actor ? personName(a.actor) : '—'}</td>
                <td class="nowrap">{#if AGENT_ICON[a.type]}<span class="aic {AGENT_ICON[a.type]![1]}" data-testid="activity-icon"><Icon icon={AGENT_ICON[a.type]![0]} size={13} /></span>{/if}<code class="mono xs" data-tip={activityTypeLabel(a.type)}>{a.type}</code></td>
                <td class="ellipsis" style="max-width:360px">{a.summary}</td>
                <td class="small faint">{a.uploadedVia ?? '—'}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      <div class="more">
        {#if list.hasMore}<Button onclick={() => list.more()} loading={list.loadingMore} data-testid="load-more">Load more</Button>{:else}<span class="faint small">{list.items.length} activities · end of the list</span>{/if}
      </div>
    {/if}
  {/if}
</div>


<Drawer bind:open={drawerOpen} title={sel ? sel.summary : ''} subtitle={sel ? `${siteName(sel.siteSlug)} · seq ${sel.seq} · ${sel.type}` : ''} testid="activity-drawer">
  {#if sel}
    <dl class="kv">
      <dt>Activity</dt><dd><IdChip id={sel.activityId} /></dd>
      <dt>Time</dt><dd>{fmtDate(sel.ts)} <span class="faint xs mono">{sel.ts}</span></dd>
      <dt>Site</dt><dd>{siteName(sel.siteSlug)}</dd>
      <dt>Seq</dt><dd class="mono">{sel.seq}</dd>
      <dt>Origin</dt><dd>{sel.origin}</dd>
      <dt>Actor</dt><dd>{sel.actor ? personName(sel.actor) : 'system'}{#if sel.actor && personName(sel.actor) !== sel.actor}<span class="faint xs mono"> {sel.actor}</span>{/if}</dd>
      <dt>Type</dt><dd><code class="mono">{sel.type}</code> <span class="muted small">{activityTypeLabel(sel.type)}</span></dd>
      <dt>Uploaded</dt><dd>{sel.uploadedVia ?? '—'}{#if sel.uploadedAt} · <RelTime ts={sel.uploadedAt} />{/if}</dd>
      <dt>Prev hash</dt><dd>{#if sel.prevHash}<IdChip id={sel.prevHash} short={24} />{:else}<span class="faint">null (first entry)</span>{/if}</dd>
      <dt>Hash</dt><dd><IdChip id={sel.hash} short={24} /></dd>
    </dl>
    {#if sel.type === 'update.failed' && Array.isArray(sel.data?.failures)}
      <h4 class="dh">Failed containers</h4>
      {#each sel.data.failures as f, i (i)}
        <div class="failbox" data-testid="activity-failure">
          <div><strong class="mono">{f.containerSlug}</strong>{#if f.exitCode !== undefined}{' '}<span class="muted small">exit code {f.exitCode}{f.oomKilled ? ' · OOM killed' : ''} · {f.restartCount} restarts</span>{/if}</div>
          <div class="small">{f.error}</div>
          {#if typeof f.hint === 'string' && f.hint}<div class="banner warn hint" data-testid="activity-failure-hint"><Icon icon={Lightbulb} size={16} class="icon" /><div class="small"><InlineCode text={f.hint} /></div></div>{/if}
          {#if f.keptAs}<div class="small muted">kept on the server as <code>{f.keptAs}</code> (<code>docker logs {f.keptAs}</code>)</div>{/if}
          {#if Array.isArray(f.logTail) && f.logTail.length}<AnsiLog lines={f.logTail.map(String)} testid="activity-logtail" filename="{f.containerSlug ?? 'container'}-{sel.seq}.log" maxHeight="260px" />{/if}
        </div>
      {/each}
    {/if}
    {#if AGENT_ICON[sel.type]}
      {@const d = (sel.data ?? {}) as Record<string, unknown>}
      <h4 class="dh row" style="gap:8px"><span class="aic {AGENT_ICON[sel.type]![1]}"><Icon icon={AGENT_ICON[sel.type]![0]} size={14} /></span>{activityTypeLabel(sel.type)}</h4>
      <dl class="kv" data-testid="activity-agent">
        {#if str(d.from) || str(d.to)}<dt>{sel.type === 'agent.rolled-back' ? 'Failed → restored' : 'From → to'}</dt><dd class="mono" data-testid="activity-agent-fromto">{str(d.from) ?? '?'} → {str(d.to) ?? '?'}</dd>{/if}
        {#if str(d.version)}<dt>Version</dt><dd class="mono">{d.version}</dd>{/if}
        {#if str(d.siteSlug)}<dt>Site</dt><dd class="mono">{d.siteSlug}</dd>{/if}
        {#if str(d.via)}<dt>Via</dt><dd>{VIA_LABEL[String(d.via)] ?? d.via}</dd>{/if}
        {#if str(d.phase)}<dt>Phase</dt><dd data-testid="activity-agent-phase">{d.phase}</dd>{/if}
        {#if str(d.error)}<dt>Error</dt><dd class="text-red" data-testid="activity-agent-error">{d.error}</dd>{/if}
        {#if str(d.reason)}<dt>Reason</dt><dd>{d.reason}</dd>{/if}
        {#if str(d.by)}<dt>Rolled back by</dt><dd>{BY_LABEL[String(d.by)] ?? d.by}</dd>{/if}
        {#if typeof d.durationSec === 'number'}<dt>Duration</dt><dd>{d.durationSec} s</dd>{/if}
        {#if typeof d.sizeBytes === 'number'}<dt>Size</dt><dd>{bytes(d.sizeBytes)}</dd>{/if}
        {#if Array.isArray(d.sections)}<dt>Sections</dt><dd class="row wrap" style="gap:4px">{#each d.sections as x, i (i)}<span class="chip mono">{x}</span>{/each}</dd>{/if}
        {#if Array.isArray(d.sites)}<dt>Sites</dt><dd class="mono">{d.sites.join(', ')}</dd>{/if}
        {#if str(d.requestId)}<dt>Request</dt><dd><IdChip id={String(d.requestId)} short={14} /></dd>{/if}
        {#if str(d.runId)}<dt>Run</dt><dd><IdChip id={String(d.runId)} short={14} /></dd>{/if}
        {#if str(d.upgradeId)}<dt>Upgrade</dt><dd><IdChip id={String(d.upgradeId)} short={14} /></dd>{/if}
      </dl>
      {#if Array.isArray(d.changed) && d.changed.length}
        {@const before = (d.before ?? {}) as Record<string, unknown>}
        {@const after = (d.after ?? {}) as Record<string, unknown>}
        <table class="table compact chg" data-testid="activity-agent-changes">
          <thead><tr><th>Setting</th><th>Before</th><th>After</th></tr></thead>
          <tbody>{#each d.changed as k (k)}<tr><td class="mono small">{k}</td><td class="mono small">{valText(before[String(k)])}</td><td class="mono small">{valText(after[String(k)])}</td></tr>{/each}</tbody>
        </table>
      {/if}
    {/if}
    <h4 class="dh">Data</h4>
    <pre class="code wrap" data-testid="activity-data">{jsonPretty(sel.data)}</pre>
  {/if}
</Drawer>

<style>
  .aic {
    display: inline-grid;
    place-items: center;
    width: 20px;
    height: 20px;
    border-radius: 6px;
    margin-right: 6px;
    vertical-align: middle;
    background: var(--surface-2);
    color: var(--text-2);
  }
  .aic.blue {
    background: var(--blue-soft);
    color: var(--blue-text);
  }
  .aic.green {
    background: var(--green-soft);
    color: var(--green-text);
  }
  .aic.red {
    background: var(--red-soft);
    color: var(--red-text);
  }
  .aic.amber {
    background: var(--amber-soft);
    color: var(--amber-text);
  }
  .aic.violet {
    background: var(--violet-soft, var(--accent-soft));
    color: var(--violet-text, var(--accent-text));
  }
  .chg {
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    margin-top: 8px;
  }
  .failbox {
    display: grid;
    gap: 4px;
    margin-bottom: 12px;
  }
  .hint {
    margin: 2px 0;
  }
  .filters {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
    padding: 10px 12px;
    margin-bottom: var(--sp-4);
  }
  .filters :global(.input) {
    width: auto;
    min-width: 120px;
  }
  .fsel {
    width: 140px;
  }
  .fsel.type {
    width: 200px;
  }
  .actor {
    width: 220px;
  }
  .fi {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-weight: var(--fw-medium);
  }
  .dt {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  .more {
    display: flex;
    justify-content: center;
    padding: var(--sp-4) 0;
  }
  .dh {
    margin: var(--sp-5) 0 var(--sp-2);
  }
  @media (max-width: 560px) {
    .filters :global(.input),
    .fsel,
    .fsel.type,
    .actor {
      width: 100%;
      min-width: 0;
    }
    .dt {
      width: 100%;
    }
  }
</style>
