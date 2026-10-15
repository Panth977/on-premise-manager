<script lang="ts">
  /**
   * §21 #100 Transfers: Send (this site → cloud, left) and Receive (cloud → this site, right) in one module.
   * Both directions are store-and-forward: the agent queues + retries to-cloud uploads and fetches to-site files whenever
   * the cloud is reachable; nobody has to be on the other end.
   */
  import { page } from '$app/state';
  import { api, apiPath, download, errCode, errMessage, uploadRaw } from '$lib/api';
  import { Resource, poll } from '$lib/state/resource.svelte';
  import { overview } from '$lib/state/app.svelte';
  import { toasts } from '$lib/state/toast.svelte';
  import { bytes } from '$lib/format';
  import { transferState } from '@opm/shared/transfers';
  import type { HostFsEntry, Transfer } from '@opm/shared/types';
  import PageHead from '$lib/ui/PageHead.svelte';
  import ChooseSite from '$lib/ui/ChooseSite.svelte';
  import Load from '$lib/ui/Load.svelte';
  import Empty from '$lib/ui/Empty.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Time from '$lib/ui/Time.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import TransferSendForm, { transferFileError, transferNoteError } from '$opm/forms/transfers/TransferSendForm.svelte';
  import Progress from '$lib/ui/Progress.svelte';
  import Stale from '$lib/ui/Stale.svelte';
  import HostFsPicker from '$lib/transfers/HostFsPicker.svelte';

  const slug = $derived(page.params.siteSlug ?? '');
  const sent = new Resource(() => api.listSiteTransfers({ siteSlug: slug, query: { direction: 'to-cloud' } }));
  const recv = new Resource(() => api.listSiteTransfers({ siteSlug: slug, query: { direction: 'to-site' } }));
  $effect(() => {
    void slug;
    sent.reset();
    recv.reset();
    if (slug === '*') return;
    const a = poll(() => sent.load(), 5000);
    const b = poll(() => recv.load(), 10_000);
    return () => {
      a();
      b();
    };
  });
  const tile = $derived(overview.data?.sites.find((s) => s.siteSlug === slug));

  // ── Send: one button → Browser | Path
  let chooseOpen = $state(false);
  let pickerOpen = $state(false);
  let uploads = $state<{ id: number; name: string; size: number; p: number; error: string | null }[]>([]);
  let n = 0;

  // path → note → send
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
  async function sendPath(e?: SubmitEvent) {
    e?.preventDefault();
    if (!pathEntry || transferNoteError(pathNote)) return;
    pathBusy = true;
    pathErr = null;
    try {
      const t = await api.sendFromPath({ siteSlug: slug, body: { path: pathEntry.path, note: pathNote || undefined } });
      pathOpen = false;
      toasts.ok(`${t.fileName} queued on ${slug}`, 'Stored on the site; it uploads to the cloud as soon as the cloud is reachable.');
      pathNote = '';
      pathEntry = null;
      sent.load();
    } catch (err) {
      const c = errCode(err);
      pathErr = c === 'not-found' ? `${pathEntry?.path}: no such file on ${slug} (any more).` : c === 'forbidden' ? `Not allowed: ${errMessage(err)}` : errMessage(err);
    } finally {
      pathBusy = false;
    }
  }

  // browser → file + note → upload
  let fileOpen = $state(false);
  let file = $state<File | null>(null);
  let fileNote = $state('');
  function chooseBrowser() {
    chooseOpen = false;
    fileSubmitted = false;
    fileOpen = true;
  }
  let fileSubmitted = $state(false);
  async function sendFile(e?: SubmitEvent) {
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
      await uploadRaw<Transfer>('sendFromBrowser', apiPath('sendFromBrowser', { siteSlug: slug }, { fileName: f.name, note: note || undefined }), f, (p) => {
        const u = uploads.find((x) => x.id === id);
        if (u) u.p = p;
      });
      uploads = uploads.filter((x) => x.id !== id);
      toasts.ok(`${f.name} stored on ${slug}`, 'Queued for the cloud; it uploads in the background.');
      sent.load();
    } catch (err) {
      const u = uploads.find((x) => x.id === id);
      if (u) u.error = errMessage(err);
    }
  }

  // ── Receive: save on server / download
  let saveFor = $state<Transfer | null>(null);
  let saveOpen = $state(false);
  let savePath = $state('');
  let saveBusy = $state(false);
  let saveErr = $state<string | null>(null);
  function openSave(t: Transfer) {
    saveFor = t;
    savePath = `/srv/opm-drop/${t.fileName}`;
    saveErr = null;
    saveOpen = true;
  }
  async function save(e?: SubmitEvent) {
    e?.preventDefault();
    if (!saveFor) return;
    if (!savePath.startsWith('/') || savePath.endsWith('/')) return (saveErr = 'An absolute file path (start with /, not a directory).');
    saveBusy = true;
    saveErr = null;
    try {
      const t = await api.saveTransfer({ siteSlug: slug, transferId: saveFor.transferId, body: { path: savePath.trim() } });
      saveOpen = false;
      toasts.ok(`Saved ${t.fileName}`, `on ${slug} at ${t.savedPath} (mode 0640, owner root)`);
      recv.load();
    } catch (x) {
      saveErr = errCode(x) === 'conflict' ? `${savePath} already exists on ${slug}. Choose another path.` : errMessage(x);
    } finally {
      saveBusy = false;
    }
  }
  async function dl(t: Transfer) {
    try {
      await download('downloadSiteTransfer', apiPath('downloadSiteTransfer', { siteSlug: slug, transferId: t.transferId }), t.fileName);
      setTimeout(() => (t.direction === 'to-site' ? recv : sent).load(), 800);
    } catch (e) {
      toasts.err(e, 'Download failed');
    }
  }
  /** a received file is on this site (save / download possible) */
  const onSite = (t: Transfer) => t.status !== 'expired' && t.status !== 'uploading' && (t.status === 'saved-on-server' || t.status === 'downloaded' || t.delivery?.phase === 'on-site' || (!t.delivery && t.status === 'ready'));
  const sentCount = $derived(sent.data?.items.length ?? 0);
  const recvCount = $derived(recv.data?.items.length ?? 0);
