/** Catalog loading (validation contexts, revision catalogs) and usage scans (templates + deployments). */
import {
  imageKey,
  normalizeImageVolume,
  PROPOSAL_TERMINAL,
  type CatalogImage,
  type Config,
  type Image,
  type ImageSpec,
  type NotFoundPage,
  type RevisionCatalog,
  type Service,
  type UsageItem,
  type ValidateMode,
  type ValidationContext,
} from '@opm/shared';
import { col, db, pub } from './fire.js';
import { decodeDeployment, decodeRevision, decodeTemplate, type StoredDeployment } from './codec.js';
import { E } from './errors.js';
import { embedIcons } from './icons.js';
import { imageVolumesFresh, setImageVolumes } from './config.js';

/**
 * Service document → API view (new shape: doc id = serviceSlug). Pre-migration docs (auto id + `slug`, `serviceId`,
 * `repository`) are read tolerantly: serviceSlug = slug ?? doc id; the old fields are dropped from the view.
 */
export function serviceView(d: Record<string, unknown>, docId?: string): Service {
  const p = pub<Record<string, unknown>>(d);
  const { serviceId, slug, repository, defaultRepository, ...rest } = p;
  void serviceId;
  void repository;
  void defaultRepository;
  return { ...(rest as unknown as Service), serviceSlug: (rest.serviceSlug as string) ?? (slug as string) ?? docId ?? '', icon: (rest.icon as Service['icon']) ?? null };
}
/** Image document → API view (pre-migration docs: version = tag / doc id, serviceSlug from the parent). */
export function imageView(d: Record<string, unknown>, docId?: string, parentSlug?: string): Image {
  const p = pub<Record<string, unknown>>(d);
  const { serviceId, tag, repository, ...rest } = p;
  void serviceId;
  void repository;
  const out = { ...(rest as unknown as Image), version: (rest.version as string) ?? (tag as string) ?? docId ?? '', serviceSlug: (rest.serviceSlug as string) ?? parentSlug ?? '' };
  // §21 #83: kind always explicit in the API (documents before the migration: inferred from the path)
  if (Array.isArray(out.volumes)) out.volumes = out.volumes.map((v) => (v && typeof v === 'object' ? normalizeImageVolume(v) : v));
  return out;
}

export interface Catalog {
  services: Record<string, Service>;
  images: Record<string, Image>;
  notFoundPageIds: string[];
  /**
   * §21 #113 site images (digest "", added on a site or adopted from its Docker) known to this write — from the target
   * head's catalog and the merged source commit's catalog. Only used for keys the cloud catalog does not have.
   */
  site?: RevisionCatalog;
}
/** §21 #113: the catalog plus the site images (digest "") of revision catalogs (target head, merge source). Pure. */
export function withSiteImages(cat: Catalog, ...from: (RevisionCatalog | null | undefined)[]): Catalog {
  const site: RevisionCatalog = { services: { ...(cat.site?.services ?? {}) }, images: { ...(cat.site?.images ?? {}) }, notFoundPages: {} };
  for (const rc of from) {
    if (!rc) continue;
    for (const [k, im] of Object.entries(rc.images ?? {})) {
      if (im.digest || cat.images[k] || site.images[k]) continue;
      site.images[k] = im;
      const s = rc.services?.[im.serviceSlug];
      if (s && !site.services[im.serviceSlug]) site.services[im.serviceSlug] = s;
      else if (!site.services[im.serviceSlug]) site.services[im.serviceSlug] = { serviceSlug: im.serviceSlug, label: im.serviceSlug, icon: null };
    }
  }
  return Object.keys(site.images).length ? { ...cat, site } : cat;
}
export async function loadCatalog(): Promise<Catalog> {
  const [svc, imgs, pages] = await Promise.all([col.services().get(), db().collectionGroup('images').get(), col.notFoundPages().select('label').get()]);
  const services: Record<string, Service> = {};
  for (const d of svc.docs) {
    const s = serviceView(d.data(), d.id);
    services[s.serviceSlug] = s;
  }
  const images: Record<string, Image> = {};
  for (const d of imgs.docs) {
    const i = imageView(d.data(), d.id, d.ref.parent.parent!.id);
    images[imageKey(i.serviceSlug, i.version)] = i;
  }
  const cat = { services, images, notFoundPageIds: pages.docs.map((d) => d.id) };
  setImageVolumes(imageVolumes(cat));
  return cat;
}
/** Refresh the image-volume cache used by normalize-on-read (round D) when it is older than its TTL. Never throws. */
export async function primeImageVolumes(): Promise<void> {
  if (imageVolumesFresh()) return;
  try {
    const q = await db().collectionGroup('images').select('volumes').get();
    const out: Record<string, Pick<CatalogImage, 'volumes'>> = {};
    for (const d of q.docs) {
      const parent = d.ref.parent.parent;
      if (!parent || parent.parent.id !== 'services') continue;
      const v = d.data().volumes;
      out[imageKey(parent.id, d.id)] = { volumes: Array.isArray(v) ? v : [] };
    }
    setImageVolumes(out);
  } catch {
    /* keep the previous cache; normalize-on-read then works without (or with stale) image volumes */
  }
}
export function catalogImage(i: Image): CatalogImage {
  return { kind: i.kind, ports: i.ports, env: i.env, volumes: i.volumes, healthcheck: i.healthcheck, importStatus: i.import.status };
}
/** Image volumes of every catalog image (normalizeSiteConfig makes legacy implicit image volumes explicit). */
export function imageVolumes(cat: Catalog): Record<string, Pick<CatalogImage, 'volumes'>> {
  const out: Record<string, Pick<CatalogImage, 'volumes'>> = {};
  for (const [k, i] of Object.entries(cat.images)) out[k] = { volumes: Array.isArray(i.volumes) ? i.volumes : [] };
  for (const [k, i] of Object.entries(cat.site?.images ?? {})) out[k] ??= { volumes: i.volumes ?? [] };
  return out;
}
/**
 * siteSlug → AgentDoc.advertiseHost of the enrolled sites of a deployment (round C: auto hosts, ValidationContext.detectedHosts).
 * Revoked enrollments have no agent doc; empty advertiseHost is skipped.
 */
