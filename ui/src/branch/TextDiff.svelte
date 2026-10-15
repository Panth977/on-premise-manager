<script lang="ts" module>
  export type DiffLine = { t: 'same' | 'add' | 'del'; text: string; a: number | null; b: number | null };
  /** Line diff (LCS; inputs above 4000 lines are compared line by line without alignment). */
  export function lineDiff(before: string, after: string): DiffLine[] {
    const A = before.split('\n');
    const B = after.split('\n');
    if (A.length * B.length > 16_000_000) {
      const out: DiffLine[] = [];
      const n = Math.max(A.length, B.length);
      for (let i = 0; i < n; i++) {
        if (A[i] === B[i]) out.push({ t: 'same', text: A[i], a: i + 1, b: i + 1 });
        else {
          if (i < A.length) out.push({ t: 'del', text: A[i], a: i + 1, b: null });
          if (i < B.length) out.push({ t: 'add', text: B[i], a: null, b: i + 1 });
        }
      }
      return out;
    }
    const m = A.length;
    const n = B.length;
    const L: Uint32Array[] = Array.from({ length: m + 1 }, () => new Uint32Array(n + 1));
    for (let i = m - 1; i >= 0; i--) for (let j = n - 1; j >= 0; j--) L[i][j] = A[i] === B[j] ? L[i + 1][j + 1] + 1 : Math.max(L[i + 1][j], L[i][j + 1]);
    const out: DiffLine[] = [];
    let i = 0;
    let j = 0;
    while (i < m && j < n) {
      if (A[i] === B[j]) out.push({ t: 'same', text: A[i++], a: i, b: ++j });
      else if (L[i + 1][j] >= L[i][j + 1]) out.push({ t: 'del', text: A[i++], a: i, b: null });
      else out.push({ t: 'add', text: B[j++], a: null, b: j });
    }
    while (i < m) out.push({ t: 'del', text: A[i++], a: i, b: null });
    while (j < n) out.push({ t: 'add', text: B[j++], a: null, b: j });
    return out;
  }
</script>

<script lang="ts">
  /** Unified line diff of two texts (file contents, volume content entries); unchanged runs collapse to `context` lines. */
  let { before, after, context = 3, testid = 'text-diff' }: { before: string; after: string; context?: number; testid?: string } = $props();
  const lines = $derived(lineDiff(before, after));
  const shownLines = $derived.by(() => {
    const keep = new Set<number>();
    lines.forEach((l, i) => {
      if (l.t !== 'same') for (let k = Math.max(0, i - context); k <= Math.min(lines.length - 1, i + context); k++) keep.add(k);
    });
    const out: (DiffLine | { gap: number })[] = [];
    let gap = 0;
    lines.forEach((l, i) => {
      if (keep.has(i)) {
        if (gap) out.push({ gap });
        gap = 0;
        out.push(l);
      } else gap++;
    });
    if (gap) out.push({ gap });
    return out;
  });
  const changed = $derived(lines.some((l) => l.t !== 'same'));
</script>

<div class="td" data-testid={testid}>
  {#if !changed}
    <p class="muted small pad">No text changes.</p>
  {:else}
    {#each shownLines as l, k (k)}
      {#if 'gap' in l}
        <div class="gap">⋯ {l.gap} unchanged line{l.gap === 1 ? '' : 's'}</div>
      {:else}
        <div class="ln {l.t}"><span class="no">{l.a ?? ''}</span><span class="no">{l.b ?? ''}</span><span class="sg">{l.t === 'add' ? '+' : l.t === 'del' ? '−' : ' '}</span><span class="tx">{l.text}</span></div>
      {/if}
    {/each}
  {/if}
</div>

<style>
  .td {
    font-family: var(--font-mono);
    font-size: 12px;
    background: var(--code-bg);
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    overflow: auto;
    max-height: 420px;
  }
  .pad {
    padding: 8px 10px;
    margin: 0;
  }
  .ln {
    display: grid;
    grid-template-columns: 3.2em 3.2em 1.2em 1fr;
    white-space: pre;
  }
  .ln.add {
    background: color-mix(in srgb, var(--green-soft) 70%, transparent);
  }
  .ln.del {
    background: color-mix(in srgb, var(--red-soft) 70%, transparent);
  }
  .no {
    color: var(--text-muted, #888);
    text-align: right;
    padding-right: 6px;
    user-select: none;
  }
  .sg {
    user-select: none;
  }
  .gap {
    color: var(--text-muted, #888);
    padding: 2px 10px;
    background: var(--bg-sunken);
  }
</style>
