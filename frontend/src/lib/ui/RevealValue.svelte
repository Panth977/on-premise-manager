<script lang="ts" module>
  /** What a reveal / regenerate / create call gives RevealValue (every shared RevealResponse fits). */
  export type RevealResult = { value: string; hideAfterSec?: number };
  export type RevealConfirm = { title: string; message: string; typed?: string; confirmLabel?: string; danger?: boolean };
</script>

<script lang="ts">
  /**
   * THE credential display (SPEC-final §21 #84): masked by default; View → the reveal endpoint (logged) → the value is
   * visible for hideAfterSec (60 s) with a countdown ring, then masked again (also on route change and when the tab is
   * hidden). Regenerate… → confirm (typed ACCEPT when configured) → the regenerate endpoint → the new value for 60 s.
   * The value lives only in this component's memory (never localStorage / sessionStorage).
   */
  import { onDestroy, onMount, untrack } from 'svelte';
  import { beforeNavigate } from '$app/navigation';
  import { REVEAL_SECONDS } from '@opm/shared';
  import Icon from './Icon.svelte';
  import CopyButton from './CopyButton.svelte';
  import Spinner from './Spinner.svelte';
  import ConfirmDialog from './ConfirmDialog.svelte';
  import { Eye, EyeOff, RefreshCw } from './icons';
  import { toast } from '../toast.svelte';
  import { isApiError } from '../api';

  type Props = {
    /** the reveal endpoint (audited + activity); omitted = nothing to view (e.g. template scope) */
    reveal?: () => Promise<RevealResult>;
    /** the regenerate endpoint; the new value shows for 60 s */
    regenerate?: () => Promise<RevealResult>;
    /** confirm dialog before regenerate (typed: e.g. 'ACCEPT') */
    confirm?: RevealConfirm;
    regenerateLabel?: string;
    /** false = the value is not stored (created before #84) → "not stored — regenerate to view" */
    stored?: boolean;
    /** false = no value at all → "not set" */
    isSet?: boolean;
    notStoredText?: string;
    notSetText?: string;
    /** known length → "24 chars" next to the mask */
    lengthHint?: number | null;
    /** a value just created (create deployment / account / install command): shown at once with its timer */
    initial?: RevealResult | null;
    canReveal?: boolean;
    canRegenerate?: boolean;
    /** 'command' = long values (install command, JSON key): a wrapped block */
    variant?: 'inline' | 'command';
    testid?: string;
    /** after a successful regenerate (refresh the page data) */
    onregenerated?: (r: RevealResult) => void;
    /** the server said the value is gone (410) — reason from details.reason */
    ongone?: (reason: string) => void;
    /** display transformation applied to the shown AND copied value (e.g. the "skip Docker install" command form) */
    display?: (v: string) => string;
    /** when this changes the value is masked again (e.g. the value was replaced elsewhere on the page) */
    maskKey?: unknown;
  };
  let {
    reveal,
    regenerate,
    confirm,
    regenerateLabel = 'Regenerate…',
    stored = true,
    isSet = true,
    notStoredText = 'not stored — regenerate to view',
    notSetText = 'not set',
    lengthHint = null,
    initial = null,
    canReveal = true,
    canRegenerate = true,
    variant = 'inline',
    testid = 'reveal',
    onregenerated,
    ongone,
    display,
    maskKey,
  }: Props = $props();

  let value = $state<string | null>(null);
  let total = $state<number>(REVEAL_SECONDS);
  let deadline = $state(0);
  let nowMs = $state(Date.now());
  let busy = $state(false);
  let regenBusy = $state(false);
  let confirmOpen = $state(false);
  /** the server told us the value is not stored (410 not-stored) */
  let goneNotStored = $state(false);
  let timer: ReturnType<typeof setInterval> | null = null;

  const left = $derived(value === null ? 0 : Math.max(0, Math.ceil((deadline - nowMs) / 1000)));
  const frac = $derived(value === null || total <= 0 ? 0 : Math.max(0, Math.min(1, (deadline - nowMs) / (total * 1000))));
  const notStored = $derived(!stored || goneNotStored);
  const canRegen = $derived(!!regenerate && canRegenerate);
  const shown = $derived(value === null ? '' : display ? display(value) : value);

  function stop() {
    if (timer) clearInterval(timer);
    timer = null;
  }
  function hide() {
    value = null;
    deadline = 0;
    stop();
  }
  function tick() {
    nowMs = Date.now();
    if (nowMs >= deadline) hide();
  }
  function show(r: RevealResult) {
    const secs = r.hideAfterSec && r.hideAfterSec > 0 ? r.hideAfterSec : REVEAL_SECONDS;
    total = secs;
    nowMs = Date.now();
    deadline = nowMs + secs * 1000;
    value = r.value;
    goneNotStored = false;
    stop();
    // a Date.now() deadline (not a counter) so a throttled tab or Playwright's fake clock stays exact
    timer = setInterval(tick, 250);
  }
  function fail(e: unknown, title: string) {
    if (isApiError(e) && e.code === 'rate-limited') {
      const n = Number((e.details as { retryAfterSec?: unknown })?.retryAfterSec) || 60;
      toast.push('error', `Too many reveals — try again in ${n} s`);
      return;
    }
    if (isApiError(e) && e.code === 'gone') {
      const reason = String((e.details as { reason?: unknown })?.reason ?? '');
      // not-stored / changed / used / expired / revoked / no-user: nothing to view any more → the not-stored state
      goneNotStored = true;
      ongone?.(reason);
    }
    toast.error(e, title);
  }
  async function view() {
    if (!reveal || busy) return;
    busy = true;
    try {
      show(await reveal());
    } catch (e) {
      fail(e, 'Could not show the value');
    } finally {
      busy = false;
    }
  }
  async function doRegenerate() {
    if (!regenerate) return;
    regenBusy = true;
    try {
      const r = await regenerate();
      show(r);
      onregenerated?.(r);
    } catch (e) {
      fail(e, 'Could not regenerate');
    } finally {
      regenBusy = false;
    }
  }
  function askRegenerate() {
    if (confirm) confirmOpen = true;
    else void doRegenerate();
  }

  // a just-created value (initial) shows at once
  $effect(() => {
    const i = initial;
    if (i) untrack(() => show(i));
  });
  let lastMaskKey: unknown = untrack(() => maskKey);
  $effect(() => {
    const k = maskKey;
    if (k !== lastMaskKey) {
      lastMaskKey = k;
      untrack(() => hide());
    }
  });
  // re-mask when the route changes and when the tab is hidden
  beforeNavigate(() => hide());
  function onVis() {
    if (document.visibilityState === 'hidden') hide();
  }
  onMount(() => {
    document.addEventListener('visibilitychange', onVis);
    return () => document.removeEventListener('visibilitychange', onVis);
  });
  onDestroy(hide);

  const R = 9;
  const C = 2 * Math.PI * R;
