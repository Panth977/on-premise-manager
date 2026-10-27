import { test, expect } from './fixtures';
import { expectOnTop, login, open, shot, treeLink } from './helpers';

const PAGES = ['/_opm/app', '/_opm/app/variables', '/_opm/app/secrets', '/_opm/app/updates', '/_opm/app/backups', '/_opm/app/app/status', '/_opm/app/app/activities', '/_opm/app/app/config', '/_opm/app/internet', '/_opm/app/app/transfers'];

test('390 px: no horizontal page scroll, menu drawer works', async ({ page }) => {
  await login(page);
  await shot(page, 'm-sites');
  await page.getByRole('button', { name: 'Menu' }).click();
  await expect(page.getByRole('navigation', { name: 'Main' })).toBeInViewport();
  await shot(page, 'm-menu', false);
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Backups' }).click();
  await page.waitForURL(/backups/);
  for (const p of PAGES) {
    await open(page, p);
    await page.waitForTimeout(400);
    const over = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    expect(over, `horizontal overflow on ${p}`).toBeLessThanOrEqual(1);
    await shot(page, 'm-' + (p.replace('/_opm/app', '').replace(/[^a-z0-9]+/gi, '-').replace(/^-|-$/g, '') || 'home'));
  }
});

test('390 px: Change person popup and a drawer popover are fully visible and on top; container editor fits', async ({ page }) => {
  await login(page, '/_opm/app/app/config?tab=edge');
  // round D: the account lives in the sidebar footer (drawer at 390 px; its toggle sits in the breadcrumb row)
  await page.getByTestId('breadcrumb').getByRole('button', { name: 'Menu' }).click();
  const cp = page.getByTestId('change-person');
  await cp.scrollIntoViewIfNeeded();
  await cp.click();
  await expect(page.getByTestId('actor-popup')).toBeVisible();
  await expectOnTop(page, '[data-testid="actor-popup"] .inner');
  await shot(page, 'ux-m-change-person', false);
  await page.getByTestId('actor-email').fill('mira@acme.example');
  await page.getByTestId('actor-continue').click();
  await page.getByTestId('add-host').click();
  await expectOnTop(page, '[data-testid="host-drawer"] .inner');
  await page.getByTestId('host-tls-acme').click();
  await page.getByTestId('host-challenge-dns-01').click();
  await page.getByTestId('host-drawer').getByRole('combobox', { name: 'DNS provider' }).click();
  await expectOnTop(page, '.lb:popover-open');
  await shot(page, 'ux-m-drawer-popover', false);
  await page.keyboard.press('Escape');
  await open(page, '/_opm/app/app/config/containers/hello-api');
  await page.waitForTimeout(300);
  const over = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  expect(over).toBeLessThanOrEqual(1);
  await shot(page, 'ux-m-editor');
});

test('390 px (round D): site scope without top bar, drawer toggle in the breadcrumb row, accordion drawer, volumes pages fit', async ({ page }) => {
  await login(page, '/_opm/app/app/config/volumes');
  await expect(page.locator('header.top')).toHaveCount(0);
  const menu = page.getByTestId('breadcrumb').getByRole('button', { name: 'Menu' });
  await expect(menu).toBeVisible();
  await shot(page, 'd-m-volumes');
  await menu.click();
  await expect(page.getByRole('navigation', { name: 'Main' })).toBeInViewport();
  // the Sites tree in the drawer: this site's Volumes node is open on a config page (the sections are tabs in the page)
  await expect(page.getByTestId('tree-app-volumes')).toBeVisible();
  await shot(page, 'd-nav-mobile', false);
  await page.getByTestId('actor-chip').scrollIntoViewIfNeeded();
  await expect(page.getByTestId('actor-chip')).toBeInViewport();
  await shot(page, 'd-nav-mobile-footer', false);
  await treeLink(page, 'app', 'config-settings');
  await page.waitForURL(/config\/settings$/);
  await expect(page.getByRole('navigation', { name: 'Main' })).not.toBeInViewport();
  for (const p of ['/_opm/app/files', '/_opm/app/app/config/volumes', '/_opm/app/app/config/containers/hello-api', '/_opm/app/backups']) {
    await open(page, p);
    await page.waitForTimeout(400);
    const over = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    expect(over, `horizontal overflow on ${p}`).toBeLessThanOrEqual(1);
  }
  await page.getByTestId('site-volumes').scrollIntoViewIfNeeded();
  await shot(page, 'd-m-backups', false);
  await open(page, '/_opm/app/app/config/containers/hello-api');
  await page.getByTestId('vol-row-data').scrollIntoViewIfNeeded();
  await shot(page, 'd-m-container-volumes', false);
});

test('390 px (round E): edge page, route editor and nginx importer fit without sideways scroll', async ({ page }) => {
  await login(page, '/_opm/app/northwind/config/edge/route?id=x-ez-terminal-s3');
  await expect(page.getByTestId('hdr-warn-0')).toBeVisible();
  for (const p of ['/_opm/app/northwind/config/edge/route?id=x-ez-terminal-s3', '/_opm/app/northwind/config/edge/import']) {
    if (p.endsWith('import')) {
      await page.getByTestId('config-tabs').getByTestId('nav-edge-import').click();
      await page.waitForURL(/edge\/import$/);
    }
    await page.waitForTimeout(300);
    const over = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    expect(over, `horizontal overflow on ${p}`).toBeLessThanOrEqual(1);
    await shot(page, p.endsWith('import') ? 'e-m-import' : 'e-m-route');
  }
});
