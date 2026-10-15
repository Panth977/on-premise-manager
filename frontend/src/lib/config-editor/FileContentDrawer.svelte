<script lang="ts">
  import { untrack } from 'svelte';
  import type { ExprContext, FileDef } from '@opm/shared';
  import { checkFileContent, fileRenderMode, isJsonMime, normalizeConfig, siteExprMap } from '@opm/shared';
  import Drawer from '../ui/Drawer.svelte';
  import Button from '../ui/Button.svelte';
  import ErrorState from '../ui/ErrorState.svelte';
  import { exprPaths } from '../ui/ExprInput.svelte';
  import FileTemplateEditor, { type EditorMark, type PathSuggestion } from './files/FileTemplateEditor.svelte';
  import FilePreview from './files/FilePreview.svelte';
  import { fileSites, previewVctx, renderForSite, siteCtx } from './files/render';
  import { catalog } from '../stores/catalog.svelte';
  import { errMsg } from '../api';
  import { fetchContent } from './files';
  import { bytes, shortSha } from '../util/format';
  import { utf8Bytes } from '../util/validators';
  import type { ConfigScope } from './scope.svelte';

  /**
   * Edit (or view, read-only) the content of a text file: code editor with {{ }} highlight + autocomplete + live check
   * (shared checkFileContent, line:col), render check once for every site where a container mounts it, "Preview for
   * site…" (rendered, secrets masked). Round D part 2: one copy per site, no container.* / file.* in files.
   * §21 #107: no Save / Apply — the text IS a pending change: ~300 ms after the last keystroke (and on close) it is
   * hashed, kept as pending bytes (scope.putBytes: memory + IndexedDB) and FileDef.content points at it in the browser
   * change set; nothing is uploaded until Commit…. "Revert to committed" puts the head's content back.
   */
  let { open = $bindable(), scope, file }: { open: boolean; scope: ConfigScope; file: FileDef | null } = $props();
  let text = $state('');
  let loading = $state(false);
  let loadErr = $state<string | null>(null);
  /** the text the change set holds now (applied), null while unknown */
  let applied = $state<string | null>(null);
  let applying = $state(false);
  let timer: ReturnType<typeof setTimeout> | null = null;
  let editor = $state<ReturnType<typeof FileTemplateEditor> | null>(null);
  let seq = 0;

  async function load() {
    if (!file) return;
    const my = ++seq;
    loadErr = null;
    // the content in the change set (pending bytes are read locally by fetchContent)
    const content = scope.config.files.find((x) => x.fileSlug === file.fileSlug)?.content ?? null;
    if (!content) {
      text = '';
      applied = '';
      return;
    }
    loading = true;
    try {
      const b = await fetchContent(content.sha256);
      const t = await b.text();
      if (my === seq) text = applied = t;
    } catch (e) {
      if (my === seq) loadErr = errMsg(e);
    } finally {
      if (my === seq) loading = false;
    }
  }
  let loadedFor = $state<string | null>(null);
  $effect(() => {
    if (open && file && loadedFor !== file.fileSlug) {
      loadedFor = file.fileSlug;
      untrack(() => void load());
    }
    if (!open && loadedFor) {
      loadedFor = null;
      untrack(() => void flush());
    }
  });
  /** the working FileDef (the change set) and the head's */
  const work = $derived(file ? (scope.config.files.find((x) => x.fileSlug === file.fileSlug) ?? null) : null);
  const headFile = $derived(file ? (scope.baseConfig.files.find((x) => x.fileSlug === file.fileSlug) ?? null) : null);
  const differsFromHead = $derived(!!work && (work.content?.sha256 ?? null) !== (headFile?.content?.sha256 ?? null));
  // a text edit → applied into the change set ~300 ms after the last keystroke
  $effect(() => {
    void text;
    if (!open || ro || loading || loadErr || applied === null || text === applied) return;
    if (timer) clearTimeout(timer);
    timer = setTimeout(() => void flush(), 300);
  });
  async function flush() {
    if (timer) {
      clearTimeout(timer);
      timer = null;
    }
    const f = file;
    if (!f || ro || applied === null || text === applied || utf8Bytes(text) > f.maxBytes) return;
    const t = text;
    applying = true;
    try {
      const ref = t === '' && !headFile?.content ? null : await scope.putBytes(new TextEncoder().encode(t));
      const w = scope.config.files.find((x) => x.fileSlug === f.fileSlug);
      if (w) w.content = ref;
      applied = t;
    } finally {
      applying = false;
    }
  }
  /** back to the head commit's content (drops this file's content change) */
  async function revert() {
    const f = file;
    if (!f) return;
    const w = scope.config.files.find((x) => x.fileSlug === f.fileSlug);
    if (w) w.content = headFile?.content ? { ...headFile.content } : null;
    loadedFor = null; // reload the text of the head content
    applied = null;
    await load();
    loadedFor = f.fileSlug;
  }

  const ro = $derived(!scope.canWrite);
  const size = $derived(utf8Bytes(text));
  const tooBig = $derived(!!file && size > file.maxBytes);
  const pendingApply = $derived(applied !== null && text !== applied);
  const lang = $derived(file && isJsonMime(file.mime) ? 'json' : file?.mime === 'text/html' ? 'html' : 'text');
  const exprOn = $derived(!!file && fileRenderMode(file) === 'expressions');

  // ───── expressions: live check + render once per site that mounts the file
  const cfg = $derived(open && file ? normalizeConfig(scope.working(), catalog.vctx(scope.mode, scope.deploymentId, scope.secretsSet).images) : null);
  const vctx = $derived(cfg ? previewVctx(catalog.vctx(scope.mode, scope.deploymentId, scope.secretsSet), cfg, scope.kind === 'template') : null);
  const siteMap = $derived(cfg && vctx ? siteExprMap(cfg, vctx) : undefined);
  const targets = $derived(cfg && vctx && file ? fileSites(cfg, vctx, file.fileSlug) : { mounting: [], all: [] });
  const syntax = $derived(exprOn && !loading && !loadErr ? checkFileContent(text) : []);
  const renders = $derived(cfg && vctx && file && exprOn && !syntax.length && !loading && !loadErr ? targets.mounting.map((t) => renderForSite(cfg, vctx, file, t, text, siteMap)).filter((r) => !!r) : []);
  type Issue = { line: number; col: number; code: string; message: string; where: string | null };
  const issues = $derived.by<Issue[]>(() => {
    const out: Issue[] = syntax.map((i) => ({ line: i.line, col: i.col, code: i.code, message: i.message, where: null }));
    for (const r of renders) {
      if (!r.out.ok) out.push({ line: r.out.line, col: r.out.col, code: r.out.code, message: r.out.message, where: `site ${r.target.siteSlug}` });
      else if (r.jsonError) out.push({ line: 0, col: 0, code: 'V_FILE_JSON', message: `the rendered file is not valid JSON: ${r.jsonError}`, where: `site ${r.target.siteSlug}` });
    }
    return out;
  });
  const marks = $derived<EditorMark[]>(issues.filter((i) => i.line > 0).map((i) => ({ line: i.line, col: i.col })));

  // ───── autocomplete paths (the previewed site, else the first mounting / any site)
  let previewCtx = $state<ExprContext | null>(null);
  const pathCtx = $derived.by<ExprContext | null>(() => {
    if (!cfg || !vctx || !file) return null;
    if (previewCtx) return previewCtx;
    return siteCtx(cfg, vctx, targets.mounting[0]?.siteSlug ?? null, siteMap);
  });
  const paths = $derived<PathSuggestion[]>([...exprPaths(pathCtx, 'file').map((p) => ({ label: p.label, detail: p.detail, kind: p.kind })), { label: '"{{"', detail: 'a literal {{', kind: 'escape' }]);

  function jump(line: number, col: number) {
    if (line > 0) editor?.jumpTo(line, col);
  }

