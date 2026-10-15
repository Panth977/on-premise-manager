<script lang="ts">
  import type { TagListItem, ValidationIssue } from '@opm/shared';
  import { onMount, tick } from 'svelte';
  import { api, errIssues, isApiError } from '$lib/api';
  import { Loader } from '$lib/resource.svelte';
  import { session } from '$lib/session.svelte';
  import { toast } from '$lib/toast.svelte';
  import { catalog } from '$lib/stores/catalog.svelte';
  import { validateTag } from '$lib/util/validators';
  import { errorsAt, mergeIssues, issue } from '$lib/util/issues';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import HistoryButton from '$lib/audit/HistoryButton.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import Field from '$lib/ui/Field.svelte';
  import ConfirmDialog from '$lib/ui/ConfirmDialog.svelte';
  import TableSkeleton from '$lib/ui/TableSkeleton.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import ErrorState from '$lib/ui/ErrorState.svelte';
  import SearchInput from '$lib/ui/SearchInput.svelte';
  import FormSummary from '$lib/ui/FormSummary.svelte';
  import IconView from '$lib/ui/IconView.svelte';
  import IconPicker from '$lib/ui/IconPicker.svelte';
  import SlugInput from '$lib/ui/SlugInput.svelte';
  import ColorPicker, { SWATCHES } from '$lib/ui/ColorPicker.svelte';
  import { Plus, Pencil, Trash2, Tags } from '$lib/ui/icons';
  import { page } from '$app/state';

  const list = new Loader(() => api.listTags({}).then((r) => r.items));
  onMount(() => list.load());
  /** ?tag=… (audit-log links) highlights that row */
  const hl = $derived(page.url.searchParams.get('tag'));
  $effect(() => {
    if (!hl || !list.data) return;
    tick().then(() => document.getElementById(`tag-${hl}`)?.scrollIntoView({ block: 'center' }));
  });
  const canWrite = $derived(session.can('tags', 'write'));
  let q = $state('');
  const rows = $derived((list.data ?? []).filter((t) => !q || `${t.tagSlug} ${t.label}`.toLowerCase().includes(q.toLowerCase())));

  let open = $state(false);
  let editing = $state<TagListItem | null>(null);
  let f = $state<{ tagSlug: string; label: string; icon: string | null; color: string }>({ tagSlug: '', label: '', icon: null, color: '#4f46e5' });
  let touched = $state(false);
  let saving = $state(false);
  let server = $state<ValidationIssue[]>([]);
  const local = $derived(validateTag(f, !editing));
  const issues = $derived(mergeIssues(local, server));
  const show = (p: string) => (touched || server.length ? errorsAt(issues, p) : []);
  function openNew() {
    editing = null;
    f = { tagSlug: '', label: '', icon: null, color: SWATCHES[Math.floor(Math.random() * SWATCHES.length)]! };
    touched = false;
    server = [];
    open = true;
  }
  function openEdit(t: TagListItem) {
    editing = t;
    f = { tagSlug: t.tagSlug, label: t.label, icon: t.icon, color: t.color };
    touched = false;
    server = [];
    open = true;
  }
  async function save(e: Event) {
    e.preventDefault();
    touched = true;
    if (local.length) return;
    saving = true;
    server = [];
    try {
      if (editing) {
        const body: { label?: string; icon?: string | null; color?: string } = {};
        if (f.label !== editing.label) body.label = f.label.trim();
        if (f.icon !== editing.icon) body.icon = f.icon;
        if (f.color !== editing.color) body.color = f.color;
        if (Object.keys(body).length) await api.patchTag({ tagSlug: editing.tagSlug, body });
        toast.success('Tag saved');
      } else {
        await api.createTag({ body: { tagSlug: f.tagSlug, label: f.label.trim(), icon: f.icon, color: f.color } });
        toast.success(`Tag ${f.tagSlug} created`);
      }
      catalog.invalidate();
      open = false;
      list.load(true);
    } catch (err) {
      if (isApiError(err) && err.code === 'conflict') server = [issue('tagSlug', 'V_DUPLICATE', 'This tag slug exists')];
      else server = errIssues(err);
      toast.error(err, 'Not saved');
    } finally {
      saving = false;
    }
  }
  let del = $state<TagListItem | null>(null);
  let delOpen = $state(false);
  async function doDelete() {
    const r = await api.deleteTag({ tagSlug: del!.tagSlug });
    catalog.invalidate();
    toast.success(`Tag ${del!.tagSlug} deleted`, `Removed from ${r.removedFrom.deployments} deployment(s) and ${r.removedFrom.accessRules} access rule(s).`);
    list.load(true);
  }
</script>

