<script lang="ts">
  /** A flat list of sidebar links (optional group headers). Styles: app.css `.nav-item` / `.nav-cnt` / `.nav-grp`. */
  import { page } from '$app/state';
  import Icon from '../ui/Icon.svelte';
  import type { NavItem } from './Shell.svelte';
  let { items }: { items: NavItem[] } = $props();
  function isActive(n: NavItem) {
    const p = page.url.pathname;
    return n.exact ? p === n.href : p === n.href || p.startsWith(n.href + '/');
  }
</script>

{#each items as n, i (n.href)}
  {#if n.group && n.group !== items[i - 1]?.group}<div class="nav-grp">{n.group}</div>{/if}
  <a href={n.href} class="nav-item" class:on={isActive(n)} aria-current={isActive(n) ? 'page' : undefined} data-testid={n.testid ?? `nav-${n.label.toLowerCase().replace(/\s+/g, '-')}`}>
    <Icon icon={n.icon} size={16} />
    <span class="grow ellipsis">{n.label}</span>
    {#if n.count !== undefined && n.count !== null}<span class="nav-cnt {n.tone ?? ''}">{n.count}</span>{/if}
  </a>
{/each}
