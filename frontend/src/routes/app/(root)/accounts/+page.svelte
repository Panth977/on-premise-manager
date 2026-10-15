<script lang="ts">
  import type { Account, ValidationIssue } from '@opm/shared';
  import { isAdmin } from '@opm/shared';
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import { api, errIssues, isApiError } from '$lib/api';
  import { Loader } from '$lib/resource.svelte';
  import { session } from '$lib/session.svelte';
  import { toast } from '$lib/toast.svelte';
  import { catalog } from '$lib/stores/catalog.svelte';
  import { validateProfile } from '$lib/util/validators';
  import { errorsAt, mergeIssues, issue } from '$lib/util/issues';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import Field from '$lib/ui/Field.svelte';
  import IconPicker from '$lib/ui/IconPicker.svelte';
  import ConfirmDialog from '$lib/ui/ConfirmDialog.svelte';
  import TableSkeleton from '$lib/ui/TableSkeleton.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import ErrorState from '$lib/ui/ErrorState.svelte';
  import SearchInput from '$lib/ui/SearchInput.svelte';
  import FormSummary from '$lib/ui/FormSummary.svelte';
  import Avatar from '$lib/ui/Avatar.svelte';
  import RelTime from '$lib/ui/RelTime.svelte';
  import RevealValue, { type RevealResult } from '$lib/ui/RevealValue.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import { Plus, Users, Ban, CheckCircle, KeyRound } from '$lib/ui/icons';

  const list = new Loader(() => api.listAccounts({}).then((r) => r.items));
  onMount(() => list.load());
  const admin = $derived(session.can('accounts', 'admin'));
  let q = $state('');
  const rows = $derived((list.data ?? []).filter((a) => !q || `${a.name} ${a.email} ${a.phone ?? ''}`.toLowerCase().includes(q.toLowerCase())));
  const activeRows = $derived(rows.filter((a) => !a.disabled));
  const disabledRows = $derived(rows.filter((a) => a.disabled));

  let open = $state(false);
  let f = $state<{ name: string; email: string; phone: string; img: string | null }>({ name: '', email: '', phone: '', img: null });
  let touched = $state(false);
  let saving = $state(false);
  let server = $state<ValidationIssue[]>([]);
  const local = $derived(validateProfile(f, true));
  const issues = $derived(mergeIssues(local, server));
  const show = (p: string) => (touched || server.length ? errorsAt(issues, p) : []);
  let created = $state<{ account: Account; password: RevealResult } | null>(null);
  let linkOpen = $state(false);

  function openNew() {
    f = { name: '', email: '', phone: '', img: null };
    touched = false;
    server = [];
    open = true;
  }
  async function save(e: Event) {
    e.preventDefault();
    touched = true;
    if (local.length) return;
    saving = true;
    server = [];
    try {
      const r = await api.createAccount({ body: { name: f.name.trim(), email: f.email.trim().toLowerCase(), phone: f.phone.trim() || null, img: f.img } });
      catalog.invalidate();
      toast.success(`Account ${r.account.email} created`);
      open = false;
      created = { account: r.account, password: { value: r.value, hideAfterSec: r.hideAfterSec } };
      linkOpen = true;
      list.load(true);
    } catch (err) {
      if (isApiError(err) && err.code === 'conflict') server = [issue('email', 'V_DUPLICATE', 'An account with this email exists')];
      else server = errIssues(err);
      toast.error(err, 'Not created');
    } finally {
      saving = false;
    }
  }
  function closeLink() {
    linkOpen = false;
    if (created) goto(`/app/accounts/${created.account.uid}`);
  }

  let tgt = $state<Account | null>(null);
  let disOpen = $state(false);
  async function toggleDisabled() {
    const a = tgt!;
    await api.disableAccount({ uid: a.uid, body: { disabled: !a.disabled } });
    toast.success(a.disabled ? `${a.name} enabled` : `${a.name} disabled`);
    list.load(true);
  }
  const accessText = (a: Account) => (isAdmin(a) ? 'admin' : `${a.access.length} rule${a.access.length === 1 ? '' : 's'}`);
</script>

