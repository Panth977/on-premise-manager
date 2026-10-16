<script lang="ts">
  /**
   * §21 #106 Admin → GitHub: the GitHub mirror of every OPM branch (push only). Repository + enabled, token (stored like a
   * registry password: masked, View 60 s, logged, replaceable), Test connection, the per-branch status table, Sync now /
   * Resync everything. Every change is audited (github.*). Perm accounts.admin.
   */
  import {
    GITHUB_OWNER_RE,
    GITHUB_REPO_RE,
    GITHUB_TOKEN_RE,
    type GithubMirrorView,
    type GithubTestResponse,
    type MirrorBranchState,
    type MirrorBranchKind,
    type ValidationIssue,
  } from '@opm/shared';
  import { api, errIssues, errMsg } from '$lib/api';
  import { Loader } from '$lib/resource.svelte';
  import { session } from '$lib/session.svelte';
  import { toast } from '$lib/toast.svelte';
  import { catalog } from '$lib/stores/catalog.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import HistoryButton from '$lib/audit/HistoryButton.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import Field from '$lib/ui/Field.svelte';
  import Toggle from '$lib/ui/Toggle.svelte';
  import ConfirmDialog from '$lib/ui/ConfirmDialog.svelte';
  import RevealValue from '$lib/ui/RevealValue.svelte';
  import RelTime from '$lib/ui/RelTime.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import ErrorState from '$lib/ui/ErrorState.svelte';
  import TableSkeleton from '$lib/ui/TableSkeleton.svelte';
  import CardSkeleton from '$lib/ui/CardSkeleton.svelte';
  import SearchInput from '$lib/ui/SearchInput.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import FormSummary from '$lib/ui/FormSummary.svelte';
  import CopyButton from '$lib/ui/CopyButton.svelte';
  import Popover from '$lib/ui/Popover.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import { GitBranch, RefreshCw, Zap, KeyRound, Trash2, ExternalLink, CheckCircle, CircleAlert, CirclePause, Info, TriangleAlert, Ban } from '$lib/ui/icons';
  import { onDestroy, onMount } from 'svelte';

  const admin = $derived(session.isAdmin);
  const mirror = new Loader(() => api.getGithubMirror({}));
  const rows = new Loader(() => api.listGithubMirrorBranches({}).then((r) => r.items));
  onMount(() => {
    if (!admin) return;
    catalog.ensure();
    mirror.load();
    rows.load();
  });

  // ── poll while something is queued / pushing
  let timer: ReturnType<typeof setTimeout> | null = null;
  const busyRows = $derived((rows.data ?? []).some((r) => r.status === 'pushing') || (!!mirror.data?.enabled && (rows.data ?? []).some((r) => r.status === 'queued')));
  $effect(() => {
    void rows.data; // re-arm after every refresh
    if (!busyRows) return;
    timer = setTimeout(() => refresh(), 1500);
    return () => {
      if (timer) clearTimeout(timer);
    };
  });
  onDestroy(() => timer && clearTimeout(timer));
  async function refresh() {
    await Promise.all([mirror.load(true), rows.load(true)]);
  }

  // ── repository form
  let owner = $state('');
  let repo = $state('');
  let enabled = $state(false);
  let seededFrom = $state<string | null>(null);
  $effect(() => {
    const v = mirror.data;
    if (!v) return;
    const k = `${v.owner}/${v.repo}/${v.enabled}/${v.updatedAt}`;
    if (k === seededFrom) return;
    seededFrom = k;
    owner = v.owner;
    repo = v.repo;
    enabled = v.enabled;
  });
  let saving = $state(false);
  let touched = $state(false);
  let server = $state<ValidationIssue[]>([]);
  const local = $derived.by(() => {
    const out: ValidationIssue[] = [];
    if (owner.trim() && !GITHUB_OWNER_RE.test(owner.trim())) out.push({ path: 'owner', code: 'V_FORMAT', message: 'A GitHub user or organization: letters, digits and "-" (≤ 39, no leading "-")', severity: 'error' });
    if (repo.trim() && !GITHUB_REPO_RE.test(repo.trim())) out.push({ path: 'repo', code: 'V_FORMAT', message: 'Letters, digits, ".", "_" and "-" (≤ 100)', severity: 'error' });
    if (enabled && (!owner.trim() || !repo.trim())) out.push({ path: !owner.trim() ? 'owner' : 'repo', code: 'V_REQUIRED', message: 'Enabling needs the owner and the repository name', severity: 'error' });
    if (enabled && mirror.data && !mirror.data.tokenSet) out.push({ path: 'enabled', code: 'V_REQUIRED', message: 'Add a token first, then enable', severity: 'error' });
    return out;
  });
  const issuesAt = (p: string) => (touched || server.length ? [...local, ...server].filter((i) => i.path === p).map((i) => i.message) : []);
  const dirty = $derived(!!mirror.data && (owner.trim() !== mirror.data.owner || repo.trim() !== mirror.data.repo || enabled !== mirror.data.enabled));
  const repoUrl = $derived(owner.trim() && repo.trim() && !local.some((i) => i.path === 'owner' || i.path === 'repo') ? `https://github.com/${owner.trim()}/${repo.trim()}` : null);
  async function saveRepo(e: Event) {
    e.preventDefault();
    touched = true;
    if (local.length) return;
    const v = mirror.data!;
    const body: { owner?: string; repo?: string; enabled?: boolean } = {};
    if (owner.trim() !== v.owner) body.owner = owner.trim();
    if (repo.trim() !== v.repo) body.repo = repo.trim();
    if (enabled !== v.enabled) body.enabled = enabled;
    saving = true;
    server = [];
    try {
      const out = await api.patchGithubMirror({ body });
      mirror.set(out);
      touched = false;
      const repoChanged = body.owner !== undefined || body.repo !== undefined;
      toast.success('GitHub mirror saved', out.enabled && (repoChanged || body.enabled) ? 'Every branch is queued for a catch-up push.' : !out.enabled ? 'Mirroring is off: nothing is pushed.' : undefined);
      rows.load(true);
    } catch (err) {
      server = errIssues(err);
      toast.error(err, 'Not saved');
    } finally {
      saving = false;
    }
  }
  function resetRepo() {
    const v = mirror.data!;
    owner = v.owner;
    repo = v.repo;
    enabled = v.enabled;
    touched = false;
    server = [];
  }

  // ── token
  let tokOpen = $state(false);
  let tokValue = $state('');
  let tokTouched = $state(false);
  let tokSaving = $state(false);
  let tokServer = $state('');
  const tokLocal = $derived(!tokValue.trim() ? 'Paste the token' : !GITHUB_TOKEN_RE.test(tokValue.trim()) ? 'A token is 20–255 printable characters without spaces (a fine-grained token starts with github_pat_)' : '');
  function openToken() {
    tokValue = '';
    tokTouched = false;
    tokServer = '';
    tokOpen = true;
  }
  async function saveToken(e: Event) {
    e.preventDefault();
    tokTouched = true;
    if (tokLocal) return;
    tokSaving = true;
    tokServer = '';
    try {
      const out = await api.patchGithubMirror({ body: { token: tokValue.trim() } });
      mirror.set(out);
      tokValue = '';
      tokOpen = false;
      toast.success(out.tokenSet ? 'Token saved' : 'Token removed', 'Run Test connection to check its permissions.');
      rows.load(true);
    } catch (err) {
      tokServer = errIssues(err)[0]?.message ?? errMsg(err);
      toast.error(err, 'Token not saved');
    } finally {
      tokSaving = false;
    }
  }
  let rmOpen = $state(false);
  async function removeToken() {
    const out = await api.patchGithubMirror({ body: { token: null } });
    mirror.set(out);
    toast.success('Token removed', 'Mirroring stopped: nothing is pushed until a new token is added.');
    rows.load(true);
  }
  const setBy = $derived.by(() => {
    const uid = mirror.data?.tokenSetBy;
    if (!uid) return null;
    const a = catalog.accounts.find((x) => x.uid === uid);
    return a?.name || a?.email || uid;
  });

  // ── test connection
  let testing = $state(false);
  let test = $state<GithubTestResponse | null>(null);
  const shownTest = $derived(test ?? mirror.data?.lastTest ?? null);
  async function runTest() {
    testing = true;
    try {
      test = await api.testGithubMirror({});
      if (test.ok) toast.success('Connection OK', `${test.repo?.fullName ?? ''} is reachable and writable.`);
      else toast.push('error', 'Connection failed', test.checks.find((c) => !c.ok)?.message);
      mirror.load(true);
    } catch (err) {
      toast.error(err, 'Test failed');
    } finally {
      testing = false;
    }
  }

  // ── sync / resync
  let syncing = $state<string | null>(null);
  async function sync(gitBranch?: string) {
    syncing = gitBranch ?? '*';
    try {
      const r = await api.syncGithubMirror({ body: gitBranch ? { gitBranch } : {} });
      toast.success(gitBranch ? `Sync ${gitBranch}` : 'Sync now', r.enqueued ? `${r.enqueued} branch${r.enqueued === 1 ? '' : 'es'} queued — pushing in order.` : 'Everything is already on GitHub.');
      await refresh();
    } catch (err) {
      toast.error(err, 'Sync failed');
    } finally {
      syncing = null;
    }
  }
  let resyncOpen = $state(false);
  async function resync() {
    const r = await api.resyncGithubMirror({ body: {} });
    toast.success('Resync started', `${r.branches} branches are rebuilt from OPM history.`);
    await refresh();
  }

  // ── table
  let q = $state('');
  type KindFilter = 'all' | MirrorBranchKind | 'problems';
  let kind = $state<KindFilter>('all');
  const shown = $derived(
    (rows.data ?? []).filter((r) => {
      if (kind === 'problems' && r.status !== 'error' && r.status !== 'paused') return false;
      if (kind !== 'all' && kind !== 'problems' && r.branch.kind !== kind) return false;
      return !q || `${r.gitBranch} ${r.label} ${r.last?.message ?? ''}`.toLowerCase().includes(q.toLowerCase());
    }),
  );
  const KIND_LABEL: Record<MirrorBranchKind, string> = { template: 'template', deployment: 'deployment', agent: 'agent', uncommitted: 'uncommitted' };
  const KIND_TONE: Record<MirrorBranchKind, string> = { template: 'violet', deployment: 'accent', agent: 'blue', uncommitted: 'amber' };
  const STATUS: Record<MirrorBranchState['status'], { tone: string; label: string }> = {
    synced: { tone: 'green', label: 'synced' },
    queued: { tone: 'blue', label: 'queued' },
    pushing: { tone: 'accent', label: 'pushing' },
    error: { tone: 'red', label: 'error' },
    paused: { tone: 'amber', label: 'paused' },
    deleted: { tone: 'outline', label: 'deleted' },
    never: { tone: 'outline', label: 'not pushed' },
  };
  const short = (sha: string | null) => (sha ? sha.slice(0, 7) : '');
  const paused = $derived(!!mirror.data?.pausedUntil && Date.parse(mirror.data.pausedUntil) > Date.now());
  const cloneHint = $derived(mirror.data?.repoUrl ? `${mirror.data.repoUrl}.git` : 'https://github.com/<owner>/<repo>.git');
  const exampleBranch = $derived((rows.data ?? []).find((r) => r.branch.kind === 'deployment')?.gitBranch ?? 'deployment/<slug>/cloud');
  // §21 #115 the branch a machine run with Docker only keeps (written by that machine, never by the mirror)
  const siteBranch = $derived(exampleBranch.replace(/\/cloud$/, '/site/<site>'));
  const runSteps = $derived(
    [
      `git clone ${cloneHint} opm && cd opm`,
      `git switch -c ${siteBranch} origin/${exampleBranch}`,
      'cd sites/<site> && cp site.env.example site.env   # fill it: secrets, this machine\'s address, Portainer password',
      './deploy.sh',
      `git push -u origin ${siteBranch}`,
      '# a new cloud commit, later:',
      `git fetch origin && git merge origin/${exampleBranch} && (cd sites/<site> && ./deploy.sh) && git push`,
    ].join('\n'),
  );
