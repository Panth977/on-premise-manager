<script lang="ts">
  /** Scaled, sandboxed (no scripts) preview of a not-found page document. */
  let { html, width = 168, label }: { html: string | null; width?: number; label?: string } = $props();
  const W = 1024;
  const H = 640;
  const scale = $derived(width / W);
</script>

<div class="th" style="width:{width}px;height:{Math.round(H * scale)}px" aria-label={label ? `Preview of ${label}` : 'Preview'} role="img">
  {#if html !== null}
    <iframe title={label ?? 'Preview'} sandbox="" srcdoc={html} tabindex="-1" style="width:{W}px;height:{H}px;transform:scale({scale})"></iframe>
  {:else}
    <span class="skel" style="width:100%;height:100%"></span>
  {/if}
</div>

<style>
  .th {
    position: relative;
    overflow: hidden;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    background: #fff;
    flex: none;
  }
  iframe {
    border: 0;
    transform-origin: 0 0;
    pointer-events: none;
    position: absolute;
    inset: 0 auto auto 0;
  }
</style>
