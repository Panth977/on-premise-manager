/**
 * §21 #95 hold-to-fast-forward: the client side of an intent wait (approve, rollback to a snapshot, reset a volume).
 *
 * The AGENT is authoritative (it integrates Σ speed × dt on its own clock and refuses ACCEPT before the wait is done).
 * This clock only animates between the agent's answers:
 *   - hold → request ×2 at once, ×4 after INTENT_SPEED_X4_AFTER_MS of continuous holding; a heartbeat every
 *     INTENT_SPEED_HEARTBEAT_MS while > 1 (the agent decays to ×1 after 1.5 s without one);
 *   - release → request ×1 at once; the SHOWN speed eases back to ×1 over INTENT_SPEED_EASE_MS;
 *   - every agent answer (speed / heartbeat / a 1 s poll) re-anchors the estimate; the shown time converges to it
 *     smoothly: ahead of the agent it slows down (never runs backwards), behind it catches up; > 3 s off it snaps.
 * Ready = the agent says ready (a final GET when the shown time reaches 0).
 */
import {
  INTENT_SPEED_EASE_MS,
  INTENT_SPEED_HEARTBEAT_MS,
  INTENT_SPEED_X4_AFTER_MS,
  type ApproveIntent,
  type IntentProgress,
  type IntentSpeed,
} from '@opm/shared/types';

export interface IntentApi {
  get: (intentId: string) => Promise<IntentProgress>;
  speed: (intentId: string, speed: IntentSpeed) => Promise<IntentProgress>;
}

const POLL_MS = 1000;
const now = () => performance.now();

export class IntentClock {
  /** the effective wait the agent requires */
  waitSec = $state(60);
  /** shown remaining seconds (animated) */
  remaining = $state(60);
  /** shown speed (eases down on release) */
  shownSpeed = $state(1);
  /** the speed requested from the agent */
  target = $state<IntentSpeed>(1);
  holding = $state(false);
  ready = $state(false);
  /** the agent's last answer */
  server = $state<IntentProgress | null>(null);

  #id: string;
  #api: IntentApi;
  #onready: () => void;
  #anchorRemaining = 60;
  #anchorAt = 0;
  #anchorSpeed = 1;
  #easeFrom = 1;
  #easeAt = 0;
  #raf = 0;
  #last = 0;
  #poll: ReturnType<typeof setInterval> | undefined;
  #beat: ReturnType<typeof setInterval> | undefined;
  #x4: ReturnType<typeof setTimeout> | undefined;
  #checking = false;
  #gone = false;

  constructor(intent: ApproveIntent, api: IntentApi, onready: () => void, agentNowMs: number) {
    this.#id = intent.intentId;
    this.#api = api;
    this.#onready = onready;
    const left = Math.max(0, (Date.parse(intent.notBefore) - agentNowMs) / 1000);
    this.waitSec = intent.waitSec ?? Math.max(1, Math.round(left));
    this.remaining = left;
    this.#anchor(left, 1);
  }

  /** the badge value: ×1 / ×2 / ×4 (the eased speed snapped to the nearest step) */
  get badge(): IntentSpeed {
    return this.shownSpeed >= 3 ? 4 : this.shownSpeed >= 1.5 ? 2 : 1;
  }
  get progress(): number {
    return Math.max(0, Math.min(1, 1 - this.remaining / Math.max(1, this.waitSec)));
  }
  /** shortest possible real wait left if held at ×4 from now */
  get minLeftSec(): number {
    return this.remaining / 4;
  }

  start() {
    this.#last = now();
    const frame = () => {
      this.#frame();
      this.#raf = requestAnimationFrame(frame);
    };
    this.#raf = requestAnimationFrame(frame);
    this.#poll = setInterval(() => void this.#sync(this.#api.get(this.#id)), POLL_MS);
    void this.#sync(this.#api.get(this.#id));
  }

