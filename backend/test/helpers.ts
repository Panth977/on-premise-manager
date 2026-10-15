/**
 * Integration-test helpers. Two backends run the SAME suite (§21 #108):
 *   firebase (default)       inside `firebase emulators:exec` (project demo-opm): Firestore / Auth / Storage emulators,
 *                            the API = the Functions emulator
 *   local (OPM_TEST_ADAPTER=local)  scripts/test-local.sh starts opm-cloud on a temp data dir; the tests open the same
 *                            SQLite files / blob dir (OPM_DATA_DIR) and sign in through opm-cloud /auth/login
 * adb / aauth / abucket are the PORTS (DocStore / AuthAdmin / a Bucket-shaped shim over the BlobStore).
 */
import { createHash, generateKeyPairSync, randomBytes, sign, type KeyObject } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { checkCommitMessage, createCloudClient, validateConfig, type Config, fullAdminRules, OpmApiError, signedRequestString, type AccessRule, type Account, type CloudClient, type AuthProvider, type Commit, type CommitWorkingRequest, type ConfigInput, type FileDef, type SecretDefInput, type Site, type Template, type ValidationIssue, type Variable } from '@opm/shared';
import { setPorts, type AuthAdmin, type DocStore, type Ports } from '../functions/src/ports/index.js';
import { bucketShim, type BucketShim } from '../scripts/_admin.ts';

export const here = dirname(fileURLToPath(import.meta.url));
export const ADAPTER: 'firebase' | 'local' = process.env.OPM_TEST_ADAPTER === 'local' ? 'local' : 'firebase';
export const PROJECT = process.env.GCLOUD_PROJECT || (ADAPTER === 'local' ? 'local' : 'demo-opm');
if (ADAPTER === 'firebase') {
  process.env.FIREBASE_AUTH_EMULATOR_HOST ??= '127.0.0.1:9099';
  process.env.FIRESTORE_EMULATOR_HOST ??= '127.0.0.1:8085';
  process.env.FIREBASE_STORAGE_EMULATOR_HOST ??= '127.0.0.1:9199';
}
/** opm-cloud origin in local mode (the /auth endpoints live there) */
export const SERVER = (process.env.OPM_TEST_SERVER ?? 'http://127.0.0.1:8790').replace(/\/$/, '');
export const BASE = process.env.OPM_TEST_API ?? (ADAPTER === 'local' ? SERVER : `http://127.0.0.1:5001/${PROJECT}/asia-south1/api`);
export const BUCKET = `${PROJECT}.appspot.com`;
process.env.GCLOUD_PROJECT ??= PROJECT;
if (ADAPTER === 'firebase') process.env.OPM_BUCKET ??= BUCKET;

async function makePorts(): Promise<Ports> {
  if (ADAPTER === 'local') {
    const dataDir = process.env.OPM_DATA_DIR;
    if (!dataDir) throw new Error('OPM_TEST_ADAPTER=local needs OPM_DATA_DIR (scripts/test-local.sh sets it)');
    const { localPorts } = await import('../functions/src/adapters/local/index.js');
    return localPorts({ dataDir, publicUrl: SERVER, consoleUrl: process.env.OPM_CONSOLE_URL });
  }
  const { initializeApp, getApps } = await import('firebase-admin/app');
  const { getFirestore, Timestamp, FieldValue } = await import('firebase-admin/firestore');
  const { getStorage } = await import('firebase-admin/storage');
  const { getAuth } = await import('firebase-admin/auth');
  const { firebasePorts } = await import('../functions/src/adapters/firebase/index.js');
  if (!getApps().length) initializeApp({ projectId: PROJECT, storageBucket: BUCKET });
  const fs = getFirestore();
  try {
    fs.settings({ ignoreUndefinedProperties: true });
  } catch {
    /* already set */
  }
  return firebasePorts({ db: fs, bucket: getStorage().bucket(BUCKET), auth: getAuth(), sdk: { Timestamp, FieldValue } as never });
}
export const ports: Ports = await makePorts();
// backend modules imported by tests (catalog.ts, migrations …) use the same adapter
setPorts(ports);
export const adb: DocStore = ports.store;
export const aauth: AuthAdmin = ports.auth;
export const abucket: BucketShim = bucketShim(ports.blobs);

