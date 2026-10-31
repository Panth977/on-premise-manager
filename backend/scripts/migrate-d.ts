/**
 * migrate-d — whole-DB migration to the round-D data model (site volumes, SPEC-final §21 #58+, BRIEF-D "Contract"):
 *   every site config WITHOUT `volumes` (pre-D) → shared normalizeSiteConfig(sc, image volumes from Firestore):
 *     - image volumes without a row (implicit data volume `<containerSlug>-<imageVolume>` before D) become explicit rows
 *       `{kind:'volume', volumeSlug:'<containerSlug>-<imageVolume>'}` (image known; unknown image → warning, row not added)
 *     - every `{kind:'data', name}` → `{kind:'volume', volumeSlug: name}` with readOnly RESET to false (the round-C editor
 *       stored read-only for data volumes by mistake; every reset is listed)
 *     - every volume used that way is DECLARED on the site `{volumeSlug, label: volumeSlug, description:'', backup:true}`
 *   (a site config that already has `volumes` is only normalized; nothing is auto-declared — an undeclared mount there is a
 *   validation error the user fixes). Files without `render` (pre-C) are converted exactly like migrate-c.
 *   in: templates (_body), deployments (_draft, _srcSnapshot; also deleted ones), revisions (_config — open proposals and
 *     stored revisions; hash + catalogHash recomputed only when the config changes; the catalog is unchanged).
 *   Templates keep version / updatedAt (a migration is not an edit; srcTemplate.templateVersion stays comparable).
 *   One `system.migration` audit entry per --apply run that changes something (data.migration = 'd').
 * Part 2 (one copy per site): every file and every site volume without `location` gets `{kind:'auto'}`
 *   (files <agentHome>/files/<fileSlug>, volumes <agentHome>/volumes/<volumeSlug>); every render-expressions file whose
 *   content uses container.* / file.* (E_NOT_ALLOWED now: files render once per site) is REPORTED (line + WARN), not rewritten.
 * Idempotent: a second run plans 0 changes (content reports repeat as WARN lines).
 *
 * Usage (from backend/):
 *   npx tsx scripts/migrate-d.ts --data-dir <dir> [--dry-run | --apply]
 * Default is the dry run; only --apply writes.
 */
import { pathToFileURL } from 'node:url';
import type { DocStore as Firestore } from '../functions/src/ports/index.js';
import { auditActionKeys, canonicalize, checkFileContent, imageKey, locationLabel, normalizeFileDef, normalizeSiteConfig, type FileDef, type FileExprIssue, type SiteConfig } from '@opm/shared';
import { jcsHash } from '../functions/src/core/util.ts';
import { buildConvertEnv, convertConfig, type BucketLike, type ConvertEnv as ConvertEnvC } from './migrate-c.ts';

/** migrate-c's env + the expression issues of a file content (read from Storage files/<hex>, cached per sha256). */
type ConvertEnv = ConvertEnvC & { fileIssues?: (sha256: string, where: string) => Promise<FileExprIssue[]> };

type Doc = Record<string, unknown>;
const isObj = (v: unknown): v is Doc => !!v && typeof v === 'object' && !Array.isArray(v);
const same = (a: unknown, b: unknown) => canonicalize(a ?? null) === canonicalize(b ?? null);

export type Op = { op: 'update'; path: string; data: Record<string, unknown>; why: string };
export interface DocSummary {
  path: string;
  lines: string[];
}
export interface PlanD {
  ops: Op[];
  docs: DocSummary[];
  warnings: string[];
  counts: Record<string, number>;
}
interface Tally {
  sites: number;
  fileLocations: number;
  volumeLocations: number;
  fileContentIssues: number;
  volumesDeclared: number;
  readOnlyResets: number;
  rowsMadeExplicit: number;
}

const mountName = (v: Doc, i: number) => (v.imageVolume ? `image volume ${v.imageVolume}` : `extra mount #${i + 1}${v.mountPath ? ` @${v.mountPath}` : ''}`);

