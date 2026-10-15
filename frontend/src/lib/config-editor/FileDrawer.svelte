<script lang="ts">
  /**
   * Add / edit a file definition — the shared FileForm ($opm, SPEC §21 #102: the agent shows the same form read-only)
   * in a side drawer. Applied into the browser change set (§21 #105; scope.saveSection). Content is edited separately.
   */
  import { untrack } from 'svelte';
  import type { FileDef } from '@opm/shared';
  import { normalizeFileDef } from '@opm/shared';
  import FileForm, { blankFile } from '$opm/forms/files/FileForm.svelte';
  import Drawer from '../ui/Drawer.svelte';
  import Button from '../ui/Button.svelte';
  import FormSummary from '../ui/FormSummary.svelte';
  import { issuesUnder } from '../util/issues';
  import type { ConfigScope } from './scope.svelte';

  let { open = $bindable(), scope, file = null }: { open: boolean; scope: ConfigScope; file?: FileDef | null } = $props();
  let f = $state<FileDef>(blankFile());
  let submitted = $state(false);
  let valid = $state(false);
  let ready = $state(false);
  $effect(() => {
    if (!open) return void (ready = false);
    const init = file ? normalizeFileDef(structuredClone($state.snapshot(file)) as FileDef) : blankFile();
    untrack(() => {
      f = init;
      submitted = false;
      ready = true;
    });
  });
  const isNew = $derived(!file);
  const ro = $derived(!scope.canWrite);
  const server = $derived(issuesUnder(scope.saveIssues, `files[${f.fileSlug}]`));
  async function save(e: Event) {
    e.preventDefault();
    submitted = true;
    if (!valid) return;
    const item = $state.snapshot(f) as FileDef;
    const list = scope.config.files.map((x) => $state.snapshot(x) as FileDef);
    const items = isNew ? [...list, item] : list.map((x) => (x.fileSlug === file!.fileSlug ? item : x));
    if (await scope.saveSection('files', items, isNew ? `File ${item.fileSlug} added` : `File ${item.fileSlug} applied`)) open = false;
  }
</script>

<Drawer bind:open title={ro ? `File ${file?.fileSlug ?? ''}` : isNew ? 'Add file' : `Edit file ${file?.fileSlug}`} subtitle="One copy per site on the server; containers mount it read-only (Mounted from → Files) and find it with {'{{ file.<slug>.path }}'}." busy={scope.saving} testid="file-drawer">
  <form id="file-form-el" class="stack" onsubmit={save} novalidate>
    <FormSummary issues={server} />
    {#if open && ready}<FileForm bind:value={f} bind:valid mode={ro ? 'read' : isNew ? 'create' : 'edit'} serverIssues={server} {submitted} />{/if}
  </form>
  {#snippet footer()}
    <Button onclick={() => (open = false)} disabled={scope.saving}>{ro ? 'Close' : 'Cancel'}</Button>
    {#if !ro}<Button type="submit" form="file-form-el" variant="primary" loading={scope.saving} disabled={submitted && !valid} data-testid="file-save">{isNew ? 'Add file' : 'Apply'}</Button>{/if}
  {/snippet}
</Drawer>
