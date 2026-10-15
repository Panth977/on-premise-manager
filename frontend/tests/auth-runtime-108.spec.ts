import { test, expect } from '@playwright/test';
import { parseRuntimeConfig, loadRuntimeConfig } from '../src/lib/auth/runtime-config';
import { LocalAuth, REFRESH_KEY, checkPasswordReset, confirmPasswordReset, type KeyStore } from '../src/lib/auth/local-auth';
import { authMessage } from '../src/lib/auth/messages';
import { login } from './helpers';

/**
 * §21 #108 runtime auth provider. Node-only tests of lib/auth (no browser) + the /reset-password page in mock mode
 * (token `mock-reset:<email>` is valid, anything else is "expired or invalid").
 */

function memStore(init: Record<string, string> = {}): KeyStore & { m: Map<string, string> } {
  const m = new Map(Object.entries(init));
  return { m, get: (k) => m.get(k) ?? null, set: (k, v) => void m.set(k, v), del: (k) => void m.delete(k) };
}

type Call = { url: string; body: unknown };
function fakeFetch(handler: (url: string, body: Record<string, unknown>) => Response | Promise<Response>) {
  const calls: Call[] = [];
  const f = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const body = init?.body ? (JSON.parse(String(init.body)) as Record<string, unknown>) : {};
    calls.push({ url, body });
    return handler(url, body);
  }) as typeof fetch;
  return { f, calls };
}
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
const pair = (n: number, expiresIn = 3600) => ({ idToken: `id-${n}`, refreshToken: `rt-${n}`, expiresIn, uid: 'u1', email: 'a@x.io' });
const noTimer = { setTimer: () => 0, clearTimer: () => undefined };

test.describe('runtime config (opm-config.json)', () => {
  test('parse: firebase defaults for missing / invalid / unknown; local fields', () => {
    for (const raw of [null, 'x', [], {}, { auth: null }, { auth: { provider: 'firebase' } }, { auth: { provider: 'saml' } }]) {
      expect(parseRuntimeConfig(raw)).toEqual({ auth: { provider: 'firebase', passwordReset: true, oidc: null }, apiBase: null });
    }
    expect(parseRuntimeConfig({ auth: { provider: 'local' } }).auth).toEqual({ provider: 'local', passwordReset: false, oidc: null });
    expect(parseRuntimeConfig({ auth: { provider: 'local', passwordReset: true, oidc: { label: ' Okta ' } }, apiBase: 'https://api.example/' })).toEqual({
      auth: { provider: 'local', passwordReset: true, oidc: { label: 'Okta' } },
      apiBase: 'https://api.example',
    });
    expect(parseRuntimeConfig({ auth: { provider: 'local', oidc: { label: '' } }, apiBase: 'javascript:x' })).toEqual({ auth: { provider: 'local', passwordReset: false, oidc: null }, apiBase: null });
  });

  test('load: HTML (SPA fallback), 404 and network errors → firebase defaults; no-store', async () => {
    const html = fakeFetch(() => new Response('<!doctype html><html></html>', { status: 200, headers: { 'Content-Type': 'text/html' } }));
    expect((await loadRuntimeConfig('/opm-config.json', html.f)).auth.provider).toBe('firebase');
    const nf = fakeFetch(() => new Response('nope', { status: 404 }));
    expect((await loadRuntimeConfig('/opm-config.json', nf.f)).auth.provider).toBe('firebase');
    const down = (async () => {
      throw new TypeError('Failed to fetch');
    }) as typeof fetch;
    expect((await loadRuntimeConfig('/opm-config.json', down)).auth.provider).toBe('firebase');
    let cache: RequestCache | undefined;
    const ok = (async (_i: RequestInfo | URL, init?: RequestInit) => {
      cache = init?.cache;
      return json(200, { auth: { provider: 'local' } });
    }) as typeof fetch;
    expect((await loadRuntimeConfig('/opm-config.json', ok)).auth.provider).toBe('local');
    expect(cache).toBe('no-store');
  });
});

