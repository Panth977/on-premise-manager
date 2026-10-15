<script lang="ts">
  import Icon from './Icon.svelte';
  import Button from './Button.svelte';
  import { CircleAlert, RefreshCw } from './icons';
  let { message, onretry, title = 'Could not load' }: { message: string; onretry?: () => void; title?: string } = $props();
</script>

<div class="err" role="alert" data-testid="error-state">
  <Icon icon={CircleAlert} size={20} class="ei" />
  <div class="grow">
    <div class="strong">{title}</div>
    <div class="muted small break">{message}</div>
  </div>
  {#if onretry}<Button icon={RefreshCw} onclick={onretry} data-testid="retry">Retry</Button>{/if}
</div>

<style>
  .err {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 14px 16px;
    border: 1px solid color-mix(in srgb, var(--red) 30%, var(--border));
    background: var(--red-soft);
    border-radius: var(--r-lg);
  }
  .err :global(.ei) {
    color: var(--red);
  }
</style>
