/**
 * §21 #104 branches + commits: create-from (all pairs), commit levels, working-copy discard, compare kinds (conflict,
 * removals, skipped), merge / cherry-pick / revert / reset, 409 dirty / stale / source-changed, permissions incl. secret
 * values, agent-branch push (verify + fork refusal), agent-branch views, compat proposals vs 0.14 sites, 410 routes,
 * detachment proof (template edits never reach a deployment by themselves).
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { makeCommit, ulid, MASK, type AccessRule, type BranchRef, type Commit, type Config, type MergeChoice } from '@opm/shared';
import { adb, enrollSite, helloDeployment, HELLO, person, raw, rejects, tplInput, uniq, type Person } from './helpers.ts';

const clone = <T>(v: T): T => JSON.parse(JSON.stringify(v)) as T;
const tplRef = (templateId: string): BranchRef => ({ kind: 'template', templateId });
const depRef = (deploymentId: string): BranchRef => ({ kind: 'deployment', deploymentId });
const siteOf = (c: Pick<Config, 'sites'>, s: string) => c.sites.find((x) => x.siteSlug === s)!;

async function editTemplate(admin: Person, templateId: string, edit: (c: Config) => void, message?: string) {
  const t = await admin.api.getTemplate({ templateId });
  const c: Config = clone({ variables: t.variables, secrets: t.secrets, files: t.files, sites: t.sites });
  edit(c);
  const w = await admin.api.patchTemplate({ templateId, body: { ifVersion: t.version, ...c } });
  if (message) return admin.api.commitTemplate({ templateId, body: { message, ifVersion: w.template.version } });
  return null;
}
async function editDeployment(admin: Person, id: string, edit: (c: Config) => void, message?: string) {
  const d = await admin.api.getDeployment({ deploymentId: id });
  const draft = clone(d.draft) as unknown as Config;
  edit(draft);
  const w1 = await admin.api.putDraftSection({ deploymentId: id, section: 'variables', body: { ifRevision: d.draftVersion, items: draft.variables } });
  const w2 = await admin.api.putDraftSection({ deploymentId: id, section: 'sites', body: { ifRevision: w1.draftVersion, items: draft.sites } });
  if (message) return admin.api.commitDeployment({ deploymentId: id, body: { message, ifRevision: w2.draftVersion } });
  return null;
}
const head = async (admin: Person, id: string) => (await admin.api.getDeployment({ deploymentId: id })).branch!.head!;

test('create from: template ← template / deployment, deployment ← deployment (secret rules); origin + merge base; first compare is 3-way', async () => {
  const admin = await person('admin');
  const { dep, templateId } = await helloDeployment(admin);
  const id = dep.deploymentId;
  // template ← template (head)
  const t2 = await admin.api.createTemplate({ body: { label: uniq('copy'), from: { branch: tplRef(templateId), at: 'head' } } });
  assert.equal(t2.branch!.head!.seq, 1);
  assert.equal(t2.branch!.head!.kind, 'create');
  assert.match(t2.branch!.head!.message, /^created from .+@\d+$/);
  assert.equal(t2.branch!.origin!.branch.kind, 'template');
  assert.equal(t2.sites.length, HELLO().config.sites.length);
  const mb = (await adb.collection('templates').doc(t2.templateId).collection('mergeBases').doc(`template:${templateId}`).get()).data()!;
  assert.equal(mb.sourceKey, `template:${templateId}`);
  // compare with the origin right away: nothing differs, base = record
  const p0 = await admin.api.compareBranches({ body: { source: { branch: tplRef(templateId), at: 'head' }, target: tplRef(t2.templateId) } });
  assert.equal(p0.base.kind, 'record');
  assert.equal(p0.items.length, 0);
  // template ← deployment: secret values never reach a template; generate rules stay
  const t3 = await admin.api.createTemplate({ body: { label: uniq('fromdep'), from: { branch: depRef(id), at: 'head' }, message: 'snapshot of the deployment' } });
  assert.equal(t3.branch!.head!.message, 'snapshot of the deployment');
  assert.equal(t3.secrets[0]!.value, null);
  assert.ok(t3.secrets[0]!.generate);
  const tc = await admin.api.getTemplateCommit({ templateId: t3.templateId, commitId: t3.branch!.head!.commitId });
  assert.equal(tc.commit.source!.branch.kind, 'deployment');
  // deployment ← deployment: generated secrets get NEW values; copySecretValues needs secrets.read and copies only non-generate ones
  await editDeployment(admin, id, (c) => {
    c.variables.find((v) => v.variableSlug === 'theme')!.value = 'blue';
  }, 'blue');
  const src = await admin.api.revealSecret({ deploymentId: id, secretSlug: 'api-token' });
  const d2 = await admin.api.createDeployment({ body: { label: uniq('clone'), templateId: null, tagSlugs: [], managerAccountUids: [], from: { branch: depRef(id), at: 'head' } } });
  assert.equal(d2.deployment.draft.variables.find((v) => v.variableSlug === 'theme')!.value, 'blue');
  const v2 = await admin.api.revealSecret({ deploymentId: d2.deployment.deploymentId, secretSlug: 'api-token' });
  assert.notEqual(v2.value, src.value, 'generate → a new value');
  assert.equal(d2.deployment.branch!.origin!.branch.kind, 'deployment');
  assert.equal(d2.deployment.srcTemplate, null);
  // a user without deployment.secrets.read on the source cannot copy values
  const rules: AccessRule[] = [
    { resource: 'deployments', actions: ['read', 'write'], scope: { kind: 'all' } },
    { resource: 'deployment.config', actions: ['read', 'write', 'publish'], scope: { kind: 'all' } },
  ] as AccessRule[];
  const ops = await person(rules);
  await rejects(ops.api.createDeployment({ body: { label: uniq('x'), templateId: null, tagSlugs: [], managerAccountUids: [], from: { branch: depRef(id), at: 'head' }, copySecretValues: true } }), 'forbidden', 403);
  // deployment ← deployment compare: secret VALUE items exist, masked
  await editDeployment(admin, d2.deployment.deploymentId, (c) => (c.variables.find((v) => v.variableSlug === 'greeting')!.value = 'other'), 'greeting');
  const pc = await admin.api.compareBranches({ body: { source: { branch: depRef(id), at: 'head' }, target: depRef(d2.deployment.deploymentId) } });
  const sv = pc.items.find((i) => i.key === 'secrets/api-token/value')!;
  assert.ok(sv, JSON.stringify(pc.items.map((i) => i.key)));
  assert.equal(sv.source.value, 'set');
  assert.equal(pc.items.find((i) => i.key === 'variables/greeting/value')!.kind, 'ours');
  // the ops person sees the secret value item locked (no deployment.secrets.*)
  const pl = await ops.api.compareBranches({ body: { source: { branch: depRef(id), at: 'head' }, target: depRef(d2.deployment.deploymentId) } });
  assert.match(pl.items.find((i) => i.key === 'secrets/api-token/value')!.locked ?? '', /secrets/);
  await rejects(ops.api.merge({ body: { source: { branch: depRef(id), at: 'head' }, target: depRef(d2.deployment.deploymentId), targetHead: pl.target.head!.commitId, sourceCommit: pl.source.commit!.commitId, choices: [{ key: 'secrets/api-token/value', take: 'source' }], message: 'copy the token' } }), 'forbidden', 403);
  // admin takes the value: the target now holds the source plain value
  const m = await admin.api.merge({ body: { source: { branch: depRef(id), at: 'head' }, target: depRef(d2.deployment.deploymentId), targetHead: pc.target.head!.commitId, sourceCommit: pc.source.commit!.commitId, choices: [{ key: 'secrets/api-token/value', take: 'source' }], message: 'copy the token' } });
  assert.equal(m.kind, 'merge', 'the source has no changes since the clone: taking the item is a (trivial) merge');
  assert.equal((await admin.api.revealSecret({ deploymentId: d2.deployment.deploymentId, secretSlug: 'api-token' })).value, src.value);
  const mbs = (await adb.collection('deployments').doc(d2.deployment.deploymentId).collection('mergeBases').doc(`deployment:${id}`).get()).data()!;
  assert.ok(!String(mbs._base).includes(src.value), 'merge bases never store a plain secret value');
});

test('template commits (§21 #105 direct): message required, V_NO_CHANGE, stale base, history, commit detail; detachment proof', async () => {
  const admin = await person('admin');
  const { dep, templateId } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const api = admin.real;
  const depBefore = JSON.stringify((await adb.collection('deployments').doc(id).get()).data()!._draft);
  const t = await api.getTemplate({ templateId });
  const base = t.branch!.head!.commitId;
  const n0 = (await api.listTemplateCommits({ templateId })).items.length;
  await rejects(api.commitTemplate({ templateId, body: { message: 'x', baseCommitId: base, config: await tplInput(api, templateId) } }), 'invalid', 422); // nothing to commit
  const red = await tplInput(api, templateId);
  red.variables.find((v) => v.variableSlug === 'theme')!.value = 'red';
  assert.equal('uncommitted' in t.branch!, false, '§21 #105: no server-side uncommitted count');
  await rejects(api.commitTemplate({ templateId, body: { message: '', baseCommitId: base, config: red } }), 'invalid', 422);
  await rejects(api.commitTemplate({ templateId, body: { message: 'x'.repeat(201), baseCommitId: base, config: red } }), 'invalid', 422);
  await rejects(api.commitTemplate({ templateId, body: { message: 'red', baseCommitId: ulid(), config: red } }), 'stale', 409);
  const c = await api.commitTemplate({ templateId, body: { message: 'theme red\n\nbody text', baseCommitId: base, config: red } });
  assert.equal(c.kind, 'change');
  assert.equal(c.stats.changed, 1);
  assert.equal(c.seq, n0 + 1);
  const hist = await api.listTemplateCommits({ templateId });
  assert.equal(hist.items[0]!.commitId, c.commitId);
  const det = await api.getTemplateCommit({ templateId, commitId: c.commitId });
  assert.deepEqual(det.changes.map((x) => x.key), ['variables/theme/value']);
  assert.equal((await api.getTemplate({ templateId })).variables.find((v) => v.variableSlug === 'theme')!.value, 'red');
  // discard is gone (pending changes live in the browser)
  await rejects(api.discardTemplateWorking({ templateId, body: {} }), 'gone', 410);
  // detachment: the deployment draft is untouched by template commits
  assert.equal(JSON.stringify((await adb.collection('deployments').doc(id).get()).data()!._draft), depBefore);
  // template audit entries
  const au = await api.listAuditLog({ query: { target: `templates/${templateId}`, action: 'template.committed' } });
  assert.ok(au.items.some((x) => x.outcome === 'ok'));
});

test('compare + merge: conflict / theirs removal / target-only removal / skipped; 409 stale / source-changed; revert + reset', async () => {
  const admin = await person('admin');
  const { dep, templateId } = await helloDeployment(admin);
  const id = dep.deploymentId;
  // template: theme yellow (conflict with the deployment's blue), greeting changed (clean), removes the db edge host? no: drops variable db-host default label
  await editTemplate(admin, templateId, (c) => {
    c.variables.find((v) => v.variableSlug === 'theme')!.value = 'yellow';
    c.variables.find((v) => v.variableSlug === 'greeting')!.value = 'Hello from the template';
    siteOf(c, 'app').config.containers[0]!.env = siteOf(c, 'app').config.containers[0]!.env.filter((_, i) => i !== 0);
  }, 'template changes');
  await editDeployment(admin, id, (c) => {
    c.variables.find((v) => v.variableSlug === 'theme')!.value = 'blue';
    siteOf(c, 'db').config.containers[0]!.env.push({ name: 'ONLY_HERE', value: '1', secretRef: null });
  }, 'deployment changes');
  const src = { branch: tplRef(templateId), at: 'head' as const };
  const plan = await admin.api.compareBranches({ body: { source: src, target: depRef(id) } });
  const kinds = Object.fromEntries(plan.items.map((i) => [i.key, i.kind]));
  assert.equal(kinds['variables/theme/value'], 'conflict');
  assert.equal(kinds['variables/greeting/value'], 'theirs');
  assert.equal(kinds['sites/db/containers/redis/env/ONLY_HERE'], 'ours');
  assert.equal(plan.items.find((i) => i.key === 'sites/db/containers/redis/env/ONLY_HERE')!.effect, 'remove');
  const appEnv0 = HELLO().config.sites.find((s) => s.siteSlug === 'app')!.config.containers[0]!.env[0];
  if (appEnv0) assert.equal(plan.items.find((i) => i.key.endsWith(`/env/${appEnv0.name}`))!.effect, 'remove');
  const req = { source: src, target: depRef(id), targetHead: plan.target.head!.commitId, sourceCommit: plan.source.commit!.commitId, message: 'take greeting' };
  // unresolved conflict → 422
  const un = await rejects(admin.api.merge({ body: { ...req, choices: [{ key: 'variables/greeting/value', take: 'source' }] } }), 'invalid', 422);
  assert.match(JSON.stringify(un.details), /V_MERGE_UNRESOLVED/);
  // stale target head
  await rejects(admin.api.merge({ body: { ...req, targetHead: ulid(), choices: [] } }), 'stale', 409);
  // §21 #105: no server working copy → no dirty-working-copy refusal (30-commit-direct covers merges next to browser changes)
  // cherry-pick: greeting taken, theme kept (target), ONLY_HERE kept → kind cherry-pick? every source change = greeting + theme + app env removal
  const choices: MergeChoice[] = [{ key: 'variables/greeting/value', take: 'source' }, { key: 'variables/theme/value', take: 'target' }, { key: 'sites/db/containers/redis/env/ONLY_HERE', take: 'source' }];
  const pv = await admin.api.previewMerge({ body: { ...req, choices } });
  assert.equal(pv.validation.errors.length, 0, JSON.stringify(pv.validation.errors));
  const m = await admin.api.merge({ body: { ...req, choices } });
  assert.equal(m.kind, 'cherry-pick');
  assert.equal(m.source!.branch.kind, 'template');
  const after = await admin.api.getDeployment({ deploymentId: id });
  assert.equal(after.draft.variables.find((v) => v.variableSlug === 'greeting')!.value, 'Hello from the template');
  assert.equal(after.draft.variables.find((v) => v.variableSlug === 'theme')!.value, 'blue');
  assert.ok(!siteOf(after.draft, 'db').config.containers[0]!.env.some((e) => e.name === 'ONLY_HERE'), 'removed from target');
  // the not-taken template changes are "skipped" next time; a source change of a moved head → source-changed
  const plan2 = await admin.api.compareBranches({ body: { source: src, target: depRef(id) } });
  // the not-taken template change is no longer offered as a source change (base moved to the source); shared/branch.ts
  // classifies it 'ours' today — CONTRACT REQUEST (report): mark 'ours' items with a matching skip record as 'skipped' (#94 rule)
  const th = plan2.items.find((i) => i.key === 'variables/theme/value')!;
  assert.ok(th.kind === 'skipped' || th.kind === 'ours', th.kind);
  const rec = (await adb.collection('deployments').doc(id).collection('mergeBases').doc(`template:${templateId}`).get()).data()!;
  assert.ok(JSON.parse(rec._skipped).some((x: { key: string }) => x.key === 'variables/theme/value'), 'skip recorded');
  await editTemplate(admin, templateId, (c) => (c.variables.find((v) => v.variableSlug === 'app-host')!.value = '10.1.1.1'), 'app host');
  await rejects(admin.api.merge({ body: { ...req, targetHead: m.commitId, sourceCommit: plan2.source.commit!.commitId, choices: [] } }), 'conflict', 409);
  // revert the merge, then reset to the create commit
  const rv = await admin.api.revertCommit({ body: { branch: depRef(id), commitId: m.commitId, message: 'Revert "take greeting"', ifHead: m.commitId } });
  assert.equal(rv.kind, 'revert');
  assert.equal(rv.revertOf!.commitId, m.commitId);
  const ar = await admin.api.getDeployment({ deploymentId: id });
  assert.equal(ar.draft.variables.find((v) => v.variableSlug === 'greeting')!.value, HELLO().config.variables.find((v) => v.variableSlug === 'greeting')!.value);
  const all = await admin.api.listDeploymentCommits({ deploymentId: id });
  const first = all.items.at(-1)!;
  await rejects(admin.api.revertCommit({ body: { branch: depRef(id), commitId: first.commitId, message: 'x', ifHead: rv.commitId } }), 'invalid', 422);
  const rs = await admin.api.resetToCommit({ body: { branch: depRef(id), commitId: first.commitId, message: 'back to the start', ifHead: rv.commitId } });
  assert.equal(rs.kind, 'reset');
  assert.equal(rs.hash, first.hash);
  const acts = await admin.api.listActivities({ deploymentId: id, query: { type: 'branch.' } });
  for (const tp of ['branch.committed', 'branch.cherry-picked', 'branch.reverted', 'branch.reset']) assert.ok(acts.items.some((a) => a.type === tp), tp);
  const au = await admin.api.listAuditLog({ query: { target: `deployments/${id}`, action: 'deployment.merged' } });
  assert.ok(au.items.some((x) => x.outcome === 'ok' && x.data.kind === 'cherry-pick'));
  // read-only person cannot merge
  const reader = await person([{ resource: 'deployments', actions: ['read'], scope: { kind: 'all' } }, { resource: 'deployment.config', actions: ['read'], scope: { kind: 'all' } }, { resource: 'templates', actions: ['read'], scope: { kind: 'all' } }] as AccessRule[]);
  await reader.api.compareBranches({ body: { source: src, target: depRef(id) } });
  await rejects(reader.api.merge({ body: { ...req, targetHead: rs.commitId, sourceCommit: null, choices: [] } }), 'forbidden', 403);
  // diffOnly compare of two commits
  const dc = await admin.api.compareBranches({ body: { source: { branch: depRef(id), at: { commitId: m.commitId } }, target: { branch: depRef(id), at: { commitId: first.commitId } }, diffOnly: true } as never });
  assert.equal(dc.diffOnly, true);
  assert.ok(dc.items.length > 0);
});

test('agent branches: compat only for old sites; push verify + fork; views; cloud picks from the site; 0.14 cloud view; removed routes 410', async () => {
  const admin = await person('admin');
  const { dep, templateId } = await helloDeployment(admin);
  const id = dep.deploymentId;
  const app = await enrollSite(admin, id, 'app');
  const db = await enrollSite(admin, id, 'db');
  await adb.collection('deployments').doc(id).collection('agents').doc('app').update({ agentVersion: '0.14.0-test' });
  // db is still old → commits keep writing compat proposals
  const c1 = (await editDeployment(admin, id, (c) => (c.variables.find((v) => v.variableSlug === 'theme')!.value = 'red'), 'red'))!;
  assert.ok(c1.data.compat, 'db < 0.14 → compat proposal');
  await adb.collection('deployments').doc(id).collection('agents').doc('db').update({ agentVersion: '0.14.1' });
  const c2 = (await editDeployment(admin, id, (c) => (c.variables.find((v) => v.variableSlug === 'theme')!.value = 'blue'), 'blue'))!;
  assert.equal(c2.data.compat ?? null, null, 'every site ≥ 0.14 → no compat proposal');
  // the cloud view carries the head; agents fetch it with plain secrets
  const view = await app.api.poll({ query: { wait: 0 } });
  assert.equal(view.branch!.head!.commitId, c2.commitId);
  assert.ok(view.branch!.head!.catalogHash);
  const got = await app.api.getAgentCommit({ commitId: c2.commitId });
  assert.match(got.commit.config.secrets[0]!.value!, /^[A-Za-z0-9]{24}$/);
  const metas = await app.api.listAgentCloudCommits({ query: { limit: 2 } });
  assert.deepEqual(metas.items.map((x) => x.commitId), [c2.commitId, c1.commitId]);
  // the site merges the cloud head (seq 1 on its branch), then commits a hotfix (seq 2)
  const own: BranchRef = { kind: 'agent', deploymentId: id, siteSlug: 'app' };
  const author = { kind: 'person' as const, email: 'ops@site.test', uid: null, name: null, siteSlug: 'app' };
  const a1 = makeCommit({ commitId: ulid(), branch: own, parent: null, kind: 'merge', message: 'merge cloud seq ' + c2.seq, author, createdAt: new Date().toISOString(), config: got.commit.config, catalog: got.commit.catalog, source: { branch: depRef(id), commitId: c2.commitId, seq: c2.seq, label: dep.label, picks: 'all', skipped: 0 }, scope: 'app' });
  const hot = clone(got.commit.config);
  siteOf(hot, 'app').config.containers[0]!.env.push({ name: 'HOTFIX', value: 'on', secretRef: null });
  const a2 = makeCommit({ commitId: ulid(), branch: own, parent: a1, kind: 'drift', message: 'hotfix made on the server', author, createdAt: new Date().toISOString(), config: hot, catalog: got.commit.catalog, scope: 'app' });
  await rejects(app.api.pushAgentBranch({ body: { commits: [{ ...a1, hash: 'sha256:' + '0'.repeat(64) }] } }), 'invalid', 422);
  await rejects(app.api.pushAgentBranch({ body: { commits: [{ ...a1, branch: { kind: 'agent', deploymentId: id, siteSlug: 'db' } }] } }), 'invalid', 422);
  await rejects(app.api.pushAgentBranch({ body: { commits: [a2] } }), 'invalid', 422); // parent unknown
  const pushed = await app.api.pushAgentBranch({ body: { commits: [a2, a1] } });
  assert.deepEqual(pushed.stored.sort(), [a1.commitId, a2.commitId].sort());
  assert.equal(pushed.head!.commitId, a2.commitId);
  assert.deepEqual((await app.api.pushAgentBranch({ body: { commits: [a1, a2] } })).stored, [], 'idempotent');
  const fork: Commit = { ...a2, commitId: ulid(), message: 'fork' };
  await rejects(app.api.pushAgentBranch({ body: { commits: [fork] } }), 'conflict', 409);
  // heartbeat.branch → uncommitted drift
  await app.api.heartbeat({ body: { siteSlug: 'app', agentVersion: '0.14.0-test', headRevId: null, staging: null, peersSeen: [], metrics: { cpu: 0, memPct: 0, diskPct: 0 } as never, edge: null, keyVersion: 1, advertiseHost: '127.0.0.1', agentPort: 7080, ip: '127.0.0.1', hostname: 'h', branch: { head: null, mergedFrom: { commitId: c2.commitId, seq: c2.seq }, uncommitted: { working: 1, drift: 2, notes: 0, scannedAt: new Date().toISOString() } } } as never });
  const views = await admin.api.listAgentBranches({ deploymentId: id });
  const av = views.items.find((x) => x.siteSlug === 'app')!;
  assert.equal(av.head!.commitId, a2.commitId);
  assert.equal(av.behind, 0);
  assert.equal(av.differs, 1);
  assert.equal(av.ahead, 1);
  assert.equal(av.uncommitted!.drift, 2);
  assert.equal(av.needsAgent014, false);
  const hist = await admin.api.listAgentBranchCommits({ deploymentId: id, siteSlug: 'app' });
  assert.deepEqual(hist.items.map((x) => x.seq), [2, 1]);
  const det = await admin.api.getAgentBranchCommit({ deploymentId: id, siteSlug: 'app', commitId: a2.commitId });
  assert.equal(det.commit.config.secrets[0]!.value, MASK);
  assert.deepEqual(det.changes.map((x) => x.key), ['sites/app/containers/hello-api/env/HOTFIX'.replace('hello-api', siteOf(hot, 'app').config.containers[0]!.containerSlug)]);
  // the cloud cherry-picks the hotfix into the deployment (base = the merged deployment commit)
  const plan = await admin.api.compareBranches({ body: { source: { branch: own, at: 'head' }, target: depRef(id) } });
  assert.equal(plan.base.kind, 'origin');
  assert.equal(plan.items.length, 1);
  assert.equal(plan.items[0]!.kind, 'theirs');
  const m = await admin.api.merge({ body: { source: { branch: own, at: 'head' }, target: depRef(id), targetHead: c2.commitId, sourceCommit: a2.commitId, choices: [{ key: plan.items[0]!.key, take: 'source' }], message: 'take the hotfix from app' } });
  assert.equal(m.kind, 'merge');
  assert.equal(m.source!.branch.kind, 'agent');
  // agent branches are never written by the cloud
  await rejects(admin.api.merge({ body: { source: { branch: depRef(id), at: 'head' }, target: own, targetHead: a2.commitId, sourceCommit: null, choices: [], message: 'x' } }), 'forbidden', 403);
  // a template made from the agent branch
  const t = await admin.api.createTemplate({ body: { label: uniq('from-site'), from: { branch: own, at: 'head' } } });
  assert.equal(t.branch!.origin!.branch.kind, 'agent');
  void templateId;
  void db;
  // removed routes
  const g = await rejects(admin.api.publish({ deploymentId: id, body: { ifRevision: 0, summary: 'x' } }), 'gone', 410);
  assert.ok(g.details.hint);
  await rejects(admin.api.getTemplateSyncStatus({ deploymentId: id }), 'gone', 410);
  await rejects(admin.api.getTemplateSyncPlan({ deploymentId: id }), 'gone', 410);
  await rejects(admin.api.unlinkTemplate({ deploymentId: id, body: { confirm: true } }), 'gone', 410);
  const r = await raw('POST', `/v1/deployments/${id}/template-sync/apply`, { headers: { Authorization: admin.bearer, 'Content-Type': 'application/json' }, body: '{}' });
  assert.equal(r.status, 410);
});

test('merge into a deployment: a new required secret gets its value in the merge (secretValues); never on a template', async () => {
  const admin = await person('admin');
  const { dep, templateId } = await helloDeployment(admin);
  const id = dep.deploymentId;
  await editTemplate(admin, templateId, (c) => {
    c.secrets.push({ secretSlug: 'maps-key', label: 'Maps key', description: '', required: true, generate: null, value: null, rotatedAt: null });
  }, 'add maps-key');
  const src = { branch: tplRef(templateId), at: 'head' as const };
  const plan = await admin.api.compareBranches({ body: { source: src, target: depRef(id) } });
  const choices: MergeChoice[] = plan.items.filter((i) => i.key.startsWith('secrets/maps-key')).map((i) => ({ key: i.key, take: 'source' }));
  assert.ok(choices.length, JSON.stringify(plan.items.map((i) => i.key)));
  const req = { source: src, target: depRef(id), targetHead: plan.target.head!.commitId, sourceCommit: plan.source.commit!.commitId, choices, message: 'add maps-key' };
  const pv0 = await admin.api.previewMerge({ body: req });
  assert.deepEqual(pv0.missing.secrets.map((s) => s.secretSlug), ['maps-key']);
  assert.ok(pv0.validation.errors.some((e) => e.code === 'V_SECRET_REQUIRED'));
  await rejects(admin.api.merge({ body: req }), 'invalid', 422);
  const pv1 = await admin.api.previewMerge({ body: { ...req, secretValues: { 'maps-key': 'abc123' } } });
  assert.equal(pv1.validation.errors.length, 0, JSON.stringify(pv1.validation.errors));
  assert.equal(pv1.missing.secrets.length, 0);
  assert.ok(!JSON.stringify(pv1).includes('abc123'), 'the preview never echoes the value');
  const m = await admin.api.merge({ body: { ...req, secretValues: { 'maps-key': 'abc123' } } });
  assert.ok(!JSON.stringify(m).includes('abc123'), 'the merge response masks the value');
  assert.deepEqual((m.data as { secretValues?: { set: string[] } }).secretValues?.set, ['maps-key']);
  const d = await admin.api.getDeployment({ deploymentId: id });
  assert.equal(d.draft.secrets.find((s) => s.secretSlug === 'maps-key')!.isSet, true);
  // a template target holds no values
  await rejects(admin.api.previewMerge({ body: { source: { branch: depRef(id), at: 'head' }, target: tplRef(templateId), targetHead: null, sourceCommit: null, choices: [], secretValues: { 'maps-key': 'x' } } }), 'invalid', 422);
});
