<script lang="ts">
  /** Redis INFO (§21 #97): headline numbers, keyspace per db (the connection's db marked), sections, SLOWLOG. */
  import type { RedisInfoResponse } from '@opm/shared/devtools';
  import { errMessage } from '$lib/api';
  import Icon from '$lib/ui/Icon.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import { relTime } from '$lib/format';
  import { dev } from '../api';

  let { site, connectionId, db }: { site: string; connectionId: string; db: string } = $props();
  let info = $state<RedisInfoResponse | null>(null);
  let err = $state<string | null>(null);
  let busy = $state(false);
  let open = $state<Record<string, boolean>>({ server: true, memory: true });
  async function load() {
    busy = true;
    try {
      info = await dev.redisInfo({ siteSlug: site, connectionId });
      err = null;
    } catch (e) {
      err = errMessage(e);
    } finally {
      busy = false;
    }
  }
  $effect(() => {
    void connectionId;
    load();
  });
  const s = $derived(info?.sections ?? {});
  const tiles = $derived(
    info
      ? [
          ['Version', info.serverVersion ?? '—'],
          ['Uptime', s.server?.uptime_in_days ? `${s.server.uptime_in_days} d` : '—'],
          ['Memory', s.memory?.used_memory_human ?? '—'],
          ['Max memory', `${s.memory?.maxmemory_human ?? '—'}${s.memory?.maxmemory_policy ? ` · ${s.memory.maxmemory_policy}` : ''}`],
          ['Clients', s.clients?.connected_clients ?? '—'],
          ['Ops / s', s.stats?.instantaneous_ops_per_sec ?? '—'],
          ['Hit rate', s.stats?.keyspace_hits ? `${Math.round((Number(s.stats.keyspace_hits) / Math.max(1, Number(s.stats.keyspace_hits) + Number(s.stats.keyspace_misses ?? 0))) * 1000) / 10} %` : '—'],
          ['Role', s.replication?.role ?? '—'],
        ]
      : [],
  );
</script>

<div class="ri" data-testid="redis-info">
  <div class="bar"><span class="strong">Server</span><span class="grow"></span><Btn size="xs" variant="ghost" icon="refresh" busy={busy} onclick={load}>Refresh</Btn></div>
  {#if err}<div class="banner bad"><Icon name="alert" /><div class="grow">{err}</div></div>{/if}
  {#if info}
    <div class="tiles">
      {#each tiles as [k, v] (k)}<div class="tile"><span class="faint xs">{k}</span><span class="strong mono">{v}</span></div>{/each}
    </div>
    <h4>Keyspace</h4>
    <table class="table compact" data-testid="redis-dbs">
      <thead><tr><th>db</th><th class="r">keys</th><th class="r">with expiry</th><th></th></tr></thead>
      <tbody>
        {#each info.dbs as d (d.db)}
          <tr class:cur={String(d.db) === db}><td class="mono">db{d.db}</td><td class="r">{d.keys.toLocaleString()}</td><td class="r">{d.expires.toLocaleString()}</td><td>{#if String(d.db) === db}<span class="badge accent">this connection</span>{/if}</td></tr>
        {/each}
      </tbody>
    </table>
    <h4>Slow log <span class="faint xs">(SLOWLOG GET 20)</span></h4>
    {#if info.slowlog.length}
      <table class="table compact" data-testid="redis-slowlog">
        <thead><tr><th>#</th><th>When</th><th class="r">Duration</th><th>Command</th></tr></thead>
        <tbody>
          {#each info.slowlog as l (l.id)}
            <tr><td class="faint">{l.id}</td><td class="nowrap">{relTime(l.at)}</td><td class="r mono">{(l.durationUs / 1000).toFixed(1)} ms</td><td class="mono cmd">{l.command}</td></tr>
          {/each}
        </tbody>
      </table>
    {:else}
      <p class="faint small">No slow commands.</p>
    {/if}
    <h4>INFO sections</h4>
    {#each Object.entries(info.sections) as [name, kv] (name)}
      <details class="sec" open={open[name] ?? false} ontoggle={(e) => (open = { ...open, [name]: e.currentTarget.open })}>
        <summary>{name} <span class="faint xs">{Object.keys(kv).length}</span></summary>
        <div class="kvs">
          {#each Object.entries(kv) as [k, v] (k)}<span class="faint mono">{k}</span><span class="mono">{v}</span>{/each}
        </div>
      </details>
    {/each}
  {:else if !err}
    <span class="skel" style="width:100%;height:200px"></span>
  {/if}
</div>

<style>
  .ri {
    padding: 10px 12px;
    overflow: auto;
    height: 100%;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .bar {
    display: flex;
    align-items: center;
  }
  .tiles {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
    gap: 8px;
  }
  .tile {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 8px 10px;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    background: var(--surface-2);
    min-width: 0;
  }
  .tile .strong {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  h4 {
    margin: 8px 0 0;
    font-size: var(--fs-sm);
  }
  .compact :global(td),
  .compact :global(th) {
    padding: 4px 10px;
    font-size: 12.5px;
  }
  tr.cur td {
    background: var(--accent-soft);
  }
  .r {
    text-align: right;
  }
  .cmd {
    word-break: break-all;
  }
  .sec {
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    padding: 6px 10px;
  }
  .sec summary {
    cursor: pointer;
    font-weight: var(--fw-medium);
    text-transform: capitalize;
  }
  .kvs {
    display: grid;
    grid-template-columns: minmax(160px, auto) 1fr;
    gap: 2px 14px;
    font-size: 12px;
    margin-top: 6px;
    word-break: break-all;
  }
</style>
