<script lang="ts">
  /** Native <dialog> modal: focus trap + inert page for free. ≤ 4 fields forms and every confirmation (SPEC §17.0). */
  import type { Snippet } from 'svelte';
  import Icon from './Icon.svelte';
  import { portal } from './portal';
  interface Props {
    open: boolean;
    title: string;
    description?: string;
    size?: 'sm' | 'md' | 'lg';
    /** false = blocking (no Esc, no backdrop click, no × button) */
    closable?: boolean;
    onclose?: () => void;
    children: Snippet;
    footer?: Snippet;
    testid?: string;
  }
  let { open = $bindable(false), title, description, size = 'md', closable = true, onclose, children, footer, testid }: Props = $props();
  const id = $props.id();
  let dlg: HTMLDialogElement | undefined = $state();

  $effect(() => {
    if (!dlg) return;
    if (open && !dlg.open) dlg.showModal();
    else if (!open && dlg.open) dlg.close();
  });
  function close() {
    if (!closable) return;
    open = false;
    onclose?.();
  }
</script>

<dialog
  use:portal
  bind:this={dlg}
  class="modal {size}"
  aria-labelledby="{id}-t"
  aria-describedby={description ? `${id}-d` : undefined}
  data-testid={testid}
  oncancel={(e) => {
    e.preventDefault();
    close();
  }}
  onclick={(e) => {
    if (e.target === dlg) close();
  }}
  onclose={() => {
    if (open) {
      open = false;
      onclose?.();
    }
  }}
>
  {#if open}
    <div class="inner">
      <header>
        <div class="grow">
          <h2 id="{id}-t">{title}</h2>
          {#if description}<p id="{id}-d" class="muted small">{description}</p>{/if}
        </div>
        {#if closable}
          <button class="btn ghost sm icon" type="button" aria-label="Close" onclick={close}><Icon name="x" /></button>
        {/if}
      </header>
      <div class="body">{@render children()}</div>
      {#if footer}<footer>{@render footer()}</footer>{/if}
    </div>
  {/if}
</dialog>

<style>
  .modal {
    z-index: var(--z-modal);
    padding: 0;
    border: 1px solid var(--border);
    border-radius: var(--r-xl);
    background: var(--surface);
    color: var(--text);
    box-shadow: var(--shadow-lg);
    width: min(520px, calc(100vw - 24px));
    max-height: calc(100dvh - 32px);
    overflow: visible;
  }
  .modal.sm {
    width: min(420px, calc(100vw - 24px));
  }
  .modal.lg {
    width: min(760px, calc(100vw - 24px));
  }
  .modal::backdrop {
    background: var(--overlay);
    backdrop-filter: blur(1.5px);
  }
  .modal[open] {
    animation: pop 0.14s ease-out;
  }
  @keyframes pop {
    from {
      opacity: 0;
      transform: translateY(6px) scale(0.985);
    }
  }
  .inner {
    display: flex;
    flex-direction: column;
    max-height: calc(100dvh - 34px);
  }
  header {
    display: flex;
    gap: var(--sp-3);
    align-items: flex-start;
    padding: var(--sp-4) var(--sp-5) var(--sp-3);
  }
  header :global(h2) {
    font-size: var(--fs-lg);
  }
  header p {
    margin: 4px 0 0;
  }
  .body {
    padding: var(--sp-2) var(--sp-5) var(--sp-5);
    overflow: auto;
  }
  footer {
    display: flex;
    gap: 8px;
    justify-content: flex-end;
    flex-wrap: wrap;
    padding: var(--sp-3) var(--sp-5);
    border-top: 1px solid var(--border);
    background: var(--surface-2);
    border-radius: 0 0 var(--r-xl) var(--r-xl);
  }
</style>
