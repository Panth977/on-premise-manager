<script lang="ts">
  /**
   * §21 #101 a volume's starter content, READ-ONLY (shared by both GUIs, §21 #102) (it is cloud configuration: the bytes live in cloud Storage):
   * tree = file list with size / mode / render + empty folders; single = one file; archive; legacy File seeds of an old
   * head = "(legacy) from Files …". `compact` = one line.
   */
  import type { VolumeSeed } from '@opm/shared/types';
  import { formatContentBytes, formatMode, treeStats } from '@opm/shared/volume-content';
  import Icon from '../../primitives/Icon.svelte';

  interface Props {
    seed: VolumeSeed | null | undefined;
    compact?: boolean;
    testid?: string;
  }
  let { seed, compact = false, testid = 'seed-view' }: Props = $props();
  const stats = $derived(seed?.kind === 'tree' ? treeStats(seed) : null);
  const line = $derived.by(() => {
    if (!seed) return 'Empty (starts empty)';
    if (seed.kind === 'tree') return stats!.fileCount === 0 && seed.dirs.length === 0 ? 'Empty (starts empty)' : `${stats!.fileCount} file${stats!.fileCount === 1 ? '' : 's'} · ${formatContentBytes(stats!.totalBytes)}${seed.dirs.length ? ` · ${seed.dirs.length} empty folder${seed.dirs.length === 1 ? '' : 's'}` : ''}`;
    if (seed.kind === 'single') return `1 file · ${formatContentBytes(seed.size)} · ${seed.mode}${seed.render === 'expressions' ? ' · template' : ''}`;
    if (seed.kind === 'files') return `(legacy) from Files ${seed.entries.map((e) => e.fileSlug).join(', ')}`;
    if (seed.kind === 'file') return `(legacy) from File ${seed.fileSlug}`;
    return `archive ${seed.name} · ${formatContentBytes(seed.size)}`;
  });
  const legacy = $derived(seed?.kind === 'files' || seed?.kind === 'file');
</script>

{#if compact}
  <span class="small" class:legacy data-testid={testid}>{line}</span>
{:else}
  <div class="sv" data-testid={testid} data-kind={seed?.kind ?? 'none'}>
    <div class="small">{#if legacy}<span class="badge warn">legacy</span> {/if}{line}</div>
    {#if seed?.kind === 'tree' && (seed.entries.length || seed.dirs.length)}
      <div class="table-wrap">
        <table class="table compact" data-testid="{testid}-tree">
          <thead><tr><th>Path</th><th class="num">Size</th><th>Mode</th><th>Render</th></tr></thead>
          <tbody>
            {#each seed.entries as e (e.path)}
              <tr data-testid="{testid}-entry-{e.path}"><td class="mono small path"><Icon name="file" size={12} /> {e.path}</td><td class="num small">{formatContentBytes(e.size)}</td><td class="mono xs" title={formatMode(e.mode)}>{e.mode}</td><td class="small">{e.render === 'expressions' ? 'template (once per site)' : 'as is'}</td></tr>
            {/each}
            {#each seed.dirs as d (d)}
              <tr><td class="mono small path"><Icon name="folder" size={12} /> {d}/</td><td class="num small faint">empty</td><td></td><td></td></tr>
            {/each}
          </tbody>
        </table>
      </div>
    {:else if seed?.kind === 'single'}
      <dl class="kv small"><dt>Size</dt><dd>{formatContentBytes(seed.size)}</dd><dt>Mode</dt><dd class="mono">{seed.mode} {formatMode(seed.mode)}</dd><dt>Render</dt><dd>{seed.render === 'expressions' ? 'template (once per site)' : 'as is'}</dd></dl>
    {:else if seed?.kind === 'files'}
      <ul class="small mono leg">{#each seed.entries as e (e.path)}<li>{e.path} ← file {e.fileSlug}</li>{/each}</ul>
    {/if}
    {#if legacy}
      <p class="tiny muted" style="margin:0">Files are static and read-only — they are no longer volume content. Convert it to the volume's own content in the cloud console (agent 0.12+ still applies this old revision).</p>
    {/if}
  </div>
{/if}

<style>
  .sv {
    display: flex;
    flex-direction: column;
    gap: 6px;
    min-width: 0;
  }
  .path {
    overflow-wrap: anywhere;
  }
  .legacy {
    color: var(--amber-text, var(--text-2));
  }
  .leg {
    margin: 0;
    padding-left: 18px;
  }
  .table-wrap {
    max-height: 260px;
    overflow: auto;
  }
</style>
