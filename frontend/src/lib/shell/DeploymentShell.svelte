<script lang="ts">
  import type { Snippet } from 'svelte';
  import { onDestroy, onMount } from 'svelte';
  import { page } from '$app/state';
  import Shell, { type NavItem } from './Shell.svelte';
  import SideNav from './SideNav.svelte';
  import { scopeCrumbs } from './crumbs';
  import SitesTree from '../config-editor/SitesTree.svelte';
  import { DeploymentScope } from '../config-editor/scope.svelte';
  import { provideCloudConfigHost } from '../config-editor/configHost';
  import { setScope } from '../config-editor/context';
  import ScopeConflict from '../config-editor/ScopeConflict.svelte';
  import PendingBar from '../branch/PendingBar.svelte';
  import PendingBanners from '../branch/PendingBanners.svelte';
  import { catalog } from '../stores/catalog.svelte';
  import Avatar from '../ui/Avatar.svelte';
  import Icon from '../ui/Icon.svelte';
  import Button from '../ui/Button.svelte';
  import Spinner from '../ui/Spinner.svelte';
  import ErrorState from '../ui/ErrorState.svelte';
  import EmptyState from '../ui/EmptyState.svelte';
  import { LayoutDashboard, KeyRound, Activity, ScrollText, Variable, Lock, Files, GitCompare, History, ArrowLeft, Rocket, GitBranch } from '../ui/icons';

  let { id, children }: { id: string; children: Snippet } = $props();
  // svelte-ignore state_referenced_locally
  const scope = setScope(new DeploymentScope(id));
  provideCloudConfigHost(scope);
  let timer: ReturnType<typeof setInterval> | null = null;
  onMount(async () => {
    await scope.load();
    scope.loadDetected();
    timer = setInterval(() => {
      if (document.hidden) return;
      scope.refresh();
    }, 30_000);
  });
  onDestroy(() => {
    if (timer) clearInterval(timer);
    scope.dispose();
  });
  /** config pages show the change bar even when nothing is pending */
  const onConfigPage = $derived(/\/(variables|secrets|files|sites)(\/|$)/.test(page.url.pathname));

  const base = $derived(`/app/deployments/${id}`);
  const head = $derived(scope.branchState?.head ?? null);
  const nav = $derived.by(() => {
    if (!scope.deployment) return [] as NavItem[];
    const cfg = scope.can('deployment.config', 'read');
    const items: (NavItem & { show: boolean })[] = [
      { href: base, label: 'Overview', icon: LayoutDashboard, exact: true, show: true },
      { href: `${base}/setup`, label: 'Setup', icon: KeyRound, show: scope.can('deployment.setup', 'read') },
      { href: `${base}/status`, label: 'Status', icon: Activity, show: true },
      { href: `${base}/activities`, label: 'Activities', icon: ScrollText, show: scope.can('deployment.activities', 'read') },
      { href: `${base}/variables`, label: 'Variables', icon: Variable, show: cfg, group: 'Config' },
      { href: `${base}/secrets`, label: 'Secrets', icon: Lock, show: cfg, group: 'Config' },
      { href: `${base}/files`, label: 'Files', icon: Files, show: cfg, group: 'Config' },
      { href: `${base}/compare`, label: 'Compare', icon: GitCompare, show: cfg, group: 'Branch', count: scope.pendingCount || null, tone: scope.pendingCount ? 'amber' : null, testid: 'nav-compare' },
      { href: `${base}/history`, label: 'History', icon: History, show: cfg, group: 'Branch', testid: 'nav-history' },
      { href: `${base}/branches`, label: 'Agent branches', icon: GitBranch, show: cfg, group: 'Branch', testid: 'nav-branches' },
    ];
    return items.filter((i) => i.show);
  });
  const cfgNav = $derived(nav.filter((n) => n.group === 'Config'));
  const topNav = $derived(nav.filter((n) => !n.group));
  const branchNav = $derived(nav.filter((n) => n.group === 'Branch'));
  const crumbs = $derived(scopeCrumbs({ label: 'Deployments', href: '/app/deployments' }, scope.deployment?.label ?? '', base, page.url.pathname));
  const tagOf = (slug: string) => catalog.tags.find((t) => t.tagSlug === slug);
