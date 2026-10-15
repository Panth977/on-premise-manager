<script lang="ts">
  /** Round avatar: an Icon value (upload / lucide / legacy https URL) or the initials of `name` when null. */
  import IconView from './IconView.svelte';
  let { name, img = null, size = 28 }: { name: string; img?: string | null; size?: number } = $props();
  const isLucide = $derived(!!img && !img.startsWith('upload:') && !img.startsWith('https://') && !img.startsWith('data:'));
</script>

{#if isLucide}
  <span class="av" style="width:{size}px;height:{size}px"><IconView value={img} size={Math.round(size * 0.58)} /></span>
{:else}
  <IconView value={img} initials={name || '?'} {size} round />
{/if}

<style>
  .av {
    border-radius: 50%;
    flex: none;
    display: inline-grid;
    place-items: center;
    background: var(--accent-soft);
    color: var(--accent-text);
  }
</style>
