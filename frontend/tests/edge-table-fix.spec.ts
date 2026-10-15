import { test, expect, type Page } from '@playwright/test';
import { login, expectToast } from './helpers';

/**
 * Regression (mock): routes table of the edge tab with a warning row (user screenshot, W_RESPONSE_HEADER_AS_REQUEST).
 * - every cell of the row (incl. the indicator + action cells) has the same background, no inner fixed-height box
 * - the warning text is not in the Route id cell; a compact badge opens a popover with the one-click fix
 * - rows stay short, column widths do not depend on the warning, the page never scrolls sideways
 */
const EDGE = '/app/templates/TplNorthwindClient04/sites/site-01/edge';
const ROUTE = 'x-ez-terminal-s3';

async function cellBackgrounds(page: Page, testid: string): Promise<string[]> {
  return page.getByTestId(testid).locator(':scope > td').evaluateAll((tds) => tds.map((td) => getComputedStyle(td).backgroundColor));
}
async function headWidths(page: Page): Promise<number[]> {
  return page.getByTestId('routes-table').locator('thead th').evaluateAll((ths) => ths.slice(0, 5).map((th) => Math.round(th.getBoundingClientRect().width)));
}
async function noPageScrollX(page: Page) {
  const { sw, cw } = await page.evaluate(() => ({ sw: document.documentElement.scrollWidth, cw: document.documentElement.clientWidth }));
  expect(sw, 'page must not scroll sideways').toBeLessThanOrEqual(cw);
}

test.describe('edge routes table: warning rows', () => {
  test('1440 px: one background per row, short rows, badge popover + move fix', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await login(page, 'admin', EDGE);
    const row = page.getByTestId(`route-row-${ROUTE}`);
    await expect(row).toBeVisible();
    await expect(row).toHaveClass(/\bwarn\b/);

    // every cell (incl. indicator + actions) shares the highlight, which is not the plain surface
    const bgs = await cellBackgrounds(page, `route-row-${ROUTE}`);
    expect(bgs.length).toBe(8);
    expect(new Set(bgs).size, `backgrounds ${bgs.join(' | ')}`).toBe(1);
    const plain = await cellBackgrounds(page, 'route-row-root');
    expect(bgs[0]).not.toBe(plain[0]);
    // the summary sub-row has the same highlight
    expect(await cellBackgrounds(page, `route-sub-${ROUTE}`)).toEqual([bgs[0]]);
    // cells are table cells (no flex td) and share the row's top + height
    const boxes = await row.locator(':scope > td').evaluateAll((tds) => tds.map((td) => ({ d: getComputedStyle(td).display, t: Math.round(td.getBoundingClientRect().top), h: Math.round(td.getBoundingClientRect().height) })));
    for (const b of boxes) {
      expect(b.d).toBe('table-cell');
      expect(b.t).toBe(boxes[0]!.t);
      expect(b.h).toBe(boxes[0]!.h);
    }

    // the warning sentence is not inside the Route id cell; rows stay short
    const idCell = row.locator(':scope > td').nth(3);
    await expect(idCell).toHaveText(ROUTE);
    const rowH = (await row.boundingBox())!.height;
    const subH = (await page.getByTestId(`route-sub-${ROUTE}`).boundingBox())!.height;
    expect(rowH).toBeLessThan(120);
    expect(rowH + subH).toBeLessThan(120);
    // the sub-row summary is one line (ellipsis), not wrapped
    expect(subH).toBeLessThan(32);

    const before = await headWidths(page);
    await page.getByTestId('routes-table').scrollIntoViewIfNeeded();
    await page.screenshot({ path: 'screenshots/fix-edge-table-mock.png', fullPage: true });

    // badge → popover with the full text + one-click fix
    const badge = page.getByTestId(`route-issues-${ROUTE}`);
    await expect(badge).toHaveText(/1 warning/);
    await badge.click();
    const pop = page.getByTestId(`route-issues-pop-${ROUTE}`);
    await expect(pop).toBeVisible();
    await expect(pop).toContainText('Cache-Control is set as a REQUEST header');
    await page.screenshot({ path: 'screenshots/fix-edge-table-popover-mock.png' });
    await pop.getByTestId(`route-move-${ROUTE}-Cache-Control`).click();
    await expectToast(page, 'success', 'Cache-Control moved');
    await expect(pop).toHaveCount(0);
    await expect(badge).toHaveCount(0);
    await expect(page.getByTestId(`route-sub-${ROUTE}`)).toHaveCount(0);
    await expect(row).not.toHaveClass(/\bwarn\b/);
    await expect(page.getByTestId('dirty-dot')).toBeVisible();
    // column widths do not depend on the warning
    const after = await headWidths(page);
    after.forEach((w, i) => expect(Math.abs(w - before[i]!), `column ${i}: ${before[i]} → ${w}`).toBeLessThanOrEqual(2));

    // the fix really moved the header: Test a URL answers it as a response header
    await page.getByTestId('test-url-input').fill('http://localhost/x/ez-terminal/s3/a.png');
    await page.getByTestId('test-run').click();
    await expect(page.getByTestId('test-resp-set-Cache-Control')).toContainText('public, max-age=3600');
    await expect(page.getByTestId('test-req-headers')).not.toContainText('Cache-Control');
    await noPageScrollX(page);
  });

  test('390 px: the table scrolls inside its container, the page does not', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await login(page, 'admin', EDGE);
    await expect(page.getByTestId(`route-row-${ROUTE}`)).toBeVisible();
    await noPageScrollX(page);
    const wrap = page.getByTestId('routes-table').locator('xpath=..');
    const { sw, cw, ox } = await wrap.evaluate((el) => ({ sw: el.scrollWidth, cw: el.clientWidth, ox: getComputedStyle(el).overflowX }));
    expect(ox).toBe('auto');
    expect(sw).toBeGreaterThan(cw);
    const bgs = await cellBackgrounds(page, `route-row-${ROUTE}`);
    expect(new Set(bgs).size).toBe(1);
    await page.getByTestId('routes-table').scrollIntoViewIfNeeded();
    await page.screenshot({ path: 'screenshots/fix-edge-table-390-mock.png' });
    // the popover fits the phone viewport
    await page.getByTestId(`route-issues-${ROUTE}`).scrollIntoViewIfNeeded();
    await page.getByTestId(`route-issues-${ROUTE}`).click();
    const box = (await page.getByTestId(`route-issues-pop-${ROUTE}`).boundingBox())!;
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(390);
    await noPageScrollX(page);
  });
});
