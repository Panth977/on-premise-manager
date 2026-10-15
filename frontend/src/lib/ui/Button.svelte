<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLButtonAttributes } from 'svelte/elements';
  import Icon from './Icon.svelte';
  import Spinner from './Spinner.svelte';
  import type { IconNode } from './icons';

  type Props = Omit<HTMLButtonAttributes, 'class'> & {
    variant?: 'default' | 'primary' | 'danger' | 'ghost' | 'danger-outline';
    size?: 'md' | 'sm';
    icon?: IconNode;
    iconRight?: IconNode;
    loading?: boolean;
    href?: string;
    tip?: string;
    block?: boolean;
    class?: string;
    children?: Snippet;
  };
  let { variant = 'default', size = 'md', icon, iconRight, loading = false, href, tip, block = false, class: cls = '', children, disabled, type = 'button', ...rest }: Props = $props();
  const onlyIcon = $derived(!children);
  const classes = $derived(['btn', variant !== 'default' && variant, size === 'sm' && 'sm', onlyIcon && 'icon', block && 'block', cls].filter(Boolean).join(' '));
  const isz = $derived(size === 'sm' ? 14 : 15);
</script>

{#if href && !disabled}
  <a {href} class={classes} data-tip={tip} aria-label={rest['aria-label'] ?? (onlyIcon ? tip : undefined)} {...(rest as Record<string, unknown>)}>
    {#if icon}<Icon {icon} size={isz} />{/if}
    {@render children?.()}
    {#if iconRight}<Icon icon={iconRight} size={isz} />{/if}
  </a>
{:else}
  <button {type} class={classes} disabled={disabled || loading} aria-busy={loading || undefined} data-tip={tip} aria-label={rest['aria-label'] ?? (onlyIcon ? tip : undefined)} {...rest}>
    {#if loading}<Spinner size={isz} />{:else if icon}<Icon {icon} size={isz} />{/if}
    {@render children?.()}
    {#if iconRight && !loading}<Icon icon={iconRight} size={isz} />{/if}
  </button>
{/if}
