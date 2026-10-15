<script lang="ts">
  import { toneOf } from '../util/status';
  let { kind, value, label, tip, dot = true }: { kind: 'proposal' | 'import' | 'staging' | 'transfer' | 'token'; value: string; label?: string; tip?: string; dot?: boolean } = $props();
  const tone = $derived(toneOf(kind, value));
  const busy = $derived(['staging', 'applying', 'copying', 'downloading', 'uploading'].includes(value));
</script>

<span class="badge {tone}" data-tip={tip} data-testid="status-{value}">
  {#if dot}<span class="dot" class:pulse={busy}></span>{/if}
  {label ?? value}
</span>

<style>
  .pulse {
    animation: pulse 1.2s ease-in-out infinite;
  }
  @keyframes pulse {
    50% {
      opacity: 0.3;
    }
  }
</style>
