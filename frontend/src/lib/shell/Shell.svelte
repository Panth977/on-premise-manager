<script lang="ts" module>
  import type { IconNode } from '../ui/icons';
  export interface NavItem {
    href: string;
    label: string;
    icon: IconNode;
    exact?: boolean;
    count?: number | string | null;
    tone?: 'red' | 'amber' | 'accent' | null;
    group?: string;
    testid?: string;
  }
  export interface Crumb {
    label: string;
    href?: string;
    mono?: boolean;
  }
</script>

<script lang="ts">
  /**
   * App frame (round D). Left sidebar = brand + navigation + footer (account menu + theme, in EVERY scope).
   * - root scope: no top bar on desktop; below 900 px a slim bar with the drawer toggle + brand (the root pages have no
   *   breadcrumb row to carry the toggle).
   * - inner scopes (template, deployment): no top bar at all; the main area starts with the breadcrumb row, which holds
   *   the drawer toggle below 900 px. Pages render their own page header (title + actions) below it.
   */
  import type { Snippet } from 'svelte';
  import { setContext } from 'svelte';
  import { afterNavigate } from '$app/navigation';
  import Icon from '../ui/Icon.svelte';
  import { Menu, X, ChevronRight } from '../ui/icons';
  import AccountMenu from './AccountMenu.svelte';
  import SideNav from './SideNav.svelte';
  import SidebarResizer, { loadSidebarWidth } from '$opm/primitives/SidebarResizer.svelte';

  type Props = {
    nav?: NavItem[];
    /** custom navigation (template / deployment scopes: nav items + the Sites tree) */
    sidebar?: Snippet;
    brand: Snippet;
    /** inner scopes: the breadcrumb (also the row with the drawer toggle on phones) */
    crumbs?: Crumb[];
    children: Snippet;
    scope: 'root' | 'template' | 'deployment';
  };
  let { nav = [], sidebar, brand, crumbs = [], children, scope }: Props = $props();
  let mobileOpen = $state(false);
  afterNavigate(() => (mobileOpen = false));
  /** lets the sidebar footer close the phone drawer when it opens an overlay (My profile) */
  setContext('opm.shell', { closeNav: () => (mobileOpen = false) });
  const inner = $derived(scope !== 'root');
  /** the sidebar width the person dragged (null = the --sidebar-w token); remembered per browser */
  let sideW = $state<number | null>(loadSidebarWidth());
  let sideEl = $state<HTMLElement | null>(null);
</script>

