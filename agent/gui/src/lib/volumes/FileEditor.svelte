<script lang="ts">
  /**
   * §21 #96 edit (or create) a small text file in a volume: ≤ 1 MiB, a diff preview before saving, a conflict when the
   * file changed on the server since it was opened (baseMtime).
   */
  import { tools } from '$lib/tools/api';
  import { errCode, errMessage } from '$lib/api';
  import { bytes } from '$lib/format';
  import Drawer from '$lib/ui/Drawer.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import CodeEditor from '$lib/ui/CodeEditor.svelte';
  import { hunks, lineDiff } from './vol';

  interface Props {
    open: boolean;
    siteSlug: string;
    volumeSlug: string;
    /** the file to edit; null = create a new file in `dir` */
    path: string | null;
    dir?: string;
    onsaved: (path: string) => void;
  }
  let { open = $bindable(false), siteSlug, volumeSlug, path, dir = '/', onsaved }: Props = $props();
  let original = $state('');
  let text = $state('');
  let mtime = $state<string | null>(null);
  let name = $state('');
  let loading = $state(false);
  let err = $state<string | null>(null);
  let review = $state(false);
  let saving = $state(false);
  let conflict = $state(false);
  const MAX = 1 << 20;

  async function load() {
    err = null;
    review = false;
    conflict = false;
    if (path === null) {
      original = text = '';
      mtime = null;
      name = '';
      return;
    }
    loading = true;
    try {
      const f = await tools.volumeFile({ siteSlug, volumeSlug, query: { path } });
      if (f.text === null) {
        err = f.binary ? 'This file is not text: download it, edit it, upload it again.' : 'Files up to 1 MiB can be edited here: download it, edit it, upload it again.';
        original = text = '';
      } else {
        original = text = f.text;
      }
      mtime = f.mtime;
    } catch (e) {
      err = errMessage(e);
    } finally {
      loading = false;
    }
  }
  $effect(() => {
    if (open) void load();
  });
  const target = $derived(path ?? `${dir === '/' ? '' : dir}/${name.trim()}`);
  const nameProblem = $derived(path === null ? (!name.trim() ? 'a file name' : /[\0\\]/.test(name) || name.trim() === '.' || name.trim() === '..' || name.includes('/') ? 'no / \\ . ..' : null) : null);
  const size = $derived(new TextEncoder().encode(text).length);
  const changed = $derived(text !== original || path === null);
  const diff = $derived(review ? hunks(lineDiff(original, text)) : []);
  const stats = $derived.by(() => {
    if (!review) return { add: 0, del: 0 };
    const d = lineDiff(original, text);
    return { add: d.filter((l) => l.kind === 'add').length, del: d.filter((l) => l.kind === 'del').length };
  });

  async function save() {
    saving = true;
    err = null;
    try {
      await tools.writeVolumeFile({ siteSlug, volumeSlug, body: { path: target, text, baseMtime: path === null ? null : mtime } });
      open = false;
      onsaved(target);
    } catch (e) {
      if (errCode(e) === 'conflict') conflict = true;
      err = errMessage(e);
    } finally {
      saving = false;
    }
  }
</script>

<Drawer bind:open wide title={path === null ? 'New file' : (path.split('/').pop() ?? path)} subtitle="{volumeSlug}:{target}{path !== null ? ' · edit' : ''}" testid="file-editor">
  {#if loading}
    <span class="skel" style="height:300px"></span>
  {:else}
    <div class="stack">
      {#if path === null}
        <div class="field">
          <label for="new-name">File name <span class="faint">in {dir}</span></label>
          <input id="new-name" class="input mono" bind:value={name} autocomplete="off" spellcheck="false" data-testid="new-file-name" placeholder="config.env" />
          {#if name && nameProblem}<div class="field-err">{nameProblem}</div>{/if}
        </div>
      {/if}
      {#if review}
        <div class="row between small"><span><span class="ok-text">+{stats.add}</span> <span class="bad-text">−{stats.del}</span> lines</span><Btn size="sm" onclick={() => (review = false)}>Back to editing</Btn></div>
        <div class="diff" data-testid="file-diff">
          {#if diff.length === 0}<div class="muted small" style="padding:10px">No change.</div>{/if}
          {#each diff as l, i (i)}
            {#if l === null}<div class="gap">…</div>{:else}<div class="dl {l.kind}"><span class="no">{l.a ?? ''}</span><span class="no">{l.b ?? ''}</span><span class="sg">{l.kind === 'add' ? '+' : l.kind === 'del' ? '−' : ' '}</span><span class="tx">{l.text}</span></div>{/if}
          {/each}
        </div>
      {:else if !err || conflict}
        <CodeEditor bind:value={text} maxBytes={MAX} label="Content of {target}" />
      {/if}
      <div class="tiny muted">{bytes(size)} of 1 MiB{#if mtime} · loaded version {mtime}{/if}. New files get the owner of their folder.</div>
      {#if err}<div class="banner {conflict ? 'warn' : 'bad'} small" data-testid="file-editor-err"><Icon name="alert" /><div class="grow">{err}{#if conflict} <button class="linklike" onclick={load}>Reload the server version</button> (your edits are lost) or copy them first.{/if}</div></div>{/if}
    </div>
  {/if}
  {#snippet footer()}
    <Btn onclick={() => (open = false)}>Cancel</Btn>
    {#if !review}
      <Btn disabled={!changed || size > MAX || !!nameProblem || (!!err && !conflict)} onclick={() => (review = true)} data-testid="file-review">Review changes</Btn>
    {:else}
      <Btn variant="primary" icon="check" busy={saving} disabled={size > MAX || !!nameProblem} onclick={save} data-testid="file-save">Save to the server</Btn>
    {/if}
  {/snippet}
</Drawer>

<style>
  .diff {
    font-family: var(--font-mono);
    font-size: 12px;
    line-height: 1.6;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    background: var(--code-bg);
    overflow: auto;
    max-height: 64vh;
  }
  .dl {
    display: flex;
    white-space: pre;
  }
  .dl.add {
    background: color-mix(in srgb, var(--green) 14%, transparent);
  }
  .dl.del {
    background: color-mix(in srgb, var(--red) 14%, transparent);
  }
  .no {
    flex: none;
    width: 40px;
    text-align: right;
    padding-right: 8px;
    color: var(--text-3);
    user-select: none;
  }
  .sg {
    width: 16px;
    flex: none;
    user-select: none;
  }
  .gap {
    color: var(--text-3);
    padding: 0 12px;
    background: var(--surface-2);
  }
  .linklike {
    all: unset;
    cursor: pointer;
    color: var(--accent-text);
    text-decoration: underline;
  }
</style>
