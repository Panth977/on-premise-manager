<script lang="ts">
  import { page } from '$app/state';
  import TemplateShell from '$lib/config-editor/TemplateShell.svelte';
  let { children } = $props();
</script>

{#key page.params.templateId}
  <TemplateShell templateId={page.params.templateId!}>{@render children()}</TemplateShell>
{/key}
