<script lang="ts">
  /**
   * Edge editor (round E · nginx parity): ports + edge body limit · hosts (drawer: TLS + response headers) · routes in
   * EFFECTIVE order (the order the edge evaluates them; group badge + "why"; manual order only among regex routes) ·
   * Test a URL (full resolveEdgeRequest trace) · "Import from nginx.conf…". Routes are edited on their own page
   * (config/edge/route?id=…, >12 nested fields). The whole edge is locally editable (local working copy).
   */
  import { base } from '$app/paths';
  import type { Config, EdgeHost, EdgeTls, RevisionCatalog, Site, ValidationIssue } from '@opm/shared/types';
  import { effectiveRouteOrder, isEmptyResponseHeaders } from '@opm/shared/edge-resolve';
  import HostForm, { applyHost, newHost } from '$opm/forms/edge/HostForm.svelte';
  import EdgePortsForm from '$opm/forms/edge/EdgePortsForm.svelte';
  import TestUrl from '$opm/forms/edge/TestUrl.svelte';
  import { GROUP_LABEL, GROUP_TONE, actionText, matchText } from '../edge';
  import { moveRegex } from '$opm/forms/edge/edge';
  import Drawer from '../ui/Drawer.svelte';
  import Confirm from '../ui/Confirm.svelte';
  import Btn from '../ui/Btn.svelte';
  import Icon from '../ui/Icon.svelte';
  import Issues from '../ui/Issues.svelte';
  import InfoTip from '../ui/InfoTip.svelte';

  interface Props {
    site: Site;
    config: Config | null;
    catalog: RevisionCatalog | null;
    deploymentId: string;
    issues: ValidationIssue[];
    certs?: { hostname: string; notAfter: string | null; lastError: string | null; issuer: string | null }[];
  }
  let { site, config, catalog, deploymentId, issues, certs = [] }: Props = $props();
  const edge = $derived(site.config.edge);
  const certOf = (h: string) => certs.find((c) => c.hostname === h);
  const pfx = $derived(`sites[${site.siteSlug}].config.edge`);

  // ── hosts: the shared HostForm ($opm, SPEC §21 #102 — the same form as the cloud console) in a wide drawer
  let hostOpen = $state(false);
  let hostIdx = $state<number | null>(null);
  let hostValue = $state<EdgeHost>(newHost());
  let hostOriginal = $state<EdgeHost | null>(null);
  let hostSubmitted = $state(false);
  let hostValid = $state(false);
  let removeHost = $state<number | null>(null);
  let rmHostOpen = $state(false);
  function editHost(i: number | null) {
    hostIdx = i;
    hostOriginal = i === null ? null : (JSON.parse(JSON.stringify(edge.hosts[i])) as EdgeHost);
    hostValue = hostOriginal ? (JSON.parse(JSON.stringify(hostOriginal)) as EdgeHost) : newHost();
    hostSubmitted = false;
    hostOpen = true;
  }
  function onApplyHost() {
    hostSubmitted = true;
    if (!hostValid) return;
    applyHost(site, hostOriginal, $state.snapshot(hostValue) as EdgeHost);
    hostOpen = false;
  }
  function doRemoveHost() {
    if (removeHost === null) return;
    const name = edge.hosts[removeHost].hostname;
    edge.hosts.splice(removeHost, 1);
    for (const r of edge.routes) r.hosts = r.hosts.filter((x) => x !== name);
    removeHost = null;
  }
  function tlsText(t: EdgeTls): string {
    if (t.mode === 'acme') return `acme · ${t.challenge} · ${t.email}`;
    if (t.mode === 'files') return `files · cert ${t.certFileSlug} · key ${t.keySecretSlug}`;
    if (t.mode === 'internal') return 'site local CA (12 h certificates)';
    return '—';
  }

  // ── routes (effective order = how the edge evaluates them; only regex routes have a manual order)
  const routeHref = (q: string) => `${base}/app/${site.siteSlug}/config/edge/route?${q}`;
  const order = $derived(effectiveRouteOrder(edge.routes));
  const ordered = $derived(order.map((o) => ({ o, r: edge.routes.find((x) => x.routeId === o.routeId)! })).filter((x) => x.r));
  const regexIds = $derived(order.filter((o) => o.group === 'regex').map((o) => o.routeId));
  let removeRoute = $state<string | null>(null);
  let rmRouteOpen = $state(false);
  function doRemoveRoute() {
    const i = edge.routes.findIndex((r) => r.routeId === removeRoute);
    if (i >= 0) edge.routes.splice(i, 1);
    removeRoute = null;
  }
  const routeIssues = (id: string) => issues.filter((i) => i.severity !== 'info').filter((i) => i.path === `${pfx}.routes[${id}]` || i.path.startsWith(`${pfx}.routes[${id}].`));

