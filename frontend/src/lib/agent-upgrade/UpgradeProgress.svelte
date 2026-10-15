<script lang="ts">
  import type { AgentUpgradeProgress } from '@opm/shared';
  import Icon from '$lib/ui/Icon.svelte';
  import Spinner from '$lib/ui/Spinner.svelte';
  import ProgressBar from '$lib/ui/ProgressBar.svelte';
  import IdChip from '$lib/ui/IdChip.svelte';
  import RelTime from '$lib/ui/RelTime.svelte';
  import { Check, X, CircleAlert, Undo2, CheckCircle, ShieldCheck } from '$lib/ui/icons';
  import { bytes } from '$lib/util/format';
  import { personName } from '$lib/util/people';
  import { PHASE_LABEL, UPGRADE_STEPS, stepStates, isActivePhase } from './upgrade';

  /** §21 #90 one upgrade attempt: phase stepper, download progress, verification result, error / result */
  let { progress: p, testid = 'upgrade-progress' }: { progress: AgentUpgradeProgress; testid?: string } = $props();
  const states = $derived(stepStates(p));
  const active = $derived(isActivePhase(p.phase));
  const v = $derived(p.verification);
  const secs = $derived(p.finishedAt ? Math.max(0, Math.round((Date.parse(p.finishedAt) - Date.parse(p.startedAt)) / 1000)) : null);
  const VIA: Record<string, string> = { gui: 'agent GUI', cloud: 'cloud console', cli: 'opm-agent upgrade', mesh: 'upgrade all (agent)' };
  const SRC: Record<string, string> = { 'agent.json': 'agent.json releasePublicKey', cloud: 'cloud release key', flag: '--pubkey flag' };
</script>

