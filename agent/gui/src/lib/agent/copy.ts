import { toasts } from '$lib/state/toast.svelte';

/** Copy text (navigator.clipboard, else the execCommand fallback for plain-http LAN addresses) + a toast. */
export async function copyText(text: string, label: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    const ta = document.createElement('textarea');
    ta.value = text;
    document.body.appendChild(ta);
    ta.select();
    document.execCommand('copy');
    ta.remove();
  }
  toasts.ok(`Copied ${label}`);
}