</script>

<svelte:head><title>GitHub · OPM</title></svelte:head>

<div class="page wide">
  <PageHeader title="GitHub mirror" subtitle="Every OPM branch is pushed to one GitHub repository as git branches — a backup and a side database of your configs. Each branch also runs with docker compose, without the agent." icon={GitBranch}>
    {#snippet actions()}
      {#if admin}
        <HistoryButton target="githubMirror/config" title="History of the GitHub mirror" />
        {#if mirror.data?.configured}
          <Button icon={RefreshCw} loading={syncing === '*'} disabled={!mirror.data.enabled} onclick={() => sync()} data-testid="gh-sync" tip={mirror.data.enabled ? 'Push everything that is not on GitHub yet; failed branches retry now' : 'Enable the mirror first'}>Sync now</Button>
          <Button variant="ghost" disabled={!mirror.data.enabled} onclick={() => (resyncOpen = true)} data-testid="gh-resync">Resync everything…</Button>
        {/if}
      {/if}
    {/snippet}
  </PageHeader>

  {#if !admin}
    <EmptyState icon={Ban} title="Admins only" text="The GitHub mirror is configured by an account with accounts.admin." />
  {:else if mirror.loading && !mirror.data}
    <CardSkeleton count={2} />
  {:else if mirror.error}
    <ErrorState message={mirror.error} onretry={() => mirror.load()} />
  {:else if mirror.data}
    {@const v = mirror.data}
    {#if paused}
      <div class="banner warn" data-testid="gh-paused">
        <Icon icon={CirclePause} size={16} class="icon" />
        <div class="grow">
          <b>Pushing is paused until <RelTime ts={v.pausedUntil!} />.</b>
          {v.pauseReason ?? ''}
          <div class="xs muted">Queued commits wait and are pushed in order afterwards. Sync now retries at once.</div>
        </div>
      </div>
    {/if}
    {#if v.configured && !v.enabled}
      <div class="banner info" data-testid="gh-disabled"><Icon icon={Info} size={16} class="icon" /><div>Mirroring is <b>off</b>: OPM records nothing for GitHub. Turn it on below — every branch is then pushed from OPM history (one git commit per OPM commit).</div></div>
    {/if}
    {#if !v.configured}
      <section class="card setup" data-testid="gh-setup">
        <div class="card-head"><h3>Set up the mirror</h3></div>
        <ol class="card-body steps">
          <li><b>Create a repository</b> on GitHub (private is recommended), e.g. <code class="inline">opm-mirror</code>. It may be empty: OPM creates the first commit.</li>
          <li>
            <b>Create a fine-grained personal access token</b>
            (<a href="https://github.com/settings/personal-access-tokens/new" target="_blank" rel="noopener noreferrer">GitHub → Settings → Developer settings → Fine-grained tokens <Icon icon={ExternalLink} size={12} /></a>): Repository access = <i>Only select repositories</i> → that repository; Permissions → Repository → <b>Contents: Read and write</b> (Metadata: read is added by GitHub).
          </li>
          <li><b>Enter both here</b>: owner + repository below, then Add token…, Test connection, and turn mirroring on.</li>
        </ol>
      </section>
    {/if}

    <div class="grid">
      <div class="stack loose">
        <section class="card" data-testid="gh-repo-card">
          <div class="card-head wrapc"><h3>Repository</h3><span class="spacer"></span>{#if v.repoUrl}<a class="small row" href={v.repoUrl} target="_blank" rel="noopener noreferrer" data-testid="gh-repo-link">{v.owner}/{v.repo} <Icon icon={ExternalLink} size={12} /></a>{/if}</div>
          <form class="card-body stack" onsubmit={saveRepo} novalidate>
            <FormSummary issues={server} />
            <div class="grid-2">
              <Field label="Owner" id="gh-owner" hint="GitHub user or organization" error={issuesAt('owner')}>
                <input id="gh-owner" class="input mono" bind:value={owner} maxlength="39" placeholder="acme-ops" autocomplete="off" spellcheck="false" data-testid="gh-owner" />
              </Field>
              <Field label="Repository" id="gh-repo" hint={repoUrl ?? 'the repository name'} error={issuesAt('repo')}>
                <input id="gh-repo" class="input mono" bind:value={repo} maxlength="100" placeholder="opm-mirror" autocomplete="off" spellcheck="false" data-testid="gh-repo" />
              </Field>
            </div>
            <Field label="Mirroring" id="gh-enabled" error={issuesAt('enabled')} hint="On: every commit of every template, deployment and agent branch is pushed (push only, OPM → GitHub).">
              <Toggle bind:checked={enabled} label={enabled ? 'On — push every OPM branch' : 'Off'} testid="gh-enabled" />
            </Field>
            <div class="row wrap">
              <Button type="submit" variant="primary" loading={saving} disabled={!dirty || (touched && local.length > 0)} data-testid="gh-save">Save</Button>
              {#if dirty}<Button onclick={resetRepo} disabled={saving}>Reset</Button>{/if}
              {#if v.updatedAt}<span class="xs faint">changed <RelTime ts={v.updatedAt} /> · every change is in the audit log</span>{/if}
            </div>
          </form>
        </section>

        <section class="card" data-testid="gh-token-card">
          <div class="card-head wrapc"><h3>Token</h3><span class="spacer"></span><span class="xs faint">fine-grained PAT · Contents: Read and write</span></div>
          <div class="card-body stack">
            {#if v.tokenSet}
              <div class="row wrap tokrow">
                <span class="grow" style="min-width:0"><RevealValue reveal={() => api.revealGithubToken({})} maskKey={v.tokenSetAt} testid="gh-token" /></span>
              </div>
              <div class="xs muted" data-testid="gh-token-meta">
                <span class="mono">{v.token}</span>{#if v.tokenSetAt}&nbsp;· set <RelTime ts={v.tokenSetAt} />{/if}{#if setBy}&nbsp;by {setBy}{/if}&nbsp;· View shows it for 60 s and is logged.
              </div>
              <div class="row wrap">
                <Button icon={KeyRound} onclick={openToken} data-testid="gh-token-replace">Replace…</Button>
                <Button variant="ghost" icon={Trash2} onclick={() => (rmOpen = true)} data-testid="gh-token-remove">Remove</Button>
              </div>
            {:else}
              <p class="small muted" data-testid="gh-token-none">No token stored. OPM pushes nothing without one.</p>
              <div class="row"><Button variant="primary" icon={KeyRound} onclick={openToken} data-testid="gh-token-add">Add token…</Button></div>
            {/if}
          </div>
        </section>

        <section class="card" data-testid="gh-test-card">
          <div class="card-head wrapc">
            <h3>Connection</h3><span class="spacer"></span>
            <Button size="sm" icon={Zap} loading={testing} disabled={!v.configured} onclick={runTest} data-testid="gh-test" tip={v.configured ? 'Checks the token, the repository and its Contents permission' : 'Set the repository and a token first'}>Test connection</Button>
          </div>
          <div class="card-body stack tight">
            {#if shownTest}
              <div class="row wrap small" data-testid="gh-test-summary">
                <span class="badge {shownTest.ok ? 'green' : 'red'}"><span class="dot"></span>{shownTest.ok ? 'OK' : 'failed'}</span>
                <span class="muted"><RelTime ts={shownTest.at} /></span>
                {#if shownTest.login}<span class="muted">as <b class="mono">{shownTest.login}</b></span>{/if}
                {#if shownTest.repo}<span class="badge outline">{shownTest.repo.private ? 'private' : 'public'}</span>{#if shownTest.repo.empty}<span class="badge amber" data-tip="OPM creates the first commit on main (a README) before the first push">empty</span>{/if}{/if}
              </div>
              <ul class="checks" data-testid="gh-test-checks">
                {#each shownTest.checks as c (c.name)}
                  <li class:bad={!c.ok} data-testid="gh-check-{c.name}">
                    <Icon icon={c.ok ? CheckCircle : CircleAlert} size={14} />
                    <span class="mono xs nm">{c.name}</span>
                    <span class="small msg">{c.message}</span>
                  </li>
                {/each}
              </ul>
              {#if shownTest.rate}<div class="xs faint">Rate limit: {shownTest.rate.remaining} / {shownTest.rate.limit} · resets <RelTime ts={shownTest.rate.resetAt} /></div>{/if}
              {#if shownTest.repo?.fullName && v.repoUrl && shownTest.repo.fullName.toLowerCase() !== `${v.owner}/${v.repo}`.toLowerCase()}<div class="banner warn small"><Icon icon={TriangleAlert} size={14} class="icon" /><div>Tested {shownTest.repo.fullName}; the repository changed since — test again.</div></div>{/if}
            {:else}
              <p class="small muted">Not tested yet.</p>
            {/if}
          </div>
        </section>
      </div>

      <aside class="card help" data-testid="gh-help">
        <div class="card-head"><h3>What is in the repository</h3></div>
        <div class="card-body stack small">
          <ul class="branches">
            <li><code class="inline">template/&lt;slug&gt;</code> one git commit per template commit</li>
            <li><code class="inline">deployment/&lt;slug&gt;/cloud</code> the deployment branch (what the sites should run)</li>
            <li><code class="inline">deployment/&lt;slug&gt;/agent/&lt;site&gt;</code> each site's own branch + its persist-to-cloud volume snapshots</li>
            <li><code class="inline">deployment/&lt;slug&gt;/uncommitted/&lt;site&gt;</code> manual changes found on that site, not committed yet (deleted when committed or discarded)</li>
            <li><code class="inline">deployment/&lt;slug&gt;/site/&lt;site&gt;</code> a site run with Docker only: pushed by that machine, never written by the mirror</li>
          </ul>
          <p class="muted">Same message, author and time as in OPM, with a trailer <code class="inline">OPM-Commit: &lt;id&gt;</code>. Secret values are never pushed. The slug is the template's / deployment's fixed slug.</p>
          <div>
            <div class="strong">Run a site with Docker only (no agent)</div>
            <div class="codebox">
              <pre class="code wrap" data-testid="gh-run">{runSteps}</pre>
              <CopyButton text={runSteps} tip="Copy" />
            </div>
            <p class="xs muted">The last resort when the agent is not allowed: plain <code class="inline">docker compose</code>, Caddy as the edge and Portainer as the GUI (https://&lt;machine&gt;:9443), no OPM code on the machine. Containers, files, starter volume content, health-ordered start and the routes come up like on the agent. Each site's <code class="inline">sites/&lt;site&gt;/README.md</code> has the exact steps (Docker install, updates, roll back, backups).</p>
          </div>
          <div>
            <div class="strong">Not equivalent without the agent</div>
            <p class="xs muted">drift detection, automatic snapshots + rollback, persist to cloud, approvals, the agent GUI (/_opm), dev tools, transfers, auto host detection (→ site.env). Git is the history and Portainer the GUI. Each branch's README lists the details; the Notes column shows them per branch.</p>
          </div>
        </div>
      </aside>
    </div>

    <section class="card section" data-testid="gh-branches-card">
      <div class="card-head wrapc">
        <h3>Branches</h3>
        <span class="row wrap small muted sum" data-testid="gh-summary">
          <span>{v.summary.branches} branches</span>
          <span class="badge green">{v.summary.synced} synced</span>
          {#if v.summary.queued}<span class="badge blue">{v.summary.queued} queued</span>{/if}
          {#if v.summary.errors}<span class="badge red">{v.summary.errors} error{v.summary.errors === 1 ? '' : 's'}</span>{/if}
          {#if v.summary.lastPushAt}<span>last push <RelTime ts={v.summary.lastPushAt} /></span>{/if}
        </span>
        <span class="spacer"></span>
        <SegmentedControl options={[{ value: 'all', label: 'All' }, { value: 'template', label: 'Templates' }, { value: 'deployment', label: 'Deployments' }, { value: 'agent', label: 'Agents' }, { value: 'problems', label: 'Problems' }] as { value: KindFilter; label: string }[]} bind:value={kind} ariaLabel="Branch kind" testid="gh-kind" />
        <SearchInput bind:value={q} placeholder="Search branches" />
      </div>
      {#if rows.loading && !rows.data}
        <TableSkeleton cols={7} />
      {:else if rows.error}
        <div class="card-body"><ErrorState message={rows.error} onretry={() => rows.load()} /></div>
      {:else if !rows.data?.length}
        <div class="card-body"><EmptyState compact icon={GitBranch} title="No branches yet" text="Create a template or a deployment; its branch appears here." /></div>
      {:else if !shown.length}
        <div class="card-body"><EmptyState compact title="No match" text="No branch matches the filter." /></div>
      {:else}
        <div class="table-wrap">
          <table class="table" data-testid="gh-table">
            <thead><tr><th>Git branch</th><th>OPM branch</th><th>Last pushed</th><th>Git sha</th><th>State</th><th>Last error</th><th>Notes</th><th class="actions" aria-label="Actions"></th></tr></thead>
            <tbody>
              {#each shown as r (r.gitBranch)}
                <tr data-testid="gh-row" data-branch={r.gitBranch}>
                  <td class="br">
                    {#if r.treeUrl}<a class="mono small" href={r.treeUrl} target="_blank" rel="noopener noreferrer" data-testid="gh-tree-link">{r.gitBranch}</a>{:else}<span class="mono small">{r.gitBranch}</span>{/if}
                  </td>
                  <td><span class="badge {KIND_TONE[r.branch.kind]}">{KIND_LABEL[r.branch.kind]}</span> <span class="small lab">{r.label}</span></td>
                  <td class="last">
                    {#if r.last}
                      <div class="row" style="gap:6px;min-width:0">{#if r.last.seq !== null}<span class="badge outline mono">{r.last.seq}</span>{/if}<span class="small ellipsis" title={r.last.message}>{r.last.message.split('\n')[0]}</span></div>
                      <div class="xs faint"><RelTime ts={r.last.at} /></div>
                    {:else}<span class="faint small">—</span>{/if}
                  </td>
                  <td>{#if r.gitSha}{#if r.gitUrl}<a class="mono small" href={r.gitUrl} target="_blank" rel="noopener noreferrer" data-testid="gh-sha-link">{short(r.gitSha)}</a>{:else}<span class="mono small">{short(r.gitSha)}</span>{/if}{:else}<span class="faint small">—</span>{/if}</td>
                  <td>
                    <span class="row nowrap" style="gap:4px">
                      <span class="badge {STATUS[r.status].tone}" data-testid="gh-status"><span class="dot"></span>{STATUS[r.status].label}</span>
                      {#if r.queue.pending}<span class="xs muted" data-testid="gh-pending">{r.queue.pending} pending</span>{/if}
                    </span>
                  </td>
                  <td class="err">
                    {#if r.lastError}
                      <Popover title="Last error · {r.gitBranch}" tone="red" testid="gh-error-{r.gitBranch}">
                        {#snippet label()}<span class="ellipsis errtxt">{r.lastError!.status ? `${r.lastError!.status} · ` : ''}{r.lastError!.message}</span>{/snippet}
                        <p>{r.lastError.message}</p>
                        <p class="xs muted">at <RelTime ts={r.lastError.at} />{#if r.lastError.status} · HTTP {r.lastError.status}{/if}{#if r.queue.nextAttemptAt} · next try <RelTime ts={r.queue.nextAttemptAt} />{/if}</p>
                      </Popover>
                    {:else}<span class="faint small">—</span>{/if}
                  </td>
                  <td>
                    {#if r.notes.length}
                      <Popover title="Not equivalent without the agent" tone="outline" testid="gh-notes-{r.gitBranch}" minWidth={340}>
                        {#snippet label()}{r.notes.length} note{r.notes.length === 1 ? '' : 's'}{/snippet}
                        <ul class="notes">
                          {#each r.notes as n, i (i)}<li><code class="inline">{n.code}</code>{#if n.site} <span class="chip mono">{n.site}</span>{/if} {n.message}</li>{/each}
                        </ul>
                      </Popover>
                    {:else}<span class="faint small">—</span>{/if}
                  </td>
                  <td class="actions">
                    <Button size="sm" variant="ghost" icon={RefreshCw} loading={syncing === r.gitBranch} disabled={!v.enabled} tip="Sync this branch" aria-label="Sync {r.gitBranch}" onclick={() => sync(r.gitBranch)} data-testid="gh-row-sync" />
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </section>
  {/if}
</div>

<Modal bind:open={tokOpen} title={mirror.data?.tokenSet ? 'Replace the token' : 'Add a token'} subtitle="Stored by the cloud like a registry password: masked everywhere, View shows it for 60 s (logged)." busy={tokSaving} testid="gh-token-modal">
  <form id="gh-tok-form" class="stack" onsubmit={saveToken} novalidate>
    <ol class="small steps">
      <li>Open <a href="https://github.com/settings/personal-access-tokens/new" target="_blank" rel="noopener noreferrer">github.com/settings/personal-access-tokens/new <Icon icon={ExternalLink} size={12} /></a>.</li>
      <li>Resource owner = <b class="mono">{mirror.data?.owner || '<owner>'}</b>; Repository access = <b>Only select repositories</b> → <b class="mono">{mirror.data?.repo || '<repo>'}</b>.</li>
      <li>Repository permissions → <b>Contents: Read and write</b>. Pick an expiry you will remember to renew.</li>
    </ol>
    <Field label="Token" id="gh-tok" required error={tokTouched && tokLocal ? [tokLocal] : tokServer ? [tokServer] : []}>
      <input id="gh-tok" class="input mono" type="password" bind:value={tokValue} autocomplete="off" spellcheck="false" maxlength="255" placeholder="github_pat_…" data-testid="gh-token-input" />
    </Field>
  </form>
  {#snippet footer()}
    <Button onclick={() => (tokOpen = false)} disabled={tokSaving}>Cancel</Button>
    <Button type="submit" form="gh-tok-form" variant="primary" loading={tokSaving} disabled={tokTouched && !!tokLocal} data-testid="gh-token-save">Save token</Button>
  {/snippet}
</Modal>

<ConfirmDialog bind:open={rmOpen} title="Remove the token?" message="Mirroring stops at once (nothing is pushed). The branches already on GitHub stay. Add a token later to continue where it stopped." confirmLabel="Remove token" danger onconfirm={removeToken} testid="gh-token-remove-dialog" />

<ConfirmDialog bind:open={resyncOpen} title="Resync everything?" message="Every branch is rebuilt from OPM history (commit 1 … head) and FORCE-pushed. History that did not change keeps its git shas (the export is deterministic), so normally nothing visible changes; anything pushed to these branches outside OPM is overwritten. Use Sync now for a normal catch-up." confirmLabel="Rebuild and force-push" danger onconfirm={resync} testid="gh-resync-dialog" />

<style>
  .grid {
    display: grid;
    grid-template-columns: minmax(0, 1.4fr) minmax(0, 1fr);
    gap: var(--sp-4);
    align-items: start;
  }
  @media (max-width: 980px) {
    .grid {
      grid-template-columns: minmax(0, 1fr);
    }
  }
  .section {
    margin-top: var(--sp-4);
  }
  .setup {
    margin-bottom: var(--sp-4);
  }
  .banner {
    margin-bottom: var(--sp-3);
  }
  .steps {
    margin: 0;
    padding-left: 20px;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .card-body.steps {
    padding-left: 34px;
  }
  .checks {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .checks li {
    display: grid;
    grid-template-columns: 16px 110px minmax(0, 1fr);
    align-items: start;
    gap: 6px;
    color: var(--green-text, var(--text));
  }
  .checks li :global(svg) {
    color: var(--green, #16a34a);
    margin-top: 2px;
  }
  .checks li.bad :global(svg) {
    color: var(--red, #dc2626);
  }
  .checks .nm {
    color: var(--text-2);
    padding-top: 1px;
  }
  .checks .msg {
    color: var(--text);
    overflow-wrap: anywhere;
  }
  .help .branches {
    margin: 0;
    padding-left: 18px;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .help code {
    overflow-wrap: anywhere;
  }
  .codebox {
    position: relative;
    margin-top: 6px;
  }
  .codebox :global(.btn) {
    position: absolute;
    top: 4px;
    right: 4px;
  }
  .codebox pre {
    margin: 0;
    padding-right: 36px;
  }
  .wrapc {
    flex-wrap: wrap;
    gap: 8px;
  }
  .sum {
    gap: 6px;
  }
  td.br {
    white-space: nowrap;
  }
  td.last {
    max-width: 220px;
  }
  td.err {
    max-width: 240px;
  }
  .errtxt {
    display: inline-block;
    max-width: 210px;
    vertical-align: bottom;
  }
  .lab {
    display: block;
    max-width: 200px;
  }
  .notes {
    margin: 0;
    padding-left: 16px;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .tokrow {
    min-width: 0;
  }
  .nowrap {
    flex-wrap: nowrap;
  }
</style>
