<script lang="ts">
  /**
   * §21 #123 Files: ONE shared bucket per deployment, as seen by this site (the URL stays /transfers). The listing is the
   * same everywhere; `here` / job / sources belong to this site. Publish from this browser or from a path on the
   * server; a copy moves only when someone asks for it (Download, Save on server, Get a copy here) — nothing downloads
   * by itself. A Download of a file that is not here gets a copy first and starts the browser download when it
   * arrives (only while this page is open).
   */
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import { api, apiPath, download, errCode, errMessage, uploadRaw } from '$lib/api';
  import { Resource, poll } from '$lib/state/resource.svelte';
  import { overview } from '$lib/state/app.svelte';
  import { toasts } from '$lib/state/toast.svelte';
  import { bytes } from '$lib/format';
  import { fileCopiesText, fileOriginText, siteFileBadge, type FileBadge, type SiteSharedFile } from '@opm/shared/files';
  import type { HostFsEntry } from '@opm/shared/types';
  import PageHead from '$lib/ui/PageHead.svelte';
  import ChooseSite from '$lib/ui/ChooseSite.svelte';
  import Load from '$lib/ui/Load.svelte';
  import Empty from '$lib/ui/Empty.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Time from '$lib/ui/Time.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import Confirm from '$lib/ui/Confirm.svelte';
  import TransferSendForm, { transferFileError, transferNoteError } from '$opm/forms/transfers/TransferSendForm.svelte';
  import Progress from '$lib/ui/Progress.svelte';
  import Stale from '$lib/ui/Stale.svelte';
  import HostFsPicker from '$lib/transfers/HostFsPicker.svelte';

  const slug = $derived(page.params.siteSlug ?? '');
  let showGone = $state(false);
  const list = new Resource(() => api.listSiteFiles({ siteSlug: slug, query: showGone ? { unpublished: true } : {} }));
  const items = $derived(list.data?.items ?? []);
  /** fileId → the browser download waits for the copy */
  let wantDl = $state<Record<string, true>>({});

  // ── Save on server state (declared early: `fast` reads saveWait)
  let saveFor = $state<SiteSharedFile | null>(null);
  let saveOpen = $state(false);
  let savePath = $state('');
  let saveBusy = $state(false);
  let saveWait = $state(false);
  let saveErr = $state<string | null>(null);

  /** a copy is running on this site (poll faster) */
  const fast = $derived(items.some((f) => f.job && f.job.phase !== 'failed') || Object.keys(wantDl).length > 0 || saveWait);
  $effect(() => {
    void slug;
    untrack(() => {
      list.reset();
      wantDl = {};
      saveOpen = false;
    });
  });
  $effect(() => {
    void showGone;
    if (slug === '*') return;
    return poll(() => list.load(), fast ? 2000 : 10_000);
  });
  const tile = $derived(overview.data?.sites.find((s) => s.siteSlug === slug));

  const BADGE: Record<FileBadge, { label: string; tone: string }> = {
    uploading: { label: 'uploading to the cloud', tone: 'info' },
    here: { label: 'on this site', tone: 'ok' },
    getting: { label: 'getting a copy', tone: 'info' },
    'waiting-cloud': { label: 'waiting for the cloud', tone: 'warn' },
    failed: { label: 'copy failed', tone: 'bad' },
    available: { label: 'available', tone: '' },
    unavailable: { label: 'unavailable', tone: 'bad' },
    unpublished: { label: 'unpublished', tone: '' },
  };
  const fromText = (from: string | null) => (from === 'cloud' ? 'the cloud' : from?.startsWith('peer:') ? from.slice(5) : 'a site');
  /** why the bytes cannot be reached now (Download / Save / Get a copy are disabled) */
  function blocked(f: SiteSharedFile): string | null {
    const b = siteFileBadge(f);
    if (b === 'uploading') return 'It is still being uploaded to the cloud.';
    if (b === 'unavailable') return 'No site and not the cloud holds a copy; it cannot be downloaded.';
    return null;
  }
  function hint(f: SiteSharedFile): string | null {
    const b = siteFileBadge(f);
    if (b === 'uploading') return 'The upload into the cloud is still running; it can be copied once it is complete.';
    if (b === 'getting') return `Copying from ${fromText(f.job?.from ?? null)} onto ${slug}.`;
    if (b === 'waiting-cloud') return `No site that holds it answers over the mesh; the cloud gets a copy${f.cloud.fetch?.from ? ` from ${f.cloud.fetch.from}` : ''} first.`;
    if (b === 'failed') return `${f.job?.error ?? 'The copy failed.'} Try again with Get a copy here.`;
    if (b === 'available') return f.sources.peers.length ? `Can be copied from ${f.sources.peers.join(', ')} over the mesh.` : f.sources.cloud ? 'Can be copied from the cloud.' : `Held by ${f.sites.join(', ')} (not reachable now); the cloud gets a copy first.`;
    if (b === 'unavailable') return blocked(f);
    return null;
  }
  const running = (f: SiteSharedFile) => !!f.job && f.job.phase !== 'failed';
  const pct = (f: SiteSharedFile) => (f.sizeBytes ? Math.min(1, (f.job?.bytesDone ?? 0) / f.sizeBytes) : 0);

  // ── follow the copies this page asked for (download / save when they arrive)
  $effect(() => {
    const all = list.data?.items;
    if (!all) return;
    untrack(() => {
      for (const id of Object.keys(wantDl)) {
        const f = all.find((x) => x.fileId === id);
        if (f && !f.here && f.state !== 'unpublished' && f.job?.phase !== 'failed') continue;
        const rest = { ...wantDl };
        delete rest[id];
        wantDl = rest;
        if (!f || f.state === 'unpublished') toasts.info('Download cancelled', 'The file was unpublished.');
        else if (f.here) void dlNow(f);
        else toasts.push('bad', `Could not get ${f.fileName} onto ${slug}`, f.job?.error ?? undefined);
      }
      if (saveWait && saveFor) {
        const f = all.find((x) => x.fileId === saveFor!.fileId);
        if (!f || f.state === 'unpublished') {
          saveWait = false;
          saveErr = 'The file was unpublished.';
        } else if (f.here) {
          saveWait = false;
          saveFor = f;
          void doSave();
        } else if (f.job?.phase === 'failed') {
          saveWait = false;
          saveErr = `Could not get a copy onto ${slug}: ${f.job.error ?? 'failed'}`;
        } else saveFor = f;
      }
    });
  });

  // ── actions
  async function dlNow(f: SiteSharedFile) {
    try {
      await download('downloadSiteFile', apiPath('downloadSiteFile', { siteSlug: slug, fileId: f.fileId }), f.fileName);
    } catch (e) {
      toasts.err(e, 'Download failed');
    }
  }
  async function getCopy(f: SiteSharedFile): Promise<boolean> {
    try {
      await api.fetchToSite({ siteSlug: slug, fileId: f.fileId });
      list.load();
      return true;
    } catch (e) {
      toasts.err(e, `Could not get ${f.fileName} onto ${slug}`);
      list.load();
      return false;
    }
  }
  async function dl(f: SiteSharedFile) {
    if (f.here) return dlNow(f);
    if (await getCopy(f)) {
      wantDl = { ...wantDl, [f.fileId]: true };
      toasts.info(`Getting ${f.fileName} onto ${slug}`, 'The download starts when the copy is here. Keep this page open.');
    }
  }
  async function getHere(f: SiteSharedFile) {
    if (await getCopy(f)) toasts.info(`Getting ${f.fileName} onto ${slug}`, 'Follow the progress in the list.');
  }

  let dropFor = $state<SiteSharedFile | null>(null);
  let dropOpen = $state(false);
  async function drop() {
    const f = dropFor!;
    try {
      await api.dropSiteCopy({ siteSlug: slug, fileId: f.fileId });
      toasts.ok(`Dropped the copy on ${slug}`, `${f.fileName} stays published.`);
    } catch (e) {
      toasts.push('bad', 'Could not drop the copy', errMessage(e));
    }
    list.load();
  }
  let unpubFor = $state<SiteSharedFile | null>(null);
  let unpubOpen = $state(false);
  async function unpublish() {
    const f = unpubFor!;
    try {
      await api.unpublishSiteFile({ siteSlug: slug, fileId: f.fileId });
      toasts.ok(`Unpublished ${f.fileName}`);
    } catch (e) {
      toasts.err(e, 'Could not unpublish');
    }
    list.load();
  }

  // ── Save on server (gets a copy first when it is not here)
  $effect(() => {
    if (!saveOpen) untrack(() => (saveWait = false));
  });
  function openSave(f: SiteSharedFile) {
    saveFor = f;
    savePath = `/srv/opm-drop/${f.fileName}`;
    saveErr = null;
    saveWait = false;
    saveOpen = true;
  }
  async function save(e?: SubmitEvent) {
    e?.preventDefault();
    if (!saveFor || saveWait) return;
    const p = savePath.trim();
    if (!p.startsWith('/') || p.endsWith('/')) return (saveErr = 'An absolute file path (start with /, not a directory).');
    saveErr = null;
    if (saveFor.here) return doSave();
    saveBusy = true;
    try {
      await api.fetchToSite({ siteSlug: slug, fileId: saveFor.fileId });
      saveWait = true;
      list.load();
    } catch (x) {
      saveErr = errMessage(x);
    } finally {
      saveBusy = false;
    }
  }
  async function doSave() {
    if (!saveFor) return;
    saveBusy = true;
    saveErr = null;
    const p = savePath.trim();
    try {
      const f = await api.saveSiteFile({ siteSlug: slug, fileId: saveFor.fileId, body: { path: p } });
      saveOpen = false;
      toasts.ok(`Saved ${f.fileName}`, `on ${slug} at ${p} (mode 0640)`);
      list.load();
    } catch (x) {
      saveErr = errCode(x) === 'conflict' && /exist/i.test(errMessage(x)) ? `${p} already exists on ${slug}. Choose another path.` : errMessage(x);
    } finally {
      saveBusy = false;
    }
  }

  // ── Publish: one button → From this browser | From the server
  let chooseOpen = $state(false);
  let pickerOpen = $state(false);
  let uploads = $state<{ id: number; name: string; size: number; p: number; error: string | null }[]>([]);
  let n = 0;

  let pathOpen = $state(false);
  let pathEntry = $state<HostFsEntry | null>(null);
  let pathNote = $state('');
  let pathBusy = $state(false);
  let pathErr = $state<string | null>(null);
  function choosePath() {
    chooseOpen = false;
    pickerOpen = true;
  }
  function picked(_p: string, e: HostFsEntry) {
    pathEntry = e;
    pathErr = null;
    pathOpen = true;
  }
  async function publishPath(e?: SubmitEvent) {
    e?.preventDefault();
    if (!pathEntry || transferNoteError(pathNote)) return;
    pathBusy = true;
    pathErr = null;
    try {
      const f = await api.publishFromPath({ siteSlug: slug, body: { path: pathEntry.path, note: pathNote || null } });
      pathOpen = false;
      toasts.ok(`Published ${f.fileName}`, `Copied into the agent's store on ${slug}; every site sees it now.`);
      pathNote = '';
      pathEntry = null;
      list.load();
    } catch (err) {
      const c = errCode(err);
      pathErr = c === 'not-found' ? `${pathEntry?.path}: no such file on ${slug} (any more).` : c === 'forbidden' ? `Not allowed: ${errMessage(err)}` : errMessage(err);
    } finally {
      pathBusy = false;
    }
  }

  let fileOpen = $state(false);
  let file = $state<File | null>(null);
  let fileNote = $state('');
  let fileSubmitted = $state(false);
  function chooseBrowser() {
    chooseOpen = false;
    fileSubmitted = false;
    fileOpen = true;
  }
  async function publishFile(e?: SubmitEvent) {
    e?.preventDefault();
    fileSubmitted = true;
    if (!file || transferFileError(file) || transferNoteError(fileNote)) return;
    const f = file;
    const note = fileNote;
    fileOpen = false;
    file = null;
    fileNote = '';
    const id = ++n;
    uploads = [...uploads, { id, name: f.name, size: f.size, p: 0, error: null }];
    try {
      await uploadRaw<SiteSharedFile>('publishFromBrowser', apiPath('publishFromBrowser', { siteSlug: slug }, { fileName: f.name, note: note || undefined }), f, (p) => {
        const u = uploads.find((x) => x.id === id);
        if (u) u.p = p;
      });
      uploads = uploads.filter((x) => x.id !== id);
      toasts.ok(`Published ${f.name}`, `Stored on ${slug}; every site sees it now.`);
      list.load();
    } catch (err) {
      const u = uploads.find((x) => x.id === id);
      if (u) u.error = errMessage(err);
    }
  }
