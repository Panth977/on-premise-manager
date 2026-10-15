import { test, expect } from './fixtures';
import { knobs, login, open, shot } from './helpers';

/**
 * Visual redesign (2026-10-08): the agent GUI uses the cloud console's design tokens + component look.
 * Screenshots → screenshots/redesign-*.png (light, dark, 390 px). Compare with ../../frontend/screenshots/d-nav-*.png.
 */
const PAGES: [string, string][] = [
  ['sites', '/_opm/app'],
  ['updates', '/_opm/app/updates'],
  ['backups', '/_opm/app/backups'],
  ['status', '/_opm/app/app/status'],
  ['activities', '/_opm/app/app/activities'],
  ['debug', '/_opm/app/app/debug'],
  ['containers', '/_opm/app/app/config/containers'],
  ['volumes', '/_opm/app/app/config/volumes'],
  ['edge', '/_opm/app/app/config/edge'],
  ['variables', '/_opm/app/variables'],
  ['secrets', '/_opm/app/secrets'],
  ['files', '/_opm/app/files'],
  ['transfers', '/_opm/app/app/transfers'],
  ['internet', '/_opm/app/internet'],
];

for (const scheme of ['light', 'dark'] as const) {
  test(`redesign screenshots · ${scheme}`, async ({ page }) => {
    test.setTimeout(180_000);
    await page.emulateMedia({ colorScheme: scheme });
    await login(page);
    // the sidebar: product mark, deployment card, status block (head · sites · cloud), nav groups, account footer
    const side = page.getByRole('navigation', { name: 'Main' });
    await expect(side).toContainText('On-Prem Manager');
    await expect(page.getByTestId('status-strip')).toContainText('Hello World');
    await expect(page.getByTestId('page-title')).toHaveText('Sites');
    for (const [name, path] of PAGES) {
      await open(page, path);
      await expect(page.getByTestId('page-title')).toBeVisible();
      await shot(page, `redesign-${scheme}-${name}`, false);
    }
    // §21 #104 Branch: cloud head vs this site (replaces the proposal detail)
    await open(page, '/_opm/app/app/branch');
    await expect(page.getByTestId('cloud-card')).toBeVisible();
    await shot(page, `redesign-${scheme}-proposal`);
    // container editor
    await open(page, '/_opm/app/app/config/containers/hello-api');
    await shot(page, `redesign-${scheme}-container`, false);
  });
}

test('redesign screenshots · login + actor popup', async ({ page }) => {
  await page.goto('/_opm/app');
  await page.waitForURL(/\/_opm\/login/);
  await shot(page, 'redesign-light-login', false);
  await page.emulateMedia({ colorScheme: 'dark' });
  await shot(page, 'redesign-dark-login', false);
  await page.emulateMedia({ colorScheme: 'light' });
  await page.getByTestId('access-key').fill('K7QF-2M3X-PL4D-6WZT-R3VB-6HJN');
  await page.getByRole('button', { name: 'Log in' }).click();
  await expect(page.getByTestId('actor-popup')).toBeVisible();
  await shot(page, 'redesign-light-actor', false);
});

test('redesign screenshots · empty states (fresh install)', async ({ page }) => {
  await knobs(page, { 'opm.mock.noHead': '1' });
  await login(page, '/_opm/app/updates');
  await expect(page.getByTestId('empty-state')).toContainText('No cloud commit yet');
  await expect(page.getByTestId('console-diff-link')).toHaveAttribute('href', /^https:\/\/opm\.example\.com\/app\/deployments\/.+\/history$/);
  await shot(page, 'redesign-light-updates-empty', false);
  await open(page, '/_opm/app/variables');
  await expect(page.getByTestId('next-steps')).toBeVisible();
  await shot(page, 'redesign-light-nohead', false);
  await page.emulateMedia({ colorScheme: 'dark' });
  await open(page, '/_opm/app/updates');
  await shot(page, 'redesign-dark-updates-empty', false);
});

test('redesign screenshots · 390 px', async ({ page }) => {
  test.setTimeout(120_000);
  await page.setViewportSize({ width: 390, height: 844 });
  await login(page);
  await shot(page, 'redesign-m-sites', false);
  await page.getByTestId('breadcrumb').getByRole('button', { name: 'Menu' }).click();
  await expect(page.getByRole('navigation', { name: 'Main' })).toBeInViewport();
  await shot(page, 'redesign-m-menu', false);
  for (const [name, path] of PAGES.slice(1, 7)) {
    await open(page, path);
    await page.waitForTimeout(300);
    const over = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    expect(over, `horizontal overflow on ${path}`).toBeLessThanOrEqual(1);
    await shot(page, `redesign-m-${name}`, false);
  }
});
