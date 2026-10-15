// SPEC-final §21 #96 on a phone (390 px) and in dark mode: the volume manager has no horizontal page scroll; the action
// drawers fit.
import { test, expect } from './fixtures';
import { knobs, login, open, shot } from './helpers';

for (const scheme of ['light', 'dark'] as const) {
  test(`volume manager at 390 px (${scheme})`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: scheme });
    await knobs(page, { 'opm.mock.waitSec': '2' });
    await login(page, '/_opm/app/app/volumes?v=api-env');
    await expect(page.getByTestId('vol-overview')).toBeVisible();
    const over = async () => page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    expect(await over()).toBeLessThanOrEqual(1);
    await shot(page, `vm-mobile-overview-${scheme}`);
    await page.getByTestId('vtab-files').click();
    await expect(page.getByTestId('vol-entries')).toBeVisible();
    expect(await over()).toBeLessThanOrEqual(1);
    await shot(page, `vm-mobile-files-${scheme}`);
    await page.getByTestId('vol-copy').click();
    const cd = page.getByTestId('copy-drawer');
    await expect(cd.getByTestId('copy-go')).toBeVisible();
    await page.waitForFunction(() => document.getAnimations().every((a) => a.playState !== 'running'));
    const box = await cd.getByTestId('copy-go').boundingBox();
    expect(box!.x + box!.width).toBeLessThanOrEqual(391);
    await shot(page, `vm-mobile-copy-${scheme}`, false);
    await page.keyboard.press('Escape');
    await open(page, '/_opm/app/*/volumes');
    await expect(page.getByTestId('all-volumes')).toBeVisible();
    expect(await over()).toBeLessThanOrEqual(1);
  });
}
