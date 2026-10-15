<script lang="ts">
  /** DOCKER › Containers (nav2): live list of the selected site ("*" = every site); a row opens the container page. */
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import type { MetricsSummary, ToolsContainer } from '@opm/shared/agent-tools';
  import { Resource, poll } from '$lib/state/resource.svelte';
  import { tools } from '$lib/tools/api';
  import { stateTone, uptime, sinceSec, bytes, pctNum } from '$lib/tools/fmt';
  import PageHead from '$lib/ui/PageHead.svelte';
  import Load from '$lib/ui/Load.svelte';
  import Empty from '$lib/ui/Empty.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Time from '$lib/ui/Time.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import ContainerActions from '$lib/tools/ContainerActions.svelte';
  import Sparkline from '$lib/tools/charts/Sparkline.svelte';
  import { appHref } from '$lib/nav';

  const slug = $derived(page.params.siteSlug ?? '*');
  const all = $derived(slug === '*');
  const list = new Resource(() => tools.toolsContainers({ siteSlug: slug }));
  $effect(() => {
    void slug;
    list.reset();
    return poll(() => list.load(), 10_000);
  });
  let q = $state('');
  let showFailed = $state(true);
  const items = $derived((list.data?.items ?? []).filter((c) => (showFailed || !c.failedCopy) && (!q || `${c.ref} ${c.image} ${c.serviceSlug}`.toLowerCase().includes(q.toLowerCase()))));
  const counts = $derived.by(() => {
    const xs = list.data?.items ?? [];
    return { running: xs.filter((c) => c.state === 'running').length, stopped: xs.filter((c) => c.manuallyStopped).length, problem: xs.filter((c) => !c.failedCopy && !c.manuallyStopped && (c.state !== 'running' || c.health === 'unhealthy')).length, failed: xs.filter((c) => c.failedCopy).length, total: xs.length };
  });
  // 60-min CPU / memory sparklines (metrics summary, 15 s)
  const met = new Resource(async () => {
    const r = (await tools.metricsSummary({ siteSlug: slug })) as MetricsSummary | { items: MetricsSummary[] };
    return 'items' in r ? r.items : [r];
  });
  $effect(() => {
    void slug;
    met.reset();
    return poll(() => met.load(), 15_000);
  });
  const spark = (c: ToolsContainer) => met.data?.find((m) => m.siteSlug === c.siteSlug)?.containers.find((x) => x.ref === c.ref)?.spark;
  function changed(c: ToolsContainer) {
    if (list.data) list.data = { items: list.data.items.map((x) => (x.siteSlug === c.siteSlug && x.ref === c.ref ? c : x)) };
    list.load();
  }
  const href = (c: ToolsContainer) => appHref(c.siteSlug, `containers/${encodeURIComponent(c.ref)}`);
  const portsText = (c: ToolsContainer) => c.ports.filter((p) => p.hostPort !== null).map((p) => `${p.hostPort}→${p.containerPort}`).join(', ');
  const stateLabel = (c: ToolsContainer) => (c.manuallyStopped && c.state !== 'running' ? 'stopped manually' : c.state === 'exited' && c.exitCode !== null ? `exited (${c.exitCode})` : c.state);
</script>

<PageHead icon="container" eyebrow={all ? 'Docker · all sites' : `Docker · site ${slug}`} title="Containers" sub="Live state, health and resources. Open a container for its logs, terminal, monitoring, inspect and config." />

