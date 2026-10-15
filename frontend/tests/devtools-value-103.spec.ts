import { test, expect, type Page } from '@playwright/test';
import { login, expectToast, SEED, commitAll, nav } from './helpers';

/**
 * §21 #103: the "Value" credential mode (a static password / token typed in the preset drawer) + the hash-filter agent
 * warning. The value is masked with a show / hide toggle, carries the non-blocking plain-text warning, is saved as
 * `{ kind: 'literal', value }`, and the Diff never shows it in clear. Sites whose agent is older than 0.13.1 get a
 * per-site warning (static value; sha256 / sha512 / mosquitto_passwd filters).
 * Screenshots: screenshots/devtools-value-{drawer,drawer-shown,diff}.png
 */
const DEP = `/app/deployments/${SEED.deployment}`;
const SHOTS = 'screenshots';
const VALUE = 'Mqtt-Static-Value-103';

async function settled(page: Page) {
  await expect(page.locator('.skel')).toHaveCount(0);
  await page.waitForTimeout(250);
}
async function patchMockDb(page: Page, fn: string) {
  await page.waitForTimeout(1200); // the mock persists with a debounce
  await page.evaluate((src) => {
    const db = JSON.parse(localStorage.getItem('opm.mock.db')!);
    new Function('db', src)(db);
    localStorage.setItem('opm.mock.db', JSON.stringify(db));
  }, fn);
  await page.reload();
}
async function writes(page: Page): Promise<{ method: string; path: string; body: string }[]> {
  return page.evaluate(() => (window as unknown as { __opmMockWrites?: { method: string; path: string; body: string }[] }).__opmMockWrites ?? []);
}
async function setAgent(page: Page, version: string) {
  await page.waitForFunction(() => typeof (window as unknown as { __opmMockAgent?: unknown }).__opmMockAgent === 'function');
  await page.evaluate(([dep, v]) => (window as unknown as { __opmMockAgent: (d: string, s: string, p: Record<string, unknown>) => boolean }).__opmMockAgent(dep!, 'db', { agentVersion: v }), [SEED.deployment, version]);
}

