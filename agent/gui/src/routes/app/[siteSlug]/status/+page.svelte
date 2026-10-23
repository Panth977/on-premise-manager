<script lang="ts">
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import { api, apiPath, download, errCode, errMessage } from '$lib/api';
  import { Resource, poll } from '$lib/state/resource.svelte';
  import { toasts } from '$lib/state/toast.svelte';
  import { fmtDateTime, bytes } from '$lib/format';
  import { containerTone } from '$lib/ui/tones';
  import type { CertStatus, ContainerLogsResponse, ListResponse, SiteStatus } from '@opm/shared/types';
  import PageHead from '$lib/ui/PageHead.svelte';
  import Load from '$lib/ui/Load.svelte';
  import Empty from '$lib/ui/Empty.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Time from '$lib/ui/Time.svelte';
  import Stale from '$lib/ui/Stale.svelte';
  import Heatmap from '$lib/ui/Heatmap.svelte';
  import Confirm from '$lib/ui/Confirm.svelte';
  import EntityIcon from '$lib/ui/EntityIcon.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import { draft } from '$lib/state/draft.svelte';
  // round T: operator toolbox quick actions
  import type { ToolsContainerState } from '@opm/shared/agent-tools';
  import ContainerActions from '$lib/tools/ContainerActions.svelte';
  import MismatchHint from '$lib/volumes/MismatchHint.svelte';
  import Select from '$lib/ui/Select.svelte';
  import { agentOf } from '$lib/agent/state.svelte';
  import AnsiToggles from '$lib/ui/AnsiToggles.svelte';
  import { ansiPrefs, lineHtml, parseKeyed } from '$lib/ansi.svelte';
  // nav2 (§21 #97): host monitoring (moved from the old Tools › Monitoring page)
  import { tools } from '$lib/tools/api';
  import { appHref } from '$lib/nav';
  import type { MetricsSeriesResponse, MetricsSummary } from '@opm/shared/agent-tools';
  import SiteMonitor from '$lib/tools/SiteMonitor.svelte';
  import MetricsDrawer from '$lib/tools/MetricsDrawer.svelte';

  const slug = $derived(page.params.siteSlug ?? '*');
  // §21 #124 the page loads in parts, each its own small request and refresh: a slow part (containers) no longer holds
  // up the rest. An agent older than the split (no status/* routes → not-found) answers with the full status instead.
  const whole = (p: Promise<ListResponse<SiteStatus>>) => p.catch((e) => (errCode(e) === 'not-found' && slug !== '*' ? api.siteStatus({ siteSlug: slug }) : Promise.reject(e)));
  const overview = new Resource(() => whole(api.siteStatusOverview({ siteSlug: slug })));
  const ctrs = new Resource(() => whole(api.siteStatusContainers({ siteSlug: slug })));
  let range = $state<'24h' | '30d'>('24h');
  const uptime = new Resource(() => whole(api.siteStatusUptime({ siteSlug: slug, query: { range } })));
  const edgeR = new Resource(() => whole(api.siteStatusEdge({ siteSlug: slug })));
  $effect(() => {
    void slug;
    overview.reset();
    ctrs.reset();
    edgeR.reset();
    const stops = [poll(() => overview.load(), 10_000), poll(() => ctrs.load(), 10_000), poll(() => edgeR.load(), 30_000)];
    return () => stops.forEach((f) => f());
  });
  $effect(() => {
    void slug;
    void range;
    uptime.reset();
    return poll(() => uptime.load(), 60_000);
  });
  /** one SiteStatus per site, each part from its own request */
  const items = $derived.by((): SiteStatus[] => {
    const by = new Map<string, SiteStatus>();
    const pick = (r: Resource<ListResponse<SiteStatus>>, f: (to: SiteStatus, from: SiteStatus) => void) => {
      for (const x of r.data?.items ?? []) {
        let to = by.get(x.siteSlug);
        if (!to) by.set(x.siteSlug, (to = { siteSlug: x.siteSlug, reachable: x.reachable, lastSeenAt: x.lastSeenAt, agentVersion: null, host: null, containers: [], uptime24h: [], uptime30d: [], certs: [], edge: null }));
        to.reachable = to.reachable && x.reachable;
        f(to, x);
      }
    };
    pick(overview, (t, f) => ((t.host = f.host), (t.agentVersion = f.agentVersion), (t.agentWarnings = f.agentWarnings), (t.lastSeenAt = f.lastSeenAt)));
    pick(ctrs, (t, f) => (t.containers = f.containers));
    pick(uptime, (t, f) => ((t.uptime24h = f.uptime24h), (t.uptime30d = f.uptime30d)));
    pick(edgeR, (t, f) => ((t.edge = f.edge), (t.certs = f.certs)));
    return [...by.values()];
  });
  const reload = () => (overview.load(), ctrs.load(), edgeR.load());
  const mon = new Resource(async () => {
    const m = (await tools.metricsSummary({ siteSlug: slug })) as MetricsSummary;
    const host: MetricsSeriesResponse | null = await tools.metricsSeries({ siteSlug: slug, query: { subject: 'host', range: '1h' } }).catch(() => null);
    return { m, host };
  });
  $effect(() => {
    void slug;
    if (slug === '*') return;
    mon.reset();
    return poll(() => mon.load(), 15_000);
  });
  let hOpen = $state(false);
  const svcOf = (slug: string) => draft.base?.catalog?.services[slug];
  const all = $derived(slug === '*');

  // certificates
  let renewFor = $state<CertStatus | null>(null);
  let renewOpen = $state(false);
  async function renew() {
    if (!renewFor) return;
    try {
      const r = await api.renewCert({ hostname: renewFor.hostname, query: { site: renewFor.siteSlug } });
      if (r.status === 'renewed') toasts.ok(`Certificate renewed for ${r.cert.hostname}`, r.cert.notAfter ? `valid until ${fmtDateTime(r.cert.notAfter)}` : undefined);
      else toasts.push('bad', `Renewal failed for ${r.cert.hostname}`, r.cert.lastError ?? undefined);
      edgeR.load();
    } catch (e) {
      toasts.err(e, 'Renew failed');
      return false;
    }
  }
  async function localCa(site: string) {
    try {
      await download('localCa', apiPath('localCa', {}, { site }), `opm-${site}-local-ca.crt`);
    } catch (e) {
      toasts.err(e, 'Download failed');
    }
  }

  // container logs
  const containerOpts = $derived(items.filter((s) => s.reachable).flatMap((s) => s.containers.filter((c) => c.managed).map((c) => ({ value: `${s.siteSlug}|${c.containerSlug}`, label: `${all ? `${s.siteSlug} · ` : ''}${c.containerSlug} (${c.image})` }))));
  let logSel = $state('');
  let tail = $state(200);
  let q = $state('');
  let autoRefresh = $state(false);
  let logs = $state<ContainerLogsResponse | null>(null);
  let logErr = $state<string | null>(null);
  let logBusy = $state(false);
  $effect(() => {
    if (!logSel && containerOpts.length) logSel = containerOpts[0].value;
    else if (logSel && containerOpts.length && !containerOpts.some((o) => o.value === logSel)) logSel = containerOpts[0].value;
  });
  async function loadLogs() {
    if (!logSel) return;
    const [site, containerSlug] = logSel.split('|');
    logBusy = true;
    try {
      logs = await api.containerLogs({ siteSlug: site, containerSlug, query: { tail, q: q || undefined } });
      logErr = null;
    } catch (e) {
      logErr = errMessage(e);
    } finally {
      logBusy = false;
    }
  }
  $effect(() => {
    void logSel;
    void tail;
    if (logSel) untrack(loadLogs);
  });
  $effect(() => {
    if (!autoRefresh) return;
    return poll(() => loadLogs(), 3000);
  });
  // follow: keep the newest line in view while auto-refresh is on
  let logEl = $state<HTMLPreElement | null>(null);
  /** §21 #93: ANSI colors — SGR state + ESC-lost detection per stream (stdout / stderr) */
  const parsedLogs = $derived(logs ? parseKeyed(logs.lines.map((l) => ({ key: l.stream, raw: l.text }))) : []);
  $effect(() => {
    void logs;
    if (autoRefresh && logEl) logEl.scrollTop = logEl.scrollHeight;
  });
  function downloadLogs() {
    if (!logs || !logSel) return;
    const [site, slug] = logSel.split('|');
    const text = logs.lines.map((l) => `${l.ts} ${l.stream} ${l.text}`).join('\n') + '\n';
    const a = document.createElement('a');
    a.href = URL.createObjectURL(new Blob([text], { type: 'text/plain' }));
    a.download = `opm-${site}-${slug}-${new Date().toISOString().replace(/[:.]/g, '-')}.log`;
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 1000);
  }
  let qTimer: ReturnType<typeof setTimeout>;
  function onSearch(v: string) {
    q = v;
    clearTimeout(qTimer);
    qTimer = setTimeout(loadLogs, 350);
  }
  const fmtPorts = (ps: { portName: string; containerPort: number; hostPort: number | null; protocol: string }[]) => ps.map((p) => `${p.containerPort}${p.hostPort ? `→${p.hostPort}` : ''}/${p.protocol}`).join(', ') || '—';
