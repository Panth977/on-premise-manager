/**
 * §21 #104 screenshots → screenshots/br-*.png (light, dark, 390 px): Compare (conflict + removals), History, Commit
 * dialog, Agent branches, New deployment from. Skipped unless OPM_SCREENSHOTS=1.
 */
import { expect, test, type Page } from '@playwright/test';
import { login, SEED, expectToast, pickCombo } from './helpers';

test.skip(!process.env.OPM_SCREENSHOTS, 'set OPM_SCREENSHOTS=1');
const D = `/app/deployments/${SEED.deployment}`;

async function setup(page: Page, theme: 'light' | 'dark') {
  await page.addInitScript((t) => localStorage.setItem('opm.theme', t), theme);
  await login(page, 'admin', `${D}/variables`);
  await page.evaluate(() => (window as unknown as { __opmMockReset: () => void }).__opmMockReset());
  await page.reload();
  // ACME commits its own greeting → a conflict with Hello world's commit 2
  await page.getByTestId('val-greeting').fill('Welcome to ACME');
  await page.getByTestId('change-bar-commit').click();
  await page.getByTestId('commit-dialog').getByTestId('commit-message').fill('ACME greeting, yellow theme and plant presets\n\nThe plant asked for its own greeting.');
  await page.getByTestId('commit-dialog').getByTestId('commit-submit').click();
  await expect(page.getByTestId('nav-head')).toHaveText('commit 3');
}
const still = (page: Page) => page.waitForTimeout(500);

for (const [name, theme, width] of [
  ['light', 'light', 1360],
  ['dark', 'dark', 1360],
  ['m', 'light', 390],
] as const) {
  test(`br screens ${name}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width < 600 ? 844 : 900 });
    await setup(page, theme);
    const sfx = name === 'light' ? '' : `-${name}`;
    // compare: conflict + removals, conflict resolved, a removal ticked
    await page.goto(`${D}/compare`);
    const conflict = page.locator('[data-testid^="merge-item-"][data-kind="conflict"]');
    await expect(conflict).toHaveCount(1);
    const key = (await conflict.getAttribute('data-testid'))!.slice('merge-item-'.length);
    await page.getByTestId(`merge-expand-${key}`).click();
    await still(page);
    await page.screenshot({ path: `screenshots/br-compare${sfx}.png`, fullPage: true });
    // history with a commit open
    await page.goto(`${D}/history`);
    await page.getByTestId('history-open-2').click();
    await expect(page.getByTestId('history-detail')).toBeVisible();
    await still(page);
    await page.screenshot({ path: `screenshots/br-history${sfx}.png`, fullPage: true });
    // commit dialog
    await page.goto(`${D}/variables`);
    await page.getByTestId('val-greeting').fill('Welcome to the ACME plant');
    await page.getByTestId('change-bar-commit').click();
    await page.getByTestId('commit-dialog').getByTestId('commit-message').fill('Longer greeting');
    await still(page);
    await page.screenshot({ path: `screenshots/br-commit${sfx}.png` });
    await page.getByTestId('commit-cancel').click();
    // agent branches
    await page.goto(`${D}/branches`);
    await expect(page.getByTestId('agent-branches')).toBeVisible();
    await still(page);
    await page.screenshot({ path: `screenshots/br-agent-branches${sfx}.png`, fullPage: true });
    // new deployment from a deployment
    await page.goto('/app/deployments');
    await page.getByTestId('new-deployment').click();
    await page.getByTestId('dep-label').fill('ACME copy');
    await page.getByTestId('dep-src-deployment').click();
    await pickCombo(page, 'dep-deployment', SEED.deployment, 'ACME');
    await expect(page.getByTestId('dep-commit')).toBeVisible();
    await still(page);
    await page.screenshot({ path: `screenshots/br-new-deployment${sfx}.png` });
  });
}
