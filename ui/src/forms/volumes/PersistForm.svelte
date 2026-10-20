<script lang="ts" module>
  import { compileExcludes, isExcluded, parentDir } from '@opm/shared/volume-content';
  import type { VolumeTree } from '@opm/shared/types';
  /** what the exclude patterns hide in a tree: excluded files + the TOP excluded folders */
  export function excludedIn(tree: VolumeTree, patterns: readonly string[]): { files: string[]; dirs: string[] } {
    const c = compileExcludes(patterns);
    const dirs = new Set<string>(tree.dirs);
    for (const e of tree.entries) {
      const parts = e.path.split('/');
      for (let i = 1; i < parts.length; i++) dirs.add(parts.slice(0, i).join('/'));
    }
    const exDirs = [...dirs].filter((d) => isExcluded(d, true, c) && (!parentDir(d) || !isExcluded(parentDir(d), true, c))).sort();
    return { files: tree.entries.filter((e) => isExcluded(e.path, false, c)).map((e) => e.path), dirs: exDirs };
  }
</script>

<script lang="ts">
  /**
   * §21 #101 "Persist to cloud" settings of a folder volume (SiteVolume.persist; the key is DELETED when off) — ONE form
   * for the cloud content page (editable) and the agent Cloud sync tab (read-only, cloud configuration), §21 #102:
   * on / off · Exclude patterns (gitignore-lite, live check + preview of what they hide) · Size cap. §21 #117: the
   * content is committed and merged (no snapshots / Send); `auto` stays in the schema and is not shown. Test ids:
   * persist-card · persist-toggle · persist-help · persist-issue · persist-exclude(-i, -del-i, -add, -defaults) ·
   * persist-preview(-none, -item, -count) · persist-cap.
   */
  import type { SiteVolume, ValidationIssue } from '@opm/shared/types';
  import { VOLUME_CONTENT_LIMITS, VOLUME_PERSIST_DEFAULT_EXCLUDE, VOLUME_SEED_MARKER, defaultVolumePersist, excludePatternError } from '@opm/shared/volume-content';
  import Toggle from '../../primitives/Toggle.svelte';
  import Field from '../../primitives/Field.svelte';
  import NumberStepper from '../../primitives/NumberStepper.svelte';
  import Btn from '../../primitives/Btn.svelte';
  import Icon from '../../primitives/Icon.svelte';

  type Props = {
    volume: Pick<SiteVolume, 'kind' | 'persist'>;
    previewTree?: VolumeTree | null;
    previewLabel?: string;
    readOnly?: boolean;
    /** why it is read-only here (agent: cloud configuration) */
    lock?: string | null;
    issues?: readonly ValidationIssue[];
    /** a short help paragraph (cloud) — off in the agent's compact tab */
    help?: boolean;
    testid?: string;
  };
  let { volume, previewTree = null, previewLabel = 'the current tree', readOnly = false, lock = null, issues = [], help = true, testid = 'persist-card' }: Props = $props();
  const L = VOLUME_CONTENT_LIMITS;
  const p = $derived(volume.persist ?? null);
  const isFile = $derived(volume.kind === 'file');
  function setOn(on: boolean) {
    if (on) volume.persist = defaultVolumePersist();
    else delete volume.persist;
  }
  const errs = $derived((p?.exclude ?? []).map((x, i, all) => excludePatternError(x) ?? (all.indexOf(x) !== i ? 'listed twice' : null)));
  const preview = $derived(p && previewTree ? excludedIn(previewTree, p.exclude.filter((_x, i) => !errs[i])) : null);
  const mib = $derived(p ? Math.round(p.maxBytes / 1048576) : 50);
</script>

