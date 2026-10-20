// SPEC-final §21 #101 / #117 on a phone (390 px), light + dark: the volume list with sync badges and the Cloud sync tab
// have no horizontal page scroll.
import { test, expect } from './fixtures';
import { knobs, login, shot } from './helpers';

for (const scheme of ['light', 'dark'] as const) {
  test(`persist-to-cloud volumes at 390 px (${scheme})`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: scheme });
    await knobs(page, { 'opm.mock.waitSec': '2' });
    await login(page, '/_opm/app/northwind/volumes?v=ez-scripts&tab=sync');
    const over = async () => page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    await expect(page.getByTestId('sync-badge-ez-scripts')).toHaveText('Changed on the server');
    await expect(page.getByTestId('vol-sync')).toBeVisible();
    expect(await over()).toBeLessThanOrEqual(1);
    await shot(page, `vc-volumes-sync-m-${scheme}`);
  });
}
