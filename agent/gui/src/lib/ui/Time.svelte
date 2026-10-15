<script lang="ts">
  import { relTime, fmtDateTime } from '../format';
  import { session } from '../state/session.svelte';
  let { ts, clock = false }: { ts: string | null | undefined; clock?: boolean } = $props();
  const text = $derived.by(() => {
    void session.tick;
    return relTime(ts, session.now());
  });
</script>

{#if ts}<time datetime={ts} title={`${fmtDateTime(ts)} (${ts})`} class="nowrap">{clock ? fmtDateTime(ts).slice(11, 16) : text}</time>{:else}<span class="faint">—</span>{/if}
