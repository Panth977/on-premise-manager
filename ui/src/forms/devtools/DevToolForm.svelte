<script lang="ts">
  /**
   * Dev-tool preset / connection form (SPEC §7.6, §21 #97 / #99 / #102) — ONE form for the cloud preset drawer (template +
   * deployment, create / edit / read), the agent's read-only preset view and the agent's manual connection drawer
   * (create / edit). Groups, order and labels are identical: Preset|Connection · Connect to · Authentication · Options.
   * `variant` only changes what the data model needs: preset = Preset id, a credential Value is kept in the config (§21 #103,
   * plain-text warning); connection = no id, kind picker when new, a credential Value is stored on the agent. Drawer content (5–12 fields); the caller owns the Drawer + Save /
   * Apply / Test buttons and converts with draftToPreset / draftToConnectionInput (devtools.ts).
   * `issues` are RELATIVE paths (presetIssues / connectionIssues / server issues of the connection API).
   * Test ids (cloud's): dt-kind · dt-name · dt-slug · dt-desc · dt-readonly · dt-target(-<t>) · dt-container · dt-port ·
   * dt-host · dt-hport · dt-url · dt-volume · dt-vpath · dt-cpath · dt-mount-<i> · dt-target-error · dt-http-scheme ·
   * dt-username · dt-password-* · dt-token-* · dt-apikey-header · dt-database · dt-redis-db · dt-clientid ·
   * dt-<field>-env-suggest · dt-<field>-env-<NAME> · dt-scheme · dt-basepath · dt-follow · dt-headers · dt-hname-<i> ·
   * dt-hvalue-<i> · dt-hdel-<i> · dt-hadd · dt-sslmode · dt-consistency · dt-tls · dt-tls-insecure · dt-sni · dt-clean ·
   * dt-keepalive · dt-timeout · dt-maxrows · dt-summary.
   */
  import type { Snippet } from 'svelte';
  import type { ValidationIssue } from '@opm/shared/types';
  import { LIMITS } from '@opm/shared/validate';
  import { DEVTOOL_KIND_LABEL, DEVTOOL_TARGETS, DEVTOOL_KINDS, containerEnvExpr, suggestDevToolEnv, type CqlConsistency, type DevToolEnvField, type DevToolHttpAuthScheme, type DevToolKind, type DevToolTargetKind, type PgSslMode } from '@opm/shared/devtools';
  import Field from '../../primitives/Field.svelte';
  import Toggle from '../../primitives/Toggle.svelte';
  import SegmentedControl from '../../primitives/SegmentedControl.svelte';
  import Select from '../../primitives/Select.svelte';
  import NumberStepper from '../../primitives/NumberStepper.svelte';
  import ExprInput from '../../primitives/ExprInput.svelte';
  import IssueList from '../../primitives/Issues.svelte';
  import Btn from '../../primitives/Btn.svelte';
  import Icon from '../../primitives/Icon.svelte';
  import CredentialField from './CredentialField.svelte';
  import { KIND_ICON, READONLY_HINT, TARGET_LABEL, blankDraft, type DevToolDraft, type DevToolSources } from './devtools';
  import type { FormMode } from '../host';
  import { LOCK_FIXED } from '../locks';

  interface Props {
    value: DevToolDraft;
    variant: 'preset' | 'connection';
    mode: FormMode;
    sources: DevToolSources;
    /** relative issue paths (name, target.containerSlug, auth.password, options.timeoutSec …) */
    issues?: readonly ValidationIssue[];
    /** show errors of untouched fields (after the first Save; edit mode always) */
    submitted?: boolean;
    /** connection create: the kinds offered by the Kind picker (preset: kind is fixed, picked before) */
    kinds?: DevToolKind[];
    siteSlug: string;
    /** "+ New secret…" in the credential pickers (cloud): open the secret drawer, then call apply(newSlug) */
    onnewsecret?: (field: 'password' | 'token', apply: (secretSlug: string) => void) => void;
    /** the reveal control of a stored literal (agent RevealValue of a saved connection / preset static value) */
    reveal?: Snippet<['password' | 'token']>;
    valid?: boolean;
  }
  let { value = $bindable(), variant, mode, sources, issues = [], submitted = false, kinds, siteSlug, onnewsecret, reveal, valid = $bindable(true) }: Props = $props();
  const ro = $derived(mode === 'read');
  const isNew = $derived(mode === 'create');
  const isPreset = $derived(variant === 'preset');
  const show = $derived(submitted || mode === 'edit');
  const at = (f: string) => (show ? issues.filter((i) => i.path === f) : []);
  const has = (f: string) => at(f).some((i) => i.severity === 'error');
  $effect(() => {
    valid = !ro && !issues.some((i) => i.severity === 'error');
  });
  const errors = $derived(issues.filter((i) => i.severity === 'error'));

  const envContainer = $derived(value.target.kind === 'container' && value.target.containerSlug && sources.containers.some((c) => value.target.kind === 'container' && c.value === value.target.containerSlug) ? value.target.containerSlug : null);
  const envChoices = $derived(sources.envChoices(envContainer));
  const envSuggest = (f: DevToolEnvField): string[] => (envContainer ? suggestDevToolEnv(value.kind, f, envChoices.map((e) => e.name)) : []);
  const targetOpts = $derived(DEVTOOL_TARGETS[value.kind].map((t) => ({ value: t, label: TARGET_LABEL[t] })));
  const kindOpts = $derived((kinds ?? [...DEVTOOL_KINDS]).map((k) => ({ value: k, label: DEVTOOL_KIND_LABEL[k], icon: KIND_ICON[k] })));
  const strOrNull = (v: string): string | null => (v.trim() === '' ? null : v);

  function setKind(k: DevToolKind) {
    if (k === value.kind) return;
    const n = blankDraft(k, sources);
    n.name = value.name;
    n.description = value.description;
    n.presetSlug = value.presetSlug;
    value = n;
  }
  function setTargetKind(tk: DevToolTargetKind) {
    if (value.target.kind === tk) return;
    value.target = sources.defaultTarget(value.kind, tk);
  }
  function setContainer(slug: string) {
    if (value.target.kind === 'container') value.target = { kind: 'container', containerSlug: slug, portName: sources.defaultPort(slug, value.kind) };
    else if (value.target.kind === 'container-file') value.target = { ...value.target, containerSlug: slug };
  }
  function newSecret(f: 'password' | 'token') {
    onnewsecret?.(f, (slug) => (value.auth[f] = { kind: 'expr', expr: `{{ secret.${slug} }}` }));
  }

  const HTTP_SCHEMES: { value: DevToolHttpAuthScheme; label: string }[] = [
    { value: 'none', label: 'None' },
    { value: 'basic', label: 'Basic' },
    { value: 'bearer', label: 'Bearer' },
    { value: 'api-key', label: 'API key' },
  ];
  const SSL = [
    { value: '', label: 'prefer (default)' },
    { value: 'disable', label: 'disable' },
    { value: 'require', label: 'require', hint: 'TLS, certificate not verified' },
    { value: 'verify-full', label: 'verify-full', hint: 'TLS + certificate + host name' },
  ];
  const CONSISTENCY = [{ value: '', label: 'LOCAL_ONE (default)' }, ...(['ANY', 'ONE', 'TWO', 'THREE', 'QUORUM', 'ALL', 'LOCAL_QUORUM', 'EACH_QUORUM'] as CqlConsistency[]).map((c) => ({ value: c, label: c }))];
  const REDIS_DB = Array.from({ length: 16 }, (_, i) => ({ value: String(i), label: `db ${i}${i === 0 ? ' (default)' : ''}` }));
  const tlsKinds = ['redis', 'mqtt', 'cassandra'];
  const timeoutPh = $derived(`${sources.limits?.statementTimeoutSec ?? 30} (default)`);
  const rowsPh = $derived(`${sources.limits?.maxRows ?? 1000} (default)`);
