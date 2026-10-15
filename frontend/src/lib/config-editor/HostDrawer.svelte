<script lang="ts">
  /**
   * Add / edit an edge host — the shared HostForm ($opm, SPEC §21 #102: the same form in the agent GUI) in a wide drawer.
   * Applies to the site in the browser change set (§21 #105); Commit… records it.
   */
  import type { EdgeHost, Site } from '@opm/shared';
  import HostForm, { applyHost, newHost } from '$opm/forms/edge/HostForm.svelte';
  import Drawer from '../ui/Drawer.svelte';
  import Button from '../ui/Button.svelte';
  import type { ConfigScope } from './scope.svelte';

  let { open = $bindable(), scope, site, host = null }: { open: boolean; scope: ConfigScope; site: Site; host?: EdgeHost | null } = $props();
  let value = $state<EdgeHost>(newHost());
  let submitted = $state(false);
  let valid = $state(false);
  $effect(() => {
    if (!open) return;
    value = host ? (structuredClone($state.snapshot(host)) as EdgeHost) : newHost();
    submitted = false;
  });
  const ro = $derived(!scope.canWrite);
  function apply(e: Event) {
    e.preventDefault();
    submitted = true;
    if (!valid) return;
    applyHost(site, host, $state.snapshot(value) as EdgeHost);
    open = false;
  }
</script>

<Drawer bind:open title={ro ? `Host ${host?.hostname ?? ''}` : host ? `Edit host ${host.hostname}` : 'Add host'} width="wide" subtitle={ro ? 'Read only' : 'Applies to the change set; Commit… records it.'} testid="host-drawer">
  <form id="host-form-el" onsubmit={apply} novalidate>
    {#if open}<HostForm bind:value bind:valid {site} original={host} mode={ro ? 'read' : host ? 'edit' : 'create'} {submitted} />{/if}
  </form>
  {#snippet footer()}
    <Button onclick={() => (open = false)}>{ro ? 'Close' : 'Cancel'}</Button>
    {#if !ro}<Button type="submit" form="host-form-el" variant="primary" disabled={submitted && !valid} data-testid="host-apply">{host ? 'Apply' : 'Add host'}</Button>{/if}
  {/snippet}
</Drawer>
