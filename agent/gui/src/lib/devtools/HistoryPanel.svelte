<script lang="ts">
  /** Per-connection history (§21 #97 g: devtools.query activities, redacted): mine toggle, load more, re-open. */
  import type { DevToolHistoryItem } from '@opm/shared/devtools';
  import { toasts } from '$lib/state/toast.svelte';
  import { relTime } from '$lib/format';
  import Icon from '$lib/ui/Icon.svelte';
  import { dev } from './api';

  let { site, connectionId, onopen, version = 0 }: { site: string; connectionId: string; onopen: (h: DevToolHistoryItem) => void; version?: number } = $props();
  let items = $state<DevToolHistoryItem[] | null>(null);
  let cursor = $state<string | null>(null);
  let mine = $state(false);
  let busy = $state(false);
  async function load(more = false) {
    busy = true;
    try {
      const r = await dev.history({ siteSlug: site, query: { connectionId, mine: mine || undefined, cursor: more ? (cursor ?? undefined) : undefined, limit: 50 } });
      items = more ? [...(items ?? []), ...r.items] : r.items;
      cursor = r.cursor;
    } catch (e) {
      toasts.err(e, 'History could not be loaded');
      items ??= [];
    } finally {
      busy = false;
    }
  }
  $effect(() => {
    void version;
    void mine;
    load();
  });
</script>

<div class="hp" data-testid="history-panel">
  <div class="th">
    <label class="switch small"><input type="checkbox" bind:checked={mine} data-testid="history-mine" />Only mine</label>
    <span class="grow"></span>
    <button class="btn ghost icon sm" onclick={() => load()} aria-label="Refresh history" title="Refresh"><Icon name="refresh" size={13} /></button>
  </div>
  <div class="body">
    {#if items === null}
      <div class="pad"><span class="skel" style="width:70%"></span></div>
    {:else if !items.length}
      <p class="faint small pad">No query yet{mine ? ' by you' : ''} on this connection.</p>
    {:else}
      {#each items as h (h.activityId)}
        <button class="it" onclick={() => onopen(h)} title="Open in a new tab — {h.statement.slice(0, 300)}" data-testid="history-item">
          <span class="l1">
            <i class="dot {h.ok ? 'green' : 'red'}"></i>
            <span class="op">{h.op}</span>
            <span class="faint">{relTime(h.ts)}</span>
            <span class="grow"></span>
            <span class="faint">{h.durationMs} ms{h.rows !== null ? ` · ${h.rows} rows` : ''}{h.status !== null ? ` · ${h.status}` : ''}</span>
          </span>
          <span class="st mono">{h.statement}</span>
          {#if h.error}<span class="er">{h.error}</span>{/if}
          <span class="faint by">{h.actor ?? 'system'}</span>
        </button>
      {/each}
      {#if cursor}<button class="btn ghost sm more" disabled={busy} onclick={() => load(true)}>Load more</button>{/if}
    {/if}
  </div>
</div>

<style>
  .hp {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
  }
  .th {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 6px 8px;
    border-bottom: 1px solid var(--border);
  }
  .body {
    overflow: auto;
    flex: 1;
    padding: 4px;
  }
  .pad {
    padding: 8px 10px;
    margin: 0;
  }
  .it {
    all: unset;
    box-sizing: border-box;
    width: 100%;
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 6px 8px;
    border-radius: var(--r-sm);
    cursor: pointer;
    font-size: 11.5px;
    border-bottom: 1px solid color-mix(in srgb, var(--border) 60%, transparent);
  }
  .it:hover {
    background: var(--surface-hover);
  }
  .l1 {
    display: flex;
    gap: 6px;
    align-items: center;
  }
  .dot.green {
    color: var(--green);
  }
  .dot.red {
    color: var(--red);
  }
  .op {
    font-weight: var(--fw-semibold);
    text-transform: uppercase;
    font-size: 10px;
    letter-spacing: 0.04em;
  }
  .st {
    display: -webkit-box;
    -webkit-line-clamp: 3;
    line-clamp: 3;
    -webkit-box-orient: vertical;
    overflow: hidden;
    white-space: pre-wrap;
    word-break: break-word;
    color: var(--text);
    font-size: 11.5px;
  }
  .er {
    color: var(--red-text);
  }
  .by {
    font-size: 10.5px;
  }
  .more {
    margin: 6px auto;
    display: flex;
  }
</style>