</script>

<fieldset class="form stack" disabled={ro} data-testid="devtool-form" data-kind={value.kind} data-variant={variant}>
  {#if show && errors.length}<IssueList issues={errors} title="Fix before saving" testid="dt-summary" />{/if}

  <h4 class="grp">{isPreset ? 'Preset' : 'Connection'}</h4>
  <Field label="Kind" id="dt-kind" group lock={!isNew || isPreset ? (isPreset && !ro ? 'Kind is fixed — add another preset for another kind' : null) : null}>
    {#if isNew && !isPreset}
      <Select id="dt-kind" value={value.kind} options={kindOpts} onchange={(v) => setKind(v as DevToolKind)} ariaLabel="Kind" testid="dt-kind" />
    {:else}
      <span class="kindro" data-testid="dt-kind"><span class="kic"><Icon name={KIND_ICON[value.kind]} size={15} /></span>{DEVTOOL_KIND_LABEL[value.kind]}</span>
    {/if}
  </Field>
  <Field label="Name" id="dt-name" required issues={at('name')} hint={isPreset ? 'Shown in the agent GUI connection list.' : undefined}>
    <input id="dt-name" class="input" bind:value={value.name} maxlength={LIMITS.label} disabled={ro} placeholder={isPreset ? '' : 'e.g. App database'} data-testid="dt-name" />
  </Field>
  {#if isPreset}
    <Field label="Preset id" id="dt-slug" required lock={isNew ? null : LOCK_FIXED} issues={at('presetSlug')} hint={isNew ? 'Unique on the site: a–z, 0–9 and "-". Fixed after create.' : `The agent shows it as p:${value.presetSlug}.`}>
      <input id="dt-slug" class="input mono" value={value.presetSlug} oninput={(e) => (value.presetSlug = e.currentTarget.value.trim().toLowerCase())} disabled={!isNew || ro} maxlength="63" spellcheck="false" autocomplete="off" data-testid="dt-slug" />
    </Field>
  {/if}
  <Field label="Description" id="dt-desc" issues={at('description')}>
    <textarea id="dt-desc" class="textarea" rows="2" bind:value={value.description} maxlength="2000" disabled={ro} placeholder="What operators use it for" data-testid="dt-desc"></textarea>
  </Field>
  <Field label="Read-only" id="dt-ro" hint={value.readOnly ? READONLY_HINT[value.kind] : 'Operators may write (destructive statements still ask for the typed connection name).'}>
    <Toggle id="dt-ro" bind:checked={value.readOnly} label={value.readOnly ? 'read-only' : 'read-write'} disabled={ro} testid="dt-readonly" />
  </Field>

  <h4 class="grp">Connect to</h4>
  {#if targetOpts.length > 1}<SegmentedControl options={targetOpts} value={value.target.kind} onchange={setTargetKind} size="sm" disabled={ro} ariaLabel="Target" testid="dt-target" />{/if}
  {#if value.target.kind === 'container'}
    {@const t = value.target}
    <div class="two">
      <Field label="Container" id="dt-container" required issues={at('target.containerSlug')}>
        <Select id="dt-container" options={sources.containers} value={t.containerSlug || null} placeholder={sources.containers.length ? 'choose' : 'no containers on this site'} invalid={has('target.containerSlug')} disabled={ro} onchange={setContainer} testid="dt-container" ariaLabel="Container" />
      </Field>
      <Field label="Port" id="dt-port" required issues={at('target.portName')}>
        <Select id="dt-port" options={sources.ports(t.containerSlug, value.kind)} value={t.portName || null} placeholder={t.containerSlug ? 'choose' : 'pick the container first'} invalid={has('target.portName')} disabled={ro || !t.containerSlug} onchange={(v) => value.target.kind === 'container' && (value.target.portName = v)} testid="dt-port" ariaLabel="Port" />
      </Field>
    </div>
    <p class="hint">The agent dials the container on the site network (falls back to its published host port).</p>
  {:else if value.target.kind === 'host'}
    {@const t = value.target}
    <div class="two hp">
      <Field label="Host" id="dt-host" required issues={at('target.host')} hint="IP or DNS name reachable from the site server">
        <input id="dt-host" class="input mono" value={t.host} oninput={(e) => value.target.kind === 'host' && (value.target.host = e.currentTarget.value.trim())} disabled={ro} placeholder="10.0.0.20" spellcheck="false" data-testid="dt-host" />
      </Field>
      <Field label="Port" id="dt-hport" required issues={at('target.port')}>
        <NumberStepper id="dt-hport" value={t.port} onchange={(v) => value.target.kind === 'host' && (value.target.port = v ?? NaN)} min={1} max={65535} disabled={ro} label="Port" testid="dt-hport" />
      </Field>
    </div>
  {:else if value.target.kind === 'url'}
    <Field label="Base URL" id="dt-url" required issues={at('target.url')} hint={'http(s)://…; may hold {{ var.x }}. Requests are relative to it.'}>
      <ExprInput id="dt-url" value={value.target.url} onchange={(v) => value.target.kind === 'url' && (value.target.url = v)} field="devtool" ctx={sources.ctx} disabled={ro} invalid={has('target.url')} placeholder="https://api.example.com" testid="dt-url" />
    </Field>
  {:else if value.target.kind === 'volume-file'}
    {@const t = value.target}
    <div class="two">
      <Field label="Volume" id="dt-volume" required issues={at('target.volumeSlug')}>
        <Select id="dt-volume" options={sources.volumes} value={t.volumeSlug || null} placeholder={sources.volumes.length ? 'choose' : 'no volumes on this site'} invalid={has('target.volumeSlug')} disabled={ro} onchange={(v) => value.target.kind === 'volume-file' && (value.target.volumeSlug = v)} testid="dt-volume" ariaLabel="Volume" />
      </Field>
      <Field label="File in the volume" id="dt-vpath" required issues={at('target.path')}>
        <input id="dt-vpath" class="input mono" value={t.path} oninput={(e) => value.target.kind === 'volume-file' && (value.target.path = e.currentTarget.value.trim())} disabled={ro} placeholder="app.db" spellcheck="false" data-testid="dt-vpath" />
      </Field>
    </div>
    <p class="hint">A relative path inside the folder volume (no leading “/”, no “..”).</p>
  {:else if value.target.kind === 'container-file'}
    {@const t = value.target}
    <div class="two">
      <Field label="Container" id="dt-cfc" required issues={at('target.containerSlug')}>
        <Select id="dt-cfc" options={sources.containers} value={t.containerSlug || null} placeholder={sources.containers.length ? 'choose' : 'no containers on this site'} invalid={has('target.containerSlug')} disabled={ro} onchange={setContainer} testid="dt-container" ariaLabel="Container" />
      </Field>
      <Field label="Path in the container" id="dt-cpath" required issues={at('target.path')}>
        <input id="dt-cpath" class="input mono" value={t.path} oninput={(e) => value.target.kind === 'container-file' && (value.target.path = e.currentTarget.value.trim())} disabled={ro} placeholder="/data/app.db" spellcheck="false" data-testid="dt-cpath" />
      </Field>
    </div>
    {@const ms = t.containerSlug && sources.mounts ? sources.mounts(t.containerSlug) : null}
    {#if ms}
      <div class="mounts">
        <span class="hint">Mounts of {t.containerSlug} (the file must be on one):</span>
        {#each ms as m, i (m.mountPath)}
          <button type="button" class="chip mono" disabled={ro} onclick={() => value.target.kind === 'container-file' && (value.target.path = m.mountPath.replace(/\/?$/, '/'))} data-testid="dt-mount-{i}">{m.mountPath}<span class="faint"> · {m.label}</span></button>
        {:else}
          <span class="hint">none — a file on container-local storage cannot be opened.</span>
        {/each}
      </div>
    {:else}
      <p class="hint">Must be on a volume or host mount of that container (the agent maps it to the host file).</p>
    {/if}
  {/if}
  {#each at('target') as i, k (k)}<div class="field-err" data-testid="dt-target-error">{i.message}</div>{/each}

  {#if value.kind !== 'sqlite'}
    <h4 class="grp">Authentication {#if isPreset}<span class="nb hint"><Icon name="lock" size={11} /> prefer a secret — a typed Value is stored in plain text in the config</span>{/if}</h4>
    {#each at('auth') as i, k (k)}<div class={i.severity === 'error' ? 'field-err' : 'field-warn'} data-testid="dt-auth-issue">{i.message}</div>{/each}
    {#if value.kind === 'http'}
      <Field label="Scheme" id="dt-hs" group issues={at('auth.httpScheme')}>
        <SegmentedControl options={HTTP_SCHEMES} value={value.auth.httpScheme} onchange={(v) => (value.auth.httpScheme = v)} size="sm" disabled={ro} ariaLabel="Auth scheme" testid="dt-http-scheme" />
      </Field>
      {#if value.auth.httpScheme === 'basic'}
        {@render user('Username', true)}
        {@render cred('password', 'Password')}
      {:else if value.auth.httpScheme === 'bearer'}
        {@render cred('token', 'Bearer token')}
      {:else if value.auth.httpScheme === 'api-key'}
        <Field label="Header" id="dt-akh" issues={at('auth.apiKeyHeader')} hint="Default X-API-Key">
          <input id="dt-akh" class="input mono" value={value.auth.apiKeyHeader ?? ''} oninput={(e) => (value.auth.apiKeyHeader = strOrNull(e.currentTarget.value))} disabled={ro} placeholder="X-API-Key" spellcheck="false" data-testid="dt-apikey-header" />
        </Field>
        {@render cred('token', 'API key')}
      {/if}
    {:else}
      {@render user(value.kind === 'redis' ? 'Username (ACL)' : 'Username', value.kind === 'postgres')}
      {@render cred('password', 'Password')}
      {#if value.kind === 'postgres' || value.kind === 'cassandra'}
        <Field label={value.kind === 'postgres' ? 'Database' : 'Keyspace'} id="dt-db" issues={at('auth.database')} hint={value.kind === 'postgres' ? 'Empty = the username' : 'Optional default keyspace'}>
          <ExprInput id="dt-db" value={value.auth.database ?? ''} onchange={(v) => (value.auth.database = strOrNull(v))} field="devtool" ctx={sources.ctx} disabled={ro} invalid={has('auth.database')} placeholder={value.kind === 'postgres' ? value.auth.username || 'postgres' : 'system'} testid="dt-database" />
        </Field>
        {@render envChips('database', value.auth.database, (ex) => (value.auth.database = ex))}
      {:else if value.kind === 'redis'}
        <Field label="Database index" id="dt-rdb" issues={at('auth.database')}>
          <Select id="dt-rdb" options={REDIS_DB} value={value.auth.database ?? '0'} disabled={ro} onchange={(v) => (value.auth.database = v)} testid="dt-redis-db" ariaLabel="Redis database" />
        </Field>
      {:else if value.kind === 'mqtt'}
        <Field label="Client id" id="dt-cid" issues={at('auth.clientId')} hint="Empty = opm-devtools-<site>-<random>">
          <ExprInput id="dt-cid" value={value.auth.clientId ?? ''} onchange={(v) => (value.auth.clientId = strOrNull(v))} field="devtool" ctx={sources.ctx} disabled={ro} invalid={has('auth.clientId')} placeholder="opm-devtools-{siteSlug}" testid="dt-clientid" />
        </Field>
        {@render envChips('clientId', value.auth.clientId, (ex) => (value.auth.clientId = ex))}
      {/if}
    {/if}
  {/if}

  <h4 class="grp">Options</h4>
  {#each at('options') as i, k (k)}<div class="field-err">{i.message}</div>{/each}
  {#if value.kind === 'http'}
    {#if value.target.kind !== 'url'}
      <Field label="Scheme" id="dt-scheme" group issues={at('options.scheme')}>
        <SegmentedControl options={[{ value: 'http' as const, label: 'http' }, { value: 'https' as const, label: 'https' }]} value={value.options.scheme ?? 'http'} onchange={(v) => (value.options.scheme = v === 'http' ? null : v)} size="sm" disabled={ro} ariaLabel="Scheme" testid="dt-scheme" />
      </Field>
    {/if}
    <Field label="Base path" id="dt-bp" issues={at('options.basePath')} hint="Prefix of relative request URLs, e.g. /api">
      <input id="dt-bp" class="input mono" value={value.options.basePath ?? ''} oninput={(e) => (value.options.basePath = strOrNull(e.currentTarget.value.trim()))} disabled={ro} placeholder="/" spellcheck="false" data-testid="dt-basepath" />
    </Field>
    <Field label="Follow redirects" id="dt-fr">
      <Toggle id="dt-fr" checked={value.options.followRedirects ?? true} onchange={(v) => (value.options.followRedirects = v ? null : false)} label={(value.options.followRedirects ?? true) ? 'up to 10' : 'off'} disabled={ro} testid="dt-follow" />
    </Field>
    <Field label="Default headers" id="dt-hdr" group hint={'Added to every request; values may hold {{ secret.x }}.'}>
      <div class="rows" data-testid="dt-headers">
        {#each value.options.defaultHeaders as h, i (i)}
          <div class="hrow">
            <input class="input sm mono" bind:value={h.name} placeholder="X-Header" aria-label="Header name" disabled={ro} aria-invalid={has(`options.defaultHeaders[${i}].name`) ? 'true' : undefined} data-testid="dt-hname-{i}" />
            <ExprInput value={h.value} onchange={(v) => (h.value = v)} field="devtool" ctx={sources.ctx} preview={false} size="sm" disabled={ro} invalid={has(`options.defaultHeaders[${i}].value`)} placeholder="value" ariaLabel="Header value" testid="dt-hvalue-{i}" />
            {#if !ro}<Btn size="sm" variant="ghost" icon="x" aria-label="Remove header {h.name}" onclick={() => value.options.defaultHeaders.splice(i, 1)} data-testid="dt-hdel-{i}" />{/if}
          </div>
          {#each [...at(`options.defaultHeaders[${i}].name`), ...at(`options.defaultHeaders[${i}].value`)] as m, k (k)}<div class="field-err">{m.message}</div>{/each}
        {/each}
        {#if !ro}<div><Btn size="sm" icon="plus" onclick={() => value.options.defaultHeaders.push({ name: '', value: '' })} data-testid="dt-hadd">Add header</Btn></div>{/if}
      </div>
    </Field>
  {/if}
  {#if value.kind === 'postgres'}
    <Field label="SSL mode" id="dt-ssl" issues={at('options.sslMode')}>
      <Select id="dt-ssl" options={SSL} value={value.options.sslMode ?? ''} disabled={ro} onchange={(v) => (value.options.sslMode = (v || null) as PgSslMode | null)} testid="dt-sslmode" ariaLabel="SSL mode" />
    </Field>
  {/if}
  {#if value.kind === 'cassandra'}
    <Field label="Consistency" id="dt-cons" issues={at('options.consistency')}>
      <Select id="dt-cons" options={CONSISTENCY} value={value.options.consistency ?? ''} disabled={ro} onchange={(v) => (value.options.consistency = (v || null) as CqlConsistency | null)} testid="dt-consistency" ariaLabel="Consistency" />
    </Field>
  {/if}
  {#if tlsKinds.includes(value.kind)}
    <Field label="TLS" id="dt-tls">
      <Toggle id="dt-tls" bind:checked={value.options.tls} label={value.options.tls ? 'on' : 'off'} disabled={ro} testid="dt-tls" />
    </Field>
  {/if}
  {#if value.kind !== 'sqlite' && (value.options.tls || value.kind === 'http' || value.kind === 'postgres')}
    <div class="two">
      <Field label="Skip certificate check" id="dt-tlsi" hint="Self-signed certificates">
        <Toggle id="dt-tlsi" bind:checked={value.options.tlsInsecure} label={value.options.tlsInsecure ? 'insecure' : 'verify'} disabled={ro} testid="dt-tls-insecure" />
      </Field>
      <Field label="TLS server name" id="dt-sni" issues={at('options.tlsServerName')} hint="Optional (SNI)">
        <input id="dt-sni" class="input mono" value={value.options.tlsServerName ?? ''} oninput={(e) => (value.options.tlsServerName = strOrNull(e.currentTarget.value.trim()))} disabled={ro} spellcheck="false" placeholder="from the target" data-testid="dt-sni" />
      </Field>
    </div>
  {/if}
  {#if value.kind === 'mqtt'}
    <div class="two">
      <Field label="Clean session" id="dt-cs">
        <Toggle id="dt-cs" checked={value.options.cleanSession ?? true} onchange={(v) => (value.options.cleanSession = v ? null : false)} label={(value.options.cleanSession ?? true) ? 'on' : 'off'} disabled={ro} testid="dt-clean" />
      </Field>
      <Field label="Keep alive (s)" id="dt-ka" issues={at('options.keepAliveSec')}>
        <NumberStepper id="dt-ka" nullable value={value.options.keepAliveSec} onchange={(v) => (value.options.keepAliveSec = v)} min={0} max={65535} step={5} placeholder="30 (default)" suffix="s" disabled={ro} label="Keep alive" testid="dt-keepalive" />
      </Field>
    </div>
  {/if}
  <div class="two">
    <Field label="Timeout (s)" id="dt-to" issues={at('options.timeoutSec')} hint="1–3600; empty = agent default">
      <NumberStepper id="dt-to" nullable value={value.options.timeoutSec} onchange={(v) => (value.options.timeoutSec = v)} min={1} max={3600} step={5} placeholder={timeoutPh} suffix="s" disabled={ro} label="Timeout" testid="dt-timeout" />
    </Field>
    {#if value.kind === 'postgres' || value.kind === 'sqlite' || value.kind === 'cassandra'}
      <Field label="Rows per page" id="dt-mr" issues={at('options.maxRows')} hint="1–10000; empty = agent default">
        <NumberStepper id="dt-mr" nullable value={value.options.maxRows} onchange={(v) => (value.options.maxRows = v)} min={1} max={10000} step={100} placeholder={rowsPh} disabled={ro} label="Rows per page" testid="dt-maxrows" />
      </Field>
    {/if}
  </div>
  {#if isPreset}<p class="hint note"><Icon name="info" size={11} /> Operators can always add their own manual connections in the agent GUI; a preset is shown there read-only (“Duplicate to edit locally”).</p>{/if}
</fieldset>

{#snippet user(label: string, required: boolean)}
  <Field {label} id="dt-user" {required} issues={at('auth.username')} hint={'Text or {{ var.x }} / {{ secret.x }} / {{ containers.<c>.env.<NAME> }} — type {{ to pick'}>
    <ExprInput id="dt-user" value={value.auth.username ?? ''} onchange={(v) => (value.auth.username = strOrNull(v))} field="devtool" ctx={sources.ctx} disabled={ro} invalid={has('auth.username')} placeholder={value.kind === 'postgres' ? 'postgres' : 'optional'} testid="dt-username" />
  </Field>
  {@render envChips('username', value.auth.username, (ex) => (value.auth.username = ex))}
{/snippet}

{#snippet cred(f: 'password' | 'token', label: string)}
  <Field {label} id="dt-{f}" group issues={at(`auth.${f}`).filter((i) => i.code !== 'V_DEVTOOL_LITERAL')}>
    <CredentialField value={value.auth[f]} onchange={(c) => (value.auth[f] = c)} secrets={sources.secrets} ctx={sources.ctx} disabled={ro} invalid={has(`auth.${f}`)} what={f} testid="dt-{f}" onnewsecret={onnewsecret && !ro ? () => newSecret(f) : undefined} {envContainer} {envChoices} literalWhere={isPreset ? 'config' : 'agent'} reveal={reveal ? (f === 'password' ? revealPassword : revealToken) : undefined} />
  </Field>
  {@const cur = value.auth[f]}
  {@render envChips(f, cur.kind === 'expr' ? cur.expr : null, (ex) => (value.auth[f] = { kind: 'expr', expr: ex }))}
{/snippet}

{#snippet envChips(f: DevToolEnvField, current: string | null, apply: (expr: string) => void)}
  {#if envContainer && !ro}
    {@const names = envSuggest(f).filter((n) => (current ?? '').trim() !== containerEnvExpr(envContainer, n))}
    {#if names.length}
      <div class="envsug" data-testid="dt-{f}-env-suggest">
        {#each names as n (n)}
          {@const ch = envChoices.find((e) => e.name === n)}
          <button type="button" class="envchip" onclick={() => apply(containerEnvExpr(envContainer, n))} title={containerEnvExpr(envContainer, n)} data-testid="dt-{f}-env-{n}">
            <Icon name="plus" size={11} /> Use env <code>{n}</code> of <b>{envContainer}</b>
            {#if ch?.secret}<span class="badge amber">secret</span>{:else if f === 'password' || f === 'token'}<span class="badge outline">plain</span>{/if}
          </button>
        {/each}
      </div>
    {/if}
  {/if}
{/snippet}

{#snippet revealPassword()}{#if reveal}{@render reveal('password')}{/if}{/snippet}
{#snippet revealToken()}{#if reveal}{@render reveal('token')}{/if}{/snippet}

<style>
  .grp {
    margin: 6px 0 -4px;
    font-size: var(--fs-xs);
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--text-3);
    font-weight: 600;
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    border-top: 1px solid var(--border);
    padding-top: 10px;
  }
  .grp:first-of-type {
    border-top: 0;
    padding-top: 0;
  }
  .nb {
    text-transform: none;
    letter-spacing: 0;
    font-weight: 400;
    display: inline-flex;
    align-items: center;
    gap: 3px;
  }
  .kindro {
    display: inline-flex;
    gap: 8px;
    align-items: center;
    height: var(--control-h);
    font-weight: var(--fw-medium);
  }
  .kic {
    display: inline-flex;
    width: 26px;
    height: 26px;
    align-items: center;
    justify-content: center;
    border-radius: 6px;
    background: var(--accent-soft);
    color: var(--accent-text);
  }
  .two {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 10px;
  }
  .two.hp {
    grid-template-columns: minmax(0, 1fr) 150px;
  }
  @media (max-width: 520px) {
    .two,
    .two.hp {
      grid-template-columns: minmax(0, 1fr);
    }
  }
  .rows {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .hrow {
    display: grid;
    grid-template-columns: minmax(0, 0.8fr) minmax(0, 1.2fr) 28px;
    gap: 6px;
    align-items: start;
  }
  .mounts {
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
    align-items: center;
  }
  .mounts .chip {
    cursor: pointer;
    border: 1px solid var(--border);
    background: var(--surface-2);
    border-radius: var(--r-full);
    padding: 2px 8px;
    font-size: var(--fs-xs);
  }
  .envsug {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin-top: -4px;
  }
  .envchip {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    border: 1px dashed var(--border-strong);
    background: var(--surface);
    color: var(--text-2);
    border-radius: 999px;
    padding: 2px 10px 2px 8px;
    font-size: var(--fs-xs);
    cursor: pointer;
    max-width: 100%;
  }
  .envchip:hover {
    border-color: var(--accent);
    border-style: solid;
    background: var(--accent-soft);
    color: var(--accent-text);
  }
  .envchip code {
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--text);
  }
  .note {
    display: flex;
    gap: 4px;
    align-items: flex-start;
    margin-top: 4px;
  }
</style>
