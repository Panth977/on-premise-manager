<script lang="ts">
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import { base } from '$app/paths';
  import type { NetcheckKind, NetcheckResult, ToolsContainer } from '@opm/shared/agent-tools';
  import { toasts } from '$lib/state/toast.svelte';
  import { errMessage, issuesOf } from '$lib/api';
  import { tools } from '$lib/tools/api';
  import LogViewer from '$lib/tools/LogViewer.svelte';
  import PageTabs, { tabOf } from '$lib/ui/PageTabs.svelte';
  import ChooseSite from '$lib/ui/ChooseSite.svelte';
  import PageHead from '$lib/ui/PageHead.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Select from '$lib/ui/Select.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Time from '$lib/ui/Time.svelte';
  import AnsiLog from '$lib/ui/AnsiLog.svelte';

  const slug = $derived(page.params.siteSlug ?? '*');
  const TABS = [
    { k: 'checks', label: 'Checks', icon: 'network' },
    { k: 'access', label: 'Edge access log', icon: 'logs' },
  ];
  const tab = $derived(tabOf(page.url, ['checks', 'access']));
  let cons = $state<ToolsContainer[]>([]);
  $effect(() => {
    const s = slug;
    if (s === '*') return;
    untrack(async () => {
      try {
        cons = (await tools.toolsContainers({ siteSlug: s })).items.filter((c) => c.siteSlug === s && c.state === 'running' && !c.failedCopy);
      } catch {
        cons = [];
      }
    });
  });

  let from = $state<string>('host');
  let check = $state<NetcheckKind>('http');
  let target = $state('');
  let timeoutSec = $state(10);
  let busy = $state(false);
  let fieldErr = $state<string | null>(null);
  let results = $state<NetcheckResult[]>([]);
  const PH: Record<NetcheckKind, string> = { dns: 'redis.db.opm or example.com', tcp: 'redis.db.opm:6379', http: 'http://hello-api:3000/health', ping: '10.0.1.11' };
  const HINT: Record<NetcheckKind, string> = {
    dns: 'Resolves a name with the resolver of the host / container.',
    tcp: 'Opens a TCP connection to host:port and closes it.',
    http: 'GET with timing (DNS, connect, TLS, first byte) and the response headers. No body is shown.',
    ping: '3 ICMP echo requests (needs the ping tool inside a container).',
  };
  async function run() {
    if (!target.trim()) {
      fieldErr = 'Enter a target';
      return;
    }
    busy = true;
    fieldErr = null;
    try {
      const r = await tools.netcheck({ siteSlug: slug, body: { from: from === 'host' ? { kind: 'host' } : { kind: 'container', containerSlug: from }, check, target: target.trim(), timeoutSec } });
      results = [r, ...results].slice(0, 30);
    } catch (e) {
      const iss = issuesOf(e);
      if (iss.length) fieldErr = iss[0].message;
      else toasts.err(e, 'Check failed');
      if (!iss.length) fieldErr = errMessage(e);
    } finally {
      busy = false;
    }
  }
  const tm = (r: NetcheckResult) => r.timing;
  function segs(r: NetcheckResult) {
    const t = tm(r);
    if (!t || !t.totalMs) return [];
    const parts = [
      { k: 'DNS', v: t.dnsMs ?? 0, c: 'var(--violet)' },
      { k: 'connect', v: t.connectMs ?? 0, c: 'var(--blue)' },
      { k: 'TLS', v: t.tlsMs ?? 0, c: 'var(--amber)' },
      { k: 'wait', v: Math.max(0, (t.ttfbMs ?? 0) - (t.dnsMs ?? 0) - (t.connectMs ?? 0) - (t.tlsMs ?? 0)), c: 'var(--accent)' },
    ];
    const rest = Math.max(0, t.totalMs - parts.reduce((a, p) => a + p.v, 0));
    parts.push({ k: 'download', v: rest, c: 'var(--green)' });
    return parts.filter((p) => p.v > 0).map((p) => ({ ...p, pct: (100 * p.v) / t.totalMs }));
  }
