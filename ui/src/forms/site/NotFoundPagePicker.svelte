<script lang="ts">
  /**
   * Not-found page of a site — ONE picker for both GUIs (§21 #102): a radio group of live thumbnails (sandboxed
   * iframes, loaded lazily) — "None" = the built-in agent page — plus every page the host offers (cloud: the library;
   * agent: the pages in the head catalog). Test ids: nf-picker · nf-opt-none · nf-opt-<notFoundPageId>.
   */
  import { useConfigHost } from '../host';
  type Props = { value: string | null; disabled?: boolean; noneLabel?: string; labelledby?: string; onchange: (v: string | null) => void; testid?: string };
  let { value, disabled = false, noneLabel = 'Built-in page', labelledby, onchange, testid = 'nf-picker' }: Props = $props();
  const host = useConfigHost();
  const pages = $derived(host.notFoundPages?.() ?? []);
  let html = $state<Record<string, string | null>>({});
  $effect(() => {
    for (const p of pages) {
      if (p.notFoundPageId in html) continue;
      const r = host.notFoundPageHtml?.(p.notFoundPageId) ?? null;
      if (r instanceof Promise) {
        html[p.notFoundPageId] = null;
        r.then((h) => (html[p.notFoundPageId] = h));
      } else html[p.notFoundPageId] = r;
    }
  });
  const missing = $derived(!!value && !pages.some((p) => p.notFoundPageId === value));
</script>

<div class="nfp" role="radiogroup" aria-labelledby={labelledby} data-testid={testid}>
  <label class="nfo" class:sel={!value} class:dis={disabled} data-testid="nf-opt-none">
    <input type="radio" name="nfp-{testid}" class="sr-only" checked={!value} {disabled} onchange={() => onchange(null)} />
    <span class="thumb builtin"><span>404</span></span>
    <span class="nfl">{noneLabel}<span class="tiny muted">built into the agent</span></span>
  </label>
  {#each pages as p (p.notFoundPageId)}
    <label class="nfo" class:sel={value === p.notFoundPageId} class:dis={disabled} data-testid="nf-opt-{p.notFoundPageId}">
      <input type="radio" name="nfp-{testid}" class="sr-only" checked={value === p.notFoundPageId} {disabled} onchange={() => onchange(p.notFoundPageId)} />
      <span class="thumb">{#if html[p.notFoundPageId]}<iframe title="Preview of {p.label}" sandbox="allow-scripts" srcdoc={html[p.notFoundPageId]} tabindex="-1" loading="lazy"></iframe>{/if}</span>
      <span class="nfl ellipsis">{p.label}</span>
    </label>
  {/each}
  {#if missing}<div class="field-warn">The chosen page ({value}) is not available here.</div>{/if}
</div>

<style>
  .nfp {
    display: flex;
    flex-wrap: wrap;
    gap: 10px;
  }
  .nfo {
    display: flex;
    flex-direction: column;
    gap: 6px;
    width: 160px;
    padding: 6px;
    border: 1px solid var(--border-strong);
    border-radius: var(--r-md);
    cursor: pointer;
    background: var(--surface);
    min-width: 0;
  }
  .nfo.dis {
    cursor: not-allowed;
    opacity: 0.75;
  }
  .nfo:focus-within {
    box-shadow: var(--focus-ring);
  }
  .nfo.sel {
    border-color: var(--accent);
    background: var(--accent-soft);
  }
  .thumb {
    position: relative;
    display: block;
    height: 88px;
    border-radius: 5px;
    overflow: hidden;
    background: #fff;
    border: 1px solid var(--border);
  }
  .thumb iframe {
    position: absolute;
    top: 0;
    left: 0;
    width: 400%;
    height: 400%;
    border: 0;
    transform: scale(0.25);
    transform-origin: 0 0;
    pointer-events: none;
  }
  .thumb.builtin {
    display: grid;
    place-items: center;
    background: var(--gray-soft);
    color: var(--text-2);
    font-weight: 700;
    font-size: 22px;
  }
  .nfl {
    display: flex;
    flex-direction: column;
    font-size: 12.5px;
    font-weight: var(--fw-semibold);
  }
  .ellipsis {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  @media (max-width: 480px) {
    .nfo {
      width: calc(50% - 5px);
    }
  }
</style>
