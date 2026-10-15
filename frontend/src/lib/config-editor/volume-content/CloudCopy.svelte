<script lang="ts">
  /**
   * §21 #101 "Cloud copy" of a persist-to-cloud volume (deployment scope, VOLUME_CONTENT_CLOUD_API): sync state badge
   * (cloudSyncState / VOLUME_SYNC_STATE_LABEL), the LATEST version in the same workspace (Save as new version — 409 →
   * "a newer version arrived" with Reload / Compare), versions history (origin, trigger, who, when, counts, diff vs the
   * previous version with text diffs), Restore, Send to site… (version, restart, note; agent < 0.12 warning), Cancel
   * pending push, Snapshot now, Use as starter content. Polls while a snapshot request or a push is open.
   * §21 #107: Save uploads the new bytes into a stage first, then saves the version with the stageId (one transaction).
   */
  import { onDestroy, onMount } from 'svelte';
  import type { Site, VolumeContentState, VolumeContentVersion, VolumeSyncState, VolumeTree } from '@opm/shared';
  import { VOLUME_CONTENT_LIMITS, VOLUME_CONTENT_MIN_AGENT, VOLUME_SYNC_STATE_LABEL, cloudSyncState, formatContentBytes, normalizeVolumeTree, volumeUsers } from '@opm/shared';
  import { api, errMsg, isApiError } from '../../api';
  import { toast } from '../../toast.svelte';
  import { personName } from '../../util/people';
  import Button from '../../ui/Button.svelte';
  import Modal from '../../ui/Modal.svelte';
  import Field from '../../ui/Field.svelte';
  import Select from '../../ui/Select.svelte';
  import ConfirmDialog from '../../ui/ConfirmDialog.svelte';
  import RelTime from '../../ui/RelTime.svelte';
  import Tabs from '../../ui/Tabs.svelte';
  import ErrorState from '../../ui/ErrorState.svelte';
  import EmptyState from '../../ui/EmptyState.svelte';
  import CardSkeleton from '../../ui/CardSkeleton.svelte';
  import Icon from '../../ui/Icon.svelte';
  import { Camera, Send, X, RotateCcw, Save, TriangleAlert, Info, RefreshCw, GitCompare, History, Cloud, Download, Undo2 } from '../../ui/icons';
  import VolumeContentWorkspace from './VolumeContentWorkspace.svelte';
  import TreeDiffList from './TreeDiffList.svelte';
  import { ContentWorkspace } from './workspace.svelte';
  import { agentSupportsVolumeContent, excludedIn } from './content';
  import { stageTransport } from '../files';
  import { commitWithStage } from '$opm/branch/stage-commit';
  import type { DeploymentScope } from '../scope.svelte';

  type Props = {
    scope: DeploymentScope;
    site: Site;
    volumeSlug: string;
    /** dirty state of the latest-version workspace (the page's leave guard) */
    dirty?: boolean;
    /** copy a version's tree into the draft's starter content (the page saves the site) */
    onstarter: (tree: VolumeTree, seq: number) => Promise<boolean>;
  };
  let { scope, site, volumeSlug, dirty = $bindable(false), onstarter }: Props = $props();
  const key = $derived({ deploymentId: scope.id, siteSlug: site.siteSlug, volumeSlug });
  const canWrite = $derived(scope.can('deployment.config', 'write'));
  const canPublish = $derived(scope.can('deployment.config', 'publish'));

  let cs = $state<VolumeContentState | null>(null);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let versions = $state<VolumeContentVersion[]>([]);
  const ws = new ContentWorkspace(false);
  let latestTree = $state<VolumeTree | null>(null);
  let latestId = $state<string | null>(null);
  $effect(() => {
    dirty = ws.dirty;
  });
  const sync = $derived<VolumeSyncState | null>(cs ? cloudSyncState(cs) : null);
  const TONE: Record<VolumeSyncState, string> = { 'in-sync': 'green', 'live-changed': 'amber', 'cloud-ahead': 'blue', conflict: 'red', 'never-snapshotted': 'gray', 'pending-upload': 'violet', error: 'red' };
  const HELP: Record<VolumeSyncState, string> = {
    'in-sync': 'The site has exactly the latest snapshot.',
    'live-changed': 'The folder changed on the site since its last snapshot (an automatic snapshot will upload it).',
    'cloud-ahead': 'A version was sent and the site has not changed since: applying it is a clean fast-forward.',
    conflict: 'A version was sent but the site changed too: the person applying it chooses overwrite / keep live / per file.',
    'never-snapshotted': 'Persist is on but the site has not uploaded a snapshot yet (agent 0.12.0+, after the revision is applied).',
    'pending-upload': 'The site has snapshots waiting to upload (store-and-forward).',
    error: 'The site reported an error with this volume.',
  };

  async function loadState() {
    cs = await api.getVolumeContent(key);
  }
  async function loadVersions() {
    versions = (await api.listVolumeContentVersions({ ...key, query: { limit: 50 } })).items;
  }
  async function loadLatest(force = false) {
    const id = cs?.latest?.versionId ?? null;
    if (!force && id === latestId && latestTree) return;
    latestId = id;
    if (!id) {
      latestTree = { entries: [], dirs: [] };
    } else {
      const r = await api.getVolumeContentVersion({ ...key, versionId: id });
      latestTree = normalizeVolumeTree(r.tree);
    }
    ws.reset(latestTree);
  }
  async function loadAll(first = false) {
    if (first) loading = true;
    try {
      await loadState();
      await Promise.all([loadVersions(), first || !ws.dirty ? loadLatest(first) : Promise.resolve()]);
      error = null;
    } catch (e) {
      error = errMsg(e);
    } finally {
      loading = false;
    }
  }
  let timer: ReturnType<typeof setInterval> | null = null;
  onMount(() => {
    void loadAll(true);
    scope.loadStatus();
    timer = setInterval(() => {
      if (document.hidden || !cs) return;
      if (cs.snapshotRequest || cs.push?.status === 'pending') void loadAll();
    }, 3000);
  });
  onDestroy(() => timer && clearInterval(timer));

  // ───── tabs
  let tab = $state('latest');
  const tabs = $derived([
    { id: 'latest', label: cs?.latest ? `Latest version (#${cs.latest.seq})` : 'Latest version' },
    { id: 'history', label: 'History', count: cs?.versionCount ?? versions.length },
  ]);

  // ───── excludes (a version must not hold excluded paths)
  const patterns = $derived(cs?.persist?.exclude ?? site.config.volumes.find((v) => v.volumeSlug === volumeSlug)?.persist?.exclude ?? []);
  const excluded = $derived.by(() => {
    const x = excludedIn(ws.tree, patterns);
    return new Set([...x.files, ...x.dirs]);
  });
  const excludedFiles = $derived(ws.tree.entries.filter((e) => excluded.has(e.path) || [...excluded].some((d) => e.path.startsWith(d + '/'))).map((e) => e.path));

  // ───── save as new version
  let note = $state('');
  let saving = $state(false);
  let progress = $state<string | null>(null);
  let newer = $state<{ seq: number; createdBy: string; versionId: string } | null>(null);
  const canSave = $derived(canWrite && ws.dirty && !ws.issues.length && !excludedFiles.length && !saving && note.length <= VOLUME_CONTENT_LIMITS.noteMax);
  async function saveVersion() {
    saving = true;
    newer = null;
    try {
      // §21 #107 one transaction: the new bytes go to a stage, the save names it (verified + promoted, then written)
      const { tree, local } = await ws.prepare();
      const needs = tree.entries.filter((e, i, a) => local.has(e.sha256) && a.findIndex((x) => x.sha256 === e.sha256) === i).map((e) => ({ sha256: e.sha256, sizeBytes: e.size }));
      const r = await commitWithStage({
        needs,
        bytesOf: (sha) => local.get(sha),
        transport: stageTransport(),
        commit: (stageId) => api.createVolumeContentVersion({ ...key, body: { baseVersionId: latestId, tree, note: note.trim(), ...(stageId ? { stageId } : {}) } }),
        onProgress: (_p, t) => (progress = t),
      });
      ws.saved();
      toast.success(`Version ${r.version.seq} saved`, 'It reaches the site only with Send to site.');
      note = '';
      latestId = r.version.versionId;
      latestTree = normalizeVolumeTree(r.tree);
      ws.reset(latestTree);
      await loadState();
      await loadVersions();
    } catch (e) {
      if (isApiError(e) && e.code === 'conflict') {
        const l = (e.details as { latest?: { seq: number; createdBy: string; versionId: string } }).latest;
        newer = l ?? { seq: 0, createdBy: '', versionId: '' };
        await loadState().catch(() => undefined);
      } else toast.error(e, 'Not saved');
    } finally {
      saving = false;
      progress = null;
    }
  }
  async function reloadLatest() {
    newer = null;
    await loadState();
    await loadVersions();
    await loadLatest(true);
  }
  let cmpOpen = $state(false);
  let cmpTree = $state<VolumeTree | null>(null);
  async function compare() {
    const id = newer?.versionId || cs?.latest?.versionId;
    if (!id) return;
    try {
      cmpTree = normalizeVolumeTree((await api.getVolumeContentVersion({ ...key, versionId: id })).tree);
      cmpOpen = true;
    } catch (e) {
      toast.error(e);
    }
  }
  const editedTexts = $derived(Object.fromEntries(Object.entries(ws.texts).filter(([p]) => ws.textChanged(p))));

  // ───── snapshot / cancel
  let busy = $state<string | null>(null);
  async function snapshotNow() {
    busy = 'snapshot';
    try {
      cs = await api.requestVolumeSnapshot(key);
      toast.success('Snapshot requested', 'The site uploads it the next time it talks to the cloud.');
    } catch (e) {
      toast.error(e, 'Not requested');
    } finally {
      busy = null;
    }
  }
  let cancelOpen = $state(false);
  async function cancelPush() {
    cs = await api.cancelVolumeContentPush(key);
    toast.success('Pending push cancelled');
    await loadVersions();
  }

  // ───── send
  const users = $derived((volumeUsers(site.config)[volumeSlug] ?? []).filter((u) => u.enabled).map((u) => u.containerSlug));
  const agentVersion = $derived(scope.status?.sites.find((s) => s.siteSlug === site.siteSlug)?.agentVersion ?? null);
  const oldAgent = $derived(!!agentVersion && !agentSupportsVolumeContent(agentVersion));
  let sendOpen = $state(false);
  let sendVersion = $state('');
  let sendRestart = $state(true);
  let sendNote = $state('');
  let sending = $state(false);
  const versionOpts = $derived(versions.map((v) => ({ value: v.versionId, label: `#${v.seq} · ${v.origin}${v.trigger ? ` (${v.trigger})` : ''}`, sub: `${v.fileCount} files · ${formatContentBytes(v.totalBytes)} · ${personName(v.createdBy)}` })));
  function openSend(versionId?: string) {
    sendVersion = versionId ?? cs?.latest?.versionId ?? versions[0]?.versionId ?? '';
    sendRestart = true;
    sendNote = '';
    sendOpen = true;
  }
  async function send(e: Event) {
    e.preventDefault();
    if (!sendVersion) return;
    sending = true;
    try {
      const p = await api.sendVolumeContent({ ...key, body: { versionId: sendVersion, restart: sendRestart, note: sendNote.trim() } });
      toast.success(`Version #${pushSeq(p) ?? '?'} sent to ${site.siteSlug}`, 'A person on the site applies it in the agent GUI (Docker › Volumes).');
      sendOpen = false;
      if (cs) cs = { ...cs, push: p };
      await loadState();
    } catch (err) {
      toast.error(err, 'Not sent');
    } finally {
      sending = false;
    }
  }

  // ───── history: diff vs previous, restore, use as starter
  let diffOf = $state<string | null>(null);
  let diffTrees = $state<{ a: VolumeTree | null; b: VolumeTree; aSeq: number | null; bSeq: number } | null>(null);
  async function showDiff(v: VolumeContentVersion) {
    if (diffOf === v.versionId) return void (diffOf = null);
    diffOf = v.versionId;
    diffTrees = null;
    try {
      const prev = versions.filter((x) => x.seq < v.seq).sort((x, y) => y.seq - x.seq)[0] ?? null;
      const [b, a] = await Promise.all([api.getVolumeContentVersion({ ...key, versionId: v.versionId }), prev ? api.getVolumeContentVersion({ ...key, versionId: prev.versionId }) : Promise.resolve(null)]);
      diffTrees = { a: a ? normalizeVolumeTree(a.tree) : null, b: normalizeVolumeTree(b.tree), aSeq: prev?.seq ?? null, bSeq: v.seq };
    } catch (e) {
      toast.error(e);
      diffOf = null;
    }
  }
  let restoreOf = $state<VolumeContentVersion | null>(null);
  let restoreOpen = $state(false);
  let restoreNote = $state('');
  async function restore() {
    const v = restoreOf!;
    const r = await api.restoreVolumeContentVersion({ ...key, versionId: v.versionId, body: { note: restoreNote.trim() } });
    toast.success(`Version ${v.seq} restored as version ${r.version.seq}`, 'It reaches the site only with Send to site.');
    await reloadLatest();
  }
  let starterOf = $state<VolumeContentVersion | null>(null);
  let starterOpen = $state(false);
  async function useAsStarter() {
    const v = starterOf!;
    const r = await api.getVolumeContentVersion({ ...key, versionId: v.versionId });
    await onstarter(normalizeVolumeTree(r.tree), v.seq);
  }
  const ORIGIN_TONE = { snapshot: 'blue', edit: 'violet', restore: 'amber' } as const;
  const TRIGGER: Record<string, string> = { 'manual-cloud': 'requested here', 'manual-agent': 'Snapshot now on the site', auto: 'automatic', 'before-apply': 'before an apply', 'after-apply': 'after an apply' };
  const pending = $derived(cs?.push?.status === 'pending' ? cs.push : null);
  const seqOf = (id: string | null | undefined) => versions.find((v) => v.versionId === id)?.seq ?? null;
  /** the pushed version's seq: VolumeContentPush.versionSeq when the cloud sends it, else looked up in the loaded history */
  const pushSeq = (p: { versionId: string; versionSeq?: number } | null | undefined) => (p ? (p.versionSeq ?? seqOf(p.versionId)) : null);