<Load loading={list.loading} loaded={list.loaded} error={list.error} retry={() => list.load()} rows={6}>
  <div class="stack">
    <div class="sum">
      <div class="tile"><span class="muted tiny">Running</span><strong>{counts.running}<span class="faint">{" / "}{counts.total - counts.failed}</span></strong></div>
      <div class="tile"><span class="muted tiny">Needs attention</span><strong class:bad-text={counts.problem > 0}>{counts.problem}</strong></div>
      <div class="tile"><span class="muted tiny">Stopped manually</span><strong>{counts.stopped}</strong></div>
      <div class="tile"><span class="muted tiny">Failed copies kept</span><strong>{counts.failed}</strong></div>
    </div>
    <section class="card">
      <div class="card-head">
        <div class="row grow">
          <input class="input sm" style="max-width:240px" placeholder="Filter containers" bind:value={q} aria-label="Filter containers" />
          <label class="switch"><input type="checkbox" bind:checked={showFailed} />failed copies</label>
        </div>
        <Btn size="sm" icon="refresh" busy={list.loading} onclick={() => list.load()} aria-label="Refresh" />
      </div>
      {#if items.length === 0}
        <Empty icon="container" title="No containers" text={q ? 'Nothing matches the filter.' : `No OPM container on ${all ? 'any site' : slug} yet. Containers start when a cloud commit is merged on the Branch page.`} compact />
      {:else}
        <div class="table-wrap">
          <table class="table" data-testid="tools-containers">
            <thead><tr>{#if all}<th>Site</th>{/if}<th>Container</th><th>State</th><th>Ports</th><th>Uptime</th><th class="num">Restarts</th><th>CPU</th><th>Memory</th><th class="actions">Actions</th></tr></thead>
            <tbody>
              {#each items as c (c.siteSlug + '/' + c.ref)}
                {@const sp = spark(c)}
                <tr class="clickable" class:dim={c.failedCopy} data-testid="row-{c.ref}" onclick={(e) => !(e.target as HTMLElement).closest('a,button') && goto(href(c))}>
                  {#if all}<td class="mono small">{c.siteSlug}</td>{/if}
                  <td>
                    <a class="linkish" href={href(c)} data-testid="open-{c.ref}"><strong class="mono">{c.ref}</strong></a>
                    <div class="tiny faint mono ellipsis">{c.image}</div>
                  </td>
                  <td>
                    <div class="row tight">
                      <span class="badge {stateTone(c)}" data-testid="state-{c.ref}">{stateLabel(c)}</span>
                      {#if c.health !== 'none' && c.state === 'running'}<span class="badge {c.health === 'healthy' ? 'ok' : c.health === 'starting' ? 'warn' : 'bad'}" title="health check">{c.health}</span>{/if}
                      {#if c.oomKilled}<span class="badge bad" title="the kernel killed it for memory">OOM</span>{/if}
                      {#if c.failedCopy}<span class="badge" title="kept after a failed update for reading">failed copy</span>{/if}
                    </div>
                    {#if c.manuallyStopped && c.state !== 'running'}<div class="tiny muted" data-testid="manual-{c.ref}">by {c.manuallyStopped.actor ?? 'unknown'} · <Time ts={c.manuallyStopped.at} /></div>{/if}
                  </td>
                  <td class="mono small nowrap">{portsText(c) || '—'}</td>
                  <td class="small nowrap">{c.state === 'running' ? uptime(sinceSec(c.startedAt)) : '—'}</td>
                  <td class="num" class:warn-text={c.restartCount > 0}>{c.restartCount}</td>
                  <td class="small nowrap">
                    <div class="cell"><span class="v" class:warn-text={(c.cpuPct ?? 0) > 85}>{pctNum(c.cpuPct, 1)}</span>{#if sp}<Sparkline values={sp.cpu} max={100} width={70} height={20} tone={(c.cpuPct ?? 0) > 85 ? 'amber' : 'accent'} />{/if}</div>
                  </td>
                  <td class="small nowrap">
                    {#if c.memBytes !== null}
                      {@const f = c.memLimitBytes ? c.memBytes / c.memLimitBytes : null}
                      <div class="mem"><span>{bytes(c.memBytes)}</span>{#if c.memLimitBytes}<span class="faint">{" / "}{bytes(c.memLimitBytes)}</span>{/if}</div>
                      <div class="cell">
                        {#if f !== null}<div class="bar"><i style="width:{Math.min(100, f * 100)}%" class:warn={f > 0.9}></i></div>{/if}
                        {#if sp}<Sparkline values={sp.mem} max={c.memLimitBytes} width={70} height={20} tone={f !== null && f > 0.9 ? 'amber' : 'violet'} />{/if}
                      </div>
                    {:else}<span class="faint">—</span>{/if}
                  </td>
                  <td class="actions"><ContainerActions {c} onchanged={changed} /></td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </section>
    <p class="faint xs foot"><Icon name="info" size={12} /> A container you stop here is marked "stopped manually": the agent does not report it as failed and does not restart it until you start it or the next update.</p>
  </div>
</Load>


<style>
  .sum {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 10px;
  }
  .tile {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--r-lg);
    padding: 10px 14px;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .tile strong {
    font-size: 20px;
    font-variant-numeric: tabular-nums;
  }
  @media (max-width: 700px) {
    .sum {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
  }
  .row.tight {
    gap: 4px;
    flex-wrap: wrap;
  }
  .linkish {
    color: var(--text);
  }
  .cell {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .v {
    min-width: 44px;
    font-variant-numeric: tabular-nums;
  }
  .linkish:hover strong {
    color: var(--accent-text);
    text-decoration: underline;
  }
  .linkish:focus-visible {
    outline: 2px solid var(--accent);
    border-radius: 3px;
  }
  .mem {
    font-variant-numeric: tabular-nums;
  }
  .bar {
    height: 4px;
    border-radius: 2px;
    background: var(--bg-sunken);
    margin-top: 3px;
    width: 70px;
    overflow: hidden;
  }
  .bar i {
    display: block;
    height: 100%;
    background: var(--accent);
  }
  .bar i.warn {
    background: var(--amber);
  }
  .foot {
    display: flex;
    gap: 6px;
    align-items: center;
    margin: 0;
  }
  td :global(.ellipsis) {
    max-width: 260px;
  }
</style>
