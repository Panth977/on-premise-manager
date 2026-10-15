<script lang="ts">
  import { page } from '$app/state';
  import { getScope } from '$lib/config-editor/context';
  import SitePage from '$lib/config-editor/SitePage.svelte';
  import { SECTION_TITLE } from '$lib/shell/crumbs';
  const scope = getScope();
</script>

<svelte:head><title>{SECTION_TITLE.volumes} · site {page.params.siteSlug} · {scope.label} · OPM</title></svelte:head>
<div class="page">
{#key page.params.siteSlug}
  <SitePage {scope} siteSlug={page.params.siteSlug!} section="volumes" />
{/key}
</div>
