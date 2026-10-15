<script lang="ts">
  import type { Variable, VariableValue } from '@opm/shared';
  import { Issues, validateVariable } from '@opm/shared';
  import ScopeHeader from './ScopeHeader.svelte';
  import Button from '../ui/Button.svelte';
  import EmptyState from '../ui/EmptyState.svelte';
  import SearchInput from '../ui/SearchInput.svelte';
  import ConfirmDialog from '../ui/ConfirmDialog.svelte';
  import Icon from '../ui/Icon.svelte';
  import { Plus, Pencil, Trash2, Variable as VarIcon, Undo2, CircleDot } from '../ui/icons';
  import VariableDrawer from './VariableDrawer.svelte';
  import VariableValueInput from '$opm/forms/variables/VariableValueInput.svelte';
  import DraftBanner from './DraftBanner.svelte';
  import { variableRefs } from './refs';
  import { catalog } from '../stores/catalog.svelte';
  import type { AnyScope } from './scope.svelte';

  let { scope }: { scope: AnyScope } = $props();
  const isDep = $derived(scope.kind === 'deployment');

  let q = $state('');
  /** §21 #105: inline value edits go straight into the browser change set (no Save); Revert = the base head value */
  const vars = $derived(scope.config.variables);
  const rows = $derived(vars.filter((v) => !q || `${v.variableSlug} ${v.label} ${v.description}`.toLowerCase().includes(q.toLowerCase())));
  const baseVar = (slug: string) => scope.baseConfig.variables.find((v) => v.variableSlug === slug) ?? null;
  const valueChanged = (v: Variable) => {
    const b = baseVar(v.variableSlug);
    return !!b && JSON.stringify(b.value) !== JSON.stringify(v.value);
  };
  function setValue(slug: string, nv: VariableValue) {
    const v = scope.config.variables.find((x) => x.variableSlug === slug);
    if (v) v.value = nv;
  }

  const issues = $derived.by(() => {
    const is = new Issues(scope.mode);
    for (const v of vars) validateVariable($state.snapshot(v) as Variable, `variables[${v.variableSlug}]`, is);
    return is.list;
  });
  const errorsOf = (slug: string) => issues.filter((i) => i.path.startsWith(`variables[${slug}]`) && i.severity === 'error');
  const warnsOf = (slug: string) => issues.filter((i) => i.path.startsWith(`variables[${slug}]`) && i.severity === 'warning');

  const headVar = (slug: string) => (scope.kind === 'deployment' ? baseVar(slug) : null);
  const changedSinceHead = (v: Variable) => {
    if (scope.kind !== 'deployment' || !scope.headCommit) return false;
    const h = headVar(v.variableSlug);
    return !h || JSON.stringify(h.value) !== JSON.stringify(v.value);
  };

  function show(v: VariableValue): string {
    if (v === null || v === undefined) return '—';
    if (typeof v === 'string') return v === '' ? '""' : v;
    return JSON.stringify(v);
  }

  function revert(slug: string) {
    const b = baseVar(slug);
    if (b) setValue(slug, structuredClone($state.snapshot(b.value)) as VariableValue);
  }

  // drawer
  let drawerOpen = $state(false);
  let editing = $state<Variable | null>(null);
  function openAdd() {
    editing = null;
    drawerOpen = true;
  }
  function openEdit(v: Variable) {
    editing = scope.config.variables.find((x) => x.variableSlug === v.variableSlug) ?? null;
    drawerOpen = true;
  }
  // remove
  let rmOpen = $state(false);
  let rm = $state<Variable | null>(null);
  const rmRefs = $derived(rm ? variableRefs(scope.working(), rm.variableSlug, (id) => catalog.slugOf(id)) : []);
  async function doRemove() {
    const items = scope.config.variables.filter((v) => v.variableSlug !== rm!.variableSlug).map((v) => $state.snapshot(v) as Variable);
    return scope.saveSection('variables', items, `Variable ${rm!.variableSlug} removed`);
  }
</script>

