<script lang="ts">
  /**
   * §21 #101 per-file changes between two trees (shared diffVolumeTrees): added / removed / changed (+ mode / render),
   * each expandable into a text diff (TextDiff, round L) of the two blobs (GET /v1/blobs/{sha}); binary or > 2 MiB →
   * sizes + hashes. `textOf` overrides where a side's text comes from (unsaved edits in the workspace).
   */
  import type { VolumeTree, VolumeTreeEntry } from '@opm/shared';
  import { VOLUME_CONTENT_LIMITS, diffVolumeTrees, formatContentBytes, looksLikeText } from '@opm/shared';
  import TextDiff from '../../ui/TextDiff.svelte';
  import Spinner from '../../ui/Spinner.svelte';
  import Icon from '../../ui/Icon.svelte';
  import { ChevronRight, ChevronDown } from '../../ui/icons';
  import { blobBytes, decodeText } from './content';

  type Props = {
    a: VolumeTree | null;
    b: VolumeTree;
    aLabel?: string;
    bLabel?: string;
    /** extra changed paths (e.g. unsaved text edits) with their texts */
    edited?: Record<string, string>;
    testid?: string;
  };
  let { a, b, aLabel = 'before', bLabel = 'after', edited = {}, testid = 'tree-diff' }: Props = $props();
  type Item = { path: string; kind: 'added' | 'removed' | 'changed'; before: VolumeTreeEntry | null; after: VolumeTreeEntry | null; edited: boolean };
  const items = $derived.by<Item[]>(() => {
    const list: Item[] = diffVolumeTrees(a, b).map((d) => ({ ...d, edited: d.path in edited }));
    for (const p of Object.keys(edited)) if (!list.some((x) => x.path === p)) {
      const e = b.entries.find((x) => x.path === p) ?? null;
      const o = a?.entries.find((x) => x.path === p) ?? null;
      list.push({ path: p, kind: o ? 'changed' : 'added', before: o, after: e, edited: true });
    }
    return list.sort((x, y) => (x.path < y.path ? -1 : 1));
  });
  const tone = { added: 'green', removed: 'red', changed: 'amber' } as const;
  let open = $state<Record<string, boolean>>({});
  let texts = $state<Record<string, { a: string | null; b: string | null; note: string | null } | 'loading'>>({});

  async function side(e: VolumeTreeEntry | null): Promise<{ text: string | null; note: string | null }> {
    if (!e) return { text: '', note: null };
    if (e.size > VOLUME_CONTENT_LIMITS.editorTextMaxBytes) return { text: null, note: `${formatContentBytes(e.size)} — too large for a text diff` };
    const bytes = await blobBytes(e.sha256);
    return looksLikeText(bytes) ? { text: decodeText(bytes), note: null } : { text: null, note: 'binary' };
  }
  async function toggle(it: Item) {
    open[it.path] = !open[it.path];
    if (!open[it.path] || texts[it.path]) return;
    texts[it.path] = 'loading';
    try {
      const [x, y] = await Promise.all([side(it.before), it.edited ? Promise.resolve({ text: edited[it.path]!, note: null }) : side(it.after)]);
      texts[it.path] = { a: x.text, b: y.text, note: x.note ?? y.note };
    } catch (e) {
      texts[it.path] = { a: null, b: null, note: `could not load: ${e instanceof Error ? e.message : String(e)}` };
    }
  }
</script>

<div class="tdl" data-testid={testid}>
  {#if !items.length}
    <div class="small faint pad" data-testid="{testid}-same">No file changes.</div>
  {:else}
    {#each items as it (it.path)}
      {@const t = texts[it.path]}
      <div class="it" data-testid="{testid}-item" data-path={it.path} data-kind={it.kind}>
        <button type="button" class="hd" aria-expanded={!!open[it.path]} onclick={() => toggle(it)} data-testid="{testid}-toggle">
          <Icon icon={open[it.path] ? ChevronDown : ChevronRight} size={13} />
          <span class="mono small p">{it.path}</span>
          <span class="badge {tone[it.kind]} xs">{it.kind}{it.edited ? ' (unsaved)' : ''}</span>
          {#if it.before && it.after && it.before.mode !== it.after.mode}<span class="xs faint">mode {it.before.mode} → {it.after.mode}</span>{/if}
          {#if it.before && it.after && it.before.render !== it.after.render}<span class="xs faint">render {it.before.render} → {it.after.render}</span>{/if}
          <span class="xs faint sz">{it.before ? formatContentBytes(it.before.size) : '—'} → {it.after ? (it.edited ? 'edited' : formatContentBytes(it.after.size)) : '—'}</span>
        </button>
        {#if open[it.path]}
          <div class="bd">
            {#if t === 'loading' || !t}
              <div class="small faint"><Spinner size={13} /> Loading…</div>
            {:else if t.note || t.a === null || t.b === null}
              <div class="small muted" data-testid="{testid}-note">{t.note ?? 'no text diff'} · {it.before?.sha256.slice(0, 19) ?? '—'}… → {it.after?.sha256.slice(0, 19) ?? '—'}…</div>
            {:else}
              <TextDiff a={t.a} b={t.b} {aLabel} {bLabel} testid="{testid}-text" />
            {/if}
          </div>
        {/if}
      </div>
    {/each}
  {/if}
</div>

<style>
  .tdl {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .it {
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    background: var(--surface);
  }
  .hd {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 6px 8px;
    background: transparent;
    border: 0;
    cursor: pointer;
    color: var(--text);
    text-align: left;
    flex-wrap: wrap;
  }
  .hd:hover {
    background: var(--surface-hover);
  }
  .p {
    overflow-wrap: anywhere;
    min-width: 0;
  }
  .sz {
    margin-left: auto;
  }
  .bd {
    padding: 0 8px 8px;
  }
  .pad {
    padding: 8px 0;
  }
</style>
