<script lang="ts">
  import { page } from '$app/state';
  import { headConfig } from '$lib/state/app.svelte';
  import { draft } from '$lib/state/draft.svelte';
  import PageHead from '$lib/ui/PageHead.svelte';
  import Load from '$lib/ui/Load.svelte';
  import Empty from '$lib/ui/Empty.svelte';
  import NoHead from '$lib/ui/NoHead.svelte';
  import LocalEditBar from '$lib/ui/LocalEditBar.svelte';
  import FieldIssues from '$lib/ui/FieldIssues.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Time from '$lib/ui/Time.svelte';
  import RevealValue from '$lib/ui/RevealValue.svelte';
  import Drawer from '$lib/ui/Drawer.svelte';
  import SecretValueInput from '$opm/forms/secrets/SecretValueInput.svelte';
  import SecretForm, { blankSecret } from '$opm/forms/secrets/SecretForm.svelte';
  import Confirm from '$lib/ui/Confirm.svelte';
  import { REVEAL_SECONDS, type SecretDef } from '@opm/shared/types';

  // the shared SecretForm (SPEC §21 #102: the same form as the cloud drawer); §21 #113 a site is standalone: the
  // definition is a local edit, secrets are added and removed here (committed from the change bar)
  let formOpen = $state(false);
  let formDef = $state<SecretDef | null>(null);
  let formValid = $state(true);
  let creating = $state(false);
  let removeSlug = $state<string | null>(null);
  let removeOpen = $state(false);
  function openForm(slug: string) {
    const s = draft.secretInput(slug);
    if (!s) return;
    formDef = { secretSlug: s.secretSlug, label: s.label, description: s.description, required: s.required, generate: s.generate ? { ...s.generate } : null, value: null, rotatedAt: head(slug)?.rotatedAt ?? null };
    creating = false;
    formOpen = true;
  }
  function openNew() {
    formDef = blankSecret();
    creating = true;
    formOpen = true;
  }
  function applyForm() {
    if (!formDef || !formValid || !draft.work) return;
    const d = $state.snapshot(formDef) as SecretDef;
    const def = { secretSlug: d.secretSlug, label: d.label, description: d.description, required: d.required, generate: d.generate ? { ...d.generate } : null };
    const cur = draft.secretInput(d.secretSlug);
    if (creating || !cur) draft.work.secrets.push({ ...def, ...(def.generate ? { generateNow: true } : {}) });
    else Object.assign(cur, def);
    formOpen = false;
  }
  function remove() {
    if (!draft.work || !removeSlug) return;
    draft.work.secrets = draft.work.secrets.filter((x) => x.secretSlug !== removeSlug);
    removeSlug = null;
  }

  const slug = $derived(page.params.siteSlug ?? '');
  const head = (slug: string) => draft.base?.config.secrets.find((s) => s.secretSlug === slug);
  function setNew(slug: string, value: string) {
    const s = draft.secretInput(slug);
    if (!s) return;
    s.generateNow = undefined;
    if (value === '') delete s.plainValue;
    else s.plainValue = value;
  }
  function generate(slug: string) {
    const s = draft.secretInput(slug);
    if (!s) return;
    delete s.plainValue;
    s.generateNow = true;
  }
  function undo(slug: string) {
    const s = draft.secretInput(slug);
    if (!s) return;
    delete s.plainValue;
    delete s.generateNow;
  }
</script>

