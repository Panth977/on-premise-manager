/**
 * Round I (SPEC-final §21 #90): agent upgrades from the cloud console.
 * - pinned release (deps/manifest.json agent { version, notes }) → GET /v1/agent/release notes + sizeBytes, CloudView /
 *   Setup agentRelease
 * - requestAgentUpgrade → agents/{site}.upgradeRequest → CloudView.agentUpgrades → heartbeat progress … done clears it
 *   (_cloud activities agent.upgrade-requested / -cancelled + root audit deployment.agent-upgrade-*)
 * - stale / invalid (+ hint) / conflict; cancel before start / conflict after start
 * - upgrade all: sequential (A done → B queued), B rolled-back → run failed, C cancelled; old agent skipped with the hint
 * - expiry (read = absent; heartbeat / daily job fail the run); Setup + Status `upgrade`; upload-deps --agent-notes
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import type { AgentUpgradeProgress, AgentUpgradeRequest, DepsManifest, HeartbeatRequest } from '@opm/shared';
import { abucket, adb, agentClient, enrollSite, helloDeployment, here, newSiteKey, person, PROJECT, raw, rejects, sha256Hex, signHeaders, type Person } from './helpers.ts';
import { runDaily } from '../functions/src/jobs/daily.ts';

const PIN = '0.10.3-20';
const NOTES = 'Round I release\n- upgrade from the GUI\n- boot guard';
const BIN = Buffer.from('fake opm-agent binary ' + PIN + ' '.repeat(100));

async function uploadPin(version = PIN, notes: string | null = NOTES) {
  const base = `agent/${version}/linux-amd64/opm-agent`;
  await abucket.file(base).save(BIN);
  await abucket.file(`${base}.sha256`).save(sha256Hex(BIN) + '\n');
  await abucket.file(`${base}.sig`).save('sig-' + version + '\n');
  const manifest: DepsManifest = {
    docker: { version: '0.0.1-i', files: { amd64: { path: 'deps/docker/i/amd64/docker.tgz', sha256: 'e'.repeat(64), size: 1 }, arm64: { path: 'deps/docker/i/arm64/docker.tgz', sha256: 'f'.repeat(64), size: 1 } } },
    minDockerApi: '1.41',
    agent: { version, notes },
    updatedAt: new Date().toISOString(),
  };
  await abucket.file('deps/manifest.json').save(JSON.stringify(manifest));
}

type Api = ReturnType<typeof agentClient>;
function hb(siteSlug: string, agentVersion: string, upgrade?: AgentUpgradeProgress | null): HeartbeatRequest {
  return { siteSlug, agentVersion, headRevId: null, staging: null, peersSeen: [], metrics: { cpu: 0, memPct: 0, diskPct: 0 } as never, edge: null, keyVersion: 1, advertiseHost: '127.0.0.1', agentPort: 7080, ip: '127.0.0.1', hostname: `host-${siteSlug}`, detectedAddresses: [], ...(upgrade !== undefined ? { upgrade } : {}) };
}
function progress(req: AgentUpgradeRequest, from: string, phase: AgentUpgradeProgress['phase'], extra: Partial<AgentUpgradeProgress> = {}): AgentUpgradeProgress {
  const now = new Date().toISOString();
  return { upgradeId: `U${req.requestId.slice(1)}`, requestId: req.requestId, runId: req.runId, via: 'cloud', siteSlug: req.siteSlug, from, to: req.version, phase, progress: phase === 'download' ? 0.5 : null, bytesDone: 10, bytesTotal: 20, verification: null, error: null, actor: req.requestedBy, startedAt: now, updatedAt: now, finishedAt: ['done', 'failed', 'rolled-back'].includes(phase) ? now : null, ...extra };
}
/** an enrolled site written directly (more sites than the hello config) */
async function fakeSite(depId: string, slug: string, agentVersion: string): Promise<Api> {
  const key = newSiteKey();
  const now = new Date().toISOString();
  await adb.collection('deployments').doc(depId).collection('agents').doc(slug).set({ siteSlug: slug, publicKey: key.publicKey, enrolledAt: now, agentVersion, lastSeenAt: now, ip: '127.0.0.1', hostname: `host-${slug}`, advertiseHost: '127.0.0.1', agentPort: 7080, os: 'linux', arch: 'amd64', relayVia: null, headRevId: null, staging: null, keyVersion: 1, ackSeq: 0, metrics: null, edge: null, peersSeen: [], detectedAddresses: [] });
  return agentClient(key, depId, slug);
}
const agentDoc = async (depId: string, slug: string) => (await adb.collection('deployments').doc(depId).collection('agents').doc(slug).get()).data()!;
const epoch = async (depId: string) => (await adb.collection('deployments').doc(depId).get()).data()!.agentEpoch as number;
async function cloudTypes(admin: Person, depId: string): Promise<{ type: string; data: Record<string, unknown> }[]> {
  const r = await admin.api.listActivities({ deploymentId: depId, query: { site: '_cloud', type: 'agent.', limit: 500 } });
  return r.items.map((a) => ({ type: a.type, data: a.data }));
}

