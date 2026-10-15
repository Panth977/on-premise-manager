/** Accounts + root audit log (SPEC-final §5.1, §2.2 Account rules). */
import type { AuthUser as UserRecord } from '../ports/index.js';
import {
  AUDIT_TARGET_TYPES,
  auditActionMatches,
  isAdmin,
  RE,
  REVEAL_SECONDS,
  validateAccessRules,
  type AccessRule,
  type Account,
  type AccountDoc,
  type AuditLogEntry,
  type CreateAccountRequest,
  type CreateAccountResponse,
  type PatchAccountRequest,
  type ResetLinkResponse,
  type SetAccountPasswordResponse,
} from '@opm/shared';
import { auth, col, isoPlus, nowIso } from '../core/fire.js';
import { E } from '../core/errors.js';
import { has, need } from '../core/perm.js';
import { decodeAudit } from '../core/audit.js';
import { asObject, generateSecret, onlyKeys, V, vLabel } from '../core/util.js';
import { accountView } from '../core/views.js';
import { vIconFormat, vIconRefs } from '../core/icons.js';
import { env } from '../core/env.js';
import type { Ctx, Handlers } from '../core/router.js';
import { logger } from '../ports/index.js';
import type { DocSnap, Query, WhereOp } from '../ports/index.js';

export async function sendMail(to: string, subject: string, text: string) {
  if (!env.smtpUrl) return;
  try {
    const nm = await import('nodemailer');
    const tr = nm.createTransport(env.smtpUrl);
    await tr.sendMail({ from: env.smtpFrom, to, subject, text });
  } catch (e) {
    logger.warn('password mail failed', { to, error: (e as Error).message });
  }
}
async function existingRefs() {
  const [tags, deps] = await Promise.all([col.tags().select().get(), col.deployments().select().get()]);
  return { tags: tags.docs.map((d) => d.id), deps: deps.docs.map((d) => d.id) };
}
function profileChecks(v: V, b: Record<string, unknown>, create: boolean) {
  if (create || b.name !== undefined) vLabel(v, b.name, 'name');
  if (b.phone !== undefined && b.phone !== null && (typeof b.phone !== 'string' || !RE.e164.test(b.phone))) v.err('phone', 'V_FORMAT', 'phone must be E.164 (e.g. +919876543210) or null');
  vIconFormat(v, b.img, 'img', true);
}
async function adminCountExcept(uid: string): Promise<number> {
  const q = await col.accounts().get();
  return q.docs.map((d) => accountView(d.data())).filter((a) => a.uid !== uid && !a.disabled && isAdmin(a)).length;
}

// ───────────────────────────────────────── stored credentials (§21 #84)
/** Firebase oob reset links live 1 h. */
export const RESET_LINK_TTL_MS = 3600_000;
export const ACCOUNT_PASSWORD_LENGTH = 20;
type StoredPw = NonNullable<AccountDoc['lastSetPassword']>;
type StoredLink = NonNullable<AccountDoc['lastResetLink']>;
/** accounts/{uid} as stored (pre-round-G docs lack the credential fields → null). */
function asDoc(d: Record<string, unknown>): AccountDoc {
  return { ...(d as unknown as AccountDoc), lastSetPassword: (d.lastSetPassword as StoredPw | undefined) ?? null, lastResetLink: (d.lastResetLink as StoredLink | undefined) ?? null };
}
async function loadAccountDoc(uid: string): Promise<AccountDoc> {
  const s = await col.accounts().doc(uid).get();
  if (!s.exists) throw E.notFound('account not found');
  return asDoc(s.data()!);
}
const isoOrNull = (t: string | undefined | null) => (t && Number.isFinite(Date.parse(t)) ? new Date(t).toISOString() : null);
/** Auth tokensValidAfterTime (ISO) — moves when the person changes the password, uses a reset link or tokens are revoked. */
async function validAfterOf(uid: string): Promise<string | null> {
  return isoOrNull((await auth().getUser(uid)).tokensValidAfterTime);
}
const moved = (stored: string | null, current: string | null) => !!current && (!stored || Date.parse(current) > Date.parse(stored));
type ClearReason = 'changed' | 'expired' | 'no-user';
interface CredCheck {
  pw: ClearReason | null;
  link: ClearReason | null;
}
function checkCreds(d: AccountDoc, user: UserRecord | undefined, now = Date.now()): CredCheck {
  const current = isoOrNull(user?.tokensValidAfterTime);
  const pw = d.lastSetPassword ? (!user ? 'no-user' : moved(d.lastSetPassword.validAfter, current) ? 'changed' : null) : null;
  const link = d.lastResetLink ? (!user ? 'no-user' : moved(d.lastResetLink.validAfter, current) ? 'changed' : Date.parse(d.lastResetLink.expiresAt) <= now ? 'expired' : null) : null;
  return { pw, link };
}
/**
 * The clearing rule (§21 #84 d): a stored password / reset link whose Auth tokensValidAfterTime moved since it was
 * stored (the person changed the password, used the link, tokens revoked), an expired link, or a missing Auth user →
 * set to null (persisted). Batched getUsers (≤ 100 per call). Returns the docs (mutated) + why each was cleared.
 */
