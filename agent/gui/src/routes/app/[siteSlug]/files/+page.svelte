<script lang="ts">
  import Select from '$lib/ui/Select.svelte';
  import { apiPath, download, fetchText } from '$lib/api';
  import { headConfig } from '$lib/state/app.svelte';
  import { draft } from '$lib/state/draft.svelte';
  import { toasts } from '$lib/state/toast.svelte';
  import { bytes, shortHash } from '$lib/format';
  import PageHead from '$lib/ui/PageHead.svelte';
  import Load from '$lib/ui/Load.svelte';
  import Empty from '$lib/ui/Empty.svelte';
  import NoHead from '$lib/ui/NoHead.svelte';
  import LocalEditBar from '$lib/ui/LocalEditBar.svelte';
  import FieldIssues from '$lib/ui/FieldIssues.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Drawer from '$lib/ui/Drawer.svelte';
  import CodeEditor from '$lib/ui/CodeEditor.svelte';
  import Progress from '$lib/ui/Progress.svelte';
  import type { BlobRef, FileDef } from '@opm/shared/types';
  import { checkFileContent, fileRenderMode, isJsonMime, jsonError, locationLabel, locationPath } from '@opm/shared/validate';
  import { agentHomes, overview } from '$lib/state/app.svelte';
  import { mountersOf, renderPreview } from '$lib/containerPreview';
  import Deferred from '$lib/ui/Deferred.svelte';
  import FileForm, { blankFile } from '$opm/forms/files/FileForm.svelte';
  import Confirm from '$lib/ui/Confirm.svelte';

  let editing = $state<FileDef | null>(null);
  /** §21 #102: the file DEFINITION in the shared form; §21 #113: a local edit (add / change / remove here) */
  let details = $state<FileDef | null>(null);
  let detailsOpen = $state(false);
  let detailsNew = $state(false);
  let detailsValid = $state(true);
  let removeSlug = $state<string | null>(null);
  let removeOpen = $state(false);
  function openDetails(f: FileDef | null) {
    details = f ? ($state.snapshot(f) as FileDef) : blankFile();
    detailsNew = !f;
    detailsOpen = true;
  }
  function applyDetails() {
    if (!details || !detailsValid || !draft.work) return;
    const d = $state.snapshot(details) as FileDef;
    const i = draft.work.files.findIndex((x) => x.fileSlug === d.fileSlug);
    if (detailsNew || i < 0) {
      draft.work.files.push({ ...d, content: null });
      toasts.ok(`File ${d.fileSlug} added`, 'Edit or upload its content, then commit from the change bar.');
    } else draft.work.files[i] = { ...d, content: draft.work.files[i]!.content };
    detailsOpen = false;
  }
  function removeFile() {
    if (!draft.work || !removeSlug) return;
    draft.work.files = draft.work.files.filter((x) => x.fileSlug !== removeSlug);
    removeSlug = null;
  }
  let editOpen = $state(false);
  let text = $state('');
  let loadingText = $state(false);
  let loadErr = $state<string | null>(null);
  /** §21 #107 the head text loaded into the editor (typing it back = no change) */
  let headText = $state<string | null>(null);
  /** the editor holds text that is not in the change set yet (debounced apply) */
  let applyTimer: ReturnType<typeof setTimeout> | null = null;
  let lastApplied = '';
  let tooLarge = $state<string | null>(null);
  let uploading = $state<{ slug: string; p: number } | null>(null);
  let picker: HTMLInputElement | undefined = $state();
  let pickFor = $state<FileDef | null>(null);

  const cfg = $derived(draft.asConfig('work'));
  const depId = $derived(overview.data?.deploymentId ?? draft.deploymentId);
  /** template files ({{ }} resolved per mounting container, SPEC §21 #54) */
  const isTemplate = $derived(!!editing && fileRenderMode(editing) === 'expressions');
  const exprIssues = $derived(isTemplate ? checkFileContent(text) : []);
  const mounters = $derived(editing && cfg ? mountersOf(cfg, editing.fileSlug) : []);
  /** round D: a template file is rendered ONCE per site (shared by all its containers) → preview per site */
  const self = $derived(overview.data?.self ?? '');
  const sites = $derived((cfg?.sites ?? []).map((s) => s.siteSlug));
  let pvTarget = $state('');
  let pvShow = $state(false);
  const pvResult = $derived.by(() => {
    if (!pvShow || !cfg || !pvTarget || !isTemplate) return null;
    return renderPreview(cfg, depId, pvTarget, text);
  });
  $effect(() => agentHomes.ensure(self));
  /** "auto · <agentHome>/files/<slug>" (agentHome of this site) or the custom path ("./" resolved in the title) */
  const where = (f: FileDef) => locationLabel(f.location, 'files', f.fileSlug, agentHomes.of(self));
  const whereAbs = (f: FileDef) => locationPath(f.location, 'files', f.fileSlug, agentHomes.map[self]);
  const pvJsonErr = $derived(pvResult && pvResult.ok && editing && isJsonMime(editing.mime) ? jsonError(pvResult.value) : null);
  const head = (slug: string) => draft.base?.config.files.find((f) => f.fileSlug === slug);
  const changed = (f: FileDef) => JSON.stringify(f.content) !== JSON.stringify(head(f.fileSlug)?.content ?? null);

  async function openEdit(f: FileDef) {
    editing = f;
    editOpen = true;
    loadErr = null;
    pvShow = false;
    const m = draft.work && cfg ? mountersOf(cfg, f.fileSlug) : [];
    pvTarget = m[0]?.siteSlug ?? (sites.includes(self) ? self : (sites[0] ?? ''));
    tooLarge = null;
    headText = null;
    loadingText = true;
    try {
      if (head(f.fileSlug)?.content) headText = await fetchText('getFileContent', apiPath('getFileContent', { fileSlug: f.fileSlug }));
    } catch (e) {
      loadErr = e instanceof Error ? e.message : String(e);
    }
    // a pending edit: its text (kept for the editor) or its bytes (kept in this browser)
    const local = draft.fileText[f.fileSlug];
    const cur = draft.work?.files.find((x) => x.fileSlug === f.fileSlug);
    const pend = cur?.content ? draft.bytesOf(cur.content.sha256) : undefined;
    if (changed(f) && local !== undefined) text = local;
    else if (changed(f) && pend) text = new TextDecoder().decode(pend);
    else text = headText ?? '';
    lastApplied = text;
    loadingText = false;
  }
  function setContent(slug: string, ref: BlobRef | null) {
    const f = draft.work?.files.find((x) => x.fileSlug === slug);
    if (f) f.content = ref ? { sha256: ref.sha256, sizeBytes: ref.sizeBytes } : null;
  }
  /**
   * §21 #107 the editor applies into the change set as you type (debounced) and when the drawer closes: the bytes stay
   * in this browser (memory + IndexedDB) and are uploaded only by Commit. Typing the head text back = no change.
   */
  async function applyText() {
    if (applyTimer) clearTimeout(applyTimer);
    applyTimer = null;
    const f = editing;
    if (!f || loadingText || text === lastApplied) return;
    const enc = new TextEncoder().encode(text);
    if (enc.length > f.maxBytes) {
      tooLarge = `${bytes(enc.length)} > max ${bytes(f.maxBytes)} — not applied`;
      return;
    }
    tooLarge = null;
    lastApplied = text;
    const h = head(f.fileSlug);
    if (headText !== null && text === headText && h?.content) {
      setContent(f.fileSlug, h.content);
      delete draft.fileText[f.fileSlug];
      return;
    }
    const t = text;
    const ref = await draft.putBytes(enc);
    setContent(f.fileSlug, ref);
    draft.fileText[f.fileSlug] = t;
  }
  function scheduleApply() {
    if (applyTimer) clearTimeout(applyTimer);
    applyTimer = setTimeout(() => void applyText(), 300);
  }
  $effect(() => {
    void text;
    if (editOpen && editing && !loadingText && text !== lastApplied) scheduleApply();
  });
  $effect(() => {
    if (!editOpen && editing) void applyText(); // closing the drawer flushes the last keystrokes
  });
  function revertFile() {
    if (!editing) return;
    undo(editing);
    text = headText ?? '';
    lastApplied = text;
    tooLarge = null;
  }
  function pick(f: FileDef) {
    pickFor = f;
    picker?.click();
  }
  async function onPicked(e: Event) {
    const input = e.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    input.value = '';
    const f = pickFor;
    if (!file || !f) return;
    if (file.size > f.maxBytes) return toasts.push('bad', 'File too large', `${bytes(file.size)} > max ${bytes(f.maxBytes)} for ${f.fileSlug}`);
    if (file.size > 50 * 1024 * 1024) return toasts.push('bad', 'File too large', 'The agent accepts at most 50 MiB.');
    uploading = { slug: f.fileSlug, p: 0 };
    try {
      // §21 #107 kept in this browser; uploaded by Commit together with the rest of the change set
      const ref = await draft.putBytes(new Uint8Array(await file.arrayBuffer()));
      setContent(f.fileSlug, ref);
      delete draft.fileText[f.fileSlug];
      toasts.ok(`${file.name} → ${f.fileSlug}`, `${bytes(ref.sizeBytes)} kept in this browser — uploaded when you commit.`);
    } catch (err) {
      toasts.err(err, 'Could not read the file');
    } finally {
      uploading = null;
    }
  }
  function undo(f: FileDef) {
    const w = draft.work?.files.find((x) => x.fileSlug === f.fileSlug);
    if (w) w.content = structuredClone($state.snapshot(head(f.fileSlug)?.content ?? null));
    delete draft.fileText[f.fileSlug];
  }
  async function dl(f: FileDef) {
    try {
      await download('getFileContent', apiPath('getFileContent', { fileSlug: f.fileSlug }), f.fileSlug);
    } catch (e) {
      toasts.err(e, 'Download failed');
    }
  }
