// SPEC-final §21 #84 — every credential viewable any time: contract consistency of the reveal / regenerate endpoints.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { CLOUD_API } from './cloud-api.js';
import { AGENT_API } from './agent-api.js';
import { auditDiff, redactSecrets, SECRET_MASK } from './audit.js';
import { ACTIVITY_TYPES, AUDIT_ACTIONS, AUDIT_DEPLOYMENT_OPS, AUDIT_OPS, CREDENTIAL_OPS, REVEAL_RATE_PER_MINUTE, REVEAL_SECONDS } from './types.js';

test('reveal constants', () => {
  assert.equal(REVEAL_SECONDS, 60);
  assert.equal(REVEAL_RATE_PER_MINUTE, 30);
});

test('every credential op is a POST human CLOUD_API endpoint that may answer rate-limited', () => {
  for (const op of CREDENTIAL_OPS) {
    const def = (CLOUD_API as Record<string, { method: string; auth: string; errors: readonly string[] }>)[op];
    assert.ok(def, `CLOUD_API.${op} exists`);
    assert.equal(def.method, 'POST', op);
    assert.equal(def.auth, 'human', op);
    assert.ok(def.errors.includes('rate-limited'), `${op} lists rate-limited`);
  }
});

test('every reveal endpoint is a credential op and is audited', () => {
  const reveals = Object.entries(CLOUD_API).filter(([, d]) => /reveal/.test((d as { path: string }).path)).map(([k]) => k);
  assert.deepEqual(reveals.sort(), ['revealAccessKey', 'revealAccountPassword', 'revealAccountResetLink', 'revealGithubToken', 'revealInstallCommand', 'revealRegistryPassword', 'revealSecret', 'revealUpgradeCommand'].sort());
  for (const op of reveals) {
    assert.ok((CREDENTIAL_OPS as readonly string[]).includes(op), `${op} in CREDENTIAL_OPS`);
    assert.ok(op in AUDIT_OPS, `${op} in AUDIT_OPS`);
  }
});

test('deployment-scoped audited ops have an audit spec, a deployment path and a cloud activity', () => {
  for (const op of AUDIT_DEPLOYMENT_OPS) {
    const spec = (AUDIT_OPS as Record<string, { action: string; targetType: string }>)[op];
    assert.ok(spec, op);
    assert.equal(spec.targetType, 'deployment');
    assert.ok((AUDIT_ACTIONS as readonly string[]).includes(spec.action), spec.action);
    const def = (CLOUD_API as Record<string, { path: string; activities?: readonly string[] }>)[op]!;
    assert.match(def.path, /^\/v1\/deployments\/\{deploymentId\}\/.+/);
    const acts = (def.activities ?? []).filter((a) => !a.startsWith('audit:'));
    assert.ok(acts.length > 0, `${op} writes a _cloud activity`);
    for (const a of acts) assert.ok((ACTIVITY_TYPES as readonly string[]).includes(a), a);
  }
});

test('new activity + audit catalog entries', () => {
  for (const a of ['access-key.revealed', 'install-token.revealed', 'secret.regenerated', 'secret.revealed']) assert.ok((ACTIVITY_TYPES as readonly string[]).includes(a), a);
  for (const a of ['account.password-set', 'account.password-revealed', 'account.reset-link-created', 'account.reset-link-revealed', 'registry.password-revealed']) assert.ok((AUDIT_ACTIONS as readonly string[]).includes(a), a);
});

test('agent GUI secret reveal may be rate-limited', () => {
  assert.ok(AGENT_API.revealSecret.errors.includes('rate-limited'));
});

test('stored credentials are redacted in audit data and diffs', () => {
  const doc = {
    uid: 'u1',
    name: 'Kiran',
    accessKey: 'ABCD-EFGH-IJKL-MNOP-QRST-UVWX',
    token: 'opi_secret',
    lastSetPassword: { value: 'Pa55word-very-long', setAt: '2026-10-08T00:00:00Z', setBy: 'u_admin', validAfter: '2026-10-08T00:00:00Z' },
    lastResetLink: { value: 'https://example/reset?oobCode=x', createdAt: '2026-10-08T00:00:00Z', createdBy: 'u_admin', expiresAt: '2026-10-08T01:00:00Z', validAfter: null },
  };
  const r = redactSecrets(doc);
  assert.equal(r.accessKey, SECRET_MASK);
  assert.equal(r.token, SECRET_MASK);
  assert.equal(r.lastSetPassword.value, SECRET_MASK);
  assert.equal(r.lastSetPassword.setAt, '2026-10-08T00:00:00Z');
  assert.equal(r.lastSetPassword.setBy, 'u_admin');
  assert.equal(r.lastResetLink.value, SECRET_MASK);
  assert.equal(r.lastResetLink.expiresAt, '2026-10-08T01:00:00Z');
  assert.equal(r.name, 'Kiran');
  const d = auditDiff({ ...doc, lastSetPassword: null }, doc);
  const json = JSON.stringify(d.changes);
  assert.ok(!json.includes('Pa55word'), json);
  const d2 = auditDiff(doc, { ...doc, lastSetPassword: { ...doc.lastSetPassword, value: 'Other-pass', setAt: '2026-10-08T02:00:00Z' } });
  assert.ok(!JSON.stringify(d2.changes).includes('Other-pass'));
  assert.ok(d2.changes.some((c) => c.path === 'lastSetPassword.value' && c.secret === true));
  assert.ok(d2.changes.some((c) => c.path === 'lastSetPassword.setAt' && c.after === '2026-10-08T02:00:00Z'));
  const d3 = auditDiff(null, doc);
  assert.ok(!JSON.stringify(d3.changes).includes('Pa55word'));
  assert.ok(!JSON.stringify(d3.changes).includes('oobCode'));
});
