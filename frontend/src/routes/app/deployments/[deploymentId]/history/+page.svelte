<script lang="ts">
  import { getScope } from '$lib/config-editor/context';
  import HistoryPage from '$lib/branch/HistoryPage.svelte';
  const scope = getScope();
</script>

<svelte:head><title>History · {scope.label} · OPM</title></svelte:head>
<div class="page">{#if scope.loaded}<HistoryPage {scope} />{/if}</div>
