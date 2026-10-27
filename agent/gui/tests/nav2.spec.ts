/**
 * nav: the sidebar is the cloud deployment sidebar — deployment-wide pages + Config, then the Sites tree (every site ›
 * its pages, collapsible, remembered; no site picker); the site is part of the URL (remembered per tab), "Choose a
 * site" states under "*", the Branch module (replaces Updates / Diff), redirects of every old URL. Screenshots → screenshots/nav2-*.png.
 */
import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { knobs, login, open, pickOption, shot, nav } from './helpers';

const side = (page: Page) => page.getByRole('navigation', { name: 'Main' });

test('Sites tree (same as the cloud sidebar): every site with its pages; nodes collapse and are remembered; no site picker', async ({ page }) => {
  await login(page, '/_opm/app/app/status');
  await expect(page.getByTestId('site-select')).toHaveCount(0);
  for (const s of ['app', 'db', 'northwind']) await expect(page.getByTestId(`tree-site-${s}`)).toBeVisible();
  await expect(page.getByTestId('tree-app-status')).toHaveAttribute('aria-current', 'page');
  // the site of the page is open, the others closed; the pages of a site in the cloud order
  for (const k of ['status', 'containers', 'volumes', 'devtools', 'variables', 'secrets', 'files', 'server']) await expect(page.getByTestId(`tree-app-${k}`)).toBeVisible();
  await expect(page.getByTestId('tree-db-containers')).toBeHidden();
  await expect(page.getByTestId('tree-site-app').locator(':scope > ul > li > a, :scope > ul > li > .node > a')).toHaveText([/^Status/, /^Containers/, /^Volumes/, /^Dev tools/, /^Variables/, /^Secrets/, /^Files/, /^Server/]);
  // §21 #104 Branch (§21 #126: under Server): 3 uncommitted + 2 behind
  await page.getByTestId('tree-toggle-app-server').click();
  await expect(page.getByTestId('branch-badge')).toHaveText('5');
  await page.getByTestId('tree-toggle-app-server').click();
  // Containers open by default (one leaf per container), Volumes closed until opened — then remembered
  await expect(page.getByTestId('tree-app-c-hello-api')).toBeVisible();
  await expect(page.getByTestId('tree-app-v-api-env')).toBeHidden();
  await page.getByTestId('tree-toggle-app-volumes').click();
  await expect(page.getByTestId('tree-app-v-api-env')).toHaveAttribute('href', '/_opm/app/app/volumes?v=api-env');
  await page.getByTestId('tree-toggle-app-containers').click();
  await expect(page.getByTestId('tree-app-c-hello-api')).toBeHidden();
  await page.reload();
  await page.getByTestId('actor-email').fill('tester@acme.example');
  await page.getByTestId('actor-continue').click();
  await expect(page.getByTestId('tree-app-v-api-env')).toBeVisible();
  await expect(page.getByTestId('tree-app-c-hello-api')).toBeHidden();
  // keyboard: the twisty is a button
  await expect(page.getByTestId('actor-email')).toBeHidden(); // the person popup gives the focus back when it closes
  await page.getByTestId('tree-toggle-db').focus();
  await page.keyboard.press('Enter');
  await expect(page.getByTestId('tree-toggle-db')).toHaveAttribute('aria-expanded', 'true');
  await expect(page.getByTestId('tree-db-containers')).toBeVisible();
  // another site's pages straight from the tree (over the mesh)
  await page.getByTestId('tree-db-containers').click();
  await page.waitForURL(/\/_opm\/app\/db\/containers$/);
  await expect(page.getByTestId('tools-containers')).toContainText('redis');
  await page.getByTestId('tree-db-status').click();
  await expect(page.getByTestId('page-title')).toHaveText('Status · db');
  // Server › Docker · Network · Agent · Host shell · Debug · Tunnels · Shared files · Branch · Edge · Settings, Dev tools › tools
  await page.getByTestId('tree-toggle-db-server').click();
  for (const k of ['docker', 'network', 'agent', 'shell', 'debug', 'tunnels', 'transfers', 'branch']) await expect(page.getByTestId(`tree-db-${k}`)).toHaveAttribute('href', `/_opm/app/db/${k}`);
  for (const k of ['edge', 'settings']) await expect(page.getByTestId(`tree-db-config-${k}`)).toHaveAttribute('href', `/_opm/app/db/config/${k}`);
  await page.getByTestId('tree-toggle-db-devtools').click();
  await expect(page.getByTestId('tree-db-devtools-sql')).toHaveAttribute('href', '/_opm/app/db/devtools/sql');
  // a page inside a collapsed node opens it
  // only the site of the page opens by itself: on a db page, app is closed until one of its pages opens it
  await expect(page.getByTestId('tree-app-status')).toBeHidden();
  await open(page, '/_opm/app/app/config/edge');
  await expect(page.getByTestId('tree-app-config-edge')).toHaveAttribute('aria-current', 'page');
});

