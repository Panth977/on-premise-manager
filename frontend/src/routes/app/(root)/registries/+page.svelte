<script lang="ts">
  import type { RegistryView, ValidationIssue } from '@opm/shared';
  import type { RegistryTestResponse } from '@opm/shared';
  import { api, authFetch, errIssues, errMsg, isApiError } from '$lib/api';
  import { Loader } from '$lib/resource.svelte';
  import { session } from '$lib/session.svelte';
  import { toast } from '$lib/toast.svelte';
  import { validateRegistry } from '$lib/util/validators';
  import { errorsAt, mergeIssues } from '$lib/util/issues';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import HistoryButton from '$lib/audit/HistoryButton.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import Field from '$lib/ui/Field.svelte';
  import ConfirmDialog from '$lib/ui/ConfirmDialog.svelte';
  import TableSkeleton from '$lib/ui/TableSkeleton.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import ErrorState from '$lib/ui/ErrorState.svelte';
  import SearchInput from '$lib/ui/SearchInput.svelte';
  import RevealValue from '$lib/ui/RevealValue.svelte';
  import RelTime from '$lib/ui/RelTime.svelte';
  import FormSummary from '$lib/ui/FormSummary.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import Toggle from '$lib/ui/Toggle.svelte';
  import Combobox, { type ComboOption } from '$lib/ui/Combobox.svelte';
  import Select from '$lib/ui/Select.svelte';
  import { Plus, Pencil, Trash2, Database, Zap } from '$lib/ui/icons';
  import { onMount, tick } from 'svelte';
  import { page } from '$app/state';

  const list = new Loader(() => api.listRegistries({}).then((r) => r.items));
  onMount(() => list.load());
  /** ?registry=… (audit-log links) highlights that row */
  const hl = $derived(page.url.searchParams.get('registry'));
  $effect(() => {
    if (!hl || !list.data) return;
    tick().then(() => document.getElementById(`registry-${hl}`)?.scrollIntoView({ block: 'center' }));
  });
  const canWrite = $derived(session.can('registries', 'write'));

  let q = $state('');
  const rows = $derived((list.data ?? []).filter((r) => !q || `${r.label} ${r.url} ${r.username}`.toLowerCase().includes(q.toLowerCase())));

  // ── create / edit modal
  type Provider = 'hub' | 'ar' | 'ghcr' | 'custom';
  const HUB = 'https://registry-1.docker.io';
  const GHCR = 'https://ghcr.io';
  const AR_REGIONS = ['asia-south1', 'asia-south2', 'asia-southeast1', 'asia-east1', 'asia-northeast1', 'australia-southeast1', 'europe-west1', 'europe-west2', 'europe-west3', 'europe-west4', 'me-central1', 'us-central1', 'us-east1', 'us-east4', 'us-west1', 'asia', 'europe', 'us'];
  const providerOf = (url: string): { p: Provider; region: string } => {
    if (!url || url === HUB) return { p: 'hub', region: 'asia-south1' };
    if (url === GHCR) return { p: 'ghcr', region: 'asia-south1' };
    const m = /^https:\/\/([a-z0-9-]+)-docker\.pkg\.dev$/.exec(url);
    if (m && AR_REGIONS.includes(m[1]!)) return { p: 'ar', region: m[1]! };
    return { p: 'custom', region: 'asia-south1' };
  };
  let formOpen = $state(false);
  let editing = $state<RegistryView | null>(null);
  let provider = $state<Provider>('hub');
  let region = $state('asia-south1');
  let anonymous = $state(false);
  let customUrl = $state('');
  let f = $state({ label: '', url: '', username: '', password: '' });
  $effect(() => {
    f.url = provider === 'hub' ? HUB : provider === 'ghcr' ? GHCR : provider === 'ar' ? `https://${region}-docker.pkg.dev` : customUrl.trim();
  });
  const wasAnonymous = $derived(!!editing && !editing.username);
  let touched = $state(false);
  let saving = $state(false);
  let server = $state<ValidationIssue[]>([]);
  const local = $derived(validateRegistry({ ...f, anonymous }, !editing || (wasAnonymous && !anonymous)));
  const issues = $derived(mergeIssues(local, server));
  const show = (p: string) => (touched || server.length ? errorsAt(issues, p) : []);

  function openNew() {
    editing = null;
    provider = 'hub';
    region = 'asia-south1';
    customUrl = '';
    anonymous = false;
    f = { label: '', url: HUB, username: '', password: '' };
    touched = false;
    server = [];
    formOpen = true;
  }
  function openEdit(r: RegistryView) {
    editing = r;
    const pr = providerOf(r.url);
    provider = pr.p;
    region = pr.region;
    customUrl = pr.p === 'custom' ? r.url : '';
    anonymous = !r.username;
    f = { label: r.label, url: r.url, username: r.username, password: '' };
    touched = false;
    server = [];
    formOpen = true;
  }
  async function save(e: Event) {
    e.preventDefault();
    touched = true;
    if (local.length) return;
    saving = true;
    server = [];
    try {
      if (editing) {
        const body: Record<string, string> = {};
        if (f.label !== editing.label) body.label = f.label.trim();
        if ((f.url || HUB) !== editing.url) body.url = f.url.trim() || HUB;
        if (anonymous) {
          if (editing.username) {
            body.username = '';
            body.password = '';
          }
        } else {
          if (f.username !== editing.username) body.username = f.username;
          if (f.password) body.password = f.password;
        }
        if (!Object.keys(body).length) {
          formOpen = false;
          return;
        }
        await api.patchRegistry({ registryId: editing.registryId, body });
        toast.success('Registry saved', body.url || body.username || body.password ? 'The last check was reset; test the login again.' : undefined);
      } else {
        await api.createRegistry({ body: { label: f.label.trim(), username: anonymous ? '' : f.username, password: anonymous ? '' : f.password, ...(f.url.trim() ? { url: f.url.trim() } : {}) } });
        toast.success('Registry created');
      }
      formOpen = false;
      list.load(true);
    } catch (err) {
      server = errIssues(err);
      toast.error(err, 'Not saved');
    } finally {
      saving = false;
    }
  }

  // ── test login (modal with an optional repository: checks pull access to it too)
  let testing = $state<Record<string, boolean>>({});
  let testFor = $state<RegistryView | null>(null);
  let testOpen = $state(false);
  let testRepo = $state('');
  let testResult = $state<RegistryTestResponse | null>(null);
  function openTest(r: RegistryView) {
    testFor = r;
    testRepo = '';
    testResult = null;
    testOpen = true;
  }
  async function loadRepos(q: string): Promise<ComboOption[]> {
    if (!testFor || !session.can('images', 'write')) return [];
    const r = await api.listRegistryRepositories({ registryId: testFor.registryId, query: q ? { q } : {} });
    return r.items.map((x) => ({ value: x.name, sub: x.description ?? undefined, badge: x.official ? 'official' : undefined }));
  }
  async function runTest() {
    const r = testFor!;
    testing[r.registryId] = true;
    testResult = null;
    try {
      let res: RegistryTestResponse;
      if (testRepo.trim()) {
        // the typed client sends no body for testRegistry; the optional { repository } goes as raw JSON
        const resp = await authFetch(`/v1/registries/${encodeURIComponent(r.registryId)}/test`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ repository: testRepo.trim() }) });
        const j = await resp.json();
        if (!resp.ok) throw new Error(j?.error?.message ?? `HTTP ${resp.status}`);
        res = j as RegistryTestResponse;
      } else res = await api.testRegistry({ registryId: r.registryId });
      testResult = res;
      if (res.ok) toast.success(`${r.label}: ${res.message}`);
      else toast.push('error', `${r.label}: login failed`, res.message);
      await list.load(true);
    } catch (err) {
      testResult = { ok: false, message: errMsg(err), at: new Date().toISOString() };
      toast.error(err, 'Test failed');
    } finally {
      testing[r.registryId] = false;
    }
  }

  // ── delete
  let del = $state<RegistryView | null>(null);
  let delOpen = $state(false);
  let delUsedBy = $state<string[]>([]);
  async function doDelete() {
    try {
      await api.deleteRegistry({ registryId: del!.registryId });
      toast.success('Registry deleted');
      list.load(true);
    } catch (err) {
      if (isApiError(err) && err.code === 'conflict') {
        delUsedBy = (err.details.usedBy as string[]) ?? [];
        toast.push('error', 'The registry is in use', `Used by images: ${delUsedBy.join(', ')}`);
        return false;
      }
      throw err;
    }
  }
