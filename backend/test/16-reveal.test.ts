/**
 * Round G (SPEC-final §21 #84): nothing is "shown once" — every credential is stored, viewable (60 s) and regenerable,
 * and every reveal / regenerate is logged (deployment ops: _cloud activity + root audit; root ops: audit).
 * Also: Cache-Control no-store on credential ops (success and error), per-uid rate limit (30 / minute) → 429,
 * 410 gone shapes (not-stored / used / revoked / expired / changed), permission denials.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createCloudClient, type Activity, type AccessRule } from '@opm/shared';
import { FieldValue } from '../src/driver/index.js';
import { applyPlanG, planMigrationG } from '../scripts/migrate-g.ts';
import { verifySchema } from '../scripts/verify-schema.ts';
import { aauth, adb, BASE, helloDeployment, idToken, newSiteKey, person, raw, rejects, sleep, uniq, type Person } from './helpers.ts';
import { depInput } from './helpers.ts';

async function cloudActivities(deploymentId: string): Promise<Activity[]> {
  const q = await adb.collection('deployments').doc(deploymentId).collection('activities').where('siteSlug', '==', '_cloud').get();
  return q.docs.map((d) => ({ ...d.data(), data: JSON.parse(d.data()._data) }) as unknown as Activity).sort((a, b) => a.seq - b.seq);
}
async function auditOf(target: string) {
  return (await adb.collection('auditLog').where('target', '==', target).get()).docs.map((d) => {
    const x = d.data();
    return { ...x, changes: JSON.parse(x._changes), data: JSON.parse(x._data) } as Record<string, any>;
  });
}
const post = (p: Person, path: string, body?: unknown) =>
  raw('POST', path, { headers: { Authorization: p.bearer, ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}) }, body: body !== undefined ? JSON.stringify(body) : undefined });
/** wait until a fresh minute window has ≥ 20 s left (rate-limit windows are fixed UTC minutes) */
async function freshWindow() {
  const s = new Date().getUTCSeconds();
  if (s > 40) await sleep((61 - s) * 1000);
}
const allLogs = async () => {
  const audit = (await adb.collection('auditLog').get()).docs.map((d) => JSON.stringify(d.data())).join('\n');
  const acts = (await adb.collectionGroup('activities').get()).docs.map((d) => JSON.stringify(d.data())).join('\n');
  return audit + '\n' + acts;
};

