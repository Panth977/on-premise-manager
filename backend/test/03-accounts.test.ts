import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fullAdminRules } from '@opm/shared';
import { adb, idToken, person, rejects, uniq } from './helpers.ts';
import { createCloudClient } from '@opm/shared';
import { BASE } from './helpers.ts';

test('accounts: create (generated password + audit), email conflict, validation', async () => {
  const admin = await person('admin');
  const email = `${uniq('new')}@Opm.Test`;
  const r = await admin.api.createAccount({ body: { name: 'New Person', email, phone: '+919876543210' } });
  assert.equal(r.account.email, email.toLowerCase());
  assert.equal(r.account.createdBy, admin.uid);
  assert.match(r.value, /^[A-Za-z0-9]{20}$/);
  assert.equal(r.hideAfterSec, 60);
  assert.equal(r.account.lastSetPassword?.setBy, admin.uid);
  assert.ok(!JSON.stringify(r.account).includes(r.value));
  await rejects(admin.api.createAccount({ body: { name: 'Dup', email } }), 'conflict', 409);
  await rejects(admin.api.createAccount({ body: { name: 'Bad', email: 'not-an-email' } }), 'invalid');
  await rejects(admin.api.createAccount({ body: { name: 'Bad', email: `${uniq('p')}@x.io`, phone: '12345' } }), 'invalid');
  await rejects(admin.api.createAccount({ body: { name: 'Bad', email: `${uniq('p')}@x.io`, access: [{ resource: 'registries', actions: ['read'], scope: { kind: 'tags', tagSlugs: ['nope'] } }] } }), 'invalid');
  const audit = await admin.api.listAuditLog({});
  assert.ok(audit.items.some((a) => a.action === 'account.created' && a.target === `accounts/${r.account.uid}`));
  const reset = await admin.api.resetAccountPassword({ uid: r.account.uid });
  assert.match(reset.value, /^https?:\/\//);
  assert.equal(reset.passwordSetLink, reset.value);
  const all = await admin.api.listAccounts({});
  assert.ok(all.items.some((a) => a.uid === r.account.uid));
});

test('accounts: own profile always; access needs accounts.admin; get own always', async () => {
  const admin = await person('admin');
  const me = await person([]);
  assert.equal((await me.api.getAccount({ uid: me.uid })).uid, me.uid);
  await rejects(me.api.getAccount({ uid: admin.uid }), 'forbidden');
  assert.equal((await me.api.patchAccount({ uid: me.uid, body: { name: 'Renamed', img: 'https://x.example/a.png' } })).name, 'Renamed');
  await rejects(me.api.patchAccount({ uid: me.uid, body: { access: fullAdminRules() } }), 'forbidden');
  await rejects(me.api.patchAccount({ uid: admin.uid, body: { name: 'hack' } }), 'forbidden');
  await rejects(me.api.patchAccount({ uid: me.uid, body: { email: 'x@y.z' } as never }), 'invalid');
  const p = await admin.api.patchAccount({ uid: me.uid, body: { access: [{ resource: 'templates', actions: ['read'], scope: { kind: 'all' } }] } });
  assert.equal(p.access.length, 1);
  assert.ok((await me.api.listTemplates({})).items);
  const audit = await admin.api.listAuditLog({});
  assert.ok(audit.items.some((a) => a.action === 'account.access-changed' && a.target === `accounts/${me.uid}`));
  await rejects(admin.api.patchAccount({ uid: me.uid, body: { access: [{ resource: 'templates', actions: ['publish'], scope: { kind: 'all' } }] } }), 'invalid');
});

test('accounts: disable / enable; self and last admin protected; disabled tokens rejected', async () => {
  const admin = await person('admin');
  await rejects(admin.api.disableAccount({ uid: admin.uid, body: { disabled: true } }), 'conflict');
  const other = await person([]);
  const d = await admin.api.disableAccount({ uid: other.uid, body: { disabled: true } });
  assert.equal(d.disabled, true);
  await rejects(other.api.getMe({}), 'forbidden');
  const audit = await admin.api.listAuditLog({});
  assert.ok(audit.items.some((a) => a.action === 'account.disabled' && a.target === `accounts/${other.uid}`));
  await admin.api.disableAccount({ uid: other.uid, body: { disabled: false } });
  // last-admin rule: make every other admin non-admin in a scratch view → simulate with a lone admin
  const admins = (await adb.collection('accounts').get()).docs.filter((x) => (x.data().access as { resource: string; actions: string[] }[]).some((r) => r.resource === 'accounts' && r.actions.includes('admin')) && !x.data().disabled);
  const saved = admins.filter((x) => x.id !== admin.uid).map((x) => ({ id: x.id, data: x.data() }));
  try {
    for (const s of saved) await adb.collection('accounts').doc(s.id).update({ disabled: true });
    const helper = await person([{ resource: 'accounts', actions: ['read', 'write'], scope: { kind: 'all' } }]);
    void helper;
    await rejects(admin.api.patchAccount({ uid: admin.uid, body: { access: [] } }), 'conflict');
    const other2 = await person('admin');
    // now there are 2 admins → removing one is fine
    await admin.api.patchAccount({ uid: other2.uid, body: { access: [] } });
  } finally {
    for (const s of saved) await adb.collection('accounts').doc(s.id).update({ disabled: s.data.disabled ?? false });
  }
});

test('audit log paging with cursor', async () => {
  const admin = await person('admin');
  for (let i = 0; i < 3; i++) await admin.api.createAccount({ body: { name: 'P', email: `${uniq('pg')}@opm.test` } });
  const p1 = await admin.api.listAuditLog({ query: { limit: 2 } });
  assert.equal(p1.items.length, 2);
  assert.ok(p1.cursor);
  const p2 = await admin.api.listAuditLog({ query: { limit: 2, cursor: p1.cursor! } });
  assert.ok(p2.items.length >= 1);
  assert.notEqual(p2.items[0]!.auditId, p1.items[1]!.auditId);
  assert.ok(p2.items[0]!.ts <= p1.items[1]!.ts);
  void idToken;
  void createCloudClient;
  void BASE;
});
