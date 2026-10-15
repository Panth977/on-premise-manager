<script lang="ts">
  import { untrack } from 'svelte';
  import type { NotFoundPageDetail } from '@opm/shared';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { api } from '$lib/api';
  import { Loader } from '$lib/resource.svelte';
  import { session } from '$lib/session.svelte';
  import { toast } from '$lib/toast.svelte';
  import { catalog } from '$lib/stores/catalog.svelte';
  import { bytes } from '$lib/util/format';
  import { utf8Bytes } from '$lib/util/validators';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import HistoryButton from '$lib/audit/HistoryButton.svelte';
  import Button from '$lib/ui/Button.svelte';
  import ErrorState from '$lib/ui/ErrorState.svelte';
  import ConfirmDialog from '$lib/ui/ConfirmDialog.svelte';
  import CodeEditor from '$lib/ui/CodeEditor.svelte';
  import RelTime from '$lib/ui/RelTime.svelte';
  import IdChip from '$lib/ui/IdChip.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import NewPageModal from '$lib/pages404/NewPageModal.svelte';
  import { Copy, Trash2, Monitor, Smartphone } from 'lucide';
  import { FileCode } from '$lib/ui/icons';

  const id = $derived(page.params.notFoundPageId!);
  const p = new Loader<NotFoundPageDetail>(() => api.getNotFoundPage({ notFoundPageId: id }));
  $effect(() => {
    void id;
    untrack(() => {
      p.load();
      catalog.ensure();
    });
  });
  const canWrite = $derived(session.can('notFoundPages', 'write'));
  let width = $state<'desktop' | 'mobile'>('desktop');
  let copyOpen = $state(false);
  let delOpen = $state(false);
  async function doDelete() {
    await api.deleteNotFoundPage({ notFoundPageId: id });
    catalog.invalidate();
    toast.success('Page deleted');
    goto('/app/not-found-pages');
  }
  const who = (uid: string) => catalog.accounts.find((a) => a.uid === uid)?.name ?? uid;
</script>

<svelte:head><title>{p.data?.label ?? 'Not-found page'} · OPM</title></svelte:head>
<div class="page">
  {#if p.loading && !p.data}
    <div class="stack"><span class="skel" style="width:260px;height:22px"></span><span class="skel" style="width:100%;height:320px"></span></div>
  {:else if p.error}
    <PageHeader title="Not-found page" back={{ href: '/app/not-found-pages', label: 'Not-found pages' }} />
    <ErrorState message={p.error} onretry={() => p.load()} />
  {:else if p.data}
    {@const d = p.data}
    <PageHeader title={d.label} icon={FileCode} back={{ href: '/app/not-found-pages', label: 'Not-found pages' }} readOnly>
      <div class="row wrap small muted" style="margin-top:6px;gap:14px">
        <span>{bytes(utf8Bytes(d.htmlCode))}</span><span>by {who(d.createdBy)} · <RelTime ts={d.createdAt} /></span><IdChip id={d.notFoundPageId} label="id" />
      </div>
      {#snippet actions()}
        <HistoryButton target="notFoundPages/{d.notFoundPageId}" title="History of {d.label}" />
        {#if canWrite}
          <Button icon={Copy} onclick={() => (copyOpen = true)} data-testid="copy-page">Create a copy</Button>
          <span data-tip={d.usedBy.length ? `Used by ${d.usedBy.length} site(s)` : undefined}><Button icon={Trash2} variant="danger-outline" disabled={d.usedBy.length > 0} onclick={() => (delOpen = true)} data-testid="delete-page">Delete</Button></span>
        {/if}
      {/snippet}
    </PageHeader>
    <div class="grid">
      <section class="card">
        <div class="card-head"><h3>Preview</h3><span class="faint small">sandboxed, no same-origin</span><span class="spacer"></span>
          <div class="seg" role="radiogroup" aria-label="Preview width">
            <button type="button" role="radio" aria-checked={width === 'desktop'} class:on={width === 'desktop'} onclick={() => (width = 'desktop')} aria-label="Desktop"><Icon icon={Monitor} size={14} /></button>
            <button type="button" role="radio" aria-checked={width === 'mobile'} class:on={width === 'mobile'} onclick={() => (width = 'mobile')} aria-label="Mobile"><Icon icon={Smartphone} size={14} /></button>
          </div>
        </div>
        <div class="stage"><iframe title="Preview of {d.label}" sandbox="allow-scripts" srcdoc={d.htmlCode} class:mobile={width === 'mobile'} data-testid="page-preview"></iframe></div>
      </section>
      <section class="stack">
        <div class="card">
          <div class="card-head"><h3>Used by</h3><span class="badge">{d.usedBy.length}</span></div>
          <div class="card-body">
            {#if !d.usedBy.length}<p class="small muted">No site uses this page. You can delete it.</p>{:else}
              <ul class="ul">{#each d.usedBy as u, i (i)}<li><span class="badge {u.kind === 'template' ? 'violet' : 'blue'}">{u.kind}</span> <a href="/app/{u.kind === 'template' ? 'templates' : 'deployments'}/{u.id}">{u.label}</a> · <span class="mono small">{u.siteSlug}</span></li>{/each}</ul>
            {/if}
          </div>
        </div>
      </section>
    </div>
    <div class="section">
      <div class="section-title"><h3>HTML</h3><span class="faint small">read only</span></div>
      <CodeEditor value={d.htmlCode} lang="html" readonly minHeight="200px" height="420px" testid="page-code" />
    </div>
  {/if}
</div>
<NewPageModal bind:open={copyOpen} prefill={p.data ? { label: `Copy of ${p.data.label}`.slice(0, 80), htmlCode: p.data.htmlCode } : null} oncreated={(x) => goto(`/app/not-found-pages/${x.notFoundPageId}`)} />
<ConfirmDialog bind:open={delOpen} title="Delete {p.data?.label}?" message="The page is not used by any site. This cannot be undone." confirmLabel="Delete" danger onconfirm={doDelete} />

<style>
  .grid {
    display: grid;
    grid-template-columns: minmax(0, 2fr) minmax(0, 1fr);
    gap: var(--sp-4);
    align-items: start;
  }
  @media (max-width: 1000px) {
    .grid {
      grid-template-columns: minmax(0, 1fr);
    }
  }
  .stage {
    padding: 16px;
    background: var(--bg-sunken);
    display: flex;
    justify-content: center;
    border-radius: 0 0 var(--r-lg) var(--r-lg);
  }
  iframe {
    width: 100%;
    height: 420px;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    background: #fff;
    transition: width var(--dur);
  }
  iframe.mobile {
    width: 375px;
    max-width: 100%;
    height: 560px;
  }
  .seg {
    display: inline-flex;
    border: 1px solid var(--border-strong);
    border-radius: var(--r-md);
    overflow: hidden;
  }
  .seg button {
    border: 0;
    background: var(--surface);
    padding: 4px 10px;
    cursor: pointer;
    color: var(--text-2);
    display: grid;
    place-items: center;
  }
  .seg button.on {
    background: var(--accent-soft);
    color: var(--accent-text);
  }
  .ul {
    margin: 0;
    padding: 0;
    list-style: none;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
</style>
