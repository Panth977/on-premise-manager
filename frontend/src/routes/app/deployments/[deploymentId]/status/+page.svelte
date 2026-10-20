<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import type { StatusResponse } from '@opm/shared';
  import { api } from '$lib/api';
  import { Loader } from '$lib/resource.svelte';
  import { getDeploymentScope } from '$lib/config-editor/context';
  import { pct } from '$lib/util/format';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Button from '$lib/ui/Button.svelte';
  import TableSkeleton from '$lib/ui/TableSkeleton.svelte';
  import ErrorState from '$lib/ui/ErrorState.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import RelTime from '$lib/ui/RelTime.svelte';
  import ClockSkew from '$lib/ui/ClockSkew.svelte';
  import BrowserClockNote from '$lib/ui/BrowserClockNote.svelte';
  import StatusBadge from '$lib/ui/StatusBadge.svelte';
  import ProgressBar from '$lib/ui/ProgressBar.svelte';
  import Heatmap, { type HeatCell } from '$lib/ui/Heatmap.svelte';
  import { Activity, ExternalLink, RefreshCw, Server } from '$lib/ui/icons';
  import { openRemoteGui } from '$lib/remote-gui/open';
  import UpgradeBadge from '$lib/agent-upgrade/UpgradeBadge.svelte';

  const scope = getDeploymentScope();
  const id = scope.id;
  const st = new Loader(() => api.getStatus({ deploymentId: id }));
  let timer: ReturnType<typeof setInterval> | null = null;
  onMount(() => {
    st.load();
    timer = setInterval(() => {
      if (!document.hidden) st.load(true);
    }, 15_000);
  });
  onDestroy(() => timer && clearInterval(timer));

  type Up = StatusResponse['uptime'][number];
  const fmtDate = (d: string) => `${d.slice(0, 4)}-${d.slice(4, 6)}-${d.slice(6, 8)}`;
  /** hourly cells of the 30 days; hours before the first reported hour and future hours = no data (null) */
  function hourly(u: Up, serverTime: string): HeatCell[][] {
    const curH = new Date(serverTime).getUTCHours();
    const flat = u.days.flatMap((d) => d.hours);
    const first = flat.findIndex((h) => h > 0);
    return u.days.map((d, di) =>
      d.hours.map((h, hi) => {
        const k = di * 24 + hi;
        const future = di === u.days.length - 1 && hi > curH;
        return { label: `${fmtDate(d.date)} ${String(hi).padStart(2, '0')}:00 UTC`, value: future || first < 0 || k < first ? null : h };
      }),
    );
  }
  function days30(u: Up, serverTime: string): HeatCell[] {
    return hourly(u, serverTime).map((hs, i) => ({ label: fmtDate(u.days[i]!.date), value: mean(hs) }));
  }
  function hours24(u: Up, serverTime: string): HeatCell[] {
    const all = hourly(u, serverTime).flat();
    const curH = new Date(serverTime).getUTCHours();
    const end = (u.days.length - 1) * 24 + curH + 1;
    return all.slice(Math.max(0, end - 24), end);
  }
  const mean = (cells: HeatCell[]) => {
    const v = cells.filter((c) => c.value !== null);
    return v.length ? v.reduce((a, c) => a + (c.value ?? 0), 0) / v.length : null;
  };
</script>

<svelte:head><title>Status · {scope.label} · OPM</title></svelte:head>

