/**
 * opm-cloud `/auth/*` endpoints of the local Auth (JSON, same origin as the console):
 *   GET  /auth/config                  { provider:'local', passwordReset, oidc: {label} | null }
 *   POST /auth/login {email,password}  token pair | 400 auth/invalid-credential | auth/user-disabled | auth/too-many-requests
 *   POST /auth/refresh {refreshToken}  new pair (rotation) | 400 auth/invalid-refresh-token
 *   POST /auth/logout {refreshToken}   204
 *   POST /auth/reset/request {email}   204 always (mails the link when sendMail is configured)
 *   GET  /auth/reset/check?token=      { email } | 410
 *   POST /auth/reset/confirm {token,password}  204 | 400 auth/weak-password | 410 auth/expired | auth/invalid-reset-token
 *   GET  /auth/oidc/start?next=/app    302 → the provider (PKCE S256 + state + nonce in authOidcState/{state})
 *   GET  /auth/oidc/callback           302 → <console>/login?next=<next>#oidc=<one-time code> (or #oidcError=<code>)
 *   POST /auth/oidc/exchange {code}    token pair | 400 auth/invalid-oidc-code
 */
import { createHash, createPublicKey, verify as cryptoVerify, type JsonWebKey } from 'node:crypto';
import { AuthError, logger } from './index.js';
import { normEmail, randomToken, sha256, type LocalAuth, type UserDoc } from './auth.js';

export interface OidcConfig {
  issuer: string;
  clientId: string;
  clientSecret: string;
  label: string;
  allowedDomain?: string | null;
  redirectUri: string;
}
export interface AuthHttpRequest {
  method: string;
  url: string;
  headers: Record<string, string | undefined>;
  body: Buffer;
  ip: string;
}
export interface AuthHttpResponse {
  status: number;
  headers: Record<string, string>;
  body: string | null;
}
export interface AuthHttpOptions {
  sendMail?: (to: string, subject: string, text: string) => Promise<void>;
  oidc?: OidcConfig | null;
  consoleUrl: string;
}

const JSON_H = { 'Content-Type': 'application/json; charset=utf-8', 'Cache-Control': 'no-store' };
const json = (status: number, o: unknown): AuthHttpResponse => ({ status, headers: JSON_H, body: JSON.stringify(o) });
const fail = (status: number, code: string, message: string) => json(status, { error: { code, message } });
const noContent = (): AuthHttpResponse => ({ status: 204, headers: { 'Cache-Control': 'no-store' }, body: null });
const redirect = (to: string): AuthHttpResponse => ({ status: 302, headers: { Location: to, 'Cache-Control': 'no-store' }, body: null });

// ─── login failure limiter (in memory, per ip + e-mail)
const MAX_FAILS = 10;
const WINDOW_MS = 15 * 60_000;
const fails = new Map<string, { n: number; since: number }>();
function limited(k: string): boolean {
  const f = fails.get(k);
  if (!f) return false;
  if (Date.now() - f.since > WINDOW_MS) {
    fails.delete(k);
    return false;
  }
  return f.n >= MAX_FAILS;
}
function failed(k: string) {
  const f = fails.get(k);
  if (!f || Date.now() - f.since > WINDOW_MS) fails.set(k, { n: 1, since: Date.now() });
  else f.n++;
  if (fails.size > 50_000) for (const [key, v] of fails) if (Date.now() - v.since > WINDOW_MS) fails.delete(key);
}
/** test hook */
export function resetLoginLimiter() {
  fails.clear();
}

/** the providers id of an OIDC issuer (google.com for Google, else the issuer host) */
export const providerOf = (issuer: string) => {
  const host = new URL(issuer).host;
  return /(^|\.)accounts\.google\.com$/.test(host) ? 'google.com' : host;
};
/** map key of a federated provider (no dots: field paths) */
export const fedKey = (provider: string) => provider.replace(/[^A-Za-z0-9_-]/g, '_');

