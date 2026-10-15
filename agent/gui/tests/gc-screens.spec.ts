/**
 * §21 #102 side-by-side evidence: the SAME entity forms in the agent GUI (local edit / read) — the cloud twin is
 * frontend/tests/gc-screens.spec.ts. Writes docs-v2/assets/gui-consistency/agent-<entity>-<light|dark|390>.png.
 * Skipped unless OPM_GC_SHOTS=1.
 */
import { test, expect, type Page } from '@playwright/test';
import { login, open } from './helpers';

test.skip(!process.env.OPM_GC_SHOTS, 'set OPM_GC_SHOTS=1');
const OUT = '../../docs-v2/assets/gui-consistency';
const A = '/_opm/app/app';

async function shot(page: Page, name: string, full = false) {
  await page.addStyleTag({ content: '.toasts { display: none !important; }' });
  await page.waitForTimeout(450);
  await page.screenshot({ path: `${OUT}/agent-${name}.png`, fullPage: full });
}
async function all(page: Page, sfx: string) {
  await expect(page.getByTestId('container-form')).toBeVisible();
  await shot(page, `container-${sfx}`, true);

  await open(page, `${A}/config/volumes`);
  await page.getByTestId('edit-hello-api-data').click();
  await expect(page.getByTestId('volume-modal')).toBeVisible();
  await shot(page, `volume-${sfx}`);
  await page.keyboard.press('Escape');

  await open(page, `${A}/config/edge/route?id=api`);
  await expect(page.getByTestId('route-form')).toBeVisible();
  await shot(page, `route-${sfx}`, true);

  await open(page, `${A}/variables`);
  await page.getByTestId('var-open-theme').click();
  await expect(page.getByTestId('variable-drawer')).toBeVisible();
  await shot(page, `variable-${sfx}`);
  await page.keyboard.press('Escape');

  await open(page, `${A}/secrets`);
  await page.getByTestId('sec-open-api-token').click();
  await expect(page.getByTestId('secret-drawer')).toBeVisible();
  await shot(page, `secret-${sfx}`);
  await page.keyboard.press('Escape');

  await open(page, `${A}/devtools`);
  await page.getByRole('button', { name: 'View App database' }).click();
  await expect(page.getByTestId('conn-drawer')).toBeVisible();
  await shot(page, `preset-${sfx}`);
}

test.describe('gc agent', () => {
  test('light', async ({ page }) => {
    await login(page, `${A}/config/containers/hello-api`);
    await all(page, 'light');
  });
  test('dark', async ({ page }) => {
    await page.addInitScript(() => localStorage.setItem('opm.theme', 'dark'));
    await login(page, `${A}/config/containers/hello-api`);
    await all(page, 'dark');
  });
  test('390', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await login(page, `${A}/config/containers/hello-api`);
    await all(page, '390');
  });
});
