<script lang="ts">
  /** §21 #126 a shell on the server of one site (under Server in the sites tree; was the Agent page's "Host shell" tab). */
  import { page } from '$app/state';
  import PageHead from '$lib/ui/PageHead.svelte';
  import ChooseSite from '$lib/ui/ChooseSite.svelte';
  import TerminalTool from '$lib/tools/TerminalTool.svelte';

  const slug = $derived(page.params.siteSlug ?? '');
</script>

<PageHead icon="terminal" eyebrow="Server · site {slug}" title="Host shell" />

{#if slug === '*'}
  <ChooseSite what="a host shell" text={'"All sites" is selected; a host shell runs on the server of one site.'} />
{:else}
  <TerminalTool site={slug} mode="host" />
{/if}
