/**
 * Shared plumbing of the custom dropdowns (Select, MultiSelect) — a port of the cloud console's overlay/portal
 * helpers (frontend/src/lib/ui/portal.ts `floating`). The list is a `popover="manual"` element, so it renders in the
 * browser top layer: above every card, sticky header and drawer, never clipped by a .table-wrap or overflow:auto,
 * and it still works INSIDE an open modal <dialog> (a node moved to <body> would be inert behind the modal).
 * That is the agent GUI's equivalent of the cloud's "portal to body" (see ui/portal.ts).
 */
export interface SelectOption {
  value: string;
  label: string;
  /** right-aligned muted text */
  hint?: string;
  /** Icon.svelte name */
  icon?: string;
  /** a small status dot before the label (nav2 site selector: reachability) */
  dot?: 'green' | 'amber' | 'red' | 'gray';
  badge?: { text: string; tone?: 'green' | 'amber' | 'red' | 'blue' | 'gray' };
  /** options with the same group are listed under one heading (in first-seen order) */
  group?: string;
  disabled?: boolean;
  /** an action entry (e.g. "+ New data volume…"): calls onchange but never becomes the value; accent colour */
  action?: boolean;
}

/** Position a fixed list under its anchor (flip above when there is no room), keep it inside the viewport. */
export function placeList(pop: HTMLElement, anchor: HTMLElement, minWidth = 200) {
  const r = anchor.getBoundingClientRect();
  const vw = document.documentElement.clientWidth || window.innerWidth;
  const vh = window.innerHeight;
  const w = Math.min(Math.max(r.width, minWidth), vw - 16);
  pop.style.minWidth = `${w}px`;
  pop.style.maxWidth = `${Math.max(w, Math.min(480, vw - 16))}px`;
  const pw = Math.min(pop.offsetWidth || w, vw - 16);
  pop.style.left = `${Math.round(Math.max(8, Math.min(r.left, vw - pw - 8)))}px`;
  const below = vh - r.bottom - 12;
  const above = r.top - 12;
  const h = Math.min(pop.scrollHeight || 320, 360);
  if (h <= below || below >= above) {
    pop.style.maxHeight = `${Math.max(140, Math.min(360, below))}px`;
    pop.style.top = `${Math.round(r.bottom + 4)}px`;
  } else {
    const mh = Math.max(140, Math.min(360, above));
    pop.style.maxHeight = `${mh}px`;
    pop.style.top = `${Math.round(Math.max(8, r.top - 4 - Math.min(h, mh)))}px`;
  }
}

/** Follow scroll / resize while open; returns the cleanup. */
export function followList(pop: HTMLElement, anchor: HTMLElement, minWidth = 200): () => void {
  const f = () => placeList(pop, anchor, minWidth);
  window.addEventListener('scroll', f, true);
  window.addEventListener('resize', f);
  const ro = typeof ResizeObserver !== 'undefined' ? new ResizeObserver(f) : null;
  ro?.observe(pop);
  return () => {
    window.removeEventListener('scroll', f, true);
    window.removeEventListener('resize', f);
    ro?.disconnect();
  };
}

export function openPop(pop: HTMLElement | undefined) {
  try {
    if (pop && !pop.matches(':popover-open')) pop.showPopover();
  } catch {
    /* detached */
  }
}
export function closePop(pop: HTMLElement | undefined) {
  try {
    if (pop && pop.matches(':popover-open')) pop.hidePopover();
  } catch {
    /* detached */
  }
}

/** Options grouped in first-seen order (ungrouped first), each with its flat index. */
export function groupOptions<T extends SelectOption>(opts: T[]): { label: string | null; items: { o: T; i: number }[] }[] {
  const out: { label: string | null; items: { o: T; i: number }[] }[] = [];
  const by = new Map<string | null, { label: string | null; items: { o: T; i: number }[] }>();
  opts.forEach((o, i) => {
    const k = o.group ?? null;
    let g = by.get(k);
    if (!g) {
      g = { label: k, items: [] };
      by.set(k, g);
      if (k === null) out.unshift(g);
      else out.push(g);
    }
    g.items.push({ o, i });
  });
  // flat order must follow the visual order for keyboard navigation
  return out;
}

export function matches(o: SelectOption, q: string): boolean {
  const s = q.trim().toLowerCase();
  if (!s) return true;
  return o.label.toLowerCase().includes(s) || o.value.toLowerCase().includes(s) || (o.hint ?? '').toLowerCase().includes(s) || (o.group ?? '').toLowerCase().includes(s);
}

export const TONE: Record<string, string> = { green: 'green', amber: 'amber', red: 'red', blue: 'blue', gray: '' };
