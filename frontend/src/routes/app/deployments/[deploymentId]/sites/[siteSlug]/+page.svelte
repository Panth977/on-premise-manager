<script lang="ts">
  /** /sites/{site} and old `?tab=containers|edge|site` links → the section route (round D: no tab row). */
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { sectionOfTab } from '$lib/shell/crumbs';
  $effect(() => {
    const u = page.url;
    goto(`${u.pathname.replace(/\/$/, '')}/${sectionOfTab(u.searchParams.get('tab'))}`, { replaceState: true });
  });
</script>
