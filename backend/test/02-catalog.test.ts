import { test } from 'node:test';
import assert from 'node:assert/strict';
import { MASK } from '@opm/shared';
import { adb, person, raw, rejects, uniq, sleep } from './helpers.ts';

test('registries: create (masked), get, patch resets lastCheck, reveal writes audit, delete conflict while used', async () => {
  const admin = await person('admin');
  await rejects(admin.api.createRegistry({ body: { label: '', username: 'u', password: 'p' } }), 'invalid', 422);
  await rejects(admin.api.createRegistry({ body: { label: 'x', username: 'u', password: 'p', url: 'http://insecure.example' } }), 'invalid');
  await rejects(admin.api.createRegistry({ body: { label: 'x', username: 'u', password: 'p', url: 'https://r.example/path' } }), 'invalid');
  const e = await rejects(admin.api.createRegistry({ body: { label: 'x', username: 'u', password: 'p', bogus: 1 } as never }), 'invalid');
  assert.match(JSON.stringify(e.details), /unknown field bogus/);
  const r = await admin.api.createRegistry({ body: { label: 'Hub', username: 'me', password: 'secret-pw' } });
  assert.equal(r.url, 'https://registry-1.docker.io');
  assert.equal(r.password, MASK);
  assert.equal(r.isSet, true);
  assert.equal((await admin.api.getRegistry({ registryId: r.registryId })).password, MASK);
  const list = await admin.api.listRegistries({});
  assert.ok(list.items.every((x) => x.password === MASK));
  await adb.collection('registries').doc(r.registryId).update({ lastCheck: { at: new Date().toISOString(), ok: true, message: 'x' } });
  const p1 = await admin.api.patchRegistry({ registryId: r.registryId, body: { label: 'Hub 2' } });
  assert.ok(p1.lastCheck, 'label change keeps lastCheck');
  const p2 = await admin.api.patchRegistry({ registryId: r.registryId, body: { password: 'new-pw' } });
  assert.equal(p2.lastCheck, null);
  const rev = await admin.api.revealRegistryPassword({ registryId: r.registryId });
  assert.equal(rev.password, 'new-pw');
  assert.equal(rev.hideAfterSec, 60);
  assert.equal(rev.value, rev.password);
  const audit = await admin.api.listAuditLog({ query: { limit: 50 } });
  assert.ok(audit.items.some((a) => a.action === 'registry.password-revealed' && a.target === `registries/${r.registryId}` && a.actor === admin.uid));
  // in use → conflict
  const svc = await admin.api.createService({ body: { label: 'S', serviceSlug: uniq('svc'), icon: 'lucide:box' } });
  await adb.collection('services').doc(svc.serviceSlug).collection('images').doc('v1').set({ version: 'v1', serviceSlug: svc.serviceSlug, registryId: r.registryId, import: { status: 'failed' }, createdAt: new Date().toISOString() });
  const c = await rejects(admin.api.deleteRegistry({ registryId: r.registryId }), 'conflict', 409);
  assert.deepEqual(c.details.usedBy, [{ serviceSlug: svc.serviceSlug, version: 'v1' }]);
  await adb.collection('services').doc(svc.serviceSlug).collection('images').doc('v1').delete();
  assert.deepEqual(await admin.api.deleteRegistry({ registryId: r.registryId }), { deleted: true });
  await rejects(admin.api.getRegistry({ registryId: r.registryId }), 'not-found', 404);
});

test('registry test against an unreachable registry stores lastCheck ok=false', async () => {
  const admin = await person('admin');
  const r = await admin.api.createRegistry({ body: { label: 'dead', url: 'https://127.0.0.1:1', username: 'u', password: 'p' } });
  const t = await admin.api.testRegistry({ registryId: r.registryId });
  assert.equal(t.ok, false);
  assert.ok(t.message.length > 0);
  const g = await admin.api.getRegistry({ registryId: r.registryId });
  assert.equal(g.lastCheck?.ok, false);
});

test('permissions: developer resources need scope all; catalog read is implicit', async () => {
  const nobody = await person([]);
  await rejects(nobody.api.listRegistries({}), 'forbidden', 403);
  await rejects(nobody.api.createService({ body: { label: 'S', serviceSlug: uniq('svc') } }), 'forbidden');
  assert.ok(Array.isArray((await nobody.api.listServices({})).items), 'services.read is implicit');
  assert.ok(Array.isArray((await nobody.api.listTags({})).items), 'tags.read is implicit');
  assert.ok(Array.isArray((await nobody.api.listNotFoundPages({})).items));
  await rejects(nobody.api.listTemplates({}), 'forbidden');
  await rejects(nobody.api.listAccounts({}), 'forbidden');
  await rejects(nobody.api.listAuditLog({}), 'forbidden');
  const scoped = await person([{ resource: 'registries', actions: ['read'], scope: { kind: 'tags', tagSlugs: ['x'] } }]);
  await rejects(scoped.api.listRegistries({}), 'forbidden');
  const reader = await person([{ resource: 'registries', actions: ['read'], scope: { kind: 'all' } }]);
  assert.ok((await reader.api.listRegistries({})).items);
  const r = (await reader.api.listRegistries({})).items[0];
  if (r) await rejects(reader.api.revealRegistryPassword({ registryId: r.registryId }), 'forbidden');
});

