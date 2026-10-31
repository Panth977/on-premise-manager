import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createPublicKey, sign, verify } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { activityHashInput, canonicalize, createCloudClient, ulid, type Activity, type Proposal, type Revision } from '@opm/shared';
import { abucket, adb, agentClient, BASE, helloDeployment, newSiteKey, person, raw, rejects, sha256Hex, signHeaders, sleep, enrollSite, here, type SiteKey, tinyScript, publish } from './helpers.ts';
import { depInput } from './helpers.ts';
import { signHex } from '../scripts/upload-agent.ts';
import { writeTar } from '../src/core/tar.ts';

const releaseKey = JSON.parse(readFileSync(resolve(here, '../../.secrets/release-key.json'), 'utf8'));

export function makeActivity(deploymentId: string, siteSlug: string, seq: number, prevHash: string | null, over: Partial<Activity> = {}): Activity {
  const base = { activityId: `${siteSlug}:${seq}`, deploymentId, siteSlug, seq, ts: new Date(Date.now() + seq).toISOString(), actor: 'ops@example.com', origin: 'agent' as const, type: 'actor.declared', summary: `entry ${seq}`, data: { n: seq, nested: [[1, 2], { a: null }] }, prevHash, ...over };
  const hash = 'sha256:' + sha256Hex(activityHashInput(base));
  return { ...base, hash, uploadedVia: null, uploadedAt: null };
}
export function chain(deploymentId: string, siteSlug: string, from: number, to: number, prev: string | null): Activity[] {
  const out: Activity[] = [];
  let p = prev;
  for (let s = from; s <= to; s++) {
    const a = makeActivity(deploymentId, siteSlug, s, p);
    out.push(a);
    p = a.hash;
  }
  return out;
}
export function opmlog(deploymentId: string, signer: { siteSlug: string; key: SiteKey }, entries: Activity[], o: { tamperSig?: boolean } = {}) {
  const nd = Buffer.from(entries.map((e) => canonicalize(e) + '\n').join(''), 'utf8');
  const sites = [...new Set(entries.map((e) => e.siteSlug))].map((s) => {
    const l = entries.filter((e) => e.siteSlug === s);
    return { siteSlug: s, fromSeq: l[0]!.seq, toSeq: l.at(-1)!.seq, count: l.length };
  });
  const manifest = { format: 'opmlog/1', deploymentId, createdAt: new Date().toISOString(), createdBy: 'ops@example.com', signedBy: { siteSlug: signer.siteSlug, publicKey: signer.key.publicKey }, sites, activities: { path: 'activities.ndjson', sizeBytes: nd.length, sha256: 'sha256:' + sha256Hex(nd) }, debug: [] };
  const mb = Buffer.from(canonicalize(manifest), 'utf8');
  let sig = sign(null, mb, signer.key.priv).toString('base64url');
  if (o.tamperSig) sig = sign(null, Buffer.from('x'), signer.key.priv).toString('base64url');
  return writeTar([
    { name: 'manifest.json', data: mb },
    { name: 'activities.ndjson', data: nd },
    { name: 'signature.sig', data: Buffer.from(sig, 'ascii') },
  ]);
}

