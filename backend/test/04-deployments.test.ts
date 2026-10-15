import { test } from 'node:test';
import assert from 'node:assert/strict';
import { MASK, type SecretDefInput } from '@opm/shared';
import { depInput, adb, HELLO, helloDeployment, person, rejects, seedHelloCatalog, uniq, publish } from './helpers.ts';
import { verifyAccessKey } from '../functions/src/core/accesskey.ts';

test('templates (§21 #105): create v1, details PATCH (ifVersion), config only by a commit; save-level errors → 422 nothing saved', async () => {
  const admin = await person('admin');
  const api = admin.real;
  await seedHelloCatalog();
  const t = await api.createTemplate({ body: { label: 'Hello', icon: 'box', description: 'd' } });
  assert.equal(t.version, 1);
  assert.equal(Object.keys(await api.getTemplate({ templateId: t.templateId })).some((k) => k.startsWith('_')), false, 'no internal fields');
  assert.deepEqual([t.variables, t.secrets, t.files, t.sites], [[], [], [], []]);
  const h = HELLO();
  await rejects(api.patchTemplate({ templateId: t.templateId, body: { ifVersion: 7, label: 'x' } }), 'conflict');
  await rejects(api.patchTemplate({ templateId: t.templateId, body: { ifVersion: 1, variables: h.config.variables } }), 'gone', 410);
  const base = t.branch!.head!.commitId;
  const withValue = await rejects(api.commitTemplate({ templateId: t.templateId, body: { message: 'v', baseCommitId: base, config: { ...h.config, secrets: h.config.secrets.map(({ value, rotatedAt: _r, ...s }) => (void _r, { ...s, plainValue: value })) } } }), 'invalid', 422);
  assert.match(JSON.stringify(withValue.details), /V_TEMPLATE_SECRET_VALUE/);
  assert.equal((await api.getTemplate({ templateId: t.templateId })).version, 1, 'nothing saved');
  // §21 #107: a commit's new blobs must be stored (or staged) — the banner File content
  await api.uploadBlob({ body: new TextEncoder().encode(h.banner) });
  const secrets = h.config.secrets.map(({ value: _v, rotatedAt: _r, ...s }) => (void _v, void _r, s));
  const c = await api.commitTemplate({ templateId: t.templateId, body: { message: 'hello config', baseCommitId: base, config: { variables: h.config.variables, secrets, files: h.config.files, sites: h.config.sites } } });
  assert.equal(c.seq, 2);
  const t2 = await api.getTemplate({ templateId: t.templateId });
  assert.equal(t2.version, 2, 'a commit that changes the config moves the version');
  assert.equal(t2.branch!.head!.commitId, c.commitId);
  const s = await api.listTemplates({});
  const item = s.items.find((x) => x.templateId === t.templateId)!;
  assert.deepEqual(item.counts, { variables: 5, secrets: 1, files: 1, sites: 2 });
  assert.deepEqual(item.siteSlugs, ['db', 'app']);
  // a save-level error: unknown service
  const bad = structuredClone(h.config.sites);
  bad[0]!.config.containers[0]!.serviceSlug = 'no-such-service';
  await rejects(api.commitTemplate({ templateId: t.templateId, body: { message: 'bad', baseCommitId: c.commitId, config: { variables: h.config.variables, secrets, files: h.config.files, sites: bad } } }), 'invalid');
  await api.deleteTemplate({ templateId: t.templateId });
  await rejects(api.getTemplate({ templateId: t.templateId }), 'not-found');
});

