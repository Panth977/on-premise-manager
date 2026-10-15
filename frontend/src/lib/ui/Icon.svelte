<script lang="ts">
  import type { IconNode } from './icons';
  let { icon, size = 16, stroke = 2, class: cls = '', label = '' }: { icon: IconNode; size?: number; stroke?: number; class?: string; label?: string } = $props();
</script>

<svg
  xmlns="http://www.w3.org/2000/svg"
  width={size}
  height={size}
  viewBox="0 0 24 24"
  fill="none"
  stroke="currentColor"
  stroke-width={stroke}
  stroke-linecap="round"
  stroke-linejoin="round"
  class="ic {cls}"
  role={label ? 'img' : undefined}
  aria-label={label || undefined}
  aria-hidden={label ? undefined : 'true'}
>
  {#each icon as [tag, attrs], i (i)}
    <svelte:element this={tag} {...attrs} />
  {/each}
</svg>

<style>
  .ic {
    flex: none;
    display: inline-block;
    vertical-align: middle;
  }
</style>
