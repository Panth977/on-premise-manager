<!--
  ⓘ tooltip (round T): replaces help paragraphs with a one-line hint + this button. The text lives in a
  popover="manual" element in the top layer (never clipped by tables / cards), shown on hover, focus or tap;
  Escape closes it. The text stays in the DOM (screen readers via aria-describedby, tests via textContent).
-->
<script lang="ts">
  import type { Snippet } from 'svelte';
  import { showPop, hidePop } from './popover';
  import Icon from './Icon.svelte';

  let { text = '', label = 'More information', testid, size = 14, children }: { text?: string; label?: string; testid?: string; size?: number; children?: Snippet } = $props();
  const id = `tip-${Math.random().toString(36).slice(2, 9)}`;
  let btn = $state<HTMLButtonElement>();
  let pop = $state<HTMLElement>();
  let pinned = $state(false);

  function place() {
    if (!btn || !pop) return;
    const r = btn.getBoundingClientRect();
    const vw = window.innerWidth;
    const w = Math.min(340, vw - 16);
    pop.style.maxWidth = `${w}px`;
    const pw = Math.min(pop.offsetWidth || w, w);
    pop.style.left = `${Math.max(8, Math.min(r.left + r.width / 2 - pw / 2, vw - pw - 8))}px`;
    const ph = pop.offsetHeight || 60;
    pop.style.top = `${r.bottom + 6 + ph > window.innerHeight - 8 ? Math.max(8, r.top - 6 - ph) : r.bottom + 6}px`;
  }
  function open() {
    showPop(pop);
    place();
    requestAnimationFrame(place);
  }
  function close() {
    if (!pinned) hidePop(pop);
  }
  function toggle(e: MouseEvent) {
    e.preventDefault();
    e.stopPropagation();
    pinned = !pinned;
    if (pinned) open();
    else hidePop(pop);
  }
  $effect(() => {
    if (!pinned) return;
    const out = (e: PointerEvent) => {
      if (btn && !btn.contains(e.target as Node) && pop && !pop.contains(e.target as Node)) {
        pinned = false;
        hidePop(pop);
      }
    };
    const f = () => place();
    document.addEventListener('pointerdown', out, true);
    window.addEventListener('scroll', f, true);
    return () => {
      document.removeEventListener('pointerdown', out, true);
      window.removeEventListener('scroll', f, true);
    };
  });
</script>

<button
  type="button"
  class="infotip"
  bind:this={btn}
  aria-label={label}
  aria-describedby={id}
  aria-expanded={pinned}
  data-testid={testid}
  onmouseenter={open}
  onmouseleave={close}
  onfocus={open}
  onblur={close}
  onclick={toggle}
  onkeydown={(e) => {
    if (e.key === 'Escape') {
      pinned = false;
      hidePop(pop);
    }
  }}><Icon name="info" {size} /></button
><span class="tip" role="tooltip" {id} popover="manual" bind:this={pop} data-testid={testid ? `${testid}-text` : undefined}>{#if children}{@render children()}{:else}{text}{/if}</span>

<style>
  .infotip {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    vertical-align: middle;
    padding: 2px;
    margin: -2px 0;
    border: 0;
    border-radius: 999px;
    background: none;
    color: var(--text-3);
    cursor: help;
    line-height: 0;
  }
  .infotip:hover,
  .infotip[aria-expanded='true'] {
    color: var(--accent);
  }
  .infotip:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 1px;
  }
  .tip {
    position: fixed;
    inset: auto;
    margin: 0;
    padding: 8px 10px;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    background: var(--surface);
    color: var(--text);
    box-shadow: var(--shadow-lg);
    font-size: var(--fs-sm);
    font-weight: var(--fw-normal, 400);
    line-height: 1.45;
    text-transform: none;
    letter-spacing: normal;
    white-space: normal;
    width: max-content;
    max-width: 340px;
  }
</style>
