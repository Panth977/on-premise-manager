/** Round T (SPEC §21 #89): every dropdown of the agent GUI is the custom Select / MultiSelect (cloud look) —
 *  no native <select> anywhere (source + every route), keyboard, search, grouped options, 390 px fit. */
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { login, open, shot } from './helpers';

const SRC = fileURLToPath(new URL('../src', import.meta.url));
function walk(d: string): string[] {
  return readdirSync(d).flatMap((n) => {
    const p = join(d, n);
    return statSync(p).isDirectory() ? walk(p) : [p];
  });
}
/** every page route from src/routes ([siteSlug] → app, [containerSlug] → hello-api) */
const ROUTES = walk(join(SRC, 'routes'))
  .filter((f) => f.endsWith('+page.svelte'))
  .map((f) => '/_opm' + ('/' + relative(join(SRC, 'routes'), f)).replace(/\/\+page\.svelte$/, '').replace('[siteSlug]', 'app').replace('[containerSlug]', 'hello-api').replace('[ref]', 'hello-api'))
  .filter((r) => r !== '/_opm/login')
  .sort();
const EXTRA = [
  '/_opm/app/*/status',
  '/_opm/app/*/agent',
  '/_opm/app/app/config/edge/route?new=proxy',
  '/_opm/app/northwind/config/containers/northwind-backend-terminal',
  // nav2: the container page tabs + the tabs that replaced the Tools pages
  ...['logs', 'terminal', 'monitoring', 'inspect', 'processes', 'config'].map((t) => `/_opm/app/db/containers/redis?tab=${t}`),
  '/_opm/app/app/network?tab=access',
  '/_opm/app/app/debug?tab=agent',
  '/_opm/app/app/agent?tab=shell',
];

async function dark(page: Page) {
  await page.addInitScript(() => {
    try {
      localStorage.setItem('opm.theme', 'dark');
    } catch {
      /* sandboxed iframe */
    }
  });
}

test('source: no native <select> in any .svelte file', () => {
  const hits = walk(SRC)
    .filter((f) => f.endsWith('.svelte'))
    .flatMap((f) => readFileSync(f, 'utf8').split('\n').map((l, i) => ({ f, i, l })))
    .filter((x) => /<select[\s>]/.test(x.l) && !x.l.trim().startsWith('//') && !x.l.includes('native <select>'));
  expect(hits.map((h) => `${relative(SRC, h.f)}:${h.i + 1}`)).toEqual([]);
});

test(`every route (${ROUTES.length + EXTRA.length}) renders without a native <select>`, async ({ page }) => {
  test.setTimeout(180_000);
  expect(ROUTES).toContain('/_opm/app/app/network');
  expect(ROUTES).toContain('/_opm/app/app/containers/hello-api');
  await login(page, '/_opm/app');
  for (const r of [...ROUTES, ...EXTRA]) {
    await open(page, r);
    await page.waitForTimeout(150);
    expect(await page.locator('select').count(), `native <select> on ${r}`).toBe(0);
  }
});

test('keyboard: Enter / arrows / Home / End / Escape / type-ahead (open and closed)', async ({ page }) => {
  await login(page, '/_opm/app/app/config/containers/hello-api-2');
  const t = page.getByTestId('env-choice-NODE_ENV'); // integration · dc · worker · pg-listen · terminal · migrate
  await expect(t).toHaveAttribute('data-value', 'dc');
  await t.focus();
  await page.keyboard.press('Enter');
  const list = page.getByTestId('env-choice-NODE_ENV-list');
  await expect(list).toBeVisible();
  await expect(list.locator('.opt.active')).toHaveAttribute('data-value', 'dc'); // starts on the current value
  await page.keyboard.press('ArrowDown');
  await expect(list.locator('.opt.active')).toHaveAttribute('data-value', 'worker');
  await page.keyboard.press('End');
  await expect(list.locator('.opt.active')).toHaveAttribute('data-value', 'migrate');
  await page.keyboard.press('Home');
  await expect(list.locator('.opt.active')).toHaveAttribute('data-value', 'integration');
  await page.keyboard.press('ArrowUp'); // stays on the first
  await expect(list.locator('.opt.active')).toHaveAttribute('data-value', 'integration');
  await page.keyboard.press('Escape'); // closes, value unchanged, focus back on the trigger
  await expect(list).toBeHidden();
  await expect(t).toHaveAttribute('data-value', 'dc');
  await expect(t).toBeFocused();
  await page.keyboard.press('ArrowDown'); // opens
  await expect(list).toBeVisible();
  await page.keyboard.type('te'); // type-ahead
  await expect(list.locator('.opt.active')).toHaveAttribute('data-value', 'terminal');
  await page.keyboard.press('Enter');
  await expect(list).toBeHidden();
  await expect(t).toHaveAttribute('data-value', 'terminal');
  await expect(page.getByTestId('resolved-preview')).toContainText('/app/helper/config/env/terminal.ts');
  await page.keyboard.press('w'); // closed type-ahead picks like a native select
  await expect(t).toHaveAttribute('data-value', 'worker');
  // click outside closes
  await t.click();
  await expect(list).toBeVisible();
  await page.getByRole('heading', { name: 'hello-api-2', exact: true }).click();
  await expect(list).toBeHidden();
});

