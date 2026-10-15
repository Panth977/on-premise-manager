import { test, expect, type Page } from '@playwright/test';
import { login, expectToast, SEED, nav, commitAll } from './helpers';

/**
 * Round G (SPEC-final §21 #84): nothing is "shown once". RevealValue = masked → View (logged) → 60 s countdown ring →
 * masked again (timer, navigation, hidden tab); Copy while visible; Regenerate… (typed ACCEPT where configured).
 */
const D = `/app/deployments/${SEED.deployment}`; //       ACME: access key NOT stored (pre-#84), sites enrolled (used tokens)
const E = `/app/deployments/${SEED.deploymentEmpty}`; // North grid: access key stored, site main not installed

test.use({ permissions: ['clipboard-read', 'clipboard-write'] });

const left = async (page: Page, testid: string) => Number(await page.getByTestId(`${testid}-countdown`).getAttribute('data-left'));

test.describe('RevealValue (access key on Setup)', () => {
  test('masked → View → countdown ring decreasing → re-masks at 60 s (fake clock); Copy while visible', async ({ page }) => {
    await page.clock.install();
    await login(page, 'admin', `${E}/setup`);
    await expect(page.getByTestId('access-key-masked')).toBeVisible();
    await expect(page.getByTestId('access-key-length')).toHaveText('29 chars');
    await expect(page.getByTestId('access-key-panel')).toContainText('by Asha Admin');
    await expect(page.getByTestId('access-key-value')).toHaveCount(0);
    await page.getByTestId('access-key-view').click();
    const v = page.getByTestId('access-key-value');
    await expect(v).toHaveText(/^([A-Z2-7]{4}-){5}[A-Z2-7]{4}$/);
    const ring = page.getByTestId('access-key-countdown');
    await expect(ring).toBeVisible();
    await expect(ring.locator('svg circle')).toHaveCount(2);
    const l0 = await left(page, 'access-key');
    expect(l0).toBeGreaterThanOrEqual(58);
    expect(l0).toBeLessThanOrEqual(60);
    // Copy while visible
    await page.getByTestId('access-key-copy').click();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(await v.textContent());
    // never in browser storage
    const key = (await v.textContent())!;
    const stored = await page.evaluate((k) => {
      const all: string[] = [];
      for (let i = 0; i < sessionStorage.length; i++) all.push(sessionStorage.getItem(sessionStorage.key(i)!) ?? '');
      for (let i = 0; i < localStorage.length; i++) if (localStorage.key(i) !== 'opm.mock.db') all.push(localStorage.getItem(localStorage.key(i)!) ?? '');
      return all.some((x) => x.includes(k));
    }, key);
    expect(stored).toBe(false);

    await page.clock.fastForward(20_000);
    await expect.poll(() => left(page, 'access-key')).toBeLessThanOrEqual(l0 - 19);
    await expect(ring).toHaveText(String(await left(page, 'access-key')));
    await page.clock.fastForward(41_000);
    await expect(page.getByTestId('access-key-value')).toHaveCount(0);
    await expect(page.getByTestId('access-key-masked')).toBeVisible();
  });

  test('re-masks on navigation and when the tab is hidden; Hide button', async ({ page }) => {
    await login(page, 'admin', `${E}/setup`);
    // same-page navigation (the component stays mounted)
    await page.getByTestId('access-key-view').click();
    await expect(page.getByTestId('access-key-value')).toBeVisible();
    await page.locator(`a[href="${E}/setup"]`).first().click();
    await expect(page.getByTestId('access-key-value')).toHaveCount(0);
    await expect(page.getByTestId('access-key-masked')).toBeVisible();
    // another route and back
    await page.getByTestId('access-key-view').click();
    await expect(page.getByTestId('access-key-value')).toBeVisible();
    await page.locator(`a[href="${E}/activities"]`).first().click();
    await page.waitForURL(`**${E}/activities`);
    await page.goBack();
    await expect(page.getByTestId('access-key-masked')).toBeVisible();
    await expect(page.getByTestId('access-key-value')).toHaveCount(0);
    // hidden tab
    await page.getByTestId('access-key-view').click();
    await expect(page.getByTestId('access-key-value')).toBeVisible();
    await page.evaluate(() => {
      Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' });
      document.dispatchEvent(new Event('visibilitychange'));
    });
    await expect(page.getByTestId('access-key-value')).toHaveCount(0);
    await page.evaluate(() => {
      Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' });
    });
    // Hide
    await page.getByTestId('access-key-view').click();
    await page.getByTestId('access-key-hide').click();
    await expect(page.getByTestId('access-key-masked')).toBeVisible();
  });

  test('a reveal writes the activity access-key.revealed and the audit entry deployment.access-key-revealed', async ({ page }) => {
    await login(page, 'admin', `${E}/setup`);
    await page.getByTestId('access-key-view').click();
    await expect(page.getByTestId('access-key-value')).toBeVisible();
    await nav(page, `${E}/activities?type=access-key.revealed`);
    await expect(page.getByTestId('activity-row').first()).toContainText('access-key.revealed');
    await nav(page, '/app/audit-log?action=deployment.access-key-revealed');
    const row = page.getByTestId('audit-row').first();
    await expect(row).toHaveAttribute('data-action', 'deployment.access-key-revealed');
    await expect(row).toHaveAttribute('data-outcome', 'ok');
    await row.click();
    await expect(page.getByTestId('audit-drawer-action-label')).toHaveText('Access key viewed');
  });

  test('not stored (pre-#84 deployment) → Regenerate with typed ACCEPT shows the new key; rate limit toast', async ({ page }) => {
    await login(page, 'admin', `${D}/setup`);
    await expect(page.getByTestId('access-key-not-stored')).toHaveText('not stored — regenerate to view');
    await expect(page.getByTestId('access-key-view')).toHaveCount(0);
    await page.getByTestId('access-key-regenerate').click();
    const dlg = page.getByTestId('access-key-confirm');
    await expect(dlg).toContainText('sites switch one by one');
    await expect(dlg.getByTestId('confirm-ok')).toBeDisabled();
    await dlg.getByTestId('typed-confirm').fill('ACCEPT');
    await dlg.getByTestId('confirm-ok').click();
    await expect(page.getByTestId('access-key-value')).toHaveText(/^([A-Z2-7]{4}-){5}[A-Z2-7]{4}$/);
    await expect(page.getByTestId('access-key-countdown')).toBeVisible();
    await expect(page.getByTestId('key-version')).toHaveText('v2');
    await page.getByTestId('access-key-hide').click();
    // now stored → View works
    await page.getByTestId('access-key-view').click();
    await expect(page.getByTestId('access-key-value')).toHaveText(/^([A-Z2-7]{4}-){5}[A-Z2-7]{4}$/);
    await page.getByTestId('access-key-hide').click();
    // 30 value-returning requests per minute → 429 → "Too many reveals — try again in N s"
    await page.evaluate(async (id) => {
      localStorage.setItem('opm.mock.latency', '0');
      const h = { Authorization: 'Bearer mock:uAdmin00000000000001' };
      for (let i = 0; i < 31; i++) await (window as unknown as { __opmMockFetch: typeof fetch }).__opmMockFetch(`${location.origin}/v1/deployments/${id}/access-key/reveal`, { method: 'POST', headers: h });
    }, SEED.deployment);
    await page.getByTestId('access-key-view').click();
    await expectToast(page, 'error', /Too many reveals — try again in \d+ s/);
    await expect(page.getByTestId('access-key-value')).toHaveCount(0);
    await page.evaluate(() => (window as unknown as { __opmMockRevealReset: () => void }).__opmMockRevealReset());
  });
});

