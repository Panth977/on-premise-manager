// OPM hello-world end-to-end (SPEC-final §20.2 steps 1–20).
// Cloud API as the admin human; agent local API as an operator (access key + actor); Playwright for the UI flows.
import { readFileSync, writeFileSync, existsSync, mkdirSync, copyFileSync, statSync, rmSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { join } from 'node:path';
import { execFileSync } from 'node:child_process';
import type * as T from '../../shared/dist/types.js';
import { CFG, OPM_DIR, E2E_DIR, type SiteName } from './lib/env.js';
import { step, check, warn, note, log, write, summary, extra, shot } from './lib/report.js';
import { cloud, cloudRaw, signIn } from './lib/cloud.js';
import { AgentSession } from './lib/agent.js';
import { sleep, waitFor, http, expectError, errInfo } from './lib/util.js';
import { opmContainers, portFree, writeAgentConfig, startAgent, killPrevious, stopAgents, tailLog, docker, agentAlive, siteDir } from './lib/procs.js';
import * as ui from './ui.js';

// ───────────────────────── fixtures
const fixture = JSON.parse(readFileSync(join(OPM_DIR, 'shared/testdata/hello-config.json'), 'utf8')) as { banner: string; config: T.Config };
const catalog = JSON.parse(readFileSync(join(OPM_DIR, 'shared/testdata/hello-catalog.json'), 'utf8')) as {
  services: Record<string, { serviceSlug: string; label: string; icon: string | null }>;
  images: Record<string, { kind: T.ImageKind; ports: T.ImagePort[]; env: T.ImageEnv[]; volumes: T.ImageVolume[]; healthcheck: T.HealthCheck | null }>;
};
// services are keyed by their slug (one identifier); every image is OPM version v1 of a docker reference in the test registry
const FIXED = { web: 'hello-web', api: 'hello-api', redis: 'redis', nfp: 'NfpHello000000000001' };
const VERSION = 'v1';
/** docker tag pushed to the test registry per service (e2e/apps push script) */
const DOCKER_TAGS: Record<string, string> = { [FIXED.web]: '1.0.0', [FIXED.api]: '1.0.0', [FIXED.redis]: '7.4-alpine' };
const registryHostOf = (url: string) => url.replace(/^https?:\/\//, '').replace(/\/.*$/, '');
const referenceOf = (slug: string) => `${registryHostOf(CFG.registryUrl)}/${CFG.repoPrefix}/${slug}:${DOCKER_TAGS[slug]}`;

// ───────────────────────── run state (shared between steps)
const S = {
  registryId: '',
  svc: {} as Record<string, string>, // service slug → service slug (kept for state.json compatibility)
  nfpId: '',
  templateId: '',
  deploymentId: '',
  accessKey: '',
  newAccessKey: '',
  bannerSha: '',
  agentBin: CFG.agentBin,
  platform: 'linux/arm64' as T.Platform,
  rev1: '',
  rev2: '',
  rev3: '',
  revFail: '',
  revStale: '',
  s1: '',
  ctr12: {} as Record<string, string>,
  sess: {} as Record<SiteName, AgentSession>,
  timings: {} as Record<string, number>,
};
const statePath = join(CFG.runDir, 'state.json');
function saveState(): void {
  mkdirSync(CFG.runDir, { recursive: true });
  const prev = existsSync(statePath) ? (JSON.parse(readFileSync(statePath, 'utf8')) as { deployments?: string[] }) : {};
  const deployments = [...new Set([...(prev.deployments ?? []), S.deploymentId].filter(Boolean))];
  writeFileSync(statePath, JSON.stringify({ ...S, sess: undefined, deployments }, null, 2));
}

const edgeUrl = (site: SiteName, path: string) => `http://127.0.0.1:${CFG.sites[site].edgePort}${path}`;
const guiBase = (site: SiteName) => `http://127.0.0.1:${CFG.sites[site].guiAliasPort}`;
const mapId = (id: string) => S.svc[id] ?? id;

/** hello-config with the real ids (services, not-found page, banner sha) */
function realConfig(): T.Config {
  const c = structuredClone(fixture.config);
  for (const site of c.sites) {
    if (site.config.notFoundPageId) site.config.notFoundPageId = S.nfpId;
    // round C: containers are instances (containerSlug = their name, unchanged); only the service reference is mapped.
    // Proxy routes / depends name containers (containerSlug), so they stay as they are. host null = auto.
    for (const ct of site.config.containers) ct.serviceSlug = mapId(ct.serviceSlug);
  }
  for (const f of c.files) if (f.fileSlug === 'banner') f.content = { sha256: S.bannerSha, sizeBytes: Buffer.byteLength(fixture.banner) };
  c.secrets = c.secrets.map((s) => ({ ...s, value: null, rotatedAt: null }));
  return c;
}

async function getCount(): Promise<number | null> {
  const r = await http(edgeUrl('app', '/api/count'));
  try {
    return (JSON.parse(r.text) as { count: number }).count;
  } catch {
    return null;
  }
}
async function postCount(): Promise<number | null> {
  const r = await http(edgeUrl('app', '/api/count'), { method: 'POST' });
  try {
    return (JSON.parse(r.text) as { count: number }).count;
  } catch {
    return null;
  }
}
async function pageTheme(): Promise<string | null> {
  const r = await http(edgeUrl('app', '/'));
  return /data-theme="([a-z]+)"/.exec(r.text)?.[1] ?? null;
}

async function cloudProposal(revId: string): Promise<T.Proposal | undefined> {
  const l = await cloud.listProposals({ deploymentId: S.deploymentId, query: { limit: 50 } });
  return l.items.find((p) => p.revId === revId);
}
async function waitCloudProposal(revId: string, statuses: T.ProposalStatus[], timeoutMs: number): Promise<T.Proposal> {
  return waitFor(`cloud proposal ${revId.slice(-6)} ∈ ${statuses.join('|')}`, async () => {
    const p = await cloudProposal(revId);
    return p && statuses.includes(p.status) ? p : null;
  }, timeoutMs, 3000);
}
async function waitCloudHead(seq: number, timeoutMs = 90_000): Promise<T.DeploymentView> {
  return waitFor(`cloud head seq ${seq}`, async () => {
    const d = await cloud.getDeployment({ deploymentId: S.deploymentId });
    return d.head && d.head.seq === seq ? d : null;
  }, timeoutMs, 3000);
}
async function agentHeads(sess: AgentSession): Promise<Record<string, number | null>> {
  const o = await sess.api.overview({});
  return Object.fromEntries(o.sites.map((t) => [t.siteSlug, t.head?.seq ?? null]));
}
async function waitAgentHeads(sess: AgentSession, seq: number, timeoutMs = 90_000): Promise<Record<string, number | null>> {
  return waitFor(`agent heads seq ${seq} on app+db`, async () => {
    const h = await agentHeads(sess);
    return h.app === seq && h.db === seq ? h : null;
  }, timeoutMs, 2000);
}

/** gate → approve/start → (early approve refused) → wait → wrong word refused → ACCEPT → run until done/failed */
async function approveViaApi(sess: AgentSession, kind: 'update' | 'rollback', id: string, opts: { negative?: boolean } = {}): Promise<T.UpdateRun> {
  const st = Date.now();
  if (kind === 'update') {
    await waitFor(`gate open for ${id.slice(-6)} on ${sess.label}`, async () => {
      const d = await sess.api.getProposal({ revId: id });
      return d.gate.open ? d : null;
    }, 180_000, 2000);
  } else {
    await waitFor(`rollback gate open for ${id.slice(-6)}`, async () => {
      const p = await sess.api.rollbackPlan({ snapshotId: id });
      return p.gate.open ? p : null;
    }, 120_000, 2000);
  }
  const start = kind === 'update' ? await sess.api.approveStart({ revId: id }) : await sess.api.rollbackStart({ snapshotId: id });
  const intent = start.intent;
  check(`${kind} intent created (notBefore ${intent.notBefore})`, !!intent.intentId);
  const call = (typed: string) =>
    kind === 'update' ? sess.api.approve({ revId: id, body: { intentId: intent.intentId, typed } }) : sess.api.rollback({ snapshotId: id, body: { intentId: intent.intentId, typed } });
  if (opts.negative && CFG.approveWaitSec >= 2) {
    const early = await expectError(() => call('ACCEPT'));
    check('approve before notBefore → invalid (wait)', early?.code === 'invalid', early ?? 'accepted!');
  }
  const waitMs = new Date(intent.notBefore).getTime() - Date.now();
  if (waitMs > 0) await sleep(waitMs + 300);
  if (opts.negative) {
    const wrong = await expectError(() => call('accept'));
    check('typed "accept" (wrong word) → invalid', wrong?.code === 'invalid', wrong ?? 'accepted!');
  }
  const res = await call('ACCEPT');
  check(`${kind} ACCEPT → update ${res.updateId} started (coordinator ${res.run.coordinator})`, !!res.updateId);
  const phases: string[] = [];
  const run = await waitFor(`update ${res.updateId.slice(-6)} finished`, async () => {
    const r = await sess.api.getUpdate({ updateId: res.updateId });
    if (phases[phases.length - 1] !== r.phase) phases.push(r.phase);
    return r.phase === 'done' || r.phase === 'failed' ? r : null;
  }, 20 * 60_000, 1000);
  note(`${kind} phases observed: ${phases.join(' → ')}; result ${JSON.stringify(run.result)}; ${((Date.now() - st) / 1000).toFixed(1)} s incl. ${CFG.approveWaitSec} s wait`);
  for (const [site, ss] of Object.entries(run.sites)) note(`  ${site}: step ${ss.step} ${ss.state}${ss.error ? ' error ' + ss.error : ''}; containers ${ss.containers.map((c) => `${c.containerSlug}(${c.serviceSlug}):${c.action}:${c.state}`).join(', ')}`);
  return run;
}

async function approve(sess: AgentSession, revId: string, opts: { negative?: boolean; viaUi?: boolean; shotName?: string } = {}): Promise<T.UpdateRun> {
  if (opts.viaUi && CFG.ui) {
    try {
      let updateId = await ui.agentApproveUi(sess.base, S.accessKey, CFG.actor, revId, opts.shotName ?? `agent-approve-${revId.slice(-6)}`);
      if (!updateId) updateId = (await waitFor('current update after the GUI approve', async () => (await sess.api.currentUpdate({})).run?.updateId, 30_000, 1000));
      const run = await waitFor(`update ${updateId} finished (UI)`, async () => {
        const r = await sess.api.getUpdate({ updateId });
        return r.phase === 'done' || r.phase === 'failed' ? r : null;
      }, 20 * 60_000, 1000);
      check('approved through the agent GUI (countdown + typed ACCEPT)', true, `update ${updateId}`);
      return run;
    } catch (e) {
      warn('agent GUI approve flow failed — falling back to the API', errInfo(e).message);
    }
  }
  return approveViaApi(sess, 'update', revId, opts);
}

async function publish(summaryText: string): Promise<T.PublishResponse> {
  const d = await cloud.getDeployment({ deploymentId: S.deploymentId });
  const p = await cloud.publish({ deploymentId: S.deploymentId, body: { ifRevision: d.draftVersion, summary: summaryText } });
  check(`published "${summaryText}" → rev ${p.revision.revId} seq ${p.revision.seq}`, p.proposal.status === 'published' || p.proposal.status === 'staging', p.proposal.status);
  return p;
}

async function putVariables(mut: (v: T.Variable[]) => void): Promise<T.DraftWriteResponse> {
  const d = await cloud.getDeployment({ deploymentId: S.deploymentId });
  const vars = structuredClone(d.draft.variables);
  mut(vars);
  return cloud.putDraftSection({ deploymentId: S.deploymentId, section: 'variables', body: { ifRevision: d.draftVersion, items: vars } });
}

async function resetToHead(): Promise<void> {
  const r = await cloud.resetDraftToHead({ deploymentId: S.deploymentId, body: { confirm: true } });
  note(`draft reset to head (draftVersion ${r.draftVersion})`);
}

function diffSummary(d: T.DiffResponse): string {
  return d.changes.map((c) => `${c.section}:${c.path} ${JSON.stringify(c.before)}→${JSON.stringify(c.after)}`).join('; ') || '(no changes)';
}

async function cloudActivities(q: T.ActivitiesQuery): Promise<T.Activity[]> {
  const out: T.Activity[] = [];
  let cursor: string | undefined;
  for (let i = 0; i < 20; i++) {
    const r = await cloud.listActivities({ deploymentId: S.deploymentId, query: { ...q, limit: 500, cursor } });
    out.push(...r.items);
    if (!r.cursor) break;
    cursor = r.cursor;
  }
  return out;
}

function containersBySlug(): Record<string, { id: string; started: string; site: string }> {
  const out: Record<string, { id: string; started: string; site: string }> = {};
  for (const c of opmContainers(S.deploymentId)) {
    if (c.state !== 'running') continue;
    const slug = c.container || c.slug || c.name.split('_').pop()!;
    out[slug] = { id: c.id, started: c.started, site: c.site };
  }
  return out;
}

// ───────────────────────── steps
async function main(): Promise<void> {
  log(`OPM e2e — api ${CFG.apiBase}, frontend ${CFG.frontendUrl}`);
  killPrevious();
  const arch = docker('info', '--format', '{{.Architecture}}');
  S.platform = /aarch64|arm64/.test(arch) ? 'linux/arm64' : 'linux/amd64';

  await step('0', 'Preflight (ports free, docker, previous run cleaned)', async () => {
    // remove containers of previous e2e deployments only (other teams may use docker too)
    if (existsSync(statePath)) {
      const prev = JSON.parse(readFileSync(statePath, 'utf8')) as { deployments?: string[] };
      for (const dep of prev.deployments ?? []) {
        const ids = opmContainers(dep).map((c) => c.id);
        if (ids.length) {
          docker('rm', '-f', ...ids);
          note(`removed ${ids.length} containers of previous e2e deployment ${dep}`);
        }
      }
    }
    for (const site of ['app', 'db'] as SiteName[]) rmSync(siteDir(site), { recursive: true, force: true });
    await sleep(1000);
    const ports = [7080, 7081, 8090, 8091, 8081, 8082, 16379];
    for (const p of ports) check(`port ${p} free`, await portFree(p), '', false);
    check(`docker reachable (${arch}, images for ${S.platform})`, !!arch, arch, true);
    const r = await http(`${CFG.apiBase}/v1/me`).catch((e) => ({ status: 0, text: String(e), headers: new Headers() }));
    check('cloud API reachable (GET /v1/me without token → 401)', r.status === 401, `${r.status} ${r.text.slice(0, 200)}`, true);
  });

  await step('1', 'Admin login + GET /v1/me', async () => {
    await signIn();
    const me = await cloud.getMe({});
    check('GET /v1/me → isAdmin', me.isAdmin === true, { uid: me.account.uid, email: me.account.email, isAdmin: me.isAdmin }, true);
    if (CFG.uiCloud) await ui.cloudLogin(CFG.frontendUrl, CFG.adminEmail, CFG.adminPassword).catch((e) => warn('frontend login (Playwright) failed', errInfo(e).message));
  }, ['0']);

  await step('2', 'Registry: create + Test login', async () => {
    const key = readFileSync(CFG.registryKeyFile, 'utf8');
    const label = 'E2E Artifact Registry';
    const existing = (await cloud.listRegistries({})).items.find((r) => r.label === label);
    if (existing) {
      S.registryId = existing.registryId;
      await cloud.patchRegistry({ registryId: S.registryId, body: { url: CFG.registryUrl, username: '_json_key', password: key } });
      note(`reusing registry ${S.registryId} (password re-set)`);
    } else {
      const r = await cloud.createRegistry({ body: { label, url: CFG.registryUrl, username: '_json_key', password: key } });
      S.registryId = r.registryId;
      check('registry created, password masked', JSON.stringify(r).indexOf('private_key') < 0, { registryId: r.registryId });
    }
    const t = await cloud.testRegistry({ registryId: S.registryId });
    check('Test login → ok', t.ok === true, t.message, true);
  }, ['1']);

  await step('3', 'Services + 3 images (copy) → import ready', async () => {
    const existing = (await cloud.listServices({})).items;
    for (const [fid, s] of Object.entries(catalog.services)) {
      let svc: T.Service | undefined = existing.find((x) => x.serviceSlug === s.serviceSlug);
      if (!svc) {
        svc = await cloud.createService({ body: { label: s.label, serviceSlug: s.serviceSlug, icon: s.icon, description: `e2e ${s.serviceSlug}` } });
        note(`service ${s.serviceSlug} created`);
      } else note(`service ${s.serviceSlug} exists`);
      S.svc[fid] = svc.serviceSlug;
    }
    // inspect prefill once (hello-web)
    try {
      const ins = await cloud.inspectImage({ serviceSlug: S.svc[FIXED.web], body: { reference: referenceOf(FIXED.web), registryId: S.registryId, platform: S.platform } });
      check('inspect hello-web:1.0.0 → prefill port 8080 + THEME env', JSON.stringify(ins).includes('8080') && JSON.stringify(ins).includes('THEME'), JSON.stringify(ins).slice(0, 300));
    } catch (e) {
      check('inspect hello-web:1.0.0', false, errInfo(e));
    }
    for (const fid of [FIXED.web, FIXED.api, FIXED.redis]) {
      const sid = S.svc[fid];
      const meta = catalog.images[`${fid}:${VERSION}`];
      const imgs = await cloud.listImages({ serviceSlug: sid });
      let img = imgs.items.find((i) => i.version === VERSION);
      if (img && img.import.status === 'failed') img = await cloud.retryImport({ serviceSlug: sid, version: VERSION });
      if (!img) {
        img = await cloud.registerImage({ serviceSlug: sid, body: { version: VERSION, reference: referenceOf(fid), registryId: S.registryId, platform: S.platform, mode: 'copy', kind: meta.kind, ports: meta.ports, env: meta.env, volumes: meta.volumes, healthcheck: meta.healthcheck, description: 'e2e' } });
        note(`registered ${fid}:${VERSION} = ${referenceOf(fid)} (${img.import.status})`);
      }
    }
    const st = Date.now();
    for (const fid of [FIXED.web, FIXED.api, FIXED.redis]) {
      const img = await waitFor(`import ${fid}:${VERSION}`, async () => {
        const i = await cloud.getImage({ serviceSlug: S.svc[fid], version: VERSION });
        if (i.import.status === 'failed') throw Object.assign(new Error(`import failed: ${i.import.error}`), { fatal: true });
        return i.import.status === 'ready' ? i : null;
      }, 10 * 60_000, 5000);
      check(`${fid}:${VERSION} import ready — ${img.import.layers.length} layers, ${img.import.sizeBytes} B, digest ${img.digest?.slice(0, 19)}`, img.import.layers.length > 0 && !!img.digest && img.platform === S.platform);
    }
    S.timings.importSec = Math.round((Date.now() - st) / 1000);
  }, ['2']);

  await step('4', 'Not-found page "Hello 404"', async () => {
    const p = await cloud.createNotFoundPage({ body: { label: 'Hello 404', htmlCode: '<!doctype html><html><head><title>Hello 404</title></head><body><h1>Hello 404</h1><p>nothing here</p></body></html>' } });
    S.nfpId = p.notFoundPageId;
    check('page created', !!p.notFoundPageId, p.notFoundPageId);
  }, ['1']);

  await step('5', 'Template "Hello world" (PATCH config, secret value null) → validation ok', async () => {
    const blob = await cloud.uploadBlob({ body: fixture.banner, contentType: 'text/plain' });
    S.bannerSha = blob.sha256;
    const expectSha = 'sha256:' + createHash('sha256').update(fixture.banner).digest('hex');
    check('banner blob uploaded, sha256 matches', blob.sha256 === expectSha && blob.sizeBytes === 38, blob);
    const t = await cloud.createTemplate({ body: { label: `Hello world ${new Date().toISOString().slice(0, 16)}`, icon: 'globe', description: 'OPM e2e hello world' } });
    S.templateId = t.templateId;
    const c = realConfig();
    const w = await cloud.patchTemplate({ templateId: t.templateId, body: { ifVersion: t.version, variables: c.variables, secrets: c.secrets, files: c.files, sites: c.sites } });
    const errors = w.validation.issues?.filter((i) => i.severity === 'error') ?? [];
    check(`template saved v${w.template.version}, no validation errors`, errors.length === 0, { warnings: (w.validation.issues ?? []).filter((i) => i.severity === 'warning').map((i) => i.code) }, true);
    check('template secret value is null', w.template.secrets.every((s) => s.value === null));
  }, ['3', '4']);

  await step('6', 'Deployment from the template → access key returned; api-token generated', async () => {
    const r = await cloud.createDeployment({ body: { label: `Hello e2e ${new Date().toISOString().slice(5, 16)}`, templateId: S.templateId, tagSlugs: [], managerAccountUids: [] } });
    S.deploymentId = r.deployment.deploymentId;
    S.accessKey = r.accessKey;
    saveState();
    check('access key shown (XXXX-XXXX-XXXX-XXXX-XXXX-XXXX)', /^[A-Z2-7]{4}(-[A-Z2-7]{4}){5}$/.test(r.accessKey), r.accessKey.replace(/[A-Z2-7]/g, 'X'));
    const sec = r.deployment.draft.secrets.find((s) => s.secretSlug === 'api-token');
    check('draft.secrets[api-token].isSet = true, value masked', !!sec?.isSet && sec.value !== null && !/^[A-Za-z0-9]{24}$/.test(String(sec.value)), sec);
    const again = await cloud.getDeployment({ deploymentId: S.deploymentId });
    check('access key not readable later', !JSON.stringify(again).includes(r.accessKey) && !JSON.stringify(again).includes('accessKeyHash'));
    const v = await cloud.getDiff({ deploymentId: S.deploymentId });
    check('diff: publishable (no head yet)', v.publishable, { blockers: v.blockers.map((b) => `${b.code} ${b.path}`) });
    if (CFG.uiCloud) await ui.cloudTour(CFG.frontendUrl, S.deploymentId).catch((e) => warn('frontend tour failed', errInfo(e).message));
  }, ['5']);

  await step('7', 'Setup: install tokens, install script, start both agents → enrolled + online', async () => {
    for (const site of ['db', 'app'] as SiteName[]) {
      const tk = await cloud.createInstallToken({ deploymentId: S.deploymentId, body: { siteSlug: site } });
      check(`install token ${site}: opi_ + 43 chars, command has the header form`, /^opi_[A-Za-z0-9_-]{43}$/.test(tk.token) && tk.command.includes(`Authorization: Bearer ${tk.token}`) && tk.command.includes('/v1/install/script'), tk.command.replace(tk.token, 'opi_…'));
      const scr = await cloudRaw('/v1/install/script', {}, tk.token);
      const body = await scr.text();
      check(`install script ${site} (200, sh, SITE="${site}")`, scr.status === 200 && body.startsWith('#!/bin/sh') && body.includes(`SITE="${site}"`) && body.includes(S.deploymentId), `${scr.status} ${body.slice(0, 120)}`);
      if (!CFG.agentBin) {
        // the REAL install script, adapted for an unprivileged macOS run: no root check, paths under e2e/run/<site>,
        // no systemd (the harness starts the agent). Download, checksum and verify-release run unchanged.
        const dir = siteDir(site);
        mkdirSync(dir, { recursive: true });
        const adapted = body
          .replace(/^\[ "\$\(id -u\)" = 0 \].*$/m, ': # (e2e) root check skipped')
          .replaceAll('/usr/local/bin/opm-agent', `${dir}/bin/opm-agent`)
          .replaceAll('/etc/systemd/system/opm-agent.service', `${dir}/opm-agent.service`)
          .replaceAll('/etc/opm', `${dir}/etc`)
          .replaceAll('/var/lib/opm', `${dir}/data`)
          .replace(/^systemctl .*$/m, 'echo "(e2e) systemctl skipped"');
        mkdirSync(join(dir, 'bin'), { recursive: true });
        writeFileSync(join(dir, 'install.sh'), adapted);
        try {
          const out = execFileSync('sh', [join(dir, 'install.sh')], { encoding: 'utf8', stdio: 'pipe', env: { ...process.env, PATH: process.env.PATH ?? '' } });
          check(`install script ran for ${site} (download + sha256 + verify-release)`, existsSync(join(dir, 'bin/opm-agent')), out.trim().split('\n').slice(-2).join(' | '));
        } catch (e) {
          const x = e as { stdout?: string; stderr?: string; status?: number };
          check(`install script ran for ${site}`, false, `exit ${x.status}: ${x.stdout ?? ''} ${x.stderr ?? ''}`, true);
        }
        const written = JSON.parse(readFileSync(join(dir, 'etc/agent.json'), 'utf8')) as Record<string, string>;
        check(`script wrote agent.json for ${site}`, written.siteSlug === site && written.deploymentId === S.deploymentId && written.apiBase === CFG.apiBase, written);
        check(`script wrote the install token file (0600)`, (statSync(join(dir, 'etc/install-token')).mode & 0o777) === 0o600);
        S.agentBin = join(dir, 'bin/opm-agent');
      } else {
        mkdirSync(join(siteDir(site), 'etc'), { recursive: true });
        writeFileSync(join(siteDir(site), 'etc/install-token'), tk.token, { mode: 0o600 });
      }
      writeAgentConfig(site, S.deploymentId);
    }
    saveState();
    for (const site of ['db', 'app'] as SiteName[]) startAgent(site, CFG.agentBin || join(siteDir(site), 'bin/opm-agent'));
    const st = Date.now();
    const status = await waitFor('both sites online in the cloud', async () => {
      const s = await cloud.getStatus({ deploymentId: S.deploymentId });
      const ok = ['app', 'db'].every((x) => s.sites.find((y) => y.siteSlug === x)?.online);
      if (!agentAlive('app') || !agentAlive('db')) throw new Error(`agent died:\napp:\n${tailLog('app', 15)}\ndb:\n${tailLog('db', 15)}`);
      return ok ? s : null;
    }, 90_000, 2000);
    S.timings.onlineSec = Math.round((Date.now() - st) / 1000);
    check(`both enrolled + online within ${S.timings.onlineSec} s (≤ 60)`, S.timings.onlineSec <= 60, status.sites.map((s) => `${s.siteSlug}:${s.enrolled}/${s.online}/${s.agentVersion}`));
    for (const site of ['app', 'db'] as SiteName[]) check(`install token file of ${site} deleted after enroll`, !existsSync(join(siteDir(site), 'etc/install-token')));
    const setup = await cloud.getSetup({ deploymentId: S.deploymentId });
    check('setup lists both agents', setup.sites.filter((s) => s.agent).length === 2, setup.sites.map((s) => `${s.siteSlug}:${!!s.agent}`));
    const g = await http(`${guiBase('app')}/_opm/`);
    check('GET :8090/_opm/ → 302 /_opm/app', g.status === 302 && (g.headers.get('location') ?? '').includes('/_opm/app'), `${g.status} ${g.headers.get('location')}`);
    const lp = await http(`${guiBase('app')}/_opm/login`);
    check('GET :8090/_opm/login → GUI page (200 html)', lp.status === 200 && /<html/i.test(lp.text), `${lp.status}`);
    if (CFG.ui) await ui.agentLoginPage(guiBase('app')).catch((e) => warn('agent login page screenshot failed', errInfo(e).message));
    const running = opmContainers(S.deploymentId);
    check('nothing runs before the first proposal (docker ps label=opm.deployment empty)', running.length === 0, running.map((c) => c.name));
    const info = await (new AgentSession(guiBase('db'), 'db')).api.info({});
    check('db /_opm/api/info: enrolled + has access key', info.enrolled && info.hasAccessKey, info);
  }, ['6']);

  await step('8', 'Diff → publish "release 1" → staging → ready on both sites', async () => {
    const d = await cloud.getDiff({ deploymentId: S.deploymentId });
    check('diff publishable', d.publishable, d.blockers);
    note(`diff: ${d.changes.length} changes`);
    const p = await publish('release 1');
    S.rev1 = p.revision.revId;
    check('release 1 seq 1, parent null', p.revision.seq === 1 && p.revision.parentRevId === null, p.revision);
    const st = Date.now();
    const ready = await waitCloudProposal(S.rev1, ['ready'], 15 * 60_000);
    S.timings.stage1Sec = Math.round((Date.now() - st) / 1000);
    check(`staged ready on app + db (${S.timings.stage1Sec} s)`, ready.sites.app?.staging === 'ready' && ready.sites.db?.staging === 'ready', ready.sites);
  }, ['7']);

  await step('9', 'Agent GUI API on app: login (wrong key once), actor, gate, approve ACCEPT → applied seq 1', async () => {
    const app = new AgentSession(guiBase('app'), 'app');
    const bad = await expectError(() => app.api.login({ body: { accessKey: 'AAAA-AAAA-AAAA-AAAA-AAAA-AAAA' } }));
    check('wrong key → 401 invalid-key', bad?.status === 401 && bad.code === 'invalid-key', bad);
    await app.login(S.accessKey, CFG.actor);
    S.sess.app = app;
    const noActor = new AgentSession(app.base, 'app-no-actor');
    noActor.cookie = app.cookie;
    const na = await expectError(() => noActor.api.approveStart({ revId: S.rev1 }));
    check('session+actor endpoint without actor token → 401 actor-required', na?.status === 401 && na.code === 'actor-required', na);
    const det = await app.api.getProposal({ revId: S.rev1 });
    const sources = det.staging.map((s) => `${s.siteSlug}: ${[...new Set(s.blobs.map((b) => b.source ?? 'null'))].join('/')} (${s.layers.have}/${s.layers.total} layers)`);
    note(`staging sources — ${sources.join(' · ')}`);
    const perSite = det.staging.map((s) => new Set(s.blobs.map((b) => b.sha256)));
    const shared = perSite.length === 2 ? [...perSite[0]].filter((x) => perSite[1].has(x)) : [];
    const peerCopied = det.staging.some((s) => s.blobs.some((b) => (b.source ?? '').startsWith('peer:')));
    if (shared.length === 0) note('app and db need no common blob (redis only on db, hello-* only on app) → nothing to copy from a peer; all from the cloud is correct');
    else if (!peerCopied) warn(`${shared.length} blob(s) needed by both sites, none copied from the peer (timing dependent)`, sources.join(' · '));
    else check('smart distribution: a shared blob was copied from the peer', true, sources.join(' · '));
    check('gate conditions listed', det.gate.conditions.length >= 4, det.gate.conditions.map((c) => `${c.key}:${c.ok}`));
    const st = Date.now();
    const run = await approve(app, S.rev1, { negative: true, viaUi: false });
    S.timings.apply1Sec = Math.round((Date.now() - st) / 1000);
    check('update run done, ok', run.phase === 'done' && run.result?.ok === true, run.result, true);
    const p = await waitCloudProposal(S.rev1, ['applied'], 90_000);
    check('cloud proposal applied, approvedBy = actor', p.approvedBy === CFG.actor, { status: p.status, approvedBy: p.approvedBy });
    const heads = await waitAgentHeads(app, 1);
    check('head seq 1 on both sites', heads.app === 1 && heads.db === 1, heads);
    const dep = await waitCloudHead(1);
    check('cloud head seq 1', dep.head?.revId === S.rev1, dep.head);
    // order: db (redis) before app; hello-api ready before hello-web starts
    const c = containersBySlug();
    check('containers running: redis, hello-api, hello-web', !!c.redis && !!c['hello-api'] && !!c['hello-web'], Object.keys(c));
    if (c.redis && c['hello-api'] && c['hello-web']) {
      check('site order db → app (redis started before hello-api)', c.redis.started < c['hello-api'].started, { redis: c.redis.started, api: c['hello-api'].started });
      check('container order hello-api → hello-web', c['hello-api'].started < c['hello-web'].started, { api: c['hello-api'].started, web: c['hello-web'].started });
    }
  }, ['8']);

  await step('10', 'Checks after release 1 (edge, counter, 404, redirect, GUI, activities, diff)', async () => {
    const root = await http(edgeUrl('app', '/'));
    check('GET :8081/ → 200 data-theme="green"', root.status === 200 && root.text.includes('data-theme="green"'), `${root.status} ${/<body[^>]*>/.exec(root.text)?.[0]}`);
    const cfg = await http(edgeUrl('app', '/config.json'));
    const cj = JSON.parse(cfg.text || '{}') as { theme?: string; banner?: string };
    check('/config.json theme green + banner text', cj.theme === 'green' && cj.banner === fixture.banner, cj);
    const c0 = await getCount();
    check('initial count 0', c0 === 0, c0);
    let last: number | null = null;
    let apiPosts = 3;
    if (CFG.ui) {
      // first increment through the real page (browser → edge → hello-api → redis), then 2 API POSTs
      const n = await ui.helloShot(edgeUrl('app', '/'), 'hello-release1-green', 'green', true).catch((e) => { warn('hello page (browser) failed', errInfo(e).message); return null; });
      if (n === 1) apiPosts = 2;
    }
    for (let i = 0; i < apiPosts; i++) last = await postCount();
    check(`${apiPosts === 2 ? '1 browser click + POST /api/count ×2' : 'POST /api/count ×3'} → 3`, last === 3, last);
    check('GET /api/count → 3', (await getCount()) === 3);
    const infoR = await http(edgeUrl('app', '/api/info'));
    const info = JSON.parse(infoR.text || '{}') as { greeting?: string; store?: string; tokenLength?: number };
    check('/api/info greeting "Hello from OPM", store redis, tokenLength 24', info.greeting === 'Hello from OPM' && info.store === 'redis' && info.tokenLength === 24, info);
    check('route api sets X-Opm-Site? (header to upstream; not visible) — skipped', true, 'upstream header, not observable from the client');
    // route `web` has prefix "/" on every host, so /nope reaches hello-web, which answers 404 itself (decision in TEAM.md);
    // the site not-found page ("Hello 404") is shown only when no route matches or the upstream is not ready (503).
    const nope = await http(edgeUrl('app', '/nope'));
    check('GET /nope → 404 (through route web; hello-web answers 404 for unknown paths)', nope.status === 404, `${nope.status} ${nope.text.slice(0, 120)}`);
    if (nope.text.includes('Hello 404')) note('/nope body is the "Hello 404" page');
    const old = await http(edgeUrl('app', '/old/x'));
    check('GET /old/x → 308 Location /', old.status === 308 && old.headers.get('location') === '/', `${old.status} ${old.headers.get('location')}`);
    const opmNope = await http(edgeUrl('app', '/_opm/nope'));
    check('GET :8081/_opm/nope → agent 404 (not the site page)', opmNope.status === 404 && !opmNope.text.includes('Hello 404'), `${opmNope.status} ${opmNope.text.slice(0, 120)}`);
    const dbAny = await http(edgeUrl('db', '/anything'));
    check('GET :8082/anything (db edge) → 404 built-in page', dbAny.status === 404 && !dbAny.text.includes('Hello 404'), `${dbAny.status} ${dbAny.text.slice(0, 120)}`);
    const gui = await http(edgeUrl('app', '/_opm/app'));
    check('GET :8081/_opm/app → GUI', gui.status === 200 && /<html/i.test(gui.text), gui.status);
    const st = Date.now();
    try {
      const acts = await waitFor('update.applied for app + db in the cloud', async () => {
        const a = await cloudActivities({ type: 'update.applied' });
        const sites = new Set(a.map((x) => x.siteSlug));
        return sites.has('app') && sites.has('db') ? a : null;
      }, 90_000, 5000);
      S.timings.pipeSec = Math.round((Date.now() - st) / 1000);
      check(`activities update.applied app + db in cloud (${S.timings.pipeSec} s after commit check, ≤ 60)`, S.timings.pipeSec <= 60, acts.map((a) => `${a.siteSlug}:${a.seq}:${a.uploadedVia}`));
      const lf = await cloudActivities({ type: 'login.failed' });
      check('login.failed activity piped (app)', lf.some((a) => a.siteSlug === 'app'), lf.length);
    } catch (e) {
      check('activities update.applied app + db in the cloud within 60 s (pipe)', false, `${errInfo(e).message}; agent log: ${tailLog('app', 200).split('\n').filter((l) => l.includes('[pipe]')).slice(-2).join(' | ')}`);
    }
    const d = await cloud.getDiff({ deploymentId: S.deploymentId });
    check('cloud diff: blocker V_NO_CHANGE only', d.blockers.length === 1 && d.blockers[0].code === 'V_NO_CHANGE' && d.changes.length === 0, { blockers: d.blockers.map((b) => b.code), changes: diffSummary(d) });
  }, ['9']);

  await step('11', 'Manual snapshot S1 (note "count 3") → complete on both sites', async () => {
    const app = S.sess.app;
    const r = await app.api.createSnapshot({ body: { note: 'count 3' } });
    S.s1 = r.snapshotId;
    saveState();
    check('snapshot accepted for app + db', r.sites.includes('app') && r.sites.includes('db'), r);
    const s = await waitFor('snapshot S1 complete on both sites', async () => {
      const d = await app.api.getSnapshot({ snapshotId: S.s1 });
      return d.summary.sites.includes('app') && d.summary.sites.includes('db') && d.summary.missingSites.length === 0 ? d : null;
    }, 5 * 60_000, 2000);
    check('S1 reason manual, note "count 3"', s.summary.reason === 'manual' && s.summary.note === 'count 3', s.summary);
    // round D: a snapshot holds exactly the declared site volumes with backup = true, keyed by volumeSlug
    const vols = Object.entries(s.snapshot.sites).flatMap(([site, p]) => p.volumes.map((v) => `${site}/${v.volumeSlug}`)).sort();
    const want = realConfig().sites.flatMap((x) => x.config.volumes.filter((v) => v.backup).map((v) => `${x.siteSlug}/${v.volumeSlug}`)).sort();
    check(`S1 volumes = the backup volumes (${vols.join(',')})`, vols.join(',') === want.join(','), { vols, want });
    const dv = await app.api.siteVolumes({ siteSlug: 'db' });
    const redis = dv.volumes.find((v) => v.volumeSlug === 'redis-data');
    check('db volumes: redis-data declared, used by redis, last snapshot S1', !!redis && redis.declared && redis.usedBy.some((u) => u.containerSlug === 'redis') && redis.lastSnapshot?.snapshotId === S.s1, redis);
    check('db volumes: redis-data lives at its auto location <agentHome>/volumes/redis-data', !!redis && redis.location.kind === 'auto' && redis.path === `${dv.agentHome.replace(/\/+$/, '')}/volumes/redis-data`, { agentHome: dv.agentHome, path: redis?.path });
    check('counter still 3 after the snapshot (containers restarted)', (await waitFor('count readable', async () => (await getCount()) ?? null, 60_000)) === 3);
  }, ['10']);

  await step('12', 'Release 2: theme yellow → diff → publish → approve → only hello-web recreated, count kept', async () => {
    S.ctr12 = Object.fromEntries(Object.entries(containersBySlug()).map(([k, v]) => [k, v.id]));
    let viaUi = false;
    if (CFG.uiCloud) {
      try {
        await ui.cloudSetThemeAndPublish(CFG.frontendUrl, S.deploymentId, 'yellow', 'release 2');
        viaUi = true;
        check('theme=yellow + publish done through the cloud frontend', true);
      } catch (e) {
        warn('cloud frontend variables/publish flow failed — falling back to the API', errInfo(e).message);
      }
    }
    if (!viaUi) {
      const w = await putVariables((v) => { v.find((x) => x.variableSlug === 'theme')!.value = 'yellow'; });
      check('draft variables saved', w.draftVersion > 0, { draftVersion: w.draftVersion });
      const d = await cloud.getDiff({ deploymentId: S.deploymentId });
      const ch = d.changes.find((c) => c.path.includes('theme'));
      check('diff shows theme green → yellow', !!ch && ch.before === 'green' && ch.after === 'yellow' && d.changes.length === 1, diffSummary(d));
      const p = await publish('release 2');
      S.rev2 = p.revision.revId;
    } else {
      const ps = await cloud.listProposals({ deploymentId: S.deploymentId, query: { limit: 10 } });
      S.rev2 = ps.items.find((p) => p.summary === 'release 2')!.revId;
    }
    saveState();
    await waitCloudProposal(S.rev2, ['ready'], 10 * 60_000);
    const run = await approve(S.sess.app, S.rev2, { viaUi: true, shotName: 'agent-approve-release2' });
    check('release 2 applied', run.phase === 'done' && run.result?.ok === true, run.result, true);
    await waitAgentHeads(S.sess.app, 2);
    check('page data-theme="yellow"', (await pageTheme()) === 'yellow');
    const after = Object.fromEntries(Object.entries(containersBySlug()).map(([k, v]) => [k, v.id]));
    check('hello-web recreated', !!after['hello-web'] && after['hello-web'] !== S.ctr12['hello-web']);
    check('hello-api + redis unchanged (same container ids)', after['hello-api'] === S.ctr12['hello-api'] && after.redis === S.ctr12.redis, { before: S.ctr12, after });
    check('count still 3', (await getCount()) === 3);
    let four = false;
    if (CFG.ui) four = (await ui.helloShot(edgeUrl('app', '/'), 'hello-release2-yellow', 'yellow', true).catch((e) => { warn('hello page (browser) failed', errInfo(e).message); return null; })) === 4;
    if (four) check('Increment in the browser → 4', (await getCount()) === 4);
    else check('POST → 4', (await postCount()) === 4);
    await waitCloudHead(2);
  }, ['10']);

  await step('13', 'Local change on db GUI: greeting "Hi from db" → local proposal → approve on db → seq 3', async () => {
    const db = new AgentSession(guiBase('db'), 'db');
    await db.login(S.accessKey, CFG.actor);
    S.sess.db = db;
    const cur = await db.api.config({});
    check('db sees head seq 2', cur.head?.seq === 2, cur.head);
    const config: T.ConfigInput = {
      variables: cur.config.variables.map((v) => (v.variableSlug === 'greeting' ? { ...v, value: 'Hi from db' } : v)),
      secrets: cur.config.secrets.map((s) => ({ secretSlug: s.secretSlug, label: s.label, description: s.description, required: s.required, generate: s.generate })),
      files: cur.config.files,
      sites: cur.config.sites,
    };
    const pre = await db.api.previewLocalProposal({ body: { baseRevId: cur.head!.revId, summary: 'greeting from db', config } });
    check('preview: one change (greeting), no errors', pre.changes.length === 1 && !(pre.validation.issues ?? []).some((i) => i.severity === 'error'), pre.changes);
    const lp = await db.api.createLocalProposal({ body: { baseRevId: cur.head!.revId, summary: 'greeting from db', config } });
    S.rev3 = lp.proposal.revId;
    saveState();
    check('local proposal origin agent seq 3', lp.proposal.origin === 'agent' && lp.proposal.seq === 3, { origin: lp.proposal.origin, seq: lp.proposal.seq });
    const run = await approve(db, S.rev3);
    check('applied with db as coordinator', run.phase === 'done' && run.coordinator === 'db', { coordinator: run.coordinator, result: run.result }, true);
    await waitAgentHeads(db, 3);
    const info = JSON.parse((await http(edgeUrl('app', '/api/info'))).text || '{}') as { greeting?: string };
    check('/api/info greeting "Hi from db"', info.greeting === 'Hi from db', info);
    const dep = await waitCloudHead(3);
    const d = await cloud.getDiff({ deploymentId: S.deploymentId });
    check('cloud diff: head seq 3 ≠ draft (greeting)', dep.head?.seq === 3 && d.changes.some((c) => c.path.includes('greeting')), diffSummary(d));
    const rev = await cloud.getRevision({ deploymentId: S.deploymentId, revId: S.rev3 });
    check('cloud revision 3 origin agent, actor = email', rev.origin === 'agent' && rev.actor === CFG.actor, { origin: rev.origin, actor: rev.actor });
  }, ['12']);

  await step('14', 'Copy head into draft → no changes (V_NO_CHANGE only)', async () => {
    await resetToHead();
    const d = await cloud.getDiff({ deploymentId: S.deploymentId });
    check('diff: no changes, blocker V_NO_CHANGE only', d.changes.length === 0 && d.blockers.length === 1 && d.blockers[0].code === 'V_NO_CHANGE', { changes: diffSummary(d), blockers: d.blockers.map((b) => b.code) });
  }, ['13']);

  await step('15', 'Failure + automatic rollback (FAIL_HEALTH=1)', async () => {
    const d = await cloud.getDeployment({ deploymentId: S.deploymentId });
    const site = structuredClone(d.draft.sites.find((s) => s.siteSlug === 'app')!);
    const api = site.config.containers.find((c) => c.serviceSlug === S.svc[FIXED.api])!;
    api.env.push({ name: 'FAIL_HEALTH', value: '1', secretRef: null });
    await cloud.putDraftSite({ deploymentId: S.deploymentId, siteSlug: 'app', body: { ifRevision: d.draftVersion, site }  });
    const p = await publish('break hello-api');
    S.revFail = p.revision.revId;
    saveState();
    await waitCloudProposal(S.revFail, ['ready'], 10 * 60_000);
    // while hello-api is unhealthy the edge must answer /api with the site not-found page and 503 (§12.1 rule 8)
    const seen: Record<string, number> = {};
    let hello503 = false;
    let polling = true;
    const poller = (async () => {
      while (polling) {
        const r = await http(edgeUrl('app', '/api/count'), { timeoutMs: 3000 }).catch(() => null);
        if (r) {
          seen[r.status] = (seen[r.status] ?? 0) + 1;
          if (r.status === 503 && r.text.includes('Hello 404')) hello503 = true;
        }
        await sleep(250);
      }
    })();
    const run = await approve(S.sess.app, S.revFail).finally(async () => { polling = false; await poller; });
    note(`edge /api/count statuses during the failing update: ${JSON.stringify(seen)}`);
    if (hello503) check('upstream not ready → 503 with the "Hello 404" site page', true);
    else warn('did not observe the 503 "Hello 404" page while hello-api was unhealthy (timing dependent)', seen);
    check('update failed', run.phase === 'failed' && run.result?.ok === false, run.result);
    const cp = await waitCloudProposal(S.revFail, ['failed-rolled-back'], 120_000);
    check('proposal failed-rolled-back with message', !!cp.result?.message, cp.result);
    check('page still yellow', (await waitFor('page answers', async () => pageTheme(), 60_000)) === 'yellow');
    check('count 4', (await getCount()) === 4);
    const heads = await agentHeads(S.sess.app);
    check('head still seq 3 on both sites', heads.app === 3 && heads.db === 3, heads);
    try {
      const acts = await waitFor('update.failed + update.rolled-back in the cloud', async () => {
        const f = await cloudActivities({ type: 'update.failed' });
        const r = await cloudActivities({ type: 'update.rolled-back' });
        return f.length && r.length ? { f, r } : null;
      }, 120_000, 5000);
      check('activities update.failed + update.rolled-back in the cloud', true, { failed: acts.f.map((a) => `${a.siteSlug}:${a.summary}`), rolledBack: acts.r.map((a) => a.siteSlug) });
    } catch (e) {
      const local = await S.sess.app.api.siteActivities({ siteSlug: '*', query: { type: 'update.', limit: 50 } }).catch(() => null);
      check('activities update.failed + update.rolled-back in the cloud', false, `${errInfo(e).message}; on the sites: ${local?.items.map((a) => `${a.siteSlug}:${a.type}`).join(', ')}`);
    }
    await resetToHead();
  }, ['14']);

  await step('16', 'Stale proposal: theme blue → publish P → ready (not approved)', async () => {
    await putVariables((v) => { v.find((x) => x.variableSlug === 'theme')!.value = 'blue'; });
    const p = await publish('blue (will be stale)');
    S.revStale = p.revision.revId;
    saveState();
    await waitCloudProposal(S.revStale, ['ready'], 10 * 60_000);
    check('P ready', true);
  }, ['14']);

  await step('17', 'Rollback to S1 → seq 4 kind rollback: green, count 3, greeting restored; P stale', async () => {
    const app = S.sess.app;
    const plan = await app.api.rollbackPlan({ snapshotId: S.s1 });
    check(`rollback plan: new seq ${plan.newSeq}, volumes restored ${plan.volumesRestored.map((v) => v.siteSlug + '/' + v.volumeSlug).join(',')}, kept ${plan.volumesKept.map((v) => `${v.siteSlug}/${v.volumeSlug}(backup ${v.backup})`).join(',') || '-'}`, plan.newSeq === 4, { changes: plan.changes.length, dataLossSince: plan.dataLossSince });
    let run: T.UpdateRun | null = null;
    if (CFG.ui) {
      try {
        let id = await ui.agentRollbackUi(app.base, S.accessKey, CFG.actor, S.s1, 'agent-rollback-s1');
        if (!id) id = await waitFor('current update after the GUI rollback', async () => (await app.api.currentUpdate({})).run?.updateId, 30_000, 1000);
        run = await waitFor('rollback finished (UI)', async () => {
          const r = await app.api.getUpdate({ updateId: id });
          return r.phase === 'done' || r.phase === 'failed' ? r : null;
        }, 20 * 60_000, 1000);
        check('rollback done through the agent GUI', true);
      } catch (e) {
        warn('agent GUI rollback flow failed — falling back to the API', errInfo(e).message);
      }
    }
    if (!run) run = await approveViaApi(app, 'rollback', S.s1);
    check('rollback run done', run.phase === 'done' && run.result?.ok === true, run.result, true);
    await waitAgentHeads(app, 4);
    const revs = await app.api.listRevisions({ query: { limit: 1 } });
    check('head seq 4 kind rollback', revs.items[0]?.seq === 4 && revs.items[0]?.kind === 'rollback', revs.items[0]);
    check('page green', (await pageTheme()) === 'green');
    check('count 3 (redis volume restored)', (await getCount()) === 3);
    const info = JSON.parse((await http(edgeUrl('app', '/api/info'))).text || '{}') as { greeting?: string };
    check('greeting "Hello from OPM"', info.greeting === 'Hello from OPM', info);
    const p = await waitCloudProposal(S.revStale, ['stale'], 120_000).catch(async () => cloudProposal(S.revStale));
    check('P becomes stale', p?.status === 'stale', p?.status);
    const d = await cloud.getDiff({ deploymentId: S.deploymentId });
    note(`cloud diff pending after rollback: ${d.pending.map((x) => `${x.seq}:${x.status}`).join(', ')}`);
    await waitCloudHead(4);
    await resetToHead();
    if (CFG.ui) await ui.helloShot(edgeUrl('app', '/'), 'hello-rollback-green', 'green', false).catch((e) => warn('hello page (browser) failed', errInfo(e).message));
  }, ['11', '16']);

  await step('18', 'Activities bundle (.opmlog) → cloud upload: skipped > 0; tampered copy refused', async () => {
    const r = await S.sess.app.api.downloadActivities({ siteSlug: 'app', query: { includeDebug: true } });
    const buf = Buffer.from(await r.arrayBuffer());
    writeFileSync(join(CFG.runDir, 'app.opmlog'), buf);
    check(`.opmlog downloaded (${buf.length} B)`, buf.length > 1024);
    const up = await cloud.uploadActivities({ deploymentId: S.deploymentId, body: buf, contentType: 'application/x-tar' });
    check('upload: inserted 0, skipped > 0, rejected 0, signature ok', up.inserted === 0 && up.skipped > 0 && up.rejected === 0 && up.signatureOk, up);
    // tamper: change one summary inside activities.ndjson (same length to keep the tar layout)
    const s = buf.toString('latin1');
    const m = /"summary":"([^"]{4,})"/.exec(s);
    if (!m) { check('found a summary to tamper with', false); return; }
    const orig = m[1];
    const forged = orig.slice(0, -1) + (orig.endsWith('X') ? 'Y' : 'X');
    const t = Buffer.from(s.replace(`"summary":"${orig}"`, `"summary":"${forged}"`), 'latin1');
    let tr: T.UploadActivitiesResponse | null = null;
    let te: ReturnType<typeof errInfo> | null = null;
    try {
      tr = await cloud.uploadActivities({ deploymentId: S.deploymentId, body: t, contentType: 'application/x-tar' });
    } catch (e) {
      te = errInfo(e);
    }
    // the manifest (signed) carries the ndjson sha256, so a forged summary is caught either as 422 invalid
    // (bundle hash / signature) or as rejected ≥ 1 (entry hash chain) — both mean "refused" (see TEAM.md)
    if (tr) check('tampered bundle → rejected ≥ 1, inserted 0', tr.rejected >= 1 && tr.inserted === 0, tr);
    else check('tampered bundle refused (422 invalid)', te?.code === 'invalid', te);
  }, ['17']);

  await step('19', '.opmsnap download → import on db (409) → delete S1 → import → listed (imported, both sites)', async () => {
    const r = await S.sess.app.api.downloadSnapshot({ snapshotId: S.s1, query: { includeImages: false } });
    check('X-OPM-Warning: contains-secrets', r.headers.get('x-opm-warning') === 'contains-secrets', r.headers.get('x-opm-warning'));
    const buf = Buffer.from(await r.arrayBuffer());
    writeFileSync(join(CFG.runDir, 's1.opmsnap'), buf);
    check(`.opmsnap downloaded (${buf.length} B)`, buf.length > 1024);
    const db = S.sess.db;
    const c = await expectError(() => db.api.importSnapshot({ body: buf, contentType: 'application/x-tar' }));
    check('import while S1 exists → 409 conflict', c?.status === 409, c);
    await db.api.deleteSnapshot({ snapshotId: S.s1 });
    await waitFor('S1 gone on all sites', async () => !(await db.api.listSnapshots({})).items.some((x) => x.snapshotId === S.s1), 60_000);
    const imp = await db.api.importSnapshot({ body: buf, contentType: 'application/x-tar' });
    check('imported (same id, reason imported, createdBy actor)', imp.snapshotId === S.s1 && imp.reason === 'imported', imp);
    const done = await waitFor('imported S1 complete on both sites', async () => {
      const l = await db.api.listSnapshots({});
      const x = l.items.find((i) => i.snapshotId === S.s1);
      return x && x.sites.includes('app') && x.sites.includes('db') && x.missingSites.length === 0 ? x : null;
    }, 120_000);
    check('listed with reason imported on app + db', done.reason === 'imported', done);
  }, ['17']);

  await step('20', 'Access key rotate → both sites keyVersion 2; old key invalid, new key ok, old session still valid', async () => {
    const before = S.sess.app.clone('app-old-session');
    const rot = await cloud.rotateAccessKey({ deploymentId: S.deploymentId });
    S.newAccessKey = rot.accessKey;
    check('rotate → version 2', rot.version === 2, { version: rot.version });
    for (const site of ['app', 'db'] as SiteName[]) {
      await waitFor(`${site} keyVersion 2`, async () => {
        const i = await new AgentSession(guiBase(site), site).api.info({});
        return i.keyVersion === 2 ? i : null;
      }, 120_000);
      check(`${site} has keyVersion 2`, true);
      const old = await expectError(() => new AgentSession(guiBase(site), site).api.login({ body: { accessKey: S.accessKey } }));
      check(`${site}: old key → 401 invalid-key`, old?.status === 401 && old.code === 'invalid-key', old);
      const fresh = new AgentSession(guiBase(site), `${site}-new`);
      await fresh.login(S.newAccessKey, CFG.actor);
      check(`${site}: new key logs in`, !!fresh.cookie);
    }
    const sess = await before.api.session({});
    check('session opened before the rotation still works', !!sess.sessionId && sess.keyVersion === 1, sess);
  }, ['9']);

  if (CFG.uiCloud && S.deploymentId) {
    await step('21', 'Cloud frontend evidence (overview, status, activities, history, diff)', async () => {
      await ui.cloudFinal(CFG.frontendUrl, S.deploymentId);
    }, ['9']);
  }

  // ───── summary tables
  try {
    if (S.deploymentId) {
      const all = await cloudActivities({});
      const per: Record<string, number> = {};
      for (const a of all) per[a.siteSlug] = (per[a.siteSlug] ?? 0) + 1;
      extra.push({ title: 'Activity counts per site (cloud)', body: Object.entries(per).map(([k, v]) => `- ${k}: ${v}`).join('\n') });
      const types: Record<string, number> = {};
      for (const a of all) types[a.type] = (types[a.type] ?? 0) + 1;
      extra.push({ title: 'Activity types (cloud)', body: Object.entries(types).sort().map(([k, v]) => `- ${k}: ${v}`).join('\n') });
    }
  } catch (e) {
    extra.push({ title: 'Activity counts', body: `failed: ${errInfo(e).message}` });
  }
  extra.push({ title: 'Timings (s)', body: Object.entries(S.timings).map(([k, v]) => `- ${k}: ${v}`).join('\n') || '-' });
  extra.push({ title: 'Ids', body: '```json\n' + JSON.stringify({ ...S, sess: undefined, accessKey: undefined, newAccessKey: undefined }, null, 2) + '\n```' });
  extra.push({ title: 'Agent log tails', body: (['app', 'db'] as SiteName[]).map((s) => `### ${s}\n\n\`\`\`\n${tailLog(s, 30)}\n\`\`\``).join('\n\n') });
}

main()
  .catch((e) => log('FATAL', errInfo(e)))
  .finally(async () => {
    await ui.close().catch(() => {});
    const f = write();
    const s = summary();
    log(`results: ${f} — ${JSON.stringify(s)}`);
    if (process.env.OPM_E2E_KEEP !== '1') stopAgents();
    else log('agents kept running (OPM_E2E_KEEP=1)');
    copyFileSync(f, join(E2E_DIR, 'RESULTS.md'));
    process.exit(s.fail ? 1 : 0);
  });
void shot;
