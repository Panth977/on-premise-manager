<script lang="ts">
  import type { EdgeHost, EdgeRoute, Site, ValidationIssue } from '@opm/shared';
  import Button from '../ui/Button.svelte';
  import EmptyState from '../ui/EmptyState.svelte';
  import ConfirmDialog from '../ui/ConfirmDialog.svelte';
  import IssuesList from '../ui/IssuesList.svelte';
  import RelTime from '../ui/RelTime.svelte';
  import Icon from '../ui/Icon.svelte';
  import { Plus, Pencil, Trash2, ArrowUp, ArrowDown, Globe, Route as RouteIcon, ShieldCheck, Lock, Radio, Plug, FileUp, Timer, Layers } from '../ui/icons';
  import { effectiveRouteOrder, moveToResponseHeaders } from '@opm/shared';
  import RouteIssues from './edge/RouteIssues.svelte';
  import { toast } from '../toast.svelte';
  import HostDrawer from './HostDrawer.svelte';
  import TestUrl from '$opm/forms/edge/TestUrl.svelte';
  import EdgePortsForm from '$opm/forms/edge/EdgePortsForm.svelte';
  import { actionSummary, groupLabel, GROUP_TONE, headersCount, bytesText, inheritHost } from '$opm/forms/edge/edge';
  import { catalog } from '../stores/catalog.svelte';
  import { errorsAt, issuesUnder, sevClass } from '../util/issues';
  import type { AnyScope } from './scope.svelte';

  let { scope, site, issues }: { scope: AnyScope; site: Site; issues: ValidationIssue[] } = $props();
  const eb = $derived(`sites[${site.siteSlug}].config.edge`);
  const edge = $derived(site.config.edge);
  const ro = $derived(!scope.canWrite);
  const edgeIssues = $derived(issuesUnder(issues, eb));
  /** effective order (what the edge evaluates): exact, ^~ prefixes, regex (by order), prefixes (longest first) */
  const eff = $derived(effectiveRouteOrder(edge.routes));
  const sorted = $derived(eff.map((o) => edge.routes.find((r) => r.routeId === o.routeId)!).filter(Boolean));
  const regexIds = $derived(eff.filter((o) => o.group === 'regex').map((o) => o.routeId));
  const sitePath = $derived(`${scope.base}/sites/${site.siteSlug}`);
  const routeHref = (id: string) => `${sitePath}/edge/route?id=${encodeURIComponent(id)}`;
  const certs = $derived(scope.kind === 'deployment' ? scope.certsOf(site.siteSlug) : []);


  // hosts
  let hostOpen = $state(false);
  let hostEdit = $state<EdgeHost | null>(null);
  let hostRmOpen = $state(false);
  let hostRm = $state<EdgeHost | null>(null);
  const hostRmRoutes = $derived(hostRm ? edge.routes.filter((r) => r.hosts.includes(hostRm!.hostname)) : []);
  function removeHost() {
    const n = hostRm!.hostname;
    edge.hosts = edge.hosts.filter((h) => h.hostname !== n);
    for (const r of edge.routes) r.hosts = r.hosts.filter((x) => x !== n);
  }
  function tlsDetails(h: EdgeHost): string {
    const t = h.tls;
    if (t.mode === 'acme') return `${t.email} · ${t.challenge}${t.challenge === 'dns-01' ? ` · ${t.dnsProvider ?? '?'} / ${t.dnsTokenSecretSlug ?? '?'}` : ''}`;
    if (t.mode === 'files') return `cert ${t.certFileSlug} · key ${t.keySecretSlug}`;
    if (t.mode === 'internal') return 'agent CA, 12 h certificates';
    return 'HTTP only';
  }
  const certOf = (h: EdgeHost) => certs.find((c) => c.hostname === h.hostname) ?? null;

  // routes (the editor is its own page: …/edge/route)
  let routeRmOpen = $state(false);
  let routeRm = $state<EdgeRoute | null>(null);
  /** only the relative order of regex routes matters: renumber them 1..n in the given sequence */
  function renumberRegex(ids: string[]) {
    ids.forEach((id, i) => {
      const real = edge.routes.find((x) => x.routeId === id);
      if (real) real.order = i + 1;
    });
  }
  function move(r: EdgeRoute, d: -1 | 1) {
    const list = regexIds.slice();
    const i = list.indexOf(r.routeId);
    const j = i + d;
    if (i < 0 || j < 0 || j >= list.length) return;
    [list[i], list[j]] = [list[j]!, list[i]!];
    renumberRegex(list);
  }
  function removeRoute() {
    const id = routeRm!.routeId;
    edge.routes = edge.routes.filter((r) => r.routeId !== id);
    renumberRegex(regexIds.filter((x) => x !== id));
  }
  const ent = (r: EdgeRoute) => eff.find((o) => o.routeId === r.routeId)!;
  /** one-click fix of W_RESPONSE_HEADER_AS_REQUEST from the routes table (same shared helper as the route page) */
  function moveHeader(r: EdgeRoute, name: string) {
    const i = edge.routes.findIndex((x) => x.routeId === r.routeId);
    if (i < 0) return;
    const snap = $state.snapshot(edge) as Site['config']['edge'];
    edge.routes[i] = moveToResponseHeaders(snap.routes[i]!, name, inheritHost(snap, snap.routes[i]!));
    toast.success(`${name} moved to “Response headers → browser” of ${r.routeId}`);
  }
  const shownIssues = (re: ValidationIssue[]) => re.filter((x) => x.severity !== 'info');
  const routeErr = (r: EdgeRoute) => issuesUnder(edgeIssues, `${eb}.routes[${r.routeId}]`);
  const hostErr = (h: EdgeHost) => issuesUnder(edgeIssues, `${eb}.hosts[${h.hostname}]`);
