<script lang="ts" module>
  import { blankFrom, type FromDraft } from '../branch/FromPicker.svelte';
  export type DeploymentDraft = { label: string; slug: string; slugManual: boolean; image: string | null; tagSlugs: string[]; managers: string[] } & FromDraft;
  export const blankDeployment = (): DeploymentDraft => ({ label: '', slug: '', slugManual: false, image: null, tagSlugs: [], managers: [], ...blankFrom('template') });
</script>

<script lang="ts">
  /**
   * ONE deployment form for create AND edit (SPEC §17.0, §21 #102), in a Drawer both times, one field order:
   * Label · Logo · Source (§21 #104: Empty · Template · Deployment · Agent branch + commit + copy secret values) · Tags ·
   * Managers. The source is chosen at create; on edit it shows locked (the origin the branch was created from).
   * Test ids: dep-label · dep-logo · dep-src(-empty|-template|-deployment|-agent) · dep-template · dep-deployment ·
   * dep-site · dep-commit · dep-copy-secrets · dep-origin · dep-tags · dep-managers.
   */
  import type { ValidationIssue } from '@opm/shared';
  import Field from '../ui/Field.svelte';
  import FormSummary from '../ui/FormSummary.svelte';
  import IconPicker from '../ui/IconPicker.svelte';
  import TagPicker from '../ui/TagPicker.svelte';
  import AccountPicker from '../ui/AccountPicker.svelte';
  import FromPicker from '../branch/FromPicker.svelte';
  import SlugInput from '../ui/SlugInput.svelte';
  import { slugifyLabel } from '@opm/shared';
  import { errorsAt } from '../util/issues';
  import { catalog } from '../stores/catalog.svelte';

  type Props = {
    value: DeploymentDraft;
    mode: 'create' | 'edit';
    issues: ValidationIssue[];
    server?: ValidationIssue[];
    show: boolean;
    /** edit: where the branch came from ("Single@12") or null */
    origin?: string | null;
    /** null = every tag; else only these (scoped write access) */
    onlyTags?: string[] | null;
    canSeeAccounts?: boolean;
  };
  let { value = $bindable(), mode, issues, server = [], show, origin = null, onlyTags = null, canSeeAccounts = true }: Props = $props();
  const at = (p: string) => (show ? errorsAt(issues, p) : []);
  const LOCK = 'Chosen at create — the deployment keeps its own branch; Compare brings changes from any source';
</script>

<div class="stack" data-testid="deployment-form">
  <FormSummary issues={server} />
  <Field label="Label" id="d-label" required error={at('label')}>
    <input id="d-label" class="input" bind:value={value.label} maxlength="80" placeholder="ACME plant" data-testid="dep-label" />
  </Field>
  <Field label="Slug" id="d-slug" lock={mode === 'edit' ? 'Fixed at create — it names the GitHub branches deployment/<slug>/…' : undefined} hint={mode === 'create' ? `Used as the GitHub branch name: deployment/${value.slug || '<slug>'}/cloud · fixed after create` : undefined} error={at('slug')}>
    <SlugInput bind:value={value.slug} bind:manual={value.slugManual} from={value.label} derive={(l) => (l.trim() ? slugifyLabel(l, 'd-') : '')} fixed={mode === 'edit'} id="d-slug" testid="dep-slug" placeholder="acme-plant" />
  </Field>
  <Field label="Logo" id="d-img" hint="Optional — none = initials of the label" error={at('image')}>
    <IconPicker bind:value={value.image} type="deployment" initials={value.label || '?'} id="d-img" testid="dep-logo" title="Choose a logo" />
  </Field>
  {#if mode === 'create'}
    <FromPicker bind:value target="deployment" testid="dep" errors={{ templateId: at('templateId'), deploymentId: at('from.deploymentId'), siteSlug: at('from.siteSlug') }} />
  {:else}
    <Field label="Source" id="d-src" lock={LOCK}>
      <input id="d-src" class="input" value={origin ?? 'empty (no origin)'} disabled data-testid="dep-origin" />
    </Field>
  {/if}
  <Field label="Tags" id="d-tags" hint={onlyTags ? `Pick at least one of your tags: ${onlyTags.join(', ')}` : 'Optional'} error={at('tagSlugs')}>
    <TagPicker bind:value={value.tagSlugs} only={onlyTags} placeholder="No tags" testid="dep-tags" />
  </Field>
  {#if canSeeAccounts}
    <Field label="Managers" id="d-mgr" hint="Managers get every deployment.* action on this deployment." error={at('managerAccountUids')}>
      <AccountPicker bind:value={value.managers} exclude={catalog.accounts.filter((a) => a.disabled).map((a) => a.uid)} placeholder="No managers" testid="dep-managers" />
    </Field>
  {/if}
</div>
