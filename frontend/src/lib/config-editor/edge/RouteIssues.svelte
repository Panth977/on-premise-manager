<script lang="ts">
  import type { ValidationIssue } from '@opm/shared';
  import { tick } from 'svelte';
  import Icon from '../../ui/Icon.svelte';
  import { TriangleAlert, CircleAlert, X } from '../../ui/icons';
  import { portal, floating } from '../../ui/portal';

  /**
   * Compact issue badge of a routes-table row ("1 warning" / "2 errors") + popover with the full messages and the
   * one-click fix of W_RESPONSE_HEADER_AS_REQUEST ("Move to response headers"). Keeps long messages out of the table.
   */
  type Props = {
    issues: ValidationIssue[];
    routeId: string;
    /** header names that can be moved (W_RESPONSE_HEADER_AS_REQUEST), null when read only */
    onmove?: ((header: string) => void) | null;
    editHref: string;
  };
  let { issues, routeId, onmove = null, editHref }: Props = $props();
  const shown = $derived(issues.filter((i) => i.severity !== 'info'));
  const errors = $derived(shown.filter((i) => i.severity === 'error').length);
  const warnings = $derived(shown.length - errors);
  const label = $derived(errors ? `${errors} error${errors === 1 ? '' : 's'}${warnings ? ` · ${warnings}` : ''}` : `${warnings} warning${warnings === 1 ? '' : 's'}`);
  let open = $state(false);
  let anchor = $state<HTMLElement | null>(null);
  let pop = $state<HTMLElement | null>(null);

  /** header name of a W_RESPONSE_HEADER_AS_REQUEST issue (path …action.setHeaders.<Name>) */
  function headerOf(i: ValidationIssue): string | null {
    if (i.code !== 'W_RESPONSE_HEADER_AS_REQUEST') return null;
    const m = /\.action\.setHeaders\.(.+)$/.exec(i.path);
    return m ? m[1]! : null;
  }
  async function toggle(e: Event) {
    e.stopPropagation();
    open = !open;
    if (open) {
      await tick();
      pop?.querySelector<HTMLElement>('button, a')?.focus();
    }
  }
  function onkey(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      open = false;
      anchor?.focus();
    }
  }
  function onwin(e: MouseEvent) {
    const t = e.target as Node | null;
    if (open && t && !anchor?.contains(t) && !pop?.contains(t)) open = false;
  }
</script>

<svelte:window onclick={onwin} />

{#if shown.length}
  <button
    type="button"
    class="badge {errors ? 'red' : 'amber'} trig"
    aria-haspopup="dialog"
    aria-expanded={open}
    bind:this={anchor}
    onclick={toggle}
    data-testid="route-issues-{routeId}"
  >
    <Icon icon={errors ? CircleAlert : TriangleAlert} size={11} />{label}
  </button>
  {#if open}
    <div class="pop" role="dialog" aria-label="Issues of route {routeId}" tabindex="-1" bind:this={pop} onkeydown={onkey} use:portal use:floating={{ anchor, placement: 'bottom-end', minWidth: 300 }} data-testid="route-issues-pop-{routeId}">
      <div class="head">
        <strong class="small">Route <span class="mono">{routeId}</span></strong>
        <span class="spacer"></span>
        <button type="button" class="btn ghost icon sm" aria-label="Close" onclick={() => (open = false)}><Icon icon={X} size={13} /></button>
      </div>
      <ul>
        {#each shown as i, k (k)}
          {@const h = headerOf(i)}
          <li class={i.severity}>
            <div class="msg small"><Icon icon={i.severity === 'error' ? CircleAlert : TriangleAlert} size={13} /><span>{i.message}</span></div>
            {#if h && onmove}
              <button type="button" class="btn primary sm" onclick={() => ((open = false), onmove!(h))} data-testid="route-move-{routeId}-{h}">Move to response headers</button>
            {/if}
          </li>
        {/each}
      </ul>
      <div class="foot"><a class="small" href={editHref}>Open the route →</a></div>
    </div>
  {/if}
{/if}

<style>
  .trig {
    border: 0;
    cursor: pointer;
    font: inherit;
    font-size: var(--fs-xs, 11px);
    white-space: nowrap;
  }
  .trig:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 1px;
  }
  .pop {
    width: min(440px, calc(100vw - 16px));
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--r-lg);
    box-shadow: var(--shadow-lg);
    padding: 8px 12px 10px;
    overflow: auto;
  }
  .head {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .spacer {
    flex: 1;
  }
  ul {
    list-style: none;
    margin: 6px 0 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  li {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 6px;
  }
  .msg {
    display: flex;
    gap: 6px;
    align-items: flex-start;
    overflow-wrap: anywhere;
  }
  .msg :global(svg) {
    flex: none;
    margin-top: 2px;
  }
  li.warning .msg :global(svg) {
    color: var(--amber-text);
  }
  li.error .msg :global(svg) {
    color: var(--red-text);
  }
  .foot {
    margin-top: 8px;
    padding-top: 6px;
    border-top: 1px solid var(--border);
  }
</style>
