<script lang="ts">
  /** §21 #93: the one-click fix of an issue that has one (V_DEPENDS_SUGGESTED → "Add" to Depends on), else nothing. */
  import type { ValidationIssue } from '@opm/shared/types';
  import { addDepend, canAddDepend, suggestionOf } from '../depends';
  import { toasts } from '../state/toast.svelte';
  import Btn from './Btn.svelte';
  let { issue, onfix }: { issue: ValidationIssue; onfix?: () => void } = $props();
  const sg = $derived(suggestionOf(issue));
  const can = $derived(!!sg && canAddDepend(sg.siteSlug, sg.containerSlug, sg.target));
  function add() {
    if (!sg || !addDepend(sg.siteSlug, sg.containerSlug, sg.target)) return;
    toasts.ok(`${sg.target} added to Depends on of ${sg.containerSlug}`, 'Local edit — save it, then commit it on this site.');
    onfix?.();
  }
</script>

{#if sg && can}
  <Btn size="xs" icon="plus" onclick={add} title="Add {sg.target} to Depends on of {sg.containerSlug} (local edit)" data-testid="fix-depend-{sg.containerSlug}-{sg.target}">Add</Btn>
{/if}