test('access key: stored, revealed any time (activity + audit), rotate = regenerate, not-stored → 410; never in views', async () => {
  await freshWindow();
  const admin = await person('admin');
  const { dep, accessKey } = await helloDeployment(admin);
  const id = dep.deploymentId;
  // create response: value + 60 s
  const view = await admin.api.getDeployment({ deploymentId: id });
  assert.ok(!JSON.stringify(view).includes(accessKey), 'DeploymentView leaks the key');
  assert.ok(!('accessKey' in view));
  const setup = await admin.api.getSetup({ deploymentId: id });
  assert.equal(setup.accessKey.stored, true);
  const r1 = await post(admin, `/v1/deployments/${id}/access-key/reveal`);
  assert.equal(r1.status, 200);
  assert.equal(r1.headers.get('cache-control'), 'no-store');
  assert.equal(r1.json.value, accessKey);
  assert.equal(r1.json.accessKey, accessKey);
  assert.equal(r1.json.hideAfterSec, 60);
  assert.equal(r1.json.version, 1);
  const acts = await cloudActivities(id);
  const rev = acts.find((a) => a.type === 'access-key.revealed')!;
  assert.ok(rev, 'access-key.revealed activity');
  assert.deepEqual(rev.data, { version: 1 });
  assert.equal(rev.actor, admin.uid);
  const au = (await auditOf(`deployments/${id}`)).filter((e) => e.action === 'deployment.access-key-revealed');
  assert.equal(au.length, 1);
  assert.deepEqual(au[0]!.data, { version: 1 });
  assert.equal(au[0]!.actor, admin.uid);
  // regenerate
  const rot = await admin.api.rotateAccessKey({ deploymentId: id });
  assert.notEqual(rot.value, accessKey);
  assert.equal(rot.value, rot.accessKey);
  assert.equal(rot.hideAfterSec, 60);
  assert.equal((await admin.api.revealAccessKey({ deploymentId: id })).value, rot.value);
  assert.equal((await auditOf(`deployments/${id}`)).filter((e) => e.action === 'deployment.access-key-rotated').length, 1);
  // agents never get the plaintext: enroll response + cloud view
  const tok = await admin.api.createInstallToken({ deploymentId: id, body: { siteSlug: 'app' } });
  const key = newSiteKey();
  const anon = createCloudClient(BASE, () => `Bearer ${tok.token}`);
  const en = await anon.enroll({ body: { publicKey: key.publicKey, agentVersion: '0.1.0-test', hostname: 'h', ip: '127.0.0.1', advertiseHost: '127.0.0.1', agentPort: 7080, os: 'darwin', arch: 'arm64' } });
  assert.ok(!JSON.stringify(en).includes(rot.value) && !JSON.stringify(en).includes(rot.value.replace(/-/g, '')));
  const { agentClient } = await import('./helpers.ts');
  const poll = await agentClient(key, id, 'app').poll({ query: { wait: 0 } });
  assert.ok(!JSON.stringify(poll).includes(rot.value));
  // never in audit / activities
  const logs = await allLogs();
  for (const k of [accessKey, rot.value, accessKey.replace(/-/g, ''), rot.value.replace(/-/g, '')]) assert.ok(!logs.includes(k), 'access key leaked into a log');
  // pre-#84 deployment: not stored → 410 gone reason not-stored (no-store on the error too)
  await adb.collection('deployments').doc(id).update({ accessKey: null });
  assert.equal((await admin.api.getSetup({ deploymentId: id })).accessKey.stored, false);
  const g = await post(admin, `/v1/deployments/${id}/access-key/reveal`);
  assert.equal(g.status, 410);
  assert.equal(g.json.error.code, 'gone');
  assert.deepEqual(g.json.error.details, { reason: 'not-stored' });
  assert.equal(g.headers.get('cache-control'), 'no-store');
  // patchDeployment audit diff does not carry the key
  const rot2 = await admin.api.rotateAccessKey({ deploymentId: id });
  await admin.api.patchDeployment({ deploymentId: id, body: { label: uniq('renamed') } });
  const patch = (await auditOf(`deployments/${id}`)).find((e) => e.action === 'deployment.updated')!;
  assert.ok(!JSON.stringify(patch).includes(rot2.value));
  assert.ok(!patch.changes.some((c: { path: string }) => c.path === 'accessKey'));
});

