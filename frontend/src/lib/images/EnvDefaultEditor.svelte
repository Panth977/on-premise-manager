<script lang="ts" module>
  export type EnvDefaultKind = 'none' | 'value' | 'choice' | 'list';
  /** The kind an ImageEnv default is edited as (list = a plain comma-separated value). */
  export function envKindOf(value: string | undefined, options: string[] | null | undefined): EnvDefaultKind {
    if (Array.isArray(options)) return 'choice';
    if (value === undefined) return 'none';
    if (!value.includes('{{') && value.includes(',')) return 'list';
    return 'value';
  }
</script>

<script lang="ts">
  /**
   * Default of an image env (ImageEnv.value + ImageEnv.options):
   *   none   → no default (value undefined, options null)
   *   value  → plain text or an expression (field imageEnv)
   *   choice → allowed values as chips (options) + the default picked from them (value ∈ options)
   *   list   → a real list value, stored as ONE comma-separated string (options null)
   */
  import type { ExprContext } from '@opm/shared';
  import ExprInput from '../ui/ExprInput.svelte';
  import ChipInput from '../ui/ChipInput.svelte';
  import Select, { type SelectOption } from '../ui/Select.svelte';
  type Props = {
    value: string | undefined;
    options?: string[] | null;
    disabled?: boolean;
    invalid?: boolean;
    testid?: string;
    ctx?: ExprContext | null;
    onchange?: () => void;
  };
  let { value = $bindable(undefined), options = $bindable(null), disabled = false, invalid = false, testid, ctx = null, onchange }: Props = $props();

  let kind = $state<EnvDefaultKind>(envKindOf(value, options));
  // external changes (clone / read from registry) → keep the kind consistent
  $effect(() => {
    const v = value;
    const o = options;
    const k = kind;
    if (Array.isArray(o) && k !== 'choice') kind = 'choice';
    else if (!Array.isArray(o) && k === 'choice') kind = envKindOf(v, null);
    else if (v === undefined && (k === 'value' || k === 'list')) kind = 'none';
    else if (v !== undefined && k === 'none') kind = envKindOf(v, null);
  });
  const listItems = $derived(kind === 'list' && value ? value.split(',').map((x) => x.trim()).filter(Boolean) : []);

  function setKind(k: EnvDefaultKind) {
    if (k === kind) return;
    const prevVals = kind === 'choice' ? (options ?? []) : kind === 'list' ? listItems : value ? [value] : [];
    if (k === 'none') {
      value = undefined;
      options = null;
    } else if (k === 'value') {
      options = null;
      value = kind === 'list' ? (value ?? '') : (prevVals[0] ?? '');
    } else if (k === 'choice') {
      const opts = prevVals.filter((x) => x && !x.includes('{{'));
      options = opts;
      value = opts[0];
    } else {
      options = null;
      value = prevVals.join(',');
    }
    kind = k;
    onchange?.();
  }
  const KIND_OPTIONS: SelectOption[] = (['none', 'value', 'choice', 'list'] as const).map((k) => ({ value: k, label: k }));
  const choiceOptions = $derived<SelectOption[]>([
    { value: '', label: '(no default)' },
    ...(value !== undefined && !(options ?? []).includes(value) ? [{ value, label: `${value} (not an option)` }] : []),
    ...(options ?? []).map((o) => ({ value: o, label: o })),
  ]);
  function setOptions(v: string[]) {
    options = [...v];
    if (value !== undefined && !v.includes(value) && v.length === 0) value = undefined;
    else if (value === undefined && v.length) value = v[0];
    onchange?.();
  }
</script>

<div class="ed" data-testid={testid}>
  <div class="top">
    <div class="k"><Select value={kind} options={KIND_OPTIONS} size="sm" {disabled} ariaLabel="Default kind" testid="env-kind" minWidth={120} onchange={(v) => setKind(v as EnvDefaultKind)} /></div>
    {#if kind === 'value'}
      <div class="grow"><ExprInput value={value ?? ''} field="imageEnv" {ctx} size="sm" {disabled} {invalid} ariaLabel="Default value" testid="env-value" oninput={(v) => ((value = v), onchange?.())} /></div>
    {:else if kind === 'choice'}
      <div class="grow"><Select value={value ?? ''} options={choiceOptions} size="sm" {disabled} {invalid} ariaLabel="Default option" testid="env-choice-default" onchange={(v) => ((value = v || undefined), onchange?.())} /></div>
    {:else if kind === 'list'}
      <div class="grow"><ChipInput value={listItems} max={50} {disabled} {invalid} placeholder="Item + Enter" testid="env-list" onchange={(v) => ((value = v.join(',')), onchange?.())} /></div>
    {:else}
      <span class="faint small grow none">{disabled ? 'secret: no default allowed' : 'no default'}</span>
    {/if}
  </div>
  {#if kind === 'choice'}
    <ChipInput value={options ?? []} max={50} {disabled} placeholder="Allowed value + Enter" testid="env-options" onchange={setOptions} />
    <div class="xs faint">Containers must use one of these values. The default is picked above.</div>
  {:else if kind === 'list'}
    <div class="xs faint">Stored as one comma-separated value, e.g. a,b,c — for a fixed set of allowed values use choice.</div>
  {/if}
</div>

<style>
  .ed {
    display: flex;
    flex-direction: column;
    gap: 5px;
    min-width: 260px;
  }
  .top {
    display: flex;
    gap: 6px;
    align-items: flex-start;
  }
  .k {
    width: 92px;
    flex: none;
  }
  .none {
    min-height: var(--control-h-sm);
    display: flex;
    align-items: center;
  }
</style>
