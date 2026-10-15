<script lang="ts">
  /** Detail charts of one monitoring subject in a drawer (host cards on Status). */
  import Drawer from '$lib/ui/Drawer.svelte';
  import MetricsPanel from './MetricsPanel.svelte';

  let { open = $bindable(false), site, subject, title }: { open: boolean; site: string; subject: string; title: string } = $props();
  const isHost = $derived(subject === 'host');
</script>

<Drawer bind:open wide {title} subtitle="{site} · {isHost ? 'host' : subject.replace(/^container:/, 'container ')} · sampled every 15 s" testid="metrics-drawer">
  {#if open}<MetricsPanel {site} {subject} />{/if}
</Drawer>
