<script lang="ts">
  import type { Site, ValidationIssue, Container } from '@opm/shared';
  import { containerOrder, containerPorts } from '@opm/shared';
  import { goto } from '$app/navigation';
  import Button from '../ui/Button.svelte';
  import EmptyState from '../ui/EmptyState.svelte';
  import ConfirmDialog from '../ui/ConfirmDialog.svelte';
  import Toggle from '../ui/Toggle.svelte';
  import IconView from '../ui/IconView.svelte';
  import { Plus, Trash2, Boxes, Pencil, Copy } from '../ui/icons';
  import { catalog } from '../stores/catalog.svelte';
  import { issuesUnder, countBy } from '../util/issues';
  import type { AnyScope } from './scope.svelte';
  import { siteDepends } from './depends';

  /**
   * Containers of a site (instances: the same service + version may run many times under different names).
   * Add / Edit / Duplicate open the full container page (…/containers/new, …/containers/{containerSlug}).
   */
  let { scope, site, issues }: { scope: AnyScope; site: Site; issues: ValidationIssue[] } = $props();
  const cb = $derived(`sites[${site.siteSlug}].config`);
  const prefix = $derived(`${scope.base}/sites/${site.siteSlug}`);
  const order = $derived(containerOrder(site.config));
  // §21 #93: the APPLY start order (explicit + implicit + suggested references, ties in the declared order)
  const applyOrder = $derived.by(() => {
    if (!('order' in order)) return null;
    const w = scope.working();
    return siteDepends({ ...w, sites: w.sites.map((s) => (s.siteSlug === site.siteSlug ? site : s)) }, site.siteSlug, scope.vctx())?.order ?? null;
  });
  const editHref = (c: Container) => `${prefix}/containers/${c.containerSlug}`;
  const dupHref = (c: Container) => `${prefix}/containers/new?duplicate=${encodeURIComponent(c.containerSlug)}`;
  const newHref = $derived(`${prefix}/containers/new`);

  // remove
  let rmOpen = $state(false);
  let rm = $state<Container | null>(null);
  let alsoRoutes = $state(true);
  const rmRoutes = $derived(rm ? site.config.edge.routes.filter((r) => r.action.type === 'proxy' && r.action.containerSlug === rm!.containerSlug) : []);
  const rmDependents = $derived(rm ? site.config.containers.filter((c) => c.depends.some((d) => d.containerSlug === rm!.containerSlug)).map((c) => c.containerSlug) : []);
  function doRemove() {
    const id = rm!.containerSlug;
    site.config.containers = site.config.containers.filter((c) => c.containerSlug !== id);
    for (const c of site.config.containers) c.depends = c.depends.filter((d) => d.containerSlug !== id);
    if (alsoRoutes) site.config.edge.routes = site.config.edge.routes.filter((r) => !(r.action.type === 'proxy' && r.action.containerSlug === id)).map((r, i) => ({ ...r, order: i + 1 }));
  }
  const count = (c: Container) => countBy(issuesUnder(issues, `${cb}.containers[${c.containerSlug}]`));
  function portSummary(c: Container): string[] {
    const img = catalog.image(c.serviceSlug, c.version);
    const rows = img ? containerPorts(c, img) : c.ports.map((p) => ({ ...p, protocol: 'tcp' as const }));
    return rows.map((p) => (p.hostPort === null ? `${p.portName}${p.edge ? '' : ' (no edge)'}` : `${p.hostIp === '127.0.0.1' ? '127.0.0.1:' : ''}${p.hostPort}→${p.portName}`));
  }
</script>

