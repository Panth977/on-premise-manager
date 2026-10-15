<script lang="ts">
  import { headConfig } from '$lib/state/app.svelte';
  import { draft } from '$lib/state/draft.svelte';
  import { showValue } from '$lib/format';
  import PageHead from '$lib/ui/PageHead.svelte';
  import Load from '$lib/ui/Load.svelte';
  import Empty from '$lib/ui/Empty.svelte';
  import NoHead from '$lib/ui/NoHead.svelte';
  import LocalEditBar from '$lib/ui/LocalEditBar.svelte';
  import FieldIssues from '$lib/ui/FieldIssues.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Drawer from '$lib/ui/Drawer.svelte';
  import VariableValueInput from '$opm/forms/variables/VariableValueInput.svelte';
  import VariableForm, { blankVariable } from '$opm/forms/variables/VariableForm.svelte';
  import Confirm from '$lib/ui/Confirm.svelte';
  import type { Variable, VariableValue } from '@opm/shared/types';

  const headVars = $derived(draft.base?.config.variables ?? []);
  function setValue(slug: string, val: VariableValue) {
    const v = draft.work?.variables.find((x) => x.variableSlug === slug);
    if (v) v.value = val;
  }
  const changed = (slug: string) => {
    const a = draft.work?.variables.find((x) => x.variableSlug === slug)?.value;
    const b = headVars.find((x) => x.variableSlug === slug)?.value;
    return JSON.stringify(a ?? null) !== JSON.stringify(b ?? null);
  };
  // the shared VariableForm (SPEC §21 #102: the same form as the cloud drawer); §21 #113 a site is standalone: the
  // whole definition is a local edit, variables are added and removed here (committed from the change bar)
  let formOpen = $state(false);
  let formVar = $state<Variable | null>(null);
  let formValid = $state(true);
  let creating = $state(false);
  let removeSlug = $state<string | null>(null);
  let removeOpen = $state(false);
  function openForm(slug: string) {
    const v = draft.work?.variables.find((x) => x.variableSlug === slug);
    if (!v) return;
    formVar = structuredClone($state.snapshot(v)) as Variable;
    creating = false;
    formOpen = true;
  }
  function openNew() {
    formVar = blankVariable();
    creating = true;
    formOpen = true;
  }
  function applyForm() {
    if (!formVar || !formValid || !draft.work) return;
    const v = structuredClone($state.snapshot(formVar)) as Variable;
    const i = draft.work.variables.findIndex((x) => x.variableSlug === v.variableSlug);
    if (creating || i < 0) draft.work.variables.push(v);
    else draft.work.variables[i] = v;
    formOpen = false;
  }
  function remove() {
    if (!draft.work || !removeSlug) return;
    draft.work.variables = draft.work.variables.filter((x) => x.variableSlug !== removeSlug);
    removeSlug = null;
  }
  const rules = (v: { validation: { min: number | null; max: number | null; pattern: string | null } | null; options: string[] | null }) =>
    [v.validation?.min !== null && v.validation?.min !== undefined ? `min ${v.validation.min}` : '', v.validation?.max !== null && v.validation?.max !== undefined ? `max ${v.validation.max}` : '', v.validation?.pattern ? `pattern ${v.validation.pattern}` : ''].filter(Boolean).join(' · ');
</script>