test('deployments: create from template generates secrets + access key; view masks secrets; delete is soft', async () => {
  const admin = await person('admin');
  const { dep, accessKey } = await helloDeployment(admin);
  assert.match(accessKey, /^[A-Z2-7]{4}(-[A-Z2-7]{4}){5}$/);
  const stored = (await adb.collection('deployments').doc(dep.deploymentId).get()).data()!;
  assert.ok(await verifyAccessKey(accessKey, stored.accessKeyHash), 'PHC argon2id hash verifies');
  assert.match(stored.accessKeyHash, /^\$argon2id\$v=19\$m=65536,t=3,p=1\$[A-Za-z0-9+/]+\$[A-Za-z0-9+/]+$/);
  assert.equal(dep.draftVersion, 0);
  assert.equal(dep.agentEpoch, 2, 'the create commit wrote a compat proposal (agentEpoch +1)');
  assert.equal(dep.head, null);
  // §21 #104 the create commit + origin link (srcTemplate is frozen legacy data)
  assert.equal(dep.branch!.head!.seq, 1);
  assert.equal(dep.branch!.head!.kind, 'create');
  assert.equal(dep.branch!.origin!.branch.kind, 'template');
  assert.equal(dep.draft.secrets[0]!.value, MASK);
  assert.equal(dep.draft.secrets[0]!.isSet, true);
  assert.equal((dep as unknown as Record<string, unknown>).accessKeyHash, undefined);
  assert.ok(dep.srcTemplate?.label.length);
  const rev = await admin.api.revealSecret({ deploymentId: dep.deploymentId, secretSlug: 'api-token' });
  assert.match(rev.value, /^[A-Za-z0-9]{24}$/);
  const acts = await admin.api.listActivities({ deploymentId: dep.deploymentId, query: { site: '_cloud' } });
  assert.ok(acts.items.some((a) => a.type === 'secret.revealed' && (a.data as { where: string }).where === 'cloud'));
  assert.ok(acts.items.some((a) => a.type === 'deployment.created'));
  // patch
  const p = await admin.api.patchDeployment({ deploymentId: dep.deploymentId, body: { label: 'Renamed', image: 'https://x.example/logo.png' } });
  assert.equal(p.label, 'Renamed');
  await rejects(admin.api.patchDeployment({ deploymentId: dep.deploymentId, body: { tagSlugs: ['no-such-tag'] } }), 'invalid');
  await rejects(admin.api.patchDeployment({ deploymentId: dep.deploymentId, body: { managerAccountUids: ['nobody'] } }), 'invalid');
  const list = await admin.api.listDeployments({ query: { limit: 500 } });
  const sum = list.items.find((x) => x.deploymentId === dep.deploymentId)!;
  assert.deepEqual(sum.siteSlugs, ['db', 'app']);
  assert.equal(sum.sitesTotal, 2);
  assert.equal(sum.sitesOnline, 0);
  await rejects(admin.api.deleteDeployment({ deploymentId: dep.deploymentId, body: { confirm: 'wrong' } }), 'invalid');
  assert.deepEqual(await admin.api.deleteDeployment({ deploymentId: dep.deploymentId, body: { confirm: 'Renamed' } }), { deleted: true });
  await rejects(admin.api.getDeployment({ deploymentId: dep.deploymentId }), 'not-found', 404);
  const after = (await adb.collection('deployments').doc(dep.deploymentId).get()).data()!;
  assert.equal(after.deleted, true);
  assert.equal(after.agentEpoch, 3);
  assert.ok(!(await admin.api.listDeployments({ query: { limit: 500 } })).items.some((x) => x.deploymentId === dep.deploymentId));
});

test('deployment access: create needs deployments.write covering tags; list filtered; managers see their deployment', async () => {
  const admin = await person('admin');
  const tag = uniq('tg');
  await admin.api.createTag({ body: { tagSlug: tag, label: 'T', icon: 'tag', color: '#123456' } });
  const tagWriter = await person([{ resource: 'deployments', actions: ['read', 'write'], scope: { kind: 'tags', tagSlugs: [tag] } }]);
  await rejects(tagWriter.api.createDeployment({ body: { label: 'no tag', templateId: null, tagSlugs: [], managerAccountUids: [] } }), 'forbidden');
  const d = await tagWriter.api.createDeployment({ body: { label: 'tagged', templateId: null, tagSlugs: [tag], managerAccountUids: [] } });
  const other = await admin.api.createDeployment({ body: { label: 'other', templateId: null, tagSlugs: [], managerAccountUids: [] } });
  const mine = await tagWriter.api.listDeployments({ query: { limit: 500 } });
  assert.ok(mine.items.some((x) => x.deploymentId === d.deployment.deploymentId));
  assert.ok(!mine.items.some((x) => x.deploymentId === other.deployment.deploymentId));
  await rejects(tagWriter.api.getDeployment({ deploymentId: other.deployment.deploymentId }), 'not-found', 404);
  const filtered = await admin.api.listDeployments({ query: { tag } });
  assert.deepEqual(filtered.items.map((x) => x.deploymentId), [d.deployment.deploymentId]);
  // manager
  const mgr = await person([]);
  await admin.api.patchDeployment({ deploymentId: other.deployment.deploymentId, body: { managerAccountUids: [mgr.uid] } });
  assert.equal((await mgr.api.getDeployment({ deploymentId: other.deployment.deploymentId })).label, 'other');
  assert.ok((await mgr.api.getDiff({ deploymentId: other.deployment.deploymentId })).blockers);
  await rejects(mgr.api.patchDeployment({ deploymentId: other.deployment.deploymentId, body: { label: 'x' } }), 'forbidden');
  // deployment-scoped reader sees it but cannot write config
  const cfgReader = await person([{ resource: 'deployment.config', actions: ['read'], scope: { kind: 'deployments', deploymentIds: [other.deployment.deploymentId] } }]);
  assert.equal((await cfgReader.api.getDeployment({ deploymentId: other.deployment.deploymentId })).deploymentId, other.deployment.deploymentId);
  await rejects(cfgReader.api.putDraftSection({ deploymentId: other.deployment.deploymentId, section: 'variables', body: { ifRevision: 0, items: [] } }), 'forbidden');
  await rejects(cfgReader.api.revealSecret({ deploymentId: other.deployment.deploymentId, secretSlug: 'x' }), 'forbidden');
  await rejects(cfgReader.api.getSetup({ deploymentId: other.deployment.deploymentId }), 'forbidden');
  // paging
  const p1 = await admin.api.listDeployments({ query: { limit: 1 } });
  assert.equal(p1.items.length, 1);
  assert.ok(p1.cursor);
  const p2 = await admin.api.listDeployments({ query: { limit: 1, cursor: p1.cursor! } });
  assert.notEqual(p2.items[0]?.deploymentId, p1.items[0]!.deploymentId);
});

