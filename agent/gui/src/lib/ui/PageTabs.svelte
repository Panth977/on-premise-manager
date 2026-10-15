<script lang="ts" module>
  /** the current tab of a page (?tab=), falling back to the first one */
  export function tabOf<K extends string>(url: URL, keys: readonly K[]): K {
    const t = url.searchParams.get('tab') as K | null;
    return t && keys.includes(t) ? t : keys[0];
  }
</script>

<script lang="ts">
  /**
   * Page-level tab row synced with ?tab= (nav2: Network › Checks | Edge access log, Debug › Debug log | Live agent log,
   * Agent › Agent | Host shell). ← / → move between tabs (roving tabindex).
   */
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import Icon from './Icon.svelte';

  let { tabs, label, testid = 'page-tabs' }: { tabs: { k: string; label: string; icon?: string }[]; label: string; testid?: string } = $props();
  const cur = $derived(tabOf(page.url, tabs.map((t) => t.k)));
  function set(k: string) {
    const u = new URL(page.url);
    if (k === tabs[0].k) u.searchParams.delete('tab');
    else u.searchParams.set('tab', k);
    goto(u.pathname + u.search, { replaceState: true, noScroll: true, keepFocus: true });
  }
  function key(e: KeyboardEvent, i: number) {
    const d = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0;
    if (!d) return;
    e.preventDefault();
    const n = tabs[(i + d + tabs.length) % tabs.length];
    set(n.k);
    (document.querySelector(`[data-testid="${testid}-${n.k}"]`) as HTMLElement | null)?.focus();
  }
</script>

<div class="tabs ptabs" role="tablist" aria-label={label} data-testid={testid}>
  {#each tabs as t, i (t.k)}
    <button role="tab" aria-selected={cur === t.k} tabindex={cur === t.k ? 0 : -1} onclick={() => set(t.k)} onkeydown={(e) => key(e, i)} data-testid="{testid}-{t.k}">{#if t.icon}<Icon name={t.icon} size={14} />{/if}{t.label}</button>
  {/each}
</div>

<style>
  .ptabs {
    margin-bottom: 16px;
    overflow-x: auto;
    scrollbar-width: none;
  }
  .ptabs button {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    white-space: nowrap;
  }
</style>
