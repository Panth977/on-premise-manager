<script lang="ts">
  /**
   * Site config · Volumes (round D): the declared data folders of the site (<dataDir>/vol/<volumeSlug>). Locally editable:
   * add, edit label / description / backup, remove when no container mounts it (a used volume is blocked with the list
   * of its containers). Changes go into this browser’s change set (one commit).
   */
  import { base } from '$app/paths';
  import { page } from '$app/state';
  import { draft } from '$lib/state/draft.svelte';
  import { toasts } from '$lib/state/toast.svelte';
  import { locationLabel, newSiteVolume, undeclaredVolumes, volumeUsers } from '@opm/shared/validate';
  import SeedView from '$opm/forms/volumes/SeedView.svelte';
  import { agentHomes } from '$lib/state/app.svelte';
  import type { Site, SiteVolume } from '@opm/shared/types';
  import SiteConfigFrame from '$lib/ui/SiteConfigFrame.svelte';
  import Empty from '$lib/ui/Empty.svelte';
  import FieldIssues from '$lib/ui/FieldIssues.svelte';
  import Issues from '$lib/ui/Issues.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import Confirm from '$lib/ui/Confirm.svelte';
  import VolumeModal from '$lib/editors/VolumeModal.svelte';
  import InfoTip from '$lib/ui/InfoTip.svelte';
  import PathText from '$lib/ui/PathText.svelte';

  const slug = $derived(page.params.siteSlug ?? '');
  const site = $derived(draft.work?.sites.find((s) => s.siteSlug === slug));
  const headVols = $derived(draft.base?.config.sites.find((s) => s.siteSlug === slug)?.config.volumes ?? []);
  const pfx = $derived(`sites[${slug}].config`);
  $effect(() => agentHomes.ensure(slug));
  const users = $derived(site ? volumeUsers(site.config) : {});
  const undeclared = $derived(site ? undeclaredVolumes(site.config) : []);
  const taken = $derived((site?.config.volumes ?? []).map((v) => v.volumeSlug));
  const rowState = (v: SiteVolume): 'new' | 'edited' | null => {
    const h = headVols.find((x) => x.volumeSlug === v.volumeSlug);
    return !h ? 'new' : JSON.stringify(h) !== JSON.stringify(v) ? 'edited' : null;
  };
  const removed = $derived(headVols.filter((h) => !taken.includes(h.volumeSlug)));

  let modalOpen = $state(false);
  let editing = $state<SiteVolume | null>(null);
  function vols(s: Site): SiteVolume[] {
    if (!Array.isArray(s.config.volumes)) s.config.volumes = [];
    return s.config.volumes;
  }
  function save(v: SiteVolume) {
    if (!site) return;
    const list = vols(site);
    const i = list.findIndex((x) => x.volumeSlug === v.volumeSlug);
    if (i >= 0) list[i] = v;
    else list.push(v);
    toasts.ok(i >= 0 ? `Volume ${v.volumeSlug} changed` : `Volume ${v.volumeSlug} added`, 'In this browser, not committed — commit it to apply.');
  }
  function declare(name: string) {
    if (site) vols(site).push(newSiteVolume(name));
  }

  /** put a locally removed volume back at its head position (so an undo leaves no diff) */
  function undo(s: Site, r: SiteVolume) {
    const order = headVols.map((x) => x.volumeSlug);
    const list = vols(s);
    const at = list.findIndex((x) => order.indexOf(x.volumeSlug) > order.indexOf(r.volumeSlug) || !order.includes(x.volumeSlug));
    list.splice(at < 0 ? list.length : at, 0, { ...r });
  }

  let blockedFor = $state<string | null>(null);
  let blockedOpen = $state(false);
  let delFor = $state<string | null>(null);
  let delOpen = $state(false);
  function askDelete(v: SiteVolume) {
    if (users[v.volumeSlug]?.length) {
      blockedFor = v.volumeSlug;
      blockedOpen = true;
    } else {
      delFor = v.volumeSlug;
      delOpen = true;
    }
  }
  function doDelete() {
    if (!site || !delFor) return;
    const name = delFor;
    site.config.volumes = vols(site).filter((x) => x.volumeSlug !== name);
    toasts.ok(`Volume ${name} removed`, 'Its folder stays on the site server (listed as orphan on Backups).');
  }
</script>

