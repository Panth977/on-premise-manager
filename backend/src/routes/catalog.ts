/** Developer catalog: me, registries, services, images, not-found pages, tags (SPEC-final §2.2, §6, §6.1). */
import { FieldValue } from '../driver/index.js';
import {
  isAdmin,
  REVEAL_SECONDS,
  type RevealPasswordResponse,
  LIMITS,
  RE,
  validateRegisterImage,
  validateImageSpec,
  imageNameChanges,
  imageNameUses,
  renameImageNames,
  auditDiff,
  normalizeImageEnv,
  normalizeImageVolume,
  parseImageReference,
  registryHost,
  matchRegistries,
  Issues,
  type ImageRefParts,
  type Account,
  type CreateNotFoundPageRequest,
  type CreateRegistryRequest,
  type CreateServiceRequest,
  type CreateTagRequest,
  type Image,
  type ImageEnv,
  type ImagePort,
  type ImageVolume,
  type InspectImageRequest,
  type InspectImageResponse,
  type NotFoundPage,
  type NotFoundPageListItem,
  type PatchImageRequest,
  type PatchRegistryRequest,
  type PatchServiceRequest,
  type PatchTagRequest,
  type Platform,
  type RegisterImageRequest,
  type Registry,
  type Service,
  type Tag,
  type Template,
  type ImageImpactItem,
  type ImageInUseDetails,
  type ImageNameUse,
  type ValidationIssue,
  makeCommit,
  commitRef,
  type BranchState,
  type Config,
} from "@opm/shared";
import { col, db, isAlreadyExists, nowIso, pub } from '../core/fire.js';
import { E } from '../core/errors.js';
import { need } from '../core/perm.js';
import { registryView } from '../core/views.js';
import { asObject, clone, onlyKeys, V, vDescription, vLabel, vSlug, vString } from '../core/util.js';
import { dedupeUsage, imageView, scanUsage, serviceView } from '../core/catalog.js';
import { isRegistryAccessError, normalizeRepository, RegistryClient, RegistryError, type OciConfig } from '../core/registry.js';
import { accessHint, listRepositories, listTags, withAccessHint } from '../core/registry-browse.js';
import { sourceCreds, sourceRef } from '../core/importer.js';
import { vIconFormat, vIconRefs } from '../core/icons.js';
import type { Ctx, Handlers } from '../core/router.js';
import { decodeDeployment, decodeTemplate, encodeTemplate } from '../core/codec.js';
import { loadCommit, newCommitId, personAuthor, storeCommitBody, writeCommit } from '../core/branches.js';
import { writeAuditEntry } from '../core/audit.js';
import type { DocRef } from '../driver/index.js';

const audited = (uid: string) => {
  const t = nowIso();
  return { createdAt: t, createdBy: uid, updatedAt: t, updatedBy: uid };
};
const touched = (uid: string) => ({ updatedAt: nowIso(), updatedBy: uid });
const uidOf = (c: Ctx) => c.account!.uid;

const PATCH_IMAGE_KEYS = ['reference', 'registryId', 'kind', 'entrypoint', 'command', 'workdir', 'user', 'ports', 'env', 'volumes', 'healthcheck', 'stopTimeoutSec', 'description', 'renames', 'renameEverywhere'];
const has = (o: unknown, k: string) => !!o && typeof o === 'object' && Object.prototype.hasOwnProperty.call(o, k);
/**
 * Merge a PatchImageRequest into `next` (no validation of values here — validateImageSpec runs on the result).
 * ports / env / volumes: full lists replace; the legacy partial forms (every port item only { name, description }, every
 * env item without required + secret) merge by name (unknown name → V_REF). Mixed forms → V_TYPE.
 */
function applyImageEdits(next: Image, p: PatchImageRequest) {
  const v = new V();
  if (p.kind !== undefined) next.kind = p.kind;
  if (p.entrypoint !== undefined) next.entrypoint = p.entrypoint;
  if (p.command !== undefined) next.command = p.command;
  if (p.workdir !== undefined) next.workdir = p.workdir === '' ? null : p.workdir;
  if (p.user !== undefined) next.user = p.user === '' ? null : p.user;
  if (p.description !== undefined) next.description = p.description;
  if (p.healthcheck !== undefined) next.healthcheck = p.healthcheck;
  if (p.stopTimeoutSec !== undefined) next.stopTimeoutSec = p.stopTimeoutSec;
  if (p.ports !== undefined) {
    if (!Array.isArray(p.ports)) v.err('ports', 'V_TYPE', 'ports must be a list');
    else {
      const partial = p.ports.map((x) => !!x && typeof x === 'object' && !has(x, 'port') && !has(x, 'protocol'));
      if (partial.length && partial.every(Boolean))
        (p.ports as { name: string; description: string }[]).forEach((pp, i) => {
          const extra = Object.keys(pp).filter((k) => k !== 'name' && k !== 'description');
          if (extra.length) return v.err(`ports[${pp.name ?? i}]`, 'V_TYPE', `unknown fields ${extra.join(', ')}`);
          const t = next.ports.find((x) => x.name === pp.name);
          if (!t) return v.err(`ports[${pp.name}]`, 'V_REF', `unknown port ${String(pp.name)}`);
          t.description = pp.description;
        });
      else if (partial.some(Boolean)) v.err('ports', 'V_TYPE', 'ports: send the full list ({ name, port, protocol, description }) or only { name, description } items, not both');
      else next.ports = (p.ports as ImagePort[]).map((x) => (x && typeof x === 'object' ? { name: x.name, port: x.port, protocol: x.protocol, description: x.description ?? '' } : x));
    }
  }
  if (p.env !== undefined) {
    if (!Array.isArray(p.env)) v.err('env', 'V_TYPE', 'env must be a list');
    else {
      const partial = p.env.map((x) => !!x && typeof x === 'object' && !has(x, 'required') && !has(x, 'secret'));
      if (partial.length && partial.every(Boolean))
        (p.env as { name: string; value?: unknown; options?: string[] | null; description?: string }[]).forEach((pe, i) => {
          const extra = Object.keys(pe).filter((k) => !['name', 'value', 'options', 'description'].includes(k));
          if (extra.length) return v.err(`env[${pe.name ?? i}]`, 'V_TYPE', `unknown fields ${extra.join(', ')}`);
          const t = next.env.find((x) => x.name === pe.name);
          if (!t) return v.err(`env[${pe.name}]`, 'V_REF', `unknown env ${String(pe.name)}`);
          if (pe.options !== undefined) t.options = pe.options;
          if (pe.value === null) delete t.value;
          else if (Array.isArray(pe.value)) {
            const n = normalizeImageEnv({ ...t, value: pe.value });
            t.options = pe.options ?? n.options;
            if (n.value !== undefined) t.value = n.value;
          } else if (pe.value !== undefined) t.value = pe.value as string;
          if (pe.description !== undefined) t.description = pe.description;
        });
      else if (partial.some(Boolean)) v.err('env', 'V_TYPE', 'env: send the full list (with required + secret) or only partial { name, value?, options?, description? } items, not both');
      else
        next.env = (p.env as ImageEnv[]).map((e) => {
          if (!e || typeof e !== 'object') return e;
          const o = normalizeImageEnv({ name: e.name, value: (e as { value?: unknown }).value ?? undefined, options: e.options ?? null, required: e.required, secret: e.secret, description: e.description ?? '' });
          return o;
        });
    }
  }
  if (p.volumes !== undefined) {
    if (!Array.isArray(p.volumes)) v.err('volumes', 'V_TYPE', 'volumes must be a list');
    // kind: kept as sent (validateImageSpec checks it); absent → inferred from the path after validation (§21 #83)
    else next.volumes = p.volumes.map((x) => (x && typeof x === 'object' ? { name: x.name, mountPath: x.mountPath, kind: x.kind as ImageVolume['kind'], description: x.description ?? '' } : x));
  }
  if (p.renames !== undefined && !Array.isArray(p.renames)) v.err('renames', 'V_TYPE', 'renames must be a list of { kind, from, to }');
  v.throwIf();
}