</script>

<svelte:head><title>Registries · OPM</title></svelte:head>

<div class="page">
  <PageHeader title="Registries" subtitle="Docker registries the cloud imports images from. Passwords are stored masked; View shows one for 60 s (logged)." icon={Database} readOnly={!canWrite}>
    {#snippet actions()}
      <SearchInput bind:value={q} placeholder="Search registries" />
      {#if canWrite}<Button variant="primary" icon={Plus} onclick={openNew} data-testid="new-registry">New registry</Button>{/if}
    {/snippet}
  </PageHeader>

  {#if list.loading && !list.data}
    <TableSkeleton cols={6} />
  {:else if list.error}
    <ErrorState message={list.error} onretry={() => list.load()} />
  {:else if !list.data?.length}
    <EmptyState icon={Database} title="No registries yet" text="Add the registry that holds your images (Docker Hub, Artifact Registry, GHCR …).">
      {#snippet action()}{#if canWrite}<Button variant="primary" icon={Plus} onclick={openNew}>New registry</Button>{/if}{/snippet}
    </EmptyState>
  {:else if !rows.length}
    <EmptyState compact title="No match" text="No registry matches “{q}”." />
  {:else}
    <div class="table-wrap">
      <table class="table" data-testid="registries-table">
        <thead><tr><th>Label</th><th>URL</th><th>Username</th><th>Password</th><th>Last check</th><th class="actions"><span class="sr-only">Actions</span></th></tr></thead>
        <tbody>
          {#each rows as r (r.registryId)}
            <tr data-testid="registry-row" class:hl={hl === r.registryId} id="registry-{r.registryId}">
              <td class="strong">{r.label}</td>
              <td class="mono small">{r.url}</td>
              <td class="mono small ellipsis" style="max-width:180px">{#if r.username}{r.username}{:else}<span class="badge outline">anonymous</span>{/if}</td>
              <td>{#if !r.username}<span class="faint small">—</span>{:else}<RevealValue isSet={r.isSet} canReveal={canWrite} variant={r.username === "_json_key" ? "command" : "inline"} reveal={() => api.revealRegistryPassword({ registryId: r.registryId })} testid="reveal-{r.registryId}" />{/if}</td>
              <td>
                {#if r.lastCheck}
                  <span class="row" data-tip={r.lastCheck.message}>
                    <span class="badge {r.lastCheck.ok ? 'green' : 'red'}"><span class="dot"></span>{r.lastCheck.ok ? 'ok' : 'failed'}</span>
                    <span class="small muted"><RelTime ts={r.lastCheck.at} /></span>
                  </span>
                {:else}<span class="faint small">never tested</span>{/if}
              </td>
              <td class="actions">
                <HistoryButton target="registries/{r.registryId}" title="History of {r.label}" iconOnly size="sm" />
                {#if canWrite}
                  <Button size="sm" icon={Zap} loading={testing[r.registryId]} onclick={() => openTest(r)} data-testid="test-registry">Test login</Button>
                  <Button size="sm" variant="ghost" icon={Pencil} tip="Edit" onclick={() => openEdit(r)} aria-label="Edit {r.label}" />
                  <Button size="sm" variant="ghost" icon={Trash2} tip="Delete" onclick={() => ((del = r), (delUsedBy = []), (delOpen = true))} aria-label="Delete {r.label}" />
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>

<Modal bind:open={formOpen} title={editing ? `Edit ${editing.label}` : 'New registry'} busy={saving} testid="registry-modal">
  <form id="reg-form" class="stack" onsubmit={save} novalidate>
    <FormSummary issues={server} />
    <Field label="Label" id="r-label" required error={show('label')}>
      <input id="r-label" class="input" bind:value={f.label} maxlength="80" placeholder="Artifact Registry" data-testid="reg-label" />
    </Field>
    <Field label="Registry" id="r-prov" error={provider === 'custom' ? [] : show('url')}>
      <SegmentedControl options={[{ value: 'hub', label: 'Docker Hub' }, { value: 'ar', label: 'Artifact Registry' }, { value: 'ghcr', label: 'GHCR' }, { value: 'custom', label: 'Custom' }] as { value: Provider; label: string }[]} bind:value={provider} ariaLabel="Registry provider" testid="reg-provider" />
    </Field>
    {#if provider === 'ar'}
      <Field label="Region" id="r-region" hint={f.url}>
        <Select id="r-region" value={region} options={AR_REGIONS.map((r) => ({ value: r, label: r, mono: true }))} ariaLabel="Region" testid="reg-region" onchange={(v) => (region = v)} />
      </Field>
    {:else if provider === 'custom'}
      <Field label="URL" id="r-url" required hint="https://host[:port] without a path" error={show('url')}>
        <input id="r-url" class="input mono" bind:value={customUrl} placeholder="https://harbor.example.com" data-testid="reg-url" />
      </Field>
    {:else}
      <p class="xs faint url">{f.url}</p>
    {/if}
    <Toggle bind:checked={anonymous} label="Anonymous (public images only, no login)" testid="reg-anonymous" />
    {#if !anonymous}
      <Field label="Username" id="r-user" required hint={provider === 'ar' ? 'Artifact Registry with a service-account key: _json_key' : provider === 'ghcr' ? 'Your GitHub user name' : undefined} error={show('username')}>
        <input id="r-user" class="input mono" bind:value={f.username} autocomplete="off" maxlength="255" placeholder={provider === 'ar' ? '_json_key' : ''} data-testid="reg-username" />
      </Field>
      <Field label={provider === 'ar' ? 'Service-account key (JSON)' : 'Password / token'} id="r-pw" required={!editing || wasAnonymous} hint={editing && !wasAnonymous ? 'Empty = unchanged · up to 8192 characters' : 'Up to 8192 characters (a _json_key JSON fits). Stored by the cloud; View shows it for 60 s (logged).'} error={show('password')}>
        <textarea id="r-pw" class="textarea mono pw" rows="3" bind:value={f.password} autocomplete="off" spellcheck="false" maxlength="8192" placeholder={editing && !wasAnonymous ? '•••  (unchanged)' : provider === 'ghcr' ? 'a personal access token (read:packages)' : 'token, password, or a pasted _json_key service-account JSON'} data-testid="reg-password"></textarea>
      </Field>
    {/if}
  </form>
  {#snippet footer()}
    <Button onclick={() => (formOpen = false)} disabled={saving}>Cancel</Button>
    <Button type="submit" form="reg-form" variant="primary" loading={saving} disabled={touched && local.length > 0} data-testid="reg-save">{editing ? 'Save' : 'Create registry'}</Button>
  {/snippet}
</Modal>

<Modal bind:open={testOpen} title="Test login · {testFor?.label ?? ''}" subtitle="Logs in to {testFor?.url ?? ''}. With a repository, also checks pull access to it." busy={!!testFor && testing[testFor.registryId]} testid="registry-test-modal">
  <div class="stack">
    <Field label="Repository (optional)" id="rt-repo" hint="e.g. library/redis — checks that this account may pull it">
      <Combobox bind:value={testRepo} load={loadRepos} freeText mono clearable placeholder="library/redis" id="rt-repo" testid="test-repo" emptyText="Type a repository name" />
    </Field>
    {#if testResult}
      <div class="banner {testResult.ok ? 'ok' : 'error'} small" data-testid="test-result"><div><b>{testResult.ok ? 'OK' : 'Failed'}:</b> {testResult.message}</div></div>
    {/if}
  </div>
  {#snippet footer()}
    <Button onclick={() => (testOpen = false)}>Close</Button>
    <Button variant="primary" icon={Zap} loading={!!testFor && testing[testFor.registryId]} onclick={runTest} data-testid="test-run">Test</Button>
  {/snippet}
</Modal>

<ConfirmDialog bind:open={delOpen} title="Delete registry {del?.label}?" message="Images keep nothing from the registry after their import, but a registry that images still use cannot be deleted." confirmLabel="Delete" danger onconfirm={doDelete}>
  {#snippet body()}
    {#if delUsedBy.length}
      <div class="banner error small"><div><b>Used by images:</b> {delUsedBy.join(', ')}</div></div>
    {/if}
  {/snippet}
</ConfirmDialog>

<style>
  tr.hl :global(td) {
    background: var(--accent-soft);
  }
  .pw {
    -webkit-text-security: disc;
    min-height: 64px;
  }
  .pw:focus {
    -webkit-text-security: none;
  }
  .url {
    margin-top: -8px;
    font-family: var(--font-mono);
  }
</style>
