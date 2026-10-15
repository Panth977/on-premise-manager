<script lang="ts">
  /** On / off switch (SPEC §17.0: no native checkboxes in forms). Label text sits right of the switch. */
  let { checked = $bindable(false), label = '', onchange, disabled = false, id, testid, ariaLabel }: { checked?: boolean; label?: string; onchange?: (v: boolean) => void; disabled?: boolean; id?: string; testid?: string; ariaLabel?: string } = $props();
</script>

<label class="switch" class:dis={disabled} data-testid={testid} data-checked={checked ? 'true' : 'false'}>
  <input type="checkbox" role="switch" {id} {disabled} bind:checked aria-label={ariaLabel ?? (label || undefined)} onchange={(e) => onchange?.(e.currentTarget.checked)} data-testid={testid ? `${testid}-input` : undefined} />
  {#if label}<span>{label}</span>{/if}
</label>

<style>
  .dis {
    cursor: not-allowed;
    color: var(--text-2);
  }
</style>
