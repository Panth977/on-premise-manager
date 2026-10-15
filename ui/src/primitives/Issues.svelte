<script lang="ts">
  /** Summary list of validation issues (errors first). */
  import type { Snippet } from 'svelte';
  import type { ValidationIssue } from '@opm/shared/types';
  import Icon from './Icon.svelte';
  /** fix = a one-click fix rendered next to an issue (V_DEPENDS_SUGGESTED → Add …); onpick = the issue row is clickable (jump to the field) */
  let { issues, title = 'Validation', max = 50, fix, onpick, testid = 'issues' }: { issues: readonly ValidationIssue[]; title?: string; max?: number; fix?: Snippet<[ValidationIssue]>; onpick?: (i: ValidationIssue) => void; testid?: string } = $props();
    const rank = { error: 0, warning: 1, info: 2 } as const;
  const sorted = $derived([...issues].sort((a, b) => (rank[a.severity] ?? 1) - (rank[b.severity] ?? 1)));
  const errs = $derived(issues.filter((i) => i.severity === 'error').length);
  /** info issues (e.g. V_VAR_OPTIONAL_NULL) are notes: never counted as warnings */
  const warns = $derived(issues.filter((i) => i.severity === 'warning').length);
  const infos = $derived(issues.filter((i) => i.severity === 'info').length);
</script>

{#if issues.length}
  <div class="issues" class:has-err={errs > 0} class:only-info={errs === 0 && warns === 0} data-testid={testid}>
    <div class="h"><Icon name={errs ? 'alert' : 'info'} />{title}: {[errs ? `${errs} ${errs === 1 ? 'error' : 'errors'}` : '', warns ? `${warns} ${warns === 1 ? 'warning' : 'warnings'}` : '', infos ? `${infos} ${infos === 1 ? 'note' : 'notes'}` : ''].filter(Boolean).join(', ')}</div>
    <ul>
      {#each sorted.slice(0, max) as i, k (k)}
        <li class={i.severity} data-code={i.code}>
          <span class="badge {i.severity === 'error' ? 'bad' : i.severity === 'info' ? 'info' : 'warn'}">{i.code}</span>
          {#if onpick}<button type="button" class="grow pick" onclick={() => onpick(i)}>{i.message}{#if i.path}<code class="p">{i.path}</code>{/if}</button>{:else}<span class="grow">{i.message}{#if i.path}<code class="p">{i.path}</code>{/if}</span>{/if}
          {#if fix}{@render fix(i)}{/if}
        </li>
      {/each}
    </ul>
    {#if sorted.length > max}<p class="muted small">… and {sorted.length - max} more</p>{/if}
  </div>
{/if}

<style>
  .issues {
    border: 1px solid color-mix(in srgb, var(--amber) 35%, var(--border));
    background: color-mix(in srgb, var(--amber-soft) 60%, var(--surface));
    border-radius: var(--r-md);
    padding: 10px 12px;
    font-size: 13px;
  }
  .issues.only-info {
    border-color: color-mix(in srgb, var(--blue) 30%, var(--border));
    background: color-mix(in srgb, var(--blue-soft) 60%, var(--surface));
  }
  .issues.has-err {
    border-color: color-mix(in srgb, var(--red) 35%, var(--border));
    background: color-mix(in srgb, var(--red-soft) 55%, var(--surface));
  }
  .h {
    display: flex;
    gap: 8px;
    align-items: center;
    font-weight: var(--fw-semibold);
    margin-bottom: 6px;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 5px;
  }
  li {
    display: flex;
    gap: 8px;
    align-items: baseline;
  }
  .pick {
    border: 0;
    background: none;
    padding: 0;
    font: inherit;
    color: inherit;
    text-align: left;
    cursor: pointer;
  }
  .pick:hover {
    text-decoration: underline;
  }
  .p {
    margin-left: 6px;
    font-size: 11px;
    color: var(--text-2);
    background: none;
    padding: 0;
    overflow-wrap: anywhere;
  }
</style>
