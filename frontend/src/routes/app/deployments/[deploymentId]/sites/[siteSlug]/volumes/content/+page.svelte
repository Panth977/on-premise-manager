<script lang="ts">
  /** §21 #101 the content page of one site volume: `?volume=<volumeSlug>&tab=content|persist|cloud` (static path + query, like edge/route). */
  import { page } from '$app/state';
  import { getScope } from '$lib/config-editor/context';
  import VolumeContentPage from '$lib/config-editor/volume-content/VolumeContentPage.svelte';
  const scope = getScope();
</script>

<div class="page">
  {#key `${page.params.siteSlug}/${page.url.searchParams.get('volume')}`}
    <VolumeContentPage {scope} siteSlug={page.params.siteSlug!} />
  {/key}
</div>
