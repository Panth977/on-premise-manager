<script lang="ts">
  let { value, tone = '', label = 'Progress' }: { value: number; tone?: '' | 'ok' | 'bad'; label?: string } = $props();
  const p = $derived(Math.max(0, Math.min(1, Number.isFinite(value) ? value : 0)));
</script>

<div class="progress {tone}" role="progressbar" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(p * 100)}><i style="width:{p * 100}%"></i></div>
