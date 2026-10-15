<script lang="ts">
  import type { Snippet } from 'svelte';
  import { fade, scale } from 'svelte/transition';
  import { onDestroy, tick } from 'svelte';
  import Icon from './Icon.svelte';
  import { X } from './icons';
  import { pushOverlay, popOverlay, isTop, trapTab, focusFirst } from './overlay';
  import { portal } from './portal';

  type Props = {
    open: boolean;
    title: string;
    subtitle?: string;
    size?: 'sm' | 'md' | 'lg' | 'xl';
    /** prevents closing (Esc, backdrop, X) while a request runs */
    busy?: boolean;
    /** called after the modal asked to close (Esc, backdrop, X) */
    onclose?: () => void;
    children: Snippet;
    footer?: Snippet;
    testid?: string;
  };
  let { open = $bindable(), title, subtitle, size = 'md', busy = false, onclose, children, footer, testid }: Props = $props();

  let panel = $state<HTMLElement | null>(null);
  let entry: { id: number; z: number } | null = $state(null);
  let returnFocus: HTMLElement | null = null;

  $effect(() => {
    if (open && !entry) {
      returnFocus = document.activeElement as HTMLElement | null;
      entry = pushOverlay();
      tick().then(() => panel && focusFirst(panel));
    } else if (!open && entry) {
      release();
    }
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
  const titleId = `m-${Math.random().toString(36).slice(2, 9)}`;
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && entry && isTop(entry.id) && !panel?.contains(e.target as Node) && (e.preventDefault(), requestClose())} />

{#if open && entry}
  <div class="backdrop" use:portal style="z-index:{entry.z}" transition:fade={{ duration: 120 }} onmousedown={(e) => e.target === e.currentTarget && requestClose()} role="presentation">
    <div
      bind:this={panel}
      class="modal {size}"
      role="dialog"
      aria-modal="true"
      aria-labelledby={titleId}
      tabindex="-1"
      {onkeydown}
      data-testid={testid}
      transition:scale={{ duration: 140, start: 0.97 }}
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
    align-items: flex-start;
    justify-content: center;
    padding: 8vh 16px 16px;
    overflow-y: auto;
    backdrop-filter: blur(1.5px);
  }
  .modal {
    width: 100%;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--r-xl);
    box-shadow: var(--shadow-lg);
    display: flex;
    flex-direction: column;
    max-height: calc(100vh - 8vh - 24px);
    outline: none;
  }
  .sm {
    max-width: 420px;
  }
  .md {
    max-width: 520px;
  }
  .lg {
    max-width: 760px;
  }
  .xl {
    max-width: 1040px;
  }
  .head {
    display: flex;
    align-items: flex-start;
    gap: var(--sp-3);
    padding: var(--sp-4) var(--sp-5) var(--sp-3);
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
    padding: var(--sp-2) var(--sp-5) var(--sp-5);
    overflow-y: auto;
    min-height: 0;
  }
  .foot {
    display: flex;
    justify-content: flex-end;
    gap: var(--sp-2);
    padding: var(--sp-3) var(--sp-5);
    border-top: 1px solid var(--border);
    background: var(--surface-2);
    border-radius: 0 0 var(--r-xl) var(--r-xl);
    flex-wrap: wrap;
  }
  @media (max-width: 560px) {
    .backdrop {
      padding: 12px 8px;
      align-items: flex-end;
    }
    .modal {
      max-height: calc(100vh - 24px);
    }
  }
</style>