test('deployment-wide pages follow the site of the URL; the site is remembered per tab', async ({ page }) => {
  await login(page, '/_opm/app/db/containers');
  for (const k of ['activities', 'backups', 'internet']) await expect(page.getByTestId(`side-${k}`)).toHaveAttribute('href', `/_opm/app/db/${k}`);
  // §21 #126 per site: Variables · Secrets · Files are in each site's tree
  for (const k of ['variables', 'secrets', 'files']) await expect(page.getByTestId(`tree-db-${k}`)).toHaveAttribute('href', `/_opm/app/db/${k}`);
  await nav(page, 'sites');
  await page.waitForURL(/\/_opm\/app\/db\/sites$/);
  await expect(page.getByTestId('tile-db')).toHaveAttribute('aria-current', 'true');
  // remembered per tab: /_opm/app and old URLs land on the last site
  await page.goto('/_opm/app');
  await page.waitForURL(/\/_opm\/app\/db\/sites$/);
  await page.getByTestId('actor-email').fill('tester@acme.example');
  await page.getByTestId('actor-continue').click();
  await page.goto('/_opm/app/variables');
  await page.waitForURL(/\/_opm\/app\/db\/variables$/);
  expect(await page.evaluate(() => sessionStorage.getItem('opm.site'))).toBe('db');
  // a new tab starts on this site
  const p2 = await page.context().newPage();
  await p2.goto('/_opm/app');
  await p2.waitForURL(/\/_opm\/app\/app\/sites$/);
});

test('"*" (All sites): one-site modules show "Choose a site" with an inline chooser; merged modules still work', async ({ page }) => {
  await login(page, '/_opm/app/*/transfers');
  const cases: [string, RegExp][] = [
    ['/_opm/app/*/transfers', /Choose a site to see shared files/],
    ['/_opm/app/*/network', /Choose a site to see network checks/],
    ['/_opm/app/*/containers/hello-api', /Choose a site to see container hello-api/],
    ['/_opm/app/*/agent?tab=shell', /Choose a site to see a host shell/],
    ['/_opm/app/*/debug?tab=agent', /Choose a site to see the live agent log/],
    ['/_opm/app/*/config/edge', /Choose a site to see its edge/],
  ];
  for (const [url, text] of cases) {
    if (url !== cases[0][0]) await open(page, url);
    await expect(page.getByTestId('choose-site'), url).toContainText(text);
    await expect(page.getByTestId('choose-site-select')).toBeVisible();
  }
  await shot(page, 'nav2-choose-site-light', false);
  // "This site" shortcut
  await open(page, '/_opm/app/*/transfers');
  await page.getByTestId('choose-this-site').click();
  await page.waitForURL(/\/_opm\/app\/app\/transfers$/);
  // merged modules under *
  await open(page, '/_opm/app/*/containers');
  await expect(page.getByTestId('tools-containers')).toContainText('redis');
  await open(page, '/_opm/app/*/status');
  await expect(page.getByTestId('containers-status')).toContainText('redis');
  await open(page, '/_opm/app/*/config');
  await expect(page.getByTestId('config-summary')).toContainText('northwind');
});

test('Branch (§21 #104, replaces Diff): cloud vs this site; a local edit shows as unsaved, then uncommitted, with the sidebar dot', async ({ page }) => {
  await login(page, '/_opm/app/app/diff');
  await expect(page).toHaveURL(/\/app\/app\/branch$/);
  await expect(page.getByTestId('page-title')).toHaveText('Branch');
  await expect(page.getByTestId('cloud-card')).toContainText('seq 9');
  await expect(page.getByTestId('local-card')).toContainText('seq 7');
  await shot(page, 'nav2-diff-light', false);
  await nav(page, 'variables');
  await pickOption(page, 'var-theme', 'blue');
  await expect(page.getByTestId('local-edit-bar')).toContainText('1 change (not committed)');
  await expect(page.getByTestId('tree-edited-app')).toBeVisible();
  // §21 #105: the change stays in this browser (no Save); Branch counts it as "in this browser, not committed"
  await nav(page, 'branch');
  await expect(page.getByTestId('n-edits')).toHaveText('1');
});

