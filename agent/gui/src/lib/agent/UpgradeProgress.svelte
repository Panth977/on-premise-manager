<script lang="ts">
  /** Upgrade stepper (round I): download (% bar) → verify → swap → restart → health → done / failed / rolled back. */
  import type { AgentUpgradeProgress } from '@opm/shared/types';
  import { AGENT_UPGRADE_HEALTH_SEC } from '@opm/shared/types';
  import Icon from '$lib/ui/Icon.svelte';
  import Progress from '$lib/ui/Progress.svelte';
  import Time from '$lib/ui/Time.svelte';
  import { bytes } from '$lib/format';
  import VerificationCard from './VerificationCard.svelte';

  interface Props {
    p: AgentUpgradeProgress;
    /** the last running phase this page saw (marks the failed step) */
    lastPhase?: string | null;
    /** another site's agent does not answer right now (its restart) */
    siteRestarting?: boolean;
    ondismiss?: () => void;
  }
  let { p, lastPhase = null, siteRestarting = false, ondismiss }: Props = $props();

  const STEPS = [
    { k: 'download', label: 'Download', sub: 'signed release from the cloud (resumable)' },
    { k: 'verify', label: 'Verify', sub: 'signature · sha256 · self-test' },
    { k: 'swap', label: 'Swap', sub: 'keeps opm-agent.prev' },
    { k: 'restart', label: 'Restart', sub: 'containers keep running' },
    { k: 'health', label: 'Health', sub: `healthy within ${AGENT_UPGRADE_HEALTH_SEC} s, else rollback` },
    { k: 'done', label: 'Done', sub: '' },
  ] as const;
  const ORDER = STEPS.map((s) => s.k as string);
  const terminal = $derived(p.phase === 'done' || p.phase === 'failed' || p.phase === 'rolled-back');
  /** the step that failed (failed / rolled-back) */
  const failedAt = $derived.by(() => {
    if (p.phase !== 'failed' && p.phase !== 'rolled-back') return null;
    // §21 #90: the agent reports the failed phase; older answers → guess
    if (p.failedPhase && ORDER.includes(p.failedPhase) && p.failedPhase !== 'done') return p.failedPhase;
    if (p.phase === 'rolled-back') return 'health';
    if (lastPhase && ORDER.includes(lastPhase) && lastPhase !== 'done') return lastPhase;
    if (p.verification && (!p.verification.signatureOk || p.verification.sha256Ok === false || p.verification.selfTestOk === false)) return 'verify';
    if (p.bytesTotal && p.bytesDone < p.bytesTotal) return 'download';
    return 'verify';
  });
  const cur = $derived(p.phase === 'queued' ? -1 : p.phase === 'done' ? ORDER.length - 1 : ORDER.indexOf(failedAt ?? p.phase));
  function stateOf(i: number): 'done' | 'active' | 'failed' | 'pending' {
    if (p.phase === 'done') return 'done';
    if (failedAt) return i < cur ? 'done' : i === cur ? 'failed' : 'pending';
    return i < cur ? 'done' : i === cur ? 'active' : 'pending';
  }
  const pct = $derived(p.progress !== null ? Math.round(p.progress * 100) : p.bytesTotal ? Math.round((100 * p.bytesDone) / p.bytesTotal) : 0);
</script>

