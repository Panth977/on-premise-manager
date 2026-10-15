<script lang="ts">
  /** Site settings tab — the shared SiteSettingsForm ($opm, SPEC §21 #102: the same form as the agent's Config › Settings). */
  import type { Site, ValidationIssue } from '@opm/shared';
  import SiteSettingsForm from '$opm/forms/site/SiteSettingsForm.svelte';
  import type { AnyScope } from './scope.svelte';
  let { scope, site, issues }: { scope: AnyScope; site: Site; issues: ValidationIssue[] } = $props();
</script>

<SiteSettingsForm {site} mode={scope.canWrite ? 'edit' : 'read'} {issues} />
