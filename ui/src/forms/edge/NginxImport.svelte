<script lang="ts" module>
  export type NginxApplyInfo = { mode: 'append' | 'replace'; routes: number; hosts: number; fileName: string | null };
</script>

<script lang="ts">
  /**
   * "Import from nginx.conf…" (SPEC §12.7; §21 #102) — ONE import page body for the cloud editor and the agent GUI.
   * Paste or upload an nginx.conf → shared importNginx (containers + image ports of this site, live) → 2 What maps where
   * (summary + mapping table, status filter) → 3 Upstreams (name:port → container + port; suggestions prefilled) →
   * 4 Certificates (ssl_certificate → file, key → secret) → Import (Append / Replace, the resulting routes in effective
   * order + validation of the result). Nothing is written here: `onapply(edge, info)` hands the new edge to the caller,
   * which writes it into its working copy (cloud: site draft; agent: local edit) — and may confirm Replace first.
   */
  import type { Config, Edge, Site } from '@opm/shared/types';
  import { applyNginxImport, importNginx, type NginxCertBindings, type NginxImportResult, type NginxMapStatus, type NginxUpstreamRef } from '@opm/shared/nginx-import';
  import { effectiveRouteOrder } from '@opm/shared/edge-resolve';
  import Combobox from '../../primitives/Combobox.svelte';
  import Select from '../../primitives/Select.svelte';
  import SegmentedControl from '../../primitives/SegmentedControl.svelte';
  import FormSection from '../../primitives/FormSection.svelte';
  import Issues from '../../primitives/Issues.svelte';
  import Btn from '../../primitives/Btn.svelte';
  import Icon from '../../primitives/Icon.svelte';
  import { useConfigHost, issuesUnder } from '../host';
  import { fileOptions, secretOptions } from '../pickers';
  import { GROUP_TONE, actionSummary, groupLabel, importContainers } from './edge';

  interface Props {
    site: Site;
    disabled?: boolean;
    /** the new edge of the site (applyNginxImport result); the caller writes it and navigates back */
    onapply: (edge: Edge, info: NginxApplyInfo) => void;
    oncancel?: () => void;
  }
  let { site, disabled = false, onapply, oncancel }: Props = $props();
  const host = useConfigHost();
  const ro = $derived(disabled || host.readOnly);
  const slug = $derived(site.siteSlug);

  let text = $state('');
  let fileName = $state<string | null>(null);
  let fileErr = $state<string | null>(null);
  let mode = $state<'append' | 'replace'>('append');
  let filter = $state<NginxMapStatus | 'all'>('all');
  let binds = $state<Record<string, { certFileSlug: string; keySecretSlug: string }>>({});

  const containers = $derived(importContainers(host, site));
  type Target = { containerSlug: string; portName: string };
  /** per upstream "<name>:<port>": the chosen target (null = left unresolved); unresolved upstreams start with the suggestion */
  let maps = $state<Record<string, Target | null>>({});
  let touchedUp = $state<Record<string, boolean>>({});
  type Parsed = { ok: true; r: NginxImportResult } | { ok: false; error: string } | null;
  const base = $derived.by<Parsed>(() => {
    if (!text.trim()) return null;
    try {
      return { ok: true, r: importNginx(text, { containers }) };
    } catch (e) {
      return { ok: false, error: (e as Error).message };
    }
  });
  const ups = $derived<NginxUpstreamRef[]>(base?.ok ? (base.r.upstreams ?? []) : []);
  $effect(() => {
    const next: Record<string, Target | null> = {};
    let changed = false;
    for (const u of ups) {
      if (u.key in maps) next[u.key] = maps[u.key]!;
      else {
        next[u.key] = u.how === 'name' ? null : u.suggestion ? { ...u.suggestion } : null;
        changed = true;
      }
    }
    if (changed || Object.keys(maps).length !== Object.keys(next).length) {
      maps = next;
      touchedUp = Object.fromEntries(Object.entries(touchedUp).filter(([k]) => k in next));
    }
  });
  const upstreamOpt = $derived(Object.fromEntries(Object.entries(maps).filter(([, t]) => t && t.containerSlug && t.portName)) as Record<string, Target>);
  const parsed = $derived.by<Parsed>(() => {
    if (!base || !base.ok || !Object.keys(upstreamOpt).length) return base;
    try {
      return { ok: true, r: importNginx(text, { containers, upstreams: upstreamOpt }) };
    } catch (e) {
      return { ok: false, error: (e as Error).message };
    }
  });
  const contOpts = $derived(containers.map((c) => ({ value: c.containerSlug, label: c.containerSlug, hint: c.ports.map((p) => `${p.portName} ${p.port}${p.edge ? '' : ' (edge off)'}`).join(' · ') || 'no ports' })));
  const portOptsOf = (cs: string) => (containers.find((c) => c.containerSlug === cs)?.ports ?? []).map((p) => ({ value: p.portName, label: p.portName, hint: `${p.port}`, disabled: !p.edge, badge: p.edge ? undefined : { text: 'edge off', tone: 'gray' as const } }));
  function setUp(key: string, t: Target | null) {
    maps = { ...maps, [key]: t };
    touchedUp = { ...touchedUp, [key]: true };
  }
  function pickContainer(key: string, cs: string, port: number) {
    const edge = (containers.find((x) => x.containerSlug === cs)?.ports ?? []).filter((p) => p.edge);
    const p = edge.find((x) => x.port === port) ?? edge[0];
    setUp(key, { containerSlug: cs, portName: p?.portName ?? '' });
  }
  const isSuggested = (u: NginxUpstreamRef) => !touchedUp[u.key] && !!u.suggestion && maps[u.key]?.containerSlug === u.suggestion.containerSlug && maps[u.key]?.portName === u.suggestion.portName;
  const upState = (u: NginxUpstreamRef): 'name' | 'mapped' | 'unresolved' => (u.how === 'name' ? 'name' : maps[u.key]?.containerSlug && maps[u.key]?.portName ? 'mapped' : 'unresolved');
  const unresolvedUps = $derived(ups.filter((u) => upState(u) === 'unresolved').length);
  const result = $derived(parsed?.ok ? parsed.r : null);
  $effect(() => {
    for (const c of result?.certs ?? []) if (!binds[c.hostname]) binds[c.hostname] = { certFileSlug: '', keySecretSlug: '' };
  });
  const STATUSES: NginxMapStatus[] = ['ok', 'note', 'default', 'ignored', 'unsupported', 'error'];
  const TONE: Record<NginxMapStatus, string> = { ok: 'green', note: 'blue', default: '', ignored: '', unsupported: 'amber', error: 'red' };
  const HELP: Record<NginxMapStatus, string> = {
    ok: 'mapped 1:1',
    note: 'mapped, with a difference to read',
    default: 'nothing to set: OPM does this by default',
    ignored: 'no meaning for OPM',
    unsupported: 'not mapped: handle it by hand',
    error: 'mapped but needs input (e.g. the upstream container is not on this site)',
  };
  const rows = $derived((result?.rows ?? []).filter((r) => filter === 'all' || r.status === filter));
  const unresolvedRows = $derived((result?.rows ?? []).filter((r) => r.status === 'error').length);
  const bindings = $derived<NginxCertBindings>(Object.fromEntries(Object.entries(binds).filter(([, b]) => b.certFileSlug && b.keySecretSlug)));
  const merged = $derived(result ? applyNginxImport($state.snapshot(site.config.edge) as Edge, $state.snapshot(result) as NginxImportResult, mode, bindings) : null);
  const candidate = $derived.by<Config | null>(() => {
    if (!merged) return null;
    const cfg = host.config();
    const s = structuredClone($state.snapshot(site)) as Site;
    s.config.edge = merged;
    return { ...cfg, sites: cfg.sites.map((x) => (x.siteSlug === slug ? s : x)) };
  });
  const issues = $derived(candidate ? issuesUnder(host.validate(candidate), `sites[${slug}].config.edge`) : []);
  const order = $derived(merged ? effectiveRouteOrder(merged.routes) : []);
  const noTarget = $derived(merged ? merged.routes.filter((r) => r.action.type === 'proxy' && (!r.action.containerSlug || !r.action.portName)).length : 0);
  const fileOpts = $derived(fileOptions(host));
  const secOpts = $derived(secretOptions(host));
  const certStep = $derived(3 + (ups.length ? 1 : 0));
  const importStep = $derived(3 + (ups.length ? 1 : 0) + (result?.certs.length ? 1 : 0));

  async function onfile(e: Event & { currentTarget: HTMLInputElement }) {
    const f = e.currentTarget.files?.[0];
    e.currentTarget.value = '';
    fileErr = null;
    if (!f) return;
    if (f.size > 2 * 1024 * 1024) {
      fileErr = 'The file is larger than 2 MiB — is it really an nginx.conf?';
      return;
    }
    text = await f.text();
    fileName = f.name;
  }
  function apply() {
    if (!merged || ro) return;
    const n = mode === 'replace' ? merged.routes.length : merged.routes.length - site.config.edge.routes.length;
    onapply($state.snapshot(merged) as Edge, { mode, routes: n, hosts: merged.hosts.length, fileName });
  }
