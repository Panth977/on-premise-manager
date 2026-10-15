<script lang="ts">
  /**
   * "Test a URL" (SPEC §12.1; §21 #102) — ONE card for the cloud Edge tab and the agent Edge page: the full decision of the
   * agent edge for one request (shared resolveEdgeRequest over the working edge with expressions resolved): status,
   * route, upstream / location, effective order, rewrites, match, captures, upstream path, timeouts, buffering, body
   * limit, WebSocket, request headers → container and response headers → browser. Re-runs live after the first Test.
   */
  import type { Site } from '@opm/shared/types';
  import { AUTO_HOST_RE } from '@opm/shared/expr';
  import { EDGE_METHODS, edgeExprContext, resolveSiteHosts } from '@opm/shared/validate';
  import { ADD_HEADER_STATUSES, resolveEdgeRequest, type EdgeResolution } from '@opm/shared/edge-resolve';
  import Select from '../../primitives/Select.svelte';
  import NumberStepper from '../../primitives/NumberStepper.svelte';
  import Toggle from '../../primitives/Toggle.svelte';
  import Btn from '../../primitives/Btn.svelte';
  import Icon from '../../primitives/Icon.svelte';
  import { UNITS, type Unit } from './ByteSizeInput.svelte';
  import { useConfigHost } from '../host';
  import { GROUP_TONE, bytesText, groupLabel, portMaps, resolvedEdge } from './edge';

  let { site }: { site: Site } = $props();
  const host = useConfigHost();
  const cfg = $derived(host.config());
  const siteHost = $derived((resolveSiteHosts(cfg, host.deploymentId)[site.siteSlug]?.host || 'localhost').replace(new RegExp(AUTO_HOST_RE.source, 'g'), 'localhost'));
  let method = $state('GET');
  let url = $state('');
  let notReady = $state<Record<string, boolean>>({});
  let upgrade = $state(false);
  let bodyOn = $state(false);
  let bodyNum = $state<number | null>(1);
  let bodyUnit = $state<Unit>('MiB');
  let result = $state<EdgeResolution | null>(null);
  let tested = $state(false);
  $effect(() => {
    if (!url) url = `http://${siteHost}${site.config.edge.httpPort === 80 ? '' : `:${site.config.edge.httpPort}`}/`;
  });
  const edge = $derived(resolvedEdge($state.snapshot(site.config.edge) as Site['config']['edge'], edgeExprContext(cfg, host.deploymentId, site.siteSlug)));
  const ports = $derived(portMaps(host, site));
  const slugs = $derived(Object.fromEntries(site.config.containers.map((c) => [c.containerSlug, c.containerSlug])));
  const bodyBytes = $derived(bodyOn && bodyNum !== null && Number.isFinite(bodyNum) ? Math.round(bodyNum * UNITS.find((u) => u.value === bodyUnit)!.mult) : undefined);
  function run() {
    tested = true;
    result = resolveEdgeRequest(edge, url.trim(), {
      method,
      ports,
      slugs,
      notReady: { ...Object.fromEntries(site.config.containers.filter((c) => !c.enabled).map((c) => [c.containerSlug, true])), ...notReady },
      bodyBytes,
      upgrade,
    });
  }
  // live re-test after the first run when the edge or the inputs change
  $effect(() => {
    void [edge, ports, method, notReady, upgrade, bodyBytes];
    if (tested) run();
  });
  const tone = $derived(!result ? '' : result.status === 0 ? 'red' : result.status >= 500 ? 'red' : result.status >= 400 ? 'amber' : result.status >= 300 ? 'violet' : 'green');
  function onsubmit(e: Event) {
    e.preventDefault();
    run();
  }
  const pathOf = (id: string) => edge.routes.find((r) => r.routeId === id)?.match.path ?? '';
  const t = $derived(result?.trace ?? null);
  const winner = $derived(result?.route?.routeId ?? null);
  const respRows = $derived.by(() => {
    if (!t) return [];
    const h = t.responseHeaders;
    const al = (k: string) => h.always.some((x) => x.toLowerCase() === k.toLowerCase());
    return [...h.remove.map((k) => ({ op: 'remove', name: k, value: '', always: false })), ...Object.entries(h.set).map(([k, v]) => ({ op: 'set', name: k, value: v, always: al(k) })), ...Object.entries(h.add).map(([k, v]) => ({ op: 'add', name: k, value: v, always: al(k) }))];
  });
  /** add_header applies to these statuses (a proxied answer is assumed to be the container's 200) */
  const respStatus = $derived(result ? (result.kind === 'proxy' ? 200 : result.status) : 0);
  const applies = $derived((ADD_HEADER_STATUSES as readonly number[]).includes(respStatus));