test.describe('§21 #103 static Value credentials + hash filters', () => {
  test('preset drawer: Value mode — masked, show toggle, warning; saved as a literal; Diff hides it; old-agent warning', async ({ page }) => {
    await login(page, 'admin', `${DEP}/sites/db/devtools`);
    await settled(page);
    await page.getByTestId('add-devtool').click();
    await page.getByTestId('devtool-kind-picker').getByTestId('dt-kind-mqtt').click();
    const d = page.getByTestId('devtool-drawer');
    await expect(d).toBeVisible();
    await d.getByTestId('dt-target-host').click();
    await d.getByTestId('dt-host').fill('10.0.0.30');
    await d.getByTestId('dt-username-input').fill('northwind');
    // the modes, in order
    await expect(d.getByTestId('dt-password-mode').getByRole('radio')).toHaveText(['None', 'Value', 'Secret', 'Expression']);
    await d.getByTestId('dt-password-mode-literal').click();
    await expect(d.getByTestId('dt-password')).toHaveAttribute('data-mode', 'literal');
    const input = d.getByTestId('dt-password-literal');
    await expect(input).toHaveAttribute('type', 'password');
    await input.fill(VALUE);
    await expect(d.getByTestId('dt-password-literal-warning')).toHaveText('Stored in plain text in the config — anyone who can read the config sees it; use a secret for production.');
    await expect(d.getByTestId('dt-summary')).toHaveCount(0); // non-blocking
    await d.getByTestId('dt-password').scrollIntoViewIfNeeded();
    await page.waitForTimeout(250);
    await page.screenshot({ path: `${SHOTS}/devtools-value-drawer.png` });
    await d.getByTestId('dt-password-show').click();
    await expect(input).toHaveAttribute('type', 'text');
    await expect(input).toHaveValue(VALUE);
    await page.screenshot({ path: `${SHOTS}/devtools-value-drawer-shown.png` });
    await d.getByTestId('dt-password-show').click();
    await expect(input).toHaveAttribute('type', 'password');
    await d.getByTestId('dt-save').click();
    await expect(d).toHaveCount(0);
    await expect(page.getByTestId('devtool-auth-mqtt')).toHaveText('user northwind · password: static value');
    await expect(page.getByTestId('devtool-row-mqtt')).not.toContainText(VALUE);
    // the old-agent warning of the site names the static value (db runs agent 0.9.0)
    await expect(page.getByTestId('devtools-agent-warning')).toContainText('a preset holds a static password / token value (mqtt.password), which needs agent 0.13.1 or newer');

    // edit again: the value comes back masked in the input (type password), editable
    await page.getByTestId('dt-edit-mqtt').click();
    await expect(page.getByTestId('devtool-drawer').getByTestId('dt-password-literal')).toHaveAttribute('type', 'password');
    await page.getByTestId('devtool-drawer').getByTestId('dt-cancel').click();

    // Compare · Uncommitted: the dev tool row never shows the value in clear (expanded sides included)
    await nav(page, `${DEP}/compare?review=working`);
    const row = page.getByTestId('merge-item-sites/db/devtools/mqtt');
    await expect(row).toHaveAttribute('data-effect', 'add');
    await page.getByTestId('merge-expand-sites/db/devtools/mqtt').click();
    await expect(page.getByTestId('merge-sides-sites/db/devtools/mqtt')).toBeVisible();
    await expect(page.locator('body')).not.toContainText(VALUE);
    await settled(page);
    await page.screenshot({ path: `${SHOTS}/devtools-value-diff.png` });
    // publish dialog: per-site warning; an agent at 0.13.0 still gets the static-value warning, 0.13.1 none
    await setAgent(page, '0.13.0-10090239');
    await page.reload();
    await expect(page.getByTestId('working-checks')).toBeVisible();
    await page.getByTestId('pending-banner-restore').click();
    await page.getByTestId('change-bar-commit').click();
    const m = page.getByTestId('commit-dialog');
    await expect(m.getByTestId('commit-warning').filter({ hasText: 'Site db runs agent 0.13.0-10090239; a preset holds a static password / token value (mqtt.password), which needs agent 0.13.1 or newer' })).toHaveCount(1);
    await expect(m.getByTestId('commit-submit')).toBeVisible(); // advisory: never blocks
    await m.getByTestId('commit-cancel').click();
    await commitAll(page);
    const put = (await writes(page)).filter((x) => x.method === 'POST' && x.path.endsWith('/commits')).at(-1)!;
    const saved = (JSON.parse(put.body) as { config: { sites: { siteSlug: string; config: { devTools: { presetSlug: string; auth: { password: unknown } }[] } }[] } }).config.sites.find((x) => x.siteSlug === 'db')!.config.devTools.find((p) => p.presetSlug === 'mqtt')!;
    expect(saved.auth.password).toEqual({ kind: 'literal', value: VALUE });
    await setAgent(page, '0.13.1-10091200');
    await page.reload();
    await expect(page.getByTestId('working-checks')).toBeVisible();
    await expect(page.getByTestId('devtools-agent-banner')).toHaveCount(0);
  });

  test('hash filters: a mosquitto_passwd / sha256 expression warns per site whose agent is older than 0.13.1', async ({ page }) => {
    await login(page, 'admin', `${DEP}/sites/db/devtools`);
    await settled(page);
    await patchMockDb(
      page,
      `const s = db.deployments['${SEED.deployment}'].draft.sites.find((x) => x.siteSlug === 'db');
       s.config.devTools.find((p) => p.presetSlug === 'redis').auth.password = { kind: 'expr', expr: '{{ secret.pg-password | sha256 }}' };`,
    );
    await settled(page);
    await setAgent(page, '0.13.0-10090239');
    await page.goto(`${DEP}/compare?review=working`);
    await expect(page.getByTestId('working-checks')).toBeVisible();
    await expect(page.getByTestId('devtools-agent-banner')).toContainText('Site db runs agent 0.13.0-10090239; the expression filter sha256 (dev tool redis) needs agent 0.13.1 or newer');
    // the preset drawer previews the hashed secret masked
    await nav(page, `${DEP}/sites/db/devtools`);
    await settled(page);
    await page.getByTestId('dt-edit-redis').click();
    const d = page.getByTestId('devtool-drawer');
    await expect(d.getByTestId('dt-password')).toHaveAttribute('data-mode', 'expr');
    await expect(d.getByTestId('dt-password-expr-preview')).toContainText('••••');
    await expect(d.getByTestId('dt-summary')).toHaveCount(0);
  });
});
