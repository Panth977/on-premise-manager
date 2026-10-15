<script lang="ts">
  /** Live progress of an update / rollback run: LOCK · SNAPSHOT · APPLY · VERIFY · COMMIT per site (GET updates/{id}, 1 s). */
  import { fastForwardLabel } from '@opm/shared/intent-wait';
  import type { UpdateRun, UpdateStepName } from '@opm/shared/types';
  import { errMessage } from '../api';
  import { overview } from '../state/app.svelte';
  import { session } from '../state/session.svelte';
  import { toasts } from '../state/toast.svelte';
  import Icon from './Icon.svelte';
  import Btn from './Btn.svelte';
  import Confirm from './Confirm.svelte';
  import Time from './Time.svelte';
  import { stepTone } from './tones';
  import { duration } from '../format';
  import { base } from '$app/paths';
  import AnsiLog from './AnsiLog.svelte';
  import { logTailPrefix } from '../ansi.svelte';
  import { siteBranchApi } from '../branch/siteBranch';

  /** §21 #111 site: the run is read (and abandoned) on that site (a per-site merge started from another site's GUI) */
  let { run: initial, ondismiss, site = null }: { run: UpdateRun; ondismiss?: () => void; site?: string | null } = $props();
  const runApi = $derived(siteBranchApi(site));
  // writable derived: follows the prop, overwritten by each poll
  let run = $derived<UpdateRun>(initial);
  let pollErr = $state<string | null>(null);
  let confirmAbandon = $state(false);
  const done = $derived(!!run.finishedAt || run.phase === 'done' || run.phase === 'failed');
  const runId = $derived(run.updateId);
  $effect(() => {
    if (done) return;
    const id = runId;
    const t = setInterval(async () => {
      try {
        run = await runApi.getUpdate(id);
        pollErr = null;
        if (run.finishedAt) {
          overview.load();
          if (run.result?.ok) toasts.ok(run.kind === 'rollback' ? 'Rollback applied on every site' : 'Update applied on every site');
          else toasts.push('bad', 'Update failed — every site was rolled back', run.result?.message);
        }
      } catch (e) {
        pollErr = errMessage(e);
      }
    }, 1000);
    return () => clearInterval(t);
  });

  const STEPS: UpdateStepName[] = ['lock', 'snapshot', 'apply', 'verify', 'commit'];
  const ORDER: Record<string, number> = { lock: 0, snapshot: 1, apply: 2, verify: 3, commit: 4, rollback: 5, unlock: 6 };
  function stepState(site: string, step: UpdateStepName): 'done' | 'run' | 'fail' | 'wait' | 'skip' {
    const s = run.sites[site];
    if (!s || !s.step) return 'wait';
    const cur = ORDER[s.step];
    const me = ORDER[step];
    if (s.step === 'rollback' || s.step === 'unlock') {
      if (s.error && step === 'apply') return 'fail';
      return me <= 1 ? 'done' : 'skip';
    }
    if (me < cur) return 'done';
    if (me === cur) return s.state === 'ok' ? 'done' : s.state === 'failed' ? 'fail' : s.state === 'running' ? 'run' : 'wait';
    return 'wait';
  }
  const sites = $derived(Object.keys(run.sites));
  /** §21 #87: containers whose failure diagnostics were captured before the rollback */
  const failures = $derived(sites.flatMap((site) => (run.sites[site]?.containers ?? []).filter((c) => c.failure).map((c) => ({ site, f: c.failure! }))));
  const rolling = $derived(run.phase === 'rollback' || (run.phase === 'failed' && !run.result?.ok));
  const lockOld = $derived.by(() => {
    void session.tick;
    const l = overview.data?.lock;
    return !!l && l.updateId === run.updateId && l.coordinator !== overview.data?.self && session.now() - Date.parse(l.lastContactAt) > 600_000;
  });
  async function abandon() {
    try {
      await runApi.abandon(run.updateId);
      toasts.ok('This site rolled back and unlocked');
      overview.load();
    } catch (e) {
      toasts.err(e, 'Abandon failed');
      return false;
    }
  }