test.describe('LocalAuth (/auth/*)', () => {
  test('login keeps the ID token in memory and the refresh token in localStorage; errors map like Firebase', async () => {
    const store = memStore();
    const { f, calls } = fakeFetch((url, b) => {
      if (url === '/auth/login') return b.password === 'pw' ? json(200, pair(1)) : json(400, { error: { code: 'auth/invalid-credential', message: 'bad' } });
      return json(404, {});
    });
    const la = new LocalAuth({ store, fetch: f, ...noTimer });
    const err = await la.login('a@x.io', 'nope').catch((e) => e);
    expect(authMessage(err)).toBe('Email or password is wrong');
    expect(store.m.size).toBe(0);
    await la.login(' a@x.io ', 'pw');
    expect(calls.at(-1)).toEqual({ url: '/auth/login', body: { email: 'a@x.io', password: 'pw' } });
    expect(store.get(REFRESH_KEY)).toBe('rt-1');
    expect(await la.getIdToken()).toBe('id-1');
    expect(calls.length).toBe(2);
    expect(authMessage({ code: 'auth/user-disabled' })).toBe('This account is disabled');
    expect(authMessage({ code: 'auth/too-many-requests' })).toBe('Too many attempts. Wait a moment and try again.');
  });

  test('refresh near expiry rotates the token; rejected refresh → signed out', async () => {
    let t = 0;
    let n = 1;
    let signedOut = 0;
    const store = memStore();
    const { f, calls } = fakeFetch((url, b) => {
      if (url === '/auth/login') return json(200, pair(n));
      if (url === '/auth/refresh') return b.refreshToken === `rt-${n}` ? json(200, pair(++n)) : json(400, { error: { code: 'auth/invalid-refresh-token', message: 'x' } });
      if (url === '/auth/logout') return new Response(null, { status: 204 });
      return json(404, {});
    });
    const la = new LocalAuth({ store, fetch: f, now: () => t, onSignedOut: () => signedOut++, ...noTimer });
    await la.login('a@x.io', 'pw');
    t = 3600_000 - 30_000; // < 1 min left
    expect(await la.getIdToken()).toBe('id-2');
    expect(store.get(REFRESH_KEY)).toBe('rt-2');
    // concurrent callers share one refresh
    const [a, b] = await Promise.all([la.forceRefresh(), la.forceRefresh()]);
    expect(a).toBe('id-3');
    expect(b).toBe('id-3');
    expect(calls.filter((c) => c.url === '/auth/refresh').length).toBe(2);
    // revoked server-side
    store.set(REFRESH_KEY, 'rt-revoked');
    n = 99;
    expect(await la.forceRefresh()).toBeNull();
    expect(signedOut).toBe(1);
    expect(store.get(REFRESH_KEY)).toBeNull();
  });

  test('another tab rotated the token → the stored one is used (and retried once after a reject)', async () => {
    const store = memStore();
    let valid = 'rt-1';
    let issued = 1;
    const { f } = fakeFetch((url, b) => {
      if (url === '/auth/login') return json(200, pair(1));
      if (url === '/auth/refresh') {
        if (b.refreshToken !== valid) {
          // the other tab rotated it right now
          if (b.refreshToken === 'rt-1') store.set(REFRESH_KEY, 'rt-other');
          return json(400, { error: { code: 'auth/invalid-refresh-token', message: 'x' } });
        }
        issued++;
        const p = pair(issued);
        valid = p.refreshToken;
        return json(200, p);
      }
      return json(404, {});
    });
    const la = new LocalAuth({ store, fetch: f, ...noTimer });
    await la.login('a@x.io', 'pw');
    valid = 'rt-other'; // rotated by tab B; tab A still holds rt-1 in memory, storage updated on the reject
    expect(await la.forceRefresh()).toBe('id-2');
    expect(store.get(REFRESH_KEY)).toBe('rt-2');
  });

  test('restore from localStorage; logout posts the refresh token and clears it', async () => {
    const store = memStore({ [REFRESH_KEY]: 'rt-7' });
    const { f, calls } = fakeFetch((url, b) => {
      if (url === '/auth/refresh') return b.refreshToken === 'rt-7' ? json(200, pair(8)) : json(400, { error: { code: 'auth/invalid-refresh-token', message: 'x' } });
      if (url === '/auth/logout') return new Response(null, { status: 204 });
      return json(404, {});
    });
    const la = new LocalAuth({ store, fetch: f, ...noTimer });
    expect(await la.restore()).toBe(true);
    expect(await la.getIdToken()).toBe('id-8');
    await la.logout();
    expect(calls.at(-1)).toEqual({ url: '/auth/logout', body: { refreshToken: 'rt-8' } });
    expect(store.get(REFRESH_KEY)).toBeNull();
    expect(await la.getIdToken()).toBeNull();
    expect(await new LocalAuth({ store: memStore(), fetch: f, ...noTimer }).restore()).toBe(false);
  });

  test('reset check / confirm', async () => {
    const { f } = fakeFetch((url, b) => {
      if (url.startsWith('/auth/reset/check?token=good')) return json(200, { email: 'a@x.io' });
      if (url.startsWith('/auth/reset/check?token=html')) return new Response('<!doctype html>', { status: 200 });
      if (url.startsWith('/auth/reset/check')) return new Response(null, { status: 410 });
      if (url === '/auth/reset/confirm') return b.token === 'good' ? new Response(null, { status: 204 }) : json(410, { error: { code: 'auth/expired', message: 'gone' } });
      return json(404, {});
    });
    expect(await checkPasswordReset(f, '', 'good')).toEqual({ ok: true, email: 'a@x.io' });
    expect((await checkPasswordReset(f, '', 'bogus')).ok).toBe(false);
    expect((await checkPasswordReset(f, '', 'html')).ok).toBe(false);
    expect((await checkPasswordReset(f, '', '')).ok).toBe(false);
    await confirmPasswordReset(f, '', 'good', 'new-password');
    const e = await confirmPasswordReset(f, '', 'old', 'new-password').catch((x) => x);
    expect(e.status).toBe(410);
  });
});

