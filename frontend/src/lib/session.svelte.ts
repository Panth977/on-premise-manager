/**
 * Human login + the OPM account (GET /v1/me). The provider is chosen at runtime (§21 #108, <base>/opm-config.json):
 * - firebase (default; Firebase Hosting): Firebase Auth email/password; Google only with VITE_OPM_GOOGLE_DOMAIN.
 * - local (opm-cloud on a VM): /auth/* of the same origin (lib/auth/local-auth.ts); optional OIDC + password reset.
 * In mock mode a fake auth keeps the uid in localStorage and sends "Bearer mock:<uid>" (no config fetch).
 */
import type { Account, MeResponse, Resource, Action } from '@opm/shared';
import { can, canSeeDeployment, isAdmin, type AccessTarget } from '@opm/shared';
import { api, errCode, errMsg, isApiError, setTokenProvider } from './api';
import { MOCK, USE_EMULATORS, AUTH_EMULATOR_URL, GOOGLE_DOMAIN, EMULATOR_PROJECT, setRuntimeApiBase } from './env';
import { firebaseConfig } from './firebase-config';
import type { Auth, User } from 'firebase/auth';
import { asset } from '$app/paths';
import { DEFAULT_RUNTIME_CONFIG, loadRuntimeConfig, type OpmAuthConfig } from './auth/runtime-config';
import { authMessage } from './auth/messages';
import { LocalAuth, checkPasswordReset, confirmPasswordReset, requestPasswordReset, type ResetCheck } from './auth/local-auth';

export type SessionStatus = 'loading' | 'signed-out' | 'signed-in';
export type SessionProvider = 'mock' | 'firebase' | 'local';

const MOCK_KEY = 'opm.mock.session';

class Session {
  status: SessionStatus = $state('loading');
  me: MeResponse | null = $state(null);
  /** error to show on the login page (no account, disabled, …) */
  loginError: string | null = $state(null);
  /** §21 #108 which login this console uses (known after init) */
  provider: SessionProvider = $state(MOCK ? 'mock' : 'firebase');
  authConfig: OpmAuthConfig = $state({ ...DEFAULT_RUNTIME_CONFIG.auth });
  /** opm-config.json was read (the login page waits for it before showing provider-specific options) */
  configReady: boolean = $state(MOCK);
  #auth: Auth | null = null;
  #user: User | null = null;
  #local: LocalAuth | null = null;
  #ready: Promise<void> | null = null;

  get account(): Account | null {
    return this.me?.account ?? null;
  }
  get isAdmin(): boolean {
    return isAdmin(this.account);
  }

  /** can() of the signed-in account */
  can(resource: Resource, action: Action, target: AccessTarget | null = null): boolean {
    return can(this.account, resource, action, target).allowed;
  }
  canSee(target: AccessTarget): boolean {
    return canSeeDeployment(this.account, target);
  }

  init(): Promise<void> {
    if (this.#ready) return this.#ready;
    this.#ready = MOCK ? this.#initMock() : this.#initRuntime();
    return this.#ready;
  }

  /** §21 #108 read <base>/opm-config.json, then start the provider it names (emulators: always Firebase). */
  async #initRuntime() {
    const cfg = await loadRuntimeConfig(asset('/opm-config.json'));
    if (cfg.apiBase) setRuntimeApiBase(cfg.apiBase);
    const local = cfg.auth.provider === 'local' && !USE_EMULATORS;
    this.authConfig = local ? cfg.auth : { ...DEFAULT_RUNTIME_CONFIG.auth };
    this.provider = local ? 'local' : 'firebase';
    this.configReady = true;
    if (local) await this.#initLocal();
    else await this.#initFirebase();
  }

