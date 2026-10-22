<script lang="ts">
  /**
   * Sites tree of the template / deployment sidebar (round D, replaces the site tab row):
   * Sites (list page) › site › Containers (› container pages) · Edge · Volumes (› volume content pages) · Settings,
   * plus "+ Add site". Containers and Volumes fold too (keys `<site>/containers`, `<site>/volumes`: containers open,
   * volumes closed by default; the node of the current page is always open).
   * Validation badges (errors red, else warnings amber) on every node; the site of the current page is expanded;
   * the expanded state is remembered per scope in localStorage (opm.tree.<kind>.<id>).
   */
  import { page } from '$app/state';
  import { untrack } from 'svelte';
  import type { ValidationIssue } from '@opm/shared';
  import Icon from '../ui/Icon.svelte';
  import { ChevronRight, Server, Boxes, Globe, HardDrive, Settings, Plus, Box, Wrench, Database } from '../ui/icons';
  import AddSiteModal from './AddSiteModal.svelte';
  import type { AnyScope } from './scope.svelte';

  let { scope }: { scope: AnyScope } = $props();
  const KEY = $derived(`opm.tree.${scope.kind}.${scope.id}`);
  let expanded = $state<Record<string, boolean>>({});
  let addOpen = $state(false);

  // load / save the expanded state (storage may be unavailable: private mode, blocked site data)
  $effect(() => {
    const k = KEY;
    untrack(() => {
      try {
        const raw = localStorage.getItem(k);
        expanded = raw ? (JSON.parse(raw) as Record<string, boolean>) : {};
      } catch {
        expanded = {};
      }
    });
  });
  function persist() {
    try {
      localStorage.setItem(KEY, JSON.stringify(expanded));
    } catch {
      /* not remembered */
    }
  }
  function toggle(key: string) {
    expanded = { ...expanded, [key]: !isOpen(key) };
    persist();
  }
  const sitesBase = $derived(`${scope.base}/sites`);
  const path = $derived(page.url.pathname);
  /** the site of the current page (always shown expanded) */
  const current = $derived(path.startsWith(sitesBase + '/') ? decodeURIComponent(path.slice(sitesBase.length + 1).split('/')[0] ?? '') : null);
  /** the Containers / Volumes node of the current page (always shown expanded) */
  const curGroup = $derived.by(() => {
    if (!current) return null;
    const sub = path.slice(sitesBase.length + 1).split('/')[1];
    return sub === 'containers' || sub === 'volumes' ? `${current}/${sub}` : null;
  });
  const isOpen = (key: string) => expanded[key] ?? (key === current || key === curGroup || key.endsWith('/containers'));
  $effect(() => {
    for (const k of [current, curGroup]) if (k && expanded[k] === false) untrack(() => ((expanded = { ...expanded, [k]: true }), persist()));
  });
  const volQ = $derived(page.url.searchParams.get('volume'));

  const sites = $derived(scope.loaded ? scope.working().sites : []);
  const issues = $derived<ValidationIssue[]>(scope.loaded ? scope.validate().list : []);
  type Cnt = { e: number; w: number };
  /** counts per tree node: `<site>` · `<site>/containers` · `<site>/c/<container>` · `<site>/edge` · `<site>/volumes` · `<site>/settings` */
  const counts = $derived.by(() => {
    const m = new Map<string, Cnt>();
    const add = (k: string, i: ValidationIssue) => {
      if (i.severity === 'info') return; // advice only: never a badge
      const c = m.get(k) ?? { e: 0, w: 0 };
      if (i.severity === 'error') c.e++;
      else c.w++;
      m.set(k, c);
    };
    for (const i of issues) {
      const r = /^sites\[([^\]]+)\](?:\.config\.(containers|edge|volumes|devTools)(?:\[([^\]]+)\])?)?/.exec(i.path);
      if (!r) continue;
      const s = r[1]!;
      add(s, i);
      if (r[2] === 'containers') {
        add(`${s}/containers`, i);
        if (r[3]) add(`${s}/c/${r[3]}`, i);
      } else if (r[2]) add(`${s}/${r[2] === 'devTools' ? 'devtools' : r[2]}`, i);
      else add(`${s}/settings`, i);
    }
    return m;
  });
  const cnt = (k: string) => counts.get(k) ?? null;
  const on = (href: string, exact = false) => (exact ? path === href : path === href || path.startsWith(href + '/'));
</script>