test.describe('install commands', () => {
  test('create shows the command at once; View again; unattended flags; used tokens show their state', async ({ page }) => {
    await login(page, 'admin', `${E}/setup`);
    const site = page.getByTestId('setup-site-main');
    // pre-#84 active command → not stored; Regenerate replaces it
    await expect(site.getByTestId('install-cmd-not-stored')).toBeVisible();
    await site.getByTestId('install-cmd-regenerate').click();
    await page.getByTestId('install-cmd-confirm').getByTestId('confirm-ok').click();
    const val = site.getByTestId('install-cmd-value');
    await expect(val).toContainText('Authorization: Bearer opi_');
    await expect(val).toContainText('| sudo sh');
    await expect(site.getByTestId('install-cmd-countdown')).toBeVisible();
    await expect(site.getByTestId('install-deps')).toHaveText('Asks before installing Docker 28.5.2 when it is missing (uses your existing Docker ≥ 20.10 without asking) · agent 0.10.0-10081200');
    // §21 #91 Unattended install: OPM_YES=1 / OPM_INSTALL_DOCKER=0|1 go after sudo, in the shown + copied command
    await site.getByTestId('unattended').locator('summary').click();
    const yes = site.getByTestId('opt-yes');
    const docker = site.getByTestId('opt-docker');
    const check = async (want: string) => {
      await expect(val).toContainText(want);
      await site.getByTestId('install-cmd-copy').click();
      const copied = await page.evaluate(() => navigator.clipboard.readText());
      expect(copied).toBe(await val.textContent());
      expect(copied).toContain(want);
    };
    await yes.click();
    await expect(yes).toHaveAttribute('data-checked', 'true');
    await check('| sudo OPM_YES=1 sh');
    await docker.getByRole('radio', { name: /^Skip/ }).click();
    await check('| sudo OPM_YES=1 OPM_INSTALL_DOCKER=0 sh');
    await docker.getByRole('radio', { name: /^Install/ }).click();
    await check('| sudo OPM_YES=1 OPM_INSTALL_DOCKER=1 sh');
    await yes.click();
    await check('| sudo OPM_INSTALL_DOCKER=1 sh');
    await docker.getByRole('radio', { name: /^Ask/ }).click();
    await check('| sudo sh');
    const cmd = await val.textContent();
    // hide → View again (logged) → the same command
    await site.getByTestId('install-cmd-hide').click();
    await expect(site.getByTestId('install-cmd-masked')).toBeVisible();
    await site.getByTestId('install-cmd-view').click();
    await expect(val).toHaveText(cmd!);
    await nav(page, `${E}/activities?type=install-token.revealed`);
    await expect(page.getByTestId('activity-row').first()).toContainText('install-token.revealed');
    await nav(page, '/app/audit-log?action=deployment.install-command-revealed');
    await expect(page.getByTestId('audit-row').first()).toHaveAttribute('data-action', 'deployment.install-command-revealed');

    // ACME: enrolled sites, used tokens → state badge, no value
    await nav(page, `${D}/setup`);
    const app = page.getByTestId('setup-site-app');
    await expect(app.getByTestId('token-row').first()).toContainText('used');
    await expect(app.getByTestId('install-cmd')).toHaveCount(0);
  });

  test('a command revoked elsewhere → View says gone and the page refreshes', async ({ page }) => {
    await login(page, 'admin', `${E}/setup`);
    const site = page.getByTestId('setup-site-main');
    await site.getByTestId('token-row').filter({ hasText: 'active' }).getByTestId('revoke-token').click();
    await page.getByTestId('confirm-ok').click();
    await site.getByTestId('create-install').click();
    await expect(site.getByTestId('install-cmd-value')).toBeVisible();
    await site.getByTestId('install-cmd-hide').click();
    const hash = await site.getByTestId('install-command').getAttribute('data-token');
    await page.evaluate(
      async ([id, h]) => {
        await (window as unknown as { __opmMockFetch: typeof fetch }).__opmMockFetch(`${location.origin}/v1/deployments/${id}/install-tokens/${h}`, { method: 'DELETE', headers: { Authorization: 'Bearer mock:uAdmin00000000000001' } });
      },
      [SEED.deploymentEmpty, hash!],
    );
    await site.getByTestId('install-cmd-view').click();
    await expectToast(page, 'error', 'revoked');
    await expect(site.getByTestId('install-command')).toHaveCount(0);
    await expect(site.getByTestId('create-install')).toBeVisible();
  });
});

