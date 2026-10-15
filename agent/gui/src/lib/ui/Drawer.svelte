<script lang="ts">
  /** Right side drawer (5–12 field forms; wide = code editors) — SPEC §17.0 container rule. */
  import type { Snippet } from 'svelte';
  import Icon from './Icon.svelte';
  import { portal } from './portal';
  interface Props {
    open: boolean;
    title: string;
    subtitle?: string;
    wide?: boolean;
    onclose?: () => void;
    children: Snippet;
    footer?: Snippet;
    testid?: string;
  }
  let { open = $bindable(false), title, subtitle, wide = false, onclose, children, footer, testid }: Props = $props();
  const id = $props.id();
  let dlg: HTMLDialogElement | undefined = $state();
  $effect(() => {
    if (!dlg) return;
    if (open && !dlg.open) dlg.showModal();
    else if (!open && dlg.open) dlg.close();
  });
  function close() {
    open = false;
    onclose?.();
  }
</script>

<dialog
  use:portal
  bind:this={dlg}
  class="drawer"
  class:wide
  aria-labelledby="{id}-t"
  data-testid={testid}
  oncancel={(e) => {
    e.preventDefault();
    close();
  }}
  onclick={(e) => {
    if (e.target === dlg) close();
  }}
  onclose={() => {
    if (open) close();
  }}
>
  {#if open}
    <div class="inner">
      <header>
        <div class="grow">
          <h2 id="{id}-t">{title}</h2>
          {#if subtitle}<p class="muted small">{subtitle}</p>{/if}
        </div>
        <button class="btn ghost sm icon" type="button" aria-label="Close" onclick={close}><Icon name="x" /></button>
      </header>
      <div class="body">{@render children()}</div>
      {#if footer}<footer>{@render footer()}</footer>{/if}
    </div>
  {/if}
</dialog>

<style>
  .drawer {
    z-index: var(--z-drawer);
    margin: 0 0 0 auto;
    padding: 0;
    border: 0;
    border-left: 1px solid var(--border);
    height: 100dvh;
    max-height: 100dvh;
    width: min(var(--drawer-w), 100vw);
    max-width: 100vw;
    background: var(--surface);
    color: var(--text);
    box-shadow: var(--shadow-lg);
  }
  .drawer.wide {
    width: min(var(--drawer-w-wide), 100vw);
  }
  .drawer::backdrop {
    background: var(--overlay);
  }
  .drawer[open] {
    animation: slide 0.18s ease-out;
  }
  @keyframes slide {
    from {
      transform: translateX(24px);
      opacity: 0.4;
    }
  }
  .inner {
    display: flex;
    flex-direction: column;
    height: 100%;
  }
  header {
    display: flex;
    gap: 12px;
    align-items: flex-start;
    padding: var(--sp-4) var(--sp-5) var(--sp-3);
    border-bottom: 1px solid var(--border);
  }
  header p {
    margin: 3px 0 0;
  }
  header h2 {
    font-size: var(--fs-lg);
  }
  .body {
    padding: var(--sp-4) var(--sp-5);
    overflow: auto;
    flex: 1;
  }
  footer {
    display: flex;
    gap: 8px;
    justify-content: flex-end;
    flex-wrap: wrap;
    padding: var(--sp-3) var(--sp-5);
    border-top: 1px solid var(--border);
    background: var(--surface-2);
  }
</style>
