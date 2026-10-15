<script lang="ts">
  import type { Snippet } from 'svelte';
  import Icon from './Icon.svelte';
  import IconView, { type IconType } from './IconView.svelte';
  import { ArrowLeft, Lock, type IconNode } from './icons';
  type Props = {
    title: string;
    subtitle?: string;
    icon?: IconNode;
    /** an Icon value (lucide:/upload:/legacy); with iconType, null shows the default icon of that type */
    iconName?: string | null;
    iconType?: IconType;
    back?: { href: string; label: string };
    readOnly?: boolean;
    actions?: Snippet;
    meta?: Snippet;
    children?: Snippet;
  };
  let { title, subtitle, icon, iconName, iconType, back, readOnly = false, actions, meta, children }: Props = $props();
</script>

<header class="ph">
  {#if back}<a class="back" href={back.href}><Icon icon={ArrowLeft} size={14} />{back.label}</a>{/if}
  <div class="top">
    {#if icon || iconName || iconType}
      <div class="pic">{#if iconName || iconType}<IconView value={iconName} type={iconType} size={20} />{:else if icon}<Icon {icon} size={20} />{/if}</div>
    {/if}
    <div class="grow tt">
      <div class="row wrap">
        <h1 data-testid="page-title">{title}</h1>
        {#if readOnly}<span class="badge outline" data-testid="read-only"><Icon icon={Lock} size={11} />read only</span>{/if}
        {@render meta?.()}
      </div>
      {#if subtitle}<p class="sub">{subtitle}</p>{/if}
      {@render children?.()}
    </div>
    {#if actions}<div class="acts">{@render actions()}</div>{/if}
  </div>
</header>

<style>
  .ph {
    margin-bottom: var(--sp-5);
  }
  .back {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    color: var(--text-2);
    font-size: var(--fs-sm);
    margin-bottom: 8px;
  }
  .back:hover {
    color: var(--text);
    text-decoration: none;
  }
  .top {
    display: flex;
    align-items: flex-start;
    gap: var(--sp-3);
    flex-wrap: wrap;
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
    min-width: 200px;
  }
  .sub {
    color: var(--text-2);
    margin-top: 4px;
    max-width: 760px;
  }
  .acts {
    display: flex;
    gap: var(--sp-2);
    flex-wrap: wrap;
    align-items: center;
    margin-left: auto;
  }
</style>
