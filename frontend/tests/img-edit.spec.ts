/** Image page = one editable "Image" form (SPEC §21 #82): edit everything but the identity; in-use renames / removals. */
import { test, expect, type Page } from '@playwright/test';
import { login, expectToast, SEED } from './helpers';

const SHOTS = 'screenshots';
const URL_API = `/app/services/${SEED.svcApi}/image/v1`; // hello-api:v1 — used by template "Hello world" and deployment "ACME plant"

type MockDb = { templates: Record<string, { sites: { siteSlug: string; config: { containers: { containerSlug: string; serviceSlug: string; version: string; ports: { portName: string }[] }[]; edge: { routes: { action: { type: string; containerSlug?: string; portName?: string } }[] } } }[]; version: number }>; deployments: Record<string, { draft: MockDb['templates'][string]; draftVersion: number }>; revisions: Record<string, { revId: string; config: MockDb['templates'][string] }[]> };
const mockDb = (page: Page) => page.evaluate(() => JSON.parse(localStorage.getItem('opm.mock.db') ?? '{}') as unknown) as Promise<{ db?: MockDb } & MockDb>;
async function db(page: Page): Promise<MockDb> {
  const raw = await mockDb(page);
  return (raw.db ?? raw) as MockDb;
}
const portRow = (page: Page, i: number) => page.getByTestId('ri-port-row').nth(i);

