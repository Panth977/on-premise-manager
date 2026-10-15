<script lang="ts">
  import type { ValidationIssue } from '@opm/shared';
  import Icon from './Icon.svelte';
  import { CircleAlert } from './icons';
  /** The summary list at the top of a form (API `invalid` issues and client errors). */
  let { issues, title = 'Fix these problems' }: { issues: ValidationIssue[]; title?: string } = $props();
  const errs = $derived(issues.filter((i) => i.severity === 'error'));
</script>

{#if errs.length}
  <div class="banner error fs" role="alert" data-testid="form-summary">
    <Icon icon={CircleAlert} size={16} class="icon" />
    <div class="grow">
      <div class="strong">{title}</div>
      <ul>
        {#each errs.slice(0, 8) as e, i (i)}<li><span class="mono xs faint">{e.path || '·'}</span> {e.message}</li>{/each}
        {#if errs.length > 8}<li class="faint">+{errs.length - 8} more</li>{/if}
      </ul>
    </div>
  </div>
{/if}

<style>
  .fs ul {
    margin: 4px 0 0;
    padding-left: 16px;
    font-size: var(--fs-sm);
  }
  .fs :global(.icon) {
    color: var(--red);
  }
</style>
