<script lang="ts">
  let { count = 3, height = 120 }: { count?: number; height?: number } = $props();
</script>

<div class="cards" aria-busy="true" data-testid="skeleton">
  {#each Array(count) as _, i (i)}
    <div class="card card-pad stack" style="min-height:{height}px">
      <span class="skel" style="width:55%;height:14px"></span>
      <span class="skel" style="width:35%"></span>
      <span class="skel" style="width:80%"></span>
      <span class="skel" style="width:60%"></span>
    </div>
  {/each}
</div>

<style>
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    gap: var(--sp-4);
  }
</style>
