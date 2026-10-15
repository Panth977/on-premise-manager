import { test } from 'node:test';
import assert from 'node:assert/strict';
import { auditActionKeys, auditActionMatches, auditDiff, capAuditValue, isAuditTruncated, isSecretField, redactSecrets, SECRET_MASK } from './audit.js';
import { AUDIT_ACTIONS, AUDIT_OPS, AUDIT_TARGET_TYPES, ACTIVITY_TYPES } from './types.js';
import { CLOUD_API } from './cloud-api.js';

const tpl = (secretValue: string | null, label = 'T') => ({
  templateId: 't1',
  label,
  version: 1,
  variables: [{ variableSlug: 'port', label: 'Port', type: 'number', value: 8080 }],
  secrets: [{ secretSlug: 'db-pass', label: 'DB password', value: secretValue, generate: { charset: 'alnum', length: 24 } }],
  sites: [{ siteSlug: 'app', label: 'App', containers: [{ serviceSlug: 's1', env: { TOKEN: 'x' } }] }],
  updatedAt: '2026-01-01T00:00:00.000Z',
});

test('isSecretField: keys, identifiers and secret lists', () => {
  assert.equal(isSecretField('password', {}, null), true);
  assert.equal(isSecretField('accessKeyHash', {}, null), true);
  assert.equal(isSecretField('privateKey', {}, null), true);
  assert.equal(isSecretField('apiToken', {}, null), true);
  assert.equal(isSecretField('secretSlug', {}, 'secrets'), false);
  assert.equal(isSecretField('secretSlugs', {}, null), false);
  assert.equal(isSecretField('tokenHash', {}, null), false);
  assert.equal(isSecretField('accessKeyVersion', {}, null), false);
  assert.equal(isSecretField('accessKeyRotatedAt', {}, null), false);
  assert.equal(isSecretField('value', {}, 'secrets'), true);
  assert.equal(isSecretField('plainValue', {}, 'secrets'), true);
  assert.equal(isSecretField('value', {}, 'variables'), false);
  assert.equal(isSecretField('value', { secret: true }, 'env'), true);
  assert.equal(isSecretField('value', { secret: false }, 'env'), false);
  assert.equal(isSecretField('label', {}, null), false);
});

test('redactSecrets masks every secret value and keeps structure', () => {
  const r = redactSecrets({
    registry: { username: 'bot', password: 'hunter2' },
    secrets: [{ secretSlug: 'a', value: 'plain-a' }, { secretSlug: 'b', value: null }, { secretSlug: 'c', plainValue: 'p' }],
    env: [{ name: 'X', value: 'shh', secret: true }, { name: 'Y', value: 'open', secret: false }],
    accessKeyHash: '$argon2id$v=19$…',
    tokenHash: 'abc',
    nested: { deep: { clientSecret: 42, secret: true } },
  });
  const json = JSON.stringify(r);
  for (const s of ['hunter2', 'plain-a', '"p"', 'shh', '$argon2id', '42']) assert.ok(!json.includes(s), `leaked ${s}: ${json}`);
  assert.equal(r.registry.password, SECRET_MASK);
  assert.equal(r.secrets[0]!.value, SECRET_MASK);
  assert.equal(r.secrets[1]!.value, null); // unset stays visible
  assert.equal(r.secrets[0]!.secretSlug, 'a');
  assert.equal(r.env[1]!.value, 'open');
  assert.equal(r.tokenHash, 'abc');
  assert.equal(r.nested.deep.secret, true);
});

test('auditDiff: patch → changed leaves with readable paths; ignores updatedAt', () => {
  const a = tpl('old-secret');
  const b = { ...tpl('new-secret', 'T2'), updatedAt: '2026-02-02T00:00:00.000Z', version: 2 };
  b.variables = [{ ...b.variables[0]!, value: 9090 }];
  b.sites = [{ ...b.sites[0]!, containers: [{ serviceSlug: 's1', env: { TOKEN: 'y' } }] }];
  const d = auditDiff(a, b);
  const byPath = Object.fromEntries(d.changes.map((c) => [c.path, c]));
  assert.deepEqual(Object.keys(byPath).sort(), ['label', 'secrets[db-pass].value', 'sites[app].containers[s1].env.TOKEN', 'variables[port].value', 'version']);
  assert.deepEqual(byPath['label'], { path: 'label', before: 'T', after: 'T2' });
  assert.deepEqual(byPath['variables[port].value'], { path: 'variables[port].value', before: 8080, after: 9090 });
  assert.deepEqual(byPath['secrets[db-pass].value'], { path: 'secrets[db-pass].value', before: SECRET_MASK, after: SECRET_MASK, secret: true });
  assert.equal(byPath['sites[app].containers[s1].env.TOKEN']!.secret, true);
  assert.equal(d.total, 5);
  const json = JSON.stringify(d);
  assert.ok(!json.includes('old-secret') && !json.includes('new-secret'));
});