<SiteConfigFrame title="Volumes" sub="Data folders of this site. Containers mount them by name; several containers may share one.">
  {#snippet actions()}
    <Btn variant="primary" icon="plus" onclick={() => ((editing = null), (modalOpen = true))} disabled={!site} data-testid="add-volume">Add volume</Btn>
  {/snippet}
  {#snippet children(s)}
    {#if undeclared.length}
      <div class="banner bad small" style="margin-bottom:14px" data-testid="undeclared">
        <Icon name="alert" />
        <div class="grow">Mounted but not declared (V_VOLUME_UNDECLARED): {#each undeclared as u (u)}<span class="mono">{u}</span> <Btn size="xs" onclick={() => declare(u)} data-testid="declare-{u}">Declare</Btn> {/each}</div>
      </div>
    {/if}
    <section class="card">
      <p class="card-hint info-line" data-testid="volumes-info"><InfoTip size={13} label="About volumes">Volumes are folders (or single files) on the site server — Auto: <code>{agentHomes.of(slug)}/volumes/&lt;name&gt;</code>, or a custom path where <code>./</code> = the agent home. Back up = included in snapshots. Starter content is written only when the volume is created or empty (reset on purpose from Backups). Starter content and persist-to-cloud are cloud configuration (read-only here; Edit shows them). Host paths are never backed up.</InfoTip><span>Back up = included in snapshots · starter content fills new / empty volumes · sizes on <a href="{base}/app/backups">Backups</a></span></p>
      {#if (s.config.volumes ?? []).length === 0}
        <Empty icon="archive" title="No volumes on this site" text="Add a volume, then pick it on a container's Volumes section (or use “+ New data volume…” there).">
          <Btn variant="primary" icon="plus" onclick={() => ((editing = null), (modalOpen = true))}>Add volume</Btn>
        </Empty>
      {:else}
        <div class="table-wrap">
          <table class="table" data-testid="volumes-table">
            <thead><tr><th>Volume</th><th>Kind</th><th>Starts with</th><th>On the server</th><th>Back up</th><th>Used by</th><th class="right">Actions</th></tr></thead>
            <tbody>
              {#each s.config.volumes as v, i (v.volumeSlug)}
                {@const st = rowState(v)}
                {@const us = users[v.volumeSlug] ?? []}
                <tr data-testid="volume-row-{v.volumeSlug}" class:changed={!!st}>
                  <td class="vol">
                    <div class="vtop"><span class="mono strong">{v.volumeSlug}</span>{#if v.description}<InfoTip size={13} label="Description of {v.volumeSlug}" text={v.description} />{/if}{#if st}<span class="badge accent">{st}</span>{/if}{#if v.persist}<span class="badge info" title="persist to cloud (cloud configuration): snapshots go to the cloud, the cloud can send versions back" data-testid="vpersist-{v.volumeSlug}">persist</span>{/if}</div>
                    {#if v.label && v.label !== v.volumeSlug}<div class="tiny muted">{v.label}</div>{/if}
                  </td>
                  <td class="small" data-testid="vkind-{v.volumeSlug}">{v.kind === 'file' ? 'single file' : 'folder'}</td>
                  <td class="small" data-testid="vseed-{v.volumeSlug}"><SeedView seed={v.seed} compact testid="vseed-text-{v.volumeSlug}" />{#if headVols.some((h) => h.volumeSlug === v.volumeSlug && JSON.stringify(h.seed ?? null) !== JSON.stringify(v.seed ?? null))}<div class="tiny warn-text" title="applies only to new / empty volumes">seed changed — new / empty volumes only</div>{/if}</td>
                  <td class="small loc" data-testid="vloc-{v.volumeSlug}"><span class="mono"><PathText path={locationLabel(v.location, 'volumes', v.volumeSlug, agentHomes.of(slug))} /></span></td>
                  <td><label class="switch small"><input type="checkbox" checked={v.backup} onchange={(e) => (s.config.volumes[i] = { ...v, backup: e.currentTarget.checked })} aria-label="Back up {v.volumeSlug}" data-testid="backup-{v.volumeSlug}" />{v.backup ? 'on' : 'off'}</label></td>
                  <td>
                    <div class="chips">
                      {#each us as u, k (k)}
                        <a class="chip" href="{base}/app/{slug}/config/containers/{u.containerSlug}" title="{u.imageVolume ? `image volume ${u.imageVolume}` : 'extra mount'}{u.readOnly ? ' · read-only' : ''}" data-testid="used-{v.volumeSlug}-{u.containerSlug}"><Icon name="box" size={11} /><span class="mono">{u.containerSlug}</span>{#if u.readOnly}<span class="tiny muted">ro</span>{/if}{#if !u.enabled}<span class="tiny muted">off</span>{/if}</a>
                      {:else}<span class="badge warn" title="V_VOLUME_UNUSED: declared but no container mounts it">unused</span>{/each}
                    </div>
                  </td>
                  <td class="actions">
                    <Btn size="xs" variant="ghost" icon="edit" onclick={() => ((editing = { ...v }), (modalOpen = true))} data-testid="edit-{v.volumeSlug}">Edit</Btn>
                    <Btn size="xs" variant="ghost" icon="trash" aria-label="Delete volume {v.volumeSlug}" onclick={() => askDelete(v)} data-testid="delete-{v.volumeSlug}" />
                  </td>
                </tr>
                {#if draft.issuesAt(`${pfx}.volumes[${v.volumeSlug}]`).filter((x) => x.code !== 'V_VOLUME_UNUSED').length}
                  <tr class="issue-row"><td colspan="7"><FieldIssues issues={draft.issuesAt(`${pfx}.volumes[${v.volumeSlug}]`).filter((x) => x.code !== 'V_VOLUME_UNUSED')} /></td></tr>
                {/if}
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
      {#if removed.length}
        <div class="card-body small muted" data-testid="removed-volumes">
          Removed locally (folders stay on disk): {#each removed as r (r.volumeSlug)}<span class="mono">{r.volumeSlug}</span> <Btn size="xs" variant="ghost" onclick={() => undo(s, r)}>Undo</Btn> {/each}
        </div>
      {/if}
    </section>
    <div style="margin-top:12px"><Issues issues={draft.issues.filter((x) => x.path.startsWith(`${pfx}.volumes`) || x.code === 'V_VOLUME_UNDECLARED' && x.path.startsWith(`${pfx}.`))} title="Volume validation" /></div>
  {/snippet}
</SiteConfigFrame>

<VolumeModal bind:open={modalOpen} {editing} {taken} siteSlug={slug} agentHome={agentHomes.map[slug]} onsave={save} />

<Modal bind:open={blockedOpen} title="Volume {blockedFor} is in use" size="sm" testid="delete-blocked">
  <p class="small" style="margin-top:0">Remove it from these containers first (pick another source on their Volumes section):</p>
  <ul class="used">
    {#each users[blockedFor ?? ''] ?? [] as u, k (k)}
      <li><a href="{base}/app/{slug}/config/containers/{u.containerSlug}" class="mono" onclick={() => (blockedOpen = false)}>{u.containerSlug}</a> <span class="muted small">{u.imageVolume ? `image volume ${u.imageVolume}` : 'extra mount'}{u.mountPath ? ` · ${u.mountPath}` : ''}</span></li>
    {/each}
  </ul>
  {#snippet footer()}<Btn variant="primary" onclick={() => (blockedOpen = false)}>OK</Btn>{/snippet}
</Modal>

<Confirm bind:open={delOpen} title="Remove volume {delFor}?" danger confirmLabel="Remove" message="No container mounts it. The declaration is removed when you commit; its folder stays on the site server (shown as orphan on Backups) and is no longer backed up." onconfirm={doDelete} testid="delete-confirm" />

<style>
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 2px 8px;
    border: 1px solid var(--border-strong);
    border-radius: 99px;
    font-size: 12px;
    text-decoration: none;
    color: var(--text);
    background: var(--surface);
  }
  .chip:hover {
    border-color: var(--accent);
    color: var(--accent-text);
  }
  .loc {
    max-width: 240px;
    overflow-wrap: anywhere;
  }
  .vol {
    min-width: 180px;
  }
  .vtop {
    display: flex;
    align-items: center;
    gap: 6px;
    white-space: nowrap;
    font-size: var(--fs-sm);
  }
  .info-line {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .info-line span {
    overflow: hidden;
    text-overflow: ellipsis;
  }
  tr.issue-row td {
    padding-top: 0;
    padding-left: 24px;
    background: var(--surface-2);
  }
  .used {
    margin: 0;
    padding-left: 18px;
    display: grid;
    gap: 4px;
  }
  tr.changed td {
    background: color-mix(in srgb, var(--accent-soft) 45%, transparent);
  }
</style>
