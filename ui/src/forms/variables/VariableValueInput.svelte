<script lang="ts">
  /**
   * Typed value editor of a variable (SPEC §11; §21 #102) — ONE control for the cloud variable drawer, the cloud deployment
   * Variables table (compact), the agent Variables table and the agent variable drawer:
   * string → text · number → stepper (empty = null) · boolean → switch (+ Set null) · enum → select (— not set —) ·
   * list → chips · json → JSON textarea (parse error under it).
   * Test ids: {testid} on the control; {testid}-null (Set null); {testid}-json-error.
   */
  import type { Variable, VariableValue } from '@opm/shared/types';
  import Toggle from '../../primitives/Toggle.svelte';
  import ChipList from '../../primitives/ChipList.svelte';
  import NumberStepper from '../../primitives/NumberStepper.svelte';
  import Select from '../../primitives/Select.svelte';
  import Btn from '../../primitives/Btn.svelte';

  type Props = {
    variable: Pick<Variable, 'type' | 'options' | 'validation'> & Partial<Pick<Variable, 'required' | 'variableSlug' | 'label'>>;
    value: VariableValue;
    disabled?: boolean;
    invalid?: boolean;
    id?: string;
    /** table cell: small controls, smaller JSON box */
    compact?: boolean;
    testid?: string;
    onchange?: (v: VariableValue) => void;
  };
  let { variable, value = $bindable(null), disabled = false, invalid = false, id, compact = false, testid, onchange }: Props = $props();
  const name = $derived(variable.label || variable.variableSlug || 'value');

  function set(v: VariableValue) {
    value = v;
    onchange?.(v);
  }
  // JSON keeps its own text so invalid JSON can be typed
  let jsonText = $state('');
  let jsonErr = $state<string | null>(null);
  let lastJson = '\u0000';
  $effect(() => {
    if (variable.type !== 'json') return;
    const cur = JSON.stringify(value ?? null);
    if (cur !== lastJson) {
      lastJson = cur;
      jsonText = value === null || value === undefined ? '' : JSON.stringify(value, null, 2);
      jsonErr = null;
    }
  });
  function onJson(t: string) {
    jsonText = t;
    if (!t.trim()) {
      jsonErr = null;
      lastJson = 'null';
      set(null);
      return;
    }
    try {
      const v = JSON.parse(t);
      if (typeof v !== 'object' || v === null) throw new Error('must be an object or an array');
      jsonErr = null;
      lastJson = JSON.stringify(v);
      set(v);
    } catch (e) {
      jsonErr = e instanceof Error ? e.message : 'not JSON';
    }
  }
  const enumOptions = $derived([
    ...(!variable.required || value === null ? [{ value: '', label: '— not set —' }] : []),
    ...(variable.options ?? []).map((o) => ({ value: o, label: o })),
    ...(typeof value === 'string' && value !== '' && !(variable.options ?? []).includes(value) ? [{ value, label: `${value} (not an option)` }] : []),
  ]);
  const notSet = $derived(variable.required ? 'required — not set' : 'not set (null)');
</script>

{#if variable.type === 'string'}
  <input {id} class="input" class:sm={compact} value={typeof value === 'string' ? value : ''} oninput={(e) => set(e.currentTarget.value)} {disabled} aria-invalid={invalid ? 'true' : undefined} aria-label={id ? undefined : `Value of ${name}`} placeholder={value === null ? notSet : ''} data-testid={testid} />
{:else if variable.type === 'number'}
  <NumberStepper {id} nullable value={typeof value === 'number' ? value : null} min={variable.validation?.min ?? undefined} max={variable.validation?.max ?? undefined} size={compact ? 'sm' : 'md'} placeholder={notSet} {invalid} {disabled} label={`Value of ${name}`} onchange={(n) => set(n === null || Number.isNaN(n) ? null : n)} {testid} />
{:else if variable.type === 'boolean'}
  <div class="row">
    <Toggle {id} checked={value === true} {disabled} onchange={(v) => set(v)} label={value === null ? 'not set' : value ? 'true' : 'false'} ariaLabel={`Value of ${name}`} {testid} />
    {#if value !== null && !disabled}<Btn size="xs" variant="ghost" onclick={() => set(null)} data-testid={testid ? `${testid}-null` : undefined}>Set null</Btn>{/if}
  </div>
{:else if variable.type === 'enum'}
  <Select {id} value={typeof value === 'string' ? value : ''} options={enumOptions} size={compact ? 'sm' : 'md'} {invalid} {disabled} ariaLabel={`Value of ${name}`} {testid} onchange={(v) => set(v === '' ? null : v)} />
{:else if variable.type === 'list'}
  <ChipList {id} value={Array.isArray(value) ? (value as string[]) : []} onchange={(v) => set(v)} {disabled} {invalid} {testid} placeholder="Add item, Enter" />
{:else}
  <textarea {id} class="textarea mono" rows={compact ? 3 : 6} value={jsonText} {disabled} aria-invalid={invalid || jsonErr ? 'true' : undefined} aria-label={id ? undefined : `JSON value of ${name}`} spellcheck="false" oninput={(e) => onJson(e.currentTarget.value)} data-testid={testid}></textarea>
  {#if jsonErr}<div class="field-err" data-testid={testid ? `${testid}-json-error` : undefined}>JSON: {jsonErr}</div>{/if}
{/if}

<style>
  .row {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
  }
</style>
