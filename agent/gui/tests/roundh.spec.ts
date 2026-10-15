/** Round H (SPEC §21 #86): the status page alerts when the edge cannot bind port 80 (owner process named). */
import { test, expect } from './fixtures';
import { login } from './helpers';

test('status: port 80 busy → alert names the owning process', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('opm.mock.port80Busy', '1'));
  await login(page, '/_opm/app/app/status');
  const alert = page.getByTestId('edge-port-alert');
  await expect(alert).toContainText('port 80 is used by nginx (pid 812) — stop it or change edge.httpPort');
});

test('status: no alert when the edge port is bound', async ({ page }) => {
  await login(page, '/_opm/app/app/status');
  await expect(page.getByRole('heading', { name: /Status/ })).toBeVisible();
  await expect(page.getByTestId('edge-port-alert')).toHaveCount(0);
});

test('status: container logs follow + download (§21 #87)', async ({ page }) => {
  await login(page, '/_opm/app/app/status');
  await expect(page.getByTestId('logs')).toBeVisible();
  await page.getByTestId('logs-follow').check();
  const dl = page.waitForEvent('download');
  await page.getByTestId('logs-download').click();
  expect((await dl).suggestedFilename()).toMatch(/^opm-app-.*\.log$/);
});