test('install commands: stored while active, revealed (activity + audit), replaceActive, gone reasons', async () => {
  await freshWindow();
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const t1 = await admin.api.createInstallToken({ deploymentId: id, body: { siteSlug: 'app' } });
  assert.equal(t1.value, t1.command);
  assert.ok(t1.command.includes(t1.token));
  assert.equal(t1.siteSlug, 'app');
  assert.equal(t1.hideAfterSec, 60);
  const setup = await admin.api.getSetup({ deploymentId: id });
  const views = setup.sites.find((s) => s.siteSlug === 'app')!.tokens;
  assert.equal(views[0]!.stored, true);
  assert.equal(views[0]!.state, 'active');
  assert.ok(!JSON.stringify(setup).includes(t1.token), 'getSetup leaks the token');
  const r = await post(admin, `/v1/deployments/${id}/install-tokens/${t1.tokenHash}/reveal`);
  assert.equal(r.status, 200);
  assert.equal(r.headers.get('cache-control'), 'no-store');
  assert.equal(r.json.value, t1.command);
  assert.equal(r.json.siteSlug, 'app');
  const act = (await cloudActivities(id)).find((a) => a.type === 'install-token.revealed')!;
  assert.deepEqual(act.data, { siteSlug: 'app', tokenHash: t1.tokenHash });
  const au = (await auditOf(`deployments/${id}`)).filter((e) => e.action === 'deployment.install-command-revealed');
  assert.equal(au.length, 1);
  assert.deepEqual(au[0]!.data, { siteSlug: 'app', tokenHash: t1.tokenHash });
  // Regenerate: replaceActive revokes the active one (activity per revoked token, token nulled)
  const t2 = await admin.api.createInstallToken({ deploymentId: id, body: { siteSlug: 'app', replaceActive: true } });
  const old = (await adb.collection('deployments').doc(id).collection('installTokens').doc(t1.tokenHash).get()).data()!;
  assert.ok(old.revokedAt);
  assert.equal(old.token, null);
  assert.ok((await cloudActivities(id)).some((a) => a.type === 'install-token.revoked' && a.data.tokenHash === t1.tokenHash));
  const gRevoked = await rejects(admin.api.revealInstallCommand({ deploymentId: id, tokenHash: t1.tokenHash }), 'gone', 410);
  assert.deepEqual(gRevoked.details, { reason: 'revoked' });
  assert.equal((await admin.api.getSetup({ deploymentId: id })).sites.find((s) => s.siteSlug === 'app')!.tokens.filter((t) => t.state === 'active').length, 1);
  // used: enroll KEEPS the plaintext (§21 #92: it is now the site's upgrade command, revealUpgradeCommand); the fresh-site
  // install command view still answers gone/used
  const anon = createCloudClient(BASE, () => `Bearer ${t2.token}`);
  await anon.enroll({ body: { publicKey: newSiteKey().publicKey, agentVersion: '0.1.0-test', hostname: 'h', ip: '127.0.0.1', advertiseHost: '127.0.0.1', agentPort: 7080, os: 'darwin', arch: 'arm64' } });
  assert.equal((await adb.collection('deployments').doc(id).collection('installTokens').doc(t2.tokenHash).get()).data()!.token, t2.token);
  const gUsed = await rejects(admin.api.revealInstallCommand({ deploymentId: id, tokenHash: t2.tokenHash }), 'gone', 410);
  assert.deepEqual(gUsed.details, { reason: 'used' });
  // not-stored (pre-#84 token) and expired
  const t3 = await admin.api.createInstallToken({ deploymentId: id, body: { siteSlug: 'db' } });
  await adb.collection('deployments').doc(id).collection('installTokens').doc(t3.tokenHash).update({ token: null });
  const st = (await admin.api.getSetup({ deploymentId: id })).sites.find((s) => s.siteSlug === 'db')!.tokens.find((t) => t.tokenHash === t3.tokenHash)!;
  assert.equal(st.stored, false);
  assert.deepEqual((await rejects(admin.api.revealInstallCommand({ deploymentId: id, tokenHash: t3.tokenHash }), 'gone', 410)).details, { reason: 'not-stored' });
  const t4 = await admin.api.createInstallToken({ deploymentId: id, body: { siteSlug: 'db' } });
  await adb.collection('deployments').doc(id).collection('installTokens').doc(t4.tokenHash).update({ expiresAt: new Date(Date.now() - 1000).toISOString() });
  assert.deepEqual((await rejects(admin.api.revealInstallCommand({ deploymentId: id, tokenHash: t4.tokenHash }), 'gone', 410)).details, { reason: 'expired' });
  // the daily job nulls the plaintext of expired tokens
  const { runDaily } = await import('../src/jobs/daily.ts');
  const d = await runDaily();
  assert.ok(d.tokensCleared >= 1);
  assert.equal((await adb.collection('deployments').doc(id).collection('installTokens').doc(t4.tokenHash).get()).data()!.token, null);
  // revoke nulls the token
  const t5 = await admin.api.createInstallToken({ deploymentId: id, body: { siteSlug: 'db' } });
  await admin.api.revokeInstallToken({ deploymentId: id, tokenHash: t5.tokenHash });
  assert.equal((await adb.collection('deployments').doc(id).collection('installTokens').doc(t5.tokenHash).get()).data()!.token, null);
  const logs = await allLogs();
  for (const t of [t1.token, t2.token, t3.token, t4.token, t5.token]) assert.ok(!logs.includes(t), 'install token leaked into a log');
});

