<script lang="ts">
  import { pendingCountOf } from '$lib/branch/pendingCount';
  import type { TemplateSummary, ValidationIssue } from '@opm/shared';
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import { api, errIssues } from '$lib/api';
  import { Loader } from '$lib/resource.svelte';
  import { session } from '$lib/session.svelte';
  import { toast } from '$lib/toast.svelte';
  import { slugIssues, validateTemplateMeta } from '$lib/util/validators';
  import SlugInput from '$lib/ui/SlugInput.svelte';
  import { slugifyLabel } from '@opm/shared';
  import { errorsAt, mergeIssues, issue } from '$lib/util/issues';
  import FromPicker, { blankFrom, fromIssue, fromPoint, type FromDraft } from '$lib/branch/FromPicker.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import Button from '$lib/ui/Button.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import Field from '$lib/ui/Field.svelte';
  import TableSkeleton from '$lib/ui/TableSkeleton.svelte';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import ErrorState from '$lib/ui/ErrorState.svelte';
  import SearchInput from '$lib/ui/SearchInput.svelte';
  import RelTime from '$lib/ui/RelTime.svelte';
  import IconView from '$lib/ui/IconView.svelte';
  import IconPicker from '$lib/ui/IconPicker.svelte';
  import FormSummary from '$lib/ui/FormSummary.svelte';
  import { Plus, LayoutTemplate } from '$lib/ui/icons';

  const list = new Loader(() => api.listTemplates({}).then((r) => r.items));
  onMount(() => list.load());
  const canWrite = $derived(session.can('templates', 'write'));
  let q = $state('');
  const rows = $derived((list.data ?? []).filter((t: TemplateSummary) => !q || `${t.label} ${t.description} ${t.siteSlugs.join(' ')}`.toLowerCase().includes(q.toLowerCase())));

  let open = $state(false);
  let f = $state<{ label: string; slug: string; slugManual: boolean; icon: string | null; description: string } & FromDraft>({ label: '', slug: '', slugManual: false, icon: null, description: '', ...blankFrom('empty') });
  let touched = $state(false);
  let saving = $state(false);
  let server = $state<ValidationIssue[]>([]);
  const local = $derived.by(() => {
    const out = validateTemplateMeta(f);
    slugIssues(f.slug, f.slugManual, out);
    const fi = fromIssue(f);
    if (fi) out.push(issue(fi.path, 'V_REQUIRED', fi.message));
    return out;
  });
  const all = $derived(mergeIssues(local, server));
  const show = (p: string) => (touched || server.length ? errorsAt(all, p) : []);
  function openNew() {
    f = { label: '', slug: '', slugManual: false, icon: null, description: '', ...blankFrom('empty') };
    touched = false;
    server = [];
    open = true;
  }
  async function create(e: Event) {
    e.preventDefault();
    touched = true;
    if (local.length) return;
    saving = true;
    server = [];
    try {
      const from = fromPoint(f);
      const t = await api.createTemplate({ body: { label: f.label.trim(), ...(f.slugManual && f.slug ? { slug: f.slug } : {}), icon: f.icon, ...(f.description ? { description: f.description } : {}), ...(from ? { from } : {}) } });
      toast.success(`Template ${t.label} created`);
      open = false;
      goto(`/app/templates/${t.templateId}`);
    } catch (err) {
      server = errIssues(err);
      toast.error(err, 'Not created');
    } finally {
      saving = false;
    }
  }
</script>

<svelte:head><title>Templates · OPM</title></svelte:head>

