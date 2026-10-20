<script lang="ts">
  /**
   * §21 #101 / #117 Docker › Volumes › "Cloud sync" tab of a persist-to-cloud volume: the live folder compared with the
   * content the site's head commits (in sync / changed on the server, not committed), Rescan, a link to the Branch page
   * (commit or discard the server changes; merges write the cloud's content), persist settings (read-only: cloud
   * configuration). There is no Snapshot now / Apply cloud version any more: content travels with the commits.
   */
  import type { VolumeSyncStatus } from '@opm/shared/volume-content';
  import { formatContentBytes } from '@opm/shared/volume-content';
  import { base } from '$app/paths';
  import { goto } from '$app/navigation';
  import { api } from '$lib/api';
  import { toasts } from '$lib/state/toast.svelte';
  import { session } from '$lib/state/session.svelte';
  import { consoleDeploymentUrl } from '$lib/cloud';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Time from '$lib/ui/Time.svelte';
  import PersistForm from '$opm/forms/volumes/PersistForm.svelte';
  import { LOCK_CLOUD_ONLY } from '$opm/forms/locks';
  import { SYNC_HINT, SYNC_LABEL, SYNC_TONE, shortHash } from './sync';

  interface Props {
    siteSlug: string;
    status: VolumeSyncStatus;
    canWrite: boolean;
    onchanged: () => void;
    onfiles: () => void;
  }
  let { siteSlug, status, canWrite, onchanged, onfiles }: Props = $props();
  const s = $derived(status);
  const consoleUrl = $derived(consoleDeploymentUrl(session.info, `/sites/${encodeURIComponent(siteSlug)}/volumes/content?volume=${encodeURIComponent(status.volumeSlug)}`));
  const branchHref = $derived(`${base}/app/${encodeURIComponent(siteSlug)}/branch`);

  let busy = $state(false);
  async function rescan() {
    busy = true;
    try {
      await api.siteRescanDrift({ siteSlug });
      onchanged();
    } catch (e) {
      toasts.err(e, 'Rescan failed');
    } finally {
      busy = false;
    }
  }
  const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? '' : 's'}`;
</script>

<div class="card-body stack" data-testid="vol-sync" data-state={s.state}>
  <div class="statebar">
    <span class="badge lg {SYNC_TONE[s.state]}" data-testid="sync-state">{SYNC_LABEL[s.state]}</span>
    <span class="small muted hint">{SYNC_HINT[s.state]}</span>
  </div>
  {#if s.lastError}
    <div class="banner bad small" data-testid="sync-error"><Icon name="alert" /><div class="grow">{s.lastError}</div></div>
  {/if}
  {#if s.state === 'live-changed'}
    <div class="banner warn small" data-testid="sync-uncommitted">
      <Icon name="alert" />
      <div class="grow">Files of this volume were changed on the server and are <b>not committed</b>. On the Branch page, commit them (the cloud can then take them from this site's branch) or discard them (the committed content is written back). A merge from the cloud never overwrites them silently: files both sides changed are shown so you can keep this site's version or take the cloud's.</div>
    </div>
  {/if}

  <div class="acts">
    <Btn size="sm" variant={s.state === 'live-changed' ? 'primary' : 'default'} icon="branch" onclick={() => goto(branchHref)} data-testid="sync-branch">Branch page</Btn>
    <Btn size="sm" icon="refresh" {busy} disabled={!canWrite} onclick={rescan} data-testid="sync-rescan">Rescan</Btn>
    <Btn size="sm" variant="ghost" icon="folder" onclick={onfiles} data-testid="sync-browse">Browse live files</Btn>
  </div>

  <div class="tiles">
    <div class="tile" data-testid="sync-live">
      <span class="tl">On the server</span>
      {#if s.live}
        <b>{plural(s.live.fileCount, 'file')} · {formatContentBytes(s.live.totalBytes)}</b>
        <span class="tiny muted">{s.live.excludedCount} excluded · tree <span class="mono">{shortHash(s.live.treeHash)}</span>{#if s.live.scannedAt}<span>&nbsp;· scanned <Time ts={s.live.scannedAt} /></span>{/if}</span>
      {:else}<b>—</b><span class="tiny muted">not scanned yet</span>{/if}
    </div>
    <div class="tile" data-testid="sync-committed">
      <span class="tl">Committed</span>
      {#if s.committed}
        <b>{plural(s.committed.fileCount, 'file')}</b>
        <span class="tiny muted">the content of this site's head commit · tree <span class="mono">{shortHash(s.committed.treeHash)}</span></span>
      {:else}<b>—</b><span class="tiny muted">unknown</span>{/if}
    </div>
  </div>

  <div class="stack tight">
    <PersistForm volume={{ kind: 'dir', persist: s.persist }} readOnly lock={LOCK_CLOUD_ONLY} help={false} testid="sync-persist" />
    <p class="tiny muted" style="margin:0">These settings change only in the cloud console{#if consoleUrl}<span>&nbsp;—&nbsp;</span><a href={consoleUrl} target="_blank" rel="noopener" data-testid="sync-console">edit in the cloud console</a>{/if}.</p>
  </div>
</div>

<style>
  .statebar {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 10px;
    align-items: center;
  }
  .badge.lg {
    font-size: var(--fs-sm);
    padding: 3px 10px;
  }
  .hint {
    flex: 1 1 260px;
  }
  .acts {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .tiles {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(190px, 1fr));
    gap: 10px;
  }
  .tile {
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    padding: 10px 12px;
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }
  .tl {
    font-size: var(--fs-xs);
    color: var(--text-3);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .tile b {
    font-size: var(--fs-md, 15px);
    font-variant-numeric: tabular-nums;
  }
</style>
