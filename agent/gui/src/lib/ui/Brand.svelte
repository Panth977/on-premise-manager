<script lang="ts">
  /** The product mark (same as the cloud console's favicon.svg) — inline, no request. */
  let { size = 26 }: { size?: number } = $props();
</script>

<svg class="mark" width={size} height={size} viewBox="0 0 32 32" aria-hidden="true"
  ><rect width="32" height="32" rx="8" fill="var(--accent)" /><path d="M9 11.5 16 8l7 3.5v9L16 24l-7-3.5z" fill="none" stroke="#fff" stroke-width="2" stroke-linejoin="round" /><path
    d="M9 11.5 16 15l7-3.5M16 15v9"
    fill="none"
    stroke="#fff"
    stroke-width="2"
    stroke-linejoin="round"
  /></svg
>

<style>
  .mark {
    flex: none;
    display: block;
  }
</style>
