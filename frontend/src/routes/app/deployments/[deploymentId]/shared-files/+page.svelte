<script lang="ts">
  /**
   * §21 #123 Shared files: ONE bucket per deployment (replaces the per-site Transfers of §21 #100).
   * Publish from this browser into the cloud; a site publishes from its agent GUI. Only the listing spreads — the bytes
   * stay where they were published until someone asks for a copy. Nothing downloads by itself: Download needs a copy in
   * the cloud; when only sites hold one, "Get into the cloud" asks a holding site to upload it (this page polls the
   * progress and starts the browser download when the copy arrives, if the person is still here).
   * Route: `shared-files` (the deployment's config "Files" page already owns `files`).
   */
  import { onMount } from 'svelte';
  import { cloudFileBadge, fileCopiesText, fileOriginText, type FileBadge, type SharedFile } from '@opm/shared';
  import { api } from '$lib/api';
  import { Loader } from '$lib/resource.svelte';
  import { getDeploymentScope } from '$lib/config-editor/context';
  import { toast } from '$lib/toast.svelte';
  import { bytes } from '$lib/util/format';
  import { downloadAuthorized } from '$lib/util/download';
  import { uploads, pendingOf, clearPending, type Pending } from '$lib/files/upload.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import Toggle from '$lib/ui/Toggle.svelte';
  import TransferSendForm, { transferFileError, transferNoteError } from '$opm/forms/transfers/TransferSendForm.svelte';
  import ConfirmDialog from '$lib/ui/ConfirmDialog.svelte';
  import TableSkeleton from '$lib/ui/TableSkeleton.svelte';
  import ErrorState from '$lib/ui/ErrorState.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import RelTime from '$lib/ui/RelTime.svelte';
  import ProgressBar from '$lib/ui/ProgressBar.svelte';
  import { Download, Trash2, Upload, RotateCcw, X, RefreshCw, FolderOpen, CloudDownload } from '$lib/ui/icons';

  const scope = getDeploymentScope();
  const id = scope.id;
  const canRead = $derived(scope.can('deployment.transfers', 'read'));
  const canWrite = $derived(scope.can('deployment.transfers', 'write'));

  let showUnpublished = $state(false);
  const files = new Loader(() => api.listFiles({ deploymentId: id, query: showUnpublished ? { unpublished: true } : {} }).then((r) => r.items));

  /** files the person asked to get into the cloud from this page: downloaded to the browser once the copy is there */
  let wanted = $state<Record<string, true>>({});
  let alive = true;
  const fetching = $derived((files.data ?? []).some((f) => f.state === 'ready' && !f.cloud.stored && !!f.cloud.fetch && !f.cloud.fetch.error));

  async function reload() {
    const list = await files.load(true);
    if (!list || !alive) return;
    for (const fid of Object.keys(wanted)) {
      const f = list.find((x) => x.fileId === fid);
      if (!f || f.state === 'unpublished') delete wanted[fid];
      else if (f.cloud.stored) {
        delete wanted[fid];
        void download(f);
      } else if (f.cloud.fetch?.error) delete wanted[fid];
    }
  }
  onMount(() => {
    if (canRead) files.load();
    // fast while a copy is on its way into the cloud, else every 15 s (sites publish whenever they like)
    let last = Date.now();
    const t = setInterval(() => {
      if (document.visibilityState !== 'visible') return;
      const every = fetching || Object.keys(wanted).length ? 2000 : 15_000;
      if (Date.now() - last < every) return;
      last = Date.now();
      void reload();
    }, 1000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  });
  function toggleUnpublished(v: boolean) {
    showUnpublished = v;
    files.load(true);
  }
  /** uploads of this browser that the listing does not show yet (hashing / starting) */
  const local = $derived(uploads.forDeployment(id).filter((p) => !p.fileId || !(files.data ?? []).some((f) => f.fileId === p.fileId)));

  const BADGE: Record<FileBadge, { label: string; tone: string; tip: string }> = {
    uploading: { label: 'uploading', tone: 'blue', tip: 'The bytes are still arriving at the cloud' },
    here: { label: 'in the cloud', tone: 'green', tip: 'The cloud holds a copy: download it any time' },
    getting: { label: 'getting into the cloud', tone: 'blue', tip: 'A site that holds the file is uploading it to the cloud' },
    'waiting-cloud': { label: 'waiting for the cloud', tone: 'amber', tip: '' },
    failed: { label: 'failed', tone: 'red', tip: 'Getting the file into the cloud failed' },
    available: { label: 'on a site', tone: 'violet', tip: 'Only sites hold a copy: get it into the cloud to download it' },
    unavailable: { label: 'no copy', tone: 'amber', tip: 'No site holds a copy any more' },
    unpublished: { label: 'unpublished', tone: 'outline', tip: 'Removed from the listing everywhere' },
  };

  // publish (browser → cloud)
  let open = $state(false);
  let file = $state<File | null>(null);
  let note = $state('');
  let touched = $state(false);
  const fileErr = $derived(transferFileError(file));
  const noteErr = $derived(transferNoteError(note));
  function openPublish() {
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
      .publish(id, f, note, () => files.load(true))
      .then(() => {
        toast.success(`${f.name} published`, 'Stored in the cloud. Every site lists it; a site gets a copy when someone asks for one.');
        files.load(true);
      })
      .catch((err) => toast.error(err, `Publishing ${f.name} failed`));
  }

  // resume an interrupted browser upload
  let resumeFor = $state<Pending | null>(null);
  let resumeInput = $state<HTMLInputElement | null>(null);
  function askResume(f: SharedFile) {
    resumeFor = pendingOf(f.fileId);
    resumeInput?.click();
  }
  function onResumeFile(f: File | undefined) {
    const p = resumeFor;
    if (!f || !p) return;
    if (resumeInput) resumeInput.value = '';
    uploads
      .resume(p, f)
      .then(() => {
        toast.success(`${f.name} published`);
        files.load(true);
      })
      .catch((err) => toast.error(err, 'Resume failed'));
  }

  // download (token-only, never a public link) / get into the cloud / unpublish
  let busy = $state<Record<string, boolean>>({});
  async function download(f: SharedFile) {
    busy[f.fileId] = true;
    try {
      await downloadAuthorized(`/v1/deployments/${id}/files/${f.fileId}/download`, f.fileName);
    } catch (e) {
      toast.error(e, `Downloading ${f.fileName} failed`);
      files.load(true);
    } finally {
      busy[f.fileId] = false;
    }
  }
  async function getIntoCloud(f: SharedFile) {
    busy[f.fileId] = true;
    try {
      const r = await api.fetchFileToCloud({ deploymentId: id, fileId: f.fileId });
      if (r.cloud.stored) {
        await download(r);
        return;
      }
      wanted[f.fileId] = true;
      if (files.data) files.set(files.data.map((x) => (x.fileId === r.fileId ? r : x)));
      toast.success(`Getting ${f.fileName} into the cloud`, 'The download starts by itself when the copy has arrived, if this page is still open.');
    } catch (e) {
      toast.error(e, `Getting ${f.fileName} into the cloud failed`);
      files.load(true);
    } finally {
      busy[f.fileId] = false;
    }
  }
  let del = $state<SharedFile | null>(null);
  let delOpen = $state(false);
  async function doUnpublish() {
    const f = del!;
    await api.unpublishFile({ deploymentId: id, fileId: f.fileId });
    clearPending(f.fileId);
    delete wanted[f.fileId];
    toast.success(`${f.fileName} unpublished`);
    files.load(true);
  }
  const pct = (done: number, total: number) => (total ? done / total : 1);
  const actor = (s: string | null) => (s?.startsWith('site:') ? `site ${s.slice(5)}` : (s ?? '—'));
</script>

<svelte:head><title>Shared files · {scope.label} · OPM</title></svelte:head>

<div class="page">
  <PageHeader title="Shared files" subtitle="One shared bucket for the whole deployment. Publish from here or from any site; every site lists the file, and nothing is copied until someone asks for it." icon={FolderOpen}>
    {#snippet actions()}
      {#if canRead}
        <Toggle checked={showUnpublished} label="Show unpublished" onchange={toggleUnpublished} testid="show-unpublished" />
        <Button icon={RefreshCw} onclick={() => files.load(true)} loading={files.loading} data-testid="refresh">Refresh</Button>
        {#if canWrite}<Button variant="primary" icon={Upload} onclick={openPublish} data-testid="publish-file">Publish a file</Button>{/if}
      {/if}
    {/snippet}
  </PageHeader>

  {#if !canRead}
    <EmptyState icon={FolderOpen} title="No access" text="You need deployment.transfers.read to see the shared files." />
  {:else if files.loading && !files.data}
    <TableSkeleton cols={7} rows={4} />
  {:else if files.error && !files.data}
    <ErrorState message={files.error} onretry={() => files.load()} />
  {:else if !files.data?.length && !local.length}
    <EmptyState icon={FolderOpen} title="No shared files yet" text="Publish a file (up to 2 GiB): licences, config bundles, firmware, dumps. A person on a site can publish one from the agent GUI too.">
      {#snippet action()}{#if canWrite}<Button variant="primary" icon={Upload} onclick={openPublish}>Publish a file</Button>{/if}{/snippet}
    </EmptyState>
  {:else}
    <div class="table-wrap">
      <table class="table" data-testid="files-table">
        <thead><tr><th>Name</th><th class="num">Size</th><th>Published</th><th>Origin</th><th>Copies</th><th>State</th><th class="right">Actions</th></tr></thead>
        <tbody>
          {#each local as p (p.key)}
            <tr data-testid="file-local">
              <td><div class="strong ellipsis nm">{p.fileName}</div></td>
              <td class="num small nowrap">{bytes(p.size)}</td>
              <td class="small muted">you · now</td>
              <td class="small">the cloud (browser)</td>
              <td class="small faint">—</td>
              <td class="st">
                {#if p.phase === 'error'}<span class="small text-red">{p.error}</span>
                {:else}<div class="stack tight"><span class="xs muted">{p.phase === 'hashing' ? 'Hashing…' : 'Starting…'} {Math.round(pct(p.done, p.size) * 100)} %</span><ProgressBar value={pct(p.done, p.size)} tone="blue" label="hashing {p.fileName}" /></div>{/if}
              </td>
              <td class="acts">{#if p.phase === 'error'}<Button size="sm" variant="ghost" icon={X} onclick={() => uploads.dismiss(p.key)} aria-label="Dismiss" />{/if}</td>
            </tr>
          {/each}
          {#each files.data ?? [] as f (f.fileId)}
            {@const prog = uploads.byFile(f.fileId)}
            {@const b = prog?.phase === 'done' && f.state === 'uploading' ? 'here' : cloudFileBadge(f)}
            {@const fx = f.cloud.fetch}
            <tr class:dim={f.state === 'unpublished'} data-testid="file-row" data-badge={b} data-file={f.fileName}>
              <td>
                <div class="strong ellipsis nm" title={f.fileName}>{f.fileName}</div>
                {#if f.note}<div class="xs muted ellipsis nm" title={f.note}>{f.note}</div>{/if}
              </td>
              <td class="num small nowrap">{bytes(f.sizeBytes)}</td>
              <td class="small">
                <div class="ellipsis pub">{f.publishedBy}</div>
                <div class="xs faint"><RelTime ts={f.publishedAt} /></div>
              </td>
              <td class="small" data-testid="origin">{fileOriginText(f)}</td>
              <td class="small" data-testid="copies">{f.state === 'unpublished' ? '—' : fileCopiesText(f)}</td>
              <td class="st">
                {#if f.state === 'uploading' && prog && prog.phase !== 'done'}
                  {#if prog.phase === 'error'}<span class="small text-red">{prog.error}</span>
                  {:else}<div class="stack tight" data-testid="upload-progress"><span class="xs muted">{prog.phase === 'completing' ? 'Checking…' : prog.phase === 'hashing' ? 'Hashing…' : 'Uploading'} {Math.round(pct(prog.done, prog.size) * 100)} %</span><ProgressBar value={pct(prog.done, prog.size)} label="upload {f.fileName}" /></div>{/if}
                {:else}
                  <span class="badge {BADGE[b].tone}" data-testid="badge" data-tip={BADGE[b].tip || null}><span class="dot"></span>{BADGE[b].label}</span>
                  {#if b === 'getting' && fx}
                    <div class="stack tight prog" data-testid="fetch-progress">
                      <span class="xs muted">{fx.startedAt ? `from ${fx.from} · ${Math.round(pct(fx.bytesDone, f.sizeBytes) * 100)} %` : fx.from ? `waiting for ${fx.from} to start` : 'waiting for a site that holds it'}</span>
                      <ProgressBar value={pct(fx.bytesDone, f.sizeBytes)} tone="blue" indeterminate={!fx.startedAt} label="getting {f.fileName} into the cloud" />
                    </div>
                  {:else if b === 'failed' && fx?.error}<div class="xs text-red" data-testid="fetch-error">{fx.error}</div>
                  {:else if f.state === 'uploading'}<div class="xs text-amber">interrupted — resume from the browser that published it</div>
                  {:else if f.state === 'unpublished'}<div class="xs faint">by {actor(f.unpublishedBy)}{#if f.unpublishedAt} · <RelTime ts={f.unpublishedAt} />{/if}</div>{/if}
                {/if}
              </td>
              <td class="acts">
                {#if f.state === 'uploading'}
                  {#if !prog && canWrite && pendingOf(f.fileId)}<Button size="sm" icon={RotateCcw} onclick={() => askResume(f)} data-testid="resume">Resume</Button>{/if}
                {:else if f.state === 'ready'}
                  {#if f.cloud.stored || b === 'here'}
                    <Button size="sm" icon={Download} loading={busy[f.fileId]} onclick={() => download(f)} data-testid="download" aria-label="Download {f.fileName}">Download</Button>
                  {:else if fx && !fx.error}
                    <Button size="sm" icon={Download} disabled tip={wanted[f.fileId] ? 'Downloads by itself when the copy has arrived' : 'Download once the copy has arrived'} data-testid="download-wait">Download</Button>
                  {:else if f.sites.length}
                    <Button size="sm" icon={CloudDownload} loading={busy[f.fileId]} onclick={() => getIntoCloud(f)} tip="Ask {f.sites[0]} to upload its copy, then download it" data-testid="get-into-cloud">{fx?.error ? 'Retry' : 'Get into the cloud'}</Button>
                  {:else}
                    <Button size="sm" icon={Download} disabled tip="No site holds a copy any more, and the cloud has none" data-testid="download-unavailable">Download</Button>
                  {/if}
                {/if}
                {#if canWrite && f.state !== 'unpublished'}<Button size="sm" variant="ghost" icon={Trash2} onclick={() => ((del = f), (delOpen = true))} aria-label="Unpublish {f.fileName}" tip="Unpublish" data-testid="unpublish" />{/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>
<input bind:this={resumeInput} type="file" class="sr-only" tabindex="-1" aria-hidden="true" onchange={(e) => onResumeFile(e.currentTarget.files?.[0])} />

<Modal bind:open title="Publish a file" size="md" testid="publish-modal">
  <form id="publish-form" class="stack" onsubmit={start} novalidate>
    <p class="small muted">The file goes from this browser to the cloud and is listed on every site of {scope.label}. Sites do not copy it by themselves: a person on a site gets a copy when they need it.</p>
    <TransferSendForm bind:file bind:note submitted={touched} fileHint="Up to 2 GiB. Large files upload in 8 MiB parts and can be resumed after a reload." noteHint="Optional, shown with the file everywhere" />
  </form>
  {#snippet footer()}
    <Button onclick={() => (open = false)}>Cancel</Button>
    <Button type="submit" form="publish-form" variant="primary" icon={Upload} disabled={touched && (!!fileErr || !!noteErr)} data-testid="publish-submit">Publish</Button>
  {/snippet}
</Modal>

<ConfirmDialog
  bind:open={delOpen}
  title="Unpublish {del?.fileName}?"
  message="The file disappears from the listing everywhere: the cloud copy and every site's copy are deleted. A copy someone saved to a server path stays."
  confirmLabel="Unpublish"
  danger
  onconfirm={doUnpublish}
/>

<style>
  th.right {
    text-align: right;
  }
  .nm {
    max-width: 320px;
  }
  .pub {
    max-width: 200px;
  }
  .st {
    min-width: 170px;
  }
  .st .badge {
    white-space: nowrap;
  }
  .prog {
    margin-top: 4px;
    max-width: 200px;
  }
  .acts {
    text-align: right;
    white-space: nowrap;
  }
  .acts :global(> *) {
    margin-left: var(--sp-1);
  }
  tr.dim td {
    opacity: 0.6;
  }
</style>