test('services: slug = id (unique, immutable), no repository; images: version = id, reference + registry match, source fix only while failed, env options', async () => {
  const admin = await person('admin');
  const slug = uniq('svc');
  await rejects(admin.api.createService({ body: { label: 'S', serviceSlug: 'Bad_Slug' } }), 'invalid');
  await rejects(admin.api.createService({ body: { label: 'S', serviceSlug: uniq('s'), repository: 'a/b' } as never }), 'invalid');
  await rejects(admin.api.createService({ body: { label: 'S', serviceSlug: uniq('s'), icon: 'https://x.example/a.png' } }), 'invalid');
  const bad = await rejects(admin.api.createService({ body: { label: 'S', serviceSlug: uniq('s'), icon: 'upload:AAAAAAAAAAAAAAAAAAAA' } }), 'invalid');
  assert.match(JSON.stringify(bad.details), /V_REF/);
  const s = await admin.api.createService({ body: { label: 'Svc', serviceSlug: slug, description: 'd' } });
  assert.equal(s.serviceSlug, slug);
  assert.equal(s.icon, null, 'icon defaults to null');
  assert.equal((s as unknown as Record<string, unknown>).serviceId, undefined);
  assert.equal((await adb.collection('services').doc(slug).get()).exists, true, 'doc id = slug');
  await rejects(admin.api.createService({ body: { label: 'Svc2', serviceSlug: slug } }), 'conflict', 409);
  const imm = await rejects(admin.api.patchService({ serviceSlug: slug, body: { serviceSlug: 'other' } as never }), 'invalid');
  assert.match(JSON.stringify(imm.details), /V_IMMUTABLE/);
  const p = await admin.api.patchService({ serviceSlug: slug, body: { label: 'Svc!', icon: 'lucide:box' } });
  assert.equal(p.label, 'Svc!');
  assert.equal(p.icon, 'lucide:box');
  const reg = await admin.api.createRegistry({ body: { label: 'dead', url: 'https://127.0.0.1:1', username: 'u', password: 'p' } });
  // host mismatch → invalid; bad reference → V_REFERENCE
  const hm = await rejects(admin.api.registerImage({ serviceSlug: slug, body: { version: 'v1', reference: 'docker.io/library/busybox:1.36', registryId: reg.registryId, mode: 'copy', kind: 'service' } }), 'invalid');
  assert.match(JSON.stringify(hm.details), /registryId/);
  const br = await rejects(admin.api.registerImage({ serviceSlug: slug, body: { version: 'v1', reference: 'not a ref', mode: 'copy', kind: 'service' } }), 'invalid');
  assert.match(JSON.stringify(br.details), /V_REFERENCE/);
  // registry auto-matched by host (first by label)
  const host = `${uniq('reg')}.example`;
  const r2 = await admin.api.createRegistry({ body: { label: 'B match', url: `https://${host}`, username: 'u', password: 'p' } });
  await admin.api.createRegistry({ body: { label: 'C match', url: `https://${host}`, username: 'u', password: 'p' } });
  const auto = await admin.api.registerImage({ serviceSlug: slug, body: { version: 'auto', reference: `${host}/acme/api:1.0`, mode: 'gateway', kind: 'service' } });
  assert.equal(auto.registryId, r2.registryId, 'registry matched by host');
  assert.equal(auto.source.host, host);
  await adb.collection('services').doc(slug).collection('images').doc('auto').delete();
  const img = await admin.api.registerImage({
    serviceSlug: slug,
    body: {
      version: 'v1',
      reference: '127.0.0.1:1/acme/api:1.0',
      registryId: reg.registryId,
      mode: 'copy',
      kind: 'service',
      ports: [{ name: 'http', port: 8080, protocol: 'tcp', description: '' }],
      env: [
        { name: 'A', value: 'x', required: false, secret: false, description: '' },
        { name: 'MODE', value: 'dc', options: ['dc', 'worker'], required: false, secret: false, description: '' },
        { name: 'LEGACY', value: ['b', 'a'] as never, required: false, secret: false, description: '' },
      ],
    },
  });
  assert.equal(img.import.status, 'pending');
  assert.equal(img.version, 'v1');
  assert.equal(img.serviceSlug, slug);
  assert.equal(img.registryId, reg.registryId);
  assert.deepEqual(img.source, { host: '127.0.0.1:1', repository: 'acme/api', tag: '1.0', digest: null });
  assert.equal(img.digest, null);
  assert.equal(img.platform, 'linux/amd64');
  assert.deepEqual(img.env.map((e) => [e.name, e.value, e.options]), [['A', 'x', null], ['MODE', 'dc', ['dc', 'worker']], ['LEGACY', 'b', ['b', 'a']]], 'env normalized, options always stored');
  const vx = await rejects(admin.api.registerImage({ serviceSlug: slug, body: { version: 'v1', reference: '127.0.0.1:1/acme/api:1.0', mode: 'copy', kind: 'service' } }), 'conflict', 409);
  assert.equal(vx.details.reason, 'version-exists');
  await rejects(admin.api.registerImage({ serviceSlug: slug, body: { version: 'Bad Version', reference: 'redis', mode: 'copy', kind: 'service' } }), 'invalid');
  const opt = await rejects(admin.api.registerImage({ serviceSlug: slug, body: { version: 'v9', reference: 'redis:7', registryId: null, mode: 'copy', kind: 'service', env: [{ name: 'M', value: 'x', options: ['a', 'b'], required: false, secret: false, description: '' }] } }), 'invalid');
  assert.match(JSON.stringify(opt.details), /V_OPTION/);
  await rejects(admin.api.registerImage({ serviceSlug: slug, body: { version: 'v2', reference: '127.0.0.1:1/acme/api:1.0', registryId: 'nope', mode: 'copy', kind: 'service' } }), 'not-found');
  await rejects(admin.api.deleteService({ serviceSlug: slug }), 'conflict');
  let cur = img;
  for (let i = 0; i < 60 && cur.import.status !== 'failed'; i++) {
    await sleep(500);
    cur = await admin.api.getImage({ serviceSlug: slug, version: 'v1' });
  }
  assert.equal(cur.import.status, 'failed', 'trigger ran and failed');
  assert.ok(cur.import.error);
  // source change only while failed
  const iref = adb.collection('services').doc(slug).collection('images').doc('v1');
  await iref.update({ 'import.status': 'ready' });
  await rejects(admin.api.patchImage({ serviceSlug: slug, version: 'v1', body: { reference: '127.0.0.1:1/acme/other:1.0' } }), 'conflict', 409);
  await iref.update({ 'import.status': 'failed' });
  const fixed = await admin.api.patchImage({ serviceSlug: slug, version: 'v1', body: { reference: 'docker.io/library/busybox:1.36', registryId: null } });
  assert.equal(fixed.registryId, null, 'explicit null → public');
  assert.deepEqual(fixed.source, { host: 'docker.io', repository: 'library/busybox', tag: '1.36', digest: null });
  const back = await admin.api.patchImage({ serviceSlug: slug, version: 'v1', body: { reference: '127.0.0.1:1/acme/api:1.0', registryId: reg.registryId } });
  assert.equal(back.registryId, reg.registryId);
  await rejects(admin.api.patchImage({ serviceSlug: slug, version: 'v1', body: { platform: 'linux/arm64' } as never }), 'invalid'); // identity (kind is editable since §21 #82)
  await rejects(admin.api.patchImage({ serviceSlug: slug, version: 'v1', body: { ports: [{ name: 'nope', description: 'x' }] } }), 'invalid');
  const eo = await rejects(admin.api.patchImage({ serviceSlug: slug, version: 'v1', body: { env: [{ name: 'MODE', value: 'nope' }] } }), 'invalid');
  assert.match(JSON.stringify(eo.details), /V_OPTION/);
  const pi = await admin.api.patchImage({ serviceSlug: slug, version: 'v1', body: { description: 'new', ports: [{ name: 'http', description: 'web' }], env: [{ name: 'A', value: null, description: 'no default' }, { name: 'MODE', value: 'worker', options: ['worker', 'dc', 'pg'] }, { name: 'LEGACY', options: null }], stopTimeoutSec: 10 } });
  assert.equal(pi.ports[0]!.description, 'web');
  assert.equal(pi.env[0]!.value, undefined);
  assert.deepEqual(pi.env[1]!.options, ['worker', 'dc', 'pg']);
  assert.equal(pi.env[1]!.value, 'worker');
  assert.equal(pi.env[2]!.options, null);
  assert.equal(pi.stopTimeoutSec, 10);
  const rr = await admin.api.retryImport({ serviceSlug: slug, version: 'v1' });
  assert.equal(rr.import.status, 'pending');
  await rejects(admin.api.retryImport({ serviceSlug: slug, version: 'v1' }), 'conflict');
  assert.equal((await admin.api.listImages({ serviceSlug: slug })).items.length, 1);
  assert.deepEqual((await admin.api.imageUsage({ serviceSlug: slug, version: 'v1' })).items, []);
  assert.deepEqual((await admin.api.serviceUsage({ serviceSlug: slug })).items, []);
  const ls = await admin.api.listServices({});
  assert.equal(ls.items.find((x) => x.serviceSlug === slug)?.imageCount, 1);
  for (let i = 0; i < 60; i++) {
    const g = await admin.api.getImage({ serviceSlug: slug, version: 'v1' });
    if (g.import.status === 'failed') break;
    await sleep(500);
  }
  await admin.api.deleteImage({ serviceSlug: slug, version: 'v1' });
  assert.deepEqual(await admin.api.deleteService({ serviceSlug: slug }), { deleted: true });
  const again = await admin.api.createService({ body: { label: 'Svc', serviceSlug: slug } });
  assert.equal(again.serviceSlug, slug, 'slug is free again after delete');
});

