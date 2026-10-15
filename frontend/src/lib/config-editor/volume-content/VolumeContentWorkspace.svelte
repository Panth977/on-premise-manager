<script lang="ts">
  /**
   * §21 #101 the reusable volume content workspace (full page body): toolbar + file tree (FileTree) + CodeMirror editor
   * of the open file (language by extension, mode + render per file, binary / > 2 MiB = size + download), uploads of
   * files AND folders (inputs + drops; relative paths kept; conflict prompt overwrite / skip / keep both), limits
   * (VOLUME_CONTENT_LIMITS) and tree issues. Writes nothing to the server: a LIVE `ws` (starter content, §21 #107) puts
   * every edit into the scope's change set at once (Commit… uploads + commits); a version `ws` is saved by Cloud copy.
   * `single`: a single-file volume = one editor, no tree.
   */
  import type { FileRender } from '@opm/shared';
  import { VOLUME_CONTENT_LIMITS, baseName, contentPathError, duplicateName, formatContentBytes, formatMode, joinPath, languageForPath, parentDir, treeHasDir, treeHasFile } from '@opm/shared';
  import Button from '../../ui/Button.svelte';
  import Modal from '../../ui/Modal.svelte';
  import Field from '../../ui/Field.svelte';
  import Toggle from '../../ui/Toggle.svelte';
  import Select from '../../ui/Select.svelte';
  import ConfirmDialog from '../../ui/ConfirmDialog.svelte';
  import Icon from '../../ui/Icon.svelte';
  import Spinner from '../../ui/Spinner.svelte';
  import { FilePlus, FolderPlus, Upload, FolderUp, Pencil, FolderInput, Copy, Trash2, Download, TriangleAlert, FileText, CircleAlert, ChevronLeft } from '../../ui/icons';
  import { toast } from '../../toast.svelte';
  import { saveAs } from '../files';
  import FileTree, { type TreeAction } from './FileTree.svelte';
  import ContentCodeEditor from './ContentCodeEditor.svelte';
  import { LANG_LABEL, allDirs, pickedFromInput, type PickedFile, SINGLE_PATH } from './content';
  import type { ContentWorkspace } from './workspace.svelte';

  type Props = {
    ws: ContentWorkspace;
    readOnly?: boolean;
    /** single-file volume: one editor, no tree */
    single?: boolean;
    /** the per-file "render expressions" toggle (starter content only) */
    allowRender?: boolean;
    /** paths matched by the persist excludes (marked in the tree; a version must not hold them) */
    excluded?: Set<string>;
    /** the name shown for the root / the single file */
    name: string;
    testid?: string;
  };
  let { ws, readOnly = false, single = false, allowRender = false, excluded = new Set(), name, testid = 'vc-workspace' }: Props = $props();
  const L = VOLUME_CONTENT_LIMITS;

  // the focused node → where "new" / "upload" go
  const focusEntry = $derived(ws.focus ? ws.entry(ws.focus) : null);
  const targetDir = $derived(!ws.focus ? '' : focusEntry ? parentDir(ws.focus) : ws.focus);
  const issues = $derived(ws.issues);
  const maxEntries = $derived(ws.forSeed ? L.seedMaxEntries : L.versionMaxEntries);

  // ───── name modal (new file / new folder / rename)
  let nm = $state<{ kind: 'new-file' | 'new-folder' | 'rename'; dir: string; path: string } | null>(null);
  let nmOpen = $state(false);
  let nmText = $state('');
  let nmErr = $state<string | null>(null);
  const nmFull = $derived(nm ? joinPath(nm.dir, nmText.trim()) : '');
  const nmLive = $derived.by(() => {
    if (!nm) return null;
    const t = nmText.trim();
    if (!t) return 'enter a name';
    const pe = contentPathError(nmFull);
    if (pe) return pe;
    if (nm.kind === 'rename' && nmFull === nm.path) return null;
    if (treeHasFile(ws.tree, nmFull) || treeHasDir(ws.tree, nmFull)) return `${nmFull} already exists`;
    return null;
  });
  function askName(kind: 'new-file' | 'new-folder' | 'rename', path: string) {
    const dir = kind === 'rename' ? parentDir(path) : path;
    nm = { kind, dir, path };
    nmText = kind === 'rename' ? baseName(path) : '';
    nmErr = null;
    nmOpen = true;
  }
  async function submitName(e: Event) {
    e.preventDefault();
    if (!nm || nmLive) return;
    const t = nmText.trim();
    const err = nm.kind === 'new-file' ? await ws.newFile(nm.dir, t) : nm.kind === 'new-folder' ? ws.newFolder(nm.dir, t) : ws.rename(nm.path, t);
    if (err) return void (nmErr = err);
    nmOpen = false;
  }

  // ───── move to…
  let mv = $state<string | null>(null);
  let mvOpen = $state(false);
  let mvTo = $state('');
  const mvOptions = $derived.by(() => {
    if (!mv) return [];
    const src = mv;
    return [{ value: '', label: `/ (${name})` }, ...allDirs(ws.tree).filter((d) => d !== src && !d.startsWith(src + '/')).map((d) => ({ value: d, label: d, mono: true }))];
  });
  const mvErr = $derived.by(() => {
    if (!mv) return null;
    if (parentDir(mv) === mvTo) return 'it is already there';
    const to = joinPath(mvTo, baseName(mv));
    if (treeHasFile(ws.tree, to) || treeHasDir(ws.tree, to)) return `${to} already exists`;
    return null;
  });
  function askMove(path: string) {
    mv = path;
    mvTo = '';
    mvOpen = true;
  }
  function submitMove(e: Event) {
    e.preventDefault();
    if (!mv || mvErr) return;
    const err = ws.moveInto(mv, mvTo);
    if (err) return void toast.error(err, 'Not moved');
    mvOpen = false;
  }

  // ───── delete
  let rm = $state<string | null>(null);
  let rmOpen = $state(false);
  const rmCount = $derived(rm ? ws.tree.entries.filter((e) => e.path === rm || e.path.startsWith(rm + '/')).length : 0);

  // ───── uploads
  let filesInput = $state<HTMLInputElement | null>(null);
  let folderInput = $state<HTMLInputElement | null>(null);
  /** the folder of the upload in progress (null = the focused folder) */
  let upDir = $state<string | null>(null);
  let uploading = $state(false);
  let conflict = $state<{ dir: string; picked: PickedFile[]; clashes: string[] } | null>(null);
  let conflictOpen = $state(false);
  function chooseUpload(kind: 'files' | 'folder', dir: string) {
    upDir = dir;
    (kind === 'files' ? filesInput : folderInput)?.click();
  }
  function onPicked(e: Event & { currentTarget: HTMLInputElement }) {
    const picked = pickedFromInput(e.currentTarget.files);
    e.currentTarget.value = '';
    const dir = upDir ?? targetDir;
    upDir = null;
    receive(dir, picked);
  }
  /** files dropped / picked into `dir`: check names + sizes, ask about clashes, then add them */
  export function receive(dir: string, picked: PickedFile[]) {
    if (readOnly || !picked.length) return;
    if (single) {
      void putAll('', [{ rel: SINGLE_PATH, file: picked[0]!.file }], 'overwrite');
      return;
    }
    const bad: string[] = [];
    const ok: PickedFile[] = [];
    for (const p of picked) {
      const path = joinPath(dir, p.rel);
      const pe = contentPathError(path);
      if (pe) bad.push(`${p.rel}: ${pe}`);
      else if (p.file.size > L.fileMaxBytes) bad.push(`${p.rel}: ${formatContentBytes(p.file.size)} — one file is at most ${L.fileMaxBytes / 1048576} MiB`);
      else ok.push(p);
    }
    if (bad.length) toast.error(`${bad.length} file${bad.length === 1 ? '' : 's'} not added: ${bad.slice(0, 3).join('; ')}${bad.length > 3 ? ' …' : ''}`, 'Upload');
    if (ws.tree.entries.length + ok.length > maxEntries) return void toast.error(`at most ${maxEntries} files in ${ws.forSeed ? 'starter content' : 'a version'} (now ${ws.tree.entries.length}, adding ${ok.length})`, 'Too many files');
    const clashes = ok.map((p) => joinPath(dir, p.rel)).filter((p) => treeHasFile(ws.tree, p) || treeHasDir(ws.tree, p));
    if (clashes.length) {
      conflict = { dir, picked: ok, clashes };
      conflictOpen = true;
    } else void putAll(dir, ok, 'overwrite');
  }
  async function putAll(dir: string, picked: PickedFile[], how: 'overwrite' | 'skip' | 'rename') {
    conflictOpen = false;
    uploading = true;
    let added = 0;
    const errs: string[] = [];
    try {
      for (const p of picked) {
        let path = joinPath(dir, p.rel);
        const exists = treeHasFile(ws.tree, path) || treeHasDir(ws.tree, path);
        if (exists && how === 'skip') continue;
        if (exists && (how === 'rename' || treeHasDir(ws.tree, path))) path = duplicateName(ws.tree, path);
        const err = await ws.putBytes(path, new Uint8Array(await p.file.arrayBuffer()), true);
        if (err) errs.push(err);
        else added++;
      }
    } finally {
      uploading = false;
    }
    if (errs.length) toast.error(errs.slice(0, 3).join('; '), 'Some files were not added');
    if (added) {
      toast.success(`${added} file${added === 1 ? '' : 's'} added`, ws.sink ? 'In the change set — not committed yet (Commit… in the change bar).' : 'Not saved yet — Save as new version.');
      if (single) ws.openFile(SINGLE_PATH);
    }
  }

  function act(a: TreeAction, path: string) {
    const dirOf = (p: string) => (!p ? '' : ws.entry(p) ? parentDir(p) : p);
    switch (a) {
      case 'new-file':
      case 'new-folder':
        return askName(a, dirOf(path));
      case 'rename':
        return path && askName('rename', path);
      case 'move':
        return path && askMove(path);
      case 'duplicate': {
        const err = path ? ws.duplicate(path) : null;
        if (err) toast.error(err, 'Not duplicated');
        return;
      }
      case 'delete':
        rm = path;
        rmOpen = !!path;
        return;
      case 'upload-files':
        return chooseUpload('files', dirOf(path));
      case 'upload-folder':
        return chooseUpload('folder', dirOf(path));
      case 'download':
        return void download(path);
    }
  }
  async function download(path: string) {
    try {
      saveAs(new Blob([(await ws.bytesOf(path)) as BlobPart]), single ? name : baseName(path));
    } catch (e) {
      toast.error(e, 'Download failed');
    }
  }

  // ───── editor of the open file
  const openPath = $derived(single ? (ws.entry(SINGLE_PATH) ? SINGLE_PATH : null) : ws.open);
  const openEntry = $derived(openPath ? ws.entry(openPath) : null);
  const loaded = $derived(openEntry ? ws.loaded[openEntry.sha256] : undefined);
  $effect(() => {
    if (openPath && openEntry && !loaded) void ws.load(openPath);
  });
  const text = $derived(openPath ? (ws.texts[openPath] ?? (loaded?.kind === 'text' ? loaded.text : '')) : '');
  const lang = $derived(openPath ? languageForPath(single ? name : openPath) : null);
  let modeText = $state('');
  $effect(() => {
    modeText = openEntry?.mode ?? '';
  });
  const modeErr = $derived(modeText && !/^0[0-7]{3}$/.test(modeText) ? 'like 0644' : null);
  function applyMode() {
    if (!openPath || modeErr || modeText === openEntry?.mode) return;
    const err = ws.setMode(openPath, modeText);
    if (err) toast.error(err);
  }
  function setRender(on: boolean) {
    if (!openPath) return;
    const err = ws.setRender(openPath, (on ? 'expressions' : 'none') as FileRender);
    if (err) toast.error(err);
  }
  const textBytes = $derived(new TextEncoder().encode(text).length);
  const tooBig = $derived(textBytes > L.fileMaxBytes);
