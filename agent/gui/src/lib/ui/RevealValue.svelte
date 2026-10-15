<script lang="ts">
  /**
   * A credential value, masked by default (SPEC §21 #84 b/e). View calls an audited reveal endpoint (the agent writes
   * the activity), the value shows with a visible countdown ring (REVEAL_SECONDS = 60), then re-masks by itself.
   * It also re-masks on route change, when the tab becomes hidden and when the component goes away. The value lives in
   * component memory only (never localStorage / sessionStorage). Copy works while it is visible.
   * The countdown is deadline based (Date.now()), so it is right after a throttled background tab and with a fake clock.
   */
  import { beforeNavigate } from '$app/navigation';
  import { MASK, REVEAL_SECONDS } from '@opm/shared/types';
  import Btn from './Btn.svelte';
  import { toasts } from '../state/toast.svelte';

  type Props = {
    /** the reveal endpoint call (logged by the server) */
    reveal: () => Promise<{ value: string; hideAfterSec: number }>;
    /** false → nothing to show ("—") */
    isSet?: boolean;
    /** length hint for the mask when known ("24 chars"); after a View the last seen length is used */
    length?: number | null;
    /** what the value is, for toasts and aria labels ("secret api-token") */
    label: string;
    testid: string;
  };
  let { reveal, isSet = true, length = null, label, testid }: Props = $props();

  let value = $state<string | null>(null);
  let deadline = $state(0);
  let total = $state<number>(REVEAL_SECONDS);
  let now = $state(Date.now());
  let busy = $state(false);
  let copied = $state(false);
  let lastLen = $state<number | null>(null);
  let timer: ReturnType<typeof setInterval> | null = null;

  const R = 7;
  const C = 2 * Math.PI * R;
  const left = $derived(value === null ? 0 : Math.max(0, Math.ceil((deadline - now) / 1000)));
  const frac = $derived(value === null ? 0 : Math.max(0, Math.min(1, (deadline - now) / (total * 1000))));
  const hint = $derived(length ?? lastLen);

  function stop() {
    if (timer) clearInterval(timer);
    timer = null;
  }
  function hide() {
    value = null;
    deadline = 0;
    copied = false;
    stop();
  }
  function tick() {
    now = Date.now();
    if (value !== null && now >= deadline) hide();
  }
  async function view() {
    busy = true;
    try {
      const r = await reveal();
      total = r.hideAfterSec > 0 ? r.hideAfterSec : REVEAL_SECONDS;
      value = r.value;
      lastLen = [...r.value].length;
      now = Date.now();
      deadline = now + total * 1000;
      stop();
      timer = setInterval(tick, 250);
      toasts.info(`${label} visible for ${total} s`, 'This view is written to the activity log with your email.');
    } catch (e) {
      toasts.err(e, `Could not show ${label}`);
    } finally {
      busy = false;
    }
  }
  async function copy() {
    if (value === null) return;
    const v = value;
    try {
      await navigator.clipboard.writeText(v);
    } catch {
      // insecure context (plain http on a LAN IP): fallback
      const ta = document.createElement('textarea');
      ta.value = v;
      document.body.appendChild(ta);
      ta.select();
      document.execCommand('copy');
      ta.remove();
    }
    copied = true;
    toasts.ok(`Copied ${label}`);
  }

  beforeNavigate(hide);
  $effect(() => {
    const onVis = () => {
      if (document.visibilityState === 'hidden') hide();
    };
    document.addEventListener('visibilitychange', onVis);
    return () => {
      document.removeEventListener('visibilitychange', onVis);
      stop();
    };
  });
</script>

<span class="rv" data-testid={testid} data-state={!isSet ? 'unset' : value !== null ? 'visible' : 'masked'}>
  {#if !isSet}
    <span class="faint" data-testid="{testid}-value">—</span>
  {:else if value !== null}
    <code class="val shown" data-testid="{testid}-value">{value}</code>
    <span class="ring" role="timer" aria-label="Hidden again in {left} s" title="Hidden again in {left} s">
      <svg width="18" height="18" viewBox="0 0 18 18" aria-hidden="true">
        <circle cx="9" cy="9" r={R} class="track" />
        <circle cx="9" cy="9" r={R} class="arc" stroke-dasharray={C} stroke-dashoffset={C * (1 - frac)} transform="rotate(-90 9 9)" />
      </svg>
      <span class="secs" data-testid="{testid}-left">{left}s</span>
    </span>
    <Btn size="xs" variant="ghost" icon={copied ? 'check' : 'copy'} aria-label="Copy {label}" title="Copy" onclick={copy} data-testid="{testid}-copy" />
    <Btn size="xs" variant="ghost" icon="eyeOff" aria-label="Hide {label}" title="Hide" onclick={hide} data-testid="{testid}-hide" />
  {:else}
    <code class="val mask" data-testid="{testid}-value" aria-label="hidden value">{MASK}</code>
    {#if hint}<span class="tiny muted" data-testid="{testid}-len">{hint} chars</span>{/if}
    <Btn size="xs" icon="eye" {busy} aria-label="View {label}" title="Show for {REVEAL_SECONDS} s (logged)" onclick={view} data-testid="{testid}-view">View</Btn>
  {/if}
</span>

<style>
  .rv {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    min-width: 0;
    flex-wrap: nowrap;
  }
  .val {
    max-width: 260px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .mask {
    letter-spacing: 0.15em;
  }
  .shown {
    background: var(--amber-soft);
    padding: 1px 6px;
    border-radius: 4px;
  }
  .ring {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    flex: none;
  }
  .ring circle {
    fill: none;
    stroke-width: 2.5;
  }
  .track {
    stroke: var(--border-strong);
  }
  .arc {
    stroke: var(--accent);
    stroke-linecap: round;
    transition: stroke-dashoffset 0.25s linear;
  }
  .secs {
    font-size: 11px;
    font-variant-numeric: tabular-nums;
    color: var(--text-2);
    min-width: 2.4em;
  }
</style>