/** Convert the sites of one config-like to the round-D shape (files: migrate-c's convertConfig). */
export async function convertConfigD(cfg: Doc, where: string, env: ConvertEnv, tally: Tally): Promise<{ next: Doc | null; lines: string[] }> {
  const lines: string[] = [];
  let changed = false;
  let out: Doc = { ...cfg };
  // files: render for pre-C documents (migrate-c's decision), location auto when missing (part 2), and a REPORT of every
  // render-expressions file whose content uses container.* / file.* (rendered once per site now → E_NOT_ALLOWED; the
  // content is not rewritten, the template shows the validation error)
  if (Array.isArray(cfg.files)) {
    const files: unknown[] = [];
    for (const f0 of cfg.files as Doc[]) {
      if (!isObj(f0)) {
        files.push(f0);
        continue;
      }
      let f = f0;
      if (f.render === undefined) {
        const r = await convertConfig({ files: [f] }, where, env);
        if (r.next) {
          f = (r.next.files as Doc[])[0]!;
          lines.push(...r.lines);
        }
      }
      const nf = normalizeFileDef(f as unknown as FileDef) as unknown as Doc;
      if (f0.location === undefined || f0.location === null) {
        tally.fileLocations++;
        lines.push(`file ${f0.fileSlug}: location auto (${locationLabel(null, 'files', String(f0.fileSlug))})`);
      } else if (!same(nf.location, f0.location)) lines.push(`file ${f0.fileSlug}: location ${JSON.stringify(f0.location)} → ${JSON.stringify(nf.location)}`);
      if (nf.render === 'expressions' && nf.editor !== 'binary' && isObj(nf.content) && env.fileIssues) {
        const issues = await env.fileIssues(String(nf.content.sha256), `${where} file ${f0.fileSlug}`);
        for (const i of issues) {
          tally.fileContentIssues++;
          const l = `file ${f0.fileSlug}: content ${i.line}:${i.col} ${i.code} ${i.message} — becomes a validation error (files render once per site; use a variable)`;
          lines.push(l);
          env.warn(`${where}: ${l}`);
        }
      }
      if (!same(nf, f0)) changed = true;
      files.push(nf);
    }
    out.files = files;
  }
  if (Array.isArray(cfg.sites)) {
    const sites = (cfg.sites as Doc[]).map((site) => {
      if (!isObj(site) || !isObj(site.config)) return site;
      const before = site.config as Doc;
      const s = String(site.siteSlug);
      let after: SiteConfig;
      try {
        after = normalizeSiteConfig(before as Partial<SiteConfig>, env.images);
      } catch (e) {
        env.warn(`${where} site ${s}: cannot normalize (${(e as Error).message}) — left unchanged`);
        return site;
      }
      if (same(after, before)) return site;
      changed = true;
      tally.sites++;
      const preD = !Array.isArray(before.volumes);
      const declaredBefore = new Set(Array.isArray(before.volumes) ? (before.volumes as Doc[]).map((v) => String(v?.volumeSlug)) : []);
      const added = after.volumes.filter((v) => !declaredBefore.has(v.volumeSlug));
      tally.volumesDeclared += added.length;
      if (preD) lines.push(`site ${s}: volumes declared [${added.map((v) => `${v.volumeSlug} (backup ${v.backup ? 'on' : 'off'})`).join(', ')}]${added.length ? '' : ' (none — no data volumes in use)'}`);
      else if (added.length) lines.push(`site ${s}: volumes declared [${added.map((v) => v.volumeSlug).join(', ')}]`);
      for (const v of after.volumes) {
        const b = Array.isArray(before.volumes) ? (before.volumes as Doc[]).find((x) => isObj(x) && x.volumeSlug === v.volumeSlug) : undefined;
        if (b && b.location !== undefined && b.location !== null) continue;
        tally.volumeLocations++;
        lines.push(`site ${s}: volume ${v.volumeSlug}: location auto (${locationLabel(v.location, 'volumes', v.volumeSlug)})`);
      }
      const bc = Array.isArray(before.containers) ? (before.containers as Doc[]) : [];
      bc.forEach((c, ci) => {
        const a = after.containers[ci] as unknown as Doc | undefined;
        if (!isObj(c) || !a) return;
        const cs = String(c.containerSlug ?? c.serviceSlug);
        const rows = Array.isArray(c.volumes) ? (c.volumes as Doc[]) : [];
        rows.forEach((v, i) => {
          const src = isObj(v) && isObj(v.source) ? v.source : null;
          if (!src || src.kind !== 'data') return;
          lines.push(`site ${s}: container ${cs}: ${mountName(v, i)}: data:${src.name} → volume:${src.name}`);
          if (v.readOnly === true) {
            tally.readOnlyResets++;
            lines.push(`site ${s}: container ${cs}: ${mountName(v, i)}: READ-ONLY RESET true → false (volume ${src.name})`);
          }
        });
        const afterRows = (a.volumes as Doc[]) ?? [];
        const imgKnown = !!env.images[imageKey(String(c.serviceSlug), String(c.version))];
        if (preD && Array.isArray(c.volumes)) {
          for (const r of afterRows) {
            if (!r.imageVolume || rows.some((x) => isObj(x) && x.imageVolume === r.imageVolume)) continue;
            tally.rowsMadeExplicit++;
            lines.push(`site ${s}: container ${cs}: image volume ${r.imageVolume}: implicit → volume:${(r.source as Doc).volumeSlug} (explicit row)`);
          }
          if (!imgKnown) env.warn(`${where} site ${s}: container ${cs}: image ${c.serviceSlug}:${c.version} is not in the catalog — implicit image volumes could not be made explicit (they will be NOT mounted)`);
        }
        if (!Array.isArray(c.volumes) || 'port' in c || 'volume' in c || c.containerSlug === undefined) lines.push(`site ${s}: container ${cs}: legacy one-per-service shape → instance, volumes [${afterRows.map((r) => `${r.imageVolume ? `image:${r.imageVolume}` : `extra@${r.mountPath}`}=${(r.source as Doc).kind}:${(r.source as Doc).volumeSlug ?? (r.source as Doc).fileSlug ?? (r.source as Doc).secretSlug ?? (r.source as Doc).path ?? ''}${r.readOnly ? ' ro' : ''}`).join(', ')}]`);
      });
      const otherKeys = ['host', 'agentPort', 'agentGuiAliasPort', 'depends', 'notFoundPageId', 'edge'].filter((k) => !same(before[k], (after as unknown as Doc)[k]));
      if (otherKeys.length) lines.push(`site ${s}: defaults filled / normalized: ${otherKeys.join(', ')}`);
      return { ...site, config: after };
    });
    out = { ...out, sites };
  }
  return { next: changed ? out : null, lines };
}

