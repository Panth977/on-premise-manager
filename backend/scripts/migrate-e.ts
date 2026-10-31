/**
 * migrate-e — whole-DB migration of every stored edge to the round-E shape (nginx parity, BRIEF-E "Contract" / SPEC §12.1):
 *   shared normalizeEdge() on every `sites[].config.edge`, NOTHING ELSE:
 *     - route match {pathPrefix, methods} → {kind 'prefix', path, stopRegex false, methods}
 *     - proxy timeoutSec → readTimeoutSec (+ connectTimeoutSec / sendTimeoutSec 60, rewrite null, requestBuffering /
 *       responseBuffering false, maxBodyBytes null = inherit the edge); websocket KEPT as stored
 *     - route responseHeaders → null (inherit the host), host responseHeaders → empty, edge maxBodyBytes → null (unlimited)
 *   Request headers are NOT moved (a response-only header in setHeaders is reported as a NOTE: the GUI shows warning
 *   W_RESPONSE_HEADER_AS_REQUEST with a one-click fix); route ids / order / paths unchanged; files / containers / volumes
 *   untouched (other site-config keys are not re-normalized).
 *   Round E volumes: every declared site volume without kind / seed → kind 'dir', seed null (only missing keys filled).
 *   NOTES (no change, behaviour to review): a migrated prefix without a trailing "/" (≠ "/") is now an nginx plain string
 *   prefix ("/api" also matches "/apix"; before: "/api" + "/api/…" only); prefix routes are now chosen by the LONGEST match
 *   (nginx), not by `order` — reported where a lower-order shorter prefix used to shadow a longer one.
 *   in: templates (_body), deployments (_draft, _srcSnapshot; also deleted ones), revisions (_config — open proposals and
 *     stored revisions; hash + catalogHash recomputed only when the config changes; the catalog is unchanged), as migrate-d.
 *   Templates keep version / updatedAt. One `system.migration` audit entry per --apply run that changes something
 *   (op system:migrate-e, data.migration = 'e'). Idempotent: a second run plans 0 changes.
 *
 * Usage (from backend/):
 *   npx tsx scripts/migrate-e.ts --data-dir <dir> [--dry-run | --apply]
 * Default is the dry run; only --apply writes.
 */
import { pathToFileURL } from 'node:url';
import type { DocStore as Firestore } from '../functions/src/ports/index.js';
import { auditActionKeys, canonicalize, isResponseOnlyHeader, normalizeEdge, type Edge, type EdgeRoute } from '@opm/shared';
import { jcsHash } from '../functions/src/core/util.ts';

type Doc = Record<string, unknown>;
const isObj = (v: unknown): v is Doc => !!v && typeof v === 'object' && !Array.isArray(v);
const same = (a: unknown, b: unknown) => canonicalize(a ?? null) === canonicalize(b ?? null);

export type Op = { op: 'update'; path: string; data: Record<string, unknown>; why: string };
export interface DocSummary {
  path: string;
  lines: string[];
  notes: string[];
}
export interface PlanE {
  ops: Op[];
  docs: DocSummary[];
  warnings: string[];
  counts: Record<string, number>;
}
interface Tally {
  sites: number;
  routes: number;
  routesMigrated: number;
  matchConverted: number;
  timeoutsConverted: number;
  hostsResponseHeaders: number;
  edgeMaxBody: number;
  notesPlainPrefix: number;
  notesResponseHeader: number;
  notesOrder: number;
  volumes: number;
}
const newTally = (): Tally => ({ sites: 0, routes: 0, routesMigrated: 0, matchConverted: 0, timeoutsConverted: 0, hostsResponseHeaders: 0, edgeMaxBody: 0, notesPlainPrefix: 0, notesResponseHeader: 0, notesOrder: 0, volumes: 0 });

