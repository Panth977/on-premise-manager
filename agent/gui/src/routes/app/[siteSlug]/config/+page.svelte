<script lang="ts">
  /**
   * SITE › Config with "All sites" (nav2: also the old Sites config module, /app/sites redirects here): one card per site
   * of the head config — host, depends, ports, edge, volumes, containers — with links into each site's Config tabs.
   * A single site redirects to its Containers tab (+page.ts).
   */
  import { base } from '$app/paths';
  import { autoHostText, headConfig, overview } from '$lib/state/app.svelte';
  import { draft } from '$lib/state/draft.svelte';
  import { resolveSiteHosts } from '@opm/shared/validate';
  import PageHead from '$lib/ui/PageHead.svelte';
  import Load from '$lib/ui/Load.svelte';
  import NoHead from '$lib/ui/NoHead.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Stale from '$lib/ui/Stale.svelte';
  import LocalEditBar from '$lib/ui/LocalEditBar.svelte';
  import EntityIcon from '$lib/ui/EntityIcon.svelte';

  const cat = $derived(draft.base?.catalog ?? null);
  const cfg = $derived(draft.asConfig('work'));
  const hosts = $derived(cfg ? resolveSiteHosts(cfg, overview.data?.deploymentId ?? draft.deploymentId, { detected: draft.detectedHosts, placeholder: false }) : {});
  const tile = (s: string) => overview.data?.sites.find((t) => t.siteSlug === s);
</script>

<PageHead icon="cog" eyebrow="All sites" title="Config" sub="Every site of the head config (the same config on every site). Open a site to edit its containers, edge, volumes and settings.">
  {#snippet actions()}
    <Btn icon="plus" disabled title="cloud only: sites are added in the cloud draft">Add site</Btn>
  {/snippet}
</PageHead>

<Load loading={headConfig.loading} loaded={headConfig.loaded} error={headConfig.error} retry={() => headConfig.load()}>
  {#if !draft.base?.head}
    <section class="card"><NoHead /></section>
  {:else}
    <div data-testid="config-summary"><div class="cards" data-testid="site-cards">
      {#each draft.work?.sites ?? [] as s (s.siteSlug)}
        {@const t = tile(s.siteSlug)}
        <article class="card">
          <div class="card-head">
            <div>
              <h2 class="mono">{s.siteSlug}</h2>
              <div class="muted small">{s.label}</div>
            </div>
            <div class="row">
              {#if t && !t.reachable}<Stale lastSeenAt={t.lastSeenAt} />{/if}
              <Btn size="xs" variant="ghost" icon="trash" disabled title="cloud only" aria-label="Remove site (cloud only)" />
            </div>
          </div>
          <div class="card-body stack tight">
            <dl class="kv">
              <dt>Host</dt>
              <dd class="mono" data-testid="site-host-{s.siteSlug}">{#if s.config.host === null}<span class="muted" data-testid="auto-host-{s.siteSlug}">{autoHostText(s.siteSlug)}</span>{:else}{s.config.host}{#if hosts[s.siteSlug]?.fallback}<span class="warn-text small" title={hosts[s.siteSlug]?.fallback?.message}> → {autoHostText(s.siteSlug)} (no value)</span>{:else if hosts[s.siteSlug]?.host && hosts[s.siteSlug]?.host !== s.config.host}<span class="faint"> → {hosts[s.siteSlug]?.host}</span>{/if}{/if}</dd>
              <dt>Depends</dt>
              <dd>{s.config.depends.map((d) => d.siteSlug).join(', ') || '—'}</dd>
              <dt>Agent ports</dt>
              <dd>{s.config.agentPort} · GUI alias {s.config.agentGuiAliasPort ?? 'off'}</dd>
              <dt>Volumes</dt>
              <dd><a href="{base}/app/{s.siteSlug}/config/volumes">{(s.config.volumes ?? []).length}</a> · {(s.config.volumes ?? []).filter((v) => v.backup).length} backed up</dd>
              <dt>Edge</dt>
              <dd>HTTP {s.config.edge.httpPort}{s.config.edge.httpsPort ? ` · HTTPS ${s.config.edge.httpsPort}` : ' · no HTTPS'} · {s.config.edge.hosts.length} hosts ({s.config.edge.hosts.map((h) => h.hostname).join(', ')}) · {s.config.edge.routes.length} routes</dd>
            </dl>
            <div class="section-title" style="margin-top:6px">Containers</div>
            <ul class="clist">
              {#each s.config.containers as c (c.containerSlug)}
                <li>
                  <EntityIcon icon={cat?.services[c.serviceSlug]?.icon} size={15} />
                  <a class="mono" href="{base}/app/{s.siteSlug}/config/containers/{c.containerSlug}">{c.containerSlug}</a>
                  <span class="mono small muted" title="image">{c.serviceSlug}:{c.version}</span>
                  <span class="grow"></span>
                  {#if c.enabled}<span class="badge ok">enabled</span>{:else}<span class="badge">disabled</span>{/if}
                </li>
              {:else}<li class="muted small">No containers.</li>{/each}
            </ul>
            <div class="row" style="margin-top:6px">
              <a class="btn sm primary" href="{base}/app/{s.siteSlug}/config/containers"><Icon name="edit" size={14} />Edit config</a>
              <a class="btn sm" href="{base}/app/{s.siteSlug}/config/containers"><Icon name="plus" size={14} />Add container</a>
            </div>
          </div>
        </article>
      {/each}
    </div></div>
  {/if}
  <LocalEditBar />
</Load>

<style>
  .cards {
    display: grid;
    gap: 14px;
    grid-template-columns: repeat(auto-fill, minmax(min(100%, 420px), 1fr));
  }
  .clist {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .clist li {
    display: flex;
    align-items: center;
    gap: 8px;
  }
</style>
