<script lang="ts">
  /** §21 #123: the per-site Transfers (and the older Send / Receive) became the deployment's Shared files; redirect (query kept). */
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  $effect(() => {
    const u = page.url;
    goto(`${u.pathname.replace(/\/sites\/[^/]+\/(transfers|send|receive)\/?$/, '/shared-files')}${u.search}`, { replaceState: true });
  });
</script>
