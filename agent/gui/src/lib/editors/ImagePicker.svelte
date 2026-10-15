<script lang="ts">
  /**
   * §21 #113 "Change image…" / the image of a new container (Portainer-like): a catalog version the site knows (head,
   * recent cloud commits, drift-adopted, site images), a docker reference (pulled by this host's Docker when missing;
   * registry credentials only for that pull) or an image already on the host. A reference / host image becomes a site
   * image (<repository>:local-<12 hex>) that the commit carries in its catalog.
   */
  import { api, errMessage } from '$lib/api';
  import { draft } from '$lib/state/draft.svelte';
  import type { SiteImagesResponse } from '@opm/shared/types';
  import Drawer from '$lib/ui/Drawer.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import { bytes as formatBytes } from '$lib/format';

  let { open = $bindable(false), current = null, onpick, title = 'Change image' }: { open?: boolean; current?: { serviceSlug: string; version: string } | null; onpick: (serviceSlug: string, version: string) => void; title?: string } = $props();

  type Tab = 'catalog' | 'reference' | 'host';
  let tab = $state<Tab>('catalog');
  let data = $state<SiteImagesResponse | null>(null);
  let loadErr = $state<string | null>(null);
  let q = $state('');
  let ref = $state('');
  let user = $state('');
  let pass = $state('');
  let busy = $state<string | null>(null);
  let err = $state<string | null>(null);

  $effect(() => {
    if (!open) return;
    err = null;
    loadErr = null;
    api.siteImages({}).then(
      (r) => (data = r),
      (e) => (loadErr = errMessage(e)),
    );
  });

  const rows = $derived.by(() => {
    const cat = data?.catalog ?? draft.base?.catalog;
    if (!cat) return [];
    const out = Object.entries(cat.images).map(([key, im]) => ({ key, im, label: cat.services[im.serviceSlug]?.label ?? im.serviceSlug, site: !im.digest }));
    const f = q.trim().toLowerCase();
    return out.filter((r) => !f || `${r.key} ${r.label} ${r.im.reference ?? ''}`.toLowerCase().includes(f)).sort((a, b) => a.label.localeCompare(b.label) || b.im.version.localeCompare(a.im.version));
  });
  const hostRows = $derived((data?.host ?? []).filter((h) => !q.trim() || h.repoTags.join(' ').toLowerCase().includes(q.trim().toLowerCase())));

  function pick(serviceSlug: string, version: string) {
    onpick(serviceSlug, version);
    open = false;
  }
  function useCatalog(key: string) {
    const cat = data?.catalog;
    const im = cat?.images[key];
    if (!im) return;
    draft.addSiteImage(key, im, cat!.services[im.serviceSlug] ?? { serviceSlug: im.serviceSlug, label: im.serviceSlug, icon: null });
    pick(im.serviceSlug, im.version);
  }
  async function resolve(body: { reference?: string; imageId?: string; pull?: boolean }, id: string) {
    busy = id;
    err = null;
    try {
      const r = await api.resolveSiteImage({ body: { ...body, ...(user.trim() ? { username: user.trim(), password: pass } : {}) } });
      draft.addSiteImage(r.key, r.image, r.service);
      pick(r.image.serviceSlug, r.image.version);
    } catch (e) {
      err = errMessage(e);
    } finally {
      busy = null;
    }
  }
</script>

