<script lang="ts">
  import { untrack } from 'svelte';
  import type { AccessRule, Account, Action, DeploymentSummary, Resource, ValidationIssue } from '@opm/shared';
  import { DEVELOPER_RESOURCES, RESOURCES, VALID_ACTIONS, fullAdminRules, isAdmin, validateAccessRules } from '@opm/shared';
  import { page } from '$app/state';
  import { api, errIssues } from '$lib/api';
  import { Loader } from '$lib/resource.svelte';
  import { session } from '$lib/session.svelte';
  import { toast } from '$lib/toast.svelte';
  import { catalog } from '$lib/stores/catalog.svelte';
  import { useLeaveGuard } from '$lib/util/guard.svelte';
  import { validateProfile } from '$lib/util/validators';
  import { errorsAt, mergeIssues } from '$lib/util/issues';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import HistoryButton from '$lib/audit/HistoryButton.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Field from '$lib/ui/Field.svelte';
  import ConfirmDialog from '$lib/ui/ConfirmDialog.svelte';
  import ErrorState from '$lib/ui/ErrorState.svelte';
  import FormSummary from '$lib/ui/FormSummary.svelte';
  import MultiSelect from '$lib/ui/MultiSelect.svelte';
  import Select from '$lib/ui/Select.svelte';
  import IconPicker from '$lib/ui/IconPicker.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import TagPicker from '$lib/ui/TagPicker.svelte';
  import RelTime from '$lib/ui/RelTime.svelte';
  import RevealValue from '$lib/ui/RevealValue.svelte';
  import { personName } from '$lib/util/people';
  import IdChip from '$lib/ui/IdChip.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import { Plus, X, ArrowUp, ArrowDown, ShieldCheck, Ban, CheckCircle, KeyRound, Undo2 } from '$lib/ui/icons';

  const uid = $derived(page.params.uid!);
  const acc = new Loader<Account>(() => api.getAccount({ uid }));
  const deps = new Loader<DeploymentSummary[]>(async () => {
    const out: DeploymentSummary[] = [];
    let cursor: string | null = null;
    do {
      const r: { items: DeploymentSummary[]; cursor: string | null } = await api.listDeployments({ query: { cursor: cursor ?? undefined, limit: 100 } });
      out.push(...r.items);
      cursor = r.cursor;
    } while (cursor && out.length < 1000);
    return out;
  });

  let prof = $state<{ name: string; phone: string; img: string | null }>({ name: '', phone: '', img: null });
  let profBase = $state('');
  let rules = $state<AccessRule[]>([]);
  let rulesBase = $state('');
  function resetProfile(a: Account) {
    prof = { name: a.name, phone: a.phone ?? '', img: a.img ?? null };
    profBase = JSON.stringify(prof);
  }
  function resetRules(a: Account) {
    rules = structuredClone($state.snapshot(a.access));
    rulesBase = JSON.stringify(rules);
  }
  $effect(() => {
    void uid;
    untrack(() => {
      acc.load().then((a) => {
        if (a) {
          resetProfile(a);
          resetRules(a);
        }
      });
      catalog.ensure();
      deps.load();
    });
  });

  const me = $derived(session.account?.uid === uid);
  const admin = $derived(session.can('accounts', 'admin'));
  const canProfile = $derived(me || session.can('accounts', 'write'));
  const profDirty = $derived(!!profBase && JSON.stringify(prof) !== profBase);
  const rulesDirty = $derived(!!rulesBase && JSON.stringify(rules) !== rulesBase);
  useLeaveGuard(() => profDirty || rulesDirty);

  // ── profile
  let profSaving = $state(false);
  let profTouched = $state(false);
  let profServer = $state<ValidationIssue[]>([]);
  const profLocal = $derived(validateProfile(prof, false));
  const profIssues = $derived(mergeIssues(profLocal, profServer));
  async function saveProfile(e: Event) {
    e.preventDefault();
    profTouched = true;
    if (profLocal.length || !profDirty) return;
    const a = acc.data!;
    profSaving = true;
    profServer = [];
    try {
      const body: { name?: string; phone?: string | null; img?: string | null } = {};
      if (prof.name !== a.name) body.name = prof.name.trim();
      if (prof.phone !== (a.phone ?? '')) body.phone = prof.phone.trim() || null;
      if (prof.img !== (a.img ?? null)) body.img = prof.img;
      const r = await api.patchAccount({ uid, body });
      acc.set(r);
      resetProfile(r);
      if (me) session.refreshMe();
      catalog.invalidate();
      toast.success('Profile saved');
    } catch (err) {
      profServer = errIssues(err);
      toast.error(err, 'Not saved');
    } finally {
      profSaving = false;
    }
  }

  // ── access rules
  const isDev = (r: Resource) => (DEVELOPER_RESOURCES as readonly string[]).includes(r);
  const depOpts = $derived((deps.data ?? []).map((d) => ({ value: d.deploymentId, label: d.label, hint: d.tagSlugs.join(', ') || d.deploymentId.slice(0, 8), icon: d.image ?? null })));
  const rulesLocal = $derived(validateAccessRules($state.snapshot(rules), catalog.loaded ? catalog.tags.map((t) => t.tagSlug) : undefined, deps.data ? deps.data.map((d) => d.deploymentId) : undefined));
  let rulesServer = $state<ValidationIssue[]>([]);
  let rulesTouched = $state(false);
  const rulesIssues = $derived(mergeIssues(rulesLocal, rulesServer));
  const re = (i: number, k?: string) => (rulesTouched || rulesServer.length ? errorsAt(rulesIssues, k ? `access[${i}].${k}` : `access[${i}]`) : []);
  const LABEL: Record<Resource, string> = {
    registries: 'Registries',
    services: 'Services',
    images: 'Images',
    notFoundPages: 'Not-found pages',
    templates: 'Templates',
    tags: 'Tags',
    accounts: 'Accounts',
    deployments: 'Deployments',
    'deployment.setup': 'Deployment · setup',
    'deployment.config': 'Deployment · config',
    'deployment.secrets': 'Deployment · secrets',
    'deployment.activities': 'Deployment · activities',
    'deployment.transfers': 'Deployment · transfers',
  };
  const RESOURCE_OPTIONS = RESOURCES.map((res) => ({ value: res, label: LABEL[res] }));
  function addRule() {
    rules.push({ resource: 'deployments', actions: ['read'], scope: { kind: 'all' } });
  }
  function setResource(i: number, res: Resource) {
    const r = rules[i]!;
    r.resource = res;
    r.actions = r.actions.filter((a) => VALID_ACTIONS[res].includes(a));
    if (!r.actions.length) r.actions = ['read'];
    if (isDev(res)) r.scope = { kind: 'all' };
  }
  function toggleAction(i: number, a: Action) {
    const r = rules[i]!;
    r.actions = r.actions.includes(a) ? r.actions.filter((x) => x !== a) : VALID_ACTIONS[r.resource].filter((x) => x === a || r.actions.includes(x));
  }
  function setScopeKind(i: number, k: string) {
    const r = rules[i]!;
    r.scope = k === 'tags' ? { kind: 'tags', tagSlugs: [] } : k === 'deployments' ? { kind: 'deployments', deploymentIds: [] } : { kind: 'all' };
  }
  function move(i: number, d: number) {
    const j = i + d;
    if (j < 0 || j >= rules.length) return;
    const n = [...rules];
    [n[i], n[j]] = [n[j]!, n[i]!];
    rules = n;
  }
  let rulesSaving = $state(false);
  async function saveRules() {
    rulesTouched = true;
    if (rulesLocal.length) {
      toast.error('Fix the highlighted rules first');
      return;
    }
    rulesSaving = true;
    rulesServer = [];
    try {
      const r = await api.patchAccount({ uid, body: { access: $state.snapshot(rules) } });
      acc.set(r);
      resetRules(r);
      if (me) session.refreshMe();
      toast.success('Access saved');
    } catch (err) {
      rulesServer = errIssues(err);
      toast.error(err, 'Access not saved');
    } finally {
      rulesSaving = false;
    }
  }
  let adminOpen = $state(false);

  // ── password (§21 #84): the last password an admin set + the last reset link, viewable any time (60 s, logged)
  const linkValid = (a: Account) => !!a.lastResetLink && Date.parse(a.lastResetLink.expiresAt) > Date.now();
  const reloadAcc = () => void acc.load(true);
  let disOpen = $state(false);
  async function toggleDisabled() {
    const a = acc.data!;
    const r = await api.disableAccount({ uid, body: { disabled: !a.disabled } });
    acc.set(r);
    toast.success(r.disabled ? 'Account disabled' : 'Account enabled');
  }