</script>

<div class="stack">
  <section class="card">
    <div class="card-head"><h3>Ports &amp; limits<InfoTip text="The edge (embedded Caddy) listens on these host ports and serves every host and route below." /></h3></div>
    <div class="card-body">
      <EdgePortsForm {edge} issues={(f) => issues.filter((i) => i.path === `${pfx}.${f}`)} />
    </div>
  </section>

  <section class="card">
    <div class="card-head"><h3>Hosts</h3><Btn size="sm" icon="plus" onclick={() => editHost(null)} data-testid="add-host">Add host</Btn></div>
    <div class="table-wrap">
      <table class="table" data-testid="hosts">
        <thead><tr><th>Hostname</th><th>TLS mode</th><th>TLS details</th><th>HTTP→HTTPS</th><th>Response headers</th><th>Certificate</th><th></th></tr></thead>
        <tbody>
          {#each edge.hosts as h, i (h.hostname + i)}
            {@const c = certOf(h.hostname)}
            {@const rh = h.responseHeaders}
            <tr>
              <td class="mono"><strong>{h.hostname}</strong></td>
              <td><span class="badge {h.tls.mode === 'none' ? '' : 'ok'}">{h.tls.mode}</span></td>
              <td class="small">{tlsText(h.tls)}</td>
              <td>{h.redirectHttpToHttps ? 'yes' : '—'}</td>
              <td class="small mono">{#if isEmptyResponseHeaders(rh)}<span class="faint">—</span>{:else}{#each Object.keys(rh.set) as n (n)}<div>set {n}</div>{/each}{#each Object.keys(rh.add) as n (n)}<div>add {n}</div>{/each}{#each rh.remove as n (n)}<div>remove {n}</div>{/each}{/if}</td>
              <td class="small">{#if c?.lastError}<span class="bad-text">{c.lastError}</span>{:else if c?.notAfter}valid until {c.notAfter.slice(0, 16).replace('T', ' ')}{:else}<span class="faint">—</span>{/if}</td>
              <td class="actions">
                <Btn size="xs" icon="edit" onclick={() => editHost(i)} data-testid="edit-host-{h.hostname}">Edit</Btn>
                <Btn size="xs" variant="ghost" icon="trash" aria-label="Remove host {h.hostname}" onclick={() => ((removeHost = i), (rmHostOpen = true))} />
              </td>
            </tr>
          {:else}
            <tr><td colspan="7" class="muted small">No hosts: every request gets the not-found page.</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  </section>

  <section class="card">
    <div class="card-head">
      <h3>Routes <span class="muted small">· effective order</span><InfoTip text="The edge checks routes like nginx locations: exact first, then the longest prefix — a prefix marked ^~ stops there — then regex routes in their order (first match wins), else the longest prefix. Only regex routes can be moved; every other position follows from the path. Hover a group badge for the reason." /></h3>
      <div class="row">
        <a class="btn sm" href={routeHref('new=proxy')} data-testid="add-proxy"><Icon name="plus" size={14} />Proxy route</a>
        <a class="btn sm" href={routeHref('new=redirect')} data-testid="add-redirect"><Icon name="plus" size={14} />Redirect route</a>
        <a class="btn sm" href={routeHref('new=rewrite')} data-testid="add-rewrite"><Icon name="plus" size={14} />Rewrite route</a>
        <a class="btn sm" href="{base}/app/{site.siteSlug}/config/edge/import" data-testid="import-nginx"><Icon name="upload" size={14} />Import from nginx.conf…</a>
      </div>
    </div>
    <p class="card-hint order-note">Matched like nginx: <strong>exact</strong> → longest <strong>prefix</strong> (<code>^~</code> stops) → <strong>regex</strong> in order · only regex routes move</p>
    <div class="table-wrap">
      <table class="table" data-testid="routes">
        <thead><tr><th>#</th><th>Group</th><th></th><th>Route id</th><th>Hosts</th><th>Match</th><th>Methods</th><th>Action</th><th></th></tr></thead>
        <tbody>
          {#each ordered as { o, r } (r.routeId)}
            {@const ri = routeIssues(r.routeId)}
            {@const k = regexIds.indexOf(r.routeId)}
            <tr data-testid="route-row-{r.routeId}" data-group={o.group}>
              <td class="num">{o.rank}</td>
              <td><span class="badge {GROUP_TONE[o.group]}" title={o.why} data-testid="why-{r.routeId}">{GROUP_LABEL[o.group]}<Icon name="info" size={11} /></span></td>
              <td class="nowrap">
                {#if o.group === 'regex'}
                  <Btn size="xs" variant="ghost" icon="arrowUp" aria-label="Move {r.routeId} up" disabled={k === 0} onclick={() => moveRegex(edge.routes, r.routeId, -1)} />
                  <Btn size="xs" variant="ghost" icon="arrowDown" aria-label="Move {r.routeId} down" disabled={k === regexIds.length - 1} onclick={() => moveRegex(edge.routes, r.routeId, 1)} />
                {/if}
              </td>
              <td class="mono rid"><a href={routeHref(`id=${encodeURIComponent(r.routeId)}`)}><strong>{r.routeId}</strong></a>{#if ri.some((i) => i.severity === 'error')}<span class="badge bad">{ri.filter((i) => i.severity === 'error').length} errors</span>{:else if ri.length}<span class="badge warn" title={ri.map((i) => `${i.code}: ${i.message}`).join('\n')}>{ri.length} warning{ri.length === 1 ? '' : 's'}</span>{/if}</td>
              <td class="small">{r.hosts.length ? r.hosts.join(', ') : 'all'}</td>
              <td class="mono small">{matchText(r)}</td>
              <td class="small">{r.match.methods?.join(' ') ?? 'all'}</td>
              <td class="small act">{actionText(r)}</td>
              <td class="actions">
                <a class="btn xs" href={routeHref(`id=${encodeURIComponent(r.routeId)}`)} data-testid="edit-route-{r.routeId}"><Icon name="edit" size={13} />Edit</a>
                <Btn size="xs" variant="ghost" icon="trash" aria-label="Remove route {r.routeId}" onclick={() => ((removeRoute = r.routeId), (rmRouteOpen = true))} />
              </td>
            </tr>
          {:else}
            <tr><td colspan="9" class="muted small">No routes: every request gets the not-found page (404).</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  </section>

  <TestUrl {site} />

  <Issues issues={issues} title="Edge validation" />
</div>

<Drawer bind:open={hostOpen} wide title={hostIdx === null ? 'Add host' : `Edit host ${edge.hosts[hostIdx]?.hostname ?? ''}`} subtitle="Applies to this site’s changes in this browser (not committed until you commit them)." testid="host-drawer">
  {#if hostOpen}<HostForm bind:value={hostValue} bind:valid={hostValid} {site} original={hostOriginal} mode={hostIdx === null ? 'create' : 'edit'} submitted={hostSubmitted} />{/if}
  {#snippet footer()}
    <Btn onclick={() => (hostOpen = false)}>Cancel</Btn>
    <Btn variant="primary" disabled={hostSubmitted && !hostValid} onclick={onApplyHost} data-testid="host-apply">{hostIdx === null ? 'Add host' : 'Apply'}</Btn>
  {/snippet}
</Drawer>

<Confirm bind:open={rmHostOpen} title="Remove host?" danger confirmLabel="Remove" message="The host {removeHost !== null ? edge.hosts[removeHost]?.hostname : ''} is removed from the edge and from every route host filter." onconfirm={doRemoveHost} />
<Confirm bind:open={rmRouteOpen} title="Remove route {removeRoute ?? ''}?" danger confirmLabel="Remove" message="Requests that matched this route go to the next matching route or the not-found page." onconfirm={doRemoveRoute} />

<style>
  .act {
    min-width: 240px;
    max-width: 380px;
    word-break: break-word;
  }
  .rid {
    white-space: nowrap;
  }
  .rid :global(.badge) {
    margin-left: 6px;
  }
</style>
