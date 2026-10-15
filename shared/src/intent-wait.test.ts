// SPEC-final §21 #95 — hold-to-fast-forward: server-side integration of an intent wait (Go mirror: intentwait_test.go).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { AGENT_API } from './agent-api.js';
import { advanceIntentWait, fastForwardLabel, fastForwardUse, intentReady, newIntentWait, setIntentSpeed } from './intent-wait.js';
import { INTENT_MAX_SPEED, INTENT_SPEED_DECAY_MS, INTENT_SPEED_HEARTBEAT_MS } from './types.js';

const T0 = 1_000_000;

test('×1 only: ready exactly after waitSec, no fast-forward record', () => {
  const s = newIntentWait(60, T0);
  advanceIntentWait(s, T0 + 59_999);
  assert.equal(intentReady(s), false);
  advanceIntentWait(s, T0 + 60_000);
  assert.equal(intentReady(s), true);
  assert.equal(fastForwardUse(s, 'a@x'), null);
});

test('held at ×4 with heartbeats: minimum real wait = waitSec / 4', () => {
  const s = newIntentWait(60, T0);
  let t = T0;
  setIntentSpeed(s, 4, t);
  while (!intentReady(s)) {
    t += INTENT_SPEED_HEARTBEAT_MS;
    advanceIntentWait(s, t);
    if (!intentReady(s)) setIntentSpeed(s, 4, t);
  }
  assert.equal(t - T0, 15_000);
  assert.equal(INTENT_MAX_SPEED, 4);
  assert.deepEqual(fastForwardUse(s, 'a@x'), { usedBy: 'a@x', maxSpeed: 4, realSeconds: 15, effectiveSeconds: 60 });
  assert.equal(fastForwardLabel(fastForwardUse(s, 'a@x')!), 'fast-forwarded (×4, 15 s real)');
});

test('no heartbeat: a speed decays to ×1 after INTENT_SPEED_DECAY_MS (a lost release cannot keep it fast)', () => {
  const s = newIntentWait(60, T0);
  setIntentSpeed(s, 4, T0);
  advanceIntentWait(s, T0 + 10_000);
  // 1.5 s at ×4 = 6 + 8.5 s at ×1
  assert.equal(s.effective, 4 * (INTENT_SPEED_DECAY_MS / 1000) + 8.5);
  assert.equal(s.speed, 1);
});

test('speed changes integrate piecewise; release (×1) applies at once', () => {
  const s = newIntentWait(60, T0);
  setIntentSpeed(s, 2, T0 + 1000); //   1 s at ×1
  setIntentSpeed(s, 4, T0 + 2000); //   1 s at ×2
  setIntentSpeed(s, 1, T0 + 3000); //   1 s at ×4
  advanceIntentWait(s, T0 + 4000); //   1 s at ×1
  assert.equal(s.effective, 1 + 2 + 4 + 1);
  assert.equal(s.maxSpeed, 4);
});

test('only 1 | 2 | 4; a clock going backwards credits nothing', () => {
  const s = newIntentWait(60, T0);
  for (const bad of [0, 3, 8, -1, 4.5, Number.NaN]) assert.throws(() => setIntentSpeed(s, bad, T0));
  advanceIntentWait(s, T0 + 5000);
  advanceIntentWait(s, T0 + 1000);
  assert.equal(s.effective, 5);
});

test('ready time is interpolated inside a segment (realSeconds)', () => {
  const s = newIntentWait(10, T0);
  setIntentSpeed(s, 2, T0 + 4000); // 4 effective
  advanceIntentWait(s, T0 + 5000); // + 2 = 6 (heartbeat window 1.5 s)
  setIntentSpeed(s, 2, T0 + 5000);
  advanceIntentWait(s, T0 + 6500); // + 3 = 9
  setIntentSpeed(s, 2, T0 + 6500);
  advanceIntentWait(s, T0 + 8000); // reaches 10 at 7.0 s
  assert.equal(fastForwardUse(s, 'b@x')!.realSeconds, 7);
});

test('agent API: intent endpoints', () => {
  assert.equal(AGENT_API.intentSpeed.method, 'POST');
  assert.equal(AGENT_API.intentSpeed.path, '/_opm/api/intents/{intentId}/speed');
  assert.equal(AGENT_API.intentSpeed.auth, 'session+actor');
  assert.equal(AGENT_API.getIntent.auth, 'session');
  assert.equal(AGENT_API.siteIntentSpeed.path, '/_opm/api/sites/{siteSlug}/intents/{intentId}/speed');
  assert.equal(AGENT_API.siteCancelIntent.method, 'DELETE');
});
