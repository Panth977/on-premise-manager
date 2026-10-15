/**
 * §21 #105 screenshots → screenshots/j-*.png (light, dark, 390 px): the change bar with pending changes, the commit
 * dialog, the restore banner after a reload, the rebase dialog (conflict). Skipped unless OPM_SCREENSHOTS=1.
 */
import { expect, test, type Page } from '@playwright/test';
import { login, SEED, nav } from './helpers';

test.skip(!process.env.OPM_SCREENSHOTS, 'set OPM_SCREENSHOTS=1');
const D = `/app/deployments/${SEED.deployment}`;
const still = (page: Page) => page.waitForTimeout(500);

async function commitBehind(page: Page, slug: string, value: string) {
  await page.evaluate(
    async ({ dep, slug, value }) => {
      const f = (window as unknown as { __opmMockFetch: typeof fetch }).__opmMockFetch;
      const h = { Authorization: 'Bearer mock:uAdmin00000000000001', 'Content-Type': 'application/json' };
      const d = await (await f(`${location.origin}/v1/deployments/${dep}`, { headers: h })).json();
      const c = d.draft;
      const config = { variables: c.variables.map((v: { variableSlug: string }) => (v.variableSlug === slug ? { ...v, value } : v)), files: c.files, sites: c.sites, secrets: c.secrets.map(({ isSet: _i, rotatedAt: _r, ...s }: { isSet: boolean; rotatedAt: unknown }) => s) };
      await f(`${location.origin}/v1/deployments/${dep}/commits`, { method: 'POST', headers: h, body: JSON.stringify({ message: 'Greeting (Ravi)', baseCommitId: d.branch.head.commitId, config }) });
      await new Promise((ok) => setTimeout(ok, 150));
    },
    { dep: SEED.deployment, slug, value },
  );
}

for (const [name, theme, width] of [
  ['light', 'light', 1360],
  ['dark', 'dark', 1360],
  ['m', 'light', 390],
] as const) {
  test(`j screens ${name}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width < 600 ? 844 : 900 });
    await page.addInitScript((t) => localStorage.setItem('opm.theme', t), theme);
    await login(page, 'admin', '/app/deployments');
    await page.evaluate(() => (window as unknown as { __opmMockReset: () => void }).__opmMockReset());
    await page.waitForTimeout(150);
    await page.goto(`${D}/variables`);
    const sfx = name === 'light' ? '' : `-${name}`;
    // several changes: a value, a container env, a secret value
    await page.getByTestId('val-greeting').fill('Welcome to the ACME plant');
    await nav(page, `${D}/sites/app/containers/hello-web`);
    await page.getByTestId('add-env').click();
    await page.getByTestId('env-name-2').fill('LOG_LEVEL');
    await page.getByTestId('env-value-x2-input').fill('debug');
    await page.getByTestId('apply-container').click();
    await nav(page, `${D}/secrets`);
    await page.getByTestId('set-api-token').click();
    await page.getByTestId('secret-value').fill('typed-secret-value');
    await page.getByTestId('secret-save').click();
    await expect(page.getByTestId('change-bar-count')).toContainText('3 changes');
    await still(page);
    await page.screenshot({ path: `screenshots/j-changebar${sfx}.png`, fullPage: true });
    // commit dialog
    await page.getByTestId('change-bar-commit').click();
    await page.getByTestId('commit-dialog').getByTestId('commit-message').fill('Plant greeting, debug logs, own API token');
    await page.getByTestId('commit-changes-toggle').click().catch(() => undefined);
    await still(page);
    await page.screenshot({ path: `screenshots/j-commit${sfx}.png` });
    await page.getByTestId('commit-cancel').click();
    // reload → restore banner
    await page.waitForTimeout(400);
    await page.reload();
    await expect(page.getByTestId('pending-banner')).toBeVisible();
    await still(page);
    await page.screenshot({ path: `screenshots/j-restore${sfx}.png` });
    // the head moves (same variable) → Review & rebase → the conflict dialog
    await commitBehind(page, 'greeting', 'Hello from Ravi');
    await page.reload();
    await page.getByTestId('pending-banner-restore').click();
    await expect(page.getByTestId('rebase-dialog')).toBeVisible();
    await still(page);
    await page.screenshot({ path: `screenshots/j-rebase${sfx}.png` });
  });
}
