<script lang="ts">
  /**
   * HTTP client (§21 #97, Postman-like; the request runs on the site server): request tabs (persisted per site +
   * connection), method, URL with {{var}} highlighting (green = environment, violet = OPM expression resolved on the
   * site), query params ↔ URL sync, headers with enable toggles + suggestions, body none / JSON / text / form /
   * multipart / binary, auth (inherit the connection / none / basic / bearer / API key in header or query), options,
   * environments, collections, history, send / cancel, response viewer, copy as curl.
   */
  import { onDestroy, untrack } from 'svelte';
  import { applyEnvironment, type DevToolConnection, type DevToolEnvironment, type DevToolHistoryItem, type DevToolSaved, type DevToolsInfo, type ExecuteHttpResponse, type HttpBody, type HttpKV, type HttpRequestSpec } from '@opm/shared/devtools';
  import { MASK } from '@opm/shared/types';
  import { errMessage } from '$lib/api';
  import { toasts } from '$lib/state/toast.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Select from '$lib/ui/Select.svelte';
  import Combobox from '$lib/ui/Combobox.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import NumberStepper from '$lib/ui/NumberStepper.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import Editor from '../cm/Editor.svelte';
  import TypedConfirm from '../TypedConfirm.svelte';
  import SavedPanel from '../SavedPanel.svelte';
  import SaveDialog from '../SaveDialog.svelte';
  import HistoryPanel from '../HistoryPanel.svelte';
  import { confirmReasons, dev, newQueryId } from '../api';
  import { devStore } from '../state.svelte';
  import { copyText } from '../grid';
  import { loadJson, saveJson } from '../kinds';
  import EnvDrawer from './EnvDrawer.svelte';
  import ResponseView from './ResponseView.svelte';
  import { COMMON_HEADERS, CONTENT_TYPES, METHODS, METHOD_TONE, applyUrl, blankRequest, bytesToB64, connBase, displayUrl, fromHistory, toCurl } from './http';

  let { conn, info, site }: { conn: DevToolConnection; info: DevToolsInfo; site: string } = $props();
  // svelte-ignore state_referenced_locally
  const KEY = `opm.dev.http.${site}.${conn.id}`;
  // svelte-ignore state_referenced_locally
  const ENVKEY = `opm.dev.http.env.${site}`;
  interface Tab {
    id: string;
    title: string;
    spec: HttpRequestSpec;
    saved: DevToolSaved | null;
  }
  const stored = loadJson<{ tabs: Tab[]; active: string } | null>(KEY, null);
  const first: Tab[] = stored?.tabs?.length ? stored.tabs : [{ id: 'r1', title: 'Request 1', spec: { ...blankRequest(), url: '/health' }, saved: null }];
  let tabs = $state<Tab[]>(first);
  let active = $state(stored?.active && first.some((t) => t.id === stored.active) ? stored.active : first[0]!.id);
  const tab = $derived(tabs.find((t) => t.id === active) ?? tabs[0]!);
  const spec = $derived(tab.spec);
  let urlText = $state('');
  $effect(() => {
    void active;
    untrack(() => (urlText = displayUrl(tab.spec)));
  });
  let persistT: ReturnType<typeof setTimeout> | null = null;
  $effect(() => {
    const snap = JSON.stringify({ tabs, active });
    untrack(() => {
      if (persistT) clearTimeout(persistT);
      persistT = setTimeout(() => saveJson(KEY, JSON.parse(snap)), 300);
    });
  });
  function newTab(s: HttpRequestSpec = blankRequest(), title?: string, saved: DevToolSaved | null = null) {
    const n = Math.max(0, ...tabs.map((t) => Number(/^r(\d+)$/.exec(t.id)?.[1] ?? 0))) + 1;
    tabs = [...tabs, { id: `r${n}`, title: title ?? `Request ${n}`, spec: s, saved }];
    active = `r${n}`;
  }
  function closeTab(id: string) {
    if (tabs.length === 1) {
      tabs = [{ id: 'r1', title: 'Request 1', spec: blankRequest(), saved: null }];
      active = 'r1';
      urlText = '';
      return;
    }
    const i = tabs.findIndex((t) => t.id === id);
    tabs = tabs.filter((t) => t.id !== id);
    if (active === id) active = tabs[Math.max(0, i - 1)]!.id;
  }
  function onUrl(v: string) {
    urlText = v;
    applyUrl(tab.spec, v);
  }
  function rowsChanged() {
    urlText = displayUrl(tab.spec);
  }

  // ── environments
  let envs = $state<DevToolEnvironment[]>([]);
  let envId = $state<string | null>(loadJson<string | null>(ENVKEY, null));
  let envOpen = $state(false);
  async function loadEnvs(sel?: string | null) {
    try {
      envs = (await dev.listEnvironments({ siteSlug: site })).items;
      if (sel !== undefined) envId = sel;
      if (envId && !envs.some((e) => e.id === envId)) envId = null;
      saveJson(ENVKEY, envId);
    } catch {
      envs = [];
    }
  }
  loadEnvs();
  const env = $derived(envs.find((e) => e.id === envId) ?? null);
  const envVars = $derived(Object.fromEntries((env?.variables ?? []).filter((v) => v.enabled).map((v) => [v.name, v.secret || v.value === MASK ? '•••' : String(v.value)])));
  const resolvedUrl = $derived(applyEnvironment(urlText, envVars));
  const unknownVars = $derived([...urlText.matchAll(/\{\{\s*([^{}\s.|]+)\s*\}\}/g)].map((m) => m[1]!).filter((n) => !(n in envVars) && !['var', 'secret', 'site', 'self', 'deployment', 'containers', 'container', 'file'].includes(n)));

  // ── request editing
  type ReqTab = 'params' | 'headers' | 'body' | 'auth' | 'options';
  let rtab = $state<ReqTab>('params');
  function setBody(k: HttpBody['kind']) {
    const b = tab.spec.body;
    const text = b.kind === 'json' || b.kind === 'text' ? b.text : '';
    tab.spec.body = k === 'none' ? { kind: 'none' } : k === 'json' ? { kind: 'json', text: text || '{\n  \n}' } : k === 'text' ? { kind: 'text', text, contentType: 'text/plain' } : k === 'form' ? { kind: 'form', fields: b.kind === 'multipart' ? b.fields.map((f) => ({ name: f.name, value: f.value, enabled: f.enabled })) : [{ name: '', value: '', enabled: true }] } : k === 'multipart' ? { kind: 'multipart', fields: b.kind === 'form' ? b.fields.map((f) => ({ ...f, fileName: null, base64: false })) : [{ name: '', value: '', enabled: true, fileName: null, base64: false }] } : { kind: 'binary', base64: '', contentType: null };
  }
  async function pickFile(e: Event, onfile: (b64: string, name: string, type: string) => void) {
    const f = (e.currentTarget as HTMLInputElement).files?.[0];
    if (!f) return;
    if (f.size > 8 * 1024 * 1024) return toasts.push('bad', 'File too large', 'At most 8 MiB in a dev tools request.');
    onfile(bytesToB64(new Uint8Array(await f.arrayBuffer())), f.name, f.type);
  }
  type AuthMode = 'inherit' | 'none' | 'basic' | 'bearer' | 'api-key';
  const authMode = $derived<AuthMode>(spec.auth === null ? 'inherit' : spec.auth.scheme);
  function setAuth(m: AuthMode) {
    tab.spec.auth = m === 'inherit' ? null : { scheme: m, username: spec.auth?.username ?? null, password: spec.auth?.password ?? null, token: spec.auth?.token ?? null, apiKeyHeader: spec.auth?.apiKeyHeader ?? 'X-API-Key', apiKeyIn: spec.auth?.apiKeyIn ?? 'header' };
  }
  const connAuthText = $derived(conn.auth.httpScheme === 'none' ? 'no auth' : `${conn.auth.httpScheme}${conn.auth.httpScheme === 'basic' ? ` as ${conn.auth.username}` : ''} (${(conn.auth.httpScheme === 'basic' ? conn.auth.password : conn.auth.token).kind === 'expr' ? 'secret expression' : 'stored value'})`);
  const counts = $derived({ params: spec.query.filter((q) => q.enabled && q.name).length, headers: spec.headers.filter((h) => h.enabled && h.name).length });

  // ── send
  let busy = $state(false);
  let queryId = $state<string | null>(null);
  let res = $state<ExecuteHttpResponse | null>(null);
  let err = $state<string | null>(null);
  let started = $state(0);
  let now = $state(0);
  let tick: ReturnType<typeof setInterval> | null = null;
  onDestroy(() => tick && clearInterval(tick));
  let confirm = $state<{ open: boolean; reasons: string[]; what: string; again: (t: string) => void }>({ open: false, reasons: [], what: '', again: () => {} });
  async function send(typed?: string) {
    if (busy) return;
    if (!spec.url.trim() && !spec.query.length) return toasts.info('Enter a URL', 'Absolute, or a path relative to the connection.');
    busy = true;
    err = null;
    queryId = newQueryId();
    started = now = Date.now();
    tick = setInterval(() => (now = Date.now()), 100);
    try {
      res = await dev.executeHttp({ siteSlug: site, connectionId: conn.id, body: { request: $state.snapshot(spec) as HttpRequestSpec, environmentId: envId, queryId, confirm: typed } });
      histV++;
    } catch (e) {
      const reasons = confirmReasons(e);
      if (reasons && typed === undefined) confirm = { open: true, reasons, what: `${spec.method} ${urlText}`, again: (t) => send(t) };
      else {
        err = errMessage(e);
        res = null;
      }
    } finally {
      busy = false;
      if (tick) clearInterval(tick);
      tick = null;
      void devStore.refreshConns();
    }
  }
  async function cancel() {
    if (!queryId) return;
    try {
      await dev.cancel({ siteSlug: site, connectionId: conn.id, body: { queryId } });
      toasts.info('Cancel sent');
    } catch (e) {
      toasts.err(e, 'Cancel failed');
    }
  }
  let curlOpen = $state(false);
  const curl = $derived(curlOpen ? toCurl(spec, conn, info) : '');

  // ── sidebar
  type Side = 'collections' | 'history';
  let side = $state<Side>('collections');
  let savedV = $state(0);
  let histV = $state(0);
  let saveOpen = $state(false);
  let saveAs = $state(false);
  let cols = $state<string[]>([]);
  async function openSave(as: boolean) {
    saveAs = as;
    try {
      cols = [...new Set((await dev.listSaved({ siteSlug: site, query: { kind: 'http' } })).items.map((x) => x.collection).filter(Boolean))];
    } catch {
      cols = [];
    }
    saveOpen = true;
  }
  function openSaved(s: DevToolSaved) {
    const ex = tabs.find((t) => t.saved?.id === s.id);
    if (ex) return (active = ex.id);
    try {
      newTab({ ...blankRequest(), ...(JSON.parse(s.content) as HttpRequestSpec) }, s.name, s);
    } catch {
      toasts.push('bad', 'Not a saved HTTP request');
    }
  }
  async function duplicateSaved(s: DevToolSaved) {
    try {
      await dev.createSaved({ siteSlug: site, body: { kind: 'http', connectionId: s.connectionId, collection: s.collection, name: `${s.name} (copy)`, description: s.description, content: s.content } });
      savedV++;
    } catch (e) {
      toasts.err(e, 'Duplicate failed');
    }
  }
  function openHistory(h: DevToolHistoryItem) {
    newTab(fromHistory(h.statement), `${h.status ?? ''} ${new Date(h.ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}`.trim());
  }
  const base = $derived(connBase(conn, info));
  const shownUrl = $derived(/^https?:\/\//i.test(resolvedUrl) ? resolvedUrl : `${base}${resolvedUrl.startsWith('/') || !resolvedUrl ? '' : '/'}${resolvedUrl}`);
  const elapsed = $derived(busy ? ((now - started) / 1000).toFixed(1) : null);
</script>

{#snippet kv(rows: HttpKV[], set: (r: HttpKV[]) => void, names: string[] | null, testid: string, valuePh = 'value')}
  <div class="kvt" data-testid={testid}>
    {#each rows as r, i (i)}
      <div class="kvr" class:off={!r.enabled}>
        <input type="checkbox" checked={r.enabled} onchange={(e) => set(rows.map((x, j) => (j === i ? { ...x, enabled: e.currentTarget.checked } : x)))} aria-label="Enabled" />
        {#if names}
          <Combobox value={r.name} onchange={(v) => set(rows.map((x, j) => (j === i ? { ...x, name: v } : x)))} options={names.map((n) => ({ value: n }))} allowCustom mono size="sm" placeholder="Header" testid="{testid}-name-{i}" />
        {:else}
          <input class="input sm mono" value={r.name} oninput={(e) => set(rows.map((x, j) => (j === i ? { ...x, name: e.currentTarget.value } : x)))} placeholder="name" aria-label="Name" data-testid="{testid}-name-{i}" />
        {/if}
        <input class="input sm mono" value={r.value} oninput={(e) => set(rows.map((x, j) => (j === i ? { ...x, value: e.currentTarget.value } : x)))} placeholder={valuePh} aria-label="Value" data-testid="{testid}-value-{i}" />
        <button class="btn ghost icon xs" aria-label="Remove" onclick={() => set(rows.filter((_, j) => j !== i))}><Icon name="x" size={12} /></button>
      </div>
    {/each}
    <button class="btn ghost sm add" onclick={() => set([...rows, { name: '', value: '', enabled: true }])} data-testid="{testid}-add"><Icon name="plus" size={13} />Add</button>
  </div>
{/snippet}

<div class="hc" data-testid="http-client">
  <aside class="side">
    <div class="stabs" role="tablist">
      <button role="tab" aria-selected={side === 'collections'} class:on={side === 'collections'} onclick={() => (side = 'collections')}><Icon name="folder" size={13} />Collections</button>
      <button role="tab" aria-selected={side === 'history'} class:on={side === 'history'} onclick={() => (side = 'history')} data-testid="http-side-history"><Icon name="history" size={13} />History</button>
    </div>
    <div class="sbody">
      {#if side === 'collections'}
        <SavedPanel {site} kinds={['http']} connectionId={conn.id} onopen={openSaved} version={savedV} preview={(s) => { try { const x = JSON.parse(s.content) as HttpRequestSpec; return `${x.method} ${x.url}`; } catch { return s.content.slice(0, 80); } }} />
        <button class="btn ghost xs dupl" onclick={() => tab.saved && duplicateSaved(tab.saved)} disabled={!tab.saved} title="Duplicate the saved request of this tab"><Icon name="copy" size={12} />Duplicate current</button>
      {:else}
        <HistoryPanel {site} connectionId={conn.id} onopen={openHistory} version={histV} />
      {/if}
    </div>
  </aside>

  <section class="main">
    <div class="etabs" role="tablist" aria-label="Requests">
      <div class="tl">
        {#each tabs as t (t.id)}
          <div class="et" class:on={t.id === active} role="tab" aria-selected={t.id === active} tabindex="0" onclick={() => (active = t.id)} onkeydown={(e) => e.key === 'Enter' && (active = t.id)} data-testid="htab-{t.id}">
            <span class="mt {METHOD_TONE[t.spec.method]}">{t.spec.method}</span><span class="tt">{t.title}</span>
            <button class="x" onclick={(e) => (e.stopPropagation(), closeTab(t.id))} aria-label="Close {t.title}" tabindex="-1"><Icon name="x" size={11} /></button>
          </div>
        {/each}
      </div>
      <button class="btn ghost icon sm" onclick={() => newTab()} aria-label="New request" data-testid="htab-new"><Icon name="plus" size={14} /></button>
      <span class="grow"></span>
      <div class="envsel"><Select value={envId ?? ''} options={[{ value: '', label: 'No environment' }, ...envs.map((e) => ({ value: e.id, label: e.name, hint: `${e.variables.length} vars` }))]} onchange={(v) => ((envId = v || null), saveJson(ENVKEY, envId))} size="sm" ariaLabel="Environment" testid="http-env" /></div>
      <Btn size="sm" variant="ghost" icon="sliders" onclick={() => (envOpen = true)} title="Manage environments" data-testid="http-env-manage">Env</Btn>
    </div>

    <div class="req">
      <div class="urlrow">
        <div class="msel"><Select value={spec.method} options={METHODS.map((m) => ({ value: m, label: m }))} onchange={(v) => (tab.spec.method = v as HttpRequestSpec['method'])} ariaLabel="Method" testid="http-method" mono /></div>
        <div class="url">
          {#key active}<Editor value={urlText} lang="text" singleLine placeholders onchange={onUrl} onrun={() => send()} placeholder="/path or https://… — {'{{var}}'} from the environment" testid="http-url" ariaLabel="URL" />{/key}
        </div>
        {#if busy}
          <Btn variant="danger" icon="stop" onclick={cancel} data-testid="http-cancel">Cancel {elapsed}s</Btn>
        {:else}
          <Btn variant="primary" icon="send" onclick={() => send()} title="Send (Enter in the URL)" data-testid="http-send">Send</Btn>
        {/if}
        <Btn variant="ghost" icon="save" onclick={() => openSave(false)} data-testid="http-save">{tab.saved ? 'Save' : 'Save…'}</Btn>
        {#if tab.saved}<Btn variant="ghost" size="sm" onclick={() => openSave(true)}>Save as…</Btn>{/if}
        <Btn variant="ghost" size="sm" icon="code" onclick={() => (curlOpen = true)} title="Copy as curl" data-testid="http-curl">curl</Btn>
      </div>
      <div class="resolved faint xs mono" data-testid="http-resolved">
        → {shownUrl}
        {#if unknownVars.length}<span class="warn-text"> · not in the environment: {unknownVars.join(', ')} (sent as is)</span>{/if}
        {#if conn.readOnly}<span class="badge amber"><Icon name="lock" size={10} />read-only: GET / HEAD / OPTIONS</span>{/if}
      </div>
      <div class="tabs" role="tablist">
        {#each [['params', `Params${counts.params ? ` ${counts.params}` : ''}`], ['headers', `Headers${counts.headers ? ` ${counts.headers}` : ''}`], ['body', `Body${spec.body.kind !== 'none' ? ' •' : ''}`], ['auth', `Auth${spec.auth ? ' •' : ''}`], ['options', 'Options']] as [k, l] (k)}
          <button role="tab" aria-selected={rtab === k} class:active={rtab === k} onclick={() => (rtab = k as ReqTab)} data-testid="http-rtab-{k}">{l}</button>
        {/each}
      </div>
      <div class="rbody">
        {#if rtab === 'params'}
          {@render kv(spec.query, (r) => ((tab.spec.query = r), rowsChanged()), null, 'http-params')}
        {:else if rtab === 'headers'}
          {#if conn.options.defaultHeaders.length}<p class="faint xs">Connection headers (always sent): {conn.options.defaultHeaders.map((h) => `${h.name}: ${h.value}`).join(' · ')}</p>{/if}
          {@render kv(spec.headers, (r) => (tab.spec.headers = r), COMMON_HEADERS, 'http-headers-ed')}
        {:else if rtab === 'body'}
          <SegmentedControl options={[{ value: 'none' as const, label: 'None' }, { value: 'json' as const, label: 'JSON' }, { value: 'text' as const, label: 'Text' }, { value: 'form' as const, label: 'Form' }, { value: 'multipart' as const, label: 'Multipart' }, { value: 'binary' as const, label: 'Binary' }]} value={spec.body.kind} onchange={setBody} label="Body" size="sm" testid="http-body-kind" />
          {#if spec.body.kind === 'json' || spec.body.kind === 'text'}
            {@const b = spec.body}
            {#if b.kind === 'text'}<div class="ctsel"><Combobox value={b.contentType ?? ''} onchange={(v) => (tab.spec.body = { ...b, contentType: v || null })} options={CONTENT_TYPES.map((c) => ({ value: c }))} allowCustom size="sm" mono placeholder="Content-Type" /></div>{/if}
            <div class="bed">{#key `${active}:${b.kind}`}<Editor value={b.text} lang={b.kind === 'json' ? 'json' : 'text'} placeholders onchange={(v) => (tab.spec.body = { ...b, text: v })} onrun={() => send()} height="100%" testid="http-body-editor" ariaLabel="Request body" />{/key}</div>
          {:else if spec.body.kind === 'form'}
            {@const b = spec.body}
            {@render kv(b.fields, (r) => (tab.spec.body = { kind: 'form', fields: r }), null, 'http-form')}
          {:else if spec.body.kind === 'multipart'}
            {@const b = spec.body}
            <div class="kvt">
              {#each b.fields as f, i (i)}
                <div class="kvr mp">
                  <input type="checkbox" bind:checked={f.enabled} aria-label="Enabled" />
                  <input class="input sm mono" bind:value={f.name} placeholder="name" aria-label="Name" />
                  {#if f.base64}
                    <span class="small mono fn">{f.fileName} <button class="btn ghost xs" onclick={() => ((f.base64 = false), (f.value = ''), (f.fileName = null))}>text</button></span>
                  {:else}
                    <input class="input sm mono" bind:value={f.value} placeholder="value" aria-label="Value" />
                  {/if}
                  <label class="btn ghost xs file">File<input type="file" onchange={(e) => pickFile(e, (b64, name) => ((f.value = b64), (f.fileName = name), (f.base64 = true)))} /></label>
                  <button class="btn ghost icon xs" aria-label="Remove" onclick={() => (b.fields = b.fields.filter((_, j) => j !== i))}><Icon name="x" size={12} /></button>
                </div>
              {/each}
              <button class="btn ghost sm add" onclick={() => (b.fields = [...b.fields, { name: '', value: '', enabled: true, fileName: null, base64: false }])}><Icon name="plus" size={13} />Add</button>
            </div>
          {:else if spec.body.kind === 'binary'}
            {@const b = spec.body}
            <div class="bin">
              <label class="btn sm file"><Icon name="upload" size={13} />Choose a file<input type="file" onchange={(e) => pickFile(e, (b64, _n, type) => (tab.spec.body = { kind: 'binary', base64: b64, contentType: type || 'application/octet-stream' }))} /></label>
              <span class="small">{b.base64 ? `${Math.round((b.base64.length * 3) / 4).toLocaleString()} bytes · ${b.contentType ?? ''}` : 'no file'}</span>
            </div>
          {:else}
            <p class="faint small">No body.</p>
          {/if}
        {:else if rtab === 'auth'}
          <SegmentedControl options={[{ value: 'inherit' as AuthMode, label: 'Connection' }, { value: 'none' as AuthMode, label: 'None' }, { value: 'basic' as AuthMode, label: 'Basic' }, { value: 'bearer' as AuthMode, label: 'Bearer' }, { value: 'api-key' as AuthMode, label: 'API key' }]} value={authMode} onchange={setAuth} label="Auth" size="sm" testid="http-auth" />
          {#if spec.auth === null}
            <p class="small">Uses the connection's auth: <strong>{connAuthText}</strong>. Credentials are added by the agent on the site; they never reach the browser.</p>
          {:else if spec.auth.scheme === 'basic'}
            {@const a = spec.auth}
            <div class="form-grid"><div class="field"><label for="au-u">User</label><input id="au-u" class="input sm mono" value={a.username ?? ''} oninput={(e) => (a.username = e.currentTarget.value)} /></div><div class="field"><label for="au-p">Password</label><input id="au-p" class="input sm mono" type="password" value={a.password ?? ''} oninput={(e) => (a.password = e.currentTarget.value)} placeholder={'{{password}} or {{ secret.x }}'} /></div></div>
          {:else if spec.auth.scheme === 'bearer' || spec.auth.scheme === 'api-key'}
            {@const a = spec.auth}
            <div class="form-grid">
              {#if a.scheme === 'api-key'}
                <div class="field"><label for="au-h">{a.apiKeyIn === 'query' ? 'Query parameter' : 'Header'}</label><input id="au-h" class="input sm mono" value={a.apiKeyHeader ?? ''} oninput={(e) => (a.apiKeyHeader = e.currentTarget.value)} /></div>
                <div class="field"><span class="label">Send in</span><SegmentedControl options={[{ value: 'header' as const, label: 'Header' }, { value: 'query' as const, label: 'Query' }]} value={a.apiKeyIn} onchange={(v) => (a.apiKeyIn = v)} label="Send in" size="sm" /></div>
              {/if}
              <div class="field"><label for="au-t">{a.scheme === 'bearer' ? 'Token' : 'Key'}</label><input id="au-t" class="input sm mono" type="password" value={a.token ?? ''} oninput={(e) => (a.token = e.currentTarget.value)} placeholder={'{{token}} or {{ secret.api-token }}'} data-testid="http-auth-token" /></div>
            </div>
            <p class="hint">Saved requests keep what you type here — prefer {'{{ secret.x }}'} or an environment secret over a literal token.</p>
          {:else}
            <p class="faint small">No Authorization header (the connection's auth is not used).</p>
          {/if}
        {:else}
          <div class="opts">
            <label class="switch"><input type="checkbox" checked={spec.followRedirects ?? conn.options.followRedirects ?? true} onchange={(e) => (tab.spec.followRedirects = e.currentTarget.checked)} />Follow redirects (≤ 10)</label>
            <label class="switch"><input type="checkbox" checked={spec.tlsInsecure ?? conn.options.tlsInsecure} onchange={(e) => (tab.spec.tlsInsecure = e.currentTarget.checked)} />Skip TLS certificate verification</label>
            <div class="field"><span class="label">Timeout</span><NumberStepper value={spec.timeoutSec} onchange={(v) => (tab.spec.timeoutSec = v)} min={1} max={3600} step={5} nullable size="sm" placeholder={String(conn.options.timeoutSec ?? info.limits.statementTimeoutSec)} suffix="s" label="Timeout" /></div>
            <p class="hint">Bodies up to {Math.round(info.limits.maxResponseBytes / 1048576)} MiB are read; {Math.round(info.limits.inlineResponseBytes / 1048576)} MiB are shown inline, the rest via “Download full body”. The browser's cookies are never sent.</p>
          </div>
        {/if}
      </div>
    </div>
    <div class="resp">
      {#if err}
        <div class="banner bad" data-testid="http-error"><Icon name="alert" /><div class="grow">{err}</div></div>
      {:else if res}
        {#key res.queryId}<ResponseView {res} {site} />{/key}
      {:else if busy}
        <div class="pad faint"><span class="spin"></span> Waiting for the site server… {elapsed} s</div>
      {:else}
        <div class="pad hint">Send the request to see the response. It runs on the site server ({base}).</div>
      {/if}
    </div>
  </section>
</div>

<TypedConfirm bind:open={confirm.open} connectionName={conn.name} reasons={confirm.reasons} what={confirm.what} onconfirm={(t) => confirm.again(t)} />
<EnvDrawer bind:open={envOpen} {site} {envs} activeId={envId} onchanged={(id) => loadEnvs(id)} />
<SaveDialog bind:open={saveOpen} {site} kind="http" connectionId={conn.id} content={JSON.stringify(spec)} existing={saveAs ? null : tab.saved} collections={cols} suggestedName={tab.title.startsWith('Request ') ? `${spec.method} ${spec.url}` : tab.title} onsaved={(s) => ((tab.saved = s), (tab.title = s.name), savedV++)} />
<Modal bind:open={curlOpen} title="Copy as curl" description="Placeholders are left unresolved; the connection's credentials appear as their expression." size="lg" testid="http-curl-modal">
  <pre class="curl mono" data-testid="http-curl-text">{curl}</pre>
  {#snippet footer()}
    <Btn variant="primary" icon="copy" onclick={async () => (await copyText(curl)) && toasts.ok('curl copied')}>Copy</Btn>
  {/snippet}
</Modal>

<style>
  .hc {
    display: grid;
    grid-template-columns: 260px minmax(0, 1fr);
    gap: 10px;
    height: calc(100dvh - 132px);
    min-height: 560px;
  }
  .side,
  .main {
    border: 1px solid var(--border);
    border-radius: var(--r-lg);
    background: var(--surface);
    min-height: 0;
    min-width: 0;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }
  .stabs {
    display: flex;
    gap: 2px;
    padding: 4px 4px 0;
    border-bottom: 1px solid var(--border);
    background: var(--surface-2);
  }
  .stabs button {
    all: unset;
    display: inline-flex;
    gap: 5px;
    align-items: center;
    padding: 6px 9px 7px;
    font-size: 12.5px;
    color: var(--text-2);
    cursor: pointer;
    border-bottom: 2px solid transparent;
    margin-bottom: -1px;
  }
  .stabs button.on {
    color: var(--text);
    border-bottom-color: var(--accent);
  }
  .sbody {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
  }
  .sbody > :global(:first-child) {
    flex: 1;
    min-height: 0;
  }
  .dupl {
    margin: 4px 8px 8px;
    align-self: flex-start;
  }
  .etabs {
    display: flex;
    align-items: flex-end;
    gap: 4px;
    padding: 4px 6px 0;
    background: var(--surface-2);
    border-bottom: 1px solid var(--border);
    min-width: 0;
  }
  .envsel {
    width: 170px;
    margin-bottom: 4px;
  }
  .etabs :global(.btn) {
    margin-bottom: 4px;
  }
  .tl {
    display: flex;
    gap: 2px;
    overflow-x: auto;
    min-width: 0;
    scrollbar-width: thin;
  }
  .et {
    display: inline-flex;
    gap: 5px;
    align-items: center;
    padding: 5px 6px 6px 9px;
    border: 1px solid transparent;
    border-bottom: 0;
    border-radius: 6px 6px 0 0;
    font-size: 12.5px;
    color: var(--text-2);
    cursor: pointer;
    white-space: nowrap;
    max-width: 220px;
  }
  .et.on {
    background: var(--surface);
    border-color: var(--border);
    color: var(--text);
    margin-bottom: -1px;
    padding-bottom: 7px;
  }
  .tt {
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .et .x {
    all: unset;
    display: grid;
    place-items: center;
    width: 16px;
    height: 16px;
    border-radius: 4px;
    color: var(--text-3);
    cursor: pointer;
  }
  .mt {
    font-size: 9.5px;
    font-weight: 700;
    font-family: var(--font-mono);
  }
  .mt.green {
    color: var(--green-text);
  }
  .mt.amber {
    color: var(--amber-text);
  }
  .mt.blue {
    color: var(--blue-text);
  }
  .mt.violet {
    color: var(--violet-text);
  }
  .mt.red {
    color: var(--red-text);
  }
  .mt.gray {
    color: var(--text-3);
  }
  .req {
    padding: 8px 10px 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
    flex: none;
    max-height: 52%;
    min-height: 0;
  }
  .urlrow {
    display: flex;
    gap: 6px;
    align-items: center;
    flex-wrap: wrap;
  }
  .msel {
    width: 112px;
    flex: none;
  }
  .url {
    flex: 1 1 300px;
    min-width: 0;
    border: 1px solid var(--border-strong);
    border-radius: var(--r-md);
    padding: 1px 6px;
    background: var(--surface);
  }
  .url:focus-within {
    box-shadow: var(--focus-ring);
  }
  .resolved {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    display: flex;
    gap: 6px;
    align-items: center;
  }
  .rbody {
    overflow: auto;
    padding: 8px 0 10px;
    min-height: 120px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .kvt {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .kvr {
    display: grid;
    grid-template-columns: 22px minmax(120px, 0.8fr) minmax(160px, 1.4fr) 26px;
    gap: 6px;
    align-items: center;
  }
  .kvr.mp {
    grid-template-columns: 22px minmax(100px, 0.7fr) minmax(140px, 1.3fr) auto 26px;
  }
  .kvr.off :global(input:not([type='checkbox'])) {
    opacity: 0.55;
  }
  .add {
    align-self: flex-start;
  }
  .file {
    position: relative;
    overflow: hidden;
    cursor: pointer;
  }
  .file input {
    position: absolute;
    inset: 0;
    opacity: 0;
    cursor: pointer;
  }
  .fn {
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .ctsel {
    width: 260px;
  }
  .bed {
    height: 180px;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    overflow: hidden;
  }
  .bin {
    display: flex;
    gap: 10px;
    align-items: center;
  }
  .opts {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .resp {
    flex: 1;
    min-height: 0;
    border-top: 1px solid var(--border);
    padding: 8px 10px;
    display: flex;
    flex-direction: column;
  }
  .resp > :global(*) {
    flex: 1;
  }
  .pad {
    padding: 10px 0;
  }
  .curl {
    margin: 0;
    padding: 10px 12px;
    background: var(--code-bg);
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    font-size: 12px;
    white-space: pre-wrap;
    word-break: break-all;
    max-height: 50vh;
    overflow: auto;
  }
  @media (max-width: 900px) {
    .hc {
      grid-template-columns: minmax(0, 1fr);
      height: auto;
    }
    .side {
      height: 260px;
      order: 2;
    }
    .main {
      min-height: 760px;
    }
    .req {
      max-height: none;
    }
    .kvr {
      grid-template-columns: 22px 1fr 1fr 26px;
    }
  }
</style>
