<script lang="ts">
  import { fly } from 'svelte/transition';
  import { toast } from '../toast.svelte';
  import Icon from './Icon.svelte';
  import { portal } from './portal';
  import { CheckCircle, CircleAlert, Info, TriangleAlert, X } from './icons';
  const icons = { success: CheckCircle, error: CircleAlert, info: Info, warning: TriangleAlert };
</script>

<div class="toaster" use:portal aria-live="polite" aria-relevant="additions">
  {#each toast.items as t (t.id)}
    <div class="toast {t.kind}" role={t.kind === 'error' ? 'alert' : 'status'} in:fly={{ y: 12, duration: 160 }} out:fly={{ x: 40, duration: 140 }} data-testid="toast-{t.kind}">
      <Icon icon={icons[t.kind]} size={17} class="ti" />
      <div class="grow">
        <div class="tt">{t.title}</div>
        {#if t.detail}<div class="td">{t.detail}</div>{/if}
      </div>
      <button class="btn ghost icon sm" aria-label="Dismiss" onclick={() => toast.dismiss(t.id)}><Icon icon={X} size={14} /></button>
    </div>
  {/each}
</div>

<style>
  .toaster {
    position: fixed;
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
    padding: 10px 10px 10px 12px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-left: 3px solid var(--text-3);
    border-radius: var(--r-lg);
    box-shadow: var(--shadow-lg);
    font-size: var(--fs-md);
  }
  .toast.success {
    border-left-color: var(--green);
  }
  .toast.success :global(.ti) {
    color: var(--green);
  }
  .toast.error {
    border-left-color: var(--red);
  }
  .toast.error :global(.ti) {
    color: var(--red);
  }
  .toast.warning {
    border-left-color: var(--amber);
  }
  .toast.warning :global(.ti) {
    color: var(--amber);
  }
  .toast.info {
    border-left-color: var(--blue);
  }
  .toast.info :global(.ti) {
    color: var(--blue);
  }
  :global(.ti) {
    margin-top: 1px;
  }
  .tt {
    font-weight: var(--fw-medium);
  }
  .td {
    color: var(--text-2);
    font-size: var(--fs-sm);
    margin-top: 2px;
    overflow-wrap: anywhere;
  }
</style>
