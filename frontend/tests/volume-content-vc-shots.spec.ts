import { test, expect, type Page } from '@playwright/test';
import { login } from './helpers';

/** Round VC (§21 #101) screenshots → screenshots/vc-*.png (run with OPM_VC_SHOTS=1; skipped otherwise). */
const TPL = '/app/templates/TplNodeRedContent007/sites/flows/volumes/content?volume=nodered-data';
const DEP = '/app/deployments/DepNodeRedFlows00003';
const CLOUD = `${DEP}/sites/flows/volumes/content?volume=nodered-data&tab=cloud`;
const node = (page: Page, p: string) => page.locator(`[data-testid="vc-node"][data-path="${p}"]`);
async function theme(page: Page, t: 'light' | 'dark') {
  await page.emulateMedia({ colorScheme: t });
  await page.evaluate((x) => document.documentElement.setAttribute('data-theme', x), t);
}
const shot = async (page: Page, name: string, fullPage = false) => {
  await page.waitForTimeout(400);
  await page.screenshot({ path: `screenshots/vc-${name}.png`, fullPage });
};

test.describe('round VC screenshots', () => {
  test.skip(!process.env.OPM_VC_SHOTS, 'screenshots only with OPM_VC_SHOTS=1');

  test('editor: template + deployment (light, dark, 390 px)', async ({ page }) => {
    await login(page, 'admin', TPL);
    await theme(page, 'light');
    await node(page, 'lib').click();
    await node(page, 'settings.js').click();
    await expect(page.getByTestId('vc-code-content')).toContainText('module.exports');
    await shot(page, 'editor-template');
    await theme(page, 'dark');
    await shot(page, 'editor-template-dark');
    await theme(page, 'light');
    await node(page, 'lib/helper.js').click({ button: 'right' });
    await expect(page.getByTestId('vc-menu')).toBeVisible();
    await shot(page, 'editor-context-menu');
    await page.keyboard.press('Escape');
    await page.getByTestId('vc-tabs').locator('[data-tab="persist"]').click();
    await page.getByTestId('persist-exclude-add').click();
    await page.getByTestId('persist-exclude-4').fill('*.md');
    await shot(page, 'persist-settings', true);
    await page.setViewportSize({ width: 390, height: 844 });
    await page.getByTestId('vc-tabs').locator('[data-tab="content"]').click();
    await shot(page, 'editor-template-390', true);
    await page.setViewportSize({ width: 1360, height: 860 });
    await page.getByTestId('change-bar-discard').click();
    await page.getByTestId('pending-discard-dialog').getByTestId('confirm-ok').click();
    await page.goto(`${DEP}/sites/flows/volumes/content?volume=nodered-data`);
    await node(page, 'flows.json').click();
    await expect(page.getByTestId('vc-code-content')).toContainText('Plant integration');
    await shot(page, 'editor-deployment');
  });

  test('cloud copy in every sync state + send modal + history diff', async ({ page }) => {
    await login(page, 'admin', CLOUD);
    await theme(page, 'light');
    await expect(page.getByTestId('cloud-sync-badge')).toHaveAttribute('data-state', 'conflict');
    await shot(page, 'cloud-conflict');
    for (const s of ['cloud-ahead', 'in-sync', 'live-changed', 'pending-upload', 'error', 'never-snapshotted'] as const) {
      await page.evaluate((x) => (window as unknown as { __opmMockVc: { state: (n: string) => Promise<void> } }).__opmMockVc.state(x), s);
      await page.reload();
      await expect(page.getByTestId('cloud-sync-badge')).toHaveAttribute('data-state', s);
      await shot(page, `cloud-${s}`);
    }
    await page.evaluate(() => (window as unknown as { __opmMockReset: () => void }).__opmMockReset());
    await page.reload();
    await theme(page, 'dark');
    await expect(page.getByTestId('cloud-sync-badge')).toBeVisible();
    await shot(page, 'cloud-dark');
    await theme(page, 'light');
    await page.getByTestId('cloud-tabs').locator('[data-tab="history"]').click();
    await page.locator('[data-testid="cloud-version-row"][data-seq="3"]').getByTestId('cloud-version-diff').click();
    await page.getByTestId('cloud-version-changes').locator('[data-path="flows.json"]').getByTestId('cloud-version-changes-toggle').click();
    await expect(page.getByTestId('cloud-version-changes-text')).toBeVisible();
    await shot(page, 'cloud-history', true);
    await page.getByTestId('cloud-send').click();
    await expect(page.getByTestId('send-agent-warning')).toBeVisible();
    await shot(page, 'send-modal');
    await theme(page, 'dark');
    await shot(page, 'send-modal-dark');
    await theme(page, 'light');
    await page.keyboard.press('Escape');
    await page.setViewportSize({ width: 390, height: 844 });
    await page.getByTestId('cloud-tabs').locator('[data-tab="latest"]').click();
    await shot(page, 'cloud-390', true);
  });

  test('diff page per-file + publish warning; legacy card', async ({ page }) => {
    await login(page, 'admin', `${DEP}/diff`);
    await theme(page, 'light');
    await page.getByTestId('diff-tabs').locator('[data-tab="volumes"]').click();
    await shot(page, 'diff-page', true);
    await page.getByTestId('diff-row').filter({ hasText: 'content/flows.json' }).getByTestId('diff-blob-view').click();
    await expect(page.getByTestId('diff-blob-text')).toBeVisible();
    await shot(page, 'diff-blob');
    await page.keyboard.press('Escape');
    await page.getByTestId('publish').click();
    await expect(page.getByTestId('publish-content-warning')).toBeVisible();
    await shot(page, 'publish-warning');
    await page.keyboard.press('Escape');
    await page.goto('/app/templates/TplLegacySeeds000008/sites/site-01/volumes/content?volume=nodered-data');
    await expect(page.getByTestId('vc-legacy')).toBeVisible();
    await shot(page, 'legacy-card');
    await page.getByTestId('vc-convert').click();
    await shot(page, 'legacy-converted');
  });
});
