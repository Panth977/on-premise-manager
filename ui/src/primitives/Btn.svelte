<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLButtonAttributes } from 'svelte/elements';
  import Icon from './Icon.svelte';
  interface Props extends HTMLButtonAttributes {
    variant?: 'default' | 'primary' | 'danger' | 'ghost';
    size?: 'md' | 'sm' | 'xs';
    busy?: boolean;
    icon?: string;
    children?: Snippet;
  }
  let { variant = 'default', size = 'md', busy = false, icon, children, disabled, type = 'button', class: cls = '', ...rest }: Props = $props();
</script>

<button {...rest} {type} class="btn {variant === 'default' ? '' : variant} {size === 'md' ? '' : size} {children ? '' : 'icon'} {cls}" disabled={disabled || busy} aria-busy={busy || undefined}>
  {#if busy}<span class="spin" aria-hidden="true"></span>{:else if icon}<Icon name={icon} size={size === 'xs' ? 13 : 15} />{/if}
  {#if children}{@render children()}{/if}
</button>
