<script lang="ts">
  /**
   * One form field (SPEC §17.0, §21 #102) — the SAME markup in both GUIs and in every mode:
   * label (+ required star, + lock with the reason when the field is locked in this place) · control · issues · hint.
   * Errors / warnings show under the control (the hint hides while there is an error). `error` / `warning` are plain
   * messages (client checks); `issues` are shared-validator issues mapped to this field's path.
   */
  import type { Snippet } from 'svelte';
  import type { ValidationIssue } from '@opm/shared/types';
  import FieldIssues from './FieldIssues.svelte';
  import Icon from './Icon.svelte';
  interface Props {
    label?: string;
    /** the control's id (label for=) */
    id?: string;
    hint?: string;
    required?: boolean;
    error?: string | string[] | null;
    warning?: string | string[] | null;
    issues?: readonly ValidationIssue[];
    /** locked here: the reason ("Cloud only — change it in the cloud console", "Fixed after create") */
    lock?: string | null;
    testid?: string;
    /** a control that is not a single labelable element (segmented control, chip list …): label is a span */
    group?: boolean;
    aside?: Snippet;
    fix?: Snippet<[ValidationIssue]>;
    children: Snippet;
    class?: string;
  }
  let { label, id, hint, required = false, error = null, warning = null, issues = [], lock = null, testid, group = false, aside, fix, children, class: cls = '' }: Props = $props();
  const errs = $derived(error == null ? [] : Array.isArray(error) ? error.filter(Boolean) : error ? [error] : []);
  const warns = $derived(warning == null ? [] : Array.isArray(warning) ? warning.filter(Boolean) : warning ? [warning] : []);
  const hasErr = $derived(errs.length > 0 || issues.some((i) => i.severity === 'error'));
</script>

<div class="field {cls}" class:has-err={hasErr} class:locked={!!lock} data-testid={testid}>
  {#if label || aside}
    <div class="lrow">
      {#if label}
        {#if group}<span class="label" id={id ? `${id}-label` : undefined}>{label}{#if required}<span class="req" aria-hidden="true">*</span>{/if}</span>
        {:else}<label class="label" for={id}>{label}{#if required}<span class="req" aria-hidden="true">*</span>{/if}</label>{/if}
      {/if}
      {#if lock}<span class="lock" title={lock} aria-label={lock} data-testid="field-lock"><Icon name="lock" size={12} /></span>{/if}
      {#if aside}<span class="aside">{@render aside()}</span>{/if}
    </div>
  {/if}
  {@render children()}
  {#each errs as e, k (k)}<div class="field-err" role="alert" data-testid="field-error">{e}</div>{/each}
  {#each warns as w, k (k)}<div class="field-warn">{w}</div>{/each}
  <FieldIssues {issues} {fix} />
  {#if hint && !hasErr}<div class="hint">{hint}</div>{/if}
  {#if lock && !label}<div class="hint lock-hint"><Icon name="lock" size={11} /> {lock}</div>{/if}
</div>

<style>
  .lrow {
    display: flex;
    align-items: center;
    gap: 6px;
    min-height: 18px;
  }
  .lock {
    display: inline-flex;
    color: var(--text-3);
    cursor: help;
  }
  .aside {
    margin-left: auto;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: var(--fs-xs);
    color: var(--text-3);
  }
  .has-err :global(.input),
  .has-err :global(.textarea),
  .has-err :global(.select) {
    border-color: var(--red);
  }
  .lock-hint {
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }
</style>
