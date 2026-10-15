<script lang="ts">
  /** §21 #92: this browser's clock differs from the cloud's by > 2 min → say so (relative times are already corrected). */
  import { CLOCK_SKEW_WARN_SEC, formatSkewDuration } from '@opm/shared';
  import { serverClock } from '../util/serverclock.svelte';
  import Icon from './Icon.svelte';
  import { Clock } from './icons';
  const off = $derived(serverClock.offsetMs / 1000);
</script>

{#if Math.abs(off) > CLOCK_SKEW_WARN_SEC}
  <div class="banner warn small" data-testid="browser-clock-note" data-offset={Math.round(off)}>
    <Icon icon={Clock} size={15} class="icon" />
    <span>This computer's clock is <b>{formatSkewDuration(off)} {off > 0 ? 'behind' : 'ahead of'}</b> the OPM cloud. Times on this page use the cloud's clock; fix the clock of this computer (automatic time / NTP).</span>
  </div>
{/if}
