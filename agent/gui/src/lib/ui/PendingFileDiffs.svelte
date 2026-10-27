<script lang="ts">
  /**
   * §21 #107 Review: the per-file content changes of the change set as text diffs (head content ↔ the new bytes kept in
   * this browser). Binary / unreadable content shows sizes only.
   */
  import TextDiff from '$opm/branch/TextDiff.svelte';
  import { looksLikeText } from '@opm/shared/volume-content';
  import { fetchText } from '../api';
  import { draft } from '../state/draft.svelte';
  import { bytes } from '../format';

  const rows = $derived.by(() => {
    void draft.blobsTick;
    const out: { slug: string; before: { sha: string; size: number } | null; after: { sha: string; size: number } | null; text: string | null }[] = [];
    for (const f of draft.work?.files ?? []) {
      const h = draft.base?.config.files.find((x) => x.fileSlug === f.fileSlug)?.content ?? null;
      if (JSON.stringify(h) === JSON.stringify(f.content ?? null)) continue;
      let text: string | null = draft.fileText[f.fileSlug] ?? null;
      if (text === null && f.content) {
        const b = draft.bytesOf(f.content.sha256);
        if (b && looksLikeText(b)) text = new TextDecoder().decode(b);
      }
      out.push({ slug: f.fileSlug, before: h ? { sha: h.sha256, size: h.sizeBytes } : null, after: f.content ? { sha: f.content.sha256, size: f.content.sizeBytes } : null, text });
    }
    return out;
  });
  let heads = $state<Record<string, string | null>>({});
  $effect(() => {
    for (const r of rows) {
      if (!r.before || r.text === null || r.slug in heads) continue;
      heads[r.slug] = null;
      fetchText(...draft.fileContentPath(r.slug))
        .then((t) => (heads[r.slug] = t))
        .catch(() => undefined);
    }
  });
</script>

{#if rows.length}
  <section class="stack tight" data-testid="pending-file-diffs">
    <h4 class="small strong m0">File contents</h4>
    {#each rows as r (r.slug)}
      <div class="fd" data-testid="pending-file-diff-{r.slug}">
        <div class="small"><span class="mono strong">{r.slug}</span> <span class="muted tiny">{r.before ? bytes(r.before.size) : 'no content'} → {r.after ? bytes(r.after.size) : 'no content'} · kept in this browser, uploaded with the commit</span></div>
        {#if r.text !== null && (!r.before || heads[r.slug] !== null && heads[r.slug] !== undefined)}
          <TextDiff before={r.before ? (heads[r.slug] ?? '') : ''} after={r.text} testid="pending-text-diff-{r.slug}" />
        {:else if r.text === null}
          <div class="tiny muted">Binary content — no text diff.</div>
        {:else}
          <div class="tiny muted">Loading the committed content…</div>
        {/if}
      </div>
    {/each}
  </section>
{/if}

<style>
  .m0 {
    margin: 0;
  }
  .fd {
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    padding: 8px 10px;
    min-width: 0;
    overflow: auto;
  }
</style>