</script>

<Drawer bind:open title="{ro ? 'Content of' : 'Edit content of'} {file?.fileSlug}" subtitle="{file?.mime} · max {bytes(file?.maxBytes)}{file?.content ? ` · current ${shortSha(file.content.sha256)} (${bytes(file.content.sizeBytes)})` : ' · no content yet'}" width="wide" testid="file-content-drawer">
  {#if loadErr}
    <ErrorState title="Could not load the content" message={loadErr} onretry={load} />
  {:else if loading}
    <div class="stack"><span class="skel" style="height:240px" data-testid="file-content-loading"></span></div>
  {:else if file}
    <div class="stack">
      <div class="row between small">
        {#if exprOn}
          <span class="muted" data-testid="file-render-note"><span class="badge outline">{'{{ }}'} on</span> Rendered once per site — every container on the site shares that copy. Literal <code class="mono">{'{{'}</code> = <code class="mono">{'{{ "{{" }}'}</code>.</span>
        {:else}
          <span class="muted" data-testid="file-render-note"><span class="badge outline">as is</span> Mounted byte for byte ({'{{ }}'} is not resolved).</span>
        {/if}
      </div>
      <FileTemplateEditor bind:this={editor} bind:value={text} {lang} expressions={exprOn} {paths} {marks} readonly={ro} invalid={tooBig} testid="file-content" label="File content" />
      <div class="row between small">
        <span class:text-red={tooBig} class="muted" data-testid="content-size">{bytes(size)} of {bytes(file.maxBytes)}{tooBig ? ' — too large' : ''}</span>
        {#if tooBig}<span class="badge red" data-testid="content-state">too large — not applied</span>{:else if pendingApply || applying}<span class="badge gray" data-testid="content-state">applying…</span>{:else if differsFromHead}<span class="badge amber" data-testid="content-state">changed — not committed</span>{/if}
      </div>
      {#if exprOn}
        {#if issues.length}
          <div class="issues" data-testid="file-issues">
            <div class="xs strong text-red">{issues.length} problem{issues.length === 1 ? '' : 's'}</div>
            <ul>
              {#each issues as i, k (k)}
                <li>
                  <button type="button" class="issue" onclick={() => jump(i.line, i.col)} disabled={i.line === 0} data-testid="file-issue">
                    <span class="mono xs loc">{file.fileSlug}{i.line ? `:${i.line}:${i.col}` : ''}</span>
                    <span class="mono xs ecode">{i.code}</span>
                    <span class="small">{i.message}{#if i.where}{' '}<span class="faint">({i.where})</span>{/if}</span>
                  </button>
                </li>
              {/each}
            </ul>
          </div>
        {:else if text.includes('{{')}
          <div class="xs muted" data-testid="file-issues-ok">Expressions OK{targets.mounting.length ? ` · renders on ${targets.mounting.length} site${targets.mounting.length === 1 ? '' : 's'}` : ''}.</div>
        {/if}
        {#if cfg && vctx}
          <FilePreview {cfg} {vctx} {file} {text} {targets} {siteMap} blocked={syntax.length > 0} onjump={jump} onctx={(c) => (previewCtx = c)} />
        {/if}
      {/if}
      {#if !ro}<p class="xs faint m0" data-testid="content-live-note">Changes go into this browser's change set as you type — nothing is uploaded until Commit… in the change bar.</p>{/if}
    </div>
  {/if}
  {#snippet footer()}
    {#if ro}
      <Button onclick={() => (open = false)} data-testid="close-content">Close</Button>
    {:else}
      {#if differsFromHead}<Button variant="ghost" onclick={revert} disabled={applying} data-testid="revert-content">Revert to committed</Button>{/if}
      <Button variant="primary" onclick={async () => { await flush(); open = false; }} loading={applying} data-testid="close-content">Done</Button>
    {/if}
  {/snippet}
</Drawer>

<style>
  .m0 {
    margin: 0;
  }
  .issues {
    border: 1px solid color-mix(in srgb, var(--red) 30%, var(--border));
    background: var(--red-soft);
    border-radius: var(--r-md);
    padding: 8px 10px;
  }
  .issues ul {
    list-style: none;
    margin: 4px 0 0;
    padding: 0;
  }
  .issue {
    display: flex;
    gap: 8px;
    align-items: baseline;
    width: 100%;
    text-align: left;
    background: none;
    border: 0;
    padding: 3px 4px;
    border-radius: var(--r-sm);
    cursor: pointer;
    color: var(--text);
  }
  .issue:hover:not(:disabled) {
    background: color-mix(in srgb, var(--red) 10%, transparent);
  }
  .issue:disabled {
    cursor: default;
  }
  .loc {
    color: var(--red);
    white-space: nowrap;
  }
  .ecode {
    color: var(--text-2);
    white-space: nowrap;
  }
</style>
