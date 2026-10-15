/** 390 px: the sidebar is a drawer with the Sites tree (the cloud sidebar); picking a page closes it; no sideways scroll. */
import { test, expect } from './fixtures';
import { login, open, shot } from './helpers';

test('390 px: drawer with the Sites tree; picking a page closes the drawer; pages fit', async ({ page }) => {
  await login(page, '/_opm/app/app/status');
  const menu = page.getByTestId('breadcrumb').getByRole('button', { name: 'Menu' });
  await menu.click();
  const side = page.getByRole('navigation', { name: 'Main' });
  await expect(side).toBeInViewport();
  await expect(page.getByTestId('sites-tree')).toBeVisible();
  await expect(page.getByTestId('sidebar-resizer')).toBeHidden(); // a drawer is not resizable
  await shot(page, 'nav2-drawer-m', false);
  // another site's page from the tree inside the drawer
  await page.getByTestId('tree-toggle-db').click();
  await expect(page.getByTestId('tree-db-containers')).toBeVisible();
  await shot(page, 'nav2-drawer-open-m', false);
  await page.getByTestId('tree-db-containers').click();
  await page.waitForURL(/\/_opm\/app\/db\/containers$/);
  await expect(side).not.toBeInViewport();
  for (const p of ['/_opm/app/db/containers', '/_opm/app/app/containers/hello-api', '/_opm/app/app/containers/hello-api?tab=logs', '/_opm/app/app/diff', '/_opm/app/app/status', '/_opm/app/app/config/containers', '/_opm/app/*/network', '/_opm/app/app/agent?tab=shell', '/_opm/app/app/debug?tab=agent', '/_opm/app/app/network?tab=access']) {
    await open(page, p);
    await page.waitForTimeout(400);
    const over = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    expect(over, `horizontal overflow on ${p}`).toBeLessThanOrEqual(1);
  }
  await open(page, '/_opm/app/app/containers/hello-api');
  await shot(page, 'nav2-container-m', false);
  await open(page, '/_opm/app/app/diff');
  await shot(page, 'nav2-diff-m', false);
  await open(page, '/_opm/app/*/network');
  await shot(page, 'nav2-choose-site-m', false);
});