// ───────────────────────────────────────── me
async function getMe(c: Ctx) {
  const acc = c.account!;
  const last = acc.lastLoginAt ? Date.parse(acc.lastLoginAt) : 0;
  let account: Account = acc;
  if (Date.now() - last > 3600_000) {
    const lastLoginAt = nowIso();
    await col.accounts().doc(acc.uid).update({ lastLoginAt });
    account = { ...acc, lastLoginAt };
  }
  return { account: pub<Account>(account as unknown as Record<string, unknown>), isAdmin: isAdmin(account) };
}

// ───────────────────────────────────────── registries
function validateRegistryFields(v: V, b: Record<string, unknown>, create: boolean) {
  if (create || b.label !== undefined) vLabel(v, b.label);
  if (b.url !== undefined && (typeof b.url !== 'string' || !RE.httpsUrl.test(b.url.replace(/\/$/, '')))) v.err('url', 'V_FORMAT', 'url must be an https URL without a path (e.g. https://registry-1.docker.io)');
  // "" + "" = anonymous (public images)
  if (create || b.username !== undefined) vString(v, b.username, 'username', 0, 255);
  if (create || b.password !== undefined) vString(v, b.password, 'password', 0, 8192);
}
/** username and password are both empty (anonymous) or both set. */
function checkRegistryPair(username: string, password: string) {
  if (!username !== !password) throw E.bad(username ? 'password' : 'username', 'V_REQUIRED', 'username and password are both required (or both empty for anonymous access)');
}
async function loadRegistry(id: string): Promise<Registry> {
  const s = await col.registries().doc(id).get();
  if (!s.exists) throw E.notFound('registry not found');
  return s.data() as Registry;
}
/** A repository an image of this registry uses (scope for the default login test), or null. */
async function registryRepository(registryId: string): Promise<string | null> {
  const imgs = await db().collectionGroup('images').where('registryId', '==', registryId).limit(1).get();
  if (imgs.empty) return null;
  return ((imgs.docs[0]!.data() as Image).source?.repository as string | undefined) ?? null;
}
function vRepository(v: V, val: unknown, path: string, required: boolean) {
  if (val === undefined || val === null) {
    if (required) v.err(path, 'V_REQUIRED', `${path} is required`);
    return;
  }
  if (typeof val !== 'string' || !RE.repository.test(val) || val.length > 255) v.err(path, 'V_FORMAT', `${path} must look like "library/redis", "acme/api" or "project/repo/image" (lower case)`);
}

// ───────────────────────────────────────── services
async function validateServiceFields(v: V, b: Record<string, unknown>, create: boolean) {
  if (create || b.label !== undefined) vLabel(v, b.label);
  if (create) vSlug(v, b.serviceSlug, 'serviceSlug');
  vIconFormat(v, b.icon, 'icon');
  vDescription(v, b.description);
  if (v.ok) await vIconRefs(v, [{ path: 'icon', value: b.icon }]);
}
async function loadService(slug: string): Promise<Service> {
  const s = await col.services().doc(slug).get();
  if (!s.exists) throw E.notFound('service not found');
  return serviceView(s.data()!, s.id);
}
async function loadImage(serviceSlug: string, version: string): Promise<Image> {
  const s = await col.images(serviceSlug).doc(version).get();
  if (!s.exists) throw E.notFound('image not found');
  return imageView(s.data()!, s.id, serviceSlug);
}
/** Parse a reference + resolve registryId (undefined → match by host, first by label; null → public). */
async function resolveSource(v: V, reference: unknown, registryId: unknown): Promise<{ source: ImageRefParts; reg: Registry | null; registryId: string | null } | null> {
  const pr = parseImageReference(reference);
  if (!pr.ok) {
    v.err('reference', 'V_REFERENCE', pr.error);
    return null;
  }
  if (registryId !== undefined && registryId !== null && (typeof registryId !== 'string' || !registryId)) {
    v.err('registryId', 'V_TYPE', 'registryId must be a registry id or null');
    return null;
  }
  let reg: Registry | null = null;
  if (typeof registryId === 'string') {
    reg = await loadRegistry(registryId);
    if (registryHost(reg.url) !== pr.parts.host) {
      v.err('registryId', 'V_REF', `the registry "${reg.label}" is for ${registryHost(reg.url)}, but the reference is on ${pr.parts.host}`);
      return null;
    }
  } else if (registryId === undefined) {
    const all = (await col.registries().get()).docs.map((d) => d.data() as Registry);
    reg = matchRegistries(pr.parts.host, all)[0] ?? null;
  }
  return { source: pr.parts, reg, registryId: reg?.registryId ?? null };
}