export async function planMigrationD(db: Firestore, bucket: BucketLike | null): Promise<PlanD> {
  const plan: PlanD = { ops: [], docs: [], warnings: [], counts: {} };
  const count = (k: string, n = 1) => (plan.counts[k] = (plan.counts[k] ?? 0) + n);
  const envC = await buildConvertEnv(db, bucket, (m) => plan.warnings.push(m));
  const issueCache = new Map<string, FileExprIssue[]>();
  const env: ConvertEnv = {
    ...envC,
    async fileIssues(sha, where) {
      const hit = issueCache.get(sha);
      if (hit) return hit;
      let res: FileExprIssue[] = [];
      try {
        if (!bucket) throw new Error('no bucket');
        const [buf] = await bucket.file(`files/${sha.replace(/^sha256:/, '')}`).download();
        const text = buf.toString('utf8');
        if (text.includes('{{')) res = checkFileContent(text);
      } catch (e) {
        plan.warnings.push(`${where}: content ${sha} could not be read (${(e as Error).message}) — not checked for container.* / file.*`);
      }
      issueCache.set(sha, res);
      return res;
    },
  };
  const tally: Tally = { sites: 0, fileLocations: 0, volumeLocations: 0, fileContentIssues: 0, volumesDeclared: 0, readOnlyResets: 0, rowsMadeExplicit: 0 };

  for (const d of (await db.collection('templates').get()).docs) {
    const t = d.data();
    if (typeof t._body !== 'string') continue;
    const path = `templates/${d.id}`;
    const r = await convertConfigD(JSON.parse(t._body), `${path} (${t.label})`, env, tally);
    if (!r.next) continue;
    plan.ops.push({ op: 'update', path, data: { _body: JSON.stringify(r.next) }, why: 'template body → round-D shape (version / updatedAt unchanged)' });
    plan.docs.push({ path: `${path} "${t.label}" v${t.version}`, lines: r.lines });
    count('templates');
  }
  for (const d of (await db.collection('deployments').get()).docs) {
    const x = d.data();
    const path = `deployments/${d.id}`;
    const upd: Doc = {};
    const lines: string[] = [];
    if (typeof x._draft === 'string') {
      const r = await convertConfigD(JSON.parse(x._draft), `${path} draft`, env, tally);
      if (r.next) {
        upd._draft = JSON.stringify(r.next);
        lines.push(...r.lines.map((l) => `draft: ${l}`));
      }
    }
    if (typeof x._srcSnapshot === 'string') {
      const snap = JSON.parse(x._srcSnapshot);
      if (isObj(snap)) {
        const r = await convertConfigD(snap, `${path} srcTemplate snapshot`, env, tally);
        if (r.next) {
          upd._srcSnapshot = JSON.stringify(r.next);
          lines.push(...r.lines.map((l) => `srcTemplate snapshot: ${l}`));
        }
      }
    }
    if (Object.keys(upd).length) {
      plan.ops.push({ op: 'update', path, data: upd, why: `deployment ${Object.keys(upd).join(', ')} → round-D shape` });
      plan.docs.push({ path: `${path} "${x.label}"${x.deleted ? ' (deleted)' : ''}`, lines });
      count('deployments');
    }
    for (const rv of (await d.ref.collection('revisions').get()).docs) {
      const r0 = rv.data();
      if (typeof r0._config !== 'string' || typeof r0._catalog !== 'string') continue;
      const rpath = `${path}/revisions/${rv.id}`;
      const r = await convertConfigD(JSON.parse(r0._config), `${rpath} (seq ${r0.seq})`, env, tally);
      if (!r.next) continue;
      const catalog = JSON.parse(r0._catalog);
      const hash = jcsHash(r.next);
      plan.ops.push({ op: 'update', path: rpath, data: { _config: JSON.stringify(r.next), hash, catalogHash: jcsHash(catalog) }, why: 'revision config → round-D shape (hash + catalogHash recomputed)' });
      plan.docs.push({ path: `${rpath} seq ${r0.seq}${r0.status ? ` ${r0.status}` : ''} hash ${String(r0.hash).slice(0, 19)}… → ${hash.slice(0, 19)}…`, lines: r.lines });
      count('revisions');
    }
  }
  count('sites', tally.sites);
  count('volumesDeclared', tally.volumesDeclared);
  count('readOnlyResets', tally.readOnlyResets);
  count('rowsMadeExplicit', tally.rowsMadeExplicit);
  count('fileLocations', tally.fileLocations);
  count('volumeLocations', tally.volumeLocations);
  count('fileContentIssues', tally.fileContentIssues);
  return plan;
}

