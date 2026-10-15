<script lang="ts">
  /**
   * §21 #101 the content page of one site volume (both scopes): `…/sites/{site}/volumes/content?volume=<slug>&tab=`.
   * Tabs: Starter content (the volume's own file tree / single file; a legacy File seed = error card + "Convert to own
   * content"; an archive seed = info card) · Persist to cloud (folder volumes) · Cloud copy (deployment scope, when persist
   * is on in the config or versions exist). §21 #107: there is no Apply — the workspace is LIVE: every tree op and
   * (debounced) every text edit is written into the site's volume seed in the scope's browser change set at once, the
   * bytes kept as pending (memory + IndexedDB); the ChangeBar counts the files and Commit… uploads + commits in one go.
   */
  import { page } from '$app/state';
  import { beforeNavigate, goto } from '$app/navigation';
  import { onDestroy, onMount, untrack } from 'svelte';
  import type { SiteVolume, ValidationIssue, VolumeSeed, VolumeTree } from '@opm/shared';
  import { isLegacyFileSeed, normalizeVolumeTree, seedLabel, formatContentBytes } from '@opm/shared';
  import Button from '../../ui/Button.svelte';
  import PageHeader from '../../ui/PageHeader.svelte';
  import EmptyState from '../../ui/EmptyState.svelte';
  import Tabs from '../../ui/Tabs.svelte';
  import Modal from '../../ui/Modal.svelte';
  import IssuesList from '../../ui/IssuesList.svelte';
  import Icon from '../../ui/Icon.svelte';
  import { HardDrive, ArrowLeft, CircleAlert, Wand2, Info, Package, TriangleAlert } from '../../ui/icons';
  import DraftBanner from '../DraftBanner.svelte';
  import { useLeaveGuard } from '../../util/guard.svelte';
  import { issuesUnder, sevClass } from '../../util/issues';
  import { toast } from '../../toast.svelte';
  import { api } from '../../api';
  import type { AnyScope } from '../scope.svelte';
  import VolumeContentWorkspace from './VolumeContentWorkspace.svelte';
  import PersistForm from '$opm/forms/volumes/PersistForm.svelte';
  import CloudCopy from './CloudCopy.svelte';
  import { ContentWorkspace } from './workspace.svelte';
  import { SINGLE_PATH, convertSiteSeeds, seedTree } from './content';

  let { scope, siteSlug }: { scope: AnyScope; siteSlug: string } = $props();
  const volumeSlug = $derived(page.url.searchParams.get('volume') ?? '');
  $effect(() => {
    if (scope.loaded) scope.openSite(siteSlug);
  });
  onMount(() => {
    if (scope.kind === 'deployment') scope.loadStatus();
  });
  const site = $derived(scope.siteDrafts[siteSlug]);
  const vol = $derived<SiteVolume | null>(site?.config.volumes.find((v) => v.volumeSlug === volumeSlug) ?? null);
  const single = $derived(vol?.kind === 'file');
  const prefix = $derived(`${scope.base}/sites/${siteSlug}`);
  const ro = $derived(!scope.canWrite);

  /** the volume in the BASE head (dirty markers = not committed) */
  const headVol = $derived<SiteVolume | null>(scope.headSite(siteSlug)?.config.volumes.find((v) => v.volumeSlug === volumeSlug) ?? null);
  // starter content workspace — LIVE: edits go straight into the working site's volume seed (the change set)
  const ws = new ContentWorkspace(true, {
    keep: (sha, b) => scope.keepBytes(sha, b),
    get: (sha) => scope.bytesOf(sha),
    apply: (tree) => {
      const v = scope.siteDrafts[siteSlug]?.config.volumes.find((x) => x.volumeSlug === volumeSlug);
      if (v) v.seed = toSeed(tree, v.kind === 'file');
    },
  });
  let inited = $state<string | null>(null);
  $effect(() => {
    if (!vol || inited === `${siteSlug}/${volumeSlug}`) return;
    const t = seedTree(vol.seed, vol.kind === 'file');
    untrack(() => {
      ws.reset(seedTree(headVol?.seed, vol.kind === 'file') ?? { entries: [], dirs: [] });
      ws.follow(ws.base, t ?? { entries: [], dirs: [] });
      if (vol.kind === 'file' && t?.entries.length) ws.openFile(SINGLE_PATH);
    });
    inited = `${siteSlug}/${volumeSlug}`;
  });
  // Discard of the change set: drop text edits still waiting for their debounce (else they would re-apply)
  let seenDiscards = untrack(() => scope.discards);
  $effect(() => {
    const d = scope.discards;
    if (d === seenDiscards) return;
    seenDiscards = d;
    untrack(() => {
      ws.abandon();
      const t = vol ? seedTree(vol.seed, vol.kind === 'file') : null;
      const b = vol ? (seedTree(headVol?.seed, vol.kind === 'file') ?? { entries: [], dirs: [] }) : null;
      if (t && b) ws.follow(b, t);
    });
  });
  // follow changes of the seed made elsewhere (Discard, restore, rebase, commit, convert, Cloud copy → starter)
  $effect(() => {
    if (!vol || inited !== `${siteSlug}/${volumeSlug}`) return;
    const t = seedTree(vol.seed, vol.kind === 'file');
    const b = seedTree(headVol?.seed, vol.kind === 'file') ?? { entries: [], dirs: [] };
    if (t) untrack(() => ws.follow(b, t));
  });
  // a text edit is applied ~300 ms after the last keystroke; leaving / blur applies it at once
  beforeNavigate(() => void ws.flush());
  onDestroy(() => void ws.flush());
  const seedKind = $derived(vol?.seed?.kind ?? null);
  const legacy = $derived(!!vol && isLegacyFileSeed(vol.seed));
  const archive = $derived(vol?.seed?.kind === 'archive' ? vol.seed : null);
  const editable = $derived(!legacy && !archive);

  let cloudDirty = $state(false);
  /** files of this volume not committed (the ChangeBar counts them) */
  const changed = $derived(ws.changedPaths().size ? ws.tree.entries.filter((e) => ws.changedPaths().has(e.path)).length + ws.base.entries.filter((e) => !ws.entry(e.path)).length : 0);
  useLeaveGuard(() => cloudDirty);

  // tabs (?tab=content|persist|cloud)
  const showCloud = $derived(scope.kind === 'deployment' && !!vol && vol.kind !== 'file' && (!!vol.persist || !!scope.serverSite(siteSlug)?.config.volumes.find((v) => v.volumeSlug === volumeSlug)?.persist));
  const tabs = $derived([
    { id: 'content', label: single ? 'Content' : 'Starter content', badge: (legacy ? 'red' : null) as 'red' | null, count: legacy ? '!' : null },
    ...(vol && !single ? [{ id: 'persist', label: 'Persist to cloud', count: vol.persist ? 'on' : null }] : []),
    ...(showCloud ? [{ id: 'cloud', label: 'Cloud copy' }] : []),
  ]);
  let tab = $state(page.url.searchParams.get('tab') ?? 'content');
  $effect(() => {
    if (vol && !tabs.some((t) => t.id === tab)) tab = 'content';
  });
  function selectTab(id: string) {
    const u = new URL(page.url);
    u.searchParams.set('tab', id);
    void goto(u.pathname + u.search, { replaceState: true, keepFocus: true, noScroll: true });
  }

  // validation of this volume (+ container mounts that name it)
  const all = $derived(site ? scope.validate().list : []);
  const vp = $derived(`sites[${siteSlug}].config.volumes[${volumeSlug}]`);
  const volIssues = $derived<ValidationIssue[]>([...issuesUnder(all, vp), ...issuesUnder(scope.saveIssues, vp).filter((s) => !all.some((a) => a.path === s.path && a.code === s.code))]);
  const persistIssues = $derived(volIssues.filter((i) => i.path.startsWith(`${vp}.persist`)));
  const nested = $derived(all.filter((i) => i.code === 'V_FILE_IN_WRITABLE_VOLUME' && i.path.startsWith(`sites[${siteSlug}]`) && i.message.includes(`volume ${volumeSlug} `)));
  const blocking = $derived(volIssues.filter((i) => i.severity === 'error').length + ws.issues.length);

  function toSeed(tree: VolumeTree, isFile: boolean): VolumeSeed | null {
    if (isFile) {
      const e = tree.entries.find((x) => x.path === SINGLE_PATH);
      return e ? { kind: 'single', sha256: e.sha256, size: e.size, mode: e.mode, render: e.render } : null;
    }
    return tree.entries.length || tree.dirs.length ? { kind: 'tree', ...normalizeVolumeTree(tree) } : null;
  }
  // ───── the Persist card previews its excludes on the latest cloud version (deployment scope), else on the starter content
  let cloudLatest = $state<VolumeTree | null>(null);
  $effect(() => {
    if (scope.kind !== 'deployment' || !showCloud || tab !== 'persist') return;
    const key = { deploymentId: scope.id, siteSlug, volumeSlug };
    void (async () => {
      try {
        const st = await api.getVolumeContent(key);
        cloudLatest = st.latest ? normalizeVolumeTree((await api.getVolumeContentVersion({ ...key, versionId: st.latest.versionId })).tree) : null;
      } catch {
        cloudLatest = null; // optional: fall back to the starter content
      }
    })();
  });

  // ───── legacy convert
  let conv = $state<{ lines: string[]; notes: string[]; unused: string[] } | null>(null);
  let convOpen = $state(false);
  function convert() {
    if (!site) return;
    const r = convertSiteSeeds(scope.config.files, $state.snapshot(site) as typeof site);
    site.config.volumes = r.config.volumes;
    site.config.containers = r.config.containers;
    // the workspace follows the new seed (effect above)
    // the site save writes only the site: Files that nothing uses any more stay under Files (removed there by hand / migrate-h)
    conv = { lines: r.lines.filter((l) => !/^file \S+ (removed|kept):/.test(l)), notes: r.notes, unused: r.removedFiles };
    convOpen = true;
  }
  function dropArchive() {
    if (!vol) return;
    vol.seed = null;
  }

  // ───── cloud → starter content
  async function useAsStarter(tree: VolumeTree, seq: number): Promise<boolean> {
    if (!site) return false;
    const v = site.config.volumes.find((x) => x.volumeSlug === volumeSlug);
    if (!v) return false;
    const t = normalizeVolumeTree({ entries: tree.entries.map((e) => ({ ...e, render: 'none' as const })), dirs: tree.dirs });
    v.seed = toSeed(t, false);
    const ok = await scope.saveSite(siteSlug);
    if (ok) {
      toast.success(`Starter content = version #${seq}`);
      tab = 'content';
      selectTab('content');
    }
    return ok;
  }
  const seedSummary = $derived(vol ? (seedKind === 'tree' || seedKind === 'single' ? `${seedLabel(vol.seed)} · ${formatContentBytes(ws.totalBytes)}` : seedLabel(vol.seed)) : '');