// ───────────────────────────────────────── inspect helpers (§6.1)
export function prefillFromConfig(cfg: OciConfig): Pick<InspectImageResponse, 'entrypoint' | 'command' | 'workdir' | 'user' | 'ports' | 'env' | 'volumes'> {
  const c = cfg.config ?? {};
  const ports: ImagePort[] = [];
  for (const k of Object.keys(c.ExposedPorts ?? {})) {
    const m = /^(\d+)(?:\/(tcp|udp))?$/.exec(k);
    if (!m) continue;
    const port = Number(m[1]);
    const protocol = (m[2] ?? 'tcp') as 'tcp' | 'udp';
    const name = protocol === 'udp' && ports.some((p) => p.name === `p${port}`) ? `p${port}-udp` : `p${port}`;
    if (port >= 1 && port <= 65535 && !ports.some((p) => p.name === name)) ports.push({ name, port, protocol, description: '' });
  }
  const env: ImageEnv[] = [];
  for (const kv of c.Env ?? []) {
    const i = kv.indexOf('=');
    const name = i < 0 ? kv : kv.slice(0, i);
    const value = i < 0 ? '' : kv.slice(i + 1);
    if (!RE.envName.test(name) || env.some((e) => e.name === name)) continue;
    env.push({ name, value: value.slice(0, LIMITS.value), required: false, secret: false, description: '' });
  }
  // volume names from the path ("/var/lib/data" → "data"), so they do not read like image versions (v1); unique, slug-safe
  const used = new Set<string>();
  const volumes: ImageVolume[] = Object.keys(c.Volumes ?? {}).map((mountPath, i) => {
    const last = mountPath.split('/').filter(Boolean).pop() ?? '';
    let base = last.toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^[^a-z]+/, '').replace(/-+$/, '').slice(0, 32);
    if (base.length < 2) base = `volume-${i + 1}`;
    let name = base;
    for (let n = 2; used.has(name); n++) name = `${base}-${n}`;
    used.add(name);
    return { name, mountPath, kind: 'dir' as const, description: '' }; // docker VOLUME entries are folders (§21 #83)
  });
  return {
    entrypoint: c.Entrypoint?.length ? c.Entrypoint : null,
    command: c.Cmd?.length ? c.Cmd : null,
    workdir: c.WorkingDir ? c.WorkingDir : null,
    user: c.User ? c.User : null,
    ports,
    env,
    volumes,
  };
}
function registryErr(e: unknown): never {
  if (isRegistryAccessError(e)) throw E.unavailable(e.message, { repository: e.repository });
  if (e instanceof RegistryError) {
    if (e.status === 404) throw E.notFound(e.message);
    if (/platform .* not in the image/.test(e.message)) throw E.bad('platform', 'V_ENUM', e.message);
    throw E.unavailable(e.message);
  }
  throw e;
}

// ───────────────────────────────────────── not-found pages
const utf8Len = (s: string) => Buffer.byteLength(s, 'utf8');

export const catalogHandlers: Pick<
  Handlers,
  | 'getMe'
  | 'listRegistries'
  | 'createRegistry'
  | 'getRegistry'
  | 'patchRegistry'
  | 'deleteRegistry'
  | 'revealRegistryPassword'
  | 'testRegistry'
  | 'listRegistryRepositories'
  | 'listRegistryTags'
  | 'listServices'
  | 'createService'
  | 'getService'
  | 'patchService'
  | 'deleteService'
  | 'serviceUsage'
  | 'listImages'
  | 'inspectImage'
  | 'registerImage'
  | 'getImage'
  | 'patchImage'
  | 'deleteImage'
  | 'retryImport'
  | 'imageUsage'
  | 'listNotFoundPages'
  | 'createNotFoundPage'
  | 'getNotFoundPage'
  | 'deleteNotFoundPage'
  | 'listTags'
  | 'createTag'
  | 'patchTag'
  | 'deleteTag'