<div class="row between wrap tab-head">
  <p class="muted small">
    {#if applyOrder && applyOrder.order.length > 1}<span data-testid="start-order" data-tip="Dependencies first: Depends on, containers.&lt;slug&gt;.* expressions and hostname mentions; ties keep the table order">Start order: <span class="mono">{applyOrder.order.join(' → ')}</span></span>{#if applyOrder.cycle.length}<span class="text-amber" data-testid="start-order-cycle"> · {applyOrder.cycle.join(', ')} reference each other: they start in the table order</span>{/if}{:else if 'order' in order && site.config.containers.length > 1}Start order: <span class="mono">{order.order.join(' → ')}</span>{:else if 'cycle' in order}<span class="text-red">Depends form a cycle: {order.cycle.join(' → ')}</span>{:else}Each container is a named instance of a service version; the same image may run many times with different env, ports and volumes.{/if}
  </p>
</div>

{#if !site.config.containers.length}
  <EmptyState icon={Boxes} title="No containers" text="Add a container: pick a service and version, give it a name, map its ports and volumes.">
    {#snippet action()}{#if scope.canWrite}<Button variant="primary" icon={Plus} href={newHref}>Add container</Button>{/if}{/snippet}
  </EmptyState>
{:else}
  <div class="table-wrap">
    <table class="table" data-testid="containers-table">
      <thead><tr><th>Name</th><th>Service</th><th>Version</th><th>Ports</th><th>Enabled</th><th></th><th class="actions"><span class="sr-only">Actions</span></th></tr></thead>
      <tbody>
        {#each site.config.containers as c (c.containerSlug)}
          {@const svc = catalog.service(c.serviceSlug)}
          {@const img = catalog.image(c.serviceSlug, c.version)}
          {@const n = count(c)}
          <tr class="clickable" onclick={() => goto(editHref(c))} data-testid="container-row-{c.containerSlug}">
            <td>
              <div class="row">
                <IconView value={svc?.icon ?? null} type="service" size={18} />
                <a href={editHref(c)} class="mono strong" onclick={(e) => e.stopPropagation()} data-testid="container-name">{c.containerSlug}</a>
              </div>
            </td>
            <td><span class="mono small">{c.serviceSlug}</span>{#if svc?.label}<div class="xs faint">{svc.label}</div>{:else if !svc}<div class="xs text-red">unknown service</div>{/if}</td>
            <td class="mono small">{c.version}{#if img && img.import.status !== 'ready'}<span class="badge {img.import.status === 'failed' ? 'red' : 'blue'}" style="margin-left:4px">{img.import.status}</span>{/if}</td>
            <td class="mono xs" data-testid="container-ports">{#each portSummary(c) as p, i (i)}<div>{p}</div>{:else}—{/each}</td>
            <td onclick={(e) => e.stopPropagation()}><Toggle checked={c.enabled} disabled={!scope.canWrite} onchange={(v) => (c.enabled = v)} testid="enabled-{c.containerSlug}" /></td>
            <td>
              {#if n.errors}<span class="badge red">{n.errors}</span>{:else if n.warnings}<span class="badge amber">{n.warnings}</span>{/if}
            </td>
            <td class="actions nowrap" onclick={(e) => e.stopPropagation()}>
              <Button size="sm" variant="ghost" icon={Pencil} tip={scope.canWrite ? 'Edit' : 'Open'} aria-label="Edit {c.containerSlug}" href={editHref(c)} data-testid="edit-container-{c.containerSlug}" />
              {#if scope.canWrite}
                <Button size="sm" variant="ghost" icon={Copy} tip="Duplicate" aria-label="Duplicate {c.containerSlug}" href={dupHref(c)} data-testid="dup-container-{c.containerSlug}" />
                <Button size="sm" variant="ghost" icon={Trash2} tip="Remove" aria-label="Remove {c.containerSlug}" onclick={() => ((rm = c), (alsoRoutes = true), (rmOpen = true))} data-testid="rm-container-{c.containerSlug}" />
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/if}

<ConfirmDialog bind:open={rmOpen} title="Remove container {rm?.containerSlug ?? ''}?" confirmLabel="Remove" danger onconfirm={doRemove} testid="remove-container">
  {#snippet body()}
    <p class="muted small">Removed from the change set in this browser; Commit… records it. Its site volumes stay declared (and on disk).</p>
    {#if rmDependents.length}<div class="banner warn small"><div>Containers that depend on it lose that dependency: <span class="mono">{rmDependents.join(', ')}</span></div></div>{/if}
    {#if rmRoutes.length}
      <div class="banner warn small">
        <div>
          <b>{rmRoutes.length} edge route{rmRoutes.length === 1 ? '' : 's'} target it:</b> {rmRoutes.map((r) => r.routeId).join(', ')}
          <label class="check" style="margin-top:6px"><input type="checkbox" bind:checked={alsoRoutes} /> also remove these routes</label>
        </div>
      </div>
    {/if}
  {/snippet}
</ConfirmDialog>

<style>
  .tab-head {
    margin-bottom: var(--sp-3);
  }
</style>
