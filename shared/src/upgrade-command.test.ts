// SPEC-final §21 #92 — the enrolled site's upgrade / reinstall command (install | maintenance tokens) + agent clock skew.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { CLOUD_API } from './cloud-api.js';
import { ACTIVITY_TYPES, AUDIT_ACTIONS, AUDIT_DEPLOYMENT_OPS, AUDIT_OPS, CLOCK_SKEW_WARN_SEC, CREDENTIAL_OPS, INSTALL_TOKEN_KINDS, clockSkewWarning, formatSkewDuration, installTokenState } from './types.js';

test('token kinds', () => {
  assert.deepEqual([...INSTALL_TOKEN_KINDS], ['install', 'maintenance']);
});

test('upgrade command endpoints: credential ops, audited, cloud activities in the catalog', () => {
  for (const op of ['revealUpgradeCommand', 'regenerateUpgradeCommand'] as const) {
    const d = CLOUD_API[op];
    assert.equal(d.method, 'POST');
    assert.match(d.path, /^\/v1\/deployments\/\{deploymentId\}\/agents\/\{siteSlug\}\/upgrade-command/);
    assert.ok((CREDENTIAL_OPS as readonly string[]).includes(op), op);
    assert.ok((AUDIT_DEPLOYMENT_OPS as readonly string[]).includes(op), op);
    assert.ok((AUDIT_ACTIONS as readonly string[]).includes(AUDIT_OPS[op].action), op);
  }
  for (const a of ['install-command.revealed', 'install-command.regenerated']) assert.ok((ACTIVITY_TYPES as readonly string[]).includes(a), a);
});

test('installTokenState: maintenance never expires / is never used', () => {
  const now = Date.parse('2026-10-08T12:00:00Z');
  assert.equal(installTokenState({ kind: 'maintenance', usedAt: null, revokedAt: null, expiresAt: null }, now), 'active');
  assert.equal(installTokenState({ kind: 'maintenance', usedAt: null, revokedAt: '2026-10-08T11:00:00Z', expiresAt: null }, now), 'revoked');
  assert.equal(installTokenState({ usedAt: null, revokedAt: null, expiresAt: '2026-10-08T11:00:00Z' }, now), 'expired');
  assert.equal(installTokenState({ kind: 'install', usedAt: null, revokedAt: null, expiresAt: '2026-10-08T13:00:00Z' }, now), 'active');
  assert.equal(installTokenState({ kind: 'install', usedAt: '2026-10-08T10:00:00Z', revokedAt: null, expiresAt: '2026-10-08T11:00:00Z' }, now), 'used');
  // a used token revoked by Regenerate of the upgrade command shows revoked
  assert.equal(installTokenState({ kind: 'install', usedAt: '2026-10-08T10:00:00Z', revokedAt: '2026-10-08T11:30:00Z', expiresAt: '2026-10-08T11:00:00Z' }, now), 'revoked');
});

test('clock skew warning: > 2 min only, ahead / behind, human durations', () => {
  assert.equal(CLOCK_SKEW_WARN_SEC, 120);
  assert.equal(clockSkewWarning(null), null);
  assert.equal(clockSkewWarning(0), null);
  assert.equal(clockSkewWarning(120), null);
  assert.equal(clockSkewWarning(-120), null);
  assert.match(clockSkewWarning(121)!, /^agent clock is 2 min ahead — fix NTP on the server/);
  assert.match(clockSkewWarning(5 * 3600)!, /^agent clock is 5 h ahead/);
  assert.match(clockSkewWarning(-19800)!, /^agent clock is 5 h 30 min behind/);
  assert.equal(formatSkewDuration(59), '59 s');
  assert.equal(formatSkewDuration(3599), '60 min');
  assert.equal(formatSkewDuration(3 * 86400 + 7200), '3 d 2 h');
});