test('round I: agent upgrades from the cloud console', async (t) => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  await uploadPin();
  const app = await enrollSite(admin, id, 'app');
  const db = await enrollSite(admin, id, 'db');
  await app.api.heartbeat({ body: hb('app', '0.10.0-1') });
  await db.api.heartbeat({ body: hb('db', '0.7.2-10080232') });

  await t.test('release notes + sizeBytes: agentRelease, poll agentRelease, setup agentRelease', async () => {
    const r = await app.api.agentRelease({ query: { os: 'linux', arch: 'amd64' } });
    assert.equal(r.version, PIN);
    assert.equal(r.notes, NOTES);
    assert.equal(r.sizeBytes, BIN.length);
    const view = await app.api.poll({ query: { wait: 0 } });
    assert.deepEqual(view.agentRelease, { version: PIN, notes: NOTES });
    assert.deepEqual(view.agentUpgrades, []);
    const setup = await admin.api.getSetup({ deploymentId: id });
    assert.deepEqual(setup.agentRelease, { version: PIN, notes: NOTES });
    assert.equal(setup.agentUpgradeRun, null);
    const sa = setup.sites.find((s) => s.siteSlug === 'app')!.upgrade!;
    assert.deepEqual({ ...sa }, { installed: '0.10.0-1', latest: PIN, available: true, supported: true, hint: null, request: null, progress: null });
    const sd = setup.sites.find((s) => s.siteSlug === 'db')!.upgrade!;
    assert.equal(sd.supported, false);
    assert.equal(sd.available, true);
    assert.match(sd.hint!, /rerun the install command of this site once/);
  });

  await t.test('stale / invalid (+ hint) / not enrolled / bad confirm', async () => {
    await rejects(admin.api.requestAgentUpgrade({ deploymentId: id, siteSlug: 'app', body: { version: '0.10.2', confirm: 'app' } }), 'stale', 409);
    await rejects(admin.api.requestAgentUpgrade({ deploymentId: id, siteSlug: 'app', body: { version: PIN, confirm: 'db' } }), 'invalid', 422);
    const e = await rejects(admin.api.requestAgentUpgrade({ deploymentId: id, siteSlug: 'db', body: { version: PIN, confirm: 'db' } }), 'invalid', 422);
    assert.match(String(e.details.hint), /rerun the install command of this site once/);
    await rejects(admin.api.requestAgentUpgrade({ deploymentId: id, siteSlug: 'nope', body: { version: PIN, confirm: 'nope' } }), 'invalid', 422);
    const ro = await person([{ resource: 'deployment.setup', actions: ['read'], scope: { kind: 'all' } }] as never);
    await rejects(ro.api.requestAgentUpgrade({ deploymentId: id, siteSlug: 'app', body: { version: PIN, confirm: 'app' } }), 'forbidden', 403);
  });

  await t.test('queued request → poll agentUpgrades → progress download … done clears it (+ activities, audit)', async () => {
    const e0 = await epoch(id);
    const { request } = await admin.api.requestAgentUpgrade({ deploymentId: id, siteSlug: 'app', body: { version: PIN, confirm: 'app' } });
    assert.equal(request.siteSlug, 'app');
    assert.equal(request.version, PIN);
    assert.equal(request.requestedBy, admin.email);
    assert.equal(request.requestedByUid, admin.uid);
    assert.equal(request.runId, null);
    assert.equal(Date.parse(request.expiresAt) - Date.parse(request.requestedAt), 24 * 3600 * 1000);
    assert.ok((await epoch(id)) > e0, 'agentEpoch +1 so long polls wake up');
    let view = await app.api.poll({ query: { wait: 0 } });
    assert.deepEqual(view.agentUpgrades, [request]);
    // the other site sees it too (each agent filters its own)
    assert.deepEqual((await db.api.poll({ query: { wait: 0 } })).agentUpgrades, [request]);
    await rejects(admin.api.requestAgentUpgrade({ deploymentId: id, siteSlug: 'app', body: { version: PIN, confirm: 'app' } }), 'conflict', 409);
    await rejects(admin.api.upgradeAllAgents({ deploymentId: id, body: { version: PIN, confirm: 'UPGRADE' } }), 'conflict', 409);
    let setup = await admin.api.getSetup({ deploymentId: id });
    assert.deepEqual(setup.sites.find((s) => s.siteSlug === 'app')!.upgrade!.request, request);
    for (const phase of ['download', 'verify', 'swap', 'restart', 'health'] as const) {
      await app.api.heartbeat({ body: hb('app', '0.10.0-1', progress(request, '0.10.0-1', phase)) });
      assert.equal((await agentDoc(id, 'app')).upgradeRequest.requestId, request.requestId, `kept during ${phase}`);
      assert.equal((await agentDoc(id, 'app')).upgrade.phase, phase);
    }
    const st = await admin.api.getStatus({ deploymentId: id });
    assert.equal(st.sites.find((s) => s.siteSlug === 'app')!.upgrade!.progress!.phase, 'health');
    const e1 = await epoch(id);
    // the NEW agent reports done
    await app.api.heartbeat({ body: hb('app', PIN, progress(request, '0.10.0-1', 'done')) });
    assert.equal((await agentDoc(id, 'app')).upgradeRequest, null);
    assert.ok((await epoch(id)) > e1);
    view = await app.api.poll({ query: { wait: 0 } });
    assert.deepEqual(view.agentUpgrades, []);
    setup = await admin.api.getSetup({ deploymentId: id });
    const u = setup.sites.find((s) => s.siteSlug === 'app')!.upgrade!;
    assert.equal(u.installed, PIN);
    assert.equal(u.available, false);
    assert.equal(u.request, null);
    assert.equal(u.progress!.phase, 'done');
    assert.equal(u.progress!.siteSlug, 'app');
    const types = await cloudTypes(admin, id);
    assert.ok(types.some((a) => a.type === 'agent.upgrade-requested' && a.data.requestId === request.requestId && a.data.siteSlug === 'app' && a.data.version === PIN));
    const audit = await admin.api.listAuditLog({ query: { target: `deployments/${id}` } });
    const ent = audit.items.find((x) => x.action === 'deployment.agent-upgrade-requested' && x.outcome === 'ok');
    assert.ok(ent, JSON.stringify(audit.items.map((x) => x.action)));
    assert.equal(ent.op, 'requestAgentUpgrade');
    assert.equal(ent.data.requestId, request.requestId);
    // already on it
    await rejects(admin.api.requestAgentUpgrade({ deploymentId: id, siteSlug: 'app', body: { version: PIN, confirm: 'app' } }), 'invalid', 422);
  });

  await t.test('cancel before start; conflict after start; failed clears it; agentVersion reaching the pin clears it', async () => {
    await app.api.heartbeat({ body: hb('app', '0.10.1') });
    const { request } = await admin.api.requestAgentUpgrade({ deploymentId: id, siteSlug: 'app', body: { version: PIN, confirm: 'app' } });
    assert.deepEqual(await admin.api.cancelAgentUpgrade({ deploymentId: id, siteSlug: 'app' }), { cancelled: true });
    assert.equal((await agentDoc(id, 'app')).upgradeRequest, null);
    assert.deepEqual((await app.api.poll({ query: { wait: 0 } })).agentUpgrades, []);
    await rejects(admin.api.cancelAgentUpgrade({ deploymentId: id, siteSlug: 'app' }), 'not-found', 404);
    const types = await cloudTypes(admin, id);
    assert.ok(types.some((a) => a.type === 'agent.upgrade-cancelled' && a.data.requestId === request.requestId));
    const audit = await admin.api.listAuditLog({ query: { target: `deployments/${id}` } });
    assert.ok(audit.items.some((x) => x.action === 'deployment.agent-upgrade-cancelled' && x.op === 'cancelAgentUpgrade' && x.data.requestId === request.requestId));
    // started → conflict; failed → cleared
    const r2 = (await admin.api.requestAgentUpgrade({ deploymentId: id, siteSlug: 'app', body: { version: PIN, confirm: 'app' } })).request;
    await app.api.heartbeat({ body: hb('app', '0.10.1', progress(r2, '0.10.1', 'download')) });
    await rejects(admin.api.cancelAgentUpgrade({ deploymentId: id, siteSlug: 'app' }), 'conflict', 409);
    await rejects(admin.api.requestAgentUpgrade({ deploymentId: id, siteSlug: 'app', body: { version: PIN, confirm: 'app' } }), 'conflict', 409);
    await app.api.heartbeat({ body: hb('app', '0.10.1', progress(r2, '0.10.1', 'failed', { error: 'the pinned release moved to 9.9.9; request again' })) });
    assert.equal((await agentDoc(id, 'app')).upgradeRequest, null);
    assert.equal((await agentDoc(id, 'app')).upgrade.error, 'the pinned release moved to 9.9.9; request again');
    // upgraded by other means (CLI / rerun install): the reported version reaching the request clears it
    await admin.api.requestAgentUpgrade({ deploymentId: id, siteSlug: 'app', body: { version: PIN, confirm: 'app' } });
    await app.api.heartbeat({ body: hb('app', PIN) });
    assert.equal((await agentDoc(id, 'app')).upgradeRequest, null);
    // omitted upgrade keeps the stored progress
    assert.equal((await agentDoc(id, 'app')).upgrade.phase, 'failed');
    // heartbeat validation
    await rejects(app.api.heartbeat({ body: hb('app', PIN, { ...progress(r2, '0.10.1', 'done'), phase: 'exploded' as never }) }), 'invalid', 422);
    await rejects(app.api.heartbeat({ body: hb('app', PIN, { ...progress(r2, '0.10.1', 'done'), upgradeId: 7 as never }) }), 'invalid', 422);
  });

  await t.test('upgrade all: sequential in siteSlug order; A done → B queued; B rolled-back → run failed, C cancelled; old agent skipped', async () => {
    await app.api.heartbeat({ body: hb('app', '0.10.0') });
    const cache = await fakeSite(id, 'cache', '0.10.1');
    const edge = await fakeSite(id, 'edge', '0.10.0');
    await fakeSite(id, 'fresh', PIN); // up to date → not in the run
    await rejects(admin.api.upgradeAllAgents({ deploymentId: id, body: { version: PIN, confirm: 'upgrade' as never } }), 'invalid', 422);
    await rejects(admin.api.upgradeAllAgents({ deploymentId: id, body: { version: '0.1.0', confirm: 'UPGRADE' } }), 'stale', 409);
    await rejects(admin.api.cancelAgentUpgradeRun({ deploymentId: id }), 'not-found', 404);
    const { run } = await admin.api.upgradeAllAgents({ deploymentId: id, body: { version: PIN, confirm: 'UPGRADE' } });
    assert.equal(run.status, 'running');
    assert.equal(run.origin, 'cloud');
    assert.equal(run.requestedBy, admin.email);
    assert.deepEqual(run.sites.map((s) => [s.siteSlug, s.status]), [['app', 'requested'], ['cache', 'pending'], ['db', 'skipped'], ['edge', 'pending']]);
    assert.match(run.sites[2]!.error!, /rerun the install command of this site once/);
    assert.equal(run.sites[0]!.from, '0.10.0');
    await rejects(admin.api.upgradeAllAgents({ deploymentId: id, body: { version: PIN, confirm: 'UPGRADE' } }), 'conflict', 409);
    await rejects(admin.api.requestAgentUpgrade({ deploymentId: id, siteSlug: 'cache', body: { version: PIN, confirm: 'cache' } }), 'conflict', 409);
    let view = await app.api.poll({ query: { wait: 0 } });
    assert.deepEqual(view.agentUpgrades!.map((r) => [r.siteSlug, r.runId]), [['app', run.runId]]);
    const reqA = view.agentUpgrades![0]!;
    assert.equal(reqA.requestId, run.sites[0]!.requestId);
    // A starts → running; A done → B queued (only once even when the done heartbeat repeats)
    await app.api.heartbeat({ body: hb('app', '0.10.0', progress(reqA, '0.10.0', 'download')) });
    let setup = await admin.api.getSetup({ deploymentId: id });
    assert.equal(setup.agentUpgradeRun!.sites[0]!.status, 'running');
    await app.api.heartbeat({ body: hb('app', PIN, progress(reqA, '0.10.0', 'done')) });
    await app.api.heartbeat({ body: hb('app', PIN, progress(reqA, '0.10.0', 'done')) });
    view = await app.api.poll({ query: { wait: 0 } });
    assert.deepEqual(view.agentUpgrades!.map((r) => r.siteSlug), ['cache']);
    const reqB = view.agentUpgrades![0]!;
    assert.equal(reqB.runId, run.runId);
    assert.equal(reqB.requestedBy, admin.email);
    setup = await admin.api.getSetup({ deploymentId: id });
    assert.deepEqual(setup.agentUpgradeRun!.sites.map((s) => [s.siteSlug, s.status]), [['app', 'done'], ['cache', 'requested'], ['db', 'skipped'], ['edge', 'pending']]);
    assert.equal(setup.agentUpgradeRun!.sites[1]!.requestId, reqB.requestId);
    // B rolled back → run failed, C cancelled, no request left
    await cache.heartbeat({ body: hb('cache', '0.10.1', progress(reqB, '0.10.1', 'rolled-back', { error: 'not healthy within 60 s' })) });
    setup = await admin.api.getSetup({ deploymentId: id });
    const r = setup.agentUpgradeRun!;
    assert.equal(r.status, 'failed');
    assert.ok(r.finishedAt);
    assert.deepEqual(r.sites.map((s) => [s.siteSlug, s.status]), [['app', 'done'], ['cache', 'failed'], ['db', 'skipped'], ['edge', 'cancelled']]);
    assert.match(r.sites[1]!.error!, /rolled-back: not healthy within 60 s/);
    assert.deepEqual((await edge.poll({ query: { wait: 0 } })).agentUpgrades, []);
    await rejects(admin.api.cancelAgentUpgradeRun({ deploymentId: id }), 'conflict', 409);
    const types = await cloudTypes(admin, id);
    assert.ok(types.some((a) => a.type === 'agent.upgrade-requested' && a.data.runId === run.runId && a.data.siteSlug === 'cache'));
    // the deployment view carries the run too
    assert.equal((await admin.api.getDeployment({ deploymentId: id })).agentUpgradeRun!.runId, run.runId);
  });

  await t.test('cancel a run: the queued, unstarted request is removed, remaining sites cancelled; audit', async () => {
    await app.api.heartbeat({ body: hb('app', '0.10.0') });
    const { run } = await admin.api.upgradeAllAgents({ deploymentId: id, body: { version: PIN, confirm: 'UPGRADE' } });
    assert.deepEqual(run.sites.filter((s) => s.status !== 'skipped').map((s) => s.siteSlug), ['app', 'cache', 'edge']);
    const { run: c } = await admin.api.cancelAgentUpgradeRun({ deploymentId: id });
    assert.equal(c.status, 'cancelled');
    assert.deepEqual(c.sites.map((s) => s.status), ['cancelled', 'cancelled', 'skipped', 'cancelled']);
    assert.equal((await agentDoc(id, 'app')).upgradeRequest, null);
    const audit = await admin.api.listAuditLog({ query: { target: `deployments/${id}` } });
    assert.ok(audit.items.some((x) => x.op === 'cancelAgentUpgradeRun' && x.action === 'deployment.agent-upgrade-cancelled' && x.data.runId === run.runId));
    assert.ok(audit.items.some((x) => x.op === 'upgradeAllAgents' && x.action === 'deployment.agent-upgrade-requested' && x.data.runId === run.runId));
    // cancelling the run's queued request itself also cancels the run
    const { run: r2 } = await admin.api.upgradeAllAgents({ deploymentId: id, body: { version: PIN, confirm: 'UPGRADE' } });
    await admin.api.cancelAgentUpgrade({ deploymentId: id, siteSlug: 'app' });
    const s = await admin.api.getSetup({ deploymentId: id });
    assert.equal(s.agentUpgradeRun!.runId, r2.runId);
    assert.equal(s.agentUpgradeRun!.status, 'cancelled');
  });

  await t.test('expiry: an expired request is absent on read; heartbeat / daily job fail the run with the expired error', async () => {
    const past = new Date(Date.now() - 1000).toISOString();
    // single request: expired → not in the poll / setup, a new request is allowed
    await admin.api.requestAgentUpgrade({ deploymentId: id, siteSlug: 'app', body: { version: PIN, confirm: 'app' } });
    await adb.collection('deployments').doc(id).collection('agents').doc('app').update({ 'upgradeRequest.expiresAt': past });
    assert.deepEqual((await app.api.poll({ query: { wait: 0 } })).agentUpgrades, []);
    assert.equal((await admin.api.getSetup({ deploymentId: id })).sites.find((x) => x.siteSlug === 'app')!.upgrade!.request, null);
    const again = (await admin.api.requestAgentUpgrade({ deploymentId: id, siteSlug: 'app', body: { version: PIN, confirm: 'app' } })).request;
    // a heartbeat of an agent that never picked it up, after expiry → cleared + agent.upgrade-cancelled reason expired
    await adb.collection('deployments').doc(id).collection('agents').doc('app').update({ 'upgradeRequest.expiresAt': past });
    await app.api.heartbeat({ body: hb('app', '0.10.0') });
    assert.equal((await agentDoc(id, 'app')).upgradeRequest, null);
    assert.ok((await cloudTypes(admin, id)).some((a) => a.type === 'agent.upgrade-cancelled' && a.data.requestId === again.requestId && a.data.reason === 'expired'));
    // run: the current request expires → setup shows the run failed at once; the daily job persists it
    const { run } = await admin.api.upgradeAllAgents({ deploymentId: id, body: { version: PIN, confirm: 'UPGRADE' } });
    await adb.collection('deployments').doc(id).collection('agents').doc('app').update({ 'upgradeRequest.expiresAt': past });
    const shown = (await admin.api.getSetup({ deploymentId: id })).agentUpgradeRun!;
    assert.equal(shown.status, 'failed');
    assert.equal(shown.sites[0]!.error, 'expired: the agent did not pick it up within 24 h');
    assert.equal((await adb.collection('deployments').doc(id).get()).data()!.agentUpgradeRun.status, 'running', 'not persisted by a read');
    const d = await runDaily();
    assert.ok(d.agentUpgradesExpired >= 1);
    const stored = (await adb.collection('deployments').doc(id).get()).data()!.agentUpgradeRun;
    assert.equal(stored.runId, run.runId);
    assert.equal(stored.status, 'failed');
    assert.deepEqual(stored.sites.map((s: { status: string }) => s.status), ['failed', 'cancelled', 'skipped', 'cancelled']);
    assert.equal((await agentDoc(id, 'app')).upgradeRequest, null);
  });

  await t.test('status: SiteCloudStatus.upgrade (null when not enrolled)', async () => {
    await adb.collection('deployments').doc(id).collection('agents').doc('fresh').delete();
    const st = await admin.api.getStatus({ deploymentId: id });
    const a = st.sites.find((s) => s.siteSlug === 'app')!.upgrade!;
    assert.equal(a.latest, PIN);
    assert.equal(a.supported, true);
    assert.equal(st.sites.find((s) => s.siteSlug === 'db')!.upgrade!.supported, false);
    await admin.api.revokeAgent({ deploymentId: id, siteSlug: 'db', body: { confirm: 'db' } });
    const st2 = await admin.api.getStatus({ deploymentId: id });
    assert.equal(st2.sites.find((s) => s.siteSlug === 'db')!.upgrade, null);
    assert.equal((await admin.api.getSetup({ deploymentId: id })).sites.find((s) => s.siteSlug === 'db')!.upgrade, null);
  });

  await t.test('used install token of the current enrollment → upgrade-only script with the boot guard + GUI upgrade line', async () => {
    const s = await raw('GET', '/v1/install/script', { headers: { Authorization: `Bearer ${app.token}` } });
    assert.equal(s.status, 200);
    const sh = s.body.toString();
    assert.ok(sh.includes('UPGRADE_ONLY="1"'));
    assert.ok(sh.includes(`AGENT_VERSION="${PIN}"`));
    assert.ok(sh.includes('GUI_UPGRADE="1"'));
    assert.ok(sh.includes('upgrade-guard --install --data-dir'));
    assert.ok(sh.includes('Upgrades from now on: cloud console Setup → Upgrade agent, or the agent GUI Agent page'));
    void signHeaders;
  });

  await abucket.file('deps/manifest.json').delete().catch(() => undefined);
});