test('drafts are gone (§21 #105): section / site writes 410; a commit validates like the old save + publish', async () => {
  const admin = await person('admin');
  const api = admin.real;
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const h = HELLO();
  const d = await api.getDeployment({ deploymentId: id });
  await rejects(api.putDraftSection({ deploymentId: id, section: 'variables', body: { ifRevision: d.draftVersion, items: h.config.variables } }), 'gone', 410);
  await rejects(api.putDraftSite({ deploymentId: id, siteSlug: 'app', body: { ifRevision: d.draftVersion, site: h.config.sites[1]! } }), 'gone', 410);
  // a save-level error (enum) → 422, nothing saved; a site whose slug mismatches is just another config (validated)
  const cfg = await depInput(api, id);
  cfg.variables[0]!.value = 'purple';
  await rejects(api.commitDeployment({ deploymentId: id, body: { message: 'bad', baseCommitId: d.branch!.head!.commitId, config: cfg } }), 'invalid', 422);
  assert.equal((await api.getDeployment({ deploymentId: id })).branch!.head!.commitId, d.branch!.head!.commitId, 'nothing saved on 422');
  // a site label edit commits
  const ok = await depInput(api, id);
  ok.sites.find((s) => s.siteSlug === 'app')!.label = 'App site!';
  await api.commitDeployment({ deploymentId: id, body: { message: 'site label', baseCommitId: d.branch!.head!.commitId, config: ok } });
  assert.equal((await api.getDeployment({ deploymentId: id })).draft.sites.find((s) => s.siteSlug === 'app')!.label, 'App site!');
});

