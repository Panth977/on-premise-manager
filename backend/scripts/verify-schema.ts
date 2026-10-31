/**
 * verify-schema — reads EVERY document of the OPM Firestore (prod or emulator) and checks it against the phase-U model
 * (shared types + validate.ts + the simple-body rules of the API). Prints one line per violation; exit 1 if any.
 *
 *   npx tsx scripts/verify-schema.ts --data-dir <dir>
 *
 * Checked: registries (username/password pair), services (doc id = serviceSlug, no serviceId/slug/repository/
 * defaultRepository, icon), images (doc id = version, serviceSlug = parent, reference parses and equals source, registryId
 * exists and matches the host, env normalized + validateImageEnv), templates (icon, body: validateConfig mode template),
 * deployments (image, draft: validateConfig mode draft save-level errors, srcTemplate snapshot shape), revisions (no
 * serviceId/tag containers, catalog keyed <serviceSlug>:<version>, hash / catalogHash), tags / accounts (icon / img),
 * icons (fields + bytes), obsolete collections (serviceSlugs) empty.
 * Round C (migrate-c): every config (template body, draft, srcTemplate snapshot, revision config) has instance containers
 * (containerSlug + ports + volumes arrays, no `port` / `volume`, depends {containerSlug}), proxy actions with containerSlug
 * (no serviceSlug), files with render ('expressions' | 'none'; binary → none) and SiteConfig.host string | null (not "").
 * Round D (migrate-d): every site config has a `volumes` list, no mount uses the old source kind `data`, every `volume`
 * source names a declared site volume, file / secret mounts are readOnly true. Part 2: every file and every site volume has a
 * `location` that passes checkLocation (collisions per site are the validator's V_LOCATION).
 * Round E (migrate-e): every edge is in the round-E shape — maxBodyBytes present, every host has responseHeaders
 * {set, add, remove, always}, every route has match {kind, path, stopRegex} (no legacy pathPrefix) and responseHeaders
 * (null | object), every proxy action has connect / send / read timeouts, rewrite, buffering flags, maxBodyBytes and no
 * legacy timeoutSec; anything else normalizeEdge would still change is reported too. The validator's errors on a legacy
 * edge (V_ENUM match.kind, V_RANGE timeouts, …) are not printed twice. Every site volume has kind 'dir' | 'file' and seed
 * (null | object; the validator checks its content).
 * Volume kind fix (migrate-f, §21 #83): every image volume has kind 'dir' | 'file' (+ validateImageVolumes).
 * Dev tools (§21 #97): SiteConfig.devTools absent or a NON-empty list (never []); every preset equals
 * normalizeStoredDevToolPreset(preset) (known keys only); a static credential (§21 #103 literal) is a non-empty string; the rules are validateConfig's
 * V_DEVTOOL_* (printed like every other validator error).
 * Volume content (migrate-h, §21 #101): no legacy File seed (`files` / `file`) in templates, drafts, srcTemplate snapshot /
 * base (revisions keep them: immutable history); every tree seed is canonical (normalizeVolumeTree leaves it unchanged);
 * `persist` is absent (off) or exactly { exclude: string[], maxBytes, auto } on a folder volume (never null); no File
 * mounted inside a writable volume / host mount (the validator's V_FILE_IN_WRITABLE_VOLUME, printed with the other errors).
 */
import { pathToFileURL } from 'node:url';
import type { DocStore as Firestore, DocRef, CollectionRef } from '../src/driver/index.js';
import { isLegacyFileSeed, normalizeVolumeTree, normalizeEdge, checkIcon, checkLocation, checkPicture, Issues, normalizeImageEnv, parseImageReference, registryHost, RE, ICON_CONTENT_TYPES, ICON_MAX_BYTES, validateConfig, validateImageEnv, validateImageVolumes, canonicalize, imageKey, normalizeStoredDevToolPreset, type Config, type ImageEnv, type ValidationContext } from '@opm/shared';
import { jcsHash } from '../src/core/util.ts';
import { COMMIT_KINDS, parseBranchKey, configHash as cfgHash, uncommittedItems } from '@opm/shared';

type Doc = Record<string, unknown>;
const isObj = (v: unknown): v is Doc => !!v && typeof v === 'object' && !Array.isArray(v);

