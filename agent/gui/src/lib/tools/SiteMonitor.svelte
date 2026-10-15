<script lang="ts">
  /** Monitoring of one site (round T): host cards + per-container table with sparklines; nav2: hostOnly = the host cards on Status. */
  import type { MetricAlert, MetricsSeriesResponse, MetricsSummary } from '@opm/shared/agent-tools';
  import Icon from '$lib/ui/Icon.svelte';
  import Sparkline from './charts/Sparkline.svelte';
  import { bytes, rate, uptime, pctNum, levelOf, sinceSec } from './fmt';

  let {
    m,
    host,
    showSite = false,
    hostOnly = false,
    onpick,
  }: { m: MetricsSummary; host: MetricsSeriesResponse | null; showSite?: boolean; hostOnly?: boolean; onpick: (subject: string, title: string) => void } = $props();

  const alertFor = (subject: string, metric: MetricAlert['metric']) => m.alerts.find((a) => a.subject === subject && a.metric === metric) ?? null;
  const h = $derived(m.host);
  const memPct = $derived(h && h.memTotalBytes ? (100 * (h.memUsedBytes ?? 0)) / h.memTotalBytes : null);
  const swapPct = $derived(h && h.swapTotalBytes ? (100 * (h.swapUsedBytes ?? 0)) / h.swapTotalBytes : null);
  const cpuLv = $derived(levelOf(h?.cpuPct, 'cpu', alertFor('host', 'cpu')));
  const memLv = $derived(levelOf(memPct, 'mem', alertFor('host', 'mem')));
  const hs = $derived(host?.series ?? {});
  const tail = <T,>(xs: T[] | undefined, n = 120) => (xs ?? []).slice(-n);
  const memSeries = $derived(tail(hs.memUsedBytes ?? hs.memBytes));
  const LV = { ok: '', amber: 'amber', red: 'red' } as const;
  const LVTXT = { ok: '', amber: 'above threshold', red: 'sustained ≥ 5 min' } as const;
</script>

