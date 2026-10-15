/** Stack of open overlays (modals / drawers): z-index order, Esc closes only the top one, body scroll lock. */
let stack: number[] = [];
let n = 0;
export function pushOverlay(): { id: number; z: number } {
  const id = ++n;
  stack.push(id);
  if (typeof document !== 'undefined') document.documentElement.style.overflow = 'hidden';
  return { id, z: 1000 + stack.length * 10 }; // --z-overlay + 10 per level
}
export function popOverlay(id: number) {
  stack = stack.filter((x) => x !== id);
  if (!stack.length && typeof document !== 'undefined') document.documentElement.style.overflow = '';
}
export function isTop(id: number) {
  return stack[stack.length - 1] === id;
}
const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"]), [contenteditable="true"]';
export function focusables(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter((el) => el.offsetParent !== null || el === document.activeElement);
}
/** Keep Tab inside root. */
export function trapTab(e: KeyboardEvent, root: HTMLElement) {
  if (e.key !== 'Tab') return;
  const f = focusables(root);
  if (!f.length) {
    e.preventDefault();
    root.focus();
    return;
  }
  const first = f[0]!;
  const last = f[f.length - 1]!;
  if (e.shiftKey && (document.activeElement === first || document.activeElement === root)) {
    e.preventDefault();
    last.focus();
  } else if (!e.shiftKey && document.activeElement === last) {
    e.preventDefault();
    first.focus();
  }
}
/** Focus the first [autofocus] / input / button in root. */
export function focusFirst(root: HTMLElement) {
  const auto = root.querySelector<HTMLElement>('[data-autofocus], [autofocus]');
  if (auto) return auto.focus();
  const f = focusables(root).filter((el) => !el.classList.contains('overlay-close'));
  const input = f.find((el) => ['INPUT', 'TEXTAREA', 'SELECT'].includes(el.tagName));
  (input ?? f[0] ?? root).focus();
}
