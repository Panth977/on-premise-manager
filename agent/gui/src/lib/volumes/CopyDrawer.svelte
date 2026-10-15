<script lang="ts">
  /**
   * §21 #96 Copy a volume: to a path on the server (default <agentHome>/volume-copies/<volume>-<time>; system folders,
   * the agent's own folders and other volumes are refused) or into another declared volume (empty, or overwrite with its
   * name typed). Stop the containers during the copy (default on for databases), keep owner / mode / times / xattrs.
   */
  import type { VolumeOp } from '@opm/shared/types';
  import { copyPathProblem, defaultCopyPath, isDatabaseImage } from '@opm/shared/volume-ops';
  import { api, errCode, errMessage, issuesOf } from '$lib/api';
  import { bytes } from '$lib/format';
  import Drawer from '$lib/ui/Drawer.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Select from '$lib/ui/Select.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';

  interface Props {
    open: boolean;
    siteSlug: string;
    volumeSlug: string;
    kind: 'dir' | 'file';
    agentHome: string;
    users: { slug: string; image: string }[];
    others: { volumeSlug: string; kind: 'dir' | 'file'; sizeBytes: number | null; fileCount: number | null }[];
    onstarted: (op: VolumeOp) => void;
  }
  let { open = $bindable(false), siteSlug, volumeSlug, kind, agentHome, users, others, onstarted }: Props = $props();
  let mode = $state<'path' | 'volume'>('path');
  let path = $state('');
  let target = $state<string | null>(null);
  let overwrite = $state('');
  let stop = $state(false);
  let preserve = $state(true);
  let busy = $state(false);
  let err = $state<string | null>(null);
  const dbUsers = $derived(users.filter((u) => isDatabaseImage(u.image)).map((u) => u.slug));
  $effect(() => {
    if (open) {
      mode = 'path';
      path = '';
      target = null;
      overwrite = '';
      stop = dbUsers.length > 0;
      preserve = true;
      err = null;
    }
  });
  const placeholder = $derived(defaultCopyPath(agentHome, volumeSlug));
  const problem = $derived(mode === 'path' ? copyPathProblem(path, agentHome) : null);
  const tv = $derived(others.find((o) => o.volumeSlug === target) ?? null);
  const tvNonEmpty = $derived(!!tv && ((tv.sizeBytes ?? 0) > 0 || (tv.fileCount ?? 0) > 0));
  const sameKind = $derived(others.filter((o) => o.kind === kind));
  const canGo = $derived(!busy && (mode === 'path' ? !problem : !!tv && (!tvNonEmpty || overwrite.trim() === tv.volumeSlug)));

  async function go() {
    busy = true;
    err = null;
    try {
      const body =
        mode === 'path'
          ? { target: { kind: 'path' as const, path: path.trim() || null }, stopContainers: stop, preserve }
          : { target: { kind: 'volume' as const, volumeSlug: target!, overwrite: tvNonEmpty ? overwrite.trim() : null }, stopContainers: stop, preserve };
      const op = await api.copyVolume({ siteSlug, volumeSlug, body });
      open = false;
      onstarted(op);
    } catch (e) {
      const iss = issuesOf(e);
      err = iss.length ? iss.map((i) => i.message).join('; ') : errCode(e) === 'conflict' ? errMessage(e) : errMessage(e);
    } finally {
      busy = false;
    }
  }
</script>

<Drawer bind:open title="Copy volume {volumeSlug}" subtitle="site {siteSlug} · a {kind === 'file' ? 'file' : 'folder'} volume" testid="copy-drawer">
  <div class="stack">
    <SegmentedControl label="Copy to" value={mode} onchange={(v) => (mode = v)} testid="copy-mode" options={[{ value: 'path', label: 'A folder on the server' }, { value: 'volume', label: 'Another volume' }]} />
    {#if mode === 'path'}
      <div class="field">
        <label for="copy-path">Target path <span class="faint">(absolute, or ./… relative to the agent home)</span></label>
        <input id="copy-path" class="input mono" bind:value={path} {placeholder} autocomplete="off" spellcheck="false" data-testid="copy-path" aria-invalid={!!problem} />
        {#if problem}<div class="field-err" data-testid="copy-path-err">{problem}</div>{:else}<div class="tiny muted">Empty = <span class="mono">{placeholder}</span>. The folder must not exist (or be empty). Copies are listed under Copies; delete them there.</div>{/if}
      </div>
    {:else}
      <div class="field">
        <label for="copy-target">Target volume <span class="faint">(declared on {siteSlug}, same kind)</span></label>
        {#if sameKind.length === 0}
          <div class="banner small"><Icon name="info" /><div class="grow">No other {kind === 'file' ? 'file' : 'folder'} volume is declared on {siteSlug}. To duplicate this volume, declare a new one in Config → Volumes, approve the proposal, then copy into it.</div></div>
        {:else}
          <Select id="copy-target" bind:value={target} options={sameKind.map((o) => ({ value: o.volumeSlug, label: o.volumeSlug, hint: o.sizeBytes ? bytes(o.sizeBytes) : 'empty' }))} placeholder="Choose a volume…" testid="copy-target" mono />
        {/if}
      </div>
      {#if tv && tvNonEmpty}
        <div class="banner warn small"><Icon name="alert" /><div class="grow">Volume <strong class="mono">{tv.volumeSlug}</strong> is not empty ({bytes(tv.sizeBytes)}). Its contents are DELETED first and its containers stop during the copy. Type its name to overwrite it.</div></div>
        <input class="input mono" bind:value={overwrite} placeholder={tv.volumeSlug} autocomplete="off" spellcheck="false" aria-label="Type the target volume name" data-testid="copy-overwrite" />
      {/if}
    {/if}
    <label class="switch"><input type="checkbox" bind:checked={stop} data-testid="copy-stop" />Stop the containers that use it during the copy</label>
    <p class="tiny muted" style="margin:-6px 0 0 40px">
      {#if users.length === 0}No container mounts this volume.{:else}{users.map((u) => u.slug).join(', ')} {stop ? 'stop (reverse start order) and start again afterwards' : 'keep running — files that change during the copy may be inconsistent'}.{#if dbUsers.length} <strong>{dbUsers.join(', ')} {dbUsers.length === 1 ? 'is a database' : 'are databases'}: stopping is recommended.</strong>{/if}{/if}
    </p>
    <label class="switch"><input type="checkbox" bind:checked={preserve} data-testid="copy-preserve" />Keep owner, permissions, times and extended attributes</label>
    {#if err}<div class="banner bad small" data-testid="copy-err"><Icon name="alert" /><div class="grow">{err}</div></div>{/if}
  </div>
  {#snippet footer()}
    <Btn onclick={() => (open = false)}>Cancel</Btn>
    <Btn variant="primary" icon="copy" busy={busy} disabled={!canGo} onclick={go} data-testid="copy-go">Copy</Btn>
  {/snippet}
</Drawer>