test('agent lifecycle: install → enroll → signed calls → poll → staging → head → activities → relay → revoke', async (t) => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  let app!: Awaited<ReturnType<typeof enrollSite>>;
  let db!: Awaited<ReturnType<typeof enrollSite>>;

  await t.test('install token + script + binary', async () => {
    await rejects(admin.api.createInstallToken({ deploymentId: id, body: { siteSlug: 'nope' } }), 'not-found');
    const tok = await admin.api.createInstallToken({ deploymentId: id, body: { siteSlug: 'app' } });
    assert.match(tok.token, /^opi_[A-Za-z0-9_-]{43}$/);
    assert.equal(tok.tokenHash, sha256Hex(tok.token));
    assert.equal(tok.command, `curl -fsSL -H "Authorization: Bearer ${tok.token}" ${BASE}/v1/install/script | sudo sh`);
    const s = await raw('GET', '/v1/install/script', { headers: { Authorization: `Bearer ${tok.token}` } });
    assert.equal(s.status, 200);
    const sh = s.body.toString();
    assert.ok(sh.startsWith('#!/bin/sh\n# On-Prem Manager agent installer · site app'));
    assert.ok(sh.includes(`API="${BASE}"; DEP="${id}"; SITE="app"; TOKEN="${tok.token}"`));
    assert.ok(sh.includes(`RELEASE_PUBKEY="${releaseKey.publicKeyB64}"`));
    assert.ok(sh.includes('ExecStart=/usr/local/bin/opm-agent run --config /etc/opm/agent.json'));
    tinyScript(await raw('GET', '/v1/install/script', { headers: { Authorization: 'Bearer opi_unknown' } }), /unknown install token/);
    assert.equal((await raw('GET', '/v1/install/script')).status, 401);
    assert.equal((await raw('GET', '/v1/install/binary?os=linux&arch=arm64&version=9.9.9', { headers: { Authorization: `Bearer ${tok.token}` } })).status, 404);
    assert.equal((await raw('GET', '/v1/install/binary?os=windows&arch=arm64', { headers: { Authorization: `Bearer ${tok.token}` } })).status, 422);
    const bin = Buffer.from('#!/bin/sh\necho fake opm-agent\n');
    const hex = sha256Hex(bin);
    const base = 'agent/0.0.1-test/linux-arm64/opm-agent';
    await abucket.file(base).save(bin);
    await abucket.file(`${base}.sha256`).save(hex + '\n');
    await abucket.file(`${base}.sig`).save(signHex(hex, releaseKey.privateKeySeedB64) + '\n');
    await abucket.file('agent/latest').save('0.0.1-test\n');
    const b = await raw('GET', '/v1/install/binary?os=linux&arch=arm64', { headers: { Authorization: `Bearer ${tok.token}` } });
    assert.equal(b.status, 200);
    assert.deepEqual(b.body, bin);
    assert.equal(b.headers.get('x-opm-sha256'), hex);
    const pk = createPublicKey({ key: { kty: 'OKP', crv: 'Ed25519', x: Buffer.from(releaseKey.publicKeyB64, 'base64').toString('base64url') }, format: 'jwk' });
    assert.ok(verify(null, Buffer.from(hex), pk, Buffer.from(b.headers.get('x-opm-signature')!, 'base64url')), 'release signature verifies');
    const rg = await raw('GET', '/v1/install/binary?os=linux&arch=arm64', { headers: { Authorization: `Bearer ${tok.token}`, Range: 'bytes=0-8' } });
    assert.equal(rg.status, 206);
    assert.equal(rg.body.toString(), '#!/bin/sh');
    assert.equal(rg.headers.get('content-range'), `bytes 0-8/${bin.length}`);
    const setup = await admin.api.getSetup({ deploymentId: id });
    const appRow = setup.sites.find((x) => x.siteSlug === 'app')!;
    assert.equal(appRow.tokens[0]!.state, 'active');
    assert.equal(setup.apiBase, BASE);
    // revoke an unused token → the script says so (§21 #91: 200 + tiny script, exit 1)
    await admin.api.revokeInstallToken({ deploymentId: id, tokenHash: tok.tokenHash });
    tinyScript(await raw('GET', '/v1/install/script', { headers: { Authorization: `Bearer ${tok.token}` } }), /token revoked/);
    // expired token → gone
    const t2 = await admin.api.createInstallToken({ deploymentId: id, body: { siteSlug: 'app' } });
    await adb.collection('deployments').doc(id).collection('installTokens').doc(t2.tokenHash).update({ expiresAt: new Date(Date.now() - 1000).toISOString() });
    tinyScript(await raw('GET', '/v1/install/script', { headers: { Authorization: `Bearer ${t2.token}` } }), /token expired/);
  });

  await t.test('enroll: consumes the token; reuse → gone; enrolled site → conflict', async () => {
    app = await enrollSite(admin, id, 'app', { agentPort: 7080 });
    assert.equal(app.enroll.siteSlug, 'app');
    assert.equal(app.enroll.deploymentId, id);
    assert.equal(app.enroll.accessKey.version, 1);
    assert.match(app.enroll.accessKey.hash, /^\$argon2id\$/);
    assert.deepEqual(app.enroll.peers.map((p) => p.siteSlug), ['app']);
    const again = createCloudClient(BASE, () => `Bearer ${app.token}`);
    await rejects(again.enroll({ body: { publicKey: newSiteKey().publicKey, agentVersion: '1', hostname: 'h', ip: '1.2.3.4', advertiseHost: '1.2.3.4', agentPort: 7080, os: 'linux', arch: 'arm64' } }), 'gone', 410);
    await rejects(admin.api.createInstallToken({ deploymentId: id, body: { siteSlug: 'app' } }), 'conflict');
    const bad = await admin.api.createInstallToken({ deploymentId: id, body: { siteSlug: 'db' } });
    await rejects(createCloudClient(BASE, () => `Bearer ${bad.token}`).enroll({ body: { publicKey: 'short', agentVersion: '1', hostname: 'h', ip: '', advertiseHost: 'x', agentPort: 7081, os: 'linux', arch: 'arm64' } }), 'invalid');
    await admin.api.revokeInstallToken({ deploymentId: id, tokenHash: bad.tokenHash });
    db = await enrollSite(admin, id, 'db', { agentPort: 7081 });
    assert.deepEqual(db.enroll.peers.map((p) => p.siteSlug), ['app', 'db']);
    const acts = await admin.api.listActivities({ deploymentId: id, query: { type: 'install.enrolled' } });
    assert.equal(acts.items.length, 2);
    const setup = await admin.api.getSetup({ deploymentId: id });
    assert.equal(setup.sites.find((x) => x.siteSlug === 'app')!.agent?.hostname, 'host-app');
    assert.equal(setup.sites.find((x) => x.siteSlug === 'app')!.tokens.find((x) => x.state === 'used')?.siteSlug, 'app');
  });

  await t.test('signed requests: clock, nonce replay, bad signature, unknown site, tampered body', async () => {
    const path = '/v1/agent/poll?wait=0';
    const h1 = signHeaders(app.key, id, 'app', 'GET', path, null);
    const ok = await raw('GET', path, { headers: h1 });
    assert.equal(ok.status, 200, JSON.stringify(ok.json));
    // §21 #86 add-on: the agent GUI links to the cloud console and shows the deployment name
    assert.equal(typeof ok.json.deploymentLabel, 'string');
    assert.ok(ok.json.deploymentLabel.length > 0);
    assert.ok(ok.json.consoleUrl === null || /^https?:\/\//.test(ok.json.consoleUrl), String(ok.json.consoleUrl));
    assert.ok('consoleUrl' in app.enroll && 'deploymentLabel' in app.enroll);
    const replay = await raw('GET', path, { headers: h1 });
    assert.equal(replay.status, 401);
    assert.equal(replay.json.error.details.reason, 'nonce');
    const clock = await raw('GET', path, { headers: signHeaders(app.key, id, 'app', 'GET', path, null, { ts: Math.floor(Date.now() / 1000) - 120 }) });
    assert.equal(clock.json.error.details.reason, 'clock');
    assert.ok(clock.headers.get('x-opm-server-time'));
    const other = newSiteKey();
    const sig = await raw('GET', path, { headers: signHeaders(other, id, 'app', 'GET', path, null) });
    assert.equal(sig.json.error.details.reason, 'signature');
    const unk = await raw('GET', path, { headers: signHeaders(app.key, id, 'zzz', 'GET', path, null) });
    assert.equal(unk.json.error.details.reason, 'unknown-site');
    const unkDep = await raw('GET', path, { headers: signHeaders(app.key, 'NoSuchDeployment0001', 'app', 'GET', path, null) });
    assert.equal(unkDep.json.error.details.reason, 'unknown-site');
    const body = Buffer.from(JSON.stringify({ siteSlug: 'app' }));
    const hb = signHeaders(app.key, id, 'app', 'POST', '/v1/agent/heartbeat', body);
    const tampered = await raw('POST', '/v1/agent/heartbeat', { headers: { ...hb, 'Content-Type': 'application/json' }, body: Buffer.from(JSON.stringify({ siteSlug: 'db' })) });
    assert.equal(tampered.json.error.details.reason, 'signature');
    // path with a different query is a different string
    const hq = signHeaders(app.key, id, 'app', 'GET', '/v1/agent/poll?wait=0', null);
    const q2 = await raw('GET', '/v1/agent/poll?wait=1', { headers: hq });
    assert.equal(q2.json.error.details.reason, 'signature');
  });

  await t.test('poll: immediate answer, long-poll wakes on publish, holds when nothing changes', async () => {
    const v0 = await app.api.poll({ query: { wait: 0 } });
    assert.equal(v0.deploymentId, id);
    assert.equal(v0.source, 'cloud');
    assert.equal(v0.deleted, false);
    assert.equal(v0.draftHead, null);
    assert.equal(v0.accessKey.version, 1);
    assert.deepEqual(v0.peers.map((p) => p.siteSlug), ['app', 'db']);
    const t0 = Date.now();
    const held = await app.api.poll({ query: { cursor: v0.cursor, wait: 2 } });
    assert.ok(Date.now() - t0 >= 1800, 'held about 2 s');
    assert.equal(held.cursor, v0.cursor);
    const waiting = app.api.poll({ query: { cursor: v0.cursor, wait: 20 } });
    await sleep(1500);
    // §21 #104: a commit of the working copy is the release (compat proposal for these 0.1.0-test agents)
    const d0 = await admin.api.getDeployment({ deploymentId: id });
    const v1 = d0.draft.variables.map((v) => (v.variableSlug === 'greeting' ? { ...v, value: 'Hello release 1' } : v));
    const w1 = await admin.api.putDraftSection({ deploymentId: id, section: 'variables', body: { ifRevision: d0.draftVersion, items: v1 } });
    const pub = await publish(admin.api, id, { ifRevision: w1.draftVersion, summary: 'release 1' });
    const t1 = Date.now();
    const woke = await waiting;
    assert.ok(Date.now() - t1 < 5000, 'answered soon after the change');
    assert.notEqual(woke.cursor, v0.cursor);
    assert.equal(woke.proposals[0]!.revId, pub.proposal.revId);
    assert.equal(woke.proposals[0]!.status, 'published');
    assert.equal(woke.branch?.head?.commitId, pub.commit.commitId, '§21 #104 the cloud view carries the deployment head');
    assert.equal(woke.branch?.recent.length, 2);
  });

  let p1!: Proposal;
  let r1!: Revision;
  await t.test('proposal fetch (plain secrets + secret.fetched), staging reports → derived status', async () => {
    const view = await app.api.poll({ query: { wait: 0 } });
    const got = await app.api.getAgentProposal({ revId: view.proposals[0]!.revId });
    p1 = got.proposal;
    r1 = got.revision;
    assert.match(r1.config.secrets[0]!.value!, /^[A-Za-z0-9]{24}$/, 'plain secret for agents');
    const acts = await admin.api.listActivities({ deploymentId: id, query: { type: 'secret.fetched' } });
    assert.equal(acts.items[0]!.data.siteSlug, 'app');
    assert.deepEqual(acts.items[0]!.data.secretSlugs, ['api-token']);
    await rejects(app.api.reportProposalStatus({ revId: p1.revId, body: { siteSlug: 'db', staging: 'ready' } }), 'invalid');
    const s1 = await app.api.reportProposalStatus({ revId: p1.revId, body: { siteSlug: 'app', staging: 'downloading', progress: 0.5 } });
    assert.equal(s1.proposal.status, 'staging');
    assert.equal(s1.proposal.sites.app!.progress, 0.5);
    const s2 = await app.api.reportProposalStatus({ revId: p1.revId, body: { siteSlug: 'app', staging: 'ready' } });
    assert.equal(s2.proposal.status, 'staging');
    const s3 = await db.api.reportProposalStatus({ revId: p1.revId, body: { siteSlug: 'db', staging: 'ready' } });
    assert.equal(s3.proposal.status, 'ready');
    const s4 = await db.api.reportProposalStatus({ revId: p1.revId, body: { siteSlug: 'db', staging: 'failed', error: 'disk full' } });
    assert.equal(s4.proposal.status, 'staging', 'ready → staging when a site falls back');
    await db.api.reportProposalStatus({ revId: p1.revId, body: { siteSlug: 'db', staging: 'ready' } });
    const a = await app.api.reportProposalStatus({ revId: p1.revId, body: { siteSlug: 'app', status: 'approved', approvedBy: 'ops@example.com', approvedAt: new Date().toISOString(), updateId: ulid() } });
    assert.equal(a.proposal.status, 'approved');
    // LOCK failed → back to ready with result
    const lf = await app.api.reportProposalStatus({ revId: p1.revId, body: { siteSlug: 'app', result: { ok: false, message: 'lock refused by db', snapshotId: null } } });
    assert.equal(lf.proposal.status, 'ready');
    await app.api.reportProposalStatus({ revId: p1.revId, body: { siteSlug: 'app', status: 'approved', approvedBy: 'ops@example.com' } });
    const ap = await app.api.reportProposalStatus({ revId: p1.revId, body: { siteSlug: 'app', status: 'applying' } });
    assert.equal(ap.proposal.status, 'applying');
    // §21 #104: nothing to commit → invalid V_NO_CHANGE (a running update never blocks a commit; it only skips the compat proposal)
    await rejects(admin.real.commitDeployment({ deploymentId: id, body: { message: 'x', baseCommitId: (await admin.real.getDeployment({ deploymentId: id })).branch!.head!.commitId, config: await depInput(admin.real, id) } }), 'invalid');
    await rejects(app.api.reportProposalStatus({ revId: '01ZZZZZZZZZZZZZZZZZZZZZZZZ', body: { siteSlug: 'app', staging: 'ready' } }), 'not-found');
  });

  await t.test('reportHead: verify hashes, store, head moves, proposal applied, others stale, fork conflict', async () => {
    await rejects(app.api.reportHead({ body: { revision: { ...r1, hash: 'sha256:' + '0'.repeat(64) } } }), 'invalid');
    await rejects(app.api.reportHead({ body: { revision: { ...r1, parentRevId: ulid(), seq: 2 } } }), 'invalid');
    const r = await app.api.reportHead({ body: { revision: r1 } });
    assert.deepEqual(r, { stored: false, headChanged: true });
    const again = await db.api.reportHead({ body: { revision: r1 } });
    assert.deepEqual(again, { stored: false, headChanged: false });
    const d = await admin.api.getDeployment({ deploymentId: id });
    assert.equal(d.head?.revId, r1.revId);
    assert.equal(d.head?.seq, 1);
    const props = await admin.api.listProposals({ deploymentId: id });
    assert.equal(props.items.find((p) => p.revId === r1.revId)!.status, 'applied');
    // a stale proposal: publish P2 (parent = head), and a local fork at seq 2 wins → P2 stale
    const vars = (await admin.api.getDeployment({ deploymentId: id })).draft.variables.map((v) => (v.variableSlug === 'theme' ? { ...v, value: 'yellow' } : v));
    const w = await admin.api.putDraftSection({ deploymentId: id, section: 'variables', body: { ifRevision: d.draftVersion, items: vars } });
    const p2 = await publish(admin.api, id, { ifRevision: w.draftVersion, summary: 'release 2' } );
    const localCfg = structuredClone(r1.config);
    localCfg.variables = localCfg.variables.map((v) => (v.variableSlug === 'greeting' ? { ...v, value: 'Hi from db' } : v));
    const local: Revision = { ...r1, revId: ulid(), seq: 2, parentRevId: r1.revId, origin: 'agent', actor: 'ops@example.com', createdAt: new Date().toISOString(), config: localCfg, hash: 'sha256:' + sha256Hex(canonicalize(localCfg)), summary: 'local greeting', kind: 'change' };
    const lp: Proposal = { ...p1, revId: local.revId, parentRevId: r1.revId, seq: 2, summary: local.summary, origin: 'agent', status: 'published', createdBy: 'ops@example.com', sites: { app: { staging: 'pending', progress: 0, error: null, updatedAt: null }, db: { staging: 'pending', progress: 0, error: null, updatedAt: null } }, approvedBy: null, approvedAt: null, updateId: null, result: null, supersededBy: null };
    assert.deepEqual(await db.api.registerLocalProposal({ body: { proposal: lp, revision: local } }), { stored: true });
    assert.deepEqual(await db.api.registerLocalProposal({ body: { proposal: lp, revision: local } }), { stored: false });
    const list = await admin.api.listProposals({ deploymentId: id });
    assert.equal(list.items.find((x) => x.revId === p2.proposal.revId)!.status, 'superseded', 'the newer local proposal supersedes the cloud one');
    assert.equal(list.items.find((x) => x.revId === local.revId)!.origin, 'agent');
    const h2 = await db.api.reportHead({ body: { revision: local } });
    assert.deepEqual(h2, { stored: false, headChanged: true });
    // fork: another seq-2 revision
    const fork: Revision = { ...local, revId: ulid(), summary: 'fork' };
    await rejects(app.api.reportHead({ body: { revision: fork } }), 'conflict');
    // stale: a published proposal whose parent is no longer the head
    const d3 = await admin.api.getDeployment({ deploymentId: id });
    const vars2 = d3.draft.variables.map((v) => (v.variableSlug === 'theme' ? { ...v, value: 'blue' } : v));
    const w3 = await admin.api.putDraftSection({ deploymentId: id, section: 'variables', body: { ifRevision: d3.draftVersion, items: vars2 } });
    const p3 = await publish(admin.api, id, { ifRevision: w3.draftVersion, summary: 'release 3' } );
    assert.equal(p3.revision.seq, 3);
    const getR = await app.api.getAgentRevision({ revId: p3.proposal.revId });
    const cfg4 = structuredClone(local.config);
    cfg4.variables = cfg4.variables.map((v) => (v.variableSlug === 'greeting' ? { ...v, value: 'rollback' } : v));
    const r4: Revision = { ...local, revId: ulid(), seq: 3, parentRevId: local.revId, kind: 'rollback', rollbackOf: { snapshotId: ulid(), seq: 1 }, config: cfg4, hash: 'sha256:' + sha256Hex(canonicalize(cfg4)), summary: 'rollback to X (seq 1)' };
    assert.equal((await app.api.reportHead({ body: { revision: r4 } })).headChanged, true);
    const list2 = await admin.api.listProposals({ deploymentId: id });
    assert.equal(list2.items.find((x) => x.revId === p3.proposal.revId)!.status, 'stale');
    const stale = await admin.api.listActivities({ deploymentId: id, query: { type: 'proposal.stale' } });
    assert.equal(stale.items[0]!.data.revId, p3.proposal.revId);
    void getR;
    assert.equal((await admin.api.getDeployment({ deploymentId: id })).head?.seq, 3);
    // §21 #104: the pre-0.14 sites' agent-branch mirrors follow their reported heads; the local proposal is a legacy commit
    const ab = await admin.api.listAgentBranches({ deploymentId: id });
    assert.equal(ab.items.find((x) => x.siteSlug === 'app')!.head?.commitId, r4.revId);
    assert.ok(ab.items.every((x) => x.needsAgent014 && x.legacy));
    const lc = await admin.api.listAgentBranchCommits({ deploymentId: id, siteSlug: 'db' });
    assert.equal(lc.items.find((x) => x.commitId === local.revId)?.kind, 'legacy');
  });

  await t.test('image manifest + blobs: only head / open proposal content; Range; file blobs', async () => {
    const m = await app.api.getImageManifest({ serviceSlug: 'redis', version: 'v1' });
    assert.equal(m.config.sizeBytes, 1234);
    assert.equal(m.layers.length, 1);
    await rejects(app.api.getImageManifest({ serviceSlug: 'redis', version: 'other' }), 'forbidden');
    const banner = (await admin.api.getDeployment({ deploymentId: id })).draft.files[0]!.content!.sha256;
    const fb = await app.api.getAgentBlob({ sha256: banner });
    assert.equal(await fb.text(), 'Welcome to the hello-world deployment\n');
    const layerHex = m.layers[0]!.digest.slice(7);
    await abucket.file(`blobs/sha256/${layerHex}`).delete({ ignoreNotFound: true });
    await rejects(app.api.getAgentBlob({ sha256: m.layers[0]!.digest }), 'not-found');
    const layer = Buffer.alloc(5000, 7);
    void layer;
    await abucket.file(`blobs/sha256/${layerHex}`).save(layer);
    const lb = await app.api.getAgentBlob({ sha256: layerHex });
    assert.equal(Buffer.from(await lb.arrayBuffer()).length, 5000);
    const rg = await raw('GET', `/v1/agent/blobs/sha256:${layerHex}`, { headers: { ...signHeaders(app.key, id, 'app', 'GET', `/v1/agent/blobs/sha256:${layerHex}`, null), Range: 'bytes=4990-' } });
    assert.equal(rg.status, 206);
    assert.equal(rg.body.length, 10);
    assert.equal(rg.headers.get('accept-ranges'), 'bytes');
    await rejects(app.api.getAgentBlob({ sha256: 'sha256:' + 'a'.repeat(64) }), 'forbidden');
    await rejects(app.api.getAgentBlob({ sha256: 'zz' }), 'invalid');
  });

  await t.test('heartbeat: liveness, uptime beats, status online; advertise change bumps agentEpoch', async () => {
    const hb = { siteSlug: 'app', agentVersion: '0.1.0-test', headRevId: null, staging: null, peersSeen: [], metrics: { cpu: 0.1, memPct: 0.2, diskPct: 0.3 }, edge: null, keyVersion: 1, advertiseHost: '127.0.0.1', agentPort: 7080, ip: '127.0.0.1', hostname: 'host-app' };
    await rejects(app.api.heartbeat({ body: { ...hb, siteSlug: 'db' } }), 'invalid');
    const before = (await app.api.poll({ query: { wait: 0 } })).cursor;
    const r = await app.api.heartbeat({ body: hb });
    assert.equal(r.ok, true);
    assert.equal((await app.api.poll({ query: { wait: 0 } })).cursor, before, 'no epoch change');
    await app.api.heartbeat({ body: { ...hb, advertiseHost: '10.0.0.5' } });
    assert.equal(Number((await app.api.poll({ query: { wait: 0 } })).cursor), Number(before) + 1);
    const st = await admin.api.getStatus({ deploymentId: id });
    const s = st.sites.find((x) => x.siteSlug === 'app')!;
    assert.equal(s.online, true);
    assert.equal(s.enrolled, true);
    assert.deepEqual(s.metrics, { cpu: 0.1, memPct: 0.2, diskPct: 0.3 });
    const up = st.uptime.find((x) => x.siteSlug === 'app')!;
    assert.equal(up.days.length, 30);
    const today = up.days.at(-1)!;
    const h = new Date().getUTCHours();
    assert.ok(Math.abs(today.hours[h]! - 2 / 120) < 1e-9);
    assert.ok(today.onlineFraction > 0);
    assert.equal(up.days[0]!.onlineFraction, 0);
  });

  await t.test('activities pipe: contiguous, gap → invalid, skip existing, broken chain → 422', async () => {
    const ch = chain(id, 'app', 1, 3, null);
    const r = await app.api.uploadAgentActivities({ body: { siteSlug: 'app', entries: ch } });
    assert.deepEqual([r.ackSeq, r.inserted, r.skipped, r.rejected], [3, 3, 0, 0]);
    const again = await app.api.uploadAgentActivities({ body: { siteSlug: 'app', entries: ch } });
    assert.deepEqual([again.ackSeq, again.inserted, again.skipped], [3, 0, 3]);
    const gap = chain(id, 'app', 5, 6, 'sha256:' + '1'.repeat(64));
    const g = await rejects(app.api.uploadAgentActivities({ body: { siteSlug: 'app', entries: gap } }), 'invalid');
    assert.match(g.message, /missing seq 4/);
    await rejects(app.api.uploadAgentActivities({ body: { siteSlug: 'db', entries: [] } }), 'invalid');
    const next = chain(id, 'app', 4, 6, ch[2]!.hash);
    next[1] = { ...next[1]!, summary: 'tampered' };
    const br = await rejects(app.api.uploadAgentActivities({ body: { siteSlug: 'app', entries: next } }), 'invalid');
    assert.equal(br.details.inserted, 1);
    assert.equal(br.details.rejected, 2);
    assert.equal(br.details.ackSeq, 4);
    const list = await admin.api.listActivities({ deploymentId: id, query: { site: 'app', via: 'pipe' } });
    assert.equal(list.items.length, 4);
    assert.deepEqual(list.items.map((a) => a.seq), [4, 3, 2, 1]);
    assert.deepEqual(list.items[3]!.data, { n: 1, nested: [[1, 2], { a: null }] }, 'nested arrays round-trip');
    assert.equal(list.items[0]!.uploadedVia, 'pipe');
    const byActor = await admin.api.listActivities({ deploymentId: id, query: { actor: 'ops@example.com', site: 'app', type: 'actor.' } });
    assert.equal(byActor.items.length, 4);
    const pg = await admin.api.listActivities({ deploymentId: id, query: { limit: 2 } });
    assert.equal(pg.items.length, 2);
    const pg2 = await admin.api.listActivities({ deploymentId: id, query: { limit: 2, cursor: pg.cursor! } });
    assert.ok(!pg2.items.some((a) => pg.items.some((b) => b.activityId === a.activityId)));
    await rejects(admin.api.listActivities({ deploymentId: id, query: { origin: 'martian' as never } }), 'invalid');
  });

  await t.test('.opmlog manual upload: signature, chain, skip / insert / reject', async () => {
    const ch = chain(id, 'app', 1, 6, null);
    // seq 1..3 equal the piped ones? they differ (new ts) → use the stored ones for 1..4 and continue
    const stored = (await admin.api.listActivities({ deploymentId: id, query: { site: 'app' } })).items.sort((a, b) => a.seq - b.seq).map((a) => ({ ...a, uploadedVia: null, uploadedAt: null }));
    const more = chain(id, 'app', 5, 6, stored.at(-1)!.hash);
    const bundle = opmlog(id, { siteSlug: 'app', key: app.key }, [...stored, ...more]);
    const res = await admin.api.uploadActivities({ deploymentId: id, body: bundle });
    assert.deepEqual([res.inserted, res.skipped, res.rejected, res.signatureOk], [2, 4, 0, true]);
    assert.deepEqual(res.sites, [{ siteSlug: 'app', fromSeq: 1, toSeq: 6 }]);
    const tamper = more.map((a) => ({ ...a }));
    tamper[1]!.summary = 'changed';
    const res2 = await admin.api.uploadActivities({ deploymentId: id, body: opmlog(id, { siteSlug: 'app', key: app.key }, [...stored, ...tamper]) });
    assert.ok(res2.rejected >= 1);
    await rejects(admin.api.uploadActivities({ deploymentId: id, body: opmlog(id, { siteSlug: 'app', key: app.key }, stored, { tamperSig: true }) }), 'invalid');
    await rejects(admin.api.uploadActivities({ deploymentId: id, body: opmlog(id, { siteSlug: 'app', key: newSiteKey() }, stored) }), 'invalid');
    await rejects(admin.api.uploadActivities({ deploymentId: id, body: Buffer.from('not a tar file at all') }), 'invalid');
    const manual = await admin.api.listActivities({ deploymentId: id, query: { via: 'manual' } });
    assert.equal(manual.items.length, 2);
    const [files] = await abucket.getFiles({ prefix: `activity-bundles/${id}/` });
    assert.ok(files.length >= 2);
    void ch;
  });

  await t.test('relay: db relays app heartbeats; inner signature checked; relayVia set', async () => {
    const hb = { siteSlug: 'app', agentVersion: '0.1.0-test', headRevId: null, staging: null, peersSeen: [], metrics: { cpu: 0, memPct: 0, diskPct: 0 }, edge: null, keyVersion: 1, advertiseHost: '10.0.0.5', agentPort: 7080, ip: '127.0.0.1', hostname: 'host-app' };
    const body = Buffer.from(JSON.stringify(hb));
    const inner = { method: 'POST', path: '/v1/agent/heartbeat', headers: { ...signHeaders(app.key, id, 'app', 'POST', '/v1/agent/heartbeat', body), 'Content-Type': 'application/json' }, bodyB64: body.toString('base64') };
    const rr = await db.api.relay({ body: { forSite: 'app', request: inner } });
    assert.equal(rr.status, 200, Buffer.from(rr.bodyB64 ?? '', 'base64').toString());
    assert.equal(JSON.parse(Buffer.from(rr.bodyB64!, 'base64').toString()).ok, true);
    const st = await admin.api.getStatus({ deploymentId: id });
    assert.equal(st.sites.find((x) => x.siteSlug === 'app')!.relayVia, 'db');
    const replay = await db.api.relay({ body: { forSite: 'app', request: inner } });
    assert.equal(replay.status, 401, 'inner nonce replay');
    const forged = { ...inner, headers: { ...signHeaders(db.key, id, 'app', 'POST', '/v1/agent/heartbeat', body), 'Content-Type': 'application/json' } };
    assert.equal((await db.api.relay({ body: { forSite: 'app', request: forged } })).status, 401);
    await rejects(db.api.relay({ body: { forSite: 'db', request: inner } }), 'invalid');
    await rejects(db.api.relay({ body: { forSite: 'app', request: { ...inner, path: '/v1/me' } } }), 'invalid');
    // a relayed poll
    const pp = '/v1/agent/poll?wait=0';
    const pr = await db.api.relay({ body: { forSite: 'app', request: { method: 'GET', path: pp, headers: signHeaders(app.key, id, 'app', 'GET', pp, null), bodyB64: null } } });
    assert.equal(pr.status, 200);
    assert.equal(JSON.parse(Buffer.from(pr.bodyB64!, 'base64').toString()).deploymentId, id);
    // direct call clears relayVia
    await app.api.poll({ query: { wait: 0 } });
    const st2 = await admin.api.getStatus({ deploymentId: id });
    assert.equal(st2.sites.find((x) => x.siteSlug === 'app')!.relayVia, null);
  });

  await t.test('access key rotate → cloud view version 2; revoke → key stops working; deleted → poll says deleted', async () => {
    const before = await app.api.poll({ query: { wait: 0 } });
    const rot = await admin.api.rotateAccessKey({ deploymentId: id });
    assert.equal(rot.version, 2);
    assert.match(rot.accessKey, /^[A-Z2-7]{4}(-[A-Z2-7]{4}){5}$/);
    const after = await app.api.poll({ query: { wait: 0 } });
    assert.equal(after.accessKey.version, 2);
    assert.notEqual(after.accessKey.hash, before.accessKey.hash);
    assert.equal(Number(after.cursor), Number(before.cursor) + 1);
    await rejects(admin.api.revokeAgent({ deploymentId: id, siteSlug: 'app', body: { confirm: 'wrong' } }), 'invalid');
    await admin.api.revokeAgent({ deploymentId: id, siteSlug: 'app', body: { confirm: 'app' } });
    await rejects(app.api.poll({ query: { wait: 0 } }), 'unauthenticated', 401);
    await rejects(admin.api.revokeAgent({ deploymentId: id, siteSlug: 'app', body: { confirm: 'app' } }), 'not-found');
    // a bundle signed with the revoked key still verifies
    const stored = (await admin.api.listActivities({ deploymentId: id, query: { site: 'app' } })).items.sort((a, b) => a.seq - b.seq).map((a) => ({ ...a, uploadedVia: null, uploadedAt: null }));
    const res = await admin.api.uploadActivities({ deploymentId: id, body: opmlog(id, { siteSlug: 'app', key: app.key }, stored) });
    assert.equal(res.signatureOk, true);
    assert.equal(res.inserted, 0);
    // re-install with a new key
    const app2 = await enrollSite(admin, id, 'app');
    assert.equal((await app2.api.poll({ query: { wait: 0 } })).peers.length, 2);
    const d = await admin.api.getDeployment({ deploymentId: id });
    await admin.api.deleteDeployment({ deploymentId: id, body: { confirm: d.label } });
    const v = await db.api.poll({ query: { wait: 0 } });
    assert.equal(v.deleted, true);
    await rejects(db.api.heartbeat({ body: { siteSlug: 'db', agentVersion: '1', headRevId: null, staging: null, peersSeen: [], metrics: { cpu: 0, memPct: 0, diskPct: 0 }, edge: null, keyVersion: 2, advertiseHost: '127.0.0.1', agentPort: 7081, ip: '', hostname: '' } }), 'unauthenticated');
    void agentClient;
  });
});
