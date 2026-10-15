<script lang="ts">
  import { api, apiPath, download, errMessage, uploadRaw } from '$lib/api';
  import { Resource, poll } from '$lib/state/resource.svelte';
  import { overview, siteSlugs } from '$lib/state/app.svelte';
  import { toasts } from '$lib/state/toast.svelte';
  import { bytes } from '$lib/format';
  import type { RollbackPlanResponse, SnapshotDetailResponse, SnapshotSummary, SnapshotVolume, UpdateRun } from '@opm/shared/types';
  import SiteVolumesCard from '$lib/ui/SiteVolumesCard.svelte';
  import PageHead from '$lib/ui/PageHead.svelte';
  import Load from '$lib/ui/Load.svelte';
  import Empty from '$lib/ui/Empty.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Time from '$lib/ui/Time.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import Drawer from '$lib/ui/Drawer.svelte';
  import Confirm from '$lib/ui/Confirm.svelte';
  import CopyId from '$lib/ui/CopyId.svelte';
  import Progress from '$lib/ui/Progress.svelte';
  import DiffTables from '$lib/ui/DiffTables.svelte';
  import GateChecklist from '$lib/ui/GateChecklist.svelte';
  import ApproveFlow from '$lib/ui/ApproveFlow.svelte';
  import UpdateProgress from '$lib/ui/UpdateProgress.svelte';

  const list = new Resource(() => api.listSnapshots({}));
  $effect(() => poll(() => list.load(), 10_000));
  /** §21 #110 a running update never blocks snapshots or rollbacks (starting a rollback stops it) */
  const updateRunning = $derived(!!overview.data?.lock);

  // take snapshot
  let takeOpen = $state(false);
  let note = $state('');
  let takeBusy = $state(false);
  async function take(e?: SubmitEvent) {
    e?.preventDefault();
    if (note.length > 500) return;
    takeBusy = true;
    try {
      const r = await api.createSnapshot({ body: { note } });
      takeOpen = false;
      note = '';
      const tid = toasts.push('info', 'Snapshot running', `on ${r.sites.join(', ')} — containers stop one by one around their archive`, 0);
      list.load();
      const t0 = Date.now();
      const iv = setInterval(async () => {
        const d = await api.getSnapshot({ snapshotId: r.snapshotId }).catch(() => null);
        const done = d && d.summary.missingSites.length === 0 && r.sites.every((s) => d.summary.sites.includes(s));
        if (done || Date.now() - t0 > 30 * 60_000) {
          clearInterval(iv);
          toasts.update(tid, done ? 'ok' : 'warn', done ? 'Snapshot complete' : 'Snapshot still running', done ? `${r.snapshotId} · ${bytes(d!.summary.sizeBytes)} on ${r.sites.length} sites` : 'Check Backups later.');
          list.load();
        } else toasts.update(tid, 'info', 'Snapshot running', `${d?.summary.sites.length ?? 0}/${r.sites.length} sites done`, 0);
      }, 1500);
    } catch (err) {
      toasts.err(err, 'Snapshot not started');
    } finally {
      takeBusy = false;
    }
  }

  // import
  let picker: HTMLInputElement | undefined = $state();
  let importing = $state<{ name: string; p: number } | null>(null);
  let importErr = $state<string | null>(null);
  async function onPicked(e: Event) {
    const input = e.currentTarget as HTMLInputElement;
    const f = input.files?.[0];
    input.value = '';
    if (!f) return;
    if (f.size > 2 * 1024 * 1024 * 1024) return toasts.push('bad', 'File too large', 'At most 2 GiB.');
    importing = { name: f.name, p: 0 };
    try {
      const s = await uploadRaw<SnapshotSummary>('importSnapshot', apiPath('importSnapshot'), f, (p) => (importing = { name: f.name, p }));
      toasts.ok('Snapshot imported', `${s.snapshotId} (seq ${s.seq}) · parts sent to ${s.sites.join(', ')}`);
      list.load();
    } catch (err) {
      importErr = errMessage(err);
    } finally {
      importing = null;
    }
  }

  // download
  let dlFor = $state<SnapshotSummary | null>(null);
  let dlOpen = $state(false);
  let includeImages = $state(false);
  let dlBusy = $state(false);
  async function doDownload() {
    if (!dlFor) return;
    dlBusy = true;
    try {
      await download('downloadSnapshot', apiPath('downloadSnapshot', { snapshotId: dlFor.snapshotId }, { includeImages }), `${dlFor.snapshotId}.opmsnap`);
      dlOpen = false;
    } catch (e) {
      toasts.err(e, 'Download failed');
    } finally {
      dlBusy = false;
    }
  }

  // note
  let noteFor = $state<SnapshotSummary | null>(null);
  let noteOpen = $state(false);
  let noteText = $state('');
  let noteBusy = $state(false);
  async function saveNote(e?: SubmitEvent) {
    e?.preventDefault();
    if (!noteFor || noteText.length > 500) return;
    noteBusy = true;
    try {
      await api.patchSnapshot({ snapshotId: noteFor.snapshotId, body: { note: noteText } });
      noteOpen = false;
      toasts.ok('Note saved on every site');
      list.load();
    } catch (err) {
      toasts.err(err, 'Saving the note failed');
    } finally {
      noteBusy = false;
    }
  }

  // delete
  let delFor = $state<SnapshotSummary | null>(null);
  let delOpen = $state(false);
  async function doDelete() {
    if (!delFor) return;
    try {
      await api.deleteSnapshot({ snapshotId: delFor.snapshotId });
      toasts.ok('Snapshot deleted on every reachable site');
      list.load();
    } catch (e) {
      toasts.err(e, 'Delete failed');
      return false;
    }
  }

  // rollback
  let rbFor = $state<SnapshotSummary | null>(null);
  let rbOpen = $state(false);
  let plan = new Resource<RollbackPlanResponse>(() => api.rollbackPlan({ snapshotId: rbFor?.snapshotId ?? '' }));
  let approving = $state(false);
  let run = $state<UpdateRun | null>(null);
  function openRollback(s: SnapshotSummary) {
    rbFor = s;
    plan.reset();
    plan.load();
    rbOpen = true;
  }
  function onStarted(r: UpdateRun) {
    run = r;
    rbOpen = false;
    toasts.info('Rollback started', 'A pre-update snapshot of the current state is taken first.');
    overview.load();
    window.scrollTo({ top: 0, behavior: 'smooth' });
  }
  // detail (volumes by volumeSlug; snapshots written before round D carry `name`)
  let detFor = $state<SnapshotSummary | null>(null);
  let detOpen = $state(false);
  const detail = new Resource<SnapshotDetailResponse>(() => api.getSnapshot({ snapshotId: detFor?.snapshotId ?? '' }));
  function openDetail(s: SnapshotSummary) {
    detFor = s;
    detail.reset();
    detail.load();
    detOpen = true;
  }
  const volSlug = (v: SnapshotVolume) => v.volumeSlug ?? (v as unknown as { name?: string }).name ?? '?';
  const volSites = $derived(siteSlugs(overview.data).filter((s) => overview.data?.sites.find((t) => t.siteSlug === s)?.inConfig !== false));

  const reasonTone = { 'pre-update': 'info', manual: 'accent', scheduled: '', imported: 'warn', 'volume-auto': '' } as const; // §21 #112
