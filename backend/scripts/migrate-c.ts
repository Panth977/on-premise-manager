/**
 * migrate-c — whole-DB migration to the round-C data model (SPEC-final §21 #51–56):
 *   containers (one per service: no containerSlug, `port`, `volume`, depends {serviceSlug})
 *     → instances { containerSlug = serviceSlug, ports, volumes (image volumes made explicit: data `<serviceSlug>-<vol>`),
 *       entrypoint, healthcheck, depends {containerSlug} } via shared normalizeContainer + the image volumes from Firestore
 *   edge proxy actions serviceSlug → containerSlug;  SiteConfig.host "" → null (non-empty hosts / expressions are KEPT)
 *   files: render = 'none' for binary; text → 'expressions' unless the content contains "{{" and checkFileContent reports
 *     errors (then 'none'; content read from Storage files/<hex>; unreadable content → 'none' + warning)
 *   in: templates (_body), deployments (_draft, _srcSnapshot), revisions (_config; hash + catalogHash recomputed when the
 *     config changes; the catalog itself is unchanged: images stay keyed <serviceSlug>:<version>)
 *   Templates keep version / updatedAt (decision: a migration is not an edit; srcTemplate.templateVersion stays comparable).
 *   One `system.migration` audit entry per --apply run (data.migration = 'c': counts + per-document summary + warnings).
 * Idempotent: a second run plans 0 changes.
 *
 * Usage (from backend/):
 *   npx tsx scripts/migrate-c.ts --data-dir <dir> [--dry-run | --apply]
 * Default is the dry run; only --apply writes.
 */
import { pathToFileURL } from 'node:url';
import type { DocStore as Firestore } from '../src/driver/index.js';
import { auditActionKeys, canonicalize, checkFileContent, imageKey, normalizeFileDef, normalizeSiteConfig, type CatalogImage, type FileDef, type SiteConfig } from '@opm/shared';
import { jcsHash } from '../src/core/util.ts';

type Doc = Record<string, unknown>;
const isObj = (v: unknown): v is Doc => !!v && typeof v === 'object' && !Array.isArray(v);
const same = (a: unknown, b: unknown) => canonicalize(a ?? null) === canonicalize(b ?? null);

/** Minimal bucket surface (firebase-admin / @google-cloud/storage Bucket). */
export interface BucketLike {
  file(path: string): { download(): Promise<[Buffer]> };
}

export type Op = { op: 'update'; path: string; data: Record<string, unknown>; why: string };
export interface DocSummary {
  path: string;
  lines: string[];
}
export interface PlanC {
  ops: Op[];
  docs: DocSummary[];
  warnings: string[];
  counts: Record<string, number>;
}

// ───────────────────────────────────────── summaries (dry-run output)
function portStr(p: Doc): string {
  const ip = p.hostIp && p.hostIp !== '0.0.0.0' ? `${p.hostIp}:` : '';
  const edge = p.edge === false ? ' no-edge' : '';
  return `${p.portName}→${p.hostPort === null || p.hostPort === undefined ? 'unpublished' : `${ip}${p.hostPort}`}${edge}`;
}
function srcStr(s: Doc | undefined): string {
  if (!s) return '?';
  if (s.kind === 'data') return `data:${s.name}`;
  if (s.kind === 'volume') return `volume:${s.volumeSlug}`;
  if (s.kind === 'host') return `host:${s.path}`;
  if (s.kind === 'file') return `file:${s.fileSlug}`;
  if (s.kind === 'secret') return `secret:${s.secretSlug}`;
  return 'none';
}
export function containerSummary(c: Doc): string {
  const id = `${String(c.containerSlug ?? c.serviceSlug)} (${c.serviceSlug}:${c.version})`;
  if ('port' in c || 'volume' in c || c.containerSlug === undefined) {
    const ports = ((c.port as Doc[]) ?? []).map((p) => `${p.portName}→${p.hostPort ?? 'unpublished'}`);
    const vols = ((c.volume as Doc[]) ?? []).map((v) => `${v.kind}:${v.name ?? v.fileSlug ?? v.secretSlug}@${v.mountPath}`);
    const deps = ((c.depends as Doc[]) ?? []).map((d) => String(d.serviceSlug ?? d.containerSlug));
    return `LEGACY ${id} port[${ports.join(', ')}] volume[${vols.join(', ')}]${deps.length ? ` depends[${deps.join(', ')}]` : ''}`;
  }
  const ports = ((c.ports as Doc[]) ?? []).map(portStr);
  const vols = ((c.volumes as Doc[]) ?? []).map((v) => `${v.imageVolume ? `image:${v.imageVolume}` : 'extra'}=${srcStr(v.source as Doc)}${v.mountPath ? `@${v.mountPath}` : ''}${v.readOnly ? ' ro' : ''}`);
  const deps = ((c.depends as Doc[]) ?? []).map((d) => String(d.containerSlug));
  return `${id} ports[${ports.join(', ')}] volumes[${vols.join(', ')}]${deps.length ? ` depends[${deps.join(', ')}]` : ''}`;
}

