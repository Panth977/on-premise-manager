<script lang="ts" module>
  import type { FileDef } from '@opm/shared/types';
  export function blankFile(): FileDef {
    return { fileSlug: '', label: '', description: '', mime: 'text/plain', editor: 'text', maxBytes: 1048576, content: null, render: 'expressions', location: { kind: 'auto' } };
  }
</script>

<script lang="ts">
  /**
   * File DEFINITION (SPEC §2.2 Files; static + read-only for containers) — ONE form for the cloud drawer (template +
   * deployment) and the agent's file details (§21 #113: the agent edits definitions too — a site is standalone), §21 #102.
   * Order: Label · Slug · Description · Mime type · Editor · Expressions · Location on the server · Max size.
   * The content is edited separately (per app). Test ids: file-label · file-slug(-*) · file-desc · file-mime ·
   * file-editor(-text|-binary) · file-render · file-location(-*) · file-max · file-unit(-B|-KiB|-MiB) · file-size-error.
   */
  import type { ValidationIssue } from '@opm/shared/types';
  import { Issues, validateFileDef, LIMITS } from '@opm/shared/validate';
  import { formatContentBytes as formatBytes } from '@opm/shared/volume-content';
  import Field from '../../primitives/Field.svelte';
  import SegmentedControl from '../../primitives/SegmentedControl.svelte';
  import NumberStepper from '../../primitives/NumberStepper.svelte';
  import SlugInput from '../../primitives/SlugInput.svelte';
  import Combobox from '../../primitives/Combobox.svelte';
  import Toggle from '../../primitives/Toggle.svelte';
  import LocationField from '../volumes/LocationField.svelte';
  import { MIME_OPTIONS, isTextMime } from './mime';
  import { useConfigHost, type FormMode } from '../host';
  import { LOCK_FIXED } from '../locks';

  type Props = {
    value: FileDef;
    mode: FormMode;
    /** API issues of the last refused save (paths files[<slug>].…) */
    serverIssues?: readonly ValidationIssue[];
    submitted?: boolean;
    valid?: boolean;
    agentHome?: string;
  };
  let { value = $bindable(), mode, serverIssues = [], submitted = false, valid = $bindable(false), agentHome }: Props = $props();
  const host = useConfigHost();
  const create = $derived(mode === 'create');
  const agent = $derived(host.scope === 'agent');
  const ro = $derived(mode === 'read' || host.readOnly);
  const lock: string | null = null; // §21 #113: nothing of a file definition is cloud only
  // svelte-ignore state_referenced_locally
  const mb0 = value.maxBytes;
  let unit = $state<'B' | 'KiB' | 'MiB'>(mb0 % 1048576 === 0 ? 'MiB' : mb0 % 1024 === 0 ? 'KiB' : 'B');
  // svelte-ignore state_referenced_locally
  let amount = $state<number | null>(mb0 % 1048576 === 0 ? mb0 / 1048576 : mb0 % 1024 === 0 ? mb0 / 1024 : mb0);
  function setSize() {
    value.maxBytes = amount !== null && Number.isFinite(amount) ? Math.round(amount * (unit === 'MiB' ? 1048576 : unit === 'KiB' ? 1024 : 1)) : NaN;
  }
  function pickMime(m: string) {
    value.mime = m.trim();
    if (create && !value.content) setEditor(isTextMime(value.mime) ? 'text' : 'binary');
  }
  /** binary files are always mounted as is (render none); back to text turns expressions on again */
  function setEditor(ed: FileDef['editor']) {
    if (ed === value.editor) return;
    value.render = ed === 'binary' ? 'none' : 'expressions';
    value.editor = ed;
  }
  const base = $derived(`files[${value.fileSlug}]`);
  const local = $derived.by(() => {
    const is = new Issues(host.scope === 'template' ? 'template' : host.scope === 'agent' ? 'agent' : 'draft');
    validateFileDef($state.snapshot(value) as FileDef, base, is);
    const out: ValidationIssue[] = [...is.list];
    if (create && host.config().files.some((x) => x.fileSlug === value.fileSlug)) out.push({ path: `${base}.fileSlug`, code: 'V_DUPLICATE', message: `file ${value.fileSlug} exists`, severity: 'error' });
    return out;
  });
  const all = $derived([...local, ...serverIssues.filter((s) => !local.some((l) => l.path === s.path && l.message === s.message))]);
  $effect(() => {
    valid = !ro && !all.some((i) => i.severity === 'error');
  });
  const show = $derived(submitted || serverIssues.length > 0 || mode === 'edit');
  const at = (p: string) => (show ? all.filter((i) => i.path === `${base}.${p}`) : []);
  const mimeOpts = MIME_OPTIONS.map((m) => ({ value: m.value, hint: m.sub }));
  const UNITS = (['B', 'KiB', 'MiB'] as const).map((u) => ({ value: u, label: u }));