</script>

<section class="card prog" data-testid="update-progress">
  <div class="card-head">
    <div class="row">
      {#if !done}<span class="spin" aria-hidden="true" style="color:var(--accent)"></span>{:else if run.result?.ok}<span class="ok-text"><Icon name="check" /></span>{:else}<span class="bad-text"><Icon name="alert" /></span>{/if}
      <h2>{run.kind === 'rollback' ? 'Rollback' : 'Update'} {done ? (run.result?.ok ? 'applied' : 'failed') : rolling ? 'rolling back…' : `running · ${run.phase}`}</h2>
    </div>
    <span class="muted small">coordinator <strong>{run.coordinator}</strong> · approved by {run.approvedBy}{#if run.fastForward} <span class="badge accent" data-testid="run-ff" title="The wait before ACCEPT was fast-forwarded">⏩ {fastForwardLabel(run.fastForward)}</span>{/if} · started <Time ts={run.startedAt} />{#if run.finishedAt} · took {duration(Date.parse(run.finishedAt) - Date.parse(run.startedAt))}{/if}</span>
  </div>
  <div class="card-body stack">
    {#if done && run.result}
      <div class="banner {run.result.ok ? 'ok' : 'bad'}" data-testid="update-result">
        <Icon name={run.result.ok ? 'check' : 'alert'} />
        <div class="grow">
          <strong>{run.result.ok ? (run.kind === 'rollback' ? 'Rollback applied' : 'Applied') : 'Failed — rolled back'}</strong>: {run.result.message}
          {#if run.result.snapshotId}<div class="small muted">pre-update snapshot <code>{run.result.snapshotId}</code> (Backups)</div>{/if}
        </div>
        {#if ondismiss}<Btn variant="primary" size="sm" onclick={ondismiss} data-testid="update-ok">OK</Btn>{/if}
      </div>
    {:else if rolling}
      <div class="banner bad small"><Icon name="history" /><div class="grow">A step failed. Every site is being restored from the pre-update snapshot (reverse order).</div></div>
    {/if}
    {#if pollErr}<div class="banner warn small"><Icon name="wifiOff" /><div class="grow">Progress refresh failed: {pollErr}</div></div>{/if}
    <div class="table-wrap">
      <table class="table steps">
        <thead><tr><th>Site</th>{#each STEPS as s (s)}<th>{s}</th>{/each}<th>Containers</th></tr></thead>
        <tbody>
          {#each sites as site (site)}
            {@const st = run.sites[site]}
            <tr>
              <th scope="row" class="mono">{site}</th>
              {#each STEPS as step (step)}
                {@const x = stepState(site, step)}
                <td><span class="pill {x}" data-testid="step-{site}-{step}" title="{step}: {x}">{#if x === 'run'}<span class="spin"></span>{:else if x === 'done'}<Icon name="check" size={12} />{:else if x === 'fail'}<Icon name="x" size={12} />{/if}{x === 'skip' ? 'rolled back' : x === 'wait' ? '·' : x}</span></td>
              {/each}
              <td>
                <div class="acts">
                  {#each st.containers as c (c.containerSlug)}
                    <span class="badge {stepTone(c.state)}" title={c.error ?? `${c.serviceSlug} · ${c.action}: ${c.state}`} data-testid="progress-{c.containerSlug}">{c.containerSlug} · {c.action}</span>
                  {:else}<span class="faint small">none</span>{/each}
                </div>
                {#if st.error}<div class="field-err">{st.error}</div>{/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    {#if failures.length}
      <section class="failed" data-testid="failed-containers">
        <h3 class="small">Failed containers <span class="muted">· captured before the rollback</span></h3>
        {#each failures as { site, f } (site + f.containerSlug)}
          <details class="fail" open={failures.length === 1} data-testid="failed-{f.containerSlug}">
            <summary>
              <span class="bad-text"><Icon name="alert" size={14} /></span>
              <strong class="mono">{site} · {f.containerSlug}</strong>
              {#if f.state}<span class="muted small">exit code {f.state.exitCode}{f.state.oomKilled ? ' · OOM killed' : ''} · {f.state.restartCount} restarts · {f.state.status}</span>{/if}
            </summary>
            <div class="stack">
              <div class="field-err">{f.error}</div>
              {#if f.hint}
                <div class="banner warn small hint" data-testid="failure-hint-{f.containerSlug}"><Icon name="info" /><div class="grow"><strong>Likely cause:</strong> {f.hint}<div class="small"><a href="{base}/app/{site}/config/containers/{f.containerSlug}">Open {f.containerSlug} → Depends on</a></div></div></div>
              {/if}
              <div class="small muted">ports <code>{f.ports}</code>{#if f.keptAs} · kept as <code>{f.keptAs}</code> (stopped; removed after the next successful update or 24 h) — on the server: <code>docker logs {f.keptAs}</code>{/if}</div>
              {#if f.healthHistory.length}
                <div class="table-wrap">
                  <table class="table hist">
                    <thead><tr><th>Check</th><th>Result</th><th>Message</th></tr></thead>
                    <tbody>
                      {#each f.healthHistory as h, i (i)}
                        <tr><td class="mono small"><Time ts={h.at} /></td><td><span class="badge {h.ok ? 'ok' : h.counted ? 'bad' : ''}">{h.ok ? 'pass' : h.counted ? 'fail' : 'start period'}</span></td><td class="small">{h.message}</td></tr>
                      {/each}
                    </tbody>
                  </table>
                </div>
              {/if}
              <AnsiLog lines={f.logTail} prefix={logTailPrefix} toggles testid="failed-logs-{f.containerSlug}">{#snippet head()}<span class="small muted">Last {f.logTail.length} log lines (secrets redacted)</span>{/snippet}</AnsiLog>
            </div>
          </details>
        {/each}
      </section>
    {/if}
    {#if lockOld}
      <div class="banner warn">
        <Icon name="alert" />
        <div class="grow">The coordinator <strong>{run.coordinator}</strong> has not contacted this site for 10 min. You can roll THIS site back to the pre-update snapshot and unlock it.</div>
        <Btn variant="danger" size="sm" onclick={() => (confirmAbandon = true)}>Abandon update on this site</Btn>
      </div>
    {/if}
  </div>
</section>
<Confirm bind:open={confirmAbandon} title="Abandon the update on this site?" danger confirmLabel="Roll back this site" onconfirm={abandon} message="This site restores the pre-update snapshot (if APPLY changed anything) and unlocks. Other sites are not touched." />

<style>
  .failed {
    display: grid;
    gap: 8px;
  }
  .fail {
    border: 1px solid var(--border);
    border-radius: var(--radius, 8px);
    padding: 8px 12px;
  }
  .fail summary {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
    cursor: pointer;
  }
  .fail :global(.logbox) {
    max-height: 320px;
    overflow: auto;
    white-space: pre-wrap;
    word-break: break-word;
    font-size: 12px;
  }
  .prog {
    border-color: color-mix(in srgb, var(--accent) 35%, var(--border));
  }
  .steps th[scope='row'] {
    position: static;
    text-transform: none;
    font-size: 13px;
    color: var(--text);
  }
  .pill {
    display: inline-flex;
    gap: 5px;
    align-items: center;
    font-size: 11.5px;
    font-weight: var(--fw-semibold);
    padding: 3px 9px;
    border-radius: 99px;
    background: var(--gray-soft);
    color: var(--text-2);
    white-space: nowrap;
  }
  .pill .spin {
    width: 11px;
    height: 11px;
    border-width: 2px;
  }
  .pill.done {
    background: var(--green-soft);
    color: var(--green-text);
  }
  .pill.run {
    background: var(--blue-soft);
    color: var(--blue-text);
  }
  .pill.fail {
    background: var(--red-soft);
    color: var(--red-text);
  }
  .pill.skip {
    background: var(--amber-soft);
    color: var(--amber-text);
  }
  .acts {
    display: flex;
    gap: 4px;
    flex-wrap: wrap;
  }
</style>