</script>

<section class="card" data-testid="test-url">
  <div class="card-head"><h3><Icon name="play" size={16} />Test a URL</h3><span class="hint">The whole decision of the edge, over the working copy with expressions resolved.</span></div>
  <div class="card-body stack">
    <form class="tr" {onsubmit}>
      <div class="meth"><Select value={method} options={EDGE_METHODS.map((m) => ({ value: m, label: m }))} ariaLabel="Method" testid="test-method" onchange={(v) => (method = v)} /></div>
      <input class="input mono grow" bind:value={url} placeholder="https://plant.acme.com/api/v1/x" aria-label="URL" data-testid="test-url-input" />
      <Btn type="submit" variant="primary" icon="play" data-testid="test-run">Test</Btn>
    </form>
    <div class="nr small">
      <Toggle bind:checked={upgrade} label="WebSocket upgrade" testid="test-upgrade" />
      <Toggle bind:checked={bodyOn} label="Body size" testid="test-body-on" />
      {#if bodyOn}
        <div class="bnum"><NumberStepper bind:value={bodyNum} min={0} size="sm" label="Body size" testid="test-body-num" /></div>
        <div class="bunit"><Select value={bodyUnit} options={UNITS.map((u) => ({ value: u.value, label: u.value }))} size="sm" ariaLabel="Body size unit" testid="test-body-unit" onchange={(v) => (bodyUnit = v as Unit)} /></div>
      {/if}
    </div>
    {#if site.config.containers.length}
      <div class="nr small" data-testid="test-not-ready">
        <span class="muted">Not ready:</span>
        {#each site.config.containers as c (c.containerSlug)}
          <Toggle checked={!c.enabled || !!notReady[c.containerSlug]} disabled={!c.enabled} label="{c.containerSlug}{c.enabled ? '' : ' (disabled)'}" testid="test-nr-{c.containerSlug}" onchange={(v) => (notReady = { ...notReady, [c.containerSlug]: v })} />
        {/each}
      </div>
    {/if}
    {#if result}
      <div class="res" data-testid="test-result" data-route={winner ?? ''} data-status={result.status}>
        <div class="row wrap">
          <span class="badge {tone} code" data-testid="test-status">{result.status === 0 ? 'no response' : result.status}</span>
          <span class="badge outline" data-testid="test-kind">{result.kind}</span>
          <strong>{result.title}</strong>
          {#if winner}<span class="small muted">route <span class="mono strong" data-testid="test-route">{winner}</span></span>{/if}
        </div>
        {#if result.upstream}
          <div class="up"><span class="muted small">Upstream</span> <Icon name="arrowRight" size={13} /> <code data-testid="test-upstream">{result.upstream.url}</code> <span class="faint xs">(edge dials 127.0.0.1:&lt;loopback&gt; of {result.upstream.containerSlug})</span></div>
        {/if}
        {#if result.location}<div class="up"><span class="muted small">Location</span> <Icon name="arrowRight" size={13} /> <code data-testid="test-location">{result.location}</code></div>{/if}
        {#if result.autoRedirect}<div class="field-warn" data-testid="test-auto-redirect">nginx auto-redirect: no route for this exact path, but proxy route <span class="mono">{winner}</span> is the same path with a trailing “/” → 301.</div>{/if}

        <div class="grid">
          {#if result.order.length}
            <div class="blk" data-testid="test-order">
              <div class="bh">Effective order ({method})</div>
              <ol class="ord">
                {#each result.order as o (o.routeId)}
                  <li class:win={o.routeId === winner} class:rw={result.rewrites.some((r) => r.routeId === o.routeId)} data-testid="test-order-{o.routeId}" data-win={o.routeId === winner ? '1' : undefined} title={o.why}>
                    <span class="mono xs rk">{o.rank}</span><span class="badge {GROUP_TONE[o.group]}">{groupLabel(o, result.order)}</span><span class="mono xs">{o.routeId}</span><span class="mono xs faint ellipsis">{pathOf(o.routeId)}</span>
                    {#if o.routeId === winner}<span class="badge green">wins</span>{:else if result.rewrites.some((r) => r.routeId === o.routeId)}<span class="badge amber">rewrote</span>{/if}
                  </li>
                {/each}
              </ol>
            </div>
          {/if}
          <div class="blk stack tight">
            {#if result.rewrites.length}
              <div data-testid="test-rewrites">
                <div class="bh">Internal rewrites (nginx “last”)</div>
                {#each result.rewrites as s, i (i)}<div class="up xs"><span class="mono faint">{s.routeId}</span><code>{s.from}</code><Icon name="arrowRight" size={12} /><code>{s.to}</code></div>{/each}
              </div>
            {/if}
            {#if t}
              <div data-testid="test-match"><div class="bh">Match</div><div class="xs"><span class="badge outline">{t.match.kind}</span> <span class="mono">{t.match.path}</span> — {t.match.why}</div></div>
            {/if}
            {#if result.captures.length > 1}
              <div data-testid="test-captures"><div class="bh">Captures</div><div class="xs mono">{#each result.captures.slice(1) as c, i (i)}<span class="cap">${i + 1} = “{c}”</span>{/each}</div></div>
            {/if}
            {#if t?.upstreamPath}
              <div data-testid="test-upstream-path"><div class="bh">Upstream path</div><div class="xs"><span class="badge outline">{t.upstreamPath.how === 'none' ? 'unchanged' : t.upstreamPath.how === 'strip' ? 'strip prefix' : 'rewrite'}</span> <code>{t.upstreamPath.from}</code> → <code>{t.upstreamPath.to}</code></div></div>
            {/if}
            {#if t && t.timeouts}
              <div class="xs" data-testid="test-timeouts"><span class="bh-in">Timeouts</span> connect {t.timeouts.connectSec} s · send {t.timeouts.sendSec} s · read {t.timeouts.readSec} s <span class="faint">(read = idle between reads; a stream that keeps sending is not cut)</span></div>
              <div class="xs" data-testid="test-buffering"><span class="bh-in">Buffering</span> request {t.buffering?.request ? 'buffered (≤ 8 MiB in memory)' : 'streamed'} · response {t.buffering?.response ? 'buffered' : 'flushed at once'} <span class="faint">(text/event-stream always flushes)</span></div>
              <div class="xs" data-testid="test-body"><span class="bh-in">Body limit</span> {bytesText(t.maxBodyBytes)} <span class="faint">(from the {t.maxBodyFrom})</span></div>
              <div class="xs" data-testid="test-ws"><span class="bh-in">WebSocket</span> {t.websocket ? 'allowed' : 'refused (400)'}</div>
            {/if}
          </div>
        </div>

        {#if t && t.requestHeaders.length}
          <div class="blk" data-testid="test-req-headers">
            <div class="bh">Request headers → container</div>
            <table class="hd">
              <tbody>
                {#each t.requestHeaders as h (h.name)}<tr data-testid="test-req-{h.name}"><td class="mono">{h.name}</td><td class="mono">{h.value}</td><td><span class="badge {h.source === 'route' ? 'blue' : ''}">{h.source}</span></td></tr>{/each}
                {#each t.removedRequestHeaders as h (h)}<tr><td class="mono"><s>{h}</s></td><td class="faint xs">not forwarded</td><td><span class="badge red">removed</span></td></tr>{/each}
              </tbody>
            </table>
          </div>
        {/if}
        {#if t}
          <div class="blk" data-testid="test-resp-headers" data-from={t.responseHeadersFrom}>
            <div class="bh">Response headers → browser <span class="faint">({t.responseHeadersFrom === 'none' ? 'none set' : `from the ${t.responseHeadersFrom}`})</span></div>
            {#if respRows.length}
              <table class="hd">
                <tbody>
                  {#each respRows as h, i (i)}
                    <tr data-testid="test-resp-{h.op}-{h.name}" class:off={h.op !== 'remove' && !applies && !h.always}>
                      <td><span class="badge {h.op === 'set' ? 'blue' : h.op === 'add' ? 'green' : 'red'}">{h.op}</span></td>
                      <td class="mono">{h.name}</td>
                      <td class="mono">{h.value}</td>
                      <td class="xs faint">{h.always ? 'always' : h.op === 'remove' ? 'from the container’s response' : applies ? '' : `not on ${result.status}`}</td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            {:else}
              <div class="xs faint">The browser gets the container’s headers unchanged.</div>
            {/if}
          </div>
        {/if}
        <details class="steps-d"><summary class="xs muted">All steps</summary><ol class="steps">{#each result.steps as s, i (i)}<li>{s}</li>{/each}</ol></details>
      </div>
    {:else}
      <p class="hint">Enter a URL and press Test to see the host match, the effective route order, rewrites, the upstream URL or the redirect, and every header, timeout and limit that applies.</p>
    {/if}
  </div>
</section>

<style>
  .tr {
    display: flex;
    gap: 8px;
    flex-wrap: wrap;
  }
  .meth {
    width: 110px;
    flex: none;
  }
  .tr .grow {
    flex: 1 1 260px;
    width: auto;
    min-width: 0;
  }
  .nr {
    display: flex;
    gap: 12px;
    flex-wrap: wrap;
    align-items: center;
  }
  .bnum {
    width: 130px;
  }
  .strong {
    font-weight: var(--fw-semibold);
  }
  .ellipsis {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
  }
  .card-head h3 {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    margin: 0;
    font-size: var(--fs-md);
  }
  .wrap {
    flex-wrap: wrap;
  }
  .bunit {
    width: 80px;
  }
  .res {
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    padding: 12px 14px;
    background: var(--surface-2);
    display: flex;
    flex-direction: column;
    gap: 10px;
    min-width: 0;
  }
  .code {
    font-size: 13px;
    height: 24px;
    padding: 0 10px;
    font-variant-numeric: tabular-nums;
  }
  .up {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
  }
  .res code {
    font-size: 12px;
    background: var(--surface);
    border: 1px solid var(--border);
    padding: 1px 6px;
    border-radius: 5px;
    overflow-wrap: anywhere;
  }
  .grid {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1.2fr);
    gap: 12px;
  }
  @media (max-width: 760px) {
    .grid {
      grid-template-columns: minmax(0, 1fr);
    }
  }
  .blk {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    padding: 8px 10px;
    min-width: 0;
    overflow-x: auto;
  }
  .stack.tight {
    gap: 8px;
  }
  .bh {
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--text-3);
    margin-bottom: 4px;
  }
  .bh-in {
    color: var(--text-3);
    margin-right: 4px;
  }
  .ord {
    margin: 0;
    padding: 0;
    list-style: none;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .ord li {
    display: flex;
    gap: 6px;
    align-items: center;
    padding: 2px 6px;
    border-radius: 5px;
    min-width: 0;
  }
  .ord li.win {
    background: var(--green-soft);
    outline: 1px solid var(--green);
  }
  .ord li.rw {
    background: var(--amber-soft);
  }
  .rk {
    width: 18px;
    color: var(--text-3);
  }
  .cap {
    margin-right: 10px;
  }
  .hd {
    border-collapse: collapse;
    font-size: 12px;
    width: 100%;
  }
  .hd td {
    padding: 3px 8px 3px 0;
    border-top: 1px solid var(--border);
    vertical-align: top;
    overflow-wrap: anywhere;
  }
  .hd tr.off {
    opacity: 0.5;
  }
  .steps {
    margin: 4px 0 0;
    padding-left: 20px;
    color: var(--text-2);
    font-size: var(--fs-sm);
    display: flex;
    flex-direction: column;
    gap: 3px;
  }
</style>
