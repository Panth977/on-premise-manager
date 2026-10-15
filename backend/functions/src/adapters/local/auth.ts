/**
 * Local Auth (AuthAdmin): users in the global DB through the DocStore, argon2id passwords (hash-wasm), HS256 ID tokens
 * (1 h), rotating opaque refresh tokens (30 d, reuse of a rotated token revokes the family), one-time reset tokens.
 *
 *   authUsers/{uid}        { uid, email, displayName?, disabled, emailVerified, passwordHash, validSinceSec, providers[],
 *                            federated{provider: sub}, createdAt }
 *   authEmails/{sha256(email)}  { uid }  — e-mail uniqueness (claimed in the same transaction as the user)
 *   authRefresh/{sha256(token)} { uid, family, createdSec, expiresAt, rotatedAt? }
 *   authReset/{sha256(token)}   { uid, email, expiresAt, usedAt }
 */
import { createHash, createHmac, randomBytes, randomInt, timingSafeEqual } from 'node:crypto';
import { argon2Verify, argon2id } from 'hash-wasm';
import { AuthError, type AuthAdmin, type AuthUser, type CreateUserInput, type DecodedIdToken, type DocData, type UpdateUserInput } from '../../ports/index.js';
import type { LocalDocStore } from './docstore.js';
import { loadSecret } from './blobstore.js';

export const ID_TOKEN_TTL_SEC = 3600;
export const REFRESH_TTL_MS = 30 * 86_400_000;
export const RESET_TTL_MS = 3_600_000;
const ISS = 'opm-local';
const AUD = 'opm';
const ALNUM = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';

export const sha256 = (s: string) => createHash('sha256').update(s).digest('hex');
export const randomToken = () => randomBytes(32).toString('base64url');
const newUid = () => Array.from({ length: 28 }, () => ALNUM[randomInt(ALNUM.length)]).join('');
const nowSec = () => Math.floor(Date.now() / 1000);
const b64 = (o: unknown) => Buffer.from(JSON.stringify(o)).toString('base64url');

export interface UserDoc {
  uid: string;
  email: string;
  displayName?: string;
  disabled: boolean;
  emailVerified: boolean;
  passwordHash: string | null;
  validSinceSec: number;
  providers: string[];
  federated: Record<string, string>;
  createdAt: string;
}
export interface TokenPair {
  idToken: string;
  refreshToken: string;
  expiresIn: number;
  uid: string;
  email: string;
}

export function normEmail(e: unknown): string {
  if (typeof e !== 'string') throw new AuthError('auth/invalid-email', 'The email address is improperly formatted.');
  const v = e.trim().toLowerCase();
  if (!/^[^\s@]+@[^\s@]+$/.test(v) || v.length > 320) throw new AuthError('auth/invalid-email', 'The email address is improperly formatted.');
  return v;
}
function toUser(d: UserDoc): AuthUser {
  return {
    uid: d.uid,
    email: d.email,
    displayName: d.displayName ?? undefined,
    disabled: !!d.disabled,
    emailVerified: !!d.emailVerified,
    tokensValidAfterTime: new Date(d.validSinceSec * 1000).toUTCString(),
    hasPassword: !!d.passwordHash,
    providers: [...(d.providers ?? [])],
  };
}
export async function hashPassword(pw: string): Promise<string> {
  return argon2id({ password: pw, salt: randomBytes(16), parallelism: 1, iterations: 2, memorySize: 19456, hashLength: 32, outputType: 'encoded' });
}
const DUMMY = '$argon2id$v=19$m=19456,t=2,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA';

export class LocalAuth implements AuthAdmin {
  readonly kind = 'local' as const;
  private key: Buffer | null = null;
  constructor(
    readonly store: LocalDocStore,
    private readonly dataDir: string,
    readonly consoleUrl: string,
  ) {}
  private secret(): Buffer {
    return (this.key ??= loadSecret('OPM_AUTH_SECRET', this.dataDir, 'auth-jwt.key'));
  }
  private users = () => this.store.collection('authUsers');
  private emailRef = (email: string) => this.store.doc(`authEmails/${sha256(email)}`);
  private async userDoc(uid: string): Promise<UserDoc | null> {
    if (typeof uid !== 'string' || !uid || uid.includes('/') || uid.length > 128) return null;
    const s = await this.users().doc(uid).get();
    return s.exists ? (s.data() as UserDoc) : null;
  }
  private async needUser(uid: string): Promise<UserDoc> {
    const d = await this.userDoc(uid);
    if (!d) throw new AuthError('auth/user-not-found', 'There is no user record corresponding to the provided identifier.');
    return d;
  }
  /** user document by e-mail (null when none) */
  async userByEmail(email: string): Promise<UserDoc | null> {
    const e = await this.emailRef(normEmail(email)).get();
    if (!e.exists) return null;
    return this.userDoc((e.data() as { uid: string }).uid);
  }

