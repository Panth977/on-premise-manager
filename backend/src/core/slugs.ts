/**
 * §21 #106 entity slugs (Template.slug / Deployment.slug): the stable name of the GitHub mirror branches.
 * Assigned at create (the requested slug, else uniqueSlug(slugifyLabel(label))), immutable. Unique per collection
 * (deleted deployments included). Documents created before #106 get one from migrate-k — or lazily from ensureSlug()
 * the first time the mirror needs it (stored at once, never changed afterwards).
 */
import { RE, slugifyLabel, uniqueSlug } from '@opm/shared';
import { col, db, type Tx } from './fire.js';
import { E } from './errors.js';

export type SlugKind = 'template' | 'deployment';
const coll = (k: SlugKind) => (k === 'template' ? col.templates() : col.deployments());
const prefix = (k: SlugKind) => (k === 'template' ? 't-' : 'd-');

/** every slug taken in the collection (small collections: templates / deployments are tens, not thousands) */
export async function takenSlugs(kind: SlugKind, tx?: Tx): Promise<Set<string>> {
  const q = coll(kind).select('slug');
  const s = tx ? await tx.get(q) : await q.get();
  return new Set(s.docs.map((d) => d.data().slug).filter((x): x is string => typeof x === 'string' && !!x));
}

/**
 * The slug for a new document, inside the create transaction (reads first). requested → validated + 409 conflict
 * reason slug-taken when used; else derived from the label.
 */
export async function newSlug(tx: Tx, kind: SlugKind, label: string, requested?: unknown): Promise<string> {
  const taken = await takenSlugs(kind, tx);
  if (requested !== undefined && requested !== null) {
    if (typeof requested !== 'string' || !RE.slug.test(requested)) throw E.bad('slug', 'V_FORMAT', 'slug must match ^[a-z][a-z0-9-]{1,39}$');
    if (taken.has(requested)) throw E.conflict(`slug ${requested} is already used by another ${kind}`, { reason: 'slug-taken', slug: requested });
    return requested;
  }
  return uniqueSlug(slugifyLabel(label, prefix(kind)), taken);
}

/** Validate a requested slug before the transaction (format only). */
export function checkRequestedSlug(v: { err(path: string, code: string, message: string): void }, requested: unknown) {
  if (requested === undefined) return;
  if (typeof requested !== 'string' || !RE.slug.test(requested)) v.err('slug', 'V_FORMAT', 'slug must match ^[a-z][a-z0-9-]{1,39}$');
}

/** The stored slug of a document; a legacy document without one gets one now (stored, then immutable). */
export async function ensureSlug(kind: SlugKind, id: string): Promise<string> {
  const ref = coll(kind).doc(id);
  const s0 = await ref.get();
  if (!s0.exists) throw E.notFound(`${kind} not found`);
  const cur = s0.data()!.slug;
  if (typeof cur === 'string' && RE.slug.test(cur)) return cur;
  return db().runTransaction(async (tx) => {
    const s = await tx.get(ref);
    const again = s.data()?.slug;
    if (typeof again === 'string' && RE.slug.test(again)) return again;
    const slug = uniqueSlug(slugifyLabel(String(s.data()?.label ?? ''), prefix(kind)), await takenSlugs(kind, tx));
    tx.update(ref, { slug });
    return slug;
  });
}

/** Read-side fallback for documents not migrated yet (never stored here). */
export const slugOf = (d: { slug?: unknown; label?: unknown }, kind: SlugKind): string =>
  typeof d.slug === 'string' && d.slug ? d.slug : slugifyLabel(String(d.label ?? ''), prefix(kind));
