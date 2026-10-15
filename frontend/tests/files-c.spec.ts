/**
 * Round C (F2) — files with expressions (SPEC §21 #54) + blob loading errors (#55), against the mock.
 * Seed: template "Hello world" (file banner, mounted by hello-web on site app), deployment ACME (secrets set).
 */
import { test, expect, type Page } from '@playwright/test';
import { login, SEED, pickCombo, expectToast, pick } from './helpers';

const TPL = `/app/templates/${SEED.template}`;
const DEP = `/app/deployments/${SEED.deployment}`;

/** Replace the whole content of the file editor (one input event: no autocomplete popup). */
async function setContent(page: Page, text: string) {
  const ed = page.getByTestId('file-content-content');
  await ed.click();
  await page.keyboard.press('ControlOrMeta+a');
  await page.keyboard.press('Delete');
  if (text) await page.keyboard.insertText(text);
}
async function openContent(page: Page, slug: string) {
  await page.getByTestId(`edit-content-${slug}`).click();
  await expect(page.getByTestId('file-content-drawer')).toBeVisible();
  await expect(page.getByTestId('file-content-content')).toBeVisible();
}

test.describe('files with expressions (round C)', () => {
  test('render toggle: on by default for text, hidden for binary, off → "as is"', async ({ page }) => {
    await login(page, 'admin', `${TPL}/files`);
    await expect(page.getByTestId('render-badge-banner')).toBeVisible();
    await page.getByTestId('menu-banner').click();
    await page.getByTestId('m-edit-def').click();
    const drawer = page.getByTestId('file-drawer');
    const toggle = drawer.getByTestId('file-render');
    await expect(toggle).toBeVisible();
    await expect(toggle.locator('input')).toBeChecked();
    await expect(drawer).toContainText('{{ "{{" }}');
    await drawer.getByTestId('file-editor-binary').click();
    await expect(toggle).toHaveCount(0);
    await drawer.getByTestId('file-editor-text').click();
    await expect(toggle.locator('input')).toBeChecked();
    await toggle.click();
    await expect(toggle.locator('input')).not.toBeChecked();
    await drawer.getByTestId('file-save').click();
    await expectToast(page, 'success', 'banner applied');
    await expect(page.getByTestId('render-badge-banner')).toHaveCount(0);
    await openContent(page, 'banner');
    await expect(page.getByTestId('file-render-note')).toContainText('as is');
    await expect(page.getByTestId('file-preview')).toHaveCount(0);
  });

  test('editor: {{ }} highlight, autocomplete, live error with line:col + jump', async ({ page }) => {
    await login(page, 'admin', `${TPL}/files`);
    await openContent(page, 'banner');
    await expect(page.getByTestId('file-content-content')).toContainText('Welcome to the hello-world deployment');
    // autocomplete after "{{ "
    await setContent(page, '');
    await page.keyboard.type('Theme: {{ var.th');
    const ac = page.locator('.cm-tooltip-autocomplete');
    await expect(ac).toBeVisible();
    await expect(ac).toContainText('var.theme');
    await page.waitForTimeout(200); // CodeMirror ignores Enter for 75 ms after the list opens
    await page.keyboard.press('Enter');
    await expect(page.getByTestId('file-content-content')).toContainText('Theme: {{ var.theme }}');
    await expect(page.locator('[data-testid="file-content"] .cm-opm-expr').first()).toBeVisible();
    await expect(page.getByTestId('file-issues-ok')).toBeVisible();
    // live error on line 3
    await setContent(page, 'Theme: {{ var.theme }}\nok line\nBad: {{ var. }}\n');
    const issue = page.getByTestId('file-issue').first();
    await expect(issue).toContainText(/banner:3:\d+/);
    await expect(page.locator('[data-testid="file-content"] .cm-opm-err')).toHaveCount(1);
    await issue.click();
    await expect(page.locator('[data-testid="file-content"] .cm-activeLine')).toContainText('Bad:');
    // the escape is not an error
    await setContent(page, 'lit {{ "{{" }} end');
    await expect(page.getByTestId('file-issue')).toHaveCount(0);
  });

  test('preview for site (round D: once per site): resolved value, masked secret, literal {{, container.* not allowed', async ({ page }) => {
    await login(page, 'admin', `${TPL}/files`);
    await openContent(page, 'banner');
    await setContent(page, 'Theme {{ var.theme }}\ntoken {{ secret.api-token }}\nlit {{ "{{" }} site {{ self.slug }}');
    const sel = page.getByTestId('preview-target');
    await expect(page.getByTestId('file-preview')).toContainText('Preview for site…');
    await expect(sel).toHaveAttribute('data-value', 'app');
    const out = page.getByTestId('file-preview-output');
    await expect(out).toContainText('Theme green');
    await expect(out).toContainText('token ••••');
    await expect(out).toContainText('lit {{ site app');
    await expect(page.getByTestId('preview-site-note')).toContainText('Rendered once for site app · shared by hello-web');
    await expect(page.getByTestId('preview-secret')).toBeVisible();
    await page.screenshot({ path: 'screenshots/c-file-preview-mock.png' });
    // a file is shared by every container of the site: container.* → E_NOT_ALLOWED (use a variable instead)
    await setContent(page, 'x\n{{ container.slug }}');
    await expect(page.getByTestId('file-issue').first()).toContainText('E_NOT_ALLOWED');
    await expect(page.getByTestId('file-issue').first()).toContainText('banner:2:');
    // unknown path → render error with file:line:col, once for the site
    await setContent(page, 'x\n{{ var.nope }}');
    await expect(page.getByTestId('file-preview-error')).toContainText('banner:2:');
    await expect(page.getByTestId('file-issue').first()).toContainText('(site app)');
    await page.screenshot({ path: 'screenshots/c-file-error-mock.png' });
  });

  test('JSON file: hypothetical preview when nothing mounts it + V_FILE_JSON warning', async ({ page }) => {
    await login(page, 'admin', `${TPL}/files`);
    await page.getByTestId('add-file').click();
    await page.getByTestId('file-label').fill('Test config');
    await pickCombo(page, 'file-mime', 'application/json', 'json');
    await expect(page.getByTestId('file-render').locator('input')).toBeChecked();
    await page.getByTestId('file-save').click();
    await expectToast(page, 'success', 'test-config added');
    await openContent(page, 'test-config');
    await setContent(page, '{"theme": {{ var.theme }}}');
    await expect(page.getByTestId('preview-none-mounting')).toBeVisible();
    await pick(page, 'preview-target', 'app');
    await expect(page.getByTestId('preview-hypothetical')).toBeVisible();
    await expect(page.getByTestId('file-preview-json')).toBeVisible();
    await expect(page.getByTestId('file-preview-output')).toContainText('{"theme": green}');
    await setContent(page, '{"theme": "{{ var.theme }}", "lit": "{{ "{{" }}"}');
    await expect(page.getByTestId('file-preview-json')).toHaveCount(0);
    await expect(page.getByTestId('file-preview-output')).toContainText('{"theme": "green", "lit": "{{"}');
    await expect(page.getByTestId('save-content')).toHaveCount(0);
    await expect(page.getByTestId('content-state')).toContainText('changed — not committed');
    await page.getByTestId('close-content').click();
    await expect(page.getByTestId('file-row-test-config')).toContainText(/\d+ B/);
  });

  test('deployment draft scope: preview masks the set secret', async ({ page }) => {
    await login(page, 'admin', `${DEP}/files`);
    await openContent(page, 'banner');
    await setContent(page, 'token={{ secret.api-token }} theme={{ var.theme }}');
    await expect(page.getByTestId('file-preview-output')).toContainText('token=•••• theme=yellow');
    await expect(page.getByTestId('file-preview-output')).not.toContainText('Zx8Kq2');
  });

  test('read-only account: View opens the content read-only (Close, no Save)', async ({ page }) => {
    await login(page, 'viewer', `${TPL}/files`);
    await page.getByTestId('view-content-banner').click();
    const drawer = page.getByTestId('file-content-drawer');
    await expect(drawer.getByTestId('file-content-content')).toContainText('Welcome to the hello-world deployment');
    await expect(drawer.getByTestId('file-content-content')).toHaveAttribute('contenteditable', 'false');
    await expect(drawer.getByTestId('save-content')).toHaveCount(0);
    await expect(drawer.getByTestId('file-preview-output')).toContainText('Welcome');
    await drawer.getByTestId('close-content').click();
    await expect(drawer).toHaveCount(0);
  });

  test('§21 #107 edits apply live; Revert to committed puts the head content back', async ({ page }) => {
    await login(page, 'admin', `${TPL}/files`);
    await openContent(page, 'banner');
    await setContent(page, 'changed');
    await expect(page.getByTestId('content-state')).toContainText('changed — not committed');
    await page.getByTestId('close-content').click();
    await expect(page.getByTestId('file-content-drawer')).toHaveCount(0);
    await expect(page.getByTestId('file-row-banner')).toContainText('7 B');
    await openContent(page, 'banner');
    await expect(page.getByTestId('file-content-content')).toContainText('changed');
    await page.getByTestId('revert-content').click();
    await expect(page.getByTestId('file-content-content')).toContainText('Welcome to the hello-world deployment');
    await expect(page.getByTestId('revert-content')).toHaveCount(0);
    await page.getByTestId('close-content').click();
    await expect(page.getByTestId('file-row-banner')).toContainText('38 B');
  });

  test('blob load error shows the real error text + Retry', async ({ page }) => {
    await login(page, 'admin', `${TPL}/files`);
    await expect(page.getByTestId('file-row-banner')).toBeVisible();
    // drop the banner blob from the persisted mock db, then reload
    await page.waitForTimeout(200);
    await page.evaluate(() => {
      const db = JSON.parse(localStorage.getItem('opm.mock.db')!);
      for (const k of Object.keys(db.blobs)) if (k.startsWith('170d0b7e')) delete db.blobs[k];
      localStorage.setItem('opm.mock.db', JSON.stringify(db));
    });
    await page.reload();
    await page.getByTestId('edit-content-banner').click();
    const err = page.getByTestId('file-content-drawer').getByTestId('error-state');
    await expect(err).toContainText('download failed: 404');
    await expect(err).toContainText('blob not found');
    await expect(err).not.toContainText('Failed to fetch');
    await expect(err.getByTestId('retry')).toBeVisible();
    await err.getByTestId('retry').click();
    await expect(err).toContainText('download failed: 404');
  });
});