</script>

<div class="stack loose">
  <section class="card">
    <div class="card-head"><Icon icon={Globe} size={16} /><h3>Ports</h3><span class="faint small">The edge (embedded Caddy) listens on these host ports.</span></div>
    <div class="card-body">
      <EdgePortsForm {edge} disabled={ro} issues={(f) => issuesUnder(issues, `${eb}.${f}`)} />
      <div class="small muted hint">Container host ports cannot use 80, 443 or these ports — add a route instead.</div>
    </div>
    {#if errorsAt(issues, eb).length}<div class="card-body" style="padding-top:0">{#each errorsAt(issues, eb) as m, i (i)}<div class="xs text-red">{m}</div>{/each}</div>{/if}
  </section>

  <section class="card">
    <div class="card-head">
      <Icon icon={ShieldCheck} size={16} /><h3>Hosts</h3><span class="badge">{edge.hosts.length}</span>
      <span class="spacer"></span>
      {#if !ro}<Button size="sm" icon={Plus} onclick={() => ((hostEdit = null), (hostOpen = true))} data-testid="add-host">Add host</Button>{/if}
    </div>
    {#if !edge.hosts.length}
      <div class="card-body"><EmptyState compact title="No hosts" text="Without a host every request gets 404 (HTTP) or a TLS failure. Add “*” to answer any name." /></div>
    {:else}
      <div class="scroll-x">
        <table class="table" data-testid="hosts-table">
          <thead><tr><th>Hostname</th><th>TLS mode</th><th>TLS details</th><th>HTTP→HTTPS</th><th>Response headers</th>{#if scope.kind === 'deployment'}<th>Certificate</th>{/if}<th class="actions"></th></tr></thead>
          <tbody>
            {#each edge.hosts as h (h.hostname)}
              {@const he = hostErr(h)}
              {@const c = certOf(h)}
              <tr class:err={he.some((i) => i.severity === 'error')}>
                <td class="mono strong">{h.hostname}{#each he as i, k (k)}<div class="xs {sevClass(i.severity)}" style="font-family:var(--font-sans);font-weight:400">{i.message}</div>{/each}</td>
                <td><span class="badge {h.tls.mode === 'none' ? 'gray' : 'green'}">{#if h.tls.mode !== 'none'}<Icon icon={Lock} size={10} />{/if}{h.tls.mode}</span></td>
                <td class="small muted">{tlsDetails(h)}</td>
                <td>{h.redirectHttpToHttps ? 'yes (308)' : '—'}</td>
                <td class="small" data-testid="host-resp-{h.hostname}">{#if headersCount(h.responseHeaders)}<span class="badge blue" data-tip={[...Object.entries(h.responseHeaders.set).map(([k, v]) => `set ${k}: ${v}`), ...Object.entries(h.responseHeaders.add).map(([k, v]) => `add ${k}: ${v}`), ...h.responseHeaders.remove.map((k) => `remove ${k}`)].join('\n')}>{headersCount(h.responseHeaders)} header{headersCount(h.responseHeaders) === 1 ? '' : 's'}</span>{:else}<span class="faint">—</span>{/if}</td>
                {#if scope.kind === 'deployment'}
                  <td class="small">
                    {#if h.tls.mode === 'none'}<span class="faint">—</span>
                    {:else if !c}<span class="faint" data-tip="No report from the agent yet (after the first apply)">no report</span>
                    {:else if c.lastError}<span class="badge red" data-tip={c.lastError}>error</span>
                    {:else}<span class="badge green" data-tip="{c.issuer ?? ''} · renews {c.nextRenewalAt ?? '—'}">valid</span> <span class="faint xs">until <RelTime ts={c.notAfter} /></span>{/if}
                  </td>
                {/if}
                <td class="actions">
                  {#if !ro}
                    <Button size="sm" variant="ghost" icon={Pencil} aria-label="Edit {h.hostname}" tip="Edit" onclick={() => ((hostEdit = h), (hostOpen = true))} />
                    <Button size="sm" variant="ghost" icon={Trash2} aria-label="Remove {h.hostname}" tip="Remove" onclick={() => ((hostRm = h), (hostRmOpen = true))} />
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>

  <section class="card">
    <div class="card-head wrap">
      <Icon icon={RouteIcon} size={16} /><h3>Routes</h3><span class="badge">{edge.routes.length}</span>
      <span class="faint small hide-sm">Effective order, like nginx: exact → longest prefix (^~ stops) → regex in order → longest prefix. /_opm/ always goes to the agent.</span>
      <span class="spacer"></span>
      {#if !ro}
        <Button size="sm" icon={FileUp} href="{sitePath}/edge/import" data-testid="import-nginx">Import from nginx.conf…</Button>
        <Button size="sm" icon={Plus} href="{sitePath}/edge/route?new=proxy" disabled={!site.config.containers.length} tip={site.config.containers.length ? undefined : 'Add a container first'} data-testid="add-proxy-route">Proxy route</Button>
        <Button size="sm" icon={Plus} href="{sitePath}/edge/route?new=redirect" data-testid="add-redirect-route">Redirect</Button>
        <Button size="sm" icon={Plus} href="{sitePath}/edge/route?new=rewrite" data-testid="add-rewrite-route">Rewrite</Button>
      {/if}
    </div>
    {#if !edge.routes.length}
      <div class="card-body"><EmptyState compact title="No routes" text="Every path answers with the not-found page. Add a proxy route to a container port, a redirect, or import your nginx.conf." /></div>
    {:else}
      <div class="scroll-x">
        <table class="table" data-testid="routes-table">
          <thead><tr><th class="num">#</th><th>Group</th><th>Match</th><th>Route id</th><th>Action</th><th class="ind-col" aria-label="Options"></th><th class="issues-col">Issues</th><th class="actions"></th></tr></thead>
          <tbody>
            {#each sorted as r (r.routeId)}
              {@const re = routeErr(r)}
              {@const o = ent(r)}
              {@const a = r.action}
              {@const ri = regexIds.indexOf(r.routeId)}
              {@const si = shownIssues(re)}
              <tr class:has-sub={si.length > 0} class:err={re.some((x) => x.severity === 'error')} class:warn={!re.some((x) => x.severity === 'error') && re.some((x) => x.severity === 'warning')} data-testid="route-row-{r.routeId}" data-rank={o.rank} data-group={o.group}>
                <td class="num mono">{o.rank}</td>
                <td class="nowrap">
                  <span class="badge {GROUP_TONE[o.group]}" data-tip={o.why} data-testid="route-group-{r.routeId}">{groupLabel(o, eff)}</span>
                  {#if o.group === 'regex' && !ro}
                    <button class="btn ghost icon sm" aria-label="Move {r.routeId} up" disabled={ri === 0} onclick={() => move(r, -1)} data-testid="up-{r.routeId}"><Icon icon={ArrowUp} size={13} /></button>
                    <button class="btn ghost icon sm" aria-label="Move {r.routeId} down" disabled={ri === regexIds.length - 1} onclick={() => move(r, 1)} data-testid="down-{r.routeId}"><Icon icon={ArrowDown} size={13} /></button>
                  {/if}
                </td>
                <td>
                  <span class="nowrap"><span class="badge outline xs">{r.match.kind}</span> <span class="mono strong">{r.match.path}</span></span>
                  <div class="xs faint">{r.hosts.length ? r.hosts.join(', ') : 'all hosts'} · {r.match.methods?.join(', ') ?? 'all methods'}</div>
                </td>
                <td class="mono nowrap">{r.routeId}</td>
                <td class="small"><span class="badge {a.type === 'proxy' ? 'blue' : a.type === 'redirect' ? 'violet' : 'amber'}">{a.type}</span> <span class="mono xs" data-testid="route-action-{r.routeId}">{actionSummary(r)}</span></td>
                <td class="ind-col">
                  <div class="icons">
                  {#if a.type === 'proxy'}
                    {#if a.websocket}<span data-tip="WebSocket allowed"><Icon icon={Plug} size={13} /></span>{/if}
                    {#if !a.requestBuffering && !a.responseBuffering}<span data-tip="Streams both ways (no buffering): SSE ok"><Icon icon={Radio} size={13} /></span>{:else}<span data-tip="Buffers the {a.requestBuffering ? 'request' : ''}{a.requestBuffering && a.responseBuffering ? ' and the ' : ''}{a.responseBuffering ? 'response' : ''}"><Icon icon={Layers} size={13} /></span>{/if}
                    {#if a.readTimeoutSec !== 60 || a.connectTimeoutSec !== 60 || a.sendTimeoutSec !== 60}<span data-tip="Timeouts connect {a.connectTimeoutSec} s · send {a.sendTimeoutSec} s · read {a.readTimeoutSec} s"><Icon icon={Timer} size={13} /></span>{/if}
                    {#if a.maxBodyBytes !== null}<span class="badge xs" data-tip="Own body limit">{bytesText(a.maxBodyBytes === 0 ? null : a.maxBodyBytes)}</span>{/if}
                  {/if}
                  {#if r.responseHeaders}<span class="badge blue xs" data-tip="Own response headers (replace the host’s)">hdr {headersCount(r.responseHeaders)}</span>{/if}
                  </div>
                </td>
                <td class="issues-col"><RouteIssues issues={re} routeId={r.routeId} editHref={routeHref(r.routeId)} onmove={ro ? null : (h) => moveHeader(r, h)} /></td>
                <td class="actions">
                  <Button size="sm" variant="ghost" icon={Pencil} href={routeHref(r.routeId)} aria-label="Edit {r.routeId}" tip={ro ? 'View' : 'Edit'} data-testid="edit-route-{r.routeId}" />
                  {#if !ro}<Button size="sm" variant="ghost" icon={Trash2} aria-label="Remove {r.routeId}" tip="Remove" onclick={() => ((routeRm = r), (routeRmOpen = true))} />{/if}
                </td>
              </tr>
              {#if si.length}
                <tr class="sub" class:err={si.some((x) => x.severity === 'error')} class:warn={!si.some((x) => x.severity === 'error')} data-testid="route-sub-{r.routeId}">
                  <td colspan="8"><div class="sub-line xs {si.some((x) => x.severity === 'error') ? 'text-red' : 'text-amber'}" title={si.map((x) => x.message).join('\n')}>{si[0]!.message}{si.length > 1 ? ` (+${si.length - 1} more)` : ''}</div></td>
                </tr>
              {/if}
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>

  <TestUrl {site} />

  <IssuesList issues={edgeIssues} title="Edge errors and warnings" emptyText="The edge config is valid." />
</div>

<HostDrawer bind:open={hostOpen} {scope} {site} host={hostEdit} />
<ConfirmDialog bind:open={hostRmOpen} title="Remove host {hostRm?.hostname}?" confirmLabel="Remove" danger onconfirm={removeHost} testid="remove-host">
  {#snippet body()}
    {#if hostRmRoutes.length}<p class="muted small">It is also removed from the host filter of {hostRmRoutes.length} route{hostRmRoutes.length === 1 ? '' : 's'}: {hostRmRoutes.map((r) => r.routeId).join(', ')}.</p>{:else}<p class="muted small">No route filters on this host.</p>{/if}
  {/snippet}
</ConfirmDialog>
<ConfirmDialog bind:open={routeRmOpen} title="Remove route {routeRm?.routeId}?" message="Removed from the change set in this browser; Commit… records it." confirmLabel="Remove" danger onconfirm={removeRoute} testid="remove-route" />

<style>
  .hint {
    align-self: center;
  }
  @media (max-width: 720px) {
    .hide-sm {
      display: none;
    }
  }
  .card-head.wrap {
    flex-wrap: wrap;
    row-gap: 6px;
  }
  /* the indicator cell stays a table cell (shares the row background + borders); only its content is a flex row */
  .icons {
    display: flex;
    gap: 6px;
    align-items: center;
    color: var(--text-2);
  }
  /* fixed-size indicator + issues columns: their content (icons, badges) never moves the other columns */
  .ind-col {
    width: 150px;
    min-width: 150px;
    white-space: nowrap;
  }
  .issues-col {
    width: 1%;
    min-width: 104px;
    white-space: nowrap;
  }
  /* the row + its one-line issue summary read as one row */
  tr.has-sub > td {
    border-bottom: 0;
  }
  tr.sub > td {
    padding-top: 0;
    padding-bottom: 7px;
  }
  /* width:0 + min-width:100% → the summary never widens the table; it is cut with an ellipsis */
  .sub-line {
    width: 0;
    min-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  :global(.card-head) .spacer {
    flex: 1;
  }
</style>