</script>

<PageHead icon="archive" title="Backups" sub="Snapshots of every site: config, image refs and the declared volumes with Back up on. Host paths are never backed up. Pre-update snapshots are taken by every update; scheduled ones daily at 02:00.">
  {#snippet actions()}
    <Btn icon="upload" onclick={() => picker?.click()} disabled={!!importing} data-testid="import">Import .opmsnap</Btn>
    <Btn variant="primary" icon="archive" onclick={() => (takeOpen = true)} data-testid="take-snapshot">Take snapshot now</Btn>
  {/snippet}
</PageHead>
<input type="file" accept=".opmsnap,application/x-tar" class="sr-only" bind:this={picker} onchange={onPicked} tabindex="-1" aria-hidden="true" data-testid="import-input" />
<p class="card-hint small muted cli" data-testid="backups-cli">Same as <code>opm-agent snapshot list</code> (the table), <code>snapshot export</code> (Download, with or without images) and <code>snapshot import</code> (Import .opmsnap) on the server.</p>

{#if run}
  <div style="margin-bottom:18px"><UpdateProgress {run} ondismiss={() => ((run = null), list.load())} /></div>
{/if}
{#if importing}
  <div class="card card-pad row" style="margin-bottom:14px" data-testid="import-progress">
    <Icon name="upload" />
    <span class="grow">Uploading <strong>{importing.name}</strong> — the agent verifies the signature and every part, then sends the parts to the other sites.</span>
    <span style="width:160px"><Progress value={importing.p} /></span><span class="small mono">{Math.round(importing.p * 100)} %</span>
  </div>
{/if}

<section class="card">
  <Load loading={list.loading} loaded={list.loaded} error={list.error} retry={() => list.load()}>
    {#if (list.data?.items.length ?? 0) === 0}
      <Empty icon="archive" title="No snapshots yet" text="Take one now, or wait for the first update / the daily scheduled snapshot.">
        <Btn variant="primary" onclick={() => (takeOpen = true)}>Take snapshot now</Btn>
      </Empty>
    {:else}
      <div class="table-wrap">
        <table class="table" data-testid="snapshots">
          <thead><tr><th>Snapshot id</th><th>When</th><th>Reason</th><th>Head seq</th><th>Size</th><th>Sites</th><th>Note</th><th>By</th><th class="right">Actions</th></tr></thead>
          <tbody>
            {#each list.data?.items ?? [] as s (s.snapshotId)}
              <tr>
                <td><CopyId value={s.snapshotId} short={10} label="snapshot id" /></td>
                <td class="small"><Time ts={s.createdAt} /></td>
                <td><span class="badge {reasonTone[s.reason]}">{s.reason}</span></td>
                <td class="small">{s.seq || '—'}</td>
                <td class="small nowrap">{bytes(s.sizeBytes)}{#if s.hasImages} <span class="badge">+ images</span>{/if}</td>
                <td class="small nowrap">
                  {#if s.missingSites.length}<span class="warn-text">{s.sites.length}/{s.sites.length + s.missingSites.length}</span> <span class="tiny muted">missing {s.missingSites.join(', ')}</span>{:else}<span class="ok-text">{s.sites.join(', ')}</span>{/if}
                </td>
                <td class="small note">{s.note || '—'}</td>
                <td class="small">{s.createdBy}</td>
                <td class="actions">
                  <Btn size="xs" icon="eye" variant="ghost" onclick={() => openDetail(s)} data-testid="detail-{s.snapshotId}">Details</Btn>
                  <Btn size="xs" icon="download" onclick={() => ((dlFor = s), (includeImages = false), (dlOpen = true))} data-testid="download-{s.snapshotId}">Download</Btn>
                  <Btn size="xs" icon="edit" variant="ghost" onclick={() => ((noteFor = s), (noteText = s.note), (noteOpen = true))}>Note</Btn>
                  <Btn size="xs" icon="history" onclick={() => openRollback(s)} disabled={s.missingSites.length > 0} title={s.missingSites.length ? 'A site part is missing' : undefined} data-testid="rollback-{s.snapshotId}">Roll back</Btn>
                  <Btn size="xs" variant="ghost" icon="trash" aria-label="Delete snapshot" onclick={() => ((delFor = s), (delOpen = true))} />
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </Load>
</section>

<h2 class="sect" id="volumes">Volumes per site</h2>
<div class="stack" data-testid="site-volumes">
  {#each volSites as s (s)}<SiteVolumesCard siteSlug={s} self={overview.data?.self === s} />{:else}<p class="muted small">No sites yet.</p>{/each}
</div>

<Drawer bind:open={detOpen} wide title="Snapshot {detFor?.snapshotId ?? ''}" subtitle={detFor ? `${detFor.reason} · head seq ${detFor.seq} · ${bytes(detFor.sizeBytes)}` : ''} testid="snapshot-detail">
  <Load loading={detail.loading} loaded={detail.loaded} error={detail.error} retry={() => detail.load()} rows={5}>
    {#if detail.data}
      {@const sn = detail.data.snapshot}
      <div class="stack">
        <p class="small muted" style="margin:0">Created <Time ts={sn.createdAt} /> by {sn.createdBy}{sn.note ? ` · “${sn.note}”` : ''}. Volumes are restored by name into <code>vol/&lt;name&gt;</code>.</p>
        {#each Object.entries(sn.sites) as [site, part] (site)}
          <section data-testid="detail-site-{site}">
            <h3 style="margin-bottom:8px"><span class="mono">{site}</span> <span class="muted small">{bytes(part.sizeBytes)}</span></h3>
            <div class="table-wrap">
              <table class="table">
                <thead><tr><th>Volume</th><th>Containers then</th><th>Size</th><th>sha256</th><th>Archive</th></tr></thead>
                <tbody>
                  {#each part.volumes as v, i (i)}
                    <tr data-testid="detail-vol-{site}-{volSlug(v)}"><td class="mono small"><strong>{volSlug(v)}</strong></td><td class="small">{v.containers.join(', ') || '—'}</td><td class="small nowrap">{bytes(v.sizeBytes)}</td><td><CopyId value={v.sha256} short={14} label="sha256" /></td><td class="mono tiny muted">{v.path}</td></tr>
                  {:else}<tr><td colspan="5" class="muted small">No volumes archived (none declared with Back up on).</td></tr>{/each}
                </tbody>
              </table>
            </div>
            {#if part.images.length}<p class="tiny muted" style="margin:6px 0 0">Images: <span class="mono">{part.images.map((im) => `${im.serviceSlug}:${im.version}`).join(' · ')}</span></p>{/if}
          </section>
        {/each}
      </div>
    {/if}
  </Load>
  {#snippet footer()}<Btn onclick={() => (detOpen = false)}>Close</Btn>{/snippet}
</Drawer>

<Modal bind:open={takeOpen} title="Take snapshot now" description="Every site of the head config archives its declared volumes with Back up on (containers stop one by one around their own archive and restart at once). All sites must be reachable.">
  {#if updateRunning}<div class="banner warn small" style="margin-bottom:10px" data-testid="snap-update-running"><Icon name="alert" /><div class="grow">An update is running on this site — the snapshot may catch containers mid-update.</div></div>{/if}
  <form id="take-form" onsubmit={take} class="field">
    <label for="snap-note">Note</label>
    <textarea id="snap-note" class="textarea" rows="3" maxlength="500" bind:value={note} placeholder="Why (optional)" data-testid="snap-note"></textarea>
    <span class="hint right">{note.length}/500</span>
  </form>
  {#snippet footer()}
    <Btn onclick={() => (takeOpen = false)}>Cancel</Btn>
    <Btn type="submit" form="take-form" variant="primary" busy={takeBusy} data-testid="snap-go">Take snapshot</Btn>
  {/snippet}
</Modal>

<Modal bind:open={dlOpen} title="Download snapshot" size="sm">
  <div class="stack">
    <div class="banner warn small"><Icon name="alert" /><div class="grow">The file contains the config <strong>with secret values</strong>. Keep it safe.</div></div>
    <label class="check"><input type="checkbox" bind:checked={includeImages} data-testid="dl-images" />Include images (larger; restores without the cloud)</label>
    <p class="muted small" style="margin:0">{dlFor?.snapshotId} · {bytes(dlFor?.sizeBytes)} without images · parts of {dlFor?.sites.join(', ')}</p>
  </div>
  {#snippet footer()}
    <Btn onclick={() => (dlOpen = false)}>Cancel</Btn>
    <Btn variant="primary" icon="download" busy={dlBusy} onclick={doDownload} data-testid="dl-go">Download .opmsnap</Btn>
  {/snippet}
</Modal>

<Modal bind:open={noteOpen} title="Edit note" size="sm">
  <form id="note-form" onsubmit={saveNote} class="field">
    <label for="n-text">Note</label>
    <textarea id="n-text" class="textarea" rows="3" maxlength="500" bind:value={noteText}></textarea>
    <span class="hint right">{noteText.length}/500</span>
  </form>
  {#snippet footer()}
    <Btn onclick={() => (noteOpen = false)}>Cancel</Btn>
    <Btn type="submit" form="note-form" variant="primary" busy={noteBusy}>Save</Btn>
  {/snippet}
</Modal>

<Modal open={importErr !== null} title="Import failed" size="sm" onclose={() => (importErr = null)}>
  <div class="banner bad"><Icon name="alert" /><div class="grow">{importErr}</div></div>
  {#snippet footer()}<Btn variant="primary" onclick={() => (importErr = null)}>OK</Btn>{/snippet}
</Modal>

<Confirm bind:open={delOpen} title="Delete snapshot?" danger confirmLabel="Delete" message="Snapshot {delFor?.snapshotId} is deleted on every reachable site. A rollback to it is not possible afterwards." onconfirm={doDelete} />

<Drawer bind:open={rbOpen} wide title="Roll back to snapshot" subtitle={rbFor ? `${rbFor.snapshotId} · ${rbFor.reason} · head seq ${rbFor.seq}` : ''} testid="rollback-drawer">
  <Load loading={plan.loading} loaded={plan.loaded} error={plan.error} retry={() => plan.load()} rows={6}>
    {#if plan.data}
      {@const p = plan.data}
      <div class="stack">
        <div class="banner warn"><Icon name="alert" /><div class="grow"><strong>Data written after <Time ts={p.dataLossSince} /> is lost</strong> on the restored volumes. The rollback becomes a new revision <strong>seq {p.newSeq}</strong>; a pre-update snapshot of the current state is taken first.</div></div>
        <section>
          <h3 style="margin-bottom:8px">Images</h3>
          <div class="table-wrap">
            <table class="table">
              <thead><tr><th>Site</th><th>Container</th><th>Now</th><th>After rollback</th></tr></thead>
              <tbody>
                {#each p.images as im, i (i)}
                  <tr><td class="mono">{im.siteSlug}</td><td class="mono">{im.containerSlug}{#if im.containerSlug !== im.serviceSlug}<span class="tiny muted"> {im.serviceSlug}</span>{/if}</td><td class="mono small">{im.from ?? '—'}</td><td class="mono small" class:accent-text={im.from !== im.to}>{im.to ?? 'removed'}{#if im.from !== im.to} <span class="badge info">changes</span>{/if}</td></tr>
                {:else}<tr><td colspan="4" class="muted small">No images.</td></tr>{/each}
              </tbody>
            </table>
          </div>
        </section>
        <section>
          <h3 style="margin-bottom:8px">Config changes</h3>
          <DiffTables changes={p.changes} beforeLabel="Now" afterLabel="Snapshot" empty="The config is the same as now." />
        </section>
        <section class="grid cols-2">
          <div>
            <h3 style="margin-bottom:8px">Volumes restored</h3>
            <p class="tiny muted" style="margin:0 0 6px">Restored by name into <code>vol/&lt;name&gt;</code>, whatever container mounts it now.</p>
            {#each p.volumesRestored as v, i (i)}<div class="small row" data-testid="restored-{v.siteSlug}-{v.volumeSlug}"><Icon name="history" size={13} /><span class="mono">{v.siteSlug}/{v.volumeSlug}</span><span class="muted">{bytes(v.sizeBytes)}</span></div>{:else}<p class="muted small">none</p>{/each}
          </div>
          <div>
            <h3 style="margin-bottom:8px">Volumes kept</h3>
            <p class="tiny muted" style="margin:0 0 6px">Declared now but not in the snapshot — left untouched.</p>
            {#each p.volumesKept as v, i (i)}<div class="small row" data-testid="kept-{v.siteSlug}-{v.volumeSlug}"><Icon name="check" size={13} /><span class="mono">{v.siteSlug}/{v.volumeSlug}</span>{#if !v.backup}<span class="badge">backup off</span>{:else}<span class="tiny muted">not in this snapshot</span>{/if}</div>{:else}<p class="muted small">none</p>{/each}
          </div>
        </section>
        <section>
          <h3 style="margin-bottom:8px">Gate</h3>
          <GateChecklist gate={p.gate} />
          {#if updateRunning}<div class="banner warn small" style="margin-top:8px" data-testid="rollback-update-running"><Icon name="alert" /><div class="grow">An update is running — starting this rollback stops it.</div></div>{/if}
        </section>
      </div>
    {/if}
  </Load>
  {#snippet footer()}
    <Btn onclick={() => (rbOpen = false)}>Close</Btn>
    <Btn variant="danger" icon="history" disabled={!plan.data?.gate.open} onclick={() => (approving = true)} data-testid="rollback-go">Roll back</Btn>
  {/snippet}
</Drawer>

{#if rbFor}
  <ApproveFlow bind:open={approving} kind="rollback" target={rbFor.snapshotId} title="Roll back to {rbFor.snapshotId.slice(-8)}" onstarted={onStarted}>
    {#snippet details()}
      <p class="small">Restores the config, images and data volumes of seq {rbFor?.seq} ({plan.data?.changes.length ?? 0} config changes). Data written after the snapshot is lost.</p>
    {/snippet}
  </ApproveFlow>
{/if}

<style>
  .cli {
    margin: -8px 0 14px;
  }
  .note {
    min-width: 140px;
    max-width: 240px;
    overflow-wrap: anywhere;
  }
  .sect {
    margin: 26px 0 12px;
    font-size: 16px;
  }
  .accent-text {
    color: var(--accent-text);
  }
</style>
