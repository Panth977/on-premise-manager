<script lang="ts">
  /**
   * Sites tree of the agent sidebar — the cloud deployment sidebar's tree (frontend SitesTree.svelte), same names and
   * order: Sites › site › Status · Containers (› containers) · Edge · Volumes (› volumes) · Dev tools (› tools) ·
   * Settings · Transfers · Branch · Server (› Docker · Network · Agent · Debug). Every site of the deployment is in the
   * tree (pages of another site go over the mesh), so there is no site picker. Expanded state per node in localStorage
   * (opm.agent.tree); the site / node of the current page is always open.
   */
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import Icon from './Icon.svelte';
  import { appHref, parseAppPath } from '$lib/nav';
  import { overview } from '$lib/state/app.svelte';
  import { draft } from '$lib/state/draft.svelte';
  import { uncommittedTotal, behindCount } from '$lib/state/branch.svelte';
  import { upgradableSites } from '$lib/agent/state.svelte';

  const KEY = 'opm.agent.tree';
  let expanded = $state<Record<string, boolean>>(load());
  function load(): Record<string, boolean> {
    try {
      const v = JSON.parse(localStorage.getItem(KEY) ?? '{}');
      return v && typeof v === 'object' ? v : {};
    } catch {
      return {};
    }
  }
  function persist() {
    try {
      localStorage.setItem(KEY, JSON.stringify(expanded));
    } catch {
      /* not remembered */
    }
  }

  const loc = $derived(parseAppPath(page.url.pathname));
  const sub = $derived(loc ? [loc.mod, ...loc.rest].join('/') : '');
  /** the node of the current page: `<site>`, `<site>/containers`, `<site>/volumes`, `<site>/devtools`, `<site>/server` */
  const curSite = $derived(loc && loc.site !== '*' ? loc.site : null);
  const curNode = $derived.by(() => {
    if (!curSite || !loc) return null;
    if (loc.mod === 'containers') return `${curSite}/containers`;
    if (loc.mod === 'volumes') return `${curSite}/volumes`;
    if (loc.mod === 'devtools') return `${curSite}/devtools`;
    if (['docker', 'network', 'agent', 'debug'].includes(loc.mod)) return `${curSite}/server`;
    return null;
  });
  /** defaults: the site of this agent and Containers open; the rest closed */
  const isOpen = (k: string) => expanded[k] ?? (k === curSite || k === curNode || k.endsWith('/containers') || (!curSite && k === o?.self));
  function toggle(k: string) {
    expanded = { ...expanded, [k]: !isOpen(k) };
    persist();
  }
  // the current page's site and node open themselves
  $effect(() => {
    const ks = [curSite, curNode].filter((k): k is string => !!k);
    untrack(() => {
      const closed = ks.filter((k) => expanded[k] === false);
      if (closed.length) {
        expanded = { ...expanded, ...Object.fromEntries(closed.map((k) => [k, true])) };
        persist();
      }
    });
  });

  const o = $derived(overview.data);
  const configs = $derived(draft.work?.sites ?? draft.base?.config.sites ?? []);
  const cfgOf = (s: string) => configs.find((x) => x.siteSlug === s)?.config;
  /** on = the current page is `href` (or below it); exact = only that page */
  const on = (site: string, p: string, exact = false) => curSite === site && (exact ? sub === p : sub === p || sub.startsWith(p + '/'));
  const volOn = (site: string, v: string) => curSite === site && loc?.mod === 'volumes' && page.url.searchParams.get('v') === v;
  const issuesOf = (s: string) => {
    const l = draft.issuesAt(`sites[${s}].config`);
    return { e: l.filter((i) => i.severity === 'error').length, w: l.filter((i) => i.severity === 'warning').length };
  };
  const DEVTOOLS = [
    { k: 'devtools/sql', label: 'SQL', icon: 'database' },
    { k: 'devtools/redis', label: 'Redis', icon: 'layers' },
    { k: 'devtools/mqtt', label: 'MQTT', icon: 'radio' },
    { k: 'devtools/http', label: 'HTTP client', icon: 'send' },
    { k: 'devtools/history', label: 'History', icon: 'history' },
  ];
  const SERVER = [
    { k: 'docker', label: 'Docker', icon: 'layers' },
    { k: 'network', label: 'Network', icon: 'network' },
    { k: 'agent', label: 'Agent', icon: 'server' },
    { k: 'debug', label: 'Debug', icon: 'bug' },
  ];
