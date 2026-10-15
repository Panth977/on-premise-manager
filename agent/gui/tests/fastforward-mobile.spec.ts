// SPEC-final §21 #95 on a phone: touch hold (pointer events, pointerType touch; no context menu on long press) and the
// footer fits at 390 px (Cancel + Hold to fast-forward).
import { test, expect } from './fixtures';
import { knobs, login, shot } from './helpers';

test('touch hold → ×2 → ×4, lift → ×1; no context menu; the footer fits', async ({ page }) => {
  await knobs(page, { 'opm.mock.waitSec': '60' });
  // §21 #104: the roll-back-to-a-snapshot flow (approving proposals is gone)
  await login(page, '/_opm/app/app/backups');
  await page.getByTestId('snapshots').locator('tr', { hasText: 'Before changing the theme' }).getByRole('button', { name: 'Roll back' }).click();
  await page.getByTestId('rollback-drawer').getByTestId('rollback-go').click();
  const flow = page.getByTestId('approve-flow');
  const btn = flow.getByTestId('ff-hold');
  const badge = flow.getByTestId('ff-speed');
  await expect(btn).toBeVisible();
  const box = await btn.boundingBox();
  expect(box!.x).toBeGreaterThanOrEqual(0);
  expect(box!.x + box!.width).toBeLessThanOrEqual(390);
  await expect(btn).toHaveCSS('touch-action', 'none');
  await expect(btn).toHaveCSS('user-select', 'none');

  await btn.dispatchEvent('pointerdown', { pointerType: 'touch', button: 0, isPrimary: true, pointerId: 7 });
  await expect(badge).toHaveText('×2');
  const menu = await btn.evaluate((el) => !el.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true })));
  expect(menu, 'long-press context menu is prevented').toBe(true);
  await expect(badge).toHaveText('×4', { timeout: 2500 });
  await expect(flow.getByTestId('countdown')).toHaveAttribute('data-server-speed', '4');
  await shot(page, 'ff-mobile-hold', false);
  await btn.dispatchEvent('pointerup', { pointerType: 'touch', button: 0, isPrimary: true, pointerId: 7 });
  await expect(badge).toHaveText('×1');
  await expect(flow.getByTestId('countdown')).toHaveAttribute('data-server-speed', '1');
  // a cancelled touch (scroll / system gesture) releases too
  await btn.dispatchEvent('pointerdown', { pointerType: 'touch', button: 0, isPrimary: true, pointerId: 8 });
  await expect(badge).toHaveText('×2');
  await btn.dispatchEvent('pointercancel', { pointerType: 'touch', pointerId: 8 });
  await expect(badge).toHaveText('×1');
});
