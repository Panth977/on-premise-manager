<script lang="ts">
  /**
   * Site config · Settings (label, host, site depends, not-found page, agent ports) — the shared SiteSettingsForm
   * ($opm, SPEC §21 #102: the same form as the cloud console's site Settings tab). §21 #113: the label is a local edit too.
   */
  import { page } from '$app/state';
  import { draft } from '$lib/state/draft.svelte';
  import SiteConfigFrame from '$lib/ui/SiteConfigFrame.svelte';
  import SiteSettingsForm from '$opm/forms/site/SiteSettingsForm.svelte';

  const slug = $derived(page.params.siteSlug ?? '');
  const issues = $derived(draft.issuesAt(`sites[${slug}]`));
</script>

<SiteConfigFrame title="Settings" sub="Label, host, site order, not-found page and agent ports of this site.">
  {#snippet children(site)}
    <SiteSettingsForm {site} mode="edit" {issues} />
  {/snippet}
</SiteConfigFrame>