export async function loadDetectedHosts(deploymentId: string): Promise<Record<string, string>> {
  const q = await col.agents(deploymentId).select('advertiseHost').get();
  const out: Record<string, string> = {};
  for (const d of q.docs) {
    const h = d.data().advertiseHost;
    if (typeof h === 'string' && h) out[d.id] = h;
  }
  return out;
}
export function validationCtx(mode: ValidateMode, deploymentId: string, cat: Catalog, secretsSet?: string[], detectedHosts?: Record<string, string>): ValidationContext {
  const services: ValidationContext['services'] = {};
  for (const [slug, s] of Object.entries(cat.services)) services[slug] = { label: s.label };
  const images: ValidationContext['images'] = {};
  for (const [k, i] of Object.entries(cat.images)) images[k] = catalogImage(i);
  // §21 #113 site images: valid on the deployment (the site's Docker has them; another site pulls their reference)
  for (const [slug, s] of Object.entries(cat.site?.services ?? {})) services[slug] ??= { label: s.label };
  for (const [k, i] of Object.entries(cat.site?.images ?? {})) images[k] ??= { kind: i.kind, ports: i.ports, env: i.env, volumes: i.volumes, healthcheck: i.healthcheck, importStatus: 'ready' };
  return { mode, deploymentId, services, images, notFoundPageIds: cat.notFoundPageIds, secretsSet, ...(detectedHosts ? { detectedHosts } : {}) };
}
export function imageSpec(i: Image): ImageSpec {
  if (i.import.status !== 'ready' || !i.digest || !i.import.configDigest) throw E.bad(`images[${i.serviceSlug}:${i.version}]`, 'V_IMAGE_NOT_READY', `image ${i.serviceSlug}:${i.version} is not imported yet`);
  return {
    serviceSlug: i.serviceSlug,
    version: i.version,
    digest: i.digest,
    configDigest: i.import.configDigest,
    platform: i.platform,
    manifestMediaType: i.import.manifestMediaType ?? 'application/vnd.oci.image.manifest.v1+json',
    layers: i.import.layers,
    sizeBytes: i.import.sizeBytes ?? i.import.layers.reduce((a, l) => a + l.sizeBytes, 0),
    kind: i.kind,
    entrypoint: i.entrypoint,
    command: i.command,
    workdir: i.workdir,
    user: i.user,
    ports: i.ports,
    env: i.env,
    volumes: i.volumes,
    healthcheck: i.healthcheck,
    stopTimeoutSec: i.stopTimeoutSec,
  };
}
/** services + ImageSpec for every container version + the not-found pages referenced (§6.1 Publish). Uploaded service icons are embedded as data: URLs. */
export async function buildRevisionCatalog(config: Config, cat: Catalog): Promise<RevisionCatalog> {
  const out: RevisionCatalog = { services: {}, images: {}, notFoundPages: {} };
  const pageIds = new Set<string>();
  for (const site of config.sites) {
    if (site.config.notFoundPageId) pageIds.add(site.config.notFoundPageId);
    for (const c of site.config.containers) {
      const s = cat.services[c.serviceSlug];
      const i = cat.images[imageKey(c.serviceSlug, c.version)];
      // §21 #113 a site image: carried as it is (no blobs in the cloud)
      const si = !i ? cat.site?.images[imageKey(c.serviceSlug, c.version)] : undefined;
      if (si) {
        const ss = cat.site!.services[si.serviceSlug] ?? { serviceSlug: si.serviceSlug, label: si.serviceSlug, icon: null };
        out.services[si.serviceSlug] ??= { serviceSlug: ss.serviceSlug, label: ss.label, icon: ss.icon ?? null };
        out.images[imageKey(c.serviceSlug, c.version)] = si;
        continue;
      }
      if (!s || !i) throw E.bad(`sites[${site.siteSlug}].config.containers[${c.containerSlug ?? c.serviceSlug}]`, 'V_REF', `image ${c.serviceSlug}:${c.version} (container ${c.containerSlug ?? c.serviceSlug}) does not exist`);
      // several instances of the same image share one entry (images are keyed <serviceSlug>:<version>)
      out.services[s.serviceSlug] = { serviceSlug: s.serviceSlug, label: s.label, icon: s.icon ?? null };
      out.images[imageKey(c.serviceSlug, c.version)] = imageSpec(i);
    }
  }
  const embedded = await embedIcons(Object.values(out.services).map((x) => x.icon));
  for (const x of Object.values(out.services)) if (x.icon && embedded.has(x.icon)) x.icon = embedded.get(x.icon)!;
  if (pageIds.size) {
    const snaps = await db().getAll(...[...pageIds].map((id) => col.notFoundPages().doc(id)));
    for (const sn of snaps) {
      if (!sn.exists) throw E.bad('sites', 'V_REF', `not-found page ${sn.id} does not exist`);
      const p = sn.data() as NotFoundPage;
      out.notFoundPages[sn.id] = { notFoundPageId: sn.id, label: p.label, htmlCode: p.htmlCode };
    }
  }
  return out;
}

