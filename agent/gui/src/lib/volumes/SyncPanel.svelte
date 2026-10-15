<script lang="ts">
  /**
   * §21 #101 Docker › Volumes › "Cloud sync" tab of a persist-to-cloud volume: sync state, live tree summary, last
   * snapshot (when, trigger, version seq or "queued"), pending push (version, who, note, restart), Snapshot now,
   * Apply cloud version (ApplyFlow), persist settings (read-only: cloud configuration) and the local snapshot history.
   */
  import type { VolumeSyncStatus } from '@opm/shared/volume-content';
  import { formatContentBytes } from '@opm/shared/volume-content';
  import { errCode, errMessage, isApiError } from '$lib/api';
  import { toasts } from '$lib/state/toast.svelte';
  import { session } from '$lib/state/session.svelte';
  import { consoleDeploymentUrl } from '$lib/cloud';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Time from '$lib/ui/Time.svelte';
  import Empty from '$lib/ui/Empty.svelte';
  import ApplyFlow from './ApplyFlow.svelte';
  import PersistForm from '$opm/forms/volumes/PersistForm.svelte';
  import { LOCK_CLOUD_ONLY } from '$opm/forms/locks';
  import { SYNC_HINT, SYNC_LABEL, SYNC_TONE, TRIGGER_LABEL, shortHash, vsync } from './sync';

  interface Props {
    siteSlug: string;
    status: VolumeSyncStatus;
    canWrite: boolean;
    onchanged: () => void;
    onfiles: () => void;
  }
  let { siteSlug, status, canWrite, onchanged, onfiles }: Props = $props();
  const s = $derived(status);
  const last = $derived(s.lastSnapshot);
  const consoleUrl = $derived(consoleDeploymentUrl(session.info, `/sites/${encodeURIComponent(siteSlug)}/volumes/content?volume=${encodeURIComponent(status.volumeSlug)}&tab=cloud`));

  let snapBusy = $state(false);
  let snapErr = $state<{ message: string; total?: number; max?: number; biggest: { path: string; size: number }[] } | null>(null);
  async function snapshotNow() {
    snapBusy = true;
    snapErr = null;
    try {
      const r = await vsync.snapshotVolumeSync({ siteSlug, volumeSlug: s.volumeSlug, body: {} });
      if (r.created) toasts.ok(`Snapshot of ${s.volumeSlug} taken`, r.snapshot.versionId ? `uploaded as version ${r.snapshot.seq ?? ''}` : 'queued — uploads when the cloud is reachable');
      else toasts.ok(`No change in ${s.volumeSlug}`, 'The live volume equals the last snapshot — nothing new stored.');
      onchanged();
    } catch (e) {
      if (errCode(e) === 'too-large' && isApiError(e)) {
        const d = e.details as { totalBytes?: number; maxBytes?: number; biggest?: { path: string; size?: number; sizeBytes?: number }[] };
        snapErr = { message: errMessage(e), total: d.totalBytes, max: d.maxBytes, biggest: (d.biggest ?? []).map((b) => ({ path: b.path, size: b.size ?? b.sizeBytes ?? 0 })) };
      } else toasts.err(e, 'Snapshot failed');
    } finally {
      snapBusy = false;
    }
  }
  let applyOpen = $state(false);
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
  {#if snapErr}
    <div class="banner bad small" data-testid="snap-too-large">
      <Icon name="alert" />
      <div class="grow">
        <strong>Snapshot refused:</strong> {snapErr.message}
        {#if snapErr.total !== undefined && snapErr.max !== undefined}<div class="tiny">{formatContentBytes(snapErr.total)} of at most {formatContentBytes(snapErr.max)} — exclude caches (cloud console → persist settings) or remove big files.</div>{/if}
        {#if snapErr.biggest.length}<ul class="tiny mono big">{#each snapErr.biggest.slice(0, 6) as b (b.path)}<li>{b.path} · {formatContentBytes(b.size)}</li>{/each}</ul>{/if}
      </div>
    </div>
  {/if}

  {#if s.push}
    <div class="push" data-testid="sync-push">
      <div class="ph">
        <Icon name="download" size={16} />
        <strong class="pt">The cloud sent a new version</strong>
        <Btn variant="primary" size="sm" icon="download" disabled={!canWrite} onclick={() => (applyOpen = true)} data-testid="sync-apply">Apply cloud version…</Btn>
      </div>
      <dl class="kv small">
        <dt>Version</dt><dd data-testid="push-version">{#if s.push.versionSeq != null}version seq {s.push.versionSeq}{:else}<span class="mono" title={s.push.versionId}>{s.push.versionId.slice(-8)}</span>{/if} · tree <span class="mono">{shortHash(s.push.treeHash)}</span></dd>
        <dt>Sent by</dt><dd>{s.push.createdBy} · <Time ts={s.push.createdAt} /></dd>
        {#if s.push.note}<dt>Note</dt><dd data-testid="push-note">{s.push.note}</dd>{/if}
        <dt>Restart</dt><dd>{s.push.restart ? `yes — ${s.users.join(', ') || 'no container mounts it'}` : 'no'} <span class="tiny muted">(you can change it when applying)</span></dd>
        <dt>Based on</dt><dd>{s.push.baseTreeHash ? `snapshot ${shortHash(s.push.baseTreeHash)}` : 'unknown (every difference is a conflict)'}</dd>
      </dl>
    </div>
  {/if}

  <div class="acts">
    <Btn size="sm" icon="upload" busy={snapBusy} disabled={!canWrite} onclick={snapshotNow} data-testid="sync-snapshot">Snapshot now</Btn>
    {#if !s.push}<Btn size="sm" icon="download" disabled title="the cloud has not sent a version" data-testid="sync-apply">Apply cloud version…</Btn>{/if}
    <Btn size="sm" variant="ghost" icon="folder" onclick={onfiles} data-testid="sync-browse">Browse live files</Btn>
  </div>

  <div class="tiles">
    <div class="tile" data-testid="sync-live">
      <span class="tl">Live</span>
      {#if s.live}
        <b>{plural(s.live.fileCount, 'file')} · {formatContentBytes(s.live.totalBytes)}</b>
        <span class="tiny muted">{s.live.excludedCount} excluded · {#if s.live.changed}<span class="warn-text">changed since the last snapshot</span>{:else}unchanged{/if}{#if s.live.scannedAt}<span>&nbsp;· scanned <Time ts={s.live.scannedAt} /></span>{/if}</span>
      {:else}<b>—</b><span class="tiny muted">not scanned yet</span>{/if}
    </div>
    <div class="tile" data-testid="sync-last">
      <span class="tl">Last snapshot</span>
      {#if last}
        <b>{#if last.seq !== null}version {last.seq}{:else}<span data-testid="sync-queued">queued</span>{/if}</b>
        <span class="tiny muted"><Time ts={last.takenAt} /> · {TRIGGER_LABEL[last.trigger]}{last.actor ? ` · ${last.actor}` : ''} · {plural(last.fileCount, 'file')} · {formatContentBytes(last.totalBytes)}</span>
      {:else}<b>none yet</b><span class="tiny muted">{s.persist.auto ? 'the first automatic snapshot follows the first scan' : 'automatic snapshots are off — use Snapshot now'}</span>{/if}
    </div>
    <div class="tile">
      <span class="tl">Waiting to upload</span>
      <b data-testid="sync-queue">{s.queued}</b>
      <span class="tiny muted">{s.queued ? 'store-and-forward: retried until the cloud takes them' : 'nothing queued'}</span>
    </div>
  </div>

  <div class="stack tight">
    <PersistForm volume={{ kind: 'dir', persist: s.persist }} readOnly lock={LOCK_CLOUD_ONLY} help={false} testid="sync-persist" />
    <p class="tiny muted" style="margin:0">These settings change only in the cloud console{#if consoleUrl}<span>&nbsp;—&nbsp;</span><a href={consoleUrl} target="_blank" rel="noopener" data-testid="sync-console">edit in the cloud console</a>{/if}.</p>
  </div>

  <div>
    <div class="section-title">Snapshots of this site</div>
    {#if s.history.length === 0}
      <Empty icon="history" title="No snapshot yet" text="Snapshots taken on this site (automatic, Snapshot now, before / after applying) show here, also while they wait for the cloud." compact />
    {:else}
      <div class="table-wrap">
        <table class="table compact" data-testid="sync-history">
          <thead><tr><th>Version</th><th>Taken</th><th>Trigger</th><th>By</th><th class="num">Files</th><th class="num">Size</th><th>Tree</th></tr></thead>
          <tbody>
            {#each s.history as h (h.snapshotId)}
              <tr data-testid="hist-snap-{h.snapshotId}">
                <td>{#if h.error}<span class="badge bad" title={h.error}>failed</span>{:else if h.seq !== null}<strong>{h.seq}</strong>{:else}<span class="badge info">queued</span>{/if}</td>
                <td class="small nowrap"><Time ts={h.takenAt} /></td>
                <td class="small">{TRIGGER_LABEL[h.trigger]}</td>
                <td class="small">{h.actor ?? 'agent'}</td>
                <td class="num small">{h.fileCount}</td>
                <td class="num small">{formatContentBytes(h.totalBytes)}</td>
                <td class="mono xs faint">{shortHash(h.treeHash)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </div>
</div>

<ApplyFlow bind:open={applyOpen} {siteSlug} status={s} ondone={onchanged} />

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
  .pt {
    flex: 1 1 200px;
  }
  .acts {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .push {
    border: 1px solid color-mix(in srgb, var(--accent) 45%, var(--border));
    background: color-mix(in srgb, var(--accent-soft) 45%, transparent);
    border-radius: var(--r-lg);
    padding: 10px 12px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .ph {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
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
  .ex {
    font-size: 12px;
    margin-right: 4px;
  }
  .big {
    margin: 4px 0 0;
    padding-left: 16px;
  }
</style>
