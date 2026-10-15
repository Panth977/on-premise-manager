<script lang="ts">
  /** Old links: /app/services/{slug}/image?tag=… or ?version=… → /app/services/{slug}/image/{version}. */
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { onMount } from 'svelte';
  onMount(() => {
    const v = page.url.searchParams.get('version') ?? page.url.searchParams.get('tag');
    const slug = page.params.serviceSlug!;
    goto(v ? `/app/services/${slug}/image/${encodeURIComponent(v)}` : `/app/services/${slug}`, { replaceState: true });
  });
</script>
