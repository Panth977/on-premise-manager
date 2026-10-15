<script lang="ts">
  /**
   * Edge route page (…/sites/{s}/edge/route?id=<routeId> or ?new=proxy|redirect|rewrite): the shared RouteForm ($opm,
   * SPEC §21 #102 — the same form as the agent GUI route page) under a sticky header with Cancel / Apply. Edits a LOCAL
   * copy; Apply writes it into the site in the browser change set (committed with Commit…) and returns to the Edge page.
   */
  import type { EdgeRoute, Site } from '@opm/shared';
  import RouteForm, { applyRoute, newRoute } from '$opm/forms/edge/RouteForm.svelte';
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import Button from '../../ui/Button.svelte';
  import EmptyState from '../../ui/EmptyState.svelte';
  import Icon from '../../ui/Icon.svelte';
  import { ArrowLeft, Route as RouteIcon, CircleAlert } from '../../ui/icons';
  import DraftBanner from '../DraftBanner.svelte';
  import { catalog } from '../../stores/catalog.svelte';
  import { toast } from '../../toast.svelte';
  import { useLeaveGuard } from '../../util/guard.svelte';
  import type { AnyScope } from '../scope.svelte';

  type ActType = 'proxy' | 'redirect' | 'rewrite';
  type Props = { scope: AnyScope; siteSlug: string; routeId: string | null; newType?: ActType | null };
  let { scope, siteSlug, routeId, newType = null }: Props = $props();
  const isNew = $derived(routeId === null);
  $effect(() => {
    if (scope.loaded) scope.openSite(siteSlug);
  });
  onMount(() => catalog.ensure());
  const site = $derived<Site | undefined>(scope.siteDrafts[siteSlug]);
  const sitePath = $derived(`${scope.base}/sites/${siteSlug}`);
  const edgePath = $derived(`${sitePath}/edge`);
  const ro = $derived(!scope.canWrite);
  const existing = $derived(!isNew && site ? (site.config.edge.routes.find((r) => r.routeId === routeId) ?? null) : null);

  let value = $state<EdgeRoute | null>(null);
  let initial = $state('');
  let leaving = $state(false);
  let submitted = $state(false);
  let valid = $state(true);
  let errorCount = $state(0);
  $effect(() => {
    if (value || !site || !catalog.loaded) return;
    if (!isNew && !existing) return;
    const v = existing ? (structuredClone($state.snapshot(existing)) as EdgeRoute) : newRoute(newType ?? 'proxy', $state.snapshot(site) as Site, (s, ver) => catalog.image(s, ver) ?? undefined);
    value = v;
    initial = JSON.stringify(v);
  });
  const dirtyLocal = $derived(!!value && !leaving && JSON.stringify(value) !== initial);
  useLeaveGuard(
    () => dirtyLocal,
    () => null,
  );

  function apply() {
    submitted = true;
    if (!valid || !site || !value || ro) return;
    const v = $state.snapshot(value) as EdgeRoute;
    applyRoute(site, existing, v);
    leaving = true;
    toast.success(`Route ${v.routeId.trim()} ${existing ? 'updated' : 'added'} — not committed yet`);
    goto(edgePath);
  }
  function cancel() {
    leaving = true;
    goto(edgePath);
  }
</script>

{#if !scope.loaded || (!site && scope.serverSite(siteSlug)) || (!!site && !value && (isNew || !!existing))}
  <div class="stack"><span class="skel" style="height:60px"></span><span class="skel" style="height:300px"></span></div>
{:else if !site}
  <EmptyState icon={RouteIcon} title="Site {siteSlug} does not exist" text="It may have been removed.">
    {#snippet action()}<Button href="{scope.base}/sites" icon={ArrowLeft}>Back to sites</Button>{/snippet}
  </EmptyState>
{:else if !isNew && !existing}
  <EmptyState icon={RouteIcon} title="No route {routeId} on {siteSlug}" text="It may have been removed or renamed.">
    {#snippet action()}<Button href={edgePath} icon={ArrowLeft}>Back to the edge</Button>{/snippet}
  </EmptyState>
{:else if value}
  {#if scope.kind === 'deployment'}<DraftBanner deploymentId={scope.id} />{/if}
  <div class="sticky">
    <div class="bar">
      <div class="ttl">
        <span class="pic"><Icon icon={RouteIcon} size={18} /></span>
        <h2 class="mono" data-testid="route-title">{isNew ? value.routeId.trim() || 'New route' : routeId}</h2>
        {#if isNew}<span class="badge blue">new</span>{/if}
        <span class="badge outline">{value.action.type}</span>
        {#if dirtyLocal}<span class="dirty" data-tip="Not applied yet"></span>{/if}
      </div>
      <div class="acts">
        <span class="vc" data-testid="route-counter">
          {#if errorCount}<span class="badge red"><Icon icon={CircleAlert} size={11} />{errorCount}</span>{:else}<span class="badge green">valid</span>{/if}
        </span>
        <Button onclick={cancel} data-testid="route-cancel">{ro ? 'Back' : 'Cancel'}</Button>
        {#if !ro}<Button variant="primary" onclick={apply} disabled={submitted && !valid} tip={`Applies the route to site ${siteSlug} in the change set (not committed until Commit…)`} data-testid="route-apply">{isNew ? 'Add route' : 'Apply'}</Button>{/if}
      </div>
    </div>
  </div>
  <RouteForm bind:value bind:valid bind:errorCount {site} original={existing} mode={ro ? 'read' : isNew ? 'create' : 'edit'} {submitted} onnotice={(t) => toast.info(t)} />
{/if}

<style>
  .sticky {
    position: sticky;
    top: var(--sticky-top, 0px);
    z-index: var(--z-sticky);
    background: var(--bg);
    padding: 4px 0 10px;
    margin: -4px 0 var(--sp-4);
    border-bottom: 1px solid var(--border);
  }
  .bar {
    display: flex;
    align-items: center;
    gap: 12px;
    flex-wrap: wrap;
  }
  .ttl {
    display: flex;
    align-items: center;
    gap: 8px;
    flex: 1;
    min-width: 0;
    flex-wrap: wrap;
  }
  .ttl h2 {
    margin: 0;
    font-size: var(--fs-xl, 18px);
    overflow-wrap: anywhere;
  }
  .pic {
    display: grid;
    place-items: center;
    width: 32px;
    height: 32px;
    border-radius: var(--r-md);
    background: var(--accent-soft);
    color: var(--accent-text);
  }
  .dirty {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--amber);
  }
  .acts {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
  }
  .vc {
    display: inline-flex;
    gap: 4px;
  }
</style>