</script>

<svelte:head><title>Volume {volumeSlug} · site {siteSlug} · {scope.label} · OPM</title></svelte:head>

{#if !scope.loaded}
  <div class="stack"><span class="skel" style="height:60px"></span><span class="skel" style="height:420px"></span></div>
{:else if !site || !vol}
  <EmptyState icon={HardDrive} title={site ? `Volume ${volumeSlug || '(none)'} does not exist on site ${siteSlug}` : `Site ${siteSlug} does not exist`} text="It may have been removed.">
    {#snippet action()}<Button href="{prefix}/volumes" icon={ArrowLeft}>Back to volumes</Button>{/snippet}
  </EmptyState>
{:else}
  {#if scope.kind === 'deployment'}<DraftBanner deploymentId={scope.id} />{/if}
  <div class="sticky" data-testid="volume-content-page" data-volume={volumeSlug}>
    <PageHeader title="Volume {volumeSlug}" icon={HardDrive} readOnly={ro} back={{ href: `${prefix}/volumes`, label: 'Volumes' }}>
      {#snippet meta()}
        <span class="badge {single ? 'violet' : 'gray'}">{single ? 'single file' : 'folder'}</span>
        {#if vol.persist}<span class="badge blue" data-testid="vc-persist-badge">persist to cloud</span>{/if}
        {#if blocking}<span class="badge red"><Icon icon={CircleAlert} size={11} />{blocking} error{blocking === 1 ? '' : 's'}</span>{/if}
        {#if changed}<span class="badge amber" data-tip="Changed in this browser — not committed (Commit… in the change bar)" data-testid="vc-changed">{changed} file{changed === 1 ? '' : 's'} not committed</span>{/if}
      {/snippet}
      <div class="small muted sub">Site <span class="mono strong">{siteSlug}</span> · {vol.label || volumeSlug} · starts with <span data-testid="vc-seed-summary">{seedSummary}</span></div>
    </PageHeader>
    <Tabs {tabs} bind:active={tab} onselect={selectTab} testid="vc-tabs" />
  </div>

  <div class="body stack">
    {#if tab === 'content'}
      {#if legacy}
        <div class="card card-pad legacy" data-testid="vc-legacy">
          <div class="row" style="gap:8px"><Icon icon={CircleAlert} size={18} class="text-red" /><h3>This volume takes its content from Files</h3></div>
          <p class="small">Starter content <b>{seedLabel(vol.seed)}</b> points at Files ({vol.seed?.kind === 'file' ? vol.seed.fileSlug : vol.seed?.kind === 'files' ? vol.seed.entries.map((e) => `${e.path} ← ${e.fileSlug}`).join(', ') : ''}). Files are static and read-only and are never volume content: give the volume its own content. <b>Convert to own content</b> copies each file's content into the volume (same bytes, same render mode) and also moves Files mounted inside a writable volume of this site into that volume; review, then Save. (backend/scripts/migrate-h.ts does the same for every template and draft.)</p>
          {#if scope.canWrite}<div><Button variant="primary" icon={Wand2} onclick={convert} data-testid="vc-convert">Convert to own content</Button></div>{/if}
        </div>
      {:else if archive}
        <div class="card card-pad" data-testid="vc-archive">
          <div class="row" style="gap:8px"><Icon icon={Package} size={18} /><h3>Starts from the archive {archive.name}</h3></div>
          <p class="small muted">{formatContentBytes(archive.size)} .tar.gz, unpacked as is into a new / empty volume. To edit the files here instead, start own content (the archive is dropped from the starter content).</p>
          {#if scope.canWrite}<div><Button onclick={dropArchive} data-testid="vc-drop-archive">Start own content instead</Button></div>{/if}
        </div>
      {:else}
        <div class="banner info small" data-testid="vc-seed-note"><Icon icon={Info} size={15} class="icon" /><div>{single ? 'The file' : 'These files'} are written once into a <b>new or empty</b> volume on a site (before its containers start) — existing data is never touched. Files with <b>render {'{{ }}'}</b> on are rendered once per site (like Files).{#if vol.persist}{' '}For a persist-to-cloud volume, change what the site runs with <b>Cloud copy → Send to site</b>.{/if}</div></div>
        <div class="live" onfocusout={() => void ws.flush()}>
          <VolumeContentWorkspace {ws} readOnly={ro} {single} allowRender name={volumeSlug} testid="seed-workspace" />
        </div>
        {#if ws.flushError}<div class="xs text-red" data-testid="vc-flush-error">{ws.flushError}</div>{/if}
      {/if}
      {#each nested as x, k (k)}<div class="xs {sevClass(x.severity)}" data-testid="vc-nested-issue">{x.message}</div>{/each}
      <IssuesList issues={volIssues} title="Validation of volume {volumeSlug}" collapsible emptyText="No problems." testid="vc-volume-issues" />
    {:else if tab === 'persist'}
      <PersistForm volume={vol} previewTree={cloudLatest ?? ws.tree} previewLabel={cloudLatest ? 'the latest cloud version' : 'the starter content'} readOnly={ro} issues={persistIssues} />
      <div class="xs faint">Needs agent 0.12.0 or newer on the site. Takes effect when the revision is published and applied.</div>
      {#if scope.kind === 'template' && vol.persist}<div class="xs faint row" style="gap:5px"><Icon icon={TriangleAlert} size={12} /> In a template this is the default of new deployments; the Cloud copy (versions, Send to site) lives in each deployment.</div>{/if}
    {:else if tab === 'cloud' && scope.kind === 'deployment'}
      <CloudCopy {scope} {site} {volumeSlug} bind:dirty={cloudDirty} onstarter={useAsStarter} />
    {/if}
  </div>
{/if}

<Modal bind:open={convOpen} title="Converted to own content" subtitle="Applied to the change set in this browser — review, then Commit…. Nothing is committed yet." size="lg" testid="vc-convert-result">
  <div class="stack">
    {#if conv?.lines.length}<ul class="small lines" data-testid="vc-convert-lines">{#each conv.lines as l, k (k)}<li>{l}</li>{/each}</ul>{/if}
    {#if conv?.notes.length}<div class="banner warn small"><Icon icon={TriangleAlert} size={15} class="icon" /><ul class="lines">{#each conv.notes as l, k (k)}<li>{l}</li>{/each}</ul></div>{/if}
    {#if conv?.unused.length}<p class="xs faint" data-testid="vc-convert-unused">Not used any more: {conv.unused.join(', ')} — they stay under Files (delete them there if you want).</p>{/if}
  </div>
  {#snippet footer()}<Button variant="primary" onclick={() => (convOpen = false)} data-testid="vc-convert-ok">OK</Button>{/snippet}
</Modal>

<style>
  .sticky {
    position: sticky;
    top: var(--sticky-top, 0px);
    z-index: var(--z-sticky);
    background: var(--bg);
    padding-top: 8px;
    margin: -8px 0 var(--sp-4);
  }
  .sticky :global(.ph) {
    margin-bottom: 10px;
  }
  .sub {
    margin-top: 4px;
  }
  .live {
    display: contents;
  }
  .legacy {
    border-color: color-mix(in srgb, var(--red) 45%, var(--border));
    background: color-mix(in srgb, var(--red) 4%, var(--surface));
  }
  .legacy h3,
  .card h3 {
    margin: 0;
  }
  .lines {
    margin: 0;
    padding-left: 18px;
    display: flex;
    flex-direction: column;
    gap: 4px;
    overflow-wrap: anywhere;
  }
  @media (max-width: 720px) {
    .sticky {
      position: static;
    }
  }
</style>
