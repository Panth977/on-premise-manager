<script lang="ts">
  /**
   * Import from nginx.conf (…/sites/{s}/edge/import): the shared NginxImport ($opm, SPEC §21 #102 — the same steps as the
   * agent GUI). Nothing changes until Import; then the edge lands in the site in the browser change set (committed with Commit…).
   */
  import type { Edge, Site } from '@opm/shared';
  import NginxImport, { type NginxApplyInfo } from '$opm/forms/edge/NginxImport.svelte';
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import Button from '../../ui/Button.svelte';
  import EmptyState from '../../ui/EmptyState.svelte';
  import { ArrowLeft, FileUp } from '../../ui/icons';
  import DraftBanner from '../DraftBanner.svelte';
  import { catalog } from '../../stores/catalog.svelte';
  import { toast } from '../../toast.svelte';
  import { useLeaveGuard } from '../../util/guard.svelte';
  import type { AnyScope } from '../scope.svelte';

  let { scope, siteSlug }: { scope: AnyScope; siteSlug: string } = $props();
  $effect(() => {
    if (scope.loaded) scope.openSite(siteSlug);
  });
  onMount(() => catalog.ensure());
  let leaving = $state(false);
  const site = $derived<Site | undefined>(scope.siteDrafts[siteSlug]);
  const sitePath = $derived(`${scope.base}/sites/${siteSlug}`);
  function onapply(edge: Edge, info: NginxApplyInfo) {
    if (!site || !scope.canWrite) return;
    site.config.edge = edge;
    leaving = true;
    toast.success(`${info.mode === 'replace' ? 'Replaced the edge with' : 'Appended'} ${info.routes} route${info.routes === 1 ? '' : 's'} from ${info.fileName ?? 'nginx.conf'} — not committed yet`);
    goto(`${sitePath}/edge`);
  }
</script>

{#if !scope.loaded || (!site && scope.serverSite(siteSlug))}
  <div class="stack"><span class="skel" style="height:60px"></span><span class="skel" style="height:300px"></span></div>
{:else if !site}
  <EmptyState icon={FileUp} title="Site {siteSlug} does not exist" text="It may have been removed.">
    {#snippet action()}<Button href="{scope.base}/sites" icon={ArrowLeft}>Back to sites</Button>{/snippet}
  </EmptyState>
{:else}
  {#if scope.kind === 'deployment'}<DraftBanner deploymentId={scope.id} />{/if}
  <div class="head" data-testid="ngx-head">
    <div class="row wrap">
      <Button href="{sitePath}/edge" icon={ArrowLeft} size="sm" data-testid="ngx-back">Edge</Button>
      <h2>Import from nginx.conf</h2>
      <span class="muted small">into site <span class="mono strong">{siteSlug}</span> · {scope.kind === 'template' ? 'template' : 'deployment'}</span>
    </div>
    <p class="small muted">Paste the file (or upload it). Nothing changes until you press Import; then the routes land in the site in this browser; Commit… records them.</p>
  </div>
  <NginxImport {site} disabled={!scope.canWrite} {onapply} oncancel={() => goto(`${sitePath}/edge`)} />
{/if}

<style>
  .head {
    margin-bottom: var(--sp-4);
  }
  .head h2 {
    margin: 0;
    font-size: 18px;
  }
  .head p {
    margin: 6px 0 0;
  }
</style>
