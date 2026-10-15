<script lang="ts">
  /** Dev tools › Redis (§21 #97): key browser, value editors, console, INFO. */
  import DevFrame from '$lib/devtools/DevFrame.svelte';
  import RedisWorkbench from '$lib/devtools/redis/RedisWorkbench.svelte';
</script>

<DevFrame tool="redis">
  {#snippet workbench(conn, info, site)}
    <RedisWorkbench {conn} {info} {site} />
  {/snippet}
</DevFrame>
