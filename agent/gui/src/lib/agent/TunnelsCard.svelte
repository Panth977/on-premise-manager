<script lang="ts">
  /**
   * §21 #125 port tunnels of a site: ports reachable at `<cloud host>:<public port>` (raw TCP). Added and removed in
   * the cloud console; each is off on its site until switched on here. Switching ON through the cloud (a remote GUI
   * session) is refused — the site decides; switching off works from anywhere. The default "Agent GUI" tunnel is the
   * remote GUI switch (§21 #118): shown here as status, switched in the Remote GUI card.
   */
  import { onMount } from 'svelte';
  import type { SitePortTunnel, SitePortTunnelsResponse } from '@opm/shared/tunnels';
  import { portTunnelTargetText } from '@opm/shared/tunnels';
  import { api, errMessage } from '$lib/api';
  import { consoleOrigin } from '$lib/cloud';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toast.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Time from '$lib/ui/Time.svelte';

  /** §21 #121 any site: this one in process, another over the mesh */
  let { site }: { site: string } = $props();
  let v = $state<SitePortTunnelsResponse | null>(null);
  let err = $state<string | null>(null);
  let busy = $state<string | null>(null);
  const remote = $derived(!!session.session?.remote);
  /** the cloud host the tunnels listen on (the console's host), when known */
  const host = $derived.by(() => {
    const o = consoleOrigin(session.info);
    return o ? new URL(o).hostname : null;
  });
  async function load() {
    try {
      v = await api.sitePortTunnels({ siteSlug: site });
      err = null;
    } catch (e) {
      err = errMessage(e);
    }
  }
  $effect(() => {
    void site;
    v = null;
    err = null;
    void load();
  });
  onMount(() => {
    const t = setInterval(() => !document.hidden && !busy && void load(), 20_000);
    return () => clearInterval(t);
  });
  async function set(t: SitePortTunnel, enabled: boolean) {
    busy = t.tunnelId;
    try {
      const x = await api.siteSetPortTunnel({ siteSlug: site, tunnelId: t.tunnelId, body: { enabled } });
      if (v) v = { ...v, items: v.items.map((y) => (y.tunnelId === x.tunnelId ? x : y)) };
      toasts.ok(enabled ? 'Tunnel switched on' : 'Tunnel switched off', `${portTunnelTargetText(t.target)} · cloud port ${t.publicPort}`);
    } catch (e) {
      toasts.err(e, 'Not changed');
    } finally {
      busy = null;
    }
  }
</script>

<section class="card card-pad tc" data-testid="tunnels-card">
  <h3><Icon name="network" size={15} />Tunnels</h3>
  <p class="small muted">Ports of this site reachable at the cloud host (raw TCP). Added and removed in the cloud console; each stays off until switched on here.</p>
  {#if err}<p class="small bad-text" data-testid="tunnels-error">{err}</p>{/if}
  {#if v}
    {#if !v.cloudTunnels}
      <p class="small muted" data-testid="tunnels-none-cloud">This cloud does not carry port tunnels.</p>
    {:else}
      {#if remote}<p class="small warn-text" data-testid="tunnels-remote-hint">You work through the cloud: switch tunnels on on the site itself. Switching off works here.</p>{/if}
      <ul class="rows">
        <li class="trow" data-testid="tunnel-agent-gui">
          <div class="main">
            <div class="what"><b>Agent GUI</b> <span class="faint small">default tunnel</span></div>
            <div class="tiny muted">This GUI opened from the cloud console — switched in <b>Remote GUI through the cloud</b>.</div>
          </div>
          <div class="side"><span class="badge {v.remoteGui ? 'ok' : 'outline'}" data-testid="tunnel-agent-gui-state">{v.remoteGui ? 'on' : 'off'}</span></div>
        </li>
        {#each v.items as t (t.tunnelId)}
          <li class="trow" data-testid="tunnel-row" data-tunnel={t.tunnelId}>
            <div class="main">
              <div class="what">
                {#if t.label}<b>{t.label}</b>{/if}
                <span class="mono small">{portTunnelTargetText(t.target)}</span>
              </div>
              <div class="tiny muted">
                <span data-testid="tunnel-address">{#if host}<span class="mono">{host}:{t.publicPort}</span>{:else}cloud port <span class="mono">{t.publicPort}</span>{/if}</span>
                · <span data-testid="tunnel-conns">{t.openConnections} open</span>
                · last connection {#if t.lastConnectionAt}<Time ts={t.lastConnectionAt} />{:else}never{/if}
              </div>
              {#if t.lastError}<div class="tiny bad-text err" data-testid="tunnel-error">{t.lastError}</div>{/if}
            </div>
            <div class="side">
              <span class="badge {t.enabled ? 'ok' : 'outline'}" data-testid="tunnel-state">{t.enabled ? 'on' : 'off'}</span>
              {#if t.enabled}
                <Btn size="sm" icon="x" busy={busy === t.tunnelId} disabled={!!busy && busy !== t.tunnelId} onclick={() => set(t, false)} data-testid="tunnel-off">Switch off</Btn>
              {:else}
                <Btn size="sm" icon="check" busy={busy === t.tunnelId} disabled={remote || (!!busy && busy !== t.tunnelId)} title={remote ? 'switch tunnels on on the site itself' : ''} onclick={() => set(t, true)} data-testid="tunnel-on">Switch on</Btn>
              {/if}
            </div>
          </li>
        {/each}
      </ul>
      {#if !v.items.length}<p class="small muted" data-testid="tunnels-empty">No tunnels yet — add one in the cloud console (Tunnels).</p>{/if}
    {/if}
  {:else if !err}
    <p class="small muted"><span class="spin" aria-hidden="true"></span> Loading…</p>
  {/if}
</section>

<style>
  .tc {
    display: flex;
    flex-direction: column;
    gap: 10px;
    min-width: 0;
  }
  .tc h3 {
    display: flex;
    align-items: center;
    gap: 7px;
    font-size: var(--fs-md);
    margin: 0;
  }
  .tc h3 :global(svg) {
    color: var(--accent-text);
  }
  .tc p {
    margin: 0;
  }
  .rows {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
  }
  .trow {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 8px 14px;
    padding: 10px 12px;
    min-width: 0;
  }
  .trow + .trow {
    border-top: 1px solid var(--border);
  }
  .main {
    display: flex;
    flex-direction: column;
    gap: 3px;
    min-width: 0;
    flex: 1 1 260px;
  }
  .what {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 4px 8px;
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .err {
    overflow-wrap: anywhere;
  }
  .side {
    display: flex;
    align-items: center;
    gap: 8px;
  }
</style>
