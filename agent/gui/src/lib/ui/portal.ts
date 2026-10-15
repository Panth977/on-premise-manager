/**
 * Svelte action: move an overlay (modal / drawer <dialog>, toasts) to document.body so no ancestor with
 * backdrop-filter / transform / filter / contain (e.g. the sticky header) becomes its containing block or clips it.
 * Listbox popovers (Combobox, expression autocomplete) are NOT portaled: they use the top layer (popover="manual")
 * and must stay inside an open modal <dialog>, or the modal would make them inert.
 */
export function portal(node: HTMLElement) {
  if (typeof document === 'undefined') return;
  const home = node.parentNode;
  const marker = document.createComment('portal');
  home?.insertBefore(marker, node);
  document.body.appendChild(node);
  return {
    destroy() {
      marker.remove();
      node.remove();
    },
  };
}
