<script lang="ts">
  /**
   * Backups page: the volumes of ONE site (GET /_opm/api/sites/{siteSlug}/volumes, round D): declared volumes with
   * size on disk, backup flag, users and last snapshot; orphan folders (declared = false); host-path mounts
   * (never backed up). Other sites come through the peer mirror (unreachable → error state with Retry).
   */
  import { base } from '$app/paths';
  import { api } from '$lib/api';
  import { Resource, poll } from '$lib/state/resource.svelte';
  import { agentHomes } from '$lib/state/app.svelte';
  import { bytes } from '$lib/format';
  import Load from './Load.svelte';
  import Icon from './Icon.svelte';
  import Time from './Time.svelte';
  import CopyId from './CopyId.svelte';
  import Btn from './Btn.svelte';
  import ResetVolumeFlow from './ResetVolumeFlow.svelte';
  import { toasts } from '$lib/state/toast.svelte';
  import SeedView from '$opm/forms/volumes/SeedView.svelte';
  import type { ResetVolumeResponse } from '@opm/shared/types';

  let { siteSlug, self = false }: { siteSlug: string; self?: boolean } = $props();
  const res = new Resource(async () => {
    const r = await api.siteVolumes({ siteSlug });
    agentHomes.set(siteSlug, r.agentHome);
    return r;
  });
  $effect(() => {
    void siteSlug;
    res.reset();
    return poll(() => res.load(), 30_000);
  });
  const d = $derived(res.data);
  const declared = $derived(d?.volumes.filter((v) => v.declared) ?? []);
  const orphans = $derived(d?.volumes.filter((v) => !v.declared) ?? []);
  const backedUp = $derived(declared.filter((v) => v.backup));
  let resetFor = $state<string | null>(null);
  let resetOpen = $state(false);
  let lastReset = $state<ResetVolumeResponse | null>(null);
  function done(r: ResetVolumeResponse) {
    lastReset = r;
    toasts.ok(`Volume ${r.volumeSlug} reset to its starter content`, `${r.entries} ${r.entries === 1 ? 'entry' : 'entries'} written · snapshot ${r.snapshotId} taken first`);
    res.load();
  }
  const total = $derived((d?.volumes ?? []).reduce((a, v) => a + (v.sizeBytes ?? 0), 0));
</script>

