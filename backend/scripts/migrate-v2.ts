/**
 * migrate-v2 — whole-DB migration to the phase-U data model (ORCH.md "phase U — DESIGN CHANGES"):
 *   services/{autoId} {serviceId, slug, repository}          → services/{serviceSlug} {serviceSlug, label, icon, description}
 *   services/{id}/images/{tag} {serviceId, tag}               → services/{serviceSlug}/images/{version} {version, serviceSlug,
 *                                                                reference, source, registryId, …, env normalized (options always)}
 *   containers {serviceId, tag}, depends {serviceId}, edge proxy {serviceId} → {serviceSlug, version} / {serviceSlug}
 *     in templates (_body), deployments (_draft, _srcSnapshot), revisions (_config, _catalog; hash + catalogHash recomputed)
 *   icons: service / template / tag icon → "lucide:<name>" | "upload:<id>" | null; account img / deployment image → same or https URL
 *   serviceSlugs/* deleted (doc id = slug gives uniqueness)
 *   auditLog: old entries stay as written (old targets services/<autoId>…); one `system.migration` entry per --apply run with
 *     the counts and the id → slug / tag → version mapping.
 * Idempotent: a second run plans 0 changes. Writes happen in order: new docs + rewrites first, deletes last.
 *
 * Usage (from backend/):
 *   npx tsx scripts/migrate-v2.ts --data-dir <dir> --dry-run | --apply
 */
import { pathToFileURL } from 'node:url';
import type { DocStore as Firestore } from '../functions/src/ports/index.js';
import { auditActionKeys, canonicalize, normalizeImageEnv, parseImageReference, registryHost, RE, type ImageEnv, type ImageRefParts } from '@opm/shared';
import { jcsHash } from '../functions/src/core/util.ts';

// ═════════════════════════════════════════ USER-SPECIFIC FIXES (by document id — not generic rules)
/**
 * The user's real data (prod your-firebase-project): service 4WczlsAAaexWRXrzPvq4 (slug northwind-backend) and its image
 * backend-3.0.9, whose old service repository (northwind/backend) was wrong (Docker Hub 401).
 */
export const USER_FIXES: Record<string, { slug?: string; images: Record<string, { version: string; reference: string; resetImport: boolean; envOptions: Record<string, string[]> }> }> = {
  '4WczlsAAaexWRXrzPvq4': {
    slug: 'northwind-backend',
    images: {
      'backend-3.0.9': {
        version: 'v1',
        reference: 'docker.io/northwind/backend:3.0.9',
        resetImport: true, // pending → the import trigger starts the import right after the apply
        envOptions: {
          NODE_ENV: ['integration', 'dc', 'worker', 'pg-listen', 'terminal', 'migrate'], // default = the old first item (kept)
          LOG_IN_CONSOLE: ['false', 'true'], //                                          default = the current one (kept)
        },
      },
    },
  },
};
// ═════════════════════════════════════════

export type Op = { op: 'set'; path: string; data: Record<string, unknown>; why: string } | { op: 'update'; path: string; data: Record<string, unknown>; why: string } | { op: 'delete'; path: string; why: string };
export interface Plan {
  ops: Op[];
  services: Record<string, string>; //  old id → slug
  images: Record<string, string>; //    "<oldServiceId>:<tag>" → "<slug>:<version>"
  warnings: string[];
  counts: Record<string, number>;
}

type Doc = Record<string, unknown>;
const isObj = (v: unknown): v is Doc => !!v && typeof v === 'object' && !Array.isArray(v);
const str = (v: unknown): string | null => (typeof v === 'string' && v ? v : null);

/** service / template / tag icon */
export function normIcon(v: unknown): string | null {
  if (typeof v !== 'string' || !v) return null;
  if (/^lucide:[a-z0-9-]+$/.test(v) || /^upload:[A-Za-z0-9]{20}$/.test(v)) return v;
  if (/^[a-z0-9-]+$/.test(v)) return `lucide:${v}`;
  return null;
}
/** account img / deployment image */
export function normPicture(v: unknown): string | null {
  if (typeof v === 'string' && /^https:\/\/\S+$/.test(v)) return v;
  return normIcon(v);
}
const sameJson = (a: unknown, b: unknown) => canonicalize(a ?? null) === canonicalize(b ?? null);
const toVersion = (tag: string) => {
  const v = tag.toLowerCase().replace(/[^a-z0-9._-]/g, '-').replace(/^[^a-z0-9]+/, '').slice(0, 64);
  return RE.version.test(v) ? v : null;
};

