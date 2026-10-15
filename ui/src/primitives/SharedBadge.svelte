<!--
  Compact "shared · N containers" badge (round T). N counts every user incl. the current one; the button opens a
  light-dismiss popover (popover="auto", top layer) listing them, the current one marked "this container".
  Optional href(item) turns each item into a link.
-->
<script lang="ts">
  import Icon from './Icon.svelte';

  let {
    items,
    current = null,
    label = 'shared',
    noun = 'containers',
    title = 'Containers of this site that use the same copy',
    href,
    testid,
  }: { items: string[]; current?: string | null; label?: string; noun?: string; title?: string; href?: (item: string) => string; testid?: string } = $props();
  const id = `sb-${Math.random().toString(36).slice(2, 9)}`;
  let btn = $state<HTMLButtonElement>();
  let pop = $state<HTMLElement>();
  let open = $state(false);
  const all = $derived(current && !items.includes(current) ? [current, ...items] : items);

  function place() {
    if (!btn || !pop) return;
    const r = btn.getBoundingClientRect();
    const vw = window.innerWidth;
    const pw = Math.min(pop.offsetWidth || 260, vw - 16);
    pop.style.left = `${Math.max(8, Math.min(r.left, vw - pw - 8))}px`;
    const ph = pop.offsetHeight || 120;
    pop.style.top = `${r.bottom + 6 + ph > window.innerHeight - 8 ? Math.max(8, r.top - 6 - ph) : r.bottom + 6}px`;
  }
  function toggle() {
    if (!pop) return;
    try {
      pop.togglePopover();
    } catch {
      /* detached */
    }
  }
  $effect(() => {
    if (!open) return;
    place();
    const f = () => place();
    window.addEventListener('scroll', f, true);
    window.addEventListener('resize', f);
    return () => {
      window.removeEventListener('scroll', f, true);
      window.removeEventListener('resize', f);
    };
  });
</script>

<button type="button" class="badge accent shared-badge" bind:this={btn} aria-haspopup="true" aria-expanded={open} aria-controls={id} {title} onclick={toggle} data-testid={testid}
  ><Icon name="link" size={11} />{label} · {all.length} {all.length === 1 ? noun.replace(/s$/, '') : noun}</button
><div class="sb-pop" {id} popover="auto" bind:this={pop} ontoggle={(e) => (open = (e as ToggleEvent).newState === 'open')} data-testid={testid ? `${testid}-list` : undefined}>
  <div class="sb-head">{title}</div>
  <ul>
    {#each all as it (it)}
      <li class:cur={it === current}>
        {#if href && it !== current}<a href={href(it)} class="mono">{it}</a>{:else}<span class="mono">{it}</span>{/if}
        {#if it === current}<span class="tiny muted">this container</span>{/if}
      </li>
    {/each}
  </ul>
</div>

<style>
  .shared-badge {
    cursor: pointer;
    border: 0;
    font-family: inherit;
    font-size: var(--fs-xs);
    gap: 4px;
  }
  .shared-badge:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 1px;
  }
  .sb-pop {
    position: fixed;
    inset: auto;
    margin: 0;
    padding: 6px;
    min-width: 220px;
    max-width: min(360px, calc(100vw - 16px));
    max-height: 320px;
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    background: var(--surface);
    color: var(--text);
    box-shadow: var(--shadow-lg);
  }
  .sb-head {
    padding: 4px 8px 6px;
    font-size: var(--fs-xs);
    color: var(--text-2);
    border-bottom: 1px solid var(--border);
    margin-bottom: 4px;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  li {
    display: flex;
    justify-content: space-between;
    gap: 12px;
    padding: 5px 8px;
    border-radius: var(--r-sm, 5px);
    font-size: var(--fs-sm);
  }
  li.cur {
    background: var(--accent-soft, var(--surface-2));
  }
  li a {
    color: var(--text);
    text-decoration: none;
  }
  li a:hover {
    color: var(--accent);
    text-decoration: underline;
  }
</style>