/** What changed on one route (human text), legacy → round E. */
function routeChanges(b: Doc, a: EdgeRoute): string[] {
  const out: string[] = [];
  const bm = isObj(b.match) ? b.match : {};
  if (typeof bm.pathPrefix === 'string') out.push(`match {pathPrefix "${bm.pathPrefix}"} → prefix "${a.match.path}" (stopRegex off)`);
  else if (!same(bm, a.match)) out.push(`match → ${JSON.stringify(a.match)}`);
  const ba = isObj(b.action) ? b.action : {};
  if (a.action.type === 'proxy') {
    const p = a.action;
    if (typeof ba.timeoutSec === 'number') out.push(`timeoutSec ${ba.timeoutSec} → readTimeoutSec ${p.readTimeoutSec} (connect ${p.connectTimeoutSec}, send ${p.sendTimeoutSec})`);
    const filled = (['connectTimeoutSec', 'sendTimeoutSec', 'readTimeoutSec', 'rewrite', 'requestBuffering', 'responseBuffering', 'maxBodyBytes', 'websocket', 'addPrefix', 'setHeaders', 'removeHeaders'] as const).filter(
      (k) => !(k in ba) && !(typeof ba.timeoutSec === 'number' && (k === 'readTimeoutSec' || k === 'connectTimeoutSec' || k === 'sendTimeoutSec')),
    );
    if (filled.length) out.push(`proxy defaults: ${filled.map((k) => `${k} ${JSON.stringify(p[k])}`).join(', ')}`);
    out.push(`websocket ${p.websocket} (kept)`);
  } else if (!same(ba, a.action)) out.push(`${a.action.type} action normalized`);
  if (!('responseHeaders' in b)) out.push('responseHeaders → null (inherit host)');
  if (!same(b.hosts, a.hosts)) out.push(`hosts → ${JSON.stringify(a.hosts)}`);
  return out;
}

/** Notes (no data change) for a migrated edge. `legacyPrefix` = route ids that were {pathPrefix} before. */
function edgeNotes(s: string, e: Edge, legacyPrefix: Set<string>, tally: Tally): string[] {
  const notes: string[] = [];
  for (const r of e.routes) {
    if (r.match.kind === 'prefix' && legacyPrefix.has(r.routeId) && r.match.path !== '/' && !r.match.path.endsWith('/')) {
      tally.notesPlainPrefix++;
      notes.push(`site ${s}: route ${r.routeId}: prefix "${r.match.path}" has no trailing "/" — nginx plain prefix: now also matches "${r.match.path}x…" (before: "${r.match.path}" and "${r.match.path}/…" only)`);
    }
    if (r.action.type === 'proxy')
      for (const [k, v] of Object.entries(r.action.setHeaders))
        if (typeof v === 'string' && isResponseOnlyHeader(k, v)) {
          tally.notesResponseHeader++;
          notes.push(`site ${s}: route ${r.routeId}: setHeaders ${k}: ${JSON.stringify(v)} → W_RESPONSE_HEADER_AS_REQUEST (sent to the container, not the browser; NOT moved — GUI one-click fix)`);
        }
  }
  // prefix selection is now longest-match (nginx), not by order: a shorter prefix with a LOWER order used to win
  const pre = e.routes.filter((r) => r.match.kind === 'prefix' && legacyPrefix.has(r.routeId));
  for (const a of pre)
    for (const b of pre) {
      if (a === b || a.order >= b.order || b.match.path.length <= a.match.path.length) continue;
      const covers = a.match.path === '/' || b.match.path === a.match.path || b.match.path.startsWith(a.match.path.endsWith('/') ? a.match.path : `${a.match.path}/`);
      const hostsOverlap = !a.hosts.length || !b.hosts.length || a.hosts.some((h) => b.hosts.map((x) => x.toLowerCase()).includes(h.toLowerCase()));
      if (!covers || !hostsOverlap) continue;
      tally.notesOrder++;
      notes.push(`site ${s}: route ${b.routeId} ("${b.match.path}", order ${b.order}) was shadowed by route ${a.routeId} ("${a.match.path}", order ${a.order}) — longest prefix wins now, so ${b.routeId} takes its paths`);
    }
  return notes;
}

/**
 * Round E volumes: every declared site volume without `kind` / `seed` gets kind 'dir' (a folder, as before) and seed null
 * (starts empty) — only missing keys are filled, nothing else.
 */
function convertVolumes(s: string, vols: unknown, tally: Tally, lines: string[]): unknown[] | null {
  if (!Array.isArray(vols)) return null;
  const done: string[] = [];
  const next = vols.map((v) => {
    if (!isObj(v) || ('kind' in v && 'seed' in v)) return v;
    done.push(String(v.volumeSlug));
    return { ...v, kind: v.kind ?? 'dir', seed: v.seed === undefined ? null : v.seed };
  });
  if (!done.length) return null;
  tally.volumes += done.length;
  lines.push(`site ${s}: volumes → kind dir, seed none (starts empty): ${done.join(', ')}`);
  return next;
}

