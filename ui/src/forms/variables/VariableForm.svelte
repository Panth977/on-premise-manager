<script lang="ts" module>
  import type { Variable, VariableType } from '@opm/shared/types';
  export function blankVariable(): Variable {
    return { variableSlug: '', label: '', description: '', type: 'string', value: null, required: false, options: null, validation: null };
  }
  export const VARIABLE_TYPES: { value: VariableType; label: string; title: string }[] = [
    { value: 'string', label: 'text', title: 'Text (string)' },
    { value: 'number', label: 'number', title: 'Number' },
    { value: 'boolean', label: 'toggle', title: 'Toggle (boolean)' },
    { value: 'enum', label: 'enum', title: 'One of a list (enum)' },
    { value: 'list', label: 'list', title: 'List of texts' },
    { value: 'json', label: 'json', title: 'JSON object / array' },
  ];
</script>

<script lang="ts">
  /**
   * Variable definition + value (SPEC §11; §21 #102) — ONE form for cloud add / edit (template: "Default value",
   * deployment: "Value"), agent read and agent local edit (scope 'agent', §21 #113: the whole definition is a local edit,
   * like on the cloud). Drawer content (7–9 fields); the caller owns the Drawer + Save.
   * Fields: Label · Slug · Description · Type · Options | Min / Max | Pattern · Default value | Value · Required.
   * Validation = shared validateVariable + duplicate slug (create) + `serverIssues` (from the save call).
   * Test ids: var-label · var-slug(-wrap/-edit/-auto) · var-desc · var-type(-<t>) · var-options · var-min · var-max ·
   * var-pattern · var-value · var-required · var-usage · var-summary.
   */
  import type { ValidationIssue, VariableValue } from '@opm/shared/types';
  import { Issues, RE, validateVariable, type ValidateMode } from '@opm/shared/validate';
  import Field from '../../primitives/Field.svelte';
  import Toggle from '../../primitives/Toggle.svelte';
  import ChipList from '../../primitives/ChipList.svelte';
  import SegmentedControl from '../../primitives/SegmentedControl.svelte';
  import NumberStepper from '../../primitives/NumberStepper.svelte';
  import SlugInput from '../../primitives/SlugInput.svelte';
  import Issues_ from '../../primitives/Issues.svelte';
  import VariableValueInput from './VariableValueInput.svelte';
  import { useConfigHost, type FormMode } from '../host';
  import { LOCK_FIXED } from '../locks';

  interface Props {
    value: Variable;
    mode: FormMode;
    /** issues the save call returned (paths `variables[<slug>].…`) */
    serverIssues?: readonly ValidationIssue[];
    /** show errors of untouched fields (after the first Save attempt; edit mode always) */
    submitted?: boolean;
    valid?: boolean;
  }
  let { value = $bindable(), mode, serverIssues = [], submitted = false, valid = $bindable(true) }: Props = $props();
  const host = useConfigHost();
  const ro = $derived(mode === 'read' || host.readOnly);
  const isNew = $derived(mode === 'create');
  const base = $derived(`variables[${value.variableSlug}]`);
  const vmode = $derived<ValidateMode>(host.scope === 'template' ? 'template' : host.scope === 'agent' ? 'agent' : 'draft');
  /** a definition field is locked in the agent (only the value is local) */
  const defLock = (f: string): string | null => host.lock(`${base}.${f}`); // §21 #113: the agent edits definitions too
  const valueLock = $derived(host.lock(`${base}.value`));

  const TYPE_HINT: Record<VariableType, string> = { string: 'Text; optional RE2 pattern', number: 'Number; optional min / max', boolean: 'true / false', enum: 'One of the options below', list: 'A list of texts (join with {{ var.x | join(",") }})', json: 'A JSON object or array' };

  const local = $derived.by(() => {
    const is = new Issues(vmode);
    validateVariable($state.snapshot(value) as Variable, base, is, vmode);
    const out: ValidationIssue[] = [...is.list];
    if (isNew && host.config().variables.some((x) => x.variableSlug === value.variableSlug)) out.push({ path: `${base}.variableSlug`, code: 'V_DUPLICATE', message: `variable ${value.variableSlug} exists`, severity: 'error' });
    return out;
  });
  const all = $derived([...local, ...serverIssues.filter((s) => !local.some((l) => l.path === s.path && l.code === s.code))]);
  const show = $derived(submitted || mode === 'edit' || serverIssues.length > 0);
  const at = (p: string) => (show ? all.filter((i) => i.path === `${base}.${p}`) : []);
  $effect(() => {
    valid = !ro && !all.some((i) => i.severity === 'error');
  });

  function setType(t: VariableType) {
    const old = value.type;
    value.type = t;
    if (t !== old) value.value = t === 'list' ? [] : t === 'boolean' ? false : null;
    value.options = t === 'enum' ? (value.options ?? []) : null;
    if (t !== 'number' && t !== 'string') value.validation = null;
    else if (value.validation) value.validation = { min: t === 'number' ? value.validation.min : null, max: t === 'number' ? value.validation.max : null, pattern: t === 'string' ? value.validation.pattern : null };
  }
  function setRule(k: 'min' | 'max' | 'pattern', raw: string | number | null) {
    const cur = value.validation ?? { min: null, max: null, pattern: null };
    const val = k === 'pattern' ? (raw === '' ? null : raw) : typeof raw === 'number' && Number.isFinite(raw) ? raw : null;
    const next = { ...cur, [k]: val };
    value.validation = next.min === null && next.max === null && next.pattern === null ? null : next;
  }
