import { test, expect, type Page } from '@playwright/test';
import { login, expectToast, SEED, pickIcon, pickCombo, pick, commitAll, nav } from './helpers';

const TPL = `/app/templates/${SEED.template}`;
const DEP = `/app/deployments/${SEED.deployment}`;

async function openMenu(page: Page, testid: string, item: string) {
  await page.getByTestId(testid).getByRole('button').first().click();
  await page.getByTestId(item).click();
}

test.describe('templates', () => {
  test('create a template, edit its meta, version increments', async ({ page }) => {
    await login(page, 'admin', '/app/templates');
    await expect(page.getByTestId('template-row').first()).toBeVisible();
    await page.getByTestId('new-template').click();
    await page.getByTestId('nt-create').click();
    await expect(page.getByTestId('template-modal')).toContainText('Required');
    await page.getByTestId('nt-label').fill('Edge lab');
    await pickIcon(page, 'nt-icon', 'flask-conical');
    await page.getByTestId('nt-create').click();
    await expectToast(page, 'success', 'Edge lab');
    await expect(page.getByTestId('page-title')).toHaveText('Edge lab');
    await expect(page.getByTestId('template-version')).toHaveText('v1');
    await page.getByTestId('tpl-label').fill('Edge lab 2');
    await page.getByTestId('tpl-save').click();
    await expectToast(page, 'success', 'Details saved');
    await expect(page.getByTestId('template-version')).toHaveText('v2');
    // json preview + delete with typed label
    await page.getByTestId('json-toggle').click();
    await expect(page.getByTestId('tpl-json')).toContainText('"variables": []');
    await page.getByTestId('tpl-delete').click();
    await expect(page.getByTestId('confirm-ok')).toBeDisabled();
    await page.getByTestId('typed-confirm').fill('Edge lab 2');
    await page.getByTestId('confirm-ok').click();
    await page.waitForURL('**/app/templates');
  });

  test('variables: add, edit, remove with usage list', async ({ page }) => {
    await login(page, 'admin', `${TPL}/variables`);
    await expect(page.getByTestId('variables-table')).toBeVisible();
    await page.getByTestId('add-variable').click();
    await page.getByTestId('var-label').fill('Log level');
    await expect(page.getByTestId('var-slug')).toHaveValue('log-level');
    await page.getByTestId('var-type-enum').click();
    await page.getByTestId('var-options-input').fill('debug');
    await page.keyboard.press('Enter');
    await page.getByTestId('var-options-input').fill('info');
    await page.keyboard.press('Enter');
    await pick(page, 'var-value', 'info');
    await page.getByTestId('var-save').click();
    await expectToast(page, 'success', 'log-level added');
    await expect(page.getByTestId('var-row-log-level')).toContainText('info');
    // duplicate slug is refused client-side
    await page.getByTestId('add-variable').click();
    await page.getByTestId('var-label').fill('x');
    await page.getByTestId('var-slug-edit').click();
    await page.getByTestId('var-slug').fill('theme');
    await page.getByTestId('var-save').click();
    await expect(page.getByTestId('variable-drawer')).toContainText('exists');
    await page.keyboard.press('Escape');
    // remove "theme": its usage in hello-web THEME is listed
    await page.getByRole('button', { name: 'Remove theme' }).click();
    await expect(page.getByTestId('remove-variable')).toContainText('var.theme');
    await expect(page.getByTestId('remove-variable')).toContainText('hello-web');
    await page.getByTestId('confirm-ok').click();
    await expectToast(page, 'success', 'theme removed');
    await expect(page.getByTestId('var-row-theme')).toHaveCount(0);
  });

  test('secrets: templates hold definitions only', async ({ page }) => {
    await login(page, 'admin', `${TPL}/secrets`);
    await expect(page.getByTestId('sec-row-api-token')).toContainText('generate alnum 24');
    await page.getByTestId('add-secret').click();
    await page.getByTestId('sec-label').fill('DB password');
    await expect(page.getByTestId('sec-slug')).toHaveValue('db-password');
    await page.getByTestId('sec-generate').click();
    await page.getByTestId('sec-charset-hex').click();
    await page.getByTestId('sec-length').fill('4');
    await page.getByTestId('sec-save').click();
    await expect(page.getByTestId('secret-drawer')).toContainText('8–128');
    await page.getByTestId('sec-length').fill('40');
    await page.getByTestId('sec-save').click();
    await expectToast(page, 'success', 'db-password added');
    await expect(page.getByTestId('sec-row-db-password')).toContainText('generate hex 40');
  });

  test('files: add a definition with pickers (slug from label, mime combobox, editor, max size)', async ({ page }) => {
    await login(page, 'admin', `${TPL}/files`);
    await page.getByTestId('add-file').click();
    await page.getByTestId('file-label').fill('Site logo');
    await expect(page.getByTestId('file-slug')).toHaveValue('site-logo');
    await pickCombo(page, 'file-mime', 'image/png', 'png');
    // a binary mime switches the editor to binary
    await expect(page.getByTestId('file-editor-binary')).toHaveAttribute('aria-checked', 'true');
    await page.getByTestId('file-unit-KiB').click();
    await page.getByTestId('file-max').fill('256');
    await expect(page.getByTestId('file-drawer')).toContainText('256 KiB');
    await page.getByTestId('file-save').click();
    await expectToast(page, 'success', 'site-logo added');
    await expect(page.getByTestId('file-row-site-logo')).toContainText('image/png');
  });

  test('files: edit text content (live into the change set) and commit', async ({ page }) => {
    await login(page, 'admin', `${TPL}/files`);
    await expect(page.getByTestId('file-row-banner')).toContainText('38 B');
    await page.getByTestId('edit-content-banner').click();
    const editor = page.getByTestId('file-content-content');
    await expect(editor).toContainText('Welcome to the hello-world deployment');
    await editor.click();
    await page.keyboard.press('ControlOrMeta+a');
    await page.keyboard.type('Hello from the test\n');
    await expect(page.getByTestId('content-size')).toContainText('20 B');
    // §21 #107 no Save: the content is a pending change as you type
    await expect(page.getByTestId('save-content')).toHaveCount(0);
    await expect(page.getByTestId('content-state')).toContainText('changed — not committed');
    await page.getByTestId('close-content').click();
    await expect(page.getByTestId('file-row-banner')).toContainText('20 B');
    await expect(page.getByTestId('change-bar-count')).toContainText('1 change (not committed)');
    await commitAll(page, 'New banner text');
    await page.reload();
    await expect(page.getByTestId('file-row-banner')).toContainText('20 B');
  });

  test('site editor: add site, container, host port expression, save', async ({ page }) => {
    await login(page, 'admin', `${TPL}/sites`);
    await page.getByTestId('add-site').click();
    // the slug derives from the label; "backups" is reserved
    await page.getByTestId('site-label').fill('Backups');
    await expect(page.getByTestId('site-slug')).toHaveValue('backups');
    // host: auto-detect is on by default (no host needed)
    await expect(page.getByTestId('site-host-auto').locator('input')).toBeChecked();
    await page.getByTestId('site-save').click();
    await expect(page.getByTestId('add-site-modal')).toContainText('reserved');
    await page.getByTestId('site-label').fill('Cache');
    await expect(page.getByTestId('site-slug')).toHaveValue('cache');
    await page.getByTestId('site-save').click();
    await expectToast(page, 'success', 'Site cache added');
    await page.waitForURL(`**${TPL}/sites/cache/containers`);
    await page.getByTestId('add-container').click();
    await page.waitForURL(`**${TPL}/sites/cache/containers/new`);
    await pickCombo(page, 'c-service', SEED.svcRedis, 'red');
    await expect(page.getByTestId('container-version')).toHaveAttribute('data-value', 'v1');
    await expect(page.getByTestId('c-name')).toHaveValue('redis');
    await expect(page.getByTestId('container-editor')).toContainText('redis:v1');
    // host port 80 belongs to the edge → error, save disabled
    await page.getByTestId('port-tcp-published').click();
    await page.getByTestId('hostport-tcp-num').fill('80');
    await expect(page.getByTestId('ports-table')).toContainText('belongs to the edge');
    await expect(page.getByTestId('apply-container')).toBeDisabled();
    // expression with autocomplete ({ } toggle)
    await page.getByTestId('hostport-tcp-expr').click();
    await page.getByTestId('hostport-tcp-input').fill('{{ var.red');
    await expect(page.getByRole('option', { name: /var\.redis-host-port/ })).toBeVisible();
    await page.keyboard.press('Enter');
    await expect(page.getByTestId('hostport-tcp-input')).toHaveValue('{{ var.redis-host-port }}');
    await expect(page.getByTestId('hostport-tcp-preview')).toContainText('16379');
    // conflicts with site db? no: different site → fine
    await page.getByTestId('apply-container').click();
    await expectToast(page, 'success', 'Container redis added');
    await page.waitForURL(`**${TPL}/sites/cache/containers`);
    await expect(page.getByTestId('container-row-redis')).toBeVisible();
    // site + container are ONE pending change set until Commit (§21 #105)
    await expect(page.getByTestId('change-bar-count')).toContainText('1 change (not committed)');
    await commitAll(page, 'Cache site with redis');
    await expect(page.getByTestId('change-bar-count')).toHaveCount(0);
  });

  test('edge: add proxy route, effective order, Test a URL', async ({ page }) => {
    await login(page, 'admin', `${TPL}/sites/app/edge`);
    await expect(page.getByTestId('routes-table')).toBeVisible();
    // Test a URL against the existing routes
    await page.getByTestId('test-url-input').fill('http://127.0.0.1:8081/api/count?x=1');
    await page.getByTestId('test-run').click();
    await expect(page.getByTestId('test-status')).toHaveText('200');
    await expect(page.getByTestId('test-upstream')).toHaveText('http://hello-api:8080/count?x=1');
    await page.getByTestId('test-url-input').fill('http://127.0.0.1:8081/old/x');
    await page.getByTestId('test-run').click();
    await expect(page.getByTestId('test-status')).toHaveText('308');
    await expect(page.getByTestId('test-location')).toHaveText('/');
    await page.getByTestId('test-url-input').fill('https://127.0.0.1:8081/');
    await page.getByTestId('test-run').click();
    await expect(page.getByTestId('test-result')).toContainText('not an edge port');
    // add a proxy route /v2 → hello-api with strip (route page)
    await page.getByTestId('add-proxy-route').click();
    await page.waitForURL('**/edge/route?new=proxy');
    await page.getByTestId('route-path').fill('/v2');
    await expect(page.getByTestId('route-id')).toHaveValue('v2');
    await pick(page, 'route-service', 'hello-api');
    await expect(page.getByTestId('route-port')).toHaveAttribute('data-value', 'http');
    await page.getByTestId('route-path-mode-strip').click();
    await expect(page.getByTestId('route-preview-upstream')).toHaveText('/example');
    await page.getByTestId('route-apply').click();
    await page.waitForURL((u) => u.pathname.endsWith('/sites/app/edge'));
    await expect(page.getByTestId('route-row-v2')).toBeVisible();
    // prefix routes are order-free (longest prefix wins): no manual reordering, effective order shown
    await expect(page.getByTestId('route-row-v2')).toHaveAttribute('data-group', 'prefix');
    await expect(page.getByTestId('up-v2')).toHaveCount(0);
    await page.getByTestId('test-url-input').fill('http://127.0.0.1:8081/v2/count');
    await page.getByTestId('test-run').click();
    await expect(page.getByTestId('test-upstream')).toHaveText('http://hello-api:8080/count');
    await expect(page.getByTestId('dirty-dot')).toBeVisible();
    await commitAll(page, 'Route /v2');
    await expect(page.getByTestId('dirty-dot')).toHaveCount(0);
  });

  test('§21 #105 no leave guard: site edits stay in the change set across pages', async ({ page }) => {
    await login(page, 'admin', `${TPL}/sites/app/settings`);
    await page.getByTestId('agent-port').fill('7099');
    await expect(page.getByTestId('dirty-dot')).toBeVisible();
    await page.getByTestId('tree-app-edge').click();
    await page.waitForURL(`**${TPL}/sites/app/edge`);
    await expect(page.getByTestId('dirty-dot')).toBeVisible();
    await page.getByTestId('nav-variables').click();
    await page.waitForURL(`**${TPL}/variables`);
    await expect(page.getByTestId('leave-modal')).toHaveCount(0);
    await expect(page.getByTestId('change-bar-count')).toContainText('1 change (not committed)');
    await nav(page, `${TPL}/sites/app/settings`);
    await expect(page.getByTestId('agent-port')).toHaveValue('7099');
  });

  test('a second tab committed meanwhile: the change set is rebased (409 stale)', async ({ page, context }) => {
    await login(page, 'admin', `${TPL}/sites/db/settings`);
    await page.getByTestId('agent-port').fill('7091');
    const other = await context.newPage();
    await other.goto(`${TPL}/variables`);
    // the other tab sees this tab's stored set: it discards the offer and commits its own change
    await other.getByTestId('pending-banner-discard').click();
    await other.getByTestId('add-variable').click();
    await other.getByTestId('var-label').fill('Other tab');
    await other.getByTestId('var-save').click();
    await commitAll(other, 'Other tab variable');
    await other.close();
    await page.getByTestId('change-bar-commit').click();
    const dlg = page.getByTestId('commit-dialog');
    await dlg.getByTestId('commit-message').fill('Agent port 7091');
    await dlg.getByTestId('commit-submit').click();
    await expect(dlg.getByTestId('commit-info')).toContainText(/rebased onto commit \d+/i);
    await dlg.getByTestId('commit-message').fill('Agent port 7091');
    await dlg.getByTestId('commit-submit').click();
    await expectToast(page, 'success', /Commit \d+ recorded/);
    await page.getByTestId('nav-variables').click();
    await expect(page.getByTestId('var-row-other-tab')).toBeVisible();
  });

  test('viewer sees the template read only', async ({ page }) => {
    await login(page, 'viewer', `${TPL}/variables`);
    await expect(page.getByTestId('read-only').first()).toBeVisible();
    await expect(page.getByTestId('add-variable')).toHaveCount(0);
    await page.goto(`${TPL}/sites/app`);
    await expect(page.getByTestId('change-bar')).toHaveCount(0);
  });
});

