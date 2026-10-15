<script lang="ts">
  /** Empty state (cloud ui/EmptyState look): icon tile, title, text, optional detail block, actions. */
  import type { Snippet } from 'svelte';
  import Icon from './Icon.svelte';
  let { icon = 'inbox', title, text, detail, children, compact = false }: { icon?: string; title: string; text?: string; detail?: Snippet; children?: Snippet; compact?: boolean } = $props();
</script>

<div class="empty" class:compact data-testid="empty-state">
  <span class="ic"><Icon name={icon} size={compact ? 18 : 22} /></span>
  <h4>{title}</h4>
  {#if text}<p>{text}</p>{/if}
  {#if detail}<div class="detail">{@render detail()}</div>{/if}
  {#if children}<div class="act">{@render children()}</div>{/if}
</div>

<style>
  .empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    text-align: center;
    padding: 40px 20px;
    border: 1px dashed var(--border-strong);
    border-radius: var(--r-lg);
    background: var(--surface);
    gap: 6px;
  }
  :global(.card) > .empty,
  :global(.card) > :global(.card-body) > .empty {
    margin: var(--sp-4);
  }
  :global(.card) > :global(.card-body) > .empty {
    margin: 0;
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
  h4 {
    font-size: var(--fs-md);
  }
  p {
    color: var(--text-2);
    max-width: 480px;
    margin: 0;
  }
  .detail {
    margin-top: 8px;
    width: 100%;
    max-width: 560px;
    text-align: left;
  }
  .act {
    margin-top: 10px;
    display: flex;
    gap: var(--sp-2);
    flex-wrap: wrap;
    justify-content: center;
  }
</style>