  stop() {
    cancelAnimationFrame(this.#raf);
    clearInterval(this.#poll);
    this.#stopHold();
    if (this.target > 1 && !this.#gone) void this.#api.speed(this.#id, 1).catch(() => {});
    this.target = 1;
    this.holding = false;
    this.#gone = true;
  }

  press() {
    if (this.holding || this.ready || this.#gone) return;
    this.holding = true;
    this.#request(2);
    this.#beat = setInterval(() => {
      if (this.holding && this.target > 1) this.#send(this.target);
    }, INTENT_SPEED_HEARTBEAT_MS);
    this.#x4 = setTimeout(() => {
      if (this.holding) this.#request(4);
    }, INTENT_SPEED_X4_AFTER_MS);
  }

  release() {
    if (!this.holding) return;
    this.holding = false;
    this.#stopHold();
    this.#easeFrom = this.shownSpeed;
    this.#easeAt = now();
    this.target = 1;
    this.#send(1);
  }

  #stopHold() {
    clearInterval(this.#beat);
    clearTimeout(this.#x4);
    this.#beat = undefined;
    this.#x4 = undefined;
  }

  #request(s: IntentSpeed) {
    this.target = s;
    this.shownSpeed = s; // speeding up is immediate
    this.#send(s);
  }

  #send(s: IntentSpeed) {
    if (this.#gone) return;
    void this.#sync(this.#api.speed(this.#id, s));
  }

  #anchor(remaining: number, speed: number) {
    this.#anchorRemaining = remaining;
    this.#anchorAt = now();
    this.#anchorSpeed = speed;
  }

  async #sync(p: Promise<IntentProgress>) {
    try {
      const r = await p;
      if (this.#gone) return;
      this.server = r;
      this.waitSec = r.waitSec;
      // the agent applies r.speed until speedUntil (then ×1); the release we just sent applies ×1 at once
      this.#anchor(r.remainingSec, this.holding ? r.speed : 1);
      if (r.ready) this.#markReady();
    } catch (e) {
      // not-found: the intent is gone (expired / used / cancelled elsewhere) — the flow's confirm will report it
      if ((e as { code?: string })?.code === 'not-found') {
        this.#gone = true;
        this.#stopHold();
        clearInterval(this.#poll);
      }
    }
  }

  #markReady() {
    if (this.ready) return;
    this.ready = true;
    this.remaining = 0;
    this.#stopHold();
    this.holding = false;
    this.target = 1;
    this.shownSpeed = 1;
    clearInterval(this.#poll);
    cancelAnimationFrame(this.#raf);
    this.#onready();
  }

  #frame() {
    const t = now();
    const dt = Math.min(0.25, (t - this.#last) / 1000);
    this.#last = t;
    // shown speed: immediate up, eased down after a release
    if (!this.holding && this.shownSpeed > 1) {
      const k = Math.min(1, (t - this.#easeAt) / INTENT_SPEED_EASE_MS);
      const eased = this.#easeFrom + (1 - this.#easeFrom) * (1 - (1 - k) * (1 - k)); // ease-out
      this.shownSpeed = k >= 1 ? 1 : eased;
    }
    // the agent's estimate (authoritative) …
    const est = Math.max(0, this.#anchorRemaining - ((t - this.#anchorAt) / 1000) * this.#anchorSpeed);
    const err = est - this.remaining; // > 0: the shown time runs ahead of the agent
    let next: number;
    if (err > 3) next = est; // far off (e.g. a stalled tab): snap
    else if (err > 0) next = this.remaining - dt * this.shownSpeed * Math.max(0, 1 - err); // ahead: slow down, never back
    else next = this.remaining - dt * this.shownSpeed + err * Math.min(1, dt * 4); // behind: catch up smoothly
    this.remaining = Math.max(0, next);
    if (this.remaining <= 0.05 && !this.ready && !this.#checking) {
      this.#checking = true;
      void this.#sync(this.#api.get(this.#id)).finally(() => {
        setTimeout(() => (this.#checking = false), 250);
      });
    }
  }
}