<div class="up" data-testid={testid} data-phase={p.phase}>
  <div class="row between wrap head">
    <span class="small"><b class="mono">{p.from}</b> → <b class="mono">{p.to}</b></span>
    <span class="xs faint">via {VIA[p.via] ?? p.via}{#if p.actor}{' '}· {personName(p.actor)}{/if} · started <RelTime ts={p.startedAt} /></span>
  </div>

  {#snippet details()}
  <ol class="steps" aria-label="Upgrade phases">
    {#each UPGRADE_STEPS as s, i (s.phase)}
      {@const st = states[i]}
      <li class="step {st}" data-testid="upgrade-step" data-step={s.phase} data-state={st} aria-current={st === 'current' ? 'step' : undefined}>
        <span class="mark">
          {#if st === 'done'}<Icon icon={Check} size={12} stroke={3} />
          {:else if st === 'failed'}<Icon icon={X} size={12} stroke={3} />
          {:else if st === 'current'}<Spinner size={12} />
          {:else}<span class="n">{i + 1}</span>{/if}
        </span>
        <span class="lbl xs">{s.label}{#if s.phase === 'download' && st === 'current' && p.progress !== null}{' '}{Math.round(p.progress * 100)}%{/if}</span>
      </li>
    {/each}
  </ol>

  {#if p.phase === 'download'}
    <div class="dl">
      <ProgressBar value={p.progress ?? 0} tone="blue" label="download {p.to}" />
      <span class="xs muted mono" data-testid="{testid}-bytes">{bytes(p.bytesDone)} / {bytes(p.bytesTotal)}{#if p.progress !== null}{' '}· {Math.round(p.progress * 100)}%{/if}</span>
    </div>
  {:else if active}
    <div class="xs muted" data-testid="{testid}-phase">{PHASE_LABEL[p.phase]}…{#if p.phase === 'restart' || p.phase === 'health'} the agent restarts; it rolls back to {p.from} by itself when the new agent is not healthy within 60 s{/if}</div>
  {/if}

  {#if v}
    <div class="verify" data-testid="{testid}-verification">
      <div class="small strong row"><Icon icon={ShieldCheck} size={14} />Release verification</div>
      <dl class="kv xs vk">
        <dt>Signature</dt>
        <dd data-testid="verify-signature">{#if v.signatureOk}<span class="text-green">✓ valid</span>{:else}<span class="text-red">✗ invalid</span>{/if}</dd>
        <dt>Key</dt>
        <dd><span data-testid="verify-key-source">{SRC[v.publicKeySource] ?? v.publicKeySource}</span> <span class="mono faint brk">{v.publicKeyFingerprint}</span></dd>
        <dt>sha256</dt>
        <dd class="row wrap" style="gap:6px"><IdChip id={v.sha256} short={16} /><span data-testid="verify-sha">{#if v.sha256Ok === true}<span class="text-green">✓ matches</span>{:else if v.sha256Ok === false}<span class="text-red">✗ mismatch</span>{:else}<span class="faint">not checked</span>{/if}</span></dd>
        <dt>Self-test</dt>
        <dd data-testid="verify-selftest">{#if v.selfTestOk === true}<span class="text-green">✓ <code>opm-agent version</code> = {v.version}</span>{:else if v.selfTestOk === false}<span class="text-red">✗ failed</span>{:else}<span class="faint">not run</span>{/if}</dd>
      </dl>
      {#if v.error}<div class="xs text-red">{v.error}</div>{/if}
    </div>
  {/if}

  {/snippet}
  {#if p.phase === 'done'}
    <details class="more" data-testid="{testid}-details"><summary class="xs muted">Phases and verification</summary><div class="up inner">{@render details()}</div></details>
  {:else}
    {@render details()}
  {/if}

  {#if p.phase === 'done'}
    <div class="banner ok small" data-testid="{testid}-done"><Icon icon={CheckCircle} size={15} class="icon" /><span>Upgraded to <b class="mono">{p.to}</b>{#if secs !== null}{' '}in {secs} s{/if} · <RelTime ts={p.finishedAt} /></span></div>
  {:else if p.phase === 'rolled-back'}
    <div class="banner warn small" data-testid="{testid}-rolled-back"><Icon icon={Undo2} size={15} class="icon" /><span><b>Rolled back to {p.from}.</b> {p.error ?? 'The new agent was not healthy in time.'} <span class="faint">· <RelTime ts={p.finishedAt} /></span></span></div>
  {:else if p.phase === 'failed'}
    <div class="banner error small" data-testid="{testid}-failed"><Icon icon={CircleAlert} size={15} class="icon" /><span><b>Upgrade failed — still on {p.from}.</b> {p.error ?? ''} <span class="faint">· <RelTime ts={p.finishedAt} /></span></span></div>
  {/if}
</div>

<style>
  .up {
    display: flex;
    flex-direction: column;
    gap: 10px;
    min-width: 0;
  }
  .head {
    gap: 6px;
  }
  .more summary {
    cursor: pointer;
    user-select: none;
  }
  .inner {
    margin-top: 8px;
  }
  .steps {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    grid-template-columns: repeat(6, minmax(0, 1fr));
    gap: 4px;
  }
  .step {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 4px;
    position: relative;
    text-align: center;
    min-width: 0;
  }
  .step:not(:first-child)::before {
    content: '';
    position: absolute;
    top: 10px;
    right: calc(50% + 14px);
    width: calc(100% - 24px);
    height: 2px;
    background: var(--border);
  }
  .step.done:not(:first-child)::before,
  .step.current:not(:first-child)::before,
  .step.failed:not(:first-child)::before {
    background: color-mix(in srgb, var(--green) 55%, var(--border));
  }
  .mark {
    width: 22px;
    height: 22px;
    border-radius: 999px;
    display: grid;
    place-items: center;
    border: 1.5px solid var(--border-strong, var(--border));
    background: var(--surface);
    color: var(--text-3, var(--text-2));
    font-size: 11px;
  }
  .done .mark {
    background: var(--green);
    border-color: var(--green);
    color: #fff;
  }
  .current .mark {
    border-color: var(--blue);
    color: var(--blue);
    background: var(--blue-soft);
  }
  .failed .mark {
    background: var(--red);
    border-color: var(--red);
    color: #fff;
  }
  .lbl {
    color: var(--text-2);
    overflow-wrap: anywhere;
  }
  .current .lbl {
    color: var(--blue-text);
    font-weight: var(--fw-medium);
  }
  .failed .lbl {
    color: var(--red-text);
    font-weight: var(--fw-medium);
  }
  .dl {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .verify {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    background: var(--surface-2);
  }
  .vk {
    margin: 0;
  }
  .brk {
    overflow-wrap: anywhere;
  }
  @media (max-width: 560px) {
    .steps {
      grid-template-columns: repeat(3, minmax(0, 1fr));
      row-gap: 10px;
    }
    .step:nth-child(4)::before {
      display: none;
    }
  }
</style>