test('not-found pages: create-only (405), too-large, usage blocks delete', async () => {
  const admin = await person('admin');
  const big = 'x'.repeat(262145);
  await rejects(admin.api.createNotFoundPage({ body: { label: 'big', htmlCode: big } }), 'too-large', 413);
  const p = await admin.api.createNotFoundPage({ body: { label: 'Oops', htmlCode: '<h1>Oops</h1>' } });
  const l = await admin.api.listNotFoundPages({});
  const item = l.items.find((x) => x.notFoundPageId === p.notFoundPageId)!;
  assert.equal((item as unknown as Record<string, unknown>).htmlCode, undefined);
  assert.equal(item.sizeBytes, 13);
  const d = await admin.api.getNotFoundPage({ notFoundPageId: p.notFoundPageId });
  assert.equal(d.htmlCode, '<h1>Oops</h1>');
  assert.deepEqual(d.usedBy, []);
  const r = await raw('PATCH', `/v1/not-found-pages/${p.notFoundPageId}`, { headers: { Authorization: admin.bearer } });
  assert.equal(r.status, 405);
  // used by a template
  const t = await admin.api.createTemplate({ body: { label: 'T', icon: 'box' } });
  await admin.api.patchTemplate({ templateId: t.templateId, body: { ifVersion: 1, sites: [{ siteSlug: 'app', label: 'App', config: { host: '127.0.0.1', notFoundPageId: p.notFoundPageId } as never }] } });
  const c = await rejects(admin.api.deleteNotFoundPage({ notFoundPageId: p.notFoundPageId }), 'conflict');
  assert.equal((c.details.usedBy as unknown[]).length, 1);
  await admin.api.deleteTemplate({ templateId: t.templateId });
  assert.deepEqual(await admin.api.deleteNotFoundPage({ notFoundPageId: p.notFoundPageId }), { deleted: true });
});