<section class="card" data-testid={testid}>
  <div class="card-head">
    <h3 class="ttl"><Icon name="cloud" size={16} /> Persist to cloud{#if lock}<span class="lk" title={lock} aria-label={lock} data-testid="field-lock"><Icon name="lock" size={12} /></span>{/if}</h3>
    <Toggle checked={!!p} onchange={setOn} disabled={readOnly || isFile} label={p ? 'on' : 'off'} testid="persist-toggle" />
  </div>
  <fieldset class="form card-body stack" disabled={readOnly}>
    {#if isFile}
      <p class="small muted">Only folder volumes can persist to the cloud.</p>
    {:else if help}
      <p class="small muted" data-testid="persist-help">The folder's content becomes part of the commits: edit it under <b>Content</b> and Commit…; a site writes it into the volume when it merges. Files changed on the server show on the site as uncommitted changes — a person commits them there (the cloud then takes them from the site's branch), and a merge shows every file both sides changed so they can keep the site's version or take the cloud's. Use it for data that changes on the site and that you also want to edit in the console — e.g. Node-RED flows with a client's integration code — instead of a git repository.</p>
    {/if}
    {#each issues as x, k (k)}<div class={x.severity === 'error' ? 'field-err' : x.severity === 'info' ? 'field-info' : 'field-warn'} data-testid="persist-issue">{x.message}</div>{/each}
    {#if p}
      <Field label="Exclude" group hint="gitignore-lite: name = at any depth, a/b = from the volume root, trailing / = folders only, * ? ** globs. {VOLUME_SEED_MARKER} is always excluded. Excluded paths are never committed nor touched when a merge writes the content.">
        <div class="ex" data-testid="persist-exclude">
          {#each p.exclude as _x, i (i)}
            <div class="er">
              <input class="input sm mono" bind:value={p.exclude[i]} disabled={readOnly} class:invalid={!!errs[i]} aria-label="Exclude pattern {i + 1}" data-testid="persist-exclude-{i}" />
              {#if !readOnly}<Btn size="sm" variant="ghost" icon="x" aria-label="Remove {p.exclude[i]}" onclick={() => p.exclude.splice(i, 1)} data-testid="persist-exclude-del-{i}" />{/if}
              {#if errs[i]}<div class="field-err full">{errs[i]}</div>{/if}
            </div>
          {:else}
            <span class="small muted">nothing</span>
          {/each}
          {#if !readOnly}
            <div class="row wrap" style="gap:6px">
              <Btn size="sm" icon="plus" disabled={p.exclude.length >= L.excludeMax} onclick={() => p.exclude.push('')} data-testid="persist-exclude-add">Add pattern</Btn>
              <Btn size="sm" variant="ghost" icon="undo" onclick={() => (p.exclude = [...VOLUME_PERSIST_DEFAULT_EXCLUDE])} data-testid="persist-exclude-defaults">Defaults</Btn>
            </div>
          {/if}
        </div>
      </Field>
      {#if previewTree || !readOnly}
        <div class="pv" data-testid="persist-preview">
          <div class="xs strong">Excluded in {previewLabel}</div>
          {#if !preview}
            <div class="xs faint">No tree to preview yet.</div>
          {:else if !preview.files.length && !preview.dirs.length}
            <div class="xs faint" data-testid="persist-preview-none">Nothing in {previewLabel} matches.</div>
          {:else}
            <ul class="mono xs">
              {#each preview.dirs.slice(0, 12) as d (d)}<li data-testid="persist-preview-item">{d}/</li>{/each}
              {#each preview.files.filter((f) => !preview.dirs.some((d) => f.startsWith(d + '/'))).slice(0, 20) as f (f)}<li data-testid="persist-preview-item">{f}</li>{/each}
            </ul>
            <div class="xs faint" data-testid="persist-preview-count">{preview.files.length} file{preview.files.length === 1 ? '' : 's'}{preview.dirs.length ? ` in / under ${preview.dirs.length} folder${preview.dirs.length === 1 ? '' : 's'}` : ''}</div>
          {/if}
        </div>
      {/if}
      <div class="two">
        <Field label="Size cap" id="persist-cap" hint="Sum of the non-excluded files; bigger content cannot be committed ({L.persistMinBytes / 1048576}–{L.persistMaxBytes / 1048576} MiB).">
          <NumberStepper id="persist-cap" value={mib} min={1} max={200} suffix="MiB" disabled={readOnly} testid="persist-cap" label="Size cap in MiB" onchange={(v) => (p.maxBytes = Math.round(Math.min(200, Math.max(1, v !== null && Number.isFinite(v) ? v : 50))) * 1048576)} />
        </Field>
      </div>
    {/if}
  </fieldset>
</section>

<style>
  .card-head {
    gap: 8px;
  }
  .ttl {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    margin: 0;
  }
  .lk {
    display: inline-flex;
    color: var(--text-3);
    cursor: help;
  }
  .ex {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .er {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 28px;
    gap: 6px;
    align-items: center;
  }
  .full {
    grid-column: 1 / -1;
  }
  .pv {
    border: 1px dashed var(--border-strong);
    border-radius: var(--r-md);
    padding: 8px 10px;
    background: var(--surface-2);
  }
  .pv ul {
    margin: 4px 0;
    padding-left: 16px;
    max-height: 160px;
    overflow: auto;
  }
  .two {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--sp-3);
  }
  @media (max-width: 640px) {
    .two {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>