// ───────────────────────────────────────── usage scans
export interface UsageEntry extends UsageItem {
  notFoundPageId: string | null;
  source: 'template' | 'draft' | 'head' | 'proposal';
}
function configUsage(kind: UsageItem['kind'], id: string, label: string, cfg: Pick<Config, 'sites'>, source: UsageEntry['source'], out: UsageEntry[]) {
  for (const site of cfg.sites ?? []) {
    if (site.config?.notFoundPageId) out.push({ kind, id, label, siteSlug: site.siteSlug, containerSlug: '', serviceSlug: '', version: '', notFoundPageId: site.config.notFoundPageId, source });
    // one row per container INSTANCE (legacy stored containers have no containerSlug: = serviceSlug)
    for (const c of site.config?.containers ?? []) out.push({ kind, id, label, siteSlug: site.siteSlug, containerSlug: c.containerSlug ?? c.serviceSlug, serviceSlug: c.serviceSlug, version: c.version, notFoundPageId: null, source });
  }
}
/** Everything that references catalog items: templates, deployment drafts, head revisions, (optionally) open proposals. */
export async function scanUsage(opts: { proposals?: boolean } = {}): Promise<{ entries: UsageEntry[]; deployments: StoredDeployment[] }> {
  const [tpls, deps] = await Promise.all([col.templates().get(), col.deployments().where('deleted', '==', false).get()]);
  const out: UsageEntry[] = [];
  for (const d of tpls.docs) {
    const t = decodeTemplate(d.data());
    configUsage('template', t.templateId, t.label, t, 'template', out);
  }
  const deployments = deps.docs.map((d) => decodeDeployment(d.data()));
  const headRefs = deployments.filter((d) => d.head).map((d) => col.revisions(d.deploymentId).doc(d.head!.revId));
  const heads = headRefs.length ? await db().getAll(...headRefs) : [];
  const headById = new Map<string, Config>();
  for (const h of heads) if (h.exists) headById.set(h.ref.parent.parent!.id, decodeRevision(h.data()!).config);
  for (const d of deployments) {
    configUsage('deployment', d.deploymentId, d.label, d.draft, 'draft', out);
    const h = headById.get(d.deploymentId);
    if (h) configUsage('deployment', d.deploymentId, d.label, h, 'head', out);
  }
  if (opts.proposals) {
    const open = await db().collectionGroup('proposals').where('status', 'in', ['published', 'staging', 'ready', 'approved', 'applying']).get();
    const revRefs = open.docs.map((p) => p.ref.parent.parent!.collection('revisions').doc(p.id));
    const revs = revRefs.length ? await db().getAll(...revRefs) : [];
    for (const r of revs) {
      if (!r.exists) continue;
      const depId = r.ref.parent.parent!.id;
      const d = deployments.find((x) => x.deploymentId === depId);
      if (!d) continue;
      configUsage('deployment', depId, d.label, decodeRevision(r.data()!).config, 'proposal', out);
    }
  }
  return { entries: out, deployments };
}
export function dedupeUsage(list: UsageEntry[]): UsageItem[] {
  const seen = new Set<string>();
  const out: UsageItem[] = [];
  for (const u of list) {
    const k = `${u.kind}|${u.id}|${u.siteSlug}|${u.containerSlug}|${u.serviceSlug}|${u.version}`;
    if (seen.has(k)) continue;
    seen.add(k);
    out.push({ kind: u.kind, id: u.id, label: u.label, siteSlug: u.siteSlug, containerSlug: u.containerSlug, serviceSlug: u.serviceSlug, version: u.version });
  }
  return out;
}
export const OPEN_STATUSES = ['published', 'staging', 'ready', 'approved', 'applying'] as const;
export const isTerminal = (s: string) => (PROPOSAL_TERMINAL as readonly string[]).includes(s);
