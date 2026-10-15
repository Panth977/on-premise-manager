/**
 * Smoke test of a deployed API (default: prod). Signs in as the admin from .secrets/admin.json via the Auth REST API,
 * then exercises every endpoint family that needs prod-only setup (indexes, IAM signBlob, triggers):
 *   npx tsx scripts/smoke.ts [--api https://…/api] [--keep]
 */
import { generateKeyPairSync, randomBytes, sign, createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createCloudClient, signedRequestString, type AuthProvider } from '@opm/shared';
import { arg, flag } from './_admin.ts';

const here = dirname(fileURLToPath(import.meta.url));
const sec = (f: string) => JSON.parse(readFileSync(resolve(here, '../../.secrets', f), 'utf8'));
const API = arg('api', 'https://opm.example.com/api')!;
const web = sec('firebase-web-config.json');
const admin = sec('admin.json');
const step = async <T>(name: string, f: () => Promise<T>): Promise<T> => {
  const t = Date.now();
  try {
    const r = await f();
    console.log(`ok   ${name} (${Date.now() - t} ms)`);
    return r;
  } catch (e) {
    console.log(`FAIL ${name}: ${(e as Error).message} ${JSON.stringify((e as { details?: unknown }).details ?? '')}`);
    process.exitCode = 1;
    throw e;
  }
};