</script>

<fieldset class="form stack" disabled={ro} data-testid="file-form">
  <Field label="Label" id="f-label" required {lock} issues={at('label')}><input id="f-label" class="input" bind:value={value.label} maxlength="80" placeholder="Banner text" disabled={ro} data-testid="file-label" /></Field>
  <Field label="Slug" id="f-slug" required lock={lock ?? (create ? null : LOCK_FIXED)} hint={create ? 'From the label (pencil = edit). {{ file.<slug>.path }} — fixed after create' : '{{ file.<slug>.path }} — fixed after create'} issues={at('fileSlug')}>
    <SlugInput id="f-slug" bind:value={value.fileSlug} from={value.label} fixed={!create} disabled={ro} placeholder="banner" testid="file-slug" />
  </Field>
  <Field label="Description" id="f-desc" {lock} issues={at('description')}><textarea id="f-desc" class="textarea" rows="2" bind:value={value.description} disabled={ro} data-testid="file-desc"></textarea></Field>
  <Field label="Mime type" id="f-mime" {lock} hint="Pick or type any type/subtype" issues={at('mime')}>
    <Combobox id="f-mime" value={value.mime} options={mimeOpts} allowCustom mono placeholder="type/subtype" ariaLabel="Mime type" disabled={ro} testid="file-mime" onchange={pickMime} />
  </Field>
  <Field label="Editor" id="f-ed" group {lock} hint={value.editor === 'text' ? 'Edit the content in the browser' : 'Upload / download only'} issues={at('editor')}>
    <SegmentedControl id="f-ed" options={[{ value: 'text' as const, label: 'text' }, { value: 'binary' as const, label: 'binary' }]} value={value.editor} onchange={setEditor} ariaLabel="Editor" disabled={ro} testid="file-editor" />
  </Field>
  {#if value.editor === 'text'}
    <Field label="Expressions" id="f-render" group {lock} hint={value.render === 'expressions' ? 'Rendered ONCE per site (all containers share that copy): var, secret, site, self, deployment — not container.* / file.* (use a variable instead). Secrets are allowed (masked in previews). Write a literal {{ as {{ "{{" }}.' : 'Mounted byte for byte; {{ }} stays as text.'} issues={at('render')}>
      <Toggle id="f-render" checked={value.render === 'expressions'} onchange={(v) => (value.render = v ? 'expressions' : 'none')} label={'Resolve {{ }} expressions'} disabled={ro} testid="file-render" />
    </Field>
  {/if}
  <LocationField value={value.location} kind="files" slug={value.fileSlug} {agentHome} {lock} disabled={ro} showErrors={show} error={[...at('location'), ...at('location.path')].map((i) => i.message)} onchange={(l) => (value.location = l)} testid="file-location" />
  <Field label="Max size" id="f-max" {lock} hint="1 B – 50 MiB (= {Number.isFinite(value.maxBytes) ? formatBytes(value.maxBytes) : '—'}) · limit {formatBytes(LIMITS.fileMaxBytes)} per file" issues={at('maxBytes')}>
    <div class="row">
      <span class="num"><NumberStepper id="f-max" bind:value={amount} onchange={setSize} min={1} max={unit === 'MiB' ? 50 : unit === 'KiB' ? 51200 : 52428800} disabled={ro} testid="file-max" label="Max size" /></span>
      <SegmentedControl options={UNITS} bind:value={unit} onchange={setSize} ariaLabel="Unit" disabled={ro} testid="file-unit" />
    </div>
  </Field>
  {#if value.content && value.content.sizeBytes > value.maxBytes}<div class="field-err" data-testid="file-size-error">The current content ({formatBytes(value.content.sizeBytes)}) is larger than the new max size.</div>{/if}
</fieldset>

<style>
  .row {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
  }
  .num {
    width: 150px;
  }
</style>
