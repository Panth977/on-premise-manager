<script lang="ts">
  import { overview, proposals } from '$lib/state/app.svelte';
  import { draft } from '$lib/state/draft.svelte';
  import { base } from '$app/paths';
  import { page } from '$app/state';
  import PageHead from '$lib/ui/PageHead.svelte';
  import Load from '$lib/ui/Load.svelte';
  import Stale from '$lib/ui/Stale.svelte';
  import Time from '$lib/ui/Time.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Progress from '$lib/ui/Progress.svelte';
  import Empty from '$lib/ui/Empty.svelte';
  import { stagingTone, routeLabel } from '$lib/ui/tones';
  import type { SiteTile } from '@opm/shared/types';
  import { agentOf } from '$lib/agent/state.svelte';

  const o = $derived(overview.data);
  /** nav2: the global site selection — its tile is highlighted (deployment-wide page, the selection is only a filter) */
  const selSite = $derived(page.params.siteSlug ?? '*');
  const seqOf = (revId: string) => proposals.data?.items.find((p) => p.proposal.revId === revId)?.proposal.seq;
  const sites = $derived(o ? [...o.sites].sort((a, b) => Number(b.self) - Number(a.self) || Number(b.inConfig) - Number(a.inConfig) || a.siteSlug.localeCompare(b.siteSlug)) : []);
  function cell(a: string, b: string): { text: string; ok: boolean | null } {
    const v = o?.links[a]?.[b];
    if (v === 'self') return { text: '—', ok: null };
    if (typeof v === 'number') return { text: `${v} ms`, ok: true };
    return { text: 'no link', ok: false };
  }
  /** head config host of a site: null = auto (SPEC §21 #51) → "auto · <advertised address>" */
  const isAuto = (slug: string) => draft.base?.config.sites.find((s) => s.siteSlug === slug)?.config.host === null;
  function cloudCell(t: SiteTile): { text: string; ok: boolean } {
    const r = t.cloudRoute ?? 'none';
    return { text: routeLabel(r), ok: r !== 'none' };
  }
</script>

<PageHead icon="grid" title="Sites" sub={o ? `Deployment ${o.deploymentId} · ${o.sites.length} sites · the same config on every site · refreshes every 5 s` : 'All sites of the deployment'} />