<PageHead icon="variable" title="Variables" sub="Deployment-wide values, the same on every site. Add, change and remove variables here — every change is a commit of this site's branch (merge it into the cloud from the Branch page).">
  {#snippet actions()}
    <Btn icon="plus" variant="primary" disabled={!draft.work} onclick={openNew} data-testid="add-variable">Add variable</Btn>
  {/snippet}
</PageHead>

<Load loading={headConfig.loading} loaded={headConfig.loaded} error={headConfig.error} retry={() => headConfig.load()}>
  {#if !draft.base?.head}
    <section class="card"><NoHead /></section>
  {:else if (draft.work?.variables.length ?? 0) === 0}
    <section class="card"><Empty icon="variable" title="No variables" text="This deployment has no variables yet. Add one here, or merge a cloud commit on the Branch page."><Btn icon="plus" disabled={!draft.work} onclick={openNew}>Add variable</Btn></Empty></section>
  {:else}
    <section class="card">
      <div class="table-wrap">
        <table class="table" data-testid="variables">
          <thead><tr><th>Slug</th><th>Type</th><th style="min-width:260px">Value</th><th>Head value</th><th></th></tr></thead>
          <tbody>
            {#each draft.work?.variables ?? [] as v (v.variableSlug)}
              {@const iss = draft.issuesAt(`variables[${v.variableSlug}]`)}
              <tr class:changed={changed(v.variableSlug)}>
                <td>
                  <label for="var-{v.variableSlug}" class="mono"><strong>{v.variableSlug}</strong></label>
                  <div class="muted tiny">{v.label}{v.description ? ` — ${v.description}` : ''}</div>
                </td>
                <td class="small nowrap">
                  {v.type}{#if v.required}<span class="badge warn" style="margin-left:6px" title="A value is required">required</span>{/if}
                  {#if rules(v)}<div class="faint tiny">{rules(v)}</div>{/if}
                </td>
                <td>
                  <VariableValueInput variable={v} value={v.value} compact id="var-{v.variableSlug}" testid="var-{v.variableSlug}" invalid={iss.some((i) => i.severity === 'error')} onchange={(val) => setValue(v.variableSlug, val)} />
                  <FieldIssues issues={iss} />
                </td>
                <td class="mono small head">{showValue(headVars.find((x) => x.variableSlug === v.variableSlug)?.value)}</td>
                <td class="actions">
                  {#if changed(v.variableSlug)}
                    <span class="badge accent" title="Changed since head">changed</span>
                    <Btn size="xs" variant="ghost" onclick={() => setValue(v.variableSlug, structuredClone($state.snapshot(headVars.find((x) => x.variableSlug === v.variableSlug)?.value ?? null)))}>Undo</Btn>
                  {/if}
                  {#if !headVars.some((x) => x.variableSlug === v.variableSlug)}<span class="badge accent" title="Added on this site, not committed yet">new</span>{/if}
                  <Btn size="xs" variant="ghost" icon="edit" title="Edit the variable" aria-label="Open {v.variableSlug}" onclick={() => openForm(v.variableSlug)} data-testid="var-open-{v.variableSlug}" />
                  <Btn size="xs" variant="ghost" icon="trash" title="Remove the variable" aria-label="Remove {v.variableSlug}" onclick={() => ((removeSlug = v.variableSlug), (removeOpen = true))} data-testid="var-remove-{v.variableSlug}" />
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </section>
  {/if}
  <LocalEditBar />
</Load>

<Drawer bind:open={formOpen} title={creating ? 'Add variable' : formVar ? `Edit variable ${formVar.variableSlug}` : 'Edit variable'} subtitle="A local edit — kept in this browser, applied on this site when you commit." testid="variable-drawer">
  {#if formOpen && formVar}<VariableForm bind:value={formVar} bind:valid={formValid} mode={creating ? 'create' : 'edit'} />{/if}
  {#snippet footer()}
    <Btn onclick={() => (formOpen = false)}>Cancel</Btn>
    <Btn variant="primary" disabled={!formValid} onclick={applyForm} data-testid="var-save">{creating ? 'Add' : 'Apply'}</Btn>
  {/snippet}
</Drawer>
<Confirm bind:open={removeOpen} title="Remove variable {removeSlug ?? ''}?" confirmLabel="Remove" danger onconfirm={remove} testid="var-remove-confirm">
  <p>Expressions that use it ({'{{'} var.{removeSlug} {'}}'}) are reported by the validation before you can commit.</p>
</Confirm>

<style>
  tr.changed td {
    background: color-mix(in srgb, var(--accent-soft) 45%, transparent);
  }
  .head {
    max-width: 220px;
    overflow-wrap: anywhere;
    color: var(--text-2);
  }
</style>
