<script lang="ts">
  import { getScope } from '$lib/config-editor/context';
  import SitesPage from '$lib/config-editor/SitesPage.svelte';
  const scope = getScope();
</script>

<svelte:head><title>Sites · {scope.label} · OPM</title></svelte:head>
<div class="page">
  <SitesPage {scope} />
</div>
