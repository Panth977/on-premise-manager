/**
 * opm-cloud migrate (§21 #108): copy an OPM cloud between adapters 1:1 and verify it.
 *   --from firebase --to local   every Firestore document (recursive collection walk, incl. sub-collections under
 *                                missing parents), every Storage object, every Auth user (no passwords: Firebase does not
 *                                export usable hashes → the reset flow, see docs-v2/self-host.md)
 *   --from local --to firebase   the reverse (needs --confirm-project <same id>; never used against production here)
 * Reads of the source are read-only. Verification: per-collection document counts + a sha256 over the canonical
 * encoding of every document, object sha256s, user counts + identity hashes. Writes go to the destination with the
 * outbox OFF (copied commits must not re-trigger the mirror / image import).
 */
import { createHash } from 'node:crypto';
import { mkdirSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { pipeline } from 'node:stream/promises';
import { Transform } from 'node:stream';
import { Timestamp, type AuthAdmin, type BlobStore, type DocData, type DocStore } from '../ports/index.js';
import type { CloudConfig } from './config.js';

type Arg = (name: string) => string | undefined;
type Flag = (name: string) => boolean;

export interface MigrateSide {
  kind: 'firebase' | 'local';
  store: DocStore;
  blobs: BlobStore;
  auth: AuthAdmin;
  /** every document path (sorted not required) */
  paths(): AsyncIterable<string>;
  close(): Promise<void>;
}

/** canonical JSON (sorted keys; Timestamp / bytes tagged like the local adapter) */
export function canonical(v: unknown): string {
  if (v instanceof Timestamp) return `{"$timestamp":{"nanos":${v.nanoseconds},"seconds":${v.seconds}}}`;
  if (Buffer.isBuffer(v) || v instanceof Uint8Array) return `{"$bytes":${JSON.stringify(Buffer.from(v).toString('base64'))}}`;
  if (Array.isArray(v)) return `[${v.map(canonical).join(',')}]`;
  if (v && typeof v === 'object') {
    const keys = Object.keys(v as object).filter((k) => (v as Record<string, unknown>)[k] !== undefined).sort();
    return `{${keys.map((k) => `${JSON.stringify(k)}:${canonical((v as Record<string, unknown>)[k])}`).join(',')}}`;
  }
  return JSON.stringify(v ?? null);
}
const sha = (s: string | Buffer) => createHash('sha256').update(s).digest('hex');
/** collection pattern of a document path: deployments/*\/commits */
export const patternOf = (p: string) => p.split('/').map((s, i) => (i % 2 ? '*' : s)).slice(0, -1).join('/');

export async function localSide(dataDir: string, publicUrl: string): Promise<MigrateSide> {
  const L = await import('../adapters/local/index.js');
  const p = L.localPorts({ dataDir, publicUrl, triggers: [] });
  return {
    kind: 'local',
    store: p.store,
    blobs: p.blobs,
    auth: p.auth,
    async *paths() {
      // materialized first: the SQLite cursor must not stay open while getAll() runs other statements
      const all = Array.from(p.store.allDocuments(), (d) => d.path);
      for (const x of all) yield x;
    },
    close: () => p.close(),
  };
}

interface DocInventory {
  count: number;
  byPattern: Record<string, number>;
  hashes: Map<string, string>;
}
async function inventory(side: MigrateSide, onDoc?: (p: string, d: DocData) => Promise<void>): Promise<DocInventory> {
  const inv: DocInventory = { count: 0, byPattern: {}, hashes: new Map() };
  let buf: string[] = [];
  const flush = async () => {
    const snaps = await side.store.getAll(...buf.map((p) => side.store.doc(p)));
    for (const s of snaps) {
      if (!s.exists) continue;
      const d = s.data()!;
      inv.count++;
      const pat = patternOf(s.ref.path);
      inv.byPattern[pat] = (inv.byPattern[pat] ?? 0) + 1;
      inv.hashes.set(s.ref.path, sha(canonical(d)));
      if (onDoc) await onDoc(s.ref.path, d);
    }
    buf = [];
  };
  for await (const p of side.paths()) {
    buf.push(p);
    if (buf.length >= 300) await flush();
  }
  if (buf.length) await flush();
  return inv;
}

async function hashBlob(b: BlobStore, p: string): Promise<{ hex: string; size: number }> {
  const h = createHash('sha256');
  let size = 0;
  await pipeline(
    b.stream(p),
    new Transform({
      transform(c: Buffer, _e, cb) {
        h.update(c);
        size += c.length;
        cb();
      },
    }),
  );
  return { hex: h.digest('hex'), size };
}

export interface MigrateReport {
  from: string;
  to: string;
  dryRun: boolean;
  startedAt: string;
  finishedAt: string;
  docs: { source: number; destination: number; byPattern: Record<string, { source: number; destination: number }>; missing: string[]; extra: string[]; different: string[]; digest: { source: string; destination: string } };
  objects: { source: number; copied: number; skipped: number; bytes: number; mismatched: string[]; missing: string[]; digest: { source: string; destination: string } };
  users: { source: number; copied: number; skipped: string[]; missing: string[]; withoutPassword: number };
  ok: boolean;
}

export async function migrate(src: MigrateSide, dst: MigrateSide, o: { dryRun: boolean; log: (s: string) => void; skipExistingObjects?: boolean }): Promise<MigrateReport> {
  const startedAt = new Date().toISOString();
  const log = o.log;
  // ── documents
  log(`documents: reading ${src.kind} …`);
  let batch = dst.store.batch();
  let inBatch = 0;
  const srcInv = await inventory(src, o.dryRun ? undefined : async (p, d) => {
    batch.set(dst.store.doc(p), d);
    if (++inBatch >= 400) {
      await batch.commit();
      batch = dst.store.batch();
      inBatch = 0;
    }
  });
  if (!o.dryRun && inBatch) await batch.commit();
  log(`documents: ${srcInv.count} in ${Object.keys(srcInv.byPattern).length} collections${o.dryRun ? ' (dry run: nothing written)' : ' copied'}`);
  const dstInv = o.dryRun ? { count: 0, byPattern: {}, hashes: new Map<string, string>() } : await inventory(dst);
  const missing: string[] = [];
  const different: string[] = [];
  for (const [p, h] of srcInv.hashes) {
    const d = dstInv.hashes.get(p);
    if (d === undefined) missing.push(p);
    else if (d !== h) different.push(p);
  }
  const extra = [...dstInv.hashes.keys()].filter((p) => !srcInv.hashes.has(p));
  const digest = (m: Map<string, string>) => sha([...m.entries()].sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)).map(([p, h]) => `${p} ${h}`).join('\n'));
  const byPattern: MigrateReport['docs']['byPattern'] = {};
  for (const k of new Set([...Object.keys(srcInv.byPattern), ...Object.keys(dstInv.byPattern)])) byPattern[k] = { source: srcInv.byPattern[k] ?? 0, destination: (dstInv.byPattern as Record<string, number>)[k] ?? 0 };

  // ── objects
  log('objects: listing …');
  const objs = await src.blobs.list('');
  let copied = 0;
  let skipped = 0;
  let bytes = 0;
  const mismatched: string[] = [];
  const missingObj: string[] = [];
  const srcObjHash = new Map<string, string>();
  const dstObjHash = new Map<string, string>();
  for (const f of objs) {
    const h = await src.blobs.head(f.path);
    if (!h) continue;
    if (o.dryRun) {
      srcObjHash.set(f.path, `${h.size}`);
      continue;
    }
    const exists = o.skipExistingObjects ? await dst.blobs.head(f.path) : null;
    let sHex: string;
    if (exists && exists.size === h.size) {
      sHex = (await hashBlob(src.blobs, f.path)).hex;
      skipped++;
    } else {
      const hh = createHash('sha256');
      await pipeline(
        src.blobs.stream(f.path),
        new Transform({
          transform(c: Buffer, _e, cb) {
            hh.update(c);
            cb(null, c);
          },
        }),
        dst.blobs.writeStream(f.path, { contentType: h.contentType, meta: h.meta }),
      );
      sHex = hh.digest('hex');
      copied++;
      bytes += h.size;
    }
    srcObjHash.set(f.path, sHex);
    const dh = await dst.blobs.head(f.path);
    if (!dh) {
      missingObj.push(f.path);
      continue;
    }
    const d = await hashBlob(dst.blobs, f.path);
    dstObjHash.set(f.path, d.hex);
    if (d.hex !== sHex) mismatched.push(f.path);
  }
  log(`objects: ${objs.length}${o.dryRun ? '' : `, copied ${copied}, kept ${skipped}, ${bytes} bytes`}`);

  // ── users (no password hashes: the person sets one through a reset link)
  const users = await src.auth.listAllUsers();
  const skippedUsers: string[] = [];
  const missingUsers: string[] = [];
  let copiedUsers = 0;
  if (!o.dryRun)
    for (const u of users) {
      if (!u.email) {
        skippedUsers.push(`${u.uid} (no email)`);
        continue;
      }
      try {
        await dst.auth.getUser(u.uid);
        await dst.auth.updateUser(u.uid, { email: u.email, displayName: u.displayName, disabled: u.disabled, emailVerified: u.emailVerified });
      } catch (e) {
        if ((e as { code?: string }).code !== 'auth/user-not-found') throw e;
        await dst.auth.createUser({ uid: u.uid, email: u.email, displayName: u.displayName, disabled: u.disabled, emailVerified: u.emailVerified });
      }
      copiedUsers++;
    }
  if (!o.dryRun) {
    const after = new Map((await dst.auth.listAllUsers()).map((u) => [u.uid, u]));
    for (const u of users) if (u.email && (!after.has(u.uid) || after.get(u.uid)!.email !== u.email?.toLowerCase() && after.get(u.uid)!.email !== u.email)) missingUsers.push(u.uid);
  }
  log(`users: ${users.length}${o.dryRun ? '' : `, copied ${copiedUsers} (no passwords — reset links)`}`);

  const report: MigrateReport = {
    from: src.kind,
    to: dst.kind,
    dryRun: o.dryRun,
    startedAt,
    finishedAt: new Date().toISOString(),
    docs: { source: srcInv.count, destination: dstInv.count, byPattern, missing, extra, different, digest: { source: digest(srcInv.hashes), destination: digest(dstInv.hashes) } },
    objects: { source: objs.length, copied, skipped, bytes, mismatched, missing: missingObj, digest: { source: digest(srcObjHash), destination: digest(dstObjHash) } },
    users: { source: users.length, copied: copiedUsers, skipped: skippedUsers, missing: missingUsers, withoutPassword: copiedUsers },
    ok: false,
  };
  report.ok = o.dryRun || (!missing.length && !different.length && !mismatched.length && !missingObj.length && !missingUsers.length && report.docs.digest.source === report.docs.digest.destination && report.objects.digest.source === report.objects.digest.destination);
  return report;
}

