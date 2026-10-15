<script lang="ts" module>
  import type { EdgeHost, EdgeTls, Site } from '@opm/shared/types';
  /** A blank host for "Add host". */
  export function newHost(): EdgeHost {
    return { hostname: '', tls: { mode: 'none' }, redirectHttpToHttps: false, responseHeaders: { set: {}, add: {}, remove: [], always: [] } };
  }
  /** Write an edited host into the site (rename → routes follow the new hostname). Same rule in both GUIs. */
  export function applyHost(site: Site, original: EdgeHost | null, next: EdgeHost) {
    const hosts = site.config.edge.hosts;
    const clean: EdgeHost = { ...next, hostname: next.hostname.trim(), redirectHttpToHttps: next.tls.mode === 'none' ? false : next.redirectHttpToHttps };
    if (original) {
      const i = hosts.findIndex((h) => h.hostname === original.hostname);
      if (i >= 0) hosts[i] = clean;
      if (original.hostname !== clean.hostname) for (const r of site.config.edge.routes) r.hosts = r.hosts.map((x) => (x === original.hostname ? clean.hostname : x));
    } else hosts.push(clean);
  }
  export type TlsMode = EdgeTls['mode'];
</script>

<script lang="ts">
  /**
   * Edge host form (SPEC §12, §17 / §18 Edge; §21 #102) — ONE form for cloud add / edit, agent read and agent local edit.
   * Wide drawer content; the caller owns the Drawer + Apply / Close buttons and calls `applyHost`.
   * Fields: Hostname · TLS mode · ACME email · Challenge · DNS provider · DNS token secret · Certificate file · Key secret ·
   * Redirect HTTP → HTTPS · Response headers. `valid` (bindable) = no errors (Apply enabled).
   */
  import { edgeExprContext } from '@opm/shared/validate';
  import type { ValidationIssue } from '@opm/shared/types';
  import Field from '../../primitives/Field.svelte';
  import ExprInput from '../../primitives/ExprInput.svelte';
  import SegmentedControl from '../../primitives/SegmentedControl.svelte';
  import Combobox from '../../primitives/Combobox.svelte';
  import Toggle from '../../primitives/Toggle.svelte';
  import Issues from '../../primitives/Issues.svelte';
  import ResponseHeadersEditor from './ResponseHeadersEditor.svelte';
  import { DNS_PROVIDERS } from './headers';
  import { useConfigHost, issuesUnder, type FormMode } from '../host';
  import { fileOptions, secretOptions } from '../pickers';

  interface Props {
    value: EdgeHost;
    site: Site;
    /** the host being edited (null = add) */
    original: EdgeHost | null;
    mode: FormMode;
    /** show errors of untouched fields too (after the first Apply attempt; edit mode always) */
    submitted?: boolean;
    valid?: boolean;
  }
  let { value = $bindable(), site, original, mode, submitted = false, valid = $bindable(true) }: Props = $props();
  const host = useConfigHost();
  const ro = $derived(mode === 'read' || host.readOnly);
  let respErrs = $state<string[]>([]);

  const ctx = $derived(edgeExprContext(host.config(), host.deploymentId, site.siteSlug));
  const candidate = $derived.by(() => {
    const cfg = host.config();
    const s = structuredClone($state.snapshot(site)) as Site;
    const next = $state.snapshot(value) as EdgeHost;
    const i = original ? s.config.edge.hosts.findIndex((h) => h.hostname === original.hostname) : -1;
    if (i >= 0) s.config.edge.hosts[i] = next;
    else s.config.edge.hosts.push(next);
    return { ...cfg, sites: cfg.sites.map((x) => (x.siteSlug === s.siteSlug ? s : x)) };
  });
  const prefix = $derived(`sites[${site.siteSlug}].config.edge.hosts[${value.hostname.trim() || original?.hostname || ''}]`);
  const all = $derived(ro ? [] : issuesUnder(host.validate(candidate), prefix));
  const dupe = $derived(!!value.hostname.trim() && site.config.edge.hosts.some((h) => h.hostname.toLowerCase() === value.hostname.trim().toLowerCase() && h.hostname !== original?.hostname));
  const show = $derived(submitted || mode === 'edit');
  const at = (p: string, deep = false): ValidationIssue[] => (show ? all.filter((i) => i.path === `${prefix}.${p}` || (deep && i.path.startsWith(`${prefix}.${p}`))) : []);
  const rest = $derived(show ? all.filter((i) => !['hostname', 'tls.mode', 'tls.email', 'tls.challenge', 'tls.dnsProvider', 'tls.dnsTokenSecretSlug', 'tls.certFileSlug', 'tls.keySecretSlug', 'redirectHttpToHttps'].some((p) => i.path === `${prefix}.${p}`) && !i.path.startsWith(`${prefix}.responseHeaders`)) : []);
  $effect(() => {
    valid = !ro && !!value.hostname.trim() && !dupe && respErrs.length === 0 && !all.some((i) => i.severity === 'error');
  });

  const secOpts = $derived(secretOptions(host));
  const fileOpts = $derived(fileOptions(host));
  const MODES = [
    { value: 'none' as const, label: 'none', title: 'HTTP only' },
    { value: 'internal' as const, label: 'internal', title: 'Self-signed by the agent CA (12 h certificates)' },
    { value: 'acme' as const, label: 'acme', title: "Let's Encrypt / ACME: automatic public certificates" },
    { value: 'files' as const, label: 'files', title: 'Your certificate (a file) + key (a secret)' },
  ];
  const CHALLENGES = [
    { value: 'http-01' as const, label: 'http-01', title: 'Needs the edge on port 80' },
    { value: 'tls-alpn-01' as const, label: 'tls-alpn-01', title: 'Needs the edge on port 443' },
    { value: 'dns-01' as const, label: 'dns-01', title: 'Wildcards; needs a DNS API token' },
  ];
  function setMode(m: TlsMode) {
    const t = value.tls;
    value.tls =
      m === 'acme'
        ? { mode: 'acme', email: t.mode === 'acme' ? t.email : '', challenge: t.mode === 'acme' ? t.challenge : 'http-01', dnsProvider: t.mode === 'acme' ? t.dnsProvider : null, dnsTokenSecretSlug: t.mode === 'acme' ? t.dnsTokenSecretSlug : null }
        : m === 'files'
          ? { mode: 'files', certFileSlug: t.mode === 'files' ? t.certFileSlug : '', keySecretSlug: t.mode === 'files' ? t.keySecretSlug : '' }
          : { mode: m };
    if (m === 'none') value.redirectHttpToHttps = false;
  }
  function setChallenge(c: 'http-01' | 'tls-alpn-01' | 'dns-01') {
    if (value.tls.mode !== 'acme') return;
    value.tls.challenge = c;
    if (c !== 'dns-01') {
      value.tls.dnsProvider = null;
      value.tls.dnsTokenSecretSlug = null;
    }
  }
