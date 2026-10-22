/**
 * Root audit log (SPEC-final §6.2) + deployment activities guarantee:
 *   every human non-GET CLOUD_API op writes an audit entry (root scope) or a "_cloud" activity (deployment scope);
 *   the only exemption is putUploadPart. A new route that logs nothing fails here.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { sign } from 'node:crypto';
import { activityHashInput, AUDIT_DEPLOYMENT_OPS, AUDIT_EXEMPT_OPS, canonicalize, CLOUD_API, createCloudClient, type Activity, type AuditLogEntry, type EndpointDef } from '@opm/shared';
import { abucket, adb, ADAPTER, BASE, enrollSite, helloDeployment, person, raw, rejects, sha256Hex, sleep, uniq, type Person, type SiteKey } from './helpers.ts';
import { depInput } from './helpers.ts';
import { writeTar } from '../functions/src/core/tar.ts';
import { runDaily } from '../functions/src/jobs/daily.ts';

const DEP_SCOPE = /^\/v1\/deployments\/\{deploymentId\}\/.+/;
const ops = Object.entries(CLOUD_API as unknown as Record<string, EndpointDef>).filter(([, d]) => d.auth === 'human' && d.method !== 'GET');

async function auditByRequest(requestId: string): Promise<AuditLogEntry[]> {
  const q = await adb.collection('auditLog').where('requestId', '==', requestId).get();
  return q.docs.map((d) => {
    const x = d.data();
    return { ...x, changes: JSON.parse(x._changes), data: JSON.parse(x._data) } as unknown as AuditLogEntry;
  });
}
async function cloudSeq(deploymentId: string): Promise<number> {
  return ((await adb.collection('deployments').doc(deploymentId).get()).data()!.cloudActivitySeq as number) ?? 0;
}
async function lastCloud(deploymentId: string): Promise<Activity> {
  const seq = await cloudSeq(deploymentId);
  const d = (await adb.collection('deployments').doc(deploymentId).collection('activities').doc(`_cloud:${seq}`).get()).data()!;
  return { ...d, data: JSON.parse(d._data) } as unknown as Activity;
}
function fill(path: string, params: Record<string, string>) {
  return path.replace(/\{([A-Za-z0-9_]+)\}/g, (_, k: string) => encodeURIComponent(params[k] ?? `zz-${k}`));
}
async function call(p: Person, def: EndpointDef, params: Record<string, string>, body?: unknown, rawBody?: Buffer) {
  const headers: Record<string, string> = { Authorization: p.bearer, 'User-Agent': 'opm-audit-test/1' };
  let b: Buffer | string | undefined;
  if (def.body === 'json') {
    headers['Content-Type'] = 'application/json';
    b = JSON.stringify(body ?? {});
  } else if (def.body === 'raw') {
    headers['Content-Type'] = 'application/octet-stream';
    b = rawBody ?? Buffer.alloc(0);
  }
  const r = await raw(def.method, fill(def.path, params), { headers, body: b });
  return { ...r, requestId: r.headers.get('x-request-id')! };
}

function makeActivity(deploymentId: string, siteSlug: string, seq: number, prevHash: string | null): Activity {
  const base = { activityId: `${siteSlug}:${seq}`, deploymentId, siteSlug, seq, ts: new Date(Date.now() + seq).toISOString(), actor: 'ops@example.com', origin: 'agent' as const, type: 'actor.declared', summary: `entry ${seq}`, data: { n: seq }, prevHash };
  return { ...base, hash: 'sha256:' + sha256Hex(activityHashInput(base)), uploadedVia: null, uploadedAt: null };
}
function opmlog(deploymentId: string, signer: { siteSlug: string; key: SiteKey }, entries: Activity[]) {
  const nd = Buffer.from(entries.map((e) => canonicalize(e) + '\n').join(''), 'utf8');
  const manifest = { format: 'opmlog/1', deploymentId, createdAt: new Date().toISOString(), createdBy: 'ops@example.com', signedBy: { siteSlug: signer.siteSlug, publicKey: signer.key.publicKey }, sites: [{ siteSlug: signer.siteSlug, fromSeq: 1, toSeq: entries.length, count: entries.length }], activities: { path: 'activities.ndjson', sizeBytes: nd.length, sha256: 'sha256:' + sha256Hex(nd) }, debug: [] };
  const mb = Buffer.from(canonicalize(manifest), 'utf8');
  return writeTar([
    { name: 'manifest.json', data: mb },
    { name: 'activities.ndjson', data: nd },
    { name: 'signature.sig', data: Buffer.from(sign(null, mb, signer.key.priv).toString('base64url'), 'ascii') },
  ]);
}

// §21 #104 + #105: removed routes answer 410 gone (kept in the contract for old clients)
// §21 #117: the cloud-copy writes of persist volumes are gone too (content is committed)
// §21 #118 openRemoteGui needs the single-process cloud (503 elsewhere)
const NO_ACTIVITY = new Set(['fetchFileToCloud', ...(ADAPTER === 'local' ? [] : ['openRemoteGui'])]);
const GONE = new Set(['publish', 'resetDraftToHead', 'applyTemplateSync', 'unlinkTemplate', 'putDraftSection', 'putDraftSite', 'regenerateSecret', 'discardDeploymentWorking', 'createVolumeContentVersion', 'restoreVolumeContentVersion', 'requestVolumeSnapshot', 'sendVolumeContent', 'cancelVolumeContentPush']);
test('guarantee: every human non-GET op writes an audit entry (root) or a _cloud activity (deployment scope)', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  // §21 #101: the app volume is persisted to the cloud (published by the publish recipe, head via resetDraftToHead)
  const v0 = await admin.api.getDeployment({ deploymentId: id });
  const appSite0 = structuredClone(v0.draft.sites.find((s) => s.siteSlug === 'app')!);
  appSite0.config.volumes[0]!.persist = { exclude: [], maxBytes: 50 * 1024 * 1024, auto: true };
  await admin.api.putDraftSite({ deploymentId: id, siteSlug: 'app', body: { ifRevision: v0.draftVersion, site: appSite0 } });
  // §21 #104: commit it (the release) and point the legacy head at its compat revision (volume content needs a head)
  const c0 = await admin.api.commitDeployment({ deploymentId: id, body: { message: 'persist the app volume', ifRevision: (await admin.api.getDeployment({ deploymentId: id })).draftVersion } });
  await adb.collection('deployments').doc(id).update({ head: { revId: c0.data.compat!.revId, seq: c0.data.compat!.seq, hash: c0.hash, reportedAt: new Date().toISOString() } });
  const view = await admin.api.getDeployment({ deploymentId: id });
  const db = await enrollSite(admin, id, 'db');
  const app = await enrollSite(admin, id, 'app');
  const secretSlug = view.draft.secrets.find((s) => s.isSet)!.secretSlug;
  const dv = async () => (await admin.api.getDeployment({ deploymentId: id })).draftVersion;
  let transferId = '';
  let fileId = '';
  let tokenHash = '';
  // deployment-scope recipes (must succeed and must write a _cloud activity)
  const recipes: Record<string, () => Promise<{ params?: Record<string, string>; body?: unknown; raw?: Buffer }>> = {
    revealSecret: async () => ({ params: { secretSlug } }),
    revealAccessKey: async () => ({}),
    revealInstallCommand: async () => {
      await adb.collection('deployments').doc(id).collection('agents').doc('app').delete();
      const t = await admin.api.createInstallToken({ deploymentId: id, body: { siteSlug: 'app', replaceActive: true } });
      return { params: { tokenHash: t.tokenHash } };
    },
    // §21 #92 the enrolled site db's upgrade command (its install token keeps its plaintext)
    revealUpgradeCommand: async () => ({ params: { siteSlug: 'db' } }),
    regenerateUpgradeCommand: async () => ({ params: { siteSlug: 'db' } }),
    // §21 #104 commit = the release (+ the compat proposal; the legacy head points at it for the volume-content recipes)
    commitDeployment: async () => {
      const config = await depInput(admin.real, id);
      config.variables[0]!.description = 'audited';
      return { body: { message: 'audit guarantee', baseCommitId: (await admin.real.getDeployment({ deploymentId: id })).branch!.head!.commitId, config } };
    },
    rotateAccessKey: async () => ({}),
    createInstallToken: async () => {
      await adb.collection('deployments').doc(id).collection('agents').doc('app').delete(); // free the slug for a token
      return { body: { siteSlug: 'app' } };
    },
    revokeInstallToken: async () => {
      const t = await admin.api.createInstallToken({ deploymentId: id, body: { siteSlug: 'app' } });
      tokenHash = t.tokenHash;
      return { params: { tokenHash } };
    },
    // §21 #90 (pin 0.10.5-audit via deps/manifest.json; site db on 0.10.0)
    requestAgentUpgrade: async () => {
      await abucket.file('deps/manifest.json').save(JSON.stringify({ docker: { version: '0.0.1-a', files: { amd64: { path: 'x', sha256: 'e'.repeat(64), size: 1 }, arm64: { path: 'y', sha256: 'f'.repeat(64), size: 1 } } }, minDockerApi: '1.41', agent: { version: '0.10.5-audit', notes: null }, updatedAt: new Date().toISOString() }));
      await adb.collection('deployments').doc(id).collection('agents').doc('db').update({ agentVersion: '0.10.0' });
      return { params: { siteSlug: 'db' }, body: { version: '0.10.5-audit', confirm: 'db' } };
    },
    cancelAgentUpgrade: async () => ({ params: { siteSlug: 'db' } }),
    upgradeAllAgents: async () => ({ body: { version: '0.10.5-audit', confirm: 'UPGRADE' } }),
    cancelAgentUpgradeRun: async () => ({}),
    revokeAgent: async () => {
      await abucket.file('deps/manifest.json').delete().catch(() => undefined);
      return { params: { siteSlug: 'db' }, body: { confirm: 'db' } };
    },
    uploadActivities: async () => {
      const a1 = makeActivity(id, 'db', 1, null);
      const a2 = makeActivity(id, 'db', 2, a1.hash);
      return { raw: opmlog(id, { siteSlug: 'db', key: db.key }, [a1, a2]) };
    },
    createTransfer: async () => ({ params: { siteSlug: 'app' }, body: { fileName: 'notes.txt', sizeBytes: 5, sha256: 'sha256:' + sha256Hex('hello') } }),
    deleteTransfer: async () => ({ params: { transferId } }),
    // §21 #123 shared files
    openRemoteGui: async () => ({ params: { siteSlug: 'app' } }), // §21 #118 remote-gui.opened
    publishFile: async () => ({ body: { fileName: 'shared.txt', sizeBytes: 5, sha256: 'sha256:' + sha256Hex('hello') } }),
    unpublishFile: async () => ({ params: { fileId } }),
  };
  void app;
  const checked: string[] = [];
  for (const [op, def] of ops) {
    if ((AUDIT_EXEMPT_OPS as readonly string[]).includes(op)) continue;
    // §21 #104: removed routes answer 410 gone (kept in the contract for old clients)
    if (GONE.has(op)) continue;
    // §21 #123 asking a holder for a copy writes no activity (the arrival does: file.fetched)
    if (NO_ACTIVITY.has(op)) continue;
    if (DEP_SCOPE.test(def.path)) {
      const recipe = recipes[op];
      assert.ok(recipe, `deployment-scope op ${op} has no recipe in 09-audit.test.ts: add one (and make the op write a _cloud activity)`);
      const r0 = await recipe();
      const before = await cloudSeq(id);
      const res = await call(admin, def, { deploymentId: id, ...(r0.params ?? {}) }, r0.body, r0.raw);
      assert.ok(res.status < 300, `${op} must succeed for the guarantee test: ${res.status} ${res.body.toString().slice(0, 300)}`);
      if (op === 'createTransfer') transferId = res.json.transfer.transferId;
      if (op === 'publishFile') fileId = res.json.file.fileId;
      const after = await cloudSeq(id);
      assert.ok(after > before, `${op} wrote no _cloud activity`);
      const audited = (AUDIT_DEPLOYMENT_OPS as readonly string[]).includes(op);
      const entries = await auditByRequest(res.requestId);
      assert.equal(entries.length, audited ? 1 : 0, audited ? `${op} is a deployment credential op: exactly one root audit entry` : `${op} is deployment-internal: no root audit entry`);
      if (audited) assert.equal(entries[0]!.target, `deployments/${id}`);
    } else {
      // root scope: any outcome (dummy ids / empty bodies) must produce exactly one entry with this request id
      const res = await call(admin, def, {}, {});
      const entries = await auditByRequest(res.requestId);
      assert.equal(entries.length, 1, `${op} (${res.status}) wrote ${entries.length} audit entries`);
      const e = entries[0]!;
      assert.equal(e.op, op);
      assert.equal(e.actor, admin.uid);
      assert.equal(e.actorEmail, admin.email);
      assert.equal(e.status, res.status);
      assert.equal(e.outcome, res.status < 400 ? 'ok' : 'error');
      if (res.status >= 400) assert.equal(e.errorCode, res.json.error.code);
      assert.equal(e.userAgent, 'opm-audit-test/1');
      assert.ok(e.ip !== undefined);
    }
    checked.push(op);
  }
  // exempt: per-part upload PUTs write nothing
  const part = await call(admin, CLOUD_API.putUploadPart as EndpointDef, {}, undefined, Buffer.from('x'));
  assert.equal((await auditByRequest(part.requestId)).length, 0);
  assert.ok(checked.length >= 40, `checked ${checked.length} ops`);
  // the root deployment ops are audited in addition to their activities
  for (const op of ['patchDeployment', 'deleteDeployment'] as const) assert.ok(checked.includes(op));
  const types = new Set((await adb.collection('deployments').doc(id).collection('activities').where('siteSlug', '==', '_cloud').get()).docs.map((d) => d.data().type as string));
  for (const t of ['transfer.sent', 'transfer.deleted', 'activities.uploaded', 'secret.revealed', 'proposal.published', 'branch.committed', 'access-key.rotated', 'install-token.created', 'install-token.revoked', 'agent.revoked', 'access-key.revealed', 'install-token.revealed', 'agent.upgrade-requested', 'agent.upgrade-cancelled', 'file.published', 'file.unpublished'])
    assert.ok(types.has(t), `missing _cloud activity ${t}`);
});

test('§21 #105 a commit records secret value changes by slug (never values); deployment delete writes deployment.deleted + audit', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const v = await admin.real.getDeployment({ deploymentId: id });
  const config = await depInput(admin.real, id);
  config.secrets = config.secrets.map((s) => ({ ...s, plainValue: 'PLAIN-VALUE-Q9' }));
  await admin.real.commitDeployment({ deploymentId: id, body: { message: 'set the token', baseCommitId: v.branch!.head!.commitId, config } });
  const a = await lastCloud(id);
  assert.equal(a.type, 'branch.committed');
  assert.deepEqual((a.data as Record<string, unknown>).secretValues, { set: config.secrets.map((s) => s.secretSlug), generated: [], cleared: [] });
  assert.ok(!JSON.stringify(a).includes('PLAIN-VALUE-Q9'));
  await admin.api.deleteDeployment({ deploymentId: id, body: { confirm: v.label } });
  assert.equal((await lastCloud(id)).type, 'deployment.deleted');
  const log = await admin.api.listAuditLog({ query: { target: `deployments/${id}` } });
  assert.deepEqual(log.items.map((e) => e.action), ['deployment.deleted', 'deployment.committed', 'deployment.created']);
  assert.ok(log.items[0]!.changes.some((c) => c.path === 'deleted' && c.before === false && c.after === true));
  assert.ok(!JSON.stringify(log.items).includes('PLAIN-VALUE-Q9'));
});

test('redaction: no secret value is ever stored in the audit log', async () => {
  const admin = await person('admin');
  const pw1 = `REG-PW-${uniq()}`;
  const pw2 = `REG-PW2-${uniq()}`;
  const reg = await admin.api.createRegistry({ body: { label: 'Redact', url: 'https://127.0.0.1:1', username: 'u', password: pw1 } });
  await admin.api.patchRegistry({ registryId: reg.registryId, body: { password: pw2 } });
  await admin.api.revealRegistryPassword({ registryId: reg.registryId });
  const { dep, accessKey } = await helloDeployment(admin);
  const secretSlug = (await admin.api.getDeployment({ deploymentId: dep.deploymentId })).draft.secrets.find((s) => s.isSet)!.secretSlug;
  const generated = (await admin.api.revealSecret({ deploymentId: dep.deploymentId, secretSlug })).value;
  const acc = await admin.api.createAccount({ body: { name: 'Redact person', email: `${uniq('rd')}@opm.test` } });
  const reset = await admin.api.resetAccountPassword({ uid: acc.account.uid });
  const oob = new URL(reset.value).searchParams.get('oobCode') ?? reset.passwordSetLink;
  const all = (await adb.collection('auditLog').get()).docs.map((d) => JSON.stringify(d.data())).join('\n');
  for (const secret of [pw1, pw2, generated, accessKey, accessKey.replace(/-/g, ''), oob, acc.value]) assert.ok(!all.includes(secret), `secret leaked into auditLog: ${secret.slice(0, 6)}…`);
  assert.ok(!all.includes('$argon2id'), 'access key hash leaked');
  const patch = (await admin.api.listAuditLog({ query: { target: `registries/${reg.registryId}`, action: 'registry.updated' } })).items[0]!;
  assert.deepEqual(patch.changes.find((c) => c.path === 'password'), { path: 'password', before: '***', after: '***', secret: true });
  const created = (await admin.api.listAuditLog({ query: { target: `deployments/${dep.deploymentId}`, action: 'deployment.created' } })).items[0]!;
  assert.equal(created.changes.find((c) => c.path === 'accessKeyHash')?.after, '***');
  const reveal = (await admin.api.listAuditLog({ query: { target: `registries/${reg.registryId}`, action: 'registry.password-revealed' } })).items;
  assert.equal(reveal.length, 1);
  assert.equal(reveal[0]!.actor, admin.uid);
});

test('failure entries: 403 forbidden, 422 invalid, no account; 401 is not logged', async () => {
  const admin = await person('admin');
  const viewer = await person([]);
  const slug = uniq('t');
  await rejects(viewer.api.createTag({ body: { tagSlug: slug, label: 'X', icon: 'tag', color: '#112233' } }), 'forbidden', 403);
  const f = (await admin.api.listAuditLog({ query: { actor: viewer.uid } })).items;
  assert.equal(f.length, 1);
  assert.equal(f[0]!.action, 'tag.created');
  assert.equal(f[0]!.outcome, 'error');
  assert.equal(f[0]!.status, 403);
  assert.equal(f[0]!.errorCode, 'forbidden');
  assert.equal(f[0]!.target, `tags/${slug}`);
  assert.equal(f[0]!.actorEmail, viewer.email);
  assert.ok(f[0]!.requestId);
  const byEmail = (await admin.api.listAuditLog({ query: { actor: viewer.email.toUpperCase() } })).items;
  assert.equal(byEmail.length, 1);
  await rejects(admin.api.createTag({ body: { tagSlug: 'Bad Slug', label: '', icon: 'tag', color: 'red' } as never }), 'invalid', 422);
  const inv = (await admin.api.listAuditLog({ query: { actor: admin.uid, outcome: 'error' } })).items[0]!;
  assert.equal(inv.status, 422);
  assert.equal(inv.errorCode, 'invalid');
  const ghost = await person([], { noAccount: true });
  await rejects(ghost.api.createTemplate({ body: { label: 'x', icon: 'box' } }), 'forbidden', 403);
  const g = (await admin.api.listAuditLog({ query: { actor: ghost.uid } })).items;
  assert.equal(g.length, 1);
  assert.equal(g[0]!.errorMessage, 'no account');
  const r = await raw('POST', '/v1/tags', { headers: { Authorization: 'Bearer nope', 'Content-Type': 'application/json' }, body: '{}' });
  assert.equal(r.status, 401);
  assert.equal((await auditByRequest(r.headers.get('x-request-id')!)).length, 0);
});

test('template patch: before → after diff; account actions refined; filters', async () => {
  const admin = await person('admin');
  const t = await admin.api.createTemplate({ body: { label: 'Diff me', icon: 'box' } });
  // §21 #105: details by PATCH (template.updated), config by commits (template.committed)
  await admin.real.patchTemplate({ templateId: t.templateId, body: { ifVersion: 1, label: 'Diff me 2', description: 'new' } });
  const port = (value: number) => [{ variableSlug: 'port', label: 'Port', description: '', type: 'number' as const, value, required: false, options: null, validation: null }];
  const c1 = await admin.real.commitTemplate({ templateId: t.templateId, body: { message: 'port', baseCommitId: t.branch!.head!.commitId, config: { variables: port(8080), secrets: [], files: [], sites: [] } } });
  await admin.real.commitTemplate({ templateId: t.templateId, body: { message: 'port 9090', baseCommitId: c1.commitId, config: { variables: port(9090), secrets: [], files: [], sites: [] } } });
  const hist = await admin.api.listAuditLog({ query: { target: `templates/${t.templateId}` } });
  assert.deepEqual(hist.items.map((e) => e.action), ['template.committed', 'template.committed', 'template.updated', 'template.created']);
  const [, p2, p1, c] = hist.items as [AuditLogEntry, AuditLogEntry, AuditLogEntry, AuditLogEntry];
  const by = (e: AuditLogEntry) => Object.fromEntries(e.changes.map((x) => [x.path, x]));
  assert.deepEqual(by(p1)['label'], { path: 'label', before: 'Diff me', after: 'Diff me 2' });
  assert.deepEqual(by(p1)['version'], { path: 'version', before: 1, after: 2 });
  assert.ok(!by(p1)['updatedAt']);
  assert.equal(by(c)['label']!.after, 'Diff me');
  assert.equal(c.targetType, 'template');
  // accounts: refined actions
  const acc = await admin.api.createAccount({ body: { name: 'Refine', email: `${uniq('rf')}@opm.test` } });
  const uid = acc.account.uid;
  await admin.api.patchAccount({ uid, body: { name: 'Refined' } });
  await admin.api.patchAccount({ uid, body: { access: [{ resource: 'templates', actions: ['read'], scope: { kind: 'all' } }] } });
  await admin.api.disableAccount({ uid, body: { disabled: true } });
  await admin.api.disableAccount({ uid, body: { disabled: false } });
  await admin.api.resetAccountPassword({ uid });
  const acts = (await admin.api.listAuditLog({ query: { target: `accounts/${uid}` } })).items.map((e) => e.action);
  assert.deepEqual(acts, ['account.reset-link-created', 'account.enabled', 'account.disabled', 'account.access-changed', 'account.updated', 'account.created']);
  // filters
  const prefix = (await admin.api.listAuditLog({ query: { action: 'account.', target: `accounts/${uid}` } })).items;
  assert.equal(prefix.length, 6);
  const exact = (await admin.api.listAuditLog({ query: { action: 'account.disabled', actor: admin.uid } })).items;
  assert.ok(exact.length >= 1 && exact.every((e) => e.action === 'account.disabled'));
  const tt = (await admin.api.listAuditLog({ query: { targetType: 'template', actor: admin.uid } })).items;
  assert.equal(tt.length, 4);
  const ok = (await admin.api.listAuditLog({ query: { outcome: 'ok', target: `templates/${t.templateId}` } })).items;
  assert.equal(ok.length, 4);
  const from = p1.ts;
  const ranged = (await admin.api.listAuditLog({ query: { target: `templates/${t.templateId}`, from, to: p2.ts } })).items;
  assert.deepEqual(ranged.map((e) => e.auditId), [p2.auditId, p1.auditId]);
  const paged1 = await admin.api.listAuditLog({ query: { actor: admin.uid, limit: 4 } });
  const paged2 = await admin.api.listAuditLog({ query: { actor: admin.uid, limit: 4, cursor: paged1.cursor! } });
  assert.equal(paged1.items.length, 4);
  assert.ok(paged2.items.length >= 4);
  assert.ok(paged2.items[0]!.ts <= paged1.items[3]!.ts);
  for (const q of [{ outcome: 'maybe' }, { targetType: 'planet' }, { action: 'Bad Action' }, { from: 'yesterday' }]) await rejects(admin.api.listAuditLog({ query: q as never }), 'invalid', 422);
  const viewer = await person([]);
  await rejects(viewer.api.listAuditLog({}), 'forbidden', 403);
});

test('uploads: one entry at complete (part count), none per part; blobs audited', async () => {
  const admin = await person('admin');
  const data = Buffer.from(`audit-blob-${uniq()}`);
  const blob = await admin.api.uploadBlob({ body: data });
  const b = (await admin.api.listAuditLog({ query: { target: `blobs/${blob.sha256}` } })).items;
  assert.equal(b[0]!.action, 'blob.uploaded');
  assert.equal(b[0]!.data.sizeBytes, data.length);
  const up = await admin.api.createUpload({ body: { purpose: 'blob', sizeBytes: data.length, sha256: blob.sha256 } });
  await admin.api.putUploadPart({ uploadId: up.uploadId, index: '0', body: data });
  await admin.api.completeUpload({ uploadId: up.uploadId });
  const u = (await admin.api.listAuditLog({ query: { target: `uploads/${up.uploadId}` } })).items;
  assert.deepEqual(u.map((e) => e.action), ['upload.completed', 'upload.created']);
  assert.equal(u[0]!.data.partCount, 1);
  assert.ok(u[0]!.changes.some((c) => c.path === 'status' && c.before === 'open' && c.after === 'complete'));
});

test('system entries: image import failure (trigger) and the daily job', async () => {
  const admin = await person('admin');
  const reg = await admin.api.createRegistry({ body: { label: 'Dead', url: 'https://127.0.0.1:1', username: 'u', password: 'p' } });
  const svc = await admin.api.createService({ body: { label: 'Imp', serviceSlug: uniq('imp'), icon: 'lucide:box' } });
  await admin.api.registerImage({ serviceSlug: svc.serviceSlug, body: { version: 'v1', reference: '127.0.0.1:1/test/imp:v1', registryId: reg.registryId, mode: 'copy', kind: 'service' } });
  const target = `services/${svc.serviceSlug}/images/v1`;
  let entries: AuditLogEntry[] = [];
  for (let i = 0; i < 60 && !entries.some((e) => e.actor === 'system'); i++) {
    await sleep(1000);
    entries = (await admin.api.listAuditLog({ query: { target } })).items;
  }
  const sys = entries.find((e) => e.actor === 'system');
  assert.ok(sys, `no system entry for ${target}: ${JSON.stringify(entries.map((e) => e.action))}`);
  assert.equal(sys.action, 'image.import-failed');
  assert.equal(sys.op, 'system:importImage');
  assert.equal(sys.outcome, 'error');
  assert.ok(sys.changes.some((c) => c.path === 'import.status' && c.after === 'failed'));
  assert.ok(entries.some((e) => e.action === 'image.registered' && e.actor === admin.uid));
  const r = await runDaily();
  const d = (await admin.api.listAuditLog({ query: { actor: 'system', action: 'system.daily-cleanup' } })).items[0]!;
  assert.equal(d.op, 'system:daily');
  assert.equal(d.data.uptimeDeleted, r.uptimeDeleted);
  void createCloudClient;
  void BASE;
});
