/**
 * §21 #95 hold-to-fast-forward: the server-side integration of an intent wait (TS reference; Go mirror
 * agent/internal/agent/intentwait.go — same rules, same test cases). Used by the agent GUI mock.
 *
 * effective = Σ speed × real dt on the SERVER clock. A speed > 1 applies from the moment it was received until the next
 * speed message, or until INTENT_SPEED_DECAY_MS after it (heartbeat deadline) — then ×1. ACCEPT needs effective ≥ waitSec.
 */
import { INTENT_SPEEDS, INTENT_SPEED_DECAY_MS, type FastForwardUse, type IntentSpeed } from './types.js';

export interface IntentWaitState {
  waitSec: number;
  createdMs: number;
  /** effective seconds accumulated up to lastMs */
  effective: number;
  lastMs: number;
  speed: IntentSpeed;
  /** speed > 1 is valid until this time (ms); ignored at ×1 */
  untilMs: number;
  maxSpeed: IntentSpeed;
  /** real time (ms) when effective first reached waitSec; null = not yet */
  readyMs: number | null;
}

export function newIntentWait(waitSec: number, nowMs: number): IntentWaitState {
  return { waitSec, createdMs: nowMs, effective: 0, lastMs: nowMs, speed: 1, untilMs: nowMs, maxSpeed: 1, readyMs: null };
}

function credit(s: IntentWaitState, toMs: number, speed: number) {
  if (toMs <= s.lastMs) return;
  const before = s.effective;
  s.effective += (speed * (toMs - s.lastMs)) / 1000;
  if (s.readyMs === null && s.effective >= s.waitSec) s.readyMs = s.lastMs + ((s.waitSec - before) / speed) * 1000;
  s.lastMs = toMs;
}

/** Integrate up to nowMs (a clock that went backwards credits nothing). */
export function advanceIntentWait(s: IntentWaitState, nowMs: number): void {
  if (nowMs <= s.lastMs) return;
  if (s.speed > 1) {
    credit(s, Math.min(nowMs, s.untilMs), s.speed);
    if (nowMs >= s.untilMs) s.speed = 1;
  }
  if (s.speed === 1) credit(s, nowMs, 1);
}

export function isIntentSpeed(v: unknown): v is IntentSpeed {
  return typeof v === 'number' && (INTENT_SPEEDS as readonly number[]).includes(v);
}

/** A speed message (change or heartbeat) received at nowMs. Throws on a speed other than 1 | 2 | 4. */
export function setIntentSpeed(s: IntentWaitState, speed: number, nowMs: number): void {
  if (!isIntentSpeed(speed)) throw new Error('speed must be 1, 2 or 4');
  advanceIntentWait(s, nowMs);
  s.speed = speed;
  s.untilMs = speed > 1 ? nowMs + INTENT_SPEED_DECAY_MS : nowMs;
  if (speed > s.maxSpeed) s.maxSpeed = speed;
}

export function intentReady(s: IntentWaitState): boolean {
  return s.effective >= s.waitSec - 1e-9;
}

/** The activity / run record; null when the wait was never fast-forwarded. */
export function fastForwardUse(s: IntentWaitState, usedBy: string): FastForwardUse | null {
  if (s.maxSpeed <= 1) return null;
  const readyMs = s.readyMs ?? s.lastMs;
  return { usedBy, maxSpeed: s.maxSpeed, realSeconds: Math.round((readyMs - s.createdMs) / 100) / 10, effectiveSeconds: s.waitSec };
}

/** "fast-forwarded (×4, 18 s real)" — activity summaries + GUI history. */
export function fastForwardLabel(ff: FastForwardUse): string {
  return `fast-forwarded (×${ff.maxSpeed}, ${Math.round(ff.realSeconds)} s real)`;
}
