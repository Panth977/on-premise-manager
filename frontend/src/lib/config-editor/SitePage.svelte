<script lang="ts">
  /**
   * One section of a site (round D: own route per section, no tab row): …/sites/{site}/containers | edge | volumes |
   * settings. Page header = section title + the site, its label (edit), changed dot, validation counter and the
   * section's primary action. §21 #105: every edit goes straight into the scope's browser change set (shared by every
   * page of the template / deployment); the scope's ONE change bar (PendingBar, in the shell) commits it.
   */
  import { onMount } from 'svelte';
  import type { ValidationIssue } from '@opm/shared';
  import Button from '../ui/Button.svelte';
  import IssuesList from '../ui/IssuesList.svelte';
  import EmptyState from '../ui/EmptyState.svelte';
  import PageHeader from '../ui/PageHeader.svelte';
  import Icon from '../ui/Icon.svelte';
  import { Server, Pencil, ArrowLeft, CircleAlert, TriangleAlert, Plus, Boxes, Globe, HardDrive, Settings, Wrench } from '../ui/icons';
  import ContainersTab from './ContainersTab.svelte';
  import EdgeTab from './EdgeTab.svelte';
  import SiteTab from './SiteTab.svelte';
  import SiteVolumes from './SiteVolumes.svelte';
  import SiteDevTools from './devtools/SiteDevTools.svelte';
  import DraftBanner from './DraftBanner.svelte';
  import { issuesUnder, countBy } from '../util/issues';
  import { SECTION_TITLE, type SiteSection } from '../shell/crumbs';
  import type { AnyScope } from './scope.svelte';

  let { scope, siteSlug, section }: { scope: AnyScope; siteSlug: string; section: SiteSection } = $props();

  $effect(() => {
    if (scope.loaded) scope.openSite(siteSlug);
  });
  onMount(() => {
    if (scope.kind === 'deployment') scope.loadStatus();
  });
  const site = $derived(scope.siteDrafts[siteSlug]);
  const exists = $derived(!!scope.serverSite(siteSlug));
  const dirty = $derived(scope.siteDirty(siteSlug));
  const prefix = $derived(`${scope.base}/sites/${siteSlug}`);

  const all = $derived(site ? scope.validate().list : []);
  const base = $derived(`sites[${siteSlug}]`);
  const siteIssues = $derived<ValidationIssue[]>([...issuesUnder(all, base), ...issuesUnder(scope.saveIssues, base).filter((s) => !all.some((a) => a.path === s.path && a.code === s.code))]);
  const counts = $derived(countBy(siteIssues));

  function linkOf(i: ValidationIssue): string | null {
    const m = /^sites\[[^\]]+\]\.config\.containers\[([^\]]+)\]/.exec(i.path);
    if (m) return site?.config.containers.some((c) => c.containerSlug === m[1]) ? `${prefix}/containers/${m[1]}` : `${prefix}/containers`;
    const sec = /^sites\[[^\]]+\]\.config\.(containers|edge|volumes|devTools)/.exec(i.path)?.[1] ?? 'settings';
    return `${prefix}/${sec === 'devTools' ? 'devtools' : sec}`;
  }
  const ICONS = { containers: Boxes, edge: Globe, volumes: HardDrive, devtools: Wrench, settings: Settings } as const;
  const SUBS: Record<SiteSection, string> = {
    containers: 'Named instances of service versions on this machine.',
    edge: 'Hosts, TLS and routes of the edge proxy of this site.',
    volumes: 'The data folders of this site; containers mount them by name.',
    devtools: 'Connections the agent GUI Dev tools list ready-to-use (presets).',
    settings: 'Host, agent ports, site dependencies and the not-found page.',
  };

  let addVolume = $state(false);
  let addPreset = $state(false);
</script>

