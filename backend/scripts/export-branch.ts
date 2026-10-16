/**
 * export-branch — §21 #106: write the GitHub-mirror tree of ONE template / deployment / agent-branch commit into a local
 * directory, exactly as the mirror would push it (same ExportRequest assembly, same exporter: functions/lib/opm-export.wasm
 * — run `npm run build` first). READ-ONLY on the project: nothing is written to Firestore / Storage / GitHub.
 * Secret values never leave the exporter input (they are stripped), and this script prints none.
 *
 * Usage (from backend/):
 *   prod:     OPM_GCLOUD_TOKEN=1 OPM_GCLOUD_ACCOUNT=<owner> npx tsx scripts/export-branch.ts --project your-firebase-project \
 *               (--template <id> | --deployment <id> [--site <slug> (agent branch)]) [--commit <commitId> (default head)] --out <dir>
 *   emulator: FIRESTORE_EMULATOR_HOST=127.0.0.1:8085 FIREBASE_STORAGE_EMULATOR_HOST=127.0.0.1:9199 npx tsx scripts/export-branch.ts --project demo-opm …
 * Prints the tree (path, mode, size) and the exporter notes; --json writes the ExportRequest to <out>/../export-request.json too.
 */
import { mkdirSync, writeFileSync, chmodSync, existsSync, rmSync } from 'node:fs';
import path from 'node:path';
import { mirrorBranchName, type BranchRef } from '@opm/shared';

async function main() {
  const { arg, flag, init } = await import('./_admin.ts');
  const project = arg('project');
  const out = arg('out');
  const tid = arg('template');
  const did = arg('deployment');
  const site = arg('site');
  if (!project || !out || (!tid && !did) || (tid && did)) {
    console.error('usage: tsx scripts/export-branch.ts --project <id> (--template <id> | --deployment <id> [--site <slug>]) [--commit <id>] --out <dir> [--json]');
    process.exit(2);
  }
  const cl = init(project); // installs the adapter's ports for the backend modules imported below
  process.env.OPM_EXPORT_DIR ??= path.resolve(path.dirname(new URL(import.meta.url).pathname), '../functions/lib');
  const { decodeCommit, commitsOf } = await import('../functions/src/core/branches.ts');
  const { buildRequest, newExportCtx, runExporter, personOf } = await import('../functions/src/core/mirror-export.ts');
  const { db } = await import('../functions/src/core/fire.ts');
  const { slugOf } = await import('../functions/src/core/slugs.ts');
  const ref: BranchRef = tid ? { kind: 'template', templateId: tid } : site ? { kind: 'agent', deploymentId: did!, siteSlug: site } : { kind: 'deployment', deploymentId: did! };
  const ent = await db().collection(tid ? 'templates' : 'deployments').doc((tid ?? did)!).get();
  if (!ent.exists) throw new Error(`${tid ? 'template' : 'deployment'} not found`);
  const slug = slugOf(ent.data()!, tid ? 'template' : 'deployment');
  const label = String(ent.data()!.label ?? slug);
  let snap;
  const cid = arg('commit');
  if (cid) snap = await commitsOf(ref).doc(cid).get();
  else snap = (await commitsOf(ref).orderBy('seq', 'desc').limit(1).get()).docs[0];
  if (!snap?.exists) throw new Error('commit not found');
  const c = await decodeCommit(snap.data()!);
  const kind = ref.kind === 'agent' ? 'agent' : ref.kind;
  const gitBranch = mirrorBranchName(kind, slug, ref.kind === 'agent' ? ref.siteSlug : null);
  const req = await buildRequest(newExportCtx(), {
    branch: { kind, gitBranch, entitySlug: slug, label: ref.kind === 'agent' ? `${label} · ${ref.siteSlug}` : label, templateId: tid ?? null, deploymentId: did ?? null, siteSlug: ref.kind === 'agent' ? ref.siteSlug : null },
    commit: { id: c.commitId, seq: c.seq, kind: c.kind, message: c.message, author: personOf(c.author, ref.kind === 'agent' ? ref.siteSlug : null), createdAt: c.createdAt, hash: c.hash, parentId: c.parentId },
    deploymentId: tid ? `template-${slug}` : did!,
    config: c.config,
    catalog: c.catalog,
    sites: ref.kind === 'agent' ? [ref.siteSlug] : c.config.sites.map((s) => s.siteSlug),
    persist: {},
  });
  const res = await runExporter(req);
  if (existsSync(out)) rmSync(out, { recursive: true, force: true });
  mkdirSync(out, { recursive: true });
  if (flag('json')) writeFileSync(path.join(path.dirname(path.resolve(out)), 'export-request.json'), JSON.stringify(req, null, 2));
  for (const f of res.files) {
    const p = path.join(out, f.path);
    mkdirSync(path.dirname(p), { recursive: true });
    let bytes: Buffer;
    if (f.text !== undefined) bytes = Buffer.from(f.text, 'utf8');
    else if (f.base64 !== undefined) bytes = Buffer.from(f.base64, 'base64');
    else bytes = (await cl.bucket.file(`files/${f.blob!.replace(/^sha256:/, '')}`).download())[0];
    writeFileSync(p, bytes);
    if (f.mode === '100755') chmodSync(p, 0o755);
    console.log(`${f.mode} ${String(f.size).padStart(9)} ${f.path}`);
  }
  console.log(`\nbranch ${gitBranch} · commit ${c.commitId} (seq ${c.seq}) · ${res.files.length} files → ${out}`);
  for (const n of res.notes) console.log(`note ${n.code}${n.site ? ` [${n.site}]` : ''}: ${n.message}`);
  for (const e of res.env) console.log(`env ${e.site}/${e.name}${e.required ? ' (required)' : ''}: ${e.description}`);
}
main().catch((e) => {
  console.error(e?.message ?? e);
  process.exit(1);
});
