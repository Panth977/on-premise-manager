<script lang="ts">
  /**
   * DEPLOYMENT › Branch (§21 #104; replaces Diff + local proposals). This site's branch next to the cloud's:
   *   Overview     — cloud head (what the deployment wants), this site (head, merged from, ahead / behind / differs),
   *                  changes made on the server (drift + notes), the changes held in this browser (§21 #105: not
   *                  committed until Commit on a config page), Merge from cloud, Fast-forward all sites, the run.
   *   Uncommitted  — changes made on the server outside the agent (drift) with Commit… / Discard, drift notes
   *                  (Discard / Accept / Take the bytes). GUI edits are never here: they live in the browser.
   *   History      — the shared HistoryView (open, compare two, revert / reset → applied by the agent).
   * Nothing changes on the server until a person here commits or merges.
   * §21 #111 the site of the URL: another site than this agent → that site's branch (read, commit its server changes,
   * merge, revert / reset) over the mesh; browser edits and Fast-forward all stay on this agent.
   */
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { appHref } from '$lib/nav';
  import { api, errMessage } from '$lib/api';
  import { poll, Resource } from '$lib/state/resource.svelte';
  import { branch, uncommitted, refreshBranch } from '$lib/state/branch.svelte';
  import { overview, headConfig } from '$lib/state/app.svelte';
  import { draft } from '$lib/state/draft.svelte';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toast.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import { provideAgentBranchHost } from '$lib/branch/branchHost';
  import { siteBranchApi, selfSite } from '$lib/branch/siteBranch';
  import { commitSummary, type DriftItem, type DriftNote, type BranchRef } from '@opm/shared/branch';
  import type { UpdateRun } from '@opm/shared/types';
  import CommitDialog from '$opm/branch/CommitDialog.svelte';
  import HistoryView from '$opm/branch/HistoryView.svelte';
  import ChangeCount from '$opm/branch/ChangeCount.svelte';
  import PageHead from '$lib/ui/PageHead.svelte';
  import PageTabs, { tabOf } from '$lib/ui/PageTabs.svelte';
  import Load from '$lib/ui/Load.svelte';
  import Empty from '$lib/ui/Empty.svelte';
  import Time from '$lib/ui/Time.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Confirm from '$lib/ui/Confirm.svelte';
  import CopyId from '$lib/ui/CopyId.svelte';
  import UpdateProgress from '$lib/ui/UpdateProgress.svelte';
  import CloudNextStep from '$lib/ui/CloudNextStep.svelte';
  import { stagingTone } from '$lib/ui/tones';

  const site = $derived(page.params.siteSlug ?? '*');
  const TABS = ['overview', 'uncommitted', 'history'] as const;
  const tab = $derived(tabOf(page.url, TABS));
  /** §21 #111 the branch API of the page's site (null remote = this agent) */
  const sb = $derived(siteBranchApi(site));
  const remote = $derived(sb.remote);
  const self = $derived(selfSite());
  // this agent: the shared resources (sidebar badge, change bar); another site: its own, read over the mesh
  const remoteBranch = new Resource(() => siteBranchApi(page.params.siteSlug).state());
  const remoteUncommitted = new Resource(() => siteBranchApi(page.params.siteSlug).uncommitted());
  const br = $derived(remote ? remoteBranch : branch);
  const unc = $derived(remote ? remoteUncommitted : uncommitted);
  $effect(() => {
    void remote;
    remoteBranch.reset();
    remoteUncommitted.reset();
  });
  function reloadBranch() {
    refreshBranch();
    if (remote) {
      remoteBranch.load();
      remoteUncommitted.load();
    }
  }
  // §21 #113 what happens with changes made in Docker outside OPM on this page's site
  const policy = new Resource(() => siteBranchApi(page.params.siteSlug).driftPolicy());
  $effect(() => {
    void site;
    policy.load();
  });
  let policyBusy = $state(false);
  async function setPolicy(mode: 'ask' | 'auto-commit') {
    policyBusy = true;
    try {
      policy.data = await sb.setDriftPolicy({ mode });
      toasts.ok(mode === 'auto-commit' ? 'Docker changes are committed automatically' : 'Docker changes wait for a person', mode === 'auto-commit' ? 'Author UNKNOWN (changed in Docker); revert them from History.' : 'They show here as uncommitted.');
    } catch (e) {
      toasts.err(e, 'Could not change the policy');
    } finally {
      policyBusy = false;
    }
  }
  const current = new Resource(() => siteBranchApi(page.params.siteSlug).currentUpdate());
  let started = $state<UpdateRun | null>(null);
  let dismissed = $state<string[]>([]);
  let historyVersion = $state(0);
  provideAgentBranchHost(
    (r) => {
      if (r && 'updateId' in r) onRun(r as UpdateRun);
      if (remote) reloadBranch();
      historyVersion++;
    },
    () => page.params.siteSlug,
  );

  $effect(() => {
    const r = br;
    return poll(() => r.load(), 4000);
  });
  $effect(() => {
    void site;
    current.reset();
    started = null;
    return poll(() => current.load(), 3000);
  });
  $effect(() => {
    const r = unc;
    if (tab === 'uncommitted' || tab === 'overview') return poll(() => r.load(), 6000);
  });

  const b = $derived(br.data);
  const u = $derived(b?.uncommitted ?? { working: 0, drift: 0, notes: 0, scannedAt: null });
  const total = $derived(u.drift + u.notes);
  const run = $derived.by(() => {
    const r = current.data?.run ?? started;
    return r && !dismissed.includes(r.updateId) ? r : null;
  });
  const LOCAL = $derived<BranchRef>({ kind: 'agent', deploymentId: overview.data?.deploymentId ?? session.info?.deploymentId ?? '', siteSlug: b?.siteSlug ?? remote ?? overview.data?.self ?? '' });
  /** "this site" on this agent, "site X" for another one */
  const siteName = $derived(remote ? `site ${remote}` : 'this site');
  const mergeBlocked = $derived(!b?.cloud.head ? 'The cloud has no commit yet' : total ? `Commit or discard the ${total} ${total === 1 ? 'change' : 'changes'} made on the server first` : b.local.behind === 0 && b.local.differs === 0 ? 'This site is up to date with the cloud' : null);
  /** §21 #110 a running update never blocks a merge: starting one stops it */
  const updateRunning = $derived(!!overview.data?.lock || (!!run && !run.finishedAt));
  const ffSites = $derived((b?.sites ?? []).filter((s) => s.canFastForward));
  const behindSites = $derived((b?.sites ?? []).filter((s) => s.mergedFrom && b?.cloud.head && s.mergedFrom.seq < b.cloud.head.seq));

  // a run that finished: the commit exists now (or the merge failed) → reload the branch + history once
  let finishedSeen: string | null = null;
  $effect(() => {
    const r = run;
    if (r?.finishedAt && finishedSeen !== r.updateId) {
      finishedSeen = r.updateId;
      reloadBranch();
      headConfig.load();
      historyVersion++;
    }
  });
  function onRun(r: UpdateRun) {
    started = r;
    current.load();
    overview.load();
    reloadBranch();
    window.scrollTo({ top: 0, behavior: 'smooth' });
  }

  // ── uncommitted: selection + commit + discard
  let pick = $state<Record<string, boolean>>({});
  let accept = $state<Record<string, boolean>>({});
  let takeBytes = $state<Record<string, boolean>>({});
  const items = $derived<DriftItem[]>(unc.data?.items ?? []);
  const notes = $derived<DriftNote[]>(unc.data?.notes ?? []);
  const picked = $derived(items.filter((i) => pick[i.key] !== false));
  let commitOpen = $state(false);
  let commitBusy = $state(false);
  let rescanBusy = $state(false);
  let discardOne = $state<{ keys: string[]; notes: string[]; label: string } | null>(null);
  let discardOpen = $state(false);
  const ORIGIN: Record<string, { label: string; tone: string; icon: string }> = {
    docker: { label: 'changed on the server · Docker', tone: 'amber', icon: 'container' },
    file: { label: 'changed on the server · file', tone: 'amber', icon: 'file' },
    'secret-file': { label: 'changed on the server · secret file', tone: 'amber', icon: 'key' },
    'volume-content': { label: 'changed on the server · volume content', tone: 'amber', icon: 'hardDrive' },
  };
  const effectText = (i: DriftItem) => (i.effect === 'add' ? 'added' : i.effect === 'remove' ? 'removed' : 'changed');
  const hasEdits = $derived(Object.values(takeBytes).some(Boolean));

  async function rescan() {
    rescanBusy = true;
    try {
      const r = await sb.rescan();
      toasts.ok(remote ? `${remote} scanned` : 'Server scanned', `${r.items.length} server ${r.items.length === 1 ? 'change' : 'changes'}, ${r.notes.length} ${r.notes.length === 1 ? 'note' : 'notes'}`);
      reloadBranch();
    } catch (e) {
      toasts.err(e, 'Scan failed');
    } finally {
      rescanBusy = false;
    }
  }
  async function openCommit() {
    await unc.load();
    commitOpen = true;
  }
  async function commit(message: string) {
    commitBusy = true;
    try {
      const keys = picked.map((i) => i.key);
      const r = await sb.commitDrift({ message, keys, accept: notes.filter((n) => accept[n.fingerprint]).map((n) => n.fingerprint), takeBytes: notes.filter((n) => takeBytes[n.fingerprint]).map((n) => n.fingerprint) });
      commitOpen = false;
      pick = {};
      accept = {};
      takeBytes = {};
      reloadBranch();
      headConfig.load();
      historyVersion++;
      if (r.run) {
        onRun(r.run);
        toasts.info('Applying your commit', 'The commit is written when the apply succeeds.');
      } else toasts.ok(`Committed seq ${r.commit?.seq ?? ''}`, `Recorded on ${remote ? `${remote}’s` : 'this site’s'} branch (nothing restarted).`);
    } catch (e) {
      throw new Error(errMessage(e));
    } finally {
      commitBusy = false;
    }
  }
  async function discard() {
    if (!discardOne) return;
    try {
      const r = await sb.discard({ keys: discardOne.keys, notes: discardOne.notes });
      toasts.ok('Discarded', `The server state is set back to ${remote ? `${remote}’s` : 'this site’s'} branch.`);
      if (r.run) onRun(r.run);
      reloadBranch();
      headConfig.load();
    } catch (e) {
      toasts.err(e, 'Discard failed');
    }
  }

  // ── fast-forward all
  let ffPick = $state<Record<string, boolean>>({});
  let ffOpen = $state(false);
  let ffBusy = $state(false);
  const ffChosen = $derived(ffSites.filter((s) => ffPick[s.siteSlug] !== false).map((s) => s.siteSlug));
  async function fastForward(message: string) {
    if (!b?.cloud.head) return;
    ffBusy = true;
    try {
      const r = await api.fastForwardAll({ body: { sourceCommitId: b.cloud.head.commitId, sites: ffChosen, message } });
      ffOpen = false;
      onRun(r);
      toasts.info('Fast-forward started', `${ffChosen.join(', ')} → cloud seq ${b.cloud.head.seq}`);
    } catch (e) {
      throw new Error(errMessage(e));
    } finally {
      ffBusy = false;
    }
  }
