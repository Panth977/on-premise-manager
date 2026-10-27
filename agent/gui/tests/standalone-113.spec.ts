// §21 #113 a site is standalone (Portainer-like): containers (any image: a docker reference, an image on the host, a
// catalog version), variables, secrets and files are added, changed and removed in the agent GUI and committed like any
// other change; Docker changes made outside OPM follow the site's drift policy.
import { test, expect } from './fixtures';
import { knobs, login, nav, treeLink } from './helpers';

const bar = (page: import('@playwright/test').Page) => page.getByTestId('local-edit-bar');

test('add a container from a docker reference, remove another, change an image to a host image → ONE commit', async ({ page }) => {
  await knobs(page, { 'opm.mock.branch.clean': '1' });
  await login(page, '/_opm/app/app/config/containers');
  await expect(page.getByTestId('add-container')).toBeEnabled();
  await page.getByTestId('add-container').click();
  const ip = page.getByTestId('image-picker');
  await ip.getByTestId('ip-tab').getByText('Docker reference').click();
  await ip.getByTestId('ip-ref').fill('nginx:1.27');
  await ip.getByTestId('ip-use-ref').click();
  // the editor of the new container (a site image: repository name + local-<hex>)
  await expect(page).toHaveURL(/\/config\/containers\/nginx$/);
  await expect(page.getByTestId('container-image')).toContainText(/^nginx:local-[0-9a-f]{12}$/);
  await expect(bar(page)).toContainText('not committed');
  // remove hello-api-2 from the list (client-side navigation: the mock agent lives in the page; hello-web is an edge target)
  await treeLink(page, 'app', 'config-settings');
  await page.getByTestId('nav-containers').click();
  await expect(page.getByTestId('container-row-nginx')).toBeVisible();
  await page.getByTestId('remove-hello-api-2').click();
  await page.getByTestId('remove-confirm').getByTestId('confirm-ok').click();
  await expect(page.getByTestId('container-row-hello-api-2')).toHaveCount(0);
  // change the image of the new container to an image on the host
  await page.getByTestId('container-row-nginx').click();
  await page.getByTestId('change-image').click();
  await ip.getByTestId('ip-tab').getByText('On this host').click();
  await ip.getByTestId('ip-host-grafana/grafana:11.2.0').click();
  await expect(page.getByTestId('container-image')).toContainText(/^grafana:local-/);
  // ONE commit of the changes
  await page.getByTestId('change-bar-commit').click();
  const dlg = page.getByTestId('local-commit-dialog');
  await dlg.getByTestId('commit-message').fill('Standalone: nginx added, hello-api-2 removed, nginx on the host image');
  await dlg.getByTestId('commit-submit').click();
  await expect(page.getByTestId('update-result')).toContainText('Applied', { timeout: 40_000 });
  await page.getByTestId('branch-tabs-history').click();
  await expect(page.getByTestId('history-row-8')).toContainText('Standalone: nginx added', { timeout: 10_000 });
});

test('a bad docker reference / an unreachable registry are explained in the picker', async ({ page }) => {
  await knobs(page, { 'opm.mock.branch.clean': '1' });
  await login(page, '/_opm/app/app/config/containers');
  await page.getByTestId('add-container').click();
  const ip = page.getByTestId('image-picker');
  await ip.getByTestId('ip-tab').getByText('Docker reference').click();
  await ip.getByTestId('ip-ref').fill('unreachable.example/team/app:2');
  await ip.getByTestId('ip-use-ref').click();
  await expect(ip.getByTestId('ip-error')).toContainText('not reachable');
});

test('add a variable, a secret and a file on the site; remove a variable → committed', async ({ page }) => {
  await knobs(page, { 'opm.mock.branch.clean': '1' });
  await login(page, '/_opm/app/app/variables');
  await page.getByTestId('add-variable').click();
  const vd = page.getByTestId('variable-drawer');
  await vd.getByTestId('var-label').fill('Shift name');
  await vd.getByTestId('var-save').click();
  await expect(page.getByTestId('variables')).toContainText('shift-name');
  await expect(bar(page)).toContainText('not committed');

  await nav(page, 'secrets');
  await page.getByTestId('add-secret').click();
  const sd = page.getByTestId('secret-drawer');
  await sd.getByTestId('sec-label').fill('Plant API key');
  await sd.getByTestId('sec-save').click();
  await expect(page.getByTestId('secrets')).toContainText('plant-api-key');
  // a required secret needs a value before the commit (validation)
  await page.getByTestId('new-plant-api-key').fill('k-123-plant');

  await nav(page, 'files');
  await page.getByTestId('add-file').click();
  const fd = page.getByTestId('file-def-drawer');
  await fd.getByTestId('file-label').fill('Shift notes');
  await fd.getByTestId('file-def-save').click();
  await expect(page.getByTestId('files')).toContainText('shift-notes');

  await page.getByTestId('change-bar-commit').click();
  const dlg = page.getByTestId('local-commit-dialog');
  await dlg.getByTestId('commit-message').fill('Shift variable, secret and file');
  await dlg.getByTestId('commit-submit').click();
  await expect(page.getByTestId('update-result')).toContainText('Applied', { timeout: 40_000 });
});

test('drift policy: Docker changes outside OPM — ask (default) or auto-commit', async ({ page }) => {
  await login(page, '/_opm/app/app/branch?tab=uncommitted');
  const pol = page.getByTestId('drift-policy');
  await expect(pol).toBeVisible();
  await expect(pol.getByRole('radio', { name: 'Ask' })).toHaveAttribute('aria-checked', 'true');
  await pol.getByRole('radio', { name: 'Auto-commit' }).click();
  await expect(pol.getByRole('radio', { name: 'Auto-commit' })).toHaveAttribute('aria-checked', 'true');
  // the agent keeps it (mock: in the page) — back on the tab it is still auto-commit
  await page.getByTestId('branch-tabs-history').click();
  await page.getByTestId('branch-tabs-uncommitted').click();
  await expect(page.getByTestId('drift-policy').getByRole('radio', { name: 'Auto-commit' })).toHaveAttribute('aria-checked', 'true');
});