<div class="stack">
  {#if m.alerts.length}
    <div class="alerts" data-testid="metric-alerts">
      {#each m.alerts as a (a.subject + a.metric)}
        <div class="al {a.level}"><Icon name="alert" size={14} /><span class="mono small">{a.subject.replace(/^container:/, '')}</span><span class="small">{a.message}</span>{#if a.since}<span class="faint xs">since {uptime(sinceSec(a.since))}</span>{/if}</div>
      {/each}
    </div>
  {/if}

  {#if h}
    <div class="hosts" data-testid="host-cards">
      <button class="mc {LV[cpuLv]}" onclick={() => onpick('host', `${m.siteSlug} · host`)} data-testid="host-cpu">
        <span class="lbl"><Icon name="cpu" size={14} />CPU{#if cpuLv !== 'ok'}<span class="badge {cpuLv === 'red' ? 'bad' : 'warn'}" title={LVTXT[cpuLv]}>{cpuLv === 'red' ? 'high' : '> 85 %'}</span>{/if}</span>
        <strong>{pctNum(h.cpuPct, 1)}</strong>
        <span class="sub">{h.cpuCount} cores</span>
        <Sparkline values={tail(hs.cpuPct)} max={100} width={140} height={28} tone={cpuLv === 'red' ? 'red' : cpuLv === 'amber' ? 'amber' : 'accent'} />
      </button>
      <button class="mc {LV[memLv]}" onclick={() => onpick('host', `${m.siteSlug} · host`)}>
        <span class="lbl"><Icon name="layers" size={14} />Memory{#if memLv !== 'ok'}<span class="badge {memLv === 'red' ? 'bad' : 'warn'}">{memLv === 'red' ? 'high' : '> 90 %'}</span>{/if}</span>
        <strong>{bytes(h.memUsedBytes)}</strong>
        <span class="sub">of {bytes(h.memTotalBytes)} · {pctNum(memPct)}</span>
        <Sparkline values={memSeries} max={h.memTotalBytes} width={140} height={28} tone={memLv === 'red' ? 'red' : memLv === 'amber' ? 'amber' : 'violet'} />
      </button>
      <button class="mc" onclick={() => onpick('host', `${m.siteSlug} · host`)}>
        <span class="lbl"><Icon name="rotate" size={14} />Swap</span>
        <strong>{bytes(h.swapUsedBytes)}</strong>
        <span class="sub">{h.swapTotalBytes ? `of ${bytes(h.swapTotalBytes)} · ${pctNum(swapPct)}` : 'no swap'}</span>
        <div class="meter"><i style="width:{Math.min(100, swapPct ?? 0)}%"></i></div>
      </button>
      <button class="mc" onclick={() => onpick('host', `${m.siteSlug} · host`)}>
        <span class="lbl"><Icon name="pulse" size={14} />Load</span>
        <strong>{h.load1?.toFixed(2) ?? '—'}</strong>
        <span class="sub">5 m {h.load5?.toFixed(2) ?? '—'} · 15 m {h.load15?.toFixed(2) ?? '—'}</span>
        <Sparkline values={tail(hs.load1)} width={140} height={28} tone="blue" />
      </button>
      <div class="mc static">
        <span class="lbl"><Icon name="clock" size={14} />Uptime</span>
        <strong>{uptime(h.uptimeSec)}</strong>
        <span class="sub">sampled {uptime(sinceSec(h.t))} ago · every {m.intervalSec} s</span>
      </div>
      {#each h.disks as d (d.mount)}
        {@const p = d.totalBytes ? (100 * d.usedBytes) / d.totalBytes : 0}
        {@const lv = levelOf(p, 'disk', alertFor(`disk:${d.mount}`, 'disk'))}
        <div class="mc static {LV[lv]}" data-testid="disk-{d.label}">
          <span class="lbl"><Icon name="hardDrive" size={14} />Disk <span class="mono faint" title={d.mount}>{d.label === 'agentHome' ? 'agentHome' : d.mount}</span>{#if lv !== 'ok'}<span class="badge bad">&gt; 90 %</span>{/if}</span>
          <strong>{pctNum(p)}</strong>
          <span class="sub">{bytes(d.usedBytes)} of {bytes(d.totalBytes)} · {bytes(d.totalBytes - d.usedBytes)} free</span>
          <div class="meter"><i style="width:{Math.min(100, p)}%" class:red={lv === 'red'}></i></div>
        </div>
      {/each}
    </div>
  {:else}
    <p class="muted small">No host sample yet (the first one comes within 15 s of the agent start).</p>
  {/if}

  {#if !hostOnly}
  <section class="card">
    <div class="card-head"><h2>Containers{#if showSite} <span class="muted small mono">· {m.siteSlug}</span>{/if}</h2><span class="muted small">last 60 min · click a row for charts</span></div>
    {#if m.containers.length === 0}
      <div class="card-body"><p class="muted small" style="margin:0">No container samples on this site.</p></div>
    {:else}
      <div class="table-wrap">
        <table class="table" data-testid="metrics-containers">
          <thead><tr><th>Container</th><th>CPU</th><th>Memory</th><th class="num">Network rx / tx</th><th class="num">Disk read / write</th><th class="num">Restarts</th></tr></thead>
          <tbody>
            {#each m.containers as c (c.ref)}
              {@const cl = levelOf(c.cpuPct, 'cpu', alertFor(`container:${c.ref}`, 'cpu'))}
              {@const mp = c.memBytes !== null && c.memLimitBytes ? (100 * c.memBytes) / c.memLimitBytes : null}
              {@const ml = levelOf(mp, 'mem', alertFor(`container:${c.ref}`, 'mem'))}
              <tr class="clickable" onclick={() => onpick(`container:${c.ref}`, `${c.ref}`)} data-testid="mrow-{c.ref}">
                <td>
                  <button class="linkish mono" onclick={(e) => (e.stopPropagation(), onpick(`container:${c.ref}`, c.ref))}><strong>{c.ref}</strong></button>
                  {#if c.oomKilled}<span class="badge bad" title="killed for memory">OOM</span>{/if}
                  {#if c.cpuPct === null}<div class="tiny faint">not running</div>{/if}
                </td>
                <td>
                  <div class="cell">
                    <span class="v" class:warn-text={cl === 'amber'} class:bad-text={cl === 'red'}>{pctNum(c.cpuPct, 1)}</span>
                    <Sparkline values={c.spark.cpu} width={88} height={22} tone={cl === 'red' ? 'red' : cl === 'amber' ? 'amber' : 'accent'} />
                  </div>
                </td>
                <td>
                  <div class="cell">
                    <span class="v">{bytes(c.memBytes)}{#if c.memLimitBytes}<span class="faint tiny">{' / '}{bytes(c.memLimitBytes)}</span>{/if}
                      {#if mp !== null}<span class="mbar"><i style="width:{Math.min(100, mp)}%" class:warn={ml !== 'ok'}></i></span>{/if}
                    </span>
                    <Sparkline values={c.spark.mem} width={88} height={22} max={c.memLimitBytes} tone={ml === 'ok' ? 'violet' : 'amber'} />
                  </div>
                </td>
                <td class="num small nowrap">{rate(c.netRxBps)}<span class="faint">{' / '}</span>{rate(c.netTxBps)}</td>
                <td class="num small nowrap">{rate(c.blkReadBps)}<span class="faint">{' / '}</span>{rate(c.blkWriteBps)}</td>
                <td class="num" class:warn-text={c.restartCount > 0}>{c.restartCount}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>
  {/if}
</div>

<style>
  .alerts {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .al {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 4px 8px;
    padding: 7px 12px;
    border-radius: var(--r-md);
    border: 1px solid;
  }
  .al.amber {
    background: var(--amber-soft);
    border-color: color-mix(in srgb, var(--amber) 35%, transparent);
    color: var(--amber-text);
  }
  .al.red {
    background: var(--red-soft);
    border-color: color-mix(in srgb, var(--red) 35%, transparent);
    color: var(--red-text);
  }
  .hosts {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(min(200px, calc(50% - 5px)), 1fr));
    gap: 10px;
  }
  .mc {
    all: unset;
    box-sizing: border-box;
    display: flex;
    flex-direction: column;
    gap: 3px;
    padding: 12px 14px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--r-lg);
    cursor: pointer;
    min-width: 0;
    transition: border-color var(--dur-fast);
  }
  .mc:hover:not(.static) {
    border-color: var(--border-strong);
  }
  .mc:focus-visible {
    outline: 2px solid var(--accent);
  }
  .mc.static {
    cursor: default;
  }
  .mc.amber {
    border-color: color-mix(in srgb, var(--amber) 55%, var(--border));
    box-shadow: inset 3px 0 0 var(--amber);
  }
  .mc.red {
    border-color: color-mix(in srgb, var(--red) 55%, var(--border));
    box-shadow: inset 3px 0 0 var(--red);
  }
  .lbl {
    display: flex;
    align-items: center;
    gap: 6px;
    color: var(--text-2);
    font-size: var(--fs-sm);
  }
  .lbl .badge {
    margin-left: auto;
  }
  .mc :global(.spark) {
    max-width: 100%;
  }
  .mc strong {
    font-size: 22px;
    font-variant-numeric: tabular-nums;
    line-height: 1.2;
  }
  .sub {
    font-size: 11.5px;
    color: var(--text-3);
    margin-bottom: 4px;
  }
  .meter,
  .mbar {
    height: 6px;
    border-radius: 3px;
    background: var(--bg-sunken);
    overflow: hidden;
    margin-top: auto;
  }
  .meter i,
  .mbar i {
    display: block;
    height: 100%;
    background: var(--accent);
  }
  .meter i.red {
    background: var(--red);
  }
  .mbar {
    display: block;
    height: 3px;
    width: 90px;
    margin-top: 3px;
  }
  .mbar i.warn {
    background: var(--amber);
  }
  .cell {
    display: flex;
    align-items: center;
    gap: 10px;
    justify-content: space-between;
    min-width: 170px;
  }
  .v {
    font-variant-numeric: tabular-nums;
    font-size: var(--fs-sm);
    white-space: nowrap;
  }
  .linkish {
    all: unset;
    cursor: pointer;
  }
  .linkish:focus-visible {
    outline: 2px solid var(--accent);
  }
</style>
