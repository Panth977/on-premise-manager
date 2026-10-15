<script lang="ts">
  /** Save (or rename / move) a query / request into the site's collections (§21 #97). */
  import type { DevToolKind, DevToolSaved } from '@opm/shared/devtools';
  import { errMessage } from '$lib/api';
  import { toasts } from '$lib/state/toast.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Combobox from '$lib/ui/Combobox.svelte';
  import { dev } from './api';

  interface Props {
    open: boolean;
    site: string;
    kind: DevToolKind;
    connectionId: string | null;
    content: string;
    /** update this item (Save) instead of creating one (Save as) */
    existing?: DevToolSaved | null;
    renameOnly?: boolean;
    collections?: string[];
    suggestedName?: string;
    onsaved?: (s: DevToolSaved) => void;
  }
  let { open = $bindable(false), site, kind, connectionId, content, existing = null, renameOnly = false, collections = [], suggestedName = '', onsaved }: Props = $props();
  let name = $state('');
  let collection = $state('');
  let description = $state('');
  let anyConn = $state(false);
  let busy = $state(false);
  let err = $state<string | null>(null);
  $effect(() => {
    if (!open) return;
    name = existing?.name ?? suggestedName;
    collection = existing?.collection ?? '';
    description = existing?.description ?? '';
    anyConn = existing ? existing.connectionId === null : false;
    err = null;
  });
  async function save() {
    if (!name.trim()) return (err = 'Give it a name.');
    busy = true;
    try {
      const body = { kind, connectionId: anyConn ? null : (existing?.connectionId ?? connectionId), collection: collection.trim(), name: name.trim(), description, content: renameOnly && existing ? existing.content : content };
      const s = existing ? await dev.updateSaved({ siteSlug: site, savedId: existing.id, body }) : await dev.createSaved({ siteSlug: site, body });
      toasts.ok(existing ? `Saved ${s.name}` : `Saved as ${s.name}`, s.collection ? `in ${s.collection}` : undefined);
      open = false;
      onsaved?.(s);
    } catch (e) {
      err = errMessage(e);
    } finally {
      busy = false;
    }
  }
</script>

<Modal bind:open title={renameOnly ? 'Rename / move' : existing ? `Save ${existing.name}` : 'Save to collections'} description="Saved items are shared by every operator of site {site} (not part of the deployment config)." testid="save-dialog">
  <div class="stack">
    <div class="field">
      <label for="sv-name">Name</label>
      <input id="sv-name" class="input" bind:value={name} maxlength="120" data-testid="save-name" onkeydown={(e) => e.key === 'Enter' && save()} />
    </div>
    <div class="field">
      <label for="sv-col">Collection (folder)</label>
      <Combobox id="sv-col" value={collection} onchange={(v) => (collection = v)} options={collections.map((c) => ({ value: c }))} allowCustom placeholder="e.g. Reports (empty = Unsorted)" testid="save-collection" />
    </div>
    <div class="field">
      <label for="sv-desc">Description</label>
      <input id="sv-desc" class="input" bind:value={description} placeholder="optional" />
    </div>
    <label class="switch"><input type="checkbox" bind:checked={anyConn} />Offer it on every {kind} connection of the site</label>
    {#if err}<span class="field-err">{err}</span>{/if}
  </div>
  {#snippet footer()}
    <Btn variant="ghost" onclick={() => (open = false)}>Cancel</Btn>
    <Btn variant="primary" {busy} onclick={save} data-testid="save-ok">Save</Btn>
  {/snippet}
</Modal>