</script>

<svelte:head><title>{acc.data?.name ?? 'Account'} · OPM</title></svelte:head>
<div class="page">
  {#if acc.loading && !acc.data}
    <div class="stack"><span class="skel" style="width:240px;height:22px"></span><span class="skel" style="width:100%;height:200px"></span></div>
  {:else if acc.error}
    <PageHeader title="Account" back={{ href: '/app/accounts', label: 'Accounts' }} />
    <ErrorState message={acc.error} onretry={() => acc.load()} />
  {:else if acc.data}
    {@const a = acc.data}
    <PageHeader title={a.name} back={{ href: '/app/accounts', label: 'Accounts' }} subtitle={a.email} readOnly={!canProfile && !admin}>
      {#snippet meta()}
        {#if isAdmin(a)}<span class="badge accent">admin</span>{/if}
        {#if a.disabled}<span class="badge red"><span class="dot"></span>disabled</span>{:else}<span class="badge green"><span class="dot"></span>active</span>{/if}
        {#if me}<span class="badge outline">you</span>{/if}
      {/snippet}
      <div class="row wrap small muted" style="margin-top:6px;gap:14px"><span>Last login <RelTime ts={a.lastLoginAt} /></span><span>Created <RelTime ts={a.createdAt} /></span><IdChip id={a.uid} label="uid" /></div>
      {#snippet actions()}
        <HistoryButton target="accounts/{a.uid}" title="History of {a.name}" />
        {#if admin}
          {#if !me}<Button icon={a.disabled ? CheckCircle : Ban} variant={a.disabled ? 'default' : 'danger-outline'} onclick={() => (disOpen = true)} data-testid="toggle-disabled">{a.disabled ? 'Enable' : 'Disable'}</Button>{/if}
        {/if}
      {/snippet}
    </PageHeader>

    {#if admin}
      <section class="card pwc" data-testid="password-panel">
        <div class="card-head"><Icon icon={KeyRound} size={15} /><h3>Password</h3><span class="muted small">View shows a value for 60 s; every view is in the audit log.</span></div>
        <div class="card-body">
          <dl class="kv small pwkv">
            <dt>Password</dt>
            <dd class="stack tight">
              <RevealValue
                testid="account-password"
                stored={!!a.lastSetPassword}
                notStoredText="No password stored — set a new one"
                lengthHint={a.lastSetPassword ? 20 : null}
                reveal={() => api.revealAccountPassword({ uid })}
                regenerate={() => api.setAccountPassword({ uid })}
                regenerateLabel="Set new password…"
                confirm={{ title: `Set a new password for ${a.name}?`, message: `OPM generates a new password and sets it now. ${a.name} is signed out everywhere and must log in with the new password.`, typed: 'ACCEPT', confirmLabel: 'Set new password', danger: true }}
                onregenerated={reloadAcc}
                ongone={reloadAcc}
              />
              {#if a.lastSetPassword}<span class="xs faint" data-testid="password-meta">set <RelTime ts={a.lastSetPassword.setAt} /> by {personName(a.lastSetPassword.setBy)} · cleared when {me ? 'you change' : 'the person changes'} it</span>{/if}
            </dd>
            <dt>Reset link</dt>
            <dd class="stack tight">
              <RevealValue
                testid="reset-link"
                variant="command"
                stored={linkValid(a)}
                notStoredText={a.lastResetLink ? 'The last reset link expired' : 'No reset link'}
                reveal={() => api.revealAccountResetLink({ uid })}
                regenerate={() => api.resetAccountPassword({ uid })}
                regenerateLabel="Reset link…"
                confirm={{ title: `Make a reset link for ${a.name}?`, message: 'A new link (valid 1 h) lets the person choose their own password. It replaces the previous link.', confirmLabel: 'Make link' }}
                onregenerated={reloadAcc}
                ongone={reloadAcc}
              />
              {#if a.lastResetLink}<span class="xs faint" data-testid="reset-link-meta">made <RelTime ts={a.lastResetLink.setAt} /> by {personName(a.lastResetLink.setBy)} · {linkValid(a) ? 'expires' : 'expired'} <RelTime ts={a.lastResetLink.expiresAt} /></span>{/if}
            </dd>
          </dl>
        </div>
      </section>
    {/if}

    <div class="layout">
      <form class="card" onsubmit={saveProfile} novalidate data-testid="profile-form">
        <div class="card-head"><h3>Profile</h3>{#if profDirty}<span class="badge amber">unsaved</span>{/if}</div>
        <div class="card-body stack">
          <FormSummary issues={profServer} />
          <Field label="Avatar" id="ap-img" hint="An icon or an uploaded picture; none = initials" error={errorsAt(profIssues, 'img')}>
            <IconPicker bind:value={prof.img} type="account" initials={prof.name || a.name} id="ap-img" testid="ap-img" disabled={!canProfile} title="Choose an avatar" />
          </Field>
          <Field label="Name" id="ap-name" required error={profTouched || profServer.length ? errorsAt(profIssues, 'name') : null}><input id="ap-name" class="input" bind:value={prof.name} maxlength="80" disabled={!canProfile} data-testid="ap-name" /></Field>
          <Field label="Email" id="ap-email" hint="Cannot change"><input id="ap-email" class="input" value={a.email} readonly /></Field>
          <Field label="Phone" id="ap-phone" hint="E.164 or empty" error={errorsAt(profIssues, 'phone')}><input id="ap-phone" class="input" bind:value={prof.phone} disabled={!canProfile} /></Field>
          {#if canProfile}
            <div class="row" style="justify-content:flex-end">
              <Button icon={Undo2} disabled={!profDirty} onclick={() => resetProfile(a)}>Reset</Button>
              <Button type="submit" variant="primary" loading={profSaving} disabled={!profDirty || profLocal.length > 0} data-testid="save-profile">Save profile</Button>
            </div>
          {/if}
        </div>
      </form>

      <section class="card" data-testid="access-editor">
        <div class="card-head">
          <Icon icon={ShieldCheck} size={15} /><h3>Access rules</h3>
          <span class="muted small">{rules.length} rule{rules.length === 1 ? '' : 's'} · first match wins · catalog read is implicit</span>
          {#if rulesDirty}<span class="badge amber">unsaved</span>{/if}
        </div>
        <div class="card-body stack">
          {#if !admin}<p class="small muted">Only an account with accounts.admin changes access.</p>{/if}
          <FormSummary issues={rulesServer} />
          {#if !rules.length}
            <div class="empty small muted">No rules: this person sees only the catalog (services, images, pages, tags) and deployments they manage.</div>
          {/if}
          {#each rules as r, i (i)}
            <div class="rule" class:bad={re(i).length || re(i, 'actions').length || re(i, 'scope').length || re(i, 'scope.tagSlugs').length || re(i, 'scope.deploymentIds').length} data-testid="rule-row">
              <div class="rn">{i + 1}</div>
              <div class="rb">
                <div class="rl">
                  <Field label="Resource" id="rr-{i}" error={re(i, 'resource')}>
                    <Select id="rr-{i}" value={r.resource} options={RESOURCE_OPTIONS} disabled={!admin} ariaLabel="Resource" testid="rule-resource" onchange={(v) => setResource(i, v as Resource)} />
                  </Field>
                  <Field label="Actions" error={re(i, 'actions')}>
                    <div class="acts">
                      {#each VALID_ACTIONS[r.resource] as act (act)}
                        <label class="check"><input type="checkbox" checked={r.actions.includes(act)} onchange={() => toggleAction(i, act)} disabled={!admin} data-testid="rule-action-{act}" />{act}</label>
                      {/each}
                    </div>
                  </Field>
                  <Field label="Scope" id="rs-{i}" error={re(i, 'scope')}>
                    <span data-tip={isDev(r.resource) ? 'Developer resources always use scope all' : undefined}>
                      <SegmentedControl options={['all', 'tags', 'deployments']} value={r.scope.kind} onchange={(k) => setScopeKind(i, k)} disabled={!admin || isDev(r.resource)} ariaLabel="Scope" testid="rule-scope" />
                    </span>
                  </Field>
                </div>
                {#if r.scope.kind === 'tags'}
                  {@const sc = r.scope}
                  <Field label="Tags" error={re(i, 'scope.tagSlugs')}><TagPicker bind:value={sc.tagSlugs} placeholder="Choose tags" disabled={!admin} testid="rule-tags" /></Field>
                {:else if r.scope.kind === 'deployments'}
                  {@const sc = r.scope}
                  <Field label="Deployments" error={re(i, 'scope.deploymentIds')}><MultiSelect options={depOpts} bind:value={sc.deploymentIds} placeholder="Choose deployments" disabled={!admin} testid="rule-deps" /></Field>
                {/if}
              </div>
              {#if admin}
                <div class="rc">
                  <button type="button" class="btn ghost icon sm" aria-label="Move up" disabled={i === 0} onclick={() => move(i, -1)}><Icon icon={ArrowUp} size={13} /></button>
                  <button type="button" class="btn ghost icon sm" aria-label="Move down" disabled={i === rules.length - 1} onclick={() => move(i, 1)}><Icon icon={ArrowDown} size={13} /></button>
                  <button type="button" class="btn ghost icon sm" aria-label="Remove rule {i + 1}" onclick={() => rules.splice(i, 1)}><Icon icon={X} size={13} /></button>
                </div>
              {/if}
            </div>
          {/each}
          {#if admin}
            <div class="row wrap">
              <Button icon={Plus} onclick={addRule} data-testid="add-rule">Add rule</Button>
              <Button icon={ShieldCheck} onclick={() => (adminOpen = true)} data-testid="make-admin">Make full admin</Button>
              <span class="spacer"></span>
              <Button icon={Undo2} disabled={!rulesDirty} onclick={() => resetRules(a)}>Reset</Button>
              <Button variant="primary" loading={rulesSaving} disabled={!rulesDirty || (rulesTouched && rulesLocal.length > 0)} onclick={saveRules} data-testid="save-access">Save access</Button>
            </div>
          {/if}
        </div>
      </section>
    </div>
  {/if}
</div>

<ConfirmDialog bind:open={adminOpen} title="Make {acc.data?.name} a full admin?" message="Replaces the rules with every resource, every action, scope all (incl. accounts.admin). Save access to apply." confirmLabel="Fill admin rules" onconfirm={() => (rules = fullAdminRules())} />
<ConfirmDialog bind:open={disOpen} title="{acc.data?.disabled ? 'Enable' : 'Disable'} {acc.data?.name}?" message={acc.data?.disabled ? 'The person can log in again.' : 'Refresh tokens are revoked; the person cannot log in until enabled.'} confirmLabel={acc.data?.disabled ? 'Enable' : 'Disable'} danger={!acc.data?.disabled} onconfirm={toggleDisabled} />

<style>
  .layout {
    display: grid;
    grid-template-columns: minmax(280px, 0.8fr) minmax(0, 1.6fr);
    gap: var(--sp-4);
    align-items: start;
  }
  @media (max-width: 1050px) {
    .layout {
      grid-template-columns: minmax(0, 1fr);
    }
  }
  .rule {
    display: flex;
    gap: 10px;
    padding: 12px;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    background: var(--surface-2);
    align-items: flex-start;
  }
  .rule.bad {
    border-color: color-mix(in srgb, var(--red) 45%, var(--border));
  }
  .rn {
    width: 22px;
    height: 22px;
    border-radius: 50%;
    background: var(--surface-active);
    color: var(--text-2);
    font-size: 11.5px;
    font-weight: 600;
    display: grid;
    place-items: center;
    flex: none;
    margin-top: 24px;
  }
  .rb {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .rl {
    display: grid;
    grid-template-columns: minmax(170px, 1fr) minmax(160px, 1.2fr) auto;
    gap: 10px;
  }
  @media (max-width: 640px) {
    .rl {
      grid-template-columns: minmax(0, 1fr);
    }
  }
  .acts {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 14px;
    min-height: var(--control-h);
    align-items: center;
  }
  .rc {
    display: flex;
    flex-direction: column;
    gap: 2px;
    margin-top: 20px;
  }
  .empty {
    padding: 14px;
    border: 1px dashed var(--border-strong);
    border-radius: var(--r-md);
  }
  .pwc {
    margin-bottom: var(--sp-4);
  }
  .pwkv dd {
    min-width: 0;
  }
</style>