</script>

<PageHead icon="file" title="Files" sub="Deployment files mounted into containers. Add, change (definition and content) and remove files here — kept in this browser, applied on this site when you commit.">
  {#snippet actions()}
    <Btn icon="plus" variant="primary" disabled={!draft.work} onclick={() => openDetails(null)} data-testid="add-file">Add file</Btn>
  {/snippet}
</PageHead>
<input type="file" class="sr-only" bind:this={picker} onchange={onPicked} tabindex="-1" aria-hidden="true" />

<Load loading={headConfig.loading} loaded={headConfig.loaded} error={headConfig.error} retry={() => headConfig.load()}>
  {#if !draft.base?.head}
    <section class="card"><NoHead /></section>
  {:else if (draft.work?.files.length ?? 0) === 0}
    <section class="card"><Empty icon="file" title="No files" text="This deployment has no files yet. Add one here, or merge a cloud commit on the Branch page."><Btn icon="plus" disabled={!draft.work} onclick={() => openDetails(null)}>Add file</Btn></Empty></section>
  {:else}
    <section class="card">
      <div class="table-wrap">
        <table class="table" data-testid="files">
          <thead><tr><th>Slug</th><th>Mime</th><th>Editor</th><th>Render</th><th>On the server</th><th>Size</th><th>sha256</th><th class="right">Actions</th></tr></thead>
          <tbody>
            {#each draft.work?.files ?? [] as f (f.fileSlug)}
              <tr class:changed={changed(f)}>
                <td><span class="mono"><strong>{f.fileSlug}</strong></span><div class="muted tiny">{f.label}</div></td>
                <td class="mono small">{f.mime}</td>
                <td class="small">{f.editor}</td>
                <td class="small" data-testid="render-{f.fileSlug}">{#if fileRenderMode(f) === 'expressions'}<span class="badge info" title="{'{{ }}'} expressions are rendered once per site">expressions</span>{:else}<span class="badge">none</span>{/if}</td>
                <td class="small loc" data-testid="location-{f.fileSlug}" title="One copy per site, shared by every container that mounts it: {whereAbs(f)}."><span class="mono">{where(f)}</span></td>
                <td class="small nowrap">{f.content ? bytes(f.content.sizeBytes) : '—'} <span class="faint">/ {bytes(f.maxBytes)}</span></td>
                <td>{#if f.content}<code title={f.content.sha256}>{shortHash(f.content.sha256)}</code>{:else}<span class="badge">no content</span>{/if}{#if changed(f)} <span class="badge accent">changed</span>{/if}</td>
                <td class="actions">
                  {#if uploading?.slug === f.fileSlug}<span style="display:inline-block;width:80px;vertical-align:middle"><Progress value={uploading.p} /></span>{/if}
                  {#if !head(f.fileSlug)}<span class="badge accent" title="Added on this site, not committed yet">new</span>{/if}
                  <Btn size="xs" variant="ghost" icon="info" onclick={() => openDetails(f)} data-testid="details-{f.fileSlug}">Details</Btn>
                  {#if f.editor === 'text'}<Btn size="xs" icon="edit" onclick={() => openEdit(f)} data-testid="edit-{f.fileSlug}">Edit</Btn>{/if}
                  <Btn size="xs" icon="upload" onclick={() => pick(f)} disabled={!!uploading}>Upload</Btn>
                  {#if head(f.fileSlug)?.content}<Btn size="xs" icon="download" variant="ghost" onclick={() => dl(f)}>Download</Btn>{/if}
                  {#if changed(f)}<Btn size="xs" variant="ghost" onclick={() => undo(f)}>Undo</Btn>{/if}
                  <Btn size="xs" variant="ghost" icon="trash" title="Remove the file" aria-label="Remove {f.fileSlug}" onclick={() => ((removeSlug = f.fileSlug), (removeOpen = true))} data-testid="remove-file-{f.fileSlug}" />
                </td>
              </tr>
              {#if draft.issuesAt(`files[${f.fileSlug}]`).length}<tr><td colspan="8"><FieldIssues issues={draft.issuesAt(`files[${f.fileSlug}]`)} /></td></tr>{/if}
            {/each}
          </tbody>
        </table>
      </div>
    </section>
  {/if}
  <LocalEditBar />
</Load>

<Drawer bind:open={detailsOpen} title={detailsNew ? 'Add file' : `File ${details?.fileSlug ?? ''}`} subtitle="The definition (a local edit, applied when you commit); the content is set with Edit / Upload." testid="file-def-drawer">
  {#if details && detailsOpen}<FileForm bind:value={details} bind:valid={detailsValid} mode={detailsNew ? 'create' : draft.work ? 'edit' : 'read'} agentHome={agentHomes.map[self]} />{/if}
  {#snippet footer()}
    <Btn onclick={() => (detailsOpen = false)}>Cancel</Btn>
    {#if draft.work}<Btn variant="primary" disabled={!detailsValid} onclick={applyDetails} data-testid="file-def-save">{detailsNew ? 'Add' : 'Apply'}</Btn>{/if}
  {/snippet}
</Drawer>
<Confirm bind:open={removeOpen} title="Remove file {removeSlug ?? ''}?" confirmLabel="Remove" danger onconfirm={removeFile} testid="file-remove-confirm">
  <p>Containers that mount it are reported by the validation before you can commit.</p>
</Confirm>

<Drawer bind:open={editOpen} wide title="Edit {editing?.fileSlug ?? ''}" subtitle={editing ? `${editing.label} · ${editing.mime} · max ${bytes(editing.maxBytes)}` : ''} testid="file-drawer">
  {#if loadingText}
    <div class="row muted"><span class="spin"></span>Loading the head content…</div>
  {:else}
    {#if loadErr}<div class="banner bad small" style="margin-bottom:10px"><Icon name="alert" /><div class="grow">Could not load the head content: {loadErr}</div></div>{/if}
    {#if isTemplate}
      <div class="banner info small" style="margin-bottom:8px"><Icon name="braces" /><div class="grow">Template: <code>{'{{ … }}'}</code> blocks are rendered <strong>once per site</strong> (var, secret, site, self, deployment). Files are shared by all containers on the site — <code>container.*</code> is not allowed; use a variable instead. Write a literal <code>{'{{'}</code> as <code>{'{{ "{{" }}'}</code>.</div></div>
    {/if}
    {#if editing}
      <div class="locline small" data-testid="file-location">
        <Icon name="server" size={13} /><span>Location on the server: <span class="mono">{where(editing)}</span></span>
        <span class="muted tiny">one copy per site · {mounters.length ? `mounted read-only by ${mounters.map((m) => `${m.siteSlug}/${m.containerSlug}`).join(', ')}` : 'not mounted by any container'}</span>
      </div>
    {/if}
    <CodeEditor bind:value={text} maxBytes={editing?.maxBytes} label="Content of {editing?.fileSlug}" />
    {#if exprIssues.length}
      <div class="stack tight" style="margin-top:8px" data-testid="file-expr-issues">
        {#each exprIssues as e, i (i)}<div class="bad-text small" role="alert"><span class="issue-code">{e.code}</span>{editing?.fileSlug}:{e.line}:{e.col}: {e.message}</div>{/each}
      </div>
    {/if}
    {#if isTemplate}
      <div class="pvbar">
        <label class="small" for="pv-target">Preview for site</label>
        <div class="pvsel"><Select id="pv-target" size="sm" bind:value={pvTarget} disabled={!sites.length} options={sites.map((s) => ({ value: s, label: s, icon: 'server', badge: s === self ? { text: 'this site', tone: 'blue' as const } : undefined, hint: mounters.some((m) => m.siteSlug === s) ? undefined : 'not mounted there' }))} ariaLabel="Preview for site" testid="preview-site" /></div>
        <Btn size="sm" icon="eye" disabled={!pvTarget} onclick={() => (pvShow = !pvShow)} data-testid="preview-as">{pvShow ? 'Hide preview' : 'Preview'}</Btn>
      </div>
      {#if pvShow && pvResult}
        {#if pvResult.ok}
          {#if pvJsonErr}<div class="bad-text small" role="alert"><span class="issue-code">V_FILE_JSON</span>{pvJsonErr}</div>{/if}
          <pre class="pv" data-testid="file-render-preview"><Deferred text={pvResult.masked} /></pre>
          <div class="hint">Secrets are masked (•••). The agent renders the real bytes once per site at staging (0400 when secrets are used, else 0444; read-only mounts).</div>
        {:else}
          <div class="bad-text small" role="alert"><span class="issue-code">{pvResult.code}</span>{editing?.fileSlug}:{pvResult.line}:{pvResult.col}: {pvResult.message}</div>
        {/if}
      {/if}
    {/if}
  {/if}
  {#snippet footer()}
    <span class="small muted grow applied" data-testid="file-applied">{#if tooLarge}<span class="bad-text">{tooLarge}</span>{:else if editing && changed(editing)}<Icon name="check" size={13} />Changed — in this browser until you commit{:else}Edits apply as you type; nothing is uploaded until Commit{/if}</span>
    {#if editing && changed(editing)}<Btn variant="ghost" icon="rotate" onclick={revertFile} data-testid="file-revert">Revert to committed</Btn>{/if}
    <Btn variant="primary" disabled={loadingText} onclick={() => (editOpen = false)} data-testid="file-done">Done</Btn>
  {/snippet}
</Drawer>

<style>
  .pvsel {
    width: 260px;
    max-width: 100%;
  }
  .pvbar {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
    margin-top: 12px;
  }
  .locline {
    display: flex;
    gap: 6px 8px;
    align-items: center;
    flex-wrap: wrap;
    margin: 0 0 10px;
  }
  .loc {
    max-width: 260px;
    overflow-wrap: anywhere;
  }
  .pv {
    margin: 8px 0 4px;
    padding: 10px;
    max-height: 40vh;
    overflow: auto;
    background: var(--gray-soft);
    border-radius: var(--r-md);
    font-size: 12.5px;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .applied {
    display: inline-flex;
    gap: 5px;
    align-items: center;
    margin-right: auto;
  }
  @media (max-width: 560px) {
    .applied:not(:has(.bad-text)) {
      display: none;
    }
  }
  tr.changed td {
    background: color-mix(in srgb, var(--accent-soft) 45%, transparent);
  }
</style>