test.describe('secrets', () => {
  test('deployment: View (60 s) + Generate new (a change, committed); template: set per deployment', async ({ page }) => {
    await login(page, 'admin', `${D}/secrets`);
    await expect(page.getByTestId('reveal-api-token-length')).toHaveText('24 chars');
    await page.getByTestId('reveal-api-token-view').click();
    const v = page.getByTestId('reveal-api-token-value');
    await expect(v).toHaveText(/^[A-Za-z0-9]{24}$/);
    expect(await left(page, 'reveal-api-token')).toBeGreaterThanOrEqual(58);
    const before = await v.textContent();
    // §21 #105 no Regenerate in the viewer: "Generate new" is a pending change, the commit generates the value
    await expect(page.getByTestId('reveal-api-token-regenerate')).toHaveCount(0);
    await page.getByTestId('menu-api-token').getByRole('button').first().click();
    await page.getByTestId('m-generate').click();
    await page.getByTestId('generate-secret').getByTestId('confirm-ok').click();
    await expect(page.getByTestId('pending-api-token')).toContainText('generated at commit');
    await commitAll(page, 'Rotate the API token');
    await page.reload();
    await page.getByTestId('reveal-api-token-view').click();
    await expect(v).toHaveText(/^[A-Za-z0-9]{24}$/);
    await expect(v).not.toHaveText(before!);
    await nav(page, `${D}/activities?type=secret.`);
    await expect(page.getByTestId('activity-row').filter({ hasText: 'secret.revealed' }).first()).toBeVisible();

    await nav(page, `/app/templates/${SEED.template}/secrets`);
    await expect(page.getByTestId('tpl-value-api-token')).toHaveText('set per deployment');
    await expect(page.getByTestId('reveal-api-token-view')).toHaveCount(0);
  });
});