test('§21 #104 diff (working copy vs head commit) + commit + compat proposal + supersede + discard + revisions', async () => {
  const admin = await person('admin');
  const { dep } = await helloDeployment(admin);
  const id = dep.deploymentId;
  // the create commit (seq 1) is the head; §21 #105 getDiff = what the head changed (the create commit: everything)
  const d0 = await admin.api.getDiff({ deploymentId: id });
  assert.equal(d0.head!.seq, 1);
  assert.equal(d0.publishable, true, JSON.stringify(d0.blockers));
  assert.ok(!d0.blockers.some((b) => b.code === 'V_NO_CHANGE'));
  assert.ok(d0.changes.length > 0);
  assert.deepEqual(d0.pending, []);
  // the create commit wrote the compat proposal seq 1 (no site enrolled yet → old agents possible)
  const props0 = await admin.api.listProposals({ deploymentId: id });
  assert.equal(props0.items.length, 1);
  assert.equal(props0.items[0]!.status, 'published');
  assert.deepEqual(Object.keys(props0.items[0]!.sites).sort(), ['app', 'db']);
  await rejects(admin.api.publish({ deploymentId: id, body: { ifRevision: 0, summary: 'x' } }), 'gone', 410);
  await rejects(admin.api.resetDraftToHead({ deploymentId: id, body: { confirm: true } }), 'gone', 410);
  // edit → uncommitted → commit
  const vars = HELLO().config.variables.map((v) => (v.variableSlug === 'theme' ? { ...v, value: 'yellow' } : v));
  const cy = await depInput(admin.real, id);
  cy.variables = vars;
  const h1 = (await admin.real.getDeployment({ deploymentId: id })).branch!.head!.commitId;
  assert.equal('uncommitted' in (await admin.api.getDeployment({ deploymentId: id })).branch!, false, '§21 #105 no server-side count');
  await rejects(admin.real.commitDeployment({ deploymentId: id, body: { message: '  ', baseCommitId: h1, config: cy } }), 'invalid');
  const c2 = await admin.real.commitDeployment({ deploymentId: id, body: { message: 'theme yellow', baseCommitId: h1, config: cy } });
  const p2 = await publish(admin.api, id, { ifRevision: 0, summary: 'theme yellow' });
  assert.equal(p2.commit.commitId, c2.commitId);
  assert.equal(p2.commit.seq, 2);
  assert.equal(p2.commit.kind, 'change');
  assert.equal(p2.commit.message, 'theme yellow');
  assert.equal(p2.proposal.revId, p2.commit.commitId);
  assert.equal(p2.revision.seq, 1, 'compat revisions count the legacy chain (no head reported yet → seq 1)');
  const props = await admin.api.listProposals({ deploymentId: id });
  const old = props.items.find((p) => p.revId === props0.items[0]!.revId)!;
  assert.equal(old.status, 'superseded');
  assert.equal(old.supersededBy, p2.commit.commitId);
  const rv = await admin.api.getRevision({ deploymentId: id, revId: p2.commit.commitId });
  assert.equal(rv.config.secrets[0]!.value, MASK);
  assert.ok(rv.catalog.images['redis:v1']);
  assert.equal(rv.hash, p2.commit.hash);
  // running update: the commit is still written; no compat proposal while an old site applies
  await adb.collection('deployments').doc(id).collection('proposals').doc(p2.commit.commitId).update({ status: 'applying' });
  const v3 = HELLO().config.variables.map((v) => (v.variableSlug === 'theme' ? { ...v, value: 'blue' } : v));
  const w3 = (await admin.api.putDraftSection({ deploymentId: id, section: 'variables', body: { ifRevision: (await admin.api.getDeployment({ deploymentId: id })).draftVersion, items: v3 } })) as { draftVersion: number };
  const c3 = await admin.api.commitDeployment({ deploymentId: id, body: { message: 'blue', ifRevision: w3.draftVersion } });
  assert.equal(c3.data.compat ?? null, null);
  await adb.collection('deployments').doc(id).collection('proposals').doc(p2.commit.commitId).update({ status: 'ready' });
  // §21 #105 the discard endpoint is gone (pending changes are discarded in the browser)
  await rejects(admin.real.discardDeploymentWorking({ deploymentId: id, body: {} }), 'gone', 410);
  const after = await admin.api.getDeployment({ deploymentId: id });
  assert.equal(after.draft.variables.find((v) => v.variableSlug === 'theme')!.value, 'blue');
  // blockers: a required variable without a value cannot be committed
  const vars5 = HELLO().config.variables.map((v) => (v.variableSlug === 'theme' ? { ...v, value: null } : v));
  const w5 = await admin.api.putDraftSection({ deploymentId: id, section: 'variables', body: { ifRevision: after.draftVersion, items: vars5 } });
  const e = await rejects(admin.api.commitDeployment({ deploymentId: id, body: { message: 'bad', ifRevision: w5.draftVersion } }), 'invalid');
  assert.ok((e.details.errors as { code: string }[]).some((x) => x.code === 'V_VAR_REQUIRED'));
  // image not ready blocks the commit
  await adb.collection('services').doc('redis').collection('images').doc('v1').update({ 'import.status': 'copying' });
  try {
    const d6 = await admin.api.getDiff({ deploymentId: id });
    assert.ok(d6.blockers.some((b) => b.code === 'V_IMAGE_NOT_READY'));
  } finally {
    await adb.collection('services').doc('redis').collection('images').doc('v1').update({ 'import.status': 'ready' });
  }
  // commits paging
  const cs = await admin.api.listDeploymentCommits({ deploymentId: id, query: { limit: 1 } });
  assert.equal(cs.items[0]!.seq, 3);
  assert.ok(cs.cursor);
  const r1 = await admin.api.listRevisions({ deploymentId: id, query: { limit: 1 } });
  assert.ok(r1.items.length === 1);
});