{#snippet badge(k: string, testid?: string)}
  {@const c = cnt(k)}
  {#if c?.e}<span class="nav-cnt red" data-testid={testid} data-tip="{c.e} error{c.e === 1 ? '' : 's'}">{c.e}</span>{:else if c?.w}<span class="nav-cnt amber" data-testid={testid} data-tip="{c.w} warning{c.w === 1 ? '' : 's'}">{c.w}</span>{/if}
{/snippet}

<div class="tree" data-testid="sites-tree">
  <a href={sitesBase} class="nav-item" class:on={on(sitesBase, true)} aria-current={on(sitesBase, true) ? 'page' : undefined} data-testid="nav-sites">
    <Icon icon={Server} size={16} />
    <span class="grow">Sites</span>
    {#if scope.loaded}<span class="nav-cnt">{sites.length}</span>{:else}<span class="nav-cnt faint" aria-label="loading" data-testid="sites-loading">…</span>{/if}
  </a>
  <ul class="lvl1" role="tree" aria-label="Sites">
    {#if !scope.loaded}<li class="faint xs" style="padding:4px 10px" data-testid="sites-tree-loading">Loading sites…</li>{/if}
    {#each sites as s (s.siteSlug)}
      {@const open = isOpen(s.siteSlug)}
      {@const sb = `${sitesBase}/${s.siteSlug}`}
      {@const cOpen = isOpen(`${s.siteSlug}/containers`)}
      {@const vols = s.config.volumes ?? []}
      {@const vOpen = isOpen(`${s.siteSlug}/volumes`)}
      <li role="treeitem" aria-expanded={open} aria-selected={current === s.siteSlug} data-testid="tree-site-{s.siteSlug}">
        <div class="node site" class:cur={current === s.siteSlug}>
          <button type="button" class="tw" aria-label="{open ? 'Collapse' : 'Expand'} {s.siteSlug}" onclick={() => toggle(s.siteSlug)} data-testid="tree-toggle-{s.siteSlug}"><Icon icon={ChevronRight} size={13} class={open ? 'rot' : ''} /></button>
          <a href="{sb}/containers" class="lbl" title={s.label} onclick={() => !open && toggle(s.siteSlug)} data-testid="tree-link-{s.siteSlug}"><span class="mono">{s.siteSlug}</span></a>
          {#if scope.siteDirty(s.siteSlug)}<span class="dot-dirty" data-tip="Unsaved changes" data-testid="tree-dirty-{s.siteSlug}"></span>{/if}
          {@render badge(s.siteSlug, `tree-badge-${s.siteSlug}`)}
        </div>
        {#if open}
          <ul class="lvl2" role="group">
            <li role="treeitem" aria-selected={on(`${sb}/containers`)} aria-expanded={s.config.containers.length ? cOpen : undefined}>
              <div class="grp">
                {#if s.config.containers.length}
                  <button type="button" class="tw sm" aria-label="{cOpen ? 'Collapse' : 'Expand'} containers of {s.siteSlug}" onclick={() => toggle(`${s.siteSlug}/containers`)} data-testid="tree-toggle-{s.siteSlug}-containers"><Icon icon={ChevronRight} size={12} class={cOpen ? 'rot' : ''} /></button>
                {:else}<span class="tw sm"></span>{/if}
                <a href="{sb}/containers" class="nav-item sub" class:on={on(`${sb}/containers`, true)} data-testid="tree-{s.siteSlug}-containers">
                  <Icon icon={Boxes} size={14} /><span class="grow">Containers</span>{@render badge(`${s.siteSlug}/containers`)}<span class="nav-cnt">{s.config.containers.length}</span>
                </a>
              </div>
              {#if s.config.containers.length && cOpen}
                <ul class="lvl3" role="group">
                  {#each s.config.containers as c (c.containerSlug)}
                    {@const href = `${sb}/containers/${c.containerSlug}`}
                    <li role="treeitem" aria-selected={on(href)}>
                      <a {href} class="nav-item leaf" class:on={on(href)} class:off={!c.enabled} data-testid="tree-{s.siteSlug}-c-{c.containerSlug}">
                        <Icon icon={Box} size={13} /><span class="grow mono ellipsis">{c.containerSlug}</span>{@render badge(`${s.siteSlug}/c/${c.containerSlug}`)}
                      </a>
                    </li>
                  {/each}
                </ul>
              {/if}
            </li>
            <li role="treeitem" aria-selected={on(`${sb}/edge`)}>
              <a href="{sb}/edge" class="nav-item sub" class:on={on(`${sb}/edge`)} data-testid="tree-{s.siteSlug}-edge"><Icon icon={Globe} size={14} /><span class="grow">Edge</span>{@render badge(`${s.siteSlug}/edge`)}</a>
            </li>
            <li role="treeitem" aria-selected={on(`${sb}/volumes`)} aria-expanded={vols.length ? vOpen : undefined}>
              <div class="grp">
                {#if vols.length}
                  <button type="button" class="tw sm" aria-label="{vOpen ? 'Collapse' : 'Expand'} volumes of {s.siteSlug}" onclick={() => toggle(`${s.siteSlug}/volumes`)} data-testid="tree-toggle-{s.siteSlug}-volumes"><Icon icon={ChevronRight} size={12} class={vOpen ? 'rot' : ''} /></button>
                {:else}<span class="tw sm"></span>{/if}
                <a href="{sb}/volumes" class="nav-item sub" class:on={on(`${sb}/volumes`, true)} data-testid="tree-{s.siteSlug}-volumes"><Icon icon={HardDrive} size={14} /><span class="grow">Volumes</span>{@render badge(`${s.siteSlug}/volumes`)}<span class="nav-cnt">{vols.length}</span></a>
              </div>
              {#if vols.length && vOpen}
                <ul class="lvl3" role="group">
                  {#each vols as v (v.volumeSlug)}
                    {@const vOn = path === `${sb}/volumes/content` && volQ === v.volumeSlug}
                    <li role="treeitem" aria-selected={vOn}>
                      <a href="{sb}/volumes/content?volume={encodeURIComponent(v.volumeSlug)}" class="nav-item leaf" class:on={vOn} aria-current={vOn ? 'page' : undefined} title={v.label || v.volumeSlug} data-testid="tree-{s.siteSlug}-v-{v.volumeSlug}">
                        <Icon icon={Database} size={13} /><span class="grow mono ellipsis">{v.volumeSlug}</span>
                      </a>
                    </li>
                  {/each}
                </ul>
              {/if}
            </li>
            <li role="treeitem" aria-selected={on(`${sb}/devtools`)}>
              <a href="{sb}/devtools" class="nav-item sub" class:on={on(`${sb}/devtools`)} data-testid="tree-{s.siteSlug}-devtools"><Icon icon={Wrench} size={14} /><span class="grow">Dev tools</span>{@render badge(`${s.siteSlug}/devtools`)}{#if s.config.devTools?.length}<span class="nav-cnt">{s.config.devTools.length}</span>{/if}</a>
            </li>
            <li role="treeitem" aria-selected={on(`${sb}/settings`)}>
              <a href="{sb}/settings" class="nav-item sub" class:on={on(`${sb}/settings`)} data-testid="tree-{s.siteSlug}-settings"><Icon icon={Settings} size={14} /><span class="grow">Settings</span>{@render badge(`${s.siteSlug}/settings`)}</a>
            </li>
          </ul>
        {/if}
      </li>
    {/each}
  </ul>
  {#if scope.canWrite}
    <button type="button" class="nav-item add" onclick={() => (addOpen = true)} data-testid="tree-add-site"><Icon icon={Plus} size={14} /><span class="grow">Add site</span></button>
  {/if}
</div>

<AddSiteModal bind:open={addOpen} {scope} />

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
  .node:hover {
    background: var(--surface-hover);
  }
  .node.cur .lbl {
    color: var(--text);
    font-weight: 600;
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
    color: var(--text-2);
    font-size: var(--fs-md);
    overflow: hidden;
    text-overflow: ellipsis;
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
  }
  .lvl2 {
    margin-left: 16px;
    padding-left: 6px;
    border-left: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    gap: 1px;
    margin-bottom: 4px;
  }
  .lvl3 {
    margin-left: 14px;
    padding-left: 4px;
    border-left: 1px dashed var(--border);
  }
  .grp {
    display: flex;
    align-items: center;
    min-width: 0;
  }
  /* a node without children keeps the twisty's room, so every icon of the site lines up */
  .lvl2 > li > .nav-item.sub {
    margin-left: 20px;
  }
  .grp .nav-item.sub {
    flex: 1;
    min-width: 0;
  }
  .tw.sm {
    width: 20px;
    height: 24px;
  }
  .nav-item.sub {
    min-height: 28px;
    padding: 4px 8px;
    gap: 8px;
    font-size: var(--fs-sm);
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
  .nav-item.add {
    border: 0;
    background: transparent;
    cursor: pointer;
    width: 100%;
    text-align: left;
    color: var(--text-3);
    font-size: var(--fs-sm);
    min-height: 28px;
    margin-left: 6px;
    width: calc(100% - 6px);
  }
  .nav-item.add:hover {
    color: var(--accent-text);
  }
  .dot-dirty {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--amber);
    flex: none;
    margin: 0 3px;
  }
</style>
