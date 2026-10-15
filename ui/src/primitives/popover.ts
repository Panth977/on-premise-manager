/**
 * Anchored listbox popovers (Combobox, expression autocomplete). The list uses the HTML Popover API
 * (popover="manual") so it renders in the top layer: never clipped by .table-wrap / drawer scrolling and
 * above <dialog> modals and drawers. Position = fixed, below the anchor (above when there is no room).
 */
export function placeBelow(pop: HTMLElement, anchor: HTMLElement, maxH = 280) {
  const r = anchor.getBoundingClientRect();
  const vw = window.innerWidth;
  const vh = window.innerHeight;
  const w = Math.min(Math.max(r.width, 220), vw - 16);
  pop.style.minWidth = `${w}px`;
  pop.style.maxWidth = `${Math.max(w, Math.min(460, vw - 16))}px`;
  pop.style.left = `${Math.max(8, Math.min(r.left, vw - w - 8))}px`;
  const h = Math.min(pop.scrollHeight || maxH, maxH);
  const below = vh - r.bottom - 8;
  const top = below >= h || below >= r.top - 8 ? r.bottom + 4 : Math.max(8, r.top - 4 - h);
  pop.style.top = `${top}px`;
  pop.style.maxHeight = `${Math.max(120, below >= h || below >= r.top - 8 ? below - 4 : r.top - 12)}px`;
}

export function showPop(pop: HTMLElement | undefined) {
  if (!pop) return;
  try {
    if (!pop.matches(':popover-open')) pop.showPopover();
  } catch {
    /* already open / detached */
  }
}
export function hidePop(pop: HTMLElement | undefined) {
  if (!pop) return;
  try {
    if (pop.matches(':popover-open')) pop.hidePopover();
  } catch {
    /* detached */
  }
}

/** Re-place the popover while the page scrolls or resizes; returns the cleanup. */
export function follow(pop: HTMLElement, anchor: HTMLElement): () => void {
  const f = () => placeBelow(pop, anchor);
  window.addEventListener('scroll', f, true);
  window.addEventListener('resize', f);
  return () => {
    window.removeEventListener('scroll', f, true);
    window.removeEventListener('resize', f);
  };
}
