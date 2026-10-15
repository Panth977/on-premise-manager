<script lang="ts">
  /**
   * The ONE intent-wait modal (approve, rollback to a snapshot, reset a volume to its starter content):
   * start() → countdown (Cancel · ⏩ Hold to fast-forward, §21 #95) → type ACCEPT → confirm().
   * The wait is enforced by the agent (effective seconds = Σ speed × dt on its clock); the ring shows the agent's
   * remaining time, animated between its answers (IntentClock).
   */
  import { untrack, type Snippet } from 'svelte';
  import type { ApproveIntent, ApproveRequest, ApproveStartResponse, GateCheck } from '@opm/shared/types';
  import { errCode, errMessage, isApiError } from '../api';
  import { session } from '../state/session.svelte';
  import { mmss } from '../format';
  import { IntentClock, type IntentApi } from './intentClock.svelte';
  import Modal from './Modal.svelte';
  import Btn from './Btn.svelte';
  import Icon from './Icon.svelte';
  import GateChecklist from './GateChecklist.svelte';
  import FastForwardButton from './FastForwardButton.svelte';

  interface Props {
    open: boolean;
    title: string;
    testid: string;
    cancelTestid: string;
    confirmTestid: string;
    confirmLabel: string;
    startingText: string;
    /** the line under "Wait n s before you confirm." */
    waitText: string;
    /** "the update" / "the rollback" / "the reset" */
    what: string;
    accept: Snippet;
    details?: Snippet;
    /** §21 #96: options shown during the wait and at the confirm (e.g. "snapshot first") */
    extra?: Snippet;
    /** the word to type (default ACCEPT; the volume name for empty / restore) */
    word?: string;
    /** §21 #104: another reason the confirm is not possible yet (e.g. "a commit message is required") */
    confirmBlocked?: string | null;
    start: () => Promise<ApproveStartResponse>;
    confirm: (body: ApproveRequest) => Promise<void>;
    intentApi: IntentApi & { cancel: (intentId: string) => Promise<unknown> };
  }
  let { open = $bindable(false), title, testid, cancelTestid, confirmTestid, confirmLabel, startingText, waitText, what, accept, details, extra, word = 'ACCEPT', confirmBlocked = null, start, confirm, intentApi }: Props = $props();
  let phase = $state<'starting' | 'waiting' | 'accept' | 'submitting' | 'gate' | 'error'>('starting');
  let intent = $state<ApproveIntent | null>(null);
  let clock = $state<IntentClock | null>(null);
  let gate = $state<GateCheck | null>(null);
  /** §21 #110 open-gate conditions that only warn (e.g. an update is running: starting this stops it) */
  let warns = $state<GateCheck['conditions']>([]);
  let error = $state<string | null>(null);
  let typed = $state('');
  let input: HTMLInputElement | undefined = $state();
  const typedOk = $derived(word === 'ACCEPT' ? typed === word : typed.trim() === word);

  // only `open` starts the flow (the parent re-renders on every poll)
  $effect(() => {
    if (open) untrack(begin);
  });
  // the clock lives as long as the modal shows it
  $effect(() => {
    const c = clock;
    if (!c) return;
    untrack(() => c.start());
    return () => c.stop();
  });

  function toAccept() {
    if (phase !== 'waiting') return;
    phase = 'accept';
    setTimeout(() => input?.focus(), 30);
  }

  async function begin() {
    phase = 'starting';
    intent = null;
    clock = null;
    gate = null;
    warns = [];
    error = null;
    typed = '';
    try {
      const r = await start();
      intent = r.intent;
      warns = (r.gate?.conditions ?? []).filter((c) => c.ok && c.warn);
      clock = new IntentClock(r.intent, intentApi, toAccept, session.now());
      phase = 'waiting';
    } catch (e) {
      fail(e);
    }
  }
  function fail(e: unknown) {
    clock = null;
    if (errCode(e) === 'gate-closed' && isApiError(e) && e.details.gate) {
      gate = e.details.gate as GateCheck;
      phase = 'gate';
    } else {
      error = errMessage(e);
      phase = 'error';
    }
  }
  async function cancel() {
    const id = intent?.intentId;
    const was = phase;
    intent = null;
    clock = null;
    open = false;
    if (id && was !== 'submitting') await intentApi.cancel(id).catch(() => {});
  }
  async function submit(e?: SubmitEvent) {
    e?.preventDefault();
    if (!intent || !typedOk || confirmBlocked) return;
    phase = 'submitting';
    try {
      await confirm({ intentId: intent.intentId, typed: typed.trim() });
      intent = null;
      clock = null;
      open = false;
    } catch (err) {
      if (errCode(err) === 'invalid' && /wait/i.test(errMessage(err)) && intent) {
        // the agent says the wait is not over: back to the countdown on its numbers
        clock = new IntentClock(intent, intentApi, toAccept, session.now());
        phase = 'waiting';
        return;
      }
      fail(err);
    }
  }
</script>