</script>

<fieldset class="form stack" disabled={ro} data-testid="host-form">
  <Field label="Hostname" id="h-name" required lock={host.lock(`${prefix}.hostname`)} hint={'DNS name, *.example.com, IP, "*" (any host) or an expression ({{ var.… }}, {{ self.host }})'} error={[...(show && !value.hostname.trim() ? ['Required'] : []), ...(dupe ? ['This host is already listed'] : [])]} issues={at('hostname')}>
    <ExprInput id="h-name" bind:value={value.hostname} field="edge" {ctx} placeholder="plant.acme.com" disabled={ro} testid="host-name" />
  </Field>
  <Field label="TLS mode" id="h-mode" group issues={at('tls.mode')} hint={MODES.find((m) => m.value === value.tls.mode)?.title}>
    <SegmentedControl id="h-mode" options={MODES} value={value.tls.mode} onchange={setMode} ariaLabel="TLS mode" disabled={ro} testid="host-tls" />
  </Field>
  {#if value.tls.mode === 'acme'}
    <Field label="ACME email" id="h-email" required issues={at('tls.email')}>
      <input id="h-email" class="input" type="email" bind:value={value.tls.email} placeholder="ops@acme.com" disabled={ro} data-testid="host-email" />
    </Field>
    <Field label="Challenge" id="h-ch" group issues={at('tls.challenge')} hint={CHALLENGES.find((c) => value.tls.mode === 'acme' && c.value === value.tls.challenge)?.title}>
      <SegmentedControl id="h-ch" options={CHALLENGES} value={value.tls.challenge} onchange={setChallenge} ariaLabel="ACME challenge" disabled={ro} testid="host-challenge" />
    </Field>
    {#if value.tls.challenge === 'dns-01'}
      <div class="form-grid">
        <Field label="DNS provider" id="h-dns" required hint="Built into the agent (SPEC §12.4)" issues={at('tls.dnsProvider')}>
          <Combobox id="h-dns" value={value.tls.dnsProvider ?? ''} options={DNS_PROVIDERS} placeholder="choose a provider" ariaLabel="DNS provider" disabled={ro} testid="host-dns" onchange={(v) => value.tls.mode === 'acme' && (value.tls.dnsProvider = v || null)} />
        </Field>
        <Field label="DNS token secret" id="h-dnss" required issues={at('tls.dnsTokenSecretSlug')}>
          <Combobox id="h-dnss" value={value.tls.dnsTokenSecretSlug ?? ''} options={secOpts} mono placeholder="choose a secret" emptyText="No secrets defined" ariaLabel="DNS token secret" disabled={ro} testid="host-dns-secret" onchange={(v) => value.tls.mode === 'acme' && (value.tls.dnsTokenSecretSlug = v || null)} />
        </Field>
      </div>
    {/if}
  {/if}
  {#if value.tls.mode === 'files'}
    <div class="form-grid">
      <Field label="Certificate file" id="h-cert" required hint="A file (PEM chain)" issues={at('tls.certFileSlug')}>
        <Combobox id="h-cert" value={value.tls.certFileSlug} options={fileOpts} mono placeholder="choose a file" emptyText="No files defined" ariaLabel="Certificate file" disabled={ro} testid="host-cert" onchange={(v) => value.tls.mode === 'files' && (value.tls.certFileSlug = v)} />
      </Field>
      <Field label="Key secret" id="h-key" required hint="A secret (PEM private key)" issues={at('tls.keySecretSlug')}>
        <Combobox id="h-key" value={value.tls.keySecretSlug} options={secOpts} mono placeholder="choose a secret" emptyText="No secrets defined" ariaLabel="Key secret" disabled={ro} testid="host-key" onchange={(v) => value.tls.mode === 'files' && (value.tls.keySecretSlug = v)} />
      </Field>
    </div>
  {/if}
  {#if value.tls.mode !== 'none'}
    <Field issues={at('redirectHttpToHttps')}>
      <Toggle bind:checked={value.redirectHttpToHttps} label="Redirect HTTP → HTTPS (308)" disabled={ro} testid="host-redirect" />
    </Field>
  {/if}
  <Field label="Response headers → browser" group hint="nginx add_header (add) / proxy_hide_header (remove), both = set — for every route of this host that has no own list" error={respErrs.length ? respErrs : null} issues={at('responseHeaders', true)}>
    <ResponseHeadersEditor value={value.responseHeaders} {ctx} disabled={ro} testid="host-resp" onchange={(h, e) => ((value.responseHeaders = h), (respErrs = e))} />
  </Field>
  <Issues issues={rest} title="Host" testid="host-issues" />
</fieldset>

<style>
  .form {
    border: 0;
    margin: 0;
    padding: 0;
    min-width: 0;
  }
</style>
