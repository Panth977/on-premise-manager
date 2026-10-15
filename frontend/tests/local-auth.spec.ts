import { test, expect } from '@playwright/test';

/**
 * §21 #108 the same console build on opm-cloud (VM, no Firebase): local auth via /auth/*.
 * Runs only with OPM_E2E_MODE=local + OPM_E2E_BASE (e.g. http://localhost:8790) + OPM_E2E_EMAIL / OPM_E2E_PASSWORD
 * against a running opm-cloud (playwright.config.ts starts no dev server in that mode):
 *   OPM_E2E_MODE=local OPM_E2E_BASE=http://localhost:8790 OPM_E2E_EMAIL=… OPM_E2E_PASSWORD=… npx playwright test
 */
const EMAIL = process.env.OPM_E2E_EMAIL ?? '';
const PASSWORD = process.env.OPM_E2E_PASSWORD ?? '';
const enabled = process.env.OPM_E2E_MODE === 'local' && !!process.env.OPM_E2E_BASE && !!EMAIL && !!PASSWORD;

test.describe('opm-cloud local auth (§21 #108)', () => {
  test.skip(!enabled, 'set OPM_E2E_MODE=local, OPM_E2E_BASE, OPM_E2E_EMAIL and OPM_E2E_PASSWORD');

  test('opm-config.json names the local provider', async ({ request }) => {
    const res = await request.get('/opm-config.json');
    expect(res.ok()).toBe(true);
    const cfg = (await res.json()) as { auth?: { provider?: string } };
    expect(cfg.auth?.provider).toBe('local');
  });

  test('a wrong password says "Email or password is wrong"', async ({ page }) => {
    await page.goto('/login');
    await page.getByTestId('login-email').fill(EMAIL);
    await page.getByTestId('login-password').fill(`${PASSWORD}-wrong`);
    await page.getByTestId('login-submit').click();
    await expect(page.getByTestId('login-error')).toContainText('Email or password is wrong');
  });

  test('login → shell → Accounts / Templates / Deployments → reload keeps the login → logout', async ({ page }) => {
    const firebase: string[] = [];
    page.on('request', (r) => {
      if (/googleapis\.com|firebaseapp\.com|gstatic\.com\/firebasejs/.test(r.url())) firebase.push(r.url());
    });
    await page.goto('/login?next=%2Fapp%2Fdeployments');
    await page.getByTestId('login-email').fill(EMAIL);
    await page.getByTestId('login-password').fill(PASSWORD);
    await page.getByTestId('login-submit').click();
    await page.waitForURL((u) => u.pathname.startsWith('/app/deployments'), { timeout: 20_000 });
    await expect(page.getByTestId('account-chip')).toBeVisible();
    await expect(page.getByTestId('page-title')).toHaveText('Deployments');
    expect(await page.evaluate(() => !!localStorage.getItem('opm.local.refresh'))).toBe(true);

    for (const [path, title] of [
      ['/app/accounts', 'Accounts'],
      ['/app/templates', 'Templates'],
      ['/app/deployments', 'Deployments'],
    ] as const) {
      await page.goto(path);
      await expect(page.getByTestId('page-title')).toHaveText(title);
      await expect(page.getByTestId('error-state')).toHaveCount(0);
    }

    // a reload restores the login from the stored refresh token
    await page.reload();
    await expect(page.getByTestId('page-title')).toHaveText('Deployments');

    await page.getByTestId('account-chip').click();
    await page.getByTestId('menu-logout').click();
    await page.waitForURL((u) => u.pathname === '/login');
    expect(await page.evaluate(() => localStorage.getItem('opm.local.refresh'))).toBeNull();
    // the local provider never loads Firebase
    expect(firebase).toEqual([]);
  });

  test('reset-password with a bogus token shows "expired or invalid"', async ({ page }) => {
    await page.goto('/reset-password?token=bogus-token-108');
    await expect(page.getByTestId('reset-invalid')).toBeVisible();
    await expect(page.getByTestId('reset-form')).toHaveCount(0);
  });
});
