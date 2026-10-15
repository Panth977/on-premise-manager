<script lang="ts">
  /**
   * Gate of every dev tools page (§21 #97): "*" → choose a site (a connection lives on one site); loads devtoolsInfo +
   * the connections of the site; agent.json tools.devTools = false → how to enable it; errors with Retry.
   */
  import type { Snippet } from 'svelte';
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import type { DevToolsInfo } from '@opm/shared/devtools';
  import ChooseSite from '$lib/ui/ChooseSite.svelte';
  import { appHref } from '$lib/nav';
  import Icon from '$lib/ui/Icon.svelte';
  import { devStore } from './state.svelte';

  let { children, what = 'Dev tools' }: { children: Snippet<[DevToolsInfo, string]>; what?: string } = $props();
  const slug = $derived(page.params.siteSlug ?? '*');
  $effect(() => {
    const s = slug;
    if (s !== '*') untrack(() => devStore.load(s));
  });
  const snippet = '"tools": { "devTools": true }';
</script>

{#if slug === '*'}
  <ChooseSite {what} text="A dev tools connection lives on one site: its queries run on that site's server. Pick the site." />
{:else if devStore.site === slug && devStore.disabledReason}
  <section class="card off" data-testid="devtools-disabled">
    <span class="tile" aria-hidden="true"><Icon name="lock" size={20} /></span>
    <h2>Dev tools are switched off on <span class="mono">{slug}</span></h2>
    <p class="muted">{devStore.disabledReason}</p>
    <div class="how">
      <p>To enable them, set the switch in the agent configuration of that server and restart the agent:</p>
      <ol>
        <li>Turn on <strong>Dev tools</strong> in <a href="{appHref(slug, 'agent')}#set-devtools" data-testid="devtools-agent-link">Agent › agent.json settings</a> ("Save and restart"), or edit the file by hand:</li>
        <li>Open <code data-testid="devtools-config-path">{devStore.info?.configPath ?? '<agent home>/agent.json'}</code>.</li>
        <li>Set <code data-testid="devtools-enable-json">{snippet}</code> (the default is on).</li>
        <li>Restart the agent (<code>sudo systemctl restart opm-agent</code>).</li>
      </ol>
      <p class="hint">Every query run here is logged to the site's activities (redacted, no result data); presets come from the cloud configuration.</p>
    </div>
  </section>
{:else if devStore.site === slug && devStore.error && !devStore.loaded}
  <div class="banner bad" data-testid="devtools-error">
    <Icon name="alert" />
    <div class="grow">Dev tools of <span class="mono">{slug}</span> cannot be loaded: {devStore.error}</div>
    <button class="btn sm" onclick={() => devStore.load(slug, true)}>Retry</button>
  </div>
{:else if devStore.site === slug && devStore.loaded && devStore.info}
  {@render children(devStore.info, slug)}
{:else}
  <div class="stack" aria-busy="true">
    <span class="skel" style="width:260px;height:26px"></span>
    <span class="skel" style="width:100%;height:340px"></span>
  </div>
{/if}

<style>
  .off {
    padding: 32px 28px;
    display: flex;
    flex-direction: column;
    gap: 8px;
    max-width: 760px;
  }
  .tile {
    width: 44px;
    height: 44px;
    border-radius: var(--r-lg);
    display: grid;
    place-items: center;
    background: var(--amber-soft);
    color: var(--amber-text);
  }
  h2 {
    margin: 4px 0 0;
    font-size: var(--fs-xl);
  }
  .how {
    border-top: 1px solid var(--border);
    margin-top: 8px;
    padding-top: 12px;
  }
  .how ol {
    margin: 8px 0;
    padding-left: 20px;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .how p {
    margin: 0;
  }
</style>
