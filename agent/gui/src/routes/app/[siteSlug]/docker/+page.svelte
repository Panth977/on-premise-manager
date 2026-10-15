<script lang="ts">
  import { page } from '$app/state';
  import type { DockerImagesResponse, PruneImagesResponse } from '@opm/shared/agent-tools';
  import { Resource, poll } from '$lib/state/resource.svelte';
  import { toasts } from '$lib/state/toast.svelte';
  import { tools } from '$lib/tools/api';
  import { bytes } from '$lib/tools/fmt';
  import PageHead from '$lib/ui/PageHead.svelte';
  import Load from '$lib/ui/Load.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Confirm from '$lib/ui/Confirm.svelte';
  import Time from '$lib/ui/Time.svelte';

  const slug = $derived(page.params.siteSlug ?? '*');
  const all = $derived(slug === '*');
  const res = new Resource(async () => {
    const r = (await tools.dockerImages({ siteSlug: slug })) as DockerImagesResponse | { items: DockerImagesResponse[] };
    return 'items' in r && !('siteSlug' in r) ? r.items : [r as DockerImagesResponse];
  });
  $effect(() => {
    void slug;
    res.reset();
    return poll(() => res.load(), 60_000);
  });
  let filter = $state<'all' | 'unused'>('all');
  let pruneSite = $state('');
  let pruneOpen = $state(false);
  let result = $state<(PruneImagesResponse & { site: string }) | null>(null);
  async function prune() {
    try {
      const r = await tools.pruneImages({ siteSlug: pruneSite, body: { typed: 'PRUNE' } });
      result = { ...r, site: pruneSite };
      toasts.ok(`${r.deleted.length} images removed on ${pruneSite}`, `${bytes(r.reclaimedBytes)} reclaimed`);
      res.load();
    } catch (e) {
      toasts.err(e, 'Prune failed');
      return false;
    }
  }
  const prunable = (d: DockerImagesResponse) => d.items.filter((i) => !i.inUse && !i.protected);
  const DF = [
    { k: 'images', label: 'Images', icon: 'layers' },
    { k: 'containers', label: 'Containers', icon: 'container' },
    { k: 'volumes', label: 'Docker volumes', icon: 'hardDrive' },
    { k: 'buildCache', label: 'Build cache', icon: 'archive' },
  ] as const;
</script>

<PageHead icon="layers" eyebrow={all ? 'Docker · all sites' : `Docker · site ${slug}`} title="Docker" sub="Disk used by Docker, images and what uses them, cleanup of unused images." />

