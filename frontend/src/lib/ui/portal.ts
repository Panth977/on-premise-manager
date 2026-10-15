/**
 * Overlay plumbing.
 * - `portal`: moves the node to document.body, so no ancestor (sticky header with backdrop-filter, transformed
 *   panels, overflow:auto bodies) becomes the containing block / clip box of a position:fixed overlay.
 * - `floating`: positions a portaled popover (listbox, menu, picker) next to its anchor with position:fixed,
 *   flips above when there is no room below, follows scroll / resize, and keeps it inside the viewport.
 * z-index comes from the tokens (--z-header < --z-drawer/--z-modal stack < --z-popover < --z-toast < --z-tooltip).
 */
export function portal(node: HTMLElement, target: HTMLElement | string = 'body') {
  const el = typeof target === 'string' ? document.querySelector<HTMLElement>(target) : target;
  (el ?? document.body).appendChild(node);
  return {
    destroy() {
      node.remove();
    },
  };
}

export interface FloatingOptions {
  anchor: HTMLElement | null;
  placement?: 'bottom-start' | 'bottom-end';
  /** popover width = anchor width (listboxes) */
  matchWidth?: boolean;
  /** minimum width in px */
  minWidth?: number;
  offset?: number;
}
export function floating(node: HTMLElement, opts: FloatingOptions) {
  let o = opts;
  node.style.position = 'fixed';
  node.style.zIndex = 'var(--z-popover)';
  function place() {
    const a = o.anchor;
    if (!a || !a.isConnected) return;
    const r = a.getBoundingClientRect();
    const vw = document.documentElement.clientWidth;
    const vh = window.innerHeight;
    const off = o.offset ?? 4;
    const minW = Math.max(o.minWidth ?? 0, o.matchWidth ? r.width : 0);
    if (minW) node.style.minWidth = `${Math.min(minW, vw - 16)}px`;
    if (o.matchWidth) node.style.width = `${Math.min(Math.max(r.width, o.minWidth ?? 0), vw - 16)}px`;
    node.style.maxWidth = `${vw - 16}px`;
    const w = node.offsetWidth;
    const h = node.offsetHeight;
    let left = o.placement === 'bottom-end' ? r.right - w : r.left;
    left = Math.max(8, Math.min(left, vw - w - 8));
    const below = vh - r.bottom - off - 8;
    const above = r.top - off - 8;
    let top: number;
    if (h <= below || below >= above) {
      top = r.bottom + off;
      node.style.maxHeight = `${Math.max(120, below)}px`;
    } else {
      node.style.maxHeight = `${Math.max(120, above)}px`;
      top = Math.max(8, r.top - off - Math.min(h, above));
    }
    node.style.left = `${Math.round(left)}px`;
    node.style.top = `${Math.round(top)}px`;
  }
  const ro = typeof ResizeObserver !== 'undefined' ? new ResizeObserver(place) : null;
  ro?.observe(node);
  window.addEventListener('scroll', place, true);
  window.addEventListener('resize', place);
  place();
  requestAnimationFrame(place);
  return {
    update(n: FloatingOptions) {
      o = n;
      place();
    },
    destroy() {
      ro?.disconnect();
      window.removeEventListener('scroll', place, true);
      window.removeEventListener('resize', place);
    },
  };
}
