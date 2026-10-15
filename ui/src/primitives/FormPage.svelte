<script lang="ts">
  /**
   * Layout of a page form (> 12 fields: container, route, image — SPEC §17.0): numbered FormSection cards on the left,
   * a sticky aside on the right with jump links to the sections ("On this page"), a summary and the validation list
   * (pattern from Komodo / Rancher). Under 960 px the aside moves under the form.
   */
  import type { Snippet } from 'svelte';
  interface Props {
    sections?: { id: string; label: string; badge?: string | number | null; tone?: 'red' | 'amber' | null }[];
    aside?: Snippet;
    children: Snippet;
    testid?: string;
  }
  let { sections = [], aside, children, testid }: Props = $props();
  function jump(e: MouseEvent, id: string) {
    const el = document.getElementById(id);
    if (!el) return;
    e.preventDefault();
    el.scrollIntoView({ behavior: 'smooth', block: 'start' });
    (el.querySelector('input,button,[tabindex]') as HTMLElement | null)?.focus({ preventScroll: true });
  }
</script>

<div class="fpage" data-testid={testid}>
  <div class="main">{@render children()}</div>
  <aside class="side">
    {#if sections.length}
      <nav class="card toc" aria-label="On this page">
        <div class="toc-h">On this page</div>
        {#each sections as s (s.id)}
          <a href="#{s.id}" onclick={(e) => jump(e, s.id)} data-testid="toc-{s.id}">{s.label}{#if s.badge}<span class="badge {s.tone === 'red' ? 'red bad' : s.tone === 'amber' ? 'amber warn' : ''} tb">{s.badge}</span>{/if}</a>
        {/each}
      </nav>
    {/if}
    {#if aside}{@render aside()}{/if}
  </aside>
</div>

<style>
  .fpage {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 280px;
    gap: var(--sp-5);
    align-items: start;
  }
  .main {
    display: flex;
    flex-direction: column;
    gap: var(--sp-4);
    min-width: 0;
  }
  .side {
    position: sticky;
    top: calc(var(--topbar-h, 52px) + 12px);
    display: flex;
    flex-direction: column;
    gap: var(--sp-3);
    max-height: calc(100vh - var(--topbar-h, 52px) - 24px);
    overflow-y: auto;
    min-width: 0;
  }
  .toc {
    display: flex;
    flex-direction: column;
    padding: 8px;
  }
  .toc-h {
    font-size: 10.5px;
    font-weight: var(--fw-semibold);
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-3);
    padding: 4px 8px 6px;
  }
  .toc a {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 5px 8px;
    border-radius: var(--r-sm);
    color: var(--text-2);
    font-size: var(--fs-sm);
    text-decoration: none;
  }
  .toc a:hover {
    background: var(--surface-hover);
    color: var(--text);
  }
  .tb {
    margin-left: auto;
  }
  @media (max-width: 960px) {
    .fpage {
      grid-template-columns: minmax(0, 1fr);
    }
    .side {
      position: static;
      max-height: none;
      overflow: visible;
    }
    .toc {
      display: none;
    }
  }
</style>
