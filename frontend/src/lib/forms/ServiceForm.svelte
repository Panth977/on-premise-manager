<script lang="ts" module>
  export type ServiceDraft = { label: string; serviceSlug: string; icon: string | null; description: string };
  export const blankService = (): ServiceDraft => ({ label: '', serviceSlug: '', icon: null, description: '' });
</script>

<script lang="ts">
  /**
   * ONE service form for create AND edit (SPEC §17.0, §21 #102): Label · Name (id) · Icon · Description — 4 fields →
   * a Modal in both cases. The name is derived from the label on create and fixed afterwards.
   * Test ids: svc-label · svc-slug · svc-icon · svc-desc.
   */
  import type { ValidationIssue } from '@opm/shared';
  import Field from '../ui/Field.svelte';
  import FormSummary from '../ui/FormSummary.svelte';
  import IconPicker from '../ui/IconPicker.svelte';
  import SlugInput from '../ui/SlugInput.svelte';
  import { errorsAt } from '../util/issues';
  type Props = { value: ServiceDraft; mode: 'create' | 'edit'; issues: ValidationIssue[]; server?: ValidationIssue[]; show: boolean };
  let { value = $bindable(), mode, issues, server = [], show }: Props = $props();
  const at = (p: string) => (show ? errorsAt(issues, p) : []);
</script>

<div class="stack" data-testid="service-form">
  <FormSummary issues={server} />
  <Field label="Label" id="s-label" required error={at('label')}>
    <input id="s-label" class="input" bind:value={value.label} maxlength="80" placeholder="Postgres" data-testid="svc-label" />
  </Field>
  <Field label="Name (id)" id="s-slug" required hint={mode === 'create' ? 'From the label · fixed after create' : 'Fixed after create'} error={at('serviceSlug')}>
    <SlugInput bind:value={value.serviceSlug} from={value.label} fixed={mode === 'edit'} id="s-slug" placeholder="postgres" testid="svc-slug" invalid={at('serviceSlug').length > 0} />
  </Field>
  <Field label="Icon" id="s-icon" hint="Optional — none = the default service icon" error={at('icon')}>
    <IconPicker bind:value={value.icon} type="service" id="s-icon" testid="svc-icon" />
  </Field>
  <Field label="Description" id="s-desc" error={at('description')} aside={descCount}>
    <textarea id="s-desc" class="textarea" bind:value={value.description} rows="3" data-testid="svc-desc"></textarea>
  </Field>
</div>

{#snippet descCount()}{value.description.length} / 2000{/snippet}