</script>

<input type="file" multiple class="sr-only" bind:this={filesInput} onchange={onPicked} data-testid="vc-upload-files-input" aria-hidden="true" tabindex="-1" />
<input type="file" multiple class="sr-only" bind:this={folderInput} onchange={onPicked} data-testid="vc-upload-folder-input" webkitdirectory aria-hidden="true" tabindex="-1" />

<div class="ws" class:single data-testid={testid}>
  {#if !single}
    <div class="side">
      <div class="tb" role="toolbar" aria-label="File actions" data-testid="vc-toolbar">
        {#if !readOnly}
          <Button size="sm" variant="ghost" icon={FilePlus} tip="New file in {targetDir || '/'}" aria-label="New file" onclick={() => askName('new-file', targetDir)} data-testid="vc-new-file" />
          <Button size="sm" variant="ghost" icon={FolderPlus} tip="New folder in {targetDir || '/'}" aria-label="New folder" onclick={() => askName('new-folder', targetDir)} data-testid="vc-new-folder" />
          <Button size="sm" variant="ghost" icon={Upload} tip="Upload files into {targetDir || '/'}" aria-label="Upload files" onclick={() => chooseUpload('files', targetDir)} data-testid="vc-upload-files" />
          <Button size="sm" variant="ghost" icon={FolderUp} tip="Upload a folder into {targetDir || '/'}" aria-label="Upload a folder" onclick={() => chooseUpload('folder', targetDir)} data-testid="vc-upload-folder" />
          <span class="sep"></span>
          <Button size="sm" variant="ghost" icon={Pencil} tip="Rename (F2)" aria-label="Rename" disabled={!ws.focus} onclick={() => act('rename', ws.focus)} data-testid="vc-rename" />
          <Button size="sm" variant="ghost" icon={FolderInput} tip="Move to…" aria-label="Move to" disabled={!ws.focus} onclick={() => act('move', ws.focus)} data-testid="vc-move" />
          <Button size="sm" variant="ghost" icon={Copy} tip="Duplicate" aria-label="Duplicate" disabled={!ws.focus} onclick={() => act('duplicate', ws.focus)} data-testid="vc-duplicate" />
          <Button size="sm" variant="ghost" icon={Trash2} tip="Delete (Del)" aria-label="Delete" disabled={!ws.focus} onclick={() => act('delete', ws.focus)} data-testid="vc-delete" />
        {:else}<span class="xs faint">read only</span>{/if}
        {#if uploading}<Spinner size={14} />{/if}
      </div>
      <div class="treebox">
        <FileTree {ws} {readOnly} {excluded} label="Content of {name}" onaction={act} ondropfiles={receive} />
      </div>
      <div class="stats xs faint" data-testid="vc-stats">
        {ws.tree.entries.length} / {maxEntries} files · {formatContentBytes(ws.totalBytes)}{#if ws.forSeed}{` / ${L.seedMaxTotalBytes / 1048576} MiB`}{/if}
      </div>
    </div>
  {/if}

  <div class="main">
    {#if issues.length}
      <div class="banner error small issues" data-testid="vc-issues">
        <Icon icon={CircleAlert} size={15} class="icon" />
        <div>{#each issues.slice(0, 6) as x, k (k)}<div data-code={x.code}>{x.path ? `${x.path}: ` : ''}{x.message}</div>{/each}{#if issues.length > 6}<div>… {issues.length - 6} more</div>{/if}</div>
      </div>
    {/if}
    {#if openPath && openEntry}
      <div class="eh" data-testid="vc-editor-head">
        {#if !single}<button type="button" class="btn ghost sm icon back" aria-label="Back to the files" onclick={() => (ws.open = null)}><Icon icon={ChevronLeft} size={14} /></button>{/if}
        <span class="mono strong path" title={openPath} data-testid="vc-open-path">{single ? name : openPath}</span>
        {#if ws.textChanged(openPath) || (ws.sink && ws.changedPaths().has(openPath))}<span class="badge amber xs" data-testid="vc-open-dirty">{ws.sink ? 'not committed' : 'modified'}</span>{/if}
        {#if lang}<span class="badge outline xs">{LANG_LABEL[lang]}</span>{/if}
        <span class="xs faint" data-testid="vc-open-size">{formatContentBytes(ws.texts[openPath] !== undefined ? textBytes : openEntry.size)}</span>
        <span class="grow"></span>
        <label class="mode xs" title="Permission bits ({formatMode(openEntry.mode)})">
          <span class="faint">mode</span>
          <input class="input sm mono" value={modeText} oninput={(e) => (modeText = e.currentTarget.value.trim())} onblur={applyMode} onkeydown={(e) => e.key === 'Enter' && applyMode()} disabled={readOnly} maxlength="4" class:invalid={!!modeErr} aria-label="Mode of {openPath}" data-testid="vc-mode" />
        </label>
        {#if allowRender}
          <span class="rend" title="Render {'{{ }}'} expressions once per site when the agent writes the starter content (like Files)"><Toggle checked={openEntry.render === 'expressions'} onchange={setRender} disabled={readOnly} label="render {'{{ }}'}" testid="vc-render" /></span>
        {/if}
        <Button size="sm" variant="ghost" icon={Download} tip="Download" aria-label="Download {openPath}" onclick={() => download(openPath)} data-testid="vc-download" />
        {#if single && !readOnly}<Button size="sm" icon={Upload} onclick={() => chooseUpload('files', '')} data-testid="vc-single-replace">Replace…</Button>{/if}
      </div>
      <div class="eb">
        {#if !loaded || loaded.kind === 'loading'}
          <div class="center small faint"><Spinner size={16} /> Loading…</div>
        {:else if loaded.kind === 'text'}
          {#key `${openPath}`}
            <ContentCodeEditor value={text} path={single ? name : openPath} expressions={openEntry.render === 'expressions'} readonly={readOnly} label="Content of {openPath}" oninput={(v) => ws.setText(openPath, v)} />
          {/key}
        {:else}
          <div class="center" data-testid="vc-binary">
            <Icon icon={FileText} size={28} class="faint" />
            <div class="strong">{single ? name : baseName(openPath)}</div>
            <div class="small muted">{formatContentBytes(openEntry.size)} · {loaded.kind === 'large' ? `larger than ${L.editorTextMaxBytes / 1048576} MiB — not opened in the editor` : loaded.kind === 'binary' ? 'binary file' : `could not load: ${loaded.message}`}</div>
            <div class="row" style="gap:8px">
              <Button size="sm" icon={Download} onclick={() => download(openPath)} data-testid="vc-binary-download">Download</Button>
              {#if !readOnly && !single}<Button size="sm" icon={Upload} onclick={() => chooseUpload('files', parentDir(openPath))}>Upload a replacement…</Button>{/if}
            </div>
            <div class="xs faint mono">{openEntry.sha256.slice(0, 23)}…</div>
          </div>
        {/if}
      </div>
      {#if tooBig}<div class="xs text-red pad" data-testid="vc-too-big">This file is larger than {L.fileMaxBytes / 1048576} MiB — it cannot be saved.</div>{/if}
    {:else if single}
      <div class="center" data-testid="vc-single-empty">
        <Icon icon={FileText} size={28} class="faint" />
        <div class="strong">No starter content</div>
        <div class="small muted">The single file {name} starts empty on a new site.</div>
        {#if !readOnly}
          <div class="row" style="gap:8px">
            <Button size="sm" icon={FilePlus} onclick={async () => { const e = await ws.newFile('', SINGLE_PATH); if (e) toast.error(e); }} data-testid="vc-single-create">Write it here</Button>
            <Button size="sm" icon={Upload} onclick={() => chooseUpload('files', '')} data-testid="vc-single-upload">Upload a file…</Button>
          </div>
        {/if}
      </div>
    {:else}
      <div class="center hint" data-testid="vc-no-file">
        <Icon icon={FileText} size={28} class="faint" />
        <div class="strong">Pick a file in the tree</div>
        <div class="small muted">Text files up to {L.editorTextMaxBytes / 1048576} MiB open here (language by extension). {#if !readOnly}Right-click a node for more; drag nodes to move them; drop files or folders from your computer onto a folder.{/if}</div>
      </div>
    {/if}
  </div>
</div>

<Modal bind:open={nmOpen} title={nm?.kind === 'new-file' ? 'New file' : nm?.kind === 'new-folder' ? 'New folder' : `Rename ${nm ? baseName(nm.path) : ''}`} subtitle={nm ? `in /${nm.dir}` : ''} size="sm" testid="vc-name-modal">
  <form id="vc-name-form" onsubmit={submitName} novalidate>
    <Field label="Name" id="vc-name" required error={nmErr ?? (nmText ? nmLive : null)} hint="A name, or a/b/c to create the folders on the way.">
      <input id="vc-name" class="input mono" bind:value={nmText} spellcheck="false" autocomplete="off" data-autofocus data-testid="vc-name-input" />
    </Field>
  </form>
  {#snippet footer()}
    <Button onclick={() => (nmOpen = false)}>Cancel</Button>
    <Button type="submit" form="vc-name-form" variant="primary" disabled={!!nmLive} data-testid="vc-name-ok">{nm?.kind === 'rename' ? 'Rename' : 'Create'}</Button>
  {/snippet}
</Modal>

<Modal bind:open={mvOpen} title="Move {mv ? baseName(mv) : ''}" subtitle={mv ? `from /${parentDir(mv)}` : ''} size="sm" testid="vc-move-modal">
  <form id="vc-move-form" onsubmit={submitMove} novalidate>
    <Field label="Into the folder" id="vc-move-to" error={mvErr}>
      <Select id="vc-move-to" options={mvOptions} bind:value={mvTo} testid="vc-move-to" ariaLabel="Target folder" />
    </Field>
  </form>
  {#snippet footer()}
    <Button onclick={() => (mvOpen = false)}>Cancel</Button>
    <Button type="submit" form="vc-move-form" variant="primary" disabled={!!mvErr} data-testid="vc-move-ok">Move</Button>
  {/snippet}
</Modal>

<ConfirmDialog bind:open={rmOpen} title="Delete {rm ? baseName(rm) : ''}?" confirmLabel="Delete" danger onconfirm={() => { if (rm) { const e = ws.remove(rm); if (e) toast.error(e); } }} testid="vc-delete-dialog">
  {#snippet body()}
    <p class="small muted">{#if rm && !ws.entry(rm)}The folder <span class="mono">{rm}</span> and the {rmCount} file{rmCount === 1 ? '' : 's'} in it are removed{:else}<span class="mono">{rm}</span> is removed{/if} from the content (on Save).</p>
  {/snippet}
</ConfirmDialog>

<Modal bind:open={conflictOpen} title="{conflict?.clashes.length ?? 0} file{conflict?.clashes.length === 1 ? '' : 's'} already exist" size="sm" testid="vc-conflict-modal">
  <div class="stack">
    <ul class="clash mono xs">{#each (conflict?.clashes ?? []).slice(0, 8) as p (p)}<li>{p}</li>{/each}{#if (conflict?.clashes.length ?? 0) > 8}<li>… {(conflict?.clashes.length ?? 0) - 8} more</li>{/if}</ul>
    <div class="small muted"><Icon icon={TriangleAlert} size={13} /> {conflict?.picked.length ?? 0} file{conflict?.picked.length === 1 ? '' : 's'} picked. Replace the existing ones, skip them, or keep both (the new ones get a “copy” name).</div>
  </div>
  {#snippet footer()}
    <Button onclick={() => (conflictOpen = false)}>Cancel</Button>
    <Button onclick={() => conflict && putAll(conflict.dir, conflict.picked, 'skip')} data-testid="vc-conflict-skip">Skip existing</Button>
    <Button onclick={() => conflict && putAll(conflict.dir, conflict.picked, 'rename')} data-testid="vc-conflict-rename">Keep both</Button>
    <Button variant="primary" onclick={() => conflict && putAll(conflict.dir, conflict.picked, 'overwrite')} data-testid="vc-conflict-overwrite">Overwrite</Button>
  {/snippet}
</Modal>

<style>
  .ws {
    display: grid;
    grid-template-columns: minmax(220px, 300px) minmax(0, 1fr);
    border: 1px solid var(--border);
    border-radius: var(--r-lg);
    background: var(--surface);
    height: min(72vh, 760px);
    min-height: 420px;
    overflow: hidden;
  }
  .ws.single {
    grid-template-columns: minmax(0, 1fr);
    height: min(64vh, 640px);
  }
  .side {
    display: flex;
    flex-direction: column;
    min-height: 0;
    border-right: 1px solid var(--border);
    background: var(--surface-2);
  }
  .tb {
    display: flex;
    align-items: center;
    gap: 2px;
    padding: 4px 6px;
    border-bottom: 1px solid var(--border);
    flex-wrap: wrap;
  }
  .sep {
    width: 1px;
    height: 18px;
    background: var(--border);
    margin: 0 3px;
  }
  .treebox {
    flex: 1;
    min-height: 0;
    overflow: auto;
  }
  .stats {
    padding: 5px 10px;
    border-top: 1px solid var(--border);
  }
  .main {
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
  }
  .issues {
    margin: 8px;
  }
  .eh {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 5px 10px;
    border-bottom: 1px solid var(--border);
    flex-wrap: wrap;
    min-height: 40px;
  }
  .back {
    display: none;
  }
  .path {
    font-size: 12.5px;
    overflow-wrap: anywhere;
    min-width: 0;
  }
  .grow {
    flex: 1;
  }
  .mode {
    display: inline-flex;
    align-items: center;
    gap: 5px;
  }
  .mode input {
    width: 64px;
  }
  .rend {
    display: inline-flex;
    font-size: var(--fs-sm);
  }
  .eb {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
  }
  .center {
    flex: 1;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 8px;
    padding: 24px;
    text-align: center;
  }
  .hint .small {
    max-width: 420px;
  }
  .pad {
    padding: 4px 10px;
  }
  .clash {
    margin: 0;
    padding-left: 18px;
  }
  @media (max-width: 760px) {
    .ws {
      grid-template-columns: minmax(0, 1fr);
      height: auto;
      min-height: 0;
    }
    .side {
      border-right: 0;
      border-bottom: 1px solid var(--border);
      max-height: 46vh;
    }
    .main {
      min-height: 360px;
    }
    .eb {
      height: 52vh;
      flex: none;
    }
    .back {
      display: inline-flex;
    }
  }
</style>