export const load = (name: string) => JSON.parse(readFileSync(resolve(here, '../../shared/testdata', name), 'utf8'));
export const uniq = (p = 'x') => `${p}${randomBytes(4).toString('hex')}`;
export const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

export async function idToken(email: string, password: string): Promise<string> {
  if (ADAPTER === 'local') {
    const r = await fetch(`${SERVER}/auth/login`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ email, password }) });
    const j = (await r.json()) as { idToken?: string; error?: unknown };
    if (!j.idToken) throw new Error(`sign-in failed: ${JSON.stringify(j.error)}`);
    return j.idToken;
  }
  const r = await fetch(`http://${process.env.FIREBASE_AUTH_EMULATOR_HOST}/identitytoolkit.googleapis.com/v1/accounts:signInWithPassword?key=fake`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email, password, returnSecureToken: true }),
  });
  const j = (await r.json()) as { idToken?: string; error?: unknown };
  if (!j.idToken) throw new Error(`sign-in failed: ${JSON.stringify(j.error)}`);
  return j.idToken;
}

export interface Person {
  uid: string;
  email: string;
  token: string;
  /** browser-emulating client (§21 #105: draft writes become a pending change set, committed by commit*) */
  api: LegacyCloudClient;
  /** the plain API client */
  real: CloudClient;
  bearer: string;
}
/** Create a Firebase user + account doc directly (bootstrap path) and sign in. */
export async function person(access: AccessRule[] | 'admin', opts: { disabled?: boolean; noAccount?: boolean } = {}): Promise<Person> {
  const email = `${uniq('u')}@opm.test`;
  const password = 'pw-' + randomBytes(8).toString('hex');
  const u = await aauth.createUser({ email, password, displayName: email });
  if (!opts.noAccount) {
    const acc: Account = { uid: u.uid, img: null, name: email, email, phone: null, access: access === 'admin' ? fullAdminRules() : access, disabled: !!opts.disabled, createdAt: new Date().toISOString(), createdBy: 'bootstrap', lastLoginAt: null, lastSetPassword: null, lastResetLink: null };
    await adb.collection('accounts').doc(u.uid).set(acc);
  }
  const token = await idToken(email, password);
  const real = createCloudClient(BASE, () => `Bearer ${token}`);
  return { uid: u.uid, email, token, bearer: `Bearer ${token}`, api: browserApi(real), real };
}

// ───────────────────────────────────────── §21 #105: the browser change set, emulated for the older tests
/**
 * There is no server working copy since §21 #105: the GUI keeps a pending change set and sends it with Commit. The
 * tests written for the working-copy API keep their intent through this wrapper, which plays the browser: the removed
 * write calls (putDraftSection, putDraftSite, regenerateSecret, patchTemplate config fields, discard…Working) edit an
 * in-memory change set per entity; commitTemplate / commitDeployment with the old body ({ message, ifVersion |
 * ifRevision }) commit it ({ message, baseCommitId, config }). `Person.real` is the plain client (tests of #105 itself).
 */
const pendingDep = new Map<string, ConfigInput>();
const pendingTpl = new Map<string, ConfigInput>();
const tplOffset = new Map<string, number>();
export const clearPending = () => (pendingDep.clear(), pendingTpl.clear(), tplOffset.clear());
const cloneJ = <T>(x: T): T => JSON.parse(JSON.stringify(x));
export async function depInput(api: CloudClient, deploymentId: string): Promise<ConfigInput> {
  const d = await api.getDeployment({ deploymentId });
  return { variables: d.draft.variables, files: d.draft.files, sites: d.draft.sites, secrets: d.draft.secrets.map(({ value: _v, isSet: _i, rotatedAt: _r, ...x }) => (void _v, void _i, void _r, x as SecretDefInput)) };
}
export async function tplInput(api: CloudClient, templateId: string): Promise<ConfigInput> {
  const t = await api.getTemplate({ templateId });
  return { variables: t.variables, files: t.files, sites: t.sites, secrets: t.secrets.map(({ value: _v, rotatedAt: _r, ...x }) => (void _v, void _r, x as SecretDefInput)) };
}
function mergeSecretOps(prev: SecretDefInput[], next: SecretDefInput[]): SecretDefInput[] {
  return next.map((it) => {
    const { value: _v, rotatedAt: _r, ...x } = it as SecretDefInput & { value?: unknown; rotatedAt?: unknown };
    void _v;
    void _r;
    const p = prev.find((y) => y.secretSlug === x.secretSlug);
    if (x.plainValue === undefined && !x.generateNow && p) return { ...x, ...(p.plainValue !== undefined ? { plainValue: p.plainValue } : {}), ...(p.generateNow ? { generateNow: true } : {}) };
    return x;
  });
}
type OldCommitBody = { message: string; ifVersion?: number; ifRevision?: number };
export type LegacyCloudClient = Omit<CloudClient, 'commitTemplate' | 'commitDeployment'> & {
  commitTemplate(a: { templateId: string; body: CommitWorkingRequest | OldCommitBody }): Promise<Commit>;
  commitDeployment(a: { deploymentId: string; body: CommitWorkingRequest | OldCommitBody }): Promise<Commit>;
};

