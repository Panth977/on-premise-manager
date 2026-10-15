<script lang="ts">
  /** §21 #93: the two log display toggles (global, remembered): Colors (ANSI styles, default on) · Raw (bytes, ESC as ␛). */
  import { ansiPrefs } from '../ansi.svelte';
  let { testid = 'ansi', small = false }: { testid?: string; small?: boolean } = $props();
</script>

<span class="ansi-toggles" class:small>
  <label class="switch" title="Show the colors and bold / dim styles the program printed (ANSI escape codes)"><input type="checkbox" bind:checked={ansiPrefs.colors} disabled={ansiPrefs.raw} data-testid="{testid}-colors" />Colors</label>
  <label class="switch" title="Show the bytes as received: escape codes visible, ESC shown as ␛ (Download always keeps the raw bytes)"><input type="checkbox" bind:checked={ansiPrefs.raw} data-testid="{testid}-raw" />Raw</label>
</span>

<style>
  .ansi-toggles {
    display: inline-flex;
    gap: 12px;
    align-items: center;
  }
  .small .switch {
    font-size: var(--fs-sm);
  }
</style>
