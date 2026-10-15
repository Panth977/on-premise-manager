<script lang="ts" module>
  import type { SecretCharset, SecretDef } from '@opm/shared/types';
  export function blankSecret(): SecretDef {
    return { secretSlug: '', label: '', description: '', required: true, generate: null, value: null, rotatedAt: null };
  }
  export const SECRET_CHARSETS: { value: SecretCharset; label: string; title: string }[] = [
    { value: 'alnum', label: 'alnum', title: 'A–Z a–z 0–9' },
    { value: 'hex', label: 'hex', title: '0–9 a–f' },
    { value: 'base64url', label: 'base64url', title: 'A–Z a–z 0–9 _ -' },
  ];
</script>

<script lang="ts">
  /**
   * Secret DEFINITION (SPEC §11; §21 #102) — ONE form for cloud add / edit, agent read and agent local edit (scope
   * 'agent', §21 #113: the definition is a local edit too; the value is set with SecretValueInput).
   * Values are never edited here. Drawer content (6 fields); the caller owns the Drawer + Save.
   * Fields: Label · Slug · Description · Required · Generate a value (Charset · Length).
   * Test ids: sec-label · sec-slug(-wrap/-edit/-auto) · sec-desc · sec-required · sec-generate · sec-charset(-<c>) ·
   * sec-length · sec-summary.
   */
  import type { ValidationIssue } from '@opm/shared/types';
  import { Issues, validateSecretDef } from '@opm/shared/validate';
  import Field from '../../primitives/Field.svelte';
  import Toggle from '../../primitives/Toggle.svelte';
  import SegmentedControl from '../../primitives/SegmentedControl.svelte';
  import NumberStepper from '../../primitives/NumberStepper.svelte';
  import SlugInput from '../../primitives/SlugInput.svelte';
  import IssueList from '../../primitives/Issues.svelte';
  import { useConfigHost, type FormMode } from '../host';
  import { LOCK_FIXED } from '../locks';

  interface Props {
    value: SecretDef;
    mode: FormMode;
    serverIssues?: readonly ValidationIssue[];
    submitted?: boolean;
    valid?: boolean;
  }
  let { value = $bindable(), mode, serverIssues = [], submitted = false, valid = $bindable(true) }: Props = $props();
  const host = useConfigHost();
  const ro = $derived(mode === 'read' || host.readOnly);
  const isNew = $derived(mode === 'create');
  const base = $derived(`secrets[${value.secretSlug}]`);
  const lockOf = (f: string): string | null => host.lock(`${base}.${f}`); // §21 #113: the agent edits definitions too
  // charset / length are remembered while "Generate" is off
  let charset = $state<SecretCharset>('alnum');
  let length = $state<number | null>(32);
  $effect.pre(() => {
    if (value.generate) {
      charset = value.generate.charset;
      length = value.generate.length;
    }
  });
  function setGen(on: boolean) {
    value.generate = on ? { charset, length: length ?? 32 } : null;
  }

  const local = $derived.by(() => {
    const is = new Issues(host.scope === 'template' ? 'template' : host.scope === 'agent' ? 'agent' : 'draft');
    // the definition only: the value is not part of this form
    validateSecretDef({ ...($state.snapshot(value) as SecretDef), value: null }, base, is, true);
    const out: ValidationIssue[] = is.list.filter((i) => !i.path.startsWith(`${base}.value`));
    if (isNew && host.config().secrets.some((s) => s.secretSlug === value.secretSlug)) out.push({ path: `${base}.secretSlug`, code: 'V_DUPLICATE', message: `secret ${value.secretSlug} exists`, severity: 'error' });
    return out;
  });
  const all = $derived([...local, ...serverIssues.filter((s) => !local.some((l) => l.path === s.path && l.code === s.code))]);
  const show = $derived(submitted || mode === 'edit' || serverIssues.length > 0);
  const at = (p: string) => (show ? all.filter((i) => i.path === `${base}.${p}`) : []);
  $effect(() => {
    valid = !ro && !all.some((i) => i.severity === 'error');
  });
  const genNote = $derived(host.scope === 'template' ? 'Each new deployment generates its own value.' : host.scope === 'agent' ? 'The agent generates a new value when you use Generate on the Secrets page.' : isNew ? 'A value is generated when you add the secret.' : 'Use "Generate new" on the row to rotate the value.');
</script>

<fieldset class="form stack" disabled={ro} data-testid="secret-form">
  {#if serverIssues.length}<IssueList issues={serverIssues} title="Not saved" testid="sec-summary" />{/if}
  <Field label="Label" id="s-label" required lock={lockOf('label')} issues={at('label')}>
    <input id="s-label" class="input" bind:value={value.label} maxlength="80" placeholder="DB password" disabled={ro || !!lockOf('label')} data-testid="sec-label" />
  </Field>
  <Field label="Slug" id="s-slug" required lock={isNew ? lockOf('secretSlug') : (lockOf('secretSlug') ?? LOCK_FIXED)} hint={isNew ? 'From the label (pencil = edit). Name in expressions: {{ secret.<slug> }} — fixed after create' : undefined} issues={at('secretSlug')}>
    <SlugInput id="s-slug" bind:value={value.secretSlug} from={value.label} fixed={!isNew} disabled={ro || !!lockOf('secretSlug')} placeholder="db-password" testid="sec-slug" />
  </Field>
  <Field label="Description" id="s-desc" lock={lockOf('description')} issues={at('description')}>
    <textarea id="s-desc" class="textarea" rows="2" bind:value={value.description} maxlength="2000" disabled={ro || !!lockOf('description')} data-testid="sec-desc"></textarea>
  </Field>
  <Field lock={lockOf('required')} issues={at('required')}>
    <Toggle bind:checked={value.required} label="Required (publish needs a value)" disabled={ro || !!lockOf('required')} testid="sec-required" />
  </Field>
  <div class="gen card card-pad stack">
    <Field lock={lockOf('generate')} issues={at('generate')}>
      <Toggle checked={!!value.generate} onchange={setGen} label="Generate a value (crypto random)" disabled={ro || !!lockOf('generate')} testid="sec-generate" />
    </Field>
    {#if value.generate}
      <div class="genrow">
        <Field label="Charset" id="s-cs" group hint={SECRET_CHARSETS.find((c) => c.value === value.generate?.charset)?.title} issues={at('generate.charset')}>
          <SegmentedControl id="s-cs" options={SECRET_CHARSETS} value={value.generate.charset} onchange={(c) => value.generate && (value.generate.charset = c)} ariaLabel="Charset" disabled={ro || !!lockOf('generate')} testid="sec-charset" />
        </Field>
        <Field label="Length" id="s-len" hint="8–128 characters" issues={at('generate.length')}>
          <NumberStepper id="s-len" value={value.generate.length} onchange={(n) => value.generate && (value.generate.length = n ?? NaN)} min={8} max={128} disabled={ro || !!lockOf('generate')} label="Length" testid="sec-length" />
        </Field>
      </div>
      <p class="hint">{genNote}</p>
    {:else}
      <p class="hint">A person enters the value ({host.scope === 'agent' ? 'Secrets → New value' : 'deployment → Secrets → Set value'}).</p>
    {/if}
  </div>
</fieldset>

<style>
  .genrow {
    display: flex;
    flex-wrap: wrap;
    gap: 12px 20px;
    align-items: flex-start;
  }
  .gen {
    background: var(--surface-2);
    box-shadow: none;
  }
</style>
