<script lang="ts">
  /**
   * Line diff of two texts (unified view, console diff colors: removed red, added green), unchanged runs collapsed to
   * 3 lines of context. LCS on lines (texts up to 4000 lines each; larger → a summary). Used by Template sync to
   * compare file contents (base / template / yours).
   */
  let { a, b, aLabel = 'before', bLabel = 'after', testid = 'text-diff' }: { a: string; b: string; aLabel?: string; bLabel?: string; testid?: string } = $props();

  type Line = { t: ' ' | '-' | '+'; s: string; an: number | null; bn: number | null };
  type Row = Line | { t: 'gap'; n: number };
  const MAX = 4000;

  function diffLines(x: string[], y: string[]): Line[] {
    const n = x.length;
    const m = y.length;
    // trim common prefix / suffix first (cheap and keeps the table small)
    let p = 0;
    while (p < n && p < m && x[p] === y[p]) p++;
    let q = 0;
    while (q < n - p && q < m - p && x[n - 1 - q] === y[m - 1 - q]) q++;
    const xs = x.slice(p, n - q);
    const ys = y.slice(p, m - q);
    const W = ys.length + 1;
    const L = new Uint32Array((xs.length + 1) * W);
    for (let i = xs.length - 1; i >= 0; i--) for (let j = ys.length - 1; j >= 0; j--) L[i * W + j] = xs[i] === ys[j] ? L[(i + 1) * W + j + 1]! + 1 : Math.max(L[(i + 1) * W + j]!, L[i * W + j + 1]!);
    const out: Line[] = [];
    for (let k = 0; k < p; k++) out.push({ t: ' ', s: x[k]!, an: k + 1, bn: k + 1 });
    let i = 0;
    let j = 0;
    while (i < xs.length || j < ys.length) {
      if (i < xs.length && j < ys.length && xs[i] === ys[j]) {
        out.push({ t: ' ', s: xs[i]!, an: p + i + 1, bn: p + j + 1 });
        i++;
        j++;
      } else if (j < ys.length && (i >= xs.length || L[i * W + j + 1]! >= L[(i + 1) * W + j]!)) {
        out.push({ t: '+', s: ys[j]!, an: null, bn: p + j + 1 });
        j++;
      } else {
        out.push({ t: '-', s: xs[i]!, an: p + i + 1, bn: null });
        i++;
      }
    }
    for (let k = 0; k < q; k++) out.push({ t: ' ', s: x[n - q + k]!, an: n - q + k + 1, bn: m - q + k + 1 });
    return out;
  }
  function collapse(lines: Line[], ctx = 3): Row[] {
    const keep = lines.map(() => false);
    lines.forEach((l, k) => {
      if (l.t !== ' ') for (let d = -ctx; d <= ctx; d++) if (k + d >= 0 && k + d < lines.length) keep[k + d] = true;
    });
    const rows: Row[] = [];
    let gap = 0;
    lines.forEach((l, k) => {
      if (keep[k]) {
        if (gap) rows.push({ t: 'gap', n: gap });
        gap = 0;
        rows.push(l);
      } else gap++;
    });
    if (gap) rows.push({ t: 'gap', n: gap });
    return rows;
  }
  const al = $derived(a.split('\n'));
  const bl = $derived(b.split('\n'));
  const tooBig = $derived(al.length > MAX || bl.length > MAX);
  const lines = $derived(tooBig ? [] : diffLines(al, bl));
  const rows = $derived(collapse(lines));
  const added = $derived(lines.filter((l) => l.t === '+').length);
  const removed = $derived(lines.filter((l) => l.t === '-').length);
</script>

<div class="td" data-testid={testid}>
  <div class="hd xs">
    <span class="mono">− {aLabel}</span><span class="mono">+ {bLabel}</span>
    <span class="spacer"></span>
    {#if !tooBig}<span class="text-red">−{removed}</span><span class="text-green">+{added}</span>{/if}
  </div>
  {#if tooBig}
    <div class="small muted pad">Too large to compare line by line ({al.length} / {bl.length} lines).</div>
  {:else if !added && !removed}
    <div class="small muted pad">Identical content.</div>
  {:else}
    <div class="body">
      {#each rows as r, k (k)}
        {#if r.t === 'gap'}
          <div class="gap xs">⋯ {r.n} unchanged line{r.n === 1 ? '' : 's'}</div>
        {:else}
          <div class="ln {r.t === '+' ? 'add' : r.t === '-' ? 'del' : ''}" data-testid={r.t === '+' ? 'diff-line-add' : r.t === '-' ? 'diff-line-del' : undefined}>
            <span class="n">{r.an ?? ''}</span><span class="n">{r.bn ?? ''}</span><span class="m">{r.t === ' ' ? '' : r.t}</span><span class="s">{r.s || ' '}</span>
          </div>
        {/if}
      {/each}
    </div>
  {/if}
</div>

<style>
  .td {
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    overflow: hidden;
    background: var(--code-bg);
    min-width: 0;
  }
  .hd {
    display: flex;
    gap: 10px;
    padding: 6px 10px;
    border-bottom: 1px solid var(--border);
    background: var(--surface-2);
    color: var(--text-2);
  }
  .spacer {
    flex: 1;
  }
  .body {
    max-height: 360px;
    overflow: auto;
    font-family: var(--font-mono);
    font-size: 12px;
    line-height: 1.55;
  }
  .ln {
    display: grid;
    grid-template-columns: 36px 36px 14px 1fr;
    white-space: pre;
  }
  .ln.add {
    background: var(--green-soft);
  }
  .ln.del {
    background: var(--red-soft);
  }
  .n {
    color: var(--text-3);
    text-align: right;
    padding-right: 6px;
    user-select: none;
  }
  .m {
    color: var(--text-2);
    user-select: none;
  }
  .add .m,
  .add .s {
    color: var(--green-text);
  }
  .del .m,
  .del .s {
    color: var(--red-text);
  }
  .gap {
    padding: 2px 10px;
    color: var(--text-3);
    background: var(--surface-2);
  }
  .pad {
    padding: 10px;
  }
</style>
