/**
 * VM end-to-end test of opm-cloud (SPEC §21 #108): the selfhost Docker stack (local adapter: SQLite + files + built-in
 * auth) at OPM_VM_BASE (default http://127.0.0.1:8790) with a REAL agent (agent/dist binary downloaded through
 * /v1/install/binary) on this Mac (Colima Docker). Started by e2e/vm/run-vm-e2e.sh; results → e2e/vm/RESULTS.md.
 * Every run uses new labels (template, deployment, service, account, mirror repo), so re-runs work on the same stack.
 */
import { execFileSync, spawn, type ChildProcess } from 'node:child_process';
import { DatabaseSync } from 'node:sqlite';
import { randomBytes } from 'node:crypto';
import { chmodSync, existsSync, mkdirSync, openSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import { join } from 'node:path';
import { createCloudClient, type CloudClient } from '../../shared/dist/cloud-api.js';
import { OpmApiError, UPLOAD_PART_SIZE, configBlobs, type Config, type ConfigInput, type SecretDefInput, type Transfer } from '../../shared/dist/index.js';
import { FakeGithub } from '../../backend/test/fake-github.ts';
import { AgentSession, HERE, ROOT, RUN, check, checks, compose, docker, freePort, log, must, opmCloud, sh, sha256Hex, sleep, step, uniq, waitFor } from './lib.ts';

const API = (process.env.OPM_VM_BASE ?? 'http://127.0.0.1:8790').replace(/\/$/, '');
const ADMIN = JSON.parse(readFileSync(join(ROOT, 'e2e/run/vm-admin.json'), 'utf8')) as { email: string; password: string };
const RUN_ID = new Date().toISOString().slice(5, 16).replace(/[-T:]/g, '') + randomBytes(2).toString('hex'); // MMDDHHMM + 4 hex
const SITE = 'web';
const SITE_DIR = join(RUN, `${SITE}-${RUN_ID}`);
const ACTOR = 'vm-e2e@opm.local';
const IMAGE_REF = process.env.OPM_VM_IMAGE ?? 'docker.io/library/nginx:alpine';
const PLATFORM = 'linux/arm64';
const STATE_FILE = join(RUN, 'state.json');
const t0 = Date.now();

type State = { runs: { runId: string; deploymentId: string | null; agentPid: number | null; at: string }[] };
const loadState = (): State => (existsSync(STATE_FILE) ? (JSON.parse(readFileSync(STATE_FILE, 'utf8')) as State) : { runs: [] });
const state = loadState();
const me: State['runs'][number] = { runId: RUN_ID, deploymentId: null, agentPid: null, at: new Date().toISOString() };
state.runs.push(me);
const saveState = () => writeFileSync(STATE_FILE, JSON.stringify(state, null, 2) + '\n');
saveState();

// ───────────────────────────── shared run state
const S = {
  adminToken: '',
  publicUrl: '',
  releasePub: '',
  distVersion: '',
  serviceSlug: '',
  templateId: '',
  deploymentId: '',
  accessKey: '',
  ports: { agent: 0, gui: 0, edge: 0 },
  agent: null as ChildProcess | null,
  agentBin: '',
  gui: null as AgentSession | null,
  html1: '',
  html2: '',
};
let admin: CloudClient;

async function login(email: string, password: string): Promise<{ status: number; idToken: string | null; body: unknown }> {
  const r = await fetch(`${API}/auth/login`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ email, password }) });
  const j = (await r.json().catch(() => null)) as { idToken?: string } | null;
  return { status: r.status, idToken: j?.idToken ?? null, body: j };
}
const bearerClient = (tok: string) => createCloudClient(API, () => `Bearer ${tok}`);
async function getText(url: string, init: RequestInit = {}) {
  const r = await fetch(url, { redirect: 'manual', ...init });
  return { status: r.status, headers: r.headers, text: await r.text() };
}
async function getBytes(url: string, init: RequestInit = {}) {
  const r = await fetch(url, { redirect: 'manual', ...init });
  return { status: r.status, headers: r.headers, body: Buffer.from(await r.arrayBuffer()) };
}
const errOf = async (p: Promise<unknown>): Promise<OpmApiError | null> => {
  try {
    await p;
    return null;
  } catch (e) {
    if (e instanceof OpmApiError) return e;
    throw e;
  }
};
function depInput(d: Awaited<ReturnType<CloudClient['getDeployment']>>): ConfigInput {
  return {
    variables: d.draft.variables,
    files: d.draft.files,
    sites: d.draft.sites,
    secrets: d.draft.secrets.map(({ value: _v, isSet: _i, rotatedAt: _r, ...x }) => (void _v, void _i, void _r, x as SecretDefInput)),
  } as ConfigInput;
}
const agentAlive = () => !!S.agent && S.agent.exitCode === null && S.agent.signalCode === null;
const agentLogTail = (n = 30) => {
  try {
    return readFileSync(join(SITE_DIR, 'agent.log'), 'utf8').split('\n').slice(-n).join('\n');
  } catch {
    return '';
  }
};
function opmContainers(dep: string): { name: string; state: string; image: string; container: string }[] {
  const ids = docker(['ps', '-aq', '--filter', `label=opm.deployment=${dep}`]).out.split('\n').filter(Boolean);
  if (!ids.length) return [];
  const j = JSON.parse(docker(['inspect', ...ids]).out) as { Name: string; State: { Status: string }; Config: { Image: string; Labels: Record<string, string> } }[];
  return j.map((c) => ({ name: c.Name.replace(/^\//, ''), state: c.State.Status, image: c.Config.Image, container: c.Config.Labels['opm.container'] ?? '' }));
}
function removeDeploymentDocker(dep: string) {
  const ids = docker(['ps', '-aq', '--filter', `label=opm.deployment=${dep}`], { allowFail: true }).out.split('\n').filter(Boolean);
  if (ids.length) docker(['rm', '-f', ...ids], { allowFail: true });
  const nets = docker(['network', 'ls', '--format', '{{.Name}}', '--filter', `name=opm_${dep}_`], { allowFail: true }).out.split('\n').filter((n) => n.startsWith(`opm_${dep}_`));
  for (const n of nets) docker(['network', 'rm', n], { allowFail: true });
  return { containers: ids.length, networks: nets.length };
}
function killPid(pid: number | null) {
  if (!pid) return false;
  try {
    process.kill(pid, 0);
  } catch {
    return false;
  }
  try {
    process.kill(pid, 'SIGTERM');
    return true;
  } catch {
    return false;
  }
}
async function waitRun(gui: AgentSession, updateId: string, what: string) {
  return waitFor(`${what} (update ${updateId}) to finish`, async () => {
    const r = await gui.api.getUpdate({ updateId });
    return r.phase === 'done' || r.phase === 'failed' ? r : null;
  }, 300_000, 1500);
}
/** compare the cloud head with this site, take every pickable item from the cloud, preview, merge, wait */
async function mergeCloudHead(gui: AgentSession, cloudHead: string, message: string) {
  await waitFor(`agent sees cloud head ${cloudHead}`, async () => (await gui.api.branchState({})).cloud.head?.commitId === cloudHead, 120_000, 2000);
  const plan = await gui.api.compareLocal({ body: { source: { cloudCommitId: cloudHead } } });
  const choices = plan.items.filter((i) => !i.locked).map((i) => ({ key: i.key, take: 'source' as const }));
  check(`compare: plan from cloud commit ${plan.source.commit?.seq} (${plan.items.length} items, fastForward ${plan.fastForward})`, plan.source.commit?.commitId === cloudHead && plan.items.length > 0, { counts: plan.counts, locked: plan.items.filter((i) => i.locked).map((i) => i.key) });
  const pv = await gui.api.previewLocalMerge({ body: { sourceCommitId: cloudHead, targetHead: plan.target.head?.commitId ?? null, choices } });
  check('merge preview: no validation errors, nothing unresolved', pv.validation.errors.length === 0 && pv.unresolved.length === 0, { errors: pv.validation.errors, unresolved: pv.unresolved, choiceErrors: pv.choiceErrors, runtime: pv.runtime });
  const run = await gui.api.mergeLocal({ body: { sourceCommitId: cloudHead, targetHead: plan.target.head?.commitId ?? null, choices, message } });
  const done = await waitRun(gui, run.updateId, 'merge');
  must(`merge run ${run.updateId} done + ok`, done.phase === 'done' && !!done.result?.ok, { phase: done.phase, result: done.result, sites: done.sites });
  const st = await gui.api.branchState({});
  check('agent branch: merged from the cloud head', st.local.mergedFrom?.commitId === cloudHead, { local: st.local.head, mergedFrom: st.local.mergedFrom });
  return { plan, run: done, state: st };
}

function siteConfig(): Config['sites'][number] {
  return {
    siteSlug: SITE,
    label: 'Web site',
    config: {
      host: null,
      agentPort: S.ports.agent,
      agentGuiAliasPort: S.ports.gui,
      depends: [],
      notFoundPageId: null,
      edge: {
        httpPort: S.ports.edge,
        httpsPort: null,
        maxBodyBytes: null,
        hosts: [{ hostname: '*', tls: { mode: 'none' }, redirectHttpToHttps: false, responseHeaders: { set: {}, add: {}, remove: [], always: [] } }],
        routes: [
          {
            routeId: 'web',
            hosts: [],
            match: { kind: 'prefix', path: '/', stopRegex: false, methods: null },
            action: { type: 'proxy', containerSlug: 'nginx', portName: 'http', stripPrefix: false, rewrite: null, addPrefix: null, setHeaders: { 'X-Opm-Site': '{{ self.slug }}' }, removeHeaders: [], websocket: false, connectTimeoutSec: 60, sendTimeoutSec: 60, readTimeoutSec: 30, requestBuffering: false, responseBuffering: false, maxBodyBytes: null },
            order: 1,
            responseHeaders: null,
          },
        ],
      },
      volumes: [],
      containers: [
        {
          containerSlug: 'nginx',
          serviceSlug: S.serviceSlug,
          version: 'v1',
          enabled: true,
          env: [],
          ports: [{ portName: 'http', hostPort: null, hostIp: '0.0.0.0', edge: true }],
          volumes: [{ imageVolume: null, mountPath: '/usr/share/nginx/html/index.html', source: { kind: 'file', fileSlug: 'index' }, readOnly: true }],
          command: null,
          entrypoint: null,
          restart: 'always',
          resources: { cpus: null, memoryMb: 64 },
          healthcheck: null,
          depends: [],
        },
      ],
    },
  } as unknown as Config['sites'][number];
}

// ═════════════════════════════════════════════════════════════ steps
async function main() {
  log(`VM e2e run ${RUN_ID} against ${API}; site dir ${SITE_DIR}`);
  mkdirSync(SITE_DIR, { recursive: true });

  await step('0', 'preflight + previous runs cleaned', [], async () => {
    for (const r of state.runs.filter((x) => x.runId !== RUN_ID)) {
      if (killPid(r.agentPid)) log(`killed agent of run ${r.runId} (pid ${r.agentPid})`);
      if (r.deploymentId) {
        const n = removeDeploymentDocker(r.deploymentId);
        if (n.containers || n.networks) log(`removed ${n.containers} containers / ${n.networks} networks of run ${r.runId} (${r.deploymentId})`);
      }
    }
    state.runs = state.runs.filter((x) => x.runId === RUN_ID).concat(state.runs.filter((x) => x.runId !== RUN_ID).slice(-5).map((x) => ({ ...x, agentPid: null })));
    saveState();
    must('docker (Colima) reachable', docker(['info', '--format', '{{.ServerVersion}}'], { allowFail: true }).code === 0);
    S.publicUrl = compose(['exec', '-T', 'opm-cloud', 'printenv', 'OPM_PUBLIC_URL']).out;
    check('test hooks on (e2e compose override)', compose(['exec', '-T', 'opm-cloud', 'printenv', 'OPM_TEST_HOOKS'], { allowFail: true }).out === '1');
    const rk = JSON.parse(opmCloud(['release-key']).out.split('\n').pop()!) as { publicKeyB64: string; created: boolean };
    S.releasePub = rk.publicKeyB64;
    check('release key exists in the data dir (created by run-vm-e2e.sh)', !rk.created, rk);
    S.distVersion = (JSON.parse(readFileSync(join(ROOT, 'agent/dist/release.json'), 'utf8')) as { version: string }).version;
    const latest = compose(['exec', '-T', 'opm-cloud', 'cat', '/data/blobs/agent/latest'], { allowFail: true }).out.trim();
    check(`agent/latest in the VM blob store = agent/dist version ${S.distVersion}`, latest === S.distVersion, latest);
    for (const k of ['agent', 'gui', 'edge'] as const) S.ports[k] = await freePort();
    log('ports', S.ports);
  });

  await step('1', 'health, /opm-config.json, frontend + SPA fallback', [], async () => {
    const h = await getText(`${API}/healthz`);
    const hj = JSON.parse(h.text) as { ok: boolean; adapter: string; version: string };
    must('GET /healthz → ok, adapter local', h.status === 200 && hj.ok && hj.adapter === 'local', hj);
    const c = await getText(`${API}/opm-config.json`);
    const cj = JSON.parse(c.text) as { auth: { provider: string; passwordReset: boolean } };
    check('/opm-config.json auth.provider = local (no-store)', cj.auth.provider === 'local' && /no-store/.test(c.headers.get('cache-control') ?? ''), cj);
    const root = await getText(`${API}/`);
    check('GET / → 200 html (the console build)', root.status === 200 && /text\/html/.test(root.headers.get('content-type') ?? '') && /<html/i.test(root.text) && /_app\//.test(root.text), `${root.status} ${root.headers.get('content-type')}`);
    const spa = await getText(`${API}/app/deployments`);
    check('GET /app/deployments → SPA fallback = index.html', spa.status === 200 && spa.text === root.text, `${spa.status} same=${spa.text === root.text}`);
    const asset = /src="(\/_app\/immutable\/[^"]+\.js)"|href="(\/_app\/immutable\/[^"]+)"/.exec(root.text);
    if (asset) {
      const a = await getText(`${API}${asset[1] ?? asset[2]}`);
      check('immutable asset served with long caching', a.status === 200 && /immutable/.test(a.headers.get('cache-control') ?? ''), `${asset[1] ?? asset[2]} ${a.status} ${a.headers.get('cache-control')}`);
    }
    const me = await getText(`${API}/v1/me`);
    check('GET /v1/me without a token → 401', me.status === 401, me.status);
    const missing = await getText(`${API}/v1/nope-${RUN_ID}`, { headers: {} });
    check('unknown /v1 route is not the SPA (JSON error)', missing.status >= 400 && !/<html/i.test(missing.text), `${missing.status} ${missing.text.slice(0, 80)}`);
  });

  await step('2', 'accounts: admin login, second account, set / reveal password, reset link + confirm, disable', [], async () => {
    const a = await login(ADMIN.email, ADMIN.password);
    must('admin login /auth/login → idToken', a.status === 200 && !!a.idToken, a.status);
    S.adminToken = a.idToken!;
    admin = bearerClient(S.adminToken);
    const meA = await admin.getMe({});
    check('admin getMe', meA.account.email === ADMIN.email, meA.account.email);
    const email = `vm-${RUN_ID}@opm.local`;
    const cr = await admin.createAccount({ body: { name: `VM e2e ${RUN_ID}`, email, access: [] } });
    const uid = cr.account.uid;
    check('createAccount → generated password (RevealValue)', typeof cr.value === 'string' && cr.value.length >= 12, { uid, hideAfterSec: cr.hideAfterSec });
    const l1 = await login(email, cr.value);
    check('second account logs in with the generated password', l1.status === 200 && !!l1.idToken, l1.status);
    const u1 = bearerClient(l1.idToken!);
    check('second account getMe = its email', (await u1.getMe({})).account.email === email);
    const rv = await admin.revealAccountPassword({ uid });
    check('revealAccountPassword = the generated password', rv.value === cr.value);
    // revocation has second precision (iat < validSinceSec, as Firebase tokensValidAfterTime): leave the login's second
    await sleep(1100);
    const sp = await admin.setAccountPassword({ uid });
    check('setAccountPassword → a new password', sp.value !== cr.value && sp.value.length >= 16);
    const old = await login(email, cr.value);
    check('old password refused after Set new password', old.status >= 400 && !old.idToken, old.status);
    const l2 = await login(email, sp.value);
    check('new password logs in', l2.status === 200 && !!l2.idToken, l2.status);
    const sess1 = await getText(`${API}/v1/me`, { headers: { Authorization: `Bearer ${l1.idToken}` } });
    check('Set new password ended the earlier session (old ID token → 401)', sess1.status === 401, sess1.status);
    const rl = await admin.resetAccountPassword({ uid });
    const link = rl.value;
    const expectPrefix = `${S.publicUrl.replace(/\/$/, '')}/reset-password?token=`;
    check(`reset link = <publicUrl>/reset-password?token=… (${S.publicUrl})`, link.startsWith(expectPrefix) && rl.passwordSetLink === link, link.replace(/token=.*/, 'token=…'));
    const token = new URL(link).searchParams.get('token')!;
    const ck = await getText(`${API}/auth/reset/check?token=${encodeURIComponent(token)}`);
    check('/auth/reset/check → { email }', ck.status === 200 && (JSON.parse(ck.text) as { email: string }).email === email, ck.status);
    const weak = await fetch(`${API}/auth/reset/confirm`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token, password: 'x' }) });
    check('/auth/reset/confirm with a weak password → 400', weak.status === 400, weak.status);
    const newPw = `Vm-e2e-${randomBytes(9).toString('base64url')}`;
    await sleep(1100); // see above: the reset must be in a later second than the l2 login
    const cf = await fetch(`${API}/auth/reset/confirm`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token, password: newPw }) });
    check('/auth/reset/confirm → 204', cf.status === 204, cf.status);
    const l3 = await login(email, newPw);
    check('login with the password chosen on the reset page', l3.status === 200 && !!l3.idToken, l3.status);
    const ck2 = await getText(`${API}/auth/reset/check?token=${encodeURIComponent(token)}`);
    check('used reset token → 410', ck2.status === 410, ck2.status);
    const s2 = await getText(`${API}/v1/me`, { headers: { Authorization: `Bearer ${l2.idToken}` } });
    check('reset ended the session from before the reset (→ 401)', s2.status === 401, s2.status);
    const dis = await admin.disableAccount({ uid, body: { disabled: true } });
    check('disableAccount → disabled', dis.disabled === true);
    const s3 = await getText(`${API}/v1/me`, { headers: { Authorization: `Bearer ${l3.idToken}` } });
    check('disabled account: its ID token is rejected', s3.status === 401 || s3.status === 403, `${s3.status} ${s3.text.slice(0, 120)}`);
    const l4 = await login(email, newPw);
    check('disabled account cannot log in', !l4.idToken && l4.status >= 400, `${l4.status} ${JSON.stringify(l4.body).slice(0, 120)}`);
    const rf = await fetch(`${API}/auth/refresh`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ refreshToken: (l3.body as { refreshToken?: string }).refreshToken }) });
    check('disabled account: refresh token refused', rf.status >= 400, rf.status);
  });

  await step('3', `catalog: registry, service, real import of ${IMAGE_REF} (${PLATFORM}) via the outbox`, ['2'], async () => {
    const reg = await admin.createRegistry({ body: { label: `Docker Hub ${RUN_ID}`, url: 'https://registry-1.docker.io', username: '', password: '' } });
    const tr = await admin.testRegistry({ registryId: reg.registryId });
    check('registry created (anonymous Docker Hub) + test', !!reg.registryId && tr.ok, tr);
    S.serviceSlug = `vm-nginx-${RUN_ID.slice(-6)}`.toLowerCase();
    const svc = await admin.createService({ body: { label: `VM nginx ${RUN_ID}`, serviceSlug: S.serviceSlug, description: 'VM e2e' } });
    check('service created', svc.serviceSlug === S.serviceSlug);
    const ins = await admin.inspectImage({ serviceSlug: S.serviceSlug, body: { reference: IMAGE_REF, registryId: null, platform: PLATFORM } });
    check('inspect (public, no registry)', /^sha256:[0-9a-f]{64}$/.test(ins.digest) && ins.layers > 0, { digest: ins.digest, layers: ins.layers, sizeBytes: ins.sizeBytes, ports: ins.ports });
    const logSince = new Date().toISOString();
    const img = await admin.registerImage({
      serviceSlug: S.serviceSlug,
      body: { version: 'v1', reference: IMAGE_REF, registryId: null, platform: PLATFORM, mode: 'copy', kind: 'service', entrypoint: ins.entrypoint, command: ins.command, workdir: ins.workdir, user: ins.user, env: ins.env, volumes: ins.volumes, ports: [{ name: 'http', port: 80, protocol: 'tcp', description: 'HTTP' }], healthcheck: null, description: 'VM e2e' },
    });
    check('registerImage → import pending', img.import.status === 'pending' || img.import.status === 'copying', img.import.status);
    const t = Date.now();
    const done = await waitFor('image import ready', async () => {
      const g = await admin.getImage({ serviceSlug: S.serviceSlug, version: 'v1' });
      return g.import.status === 'ready' || g.import.status === 'failed' ? g : null;
    }, 600_000, 2000);
    must(`import ready in ${Math.round((Date.now() - t) / 1000)} s`, done.import.status === 'ready', done.import.error);
    check('import digest = inspected digest', done.digest === ins.digest, { digest: done.digest });
    const objs = [done.digest!, done.import.configDigest!, ...done.import.layers.map((l) => l.digest)];
    const sizes = compose(['exec', '-T', 'opm-cloud', 'sh', '-c', objs.map((d) => `stat -c '%s' /data/blobs/blobs/sha256/${d.slice(7)} 2>/dev/null || echo missing`).join('; ')]).out.split('\n');
    const layerOk = done.import.layers.every((l, i) => Number(sizes[i + 2]) === l.sizeBytes);
    check(`manifest + config + ${done.import.layers.length} layers in the VM blob store (sizes match)`, !sizes.includes('missing') && layerOk, { sizes, layers: done.import.layers.map((l) => l.sizeBytes) });
    const logs = compose(['logs', '--since', logSince, '--no-log-prefix', 'opm-cloud'], { allowFail: true }).out;
    // registerImage only writes the image doc; the copy runs in the importImage trigger, delivered by the outbox dispatcher
    const imported = logs.split('\n').find((l) => l.includes('"image imported"') && l.includes(S.serviceSlug));
    check('server log: "image imported" by the outbox-delivered importImage trigger', !!imported, (imported ?? logs.split('\n').filter((l) => /import|outbox/i.test(l)).slice(0, 3).join(' | ')).slice(0, 300));
  });

  await step('4', 'template (one site, nginx + an edge route, a file) + commit via a stage; deployment + commit', ['3'], async () => {
    S.html1 = `<!doctype html><html><body><h1 id="greet">{{ var.greeting }}</h1><p id="ver">content-v1 ${RUN_ID}</p></body></html>\n`;
    const bytes1 = Buffer.from(S.html1);
    const sha1 = `sha256:${sha256Hex(bytes1)}`;
    const config: ConfigInput = {
      variables: [{ variableSlug: 'greeting', label: 'Greeting', description: '', type: 'string', value: 'Hello from the template', required: false, options: null, validation: null }],
      secrets: [],
      files: [{ fileSlug: 'index', label: 'Index page', description: 'nginx index.html', mime: 'text/html', editor: 'text', maxBytes: 65536, content: { sha256: sha1, sizeBytes: bytes1.length }, render: 'expressions', location: { kind: 'auto' } }],
      sites: [siteConfig()],
    } as unknown as ConfigInput;
    const tpl = await admin.createTemplate({ body: { label: `VM e2e ${RUN_ID}`, description: 'opm-cloud VM e2e' } });
    S.templateId = tpl.templateId;
    const stage = await admin.createStage({ body: { blobs: configBlobs(config) } });
    check('createStage → the new file is missing from the store', stage.missing.length === 1 && stage.missing[0]!.sha256 === sha1, stage);
    const pb = await admin.putStageBlob({ stageId: stage.stageId, sha256: sha1, body: bytes1 });
    check('putStageBlob → staged', pb.staged === true && pb.sizeBytes === bytes1.length, pb);
    const tc = await admin.commitTemplate({ templateId: S.templateId, body: { message: `vm e2e ${RUN_ID}: nginx + index`, baseCommitId: tpl.branch?.head?.commitId ?? null, config, stageId: stage.stageId } });
    must('template commit', !!tc.commitId, { seq: tc.seq, kind: tc.kind });
    const tv = await admin.getTemplate({ templateId: S.templateId });
    check('template head = the commit', tv.branch?.head?.commitId === tc.commitId);
    const cd = await admin.createDeployment({ body: { label: `VM e2e ${RUN_ID}`, templateId: S.templateId, tagSlugs: [], managerAccountUids: [] } });
    S.deploymentId = cd.deployment.deploymentId;
    S.accessKey = cd.accessKey;
    me.deploymentId = S.deploymentId;
    saveState();
    check('deployment from the template → access key', /^[A-Z2-7]{4}(-[A-Z2-7]{4}){5}$/.test(cd.accessKey), cd.accessKey.replace(/[A-Z2-7]/g, 'X'));
    const d0 = await admin.getDeployment({ deploymentId: S.deploymentId });
    must('deployment has a head commit (from the template)', !!d0.branch?.head?.commitId, d0.branch?.head);
    const input = depInput(d0);
    input.variables.find((v) => v.variableSlug === 'greeting')!.value = `Hello VM ${RUN_ID}`;
    const dc = await admin.commitDeployment({ deploymentId: S.deploymentId, body: { message: `vm e2e ${RUN_ID}: greeting`, baseCommitId: d0.branch!.head!.commitId, config: input } });
    check('deployment commit (variable change) on the head', dc.parentId === d0.branch!.head!.commitId && dc.kind === 'change', { seq: dc.seq, kind: dc.kind });
    const list = await admin.listDeploymentCommits({ deploymentId: S.deploymentId, query: { limit: 10 } });
    check('deployment commits listed (head first)', list.items[0]?.commitId === dc.commitId, list.items.map((c) => `${c.seq}:${c.kind}`));
  });

  await step('5', 'real agent: install token, binary from the VM BlobStore (sha256 + signature), enroll, online', ['4'], async () => {
    const tk = await admin.createInstallToken({ deploymentId: S.deploymentId, body: { siteSlug: SITE } });
    check('install token + command (header form, this server)', /^opi_[A-Za-z0-9_-]{43}$/.test(tk.token) && tk.command.includes('/v1/install/script') && tk.command.includes(`Bearer ${tk.token}`), tk.command.replace(tk.token, 'opi_…'));
    const H = { Authorization: `Bearer ${tk.token}` };
    const scr = await getText(`${API}/v1/install/script`, { headers: H });
    check('install script embeds the data-dir release public key', scr.status === 200 && scr.text.startsWith('#!/bin/sh') && scr.text.includes(S.releasePub), `${scr.status} key=${scr.text.includes(S.releasePub)}`);
    const rel = JSON.parse((await getText(`${API}/v1/install/release?os=darwin&arch=arm64`, { headers: H })).text) as { version: string; sha256: string; signature: string; releasePublicKey: string; sizeBytes?: number };
    check('install/release darwin-arm64 = the uploaded dist version, release key of this VM', rel.version === S.distVersion && rel.releasePublicKey === S.releasePub, { version: rel.version, sizeBytes: rel.sizeBytes });
    const first = await getBytes(`${API}/v1/install/binary?os=darwin&arch=arm64`, { headers: H });
    let bin = first.body;
    const loc = first.headers.get('location');
    if (first.status === 302 && loc) {
      check('install/binary → 302 to a signed /_blob URL (OPM_BLOB_REDIRECT=1)', /\/_blob\/agent\/.+\?exp=\d+&sig=/.test(loc), loc.replace(/sig=[^&]+/, 'sig=…'));
      bin = (await getBytes(new URL(loc, API).toString())).body;
    } else check('install/binary → 200 stream', first.status === 200, first.status);
    const got = sha256Hex(bin);
    const local = sha256Hex(readFileSync(join(ROOT, 'agent/dist/opm-agent-darwin-arm64')));
    must('downloaded binary sha256 = release sha256 = agent/dist build', got === rel.sha256 && got === local, { got, rel: rel.sha256 });
    mkdirSync(join(SITE_DIR, 'bin'), { recursive: true });
    S.agentBin = join(SITE_DIR, 'bin/opm-agent');
    writeFileSync(S.agentBin, bin);
    chmodSync(S.agentBin, 0o755);
    const v = sh(S.agentBin, ['verify-release', '--pubkey', S.releasePub, '--sha256', got, '--sig', rel.signature], { allowFail: true });
    check('opm-agent verify-release: signature valid for the VM release key', v.code === 0, v.out);
    const badSig = rel.signature.slice(0, -2) + (rel.signature.endsWith('AA') ? 'BB' : 'AA');
    const vb = sh(S.agentBin, ['verify-release', '--pubkey', S.releasePub, '--sha256', got, '--sig', badSig], { allowFail: true });
    check('verify-release rejects a tampered signature', vb.code !== 0, vb.out.slice(0, 120));
    // agent.json (unprivileged macOS run; data dir under $HOME for Colima bind mounts)
    const tokenFile = join(SITE_DIR, 'install-token');
    writeFileSync(tokenFile, tk.token, { mode: 0o600 });
    const agentCfg = {
      apiBase: API,
      deploymentId: S.deploymentId,
      siteSlug: SITE,
      dataDir: join(SITE_DIR, 'data'),
      agentPort: S.ports.agent,
      guiAliasPort: S.ports.gui,
      edgeHttpPort: S.ports.edge,
      advertiseHost: '127.0.0.1',
      dockerHost: process.env.DOCKER_HOST || `unix://${process.env.HOME}/.colima/default/docker.sock`,
      installTokenFile: tokenFile,
      containerNames: 'prefixed',
      releasePublicKey: S.releasePub,
    };
    writeFileSync(join(SITE_DIR, 'agent.json'), JSON.stringify(agentCfg, null, 2) + '\n');
    const out = openSync(join(SITE_DIR, 'agent.log'), 'a');
    S.agent = spawn(S.agentBin, ['run', '--config', join(SITE_DIR, 'agent.json')], { env: { ...process.env, DOCKER_HOST: agentCfg.dockerHost, OPM_APPROVE_WAIT_SEC: '3', OPM_LOG_LEVEL: 'debug' }, stdio: ['ignore', out, out], detached: true });
    S.agent.on('exit', (c, s) => log(`agent exited code=${c} signal=${s}`));
    me.agentPid = S.agent.pid ?? null;
    saveState();
    log(`agent pid ${S.agent.pid}, ports`, S.ports);
    const ts = Date.now();
    const st = await waitFor('site web enrolled + online in the cloud', async () => {
      if (!agentAlive()) throw new Error(`agent died:\n${agentLogTail()}`);
      const s = await admin.getStatus({ deploymentId: S.deploymentId });
      const w = s.sites.find((x) => x.siteSlug === SITE);
      return w?.online ? w : null;
    }, 120_000, 2000);
    check(`enrolled + online in ${Math.round((Date.now() - ts) / 1000)} s`, !!st.online, { agentVersion: st.agentVersion, enrolled: st.enrolled });
    check('agent reports the downloaded version', st.agentVersion === S.distVersion, st.agentVersion);
    check('install token file removed after enroll', !existsSync(tokenFile));
    const setup = await admin.getSetup({ deploymentId: S.deploymentId });
    const ss = setup.sites.find((x) => x.siteSlug === SITE);
    check('Setup lists the agent + pinned release', !!ss?.agent && setup.agentRelease?.version === S.distVersion, { agent: !!ss?.agent, agentRelease: setup.agentRelease?.version, apiBase: setup.apiBase, installDeps: setup.installDeps });
  });

  await step('6', 'the deployment commit reaches the agent → merge + apply via the agent GUI API → container + edge route', ['5'], async () => {
    S.gui = new AgentSession(`http://127.0.0.1:${S.ports.gui}`);
    const info = await S.gui.api.info({});
    check('agent GUI /_opm/api/info: enrolled + access key', info.enrolled && info.hasAccessKey && info.deploymentId === S.deploymentId, info);
    const wrong = await errOf(S.gui.api.login({ body: { accessKey: 'AAAA-AAAA-AAAA-AAAA-AAAA-AAAA' } }));
    check('wrong access key refused', wrong?.code === 'invalid-key', wrong?.code);
    await S.gui.login(S.accessKey, ACTOR);
    check('login with the deployment access key + actor', !!S.gui.actorToken);
    const head = (await admin.getDeployment({ deploymentId: S.deploymentId })).branch!.head!.commitId;
    const r = await mergeCloudHead(S.gui, head, `vm e2e ${RUN_ID}: merge cloud head`);
    log('merge run', r.run.updateId, r.run.result);
    const cs = await waitFor('nginx container running', async () => {
      const c = opmContainers(S.deploymentId).filter((x) => x.container === 'nginx' && x.state === 'running');
      return c.length ? c : null;
    }, 60_000);
    check('nginx container running (prefixed name)', cs[0]!.name.startsWith(`opm_${S.deploymentId}_${SITE}_`), cs);
    const page = await waitFor('edge route answers', async () => {
      const g = await getText(`http://127.0.0.1:${S.ports.edge}/`).catch(() => null);
      return g && g.status === 200 ? g : null;
    }, 60_000);
    check('edge :' + S.ports.edge + ' / → nginx serves the rendered file (greeting from the deployment commit)', page.text.includes(`Hello VM ${RUN_ID}`) && page.text.includes(`content-v1 ${RUN_ID}`), page.text.slice(0, 200));
    const miss = await getText(`http://127.0.0.1:${S.ports.edge}/nope-${RUN_ID}`);
    check('edge proxies other paths to nginx (404 from nginx)', miss.status === 404, miss.status);
    const pushed = await waitFor('agent branch commit mirrored to the cloud', async () => {
      const l = await admin.listAgentBranchCommits({ deploymentId: S.deploymentId, siteSlug: SITE, query: { limit: 5 } });
      return l.items.length ? l : null;
    }, 90_000, 2000);
    check('cloud lists the agent branch commit (push outbox)', pushed.items.length >= 1, pushed.items.map((c) => `${c.seq}:${c.kind}:${c.message.slice(0, 40)}`));
  });

  await step('7', 'second cloud commit: new file content through the STAGE flow → merged on the agent → served', ['6'], async () => {
    S.html2 = `<!doctype html><html><body><h1 id="greet">{{ var.greeting }}</h1><p id="ver">content-v2 ${RUN_ID} staged</p>${'<!-- pad -->'.repeat(200)}</body></html>\n`;
    const b2 = Buffer.from(S.html2);
    const sha2 = `sha256:${sha256Hex(b2)}`;
    const d = await admin.getDeployment({ deploymentId: S.deploymentId });
    const base = d.branch!.head!.commitId;
    const input = depInput(d);
    input.files.find((f) => f.fileSlug === 'index')!.content = { sha256: sha2, sizeBytes: b2.length };
    const e1 = await errOf(admin.commitDeployment({ deploymentId: S.deploymentId, body: { message: 'no stage', baseCommitId: base, config: input } }));
    check('commit without the stage → 422 V_BLOB_MISSING (details.missing)', e1?.status === 422 && JSON.stringify(e1.details).includes('V_BLOB_MISSING') && JSON.stringify(e1.details).includes(sha2.slice(7)), e1 ? `${e1.status} ${e1.code} ${JSON.stringify(e1.details).slice(0, 200)}` : 'no error');
    const stage = await admin.createStage({ body: { blobs: [{ sha256: sha2, sizeBytes: b2.length }] } });
    check('createStage → missing = the new content', stage.missing.length === 1, stage);
    const pb = await admin.putStageBlob({ stageId: stage.stageId, sha256: sha2, body: b2 });
    check('putStageBlob → staged', pb.staged, pb);
    const before = await getText(`${API}/v1/blobs/${sha2}`, { headers: { Authorization: `Bearer ${S.adminToken}` } });
    check('staged bytes are not in the store before Commit (GET /v1/blobs → 404)', before.status === 404, before.status);
    const c = await admin.commitDeployment({ deploymentId: S.deploymentId, body: { message: `vm e2e ${RUN_ID}: new index via stage`, baseCommitId: base, config: input, stageId: stage.stageId } });
    check('commit with stageId', c.parentId === base, { seq: c.seq });
    const after = await getBytes(`${API}/v1/blobs/${sha2}`, { headers: { Authorization: `Bearer ${S.adminToken}` } });
    check('content promoted into the store at Commit (GET /v1/blobs → 200, same bytes)', after.status === 200 && after.body.equals(b2), after.status);
    const reuse = await errOf(admin.putStageBlob({ stageId: stage.stageId, sha256: sha2, body: b2 }));
    check('the stage is gone after the commit', !!reuse && (reuse.status === 404 || reuse.status === 410), reuse ? `${reuse.status} ${reuse.code}` : 'accepted');
    await mergeCloudHead(S.gui!, c.commitId, `vm e2e ${RUN_ID}: merge staged content`);
    const page = await waitFor('edge serves content-v2', async () => {
      const g = await getText(`http://127.0.0.1:${S.ports.edge}/`).catch(() => null);
      return g && g.text.includes(`content-v2 ${RUN_ID}`) ? g : null;
    }, 60_000);
    check('edge serves the staged content (rendered greeting kept)', page.text.includes(`Hello VM ${RUN_ID}`), page.text.slice(0, 160));
    const fc = await S.gui!.raw('/_opm/api/files/index/content');
    const ft = await fc.text();
    check('agent GUI getFileContent(index) = the new head content', fc.status === 200 && ft.includes(`content-v2 ${RUN_ID}`), `${fc.status} ${ft.slice(0, 80)}`);
  });

  await step('10', 'restart opm-cloud mid-way: state survives, the agent reconnects; backup', ['6'], async () => {
    const before = {
      commits: (await admin.listDeploymentCommits({ deploymentId: S.deploymentId, query: { limit: 50 } })).items.map((c) => c.commitId),
      head: (await admin.getDeployment({ deploymentId: S.deploymentId })).branch!.head!.commitId,
      image: (await admin.getImage({ serviceSlug: S.serviceSlug, version: 'v1' })).import.status,
    };
    const rs = Date.now();
    compose(['restart', 'opm-cloud'], { timeoutMs: 120_000 });
    await waitFor('healthz after restart', async () => (await getText(`${API}/healthz`).catch(() => null))?.status === 200, 60_000);
    check(`restart + healthy in ${Math.round((Date.now() - rs) / 1000)} s`, true);
    const meAfter = await getText(`${API}/v1/me`, { headers: { Authorization: `Bearer ${S.adminToken}` } });
    check('the admin ID token from before the restart is still valid (persistent signing key)', meAfter.status === 200, meAfter.status);
    const after = {
      commits: (await admin.listDeploymentCommits({ deploymentId: S.deploymentId, query: { limit: 50 } })).items.map((c) => c.commitId),
      head: (await admin.getDeployment({ deploymentId: S.deploymentId })).branch!.head!.commitId,
      image: (await admin.getImage({ serviceSlug: S.serviceSlug, version: 'v1' })).import.status,
    };
    check('deployment head, commits and image survive the restart', JSON.stringify(before) === JSON.stringify(after), { before: before.commits.length, after: after.commits.length });
    const restartAt = new Date().toISOString();
    const st = await waitFor('agent heartbeat after the restart', async () => {
      if (!agentAlive()) throw new Error(`agent died:\n${agentLogTail()}`);
      const s = (await admin.getStatus({ deploymentId: S.deploymentId })).sites.find((x) => x.siteSlug === SITE);
      return s?.online && s.lastSeenAt && s.lastSeenAt > restartAt ? s : null;
    }, 120_000, 2000);
    check('agent reconnected (online, lastSeenAt after the restart), same process', !!st && agentAlive(), { lastSeenAt: st.lastSeenAt });
    const edge = await getText(`http://127.0.0.1:${S.ports.edge}/`);
    check('site kept serving during the cloud restart', edge.status === 200 && edge.text.includes(`content-v2 ${RUN_ID}`), edge.status);
    compose(['exec', '-T', 'opm-cloud', 'rm', '-rf', '/data/backups/e2e']);
    const b = opmCloud(['backup', '--out', '/data/backups/e2e'], { timeoutMs: 300_000 });
    const bj = JSON.parse(b.out.split('\n').pop()!) as { ok: boolean; out: string };
    const ls = compose(['exec', '-T', 'opm-cloud', 'sh', '-c', 'cat /data/backups/e2e/BACKUP.json && ls /data/backups/e2e/db /data/backups/e2e/db/deployments | head -20 && du -sh /data/backups/e2e']).out;
    check('opm-cloud backup --out /data/backups/e2e → BACKUP.json + db/global.sqlite + deployment file', bj.ok && ls.includes('createdAt') && ls.includes('global.sqlite') && ls.includes(`${S.deploymentId}.sqlite`), ls.split('\n').slice(-1)[0]);
    const copied = join(RUN, `backup-${S.deploymentId}.sqlite`);
    compose(['cp', `opm-cloud:/data/backups/e2e/db/deployments/${S.deploymentId}.sqlite`, copied]);
    const bdb = new DatabaseSync(copied, { readOnly: true });
    const integrity = `${(bdb.prepare('PRAGMA integrity_check').get() as { integrity_check: string }).integrity_check} ${(bdb.prepare('SELECT count(*) n FROM commits').get() as { n: number }).n}`;
    bdb.close();
    check('backup copy of the deployment DB: integrity ok, all commits', integrity.startsWith('ok') && Number(integrity.split(/\s+/).pop()) >= after.commits.length, integrity);
  });

  await step('8', 'transfers: cloud → site (multi-part 20 MiB) and site → cloud (10 MiB); 302 /_blob, tampered sig 403', ['6'], async () => {
    // cloud → site
    const big = randomBytes(2 * UPLOAD_PART_SIZE + 4 * 1024 * 1024 + 12345);
    const bigSha = `sha256:${sha256Hex(big)}`;
    const ct = await admin.createTransfer({ deploymentId: S.deploymentId, siteSlug: SITE, body: { fileName: `to-site-${RUN_ID}.bin`, sizeBytes: big.length, sha256: bigSha, note: 'vm e2e' } });
    check(`createTransfer → upload with ${ct.upload.partCount} parts of ${ct.upload.partSize}`, ct.upload.partCount === 3 && ct.transfer.status === 'uploading', { partCount: ct.upload.partCount });
    for (let i = 0; i < ct.upload.partCount; i++) await admin.putUploadPart({ uploadId: ct.upload.uploadId, index: String(i), body: big.subarray(i * ct.upload.partSize, Math.min(big.length, (i + 1) * ct.upload.partSize)) });
    const done = await admin.completeUpload({ uploadId: ct.upload.uploadId });
    check('completeUpload → transfer ready', done.transfer?.status === 'ready', done.transfer?.status);
    const tid = ct.transfer.transferId;
    const fetched = await waitFor('agent fetched the to-site transfer', async () => {
      const l = await admin.listTransfers({ deploymentId: S.deploymentId, siteSlug: SITE, query: { direction: 'to-site' } });
      const t = l.items.find((x) => x.transferId === tid);
      return t?.fetchedAt ? t : null;
    }, 180_000, 2000);
    check('cloud: transfer fetchedAt set by the agent (store-and-forward)', !!fetched.fetchedAt, fetched.fetchedAt);
    const gl = await S.gui!.api.listSiteTransfers({ siteSlug: SITE, query: { direction: 'to-site' } });
    check('agent GUI lists the received file', gl.items.some((x: Transfer) => x.transferId === tid), gl.items.map((x: Transfer) => `${x.fileName}:${x.status}`));
    const dl = await S.gui!.raw(`/_opm/api/sites/${SITE}/transfers/${tid}/download`);
    const dlb = Buffer.from(await dl.arrayBuffer());
    check('agent GUI download of the received file = the uploaded bytes', dl.status === 200 && sha256Hex(dlb) === bigSha.slice(7), `${dl.status} ${dlb.length}`);
    const savePath = join(SITE_DIR, 'saved', `to-site-${RUN_ID}.bin`);
    mkdirSync(join(SITE_DIR, 'saved'), { recursive: true });
    const sv = await errOf(S.gui!.api.saveTransfer({ siteSlug: SITE, transferId: tid, body: { path: savePath } }));
    check('saveTransfer writes it on the server (sha256 ok)', !sv && existsSync(savePath) && sha256Hex(readFileSync(savePath)) === bigSha.slice(7), sv ? `${sv.status} ${sv.code} ${sv.message}` : statSync(savePath).size);
    if (!sv) {
      const saved = await waitFor('cloud sees saved-on-server', async () => (await admin.listTransfers({ deploymentId: S.deploymentId, siteSlug: SITE, query: { direction: 'to-site' } })).items.find((x) => x.transferId === tid && x.status === 'saved-on-server'), 60_000, 2000);
      check('cloud: status saved-on-server + savedPath', saved.savedPath === savePath, saved.savedPath);
    }
    // site → cloud (browser upload on the agent GUI, store-and-forward)
    const up = randomBytes(UPLOAD_PART_SIZE + 2 * 1024 * 1024 + 777);
    const upSha = sha256Hex(up);
    const name = `to-cloud-${RUN_ID}.bin`;
    const sr = await S.gui!.raw(`/_opm/api/sites/${SITE}/transfers/upload?fileName=${encodeURIComponent(name)}&note=vm-e2e`, { method: 'POST', headers: { 'Content-Type': 'application/octet-stream' }, body: up });
    const sj = (await sr.json()) as Transfer;
    check('agent GUI sendFromBrowser accepted (to-cloud)', sr.status < 300 && sj.direction === 'to-cloud', `${sr.status} ${JSON.stringify(sj).slice(0, 160)}`);
    const tc = await waitFor('to-cloud transfer ready in the cloud', async () => {
      const l = await admin.listTransfers({ deploymentId: S.deploymentId, siteSlug: SITE, query: { direction: 'to-cloud' } });
      return l.items.find((x) => x.fileName === name && x.status === 'ready');
    }, 180_000, 2000);
    check('cloud: to-cloud transfer ready (agent uploaded in 8 MiB parts)', tc.sha256 === `sha256:${upSha}` && tc.sizeBytes === up.length, { status: tc.status, size: tc.sizeBytes });
    const r1 = await getBytes(`${API}/v1/deployments/${S.deploymentId}/transfers/${tc.transferId}/download`, { headers: { Authorization: `Bearer ${S.adminToken}` } });
    const loc = r1.headers.get('location') ?? '';
    must('downloadTransfer → 302 to /_blob/…?exp=…&sig=…', r1.status === 302 && /\/_blob\/transfers\/.+\?exp=\d+&sig=[A-Za-z0-9_-]+/.test(loc), `${r1.status} ${loc.replace(/sig=[^&]+/, 'sig=…')}`);
    const url = new URL(loc, API);
    check('signed URL points at the public URL of the server', url.origin === new URL(S.publicUrl).origin, url.origin);
    const fetchUrl = `${API}${url.pathname}${url.search}`;
    const r2 = await getBytes(fetchUrl);
    check('GET the signed /_blob URL (no auth) → 200, same bytes', r2.status === 200 && sha256Hex(r2.body) === upSha, `${r2.status} ${r2.body.length}`);
    const rg = await getBytes(fetchUrl, { headers: { Range: 'bytes=0-99' } });
    check('Range on /_blob → 206 (100 bytes)', rg.status === 206 && rg.body.length === 100 && rg.body.equals(up.subarray(0, 100)), rg.status);
    const sig = url.searchParams.get('sig')!;
    const bad = new URL(fetchUrl);
    bad.searchParams.set('sig', (sig[0] === 'A' ? 'B' : 'A') + sig.slice(1));
    const r3 = await getBytes(bad.toString());
    check('tampered sig → 403', r3.status === 403, r3.status);
    const exp = new URL(fetchUrl);
    exp.searchParams.set('exp', String(Number(exp.searchParams.get('exp')) + 60_000));
    check('changed exp (same sig) → 403', (await getBytes(exp.toString())).status === 403);
    const st = await waitFor('to-cloud marked downloaded', async () => (await admin.listTransfers({ deploymentId: S.deploymentId, siteSlug: SITE, query: { direction: 'to-cloud' } })).items.find((x) => x.transferId === tc.transferId && x.status === 'downloaded'), 20_000, 1000).catch(() => null);
    check('to-cloud transfer status after the download = downloaded', !!st, st?.status ?? 'not downloaded');
  });

  await step('9', 'GitHub mirror against a fake GitHub on the host (outbox-triggered worker)', ['4'], async () => {
    const fake = new FakeGithub();
    const OWNER = 'opm-vm-e2e';
    const REPO = `mirror-${RUN_ID}`.toLowerCase();
    const TOKEN = 'github_pat_11VME2E' + 'x'.repeat(40) + RUN_ID.slice(-4);
    fake.addRepo(OWNER, REPO);
    fake.addToken(TOKEN, { login: 'vm-e2e-bot', repos: '*', write: true });
    const port = await freePort();
    const handler = fake as unknown as { handle(req: IncomingMessage, res: ServerResponse): Promise<void>; send(res: ServerResponse, s: number, b: unknown): void };
    const srv = createServer((req, res) => void handler.handle(req, res).catch((e: Error) => handler.send(res, 500, { message: String(e?.stack ?? e) })));
    await new Promise<void>((r) => srv.listen(port, '127.0.0.1', () => r()));
    fake.base = `http://host.docker.internal:${port}`;
    try {
      // test hook (OPM_TEST_HOOKS=1 only): point the mirror at the fake + limit it to this run's entities. There is no
      // HTTP route for the internal fields (patchGithubMirror strips them): written by a one-off opm-cloud command INSIDE
      // the container (multi-process safe: same SQLite files, WAL, busy_timeout) — never from the host.
      const hooks = { _apiBase: fake.base, _scope: [S.templateId, S.deploymentId], _stubExporter: false };
      const hs = compose(['exec', '-T', '-e', `OPM_HOOKS=${JSON.stringify(hooks)}`, 'opm-cloud', 'opm-cloud', 'test-set-mirror-hooks']);
      check('mirror test hooks written via the adapter inside the container', /hooks set/.test(hs.out), hs.out.split('\n').pop());
      const v1 = await admin.patchGithubMirror({ body: { owner: OWNER, repo: REPO, token: TOKEN, enabled: false } });
      check('patchGithubMirror: configured, token masked', v1.configured && !JSON.stringify(v1).includes(TOKEN), { token: v1.token });
      const tst = await admin.testGithubMirror({});
      check('test connection against the fake GitHub', tst.ok === true, tst);
      const v2 = await admin.patchGithubMirror({ body: { enabled: true } });
      check('mirror enabled', v2.enabled, { summary: v2.summary });
      const depCommits = (await admin.listDeploymentCommits({ deploymentId: S.deploymentId, query: { limit: 50 } })).items.length;
      const tplCommits = (await admin.listTemplateCommits({ templateId: S.templateId, query: { limit: 50 } })).items.length;
      const mine = (r: { branch: { templateId?: string; deploymentId?: string } }) => r.branch.templateId === S.templateId || r.branch.deploymentId === S.deploymentId;
      const rows = await waitFor('template + deployment + agent branches synced', async () => {
        const all = (await admin.listGithubMirrorBranches({})).items.filter((r) => mine(r as never));
        const dep = all.find((r) => r.branch.kind === 'deployment');
        const tpl = all.find((r) => r.branch.kind === 'template');
        const ag = all.find((r) => r.branch.kind === 'agent');
        const ok = [dep, tpl, ag].every((r) => r && r.status === 'synced' && !r.queue.pending && !r.queue.failed);
        return ok ? all : null;
      }, 240_000, 3000);
      check('branch rows: synced + gitSha + pushedAt', rows.every((r) => r.status !== 'synced' || (!!r.gitSha && !!r.pushedAt)), rows.map((r) => `${r.gitBranch}:${r.status}:${r.gitSha?.slice(0, 7)}`));
      const repo = fake.repo(OWNER, REPO);
      const depRow = rows.find((r) => r.branch.kind === 'deployment')!;
      const tplRow = rows.find((r) => r.branch.kind === 'template')!;
      check(`fake GitHub: deployment branch has ${depCommits} commits, template ${tplCommits}`, fake.log(repo, depRow.gitBranch).length === depCommits && fake.log(repo, tplRow.gitBranch).length === tplCommits, { dep: fake.log(repo, depRow.gitBranch).length, tpl: fake.log(repo, tplRow.gitBranch).length });
      const files = [...fake.checkout(repo, depRow.gitBranch).keys()];
      check('pushed tree has the export (compose + the file)', files.some((f) => /compose/i.test(f)) && files.some((f) => /index/.test(f)), files.slice(0, 12));
      // a new commit → outbox trigger → enqueue → worker pushes without "Sync now"
      const d = await admin.getDeployment({ deploymentId: S.deploymentId });
      const input = depInput(d);
      input.variables.find((v) => v.variableSlug === 'greeting')!.value = `Hello VM ${RUN_ID} (mirror)`;
      const c = await admin.commitDeployment({ deploymentId: S.deploymentId, body: { message: `vm e2e ${RUN_ID}: mirror me`, baseCommitId: d.branch!.head!.commitId, config: input } });
      const t1 = Date.now();
      await waitFor('new commit pushed', async () => {
        const r = (await admin.listGithubMirrorBranches({})).items.find((x) => x.gitBranch === depRow.gitBranch);
        return r && r.status === 'synced' && r.last?.opmId === c.commitId && fake.log(repo, depRow.gitBranch).length === depCommits + 1 ? r : null;
      }, 120_000, 1000);
      check(`a new commit is pushed by the outbox-triggered worker in ${Math.round((Date.now() - t1) / 1000)} s (no Sync now)`, true);
      const top = fake.log(repo, depRow.gitBranch)[0]!;
      check('git commit message = the OPM message (+ trailers)', top.message.startsWith(`vm e2e ${RUN_ID}: mirror me`), top.message.split('\n').slice(0, 4).join(' | '));
    } finally {
      await admin.patchGithubMirror({ body: { enabled: false } }).catch((e) => log('disable mirror failed', String(e)));
      srv.close();
    }
  });

  await step('11', 'Playwright: frontend/tests/local-auth.spec.ts + deployments screenshot', ['2'], async () => {
    const env = { ...process.env, OPM_E2E_MODE: 'local', OPM_E2E_BASE: API, OPM_E2E_EMAIL: ADMIN.email, OPM_E2E_PASSWORD: ADMIN.password };
    const pw = (() => {
      try {
        const out = execFileSync('npx', ['playwright', 'test', 'tests/local-auth.spec.ts', '--reporter=list', '--output=test-results-vm'], { cwd: join(ROOT, 'frontend'), env, encoding: 'utf8', stdio: 'pipe', timeout: 300_000 });
        return { code: 0, out };
      } catch (e) {
        const x = e as { status?: number; stdout?: string; stderr?: string };
        return { code: x.status ?? 1, out: `${x.stdout ?? ''}${x.stderr ?? ''}` };
      }
    })();
    const passed = /(\d+) passed/.exec(pw.out)?.[1];
    check(`local-auth.spec.ts: ${passed ?? 0} passed`, pw.code === 0 && Number(passed) >= 4, pw.out.trim().split('\n').slice(-6).join(' | '));
    const { chromium } = await import('playwright');
    const browser = await chromium.launch();
    try {
      const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
      await page.goto(`${API}/login?next=%2Fapp%2Fdeployments`);
      await page.getByTestId('login-email').fill(ADMIN.email);
      await page.getByTestId('login-password').fill(ADMIN.password);
      await page.getByTestId('login-submit').click();
      await page.waitForURL((u) => u.pathname.startsWith('/app/deployments'), { timeout: 20_000 });
      await page.getByText(`VM e2e ${RUN_ID}`).first().waitFor({ timeout: 20_000 });
      await page.waitForTimeout(800);
      mkdirSync(join(HERE, 'screenshots'), { recursive: true });
      await page.screenshot({ path: join(HERE, 'screenshots/deployments.png') });
      check('screenshot e2e/vm/screenshots/deployments.png (this run\'s deployment listed)', true);
    } finally {
      await browser.close();
    }
  });
}

