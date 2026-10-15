<script lang="ts">
  /** §21 #92 an agent's clock vs the cloud: warning when |skew| > CLOCK_SKEW_WARN_SEC, else a quiet "in sync" note. */
  import { clockSkewWarning, formatSkewDuration, type AgentClockSkew } from '@opm/shared';
  import Icon from './Icon.svelte';
  import { TriangleAlert } from './icons';
  let { skew, compact = false }: { skew: AgentClockSkew | null | undefined; compact?: boolean } = $props();
  const warn = $derived(clockSkewWarning(skew?.skewSec));
</script>

{#if skew && warn}
  <span class="sk warn" class:compact data-testid="clock-skew-warning" data-skew={skew.skewSec}><Icon icon={TriangleAlert} size={13} /><span>{warn}</span></span>
{:else if skew}
  <span class="sk ok xs faint" data-testid="clock-skew-ok" data-skew={skew.skewSec}>in sync{Math.abs(skew.skewSec) >= 1 ? ` (${skew.skewSec > 0 ? '+' : '−'}${formatSkewDuration(skew.skewSec)})` : ''}</span>
{:else if !compact}
  <span class="xs faint" data-testid="clock-skew-unknown">not reported (agent &lt; 0.11)</span>
{/if}

<style>
  .sk {
    display: inline-flex;
    gap: 6px;
    align-items: flex-start;
  }
  .warn {
    padding: 4px 8px;
    border-radius: var(--r-md);
    border: 1px solid color-mix(in srgb, var(--amber) 40%, var(--border));
    background: var(--amber-soft);
    color: var(--amber-text);
    font-size: 12.5px;
    line-height: 1.4;
  }
  .warn.compact {
    padding: 2px 6px;
    font-size: 12px;
  }
</style>
