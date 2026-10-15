/** Every dropdown of the cloud console is the custom Select / GroupedSelect (mirror of agent/gui/tests/no-native-select.spec.ts):
 *  no native <select> anywhere (source + every route), the Activities filters (single values, URL query, keyboard), 390 px fit. */
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import { test, expect } from '@playwright/test';
import { login, pick, SEED } from './helpers';

const SRC = fileURLToPath(new URL('../src', import.meta.url));
function walk(d: string): string[] {
  return readdirSync(d).flatMap((n) => {
    const p = join(d, n);
    return statSync(p).isDirectory() ? walk(p) : [p];
  });
}
/** dynamic route segments → seeded mock ids (src/lib/mock/db.ts, tests/helpers.ts SEED) */
const PARAMS: Record<string, string> = {
  deploymentId: SEED.deployment,
  templateId: SEED.template,
  uid: SEED.uidAdmin,
  serviceSlug: SEED.svcApi,
  version: 'v1',
  notFoundPageId: SEED.page404,
  siteSlug: 'app',
  containerSlug: 'hello-api',
};
const ROUTES = walk(join(SRC, 'routes'))
  .filter((f) => f.endsWith('+page.svelte'))
  .map((f) =>
    ('/' + relative(join(SRC, 'routes'), f))
      .replace(/\/\+page\.svelte$/, '')
      .replace(/\/\([^)]+\)/g, '') // route groups: (root)
      .replace(/\[(\w+)\]/g, (_, k: string) => {
        if (!PARAMS[k]) throw new Error(`no seeded id for [${k}] — add it to PARAMS`);
        return PARAMS[k]!;
      }),
  )
  .map((r) => r || '/')
  .filter((r) => r !== '/login')
  .sort();
const D = `/app/deployments/${SEED.deployment}`;
const EXTRA = [`${D}/sites/app/edge/route?new=proxy`, `/app/templates/${SEED.template}/sites/app/edge/route?new=proxy`, `${D}/activities?type=install-token.&origin=cloud`, '/app/audit-log?action=tag.&outcome=error'];

test('source: no native <select> in any .svelte file', () => {
  const hits = walk(SRC)
    .filter((f) => f.endsWith('.svelte'))
    .flatMap((f) => readFileSync(f, 'utf8').split('\n').map((l, i) => ({ f, i, l })))
    .filter((x) => /<select[\s>]/.test(x.l) && !x.l.trim().startsWith('//') && !x.l.includes('native <select>'));
  expect(hits.map((h) => `${relative(SRC, h.f)}:${h.i + 1}`)).toEqual([]);
});

test(`every route renders without a native <select>`, async ({ page }) => {
  test.setTimeout(240_000);
  expect(ROUTES).toContain(`${D}/activities`);
  expect(ROUTES.length).toBeGreaterThan(40);
  await login(page, 'admin', '/app/deployments');
  for (const r of [...ROUTES, ...EXTRA]) {
    await page.goto(r);
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(150);
    expect(await page.locator('select').count(), `native <select> on ${r}`).toBe(0);
  }
});

