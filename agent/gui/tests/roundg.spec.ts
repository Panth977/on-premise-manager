/**
 * Round G (SPEC §21 #84 e): nothing is "shown once". RevealValue on the secrets page: masked, View (logged by the
 * agent), 60 s countdown ring, auto re-mask (fake clock), re-mask on navigation and on a hidden tab, never stored in
 * Web Storage, rate limit 30 / session / minute. Login help points to the cloud Setup page. Bundle grep.
 */
import { readdirSync, readFileSync, statSync, existsSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { test, expect } from './fixtures';
import { login, KEY, nav } from './helpers';

const SECRET = 'Zx8Kq2Lm9Np4Rs7Tv1Wy3Ab5';
const GUI = join(dirname(fileURLToPath(import.meta.url)), '..');

test('RevealValue: masked → View → 60 s countdown → re-masks at 60 s (fake clock), logged, not in storage', async ({ page }) => {
  await page.clock.install();
  await login(page, '/_opm/app/secrets');
  const val = page.getByTestId('secret-api-token-value');
  await expect(val).toHaveText('•••');
  await expect(page.getByTestId('secret-api-token')).toHaveAttribute('data-state', 'masked');
  await page.getByTestId('secret-api-token-view').click();
  await expect(val).toHaveText(SECRET);
  await expect(page.getByTestId('secret-api-token-left')).toHaveText('60s');
  await expect(page.getByTestId('secret-api-token-copy')).toBeVisible();
  // never cached in Web Storage
  const stored = await page.evaluate(() => JSON.stringify({ ...localStorage }) + JSON.stringify({ ...sessionStorage }));
  expect(stored).not.toContain(SECRET);
  await page.clock.fastForward(30_000);
  await expect(page.getByTestId('secret-api-token-left')).toHaveText(/^(29|30)s$/);
  await page.clock.fastForward(29_000);
  await expect(val).toHaveText(SECRET);
  await page.clock.fastForward(2_000);
  await expect(val).toHaveText('•••');
  await expect(page.getByTestId('secret-api-token-len')).toHaveText(`${SECRET.length} chars`);
  // the reveal is in the activity log (client-side navigation keeps the mock state)
  await nav(page, 'activities');
  await expect(page.getByTestId('activities')).toContainText('secret.revealed');
  await expect(page.getByTestId('activities')).toContainText('Secret api-token revealed');
  await expect(page.getByTestId('activities')).not.toContainText(SECRET);
});

test('RevealValue: re-masks on route change and when the tab is hidden', async ({ page }) => {
  await login(page, '/_opm/app/secrets');
  const val = page.getByTestId('secret-api-token-value');
  await page.getByTestId('secret-api-token-view').click();
  await expect(val).toHaveText(SECRET);
  await page.getByRole('link', { name: 'Variables' }).click();
  await page.waitForURL(/\/app\/variables/);
  await page.getByRole('link', { name: 'Secrets' }).click();
  await page.waitForURL(/\/app\/secrets/);
  await expect(page.getByTestId('secret-api-token-value')).toHaveText('•••');
  await expect(page.getByText(SECRET)).toHaveCount(0);
  // tab hidden
  await page.getByTestId('secret-api-token-view').click();
  await expect(val).toHaveText(SECRET);
  await page.evaluate(() => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' });
    document.dispatchEvent(new Event('visibilitychange'));
  });
  await expect(val).toHaveText('•••');
});

test('RevealValue: 30 reveals per session per minute → rate-limited', async ({ page }) => {
  await page.clock.install({ time: new Date('2026-10-08T10:00:02Z') }); // start of a fixed minute
  await login(page, '/_opm/app/secrets');
  // 30 reveals in this minute are fine, the 31st is refused
  await page.getByTestId('secret-api-token-view').click();
  await page.getByTestId('secret-api-token-hide').click();
  for (let i = 0; i < 29; i++) {
    await page.getByTestId('secret-api-token-view').click();
    await expect(page.getByTestId('secret-api-token-hide')).toBeVisible();
    await page.getByTestId('secret-api-token-hide').click();
  }
  await page.getByTestId('secret-api-token-view').click();
  await expect(page.getByText(/too many reveals/i)).toBeVisible();
  await expect(page.getByTestId('secret-api-token-value')).toHaveText('•••');
  await page.clock.fastForward(61_000);
  await page.getByTestId('secret-api-token-view').click();
  await expect(page.getByTestId('secret-api-token-value')).toHaveText(SECRET);
});

test('login: the access key help points to the cloud Setup page (View any time)', async ({ page }) => {
  await page.goto('/_opm/login');
  const help = page.getByTestId('key-help');
  await expect(help).toContainText('cloud Setup page');
  await expect(help).toContainText('View');
  expect(KEY).toBeTruthy();
});

function walk(dir: string, out: string[] = []): string[] {
  for (const n of readdirSync(dir)) {
    const p = join(dir, n);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (/\.(js|css|html|json|txt|svg)$/.test(n)) out.push(p);
  }
  return out;
}

test('bundle: no "shown once" wording in the built GUI (agent/internal/webui/dist + mock build)', async () => {
  const BAD = /shown once|only once|one-time|I saved/i;
  const dirs = [join(GUI, '../internal/webui/dist'), join(GUI, 'build-mock')].filter((d) => existsSync(d));
  expect(dirs.length, 'no built bundle found; run npm run build first').toBeGreaterThan(0);
  const hits: string[] = [];
  for (const d of dirs)
    for (const f of walk(d)) {
      const m = readFileSync(f, 'utf8').match(BAD);
      if (m) hits.push(`${f}: ${m[0]}`);
    }
  expect(hits).toEqual([]);
});
