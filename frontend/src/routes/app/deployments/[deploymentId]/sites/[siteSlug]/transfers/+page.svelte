<script lang="ts">
  /**
   * §21 #100 Transfers of one site: ONE module, two columns (operator's point of view).
   * Left = Send (cloud → site, browser upload only); right = Receive (site → cloud). Store-and-forward both ways:
   * the cloud keeps the file, the agent fetches / uploads whenever it is online — nobody has to be on the other end.
   */
  import { page } from '$app/state';
  import { onMount } from 'svelte';
  import { transferState, type Transfer, type TransferState } from '@opm/shared';
  import { api } from '$lib/api';
  import { Loader } from '$lib/resource.svelte';
  import { getDeploymentScope } from '$lib/config-editor/context';
  import { toast } from '$lib/toast.svelte';
  import { bytes } from '$lib/util/format';
  import { personName } from '$lib/util/people';
  import { downloadAuthorized } from '$lib/util/download';
  import { uploads, pendingOf, clearPending, type Pending } from '$lib/transfers/upload.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import TransferSendForm, { transferFileError, transferNoteError } from '$opm/forms/transfers/TransferSendForm.svelte';
  import ConfirmDialog from '$lib/ui/ConfirmDialog.svelte';
  import TableSkeleton from '$lib/ui/TableSkeleton.svelte';
  import ErrorState from '$lib/ui/ErrorState.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import RelTime from '$lib/ui/RelTime.svelte';
  import ProgressBar from '$lib/ui/ProgressBar.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import { Send, Download, Trash2, Upload, RotateCcw, Inbox, X, RefreshCw, ArrowRightLeft } from '$lib/ui/icons';

  const scope = getDeploymentScope();
  const id = scope.id;
  const site = $derived(page.params.siteSlug ?? '');
  const canRead = $derived(scope.can('deployment.transfers', 'read'));
  const canWrite = $derived(scope.can('deployment.transfers', 'write'));
  const sent = new Loader(() => api.listTransfers({ deploymentId: id, siteSlug: site, query: { direction: 'to-site' } }).then((r) => r.items));
  const recv = new Loader(() => api.listTransfers({ deploymentId: id, siteSlug: site, query: { direction: 'to-cloud' } }).then((r) => r.items));
  function reload(quiet = true) {
    sent.load(quiet);
    recv.load(quiet);
  }
  onMount(() => {
    if (!canRead) return;
    sent.load();
    recv.load();
    // the site picks files up / sends files whenever it is online: keep the states fresh while the page is open
    const t = setInterval(() => document.visibilityState === 'visible' && reload(true), 15_000);
    return () => clearInterval(t);
  });
  const local = $derived(uploads.forSite(id, site).filter((p) => !p.transferId || !(sent.data ?? []).some((t) => t.transferId === p.transferId)));

  const TONE: Record<TransferState['tone'], string> = { info: 'blue', ok: 'green', accent: 'violet', warn: 'amber', '': '' };

  // send modal (browser upload only)
  let open = $state(false);
  let file = $state<File | null>(null);
  let note = $state('');
  let touched = $state(false);
  const fileErr = $derived(transferFileError(file));
  const noteErr = $derived(transferNoteError(note));
  function openSend() {
    file = null;
    note = '';
    touched = false;
    open = true;
  }
  function start(e: Event) {
    e.preventDefault();
    touched = true;
    if (fileErr || noteErr || !file) return;
    const f = file;
    open = false;
    uploads
      .send(id, site, f, note, () => sent.load(true))
      .then(() => {
        toast.success(`${f.name} sent`, `Stored in the cloud. ${site} fetches it the next time it is online.`);
        sent.load(true);
      })
      .catch((err) => toast.error(err, `Sending ${f.name} failed`));
  }

  // resume an interrupted browser upload
  let resumeFor = $state<Pending | null>(null);
  let resumeInput = $state<HTMLInputElement | null>(null);
  function askResume(t: Transfer) {
    resumeFor = pendingOf(t.transferId);
    resumeInput?.click();
  }
  function onResumeFile(f: File | undefined) {
    const p = resumeFor;
    if (!f || !p) return;
    if (resumeInput) resumeInput.value = '';
    uploads
      .resume(p, f)
      .then(() => {
        toast.success(`${f.name} sent`);
        sent.load(true);
      })
      .catch((err) => toast.error(err, 'Resume failed'));
  }

  // download (token-only, never a public link) / delete
  let dl = $state<Record<string, boolean>>({});
  async function download(t: Transfer) {
    dl[t.transferId] = true;
    try {
      await downloadAuthorized(`/v1/deployments/${id}/transfers/${t.transferId}/download`, t.fileName);
      if (t.direction === 'to-cloud') {
        toast.success(`${t.fileName} downloaded`);
        recv.load(true);
      }
    } catch (e) {
      toast.error(e, 'Download failed');
    } finally {
      dl[t.transferId] = false;
    }
  }
  let del = $state<Transfer | null>(null);
  let delOpen = $state(false);
  async function doDelete() {
    await api.deleteTransfer({ deploymentId: id, transferId: del!.transferId });
    clearPending(del!.transferId);
    toast.success(`${del!.fileName} deleted`);
    reload(true);
  }
  const pct = (done: number, total: number) => (total ? done / total : 1);