</script>

{#if loading && !cs}
  <CardSkeleton />
{:else if error && !cs}
  <ErrorState message={error} onretry={() => loadAll(true)} />
{:else if cs}
  <div class="stack" data-testid="cloud-copy">
    <div class="card card-pad head" data-testid="cloud-state">
      <div class="st">
        <span class="badge {TONE[sync!]}" data-testid="cloud-sync-badge" data-state={sync}>{VOLUME_SYNC_STATE_LABEL[sync!]}</span>
        <span class="small muted">{HELP[sync!]}</span>
      </div>
      <div class="facts small">
        <div><span class="lab">Latest</span>{#if cs.latest}<b>#{cs.latest.seq}</b> {cs.latest.origin} · {personName(cs.latest.createdBy)} · <RelTime ts={cs.latest.createdAt} />{:else}<span class="faint">none yet</span>{/if}</div>
        <div><span class="lab">Last snapshot</span>{#if cs.lastSnapshot}<b>#{cs.lastSnapshot.seq}</b> · <RelTime ts={cs.lastSnapshot.createdAt} />{:else}<span class="faint">none</span>{/if}</div>
        <div><span class="lab">Site reported</span>{#if cs.live}<RelTime ts={cs.live.reportedAt} />{#if cs.live.queued}{` · ${cs.live.queued} queued`}{/if}{#if cs.live.lastError}{' · '}<span class="text-red" data-testid="cloud-last-error">{cs.live.lastError}</span>{/if}{:else}<span class="faint">never</span>{/if}</div>
        {#if !cs.persistInHead}<div class="text-amber xs" data-testid="cloud-not-in-head"><Icon icon={Info} size={12} /> Persist is not on in the revision the site runs yet — publish the draft first.</div>{/if}
      </div>
      <div class="acts">
        {#if canWrite}<Button size="sm" icon={Camera} loading={busy === 'snapshot'} disabled={!!cs.snapshotRequest || !cs.persistInHead} tip={!cs.persistInHead ? 'Persist is not on in the head revision' : cs.snapshotRequest ? 'A snapshot is already requested' : undefined} onclick={snapshotNow} data-testid="cloud-snapshot">Snapshot now</Button>{/if}
        {#if canPublish}<Button size="sm" variant="primary" icon={Send} disabled={!versions.length} onclick={() => openSend()} data-testid="cloud-send">Send to site…</Button>{/if}
      </div>
    </div>

    {#if cs.snapshotRequest}
      <div class="banner info small" data-testid="cloud-snapshot-requested"><Icon icon={Camera} size={15} class="icon" /><div>Snapshot requested by {personName(cs.snapshotRequest.createdBy)} <RelTime ts={cs.snapshotRequest.createdAt} /> — waiting for the site (it answers the next time it talks to the cloud).</div></div>
    {/if}
    {#if pending}
      <div class="banner {sync === 'conflict' ? 'warn' : 'info'} small" data-testid="cloud-pending-push">
        <Icon icon={Send} size={15} class="icon" />
        <div class="grow">
          <div><b>Version #{pushSeq(pending) ?? '?'}</b> sent by {personName(pending.createdBy)} <RelTime ts={pending.createdAt} /> — waiting for a person on site {site.siteSlug} to apply it{pending.restart ? ' (restarts the containers that mount it)' : ''}.</div>
          {#if pending.note}<div class="xs muted">“{pending.note}”</div>{/if}
          {#if sync === 'conflict'}<div class="xs">The site changed the folder since version #{seqOf(pending.baseVersionId) ?? '?'}: the person chooses Overwrite / Keep live / Per file.</div>{/if}
        </div>
        {#if canPublish}<Button size="sm" icon={X} onclick={() => (cancelOpen = true)} data-testid="cloud-cancel-push">Cancel pending push</Button>{/if}
      </div>
    {:else if cs.push && cs.push.status !== 'pending'}
      <div class="xs muted" data-testid="cloud-last-push">Last push: version #{pushSeq(cs.push) ?? '?'} {cs.push.status}{cs.push.resolvedBy ? ` by ${personName(cs.push.resolvedBy)}` : ''}{#if cs.push.resolvedAt}{' '}<RelTime ts={cs.push.resolvedAt} />{/if}{cs.push.result?.error ? ` — ${cs.push.result.error}` : ''}</div>
    {/if}

    <Tabs {tabs} bind:active={tab} testid="cloud-tabs" />

    {#if tab === 'latest'}
      {#if newer}
        <div class="banner warn small" data-testid="cloud-newer">
          <Icon icon={TriangleAlert} size={15} class="icon" />
          <div class="grow">A newer version arrived{newer.seq ? ` (#${newer.seq}${newer.createdBy ? ` by ${personName(newer.createdBy)}` : ''})` : ''} while you edited — your edits are still here, not saved.</div>
          <Button size="sm" icon={GitCompare} onclick={compare} data-testid="cloud-compare">Compare</Button>
          <Button size="sm" icon={RefreshCw} onclick={reloadLatest} data-testid="cloud-reload">Reload (drop my edits)</Button>
        </div>
      {/if}
      {#if !cs.latest}
        <div class="banner info small"><Icon icon={Info} size={15} class="icon" /><div>No version yet. The site uploads its first snapshot after the revision with persist on is applied — or create the first version here.</div></div>
      {/if}
      {#if excludedFiles.length}
        <div class="banner error small" data-testid="cloud-excluded"><Icon icon={TriangleAlert} size={15} class="icon" /><div>{excludedFiles.length} file{excludedFiles.length === 1 ? '' : 's'} match the exclude patterns ({excludedFiles.slice(0, 3).join(', ')}{excludedFiles.length > 3 ? ' …' : ''}) — a version must not hold excluded paths. Remove or rename them.</div></div>
      {/if}
      <VolumeContentWorkspace {ws} readOnly={!canWrite} name={volumeSlug} {excluded} testid="cloud-workspace" />
      {#if canWrite}
        <div class="savebar">
          <input class="input" placeholder="Note (optional): what changed" bind:value={note} maxlength={VOLUME_CONTENT_LIMITS.noteMax} aria-label="Note of the new version" data-testid="cloud-note" />
          {#if ws.dirty}<Button icon={Undo2} onclick={() => latestTree && ws.reset(latestTree)} disabled={saving} data-testid="cloud-discard">Discard</Button>{/if}
          <Button variant="primary" icon={Save} loading={saving} disabled={!canSave} tip={!ws.dirty ? 'No changes' : ws.issues.length ? 'Fix the problems first' : excludedFiles.length ? 'Remove the excluded files' : undefined} onclick={saveVersion} data-testid="cloud-save">{progress ?? 'Save as new version'}</Button>
        </div>
      {/if}
    {:else}
      {#if !versions.length}
        <EmptyState compact icon={History} title="No versions yet" text="Snapshots from the site and edits saved here show up as versions." />
      {:else}
        <div class="table-wrap">
          <table class="table" data-testid="cloud-versions">
            <thead><tr><th>#</th><th>Origin</th><th class="hide-sm">By</th><th>When</th><th class="hide-sm">Content</th><th class="hide-sm">Changes</th><th class="actions"><span class="sr-only">Actions</span></th></tr></thead>
            <tbody>
              {#each versions as v (v.versionId)}
                <tr data-testid="cloud-version-row" data-seq={v.seq}>
                  <td class="strong">#{v.seq}{#if v.versionId === cs.latest?.versionId}<div class="xs text-accent">latest</div>{/if}</td>
                  <td>
                    <span class="badge {ORIGIN_TONE[v.origin]} xs">{v.origin}</span>
                    {#if v.trigger}<div class="xs faint">{TRIGGER[v.trigger] ?? v.trigger}</div>{/if}
                    {#if v.origin === 'restore' && v.restoredFromVersionId}<div class="xs faint" data-testid="cloud-version-restored-from">Restored from #{seqOf(v.restoredFromVersionId) ?? '?'}</div>{:else if v.origin === 'edit' && v.baseVersionId}<div class="xs faint">edited from #{seqOf(v.baseVersionId) ?? '?'}</div>{/if}
                    {#if v.note}<div class="xs muted note" title={v.note}>“{v.note}”</div>{/if}
                  </td>
                  <td class="small hide-sm">{personName(v.createdBy)}</td>
                  <td class="small"><RelTime ts={v.createdAt} /></td>
                  <td class="small hide-sm">{v.fileCount} files · {formatContentBytes(v.totalBytes)}{#if v.excludedCount || v.skippedCount}<div class="xs faint">{v.excludedCount} excluded{v.skippedCount ? ` · ${v.skippedCount} skipped` : ''}</div>{/if}</td>
                  <td class="small hide-sm mono"><span class="text-green">+{v.changes.added}</span> <span class="text-red">−{v.changes.removed}</span> <span class="text-amber">~{v.changes.modified}</span></td>
                  <td class="actions nowrap">
                    <Button size="sm" variant="ghost" icon={GitCompare} tip="Diff vs the previous version" aria-label="Diff of version {v.seq}" onclick={() => showDiff(v)} data-testid="cloud-version-diff" />
                    <Button size="sm" variant="ghost" icon={Download} tip="Download the tree (JSON)" aria-label="Tree of version {v.seq}" onclick={async () => { const r = await api.getVolumeContentVersion({ ...key, versionId: v.versionId }); const b = new Blob([JSON.stringify(r.tree, null, 2)], { type: 'application/json' }); const a = document.createElement('a'); a.href = URL.createObjectURL(b); a.download = `${volumeSlug}-v${v.seq}.json`; a.click(); }} />
                    {#if canWrite && v.versionId !== cs.latest?.versionId}<Button size="sm" variant="ghost" icon={RotateCcw} tip="Restore as the new latest" aria-label="Restore version {v.seq}" onclick={() => ((restoreOf = v), (restoreNote = ''), (restoreOpen = true))} data-testid="cloud-version-restore" />{/if}
                    {#if canPublish}<Button size="sm" variant="ghost" icon={Send} tip="Send this version to the site" aria-label="Send version {v.seq}" onclick={() => openSend(v.versionId)} data-testid="cloud-version-send" />{/if}
                    {#if canWrite}<Button size="sm" variant="ghost" icon={Cloud} tip="Use as starter content (draft)" aria-label="Use version {v.seq} as starter content" onclick={() => ((starterOf = v), (starterOpen = true))} data-testid="cloud-version-starter" />{/if}
                  </td>
                </tr>
                {#if diffOf === v.versionId}
                  <tr class="diffrow"><td colspan="7">
                    {#if diffTrees}<TreeDiffList a={diffTrees.a} b={diffTrees.b} aLabel={diffTrees.aSeq ? `#${diffTrees.aSeq}` : 'empty'} bLabel="#{diffTrees.bSeq}" testid="cloud-version-changes" />{:else}<div class="small faint">Loading…</div>{/if}
                  </td></tr>
                {/if}
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    {/if}
  </div>
{/if}

<Modal bind:open={sendOpen} title="Send to site {site.siteSlug}" subtitle="Nothing changes on the site until a person there applies it (agent GUI › Docker › Volumes)." busy={sending} testid="send-modal">
  <form id="send-form" class="stack" onsubmit={send} novalidate>
    <Field label="Version" id="send-version" required>
      <Select id="send-version" options={versionOpts} bind:value={sendVersion} testid="send-version" ariaLabel="Version to send" />
    </Field>
    <Field label="Restart" id="send-restart" hint={users.length ? `Stops ${users.join(', ')} before writing and starts them after (the site can change this when applying).` : 'No enabled container mounts this volume.'}>
      <label class="chk"><input id="send-restart" type="checkbox" bind:checked={sendRestart} data-testid="send-restart" /> Restart the containers that mount the volume</label>
    </Field>
    <Field label="Note" id="send-note" hint="Shown to the person who applies it.">
      <input id="send-note" class="input" bind:value={sendNote} maxlength={VOLUME_CONTENT_LIMITS.noteMax} data-testid="send-note" />
    </Field>
    {#if pending}<div class="xs muted" data-testid="send-supersedes">Replaces the pending push of version #{pushSeq(pending) ?? '?'}.</div>{/if}
    {#if oldAgent}<div class="banner warn small" data-testid="send-agent-warning"><Icon icon={TriangleAlert} size={15} class="icon" /><div>Site {site.siteSlug} runs agent {agentVersion}; volume content needs agent {VOLUME_CONTENT_MIN_AGENT} or newer — it will not see this push until the agent is upgraded. <a href="/app/deployments/{scope.id}/setup">Upgrade on Setup →</a></div></div>{/if}
  </form>
  {#snippet footer()}
    <Button onclick={() => (sendOpen = false)} disabled={sending}>Cancel</Button>
    <Button type="submit" form="send-form" variant="primary" icon={Send} loading={sending} disabled={!sendVersion} data-testid="send-submit">Send</Button>
  {/snippet}
</Modal>

<Modal bind:open={cmpOpen} title="Your edits vs the newest version" size="lg" testid="compare-modal">
  {#if cmpTree}<TreeDiffList a={cmpTree} b={ws.tree} edited={editedTexts} aLabel="newest" bLabel="yours" testid="compare-list" />{/if}
  {#snippet footer()}
    <Button onclick={() => (cmpOpen = false)}>Close</Button>
    <Button icon={RefreshCw} onclick={() => ((cmpOpen = false), reloadLatest())}>Reload (drop my edits)</Button>
  {/snippet}
</Modal>

<ConfirmDialog bind:open={cancelOpen} title="Cancel the pending push?" message="The site will not be asked to apply version #{pushSeq(pending) ?? '?'} any more (if nobody applied it yet)." confirmLabel="Cancel push" danger onconfirm={cancelPush} testid="cancel-push-dialog" />

<ConfirmDialog bind:open={restoreOpen} title="Restore version #{restoreOf?.seq ?? ''}?" confirmLabel="Restore" onconfirm={restore} testid="restore-dialog">
  {#snippet body()}
    <div class="stack">
      <p class="small muted">A copy of version #{restoreOf?.seq} becomes the new latest version. It reaches the site only with Send to site.</p>
      <input class="input" placeholder="Note (optional)" bind:value={restoreNote} maxlength={VOLUME_CONTENT_LIMITS.noteMax} aria-label="Note" data-testid="restore-note" />
    </div>
  {/snippet}
</ConfirmDialog>

<ConfirmDialog bind:open={starterOpen} title="Use version #{starterOf?.seq ?? ''} as starter content?" confirmLabel="Use as starter content" onconfirm={useAsStarter} testid="starter-dialog">
  {#snippet body()}
    <p class="small muted">The draft's starter content of {volumeSlug} becomes this version's files (render none) and the draft is saved. Starter content is written only into new / empty volumes — existing sites keep their data.</p>
  {/snippet}
</ConfirmDialog>

<style>
  .head {
    display: grid;
    grid-template-columns: minmax(0, 1.2fr) minmax(0, 1.3fr) auto;
    gap: var(--sp-4);
    align-items: start;
  }
  .st {
    display: flex;
    flex-direction: column;
    gap: 6px;
    align-items: flex-start;
  }
  .facts {
    display: flex;
    flex-direction: column;
    gap: 3px;
  }
  .lab {
    display: inline-block;
    min-width: 112px;
    color: var(--text-3);
    font-size: var(--fs-xs);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    font-weight: 600;
  }
  .acts {
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
    justify-content: flex-end;
  }
  .grow {
    flex: 1;
    min-width: 0;
  }
  .banner {
    align-items: center;
  }
  .savebar {
    display: flex;
    gap: 8px;
    align-items: center;
  }
  .savebar .input {
    flex: 1;
    min-width: 0;
  }
  .note {
    max-width: 220px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .diffrow td {
    background: var(--surface-2);
  }
  .chk {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    font-size: var(--fs-md);
  }
  @media (max-width: 900px) {
    .head {
      grid-template-columns: minmax(0, 1fr);
    }
    .acts {
      justify-content: flex-start;
    }
  }
  @media (max-width: 720px) {
    .hide-sm {
      display: none;
    }
    .savebar {
      flex-wrap: wrap;
    }
    .savebar .input {
      flex-basis: 100%;
    }
    .banner {
      flex-wrap: wrap;
    }
    .banner .grow {
      flex-basis: calc(100% - 40px);
    }
  }
</style>
