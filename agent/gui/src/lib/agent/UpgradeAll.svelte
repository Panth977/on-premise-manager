<script lang="ts">
  /**
   * Upgrade all sites (round I, SPEC §21 #90 e) — only with ≥ 2 sites. This site coordinates: the others first in
   * siteSlug order, this site LAST (it restarts), one at a time, stop on the first failure. Unreachable sites and agents
   * older than 0.10.0 are skipped. The run survives the coordinator's restart: after the reconnect the page polls
   * upgradeAllState again.
   */
  import { onMount } from 'svelte';
  import type { AgentUpgradeRun, SiteTile } from '@opm/shared/types';
  import type { AgentSelfInfo } from '@opm/shared/agent-self';
  import { GUI_UPGRADE_MIN_VERSION, agentUpgradeAvailable, supportsGuiUpgrade } from '@opm/shared/types';
  import { errCode, errMessage } from '$lib/api';
  import { toasts } from '$lib/state/toast.svelte';
  import { session } from '$lib/state/session.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Time from '$lib/ui/Time.svelte';
  import Confirm from '$lib/ui/Confirm.svelte';
  import { agentApi, isNetworkError } from './api';
  import { restart } from './restart.svelte';

  interface Props {
    tiles: SiteTile[];
    infos: AgentSelfInfo[];
    self: string;
    pin: string | null;
    selfStartedAt: string | null;
    /** a reason the coordinator cannot start a run now (busy), else null */
    blocked: string | null;
    onchanged: () => void;
  }
  let { tiles, infos, self, pin, selfStartedAt, blocked, onchanged }: Props = $props();

  type Plan = { site: string; version: string | null; kind: 'upgrade' | 'current' | 'unreachable' | 'old'; note: string };
  const plan = $derived.by((): Plan[] => {
    const sites = tiles.filter((t) => t.enrolled).map((t) => t.siteSlug);
    const order = [...sites.filter((s) => s !== self).sort(), ...(sites.includes(self) ? [self] : [])];
    return order.map((s) => {
      const t = tiles.find((x) => x.siteSlug === s)!;
      const i = infos.find((x) => x.siteSlug === s);
      const v = i?.version ?? t.agentVersion ?? null;
      if (!t.self && !t.reachable) return { site: s, version: v, kind: 'unreachable', note: 'skipped: the site does not answer' };
      if (!supportsGuiUpgrade(v)) return { site: s, version: v, kind: 'old', note: `skipped: agent ${v ?? '?'} is older than ${GUI_UPGRADE_MIN_VERSION} — rerun the install command of this site once (it stays valid for upgrades)` };
      if (!pin || !agentUpgradeAvailable(v, pin)) return { site: s, version: v, kind: 'current', note: 'up to date' };
      return { site: s, version: v, kind: 'upgrade', note: s === self ? 'upgraded last (this site coordinates and restarts)' : 'will be upgraded' };
    });
  });
  const toUpgrade = $derived(plan.filter((p) => p.kind === 'upgrade'));

  let run = $state<AgentUpgradeRun | null>(null);
  let confirmOpen = $state(false);
  let err = $state<string | null>(null);
  let polling = false;
  let cancelBusy = $state(false);
  let alive = true;
  const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

  async function follow() {
    if (polling) return;
    polling = true;
    try {
      while (alive) {
        try {
          const r = await agentApi.upgradeAllState({});
          const was = run?.status;
          run = r.run;
          if (!r.run || r.run.status !== 'running') {
            if (was === 'running' && r.run) finished(r.run);
            return;
          }
        } catch (e) {
          if (isNetworkError(e)) {
            // the coordinator (this site) restarts at the end of the run
            const res = await restart.wait({ site: self, prevStartedAt: selfStartedAt, title: 'Restarting the agent…', detail: `This site (${self}) is upgrading itself — the last step of “Upgrade all”.` });
            if (res !== 'ok') return;
            continue;
          }
          err = errMessage(e);
        }
        await sleep(1500);
      }
    } finally {
      polling = false;
    }
  }
  function finished(r: AgentUpgradeRun) {
    onchanged();
    if (r.status === 'done') toasts.ok('All sites upgraded', `${r.sites.filter((s) => s.status === 'done').length} upgraded to ${r.version}`);
    else if (r.status === 'cancelled') toasts.info('Upgrade all stopped', 'The remaining sites were not upgraded.');
    else toasts.push('bad', 'Upgrade all stopped at a failure', r.sites.find((s) => s.status === 'failed')?.error ?? undefined);
  }
  onMount(() => {
    follow();
    return () => (alive = false);
  });

  async function start() {
    err = null;
    if (!session.actor) {
      session.askActor = true;
      return false;
    }
    try {
      const r = await agentApi.startUpgradeAll({ body: { version: pin!, confirm: 'UPGRADE' } });
      run = r.run;
      toasts.info('Upgrade all started', `${toUpgrade.length} ${toUpgrade.length === 1 ? 'site' : 'sites'}, one at a time`);
      follow();
    } catch (e) {
      const c = errCode(e);
      err = c === 'stale' ? `The pinned release changed: ${errMessage(e)}` : c === 'locked' || c === 'conflict' ? `${errMessage(e)} (409 ${c})` : errMessage(e);
      if (c === 'stale') onchanged();
    }
  }
  async function cancel() {
    cancelBusy = true;
    try {
      const r = await agentApi.cancelUpgradeAll({});
      run = r.run;
      toasts.info('Stopping after the current site');
    } catch (e) {
      toasts.err(e, 'Cancel failed');
    } finally {
      cancelBusy = false;
    }
  }
  const TONE = { pending: '', requested: 'info', running: 'accent', done: 'ok', failed: 'bad', skipped: 'outline', cancelled: 'warn' } as const;
  const KTONE = { upgrade: 'accent', current: 'ok', unreachable: 'warn', old: 'warn' } as const;
  const KLABEL = { upgrade: 'upgrade', current: 'up to date', unreachable: 'unreachable', old: 'too old' } as const;
