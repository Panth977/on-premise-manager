<script lang="ts">
  /**
   * Site config · Containers (list; the editor is containers/{containerSlug}). §21 #113 a site is standalone: add (pick
   * an image: catalog version, docker reference, host image), duplicate and remove here — committed from the change bar.
   */
  import { goto } from '$app/navigation';
  import { base } from '$app/paths';
  import { page } from '$app/state';
  import { draft } from '$lib/state/draft.svelte';
  import { toasts } from '$lib/state/toast.svelte';
  import { containerMounts, containerPorts, duplicateContainer, imageKey, suggestContainerSlug } from '@opm/shared/validate';
  import type { Container } from '@opm/shared/types';
  import { freshContainer } from '$opm/forms/container/container';
  import ImagePicker from '$lib/editors/ImagePicker.svelte';
  import Confirm from '$lib/ui/Confirm.svelte';
  import SiteConfigFrame from '$lib/ui/SiteConfigFrame.svelte';
  import Empty from '$lib/ui/Empty.svelte';
  import FieldIssues from '$lib/ui/FieldIssues.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import EntityIcon from '$lib/ui/EntityIcon.svelte';
  import InfoTip from '$lib/ui/InfoTip.svelte';
  import { loadSiteFileTexts, siteDepends } from '$lib/depends';

  const slug = $derived(page.params.siteSlug ?? '');
  const headSite = $derived(draft.base?.config.sites.find((s) => s.siteSlug === slug));
  const cat = $derived(draft.base?.catalog ?? null);
  const pfx = $derived(`sites[${slug}]`);
  const labelOf = (s: string) => cat?.services[s]?.label ?? '';
  const imgOf = (c: { serviceSlug: string; version: string }) => cat?.images[imageKey(c.serviceSlug, c.version)];
  /** §21 #93: implicit / suggested depends + the start order of the APPLY (working copy) */
  const sd = $derived(siteDepends(slug));
  const implicitOf = (s: string) => sd?.deps.find((d) => d.containerSlug === s)?.implicit ?? [];
  $effect(() => {
    if (slug && draft.work) loadSiteFileTexts(slug);
  });
  const work = $derived(draft.work?.sites.find((s) => s.siteSlug === slug));
  let addOpen = $state(false);
  let removing = $state<string | null>(null);
  let removeOpen = $state(false);
  function add(serviceSlug: string, version: string) {
    const img = draft.base?.catalog?.images[imageKey(serviceSlug, version)];
    if (!work || !img) return;
    const name = suggestContainerSlug(serviceSlug, work.config.containers.map((c) => c.containerSlug));
    work.config.containers.push(freshContainer(serviceSlug, version, img, name, (draft.work?.secrets ?? []).map((s) => s.secretSlug)));
    toasts.ok(`Container ${name} added`, 'Set ports, volumes and env, then commit from the change bar.');
    goto(`${base}/app/${slug}/config/containers/${name}`);
  }
  function duplicate(c: Container) {
    if (!work) return;
    const name = suggestContainerSlug(c.serviceSlug, work.config.containers.map((x) => x.containerSlug));
    work.config.containers.push(duplicateContainer($state.snapshot(c) as Container, name));
    toasts.ok(`Container ${name} added (copy of ${c.containerSlug})`);
  }
  function remove() {
    if (!work || !removing) return;
    const gone = removing;
    work.config.containers = work.config.containers.filter((c) => c.containerSlug !== gone);
    for (const c of work.config.containers) c.depends = c.depends.filter((d) => d.containerSlug !== gone);
    toasts.ok(`Container ${gone} removed`, 'Commit from the change bar to remove it on this site.');
    removing = null;
  }
</script>

