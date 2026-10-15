<script lang="ts">
  let { rows = 5, cols = 5, head = true }: { rows?: number; cols?: number; head?: boolean } = $props();
  const widths = [38, 62, 48, 70, 30, 55, 44, 66];
</script>

<div class="table-wrap" aria-busy="true" aria-label="Loading" data-testid="skeleton">
  <table class="table">
    {#if head}
      <thead><tr>{#each Array(cols) as _, c (c)}<th><span class="skel" style="width:{30 + ((c * 17) % 40)}%;height:10px"></span></th>{/each}</tr></thead>
    {/if}
    <tbody>
      {#each Array(rows) as _, r (r)}
        <tr>{#each Array(cols) as _, c (c)}<td><span class="skel" style="width:{widths[(r + c * 3) % widths.length]}%"></span></td>{/each}</tr>
      {/each}
    </tbody>
  </table>
</div>
