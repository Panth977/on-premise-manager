<script lang="ts">
  import type { ValidationIssue } from '@opm/shared/types';
  import IssueFix from './IssueFix.svelte';
  /** fix = one-click fixes on the working copy (V_DEPENDS_SUGGESTED → Add) */
  let { issues, fix = false }: { issues: ValidationIssue[]; fix?: boolean } = $props();
</script>

{#each issues as i, k (k)}
  <div class={i.severity === 'error' ? 'field-err' : i.severity === 'info' ? 'field-info' : 'field-warn'} role={i.severity === 'error' ? 'alert' : undefined} data-testid="field-issue"><span class="issue-code">{i.code}</span>{i.message}{#if fix}<span class="fix"><IssueFix issue={i} /></span>{/if}</div>
{/each}

<style>
  .fix {
    margin-left: 8px;
    display: inline-flex;
    vertical-align: middle;
  }
</style>