</script>

<svelte:head><title>Transfers · {site} · {scope.label} · OPM</title></svelte:head>

<div class="page">
  <PageHeader
    title="Transfers · {site}"
    subtitle="Store-and-forward in both directions: the cloud keeps every file until someone deletes it, and the site does not need to be online. Files you send appear in the agent GUI at /_opm/app/{site}/transfers (Agent › Transfers) once the agent has fetched them."
    icon={ArrowRightLeft}
    back={{ href: `/app/deployments/${id}/sites`, label: 'Sites' }}
  >
    {#snippet actions()}
      <Button icon={RefreshCw} onclick={() => reload(true)} loading={sent.loading || recv.loading} data-testid="refresh">Refresh</Button>
    {/snippet}
  </PageHeader>

  {#if !canRead}
    <EmptyState icon={ArrowRightLeft} title="No access" text="You need deployment.transfers.read to see the transfers." />
  {:else}
    <div class="cols" data-testid="transfers-cols">
      <!-- LEFT: Send (cloud → site) -->
      <section class="card col" data-testid="col-send" aria-labelledby="h-send">
        <header class="card-head">
          <span class="ico"><Icon icon={Send} size={16} /></span>
          <div class="grow">
            <h3 id="h-send">Send to {site}</h3>
            <div class="xs muted">From your browser to the cloud; the agent fetches it whenever it is online.</div>
          </div>
          {#if canWrite}<Button variant="primary" size="sm" icon={Upload} onclick={openSend} data-testid="send-file">Send a file</Button>{/if}
        </header>
        {#if sent.loading && !sent.data}
          <div class="card-body"><TableSkeleton cols={3} rows={3} /></div>
        {:else if sent.error}
          <div class="card-body"><ErrorState message={sent.error} onretry={() => sent.load()} /></div>
        {:else if !sent.data?.length && !local.length}
          <EmptyState icon={Send} title="Nothing sent yet" text="Send a file (up to 2 GiB) to this site: licences, config bundles, firmware. It waits in the cloud until the site picks it up.">
            {#snippet action()}{#if canWrite}<Button variant="primary" icon={Upload} onclick={openSend}>Send a file</Button>{/if}{/snippet}
          </EmptyState>
        {:else}
          <ul class="list" data-testid="send-list">
            {#each local as p (p.key)}
              <li class="item" data-testid="transfer-local">
                <div class="main">
                  <div class="strong ellipsis">{p.fileName}</div>
                  <div class="xs faint">{bytes(p.size)} · you</div>
                </div>
                <div class="state">
                  {#if p.phase === 'error'}<span class="small text-red">{p.error}</span>
                  {:else}<div class="stack tight"><span class="xs muted">{p.phase === 'hashing' ? 'Hashing…' : 'Starting…'} {Math.round(pct(p.done, p.size) * 100)} %</span><ProgressBar value={pct(p.done, p.size)} tone="blue" label="hashing {p.fileName}" /></div>{/if}
                </div>
                <div class="acts">{#if p.phase === 'error'}<Button size="sm" variant="ghost" icon={X} onclick={() => uploads.dismiss(p.key)} aria-label="Dismiss" />{/if}</div>
              </li>
            {/each}
            {#each sent.data ?? [] as t (t.transferId)}
              {@const prog = uploads.byTransfer(t.transferId)}
              {@const st = transferState(prog?.phase === 'done' && t.status === 'uploading' ? { ...t, status: 'ready' } : t, 'cloud')}
              <li class="item" class:dim={t.status === 'expired'} data-testid="transfer-row" data-state={st.key}>
                <div class="main">
                  <div class="strong ellipsis" title={t.fileName}>{t.fileName}</div>
                  {#if t.note}<div class="xs muted ellipsis" title={t.note}>{t.note}</div>{/if}
                  <div class="xs faint">{bytes(t.sizeBytes)} · {personName(t.createdBy)} · <RelTime ts={t.createdAt} /></div>
                </div>
                <div class="state">
                  {#if t.status === 'uploading' && prog && prog.phase !== 'done'}
                    {#if prog.phase === 'error'}<span class="small text-red">{prog.error}</span>
                    {:else}<div class="stack tight" data-testid="upload-progress"><span class="xs muted">{prog.phase === 'completing' ? 'Checking…' : 'Uploading'} {Math.round(pct(prog.done, prog.size) * 100)} %</span><ProgressBar value={pct(prog.done, prog.size)} label="upload {t.fileName}" /></div>{/if}
                  {:else}
                    <span class="badge {TONE[st.tone]}" data-testid="state"><span class="dot"></span>{st.label}</span>
                    {#if t.status === 'uploading'}<div class="xs text-amber">interrupted — resume from this browser</div>
                    {:else if st.key === 'saved-on-server'}<div class="xs mono faint ellipsis" title={t.savedPath ?? ''}>{t.savedPath}</div>
                    {:else if st.key === 'on-site' && t.fetchedAt}<div class="xs faint">fetched <RelTime ts={t.fetchedAt} /></div>
                    {:else if st.hint}<div class="xs faint">{st.hint}</div>{/if}
                  {/if}
                </div>
                <div class="acts">
                  {#if t.status === 'uploading' && !prog && canWrite && pendingOf(t.transferId)}
                    <Button size="sm" icon={RotateCcw} onclick={() => askResume(t)} data-testid="resume">Resume</Button>
                  {/if}
                  {#if t.status !== 'uploading' && t.status !== 'expired'}<Button size="sm" variant="ghost" icon={Download} loading={dl[t.transferId]} onclick={() => download(t)} data-testid="download" aria-label="Download {t.fileName}" tip="Download" />{/if}
                  {#if canWrite && t.status !== 'expired'}<Button size="sm" variant="ghost" icon={Trash2} onclick={() => ((del = t), (delOpen = true))} aria-label="Delete {t.fileName}" data-testid="delete-transfer" />{/if}
                </div>
              </li>
            {/each}
          </ul>
        {/if}
      </section>

      <!-- RIGHT: Receive (site → cloud) -->
      <section class="card col" data-testid="col-receive" aria-labelledby="h-recv">
        <header class="card-head">
          <span class="ico"><Icon icon={Inbox} size={16} /></span>
          <div class="grow">
            <h3 id="h-recv">Received from {site}</h3>
            <div class="xs muted">Files a person on site sent from the agent GUI; the site uploads them by itself.</div>
          </div>
        </header>
        {#if recv.loading && !recv.data}
          <div class="card-body"><TableSkeleton cols={3} rows={3} /></div>
        {:else if recv.error}
          <div class="card-body"><ErrorState message={recv.error} onretry={() => recv.load()} /></div>
        {:else if !recv.data?.length}
          <EmptyState icon={Inbox} title="Nothing received yet" text="Files appear here once {site} has uploaded them (Agent › Transfers › Send). Nobody needs to have this page open." />
        {:else}
          <ul class="list" data-testid="receive-list">
            {#each recv.data as t (t.transferId)}
              {@const st = transferState(t, 'cloud')}
              <li class="item" class:dim={t.status === 'expired'} data-testid="receive-row" data-state={st.key}>
                <div class="main">
                  <div class="strong ellipsis" title={t.fileName}>{t.fileName}</div>
                  {#if t.note}<div class="xs muted ellipsis" title={t.note}>{t.note}</div>{/if}
                  <div class="xs faint">{bytes(t.sizeBytes)} · {t.createdBy} · <RelTime ts={t.createdAt} /></div>
                </div>
                <div class="state">
                  <span class="badge {TONE[st.tone]}" data-testid="state"><span class="dot"></span>{st.label}</span>
                  {#if st.hint}<div class="xs faint">{st.hint}</div>{/if}
                </div>
                <div class="acts">
                  {#if t.status === 'ready' || t.status === 'downloaded'}<Button size="sm" variant={t.status === 'ready' ? 'primary' : 'default'} icon={Download} loading={dl[t.transferId]} onclick={() => download(t)} data-testid="download">Download</Button>{/if}
                  {#if canWrite && t.status !== 'expired'}<Button size="sm" variant="ghost" icon={Trash2} onclick={() => ((del = t), (delOpen = true))} aria-label="Delete {t.fileName}" data-testid="delete-transfer" />{/if}
                </div>
              </li>
            {/each}
          </ul>
        {/if}
      </section>
    </div>
  {/if}
</div>
<input bind:this={resumeInput} type="file" class="sr-only" tabindex="-1" aria-hidden="true" onchange={(e) => onResumeFile(e.currentTarget.files?.[0])} />

<Modal bind:open title="Send a file to {site}" size="md" testid="send-modal">
  <form id="send-form" class="stack" onsubmit={start} novalidate>
    <p class="small muted">The file goes from this browser to the cloud. {site} does not need to be online: its agent fetches the file the next time it is, and a person on site saves it on the server or downloads it.</p>
    <TransferSendForm bind:file bind:note submitted={touched} fileHint="Up to 2 GiB. Large files upload in 8 MiB parts and can be resumed after a reload." noteHint="Optional, shown to the person on site" />
  </form>
  {#snippet footer()}
    <Button onclick={() => (open = false)}>Cancel</Button>
    <Button type="submit" form="send-form" variant="primary" icon={Send} disabled={touched && (!!fileErr || !!noteErr)} data-testid="send-submit">Send</Button>
  {/snippet}
</Modal>

<ConfirmDialog
  bind:open={delOpen}
  title="Delete {del?.fileName}?"
  message={del?.direction === 'to-site' ? 'The file is removed from the cloud. A site that did not fetch it yet cannot get it any more.' : 'The file is removed from the cloud for everyone.'}
  confirmLabel="Delete"
  danger
  onconfirm={doDelete}
/>

<style>
  .cols {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--sp-4);
    align-items: start;
  }
  @media (max-width: 1000px) {
    .cols {
      grid-template-columns: minmax(0, 1fr);
    }
  }
  .col {
    min-width: 0;
  }
  .ico {
    display: inline-grid;
    place-items: center;
    width: 28px;
    height: 28px;
    border-radius: var(--r-md);
    background: var(--surface-2, var(--surface-hover));
    color: var(--text-2);
    flex: none;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .item {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(120px, 190px) auto;
    gap: var(--sp-3);
    align-items: center;
    padding: var(--sp-3) var(--sp-4);
    border-top: 1px solid var(--border);
  }
  .item:first-child {
    border-top: 0;
  }
  .item.dim {
    opacity: 0.6;
  }
  .main,
  .state {
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .state .badge {
    align-self: flex-start;
    white-space: nowrap;
  }
  .acts {
    display: flex;
    gap: var(--sp-1);
    justify-content: flex-end;
  }
  @media (max-width: 560px) {
    .item {
      grid-template-columns: minmax(0, 1fr) auto;
    }
    .state {
      grid-column: 1 / -1;
      grid-row: 2;
    }
  }
</style>
