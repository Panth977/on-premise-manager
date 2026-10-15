/** §21 #97 at 390 px: every dev tools page fits without horizontal page scroll; a SQL query and an HTTP request run. */
import { test, expect } from './fixtures';
import { login, open, setEditor, shot } from './helpers';

const PAGES = ['devtools', 'devtools/sql?c=p:app-db', 'devtools/redis', 'devtools/mqtt', 'devtools/http?c=p:api', 'devtools/history'];
const fits = async (page: import('@playwright/test').Page, what: string) => {
  const over = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  expect(over, `horizontal overflow on ${what}`).toBeLessThanOrEqual(1);
};

test('390 px: dev tools pages fit; SQL + HTTP run', async ({ page }) => {
  await login(page, '/_opm/app/app/devtools');
  for (const p of PAGES) {
    if (p !== 'devtools') await open(page, `/_opm/app/app/${p}`);
    await page.waitForTimeout(700);
    await fits(page, p);
    await shot(page, `devtools-${p.replace(/^devtools\/?/, '').replace(/\?.*$/, '') || 'connections'}-m`, false);
  }
  await open(page, '/_opm/app/app/devtools/sql?c=p:app-db');
  await setEditor(page, 'sql-editor', 'SELECT * FROM devices ORDER BY label');
  await page.getByTestId('sql-run').click();
  await expect(page.getByTestId('results')).toContainText('40 rows');
  await fits(page, 'sql results');
  await page.getByTestId('results').scrollIntoViewIfNeeded();
  await shot(page, 'devtools-sql-results-m', false);
  await open(page, '/_opm/app/app/devtools/http?c=p:api');
  await page.getByTestId('http-send').click();
  await expect(page.getByTestId('http-status')).toContainText('200');
  await fits(page, 'http response');
  await page.getByTestId('http-status').scrollIntoViewIfNeeded();
  await shot(page, 'devtools-http-response-m', false);
});
