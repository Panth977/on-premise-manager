<script lang="ts">
  import { getScope } from '$lib/config-editor/context';
  import SecretsPage from '$lib/config-editor/SecretsPage.svelte';
  const scope = getScope();
</script>

<svelte:head><title>Secrets · {scope.label} · OPM</title></svelte:head>
<SecretsPage {scope} />