// ─── OIDC discovery + JWKS caches
interface Discovery {
  issuer: string;
  authorization_endpoint: string;
  token_endpoint: string;
  jwks_uri: string;
}
const discoCache = new Map<string, { at: number; d: Discovery }>();
const jwksCache = new Map<string, { at: number; keys: (JsonWebKey & { kid?: string })[] }>();
async function discover(issuer: string): Promise<Discovery> {
  const c = discoCache.get(issuer);
  if (c && Date.now() - c.at < 3_600_000) return c.d;
  const r = await fetch(`${issuer.replace(/\/$/, '')}/.well-known/openid-configuration`);
  if (!r.ok) throw new Error(`OIDC discovery failed: HTTP ${r.status}`);
  const d = (await r.json()) as Discovery;
  discoCache.set(issuer, { at: Date.now(), d });
  return d;
}
async function jwk(uri: string, kid: string | undefined): Promise<JsonWebKey> {
  for (let fresh = false; ; fresh = true) {
    let c = jwksCache.get(uri);
    if (!c || fresh || Date.now() - c.at > 3_600_000) {
      const r = await fetch(uri);
      if (!r.ok) throw new Error(`JWKS fetch failed: HTTP ${r.status}`);
      c = { at: Date.now(), keys: ((await r.json()) as { keys: (JsonWebKey & { kid?: string })[] }).keys ?? [] };
      jwksCache.set(uri, c);
    }
    const k = c.keys.find((x) => (kid ? x.kid === kid : x.kty === 'RSA'));
    if (k) return k;
    if (fresh) throw new Error('OIDC: no JWKS key for the token');
  }
}
/** verify an RS256 id_token → its claims */
async function verifyIdTokenRs256(token: string, d: Discovery, o: OidcConfig, nonce: string): Promise<Record<string, unknown>> {
  const [h, p, s] = token.split('.');
  if (!h || !p || !s) throw new Error('OIDC: malformed id_token');
  const head = JSON.parse(Buffer.from(h, 'base64url').toString('utf8')) as { alg?: string; kid?: string };
  if (head.alg !== 'RS256') throw new Error(`OIDC: unsupported alg ${head.alg}`);
  const key = createPublicKey({ key: await jwk(d.jwks_uri, head.kid), format: 'jwk' });
  if (!cryptoVerify('RSA-SHA256', Buffer.from(`${h}.${p}`), key, Buffer.from(s, 'base64url'))) throw new Error('OIDC: bad id_token signature');
  const c = JSON.parse(Buffer.from(p, 'base64url').toString('utf8')) as Record<string, unknown>;
  const issOk = c.iss === d.issuer || c.iss === o.issuer || (providerOf(o.issuer) === 'google.com' && (c.iss === 'accounts.google.com' || c.iss === 'https://accounts.google.com'));
  if (!issOk) throw new Error('OIDC: wrong issuer');
  const aud = Array.isArray(c.aud) ? c.aud : [c.aud];
  if (!aud.includes(o.clientId)) throw new Error('OIDC: wrong audience');
  if (typeof c.exp !== 'number' || c.exp * 1000 < Date.now() - 60_000) throw new Error('OIDC: id_token expired');
  if (c.nonce !== nonce) throw new Error('OIDC: nonce mismatch');
  if (c.email_verified !== true && c.email_verified !== 'true') throw new Error('OIDC: e-mail not verified');
  if (o.allowedDomain && c.hd !== o.allowedDomain) throw new Error('OIDC: domain not allowed');
  if (typeof c.sub !== 'string' || typeof c.email !== 'string') throw new Error('OIDC: sub / email missing');
  return c;
}
const resetGone = (why: 'invalid' | 'expired') => (why === 'expired' ? fail(410, 'auth/expired', 'The reset link has expired.') : fail(410, 'auth/invalid-reset-token', 'The reset link is invalid or was already used.'));
const safeNext = (n: string | null) => (n && n.startsWith('/') && !n.startsWith('//') && !n.includes('\\') ? n : null);

function parseBody(b: Buffer): Record<string, unknown> {
  if (!b?.length) return {};
  try {
    const v = JSON.parse(b.toString('utf8'));
    return v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : {};
  } catch {
    return {};
  }
}