// ───────────────────────────────────────── conversion
export interface ConvertEnv {
  images: Record<string, Pick<CatalogImage, 'volumes'>>;
  /** file render decision for a text file without `render` (cached per sha256) */
  textRender: (f: FileDef, where: string) => Promise<{ render: FileDef['render']; note: string }>;
  warn: (m: string) => void;
}
/**
 * Convert one config-like ({ files?, sites? }) to the round-C shape. Returns the new object (or null = unchanged) and
 * summary lines. Only `files` and `sites[].config` are touched.
 */
export async function convertConfig(cfg: Doc, where: string, env: ConvertEnv): Promise<{ next: Doc | null; lines: string[] }> {
  const lines: string[] = [];
  let changed = false;
  const out: Doc = { ...cfg };
  if (Array.isArray(cfg.files)) {
    const files: unknown[] = [];
    for (const f of cfg.files as Doc[]) {
      if (!isObj(f)) {
        files.push(f);
        continue;
      }
      let nf: Doc;
      if (f.render === undefined) {
        if (f.editor === 'binary') {
          nf = normalizeFileDef({ ...f, render: 'none' } as unknown as FileDef) as unknown as Doc;
          lines.push(`file ${f.fileSlug}: render none (binary)`);
        } else {
          const d = await env.textRender(f as unknown as FileDef, `${where} file ${f.fileSlug}`);
          nf = normalizeFileDef({ ...f, render: d.render } as unknown as FileDef) as unknown as Doc;
          lines.push(`file ${f.fileSlug}: render ${d.render}${d.note ? ` (${d.note})` : ''}`);
        }
      } else {
        nf = normalizeFileDef(f as unknown as FileDef) as unknown as Doc;
        if (nf.render !== f.render) lines.push(`file ${f.fileSlug}: render ${String(f.render)} → ${String(nf.render)} (binary files are never rendered)`);
        if (!same(nf.location, f.location)) lines.push(`file ${f.fileSlug}: location auto (round D part 2)`);
      }
      if (!same(nf, f)) changed = true;
      files.push(nf);
    }
    out.files = files;
  }
  if (Array.isArray(cfg.sites)) {
    out.sites = (cfg.sites as Doc[]).map((site) => {
      if (!isObj(site) || !isObj(site.config)) return site;
      const before = site.config as Doc;
      let after: SiteConfig;
      try {
        after = normalizeSiteConfig(before as Partial<SiteConfig>, env.images);
      } catch (e) {
        env.warn(`${where} site ${site.siteSlug}: cannot normalize (${(e as Error).message}) — left unchanged`);
        return site;
      }
      if (same(after, before)) return site;
      changed = true;
      const s = String(site.siteSlug);
      if ((before.host ?? null) !== after.host) lines.push(`site ${s}: host ${JSON.stringify(before.host ?? null)} → ${JSON.stringify(after.host)}`);
      else lines.push(`site ${s}: host ${JSON.stringify(after.host)} (kept)`);
      const bc = Array.isArray(before.containers) ? (before.containers as Doc[]) : [];
      bc.forEach((c, i) => {
        const a = after.containers[i] as unknown as Doc | undefined;
        if (!isObj(c) || !a) return;
        if (same(c, a)) return;
        const img = env.images[imageKey(String(c.serviceSlug), String(c.version))];
        if (!img && ('volume' in c || c.containerSlug === undefined)) env.warn(`${where} site ${s}: image ${c.serviceSlug}:${c.version} not in the catalog — implicit image volumes stay implicit (same effective mounts)`);
        lines.push(`site ${s}: container ${containerSummary(c)}`);
        lines.push(`site ${s}:        → ${containerSummary(a)}`);
      });
      const br = isObj(before.edge) && Array.isArray(before.edge.routes) ? (before.edge.routes as Doc[]) : [];
      br.forEach((r) => {
        const a = isObj(r) && isObj(r.action) ? r.action : null;
        if (a && a.type === 'proxy' && a.containerSlug === undefined && a.serviceSlug !== undefined) lines.push(`site ${s}: route ${r.routeId}: proxy serviceSlug ${a.serviceSlug} → containerSlug ${a.serviceSlug}`);
      });
      const otherKeys = ['agentPort', 'agentGuiAliasPort', 'depends', 'notFoundPageId', 'edge'].filter((k) => !same(before[k], (after as unknown as Doc)[k]));
      if (otherKeys.length) lines.push(`site ${s}: defaults filled / normalized: ${otherKeys.join(', ')}`);
      return { ...site, config: after };
    });
  }
  return { next: changed ? out : null, lines };
}

