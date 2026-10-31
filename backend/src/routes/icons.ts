/** Icons library (CLOUD_API listIcons / uploadIcon / deleteIcon / getIconContent). Storage: see core/icons.ts. */
import { ICON_MAX_BYTES, type Account, type IconDoc, type IconListItem } from '@opm/shared';
import { col, db, nowIso, pub } from '../core/fire.js';
import { E } from '../core/errors.js';
import { Reply } from '../core/reply.js';
import { iconUsage, isIconContentType, sanitizeSvg, sniffIcon, SvgError } from '../core/icons.js';
import { sha256Of } from '../core/util.js';
import type { Ctx, Handlers } from '../core/router.js';

/**
 * "any write permission": a rule with write / publish / admin on any resource and any scope, or being a manager of a
 * (non-deleted) deployment — everyone who can edit something that shows an icon.
 */
async function needAnyWrite(c: Ctx) {
  const a = c.account as Account;
  if (!a.disabled && a.access.some((r) => r.actions.some((x) => x === 'write' || x === 'publish' || x === 'admin'))) return;
  const q = await col.deployments().where('managerAccountUids', 'array-contains', a.uid).limit(5).get();
  if (q.docs.some((d) => !d.data().deleted)) return;
  throw E.forbidden('needs a write permission on any resource');
}
const iconDoc = (d: Record<string, unknown>): IconDoc => pub<IconDoc>(d);

export const iconHandlers: Pick<Handlers, 'listIcons' | 'uploadIcon' | 'deleteIcon' | 'getIconContent'> = {
  async listIcons() {
    const [q, usage] = await Promise.all([col.icons().select('iconId', 'name', 'contentType', 'sizeBytes', 'sha256', 'createdBy', 'createdAt').get(), iconUsage()]);
    const items: IconListItem[] = q.docs.map((d) => ({ ...iconDoc(d.data()), usedBy: usage.get(d.id)?.length ?? 0 })).sort((a, b) => b.createdAt.localeCompare(a.createdAt) || b.iconId.localeCompare(a.iconId));
    return { items };
  },
  async uploadIcon(c) {
    await needAnyWrite(c);
    const ct = (c.headers['content-type'] ?? '').split(';')[0]!.trim().toLowerCase();
    if (!isIconContentType(ct)) throw E.bad('contentType', 'V_ENUM', 'Content-Type must be image/png, image/svg+xml or image/webp');
    if (c.body.length > ICON_MAX_BYTES) throw E.tooLarge(`the icon is ${c.body.length} bytes (max ${ICON_MAX_BYTES})`);
    if (!c.body.length) throw E.bad('body', 'V_REQUIRED', 'the body is empty');
    const sniffed = sniffIcon(c.body);
    if (!sniffed) throw E.bad('body', 'V_FORMAT', 'the file is not a PNG, SVG or WebP image');
    if (sniffed !== ct) throw E.bad('contentType', 'V_FORMAT', `the file is ${sniffed}, but Content-Type says ${ct}`);
    let bytes = c.body;
    if (sniffed === 'image/svg+xml') {
      try {
        bytes = Buffer.from(sanitizeSvg(c.body.toString('utf8')), 'utf8');
      } catch (e) {
        if (e instanceof SvgError) throw E.bad('body', 'V_FORMAT', `not a valid SVG: ${e.message}`);
        throw e;
      }
      if (!bytes.length || bytes.length > ICON_MAX_BYTES) throw E.bad('body', 'V_FORMAT', 'the sanitized SVG is empty or too large');
    }
    const rawName = c.query.get('name');
    let name = (rawName ?? 'icon').trim();
    if (rawName !== null && (!name || name.length > 80)) throw E.bad('name', 'V_LENGTH', 'name must be 1–80 characters');
    if (!name) name = 'icon';
    const sha256 = sha256Of(bytes);
    const ref = col.icons().doc();
    const out = await db().runTransaction(async (tx) => {
      const dup = await tx.get(col.icons().where('sha256', '==', sha256).limit(1));
      if (!dup.empty) return { icon: iconDoc(dup.docs[0]!.data()), existing: true };
      const icon: IconDoc = { iconId: ref.id, name, contentType: sniffed, sizeBytes: bytes.length, sha256, createdBy: c.account!.uid, createdAt: nowIso() };
      tx.create(ref, { ...icon, _bytes: bytes });
      return { icon, existing: false };
    });
    c.audit = { ...(c.audit ?? {}), target: `icons/${out.icon.iconId}`, data: { existing: out.existing, name: out.icon.name, contentType: out.icon.contentType, sizeBytes: out.icon.sizeBytes, sha256: out.icon.sha256 } };
    return out;
  },
  async deleteIcon(c) {
    await needAnyWrite(c);
    const id = c.params.iconId!;
    const ref = col.icons().doc(id);
    if (!(await ref.get()).exists) throw E.notFound('icon not found');
    const users = (await iconUsage()).get(id) ?? [];
    if (users.length) throw E.conflict(`the icon is used by ${users.length} ${users.length === 1 ? 'entity' : 'entities'}`, { users });
    await ref.delete();
    return { deleted: true };
  },
  async getIconContent(c) {
    const s = await col.icons().doc(c.params.iconId!).get();
    if (!s.exists) throw E.notFound('icon not found');
    const d = s.data() as IconDoc & { _bytes?: Uint8Array };
    const bytes = Buffer.from(d._bytes ?? new Uint8Array());
    const headers: Record<string, string> = {
      'Content-Type': d.contentType,
      'Content-Length': String(bytes.length),
      'Cache-Control': 'public, max-age=31536000, immutable',
      'X-Content-Type-Options': 'nosniff',
      ETag: `"${d.sha256.slice(7)}"`,
    };
    if (d.contentType === 'image/svg+xml') headers['Content-Security-Policy'] = "default-src 'none'; style-src 'unsafe-inline'; sandbox";
    return new Reply(200, headers, bytes);
  },
};