async function refreshCreds(docs: AccountDoc[]): Promise<Map<string, CredCheck>> {
  const out = new Map<string, CredCheck>();
  const cand = docs.filter((d) => d.lastSetPassword || d.lastResetLink);
  for (let i = 0; i < cand.length; i += 100) {
    const chunk = cand.slice(i, i + 100);
    const r = await auth().getUsers(chunk.map((d) => ({ uid: d.uid })));
    const users = new Map(r.users.map((u) => [u.uid, u]));
    for (const d of chunk) {
      const chk = checkCreds(d, users.get(d.uid));
      out.set(d.uid, chk);
      const upd: Record<string, null> = {};
      if (chk.pw) upd.lastSetPassword = d.lastSetPassword = null;
      if (chk.link) upd.lastResetLink = d.lastResetLink = null;
      if (Object.keys(upd).length) await col.accounts().doc(d.uid).update(upd).catch((e) => logger.warn('account credential clear failed', { uid: d.uid, error: (e as Error).message }));
    }
  }
  return out;
}
const newPassword = () => generateSecret('alnum', ACCOUNT_PASSWORD_LENGTH);
function resetLinkResponse(l: StoredLink): ResetLinkResponse {
  return { value: l.value, passwordSetLink: l.value, hideAfterSec: REVEAL_SECONDS, createdAt: l.setAt, createdBy: l.setBy, expiresAt: l.expiresAt };
}

export const accountHandlers: Pick<
  Handlers,
  'listAccounts' | 'createAccount' | 'getAccount' | 'patchAccount' | 'disableAccount' | 'resetAccountPassword' | 'revealAccountResetLink' | 'setAccountPassword' | 'revealAccountPassword' | 'listAuditLog'
