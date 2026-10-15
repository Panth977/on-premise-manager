<script lang="ts">
  import type { Snippet } from 'svelte';
  import Icon from './Icon.svelte';
  import { Inbox, type IconNode } from './icons';
  let { icon = Inbox, title, text, action, compact = false }: { icon?: IconNode; title: string; text?: string; action?: Snippet; compact?: boolean } = $props();
</script>

<div class="empty" class:compact data-testid="empty-state">
  <div class="ic"><Icon {icon} size={compact ? 18 : 22} /></div>
  <h4>{title}</h4>
  {#if text}<p>{text}</p>{/if}
  {#if action}<div class="act">{@render action()}</div>{/if}
</div>

<style>
  .empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    text-align: center;
    padding: 48px 20px;
    border: 1px dashed var(--border-strong);
    border-radius: var(--r-lg);
    background: var(--surface);
    gap: 6px;
  }
  .compact {
    padding: 24px 16px;
  }
  .ic {
    width: 44px;
    height: 44px;
    border-radius: 12px;
    display: grid;
    place-items: center;
    background: var(--accent-soft);
    color: var(--accent-text);
    margin-bottom: 6px;
  }
  .compact .ic {
    width: 34px;
    height: 34px;
  }
  p {
    color: var(--text-2);
    max-width: 440px;
  }
  .act {
    margin-top: 10px;
  }
</style>