test.describe('image page: everything but the identity is editable', () => {
  test('identity is locked; edit every section, save, reload', async ({ page }) => {
    await login(page, 'admin', URL_API);
    const id = page.getByTestId('image-identity');
    await expect(id).toContainText('Image identity');
    await expect(id).toContainText('Reference');
    await expect(id.locator('input')).toHaveCount(0);
    await expect(page.getByTestId('im-save')).toBeDisabled();
    // runtime
    await page.getByTestId('ri-kind-job').click();
    await page.getByTestId('ri-workdir').fill('/srv/app');
    await page.getByTestId('ri-user').fill('1000:1000');
    // ports: edit description, add one
    await portRow(page, 0).getByTestId('port-desc').fill('Main HTTP');
    await page.getByTestId('ri-add-port').click();
    await portRow(page, 1).getByTestId('port-name').fill('metrics');
    await portRow(page, 1).getByLabel('Port number').fill('9100');
    await portRow(page, 1).getByLabel('Port number').blur();
    // env: add one with a default
    await page.getByTestId('ri-add-env').click();
    const env = page.getByTestId('ri-env-row').last();
    await env.getByTestId('env-name').fill('log_level');
    await expect(env.getByTestId('env-name')).toHaveValue('LOG_LEVEL');
    await env.getByTestId('env-desc').fill('added on the image page');
    // volumes: add one
    await page.getByTestId('ri-add-vol').click();
    const vol = page.getByTestId('ri-vol-row').last();
    await vol.getByTestId('vol-name').fill('cache');
    await vol.getByTestId('vol-mount-input').fill('/cache');
    // description
    await page.getByTestId('ri-desc').fill('Counter API (edited)');
    await expect(page.getByTestId('im-dirty')).toBeVisible();
    await expect(page.getByTestId('im-pending')).toContainText('port metrics added');
    await page.screenshot({ path: `${SHOTS}/img-edit-mock-form.png`, fullPage: true });
    await page.getByTestId('im-save').click();
    await expectToast(page, 'success', 'Image saved');
    await expect(page.getByTestId('im-save')).toBeDisabled();
    await page.reload();
    await expect(page.getByTestId('ri-workdir')).toHaveValue('/srv/app');
    await expect(page.getByTestId('ri-user')).toHaveValue('1000:1000');
    await expect(page.getByTestId('ri-port-row')).toHaveCount(2);
    await expect(portRow(page, 1).getByTestId('port-name')).toHaveValue('metrics');
    await expect(page.getByTestId('ri-env-row').last().getByTestId('env-name')).toHaveValue('LOG_LEVEL');
    await expect(page.getByTestId('ri-vol-row')).toHaveCount(2);
    await expect(page.getByTestId('ri-desc')).toHaveValue('Counter API (edited)');
  });

  test('Reset and the leave guard', async ({ page }) => {
    await login(page, 'admin', URL_API);
    await page.getByTestId('ri-desc').fill('temp');
    await expect(page.getByTestId('im-reset')).toBeEnabled();
    await page.getByTestId('im-reset').click();
    await expect(page.getByTestId('ri-desc')).not.toHaveValue('temp');
    await expect(page.getByTestId('im-save')).toBeDisabled();
    await page.getByTestId('ri-desc').fill('temp again');
    await page.getByRole('link', { name: 'Used by' }).click();
    await expect(page.getByTestId('leave-modal')).toBeVisible();
  });

  test('rename an in-use port → impact dialog → rename in templates only (§21 #94: deployment drafts + heads untouched)', async ({ page }) => {
    await login(page, 'admin', URL_API);
    await portRow(page, 0).getByTestId('port-name').fill('web');
    await expect(portRow(page, 0).getByTestId('port-renamed')).toContainText('http');
    await expect(page.getByTestId('pending-renamed')).toContainText('http');
    await page.getByTestId('im-save').click();
    const modal = page.getByTestId('impact-modal');
    await expect(modal).toBeVisible();
    await expect(modal.getByTestId('impact-item')).toHaveCount(1);
    await expect(modal).toContainText('renamed');
    const uses = modal.getByTestId('impact-use');
    await expect(uses.filter({ hasText: 'template' }).first()).toBeVisible();
    await expect(uses.filter({ hasText: 'draft' }).first()).toBeVisible();
    await expect(modal.getByTestId('impact-templates').first()).toContainText('will be rewritten');
    await expect(modal.getByTestId('impact-deployments').first()).toContainText('NOT changed');
    await expect(modal.getByTestId('impact-explain')).toContainText('Template sync');
    await expect(modal.getByTestId('impact-rename-everywhere')).toHaveText('Rename in templates');
    await expect(uses.first().locator('a')).toHaveAttribute('href', /\/sites\/[^/]+\/(containers\/hello-api|edge\/route\?id=)/);
    await page.screenshot({ path: `${SHOTS}/img-edit-mock-impact.png` });
    // Cancel keeps the form
    await page.getByTestId('impact-cancel').click();
    await expect(page.getByTestId('im-dirty')).toBeVisible();
    const before = await db(page);
    const headBefore = JSON.stringify(before.revisions[SEED.deployment]);
    await page.getByTestId('im-save').click();
    await modal.getByTestId('impact-rename-everywhere').click();
    await expectToast(page, 'success', /Renamed in .* template reference.*deployment.* not changed/);
    await expect(page.getByTestId('im-save')).toBeDisabled();
    await page.waitForTimeout(200);
    const after = await db(page);
    const ports = (cfg: MockDb['templates'][string]) => cfg.sites.flatMap((s) => s.config.containers.filter((c) => c.serviceSlug === 'hello-api' && c.version === 'v1').flatMap((c) => c.ports.map((p) => p.portName)));
    expect(ports(after.templates[SEED.template]!)).toContain('web');
    expect(ports(after.templates[SEED.template]!)).not.toContain('http');
    expect(after.templates[SEED.template]!.version).toBe(before.templates[SEED.template]!.version + 1);
    // detached: the deployment draft keeps the old name (needs a Template sync or a manual edit)
    expect(JSON.stringify(after.deployments[SEED.deployment]!.draft)).toBe(JSON.stringify(before.deployments[SEED.deployment]!.draft));
    expect(after.deployments[SEED.deployment]!.draftVersion).toBe(before.deployments[SEED.deployment]!.draftVersion);
    expect(JSON.stringify(after.revisions[SEED.deployment])).toBe(headBefore);
    // edge routes to the container followed the rename
    const routes = after.templates[SEED.template]!.sites.flatMap((s) => s.config.edge.routes).filter((r) => r.action.type === 'proxy' && r.action.containerSlug === 'hello-api');
    for (const r of routes) expect(r.action.portName).toBe('web');
    await page.screenshot({ path: `${SHOTS}/img-edit-mock-renamed.png`, fullPage: true });
  });

  test('removing an in-use volume is blocked; port number change warns', async ({ page }) => {
    await login(page, 'admin', URL_API);
    await portRow(page, 0).getByLabel('Port number').fill('8081');
    await portRow(page, 0).getByLabel('Port number').blur();
    await expect(page.getByTestId('port-note')).toContainText('follow it');
    await page.getByTestId('ri-vol-row').first().getByTestId('vol-remove').click();
    await expect(page.getByTestId('pending-removed')).toContainText('data');
    await page.getByTestId('im-save').click();
    const modal = page.getByTestId('impact-modal');
    await expect(modal.getByTestId('impact-blocked')).toBeVisible();
    await expect(modal.getByTestId('impact-rename-everywhere')).toHaveCount(0);
    await expect(modal.getByTestId('impact-use').first()).toContainText('volumes[data]');
    await page.screenshot({ path: `${SHOTS}/img-edit-mock-blocked.png` });
    await page.getByTestId('impact-cancel').click();
    // keep the volume, save only the port retarget → allowed
    await page.getByTestId('ri-add-vol').click();
    await page.getByTestId('im-reset').click();
    await portRow(page, 0).getByLabel('Port number').fill('8081');
    await portRow(page, 0).getByLabel('Port number').blur();
    await page.getByTestId('im-save').click();
    await expectToast(page, 'success', 'Image saved');
  });

  test('viewer sees the form read-only', async ({ page }) => {
    await login(page, 'viewer', URL_API);
    await expect(page.getByTestId('ri-desc')).toBeDisabled();
    await expect(page.getByTestId('im-save')).toHaveCount(0);
    await expect(page.getByTestId('ri-add-port')).toHaveCount(0);
  });
});
