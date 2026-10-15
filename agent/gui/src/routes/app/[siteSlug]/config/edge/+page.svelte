<script lang="ts">
  /** Site config · Edge (hosts, routes, ports, certificates) — the whole edge is locally editable. */
  import { page } from '$app/state';
  import { api } from '$lib/api';
  import { overview } from '$lib/state/app.svelte';
  import { draft } from '$lib/state/draft.svelte';
  import type { CertStatus } from '@opm/shared/types';
  import SiteConfigFrame from '$lib/ui/SiteConfigFrame.svelte';
  import EdgeEditor from '$lib/editors/EdgeEditor.svelte';

  const slug = $derived(page.params.siteSlug ?? '');
  const cat = $derived(draft.base?.catalog ?? null);
  const cfg = $derived(draft.asConfig('work'));
  const depId = $derived(overview.data?.deploymentId ?? draft.deploymentId);
  let certs = $state<CertStatus[]>([]);
  $effect(() => {
    const s = slug;
    api
      .listCerts({ query: { site: s } })
      .then((r) => (certs = r.items))
      .catch(() => (certs = []));
  });
</script>

<SiteConfigFrame title="Edge" sub="Hosts, TLS, routes and edge ports of this site. The whole edge can change locally.">
  {#snippet children(site)}
    <EdgeEditor {site} config={cfg} catalog={cat} deploymentId={depId} issues={draft.issuesAt(`sites[${slug}].config.edge`)} {certs} />
  {/snippet}
</SiteConfigFrame>
