// §21 #105 at 390 px: change bar, Commit dialog, drift-first banner, restore banner (OPM_J_SHOTS=1 → screenshots/j-*-390.png).
import { test, expect } from './fixtures';
import { knobs, login, shot, pickOption } from './helpers';

test('390 px: change bar → commit dialog (drift first) → restore after reload', async ({ page }) => {
  const shots = process.env.OPM_J_SHOTS === '1';
  await login(page, '/_opm/app/app/variables');
  await pickOption(page, 'var-theme', 'blue');
  const bar = page.getByTestId('local-edit-bar');
  await expect(bar).toContainText('1 change (not committed)');
  await expect(page.getByTestId('change-bar-commit')).toBeVisible();
  if (shots) await shot(page, 'j-changebar-390', false);
  await page.getByTestId('change-bar-commit').click();
  const dlg = page.getByTestId('local-commit-dialog');
  await expect(dlg.getByTestId('drift-first')).toBeVisible();
  const box = await dlg.boundingBox();
  expect(box && box.width <= 390).toBeTruthy();
  if (shots) await shot(page, 'j-drift-first-390', false);
  await dlg.getByTestId('commit-cancel').click();
  await knobs(page, { 'opm.mock.branch.clean': '1' });
  await page.reload();
  await page.getByTestId('actor-email').fill('tester@acme.example');
  await page.getByTestId('actor-continue').click();
  await expect(page.getByTestId('pending-banner')).toBeVisible();
  if (shots) await shot(page, 'j-restore-390', false);
  await page.getByTestId('pending-banner-restore').click();
  await page.getByTestId('change-bar-commit').click();
  await page.getByTestId('local-commit-dialog').getByTestId('commit-message').fill('Theme blue for the control room');
  if (shots) await shot(page, 'j-commit-390', false);
  await page.getByTestId('local-commit-dialog').getByTestId('commit-submit').click();
  await expect(page.getByTestId('update-result')).toContainText('Applied', { timeout: 40_000 });
});