<SiteConfigFrame title="Containers" sub="Add, change, duplicate and remove containers of this site — any image: a catalog version, a docker reference or an image on this host. Every change is a commit of this site's branch.">
  {#snippet children(site)}
    {@const changed = (s: string) => JSON.stringify(site.config.containers.find((c) => c.containerSlug === s)) !== JSON.stringify(headSite?.config.containers.find((c) => c.containerSlug === s))}
    <section class="card">
      <div class="card-head">
        <h2>Containers</h2>
        <span class="grow"></span>
        <Btn icon="plus" variant="primary" disabled={!draft.work} onclick={() => (addOpen = true)} data-testid="add-container">Add container</Btn>
      </div>
      {#if site.config.containers.length === 0}
        <Empty icon="box" title="No containers on this site" text="Add one (any docker image), or merge a cloud commit on the Branch page."><Btn icon="plus" disabled={!draft.work} onclick={() => (addOpen = true)}>Add container</Btn></Empty>
      {:else}
        <div class="table-wrap">
          <table class="table" data-testid="containers">
            <thead><tr><th>Name</th><th>Service</th><th>Image</th><th>Enabled</th><th>Ports</th><th>Mounts</th><th>Depends</th><th>Restart</th><th></th></tr></thead>
            <tbody>
              {#each site.config.containers as c (c.containerSlug)}
                {@const im = imgOf(c)}
                {@const pubs = im ? containerPorts(c, im).filter((p) => p.hostPort !== null) : []}
                {@const deps = [...c.depends.map((d) => ({ slug: d.containerSlug, from: null as string[] | null })), ...implicitOf(c.containerSlug).map((d) => ({ slug: d.containerSlug, from: d.from }))]}
                <tr class="clickable" class:changed={changed(c.containerSlug)} onclick={() => goto(`${base}/app/${slug}/config/containers/${c.containerSlug}`)} data-testid="container-row-{c.containerSlug}">
                  <td class="nowrap"><a href="{base}/app/{slug}/config/containers/{c.containerSlug}" class="mono"><strong>{c.containerSlug}</strong></a></td>
                  <td><div class="svc"><span data-testid="icon-{c.containerSlug}"><EntityIcon icon={cat?.services[c.serviceSlug]?.icon} size={16} tile /></span><div><span class="mono small">{c.serviceSlug}</span><div class="tiny muted">{labelOf(c.serviceSlug)}</div></div></div></td>
                  <td class="nowrap"><span class="mono small">{c.serviceSlug}:{c.version}</span>{#if im && !(im as { digest?: string }).digest}<span class="badge tiny" title="A site image (this site's Docker, not the cloud catalog){(im as { reference?: string }).reference ? ': ' + (im as { reference?: string }).reference : ''}">site image</span>{/if}</td>
                  <td>{#if c.enabled}<span class="badge ok">enabled</span>{:else}<span class="badge">disabled</span>{/if}</td>
                  <td class="small">{pubs.map((p) => `${p.portName}→${p.hostPort}`).join(', ') || '—'}</td>
                  <td class="small">{im ? containerMounts(c, im).length : c.volumes.length}</td>
                  <td class="small">{#each deps as d, k (d.slug)}{k ? ', ' : ''}{#if d.from}<span class="imp" title="implicit (from {d.from.join(', ')})">{d.slug}<sup>implicit</sup></span>{:else}{d.slug}{/if}{:else}—{/each}</td>
                  <td class="small">{c.restart}</td>
                  <td class="actions">
                    {#if changed(c.containerSlug)}<span class="badge accent">edited</span>{/if}
                    <Btn size="xs" variant="ghost" icon="copy" disabled={!draft.work} title="Duplicate" aria-label="Duplicate {c.containerSlug}" onclick={(e) => (e.stopPropagation(), duplicate(c))} data-testid="dup-{c.containerSlug}" />
                    <Btn size="xs" variant="ghost" icon="trash" disabled={!draft.work} title="Remove" aria-label="Remove {c.containerSlug}" onclick={(e) => (e.stopPropagation(), (removing = c.containerSlug), (removeOpen = true))} data-testid="remove-{c.containerSlug}" />
                  </td>
                </tr>
                {#if draft.issuesAt(`${pfx}.config.containers[${c.containerSlug}]`).length}
                  <tr class="issue-row"><td colspan="9"><FieldIssues fix issues={draft.issuesAt(`${pfx}.config.containers[${c.containerSlug}]`)} /></td></tr>
                {/if}
              {/each}
            </tbody>
          </table>
        </div>
        {#if sd && sd.order.order.length > 1}
          <div class="card-body order" data-testid="start-order">
            <span class="muted small">Start order<InfoTip size={12} text="The order this site's update starts the enabled containers: Depends on, implicit references ({'{{'} containers.X.* {'}}'}) and suggested hostname mentions first; ties keep the declared order. Explicit and implicit depends also wait until ready." /></span>
            <span class="mono small seq">{sd.order.order.join(' → ')}</span>
            {#if sd.order.cycle.length}<span class="badge warn" data-testid="start-order-cycle" title="These containers reference each other: they start in the declared order">cycle: {sd.order.cycle.join(', ')}</span>{/if}
          </div>
        {/if}
      {/if}
    </section>
  {/snippet}
</SiteConfigFrame>

<ImagePicker bind:open={addOpen} onpick={add} title="Image of the new container" />
<Confirm bind:open={removeOpen} title="Remove container {removing ?? ''}?" confirmLabel="Remove" danger onconfirm={remove} testid="remove-confirm">
  <p>The container is removed from site {slug} when you commit (a pre-change snapshot is taken first, so History → revert brings it back). Its data volumes stay.</p>
</Confirm>

<style>
  .svc {
    display: flex;
    gap: 9px;
    align-items: center;
  }
  tr.issue-row td {
    padding-top: 0;
    padding-left: 24px;
    background: var(--surface-2);
  }
  .imp {
    color: var(--text-2);
    cursor: help;
  }
  .imp sup {
    font-size: 9px;
    color: var(--text-3);
    margin-left: 1px;
  }
  .order {
    display: flex;
    gap: 8px;
    align-items: baseline;
    flex-wrap: wrap;
    border-top: 1px solid var(--border);
  }
  .order .seq {
    overflow-wrap: anywhere;
    min-width: 0;
  }
  tr.changed td {
    background: color-mix(in srgb, var(--accent-soft) 45%, transparent);
  }
</style>