test('upload-deps --agent-notes / --agent-notes-file: written, kept for the same version, cleared for a new one, > 8000 refused', () => {
  const dir = mkdtempSync(join(tmpdir(), 'opm-deps-'));
  for (const a of ['amd64', 'arm64']) writeFileSync(join(dir, `docker-0.0.7-${a}.tgz`), `fake docker ${a}`);
  const notesFile = join(dir, 'notes.md');
  writeFileSync(notesFile, '# 0.10.0\n- from a file\n');
  const env = { ...process.env, GCLOUD_PROJECT: PROJECT };
  const run = (...args: string[]) => execFileSync('npx', ['tsx', resolve(here, '../scripts/upload-deps.ts'), '--project', PROJECT, '--from-dir', dir, '--docker-version', '0.0.7', ...args], { env, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  const manifest = async () => JSON.parse((await abucket.file('deps/manifest.json').download())[0].toString('utf8')) as DepsManifest;
  return (async () => {
    run('--agent-version', '0.10.0-n', '--agent-notes', 'inline notes');
    assert.deepEqual((await manifest()).agent, { version: '0.10.0-n', notes: 'inline notes', schema: null, minSchema: null });
    const out = run('--agent-version', '0.10.0-n');
    assert.match(out, /notes 12 chars \(kept\)/);
    assert.deepEqual((await manifest()).agent, { version: '0.10.0-n', notes: 'inline notes', schema: null, minSchema: null });
    run('--agent-version', '0.10.0-n', '--agent-notes-file', notesFile);
    assert.equal((await manifest()).agent!.notes, '# 0.10.0\n- from a file');
    run('--agent-version', '0.10.1-n');
    assert.deepEqual((await manifest()).agent, { version: '0.10.1-n', notes: null, schema: null, minSchema: null });
    writeFileSync(notesFile, 'x'.repeat(8001));
    assert.throws(() => run('--agent-version', '0.10.1-n', '--agent-notes-file', notesFile), /at most 8000/);
    assert.throws(() => run('--agent-version', '0.10.1-n', '--agent-notes', 'a', '--agent-notes-file', notesFile), /either --agent-notes or --agent-notes-file/);
    assert.equal((await manifest()).agent!.version, '0.10.1-n', 'a refused run writes nothing');
    await abucket.file('deps/manifest.json').delete();
  })();
});
