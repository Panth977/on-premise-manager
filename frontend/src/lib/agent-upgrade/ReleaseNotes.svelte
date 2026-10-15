<script lang="ts">
  /** §21 #90 release notes of the pinned agent release (plain text / light markdown) as a disclosure */
  let { notes, version, open = false, summary = "What's new", testid = 'release-notes' }: { notes: string | null | undefined; version: string; open?: boolean; summary?: string; testid?: string } = $props();
  /** light markdown: "- " / "* " lines = bullets, "# " headings, the rest paragraphs */
  type Block = { kind: 'h' | 'p'; text: string } | { kind: 'ul'; items: string[] };
  const blocks = $derived.by<Block[]>(() => {
    const out: Block[] = [];
    for (const raw of (notes ?? '').split(/\r?\n/)) {
      const line = raw.trimEnd();
      if (!line.trim()) continue;
      const li = /^\s*[-*]\s+(.*)$/.exec(line);
      if (li) {
        const last = out[out.length - 1];
        if (last?.kind === 'ul') last.items.push(li[1]!);
        else out.push({ kind: 'ul', items: [li[1]!] });
        continue;
      }
      const h = /^#{1,6}\s+(.*)$/.exec(line);
      out.push(h ? { kind: 'h', text: h[1]! } : { kind: 'p', text: line.trim() });
    }
    return out;
  });
</script>

{#if notes && notes.trim()}
  <details class="rn" {open} data-testid={testid}>
    <summary class="small strong">{summary} <span class="faint mono xs">{version}</span></summary>
    <div class="body small" data-testid="{testid}-body">
      {#each blocks as b, i (i)}
        {#if b.kind === 'ul'}
          <ul>{#each b.items as it, j (j)}<li>{it}</li>{/each}</ul>
        {:else if b.kind === 'h'}
          <div class="strong">{b.text}</div>
        {:else}
          <p>{b.text}</p>
        {/if}
      {/each}
    </div>
  </details>
{/if}

<style>
  .rn summary {
    cursor: pointer;
    user-select: none;
    color: var(--text);
    padding: 2px 0;
  }
  .body {
    margin-top: 6px;
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    background: var(--surface-2);
    color: var(--text-2);
    display: flex;
    flex-direction: column;
    gap: 6px;
    max-height: 280px;
    overflow: auto;
    overflow-wrap: anywhere;
  }
  .body p,
  .body ul {
    margin: 0;
  }
  .body ul {
    padding-left: 18px;
  }
</style>