/** Migrate the edges (+ volume kind / seed) of one config-like (only `sites[].config.edge` and `.volumes` change). */
export function convertConfigE(cfg: Doc, where: string, tally: Tally, warn: (m: string) => void): { next: Doc | null; lines: string[]; notes: string[] } {
  const lines: string[] = [];
  const notes: string[] = [];
  if (!Array.isArray(cfg.sites)) return { next: null, lines, notes };
  let changed = false;
  const sites = (cfg.sites as Doc[]).map((site0) => {
    if (!isObj(site0) || !isObj(site0.config)) return site0;
    const s = String(site0.siteSlug);
    const vols = convertVolumes(s, site0.config.volumes, tally, lines);
    if (vols) changed = true;
    const site: Doc & { config: Doc } = vols ? { ...site0, config: { ...site0.config, volumes: vols } } : (site0 as Doc & { config: Doc });
    if (!isObj(site.config.edge)) return site;
    const before = site.config.edge as Doc;
    let after: Edge;
    try {
      after = normalizeEdge(before as Partial<Edge> & Record<string, unknown>);
    } catch (e) {
      warn(`${where} site ${s}: edge cannot be normalized (${(e as Error).message}) — left unchanged`);
      return site;
    }
    if (same(after, before)) return site;
    changed = true;
    tally.sites++;
    const br = Array.isArray(before.routes) ? (before.routes as Doc[]) : [];
    tally.routes += after.routes.length;
    const legacyPrefix = new Set<string>();
    let migrated = 0;
    const rl: string[] = [];
    after.routes.forEach((a, i) => {
      const b = br[i];
      if (!isObj(b) || same(a, b)) return;
      migrated++;
      if (isObj(b.match) && typeof b.match.pathPrefix === 'string') {
        legacyPrefix.add(a.routeId);
        tally.matchConverted++;
      }
      if (isObj(b.action) && typeof b.action.timeoutSec === 'number') tally.timeoutsConverted++;
      rl.push(`site ${s}: route ${a.routeId}: ${routeChanges(b, a).join('; ')}`);
    });
    tally.routesMigrated += migrated;
    lines.push(`site ${s}: ${migrated} of ${after.routes.length} route(s) migrated`);
    if (!('maxBodyBytes' in before)) {
      tally.edgeMaxBody++;
      lines.push(`site ${s}: edge maxBodyBytes → null (unlimited)`);
    }
    const bh = Array.isArray(before.hosts) ? (before.hosts as Doc[]) : [];
    after.hosts.forEach((h, i) => {
      const b = bh[i];
      if (isObj(b) && !isObj(b.responseHeaders)) {
        tally.hostsResponseHeaders++;
        lines.push(`site ${s}: host ${h.hostname}: responseHeaders → empty`);
      }
    });
    lines.push(...rl);
    const rest = (['httpPort', 'httpsPort'] as const).filter((k) => !same(before[k], after[k]));
    if (rest.length || !Array.isArray(before.hosts) || !Array.isArray(before.routes)) lines.push(`site ${s}: edge defaults filled: ${[...rest, ...(!Array.isArray(before.hosts) ? ['hosts'] : []), ...(!Array.isArray(before.routes) ? ['routes'] : [])].join(', ')}`);
    notes.push(...edgeNotes(s, after, legacyPrefix, tally));
    return { ...site, config: { ...site.config, edge: after } };
  });
  return { next: changed ? { ...cfg, sites } : null, lines, notes };
}

