/** Firebase Auth adapter (Admin SDK) — unchanged behaviour; the browser signs in with the Firebase JS SDK. */
import type { Auth, UserRecord } from 'firebase-admin/auth';
import type { AuthAdmin, AuthUser, CreateUserInput, DecodedIdToken, UpdateUserInput } from '../../ports/auth.js';

function user(u: UserRecord): AuthUser {
  const providers = u.providerData.map((p) => p.providerId);
  return {
    uid: u.uid,
    email: u.email,
    displayName: u.displayName,
    disabled: u.disabled,
    emailVerified: u.emailVerified,
    tokensValidAfterTime: u.tokensValidAfterTime,
    hasPassword: providers.includes('password') || !!u.passwordHash,
    providers,
  };
}

export class FirebaseAuthAdmin implements AuthAdmin {
  readonly kind = 'firebase' as const;
  constructor(private readonly authOf: () => Auth) {}
  get a(): Auth {
    return this.authOf();
  }
  async verifyIdToken(token: string, checkRevoked = false): Promise<DecodedIdToken> {
    const d = await this.a.verifyIdToken(token, checkRevoked);
    return { uid: d.uid, email: d.email, iat: d.iat, exp: d.exp };
  }
  async getUser(uid: string) {
    return user(await this.a.getUser(uid));
  }
  async getUserByEmail(email: string) {
    return user(await this.a.getUserByEmail(email));
  }
  async getUsers(ids: { uid: string }[]) {
    if (!ids.length) return { users: [] };
    const r = await this.a.getUsers(ids);
    return { users: r.users.map(user) };
  }
  async createUser(u: CreateUserInput) {
    return user(await this.a.createUser(u));
  }
  async updateUser(uid: string, u: UpdateUserInput) {
    return user(await this.a.updateUser(uid, u));
  }
  async deleteUser(uid: string) {
    await this.a.deleteUser(uid);
  }
  async revokeRefreshTokens(uid: string) {
    await this.a.revokeRefreshTokens(uid);
  }
  generatePasswordResetLink(email: string) {
    return this.a.generatePasswordResetLink(email);
  }
  async listAllUsers() {
    const out: AuthUser[] = [];
    let token: string | undefined;
    do {
      const r = await this.a.listUsers(1000, token);
      out.push(...r.users.map(user));
      token = r.pageToken;
    } while (token);
    return out;
  }
  /** raw records incl. password hashes (migration only) */
  async listAllRecords(): Promise<UserRecord[]> {
    const out: UserRecord[] = [];
    let token: string | undefined;
    do {
      const r = await this.a.listUsers(1000, token);
      out.push(...r.users);
      token = r.pageToken;
    } while (token);
    return out;
  }
}
