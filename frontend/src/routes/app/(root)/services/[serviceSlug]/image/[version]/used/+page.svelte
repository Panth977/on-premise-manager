<script lang="ts">
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import { api } from '$lib/api';
  import { Loader } from '$lib/resource.svelte';
  import PageHeader from '$lib/ui/PageHeader.svelte';
  import UsageTable from '$lib/images/UsageTable.svelte';
  import { Link } from '$lib/ui/icons';
  const sid = $derived(page.params.serviceSlug!);
  const tag = $derived(page.params.version!);
  const svc = new Loader(() => api.getService({ serviceSlug: sid }));
  const used = new Loader(() => api.imageUsage({ serviceSlug: sid, version: tag }).then((r) => r.items));
  $effect(() => {
    void sid;
    void tag;
    untrack(() => {
      svc.load();
      used.load();
    });
  });
</script>

<svelte:head><title>Used by · {tag} · OPM</title></svelte:head>
<div class="page">
  <PageHeader title="Used by {svc.data?.serviceSlug ?? ''}:{tag}" icon={Link} subtitle="Templates and deployments (draft, head or open proposal) that use this version." back={{ href: `/app/services/${sid}/image/${encodeURIComponent(tag)}`, label: `Version ${tag}` }} />
  <UsageTable items={used.data} loading={used.loading} error={used.error} onretry={() => used.load()} emptyText="Not used. You can delete this image." />
</div>
