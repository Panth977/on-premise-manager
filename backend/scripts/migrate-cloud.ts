/**
 * migrate-cloud (§21 #108) — copy an OPM cloud between Firebase and a local opm-cloud data dir, then verify it
 * (document counts + canonical sha256 per document, object sha256s, users). Runs from the repo checkout because it needs
 * the Firebase credentials helpers of _admin.ts (ADC / scripts key / OPM_GCLOUD_TOKEN=1).
 *
 *   npx tsx scripts/migrate-cloud.ts --from firebase --to local --project your-firebase-project --data-dir /var/lib/opm-cloud [--dry-run]
 *   npx tsx scripts/migrate-cloud.ts --from local --to firebase --project P --data-dir D --confirm-project P
 * Reading Firebase is read-only. Users are copied without passwords (reset links: `opm-cloud reset-link --email`).
 */
import { arg, flag, initFirebase } from './_admin.ts';
import { loadConfig } from '../functions/src/server/config.ts';
import { migrateCommand, type MigrateSide } from '../functions/src/server/migrate.ts';

export async function firebaseSide(project: string, bucket: string | undefined): Promise<MigrateSide> {
  const c = initFirebase(project, bucket);
  const fs = c.firestore!;
  type DocRefRaw = ReturnType<typeof fs.doc>;
  type ColRaw = ReturnType<typeof fs.collection>;
  async function* walkCol(col: ColRaw): AsyncIterable<string> {
    const refs = await col.listDocuments();
    for (let i = 0; i < refs.length; i += 300) {
      const chunk = refs.slice(i, i + 300);
      const snaps = await fs.getAll(...chunk);
      for (const s of snaps) if (s.exists) yield s.ref.path;
      for (const r of chunk) yield* walkDoc(r);
    }
  }
  async function* walkDoc(doc: DocRefRaw): AsyncIterable<string> {
    for (const sub of await doc.listCollections()) yield* walkCol(sub);
  }
  return {
    kind: 'firebase',
    store: c.db,
    blobs: c.blobs,
    auth: c.auth,
    async *paths() {
      for (const col of await fs.listCollections()) yield* walkCol(col);
    },
    close: c.close,
  };
}

const cfg = loadConfig();
process.exit(await migrateCommand(cfg, arg, flag, firebaseSide));
