<script lang="ts">
  /**
   * Redis key browser (§21 #97): SCAN with MATCH pattern + TYPE filter + "Load more" (cursor), tree split on ":"
   * (lazy folders) or a flat list with type / TTL / size columns.
   */
  import type { RedisKeyRow } from '@opm/shared/devtools';
  import { bytes } from '$lib/format';
  import { toasts } from '$lib/state/toast.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Select from '$lib/ui/Select.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import { dev } from '../api';
  import { loadJson, saveJson } from '../kinds';
  import { TYPE_SHORT, TYPE_TONE, keyTree, sortedChildren, ttlText, type KeyNode } from './redis';

  interface Props {
    site: string;
    connectionId: string;
    selected: string | null;
    onselect: (k: RedisKeyRow) => void;
    onnew: () => void;
    /** bump to rescan */
    version?: number;
  }
  let { site, connectionId, selected, onselect, onnew, version = 0 }: Props = $props();
  let pattern = $state('*');
  let type = $state<string>('');
  type Mode = 'tree' | 'list';
  let mode = $state<Mode>(loadJson<Mode>('opm.dev.redis.view', 'tree'));
  let keys = $state<RedisKeyRow[]>([]);
  let cursor = $state('0');
  let dbSize = $state<number | null>(null);
  let scanned = $state(0);
  let busy = $state(false);
  let err = $state<string | null>(null);
  async function scan(more = false) {
    busy = true;
    err = null;
    try {
      const r = await dev.redisScan({ siteSlug: site, connectionId, query: { pattern: pattern || '*', cursor: more ? cursor : '0', count: 200, type: type || undefined } });
      keys = more ? [...keys, ...r.keys.filter((k) => !keys.some((x) => x.key === k.key))] : r.keys;
      cursor = r.cursor;
      dbSize = r.dbSize;
      scanned = (more ? scanned : 0) + r.scanned;
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
      toasts.err(e, 'SCAN failed');
    } finally {
      busy = false;
    }
  }
  $effect(() => {
    void version;
    scan();
  });
  const tree = $derived(keyTree(keys));
  let open = $state<Record<string, boolean>>({});
  const typeOpts = [{ value: '', label: 'All types' }, ...['string', 'hash', 'list', 'set', 'zset', 'stream'].map((t) => ({ value: t, label: t }))];
  function setMode(m: Mode) {
    mode = m;
    saveJson('opm.dev.redis.view', m);
  }
</script>

