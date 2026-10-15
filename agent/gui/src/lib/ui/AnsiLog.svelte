<script lang="ts">
  /**
   * §21 #93: a static log block (update failure tails, activity data, terminal transcripts, command output) with ANSI
   * colors. `lines` or `text` (split on newlines) = ONE stream: SGR state carried across lines, ESC-lost detected for the
   * whole block. Optional `prefix(line)` → [shown prefix, rest] (e.g. the timestamp of "<ts> <stream> <text>"), `q` =
   * search term highlighted on the visible text. The Colors / Raw toggles are global (AnsiToggles); `toggles` shows them
   * above the block.
   */
  import { ansiPrefs, lineHtml, parseLines } from '../ansi.svelte';
  import AnsiToggles from './AnsiToggles.svelte';
  import type { Snippet } from 'svelte';

  let {
    lines,
    text,
    q = '',
    toggles = false,
    empty = 'no log lines',
    testid,
    class: cls = '',
    prefix,
    head,
  }: { lines?: readonly string[]; text?: string; q?: string; toggles?: boolean; empty?: string; testid?: string; class?: string; prefix?: (raw: string) => [string, string]; head?: Snippet } = $props();

  const src = $derived(lines ?? (text ? text.replace(/\r?\n$/, '').split(/\r?\n/) : []));
  const split = $derived(src.map((l) => (prefix ? prefix(l) : (['', l] as [string, string]))));
  const parsed = $derived(parseLines(split.map((x) => x[1])));
  const mode = $derived(ansiPrefs.mode);
</script>

{#if toggles || head}<div class="ansi-bar"><span class="grow">{@render head?.()}</span>{#if toggles}<AnsiToggles small testid={testid ? `${testid}-ansi` : 'ansi'} />{/if}</div>{/if}
<pre class="logbox ansi {cls}" data-testid={testid}>{#each parsed as p, i (i)}{#if split[i][0]}<span class="ts">{split[i][0]}</span>{/if}<span class="aln">{@html lineHtml(p, mode, q)}</span>{'\n'}{:else}<span class="ts">{empty}</span>{/each}</pre>

<style>
  .ansi-bar {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
    margin-bottom: 4px;
  }
  .ts {
    color: var(--ansi-8);
  }
</style>