<section class="card up" data-testid="upgrade-progress" data-phase={p.phase}>
  <div class="card-head">
    <h2 class="row tight">
      {#if !terminal}<span class="spin" aria-hidden="true"></span>{:else if p.phase === 'done'}<Icon name="check" size={16} />{:else}<Icon name="alert" size={16} />{/if}
      Upgrade <span class="mono">{p.from}</span> → <span class="mono">{p.to}</span>
      <span class="muted small">on {p.siteSlug}</span>
    </h2>
    <div class="row">
      <span class="faint small">by {p.actor ?? 'system'} · via {p.via} · started <Time ts={p.startedAt} /></span>
      {#if terminal && ondismiss}<button class="btn ghost icon xs" aria-label="Dismiss" onclick={ondismiss}><Icon name="x" size={14} /></button>{/if}
    </div>
  </div>
  <div class="card-body stack">
    <ol class="steps" aria-label="Upgrade steps">
      {#each STEPS as s, i (s.k)}
        {@const x = stateOf(i)}
        <li class={x} data-testid="step-{s.k}" data-state={x}>
          <span class="dotn" aria-hidden="true">
            {#if x === 'done'}<Icon name="check" size={13} />{:else if x === 'failed'}<Icon name="x" size={13} />{:else if x === 'active'}<span class="spin sm"></span>{:else}{i + 1}{/if}
          </span>
          <span class="txt">
            <span class="lbl">{s.label}</span>
            {#if s.k === 'download' && (x === 'active' || x === 'failed')}
              <span class="sub mono" data-testid="download-pct">{pct} % · {bytes(p.bytesDone)}{p.bytesTotal ? ` / ${bytes(p.bytesTotal)}` : ''}</span>
            {:else if s.k === 'restart' && x === 'active' && siteRestarting}
              <span class="sub">the agent of {p.siteSlug} is restarting…</span>
            {:else if s.sub}<span class="sub">{s.sub}</span>{/if}
          </span>
        </li>
      {/each}
    </ol>
    {#if p.phase === 'download'}
      <Progress value={pct / 100} label="Download" />
    {/if}
    {#if p.verification && (p.phase !== 'download' && p.phase !== 'queued')}
      <VerificationCard v={p.verification} compact />
    {/if}
    {#if p.phase === 'done'}
      <div class="banner ok" data-testid="upgrade-done"><Icon name="check" /><div class="grow"><strong>Now running {p.to}.</strong> The previous binary is kept as <code>opm-agent.prev</code> ({p.from}).{#if p.finishedAt} Finished <Time ts={p.finishedAt} />.{/if}</div></div>
    {:else if p.phase === 'rolled-back'}
      <div class="banner bad" data-testid="upgrade-rolled-back">
        <Icon name="history" />
        <div class="grow">
          <strong>Rolled back to {p.from}.</strong> The new agent {p.to} did not become healthy, so <code>opm-agent.prev</code> was restored.
          {#if p.error}<p class="small mono err">{p.error}</p>{/if}
          <p class="small" style="margin:4px 0 0">The site keeps running {p.from}. Download a support bundle below and send it to the operations team before trying again.</p>
        </div>
      </div>
    {:else if p.phase === 'failed'}
      <div class="banner bad" data-testid="upgrade-failed">
        <Icon name="alert" />
        <div class="grow">
          <strong>The upgrade failed in {failedAt}.</strong> {failedAt === 'download' || failedAt === 'verify' ? `Nothing was changed: the agent keeps running ${p.from}.` : ''}
          {#if p.error}<p class="small mono err">{p.error}</p>{/if}
        </div>
      </div>
    {/if}
  </div>
</section>

<style>
  .row.tight {
    gap: 6px;
  }
  .steps {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    grid-template-columns: repeat(6, minmax(0, 1fr));
    gap: 8px;
  }
  .steps li {
    display: flex;
    gap: 8px;
    align-items: flex-start;
    min-width: 0;
    padding-top: 10px;
    border-top: 3px solid var(--border);
  }
  .steps li.done {
    border-top-color: var(--green);
  }
  .steps li.active {
    border-top-color: var(--accent);
  }
  .steps li.failed {
    border-top-color: var(--red);
  }
  .dotn {
    width: 22px;
    height: 22px;
    border-radius: 50%;
    display: grid;
    place-items: center;
    font-size: 11px;
    font-weight: var(--fw-semibold);
    background: var(--gray-soft);
    color: var(--text-2);
    flex: none;
  }
  .done .dotn {
    background: var(--green-soft);
    color: var(--green-text);
  }
  .active .dotn {
    background: var(--accent-soft);
    color: var(--accent-text);
  }
  .failed .dotn {
    background: var(--red-soft);
    color: var(--red-text);
  }
  .spin.sm {
    width: 11px;
    height: 11px;
    border-width: 1.5px;
  }
  .txt {
    display: flex;
    flex-direction: column;
    min-width: 0;
    gap: 1px;
  }
  .lbl {
    font-weight: var(--fw-medium);
    font-size: var(--fs-md);
  }
  .pending .lbl {
    color: var(--text-3);
  }
  .sub {
    font-size: 11.5px;
    color: var(--text-3);
    line-height: 1.3;
  }
  .err {
    margin: 6px 0 0;
    overflow-wrap: anywhere;
    white-space: normal;
  }
  @media (max-width: 900px) {
    .steps {
      grid-template-columns: repeat(3, minmax(0, 1fr));
    }
  }
  @media (max-width: 520px) {
    .steps {
      grid-template-columns: minmax(0, 1fr);
      gap: 0;
    }
    .steps li {
      border-top: 0;
      border-left: 3px solid var(--border);
      padding: 6px 0 6px 10px;
    }
    .steps li.done {
      border-left-color: var(--green);
    }
    .steps li.active {
      border-left-color: var(--accent);
    }
    .steps li.failed {
      border-left-color: var(--red);
    }
  }
</style>
