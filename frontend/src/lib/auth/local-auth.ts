/**
 * §21 #108 local auth against opm-cloud's /auth/* JSON API (same origin, no Firebase).
 *   POST /auth/login {email,password}      → 200 TokenPair | 400 {error:{code,message}}
 *   POST /auth/refresh {refreshToken}       → 200 TokenPair (rotated) | 400 auth/invalid-refresh-token
 *   POST /auth/logout {refreshToken}        → 204
 *   POST /auth/oidc/exchange {code}         → 200 TokenPair | 400 {error}
 *   POST /auth/reset/request {email}        → 204 (always)
 *   GET  /auth/reset/check?token=           → 200 {email} | 410
 *   POST /auth/reset/confirm {token,password} → 204 | 400/410 {error}
 * The ID token lives in memory only; the refresh token in localStorage (REFRESH_KEY) so a reload / other tab keeps the
 * login. Refresh ~5 min before expiry, and on demand after a 401. Pure (no Svelte / import.meta.env) → unit-testable.
 */
export const REFRESH_KEY = 'opm.local.refresh';
/** refresh this long before the ID token expires */
export const REFRESH_LEAD_MS = 5 * 60_000;

export interface TokenPair {
  idToken: string;
  refreshToken: string;
  /** seconds */
  expiresIn: number;
  uid: string;
  email: string;
}

export interface KeyStore {
  get(k: string): string | null;
  set(k: string, v: string): void;
  del(k: string): void;
}

/** An /auth/* failure; `code` is an auth/* code (same codes Firebase uses where they overlap). */
export class LocalAuthError extends Error {
  readonly code: string;
  readonly status: number;
  constructor(code: string, message: string, status: number) {
    super(message);
    this.name = 'LocalAuthError';
    this.code = code;
    this.status = status;
  }
}

export interface LocalAuthOptions {
  /** prefix of /auth (default '' = same origin) */
  base?: string;
  fetch?: typeof fetch;
  store: KeyStore;
  now?: () => number;
  /** the refresh token was rejected (expired / revoked / signed out in another tab) → the app shows the login */
  onSignedOut?: () => void;
  /** schedule the background refresh (tests pass a no-op) */
  setTimer?: (fn: () => void, ms: number) => unknown;
  clearTimer?: (h: unknown) => void;
}

async function readError(res: Response): Promise<LocalAuthError> {
  let code = res.status === 410 ? 'auth/expired' : res.status === 429 ? 'auth/too-many-requests' : 'auth/internal';
  let message = res.statusText || `HTTP ${res.status}`;
  try {
    const j = (await res.json()) as { error?: { code?: unknown; message?: unknown } };
    if (typeof j?.error?.code === 'string') code = j.error.code;
    if (typeof j?.error?.message === 'string') message = j.error.message;
  } catch {
    /* not JSON */
  }
  return new LocalAuthError(code, message, res.status);
}

function isPair(x: unknown): x is TokenPair {
  const p = x as Partial<TokenPair> | null;
  return !!p && typeof p.idToken === 'string' && !!p.idToken && typeof p.refreshToken === 'string' && !!p.refreshToken && typeof p.expiresIn === 'number' && typeof p.uid === 'string';
}

export async function postJson(f: typeof fetch, url: string, body: unknown): Promise<Response> {
  try {
    return await f(url, { method: 'POST', headers: { 'Content-Type': 'application/json', Accept: 'application/json' }, body: JSON.stringify(body), cache: 'no-store', credentials: 'same-origin' });
  } catch (e) {
    throw new LocalAuthError('auth/network-request-failed', (e as Error)?.message || 'network error', 0);
  }
}

export class LocalAuth {
  readonly base: string;
  readonly #f: typeof fetch;
  readonly #store: KeyStore;
  readonly #now: () => number;
  readonly #onSignedOut: () => void;
  readonly #setTimer: (fn: () => void, ms: number) => unknown;
  readonly #clearTimer: (h: unknown) => void;
  #idToken: string | null = null;
  #expiresAt = 0;
  /** the refresh token this tab last used / received */
  #refresh: string | null = null;
  #timer: unknown = null;
  #inflight: Promise<string | null> | null = null;
  user: { uid: string; email: string } | null = null;

  constructor(o: LocalAuthOptions) {
    this.base = (o.base ?? '').replace(/\/$/, '');
    this.#f = o.fetch ?? ((i, init) => fetch(i, init));
    this.#store = o.store;
    this.#now = o.now ?? (() => Date.now());
    this.#onSignedOut = o.onSignedOut ?? (() => undefined);
    this.#setTimer = o.setTimer ?? ((fn, ms) => setTimeout(fn, ms));
    this.#clearTimer = o.clearTimer ?? ((h) => clearTimeout(h as ReturnType<typeof setTimeout>));
  }

  get signedIn(): boolean {
    return !!this.#idToken;
  }