<Load loading={res.loading} loaded={res.loaded} error={res.error} retry={() => res.load()} rows={6}>
  <div class="stack sites">
    {#each res.data ?? [] as d (d.siteSlug)}
      {@const pr = prunable(d)}
      <section class="stack" data-testid="docker-{d.siteSlug}">
        {#if all}<h2 class="sh"><Icon name="server" size={16} /><span class="mono">{d.siteSlug}</span><span class="faint small">Docker {d.dockerVersion ?? '—'}</span></h2>{/if}
        {#if d.df}
          <div class="df" data-testid="docker-df">
            {#each DF as x (x.k)}
              {@const v = d.df[x.k]}
              <div class="tile">
                <span class="lbl"><Icon name={x.icon} size={14} />{x.label}<span class="faint">· {v.count}</span></span>
                <strong>{bytes(v.sizeBytes)}</strong>
                <span class="sub">{v.reclaimableBytes ? `${bytes(v.reclaimableBytes)} reclaimable` : 'nothing to reclaim'}</span>
                <div class="meter"><i style="width:{v.sizeBytes ? (100 * v.reclaimableBytes) / v.sizeBytes : 0}%"></i></div>
              </div>
            {/each}
          </div>
        {/if}
        <section class="card">
          <div class="card-head">
            <h2>Images{#if !all}<span class="muted small">{' · '}Docker {d.dockerVersion ?? '—'}</span>{/if}</h2>
            <div class="row">
              <div class="tabs" role="tablist" style="border:0">
                <button role="tab" aria-selected={filter === 'all'} onclick={() => (filter = 'all')}>All<span class="count">{d.items.length}</span></button>
                <button role="tab" aria-selected={filter === 'unused'} onclick={() => (filter = 'unused')}>Unused<span class="count">{d.items.filter((i) => !i.inUse).length}</span></button>
              </div>
              <Btn size="sm" variant="danger" icon="trash" disabled={!pr.length} onclick={() => ((pruneSite = d.siteSlug), (pruneOpen = true))} title={pr.length ? '' : 'Nothing to prune'} data-testid="prune-open">Prune unused ({pr.length})</Btn>
            </div>
          </div>
          <div class="table-wrap">
            <table class="table" data-testid="images">
              <thead><tr><th>Image</th><th class="num">Size</th><th>Created</th><th>Used by</th><th>Status</th></tr></thead>
              <tbody>
                {#each d.items.filter((i) => filter === 'all' || !i.inUse) as i (i.id)}
                  <tr class:dim={!i.inUse}>
                    <td>
                      {#if i.tags.length}{#each i.tags as t, j (t)}<div class="mono small" class:faint={j > 0}>{t}</div>{/each}{:else}<span class="mono small faint">&lt;none&gt;</span>{/if}
                      <div class="mono tiny faint">{i.id.replace('sha256:', '').slice(0, 12)}</div>
                    </td>
                    <td class="num small">{bytes(i.sizeBytes)}</td>
                    <td class="small nowrap"><Time ts={i.created} /></td>
                    <td class="small">{#each i.containers as c (c.name)}<div><span class="mono">{c.ref ?? c.name}</span> <span class="faint">{c.state}</span></div>{:else}<span class="faint">—</span>{/each}</td>
                    <td>
                      <div class="badges">
                        {#if i.inUse}<span class="badge ok">in use</span>{:else}<span class="badge">unused</span>{/if}
                        {#if i.opm}<span class="badge accent" title="an OPM image of this deployment">OPM</span>{/if}
                        {#if i.protected}<span class="badge info" title="used by the head revision: never pruned">protected</span>{/if}
                      </div>
                    </td>
                  </tr>
                {:else}
                  <tr><td colspan="5" class="muted small">No unused image.</td></tr>
                {/each}
              </tbody>
            </table>
          </div>
        </section>
        {#if result && result.site === d.siteSlug}
          <div class="card card-pad res" data-testid="prune-result">
            <div class="row between"><strong><Icon name="check" size={15} /> {result.deleted.length} images removed · {bytes(result.reclaimedBytes)} reclaimed</strong><button class="btn ghost icon xs" aria-label="Dismiss" onclick={() => (result = null)}><Icon name="x" size={14} /></button></div>
            <ul class="small mono">{#each result.deleted as x (x.id)}<li>{x.tags[0] ?? x.id.slice(7, 19)} · {bytes(x.sizeBytes)}</li>{/each}</ul>
            {#if result.skipped.length}<p class="small muted">Skipped: {result.skipped.map((x) => `${x.tags[0] ?? x.id.slice(7, 19)} (${x.reason})`).join(', ')}</p>{/if}
          </div>
        {/if}
      </section>
    {/each}
  </div>
</Load>

<Confirm bind:open={pruneOpen} title="Prune unused images on {pruneSite}?" danger confirmLabel="Prune" typed="PRUNE" onconfirm={prune} testid="prune-confirm">
  <div class="stack tight small">
    <p>Removes {(res.data ?? []).find((d) => d.siteSlug === pruneSite) ? prunable((res.data ?? []).find((d) => d.siteSlug === pruneSite)!).length : 0} images that no container uses ({bytes(((res.data ?? []).find((d) => d.siteSlug === pruneSite)?.items ?? []).filter((i) => !i.inUse && !i.protected).reduce((a, i) => a + i.sizeBytes, 0))}). Images of the current release are kept.</p>
    <p class="muted">Older OPM releases are loaded again from the site's blob store if a rollback needs them.</p>
    <p>Type <strong class="mono">PRUNE</strong> to confirm.</p>
  </div>
</Confirm>

<style>
  .sites {
    gap: 28px;
  }
  .sh {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: var(--fs-lg);
    margin: 0;
  }
  .df {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 10px;
  }
  @media (max-width: 900px) {
    .df {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
  }
  .tile {
    display: flex;
    flex-direction: column;
    gap: 3px;
    padding: 12px 14px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--r-lg);
    min-width: 0;
  }
  .lbl {
    display: flex;
    gap: 6px;
    align-items: center;
    font-size: var(--fs-sm);
    color: var(--text-2);
  }
  .tile strong {
    font-size: 20px;
    font-variant-numeric: tabular-nums;
  }
  .sub {
    font-size: 11.5px;
    color: var(--text-3);
  }
  .meter {
    height: 5px;
    border-radius: 3px;
    background: var(--bg-sunken);
    overflow: hidden;
    margin-top: 4px;
  }
  .meter i {
    display: block;
    height: 100%;
    background: var(--amber);
  }
  .badges {
    display: flex;
    gap: 4px;
    flex-wrap: wrap;
  }
  .res ul {
    margin: 8px 0 0;
    padding-left: 18px;
  }
</style>