</script>

<span class="rv {variant}" class:on={value !== null} data-testid={testid} data-state={value !== null ? 'visible' : !isSet ? 'not-set' : notStored ? 'not-stored' : 'masked'}>
  {#if value !== null}
    {#if variant === 'command'}
      <pre class="code wrap val-block" data-testid="{testid}-value">{shown}</pre>
    {:else}
      <code class="val" data-testid="{testid}-value">{shown}</code>
    {/if}
    <span class="tools">
      <span class="ring" role="timer" aria-label="Hidden again in {left} seconds" data-tip="Hidden again in {left} s" data-testid="{testid}-countdown" data-left={left}>
        <svg viewBox="0 0 24 24" width="24" height="24" aria-hidden="true">
          <circle class="track" cx="12" cy="12" r={R} />
          <circle class="arc" cx="12" cy="12" r={R} stroke-dasharray={C} stroke-dashoffset={C * (1 - frac)} />
        </svg>
        <span class="secs">{left}</span>
      </span>
      <CopyButton text={shown} label={variant === 'command' ? 'Copy' : ''} testid="{testid}-copy" />
      <button type="button" class="btn ghost sm {variant === 'command' ? '' : 'icon'}" aria-label="Hide" data-tip={variant === 'command' ? undefined : 'Hide'} onclick={hide} data-testid="{testid}-hide"><Icon icon={EyeOff} size={14} />{#if variant === 'command'}Hide{/if}</button>
      {#if canRegen}
        <button type="button" class="btn ghost sm" onclick={askRegenerate} disabled={regenBusy} data-testid="{testid}-regenerate">{#if regenBusy}<Spinner size={13} />{:else}<Icon icon={RefreshCw} size={13} />{/if}{regenerateLabel}</button>
      {/if}
    </span>
  {:else if !isSet}
    <span class="faint small" data-testid="{testid}-not-set">{notSetText}</span>
    {#if canRegen}
      <button type="button" class="btn ghost sm" onclick={askRegenerate} disabled={regenBusy} data-testid="{testid}-regenerate">{#if regenBusy}<Spinner size={13} />{:else}<Icon icon={RefreshCw} size={13} />{/if}{regenerateLabel}</button>
    {/if}
  {:else if notStored}
    <span class="ns small" data-testid="{testid}-not-stored">{notStoredText}</span>
    {#if canRegen}
      <button type="button" class="btn sm" onclick={askRegenerate} disabled={regenBusy} data-testid="{testid}-regenerate">{#if regenBusy}<Spinner size={13} />{:else}<Icon icon={RefreshCw} size={13} />{/if}{regenerateLabel}</button>
    {/if}
  {:else}
    <span class="mask" aria-label="hidden value" data-testid="{testid}-masked">••••••••</span>
    {#if lengthHint}<span class="len xs faint" data-testid="{testid}-length">{lengthHint} chars</span>{/if}
    {#if reveal && canReveal}
      <button type="button" class="btn ghost sm" aria-label="View" data-tip="Show for {REVEAL_SECONDS} s (logged)" onclick={view} disabled={busy} data-testid="{testid}-view">
        {#if busy}<Spinner size={13} />{:else}<Icon icon={Eye} size={14} />{/if}View
      </button>
    {/if}
    {#if canRegen}
      <button type="button" class="btn ghost sm" onclick={askRegenerate} disabled={regenBusy} data-testid="{testid}-regenerate">{#if regenBusy}<Spinner size={13} />{:else}<Icon icon={RefreshCw} size={13} />{/if}{regenerateLabel}</button>
    {/if}
  {/if}
</span>

{#if confirm && canRegen}
  <ConfirmDialog bind:open={confirmOpen} title={confirm.title} message={confirm.message} typed={confirm.typed ?? null} confirmLabel={confirm.confirmLabel ?? 'Regenerate'} danger={confirm.danger ?? false} onconfirm={doRegenerate} testid="{testid}-confirm" />
{/if}

<style>
  .rv {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
    flex-wrap: wrap;
  }
  .rv.command.on {
    display: flex;
    flex-direction: column;
    align-items: stretch;
    width: 100%;
  }
  .tools {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    flex-wrap: wrap;
  }
  .mask {
    letter-spacing: 0.15em;
    color: var(--text-2);
  }
  .len {
    font-variant-numeric: tabular-nums;
  }
  .ns {
    color: var(--amber-text);
  }
  .val {
    font-size: 12px;
    background: var(--amber-soft);
    padding: 1px 6px;
    border-radius: 4px;
    max-width: 360px;
    overflow-wrap: anywhere;
    word-break: break-all;
    user-select: all;
  }
  .val-block {
    margin: 0;
    background: var(--amber-soft);
    user-select: all;
    max-height: 220px;
    overflow: auto;
  }
  .ring {
    position: relative;
    display: inline-grid;
    place-items: center;
    width: 24px;
    height: 24px;
    flex: none;
  }
  .ring svg {
    transform: rotate(-90deg);
  }
  .ring circle {
    fill: none;
    stroke-width: 2.5;
  }
  .ring .track {
    stroke: var(--border);
  }
  .ring .arc {
    stroke: var(--accent);
    stroke-linecap: round;
    transition: stroke-dashoffset 0.25s linear;
  }
  .ring .secs {
    position: absolute;
    inset: 0;
    display: grid;
    place-items: center;
    font-size: 9px;
    font-weight: 600;
    color: var(--text-2);
    font-variant-numeric: tabular-nums;
  }
</style>