<svelte:head><title>Tags · OPM</title></svelte:head>
<div class="page">
  <PageHeader title="Tags" subtitle="Group deployments (filters) and scope access rules (e.g. a team gets every deployment tagged pilot)." icon={Tags} readOnly={!canWrite}>
    {#snippet actions()}
      <SearchInput bind:value={q} placeholder="Search tags" />
      {#if canWrite}<Button variant="primary" icon={Plus} onclick={openNew} data-testid="new-tag">New tag</Button>{/if}
    {/snippet}
  </PageHeader>
  {#if list.loading && !list.data}
    <TableSkeleton cols={6} />
  {:else if list.error}
    <ErrorState message={list.error} onretry={() => list.load()} />
  {:else if !list.data?.length}
    <EmptyState icon={Tags} title="No tags yet" text="Tags group deployments and scope access rules.">
      {#snippet action()}{#if canWrite}<Button variant="primary" icon={Plus} onclick={openNew}>New tag</Button>{/if}{/snippet}
    </EmptyState>
  {:else if !rows.length}
    <EmptyState compact title="No match" text="No tag matches “{q}”." />
  {:else}
    <div class="table-wrap">
      <table class="table" data-testid="tags-table">
        <thead><tr><th>Tag</th><th>Label</th><th>Icon</th><th>Color</th><th class="num">Deployments</th><th class="actions"><span class="sr-only">Actions</span></th></tr></thead>
        <tbody>
          {#each rows as t (t.tagSlug)}
            <tr data-testid="tag-row" class:hl={hl === t.tagSlug} id="tag-{t.tagSlug}">
              <td><span class="tchip" style="--c:{t.color}"><IconView value={t.icon} type="tag" size={12} />{t.tagSlug}</span></td>
              <td class="strong">{t.label}</td>
              <td><span class="row small muted"><IconView value={t.icon} type="tag" size={16} />{#if t.icon}<code class="mono">{t.icon.startsWith('upload:') ? 'uploaded' : t.icon.replace('lucide:', '')}</code>{:else}<span class="faint">default</span>{/if}</span></td>
              <td><span class="row small"><span class="sw" style="background:{t.color}"></span><code class="mono">{t.color}</code></span></td>
              <td class="num"><a href="/app/deployments?tag={t.tagSlug}">{t.deployments}</a></td>
              <td class="actions">
                <HistoryButton target="tags/{t.tagSlug}" title="History of tag {t.tagSlug}" iconOnly size="sm" />
                {#if canWrite}
                  <Button size="sm" variant="ghost" icon={Pencil} tip="Edit" aria-label="Edit {t.tagSlug}" onclick={() => openEdit(t)} />
                  <Button size="sm" variant="ghost" icon={Trash2} tip="Delete" aria-label="Delete {t.tagSlug}" onclick={() => ((del = t), (delOpen = true))} />
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>

<Modal bind:open title={editing ? `Edit tag ${editing.tagSlug}` : 'New tag'} busy={saving} testid="tag-modal">
  <form id="tag-form" class="stack" onsubmit={save} novalidate>
    <FormSummary issues={server} />
    <Field label="Label" id="t-label" required error={show('label')}><input id="t-label" class="input" bind:value={f.label} maxlength="80" placeholder="Pilot" data-testid="tag-label" /></Field>
    <Field label="Tag (id)" id="t-slug" required hint={editing ? 'Fixed after create' : 'From the label · fixed after create'} error={show('tagSlug')}>
      <SlugInput bind:value={f.tagSlug} from={f.label} fixed={!!editing} id="t-slug" placeholder="pilot" testid="tag-slug" invalid={show('tagSlug').length > 0} />
    </Field>
    <div class="grid-2">
      <Field label="Icon" id="t-icon" hint="Optional" error={show('icon')}><IconPicker bind:value={f.icon} type="tag" id="t-icon" testid="tag-icon" /></Field>
      <Field label="Preview">
        <div class="pvw"><span class="tchip" style="--c:{f.color}"><IconView value={f.icon} type="tag" size={12} />{f.tagSlug || 'preview'}</span></div>
      </Field>
    </div>
    <Field label="Color" id="t-color" required error={show('color')}><ColorPicker bind:value={f.color} id="t-color" testid="tag-color" invalid={show('color').length > 0} /></Field>
  </form>
  {#snippet footer()}
    <Button onclick={() => (open = false)} disabled={saving}>Cancel</Button>
    <Button type="submit" form="tag-form" variant="primary" loading={saving} disabled={touched && local.length > 0} data-testid="tag-save">{editing ? 'Save' : 'Create tag'}</Button>
  {/snippet}
</Modal>

<ConfirmDialog bind:open={delOpen} title="Delete tag {del?.tagSlug}?" message="It is removed from {del?.deployments ?? 0} deployment(s) and from every access rule scope that names it. People who had access only through this tag lose it." confirmLabel="Delete tag" danger onconfirm={doDelete} />

<style>
  tr.hl :global(td) {
    background: var(--accent-soft);
  }
  .tchip {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    height: 22px;
    padding: 0 8px;
    border-radius: 999px;
    font-size: var(--fs-xs);
    font-weight: 600;
    color: var(--c);
    background: color-mix(in srgb, var(--c) 14%, transparent);
    border: 1px solid color-mix(in srgb, var(--c) 30%, transparent);
  }
  .sw {
    width: 14px;
    height: 14px;
    border-radius: 4px;
    border: 1px solid var(--border);
  }
  .pvw {
    min-height: var(--control-h);
    display: flex;
    align-items: center;
  }
</style>