</script>

<section class="card" data-testid="upgrade-all">
  <div class="card-head">
    <h2 class="row tight"><Icon name="layers" size={16} />Upgrade all sites</h2>
    {#if run?.status === 'running'}
      <Btn size="sm" variant="danger" icon="stop" busy={cancelBusy} onclick={cancel} data-testid="upgrade-all-cancel">Stop after the current site</Btn>
    {:else}
      <Btn size="sm" variant="primary" icon="download" disabled={!pin || toUpgrade.length === 0 || !!blocked} title={blocked ?? (toUpgrade.length ? '' : 'Every reachable site is up to date')} onclick={() => (confirmOpen = true)} data-testid="upgrade-all-open">Upgrade {toUpgrade.length} {toUpgrade.length === 1 ? 'site' : 'sites'}{pin ? ` to ${pin}` : ''}</Btn>
    {/if}
  </div>
  {#if run}
    <div class="card-body stack" data-testid="upgrade-all-run" data-status={run.status}>
      <div class="row between">
        <span class="small">
          Run <span class="mono">{run.runId.slice(-8)}</span> · to <span class="mono">{run.version}</span> · by {run.requestedBy} · <Time ts={run.requestedAt} />
        </span>
        <span class="badge {run.status === 'done' ? 'ok' : run.status === 'running' ? 'accent' : run.status === 'cancelled' ? 'warn' : 'bad'}" data-testid="upgrade-all-status">{run.status}</span>
      </div>
      <div class="table-wrap">
        <table class="table">
          <thead><tr><th class="num">#</th><th>Site</th><th>From</th><th>Status</th><th>Detail</th></tr></thead>
          <tbody>
            {#each run.sites as s, i (s.siteSlug)}
              <tr data-testid="run-site-{s.siteSlug}" data-status={s.status}>
                <td class="num small">{i + 1}</td>
                <td class="mono small nowrap">{s.siteSlug}{#if s.siteSlug === self}<span class="badge info sb">this site</span>{/if}</td>
                <td class="mono small nowrap">{s.from ?? '—'}</td>
                <td><span class="badge {TONE[s.status]}">{#if s.status === 'running'}<span class="spin xs-spin" aria-hidden="true"></span>{/if}{s.status}</span></td>
                <td class="small">{#if s.error}<span class={s.status === 'failed' ? 'bad-text' : 'muted'}>{s.error}</span>{:else if s.at}<Time ts={s.at} />{:else}<span class="faint">—</span>{/if}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      {#if run.status !== 'running'}<div class="row end"><Btn size="sm" variant="ghost" onclick={() => (run = null)}>Show the plan again</Btn></div>{/if}
    </div>
  {:else}
    <div class="card-body stack">
      <p class="small muted" style="margin:0">One site at a time: the others first (by name), <strong>this site last</strong> because it restarts. The first failure stops the run. Sites that do not answer and agents older than {GUI_UPGRADE_MIN_VERSION} are skipped.</p>
      <div class="table-wrap">
        <table class="table" data-testid="upgrade-all-plan">
          <thead><tr><th class="num">Order</th><th>Site</th><th>Installed</th><th>Pinned</th><th>Plan</th></tr></thead>
          <tbody>
            {#each plan as p (p.site)}
              <tr data-testid="plan-{p.site}" data-kind={p.kind}>
                <td class="num small">{p.kind === 'upgrade' ? toUpgrade.indexOf(p) + 1 : '—'}</td>
                <td class="mono small nowrap">{p.site}{#if p.site === self}<span class="badge info sb">this site</span>{/if}</td>
                <td class="mono small nowrap">{p.version ?? '—'}</td>
                <td class="mono small nowrap">{pin ?? '—'}</td>
                <td class="small"><span class="badge {KTONE[p.kind]}">{KLABEL[p.kind]}</span> <span class="muted">{p.note}</span></td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </div>
  {/if}
  {#if err}<div class="card-body"><div class="banner bad small" data-testid="upgrade-all-error"><Icon name="alert" /><div class="grow">{err}</div><Btn size="xs" onclick={() => ((err = null), onchanged())}>Reload</Btn></div></div>{/if}
</section>

<Confirm bind:open={confirmOpen} title="Upgrade {toUpgrade.length} {toUpgrade.length === 1 ? 'site' : 'sites'} to {pin}?" typed="UPGRADE" confirmLabel="Upgrade all" onconfirm={start} testid="upgrade-all-confirm">
  <div class="stack tight small">
    <p style="margin:0">In this order, one at a time (each: download → verify → swap → restart → healthy within 60 s, else rollback):</p>
    <ol class="ord">{#each toUpgrade as p (p.site)}<li><span class="mono">{p.site}</span> <span class="muted">{p.version} → {pin}</span>{#if p.site === self} <span class="badge info">this site · last</span>{/if}</li>{/each}</ol>
    {#if plan.some((p) => p.kind === 'unreachable' || p.kind === 'old')}<p class="muted" style="margin:0">Skipped: {plan.filter((p) => p.kind === 'unreachable' || p.kind === 'old').map((p) => p.site).join(', ')}.</p>{/if}
    <p style="margin:0">The first failure stops the run. Actor: <strong>{session.actor?.email ?? '—'}</strong></p>
  </div>
</Confirm>

<style>
  .row.tight {
    gap: 6px;
  }
  .sb {
    margin-left: 6px;
  }
  .ord {
    margin: 0;
    padding-left: 20px;
  }
  .xs-spin {
    width: 9px;
    height: 9px;
    border-width: 1.5px;
  }
</style>
