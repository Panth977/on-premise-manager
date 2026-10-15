<script lang="ts">
  import DynIcon from './DynIcon.svelte';
  /** A coloured tag chip; pass the tag (from the catalog) or just the slug. */
  let { slug, tag }: { slug: string; tag?: { label: string; icon: string | null; color: string } | null } = $props();
  const c = $derived(tag?.color ?? '#71717a');
</script>

<span class="tc" style="--c:{c}" title={tag?.label ?? slug}>{#if tag}<DynIcon name={tag.icon} type="tag" size={11} />{/if}{slug}</span>

<style>
  .tc {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    height: 20px;
    padding: 0 7px;
    border-radius: 999px;
    font-size: 11px;
    font-weight: 600;
    color: var(--c);
    background: color-mix(in srgb, var(--c) 13%, transparent);
    border: 1px solid color-mix(in srgb, var(--c) 28%, transparent);
    white-space: nowrap;
  }
</style>