test.describe('/reset-password page (mock)', () => {
  test.skip(process.env.OPM_E2E_MODE !== undefined && process.env.OPM_E2E_MODE !== 'mock', 'mock only');

  test('bogus token → expired / invalid state', async ({ page }) => {
    await page.goto('/reset-password?token=bogus');
    await expect(page.getByTestId('reset-invalid')).toBeVisible();
    await expect(page.getByTestId('reset-form')).toHaveCount(0);
  });

  test('valid token → email, ≥ 8 chars + confirm, then login with the new password', async ({ page }) => {
    await page.goto('/reset-password?token=' + encodeURIComponent('mock-reset:viewer@example.com'));
    await expect(page.getByTestId('reset-email')).toHaveText('viewer@example.com');
    await page.getByTestId('reset-password').fill('short');
    await page.getByTestId('reset-confirm').fill('other');
    await page.getByTestId('reset-submit').click();
    await expect(page.getByTestId('reset-form').getByTestId('field-error')).toHaveCount(2);
    await page.getByTestId('reset-password').fill('brand-new-pass');
    await page.getByTestId('reset-confirm').fill('brand-new-pass');
    await page.getByTestId('reset-submit').click();
    await page.waitForURL((u) => u.pathname === '/login' && u.searchParams.get('reset') === '1');
    await expect(page.getByTestId('login-notice')).toBeVisible();
    await page.getByTestId('login-email').fill('viewer@example.com');
    await page.getByTestId('login-password').fill('brand-new-pass');
    await page.getByTestId('login-submit').click();
    await page.waitForURL((u) => u.pathname.startsWith('/app/deployments'));
  });

  test('mock login still offers Forgot password and no OIDC button', async ({ page }) => {
    await page.goto('/login');
    await expect(page.getByTestId('forgot')).toBeVisible();
    await expect(page.getByTestId('login-oidc')).toHaveCount(0);
    await login(page, 'admin');
  });
});
