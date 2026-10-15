<script lang="ts">
  /**
   * Docker › Volumes (§21 #89 e browser + §21 #96 volume manager). Per site: the declared volumes (size, files, kind,
   * backup, users with their live state, last snapshot, starter content); Files (browse, view, edit small text files with
   * a diff preview, upload files / folders, new folder, rename / move, delete); Copies (copies made by Copy to path:
   * restore from one, delete); History (the volume actions). Actions: Snapshot now, Download .tar.gz, Copy (to a path /
   * into another volume), Restore (from a copy / an uploaded .tar.gz), Empty, Reset to starter content, Duplicate.
   * "*" = every site's volumes in one table (manage = that site's page).
   */
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { base } from '$app/paths';
  import type { ToolsVolume, VolumeEntry, VolumeFileResponse, VolumeTreeResponse, ToolsContainer } from '@opm/shared/agent-tools';
  import type { VolumeBackup, VolumeBackupsResponse, VolumeCopy, VolumeOp } from '@opm/shared/types';
  import { overview } from '$lib/state/app.svelte';
  import { Resource, poll } from '$lib/state/resource.svelte';
  import { toasts } from '$lib/state/toast.svelte';
  import { api, errCode, errMessage } from '$lib/api';
  import { tools, downloadUrl } from '$lib/tools/api';
  import { bytes } from '$lib/tools/fmt';
  import { duration, fmtDateTime } from '$lib/format';
  import PageHead from '$lib/ui/PageHead.svelte';
  import Load from '$lib/ui/Load.svelte';
  import Empty from '$lib/ui/Empty.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Drawer from '$lib/ui/Drawer.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import Confirm from '$lib/ui/Confirm.svelte';
  import Time from '$lib/ui/Time.svelte';
  import CopyId from '$lib/ui/CopyId.svelte';
  import Progress from '$lib/ui/Progress.svelte';
  import ResetVolumeFlow from '$lib/ui/ResetVolumeFlow.svelte';
  import OpProgress from '$lib/volumes/OpProgress.svelte';
  import EmptyFlow from '$lib/volumes/EmptyFlow.svelte';
  import RestoreFlow from '$lib/volumes/RestoreFlow.svelte';
  import CopyDrawer from '$lib/volumes/CopyDrawer.svelte';
  import FileEditor from '$lib/volumes/FileEditor.svelte';
  import { OP_LABEL, uploadToVolume } from '$lib/volumes/vol';
  import SyncPanel from '$lib/volumes/SyncPanel.svelte';
  import SeedView from '$opm/forms/volumes/SeedView.svelte';
  import { SYNC_LABEL, SYNC_TONE, vsync } from '$lib/volumes/sync';

  const slug = $derived(page.params.siteSlug ?? '*');
  const all = $derived(slug === '*');
  const isSelf = $derived(!!overview.data?.self && overview.data.self === slug);

  // ───────── data
  const vols = new Resource(() => tools.toolsVolumes({ siteSlug: slug }));
  const sv = new Resource(() => api.siteVolumes({ siteSlug: slug }));
  const ctrs = new Resource(() => tools.toolsContainers({ siteSlug: slug }));
  const ops = new Resource(() => api.listVolumeOps({ siteSlug: slug }));
  const copies = new Resource(() => api.listVolumeCopies({ siteSlug: slug }));
  /** §21 #101 persist-to-cloud volumes (agents < 0.12 answer not-found → no sync UI) */
  const sync = new Resource(() => vsync.listVolumeSync({ siteSlug: slug }));
  /** the open volume's status from GET …/{v} (the agent rescans the live tree there when its scan is > 10 s old; the list does not) */
  const syncOne = new Resource(() => vsync.getVolumeSync({ siteSlug: slug, volumeSlug: vSlug }));
  $effect(() => {
    void slug;
    vols.reset();
    sync.reset();
    sv.reset();
    ctrs.reset();
    ops.reset();
    copies.reset();
    return poll(() => {
      vols.load();
      if (slug === '*') return;
      sv.load();
      ctrs.load();
      ops.load();
      copies.load();
      sync.load();
    }, 5000);
  });
  function reloadAll() {
    vols.load();
    sv.load();
    ctrs.load();
    ops.load();
    copies.load();
    sync.load();
    if (vSlug && syncOf(vSlug)) syncOne.load();
    loadTree();
  }

  // ───────── selection (query: v, tab, p)
  const vSlug = $derived(page.url.searchParams.get('v') ?? '');
  const tab = $derived((page.url.searchParams.get('tab') ?? 'overview') as 'overview' | 'files' | 'copies' | 'history' | 'sync' | 'backups');
  // §21 #112 every snapshot on this site that holds the volume (restorable) + its automatic backup policy
  let backups = $state<VolumeBackupsResponse | null>(null);
  let backupsErr = $state<string | null>(null);
  async function loadBackups() {
    const v = vSlug;
    if (!v) return;
    try {
      const r = await api.listVolumeBackups({ siteSlug: slug, volumeSlug: v });
      if (v === vSlug) ((backups = r), (backupsErr = null));
    } catch (e) {
      backupsErr = errMessage(e);
    }
  }
  $effect(() => {
    void vSlug;
    void slug;
    untrack(() => {
      backups = null;
      void loadBackups();
    });
  });
  const BACKUP_REASON: Record<string, string> = { 'volume-auto': 'automatic', scheduled: 'daily site snapshot', 'pre-update': 'before an update', manual: 'manual / before a change', imported: 'imported' };
  function restoreFromBackup(b: VolumeBackup) {
    restoreSrc = { kind: 'snapshot', snapshotId: b.snapshotId, label: `backup ${b.snapshotId} (${BACKUP_REASON[b.reason] ?? b.reason}, ${fmtDateTime(b.createdAt)})` };
    restorePick = false;
    restoreOpen = true;
  }
  const path = $derived(page.url.searchParams.get('p') ?? '/');
  const cur = $derived<ToolsVolume | undefined>(vols.data?.items.find((v) => v.volumeSlug === vSlug));
  const st = $derived(sv.data?.volumes.find((v) => v.volumeSlug === vSlug) ?? null);
  const syncOf = (v: string) => sync.data?.items.find((x) => x.volumeSlug === v) ?? null;
  // each list refresh (5 s poll) also refreshes the open persist volume through GET …/{v}
  $effect(() => {
    if (vSlug && syncOf(vSlug) && sync.updatedAt) untrack(() => syncOne.load());
  });
  const curSync = $derived(syncOne.data?.volumeSlug === vSlug && syncOne.data.siteSlug === slug ? syncOne.data : syncOf(vSlug));
  const agentHome = $derived(sv.data?.agentHome ?? '/var/lib/opm');
  $effect(() => {
    const items = vols.data?.items;
    if (!all && items?.length && !vSlug) untrack(() => nav({ v: items[0].volumeSlug, p: '/' }, true));
  });
  function nav(q: { v?: string; tab?: string; p?: string }, replace = false) {
    const u = new URL(page.url);
    for (const [k, val] of Object.entries(q)) if (val !== undefined) u.searchParams.set(k, val);
    goto(u.pathname + u.search, { replaceState: replace, keepFocus: true, noScroll: true });
  }
  const running = $derived((ops.data?.items ?? []).filter((o) => !o.finishedAt));
  const runningHere = $derived(running.find((o) => o.volumeSlug === vSlug || o.target?.volumeSlug === vSlug) ?? null);
  const busyOf = (v: string) => running.some((o) => o.volumeSlug === v || o.target?.volumeSlug === v);
  const ctrOf = (ref: string): ToolsContainer | undefined => ctrs.data?.items.find((c) => c.ref === ref);
  const users = $derived(
    (st?.usedBy ?? []).map((u) => {
      const c = ctrOf(u.containerSlug);
      return { slug: u.containerSlug, mountPath: u.mountPath, readOnly: u.readOnly, state: c?.state ?? 'missing', image: c?.image ?? '', health: c?.health ?? 'none' };
    }),
  );
  const userSlugs = $derived([...new Set(users.map((u) => u.slug))]);
  /** §21 #110 a running update never blocks volume work (advisory only) */
  const updateRunning = $derived(!!overview.data?.lock);
  const volCopies = $derived((copies.data?.items ?? []).filter((c) => c.volumeSlug === vSlug));
  const volOps = $derived((ops.data?.items ?? []).filter((o) => o.volumeSlug === vSlug || o.target?.volumeSlug === vSlug));
  const others = $derived((vols.data?.items ?? []).filter((v) => v.volumeSlug !== vSlug).map((v) => ({ volumeSlug: v.volumeSlug, kind: v.kind, sizeBytes: v.sizeBytes, fileCount: v.fileCount })));
  const stateTone = (s: string) => (s === 'running' ? 'ok' : s === 'restarting' || s === 'paused' || s === 'created' ? 'warn' : s === 'exited' ? '' : 'bad');

  // ───────── operations
  let opOpen = $state(false);
  let op = $state<VolumeOp | null>(null);
  function showOp(o: VolumeOp) {
    op = o;
    opOpen = true;
    ops.load();
  }
  function opDone(o: VolumeOp) {
    if (o.ok) toasts.ok(`${OP_LABEL[o.kind]} of ${o.volumeSlug} finished`, o.kind === 'copy' ? (o.target?.path ?? undefined) : undefined);
    else toasts.push('bad', `${OP_LABEL[o.kind]} of ${o.volumeSlug} failed`, o.error ?? undefined);
    reloadAll();
  }
  let emptyOpen = $state(false);
  let copyOpen = $state(false);
  let restorePick = $state(false);
  let restoreOpen = $state(false);
  let restoreSrc = $state<{ kind: 'copy'; copyId: string; label: string } | { kind: 'upload'; uploadId: string; label: string } | { kind: 'snapshot'; snapshotId: string; label: string } | null>(null);
  let resetOpen = $state(false);
  let dupOpen = $state(false);
  let snapBusy = $state(false);
  async function snapshotNow() {
    snapBusy = true;
    try {
      const r = await api.snapshotVolume({ siteSlug: slug, volumeSlug: vSlug, body: {} });
      toasts.ok(`Snapshot ${r.snapshotId} taken`, `only volume ${vSlug} · listed under Backups`);
      sv.load();
    } catch (e) {
      toasts.err(e, 'Snapshot failed');
    } finally {
      snapBusy = false;
    }
  }
  // restore source: a copy or an uploaded archive
  let upFile = $state<File | null>(null);
  let upProg = $state<number | null>(null);
  let upErr = $state<string | null>(null);
  async function uploadArchive() {
    if (!upFile) return;
    upErr = null;
    upProg = 0;
    try {
      const { uploadRaw, apiPath } = await import('$lib/api');
      const r = await uploadRaw<{ uploadId: string; sizeBytes: number; entries: number }>('uploadVolumeRestore', apiPath('uploadVolumeRestore', { siteSlug: slug, volumeSlug: vSlug }, { fileName: upFile.name }), upFile, (f) => (upProg = f));
      restoreSrc = { kind: 'upload', uploadId: r.uploadId, label: `${upFile.name} (${bytes(r.sizeBytes)}, ${r.entries} entries)` };
      restorePick = false;
      restoreOpen = true;
    } catch (e) {
      upErr = errMessage(e);
    } finally {
      upProg = null;
    }
  }
  function restoreFrom(c: VolumeCopy) {
    restoreSrc = { kind: 'copy', copyId: c.copyId, label: `copy ${c.path}` };
    restorePick = false;
    restoreOpen = true;
  }
  let delCopy = $state<VolumeCopy | null>(null);
  let delCopyOpen = $state(false);
  async function deleteCopy() {
    if (!delCopy) return;
    await api.deleteVolumeCopy({ siteSlug: slug, copyId: delCopy.copyId, body: { confirm: delCopy.copyId } });
    toasts.ok('Copy deleted', delCopy.path);
    copies.load();
  }

  // ───────── files
  let tree = $state<VolumeTreeResponse | null>(null);
  let treeErr = $state<string | null>(null);
  let treeBusy = $state(false);
  let gen = 0;
  function loadTree() {
    const v = vSlug;
    const p = path;
    if (!v || all || tab !== 'files') return;
    untrack(async () => {
      const my = ++gen;
      treeBusy = true;
      try {
        const r = await tools.volumeTree({ siteSlug: slug, volumeSlug: v, query: { path: p } });
        if (my === gen) ((tree = r), (treeErr = null));
      } catch (e) {
        if (my === gen) treeErr = errMessage(e);
      } finally {
        if (my === gen) treeBusy = false;
      }
    });
  }
  $effect(() => {
    void vSlug;
    void path;
    void tab;
    loadTree();
    selected = [];
  });
  const crumbs = $derived(path.split('/').filter(Boolean).map((seg, i, a) => ({ seg, p: '/' + a.slice(0, i + 1).join('/') })));
  const isFileVol = $derived(cur?.kind === 'file');
  const canWrite = $derived(!runningHere);
  let selected = $state<string[]>([]);
  // viewer
  let fOpen = $state(false);
  let file = $state<VolumeFileResponse | null>(null);
  let fEntry = $state<VolumeEntry | null>(null);
  let fErr = $state<string | null>(null);
  async function openFile(e: VolumeEntry) {
    fEntry = e;
    file = null;
    fErr = null;
    fOpen = true;
    try {
      file = await tools.volumeFile({ siteSlug: slug, volumeSlug: vSlug, query: { path: e.path } });
    } catch (err) {
      fErr = errMessage(err);
    }
  }
  function click(e: VolumeEntry) {
    if (e.type === 'dir') nav({ p: e.path });
    else if (e.type === 'file') openFile(e);
  }
  async function dl(p: string, name: string) {
    try {
      const t = await tools.volumeDownloadTicket({ siteSlug: slug, volumeSlug: vSlug, query: { path: p } });
      await downloadUrl(t.url, name);
      toasts.ok('Download started', name);
    } catch (e) {
      toasts.err(e, 'Download failed');
    }
  }
  const archiveName = $derived(`${vSlug}${path === '/' ? '' : path.replace(/\//g, '_')}.tar.gz`);
  const lines = $derived(file?.text?.replace(/\n$/, '').split('\n') ?? []);
  // editor
  let edOpen = $state(false);
  let edPath = $state<string | null>(null);
  function edit(p: string | null) {
    edPath = p;
    edOpen = true;
    fOpen = false;
  }
  // new folder / move
  let nameOpen = $state(false);
  let nameMode = $state<'mkdir' | 'move'>('mkdir');
  let nameFrom = $state('');
  let nameVal = $state('');
  let nameBusy = $state(false);
  let nameErr = $state<string | null>(null);
  function askName(mode: 'mkdir' | 'move', from = '') {
    nameMode = mode;
    nameFrom = from;
    nameVal = mode === 'move' ? from : `${path === '/' ? '' : path}/`;
    nameErr = null;
    nameOpen = true;
  }
  async function doName(e?: SubmitEvent) {
    e?.preventDefault();
    nameBusy = true;
    nameErr = null;
    try {
      if (nameMode === 'mkdir') await tools.mkdirVolume({ siteSlug: slug, volumeSlug: vSlug, body: { path: nameVal } });
      else await tools.moveVolumeEntry({ siteSlug: slug, volumeSlug: vSlug, body: { from: nameFrom, to: nameVal } });
      nameOpen = false;
      toasts.ok(nameMode === 'mkdir' ? 'Folder created' : 'Moved', nameVal);
      loadTree();
    } catch (err) {
      nameErr = errMessage(err);
    } finally {
      nameBusy = false;
    }
  }
  // delete
  let delOpen = $state(false);
  let delPaths = $state<string[]>([]);
  function askDelete(paths: string[]) {
    delPaths = paths;
    delOpen = true;
  }
  async function doDelete() {
    const r = await tools.deleteVolumeEntries({ siteSlug: slug, volumeSlug: vSlug, body: { paths: delPaths, confirm: 'DELETE' } });
    toasts.ok(`${r.deleted} entries deleted`, delPaths.join(', '));
    selected = [];
    loadTree();
    vols.load();
  }
  // uploads (files, folders, drag-drop)
  type Up = { path: string; file: File; prog: number; state: 'queued' | 'up' | 'done' | 'exists' | 'failed'; err?: string };
  let ups = $state<Up[]>([]);
  let upRunning = $state(false);
  let drag = $state(false);
  let exOpen = $state(false);
  function queue(files: { file: File; rel: string }[]) {
    const basePath = path === '/' ? '' : path;
    for (const f of files) ups.push({ path: `${basePath}/${f.rel}`, file: f.file, prog: 0, state: 'queued' });
    void runUploads(false);
  }
  async function runUploads(overwrite: boolean) {
    if (upRunning) return;
    upRunning = true;
    for (const u of ups) {
      if (u.state !== 'queued') continue;
      u.state = 'up';
      try {
        await uploadToVolume(slug, vSlug, u.path, u.file, overwrite, (f) => (u.prog = f));
        u.state = 'done';
        u.prog = 1;
      } catch (e) {
        if (errCode(e) === 'conflict') u.state = 'exists';
        else ((u.state = 'failed'), (u.err = errMessage(e)));
      }
    }
    upRunning = false;
    loadTree();
    vols.load();
    if (ups.some((u) => u.state === 'exists')) exOpen = true;
    else if (ups.length && ups.every((u) => u.state === 'done')) toasts.ok(`${ups.length} ${ups.length === 1 ? 'file' : 'files'} uploaded`);
  }
  function overwriteExisting() {
    for (const u of ups) if (u.state === 'exists') u.state = 'queued';
    void runUploads(true);
  }
  function pick(e: Event) {
    const inp = e.currentTarget as HTMLInputElement;
    const fs = [...(inp.files ?? [])].map((f) => ({ file: f, rel: (f as File & { webkitRelativePath?: string }).webkitRelativePath || f.name }));
    inp.value = '';
    if (fs.length) queue(fs);
  }
  async function entriesOf(item: FileSystemEntry, prefix: string, out: { file: File; rel: string }[]) {
    if (item.isFile) {
      const f = await new Promise<File>((res, rej) => (item as FileSystemFileEntry).file(res, rej));
      out.push({ file: f, rel: prefix + item.name });
    } else if (item.isDirectory) {
      const rd = (item as FileSystemDirectoryEntry).createReader();
      let batch: FileSystemEntry[] = [];
      do {
        batch = await new Promise<FileSystemEntry[]>((res, rej) => rd.readEntries(res, rej));
        for (const c of batch) await entriesOf(c, `${prefix}${item.name}/`, out);
      } while (batch.length);
    }
  }
  async function drop(e: DragEvent) {
    e.preventDefault();
    drag = false;
    if (!canWrite || isFileVol) return;
    const out: { file: File; rel: string }[] = [];
    const items = [...(e.dataTransfer?.items ?? [])];
    if (items.length && typeof items[0].webkitGetAsEntry === 'function') {
      for (const it of items) {
        const en = it.webkitGetAsEntry();
        if (en) await entriesOf(en, '', out);
      }
    } else for (const f of [...(e.dataTransfer?.files ?? [])]) out.push({ file: f, rel: f.name });
    if (out.length) queue(out);
  }
  let fileInput: HTMLInputElement | undefined = $state();
  let dirInput: HTMLInputElement | undefined = $state();
  let replaceInput: HTMLInputElement | undefined = $state();
  async function replaceFileVolume(e: Event) {
    const inp = e.currentTarget as HTMLInputElement;
    const f = inp.files?.[0];
    inp.value = '';
    if (!f) return;
    try {
      await uploadToVolume(slug, vSlug, '/', f, true);
      toasts.ok('File replaced', vSlug);
      reloadAll();
    } catch (err) {
      toasts.err(err, 'Upload failed');
    }
  }