test('Branch without a head (fresh install): cloud hint', async ({ page }) => {
  await knobs(page, { 'opm.mock.noHead': '1' });
  await login(page, '/_opm/app/app/branch');
  await expect(page.getByTestId('cloud-card')).toContainText('No cloud commit yet');
  await expect(page.getByTestId('next-steps')).toBeVisible();
  await expect(page.getByTestId('local-card')).toContainText('Nothing committed on this site yet');
});

test('old URLs redirect to the new modules', async ({ page }) => {
  await login(page, '/_opm/app/app/status');
  const go = async (from: string, to: RegExp) => {
    await page.goto(from);
    await page.waitForURL(to);
    await page.getByTestId('actor-email').fill('tester@acme.example');
    await page.getByTestId('actor-continue').click();
  };
  await go('/_opm/app/updates', /\/_opm\/app\/app\/branch$/);
  await go('/_opm/app/updates?rev=REV123', /\/_opm\/app\/app\/branch$/);
  await go('/_opm/app/app/diff?rev=REV123', /\/_opm\/app\/app\/branch$/);
  await go('/_opm/app/sites', /\/_opm\/app\/\*\/config$/);
  await expect(page.getByTestId('site-cards')).toContainText('redis');
  await go('/_opm/app/sites/db/send', /\/_opm\/app\/db\/transfers$/);
  await go('/_opm/app/sites/db/receive', /\/_opm\/app\/db\/transfers$/);
  await go('/_opm/app/db/send', /\/_opm\/app\/db\/transfers$/);
  await go('/_opm/app/app/receive', /\/_opm\/app\/app\/transfers$/);
  await go('/_opm/app/app/tools/logs?c=hello-web', /\/_opm\/app\/app\/containers\/hello-web\?tab=logs$/);
  await go('/_opm/app/app/tools/logs?src=access', /\/_opm\/app\/app\/network\?tab=access$/);
  await go('/_opm/app/app/tools/logs?src=agent', /\/_opm\/app\/app\/debug\?tab=agent$/);
  await go('/_opm/app/app/tools/terminal?c=hello-api', /\/_opm\/app\/app\/containers\/hello-api\?tab=terminal$/);
  await go('/_opm/app/app/tools/monitoring?c=hello-api', /\/_opm\/app\/app\/containers\/hello-api\?tab=monitoring$/);
  await go('/_opm/app/app/tools/monitoring', /\/_opm\/app\/app\/status$/);
  await go('/_opm/app/app/tools/containers', /\/_opm\/app\/app\/containers$/);
  await go('/_opm/app/app/tools/volumes?v=api-env', /\/_opm\/app\/app\/volumes\?v=api-env$/);
  await go('/_opm/app/app/tools/docker', /\/_opm\/app\/app\/docker$/);
  await go('/_opm/app/app/tools/network', /\/_opm\/app\/app\/network$/);
  await go('/_opm/app/variables', /\/_opm\/app\/app\/variables$/);
  await go('/_opm/app/backups', /\/_opm\/app\/app\/backups$/);
  await go('/_opm/app/internet', /\/_opm\/app\/app\/internet$/);
  await go('/_opm/app/app', /\/_opm\/app\/app\/status$/);
});

test('container page Config tab: the container editor inside the page; edits become the local working copy', async ({ page }) => {
  await login(page, '/_opm/app/app/containers/hello-api?tab=config');
  await expect(page.getByTestId('cpanel-config')).toContainText('Ports');
  await expect(page.getByTestId('cpanel-config').getByTestId('page-title')).toHaveCount(0);
  await page.getByTestId('cpanel-config').getByRole('link', { name: 'Site › Config › Containers' }).click();
  await page.waitForURL(/\/app\/app\/config\/containers\/hello-api$/);
  await expect(page.getByTestId('config-tabs').getByTestId('nav-containers')).toHaveAttribute('aria-current', 'page');
});

for (const scheme of ['light', 'dark'] as const) {
  test(`screenshots: sidebar + key pages (${scheme})`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: scheme });
    await login(page, '/_opm/app/app/containers');
    await shot(page, `nav2-containers-${scheme}`, false);
    await open(page, '/_opm/app/app/containers/hello-api');
    await shot(page, `nav2-container-${scheme}`, false);
    await open(page, '/_opm/app/app/containers/hello-api?tab=logs');
    await shot(page, `nav2-container-logs-${scheme}`, false);
    await open(page, '/_opm/app/app/diff');
    await shot(page, `nav2-diff-${scheme}`, false);
    await open(page, '/_opm/app/app/status');
    await shot(page, `nav2-status-${scheme}`, false);
    await open(page, '/_opm/app/app/config/edge');
    await shot(page, `nav2-config-${scheme}`, false);
    await open(page, '/_opm/app/*/network');
    await shot(page, `nav2-choose-site-${scheme}`, false);
  });
}