/** Image volumes of every catalog image + the file-render decision (content from Storage); shared with migrate-d. */
export async function buildConvertEnv(db: Firestore, bucket: BucketLike | null, warn: (m: string) => void): Promise<ConvertEnv> {
  // image volumes of every catalog image (post migrate-v2: services/{slug}/images/{version})
  const images: ConvertEnv['images'] = {};
  for (const d of (await db.collectionGroup('images').get()).docs) {
    const parent = d.ref.parent.parent;
    if (!parent || parent.parent.id !== 'services') continue;
    const vols = d.data().volumes;
    images[imageKey(parent.id, d.id)] = { volumes: Array.isArray(vols) ? vols : [] };
  }

  const renderCache = new Map<string, { render: FileDef['render']; note: string }>();
  const textRender: ConvertEnv['textRender'] = async (f, where) => {
    if (!f.content) return { render: 'expressions', note: 'no content' };
    const sha = String(f.content.sha256);
    const hit = renderCache.get(sha);
    if (hit) return hit;
    let res: { render: FileDef['render']; note: string };
    let text: string | null = null;
    try {
      if (!bucket) throw new Error('no bucket');
      const [buf] = await bucket.file(`files/${sha.replace(/^sha256:/, '')}`).download();
      text = buf.toString('utf8');
    } catch (e) {
      warn(`${where}: content ${sha} could not be read (${(e as Error).message}) — render none (mounted as before)`);
      res = { render: 'none', note: 'content unreadable' };
    }
    if (text !== null) {
      if (!text.includes('{{')) res = { render: 'expressions', note: 'no {{' };
      else {
        const issues = checkFileContent(text);
        if (issues.length) {
          const first = issues[0]!;
          res = { render: 'none', note: `contains {{ with ${issues.length} error(s): ${first.line}:${first.col} ${first.code} ${first.message}` };
          warn(`${where}: render none — ${res.note}`);
        } else res = { render: 'expressions', note: 'valid {{ }} blocks' };
      }
    }
    renderCache.set(sha, res!);
    return res!;
  };
  return { images, textRender, warn };
}

export async function planMigrationC(db: Firestore, bucket: BucketLike | null): Promise<PlanC> {
  const plan: PlanC = { ops: [], docs: [], warnings: [], counts: {} };
  const count = (k: string) => (plan.counts[k] = (plan.counts[k] ?? 0) + 1);
  const warn = (m: string) => plan.warnings.push(m);
  const env = await buildConvertEnv(db, bucket, warn);

  // templates
  for (const d of (await db.collection('templates').get()).docs) {
    const t = d.data();
    if (typeof t._body !== 'string') continue;
    const path = `templates/${d.id}`;
    const r = await convertConfig(JSON.parse(t._body), `${path} (${t.label})`, env);
    if (!r.next) continue;
    plan.ops.push({ op: 'update', path, data: { _body: JSON.stringify(r.next) }, why: 'template body → round-C shape (version / updatedAt unchanged)' });
    plan.docs.push({ path: `${path} "${t.label}" v${t.version}`, lines: r.lines });
    count('templates');
  }
  // deployments + revisions
  for (const d of (await db.collection('deployments').get()).docs) {
    const x = d.data();
    const path = `deployments/${d.id}`;
    const upd: Doc = {};
    const lines: string[] = [];
    if (typeof x._draft === 'string') {
      const r = await convertConfig(JSON.parse(x._draft), `${path} draft`, env);
      if (r.next) {
        upd._draft = JSON.stringify(r.next);
        lines.push(...r.lines.map((l) => `draft: ${l}`));
      }
    }
    if (typeof x._srcSnapshot === 'string') {
      const snap = JSON.parse(x._srcSnapshot);
      if (isObj(snap)) {
        const r = await convertConfig(snap, `${path} srcTemplate snapshot`, env);
        if (r.next) {
          upd._srcSnapshot = JSON.stringify(r.next);
          lines.push(...r.lines.map((l) => `srcTemplate snapshot: ${l}`));
        }
      }
    }
    if (Object.keys(upd).length) {
      plan.ops.push({ op: 'update', path, data: upd, why: `deployment ${Object.keys(upd).join(', ')} → round-C shape` });
      plan.docs.push({ path: `${path} "${x.label}"${x.deleted ? ' (deleted)' : ''}`, lines });
      count('deployments');
    }
    for (const rv of (await d.ref.collection('revisions').get()).docs) {
      const r0 = rv.data();
      if (typeof r0._config !== 'string' || typeof r0._catalog !== 'string') continue;
      const rpath = `${path}/revisions/${rv.id}`;
      const r = await convertConfig(JSON.parse(r0._config), `${rpath} (seq ${r0.seq})`, env);
      if (!r.next) continue;
      const catalog = JSON.parse(r0._catalog);
      plan.ops.push({ op: 'update', path: rpath, data: { _config: JSON.stringify(r.next), hash: jcsHash(r.next), catalogHash: jcsHash(catalog) }, why: 'revision config → round-C shape (hash + catalogHash recomputed)' });
      plan.docs.push({ path: `${rpath} seq ${r0.seq} hash ${String(r0.hash).slice(0, 19)}… → ${jcsHash(r.next).slice(0, 19)}…`, lines: r.lines });
      count('revisions');
    }
  }
  return plan;
}