export interface VerifyOptions {
  /** §21 #104: every template / deployment must have `branch` (after migrate-i). Default false (tests of older migrations). */
  requireBranches?: boolean;
  /**
   * §21 #105 (migrate-j): no `branch.uncommitted`, and the template config fields / deployment draft equal the head
   * commit config (they are a copy of the head, written only by commits). Default false (tests of older migrations).
   */
  requireNoWorkingCopy?: boolean;
}
export async function verifySchema(db: Firestore, opts: VerifyOptions = {}): Promise<string[]> {
  const out: string[] = [];
  const bad = (path: string, msg: string) => out.push(`${path}: ${msg}`);
  const iconIs = (path: string, v: unknown, picture = false) => {
    const is = new Issues('draft');
    (picture ? checkPicture : checkIcon)(v, 'icon', is);
    for (const e of is.errors) bad(path, e.message);
  };
  const iconIds = new Set((await db.collection('icons').select().get()).docs.map((d) => d.id));
  const iconRef = (path: string, v: unknown) => {
    if (typeof v === 'string' && v.startsWith('upload:') && !iconIds.has(v.slice(7))) bad(path, `icon ${v} does not exist`);
  };

  // registries
  const regs = new Map<string, Doc>();
  for (const d of (await db.collection('registries').get()).docs) {
    const r = d.data();
    regs.set(d.id, r);
    const p = `registries/${d.id}`;
    if (r.registryId !== d.id) bad(p, 'registryId ≠ doc id');
    if (typeof r.url !== 'string' || !/^https:\/\//.test(r.url)) bad(p, 'url must be https');
    if (typeof r.username !== 'string' || typeof r.password !== 'string') bad(p, 'username / password must be strings');
    else if (!r.username !== !r.password) bad(p, 'username and password must both be set or both be empty');
  }

  // services + images
  const services: ValidationContext['services'] = {};
  const images: ValidationContext['images'] = {};
  for (const d of (await db.collection('services').get()).docs) {
    const s = d.data();
    const p = `services/${d.id}`;
    services[d.id] = { label: s.label as string };
    if (!RE.slug.test(d.id)) bad(p, 'doc id is not a service slug');
    if (s.serviceSlug !== d.id) bad(p, 'serviceSlug ≠ doc id');
    for (const k of ['serviceId', 'slug', 'repository', 'defaultRepository']) if (k in s) bad(p, `obsolete field ${k}`);
    if (typeof s.label !== 'string' || !s.label) bad(p, 'label missing');
    iconIs(p, s.icon ?? null);
    iconRef(p, s.icon);
    for (const id of (await d.ref.collection('images').get()).docs) {
      const i = id.data();
      const ip = `${p}/images/${id.id}`;
      if (!RE.version.test(id.id)) bad(ip, 'doc id is not a version');
      if (i.version !== id.id) bad(ip, 'version ≠ doc id');
      if (i.serviceSlug !== d.id) bad(ip, 'serviceSlug ≠ parent');
      for (const k of ['serviceId', 'tag', 'repository']) if (k in i) bad(ip, `obsolete field ${k}`);
      const pr = parseImageReference(i.reference);
      if (!pr.ok) bad(ip, `reference: ${pr.error}`);
      else if (canonicalize(pr.parts) !== canonicalize(i.source ?? null)) bad(ip, 'source ≠ parseImageReference(reference)');
      if (i.registryId !== null && typeof i.registryId !== 'string') bad(ip, 'registryId must be an id or null');
      if (typeof i.registryId === 'string') {
        const r = regs.get(i.registryId);
        if (!r) bad(ip, `registry ${i.registryId} does not exist`);
        else if (pr.ok && registryHost(r.url as string) !== pr.parts.host) bad(ip, `registry host ${registryHost(r.url as string)} ≠ reference host ${pr.parts.host}`);
      }
      if (!isObj(i.import) || !['pending', 'copying', 'ready', 'failed'].includes(i.import.status as string)) bad(ip, 'import.status invalid');
      const env = Array.isArray(i.env) ? (i.env as ImageEnv[]) : [];
      if (!Array.isArray(i.env)) bad(ip, 'env must be a list');
      env.forEach((e) => {
        if (!('options' in e)) bad(ip, `env ${e.name}: options missing (normalize)`);
        if (Array.isArray((e as unknown as Doc).value)) bad(ip, `env ${e.name}: legacy list value`);
        if (canonicalize(normalizeImageEnv(e)) !== canonicalize(e)) bad(ip, `env ${e.name}: not normalized`);
      });
      const is = new Issues('publish');
      validateImageEnv(env, is);
      // §21 #83 (migrate-f): every image volume has an explicit kind dir | file
      if (!Array.isArray(i.volumes)) bad(ip, 'volumes must be a list');
      else {
        for (const v of i.volumes as Doc[]) if (!isObj(v) || (v.kind !== 'dir' && v.kind !== 'file')) bad(ip, `volume ${isObj(v) ? String(v.name) : '?'}: kind missing / not dir | file (run migrate-f)`);
        validateImageVolumes(i.volumes, is);
      }
      for (const e of is.errors) if (!(e.path.endsWith('.kind') && e.code === 'V_ENUM')) bad(ip, `${e.path}: ${e.message}`);
      images[imageKey(d.id, id.id)] = { kind: i.kind as 'service', ports: (i.ports as never) ?? [], env, volumes: (i.volumes as never) ?? [], healthcheck: (i.healthcheck as never) ?? null, importStatus: (i.import as Doc)?.status as never };
    }
  }
  for (const d of (await db.collection('serviceSlugs').select().get()).docs) bad(`serviceSlugs/${d.id}`, 'obsolete collection');
  const pages = (await db.collection('notFoundPages').select().get()).docs.map((d) => d.id);
  const ctx = (mode: ValidationContext['mode'], deploymentId: string): ValidationContext => ({ mode, deploymentId, services, images, notFoundPageIds: pages });
  const noOldKeys = (path: string, v: unknown) => {
    const s = JSON.stringify(v ?? null);
    if (/"serviceId"\s*:/.test(s)) bad(path, 'contains serviceId (old shape)');
  };
  /** round D part 2: a storage location must be present ({kind:auto} | {kind:path, path}) and pass checkLocation */
  const locationIs = (path: string, what: string, l: unknown, kind: 'files' | 'volumes') => {
    if (l === undefined || l === null) return bad(path, `${what}: location missing (run migrate-d)`);
    const is = new Issues('draft');
    checkLocation(l, kind, 'location', is);
    for (const e of is.errors) bad(path, `${what}: ${e.message}`);
  };
  /** round E: legacy edge problems of one site config (empty = round-E shape) */
  const shapeE = (sp: string, edge: unknown): string[] => {
    if (edge === undefined) return []; // a config without edge gets the default edge on read
    if (!isObj(edge)) return [`${sp}: edge must be an object`];
    const out: string[] = [];
    if (!('maxBodyBytes' in edge)) out.push(`${sp}: edge maxBodyBytes missing`);
    for (const h of Array.isArray(edge.hosts) ? (edge.hosts as Doc[]) : []) {
      const rh = isObj(h) ? h.responseHeaders : undefined;
      if (!isObj(rh) || !isObj(rh.set) || !isObj(rh.add) || !Array.isArray(rh.remove) || !Array.isArray(rh.always)) out.push(`${sp} host ${isObj(h) ? String(h.hostname) : '?'}: responseHeaders {set, add, remove, always} missing`);
    }
    for (const r of Array.isArray(edge.routes) ? (edge.routes as Doc[]) : []) {
      if (!isObj(r)) continue;
      const rp = `${sp} route ${String(r.routeId)}`;
      const m = isObj(r.match) ? r.match : {};
      if ('pathPrefix' in m) out.push(`${rp}: legacy match {pathPrefix} (must be {kind, path, stopRegex})`);
      else if (typeof m.kind !== 'string' || typeof m.path !== 'string' || typeof m.stopRegex !== 'boolean') out.push(`${rp}: match must have kind, path, stopRegex`);
      if (!('responseHeaders' in r)) out.push(`${rp}: responseHeaders missing (null = inherit host)`);
      const a = isObj(r.action) ? r.action : null;
      if (a?.type === 'proxy') {
        if ('timeoutSec' in a) out.push(`${rp}: legacy proxy timeoutSec (must be readTimeoutSec)`);
        const miss = ['connectTimeoutSec', 'sendTimeoutSec', 'readTimeoutSec', 'rewrite', 'requestBuffering', 'responseBuffering', 'maxBodyBytes', 'websocket'].filter((k) => !(k in a) && !('timeoutSec' in a && k.endsWith('TimeoutSec')));
        if (miss.length) out.push(`${rp}: proxy fields missing: ${miss.join(', ')}`);
      }
    }
    if (!out.length) {
      try {
        if (canonicalize(normalizeEdge(edge as never)) !== canonicalize(edge)) out.push(`${sp}: edge not normalized (normalizeEdge changes it)`);
      } catch (e) {
        out.push(`${sp}: edge cannot be normalized (${(e as Error).message})`);
      }
    }
    return out;
  };
  /** site slugs whose edge is legacy, per document path (the validator's duplicates of these are skipped) */
  const legacyEdgeSites = new Map<string, Set<string>>();
  /** round-C shape (independent of validateConfig, so a legacy document is reported precisely) */
  const shapeC = (path: string, cfg: unknown) => {
    if (!isObj(cfg)) return;
    for (const f of Array.isArray(cfg.files) ? (cfg.files as Doc[]) : []) {
      if (!isObj(f)) continue;
      if (f.render !== 'expressions' && f.render !== 'none') bad(path, `file ${f.fileSlug}: render missing / invalid (${JSON.stringify(f.render ?? null)})`);
      else if (f.editor === 'binary' && f.render !== 'none') bad(path, `file ${f.fileSlug}: binary file with render ${f.render}`);
      locationIs(path, `file ${f.fileSlug}`, f.location, 'files');
    }
    for (const site of Array.isArray(cfg.sites) ? (cfg.sites as Doc[]) : []) {
      if (!isObj(site) || !isObj(site.config)) continue;
      const sc = site.config;
      const sp = `site ${site.siteSlug}`;
      if (sc.host !== null && typeof sc.host !== 'string') bad(path, `${sp}: host must be a string or null`);
      else if (typeof sc.host === 'string' && sc.host.trim() === '') bad(path, `${sp}: host "" (must be null = auto)`);
      // round D: declared site volumes; mounts reference them; file / secret mounts read-only
      const declared = new Set<string>();
      if (!Array.isArray(sc.volumes)) bad(path, `${sp}: volumes list missing (pre-round-D site config; run migrate-d)`);
      else
        for (const v of sc.volumes as Doc[])
          if (isObj(v) && typeof v.volumeSlug === 'string') {
            declared.add(v.volumeSlug);
            locationIs(path, `${sp} volume ${v.volumeSlug}`, v.location, 'volumes');
            // round E: kind 'dir' | 'file' + seed (null | object) present
            if (v.kind !== 'dir' && v.kind !== 'file') bad(path, `${sp} volume ${v.volumeSlug}: kind missing / invalid (${JSON.stringify(v.kind ?? null)}; run migrate-e)`);
            if (!('seed' in v) || (v.seed !== null && !isObj(v.seed))) bad(path, `${sp} volume ${v.volumeSlug}: seed missing / invalid (null = starts empty; run migrate-e)`);
          }
      for (const c of Array.isArray(sc.containers) ? (sc.containers as Doc[]) : []) {
        if (!isObj(c)) continue;
        const cp = `${sp} container ${String(c.containerSlug ?? c.serviceSlug)}`;
        (Array.isArray(c.volumes) ? (c.volumes as Doc[]) : []).forEach((v, i) => {
          const src = isObj(v) && isObj(v.source) ? v.source : null;
          if (!src) return;
          const mp = `${cp} ${v.imageVolume ? `image volume ${v.imageVolume}` : `extra mount #${i + 1}`}`;
          if (src.kind === 'data') bad(path, `${mp}: legacy source kind data (${String(src.name)}) — must be a declared volume`);
          else if (src.kind === 'volume' && Array.isArray(sc.volumes) && !declared.has(String(src.volumeSlug))) bad(path, `${mp}: volume ${String(src.volumeSlug)} is not declared on the site`);
          if ((src.kind === 'file' || src.kind === 'secret') && v.readOnly !== true) bad(path, `${mp}: ${src.kind} mount must be readOnly true`);
        });
        if (typeof c.containerSlug !== 'string' || !RE.slug.test(c.containerSlug)) bad(path, `${cp}: containerSlug missing / invalid (legacy one-per-service shape)`);
        if ('port' in c) bad(path, `${cp}: legacy field port`);
        if ('volume' in c) bad(path, `${cp}: legacy field volume`);
        if (!Array.isArray(c.ports)) bad(path, `${cp}: ports must be a list`);
        if (!Array.isArray(c.volumes)) bad(path, `${cp}: volumes must be a list`);
        else for (const v of c.volumes as Doc[]) if (!isObj(v) || !isObj(v.source) || !('imageVolume' in v)) bad(path, `${cp}: volume row without imageVolume / source`);
        for (const d of Array.isArray(c.depends) ? (c.depends as Doc[]) : []) if (!isObj(d) || typeof d.containerSlug !== 'string' || 'serviceSlug' in d) bad(path, `${cp}: depends entry must be {containerSlug}`);
      }
      // §21 #97 a: devTools is OMITTED when empty (agents < 0.11 re-hash without it); presets in the exact stored shape
      if ('devTools' in sc) {
        if (!Array.isArray(sc.devTools)) bad(path, `${sp}: devTools must be a list of presets`);
        else if (sc.devTools.length === 0) bad(path, `${sp}: devTools is [] (an empty list must be omitted — it changes the revision hash for agents < 0.11)`);
        else
          for (const pr of sc.devTools as unknown[]) {
            const pp = `${sp} dev tool ${isObj(pr) ? String(pr.presetSlug) : '?'}`;
            if (!isObj(pr)) bad(path, `${pp}: must be an object`);
            else if (canonicalize(normalizeStoredDevToolPreset(pr)) !== canonicalize(pr)) bad(path, `${pp}: not in the stored shape (normalizeStoredDevToolPreset changes it: missing / unknown keys)`);
            // §21 #103: a static value (literal) is allowed — but it must be a non-empty string (never MASK / null)
            else if (isObj(pr.auth) && [pr.auth.password, pr.auth.token].some((c) => isObj(c) && c.kind === 'literal' && (typeof c.value !== 'string' || c.value === ''))) bad(path, `${pp}: static credential value must be a non-empty string`);
          }
      }
      const ev = shapeE(sp, sc.edge);
      for (const m of ev) bad(path, `${m} (run migrate-e)`);
      if (ev.length) {
        if (!legacyEdgeSites.has(path)) legacyEdgeSites.set(path, new Set());
        legacyEdgeSites.get(path)!.add(String(site.siteSlug));
      }
      const routes = isObj(sc.edge) && Array.isArray(sc.edge.routes) ? (sc.edge.routes as Doc[]) : [];
      for (const r of routes) {
        const a = isObj(r) && isObj(r.action) ? r.action : null;
        if (a?.type === 'proxy' && (typeof a.containerSlug !== 'string' || 'serviceSlug' in a)) bad(path, `${sp} route ${r.routeId}: proxy action must name containerSlug (not serviceSlug)`);
      }
    }
  };
  /** §21 #101 (migrate-h): volume content shape of a template / draft / srcTemplate config (not revisions) */
  const shapeH = (path: string, cfg: unknown) => {
    if (!isObj(cfg)) return;
    for (const site of Array.isArray(cfg.sites) ? (cfg.sites as Doc[]) : []) {
      if (!isObj(site) || !isObj(site.config)) continue;
      for (const v of Array.isArray(site.config.volumes) ? (site.config.volumes as Doc[]) : []) {
        if (!isObj(v)) continue;
        const vp = `site ${site.siteSlug} volume ${String(v.volumeSlug)}`;
        const sd = v.seed as Doc | null | undefined;
        if (isLegacyFileSeed(sd as never)) bad(path, `${vp}: legacy starter content from Files (seed ${String(sd!.kind)}) — run migrate-h`);
        if (isObj(sd) && sd.kind === 'tree') {
          if (!Array.isArray(sd.entries) || !Array.isArray(sd.dirs)) bad(path, `${vp}: tree seed needs entries[] and dirs[]`);
          else if (canonicalize({ entries: sd.entries, dirs: sd.dirs }) !== canonicalize(normalizeVolumeTree(sd as never))) bad(path, `${vp}: tree seed is not canonical (normalizeVolumeTree changes it)`);
          for (const k of Object.keys(sd)) if (!['kind', 'entries', 'dirs'].includes(k)) bad(path, `${vp}: tree seed has an unknown field ${k}`);
        }
        if ('persist' in v) {
          const p = v.persist;
          if (!isObj(p)) bad(path, `${vp}: persist must be absent (off) or { exclude, maxBytes, auto } (got ${JSON.stringify(p)})`);
          else {
            if (!Array.isArray(p.exclude) || p.exclude.some((x) => typeof x !== 'string') || typeof p.maxBytes !== 'number' || typeof p.auto !== 'boolean') bad(path, `${vp}: persist must be { exclude: string[], maxBytes: number, auto: boolean }`);
            const extra = Object.keys(p).filter((k) => !['exclude', 'maxBytes', 'auto'].includes(k));
            if (extra.length) bad(path, `${vp}: persist has unknown field(s) ${extra.join(', ')}`);
            if (v.kind === 'file') bad(path, `${vp}: persist on a single-file volume`);
          }
        }
      }
    }
  };
  const configErrors = (path: string, cfg: Config, mode: ValidationContext['mode'], dep: string) => {
    noOldKeys(path, cfg);
    shapeC(path, cfg);
    shapeH(path, cfg);
    let is: Issues;
    try {
      is = validateConfig(cfg, ctx(mode, dep));
    } catch (e) {
      bad(path, `validateConfig threw: ${(e as Error).message}`);
      return;
    }
    // round-D shape problems are reported once by shapeC (precise message), not again by the validator
    const dupD = (e: { path: string; code: string; message: string }) =>
      e.code === 'V_VOLUME_UNDECLARED' || e.code === 'V_READONLY' || (e.code === 'V_REQUIRED' && e.path.endsWith('.volumes')) || (e.code === 'V_ENUM' && e.message.includes('"data"')) || (e.code === 'V_LOCATION' && !e.message.includes('same place or nested')) || e.code === 'V_VOLUME_KIND';
    // round E: a legacy edge is reported once by shapeE, not again field by field by the validator
    const legacyE = [...(legacyEdgeSites.get(path) ?? [])].map((s) => `sites[${s}].config.edge`);
    const dupE = (e: { path: string }) => legacyE.some((b) => e.path === b || e.path.startsWith(`${b}.`));
    // §21 #101: a legacy File seed is reported once by shapeH (run migrate-h)
    const dupH = (e: { code: string }) => e.code === 'V_VOLUME_SEED_FILE_REF';
    for (const e of is.errors) if (!dupD(e) && !dupE(e) && !dupH(e)) bad(path, `${e.path}: ${e.code} ${e.message}`);
  };

  /**
   * §21 #104 branches (migrate-i): `branch` { head, origin, commitCount } on templates + deployments (#105: no `uncommitted`;
   * the config copy = the head commit config, with requireNoWorkingCopy); the
   * commits form ONE linear chain (seq 1..n, parentId = the previous, hash = sha256(JCS(config)), message, kind, catalog
   * on deployment commits); head = the newest; merge bases parse; agent-branch mirrors are chains too (legacy / migrated
   * first commits may name a parent that is not mirrored).
   */
  const branchIs = async (p: string, x: Doc, ref: DocRef, kind: 'template' | 'deployment') => {
    const b = x.branch as Doc | undefined;
    if (!b) {
      if (opts.requireBranches) bad(p, 'branch missing (run migrate-i)');
      return;
    }
    if (typeof b.commitCount !== 'number') bad(p, 'branch.commitCount must be a number');
    if (opts.requireNoWorkingCopy && 'uncommitted' in b) bad(p, 'branch.uncommitted is still stored (run migrate-j: there is no working copy since §21 #105)');
    else if (!opts.requireNoWorkingCopy && 'uncommitted' in b && typeof b.uncommitted !== 'number') bad(p, 'branch.uncommitted must be a number');
    const chain = await commitChain(`${p}/commits`, ref.collection('commits'), kind === 'deployment', false);
    const headId = (b.head as Doc | null)?.commitId ?? null;
    if (opts.requireNoWorkingCopy) {
      const raw = kind === 'template' ? x._body : x._draft;
      const copy = typeof raw === 'string' ? (JSON.parse(raw) as Config) : null;
      if (copy) {
        const empty = !copy.variables?.length && !copy.secrets?.length && !copy.files?.length && !copy.sites?.length;
        if (!headId) {
          if (!empty) bad(p, 'no head commit but a non-empty config copy (uncommitted changes — run migrate-j)');
        } else {
          const hs = await ref.collection('commits').doc(String(headId)).get();
          const hd = hs.data() as Doc | undefined;
          if (hd && typeof hd._config === 'string' && cfgHash(copy) !== hd.hash) {
            const n = uncommittedItems(JSON.parse(hd._config) as Config, copy, { secretValues: kind === 'deployment' }).length;
            if (n) bad(p, `the config copy differs from the head commit seq ${String(hd.seq)} (${n} item(s) — uncommitted changes, run migrate-j)`);
          }
        }
      }
    }
    if ((chain.at(-1)?.commitId ?? null) !== headId) bad(p, `branch.head ${headId} is not the newest commit ${chain.at(-1)?.commitId ?? null}`);
    if (b.commitCount !== chain.length) bad(p, `branch.commitCount ${b.commitCount} ≠ ${chain.length} commits`);
    if (b.origin !== null && b.origin !== undefined && (!isObj(b.origin) || !isObj((b.origin as Doc).branch))) bad(p, 'branch.origin must be null or { branch, commitId, seq, label }');
    for (const m of (await ref.collection('mergeBases').get()).docs) {
      const mb = m.data();
      const mp = `${p}/mergeBases/${m.id}`;
      if (typeof mb.sourceKey !== 'string' || !parseBranchKey(mb.sourceKey)) bad(mp, 'sourceKey is not a branch key');
      try {
        const base = JSON.parse(String(mb._base));
        if (!isObj(base) || !Array.isArray(base.sites)) bad(mp, '_base is not a config');
        else if ((base.secrets as Doc[] | undefined ?? []).some((x) => typeof x.value === 'string' && !String(x.value).startsWith('sha256:'))) bad(mp, '_base holds a plain secret value');
        const sk = JSON.parse(String(mb._skipped ?? '[]'));
        if (!Array.isArray(sk) || sk.some((r: Doc) => typeof r.key !== 'string' || typeof r.theirsHash !== 'string')) bad(mp, '_skipped must be [{ key, theirsHash, … }]');
      } catch {
        bad(mp, '_base / _skipped are not JSON');
      }
    }
  };
  const commitChain = async (cp: string, coll: CollectionRef, needCatalog: boolean, mirror: boolean) => {
    const docs = (await coll.orderBy('seq', 'asc').get()).docs.map((c) => ({ id: c.id, d: c.data() }));
    const out: { commitId: string; seq: number }[] = [];
    docs.forEach(({ id, d }, i) => {
      const p = `${cp}/${id}`;
      if (d.commitId !== id) bad(p, 'commitId ≠ doc id');
      if (typeof d.message !== 'string' || !d.message.trim()) bad(p, 'message missing');
      if (!(COMMIT_KINDS as readonly string[]).includes(d.kind as string)) bad(p, `kind ${String(d.kind)} unknown`);
      const prev = docs[i - 1];
      const first = i === 0;
      if (first && !mirror && (d.seq !== 1 || d.parentId !== null)) bad(p, 'the first commit must be seq 1 without parent');
      if (!first && (d.seq !== (prev!.d.seq as number) + 1 || d.parentId !== prev!.id)) {
        if (!(mirror && (d.kind === 'legacy' || d.kind === 'migrated'))) bad(p, `seq ${String(d.seq)} / parent ${String(d.parentId)} do not follow ${prev!.id} seq ${String(prev!.d.seq)}`);
      }
      if (typeof d._config === 'string') {
        const config = JSON.parse(d._config);
        if (cfgHash(config) !== d.hash) bad(p, 'hash ≠ sha256(JCS(config))');
        if (needCatalog && (typeof d._catalog !== 'string' || jcsHash(JSON.parse(d._catalog)) !== d.catalogHash)) bad(p, 'catalog / catalogHash missing or wrong');
      } else if (typeof d._configRef !== 'string') bad(p, '_config / _configRef missing');
      out.push({ commitId: id, seq: d.seq as number });
    });
    return out;
  };

  // §21 #106 entity slugs (migrate-k): present, RE.slug, unique per collection (deleted deployments included)
  for (const coll of ['templates', 'deployments'] as const) {
    const seen = new Map<string, string>();
    for (const d of (await db.collection(coll).select('slug').get()).docs) {
      const sl = d.data().slug;
      if (typeof sl !== 'string' || !/^[a-z][a-z0-9-]{1,39}$/.test(sl)) bad(`${coll}/${d.id}`, `slug missing or invalid (${JSON.stringify(sl ?? null)}) — run migrate-k`);
      else if (seen.has(sl)) bad(`${coll}/${d.id}`, `slug ${sl} is also used by ${coll}/${seen.get(sl)}`);
      else seen.set(sl, d.id);
    }
  }
  // §21 #106 GitHub mirror settings: owner / repo format, enabled ⇒ configured
  {
    const g = (await db.collection('githubMirror').doc('config').get()).data();
    if (g) {
      if (g.owner && !/^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$/.test(g.owner)) bad('githubMirror/config', 'owner has a bad format');
      if (g.repo && !/^[A-Za-z0-9._-]{1,100}$/.test(g.repo)) bad('githubMirror/config', 'repo has a bad format');
      if (g.enabled && !(g.owner && g.repo && g.token)) bad('githubMirror/config', 'enabled without owner / repo / token');
    }
  }
  for (const d of (await db.collection('templates').get()).docs) {
    const t = d.data();
    const p = `templates/${d.id}`;
    iconIs(p, t.icon ?? null);
    iconRef(p, t.icon);
    const body = typeof t._body === 'string' ? JSON.parse(t._body) : null;
    if (!body) bad(p, '_body missing');
    else configErrors(p, body as Config, 'template', 'template');
    await branchIs(p, t, d.ref, 'template');
  }
  for (const d of (await db.collection('deployments').get()).docs) {
    const x = d.data();
    const p = `deployments/${d.id}`;
    iconIs(p, x.image ?? null, true);
    iconRef(p, x.image);
    // round G (§21 #84): accessKey present (grouped plaintext or null = pre-#84, not stored)
    if (!('accessKey' in x)) bad(p, 'accessKey missing (run migrate-g)');
    else if (x.accessKey !== null && (typeof x.accessKey !== 'string' || !/^[A-Z2-7]{4}(-[A-Z2-7]{4}){5}$/.test(x.accessKey))) bad(p, 'accessKey must be "XXXX-…" (24 base32 chars, grouped) or null');
    const enrolledAt = new Map((await d.ref.collection('agents').get()).docs.map((a) => [a.id, a.data().enrolledAt as string]));
    for (const t of (await d.ref.collection('installTokens').get()).docs) {
      const tk = t.data();
      const tp = `${p}/installTokens/${t.id}`;
      const current = enrolledAt.get(tk.siteSlug) ?? null;
      // §21 #92 kinds: install (absent = install) | maintenance (expiresAt null, boundEnrolledAt, never used)
      if (tk.kind !== undefined && tk.kind !== 'install' && tk.kind !== 'maintenance') bad(tp, 'kind must be install | maintenance');
      if (tk.kind === 'maintenance' && (tk.expiresAt !== null || typeof tk.boundEnrolledAt !== 'string' || tk.usedAt)) bad(tp, 'a maintenance token has expiresAt null, a boundEnrolledAt and no usedAt');
      if (!('token' in tk)) bad(tp, 'token missing (run migrate-g)');
      else if (tk.token !== null && (typeof tk.token !== 'string' || !tk.token.startsWith('opi_'))) bad(tp, 'token must be "opi_…" or null');
      else if (tk.token !== null && tk.revokedAt) bad(tp, 'a revoked token still holds its plaintext');
      else if (tk.token !== null && tk.usedAt && tk.usedAt !== current) bad(tp, 'a used token of an ended enrollment still holds its plaintext');
      else if (tk.token !== null && tk.kind === 'maintenance' && tk.boundEnrolledAt !== current) bad(tp, 'a maintenance token of an ended enrollment still holds its plaintext');
    }
    if (typeof x._draft === 'string' && !x.deleted) configErrors(`${p} draft`, JSON.parse(x._draft), 'draft', d.id);
    if (typeof x._srcSnapshot === 'string') {
      noOldKeys(`${p} srcTemplate`, JSON.parse(x._srcSnapshot));
      shapeC(`${p} srcTemplate`, JSON.parse(x._srcSnapshot));
      shapeH(`${p} srcTemplate`, JSON.parse(x._srcSnapshot));
    }
    // §21 #94 Template sync: the merge base (template config at baseVersion) + skip records; absent = pre-#94 (base = snapshot)
    if (x.srcTemplate && typeof x._srcBase === 'string') {
      noOldKeys(`${p} srcTemplate base`, JSON.parse(x._srcBase));
      shapeC(`${p} srcTemplate base`, JSON.parse(x._srcBase));
      shapeH(`${p} srcTemplate base`, JSON.parse(x._srcBase));
      if (typeof x.srcTemplate.baseVersion !== 'number') bad(p, 'srcTemplate.baseVersion missing next to _srcBase');
    }
    if (x.srcTemplate && x._srcSkipped !== undefined && x._srcSkipped !== null) {
      const sk = JSON.parse(x._srcSkipped);
      if (!Array.isArray(sk) || sk.some((r: Record<string, unknown>) => typeof r.key !== 'string' || typeof r.theirsHash !== 'string')) bad(p, '_srcSkipped must be [{ key, theirsHash, … }]');
    }
    if (!x.srcTemplate && (x._srcBase || x._srcSkipped)) bad(p, 'unlinked deployment still holds _srcBase / _srcSkipped');
    if (typeof x._draft === 'string' && x.deleted) {
      shapeC(`${p} draft`, JSON.parse(x._draft));
      shapeH(`${p} draft`, JSON.parse(x._draft));
    }
    await branchIs(p, x, d.ref, 'deployment');
    for (const ab of (await d.ref.collection('agentBranches').get()).docs) {
      const a = ab.data();
      if (a.siteSlug !== ab.id) bad(`${p}/agentBranches/${ab.id}`, 'siteSlug ≠ doc id');
      if (typeof a.legacy !== 'boolean') bad(`${p}/agentBranches/${ab.id}`, 'legacy must be a boolean');
      await commitChain(`${p}/agentBranches/${ab.id}/commits`, ab.ref.collection('commits'), false, true);
    }
    for (const r of (await d.ref.collection('revisions').get()).docs) {
      const rv = r.data();
      const rp = `${p}/revisions/${r.id}`;
      if (typeof rv._config !== 'string' || typeof rv._catalog !== 'string') {
        bad(rp, '_config / _catalog missing');
        continue;
      }
      const config = JSON.parse(rv._config);
      const catalog = JSON.parse(rv._catalog);
      noOldKeys(rp, config);
      shapeC(rp, config);
      noOldKeys(`${rp} catalog`, catalog);
      if (rv.hash !== jcsHash(config)) bad(rp, 'hash ≠ sha256(JCS(config))');
      if (rv.catalogHash !== jcsHash(catalog)) bad(rp, 'catalogHash ≠ sha256(JCS(catalog))');
      for (const site of config.sites ?? [])
        for (const c of site.config?.containers ?? []) {
          if (typeof c.serviceSlug !== 'string' || typeof c.version !== 'string') bad(rp, `site ${site.siteSlug}: container without serviceSlug / version`);
          // several instances of the same image share one catalog entry
          else if (!catalog.images?.[imageKey(c.serviceSlug, c.version)]) bad(rp, `catalog has no image ${c.serviceSlug}:${c.version}`);
        }
      for (const [k, v] of Object.entries<Doc>(catalog.services ?? {})) if (v?.serviceSlug !== k) bad(rp, `catalog service ${k}: serviceSlug ≠ key`);
      for (const [k, v] of Object.entries<Doc>(catalog.images ?? {})) if (`${v?.serviceSlug}:${v?.version}` !== k) bad(rp, `catalog image ${k}: serviceSlug:version ≠ key`);
    }
  }
  for (const d of (await db.collection('tags').get()).docs) {
    iconIs(`tags/${d.id}`, d.data().icon ?? null);
    iconRef(`tags/${d.id}`, d.data().icon);
  }
  for (const d of (await db.collection('accounts').get()).docs) {
    iconIs(`accounts/${d.id}`, d.data().img ?? null, true);
    iconRef(`accounts/${d.id}`, d.data().img);
    // round G (§21 #84): stored credentials present (null allowed)
    const a = d.data();
    for (const k of ['lastSetPassword', 'lastResetLink'] as const) {
      if (!(k in a)) bad(`accounts/${d.id}`, `${k} missing (run migrate-g)`);
      else if (a[k] !== null && (typeof a[k] !== 'object' || typeof a[k].value !== 'string' || typeof a[k].setAt !== 'string' || typeof a[k].setBy !== 'string')) bad(`accounts/${d.id}`, `${k} must be { value, setAt, setBy, … } or null`);
    }
  }
  for (const d of (await db.collection('icons').get()).docs) {
    const i = d.data();
    const p = `icons/${d.id}`;
    if (i.iconId !== d.id) bad(p, 'iconId ≠ doc id');
    if (!(ICON_CONTENT_TYPES as readonly string[]).includes(i.contentType)) bad(p, 'contentType');
    const len = i._bytes ? Buffer.from(i._bytes).length : 0;
    if (!len || len > ICON_MAX_BYTES || len !== i.sizeBytes) bad(p, 'bytes missing / size mismatch');
  }
  return out;
}

async function main() {
  const { arg, init } = await import('./_admin.ts');
  const project = arg('project');
  if (!project) {
    console.error('usage: tsx scripts/verify-schema.ts --project <id>');
    process.exit(2);
  }
  const { db } = init(project);
  const v = await verifySchema(db, { requireBranches: !process.argv.includes('--allow-pre-branches'), requireNoWorkingCopy: !process.argv.includes('--allow-working-copy') && !process.argv.includes('--allow-pre-branches') });
  for (const l of v) console.log(l);
  console.log(`verify-schema ${project}: ${v.length} violation(s)`);
  process.exit(v.length ? 1 : 0);
}
if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) main().catch((e) => {
  console.error(e);
  process.exit(1);
});