</script>

{#snippet twisty(k: string, label: string)}
  {@const open = isOpen(k)}
  <button type="button" class="tw" aria-label="{open ? 'Collapse' : 'Expand'} {label}" aria-expanded={open} onclick={() => toggle(k)} data-testid="tree-toggle-{k.replace('/', '-')}"><Icon name="chevronRight" size={13} class={open ? 'rot' : ''} /></button>
{/snippet}

<div class="tree" data-testid="sites-tree">
  <a href={appHref(curSite ?? o?.self ?? '*', 'sites')} class="nav-item" aria-current={loc?.mod === 'sites' ? 'page' : undefined} data-testid="nav-sites">
    <Icon name="grid" size={16} /><span class="grow">Sites</span>
    {#if o}<span class="nav-cnt">{o.sites.length}</span>{:else}<span class="nav-cnt faint">…</span>{/if}
  </a>
  <ul class="lvl1" role="tree" aria-label="Sites">
    {#if !o}<li class="faint xs" style="padding:4px 10px">Loading sites…</li>{/if}
    {#each o?.sites ?? [] as t (t.siteSlug)}
      {@const s = t.siteSlug}
      {@const open = isOpen(s)}
      {@const cfg = cfgOf(s)}
      {@const down = !t.self && !t.reachable}
      {@const notReady = t.self || t.reachable ? Math.max(0, t.containers.total - t.containers.ready) : 0}
      {@const iss = issuesOf(s)}
      {@const upg = upgradableSites().some((a) => a.siteSlug === s)}
      {@const br = t.self ? uncommittedTotal() + behindCount() : 0}
      <li role="treeitem" aria-expanded={open} aria-selected={curSite === s} data-testid="tree-site-{s}">
        <div class="node site" class:cur={curSite === s}>
          {@render twisty(s, s)}
          <a href={appHref(s, 'status')} class="lbl" onclick={() => !open && toggle(s)} data-testid="tree-link-{s}"><span class="mono">{s}</span>{#if t.self}<span class="me">this site</span>{/if}</a>
          {#if down}<span class="nav-cnt red" title="not reachable from this site">off</span>{:else if iss.e}<span class="nav-cnt red" title="{iss.e} errors in the local edits">{iss.e}</span>{:else if notReady}<span class="nav-cnt red" title="{notReady} containers not ready">{notReady}</span>{/if}
        </div>
        {#if open}
          <ul class="lvl2" role="group">
            <li><a href={appHref(s, 'status')} class="nav-item sub" aria-current={on(s, 'status') ? 'page' : undefined} data-testid="tree-{s}-status"><Icon name="gauge" size={14} /><span class="grow">Status</span></a></li>
            <li role="treeitem" aria-expanded={isOpen(`${s}/containers`)} aria-selected={on(s, 'containers')}>
              <div class="node sub">
                {@render twisty(`${s}/containers`, 'Containers')}
                <a href={appHref(s, 'containers')} class="nav-item sub in" aria-current={on(s, 'containers', true) ? 'page' : undefined} data-testid="tree-{s}-containers">
                  <Icon name="container" size={14} /><span class="grow">Containers</span>{#if notReady}<span class="nav-cnt red" title="{notReady} not ready">{notReady}</span>{/if}<span class="nav-cnt">{cfg?.containers.length ?? t.containers.total}</span>
                </a>
              </div>
              {#if isOpen(`${s}/containers`) && cfg?.containers.length}
                <ul class="lvl3" role="group">
                  {#each cfg.containers as c (c.containerSlug)}
                    <li><a href={appHref(s, `containers/${encodeURIComponent(c.containerSlug)}`)} class="nav-item leaf" class:off={!c.enabled} aria-current={on(s, `containers/${c.containerSlug}`) ? 'page' : undefined} title={c.containerSlug} data-testid="tree-{s}-c-{c.containerSlug}"><Icon name="box" size={13} /><span class="grow mono ellipsis">{c.containerSlug}</span></a></li>
                  {/each}
                </ul>
              {/if}
            </li>
            <li><a href={appHref(s, 'config/edge')} class="nav-item sub" aria-current={on(s, 'config/edge') ? 'page' : undefined} data-testid="tree-{s}-edge"><Icon name="globe" size={14} /><span class="grow">Edge</span></a></li>
            <li role="treeitem" aria-expanded={isOpen(`${s}/volumes`)} aria-selected={on(s, 'volumes')}>
              <div class="node sub">
                {@render twisty(`${s}/volumes`, 'Volumes')}
                <a href={appHref(s, 'volumes')} class="nav-item sub in" aria-current={on(s, 'volumes') && !page.url.searchParams.get('v') ? 'page' : undefined} data-testid="tree-{s}-volumes">
                  <Icon name="hardDrive" size={14} /><span class="grow">Volumes</span><span class="nav-cnt">{cfg?.volumes?.length ?? 0}</span>
                </a>
              </div>
              {#if isOpen(`${s}/volumes`) && cfg?.volumes?.length}
                <ul class="lvl3" role="group">
                  {#each cfg.volumes as v (v.volumeSlug)}
                    <li><a href="{appHref(s, 'volumes')}?v={encodeURIComponent(v.volumeSlug)}" class="nav-item leaf" aria-current={volOn(s, v.volumeSlug) ? 'page' : undefined} title={v.label || v.volumeSlug} data-testid="tree-{s}-v-{v.volumeSlug}"><Icon name="folder" size={13} /><span class="grow mono ellipsis">{v.volumeSlug}</span></a></li>
                  {/each}
                </ul>
              {/if}
            </li>
            <li role="treeitem" aria-expanded={isOpen(`${s}/devtools`)} aria-selected={on(s, 'devtools')}>
              <div class="node sub">
                {@render twisty(`${s}/devtools`, 'Dev tools')}
                <a href={appHref(s, 'devtools')} class="nav-item sub in" aria-current={on(s, 'devtools', true) ? 'page' : undefined} data-testid="tree-{s}-devtools"><Icon name="wrench" size={14} /><span class="grow">Dev tools</span>{#if cfg?.devTools?.length}<span class="nav-cnt">{cfg.devTools.length}</span>{/if}</a>
              </div>
              {#if isOpen(`${s}/devtools`)}
                <ul class="lvl3" role="group">
                  {#each DEVTOOLS as d (d.k)}
                    <li><a href={appHref(s, d.k)} class="nav-item leaf" aria-current={on(s, d.k) || (d.k === 'devtools/sql' && ['devtools/postgres', 'devtools/sqlite', 'devtools/cassandra'].some((p) => on(s, p))) ? 'page' : undefined} data-testid="tree-{s}-{d.k.replace('/', '-')}"><Icon name={d.icon} size={13} /><span class="grow">{d.label}</span></a></li>
                  {/each}
                </ul>
              {/if}
            </li>
            <li><a href={appHref(s, 'config/settings')} class="nav-item sub" aria-current={on(s, 'config/settings') ? 'page' : undefined} data-testid="tree-{s}-settings"><Icon name="cog" size={14} /><span class="grow">Settings</span></a></li>
            <li><a href={appHref(s, 'transfers')} class="nav-item sub" aria-current={on(s, 'transfers') ? 'page' : undefined} data-testid="tree-{s}-transfers"><Icon name="folder" size={14} /><span class="grow">Shared files</span></a></li>
            <li><a href={appHref(s, 'branch')} class="nav-item sub" aria-current={on(s, 'branch') ? 'page' : undefined} data-testid="tree-{s}-branch"><Icon name="branch" size={14} /><span class="grow">Branch</span>{#if t.self && draft.dirty}<i class="edited" title="changes in this browser, not committed"></i>{/if}{#if br}<span class="nav-cnt {uncommittedTotal() ? 'amber' : 'accent'}" title="{uncommittedTotal()} not committed · {behindCount()} cloud commits to merge" data-testid="branch-badge">{br}</span>{/if}</a></li>
            <li role="treeitem" aria-expanded={isOpen(`${s}/server`)} aria-selected={curNode === `${s}/server`}>
              <div class="node sub">
                {@render twisty(`${s}/server`, 'Server')}
                <a href={appHref(s, 'docker')} class="nav-item sub in" data-testid="tree-{s}-server"><Icon name="server" size={14} /><span class="grow">Server</span>{#if upg}<span class="nav-cnt accent" title="agent upgrade available">1</span>{/if}</a>
              </div>
              {#if isOpen(`${s}/server`)}
                <ul class="lvl3" role="group">
                  {#each SERVER as m (m.k)}
                    <li><a href={appHref(s, m.k)} class="nav-item leaf" aria-current={on(s, m.k) ? 'page' : undefined} data-testid="tree-{s}-{m.k}"><Icon name={m.icon} size={13} /><span class="grow">{m.label}</span>{#if m.k === 'agent' && upg}<span class="nav-cnt accent" data-testid="nav-agent-upgrade">up</span>{/if}</a></li>
                  {/each}
                </ul>
              {/if}
            </li>
          </ul>
        {/if}
      </li>
    {/each}
  </ul>
</div>

<style>
  .tree {
    display: flex;
    flex-direction: column;
    gap: 1px;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .lvl1 {
    display: flex;
    flex-direction: column;
    gap: 1px;
    margin-left: 6px;
  }
  .node {
    display: flex;
    align-items: center;
    gap: 2px;
    min-height: 30px;
    padding: 0 6px 0 0;
    border-radius: var(--r-md);
    min-width: 0;
  }
  .node.site:hover {
    background: var(--surface-hover);
  }
  .node.cur .lbl {
    color: var(--text);
    font-weight: 600;
  }
  .node.sub {
    min-height: 28px;
    padding: 0;
    margin-left: -22px;
  }
  .tw {
    width: 22px;
    height: 26px;
    display: grid;
    place-items: center;
    border: 0;
    background: transparent;
    color: var(--text-3);
    cursor: pointer;
    border-radius: var(--r-sm);
    flex: none;
  }
  .tw:hover {
    color: var(--text);
  }
  .tw :global(.rot) {
    transform: rotate(90deg);
  }
  .tw :global(svg) {
    transition: transform var(--dur-fast);
  }
  .lbl {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: 6px;
    color: var(--text-2);
    font-size: var(--fs-md);
    overflow: hidden;
    white-space: nowrap;
    padding: 4px 0;
  }
  .lbl:hover {
    text-decoration: none;
    color: var(--text);
  }
  .lbl .mono {
    font-family: var(--font-mono);
    font-size: 12.5px;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .me {
    font-size: 10.5px;
    color: var(--accent-text);
    flex: none;
  }
  .lvl2 {
    margin-left: 16px;
    padding-left: 22px;
    border-left: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    gap: 1px;
    margin-bottom: 4px;
  }
  .lvl3 {
    margin-left: 4px;
    padding-left: 4px;
    border-left: 1px dashed var(--border);
  }
  .nav-item.sub {
    min-height: 28px;
    padding: 4px 8px;
    gap: 8px;
    font-size: var(--fs-sm);
  }
  .nav-item.sub.in {
    flex: 1;
    min-width: 0;
  }
  .nav-item.leaf {
    min-height: 26px;
    padding: 3px 8px;
    gap: 7px;
    font-size: 12px;
    font-weight: 400;
  }
  .nav-item.leaf.off {
    opacity: 0.55;
  }
  .nav-item :global(svg) {
    flex: none;
  }
  i.edited {
    flex: none;
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--accent);
  }
</style>
