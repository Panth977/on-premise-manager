<script lang="ts">
  import { getScope } from '$lib/config-editor/context';
  import FilesPage from '$lib/config-editor/FilesPage.svelte';
  const scope = getScope();
</script>

<svelte:head><title>Files · {scope.label} · OPM</title></svelte:head>
<div class="page">
  <FilesPage {scope} />
</div>