> = {
  async listAccounts(c) {
    need(c, 'accounts', 'read');
    const q = await col.accounts().get();
    const docs = q.docs.map((d) => asDoc(d.data()));
    await refreshCreds(docs);
    return { items: docs.map(accountView).sort((a, b) => a.name.localeCompare(b.name)) };
  },
  async createAccount(c) {
    need(c, 'accounts', 'admin');
    const b = asObject(c.json());
    onlyKeys(b, ['name', 'email', 'phone', 'img', 'access']);
    const v = new V();
    profileChecks(v, b, true);
    if (v.ok) await vIconRefs(v, [{ path: 'img', value: b.img }]);
    const email = typeof b.email === 'string' ? b.email.trim().toLowerCase() : '';
    if (!RE.email.test(email) || email.length > 254) v.err('email', 'V_FORMAT', 'email is not valid');
    v.throwIf();
    const r = b as unknown as CreateAccountRequest;
    const access: AccessRule[] = r.access ?? [];
    c.audit = { data: { email, rules: access.length, passwordSet: false } };
    if (access.length) {
      const refs = await existingRefs();
      const issues = validateAccessRules(access, refs.tags, refs.deps);
      if (issues.length) throw E.invalid(issues);
    }
    // §21 #84: a generated password, stored (revealAccountPassword) until the person changes it — no set-password link
    const password = newPassword();
    let user: UserRecord;
    try {
      user = await auth().createUser({ email, password, displayName: r.name.trim(), emailVerified: false });
    } catch (e) {
      const code = (e as { code?: string }).code;
      if (code === 'auth/email-already-exists') throw E.conflict(`an account with ${email} exists`, { field: 'email' });
      if (code === 'auth/invalid-email') throw E.bad('email', 'V_FORMAT', 'email is not valid');
      throw e;
    }
    const now = nowIso();
    const validAfter = await validAfterOf(user.uid);
    const doc: AccountDoc = {
      uid: user.uid,
      img: r.img ?? null,
      name: r.name.trim(),
      email,
      phone: r.phone ?? null,
      access,
      disabled: false,
      createdAt: now,
      createdBy: c.account!.uid,
      lastLoginAt: null,
      lastSetPassword: { value: password, setAt: now, setBy: c.account!.uid, validAfter },
      lastResetLink: null,
    };
    await col.accounts().doc(user.uid).create(doc);
    c.audit = { data: { email, rules: access.length, passwordSet: true } };
    const res: CreateAccountResponse = { account: accountView(doc), value: password, hideAfterSec: REVEAL_SECONDS };
    return res;
  },
  async getAccount(c) {
    const uid = c.params.uid!;
    if (uid !== c.account!.uid) need(c, 'accounts', 'read');
    const doc = await loadAccountDoc(uid);
    await refreshCreds([doc]);
    return accountView(doc);
  },
  async patchAccount(c) {
    const uid = c.params.uid!;
    const b = asObject(c.json());
    onlyKeys(b, ['name', 'phone', 'img', 'access'], ['uid', 'email', 'disabled', 'createdAt', 'createdBy', 'lastLoginAt', 'lastSetPassword', 'lastResetLink']);
    const p = b as PatchAccountRequest;
    // audit action: access-changed when the request changes access rules, else updated (refined below)
    c.audit = { action: p.access !== undefined ? 'account.access-changed' : 'account.updated' };
    const own = uid === c.account!.uid;
    if (p.access !== undefined) need(c, 'accounts', 'admin');
    if (!own && (p.name !== undefined || p.phone !== undefined || p.img !== undefined)) need(c, 'accounts', 'write');
    const v = new V();
    profileChecks(v, b, false);
    if (v.ok) await vIconRefs(v, [{ path: 'img', value: b.img }]);
    v.throwIf();
    if (p.access !== undefined) {
      const refs = await existingRefs();
      const issues = validateAccessRules(p.access, refs.tags, refs.deps);
      if (issues.length) throw E.invalid(issues);
    }
    const cur = accountView(await loadAccountDoc(uid));
    const next: Account = { ...cur };
    const upd: Record<string, unknown> = {};
    if (p.name !== undefined) upd.name = next.name = p.name.trim();
    if (p.phone !== undefined) upd.phone = next.phone = p.phone;
    if (p.img !== undefined) upd.img = next.img = p.img;
    if (p.access !== undefined) upd.access = next.access = p.access;
    if (p.access !== undefined && isAdmin(cur) && !isAdmin(next) && !cur.disabled && (await adminCountExcept(uid)) === 0) throw E.conflict('the last account with accounts.admin cannot lose it');
    if (p.access !== undefined && JSON.stringify(cur.access) === JSON.stringify(next.access)) c.audit = { action: 'account.updated' };
    // field update only: the stored credentials (lastSetPassword / lastResetLink values) stay untouched
    if (Object.keys(upd).length) await col.accounts().doc(uid).update(upd);
    if (p.name !== undefined) await auth().updateUser(uid, { displayName: next.name }).catch(() => undefined);
    return next;
  },
  async disableAccount(c) {
    need(c, 'accounts', 'admin');
    const uid = c.params.uid!;
    const b = asObject(c.json());
    onlyKeys(b, ['disabled']);
    if (b.disabled === false) c.audit = { action: 'account.enabled' };
    if (typeof b.disabled !== 'boolean') throw E.bad('disabled', 'V_TYPE', 'disabled must be true / false');
    const cur = accountView(await loadAccountDoc(uid));
    if (b.disabled && uid === c.account!.uid) throw E.conflict('you cannot disable yourself');
    if (b.disabled && !cur.disabled && isAdmin(cur) && (await adminCountExcept(uid)) === 0) throw E.conflict('the last account with accounts.admin cannot be disabled');
    const next: Account = { ...cur, disabled: b.disabled };
    await col.accounts().doc(uid).update({ disabled: b.disabled });
    await auth().updateUser(uid, { disabled: b.disabled as boolean }).catch((e) => {
      if ((e as { code?: string }).code !== 'auth/user-not-found') throw e;
    });
    if (b.disabled) await auth().revokeRefreshTokens(uid).catch(() => undefined);
    return next;
  },
  /** "Set new password…" (§21 #84): generated 20-char alnum password via the Admin SDK; the person's sessions end. */
  async setAccountPassword(c) {
    need(c, 'accounts', 'admin');
    const uid = c.params.uid!;
    const acc = await loadAccountDoc(uid);
    c.audit = { data: { email: acc.email } };
    const password = newPassword();
    try {
      await auth().updateUser(uid, { password });
      await auth().revokeRefreshTokens(uid);
    } catch (e) {
      if ((e as { code?: string }).code === 'auth/user-not-found') throw E.notFound('the sign-in user of this account does not exist');
      throw e;
    }
    // validAfter is read AFTER the change: only a later move (the person changes it) clears the stored value
    const validAfter = await validAfterOf(uid);
    const pw: StoredPw = { value: password, setAt: nowIso(), setBy: c.account!.uid, validAfter };
    await col.accounts().doc(uid).update({ lastSetPassword: pw });
    const res: SetAccountPasswordResponse = { value: password, hideAfterSec: REVEAL_SECONDS, setAt: pw.setAt, setBy: pw.setBy };
    return res;
  },
  async revealAccountPassword(c) {
    need(c, 'accounts', 'admin');
    const uid = c.params.uid!;
    const acc = await loadAccountDoc(uid);
    c.audit = { data: { email: acc.email } };
    const why = (await refreshCreds([acc])).get(uid)?.pw ?? null;
    if (!acc.lastSetPassword) {
      if (why === 'changed') throw E.gone('the person changed their password since it was set — set a new password to view one', { reason: 'changed' });
      throw E.gone('no password is stored for this account — set a new password to view one', { reason: why === 'no-user' ? 'no-user' : 'not-stored' });
    }
    const pw = acc.lastSetPassword;
    const res: SetAccountPasswordResponse = { value: pw.value, hideAfterSec: REVEAL_SECONDS, setAt: pw.setAt, setBy: pw.setBy };
    return res;
  },
  /** New password-reset link any time (§21 #84): stored 1 h as lastResetLink (revealAccountResetLink). */
  async resetAccountPassword(c) {
    need(c, 'accounts', 'admin');
    const uid = c.params.uid!;
    const acc = await loadAccountDoc(uid);
    c.audit = { data: { email: acc.email, mailed: !!env.smtpUrl } };
    let link: string;
    try {
      link = await auth().generatePasswordResetLink(acc.email);
    } catch (e) {
      if ((e as { code?: string }).code === 'auth/user-not-found' || (e as { code?: string }).code === 'auth/email-not-found') throw E.notFound('the sign-in user of this account does not exist');
      throw e;
    }
    const now = nowIso();
    const stored: StoredLink = { value: link, setAt: now, setBy: c.account!.uid, expiresAt: isoPlus(RESET_LINK_TTL_MS), validAfter: await validAfterOf(uid) };
    await col.accounts().doc(uid).update({ lastResetLink: stored });
    // decision (round G): the reset link is still e-mailed when OPM_SMTP_URL is configured (account creation mails nothing)
    await sendMail(acc.email, 'Reset your On-Prem Manager password', `Set a new password here (valid for 1 hour):\n${link}\n`);
    return resetLinkResponse(stored);
  },
  async revealAccountResetLink(c) {
    need(c, 'accounts', 'admin');
    const uid = c.params.uid!;
    const acc = await loadAccountDoc(uid);
    c.audit = { data: { email: acc.email } };
    const why = (await refreshCreds([acc])).get(uid)?.link ?? null;
    if (!acc.lastResetLink) {
      if (why === 'changed') throw E.gone('the reset link was used (or the password changed) — create a new link', { reason: 'used' });
      if (why === 'expired') throw E.gone('the reset link expired — create a new link', { reason: 'expired' });
      throw E.gone('no reset link is stored for this account — create a new link', { reason: why === 'no-user' ? 'no-user' : 'not-stored' });
    }
    return resetLinkResponse(acc.lastResetLink);
  },
  async listAuditLog(c) {
    need(c, 'accounts', 'admin');
    const q0 = c.query;
    const limit = clampLimit(c, 100, 500);
    const action = q0.get('action') || null;
    const actor = q0.get('actor') || null;
    const targetType = q0.get('targetType') || null;
    const target = q0.get('target') || null;
    const outcome = q0.get('outcome') || null;
    const from = q0.get('from') || null;
    const to = q0.get('to') || null;
    const errs: string[] = [];
    if (action && !/^[a-z]+(-[a-z]+)*(\.[a-z-]+)*\.?$/.test(action)) errs.push('action must be an audit action or a prefix ending in "."');
    if (targetType && !(AUDIT_TARGET_TYPES as readonly string[]).includes(targetType)) errs.push(`targetType must be one of ${AUDIT_TARGET_TYPES.join(' | ')}`);
    if (outcome && outcome !== 'ok' && outcome !== 'error') errs.push('outcome must be ok | error');
    if (from && !Number.isFinite(Date.parse(from))) errs.push('from must be an ISO time');
    if (to && !Number.isFinite(Date.parse(to))) errs.push('to must be an ISO time');
    if (errs.length) throw E.invalid(errs.map((m) => ({ path: 'query', code: 'V_FORMAT', message: m, severity: 'error' as const })));
    // ONE equality filter runs in Firestore (indexes: firestore.indexes.json), the others in memory (over-fetch)
    let q: Query = col.auditLog();
    const mem: ((e: AuditLogEntry) => boolean)[] = [];
    let used = false;
    const use = (field: string, val: unknown, op: WhereOp, pred: (e: AuditLogEntry) => boolean) => {
      if (!used) {
        q = q.where(field, op, val);
        used = true;
      } else mem.push(pred);
    };
    const actorField = actor && actor.includes('@') ? 'actorEmail' : 'actor';
    const actorVal = actor && actor.includes('@') ? actor.toLowerCase() : actor;
    if (target) use('target', target, '==', (e) => e.target === target);
    if (actor) use(actorField, actorVal, '==', (e) => (actorField === 'actorEmail' ? e.actorEmail === actorVal : e.actor === actorVal));
    if (action) use('_actionKeys', action, 'array-contains', (e) => auditActionMatches(e.action, action));
    if (targetType) use('targetType', targetType, '==', (e) => e.targetType === targetType);
    if (outcome) use('outcome', outcome, '==', (e) => e.outcome === outcome);
    if (from) q = q.where('ts', '>=', new Date(from).toISOString());
    if (to) q = q.where('ts', '<=', new Date(to).toISOString());
    q = q.orderBy('ts', 'desc');
    const cursor = q0.get('cursor');
    let after: DocSnap | null = null;
    if (cursor) {
      after = await col.auditLog().doc(cursor).get();
      if (!after.exists) throw E.bad('cursor', 'V_FORMAT', 'bad cursor');
    }
    const items: AuditLogEntry[] = [];
    let exhausted = false;
    const pageSize = mem.length ? 500 : limit + 1;
    for (let round = 0; round < 20 && items.length <= limit && !exhausted; round++) {
      const page = await (after ? q.startAfter(after) : q).limit(pageSize).get();
      if (page.docs.length < pageSize) exhausted = true;
      for (const d of page.docs) {
        after = d;
        const e = decodeAudit(d.data());
        if (mem.every((f) => f(e))) items.push(e);
        if (items.length > limit) break;
      }
    }
    const shown = items.slice(0, limit);
    return { items: shown, cursor: items.length > limit ? shown.at(-1)!.auditId : null };
  },
};

export function clampLimit(c: Ctx, def: number, max: number): number {
  const s = c.query.get('limit');
  if (s === null || s === '') return def;
  const n = Number(s);
  if (!Number.isInteger(n) || n < 1) throw E.bad('limit', 'V_RANGE', `limit must be 1–${max}`);
  return Math.min(n, max);
}
export { has };