test('secrets: reveal 60 s (activity + audit); §21 #105 regenerate = generateNow in a commit (slugs in data / activity / audit), the old route 410', async () => {
  await freshWindow();
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const r = await post(admin, `/v1/deployments/${id}/secrets/api-token/reveal`);
  assert.equal(r.status, 200);
  assert.equal(r.headers.get('cache-control'), 'no-store');
  assert.equal(r.json.hideAfterSec, 60);
  const v1 = r.json.value as string;
  const sr = (await auditOf(`deployments/${id}`)).filter((e) => e.action === 'deployment.secret-revealed' && e.data.secretSlug === 'api-token');
  assert.equal(sr.length, 1, JSON.stringify(sr));
  // §21 #105: regenerate is a pending change (generateNow) written by the next commit; the route answers 410
  const dv = (await admin.real.getDeployment({ deploymentId: id })).draftVersion;
  const gone = await rejects(admin.real.regenerateSecret({ deploymentId: id, secretSlug: 'api-token', body: { ifRevision: dv } }), 'gone', 410);
  assert.ok(gone.details.hint);
  const cfg = await depInput(admin.real, id);
  cfg.secrets = cfg.secrets.map((s) => (s.secretSlug === 'api-token' ? { ...s, generateNow: true } : s));
  const cm = await admin.real.commitDeployment({ deploymentId: id, body: { message: 'new api token', baseCommitId: (await admin.real.getDeployment({ deploymentId: id })).branch!.head!.commitId, config: cfg } });
  assert.deepEqual(cm.data.secretValues, { set: [], generated: ['api-token'], cleared: [] });
  const g = await admin.api.revealSecret({ deploymentId: id, secretSlug: 'api-token' });
  assert.notEqual(g.value, v1);
  assert.match(g.value, /^[A-Za-z0-9]{24}$/);
  const acts = await cloudActivities(id);
  const bc = acts.filter((a) => a.type === 'branch.committed').at(-1)!;
  assert.deepEqual((bc.data as Record<string, unknown>).secretValues, { set: [], generated: ['api-token'], cleared: [] });
  const au = (await auditOf(`deployments/${id}`)).filter((e) => e.action === 'deployment.committed' && e.data.commitId === cm.commitId);
  assert.equal(au.length, 1);
  assert.deepEqual(au[0]!.data.secretValues, { set: [], generated: ['api-token'], cleared: [] });
  const logs = await allLogs();
  for (const v of [v1, g.value]) assert.ok(!logs.includes(v), 'secret value leaked into a log');
});

test('registry password reveal: 60 s, value = password, audited', async () => {
  await freshWindow();
  const admin = await person('admin');
  const pw = `REG-${uniq()}`;
  const reg = await admin.api.createRegistry({ body: { label: 'Reveal', url: 'https://127.0.0.1:1', username: 'u', password: pw } });
  const r = await post(admin, `/v1/registries/${reg.registryId}/reveal-password`);
  assert.equal(r.status, 200);
  assert.equal(r.headers.get('cache-control'), 'no-store');
  assert.deepEqual(r.json, { value: pw, password: pw, hideAfterSec: 60 });
});

