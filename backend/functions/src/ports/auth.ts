/**
 * Auth port (SPEC §21 #108) — human identity only (agents use Ed25519 signatures, install tokens are documents).
 *
 *   verifyIdToken  the Bearer token of a browser request → { uid, email } (checks revocation + disabled)
 *   user admin     what routes/accounts.ts + scripts need: create / get / update / revoke / reset link
 *
 * Adapters: `adapters/firebase` (Firebase Auth Admin SDK; the browser signs in with the Firebase JS SDK) ·
 * `adapters/local` (users in the global DB, argon2id passwords, HS256 ID tokens + rotating refresh tokens issued by
 * opm-cloud `/auth/*`, optional OIDC / Google sign-in).
 *
 * Errors carry Firebase-style codes so callers stay unchanged: auth/user-not-found, auth/email-already-exists,
 * auth/invalid-email, auth/user-disabled, auth/id-token-revoked, auth/id-token-expired, auth/argument-error,
 * auth/invalid-password.
 */
export interface AuthUser {
  uid: string;
  email: string | undefined;
  displayName: string | undefined;
  disabled: boolean;
  emailVerified: boolean;
  /** UTC date string; tokens issued before it are revoked (seconds precision, like Firebase) */
  tokensValidAfterTime: string | undefined;
  /** password sign-in is possible (local: a password hash is stored; Firebase: a password provider is linked) */
  hasPassword: boolean;
  /** provider ids, e.g. password, google.com */
  providers: string[];
}
export interface DecodedIdToken {
  uid: string;
  email?: string;
  iat: number;
  exp: number;
}
export interface CreateUserInput {
  uid?: string;
  email: string;
  password?: string;
  displayName?: string;
  emailVerified?: boolean;
  disabled?: boolean;
}
export interface UpdateUserInput {
  email?: string;
  password?: string;
  displayName?: string;
  disabled?: boolean;
  emailVerified?: boolean;
}

export interface AuthAdmin {
  readonly kind: 'firebase' | 'local';
  verifyIdToken(token: string, checkRevoked?: boolean): Promise<DecodedIdToken>;
  getUser(uid: string): Promise<AuthUser>;
  getUserByEmail(email: string): Promise<AuthUser>;
  /** missing uids are left out (like Firebase getUsers().users) */
  getUsers(ids: { uid: string }[]): Promise<{ users: AuthUser[] }>;
  createUser(u: CreateUserInput): Promise<AuthUser>;
  updateUser(uid: string, u: UpdateUserInput): Promise<AuthUser>;
  deleteUser(uid: string): Promise<void>;
  revokeRefreshTokens(uid: string): Promise<void>;
  /** a one-time link that lets the person choose a new password (Firebase action URL / opm-cloud /reset-password) */
  generatePasswordResetLink(email: string): Promise<string>;
  /** every user (migration / backups), paged internally */
  listAllUsers(): Promise<AuthUser[]>;
}

export class AuthError extends Error {
  constructor(
    readonly code: string,
    message: string,
  ) {
    super(message);
  }
}
