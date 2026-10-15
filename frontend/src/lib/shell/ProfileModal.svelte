<script lang="ts">
  import { api, errIssues } from '../api';
  import { session } from '../session.svelte';
  import { toast } from '../toast.svelte';
  import Modal from '../ui/Modal.svelte';
  import Button from '../ui/Button.svelte';
  import Field from '../ui/Field.svelte';
  import IconPicker from '../ui/IconPicker.svelte';
  import { validateProfile } from '../util/validators';
  import { errorsAt, mergeIssues } from '../util/issues';
  import type { ValidationIssue } from '@opm/shared';

  let { open = $bindable() }: { open: boolean } = $props();
  let name = $state('');
  let phone = $state('');
  let img = $state<string | null>(null);
  let busy = $state(false);
  let touched = $state(false);
  let server = $state<ValidationIssue[]>([]);
  $effect(() => {
    if (open) {
      const a = session.account!;
      name = a.name;
      phone = a.phone ?? '';
      img = a.img ?? null;
      touched = false;
      server = [];
    }
  });
  const local = $derived(validateProfile({ name, phone, img }, false));
  const all = $derived(mergeIssues(local, server));
  const dirty = $derived(!!session.account && (name !== session.account.name || phone !== (session.account.phone ?? '') || img !== (session.account.img ?? null)));
  async function save(e: Event) {
    e.preventDefault();
    touched = true;
    if (local.length || !dirty) return;
    busy = true;
    server = [];
    try {
      const a = session.account!;
      const body: { name?: string; phone?: string | null; img?: string | null } = {};
      if (name !== a.name) body.name = name.trim();
      if (phone !== (a.phone ?? '')) body.phone = phone.trim() || null;
      if (img !== (a.img ?? null)) body.img = img;
      await api.patchAccount({ uid: a.uid, body });
      await session.refreshMe();
      toast.success('Profile saved');
      open = false;
    } catch (err) {
      server = errIssues(err);
      toast.error(err, 'Could not save the profile');
    } finally {
      busy = false;
    }
  }
</script>

<Modal bind:open title="My profile" subtitle={session.account?.email} size="sm" {busy} testid="profile-modal">
  <form id="profile-form" class="stack" onsubmit={save} novalidate>
    <Field label="Name" id="p-name" required error={touched || server.length ? errorsAt(all, 'name') : null}>
      <input id="p-name" class="input" bind:value={name} maxlength="80" data-testid="profile-name" />
    </Field>
    <Field label="Phone" id="p-phone" hint="E.164, e.g. +919812345678 — or empty" error={errorsAt(all, 'phone')}>
      <input id="p-phone" class="input" bind:value={phone} placeholder="+919812345678" inputmode="tel" data-testid="profile-phone" />
    </Field>
    <Field label="Avatar" id="p-img" hint="An icon or an uploaded picture; none = your initials" error={errorsAt(all, 'img')}>
      <IconPicker bind:value={img} type="account" initials={name || '?'} id="p-img" testid="profile-img" title="Choose your avatar" />
    </Field>
  </form>
  {#snippet footer()}
    <Button onclick={() => (open = false)} disabled={busy}>Cancel</Button>
    <Button type="submit" form="profile-form" variant="primary" loading={busy} disabled={!dirty || local.length > 0} data-testid="profile-save">Save</Button>
  {/snippet}
</Modal>