</script>

<PageHead icon="folder" eyebrow="Agent · site {slug}" title="Shared files" sub="One shared bucket for the deployment, as seen by this site. Every site and the cloud console see the same files; a copy moves only when someone asks for it." />

{#if slug === '*'}
  <ChooseSite what="shared files" text={`"All sites" is selected. Every site sees the same files, but copies, downloads and saves belong to one site.`} />
{:else}
  {#if tile && !tile.reachable}
    <div class="banner warn small" style="margin-bottom:12px"><Icon name="wifiOff" /><div class="grow">Site {slug} does not answer; its files cannot be shown or changed from here now.</div><Stale lastSeenAt={tile.lastSeenAt} /></div>
  {/if}

  <section class="card" data-testid="files-card" aria-labelledby="files-h">
    <header class="card-head">
      <div class="hd">
        <h2 id="files-h"><Icon name="folder" size={16} /> Shared files <span class="faint">· as seen by {slug}</span></h2>
        <p class="tiny muted">Download or save a file on {slug}: if {slug} has no copy yet, it gets one from a site that holds it, or from the cloud.</p>
      </div>
      <div class="head-acts">
        <label class="switch small"><input type="checkbox" bind:checked={showGone} data-testid="show-unpublished" />Show unpublished</label>
        <Btn variant="primary" icon="upload" onclick={() => (chooseOpen = true)} data-testid="publish-open">Publish</Btn>
      </div>
    </header>
    {#each uploads as u (u.id)}
      <div class="up row" data-testid="browser-upload">
        <Icon name="upload" />
        <span class="grow ellipsis"><strong>{u.name}</strong> <span class="muted small">{bytes(u.size)} → {slug}</span></span>
        {#if u.error}<span class="bad-text small">{u.error}</span><Btn size="xs" onclick={() => (uploads = uploads.filter((x) => x.id !== u.id))}>Dismiss</Btn>{:else}<span class="pbar"><Progress value={u.p} label="Upload to {slug}" /></span><span class="mono small">{Math.round(u.p * 100)} %</span>{/if}
      </div>
    {/each}
    <Load loading={list.loading} loaded={list.loaded} error={list.error} retry={() => list.load()}>
      {#if items.length === 0}
        <Empty icon="folder" title="No files" text="Publish a log bundle, a dump or any file — from this browser or from a path on {slug}. Every site and the cloud console see it.">
          <Btn variant="primary" onclick={() => (chooseOpen = true)}>Publish</Btn>
        </Empty>
      {:else}
        <ul class="list" data-testid="files">
          {#each items as f (f.fileId)}
            {@const b = siteFileBadge(f)}
            {@const why = blocked(f)}
            {@const h = hint(f)}
            <li class="item" class:dim={b === 'unpublished'} data-testid="file-row" data-file={f.fileName} data-badge={b}>
              <div class="l1">
                <Icon name={f.origin.kind === 'site' && f.origin.via === 'path' ? 'server' : 'file'} size={15} />
                <strong class="mono fname" title={f.fileName}>{f.fileName}</strong>
                <span class="badge {BADGE[b].tone}" data-testid="badge">{BADGE[b].label}{#if b === 'getting'}&nbsp;{Math.round(pct(f) * 100)} %{/if}</span>
              </div>
              {#if b === 'getting'}<div class="pbar wide"><Progress value={pct(f)} label="Copy of {f.fileName} onto {slug}" /></div>{/if}
              {#if h}<div class="tiny muted hint" class:bad-text={b === 'failed'} data-testid="hint">{h}</div>{/if}
              <div class="meta tiny muted">
                <span>{bytes(f.sizeBytes)}</span>
                <span>published <Time ts={f.publishedAt} /> by {f.publishedBy}</span>
                <span class="ellipsis" title={fileOriginText(f)} data-testid="origin">from {fileOriginText(f)}</span>
                {#if b !== 'unpublished'}<span data-testid="copies">copies: {fileCopiesText(f)}{#if f.here}&nbsp;<strong class="ok-text">· here</strong>{/if}</span>{/if}
              </div>
              {#if f.note}<div class="small note">{f.note}</div>{/if}
              {#if b === 'unpublished'}
                <div class="tiny muted" data-testid="unpublished">Unpublished {#if f.unpublishedAt}<Time ts={f.unpublishedAt} />{/if}{#if f.unpublishedBy}&nbsp;by {f.unpublishedBy}{/if}; every copy was deleted.</div>
              {:else}
                <div class="acts">
                  <Btn size="xs" icon="download" busy={!!wantDl[f.fileId]} disabled={!!why} title={why ?? (f.here ? `Download from ${slug}` : `Get a copy onto ${slug}, then download`)} onclick={() => dl(f)} data-testid="dl-{f.fileId}">Download</Btn>
                  <Btn size="xs" icon="server" disabled={!!why} title={why ?? undefined} onclick={() => openSave(f)} data-testid="save-{f.fileId}">Save on server…</Btn>
                  {#if !f.here && !running(f)}
                    <Btn size="xs" variant="ghost" icon="hardDrive" disabled={!!why} title={why ?? `Keep a copy on ${slug} (nothing is downloaded to this browser)`} onclick={() => getHere(f)} data-testid="get-{f.fileId}">{b === 'failed' ? 'Try again' : 'Get a copy here'}</Btn>
                  {/if}
                  {#if f.here}
                    <Btn size="xs" variant="ghost" icon="minus" onclick={() => ((dropFor = f), (dropOpen = true))} data-testid="drop-{f.fileId}">Drop this site's copy</Btn>
                  {/if}
                  <span class="grow"></span>
                  <Btn size="xs" variant="ghost" icon="trash" class="bad-text" onclick={() => ((unpubFor = f), (unpubOpen = true))} data-testid="unpub-{f.fileId}">Unpublish</Btn>
                </div>
              {/if}
            </li>
          {/each}
        </ul>
      {/if}
    </Load>
  </section>
{/if}

<Modal bind:open={chooseOpen} title="Publish a file" description="Where is the file?" size="sm" testid="publish-choice">
  <div class="choices">
    <button type="button" class="choice" onclick={chooseBrowser} data-testid="publish-browser">
      <span class="ic"><Icon name="monitor" size={20} /></span>
      <span><strong>From this browser</strong><span class="small muted">Pick a file on this computer. It is stored on {slug}.</span></span>
    </button>
    <button type="button" class="choice" onclick={choosePath} data-testid="publish-path">
      <span class="ic"><Icon name="server" size={20} /></span>
      <span><strong>From the server</strong><span class="small muted">Pick a file on the server {slug} (browse its filesystem).</span></span>
    </button>
  </div>
  <p class="tiny muted" style="margin:12px 0 0">At most 2 GiB. Every site and the cloud console see it at once; the bytes stay on {slug} until someone asks for a copy.</p>
</Modal>

<HostFsPicker bind:open={pickerOpen} site={slug} onpick={picked} />

<Modal bind:open={pathOpen} title="Publish from the server" description="This file on {slug} is copied into the agent's store and published to every site." testid="path-confirm">
  <form id="path-form" class="stack" onsubmit={publishPath} novalidate>
    <TransferSendForm source="path" bind:note={pathNote} serverError={pathErr} notePlaceholder="e.g. logs for ticket OPS-1182" noteHint="Optional, shown with the file everywhere">
      {#snippet picked()}{@render pickedCard()}{/snippet}
    </TransferSendForm>
  </form>
  {#snippet footer()}
    <Btn onclick={() => (pathOpen = false)}>Cancel</Btn>
    <Btn type="submit" form="path-form" variant="primary" icon="upload" busy={pathBusy} disabled={!pathEntry || !!transferNoteError(pathNote)} data-testid="path-go">Publish</Btn>
  {/snippet}
</Modal>

{#snippet pickedCard()}
  {#if pathEntry}
    <div class="picked">
      <Icon name="file" />
      <div class="grow" style="min-width:0"><strong class="mono" style="word-break:break-all" data-testid="path-picked">{pathEntry.path}</strong><div class="tiny muted">{bytes(pathEntry.sizeBytes)} · modified <Time ts={pathEntry.mtime} /></div></div>
      <Btn size="xs" onclick={() => ((pathOpen = false), (pickerOpen = true))}>Change</Btn>
    </div>
  {/if}
{/snippet}

<Modal bind:open={fileOpen} title="Publish from this browser" description="The file is stored on {slug} and published to every site (≤ 2 GiB)." testid="file-modal">
  <form id="file-form" onsubmit={publishFile} novalidate>
    <TransferSendForm bind:file bind:note={fileNote} submitted={fileSubmitted} fileHint="Up to 2 GiB. Stored on {slug}." noteHint="Optional, shown with the file everywhere" />
  </form>
  {#snippet footer()}
    <Btn onclick={() => (fileOpen = false)}>Cancel</Btn>
    <Btn type="submit" form="file-form" variant="primary" icon="upload" disabled={fileSubmitted && (!!transferFileError(file) || !!transferNoteError(fileNote))} data-testid="file-go">Publish</Btn>
  {/snippet}
</Modal>

<Modal bind:open={saveOpen} title="Save on server" description={saveFor?.here ? `Write ${saveFor.fileName} on ${slug}. The parent directory must exist; the file must not exist yet.` : `${saveFor?.fileName ?? ''} is not on ${slug} yet: a copy is fetched first, then written. The parent directory must exist; the file must not exist yet.`} testid="save-modal">
  <form id="save-form" class="field" onsubmit={save} novalidate>
    <label for="sv-path">Path <span class="req">*</span></label>
    <input id="sv-path" class="input mono" bind:value={savePath} disabled={saveWait} aria-invalid={saveErr ? 'true' : undefined} data-testid="save-path" />
    {#if saveWait && saveFor}
      <div class="wait" data-testid="save-wait">
        <div class="tiny muted">{siteFileBadge(saveFor) === 'waiting-cloud' ? 'Waiting for the cloud to get a copy…' : `Getting a copy onto ${slug}${saveFor.job?.from ? ` from ${fromText(saveFor.job.from)}` : ''}… ${Math.round(pct(saveFor) * 100)} %`}</div>
        <Progress value={pct(saveFor)} label="Copy of {saveFor.fileName} onto {slug}" />
      </div>
    {/if}
    {#if saveErr}<div class="field-err" role="alert" data-testid="save-err">{saveErr}</div>{/if}
  </form>
  {#snippet footer()}
    <Btn onclick={() => (saveOpen = false)}>Cancel</Btn>
    <Btn type="submit" form="save-form" variant="primary" busy={saveBusy || saveWait} disabled={!savePath.trim()} data-testid="save-go">{saveFor?.here ? 'Save' : 'Get a copy and save'}</Btn>
  {/snippet}
</Modal>

<Confirm bind:open={dropOpen} title="Drop the copy on {slug}?" confirmLabel="Drop copy" message="{dropFor?.fileName ?? ''} stays published; {slug} deletes its copy and can get one again later. Copies saved to a server path stay." onconfirm={drop} testid="drop-confirm" />
<Confirm bind:open={unpubOpen} title="Unpublish {unpubFor?.fileName ?? ''}?" danger confirmLabel="Unpublish" message="It disappears from the list on every site and in the cloud console, and every copy is deleted (the cloud's and every site's). Copies saved to a server path stay." onconfirm={unpublish} testid="unpub-confirm" />

<style>
  .card-head {
    align-items: flex-start;
    flex-wrap: wrap;
    gap: 10px;
  }
  .hd {
    min-width: 0;
    flex: 1 1 280px;
  }
  .hd h2 {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
  }
  .hd p {
    margin: 4px 0 0;
  }
  .head-acts {
    display: flex;
    align-items: center;
    gap: 12px;
    flex-wrap: wrap;
  }
  .up {
    padding: 8px var(--sp-4);
    border-bottom: 1px solid var(--border);
    gap: 8px;
    min-width: 0;
  }
  .pbar {
    width: 120px;
    flex: none;
  }
  .pbar.wide {
    width: 100%;
    margin: 6px 0 2px;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .item {
    padding: 10px var(--sp-4);
    border-bottom: 1px solid var(--border);
    min-width: 0;
  }
  .item:last-child {
    border-bottom: 0;
  }
  .item.dim {
    opacity: 0.6;
  }
  .l1 {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }
  .fname {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .l1 .badge {
    flex: none;
  }
  .hint {
    margin-top: 3px;
    word-break: break-word;
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    gap: 2px 12px;
    margin-top: 4px;
    min-width: 0;
  }
  .meta > span {
    max-width: 100%;
  }
  .note {
    margin-top: 4px;
    color: var(--text-2);
  }
  .acts {
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
    align-items: center;
    margin-top: 6px;
  }
  .acts .grow {
    flex: 1;
  }
  .wait {
    display: grid;
    gap: 6px;
    margin-top: 10px;
  }
  .choices {
    display: grid;
    gap: 10px;
  }
  .choice {
    display: flex;
    gap: 12px;
    align-items: flex-start;
    text-align: left;
    padding: 14px;
    border: 1px solid var(--border);
    border-radius: var(--r-lg);
    background: var(--surface);
    color: inherit;
    cursor: pointer;
    font: inherit;
  }
  .choice:hover,
  .choice:focus-visible {
    border-color: var(--accent);
    background: var(--accent-soft);
  }
  .choice strong {
    display: block;
    margin-bottom: 2px;
  }
  .choice .small {
    display: block;
  }
  .ic {
    display: grid;
    place-items: center;
    width: 38px;
    height: 38px;
    border-radius: var(--r-md);
    background: var(--surface-2);
    border: 1px solid var(--border);
    flex: none;
  }
  .picked {
    display: flex;
    gap: 10px;
    align-items: center;
    padding: 10px;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    background: var(--surface-2);
  }
</style>
