<script lang="ts" module>
  /** the sidebar width both GUIs remember (localStorage; storage may be blocked) */
  const KEY = 'opm.sidebar.w';
  export const SIDEBAR_MIN = 200;
  export const SIDEBAR_MAX = 520;
  export function loadSidebarWidth(): number | null {
    try {
      const n = Number(localStorage.getItem(KEY));
      return Number.isFinite(n) && n >= SIDEBAR_MIN && n <= SIDEBAR_MAX ? n : null;
    } catch {
      return null;
    }
  }
  function saveSidebarWidth(w: number | null) {
    try {
      if (w == null) localStorage.removeItem(KEY);
      else localStorage.setItem(KEY, String(Math.round(w)));
    } catch {
      /* not remembered */
    }
  }
</script>

<script lang="ts">
  /**
   * Drag handle on the right edge of the sidebar (cloud Shell and agent layout). `width` = null → the --sidebar-w token.
   * Drag, or focus + ArrowLeft / ArrowRight (16 px); double-click resets. Hidden below 900 px (the sidebar is a drawer).
   */
  let { width = $bindable(null), sideEl }: { width?: number | null; sideEl?: HTMLElement | null } = $props();
  let dragging = $state(false);
  const clamp = (w: number) => Math.min(SIDEBAR_MAX, Math.max(SIDEBAR_MIN, w));
  const current = () => width ?? sideEl?.getBoundingClientRect().width ?? 236;
  function set(w: number | null) {
    width = w == null ? null : clamp(w);
    saveSidebarWidth(width);
  }
  let startX = 0;
  let startW = 0;
  function down(e: PointerEvent) {
    if (e.button !== 0) return;
    e.preventDefault();
    startX = e.clientX;
    startW = current();
    dragging = true;
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
  }
  function move(e: PointerEvent) {
    if (dragging) width = clamp(startW + e.clientX - startX);
  }
  function up(e: PointerEvent) {
    if (!dragging) return;
    dragging = false;
    (e.currentTarget as HTMLElement).releasePointerCapture(e.pointerId);
    set(width);
  }
  function key(e: KeyboardEvent) {
    if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
      e.preventDefault();
      set(current() + (e.key === 'ArrowRight' ? 16 : -16));
    } else if (e.key === 'Home') {
      e.preventDefault();
      set(null);
    }
  }
</script>

<svelte:body class:opm-resizing={dragging} />

<!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
<div
  class="rz"
  class:on={dragging}
  role="separator"
  aria-orientation="vertical"
  aria-label="Resize the sidebar (double-click resets)"
  aria-valuemin={SIDEBAR_MIN}
  aria-valuemax={SIDEBAR_MAX}
  aria-valuenow={Math.round(width ?? 236)}
  tabindex="0"
  title="Drag to resize · double-click resets"
  onpointerdown={down}
  onpointermove={move}
  onpointerup={up}
  onpointercancel={up}
  ondblclick={() => set(null)}
  onkeydown={key}
  data-testid="sidebar-resizer"
></div>

<style>
  .rz {
    position: absolute;
    top: 0;
    right: -4px;
    bottom: 0;
    width: 8px;
    cursor: col-resize;
    z-index: 2;
    touch-action: none;
  }
  .rz::after {
    content: '';
    position: absolute;
    top: 0;
    bottom: 0;
    left: 3px;
    width: 2px;
    background: transparent;
    transition: background var(--dur-fast, 0.12s);
  }
  .rz:hover::after,
  .rz:focus-visible::after,
  .rz.on::after {
    background: var(--accent);
  }
  .rz:focus-visible {
    outline: none;
  }
  :global(body.opm-resizing) {
    cursor: col-resize;
    user-select: none;
  }
  @media (max-width: 900px) {
    .rz {
      display: none;
    }
  }
</style>