test('permissions: every reveal / regenerate needs its permission (403)', async () => {
  await freshWindow();
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const tok = await admin.api.createInstallToken({ deploymentId: id, body: { siteSlug: 'app' } });
  const reg = await admin.api.createRegistry({ body: { label: 'Perm', url: 'https://127.0.0.1:1', username: 'u', password: 'p' } });
  const acc = await admin.api.createAccount({ body: { name: 'Perm target', email: `${uniq('pt')}@opm.test` } });
  const all = { kind: 'all' as const };
  const rules: AccessRule[] = [
    { resource: 'deployments', actions: ['read'], scope: all },
    { resource: 'deployment.setup', actions: ['read'], scope: all },
    { resource: 'deployment.config', actions: ['read'], scope: all },
    { resource: 'registries', actions: ['read'], scope: all },
    { resource: 'accounts', actions: ['read', 'write'], scope: all },
  ];
  const viewer = await person(rules);
  const dv = (await admin.api.getDeployment({ deploymentId: id })).draftVersion;
  const calls: [string, () => Promise<unknown>][] = [
    ['revealAccessKey', () => viewer.api.revealAccessKey({ deploymentId: id })],
    ['rotateAccessKey', () => viewer.api.rotateAccessKey({ deploymentId: id })],
    ['createInstallToken', () => viewer.api.createInstallToken({ deploymentId: id, body: { siteSlug: 'db' } })],
    ['revealInstallCommand', () => viewer.api.revealInstallCommand({ deploymentId: id, tokenHash: tok.tokenHash })],
    ['revealSecret', () => viewer.api.revealSecret({ deploymentId: id, secretSlug: 'api-token' })],
    ['revealRegistryPassword', () => viewer.api.revealRegistryPassword({ registryId: reg.registryId })],
    ['createAccount', () => viewer.api.createAccount({ body: { name: 'x', email: `${uniq('x')}@opm.test` } })],
    ['setAccountPassword', () => viewer.api.setAccountPassword({ uid: acc.account.uid })],
    ['revealAccountPassword', () => viewer.api.revealAccountPassword({ uid: acc.account.uid })],
    ['resetAccountPassword', () => viewer.api.resetAccountPassword({ uid: acc.account.uid })],
    ['revealAccountResetLink', () => viewer.api.revealAccountResetLink({ uid: acc.account.uid })],
  ];
  for (const [op, p] of calls) {
    try {
      await rejects(p(), 'forbidden', 403);
    } catch (e) {
      throw new Error(`${op}: ${(e as Error).message}`);
    }
  }
  // denials are audited (deployment credential ops too)
  const denied = (await adb.collection('auditLog').where('actor', '==', viewer.uid).get()).docs.map((d) => d.data());
  for (const a of ['deployment.access-key-revealed', 'account.password-revealed', 'registry.password-revealed'])
    assert.ok(denied.some((e) => e.action === a && e.status === 403), `no 403 audit entry ${a}`);
  // admin still can; the secret value stays the same (the denied regenerate changed nothing)
  assert.equal((await admin.api.getDeployment({ deploymentId: id })).draftVersion, dv);
});

test('rate limit: 30 value-returning requests per uid per minute → 429 rate-limited + Retry-After (no-store)', async () => {
  await freshWindow();
  const admin = await person('admin');
  const reg = await admin.api.createRegistry({ body: { label: 'Rate', url: 'https://127.0.0.1:1', username: 'u', password: 'p' } });
  for (let i = 0; i < 30; i++) {
    const r = await post(admin, `/v1/registries/${reg.registryId}/reveal-password`);
    assert.equal(r.status, 200, `request ${i + 1}: ${r.status} ${r.body.toString().slice(0, 200)}`);
  }
  const r = await post(admin, `/v1/registries/${reg.registryId}/reveal-password`);
  assert.equal(r.status, 429);
  assert.equal(r.json.error.code, 'rate-limited');
  const retry = r.json.error.details.retryAfterSec as number;
  assert.ok(Number.isInteger(retry) && retry >= 1 && retry <= 60, String(retry));
  assert.equal(r.headers.get('retry-after'), String(retry));
  assert.equal(r.headers.get('cache-control'), 'no-store');
  // other credential ops share the per-uid budget; other people are not affected
  const r2 = await post(admin, `/v1/accounts/${admin.uid}/password/reveal`);
  assert.equal(r2.status, 429);
  const other = await person('admin');
  assert.equal((await post(other, `/v1/registries/${reg.registryId}/reveal-password`)).status, 200);
  // the 429 is audited
  const e = (await adb.collection('auditLog').where('requestId', '==', r.headers.get('x-request-id')).get()).docs[0]!.data();
  assert.equal(e.status, 429);
  // the counter doc has a TTL field
  const docs = (await adb.collection('rateLimits').where('uid', '==', admin.uid).get()).docs;
  assert.ok(docs.length >= 1 && docs[0]!.data().expireAt);
});