test.describe('deployment draft', () => {
  test('variables: inline value edit goes into the change set, changed-since-head marker', async ({ page }) => {
    await login(page, 'admin', `${DEP}/variables`);
    await expect(page.getByTestId('draft-banner')).toBeVisible();
    await expect(page.getByTestId('changed-greeting')).toHaveCount(0);
    await page.getByTestId('val-greeting').fill('Hi there');
    await expect(page.getByTestId('changed-greeting')).toBeVisible();
    await expect(page.getByTestId('change-bar-count')).toContainText('1 change (not committed)');
    await expect(page.getByTestId('save-draft')).toHaveCount(0);
    // a number out of range shows the rule and blocks the commit
    await page.getByTestId('val-redis-host-port').fill('80');
    await expect(page.getByTestId('var-row-redis-host-port')).toContainText('≥ 1024');
    await expect(page.getByTestId('change-bar-commit')).toBeDisabled();
  });

  test('secrets: reveal (head), set / generate / clear are pending until Commit', async ({ page }) => {
    await login(page, 'admin', `${DEP}/secrets`);
    await page.getByTestId('reveal-api-token-view').click();
    await expect(page.getByTestId('reveal-api-token-value')).toHaveText(/^[A-Za-z0-9]{24}$/);
    await page.getByTestId('set-api-token').click();
    await page.getByTestId('secret-value').fill('my-own-token-value');
    await page.getByTestId('secret-save').click();
    await expectToast(page, 'success', 'api-token applied');
    await expect(page.getByTestId('pending-api-token')).toContainText('new value — not committed');
    await commitAll(page, 'Own API token');
    await page.getByTestId('reveal-api-token-view').click();
    await expect(page.getByTestId('reveal-api-token-value')).toHaveText('my-own-token-value');
    await openMenu(page, 'menu-api-token', 'm-generate');
    await page.getByTestId('confirm-ok').click();
    await expect(page.getByTestId('pending-api-token')).toContainText('generated at commit');
    await openMenu(page, 'menu-api-token', 'm-clear');
    await page.getByTestId('clear-secret').getByTestId('confirm-ok').click();
    await expect(page.getByTestId('pending-api-token')).toContainText('cleared — not committed');
  });

  test('site editor: a container edit is applied into the change set; Review lists it', async ({ page }) => {
    await login(page, 'admin', `${DEP}/sites`);
    await expect(page.getByTestId('sites-table')).toBeVisible();
    await page.getByTestId('site-row-app').click();
    await expect(page.getByTestId('site-editor')).toBeVisible();
    await page.getByTestId('container-row-hello-web').click();
    await page.getByTestId('add-env').click();
    await page.getByTestId('env-name-2').fill('LOG_LEVEL');
    await page.getByTestId('env-value-x2-input').fill('{{ var.greeting | upper }}');
    await expect(page.getByTestId('env-value-x2-preview')).toContainText('HELLO FROM OPM');
    await page.getByTestId('apply-container').click();
    await expectToast(page, 'success', 'Container hello-web applied');
    await page.getByTestId('review-changes').click();
    await expect(page).toHaveURL(/compare\?review=working/);
    await expect(page.getByTestId('review-view')).toContainText('LOG_LEVEL');
  });
});