<Drawer bind:open {title} subtitle="Docker on this site runs it. A catalog version, any docker reference (pulled here), or an image already on this host — committed like every other change." testid="image-picker">
  <div class="stack">
    <SegmentedControl options={[{ value: 'catalog', label: 'Catalog' }, { value: 'reference', label: 'Docker reference' }, { value: 'host', label: 'On this host' }]} value={tab} onchange={(v) => (tab = v)} label="Image source" testid="ip-tab" />
    {#if loadErr}<div class="banner bad small"><Icon name="alert" /><div class="grow">{loadErr}</div></div>{/if}
    {#if err}<div class="banner bad small" role="alert" data-testid="ip-error"><Icon name="alert" /><div class="grow">{err}</div></div>{/if}
    {#if tab !== 'reference'}<input class="input" placeholder="Filter…" bind:value={q} data-testid="ip-filter" />{/if}

    {#if tab === 'catalog'}
      {#if !rows.length}
        <p class="muted small">No catalog images known on this site yet. Use a docker reference.</p>
      {:else}
        <ul class="list" data-testid="ip-catalog">
          {#each rows as r (r.key)}
            {@const cur = current && current.serviceSlug === r.im.serviceSlug && current.version === r.im.version}
            <li>
              <button type="button" class="item" class:cur onclick={() => useCatalog(r.key)} data-testid="ip-cat-{r.key}">
                <Icon name="box" size={14} />
                <span class="grow"><span class="strong">{r.label}</span> <span class="mono small">{r.im.serviceSlug}:{r.im.version}</span>{#if r.im.reference}<span class="mono tiny muted ref">{r.im.reference}</span>{/if}</span>
                {#if r.site}<span class="badge" title="An image of this site's Docker (not in the cloud catalog)">site image</span>{/if}
                {#if cur}<span class="badge accent">current</span>{/if}
              </button>
            </li>
          {/each}
        </ul>
      {/if}
    {:else if tab === 'reference'}
      <label class="lbl" for="ip-ref">Docker reference</label>
      <input id="ip-ref" class="input mono" placeholder="nginx:1.27 · registry.example.com/team/app:2 · redis@sha256:…" bind:value={ref} data-testid="ip-ref" />
      <details class="small">
        <summary>Private registry</summary>
        <div class="stack tight" style="margin-top:6px">
          <input class="input" placeholder="Username" bind:value={user} autocomplete="off" data-testid="ip-user" />
          <input class="input" type="password" placeholder="Password / token (used for this pull only, never stored)" bind:value={pass} autocomplete="new-password" data-testid="ip-pass" />
        </div>
      </details>
      <p class="tiny muted">This host's Docker pulls it when it does not have it (the site needs to reach the registry — or load the image on the host and pick it under “On this host”). Another site pulls the same pinned reference.</p>
      <div class="row">
        <Btn variant="primary" icon="download" busy={busy === 'ref'} disabled={!ref.trim()} onclick={() => resolve({ reference: ref.trim() }, 'ref')} data-testid="ip-use-ref">Use this image</Btn>
        <Btn variant="ghost" icon="refresh" busy={busy === 'pull'} disabled={!ref.trim()} title="Pull again even when the host has it (the tag moved)" onclick={() => resolve({ reference: ref.trim(), pull: true }, 'pull')} data-testid="ip-pull-ref">Pull again</Btn>
      </div>
    {:else}
      {#if !data}
        <div class="row muted small"><span class="spin"></span>Reading the images of this host…</div>
      {:else if !hostRows.length}
        <p class="muted small">No tagged images on this host.</p>
      {:else}
        <ul class="list" data-testid="ip-host">
          {#each hostRows as h (h.id)}
            <li>
              <button type="button" class="item" disabled={!!busy} onclick={() => resolve({ imageId: h.id }, h.id)} data-testid="ip-host-{h.repoTags[0]}">
                {#if busy === h.id}<span class="spin"></span>{:else}<Icon name="layers" size={14} />{/if}
                <span class="grow"><span class="mono small">{h.repoTags.join(', ')}</span><span class="mono tiny muted ref">{h.id.slice(7, 19)} · {formatBytes(h.sizeBytes)}</span></span>
              </button>
            </li>
          {/each}
        </ul>
      {/if}
    {/if}
  </div>
</Drawer>

<style>
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
    max-height: 55vh;
    overflow: auto;
  }
  .item {
    all: unset;
    box-sizing: border-box;
    width: 100%;
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 7px 9px;
    border-radius: var(--r-md);
    cursor: pointer;
    border: 1px solid transparent;
  }
  .item:hover,
  .item:focus-visible {
    background: var(--surface-hover);
  }
  .item.cur {
    border-color: var(--accent);
  }
  .item .grow {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .ref {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .lbl {
    font-weight: var(--fw-medium);
    font-size: var(--fs-sm);
  }
</style>
