<script lang="ts" module>
  export interface TabItem {
    id: string;
    label: string;
    href?: string;
    count?: number | string | null;
    badge?: 'red' | 'amber' | null;
  }
</script>

<script lang="ts">
  let { tabs, active = $bindable(), onselect, testid = 'tabs' }: { tabs: TabItem[]; active: string; onselect?: (id: string) => void; testid?: string } = $props();
  function sel(id: string) {
    active = id;
    onselect?.(id);
  }
  function onkey(e: KeyboardEvent, i: number) {
    if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft') return;
    e.preventDefault();
    const n = tabs[(i + (e.key === 'ArrowRight' ? 1 : tabs.length - 1)) % tabs.length]!;
    const el = (e.currentTarget as HTMLElement).parentElement?.querySelector<HTMLElement>(`[data-tab="${n.id}"]`);
    el?.focus();
    if (!n.href) sel(n.id);
  }
</script>

<div class="tabs" role="tablist" data-testid={testid}>
  {#each tabs as t, i (t.id)}
    {#if t.href}
      <a href={t.href} role="tab" class="tab" class:on={active === t.id} aria-selected={active === t.id} data-tab={t.id} onkeydown={(e) => onkey(e, i)}>
        {t.label}
        {#if t.count !== undefined && t.count !== null}<span class="cnt" class:red={t.badge === 'red'} class:amber={t.badge === 'amber'}>{t.count}</span>{/if}
      </a>
    {:else}
      <button type="button" role="tab" class="tab" class:on={active === t.id} aria-selected={active === t.id} tabindex={active === t.id ? 0 : -1} data-tab={t.id} onclick={() => sel(t.id)} onkeydown={(e) => onkey(e, i)}>
        {t.label}
        {#if t.count !== undefined && t.count !== null}<span class="cnt" class:red={t.badge === 'red'} class:amber={t.badge === 'amber'}>{t.count}</span>{/if}
      </button>
    {/if}
  {/each}
</div>

<style>
  .tabs {
    display: flex;
    gap: 2px;
    border-bottom: 1px solid var(--border);
    overflow-x: auto;
    scrollbar-width: none;
  }
  .tab {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 9px 12px 10px;
    border: 0;
    background: transparent;
    color: var(--text-2);
    font-weight: var(--fw-medium);
    font-size: var(--fs-md);
    cursor: pointer;
    border-bottom: 2px solid transparent;
    margin-bottom: -1px;
    white-space: nowrap;
    text-decoration: none;
    border-radius: 6px 6px 0 0;
  }
  .tab:hover {
    color: var(--text);
    text-decoration: none;
    background: var(--surface-hover);
  }
  .tab.on {
    color: var(--text);
    border-bottom-color: var(--accent);
  }
  .cnt {
    font-size: 11px;
    min-width: 18px;
    height: 18px;
    padding: 0 5px;
    border-radius: 9px;
    background: var(--gray-soft);
    color: var(--text-2);
    display: inline-grid;
    place-items: center;
    font-variant-numeric: tabular-nums;
  }
  .cnt.red {
    background: var(--red-soft);
    color: var(--red-text);
  }
  .cnt.amber {
    background: var(--amber-soft);
    color: var(--amber-text);
  }
</style>
