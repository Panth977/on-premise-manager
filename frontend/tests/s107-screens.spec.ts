/**
 * §21 #107 screenshots → screenshots/s107-*.png (light, dark, 390 px): the content workspace with pending edits (no
 * Apply), the Files content drawer (live, no Save), Review with the per-file text diff, the commit dialog (upload note +
 * progress). Skipped unless OPM_SCREENSHOTS=1.
 */
import { expect, test, type Page } from '@playwright/test';
import { login, nav } from './helpers';

test.skip(!process.env.OPM_SCREENSHOTS, 'set OPM_SCREENSHOTS=1');
const TPL = '/app/templates/TplNodeRedContent007';
const CONTENT = `${TPL}/sites/flows/volumes/content?volume=nodered-data`;
const still = (page: Page) => page.waitForTimeout(500);
const node = (page: Page, p: string) => page.locator(`[data-testid="vc-node"][data-path="${p}"]`);

for (const [name, theme, width] of [
  ['light', 'light', 1360],
  ['dark', 'dark', 1360],
  ['m', 'light', 390],
] as const) {
  test(`s107 screens ${name}`, async ({ page }) => {
    const sfx = name === 'light' ? '' : `-${name}`;
    const shot = async (n: string, full = false) => {
      await still(page);
      await page.screenshot({ path: `screenshots/s107-${n}${sfx}.png`, fullPage: full });
    };
    await page.setViewportSize({ width, height: width < 600 ? 844 : 900 });
    await page.addInitScript((t) => localStorage.setItem('opm.theme', t), theme);
    await login(page, 'admin', '/app/templates');
    await page.evaluate(() => (window as unknown as { __opmMockReset: () => void }).__opmMockReset());
    await page.waitForTimeout(150);
    await page.goto(`${TPL}/files`);
    // a File with content edited in the drawer
    await page.getByTestId('add-file').click();
    await page.getByTestId('file-label').fill('Notes');
    await page.getByTestId('file-save').click();
    await page.getByTestId('edit-content-notes').click();
    const d = page.getByTestId('file-content-drawer');
    await d.getByTestId('file-content-content').click();
    await page.keyboard.insertText('Plant notes\nbroker: {{ var.mqtt-host }}\n');
    await expect(d.getByTestId('content-state')).toContainText('not committed');
    await shot('file-drawer');
    await d.getByTestId('close-content').click();
    // the volume content workspace: edit flows.json, add a file
    await nav(page, CONTENT);
    await node(page, 'flows.json').click();
    await page.getByTestId('vc-code-content').click();
    await page.keyboard.press('ControlOrMeta+End');
    await page.keyboard.insertText('\n// v3: alarm subflow');
    await expect(page.getByTestId('vc-changed')).toBeVisible();
    await page.getByTestId('vc-code-content').blur();
    await shot('workspace');
    // a refresh: the local draft (IndexedDB) is offered — Restore or Discard?
    await page.waitForTimeout(600);
    await page.reload();
    await expect(page.getByTestId('pending-banner')).toContainText('unsaved changes');
    await shot('restore-prompt');
    await page.getByTestId('pending-banner-restore').click();
    await expect(page.getByTestId('vc-changed')).toBeVisible();
    // Review: per-file changes with a text diff
    await page.getByTestId('review-changes').click();
    await expect(page).toHaveURL(/review=working/);
    const flows = page.locator('[data-testid^="merge-item-"]').filter({ hasText: 'flows.json' });
    const key = (await flows.getAttribute('data-testid'))!.slice('merge-item-'.length);
    await page.getByTestId(`merge-expand-${key}`).click();
    await expect(page.getByTestId(`merge-textdiff-${key}`)).toContainText('alarm subflow');
    await shot('review', true);
    // the commit dialog: upload note, then progress (slow mock)
    await page.getByTestId('change-bar-commit').click();
    const dlg = page.getByTestId('commit-dialog');
    await expect(dlg.getByTestId('commit-upload-note')).toBeVisible();
    await dlg.getByTestId('commit-message').fill('Notes + alarm subflow');
    await shot('commit-dialog');
    await page.evaluate(() => localStorage.setItem('opm.mock.latency', '2500'));
    await dlg.getByTestId('commit-submit').click();
    await expect(dlg.getByTestId('commit-progress')).toContainText('Uploading', { timeout: 15_000 });
    await page.screenshot({ path: `screenshots/s107-commit-progress${sfx}.png` });
    await expect(dlg).toBeHidden({ timeout: 30_000 });
  });
}
