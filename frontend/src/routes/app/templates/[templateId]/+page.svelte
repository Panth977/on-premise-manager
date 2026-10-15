<script lang="ts">
  import { goto } from '$app/navigation';
  import { getTemplateScope } from '$lib/config-editor/context';
  import SiteGraph from '$lib/config-editor/SiteGraph.svelte';
  import { saveAs } from '$lib/config-editor/files';
  import { api } from '$lib/api';
  import { toast } from '$lib/toast.svelte';
  import { useLeaveGuard } from '$lib/util/guard.svelte';
  import { validateTemplateMeta } from '$lib/util/validators';
  import { errorsAt, mergeIssues } from '$lib/util/issues';
  import Button from '$lib/ui/Button.svelte';
  import Field from '$lib/ui/Field.svelte';
  import IssuesList from '$lib/ui/IssuesList.svelte';
  import ConfirmDialog from '$lib/ui/ConfirmDialog.svelte';
  import CopyButton from '$lib/ui/CopyButton.svelte';
  import IconPicker from '$lib/ui/IconPicker.svelte';
  import FormSummary from '$lib/ui/FormSummary.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import IdChip from '$lib/ui/IdChip.svelte';
  import RelTime from '$lib/ui/RelTime.svelte';
  import HistoryButton from '$lib/audit/HistoryButton.svelte';
  import { countBy } from '$lib/util/issues';
  import { Trash2, Download, ChevronDown, ChevronRight, Network, ShieldCheck } from '$lib/ui/icons';
  import type { ValidationIssue } from '@opm/shared';

  const scope = getTemplateScope();
  const t = $derived(scope.template!);
  let label = $state('');
  let icon = $state<string | null>(null);
  let description = $state('');
  let synced = $state(-1);
  $effect(() => {
    if (t && synced !== t.version) {
      label = t.label;
      icon = t.icon ?? null;
      description = t.description;
      synced = t.version;
    }
  });
  const dirty = $derived(!!t && (label !== t.label || icon !== (t.icon ?? null) || description !== t.description));
  useLeaveGuard(() => dirty);
  const local = $derived(validateTemplateMeta({ label, icon, description }));
  const all = $derived(mergeIssues(local, scope.saveIssues.filter((i: ValidationIssue) => ['label', 'icon', 'description'].includes(i.path))));
  async function save(e: Event) {
    e.preventDefault();
    if (local.length || !dirty) return;
    const body: { label?: string; icon?: string | null; description?: string } = {};
    if (label !== t.label) body.label = label.trim();
    if (icon !== (t.icon ?? null)) body.icon = icon;
    if (description !== t.description) body.description = description;
    await scope.saveMeta(body);
  }
  const issues = $derived(scope.validate().list);
  const counts = $derived(countBy(issues));
  function linkOf(i: ValidationIssue): string | null {
    const b = scope.base;
    const m = /^sites\[([^\]]+)\](?:\.config\.containers\[([^\]]+)\])?/.exec(i.path);
    if (m) {
      if (m[2]) return `${b}/sites/${m[1]}/containers/${m[2]}`;
      const sec = /\.config\.(containers|edge|volumes)/.exec(i.path)?.[1] ?? 'settings';
      return `${b}/sites/${m[1]}/${sec}`;
    }
    for (const s of ['variables', 'secrets', 'files', 'sites']) if (i.path.startsWith(s)) return `${b}/${s}`;
    return null;
  }
  let jsonOpen = $state(false);
  const json = $derived(JSON.stringify({ variables: t.variables, secrets: t.secrets, files: t.files, sites: t.sites }, null, 2));
  let delOpen = $state(false);
  async function doDelete() {
    await api.deleteTemplate({ templateId: t.templateId });
    toast.success(`Template ${t.label} deleted`, 'Deployments keep their own copy.');
    goto('/app/templates');
  }
</script>

<svelte:head><title>{t.label} · Templates · OPM</title></svelte:head>

