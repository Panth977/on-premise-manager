<!-- A long path that wraps only after "/" (or "." / "-") instead of mid-word (round T). Text content is unchanged. -->
<script lang="ts">
  let { path }: { path: string } = $props();
  const parts = $derived(path.split(/(?<=[/])/));
</script>

{#each parts as seg, i (i)}{#if i}<wbr />{/if}{seg}{/each}