<PageHead icon="key" eyebrow="Site {slug}" title="Secrets" sub="Masked. View shows the value for {REVEAL_SECONDS} s (logged). Commit applies changes on {slug} only.">
  {#snippet actions()}
    <Btn icon="plus" variant="primary" disabled={!draft.work} onclick={openNew} data-testid="add-secret">Add secret</Btn>
  {/snippet}
</PageHead>

<Load loading={headConfig.loading} loaded={headConfig.loaded} error={headConfig.error} retry={() => headConfig.load()}>
  {#if !draft.base?.head}
    <section class="card"><NoHead /></section>
  {:else if (draft.work?.secrets.length ?? 0) === 0}
    <section class="card"><Empty icon="key" title="No secrets" text="This deployment has no secrets yet. Add one here, or merge a cloud commit on the Branch page."><Btn icon="plus" disabled={!draft.work} onclick={openNew}>Add secret</Btn></Empty></section>
  {:else}
    <section class="card">
      <div class="table-wrap">
        <table class="table" data-testid="secrets">
          <thead><tr><th>Slug</th><th>Label</th><th>State</th><th>Value (head)</th><th style="min-width:260px">New value</th><th></th></tr></thead>
          <tbody>
            {#each draft.work?.secrets ?? [] as s (s.secretSlug)}
              {@const h = head(s.secretSlug)}
              {@const pending = s.plainValue !== undefined || s.generateNow}
              <tr class:changed={pending}>
                <td class="mono"><strong>{s.secretSlug}</strong></td>
                <td>{s.label}{#if s.required}<div class="tiny faint">required</div>{/if}</td>
                <td>
                  {#if h?.isSet}<span class="badge ok">set</span>{:else}<span class="badge {s.required ? 'bad' : ''}">not set</span>{/if}
                  {#if h?.rotatedAt}<div class="tiny muted">rotated <Time ts={h.rotatedAt} /></div>{/if}
                </td>
                <td>
                  <RevealValue isSet={!!h?.isSet} label="secret {s.secretSlug}" testid="secret-{s.secretSlug}" reveal={() => draft.revealSecret(s.secretSlug)} />
                </td>
                <td>
                  <SecretValueInput value={s.plainValue ?? null} onchange={(v) => setNew(s.secretSlug, v ?? '')} secret={s} optional compact generated={!!s.generateNow} ongenerate={() => generate(s.secretSlug)} onundo={() => undo(s.secretSlug)} testid="new-{s.secretSlug}" />
                  <FieldIssues issues={draft.issuesAt(`secrets[${s.secretSlug}]`)} />
                </td>
                <td class="actions">
                  {#if !h}<span class="badge accent" title="Added on this site, not committed yet">new</span>{/if}
                  <Btn size="xs" variant="ghost" icon="edit" title="Edit the definition" aria-label="Open {s.secretSlug}" onclick={() => openForm(s.secretSlug)} data-testid="sec-open-{s.secretSlug}" />
                  <Btn size="xs" variant="ghost" icon="trash" title="Remove the secret" aria-label="Remove {s.secretSlug}" onclick={() => ((removeSlug = s.secretSlug), (removeOpen = true))} data-testid="sec-remove-{s.secretSlug}" />
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </section>
  {/if}
  <LocalEditBar />
</Load>

<Drawer bind:open={formOpen} title={creating ? 'Add secret' : formDef ? `Secret ${formDef.secretSlug}` : 'Secret'} subtitle="A local edit — applied on this site when you commit. The value is set in the table (kept in this browser's memory until you commit; never stored)." testid="secret-drawer">
  {#if formOpen && formDef}<SecretForm bind:value={formDef} bind:valid={formValid} mode={creating ? 'create' : 'edit'} />{/if}
  {#snippet footer()}
    <Btn onclick={() => (formOpen = false)}>Cancel</Btn>
    <Btn variant="primary" disabled={!formValid} onclick={applyForm} data-testid="sec-save">{creating ? 'Add' : 'Apply'}</Btn>
  {/snippet}
</Drawer>
<Confirm bind:open={removeOpen} title="Remove secret {removeSlug ?? ''}?" confirmLabel="Remove" danger onconfirm={remove} testid="sec-remove-confirm">
  <p>Containers and files that use it are reported by the validation before you can commit.</p>
</Confirm>

<style>
  tr.changed td {
    background: color-mix(in srgb, var(--accent-soft) 45%, transparent);
  }
</style>
