<script lang="ts">
  import type { Site } from '@opm/shared';
  import { isExpr } from '@opm/shared';
  import AutoHostText from '../ui/AutoHostText.svelte';
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import ScopeHeader from './ScopeHeader.svelte';
  import Button from '../ui/Button.svelte';
  import EmptyState from '../ui/EmptyState.svelte';
  import SearchInput from '../ui/SearchInput.svelte';
  import ConfirmDialog from '../ui/ConfirmDialog.svelte';
  import Icon from '../ui/Icon.svelte';
  import { Plus, Trash2, Server, ChevronRight } from '../ui/icons';
  import AddSiteModal from './AddSiteModal.svelte';
  import DraftBanner from './DraftBanner.svelte';
  import { siteRefs } from './refs';
  import { catalog } from '../stores/catalog.svelte';
  import type { AnyScope } from './scope.svelte';

  let { scope }: { scope: AnyScope } = $props();
  const isDep = $derived(scope.kind === 'deployment');
  onMount(() => {
    if (scope.kind === 'deployment') scope.loadStatus();
  });
  let q = $state('');
  const sites = $derived(scope.working().sites);
  const rows = $derived(sites.filter((s) => !q || `${s.siteSlug} ${s.label} ${s.config.host ?? 'auto'}`.toLowerCase().includes(q.toLowerCase())));
  const hosts = $derived(scope.hosts(scope.working()));
  const agentOf = (slug: string) => (scope.kind === 'deployment' ? (scope.status?.sites.find((s) => s.siteSlug === slug) ?? null) : null);

  let addOpen = $state(false);
  let rmOpen = $state(false);
  let rm = $state<Site | null>(null);
  const rmRefs = $derived(rm && rmOpen ? siteRefs(scope.working(), rm.siteSlug, (id) => catalog.slugOf(id)) : []);
  const rmEnrolled = $derived(!!rm && !!agentOf(rm.siteSlug)?.enrolled);
  async function doRemove() {
    const slug = rm!.siteSlug;
    const ok = await scope.saveSection('sites', scope.config.sites.filter((s) => s.siteSlug !== slug).map((s) => $state.snapshot(s) as Site), `Site ${slug} removed`);
    void ok;
    return ok;
  }
  const page404 = (id: string | null) => (id ? (catalog.page(id)?.label ?? id) : 'built-in');
</script>

