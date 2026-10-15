<script lang="ts">
  /**
   * Service / template / tag icon from the revision catalog (types.ts Icon), rendered offline:
   *   null / "upload:<id>" (not embedded) / unknown Lucide name → DEFAULT_ICONS[type]
   *   "lucide:<name>" or legacy bare "<name>" → curated Lucide SVG (shared/src/icons.ts, no CDN)
   *   "data:image/…" (uploads embedded in Revision.catalog) or "https://…" → <img>
   * The agent GUI never edits these icons (cloud only), so there is no picker here.
   */
  import { DEFAULT_ICONS, lucideSvg } from '@opm/shared/icons';
  interface Props {
    icon: string | null | undefined;
    type?: keyof typeof DEFAULT_ICONS;
    size?: number;
    label?: string;
    tile?: boolean;
  }
  let { icon, type = 'service', size = 16, label, tile = false }: Props = $props();
  const r = $derived.by((): { kind: 'img'; src: string } | { kind: 'svg'; name: string; svg: string; fallback: boolean } => {
    const v = icon ?? '';
    if (v.startsWith('data:image/') || v.startsWith('https://')) return { kind: 'img', src: v };
    const name = v.startsWith('lucide:') ? v.slice(7) : /^[a-z0-9-]+$/.test(v) ? v : '';
    const svg = name ? lucideSvg(name, size) : null;
    if (svg) return { kind: 'svg', name, svg, fallback: false };
    const d = DEFAULT_ICONS[type];
    return { kind: 'svg', name: d, svg: lucideSvg(d, size) ?? '', fallback: true };
  });
</script>

{#if r.kind === 'img'}
  <img class="ei" class:tile src={r.src} alt={label ?? ''} width={size} height={size} data-icon-kind="image" style:--s="{size}px" />
{:else}
  <span class="ei" class:tile role={label ? 'img' : undefined} aria-label={label} aria-hidden={label ? undefined : 'true'} data-icon-kind={r.fallback ? 'default' : 'lucide'} data-icon={r.name} style:--s="{size}px">{@html r.svg}</span>
{/if}

<style>
  .ei {
    flex: none;
    display: inline-grid;
    place-items: center;
    width: var(--s);
    height: var(--s);
    vertical-align: -0.2em;
  }
  img.ei {
    object-fit: contain;
    border-radius: 3px;
  }
  .ei :global(svg) {
    display: block;
  }
  .tile {
    box-sizing: content-box;
    padding: calc(var(--s) * 0.42);
    border-radius: calc(var(--s) * 0.5);
    background: var(--accent-soft);
    color: var(--accent-text);
  }
  img.tile {
    background: var(--gray-soft);
  }
</style>
