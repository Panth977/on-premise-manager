// §21 #126 every site's config is its own: Variables, Secrets and Files sit in each site's tree; a page of another site
// edits THAT site's change set and commits on that site (/_opm/api/sites/{site}/…), never on the agent you are logged in to.
import { test, expect } from './fixtures';
import { knobs, login, pickOption, treeLink } from './helpers';

const calls = (page: import('@playwright/test').Page) => page.evaluate(() => (window as unknown as { __opmMockCalls?: string[] }).__opmMockCalls ?? []);

test('tree: Variables · Secrets · Files per site; Host shell, Shared files, Branch, Edge, Settings under Server', async ({ page }) => {
  await knobs(page, { 'opm.mock.branch.clean': '1' });
  await login(page, '/_opm/app/db/variables');
  for (const s of ['app', 'db']) {
    if (s === 'app') await page.getByTestId('tree-toggle-app').click();
    for (const k of ['variables', 'secrets', 'files']) await expect(page.getByTestId(`tree-${s}-${k}`)).toBeVisible();
  }
  await expect(page.getByTestId('side-variables')).toHaveCount(0);
  await page.getByTestId('tree-toggle-db-server').click();
  for (const k of ['shell', 'transfers', 'branch', 'config-edge', 'config-settings']) await expect(page.getByTestId(`tree-db-${k}`)).toBeVisible();
  await page.getByTestId('tree-db-shell').click();
  await expect(page).toHaveURL(/\/_opm\/app\/db\/shell$/);
  await expect(page.getByRole('heading', { name: 'Host shell' })).toBeVisible();
  await expect(page.getByTestId('agent-tabs')).toHaveCount(0);
});

test("db's variables are edited and committed on db only; app's stay as they are", async ({ page }) => {
  await knobs(page, { 'opm.mock.branch.clean': '1' });
  await login(page, '/_opm/app/db/variables');
  const bar = page.getByTestId('local-edit-bar');
  await pickOption(page, 'var-theme', 'blue');
  await expect(bar).toContainText('1 change (not committed)');
  await expect(page.getByTestId('tree-edited-db')).toBeVisible();
  await expect(page.getByTestId('tree-edited-app')).toHaveCount(0);

  // app's set is its own: nothing pending there (moving inside the GUI keeps both sets)
  await page.getByTestId('tree-toggle-app').click();
  await page.getByTestId('tree-app-variables').click();
  await expect(page).toHaveURL(/\/_opm\/app\/app\/variables$/);
  await expect(page.getByTestId('local-edit-bar')).toContainText('No changes');
  await expect(page.getByTestId('var-theme')).not.toContainText('blue');

  // back on db: the edit is still there; commit it
  await treeLink(page, 'db', 'variables');
  await expect(bar).toContainText('1 change (not committed)');
  await page.getByTestId('change-bar-commit').click();
  const dlg = page.getByTestId('local-commit-dialog');
  await dlg.getByTestId('commit-message').fill('db: blue theme');
  await dlg.getByTestId('commit-submit').click();
  await expect(bar).toContainText('No changes', { timeout: 15_000 });
  await expect(page.getByTestId('var-theme')).toContainText('blue');
  await expect(page.getByTestId('tree-edited-db')).toHaveCount(0);
  const c = await calls(page);
  expect(c).toContain('GET /_opm/api/sites/db/config');
  expect(c).toContain('POST /_opm/api/sites/db/branch/commit');
  expect(c).not.toContain('POST /_opm/api/branch/commit');

  // app still has its own value
  await treeLink(page, 'app', 'variables');
  await expect(page.getByTestId('var-theme')).not.toContainText('blue');
});