</script>

<fieldset class="form stack" disabled={ro} data-testid="variable-form">
  {#if serverIssues.length}<Issues_ issues={serverIssues} title="Not saved" testid="var-summary" />{/if}
  <Field label="Label" id="v-label" required lock={defLock('label')} issues={at('label')}>
    <input id="v-label" class="input" bind:value={value.label} maxlength="80" placeholder="Redis host port" disabled={ro || !!defLock('label')} data-testid="var-label" />
  </Field>
  <Field label="Slug" id="v-slug" required lock={isNew ? defLock('variableSlug') : (defLock('variableSlug') ?? LOCK_FIXED)} hint={isNew ? 'From the label (pencil = edit). Name in expressions: {{ var.<slug> }} — fixed after create' : undefined} issues={at('variableSlug')}>
    <SlugInput id="v-slug" bind:value={value.variableSlug} from={value.label} fixed={!isNew} disabled={ro || !!defLock('variableSlug')} placeholder="redis-host-port" testid="var-slug" />
  </Field>
  <Field label="Description" id="v-desc" lock={defLock('description')} issues={at('description')}>
    <textarea id="v-desc" class="textarea" rows="2" bind:value={value.description} maxlength="2000" disabled={ro || !!defLock('description')} data-testid="var-desc"></textarea>
  </Field>
  <Field label="Type" id="v-type" group hint={TYPE_HINT[value.type]} lock={defLock('type')} issues={at('type')}>
    <div class="types"><SegmentedControl id="v-type" options={VARIABLE_TYPES} value={value.type} onchange={setType} ariaLabel="Variable type" disabled={ro || !!defLock('type')} testid="var-type" /></div>
  </Field>
  {#if value.type === 'enum'}
    <Field label="Options" id="v-opts" required group hint="1–100 unique values" lock={defLock('options')} issues={at('options')}>
      <ChipList id="v-opts" value={value.options ?? []} onchange={(o) => (value.options = o)} max={100} disabled={ro || !!defLock('options')} testid="var-options" />
    </Field>
  {/if}
  {#if value.type === 'number'}
    <div class="grid-2">
      <Field label="Min" id="v-min" hint="Empty = no minimum" lock={defLock('validation')} issues={at('validation.min')}><NumberStepper id="v-min" nullable value={value.validation?.min ?? null} onchange={(n) => setRule('min', n)} disabled={ro || !!defLock('validation')} label="Minimum" testid="var-min" /></Field>
      <Field label="Max" id="v-max" hint="Empty = no maximum" lock={defLock('validation')} issues={at('validation.max')}><NumberStepper id="v-max" nullable value={value.validation?.max ?? null} onchange={(n) => setRule('max', n)} disabled={ro || !!defLock('validation')} label="Maximum" testid="var-max" /></Field>
    </div>
    {#each at('validation') as i, k (k)}<div class="field-err">{i.message}</div>{/each}
  {/if}
  {#if value.type === 'string'}
    <Field label="Pattern" id="v-pat" hint="Whole-value RE2 regular expression, e.g. ^[a-z]+$ (optional)" lock={defLock('validation')} issues={[...at('validation.pattern'), ...at('validation')]}>
      <input id="v-pat" class="input mono" value={value.validation?.pattern ?? ''} oninput={(e) => setRule('pattern', e.currentTarget.value)} placeholder="[a-z0-9-]+" disabled={ro || !!defLock('validation')} data-testid="var-pattern" />
    </Field>
  {/if}
  <Field label={host.scope === 'template' ? 'Default value' : 'Value'} id="v-val" group={value.type !== 'string'} lock={valueLock} hint={host.scope === 'template' ? 'The value a new deployment copies' : undefined} issues={at('value')}>
    <VariableValueInput id="v-val" variable={value} value={value.value} onchange={(v: VariableValue) => (value.value = v)} disabled={ro || !!valueLock} invalid={at('value').some((i) => i.severity === 'error')} testid="var-value" />
  </Field>
  <Field lock={defLock('required')} issues={at('required')}>
    <Toggle bind:checked={value.required} label="Required (publish needs a value)" disabled={ro || !!defLock('required')} testid="var-required" />
  </Field>
  {#if RE.slug.test(value.variableSlug)}<p class="hint" data-testid="var-usage">Use it as <code>{`{{ var.${value.variableSlug} }}`}</code></p>{/if}
</fieldset>

<style>
  .types {
    overflow-x: auto;
    max-width: 100%;
  }
</style>
