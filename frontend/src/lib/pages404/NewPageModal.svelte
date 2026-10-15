<script lang="ts">
  import type { NotFoundPage, ValidationIssue } from '@opm/shared';
  import { untrack } from 'svelte';
  import { LIMITS } from '@opm/shared';
  import { api, errIssues, isApiError } from '../api';
  import { toast } from '../toast.svelte';
  import { catalog } from '../stores/catalog.svelte';
  import { validateNotFoundPage, utf8Bytes } from '../util/validators';
  import { errorsAt, mergeIssues, issue } from '../util/issues';
  import { bytes } from '../util/format';
  import Modal from '../ui/Modal.svelte';
  import Button from '../ui/Button.svelte';
  import Field from '../ui/Field.svelte';
  import FormSummary from '../ui/FormSummary.svelte';
  import CodeEditor from '../ui/CodeEditor.svelte';
  import Icon from '../ui/Icon.svelte';
  import { Info } from '../ui/icons';
  import PageThumb from './PageThumb.svelte';
  import { STARTERS, pageHtml } from './page-html';

  type Props = { open: boolean; prefill?: { label: string; htmlCode: string } | null; oncreated?: (p: NotFoundPage) => void };
  let { open = $bindable(), prefill = null, oncreated }: Props = $props();
  const STARTER = STARTERS[0]!.html;
  let start = $state('simple');
  let pageHtmls = $state<Record<string, string | null>>({});
  function startFrom(id: string) {
    start = id;
    const st = STARTERS.find((x) => x.id === id);
    if (st) html = st.html;
    else if (pageHtmls[id]) html = pageHtmls[id]!;
  }
  let label = $state('');
  let html = $state('');
  let touched = $state(false);
  let saving = $state(false);
  let server = $state<ValidationIssue[]>([]);
  let preview = $state(false);
  $effect(() => {
    if (open) {
      label = prefill?.label ?? '';
      html = prefill?.htmlCode ?? STARTER;
      touched = false;
      server = [];
      preview = false;
      start = prefill ? '' : 'simple';
      untrack(() => {
        for (const p of catalog.pages) if (!(p.notFoundPageId in pageHtmls)) pageHtml(p.notFoundPageId).then((h) => (pageHtmls[p.notFoundPageId] = h));
      });
    }
  });
  const size = $derived(utf8Bytes(html));
  const local = $derived(validateNotFoundPage({ label, htmlCode: html }));
  const issues = $derived(mergeIssues(local, server));
  const show = (p: string) => (touched || server.length ? errorsAt(issues, p) : []);
  async function save(e: Event) {
    e.preventDefault();
    touched = true;
    if (local.length) return;
    saving = true;
    server = [];
    try {
      const p = await api.createNotFoundPage({ body: { label: label.trim(), htmlCode: html } });
      catalog.invalidate();
      toast.success(`Page ${p.label} created`);
      open = false;
      oncreated?.(p);
    } catch (err) {
      server = isApiError(err) && err.code === 'too-large' ? [issue('htmlCode', 'V_LENGTH', err.message)] : errIssues(err);
      toast.error(err, 'Not created');
    } finally {
      saving = false;
    }
  }
</script>

<Modal bind:open title={prefill ? 'Create a copy' : 'New not-found page'} size="xl" busy={saving} testid="page-modal">
  <form id="nfp-form" class="stack" onsubmit={save} novalidate>
    <div class="banner"><Icon icon={Info} size={15} class="icon" /><div class="small">One self-contained document: inline CSS, JS and images (data: URLs). <b>Pages cannot be edited later.</b></div></div>
    <FormSummary issues={server} />
    <Field label="Label" id="nfp-label" required error={show('label')}><input id="nfp-label" class="input" bind:value={label} maxlength="80" placeholder="Branded 404" data-testid="nfp-label" /></Field>
    {#if !prefill}
      <Field label="Start from" id="nfp-start" hint="Replaces the code below">
        <div class="starts" role="radiogroup" aria-label="Start from">
          {#each STARTERS as st (st.id)}
            <button type="button" role="radio" aria-checked={start === st.id} class="st" class:sel={start === st.id} onclick={() => startFrom(st.id)} data-testid="nfp-start-{st.id}"><PageThumb html={st.html} width={120} label={st.label} /><span class="xs">{st.label}</span></button>
          {/each}
          {#each catalog.pages as p (p.notFoundPageId)}
            <button type="button" role="radio" aria-checked={start === p.notFoundPageId} class="st" class:sel={start === p.notFoundPageId} disabled={!pageHtmls[p.notFoundPageId]} onclick={() => startFrom(p.notFoundPageId)}><PageThumb html={pageHtmls[p.notFoundPageId] ?? null} width={120} label={p.label} /><span class="xs ellipsis">Copy of {p.label}</span></button>
          {/each}
        </div>
      </Field>
    {/if}
    <Field label="HTML code" id="nfp-html" required error={show('htmlCode')}>
      {#snippet aside()}<span class:over={size > LIMITS.notFoundPageBytes} data-testid="nfp-bytes">{bytes(size)} / 256 KiB ({size.toLocaleString()} bytes)</span> · <button type="button" class="lnk" onclick={() => (preview = !preview)}>{preview ? 'Code' : 'Preview'}</button>{/snippet}
      {#if preview}
        <iframe class="pv" title="Preview" sandbox="allow-scripts" srcdoc={html}></iframe>
      {:else}
        <CodeEditor bind:value={html} lang="html" minHeight="320px" height="380px" invalid={show('htmlCode').length > 0} testid="nfp-code" />
      {/if}
    </Field>
  </form>
  {#snippet footer()}
    <Button onclick={() => (open = false)} disabled={saving}>Cancel</Button>
    <Button type="submit" form="nfp-form" variant="primary" loading={saving} disabled={touched && local.length > 0} data-testid="nfp-save">Create page</Button>
  {/snippet}
</Modal>

<style>
  .starts {
    display: flex;
    gap: 8px;
    overflow-x: auto;
    padding: 2px 2px 6px;
  }
  .st {
    display: flex;
    flex-direction: column;
    gap: 4px;
    align-items: flex-start;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    padding: 4px;
    background: var(--surface);
    cursor: pointer;
    color: var(--text);
    max-width: 132px;
    flex: none;
  }
  .st.sel {
    border-color: var(--accent);
    box-shadow: var(--focus-ring);
  }
  .st:disabled {
    opacity: 0.5;
  }
  .over {
    color: var(--red-text);
    font-weight: 600;
  }
  .lnk {
    border: 0;
    background: transparent;
    color: var(--accent-text);
    cursor: pointer;
    padding: 0;
    font-size: inherit;
  }
  .pv {
    width: 100%;
    height: 380px;
    border: 1px solid var(--border-strong);
    border-radius: var(--r-md);
    background: #fff;
  }
</style>
