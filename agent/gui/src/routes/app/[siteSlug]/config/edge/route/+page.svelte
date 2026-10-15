<script lang="ts">
  /**
   * Edge route page (> 12 nested fields): config/edge/route?id=<routeId> or ?new=proxy|redirect|rewrite — the shared
   * RouteForm ($opm, SPEC §21 #102: the same form as the cloud console route page). Edits a COPY of the route; "Apply"
   * writes it into this browser’s change set (committed with the change bar), "Cancel" drops it. Live validation = the shared
   * validator on the working copy with this route substituted; "Try a path" runs the real resolver.
   */
  import { base } from '$app/paths';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { headConfig } from '$lib/state/app.svelte';
  import { draft } from '$lib/state/draft.svelte';
  import { toasts } from '$lib/state/toast.svelte';
  import type { EdgeRoute, Site } from '@opm/shared/types';
  import RouteForm, { applyRoute, newRoute } from '$opm/forms/edge/RouteForm.svelte';
  import PageHead from '$lib/ui/PageHead.svelte';
  import ConfigTabs from '$lib/ui/ConfigTabs.svelte';
  import Load from '$lib/ui/Load.svelte';
  import Empty from '$lib/ui/Empty.svelte';
  import NoHead from '$lib/ui/NoHead.svelte';
  import LocalEditBar from '$lib/ui/LocalEditBar.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';

  type ActionType = EdgeRoute['action']['type'];
  const slug = $derived(page.params.siteSlug ?? '');
  const qid = $derived(page.url.searchParams.get('id'));
  const qnew = $derived(page.url.searchParams.get('new') as ActionType | null);
  const site = $derived(draft.work?.sites.find((s) => s.siteSlug === slug) as Site | undefined);
  const edge = $derived(site?.config.edge);
  const edgeHref = $derived(`${base}/app/${slug}/config/edge`);
  const isNew = $derived(!qid);

  let rf = $state<EdgeRoute | null>(null);
  let original = $state<EdgeRoute | null>(null);
  let missing = $state(false);
  let submitted = $state(false);
  let valid = $state(true);
  let errorCount = $state(0);
  let loadedKey = '';
  $effect(() => {
    if (!edge || !site) return;
    const key = `${slug}|${qid}|${qnew}`;
    if (key === loadedKey) return;
    loadedKey = key;
    missing = false;
    submitted = false;
    if (qid) {
      const r = edge.routes.find((x) => x.routeId === qid);
      if (!r) {
        rf = original = null;
        missing = true;
        return;
      }
      original = JSON.parse(JSON.stringify($state.snapshot(r))) as EdgeRoute;
      rf = JSON.parse(JSON.stringify(original)) as EdgeRoute;
    } else {
      const t: ActionType = qnew === 'redirect' || qnew === 'rewrite' ? qnew : 'proxy';
      original = null;
      rf = newRoute(t, $state.snapshot(site) as Site, (s, v) => draft.base?.catalog?.images[`${s}:${v}`]);
    }
  });
  const dirty = $derived(!!rf && JSON.stringify(rf) !== JSON.stringify(original));

  function apply() {
    submitted = true;
    if (!rf || !site || !valid) return;
    const r = $state.snapshot(rf) as EdgeRoute;
    applyRoute(site, original, r);
    toasts.ok(`Route ${r.routeId.trim()} ${isNew ? 'added' : 'applied'}`, 'In this browser, not committed — commit it to apply.');
    goto(edgeHref);
  }
</script>

<PageHead icon="globe" eyebrow="Site {slug} · edge route" title={rf ? (isNew ? `New ${rf.action.type} route` : `Route ${qid}`) : 'Edge route'} sub="Matches like an nginx location; the action proxies to a container, redirects, or rewrites internally.">
  {#snippet actions()}
    {#if dirty}<span class="badge accent">edited</span>{/if}
    <a class="btn" href={edgeHref} data-testid="route-cancel"><Icon name="chevronLeft" size={14} />Cancel</a>
    {#if rf}<Btn variant="primary" disabled={submitted && !valid} onclick={apply} data-testid="route-apply">{isNew ? 'Add route' : 'Apply'}</Btn>{/if}
  {/snippet}
</PageHead>
<ConfigTabs site={slug} />

<Load loading={headConfig.loading} loaded={headConfig.loaded} error={headConfig.error} retry={() => headConfig.load()}>
  {#if !draft.base?.head}
    <section class="card"><NoHead /></section>
  {:else if !site}
    <section class="card"><Empty icon="server" title="Site {slug} is not in the head config" text="Only sites of the head config have an edge." /></section>
  {:else if missing}
    <section class="card"><Empty icon="globe" title="No route {qid}" text="This site's edge has no route with this id (removed or renamed?)."><a class="btn" href={edgeHref}>Back to the edge</a></Empty></section>
  {:else if rf}
    <RouteForm bind:value={rf} bind:valid bind:errorCount {site} {original} mode={isNew ? 'create' : 'edit'} {submitted} onnotice={(t) => toasts.info(t)} />
  {/if}
  <LocalEditBar />
</Load>
