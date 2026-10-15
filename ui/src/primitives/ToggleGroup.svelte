<script lang="ts">
  /** Multi-select as toggle chips (aria-pressed). Empty selection is meaningful for some fields (e.g. "all"). */
  let { options, selected, onchange, emptyLabel, disabled = false, label }: { options: { value: string; label?: string; disabled?: boolean; title?: string }[]; selected: string[]; onchange: (v: string[]) => void; emptyLabel?: string; disabled?: boolean; label: string } = $props();
  function toggle(v: string) {
    onchange(selected.includes(v) ? selected.filter((x) => x !== v) : [...selected, v]);
  }
</script>

<div class="tg" role="group" aria-label={label}>
  {#each options as o (o.value)}
    <button type="button" class="t" aria-pressed={selected.includes(o.value)} disabled={disabled || o.disabled} title={o.title} onclick={() => toggle(o.value)}>{o.label ?? o.value}</button>
  {:else}<span class="faint small">none available</span>{/each}
  {#if emptyLabel && selected.length === 0}<span class="faint small">{emptyLabel}</span>{/if}
</div>

<style>
  .tg {
    display: flex;
    flex-wrap: wrap;
    gap: 5px;
    align-items: center;
  }
  .t {
    font: inherit;
    font-size: 12.5px;
    padding: 4px 10px;
    border-radius: 99px;
    border: 1px solid var(--border-strong);
    background: var(--surface);
    color: var(--text-2);
    cursor: pointer;
  }
  .t[aria-pressed='true'] {
    background: var(--accent-soft);
    color: var(--accent-text);
    border-color: color-mix(in srgb, var(--accent) 50%, transparent);
    font-weight: var(--fw-semibold);
  }
  .t:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }
</style>