{#snippet accountsTable(items: Account[], testid: string)}
  <div class="table-wrap">
    <table class="table" data-testid={testid}>
      <thead><tr><th style="width:40px"></th><th>Name</th><th>Email</th><th>Phone</th><th>Access</th><th>State</th><th>Last login</th><th class="actions"><span class="sr-only">Actions</span></th></tr></thead>
      <tbody>
        {#each items as a (a.uid)}
          <tr class="clickable" onclick={() => goto(`/app/accounts/${a.uid}`)} data-testid="account-row">
            <td><Avatar name={a.name} img={a.img} size={28} /></td>
            <td><a class="strong" href="/app/accounts/{a.uid}" onclick={(e) => e.stopPropagation()}>{a.name}</a>{#if a.uid === session.account?.uid}<span class="badge outline" style="margin-left:6px">you</span>{/if}</td>
            <td class="small">{a.email}</td>
            <td class="small mono">{a.phone ?? '—'}</td>
            <td>{#if isAdmin(a)}<span class="badge accent">admin</span>{:else}<span class="small">{accessText(a)}</span>{/if}</td>
            <td>{#if a.disabled}<span class="badge red"><span class="dot"></span>disabled</span>{:else}<span class="badge green"><span class="dot"></span>active</span>{/if}</td>
            <td class="small muted"><RelTime ts={a.lastLoginAt} /></td>
            <td class="actions" onclick={(e) => e.stopPropagation()}>
              {#if admin && a.uid !== session.account?.uid}
                <Button size="sm" icon={a.disabled ? CheckCircle : Ban} onclick={() => ((tgt = a), (disOpen = true))} data-testid="toggle-account">{a.disabled ? 'Enable' : 'Disable'}</Button>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/snippet}

<svelte:head><title>Accounts · OPM</title></svelte:head>
<div class="page">
  <PageHeader title="Accounts" subtitle="People who can log in to the cloud console. There is no signup; an admin creates every account." icon={Users} readOnly={!admin}>
    {#snippet actions()}
      <SearchInput bind:value={q} placeholder="Search people" />
      {#if admin}<Button variant="primary" icon={Plus} onclick={openNew} data-testid="new-account">New account</Button>{/if}
    {/snippet}
  </PageHeader>
  {#if list.loading && !list.data}
    <TableSkeleton cols={7} />
  {:else if list.error}
    <ErrorState message={list.error} onretry={() => list.load()} />
  {:else if !list.data?.length}
    <EmptyState icon={Users} title="No accounts" text="Create the accounts of your team.">
      {#snippet action()}{#if admin}<Button variant="primary" icon={Plus} onclick={openNew}>New account</Button>{/if}{/snippet}
    </EmptyState>
  {:else if !rows.length}
    <EmptyState compact title="No match" text="No account matches “{q}”." />
  {:else}
    {#if activeRows.length}{@render accountsTable(activeRows, 'accounts-table')}{:else}<EmptyState compact title="No active account" text={q ? `No active account matches “${q}”.` : 'Every account is disabled.'} />{/if}
    {#if disabledRows.length}
      <div class="section-title disabled-head"><h3>Disabled</h3><span class="badge">{disabledRows.length}</span><span class="faint small">cannot log in until enabled again</span></div>
      {@render accountsTable(disabledRows, 'disabled-accounts-table')}
    {/if}
  {/if}
</div>

<Modal bind:open title="New account" subtitle="OPM sets a generated password and shows it next (viewable again on the account page)." busy={saving} testid="account-modal">
  <form id="acc-form" class="stack" onsubmit={save} novalidate>
    <FormSummary issues={server} />
    <Field label="Name" id="a-name" required error={show('name')}><input id="a-name" class="input" bind:value={f.name} maxlength="80" data-testid="acc-name" /></Field>
    <Field label="Email" id="a-email" required hint="Login name; cannot change later" error={show('email')}><input id="a-email" class="input" type="email" bind:value={f.email} data-testid="acc-email" /></Field>
    <Field label="Phone" id="a-phone" hint="E.164, e.g. +919812345678 — optional" error={show('phone')}><input id="a-phone" class="input" bind:value={f.phone} inputmode="tel" /></Field>
    <Field label="Avatar" id="a-img" hint="Optional — none = initials" error={show('img')}><IconPicker bind:value={f.img} type="account" initials={f.name || '?'} id="a-img" testid="acc-img" title="Choose an avatar" /></Field>
  </form>
  {#snippet footer()}
    <Button onclick={() => (open = false)} disabled={saving}>Cancel</Button>
    <Button type="submit" form="acc-form" variant="primary" loading={saving} disabled={touched && local.length > 0} data-testid="acc-save">Create account</Button>
  {/snippet}
</Modal>

<Modal bind:open={linkOpen} title="Account created" subtitle={created?.account.email} onclose={closeLink} testid="link-modal">
  <div class="stack">
    <p class="muted">Give this password to {created?.account.name} (they can change it after logging in). An admin can View it again or set a new one on the account page. The new account has no access rules yet — add them on the next page.</p>
    <div class="lnk"><Icon icon={KeyRound} size={15} />{#if created}<RevealValue testid="created-password" lengthHint={created.password.value.length} initial={created.password} reveal={() => api.revealAccountPassword({ uid: created!.account.uid })} />{/if}</div>
  </div>
  {#snippet footer()}<Button variant="primary" onclick={closeLink} data-testid="link-continue">Continue to the account</Button>{/snippet}
</Modal>

<ConfirmDialog bind:open={disOpen} title="{tgt?.disabled ? 'Enable' : 'Disable'} {tgt?.name}?" message={tgt?.disabled ? 'The person can log in again.' : 'The person is logged out (refresh tokens revoked) and cannot log in until enabled again.'} confirmLabel={tgt?.disabled ? 'Enable' : 'Disable'} danger={!tgt?.disabled} onconfirm={toggleDisabled} />

<style>
  .disabled-head {
    margin-top: 28px;
  }
  .lnk {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 10px 12px;
    background: var(--code-bg);
    border: 1px solid var(--border);
    border-radius: var(--r-md);
  }
</style>