const login = await step('auth REST signInWithPassword', async () => {
  const r = await fetch(`https://identitytoolkit.googleapis.com/v1/accounts:signInWithPassword?key=${web.apiKey}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ email: admin.email, password: admin.password, returnSecureToken: true }) });
  const j = (await r.json()) as { idToken?: string; error?: { message: string } };
  if (!j.idToken) throw new Error(j.error?.message ?? 'no idToken');
  return j.idToken;
});
const api = createCloudClient(API, () => `Bearer ${login}`);
const me = await step('GET /v1/me', () => api.getMe({}));
if (!me.isAdmin) throw new Error('admin is not admin');
const sfx = randomBytes(3).toString('hex');
const reg = await step('create registry (AR, _json_key)', () => api.createRegistry({ body: { label: `AR smoke ${sfx}`, url: 'https://asia-south1-docker.pkg.dev', username: '_json_key', password: readFileSync(resolve(here, '../../.secrets/registry-reader-key.json'), 'utf8') } }));
const svc = await step('create service', () => api.createService({ body: { label: `Busybox ${sfx}`, serviceSlug: `bb-${sfx}`, icon: 'lucide:box' } }));
await step('test registry login', async () => {
  const t = await api.testRegistry({ registryId: reg.registryId });
  if (!t.ok) throw new Error(t.message);
});
await step('register image + import trigger → ready', async () => {
  await api.registerImage({ serviceSlug: svc.serviceSlug, body: { version: 'v1', reference: 'registry.example.com/opm-test/busybox:1.36', registryId: reg.registryId, platform: 'linux/arm64', mode: 'copy', kind: 'service', command: ['sh'] } });
  for (let i = 0; i < 180; i++) {
    const g = await api.getImage({ serviceSlug: svc.serviceSlug, version: 'v1' });
    if (g.import.status === 'ready') return g;
    if (g.import.status === 'failed') throw new Error(g.import.error ?? 'failed');
    await new Promise((r) => setTimeout(r, 2000));
  }
  throw new Error('timeout');
});
const blob = await step('upload blob', () => api.uploadBlob({ body: new TextEncoder().encode(`smoke ${sfx}\n`) }));
await step('download blob (≤ 8 MiB → 200 streamed, ETag)', async () => {
  const r = await fetch(`${API}/v1/blobs/${blob.sha256}`, { headers: { Authorization: `Bearer ${login}` }, redirect: 'manual' });
  if (r.status !== 200) throw new Error(`status ${r.status}`);
  if (r.headers.get('etag') !== `"${blob.sha256.replace(/^sha256:/, '')}"`) throw new Error(`etag ${r.headers.get('etag')}`);
  if ((await r.text()) !== `smoke ${sfx}\n`) throw new Error('content');
});
const tpl = await step('template create + patch', async () => {
  const t = await api.createTemplate({ body: { label: `Smoke ${sfx}`, icon: 'box' } });
  await api.patchTemplate({ templateId: t.templateId, body: { ifVersion: 1, variables: [{ variableSlug: 'greeting', label: 'Greeting', description: '', type: 'string', value: 'hi', required: false, options: null, validation: null }], sites: [{ siteSlug: 'app', label: 'App', config: { host: '127.0.0.1', agentPort: 7080, agentGuiAliasPort: 8090, volumes: [], containers: [{ containerSlug: svc.serviceSlug, serviceSlug: svc.serviceSlug, version: 'v1', enabled: true, env: [], volumes: [], ports: [], command: ['sleep', '3600'], entrypoint: null, restart: 'always', resources: { cpus: null, memoryMb: null }, healthcheck: null, depends: [] }], depends: [], notFoundPageId: null, edge: { httpPort: 8081, httpsPort: null, maxBodyBytes: null, hosts: [{ hostname: '*', tls: { mode: 'none' }, redirectHttpToHttps: false, responseHeaders: { set: {}, add: {}, remove: [], always: [] } }], routes: [] } } }] } });
  return t;
});
const dep = await step('create deployment', () => api.createDeployment({ body: { label: `Smoke ${sfx}`, templateId: tpl.templateId, tagSlugs: [], managerAccountUids: [] } }));
const id = dep.deployment.deploymentId;
await step('list deployments (index deleted+updatedAt)', () => api.listDeployments({ query: { limit: 5 } }));
await step('diff + publish', async () => {
  const d = await api.getDiff({ deploymentId: id });
  if (!d.publishable) throw new Error(JSON.stringify(d.blockers));
  return api.publish({ deploymentId: id, body: { ifRevision: 0, summary: 'smoke release' } });
});
const tok = await step('install token + script', async () => {
  const t = await api.createInstallToken({ deploymentId: id, body: { siteSlug: 'app' } });
  const s = await fetch(`${API}/v1/install/script`, { headers: { Authorization: `Bearer ${t.token}` } });
  const txt = await s.text();
  if (!txt.includes(`DEP="${id}"`)) throw new Error('script');
  return t;
});
const { privateKey, publicKey } = generateKeyPairSync('ed25519');
const pub = Buffer.from(publicKey.export({ format: 'jwk' }).x!, 'base64url').toString('base64');
await step('enroll', () => createCloudClient(API, () => `Bearer ${tok.token}`).enroll({ body: { publicKey: pub, agentVersion: 'smoke', hostname: 'smoke', ip: '127.0.0.1', advertiseHost: '127.0.0.1', agentPort: 7080, os: 'linux', arch: 'arm64' } }));
const signer: AuthProvider = (c) => {
  const ts = Math.floor(Date.now() / 1000);
  const nonce = randomBytes(16).toString('base64url');
  const str = signedRequestString(c.method, c.path, ts, nonce, createHash('sha256').update(c.body ?? new Uint8Array()).digest('hex'));
  return { 'X-OPM-Site': `${id}/app`, 'X-OPM-Ts': String(ts), 'X-OPM-Nonce': nonce, 'X-OPM-Sig': sign(null, Buffer.from(str), privateKey).toString('base64url') };
};
const agent = createCloudClient(API, signer);
const view = await step('agent poll (wait=0)', () => agent.poll({ query: { wait: 0 } }));
await step('agent long-poll holds 3 s', async () => {
  const t = Date.now();
  await agent.poll({ query: { cursor: view.cursor, wait: 3 } });
  if (Date.now() - t < 2500) throw new Error('did not hold');
});
await step('agent heartbeat', () => agent.heartbeat({ body: { siteSlug: 'app', agentVersion: 'smoke', headRevId: null, staging: null, peersSeen: [], metrics: { cpu: 0, memPct: 0, diskPct: 0 }, edge: null, keyVersion: 1, advertiseHost: '127.0.0.1', agentPort: 7080, ip: '127.0.0.1', hostname: 'smoke' } }));
await step('agent proposal + image manifest + layer blob (302)', async () => {
  const p = await agent.getAgentProposal({ revId: view.proposals[0]!.revId });
  const m = await agent.getImageManifest({ serviceSlug: svc.serviceSlug, version: 'v1' });
  const r = await fetch(`${API}/v1/agent/blobs/${m.layers[0]!.digest}`, { headers: signer({ method: 'GET', path: `/v1/agent/blobs/${m.layers[0]!.digest}`, body: null, endpoint: null as never }) as Record<string, string>, redirect: 'manual' });
  if (r.status !== 302) throw new Error(`blob status ${r.status}`);
  const f = await fetch(r.headers.get('location')!, { headers: { Range: 'bytes=0-9' } });
  if (f.status !== 206) throw new Error(`signed URL range ${f.status}`);
  return p;
});
await step('agent activities pipe', async () => {
  const { activityHashInput } = await import('@opm/shared');
  const base = { activityId: 'app:1', deploymentId: id, siteSlug: 'app', seq: 1, ts: new Date().toISOString(), actor: null, origin: 'system' as const, type: 'agent.started', summary: 'smoke', data: { agentVersion: 'smoke', headRevId: null }, prevHash: null };
  const hash = 'sha256:' + createHash('sha256').update(activityHashInput(base)).digest('hex');
  const r = await agent.uploadAgentActivities({ body: { siteSlug: 'app', entries: [{ ...base, hash, uploadedVia: null, uploadedAt: null }] } });
  if (r.ackSeq !== 1) throw new Error(`ackSeq ${r.ackSeq}`);
});
await step('activities queries (composite indexes)', async () => {
  await api.listActivities({ deploymentId: id, query: {} });
  await api.listActivities({ deploymentId: id, query: { site: 'app' } });
  await api.listActivities({ deploymentId: id, query: { type: 'proposal.' } });
  await api.listActivities({ deploymentId: id, query: { origin: 'cloud' } });
  await api.listActivities({ deploymentId: id, query: { actor: me.account.uid } });
  await api.listActivities({ deploymentId: id, query: { via: 'pipe', from: new Date(Date.now() - 3600_000).toISOString() } });
});
await step('status + setup + transfers list (index)', async () => {
  const st = await api.getStatus({ deploymentId: id });
  if (!st.sites.find((s) => s.siteSlug === 'app')?.online) throw new Error('app not online');
  await api.getSetup({ deploymentId: id });
  await api.listTransfers({ deploymentId: id, siteSlug: 'app', query: { direction: 'to-site' } });
  await agent.listAgentTransfers({ query: { direction: 'to-site' } });
});
await step('audit log', () => api.listAuditLog({ query: { limit: 5 } }));
if (!flag('keep')) {
  await step('cleanup (soft-delete deployment, template)', async () => {
    await api.deleteDeployment({ deploymentId: id, body: { confirm: `Smoke ${sfx}` } });
    await api.deleteTemplate({ templateId: tpl.templateId });
    await api.deleteImage({ serviceSlug: svc.serviceSlug, version: 'v1' });
    await api.deleteService({ serviceSlug: svc.serviceSlug });
    await api.deleteRegistry({ registryId: reg.registryId });
  });
}
console.log(process.exitCode ? 'SMOKE FAILED' : 'SMOKE OK');
