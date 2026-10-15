import { test, expect, type Page } from '@playwright/test';
import { login, expectToast, SEED, pickIcon, pick } from './helpers';

/** Seed: 11 audit entries (incl. 1 icon.uploaded) (src/lib/mock/db.ts): 1 forbidden error by the viewer, 1 failed system import, 4 account / 2 image / 2 tag … */
async function createTag(page: Page, slug: string, label: string) {
  await page.goto('/app/tags');
  await page.getByTestId('new-tag').click();
  await page.getByTestId('tag-label').fill(label);
  await page.getByTestId('tag-slug-edit').click();
  await page.getByTestId('tag-slug').fill(slug);
  await pickIcon(page, 'tag-icon', 'flask-conical');
  await page.getByTestId('swatch-16a34a').click();
  await page.getByTestId('tag-save').click();
  await expectToast(page, 'success', `Tag ${slug} created`);
}

test.describe('audit log', () => {
  test('a write appears, filters narrow via the API + URL, drawer shows the diff', async ({ page }) => {
    await login(page, 'admin', '/app/tags');
    await createTag(page, 'lab', 'Lab');
    await page.goto('/app/audit-log');
    const first = page.getByTestId('audit-row').first();
    await expect(first).toHaveAttribute('data-action', 'tag.created');
    await expect(first).toContainText('admin@example.com');
    await expect(first).toContainText('tags/lab');
    await expect(first.getByTestId('audit-outcome')).toHaveText('ok');

    // drawer: before → after of the created tag
    await first.locator('td').first().click();
    const drawer = page.getByTestId('audit-drawer');
    await expect(drawer).toBeVisible();
    await expect(drawer).toContainText('createTag');
    await expect(drawer).toContainText('127.0.0.1');
    await expect(drawer.getByTestId('audit-change').filter({ hasText: 'label' })).toContainText('Lab');
    await expect(drawer.getByTestId('audit-change').filter({ hasText: 'color' })).toContainText('#16a34a');
    await page.keyboard.press('Escape');
    await expect(drawer).toHaveCount(0);

    // action prefix
    await pick(page, 'audit-filter-action', 'tag.');
    await expect(page).toHaveURL(/action=tag\./);
    await expect(page.getByTestId('audit-row')).toHaveCount(3);
    for (const r of await page.getByTestId('audit-row').all()) await expect(r).toHaveAttribute('data-action', /^tag\./);

    // + outcome error → only the viewer's forbidden createTag
    await pick(page, 'audit-filter-outcome', 'error');
    await expect(page).toHaveURL(/outcome=error/);
    await expect(page.getByTestId('audit-row')).toHaveCount(1);
    await expect(page.getByTestId('audit-row')).toContainText('viewer@example.com');
    await expect(page.getByTestId('audit-row').getByTestId('audit-outcome')).toContainText('403 forbidden');

    // survives a reload
    await page.reload();
    await expect(page.getByTestId('audit-filter-outcome')).toHaveAttribute('data-value', 'error');
    await expect(page.getByTestId('audit-row')).toHaveCount(1);

    // clear, then target type
    await page.getByTestId('audit-clear').click();
    await expect(page.getByTestId('audit-row')).toHaveCount(12);
    await pick(page, 'audit-filter-type', 'account');
    await expect(page.getByTestId('audit-row')).toHaveCount(3);

    // actor = system
    await page.getByTestId('audit-clear').click();
    await page.getByTestId('audit-filter-actor').locator('[role=combobox]').click();
    await page.getByRole('option', { name: /^system jobs/ }).click();
    await expect(page).toHaveURL(/actor=system/);
    await expect(page.getByTestId('audit-row')).toHaveCount(2);

    // date range in the future → empty
    await page.getByTestId('audit-clear').click();
    await page.getByTestId('audit-filter-from').fill('2099-01-01');
    await expect(page.getByTestId('empty-state')).toContainText('No matching entries');
  });

  test('a forbidden request by the viewer is logged as an error entry', async ({ page }) => {
    await login(page, 'viewer', '/app/tags');
    await expect(page.getByTestId('tag-row').first()).toBeVisible();
    const status = await page.evaluate(async (uid) => {
      const f = (window as unknown as { __opmMockFetch: typeof fetch }).__opmMockFetch;
      const r = await f(`${location.origin}/v1/tags/gov`, { method: 'PATCH', headers: { Authorization: `Bearer mock:${uid}`, 'Content-Type': 'application/json' }, body: JSON.stringify({ label: 'Hacked' }) });
      return r.status;
    }, SEED.uidViewer);
    expect(status).toBe(403);
    await page.evaluate(() => localStorage.removeItem('opm.mock.session'));
    await login(page, 'admin', '/app/audit-log?outcome=error&type=tag');
    const rows = page.getByTestId('audit-row');
    await expect(rows).toHaveCount(2);
    await expect(rows.first()).toHaveAttribute('data-action', 'tag.updated');
    await expect(rows.first()).toContainText('viewer@example.com');
    await rows.first().locator('td').first().click();
    await expect(page.getByTestId('audit-error')).toContainText('forbidden');
    await expect(page.getByTestId('audit-drawer')).toContainText('patchTag');
    await expect(page.getByTestId('audit-no-changes')).toBeVisible();
  });

  test('secrets are masked and the seed system entry renders', async ({ page }) => {
    await login(page, 'admin', '/app/audit-log?action=template.updated');
    await expect(page.getByTestId('audit-row')).toHaveCount(1);
    await page.getByTestId('audit-row').locator('td').first().click();
    const ch = page.getByTestId('audit-change').filter({ hasText: 'secrets[api-token].value' });
    await expect(ch.getByTestId('audit-secret')).toContainText('secret changed');
    await expect(ch).toContainText('***');
  });
});

test.describe('history panel', () => {
  test('edit a tag → History shows the update with before → after', async ({ page }) => {
    await login(page, 'admin', '/app/tags');
    const row = page.getByTestId('tag-row').filter({ hasText: 'gov' });
    await row.getByRole('button', { name: 'Edit gov' }).click();
    await page.getByTestId('tag-label').fill('Gov sector');
    await page.getByTestId('tag-save').click();
    await expectToast(page, 'success', 'Tag saved');
    await row.getByTestId('history-button').click();
    const drawer = page.getByTestId('history-drawer');
    await expect(drawer.getByTestId('history-entry')).toHaveCount(1);
    await drawer.getByTestId('history-entry').first().getByRole('button').click();
    const ch = drawer.getByTestId('audit-change').filter({ hasText: 'label' });
    await expect(ch).toContainText('Government');
    await expect(ch).toContainText('Gov sector');
  });

  test('template History after a save; viewer sees no History button', async ({ page }) => {
    await login(page, 'admin', `/app/templates/${SEED.template}`);
    await page.getByTestId('history-button').click();
    const drawer = page.getByTestId('history-drawer');
    await expect(drawer.getByTestId('history-entry')).toHaveCount(1);
    await expect(drawer.getByTestId('history-entry').first()).toContainText('template.updated');
    await page.keyboard.press('Escape');
    await page.goto(`/app/accounts/${SEED.uidViewer}`);
    await page.getByTestId('history-button').click();
    await expect(page.getByTestId('history-drawer').getByTestId('history-entry')).toHaveCount(2);
    await page.evaluate(() => localStorage.removeItem('opm.mock.session'));
    await login(page, 'viewer', `/app/templates/${SEED.template}`);
    await expect(page.getByTestId('template-version')).toBeVisible();
    await expect(page.getByTestId('history-button')).toHaveCount(0);
  });
});