test('tags: create / conflict / patch / delete removes the slug from deployments and access scopes', async () => {
  const admin = await person('admin');
  const slug = uniq('tag');
  await rejects(admin.api.createTag({ body: { tagSlug: slug, label: 'T', icon: 'lucide:tag', color: 'red' } }), 'invalid');
  const t = await admin.api.createTag({ body: { tagSlug: slug, label: 'Tag', icon: 'lucide:tag', color: '#ff0000' } });
  assert.equal(t.tagSlug, slug);
  await rejects(admin.api.createTag({ body: { tagSlug: slug, label: 'Tag', icon: 'lucide:tag', color: '#ff0000' } }), 'conflict');
  assert.equal((await admin.api.patchTag({ tagSlug: slug, body: { color: '#00ff00' } })).color, '#00ff00');
  const slug2 = uniq('tag');
  await admin.api.createTag({ body: { tagSlug: slug2, label: 'Tag2', icon: 'lucide:tag', color: '#0000ff' } });
  const d = await admin.api.createDeployment({ body: { label: 'tagged', templateId: null, tagSlugs: [slug, slug2], managerAccountUids: [] } });
  const user = await person([
    { resource: 'deployments', actions: ['read'], scope: { kind: 'tags', tagSlugs: [slug] } },
    { resource: 'deployment.config', actions: ['read'], scope: { kind: 'tags', tagSlugs: [slug, slug2] } },
  ]);
  assert.equal((await admin.api.listTags({})).items.find((x) => x.tagSlug === slug)?.deployments, 1);
  const res = await admin.api.deleteTag({ tagSlug: slug });
  assert.deepEqual(res.removedFrom, { deployments: 1, accessRules: 2 });
  const dep = await admin.api.getDeployment({ deploymentId: d.deployment.deploymentId });
  assert.deepEqual(dep.tagSlugs, [slug2]);
  const acc = await admin.api.getAccount({ uid: user.uid });
  assert.equal(acc.access.length, 1, 'rule with only that tag removed');
  assert.deepEqual(acc.access[0]!.scope, { kind: 'tags', tagSlugs: [slug2] });
  await rejects(admin.api.deleteTag({ tagSlug: slug }), 'not-found');
});