test('auditDiff: unchanged secret → no change; set/unset keeps null visible', () => {
  assert.equal(auditDiff(tpl('s'), tpl('s')).changes.length, 0);
  const d = auditDiff(tpl(null), tpl('now-set'));
  assert.deepEqual(d.changes, [{ path: 'secrets[db-pass].value', before: null, after: SECRET_MASK, secret: true }]);
});

test('auditDiff: create / delete store the document (redacted), list items added / removed', () => {
  const c = auditDiff(null, { registryId: 'r1', label: 'Hub', password: 'pw', updatedAt: 'x' });
  assert.deepEqual(c.changes, [
    { path: 'label', before: null, after: 'Hub' },
    { path: 'password', before: null, after: SECRET_MASK, secret: true },
    { path: 'registryId', before: null, after: 'r1' },
  ]);
  const del = auditDiff(tpl('zzz'), null);
  assert.ok(del.changes.some((x) => x.path === 'secrets' && JSON.stringify(x.before).includes(SECRET_MASK)));
  assert.ok(!JSON.stringify(del).includes('zzz'));
  const add = auditDiff({ secrets: [] }, { secrets: [{ secretSlug: 'n', value: 'v-v-v' }] });
  assert.deepEqual(add.changes, [{ path: 'secrets[n]', before: null, after: { secretSlug: 'n', value: SECRET_MASK } }]);
});

test('auditDiff: caps (200 changes, 2 KB values)', () => {
  const big: Record<string, number> = {};
  for (let i = 0; i < 300; i++) big[`k${String(i).padStart(3, '0')}`] = i;
  const d = auditDiff({}, big);
  assert.equal(d.changes.length, 200);
  assert.equal(d.total, 300);
  const v = auditDiff({ html: 'a' }, { html: 'x'.repeat(5000) });
  const after = v.changes[0]!.after;
  assert.ok(isAuditTruncated(after));
  assert.equal(after.bytes, 5002);
  assert.equal(after.preview.length, 2000);
  assert.equal(capAuditValue('small'), 'small');
  assert.equal(capAuditValue(undefined), null);
});

test('auditDiff: list order change and index-keyed lists', () => {
  const d = auditDiff({ sites: [{ siteSlug: 'a' }, { siteSlug: 'b' }] }, { sites: [{ siteSlug: 'b' }, { siteSlug: 'a' }] });
  assert.deepEqual(d.changes, [{ path: 'sites[*order]', before: ['a', 'b'], after: ['b', 'a'] }]);
  const e = auditDiff({ cmd: ['a', 'b'] }, { cmd: ['a', 'c', 'd'] });
  assert.deepEqual(e.changes, [
    { path: 'cmd[1]', before: 'b', after: 'c' },
    { path: 'cmd[2]', before: null, after: 'd' },
  ]);
});

test('action filters and keys', () => {
  assert.equal(auditActionMatches('template.updated', 'template.'), true);
  assert.equal(auditActionMatches('template.updated', 'template.updated'), true);
  assert.equal(auditActionMatches('template.updated', 'template'), false);
  assert.deepEqual(auditActionKeys('image.import-ready'), ['image.', 'image.import-ready']);
});

test('catalog: every audited op is a human non-GET op, actions are in AUDIT_ACTIONS', () => {
  for (const [op, a] of Object.entries(AUDIT_OPS)) {
    const def = (CLOUD_API as Record<string, { method: string; auth: string; activities?: readonly string[] }>)[op];
    assert.ok(def, op);
    assert.equal(def.auth, 'human', op);
    assert.notEqual(def.method, 'GET', op);
    assert.ok((AUDIT_ACTIONS as readonly string[]).includes(a.action), a.action);
    assert.ok((AUDIT_TARGET_TYPES as readonly string[]).includes(a.targetType), a.targetType);
    assert.ok(def.activities?.includes(`audit:${a.action}`), `${op} activities must list audit:${a.action}`);
  }
  for (const t of ['draft.updated', 'transfer.deleted', 'activities.uploaded', 'deployment.deleted']) assert.ok((ACTIVITY_TYPES as readonly string[]).includes(t), t);
  assert.equal(new Set(AUDIT_ACTIONS).size, AUDIT_ACTIONS.length);
});
