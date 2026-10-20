<script lang="ts">
  /**
   * Site → Volumes (round D): the declared data folders of a site. Table name · label · description · Back up · Used by
   * · Edit / Delete. Delete of a volume that a container mounts is blocked (dialog lists the containers). Every change
   * goes into the site working copy (saved with Save template / Save draft). §21 #101: "Starts with" links to the volume's
   * content page (own file tree, persist to cloud); a legacy File seed is flagged red.
   */
  import { goto } from '$app/navigation';
  import type { Site, SiteVolume, ValidationIssue } from '@opm/shared';
  import { volumeUsers, seedLabel, isLegacyFileSeed } from '@opm/shared';
  import Button from '../ui/Button.svelte';
  import Toggle from '../ui/Toggle.svelte';
  import Modal from '../ui/Modal.svelte';
  import ConfirmDialog from '../ui/ConfirmDialog.svelte';
  import EmptyState from '../ui/EmptyState.svelte';
  import Icon from '../ui/Icon.svelte';
  import { Plus, Pencil, Trash2, HardDrive, Info, Lock, FolderOpen, Cloud } from '../ui/icons';
  import VolumeDrawer from './VolumeDrawer.svelte';
  import { issuesUnder, sevClass } from '../util/issues';
  import type { AnyScope } from './scope.svelte';

  let { scope, site, issues, addOpen = $bindable(false) }: { scope: AnyScope; site: Site; issues: ValidationIssue[]; addOpen?: boolean } = $props();
  const ro = $derived(!scope.canWrite);
  const prefix = $derived(`${scope.base}/sites/${site.siteSlug}`);
  const vb = $derived(`sites[${site.siteSlug}].config.volumes`);
  const volumes = $derived(site.config.volumes ?? []);
  const users = $derived(volumeUsers(site.config));
  const taken = $derived(volumes.map((v) => v.volumeSlug));
  const rowIssues = (slug: string) => issuesUnder(issues, `${vb}[${slug}]`);
  const listIssues = $derived(issues.filter((i) => i.path === vb));

  let editing = $state<SiteVolume | null>(null);
  let editOpen = $state(false);
  /** one modal for add (the header button binds addOpen) and edit */
  const modalGet = () => addOpen || editOpen;
  const modalSet = (v: boolean) => {
    if (!v) ((addOpen = false), (editOpen = false));
  };
  const contentHref = (slug: string) => `${prefix}/volumes/content?volume=${encodeURIComponent(slug)}`;
  function add(v: SiteVolume, openContent = false) {
    site.config.volumes = [...volumes, v];
    if (openContent) void goto(contentHref(v.volumeSlug));
  }
  function apply(v: SiteVolume) {
    site.config.volumes = volumes.map((x) => (x.volumeSlug === v.volumeSlug ? v : x));
  }
  function setBackup(slug: string, on: boolean) {
    const v = volumes.find((x) => x.volumeSlug === slug);
    if (v) v.backup = on;
  }

  let rm = $state<SiteVolume | null>(null);
  let rmOpen = $state(false);
  let blockedOpen = $state(false);
  const rmUsers = $derived(rm ? (users[rm.volumeSlug] ?? []) : []);
  function askDelete(v: SiteVolume) {
    rm = v;
    if ((users[v.volumeSlug] ?? []).length) blockedOpen = true;
    else rmOpen = true;
  }
  function doDelete() {
    const slug = rm!.volumeSlug;
    site.config.volumes = volumes.filter((v) => v.volumeSlug !== slug);
  }
  const mountText = (u: { imageVolume: string | null; mountPath: string | null }) => (u.imageVolume ? (u.mountPath ? `${u.imageVolume} → ${u.mountPath}` : u.imageVolume) : (u.mountPath ?? 'extra mount'));
</script>

