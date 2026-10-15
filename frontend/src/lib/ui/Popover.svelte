<script lang="ts">
  /**
   * A click popover: a trigger button (snippet `label`) + a portaled panel (children) next to it. Esc / click outside
   * closes; focus goes into the panel and back to the trigger. Used for long texts kept out of table cells.
   */
  import type { Snippet } from 'svelte';
  import { tick } from 'svelte';
  import Icon from './Icon.svelte';
  import { X } from './icons';
  import { portal, floating } from './portal';

  type Props = { label: Snippet; children: Snippet; title: string; tone?: string; testid?: string; minWidth?: number };
  let { label, children, title, tone = 'outline', testid, minWidth = 300 }: Props = $props();
  let open = $state(false);
  let anchor = $state<HTMLElement | null>(null);
  let pop = $state<HTMLElement | null>(null);
  async function toggle(e: Event) {
    e.stopPropagation();
    open = !open;
    if (open) {
      await tick();
      pop?.focus();
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

<button type="button" class="badge {tone} trig" aria-haspopup="dialog" aria-expanded={open} bind:this={anchor} onclick={toggle} data-testid={testid}>{@render label()}</button>
{#if open}
  <div class="pop" role="dialog" aria-label={title} tabindex="-1" bind:this={pop} onkeydown={onkey} use:portal use:floating={{ anchor, placement: 'bottom-end', minWidth }} data-testid={testid ? `${testid}-pop` : undefined}>
    <div class="head">
      <strong class="small">{title}</strong>
      <span class="spacer"></span>
      <button type="button" class="btn ghost icon sm" aria-label="Close" onclick={() => (open = false)}><Icon icon={X} size={13} /></button>
    </div>
    <div class="body small">{@render children()}</div>
  </div>
{/if}

<style>
  .trig {
    cursor: pointer;
    border: 0;
    font: inherit;
    font-size: var(--fs-xs);
    max-width: 100%;
  }
  .pop {
    background: var(--surface);
    border: 1px solid var(--border-strong);
    border-radius: var(--r-md);
    box-shadow: var(--shadow-lg);
    max-width: min(460px, calc(100vw - 16px));
    max-height: 60vh;
    overflow: auto;
    outline: none;
  }
  .head {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 8px 8px 6px 12px;
    border-bottom: 1px solid var(--border);
  }
  .body {
    padding: 10px 12px 12px;
    overflow-wrap: anywhere;
  }
</style>