export async function planMigrationV2(db: Firestore): Promise<Plan> {
  const plan: Plan = { ops: [], services: {}, images: {}, warnings: [], counts: {} };
  const count = (k: string) => (plan.counts[k] = (plan.counts[k] ?? 0) + 1);
  const deletes: Op[] = [];

  // mapping of earlier runs (if an interrupted run already deleted old service docs)
  const prev = await db.collection('auditLog').where('action', '==', 'system.migration').get();
  const prevServices: Record<string, string> = {};
  const prevImages: Record<string, string> = {};
  for (const d of prev.docs) {
    try {
      const data = JSON.parse((d.data()._data as string) ?? '{}');
      if (data.migration === 'v2') {
        Object.assign(prevServices, data.services ?? {});
        Object.assign(prevImages, data.images ?? {});
      }
    } catch {
      /* ignore */
    }
  }

  const registries = new Map((await db.collection('registries').get()).docs.map((d) => [d.id, d.data()]));

  // ───────── services + images
  const svcSnap = await db.collection('services').get();
  const takenSlugs = new Set(svcSnap.docs.filter((d) => !('serviceId' in d.data()) && !('slug' in d.data())).map((d) => d.id));
  for (const sd of svcSnap.docs) {
    const s = sd.data();
    const old = 'serviceId' in s || 'slug' in s || 'repository' in s || 'defaultRepository' in s || sd.id !== s.serviceSlug;
    const fix = USER_FIXES[sd.id];
    const slug = fix?.slug ?? str(s.serviceSlug) ?? str(s.slug) ?? sd.id;
    if (!RE.slug.test(slug)) {
      plan.warnings.push(`services/${sd.id}: slug "${slug}" is not valid — skipped`);
      continue;
    }
    if (old && sd.id !== slug && takenSlugs.has(slug)) {
      plan.warnings.push(`services/${sd.id}: services/${slug} already exists — skipped (merge by hand)`);
      continue;
    }
    takenSlugs.add(slug);
    if (sd.id !== slug) plan.services[sd.id] = slug;
    const legacyRepo = str(s.repository) ?? str(s.defaultRepository);
    const next: Doc = { serviceSlug: slug, label: s.label ?? slug, icon: normIcon(s.icon), description: s.description ?? '', createdAt: s.createdAt ?? null, createdBy: s.createdBy ?? null, updatedAt: s.updatedAt ?? null, updatedBy: s.updatedBy ?? null };
    for (const [k, v] of Object.entries(s)) if (k.startsWith('_')) next[k] = v;
    if (sd.id !== slug) {
      plan.ops.push({ op: 'set', path: `services/${slug}`, data: next, why: `service ${sd.id} → ${slug}` });
      deletes.push({ op: 'delete', path: `services/${sd.id}`, why: `old service doc (now services/${slug})` });
      count('services');
    } else if (!sameJson(s, next)) {
      plan.ops.push({ op: 'set', path: `services/${slug}`, data: next, why: 'service fields normalized' });
      count('services');
    }
    // images: old ones (no `version` field), plus env / icon normalization of new ones
    const imgs = (await sd.ref.collection('images').get()).docs.sort((a, b) => String(a.data().createdAt ?? '').localeCompare(String(b.data().createdAt ?? '')));
    const usedVersions = new Set(imgs.filter((d) => 'version' in d.data()).map((d) => d.id));
    let n = 0;
    const nextFree = () => {
      do n++;
      while (usedVersions.has(`v${n}`));
      return `v${n}`;
    };
    for (const idoc of imgs) {
      const i = idoc.data();
      const ifix = fix?.images[idoc.id];
      const isOld = !('version' in i) || 'tag' in i || 'serviceId' in i || 'repository' in i;
      let version = str(i.version) ?? idoc.id;
      if (isOld) {
        version = ifix?.version ?? (RE.version.test(idoc.id) && !usedVersions.has(idoc.id) ? idoc.id : (toVersion(idoc.id) && !usedVersions.has(toVersion(idoc.id)!) ? toVersion(idoc.id)! : nextFree()));
        usedVersions.add(version);
      }
      const reg = str(i.registryId) ? registries.get(i.registryId as string) : undefined;
      let reference = str(i.reference);
      let source = isObj(i.source) ? (i.source as unknown as ImageRefParts) : null;
      if (ifix) reference = ifix.reference;
      if (!reference) {
        const repo = str(i.repository) ?? legacyRepo;
        if (!repo) {
          plan.warnings.push(`services/${sd.id}/images/${idoc.id}: no repository to build a reference — skipped`);
          continue;
        }
        const host = reg ? registryHost(reg.url as string) : 'docker.io';
        const r = host === 'docker.io' && !repo.includes('/') ? `library/${repo}` : repo;
        reference = `${host}/${r}:${str(i.tag) ?? idoc.id}`;
      }
      const pr = parseImageReference(reference);
      if (!pr.ok) {
        plan.warnings.push(`services/${sd.id}/images/${idoc.id}: reference "${reference}" is invalid (${pr.error}) — skipped`);
        continue;
      }
      if (!source || ifix || !sameJson(source, pr.parts)) source = pr.parts;
      let env = (Array.isArray(i.env) ? (i.env as ImageEnv[]) : []).map((e) => normalizeImageEnv(e));
      if (ifix) {
        env = env.map((e) => {
          const opts = ifix.envOptions[e.name];
          if (!opts) return e;
          const value = e.value !== undefined && opts.includes(e.value) ? e.value : opts[0];
          if (value !== e.value) plan.warnings.push(`${slug}:${version} env ${e.name}: default "${String(e.value)}" is not an option → "${value}"`);
          return { ...e, options: opts, value };
        });
      }
      const { tag, serviceId, repository, ...keep } = i;
      void tag;
      void serviceId;
      void repository;
      const nextImg: Doc = { ...keep, version, serviceSlug: slug, reference, source, registryId: str(i.registryId), env };
      if (ifix?.resetImport) {
        nextImg.digest = source.digest;
        nextImg.import = { ...(isObj(i.import) ? i.import : {}), status: 'pending', error: null, startedAt: null, finishedAt: null, layers: [], sizeBytes: null, configDigest: null, manifestMediaType: null };
        delete nextImg._progress;
      }
      const newPath = `services/${slug}/images/${version}`;
      const oldPath = `services/${sd.id}/images/${idoc.id}`;
      if (isOld || newPath !== oldPath) plan.images[`${sd.id}:${idoc.id}`] = `${slug}:${version}`;
      if (newPath !== oldPath) {
        plan.ops.push({ op: 'set', path: newPath, data: nextImg, why: `image ${sd.id}:${idoc.id} → ${slug}:${version} (${reference})` });
        deletes.push({ op: 'delete', path: oldPath, why: `old image doc (now ${newPath})` });
        count('images');
      } else if (!sameJson(i, nextImg)) {
        plan.ops.push({ op: 'set', path: newPath, data: nextImg, why: `image ${slug}:${version} normalized` });
        count('images');
      }
    }
  }
  for (const d of (await db.collection('serviceSlugs').get()).docs) {
    deletes.push({ op: 'delete', path: `serviceSlugs/${d.id}`, why: 'obsolete collection (doc id = slug)' });
    count('serviceSlugs');
  }

  // ───────── config rewriting
  const svcMap = { ...prevServices, ...plan.services };
  const imgMap = { ...prevImages, ...plan.images };
  const slugOf = (id: string) => svcMap[id] ?? id;
  const versionOf = (id: string, tag: string) => {
    const m = imgMap[`${id}:${tag}`];
    if (m) return m.slice(m.indexOf(':') + 1);
    return RE.version.test(tag) ? tag : (toVersion(tag) ?? tag);
  };
  /** returns a rewritten copy of a config-like ({ sites }) or null when nothing changed */
  const fixSites = (sites: unknown): unknown[] | null => {
    if (!Array.isArray(sites)) return null;
    let changed = false;
    const out = sites.map((site) => {
      if (!isObj(site) || !isObj(site.config)) return site;
      const cfg = { ...site.config };
      if (Array.isArray(cfg.containers))
        cfg.containers = (cfg.containers as Doc[]).map((c) => {
          if (!isObj(c)) return c;
          let n: Doc = c;
          if ('serviceId' in c || 'tag' in c) {
            const { serviceId, tag, ...rest } = c;
            const id = String(serviceId ?? rest.serviceSlug ?? '');
            n = { ...rest, serviceSlug: slugOf(id), version: str(rest.version) ?? versionOf(id, String(tag ?? '')) };
            changed = true;
          }
          if (Array.isArray(n.depends) && (n.depends as Doc[]).some((d) => isObj(d) && 'serviceId' in d)) {
            n = { ...n, depends: (n.depends as Doc[]).map((d) => (isObj(d) && 'serviceId' in d ? { serviceSlug: slugOf(String(d.serviceId)) } : d)) };
            changed = true;
          }
          return n;
        });
      if (cfg.edge !== undefined) {
        const walk = (v: unknown): unknown => {
          if (Array.isArray(v)) return v.map(walk);
          if (!isObj(v)) return v;
          const o: Doc = {};
          for (const [k, x] of Object.entries(v)) {
            if (k === 'serviceId' && typeof x === 'string') {
              o.serviceSlug = slugOf(x);
              changed = true;
            } else o[k] = walk(x);
          }
          return o;
        };
        cfg.edge = walk(cfg.edge);
      }
      return { ...site, config: cfg };
    });
    return changed ? out : null;
  };
  const fixCatalog = (cat: Doc): Doc | null => {
    let changed = false;
    const services: Doc = {};
    for (const [k, v] of Object.entries((cat.services as Doc) ?? {})) {
      const e = (v ?? {}) as Doc;
      const slug = str(e.serviceSlug) ?? str(e.slug) ?? slugOf(k);
      const icon = typeof e.icon === 'string' && e.icon.startsWith('data:') ? e.icon : normIcon(e.icon);
      const ne = { serviceSlug: slug, label: e.label ?? slug, icon };
      if (k !== slug || !sameJson(e, ne)) changed = true;
      services[slug] = ne;
    }
    const images: Doc = {};
    for (const [k, v] of Object.entries((cat.images as Doc) ?? {})) {
      const e = (v ?? {}) as Doc;
      if ('serviceId' in e || 'tag' in e) {
        const { serviceId, tag, ...rest } = e;
        const id = String(serviceId ?? k.split(':')[0]);
        const slug = slugOf(id);
        const version = versionOf(id, String(tag ?? k.split(':').slice(1).join(':')));
        images[`${slug}:${version}`] = { ...rest, serviceSlug: slug, version };
        changed = true;
      } else images[k] = e;
    }
    return changed ? { ...cat, services, images } : null;
  };

  for (const d of (await db.collection('templates').get()).docs) {
    const t = d.data();
    const upd: Doc = {};
    const icon = normIcon(t.icon);
    if (icon !== (t.icon ?? null)) upd.icon = icon;
    if (typeof t._body === 'string') {
      const body = JSON.parse(t._body);
      const sites = fixSites(body.sites);
      if (sites) upd._body = JSON.stringify({ ...body, sites });
    }
    if (Object.keys(upd).length) {
      plan.ops.push({ op: 'update', path: `templates/${d.id}`, data: upd, why: `template ${Object.keys(upd).join(', ')}` });
      count('templates');
    }
  }
  const deps = await db.collection('deployments').get();
  for (const d of deps.docs) {
    const x = d.data();
    const upd: Doc = {};
    const image = normPicture(x.image);
    if (image !== (x.image ?? null)) upd.image = image;
    if (typeof x._draft === 'string') {
      const draft = JSON.parse(x._draft);
      const sites = fixSites(draft.sites);
      if (sites) upd._draft = JSON.stringify({ ...draft, sites });
    }
    if (typeof x._srcSnapshot === 'string') {
      const snap = JSON.parse(x._srcSnapshot);
      const sites = snap ? fixSites(snap.sites) : null;
      const icon = snap ? normIcon(snap.icon) : null;
      if (sites || (snap && icon !== (snap.icon ?? null))) upd._srcSnapshot = JSON.stringify({ ...snap, ...(sites ? { sites } : {}), icon });
    }
    if (Object.keys(upd).length) {
      plan.ops.push({ op: 'update', path: `deployments/${d.id}`, data: upd, why: `deployment ${Object.keys(upd).join(', ')}` });
      count('deployments');
    }
    for (const r of (await d.ref.collection('revisions').get()).docs) {
      const rv = r.data();
      if (typeof rv._config !== 'string' || typeof rv._catalog !== 'string') continue;
      const config = JSON.parse(rv._config);
      const catalog = JSON.parse(rv._catalog);
      const sites = fixSites(config.sites);
      const cat = fixCatalog(catalog);
      if (!sites && !cat) continue;
      const nc = sites ? { ...config, sites } : config;
      const ncat = cat ?? catalog;
      plan.ops.push({ op: 'update', path: `deployments/${d.id}/revisions/${r.id}`, data: { _config: JSON.stringify(nc), _catalog: JSON.stringify(ncat), hash: jcsHash(nc), catalogHash: jcsHash(ncat) }, why: 'revision config / catalog rekeyed (hash + catalogHash recomputed)' });
      count('revisions');
    }
  }
  for (const d of (await db.collection('tags').get()).docs) {
    const icon = normIcon(d.data().icon);
    if (icon !== (d.data().icon ?? null)) {
      plan.ops.push({ op: 'update', path: `tags/${d.id}`, data: { icon }, why: 'tag icon format' });
      count('tags');
    }
  }
  for (const d of (await db.collection('accounts').get()).docs) {
    const img = normPicture(d.data().img);
    if (img !== (d.data().img ?? null)) {
      plan.ops.push({ op: 'update', path: `accounts/${d.id}`, data: { img }, why: 'account img format' });
      count('accounts');
    }
  }
  plan.ops.push(...deletes);
  return plan;
}

