<script lang="ts">
  /** The cloud console's switch = the shared switch of both GUIs ($opm Toggle, SPEC §21 #102); same props + bind:checked. */
  import SharedToggle from '$opm/primitives/Toggle.svelte';
  let { checked = $bindable(false), label = '', disabled = false, id, onchange, testid, ariaLabel }: { checked?: boolean; label?: string; disabled?: boolean; id?: string; onchange?: (v: boolean) => void; testid?: string; ariaLabel?: string } = $props();
</script>

<SharedToggle bind:checked {label} {disabled} {id} {onchange} {testid} {ariaLabel} />
