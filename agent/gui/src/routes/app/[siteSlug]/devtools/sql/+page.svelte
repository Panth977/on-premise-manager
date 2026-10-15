<script lang="ts">
  /** Dev tools › SQL (§21 #97): one workbench for PostgreSQL / SQLite / Cassandra connections (?c=, ?kind=). */
  import DevFrame from '$lib/devtools/DevFrame.svelte';
  import SqlWorkbench from '$lib/devtools/sql/SqlWorkbench.svelte';
</script>

<DevFrame tool="sql">
  {#snippet workbench(conn, info, site)}
    <SqlWorkbench {conn} {info} {site} />
  {/snippet}
</DevFrame>