{#if isDep}<DraftBanner deploymentId={scope.id} />{/if}
<ScopeHeader kind={scope.kind} title="Sites" subtitle="Each site is one machine with one agent, its containers and its edge (hosts, TLS, routes)." icon={Server} readOnly={!scope.canWrite}>
  {#snippet actions()}
    <SearchInput bind:value={q} placeholder="Search sites" width="200px" />
    {#if scope.canWrite}<Button variant="primary" icon={Plus} onclick={() => (addOpen = true)} data-testid="add-site">Add site</Button>{/if}
  {/snippet}
</ScopeHeader>

{#if !sites.length}
  <EmptyState icon={Server} title="No sites" text="Add the first site (for example app and db). A deployment needs at least one site to publish.">
    {#snippet action()}{#if scope.canWrite}<Button variant="primary" icon={Plus} onclick={() => (addOpen = true)}>Add site</Button>{/if}{/snippet}
  </EmptyState>
{:else if !rows.length}
  <EmptyState compact title="No match" text="No site matches “{q}”." />
{:else}
  <div class="table-wrap">
    <table class="table" data-testid="sites-table">
      <thead>
        <tr>
          <th>Site</th><th>Label</th><th>Host</th><th>Containers</th><th>Depends on</th>
          {#if isDep}<th>Agent</th>{:else}<th>Not-found page</th><th>Edge</th>{/if}
          <th class="actions"><span class="sr-only">Actions</span></th>
        </tr>
      </thead>
      <tbody>
        {#each rows as s (s.siteSlug)}
          {@const h = hosts[s.siteSlug]}
          {@const a = agentOf(s.siteSlug)}
          <tr class="clickable" onclick={() => goto(`${scope.base}/sites/${s.siteSlug}/containers`)} data-testid="site-row-{s.siteSlug}">
            <td><a href="{scope.base}/sites/{s.siteSlug}/containers" class="mono strong" onclick={(e) => e.stopPropagation()}>{s.siteSlug}</a>{#if scope.siteDirty(s.siteSlug)}<span class="badge amber" style="margin-left:6px">unsaved</span>{/if}</td>
            <td>{s.label}</td>
            <td class="small">
              {#if s.config.host === null}
                {#if scope.detected[s.siteSlug]}<span class="mono" data-testid="site-host-{s.siteSlug}">{scope.detected[s.siteSlug]}</span><div class="xs faint">auto · detected</div>{:else}<span class="auto" data-testid="site-host-{s.siteSlug}">auto · detected on install</span>{/if}
              {:else}
                <span class="mono" data-testid="site-host-{s.siteSlug}">{s.config.host}</span>
                {#if h?.fallback}<div class="xs text-amber" data-tip={h.fallback.message}>no value → auto{#if h.host} · <AutoHostText text={h.host} />{/if}</div>{:else if isExpr(s.config.host)}<div class="xs {h?.error ? 'text-red' : 'faint'}">{#if h?.error}{h.error.message}{:else}→ <AutoHostText text={h?.host ?? ''} />{/if}</div>{/if}
              {/if}
            </td>
            <td>
              <div class="row wrap" style="gap:4px">
                {#each s.config.containers as c (c.containerSlug)}<span class="chip" class:off={!c.enabled} title="{c.serviceSlug}:{c.version}{c.enabled ? '' : ' (disabled)'}">{c.containerSlug}</span>{:else}<span class="faint small">none</span>{/each}
              </div>
            </td>
            <td class="small">{s.config.depends.map((d) => d.siteSlug).join(', ') || '—'}</td>
            {#if isDep}
              <td class="small">
                {#if a?.enrolled}<span class="row"><span class="dot {a.online ? 'green' : 'red'}"></span>{a.online ? 'online' : 'offline'}</span>{:else if scope.kind === 'deployment' && scope.status}<span class="faint">not installed</span>{:else if scope.kind === 'deployment' && scope.statusError}<span class="faint" data-tip="Status could not be loaded">unknown</span>{:else}<span class="skel" style="width:60px"></span>{/if}
              </td>
            {:else}
              <td class="small">{page404(s.config.notFoundPageId)}</td>
              <td class="small mono nowrap">:{s.config.edge.httpPort}{s.config.edge.httpsPort ? ` · :${s.config.edge.httpsPort}` : ''} · {s.config.edge.hosts.length}h · {s.config.edge.routes.length}r</td>
            {/if}
            <td class="actions" onclick={(e) => e.stopPropagation()}>
              {#if scope.canWrite}<Button size="sm" variant="ghost" icon={Trash2} tip="Remove" aria-label="Remove {s.siteSlug}" onclick={() => ((rm = s), (rmOpen = true))} />{/if}
              <Icon icon={ChevronRight} size={14} class="faint" />
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/if}

<AddSiteModal bind:open={addOpen} {scope} />
<ConfirmDialog bind:open={rmOpen} title="Remove site {rm?.siteSlug}?" confirmLabel="Remove site" danger onconfirm={doRemove} testid="remove-site">
  {#snippet body()}
    {#if rmEnrolled}<div class="banner warn small"><div>The agent of <b>{rm?.siteSlug}</b> is enrolled. After the next approved update it stops and removes its containers (data volumes stay) and becomes idle.</div></div>{/if}
    {#if rmRefs.length}<div class="banner warn small"><div><b>Used in {rmRefs.length} place{rmRefs.length === 1 ? '' : 's'}</b>:<ul class="refs">{#each rmRefs.slice(0, 8) as r, i (i)}<li><span class="mono xs">{r.path}</span></li>{/each}</ul></div></div>{/if}
    <p class="muted small">The site, its containers and its edge config are removed from the {isDep ? 'draft' : 'template'}.</p>
  {/snippet}
</ConfirmDialog>

<style>
  .auto {
    display: inline-block;
    padding: 0 7px;
    border-radius: 999px;
    font-size: 11.5px;
    background: var(--accent-soft);
    color: var(--accent-text);
    white-space: nowrap;
  }
  .chip.off {
    opacity: 0.55;
    text-decoration: line-through;
  }
  .refs {
    margin: 6px 0 0;
    padding-left: 16px;
  }
</style>
