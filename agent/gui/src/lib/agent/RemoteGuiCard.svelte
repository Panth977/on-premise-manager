<script lang="ts">
  /**
   * §21 #118 "Remote GUI through the cloud": whether people with access in the cloud console may open this site's agent
   * GUI from the cloud (default on; the site asks the cloud for their requests — nothing connects to the site), and who
   * is working that way right now. Switching it off is possible only here on the site (not through the cloud).
   */
  import type { RemoteGuiSetting } from '@opm/shared/remote-gui';
  import { api, errMessage } from '$lib/api';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toast.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Time from '$lib/ui/Time.svelte';

  /** §21 #121 any site: this one in process, another over the mesh */
  let { site }: { site: string } = $props();
  let s = $state<RemoteGuiSetting | null>(null);
  let err = $state<string | null>(null);
  let busy = $state(false);
  const remote = $derived(!!session.session?.remote);
  async function load() {
    try {
      s = await api.siteGetRemoteGui({ siteSlug: site });
      err = null;
    } catch (e) {
      err = errMessage(e);
    }
  }
  $effect(() => {
    void site;
    s = null;
    void load();
  });
  async function set(enabled: boolean) {
    busy = true;
    try {
      s = await api.sitePutRemoteGui({ siteSlug: site, body: { enabled } });
      toasts.ok(enabled ? 'Remote GUI switched on' : 'Remote GUI switched off', enabled ? 'People with access in the cloud console can open this GUI from there.' : 'Open remote sessions ended; the cloud cannot open this GUI any more.');
    } catch (e) {
      toasts.err(e, 'Not changed');
    } finally {
      busy = false;
    }
  }
</script>

<section class="card card-pad c" data-testid="remote-gui-card">
  <h3><Icon name="globe" size={15} />Remote GUI through the cloud</h3>
  <p class="small muted">People with access to this deployment in the cloud console can open this GUI from there (Status › Open agent GUI), working as their cloud account. The site asks the cloud for their requests — nothing connects to this server. The terminal works through the cloud too; live log follow only on the site itself.</p>
  {#if err}<p class="small bad-text">{err}</p>{/if}
  {#if s}
    <p class="small" data-testid="remote-gui-state">Status: <b class={s.enabled ? 'ok-text' : 'warn-text'}>{s.enabled ? 'on' : 'off'}</b>{#if s.sessions?.length}<span> · working through the cloud now: {#each s.sessions as x, k (x.email)}{k ? ', ' : ''}<span class="mono">{x.email}</span> (since <Time ts={x.since} />){/each}</span>{/if}</p>
    <div class="row">
      {#if s.enabled}
        <Btn icon="x" {busy} disabled={remote} title={remote ? 'switch it off on a site itself, not through the cloud' : ''} onclick={() => set(false)} data-testid="remote-gui-off">Switch off</Btn>
      {:else}
        <Btn icon="check" {busy} disabled={remote} onclick={() => set(true)} data-testid="remote-gui-on">Switch on</Btn>
      {/if}
    </div>
  {/if}
</section>