  async #initLocal() {
    const la = new LocalAuth({
      store: { get: safeGet, set: safeSet, del: safeDel },
      onSignedOut: () => {
        // refresh token rejected (expired, revoked, logged out in another tab) → back to the login
        if (this.status === 'signed-in') {
          this.me = null;
          this.loginError = 'Your login expired. Log in again.';
          this.status = 'signed-out';
        }
      },
    });
    this.#local = la;
    setTokenProvider(
      () => la.getIdToken(),
      () => la.forceRefresh(),
    );
    let ok = false;
    try {
      ok = await la.restore();
    } catch (e) {
      this.loginError = authMessage(e);
    }
    if (ok) await this.#loadMe();
    else this.status = 'signed-out';
  }

  async #initMock() {
    setTokenProvider(async () => {
      const uid = safeGet(MOCK_KEY);
      return uid ? `mock:${uid}` : null;
    });
    if (safeGet(MOCK_KEY)) await this.#loadMe();
    else this.status = 'signed-out';
  }

  async #initFirebase() {
    const { initializeApp } = await import('firebase/app');
    const { getAuth, connectAuthEmulator, onAuthStateChanged } = await import('firebase/auth');
    const app = initializeApp(USE_EMULATORS ? { ...firebaseConfig, projectId: EMULATOR_PROJECT, authDomain: `${EMULATOR_PROJECT}.firebaseapp.com` } : firebaseConfig);
    const auth = getAuth(app);
    if (USE_EMULATORS) connectAuthEmulator(auth, AUTH_EMULATOR_URL, { disableWarnings: true });
    this.#auth = auth;
    setTokenProvider(async () => (this.#user ? this.#user.getIdToken() : null));
    await new Promise<void>((resolve) => {
      let first = true;
      onAuthStateChanged(auth, async (u) => {
        this.#user = u;
        if (u) await this.#loadMe();
        else {
          this.me = null;
          this.status = 'signed-out';
        }
        if (first) {
          first = false;
          resolve();
        }
      });
    });
  }

  async #loadMe(): Promise<boolean> {
    try {
      this.me = await api.getMe({});
      this.status = 'signed-in';
      this.loginError = null;
      return true;
    } catch (e) {
      this.me = null;
      this.loginError = loginApiMessage(e);
      await this.#signOutOnly();
      this.status = 'signed-out';
      return false;
    }
  }

  async refreshMe() {
    try {
      this.me = await api.getMe({});
    } catch {
      /* keep the old one */
    }
  }

  /** Throws a user-facing Error on failure. */
  async login(email: string, password: string): Promise<void> {
    this.loginError = null;
    if (MOCK) {
      const m = await import('./mock/server');
      const uid = m.mockLogin(email, password);
      if (!uid) throw new Error('Email or password is wrong');
      safeSet(MOCK_KEY, uid);
      if (!(await this.#loadMe())) throw new Error(this.loginError ?? 'Login failed');
      return;
    }
    await this.init();
    if (this.#local) {
      try {
        await this.#local.login(email, password);
      } catch (e) {
        throw new Error(authMessage(e));
      }
      if (!(await this.#loadMe())) throw new Error(this.loginError ?? 'Login failed');
      return;
    }
    const { signInWithEmailAndPassword } = await import('firebase/auth');
    try {
      const cred = await signInWithEmailAndPassword(this.#auth!, email.trim(), password);
      this.#user = cred.user;
    } catch (e) {
      throw new Error(authMessage(e));
    }
    if (!(await this.#loadMe())) throw new Error(this.loginError ?? 'Login failed');
  }

  /** §21 #108 local + OIDC: leave for the IdP; opm-cloud comes back to /login#oidc=<code>. */
  startOidc(next: string) {
    location.assign(`/auth/oidc/start?next=${encodeURIComponent(next)}`);
  }

  /** §21 #108 the one-time code of /login#oidc=<code> → token pair → signed in. Throws a user-facing Error. */
  async loginWithOidcCode(code: string): Promise<void> {
    this.loginError = null;
    await this.init();
    if (!this.#local) throw new Error('Single sign-on is not configured for this console');
    try {
      await this.#local.exchangeOidc(code);
    } catch (e) {
      throw new Error(authMessage(e));
    }
    if (!(await this.#loadMe())) throw new Error(this.loginError ?? 'Login failed');
  }

  async loginWithGoogle(): Promise<void> {
    if (MOCK) throw new Error('Google sign-in is not available in mock mode');
    await this.init();
    if (this.#local) throw new Error('Google sign-in is not available on this console');
    const { GoogleAuthProvider, signInWithPopup } = await import('firebase/auth');
    const p = new GoogleAuthProvider();
    if (GOOGLE_DOMAIN) p.setCustomParameters({ hd: GOOGLE_DOMAIN });
    try {
      const cred = await signInWithPopup(this.#auth!, p);
      this.#user = cred.user;
    } catch (e) {
      throw new Error(authMessage(e));
    }
    if (!(await this.#loadMe())) throw new Error(this.loginError ?? 'Login failed');
  }

  /** Is "Forgot password" offered? Firebase / mock: always (as before); local: only with opm-config auth.passwordReset. */
  get passwordResetOffered(): boolean {
    return this.provider !== 'local' || this.authConfig.passwordReset;
  }

  /** §21 #108 /reset-password?token= (opm-cloud links): is the token usable, and for which e-mail? */
  async checkPasswordReset(token: string): Promise<ResetCheck> {
    if (MOCK) {
      const m = await import('./mock/server');
      const email = m.mockResetCheck(token);
      return email ? { ok: true, email } : { ok: false, reason: 'invalid', message: 'This link expired or was already used.' };
    }
    return checkPasswordReset(fetch, '', token);
  }

  /** §21 #108 set the new password of a reset link. Throws a user-facing Error. */
  async confirmPasswordReset(token: string, password: string): Promise<void> {
    if (MOCK) {
      const m = await import('./mock/server');
      if (!m.mockResetConfirm(token, password)) throw new Error('This link expired or was already used. Ask for a new one.');
      return;
    }
    try {
      await confirmPasswordReset(fetch, '', token, password);
    } catch (e) {
      const code = (e as { code?: string }).code;
      const status = (e as { status?: number }).status;
      if (status === 410 || code === 'auth/expired' || code === 'auth/invalid-reset-token') throw new Error('This link expired or was already used. Ask for a new one.');
      throw new Error(authMessage(e));
    }
  }

  /** Always resolves (the UI always says "If the account exists, an email was sent"). */
  async sendPasswordReset(email: string): Promise<void> {
    if (MOCK) return;
    await this.init();
    if (this.#local) return requestPasswordReset(fetch, '', email);
    const { sendPasswordResetEmail } = await import('firebase/auth');
    try {
      await sendPasswordResetEmail(this.#auth!, email.trim());
    } catch {
      /* never reveal whether the account exists */
    }
  }

  async #signOutOnly() {
    if (MOCK) {
      safeDel(MOCK_KEY);
      return;
    }
    if (this.#local) {
      await this.#local.logout();
      return;
    }
    if (this.#auth) {
      const { signOut } = await import('firebase/auth');
      await signOut(this.#auth).catch(() => undefined);
    }
    this.#user = null;
  }

  async logout() {
    // §21 #107 the account's local drafts are deleted at logout
    await (await import('./branch/drafts.svelte')).endCloudDrafts();
    await this.#signOutOnly();
    this.me = null;
    this.status = 'signed-out';
  }
}

function loginApiMessage(e: unknown): string {
  if (isApiError(e) && e.code === 'forbidden') {
    if (/disabled/i.test(e.message)) return 'This account is disabled';
    return 'There is no OPM account for this login. Ask an admin.';
  }
  if (errCode(e) === 'unauthenticated') return 'Your login expired. Log in again.';
  return errMsg(e);
}

function safeGet(k: string): string | null {
  try {
    return localStorage.getItem(k);
  } catch {
    return null;
  }
}
function safeSet(k: string, v: string) {
  try {
    localStorage.setItem(k, v);
  } catch {
    /* ignore */
  }
}
function safeDel(k: string) {
  try {
    localStorage.removeItem(k);
  } catch {
    /* ignore */
  }
}

export const session = new Session();