/** the old "save level" check of a draft / template write (mode draft | template), against the live catalog (adb) */
async function saveLevelErrors(config: Config, mode: 'draft' | 'template', deploymentId: string): Promise<ValidationIssue[]> {
  return (await saveLevelIssues(config, mode, deploymentId)).filter((i) => i.severity === 'error');
}
async function saveLevelIssues(config: Config, mode: 'draft' | 'template', deploymentId: string): Promise<ValidationIssue[]> {
  const { catalogImage, imageView, serviceView } = await import('../functions/src/core/catalog.ts');
  const [svc, imgs, pages] = await Promise.all([adb.collection('services').get(), adb.collectionGroup('images').get(), adb.collection('notFoundPages').select().get()]);
  const services: Record<string, { label: string }> = {};
  for (const d of svc.docs) services[d.id] = { label: serviceView(d.data(), d.id).label };
  const images: Record<string, ReturnType<typeof catalogImage>> = {};
  for (const d of imgs.docs) {
    const parent = d.ref.parent.parent;
    if (!parent || parent.parent.id !== 'services') continue;
    const i = imageView(d.data(), d.id, parent.id);
    images[`${i.serviceSlug}:${i.version}`] = catalogImage(i);
  }
  const detectedHosts: Record<string, string> = {};
  if (mode === 'draft') for (const a of (await adb.collection('deployments').doc(deploymentId).collection('agents').get()).docs) if (typeof a.data().advertiseHost === 'string' && a.data().advertiseHost) detectedHosts[a.id] = a.data().advertiseHost;
  try {
    return validateConfig(config, { mode, deploymentId: mode === 'template' ? 'template' : deploymentId, services, images, notFoundPageIds: pages.docs.map((d) => d.id), detectedHosts } as never).list;
  } catch {
    return [];
  }
}
const asConfig = (c: ConfigInput, isSet: (slug: string) => boolean = () => false): Config =>
  ({ ...c, secrets: c.secrets.map(({ plainValue, generateNow, ...d }) => ({ ...d, value: generateNow || (typeof plainValue === 'string' && plainValue) || (plainValue === undefined && (isSet(d.secretSlug) || !!d.generate)) ? 'x' : null, rotatedAt: null })) }) as Config;
