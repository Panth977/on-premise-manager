/**
 * §21 #102 side-by-side evidence: the SAME entity forms in the cloud (deployment draft, edit) — the agent GUI twin is
 * agent/gui/tests/gc-screens.spec.ts. Writes docs-v2/assets/gui-consistency/cloud-<entity>-<light|dark|390>.png.
 * Skipped unless OPM_GC_SHOTS=1.
 */
import { test, expect, type Page } from '@playwright/test';
import { login, SEED } from './helpers';

test.skip(!process.env.OPM_GC_SHOTS, 'set OPM_GC_SHOTS=1');
const OUT = '../docs-v2/assets/gui-consistency';
const D = `/app/deployments/${SEED.deployment}`;

async function shot(page: Page, name: string, full = false) {
  await page.addStyleTag({ content: '.toaster { display: none !important; }' });
  await page.waitForTimeout(450);
  await page.screenshot({ path: `${OUT}/cloud-${name}.png`, fullPage: full });
}
async function all(page: Page, sfx: string) {
  await page.goto(`${D}/sites/app/containers/hello-api`);
  await expect(page.getByTestId('container-form')).toBeVisible();
  await shot(page, `container-${sfx}`, true);

  await page.goto(`${D}/sites/app/volumes`);
  await page.getByTestId('vol-edit-hello-api-data').click();
  await expect(page.getByTestId('volume-modal')).toBeVisible();
  await shot(page, `volume-${sfx}`);
  await page.keyboard.press('Escape');

  await page.goto(`${D}/sites/app/edge/route?id=api`);
  await expect(page.getByTestId('route-form')).toBeVisible();
  await shot(page, `route-${sfx}`, true);

  await page.goto(`${D}/variables`);
  await page.getByRole('button', { name: 'Edit theme' }).click();
  await expect(page.getByTestId('variable-drawer')).toBeVisible();
  await shot(page, `variable-${sfx}`);
  await page.keyboard.press('Escape');

  await page.goto(`${D}/secrets`);
  await page.getByTestId('menu-api-token').click();
  await page.getByTestId('m-edit').click();
  await expect(page.getByTestId('secret-drawer')).toBeVisible();
  await shot(page, `secret-${sfx}`);
  await page.keyboard.press('Escape');

  await page.goto(`${D}/sites/db/devtools`);
  await page.getByTestId('dt-edit-postgres').click();
  await expect(page.getByTestId('devtool-drawer')).toBeVisible();
  await shot(page, `preset-${sfx}`);
}

test('gc cloud light', async ({ page }) => {
  await login(page, 'admin', `${D}/variables`);
  await all(page, 'light');
});
test('gc cloud dark', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('opm.theme', 'dark'));
  await login(page, 'admin', `${D}/variables`);
  await all(page, 'dark');
});
test('gc cloud 390', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await login(page, 'admin', `${D}/variables`);
  await all(page, '390');
});