export async function handleAuthHttp(req: AuthHttpRequest, auth: LocalAuth, o: AuthHttpOptions): Promise<AuthHttpResponse> {
  const u = new URL(req.url, 'http://local');
  const i = u.pathname.indexOf('/auth/');
  const route = i < 0 ? '' : u.pathname.slice(i + 5).replace(/\/$/, '');
  const m = req.method.toUpperCase();
  const consoleUrl = o.consoleUrl.replace(/\/$/, '');
  if (m === 'OPTIONS') return { status: 204, headers: { Allow: 'GET, POST, OPTIONS' }, body: null };
  const body = m === 'POST' ? parseBody(req.body) : {};
  const str = (k: string) => (typeof body[k] === 'string' ? (body[k] as string) : '');
  try {
    switch (`${m} ${route}`) {
      case 'GET /config':
        return json(200, { provider: 'local', passwordReset: !!o.sendMail, oidc: o.oidc ? { label: o.oidc.label } : null });
      case 'POST /login': {
        let email: string;
        try {
          email = normEmail(str('email'));
        } catch {
          return fail(400, 'auth/invalid-credential', 'The supplied auth credential is incorrect.');
        }
        const k = `${req.ip}|${email}`;
        if (limited(k)) return fail(400, 'auth/too-many-requests', 'Too many failed sign-in attempts. Try again later.');
        let user: UserDoc;
        try {
          user = await auth.checkPassword(email, str('password'));
        } catch (e) {
          if (e instanceof AuthError && e.code === 'auth/invalid-credential') failed(k);
          throw e;
        }
        fails.delete(k);
        return json(200, await auth.issue(user));
      }
      case 'POST /refresh':
        return json(200, await auth.refresh(str('refreshToken')));
      case 'POST /logout':
        await auth.logout(str('refreshToken'));
        return noContent();
      case 'POST /reset/request': {
        const email = str('email');
        if (o.sendMail) {
          const send = o.sendMail;
          // in the background: the answer never tells whether the account exists
          void (async () => {
            try {
              const link = await auth.generatePasswordResetLink(email);
              await send(normEmail(email), 'Reset your OPM password', `Open this link within 1 hour to choose a new password:\n\n${link}\n\nIf you did not ask for this, ignore this e-mail.`);
            } catch (e) {
              if (!(e instanceof AuthError)) logger.warn('password reset mail failed', { err: String(e) });
            }
          })();
        }
        return noContent();
      }
      case 'GET /reset/check': {
        const r = await auth.checkReset(u.searchParams.get('token') ?? '');
        return 'email' in r ? json(200, { email: r.email }) : resetGone(r.error);
      }
      case 'POST /reset/confirm': {
        const pw = str('password');
        if (pw.length < 8) return fail(400, 'auth/weak-password', 'The password must be at least 8 characters.');
        const c = await auth.checkReset(str('token'));
        if ('error' in c) return resetGone(c.error);
        const r = await auth.confirmReset(str('token'), pw);
        return r === 'ok' ? noContent() : resetGone(r);
      }
      case 'GET /oidc/start': {
        if (!o.oidc) return fail(404, 'auth/operation-not-allowed', 'single sign-on is not configured');
        const d = await discover(o.oidc.issuer);
        const state = randomToken();
        const nonce = randomToken();
        const verifier = randomToken();
        await auth.store.doc(`authOidcState/${sha256(state)}`).set({ verifier, nonce, next: safeNext(u.searchParams.get('next')), expiresAt: new Date(Date.now() + 10 * 60_000).toISOString() });
        const a = new URL(d.authorization_endpoint);
        a.searchParams.set('response_type', 'code');
        a.searchParams.set('client_id', o.oidc.clientId);
        a.searchParams.set('redirect_uri', o.oidc.redirectUri);
        a.searchParams.set('scope', 'openid email profile');
        a.searchParams.set('state', state);
        a.searchParams.set('nonce', nonce);
        a.searchParams.set('code_challenge', createHash('sha256').update(verifier).digest('base64url'));
        a.searchParams.set('code_challenge_method', 'S256');
        if (o.oidc.allowedDomain) a.searchParams.set('hd', o.oidc.allowedDomain);
        return redirect(a.toString());
      }
      case 'GET /oidc/callback':
        return await oidcCallback(u, auth, o, consoleUrl);
      case 'POST /oidc/exchange': {
        const ref = auth.store.doc(`authOidcCode/${sha256(str('code') || '-')}`);
        const uid = await auth.store.runTransaction(async (tx) => {
          const s = await tx.get(ref);
          if (!s.exists) return null;
          const c = s.data() as { uid: string; expiresAt: string };
          tx.delete(ref);
          return c.expiresAt < new Date().toISOString() ? null : c.uid;
        });
        if (!uid) return fail(400, 'auth/invalid-oidc-code', 'The sign-in code is invalid or expired.');
        const s = await auth.store.doc(`authUsers/${uid}`).get();
        if (!s.exists) return fail(400, 'auth/user-not-found', 'There is no user record corresponding to the provided identifier.');
        const user = s.data() as UserDoc;
        if (user.disabled) return fail(400, 'auth/user-disabled', 'The user account has been disabled by an administrator.');
        return json(200, await auth.issue(user));
      }
      default:
        return fail(404, 'not-found', `no auth endpoint ${m} ${route}`);
    }
  } catch (e) {
    if (e instanceof AuthError) return fail(400, e.code, e.message);
    logger.error('auth endpoint failed', { route, err: e instanceof Error ? (e.stack ?? e.message) : String(e) });
    return fail(500, 'internal', 'internal error');
  }
}

