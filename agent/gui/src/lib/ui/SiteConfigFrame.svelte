<script lang="ts">
  /**
   * Frame of one site-config section page (round D: the old Containers / Edge / Site tab row became sidebar sections,
   * routes app/{site}/config/containers | edge | volumes | settings; nav2: shown as the ConfigTabs row under one "Config" head). Page head + head-config loading + "no head" /
   * "site not in the head" states + the local edit bar. The working copy (draft) is shared by every section, so moving
   * between sections of a site keeps the edits.
   */
  import type { Snippet } from 'svelte';
  import { page } from '$app/state';
  import { headConfig } from '$lib/state/app.svelte';
  import { draft } from '$lib/state/draft.svelte';
  import type { Site } from '@opm/shared/types';
  import PageHead from './PageHead.svelte';
  import ConfigTabs from './ConfigTabs.svelte';
  import ChooseSite from './ChooseSite.svelte';
  import Load from './Load.svelte';
  import Empty from './Empty.svelte';
  import NoHead from './NoHead.svelte';
  import LocalEditBar from './LocalEditBar.svelte';
  interface Props {
    title: string;
    sub?: string;
    icon?: string;
    actions?: Snippet;
    children: Snippet<[Site]>;
  }
  let { title, sub, icon, actions, children }: Props = $props();
  const ICONS: Record<string, string> = { Containers: 'box', Edge: 'globe', Volumes: 'archive', Settings: 'sliders' };
  const slug = $derived(page.params.siteSlug ?? '');
  const site = $derived(draft.work?.sites.find((s) => s.siteSlug === slug));
</script>

<PageHead eyebrow={ICONS[title] ? `Site ${slug}` : `Site ${slug} · config`} icon={ICONS[title] ? 'cog' : (icon ?? 'cog')} title={ICONS[title] ? 'Config' : title} {sub} {actions} />
{#if slug !== '*'}<ConfigTabs site={slug} />{/if}

{#if slug === '*'}
  <ChooseSite what="its {title.toLowerCase()}" text={'"All sites" is selected; config sections belong to one site. (Config with "All sites" lists every site.)'} />
{:else}
<Load loading={headConfig.loading} loaded={headConfig.loaded} error={headConfig.error} retry={() => headConfig.load()}>
  {#if !draft.base?.head}
    <section class="card"><NoHead /></section>
  {:else if !site}
    <section class="card"><Empty icon="server" title="Site {slug} is not in the head config" text="Only sites of the head config have a config. Sites are added in the cloud." /></section>
  {:else}
    {@render children(site)}
  {/if}
  <LocalEditBar />
</Load>
{/if}