{#if !scope.loaded}
  <div class="stack"><span class="skel" style="height:60px"></span><span class="skel" style="height:300px"></span></div>
{:else if !exists}
  <EmptyState icon={Server} title="Site {siteSlug} does not exist" text="It may have been removed.">
    {#snippet action()}<Button href="{scope.base}/sites" icon={ArrowLeft}>Back to sites</Button>{/snippet}
  </EmptyState>
{:else if site}
  {#if scope.kind === 'deployment'}<DraftBanner deploymentId={scope.id} />{/if}
  <div class="sticky" data-testid="site-editor" data-section={section}>
    <PageHeader title={SECTION_TITLE[section]} icon={ICONS[section]} readOnly={!scope.canWrite}>
      {#snippet meta()}
        <span class="vc" data-testid="validation-counter">
          {#if counts.errors}<span class="badge red"><Icon icon={CircleAlert} size={11} />{counts.errors} error{counts.errors === 1 ? '' : 's'}</span>{/if}
          {#if counts.warnings}<span class="badge amber"><Icon icon={TriangleAlert} size={11} />{counts.warnings} warning{counts.warnings === 1 ? '' : 's'}</span>{/if}
          {#if !counts.errors && !counts.warnings}<span class="badge green">valid</span>{/if}
        </span>
        {#if dirty}<span class="dirty" data-tip="Site {siteSlug} has changes not committed yet" data-testid="dirty-dot"></span>{/if}
      {/snippet}
      <div class="row wrap small muted sub">
        <span>Site <span class="mono strong" data-testid="site-slug">{siteSlug}</span> · <span data-testid="site-label-text">{site.label}</span></span>
        {#if scope.canWrite && section !== 'settings'}<a class="lnk" href="{prefix}/settings" data-testid="edit-label"><Icon icon={Pencil} size={11} />Edit label</a>{/if}
        <span class="faint hide-sm">· {SUBS[section]}</span>
      </div>
      {#snippet actions()}
        {#if scope.canWrite}
          {#if section === 'containers'}<Button icon={Plus} href="{prefix}/containers/new" data-testid="add-container">Add container</Button>{/if}
          {#if section === 'volumes'}<Button icon={Plus} onclick={() => (addVolume = true)} data-testid="add-volume">Add volume</Button>{/if}
          {#if section === 'devtools'}<Button icon={Plus} onclick={() => (addPreset = true)} data-testid="add-devtool">Add preset</Button>{/if}
        {/if}
      {/snippet}
    </PageHeader>
  </div>

  <div class="body">
    {#if section === 'edge'}
      <EdgeTab {scope} {site} issues={siteIssues} />
    {:else if section === 'settings'}
      <SiteTab {scope} {site} issues={siteIssues} />
    {:else if section === 'volumes'}
      <SiteVolumes {scope} {site} issues={siteIssues} bind:addOpen={addVolume} />
    {:else if section === 'devtools'}
      <SiteDevTools {scope} {site} issues={siteIssues} bind:addOpen={addPreset} />
    {:else}
      <ContainersTab {scope} {site} issues={siteIssues} />
    {/if}
  </div>


  <div class="section">
    <IssuesList issues={siteIssues} title="Validation of site {siteSlug}" collapsible {linkOf} emptyText="No problems: this site can be committed." testid="site-issues" />
  </div>
{/if}


<style>
  .sticky {
    position: sticky;
    top: var(--sticky-top, 0px);
    z-index: var(--z-sticky);
    background: var(--bg);
    padding-top: 8px;
    margin: -8px 0 var(--sp-4);
    border-bottom: 1px solid var(--border);
  }
  .sticky :global(.ph) {
    margin-bottom: 12px;
  }
  .sub {
    margin-top: 4px;
    gap: 8px;
  }
  .lnk {
    border: 0;
    background: none;
    padding: 0;
    color: var(--accent-text);
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    gap: 3px;
    font-size: var(--fs-sm);
  }
  .lnk:hover {
    text-decoration: underline;
  }
  .dirty {
    width: 9px;
    height: 9px;
    border-radius: 50%;
    background: var(--amber);
    display: inline-block;
  }
  .vc {
    display: inline-flex;
    gap: 4px;
  }
  @media (max-width: 720px) {
    .sticky {
      position: static;
    }
    .hide-sm {
      display: none;
    }
  }
</style>