export function browserApi(api: CloudClient): LegacyCloudClient {
  const w = Object.create(api) as Record<string, unknown>;
  /**
   * The old draft write, played by the browser + Commit: a change that is publish-valid is committed at once (so reads
   * see the server-normalized result); a save-level error (mode draft) is refused like before (422, nothing kept); a
   * publish-level blocker keeps the change pending (the old draft) until a later commit.
   */
  const setOf = (d: { draft: { secrets: { secretSlug: string; isSet: boolean }[] } }) => (slug: string) => !!d.draft.secrets.find((x) => x.secretSlug === slug)?.isSet;
  const depWrite = async (deploymentId: string, next: ConfigInput, message: string) => {
    const d = await api.getDeployment({ deploymentId });
    try {
      await api.commitDeployment({ deploymentId, body: { message, baseCommitId: d.branch?.head?.commitId ?? null, config: next } });
      pendingDep.delete(deploymentId);
    } catch (e) {
      // normalization errors (E.bad) are refused like before; only validation blockers ("cannot commit: …") may be a draft
      if (!(e instanceof OpmApiError) || e.code !== 'invalid' || !/^cannot commit/.test(e.message)) throw e;
      const errs = (e.details?.errors as ValidationIssue[] | undefined) ?? [];
      if (errs.length && errs.every((x) => x.code === 'V_NO_CHANGE')) pendingDep.delete(deploymentId);
      else {
        const saveErrs = await saveLevelErrors(asConfig(next, setOf(d)), 'draft', deploymentId);
        if (saveErrs.length) throw e;
        pendingDep.set(deploymentId, next);
      }
    }
    const after = await api.getDeployment({ deploymentId });
    const issues = (await saveLevelIssues(asConfig(next, setOf(d)), 'draft', deploymentId)).filter((i) => i.severity !== 'error');
    return { draftVersion: after.draftVersion, validation: { ok: true, issues }, uncommitted: pendingDep.has(deploymentId) ? 1 : 0 };
  };
  w.putDraftSection = (async (a: { deploymentId: string; section: string; body: { ifRevision: number; items: unknown[] } }) => {
    const cur = pendingDep.get(a.deploymentId) ?? (await depInput(api, a.deploymentId));
    const next = cloneJ(cur);
    if (a.section === 'secrets') next.secrets = mergeSecretOps(cur.secrets, a.body.items as SecretDefInput[]);
    else (next as unknown as Record<string, unknown>)[a.section] = cloneJ(a.body.items);
    return depWrite(a.deploymentId, next, `test edit: ${a.section}`);
  }) as never;
  w.putDraftSite = (async (a: { deploymentId: string; siteSlug: string; body: { ifRevision: number; site: Site } }) => {
    const cur = pendingDep.get(a.deploymentId) ?? (await depInput(api, a.deploymentId));
    const next = cloneJ(cur);
    const i = next.sites.findIndex((x) => x.siteSlug === a.siteSlug);
    if (i < 0) throw new OpmApiError(404, 'not-found', `site ${a.siteSlug} is not in the draft`, {});
    next.sites[i] = cloneJ(a.body.site);
    return depWrite(a.deploymentId, next, `test edit: site ${a.siteSlug}`);
  }) as never;
  w.regenerateSecret = (async (a: { deploymentId: string; secretSlug: string; body: { ifRevision?: number } }) => {
    const cur = pendingDep.get(a.deploymentId) ?? (await depInput(api, a.deploymentId));
    const next = cloneJ(cur);
    const s = next.secrets.find((x) => x.secretSlug === a.secretSlug);
    if (!s) throw new OpmApiError(404, 'not-found', `secret ${a.secretSlug} is not in the draft`, {});
    delete s.plainValue;
    s.generateNow = true;
    pendingDep.set(a.deploymentId, next);
    return { value: '', hideAfterSec: 60, draftVersion: (a.body?.ifRevision ?? 0) + 1, rotatedAt: new Date().toISOString() };
  }) as never;
  w.patchTemplate = (async (a: { templateId: string; body: Record<string, unknown> & { ifVersion: number } }) => {
    const { variables, secrets, files, sites, ifVersion, ...meta } = a.body;
    const off = tplOffset.get(a.templateId) ?? 0;
    let template: Template;
    if (Object.keys(meta).length) template = (await api.patchTemplate({ templateId: a.templateId, body: { ifVersion, ...meta } })).template;
    else template = await api.getTemplate({ templateId: a.templateId });
    if (variables || secrets || files || sites) {
      const cur = pendingTpl.get(a.templateId) ?? (await tplInput(api, a.templateId));
      const next: ConfigInput = { variables: (variables as Variable[]) ?? cur.variables, files: (files as FileDef[]) ?? cur.files, sites: (sites as Site[]) ?? cur.sites, secrets: secrets ? mergeSecretOps(cur.secrets, (secrets as (SecretDefInput & { value?: unknown })[]).map((x) => { if (x.value !== null && x.value !== undefined) throw new OpmApiError(422, 'invalid', 'template secrets hold no values', { errors: [{ path: 'secrets', code: 'V_TEMPLATE_SECRET_VALUE', message: 'value must be null', severity: 'error' }] }); return x; })) : cur.secrets };
      // a template commit validates at template level = the old PATCH level: commit at once (422 = nothing saved)
      try {
        await api.commitTemplate({ templateId: a.templateId, body: { message: 'test edit', baseCommitId: template.branch?.head?.commitId ?? null, config: next } });
        pendingTpl.delete(a.templateId);
      } catch (e) {
        const errs = e instanceof OpmApiError ? ((e.details?.errors as ValidationIssue[] | undefined) ?? []) : [];
        if (!(errs.length && errs.every((x) => x.code === 'V_NO_CHANGE'))) throw e;
      }
      template = await api.getTemplate({ templateId: a.templateId });
      tplOffset.set(a.templateId, off + 1);
    }
    const p = pendingTpl.get(a.templateId);
    const issues = variables || secrets || files || sites ? (await saveLevelIssues({ variables: template.variables, secrets: template.secrets, files: template.files, sites: template.sites }, 'template', 'template')).filter((i) => i.severity !== 'error') : [];
    const t = { ...template, ...(p ? { variables: p.variables, files: p.files, sites: p.sites, secrets: p.secrets.map((x) => ({ ...x, value: null, rotatedAt: null })) } : {}) } as Template;
    return { template: t, validation: { ok: true, issues } };
  }) as never;
  w.commitTemplate = (async (a: { templateId: string; body: Record<string, unknown> & { message: string } }) => {
    if ('config' in a.body) return api.commitTemplate(a as never);
    const t = await api.getTemplate({ templateId: a.templateId });
    if (!pendingTpl.has(a.templateId)) {
      // the edit was committed at once by patchTemplate: re-label that commit's message is impossible — return it
      const h = t.branch?.head;
      if (h && h.message.startsWith('test edit') && !checkCommitMessage(a.body.message).length) return (await api.getTemplateCommit({ templateId: a.templateId, commitId: h.commitId })).commit as unknown as Commit;
    }
    const config = pendingTpl.get(a.templateId) ?? (await tplInput(api, a.templateId));
    const c = await api.commitTemplate({ templateId: a.templateId, body: { message: a.body.message, baseCommitId: t.branch?.head?.commitId ?? null, config } });
    pendingTpl.delete(a.templateId);
    return c;
  }) as never;
  w.commitDeployment = (async (a: { deploymentId: string; body: Record<string, unknown> & { message: string } }) => {
    if ('config' in a.body) return api.commitDeployment(a as never);
    const d = await api.getDeployment({ deploymentId: a.deploymentId });
    if (!pendingDep.has(a.deploymentId)) {
      const h = d.branch?.head;
      if (h && h.message.startsWith('test edit') && !checkCommitMessage(a.body.message).length) return (await api.getDeploymentCommit({ deploymentId: a.deploymentId, commitId: h.commitId })).commit as unknown as Commit;
    }
    const config = pendingDep.get(a.deploymentId) ?? (await depInput(api, a.deploymentId));
    const c = await api.commitDeployment({ deploymentId: a.deploymentId, body: { message: a.body.message, baseCommitId: d.branch?.head?.commitId ?? null, config } });
    pendingDep.delete(a.deploymentId);
    return c;
  }) as never;
  w.discardTemplateWorking = (async (a: { templateId: string }) => {
    pendingTpl.delete(a.templateId);
    return { version: (await api.getTemplate({ templateId: a.templateId })).version, uncommitted: 0 };
  }) as never;
  w.discardDeploymentWorking = (async (a: { deploymentId: string }) => {
    pendingDep.delete(a.deploymentId);
    return { draftVersion: (await api.getDeployment({ deploymentId: a.deploymentId })).draftVersion, uncommitted: 0 };
  }) as never;
  return w as unknown as LegacyCloudClient;
}
/** the emulated change set of a deployment (null = none) */
export const pendingOf = (deploymentId: string) => pendingDep.get(deploymentId) ?? null;

