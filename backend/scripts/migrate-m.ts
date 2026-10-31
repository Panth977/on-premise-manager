/**
 * migrate-m — §21 #123 Files: the §21 #100 transfers become shared files. Whole database (or --deployment <id>), idempotent.
 *
 * For every transfer of every deployment that is not deleted, one file deployments/{dep}/files/{fileId}:
 *   - fileId = the transferId, unchanged (agents ≥ #123 convert their local transfer copies to files with the same id,
 *     so both sides name the file alike); a file that already exists is left alone.
 *   - expired (deleted) and uploading transfers are SKIPPED (reported).
 *   - to-site (sent from the console) → origin { kind: 'cloud' }, publishedBy = the sender's account email (uid when the
 *     account is gone); sites = [siteSlug] when the site fetched it (fetchedAt, or saved-on-server / downloaded).
 *   - to-cloud (sent by a site) → origin { kind: 'site', siteSlug, via: 'browser', sourcePath: null } (the cloud never
 *     knew whether the site picked a path), publishedBy = the site's actor; sites = [siteSlug].
 *   - state ready, publishedAt = createdAt, note = the transfer note (empty → null), cloud.stored = the transfer object
 *     exists. The object is NOT moved: _storagePath = transfers/<dep>/<transferId>; _legacyTransferId = the transferId
 *     (unpublishing the file expires the transfer too, so agents that still use transfers drop their copy).
 * The transfers themselves are kept unchanged (agents < #123 keep using them). One transaction per deployment
 * (re-checks every file; agentEpoch+1 when it created any, so the agents see the listing). No activity.
 * One `system.migration` audit entry per --apply run that changed something (op system:migrate-m, data.migration 'm').
 *
 * Usage (from backend/):
 *   local:    npx tsx scripts/migrate-m.ts --project local --adapter local --data-dir <dir> [--dry-run | --apply]
 *   npx tsx scripts/migrate-m.ts --data-dir <dir> [--dry-run | --apply] [--deployment <id>] [--report <file.json>]
 * Default is the dry run; only --apply writes.
 */
import { writeFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
import type { BlobStore, DocStore as Firestore } from '../src/driver/index.js';
import { auditActionKeys, type SharedFile, type Transfer } from '@opm/shared';

export type StoredFileM = SharedFile & { _storagePath: string; _uploadId: null; _legacyTransferId: string };
export interface ItemM {
  deploymentId: string;
  transferId: string;
  direction: Transfer['direction'];
  siteSlug: string;
  fileName: string;
  action: 'create' | 'exists' | 'skip';
  reason?: string;
  file?: StoredFileM;
}
export interface PlanM {
  items: ItemM[];
  counts: { create: number; exists: number; skip: number };
}

export async function planMigrationM(db: Firestore, blobs: BlobStore, o: { deploymentId?: string } = {}): Promise<PlanM> {
  const items: ItemM[] = [];
  const deps = o.deploymentId ? [await db.collection('deployments').doc(o.deploymentId).get()] : (await db.collection('deployments').get()).docs;
  const emails = new Map<string, string>();
  const emailOf = async (uid: string) => {
    if (uid.includes('@')) return uid;
    if (!emails.has(uid)) emails.set(uid, ((await db.collection('accounts').doc(uid).get()).data()?.email as string | undefined) ?? uid);
    return emails.get(uid)!;
  };
  for (const d of deps) {
    if (!d.exists || d.data()?.deleted) continue;
    const depId = d.id;
    const ts = await db.collection('deployments').doc(depId).collection('transfers').get();
    const files = db.collection('deployments').doc(depId).collection('files');
    for (const s of ts.docs.sort((a, b) => String(a.data().createdAt).localeCompare(String(b.data().createdAt)))) {
      const t = s.data() as Transfer;
      const base = { deploymentId: depId, transferId: t.transferId, direction: t.direction, siteSlug: t.siteSlug, fileName: t.fileName };
      if (t.status === 'expired' || t.status === 'uploading') {
        items.push({ ...base, action: 'skip', reason: t.status === 'expired' ? 'deleted' : 'still uploading' });
        continue;
      }
      if ((await files.doc(t.transferId).get()).exists) {
        items.push({ ...base, action: 'exists' });
        continue;
      }
      const toSite = t.direction === 'to-site';
      const fetched = !!t.fetchedAt || t.status === 'saved-on-server' || t.status === 'downloaded';
      const file: StoredFileM = {
        fileId: t.transferId,
        fileName: t.fileName,
        sizeBytes: t.sizeBytes,
        sha256: t.sha256,
        note: t.note ? t.note : null,
        publishedAt: t.createdAt,
        publishedBy: await emailOf(t.createdBy),
        origin: toSite ? { kind: 'cloud' } : { kind: 'site', siteSlug: t.siteSlug, via: 'browser', sourcePath: null },
        state: 'ready',
        unpublishedAt: null,
        unpublishedBy: null,
        cloud: { stored: (await blobs.head(t.storagePath)) !== null, fetch: null },
        sites: !toSite || fetched ? [t.siteSlug] : [],
        _storagePath: t.storagePath,
        _uploadId: null,
        _legacyTransferId: t.transferId,
      };
      items.push({ ...base, action: 'create', file });
    }
  }
  const counts = { create: 0, exists: 0, skip: 0 };
  for (const i of items) counts[i.action]++;
  return { items, counts };
}

export function printPlanM(p: PlanM) {
  console.log(`transfers: ${p.items.length} — create ${p.counts.create}, already a file ${p.counts.exists}, skip ${p.counts.skip}`);
  for (const i of p.items) {
    if (i.action === 'exists') continue;
    const what = i.action === 'create' ? `→ file (${i.file!.origin.kind === 'cloud' ? 'from the cloud' : `from ${i.siteSlug}`}; copies: ${[...(i.file!.cloud.stored ? ['cloud'] : []), ...i.file!.sites].join(', ') || 'none'})` : `(${i.reason})`;
    console.log(`  ${i.action.padEnd(6)} deployments/${i.deploymentId}/transfers/${i.transferId} ${i.direction} ${i.siteSlug} "${i.fileName}" ${what}`);
  }
}

export async function applyMigrationM(db: Firestore, p: PlanM): Promise<{ applied: number; refused: string[] }> {
  let applied = 0;
  const refused: string[] = [];
  const byDep = new Map<string, ItemM[]>();
  for (const i of p.items) if (i.action === 'create') byDep.set(i.deploymentId, [...(byDep.get(i.deploymentId) ?? []), i]);
  for (const [depId, list] of byDep) {
    const depRef = db.collection('deployments').doc(depId);
    const files = depRef.collection('files');
    const n = await db.runTransaction(async (tx) => {
      const ds = await tx.get(depRef);
      const cur = await Promise.all(list.map((i) => tx.get(files.doc(i.transferId))));
      if (!ds.exists || ds.data()?.deleted) return 0;
      let made = 0;
      list.forEach((i, k) => {
        if (cur[k]!.exists) return; // made since the plan (rerun reports it as existing)
        tx.create(files.doc(i.transferId), i.file!);
        made++;
      });
      if (made) tx.update(depRef, { agentEpoch: ((ds.data()!.agentEpoch as number) ?? 0) + 1 });
      return made;
    });
    applied += n;
    if (n < list.length) refused.push(`deployments/${depId}: ${list.length - n} file(s) changed since the plan (rerun)`);
  }
  if (applied) {
    const ref = db.collection('auditLog').doc();
    await ref.create({
      auditId: ref.id,
      ts: new Date().toISOString(),
      op: 'system:migrate-m',
      actor: 'system',
      actorEmail: null,
      action: 'system.migration',
      outcome: 'ok',
      status: 200,
      errorCode: null,
      errorMessage: null,
      targetType: 'system',
      target: 'system/migration-m',
      requestId: null,
      ip: null,
      userAgent: null,
      changesTotal: 0,
      _changes: '[]',
      _data: JSON.stringify({ migration: 'm', dryRun: false, applied, refused, counts: p.counts, items: p.items.filter((x) => x.action !== 'exists').map(({ file: _f, ...x }) => x) }).slice(0, 900_000),
      _actionKeys: auditActionKeys('system.migration'),
    });
  }
  return { applied, refused };
}

async function main() {
  const { arg, flag, init } = await import('./_admin.ts');
  const project = arg('project');
  const apply = flag('apply');
  const report = arg('report');
  const deploymentId = arg('deployment');
  if (!project) {
    console.error('usage: tsx scripts/migrate-m.ts --project <id> [--adapter local --data-dir <dir>] [--deployment <id>] [--dry-run (default) | --apply] [--report <file.json>]');
    process.exit(2);
  }
  const { db, blobs, close } = init(project);
  console.log(`migrate-m on ${project} — ${apply ? 'APPLY' : 'dry run (nothing is written)'}`);
  const plan = await planMigrationM(db, blobs, { deploymentId });
  printPlanM(plan);
  let result: { applied: number; refused: string[] } | null = null;
  if (apply) {
    result = await applyMigrationM(db, plan);
    console.log(result.applied ? `applied ${result.applied} file(s) (+ one system.migration audit entry)` : 'nothing to do');
    for (const r of result.refused) console.log(`REFUSED ${r}`);
  } else console.log('dry run: nothing written (pass --apply)');
  if (report) writeFileSync(report, JSON.stringify({ project, apply, counts: plan.counts, items: plan.items, result }, null, 1));
  await close();
  if (result?.refused.length) process.exit(3);
}
if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href)
  main().catch((e) => {
    console.error(e);
    process.exit(1);
  });
