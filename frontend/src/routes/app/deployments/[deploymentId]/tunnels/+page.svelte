<script lang="ts">
  /**
   * §21 #125 Tunnels: raw TCP to a port of a site through the cloud host (`<host>:<public port>`, VM cloud only).
   * Every site has its default agent GUI tunnel (the remote GUI, §21 #118: opened here, never removed). Port tunnels
   * (a container port, the site's edge http / https, or a port on the site's server) are added and removed here; each
   * stays OFF until someone switches it on in that site's agent GUI.
   */
  import { onDestroy, onMount } from 'svelte';
  import { OpmApiError, PORT_TUNNEL_LIMITS, imagePortNumber, portTunnelTargetText, varValues, type PortTunnelStatus, type PortTunnelTarget } from '@opm/shared';
  import { api } from '$lib/api';
  import { Loader } from '$lib/resource.svelte';
  import { getDeploymentScope } from '$lib/config-editor/context';
  import { useConfigHost } from '$opm/forms/host';
  import { toast } from '$lib/toast.svelte';
  import { openRemoteGui } from '$lib/remote-gui/open';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import Field from '$lib/ui/Field.svelte';
  import Select, { type SelectGroup } from '$lib/ui/Select.svelte';
  import Combobox, { type ComboOption } from '$lib/ui/Combobox.svelte';
  import ConfirmDialog from '$lib/ui/ConfirmDialog.svelte';
  import CopyButton from '$lib/ui/CopyButton.svelte';
  import RelTime from '$lib/ui/RelTime.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import TableSkeleton from '$lib/ui/TableSkeleton.svelte';
  import ErrorState from '$lib/ui/ErrorState.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import { ExternalLink, Network, Plus, RefreshCw, Trash2, TriangleAlert } from '$lib/ui/icons';

  const scope = getDeploymentScope();
  const host = useConfigHost();
  const id = scope.id;
  const canRead = $derived(scope.can('deployment.config', 'read'));
  const canWrite = $derived(scope.can('deployment.config', 'publish'));

  const tn = new Loader(() => api.listPortTunnels({ deploymentId: id }));
  let timer: ReturnType<typeof setInterval> | null = null;
  onMount(() => {
    if (!canRead) return;
    tn.load();
    timer = setInterval(() => {
      if (!document.hidden) tn.load(true);
    }, 10_000);
  });
  onDestroy(() => timer && clearInterval(timer));

  // ───────── state of a tunnel
  type Tone = 'green' | 'amber' | 'red' | 'outline';
  function stateOf(t: PortTunnelStatus): { key: string; label: string; tone: Tone; tip: string | null } {
    if (!t.listening) return { key: 'error', label: 'not listening', tone: 'red', tip: t.listenError ?? 'The cloud cannot listen on this port' };
    if (t.enabledOnSite === true) return { key: 'on', label: 'on', tone: 'green', tip: null };
    if (t.enabledOnSite === false) return { key: 'off', label: 'off on the site', tone: 'amber', tip: "Switch it on in the site's agent GUI" };
    return { key: 'unknown', label: 'site not connected', tone: 'outline', tip: 'The site has not told the cloud yet (not connected, directly or through a peer)' };
  }
  /** a link for targets that speak http(s); others are only copied */
  function openUrl(t: PortTunnelStatus, h: string): string | null {
    if (t.target.kind !== 'edge') return null;
    return `${t.target.scheme}://${h}:${t.publicPort}`;
  }

  // ───────── add
  let addOpen = $state(false);
  let site = $state<string | null>(null);
  let target = $state<string | null>(null);
  let hostPort = $state('');
  let publicPort = $state('');
  let label = $state('');
  let touched = $state(false);
  let saving = $state(false);
  let addError = $state<string | null>(null);

  /** the tcp ports of the containers of a site (committed config; image port numbers resolved) */
  function containerPortsOf(siteSlug: string | null) {
    const s = scope.baseConfig.sites.find((x) => x.siteSlug === siteSlug);
    if (!s) return [];
    const vars = varValues(scope.baseConfig);
    return s.config.containers.flatMap((c) =>
      (host.image(c.serviceSlug, c.version)?.ports ?? [])
        .filter((p) => p.protocol === 'tcp')
        .map((p) => ({ containerSlug: c.containerSlug, portName: p.name, port: imagePortNumber(p, vars, id) })),
    );
  }
  const sites = $derived((tn.data?.agents ?? []).map((a) => a.siteSlug));
  const siteOpts = $derived(sites.map((s) => ({ value: s, label: s, mono: true })));
  const targetGroups = $derived<SelectGroup[]>([
    { label: 'Container port', options: containerPortsOf(site).map((p) => ({ value: `c:${p.containerSlug}:${p.portName}`, label: `${p.containerSlug} · ${p.portName}${p.port !== null ? ` (${p.port})` : ''}`, mono: true })) },
    {
      label: 'Edge of the site',
      options: [
        { value: 'edge:http', label: 'Edge · http', sub: 'the http port of the site edge (default 80)' },
        { value: 'edge:https', label: 'Edge · https', sub: 'the https port of the site edge (default 443)' },
      ],
    },
    { label: 'Server', options: [{ value: 'host', label: "Port on the site's server", sub: 'any TCP port on 127.0.0.1 of the site' }] },
  ]);
  const freePorts = $derived(tn.data?.freePorts ?? []);
  async function loadPorts(q: string): Promise<ComboOption[]> {
    const t = q.trim();
    return freePorts
      .filter((p) => !t || String(p).includes(t))
      .slice(0, 200)
      .map((p) => ({ value: String(p), label: String(p) }));
  }

  function targetOf(v: string | null): PortTunnelTarget | null {
    if (!v) return null;
    if (v === 'edge:http' || v === 'edge:https') return { kind: 'edge', scheme: v === 'edge:http' ? 'http' : 'https' };
    if (v === 'host') return { kind: 'host', port: Number(hostPort) };
    const m = /^c:([^:]+):(.+)$/.exec(v);
    return m ? { kind: 'container', containerSlug: m[1]!, portName: m[2]! } : null;
  }
  const siteErr = $derived(!site ? 'Choose a site' : null);
  const targetErr = $derived(!target ? 'Choose what to expose' : null);
  const hostPortErr = $derived(target !== 'host' ? null : !/^\d+$/.test(hostPort.trim()) || Number(hostPort) < 1 || Number(hostPort) > 65535 ? 'A port 1–65535' : null);
  const publicErr = $derived(!publicPort ? 'Choose a port of the cloud' : !freePorts.includes(Number(publicPort)) ? 'Not a free port of the cloud' : null);
  const labelErr = $derived(label.length > PORT_TUNNEL_LIMITS.labelMax ? `At most ${PORT_TUNNEL_LIMITS.labelMax} characters` : null);
  const ok = $derived(!siteErr && !targetErr && !hostPortErr && !publicErr && !labelErr);

  function openAdd() {
    site = sites.length === 1 ? sites[0]! : null;
    target = null;
    hostPort = '';
    publicPort = '';
    label = '';
    touched = false;
    addError = null;
    addOpen = true;
  }
  function setSite(v: string) {
    site = v;
    if (target?.startsWith('c:')) target = null;
  }
  async function add(e: Event) {
    e.preventDefault();
    touched = true;
    const t = targetOf(target);
    if (!ok || !t || !site) return;
    saving = true;
    addError = null;
    try {
      const r = await api.createPortTunnel({ deploymentId: id, body: { siteSlug: site, target: t, publicPort: Number(publicPort), ...(label.trim() ? { label: label.trim() } : {}) } });
      addOpen = false;
      toast.success(`Tunnel added on ${r.address}`, `It stays off until someone switches it on in the agent GUI of ${r.siteSlug}.`);
      tn.load(true);
    } catch (err) {
      if (err instanceof OpmApiError && err.code === 'conflict') {
        addError = err.message;
        publicPort = '';
        tn.load(true);
      } else if (err instanceof OpmApiError && (err.code === 'invalid' || err.code === 'unavailable' || err.code === 'not-found')) addError = err.message;
      else toast.error(err, 'Adding the tunnel failed');
    } finally {
      saving = false;
    }
  }

  // ───────── delete
  let del = $state<PortTunnelStatus | null>(null);
  let delOpen = $state(false);
  async function doDelete() {
    const t = del!;
    await api.deletePortTunnel({ deploymentId: id, tunnelId: t.tunnelId });
    toast.success(`Tunnel on port ${t.publicPort} removed`);
    tn.load(true);
  }