<Load loading={overview.loading} loaded={overview.loaded} error={overview.error} retry={() => overview.load()} rows={6}>
  {#if o}
    {#if !o.head}
      <div class="banner info" style="margin-bottom:16px">
        <Icon name="info" />
        <div class="grow">This site has no configuration yet (fresh install). The first configuration is the cloud’s newest commit; merge it on the <a href="{base}/app/{selSite}/branch">Branch</a> page.</div>
      </div>
    {:else}
      <div class="headline card card-pad">
        <div><div class="section-title">Head</div><strong>seq {o.head.seq}</strong> · {o.head.summary}</div>
        <div class="muted small">created <Time ts={o.head.createdAt} /> · cloud view from {o.cloud.source ?? '—'}, fetched <Time ts={o.cloud.fetchedAt} /></div>
      </div>
    {/if}

    <div class="tiles" data-testid="site-tiles">
      {#each sites as t (t.siteSlug)}
        <article class="tile card" class:stale={!t.reachable} class:sel={t.siteSlug === selSite} aria-current={t.siteSlug === selSite ? 'true' : undefined} data-testid="tile-{t.siteSlug}">
          <header>
            <div class="grow">
              <div class="row" style="gap:6px">
                <span class="dotlive" class:on={t.reachable} aria-hidden="true"></span>
                <h2 class="mono">{t.siteSlug}</h2>
                {#if t.self}<span class="badge accent">this site</span>{/if}
                {#if t.siteSlug === selSite}<span class="badge blue" title="the site selected at the top of the sidebar">selected</span>{/if}
                {#if !t.inConfig}<span class="badge" title="Enrolled, but not in the head config">not in config</span>{/if}
              </div>
              <div class="muted small">{t.label ?? '—'}</div>
            </div>
            {#if t.reachable}<span class="badge ok">online</span>{:else}<Stale lastSeenAt={t.lastSeenAt} />{/if}
          </header>
          <dl class="kv">
            <dt>Host</dt>
            <dd class="mono" data-testid="tile-host-{t.siteSlug}">{#if isAuto(t.siteSlug) || (t.inConfig && t.host === null)}<span class="muted">auto · {t.advertiseHost ?? t.host ?? 'not reported yet'}</span>{:else}{t.host ?? '—'}{/if}{#if t.advertiseHost}<span class="faint"> · mesh {t.advertiseHost}:{t.agentPort}</span>{/if}</dd>
            <dt>Head</dt>
            <dd>{t.head ? `seq ${t.head.seq}` : 'none'}{#if o.head && t.head && t.head.revId !== o.head.revId}<span class="badge warn" style="margin-left:6px">differs</span>{/if}</dd>
            <dt>Staging</dt>
            <dd>
              {#if t.staging}
                <span class="row" style="gap:6px">
                  <a href="{base}/app/{selSite}/branch">seq {seqOf(t.staging.revId) ?? '…'}</a>
                  <span class="badge {stagingTone[t.staging.staging.staging]}">{t.staging.staging.staging}</span>
                  {#if t.staging.staging.staging === 'downloading'}<span style="width:70px"><Progress value={t.staging.staging.progress} /></span>{/if}
                </span>
              {:else}<span class="faint">nothing to stage</span>{/if}
            </dd>
            <dt>Cloud</dt>
            <dd>{routeLabel(t.cloudRoute)}</dd>
            <dt>Containers</dt>
            <dd><span class:ok-text={t.containers.total > 0 && t.containers.ready === t.containers.total} class:warn-text={t.containers.ready < t.containers.total}>{t.containers.ready}/{t.containers.total} ready</span></dd>
            <dt>Agent</dt>
            <dd><a href="{base}/app/{t.siteSlug}/agent" class="aglink" data-testid="tile-agent-{t.siteSlug}">{t.agentVersion ?? '—'}</a>{#if agentOf(t.siteSlug)?.release?.upgradeAvailable} <a class="badge accent" href="{base}/app/{t.siteSlug}/agent" data-testid="tile-upgrade-{t.siteSlug}">upgrade available</a>{/if} · last answer <Time ts={t.lastSeenAt} /></dd>
          </dl>
          {#if t.lock}<div class="banner warn small" data-testid="tile-update-running"><span class="spin" aria-hidden="true"></span><div class="grow">{t.lock.kind === 'rollback' ? 'rollback' : 'update'} {t.lock.updateId.slice(-6)} running (coordinator {t.lock.coordinator})</div></div>{/if}
          <footer class="row">
            <a class="btn sm" href="{base}/app/{t.siteSlug}/containers"><Icon name="container" size={14} />Containers</a>
            <a class="btn sm" href="{base}/app/{t.siteSlug}/status"><Icon name="gauge" size={14} />Status</a>
            <a class="btn sm" href="{base}/app/{t.siteSlug}/config/containers"><Icon name="cog" size={14} />Config</a>
          </footer>
        </article>
      {:else}
        <Empty icon="server" title="No sites yet" text="This agent knows no other site yet. Sites come from the cloud: they are added to the deployment there and enrol with the install command of the Setup page." />
      {/each}
    </div>

    <section class="card" style="margin-top:18px">
      <div class="card-head"><h2>Mesh links</h2><span class="muted small">rtt of the last ping (30 s window) · rows = from, columns = to</span></div>
      <div class="table-wrap">
        <table class="table mesh" data-testid="mesh">
          <thead><tr><th>from \ to</th>{#each sites as t (t.siteSlug)}<th class="mono">{t.siteSlug}</th>{/each}<th>cloud</th></tr></thead>
          <tbody>
            {#each sites as a (a.siteSlug)}
              <tr class:dim={!a.reachable}>
                <th class="mono" scope="row">{a.siteSlug}</th>
                {#each sites as b (b.siteSlug)}
                  {@const c = cell(a.siteSlug, b.siteSlug)}
                  <td class:ok-text={c.ok === true} class:bad-text={c.ok === false}>{#if c.ok === true}<Icon name="check" size={13} /> {/if}{#if c.ok === false}<Icon name="x" size={13} /> {/if}{c.text}</td>
                {/each}
                <td class:ok-text={cloudCell(a).ok} class:bad-text={!cloudCell(a).ok}>{#if cloudCell(a).ok}<Icon name="check" size={13} />{:else}<Icon name="x" size={13} />{/if} {cloudCell(a).text}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </section>
  {/if}
</Load>

<style>
  .tile.sel {
    border-color: color-mix(in srgb, var(--accent) 55%, var(--border));
    box-shadow: 0 0 0 3px var(--accent-soft);
  }
  .headline {
    display: flex;
    justify-content: space-between;
    gap: 12px;
    flex-wrap: wrap;
    align-items: center;
    margin-bottom: 14px;
  }
  .tiles {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(min(100%, 340px), 1fr));
    gap: 14px;
  }
  .tile {
    padding: 14px 16px;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .tile.stale {
    background: color-mix(in srgb, var(--amber-soft) 25%, var(--surface));
  }
  .tile.stale dl {
    opacity: 0.6;
  }
  .tile header {
    display: flex;
    gap: 10px;
    align-items: flex-start;
  }
  .tile h2 {
    font-size: 16px;
  }
  .tile footer {
    margin-top: auto;
    padding-top: 2px;
  }
  .dotlive {
    width: 9px;
    height: 9px;
    border-radius: 50%;
    background: var(--amber);
  }
  .dotlive.on {
    background: var(--green);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--green) 20%, transparent);
  }
  .mesh th[scope='row'] {
    position: static;
    text-transform: none;
    font-size: 13px;
    color: var(--text);
  }
  .mesh td {
    white-space: nowrap;
  }
</style>
