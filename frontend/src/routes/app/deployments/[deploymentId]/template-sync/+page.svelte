<script lang="ts">
  /** §21 #104: Template sync became Compare with the origin template as the source. */
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { branchKey } from '@opm/shared';
  import { getDeploymentScope } from '$lib/config-editor/context';
  const scope = getDeploymentScope();
  onMount(async () => {
    if (!scope.loaded) await scope.load();
    const o = scope.branchState?.origin;
    const tid = page.url.searchParams.get('templateId');
    const src = tid ? `template:${tid}` : o ? branchKey(o.branch) : null;
    void goto(`/app/deployments/${page.params.deploymentId}/compare${src ? `?source=${encodeURIComponent(src)}` : ''}`, { replaceState: true });
  });
</script>
