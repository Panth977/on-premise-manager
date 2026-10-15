<script lang="ts">
  /**
   * Template scope (round D): the left sidebar is replaced by "← All templates", the template, Overview / Variables /
   * Secrets / Files and the Sites tree. No top bar and no tab rows: the main area starts with the breadcrumb; every
   * page renders its own header + actions.
   */
  import type { Snippet } from 'svelte';
  import { page } from '$app/state';
  import { onMount, untrack } from 'svelte';
  import { TemplateScope } from './scope.svelte';
  import { provideCloudConfigHost } from './configHost';
  import { setScope } from './context';
  import ScopeConflict from './ScopeConflict.svelte';
  import SitesTree from './SitesTree.svelte';
  import Shell, { type NavItem } from '../shell/Shell.svelte';
  import SideNav from '../shell/SideNav.svelte';
  import { scopeCrumbs } from '../shell/crumbs';
  import IconView from '../ui/IconView.svelte';
  import ErrorState from '../ui/ErrorState.svelte';
  import EmptyState from '../ui/EmptyState.svelte';
  import Button from '../ui/Button.svelte';
  import Icon from '../ui/Icon.svelte';
  import { ArrowLeft, LayoutTemplate, LayoutDashboard, Variable, Lock, Files, GitCompare, History } from '../ui/icons';
  import PendingBar from '../branch/PendingBar.svelte';
  import PendingBanners from '../branch/PendingBanners.svelte';
  import { countBy } from '../util/issues';

  let { templateId, children }: { templateId: string; children: Snippet } = $props();
  const scope = setScope(new TemplateScope(untrack(() => templateId)));
  provideCloudConfigHost(scope);
  onMount(() => {
    void scope.load();
    return () => scope.dispose();
  });

  const base = $derived(`/app/templates/${templateId}`);
  const t = $derived(scope.template);
  const counts = $derived(scope.loaded ? countBy(scope.validate().list) : { errors: 0, warnings: 0 });
  const nav = $derived<NavItem[]>([
    { href: base, label: 'Overview', icon: LayoutDashboard, exact: true, count: counts.errors || null, tone: counts.errors ? 'red' : null, testid: 'nav-overview' },
    { href: `${base}/variables`, label: 'Variables', icon: Variable, count: t?.variables.length ?? null },
    { href: `${base}/secrets`, label: 'Secrets', icon: Lock, count: t?.secrets.length ?? null },
    { href: `${base}/files`, label: 'Files', icon: Files, count: t?.files.length ?? null },
  ]);
  const branchNav = $derived<NavItem[]>([
    { href: `${base}/compare`, label: 'Compare', icon: GitCompare, count: scope.pendingCount || null, tone: scope.pendingCount ? 'amber' : null, testid: 'nav-compare' },
    { href: `${base}/history`, label: 'History', icon: History, testid: 'nav-history' },
  ]);
  const head = $derived(scope.branchState?.head ?? null);
  const crumbs = $derived(scopeCrumbs({ label: 'Templates', href: '/app/templates' }, t?.label ?? '', base, page.url.pathname));
</script>

<ScopeConflict {scope} />
<Shell scope="template" {crumbs}>
  {#snippet brand()}
    <div class="sbrand">
      <a href="/app/templates" class="exit" data-testid="exit-template"><Icon icon={ArrowLeft} size={13} />All templates</a>
      <a href={base} class="who" data-testid="scope-card">
        <span class="pic"><IconView value={t?.icon ?? null} type="template" size={18} /></span>
        <span class="grow" style="min-width:0">
          <span class="strong ellipsis nm" data-testid="tpl-name">{t?.label ?? '…'}</span>
          <span class="row xs" style="gap:4px">
            {#if t}<span class="badge accent" data-testid="template-version" data-tip="template version">v{t.version}</span>{/if}
            {#if head}<span class="badge outline" data-testid="template-head" data-tip={head.message.split('\n')[0]}>commit {head.seq}</span>{/if}
            {#if scope.pendingCount}<span class="badge amber" data-testid="nav-uncommitted" data-tip="kept in this browser until you commit">{scope.pendingCount} not committed</span>{/if}
            {#if t && !scope.canWrite}<span class="badge outline"><Icon icon={Lock} size={10} />read only</span>{/if}
          </span>
        </span>
      </a>
    </div>
  {/snippet}
  {#snippet sidebar()}
    <SideNav items={nav} />
    <div class="nav-grp">Sites</div>
    {#if scope.loaded}<SitesTree {scope} />{:else}<div class="stack tight tree-skel" aria-busy="true"><span class="skel" style="height:26px"></span><span class="skel" style="height:26px;width:70%"></span></div>{/if}
    <SideNav items={branchNav} />
  {/snippet}
  <div class="page">
    {#if scope.loaded}<PendingBanners {scope} />{/if}
    {#if scope.loading && !scope.loaded}
      <div class="stack"><span class="skel" style="height:44px;width:40%"></span><span class="skel" style="height:280px"></span></div>
    {:else if scope.notFound}
      <EmptyState icon={LayoutTemplate} title="Template not found" text="It was deleted or the link is wrong.">
        {#snippet action()}<Button href="/app/templates" icon={ArrowLeft}>All templates</Button>{/snippet}
      </EmptyState>
    {:else if scope.error && !scope.loaded}
      <ErrorState message={scope.error} onretry={() => scope.load()} />
    {:else if t}
      {@render children()}
      <PendingBar {scope} always />
    {/if}
  </div>
</Shell>

<style>
  .sbrand {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .exit {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-size: var(--fs-xs);
    color: var(--text-2);
    padding: 4px 4px 0;
  }
  .exit:hover {
    color: var(--text);
    text-decoration: none;
  }
  .who {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px;
    border: 1px solid var(--border);
    border-radius: var(--r-lg);
    background: var(--surface);
    color: var(--text);
  }
  .who:hover {
    text-decoration: none;
    border-color: var(--border-strong);
  }
  .nm {
    display: block;
  }
  .tree-skel {
    padding: 4px 10px;
  }
  .pic {
    width: 32px;
    height: 32px;
    border-radius: 8px;
    background: var(--accent-soft);
    color: var(--accent-text);
    display: grid;
    place-items: center;
    flex: none;
  }
</style>
