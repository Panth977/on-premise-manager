<script lang="ts">
  /**
   * The issues of ONE field / row, under the control (SPEC §17.0): error = red (role alert), warning = amber,
   * info = neutral note. `fix` renders a one-click fix next to an issue (e.g. V_DEPENDS_SUGGESTED → Add).
   */
  import type { Snippet } from 'svelte';
  import type { ValidationIssue } from '@opm/shared/types';
  let { issues, fix }: { issues: readonly ValidationIssue[]; fix?: Snippet<[ValidationIssue]> } = $props();
</script>

{#each issues as i, k (k)}
  <div class={i.severity === 'error' ? 'field-err' : i.severity === 'info' ? 'field-info' : 'field-warn'} role={i.severity === 'error' ? 'alert' : undefined} data-testid="field-issue"><span class="issue-code">{i.code}</span>{i.message}{#if fix}<span class="fix">{@render fix(i)}</span>{/if}</div>
{/each}

<style>
  .fix {
    margin-left: 8px;
    display: inline-flex;
    vertical-align: middle;
  }
</style>
