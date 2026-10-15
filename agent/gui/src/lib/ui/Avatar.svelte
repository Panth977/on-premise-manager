<script lang="ts">
  /** Round initials avatar (cloud ui/Avatar look without an image). */
  let { name, size = 28 }: { name: string; size?: number } = $props();
  const initials = $derived.by(() => {
    const local = (name || '?').split('@')[0] ?? '?';
    const parts = local.split(/[^A-Za-z0-9]+/).filter(Boolean);
    return ((parts[0]?.[0] ?? '?') + (parts[1]?.[0] ?? parts[0]?.[1] ?? '')).toUpperCase();
  });
</script>

<span class="av" style="width:{size}px;height:{size}px;font-size:{Math.round(size * 0.4)}px" aria-hidden="true">{initials}</span>

<style>
  .av {
    border-radius: 50%;
    flex: none;
    display: inline-grid;
    place-items: center;
    background: var(--accent-soft);
    color: var(--accent-text);
    font-weight: var(--fw-semibold);
    letter-spacing: 0.02em;
  }
</style>
