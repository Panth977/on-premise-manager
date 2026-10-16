/**
 * Phase U: icons library, icon fields, revision-catalog icon embedding, anonymous registries, registry browsing
 * (generic v2 + Docker Hub API stubs), the Docker Hub JWT access-claim check, and the v2 data migration.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import type { AddressInfo } from 'node:net';
import { defaultContainer, defaultSiteConfig, type AuditLogEntry, type Config } from '@opm/shared';
import { abucket, adb, helloDeployment, HELLO, person, raw, rejects, seedHelloCatalog, uniq } from './helpers.ts';
import { RegistryAccessError, RegistryClient, tokenGrantsPull } from '../functions/src/core/registry.js';
import { accessHint, listRepositories, listTags, withAccessHint } from '../functions/src/core/registry-browse.js';
import { sanitizeSvg, sniffIcon } from '../functions/src/core/icons.ts';
import { buildRevisionCatalog, loadCatalog } from '../functions/src/core/catalog.ts';
import { applyPlan, planMigrationV2 } from '../scripts/migrate-v2.ts';
import { applyPlanC, planMigrationC } from '../scripts/migrate-c.ts';
import { applyPlanF, planMigrationF } from '../scripts/migrate-f.ts';
import { applyPlanG, planMigrationG } from '../scripts/migrate-g.ts';
import { verifySchema } from '../scripts/verify-schema.ts';
import { jcsHash } from '../functions/src/core/util.ts';

const PNG = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==', 'base64');
const auditByRequest = async (requestId: string): Promise<AuditLogEntry[]> =>
  (await adb.collection('auditLog').where('requestId', '==', requestId).get()).docs.map((d) => ({ ...d.data(), data: JSON.parse(d.data()._data) }) as unknown as AuditLogEntry);
async function upload(bearer: string, body: Buffer, type: string, name?: string) {
  const r = await raw('POST', `/v1/icons${name !== undefined ? `?name=${encodeURIComponent(name)}` : ''}`, { headers: { Authorization: bearer, 'Content-Type': type }, body });
  return { ...r, requestId: r.headers.get('x-request-id')! };
}
const jwt = (payload: unknown) => `${Buffer.from('{"alg":"none"}').toString('base64url')}.${Buffer.from(JSON.stringify(payload)).toString('base64url')}.sig`;
async function serve(h: (req: IncomingMessage, res: ServerResponse, url: URL) => void): Promise<{ url: string; close: () => void }> {
  const srv = createServer((req, res) => h(req, res, new URL(req.url!, 'http://x')));
  await new Promise<void>((r) => srv.listen(0, '127.0.0.1', r));
  return { url: `http://127.0.0.1:${(srv.address() as AddressInfo).port}`, close: () => srv.close() };
}
const json = (res: ServerResponse, status: number, body: unknown, headers: Record<string, string> = {}) => {
  res.writeHead(status, { 'Content-Type': 'application/json', ...headers });
  res.end(JSON.stringify(body));
};

test('icons: sniff + svg sanitizer (unit)', () => {
  assert.equal(sniffIcon(PNG), 'image/png');
  assert.equal(sniffIcon(Buffer.concat([Buffer.from('RIFF\0\0\0\0WEBPVP8 '), Buffer.alloc(8)])), 'image/webp');
  assert.equal(sniffIcon(Buffer.from('<?xml version="1.0"?>\n<svg xmlns="http://www.w3.org/2000/svg"/>')), 'image/svg+xml');
  assert.equal(sniffIcon(Buffer.from('GIF89a....')), null);
  const dirty =
    '<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" onload="alert(1)" viewBox="0 0 10 10">' +
    '<script>alert(2)</script><foreignObject><div onclick="x()">hi</div></foreignObject>' +
    '<a href="javascript:alert(3)"><circle r="4" fill="url(#g)" style="fill:url(https://evil.example/x)"/></a>' +
    '<use xlink:href="#c"/><use xlink:href="https://evil.example/s.svg#x"/><image href="data:image/png;base64,iVBORw0KGgo="/>' +
    '<style>@import url(https://evil.example/a.css);</style><style>.a{fill:red}</style><rect width="1" height="1" class="a"/></svg>';
  const clean = sanitizeSvg(dirty);
  for (const bad of ['<script', 'alert', 'foreignObject', 'onload', 'onclick', 'javascript:', 'evil.example', '@import']) assert.ok(!clean.includes(bad), `${bad} removed: ${clean}`);
  for (const good of ['fill="url(#g)"', 'xlink:href="#c"', 'data:image/png;base64', '.a{fill:red}', 'viewBox="0 0 10 10"']) assert.ok(clean.includes(good), `${good} kept: ${clean}`);
  assert.throws(() => sanitizeSvg('<html><body/></html>'));
  assert.throws(() => sanitizeSvg('<svg><g></svg>'));
  assert.throws(() => sanitizeSvg('<!DOCTYPE svg [<!ENTITY x "y">]><svg/>'));
});

test('icons: upload, dedupe, svg sanitizing, limits, list usedBy, delete 409, public content, audit', async () => {
  const admin = await person('admin');
  const nobody = await person([]);
  const name = uniq('png');
  const up = await upload(admin.bearer, PNG, 'image/png', name);
  assert.equal(up.status, 201, up.body.toString());
  const icon = up.json.icon;
  assert.equal(up.json.existing, false);
  assert.match(icon.iconId, /^[A-Za-z0-9]{20}$/);
  assert.equal(icon.name, name);
  assert.equal(icon.contentType, 'image/png');
  assert.equal(icon.sizeBytes, PNG.length);
  assert.match(icon.sha256, /^sha256:[0-9a-f]{64}$/);
  let a = await auditByRequest(up.requestId);
  assert.equal(a.length, 1);
  assert.equal(a[0]!.action, 'icon.uploaded');
  assert.equal(a[0]!.target, `icons/${icon.iconId}`);
  assert.equal(a[0]!.data.existing, false);
  // same bytes → existing
  const again = await upload(admin.bearer, PNG, 'image/png', 'other');
  assert.equal(again.status, 201);
  assert.equal(again.json.existing, true);
  assert.equal(again.json.icon.iconId, icon.iconId);
  a = await auditByRequest(again.requestId);
  assert.equal(a[0]!.data.existing, true);
  assert.equal(a[0]!.target, `icons/${icon.iconId}`);
  // svg sanitized
  const svg = await upload(admin.bearer, Buffer.from(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)" data-u="${uniq()}"><script>alert(1)</script><circle r="1"/></svg>`), 'image/svg+xml');
  assert.equal(svg.status, 201, svg.body.toString());
  assert.equal(svg.json.icon.name, 'icon', 'default name');
  // errors
  assert.equal((await upload(admin.bearer, Buffer.alloc(65537, 0x89), 'image/png')).status, 413);
  const wrong = await upload(admin.bearer, Buffer.from('GIF89a'), 'image/gif');
  assert.equal(wrong.status, 422);
  assert.equal((await upload(admin.bearer, Buffer.from('GIF89a......'), 'image/png')).status, 422, 'magic bytes must match');
  assert.equal((await upload(admin.bearer, PNG, 'image/svg+xml')).status, 422, 'content-type mismatch');
  assert.equal((await upload(admin.bearer, Buffer.from('<svg><g></svg>'), 'image/svg+xml')).status, 422, 'not parseable');
  assert.equal((await upload(admin.bearer, PNG, 'image/png', 'x'.repeat(81))).status, 422);
  assert.equal((await upload(nobody.bearer, PNG, 'image/png')).status, 403);
  // public content + headers
  const c = await raw('GET', `/v1/icons/${icon.iconId}/content`, { headers: { Origin: 'https://elsewhere.example' } });
  assert.equal(c.status, 200);
  assert.deepEqual(c.body, PNG);
  assert.equal(c.headers.get('content-type'), 'image/png');
  assert.equal(c.headers.get('cache-control'), 'public, max-age=31536000, immutable');
  assert.equal(c.headers.get('x-content-type-options'), 'nosniff');
  assert.equal(c.headers.get('access-control-allow-origin'), '*');
  const sc = await raw('GET', `/v1/icons/${svg.json.icon.iconId}/content`);
  assert.equal(sc.headers.get('content-type'), 'image/svg+xml');
  assert.equal(sc.headers.get('content-security-policy'), "default-src 'none'; style-src 'unsafe-inline'; sandbox");
  assert.ok(!sc.body.toString().includes('script') && !sc.body.toString().includes('onload'), sc.body.toString());
  assert.equal((await raw('GET', '/v1/icons/AAAAAAAAAAAAAAAAAAAA/content')).status, 404);
  // users: service, tag, deployment, account
  const ref = `upload:${icon.iconId}`;
  const svc = await admin.api.createService({ body: { label: 'Iconic', serviceSlug: uniq('ico'), icon: ref } });
  const tagSlug = uniq('tag');
  await admin.api.createTag({ body: { tagSlug, label: 'T', icon: ref, color: '#112233' } });
  const dep = await admin.api.createDeployment({ body: { label: 'iconic', templateId: null, tagSlugs: [], managerAccountUids: [], image: ref } });
  await admin.api.patchAccount({ uid: admin.uid, body: { img: ref } });
  await rejects(admin.api.patchAccount({ uid: admin.uid, body: { img: 'upload:BBBBBBBBBBBBBBBBBBBB' } }), 'invalid');
  await rejects(admin.api.patchAccount({ uid: admin.uid, body: { img: 'javascript:x' } }), 'invalid');
  const list = await admin.api.listIcons({});
  assert.equal(list.items.find((x) => x.iconId === icon.iconId)?.usedBy, 4);
  assert.ok(list.items.findIndex((x) => x.iconId === svg.json.icon.iconId) < list.items.findIndex((x) => x.iconId === icon.iconId), 'newest first');
  assert.equal((list.items[0] as unknown as Record<string, unknown>)._bytes, undefined);
  const del = await raw('DELETE', `/v1/icons/${icon.iconId}`, { headers: { Authorization: admin.bearer } });
  assert.equal(del.status, 409);
  assert.deepEqual((del.json.error.details.users as { type: string }[]).map((u) => u.type).sort(), ['account', 'deployment', 'service', 'tag']);
  assert.equal((await auditByRequest(del.headers.get('x-request-id')!))[0]!.action, 'icon.deleted');
  await admin.api.patchService({ serviceSlug: svc.serviceSlug, body: { icon: null } });
  await admin.api.patchTag({ tagSlug, body: { icon: 'lucide:tag' } });
  await admin.api.patchDeployment({ deploymentId: dep.deployment.deploymentId, body: { image: 'https://example.com/logo.png' } });
  await admin.api.patchAccount({ uid: admin.uid, body: { img: null } });
  await rejects(nobody.api.deleteIcon({ iconId: icon.iconId }), 'forbidden');
  const ok = await raw('DELETE', `/v1/icons/${icon.iconId}`, { headers: { Authorization: admin.bearer } });
  assert.equal(ok.status, 200);
  const da = (await auditByRequest(ok.headers.get('x-request-id')!))[0]!;
  assert.equal(da.target, `icons/${icon.iconId}`);
  assert.equal(da.outcome, 'ok');
  assert.equal((await raw('GET', `/v1/icons/${icon.iconId}/content`)).status, 404);
  await rejects(admin.api.deleteIcon({ iconId: icon.iconId }), 'not-found');
});

test('revision catalogs embed uploaded service icons as data: URLs', async () => {
  const admin = await person('admin');
  await seedHelloCatalog();
  const up = await upload(admin.bearer, Buffer.from(`<svg xmlns="http://www.w3.org/2000/svg" id="${uniq()}"/>`), 'image/svg+xml');
  await adb.collection('services').doc('redis').update({ icon: `upload:${up.json.icon.iconId}` });
  await adb.collection('services').doc('hello-api').update({ icon: null });
  try {
    const cat = await buildRevisionCatalog(HELLO().config, await loadCatalog());
    assert.match(cat.services.redis!.icon!, /^data:image\/svg\+xml;base64,/);
    assert.equal(Buffer.from(cat.services.redis!.icon!.split(',')[1]!, 'base64').toString(), (await raw('GET', `/v1/icons/${up.json.icon.iconId}/content`)).body.toString());
    assert.equal(cat.services['hello-web']!.icon, 'lucide:globe');
    assert.equal(cat.services['hello-api']!.icon, null);
  } finally {
    await seedHelloCatalog();
  }
});

test('registries: anonymous (both empty) allowed, exactly one empty → invalid', async () => {
  const admin = await person('admin');
  const r = await admin.api.createRegistry({ body: { label: 'Hub public', username: '', password: '' } });
  assert.equal(r.isSet, false);
  await rejects(admin.api.createRegistry({ body: { label: 'x', username: 'u', password: '' } }), 'invalid');
  await rejects(admin.api.createRegistry({ body: { label: 'x', username: '', password: 'p' } }), 'invalid');
  await rejects(admin.api.patchRegistry({ registryId: r.registryId, body: { username: 'only-user' } }), 'invalid');
  await admin.api.deleteRegistry({ registryId: r.registryId });
});

test('registry JWT access claim: token without pull access → clear error + accessible repositories hint', async () => {
  assert.equal(tokenGrantsPull(jwt({ access: [] }), 'northwind/backend'), false);
  assert.equal(tokenGrantsPull(jwt({ access: [{ type: 'repository', name: 'northwind/northwind', actions: ['pull'] }] }), 'northwind/northwind'), true);
  assert.equal(tokenGrantsPull('opaque-token', 'x/y'), null);
  const stub = await serve((req, res, u) => {
    const auth = req.headers.authorization ?? '';
    if (u.pathname === '/token') {
      const scope = u.searchParams.get('scope') ?? '';
      const m = /^repository:(.+):pull$/.exec(scope);
      const access = m && m[1] !== 'acme/secret' ? [{ type: 'repository', name: m[1], actions: ['pull'] }] : [];
      return json(res, 200, { token: jwt({ access }) });
    }
    if (!auth.startsWith('Bearer ')) return json(res, 401, { errors: [{ code: 'UNAUTHORIZED' }] }, { 'WWW-Authenticate': `Bearer realm="http://${req.headers.host}/token",service="stub"` });
    if (u.pathname === '/v2/') return json(res, 200, {});
    if (u.pathname === '/v2/_catalog') return json(res, 200, { repositories: ['acme/api', 'acme/web', 'other/x'] });
    if (u.pathname === '/v2/acme/api/tags/list') return json(res, 200, { name: 'acme/api', tags: ['2.0', '1.0'] });
    if (u.pathname.startsWith('/v2/acme/secret/')) return json(res, 401, { errors: [{ code: 'UNAUTHORIZED', message: 'authentication required' }] });
    return json(res, 404, { errors: [{ code: 'NOT_FOUND' }] });
  });
  try {
    const creds = { url: stub.url, username: 'bob', password: 'pw' };
    const cl = new RegistryClient(creds);
    await assert.rejects(cl.manifest('acme/secret', '1.0'), (e: unknown) => e instanceof RegistryAccessError && e.message === 'repository acme/secret not found, or account bob has no pull access to it (the registry answers 401 for both)');
    await assert.rejects(
      withAccessHint(creds, () => new RegistryClient(creds).manifest('acme/secret', '1.0')),
      (e: unknown) => (e as { kind?: string }).kind === 'registry-access' && /repositories account bob can access: acme\/api, acme\/web, other\/x$/.test((e as Error).message),
    );
    // login test with a named repository
    const t1 = await new RegistryClient(creds).test('repository:acme/secret:pull', true);
    assert.equal(t1.ok, false);
    assert.equal(t1.accessDenied, 'acme/secret');
    assert.equal((await new RegistryClient(creds).test('repository:acme/api:pull', true)).ok, true);
    // generic browsing
    const repos = await listRepositories(creds, 'acme');
    assert.deepEqual({ ...repos, items: repos.items.map((x) => x.name) }, { items: ['acme/api', 'acme/web'], source: 'catalog', supported: true, message: null });
    const tags = await listTags(creds, 'acme/api', '');
    assert.equal(tags.source, 'registry-v2');
    assert.deepEqual(tags.items.map((x) => x.tag), ['1.0', '2.0']);
    const none = await listRepositories({ url: 'http://127.0.0.1:1', username: '', password: '' });
    assert.equal(none.supported, false);
    assert.equal(none.source, 'none');
    assert.ok(none.message);
  } finally {
    stub.close();
  }
});

test('Docker Hub browsing via the hub API (stub): account + org repos, search, tags, hint', async () => {
  const hub = await serve((req, res, u) => {
    const authed = req.headers.authorization === 'Bearer hub-jwt';
    if (u.pathname === '/v2/users/login' && req.method === 'POST') {
      let b = '';
      req.on('data', (c) => (b += c));
      req.on('end', () => (JSON.parse(b).password === 'pat' ? json(res, 200, { token: 'hub-jwt' }) : json(res, 401, { detail: 'Incorrect authentication credentials' })));
      return;
    }
    if (u.pathname === '/v2/search/repositories/') return json(res, 200, { results: [{ repo_name: 'redis', short_description: 'Redis', star_count: 9, pull_count: 99, is_official: true }, { repo_name: 'bitnami/redis', star_count: 1 }] });
    if (!authed) return json(res, 401, {});
    if (u.pathname === '/v2/namespaces/northwindbot/repositories') return json(res, 200, { results: [{ name: 'tools', namespace: 'northwindbot', description: 'x', star_count: 0, pull_count: 3, last_updated: '2026-01-01T00:00:00Z' }] });
    if (u.pathname === '/v2/user/orgs/') return json(res, 200, { results: [{ orgname: 'northwind' }] });
    if (u.pathname === '/v2/namespaces/northwind/repositories') return json(res, 200, { results: [{ name: 'northwind', namespace: 'northwind' }, { name: 'redis-fork', namespace: 'northwind' }] });
    if (u.pathname === '/v2/namespaces/northwind/repositories/northwind/tags')
      return json(res, 200, { results: [{ name: 'old', last_updated: '2025-01-01T00:00:00Z', digest: null, full_size: 1 }, { name: 'backend-3.0.9', last_updated: '2026-02-01T00:00:00Z', digest: `sha256:${'a'.repeat(64)}`, full_size: 123 }] });
    return json(res, 404, {});
  });
  try {
    const o = { hubApi: hub.url };
    const creds = { url: 'https://registry-1.docker.io', username: 'northwindbot', password: 'pat' };
    const all = await listRepositories(creds, '', o);
    assert.equal(all.source, 'docker-hub');
    assert.deepEqual(all.items.map((x) => x.name), ['northwindbot/tools', 'northwind/northwind', 'northwind/redis-fork']);
    const q = await listRepositories(creds, 'redis', o);
    assert.deepEqual(q.items.map((x) => x.name), ['northwind/redis-fork', 'library/redis', 'bitnami/redis']);
    assert.equal(q.items.find((x) => x.name === 'library/redis')?.official, true);
    const anon = await listRepositories({ url: 'https://registry-1.docker.io', username: '', password: '' }, '', o);
    assert.ok(anon.items.some((x) => x.name === 'library/redis'), 'popular official images without q');
    const tags = await listTags(creds, 'northwind/northwind', '', o);
    assert.deepEqual(tags.items.map((x) => x.tag), ['backend-3.0.9', 'old'], 'newest first');
    assert.equal(tags.items[0]!.sizeBytes, 123);
    assert.equal(tags.items[0]!.pushedAt, '2026-02-01T00:00:00.000Z');
    const bad = await listRepositories({ ...creds, password: 'wrong' }, '', o);
    assert.equal(bad.supported, false);
    assert.match(bad.message ?? '', /Docker Hub login failed/);
    const hint = await accessHint(creds, 'repository northwind/backend not found', o);
    assert.match(hint, /account northwindbot can access: northwindbot\/tools, northwind\/northwind, northwind\/redis-fork$/);
  } finally {
    hub.close();
  }
});

test('registry browse endpoints: perms, validation, supported:false fallback; test login with a repository body', async () => {
  const admin = await person('admin');
  const nobody = await person([]);
  const reg = await admin.api.createRegistry({ body: { label: 'dead', url: 'https://127.0.0.1:1', username: 'u', password: 'p' } });
  const r = await admin.api.listRegistryRepositories({ registryId: reg.registryId, query: { q: 'x' } });
  assert.equal(r.supported, false);
  assert.ok(r.message);
  const t = await admin.api.listRegistryTags({ registryId: reg.registryId, query: { repository: 'acme/api' } });
  assert.equal(t.supported, false);
  await rejects(admin.api.listRegistryTags({ registryId: reg.registryId, query: { repository: 'Bad Repo' } }), 'invalid');
  await rejects(admin.api.listRegistryRepositories({ registryId: 'nope', query: {} }), 'not-found');
  await rejects(nobody.api.listRegistryRepositories({ registryId: reg.registryId, query: {} }), 'forbidden');
  const before = (await adb.collection('auditLog').where('target', '==', `registries/${reg.registryId}`).get()).size;
  await admin.api.listRegistryRepositories({ registryId: reg.registryId, query: {} });
  assert.equal((await adb.collection('auditLog').where('target', '==', `registries/${reg.registryId}`).get()).size, before, 'GET not audited');
  const tr = await raw('POST', `/v1/registries/${reg.registryId}/test`, { headers: { Authorization: admin.bearer, 'Content-Type': 'application/json' }, body: JSON.stringify({ repository: 'acme/api' }) });
  assert.equal(tr.status, 200);
  assert.equal(tr.json.ok, false);
  const tb = await raw('POST', `/v1/registries/${reg.registryId}/test`, { headers: { Authorization: admin.bearer, 'Content-Type': 'application/json' }, body: JSON.stringify({ repository: 'NOT OK' }) });
  assert.equal(tb.status, 422);
});

// ───────────────────────────────────────── migration v2
test('migrate-v2: old-shape data → dry run changes nothing → apply → verify-schema 0 → second run 0 changes', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const sfx = uniq('');
  const regRef = adb.collection('registries').doc(`RegMig${sfx}`.padEnd(20, '0').slice(0, 20));
  const now = new Date().toISOString();
  const audit = { createdAt: now, createdBy: 'test', updatedAt: now, updatedBy: 'test' };
  await regRef.set({ registryId: regRef.id, label: 'Hub (mig)', url: 'https://registry-1.docker.io', username: 'u', password: 'p', lastCheck: null, ...audit });
  const oldA = `OldA${sfx}`.padEnd(20, '0').slice(0, 20);
  const oldB = `OldB${sfx}`.padEnd(20, '0').slice(0, 20);
  const slugA = uniq('mga');
  const slugB = uniq('mgb');
  const env = [{ name: 'NODE_ENV', value: ['a', 'b'], required: false, secret: false, description: '' }];
  const oldImg = (serviceId: string, tag: string, extra: Record<string, unknown> = {}) => ({
    tag,
    serviceId,
    registryId: regRef.id,
    digest: `sha256:${'b'.repeat(64)}`,
    platform: 'linux/amd64',
    import: { mode: 'copy', status: 'ready', error: null, sizeBytes: 1, manifestMediaType: 'application/vnd.oci.image.manifest.v1+json', configDigest: `sha256:${'c'.repeat(64)}`, layers: [], startedAt: now, finishedAt: now },
    kind: 'service',
    entrypoint: null,
    command: null,
    workdir: null,
    user: null,
    ports: [{ name: 'http', port: 80, protocol: 'tcp', description: '' }],
    env,
    volumes: [],
    healthcheck: null,
    stopTimeoutSec: 30,
    description: '',
    ...audit,
    ...extra,
  });
  await adb.collection('services').doc(oldA).set({ serviceId: oldA, slug: slugA, label: 'Mig A', icon: 'box', repository: 'acme/api', description: '', ...audit });
  await adb.collection('services').doc(oldA).collection('images').doc('1.0').set(oldImg(oldA, '1.0'));
  await adb.collection('services').doc(oldA).collection('images').doc('Latest_Build').set(oldImg(oldA, 'Latest_Build', { createdAt: '2020-01-01T00:00:00.000Z' }));
  await adb.collection('services').doc(oldB).set({ serviceId: oldB, slug: slugB, label: 'Mig B', icon: 'https://x.example/i.png', repository: 'redis', description: '', ...audit });
  await adb.collection('services').doc(oldB).collection('images').doc('7').set(oldImg(oldB, '7'));
  await adb.collection('serviceSlugs').doc(slugA).set({ serviceId: oldA });
  // the user's real service (special section of the script)
  const U = '4WczlsAAaexWRXrzPvq4';
  const UT = 'backend-3.0.9';
  // fixed ids: start clean when the emulator already ran this test
  for (const s of ['northwind-backend', U]) {
    for (const i of (await adb.collection('services').doc(s).collection('images').get()).docs) await i.ref.delete();
    await adb.collection('services').doc(s).delete();
  }
  await adb.collection('services').doc(U).set({ serviceId: U, slug: 'northwind-backend', label: 'Northwind backend', icon: 'server', repository: 'northwind/backend', description: '', ...audit });
  await adb
    .collection('services')
    .doc(U)
    .collection('images')
    .doc(UT)
    .set(
      oldImg(U, UT, {
        import: { mode: 'copy', status: 'failed', error: 'GET manifest northwind/backend:…: HTTP 401', sizeBytes: null, manifestMediaType: null, configDigest: null, layers: [], startedAt: now, finishedAt: now },
        digest: null,
        env: [
          { name: 'NODE_ENV', value: ['integration', 'dc', 'worker'], required: true, secret: false, description: 'mode' },
          { name: 'LOG_IN_CONSOLE', value: 'false', required: false, secret: false, description: '' },
          { name: 'PORT', value: '3000', required: false, secret: false, description: '' },
        ],
        volumes: [{ name: 'cfg', mountPath: '/app/helper/config/env/{{ container.env.NODE_ENV }}.ts', description: '' }],
      }),
    );
  // configs: template body, deployment draft, a revision with catalog + edge proxy
  const imgA = { kind: 'service' as const, ports: [{ name: 'http', port: 80, protocol: 'tcp' as const, description: '' }], env: [], volumes: [] };
  const old = (c: Record<string, unknown>, id: string, tag: string) => {
    const { serviceSlug, version, ...rest } = c;
    void serviceSlug;
    void version;
    return { serviceId: id, tag, ...rest };
  };
  // the pre-phase-U container shape (one per service: serviceId + tag, port / volume, no containerSlug)
  const legacy = (id: string, tag: string) => ({ serviceId: id, tag, enabled: true, env: [], volume: [], port: [{ portName: 'http', hostPort: null, protocol: 'tcp' }], command: null, restart: 'always', resources: { cpus: null, memoryMb: null }, depends: [] as unknown[] });
  void old;
  void imgA;
  void defaultContainer;
  const site = { ...defaultSiteConfig('127.0.0.1'), containers: [legacy(oldA, '1.0'), { ...legacy(oldB, '7'), depends: [{ serviceId: oldA }] }] };
  const cfg = { variables: [], secrets: [], files: [], sites: [{ siteSlug: 'app', label: 'App', config: site }] };
  const tpl = await admin.api.createTemplate({ body: { label: uniq('mig'), icon: 'box' } });
  await adb.collection('templates').doc(tpl.templateId).update({ _body: JSON.stringify({ variables: [], secrets: [], files: [], sites: cfg.sites }) });
  const depRef = adb.collection('deployments').doc(dep.deploymentId);
  await depRef.update({ _draft: JSON.stringify(cfg), image: 'rocket' });
  const revCfg = structuredClone(cfg) as unknown as { sites: { config: { edge: { routes: unknown[] } } }[] };
  revCfg.sites[0]!.config.edge.routes = [{ match: { pathPrefix: '/' }, action: { type: 'proxy', serviceId: oldA, portName: 'http' } }];
  const revCat = {
    services: { [oldA]: { serviceId: oldA, slug: slugA, label: 'Mig A', icon: 'box' }, [oldB]: { serviceId: oldB, slug: slugB, label: 'Mig B', icon: null } },
    images: { [`${oldA}:1.0`]: { serviceId: oldA, serviceSlug: slugA, tag: '1.0', digest: `sha256:${'b'.repeat(64)}` }, [`${oldB}:7`]: { serviceId: oldB, serviceSlug: slugB, tag: '7', digest: `sha256:${'b'.repeat(64)}` } },
    notFoundPages: {},
  };
  const revId = `01MIG${sfx}`.toUpperCase();
  await depRef.collection('revisions').doc(revId).set({ revId, seq: 99, parentRevId: null, origin: 'cloud', actor: 'test', createdAt: now, summary: 'old', kind: 'change', rollbackOf: null, _config: JSON.stringify(revCfg), _catalog: JSON.stringify(revCat), hash: jcsHash(revCfg), catalogHash: jcsHash(revCat) });

  // dry run: a plan, nothing written
  const plan = await planMigrationV2(adb);
  assert.equal(plan.services[oldA], slugA);
  assert.equal(plan.services[U], 'northwind-backend');
  assert.equal(plan.images[`${oldA}:1.0`], `${slugA}:1.0`);
  assert.equal(plan.images[`${oldA}:Latest_Build`], `${slugA}:latest_build`);
  assert.equal(plan.images[`${U}:${UT}`], 'northwind-backend:v1');
  assert.ok(plan.ops.some((o) => o.path === `templates/${tpl.templateId}`));
  assert.ok(plan.ops.some((o) => o.path === `deployments/${dep.deploymentId}/revisions/${revId}`));
  assert.equal((await adb.collection('services').doc(oldA).get()).exists, true, 'dry run wrote nothing');
  assert.equal((await adb.collection('services').doc(slugA).get()).exists, false);

  await applyPlan(adb, plan);
  // services + images
  assert.equal((await adb.collection('services').doc(oldA).get()).exists, false);
  assert.equal((await adb.collection('serviceSlugs').doc(slugA).get()).exists, false);
  const sa = (await adb.collection('services').doc(slugA).get()).data()!;
  assert.equal(sa.serviceSlug, slugA);
  assert.equal(sa.icon, 'lucide:box');
  for (const k of ['serviceId', 'slug', 'repository']) assert.ok(!(k in sa), k);
  assert.equal((await adb.collection('services').doc(slugB).get()).data()!.icon, null, 'https icon → null');
  const ia = (await adb.collection('services').doc(slugA).collection('images').doc('1.0').get()).data()!;
  assert.equal(ia.reference, 'docker.io/acme/api:1.0');
  assert.deepEqual(ia.source, { host: 'docker.io', repository: 'acme/api', tag: '1.0', digest: null });
  assert.equal(ia.registryId, regRef.id);
  assert.deepEqual(ia.env[0], { name: 'NODE_ENV', value: 'a', options: ['a', 'b'], required: false, secret: false, description: '' });
  assert.ok(!('tag' in ia) && !('serviceId' in ia));
  assert.equal((await adb.collection('services').doc(slugB).collection('images').doc('7').get()).data()!.reference, 'docker.io/library/redis:7');
  // the user's image
  const ui = (await adb.collection('services').doc('northwind-backend').collection('images').doc('v1').get()).data()!;
  assert.equal(ui.reference, 'docker.io/northwind/backend:3.0.9');
  assert.equal(ui.source.repository, 'northwind/backend');
  assert.ok(['pending', 'copying', 'failed'].includes(ui.import.status), 'reset to pending (the trigger may already run it)');
  const ne = ui.env.find((e: { name: string }) => e.name === 'NODE_ENV');
  assert.deepEqual(ne.options, ['integration', 'dc', 'worker', 'pg-listen', 'terminal', 'migrate']);
  assert.equal(ne.value, 'integration');
  const lc = ui.env.find((e: { name: string }) => e.name === 'LOG_IN_CONSOLE');
  assert.deepEqual(lc.options, ['false', 'true']);
  assert.equal(lc.value, 'false');
  assert.equal(ui.env.find((e: { name: string }) => e.name === 'PORT').options, null);
  assert.equal(ui.volumes[0].mountPath, '/app/helper/config/env/{{ container.env.NODE_ENV }}.ts');
  // configs
  const body = JSON.parse((await adb.collection('templates').doc(tpl.templateId).get()).data()!._body);
  assert.deepEqual(body.sites[0].config.containers.map((c: Config['sites'][0]['config']['containers'][0]) => [c.serviceSlug, c.version]), [[slugA, '1.0'], [slugB, '7']]);
  assert.deepEqual(body.sites[0].config.containers[1].depends, [{ serviceSlug: slugA }]);
  const d = (await depRef.get()).data()!;
  assert.equal(d.image, 'lucide:rocket');
  assert.equal(JSON.parse(d._draft).sites[0].config.containers[0].serviceSlug, slugA);
  const rv = (await depRef.collection('revisions').doc(revId).get()).data()!;
  const rc = JSON.parse(rv._config);
  const rk = JSON.parse(rv._catalog);
  assert.equal(rc.sites[0].config.edge.routes[0].action.serviceSlug, slugA);
  assert.deepEqual(Object.keys(rk.images).sort(), [`${slugA}:1.0`, `${slugB}:7`].sort());
  assert.deepEqual(rk.images[`${slugA}:1.0`], { serviceSlug: slugA, version: '1.0', digest: `sha256:${'b'.repeat(64)}` });
  assert.deepEqual(rk.services[slugA], { serviceSlug: slugA, label: 'Mig A', icon: 'lucide:box' });
  assert.equal(rv.hash, jcsHash(rc));
  assert.equal(rv.catalogHash, jcsHash(rk));
  // audit entry
  const mig = (await adb.collection('auditLog').where('action', '==', 'system.migration').get()).docs.map((x) => JSON.parse(x.data()._data));
  assert.ok(mig.some((m) => m.services?.[oldA] === slugA));
  // API reads the new shape
  assert.equal((await admin.api.getService({ serviceSlug: slugA })).serviceSlug, slugA);
  assert.equal((await admin.api.getImage({ serviceSlug: 'northwind-backend', version: 'v1' })).reference, 'docker.io/northwind/backend:3.0.9');
  // round C: migrate-c turns the one-per-service containers into instances (containerSlug = serviceSlug)
  await applyPlanC(adb, await planMigrationC(adb, abucket));
  const bodyC = JSON.parse((await adb.collection('templates').doc(tpl.templateId).get()).data()!._body);
  assert.deepEqual(bodyC.sites[0].config.containers.map((c: { containerSlug: string }) => c.containerSlug), [slugA, slugB]);
  assert.deepEqual(bodyC.sites[0].config.containers[1].depends, [{ containerSlug: slugA }]);
  assert.equal(JSON.parse((await depRef.collection('revisions').doc(revId).get()).data()!._config).sites[0].config.edge.routes[0].action.containerSlug, slugA);
  // later rounds: image volume kinds (migrate-f, §21 #83)
  await applyPlanF(adb, await planMigrationF(adb));
  // round G: stored-credential fields (migrate-g, §21 #84)
  await applyPlanG(adb, await planMigrationG(adb));
  // verify-schema: no violation on the migrated documents
  const mine = [`services/${slugA}`, `services/${slugB}`, 'services/northwind-backend', `templates/${tpl.templateId}`, `deployments/${dep.deploymentId}`, `registries/${regRef.id}`];
  const v = (await verifySchema(adb)).filter((l) => mine.some((p) => l.startsWith(p)));
  assert.deepEqual(v, []);
  // idempotent
  const again = await planMigrationV2(adb);
  assert.deepEqual(again.ops.map((o) => `${o.op} ${o.path} ${o.why}`), []);
  // the user's image import job: wait until it settled so it does not race later tests
  for (let i = 0; i < 40; i++) {
    const s = (await adb.collection('services').doc('northwind-backend').collection('images').doc('v1').get()).data()!.import.status;
    if (s === 'failed' || s === 'ready') break;
    await new Promise((r) => setTimeout(r, 500));
  }
});
