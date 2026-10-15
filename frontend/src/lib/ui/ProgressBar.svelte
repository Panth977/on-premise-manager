<script lang="ts">
  let { value, tone = 'accent', label, height = 6, indeterminate = false }: { value: number; tone?: 'accent' | 'green' | 'red' | 'amber' | 'blue'; label?: string; height?: number; indeterminate?: boolean } = $props();
  const p = $derived(Math.max(0, Math.min(1, value || 0)));
</script>

<div class="pb" style="height:{height}px" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow={Math.round(p * 100)} aria-label={label} class:ind={indeterminate}>
  <div class="fill {tone}" style="width:{indeterminate ? 35 : p * 100}%"></div>
</div>

<style>
  .pb {
    width: 100%;
    background: var(--surface-active);
    border-radius: 999px;
    overflow: hidden;
    position: relative;
  }
  .fill {
    height: 100%;
    border-radius: 999px;
    transition: width 0.3s var(--ease);
    background: var(--accent);
  }
  .fill.green {
    background: var(--green);
  }
  .fill.red {
    background: var(--red);
  }
  .fill.amber {
    background: var(--amber);
  }
  .fill.blue {
    background: var(--blue);
  }
  .ind .fill {
    position: absolute;
    animation: ind 1.1s ease-in-out infinite;
  }
  @keyframes ind {
    0% {
      left: -35%;
    }
    100% {
      left: 100%;
    }
  }
</style>