/** Apply a plan (batches of 400) + one system.migration audit entry. */
export async function applyPlanD(db: Firestore, plan: PlanD): Promise<void> {
  if (!plan.ops.length) return;
  for (let i = 0; i < plan.ops.length; i += 400) {
    const b = db.batch();
    for (const o of plan.ops.slice(i, i + 400)) b.update(db.doc(o.path), o.data);
    await b.commit();
  }
  const ref = db.collection('auditLog').doc();
  let docs = plan.docs.map((d) => ({ path: d.path, lines: d.lines.slice(0, 80).map((l) => l.slice(0, 400)) }));
  while (JSON.stringify(docs).length > 600_000 && docs.length > 1) docs = docs.slice(0, Math.ceil(docs.length / 2));
  await ref.create({
    auditId: ref.id,
    ts: new Date().toISOString(),
    op: 'system:migrate-d',
    actor: 'system',
    actorEmail: null,
    action: 'system.migration',
    outcome: 'ok',
    status: 200,
    errorCode: null,
    errorMessage: null,
    targetType: 'system',
    target: 'system/migration-d',
    requestId: null,
    ip: null,
    userAgent: null,
    changesTotal: 0,
    _changes: '[]',
    _data: JSON.stringify({ migration: 'd', dryRun: false, changes: plan.ops.length, counts: plan.counts, docs, docsTotal: plan.docs.length, warnings: plan.warnings.slice(0, 100) }),
    _actionKeys: auditActionKeys('system.migration'),
  });
}

export function printPlan(plan: PlanD, log: (s: string) => void = console.log) {
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
    console.error('usage: tsx scripts/migrate-d.ts --project <id> [--dry-run (default) | --apply]');
    process.exit(2);
  }
  const { db, bucket } = init(project);
  console.log(`migrate-d on ${project} — ${apply ? 'APPLY' : 'dry run (nothing is written)'}`);
  const plan = await planMigrationD(db, bucket as unknown as BucketLike);
  printPlan(plan);
  if (apply) {
    await applyPlanD(db, plan);
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
