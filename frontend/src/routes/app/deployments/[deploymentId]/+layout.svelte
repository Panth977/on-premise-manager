<script lang="ts">
  import { page } from '$app/state';
  import DeploymentShell from '$lib/shell/DeploymentShell.svelte';
  let { children } = $props();
  const id = $derived(page.params.deploymentId ?? '');
</script>

{#key id}
  <DeploymentShell {id}>{@render children()}</DeploymentShell>
{/key}
