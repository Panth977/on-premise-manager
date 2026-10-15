<script lang="ts">
  /**
   * §21 #105 the change bar of every config page of this site (Variables, Secrets, Files, Config, container / route
   * editors): "N changes (not committed)" · Discard · Review · Commit…. The changes live in this browser (draft store +
   * localStorage) until Commit: `POST /_opm/api/branch/commit { message, baseCommitId, config }` → the agent applies them
   * (snapshot → apply → verify, rollback on failure) and writes the commit only when that succeeds. There is no Save.
   *   409 uncommitted-drift → "N changes made on the server are not committed — commit them first" (Commit server
   *     changes… records them as a drift commit, then the browser changes are rebased onto it and the dialog reopens);
   *   409 stale → the browser changes are rebased onto the new head ("review and commit again").
   * §21 #107 one Commit = one transaction incl. the bytes: new File content (kept in this browser) is staged first
   * (POST /_opm/api/stages → only the blobs the agent is missing → PUT …/blobs/{sha}), then the commit names the stage;
   * the agent promotes + applies. Any failure drops the stage; every change (bytes included) stays here.
   */
  import ChangeBar from '$opm/primitives/ChangeBar.svelte';
  import CommitDialog from '$opm/branch/CommitDialog.svelte';
  import { goto } from '$app/navigation';
  import { appHref, siteHref } from '$lib/nav';
  import { page } from '$app/state';
  import { api, apiPath, errMessage, isApiError, uploadRaw } from '../api';
  import { commitWithStage, type StageTransport } from '$opm/branch/stage-commit';
  import { newConfigBlobs, totalBytes, type StageBlobResponse } from '@opm/shared/staging';
  import { viewToInput } from '../state/draft.svelte';
  import { draft } from '../state/draft.svelte';
  import { overview } from '../state/app.svelte';
  import { branch, uncommitted, refreshBranch } from '../state/branch.svelte';
  import { toasts } from '../state/toast.svelte';
  import { session } from '../state/session.svelte';
  import Btn from './Btn.svelte';
  import Drawer from './Drawer.svelte';
  import Confirm from './Confirm.svelte';
  import DiffTables from './DiffTables.svelte';
  import PendingFileDiffs from './PendingFileDiffs.svelte';
  import Issues from './Issues.svelte';
  import Icon from './Icon.svelte';

  let reviewing = $state(false);
  let discarding = $state(false);
  let committing = $state(false);
  let commitBusy = $state(false);
  let info = $state<string | null>(null);
  /** set by a 409 uncommitted-drift (or known from the branch state when the dialog opens) */
  let driftBlock = $state<{ drift: number; notes: number } | null>(null);
  let driftOpen = $state(false);
  let driftBusy = $state(false);
  let acceptNotes = $state(false);
  /** §21 #110 a running update never blocks a commit: committing starts an update, which stops the running one */
  const updateRunning = $derived(!!overview.data?.lock);
  const count = $derived(draft.pendingCount);
  const site = $derived(page.params.siteSlug ?? '*');
  /** §21 #107 progress of the commit transaction (CommitDialog) */
  let progress = $state<string | null>(null);
  /** new blobs this commit needs (vs the head) */
  const needs = $derived.by(() => {
    void draft.blobsTick;
    if (!draft.work || !draft.base) return [];
    return newConfigBlobs(viewToInput(draft.base.config), draft.work);
  });
  const uploadNote = $derived.by(() => {
    const pend = needs.filter((n) => draft.blobs.has(n.sha256));
    if (!pend.length) return null;
    const b = totalBytes(pend);
    return `${pend.length} new file content${pend.length === 1 ? '' : 's'} (${b < 1024 ? `${b} B` : b < 1048576 ? `${Math.round(b / 1024)} KB` : `${(b / 1048576).toFixed(1)} MB`}) kept in this browser ${pend.length === 1 ? 'is' : 'are'} uploaded with this commit — nothing reaches the server before.`;
  });
  const transport: StageTransport = {
    createStage: (blobs) => api.createLocalStage({ body: { blobs } }),
    putBlob: async (stageId, need, bytes, onBytes) => {
      await uploadRaw<StageBlobResponse>('putLocalStageBlob', apiPath('putLocalStageBlob', { stageId, sha256: need.sha256 }), new Blob([bytes as BlobPart]), (f) => onBytes?.(Math.round(f * bytes.length)));
    },
    deleteStage: async (stageId) => {
      await api.deleteLocalStage({ stageId });
    },
  };

  $effect(() => {
    if (!branch.loaded) branch.load();
  });

  function knownDrift(): { drift: number; notes: number } | null {
    const u = branch.data?.uncommitted;
    return u && u.drift + u.notes > 0 ? { drift: u.drift, notes: u.notes } : null;
  }
  async function openCommit() {
    info = null;
    await branch.load();
    driftBlock = knownDrift();
    committing = true;
  }
  async function commit(message: string) {
    if (!draft.work || !draft.base) return;
    commitBusy = true;
    try {
      const config = $state.snapshot(draft.work);
      const r = await commitWithStage({
        needs,
        bytesOf: (sha) => draft.bytesOf(sha),
        transport,
        onProgress: (_p, text) => (progress = text),
        commit: (stageId) => api.commitLocal({ body: { message, baseCommitId: draft.headId, config, ...(stageId ? { stageId } : {}) } }),
      });
      committing = false;
      const run = r.run;
      if (run) {
        draft.followRun(run.updateId, message, (ok, msg) => {
          refreshBranch();
          overview.load();
          if (ok) toasts.ok('Committed and applied', `“${message.split('\n')[0]}” runs on this site now.`);
          else toasts.err(`The apply failed and was rolled back${msg ? `: ${msg}` : ''}. Nothing was committed — your changes are still here.`, 'Not committed');
        });
        toasts.info('Applying your commit', 'Snapshot → apply → verify; the commit is written when it succeeds.');
        refreshBranch();
        await goto(siteHref('branch'));
      } else if (r.commit) {
        draft.discard();
        toasts.ok(`Committed seq ${r.commit.seq}`);
      }
    } catch (e) {
      if (isApiError(e) && e.code === 'conflict' && e.details?.reason === 'uncommitted-drift') {
        driftBlock = { drift: Number(e.details.drift ?? 0), notes: Number(e.details.notes ?? 0) };
        await branch.load();
        return; // the dialog stays open with the drift-first banner
      }
      if (isApiError(e) && e.code === 'stale') {
        const seq = await draft.moveToNewHead();
        if (seq === null) {
          committing = false;
          throw new Error('The head moved and the rebase was cancelled.');
        }
        info = `Someone committed meanwhile: your changes were rebased onto commit ${seq} — review and commit again.`;
        refreshBranch();
        return; // the dialog stays open on the rebased set
      }
      throw new Error(errMessage(e));
    } finally {
      commitBusy = false;
    }
  }
  /** drift first (J12): record every server change, then rebase the browser changes onto that commit */
  async function openDrift() {
    acceptNotes = false;
    await uncommitted.load();
    driftOpen = true;
  }
  async function commitDrift(message: string) {
    driftBusy = true;
    try {
      const items = (uncommitted.data?.items ?? []).filter((i) => i.origin !== 'edit');
      const notes = uncommitted.data?.notes ?? [];
      const r = await api.commitLocal({ body: { message, keys: items.map((i) => i.key), ...(acceptNotes && notes.length ? { accept: notes.map((n) => n.fingerprint) } : {}) } });
      driftOpen = false;
      refreshBranch();
      if (r.run) {
        toasts.info('Applying', 'Wait until the update ends, then commit your changes.');
        return;
      }
      const seq = await draft.moveToNewHead();
      await branch.load();
      driftBlock = knownDrift();
      info = seq === null ? null : `Server changes committed (seq ${r.commit?.seq ?? seq}); your changes were rebased onto it — review and commit.`;
      committing = true;
    } catch (e) {
      throw new Error(errMessage(e));
    } finally {
      driftBusy = false;
    }
  }
  const notesOnly = $derived(!!driftBlock && driftBlock.drift === 0 && driftBlock.notes > 0);