</script>

<PageHead icon="gauge" eyebrow={all ? 'All sites' : `Site ${slug}`} title={all ? 'Status · all sites' : `Status · ${slug}`} sub="Containers, host, uptime and certificates. Refreshes every 10 s." />

<div class="stack">
    {#each items.filter((s) => !s.reachable) as s (s.siteSlug)}
      <div class="banner warn small"><Icon name="wifiOff" /><div class="grow">Site <strong>{s.siteSlug}</strong> does not answer — its data below is the last cached state.</div><Stale lastSeenAt={s.lastSeenAt} /></div>
    {/each}
    {#each items as s (s.siteSlug)}
      {#each (s.edge?.bindErrors ?? []).filter((b) => b.purpose === 'edge-http' || b.purpose === 'edge-https') as b (b.port)}
        <div class="banner bad small" data-testid="edge-port-alert"><Icon name="alert" /><div class="grow">Site <strong>{s.siteSlug}</strong>: port {b.port} is used by {b.owner ?? 'another program'} — stop it or change edge.httpPort. The GUI stays on the alias / agent port; the agent retries every 30 s.</div></div>
      {/each}
    {/each}
    {#each items as s (s.siteSlug)}
      {#each s.agentWarnings ?? [] as w (w)}
        <!-- §21 #112 the agent's own problems (disk, store, cloud key, dead letters, stuck loops) -->
        <div class="banner warn small" data-testid="agent-warning"><Icon name="alert" /><div class="grow">Site <strong>{s.siteSlug}</strong> agent: {w}</div><a class="small" href={appHref(s.siteSlug, 'agent')}>Agent process →</a></div>
      {/each}
    {/each}
    {#each items.filter((s) => s.edge?.defaultEdge) as s (s.siteSlug)}
      <div class="banner info small"><Icon name="info" /><div class="grow">Site <strong>{s.siteSlug}</strong> runs the default edge on port {s.edge?.boundPorts[0] ?? 80}: no configuration applied yet — approve the first update to serve your routes.</div></div>
    {/each}

    {#if !overview.loaded}<Load loading={overview.loading} loaded={overview.loaded} error={overview.error} retry={() => overview.load()} rows={3}>{''}</Load>{/if}
    {#if !all}
      {#each items as s (s.siteSlug)}
        {@const ag = agentOf(s.siteSlug)}
        <section class="hostmon" data-testid="host-monitoring">
          <div class="row between hm-head">
            <h2 class="hm-t">Host</h2>
            <span class="row">
              {#if s.edge}<span class="tiny muted">edge ports {s.edge.boundPorts.join(', ') || '—'}{#if s.edge.lastReloadError} · <span class="bad-text">{s.edge.lastReloadError}</span>{/if}</span>{/if}
              <a class="tiny aglink" class:up={ag?.release?.upgradeAvailable} href={appHref(s.siteSlug, 'agent')} title="Agent page of {s.siteSlug}" data-testid="agent-hint-{s.siteSlug}">agent {s.agentVersion ?? '—'}{#if ag?.release?.upgradeAvailable} · upgrade available{/if}</a>
            </span>
          </div>
          {#if mon.data}
            <SiteMonitor m={mon.data.m} host={mon.data.host} hostOnly onpick={() => (hOpen = true)} />
            <p class="faint xs hm-foot">Sampled every {mon.data.m.intervalSec} s · click a card for 1 h / 24 h / 7 d charts · containers: open one in <a href={appHref(slug, 'containers')}>Docker › Containers</a> → Monitoring</p>
          {:else if mon.error}
            <p class="muted small">Host metrics are not available: {errMessage(mon.error)}</p>
          {:else}
            <span class="skel" style="height:110px"></span>
          {/if}
        </section>
      {/each}
    {:else}
    <section class="grid cols-3">
      {#each items as s (s.siteSlug)}
        {@const ag = agentOf(s.siteSlug)}
        <div class="card card-pad host" class:stale={!s.reachable}>
          <div class="row between">
            <strong class="mono">{s.siteSlug}</strong>
            <a class="tiny aglink" class:up={ag?.release?.upgradeAvailable} href={appHref(s.siteSlug, 'agent')} title="Agent page of {s.siteSlug}" data-testid="agent-hint-{s.siteSlug}">agent {s.agentVersion ?? '—'}{#if ag?.release?.upgradeAvailable} · upgrade available{/if}</a>
          </div>
          {#if s.host}
            <div class="metrics">
              <div><span class="muted tiny">CPU</span><strong>{s.host.cpuPct.toFixed(1)} %</strong></div>
              <div><span class="muted tiny">Memory</span><strong>{bytes(s.host.memUsedMb * 1048576)}</strong><span class="tiny faint">/ {bytes(s.host.memTotalMb * 1048576)}</span></div>
              <div><span class="muted tiny">Disk</span><strong class:warn-text={s.host.diskUsedPct > 85}>{s.host.diskUsedPct.toFixed(0)} %</strong><span class="tiny faint">{bytes(s.host.diskFreeMb * 1048576)} free</span></div>
              <div><span class="muted tiny">Load</span><strong>{s.host.loadAvg1?.toFixed(2) ?? '—'}</strong></div>
            </div>
          {:else}<p class="muted small">No host metrics.</p>{/if}
          {#if s.edge}
            <div class="tiny muted">edge ports {s.edge.boundPorts.join(', ') || '—'}{#if s.edge.lastReloadError} · <span class="bad-text">{s.edge.lastReloadError}</span>{/if}{#each s.edge.bindErrors as b (b.port)} · <span class="bad-text">{b.purpose} :{b.port} {b.error}</span>{/each}</div>
          {/if}
        </div>
      {/each}
    </section>

    {/if}

    {#each items as s (s.siteSlug)}{#if s.reachable && s.containers.length}<MismatchHint siteSlug={s.siteSlug} containers={s.containers} onchanged={() => ctrs.load()} />{/if}{/each}
    <section class="card">
      <div class="card-head"><h2>Containers</h2><span class="row"><span class="muted small">"not managed" = containers without the OPM labels of this deployment</span><a class="btn sm" href={appHref(slug, 'containers')} data-testid="status-open-tools"><Icon name="container" size={14} />Open Containers</a></span></div>
      {#if !ctrs.loaded}
        <div class="card-body"><Load loading={ctrs.loading} loaded={ctrs.loaded} error={ctrs.error} retry={() => ctrs.load()} rows={6}>{''}</Load></div>
      {:else if items.every((s) => s.containers.length === 0)}
        <Empty icon="box" title="No containers" text="No container runs on {all ? 'any site' : slug} yet. Containers start when a cloud commit is merged on the Branch page." />
      {:else}
        <div class="table-wrap">
          <table class="table" data-testid="containers-status">
            <thead><tr>{#if all}<th>Site</th>{/if}<th>Name</th><th>Service</th><th>Image</th><th>Kind</th><th>State</th><th>Health</th><th>Ready</th><th class="num">Restarts</th><th class="num">CPU</th><th class="num">RAM</th><th>Ports</th><th>Started</th><th class="actions">Actions</th></tr></thead>
            <tbody>
              {#each items as s (s.siteSlug)}
                {#each s.containers as c (c.containerSlug)}
                  <tr class:dim={!c.managed || !s.reachable} title={!c.managed ? 'not managed by OPM' : undefined}>
                    {#if all}<td class="mono">{s.siteSlug}</td>{/if}
                    <td data-testid="status-name-{c.containerSlug}">{#if c.managed}<a class="mono strong" href={appHref(s.siteSlug, `containers/${c.containerSlug}`)}>{c.containerSlug}</a>{:else}<strong class="mono">{c.containerSlug}</strong>{/if}{#if !c.managed} <span class="badge">not managed</span>{/if}{#if !c.enabled} <span class="badge">disabled</span>{/if}</td>
                    <td>{#if c.serviceSlug}<span class="svc"><EntityIcon icon={svcOf(c.serviceSlug)?.icon} size={15} /><span class="svc-t"><span class="mono">{c.serviceSlug}</span>{#if svcOf(c.serviceSlug)?.label}<span class="tiny muted">{svcOf(c.serviceSlug)?.label}</span>{/if}</span></span>{:else}<span class="faint">—</span>{/if}</td>
                    <td class="mono small">{c.image}</td>
                    <td class="small">{c.kind}</td>
                    <td><span class="badge {containerTone(c.state, c.ready)}">{c.state}{c.exitCode !== null ? ` (${c.exitCode})` : ''}</span></td>
                    <td class="small">{c.health}</td>
                    <td>{#if c.ready}<span class="ok-text"><Icon name="check" size={14} /></span>{:else}<span class="bad-text"><Icon name="x" size={14} /></span>{/if}</td>
                    <td class="num" class:warn-text={c.restarts > 0}>{c.restarts}</td>
                    <td class="num small">{c.cpuPct === null ? '—' : `${c.cpuPct.toFixed(1)} %`}</td>
                    <td class="num small">{c.memMb === null ? '—' : `${c.memMb.toFixed(0)} MB`}</td>
                    <td class="small mono">{fmtPorts(c.ports)}</td>
                    <td class="small"><Time ts={c.startedAt} /></td>
                    <!-- quick actions: tabs of the container page + restart / stop / start -->
                    <td class="actions">{#if c.managed && s.reachable}<ContainerActions c={{ siteSlug: s.siteSlug, ref: c.containerSlug, state: c.state as ToolsContainerState, failedCopy: false, expectedRunning: c.enabled, manuallyStopped: null }} onchanged={() => ctrs.load()} />{/if}</td>
                  </tr>
                {/each}
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </section>

    <section class="card">
      <div class="card-head">
        <h2>Uptime</h2>
        <div class="tabs" role="tablist" style="border:0">
          <button role="tab" aria-selected={range === '24h'} onclick={() => (range = '24h')}>24 h · 10 min</button>
          <button role="tab" aria-selected={range === '30d'} onclick={() => (range = '30d')}>30 days · 6 h</button>
        </div>
      </div>
      <div class="card-body stack tight" data-testid="uptime">
        {#if !uptime.loaded}<Load loading={uptime.loading} loaded={uptime.loaded} error={uptime.error} retry={() => uptime.load()} rows={4}>{''}</Load>{/if}
        {#each items as s (s.siteSlug)}
          {#if all}<div class="section-title" style="margin-top:4px">{s.siteSlug}</div>{/if}
          {#each range === '24h' ? s.uptime24h : s.uptime30d as u (u.subject)}
            <Heatmap cells={u.cells} group={range === '24h' ? 1 : 6} stepMin={range === '24h' ? 10 : 60} label={u.subject} />
          {/each}
        {/each}
        <div class="legend tiny muted"><i class="ok"></i>≥ 99 % <i class="warn"></i>&lt; 99 % <i class="bad"></i>&lt; 95 % <i></i>no data · {range === '24h' ? '144 cells × 10 min' : '720 h shown as 120 groups of 6 h'}</div>
      </div>
    </section>

    <section class="card">
      <div class="card-head"><h2>Certificates</h2><span class="muted small">edge TLS per host</span></div>
      {#if !edgeR.loaded}
        <div class="card-body"><Load loading={edgeR.loading} loaded={edgeR.loaded} error={edgeR.error} retry={() => edgeR.load()} rows={2}>{''}</Load></div>
      {:else if items.every((s) => s.certs.length === 0)}
        <Empty icon="shield" title="No TLS hosts" text="No edge host uses TLS on {all ? 'any site' : slug}." />
      {:else}
        <div class="table-wrap">
          <table class="table" data-testid="certs">
            <thead><tr>{#if all}<th>Site</th>{/if}<th>Host</th><th>Mode</th><th>Issuer</th><th>Valid until</th><th>Last renewal</th><th>Last error</th><th class="right">Actions</th></tr></thead>
            <tbody>
              {#each items as s (s.siteSlug)}
                {#each s.certs as c (c.hostname)}
                  <tr class:dim={!s.reachable}>
                    {#if all}<td class="mono">{s.siteSlug}</td>{/if}
                    <td class="mono"><strong>{c.hostname}</strong></td>
                    <td><span class="badge {c.mode === 'none' ? '' : 'ok'}">{c.mode}{c.challenge ? ` · ${c.challenge}` : ''}</span></td>
                    <td class="small">{c.issuer ?? '—'}</td>
                    <td class="small">{c.notAfter ? fmtDateTime(c.notAfter) : '—'}{#if c.nextRenewalAt}<div class="tiny faint">renews <Time ts={c.nextRenewalAt} /></div>{/if}</td>
                    <td class="small"><Time ts={c.lastRenewalAt} /></td>
                    <td class="small bad-text">{c.lastError ?? ''}</td>
                    <td class="actions">
                      {#if c.mode !== 'none'}<Btn size="xs" icon="refresh" disabled={!s.reachable} onclick={() => ((renewFor = c), (renewOpen = true))} data-testid="renew-{c.hostname}">Renew</Btn>{/if}
                      {#if c.mode === 'internal'}<Btn size="xs" icon="download" variant="ghost" onclick={() => localCa(s.siteSlug)}>Download local CA</Btn>{/if}
                    </td>
                  </tr>
                {/each}
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </section>

    <section class="card">
      <div class="card-head">
        <h2>Container logs</h2>
        <div class="row">
          <div class="logsel"><Select size="sm" bind:value={logSel} options={containerOpts.map((o) => ({ value: o.value, label: o.label, icon: 'box' }))} ariaLabel="Container" testid="logs-container" /></div>
          <SegmentedControl size="sm" label="Tail" options={[{ value: 200, label: 'tail 200' }, { value: 1000, label: '1000' }, { value: 5000, label: '5000' }]} value={tail} onchange={(v) => (tail = v)} />
          <input class="input sm" style="width:180px" placeholder="Search" value={q} oninput={(e) => onSearch(e.currentTarget.value)} aria-label="Search the log" />
          <label class="switch"><input type="checkbox" bind:checked={autoRefresh} data-testid="logs-follow" />follow</label>
          <AnsiToggles small testid="logs-ansi" />
          <Btn size="sm" icon="refresh" busy={logBusy} onclick={loadLogs} aria-label="Refresh logs" />
          <Btn size="sm" icon="download" variant="ghost" disabled={!logs} onclick={downloadLogs} data-testid="logs-download">Download</Btn>
        </div>
      </div>
      <div class="card-body">
        {#if !containerOpts.length}
          <p class="muted small">No reachable container.</p>
        {:else if logErr}
          <div class="banner bad small"><Icon name="alert" /><div class="grow">{logErr}</div></div>
        {:else if logs}
          <pre class="logbox" data-testid="logs" bind:this={logEl}>{#each logs.lines as l, i (i)}<span class="ts">{l.ts.slice(11, 19)}</span> <span class="ansi-line" class:err={l.stream === 'stderr'}>{#if parsedLogs[i]}{@html lineHtml(parsedLogs[i], ansiPrefs.mode, q)}{:else}{l.text}{/if}</span>{'\n'}{:else}<span class="ts">no lines{q ? ` matching "${q}"` : ''}</span>{/each}</pre>
        {:else}<span class="skel" style="height:120px"></span>{/if}
      </div>
    </section>
</div>

<Confirm bind:open={renewOpen} title="Renew certificate?" confirmLabel="Renew now" message="{renewFor?.hostname} on {renewFor?.siteSlug}: {renewFor?.mode === 'acme' ? 'a new ACME order' : renewFor?.mode === 'internal' ? 'a new certificate from the site local CA' : 're-read the certificate files'}." onconfirm={renew} />

<MetricsDrawer bind:open={hOpen} site={slug} subject="host" title="{slug} · host" />

<style>
  .hostmon {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .hm-head {
    flex-wrap: wrap;
    gap: 6px 12px;
  }
  .hm-t {
    font-size: var(--fs-lg);
    margin: 0;
  }
  .hm-foot {
    margin: 0;
  }
  .aglink {
    color: var(--text-3);
  }
  .aglink.up {
    color: var(--accent-text);
    font-weight: var(--fw-medium);
  }
  .logsel {
    width: 240px;
    max-width: 100%;
  }
  .svc {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    white-space: nowrap;
  }
  .svc-t {
    display: flex;
    flex-direction: column;
    line-height: 1.25;
  }
  .host.stale {
    opacity: 0.6;
  }
  .host {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .metrics {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 8px;
  }
  .metrics div {
    display: flex;
    flex-direction: column;
    gap: 1px;
    min-width: 0;
  }
  .metrics strong {
    font-size: 16px;
    font-variant-numeric: tabular-nums;
  }
  .legend {
    display: flex;
    gap: 6px;
    align-items: center;
    flex-wrap: wrap;
    margin-top: 6px;
  }
  .legend i {
    width: 10px;
    height: 10px;
    border-radius: 2px;
    background: var(--heat-none);
    display: inline-block;
    margin-left: 6px;
  }
  .legend i.ok {
    background: var(--green);
  }
  .legend i.warn {
    background: var(--amber);
  }
  .legend i.bad {
    background: var(--red);
  }
  .logbox .ts {
    color: #73737e;
  }
  .logbox .err {
    color: #f87171;
  }
</style>
