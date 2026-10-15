import { test, expect, type Page } from '@playwright/test';
import { login, SEED, nav } from './helpers';

/**
 * Round D navigation: template + deployment scopes use a sidebar with a Sites tree (no tab rows), one route per site
 * section, redirects for /sites/{site} and old ?tab= links, breadcrumb + page header, no top bar in inner scopes,
 * the account menu + theme toggle in the sidebar footer, a drawer at 390 px with its toggle in the breadcrumb row.
 */
const TPL = `/app/templates/${SEED.template}`;
const DEP = `/app/deployments/${SEED.deployment}`;
const SHOTS = 'screenshots';

async function crumbs(page: Page) {
  return (await page.getByTestId('breadcrumb').locator('li').allInnerTexts()).map((t) => t.trim());
}

test.describe('round D: sidebar navigation', () => {
  test('template scope: sidebar + Sites tree, no tab rows, breadcrumb, sections as routes', async ({ page }) => {
    await login(page, 'admin', TPL);
    await expect(page.getByTestId('exit-template')).toBeVisible();
    await expect(page.getByTestId('tpl-name')).toHaveText('Hello world');
    for (const id of ['nav-overview', 'nav-variables', 'nav-secrets', 'nav-files', 'nav-sites']) await expect(page.getByTestId(id)).toBeVisible();
    // no tab rows any more
    await expect(page.locator('[role="tablist"]')).toHaveCount(0);
    expect(await crumbs(page)).toEqual(['Templates', 'Hello world', 'Overview']);
    // the tree: sites collapsed until opened; a site expands to Containers (+ container pages) · Edge · Volumes · Settings
    await page.getByTestId('tree-toggle-app').click();
    for (const id of ['tree-app-containers', 'tree-app-c-hello-api', 'tree-app-c-hello-web', 'tree-app-edge', 'tree-app-volumes', 'tree-app-settings']) await expect(page.getByTestId(id)).toBeVisible();
    await page.getByTestId('tree-app-edge').click();
    await page.waitForURL(`**${TPL}/sites/app/edge`);
    await expect(page.getByTestId('page-title')).toHaveText('Edge');
    expect(await crumbs(page)).toEqual(['Templates', 'Hello world', 'Sites', 'app', 'Edge']);
    await expect(page.getByTestId('tree-app-edge')).toHaveClass(/\bon\b/);
    await page.getByTestId('tree-app-c-hello-api').click();
    await page.waitForURL(`**${TPL}/sites/app/containers/hello-api`);
    expect(await crumbs(page)).toEqual(['Templates', 'Hello world', 'Sites', 'app', 'Containers', 'hello-api']);
    // breadcrumb links go up
    await page.getByTestId('breadcrumb').getByRole('link', { name: 'Containers' }).click();
    await page.waitForURL(`**${TPL}/sites/app/containers`);
    await expect(page.getByTestId('containers-table')).toBeVisible();
    // the expanded state is remembered per template (localStorage)
    await page.getByTestId('tree-toggle-db').click();
    await expect(page.getByTestId('tree-db-volumes')).toBeVisible();
    await nav(page, `${TPL}/variables`);
    await expect(page.getByTestId('tree-db-volumes')).toBeVisible();
    await expect(page.getByTestId('tree-app-volumes')).toBeVisible();
    await page.getByTestId('tree-toggle-db').click();
    await page.reload();
    await expect(page.getByTestId('tree-app-edge')).toBeVisible();
    await expect(page.getByTestId('tree-db-volumes')).toHaveCount(0);
    await page.screenshot({ path: `${SHOTS}/d-mock-nav-template.png` });
  });

  test('deep links, /sites/{site} and old ?tab= links redirect to the section', async ({ page }) => {
    await login(page, 'admin', `${TPL}/sites/db/volumes`);
    await expect(page.getByTestId('page-title')).toHaveText('Volumes');
    await expect(page.getByTestId('tree-db-volumes')).toHaveClass(/\bon\b/); // the current site is expanded
    await nav(page, `${TPL}/sites/app?tab=edge`);
    await page.waitForURL(`**${TPL}/sites/app/edge`);
    await nav(page, `${TPL}/sites/app?tab=site`);
    await page.waitForURL(`**${TPL}/sites/app/settings`);
    await expect(page.getByTestId('agent-port')).toBeVisible();
    await nav(page, `${TPL}/sites/db`);
    await page.waitForURL(`**${TPL}/sites/db/containers`);
    await nav(page, `${DEP}/sites/app?tab=containers`);
    await page.waitForURL(`**${DEP}/sites/app/containers`);
    await expect(page.getByTestId('containers-table')).toBeVisible();
    await nav(page, `${DEP}/sites/db/volumes`);
    await expect(page.getByTestId('volume-row-redis-data')).toBeVisible();
  });

  test('deployment scope: Sites tree in the deployment sidebar, no top bar', async ({ page }) => {
    await login(page, 'admin', DEP);
    await expect(page.getByTestId('dep-name')).toHaveText('ACME plant');
    await expect(page.getByTestId('mobile-top')).toHaveCount(0);
    await expect(page.locator('header.top')).toHaveCount(0);
    // the main area starts with the breadcrumb row
    const row = (await page.getByTestId('breadcrumb-row').boundingBox())!;
    expect(row.y).toBeLessThan(12);
    expect(await crumbs(page)).toEqual(['Deployments', 'ACME plant', 'Overview']);
    await page.getByTestId('tree-toggle-db').click();
    await page.getByTestId('tree-db-volumes').click();
    await page.waitForURL(`**${DEP}/sites/db/volumes`);
    await expect(page.getByTestId('draft-banner')).toBeVisible();
    await expect(page.getByTestId('volumes-table')).toContainText('redis-data');
    expect(await crumbs(page)).toEqual(['Deployments', 'ACME plant', 'Sites', 'db', 'Volumes']);
    await page.getByTestId('tree-db-settings').click();
    await page.waitForURL(`**${DEP}/sites/db/settings`);
    await expect(page.getByTestId('change-bar-commit')).toHaveText(/Commit…/);
    await page.screenshot({ path: `${SHOTS}/d-mock-nav-deployment.png` });
  });

  test('root scope: account menu + theme in the sidebar footer, no top bar on desktop', async ({ page }) => {
    await login(page, 'admin', '/app/deployments');
    await expect(page.getByTestId('mobile-top')).toBeHidden();
    const foot = page.getByTestId('sidebar-foot');
    await expect(foot.getByTestId('account-chip')).toBeVisible();
    await expect(foot.getByTestId('theme-toggle')).toBeVisible();
    await expect(page.getByTestId('account-chip')).toHaveCount(1);
    await foot.getByTestId('theme-toggle').click();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
    await page.screenshot({ path: `${SHOTS}/d-mock-nav-root.png` });
  });

  test('§21 #105 the change set follows the person: moving to another site keeps edits (no prompt)', async ({ page }) => {
    await login(page, 'admin', `${TPL}/sites/app/settings`);
    await page.getByTestId('agent-port').fill('7099');
    await expect(page.getByTestId('dirty-dot')).toBeVisible();
    await expect(page.getByTestId('tree-dirty-app')).toBeVisible();
    await page.getByTestId('tree-app-volumes').click();
    await page.waitForURL(`**${TPL}/sites/app/volumes`);
    await expect(page.getByTestId('leave-modal')).toHaveCount(0);
    await expect(page.getByTestId('dirty-dot')).toBeVisible();
    // another site (sidebar) → no prompt; the app site keeps its pending change
    await page.getByTestId('tree-toggle-db').click();
    await page.getByTestId('tree-db-edge').click();
    await expect(page).toHaveURL(new RegExp(`${TPL}/sites/db/edge$`));
    await expect(page.getByTestId('leave-modal')).toHaveCount(0);
    await expect(page.getByTestId('tree-dirty-app')).toBeVisible();
    // an invalid agent port → error badge on the tree node of the site and of the Settings section
    await nav(page, `${TPL}/sites/app/settings`);
    await expect(page.getByTestId('agent-port')).toHaveValue('7099');
    await page.getByTestId('agent-port').fill('80');
    await expect(page.getByTestId('tree-badge-app')).toHaveClass(/red/);
    await expect(page.getByTestId('tree-app-settings').locator('.nav-cnt.red')).toBeVisible();
  });

  for (const w of [390, 1440]) {
    test(`My profile opened from the sidebar footer in an inner scope is on top (${w}px)`, async ({ page }) => {
      await page.setViewportSize({ width: w, height: w === 390 ? 844 : 900 });
      await login(page, 'admin', `${TPL}/sites/app/containers`);
      if (w === 390) await page.getByTestId('nav-toggle').click();
      await page.getByTestId('sidebar-foot').getByTestId('account-chip').click();
      await expect(page.getByTestId('account-menu')).toBeVisible();
      await page.getByTestId('menu-profile').click();
      const m = page.getByTestId('profile-modal');
      await expect(m).toBeVisible();
      await page.waitForTimeout(250);
      const box = (await m.boundingBox())!;
      const hit = await page.evaluate(({ x, y }) => !!document.elementFromPoint(x, y)?.closest('[data-testid="profile-modal"]'), { x: box.x + box.width / 2, y: box.y + box.height / 2 });
      expect(hit).toBe(true);
      expect(await m.evaluate((el) => el.closest('aside') === null)).toBe(true);
      if (w === 390) await page.screenshot({ path: `${SHOTS}/d-mock-nav-profile-390.png` });
    });
  }

  test('390 px: the sidebar is a drawer, its toggle sits in the breadcrumb row', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await login(page, 'admin', `${TPL}/sites/app/volumes`);
    const toggle = page.getByTestId('breadcrumb-row').getByTestId('nav-toggle');
    await expect(toggle).toBeVisible();
    await expect(page.getByTestId('mobile-top')).toHaveCount(0);
    const panel = page.getByTestId('sidebar-panel');
    const closed = (await panel.boundingBox())!;
    expect(closed.x + closed.width).toBeLessThanOrEqual(1);
    await toggle.click();
    await page.waitForTimeout(300);
    const open = (await panel.boundingBox())!;
    expect(open.x).toBeGreaterThanOrEqual(0);
    await expect(page.getByTestId('tree-app-volumes')).toBeVisible();
    await page.screenshot({ path: `${SHOTS}/d-mock-nav-mobile.png` });
    await page.getByTestId('tree-app-edge').click();
    await page.waitForURL(`**${TPL}/sites/app/edge`);
    await page.waitForTimeout(300);
    expect(((await panel.boundingBox())!).x + closed.width).toBeLessThanOrEqual(1);
    // no horizontal page scroll
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    // root scope on a phone: a slim bar with the toggle
    await nav(page, '/app/services');
    await expect(page.getByTestId('mobile-top')).toBeVisible();
    await expect(page.getByTestId('mobile-top').getByTestId('nav-toggle')).toBeVisible();
  });
});
