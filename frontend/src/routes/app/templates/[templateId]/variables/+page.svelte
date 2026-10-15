<script lang="ts">
  import { getScope } from '$lib/config-editor/context';
  import VariablesPage from '$lib/config-editor/VariablesPage.svelte';
  const scope = getScope();
</script>

<svelte:head><title>Variables · {scope.label} · OPM</title></svelte:head>
<VariablesPage {scope} />