test('accounts: generated password stored + revealed, set new password, cleared after the person changes it; reset links', async () => {
  await freshWindow();
  const admin = await person('admin');
  const email = `${uniq('pw')}@opm.test`;
  const created = await post(admin, '/v1/accounts', { name: 'Password person', email });
  assert.equal(created.status, 201);
  assert.equal(created.headers.get('cache-control'), 'no-store');
  const pw0 = created.json.value as string;
  assert.match(pw0, /^[A-Za-z0-9]{20}$/);
  const uid = created.json.account.uid as string;
  assert.equal(created.json.account.lastSetPassword.setBy, admin.uid);
  assert.ok(await idToken(email, pw0), 'the generated password works');
  // reveal = same value; GET never returns values
  const rv = await admin.api.revealAccountPassword({ uid });
  assert.equal(rv.value, pw0);
  assert.equal(rv.hideAfterSec, 60);
  const got = await admin.api.getAccount({ uid });
  assert.deepEqual(Object.keys(got.lastSetPassword ?? {}).sort(), ['setAt', 'setBy']);
  assert.equal(got.lastResetLink, null);
  const list = await admin.api.listAccounts({});
  assert.ok(!JSON.stringify(list).includes(pw0));
  // set new password
  const set = await admin.api.setAccountPassword({ uid });
  assert.notEqual(set.value, pw0);
  assert.match(set.value, /^[A-Za-z0-9]{20}$/);
  assert.equal(set.setBy, admin.uid);
  assert.ok(await idToken(email, set.value));
  assert.equal((await admin.api.revealAccountPassword({ uid })).value, set.value);
  // reset link: stored 1 h, revealed
  const link = await admin.api.resetAccountPassword({ uid });
  assert.match(link.value, /^https?:\/\//);
  assert.equal(link.passwordSetLink, link.value);
  assert.equal(link.createdBy, admin.uid);
  assert.ok(Date.parse(link.expiresAt) - Date.parse(link.createdAt) > 3500_000);
  assert.equal((await admin.api.revealAccountResetLink({ uid })).value, link.value);
  const meta = (await admin.api.getAccount({ uid })).lastResetLink!;
  assert.deepEqual(Object.keys(meta).sort(), ['expiresAt', 'setAt', 'setBy']);
  // expired link → gone expired (and cleared)
  await adb.collection('accounts').doc(uid).update({ 'lastResetLink.expiresAt': new Date(Date.now() - 1000).toISOString() });
  assert.deepEqual((await rejects(admin.api.revealAccountResetLink({ uid }), 'gone', 410)).details, { reason: 'expired' });
  assert.equal((await adb.collection('accounts').doc(uid).get()).data()!.lastResetLink, null);
  // the person changes their password / tokens revoked → tokensValidAfterTime moves → cleared → gone changed
  await sleep(1100);
  await aauth.revokeRefreshTokens(uid);
  const g = await post(admin, `/v1/accounts/${uid}/password/reveal`);
  assert.equal(g.status, 410);
  assert.deepEqual(g.json.error.details, { reason: 'changed' });
  assert.equal(g.headers.get('cache-control'), 'no-store');
  assert.equal((await admin.api.getAccount({ uid })).lastSetPassword, null);
  assert.equal((await adb.collection('accounts').doc(uid).get()).data()!.lastSetPassword, null);
  // none stored → gone not-stored
  assert.deepEqual((await rejects(admin.api.revealAccountResetLink({ uid }), 'gone', 410)).details, { reason: 'not-stored' });
  // listAccounts applies the clearing rule too (batched)
  const set2 = await admin.api.setAccountPassword({ uid });
  await sleep(1100);
  await aauth.revokeRefreshTokens(uid);
  const l2 = await admin.api.listAccounts({});
  assert.equal(l2.items.find((a) => a.uid === uid)!.lastSetPassword, null);
  // audit: actions, never values
  const acts = (await auditOf(`accounts/${uid}`)).map((e) => e.action);
  for (const a of ['account.created', 'account.password-set', 'account.password-revealed', 'account.reset-link-created', 'account.reset-link-revealed']) assert.ok(acts.includes(a), `missing audit ${a}`);
  const createdEntry = (await auditOf(`accounts/${uid}`)).find((e) => e.action === 'account.created')!;
  assert.equal(createdEntry.data.passwordSet, true);
  const logs = await allLogs();
  for (const v of [pw0, set.value, set2.value, link.value, new URL(link.value).searchParams.get('oobCode') ?? link.value]) assert.ok(!logs.includes(v), 'account credential leaked into a log');
});

test('migrate-g: pre-#84 docs get null fields (never rotates); verify-schema requires them; idempotent', async () => {
  const admin = await person('admin');
  const { dep, accessKey } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const tok = await admin.api.createInstallToken({ deploymentId: id, body: { siteSlug: 'app' } });
  const other = await person([]);
  const depRef = adb.collection('deployments').doc(id);
  const before = (await depRef.get()).data()!;
  await depRef.update({ accessKey: FieldValue.delete() });
  await depRef.collection('installTokens').doc(tok.tokenHash).update({ token: FieldValue.delete() });
  await adb.collection('accounts').doc(other.uid).update({ lastSetPassword: FieldValue.delete(), lastResetLink: FieldValue.delete() });
  const mine = [`deployments/${id}`, `accounts/${other.uid}`];
  const isMine = (l: string) => mine.some((m) => l.startsWith(m));
  const v0 = (await verifySchema(adb)).filter(isMine);
  assert.ok(v0.some((l) => /accessKey missing/.test(l)), v0.join('\n'));
  assert.ok(v0.some((l) => /token missing/.test(l)));
  assert.ok(v0.some((l) => /lastSetPassword missing/.test(l)) && v0.some((l) => /lastResetLink missing/.test(l)));
  const plan = await planMigrationG(adb);
  const myOps = plan.ops.filter((o) => isMine(o.path));
  assert.equal(myOps.length, 3, JSON.stringify(myOps));
  await applyPlanG(adb, plan);
  const after = (await depRef.get()).data()!;
  assert.equal(after.accessKey, null);
  assert.equal(after.accessKeyHash, before.accessKeyHash, 'never rotates');
  assert.equal(after.accessKeyVersion, before.accessKeyVersion);
  assert.equal(after.updatedAt, before.updatedAt);
  assert.equal((await depRef.collection('installTokens').doc(tok.tokenHash).get()).data()!.token, null);
  const acc = (await adb.collection('accounts').doc(other.uid).get()).data()!;
  assert.equal(acc.lastSetPassword, null);
  assert.equal(acc.lastResetLink, null);
  assert.deepEqual((await verifySchema(adb)).filter(isMine), []);
  assert.deepEqual((await planMigrationG(adb)).ops.filter((o) => isMine(o.path)), []);
  const audit = (await adb.collection('auditLog').where('op', '==', 'system:migrate-g').get()).docs.map((d) => JSON.parse(d.data()._data));
  assert.ok(audit.some((a) => a.migration === 'g' && a.paths.includes(`deployments/${id}`)));
  // a migrated deployment: not stored → regenerate → viewable again
  assert.deepEqual((await rejects(admin.api.revealAccessKey({ deploymentId: id }), 'gone', 410)).details, { reason: 'not-stored' });
  const rot = await admin.api.rotateAccessKey({ deploymentId: id });
  assert.notEqual(rot.value, accessKey);
  assert.equal((await admin.api.revealAccessKey({ deploymentId: id })).value, rot.value);
});