<section class="card" data-testid="site-volumes-{siteSlug}">
  <div class="card-head">
    <h2><span class="mono">{siteSlug}</span>{#if self}&nbsp;<span class="tiny muted">(this site)</span>{/if} · volumes</h2>
    <span class="muted small">
      {#if d}agent home <span class="mono">{d.agentHome}</span> · {declared.length} declared · {backedUp.length} backed up · {bytes(total)} on disk · measured <Time ts={d.measuredAt} />{/if}
      · <a href="{base}/app/{siteSlug}/config/volumes">Edit in site config</a>
    </span>
  </div>
  <Load loading={res.loading} loaded={res.loaded} error={res.error} retry={() => res.load()} rows={3}>
    <div class="table-wrap">
      <table class="table" data-testid="volumes-{siteSlug}">
        <thead><tr><th>Volume</th><th>Kind</th><th>Starts with</th><th>Size</th><th>Back up</th><th>Used by</th><th>Last snapshot</th><th></th></tr></thead>
        <tbody>
          {#each [...declared, ...orphans] as v (v.volumeSlug)}
            <tr class:orphan={!v.declared} data-testid="sv-{siteSlug}-{v.volumeSlug}">
              <td>
                <span class="mono small"><strong>{v.volumeSlug}</strong></span>
                {#if v.declared && v.label && v.label !== v.volumeSlug}<div class="tiny muted">{v.label}</div>{/if}
                <div class="tiny muted mono path" data-testid="svpath-{siteSlug}-{v.volumeSlug}" title={v.location.kind === 'path' ? `custom location ${v.location.path}` : 'auto location'}>{v.path}{#if v.location.kind === 'path'} <span class="badge">custom</span>{/if}</div>
                {#if !v.declared}<div><span class="badge warn" title="A folder under vol/ that the head config no longer declares. Kept on disk, never deleted automatically, never backed up.">orphan · not declared</span></div>{/if}
              </td>
              <td class="small">{v.kind === 'file' ? 'file' : 'folder'}</td>
              <td class="small" data-testid="svseed-{siteSlug}-{v.volumeSlug}">
                <SeedView seed={v.seed} compact testid="svseed-text-{siteSlug}-{v.volumeSlug}" />
                {#if v.seeded}<div class="tiny muted">seeded <Time ts={v.seeded.at} /> · {v.seeded.entries} entr{v.seeded.entries === 1 ? 'y' : 'ies'}</div>{:else if v.seed}<div class="tiny muted">not seeded yet (applies when created / empty)</div>{/if}
              </td>
              <td class="small nowrap">{v.sizeBytes === null ? 'no data yet' : bytes(v.sizeBytes)}</td>
              <td>
                {#if !v.declared}<span class="badge">never</span>
                {:else if v.backup}<span class="badge ok">on</span>
                {:else}<span class="badge" title="backup = false: not in snapshots">off</span>{/if}
              </td>
              <td class="small">
                {#each v.usedBy as u, k (k)}
                  <div><a class="mono" href="{base}/app/{siteSlug}/config/containers/{u.containerSlug}">{u.containerSlug}</a> <span class="muted mono tiny">{u.mountPath}</span>{#if u.readOnly} <span class="badge">ro</span>{/if}</div>
                {:else}<span class="muted">{v.declared ? 'no container (unused)' : '—'}</span>{/each}
              </td>
              <td class="small">
                {#if v.lastSnapshot}
                  <div><Time ts={v.lastSnapshot.createdAt} /> · {bytes(v.lastSnapshot.sizeBytes)}</div>
                  <CopyId value={v.lastSnapshot.snapshotId} short={10} label="snapshot id" />
                {:else}<span class="muted">{v.declared && v.backup ? 'none yet' : 'never'}</span>{/if}
              </td>
              <td class="actions">
                {#if v.declared && v.seed}
                  <Btn size="xs" variant="danger" icon="refresh" title="Wipe the volume and write the starter content again (snapshot first)" onclick={() => ((resetFor = v.volumeSlug), (resetOpen = true))} data-testid="reset-{siteSlug}-{v.volumeSlug}">Reset to starter content…</Btn>
                {/if}
              </td>
            </tr>
          {:else}
            <tr><td colspan="8" class="muted small">No volumes on this site.</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
    {#if lastReset}
      <div class="card-body"><div class="banner ok small" data-testid="reset-done-{siteSlug}"><Icon name="check" /><div class="grow">Volume <span class="mono">{lastReset.volumeSlug}</span> reset: {lastReset.entries} {lastReset.entries === 1 ? 'entry' : 'entries'} written. The volume was snapshotted first: <CopyId value={lastReset.snapshotId} short={12} label="snapshot id" /> (Snapshots below — roll back from there if needed).</div></div></div>
    {/if}
    {#if d && d.hostPaths.length}
      <div class="card-body hp" data-testid="hostpaths-{siteSlug}">
        <div class="small"><Icon name="alert" size={13} /> <strong>Host paths — never backed up</strong> <span class="muted">(bind mounts of host folders; back them up yourself)</span></div>
        <div class="table-wrap">
          <table class="table">
            <thead><tr><th>Container</th><th>Host path</th><th>Mount path</th><th>Mode</th><th>Backup</th></tr></thead>
            <tbody>
              {#each d.hostPaths as h, i (i)}
                <tr>
                  <td class="mono small">{h.containerSlug}</td>
                  <td class="mono small">{h.path}</td>
                  <td class="mono small">{h.mountPath}</td>
                  <td class="small">{h.readOnly ? 'read-only' : 'read-write'}</td>
                  <td><span class="badge bad">never backed up</span></td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </div>
    {/if}
  </Load>
</section>

{#if resetFor}<ResetVolumeFlow bind:open={resetOpen} {siteSlug} volumeSlug={resetFor} ondone={done} />{/if}

<style>
  tr.orphan td {
    background: color-mix(in srgb, var(--amber-soft) 50%, transparent);
  }
  .hp {
    display: grid;
    gap: 8px;
    border-top: 1px solid var(--border);
  }
  .path {
    overflow-wrap: anywhere;
  }
  h2 .tiny {
    font-weight: 400;
  }
</style>
