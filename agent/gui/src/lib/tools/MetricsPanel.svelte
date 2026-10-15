<script lang="ts">
  /** Charts of one monitoring subject (host or container) over 1 h / 24 h / 7 d (round T; nav2: inline on the container page, in MetricsDrawer for the host). */
  import { METRIC_THRESHOLDS, type MetricsRange, type MetricsSeriesResponse } from '@opm/shared/agent-tools';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import { errMessage } from '$lib/api';
  import { tools } from './api';
  import LineChart from './charts/LineChart.svelte';
  import { bytes, rate } from './fmt';

  let { site, subject }: { site: string; subject: string } = $props();
  let range = $state<MetricsRange>('1h');
  let data = $state<MetricsSeriesResponse | null>(null);
  let err = $state<string | null>(null);
  let gen = 0;
  async function load() {
    const my = ++gen;
    err = null;
    try {
      const r = await tools.metricsSeries({ siteSlug: site, query: { subject, range } });
      if (my === gen) data = r;
    } catch (e) {
      if (my === gen) err = errMessage(e);
    }
  }
  $effect(() => {
    void site;
    void subject;
    void range;
    data = null;
    load();
    const t = setInterval(load, range === '1h' ? 15_000 : 60_000);
    return () => clearInterval(t);
  });
  const isHost = $derived(subject === 'host');
  const s = $derived(data?.series ?? {});
  const C = { a: 'var(--accent)', b: 'var(--violet)', g: 'var(--green)', amber: 'var(--amber)', blue: 'var(--blue)', red: 'var(--red)', gray: 'var(--text-3)' };
  const pctF = (v: number) => `${Math.round(v)} %`;
  const step = $derived(data ? (data.stepSec < 60 ? `${data.stepSec} s` : data.stepSec < 3600 ? `${data.stepSec / 60} min` : `${data.stepSec / 3600} h`) : '');
</script>

<div class="mp" data-testid="metrics-panel">
  <div class="row between head">
    <SegmentedControl label="Range" options={[{ value: '1h', label: '1 h' }, { value: '24h', label: '24 h' }, { value: '7d', label: '7 d' }]} value={range} onchange={(v) => (range = v as MetricsRange)} testid="metrics-range" />
    <span class="faint xs">{data ? `${data.t.length} points · one per ${step}` : ''}</span>
  </div>
  {#if err}
    <div class="banner bad small"><Icon name="alert" /><div class="grow">{err}</div></div>
  {:else if !data}
    <span class="skel" style="height:420px"></span>
  {:else}
    <div class="charts" data-testid="metrics-charts" data-range={data.range}>
      <section>
        <h4>CPU</h4>
        <LineChart label="CPU %" t={data.t} yMax={100} yFmt={pctF} threshold={{ value: METRIC_THRESHOLDS.cpuPct, label: `${METRIC_THRESHOLDS.cpuPct} %` }} series={[{ label: 'CPU %', values: s.cpuPct ?? [], color: C.a, area: true }, ...(s.cpuPeak ? [{ label: 'peak', values: s.cpuPeak, color: C.b, dashed: true }] : [])]} />
      </section>
      <section>
        <h4>Memory</h4>
        {#if isHost}
          <LineChart label="Memory" t={data.t} yFmt={(v) => bytes(v)} series={[{ label: 'used', values: s.memUsedBytes ?? s.memBytes ?? [], color: C.a, area: true }, { label: 'total', values: s.memTotalBytes ?? s.memLimitBytes ?? [], color: C.gray, dashed: true }, ...(s.swapUsedBytes ? [{ label: 'swap used', values: s.swapUsedBytes, color: C.amber }] : [])]} />
        {:else}
          <LineChart label="Memory" t={data.t} yFmt={(v) => bytes(v)} series={[{ label: 'used', values: s.memBytes ?? [], color: C.a, area: true }, ...(s.memLimitBytes?.some((v) => v !== null) ? [{ label: 'limit', values: s.memLimitBytes, color: C.red, dashed: true }] : [])]} />
        {/if}
      </section>
      {#if isHost}
        <section>
          <h4>Load average</h4>
          <LineChart label="Load average" t={data.t} yFmt={(v) => v.toFixed(1)} series={[{ label: '1 min', values: s.load1 ?? [], color: C.a }, { label: '5 min', values: s.load5 ?? [], color: C.b }, { label: '15 min', values: s.load15 ?? [], color: C.gray }]} />
        </section>
      {:else}
        <section>
          <h4>Network</h4>
          <LineChart label="Network" t={data.t} yFmt={(v) => rate(v)} series={[{ label: 'received', values: s.netRxBps ?? [], color: C.blue, area: true }, { label: 'sent', values: s.netTxBps ?? [], color: C.g }]} />
        </section>
        <section>
          <h4>Block I/O</h4>
          <LineChart label="Block I/O" t={data.t} yFmt={(v) => rate(v)} series={[{ label: 'read', values: s.blkReadBps ?? [], color: C.b }, { label: 'write', values: s.blkWriteBps ?? [], color: C.amber, area: true }]} />
        </section>
      {/if}
    </div>
  {/if}
</div>


<style>
  .head {
    margin-bottom: 14px;
  }
  .charts {
    display: flex;
    flex-direction: column;
    gap: 18px;
  }
  h4 {
    margin: 0 0 6px;
    font-size: var(--fs-sm);
    color: var(--text-2);
    font-weight: var(--fw-semibold);
  }
</style>
