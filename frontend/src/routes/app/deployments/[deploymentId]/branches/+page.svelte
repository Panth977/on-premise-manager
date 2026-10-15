<script lang="ts">
  import { getDeploymentScope } from '$lib/config-editor/context';
  import AgentBranchesPage from '$lib/branch/AgentBranchesPage.svelte';
  const scope = getDeploymentScope();
</script>

<svelte:head><title>Agent branches · {scope.label} · OPM</title></svelte:head>
<div class="page">{#if scope.loaded}<AgentBranchesPage {scope} />{/if}</div>