</script>

<PageHead icon="network" eyebrow="Docker · site {slug}" title="Network" sub="DNS, TCP, HTTP and ping checks from the host or from inside a container, and the edge access log." />

{#if slug === '*'}
  <ChooseSite what="network checks and the edge access log" />
{:else}
<PageTabs tabs={TABS} label="Network sections" testid="net-tabs" />
{#if tab === 'access'}
  <LogViewer site={slug} src="access" />
{:else}
<div class="stack">
  <section class="card">
    <div class="card-body form">
      <div class="field">
        <label class="label" for="nc-from">From</label>
        <Select id="nc-from" bind:value={from} testid="nc-from" options={[{ value: 'host', label: `Host of ${slug}`, icon: 'server' }, ...cons.map((c) => ({ value: c.ref, label: c.ref, hint: c.image, icon: 'container', group: 'Containers' }))]} />
      </div>
      <div class="field">
        <span class="label">Check</span>
        <SegmentedControl label="Check" options={[{ value: 'dns', label: 'DNS' }, { value: 'tcp', label: 'TCP' }, { value: 'http', label: 'HTTP GET' }, { value: 'ping', label: 'Ping' }]} value={check} onchange={(v) => ((check = v as NetcheckKind), (fieldErr = null))} testid="nc-check" />
      </div>
      <div class="field tgt">
        <label class="label" for="nc-target">Target <span class="info" title={HINT[check]}><Icon name="info" size={12} /></span></label>
        <input id="nc-target" class="input mono" class:invalid={!!fieldErr} placeholder={PH[check]} bind:value={target} onkeydown={(e) => e.key === 'Enter' && run()} data-testid="nc-target" />
        {#if fieldErr}<div class="field-err">{fieldErr}</div>{/if}
      </div>
      <div class="field to">
        <label class="label" for="nc-to">Timeout</label>
        <Select id="nc-to" value={String(timeoutSec)} onchange={(v) => (timeoutSec = Number(v))} ariaLabel="Timeout" testid="nc-timeout" options={[3, 5, 10, 20, 30].map((s) => ({ value: String(s), label: `${s} s` }))} />
      </div>
      <div class="go"><Btn variant="primary" icon="play" busy={busy} onclick={run} data-testid="nc-run">Run</Btn></div>
    </div>
    <div class="note faint xs"><Icon name="info" size={12} /> Inside a container the check uses the tools of its image (getent / nslookup, nc, wget / curl, ping); every check is written to Activities.</div>
  </section>

  {#if results.length}
    <div class="results" data-testid="nc-results">
      {#each results as r (r.at + r.target + r.check)}
        <article class="card res" class:ok={r.ok} class:bad={!r.ok} data-testid="nc-result">
          <header>
            <span class="st"><Icon name={r.ok ? 'check' : 'x'} size={15} /></span>
            <span class="badge outline">{r.check.toUpperCase()}</span>
            <strong class="mono tg ellipsis" title={r.target}>{r.target}</strong>
            <span class="muted small">from {r.from === 'host' ? 'host' : `container ${r.from}`}</span>
            <span class="grow"></span>
            <span class="small faint"><Time ts={r.at} /></span>
          </header>
          <div class="sum"><span class:ok-text={r.ok} class:bad-text={!r.ok}>{r.summary}</span>{#if r.error && r.error !== r.summary}<span class="muted small"> · {r.error}</span>{/if}{#if r.tool}<span class="chip" title="tool used inside the container">{r.tool}</span>{/if}</div>
          {#if r.timing}
            <div class="tbar" title="total {r.timing.totalMs} ms">{#each segs(r) as s (s.k)}<i style="width:{s.pct}%;background:{s.c}"></i>{/each}</div>
            <div class="tleg xs muted">{#each segs(r) as s (s.k)}<span><i style="background:{s.c}"></i>{s.k} {Math.round(s.v)} ms</span>{/each}<span class="strong">total {Math.round(r.timing.totalMs)} ms</span></div>
          {/if}
          {#if r.addresses?.length}<div class="small"><span class="muted">Addresses</span> <span class="mono">{r.addresses.join(', ')}</span></div>{/if}
          {#if r.headers && Object.keys(r.headers).length}
            <details>
              <summary class="small">Response headers ({Object.keys(r.headers).length})</summary>
              <table class="table compact hd"><tbody>{#each Object.entries(r.headers) as [k, v] (k)}<tr><td class="mono xs">{k}</td><td class="mono xs brk">{v}</td></tr>{/each}</tbody></table>
            </details>
          {/if}
          {#if r.output}
            <details open={!r.ok}>
              <summary class="small">Output</summary>
              <AnsiLog text={r.output} class="out" empty="" />
            </details>
          {/if}
        </article>
      {/each}
    </div>
  {/if}

  <section class="card links">
    <div class="card-body row wrap">
      <Icon name="globe" size={16} />
      <span class="grow small">Testing how the edge routes a URL? Use <strong>Test a URL</strong> in the edge configuration, and see real requests in the access log.</span>
      <a class="btn sm" href="{base}/app/{slug}/config/edge"><Icon name="globe" size={14} />Test a URL</a>
      <a class="btn sm" href="?tab=access" data-testid="net-access-link"><Icon name="logs" size={14} />Access log</a>
    </div>
  </section>
</div>
{/if}
{/if}

<style>
  .form {
    display: flex;
    flex-wrap: wrap;
    gap: 14px 16px;
    align-items: flex-start;
  }
  .form .field {
    margin: 0;
  }
  .tgt {
    flex: 1;
    min-width: 240px;
  }
  .to {
    width: 96px;
  }
  .form :global(#nc-from) {
    min-width: 220px;
  }
  .go {
    padding-top: 22px;
  }
  .info {
    color: var(--text-3);
    cursor: help;
  }
  .note {
    display: flex;
    gap: 6px;
    align-items: center;
    padding: 0 16px 12px;
  }
  .results {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .res {
    padding: 12px 16px;
    display: flex;
    flex-direction: column;
    gap: 8px;
    border-left: 3px solid var(--border);
  }
  .res.ok {
    border-left-color: var(--green);
  }
  .res.bad {
    border-left-color: var(--red);
  }
  header {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
    flex-wrap: wrap;
  }
  .st {
    display: inline-grid;
    place-items: center;
    width: 22px;
    height: 22px;
    border-radius: 50%;
  }
  .ok .st {
    background: var(--green-soft);
    color: var(--green-text);
  }
  .bad .st {
    background: var(--red-soft);
    color: var(--red-text);
  }
  .tg {
    max-width: 420px;
  }
  .sum {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
    font-weight: var(--fw-medium);
  }
  .tbar {
    display: flex;
    height: 8px;
    border-radius: 4px;
    overflow: hidden;
    background: var(--bg-sunken);
  }
  .tbar i {
    display: block;
    height: 100%;
  }
  .tleg {
    display: flex;
    gap: 12px;
    flex-wrap: wrap;
  }
  .tleg span {
    display: inline-flex;
    gap: 5px;
    align-items: center;
  }
  .tleg i {
    width: 8px;
    height: 8px;
    border-radius: 2px;
  }
  details summary {
    cursor: pointer;
    color: var(--text-2);
  }
  .hd {
    margin-top: 6px;
  }
  .brk {
    overflow-wrap: anywhere;
  }
  :global(.logbox.out) {
    margin-top: 6px;
    max-height: 220px;
  }
  .links .row {
    gap: 10px;
  }
  .row.wrap {
    flex-wrap: wrap;
  }
</style>
