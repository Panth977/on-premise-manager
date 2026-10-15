<script module lang="ts">
  export type IssueAction = { label: string; run: () => void | Promise<void>; testid?: string; disabled?: boolean; tip?: string };
</script>

<script lang="ts">
  import type { ValidationIssue } from '@opm/shared';
  import Icon from './Icon.svelte';
  import InlineCode from './InlineCode.svelte';
  import { CircleAlert, TriangleAlert, ChevronDown, ChevronRight, Info } from './icons';
  type Props = {
    issues: ValidationIssue[];
    title?: string;
    /** path → link (e.g. into the site editor); null = no link */
    linkOf?: (i: ValidationIssue) => string | null;
    onpick?: (i: ValidationIssue) => void;
    collapsible?: boolean;
    max?: number;
    emptyText?: string | null;
    testid?: string;
    /** a one-click fix shown at the end of the row (e.g. "Add" for V_DEPENDS_SUGGESTED); null = none */
    actionOf?: (i: ValidationIssue) => IssueAction | null;
  };
  let { issues, title, linkOf, onpick, collapsible = false, max = 50, emptyText = null, testid = 'issues', actionOf }: Props = $props();
  let busy = $state<number | null>(null);
  async function runAction(k: number, a: IssueAction) {
    busy = k;
    try {
      await a.run();
    } finally {
      busy = null;
    }
  }
  let open = $state(true);
  let showAll = $state(false);
  const errors = $derived(issues.filter((i) => i.severity === 'error'));
  const warnings = $derived(issues.filter((i) => i.severity === 'warning'));
  const infos = $derived(issues.filter((i) => i.severity === 'info'));
  const sorted = $derived([...errors, ...warnings, ...infos]);
  const shown = $derived(showAll ? sorted : sorted.slice(0, max));
</script>

{#if issues.length || emptyText}
  <div class="iss" class:has-err={errors.length > 0} data-testid={testid}>
    {#if title || collapsible}
      <button type="button" class="hd" onclick={() => collapsible && (open = !open)} disabled={!collapsible} aria-expanded={open}>
        {#if collapsible}<Icon icon={open ? ChevronDown : ChevronRight} size={14} />{/if}
        <span class="strong">{title ?? 'Validation'}</span>
        {#if errors.length}<span class="badge red">{errors.length} {errors.length === 1 ? 'error' : 'errors'}</span>{/if}
        {#if warnings.length}<span class="badge amber">{warnings.length} {warnings.length === 1 ? 'warning' : 'warnings'}</span>{/if}
        {#if infos.length}<span class="badge" data-testid="{testid}-info-count">{infos.length} info</span>{/if}
        {#if !errors.length && !warnings.length && emptyText}<span class="badge green">ok</span>{/if}
      </button>
    {/if}
    {#if open}
      {#if !issues.length && emptyText}
        <p class="muted small ok">{emptyText}</p>
      {:else}
        <ul>
          {#each shown as i, k (k)}
            {@const href = linkOf?.(i) ?? null}
            {@const act = actionOf?.(i) ?? null}
            <li class={i.severity} data-code={i.code}>
              <Icon icon={i.severity === 'error' ? CircleAlert : i.severity === 'warning' ? TriangleAlert : Info} size={14} class="ii" />
              <div class="grow">
                <div class="m"><InlineCode text={i.message} /></div>
                <div class="p">
                  <code>{i.code}</code>
                  {#if i.path}
                    {#if href}<a {href} onclick={() => onpick?.(i)}>{i.path}</a>{:else if onpick}<button type="button" class="lnk" onclick={() => onpick?.(i)}>{i.path}</button>{:else}<span>{i.path}</span>{/if}
                  {/if}
                </div>
              </div>
              {#if act}<button type="button" class="btn sm act" onclick={() => runAction(k, act)} disabled={act.disabled || busy !== null} data-tip={act.tip} data-testid={act.testid}>{act.label}</button>{/if}
            </li>
          {/each}
        </ul>
        {#if sorted.length > max && !showAll}<button type="button" class="btn ghost sm" onclick={() => (showAll = true)}>Show all {sorted.length}</button>{/if}
      {/if}
    {/if}
  </div>
{/if}

<style>
  .iss {
    border: 1px solid var(--border);
    border-radius: var(--r-lg);
    background: var(--surface);
    padding: 10px 12px;
  }
  .iss.has-err {
    border-color: color-mix(in srgb, var(--red) 35%, var(--border));
  }
  .hd {
    display: flex;
    align-items: center;
    gap: 8px;
    border: 0;
    background: transparent;
    padding: 0;
    cursor: pointer;
    width: 100%;
    text-align: left;
    color: var(--text);
  }
  .hd:disabled {
    cursor: default;
  }
  ul {
    list-style: none;
    margin: 8px 0 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
    max-height: 340px;
    overflow-y: auto;
  }
  li {
    display: flex;
    gap: 8px;
    align-items: flex-start;
    font-size: var(--fs-md);
  }
  li.error :global(.ii) {
    color: var(--red);
    margin-top: 2px;
  }
  li.warning :global(.ii) {
    color: var(--amber);
    margin-top: 2px;
  }
  li.info :global(.ii) {
    color: var(--text-3);
    margin-top: 2px;
  }
  li.info .m {
    color: var(--text-2);
  }
  .p {
    font-size: var(--fs-xs);
    color: var(--text-3);
    display: flex;
    gap: 8px;
    flex-wrap: wrap;
    font-family: var(--font-mono);
    overflow-wrap: anywhere;
  }
  .lnk {
    border: 0;
    background: transparent;
    padding: 0;
    color: var(--accent-text);
    cursor: pointer;
    font: inherit;
  }
  .ok {
    margin-top: 6px;
  }
  .act {
    flex: none;
    align-self: center;
  }
</style>
