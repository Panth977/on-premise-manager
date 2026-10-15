<script lang="ts">
  /**
   * Add / edit a secret definition — the shared SecretForm ($opm, SPEC §21 #102: the same form in the agent GUI) in a
   * side drawer. Values are never edited here (Set value = SecretValueInput in a modal).
   */
  import type { SecretDef, SecretDefInput } from '@opm/shared';
  import SecretForm, { blankSecret } from '$opm/forms/secrets/SecretForm.svelte';
  import { issuesUnder } from '$opm/forms/host';
  import Drawer from '../ui/Drawer.svelte';
  import Button from '../ui/Button.svelte';
  import type { ConfigScope } from './scope.svelte';

  /** onsaved(slug) — the secret was saved (nested use: the dev-tool drawer picks the new secret) */
  let { open = $bindable(), scope, secret = null, onsaved }: { open: boolean; scope: ConfigScope; secret?: SecretDef | null; onsaved?: (slug: string) => void } = $props();
  let def = $state<SecretDef>(blankSecret());
  let submitted = $state(false);
  let valid = $state(true);
  $effect(() => {
    if (!open) return;
    def = secret ? ({ ...(structuredClone($state.snapshot(secret)) as SecretDef), value: null } as SecretDef) : blankSecret();
    submitted = false;
  });
  const isNew = $derived(!secret);
  const ro = $derived(!scope.canWrite);
  const server = $derived(issuesUnder(scope.saveIssues, `secrets[${def.secretSlug}]`));

  async function save(e: Event) {
    e.preventDefault();
    submitted = true;
    if (!valid) return;
    const d0 = $state.snapshot(def) as SecretDef;
    const slug = d0.secretSlug;
    let items: unknown[];
    if (scope.kind === 'template') {
      const list = scope.config.secrets.map((s) => ({ ...($state.snapshot(s) as SecretDef), value: null }));
      const item = { ...d0, value: null, rotatedAt: null };
      items = isNew ? [...list, item] : list.map((s) => (s.secretSlug === secret!.secretSlug ? { ...item, rotatedAt: s.rotatedAt } : s));
    } else {
      const list = scope.secretInputs();
      const { value: _v, rotatedAt: _r, ...d } = d0;
      void _v;
      void _r;
      const input: SecretDefInput = isNew && d.generate ? { ...d, generateNow: true } : d;
      items = isNew ? [...list, input] : list.map((s) => (s.secretSlug === secret!.secretSlug ? input : s));
    }
    if (await scope.saveSection('secrets', items, isNew ? `Secret ${slug} added` : `Secret ${slug} applied`)) {
      open = false;
      onsaved?.(slug);
    }
  }
</script>

<Drawer bind:open title={ro ? `Secret ${secret?.secretSlug ?? ''}` : isNew ? 'Add secret' : `Edit secret ${secret?.secretSlug}`} subtitle={ro ? 'Read only' : scope.kind === 'template' ? 'Templates never hold secret values.' : 'The definition only; set the value with "Set value".'} busy={scope.saving} testid="secret-drawer">
  <form id="sec-form" onsubmit={save} novalidate>
    {#if open}<SecretForm bind:value={def} bind:valid mode={ro ? 'read' : isNew ? 'create' : 'edit'} serverIssues={server} {submitted} />{/if}
  </form>
  {#snippet footer()}
    <Button onclick={() => (open = false)} disabled={scope.saving}>{ro ? 'Close' : 'Cancel'}</Button>
    {#if !ro}<Button type="submit" form="sec-form" variant="primary" loading={scope.saving} disabled={submitted && !valid} data-testid="sec-save">{isNew ? 'Add secret' : 'Apply'}</Button>{/if}
  {/snippet}
</Drawer>