export async function migrateCommand(cfg: CloudConfig, arg: Arg, flag: Flag, firebaseSide: (project: string, bucket: string | undefined) => Promise<MigrateSide>): Promise<number> {
  const from = arg('from');
  const to = arg('to');
  const project = arg('project');
  const dryRun = flag('dry-run');
  if (!from || !to || from === to || ![from, to].every((x) => x === 'firebase' || x === 'local') || !project) {
    console.error('usage: opm-cloud migrate --from firebase|local --to local|firebase --project <firebase project> [--dry-run] [--bucket B] [--data-dir D] [--report FILE]');
    return 2;
  }
  if (to === 'firebase' && !dryRun && arg('confirm-project') !== project) {
    console.error(`writing INTO Firebase project ${project} needs --confirm-project ${project}`);
    return 2;
  }
  const dataDir = path.resolve(arg('data-dir') ?? cfg.dataDir);
  const fb = await firebaseSide(project, arg('bucket'));
  const lo = await localSide(dataDir, cfg.publicUrl);
  const [src, dst] = from === 'firebase' ? [fb, lo] : [lo, fb];
  const report = await migrate(src, dst, { dryRun, log: (s) => console.error(s), skipExistingObjects: flag('skip-existing') });
  const out = arg('report') ?? (to === 'local' ? path.join(dataDir, 'migration-report.json') : null);
  if (out) {
    mkdirSync(path.dirname(out), { recursive: true });
    writeFileSync(out, JSON.stringify(report, null, 2) + '\n');
  }
  const short = { ok: report.ok, dryRun, docs: { source: report.docs.source, destination: report.docs.destination, missing: report.docs.missing.length, different: report.docs.different.length, extra: report.docs.extra.length }, objects: { source: report.objects.source, copied: report.objects.copied, mismatched: report.objects.mismatched.length }, users: { source: report.users.source, copied: report.users.copied }, report: out };
  console.log(JSON.stringify(short, null, 2));
  await src.close();
  await dst.close();
  return report.ok ? 0 : 1;
}
