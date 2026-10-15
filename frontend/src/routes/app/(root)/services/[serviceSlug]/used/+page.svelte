<script lang="ts">
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import { api } from '$lib/api';
  import { Loader } from '$lib/resource.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import UsageTable from '$lib/images/UsageTable.svelte';
  import { Link } from '$lib/ui/icons';
  const sid = $derived(page.params.serviceSlug!);
  const svc = new Loader(() => api.getService({ serviceSlug: sid }));
  const used = new Loader(() => api.serviceUsage({ serviceSlug: sid }).then((r) => r.items));
  $effect(() => {
    void sid;
    untrack(() => {
      svc.load();
      used.load();
    });
  });
</script>

<svelte:head><title>Used by · {svc.data?.serviceSlug ?? ''} · OPM</title></svelte:head>
<div class="page">
  <PageHeader title="Used by" icon={Link} subtitle="Templates and deployments (draft or head) that use any version of {svc.data?.serviceSlug ?? 'this service'}." back={{ href: `/app/services/${sid}`, label: svc.data?.label ?? 'Service' }} />
  <UsageTable items={used.data} loading={used.loading} error={used.error} onretry={() => used.load()} emptyText="No template or deployment uses this service." />
</div>
