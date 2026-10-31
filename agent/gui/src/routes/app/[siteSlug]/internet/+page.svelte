<script lang="ts">
  import { api } from '$lib/api';
  import { Resource, poll } from '$lib/state/resource.svelte';
  import { overview } from '$lib/state/app.svelte';
  import { tunnel } from '$lib/state/tunnel.svelte';
  import { toasts } from '$lib/state/toast.svelte';
  import { bytes } from '$lib/format';
  import { routeLabel } from '$lib/ui/tones';
  import PageHead from '$lib/ui/PageHead.svelte';
  import Load from '$lib/ui/Load.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Time from '$lib/ui/Time.svelte';
  import Confirm from '$lib/ui/Confirm.svelte';

  const ts = new Resource(() => api.tunnelState({}));
  $effect(() => poll(() => ts.load(), 3000));
  let busy = $state(false);
  let revokeOpen = $state(false);

  const lease = $derived(ts.data?.lease ?? null);
  const encrypted = $derived(ts.data?.encrypted !== false);
  // the agent refuses a call (no verifiable cloud key, a tampered answer) without this tab noticing: its error comes first
  const lastError = $derived(lease?.lastError ?? tunnel.lastError);
  const mine = $derived(tunnel.active && !!lease && lease.leaseId === tunnel.lease?.leaseId);
  const other = $derived(!!lease && !mine && lease.status === 'active');
  const noRoute = $derived((overview.data?.sites ?? []).filter((s) => s.reachable && s.cloudRoute === 'none'));

  async function start() {
    busy = true;
    try {
      await tunnel.start();
      toasts.ok('This tab gives internet now', 'Keep it open. Closing it ends the tunnel after 30 s.');
      ts.load();
    } catch (e) {
      toasts.err(e, 'Could not start the tunnel');
      ts.load();
    } finally {
      busy = false;
    }
  }
  async function stop() {
    busy = true;
    try {
      await tunnel.stop();
      toasts.ok('Tunnel stopped');
    } catch (e) {
      toasts.err(e, 'Stop failed');
    } finally {
      busy = false;
      ts.load();
    }
  }
  async function revoke() {
    if (!lease) return;
    try {
      await api.tunnelRevoke({ body: { leaseId: lease.leaseId } });
      toasts.ok('The other tab was revoked', 'You can give internet from this tab now.');
      await ts.load();
    } catch (e) {
      toasts.err(e, 'Revoke failed');
      return false;
    }
  }
  const stepTone = { fetching: 'info', sending: 'info', chunks: 'info', done: 'ok', failed: 'bad' } as const;
</script>

<PageHead icon="globe" title="Internet from this browser" sub="When no site reaches the cloud (no direct route, no relay), this browser tab can carry the agents' cloud calls: polls, heartbeats, reports, activities and downloads. Uploads wait for a real route." />

