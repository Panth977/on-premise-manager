<script lang="ts">
  import { page } from '$app/state';
  import ContainerEditor from '$lib/editors/ContainerEditor.svelte';
</script>

<ContainerEditor site={page.params.siteSlug ?? ''} containerSlug={page.params.containerSlug ?? ''} />
