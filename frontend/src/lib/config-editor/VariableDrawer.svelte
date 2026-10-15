<script lang="ts">
  /**
   * Add / edit one variable definition — the shared VariableForm ($opm, SPEC §21 #102: the same form in the agent GUI)
   * in a side drawer. Applies into the browser change set (§21 #105).
   */
  import type { Variable } from '@opm/shared';
  import VariableForm, { blankVariable } from '$opm/forms/variables/VariableForm.svelte';
  import { issuesUnder } from '$opm/forms/host';
  import Drawer from '../ui/Drawer.svelte';
  import Button from '../ui/Button.svelte';
  import type { ConfigScope } from './scope.svelte';

  let { open = $bindable(), scope, variable = null }: { open: boolean; scope: ConfigScope; variable?: Variable | null } = $props();
  let v = $state<Variable>(blankVariable());
  let submitted = $state(false);
  let valid = $state(true);
  $effect(() => {
    if (!open) return;
    v = variable ? (structuredClone($state.snapshot(variable)) as Variable) : blankVariable();
    submitted = false;
  });
  const isNew = $derived(!variable);
  const ro = $derived(!scope.canWrite);
  const server = $derived(issuesUnder(scope.saveIssues, `variables[${v.variableSlug}]`));

  async function save(e: Event) {
    e.preventDefault();
    submitted = true;
    if (!valid) return;
    const item = $state.snapshot(v) as Variable;
    const list = scope.config.variables.map((x) => $state.snapshot(x) as Variable);
    const items = isNew ? [...list, item] : list.map((x) => (x.variableSlug === variable!.variableSlug ? item : x));
    if (await scope.saveSection('variables', items, isNew ? `Variable ${item.variableSlug} added` : `Variable ${item.variableSlug} applied`)) open = false;
  }
</script>

<Drawer bind:open title={ro ? `Variable ${variable?.variableSlug ?? ''}` : isNew ? 'Add variable' : `Edit variable ${variable?.variableSlug}`} subtitle={ro ? 'Read only' : scope.kind === 'template' ? 'The value is the default a new deployment copies.' : 'Saved to the cloud draft at once.'} busy={scope.saving} testid="variable-drawer">
  <form id="var-form" onsubmit={save} novalidate>
    {#if open}<VariableForm bind:value={v} bind:valid mode={ro ? 'read' : isNew ? 'create' : 'edit'} serverIssues={server} {submitted} />{/if}
  </form>
  {#snippet footer()}
    <Button onclick={() => (open = false)} disabled={scope.saving}>{ro ? 'Close' : 'Cancel'}</Button>
    {#if !ro}<Button type="submit" form="var-form" variant="primary" loading={scope.saving} disabled={submitted && !valid} data-testid="var-save">{isNew ? 'Add variable' : 'Apply'}</Button>{/if}
  {/snippet}
</Drawer>
