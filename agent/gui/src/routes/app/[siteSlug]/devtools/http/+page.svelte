<script lang="ts">
  /** Dev tools › HTTP client (§21 #97): Postman-like requests run on the site server. */
  import DevFrame from '$lib/devtools/DevFrame.svelte';
  import HttpClient from '$lib/devtools/http/HttpClient.svelte';
</script>

<DevFrame tool="http">
  {#snippet workbench(conn, info, site)}
    <HttpClient {conn} {info} {site} />
  {/snippet}
</DevFrame>