  /** At startup: a stored refresh token → a fresh ID token. false = signed out. Throws only on network errors. */
  async restore(): Promise<boolean> {
    if (!this.#store.get(REFRESH_KEY)) return false;
    return (await this.refresh()) !== null;
  }

  async login(email: string, password: string): Promise<TokenPair> {
    return this.#signIn('/auth/login', { email: email.trim(), password });
  }

  async exchangeOidc(code: string): Promise<TokenPair> {
    return this.#signIn('/auth/oidc/exchange', { code });
  }

  async #signIn(path: string, body: unknown): Promise<TokenPair> {
    const res = await postJson(this.#f, this.base + path, body);
    if (!res.ok) throw await readError(res);
    const pair = (await res.json().catch(() => null)) as unknown;
    if (!isPair(pair)) throw new LocalAuthError('auth/internal', 'The login service sent an invalid answer', res.status);
    this.#accept(pair);
    return pair;
  }

  /** Current ID token (refreshes when it expires within a minute); null = signed out. */
  async getIdToken(): Promise<string | null> {
    if (this.#idToken && this.#now() < this.#expiresAt - 60_000) return this.#idToken;
    if (!this.#idToken && !this.#store.get(REFRESH_KEY)) return null;
    return this.refresh();
  }

  /** After a 401: a new ID token even if the current one looks valid (null = signed out). */
  async forceRefresh(): Promise<string | null> {
    return this.refresh();
  }

  /** One refresh at a time per tab. A token rotated by another tab is picked up from localStorage first. */
  refresh(): Promise<string | null> {
    if (!this.#inflight) {
      this.#inflight = this.#doRefresh().finally(() => {
        this.#inflight = null;
      });
    }
    return this.#inflight;
  }

  async #doRefresh(): Promise<string | null> {
    let token = this.#store.get(REFRESH_KEY);
    if (!token) {
      this.#signedOut();
      return null;
    }
    for (let attempt = 0; attempt < 2; attempt++) {
      const res = await postJson(this.#f, this.base + '/auth/refresh', { refreshToken: token });
      if (res.ok) {
        const pair = (await res.json().catch(() => null)) as unknown;
        if (!isPair(pair)) throw new LocalAuthError('auth/internal', 'The login service sent an invalid answer', res.status);
        this.#accept(pair);
        return pair.idToken;
      }
      const err = await readError(res);
      if (res.status >= 500 || err.code === 'auth/network-request-failed' || err.code === 'auth/too-many-requests') throw err;
      // rejected: another tab may have rotated it meanwhile → retry once with the newer stored token
      const now = this.#store.get(REFRESH_KEY);
      if (now && now !== token) {
        token = now;
        continue;
      }
      break;
    }
    this.#clear(false);
    // drop the rejected token (not a newer one another tab may have stored meanwhile)
    if (this.#store.get(REFRESH_KEY) === token) this.#store.del(REFRESH_KEY);
    this.#signedOut();
    return null;
  }

  /** POST /auth/logout (best effort) + forget both tokens. */
  async logout(): Promise<void> {
    const token = this.#store.get(REFRESH_KEY) ?? this.#refresh;
    this.#clear(true);
    if (token) {
      try {
        await postJson(this.#f, this.base + '/auth/logout', { refreshToken: token });
      } catch {
        /* offline: the token expires on its own */
      }
    }
  }

  #accept(p: TokenPair) {
    this.#idToken = p.idToken;
    this.#expiresAt = this.#now() + Math.max(0, p.expiresIn) * 1000;
    this.#refresh = p.refreshToken;
    this.user = { uid: p.uid, email: p.email ?? '' };
    this.#store.set(REFRESH_KEY, p.refreshToken);
    this.#schedule(Math.max(10_000, p.expiresIn * 1000 - REFRESH_LEAD_MS));
  }

  #schedule(ms: number) {
    if (this.#timer) this.#clearTimer(this.#timer);
    this.#timer = this.#setTimer(() => {
      this.#timer = null;
      void this.refresh().catch(() => {
        // offline: try again in a minute (the next API call refreshes on demand anyway)
        if (this.#idToken) this.#schedule(60_000);
      });
    }, ms);
  }

  #clear(dropStored: boolean) {
    if (this.#timer) this.#clearTimer(this.#timer);
    this.#timer = null;
    this.#idToken = null;
    this.#expiresAt = 0;
    this.user = null;
    if (dropStored) this.#store.del(REFRESH_KEY);
    this.#refresh = null;
  }

  #signedOut() {
    this.#onSignedOut();
  }
}

// ── password reset (stateless; the UI never says whether an account exists)

/** POST /auth/reset/request — always resolves. */
export async function requestPasswordReset(f: typeof fetch, base: string, email: string): Promise<void> {
  try {
    await postJson(f, base + '/auth/reset/request', { email: email.trim() });
  } catch {
    /* never reveal anything */
  }
}

export type ResetCheck = { ok: true; email: string } | { ok: false; reason: 'invalid' | 'error'; message: string };

/** GET /auth/reset/check — 200 {email} = usable; 410 / 4xx / a non-JSON answer (no opm-cloud) = invalid or expired. */
export async function checkPasswordReset(f: typeof fetch, base: string, token: string): Promise<ResetCheck> {
  if (!token) return { ok: false, reason: 'invalid', message: 'This link is incomplete.' };
  let res: Response;
  try {
    res = await f(`${base}/auth/reset/check?token=${encodeURIComponent(token)}`, { headers: { Accept: 'application/json' }, cache: 'no-store' });
  } catch (e) {
    return { ok: false, reason: 'error', message: `Cannot reach the login service (${(e as Error)?.message || 'network error'}).` };
  }
  if (res.status >= 500) return { ok: false, reason: 'error', message: `The login service failed (HTTP ${res.status}). Try again.` };
  if (!res.ok) return { ok: false, reason: 'invalid', message: 'This link expired or was already used.' };
  const j = (await res.json().catch(() => null)) as { email?: unknown } | null;
  if (!j || typeof j.email !== 'string') return { ok: false, reason: 'invalid', message: 'This link expired or was already used.' };
  return { ok: true, email: j.email };
}

/** POST /auth/reset/confirm — throws LocalAuthError (410 → code auth/expired). */
export async function confirmPasswordReset(f: typeof fetch, base: string, token: string, password: string): Promise<void> {
  const res = await postJson(f, base + '/auth/reset/confirm', { token, password });
  if (!res.ok) throw await readError(res);
}