</script>

<div class="stack imp" data-testid="nginx-import">
  <FormSection n={1} title="nginx.conf" sub="Paste the file (or upload it). Nothing changes until you press Import." testid="ngx-source">
    {#snippet actions()}
      {#if !ro}<label class="btn sm" data-testid="ngx-upload"><Icon name="upload" size={13} />Upload file…<input type="file" accept=".conf,.txt,text/plain" class="sr-only" onchange={onfile} data-testid="ngx-file" /></label>{/if}
    {/snippet}
    <div class="stack">
      <textarea class="textarea mono src" bind:value={text} oninput={() => (fileName = null)} rows="12" spellcheck="false" disabled={ro} placeholder={'http {\n  server {\n    listen 80;\n    location /api/ { proxy_pass http://api:8080; }\n  }\n}'} aria-label="nginx.conf" data-testid="ngx-text"></textarea>
      <div class="hint">Containers on this site the importer resolves <code>proxy_pass http://name:port</code> against: {containers.map((c) => `${c.containerSlug}:${c.ports.map((p) => p.port).join('/') || '?'}`).join(', ') || 'none'}</div>
      {#if fileErr}<div class="banner error small" role="alert"><Icon name="alert" size={14} /><div class="grow">{fileErr}</div></div>{/if}
      {#if parsed && !parsed.ok}<div class="banner error small" data-testid="ngx-crash"><Icon name="alert" size={14} /><div class="grow">The importer failed: {parsed.error}</div></div>{/if}
      {#if result?.parseErrors.length}
        <div class="banner error small" data-testid="ngx-parse-errors"><Icon name="alert" size={14} /><div class="grow">{#each result.parseErrors as e, i (i)}<div>line {e.line}: {e.message}</div>{/each}</div></div>
      {/if}
    </div>
  </FormSection>

  {#if result}
    <FormSection n={2} title="What maps where" testid="ngx-map">
      <div class="stack">
        <div class="chips" data-testid="ngx-summary">
          <strong>{result.summary.routes} routes · {result.summary.hosts} host{result.summary.hosts === 1 ? '' : 's'}</strong>
          {#each STATUSES as s (s)}<span class="badge {TONE[s]}" data-testid="ngx-count-{s}" title={HELP[s]}>{result.summary[s]} {s}</span>{/each}
        </div>
        {#if unresolvedRows}<div class="banner warn small" data-testid="ngx-unresolved"><Icon name="alert" size={14} /><div class="grow">{unresolvedRows} row{unresolvedRows === 1 ? '' : 's'} need input — usually an upstream not mapped in the Upstreams step below. The route is still imported with an empty target: pick the container on the route page afterwards (it shows as a validation error).</div></div>{/if}
        <div class="chips" role="group" aria-label="Filter by status">
          <button type="button" class="fchip" class:on={filter === 'all'} aria-pressed={filter === 'all'} onclick={() => (filter = 'all')} data-testid="ngx-filter-all">all {result.rows.length}</button>
          {#each STATUSES as s (s)}{#if result.summary[s]}<button type="button" class="fchip" class:on={filter === s} aria-pressed={filter === s} onclick={() => (filter = s)} data-testid="ngx-filter-{s}">{s} {result.summary[s]}</button>{/if}{/each}
        </div>
        <div class="mapwrap">
          <table class="table map" data-testid="ngx-mapping">
            <thead><tr><th class="ln">Line</th><th>Context</th><th>Directive</th><th>Status</th><th>OPM target</th><th>Note</th></tr></thead>
            <tbody>
              {#each rows as r, i (i)}
                <tr class:err={r.status === 'error'} class:warnr={r.status === 'unsupported'} data-testid="ngx-row" data-status={r.status} data-line={r.line} data-directive={r.directive}>
                  <td class="ln mono">{r.line}</td>
                  <td class="small muted ctx">{r.context}</td>
                  <td class="mono small dir">{r.text}</td>
                  <td><span class="badge {TONE[r.status]}">{r.status}</span></td>
                  <td class="mono small">{r.target ?? '—'}</td>
                  <td class="small">{r.note}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </div>
    </FormSection>

    {#if ups.length}
      <FormSection n={3} title="Upstreams" sub="proxy_pass http://name:port → a container + port of this site. Unresolved upstreams are prefilled with a suggestion (change or clear it); an upstream left unresolved imports its routes with an empty target." testid="ngx-upstreams">
        {#snippet actions()}<span class="badge {unresolvedUps ? 'red' : 'green'}" data-testid="ngx-up-unresolved">{unresolvedUps ? `${unresolvedUps} unresolved` : 'all resolved'}</span>{/snippet}
        <div class="scroll-x">
          <table class="table ups">
            <thead><tr><th>Upstream</th><th>Routes</th><th>Status</th><th>Container</th><th>Port</th><th></th></tr></thead>
            <tbody>
              {#each ups as u, i (u.key)}
                {@const st = upState(u)}
                {@const t = maps[u.key]}
                <tr data-testid="ngx-up-{i}" data-key={u.key} data-state={st} class:err={st === 'unresolved'}>
                  <td class="mono nowrap"><strong>{u.key}</strong></td>
                  <td class="small" title={u.routes.join('\n')}>{u.routes.length} <span class="faint">{u.routes.slice(0, 3).join(', ')}{u.routes.length > 3 ? ' …' : ''}</span></td>
                  <td>
                    {#if st === 'name'}<span class="badge green">resolved by name</span>
                    {:else if st === 'mapped'}<span class="badge blue">mapped</span>{#if isSuggested(u)} <span class="badge violet" data-testid="ngx-up-suggested-{i}">suggested</span>{/if}
                    {:else}<span class="badge red">unresolved</span>{/if}
                  </td>
                  {#if st === 'name'}
                    <td class="mono small" colspan="3">{u.resolved?.containerSlug}:{u.resolved?.portName} <span class="faint">(same name)</span></td>
                  {:else}
                    <td class="cb"><Select value={t?.containerSlug ?? ''} options={contOpts} mono size="sm" placeholder="pick a container" disabled={ro} ariaLabel="Container for {u.key}" testid="ngx-up-c-{i}" onchange={(v) => pickContainer(u.key, v, u.port)} /></td>
                    <td class="cb"><Select value={t?.portName ?? ''} options={portOptsOf(t?.containerSlug ?? '')} mono size="sm" placeholder="port" disabled={ro || !t?.containerSlug} ariaLabel="Port for {u.key}" testid="ngx-up-p-{i}" onchange={(v) => t && setUp(u.key, { containerSlug: t.containerSlug, portName: v })} /></td>
                    <td>{#if t && !ro}<Btn size="sm" variant="ghost" icon="x" aria-label="Clear {u.key}" onclick={() => setUp(u.key, null)} data-testid="ngx-up-clear-{i}" />{/if}</td>
                  {/if}
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </FormSection>
    {/if}

    {#if result.certs.length}
      <FormSection n={certStep} title="Certificates" sub="ssl_certificate → an OPM file · ssl_certificate_key → an OPM secret" testid="ngx-certs">
        <div class="stack">
          {#each result.certs as c, i (c.hostname)}
            <div class="cert" data-testid="ngx-cert-{i}">
              <div class="small"><Icon name="lock" size={12} /> <span class="mono"><strong>{c.hostname}</strong></span></div>
              <div class="grid-2">
                <div class="stack tight">
                  <span class="hint">Certificate <code>{c.certPath ?? '—'}</code></span>
                  {#if binds[c.hostname]}<Combobox value={binds[c.hostname]!.certFileSlug} options={fileOpts} mono placeholder="choose a file (PEM chain)" emptyText="No files — add one under Files" ariaLabel="Certificate file for {c.hostname}" disabled={ro} testid="ngx-cert-file-{i}" onchange={(v) => (binds[c.hostname]!.certFileSlug = v)} />{/if}
                </div>
                <div class="stack tight">
                  <span class="hint">Key <code>{c.keyPath ?? '—'}</code></span>
                  {#if binds[c.hostname]}<Combobox value={binds[c.hostname]!.keySecretSlug} options={secOpts} mono placeholder="choose a secret (PEM key)" emptyText="No secrets — add one under Secrets" ariaLabel="Key secret for {c.hostname}" disabled={ro} testid="ngx-cert-key-{i}" onchange={(v) => (binds[c.hostname]!.keySecretSlug = v)} />{/if}
                </div>
              </div>
              <div class="small {bindings[c.hostname] ? 'ok-text' : 'faint'}" data-testid="ngx-cert-state-{i}">{bindings[c.hostname] ? 'bound: TLS mode files with this certificate + key' : 'unbound: the host is imported in TLS mode files without a certificate — a validation error until you pick both (here or later in the host)'}</div>
            </div>
          {/each}
        </div>
      </FormSection>
    {/if}

    <FormSection n={importStep} title="Import" testid="ngx-import">
      <div class="stack">
        <SegmentedControl
          options={[
            { value: 'append' as const, label: 'Append', title: 'Keep the current edge, add the imported hosts and routes' },
            { value: 'replace' as const, label: 'Replace', title: 'The edge becomes exactly the import' },
          ]}
          bind:value={mode}
          disabled={ro}
          ariaLabel="Import mode"
          testid="ngx-mode"
        />
        <p class="hint" data-testid="ngx-mode-help">
          {#if mode === 'append'}Append: keep the current ports, body limit, hosts and {site.config.edge.routes.length} route{site.config.edge.routes.length === 1 ? '' : 's'}; add new hosts and the imported routes (ids renamed -2, -3 … on a clash; scoped to the imported server names). Settings that differ from the current edge are copied into each route.{:else}Replace: the site edge becomes the imported one — ports, body limit, hosts and routes. The {site.config.edge.routes.length} current route{site.config.edge.routes.length === 1 ? '' : 's'} and {site.config.edge.hosts.length} host{site.config.edge.hosts.length === 1 ? '' : 's'} are dropped.{/if}
        </p>
        {#if merged}
          <div class="blk" data-testid="ngx-result">
            <div class="row-w">
              <span class="hint">Result: {merged.routes.length} routes in effective order · {merged.hosts.length} host{merged.hosts.length === 1 ? '' : 's'}</span>
              {#if noTarget}<span class="badge red" data-testid="ngx-no-target">{noTarget} without a container</span>{/if}
            </div>
            <ol class="ord">
              {#each order as o (o.routeId)}
                {@const r = merged.routes.find((x) => x.routeId === o.routeId)!}
                <li data-testid="ngx-route-{r.routeId}"><span class="badge {GROUP_TONE[o.group]}">{groupLabel(o, order)}</span><span class="mono small">{r.match.path}</span><span class="small faint mono ell">{actionSummary(r)}</span></li>
              {/each}
            </ol>
          </div>
          <Issues {issues} title="Validation after the import" testid="ngx-issues" />
          {#if !issues.length}<div class="banner ok small"><Icon name="check" size={14} /><div class="grow">The imported edge is valid.</div></div>{/if}
        {/if}
        <div class="row-w end">
          {#if oncancel}<Btn onclick={oncancel} data-testid="ngx-cancel">Cancel</Btn>{/if}
          {#if !ro}<Btn variant={mode === 'replace' ? 'danger' : 'primary'} icon="download" onclick={apply} disabled={!merged} data-testid="ngx-apply">{mode === 'replace' ? 'Replace the edge' : 'Append routes'}</Btn>{/if}
        </div>
      </div>
    </FormSection>
  {/if}
</div>

<style>
  .src {
    width: 100%;
    font-size: 12px;
    min-height: 200px;
  }
  .chips,
  .row-w {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: center;
  }
  .row-w.end {
    justify-content: flex-end;
  }
  .fchip {
    font: inherit;
    font-size: 11.5px;
    padding: 2px 9px;
    border-radius: var(--r-full);
    cursor: pointer;
    border: 1px solid var(--border-strong);
    background: var(--surface);
    color: var(--text-2);
  }
  .fchip.on {
    background: var(--accent-soft);
    border-color: var(--accent);
    color: var(--accent-text);
  }
  .mapwrap {
    max-height: 560px;
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
  }
  .scroll-x {
    overflow-x: auto;
  }
  .map thead th {
    position: sticky;
    top: 0;
    z-index: 1;
    background: var(--surface);
  }
  .ln {
    width: 48px;
    text-align: right;
  }
  .ups tr.err td,
  .map tr.err td {
    background: var(--red-soft);
  }
  .map tr.warnr td {
    background: var(--amber-soft);
  }
  .ups .cb {
    min-width: 170px;
  }
  .ctx {
    min-width: 160px;
    max-width: 240px;
  }
  .dir {
    min-width: 220px;
    max-width: 420px;
    overflow-wrap: anywhere;
  }
  .nowrap {
    white-space: nowrap;
  }
  .cert {
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    padding: 10px 12px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .ok-text {
    color: var(--green-text);
  }
  .blk {
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    padding: 8px 10px;
    background: var(--surface-2);
  }
  .ord {
    margin: 6px 0 0;
    padding: 0;
    list-style: none;
    display: flex;
    flex-direction: column;
    gap: 3px;
  }
  .ord li {
    display: flex;
    gap: 8px;
    align-items: center;
    min-width: 0;
  }
  .ell {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
  }
  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
  }
</style>