test.describe('registries + accounts', () => {
  test('registry password: View for 60 s (logged)', async ({ page }) => {
    await login(page, 'admin', '/app/registries');
    await page.getByTestId(`reveal-${SEED.registryAr}-view`).click();
    await expect(page.getByTestId(`reveal-${SEED.registryAr}-value`)).toContainText('service_account');
    expect(await left(page, `reveal-${SEED.registryAr}`)).toBeGreaterThanOrEqual(58);
    await nav(page, '/app/audit-log?action=registry.password-revealed');
    await expect(page.getByTestId('audit-row').first()).toHaveAttribute('data-action', 'registry.password-revealed');
  });

  test('account password: View, Set new password… (typed ACCEPT), Reset link…; hidden for non-admins', async ({ page }) => {
    await login(page, 'admin', `/app/accounts/${SEED.uidViewer}`);
    const panel = page.getByTestId('password-panel');
    await expect(panel.getByTestId('password-meta')).toContainText('by');
    await panel.getByTestId('account-password-view').click();
    await expect(panel.getByTestId('account-password-value')).toHaveText('viewer123');
    await expect(panel.getByTestId('account-password-countdown')).toBeVisible();

    await nav(page, `/app/accounts/${SEED.uidOps}`);
    await expect(panel.getByTestId('account-password-not-stored')).toHaveText('No password stored — set a new one');
    await panel.getByTestId('account-password-regenerate').click();
    const dlg = page.getByTestId('account-password-confirm');
    await expect(dlg).toContainText('signed out');
    await expect(dlg.getByTestId('confirm-ok')).toBeDisabled();
    await dlg.getByTestId('typed-confirm').fill('ACCEPT');
    await dlg.getByTestId('confirm-ok').click();
    await expect(panel.getByTestId('account-password-value')).toHaveText(/^[A-Za-z0-9]{20}$/);
    const pw = await panel.getByTestId('account-password-value').textContent();
    await panel.getByTestId('account-password-hide').click();
    await panel.getByTestId('account-password-view').click();
    await expect(panel.getByTestId('account-password-value')).toHaveText(pw!);

    await expect(panel.getByTestId('reset-link-not-stored')).toHaveText('No reset link');
    await panel.getByTestId('reset-link-regenerate').click();
    await page.getByTestId('reset-link-confirm').getByTestId('confirm-ok').click();
    await expect(panel.getByTestId('reset-link-value')).toContainText('mode=resetPassword');
    const link = await panel.getByTestId('reset-link-value').textContent();
    await expect(panel.getByTestId('reset-link-meta')).toContainText('expires');
    await panel.getByTestId('reset-link-hide').click();
    await panel.getByTestId('reset-link-view').click();
    await expect(panel.getByTestId('reset-link-value')).toHaveText(link!);

    await nav(page, `/app/audit-log?action=account.`);
    await expect(page.getByTestId('audit-row').first()).toBeVisible();
    const actions = await page.getByTestId('audit-row').evaluateAll((rows) => rows.map((r) => r.getAttribute('data-action')));
    for (const a of ['account.password-revealed', 'account.password-set', 'account.reset-link-created', 'account.reset-link-revealed']) expect(actions).toContain(a);
  });

  test('new account: the generated password shows in RevealValue (no set-password link); viewer sees no password panel', async ({ page }) => {
    await login(page, 'admin', '/app/accounts');
    await page.getByTestId('new-account').click();
    await page.getByTestId('acc-name').fill('Gita G');
    await page.getByTestId('acc-email').fill('gita@example.com');
    await page.getByTestId('acc-save').click();
    await expect(page.getByTestId('created-password-value')).toHaveText(/^[A-Za-z0-9]{20}$/);
    const pw = await page.getByTestId('created-password-value').textContent();
    await expect(page.getByTestId('link-modal')).not.toContainText('resetPassword');
    await page.getByTestId('link-continue').click();
    await page.waitForURL(/\/app\/accounts\/[A-Za-z0-9]{20}$/);
    await page.getByTestId('account-password-view').click();
    await expect(page.getByTestId('account-password-value')).toHaveText(pw!);
    // the person can log in with it (mock auth)
    const ok = await page.evaluate(([e, p]) => (window as unknown as { __opmMockLogin: (e: string, p: string) => string | null }).__opmMockLogin(e!, p!) !== null, ['gita@example.com', pw!]);
    expect(ok).toBe(true);
  });

  test('viewer: no password panel on accounts', async ({ page }) => {
    await login(page, 'viewer', `/app/accounts/${SEED.uidOps}`);
    await expect(page.getByTestId('page-title')).toHaveText('Omar Ops');
    await expect(page.getByTestId('password-panel')).toHaveCount(0);
  });
});
