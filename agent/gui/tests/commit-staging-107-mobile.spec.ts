// §21 #107 at 390 px: the File content editor (no Save / upload), Review with the text diff, the Commit dialog with the
// upload note (OPM_S107_SHOTS=1 → screenshots/s107-*-390.png).
import { test, expect } from './fixtures';
import { knobs, login, shot } from './helpers';

test('390 px: edit a File content → Review diff → Commit uploads then commits', async ({ page }) => {
  const shots = process.env.OPM_S107_SHOTS === '1';
  await knobs(page, { 'opm.mock.branch.clean': '1' });
  await login(page, '/_opm/app/app/files');
  await page.getByTestId('edit-banner').click();
  const ed = page.getByTestId('file-drawer').getByTestId('code-editor');
  await expect(ed).toHaveValue('Welcome to the hello-world deployment\n');
  await ed.fill('Welcome to the plant, night shift (#107)\n');
  await expect(page.getByTestId('file-drawer').getByTestId('file-applied')).toContainText('in this browser until you commit');
  if (shots) await shot(page, 's107-file-edit-390', false);
  await page.getByTestId('file-drawer').getByTestId('file-done').click();
  await expect(page.getByTestId('local-edit-bar')).toContainText('1 change (not committed)');
  // refresh → the auto-saved draft is offered
  await page.waitForTimeout(800);
  await page.reload();
  await page.getByTestId('actor-email').fill('tester@acme.example');
  await page.getByTestId('actor-continue').click();
  await expect(page.getByTestId('pending-banner')).toHaveAttribute('data-state', 'same');
  if (shots) await shot(page, 's107-draft-prompt-390', false);
  await page.getByTestId('pending-banner-restore').click();
  await expect(page.getByTestId('local-edit-bar')).toContainText('1 change (not committed)');
  await page.getByTestId('review-changes').click();
  await expect(page.getByTestId('pending-text-diff-banner')).toContainText('night shift');
  if (shots) await shot(page, 's107-review-390', false);
  await page.getByTestId('review-commit').click();
  const dlg = page.getByTestId('local-commit-dialog');
  await expect(dlg.getByTestId('commit-upload-note')).toBeVisible();
  const box = await dlg.boundingBox();
  expect(box && box.width <= 390).toBeTruthy();
  await dlg.getByTestId('commit-message').fill('Banner for the night shift');
  if (shots) await shot(page, 's107-commit-390', false);
  await dlg.getByTestId('commit-submit').click();
  await expect(page.getByTestId('update-result')).toContainText('Applied', { timeout: 40_000 });
});