/** Apply a plan (batches of 400) + one system.migration audit entry. */
export async function applyPlanC(db: Firestore, plan: PlanC): Promise<void> {
  if (!plan.ops.length) return;
  for (let i = 0; i < plan.ops.length; i += 400) {
    const b = db.batch();
    for (const o of plan.ops.slice(i, i + 400)) b.update(db.doc(o.path), o.data);
    await b.commit();
  }
  const ref = db.collection('auditLog').doc();
  // per-document summary, bounded so the entry stays far below the 1 MiB document limit
  let docs = plan.docs.map((d) => ({ path: d.path, lines: d.lines.slice(0, 60).map((l) => l.slice(0, 400)) }));
  while (JSON.stringify(docs).length > 600_000 && docs.length > 1) docs = docs.slice(0, Math.ceil(docs.length / 2));
  await ref.create({
    auditId: ref.id,
    ts: new Date().toISOString(),
    op: 'system:migrate-c',
    actor: 'system',
    actorEmail: null,
    action: 'system.migration',
    outcome: 'ok',
    status: 200,
    errorCode: null,
    errorMessage: null,
    targetType: 'system',
    target: 'system/migration-c',
    requestId: null,
    ip: null,
    userAgent: null,
    changesTotal: 0,
    _changes: '[]',
    _data: JSON.stringify({ migration: 'c', dryRun: false, changes: plan.ops.length, counts: plan.counts, docs, docsTotal: plan.docs.length, warnings: plan.warnings.slice(0, 100) }),
    _actionKeys: auditActionKeys('system.migration'),
  });
}

export function printPlan(plan: PlanC, log: (s: string) => void = console.log) {
  for (const d of plan.docs) {
    log(`\n● ${d.path}`);
    for (const l of d.lines) log(`    ${l}`);
  }
  if (plan.warnings.length) log('');
  for (const w of plan.warnings) log(`WARN ${w}`);
  log(`\ncounts: ${JSON.stringify(plan.counts)}\n${plan.ops.length} change(s)`);
}

async function main() {
  const { arg, flag, init } = await import('./_admin.ts');
  const project = arg('project');
  const apply = flag('apply');
  if (!project) {
    console.error('usage: tsx scripts/migrate-c.ts --project <id> [--dry-run (default) | --apply]');
    process.exit(2);
  }
  const { db, bucket } = init(project);
  console.log(`migrate-c on ${project} — ${apply ? 'APPLY' : 'dry run (nothing is written)'}`);
  const plan = await planMigrationC(db, bucket as unknown as BucketLike);
  printPlan(plan);
  if (apply) {
    await applyPlanC(db, plan);
    console.log(plan.ops.length ? 'applied (+ one system.migration audit entry)' : 'nothing to do');
  }
}
if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href)
  main().then(
    () => process.exit(0),
    (e) => {
      console.error(e);
      process.exit(1);
    },
  );
