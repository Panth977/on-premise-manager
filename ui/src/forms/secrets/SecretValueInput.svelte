<script lang="ts" module>
  import { LIMITS } from '@opm/shared/validate';
  /** client check of a secret value (8 KiB UTF-8) — the same message in both GUIs */
  export function secretValueError(v: string | null | undefined, required = true): string | null {
    if (v === null || v === undefined || v === '') return required ? 'Required' : null;
    return new TextEncoder().encode(v).length > LIMITS.secretBytes ? `At most ${LIMITS.secretBytes} bytes (8 KiB)` : null;
  }
</script>

<script lang="ts">
  /**
   * "Set a new value" of a secret (§21 #84, #102) — ONE control for the cloud "Set value" modal and the agent Secrets page
   * "New value" column: password input + show toggle + (when the secret has a generate rule) Generate, 8 KiB check.
   * `generated` = a generate is pending (agent: generateNow) → shows the pending badge with Undo instead of the input.
   * Test ids: {testid} (input) · {testid}-show · {testid}-generate · {testid}-undo · {testid}-error.
   */
  import type { SecretDef } from '@opm/shared/types';
  import Btn from '../../primitives/Btn.svelte';
  import Icon from '../../primitives/Icon.svelte';
  interface Props {
    value: string | null;
    onchange: (v: string | null) => void;
    secret: Pick<SecretDef, 'secretSlug' | 'generate'>;
    /** empty = keep the current value (agent table) instead of "Required" (cloud modal) */
    optional?: boolean;
    generated?: boolean;
    ongenerate?: () => void;
    onundo?: () => void;
    disabled?: boolean;
    compact?: boolean;
    /** show the error even when untouched (after a Save attempt) */
    submitted?: boolean;
    id?: string;
    testid?: string;
    placeholder?: string;
  }
  let { value, onchange, secret, optional = false, generated = false, ongenerate, onundo, disabled = false, compact = false, submitted = false, id, testid = 'secret-value', placeholder }: Props = $props();
  let show = $state(false);
  let touched = $state(false);
  const err = $derived(secretValueError(value, !optional));
  const shownErr = $derived(touched || submitted || (optional && !!value) ? err : null);
</script>

{#if generated}
  <div class="row">
    <span class="badge accent"><Icon name="zap" size={12} />new value generated ({secret.generate?.charset} {secret.generate?.length})</span>
    {#if onundo && !disabled}<Btn size="xs" variant="ghost" onclick={onundo} data-testid="{testid}-undo">Undo</Btn>{/if}
  </div>
{:else}
  <div class="row">
    <input
      {id}
      class="input mono grow"
      class:sm={compact}
      type={show ? 'text' : 'password'}
      autocomplete="new-password"
      spellcheck="false"
      placeholder={placeholder ?? (optional ? 'keep current value' : '')}
      value={value ?? ''}
      {disabled}
      aria-label={id ? undefined : `New value for ${secret.secretSlug}`}
      aria-invalid={shownErr ? 'true' : undefined}
      oninput={(e) => {
        touched = true;
        onchange(e.currentTarget.value === '' && optional ? null : e.currentTarget.value);
      }}
      data-testid={testid}
    />
    <Btn size={compact ? 'xs' : 'sm'} variant="ghost" icon={show ? 'eyeOff' : 'eye'} aria-label={show ? 'Hide the typed value' : 'Show the typed value'} onclick={() => (show = !show)} data-testid="{testid}-show" />
    {#if secret.generate && ongenerate && !disabled}<Btn size={compact ? 'xs' : 'sm'} icon="zap" onclick={ongenerate} title="Generate a new value ({secret.generate.charset}, {secret.generate.length})" data-testid="{testid}-generate">Generate</Btn>{/if}
  </div>
  {#if shownErr}<div class="field-err" data-testid="{testid}-error">{shownErr}</div>{/if}
{/if}

<style>
  .row {
    display: flex;
    gap: 6px;
    align-items: center;
    min-width: 0;
  }
  .grow {
    flex: 1;
    min-width: 0;
  }
</style>