</script>

{#if draft.base?.head && draft.work}
  <ChangeBar
    {count}
    errors={draft.errorCount}
    note="{updateRunning ? 'an update is running — committing stops it · ' : ''}on head seq {draft.base.head.seq} · kept in this browser · nothing changes on the server until you commit"
    emptyText="No changes — everything is committed. Edits on Variables, Secrets, Files and Config stay in this browser until you commit them."
    onCommit={openCommit}
    blocked={draft.applying ? 'Your commit is being applied' : null}
    busy={!!draft.applying}
    ondiscard={() => (discarding = true)}
    onreview={() => (reviewing = true)}
    testid="local-edit-bar"
    reviewTestid="review-changes"
    discardTestid="discard-local"
  />
{/if}

<Drawer bind:open={reviewing} title="Review changes" subtitle="Your changes (kept in this browser) compared with what runs now (the head), checked with the same rules as the agent. Commit… applies them." wide testid="review-drawer">
  <div class="stack">
    <Issues issues={draft.issues} fix />
    <DiffTables changes={draft.changes} />
    <PendingFileDiffs />
  </div>
  {#snippet footer()}
    <Btn onclick={() => (reviewing = false)}>Close</Btn>
    <Btn variant="primary" icon="check" disabled={!draft.dirty || draft.errorCount > 0 || !!draft.applying} onclick={() => ((reviewing = false), openCommit())} data-testid="review-commit">Commit…</Btn>
  {/snippet}
</Drawer>

<CommitDialog
  bind:open={committing}
  title="Commit changes"
  primaryLabel="Commit & apply"
  items={draft.pendingItems}
  issues={draft.issues.filter((i) => i.severity === 'error')}
  intro="Your changes are applied on this server now (snapshot → apply → verify; rollback on failure) and committed when that succeeds. If the apply fails nothing is committed and the changes stay here."
  author={session.actor?.email}
  busy={commitBusy}
  {progress}
  {uploadNote}
  {info}
  blocked={driftBlock ? `${driftBlock.drift + driftBlock.notes} ${driftBlock.drift + driftBlock.notes === 1 ? 'change' : 'changes'} made on the server ${driftBlock.drift + driftBlock.notes === 1 ? 'is' : 'are'} not committed — commit ${driftBlock.drift + driftBlock.notes === 1 ? 'it' : 'them'} first.` : null}
  onsubmit={commit}
  testid="local-commit-dialog"
>
  {#snippet extra()}
    {#if updateRunning}
      <div class="banner warn small" data-testid="commit-update-running"><Icon name="alert" /><div class="grow">An update is running — starting this stops it (nothing is rolled back).</div></div>
    {/if}
    {#if driftBlock}
      <div class="banner warn small drift" data-testid="drift-first">
        <Icon name="alert" />
        <div class="grow">
          Changes made on this server outside the agent ({driftBlock.drift} {driftBlock.drift === 1 ? 'change' : 'changes'}{driftBlock.notes ? `, ${driftBlock.notes} ${driftBlock.notes === 1 ? 'note' : 'notes'}` : ''}) must be committed first, so the branch describes what runs. Your changes stay in this browser.
        </div>
        {#if notesOnly}
          <a class="btn sm" href={appHref(site, 'branch?tab=uncommitted')} onclick={() => (committing = false)} data-testid="drift-review">Decide the notes…</a>
        {:else}
          <Btn size="sm" variant="primary" onclick={openDrift} data-testid="drift-commit-open">Commit server changes…</Btn>
        {/if}
      </div>
    {/if}
  {/snippet}
</CommitDialog>

<CommitDialog
  bind:open={driftOpen}
  title="Commit the server changes"
  primaryLabel="Commit"
  items={(uncommitted.data?.items ?? []).filter((i) => i.origin !== 'edit')}
  intro="Records what was changed on this server outside the agent as it is (nothing restarts). Then your browser changes are rebased onto it."
  author={session.actor?.email}
  busy={driftBusy}
  blocked={!(uncommitted.data?.items ?? []).some((i) => i.origin !== 'edit') && !acceptNotes ? 'Nothing to commit yet: accept the notes below or decide them on Branch › Uncommitted.' : null}
  onsubmit={commitDrift}
  testid="drift-commit-dialog"
>
  {#snippet extra()}
    {#if uncommitted.data?.notes.length}
      <div class="banner small notes" data-testid="drift-notes">
        <Icon name="info" />
        <div class="grow">
          {uncommitted.data.notes.length} {uncommitted.data.notes.length === 1 ? 'note' : 'notes'} (server edits that cannot be written as config): {uncommitted.data.notes.map((n) => n.label).join(' · ')}
          <label class="row small acc"><input type="checkbox" class="cb" bind:checked={acceptNotes} data-testid="drift-accept-notes" />Accept {uncommitted.data.notes.length === 1 ? 'it' : 'them'} with this commit (kept as they are until they change)</label>
          <span class="tiny muted">Discard or take the bytes instead on <a href={appHref(site, 'branch?tab=uncommitted')} onclick={() => (driftOpen = false)}>Branch › Uncommitted</a>.</span>
        </div>
      </div>
    {/if}
  {/snippet}
</CommitDialog>

<Confirm bind:open={discarding} title="Discard {count} {count === 1 ? 'change' : 'changes'}?" danger confirmLabel="Discard" message="Your changes in this browser are thrown away; nothing on the server changes (nothing was committed)." onconfirm={() => (draft.discard(), toasts.ok('Changes discarded'))} />

<style>
  .acc {
    gap: 6px;
    align-items: center;
    margin: 6px 0 2px;
  }
  .notes .grow {
    display: flex;
    flex-direction: column;
  }
  .drift {
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
  }
</style>
