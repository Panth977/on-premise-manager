<script lang="ts">
  /** §21 #100: Send and Receive are one Transfers page; the old route redirects (query kept). */
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  $effect(() => {
    const u = page.url;
    goto(`${u.pathname.replace(/\/(send|receive)\/?$/, '/transfers')}${u.search}`, { replaceState: true });
  });
</script>