{#snippet gateWarns()}
  {#each warns as w (w.key)}
    <div class="banner warn small" data-testid="gate-warn-{w.key}"><Icon name="alert" /><div class="grow">{w.key === 'idle' ? `An update is running — starting ${what} stops it (nothing is rolled back).` : w.detail}{#if w.key === 'idle'}<div class="muted tiny">{w.detail}</div>{/if}</div></div>
  {/each}
{/snippet}

<Modal bind:open {title} size="md" onclose={cancel} {testid}>
  {#if phase === 'starting'}
    <div class="row muted"><span class="spin"></span>{startingText}</div>
  {:else if phase === 'waiting' && clock}
    {@const fast = clock.shownSpeed > 1.01}
    <div class="wait" data-testid="countdown" data-remaining={clock.remaining.toFixed(2)} data-server-remaining={clock.server?.remainingSec ?? ''} data-server-speed={clock.server?.speed ?? 1}>
      <div class="ring" class:fast style="--p:{clock.progress}">
        <span class="mono" data-testid="countdown-time">{mmss(clock.remaining)}</span>
      </div>
      <div class="grow">
        <div class="row head">
          <strong>Wait {Math.ceil(clock.remaining)} s before you confirm.</strong>
          {#key clock.badge}
            <span class="badge speed s{clock.badge}" class:fast data-testid="ff-speed" data-speed={clock.shownSpeed.toFixed(2)} title="Wait speed (the agent counts it)">×{clock.badge}</span>
          {/key}
        </div>
        <p class="muted small">{waitText}</p>
        <p class="faint tiny hint">Keep <strong>Hold to fast-forward</strong> pressed (mouse, touch, or Space / Enter) to run the wait ×2, then ×4 — never shorter than {Math.round(clock.waitSec / 4)} s in total.</p>
        <div class="meter" aria-hidden="true"><span style="width:{((clock.shownSpeed - 1) / 3) * 100}%"></span></div>
      </div>
    </div>
    {@render gateWarns()}
    {#if extra}<div class="extra">{@render extra()}</div>{/if}
    {#if details}<div class="det">{@render details()}</div>{/if}
  {:else if phase === 'accept' || phase === 'submitting'}
    <form onsubmit={submit} class="stack" novalidate>
      {@render gateWarns()}
      <div class="banner warn">
        <Icon name="alert" />
        <div class="grow">
          {@render accept()}
          <div style="margin-top:4px">Actor: <strong>{session.actor?.email ?? '—'}</strong></div>
        </div>
      </div>
      {#if clock?.server && clock.server.maxSpeed > 1}
        <p class="tiny muted ffdone" data-testid="ff-done"><Icon name="fastForward" size={12} /> The wait was fast-forwarded (up to ×{clock.server.maxSpeed}); this is recorded with the activity.</p>
      {/if}
      {#if extra}<div class="extra">{@render extra()}</div>{/if}
      {#if details}<div class="det">{@render details()}</div>{/if}
      <div class="field">
        <label for="accept-in">Type <span class="mono">{word}</span></label>
        <input bind:this={input} id="accept-in" class="input mono" bind:value={typed} autocomplete="off" spellcheck="false" autocapitalize={word === 'ACCEPT' ? 'characters' : 'off'} data-testid="accept-input" />
      </div>
    </form>
  {:else if phase === 'gate' && gate}
    <div class="banner bad small" style="margin-bottom:12px"><Icon name="lock" /><div class="grow">The gate is closed — {what} cannot start now.</div></div>
    <GateChecklist {gate} />
  {:else if phase === 'error'}
    <div class="banner bad"><Icon name="alert" /><div class="grow">{error}</div></div>
  {/if}
  {#snippet footer()}
    {#if phase === 'gate' || phase === 'error'}
      <Btn onclick={() => (open = false)}>Close</Btn>
      <Btn variant="primary" onclick={begin}>Try again</Btn>
    {:else}
      <Btn onclick={cancel} disabled={phase === 'submitting'} data-testid={cancelTestid}>Cancel</Btn>
      {#if phase === 'waiting' && clock}
        <FastForwardButton {clock} />
      {/if}
      {#if phase === 'accept' || phase === 'submitting'}
        <Btn variant="danger" busy={phase === 'submitting'} disabled={!typedOk || !!confirmBlocked} title={confirmBlocked ?? undefined} onclick={() => submit()} data-testid={confirmTestid}>{confirmLabel}</Btn>
      {/if}
    {/if}
  {/snippet}
</Modal>

<style>
  .wait {
    display: flex;
    gap: 16px;
    align-items: center;
    margin-bottom: 12px;
  }
  .ring {
    --p: 0;
    width: 76px;
    height: 76px;
    border-radius: 50%;
    flex: none;
    display: grid;
    place-items: center;
    background: conic-gradient(var(--accent) calc(var(--p) * 360deg), var(--gray-soft) 0);
    position: relative;
    transition: box-shadow 0.3s ease;
  }
  .ring.fast {
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--accent) 22%, transparent);
  }
  .ring::before {
    content: '';
    position: absolute;
    inset: 7px;
    border-radius: 50%;
    background: var(--surface);
  }
  .ring span {
    position: relative;
    font-size: 17px;
    font-weight: 700;
    font-variant-numeric: tabular-nums;
  }
  .head {
    gap: 8px;
    flex-wrap: wrap;
  }
  .speed {
    font-variant-numeric: tabular-nums;
    min-width: 34px;
    justify-content: center;
    animation: ff-pop 0.25s ease-out;
  }
  .speed.fast {
    background: var(--accent-soft-2);
    color: var(--accent-text);
    border-color: var(--accent);
  }
  @keyframes ff-pop {
    from {
      transform: scale(0.8);
      opacity: 0.4;
    }
    to {
      transform: scale(1);
      opacity: 1;
    }
  }
  .meter {
    height: 3px;
    border-radius: 2px;
    background: var(--gray-soft);
    margin-top: 8px;
    overflow: hidden;
  }
  .meter span {
    display: block;
    height: 100%;
    background: var(--accent);
  }
  .wait p {
    margin: 4px 0 0;
  }
  .hint strong {
    font-weight: 600;
  }
  .ffdone {
    display: flex;
    gap: 6px;
    align-items: center;
    margin: 0;
  }
  .extra {
    margin-bottom: 12px;
  }
  .det {
    max-height: 300px;
    overflow: auto;
  }
</style>
