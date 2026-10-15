<script lang="ts">
  /**
   * nav2 (SPEC §21 #97): a module that works on ONE site while "All sites" (*) is selected — "Choose a site to see X" +
   * an inline chooser with the same options as the top selector. Picking one changes the selection (URL) for every module.
   */
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { overview } from '$lib/state/app.svelte';
  import { switchSiteHref } from '$lib/nav';
  import { siteOptions } from '$lib/siteOptions';
  import Select from './Select.svelte';
  import Icon from './Icon.svelte';

  let { what, text }: { what: string; text?: string } = $props();
  const opts = $derived(siteOptions(overview.data));
  const self = $derived(overview.data?.self);
  function pick(v: string) {
    goto(switchSiteHref(page.url.pathname, page.url.search, v));
  }
</script>

<section class="card choose" data-testid="choose-site">
  <span class="tile" aria-hidden="true"><Icon name="server" size={20} /></span>
  <h2>Choose a site to see {what}</h2>
  <p class="muted">{text ?? `"All sites" is selected, and ${what} belong to one site.`}</p>
  <div class="row pick">
    <div class="sel"><Select value={null} placeholder="Choose a site…" options={opts} onchange={pick} ariaLabel="Site" testid="choose-site-select" /></div>
    {#if self}<button class="btn" onclick={() => pick(self)} data-testid="choose-this-site"><Icon name="server" size={14} />This site · <span class="mono">{self}</span></button>{/if}
  </div>
</section>

<style>
  .choose {
    display: flex;
    flex-direction: column;
    align-items: center;
    text-align: center;
    gap: 8px;
    padding: 40px 20px;
    border-style: dashed;
  }
  .tile {
    width: 44px;
    height: 44px;
    border-radius: var(--r-lg);
    display: grid;
    place-items: center;
    background: var(--accent-soft);
    color: var(--accent-text);
    margin-bottom: 4px;
  }
  h2 {
    margin: 0;
    font-size: var(--fs-lg);
  }
  p {
    margin: 0 0 8px;
    max-width: 460px;
  }
  .pick {
    flex-wrap: wrap;
    justify-content: center;
    gap: 8px;
  }
  .sel {
    min-width: 220px;
  }
</style>