test('grouped + searchable source picker: groups, icons, badges, search, Enter picks the first match; screenshots', async ({ page }) => {
  await login(page, '/_opm/app/app/config/containers/hello-api');
  const t = page.getByTestId('vol-data-source');
  await t.scrollIntoViewIfNeeded();
  await t.click();
  const list = page.getByTestId('vol-data-source-list');
  await expect(list.getByRole('group')).toHaveCount(4);
  await expect(list.locator('.gl')).toHaveText(['Data volumes', 'Files', 'Secrets', 'Advanced']);
  await expect(list.getByTestId('vol-data-source-opt-volume:hello-api-2-data')).toContainText('no backup');
  await expect(list.getByTestId('vol-data-source-opt-new-volume')).toHaveClass(/act/);
  const search = page.getByTestId('vol-data-source-search');
  await expect(search).toBeFocused();
  await shot(page, 'select-grouped-light', false);
  await search.fill('token');
  await expect(list.getByRole('option')).toHaveCount(1);
  await shot(page, 'select-search-light', false);
  await page.keyboard.press('Enter');
  await expect(list).toBeHidden();
  await expect(t).toHaveAttribute('data-value', 'secret:api-token');
  // action entry opens the Add volume modal and never becomes the value
  await t.click();
  await page.getByTestId('vol-data-source-opt-new-volume').click();
  await expect(page.getByTestId('volume-modal')).toBeVisible();
  await expect(t).toHaveAttribute('data-value', 'secret:api-token');
});

test('multi-select (debug modules) + dark', async ({ page }) => {
  await login(page, '/_opm/app/app/debug');
  await expect(page.getByTestId('site-select')).toHaveCount(0); // the Sites tree replaced the site picker
  const ms = page.getByTestId('debug-module');
  await ms.click();
  await page.getByTestId('debug-module-opt-mesh').click();
  await page.getByTestId('debug-module-opt-edge').click();
  await page.getByTestId('debug-module-opt-auth').click();
  await expect(page.getByTestId('debug-module-list')).toBeVisible(); // stays open while picking
  await expect(page.getByTestId('debug-module-list')).toContainText('3 selected');
  await shot(page, 'select-multi-light', false);
  await page.keyboard.press('Escape');
  await expect(ms).toHaveAttribute('data-value', 'mesh,edge,auth');
  await expect(ms).toContainText('+1');
  await dark(page);
  await open(page, '/_opm/app/app/config/containers/hello-api');
  await page.getByTestId('vol-data-source').scrollIntoViewIfNeeded();
  await page.getByTestId('vol-data-source').click();
  await shot(page, 'select-grouped-dark', false);
  await page.keyboard.press('Escape');
  await open(page, '/_opm/app/app/debug');
  await page.getByTestId('debug-module').click();
  await page.getByTestId('debug-module-opt-mesh').click();
  await shot(page, 'select-multi-dark', false);
});

test('inside a modal <dialog> (file drawer) the list is on top and usable (top layer)', async ({ page }) => {
  await login(page, '/_opm/app/files');
  await page.getByTestId('edit-api-config').click();
  const dr = page.getByTestId('file-drawer');
  await expect(dr).toBeVisible();
  const trig = dr.getByTestId('preview-site');
  await trig.click();
  const pop = page.getByTestId('preview-site-list');
  await expect(pop).toBeVisible();
  const box = (await pop.boundingBox())!;
  const hit = await page.evaluate(([x, y]) => document.elementFromPoint(x, y)?.closest('[popover]')?.getAttribute('data-testid') ?? null, [box.x + box.width / 2, box.y + box.height - 8]);
  expect(hit).toBe('preview-site-list');
  await shot(page, 'select-in-drawer-light', false);
  await page.keyboard.press('ArrowDown');
  const next = await pop.locator('.opt.active').getAttribute('data-value');
  expect(next).not.toBe('app');
  await page.keyboard.press('Enter');
  await expect(pop).toBeHidden();
  await expect(trig).toHaveAttribute('data-value', next!);
  await trig.click();
  await page.keyboard.press('Escape'); // closes only the list, not the drawer
  await expect(pop).toBeHidden();
  await expect(dr).toBeVisible();
});

test('390 px: the list fits the viewport', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await login(page, '/_opm/app/app/config/containers/hello-api');
  const t = page.getByTestId('vol-data-source');
  await t.scrollIntoViewIfNeeded();
  await t.click();
  const list = page.getByTestId('vol-data-source-list');
  await expect(list).toBeVisible();
  const b = (await list.boundingBox())!;
  expect(b.x).toBeGreaterThanOrEqual(0);
  expect(b.x + b.width).toBeLessThanOrEqual(390);
  expect(b.y).toBeGreaterThanOrEqual(0);
  expect(b.y + b.height).toBeLessThanOrEqual(844);
  await shot(page, 'select-grouped-m', false);
});
