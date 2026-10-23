<script lang="ts">
  /** §21 #125 the port tunnels of a site (switch each on / off here; added and removed in the cloud console). */
  import { page } from '$app/state';
  import PageHead from '$lib/ui/PageHead.svelte';
  import ChooseSite from '$lib/ui/ChooseSite.svelte';
  import TunnelsCard from '$lib/agent/TunnelsCard.svelte';

  const slug = $derived(page.params.siteSlug ?? '');
</script>

<PageHead icon="network" eyebrow="Agent · site {slug}" title="Tunnels" sub="Ports of this site reachable through the cloud host (raw TCP: a website, an API, a database). Added and removed in the cloud console › Tunnels; each one stays off until it is switched on here." />

{#if slug === '*'}
  <ChooseSite what="tunnels" text={`"All sites" is selected. Tunnels belong to one site: pick it.`} />
{:else}
  <TunnelsCard site={slug} />
{/if}
