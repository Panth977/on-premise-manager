<script lang="ts">
  /**
   * A numbered section card of a form (page forms: container, route, image; drawers use it without a number).
   * id = the anchor the FormPage section nav jumps to.
   */
  import type { Snippet } from 'svelte';
  let { n, title, sub, id, testid, actions, children }: { n?: number; title: string; sub?: string; id?: string; testid?: string; actions?: Snippet; children: Snippet } = $props();
</script>

<section class="card fsec" {id} data-testid={testid}>
  <header class="card-head">
    <h3>{#if n != null}<span class="num">{n}</span>{/if}{title}</h3>
    {#if actions}<div class="acts">{@render actions()}</div>{/if}
    {#if sub}<p class="sub hint">{sub}</p>{/if}
  </header>
  <div class="card-body">{@render children()}</div>
</section>

<style>
  .fsec {
    scroll-margin-top: 72px;
  }
  h3 {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
  }
  .num {
    display: inline-grid;
    place-items: center;
    width: 20px;
    height: 20px;
    border-radius: 50%;
    background: var(--accent-soft);
    color: var(--accent-text);
    font-size: 11px;
    font-weight: var(--fw-semibold);
  }
  .acts {
    display: flex;
    gap: 6px;
    margin-left: auto;
  }
  .sub {
    flex-basis: 100%;
    margin: 0;
  }
</style>
