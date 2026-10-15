<script lang="ts">
  /** Page header (cloud ui/PageHeader look): icon tile · eyebrow · title (h1) · subtitle · actions on the right. */
  import type { Snippet } from 'svelte';
  import Icon from './Icon.svelte';
  let { title, sub, actions, eyebrow, icon, meta }: { title: string; sub?: string; actions?: Snippet; eyebrow?: string; icon?: string; meta?: Snippet } = $props();
</script>

<svelte:head><title>{title} · OPM agent</title></svelte:head>
<header class="ph">
  {#if icon}<div class="pic" aria-hidden="true"><Icon name={icon} size={20} /></div>{/if}
  <div class="tt">
    {#if eyebrow}<div class="section-title eb">{eyebrow}</div>{/if}
    <div class="row hl">
      <h1 data-testid="page-title">{title}</h1>
      {@render meta?.()}
    </div>
    {#if sub}<p class="sub">{sub}</p>{/if}
  </div>
  {#if actions}<div class="acts">{@render actions()}</div>{/if}
</header>

<style>
  .ph {
    display: flex;
    align-items: flex-start;
    gap: var(--sp-3);
    flex-wrap: wrap;
    margin-bottom: var(--sp-5);
  }
  .pic {
    width: 40px;
    height: 40px;
    border-radius: 10px;
    background: var(--surface);
    border: 1px solid var(--border);
    display: grid;
    place-items: center;
    color: var(--accent-text);
    box-shadow: var(--shadow-sm);
    flex: none;
  }
  .tt {
    flex: 1 1 240px;
    min-width: 0;
  }
  .eb {
    margin-bottom: 3px;
  }
  .hl {
    gap: var(--sp-2);
  }
  h1 {
    overflow-wrap: anywhere;
  }
  .sub {
    color: var(--text-2);
    margin: 4px 0 0;
    max-width: 820px;
  }
  @media (max-width: 560px) {
    .pic {
      width: 34px;
      height: 34px;
    }
  }
  .acts {
    display: flex;
    gap: var(--sp-2);
    flex-wrap: wrap;
    align-items: center;
    margin-left: auto;
  }
</style>
