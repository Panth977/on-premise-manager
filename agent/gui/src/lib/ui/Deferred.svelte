<script lang="ts">
  /** Text with the DEPLOY_PLACEHOLDER of required variables / secrets ("‹set at deployment›") shown as a styled token. */
  import { DEPLOY_PLACEHOLDER } from '@opm/shared/expr';
  let { text }: { text: string } = $props();
  const parts = $derived(text.split(DEPLOY_PLACEHOLDER));
</script>

{#each parts as p, i (i)}{p}{#if i < parts.length - 1}<span class="deferred" title="A required variable / secret without a value: known only when the deployment is set up" data-testid="deferred">set at deployment</span>{/if}{/each}