<div class="page">
  <PageHeader title="Templates" subtitle="Reusable blueprints: variables, secrets, files and sites, each a branch of commits. A deployment copies one at create; later commits reach it only through Compare." icon={LayoutTemplate} readOnly={!canWrite}>
    {#snippet actions()}
      <SearchInput bind:value={q} placeholder="Search templates" />
      {#if canWrite}<Button variant="primary" icon={Plus} onclick={openNew} data-testid="new-template">New template</Button>{/if}
    {/snippet}
  </PageHeader>

  {#if list.loading && !list.data}
    <TableSkeleton cols={9} />
  {:else if list.error}
    <ErrorState message={list.error} onretry={() => list.load()} />
  {:else if !list.data?.length}
    <EmptyState icon={LayoutTemplate} title="No templates yet" text="Create a template, add its sites and containers, then make deployments from it.">
      {#snippet action()}{#if canWrite}<Button variant="primary" icon={Plus} onclick={openNew}>New template</Button>{/if}{/snippet}
    </EmptyState>
  {:else if !rows.length}
    <EmptyState compact title="No match" text="No template matches “{q}”." />
  {:else}
    <div class="table-wrap">
      <table class="table" data-testid="templates-table">
        <thead><tr><th></th><th>Label</th><th>Head</th><th>Not committed</th><th>Sites</th><th class="num">Variables</th><th class="num">Secrets</th><th class="num">Files</th><th class="num">Used by</th><th>Updated</th></tr></thead>
        <tbody>
          {#each rows as t (t.templateId)}
            <tr class="clickable" onclick={() => goto(`/app/templates/${t.templateId}`)} data-testid="template-row">
              <td style="width:36px"><IconView value={t.icon} type="template" size={18} /></td>
              <td><a href="/app/templates/{t.templateId}" class="strong" onclick={(e) => e.stopPropagation()}>{t.label}</a>{#if t.description}<div class="xs faint ellipsis" style="max-width:340px">{t.description}</div>{/if}</td>
              <td data-testid="tpl-head">{#if t.branch?.head}<span class="row" style="gap:6px;min-width:0"><span class="badge outline mono">{t.branch.head.seq}</span><span class="small ellipsis" style="max-width:220px">{t.branch.head.message.split('\n')[0]}</span></span>{:else}<span class="badge accent">v{t.version}</span>{/if}</td>
              <td data-testid="tpl-uncommitted">{#if pendingCountOf(`template:${t.templateId}`)}<span class="badge amber" data-tip="changes kept in this browser">{pendingCountOf(`template:${t.templateId}`)}</span>{:else}<span class="faint small">—</span>{/if}</td>
              <td><div class="row wrap" style="gap:4px">{#each t.siteSlugs as s (s)}<span class="chip mono">{s}</span>{:else}<span class="faint small">none</span>{/each}</div></td>
              <td class="num">{t.counts.variables}</td>
              <td class="num">{t.counts.secrets}</td>
              <td class="num">{t.counts.files}</td>
              <td class="num">{t.usedBy}</td>
              <td class="small muted"><RelTime ts={t.updatedAt} /></td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>

<Modal bind:open title="New template" subtitle="Empty, or a copy of another template, deployment or agent branch (its first commit records where it came from)." busy={saving} testid="template-modal">
  <form id="tpl-form" class="stack" onsubmit={create} novalidate>
    <FormSummary issues={server} />
    <Field label="Label" id="nt-label" required error={show('label')}><input id="nt-label" class="input" bind:value={f.label} maxlength="80" placeholder="Hello world" data-testid="nt-label" /></Field>
    <Field label="Slug" id="nt-slug" hint={`Used as the GitHub branch name: template/${f.slug || '<slug>'} · fixed after create`} error={show('slug')}>
      <SlugInput bind:value={f.slug} bind:manual={f.slugManual} from={f.label} derive={(l) => (l.trim() ? slugifyLabel(l) : '')} id="nt-slug" testid="nt-slug" placeholder="hello-world" />
    </Field>
    <Field label="Icon" id="nt-icon" hint="Optional — none = the default template icon" error={show('icon')}>
      <IconPicker bind:value={f.icon} type="template" id="nt-icon" testid="nt-icon" />
    </Field>
    <Field label="Description" id="nt-desc" error={show('description')}><textarea id="nt-desc" class="textarea" rows="3" bind:value={f.description}></textarea></Field>
    <FromPicker bind:value={f} target="template" testid="nt" errors={{ templateId: show('templateId'), deploymentId: show('from.deploymentId'), siteSlug: show('from.siteSlug') }} />
  </form>
  {#snippet footer()}
    <Button onclick={() => (open = false)} disabled={saving}>Cancel</Button>
    <Button type="submit" form="tpl-form" variant="primary" loading={saving} disabled={touched && local.length > 0} data-testid="nt-create">Create template</Button>
  {/snippet}
</Modal>