{#snippet toggle()}
  <button class="btn ghost icon burger" aria-label={mobileOpen ? 'Close navigation' : 'Open navigation'} aria-expanded={mobileOpen} onclick={() => (mobileOpen = !mobileOpen)} data-testid="nav-toggle"><Icon icon={mobileOpen ? X : Menu} size={18} /></button>
{/snippet}

<div class="shell" class:inner data-scope={scope} style={sideW ? `--sidebar-w:${sideW}px` : undefined}>
  <aside class="side" class:open={mobileOpen} aria-label="Main navigation" data-testid="sidebar-panel" bind:this={sideEl}>
    <div class="brand">{@render brand()}</div>
    <nav class="nav" data-testid="sidebar">
      {#if sidebar}{@render sidebar()}{:else}<SideNav items={nav} />{/if}
    </nav>
    <div class="sfoot" data-testid="sidebar-foot"><AccountMenu /></div>
    <SidebarResizer bind:width={sideW} {sideEl} />
  </aside>
  {#if mobileOpen}<button class="scrim" aria-label="Close navigation" onclick={() => (mobileOpen = false)}></button>{/if}
  <div class="main">
    {#if inner}
      <div class="crumbs" data-testid="breadcrumb-row">
        {@render toggle()}
        <ol class="bc" aria-label="Breadcrumb" data-testid="breadcrumb">
          {#each crumbs as c, i (i)}
            <li class:last={i === crumbs.length - 1}>
              {#if i > 0}<Icon icon={ChevronRight} size={12} class="sep" />{/if}
              {#if c.href && i < crumbs.length - 1}<a href={c.href} class:mono={c.mono}>{c.label}</a>{:else}<span class:mono={c.mono} aria-current={i === crumbs.length - 1 ? 'page' : undefined}>{c.label}</span>{/if}
            </li>
          {/each}
        </ol>
      </div>
    {:else}
      <header class="mtop" data-testid="mobile-top">
        {@render toggle()}
        <div class="mbrand">{@render brand()}</div>
      </header>
    {/if}
    <main class="content" id="content">{@render children()}</main>
  </div>
</div>

<style>
  .shell {
    display: grid;
    grid-template-columns: var(--sidebar-w) minmax(0, 1fr);
    min-height: 100vh;
    /* sticky page parts (site / container headers) stick to the top: there is no top bar */
    --sticky-top: 0px;
  }
  .side {
    position: sticky;
    top: 0;
    height: 100vh;
    display: flex;
    flex-direction: column;
    background: var(--surface);
    border-right: 1px solid var(--border);
    z-index: var(--z-sidebar);
  }
  .inner .side {
    background: var(--surface-2);
  }
  .brand {
    padding: 12px 12px 8px;
  }
  .nav {
    flex: 1;
    overflow-y: auto;
    padding: 4px 8px 12px;
    display: flex;
    flex-direction: column;
    gap: 1px;
  }
  .sfoot {
    border-top: 1px solid var(--border);
    padding: 8px;
  }
  .main {
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .crumbs {
    display: flex;
    align-items: center;
    gap: 6px;
    min-height: 44px;
    padding: 8px var(--sp-8) 0;
    max-width: 1320px;
    width: 100%;
    margin: 0 auto;
  }
  .bc {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    align-items: center;
    min-width: 0;
    flex-wrap: nowrap;
    overflow: hidden;
    font-size: var(--fs-sm);
    color: var(--text-2);
  }
  .bc li {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    min-width: 0;
    flex: none;
    white-space: nowrap;
  }
  .bc li.last {
    flex: 0 1 auto;
    overflow: hidden;
  }
  .bc li :global(.sep) {
    color: var(--text-3);
    margin: 0 2px;
    flex: none;
  }
  .bc a {
    color: var(--text-2);
    max-width: 220px;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .bc a:hover {
    color: var(--text);
  }
  .bc span {
    color: var(--text);
    font-weight: var(--fw-medium);
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .mono {
    font-family: var(--font-mono);
    font-size: 12.5px;
  }
  .mtop {
    display: none;
  }
  .burger {
    display: none;
    flex: none;
  }
  .content {
    flex: 1;
    min-width: 0;
  }
  .inner .content :global(.page) {
    padding-top: var(--sp-3);
  }
  .scrim {
    display: none;
  }
  @media (max-width: 900px) {
    .shell {
      grid-template-columns: minmax(0, 1fr);
    }
    .side {
      position: fixed;
      left: 0;
      top: 0;
      bottom: 0;
      width: min(290px, 86vw);
      transform: translateX(-100%);
      transition: transform var(--dur) var(--ease);
      box-shadow: none;
    }
    .side.open {
      transform: none;
      box-shadow: var(--shadow-lg);
    }
    .scrim {
      display: block;
      position: fixed;
      inset: 0;
      z-index: var(--z-scrim);
      background: var(--overlay);
      border: 0;
    }
    .burger {
      display: inline-flex;
    }
    .mtop {
      position: sticky;
      top: 0;
      z-index: var(--z-header);
      height: var(--topbar-h);
      display: flex;
      align-items: center;
      gap: 6px;
      padding: 0 10px;
      background: var(--bg);
      border-bottom: 1px solid var(--border);
    }
    .shell:not(.inner) {
      --sticky-top: var(--topbar-h);
    }
    .mbrand {
      min-width: 0;
    }
  }
  @media (max-width: 720px) {
    .crumbs {
      padding: 6px var(--sp-4) 0 6px;
    }
    /* phones: only the last two crumbs */
    .bc li:not(:nth-last-child(-n + 2)) {
      display: none;
    }
    .bc li:nth-last-child(2) :global(.sep) {
      display: none;
    }
    .bc a {
      max-width: 130px;
    }
  }
</style>
