<script lang="ts" module>
  import type { IconNode } from './icons';
  import { DEFAULT_ICONS } from '@opm/shared/icons';
  export type IconType = keyof typeof DEFAULT_ICONS;

  let all: Record<string, IconNode> | null = null;
  let loading: Promise<Record<string, IconNode>> | null = null;
  /** The full lucide set (lazy chunk) — only for legacy names outside the curated set. */
  export function loadAllLucide() {
    loading ??= import('lucide').then((m) => (all = m.icons as unknown as Record<string, IconNode>));
    return loading;
  }
  export const pascal = (s: string) =>
    s
      .split('-')
      .filter(Boolean)
      .map((p) => p[0]!.toUpperCase() + p.slice(1))
      .join('');
  export function lucideNodeSync(name: string): IconNode | null {
    return all ? (all[pascal(name)] ?? null) : null;
  }
</script>

<script lang="ts">
  /**
   * Renders any Icon value (shared types.ts Icon):
   *   null / ""        → DEFAULT_ICONS[type] (or initials of `initials` when given — avatars)
   *   lucide:<n> / <n> → curated Lucide SVG (shared/icons.ts), fallback: the full lucide set (lazy)
   *   upload:<id>      → <img> of the uploaded icon (public content URL; mock: data URL)
   *   https:// / data: → <img>
   */
  import { lucideSvg } from '@opm/shared/icons';
  import { parseIcon } from '@opm/shared';
  import Icon from './Icon.svelte';
  import { Box } from './icons';
  import { initials as ini, hueOf } from '../util/format';
  import { iconSrcSync, resolveIconSrc } from './icon-src';

  type Props = {
    value: string | null | undefined;
    /** entity type → default icon when value is null */
    type?: IconType;
    size?: number;
    class?: string;
    /** accessible label (otherwise decorative) */
    label?: string;
    /** person/entity name: null value renders initials in a tinted circle (avatars) */
    initials?: string;
    /** round frame (avatars) */
    round?: boolean;
  };
  let { value, type = 'service', size = 18, class: cls = '', label, initials, round = false }: Props = $props();

  const parsed = $derived(parseIcon(value ?? null));
  const lucideName = $derived(parsed?.kind === 'lucide' ? parsed.name : !parsed && !initials ? DEFAULT_ICONS[type] : null);
  const svg = $derived(lucideName ? lucideSvg(lucideName, size, 2) : null);
  let fallbackNode = $state<IconNode | null>(null);
  $effect(() => {
    const n = lucideName;
    if (!n || svg) return;
    const sync = lucideNodeSync(n);
    if (sync) {
      fallbackNode = sync;
      return;
    }
    loadAllLucide().then(() => {
      if (lucideName === n) fallbackNode = lucideNodeSync(n);
    });
  });

  let src = $state<string | null>(null);
  let broken = $state(false);
  $effect(() => {
    const p = parsed;
    broken = false;
    if (p?.kind === 'url') src = p.url;
    else if (p?.kind === 'upload') {
      const id = p.id;
      src = iconSrcSync(id);
      if (!src)
        resolveIconSrc(id).then((u) => {
          if (parsed?.kind === 'upload' && parsed.id === id) {
            src = u;
            if (!u) broken = true;
          }
        });
    } else src = null;
  });
  const isImg = $derived((parsed?.kind === 'upload' || parsed?.kind === 'url') && !broken);
  const showInitials = $derived(!!initials && (!parsed || broken));
</script>

{#if showInitials}
  <span class="iv ini {cls}" class:round style="width:{size}px;height:{size}px;font-size:{Math.max(9, Math.round(size * 0.4))}px;--h:{hueOf(initials ?? '')}" role={label ? 'img' : undefined} aria-label={label} aria-hidden={label ? undefined : 'true'}>{ini(initials)}</span>
{:else if isImg}
  {#if src}
    <img class="iv img {cls}" class:round {src} alt={label ?? ''} width={size} height={size} style="width:{size}px;height:{size}px" onerror={() => (broken = true)} referrerpolicy="no-referrer" />
  {:else}
    <span class="iv ph {cls}" class:round style="width:{size}px;height:{size}px" aria-hidden="true"></span>
  {/if}
{:else if svg}
  <span class="iv svg {cls}" class:round style="width:{size}px;height:{size}px" role={label ? 'img' : undefined} aria-label={label} aria-hidden={label ? undefined : 'true'}>{@html svg}</span>
{:else}
  <Icon icon={fallbackNode ?? Box} {size} class={cls} {label} />
{/if}

<style>
  .iv {
    flex: none;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    vertical-align: middle;
    line-height: 0;
  }
  .svg :global(svg) {
    display: block;
  }
  .img {
    object-fit: contain;
    border-radius: 3px;
  }
  .ph {
    border-radius: 3px;
    background: var(--skeleton);
  }
  .round {
    border-radius: 50%;
  }
  .img.round {
    object-fit: cover;
  }
  .ini {
    font-weight: 600;
    background: hsl(var(--h) 65% 92%);
    color: hsl(var(--h) 45% 32%);
    letter-spacing: 0.02em;
    border-radius: 6px;
    line-height: 1;
  }
  .ini.round {
    border-radius: 50%;
  }
  :global([data-theme='dark']) .ini {
    background: hsl(var(--h) 35% 22%);
    color: hsl(var(--h) 70% 80%);
  }
</style>
