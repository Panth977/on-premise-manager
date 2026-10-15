<script lang="ts">
  import type { FileDef } from '@opm/shared';
  import { fileRenderMode } from '@opm/shared';
  import ScopeHeader from './ScopeHeader.svelte';
  import Button from '../ui/Button.svelte';
  import EmptyState from '../ui/EmptyState.svelte';
  import SearchInput from '../ui/SearchInput.svelte';
  import ConfirmDialog from '../ui/ConfirmDialog.svelte';
  import Menu, { type MenuItem } from '../ui/Menu.svelte';
  import Icon from '../ui/Icon.svelte';
  import { Plus, Pencil, Trash2, FileText, Upload, Download, X, Files, Eye } from '../ui/icons';
  import FileDrawer from './FileDrawer.svelte';
  import FileContentDrawer from './FileContentDrawer.svelte';
  import DraftBanner from './DraftBanner.svelte';
  import ProgressBar from '../ui/ProgressBar.svelte';
  import { fileRefs } from './refs';
  import { catalog } from '../stores/catalog.svelte';
  import { toast } from '../toast.svelte';
  import { downloadContent, fileName } from './files';
  import { bytes, shortSha } from '../util/format';
  import type { AnyScope } from './scope.svelte';

  let { scope }: { scope: AnyScope } = $props();
  const isDep = $derived(scope.kind === 'deployment');
  let q = $state('');
  const rows = $derived(scope.config.files.filter((f) => !q || `${f.fileSlug} ${f.label} ${f.mime}`.toLowerCase().includes(q.toLowerCase())));

  let defOpen = $state(false);
  let contentOpen = $state(false);
  let target = $state<FileDef | null>(null);
  let rmOpen = $state(false);
  let clearOpen = $state(false);
  const rmRefs = $derived(target && rmOpen ? fileRefs(scope.working(), target.fileSlug, (id) => catalog.slugOf(id)) : []);
  let busy = $state<Record<string, number>>({});
  let picker = $state<HTMLInputElement | null>(null);

  function pick(f: FileDef) {
    target = f;
    picker?.click();
  }
  async function onPicked(e: Event) {
    const input = e.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    input.value = '';
    const t = target;
    if (!file || !t) return;
    if (file.size > t.maxBytes) {
      toast.push('error', `The file is ${bytes(file.size)}`, `Max size of ${t.fileSlug} is ${bytes(t.maxBytes)}.`);
      return;
    }
    busy[t.fileSlug] = 0;
    try {
      // §21 #107 the bytes are a pending change (memory + IndexedDB): uploaded only by Commit…
      const ref = await scope.putBytes(new Uint8Array(await file.arrayBuffer()));
      busy[t.fileSlug] = 1;
      const items = scope.config.files.map((x) => ($state.snapshot(x.fileSlug === t.fileSlug ? { ...x, content: ref } : x) as FileDef));
      await scope.saveSection('files', items, `${file.name} → ${t.fileSlug}`);
    } catch (err) {
      toast.error(err, 'Could not read the file');
    } finally {
      delete busy[t.fileSlug];
    }
  }
  async function download(f: FileDef) {
    if (!f.content) return;
    try {
      await downloadContent(f.content.sha256, fileName(f.fileSlug, f.mime));
    } catch (e) {
      toast.error(e, 'Download failed');
    }
  }
  async function doClear() {
    const slug = target!.fileSlug;
    return scope.saveSection('files', scope.config.files.map((x) => ($state.snapshot(x.fileSlug === slug ? { ...x, content: null } : x) as FileDef)), `Content of ${slug} cleared`);
  }
  async function doRemove() {
    const slug = target!.fileSlug;
    return scope.saveSection('files', scope.config.files.filter((x) => x.fileSlug !== slug).map((x) => $state.snapshot(x) as FileDef), `File ${slug} removed`);
  }
  function menu(f: FileDef): MenuItem[] {
    const m: MenuItem[] = [];
    m.push({ label: 'Edit definition', icon: Pencil, onclick: () => ((target = f), (defOpen = true)), testid: 'm-edit-def' });
    if (f.editor === 'text') m.push({ label: 'Edit content', icon: FileText, onclick: () => ((target = f), (contentOpen = true)), testid: 'm-edit-content' });
    m.push({ label: 'Upload', icon: Upload, onclick: () => pick(f), testid: 'm-upload' });
    if (f.content) m.push({ label: 'Download', icon: Download, onclick: () => download(f), testid: 'm-download' });
    if (f.content) m.push({ label: 'Clear content', icon: X, onclick: () => ((target = f), (clearOpen = true)), testid: 'm-clear' });
    m.push({ divider: true, label: '' });
    m.push({ label: 'Remove', icon: Trash2, danger: true, onclick: () => ((target = f), (rmOpen = true)), testid: 'm-remove' });
    return m;
  }
</script>

<input type="file" class="sr-only" bind:this={picker} onchange={onPicked} tabindex="-1" aria-hidden="true" data-testid="file-picker" />

