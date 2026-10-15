// SPEC-final §21 #101 on a phone (390 px), light + dark: the volume list with sync badges, the Cloud sync tab and the
// "Apply cloud version" dialog (conflict list + per-file diff, unified by default) have no horizontal page scroll.
import { test, expect } from './fixtures';
import { knobs, login, shot } from './helpers';

for (const scheme of ['light', 'dark'] as const) {
  test(`persist-to-cloud volumes at 390 px (${scheme})`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: scheme });
    await knobs(page, { 'opm.mock.waitSec': '2' });
    await login(page, '/_opm/app/northwind/volumes?v=nodered-data&tab=sync');
    const over = async () => page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    await expect(page.getByTestId('sync-badge-nodered-data')).toHaveText('Conflict');
    await expect(page.getByTestId('vol-sync')).toBeVisible();
    expect(await over()).toBeLessThanOrEqual(1);
    await shot(page, `vc-volumes-sync-m-${scheme}`);
    await page.getByTestId('sync-apply').click();
    const af = page.getByTestId('apply-flow');
    await expect(af.getByTestId('plan-item-flows.json')).toBeVisible();
    await af.getByTestId('diff-btn-flows.json').click();
    await expect(af.getByTestId('file-diff-flows.json').getByTestId('diff-unified')).toBeVisible();
    await af.getByTestId('choice-flows.json').getByRole('radio', { name: 'Take cloud' }).click();
    await page.waitForFunction(() => document.getAnimations().every((a) => a.playState !== 'running'));
    const box = await af.getByTestId('apply-continue').boundingBox();
    expect(box!.x + box!.width).toBeLessThanOrEqual(391);
    expect(await over()).toBeLessThanOrEqual(1);
    await shot(page, `vc-apply-conflict-m-${scheme}`, false);
  });
}
