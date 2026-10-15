<script lang="ts">
  import { durationLeft } from '../util/format';
  import { onDestroy } from 'svelte';
  import { serverNow } from '../util/serverclock.svelte';
  let { until, prefix = 'expires in ' }: { until: string; prefix?: string } = $props();
  let now = $state(serverNow());
  const t = setInterval(() => (now = serverNow()), 1000); // §21 #92 the cloud's clock
  onDestroy(() => clearInterval(t));
  const left = $derived(durationLeft(until, now));
</script>

<span class="cdn" class:exp={left === 'expired'} title={until}>{left === 'expired' ? 'expired' : prefix + left}</span>

<style>
  .cdn {
    font-variant-numeric: tabular-nums;
    color: var(--text-2);
    font-size: var(--fs-sm);
  }
  .exp {
    color: var(--red-text);
  }
</style>
