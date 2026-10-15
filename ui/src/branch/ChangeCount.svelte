<script lang="ts">
  /** "N uncommitted" badge (§21 #104 D4): sidebar scope headers, lists, Overview, the agent GUI. Nothing when 0 unless `showZero`. */
  let { count, showZero = false, noun = 'uncommitted', testid = 'uncommitted-count' }: { count: number | null | undefined; showZero?: boolean; noun?: string; testid?: string } = $props();
</script>

{#if (count ?? 0) > 0}
  <span class="badge amber" data-testid={testid} data-count={count} title="Changes saved in the working copy but not committed yet">{count} {noun}</span>
{:else if showZero}
  <span class="badge outline" data-testid={testid} data-count="0">clean</span>
{/if}