{#snippet node(n: KeyNode, depth: number)}
  {#each sortedChildren(n) as c (c.key ? `k:${c.path}` : `f:${c.path}`)}
    {#if c.key}
      {@const k = c.key}
      <button class="kn leaf" class:on={selected === k.key} style:padding-left="{8 + depth * 14}px" onclick={() => onselect(k)} title={k.key} data-testid="rk-{k.key}">
        <span class="ty {TYPE_TONE[k.type] ?? 'gray'}">{TYPE_SHORT[k.type] ?? k.type}</span>
        <span class="nm mono">{c.name}</span>
        {#if k.ttlSec >= 0}<span class="ttl" title="TTL {ttlText(k.ttlSec)}"><Icon name="timer" size={11} /></span>{/if}
      </button>
    {:else}
      {@const o = open[c.path] ?? false}
      <button class="kn folder" style:padding-left="{8 + depth * 14}px" onclick={() => (open = { ...open, [c.path]: !o })} aria-expanded={o} data-testid="rf-{c.path}">
        <Icon name={o ? 'chevronDown' : 'chevronRight'} size={12} />
        <Icon name={o ? 'folderOpen' : 'folder'} size={13} />
        <span class="nm mono">{c.name}</span>
        <span class="cnt">{c.count}</span>
      </button>
      {#if o}{@render node(c, depth + 1)}{/if}
    {/if}
  {/each}
{/snippet}

<div class="kb" data-testid="redis-keys">
  <div class="kh">
    <div class="srch">
      <input class="input sm mono grow" bind:value={pattern} placeholder="pattern, e.g. user:*" onkeydown={(e) => e.key === 'Enter' && scan()} aria-label="Key pattern (SCAN MATCH)" data-testid="redis-pattern" />
      <div class="tsel"><Select value={type} options={typeOpts} onchange={(v) => ((type = v), scan())} size="sm" ariaLabel="Type filter" testid="redis-type" /></div>
      <Btn size="sm" icon="search" busy={busy} onclick={() => scan()} data-testid="redis-scan">Scan</Btn>
    </div>
    <div class="row2">
      <SegmentedControl options={[{ value: 'tree' as Mode, label: 'Tree' }, { value: 'list' as Mode, label: 'List' }]} value={mode} onchange={setMode} label="Key view" size="sm" testid="redis-view" />
      <span class="faint xs grow" data-testid="redis-count">{keys.length.toLocaleString()} loaded · {scanned.toLocaleString()} scanned{dbSize !== null ? ` of ${dbSize.toLocaleString()}` : ''}</span>
      <Btn size="sm" variant="ghost" icon="plus" onclick={onnew} data-testid="redis-new">Key</Btn>
    </div>
  </div>
  <div class="kl">
    {#if err}
      <p class="bad-text small pad">{err}</p>
    {:else if !keys.length && !busy}
      <p class="faint small pad">No key matches <span class="mono">{pattern}</span>{type ? ` of type ${type}` : ''}{cursor !== '0' ? ' in this part of the keyspace — Load more scans further.' : '.'}</p>
    {/if}
    {#if mode === 'tree'}
      {@render node(tree, 0)}
    {:else}
      <table class="klist">
        <thead><tr><th>Key</th><th>Type</th><th>TTL</th><th class="r">Size</th></tr></thead>
        <tbody>
          {#each keys as k (k.key)}
            <tr class:on={selected === k.key} onclick={() => onselect(k)} data-testid="rl-{k.key}">
              <td class="mono kk" title={k.key}>{k.key}</td>
              <td><span class="ty {TYPE_TONE[k.type] ?? 'gray'}">{TYPE_SHORT[k.type] ?? k.type}</span></td>
              <td class="xs nowrap">{k.ttlSec === -1 ? '∞' : ttlText(k.ttlSec)}</td>
              <td class="xs r nowrap">{k.sizeBytes !== null ? bytes(k.sizeBytes) : '—'}{k.length !== null && k.type !== 'string' ? ` · ${k.length}` : ''}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
    {#if cursor !== '0'}
      <div class="more"><Btn size="sm" variant="ghost" icon="arrowDown" busy={busy} onclick={() => scan(true)} data-testid="redis-more">Load more (SCAN cursor {cursor})</Btn></div>
    {/if}
  </div>
</div>

<style>
  .kb {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
  }
  .kh {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 8px;
    border-bottom: 1px solid var(--border);
  }
  .srch,
  .row2 {
    display: flex;
    gap: 6px;
    align-items: center;
    min-width: 0;
  }
  .srch .input {
    height: 28px;
    min-width: 0;
  }
  .tsel {
    width: 118px;
    flex: none;
  }
  .kl {
    overflow: auto;
    flex: 1;
    min-height: 0;
    padding: 4px;
  }
  .pad {
    padding: 8px;
    margin: 0;
  }
  .kn {
    all: unset;
    box-sizing: border-box;
    display: flex;
    align-items: center;
    gap: 6px;
    width: 100%;
    padding: 3px 8px;
    border-radius: var(--r-sm);
    cursor: pointer;
    font-size: 12.5px;
    min-width: 0;
  }
  .kn:hover {
    background: var(--surface-hover);
  }
  .kn.on,
  tr.on td {
    background: var(--accent-soft);
  }
  .kn:focus-visible {
    box-shadow: var(--focus-ring);
  }
  .nm {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
  }
  .cnt {
    margin-left: auto;
    color: var(--text-3);
    font-size: 11px;
  }
  .ttl {
    margin-left: auto;
    color: var(--amber-text);
    display: inline-flex;
  }
  .ty {
    font-size: 9.5px;
    font-weight: 700;
    letter-spacing: 0.03em;
    padding: 1px 4px;
    border-radius: 3px;
    width: 34px;
    text-align: center;
    flex: none;
    font-family: var(--font-sans);
  }
  .ty.blue {
    background: var(--blue-soft);
    color: var(--blue-text);
  }
  .ty.violet {
    background: var(--violet-soft);
    color: var(--violet-text);
  }
  .ty.green {
    background: var(--green-soft);
    color: var(--green-text);
  }
  .ty.amber {
    background: var(--amber-soft);
    color: var(--amber-text);
  }
  .ty.red {
    background: var(--red-soft);
    color: var(--red-text);
  }
  .ty.accent,
  .ty.gray {
    background: var(--accent-soft);
    color: var(--accent-text);
  }
  .klist {
    width: 100%;
    border-collapse: collapse;
    font-size: 12px;
    table-layout: fixed;
  }
  .klist th {
    text-align: left;
    font-weight: var(--fw-medium);
    color: var(--text-3);
    font-size: 11px;
    padding: 4px 6px;
    position: sticky;
    top: -4px;
    background: var(--surface);
  }
  .klist th:first-child {
    width: 52%;
  }
  .klist td {
    padding: 3px 6px;
    border-top: 1px solid color-mix(in srgb, var(--border) 60%, transparent);
    cursor: pointer;
  }
  .klist tr:hover td {
    background: var(--surface-hover);
  }
  .kk {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .r {
    text-align: right;
  }
  .more {
    display: flex;
    justify-content: center;
    padding: 8px;
  }
</style>