<div class="banner {encrypted ? '' : 'warn'}" style="margin-bottom:16px" data-testid="tunnel-security">
  <Icon name="shield" />
  {#if encrypted}
    <div class="grow">Traffic is end-to-end encrypted between the agent and the cloud: this tab carries ciphertext only, also for commits with secret values. Keep it open; closing it ends the tunnel after 30 s. One tab per deployment can give internet.</div>
  {:else}
    <div class="grow">This tunnel is not encrypted: payloads (also commits with secret values) pass through this tab in clear. Set <code>releasePublicKey</code> in agent.json and upgrade the holding agent. Keep the tab open; closing it ends the tunnel after 30 s. One tab per deployment can give internet.</div>
  {/if}
</div>

<Load loading={ts.loading} loaded={ts.loaded} error={ts.error} retry={() => ts.load()}>
  {#if tunnel.ended && !tunnel.active}
    <div class="banner {tunnel.ended.startsWith('Stopped') ? '' : 'bad'} small" style="margin-bottom:12px"><Icon name="info" /><div class="grow">{tunnel.ended}</div></div>
  {/if}

  {#if mine}
    <section class="card live" data-testid="tunnel-active">
      <div class="card-head">
        <div class="row"><span class="pulse" aria-hidden="true"></span><h2>Active in this tab</h2></div>
        <Btn variant="danger" icon="stop" {busy} onclick={stop} data-testid="tunnel-stop">Stop</Btn>
      </div>
      <div class="card-body stack">
        <div class="stats">
          <div><span class="muted tiny">Requests</span><strong data-testid="tunnel-requests">{tunnel.requests}</strong></div>
          <div><span class="muted tiny">Bytes</span><strong>{bytes(tunnel.bytes)}</strong></div>
          <div><span class="muted tiny">In flight</span><strong>{tunnel.inFlight}/4</strong></div>
          <div><span class="muted tiny">Queued on {ts.data?.holderSite ?? '—'}</span><strong>{ts.data?.pending ?? 0}</strong></div>
          <div><span class="muted tiny">Encryption</span><strong class="small" data-testid="tunnel-encrypted">{encrypted ? 'End to end' : 'None'}</strong></div>
          <div class="wide"><span class="muted tiny">Last error</span><strong class="small" class:bad-text={!!lastError} data-testid="tunnel-last-error">{lastError ?? 'none'}</strong></div>
        </div>
        <div class="muted small">Lease <code>{tunnel.lease?.leaseId}</code> · held by site <strong>{tunnel.lease?.siteSlug}</strong> · started <Time ts={tunnel.lease?.startedAt} /></div>
        <div class="table-wrap">
          <table class="table" data-testid="tunnel-log">
            <thead><tr><th>reqId</th><th>Request</th><th>Step</th><th>Status</th><th class="num">Bytes</th><th class="num">ms</th></tr></thead>
            <tbody>
              {#each tunnel.log as r (r.reqId)}
                <tr>
                  <td class="mono tiny">{r.reqId.slice(-10)}</td>
                  <td class="mono small"><strong>{r.method}</strong> {r.path}</td>
                  <td><span class="badge {stepTone[r.step]}">{r.step}</span>{#if r.error}<div class="tiny bad-text">{r.error}</div>{/if}</td>
                  <td class="small">{r.status ?? '…'}</td>
                  <td class="num small">{r.bytes ? bytes(r.bytes) : '—'}</td>
                  <td class="num small">{r.ms ?? '…'}</td>
                </tr>
              {:else}
                <tr><td colspan="6" class="muted small">Waiting for the agents' first cloud call…</td></tr>
              {/each}
            </tbody>
          </table>
        </div>
      </div>
    </section>
  {:else if other && lease}
    <section class="card" data-testid="tunnel-other">
      <div class="card-head"><h2>In use by another tab</h2><span class="badge info">active</span></div>
      <div class="card-body stack">
        <dl class="kv">
          <dt>Person</dt><dd><strong>{lease.actor}</strong></dd>
          <dt>Site</dt><dd class="mono">{lease.siteSlug}</dd>
          <dt>Since</dt><dd><Time ts={lease.startedAt} /> · last poll <Time ts={lease.lastPollAt} /></dd>
          <dt>Browser</dt><dd class="small muted">{lease.userAgent || '—'}</dd>
          <dt>Traffic</dt><dd>{lease.reqs} requests · {bytes(lease.bytes)}</dd>
          <dt>Encryption</dt><dd data-testid="tunnel-encrypted">{lease.encrypted ? 'End to end' : 'None'}</dd>
        </dl>
        <div><Btn variant="danger" onclick={() => (revokeOpen = true)} data-testid="tunnel-revoke">Revoke the other tab</Btn></div>
      </div>
    </section>
  {:else}
    <section class="card free" data-testid="tunnel-free">
      <div class="card-body stack" style="align-items:flex-start">
        <div class="row"><span class="ic"><Icon name="globe" size={20} /></span><div><h2>Free</h2><p class="muted small" style="margin:2px 0 0">No tab gives internet to this deployment.</p></div></div>
        <div class="small">
          Cloud routes now:
          {#each overview.data?.sites ?? [] as s (s.siteSlug)}<span class="badge {s.cloudRoute === 'none' ? 'bad' : ''}" style="margin:2px 4px">{s.siteSlug}: {routeLabel(s.cloudRoute)}</span>{/each}
        </div>
        {#if noRoute.length === 0}<p class="muted small" style="margin:0">Every reachable site has a route to the cloud; the tunnel is usually not needed.</p>{/if}
        <Btn variant="primary" icon="globe" {busy} onclick={start} data-testid="tunnel-start">Give internet from this browser</Btn>
      </div>
    </section>
  {/if}
</Load>

<Confirm bind:open={revokeOpen} title="Revoke the other tab?" danger confirmLabel="Revoke" message="The tab of {lease?.actor ?? 'the other person'} stops giving internet at its next call. Then you can start from this tab." onconfirm={revoke} />

<style>
  .live {
    border-color: color-mix(in srgb, var(--blue) 45%, var(--border));
  }
  .pulse {
    width: 10px;
    height: 10px;
    border-radius: 50%;
    background: var(--green);
    box-shadow: 0 0 0 0 color-mix(in srgb, var(--green) 60%, transparent);
    animation: pulse 1.6s infinite;
  }
  @keyframes pulse {
    70% {
      box-shadow: 0 0 0 10px transparent;
    }
    100% {
      box-shadow: 0 0 0 0 transparent;
    }
  }
  .stats {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(120px, 1fr));
    gap: 10px;
  }
  .stats div {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    background: var(--surface-2);
  }
  .stats .wide {
    grid-column: span 2;
  }
  .stats strong {
    font-size: 18px;
    font-variant-numeric: tabular-nums;
    overflow-wrap: anywhere;
  }
  .stats strong.small {
    font-size: 13px;
  }
  .ic {
    width: 42px;
    height: 42px;
    border-radius: 11px;
    display: grid;
    place-items: center;
    background: var(--accent-soft);
    color: var(--accent-text);
  }
</style>
