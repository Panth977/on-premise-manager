<script lang="ts" module>
  import type { IconNode } from './icons';
  export interface MenuItem {
    label: string;
    icon?: IconNode;
    onclick?: () => void;
    href?: string;
    danger?: boolean;
    disabled?: boolean;
    tip?: string;
    divider?: boolean;
    testid?: string;
  }
</script>

<script lang="ts">
  import type { Snippet } from 'svelte';
  import { tick } from 'svelte';
  import Icon from './Icon.svelte';
  import { MoreHorizontal } from './icons';
  import { portal, floating } from './portal';
  type Props = { items: MenuItem[]; label?: string; align?: 'left' | 'right'; trigger?: Snippet; testid?: string; size?: 'sm' | 'md' };
  let { items, label = 'More actions', align = 'right', trigger, testid, size = 'sm' }: Props = $props();
  let open = $state(false);
  let root = $state<HTMLElement | null>(null);
  let list = $state<HTMLElement | null>(null);
  const visible = $derived(items.filter((i) => !i.divider || true));
  async function toggle(e: Event) {
    e.stopPropagation();
    open = !open;
    if (open) {
      await tick();
      list?.querySelector<HTMLElement>('[role="menuitem"]:not([aria-disabled="true"])')?.focus();
    }
  }
  function onkey(e: KeyboardEvent) {
    const els = Array.from(list?.querySelectorAll<HTMLElement>('[role="menuitem"]:not([aria-disabled="true"])') ?? []);
    const i = els.indexOf(document.activeElement as HTMLElement);
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      els[(i + 1) % els.length]?.focus();
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      els[(i - 1 + els.length) % els.length]?.focus();
    } else if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      open = false;
      root?.querySelector<HTMLElement>('.trig')?.focus();
    } else if (e.key === 'Tab') open = false;
  }
  function pick(it: MenuItem, e: Event) {
    e.stopPropagation();
    if (it.disabled) return;
    open = false;
    it.onclick?.();
  }
  function onfocusout(e: FocusEvent) {
    const t = e.relatedTarget as Node | null;
    if (root && !root.contains(t) && !list?.contains(t)) open = false;
  }
</script>

<svelte:window onclick={() => (open = false)} />

<div class="menu" bind:this={root} {onfocusout} data-testid={testid}>
  <button type="button" class="trig btn ghost {size === 'sm' ? 'sm' : ''} {trigger ? '' : 'icon'}" aria-haspopup="menu" aria-expanded={open} aria-label={label} onclick={toggle}>
    {#if trigger}{@render trigger()}{:else}<Icon icon={MoreHorizontal} size={16} />{/if}
  </button>
  {#if open}
    <div class="pop {align}" role="menu" tabindex="-1" bind:this={list} onkeydown={onkey} use:portal use:floating={{ anchor: root, placement: align === 'right' ? 'bottom-end' : 'bottom-start', minWidth: 190 }} onfocusout={onfocusout}>
      {#each visible as it, i (i)}
        {#if it.divider}
          <div class="sep" role="separator"></div>
        {:else if it.href && !it.disabled}
          <a role="menuitem" class="it" class:danger={it.danger} href={it.href} tabindex="-1" onclick={() => (open = false)} data-testid={it.testid}>
            {#if it.icon}<Icon icon={it.icon} size={14} />{/if}{it.label}
          </a>
        {:else}
          <button type="button" role="menuitem" class="it" class:danger={it.danger} aria-disabled={it.disabled || undefined} tabindex="-1" data-tip={it.tip} onclick={(e) => pick(it, e)} data-testid={it.testid}>
            {#if it.icon}<Icon icon={it.icon} size={14} />{/if}{it.label}
          </button>
        {/if}
      {/each}
    </div>
  {/if}
</div>

<style>
  .menu {
    position: relative;
    display: inline-block;
  }
  .pop {
    min-width: 190px;
    overflow-y: auto;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    box-shadow: var(--shadow-lg);
    padding: 4px;
    text-align: left;
  }
  .it {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 7px 9px;
    border: 0;
    background: transparent;
    border-radius: var(--r-sm);
    color: var(--text);
    font-size: var(--fs-md);
    cursor: pointer;
    text-align: left;
    white-space: nowrap;
    text-decoration: none;
  }
  .it:hover,
  .it:focus {
    background: var(--surface-hover);
    outline: none;
    text-decoration: none;
  }
  .it.danger {
    color: var(--red-text);
  }
  .it[aria-disabled='true'] {
    opacity: 0.45;
    cursor: not-allowed;
  }
  .sep {
    height: 1px;
    background: var(--border);
    margin: 4px 2px;
  }
</style>