async function oidcCallback(u: URL, auth: LocalAuth, o: AuthHttpOptions, consoleUrl: string): Promise<AuthHttpResponse> {
  let next: string | null = null;
  const back = (frag: string) => redirect(`${consoleUrl}/login${next ? `?next=${encodeURIComponent(next)}` : ''}#${frag}`);
  if (!o.oidc) return back('oidcError=not-configured');
  const cfg = o.oidc;
  if (u.searchParams.get('error')) return back(`oidcError=${encodeURIComponent(u.searchParams.get('error')!)}`);
  const state = u.searchParams.get('state') ?? '';
  const code = u.searchParams.get('code') ?? '';
  const sref = auth.store.doc(`authOidcState/${sha256(state || '-')}`);
  const st = await auth.store.runTransaction(async (tx) => {
    const s = await tx.get(sref);
    if (!s.exists) return null;
    tx.delete(sref);
    const v = s.data() as { verifier: string; nonce: string; next: string | null; expiresAt: string };
    return v.expiresAt < new Date().toISOString() ? null : v;
  });
  next = st?.next ?? null;
  if (!st || !code) return back('oidcError=invalid-state');
  try {
    const d = await discover(cfg.issuer);
    const r = await fetch(d.token_endpoint, {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded', Accept: 'application/json' },
      body: new URLSearchParams({ grant_type: 'authorization_code', code, redirect_uri: cfg.redirectUri, client_id: cfg.clientId, client_secret: cfg.clientSecret, code_verifier: st.verifier }).toString(),
    });
    if (!r.ok) throw new Error(`OIDC token endpoint: HTTP ${r.status}`);
    const tok = (await r.json()) as { id_token?: string };
    if (!tok.id_token) throw new Error('OIDC: no id_token');
    const c = await verifyIdTokenRs256(tok.id_token, d, cfg, st.nonce);
    const provider = providerOf(cfg.issuer);
    const fk = fedKey(provider);
    const email = normEmail(c.email);
    let user = await auth.userByEmail(email);
    if (!user) {
      // like a Google sign-in without an OPM account: an auth user exists, /v1/me answers "no account"
      user = await auth.createUserDoc({ email, emailVerified: true, displayName: typeof c.name === 'string' ? c.name : undefined, providers: [provider], federated: { [fk]: c.sub as string } });
    } else {
      const known = user.federated?.[fk];
      if (known && known !== c.sub) throw new Error('OIDC: the e-mail is linked to a different identity');
      if (!known) await auth.linkFederated(user.uid, provider, c.sub as string);
    }
    if (user.disabled) return back('oidcError=user-disabled');
    const one = randomToken();
    await auth.store.doc(`authOidcCode/${sha256(one)}`).set({ uid: user.uid, expiresAt: new Date(Date.now() + 2 * 60_000).toISOString() });
    return back(`oidc=${one}`);
  } catch (e) {
    logger.warn('OIDC sign-in failed', { err: e instanceof Error ? e.message : String(e) });
    return back('oidcError=sign-in-failed');
  }
}
