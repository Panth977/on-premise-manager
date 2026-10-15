<script lang="ts">
  /**
   * One container, full page (nav2, SPEC §18 / §21 #97 — replaces the inspect drawer and the global Logs / Terminal /
   * Monitoring pages): Overview · Logs · Terminal · Monitoring · Inspect · Processes & events · Config (?tab=).
   */
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import type { ContainerInspectResponse, ContainerTopResponse, DockerEvent, MetricsSummary, ToolsContainer } from '@opm/shared/agent-tools';
  import { Resource, poll } from '$lib/state/resource.svelte';
  import { draft } from '$lib/state/draft.svelte';
  import { toasts } from '$lib/state/toast.svelte';
  import { errMessage } from '$lib/api';
  import { tools } from '$lib/tools/api';
  import { bytes, pctNum, stateTone, uptime, sinceSec } from '$lib/tools/fmt';
  import { fmtDateTime } from '$lib/format';
  import { appHref } from '$lib/nav';
  import { siteDepends } from '$lib/depends';
  import PageHead from '$lib/ui/PageHead.svelte';
  import Load from '$lib/ui/Load.svelte';
  import Empty from '$lib/ui/Empty.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Time from '$lib/ui/Time.svelte';
  import ChooseSite from '$lib/ui/ChooseSite.svelte';
  import ContainerActions from '$lib/tools/ContainerActions.svelte';
  import JsonTree from '$lib/tools/JsonTree.svelte';
  import Sparkline from '$lib/tools/charts/Sparkline.svelte';
  import LogViewer from '$lib/tools/LogViewer.svelte';
  import TerminalTool from '$lib/tools/TerminalTool.svelte';
  import MetricsPanel from '$lib/tools/MetricsPanel.svelte';
  import ContainerEditor from '$lib/editors/ContainerEditor.svelte';

  const site = $derived(page.params.siteSlug ?? '*');
  const ref = $derived(decodeURIComponent(page.params.ref ?? ''));
  type Tab = 'overview' | 'logs' | 'terminal' | 'monitoring' | 'inspect' | 'processes' | 'config';
  const TABS: { k: Tab; label: string; icon: string }[] = [
    { k: 'overview', label: 'Overview', icon: 'gauge' },
    { k: 'logs', label: 'Logs', icon: 'logs' },
    { k: 'terminal', label: 'Terminal', icon: 'terminal' },
    { k: 'monitoring', label: 'Monitoring', icon: 'chart' },
    { k: 'inspect', label: 'Inspect', icon: 'search' },
    { k: 'processes', label: 'Processes & events', icon: 'activity' },
    { k: 'config', label: 'Config', icon: 'cog' },
  ];
  const tab = $derived((TABS.find((t) => t.k === page.url.searchParams.get('tab'))?.k ?? 'overview') as Tab);
  function setTab(k: Tab) {
    const u = new URL(page.url);
    if (k === 'overview') u.searchParams.delete('tab');
    else u.searchParams.set('tab', k);
    goto(u.pathname + u.search, { replaceState: true, noScroll: true, keepFocus: true });
  }
  function onTabKey(e: KeyboardEvent, i: number) {
    const d = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0;
    if (!d) return;
    e.preventDefault();
    const n = TABS[(i + d + TABS.length) % TABS.length];
    setTab(n.k);
    (document.querySelector(`[data-testid="ctab-${n.k}"]`) as HTMLElement | null)?.focus();
  }

  // ── the container (live list of the site, 10 s)
  const list = new Resource(() => tools.toolsContainers({ siteSlug: site }));
  $effect(() => {
    void site;
    if (site === '*') return;
    list.reset();
    return poll(() => list.load(), 10_000);
  });
  const c = $derived<ToolsContainer | undefined>((list.data?.items ?? []).find((x) => x.siteSlug === site && x.ref === ref));
  function changed(n: ToolsContainer) {
    if (list.data) list.data = { items: list.data.items.map((x) => (x.siteSlug === n.siteSlug && x.ref === n.ref ? n : x)) };
    list.load();
    loadInspect();
  }

  // ── sparklines (metrics summary, 15 s)
  const met = new Resource(async () => (await tools.metricsSummary({ siteSlug: site })) as MetricsSummary);
  $effect(() => {
    void site;
    if (site === '*' || (tab !== 'overview' && tab !== 'monitoring')) return;
    return poll(() => met.load(), 15_000);
  });
  const sample = $derived(met.data?.containers.find((x) => x.ref === ref));

  // ── docker inspect (overview + inspect)
  let ins = $state<ContainerInspectResponse | null>(null);
  let insErr = $state<string | null>(null);
  let insGen = 0;
  async function loadInspect() {
    if (site === '*' || !ref) return;
    const my = ++insGen;
    try {
      const r = await tools.inspectContainer({ siteSlug: site, ref });
      if (my === insGen) ((ins = r), (insErr = null));
    } catch (e) {
      if (my === insGen) insErr = errMessage(e);
    }
  }
  $effect(() => {
    void site;
    void ref;
    ins = null;
    insErr = null;
    if (tab !== 'overview' && tab !== 'inspect') return;
    loadInspect();
    return poll(loadInspect, 15_000);
  });
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const raw = $derived((ins?.inspect ?? {}) as Record<string, any>);
  const env = $derived(
    ((raw.Config?.Env ?? []) as string[]).map((e) => {
      const i = e.indexOf('=');
      const k = i < 0 ? e : e.slice(0, i);
      return { k, v: i < 0 ? '' : e.slice(i + 1), masked: ins?.maskedEnv.includes(k) ?? false };
    }),
  );
  const mounts = $derived((raw.Mounts ?? []) as { Type?: string; Source?: string; Destination?: string; RW?: boolean }[]);
  const portMap = $derived(Object.entries((raw.NetworkSettings?.Ports ?? {}) as Record<string, { HostIp: string; HostPort: string }[] | null>));
  const labels = $derived(Object.entries((raw.Config?.Labels ?? {}) as Record<string, string>).sort(([a], [b]) => a.localeCompare(b)));
  async function copyRaw() {
    try {
      await navigator.clipboard.writeText(JSON.stringify(ins?.inspect ?? {}, null, 2));
      toasts.ok('Inspect JSON copied');
    } catch {
      toasts.push('bad', 'Copy failed', 'The browser refused clipboard access.');
    }
  }

  // ── processes + events
  let top = $state<ContainerTopResponse | null>(null);
  let topErr = $state<string | null>(null);
  let evs = $state<DockerEvent[] | null>(null);
  async function loadTop() {
    topErr = null;
    try {
      top = await tools.containerTop({ siteSlug: site, ref });
    } catch (e) {
      top = null;
      topErr = errMessage(e);
    }
  }
  async function loadEvents() {
    try {
      evs = (await tools.containerEvents({ siteSlug: site, ref })).items;
    } catch (e) {
      evs = [];
      toasts.err(e, 'Events failed');
    }
  }
  $effect(() => {
    if (tab !== 'processes' || site === '*') return;
    void ref;
    loadTop();
    loadEvents();
  });

  // ── config: the head-config container (working copy) + depends / start order
  const cfgC = $derived(draft.work?.sites.find((s) => s.siteSlug === site)?.config.containers.find((x) => x.containerSlug === (c?.containerSlug ?? ref)));
  const dep = $derived(site !== '*' && cfgC ? siteDepends(site) : null);
  const myDeps = $derived(dep?.deps.find((d) => d.containerSlug === cfgC?.containerSlug));
  const orderPos = $derived(dep && cfgC ? dep.order.order.indexOf(cfgC.containerSlug) : -1);
  const stateLabel = (x: ToolsContainer) => (x.manuallyStopped && x.state !== 'running' ? 'stopped manually' : x.state === 'exited' && x.exitCode !== null ? `exited (${x.exitCode})` : x.state);
  const running = $derived(c?.state === 'running');
