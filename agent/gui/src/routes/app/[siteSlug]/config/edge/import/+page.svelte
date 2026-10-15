<script lang="ts">
  /**
   * "Import from nginx.conf…" (round E, SPEC §12.7) — the shared NginxImport ($opm, SPEC §21 #102: the same steps as the
   * cloud console): nginx.conf → mapping table → Upstreams → certificates → Append / Replace into the local working copy
   * (dirty state; the normal local proposal flow sends it). Replace asks first.
   */
  import { base } from '$app/paths';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { draft } from '$lib/state/draft.svelte';
  import { toasts } from '$lib/state/toast.svelte';
  import type { Edge, Site } from '@opm/shared/types';
  import NginxImport, { type NginxApplyInfo } from '$opm/forms/edge/NginxImport.svelte';
  import SiteConfigFrame from '$lib/ui/SiteConfigFrame.svelte';
  import Confirm from '$lib/ui/Confirm.svelte';
  import Icon from '$lib/ui/Icon.svelte';

  const slug = $derived(page.params.siteSlug ?? '');
  const edgeHref = $derived(`${base}/app/${slug}/config/edge`);
  let pending = $state<{ edge: Edge; info: NginxApplyInfo } | null>(null);
  let confirmOpen = $state(false);
  function write(edge: Edge, info: NginxApplyInfo) {
    const site = draft.work?.sites.find((s) => s.siteSlug === slug);
    if (!site) return;
    site.config.edge = edge;
    toasts.ok(`nginx.conf ${info.mode === 'replace' ? 'replaced the edge' : 'appended'}`, `${info.routes} routes · ${info.hosts} hosts — in this browser, not committed. Review, then commit to apply it.`);
    goto(edgeHref);
  }
  function onapply(edge: Edge, info: NginxApplyInfo) {
    if (info.mode === 'replace') {
      pending = { edge, info };
      confirmOpen = true;
    } else write(edge, info);
  }
</script>

<SiteConfigFrame title="Import from nginx.conf" sub="Every directive is mapped to the edge or explained. Nothing changes until you Append or Replace.">
  {#snippet actions()}<a class="btn sm" href={edgeHref}><Icon name="chevronLeft" size={14} />Edge</a>{/snippet}
  {#snippet children(site)}
    <NginxImport site={site as Site} {onapply} oncancel={() => goto(edgeHref)} />
  {/snippet}
</SiteConfigFrame>

<Confirm bind:open={confirmOpen} title="Replace the whole edge?" danger confirmLabel="Replace" message="The current hosts, routes, ports and body limit of this site's edge are replaced by the import (in this browser only, not committed; Discard undoes it)." onconfirm={() => pending && write(pending.edge, pending.info)} />