{#if isDep}<DraftBanner deploymentId={scope.id} />{/if}
<ScopeHeader kind={scope.kind} title="Files" subtitle="Config files and certificates. Each site keeps ONE copy on the server; every container that mounts a file shares it read-only. Text files may hold {'{{ }}'} expressions, rendered once per site." icon={Files} readOnly={!scope.canWrite}>
  {#snippet actions()}
    <SearchInput bind:value={q} placeholder="Search files" width="220px" />
    {#if scope.canWrite}<Button variant="primary" icon={Plus} onclick={() => ((target = null), (defOpen = true))} data-testid="add-file">Add file</Button>{/if}
  {/snippet}
</ScopeHeader>

{#if !scope.config.files.length}
  <EmptyState icon={Files} title="No files" text="Add a file definition, then its content (edit text in the browser or upload).">
    {#snippet action()}{#if scope.canWrite}<Button variant="primary" icon={Plus} onclick={() => ((target = null), (defOpen = true))}>Add file</Button>{/if}{/snippet}
  </EmptyState>
{:else if !rows.length}
  <EmptyState compact title="No match" text="No file matches “{q}”." />
{:else}
  <div class="table-wrap">
    <table class="table" data-testid="files-table">
      <thead><tr><th>Slug</th><th>Label</th><th>Mime</th><th>Editor</th><th>On the server</th><th class="num">Max size</th><th>{isDep ? 'Content' : 'Default content'}</th><th class="actions"><span class="sr-only">Actions</span></th></tr></thead>
      <tbody>
        {#each rows as f (f.fileSlug)}
          <tr data-testid="file-row-{f.fileSlug}">
            <td class="mono">{f.fileSlug}</td>
            <td>{f.label}{#if f.description}<div class="xs faint">{f.description}</div>{/if}</td>
            <td class="mono small">{f.mime}</td>
            <td><span class="badge outline">{f.editor}</span>{#if fileRenderMode(f) === 'expressions'} <span class="badge outline accent" data-tip="Resolves {'{{ }}'} once per site" data-testid="render-badge-{f.fileSlug}">{'{{ }}'}</span>{/if}</td>
            <td class="small mono loc" data-testid="file-server-{f.fileSlug}">{#if f.location?.kind === 'path'}{f.location.path}{:else}<span class="auto">auto</span> <span class="faint">&lt;agentHome&gt;/files/{f.fileSlug}</span>{/if}</td>
            <td class="num small">{bytes(f.maxBytes)}</td>
            <td>
              {#if busy[f.fileSlug] !== undefined}
                <div style="min-width:120px"><ProgressBar value={busy[f.fileSlug]!} /></div>
              {:else if f.content}
                <span class="mono small" data-tip={f.content.sha256}>{shortSha(f.content.sha256)}</span> <span class="faint small">· {bytes(f.content.sizeBytes)}</span>
              {:else}<span class="faint">—</span>{/if}
            </td>
            <td class="actions">
              {#if scope.canWrite}
                {#if f.editor === 'text'}<Button size="sm" icon={FileText} onclick={() => ((target = f), (contentOpen = true))} data-testid="edit-content-{f.fileSlug}">Edit content</Button>
                {:else}<Button size="sm" icon={Upload} onclick={() => pick(f)} data-testid="upload-{f.fileSlug}">Upload</Button>{/if}
                <Menu items={menu(f)} label="Actions for {f.fileSlug}" testid="menu-{f.fileSlug}" />
              {:else if f.content}
                {#if f.editor === 'text'}<Button size="sm" icon={Eye} onclick={() => ((target = f), (contentOpen = true))} data-testid="view-content-{f.fileSlug}">View</Button>{/if}
                <Button size="sm" icon={Download} onclick={() => download(f)}>Download</Button>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/if}

<FileDrawer bind:open={defOpen} {scope} file={target} />
<FileContentDrawer bind:open={contentOpen} {scope} file={target} />
<ConfirmDialog bind:open={clearOpen} title="Clear the content of {target?.fileSlug}?" message="Containers that mount it block publish until it has content again." confirmLabel="Clear content" danger onconfirm={doClear} testid="clear-file" />
<ConfirmDialog bind:open={rmOpen} title="Remove file {target?.fileSlug}?" confirmLabel="Remove" danger onconfirm={doRemove} testid="remove-file">
  {#snippet body()}
    {#if rmRefs.length}
      <div class="banner warn small"><div><b>Used in {rmRefs.length} place{rmRefs.length === 1 ? '' : 's'}</b>:<ul class="refs">{#each rmRefs.slice(0, 8) as r, i (i)}<li><span class="mono xs">{r.path}</span> <span class="faint xs">{r.value}</span></li>{/each}</ul></div></div>
    {:else}<p class="muted">No container mounts this file.</p>{/if}
  {/snippet}
</ConfirmDialog>

<style>
  .loc {
    max-width: 260px;
    overflow-wrap: anywhere;
  }
  .auto {
    display: inline-block;
    padding: 0 6px;
    border-radius: 999px;
    font-family: var(--font-sans, inherit);
    font-size: 11px;
    background: var(--accent-soft);
    color: var(--accent-text);
  }
  .refs {
    margin: 6px 0 0;
    padding-left: 16px;
  }
</style>
