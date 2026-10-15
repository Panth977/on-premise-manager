<script lang="ts">
  import { toasts } from '../state/toast.svelte';
  import Icon from './Icon.svelte';
  import { portal } from './portal';
  const icon = { ok: 'check', bad: 'alert', info: 'info', warn: 'alert' } as const;
  let el: HTMLDivElement | undefined = $state();
  // a manual popover lives in the top layer: re-showing it puts the toasts above an open modal / drawer
  let lastId = 0;
  $effect(() => {
    const top = toasts.list.at(-1)?.id ?? 0;
    if (!el || typeof el.showPopover !== 'function') return;
    if (toasts.list.length === 0) {
      if (el.matches(':popover-open')) el.hidePopover();
      return;
    }
    if (top !== lastId || !el.matches(':popover-open')) {
      lastId = top;
      if (el.matches(':popover-open')) el.hidePopover();
      el.showPopover();
    }
  });
</script>

<div use:portal class="toasts" role="region" aria-label="Notifications" popover="manual" bind:this={el}>
  {#each toasts.list as t (t.id)}
    <div class="toast {t.kind}" role={t.kind === 'bad' ? 'alert' : 'status'} data-testid="toast">
      <span class="ic"><Icon name={icon[t.kind]} /></span>
      <div class="grow">
        <div class="t">{t.title}</div>
        {#if t.detail}<div class="d">{t.detail}</div>{/if}
      </div>
      <button class="btn ghost xs icon" aria-label="Dismiss" onclick={() => toasts.dismiss(t.id)}><Icon name="x" size={13} /></button>
    </div>
  {/each}
</div>

<style>
  .toasts {
    position: fixed;
    inset: auto 16px 16px auto;
    margin: 0;
    padding: 0;
    border: 0;
    background: transparent;
    overflow: visible;
    right: 16px;
    bottom: 16px;
    z-index: var(--z-toast);
    display: flex;
    flex-direction: column;
    gap: 8px;
    width: min(400px, calc(100vw - 32px));
    pointer-events: none;
  }
  .toast {
    pointer-events: auto;
    display: flex;
    gap: 10px;
    align-items: flex-start;
    background: var(--surface);
    border: 1px solid var(--border);
    border-left: 3px solid var(--blue);
    border-radius: var(--r-lg);
    padding: 10px 10px 10px 12px;
    box-shadow: var(--shadow-lg);
    animation: in 0.18s ease-out;
    font-size: var(--fs-md);
  }
  .toast.ok {
    border-left-color: var(--green);
  }
  .toast.bad {
    border-left-color: var(--red);
  }
  .toast.warn {
    border-left-color: var(--amber);
  }
  .ic {
    margin-top: 1px;
  }
  .ok .ic {
    color: var(--green);
  }
  .bad .ic {
    color: var(--red);
  }
  .info .ic {
    color: var(--blue);
  }
  .warn .ic {
    color: var(--amber);
  }
  .t {
    font-weight: var(--fw-medium);
  }
  .d {
    font-size: var(--fs-sm);
  }
  .d {
    color: var(--text-2);
    margin-top: 2px;
    overflow-wrap: anywhere;
  }
  @keyframes in {
    from {
      transform: translateY(8px);
      opacity: 0;
    }
  }
</style>
