import { test } from 'node:test';
import assert from 'node:assert/strict';
import { allowed, can, canSeeDeployment, fullAdminRules, isAdmin, validateAccessRules } from './access.js';
import type { Account } from './types.js';

const acc = (access: Account['access'], extra: Partial<Account> = {}): Account => ({
  uid: 'u_kiran', img: null, name: 'Kiran', email: 'kiran@ops.example', phone: null, access, disabled: false, createdAt: '2026-10-07T00:00:00Z', createdBy: 'u_admin', lastLoginAt: null, lastSetPassword: null, lastResetLink: null, ...extra,
});
const DEP = { deploymentId: 'dAcme', tagSlugs: ['manufacturing', 'gov'], managerAccountUids: ['u_mgr'] };

test('deny by default', () => {
  assert.equal(allowed(acc([]), 'deployment.config', 'read', DEP), false);
  assert.equal(allowed(null, 'services', 'read'), false);
});

test('tag scope matches when the deployment has one of the tags', () => {
  const a = acc([{ resource: 'deployment.config', actions: ['read', 'write'], scope: { kind: 'tags', tagSlugs: ['manufacturing'] } }]);
  assert.equal(allowed(a, 'deployment.config', 'write', DEP), true);
  assert.equal(allowed(a, 'deployment.config', 'publish', DEP), false);
  assert.equal(allowed(a, 'deployment.config', 'write', { ...DEP, tagSlugs: ['other'] }), false);
  assert.equal(allowed(a, 'deployment.config', 'write', null), false, 'no target → only scope all');
});

test('deployments scope', () => {
  const a = acc([{ resource: 'deployment.secrets', actions: ['read'], scope: { kind: 'deployments', deploymentIds: ['dAcme'] } }]);
  assert.equal(allowed(a, 'deployment.secrets', 'read', DEP), true);
  assert.equal(allowed(a, 'deployment.secrets', 'read', { ...DEP, deploymentId: 'dOther' }), false);
  assert.equal(canSeeDeployment(a, DEP), true);
  assert.equal(canSeeDeployment(a, { ...DEP, deploymentId: 'dOther' }), false);
});

test('managers get every deployment.* action incl. publish, and deployments.read', () => {
  const m = acc([], { uid: 'u_mgr' });
  assert.equal(allowed(m, 'deployment.config', 'publish', DEP), true);
  assert.equal(allowed(m, 'deployment.secrets', 'read', DEP), true);
  assert.equal(allowed(m, 'deployments', 'read', DEP), true);
  assert.equal(allowed(m, 'deployments', 'write', DEP), false);
});

test('developer resources need scope all; implicit catalog read', () => {
  const a = acc([{ resource: 'templates', actions: ['read', 'write'], scope: { kind: 'tags', tagSlugs: ['gov'] } }]);
  assert.equal(allowed(a, 'templates', 'write'), false);
  assert.equal(allowed(acc([]), 'services', 'read'), true);
  assert.equal(allowed(acc([]), 'services', 'write'), false);
  assert.equal(allowed(acc([]), 'registries', 'read'), false);
});

test('disabled account is refused even with rules', () => {
  const a = acc(fullAdminRules(), { disabled: true });
  assert.equal(can(a, 'services', 'read').allowed, false);
  assert.equal(isAdmin(acc(fullAdminRules())), true);
});

test('validateAccessRules', () => {
  assert.deepEqual(validateAccessRules(fullAdminRules()), []);
  const bad = validateAccessRules([
    { resource: 'templates', actions: ['read'], scope: { kind: 'tags', tagSlugs: ['gov'] } },
    { resource: 'deployment.config', actions: ['admin'], scope: { kind: 'all' } },
    { resource: 'nope', actions: ['read'], scope: { kind: 'all' } },
    { resource: 'deployments', actions: ['read'], scope: { kind: 'tags', tagSlugs: ['missing'] } },
  ], ['gov']);
  assert.deepEqual(bad.map((x) => x.code), ['V_ACCESS_SCOPE', 'V_ACCESS_ACTION', 'V_ENUM', 'V_REF']);
});
