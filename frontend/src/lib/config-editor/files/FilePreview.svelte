<script lang="ts">
  import type { Config, ExprContext, FileDef, ValidationContext } from '@opm/shared';
  import { untrack } from 'svelte';
  import { autoHostRuns, renderForSite, type SiteTarget } from './render';
  import Select, { type SelectGroup } from '../../ui/Select.svelte';

  /**
   * "Preview for site…" (round D part 2: a file is rendered once per site): pick a site (the ones where a container
   * mounts the file; any site as a hypothetical when none does) and show the rendered file (secrets masked "••••", auto
   * hosts as "auto · slug" chips, render errors as file:line:col, V_FILE_JSON warning for JSON mimes).
   */
  type Props = {
    cfg: Config;
    vctx: ValidationContext;
    file: FileDef;
    text: string;
    targets: { mounting: SiteTarget[]; all: SiteTarget[] };
    siteMap?: NonNullable<ExprContext['site']>;
    /** syntax errors: nothing to render */
    blocked?: boolean;
    onjump?: (line: number, col: number) => void;
    /** the previewed site's context (autocomplete) */
    onctx?: (c: ExprContext | null) => void;
  };
  let { cfg, vctx, file, text, targets, siteMap, blocked = false, onjump, onctx }: Props = $props();

  let sel = $state('');
  let lastFile = '';
  $effect(() => {
    const slug = file.fileSlug;
    const first = targets.mounting[0]?.siteSlug ?? '';
    untrack(() => {
      if (slug !== lastFile || (sel && !targets.all.some((t) => t.siteSlug === sel))) {
        lastFile = slug;
        sel = first;
      }
    });
  });
  const others = $derived(targets.all.filter((t) => !t.mounts.length));
  const target = $derived(targets.all.find((t) => t.siteSlug === sel) ?? null);
  const preview = $derived(target && !blocked ? renderForSite(cfg, vctx, file, target, text, siteMap) : null);
  $effect(() => {
    const c = preview?.ectx ?? null;
    untrack(() => onctx?.(c));
  });
  const opt = (t: SiteTarget) => ({ value: t.siteSlug, label: `${t.siteSlug}${t.siteLabel && t.siteLabel !== t.siteSlug ? ` · ${t.siteLabel}` : ''}`, sub: t.mounts.length ? `mounted by ${t.mounts.map((m) => m.containerSlug).join(', ')}` : undefined });
  const groups = $derived<SelectGroup[]>([
    { options: [{ value: '', label: '— pick a site —' }] },
    ...(targets.mounting.length ? [{ label: 'Sites that mount this file', options: targets.mounting.map(opt) }] : []),
    ...(others.length ? [{ label: targets.mounting.length ? 'Other sites (hypothetical)' : 'Any site (hypothetical — no container mounts this file)', options: others.map(opt) }] : []),
  ]);
  const runs = $derived(preview?.out.ok ? autoHostRuns(preview.out.masked) : []);
</script>

<section class="pv" data-testid="file-preview">
  <div class="row between wrap head">
    <label class="strong small" for="pv-target">Preview for site…</label>
    <div class="tsel"><Select id="pv-target" value={sel} {groups} ariaLabel="Preview for site" testid="preview-target" disabled={!targets.all.length} minWidth={300} onchange={(v) => (sel = v)} /></div>
  </div>
  {#if !targets.mounting.length}
    <p class="xs muted" data-testid="preview-none-mounting">
      {#if targets.all.length}No container mounts <span class="mono">{file.fileSlug}</span> yet (container page → Volumes → Mounted from → Files), so no site keeps a copy. Pick any site to preview it hypothetically.
      {:else}No sites yet — add a site to preview the rendered file.{/if}
    </p>
  {/if}
  {#if target && blocked}
    <p class="xs muted">Fix the problems above to see the preview.</p>
  {:else if target && preview}
    {#if !target.mounts.length}<p class="xs amber-text" data-testid="preview-hypothetical">Hypothetical: no container on {target.siteSlug} mounts this file.</p>{/if}
    {#if !preview.out.ok}
      {@const o = preview.out}
      <button type="button" class="banner error small jump" onclick={() => onjump?.(o.line, o.col)} data-testid="file-preview-error">
        <span class="mono">{file.fileSlug}:{o.line}:{o.col}: {o.code}</span>&nbsp;{o.message} <span class="faint">(site {target.siteSlug})</span>
      </button>
    {:else}
      {#if preview.jsonError}
        <div class="banner warn small" data-testid="file-preview-json"><div><b>Not valid JSON</b> after rendering (V_FILE_JSON — staging on the site fails): {preview.jsonError}</div></div>
      {/if}
      <div class="row between xs muted">
        <span data-testid="preview-site-note">Rendered once for site <b>{target.siteSlug}</b>{target.mounts.length ? ` · shared by ${target.mounts.map((m) => m.containerSlug).join(', ')}` : ''}</span>
        {#if preview.out.secret}<span class="badge amber" data-testid="preview-secret">secrets masked ••••</span>{/if}
      </div>
      <pre class="out mono" data-testid="file-preview-output">{#each runs as r, i (i)}{#if r.auto}<span class="chip auto" title="auto host of site {r.auto}: detected on install (SiteConfig.host = auto)" data-testid="auto-host-chip">auto · {r.auto}</span>{:else}{r.t}{/if}{/each}</pre>
    {/if}
  {/if}
</section>

<style>
  .pv {
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    padding: 10px 12px;
    display: flex;
    flex-direction: column;
    gap: 8px;
    background: var(--surface-2);
  }
  .tsel {
    max-width: 100%;
    min-width: 260px;
    flex: 1 1 260px;
  }
  .head {
    gap: 8px;
  }
  .out {
    margin: 0;
    padding: 8px 10px;
    background: var(--code-bg);
    border: 1px solid var(--border);
    border-radius: var(--r-sm);
    font-size: 12px;
    line-height: 1.55;
    white-space: pre-wrap;
    word-break: break-word;
    max-height: 40vh;
    overflow: auto;
  }
  .chip.auto {
    height: 18px;
    font-size: 11px;
    background: var(--accent-soft);
    color: var(--accent);
    vertical-align: baseline;
  }
  .jump {
    text-align: left;
    cursor: pointer;
    width: 100%;
  }
  .amber-text {
    color: var(--amber, #b45309);
    margin: 0;
  }
  p {
    margin: 0;
  }
</style>