export async function planMigrationE(db: Firestore): Promise<PlanE> {
  const plan: PlanE = { ops: [], docs: [], warnings: [], counts: {} };
  const count = (k: string, n = 1) => (plan.counts[k] = (plan.counts[k] ?? 0) + n);
  const warn = (m: string) => plan.warnings.push(m);
  const tally = newTally();

  for (const d of (await db.collection('templates').get()).docs) {
    const t = d.data();
    if (typeof t._body !== 'string') continue;
    const path = `templates/${d.id}`;
    const r = convertConfigE(JSON.parse(t._body), `${path} (${t.label})`, tally, warn);
    if (!r.next) continue;
    plan.ops.push({ op: 'update', path, data: { _body: JSON.stringify(r.next) }, why: 'template edges → round-E shape (version / updatedAt unchanged)' });
    plan.docs.push({ path: `${path} "${t.label}" v${t.version}`, lines: r.lines, notes: r.notes });
    count('templates');
  }
  for (const d of (await db.collection('deployments').get()).docs) {
    const x = d.data();
    const path = `deployments/${d.id}`;
    const upd: Doc = {};
    const lines: string[] = [];
    const notes: string[] = [];
    if (typeof x._draft === 'string') {
      const r = convertConfigE(JSON.parse(x._draft), `${path} draft`, tally, warn);
      if (r.next) {
        upd._draft = JSON.stringify(r.next);
        lines.push(...r.lines.map((l) => `draft: ${l}`));
        notes.push(...r.notes.map((l) => `draft: ${l}`));
      }
    }
    if (typeof x._srcSnapshot === 'string') {
      const snap = JSON.parse(x._srcSnapshot);
      if (isObj(snap)) {
        const r = convertConfigE(snap, `${path} srcTemplate snapshot`, tally, warn);
        if (r.next) {
          upd._srcSnapshot = JSON.stringify(r.next);
          lines.push(...r.lines.map((l) => `srcTemplate snapshot: ${l}`));
          notes.push(...r.notes.map((l) => `srcTemplate snapshot: ${l}`));
        }
      }
    }
    if (Object.keys(upd).length) {
      plan.ops.push({ op: 'update', path, data: upd, why: `deployment ${Object.keys(upd).join(', ')} edges → round-E shape` });
      plan.docs.push({ path: `${path} "${x.label}"${x.deleted ? ' (deleted)' : ''}`, lines, notes });
      count('deployments');
    }
    for (const rv of (await d.ref.collection('revisions').get()).docs) {
      const r0 = rv.data();
      if (typeof r0._config !== 'string' || typeof r0._catalog !== 'string') continue;
      const rpath = `${path}/revisions/${rv.id}`;
      const r = convertConfigE(JSON.parse(r0._config), `${rpath} (seq ${r0.seq})`, tally, warn);
      if (!r.next) continue;
      const hash = jcsHash(r.next);
      plan.ops.push({ op: 'update', path: rpath, data: { _config: JSON.stringify(r.next), hash, catalogHash: jcsHash(JSON.parse(r0._catalog)) }, why: 'revision edges → round-E shape (hash + catalogHash recomputed)' });
      plan.docs.push({ path: `${rpath} seq ${r0.seq}${r0.status ? ` ${r0.status}` : ''} hash ${String(r0.hash).slice(0, 19)}… → ${hash.slice(0, 19)}…`, lines: r.lines, notes: r.notes });
      count('revisions');
    }
  }
  for (const [k, v] of Object.entries(tally)) count(k, v);
  return plan;
}

/** Apply a plan (batches of 400) + one system.migration audit entry. */
export async function applyPlanE(db: Firestore, plan: PlanE): Promise<void> {
  if (!plan.ops.length) return;
  for (let i = 0; i < plan.ops.length; i += 400) {
    const b = db.batch();
    for (const o of plan.ops.slice(i, i + 400)) b.update(db.doc(o.path), o.data);
    await b.commit();
  }
  const ref = db.collection('auditLog').doc();
  const cut = (l: string[]) => l.slice(0, 80).map((x) => x.slice(0, 400));
  let docs = plan.docs.map((d) => ({ path: d.path, lines: cut(d.lines), notes: cut(d.notes) }));
  while (JSON.stringify(docs).length > 600_000 && docs.length > 1) docs = docs.slice(0, Math.ceil(docs.length / 2));
  await ref.create({
    auditId: ref.id,
    ts: new Date().toISOString(),
    op: 'system:migrate-e',
    actor: 'system',
    actorEmail: null,
    action: 'system.migration',
    outcome: 'ok',
    status: 200,
    errorCode: null,
    errorMessage: null,
    targetType: 'system',
    target: 'system/migration-e',
    requestId: null,
    ip: null,
    userAgent: null,
    changesTotal: 0,
    _changes: '[]',
    _data: JSON.stringify({ migration: 'e', dryRun: false, changes: plan.ops.length, counts: plan.counts, docs, docsTotal: plan.docs.length, warnings: plan.warnings.slice(0, 100) }),
    _actionKeys: auditActionKeys('system.migration'),
  });
}

export function printPlan(plan: PlanE, log: (s: string) => void = console.log) {
  for (const d of plan.docs) {
    log(`\n● ${d.path}`);
    for (const l of d.lines) log(`    ${l}`);
    for (const n of d.notes) log(`    NOTE ${n}`);
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
    console.error('usage: tsx scripts/migrate-e.ts --project <id> [--dry-run (default) | --apply]');
    process.exit(2);
  }
  const { db } = init(project);
  console.log(`migrate-e on ${project} — ${apply ? 'APPLY' : 'dry run (nothing is written)'}`);
  const plan = await planMigrationE(db);
  printPlan(plan);
  if (apply) {
    await applyPlanE(db, plan);
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
