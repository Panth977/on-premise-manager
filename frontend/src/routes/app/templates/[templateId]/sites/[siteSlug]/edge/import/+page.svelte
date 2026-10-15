<script lang="ts">
  import { page } from '$app/state';
  import { getScope } from '$lib/config-editor/context';
  import NginxImport from '$lib/config-editor/edge/ImportPage.svelte';
  const scope = getScope();
</script>

<svelte:head><title>Import nginx.conf · edge · site {page.params.siteSlug} · {scope.label} · OPM</title></svelte:head>
{#key page.params.siteSlug}
  <NginxImport {scope} siteSlug={page.params.siteSlug!} />
{/key}