<div class="banner info small" data-testid="volumes-info">
  <Icon icon={Info} size={15} class="icon" />
  <div>Volumes are folders on the site server (auto: <code class="inline">&lt;agentHome&gt;/volumes/&lt;name&gt;</code>; <code class="inline">./</code> = the agent home). <b>Back up</b> = included in snapshots, wherever the folder is. Host paths are never backed up. A volume is a <b>folder</b> or a <b>single file</b> and may <b>start with</b> content (applied only when it is created or empty on a site).</div>
</div>
{#each listIssues as x, k (k)}<div class="xs {sevClass(x.severity)}">{x.message}</div>{/each}

{#if !volumes.length}
  <EmptyState icon={HardDrive} title="No volumes" text="Declare the data folders of this site (for example cassandra-data), then pick them in the Volumes section of a container.">
    {#snippet action()}{#if !ro}<Button variant="primary" icon={Plus} onclick={() => (addOpen = true)}>Add volume</Button>{/if}{/snippet}
  </EmptyState>
{:else}
  <div class="table-wrap">
    <table class="table" data-testid="volumes-table">
      <thead><tr><th>Name</th><th>Kind</th><th class="hide-sm">Starts with</th><th class="hide-sm">Label</th><th class="hide-sm">Description</th><th class="hide-sm">On the server</th><th>Back up</th><th>Used by</th><th class="actions"><span class="sr-only">Actions</span></th></tr></thead>
      <tbody>
        {#each volumes as v (v.volumeSlug)}
          {@const us = users[v.volumeSlug] ?? []}
          {@const errs = rowIssues(v.volumeSlug)}
          <tr data-testid="volume-row-{v.volumeSlug}" class:err={errs.some((x) => x.severity === 'error')}>
            <td>
              <span class="mono strong nowrap">{v.volumeSlug}</span>
              {#if v.label && v.label !== v.volumeSlug}<div class="xs faint show-sm">{v.label}</div>{/if}
              {#each errs as x, k (k)}<div class="xs {sevClass(x.severity)}" data-testid="volume-issue-{v.volumeSlug}">{x.code === 'V_VOLUME_UNUSED' ? 'not mounted by any container (data stays on disk)' : x.message}</div>{/each}
            </td>
            <td><span class="badge {v.kind === 'file' ? 'violet' : 'gray'}" data-testid="volume-kind-{v.volumeSlug}">{v.kind === 'file' ? 'single file' : 'folder'}</span></td>
            <td class="small hide-sm" title={v.seed?.kind === 'files' ? v.seed.entries.map((e) => `${e.path} ← ${e.fileSlug}`).join('\n') : undefined}>
              <a class="seedlink" href={contentHref(v.volumeSlug)} data-testid="volume-content-{v.volumeSlug}"><span data-testid="volume-seed-{v.volumeSlug}" class:faint={!v.seed} class:text-red={isLegacyFileSeed(v.seed)}>{seedLabel(v.seed)}</span>{#if isLegacyFileSeed(v.seed)} <span class="badge red xs" data-testid="volume-seed-legacy-{v.volumeSlug}">from Files — convert</span>{/if}</a>
              {#if v.persist}<div><span class="badge blue xs" data-testid="volume-persist-{v.volumeSlug}"><Icon icon={Cloud} size={10} />persist to cloud</span></div>{/if}
            </td>
            <td class="hide-sm">{#if v.label && v.label !== v.volumeSlug}{v.label}{:else}<span class="faint">—</span>{/if}</td>
            <td class="small muted desc hide-sm">{v.description || '—'}</td>
            <td class="small mono hide-sm loc" data-testid="volume-location-{v.volumeSlug}">{#if v.location?.kind === 'path'}{v.location.path}{:else}<span class="auto">auto</span> <span class="faint">&lt;agentHome&gt;/volumes/{v.volumeSlug}</span>{/if}</td>
            <td><Toggle checked={v.backup} onchange={(on) => setBackup(v.volumeSlug, on)} disabled={ro} label={v.backup ? 'on' : 'off'} testid="vol-backup-{v.volumeSlug}" /></td>
            <td>
              <div class="row wrap" style="gap:4px" data-testid="volume-users-{v.volumeSlug}">
                {#each us as u, k (k)}
                  <a class="chip" class:off={!u.enabled} href="{prefix}/containers/{u.containerSlug}" title={mountText(u)} data-testid="volume-user">{u.containerSlug}{#if u.readOnly}<Icon icon={Lock} size={10} />{/if}</a>
                {:else}<span class="faint small">unused</span>{/each}
              </div>
            </td>
            <td class="actions nowrap">
              <Button size="sm" variant="ghost" icon={FolderOpen} tip="Content" aria-label="Content of {v.volumeSlug}" href={contentHref(v.volumeSlug)} data-testid="vol-content-{v.volumeSlug}" />
              {#if !ro}
                <Button size="sm" variant="ghost" icon={Pencil} tip="Edit" aria-label="Edit {v.volumeSlug}" onclick={() => ((editing = v), (editOpen = true))} data-testid="vol-edit-{v.volumeSlug}" />
                <Button size="sm" variant="ghost" icon={Trash2} tip={us.length ? 'In use' : 'Delete'} aria-label="Delete {v.volumeSlug}" onclick={() => askDelete(v)} data-testid="vol-delete-{v.volumeSlug}" />
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/if}

<VolumeDrawer bind:open={modalGet, modalSet} edit={editOpen ? editing : null} {taken} siteSlug={site.siteSlug} {contentHref} onsave={(v, oc) => (editOpen ? apply(v) : add(v, oc))} />

<Modal bind:open={blockedOpen} title="Volume {rm?.volumeSlug ?? ''} is in use" size="sm" testid="volume-in-use">
  <div class="stack">
    <p class="muted small">Pick another source for these mounts first (container page → Volumes), then delete the volume.</p>
    <ul class="users" data-testid="volume-in-use-list">
      {#each rmUsers as u, k (k)}
        <li><a href="{prefix}/containers/{u.containerSlug}" class="mono" onclick={() => (blockedOpen = false)}>{u.containerSlug}</a> <span class="faint xs">{mountText(u)}{u.readOnly ? ' · read-only' : ''}</span></li>
      {/each}
    </ul>
  </div>
  {#snippet footer()}<Button onclick={() => (blockedOpen = false)} data-testid="volume-in-use-close">Close</Button>{/snippet}
</Modal>

<ConfirmDialog bind:open={rmOpen} title="Delete volume {rm?.volumeSlug ?? ''}?" confirmLabel="Delete volume" danger onconfirm={doDelete} testid="delete-volume">
  {#snippet body()}
    <p class="muted small">The volume is removed from the site config (not committed until Commit…). Its folder on the server is <b>not</b> deleted and is no longer backed up.</p>
  {/snippet}
</ConfirmDialog>

<style>
  .banner {
    margin-bottom: var(--sp-3);
  }
  .loc {
    max-width: 240px;
    overflow-wrap: anywhere;
  }
  .auto {
    display: inline-block;
    padding: 0 6px;
    border-radius: 999px;
    font-family: var(--font-sans, inherit);
    font-size: 11px;
    background: var(--accent-soft);
    color: var(--accent-text);
  }
  .desc {
    max-width: 320px;
    overflow-wrap: anywhere;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    font-family: var(--font-mono);
    font-size: 12px;
  }
  .chip:hover {
    text-decoration: none;
    border-color: var(--accent);
  }
  .seedlink {
    color: inherit;
  }
  .seedlink:hover span:first-child {
    text-decoration: underline;
    color: var(--accent-text);
  }
  .chip.off {
    opacity: 0.55;
  }
  tr.err td {
    background: color-mix(in srgb, var(--red) 4%, transparent);
  }
  .users {
    margin: 0;
    padding-left: 18px;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .show-sm {
    display: none;
  }
  @media (max-width: 720px) {
    .hide-sm {
      display: none;
    }
    .show-sm {
      display: block;
    }
  }
</style>
