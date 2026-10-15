<script lang="ts">
  /**
   * §21 #95 "⏩ Hold to fast-forward" (next to Cancel in every intent-wait modal). Press and hold (pointer, touch, Space
   * or Enter) → ×2 at once, ×4 after 1.5 s; release / leave / blur / tab hidden → back to ×1 (shown easing 400 ms).
   * The agent enforces the wait; this only sends the speed (IntentClock).
   */
  import type { IntentClock } from './intentClock.svelte';
  import Icon from './Icon.svelte';

  let { clock, testid = 'ff-hold' }: { clock: IntentClock; testid?: string } = $props();

  function down(e: PointerEvent) {
    if (e.button !== 0) return;
    e.preventDefault(); // no text selection / focus steal on touch
    (e.currentTarget as HTMLElement).focus({ preventScroll: true });
    clock?.press();
  }
  function keydown(e: KeyboardEvent) {
    if (e.key !== ' ' && e.key !== 'Enter') return;
    e.preventDefault(); // no click / scroll; key repeat is ignored by press()
    clock?.press();
  }
  function keyup(e: KeyboardEvent) {
    if (e.key !== ' ' && e.key !== 'Enter') return;
    e.preventDefault();
    clock?.release();
  }
  $effect(() => {
    const hide = () => {
      if (document.visibilityState === 'hidden') clock?.release();
    };
    const blur = () => clock?.release();
    const up = () => clock?.release(); // a pointerup outside the button
    document.addEventListener('visibilitychange', hide);
    window.addEventListener('blur', blur);
    window.addEventListener('pointerup', up);
    return () => {
      document.removeEventListener('visibilitychange', hide);
      window.removeEventListener('blur', blur);
      window.removeEventListener('pointerup', up);
    };
  });
</script>

<button
  type="button"
  class="btn ff"
  class:on={clock.holding}
  disabled={clock.ready}
  aria-pressed={clock.holding}
  aria-label="Hold to fast-forward the wait (up to 4 times faster)"
  aria-describedby="ff-live"
  data-testid={testid}
  data-speed={clock.target}
  onpointerdown={down}
  onpointerup={() => clock?.release()}
  onpointerleave={() => clock?.release()}
  onpointercancel={() => clock?.release()}
  onblur={() => clock?.release()}
  onkeydown={keydown}
  onkeyup={keyup}
  oncontextmenu={(e) => e.preventDefault()}
>
  <Icon name="fastForward" size={15} />
  <span>Hold to fast-forward</span>
</button>
<span id="ff-live" class="sr-only" aria-live="polite" data-testid="ff-live">{clock.target === 1 ? 'Normal speed' : `Speed ×${clock.target}`}</span>

<style>
  .ff {
    touch-action: none;
    user-select: none;
    -webkit-user-select: none;
    -webkit-touch-callout: none;
    -webkit-tap-highlight-color: transparent;
    position: relative;
    overflow: hidden;
  }
  .ff.on {
    --btn-bg: var(--accent-soft-2);
    --btn-bd: var(--accent);
    --btn-fg: var(--accent-text);
  }
  .ff.on::after {
    content: '';
    position: absolute;
    inset: 0;
    background: linear-gradient(100deg, transparent 30%, color-mix(in srgb, var(--accent) 22%, transparent) 50%, transparent 70%);
    animation: ff-sheen 0.9s linear infinite;
    pointer-events: none;
  }
  @keyframes ff-sheen {
    from {
      transform: translateX(-100%);
    }
    to {
      transform: translateX(100%);
    }
  }
</style>
