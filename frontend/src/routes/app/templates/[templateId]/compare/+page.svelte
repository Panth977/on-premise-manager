<script lang="ts">
  import { getScope } from '$lib/config-editor/context';
  import ComparePage from '$lib/branch/ComparePage.svelte';
  const scope = getScope();
</script>

<svelte:head><title>Compare · {scope.label} · OPM</title></svelte:head>
<ComparePage {scope} />