{#if isDep}<DraftBanner deploymentId={scope.id} />{/if}
<ScopeHeader kind={scope.kind} title="Variables" subtitle={isDep ? 'Values of this deployment. Expressions use them as {{ var.<slug> }}.' : 'Defaults a new deployment copies. Expressions use them as {{ var.<slug> }}.'} icon={VarIcon} readOnly={!scope.canWrite}>
  {#snippet actions()}
    <SearchInput bind:value={q} placeholder="Search variables" width="220px" />
    {#if scope.canWrite}<Button variant="primary" icon={Plus} onclick={openAdd} data-testid="add-variable">Add variable</Button>{/if}
  {/snippet}
</ScopeHeader>

{#if !scope.config.variables.length}
  <EmptyState icon={VarIcon} title="No variables" text="Variables hold values like a theme, a port or a host name that containers, hosts and routes use in expressions.">
    {#snippet action()}{#if scope.canWrite}<Button variant="primary" icon={Plus} onclick={openAdd}>Add variable</Button>{/if}{/snippet}
  </EmptyState>
{:else if !rows.length}
  <EmptyState compact title="No match" text="No variable matches “{q}”." />
{:else}
  <div class="table-wrap">
    <table class="table" data-testid="variables-table">
      <thead>
        <tr>
          <th>Slug</th><th>Label</th><th>Type</th>
          <th>{isDep ? 'Value' : 'Default value'}</th>
          <th>Required</th>
          {#if !isDep}<th>Options / rules</th><th>Description</th>{/if}
          <th class="actions"><span class="sr-only">Actions</span></th>
        </tr>
      </thead>
      <tbody>
        {#each rows as v (v.variableSlug)}
          {@const errs = errorsOf(v.variableSlug)}
          {@const warns = warnsOf(v.variableSlug)}
          <tr class:err={errs.length > 0} data-testid="var-row-{v.variableSlug}">
            <td class="mono nowrap">
              {v.variableSlug}
              {#if isDep && changedSinceHead(v)}
                {@const h = headVar(v.variableSlug)}
                <span class="chg" data-tip={h ? `Head value: ${show(h.value)}` : 'Not in the head'} data-testid="changed-{v.variableSlug}"><Icon icon={CircleDot} size={11} /> changed</span>
              {/if}
            </td>
            <td>{v.label}{#if isDep && v.description}<div class="xs faint">{v.description}</div>{/if}</td>
            <td><span class="badge outline">{v.type}</span></td>
            <td class="val">
              {#if isDep && scope.canWrite}
                <div class="inl">
                  <VariableValueInput variable={v} value={v.value} compact invalid={errs.some((e) => e.path.endsWith('.value'))} onchange={(nv) => setValue(v.variableSlug, nv)} testid="val-{v.variableSlug}" />
                  {#if valueChanged(v)}<button class="btn ghost icon sm" aria-label="Revert {v.variableSlug}" data-tip="Revert" onclick={() => revert(v.variableSlug)}><Icon icon={Undo2} size={13} /></button>{/if}
                </div>
                {#each errs as e, i (i)}<div class="xs text-red">{e.message}</div>{/each}
                {#each warns as w, i (i)}<div class="xs text-amber">{w.message}</div>{/each}
              {:else}
                <code class="mono small vshow">{show(v.value)}</code>
                {#each warns as w, i (i)}<div class="xs text-amber">{w.message}</div>{/each}
              {/if}
            </td>
            <td>{#if v.required}<span class="badge blue">required</span>{:else}<span class="faint small">optional</span>{/if}</td>
            {#if !isDep}
              <td class="small">
                {#if v.type === 'enum'}<span class="muted">{(v.options ?? []).join(' · ')}</span>
                {:else if v.validation}<span class="mono xs muted">{[v.validation.min !== null ? `min ${v.validation.min}` : '', v.validation.max !== null ? `max ${v.validation.max}` : '', v.validation.pattern ? `/${v.validation.pattern}/` : ''].filter(Boolean).join(' · ')}</span>
                {:else}<span class="faint">—</span>{/if}
              </td>
              <td class="small muted desc">{v.description || '—'}</td>
            {/if}
            <td class="actions">
              {#if scope.canWrite}
                <Button size="sm" variant="ghost" icon={Pencil} tip={isDep ? 'Edit definition' : 'Edit'} aria-label="Edit {v.variableSlug}" onclick={() => openEdit(v)} />
                <Button size="sm" variant="ghost" icon={Trash2} tip="Remove" aria-label="Remove {v.variableSlug}" onclick={() => ((rm = v), (rmOpen = true))} />
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/if}

<VariableDrawer bind:open={drawerOpen} {scope} variable={editing} />

<ConfirmDialog bind:open={rmOpen} title="Remove variable {rm?.variableSlug}?" confirmLabel="Remove" danger onconfirm={doRemove} testid="remove-variable">
  {#snippet body()}
    {#if rmRefs.length}
      <div class="banner warn small">
        <div>
          <b>{rmRefs.length} expression{rmRefs.length === 1 ? '' : 's'} use <code>var.{rm?.variableSlug}</code></b> — they stop resolving (publish is blocked until you fix them):
          <ul class="refs">{#each rmRefs.slice(0, 8) as r, i (i)}<li><span class="mono xs">{r.path}</span></li>{/each}</ul>
        </div>
      </div>
    {:else}
      <p class="muted">No expression uses <code class="inline">var.{rm?.variableSlug}</code>.</p>
    {/if}
  {/snippet}
</ConfirmDialog>

<style>
  .chg {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    margin-left: 6px;
    font-family: var(--font-sans);
    font-size: 11px;
    color: var(--amber-text);
    background: var(--amber-soft);
    padding: 1px 6px;
    border-radius: 999px;
  }
  .val {
    min-width: 220px;
    max-width: 380px;
  }
  .inl {
    display: flex;
    align-items: center;
    gap: 4px;
  }
  .vshow {
    background: var(--code-bg);
    padding: 1px 6px;
    border-radius: 4px;
    overflow-wrap: anywhere;
  }
  .desc {
    max-width: 260px;
  }
  .refs {
    margin: 6px 0 0;
    padding-left: 16px;
  }
</style>