</script>

<PageHead icon="hardDrive" eyebrow="Docker · {all ? 'all sites' : `site ${slug}`}" title="Volumes" sub="The data volumes of the site: browse and edit files, copy, empty, restore, snapshot." />

{#if all}
  <Load loading={vols.loading} loaded={vols.loaded} error={vols.error} retry={() => vols.load()} rows={5}>
    {@const items = vols.data?.items ?? []}
    {#if items.length === 0}
      <Empty icon="hardDrive" title="No volumes" text="No site declares a data volume. Volumes are declared in Config → Volumes." />
    {:else}
      <section class="card">
        <div class="table-wrap">
          <table class="table" data-testid="all-volumes">
            <thead><tr><th>Site</th><th>Volume</th><th>Kind</th><th class="num">Size</th><th class="num">Files</th><th>Used by</th><th>Backup</th><th class="actions"></th></tr></thead>
            <tbody>
              {#each items as v (`${v.siteSlug}/${v.volumeSlug}`)}
                <tr>
                  <td class="mono">{v.siteSlug}</td>
                  <td><strong class="mono">{v.volumeSlug}</strong><div class="tiny muted">{v.label}</div></td>
                  <td class="small">{v.kind === 'file' ? 'file' : 'folder'}</td>
                  <td class="num">{bytes(v.sizeBytes)}</td>
                  <td class="num small">{v.fileCount ?? '—'}</td>
                  <td class="small">{v.usedBy.join(', ') || '—'}</td>
                  <td>{#if v.backup}<span class="badge ok">backup</span>{:else}<span class="badge">no backup</span>{/if}</td>
                  <td class="actions"><a class="btn sm" href="{base}/app/{v.siteSlug}/volumes?v={v.volumeSlug}" data-testid="manage-{v.siteSlug}-{v.volumeSlug}">Manage</a></td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </section>
    {/if}
  </Load>
{:else}
  <Load loading={vols.loading} loaded={vols.loaded} error={vols.error} retry={() => vols.load()} rows={5}>
    {#if (vols.data?.items ?? []).length === 0}
      <Empty icon="hardDrive" title="No volumes" text="Site {slug} declares no data volume. Volumes are declared in Config → Volumes." />
    {:else}
      <div class="lay">
        <nav class="vlist" aria-label="Volumes" data-testid="volume-list">
          {#each vols.data?.items ?? [] as v (v.volumeSlug)}
            <button class="vol" class:on={v.volumeSlug === vSlug} onclick={() => nav({ v: v.volumeSlug, p: '/' })} data-testid="vol-{v.volumeSlug}">
              <span class="vt"><Icon name={v.kind === 'file' ? 'file' : 'hardDrive'} size={15} /><strong class="mono ellipsis">{v.volumeSlug}</strong>{#if busyOf(v.volumeSlug)}<span class="badge warn" title="an action is running">busy</span>{:else if !v.backup}<span class="badge" title="not in snapshots">no backup</span>{/if}</span>
              <span class="vl small muted ellipsis">{v.label}</span>
              <span class="vs"><b>{v.sizeBytes === null ? '—' : bytes(v.sizeBytes)}{v.sizeTruncated ? '+' : ''}</b><span class="faint">{v.fileCount ?? '—'} files</span></span>
              <span class="vu tiny faint ellipsis">used by {v.usedBy.join(', ') || 'no container'}</span>
              {#if syncOf(v.volumeSlug)}{@const ss = syncOf(v.volumeSlug)!}<span class="vsync"><span class="badge {SYNC_TONE[ss.state]}" title="Persist to cloud: {SYNC_LABEL[ss.state]}" data-testid="sync-badge-{v.volumeSlug}" data-state={ss.state}>{SYNC_LABEL[ss.state]}</span></span>{/if}
            </button>
          {/each}
          <div class="total tiny muted">Total {bytes((vols.data?.items ?? []).reduce((a, v) => a + (v.sizeBytes ?? 0), 0))}</div>
        </nav>

        {#if cur}
          <section class="card main" data-testid="vol-panel">
            <div class="card-head vhead">
              <div class="grow" style="min-width:0">
                <h2 class="mono ellipsis">{cur.volumeSlug}</h2>
                <div class="tiny muted ellipsis">{cur.label || 'no label'} · {cur.kind === 'file' ? 'a single file' : 'a folder'}</div>
              </div>
              <div class="acts">
                <Btn size="sm" icon="archive" busy={snapBusy} disabled={!canWrite} onclick={snapshotNow} data-testid="vol-snapshot">Snapshot now</Btn>
                <Btn size="sm" icon="download" onclick={() => dl('/', isFileVol ? cur!.volumeSlug : `${cur!.volumeSlug}.tar.gz`)} data-testid="vol-download">Download{isFileVol ? '' : ' .tar.gz'}</Btn>
                <Btn size="sm" icon="copy" disabled={!canWrite} onclick={() => (copyOpen = true)} data-testid="vol-copy">Copy…</Btn>
                <Btn size="sm" icon="rotate" disabled={!canWrite} onclick={() => ((upErr = null), (upFile = null), (restorePick = true))} data-testid="vol-restore">Restore…</Btn>
                <Btn size="sm" variant="danger" icon="eraser" disabled={!canWrite} onclick={() => (emptyOpen = true)} data-testid="vol-empty">Empty…</Btn>
              </div>
            </div>
            {#if runningHere}
              <div class="banner info small busy" data-testid="vol-busy"><span class="spin"></span><div class="grow"><strong>{OP_LABEL[runningHere.kind]}</strong> running ({runningHere.phase}) — files and other actions wait until it ends.</div><Btn size="sm" onclick={() => showOp(runningHere!)}>View progress</Btn></div>
            {:else if updateRunning}
              <div class="banner warn small busy" data-testid="vol-update-running"><Icon name="alert" /><div class="grow">An update is running on this site — you can still change files; the update may restart the containers that use this volume.</div></div>
            {/if}
            <div class="tabs" role="tablist">
              {#each [['overview', 'Overview'], ['files', isFileVol ? 'File' : 'Files'], ...(curSync ? [['sync', 'Cloud sync']] : []), ['backups', `Backups${backups?.items.length ? ` (${backups.items.length})` : ''}`], ['copies', `Copies${volCopies.length ? ` (${volCopies.length})` : ''}`], ['history', 'History']] as [k, l] (k)}
                <button role="tab" aria-selected={tab === k} onclick={() => nav({ tab: k })} data-testid="vtab-{k}">{l}</button>
              {/each}
            </div>

            {#if tab === 'overview'}
              <div class="card-body stack" data-testid="vol-overview">
                <div class="tiles">
                  <div class="tile"><span class="tl">Size</span><b data-testid="ov-size">{bytes(cur.sizeBytes)}{cur.sizeTruncated ? '+' : ''}</b></div>
                  <div class="tile"><span class="tl">Files</span><b>{cur.fileCount ?? '—'}</b></div>
                  <div class="tile"><span class="tl">Kind</span><b>{cur.kind === 'file' ? 'file' : 'folder'}</b></div>
                  <div class="tile"><span class="tl">Backup</span><b>{cur.backup ? 'in snapshots' : 'not backed up'}</b></div>
                </div>
                <dl class="kv">
                  <dt>Location</dt><dd class="pathc"><CopyId value={cur.hostPath} short={0} label="path" /></dd>
                  <dt>Last snapshot</dt><dd>{#if st?.lastSnapshot}<a href="{base}/app/{slug}/backups">{st.lastSnapshot.snapshotId}</a> · <Time ts={st.lastSnapshot.createdAt} /> · {bytes(st.lastSnapshot.sizeBytes)}{:else}<span class="muted">none yet</span>{/if}</dd>
                  {#if curSync}
                    <dt>Persist to cloud</dt>
                    <dd><span class="badge {SYNC_TONE[curSync.state]}" data-testid="ov-sync">{SYNC_LABEL[curSync.state]}</span> <button class="linklike small" onclick={() => nav({ tab: 'sync' })} data-testid="ov-sync-open">Cloud sync…</button></dd>
                  {/if}
                  <dt>Starter content</dt>
                  <dd>
                    {#if st?.seed}
                      <SeedView seed={st.seed} compact />{#if st.seeded}&nbsp;· applied <Time ts={st.seeded.at} /> ({st.seeded.entries} entries){:else} · not applied{/if}
                      {#if isSelf}<Btn size="xs" icon="rotate" disabled={!canWrite} onclick={() => (resetOpen = true)} data-testid="vol-reset">Reset to starter content…</Btn>{:else}<span class="tiny muted">(reset from site {slug}'s own agent GUI)</span>{/if}
                    {:else}<span class="muted">none (starts empty)</span>{/if}
                  </dd>
                  <dt>Duplicate</dt><dd><button class="linklike small" onclick={() => (dupOpen = true)} data-testid="vol-duplicate">Duplicate as a new data volume…</button></dd>
                </dl>
                <div>
                  <div class="section-title">Used by</div>
                  {#if users.length === 0}
                    <p class="muted small">No container mounts this volume.</p>
                  {:else}
                    <div class="table-wrap">
                      <table class="table compact" data-testid="vol-users">
                        <thead><tr><th>Container</th><th>State</th><th>Image</th><th>Mounted at</th><th></th></tr></thead>
                        <tbody>
                          {#each users as u (u.slug + u.mountPath)}
                            <tr>
                              <td class="mono"><a href="{base}/app/{slug}/containers/{u.slug}">{u.slug}</a></td>
                              <td><span class="badge {stateTone(u.state)}" data-testid="user-state-{u.slug}">{u.state}</span>{#if u.health !== 'none'} <span class="tiny muted">{u.health}</span>{/if}</td>
                              <td class="mono small">{u.image || '—'}</td>
                              <td class="mono small">{u.mountPath}</td>
                              <td class="small">{u.readOnly ? 'read-only' : ''}</td>
                            </tr>
                          {/each}
                        </tbody>
                      </table>
                    </div>
                  {/if}
                </div>
              </div>
            {:else if tab === 'files'}
              {#if isFileVol}
                <div class="card-body stack">
                  <p class="small muted">This volume is a single file mounted into the container{users.length ? ` (${users.map((u) => u.mountPath).join(', ')})` : ''}.</p>
                  <div class="row">
                    <Btn icon="edit" disabled={!canWrite} onclick={() => edit('/')} data-testid="file-vol-edit">Edit</Btn>
                    <Btn icon="upload" disabled={!canWrite} onclick={() => replaceInput?.click()}>Replace with a file…</Btn>
                    <Btn icon="download" onclick={() => dl('/', cur!.volumeSlug)}>Download</Btn>
                  </div>
                  <input bind:this={replaceInput} type="file" hidden onchange={replaceFileVolume} />
                </div>
              {:else}
                <div class="ftool">
                  <nav class="bc mono" aria-label="Path" data-testid="vol-path">
                    <button onclick={() => nav({ p: '/' })} class:last={!crumbs.length}><Icon name="hardDrive" size={13} />{cur.volumeSlug}</button>
                    {#each crumbs as c, i (c.p)}<span class="sl">/</span><button onclick={() => nav({ p: c.p })} class:last={i === crumbs.length - 1}>{c.seg}</button>{/each}
                  </nav>
                  <div class="acts">
                    {#if selected.length}<Btn size="sm" variant="danger" icon="trash" disabled={!canWrite} onclick={() => askDelete(selected)} data-testid="del-selected">Delete {selected.length}</Btn>{/if}
                    <Btn size="sm" icon="upload" disabled={!canWrite} onclick={() => fileInput?.click()} data-testid="up-files">Upload files</Btn>
                    <Btn size="sm" icon="upload" disabled={!canWrite} onclick={() => dirInput?.click()} data-testid="up-folder">Upload folder</Btn>
                    <Btn size="sm" icon="folderPlus" disabled={!canWrite} onclick={() => askName('mkdir')} data-testid="new-folder">New folder</Btn>
                    <Btn size="sm" icon="filePlus" disabled={!canWrite} onclick={() => edit(null)} data-testid="new-file">New file</Btn>
                    <Btn size="sm" icon="download" onclick={() => dl(path, archiveName)} data-testid="vol-download-dir">{path === '/' ? 'Volume' : 'Folder'} .tar.gz</Btn>
                  </div>
                  <input bind:this={fileInput} type="file" multiple hidden onchange={pick} data-testid="up-files-input" />
                  <input bind:this={dirInput} type="file" hidden webkitdirectory onchange={pick} data-testid="up-folder-input" />
                </div>
                <div class="hostp tiny faint mono" title="Directory on the host">{cur.hostPath}{path === '/' ? '' : path}</div>
                {#if ups.length}
                  <div class="ups" data-testid="uploads">
                    {#each ups as u, i (i)}
                      <div class="up small"><span class="mono ellipsis grow">{u.path}</span><span class="faint">{bytes(u.file.size)}</span><span class="badge {u.state === 'done' ? 'ok' : u.state === 'failed' ? 'bad' : u.state === 'exists' ? 'warn' : 'info'}">{u.state === 'exists' ? 'exists' : u.state}</span>{#if u.state === 'up'}<span class="pb"><Progress value={u.prog} label="Upload" /></span>{/if}{#if u.err}<span class="bad-text tiny">{u.err}</span>{/if}</div>
                    {/each}
                    {#if !upRunning}<button class="linklike tiny" onclick={() => (ups = [])}>Clear</button>{/if}
                  </div>
                {/if}
                <div class="dropzone" class:drag role="region" aria-label="Files of {cur.volumeSlug} (drop files or folders here to upload)" ondragover={(e) => { e.preventDefault(); if (canWrite) drag = true; }} ondragleave={() => (drag = false)} ondrop={drop} data-testid="dropzone">
                  {#if treeErr}
                    <div class="card-body"><div class="banner bad small"><Icon name="alert" /><div class="grow">{treeErr}</div></div></div>
                  {:else if !tree || (treeBusy && tree.path !== path)}
                    <div class="card-body"><span class="skel" style="height:180px"></span></div>
                  {:else if tree.entries.length === 0}
                    <Empty icon="folder" title="Empty folder" text="Nothing in {path}. Drop files or folders here, or use Upload." compact />
                  {:else}
                    <div class="table-wrap">
                      <table class="table compact" data-testid="vol-entries">
                        <thead><tr><th class="ck"><input type="checkbox" aria-label="Select all" checked={selected.length === tree.entries.length && tree.entries.length > 0} onchange={(e) => (selected = (e.currentTarget as HTMLInputElement).checked ? tree!.entries.map((x) => x.path) : [])} /></th><th>Name</th><th class="num">Size</th><th>Modified</th><th>Mode</th><th class="actions"></th></tr></thead>
                        <tbody>
                          {#if path !== '/'}
                            <tr class="clickable" onclick={() => nav({ p: crumbs.length > 1 ? crumbs[crumbs.length - 2].p : '/' })}><td></td><td colspan="5" class="small muted"><Icon name="arrowUp" size={13} /> ..</td></tr>
                          {/if}
                          {#each tree.entries as e (e.path)}
                            <tr class:clickable={e.type !== 'symlink' && e.type !== 'other'} onclick={() => click(e)} data-testid="entry-{e.name}">
                              <td class="ck"><input type="checkbox" aria-label="Select {e.name}" checked={selected.includes(e.path)} onclick={(ev) => ev.stopPropagation()} onchange={(ev) => (selected = (ev.currentTarget as HTMLInputElement).checked ? [...selected, e.path] : selected.filter((x) => x !== e.path))} data-testid="sel-{e.name}" /></td>
                              <td>
                                <span class="nm">
                                  <Icon name={e.type === 'dir' ? 'folder' : e.type === 'symlink' ? 'link' : 'file'} size={15} class={e.type === 'dir' ? 'dir' : ''} />
                                  <span class="mono">{e.name}</span>
                                  {#if e.linkTarget}<span class="faint small">→ {e.linkTarget}</span>{/if}
                                  {#if e.type === 'file' && !e.textLike}<span class="badge" title="binary or larger than 1 MiB: download only">download only</span>{/if}
                                </span>
                              </td>
                              <td class="num small">{e.sizeBytes === null ? '—' : bytes(e.sizeBytes)}</td>
                              <td class="small nowrap"><Time ts={e.mtime} /></td>
                              <td class="mono xs faint">{e.mode}</td>
                              <td class="actions nowrap">
                                {#if e.type === 'file' && e.textLike}<button class="btn ghost icon xs" title="Edit" aria-label="Edit {e.name}" disabled={!canWrite} onclick={(ev) => (ev.stopPropagation(), edit(e.path))} data-testid="edit-{e.name}"><Icon name="edit" size={14} /></button>{/if}
                                <button class="btn ghost icon xs" title="Rename / move" aria-label="Rename {e.name}" disabled={!canWrite} onclick={(ev) => (ev.stopPropagation(), askName('move', e.path))} data-testid="mv-{e.name}"><Icon name="arrowRight" size={14} /></button>
                                {#if e.type === 'file' || e.type === 'dir'}
                                  <button class="btn ghost icon xs" title="Download" aria-label="Download {e.name}" onclick={(ev) => (ev.stopPropagation(), dl(e.path, e.type === 'dir' ? `${vSlug}${e.path.replace(/\//g, '_')}.tar.gz` : e.name))} data-testid="dl-{e.name}"><Icon name="download" size={14} /></button>
                                {/if}
                                <button class="btn ghost icon xs" title="Delete" aria-label="Delete {e.name}" disabled={!canWrite} onclick={(ev) => (ev.stopPropagation(), askDelete([e.path]))} data-testid="rm-{e.name}"><Icon name="trash" size={14} /></button>
                              </td>
                            </tr>
                          {/each}
                        </tbody>
                      </table>
                    </div>
                    {#if tree.truncated}<p class="faint xs" style="padding:0 14px">Only the first 2000 entries are listed.</p>{/if}
                  {/if}
                  {#if drag}<div class="dropover"><Icon name="upload" size={22} />Drop to upload into {path}</div>{/if}
                </div>
              {/if}
            {:else if tab === 'sync'}
              {#if curSync}
                <SyncPanel siteSlug={slug} status={curSync} {canWrite} onchanged={() => (sync.load(), syncOne.load(), sv.load(), ops.load(), vols.load())} onfiles={() => nav({ tab: 'files', p: '/' })} />
              {:else if !sync.loaded && !sync.error}
                <div class="card-body"><span class="skel" style="height:180px"></span></div>
              {:else}
                <div class="card-body"><Empty icon="hardDrive" title="Not persisted to the cloud" text="Volume {vSlug} is not a persist-to-cloud volume (set in the cloud console, agent 0.12 or newer)." compact /></div>
              {/if}
            {:else if tab === 'backups'}
              <!-- §21 #112 every snapshot on this site that holds the volume; restore the volume from any of them -->
              <div class="card-body stack" data-testid="vol-backups">
                {#if backups}
                  <p class="small muted" style="margin:0" data-testid="vol-backup-policy">
                    {#if !backups.explicit}Backed up with the <strong>daily site snapshot</strong> (7 kept){:else if backups.policy.schedule === 'off'}Automatic backups <strong>off</strong> — a backup is still taken before every change{:else}Automatic backup <strong>{backups.policy.schedule === '6h' ? 'every 6 hours' : backups.policy.schedule}</strong>, the {backups.policy.keep} newest kept{/if}{#if backups.nextAt} · next <Time ts={backups.nextAt} />{/if}.
                    Change it in the volume settings (Config › Volumes). Restoring takes a snapshot of the current data first, so it can be undone.
                  </p>
                  {#if backups.items.length === 0}
                    <Empty icon="archive" title="No backup yet" text="Snapshots that hold {vSlug} show here — automatic ones, the daily site snapshot, before updates and before changes (Snapshot now)." compact />
                  {:else}
                    <div class="table-wrap">
                      <table class="table compact">
                        <thead><tr><th>Taken</th><th>Kind</th><th>Note</th><th class="num">Size</th><th>By</th><th class="actions"></th></tr></thead>
                        <tbody>
                          {#each backups.items as b (b.snapshotId)}
                            <tr data-testid="backup-{b.snapshotId}">
                              <td class="small nowrap"><Time ts={b.createdAt} /></td>
                              <td class="small"><span class="badge {b.auto ? 'accent' : ''}">{BACKUP_REASON[b.reason] ?? b.reason}</span></td>
                              <td class="small muted">{b.note}</td>
                              <td class="num small">{bytes(b.sizeBytes)}</td>
                              <td class="small">{b.createdBy}</td>
                              <td class="actions nowrap"><Btn size="xs" icon="rotate" disabled={!canWrite} onclick={() => restoreFromBackup(b)} data-testid="restore-backup-{b.snapshotId}">Restore from it…</Btn></td>
                            </tr>
                          {/each}
                        </tbody>
                      </table>
                    </div>
                  {/if}
                {:else if backupsErr}
                  <div class="banner bad small"><Icon name="alert" size={14} /><div class="grow">{backupsErr}</div><Btn size="xs" onclick={loadBackups}>Retry</Btn></div>
                {:else}
                  <span class="skel" style="height:120px"></span>
                {/if}
              </div>
            {:else if tab === 'copies'}
              <div class="card-body stack" data-testid="vol-copies">
                <p class="small muted" style="margin:0">Copies made with <strong>Copy → a folder on the server</strong>. They stay on the server until you delete them; restore the volume from one at any time.</p>
                {#if volCopies.length === 0}
                  <Empty icon="copy" title="No copy yet" text="Copy… writes the volume to {agentHome}/volume-copies/ (or a path you choose)." compact />
                {:else}
                  <div class="table-wrap">
                    <table class="table compact">
                      <thead><tr><th>Path</th><th class="num">Size</th><th class="num">Files</th><th>Made</th><th>By</th><th class="actions"></th></tr></thead>
                      <tbody>
                        {#each volCopies as c (c.copyId)}
                          <tr class:dim={!c.exists} data-testid="copy-{c.copyId}">
                            <td class="mono small pathc">{c.path}{#if !c.exists} <span class="badge bad">missing</span>{/if}{#if !c.inCopiesDir} <span class="badge" title="outside volume-copies/">custom path</span>{/if}</td>
                            <td class="num small">{bytes(c.sizeBytes)}</td>
                            <td class="num small">{c.files ?? '—'}</td>
                            <td class="small nowrap"><Time ts={c.createdAt} /></td>
                            <td class="small">{c.createdBy}</td>
                            <td class="actions nowrap">
                              <Btn size="xs" icon="rotate" disabled={!c.exists || !canWrite} onclick={() => restoreFrom(c)} data-testid="restore-from-{c.copyId}">Restore from it…</Btn>
                              <button class="btn ghost icon xs" title="Delete this copy" aria-label="Delete copy {c.path}" onclick={() => ((delCopy = c), (delCopyOpen = true))} data-testid="del-copy-{c.copyId}"><Icon name="trash" size={14} /></button>
                            </td>
                          </tr>
                        {/each}
                      </tbody>
                    </table>
                  </div>
                {/if}
                {#if (copies.data?.items.length ?? 0) > volCopies.length}<p class="tiny muted" style="margin:0">{(copies.data?.items.length ?? 0) - volCopies.length} more copies of other volumes on {slug}.</p>{/if}
              </div>
            {:else}
              <div class="card-body" data-testid="vol-history">
                {#if volOps.length === 0}
                  <Empty icon="history" title="No action yet" text="Empty, Copy and Restore of this volume show here (also as activities)." compact />
                {:else}
                  <div class="table-wrap">
                    <table class="table compact">
                      <thead><tr><th>Action</th><th>By</th><th>Started</th><th>Took</th><th>Result</th></tr></thead>
                      <tbody>
                        {#each volOps as o (o.opId)}
                          <tr class="clickable" onclick={() => showOp(o)} data-testid="hist-{o.opId}">
                            <td><strong>{OP_LABEL[o.kind]}</strong>{#if o.kind === 'copy' && o.target}<span class="tiny muted"> → {o.target.volumeSlug ?? o.target.path}</span>{/if}{#if o.fastForward} <span class="badge accent" title="the wait was fast-forwarded">⏩ ×{o.fastForward.maxSpeed}</span>{/if}</td>
                            <td class="small">{o.actor}</td>
                            <td class="small nowrap">{fmtDateTime(o.startedAt)}</td>
                            <td class="small">{o.durationMs !== null ? duration(o.durationMs) : '…'}</td>
                            <td>{#if !o.finishedAt}<span class="badge info">{o.phase}</span>{:else if o.ok}<span class="badge ok">done</span> <span class="tiny muted">{bytes(o.resultBytes)}</span>{:else}<span class="badge bad">failed</span> <span class="tiny bad-text">{o.error}</span>{/if}</td>
                          </tr>
                        {/each}
                      </tbody>
                    </table>
                  </div>
                {/if}
              </div>
            {/if}
          </section>
        {:else}
          <section class="card"><div class="card-body"><span class="skel" style="height:180px"></span></div></section>
        {/if}
      </div>
    {/if}
  </Load>
{/if}

<Drawer bind:open={fOpen} wide title={fEntry?.name ?? 'File'} subtitle="{vSlug}:{fEntry?.path ?? ''}{file ? ` · ${bytes(file.sizeBytes)} · ${fmtDateTime(file.mtime)}` : ''}" testid="file-view">
  {#if fErr}
    <div class="banner bad small"><Icon name="alert" /><div class="grow">{fErr}</div></div>
  {:else if !file}
    <span class="skel" style="height:300px"></span>
  {:else if file.text === null}
    <Empty icon="file" title={file.binary ? 'Binary file' : 'Too large to show'} text={file.binary ? 'This file is not text. Download it to open it with the right program.' : 'Files up to 1 MiB are shown here. Download it instead.'} compact />
  {:else}
    <div class="code-view" data-testid="file-text">
      {#each lines as l, i (i)}<div class="cl"><span class="no">{i + 1}</span><span class="tx">{l}</span></div>{/each}
    </div>
  {/if}
  {#snippet footer()}
    {#if fEntry && file?.text !== null && file}<Btn icon="edit" disabled={!canWrite} onclick={() => edit(fEntry!.path)} data-testid="file-edit">Edit</Btn>{/if}
    {#if fEntry}<Btn icon="download" onclick={() => fEntry && dl(fEntry.path, fEntry.name)} data-testid="file-download">Download</Btn>{/if}
  {/snippet}
</Drawer>

{#if cur}
  <FileEditor bind:open={edOpen} siteSlug={slug} volumeSlug={cur.volumeSlug} path={edPath} dir={path} onsaved={(p) => (toasts.ok('Saved', p), loadTree(), vols.load())} />
  <CopyDrawer bind:open={copyOpen} siteSlug={slug} volumeSlug={cur.volumeSlug} kind={cur.kind} {agentHome} users={users.map((u) => ({ slug: u.slug, image: u.image }))} {others} onstarted={showOp} />
  <EmptyFlow bind:open={emptyOpen} siteSlug={slug} volumeSlug={cur.volumeSlug} kind={cur.kind} users={userSlugs} onstarted={showOp} />
  <RestoreFlow bind:open={restoreOpen} siteSlug={slug} volumeSlug={cur.volumeSlug} source={restoreSrc} users={userSlugs} onstarted={showOp} />
  {#if resetOpen}<ResetVolumeFlow bind:open={resetOpen} siteSlug={slug} volumeSlug={cur.volumeSlug} ondone={(r) => (toasts.ok(`Volume ${r.volumeSlug} reset`, `${r.entries} entries written · snapshot ${r.snapshotId}`), reloadAll())} />{/if}
{/if}
<OpProgress bind:open={opOpen} bind:op ondone={opDone} />

<Drawer bind:open={restorePick} title="Restore {vSlug}" subtitle="choose what to restore from" testid="restore-pick">
  <div class="stack">
    <div class="section-title">From a copy on the server</div>
    {#if volCopies.filter((c) => c.exists).length === 0}
      <p class="muted small">No copy of {vSlug} on {slug} yet.</p>
    {:else}
      {#each volCopies.filter((c) => c.exists) as c (c.copyId)}
        <button class="pickrow" onclick={() => restoreFrom(c)} data-testid="pick-copy-{c.copyId}"><Icon name="copy" size={15} /><span class="grow"><span class="mono small pathc">{c.path}</span><span class="tiny muted">{bytes(c.sizeBytes)} · {fmtDateTime(c.createdAt)} · {c.createdBy}</span></span><Icon name="chevronRight" size={14} /></button>
      {/each}
    {/if}
    <div class="section-title">From a .tar.gz</div>
    <p class="tiny muted" style="margin:0">An archive made by Download .tar.gz (or <span class="mono">tar czf</span> of the folder's contents), up to 2 GiB. Bigger data: copy it to the server first and restore from a copy.</p>
    <input type="file" accept=".gz,.tgz,application/gzip" onchange={(e) => (upFile = (e.currentTarget as HTMLInputElement).files?.[0] ?? null)} data-testid="restore-file" />
    {#if upProg !== null}<Progress value={upProg} label="Upload" />{/if}
    {#if upErr}<div class="banner bad small"><Icon name="alert" /><div class="grow">{upErr}</div></div>{/if}
    <p class="tiny muted" style="margin:0">Then: 60 s wait (⏩ fast-forward), the volume name typed, its containers stop, a snapshot of the volume is taken, the contents are replaced, the containers start again.</p>
  </div>
  {#snippet footer()}
    <Btn onclick={() => (restorePick = false)}>Cancel</Btn>
    <Btn variant="primary" icon="upload" disabled={!upFile} busy={upProg !== null} onclick={uploadArchive} data-testid="restore-upload">Upload and continue</Btn>
  {/snippet}
</Drawer>

<Modal bind:open={nameOpen} title={nameMode === 'mkdir' ? 'New folder' : 'Rename / move'} size="sm" testid="name-modal">
  <form id="name-form" onsubmit={doName} class="stack">
    <div class="field">
      <label for="name-in">{nameMode === 'mkdir' ? 'Folder path' : 'New path'} <span class="faint">(inside {vSlug})</span></label>
      <input id="name-in" class="input mono" bind:value={nameVal} autocomplete="off" spellcheck="false" data-testid="name-input" />
      {#if nameMode === 'move'}<div class="tiny muted">From <span class="mono">{nameFrom}</span>. Missing folders are created.</div>{/if}
    </div>
    {#if nameErr}<div class="banner bad small"><Icon name="alert" /><div class="grow">{nameErr}</div></div>{/if}
  </form>
  {#snippet footer()}
    <Btn onclick={() => (nameOpen = false)}>Cancel</Btn>
    <Btn variant="primary" type="submit" form="name-form" busy={nameBusy} disabled={!nameVal.trim() || nameVal.trim() === '/' || nameVal === nameFrom} data-testid="name-go">{nameMode === 'mkdir' ? 'Create' : 'Move'}</Btn>
  {/snippet}
</Modal>

<Confirm bind:open={delOpen} danger title="Delete {delPaths.length === 1 ? delPaths[0] : `${delPaths.length} entries`}?" typed="DELETE" confirmLabel="Delete" onconfirm={doDelete} testid="del-confirm">
  <p class="small">Deletes from volume <strong class="mono">{vSlug}</strong> on site {slug} (folders with everything inside). There is no undo — take a snapshot or a copy first if you may need it.</p>
  <ul class="small mono">{#each delPaths.slice(0, 8) as p (p)}<li>{p}</li>{/each}{#if delPaths.length > 8}<li>… {delPaths.length - 8} more</li>{/if}</ul>
</Confirm>

<Confirm bind:open={exOpen} title="Some files already exist" confirmLabel="Overwrite them" onconfirm={overwriteExisting} testid="exists-confirm">
  <p class="small">{ups.filter((u) => u.state === 'exists').length} uploaded files already exist in {vSlug}. Overwrite them?</p>
</Confirm>

<Confirm bind:open={delCopyOpen} danger title="Delete this copy?" confirmLabel="Delete copy" onconfirm={deleteCopy} testid="del-copy-confirm">
  <p class="small">Deletes <span class="mono">{delCopy?.path}</span> ({bytes(delCopy?.sizeBytes ?? null)}) from the server. The volume itself is not touched.</p>
</Confirm>

<Modal bind:open={dupOpen} title="Duplicate {vSlug} as a new data volume" size="md" testid="dup-modal">
  <ol class="small dup">
    <li>Declare the new volume in <a href="{base}/app/{slug}/config/volumes">Config → Volumes</a> (same kind: {cur?.kind === 'file' ? 'file' : 'folder'}) and commit it.</li>
    <li>Approve it in Diff — the new volume exists (empty) on {slug}.</li>
    <li>Here: <strong>Copy… → Another volume</strong> → the new volume (stop the containers for a consistent copy).</li>
    <li>Point containers at the new volume in Config when you want them to use it.</li>
  </ol>
  <p class="tiny muted">Declaring volumes is a configuration change, so it goes through a commit and the apply engine; the data copy is a separate, logged action.</p>
  {#snippet footer()}
    <Btn onclick={() => (dupOpen = false)}>Close</Btn>
    <a class="btn primary" href="{base}/app/{slug}/config/volumes" data-testid="dup-config">Open Config → Volumes</a>
  {/snippet}
</Modal>

<style>
  .lay {
    display: grid;
    grid-template-columns: 260px minmax(0, 1fr);
    gap: 14px;
    align-items: start;
  }
  @media (max-width: 900px) {
    .lay {
      grid-template-columns: minmax(0, 1fr);
    }
    .vlist {
      flex-direction: row !important;
      overflow-x: auto;
    }
    .vlist .vol {
      flex: none;
      width: 230px;
    }
    .total {
      display: none;
    }
  }
  .vlist {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .vol {
    all: unset;
    box-sizing: border-box;
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: var(--r-lg);
    background: var(--surface);
    cursor: pointer;
    min-width: 0;
  }
  .vol:hover {
    border-color: var(--border-strong);
  }
  .vol.on {
    border-color: var(--accent);
    box-shadow: 0 0 0 3px var(--accent-soft);
  }
  .vol:focus-visible {
    outline: 2px solid var(--accent);
  }
  .vt {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }
  .vt .badge {
    margin-left: auto;
  }
  .vs {
    display: flex;
    gap: 8px;
    align-items: baseline;
    font-size: var(--fs-sm);
    margin-top: 4px;
  }
  .vs b {
    font-size: var(--fs-lg);
    font-variant-numeric: tabular-nums;
  }
  .total {
    padding: 2px 4px;
  }
  .vsync {
    margin-top: 4px;
  }
  .main {
    min-width: 0;
  }
  .vhead {
    flex-wrap: wrap;
    gap: 10px;
  }
  .vhead h2 {
    margin: 0;
  }
  .acts {
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
  }
  .busy {
    margin: 0 16px 10px;
  }
  .tabs {
    padding: 0 16px;
  }
  .tiles {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(130px, 1fr));
    gap: 10px;
  }
  .tile {
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    padding: 10px 12px;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .tl {
    font-size: var(--fs-xs);
    color: var(--text-3);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .tile b {
    font-size: var(--fs-lg);
    font-variant-numeric: tabular-nums;
  }
  .pathc {
    overflow-wrap: anywhere;
  }
  .ftool {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    justify-content: space-between;
    align-items: center;
    padding: 10px 16px;
    border-bottom: 1px solid var(--border);
  }
  .bc {
    display: flex;
    align-items: center;
    gap: 2px;
    flex-wrap: wrap;
    font-size: var(--fs-sm);
    min-width: 0;
  }
  .bc button {
    all: unset;
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 2px 5px;
    border-radius: var(--r-sm);
    color: var(--accent-text);
  }
  .bc button:hover {
    background: var(--surface-hover);
  }
  .bc button.last {
    color: var(--text);
    font-weight: var(--fw-semibold);
  }
  .sl {
    color: var(--text-3);
  }
  .hostp {
    padding: 6px 16px;
    border-bottom: 1px solid var(--border);
    overflow-wrap: anywhere;
  }
  .ups {
    padding: 8px 16px;
    border-bottom: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .up {
    display: flex;
    gap: 8px;
    align-items: center;
    min-width: 0;
  }
  .pb {
    width: 120px;
    flex: none;
  }
  .dropzone {
    position: relative;
    min-height: 120px;
  }
  .dropzone.drag {
    outline: 2px dashed var(--accent);
    outline-offset: -6px;
  }
  .dropover {
    position: absolute;
    inset: 0;
    display: grid;
    place-content: center;
    justify-items: center;
    gap: 6px;
    background: color-mix(in srgb, var(--accent-soft) 80%, transparent);
    color: var(--accent-text);
    font-weight: var(--fw-semibold);
    pointer-events: none;
  }
  .ck {
    width: 28px;
  }
  .nm {
    display: inline-flex;
    align-items: center;
    gap: 8px;
  }
  .nm :global(.dir) {
    color: var(--amber);
  }
  .code-view {
    font-family: var(--font-mono);
    font-size: 12px;
    line-height: 1.6;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    background: var(--code-bg);
    overflow: auto;
    max-height: 72vh;
    padding: 6px 0;
  }
  .cl {
    display: flex;
    white-space: pre;
  }
  .no {
    flex: none;
    width: 44px;
    padding-right: 12px;
    text-align: right;
    color: var(--text-3);
    user-select: none;
  }
  .tx {
    padding-right: 12px;
  }
  .pickrow {
    all: unset;
    box-sizing: border-box;
    display: flex;
    gap: 10px;
    align-items: center;
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    cursor: pointer;
  }
  .pickrow:hover {
    border-color: var(--accent);
  }
  .pickrow .grow {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .linklike {
    all: unset;
    cursor: pointer;
    color: var(--accent-text);
    text-decoration: underline;
  }
  .dup {
    padding-left: 18px;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
</style>
