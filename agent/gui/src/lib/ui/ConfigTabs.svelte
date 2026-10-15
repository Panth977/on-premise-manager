<script lang="ts">
  /**
   * The sections of SITE › Config as a tab row (nav2, SPEC §21 #97 — no nested sidebar items): Containers (count) · Edge ·
   * Volumes (count) · Settings, each with an "edited" dot and error / warning counts of the local working copy. On the
   * edge pages a secondary link "Import nginx.conf".
   */
  import { page } from '$app/state';
  import { draft } from '$lib/state/draft.svelte';
  import { appHref } from '$lib/nav';
  import Icon from './Icon.svelte';

  let { site }: { site: string } = $props();
  type Sec = 'containers' | 'edge' | 'volumes' | 'settings';
  const work = $derived(draft.work?.sites.find((x) => x.siteSlug === site));
  const head = $derived(draft.base?.config.sites.find((x) => x.siteSlug === site));
  const j = (v: unknown) => JSON.stringify(v ?? null);
  function info(sec: Sec) {
    const p = `sites[${site}].config`;
    const list = sec === 'settings' ? draft.issues.filter((i) => i.path.startsWith(`${p}.`) && !/\.(containers|edge|volumes)\b/.test(i.path)) : draft.issuesAt(`${p}.${sec}`);
    const a = work?.config;
    const b = head?.config;
    const edited = !!a && !!b && (sec === 'settings' ? (['host', 'depends', 'notFoundPageId', 'agentPort', 'agentGuiAliasPort'] as const).some((k) => j(a[k]) !== j(b[k])) : j(a[sec]) !== j(b[sec]));
    return { err: list.filter((i) => i.severity === 'error').length, warn: list.filter((i) => i.severity === 'warning').length, edited };
  }
  const TABS = $derived([
    { k: 'containers' as const, label: 'Containers', icon: 'box', n: work?.config.containers.length ?? null },
    { k: 'edge' as const, label: 'Edge', icon: 'globe', n: null },
    { k: 'volumes' as const, label: 'Volumes', icon: 'archive', n: work ? (work.config.volumes ?? []).length : null },
    { k: 'settings' as const, label: 'Settings', icon: 'sliders', n: null },
  ]);
  const path = $derived(page.url.pathname);
  const onEdge = $derived(path.startsWith(appHref(site, 'config/edge')));
</script>

<nav class="ctabs" aria-label="Config sections" data-testid="config-tabs">
  <div class="tabs">
    {#each TABS as t (t.k)}
      {@const inf = info(t.k)}
      {@const href = appHref(site, `config/${t.k}`)}
      <a class="tab" {href} aria-current={path === href || path.startsWith(href + '/') ? 'page' : undefined} data-testid="nav-{t.k}">
        <Icon name={t.icon} size={14} />{t.label}
        {#if t.n !== null}<span class="n">{t.n}</span>{/if}
        {#if inf.edited}<i class="edited" title="edited locally"></i>{/if}
        {#if inf.err}<span class="nav-cnt red" title="{inf.err} errors">{inf.err}</span>{:else if inf.warn}<span class="nav-cnt amber" title="{inf.warn} warnings">{inf.warn}</span>{/if}
      </a>
    {/each}
  </div>
  {#if onEdge}
    <a class="sub" href={appHref(site, 'config/edge/import')} aria-current={path === appHref(site, 'config/edge/import') ? 'page' : undefined} data-testid="nav-edge-import"><Icon name="upload" size={13} />Import nginx.conf</a>
  {/if}
</nav>

<style>
  .ctabs {
    display: flex;
    align-items: flex-end;
    gap: 12px;
    border-bottom: 1px solid var(--border);
    margin: -6px 0 18px;
    overflow-x: auto;
    scrollbar-width: none;
  }
  .tabs {
    display: flex;
    gap: 2px;
  }
  .tab {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    padding: 8px 12px 9px;
    font-size: var(--fs-md);
    font-weight: var(--fw-medium);
    color: var(--text-2);
    border-bottom: 2px solid transparent;
    margin-bottom: -1px;
    white-space: nowrap;
  }
  .tab:hover {
    color: var(--text);
    text-decoration: none;
  }
  .tab[aria-current='page'] {
    color: var(--accent-text);
    border-bottom-color: var(--accent);
  }
  .tab:focus-visible,
  .sub:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: -2px;
    border-radius: var(--r-sm);
  }
  .n {
    font-size: 11px;
    color: var(--text-3);
    font-variant-numeric: tabular-nums;
  }
  i.edited {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--accent);
  }
  .sub {
    margin-left: auto;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 8px 4px 9px;
    font-size: var(--fs-sm);
    color: var(--text-2);
    white-space: nowrap;
  }
  .sub[aria-current='page'] {
    color: var(--accent-text);
  }
</style>