  // ─── tokens
  signIdToken(u: UserDoc): string {
    const iat = nowSec();
    const head = b64({ alg: 'HS256', typ: 'JWT' });
    const body = b64({ iss: ISS, aud: AUD, sub: u.uid, uid: u.uid, email: u.email, iat, exp: iat + ID_TOKEN_TTL_SEC });
    const sig = createHmac('sha256', this.secret()).update(`${head}.${body}`).digest('base64url');
    return `${head}.${body}.${sig}`;
  }
  async verifyIdToken(token: string, checkRevoked = false): Promise<DecodedIdToken> {
    const bad = () => new AuthError('auth/argument-error', 'Decoding ID token failed: invalid token.');
    if (typeof token !== 'string') throw bad();
    const parts = token.split('.');
    if (parts.length !== 3) throw bad();
    let head: { alg?: string };
    let p: { iss?: string; aud?: string; sub?: string; email?: string; iat?: number; exp?: number };
    try {
      head = JSON.parse(Buffer.from(parts[0]!, 'base64url').toString('utf8'));
      p = JSON.parse(Buffer.from(parts[1]!, 'base64url').toString('utf8'));
    } catch {
      throw bad();
    }
    if (head.alg !== 'HS256') throw bad();
    const want = createHmac('sha256', this.secret()).update(`${parts[0]}.${parts[1]}`).digest();
    const got = Buffer.from(parts[2]!, 'base64url');
    if (want.length !== got.length || !timingSafeEqual(want, got)) throw bad();
    if (p.iss !== ISS || p.aud !== AUD || typeof p.sub !== 'string' || typeof p.iat !== 'number' || typeof p.exp !== 'number') throw bad();
    if (p.exp <= nowSec()) throw new AuthError('auth/id-token-expired', 'Firebase ID token has expired.');
    const u = await this.needUser(p.sub);
    if (u.disabled) throw new AuthError('auth/user-disabled', 'The user record is disabled.');
    if (checkRevoked && p.iat < (u.validSinceSec ?? 0)) throw new AuthError('auth/id-token-revoked', 'The Firebase ID token has been revoked.');
    return { uid: p.sub, email: p.email, iat: p.iat, exp: p.exp };
  }
  /** a new session: ID token + refresh token of a new (or the given) family */
  async issue(u: UserDoc, family = randomToken()): Promise<TokenPair> {
    const rt = randomToken();
    await this.store.doc(`authRefresh/${sha256(rt)}`).set({ uid: u.uid, family, createdSec: nowSec(), expiresAt: new Date(Date.now() + REFRESH_TTL_MS).toISOString() });
    return { idToken: this.signIdToken(u), refreshToken: rt, expiresIn: ID_TOKEN_TTL_SEC, uid: u.uid, email: u.email };
  }
  /** rotate a refresh token; reuse of an already rotated token revokes its whole family */
  async refresh(token: string): Promise<TokenPair> {
    const invalid = () => new AuthError('auth/invalid-refresh-token', 'Invalid refresh token.');
    if (typeof token !== 'string' || !token) throw invalid();
    const ref = this.store.doc(`authRefresh/${sha256(token)}`);
    const next = randomToken();
    const r = await this.store.runTransaction(async (tx) => {
      const s = await tx.get(ref);
      if (!s.exists) return { err: 'invalid' as const };
      const d = s.data() as { uid: string; family: string; createdSec: number; expiresAt: string; rotatedAt?: string };
      if (d.rotatedAt) return { err: 'reuse' as const, family: d.family };
      if (d.expiresAt < new Date().toISOString()) return { err: 'invalid' as const };
      const us = await tx.get(this.users().doc(d.uid));
      if (!us.exists) return { err: 'invalid' as const };
      const u = us.data() as UserDoc;
      if (u.disabled) return { err: 'disabled' as const };
      if (d.createdSec < (u.validSinceSec ?? 0)) return { err: 'invalid' as const };
      tx.update(ref, { rotatedAt: new Date().toISOString() });
      tx.set(this.store.doc(`authRefresh/${sha256(next)}`), { uid: u.uid, family: d.family, createdSec: nowSec(), expiresAt: d.expiresAt });
      return { u };
    });
    if ('u' in r && r.u) return { idToken: this.signIdToken(r.u), refreshToken: next, expiresIn: ID_TOKEN_TTL_SEC, uid: r.u.uid, email: r.u.email };
    if (r.err === 'reuse') {
      await this.deleteWhere('family', r.family!);
      throw invalid();
    }
    if (r.err === 'disabled') throw new AuthError('auth/user-disabled', 'The user account has been disabled by an administrator.');
    throw invalid();
  }
  /** drop the family of a refresh token (logout) */
  async logout(token: string): Promise<void> {
    if (typeof token !== 'string' || !token) return;
    const s = await this.store.doc(`authRefresh/${sha256(token)}`).get();
    if (s.exists) await this.deleteWhere('family', (s.data() as { family: string }).family);
  }
  private async deleteWhere(field: 'uid' | 'family', v: string) {
    for (;;) {
      const q = await this.store.collection('authRefresh').where(field, '==', v).select().limit(400).get();
      if (q.empty) return;
      const b = this.store.batch();
      q.docs.forEach((d) => b.delete(d.ref));
      await b.commit();
    }
  }
  /** e-mail + password → the user (throws auth/invalid-credential | auth/user-disabled) */
  async checkPassword(email: string, password: string): Promise<UserDoc> {
    let u: UserDoc | null = null;
    try {
      u = await this.userByEmail(email);
    } catch {
      u = null;
    }
    const hash = u?.passwordHash ?? DUMMY;
    let ok = false;
    try {
      ok = typeof password === 'string' && (await argon2Verify({ password, hash }));
    } catch {
      ok = false;
    }
    if (!u || !u.passwordHash || !ok) throw new AuthError('auth/invalid-credential', 'The supplied auth credential is incorrect.');
    if (u.disabled) throw new AuthError('auth/user-disabled', 'The user account has been disabled by an administrator.');
    return u;
  }