/** Apply a plan (batches of 400; sets/updates before deletes) + one system.migration audit entry. */
export async function applyPlan(db: Firestore, plan: Plan): Promise<void> {
  if (!plan.ops.length) return;
  for (let i = 0; i < plan.ops.length; i += 400) {
    const b = db.batch();
    for (const o of plan.ops.slice(i, i + 400)) {
      const ref = db.doc(o.path);
      if (o.op === 'set') b.set(ref, o.data);
      else if (o.op === 'update') b.update(ref, o.data);
      else b.delete(ref);
    }
    await b.commit();
  }
  const ref = db.collection('auditLog').doc();
  await ref.create({
    auditId: ref.id,
    ts: new Date().toISOString(),
    op: 'system:migrate-v2',
    actor: 'system',
    actorEmail: null,
    action: 'system.migration',
    outcome: 'ok',
    status: 200,
    errorCode: null,
    errorMessage: null,
    targetType: 'system',
    target: 'system/migration-v2',
    requestId: null,
    ip: null,
    userAgent: null,
    changesTotal: 0,
    _changes: '[]',
    _data: JSON.stringify({ migration: 'v2', dryRun: false, changes: plan.ops.length, counts: plan.counts, services: plan.services, images: plan.images, warnings: plan.warnings.slice(0, 50) }),
    _actionKeys: auditActionKeys('system.migration'),
  });
}

async function main() {
  const { arg, flag, init } = await import('./_admin.ts');
  const project = arg('project');
  const apply = flag('apply');
  if (!project || (!apply && !flag('dry-run'))) {
    console.error('usage: tsx scripts/migrate-v2.ts --project <id> (--dry-run | --apply)');
    process.exit(2);
  }
  const { db } = init(project);
  console.log(`migrate-v2 on ${project} — ${apply ? 'APPLY' : 'dry run'}`);
  const plan = await planMigrationV2(db);
  for (const o of plan.ops) console.log(`${o.op.padEnd(6)} ${o.path}  — ${o.why}${o.op !== 'delete' ? `\n         ${JSON.stringify(o.data).slice(0, 600)}` : ''}`);
  for (const w of plan.warnings) console.log(`WARN   ${w}`);
  console.log(`services: ${JSON.stringify(plan.services)}\nimages: ${JSON.stringify(plan.images)}\ncounts: ${JSON.stringify(plan.counts)}\n${plan.ops.length} change(s)`);
  if (apply) {
    await applyPlan(db, plan);
    console.log(plan.ops.length ? 'applied' : 'nothing to do');
  }
}
if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) main().catch((e) => {
  console.error(e);
  process.exit(1);
});