</script>

{#if site === '*'}
  <PageHead icon="container" eyebrow="Docker · container" title={ref} />
  <ChooseSite what="container {ref}" text={'"All sites" is selected. A container lives on one site — pick it to open this page.'} />
{:else}
  <PageHead icon="container" eyebrow="Site {site} · container" title={ref} sub={c ? `${c.image} · ${c.name}` : undefined}>
    {#snippet meta()}
      {#if c}
        <span class="badge {stateTone(c)}" data-testid="c-state">{stateLabel(c)}</span>
        {#if c.health !== 'none' && c.state === 'running'}<span class="badge {c.health === 'healthy' ? 'ok' : c.health === 'starting' ? 'warn' : 'bad'}" data-testid="c-health">{c.health}</span>{/if}
        {#if c.oomKilled}<span class="badge bad">OOM killed</span>{/if}
        {#if c.failedCopy}<span class="badge">failed copy · read-only</span>{/if}
      {/if}
    {/snippet}
    {#snippet actions()}
      <a class="btn" href={appHref(site, 'containers')}><Icon name="chevronLeft" size={14} />Containers</a>
      {#if c}<ContainerActions {c} links={false} size="sm" onchanged={changed} />{/if}
    {/snippet}
  </PageHead>

  <Load loading={list.loading} loaded={list.loaded} error={list.error} retry={() => list.load()} rows={6}>
    {#if !c}
      <section class="card"><Empty icon="container" title="No container {ref} on {site}" text="It may have been removed by an update, or the name is wrong."><a class="btn" href={appHref(site, 'containers')}>All containers of {site}</a></Empty></section>
    {:else}
      <div class="tabs ctabs" role="tablist" aria-label="Container sections" data-testid="container-tabs">
        {#each TABS as t, i (t.k)}
          <button role="tab" id="ctab-{t.k}" aria-selected={tab === t.k} aria-controls="cpanel" tabindex={tab === t.k ? 0 : -1} onclick={() => setTab(t.k)} onkeydown={(e) => onTabKey(e, i)} data-testid="ctab-{t.k}"><Icon name={t.icon} size={14} />{t.label}</button>
        {/each}
      </div>

      <div id="cpanel" role="tabpanel" aria-labelledby="ctab-{tab}" data-testid="cpanel-{tab}">
        {#if tab === 'overview'}
          {#if c.manuallyStopped && c.state !== 'running'}
            <div class="banner warn small" style="margin-bottom:14px" data-testid="manual-{c.ref}"><Icon name="pause" /><div class="grow">Stopped manually by {c.manuallyStopped.actor ?? 'unknown'} · <Time ts={c.manuallyStopped.at} />. The agent does not restart it and does not report it as failed until you start it or the next update.</div></div>
          {/if}
          <div class="ov">
            <section class="card card-pad">
              <h2 class="ch">State</h2>
              <dl class="kv">
                <dt>Status</dt><dd>{c.status}</dd>
                <dt>Started</dt><dd>{c.startedAt ? fmtDateTime(c.startedAt) : '—'}{#if running} <span class="faint">· up {uptime(sinceSec(c.startedAt))}</span>{/if}</dd>
                {#if !running}<dt>Finished</dt><dd>{c.finishedAt ? fmtDateTime(c.finishedAt) : '—'} · exit {c.exitCode ?? '—'}</dd>{/if}
                <dt>Restarts</dt><dd class:warn-text={c.restartCount > 0} data-testid="c-restarts">{c.restartCount}</dd>
                <dt>Restart policy</dt><dd class="mono small">{raw.HostConfig?.RestartPolicy?.Name ?? '—'}</dd>
                <dt>Expected</dt><dd>{c.expectedRunning ? 'running (enabled in the head config)' : 'not expected to run'}</dd>
              </dl>
              <div class="meters">
                <div class="m">
                  <span class="muted tiny">CPU</span><strong>{pctNum(c.cpuPct, 1)}</strong>
                  <Sparkline values={sample?.spark.cpu ?? []} max={100} width={150} height={28} tone="accent" />
                </div>
                <div class="m">
                  <span class="muted tiny">Memory</span><strong>{bytes(c.memBytes)}{#if c.memLimitBytes}<span class="faint small">{' / '}{bytes(c.memLimitBytes)}</span>{/if}</strong>
                  <Sparkline values={sample?.spark.mem ?? []} max={c.memLimitBytes} width={150} height={28} tone="violet" />
                </div>
              </div>
              <button class="linkish small" onclick={() => setTab('monitoring')}>Charts for 1 h / 24 h / 7 d →</button>
            </section>

            <section class="card card-pad">
              <h2 class="ch">Image &amp; start</h2>
              <dl class="kv">
                <dt>Image</dt><dd class="mono small">{c.image}</dd>
                <dt>Service</dt><dd class="mono small">{c.serviceSlug || '—'}</dd>
                <dt>Docker name</dt><dd class="mono small">{c.name} <span class="faint">({c.id ?? '—'})</span></dd>
                <dt>Command</dt><dd class="mono small brk">{[raw.Path, ...((raw.Args as string[]) ?? [])].filter(Boolean).join(' ') || '—'}</dd>
                {#if cfgC}
                  <dt>Depends</dt>
                  <dd data-testid="c-depends">
                    {#if myDeps && (myDeps.explicit.length || myDeps.implicit.length)}
                      {#each myDeps.explicit as d (d)}<a class="chip mono" href={appHref(site, `containers/${d}`)}>{d}</a>{/each}
                      {#each myDeps.implicit as d (d.containerSlug)}<a class="chip mono" href={appHref(site, `containers/${d.containerSlug}`)} title="implicit: {d.from.join(', ')}">{d.containerSlug} <span class="faint">implicit</span></a>{/each}
                    {:else}<span class="muted">none</span>{/if}
                  </dd>
                  <dt>Start order</dt><dd data-testid="c-start-order">{orderPos >= 0 ? `${orderPos + 1} of ${dep?.order.order.length}` : 'not started (disabled)'}{#if dep?.order.cycle.includes(cfgC.containerSlug)} <span class="badge warn">cycle</span>{/if}</dd>
                {:else}
                  <dt>Config</dt><dd class="muted small">{c.failedCopy ? 'a copy kept after a failed update — not in the config' : 'not in the head config'}</dd>
                {/if}
              </dl>
            </section>

            <section class="card">
              <div class="card-head"><h2>Ports</h2></div>
              <div class="table-wrap">
                <table class="table compact" data-testid="c-ports">
                  <thead><tr><th>Container port</th><th>Published on</th></tr></thead>
                  <tbody>
                    {#if portMap.length}
                      {#each portMap as [p, binds] (p)}<tr><td class="mono small">{p}</td><td class="mono small">{binds?.length ? binds.map((b) => `${b.HostIp || '0.0.0.0'}:${b.HostPort}`).join(', ') : 'not published'}</td></tr>{/each}
                    {:else}
                      {#each c.ports as p (p.containerPort + p.protocol)}<tr><td class="mono small">{p.containerPort}/{p.protocol}</td><td class="mono small">{p.hostPort ?? 'not published'}</td></tr>{:else}<tr><td colspan="2" class="muted small">No ports.</td></tr>{/each}
                    {/if}
                  </tbody>
                </table>
              </div>
            </section>

            <section class="card">
              <div class="card-head"><h2>Health history</h2><span class="muted small">OPM checks, newest first</span></div>
              <div class="card-body">
                {#if insErr}
                  <p class="bad-text small">{insErr}</p>
                {:else if !ins}
                  <span class="skel" style="height:80px"></span>
                {:else if ins.checks.length === 0}
                  <p class="muted small" style="margin:0">No health check runs for this container (no check in the image, or not running).</p>
                {:else}
                  <ol class="checks" data-testid="health-history">
                    {#each [...ins.checks].reverse().slice(0, 30) as h, i (i)}
                      <li><span class="dot {h.ok ? 'ok' : 'bad'}"></span><span class="mono xs faint">{h.at.slice(11, 19)}</span><span class="small grow">{h.message}</span></li>
                    {/each}
                  </ol>
                {/if}
                {#if ins?.healthLog.length}
                  <h3 class="sub">Docker HEALTHCHECK log</h3>
                  {#each ins.healthLog as h, i (i)}<div class="small"><span class="mono xs faint">{h.start.slice(11, 19)}</span> exit {h.exitCode} · <span class="mono">{h.output}</span></div>{/each}
                {/if}
              </div>
            </section>

            <section class="card wide">
              <div class="card-head"><h2>Mounts</h2><span class="muted small">{mounts.length}</span></div>
              <div class="table-wrap">
                <table class="table compact" data-testid="c-mounts">
                  <thead><tr><th>Container path</th><th>Host source</th><th>Type</th><th>Mode</th></tr></thead>
                  <tbody>
                    {#each mounts as m, i (i)}
                      <tr><td class="mono small">{m.Destination}</td><td class="mono small brk">{m.Source}</td><td class="small">{m.Type ?? 'bind'}</td><td><span class="badge {m.RW ? '' : 'info'}">{m.RW ? 'rw' : 'ro'}</span></td></tr>
                    {:else}<tr><td colspan="4" class="muted small">{ins ? 'No mounts.' : 'Loading…'}</td></tr>{/each}
                  </tbody>
                </table>
              </div>
            </section>

            <section class="card wide">
              <div class="card-head"><h2>Environment</h2><span class="muted small">secret values are masked by the agent (••••••)</span></div>
              <div class="table-wrap envbox">
                <table class="table compact" data-testid="env-table">
                  <thead><tr><th>Name</th><th>Value</th></tr></thead>
                  <tbody>
                    {#each env as e (e.k)}
                      <tr><td class="mono small nowrap">{e.k}{#if e.masked} <span class="badge warn" title="secret value masked">secret</span>{/if}</td><td class="mono small brk">{e.v}</td></tr>
                    {:else}<tr><td colspan="2" class="muted small">{ins ? 'No variables.' : 'Loading…'}</td></tr>{/each}
                  </tbody>
                </table>
              </div>
            </section>
          </div>
        {:else if tab === 'logs'}
          <LogViewer {site} src="containers" container={c.ref} />
        {:else if tab === 'terminal'}
          {#if c.failedCopy}
            <section class="card"><Empty icon="terminal" title="No terminal in a failed copy" text="This container was kept after a failed update for reading only. Use Logs and Inspect." compact /></section>
          {:else}
            <TerminalTool {site} mode="container" container={c.ref} />
          {/if}
        {:else if tab === 'monitoring'}
          <section class="card card-pad"><MetricsPanel {site} subject="container:{c.ref}" /></section>
        {:else if tab === 'inspect'}
          <div class="stack">
            <section class="card">
              <div class="card-head"><h2>Labels</h2><span class="muted small">{labels.length}</span></div>
              <div class="table-wrap">
                <table class="table compact">
                  <thead><tr><th>Label</th><th>Value</th></tr></thead>
                  <tbody>{#each labels as [k, v] (k)}<tr><td class="mono small">{k}</td><td class="mono small brk">{v}</td></tr>{:else}<tr><td colspan="2" class="muted small">{ins ? 'No labels.' : 'Loading…'}</td></tr>{/each}</tbody>
                </table>
              </div>
            </section>
            <section class="card">
              <div class="card-head"><h2>docker inspect</h2><Btn size="sm" icon="copy" onclick={copyRaw} disabled={!ins}>Copy JSON</Btn></div>
              <div class="card-body">
                {#if insErr}<div class="banner bad small"><Icon name="alert" /><div class="grow">{insErr}</div><Btn size="sm" onclick={loadInspect}>Retry</Btn></div>
                {:else if !ins}<span class="skel" style="height:200px"></span>
                {:else}<div class="raw" data-testid="inspect-json"><JsonTree value={ins.inspect} open={1} /></div>{/if}
                <p class="faint xs" style="margin:8px 0 0">Secret env values are masked by the agent.</p>
              </div>
            </section>
          </div>
        {:else if tab === 'processes'}
          <div class="stack">
            <section class="card">
              <div class="card-head"><h2>Processes</h2><span class="row"><span class="muted small">docker top</span><Btn size="sm" icon="refresh" variant="ghost" onclick={loadTop} aria-label="Refresh processes" /></span></div>
              {#if topErr}
                <div class="card-body"><div class="banner info small"><Icon name="info" /><div class="grow">{topErr}</div></div></div>
              {:else if !top}
                <div class="card-body"><span class="skel" style="height:120px"></span></div>
              {:else}
                <div class="table-wrap">
                  <table class="table compact" data-testid="top-table">
                    <thead><tr>{#each top.titles as t (t)}<th>{t}</th>{/each}</tr></thead>
                    <tbody>{#each top.processes as p, i (i)}<tr>{#each p as v, j (j)}<td class="mono small" class:brk={j === p.length - 1}>{v}</td>{/each}</tr>{/each}</tbody>
                  </table>
                </div>
              {/if}
            </section>
            <section class="card">
              <div class="card-head"><h2>Docker events</h2><span class="row"><span class="muted small">last 24 h</span><Btn size="sm" icon="refresh" variant="ghost" onclick={loadEvents} aria-label="Refresh events" /></span></div>
              <div class="card-body">
                {#if !evs}
                  <span class="skel" style="height:120px"></span>
                {:else if evs.length === 0}
                  <p class="muted small" style="margin:0">No Docker events in the last 24 h.</p>
                {:else}
                  <ol class="events" data-testid="events-list">
                    {#each evs as e, i (i)}
                      <li>
                        <span class="mono xs faint">{fmtDateTime(e.time)}</span>
                        <span class="badge {/die|oom|kill/.test(e.action) ? 'bad' : /stop/.test(e.action) ? 'warn' : /start|healthy/.test(e.action) ? 'ok' : ''}">{e.action}</span>
                        {#if e.attributes.exitCode}<span class="small faint">exit {e.attributes.exitCode}</span>{/if}
                      </li>
                    {/each}
                  </ol>
                {/if}
              </div>
            </section>
          </div>
        {:else if tab === 'config'}
          {#if cfgC}
            <p class="card-hint cfghint"><Icon name="info" size={13} />The container's config on this site — edits stay in this browser until you commit them (change bar below). Also under <a href={appHref(site, `config/containers/${cfgC.containerSlug}`)}>Site › Config › Containers</a>.</p>
            <ContainerEditor {site} containerSlug={cfgC.containerSlug} embedded />
          {:else}
            <section class="card"><Empty icon="cog" title="Not in the config" text={c.failedCopy ? 'A copy kept after a failed update has no config; it is removed by the next update or by docker rm.' : `${c.ref} is not a container of ${site} in the head config.`} compact /></section>
          {/if}
        {/if}
      </div>
    {/if}
  </Load>
{/if}

<style>
  .ctabs {
    margin-bottom: 16px;
    overflow-x: auto;
    scrollbar-width: none;
  }
  .ctabs button {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    white-space: nowrap;
  }
  .ov {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 14px;
    align-items: start;
  }
  .ov .wide {
    grid-column: 1 / -1;
  }
  @media (max-width: 900px) {
    .ov {
      grid-template-columns: minmax(0, 1fr);
    }
  }
  .ch {
    font-size: var(--fs-md);
    margin: 0 0 10px;
  }
  .meters {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 10px;
    margin: 14px 0 8px;
  }
  .m {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    background: var(--surface-2);
    min-width: 0;
  }
  .m strong {
    font-size: 18px;
    font-variant-numeric: tabular-nums;
  }
  .m :global(.spark) {
    max-width: 100%;
  }
  .linkish {
    all: unset;
    cursor: pointer;
    color: var(--accent-text);
  }
  .linkish:hover {
    text-decoration: underline;
  }
  .linkish:focus-visible {
    outline: 2px solid var(--accent);
    border-radius: 3px;
  }
  .chip + .chip {
    margin-left: 4px;
  }
  .checks,
  .events {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .checks {
    max-height: 260px;
    overflow: auto;
  }
  .checks li,
  .events li {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .dot.ok {
    background: var(--green);
  }
  .dot.bad {
    background: var(--red);
  }
  .sub {
    margin: 14px 0 6px;
    font-size: var(--fs-sm);
    color: var(--text-2);
  }
  .brk {
    overflow-wrap: anywhere;
    word-break: break-all;
  }
  .envbox {
    max-height: 420px;
    overflow: auto;
  }
  .raw {
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    padding: 8px 10px;
    background: var(--code-bg);
    max-height: 70vh;
    overflow: auto;
  }
  .cfghint {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 0 0 12px;
  }
</style>
