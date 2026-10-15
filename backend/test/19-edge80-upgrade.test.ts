/**
 * Round H (SPEC-final §21 #86): always-on edge on :80 (heartbeat guiPorts / defaultEdge / port alerts) + the enrolled-site upgrade path.
 * - used-token upgrade rule for /v1/install/{script,binary,deps} (the token of the CURRENT enrollment; revoke ends it)
 * - GET /v1/agent/release + /v1/agent/binary (signed, `opm-agent upgrade`)
 * - heartbeat guiPorts + edge.defaultEdge / bindErrors[].owner (validated, stored) → Setup guiUrls (port 80 first), defaultEdge, portAlerts
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createCloudClient, type DepsManifest } from '@opm/shared';
import { abucket, adb, BASE, enrollSite, helloDeployment, newSiteKey, person, raw, rejects, sha256Hex, signHeaders, tinyScript } from './helpers.ts';

const VER = '0.0.9-roundh';
const BIN = Buffer.from('fake opm-agent binary ' + VER);

async function uploadAgent() {
  const base = `agent/${VER}/linux-amd64/opm-agent`;
  await abucket.file(base).save(BIN);
  await abucket.file(`${base}.sha256`).save(sha256Hex(BIN) + '\n');
  await abucket.file(`${base}.sig`).save('sig-' + VER + '\n');
  const manifest: DepsManifest = {
    docker: { version: '0.0.1-h', files: { amd64: { path: 'deps/docker/h/amd64/docker.tgz', sha256: 'e'.repeat(64), size: 1 }, arm64: { path: 'deps/docker/h/arm64/docker.tgz', sha256: 'f'.repeat(64), size: 1 } } },
    minDockerApi: '1.41',
    agent: { version: VER },
    updatedAt: new Date().toISOString(),
  };
  await abucket.file('deps/manifest.json').save(JSON.stringify(manifest));
}

test('round H: used-token upgrade path, agent release/binary, heartbeat guiPorts → setup guiUrls / defaultEdge / portAlerts', async (t) => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  await uploadAgent();
  const app = await enrollSite(admin, id, 'app');
  const H = { Authorization: `Bearer ${app.token}` };

  await t.test('used token of the current enrollment → upgrade-only script, binary allowed; enroll still gone', async () => {
    const tokDoc = (await adb.collection('deployments').doc(id).collection('installTokens').where('siteSlug', '==', 'app').get()).docs[0]!.data();
    const agentDoc = (await adb.collection('deployments').doc(id).collection('agents').doc('app').get()).data()!;
    assert.equal(agentDoc.enrolledAt, tokDoc.usedAt, 'enroll writes the same timestamp to token.usedAt and agent.enrolledAt');
    // even after the 24 h expiry: the upgrade rule has no expiry
    await adb.collection('deployments').doc(id).collection('installTokens').doc(tokDoc.tokenHash).update({ expiresAt: new Date(Date.now() - 1000).toISOString() });
    const s = await raw('GET', '/v1/install/script', { headers: { ...H, 'X-Forwarded-For': '139.59.2.56' } });
    assert.equal(s.status, 200, s.body.toString());
    const sh = s.body.toString();
    assert.ok(sh.includes('UPGRADE_ONLY="1"; SEEN_IP="139.59.2.56"'));
    assert.ok(sh.includes(`AGENT_VERSION="${VER}"`));
    assert.ok(sh.includes('it can only upgrade that server'));
    const b = await raw('GET', `/v1/install/binary?os=linux&arch=amd64&version=${VER}`, { headers: H });
    assert.equal(b.status, 200);
    assert.deepEqual(b.body, BIN);
    const anon = createCloudClient(BASE, () => `Bearer ${app.token}`);
    await rejects(anon.enroll({ body: { publicKey: newSiteKey().publicKey, agentVersion: '1', hostname: 'h', ip: '1.2.3.4', advertiseHost: '1.2.3.4', agentPort: 7080, os: 'linux', arch: 'amd64' } }), 'gone', 410);
    // a fresh (unused) token → the normal script
    const fresh = await admin.api.createInstallToken({ deploymentId: id, body: { siteSlug: 'db' } });
    const s2 = await raw('GET', '/v1/install/script', { headers: { Authorization: `Bearer ${fresh.token}` } });
    assert.equal(s2.status, 200);
    assert.ok(s2.body.toString().includes('UPGRADE_ONLY="0"'));
    // expired unused token → gone
    await adb.collection('deployments').doc(id).collection('installTokens').doc(fresh.tokenHash).update({ expiresAt: new Date(Date.now() - 1000).toISOString() });
    tinyScript(await raw('GET', '/v1/install/script', { headers: { Authorization: `Bearer ${fresh.token}` } }), /token expired/);
  });

  await t.test('GET /v1/agent/release + /v1/agent/binary (signed)', async () => {
    const r = await app.api.agentRelease({ query: { os: 'linux', arch: 'amd64' } });
    assert.equal(r.version, VER);
    assert.equal(r.sha256, sha256Hex(BIN));
    assert.equal(r.signature, 'sig-' + VER);
    assert.equal(r.os, 'linux');
    assert.equal(r.arch, 'amd64');
    assert.equal(typeof r.releasePublicKey, 'string');
    assert.equal(r.binaryPath, `/v1/agent/binary?os=linux&arch=amd64&version=${VER}`);
    await rejects(app.api.agentRelease({ query: { os: 'linux', arch: 'arm64' } }), 'not-found', 404);
    await rejects(app.api.agentRelease({ query: { os: 'windows' as never, arch: 'amd64' } }), 'invalid', 422);
    const b = await raw('GET', r.binaryPath, { headers: signHeaders(app.key, id, 'app', 'GET', r.binaryPath, null) });
    assert.equal(b.status, 200);
    assert.deepEqual(b.body, BIN);
    assert.equal(b.headers.get('x-opm-sha256'), sha256Hex(BIN));
    assert.equal(b.headers.get('x-opm-agent-version'), VER);
    const bad = '/v1/agent/binary?os=linux&arch=amd64&version=..%2Fx';
    assert.equal((await raw('GET', bad, { headers: signHeaders(app.key, id, 'app', 'GET', bad, null) })).status, 422);
    const missing = '/v1/agent/binary?os=linux&arch=amd64&version=9.9.9';
    assert.equal((await raw('GET', missing, { headers: signHeaders(app.key, id, 'app', 'GET', missing, null) })).status, 404);
    assert.equal((await raw('GET', '/v1/agent/release?os=linux&arch=amd64')).status, 401);
  });

  await t.test('heartbeat guiPorts + edge.defaultEdge / bindErrors owner → stored, setup guiUrls (port 80 first), defaultEdge, portAlerts; bad shapes 422', async () => {
    const hb = { siteSlug: 'app', agentVersion: VER, headRevId: null, staging: null, peersSeen: [], metrics: { cpu: 0, memPct: 0, diskPct: 0 }, edge: null, keyVersion: 1, advertiseHost: '127.0.0.1', agentPort: 7080, ip: '139.59.2.56', hostname: 'droplet', detectedAddresses: ['139.59.2.56', '10.47.0.5', 'fe80::1', 'droplet'] };
    // old agent: no fields → guiUrls [], defaultEdge null, no alerts
    await app.api.heartbeat({ body: hb });
    let setup = await admin.api.getSetup({ deploymentId: id });
    let site = setup.sites.find((s) => s.siteSlug === 'app')!;
    assert.deepEqual(site.guiUrls, []);
    assert.equal(site.defaultEdge, null);
    assert.deepEqual(site.portAlerts, []);
    const guiPorts = [
      { port: 7080, scheme: 'http' as const, kind: 'agent-port' as const },
      { port: 8080, scheme: 'http' as const, kind: 'gui-alias' as const },
      { port: 80, scheme: 'http' as const, kind: 'edge' as const },
    ];
    const edge = { revSeq: null, reloadedAt: null, lastReloadError: null, boundPorts: [80, 7080, 8080], bindErrors: [], certs: [], defaultEdge: true };
    await app.api.heartbeat({ body: { ...hb, edge, guiPorts } });
    setup = await admin.api.getSetup({ deploymentId: id });
    site = setup.sites.find((s) => s.siteSlug === 'app')!;
    assert.equal(site.defaultEdge, true);
    assert.deepEqual(site.guiUrls, [
      { url: 'http://139.59.2.56/_opm/', kind: 'edge' },
      { url: 'http://10.47.0.5/_opm/', kind: 'edge' },
      { url: 'http://139.59.2.56:8080/_opm/', kind: 'gui-alias' },
      { url: 'http://10.47.0.5:8080/_opm/', kind: 'gui-alias' },
      { url: 'http://139.59.2.56:7080/_opm/', kind: 'agent-port' },
      { url: 'http://10.47.0.5:7080/_opm/', kind: 'agent-port' },
    ]);
    const stored = (await adb.collection('deployments').doc(id).collection('agents').doc('app').get()).data()!;
    assert.deepEqual(stored.guiPorts, guiPorts);
    // :80 held by another program → portAlerts
    const bindErrors = [
      { port: 80, purpose: 'edge-http' as const, error: 'listen tcp :80: bind: address already in use', owner: 'nginx' },
      { port: 443, purpose: 'edge-https' as const, error: 'in use', owner: null },
      { port: 8080, purpose: 'gui-alias' as const, error: 'in use' },
    ];
    await app.api.heartbeat({ body: { ...hb, edge: { ...edge, bindErrors, boundPorts: [7080] }, guiPorts: guiPorts.slice(0, 1) } });
    setup = await admin.api.getSetup({ deploymentId: id });
    site = setup.sites.find((s) => s.siteSlug === 'app')!;
    assert.deepEqual(site.portAlerts, [
      { port: 80, purpose: 'edge-http', owner: 'nginx', message: 'port 80 is used by nginx — stop it or change edge.httpPort' },
      { port: 443, purpose: 'edge-https', owner: null, message: 'port 443 is used by another program — stop it or change edge.httpPort' },
    ]);
    assert.deepEqual(site.guiUrls.map((u) => u.url), ['http://139.59.2.56:7080/_opm/', 'http://10.47.0.5:7080/_opm/']);
    // not enrolled site → []
    const db = setup.sites.find((s) => s.siteSlug === 'db')!;
    assert.deepEqual(db.guiUrls, []);
    assert.equal(db.defaultEdge, null);
    // bad shapes
    await rejects(app.api.heartbeat({ body: { ...hb, edge: { ...edge, defaultEdge: 'yes' } as never } }), 'invalid', 422);
    await rejects(app.api.heartbeat({ body: { ...hb, edge: { ...edge, bindErrors: [{ port: 80, purpose: 'edge-http', error: 'x', owner: 'n'.repeat(256) }] } as never } }), 'invalid', 422);
    await rejects(app.api.heartbeat({ body: { ...hb, guiPorts: [{ port: 0, scheme: 'http', kind: 'edge' }] as never } }), 'invalid', 422);
    await rejects(app.api.heartbeat({ body: { ...hb, guiPorts: [{ port: 80, scheme: 'ftp', kind: 'edge' }] as never } }), 'invalid', 422);
    await rejects(app.api.heartbeat({ body: { ...hb, guiPorts: [{ port: 80, scheme: 'http', kind: 'takeover' }] as never } }), 'invalid', 422);
    await rejects(app.api.heartbeat({ body: { ...hb, guiPorts: Array(17).fill(guiPorts[0]) } }), 'invalid', 422);
  });

  await t.test('revokeAgent ends the upgrade rule; a re-enroll with a newer token does not revive the old one', async () => {
    await admin.api.revokeAgent({ deploymentId: id, siteSlug: 'app', body: { confirm: 'app' } });
    tinyScript(await raw('GET', '/v1/install/script', { headers: H }), /used for an earlier enrollment/);
    assert.equal((await raw('GET', `/v1/install/binary?os=linux&arch=amd64&version=${VER}`, { headers: H })).status, 410);
    const again = await enrollSite(admin, id, 'app');
    tinyScript(await raw('GET', '/v1/install/script', { headers: H }), /used for an earlier enrollment/); // the old used token stays dead
    assert.equal((await raw('GET', '/v1/install/script', { headers: { Authorization: `Bearer ${again.token}` } })).status, 200, 'the new enrollment token upgrades');
    await abucket.file('deps/manifest.json').delete().catch(() => undefined);
  });
});