<PageHeader title={t.label} iconName={t.icon ?? null} iconType="template" readOnly={!scope.canWrite}>
  {#snippet meta()}<span class="badge accent">v{t.version}</span>{/snippet}
  <div class="row wrap small muted hmeta">
    <IdChip id={t.templateId} />
    <span>updated <RelTime ts={t.updatedAt} /></span>
    {#if counts.errors}<span class="badge red">{counts.errors} error{counts.errors === 1 ? '' : 's'}</span>{/if}
    {#if counts.warnings}<span class="badge amber">{counts.warnings} publish warning{counts.warnings === 1 ? '' : 's'}</span>{/if}
  </div>
  {#snippet actions()}<HistoryButton target="templates/{t.templateId}" title="History of {t.label}" />{/snippet}
</PageHeader>

<div class="ov">
  <div class="stack loose">
    <section class="card">
      <div class="card-head"><h3>Template</h3></div>
      <form class="card-body stack" onsubmit={save} novalidate>
        <FormSummary issues={scope.saveIssues.filter((i) => ['label', 'icon', 'description'].includes(i.path))} />
        <div class="grid-2">
          <Field label="Label" id="t-label" required error={errorsAt(all, 'label')}><input id="t-label" class="input" bind:value={label} maxlength="80" disabled={!scope.canWrite} data-testid="tpl-label" /></Field>
          <Field label="Icon" id="t-icon" hint="None = the default template icon" error={errorsAt(all, 'icon')}>
            <IconPicker bind:value={icon} type="template" id="t-icon" testid="tpl-icon" disabled={!scope.canWrite} />
          </Field>
        </div>
        <Field label="Slug" id="t-slug" lock="Fixed at create — it names the GitHub branch template/<slug>">
          <input id="t-slug" class="input mono" value={t.slug ?? ''} disabled data-testid="tpl-slug" />
        </Field>
        <Field label="Description" id="t-desc" error={errorsAt(all, 'description')}><textarea id="t-desc" class="textarea" rows="3" bind:value={description} disabled={!scope.canWrite}></textarea></Field>
        {#if scope.canWrite}
          <div class="row">
            <Button type="submit" variant="primary" loading={scope.saving} disabled={!dirty || local.length > 0} data-testid="tpl-save">Save details</Button>
            {#if dirty}<Button onclick={() => ((label = t.label), (icon = t.icon ?? null), (description = t.description))}>Reset</Button>{/if}
            <span class="faint xs">Details (label, icon, description) are not config: they are saved at once, without a commit. The config changes only by a commit.</span>
          </div>
        {/if}
      </form>
    </section>

    <section class="card">
      <div class="card-head"><Icon icon={Network} size={16} /><h3>Site dependencies</h3></div>
      <div class="card-body"><SiteGraph sites={t.sites} base={scope.base} /></div>
    </section>

    <section class="card">
      <button class="card-head jh" onclick={() => (jsonOpen = !jsonOpen)} aria-expanded={jsonOpen} data-testid="json-toggle">
        <Icon icon={jsonOpen ? ChevronDown : ChevronRight} size={16} /><h3>JSON preview</h3><span class="faint small">variables · secrets · files · sites</span>
      </button>
      {#if jsonOpen}
        <div class="card-body stack">
          <div class="row">
            <CopyButton text={json} label="Copy" />
            <Button size="sm" icon={Download} onclick={() => saveAs(new Blob([json], { type: 'application/json' }), `template-${t.templateId}-v${t.version}.json`)}>Download .json</Button>
          </div>
          <pre class="code" data-testid="tpl-json">{json}</pre>
        </div>
      {/if}
    </section>
  </div>

  <aside class="stack">
    <section class="card">
      <div class="card-head"><Icon icon={ShieldCheck} size={16} /><h3>Validation</h3></div>
      <div class="card-body">
        <IssuesList {issues} {linkOf} emptyText="No problems. Warnings would not block saving; deployments made from it can publish once values are set." testid="tpl-issues" />
        <p class="xs faint" style="margin-top:8px">Template mode: publish-level problems (missing values, images not ready …) show as warnings.</p>
      </div>
    </section>
    {#if scope.canWrite}
      <section class="card danger-zone">
        <div class="card-body stack">
          <div class="strong">Delete template</div>
          <p class="small muted">Deployments made from it keep their own copy.</p>
          <div><Button variant="danger-outline" icon={Trash2} onclick={() => (delOpen = true)} data-testid="tpl-delete">Delete template</Button></div>
        </div>
      </section>
    {/if}
  </aside>
</div>

<ConfirmDialog bind:open={delOpen} title="Delete template {t.label}?" message="This cannot be undone. Deployments made from it keep their own copy." typed={t.label} typedLabel="Type the label" confirmLabel="Delete template" danger onconfirm={doDelete} testid="delete-template" />

<style>
  .hmeta {
    margin-top: 4px;
    gap: 12px;
  }
  .ov {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 360px;
    gap: var(--sp-5);
    align-items: start;
  }
  @media (max-width: 1080px) {
    .ov {
      grid-template-columns: minmax(0, 1fr);
    }
  }
  .jh {
    width: 100%;
    border: 0;
    background: transparent;
    cursor: pointer;
    text-align: left;
    color: var(--text);
    border-bottom: 0;
  }
  .danger-zone {
    border-color: color-mix(in srgb, var(--red) 30%, var(--border));
  }
</style>
