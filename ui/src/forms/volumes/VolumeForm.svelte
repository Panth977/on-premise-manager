<script lang="ts" module>
  import type { SiteVolume, SiteVolumeKind, VolumeSeed } from '@opm/shared/types';
  /** A new volume (suggested name, kind). */
  export function newVolume(suggested = '', kind: SiteVolumeKind = 'dir'): SiteVolume {
    return { volumeSlug: suggested, label: '', description: '', backup: true, location: { kind: 'auto' }, kind, seed: null };
  }
  /** The volume to store: label defaults to the name; persist only on folder volumes (absent = off). */
  export function finishVolume(v: SiteVolume): SiteVolume {
    const out: SiteVolume = { volumeSlug: v.volumeSlug, label: v.label.trim() || v.volumeSlug, description: v.description, backup: v.backup, location: v.location, kind: v.kind, seed: v.seed };
    if (v.persist && v.kind === 'dir') out.persist = v.persist;
    if (v.backupPolicy) out.backupPolicy = { schedule: v.backupPolicy.schedule, keep: v.backupPolicy.keep }; // §21 #112 (absent = the daily site snapshot)
    return out;
  }
  export type SeedMode = 'empty' | 'own' | 'archive';
  export const seedModeOf = (s: VolumeSeed | null | undefined): SeedMode => (s ? (s.kind === 'archive' ? 'archive' : 'own') : 'empty');
</script>

