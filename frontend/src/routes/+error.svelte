<script lang="ts">
  import { page } from '$app/state';
  import EmptyState from '$lib/ui/EmptyState.svelte';
  import Button from '$lib/ui/Button.svelte';
  import { CircleAlert, RefreshCw, House } from '$lib/ui/icons';
</script>

<div class="wrap">
  <EmptyState icon={CircleAlert} title={page.status === 404 ? 'Page not found' : `Something went wrong (${page.status})`} text={page.error?.message ?? 'Unexpected error'}>
    {#snippet action()}
      <div class="row">
        <Button href="/app/deployments" icon={House}>Deployments</Button>
        <Button variant="primary" icon={RefreshCw} onclick={() => location.reload()}>Reload</Button>
      </div>
    {/snippet}
  </EmptyState>
</div>

<style>
  .wrap {
    max-width: 560px;
    margin: 12vh auto;
    padding: 0 16px;
  }
</style>