> = {
  getMe,

  // registries
  async listRegistries(c) {
    need(c, 'registries', 'read');
    const q = await col.registries().get();
    const items = q.docs.map((d) => registryView(d.data() as Registry)).sort((a, b) => a.label.localeCompare(b.label));
    return { items };
  },
  async createRegistry(c) {
    need(c, 'registries', 'write');
    const b = asObject(c.json());
    onlyKeys(b, ['label', 'url', 'username', 'password']);
    const v = new V();
    validateRegistryFields(v, b, true);
    v.throwIf();
    const r = b as unknown as CreateRegistryRequest;
    checkRegistryPair(r.username, r.password);
    const ref = col.registries().doc();
    const doc: Registry = {
      registryId: ref.id,
      label: r.label.trim(),
      url: (r.url ?? 'https://registry-1.docker.io').replace(/\/$/, ''),
      username: r.username,
      password: r.password,
      lastCheck: null,
      ...audited(uidOf(c)),
    };
    await ref.create(doc);
    return registryView(doc);
  },
  async getRegistry(c) {
    need(c, 'registries', 'read');
    return registryView(await loadRegistry(c.params.registryId!));
  },
  async patchRegistry(c) {
    need(c, 'registries', 'write');
    const b = asObject(c.json());
    onlyKeys(b, ['label', 'url', 'username', 'password'], ['registryId', 'lastCheck', 'createdAt', 'createdBy']);
    const v = new V();
    validateRegistryFields(v, b, false);
    v.throwIf();
    const p = b as PatchRegistryRequest;
    const ref = col.registries().doc(c.params.registryId!);
    const out = await db().runTransaction(async (tx) => {
      const s = await tx.get(ref);
      if (!s.exists) throw E.notFound('registry not found');
      const cur = s.data() as Registry;
      const next: Registry = { ...cur, ...touched(uidOf(c)) };
      if (p.label !== undefined) next.label = p.label.trim();
      if (p.url !== undefined) next.url = p.url.replace(/\/$/, '');
      if (p.username !== undefined) next.username = p.username;
      if (p.password !== undefined) next.password = p.password;
      checkRegistryPair(next.username, next.password);
      if (next.url !== cur.url || next.username !== cur.username || next.password !== cur.password) next.lastCheck = null;
      tx.set(ref, next);
      return next;
    });
    return registryView(out);
  },
  async deleteRegistry(c) {
    need(c, 'registries', 'write');
    const id = c.params.registryId!;
    await loadRegistry(id);
    const imgs = await db().collectionGroup('images').where('registryId', '==', id).get();
    if (!imgs.empty) {
      throw E.conflict('the registry is used by images', {
        usedBy: imgs.docs.map((d) => ({ serviceSlug: d.ref.parent.parent!.id, version: d.id })),
      });
    }
    await col.registries().doc(id).delete();
    return { deleted: true };
  },
  async revealRegistryPassword(c) {
    need(c, 'registries', 'write');
    const id = c.params.registryId!;
    const r = await loadRegistry(id);
    // audited by the router hook (registry.password-revealed); no-store + rate limit in the router (§21 #84)
    const res: RevealPasswordResponse = { value: r.password, password: r.password, hideAfterSec: REVEAL_SECONDS };
    return res;
  },
  async testRegistry(c) {
    need(c, 'registries', 'write');
    const id = c.params.registryId!;
    // optional JSON body { repository?: string } (not in the shared contract: body 'none'): check pull access to it
    let asked: string | null = null;
    if (c.body.length) {
      const b = asObject(c.json());
      onlyKeys(b, ['repository']);
      const v = new V();
      vRepository(v, b.repository, 'repository', false);
      v.throwIf();
      asked = (b.repository as string | null | undefined) ?? null;
    }
    const r = await loadRegistry(id);
    const repo = asked ?? (await registryRepository(id));
    const res = await new RegistryClient(r).test(repo ? `repository:${normalizeRepository(r.url, repo)}:pull` : null, !!asked);
    // Docker Hub: GET /v2/ answers 200 for any token; the access claim of a repository token is the real check
    if (res.accessDenied) res.message = await accessHint(r, res.message);
    const at = nowIso();
    await col.registries().doc(id).update({ lastCheck: { at, ok: res.ok, message: res.message } });
    return { ok: res.ok, message: res.message, at };
  },
  async listRegistryRepositories(c) {
    need(c, 'images', 'write');
    const r = await loadRegistry(c.params.registryId!);
    const q = (c.query.get('q') ?? '').trim().slice(0, 200);
    return listRepositories(r, q);
  },
  async listRegistryTags(c) {
    need(c, 'images', 'write');
    const repository = (c.query.get('repository') ?? '').trim();
    const v = new V();
    vRepository(v, repository, 'repository', true);
    v.throwIf();
    const r = await loadRegistry(c.params.registryId!);
    const q = (c.query.get('q') ?? '').trim().slice(0, 200);
    return listTags(r, normalizeRepository(r.url, repository), q);
  },

  // services
  async listServices(c) {
    need(c, 'services', 'read');
    const [svc, imgs, usage] = await Promise.all([col.services().get(), db().collectionGroup('images').select().get(), scanUsage()]);
    const counts = new Map<string, number>();
    for (const d of imgs.docs) {
      const slug = d.ref.parent.parent!.id;
      counts.set(slug, (counts.get(slug) ?? 0) + 1);
    }
    const usedBy = new Map<string, Set<string>>();
    for (const u of usage.entries) if (u.kind === 'deployment' && u.serviceSlug) (usedBy.get(u.serviceSlug) ?? usedBy.set(u.serviceSlug, new Set()).get(u.serviceSlug)!).add(u.id);
    const items = svc.docs
      .map((d) => {
        const s = serviceView(d.data(), d.id);
        return { ...s, imageCount: counts.get(d.id) ?? 0, usedBy: usedBy.get(s.serviceSlug)?.size ?? 0 };
      })
      .sort((a, b) => a.label.localeCompare(b.label));
    return { items };
  },
  async createService(c) {
    need(c, 'services', 'write');
    const b = asObject(c.json());
    onlyKeys(b, ['label', 'serviceSlug', 'icon', 'description']);
    const v = new V();
    await validateServiceFields(v, b, true);
    v.throwIf();
    const r = b as unknown as CreateServiceRequest;
    const doc: Service = { serviceSlug: r.serviceSlug, label: r.label.trim(), icon: r.icon ?? null, description: r.description ?? '', ...audited(uidOf(c)) };
    try {
      await col.services().doc(r.serviceSlug).create(doc);
    } catch (e) {
      if (isAlreadyExists(e)) throw E.conflict(`service "${r.serviceSlug}" exists`, { field: 'serviceSlug' });
      throw e;
    }
    return doc;
  },
  async getService(c) {
    need(c, 'services', 'read');
    return loadService(c.params.serviceSlug!);
  },
  async patchService(c) {
    need(c, 'services', 'write');
    const b = asObject(c.json());
    onlyKeys(b, ['label', 'icon', 'description'], ['serviceSlug', 'createdAt', 'createdBy']);
    const v = new V();
    await validateServiceFields(v, b, false);
    v.throwIf();
    const p = b as PatchServiceRequest;
    const ref = col.services().doc(c.params.serviceSlug!);
    return db().runTransaction(async (tx) => {
      const s = await tx.get(ref);
      if (!s.exists) throw E.notFound('service not found');
      const next: Service = { ...serviceView(s.data()!, s.id), ...touched(uidOf(c)) };
      if (p.label !== undefined) next.label = p.label.trim();
      if (p.icon !== undefined) next.icon = p.icon;
      if (p.description !== undefined) next.description = p.description;
      tx.set(ref, next);
      return next;
    });
  },
  async deleteService(c) {
    need(c, 'services', 'write');
    const ref = col.services().doc(c.params.serviceSlug!);
    await db().runTransaction(async (tx) => {
      const s = await tx.get(ref);
      if (!s.exists) throw E.notFound('service not found');
      const imgs = await tx.get(ref.collection('images').limit(1));
      if (!imgs.empty) throw E.conflict('delete the images of the service first');
      tx.delete(ref);
    });
    return { deleted: true };
  },
  async serviceUsage(c) {
    need(c, 'services', 'read');
    const slug = c.params.serviceSlug!;
    await loadService(slug);
    const { entries } = await scanUsage();
    return { items: dedupeUsage(entries.filter((u) => u.serviceSlug === slug)) };
  },

  // images
  async listImages(c) {
    need(c, 'images', 'read');
    const slug = c.params.serviceSlug!;
    await loadService(slug);
    const q = await col.images(slug).orderBy('createdAt', 'desc').get();
    return { items: q.docs.map((d) => imageView(d.data(), d.id, slug)) };
  },
  async inspectImage(c) {
    need(c, 'images', 'write');
    const b = asObject(c.json());
    onlyKeys(b, ['reference', 'registryId', 'platform']);
    const r = b as unknown as InspectImageRequest;
    const v = new V();
    if (r.platform !== undefined && r.platform !== 'linux/amd64' && r.platform !== 'linux/arm64') v.err('platform', 'V_ENUM', 'platform must be linux/amd64 | linux/arm64');
    await loadService(c.params.serviceSlug!);
    const src = await resolveSource(v, r.reference, r.registryId);
    v.throwIf();
    const platform: Platform = r.platform ?? 'linux/amd64';
    try {
      const creds = sourceCreds(src!.source, src!.reg);
      const cl = new RegistryClient(creds);
      const repo = src!.source.repository;
      const pm = await withAccessHint(creds, () => cl.platformManifest(repo, sourceRef(src!.source), platform));
      const cfg = JSON.parse((await cl.blobBytes(repo, pm.manifest.config.digest)).toString('utf8')) as OciConfig;
      if (cfg.architecture && cfg.architecture !== platform.split('/')[1]) throw new RegistryError(`platform ${platform} not in the image (it is ${cfg.os}/${cfg.architecture})`);
      const res: InspectImageResponse = {
        digest: pm.digest,
        platform,
        sizeBytes: pm.manifest.layers.reduce((a, l) => a + (l.size ?? 0), 0),
        layers: pm.manifest.layers.length,
        ...prefillFromConfig(cfg),
      };
      return res;
    } catch (e) {
      registryErr(e);
    }
  },
  async registerImage(c) {
    need(c, 'images', 'write');
    const b = asObject(c.json());
    onlyKeys(b, ['version', 'reference', 'registryId', 'platform', 'mode', 'kind', 'entrypoint', 'command', 'workdir', 'user', 'ports', 'env', 'volumes', 'healthcheck', 'stopTimeoutSec', 'description']);
    const is = validateRegisterImage(b);
    if (!is.ok) throw E.invalid(is.errors);
    const r = b as unknown as RegisterImageRequest;
    const serviceSlug = c.params.serviceSlug!;
    await loadService(serviceSlug);
    const v = new V();
    const src = await resolveSource(v, r.reference, r.registryId);
    v.throwIf();
    const ref = col.images(serviceSlug).doc(r.version);
    const doc: Image = {
      version: r.version,
      serviceSlug,
      reference: r.reference.trim(),
      source: src!.source,
      registryId: src!.registryId,
      digest: src!.source.digest,
      platform: r.platform ?? 'linux/amd64',
      import: { mode: r.mode, status: 'pending', error: null, sizeBytes: null, manifestMediaType: null, configDigest: null, layers: [], startedAt: null, finishedAt: null },
      kind: r.kind,
      entrypoint: r.entrypoint ?? null,
      command: r.command ?? null,
      workdir: r.workdir ?? null,
      user: r.user ?? null,
      ports: (r.ports ?? []).map((p) => ({ name: p.name, port: p.port, protocol: p.protocol, description: p.description ?? '' })),
      env: (r.env ?? []).map((e) => normalizeImageEnv({ name: e.name, value: e.value, options: e.options ?? null, required: !!e.required, secret: !!e.secret, description: e.description ?? '' })),
      volumes: (r.volumes ?? []).map((x) => normalizeImageVolume({ name: x.name, mountPath: x.mountPath, kind: x.kind, description: x.description ?? '' })), // kind explicit (absent → inferred, §21 #83)
      healthcheck: r.healthcheck ?? null,
      stopTimeoutSec: r.stopTimeoutSec ?? 30,
      description: r.description ?? '',
      ...audited(uidOf(c)),
    };
    try {
      await ref.create(doc);
    } catch (e) {
      if (isAlreadyExists(e)) throw E.conflict(`version ${r.version} exists`, { field: 'version', reason: 'version-exists' });
      throw e;
    }
    return doc;
  },
  async getImage(c) {
    need(c, 'images', 'read');
    return loadImage(c.params.serviceSlug!, c.params.version!);
  },
  async patchImage(c) {
    need(c, 'images', 'write');
    const b = asObject(c.json());
    onlyKeys(b, PATCH_IMAGE_KEYS, ['version', 'serviceSlug', 'source', 'digest', 'platform', 'import', 'mode', 'layers', 'createdAt', 'createdBy', 'updatedAt', 'updatedBy']);
    const p = b as PatchImageRequest;
    if (p.renameEverywhere !== undefined && typeof p.renameEverywhere !== 'boolean') throw E.bad('renameEverywhere', 'V_TYPE', 'renameEverywhere must be true / false');
    const slug = c.params.serviceSlug!;
    const version = c.params.version!;
    const ref = col.images(slug).doc(version);
    // the source (reference / registry) can be fixed only while the import failed (then retry-import)
    let src: Awaited<ReturnType<typeof resolveSource>> = null;
    if (p.reference !== undefined || p.registryId !== undefined) {
      const cur0 = await loadImage(slug, version);
      const sv = new V();
      src = await resolveSource(sv, p.reference ?? cur0.reference, p.registryId === undefined && p.reference === undefined ? cur0.registryId : p.registryId);
      sv.throwIf();
    }
    const uid = uidOf(c);
    const res = await db().runTransaction(async (tx) => {
      const s = await tx.get(ref);
      if (!s.exists) throw E.notFound('image not found');
      const cur = imageView(s.data()!, s.id, slug);
      cur.env = (cur.env ?? []).map(normalizeImageEnv);
      const next: Image = clone(cur);
      if (src) {
        const changed = next.reference !== (p.reference ?? cur.reference).trim() || next.registryId !== src.registryId;
        if (changed && cur.import.status !== 'failed') throw E.conflict(`reference / registryId can change only while the import failed (status ${cur.import.status})`, { field: p.reference !== undefined ? 'reference' : 'registryId' });
        if (changed) {
          next.reference = (p.reference ?? cur.reference).trim();
          next.source = src.source;
          next.registryId = src.registryId;
          next.digest = src.source.digest;
        }
      }
      applyImageEdits(next, p);
      // the same rules as registration (shared validateImageSpec) on the merged image
      const is = new Issues('publish');
      validateImageSpec(next as unknown as Record<string, unknown>, is);
      const ch = imageNameChanges(cur, next, p.renames ?? []);
      if (!is.ok || ch.issues.length) throw E.invalid([...is.errors, ...ch.issues]);
      next.env = next.env.map(normalizeImageEnv);
      next.volumes = next.volumes.map(normalizeImageVolume);

      // ── impact: renamed / removed names still used by templates + deployment drafts (heads keep their ImageSpec copy).
      // §21 #94: deployments are DETACHED from templates — rename everywhere rewrites TEMPLATES only; the drafts that use
      // a renamed name are listed (details / audit) and need a Template sync or a manual edit.
      const changes = [...ch.renamed.map((r) => ({ kind: r.kind, name: r.from, change: 'renamed' as const, to: r.to })), ...ch.removed.map((r) => ({ kind: r.kind, name: r.name, change: 'removed' as const, to: null }))];
      const touchedTpl: { ref: DocRef; t: Template; before: Template; count: number; commit?: { commitId: string; seq: number } }[] = [];
      const notRewritten: { deploymentId: string; label: string; references: number }[] = [];
      if (changes.length) {
        const [tq, dq] = await Promise.all([tx.get(col.templates()), tx.get(col.deployments().where('deleted', '==', false))]);
        const tpls = tq.docs.map((d) => ({ ref: d.ref, t: decodeTemplate(d.data()) })).filter((x) => x.t.sites.some((st) => st.config.containers.some((k) => k.serviceSlug === slug && k.version === version)));
        const deps = dq.docs.map((d) => ({ ref: d.ref, d: decodeDeployment(d.data()) })).filter((x) => x.d.draft.sites.some((st) => st.config.containers.some((k) => k.serviceSlug === slug && k.version === version)));
        const items: ImageImpactItem[] = changes.map((x) => {
          const usedBy: ImageNameUse[] = [];
          for (const { t } of tpls) for (const u of imageNameUses(t, slug, version, x.kind, x.name)) usedBy.push({ kind: 'template', templateId: t.templateId, label: t.label, source: 'template', ...u });
          for (const { d } of deps) for (const u of imageNameUses(d.draft, slug, version, x.kind, x.name)) usedBy.push({ kind: 'deployment', deploymentId: d.deploymentId, label: d.label, source: 'draft', ...u });
          return { ...x, usedBy };
        });
        const blocking = items.filter((x) => x.usedBy.length && (x.change === 'removed' || !p.renameEverywhere));
        if (blocking.length) {
          const removed = blocking.filter((x) => x.change === 'removed').length;
          const details: ImageInUseDetails = { reason: 'in-use', items: blocking };
          throw E.conflict(`${blocking.length} renamed / removed name${blocking.length === 1 ? ' is' : 's are'} still used by templates or deployment drafts${removed ? ' (removals stay blocked until those containers stop using them)' : ' — rename everywhere (rewrites templates only; deployment drafts keep the old names until a Template sync or a manual edit), or cancel'}`, details as unknown as Record<string, unknown>);
        }
        const renames = ch.renamed.filter((r) => items.some((x) => x.change === 'renamed' && x.kind === r.kind && x.name === r.from && x.usedBy.length));
        if (p.renameEverywhere && renames.length) {
          const issues: ValidationIssue[] = [];
          for (const x of tpls) {
            const r = renameImageNames(x.t, slug, version, renames);
            issues.push(...r.issues.map((i) => ({ ...i, path: `templates[${x.t.templateId}].${i.path}` })));
            if (r.count) touchedTpl.push({ ref: x.ref, t: r.config, before: x.t, count: r.count });
          }
          // deployment drafts are never rewritten (§21 #94): count their references for the audit entry
          for (const x of deps) {
            const n = items.filter((i) => i.change === 'renamed').reduce((a, i) => a + i.usedBy.filter((u) => u.kind === 'deployment' && u.deploymentId === x.d.deploymentId).length, 0);
            if (n) notRewritten.push({ deploymentId: x.d.deploymentId, label: x.d.label, references: n });
          }
          if (issues.length) throw E.invalid(issues, issues[0]!.message);
          // §21 #105: no working copy — the rewrite is ONE commit per affected template (the person as author)
          const heads = await Promise.all(touchedTpl.map((x) => (x.t.branch?.head ? loadCommit({ kind: 'template', templateId: x.t.templateId }, x.t.branch.head.commitId, tx, { human: true }) : Promise.resolve(null))));
          const now = nowIso();
          const message = `Rename everywhere: ${renames.map((r) => `${slug}:${version} ${r.kind} ${r.from} → ${r.to}`).join(', ')}`.slice(0, 200);
          for (let i = 0; i < touchedTpl.length; i++) {
            const x = touchedTpl[i]!;
            const head = heads[i] ?? null;
            const config: Config = { variables: x.t.variables, secrets: x.t.secrets.map((z) => ({ ...z, value: null, rotatedAt: null })), files: x.t.files, sites: x.t.sites };
            const commit = makeCommit({ commitId: newCommitId(), branch: { kind: 'template', templateId: x.t.templateId }, parent: head, kind: 'change', message, author: personAuthor(c), createdAt: now, config, catalog: null, source: null });
            const external = await storeCommitBody(commit);
            await writeCommit(tx, commit, external);
            const branch: BranchState = { head: commitRef(commit), origin: x.t.branch?.origin ?? null, commitCount: (x.t.branch?.commitCount ?? 0) + 1 };
            x.commit = { commitId: commit.commitId, seq: commit.seq };
            tx.set(x.ref, encodeTemplate({ ...x.t, version: x.t.version + 1, updatedAt: now, updatedBy: uid, branch }));
          }
        }
      }
      Object.assign(next, touched(uid));
      tx.set(ref, next);
      return { next, cur, ch, touchedTpl, notRewritten };
    });
    const { next, ch, touchedTpl, notRewritten } = res;
    c.audit = {
      data: {
        ...(ch.renamed.length ? { renamed: ch.renamed } : {}),
        ...(ch.removed.length ? { removed: ch.removed } : {}),
        ...(ch.added.length ? { added: ch.added } : {}),
        ...(ch.portRetargeted.length ? { portRetargeted: ch.portRetargeted } : {}),
        ...(touchedTpl.length || notRewritten.length
          ? { renamedEverywhere: { templates: touchedTpl.map((x) => ({ templateId: x.t.templateId, references: x.count, commit: x.commit ?? null })), deploymentsNotRewritten: notRewritten } }
          : {}),
      },
    };
    // one root audit entry per rewritten template (deployment drafts are never rewritten, §21 #94)
    for (const x of touchedTpl) {
      const diff = auditDiff(x.before as unknown as Record<string, unknown>, { ...x.t, version: x.t.version + 1 } as unknown as Record<string, unknown>);
      await writeAuditEntry({
        op: c.op,
        actor: uid,
        actorEmail: c.account?.email ?? null,
        action: 'template.committed',
        outcome: 'ok',
        status: 200,
        errorCode: null,
        errorMessage: null,
        targetType: 'template',
        target: `templates/${x.t.templateId}`,
        requestId: c.requestId,
        ip: c.ip || null,
        userAgent: (c.headers['user-agent'] ?? '').slice(0, 300) || null,
        changes: diff.changes,
        changesTotal: diff.total,
        data: { reason: 'image-rename-everywhere', image: `${slug}:${version}`, renames: ch.renamed, references: x.count, version: x.t.version + 1, commit: x.commit ?? null },
      }).catch(() => undefined);
    }
    return next;
  },
  async deleteImage(c) {
    need(c, 'images', 'write');
    const { serviceSlug, version } = c.params as { serviceSlug: string; version: string };
    await loadImage(serviceSlug, version);
    const { entries } = await scanUsage({ proposals: true });
    const used = entries.filter((u) => u.serviceSlug === serviceSlug && u.version === version);
    if (used.length) throw E.conflict('the image is in use', { usedBy: dedupeUsage(used) });
    await col.images(serviceSlug).doc(version).delete();
    return { deleted: true };
  },
  async retryImport(c) {
    need(c, 'images', 'write');
    const slug = c.params.serviceSlug!;
    const ref = col.images(slug).doc(c.params.version!);
    return db().runTransaction(async (tx) => {
      const s = await tx.get(ref);
      if (!s.exists) throw E.notFound('image not found');
      const cur = s.data() as Image;
      if (cur.import.status !== 'failed') throw E.conflict(`import status is ${cur.import.status} (retry only from failed)`);
      tx.update(ref, { 'import.status': 'pending', 'import.error': null, 'import.startedAt': null, 'import.finishedAt': null, ...touched(uidOf(c)) });
      const next = imageView({ ...cur, import: { ...cur.import, status: 'pending', error: null, startedAt: null, finishedAt: null }, ...touched(uidOf(c)) }, s.id, slug);
      return next;
    });
  },
  async imageUsage(c) {
    need(c, 'images', 'read');
    const { serviceSlug, version } = c.params as { serviceSlug: string; version: string };
    await loadImage(serviceSlug, version);
    const { entries } = await scanUsage();
    return { items: dedupeUsage(entries.filter((u) => u.serviceSlug === serviceSlug && u.version === version && u.source !== 'proposal')) };
  },

  // not-found pages
  async listNotFoundPages(c) {
    need(c, 'notFoundPages', 'read');
    const [q, usage] = await Promise.all([col.notFoundPages().get(), scanUsage()]);
    const items: NotFoundPageListItem[] = q.docs
      .map((d) => {
        const p = d.data() as NotFoundPage & { _sizeBytes?: number };
        const used = new Set(usage.entries.filter((u) => u.notFoundPageId === d.id).map((u) => `${u.kind}:${u.id}`));
        return { notFoundPageId: p.notFoundPageId, label: p.label, createdAt: p.createdAt, createdBy: p.createdBy, usedBy: used.size, sizeBytes: p._sizeBytes ?? utf8Len(p.htmlCode) };
      })
      .sort((a, b) => a.label.localeCompare(b.label));
    return { items };
  },
  async createNotFoundPage(c) {
    need(c, 'notFoundPages', 'write');
    const b = asObject(c.json());
    onlyKeys(b, ['label', 'htmlCode']);
    const v = new V();
    vLabel(v, b.label);
    if (typeof b.htmlCode !== 'string') v.err('htmlCode', 'V_REQUIRED', 'htmlCode is required');
    v.throwIf();
    const r = b as unknown as CreateNotFoundPageRequest;
    const size = utf8Len(r.htmlCode);
    if (size > LIMITS.notFoundPageBytes) throw E.tooLarge(`htmlCode is ${size} bytes (max ${LIMITS.notFoundPageBytes})`);
    const ref = col.notFoundPages().doc();
    const doc: NotFoundPage = { notFoundPageId: ref.id, label: r.label.trim(), htmlCode: r.htmlCode, createdAt: nowIso(), createdBy: uidOf(c) };
    await ref.create({ ...doc, _sizeBytes: size });
    return doc;
  },
  async getNotFoundPage(c) {
    need(c, 'notFoundPages', 'read');
    const s = await col.notFoundPages().doc(c.params.notFoundPageId!).get();
    if (!s.exists) throw E.notFound('not-found page not found');
    const { entries } = await scanUsage();
    const usedBy = dedupeUsage(entries.filter((u) => u.notFoundPageId === s.id));
    return { ...pub<NotFoundPage>(s.data()), usedBy };
  },
  async deleteNotFoundPage(c) {
    need(c, 'notFoundPages', 'write');
    const id = c.params.notFoundPageId!;
    const s = await col.notFoundPages().doc(id).get();
    if (!s.exists) throw E.notFound('not-found page not found');
    const { entries } = await scanUsage();
    const used = entries.filter((u) => u.notFoundPageId === id);
    if (used.length) throw E.conflict('the page is in use', { usedBy: dedupeUsage(used) });
    await s.ref.delete();
    return { deleted: true };
  },

  // tags
  async listTags(c) {
    need(c, 'tags', 'read');
    const [q, deps] = await Promise.all([col.tags().get(), col.deployments().where('deleted', '==', false).select('tagSlugs').get()]);
    const counts = new Map<string, number>();
    for (const d of deps.docs) for (const t of (d.data().tagSlugs as string[]) ?? []) counts.set(t, (counts.get(t) ?? 0) + 1);
    const items = q.docs.map((d) => ({ ...pub<Tag>(d.data()), deployments: counts.get(d.id) ?? 0 })).sort((a, b) => a.label.localeCompare(b.label));
    return { items };
  },
  async createTag(c) {
    need(c, 'tags', 'write');
    const b = asObject(c.json());
    onlyKeys(b, ['tagSlug', 'label', 'icon', 'color']);
    const v = new V();
    vSlug(v, b.tagSlug, 'tagSlug');
    vLabel(v, b.label);
    vIconFormat(v, b.icon, 'icon');
    if (typeof b.color !== 'string' || !RE.color.test(b.color)) v.err('color', 'V_FORMAT', 'color must match ^#[0-9a-f]{6}$');
    if (v.ok) await vIconRefs(v, [{ path: 'icon', value: b.icon }]);
    v.throwIf();
    const r = b as unknown as CreateTagRequest;
    const doc: Tag = { tagSlug: r.tagSlug, label: r.label.trim(), icon: r.icon ?? null, color: r.color, ...audited(uidOf(c)) };
    try {
      await col.tags().doc(r.tagSlug).create(doc);
    } catch (e) {
      if ((e as { code?: number }).code === 6) throw E.conflict(`tag "${r.tagSlug}" exists`, { field: 'tagSlug' });
      throw e;
    }
    return doc;
  },
  async patchTag(c) {
    need(c, 'tags', 'write');
    const b = asObject(c.json());
    onlyKeys(b, ['label', 'icon', 'color'], ['tagSlug']);
    const v = new V();
    vLabel(v, b.label, 'label', LIMITS.label, false);
    vIconFormat(v, b.icon, 'icon');
    if (b.color !== undefined && (typeof b.color !== 'string' || !RE.color.test(b.color))) v.err('color', 'V_FORMAT', 'color must match ^#[0-9a-f]{6}$');
    if (v.ok) await vIconRefs(v, [{ path: 'icon', value: b.icon }]);
    v.throwIf();
    const p = b as PatchTagRequest;
    const ref = col.tags().doc(c.params.tagSlug!);
    return db().runTransaction(async (tx) => {
      const s = await tx.get(ref);
      if (!s.exists) throw E.notFound('tag not found');
      const next: Tag = { ...(s.data() as Tag), ...touched(uidOf(c)) };
      if (p.label !== undefined) next.label = p.label.trim();
      if (p.icon !== undefined) next.icon = p.icon;
      if (p.color !== undefined) next.color = p.color;
      tx.set(ref, next);
      return next;
    });
  },
  async deleteTag(c) {
    need(c, 'tags', 'write');
    const slug = c.params.tagSlug!;
    const ref = col.tags().doc(slug);
    if (!(await ref.get()).exists) throw E.notFound('tag not found');
    const deps = await col.deployments().where('tagSlugs', 'array-contains', slug).get();
    const accounts = await col.accounts().get();
    const writes: { ref: DocRef; data: Record<string, unknown> }[] = [];
    let accessRules = 0;
    for (const d of deps.docs) writes.push({ ref: d.ref, data: { tagSlugs: FieldValue.arrayRemove(slug) } });
    for (const a of accounts.docs) {
      const acc = a.data() as Account;
      let changed = false;
      const access = [];
      for (const r of acc.access ?? []) {
        if (r.scope.kind === 'tags' && r.scope.tagSlugs.includes(slug)) {
          changed = true;
          accessRules++;
          const rest = r.scope.tagSlugs.filter((t) => t !== slug);
          if (rest.length) access.push({ ...r, scope: { kind: 'tags' as const, tagSlugs: rest } });
        } else access.push(r);
      }
      if (changed) writes.push({ ref: a.ref, data: { access } });
    }
    for (let i = 0; i < writes.length; i += 500) {
      const batch = db().batch();
      for (const w of writes.slice(i, i + 500)) batch.update(w.ref, w.data);
      await batch.commit();
    }
    await ref.delete();
    return { deleted: true, removedFrom: { deployments: deps.size, accessRules } };
  },
};
void decodeDeployment;
