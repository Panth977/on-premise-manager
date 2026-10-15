<script lang="ts" module>
  import { SvelteDate } from 'svelte/reactivity';
  import { serverClock } from '../util/serverclock.svelte';
  /** one shared clock for every relative time on the page (30 s tick) */
  const clock = new SvelteDate();
  if (typeof window !== 'undefined') {
    const tick = () => clock.setTime(Date.now());
    setInterval(tick, 15000);
    // timers freeze while the machine sleeps or the tab is hidden; resync the moment the page is seen again,
    // otherwise fresh data (e.g. lastSeenAt) is compared with an hours-old clock and shows "in 5 h"
    document.addEventListener('visibilitychange', () => document.visibilityState === 'visible' && tick());
    window.addEventListener('focus', tick);
    window.addEventListener('pageshow', tick);
  }
  /** now on the CLOUD's clock (§21 #92: the browser clock corrected by X-OPM-Server-Time) */
  export function nowMs() {
    return clock.getTime() + serverClock.offsetMs;
  }
</script>

<script lang="ts">
  import { relTime } from '../util/format';
  let { ts, prefix = '' }: { ts: string | null | undefined; prefix?: string } = $props();
  const text = $derived(relTime(ts, clock.getTime() + serverClock.offsetMs));
</script>

{#if ts}<time datetime={ts} title={ts} class="rt">{prefix}{text}</time>{:else}<span class="empty-dash">—</span>{/if}

<style>
  .rt {
    white-space: nowrap;
  }
</style>