/** Expect an OpmApiError with a code (and optional HTTP status). */
export async function rejects(p: Promise<unknown>, code: string, status?: number): Promise<OpmApiError> {
  try {
    await p;
  } catch (e) {
    if (!(e instanceof OpmApiError)) throw e;
    if (e.code !== code || (status !== undefined && e.status !== status)) throw new Error(`expected ${code}${status ? `/${status}` : ''}, got ${e.code}/${e.status}: ${e.message} ${JSON.stringify(e.details)}`);
    return e;
  }
  throw new Error(`expected error ${code}, but the call succeeded`);
}

/** §21 #91: an unusable install token gets 200 + a tiny script that prints why and exits 1 (curl -f hides 410 bodies). */
export function tinyScript(r: { status: number; body: Buffer }, re: RegExp) {
  if (r.status !== 200) throw new Error(`expected 200 tiny script, got ${r.status}`);
  const body = r.body.toString();
  if (!body.startsWith('#!/bin/sh') || !/\nexit 1\n$/.test(body) || !re.test(body)) throw new Error(`not the expected tiny script (${re}):\n${body}`);
  if (body.includes('AGENT_VERSION=')) throw new Error('a real install script was served');
}
export async function raw(method: string, path: string, init: { headers?: Record<string, string>; body?: Buffer | string | Uint8Array } = {}) {
  const res = await fetch(BASE + path, { method, headers: init.headers, body: init.body as never, redirect: 'manual' });
  const buf = Buffer.from(await res.arrayBuffer());
  let json: unknown = null;
  try {
    json = JSON.parse(buf.toString('utf8'));
  } catch {
    /* raw */
  }
  return { status: res.status, headers: res.headers, body: buf, json: json as Record<string, any> };
}

