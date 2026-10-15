<script lang="ts" module>
  import type { BranchPoint } from '@opm/shared';
  /** §21 #104 "Create from": Empty · Template · Deployment · Agent branch, at its head or one of its commits. */
  export type FromKind = 'empty' | 'template' | 'deployment' | 'agent';
  export interface FromDraft {
    source: FromKind;
    templateId: string;
    deploymentId: string;
    siteSlug: string;
    /** '' = head */
    commitId: string;
    copySecretValues: boolean;
  }
  export const blankFrom = (source: FromKind = 'template'): FromDraft => ({ source, templateId: '', deploymentId: '', siteSlug: '', commitId: '', copySecretValues: false });
  export function fromPoint(f: FromDraft): BranchPoint | null {
    const at: BranchPoint['at'] = f.commitId ? { commitId: f.commitId } : 'head';
    if (f.source === 'template' && f.templateId) return { branch: { kind: 'template', templateId: f.templateId }, at };
    if (f.source === 'deployment' && f.deploymentId) return { branch: { kind: 'deployment', deploymentId: f.deploymentId }, at };
    if (f.source === 'agent' && f.deploymentId && f.siteSlug) return { branch: { kind: 'agent', deploymentId: f.deploymentId, siteSlug: f.siteSlug }, at };
    return null;
  }
  /** the issue of an incomplete choice (field path, message) or null */
  export function fromIssue(f: FromDraft): { path: string; message: string } | null {
    if (f.source === 'template' && !f.templateId) return { path: 'templateId', message: 'Choose a template (or start empty)' };
    if ((f.source === 'deployment' || f.source === 'agent') && !f.deploymentId) return { path: 'from.deploymentId', message: 'Choose a deployment' };
    if (f.source === 'agent' && !f.siteSlug) return { path: 'from.siteSlug', message: 'Choose the site whose agent branch to copy' };
    return null;
  }
</script>

<script lang="ts">
  /**
   * The Source fields of New template / New deployment (one form each, §21 #102): kind (segmented) · the branch
   * (template / deployment combobox; agent branch = deployment + site) · the commit (head or an older one) · for a
   * deployment target and a deployment / agent source: "Copy secret values without a generate rule".
   * Test ids: {testid}-src(-empty|-template|-deployment|-agent) · {testid}-template · {testid}-deployment ·
   * {testid}-site · {testid}-commit · {testid}-copy-secrets.
   */
  import { api } from '../api';
  import { session } from '../session.svelte';
  import type { AgentBranchView, BranchRef, CommitSummary, DeploymentSummary, TemplateSummary } from '@opm/shared';
  import Field from '../ui/Field.svelte';
  import SegmentedControl from '../ui/SegmentedControl.svelte';
  import Combobox from '../ui/Combobox.svelte';
  import Select from '../ui/Select.svelte';
  import Toggle from '../ui/Toggle.svelte';
  import { commitsOf } from './branchHost';

  type Props = {
    value: FromDraft;
    /** 'template': secret values are never copied (no toggle) */
    target: 'template' | 'deployment';
    errors?: { templateId?: string[]; deploymentId?: string[]; siteSlug?: string[] };
    testid: string;
  };
  let { value = $bindable(), target, errors = {}, testid }: Props = $props();

  let templates = $state<TemplateSummary[] | null>(null);
  let deployments = $state<DeploymentSummary[] | null>(null);
  let sites = $state<AgentBranchView[]>([]);
  let commits = $state<CommitSummary[]>([]);
  $effect(() => {
    if (templates === null && session.can('templates', 'read'))
      void api
        .listTemplates({})
        .then((r) => {
          templates = r.items;
          if (value.source === 'template' && !value.templateId && r.items[0]) value.templateId = r.items[0].templateId;
        })
        .catch(() => (templates = []));
    if (deployments === null)
      void api
        .listDeployments({ query: { limit: 200 } })
        .then((r) => (deployments = r.items))
        .catch(() => (deployments = []));
  });
  // agent branches of the chosen deployment
  let sitesFor = '';
  $effect(() => {
    const d = value.source === 'agent' ? value.deploymentId : '';
    if (d === sitesFor) return;
    sitesFor = d;
    sites = [];
    if (d)
      void api
        .listAgentBranches({ deploymentId: d })
        .then((r) => (sites = r.items.filter((a) => a.head)))
        .catch(() => {});
  });
  const branch = $derived.by<BranchRef | null>(() => {
    if (value.source === 'template' && value.templateId) return { kind: 'template', templateId: value.templateId };
    if (value.source === 'deployment' && value.deploymentId) return { kind: 'deployment', deploymentId: value.deploymentId };
    if (value.source === 'agent' && value.deploymentId && value.siteSlug) return { kind: 'agent', deploymentId: value.deploymentId, siteSlug: value.siteSlug };
    return null;
  });
  let commitsFor = '';
  $effect(() => {
    const k = branch ? JSON.stringify(branch) : '';
    if (k === commitsFor) return;
    commitsFor = k;
    commits = [];
    value.commitId = '';
    if (branch)
      void commitsOf(branch, null, 50)
        .then((r) => (commits = r.items))
        .catch(() => {});
  });
  const tplOptions = $derived((templates ?? []).map((t) => ({ value: t.templateId, label: t.label, icon: t.icon ?? null, sub: t.description || undefined, detail: t.branch?.head ? `commit ${t.branch.head.seq}` : `v${t.version}` })));
  const depOptions = $derived((deployments ?? []).map((d) => ({ value: d.deploymentId, label: d.label, icon: d.image ?? null, detail: d.branch?.head ? `commit ${d.branch.head.seq}` : 'no commit' })));
  const siteOptions = $derived(sites.map((a) => ({ value: a.siteSlug, label: `site ${a.siteSlug}`, sub: a.head ? `commit ${a.head.seq} · ${a.head.message.split('\n')[0]}` : undefined, badge: a.needsAgent014 ? 'agent < 0.14' : undefined })));
  const commitOptions = $derived([{ value: '', label: 'Head (newest commit)' }, ...commits.map((c) => ({ value: c.commitId, label: `${c.seq} · ${c.message.split('\n')[0]}`, sub: `${c.kind} · ${c.author.email}` }))]);
  const KINDS = $derived([
    { value: 'empty', label: 'Empty' },
    { value: 'template', label: 'Template' },
    { value: 'deployment', label: 'Deployment' },
    { value: 'agent', label: 'Agent branch' },
  ] as { value: FromKind; label: string }[]);
  const canCopy = $derived(target === 'deployment' && (value.source === 'deployment' || value.source === 'agent'));
  $effect(() => {
    if (!canCopy && value.copySecretValues) value.copySecretValues = false;
  });