async function cleanup() {
  log('\n=== cleanup');
  if (S.agent && agentAlive()) {
    S.agent.kill('SIGTERM');
    await waitFor('agent exit', async () => !agentAlive(), 30_000).catch(() => S.agent?.kill('SIGKILL'));
  }
  if (S.deploymentId) {
    const n = removeDeploymentDocker(S.deploymentId);
    log(`removed ${n.containers} containers + ${n.networks} networks of ${S.deploymentId}`);
    check('cleanup: no container of this deployment left', opmContainers(S.deploymentId).length === 0);
  }
  me.agentPid = null;
  saveState();
  if (existsSync(join(SITE_DIR, 'install-token'))) rmSync(join(SITE_DIR, 'install-token'));
}

function writeResults() {
  const pass = checks.filter((c) => c.ok).length;
  const fail = checks.length - pass;
  const esc = (s: string) => s.replace(/\|/g, '\\|').replace(/\n/g, ' ');
  const lines = [
    `# opm-cloud VM e2e — results`,
    '',
    `Run \`${RUN_ID}\` · ${new Date().toISOString()} · ${API} · ${Math.round((Date.now() - t0) / 1000)} s · **${pass} pass / ${fail} fail**`,
    '',
    `Stack: \`selfhost/docker-compose.yml\` + \`e2e/vm/compose.e2e.yml\` (OPM_TEST_HOOKS=1), image built from this checkout; agent ${S.distVersion || '?'} (darwin-arm64, downloaded through /v1/install/binary); Docker = Colima. Re-run: \`e2e/vm/run-vm-e2e.sh\`.`,
    '',
    '| # | step | check | result | detail |',
    '|---|---|---|---|---|',
    ...checks.map((c) => `| ${c.section.split(' ')[0]} | ${esc(c.section.split(' ').slice(1).join(' ')).slice(0, 60)} | ${esc(c.name)} | ${c.ok ? 'PASS' : '**FAIL**'} | ${esc(c.detail).slice(0, 220)} |`),
    '',
  ];
  writeFileSync(join(HERE, 'RESULTS.md'), lines.join('\n'));
  log(`\nRESULTS: ${pass} pass / ${fail} fail → e2e/vm/RESULTS.md`);
  return fail;
}

main()
  .catch((e) => check('harness crashed', false, String((e as Error)?.stack ?? e)))
  .finally(async () => {
    if (process.env.OPM_VM_KEEP !== '1') await cleanup().catch((e) => log('cleanup failed', String(e)));
    const fail = writeResults();
    await sleep(100);
    process.exit(fail ? 1 : 0);
  });