</script>

<PageHead icon="branch" eyebrow="Server · site {b?.siteSlug ?? site}" title="Branch">
  {#snippet actions()}
    <Btn icon="refresh" busy={rescanBusy} onclick={rescan} data-testid="rescan">Rescan server</Btn>
    {#if b?.cloud.head}<a class="btn primary" class:disabled={!!mergeBlocked} aria-disabled={!!mergeBlocked} title={mergeBlocked ?? undefined} href={mergeBlocked ? undefined : appHref(site, `branch/merge?source=${b.cloud.head.commitId}`)} data-testid="merge-from-cloud"><Icon name="branch" size={14} />Merge from cloud</a>{/if}
  {/snippet}
</PageHead>


{#if run}
  <div style="margin-bottom:18px" data-testid="branch-run">
    <UpdateProgress
      {run}
      site={remote}
      ondismiss={() => {
        dismissed = [...dismissed, run!.updateId];
        started = null;
        reloadBranch();
        historyVersion++;
      }}
    />
  </div>
{/if}

<PageTabs label="Branch" testid="branch-tabs" tabs={[{ k: 'overview', label: 'Overview', icon: 'branch' }, { k: 'uncommitted', label: total ? `Uncommitted (${total})` : 'Uncommitted', icon: 'edit' }, { k: 'history', label: 'History', icon: 'history' }]} />

<Load loading={br.loading} loaded={br.loaded} error={br.error} retry={() => br.load()} rows={6}>
  {#if b}
    {#if tab === 'overview'}
      <div class="grid two">
        <section class="card card-pad" data-testid="cloud-card">
          <div class="row between">
            <div class="section-title">Cloud · deployment branch</div>
            {#if b.local.behind}<span class="badge accent" data-testid="behind">{b.local.behind} to merge</span>{:else}<span class="badge ok">merged</span>{/if}
          </div>
          {#if b.cloud.head}
            {@const h = b.cloud.head}
            <h2 class="hs">seq {h.seq} · {commitSummary(h.message)}</h2>
            <div class="muted small">{h.author.name ?? h.author.email} · <Time ts={h.createdAt} /> · <CopyId value={h.commitId} short={10} label="commit id" /></div>
            <div class="staged">
              {#each Object.entries(b.cloud.staged) as [s, st] (s)}
                <span class="badge {stagingTone[st.staging]}" title={st.error ?? undefined} data-testid="staged-{s}">{s}: {st.staging === 'ready' ? 'staged ✓' : st.staging === 'downloading' ? `downloading ${Math.round(st.progress * 100)} %` : st.staging}</span>
              {/each}
            </div>
            {#if b.cloud.recent.length > 1}
              <ol class="recent">
                {#each b.cloud.recent.slice(0, 5) as c (c.commitId)}
                  <li class:merged={!!b.local.mergedFrom && c.seq <= b.local.mergedFrom.seq}><span class="mono">{c.seq}</span><span class="grow ellipsis">{commitSummary(c.message)}</span><span class="muted tiny"><Time ts={c.createdAt} /></span></li>
                {/each}
              </ol>
            {/if}
          {:else}
            <Empty compact icon="branch" title="No cloud commit yet" text="The deployment has not been committed in the cloud console.">{#snippet detail()}<CloudNextStep />{/snippet}</Empty>
          {/if}
        </section>

        <section class="card card-pad" data-testid="local-card">
          <div class="row between">
            <div class="section-title">{remote ? 'Site' : 'This site'} · {b.siteSlug}</div>
            {#if b.local.ahead}<span class="badge blue" data-testid="ahead">{b.local.ahead} local {b.local.ahead === 1 ? 'commit' : 'commits'}</span>{/if}
          </div>
          {#if b.local.head}
            {@const h = b.local.head}
            <h2 class="hs">seq {h.seq} · {commitSummary(h.message)}</h2>
            <div class="muted small">{h.author.email} · {h.kind} · <Time ts={h.createdAt} /></div>
          {:else}
            <p class="muted small">Nothing committed on {siteName} yet (fresh install).</p>
          {/if}
          <dl class="kv">
            <dt>Merged from cloud</dt><dd data-testid="merged-from">{b.local.mergedFrom ? `seq ${b.local.mergedFrom.seq}` : 'never'}</dd>
            <dt>Behind the cloud</dt><dd>{b.local.behind} {b.local.behind === 1 ? 'commit' : 'commits'}</dd>
            <dt>Ahead (local commits)</dt><dd>{b.local.ahead}</dd>
            <dt>Differs from the cloud head</dt><dd data-testid="differs">{b.local.differs} {b.local.differs === 1 ? 'item' : 'items'}</dd>
          </dl>
        </section>

        <section class="card card-pad" class:warncard={total > 0} data-testid="uncommitted-card">
          <div class="row between">
            <div class="section-title">Changes made on the server</div>
            <ChangeCount count={total} />
          </div>
          <div class="nums">
            <div><strong data-testid="n-drift">{u.drift}</strong><span class="muted small">changed outside the agent</span></div>
            <div><strong data-testid="n-notes">{u.notes}</strong><span class="muted small">notes</span></div>
            <div><strong data-testid="n-edits">{draft.pendingCount}</strong><span class="muted small">edits in this browser</span></div>
          </div>
          <p class="muted tiny">last scan <Time ts={u.scannedAt} /></p>
          <div class="row" style="gap:6px;flex-wrap:wrap">
            <a class="btn sm" href={appHref(site, 'branch?tab=uncommitted')} data-testid="review-uncommitted"><Icon name="search" size={14} />Review</a>
            <Btn size="sm" variant="primary" icon="check" disabled={!total} onclick={openCommit} data-testid="commit-open">Commit…</Btn>
          </div>
        </section>

        <section class="card card-pad" data-testid="merge-card">
          <div class="section-title">Merge</div>
          {#if mergeBlocked}<div class="banner small" data-testid="merge-blocked"><Icon name="info" /><div class="grow">{mergeBlocked}</div></div>{:else if updateRunning}<div class="banner warn small" data-testid="merge-update-running"><Icon name="alert" /><div class="grow">An update is running — starting a merge stops it.</div></div>{/if}
          <div class="row" style="gap:6px;margin-top:8px;flex-wrap:wrap">
            {#if b.cloud.head}<a class="btn sm primary" class:disabled={!!mergeBlocked} aria-disabled={!!mergeBlocked} href={mergeBlocked ? undefined : appHref(site, `branch/merge?source=${b.cloud.head.commitId}`)} data-testid="merge-open">Merge from cloud…</a>{/if}
            {#if behindSites.length > 1 && !remote}<Btn size="sm" icon="fastForward" disabled={!ffSites.length} title={ffSites.length ? undefined : 'No site can fast-forward now'} onclick={() => (ffOpen = true)} data-testid="ff-open">Fast-forward all sites…</Btn>{/if}
          </div>
          {#if b.sites.length > 1}
            <table class="table ff" data-testid="ff-sites">
              <thead><tr><th></th><th>Site</th><th>Merged</th><th>State</th></tr></thead>
              <tbody>
                {#each b.sites as s (s.siteSlug)}
                  <tr class:dim={!s.canFastForward}>
                    <td><input type="checkbox" class="cb" disabled={!s.canFastForward || !!remote} checked={s.canFastForward && ffPick[s.siteSlug] !== false} onchange={(e) => (ffPick = { ...ffPick, [s.siteSlug]: (e.currentTarget as HTMLInputElement).checked })} aria-label="Fast-forward {s.siteSlug}" data-testid="ff-pick-{s.siteSlug}" /></td>
                    <td class="mono"><a href={appHref(s.siteSlug, 'branch')} aria-current={s.siteSlug === b.siteSlug ? 'page' : undefined} data-testid="site-branch-{s.siteSlug}">{s.siteSlug}</a>{#if s.siteSlug === self}<span class="muted tiny"> · this agent</span>{/if}</td>
                    <td class="small">{s.mergedFrom ? `seq ${s.mergedFrom.seq}` : '—'}</td>
                    <td class="small">{s.canFastForward ? 'can fast-forward' : (s.reason ?? '—')}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          {/if}
        </section>
      </div>
    {:else if tab === 'uncommitted'}
      <section class="card" data-testid="uncommitted-list">
        <div class="card-head">
          <h2>Uncommitted changes <ChangeCount count={items.length + notes.length} /></h2>
          <div class="row" style="gap:6px">
            <span class="muted small hide-sm" title="Docker is the source of truth: what changes there outside OPM shows here">When Docker changes outside OPM</span>
            <SegmentedControl size="sm" options={[{ value: 'ask', label: 'Ask', title: 'Show it here as uncommitted (commit or discard)' }, { value: 'auto-commit', label: 'Auto-commit', title: 'The agent commits it itself once stable (author UNKNOWN); revert from History' }]} value={policy.data?.mode ?? 'ask'} disabled={policyBusy || !policy.data} onchange={(v) => setPolicy(v)} label="When Docker changes outside OPM" testid="drift-policy" />
            <span class="muted small hide-sm">last scan <Time ts={unc.data?.scannedAt ?? null} /></span>
            <Btn size="sm" variant="primary" icon="check" disabled={!picked.length && !notes.some((n) => accept[n.fingerprint] || takeBytes[n.fingerprint])} onclick={openCommit} data-testid="commit-selected">Commit selected…</Btn>
          </div>
        </div>
        {#if !items.length && !notes.length}
          <div class="card-body"><Empty compact icon="check" title="Nothing uncommitted" text="The server runs exactly its branch." /></div>
        {:else}
          <div class="table-wrap">
            <table class="table">
              <thead><tr><th style="width:28px"></th><th>Change</th><th>Where from</th><th class="hide-sm">Detail</th><th></th></tr></thead>
              <tbody>
                {#each items as i (i.key)}
                  {@const o = ORIGIN[i.origin] ?? ORIGIN.docker}
                  <tr data-testid="u-row-{i.key}" data-origin={i.origin}>
                    <td><input type="checkbox" class="cb" checked={pick[i.key] !== false} onchange={(e) => (pick = { ...pick, [i.key]: (e.currentTarget as HTMLInputElement).checked })} aria-label="Commit {i.label}" data-testid="u-pick-{i.key}" /></td>
                    <td><div class="strong">{i.label}</div><div class="mono tiny muted">{i.key} · {effectText(i)}</div></td>
                    <td><span class="badge {o.tone}"><Icon name={o.icon} size={12} />{o.label}</span></td>
                    <td class="small hide-sm">{i.detail}</td>
                    <td><Btn size="sm" variant="ghost" onclick={() => ((discardOne = { keys: [i.key], notes: [], label: i.label }), (discardOpen = true))} data-testid="u-discard-{i.key}">Discard</Btn></td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
          {#if notes.length}
            <div class="card-body">
              <div class="section-title" style="margin-bottom:8px">Notes — server changes that cannot be written as config</div>
              {#each notes as n (n.fingerprint)}
                <div class="note" data-testid="note-{n.fingerprint}">
                  <div class="grow"><strong>{n.label}</strong><p class="small muted" style="margin:2px 0 0">{n.detail}</p></div>
                  <div class="acts">
                    {#if n.actions.includes('accept')}<label class="small"><input type="checkbox" class="cb" bind:checked={() => !!accept[n.fingerprint], (v) => (accept = { ...accept, [n.fingerprint]: v })} data-testid="note-accept-{n.fingerprint}" />Accept with the commit</label>{/if}
                    {#if n.actions.includes('take-bytes')}<label class="small"><input type="checkbox" class="cb" bind:checked={() => !!takeBytes[n.fingerprint], (v) => (takeBytes = { ...takeBytes, [n.fingerprint]: v })} data-testid="note-take-{n.fingerprint}" />Take the edited bytes</label>{/if}
                    {#if n.actions.includes('discard')}<Btn size="sm" variant="ghost" onclick={() => ((discardOne = { keys: [], notes: [n.fingerprint], label: n.label }), (discardOpen = true))} data-testid="note-discard-{n.fingerprint}">Discard</Btn>{/if}
                  </div>
                </div>
              {/each}
            </div>
          {/if}
        {/if}
      </section>
    {:else}
      <HistoryView branch={LOCAL} label={remote ? `site ${b.siteSlug}` : `this site (${b.siteSlug})`} version={historyVersion} onchanged={(r) => r && 'updateId' in r && onRun(r as UpdateRun)} />
    {/if}
  {/if}
</Load>

<CommitDialog
  bind:open={commitOpen}
  title="Commit the server changes"
  primaryLabel={hasEdits ? 'Commit & apply' : 'Commit'}
  items={picked}
  warnings={[...(notes.some((n) => accept[n.fingerprint]) ? [`${notes.filter((n) => accept[n.fingerprint]).length} note(s) accepted with this commit`] : []), ...(picked.length < items.length ? [`${items.length - picked.length} change(s) stay uncommitted — a merge needs all of them committed or discarded`] : [])]}
  intro={hasEdits ? 'The edited bytes become the File content and are applied now (snapshot → apply → verify; rollback on failure); server changes are recorded as they are.' : 'Server changes are recorded as they are — nothing restarts.'}
  author={session.actor?.email}
  busy={commitBusy}
  onsubmit={commit}
  testid="commit-dialog"
/>

<CommitDialog
  bind:open={ffOpen}
  title="Fast-forward {ffChosen.length} {ffChosen.length === 1 ? 'site' : 'sites'} to cloud seq {b?.cloud.head?.seq ?? ''}"
  primaryLabel="Fast-forward"
  intro="Each selected site takes every change of the cloud commit (they have no local changes). One coordinated update: lock → snapshot → apply → verify on every site; any failure rolls back every site. Sites: {ffChosen.join(', ')}"
  author={session.actor?.email}
  busy={ffBusy}
  onsubmit={fastForward}
  testid="ff-dialog"
/>

<Confirm bind:open={discardOpen} title="Discard {discardOne?.label ?? ''}?" danger confirmLabel="Discard" message="The agent sets the server back to {remote ? `${remote}’s` : 'this site’s'} branch for this item (a container is recreated, a file rewritten). What was changed by hand is lost." onconfirm={discard} testid="discard-confirm" />

<style>
  .two {
    grid-template-columns: repeat(2, minmax(0, 1fr));
    align-items: start;
  }
  @media (max-width: 1000px) {
    .two {
      grid-template-columns: minmax(0, 1fr);
    }
  }
  .hs {
    margin: 6px 0 2px;
    font-size: var(--fs-lg);
    overflow-wrap: anywhere;
  }
  .staged {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    margin-top: 10px;
  }
  .recent {
    list-style: none;
    margin: 12px 0 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .recent li {
    display: flex;
    gap: 8px;
    align-items: baseline;
    font-size: var(--fs-sm);
  }
  .recent li.merged {
    color: var(--text-3);
  }
  .ellipsis {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
  }
  .nums {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 8px;
    margin: 10px 0 6px;
  }
  .nums div {
    display: flex;
    flex-direction: column;
  }
  .nums strong {
    font-size: 22px;
  }
  .warncard {
    border-color: color-mix(in srgb, var(--amber) 45%, var(--border));
  }
  .note {
    display: flex;
    gap: 12px;
    align-items: flex-start;
    padding: 10px 0;
    border-top: 1px solid var(--border);
    flex-wrap: wrap;
  }
  .note .acts {
    display: flex;
    flex-direction: column;
    gap: 4px;
    align-items: flex-start;
  }
  .note label {
    display: flex;
    gap: 6px;
    align-items: center;
  }
  .ff {
    margin-top: 12px;
  }
  .strong {
    font-weight: 600;
  }
  a.disabled {
    opacity: 0.5;
    pointer-events: none;
  }
</style>