<div class="page">
  <PageHeader title="Status" subtitle="Live state reported by the agents (heartbeat every 30 s; online = seen in the last 90 s). Refreshes every 15 s." icon={Activity}>
    {#snippet actions()}
      <Button icon={RefreshCw} loading={st.loading} onclick={() => st.load(true)} data-testid="refresh-status">Refresh</Button>
    {/snippet}
  </PageHeader>
  <BrowserClockNote />

  {#if st.loading && !st.data}
    <TableSkeleton cols={9} rows={2} />
  {:else if st.error && !st.data}
    <ErrorState message={st.error} onretry={() => st.load()} />
  {:else if st.data}
    {@const s = st.data}
    {#if !s.sites.some((x) => x.enrolled)}
      <EmptyState icon={Server} title="No site is installed yet" text="Install the agent on each site from the Setup page; its status appears here.">
        {#snippet action()}<Button variant="primary" href="/app/deployments/{id}/setup">Go to Setup</Button>{/snippet}
      </EmptyState>
    {/if}
    {#if s.sites.length}
      <div class="table-wrap section" style="margin-top:{s.sites.some((x) => x.enrolled) ? 0 : 'var(--sp-4)'}">
        <table class="table" data-testid="status-table">
          <thead><tr><th>Site</th><th>Online</th><th>Last seen</th><th>Via</th><th>Agent</th><th class="num">Head</th><th>Staging</th><th class="num">Key</th><th>CPU / Mem / Disk</th><th>Edge</th></tr></thead>
          <tbody>
            {#each s.sites as x (x.siteSlug)}
              <tr data-testid="status-row-{x.siteSlug}">
                <td>
                  <div class="strong mono">{x.siteSlug}</div>
                  <div class="xs faint">{x.label}</div>
                  <!-- §21 #118 the site's agent GUI through the cloud (the site asks the cloud for the requests) -->
                  <Button size="sm" variant="ghost" icon={ExternalLink} onclick={() => openRemoteGui(id, x.siteSlug)} tip="Open this site's agent GUI here in the cloud, as your account (agent 0.19.0 or newer)" data-testid="open-remote-{x.siteSlug}">Open agent GUI</Button>
                </td>
                <td>{#if !x.enrolled}<span class="badge">not installed</span>{:else}<span class="row" style="gap:6px"><span class="dot {x.online ? 'green' : 'red'}"></span>{x.online ? 'online' : 'offline'}</span>{/if}</td>
                <td class="small"><RelTime ts={x.lastSeenAt} />{#if x.clockSkew}<div><ClockSkew skew={x.clockSkew} compact /></div>{/if}</td>
                <td class="small">{#if !x.enrolled}<span class="faint">—</span>{:else if x.relayVia}relay via <b>{x.relayVia}</b>{:else}direct{/if}</td>
                <td class="small" data-testid="status-agent">
                  {#if x.agentVersion}
                    <div class="mono nowrap">v{x.agentVersion}</div>
                    {#if x.upgrade}
                      <a class="ag" href="/app/deployments/{id}/setup?site={encodeURIComponent(x.siteSlug)}" data-testid="status-upgrade-link" data-tip={x.upgrade.available ? (x.upgrade.supported ? `Upgrade to ${x.upgrade.latest} on the Setup page` : (x.upgrade.hint ?? 'Rerun the install command once')) : 'Agent on the Setup page'}><UpgradeBadge view={x.upgrade} /></a>
                    {/if}
                  {:else}—{/if}
                </td>
                <td class="num">{x.headSeq ?? '—'}</td>
                <td style="min-width:130px">
                  {#if x.staging}
                    <div class="stack tight"><StatusBadge kind="staging" value={x.staging.state} /><ProgressBar value={x.staging.progress} tone={x.staging.state === 'failed' ? 'red' : x.staging.state === 'ready' ? 'green' : 'blue'} label="staging {x.siteSlug}" /></div>
                  {:else}<span class="faint small">idle</span>{/if}
                </td>
                <td class="num">{x.keyVersion !== null ? `v${x.keyVersion}` : '—'}</td>
                <td class="small nowrap">{#if x.metrics}{pct(x.metrics.cpu)} · {pct(x.metrics.memPct)} · {pct(x.metrics.diskPct)}{:else}<span class="faint">—</span>{/if}</td>
                <td class="small">
                  {#if x.edge}
                    <div class="mono xs">{x.edge.boundPorts.join(', ') || 'no ports'}</div>
                    {#each x.edge.bindErrors as be, i (i)}<div class="xs text-red" data-tip={be.error}>:{be.port} {be.purpose} failed</div>{/each}
                    {#each x.edge.certs.filter((c) => c.lastError) as c (c.hostname)}<div class="xs text-red" data-tip={c.lastError}>cert {c.hostname}</div>{/each}
                    {#if x.edge.lastReloadError}<div class="xs text-red" data-tip={x.edge.lastReloadError}>reload failed</div>{/if}
                  {:else}<span class="faint">—</span>{/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}

    {#if s.uptime.length}
      <div class="section-title section"><h3>Uptime</h3><span class="muted small">green ≥ 99 % · amber &lt; 99 % · red &lt; 95 % · grey no data</span></div>
      <div class="ups">
        {#each s.uptime as u (u.siteSlug)}
          {@const d30 = days30(u, s.serverTime)}
          {@const h24 = hours24(u, s.serverTime)}
          <article class="card card-pad up" data-testid="uptime-{u.siteSlug}">
            <div class="row between"><span class="strong mono">{u.siteSlug}</span></div>
            <div class="hm">
              <div class="row between xs muted"><span>Last 24 hours (hourly)</span><span>{pct(mean(h24), 1)}</span></div>
              <Heatmap cells={h24} height={22} label="24 hour uptime of {u.siteSlug}" testid="heat24-{u.siteSlug}" />
            </div>
            <div class="hm">
              <div class="row between xs muted"><span>Last 30 days</span><span>{pct(mean(d30), 2)}</span></div>
              <Heatmap cells={d30} height={30} label="30 day uptime of {u.siteSlug}" testid="heat30-{u.siteSlug}" />
              <div class="row between xs faint"><span>{d30[0]?.label}</span><span>today</span></div>
            </div>
          </article>
        {/each}
      </div>
    {/if}
  {/if}
</div>

<style>
  .ag {
    display: inline-block;
    margin-top: 2px;
    text-decoration: none;
  }
  .ups {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(420px, 1fr));
    gap: var(--sp-4);
  }
  @media (max-width: 560px) {
    .ups {
      grid-template-columns: minmax(0, 1fr);
    }
  }
  .up {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .hm {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
</style>