<script lang="ts">
  /**
   * Site volume definition (SPEC §2.2 "Site volumes", round D / E, §21 #101) — ONE form for the cloud drawer (template +
   * deployment, also "+ New data volume…" on the container page) and the agent local edit (§21 #102).
   * Order: Name · Label · Description · Kind · Location on the server · Back up · Starter content · Persist to cloud.
   * Agent: starter content + persist are cloud configuration (locked, shown read-only, kept as is); the name and kind are
   * fixed after create in both apps. Test ids (cloud, round D–E specs): vm-slug · vm-label · vm-desc · vm-kind(-dir|-file)
   * · vm-file-warning · vm-location(-mode-*, -path, -err, -hint) · vm-backup · vm-seed-mode(-*) · vm-seed-note ·
   * vm-seed-own · vm-seed-edit-link · vm-seed-legacy(-link) · vm-seed-view · vm-seed-archive(-box, -name, -error) ·
   * vm-persist (+ vm-persist-link / vm-console-link).
   */
  import { FILE_VOLUME_RENAME_WARNING, Issues, LIMITS, RE, checkLocation, seedLabel } from '@opm/shared/validate';
  import { SEED_ARCHIVE_MAX } from '@opm/shared/types';
  import { formatContentBytes, isLegacyFileSeed } from '@opm/shared/volume-content';
  import Field from '../../primitives/Field.svelte';
  import Toggle from '../../primitives/Toggle.svelte';
  import SegmentedControl from '../../primitives/SegmentedControl.svelte';
  import Icon from '../../primitives/Icon.svelte';
  import Select from '../../primitives/Select.svelte';
  import NumberStepper from '../../primitives/NumberStepper.svelte';
  import type { SelectOption } from '../../primitives/listbox';
  import type { VolumeBackupSchedule } from '@opm/shared/types';
  import LocationField from './LocationField.svelte';
  import SeedView from './SeedView.svelte';
  import { useConfigHost, type FormMode } from '../host';
  import { LOCK_CLOUD_ONLY, LOCK_FIXED } from '../locks';

  type Props = {
    value: SiteVolume;
    mode: FormMode;
    siteSlug: string;
    /** volumeSlugs already declared on the site */
    taken: string[];
    /** the agent data dir of the site (resolved location hint; agent GUI) */
    agentHome?: string;
    /** cloud: the volume's content page (`…/volumes/content?volume=`); null = no link */
    contentHref?: ((volumeSlug: string, tab?: 'persist') => string) | null;
    /** agent: the cloud console page of this volume ("Edit in the cloud console") */
    consoleHref?: string | null;
    /** cloud: upload a starter .tar.gz → its blob (null = archives not offered) */
    uploadArchive?: ((f: File) => Promise<{ sha256: string; sizeBytes: number }>) | null;
    submitted?: boolean;
    valid?: boolean;
    busy?: boolean;
    /** the starter content mode (bindable: the caller's "Add & edit content" needs it) */
    seedMode?: SeedMode;
  };
  let { value = $bindable(), mode, siteSlug, taken, agentHome, contentHref = null, consoleHref = null, uploadArchive = null, submitted = false, valid = $bindable(false), busy = $bindable(false), seedMode = $bindable(seedModeOf(value.seed)) }: Props = $props();
  const host = useConfigHost();
  const agent = $derived(host.scope === 'agent');
  const ro = $derived(mode === 'read' || host.readOnly);
  const create = $derived(mode === 'create');
  const initialSlug = value.volumeSlug;

  // §21 #112 automatic backups: 'site' = no own policy (the daily site snapshot keeps 7)
  const AUTO_OPTS: SelectOption[] = [
    { value: 'site', label: 'With the daily site snapshot', hint: '7 kept' },
    { value: 'hourly', label: 'Every hour', hint: 'taken live' },
    { value: '6h', label: 'Every 6 hours', hint: 'taken live' },
    { value: 'daily', label: 'Every day', hint: 'containers paused' },
    { value: 'weekly', label: 'Every week', hint: 'containers paused' },
    { value: 'off', label: 'Off', hint: 'pre-change backups only' },
  ];
  function setSchedule(v: string | null) {
    if (!v || v === 'site') {
      delete value.backupPolicy;
      value = { ...value };
      return;
    }
    value.backupPolicy = { schedule: v as VolumeBackupSchedule, keep: value.backupPolicy?.keep ?? (v === 'hourly' ? 24 : 7) };
  }
  const autoHint = $derived(
    !value.backupPolicy
      ? 'In the daily scheduled site snapshot; its 7 newest are kept.'
      : value.backupPolicy.schedule === 'off'
        ? 'No automatic backups — a backup is still taken before every change (restore, update, roll back).'
        : `Its own snapshots ${value.backupPolicy.schedule === 'hourly' ? 'every hour' : value.backupPolicy.schedule === '6h' ? 'every 6 hours' : value.backupPolicy.schedule === 'daily' ? 'every day' : 'every week'}${value.backupPolicy.schedule === 'hourly' || value.backupPolicy.schedule === '6h' ? ' (taken while the containers run)' : ' (its containers stop for the archive)'}; the ${value.backupPolicy.keep} newest are kept and never removed to save space. Restore any of them on the Volumes page.`,
  );

  // starter content: the mode control + what each mode keeps (cloud editing; agent = locked, kept as is)
  // svelte-ignore state_referenced_locally
  let kept = $state<VolumeSeed | null>(value.seed && value.seed.kind !== 'archive' ? value.seed : null);
  // svelte-ignore state_referenced_locally
  let archive = $state<{ blobSha256: string; name: string; size: number } | null>(value.seed?.kind === 'archive' ? { blobSha256: value.seed.blobSha256, name: value.seed.name, size: value.seed.size } : null);
  let upErr = $state<string | null>(null);

  const slugErr = $derived.by(() => {
    if (!create) return null;
    const s = value.volumeSlug;
    if (!s) return 'Required';
    if (!RE.volumeSlug.test(s)) return '1–100 characters: a–z, 0–9, ".", "_" and "-", starting with a letter or digit';
    if (taken.includes(s)) return `site ${siteSlug} already has a volume ${s}`;
    return null;
  });
  const labelErr = $derived(value.label.length > LIMITS.label ? `At most ${LIMITS.label} characters` : null);
  const descErr = $derived(value.description.length > LIMITS.description ? `At most ${LIMITS.description} characters` : null);
  const locErr = $derived.by(() => {
    const is = new Issues('template');
    checkLocation(value.location, 'volumes', 'l', is);
    return is.list.some((i) => i.severity === 'error');
  });
  const seedErr = $derived(seedMode === 'archive' && !archive ? 'upload a .tar.gz' : null);
  $effect(() => {
    valid = !ro && !slugErr && !labelErr && !descErr && !locErr && !seedErr && !busy;
  });
  // keep value.seed in step with the control (cloud only — the agent never changes it)
  $effect(() => {
    if (agent) return;
    const next: VolumeSeed | null = seedMode === 'own' ? kept : seedMode === 'archive' && archive ? { kind: 'archive', ...archive } : null;
    if (JSON.stringify(next) !== JSON.stringify(value.seed)) value.seed = next;
  });

  function setKind(k: SiteVolumeKind) {
    value.kind = k;
    if (k === 'file' && seedMode === 'archive') seedMode = 'empty';
    if (kept && ((k === 'file' && kept.kind === 'tree') || (k === 'dir' && kept.kind === 'single'))) kept = null;
    if (k === 'file') delete value.persist;
  }
  const SEED_MODES = $derived(
    value.kind === 'dir'
      ? [
          { value: 'empty' as SeedMode, label: 'Empty' },
          { value: 'own' as SeedMode, label: 'Own content', title: 'Files and folders edited on the volume content page' },
          { value: 'archive' as SeedMode, label: 'From archive', title: 'A .tar.gz of the folder', disabled: !uploadArchive && !archive },
        ]
      : [
          { value: 'empty' as SeedMode, label: 'Empty' },
          { value: 'own' as SeedMode, label: 'Own content', title: 'The file, edited on the volume content page' },
        ],
  );
  const legacy = $derived(seedMode === 'own' && isLegacyFileSeed(kept));
  const ownSummary = $derived(kept && !isLegacyFileSeed(kept) ? `${seedLabel(kept)}${kept.kind === 'tree' ? ` · ${formatContentBytes(kept.entries.reduce((n, e) => n + e.size, 0))}` : ''}` : null);
  const show = (dirty: boolean) => submitted || dirty;
  const seedLock = $derived(agent ? LOCK_CLOUD_ONLY : null);

  async function onArchive(e: Event & { currentTarget: HTMLInputElement }) {
    const f = e.currentTarget.files?.[0];
    e.currentTarget.value = '';
    if (!f || !uploadArchive) return;
    upErr = null;
    if (!/\.(tar\.gz|tgz)$/i.test(f.name)) return void (upErr = 'pick a .tar.gz (or .tgz) archive');
    if (f.size > SEED_ARCHIVE_MAX) return void (upErr = `the archive is larger than ${SEED_ARCHIVE_MAX / 1048576} MiB`);
    busy = true;
    try {
      const b = await uploadArchive(f);
      archive = { blobSha256: b.sha256, name: f.name, size: b.sizeBytes };
    } catch (err) {
      upErr = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }
</script>

<fieldset class="form stack" disabled={ro} data-testid="volume-form">
  <Field label="Name" id="vm-slug" required lock={create ? null : LOCK_FIXED} error={create && show(value.volumeSlug !== initialSlug) ? slugErr : null} hint={create ? 'The id of the volume on the site (containers pick it by name). Fixed after create.' : 'Fixed: a new name would be a new (empty) volume.'}>
    <input id="vm-slug" class="input mono" value={value.volumeSlug} oninput={(e) => (value.volumeSlug = e.currentTarget.value.trim().toLowerCase())} disabled={!create || ro} maxlength="100" spellcheck="false" autocomplete="off" placeholder="cassandra-data" data-autofocus data-testid="vm-slug" />
  </Field>
  <Field label="Label" id="vm-label" hint="Optional (empty = the name)" error={labelErr}>
    <input id="vm-label" class="input" bind:value={value.label} maxlength={LIMITS.label} placeholder={value.volumeSlug} disabled={ro} data-testid="vm-label" />
  </Field>
  <Field label="Description" id="vm-desc" error={descErr}>
    <textarea id="vm-desc" class="textarea" rows="2" bind:value={value.description} placeholder="What is stored here" disabled={ro} data-testid="vm-desc"></textarea>
  </Field>
  <Field label="Kind" id="vm-kind" group lock={create ? null : LOCK_FIXED} hint={!create ? 'Fixed after create (a folder cannot become a file on the server).' : value.kind === 'dir' ? 'A folder (most apps).' : 'ONE persistent read-write file, mounted at a file path (e.g. /data/settings.json).'}>
    <SegmentedControl
      id="vm-kind"
      options={[
        { value: 'dir' as SiteVolumeKind, label: 'Folder' },
        { value: 'file' as SiteVolumeKind, label: 'Single file' },
      ]}
      value={value.kind}
      onchange={setKind}
      disabled={!create || ro}
      ariaLabel="Volume kind"
      testid="vm-kind"
    />
  </Field>
  {#if value.kind === 'file'}
    <div class="banner warn small" data-testid="vm-file-warning"><Icon name="alert" size={14} /><div class="grow">{FILE_VOLUME_RENAME_WARNING} Mount it at a FILE path (not ending with "/").</div></div>
  {/if}
  <LocationField value={value.location} kind="volumes" slug={value.volumeSlug} {agentHome} noun={value.kind === 'file' ? 'File' : 'Folder'} disabled={ro} showErrors={submitted} onchange={(l) => (value.location = l)} testid="vm-location" />
  <Field label="Back up" id="vm-backup" group hint={value.backup ? 'Included in manual, scheduled and pre-update snapshots.' : 'Never archived (caches, scratch data).'}>
    <Toggle id="vm-backup" bind:checked={value.backup} label={value.backup ? 'on' : 'off'} disabled={ro} testid="vm-backup" />
  </Field>
  {#if value.backup}
    <!-- §21 #112 per-volume automatic backups (absent = the daily site snapshot, 7 kept) -->
    <Field label="Automatic backups" id="vm-autobackup" group hint={autoHint}>
      <div class="row tight">
        <Select id="vm-autobackup" value={value.backupPolicy?.schedule ?? 'site'} options={AUTO_OPTS} disabled={ro} onchange={(v) => setSchedule(v)} ariaLabel="Automatic backups" testid="vm-autobackup" />
        {#if value.backupPolicy && value.backupPolicy.schedule !== 'off'}
          <NumberStepper id="vm-autobackup-keep" value={value.backupPolicy.keep} min={1} max={100} label="backups kept" suffix="kept" disabled={ro} onchange={(n) => value.backupPolicy && n !== null && (value.backupPolicy = { ...value.backupPolicy, keep: n })} testid="vm-autobackup-keep" />
        {/if}
      </div>
    </Field>
  {/if}

  <Field label="Starter content" id="vm-seed" group lock={seedLock} error={submitted && seedErr ? seedErr : null}>
    <SegmentedControl id="vm-seed" options={SEED_MODES} bind:value={seedMode} disabled={ro || agent} ariaLabel="Starter content" testid="vm-seed-mode" />
    <div class="hint" data-testid="vm-seed-note"><Icon name="info" size={11} /> Applies only when the volume is created or empty on a site — existing data is never touched (the agent GUI can “Reset to starter content…”).</div>
  </Field>
  {#if seedMode === 'own'}
    {#if legacy}
      <div class="banner error small" data-testid="vm-seed-legacy"><Icon name="alert" size={14} /><div class="grow">Takes its content from Files ({seedLabel(kept)}) — Files are static and read-only and are never volume content. {agent ? 'Convert it to the volume’s own content in the cloud console (this site still applies the old revision).' : ''}{#if !agent}Open the content page and <b>Convert to own content</b>.{/if}{#if contentHref && !create} <a href={contentHref(value.volumeSlug)} data-testid="vm-seed-legacy-link">Open the content page →</a>{/if}{#if consoleHref} <a href={consoleHref} target="_blank" rel="noopener" data-testid="vm-console-link">Edit in the cloud console →</a>{/if}</div></div>
      {#if agent}<SeedView seed={kept} testid="vm-seed-view" />{/if}
    {:else}
      <div class="own small" data-testid="vm-seed-own">
        <span>{ownSummary ?? (value.kind === 'file' ? 'No content yet' : 'No files yet')}</span>
        {#if contentHref && !create}<a href={contentHref(value.volumeSlug)} data-testid="vm-seed-edit-link">Edit content →</a>{/if}
        {#if consoleHref}<a href={consoleHref} target="_blank" rel="noopener" data-testid="vm-console-link">Edit in the cloud console →</a>{/if}
      </div>
      {#if agent && kept}<SeedView seed={kept} testid="vm-seed-view" />{/if}
      {#if !agent}<div class="hint">The {value.kind === 'file' ? 'file is' : 'files and folders are'} edited on the volume's content page (file tree + editor){contentHref && create ? ' — use “Add & edit content”' : contentHref ? '' : ' (Site › Volumes › the volume)'}. Files under <b>Files</b> are never volume content.</div>{/if}
    {/if}
  {:else if seedMode === 'archive'}
    <div class="stack tight" data-testid="vm-seed-archive-box">
      {#if !agent && uploadArchive}
        <label class="btn sm up" aria-disabled={busy || ro}><Icon name="upload" size={13} />{busy ? 'Reading…' : archive ? 'Replace archive…' : 'Upload .tar.gz…'}<input type="file" accept=".tar.gz,.tgz,application/gzip" class="sr-only" disabled={busy || ro} onchange={onArchive} data-testid="vm-seed-archive" /></label>
      {/if}
      {#if archive}<div class="small" data-testid="vm-seed-archive-name"><span class="mono">{archive.name}</span> <span class="faint xs">{(archive.size / 1024).toFixed(1)} KiB</span></div>{/if}
      {#if upErr}<div class="field-err" data-testid="vm-seed-archive-error">{upErr}</div>{/if}
      <div class="hint">Regular files and folders only (no links, no absolute paths), ≤ {SEED_ARCHIVE_MAX / 1048576} MiB; unpacked as is (never rendered).</div>
    </div>
  {:else if agent}
    <div class="own small" data-testid="vm-seed-own"><span>Empty — the volume starts empty.</span>{#if consoleHref}<a href={consoleHref} target="_blank" rel="noopener" data-testid="vm-console-link">Edit in the cloud console →</a>{/if}</div>
  {/if}
  {#if !agent && value.kind === 'dir' && seedMode === 'empty'}
    <div class="hint">Tip: to prefill files of an app that rewrites them (Node-RED flows.json), keep a <b>Folder</b> volume with <b>Own content</b> — and turn on <b>Persist to cloud</b> on its content page when the site changes them.</div>
  {/if}

  {#if value.kind === 'dir' && !create}
    <Field label="Persist to cloud" group lock={agent ? LOCK_CLOUD_ONLY : null} testid="vm-persist" hint="Snapshots of the live folder go to the cloud; the cloud can send an edited version back (applied on the site: Docker › Volumes › Cloud sync).">
      {#if value.persist}
        <span class="small" data-testid="vm-persist-summary">On · automatic snapshots {value.persist.auto ? 'on' : 'off'} · cap {formatContentBytes(value.persist.maxBytes)}{#if value.persist.exclude.length} · excludes <span class="mono">{value.persist.exclude.join('  ')}</span>{/if}</span>
      {:else}
        <span class="small muted" data-testid="vm-persist-summary">Off</span>
      {/if}
      {#if contentHref}<a class="small" href={contentHref(value.volumeSlug, 'persist')} data-testid="vm-persist-link">Persist settings →</a>{/if}
    </Field>
  {/if}
</fieldset>

<style>
  .own {
    display: flex;
    gap: 10px;
    align-items: center;
    flex-wrap: wrap;
    padding: 8px 10px;
    border: 1px dashed var(--border-strong);
    border-radius: var(--r-md);
  }
  .up {
    align-self: flex-start;
    cursor: pointer;
  }
</style>
