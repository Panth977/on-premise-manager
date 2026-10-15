<script lang="ts">
  import type { Snippet } from 'svelte';
  import { fade, fly } from 'svelte/transition';
  import { onDestroy, tick } from 'svelte';
  import Icon from './Icon.svelte';
  import { X } from './icons';
  import { pushOverlay, popOverlay, isTop, trapTab, focusFirst } from './overlay';
  import { portal } from './portal';

  type Props = {
    open: boolean;
    title: string;
    subtitle?: string;
    /** md = 480 px (forms), wide = 720 px (code editors) */
    width?: 'md' | 'wide';
    busy?: boolean;
    onclose?: () => void;
    children: Snippet;
    footer?: Snippet;
    testid?: string;
  };
  let { open = $bindable(), title, subtitle, width = 'md', busy = false, onclose, children, footer, testid }: Props = $props();

  let panel = $state<HTMLElement | null>(null);
  let entry: { id: number; z: number } | null = $state(null);
  let returnFocus: HTMLElement | null = null;

  $effect(() => {
    if (open && !entry) {
      returnFocus = document.activeElement as HTMLElement | null;
      entry = pushOverlay();
      tick().then(() => panel && focusFirst(panel));
    } else if (!open && entry) release();
  });
  function release() {
    if (!entry) return;
    popOverlay(entry.id);
    entry = null;
    const r = returnFocus;
    returnFocus = null;
    if (r && document.contains(r)) setTimeout(() => r.focus(), 0);
  }
  onDestroy(release);
  function requestClose() {
    if (busy) return;
    open = false;
    onclose?.();
  }
  function onkeydown(e: KeyboardEvent) {
    if (!entry || !isTop(entry.id)) return;
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      requestClose();
    } else if (panel) trapTab(e, panel);
  }
  const titleId = `d-${Math.random().toString(36).slice(2, 9)}`;
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && entry && isTop(entry.id) && !panel?.contains(e.target as Node) && (e.preventDefault(), requestClose())} />

{#if open && entry}
  <div class="backdrop" use:portal style="z-index:{entry.z}" transition:fade={{ duration: 140 }} onmousedown={(e) => e.target === e.currentTarget && requestClose()} role="presentation">
    <div
      bind:this={panel}
      class="drawer {width}"
      role="dialog"
      aria-modal="true"
      aria-labelledby={titleId}
      tabindex="-1"
      {onkeydown}
      data-testid={testid}
      transition:fly={{ x: 40, duration: 180 }}
    >
      <header class="head">
        <div class="grow">
          <h2 id={titleId}>{title}</h2>
          {#if subtitle}<p class="sub">{subtitle}</p>{/if}
        </div>
        <button type="button" class="btn ghost icon sm overlay-close" aria-label="Close" onclick={requestClose} disabled={busy}><Icon icon={X} size={16} /></button>
      </header>
      <div class="body">{@render children()}</div>
      {#if footer}<footer class="foot">{@render footer()}</footer>{/if}
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: var(--overlay);
    display: flex;
    justify-content: flex-end;
  }
  .drawer {
    height: 100%;
    width: var(--drawer-w);
    max-width: 100vw;
    background: var(--surface);
    border-left: 1px solid var(--border);
    box-shadow: var(--shadow-lg);
    display: flex;
    flex-direction: column;
    outline: none;
  }
  .wide {
    width: var(--drawer-w-wide);
  }
  .head {
    display: flex;
    align-items: flex-start;
    gap: var(--sp-3);
    padding: var(--sp-4) var(--sp-5);
    border-bottom: 1px solid var(--border);
  }
  .head h2 {
    font-size: var(--fs-lg);
  }
  .sub {
    color: var(--text-2);
    font-size: var(--fs-sm);
    margin-top: 2px;
  }
  .body {
    flex: 1;
    overflow-y: auto;
    padding: var(--sp-5);
    min-height: 0;
  }
  .foot {
    display: flex;
    justify-content: flex-end;
    gap: var(--sp-2);
    padding: var(--sp-3) var(--sp-5);
    border-top: 1px solid var(--border);
    background: var(--surface-2);
    flex-wrap: wrap;
  }
</style>
