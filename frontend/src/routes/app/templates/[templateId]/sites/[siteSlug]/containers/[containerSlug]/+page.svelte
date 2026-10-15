<script lang="ts">
  import { page } from '$app/state';
  import { getScope } from '$lib/config-editor/context';
  import ContainerEditor from '$lib/config-editor/ContainerEditor.svelte';
  const scope = getScope();
</script>

<svelte:head><title>{page.params.containerSlug} · site {page.params.siteSlug} · {scope.label} · OPM</title></svelte:head>
{#key `${page.params.siteSlug}/${page.params.containerSlug}`}
  <ContainerEditor {scope} siteSlug={page.params.siteSlug!} containerSlug={page.params.containerSlug!} />
{/key}