</script>

<Field label="Source" id="{testid}-src" hint={value.source === 'empty' ? 'Starts with nothing: add sites and containers afterwards.' : target === 'template' ? 'The new template gets a copy (secret values are never copied) and remembers where it came from — Compare shows what changed since.' : 'The new deployment gets a copy and remembers where it came from — Compare shows what changed since; template edits never change it on their own.'}>
  <SegmentedControl options={KINDS} bind:value={value.source} ariaLabel="Source" testid="{testid}-src" />
</Field>
{#if value.source === 'template'}
  <Field label="Template" id="{testid}-tpl" required error={errors.templateId}>
    <Combobox bind:value={value.templateId} options={tplOptions} placeholder={templates === null ? 'Loading…' : tplOptions.length ? 'Choose a template' : 'No templates'} id="{testid}-tpl" testid="{testid}-template" emptyText="No templates" />
  </Field>
{:else if value.source === 'deployment' || value.source === 'agent'}
  <Field label="Deployment" id="{testid}-dep" required error={errors.deploymentId}>
    <Combobox bind:value={value.deploymentId} options={depOptions} placeholder={deployments === null ? 'Loading…' : depOptions.length ? 'Choose a deployment' : 'No deployments'} id="{testid}-dep" testid="{testid}-deployment" emptyText="No deployments" />
  </Field>
  {#if value.source === 'agent'}
    <Field label="Site (agent branch)" id="{testid}-site" required error={errors.siteSlug} hint="The configuration that site's agent committed — including changes made there.">
      <Select options={siteOptions} bind:value={value.siteSlug} placeholder={value.deploymentId ? (siteOptions.length ? 'Choose a site' : 'No agent branches') : 'Choose a deployment first'} disabled={!value.deploymentId} testid="{testid}-site" id="{testid}-site" />
    </Field>
  {/if}
{/if}
{#if branch}
  <Field label="Commit" id="{testid}-commit" hint="Head = the newest commit. Pick an older one to start from that point.">
    <Select options={commitOptions} bind:value={value.commitId} testid="{testid}-commit" id="{testid}-commit" />
  </Field>
{/if}
{#if canCopy}
  <Field label="Secret values" id="{testid}-copy" hint="Secrets with a generate rule always get a new value. Without one they start empty unless you copy them (needs deployment.secrets.read on the source).">
    <Toggle bind:checked={value.copySecretValues} label="Copy secret values without a generate rule" testid="{testid}-copy-secrets" id="{testid}-copy" />
  </Field>
{/if}
