<script lang="ts">
  /** §21 #104: Diff + Publish were replaced by Compare (review the uncommitted working copy) + Commit. */
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  onMount(() => void goto(`/app/deployments/${page.params.deploymentId}/compare?review=working`, { replaceState: true }));
</script>