</script>

<svelte:head><title>Tunnels · {scope.label} · OPM</title></svelte:head>

<div class="page">
  <PageHeader title="Tunnels" subtitle="Raw TCP to a port of a site through the cloud host. Each tunnel is off until someone switches it on in that site's agent GUI. Refreshes every 10 s." icon={Network} readOnly={canRead && !canWrite}>
    {#snippet actions()}
      {#if canRead}
        <Button icon={RefreshCw} loading={tn.loading} onclick={() => tn.load(true)} data-testid="refresh-tunnels">Refresh</Button>
        {#if canWrite && tn.data?.available}<Button variant="primary" icon={Plus} onclick={openAdd} data-testid="add-tunnel">Add tunnel</Button>{/if}
      {/if}
    {/snippet}
  </PageHeader>

  {#if !canRead}
    <EmptyState icon={Network} title="No access" text="You need deployment.config.read to see the tunnels." />
  {:else if tn.loading && !tn.data}
    <TableSkeleton cols={5} rows={3} />
  {:else if tn.error && !tn.data}
    <ErrorState message={tn.error} onretry={() => tn.load()} />
  {:else if tn.data}
    {@const d = tn.data}
    {#if !d.available}
      <div class="banner warn" data-testid="tunnels-unavailable"><Icon icon={TriangleAlert} size={16} class="icon" /><div><b>Port tunnels are not available on this cloud.</b> {d.reason ?? ''}</div></div>
    {/if}

    <div class="sec section"><h3>Agent GUI</h3><p class="muted small">The default tunnel of every site: its agent GUI through the cloud. It is switched on / off on the site.</p></div>
    {#if !d.agents.length}
      <p class="muted small">No sites yet.</p>
    {:else}
      <div class="table-wrap">
        <table class="table" data-testid="agent-tunnels">
          <thead><tr><th>Site</th><th>State</th><th class="right">Open</th></tr></thead>
          <tbody>
            {#each d.agents as a (a.siteSlug)}
              <tr data-testid="agent-row-{a.siteSlug}">
                <td class="strong mono">{a.siteSlug}</td>
                <td data-testid="agent-state">
                  {#if !a.connected}<span class="badge outline">not connected</span>
                  {:else if a.enabledOnSite === false}<span class="badge amber" data-tip="Switched off in the agent GUI of the site">off on the site</span>
                  {:else if a.enabledOnSite === true}<span class="badge green"><span class="dot"></span>on</span>
                  {:else}<span class="badge outline">unknown</span>{/if}
                </td>
                <td class="acts">
                  <Button size="sm" icon={ExternalLink} onclick={() => openRemoteGui(id, a.siteSlug)} disabled={!canWrite} tip={canWrite ? "Open this site's agent GUI here in the cloud, as your account" : 'You need deployment.config.publish'} data-testid="open-agent-{a.siteSlug}">Open</Button>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}

    <div class="sec section"><h3>Port tunnels</h3><p class="muted small">A container port, the site's edge or a port on its server, at <span class="mono">{d.host}:&lt;port&gt;</span>.</p></div>
    {#if !d.items.length}
      <EmptyState icon={Network} title="No port tunnels" text={d.available ? 'Add one to reach a port of a site from anywhere through the cloud host.' : 'This cloud cannot carry port tunnels.'}>
        {#snippet action()}{#if canWrite && d.available}<Button variant="primary" icon={Plus} onclick={openAdd}>Add tunnel</Button>{/if}{/snippet}
      </EmptyState>
    {:else}
      <div class="table-wrap">
        <table class="table" data-testid="port-tunnels">
          <thead><tr><th>Tunnel</th><th>Address</th><th>State</th><th>Connections</th><th class="right">Actions</th></tr></thead>
          <tbody>
            {#each d.items as t (t.tunnelId)}
              {@const st = stateOf(t)}
              {@const url = openUrl(t, d.host)}
              <tr data-testid="tunnel-row" data-port={t.publicPort} data-site={t.siteSlug}>
                <td>
                  {#if t.label}<div class="strong ellipsis nm" title={t.label}>{t.label}</div>{/if}
                  <div class="small"><span class="mono">{t.siteSlug}</span> · <span data-testid="tunnel-target">{portTunnelTargetText(t.target)}</span></div>
                  <div class="xs faint">by {t.createdBy} · <RelTime ts={t.createdAt} /></div>
                </td>
                <td>
                  <span class="row nowrap addr">
                    <span class="mono small" data-testid="tunnel-address">{t.address}</span>
                    <CopyButton text={t.address} tip="Copy the address" testid="tunnel-copy" />
                    {#if url}<a class="btn sm ghost icon" href={url} target="_blank" rel="noopener noreferrer" data-tip="Open {url}" aria-label="Open {url}" data-testid="tunnel-open"><Icon icon={ExternalLink} size={13} /></a>{/if}
                  </span>
                </td>
                <td class="st">
                  <span class="badge {st.tone}" data-testid="tunnel-state" data-state={st.key} data-tip={st.tip}>{#if st.key === 'on'}<span class="dot"></span>{/if}{st.label}</span>
                  {#if st.key === 'off'}<div class="xs muted">switch it on in the site's agent GUI</div>{/if}
                  {#if t.route?.kind === 'via'}<div class="xs muted" data-testid="tunnel-via">via {t.route.siteSlug}</div>{/if}
                  {#if t.lastError}<div class="xs text-red err" title={t.lastError} data-testid="tunnel-error">{t.lastError}</div>{/if}
                </td>
                <td class="small nowrap">
                  <div>{t.openConnections} open</div>
                  <div class="xs faint">{#if t.lastConnectionAt}last <RelTime ts={t.lastConnectionAt} />{:else}never used{/if}</div>
                </td>
                <td class="acts">
                  {#if canWrite}<Button size="sm" variant="ghost" icon={Trash2} onclick={() => ((del = t), (delOpen = true))} aria-label="Remove the tunnel on port {t.publicPort}" tip="Remove" data-testid="tunnel-delete" />{/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  {/if}
</div>

<Modal bind:open={addOpen} title="Add tunnel" subtitle="It stays off until someone switches it on in the site's agent GUI." busy={saving} testid="add-tunnel-modal">
  <form id="tunnel-form" class="stack" onsubmit={add} novalidate>
    {#if addError}<div class="banner error small" data-testid="add-tunnel-error"><div>{addError}</div></div>{/if}
    <Field label="Site" id="tn-site" required error={touched ? siteErr : null}>
      <Select id="tn-site" value={site} options={siteOpts} placeholder="— choose a site —" ariaLabel="Site" testid="tn-site" onchange={setSite} />
    </Field>
    <Field label="Expose" id="tn-target" required error={touched ? targetErr : null} hint={site && !containerPortsOf(site).length ? 'The site has no container with a TCP port: expose its edge or a port of its server.' : undefined}>
      <Select id="tn-target" bind:value={target} groups={targetGroups} placeholder={site ? '— container port, edge or server port —' : 'choose a site first'} disabled={!site} ariaLabel="What to expose" testid="tn-target" minWidth={260} />
    </Field>
    {#if target === 'host'}
      <Field label="Port on the server" id="tn-host-port" required error={touched ? hostPortErr : null} hint="A TCP port on 127.0.0.1 of the site's server (1–65535)">
        <input id="tn-host-port" class="input mono" inputmode="numeric" bind:value={hostPort} placeholder="5432" data-testid="tn-host-port" />
      </Field>
    {/if}
    <Field label="Public port" id="tn-port" required error={touched ? publicErr : null} hint="{freePorts.length} free port{freePorts.length === 1 ? '' : 's'} of the cloud (TUNNEL_FREE_PORTS); the tunnel is at {tn.data?.host ?? 'the cloud host'}:<port>">
      <Combobox id="tn-port" bind:value={publicPort} load={loadPorts} loadKey={freePorts} debounceMs={60} mono placeholder={freePorts.length ? 'type or choose a port' : 'no free port'} disabled={!freePorts.length} emptyText="No free port matches" ariaLabel="Public port" testid="tn-port" />
    </Field>
    <Field label="Label" id="tn-label" hint="Optional, e.g. Plant web" error={labelErr}>
      <input id="tn-label" class="input" bind:value={label} maxlength={PORT_TUNNEL_LIMITS.labelMax} placeholder="What it is for" data-testid="tn-label" />
    </Field>
  </form>
  {#snippet footer()}
    <Button onclick={() => (addOpen = false)} disabled={saving}>Cancel</Button>
    <Button type="submit" form="tunnel-form" variant="primary" icon={Plus} loading={saving} disabled={touched && !ok} data-testid="tn-submit">Add tunnel</Button>
  {/snippet}
</Modal>

<ConfirmDialog
  bind:open={delOpen}
  title="Remove the tunnel on port {del?.publicPort}?"
  message="The cloud stops listening on {del?.address} and closes its open connections. The port becomes free for another tunnel."
  confirmLabel="Remove"
  danger
  onconfirm={doDelete}
/>

<style>
  .sec {
    margin-bottom: var(--sp-3);
  }
  .sec h3 {
    font-size: var(--fs-lg);
  }
  .sec p {
    margin-top: 2px;
  }
  th.right {
    text-align: right;
  }
  .nm {
    max-width: 260px;
  }
  .addr {
    gap: 2px;
  }
  .st {
    min-width: 150px;
  }
  .st .badge {
    white-space: nowrap;
  }
  .err {
    max-width: 260px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .acts {
    text-align: right;
    white-space: nowrap;
  }
</style>
