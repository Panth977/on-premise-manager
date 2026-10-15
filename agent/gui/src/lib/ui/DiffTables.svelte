<script lang="ts">
  /** Config changes per section (variables · secrets · files · sites · volumes · containers · edge · dev tools) — head → new.
   *  Every DiffSection has a tab (§21 #102 fix: dev-tool preset changes were invisible in review / Diff / rollback). */
  import type { DiffChange, DiffSection } from '@opm/shared/types';
  import { showValue, jsonPretty } from '../format';
  let { changes, beforeLabel = 'Head', afterLabel = 'New', empty = 'No changes.' }: { changes: DiffChange[]; beforeLabel?: string; afterLabel?: string; empty?: string } = $props();
  const SECTIONS: DiffSection[] = ['variables', 'secrets', 'files', 'sites', 'volumes', 'containers', 'edge', 'devTools'];
  const LABEL: Partial<Record<DiffSection, string>> = { devTools: 'dev tools' };
  const counts = $derived(Object.fromEntries(SECTIONS.map((s) => [s, changes.filter((c) => c.section === s).length])) as Record<DiffSection, number>);
  let tab = $state<DiffSection | null>(null);
  const active = $derived(tab && counts[tab] ? tab : (SECTIONS.find((s) => counts[s] > 0) ?? 'variables'));
  const rows = $derived(changes.filter((c) => c.section === active));
  const kindTone = { added: 'ok', removed: 'bad', changed: 'info' } as const;
  const isObj = (v: unknown) => typeof v === 'object' && v !== null && !(Array.isArray(v) && v.every((x) => typeof x === 'string'));
</script>

{#if changes.length === 0}
  <p class="muted small" data-testid="no-changes">{empty}</p>
{:else}
  <div class="diff" data-testid="diff">
    <div class="tabs" role="tablist" aria-label="Changed sections">
      {#each SECTIONS as s (s)}
        <button role="tab" aria-selected={active === s} disabled={counts[s] === 0} onclick={() => (tab = s)} data-testid="diff-tab-{s}">{LABEL[s] ?? s}<span class="count">{counts[s]}</span></button>
      {/each}
    </div>
    <div class="table-wrap">
      <table class="table">
        <thead><tr><th>Path</th><th>{beforeLabel}</th><th>{afterLabel}</th><th>Kind</th></tr></thead>
        <tbody>
          {#each rows as c, i (i)}
            <tr>
              <td class="mono path">{c.path}{#if c.note}<div class="note" data-testid="diff-note">{c.note}</div>{/if}</td>
              {#each [c.before, c.after] as v, k (k)}
                <td class="val" class:del={k === 0 && c.kind !== 'added'} class:add={k === 1 && c.kind !== 'removed'}>
                  {#if isObj(v)}
                    <details><summary class="mono">{showValue(v).slice(0, 60)}{showValue(v).length > 60 ? '…' : ''}</summary><pre class="code">{jsonPretty(v)}</pre></details>
                  {:else}<span class="mono">{showValue(v)}</span>{/if}
                </td>
              {/each}
              <td><span class="badge {kindTone[c.kind]}">{c.kind}</span></td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  </div>
{/if}

<style>
  .note {
    font-family: inherit;
    font-size: 11.5px;
    color: var(--amber-text);
    margin-top: 2px;
  }
  .diff {
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    overflow: hidden;
  }
  .tabs {
    padding: 0 8px;
    background: var(--surface-2);
  }
  .tabs button:disabled {
    opacity: 0.45;
    cursor: default;
  }
  .path {
    font-size: 12px;
    white-space: nowrap;
  }
  .val {
    max-width: 340px;
    overflow-wrap: anywhere;
    font-size: 12px;
  }
  .val.del span {
    color: var(--red-text);
  }
  .val.add span {
    color: var(--green-text);
  }
  details summary {
    cursor: pointer;
    font-size: 12px;
  }
  pre.code {
    max-height: 240px;
    margin-top: 6px;
    white-space: pre-wrap;
  }
</style>