test('activities: Type + Origin filters are custom single-value selects (URL query, filtered list, data-value)', async ({ page }) => {
  await login(page, 'admin', `${D}/activities`);
  await expect(page.getByTestId('activity-row').first()).toBeVisible();
  const type = page.getByTestId('f-type');
  const origin = page.getByTestId('f-origin');
  await expect(type).toHaveAttribute('data-value', '');
  await expect(type).toContainText('Any type');

  await pick(page, 'f-type', 'install-token.');
  await expect(page).toHaveURL(/type=install-token\./);
  await expect(type).toContainText('install-token.*');
  await pick(page, 'f-origin', 'cloud');
  await expect(page).toHaveURL(/origin=cloud/);
  await expect(origin).toHaveAttribute('data-value', 'cloud');
  const rows = page.getByTestId('activity-row');
  await expect(rows).toHaveCount(2);
  for (const r of await rows.all()) await expect(r).toContainText('install-token.created');

  // the list groups the type prefixes and the full types
  await type.click();
  const tl = page.getByTestId('f-type-list');
  await expect(tl.getByRole('group')).toHaveCount(3);
  await expect(tl.getByTestId('f-type-opt-install-token.')).toHaveAttribute('aria-selected', 'true');
  await expect(tl.getByTestId('f-type-opt-agent.upgraded')).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(tl).toHaveCount(0);

  // the filter survives a reload (URL)
  await page.reload();
  await expect(type).toHaveAttribute('data-value', 'install-token.');
  await expect(origin).toHaveAttribute('data-value', 'cloud');
  await expect(rows).toHaveCount(2);

  // back to "any": a single value again
  await pick(page, 'f-type', '');
  await expect(page).not.toHaveURL(/type=/);
  await expect(rows.first()).toBeVisible();
  await expect.poll(() => rows.count()).toBeGreaterThan(2);
});

test('activities: keyboard — Enter opens, ↓ moves, Enter picks, Escape closes, type-ahead', async ({ page }) => {
  await login(page, 'admin', `${D}/activities`);
  const t = page.getByTestId('f-origin');
  const list = page.getByTestId('f-origin-list');
  await expect(t).toHaveAttribute('data-value', '');
  await t.focus();
  await page.keyboard.press('Enter');
  await expect(list).toBeVisible();
  await expect(list.locator('.opt.active')).toHaveAttribute('data-value', ''); // starts on the current value
  await page.keyboard.press('ArrowDown');
  await expect(list.locator('.opt.active')).toHaveAttribute('data-value', 'agent');
  await page.keyboard.press('ArrowDown');
  await expect(list.locator('.opt.active')).toHaveAttribute('data-value', 'cloud');
  await page.keyboard.press('Enter');
  await expect(list).toHaveCount(0);
  await expect(t).toHaveAttribute('data-value', 'cloud');
  await expect(t).toBeFocused();
  await expect(page).toHaveURL(/origin=cloud/);
  // Escape closes without changing the value
  await page.keyboard.press('Enter');
  await expect(list).toBeVisible();
  await page.keyboard.press('End');
  await expect(list.locator('.opt.active')).toHaveAttribute('data-value', 'system');
  await page.keyboard.press('Escape');
  await expect(list).toHaveCount(0);
  await expect(t).toHaveAttribute('data-value', 'cloud');
  await expect(t).toBeFocused();
  // type-ahead on the closed trigger picks like a native select
  await page.keyboard.press('s');
  await expect(t).toHaveAttribute('data-value', 'system');
  await expect(page).toHaveURL(/origin=system/);
  // type-ahead in the open list moves the active option
  await page.keyboard.press('ArrowDown');
  await expect(list).toBeVisible();
  await page.keyboard.type('ag');
  await expect(list.locator('.opt.active')).toHaveAttribute('data-value', 'agent');
  await page.keyboard.press('Enter');
  await expect(t).toHaveAttribute('data-value', 'agent');
});

test('390 px: the Activities filter lists fit the viewport', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await login(page, 'admin', `${D}/activities`);
  for (const id of ['f-type', 'f-site', 'f-via']) {
    const t = page.getByTestId(id);
    await t.scrollIntoViewIfNeeded();
    await t.click();
    const list = page.getByTestId(`${id}-list`);
    await expect(list).toBeVisible();
    const b = (await list.boundingBox())!;
    expect(b.x, id).toBeGreaterThanOrEqual(0);
    expect(b.x + b.width, id).toBeLessThanOrEqual(390);
    expect(b.y, id).toBeGreaterThanOrEqual(0);
    expect(b.y + b.height, id).toBeLessThanOrEqual(844);
    const hscroll = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);
    expect(hscroll, `${id}: horizontal page scroll`).toBe(false);
    await page.keyboard.press('Escape');
    await expect(list).toHaveCount(0);
  }
});
