/** Round T: config page polish — volumes table never clipped, compact "shared · N" badge + popover, ⓘ tooltips,
 *  sticky summary, no page-level x-scroll at 390 px. Screenshots screenshots/polish-*.png (light, dark, m). */
import { test, expect } from './fixtures';
import { login, open, shot } from './helpers';
import type { Page } from '@playwright/test';

const EDITOR = '/_opm/app/northwind/config/containers/northwind-backend-terminal';

/** dark theme for later loads (try/catch: sandboxed not-found preview iframes have no localStorage) */
async function dark(page: Page) {
  await page.addInitScript(() => {
    try {
      localStorage.setItem('opm.theme', 'dark');
    } catch {
      /* sandboxed iframe */
    }
  });
}
async function noPageXScroll(page: Page) {
  const r = await page.evaluate(() => ({
    sw: document.scrollingElement!.scrollWidth,
    vw: window.innerWidth,
    wide: [...document.querySelectorAll('body *')].filter((e) => e.getBoundingClientRect().right > window.innerWidth + 1 && !e.closest('.table-wrap')).slice(0, 5).map((e) => `${e.tagName}.${e.className}`),
  }));
  expect(r.sw, `page scrolls sideways: ${r.wide.join(' | ')}`).toBeLessThanOrEqual(r.vw + 1);
}

test('container editor: volumes table fits its card, shared badge + popover, ⓘ tooltip, sticky summary', async ({ page }) => {
  await login(page, EDITOR);
  const row = page.getByTestId('vol-row-config');
  await row.scrollIntoViewIfNeeded();
  // the first column is fully inside the card (the user's screenshot clipped it on the left)
  const card = page.locator('#sec-volumes');
  const cb = (await card.boundingBox())!;
  const first = (await row.locator('.cell-name').boundingBox())!;
  expect(first.x).toBeGreaterThanOrEqual(cb.x);
  expect(first.x + first.width).toBeLessThanOrEqual(cb.x + cb.width);
  // at 1360 px the rows need no sideways scrolling inside the card either (shared form, §21 #102: grid rows)
  const wrap = await card.locator('.rows').first().evaluate((el) => ({ sw: el.scrollWidth, cw: el.clientWidth, sl: el.scrollLeft }));
  expect(wrap.sw).toBeLessThanOrEqual(wrap.cw + 1);
  expect(wrap.sl).toBe(0);
  await expect(row.getByTestId('vol-config-imgkind')).toHaveText('File');
  // compact badge instead of "shared copy · also used by A, B, C…"
  const badge = page.getByTestId('vol-config-shared-copy');
  await expect(badge).toHaveText('shared · 8 containers');
  await expect(row).not.toContainText('also used by');
  await badge.click();
  const list = page.getByTestId('vol-config-shared-copy-list');
  await expect(list).toBeVisible();
  await expect(list).toContainText('northwind-backend-dc');
  await expect(list.locator('li.cur')).toContainText('this container');
  await shot(page, 'polish-container-volumes-light', false);
  await page.keyboard.press('Escape');
  await expect(list).toBeHidden();
  // help paragraph → one-line hint + ⓘ tooltip (hover)
  await page.getByTestId('mount-help').hover();
  const tip = page.getByTestId('mount-help-text');
  await expect(tip).toBeVisible();
  await expect(tip).toContainText('Must start with "/"');
  await expect(page.getByTestId('same-image')).toHaveText('same image · 8 containers');
  // the sticky aside (On this page + Summary, shared FormPage) stays visible after scrolling to the bottom
  await page.mouse.move(10, 10);
  await page.evaluate(() => window.scrollTo(0, document.body.scrollHeight));
  await page.waitForTimeout(200);
  await expect(page.getByTestId('container-summary')).toBeInViewport();
  const sb = (await page.getByRole('navigation', { name: 'On this page' }).boundingBox())!;
  expect(sb.y).toBeGreaterThanOrEqual(0);
  expect(sb.y).toBeLessThan(120);
  await shot(page, 'polish-container-summary-sticky-light', false);
});

test('container editor: section jump links + top of the page (light + dark)', async ({ page }) => {
  await login(page, EDITOR);
  await expect(page.getByRole('navigation', { name: 'On this page' })).toBeVisible();
  await shot(page, 'polish-container-top-light', false);
  await page.getByRole('navigation', { name: 'On this page' }).getByRole('link', { name: /Volumes/ }).click();
  await expect(page.locator('#sec-volumes')).toBeInViewport();
  await dark(page);
  await open(page, EDITOR);
  await shot(page, 'polish-container-top-dark', false);
  await page.getByTestId('vol-row-config').scrollIntoViewIfNeeded();
  await page.getByTestId('vol-config-shared-copy').click();
  await shot(page, 'polish-container-volumes-dark', false);
});

test('other config pages: one-line hints with ⓘ, no walls of text', async ({ page }) => {
  await login(page, '/_opm/app/northwind/config/edge');
  await expect(page.locator('.order-note')).toContainText('only regex routes move');
  await shot(page, 'polish-edge-light');
  await open(page, '/_opm/app/northwind/config/settings');
  await expect(page.getByTestId('gui-ways')).toContainText(/http:\/\/\S+:7082\/_opm\//);
  await shot(page, 'polish-settings-light');
  await open(page, '/_opm/app/app/config/volumes');
  await expect(page.getByTestId('volumes-info')).toContainText('Host paths are never backed up');
  await shot(page, 'polish-volumes-light');
  await open(page, '/_opm/app/northwind/config/containers');
  await shot(page, 'polish-containers-light');
  await open(page, '/_opm/app/northwind/config/edge/import');
  await expect(page.getByTestId('nginx-import')).toBeVisible();
  await dark(page);
  for (const [n, u] of [
    ['edge', '/_opm/app/northwind/config/edge'],
    ['settings', '/_opm/app/northwind/config/settings'],
    ['volumes', '/_opm/app/app/config/volumes'],
  ]) {
    await open(page, u);
    await shot(page, `polish-${n}-dark`);
  }
});

test('390 px: no page-level sideways scroll; volumes rows stack', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await login(page, EDITOR);
  await noPageXScroll(page);
  await page.getByTestId('vol-row-config').scrollIntoViewIfNeeded();
  await expect(page.getByTestId('vol-config-shared-copy')).toBeVisible();
  await shot(page, 'polish-container-volumes-m', false);
  await shot(page, 'polish-container-m');
  for (const [n, u] of [
    ['edge', '/_opm/app/northwind/config/edge'],
    ['settings', '/_opm/app/northwind/config/settings'],
    ['volumes', '/_opm/app/app/config/volumes'],
  ]) {
    await open(page, u);
    await noPageXScroll(page);
    await shot(page, `polish-${n}-m`);
  }
});