  // ─── AuthAdmin
  async getUser(uid: string) {
    return toUser(await this.needUser(uid));
  }
  async getUserByEmail(email: string) {
    const u = await this.userByEmail(email);
    if (!u) throw new AuthError('auth/user-not-found', 'There is no user record corresponding to the provided identifier.');
    return toUser(u);
  }
  async getUsers(ids: { uid: string }[]) {
    const out: AuthUser[] = [];
    for (const { uid } of ids) {
      const d = await this.userDoc(uid);
      if (d) out.push(toUser(d));
    }
    return { users: out };
  }
  /** createUser + federated identities (OIDC sign-up) */
  async createUserDoc(u: CreateUserInput & { providers?: string[]; federated?: Record<string, string> }): Promise<UserDoc> {
    const email = normEmail(u.email);
    if (u.password !== undefined && (typeof u.password !== 'string' || u.password.length < 6)) throw new AuthError('auth/invalid-password', 'The password must be a string with at least 6 characters.');
    if (u.uid !== undefined && (typeof u.uid !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(u.uid))) throw new AuthError('auth/invalid-uid', 'The uid must be a non-empty string with at most 128 characters.');
    const passwordHash = u.password !== undefined ? await hashPassword(u.password) : null;
    const uid = u.uid ?? newUid();
    const doc: UserDoc = {
      uid,
      email,
      ...(u.displayName !== undefined ? { displayName: u.displayName } : {}),
      disabled: !!u.disabled,
      emailVerified: !!u.emailVerified,
      passwordHash,
      validSinceSec: nowSec(),
      providers: [...new Set([...(passwordHash ? ['password'] : []), ...(u.providers ?? [])])],
      federated: u.federated ?? {},
      createdAt: new Date().toISOString(),
    };
    await this.store.runTransaction(async (tx) => {
      const [us, es] = await tx.getAll(this.users().doc(uid), this.emailRef(email));
      if (us!.exists) throw new AuthError('auth/uid-already-exists', 'The user with the provided uid already exists.');
      if (es!.exists) throw new AuthError('auth/email-already-exists', 'The email address is already in use by another account.');
      tx.create(this.users().doc(uid), doc as unknown as DocData);
      tx.create(this.emailRef(email), { uid });
    });
    return doc;
  }
  async createUser(u: CreateUserInput) {
    return toUser(await this.createUserDoc(u));
  }
  async updateUser(uid: string, u: UpdateUserInput) {
    if (u.password !== undefined && (typeof u.password !== 'string' || u.password.length < 6)) throw new AuthError('auth/invalid-password', 'The password must be a string with at least 6 characters.');
    const email = u.email !== undefined ? normEmail(u.email) : undefined;
    const passwordHash = u.password !== undefined ? await hashPassword(u.password) : undefined;
    const out = await this.store.runTransaction(async (tx) => {
      const ref = this.users().doc(uid);
      const s = await tx.get(ref);
      if (!s.exists) throw new AuthError('auth/user-not-found', 'There is no user record corresponding to the provided identifier.');
      const d = s.data() as UserDoc;
      const changeEmail = email !== undefined && email !== d.email;
      if (changeEmail) {
        const es = await tx.get(this.emailRef(email));
        if (es.exists) throw new AuthError('auth/email-already-exists', 'The email address is already in use by another account.');
      }
      const next: UserDoc = { ...d };
      if (changeEmail) {
        next.email = email;
        tx.delete(this.emailRef(d.email));
        tx.create(this.emailRef(email), { uid });
      }
      if (u.displayName !== undefined) next.displayName = u.displayName;
      if (u.disabled !== undefined) next.disabled = !!u.disabled;
      if (u.emailVerified !== undefined) next.emailVerified = !!u.emailVerified;
      if (passwordHash !== undefined) {
        next.passwordHash = passwordHash;
        next.validSinceSec = nowSec();
        if (!next.providers.includes('password')) next.providers = ['password', ...next.providers];
      }
      tx.set(ref, next as unknown as DocData);
      return next;
    });
    if (passwordHash !== undefined) await this.deleteWhere('uid', uid);
    return toUser(out);
  }
  /** set a password from a reset token (marks it used, revokes sessions) */
  async confirmReset(token: string, password: string): Promise<'ok' | 'invalid' | 'expired'> {
    const ref = this.store.doc(`authReset/${sha256(token || '-')}`);
    const hash = await hashPassword(password);
    const r = await this.store.runTransaction(async (tx) => {
      const s = await tx.get(ref);
      if (!s.exists) return { state: 'invalid' as const };
      const d = s.data() as { uid: string; expiresAt: string; usedAt: string | null };
      if (d.usedAt) return { state: 'invalid' as const };
      if (d.expiresAt < new Date().toISOString()) return { state: 'expired' as const };
      const us = await tx.get(this.users().doc(d.uid));
      if (!us.exists) return { state: 'invalid' as const };
      const u = us.data() as UserDoc;
      tx.update(ref, { usedAt: new Date().toISOString() });
      tx.update(us.ref, { passwordHash: hash, validSinceSec: nowSec(), providers: u.providers.includes('password') ? u.providers : ['password', ...u.providers] });
      return { state: 'ok' as const, uid: d.uid };
    });
    if (r.state === 'ok') await this.deleteWhere('uid', r.uid);
    return r.state;
  }
  /** a reset token's e-mail, or why it cannot be used */
  async checkReset(token: string): Promise<{ email: string } | { error: 'invalid' | 'expired' }> {
    if (typeof token !== 'string' || !token) return { error: 'invalid' };
    const s = await this.store.doc(`authReset/${sha256(token)}`).get();
    if (!s.exists) return { error: 'invalid' };
    const r = s.data() as { email: string; expiresAt: string; usedAt: string | null };
    if (r.usedAt) return { error: 'invalid' };
    if (r.expiresAt < new Date().toISOString()) return { error: 'expired' };
    return { email: r.email };
  }
  async deleteUser(uid: string) {
    await this.store.runTransaction(async (tx) => {
      const ref = this.users().doc(uid);
      const s = await tx.get(ref);
      if (!s.exists) throw new AuthError('auth/user-not-found', 'There is no user record corresponding to the provided identifier.');
      tx.delete(ref);
      tx.delete(this.emailRef((s.data() as UserDoc).email));
    });
    await this.deleteWhere('uid', uid);
  }
  async revokeRefreshTokens(uid: string) {
    await this.needUser(uid);
    await this.users().doc(uid).update({ validSinceSec: nowSec() });
    await this.deleteWhere('uid', uid);
  }
  async generatePasswordResetLink(email: string) {
    const u = await this.userByEmail(email);
    if (!u) throw new AuthError('auth/user-not-found', 'There is no user record corresponding to the provided identifier.');
    const t = randomToken();
    await this.store.doc(`authReset/${sha256(t)}`).set({ uid: u.uid, email: u.email, expiresAt: new Date(Date.now() + RESET_TTL_MS).toISOString(), usedAt: null, createdAt: new Date().toISOString() });
    return `${this.consoleUrl}/reset-password?token=${t}`;
  }
  async listAllUsers() {
    return (await this.users().get()).docs.map((d) => toUser(d.data() as UserDoc));
  }
  /** raw user documents incl. password hashes (backups / migration) */
  async listAllDocs(): Promise<UserDoc[]> {
    return (await this.users().get()).docs.map((d) => d.data() as UserDoc);
  }
  /** link a federated identity to a user (OIDC) */
  async linkFederated(uid: string, provider: string, sub: string) {
    await this.store.runTransaction(async (tx) => {
      const ref = this.users().doc(uid);
      const s = await tx.get(ref);
      if (!s.exists) return;
      const d = s.data() as UserDoc;
      tx.update(ref, { [`federated.${provider.replace(/[^A-Za-z0-9_-]/g, '_')}`]: sub, providers: d.providers.includes(provider) ? d.providers : [...d.providers, provider] });
    });
  }
}