</script>

<PageHead icon="swap" eyebrow="Agent · site {slug}" title="Transfers" sub="Files between this site and the cloud. Both directions are store-and-forward: the agent keeps a file until the other side has it, and nobody has to be on the other end." />

{#if slug === '*'}
  <ChooseSite what="transfers" text={`"All sites" is selected, and transfers belong to one site.`} />
{:else}
  {#if tile && !tile.reachable}
    <div class="banner warn small" style="margin-bottom:12px"><Icon name="wifiOff" /><div class="grow">Site {slug} does not answer; its transfers cannot be shown or started from here now.</div><Stale lastSeenAt={tile.lastSeenAt} /></div>
  {/if}

  <div class="cols" data-testid="transfers-cols">
    <!-- LEFT: Send -->
    <section class="card col" data-testid="send-col" aria-labelledby="send-h">
      <header class="card-head">
        <div class="hd">
          <h2 id="send-h"><Icon name="upload" size={16} /> Send <span class="faint">· {slug} → cloud</span></h2>
          <p class="tiny muted">Stored on this site at once, uploaded when the cloud is reachable. The operations team downloads it at the deployment's Transfers page.</p>
        </div>
        <Btn variant="primary" icon="upload" onclick={() => (chooseOpen = true)} data-testid="send-open">Send</Btn>
      </header>
      {#each uploads as u (u.id)}
        <div class="up row" data-testid="browser-upload">
          <Icon name="upload" />
          <span class="grow ellipsis"><strong>{u.name}</strong> <span class="muted small">{bytes(u.size)} → {slug}</span></span>
          {#if u.error}<span class="bad-text small">{u.error}</span><Btn size="xs" onclick={() => (uploads = uploads.filter((x) => x.id !== u.id))}>Dismiss</Btn>{:else}<span class="pbar"><Progress value={u.p} label="Upload to {slug}" /></span><span class="mono small">{Math.round(u.p * 100)} %</span>{/if}
        </div>
      {/each}
      <Load loading={sent.loading} loaded={sent.loaded} error={sent.error} retry={() => sent.load()}>
        {#if sentCount === 0}
          <Empty icon="send" title="Nothing sent yet" text="Send a log bundle, a dump or any file from {slug} to the cloud — from this browser or from a path on the server.">
            <Btn variant="primary" onclick={() => (chooseOpen = true)}>Send</Btn>
          </Empty>
        {:else}
          <ul class="list" data-testid="sent">
            {#each sent.data?.items ?? [] as t (t.transferId)}
              {@const s = transferState(t, 'agent')}
              <li class="item" class:dim={s.key === 'expired'} data-testid="sent-row" data-state={s.key}>
                <div class="l1">
                  <Icon name={t.sourcePath ? 'server' : 'file'} size={15} />
                  <strong class="mono fname" title={t.fileName}>{t.fileName}</strong>
                  <span class="badge {s.tone}" data-testid="state">{s.label}{#if s.progress !== null && s.key === 'uploading'}&nbsp;{Math.round(s.progress * 100)} %{/if}</span>
                </div>
                {#if s.progress !== null}<div class="pbar wide"><Progress value={s.progress} tone="" label="Upload of {t.fileName}" /></div>{/if}
                {#if s.hint}<div class="tiny muted hint" data-testid="hint">{s.hint}</div>{/if}
                <div class="meta tiny muted">
                  <span>{bytes(t.sizeBytes)}</span>
                  <span><Time ts={t.createdAt} /></span>
                  <span class="ellipsis">{t.createdBy}</span>
                  {#if t.sourcePath}<span class="mono ellipsis" title={t.sourcePath}>from {t.sourcePath}</span>{:else}<span>from the browser</span>{/if}
                </div>
                {#if t.note}<div class="small note">{t.note}</div>{/if}
                <div class="acts"><Btn size="xs" variant="ghost" icon="download" disabled={s.key === 'expired'} onclick={() => dl(t)}>Download</Btn></div>
              </li>
            {/each}
          </ul>
        {/if}
      </Load>
    </section>

    <!-- RIGHT: Receive -->
    <section class="card col" data-testid="receive-col" aria-labelledby="recv-h">
      <header class="card-head">
        <div class="hd">
          <h2 id="recv-h"><Icon name="inbox" size={16} /> Receive <span class="faint">· cloud → {slug}</span></h2>
          <p class="tiny muted">Files the operations team sent. The agent fetches them by itself whenever it is online; save them on the server or download them here. They stay until someone deletes them.</p>
        </div>
      </header>
      <Load loading={recv.loading} loaded={recv.loaded} error={recv.error} retry={() => recv.load()}>
        {#if recvCount === 0}
          <Empty icon="inbox" title="No files" text="Nothing was sent to {slug}. The cloud sends files from the deployment's Transfers page." />
        {:else}
          <ul class="list" data-testid="received">
            {#each recv.data?.items ?? [] as t (t.transferId)}
              {@const s = transferState(t, 'agent')}
              {@const here = onSite(t)}
              <li class="item" class:dim={s.key === 'expired'} data-testid="recv-row" data-state={s.key}>
                <div class="l1">
                  <Icon name="file" size={15} />
                  <strong class="mono fname" title={t.fileName}>{t.fileName}</strong>
                  <span class="badge {s.tone}" data-testid="state">{s.label}{#if s.progress !== null}&nbsp;{Math.round(s.progress * 100)} %{/if}</span>
                </div>
                {#if s.progress !== null}<div class="pbar wide"><Progress value={s.progress} label="Download of {t.fileName}" /></div>{/if}
                {#if s.hint}<div class="tiny muted hint" data-testid="hint">{s.hint}</div>{/if}
                <div class="meta tiny muted">
                  <span>{bytes(t.sizeBytes)}</span>
                  <span><Time ts={t.createdAt} /></span>
                  <span class="ellipsis">from {t.createdBy}</span>
                </div>
                {#if t.note}<div class="small note">{t.note}</div>{/if}
                <div class="acts">
                  <Btn size="xs" icon="server" disabled={!here} onclick={() => openSave(t)} data-testid="save-{t.transferId}">Save on server</Btn>
                  <Btn size="xs" icon="download" variant="ghost" disabled={!here} onclick={() => dl(t)} data-testid="dl-{t.transferId}">Download</Btn>
                </div>
              </li>
            {/each}
          </ul>
        {/if}
      </Load>
    </section>
  </div>
{/if}

<Modal bind:open={chooseOpen} title="Send a file to the cloud" description="Where is the file?" size="sm" testid="send-choice">
  <div class="choices">
    <button type="button" class="choice" onclick={chooseBrowser} data-testid="send-browser">
      <span class="ic"><Icon name="monitor" size={20} /></span>
      <span><strong>Browser</strong><span class="small muted">Pick a file on this computer. It is stored on {slug} first.</span></span>
    </button>
    <button type="button" class="choice" onclick={choosePath} data-testid="send-path">
      <span class="ic"><Icon name="server" size={20} /></span>
      <span><strong>Path</strong><span class="small muted">Pick a file on the server {slug} (browse its filesystem).</span></span>
    </button>
  </div>
  <p class="tiny muted" style="margin:12px 0 0">At most 2 GiB. The cloud stores it whether or not anyone is looking; it is queued on {slug} while the cloud is unreachable.</p>
</Modal>

<HostFsPicker bind:open={pickerOpen} site={slug} onpick={picked} />

<Modal bind:open={pathOpen} title="Send from the server" description="This file on {slug} is copied into the agent's store and uploaded to the cloud in the background." testid="path-confirm">
  <form id="path-form" class="stack" onsubmit={sendPath} novalidate>
    <TransferSendForm source="path" bind:note={pathNote} serverError={pathErr} notePlaceholder="e.g. logs for ticket OPS-1182" noteHint="Optional, shown in the cloud console with the file">
      {#snippet picked()}{@render pickedCard()}{/snippet}
    </TransferSendForm>
  </form>
  {#snippet footer()}
    <Btn onclick={() => (pathOpen = false)}>Cancel</Btn>
    <Btn type="submit" form="path-form" variant="primary" icon="upload" busy={pathBusy} disabled={!pathEntry || !!transferNoteError(pathNote)} data-testid="path-go">Send</Btn>
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

<Modal bind:open={fileOpen} title="Send from the browser" description="The file is stored on {slug} first, then uploaded to the cloud in the background (≤ 2 GiB)." testid="file-modal">
  <form id="file-form" onsubmit={sendFile} novalidate>
    <TransferSendForm bind:file bind:note={fileNote} submitted={fileSubmitted} fileHint="Up to 2 GiB. Stored on this site first, then uploaded to the cloud." noteHint="Optional, shown in the cloud console with the file" />
  </form>
  {#snippet footer()}
    <Btn onclick={() => (fileOpen = false)}>Cancel</Btn>
    <Btn type="submit" form="file-form" variant="primary" icon="upload" disabled={fileSubmitted && (!!transferFileError(file) || !!transferNoteError(fileNote))} data-testid="file-go">Send</Btn>
  {/snippet}
</Modal>

<Modal bind:open={saveOpen} title="Save on server" description="Write {saveFor?.fileName ?? ''} on {slug}. The parent directory must exist; the file must not exist yet.">
  <form id="save-form" class="field" onsubmit={save} novalidate>
    <label for="sv-path">Path <span class="req">*</span></label>
    <input id="sv-path" class="input mono" bind:value={savePath} aria-invalid={saveErr ? 'true' : undefined} data-testid="save-path" />
    {#if saveErr}<div class="field-err" role="alert">{saveErr}</div>{/if}
  </form>
  {#snippet footer()}
    <Btn onclick={() => (saveOpen = false)}>Cancel</Btn>
    <Btn type="submit" form="save-form" variant="primary" busy={saveBusy} disabled={!savePath.trim()} data-testid="save-go">Save</Btn>
  {/snippet}
</Modal>

<style>
  .cols {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    gap: var(--sp-4);
    align-items: start;
  }
  @media (max-width: 1100px) {
    .cols {
      grid-template-columns: minmax(0, 1fr);
    }
  }
  .col {
    min-width: 0;
  }
  .card-head {
    align-items: flex-start;
    flex-wrap: nowrap;
  }
  .hd {
    min-width: 0;
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
    margin-top: 6px;
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
