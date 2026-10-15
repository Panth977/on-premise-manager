<script lang="ts">
  /**
   * §21 #112 "Agent process": what the agent itself uses on the server (not Docker, not the containers) — memory, CPU,
   * goroutines, Docker API calls (and whether the shared Docker view is live), store health, its own disk use and
   * warnings. Polls GET /_opm/api/sites/{site}/agent/process every 15 s (relayed for another site).
   */
  import { onDestroy, onMount } from 'svelte';
  import type { AgentProcessStats } from '@opm/shared/types';
  import { api, errMessage } from '$lib/api';
  import { bytes } from '$lib/tools/fmt';
  import Icon from '$lib/ui/Icon.svelte';

  let { siteSlug }: { siteSlug: string } = $props();
  let s = $state<AgentProcessStats | null>(null);
  let err = $state<string | null>(null);
  let timer: ReturnType<typeof setInterval> | null = null;
  async function load() {
    try {
      s = await api.agentProcess({ siteSlug });
      err = null;
    } catch (e) {
      err = errMessage(e);
    }
  }
  onMount(() => {
    void load();
    timer = setInterval(() => !document.hidden && void load(), 15_000);
  });
  onDestroy(() => timer && clearInterval(timer));
  const pct = (v: number | null) => (v === null ? '—' : `${v.toFixed(1)} %`);
  const busiest = $derived(
    (s?.loops ?? [])
      .filter((l) => l.lastMs !== undefined)
      .sort((a, b) => (b.lastMs ?? 0) - (a.lastMs ?? 0))
      .slice(0, 3),
  );
</script>

<section class="card pc" data-testid="card-agent-process">
  <div class="card-head"><h2><Icon name="cpu" size={15} />Agent process</h2><span class="small muted">the agent itself — not Docker, not the containers · every 15 s</span></div>
  <div class="card-body">
    {#if err && !s}
      <div class="banner bad small"><Icon name="alert" size={14} /><div class="grow">{err}</div></div>
    {:else if !s}
      <span class="skel" style="height:90px"></span>
    {:else}
      {#if s.warnings.length}
        <div class="stack tight" data-testid="agent-process-warnings">
          {#each s.warnings as w (w)}<div class="banner warn small"><Icon name="alert" size={14} /><div class="grow">{w}</div></div>{/each}
        </div>
      {/if}
      <div class="grid">
        <div class="m"><span class="lbl">Memory (RSS)</span><span class="v" data-testid="ap-rss">{s.rssBytes === null ? '—' : bytes(s.rssBytes)}</span><span class="sub">Go heap {bytes(s.heapInUseBytes)} · limit {bytes(s.memLimitBytes)}</span></div>
        <div class="m"><span class="lbl">CPU</span><span class="v" data-testid="ap-cpu">{pct(s.cpuPct)}</span><span class="sub">of one core · {s.threads ?? '—'} threads · GC {s.gcPauseLastMs.toFixed(2)} ms</span></div>
        <div class="m"><span class="lbl">Goroutines</span><span class="v" data-testid="ap-goroutines">{s.goroutines}</span><span class="sub">{s.openFds ?? '—'} open files</span></div>
        <div class="m"><span class="lbl">Docker API</span><span class="v" data-testid="ap-docker">{s.dockerCallsPerMin === null ? '…' : `${Math.round(s.dockerCallsPerMin)}/min`}</span><span class="sub">{#if s.dockerCacheLive}<span class="ok-text">shared view live</span> · {s.dockerCacheHits} served from it{:else}<span class="warn-text">events stream down — every read asks Docker</span>{/if}</span></div>
        <div class="m"><span class="lbl">Store (opm.db)</span><span class="v" data-testid="ap-store">{s.store.ok ? 'ok' : 'failing'}</span><span class="sub">{bytes(s.disk.opmDbBytes)} · metrics {bytes(s.disk.metricsDbBytes)}</span></div>
        <div class="m"><span class="lbl">Own files</span><span class="v" data-testid="ap-disk">{bytes(s.disk.snapshotsBytes + s.disk.blobsBytes + s.disk.logsBytes + s.disk.opmDbBytes + s.disk.metricsDbBytes)}</span><span class="sub">snapshots {bytes(s.disk.snapshotsBytes)} of {bytes(s.disk.snapshotBudgetBytes)} · images/files {bytes(s.disk.blobsBytes)} · logs {bytes(s.disk.logsBytes)} · {bytes(s.disk.freeBytes)} free</span></div>
      </div>
      {#if busiest.length}
        <div class="small muted loops" data-testid="ap-loops">Slowest loops last tick: {#each busiest as l, i (l.name)}{i ? ' · ' : ''}<span class="mono">{l.name}</span> {l.lastMs} ms{#if l.panics} <span class="bad-text">({l.panics} panics)</span>{/if}{/each}</div>
      {/if}
    {/if}
  </div>
</section>

<style>
  .pc {
    margin-top: 14px;
  }
  .pc h2 {
    display: inline-flex;
    gap: 6px;
    align-items: center;
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
    gap: 10px 18px;
    margin-top: 6px;
  }
  .m {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }
  .lbl {
    font-size: var(--fs-xs);
    color: var(--text-3);
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .v {
    font-size: 18px;
    font-weight: var(--fw-semibold);
    font-variant-numeric: tabular-nums;
  }
  .sub {
    font-size: var(--fs-xs);
    color: var(--text-2);
  }
  .loops {
    margin-top: 10px;
  }
</style>