</script>

{#if scope.deployment}
  {@const d = scope.deployment}
  <Shell scope="deployment" {crumbs}>
    {#snippet brand()}
      <div class="dbrand">
        <a href="/app/deployments" class="exit" data-testid="exit-deployment-side"><Icon icon={ArrowLeft} size={13} />All deployments</a>
        <a href={base} class="who" data-testid="scope-card">
          <Avatar name={d.label} img={d.image} size={32} />
          <span class="grow" style="min-width:0">
            <span class="strong ellipsis nm" data-testid="dep-name">{d.label}</span>
            <span class="row xs meta">
              <span class="badge outline" data-testid="nav-head" data-tip={head ? head.message.split('\n')[0] : 'no commit yet'}>{head ? `commit ${head.seq}` : 'no commit'}</span>
              {#if scope.pendingCount}<span class="badge amber" data-testid="nav-uncommitted" data-tip="kept in this browser until you commit">{scope.pendingCount} not committed</span>{/if}
              {#each d.tagSlugs.slice(0, 2) as t (t)}
                {@const tg = tagOf(t)}
                <span class="tdot" style={tg ? `background:${tg.color}` : ''} data-tip={tg?.label ?? t}></span>
              {/each}
            </span>
          </span>
        </a>

      </div>
    {/snippet}
    {#snippet sidebar()}
      <SideNav items={topNav} />
      {#if cfgNav.length}
        <SideNav items={cfgNav} />
        <SitesTree {scope} />
      {/if}
      <SideNav items={branchNav} />
    {/snippet}
    {#if scope.loaded && (scope.restoreOffer || scope.stale || scope.rebaseState)}<div class="page wb-page"><PendingBanners {scope} /></div>{:else if scope.loaded}<PendingBanners {scope} />{/if}
    {@render children()}
    {#if scope.loaded && scope.canWrite && (onConfigPage || scope.pendingCount > 0)}<div class="page wb-page bar-page"><PendingBar {scope} always={onConfigPage} /></div>{/if}
  </Shell>
  <ScopeConflict {scope} />
{:else if scope.notFound}
  <div class="center">
    <EmptyState icon={Rocket} title="Deployment not found or you cannot see it" text="It may have been deleted, or your account has no access to it.">
      {#snippet action()}<Button variant="primary" href="/app/deployments">Back to deployments</Button>{/snippet}
    </EmptyState>
  </div>
{:else if scope.error}
  <div class="center"><ErrorState message={scope.error} onretry={() => scope.load()} /></div>
{:else}
  <div class="center splash" aria-busy="true"><Spinner size={18} /><span class="muted">Loading deployment…</span></div>
{/if}


<style>
  .dbrand {
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
  }
  .who {
    color: var(--text);
  }
  .who:hover {
    text-decoration: none;
    border-color: var(--border-strong);
  }
  .nm {
    display: block;
  }
  .meta {
    gap: 4px;
    margin-top: 2px;
  }
  .tdot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--text-3);
    display: inline-block;
  }
  .wb-page {
    padding-bottom: 0;
  }
  .bar-page {
    position: sticky;
    bottom: 0;
    padding-top: 0;
    padding-bottom: 12px;
    z-index: var(--z-editbar, var(--z-sticky));
    pointer-events: none;
  }
  .bar-page :global(*) {
    pointer-events: auto;
  }
  .bar-page :global(.bar) {
    margin-top: 0;
  }
  .wb-page + :global(.page) {
    padding-top: var(--sp-3);
  }
  .wb-page :global(.wbar) {
    margin: 0;
  }
  .center {
    min-height: 70vh;
    display: grid;
    place-items: center;
    padding: 16px;
  }
  .splash {
    display: flex;
    gap: 10px;
    justify-content: center;
    align-items: center;
  }
</style>