// ───────────────────────────────────────── agents
export interface SiteKey {
  priv: KeyObject;
  publicKey: string; // raw 32 bytes base64 std
}
export function newSiteKey(): SiteKey {
  const { privateKey, publicKey } = generateKeyPairSync('ed25519');
  const x = publicKey.export({ format: 'jwk' }).x!;
  return { priv: privateKey, publicKey: Buffer.from(x, 'base64url').toString('base64') };
}
export const sha256Hex = (b: Uint8Array | string) => createHash('sha256').update(b).digest('hex');
export function signHeaders(key: SiteKey, deploymentId: string, siteSlug: string, method: string, path: string, body: Uint8Array | null, o: { ts?: number; nonce?: string } = {}) {
  const ts = o.ts ?? Math.floor(Date.now() / 1000);
  const nonce = o.nonce ?? randomBytes(16).toString('base64url');
  const str = signedRequestString(method, path, ts, nonce, sha256Hex(body ?? new Uint8Array()));
  const sig = sign(null, Buffer.from(str, 'utf8'), key.priv).toString('base64url');
  return { 'X-OPM-Site': `${deploymentId}/${siteSlug}`, 'X-OPM-Ts': String(ts), 'X-OPM-Nonce': nonce, 'X-OPM-Sig': sig };
}
export function agentClient(key: SiteKey, deploymentId: string, siteSlug: string): CloudClient {
  const authp: AuthProvider = (ctx) => signHeaders(key, deploymentId, siteSlug, ctx.method, ctx.path, ctx.body);
  return createCloudClient(BASE, authp);
}

// ───────────────────────────────────────── hello catalog seeding (fixed ids from shared/testdata)
export async function seedHelloCatalog(): Promise<void> {
  const cat = load('hello-catalog.json');
  const now = new Date().toISOString();
  const audit = { createdAt: now, createdBy: 'test', updatedAt: now, updatedBy: 'test' };
  const regRef = adb.collection('registries').doc('RegHello000000000001');
  await regRef.set({ registryId: regRef.id, label: 'hello registry', url: 'https://127.0.0.1:1', username: 'u', password: 'p', lastCheck: null, ...audit });
  for (const [slug, s] of Object.entries<any>(cat.services)) {
    await adb.collection('services').doc(slug).set({ serviceSlug: slug, label: s.label, icon: s.icon ?? null, description: '', ...audit });
  }
  let n = 1;
  for (const [key, img] of Object.entries<any>(cat.images)) {
    const [serviceSlug, version] = key.split(':') as [string, string];
    const hex = (k: string) => sha256Hex(`${key}:${k}`);
    const layers = [{ digest: `sha256:${hex('l1')}`, sizeBytes: 100 + n, mediaType: 'application/vnd.oci.image.layer.v1.tar+gzip' }];
    await adb.collection('services').doc(serviceSlug).collection('images').doc(version).set({
      version,
      serviceSlug,
      reference: `127.0.0.1:1/test/${serviceSlug}:${version}`,
      source: { host: '127.0.0.1:1', repository: `test/${serviceSlug}`, tag: version, digest: null },
      registryId: regRef.id,
      digest: `sha256:${hex('m')}`,
      platform: 'linux/arm64',
      import: { mode: 'copy', status: 'ready', error: null, sizeBytes: layers[0]!.sizeBytes, manifestMediaType: 'application/vnd.oci.image.manifest.v1+json', configDigest: `sha256:${hex('c')}`, layers, startedAt: now, finishedAt: now },
      kind: img.kind,
      entrypoint: null,
      command: null,
      workdir: null,
      user: null,
      ports: img.ports,
      env: img.env,
      volumes: img.volumes,
      healthcheck: img.healthcheck ?? null,
      stopTimeoutSec: 30,
      description: '',
      _configSizeBytes: 1234,
      ...audit,
    });
    n++;
  }
  await adb.collection('notFoundPages').doc('NfpHello000000000001').set({ notFoundPageId: 'NfpHello000000000001', label: 'Hello 404', htmlCode: '<h1>Hello 404</h1>', createdAt: now, createdBy: 'test', _sizeBytes: 18 });
}
export const HELLO = () => load('hello-config.json') as { config: import('@opm/shared').Config; banner: string };

