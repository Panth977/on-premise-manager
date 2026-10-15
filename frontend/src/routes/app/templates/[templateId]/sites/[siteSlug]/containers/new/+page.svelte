<script lang="ts">
  import { page } from '$app/state';
  import { getScope } from '$lib/config-editor/context';
  import ContainerEditor from '$lib/config-editor/ContainerEditor.svelte';
  const scope = getScope();
  const q = $derived(page.url.searchParams);
</script>

<svelte:head><title>New container · site {page.params.siteSlug} · {scope.label} · OPM</title></svelte:head>
{#key `${page.params.siteSlug}?${q.toString()}`}
  <ContainerEditor {scope} siteSlug={page.params.siteSlug!} containerSlug={null} duplicate={q.get('duplicate')} service={q.get('service')} version={q.get('version')} />
{/key}
