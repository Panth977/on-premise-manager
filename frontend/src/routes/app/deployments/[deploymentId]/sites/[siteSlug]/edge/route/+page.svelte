<script lang="ts">
  import { page } from '$app/state';
  import { getScope } from '$lib/config-editor/context';
  import RouteEditor from '$lib/config-editor/edge/RoutePage.svelte';
  const scope = getScope();
  const q = $derived(page.url.searchParams);
  const nt = $derived((['proxy', 'redirect', 'rewrite'] as const).find((t) => t === q.get('new')) ?? null);
</script>

<svelte:head><title>{q.get('id') ?? 'New route'} · edge · site {page.params.siteSlug} · {scope.label} · OPM</title></svelte:head>
<div class="page">
{#key `${page.params.siteSlug}?${q.toString()}`}
  <RouteEditor {scope} siteSlug={page.params.siteSlug!} routeId={q.get('id')} newType={nt} />
{/key}
</div>