/** Admin creates a deployment with the hello config in its draft (via a template). */
export async function helloDeployment(admin: Person, label = uniq('dep')) {
  await seedHelloCatalog();
  const h = HELLO();
  const banner = await admin.api.uploadBlob({ body: new TextEncoder().encode(h.banner) });
  if (banner.sha256 !== h.config.files[0]!.content!.sha256) throw new Error('banner sha mismatch');
  const t = await admin.api.createTemplate({ body: { label: uniq('tpl'), icon: 'box' } });
  const secrets = h.config.secrets.map((s) => ({ ...s, value: null, rotatedAt: null }));
  await admin.api.patchTemplate({ templateId: t.templateId, body: { ifVersion: 1, variables: h.config.variables, secrets, files: h.config.files, sites: h.config.sites } });
  // §21 #104: a deployment is created from the template HEAD commit → commit the edited working copy first
  await admin.api.commitTemplate({ templateId: t.templateId, body: { message: 'hello config', ifVersion: 2 } });
  const d = await admin.api.createDeployment({ body: { label, templateId: t.templateId, tagSlugs: [], managerAccountUids: [] } });
  return { dep: d.deployment, accessKey: d.accessKey, templateId: t.templateId };
}

/** Install token + enroll a site; returns its key + signed client. */
export async function enrollSite(admin: Person, deploymentId: string, siteSlug: string, o: { advertiseHost?: string; agentPort?: number } = {}) {
  const tok = await admin.api.createInstallToken({ deploymentId, body: { siteSlug } });
  const key = newSiteKey();
  const anon = createCloudClient(BASE, () => `Bearer ${tok.token}`);
  const res = await anon.enroll({ body: { publicKey: key.publicKey, agentVersion: '0.1.0-test', hostname: `host-${siteSlug}`, ip: '127.0.0.1', advertiseHost: o.advertiseHost ?? '127.0.0.1', agentPort: o.agentPort ?? 7080, os: 'darwin', arch: 'arm64' } });
  return { key, api: agentClient(key, deploymentId, siteSlug), enroll: res, token: tok.token };
}

/**
 * §21 #104 the old "publish" through the new API: commit the working copy (a release) and return the compat proposal +
 * revision the cloud wrote for agents < 0.14 (test agents enroll as 0.1.0-test). A working copy equal to the head (e.g.
 * right after create) returns the head commit's compat proposal (written at create).
 */
export async function publish(api: LegacyCloudClient, deploymentId: string, body: { ifRevision: number; summary: string }) {
  let commit: import('@opm/shared').Commit;
  try {
    commit = await api.commitDeployment({ deploymentId, body: { message: body.summary, ifRevision: body.ifRevision } });
  } catch (e) {
    const errs = (e as OpmApiError).details?.errors as { code: string }[] | undefined;
    if (!(e instanceof OpmApiError) || e.code !== 'invalid' || errs?.[0]?.code !== 'V_NO_CHANGE') throw e;
    const list = await api.listDeploymentCommits({ deploymentId, query: { limit: 1 } });
    commit = (await api.getDeploymentCommit({ deploymentId, commitId: list.items[0]!.commitId })).commit as unknown as import('@opm/shared').Commit;
  }
  const compat = commit.data?.compat ?? null;
  const proposals = compat ? (await api.listProposals({ deploymentId, query: { limit: 50 } })).items : [];
  const proposal = compat ? proposals.find((p) => p.revId === compat.revId)! : null;
  const revision = compat ? await api.getRevision({ deploymentId, revId: compat.revId }) : null;
  return { commit, proposal: proposal!, revision: revision! };
}
